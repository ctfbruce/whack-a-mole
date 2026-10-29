package main

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func operatorRequest(h http.Handler, method, path, token, host, origin, body string) *httptest.ResponseRecorder {
	r := httptest.NewRequest(method, "http://127.0.0.1:19200"+path, strings.NewReader(body))
	r.Host = host
	r.Header.Set("Origin", origin)
	if token != "" {
		r.Header.Set("Authorization", "Bearer "+token)
	}
	w := httptest.NewRecorder()
	h.ServeHTTP(w, r)
	return w
}

func TestOperatorBoundaryAndReplyDelivery(t *testing.T) {
	c, client, _, id, room := fixture(t, 30*time.Second)
	inv := issue(t, c, id, room.People[0])
	visitorToken := login(t, client, inv)
	h := c.operator("operator-secret")
	for _, tc := range []struct {
		token, host string
		want        int
	}{
		{"", "127.0.0.1:19200", 401},
		{"admin-secret", "127.0.0.1:19200", 401},
		{visitorToken, "127.0.0.1:19200", 401},
		{"operator-secret", "localhost:19200", 403},
		{"operator-secret", "evil.example:19200", 403},
		{"operator-secret", "192.0.2.1:19200", 403},
		{"operator-secret", "127.0.0.1:19400", 200},
		{"operator-secret", "[::1]:19400", 200},
	} {
		if w := operatorRequest(h, "GET", "/operator/api/state", tc.token, tc.host, "", ""); w.Code != tc.want {
			t.Fatalf("operator boundary %q %d, want %d", tc.host, w.Code, tc.want)
		}
	}
	body, _ := json.Marshal(map[string]string{"conversation": id, "text": "Hello from the operator"})
	for _, origin := range []string{"", "http://evil.example", "https://127.0.0.1:19200", "http://127.0.0.1:19201"} {
		if w := operatorRequest(h, "POST", "/operator/api/reply", "operator-secret", "127.0.0.1:19200", origin, string(body)); w.Code != 403 {
			t.Fatal("wrong origin accepted", w.Code)
		}
	}
	if w := operatorRequest(c.admin("admin-secret"), "POST", "/conversations", "operator-secret", "127.0.0.1:19200", "http://127.0.0.1:19200", `{"names":["A","B"]}`); w.Code != 401 {
		t.Fatal("operator credential gained provisioning access")
	}
	w := operatorRequest(h, "POST", "/operator/api/reply", "operator-secret", "127.0.0.1:19200", "http://127.0.0.1:19200", string(body))
	if w.Code != 201 || w.Header().Get("Cache-Control") != "no-store" {
		t.Fatal("reply failed", w.Code)
	}
	var result struct {
		Messages []Message `json:"messages"`
	}
	if status := request(t, client, "GET", inv.URL+"/api/messages", visitorToken, nil, &result); status != 200 || len(result.Messages) != 1 || result.Messages[0].Sender != "Core operator" || result.Messages[0].Text != "Hello from the operator" {
		t.Fatalf("visitor did not receive operator reply: %d %+v", status, result)
	}
	loaded, err := openStore(c.store.path)
	if err != nil || len(loaded.messages(id)) != 1 {
		t.Fatal("reply not persisted", err)
	}
	w = operatorRequest(h, "GET", "/operator/api/state", "operator-secret", "127.0.0.1:19200", "", "")
	if w.Header().Get("Access-Control-Allow-Origin") != "" || !strings.Contains(w.Header().Get("Content-Security-Policy"), "frame-ancestors 'none'") {
		t.Fatal("unsafe response headers")
	}
	for _, secret := range []string{"operator-secret", "admin-secret", visitorToken, inv.Passphrase, "PRIVATE KEY", "passphrase", "token"} {
		if strings.Contains(w.Body.String(), secret) {
			t.Fatalf("snapshot exposed credential %q", secret)
		}
	}
	var state operatorState
	if err := json.Unmarshal(w.Body.Bytes(), &state); err != nil {
		t.Fatal(err)
	}
	if len(state.Events) == 0 || state.Events[len(state.Events)-1].Event != "operator_reply" || strings.Contains(string(mustJSON(t, state.Events)), "Hello from the operator") {
		t.Fatal("reply event missing or logged body")
	}
}

