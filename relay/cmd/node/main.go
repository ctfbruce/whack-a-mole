// Command node binds disposable IPv6 listeners to real Debuglet WASM runs.
package main

import (
	"context"
	"crypto/subtle"
	"encoding/hex"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/netip"
	"os"
	"os/exec"
	"os/signal"
	"strings"
	"sync"
	"syscall"
	"time"

	"github.com/netsec-ethz/debuglet/pkg/client"
)

type request struct {
	ID      string `json:"id"`
	Address string `json:"address"`
	Backend string `json:"backend"`
	TTL     int    `json:"ttl_seconds"`
}
type reply struct {
	URL   string `json:"url"`
	RunID string `json:"run_id"`
}
type session struct {
	request
	reply
	deadline    time.Time
	listener    net.Listener
	connections sync.Map
}
type node struct {
	client          *client.Client
	wasm            []byte
	executor, token string
	mu              sync.Mutex
	sessions        map[string]*session
}

func main() {
	endpoint := flag.String("dispatcher", "http://127.0.0.1:9000", "local dispatcher")
	executor := flag.String("executor", "e96cd359-f488-4cef-a4d1-3a2e1c869739", "executor ID")
	wasmPath := flag.String("wasm", "/app/.local/relay.wasm", "relay guest")
	tokenPath := flag.String("token", "/app/.local/runtime.token", "controller bearer token file")
	flag.Parse()
	wasm, err := os.ReadFile(*wasmPath)
	if err != nil {
		fail("read relay guest")
	}
	token, err := os.ReadFile(*tokenPath)
	if err != nil || len(strings.TrimSpace(string(token))) < 32 {
		fail("read controller token")
	}
	api, err := client.New(*endpoint, client.Options{})
	if err != nil {
		fail("configure dispatcher")
	}
	n := &node{client: api, wasm: wasm, executor: *executor, token: strings.TrimSpace(string(token)), sessions: map[string]*session{}}
	server := &http.Server{Addr: "127.0.0.1:9100", Handler: http.HandlerFunc(n.serve), ReadHeaderTimeout: 3 * time.Second, ReadTimeout: 5 * time.Second, WriteTimeout: 30 * time.Second, IdleTimeout: 10 * time.Second, MaxHeaderBytes: 8192}
	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer stop()
	go func() { <-ctx.Done(); server.Close() }()
	serveErr := server.ListenAndServe()
	n.mu.Lock()
	for id := range n.sessions {
		n.destroy(id)
	}
	n.mu.Unlock()
	if !errors.Is(serveErr, http.ErrServerClosed) {
		fail("controller listener")
	}
}
func fail(message string) { fmt.Fprintln(os.Stderr, message); os.Exit(1) }

