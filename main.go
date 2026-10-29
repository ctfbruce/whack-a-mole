package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"log"
	"net"
	"net/http"
	"net/netip"
	"os"
	"os/signal"
	"path/filepath"
	"strings"
	"syscall"
	"time"
)

func main() {
	if err := run(os.Args[1:]); err != nil {
		log.Print(err)
		os.Exit(1)
	}
}
func run(args []string) error {
	if len(args) == 0 {
		return errors.New("usage: mailbox serve | welcome | create | resume")
	}
	flags := flag.NewFlagSet(args[0], flag.ContinueOnError)
	dir := flags.String("state", ".local/core", "private state directory")
	admin := flags.String("admin", "http://127.0.0.1:9200", "local admin URL")
	listen := flags.String("listen", "127.0.0.1:9200", "loopback admin address")
	runtimeURL := flags.String("runtime", "http://127.0.0.1:9100", "local runtime controller")
	runtimeToken := flags.String("runtime-token", ".local/runtime.token", "runtime credential file")
	ttl := flags.Duration("ttl", 10*time.Minute, "entry lifetime (10 seconds–20 minutes)")
	prefixText := flags.String("prefix", "fd6d:626f:7800:1::/64", "local IPv6 address pool")
	out := flags.String("out", ".local/invitations.json", "private invitation output file")
	from := flags.String("from", ".local/invitations.json", "existing invitations for resume")
	names := flags.String("names", "Source,Reporter", "two participant names")
	fleetPath := flags.String("fleet", "", "private configuration for owned remote entrances")
	eventsPath := flags.String("events", "", "optional private JSONL demonstration log")
	origin := flags.String("origin", "", "public HTTPS origin for the welcome service")
	if err := flags.Parse(args[1:]); err != nil {
		return err
	}
	if args[0] == "welcome" {
		return serveWelcome(*dir, *admin, *listen, *origin)
	}
	if args[0] != "serve" && args[0] != "create" && args[0] != "resume" {
		return errors.New("unknown command")
	}
	if args[0] != "serve" {
		return invitations(args[0], *dir, *admin, *names, *from, *out)
	}
	if !loopbackURL(*runtimeURL) {
		return errors.New("runtime URL must be http://loopback-IP:port")
	}
	address, err := netip.ParseAddrPort(*listen)
	if err != nil || !address.Addr().IsLoopback() {
		return errors.New("admin must listen on a loopback IP")
	}
	prefix, err := netip.ParsePrefix(*prefixText)
	if err != nil || !prefix.Addr().Is6() || prefix.Bits() != 64 || !prefix.Addr().IsPrivate() {
		return errors.New("local prototype requires a ULA IPv6 /64")
	}
	if *ttl < 10*time.Second || *ttl > 20*time.Minute {
		return errors.New("TTL must be between 10 seconds and 20 minutes")
	}
	if err = os.MkdirAll(*dir, 0700); err != nil {
		return err
	}
	if err = os.Chmod(*dir, 0700); err != nil {
		return err
	}
	ca, err := authority(*dir)
	if err != nil {
		return err
	}
	store, err := openStore(filepath.Join(*dir, "mailboxes.json"))
	if err != nil {
		return err
	}
	adminToken, err := ensureToken(filepath.Join(*dir, "admin.token"))
	if err != nil {
		return err
	}
	c := &Core{store: store, ca: ca, prefix: prefix.Masked(), ttl: *ttl, hops: make(map[string]*Hop)}
	if *fleetPath != "" {
		c.fleet, c.runtime, err = loadFleet(*fleetPath)
		if err != nil {
			return err
		}
	} else {
		nodeToken, readErr := os.ReadFile(*runtimeToken)
		if readErr != nil {
			return readErr
		}
		if len(strings.TrimSpace(string(nodeToken))) < 32 {
			return errors.New("runtime token is too short")
		}
		c.runtime = &RemoteRuntime{URL: *runtimeURL, Token: strings.TrimSpace(string(nodeToken)), Client: runtimeClient()}
	}
	if *eventsPath != "" {
		if err := os.MkdirAll(filepath.Dir(*eventsPath), 0700); err != nil {
			return err
		}
		file, err := os.OpenFile(*eventsPath, os.O_WRONLY|os.O_CREATE|os.O_APPEND, 0600)
		if err != nil {
			return err
		}
		defer file.Close()
		if err := file.Chmod(0600); err != nil {
			return err
		}
		c.events = file
	}
	server := &http.Server{Addr: *listen, Handler: c.admin(adminToken), ReadHeaderTimeout: 5 * time.Second, ReadTimeout: 10 * time.Second, WriteTimeout: 100 * time.Second, MaxHeaderBytes: 8192}
	listener, err := net.Listen("tcp", *listen)
	if err != nil {
		return err
	}
	ctx, cancel := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer cancel()
	done := make(chan error, 1)
	go func() { done <- server.Serve(listener) }()
	log.Printf("Local core ready; trust certificate at %s", filepath.Join(*dir, "ca.pem"))
	select {
	case <-ctx.Done():
	case err = <-done:
		if err != http.ErrServerClosed {
			cancel()
		}
	}
	shutdown, stop := context.WithTimeout(context.Background(), 3*time.Second)
	defer stop()
	_ = server.Shutdown(shutdown)
	c.close()
	if err == http.ErrServerClosed {
		return nil
	}
	return err
}

