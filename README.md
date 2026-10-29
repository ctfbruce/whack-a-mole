# Whack-a-Mole

A small research prototype: two people exchange messages through temporary
websites. Each entrance has its own address, passphrase and lifetime. A person can
prepare their next entrance, close the current one, and continue the same conversation.

Built on [Debuglet](https://github.com/netsec-ethz/debuglet). No app installation
is required for messaging; the demo browser must trust the test certificate authority.

The native Go core retains messages and serves the browser interface. A real
Debuglet WebAssembly program forwards the browser's encrypted connection; it does
not decrypt messages. This project is separate from Debuglet and changes none of
its source. The default lab uses local IPv6; an optional public rehearsal rotates
among ten CPU pods with distinct public IPv4 addresses.

```text
Browser → random IPv6 address → TCP bridge → Debuglet WASM relay → trusted core
        └──────────────── verified TLS to the core ─────────────────┘
```

The small native bridge is necessary because Debuglet's local listener policy
accepts loopback peers. The Docker lab supplies IPv6 addressing and isolation;
Debuglet supplies the bounded guest runtime. The local lab publishes no host ports.

**This demonstrates a mechanism, not a safe whistleblowing service.** The core
reads messages; the active website is identifiable; changing addresses does not
hide traffic or erase device records. Read [the threat model](THREAT_MODEL.md).

## Run the local lab

Requires Linux amd64, Docker with IPv6 networking, and Git. Initial image and
dependency downloads need Internet access; the running lab uses an internal network.

Clone this repository and the pinned Debuglet source:

```sh
git clone https://github.com/ctfbruce/whack-a-mole.git
cd whack-a-mole
git clone https://github.com/netsec-ethz/debuglet.git ../debuglet-relay-base
git -C ../debuglet-relay-base checkout a060c5c03a200feff4ecc17c09d7dcba21e20617
export DEBUGLET_SOURCE="$(cd ../debuglet-relay-base && pwd)"
./demo/build.sh
./demo/run.sh up
```

Run the two-person browser rehearsal:

```sh
./tests/browser/check.sh
```

It creates a conversation, opens two Chromium sessions, exchanges messages,
prepares a new IPv6 entrance and checks that history follows. It first confirms
that the untrusted certificate is rejected, then trusts this lab's CA only inside
the disposable browser container. It also checks several UI failure cases.
Private screenshots and the result summary are saved under `.local/browser-evidence/`.
The script reports its actual output directory.

To issue invitations manually inside the lab:

```sh
docker exec ephemeral-mailbox-lab /app/.local/mailbox create \
  -state /app/.local/core -out /app/.local/invitations.json
```

Deliver each entry in that private file to its intended participant through the
assumed secure channel. A manual browser must have a route into the Docker IPv6
network and trust `.local/core/ca.pem`. These ULA addresses are not reachable from
the public Internet. Never bypass certificate warnings.

```sh
./demo/run.sh down
```

This verifies removal of the lab container and network. It retains `.local/`,
including plaintext messages, keys and invitations. Removal is not secure erasure.
The supervisor also limits the running lab to four hours; after that, use `down`
to remove the stopped container and network.

## Optional public rehearsal

Follow [the remote relay guide](cloud/README.md) to use ten owned CPU pods. Each pod
runs a Debuglet executor and relay controller; the core and private dispatcher stay
on the trusted controller host. Browser TLS passes through the real WASM guest and
an SSH tunnel, terminating only at the core. The test browser must trust the private
CA; do not bypass verification.

This emulates address changes using a fixed public IPv4 fleet, not public IPv6 or
a fresh VM per session. Each entrance chooses randomly among currently free nodes;
occupied slots are excluded, and failed cleanup keeps a slot reserved. Previously
used nodes become eligible once cleanup succeeds. Pods remain billable until
provider deletion is confirmed.

Authenticated `/api/cover` requests support bounded synthetic traffic profiles;
they do not store messages or demonstrate indistinguishability. Optional `-events`
logging records entry changes, participant labels, timestamps and byte counts,
but no credentials or message bodies. This private log reveals the relationship
between entrances and must not be published.

## First-use enrollment

The optional welcome service lets a visitor request their first entrance directly
from the core. Run it behind a trusted HTTPS front door, separate from the private
administration API:

```sh
mailbox welcome -state .local/core -admin http://127.0.0.1:9200 \
  -listen 127.0.0.1:29210 -origin https://core.example
```

The visitor clicks **Create my entrance**, receives an address and passphrase,
then opens the relay and signs in. The demo CA download is on the welcome page;
the relay still requires certificate verification in a dedicated test browser.
The HTTPS front door is trusted with enrollment credentials if it terminates TLS.

Enrollment creates a conversation with two entrances: one for the visitor and one
for the core operator to reply. Only the visitor's invitation is returned publicly;
the pair is saved privately under the core's `enrollments/` directory. Enrollment
is bounded to one request at a time and five attempts per minute, with the normal
core capacity and expiry limits. Refreshing the welcome page does not recover an
old mailbox; retain your invitation or prepare a successor inside the mailbox.

## Lifetime and recovery

- Default entrance lifetime: ten minutes. Set `SESSION_TTL=5m` before `up` to change
  it; supported range is ten seconds to twenty minutes.
- A prepared successor starts immediately and has its own expiry. Save both its
  address and passphrase before closing the current entrance. Closing a browser
  tab alone leaves the entrance alive until expiry.
- Closing an entrance retires its listener, active connections and guest run.
  Messages remain at the core. Each entrance permits one active login token;
  signing in again invalidates its previous token.
- If the close response is lost, the page clears its local credentials and reports
  that closure is unconfirmed. It does not claim that the remote entrance stopped.
- After a core restart, use `mailbox resume -from <previous-invitations.json>
  -out <new-invitations.json>` with the same `-state` directory. The administrator
  must securely deliver the replacement invitations. Old passphrases do not resume.
- There are at most sixteen conversations, one hundred messages per conversation,
  four KiB per message and eight MiB of serialized state. Full storage rejects
  further writes. There is no automatic message expiry or archival system.

## Code

The core uses Go's standard library; the browser uses plain HTML, CSS and JavaScript.
There is no frontend framework or database server.

| Files | Responsibility |
| --- | --- |
| `core.go` | Entry lifecycle, authentication, mailbox API and successor handoff |
| `store.go`, `tls.go` | Atomic private state and local certificate authority |
| `main.go`, `runtime.go` | Local administrative CLI and runtime connection |
| `fleet.go`, `events.go` | Remote entrance allocation and optional private telemetry |
| `welcome.go` | Public first-use enrollment, separate from the private administrator API |
| `relay/main.go` | Opaque TCP forwarding inside the real WASM guest |
| `relay/cmd/node/` | Guest submission, IPv6 aliases and connection cleanup |
| `web/` | Small browser mailbox |
| `demo/`, `tests/browser/`, `cloud/` | Local lab, browser checks and remote setup |

The Go tests exercise actual TLS/HTTP with provisioning replaced by a fixture.
The Docker browser rehearsal uses the actual dispatcher, executor and WASM relay.
These are distinct evidence levels; neither is a public-network anonymity test.

For focused development checks on Linux with Go 1.26.10, Python 3 and OpenSSL:

```sh
go test -race ./...
(cd relay && go test -race ./cmd/node)
python3 -m unittest discover -s traffic -p 'test_*.py'
```

Keep keys, invitations, mailbox state and rehearsal output under the ignored
`.local/` directory. Do not publish them alongside the source.

## License

Apache-2.0. Debuglet and other dependencies retain their own licenses.
