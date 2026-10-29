package main

import (
	"bytes"
	"encoding/json"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestWelcomeEnrollsThroughCoreWithoutExposingOperatorInvitation(t *testing.T) {
	c, client, _, _, _ := fixture(t, 30*time.Second)
	dir := filepath.Dir(c.store.path)
	token := strings.Repeat("x", 32)
	if err := os.WriteFile(filepath.Join(dir, "admin.token"), []byte(token), 0600); err != nil {
		t.Fatal(err)
	}
	admin := httptest.NewServer(c.admin(token))
	defer admin.Close()
	h, err := welcomeHandler(dir, admin.URL, "https://welcome.example")
	if err != nil {
		t.Fatal(err)
	}
	call := func(method, path, origin string) *httptest.ResponseRecorder {
		r := httptest.NewRequest(method, "https://welcome.example"+path, strings.NewReader(`{}`))
		r.Header.Set("Origin", origin)
		w := httptest.NewRecorder()
		h.ServeHTTP(w, r)
		return w
	}
	if w := call("POST", "/api/enroll", "https://elsewhere.example"); w.Code != 403 {
		t.Fatal(w.Code)
	}
	if w := call("POST", "/conversations", "https://welcome.example"); w.Code != 404 {
		t.Fatal("public admin route", w.Code)
	}
	if len(c.hops) != 0 {
		t.Fatal("refused request provisioned an entrance")
	}
	w := call("POST", "/api/enroll", "https://welcome.example")
	if w.Code != 201 {
		t.Fatalf("enrollment failed: %d %s", w.Code, w.Body.String())
	}
	if w.Header().Get("Cache-Control") != "no-store" {
		t.Fatal("credential response is cacheable")
	}
	var inv Invitation
	if err := json.Unmarshal(w.Body.Bytes(), &inv); err != nil {
		t.Fatal(err)
	}
	if inv.Passphrase == "" || inv.URL == "" || len(c.hops) != 2 {
		t.Fatal("enrollment did not issue usable entries")
	}
	files, err := os.ReadDir(filepath.Join(dir, "enrollments"))
	if err != nil || len(files) != 1 {
		t.Fatal("private operator record missing", err)
	}
	path := filepath.Join(dir, "enrollments", files[0].Name())
	record, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	var pair Invitations
	if err := json.Unmarshal(record, &pair); err != nil {
		t.Fatal(err)
	}
	if len(pair.Invitations) != 2 || pair.Invitations[1].Passphrase == "" {
		t.Fatal("operator invitation missing")
	}
	if bytes.Contains(w.Body.Bytes(), []byte(pair.Invitations[1].Passphrase)) || bytes.Contains(w.Body.Bytes(), []byte(token)) {
		t.Fatal("operator credential exposed")
	}
	info, err := os.Stat(path)
	if err != nil || info.Mode().Perm() != 0600 {
		t.Fatal("operator record permissions")
	}
	access := login(t, client, inv)
	if code := request(t, client, "POST", inv.URL+"/api/messages", access, map[string]string{"text": "First use works"}, nil); code != 201 {
		t.Fatal(code)
	}
	if got := c.store.messages(pair.Conversation); len(got) != 1 || got[0].Text != "First use works" {
		t.Fatal("enrolled visitor could not message the core")
	}
}