func ensureToken(path string) (string, error) {
	b, err := os.ReadFile(path)
	if os.IsNotExist(err) {
		token := randomToken()
		return token, atomicFile(path, []byte(token+"\n"))
	}
	if err != nil {
		return "", err
	}
	token := strings.TrimSpace(string(b))
	if len(token) < 32 {
		return "", errors.New("invalid admin token")
	}
	return token, nil
}

func invitations(action, dir, admin, names, from, out string) error {
	if !loopbackURL(admin) {
		return errors.New("admin URL must be http://loopback-IP:port")
	}
	token, err := os.ReadFile(filepath.Join(dir, "admin.token"))
	if err != nil {
		return err
	}
	client := &http.Client{Timeout: 100 * time.Second, CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
	call := func(path string, input, output any) error {
		b, err := json.Marshal(input)
		if err != nil {
			return err
		}
		req, err := http.NewRequest("POST", admin+path, bytes.NewReader(b))
		if err != nil {
			return err
		}
		req.Header.Set("Authorization", "Bearer "+strings.TrimSpace(string(token)))
		req.Header.Set("Content-Type", "application/json")
		resp, err := client.Do(req)
		if err != nil {
			return err
		}
		defer resp.Body.Close()
		if resp.StatusCode < 200 || resp.StatusCode >= 300 {
			return fmt.Errorf("admin request failed with HTTP %d", resp.StatusCode)
		}
		return json.NewDecoder(io.LimitReader(resp.Body, 16384)).Decode(output)
	}
	var result Invitations
	if action == "create" {
		err = call("/conversations", map[string]any{"names": strings.Split(names, ",")}, &result)
	} else {
		b, readErr := os.ReadFile(from)
		if readErr != nil {
			return readErr
		}
		if err = json.Unmarshal(b, &result); err != nil {
			return err
		}
		for i, old := range result.Invitations {
			err = call("/invitations", map[string]string{"conversation": result.Conversation, "participant": old.Participant}, &result.Invitations[i])
			if err != nil {
				break
			}
		}
	}
	if err != nil {
		return err
	}
	if err = os.MkdirAll(filepath.Dir(out), 0700); err != nil {
		return err
	}
	b, err := json.MarshalIndent(result, "", "  ")
	if err != nil {
		return err
	}
	if err = atomicFile(out, b); err != nil {
		return err
	}
	fmt.Printf("Private invitations saved to %s\n", out)
	return nil
}
