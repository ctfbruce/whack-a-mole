# Browser acceptance

Run on the lab host after the demo has created two invitations. The browser first
rejects the untrusted lab certificate, then trusts that specific CA in a disposable
NSS database. HTTPS certificate verification stays enabled throughout.

```sh
docker build -t ephemeral-mailbox-browser:local -f tests/browser/Dockerfile .
mkdir -p .local/browser-evidence
docker run --rm --name ephemeral-mailbox-browser \
  --label research.ephemeral-mailbox=true \
  --network ephemeral-mailbox-ipv6 --cpus=2 --memory=2g --pids-limit=256 \
  -v "$PWD/.local/core/ca.pem:/inputs/ca.pem:ro" \
  -v "$PWD/.local/invitations.json:/inputs/invitations.json:ro" \
  -v "$PWD/.local/browser-evidence:/evidence" ephemeral-mailbox-browser:local
```

Evidence includes screenshots of the test conversation and a bounded JSON summary.
Keep invitations, screenshots and all evidence private. The runner tests local IPv6
routing, actual browser login/message exchange, successor continuity and closure,
HTML escaping, responsive layout, and honest transport-failure handling. It does
not establish forensic erasure, censorship resistance or public IPv6 reachability.
