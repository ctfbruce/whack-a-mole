package main

import (
	"bytes"
	"context"
	"crypto/tls"
	"crypto/x509"
	"encoding/json"
	"errors"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"net/netip"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"
)

// The fake replaces provisioning only. Requests still cross the real TLS and
// HTTP servers, with IP-name and certificate-chain verification enabled.
type testRuntime struct {
	mu            sync.Mutex
	entries       map[string]EntryRequest
	fail          bool
	calls, failAt int
}

func (r *testRuntime) Start(_ context.Context, e EntryRequest) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.calls++
	if r.fail || r.calls == r.failAt {
		return errors.New("unavailable")
	}
	r.entries[e.ID] = e
	return nil
}

func TestPartialConversationIsRemoved(t *testing.T) {
	c, _, rt, _, _ := fixture(t, 30*time.Second)
	rt.failAt = 2
	server := httptest.NewServer(c.admin("admin-test-token"))
	defer server.Close()
	if code := request(t, server.Client(), "POST", server.URL+"/conversations", "admin-test-token", map[string]any{"names": []string{"A", "B"}}, nil); code != 503 {
		t.Fatal(code)
	}
	if len(c.hops) != 0 || len(rt.entries) != 0 || len(c.store.rooms) != 1 {
		t.Fatal("failed creation retained partial entries or conversation")
	}
}

func TestStateLimitPreservesReloadableFile(t *testing.T) {
	c, _, _, _, _ := fixture(t, 30*time.Second)
	r := Room{People: []Person{{ID: "A", Name: "A"}, {ID: "B", Name: "B"}}}
	// Valid 4 KiB messages can expand sixfold when JSON escapes control bytes.
	for i := 0; i < 100; i++ {
		r.Messages = append(r.Messages, Message{ID: "id", Sender: "A", Text: strings.Repeat("\x01", 4096)})
	}
	for _, id := range []string{"one", "two", "three"} {
		if err := c.store.save(id, &r); err != nil {
			t.Fatal(err)
		}
	}
	if err := c.store.save("too-large", &r); err == nil {
		t.Fatal("oversized state acknowledged")
	}
	loaded, err := openStore(c.store.path)
	if err != nil {
		t.Fatal(err)
	}
	if len(loaded.rooms) != 4 || len(c.store.rooms) != 4 {
		t.Fatal("failed save changed state")
	}
}

type delayedRuntime struct{ Runtime }

func (r delayedRuntime) Start(ctx context.Context, e EntryRequest) error {
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-time.After(16 * time.Second):
		return r.Runtime.Start(ctx, e)
	}
}
func TestSlowSuccessorProvisioning(t *testing.T) {
	c, client, _, id, room := fixture(t, 40*time.Second)
	inv := issue(t, c, id, room.People[0])
	token := login(t, client, inv)
	c.runtime = delayedRuntime{c.runtime}
	client.Timeout = 30 * time.Second
	var next Invitation
	if code := request(t, client, "POST", inv.URL+"/api/next", token, nil, &next); code != 200 || next.Passphrase == "" {
		t.Fatalf("slow successor: %d", code)
	}
	login(t, client, next)
}

