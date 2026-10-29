# Public relay rehearsal

This optional deployment replaces the local IPv6 aliases with ten disposable
CPU machines, each with a **verified distinct public IPv4 address**. It emulates
address changes; it does not demonstrate native public IPv6 or anonymity.

The trusted core and a private Debuglet dispatcher stay on the controller host.
Each pod runs an unmodified Debuglet executor and the small node controller.
The node controller launches the real relay WASM when an entrance is issued.

```
browser → pod's mapped TCP port → relay WASM → SSH tunnel → core TLS listener
```

TLS still ends at the core. Pods receive neither mailbox state nor TLS private
keys. Pod administrators and the runtime control plane are trusted in this
rehearsal: the SSH tunnels also expose the private dispatcher to each pod. This
is not a deployment design for arbitrary untrusted relay hosts. Each pod can carry one live entrance, and only to its configured core
port. Closing or expiring an entrance closes the public listener and cancels
that run. A later entrance uses another available pod. Provisioning keeps the
pods alive until the fleet deadline; it does not allocate a new VM per message.

## Prepare and start

Build `node` and `relay.wasm` with `demo/build.sh` on the Linux controller. Use
an executor binary from the same pinned Debuglet source. Start the owned
Debuglet dispatcher on loopback ports 29000/29001; its local development mode
must never be exposed publicly.

Provision exactly ten CPU pods using your cloud tooling. Arm automatic provider
deletion **before** creating the fleet. The state passed to `setup.py` must have:

- `inventory.json` with a fixed `expires_at`, `terminate: false`, and ten `pods`;
- each pod's `id`, `role` (`relay-1` … `relay-10`), `public_ip`, `ssh_port`
  and `public_port` (the provider's TCP mapping of container port 8443);
- a private `ssh_key` and `known_hosts` entries pinned by pod ID, verified
  through the provider's authenticated control plane.

`setup.py` does not allocate machines or change a production deployment:

```sh
python3 cloud/setup.py --state /private/provider-state \
  --output /private/mailbox-fleet --node .local/node --guest .local/relay.wasm \
  --executor /path/to/debuglet-executor
```

It copies verified binaries, launches a supervisor and creates SSH tunnels.
Core configuration is `/private/mailbox-fleet/fleet.json`. Core listens on
`[::1]:29301` … `29310`; node controllers are available through local forwarded
ports `29401` … `29410`. Controller tokens and invitation passphrases are
private files, never links to publish or commit.

The private CA must be trusted by the test browser. Do not disable certificate
verification. Public IPs, timing, traffic size and the relay/core relationship
remain observable; see [the threat model](../THREAT_MODEL.md).

## Stop

Use your fleet's provider deletion command and require every exact pod ID to be
absent. Stopping a process or container is not proof that cloud billing ended.
The supervisor's fixed deadline also stops the local runtime, but **does not
substitute for provider deletion**. Stop the recorded SSH tunnel processes on
the controller after cleanup. Preserve only the evidence and core state you
actually need; both may contain sensitive metadata.