func (n *node) serve(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Cache-Control", "no-store")
	w.Header().Set("Content-Type", "application/json")
	if subtle.ConstantTimeCompare([]byte(r.Header.Get("Authorization")), []byte("Bearer "+n.token)) != 1 {
		http.Error(w, `{"error":"unauthorized"}`, http.StatusUnauthorized)
		return
	}
	n.mu.Lock()
	defer n.mu.Unlock()
	if r.Method == http.MethodDelete && strings.HasPrefix(r.URL.Path, "/sessions/") {
		if err := n.destroy(strings.TrimPrefix(r.URL.Path, "/sessions/")); err != nil {
			http.Error(w, `{"error":"run cancellation pending"}`, 502)
			return
		}
		w.WriteHeader(http.StatusNoContent)
		return
	}
	if r.Method != http.MethodPost || r.URL.Path != "/sessions" {
		http.NotFound(w, r)
		return
	}
	var q request
	decoder := json.NewDecoder(http.MaxBytesReader(w, r.Body, 4096))
	decoder.DisallowUnknownFields()
	if decoder.Decode(&q) != nil || decoder.Decode(new(any)) != io.EOF || !valid(q) {
		http.Error(w, `{"error":"invalid session"}`, 400)
		return
	}
	if previous := n.sessions[q.ID]; previous != nil {
		if previous.request != q || !time.Now().Before(previous.deadline) {
			http.Error(w, `{"error":"session conflict"}`, 409)
			return
		}
		json.NewEncoder(w).Encode(previous.reply)
		return
	}
	if len(n.sessions) >= 32 {
		http.Error(w, `{"error":"capacity reached"}`, 503)
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), 20*time.Second)
	defer cancel()
	s, err := n.create(ctx, q)
	if err != nil {
		http.Error(w, `{"error":"relay unavailable"}`, 502)
		return
	}
	n.sessions[q.ID] = s
	go func() {
		timer := time.NewTimer(time.Until(s.deadline))
		defer timer.Stop()
		<-timer.C
		n.mu.Lock()
		defer n.mu.Unlock()
		if n.sessions[s.ID] == s {
			n.destroy(s.ID)
		}
	}()
	json.NewEncoder(w).Encode(s.reply)
}
func valid(q request) bool {
	id, err := hex.DecodeString(q.ID)
	if err != nil || len(id) != 16 || q.ID != strings.ToLower(q.ID) || q.TTL < 1 || q.TTL > 1200 {
		return false
	}
	address, err := netip.ParseAddr(q.Address)
	if err != nil || !netip.MustParsePrefix("fd6d:626f:7800:1::/64").Contains(address) || address.String() != q.Address {
		return false
	}
	backend, err := netip.ParseAddrPort(q.Backend)
	return err == nil && backend.Addr() == netip.IPv6Loopback() && backend.Port() > 0
}
func alias(action, address string) error {
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	args := []string{"-6", "addr", action, address + "/128", "dev", "eth0"}
	if action == "add" {
		args = append(args, "nodad")
	}
	return exec.CommandContext(ctx, "ip", args...).Run()
}
func (n *node) create(ctx context.Context, q request) (_ *session, result error) {
	deadline := time.Now().Add(time.Duration(q.TTL) * time.Second)
	batch, err := client.Prepare([]client.Request{{ExecutorID: n.executor, Wasm: n.wasm, Args: []string{q.Backend}, Policy: client.Policy{FloorBW: 1_000_000, CeilBW: 10_000_000, TimeoutMS: int64(q.TTL) * 1000, Addresses: []string{"::1"}, ListenTCP: true}}})
	if err != nil {
		return nil, err
	}
	submission, err := n.client.SubmitTEST(ctx, batch)
	if err != nil {
		return nil, err
	}
	runID := submission.IDs[0]
	defer func() {
		if result != nil {
			n.cancel(runID)
		}
	}()
	var target string
	for target == "" {
		detail, err := n.client.RunDetail(ctx, runID)
		if err != nil {
			return nil, err
		}
		if detail.Execution != nil && detail.Execution.ListenerReady && detail.Execution.TCPListener != nil {
			target = *detail.Execution.TCPListener
			break
		}
		select {
		case <-ctx.Done():
			return nil, ctx.Err()
		case <-time.After(50 * time.Millisecond):
		}
	}
	// Never forward to an unexpected endpoint supplied by the local dispatcher.
	peer, err := netip.ParseAddrPort(target)
	if err != nil || peer.Addr() != netip.IPv6Loopback() || peer.Port() < 20000 || peer.Port() > 20063 {
		return nil, errors.New("invalid guest listener")
	}
	if !time.Now().Before(deadline) {
		return nil, context.DeadlineExceeded
	}
	if err := alias("add", q.Address); err != nil {
		return nil, err
	}
	listener, err := net.Listen("tcp6", net.JoinHostPort(q.Address, "8443"))
	if err != nil {
		alias("del", q.Address)
		return nil, err
	}
	s := &session{request: q, reply: reply{URL: "https://" + net.JoinHostPort(q.Address, "8443"), RunID: runID}, deadline: deadline, listener: listener}
	go s.forward(target)
	return s, nil
}
func (s *session) forward(target string) {
	capacity := make(chan struct{}, 4)
	for {
		peer, err := s.listener.Accept()
		if err != nil {
			return
		}
		select {
		case capacity <- struct{}{}:
		default:
			peer.Close()
			continue
		}
		s.connections.Store(peer, struct{}{})
		go func() {
			defer func() { <-capacity; peer.Close(); s.connections.Delete(peer) }()
			upstream, err := net.DialTimeout("tcp6", target, 2*time.Second)
			if err != nil {
				return
			}
			defer upstream.Close()
			deadline := time.Now().Add(30 * time.Second)
			if s.deadline.Before(deadline) {
				deadline = s.deadline
			}
			peer.SetDeadline(deadline)
			upstream.SetDeadline(deadline)
			done := make(chan struct{})
			go func() { io.Copy(upstream, peer); upstream.Close(); close(done) }()
			io.Copy(peer, upstream)
			peer.Close()
			upstream.Close()
			<-done
		}()
	}
}
func (n *node) cancel(runID string) error {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	return n.client.Cancel(ctx, runID, n.executor)
}

// Keep a failed cancellation in the map so an idempotent DELETE can retry it.
// The public listener and every accepted connection close before that request.
func (n *node) destroy(id string) error {
	s := n.sessions[id]
	if s == nil {
		return nil
	}
	s.listener.Close()
	s.connections.Range(func(key, value any) bool { key.(net.Conn).Close(); return true })
	alias("del", s.Address)
	if err := n.cancel(s.RunID); err != nil {
		return err
	}
	delete(n.sessions, id)
	return nil
}