func TestAdminEndpointsRemainLocalAndBearerOnly(t *testing.T) {
	for _, input := range []string{"http://example.org:9200", "http://127.0.0.1.evil:9200", "http://user@127.0.0.1:9200", "http://127.0.0.1:9200/path", "http://127.0.0.1:9200?q=1"} {
		if loopbackURL(input) {
			t.Fatalf("accepted nonlocal admin URL: %s", input)
		}
	}
	if !loopbackURL("http://[::1]:9200") || !loopbackURL("http://127.0.0.1:9200") {
		t.Fatal("loopback rejected")
	}
	c, _, _, _, _ := fixture(t, 30*time.Second)
	r := httptest.NewRequest("POST", "/conversations", strings.NewReader(`{"names":["A","B"]}`))
	r.Header.Set("Authorization", "token-without-bearer")
	w := httptest.NewRecorder()
	c.admin("token-without-bearer").ServeHTTP(w, r)
	if w.Code != 401 {
		t.Fatal("non-Bearer credential accepted")
	}
}
func (r *testRuntime) Stop(_ context.Context, id string) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	delete(r.entries, id)
	return nil
}
func fixture(t *testing.T, ttl time.Duration) (*Core, *http.Client, *testRuntime, string, Room) {
	t.Helper()
	dir := t.TempDir()
	a, err := authority(dir)
	if err != nil {
		t.Fatal(err)
	}
	s, err := openStore(filepath.Join(dir, "mailboxes.json"))
	if err != nil {
		t.Fatal(err)
	}
	r := &testRuntime{entries: make(map[string]EntryRequest)}
	c := &Core{store: s, ca: a, runtime: r, prefix: netip.MustParsePrefix("fd6d:626f:7800:1::/64"), ttl: ttl, hops: make(map[string]*Hop)}
	roots := x509.NewCertPool()
	roots.AddCert(a.cert)
	transport := &http.Transport{TLSClientConfig: &tls.Config{RootCAs: roots, MinVersion: tls.VersionTLS13}, DialContext: func(ctx context.Context, network, address string) (net.Conn, error) {
		ip, _, _ := net.SplitHostPort(address)
		r.mu.Lock()
		backend := ""
		for _, e := range r.entries {
			if e.Address == ip {
				backend = e.Backend
				break
			}
		}
		r.mu.Unlock()
		if backend == "" {
			return nil, errors.New("entry retired")
		}
		return (&net.Dialer{}).DialContext(ctx, "tcp6", backend)
	}}
	client := &http.Client{Transport: transport, Timeout: 5 * time.Second}
	t.Cleanup(func() { c.close(); transport.CloseIdleConnections() })
	id, room, err := s.create([]string{"Source", "Reporter"})
	if err != nil {
		t.Fatal(err)
	}
	return c, client, r, id, room
}
func request(t *testing.T, client *http.Client, method, url, token string, input, output any) int {
	t.Helper()
	var b bytes.Buffer
	if input != nil {
		if err := json.NewEncoder(&b).Encode(input); err != nil {
			t.Fatal(err)
		}
	}
	req, err := http.NewRequest(method, url, &b)
	if err != nil {
		t.Fatal(err)
	}
	if token != "" {
		req.Header.Set("Authorization", "Bearer "+token)
	}
	resp, err := client.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if output != nil {
		if err := json.NewDecoder(resp.Body).Decode(output); err != nil {
			t.Fatal(err)
		}
	} else {
		_, _ = io.Copy(io.Discard, resp.Body)
	}
	return resp.StatusCode
}
func login(t *testing.T, client *http.Client, inv Invitation) string {
	t.Helper()
	var out struct {
		Token string `json:"token"`
	}
	if code := request(t, client, "POST", inv.URL+"/api/login", "", map[string]string{"passphrase": inv.Passphrase}, &out); code != 200 || out.Token == "" {
		t.Fatalf("login: %d", code)
	}
	return out.Token
}
func issue(t *testing.T, c *Core, id string, p Person) Invitation {
	t.Helper()
	inv, err := c.issue(context.Background(), id, p)
	if err != nil {
		t.Fatal(err)
	}
	return inv
}

func TestConversationIsolationAndHandoff(t *testing.T) {
	c, client, rt, id, room := fixture(t, 30*time.Second)
	a, b := issue(t, c, id, room.People[0]), issue(t, c, id, room.People[1])
	if code := request(t, client, "POST", a.URL+"/api/login", "", map[string]string{"passphrase": "wrong"}, nil); code != 401 {
		t.Fatal(code)
	}
	ta, tb := login(t, client, a), login(t, client, b)
	if code := request(t, client, "GET", b.URL+"/api/messages", ta, nil, nil); code != 401 {
		t.Fatalf("cross-entry token accepted: %d", code)
	}
	if code := request(t, client, "POST", a.URL+"/api/messages", ta, map[string]string{"text": "one confidential message"}, nil); code != 201 {
		t.Fatal(code)
	}
	var messages struct {
		Messages []Message `json:"messages"`
	}
	request(t, client, "GET", b.URL+"/api/messages", tb, nil, &messages)
	if len(messages.Messages) != 1 || messages.Messages[0].Text != "one confidential message" {
		t.Fatalf("delivery: %+v", messages)
	}
	otherID, other, err := c.store.create([]string{"Elsewhere", "Reader"})
	if err != nil {
		t.Fatal(err)
	}
	otherInv := issue(t, c, otherID, other.People[0])
	otherToken := login(t, client, otherInv)
	request(t, client, "GET", otherInv.URL+"/api/messages", otherToken, nil, &messages)
	if len(messages.Messages) != 0 {
		t.Fatal("cross-conversation leak")
	}
	var next, again Invitation
	request(t, client, "POST", a.URL+"/api/next", ta, nil, &next)
	request(t, client, "POST", a.URL+"/api/next", ta, nil, &again)
	if next.URL == a.URL || next != again || next.Passphrase == "" {
		t.Fatal("handoff not distinct and idempotent")
	}
	nextToken := login(t, client, next)
	request(t, client, "GET", next.URL+"/api/messages", nextToken, nil, &messages)
	if len(messages.Messages) != 1 {
		t.Fatal("history not carried to next entry")
	}
	if code := request(t, client, "POST", a.URL+"/api/close", ta, nil, nil); code != 204 {
		t.Fatal(code)
	}
	deadline := time.Now().Add(time.Second)
	for {
		rt.mu.Lock()
		exists := false
		for _, e := range rt.entries {
			if strings.Contains(a.URL, e.Address) {
				exists = true
			}
		}
		rt.mu.Unlock()
		if !exists {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("old entry retained")
		}
		time.Sleep(time.Millisecond)
	}
	if _, err := client.Get(a.URL); err == nil {
		t.Fatal("closed entry still reachable")
	}
	loaded, err := openStore(c.store.path)
	if err != nil {
		t.Fatal(err)
	}
	if len(loaded.messages(id)) != 1 {
		t.Fatal("messages not persisted")
	}
}

