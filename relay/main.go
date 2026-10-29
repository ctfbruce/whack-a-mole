// Command relay forwards opaque TCP bytes through a Debuglet WASM guest.
// TLS ends at the trusted core, never in this program.
package main

import (
	"errors"
	"os"
	"time"

	"github.com/netsec-ethz/debuglet/pkg/debuglet"
)

func main() {
	if len(os.Args) != 1 {
		os.Exit(2)
	}
	for {
		peer, err := debuglet.AcceptTCPTimeout(time.Second)
		if errors.Is(err, debuglet.ErrTimeout) {
			continue
		}
		if err != nil {
			return
		}
		upstream, err := debuglet.DialTimeout("tcp", os.Args[0], 2*time.Second)
		if err == nil {
			forward(peer, upstream)
			upstream.Close()
		}
		peer.Close()
	}
}

// WASI host calls block guest scheduling. Alternate bounded reads instead of
// assuming that two goroutines can perform simultaneous blocking host calls.
// This deliberately small prototype serves one connection at a time.
func forward(a, b *debuglet.Socket) {
	buf := make([]byte, 32<<10)
	idle := time.Now().Add(15 * time.Second)
	for time.Now().Before(idle) {
		for _, pair := range [][2]*debuglet.Socket{{a, b}, {b, a}} {
			src, dst := pair[0], pair[1]
			if src.SetReadDeadline(time.Now().Add(5*time.Millisecond)) != nil {
				return
			}
			n, err := src.Read(buf)
			if n > 0 {
				if dst.SetWriteDeadline(time.Now().Add(time.Second)) != nil {
					return
				}
				written, writeErr := dst.Write(buf[:n])
				if writeErr != nil || written != n {
					return
				}
				idle = time.Now().Add(15 * time.Second)
			}
			if err != nil && !errors.Is(err, debuglet.ErrTimeout) {
				return
			}
		}
	}
}
