package main

import (
	"encoding/json"
	"log"
	"strings"
	"time"
)

// Optional, private demonstration telemetry. It reveals the mapping between
// entrances, but never records passphrases, bearer tokens or message contents.
func (c *Core) event(h *Hop, kind string, bytes int, detail string) {
	if c.events == nil {
		return
	}
	e := struct {
		Time        time.Time `json:"time"`
		Event       string    `json:"event"`
		Hop         string    `json:"hop"`
		Node        string    `json:"node"`
		Address     string    `json:"address"`
		Participant string    `json:"participant"`
		Bytes       int       `json:"bytes,omitempty"`
		Detail      string    `json:"detail,omitempty"`
	}{time.Now().UTC(), kind, h.ID[:12], h.Node, strings.TrimPrefix(h.Invitation.URL, "https://"), h.Person.Name, bytes, detail}
	c.eventsMu.Lock()
	err := json.NewEncoder(c.events).Encode(e)
	c.eventsMu.Unlock()
	if err != nil {
		log.Print("Could not write demonstration event")
	}
}
