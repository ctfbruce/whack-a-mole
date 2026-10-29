package main

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

func TestAdmissionDoesNotTurnControllerIntoArbitraryProxy(t *testing.T) {
	good := request{ID: strings.Repeat("a", 32), Address: "fd6d:626f:7800:1::abcd", Backend: "[::1]:9453", TTL: 30}
	if !valid(good) {
		t.Fatal("valid local session rejected")
	}
	for _, mutate := range []func(*request){
		func(q *request) { q.Address = "2001:db8::1" },
		func(q *request) { q.Address = "127.0.0.1" },
		func(q *request) { q.Backend = "[fd6d:626f:7800:1::2]:9000" },
		func(q *request) { q.Backend = "example.com:443" },
		func(q *request) { q.Backend = "[::1]:0" },
		func(q *request) { q.ID = "../state" },
		func(q *request) { q.TTL = 1201 },
		func(q *request) { q.TTL = 0 },
	} {
		bad := good
		mutate(&bad)
		if valid(bad) {
			t.Fatalf("accepted invalid controller request: %#v", bad)
		}
	}
}
func TestControllerRejectsUnauthenticatedMutation(t *testing.T) {
	n := &node{token: strings.Repeat("a", 32), sessions: map[string]*session{}}
	for _, method := range []string{http.MethodPost, http.MethodDelete} {
		r := httptest.NewRequest(method, "/sessions/abc", nil)
		w := httptest.NewRecorder()
		n.serve(w, r)
		if w.Code != http.StatusUnauthorized {
			t.Fatalf("%s returned %d", method, w.Code)
		}
	}
	r := httptest.NewRequest(http.MethodDelete, "/sessions/missing", nil)
	r.Header.Set("Authorization", "Bearer "+n.token)
	w := httptest.NewRecorder()
	n.serve(w, r)
	if w.Code != http.StatusNoContent {
		t.Fatalf("idempotent delete returned %d", w.Code)
	}
}

func TestFailedCancellationCannotReviveClosedEndpoint(t *testing.T) {
	q := request{ID: strings.Repeat("a", 32), Address: "fd6d:626f:7800:1::abcd", Backend: "[::1]:9453", TTL: 30}
	closed := &session{request: q, deadline: time.Now().Add(time.Minute), closed: true}
	n := &node{token: strings.Repeat("a", 32), sessions: map[string]*session{q.ID: closed}}
	body, _ := json.Marshal(q)
	r := httptest.NewRequest(http.MethodPost, "/sessions", bytes.NewReader(body))
	r.Header.Set("Authorization", "Bearer "+n.token)
	w := httptest.NewRecorder()
	n.serve(w, r)
	if w.Code != http.StatusConflict {
		t.Fatalf("closed relay returned %d", w.Code)
	}
}

func TestCloudControllerPinsAddressAndOneBackend(t *testing.T) {
	n := &node{}
	if err := n.configureCloud("https://203.0.113.20:18843", 29301); err != nil {
		t.Fatal(err)
	}
	good := request{ID: strings.Repeat("a", 32), Address: "203.0.113.20", Backend: "[::1]:29301", TTL: 30}
	if !n.accepts(good) {
		t.Fatal("fixed cloud endpoint rejected")
	}
	for _, mutate := range []func(*request){
		func(q *request) { q.Address = "203.0.113.21" },
		func(q *request) { q.Backend = "[::1]:29302" },
		func(q *request) { q.Backend = "127.0.0.1:29301" },
		func(q *request) { q.Backend = "example.org:443" },
	} {
		bad := good
		mutate(&bad)
		if n.accepts(bad) {
			t.Fatalf("accepted different endpoint: %+v", bad)
		}
	}
	for _, address := range []string{"http://203.0.113.20:8443", "https://example.org:8443", "https://127.0.0.1:8443", "https://203.0.113.20:8443/path", "https://user@203.0.113.20:8443", "https://203.0.113.20:8443#fragment"} {
		if (&node{}).configureCloud(address, 29301) == nil {
			t.Fatalf("accepted cloud endpoint %q", address)
		}
	}
}
