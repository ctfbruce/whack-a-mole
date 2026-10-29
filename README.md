# Ephemeral mailbox

A small research prototype: two people exchange messages through temporary IPv6
websites. Each entrance has its own address, passphrase and lifetime. A person can
prepare their next entrance, close the current one, and continue the same conversation.

The trusted core retains messages. A real Debuglet WebAssembly program forwards
the browser's encrypted connection; it does not decrypt messages. This project is
separate from Debuglet and changes none of its source.

```text
Browser → random IPv6 address → TCP bridge → Debuglet WASM relay → trusted core
        └──────────────── verified TLS to the core ─────────────────┘
```

The small native bridge is necessary because Debuglet's local listener policy
accepts loopback peers. The Docker lab supplies IPv6 addressing and isolation;
Debuglet supplies the bounded guest runtime. No host ports are published.

**This demonstrates a mechanism, not a safe whistleblowing service.** The core
reads messages; the active website is identifiable; changing addresses does not
hide traffic or erase device records. Read [the threat model](THREAT_MODEL.md).

## Run the local lab

Requires Linux amd64, Docker with IPv6 networking, and Git. Initial image and
dependency downloads need Internet access; the running lab uses an internal network.

Obtain the Debuglet source used by this prototype:

```sh
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

## Lifetime and recovery

- Default entrance lifetime: ten minutes. Set `SESSION_TTL=5m` before `up` to change
  it; supported range is ten seconds to twenty minutes.
- A prepared successor starts immediately and has its own expiry. Save both its
  address and passphrase before closing the current entrance. Closing a browser
  tab alone leaves the entrance alive until expiry.
- Closing an entrance retires its listener, active connections and guest run.
  Messages remain at the core. Each entrance permits one active login token;
  signing in again invalidates its previous token.
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
| `relay/main.go` | Opaque TCP forwarding inside the real WASM guest |
| `relay/cmd/node/` | Guest submission, IPv6 aliases and connection cleanup |
| `web/` | Small browser mailbox |
| `demo/`, `tests/browser/` | Isolated lab and reproducible acceptance checks |

The Go tests exercise actual TLS/HTTP with provisioning replaced by a fixture.
The Docker browser rehearsal uses the actual dispatcher, executor and WASM relay.
These are distinct evidence levels; neither is a public-network anonymity test.
