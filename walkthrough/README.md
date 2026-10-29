# Private fleet walkthrough

`browser.mjs` uses the real mailbox UI, two private invitations and eight next
entrances. It requires each next entrance to differ from both live entrances, verifies TLS,
checks message continuity and confirms each retired entrance stops responding.
Previously used free nodes may recur; the report records the actual distinct count.
Passphrases are masked in screenshots. It never disables certificate checking.

Run on the owned Linux test host using the existing browser acceptance image.
Mount the private invitations and CA at `/inputs/invitations.json` and
`/inputs/ca.pem`, this script as `/check/walkthrough.mjs`, and an empty evidence
directory at `/evidence`. Use `walkthrough/run.sh` as the entrypoint. This first
checks that the CA is rejected before adding it to a disposable browser trust store.
Set `STARTED_AT` to the same Unix epoch as an accompanying cover-traffic run.
An existing trace or result is refused so retries cannot mix evidence. Browser
navigation failure is correlated with explicit core retirement events in the report.

Build a static report from the real evidence:

```sh
python3 walkthrough/report.py --fleet .local/fleet.json \
  --events .local/events.jsonl --browser .local/walkthrough \
  --traffic .local/traffic-comparison.html --output .local/report/index.html
```

The report includes only public node URLs and allowlisted event fields. Runtime
controller addresses, credentials and token-file paths are excluded. Application
body traces are not packet captures and do not demonstrate indistinguishability.
No report or evidence should be uploaded to a public endpoint.