func TestExpiryClosesEstablishedConnectionAndRuntime(t *testing.T) {
	c, client, rt, id, room := fixture(t, 3*time.Second)
	inv := issue(t, c, id, room.People[0])
	login(t, client, inv)
	rt.mu.Lock()
	var entry EntryRequest
	for _, e := range rt.entries {
		entry = e
	}
	rt.mu.Unlock()
	roots := x509.NewCertPool()
	roots.AddCert(c.ca.cert)
	conn, err := tls.Dial("tcp6", entry.Backend, &tls.Config{RootCAs: roots, ServerName: entry.Address})
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close()
	_ = conn.SetReadDeadline(time.Now().Add(5 * time.Second))
	_, err = conn.Read(make([]byte, 1))
	if err == nil {
		t.Fatal("expected server close")
	}
	if e, ok := err.(net.Error); ok && e.Timeout() {
		t.Fatal("expiry did not close established connection")
	}
	deadline := time.Now().Add(time.Second)
	for {
		rt.mu.Lock()
		count := len(rt.entries)
		rt.mu.Unlock()
		if count == 0 {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("runtime survived expiry")
		}
		time.Sleep(time.Millisecond)
	}
}

func TestProvisionFailureAndTLSVerification(t *testing.T) {
	c, client, rt, id, room := fixture(t, 30*time.Second)
	rt.fail = true
	if _, err := c.issue(context.Background(), id, room.People[0]); err == nil {
		t.Fatal("provision failure ignored")
	}
	if len(c.hops) != 0 {
		t.Fatal("failed entry leaked")
	}
	rt.fail = false
	inv := issue(t, c, id, room.People[0])
	transport := client.Transport.(*http.Transport).Clone()
	transport.TLSClientConfig.RootCAs = x509.NewCertPool()
	defer transport.CloseIdleConnections()
	if _, err := (&http.Client{Transport: transport, Timeout: time.Second}).Get(inv.URL); err == nil {
		t.Fatal("untrusted certificate accepted")
	}
	transport2 := client.Transport.(*http.Transport).Clone()
	transport2.TLSClientConfig.ServerName = "wrong.example"
	defer transport2.CloseIdleConnections()
	if _, err := (&http.Client{Transport: transport2, Timeout: time.Second}).Get(inv.URL); err == nil {
		t.Fatal("wrong TLS name accepted")
	}
	login(t, client, inv)
}

func TestMessageBoundsAndFailedStorage(t *testing.T) {
	c, client, _, id, room := fixture(t, 30*time.Second)
	inv := issue(t, c, id, room.People[0])
	token := login(t, client, inv)
	if code := request(t, client, "POST", inv.URL+"/api/messages", token, map[string]string{"text": strings.Repeat("a", 4097)}, nil); code != 400 {
		t.Fatal(code)
	}
	if len(c.store.messages(id)) != 0 {
		t.Fatal("oversize message persisted")
	}
	c.store.path = filepath.Join(t.TempDir(), "missing", "state.json")
	if _, err := c.store.add(id, room.People[0], "not acknowledged"); err == nil {
		t.Fatal("storage error ignored")
	}
	if len(c.store.messages(id)) != 0 {
		t.Fatal("failed write made message visible")
	}
}
