package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"log"
	"net/http"
	"net/netip"
	"net/url"
	"os"
	"os/signal"
	"path/filepath"
	"strings"
	"sync"
	"syscall"
	"time"
)

// The welcome service exposes enrollment only. Administration remains on the
// core's loopback API; the second participant's invitation stays on the core host.
func welcomeHandler(dir, admin, origin string) (http.Handler, error) {
	u, err := url.Parse(origin)
	if err != nil || u.Scheme != "https" || u.Host == "" || u.User != nil || u.Path != "" || u.RawQuery != "" || u.Fragment != "" || !loopbackURL(admin) {
		return nil, errors.New("welcome requires an HTTPS origin and loopback core API")
	}
	token, err := os.ReadFile(filepath.Join(dir, "admin.token"))
	if err != nil || len(strings.TrimSpace(string(token))) < 32 {
		return nil, errors.New("core administration credential unavailable")
	}
	ca, err := os.ReadFile(filepath.Join(dir, "ca.pem"))
	if err != nil {
		return nil, err
	}
	if err := os.MkdirAll(filepath.Join(dir, "enrollments"), 0700); err != nil {
		return nil, err
	}
	client := &http.Client{Timeout: 65 * time.Second, CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
	var mu sync.Mutex
	var busy bool
	var window time.Time
	var attempts int
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Cache-Control", "no-store")
		w.Header().Set("Referrer-Policy", "no-referrer")
		w.Header().Set("X-Content-Type-Options", "nosniff")
		w.Header().Set("Content-Security-Policy", "default-src 'none'; script-src 'self'; style-src 'self'; connect-src 'self'; base-uri 'none'; frame-ancestors 'none'; form-action 'none'")
		if r.Host != u.Host {
			problem(w, 403, "Unexpected welcome address")
			return
		}
		if r.Method == "GET" {
			if r.URL.Path == "/ca.pem" {
				w.Header().Set("Content-Type", "application/x-pem-file")
				w.Header().Set("Content-Disposition", `attachment; filename="mailbox-demo-ca.pem"`)
				_, _ = w.Write(ca)
				return
			}
			file := map[string]string{"/": "web/welcome.html", "/welcome.js": "web/welcome.js", "/style.css": "web/style.css"}[r.URL.Path]
			if file != "" {
				body, err := assets.ReadFile(file)
				if err != nil {
					problem(w, 503, "Welcome page unavailable")
					return
				}
				kind := "text/html; charset=utf-8"
				if strings.HasSuffix(file, ".js") {
					kind = "text/javascript; charset=utf-8"
				}
				if strings.HasSuffix(file, ".css") {
					kind = "text/css; charset=utf-8"
				}
				w.Header().Set("Content-Type", kind)
				_, _ = w.Write(body)
				return
			}
		}
		if r.Method != "POST" || r.URL.Path != "/api/enroll" {
			problem(w, 404, "Not found")
			return
		}
		if r.Header.Get("Origin") != origin {
			problem(w, 403, "Open the welcome page to enroll")
			return
		}
		var input struct{}
		if !readJSONLimit(w, r, &input, 1024) {
			return
		}
		mu.Lock()
		if time.Since(window) >= time.Minute {
			window = time.Now()
			attempts = 0
		}
		if busy || attempts >= 5 {
			mu.Unlock()
			problem(w, 429, "Enrollment is busy. Please try again shortly.")
			return
		}
		busy = true
		attempts++
		mu.Unlock()
		defer func() { mu.Lock(); busy = false; mu.Unlock() }()
		req, err := http.NewRequestWithContext(r.Context(), "POST", admin+"/conversations", bytes.NewBufferString(`{"names":["Visitor","Core operator"]}`))
		if err != nil {
			problem(w, 503, "Core unavailable")
			return
		}
		req.Header.Set("Authorization", "Bearer "+strings.TrimSpace(string(token)))
		req.Header.Set("Content-Type", "application/json")
		resp, err := client.Do(req)
		if err != nil {
			problem(w, 503, "No entrance could be confirmed. Please try again.")
			return
		}
		defer resp.Body.Close()
		var result Invitations
		if resp.StatusCode != http.StatusCreated || json.NewDecoder(io.LimitReader(resp.Body, 16384)).Decode(&result) != nil || len(result.Invitations) != 2 {
			problem(w, 503, "No entrance is available. Please try again shortly.")
			return
		}
		// Keep the core operator's invitation private, never in the visitor response.
		data, err := json.Marshal(result)
		if err == nil {
			err = atomicFile(filepath.Join(dir, "enrollments", randomToken()+".json"), data)
		}
		if err != nil {
			log.Print("Enrollment record could not be saved; entries remain bounded by expiry")
			problem(w, 503, "Enrollment could not be saved. Please try again later.")
			return
		}
		reply(w, 201, result.Invitations[0])
	}), nil
}

func serveWelcome(dir, admin, listen, origin string) error {
	address, err := netip.ParseAddrPort(listen)
	if err != nil || !address.Addr().IsLoopback() {
		return errors.New("welcome service must listen on a loopback IP behind its trusted HTTPS front door")
	}
	handler, err := welcomeHandler(dir, admin, origin)
	if err != nil {
		return err
	}
	server := &http.Server{Addr: listen, Handler: handler, ReadHeaderTimeout: 5 * time.Second, ReadTimeout: 10 * time.Second, WriteTimeout: 70 * time.Second, IdleTimeout: 10 * time.Second, MaxHeaderBytes: 8192}
	ctx, cancel := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer cancel()
	done := make(chan error, 1)
	go func() { done <- server.ListenAndServe() }()
	log.Print("Welcome service ready")
	select {
	case err = <-done:
	case <-ctx.Done():
	}
	shutdown, stop := context.WithTimeout(context.Background(), 3*time.Second)
	defer stop()
	_ = server.Shutdown(shutdown)
	if errors.Is(err, http.ErrServerClosed) {
		return nil
	}
	return err
}
