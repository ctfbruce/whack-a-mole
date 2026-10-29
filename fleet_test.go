package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"net/netip"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

type cleanupFailure struct {
	Runtime
	fail bool
}

func (r *cleanupFailure) Stop(ctx context.Context, id string) error {
	if r.fail {
		return errors.New("controller unavailable")
	}
	return r.Runtime.Stop(ctx, id)
}

func TestFailedCleanupRetainsFleetSlotAndRoute(t *testing.T) {
	c, _, rt, roomID, room := fixture(t, 30*time.Second)
	ip := netip.MustParseAddr("192.0.2.1")
	c.fleet = []Entrance{{Name: "one", URL: "https://192.0.2.1:8443", Backend: "[::1]:0", ip: ip}}
	inv := issue(t, c, roomID, room.People[0])
	var hop *Hop
	for _, h := range c.hops {
		if h.Invitation.URL == inv.URL {
			hop = h
		}
	}
	failure := &cleanupFailure{Runtime: rt, fail: true}
	c.runtime = failure
	c.retire(hop, "explicit")
	if len(c.hops) != 1 || len(rt.entries) != 1 {
		t.Fatal("failed cleanup released live slot")
	}
	if _, err := c.issue(context.Background(), roomID, room.People[0]); err == nil {
		t.Fatal("failed cleanup slot was reused")
	}
	failure.fail = false
	c.retire(hop, "explicit")
	if len(c.hops) != 0 || len(rt.entries) != 0 {
		t.Fatal("cleanup retry did not retire slot")
	}
	issue(t, c, roomID, room.People[0])

	deletes := 0
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		deletes++
		if deletes == 1 {
			w.WriteHeader(502)
		} else {
			w.WriteHeader(204)
		}
	}))
	defer server.Close()
	node := &RemoteRuntime{URL: server.URL, Client: server.Client()}
	fleet := &fleetRuntime{runs: map[string]*RemoteRuntime{"run": node}}
	if err := fleet.Stop(context.Background(), "run"); err == nil {
		t.Fatal("failed controller cancellation accepted")
	}
	if fleet.runs["run"] != node {
		t.Fatal("failed cancellation discarded controller route")
	}
	if err := fleet.Stop(context.Background(), "run"); err != nil {
		t.Fatal(err)
	}
	if deletes != 2 || len(fleet.runs) != 0 {
		t.Fatal("retry did not reach original controller")
	}
}

func TestFleetRotationAndPrivateEvents(t *testing.T) {
	c, client, _, id, room := fixture(t, 30*time.Second)
	var events bytes.Buffer
	c.events = &events
	for i := 1; i <= 3; i++ {
		ip := netip.MustParseAddr(fmt.Sprintf("192.0.2.%d", i))
		c.fleet = append(c.fleet, Entrance{Name: fmt.Sprintf("node-%d", i), URL: "https://" + ip.String() + ":8443", Backend: "[::1]:0", ip: ip})
	}
	a, b, d := issue(t, c, id, room.People[0]), issue(t, c, id, room.People[1]), issue(t, c, id, room.People[0])
	if a.URL == b.URL || b.URL == d.URL || a.URL == d.URL {
		t.Fatal("fleet reused occupied node")
	}
	if _, err := c.issue(context.Background(), id, room.People[0]); err == nil {
		t.Fatal("occupied fleet accepted another entry")
	}
	token := login(t, client, a)
	if code := request(t, client, "POST", a.URL+"/api/messages", token, map[string]string{"text": "private-message-never-in-events"}, nil); code != 201 {
		t.Fatal(code)
	}
	var old *Hop
	for _, h := range c.hops {
		if h.Invitation.URL == a.URL {
			old = h
		}
	}
	c.retire(old, "explicit")
	next := issue(t, c, id, room.People[0])
	if next.URL != a.URL || next.Passphrase == a.Passphrase {
		t.Fatal("released slot not correctly reissued")
	}
	if code := request(t, client, "POST", next.URL+"/api/login", "", map[string]string{"passphrase": a.Passphrase}, nil); code != 401 {
		t.Fatal("old credential survived node reuse")
	}
	c.eventsMu.Lock()
	log := events.String()
	c.eventsMu.Unlock()
	if strings.Contains(log, a.Passphrase) || strings.Contains(log, token) || strings.Contains(log, "private-message-never-in-events") {
		t.Fatal("secret or message in events")
	}
	for _, kind := range []string{"entry_ready", "login", "message_stored", "entry_closed"} {
		if !strings.Contains(log, kind) {
			t.Fatal("missing event", kind)
		}
	}
}

func TestCoverIsAuthenticatedBoundedAndDoesNotStoreMessages(t *testing.T) {
	c, client, _, id, room := fixture(t, 30*time.Second)
	inv := issue(t, c, id, room.People[0])
	token := login(t, client, inv)
	input := map[string]any{"response_bytes": 1024, "padding": "synthetic traffic"}
	if code := request(t, client, "POST", inv.URL+"/api/cover", "", input, nil); code != 401 {
		t.Fatal(code)
	}
	if code := request(t, client, "POST", inv.URL+"/api/cover", token, map[string]int{"response_bytes": 262145}, nil); code != 400 {
		t.Fatal(code)
	}
	for i := 0; i < 5; i++ {
		if code := request(t, client, "POST", inv.URL+"/api/cover", token, input, nil); code != 200 {
			t.Fatal(code)
		}
	}
	if code := request(t, client, "POST", inv.URL+"/api/cover", token, input, nil); code != 429 {
		t.Fatal("rate bound", code)
	}
	if len(c.store.messages(id)) != 0 {
		t.Fatal("cover wrote mailbox state")
	}
}

func TestFleetRejectsSharedIPsAndRoutesCancellation(t *testing.T) {
	dir := t.TempDir()
	tokenPath := filepath.Join(dir, "node.token")
	os.WriteFile(tokenPath, []byte(strings.Repeat("a", 32)), 0600)
	requests := make(chan string, 4)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "Bearer "+strings.Repeat("a", 32) {
			t.Error("missing controller auth")
		}
		requests <- r.Method
		w.WriteHeader(204)
	}))
	defer server.Close()
	nodes := []Entrance{{Name: "one", URL: "https://192.0.2.1:8443", Backend: "[::1]:29301", RuntimeURL: server.URL, TokenFile: tokenPath}, {Name: "two", URL: "https://192.0.2.2:8443", Backend: "[::1]:29302", RuntimeURL: server.URL, TokenFile: tokenPath}}
	path := filepath.Join(dir, "fleet.json")
	save := func() { b, _ := json.Marshal(nodes); os.WriteFile(path, b, 0600) }
	save()
	_, rt, err := loadFleet(path)
	if err != nil {
		t.Fatal(err)
	}
	if err := rt.Start(context.Background(), EntryRequest{ID: "run", Address: "192.0.2.2"}); err != nil {
		t.Fatal(err)
	}
	if err := rt.Stop(context.Background(), "run"); err != nil {
		t.Fatal(err)
	}
	if <-requests != "POST" || <-requests != "DELETE" {
		t.Fatal("wrong routing")
	}
	nodes[1].URL = "https://192.0.2.1:9999"
	save()
	if _, _, err := loadFleet(path); err == nil {
		t.Fatal("two ports on one IP accepted as distinct hosts")
	}
	nodes[1].URL = "https://192.0.2.2:8443"
	nodes[1].RuntimeURL = "http://192.0.2.3:9100"
	save()
	if _, _, err := loadFleet(path); err == nil {
		t.Fatal("unencrypted remote control allowed")
	}
}