func mustJSON(t *testing.T, value any) []byte {
	t.Helper()
	b, err := json.Marshal(value)
	if err != nil {
		t.Fatal(err)
	}
	return b
}

func TestOperatorReplyFailureIsNotAcknowledged(t *testing.T) {
	c, _, _, id, _ := fixture(t, 30*time.Second)
	h := c.operator("operator-secret")
	call := func(body string) *httptest.ResponseRecorder {
		return operatorRequest(h, "POST", "/operator/api/reply", "operator-secret", "127.0.0.1:19200", "http://127.0.0.1:19200", body)
	}
	for _, tc := range []struct {
		body string
		want int
	}{
		{`{"conversation":"missing","text":"hello"}`, 404},
		{string(mustJSON(t, map[string]string{"conversation": id, "text": "", "sender": "Visitor"})), 400},
		{string(mustJSON(t, map[string]string{"conversation": id, "text": " "})), 400},
		{string(mustJSON(t, map[string]string{"conversation": id, "text": strings.Repeat("x", 4097)})), 400},
	} {
		if w := call(tc.body); w.Code != tc.want {
			t.Fatalf("invalid reply: %d want %d", w.Code, tc.want)
		}
	}
	c.store.path = filepath.Join(t.TempDir(), "missing", "mailboxes.json")
	if w := call(string(mustJSON(t, map[string]string{"conversation": id, "text": "Must not appear"}))); w.Code != 503 {
		t.Fatal("failed storage acknowledged", w.Code)
	}
	if len(c.store.messages(id)) != 0 || len(c.operatorState().Events) != 0 {
		t.Fatal("failed reply changed state")
	}
}

func TestOperatorSnapshotDoesNotWaitForProvisioning(t *testing.T) {
	c, _, _, id, room := fixture(t, 30*time.Second)
	inv := issue(t, c, id, room.People[0])
	c.mu.Lock()
	var hop *Hop
	for _, h := range c.hops {
		hop = h
	}
	c.mu.Unlock()
	c.fleet = []Entrance{{Name: "spare", URL: "https://192.0.2.2:8443"}, {Name: "used", URL: inv.URL}}
	hop.mu.Lock()
	done := make(chan operatorState, 1)
	go func() { done <- c.operatorState() }()
	var state operatorState
	select {
	case state = <-done:
	case <-time.After(time.Second):
		hop.mu.Unlock()
		t.Fatal("snapshot blocked on a provisioning lock")
	}
	hop.mu.Unlock()
	if len(state.Relays) != 2 || state.Relays[0].Status != "free" || state.Relays[1].Status != "preparing" || state.Relays[1].Conversation != id {
		t.Fatalf("incorrect fleet snapshot: %+v", state.Relays)
	}
	state = c.operatorState()
	if state.Relays[1].Status != "active" {
		t.Fatal("ready relay not active")
	}
	hop.mu.Lock()
	hop.closed = true
	hop.mu.Unlock()
	if state = c.operatorState(); state.Relays[1].Status != "closing" {
		t.Fatal("cleanup-pending relay advertised free")
	}
	// Restore the fixture so its normal shutdown closes the TLS listener.
	hop.mu.Lock()
	hop.closed = false
	hop.mu.Unlock()
}

func TestOperatorEventsAreBoundedWithoutFileLogging(t *testing.T) {
	c, _, _, id, _ := fixture(t, 30*time.Second)
	for i := 0; i < 150; i++ {
		c.recordEvent(Event{Event: "test", Conversation: id, Bytes: i})
	}
	s := c.operatorState()
	if len(s.Events) != 100 || s.Events[0].Bytes != 50 || s.Events[99].Bytes != 149 {
		t.Fatal("event history not bounded")
	}
	for i := 1; i < len(s.Events); i++ {
		if s.Events[i].Time.Before(s.Events[i-1].Time) {
			t.Fatal("events not chronological")
		}
	}
	var log bytes.Buffer
	c.events = &log
	c.recordEvent(Event{Event: "logged"})
	if !strings.Contains(log.String(), `"event":"logged"`) {
		t.Fatal("optional file logging lost")
	}
}
