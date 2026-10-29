package main

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"
	"unicode/utf8"
)

const maxStateBytes = 8 << 20

var errMessage = errors.New("conversation full or message invalid")

type Message struct {
	ID     string    `json:"id"`
	Sender string    `json:"sender"`
	Text   string    `json:"text"`
	SentAt time.Time `json:"sent_at"`
}

type Person struct{ ID, Name string }
type Room struct {
	People   []Person  `json:"people"`
	Messages []Message `json:"messages"`
}
type Store struct {
	mu    sync.Mutex
	path  string
	rooms map[string]Room
}

func openStore(path string) (*Store, error) {
	s := &Store{path: path, rooms: make(map[string]Room)}
	f, err := os.Open(path)
	if os.IsNotExist(err) {
		return s, nil
	}
	if err != nil {
		return nil, err
	}
	defer f.Close()
	b, err := io.ReadAll(io.LimitReader(f, maxStateBytes+1))
	if err != nil {
		return nil, err
	}
	if len(b) > maxStateBytes {
		return nil, errors.New("mailbox state exceeds limit")
	}
	if err = json.Unmarshal(b, &s.rooms); err != nil {
		return nil, fmt.Errorf("read mailbox state: %w", err)
	}
	if s.rooms == nil || len(s.rooms) > 16 {
		return nil, errors.New("invalid mailbox state")
	}
	return s, nil
}

// State changes become visible only after the replacement file is durable.
func (s *Store) save(id string, room *Room) error {
	next := make(map[string]Room, len(s.rooms)+1)
	for key, value := range s.rooms {
		next[key] = value
	}
	if room == nil {
		delete(next, id)
	} else {
		next[id] = *room
	}
	b, err := json.Marshal(next)
	if err != nil {
		return err
	}
	if len(b) > maxStateBytes {
		return errors.New("mailbox storage is full")
	}
	if err = atomicFile(s.path, b); err != nil {
		return err
	}
	s.rooms = next
	return nil
}

func (s *Store) create(names []string) (string, Room, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if len(s.rooms) >= 16 || len(names) != 2 {
		return "", Room{}, errors.New("two participants required; at most 16 conversations")
	}
	r := Room{Messages: []Message{}}
	for _, name := range names {
		name = strings.TrimSpace(name)
		if name == "" || len(name) > 64 || !utf8.ValidString(name) {
			return "", r, errors.New("names must be 1–64 UTF-8 bytes")
		}
		r.People = append(r.People, Person{ID: randomToken(), Name: name})
	}
	id := randomToken()
	return id, r, s.save(id, &r)
}

func (s *Store) messages(id string) []Message {
	s.mu.Lock()
	defer s.mu.Unlock()
	return append([]Message{}, s.rooms[id].Messages...)
}

func (s *Store) add(roomID string, person Person, text string) (Message, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	r, ok := s.rooms[roomID]
	if !ok || len(r.Messages) >= 100 {
		return Message{}, errMessage
	}
	if strings.TrimSpace(text) == "" || len(text) > 4096 || !utf8.ValidString(text) {
		return Message{}, errMessage
	}
	m := Message{ID: randomToken(), Sender: person.Name, Text: text, SentAt: time.Now().UTC()}
	r.Messages = append(append([]Message{}, r.Messages...), m)
	return m, s.save(roomID, &r)
}

func atomicFile(path string, data []byte) error {
	f, err := os.CreateTemp(filepath.Dir(path), ".write-*")
	if err != nil {
		return err
	}
	defer os.Remove(f.Name())
	if _, err = f.Write(data); err == nil {
		err = f.Sync()
	}
	closeErr := f.Close()
	if err != nil {
		return err
	}
	if closeErr != nil {
		return closeErr
	}
	if err = os.Rename(f.Name(), path); err != nil {
		return err
	}
	d, err := os.Open(filepath.Dir(path))
	if err != nil {
		return err
	}
	defer d.Close()
	return d.Sync()
}
