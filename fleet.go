package main

import (
	"context"
	"crypto/rand"
	"encoding/json"
	"errors"
	"math/big"
	"net"
	"net/http"
	"net/netip"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"
)

// Public entrances are explicit operator-owned endpoints. Control traffic stays
// on loopback SSH forwards; browser TLS still terminates at the trusted core.
type Entrance struct {
	Name       string `json:"name"`
	URL        string `json:"url"`
	Backend    string `json:"backend"`
	RuntimeURL string `json:"runtime_url"`
	TokenFile  string `json:"token_file"`
	ip         netip.Addr
}
type fleetRuntime struct {
	nodes map[string]*RemoteRuntime
	mu    sync.Mutex
	runs  map[string]*RemoteRuntime
}

func runtimeClient() *http.Client {
	return &http.Client{Timeout: 30 * time.Second, CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
}
func loadFleet(path string) ([]Entrance, Runtime, error) {
	b, err := os.ReadFile(path)
	if err != nil {
		return nil, nil, err
	}
	var nodes []Entrance
	if len(b) > 65536 || json.Unmarshal(b, &nodes) != nil || len(nodes) < 2 || len(nodes) > 32 {
		return nil, nil, errors.New("fleet requires 2–32 entrances")
	}
	f := &fleetRuntime{nodes: map[string]*RemoteRuntime{}, runs: map[string]*RemoteRuntime{}}
	names, backends := map[string]bool{}, map[string]bool{}
	for i := range nodes {
		n := &nodes[i]
		u, parseErr := url.Parse(n.URL)
		if parseErr != nil || u.Scheme != "https" || u.User != nil || u.Path != "" || u.RawQuery != "" || u.Fragment != "" {
			return nil, nil, errors.New("fleet URLs must be HTTPS IP origins")
		}
		endpoint, parseErr := netip.ParseAddrPort(u.Host)
		backend, backendErr := netip.ParseAddrPort(n.Backend)
		if parseErr != nil || endpoint.Port() == 0 || !endpoint.Addr().IsGlobalUnicast() || endpoint.Addr().IsPrivate() || endpoint.Addr().IsLoopback() || endpoint.Addr().Zone() != "" || backendErr != nil || backend.Addr() != netip.IPv6Loopback() || backend.Port() == 0 || !loopbackURL(n.RuntimeURL) {
			return nil, nil, errors.New("invalid public entrance or loopback control endpoint")
		}
		n.ip = endpoint.Addr()
		if n.Name == "" || len(n.Name) > 64 || names[n.Name] || backends[n.Backend] || f.nodes[n.ip.String()] != nil {
			return nil, nil, errors.New("fleet names, public IPs and backend ports must be distinct")
		}
		if !filepath.IsAbs(n.TokenFile) {
			n.TokenFile = filepath.Join(filepath.Dir(path), n.TokenFile)
		}
		token, readErr := os.ReadFile(n.TokenFile)
		if readErr != nil || len(strings.TrimSpace(string(token))) < 32 {
			return nil, nil, errors.New("fleet controller token unavailable")
		}
		f.nodes[n.ip.String()] = &RemoteRuntime{URL: n.RuntimeURL, Token: strings.TrimSpace(string(token)), Client: runtimeClient()}
		names[n.Name], backends[n.Backend] = true, true
	}
	return nodes, f, nil
}
func (f *fleetRuntime) Start(ctx context.Context, q EntryRequest) error {
	node := f.nodes[q.Address]
	if node == nil {
		return errors.New("entrance outside owned fleet")
	}
	f.mu.Lock()
	f.runs[q.ID] = node
	f.mu.Unlock()
	return node.Start(ctx, q)
}
func (f *fleetRuntime) Stop(ctx context.Context, id string) error {
	f.mu.Lock()
	node := f.runs[id]
	f.mu.Unlock()
	if node == nil {
		return nil
	}
	if err := node.Stop(ctx, id); err != nil {
		return err
	}
	f.mu.Lock()
	delete(f.runs, id)
	f.mu.Unlock()
	return nil
}

// Called under the core lock. Only confirmed free nodes enter the random draw.
// Previously used nodes become eligible again after successful cleanup.
func (c *Core) entrance() (Entrance, error) {
	if len(c.fleet) == 0 {
		bits := c.prefix.Addr().As16()
		if _, err := rand.Read(bits[8:]); err != nil {
			return Entrance{}, err
		}
		ip := netip.AddrFrom16(bits)
		return Entrance{Name: "local IPv6", URL: "https://" + net.JoinHostPort(ip.String(), "8443"), Backend: "[::1]:0", ip: ip}, nil
	}
	free := make([]Entrance, 0, len(c.fleet))
	for _, n := range c.fleet {
		used := false
		for _, h := range c.hops {
			if h.Invitation.URL == n.URL {
				used = true
				break
			}
		}
		if !used {
			free = append(free, n)
		}
	}
	if len(free) == 0 {
		return Entrance{}, errors.New("all relay pods are occupied")
	}
	index, err := rand.Int(rand.Reader, big.NewInt(int64(len(free))))
	if err != nil {
		return Entrance{}, err
	}
	return free[index.Int64()], nil
}
