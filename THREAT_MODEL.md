# Threat model

This is a research prototype, not a service ready for sensitive disclosures.
It tests messaging through expiring entries: local IPv6 addresses, or an optional
ten-pod public IPv4 fleet. Conversations stay at a trusted core while entries change.
Neither mode establishes anonymity or censorship resistance.

## What is trusted

- The core, its administrator, and its host are trusted with message plaintext,
  participant identities, session credentials, and the mapping between entries.
- Initial invitations and the private CA certificate arrive through an authentic,
  confidential channel. This prototype does not solve that bootstrap problem.
- The optional welcome page supplies the initial invitation over the configured
  HTTPS front door. Any proxy terminating that TLS connection is trusted with the
  first passphrase and CA download. The rehearsal uses such a trusted bootstrap
  provider; later relay connections still terminate TLS at the core.
- The browser, operating system, and user device are uncompromised. The browser
  validates the entry's IP address against its certificate and trusts the intended
  private CA for this test. Certificate warnings must not be bypassed.
- The core supplies the browser's JavaScript. A malicious or compromised core
  could alter that code or read messages; transport encryption cannot prevent it.
- Runtime controllers, the Debuglet dispatcher, pod administrators, and the lab
  host are trusted to provision, constrain and retire entries. An administrator
  of the core host can read its files. Remote pods receive neither mailbox state
  nor core TLS keys, but their operators see metadata and can disrupt forwarding.

## Data path

```text
Browser -- IPv6 --> native bridge --> Debuglet WASM relay --> core
        <------------- one TLS connection ------------------>
```

The bridge and real Debuglet guest forward bytes without terminating browser TLS.
The native Go core terminates TLS using a short-lived certificate for the entry's
IP address. In the local lab, a bridge admits ULA traffic to Debuglet's loopback
listener. In the public rehearsal, a pod's mapped TCP port reaches its real WASM
relay, which forwards through an SSH tunnel to the core. Control APIs are reached
through authenticated loopback SSH forwards, not exposed public HTTP listeners.

An adversary restricted to forwarding or observing this path can see connection
metadata and interrupt service. With uncompromised keys and successful certificate
validation, it cannot read or alter application plaintext without detection.
This is encryption between browser and core, **not end-to-end encryption between
the two correspondents**. The core reads and stores their messages.

## Intended protections

- Each invitation binds a passphrase to one participant and conversation. The
  browser cannot select an arbitrary conversation or sender identity.
- After login, a bearer token authorizes that entry's requests. Possessing an
  invitation or token grants its access; these are credentials, not identity proof.
- Messages are retained across entry changes. A successor uses a random local IPv6
  address or a randomly selected free fleet IP, plus a new passphrase. Repeated requests on one
  entry return the same successor, whose lifetime starts when it is prepared.
- Closing or expiring an entry retires its listener and existing connections.
  Runtime expiry bounds its life even if normal cleanup fails. A fleet slot stays
  reserved until cleanup is confirmed. Core restart invalidates old sessions;
  an administrator must deliver fresh invitations.
- The page uses no third-party resources, persistent browser storage, or cookies.
  Credentials stay in page memory and are not placed in URLs. Responses request
  `no-store`; these choices reduce retention, not guarantee its absence.
- Core state is written atomically with restrictive file permissions. It retains
  plaintext messages, sender labels, and timestamps. File permissions do not
  protect against the core administrator or host compromise.
- Conversation, message, session, and lifetime limits bound the prototype's
  resource use. They do not ensure availability against a determined adversary.

These are design properties to check in the rehearsal, not a security audit.

## What this does not hide or prevent

An observer can record source and destination IPs, packet sizes, timing, and
connection duration. A relay can correlate its ingress and egress. A powerful
observer may correlate sessions despite their changing destination addresses.
There is no mixing or demonstrated traffic mimicry. Optional authenticated
synthetic traffic varies application sizes and timing; it is not a validated
cover-traffic defense. Private event logs deliberately link entries to participant
labels and record timing and byte counts, though not credentials or message bodies.

The public entry page is recognisable without a passphrase. An adversary that
discovers an active address can fetch it, block it, or deny service. Random address
selection is not proof of resistance to scanning, endpoint enumeration, Sybil
accounts, prefix blocking, or traffic analysis.

No installed messaging app is needed, but visiting a page can leave browser, OS,
clipboard, screenshot, synchronization, device-backup, or network records. Closing
an entry does not erase previously collected records. Memory clearing, file
removal, and container teardown are not secure deletion guarantees.

There is no protection against a malicious correspondent, stolen credentials,
coercion, compromised devices, or identification through message contents. Failed
or interrupted requests may have uncertain delivery: a lost response does not
prove that the core did not save the message. Re-sending can create duplicates.

## Local proof versus an Internet service

The local demo uses `fd6d:626f:7800:1::/64`, a locally routed IPv6 ULA prefix. These
addresses are not public Internet destinations. The optional cloud rehearsal uses
ten distinct public IPv4 addresses, reusing idle pods instead of allocating a new
VM per session. Both use a private CA that ordinary browsers do not trust by default.

An operational service would need browser trust distribution, availability and
isolation validation, and a separate operational threat model.
Debuglet starts guest programs on existing executors; it does not supply a fresh
public IP address per program. IPv6 address changes alone do not hide the containing
network or make blocking costly. These rehearsals test functional relay and session
behaviour, not safety for journalists or whistleblowers.
