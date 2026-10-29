package main

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"crypto/subtle"
	"crypto/tls"
	"embed"
	"encoding/json"
	"errors"
	"io"
	"log"
	"net"
	"net/http"
	"net/netip"
	"strings"
	"sync"
	"time"
)

//go:embed web/*
var assets embed.FS

type Invitation struct {
	Name        string    `json:"name"`
	Participant string    `json:"participant"`
	URL         string    `json:"url"`
	Passphrase  string    `json:"passphrase"`
	ExpiresAt   time.Time `json:"expires_at"`
}
type Invitations struct {
	Conversation string       `json:"conversation"`
	Invitations  []Invitation `json:"invitations"`
}
type Hop struct {
	ID, Room    string
	Person      Person
	Invitation  Invitation
	pass, token [32]byte
	server      *http.Server
	mu          sync.Mutex
	nextMu      sync.Mutex
	next        *Invitation
	failures    int
	window      time.Time
	closed      bool
	timer       *time.Timer
}
type Core struct {
	store   *Store
	ca      *Authority
	runtime Runtime
	prefix  netip.Prefix
	ttl     time.Duration
	mu      sync.Mutex
	hops    map[string]*Hop
	closing bool
}

func reply(w http.ResponseWriter, status int, value any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	if value != nil {
		_ = json.NewEncoder(w).Encode(value)
	}
}
func problem(w http.ResponseWriter, status int, message string) {
	reply(w, status, map[string]string{"error": message})
}
func readJSON(w http.ResponseWriter, r *http.Request, value any) bool {
	r.Body = http.MaxBytesReader(w, r.Body, 8192)
	d := json.NewDecoder(r.Body)
	d.DisallowUnknownFields()
	if err := d.Decode(value); err != nil {
		problem(w, 400, "Invalid request")
		return false
	}
	if err := d.Decode(new(any)); err != io.EOF {
		problem(w, 400, "Invalid request")
		return false
	}
	return true
}
func sameSecret(value string, expected [32]byte) bool {
	got := sha256.Sum256([]byte(value))
	return subtle.ConstantTimeCompare(got[:], expected[:]) == 1
}
func bearer(r *http.Request, expected [32]byte) bool {
	header := r.Header.Get("Authorization")
	return strings.HasPrefix(header, "Bearer ") && sameSecret(strings.TrimPrefix(header, "Bearer "), expected)
}

func (c *Core) issue(ctx context.Context, room string, person Person) (Invitation, error) {
	c.mu.Lock()
	if c.closing || len(c.hops) >= 32 {
		c.mu.Unlock()
		return Invitation{}, errors.New("session capacity reached")
	}
	bits := c.prefix.Addr().As16()
	if _, err := rand.Read(bits[8:]); err != nil {
		c.mu.Unlock()
		return Invitation{}, err
	}
	ip := netip.AddrFrom16(bits)
	inv := Invitation{Name: person.Name, Participant: person.ID, URL: "https://" + net.JoinHostPort(ip.String(), "8443"), Passphrase: randomToken(), ExpiresAt: time.Now().Add(c.ttl).UTC()}
	h := &Hop{ID: randomToken()[:32], Room: room, Person: person, Invitation: inv, pass: sha256.Sum256([]byte(inv.Passphrase))}
	h.Invitation.Passphrase = ""
	h.mu.Lock()
	c.hops[h.ID] = h
	c.mu.Unlock()
	var err error
	defer func() {
		h.mu.Unlock()
		if err != nil {
			c.retire(h)
		}
	}()
	var cert tls.Certificate
	cert, err = c.ca.certificate(net.ParseIP(ip.String()), inv.ExpiresAt)
	if err != nil {
		return Invitation{}, err
	}
	var listener net.Listener
	listener, err = net.Listen("tcp6", "[::1]:0")
	if err != nil {
		return Invitation{}, err
	}
	h.server = &http.Server{Handler: http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { c.serveHop(h, w, r) }), ReadHeaderTimeout: 5 * time.Second, ReadTimeout: 10 * time.Second, WriteTimeout: 35 * time.Second, IdleTimeout: 3 * time.Second, MaxHeaderBytes: 8192, ErrorLog: log.New(io.Discard, "", 0)}
	h.server.SetKeepAlivesEnabled(false)
	go func() {
		_ = h.server.Serve(tls.NewListener(listener, &tls.Config{MinVersion: tls.VersionTLS13, Certificates: []tls.Certificate{cert}}))
	}()
	startCtx, cancel := context.WithTimeout(ctx, 25*time.Second)
	defer cancel()
	err = c.runtime.Start(startCtx, EntryRequest{ID: h.ID, Address: ip.String(), Backend: listener.Addr().String(), TTLSeconds: int(c.ttl.Seconds())})
	if err != nil {
		return Invitation{}, errors.New("entry could not be started")
	}
	if time.Now().After(inv.ExpiresAt) {
		err = errors.New("entry expired during startup")
		return Invitation{}, err
	}
	h.timer = time.AfterFunc(time.Until(inv.ExpiresAt), func() { c.retire(h) })
	return inv, nil
}

