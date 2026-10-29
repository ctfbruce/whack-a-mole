# Threat model

This is a local research prototype, not a service ready for sensitive disclosures.
It tests whether a browser can exchange messages through an expiring IPv6 entry
point, then move to a new entry while its conversation stays at a trusted core.
It does not establish anonymity or censorship resistance.

## What is trusted

- The core, its administrator, and its host are trusted with message plaintext,
  participant identities, session credentials, and the mapping between entries.
- Initial invitations and the lab CA certificate arrive through an authentic,
  confidential channel. This prototype does not solve that bootstrap problem.
- The browser, operating system, and user device are uncompromised. The browser
  validates the entry's IPv6 address against its certificate and trusts only the
  intended lab CA for this test. Certificate warnings must not be bypassed.
- The core supplies the browser's JavaScript. A malicious or compromised core
  could alter that code or read messages; transport encryption cannot prevent it.
- The local runtime controller, Debuglet control plane, container administrator,
  and lab host are trusted to provision and retire entries. A privileged operator
  on this shared host is not an untrusted relay: it can access the core's files.

## Data path

```text
Browser -- IPv6 --> native bridge --> Debuglet WASM relay --> core
        <------------- one TLS connection ------------------>
```

The bridge and real Debuglet guest forward bytes without terminating browser TLS.
The core terminates TLS using a short-lived certificate for that entry's IPv6
address. The native bridge is necessary because the current Debuglet local-target
policy accepts loopback peers, not the lab's ULA ingress address.

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
- Messages are retained at the core across entry changes. The successor has a new
  random IPv6 address and passphrase; repeated requests on one entry return the
  same successor. The successor's lifetime starts when it is prepared.
- Closing or expiring an entry retires its listener and existing connections.
  Runtime expiry bounds its life even if normal cleanup fails. Core restart
  invalidates old sessions; an administrator must deliver fresh invitations.
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
There is no mixing, cover traffic, or demonstrated traffic mimicry.

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

The demo uses `fd6d:626f:7800:1::/64`, a locally routed IPv6 ULA prefix, and a private
lab CA installed only in the test client's trust store. These addresses are not
public Internet destinations, and ordinary browsers do not trust this CA by default.

Public deployment would need routed address space, authenticated browser trust,
reachability, independent-host isolation, and a separate operational threat model.
Debuglet starts guest programs on existing executors; it does not supply a fresh
public IP address per program. IPv6 address changes alone do not hide the containing
network or make blocking costly. The lab establishes functional relay and session
behaviour only, not safety for journalists or whistleblowers.
