package main

import (
	"crypto/sha256"
	"errors"
	"net/http"
	"net/netip"
	"sort"
	"strings"
	"time"
)

type operatorConversation struct {
	ID       string    `json:"id"`
	People   []Person  `json:"people"`
	Messages []Message `json:"messages"`
}
type operatorRelay struct {
	Node         string     `json:"node"`
	URL          string     `json:"url"`
	Status       string     `json:"status"`
	Conversation string     `json:"conversation,omitempty"`
	Participant  string     `json:"participant,omitempty"`
	ExpiresAt    *time.Time `json:"expires_at,omitempty"`
}
type operatorState struct {
	GeneratedAt   time.Time              `json:"generated_at"`
	Conversations []operatorConversation `json:"conversations"`
	Relays        []operatorRelay        `json:"relays"`
	Events        []Event                `json:"events"`
}

// The operator UI is served only through the existing loopback admin listener.
// A separate bearer credential grants inspection and replies, not provisioning.
func (c *Core) operator(token string) http.Handler {
	expected := sha256.Sum256([]byte(token))
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Cache-Control", "no-store")
		w.Header().Set("Referrer-Policy", "no-referrer")
		w.Header().Set("X-Content-Type-Options", "nosniff")
		w.Header().Set("X-Frame-Options", "DENY")
		w.Header().Set("Content-Security-Policy", "default-src 'none'; script-src 'self'; style-src 'self'; connect-src 'self'; base-uri 'none'; frame-ancestors 'none'; form-action 'none'")
		host, err := netip.ParseAddrPort(r.Host)
		if err != nil || !host.Addr().IsLoopback() || host.Addr().Zone() != "" || host.Port() == 0 ||
			(r.Method == "POST" && r.Header.Get("Origin") != "http://"+r.Host) {
			problem(w, 403, "Use the private loopback dashboard address")
			return
		}
		if r.Method == "GET" {
			files := map[string]string{"/operator/": "web/operator.html", "/operator/operator.js": "web/operator.js", "/operator/operator.css": "web/operator.css"}
			if file, ok := files[r.URL.Path]; ok {
				data, err := assets.ReadFile(file)
				if err != nil {
					problem(w, 500, "Page unavailable")
					return
				}
				contentType := "text/html; charset=utf-8"
				if strings.HasSuffix(file, ".js") {
					contentType = "text/javascript; charset=utf-8"
				}
				if strings.HasSuffix(file, ".css") {
					contentType = "text/css; charset=utf-8"
				}
				w.Header().Set("Content-Type", contentType)
				_, _ = w.Write(data)
				return
			}
		}
		if token == "" || !bearer(r, expected) {
			problem(w, 401, "Operator key required")
			return
		}
		switch r.Method + " " + r.URL.Path {
		case "GET /operator/api/state":
			reply(w, 200, c.operatorState())
		case "POST /operator/api/reply":
			var input struct {
				Conversation string `json:"conversation"`
				Text         string `json:"text"`
			}
			if !readJSON(w, r, &input) {
				return
			}
			c.store.mu.Lock()
			_, exists := c.store.rooms[input.Conversation]
			c.store.mu.Unlock()
			if !exists {
				problem(w, 404, "Conversation not found")
				return
			}
			message, err := c.store.add(input.Conversation, Person{Name: "Core operator"}, input.Text)
			if err != nil {
				if errors.Is(err, errMessage) {
					problem(w, 400, "Conversation full or text invalid")
				} else {
					problem(w, 503, "Core storage unavailable; delivery is unconfirmed")
				}
				return
			}
			c.recordEvent(Event{Event: "operator_reply", Conversation: input.Conversation, Participant: "Core operator", Bytes: len(input.Text)})
			reply(w, 201, message)
		default:
			problem(w, 404, "Not found")
		}
	})
}

func (c *Core) operatorState() operatorState {
	s := operatorState{GeneratedAt: time.Now().UTC(), Conversations: []operatorConversation{}, Relays: []operatorRelay{}}
	latest := map[string]time.Time{}
	c.store.mu.Lock()
	for id, room := range c.store.rooms {
		conversation := operatorConversation{ID: id, People: append([]Person{}, room.People...), Messages: append([]Message{}, room.Messages...)}
		s.Conversations = append(s.Conversations, conversation)
		for _, m := range room.Messages {
			if m.SentAt.After(latest[id]) {
				latest[id] = m.SentAt
			}
		}
	}
	c.store.mu.Unlock()
	c.eventsMu.Lock()
	s.Events = append([]Event{}, c.recentEvents...)
	c.eventsMu.Unlock()
	for _, e := range s.Events {
		if e.Time.After(latest[e.Conversation]) {
			latest[e.Conversation] = e.Time
		}
	}

	// Never hold the core lock while inspecting a hop: provisioning holds its
	// lock for up to 25 seconds, and retirement also needs the core lock.
	c.mu.Lock()
	hops := make([]*Hop, 0, len(c.hops))
	for _, h := range c.hops {
		hops = append(hops, h)
	}
	c.mu.Unlock()
	byURL := map[string]operatorRelay{}
	for _, h := range hops {
		expires := h.Invitation.ExpiresAt
		r := operatorRelay{Node: h.Node, URL: h.Invitation.URL, Status: "preparing", Conversation: h.Room, Participant: h.Person.Name, ExpiresAt: &expires}
		if h.mu.TryLock() {
			if h.closed {
				r.Status = "closing"
			} else if h.timer != nil {
				r.Status = "active"
			}
			h.mu.Unlock()
		}
		byURL[r.URL] = r
		if started := expires.Add(-c.ttl); started.After(latest[h.Room]) {
			latest[h.Room] = started
		}
	}
	for _, n := range c.fleet {
		if r, ok := byURL[n.URL]; ok {
			s.Relays = append(s.Relays, r)
		} else {
			s.Relays = append(s.Relays, operatorRelay{Node: n.Name, URL: n.URL, Status: "free"})
		}
		delete(byURL, n.URL)
	}
	// Locally generated IPv6 entrances have no preconfigured fleet order.
	urls := make([]string, 0, len(byURL))
	for url := range byURL {
		urls = append(urls, url)
	}
	sort.Strings(urls)
	for _, url := range urls {
		s.Relays = append(s.Relays, byURL[url])
	}
	sort.Slice(s.Conversations, func(i, j int) bool {
		a, b := s.Conversations[i].ID, s.Conversations[j].ID
		if latest[a].Equal(latest[b]) {
			return a < b
		}
		return latest[a].After(latest[b])
	})
	return s
}
