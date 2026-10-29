package main

import (
	"encoding/json"
	"log"
	"strings"
	"time"
)

type Event struct {
	Time         time.Time `json:"time"`
	Event        string    `json:"event"`
	Hop          string    `json:"hop,omitempty"`
	Node         string    `json:"node,omitempty"`
	Address      string    `json:"address,omitempty"`
	Participant  string    `json:"participant,omitempty"`
	Conversation string    `json:"conversation,omitempty"`
	Bytes        int       `json:"bytes,omitempty"`
	Detail       string    `json:"detail,omitempty"`
}

// Private telemetry reveals entrance mappings, never credentials or bodies.
// The dashboard retains only the latest 100 events from this process.
func (c *Core) event(h *Hop, kind string, bytes int, detail string) {
	c.recordEvent(Event{Event: kind, Hop: h.ID[:min(12, len(h.ID))], Node: h.Node,
		Address: strings.TrimPrefix(h.Invitation.URL, "https://"), Participant: h.Person.Name,
		Conversation: h.Room, Bytes: bytes, Detail: detail})
}

func (c *Core) recordEvent(e Event) {
	c.eventsMu.Lock()
	defer c.eventsMu.Unlock()
	e.Time = time.Now().UTC()
	if len(c.recentEvents) == 100 {
		copy(c.recentEvents, c.recentEvents[1:])
		c.recentEvents = c.recentEvents[:99]
	}
	c.recentEvents = append(c.recentEvents, e)
	if c.events != nil {
		if err := json.NewEncoder(c.events).Encode(e); err != nil {
			log.Print("Could not write demonstration event")
		}
	}
}
