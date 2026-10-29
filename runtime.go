package main

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/netip"
	"net/url"
)

func loopbackURL(raw string) bool {
	u, err := url.Parse(raw)
	if err != nil || u.Scheme != "http" || u.User != nil || u.Path != "" || u.RawQuery != "" || u.Fragment != "" {
		return false
	}
	a, err := netip.ParseAddrPort(u.Host)
	return err == nil && a.Addr().IsLoopback() && a.Port() != 0
}

type EntryRequest struct {
	ID         string `json:"id"`
	Address    string `json:"address"`
	Backend    string `json:"backend"`
	TTLSeconds int    `json:"ttl_seconds"`
}
type Runtime interface {
	Start(context.Context, EntryRequest) error
	Stop(context.Context, string) error
}
type RemoteRuntime struct {
	URL, Token string
	Client     *http.Client
}

func (r *RemoteRuntime) call(ctx context.Context, method, path string, body any) error {
	var b bytes.Buffer
	if body != nil {
		if err := json.NewEncoder(&b).Encode(body); err != nil {
			return err
		}
	}
	req, err := http.NewRequestWithContext(ctx, method, r.URL+path, &b)
	if err != nil {
		return err
	}
	req.Header.Set("Authorization", "Bearer "+r.Token)
	req.Header.Set("Content-Type", "application/json")
	resp, err := r.Client.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	_, _ = io.Copy(io.Discard, io.LimitReader(resp.Body, 4096))
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return fmt.Errorf("runtime returned HTTP %d", resp.StatusCode)
	}
	return nil
}
func (r *RemoteRuntime) Start(ctx context.Context, req EntryRequest) error {
	return r.call(ctx, "POST", "/sessions", req)
}
func (r *RemoteRuntime) Stop(ctx context.Context, id string) error {
	return r.call(ctx, "DELETE", "/sessions/"+id, nil)
}
