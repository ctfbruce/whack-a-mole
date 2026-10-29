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
	"net/url"
	"os"
	"os/exec"
	"os/signal"
	"strconv"
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
	mu          sync.Mutex
	closed      bool
	connections map[net.Conn]struct{}
}
type node struct {
	client          *client.Client
	wasm            []byte
	executor, token string
	mu              sync.Mutex
	sessions        map[string]*session
	publicURL       string
	publicIP        netip.Addr
	backendPort     uint16
}

func main() {
	endpoint := flag.String("dispatcher", "http://127.0.0.1:9000", "local dispatcher")
	executor := flag.String("executor", "e96cd359-f488-4cef-a4d1-3a2e1c869739", "executor ID")
	wasmPath := flag.String("wasm", "/app/.local/relay.wasm", "relay guest")
	tokenPath := flag.String("token", "/app/.local/runtime.token", "controller bearer token file")
	publicURL := flag.String("public-url", "", "optional https://public-ip:mapped-port for a dedicated cloud pod")
	backendPort := flag.Int("backend-port", 0, "the one loopback SSH tunnel port allowed in cloud mode")
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
	if err := n.configureCloud(*publicURL, *backendPort); err != nil {
		fail("invalid cloud endpoint or backend port")
	}
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
	if decoder.Decode(&q) != nil || decoder.Decode(new(any)) != io.EOF || !n.accepts(q) {
		http.Error(w, `{"error":"invalid session"}`, 400)
		return
	}
	if previous := n.sessions[q.ID]; previous != nil {
		previous.mu.Lock()
		closed := previous.closed
		previous.mu.Unlock()
		if closed || previous.request != q || !time.Now().Before(previous.deadline) {
			http.Error(w, `{"error":"session conflict"}`, 409)
			return
		}
		json.NewEncoder(w).Encode(previous.reply)
		return
	}
	capacity := 32
	if n.publicURL != "" {
		capacity = 1
	}
	if len(n.sessions) >= capacity {
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
	return (&node{}).accepts(q)
}

func (n *node) configureCloud(raw string, port int) error {
	if raw == "" && port == 0 {
		return nil
	}
	u, err := url.Parse(raw)
	if err != nil || u.Scheme != "https" || u.User != nil || u.Path != "" || u.RawQuery != "" || u.Fragment != "" || port < 1024 || port > 65535 {
		return errors.New("invalid cloud configuration")
	}
	ip, err := netip.ParseAddr(u.Hostname())
	publicPort, portErr := strconv.Atoi(u.Port())
	if err != nil || !ip.Is4() || !ip.IsGlobalUnicast() || ip.IsPrivate() || portErr != nil || publicPort < 1 || publicPort > 65535 || u.Host != net.JoinHostPort(ip.String(), u.Port()) {
		return errors.New("invalid public IPv4 endpoint")
	}
	n.publicURL, n.publicIP, n.backendPort = raw, ip, uint16(port)
	return nil
}

func (n *node) accepts(q request) bool {
	id, err := hex.DecodeString(q.ID)
	if err != nil || len(id) != 16 || q.ID != strings.ToLower(q.ID) || q.TTL < 1 || q.TTL > 1200 {
		return false
	}
	address, err := netip.ParseAddr(q.Address)
	if err != nil || address.String() != q.Address {
		return false
	}
	if n.publicURL != "" {
		return address == n.publicIP && q.Backend == net.JoinHostPort("::1", strconv.Itoa(int(n.backendPort)))
	}
	if !netip.MustParsePrefix("fd6d:626f:7800:1::/64").Contains(address) {
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
	backend := q.Backend
	addresses := []string{"::1"}
	if n.publicURL != "" {
		backend = net.JoinHostPort("127.0.0.1", strconv.Itoa(int(n.backendPort)))
		addresses = []string{"127.0.0.1"}
	}
	batch, err := client.Prepare([]client.Request{{ExecutorID: n.executor, Wasm: n.wasm, Args: []string{backend}, Policy: client.Policy{FloorBW: 1_000_000, CeilBW: 10_000_000, TimeoutMS: int64(q.TTL) * 1000, Addresses: addresses, ListenTCP: true}}})
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
	expectedPeer := netip.IPv6Loopback()
	if n.publicURL != "" {
		expectedPeer = netip.MustParseAddr("127.0.0.1")
	}
	if err != nil || peer.Addr() != expectedPeer || peer.Port() < 20000 || peer.Port() > 20063 {
		return nil, errors.New("invalid guest listener")
	}
	if !time.Now().Before(deadline) {
		return nil, context.DeadlineExceeded
	}
	network, bind, publicURL := "tcp4", "0.0.0.0:8443", n.publicURL
	if n.publicURL == "" {
		if err := alias("add", q.Address); err != nil {
			return nil, err
		}
		network, bind = "tcp6", net.JoinHostPort(q.Address, "8443")
		publicURL = "https://" + bind
	}
	listener, err := net.Listen(network, bind)
	if err != nil {
		if n.publicURL == "" {
			alias("del", q.Address)
		}
		return nil, err
	}
	s := &session{request: q, reply: reply{URL: publicURL, RunID: runID}, deadline: deadline, listener: listener, connections: map[net.Conn]struct{}{}}
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
		s.mu.Lock()
		if s.closed {
			s.mu.Unlock()
			peer.Close()
			<-capacity
			return
		}
		s.connections[peer] = struct{}{}
		s.mu.Unlock()
		go func() {
			defer func() { <-capacity; peer.Close(); s.mu.Lock(); delete(s.connections, peer); s.mu.Unlock() }()
			upstream, err := net.DialTimeout("tcp", target, 2*time.Second)
			if err != nil {
				return
			}
			defer upstream.Close()
			deadline := time.Now().Add(40 * time.Second)
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
	s.mu.Lock()
	s.closed = true
	for connection := range s.connections {
		connection.Close()
	}
	s.mu.Unlock()
	if n.publicURL == "" {
		alias("del", s.Address)
	}
	if err := n.cancel(s.RunID); err != nil {
		return err
	}
	delete(n.sessions, id)
	return nil
}