func (c *Core) retire(h *Hop) {
	h.mu.Lock()
	if h.closed {
		h.mu.Unlock()
		return
	}
	h.closed = true
	if h.timer != nil {
		h.timer.Stop()
	}
	server := h.server
	h.token = [32]byte{}
	h.pass = [32]byte{}
	h.Invitation.Passphrase = ""
	h.mu.Unlock()
	if server != nil {
		_ = server.Close()
	}
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	if err := c.runtime.Stop(ctx, h.ID); err != nil {
		log.Print("Entry cleanup failed; runtime lifetime remains bounded")
	}
	c.mu.Lock()
	delete(c.hops, h.ID)
	c.mu.Unlock()
}
func (c *Core) close() {
	c.mu.Lock()
	c.closing = true
	hops := make([]*Hop, 0, len(c.hops))
	for _, h := range c.hops {
		hops = append(hops, h)
	}
	c.mu.Unlock()
	var wg sync.WaitGroup
	for _, h := range hops {
		wg.Add(1)
		go func(h *Hop) { defer wg.Done(); c.retire(h) }(h)
	}
	wg.Wait()
}

func (c *Core) serveHop(h *Hop, w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Cache-Control", "no-store")
	w.Header().Set("Referrer-Policy", "no-referrer")
	w.Header().Set("X-Content-Type-Options", "nosniff")
	w.Header().Set("Content-Security-Policy", "default-src 'none'; script-src 'self'; style-src 'self'; connect-src 'self'; base-uri 'none'; frame-ancestors 'none'; form-action 'none'")
	w.Header().Set("Permissions-Policy", "camera=(), microphone=(), geolocation=()")
	if r.Host != strings.TrimPrefix(h.Invitation.URL, "https://") || (r.Header.Get("Origin") != "" && r.Header.Get("Origin") != h.Invitation.URL) {
		problem(w, 403, "Request refused")
		return
	}
	h.mu.Lock()
	expired := h.closed || !time.Now().Before(h.Invitation.ExpiresAt)
	h.mu.Unlock()
	if expired {
		problem(w, 410, "Session expired")
		return
	}
	if r.Method == "GET" {
		files := map[string]string{"/": "web/index.html", "/app.js": "web/app.js", "/style.css": "web/style.css"}
		if file, ok := files[r.URL.Path]; ok {
			b, err := assets.ReadFile(file)
			if err != nil {
				problem(w, 500, "Page unavailable")
				return
			}
			typeName := "text/html; charset=utf-8"
			if strings.HasSuffix(file, ".js") {
				typeName = "text/javascript; charset=utf-8"
			}
			if strings.HasSuffix(file, ".css") {
				typeName = "text/css; charset=utf-8"
			}
			w.Header().Set("Content-Type", typeName)
			_, _ = w.Write(b)
			return
		}
	}
	if r.Method == "POST" && r.URL.Path == "/api/login" {
		var input struct {
			Passphrase string `json:"passphrase"`
		}
		if !readJSON(w, r, &input) {
			return
		}
		h.mu.Lock()
		defer h.mu.Unlock()
		if time.Since(h.window) > time.Minute {
			h.window = time.Now()
			h.failures = 0
		}
		if h.failures >= 10 {
			problem(w, 429, "Too many attempts; wait a minute")
			return
		}
		if h.closed || !time.Now().Before(h.Invitation.ExpiresAt) || !sameSecret(input.Passphrase, h.pass) {
			h.failures++
			problem(w, 401, "Session unavailable or passphrase incorrect")
			return
		}
		token := randomToken()
		h.token = sha256.Sum256([]byte(token))
		reply(w, 200, map[string]any{"token": token, "name": h.Person.Name, "expires_at": h.Invitation.ExpiresAt})
		return
	}
	h.mu.Lock()
	authorized := !h.closed && time.Now().Before(h.Invitation.ExpiresAt) && h.token != [32]byte{} && bearer(r, h.token)
	h.mu.Unlock()
	if !authorized {
		problem(w, 401, "Sign in to this session")
		return
	}
	switch r.Method + " " + r.URL.Path {
	case "GET /api/messages":
		reply(w, 200, map[string]any{"messages": c.store.messages(h.Room)})
	case "POST /api/messages":
		var input struct {
			Text string `json:"text"`
		}
		if !readJSON(w, r, &input) {
			return
		}
		message, err := c.store.add(h.Room, h.Person, input.Text)
		if err != nil {
			if errors.Is(err, errMessage) {
				problem(w, 400, "Could not store message: conversation full or text invalid")
			} else {
				problem(w, 503, "Core storage unavailable; delivery is unconfirmed")
			}
			return
		}
		reply(w, 201, message)
	case "POST /api/next":
		h.nextMu.Lock()
		defer h.nextMu.Unlock()
		h.mu.Lock()
		closed := h.closed || !time.Now().Before(h.Invitation.ExpiresAt)
		h.mu.Unlock()
		if closed {
			problem(w, 410, "Session expired")
			return
		}
		if h.next == nil {
			next, err := c.issue(r.Context(), h.Room, h.Person)
			if err != nil {
				problem(w, 503, "Could not prepare next session; current session is unchanged")
				return
			}
			h.mu.Lock()
			closed = h.closed || !time.Now().Before(h.Invitation.ExpiresAt)
			h.mu.Unlock()
			if closed {
				c.mu.Lock()
				var orphan *Hop
				for _, candidate := range c.hops {
					if candidate.Invitation.URL == next.URL {
						orphan = candidate
						break
					}
				}
				c.mu.Unlock()
				if orphan != nil {
					c.retire(orphan)
				}
				problem(w, 410, "Session expired")
				return
			}
			h.next = &next
		}
		reply(w, 200, h.next)
	case "POST /api/close":
		w.WriteHeader(204)
		if f, ok := w.(http.Flusher); ok {
			f.Flush()
		}
		go c.retire(h)
	default:
		problem(w, 404, "Not found")
	}
}

