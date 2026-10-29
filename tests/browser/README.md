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
