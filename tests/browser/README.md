# Browser acceptance

With the owned lab running, use `tests/browser/check.sh` on the lab host. It builds
a cached browser image, creates two fresh private invitations and runs the browser
on the isolated IPv6 network. It removes its browser container on exit and leaves
the lab running; test entrances expire normally. Run `demo/run.sh down` afterward.

The browser first rejects the untrusted lab certificate, then trusts that specific
CA in a disposable NSS database. HTTPS verification remains enabled throughout.

Private evidence lives under `.local/browser-evidence`: screenshots of the test
conversation, a JSON summary and logs. Keep invitations and evidence private.
Checks cover local IPv6, actual login/message exchange, successor continuity,
acknowledged close, HTML escaping, responsive layout and transport failures. Named
UI fault-injection checks also cover delayed preparation, refresh ordering and
page restoration. Runtime tests separately establish endpoint and relay removal.

This does not establish forensic erasure, censorship resistance or public IPv6
reachability. Browser restoration is simulated; no forensic claim follows from it.

`operator.mjs` checks the operator dashboard against intercepted responses, with
no live credentials or core. It covers polling, draft preservation, ambiguous
replies, logout, expired credentials, literal rendering and mobile layout. With
the browser image above already built, run from the repository root:

```sh
mkdir -p .local/operator-evidence
docker run --rm --network none --cpus=2 --memory=2g \
  -v "$PWD/tests/browser/operator.mjs:/check/operator.mjs:ro" \
  -v "$PWD/web:/web:ro" -v "$PWD/.local/operator-evidence:/evidence" \
  --entrypoint node ephemeral-mailbox-browser:local /check/operator.mjs
```

These UI checks do not replace an actual visitor-to-core message and reply
rehearsal. Backend tests separately exercise the operator API and visitor TLS.