func (c *Core) admin(token string) http.Handler {
	expected := sha256.Sum256([]byte(token))
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Cache-Control", "no-store")
		if r.Method == "GET" && r.URL.Path == "/health" {
			reply(w, 200, map[string]string{"status": "ready"})
			return
		}
		if !bearer(r, expected) {
			problem(w, 401, "Unauthorized")
			return
		}
		if r.Method == "POST" && r.URL.Path == "/conversations" {
			var input struct {
				Names []string `json:"names"`
			}
			if !readJSON(w, r, &input) {
				return
			}
			id, room, err := c.store.create(input.Names)
			if err != nil {
				problem(w, 400, err.Error())
				return
			}
			result := Invitations{Conversation: id}
			for _, person := range room.People {
				inv, err := c.issue(r.Context(), id, person)
				if err != nil {
					c.mu.Lock()
					var partial []*Hop
					for _, hop := range c.hops {
						if hop.Room == id {
							partial = append(partial, hop)
						}
					}
					c.mu.Unlock()
					for _, hop := range partial {
						c.retire(hop)
					}
					c.store.mu.Lock()
					cleanupErr := c.store.save(id, nil)
					c.store.mu.Unlock()
					if cleanupErr != nil {
						log.Print("Could not remove incomplete conversation from core storage")
					}
					problem(w, 503, "Entries unavailable; conversation was not opened")
					return
				}
				result.Invitations = append(result.Invitations, inv)
			}
			reply(w, 201, result)
			return
		}
		if r.Method == "POST" && r.URL.Path == "/invitations" {
			var input struct {
				Conversation string `json:"conversation"`
				Participant  string `json:"participant"`
			}
			if !readJSON(w, r, &input) {
				return
			}
			c.store.mu.Lock()
			room, ok := c.store.rooms[input.Conversation]
			c.store.mu.Unlock()
			if ok {
				for _, person := range room.People {
					if person.ID == input.Participant {
						inv, err := c.issue(r.Context(), input.Conversation, person)
						if err != nil {
							problem(w, 503, "Entry unavailable")
							return
						}
						reply(w, 201, inv)
						return
					}
				}
			}
			problem(w, 404, "Participant not found")
			return
		}
		problem(w, 404, "Not found")
	})
}
