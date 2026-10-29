# Controlled background traffic

This client sends bounded padding exchanges to an **owned mailbox entrance**.
It does not send messages, visit unrelated websites, disguise a particular
protocol, or establish that a session is inconspicuous. An observer still sees
the entrance address, timing and encrypted packet sizes. More traffic costs
bandwidth and may make a session more distinctive.

Use the token from an already authenticated session. Do not log in again beside
an active browser: a new login replaces that entrance's current token. Save this
private JSON with owner-only permissions:

```json
{"url":"https://[YOUR_OWNED_ADDRESS]:8443","token":"SESSION_TOKEN","expires_at":"2026-10-29T17:00:00Z"}
```

Run from the same owned client environment that can reach the entrance:

```sh
python3 traffic/run.py --session .local/session.json --ca .local/core/ca.pem \
  --allow-url 'https://[YOUR_OWNED_ADDRESS]:8443' \
  --profile browsing --seconds 60 --trace .local/cover.jsonl
```

The URL must exactly match an explicit `--allow-url`. Certificate-chain and
hostname/IP verification stay enabled; redirects and environment proxies are
disabled. The client only calls authenticated `POST /api/cover`. It stops on
expiry, a failed exchange, SIGINT/SIGTERM, its byte budget or duration. An
exchange has a ten-second total deadline, also cut short by the run duration or
cancellation. A short-lived worker process makes this bound apply to DNS, TLS
and slow response bodies; its termination may add a small scheduling delay.
The original token is not printed or written into the trace or process arguments.

`idle` makes sparse exchanges, `browsing` makes small bursts and pauses, and
`steady` paces padding. These names describe timing shapes, not measured real
users. Every profile is an ordinary Python module:

```python
def next_request(rng, index):
    return rng.uniform(1, 3), 1024, 16384  # delay, padding bytes, response bytes
```

Pass a trusted file with `--profile ./my_profile.py`. Profile files execute
Python; do not load untrusted modules. `--seed` makes the proposed schedule
repeatable, although actual network timing still varies. All profiles are
bounded by `--max-rate` (default 2, maximum 5 requests/s), `--max-bytes` (default
4 MiB, maximum 16 MiB), and `--seconds` (maximum 1,200). A request is skipped
if its complete requested response would exceed the remaining byte budget.
No running profile automatically follows or requests a successor entrance.

## Trace and comparison

Each JSONL row contains `elapsed_s`, `direction` (`tx`/`rx`), `app_bytes`,
`latency_ms`, `outcome`, `profile`, `request_id` and `kind` (`cover`). It contains
no token, address, passphrase or payload. TX counts offered application request
bodies; RX counts fully read application response bodies. These are **not wire
bytes or proof of delivery**; partial failed responses may be undercounted.
Trace files are created with mode 0600 and never overwrite existing evidence.

For an actual comparison, record the user's real mailbox HTTP body sizes with
the same schema and `kind:"message"`. Give both recorders the same UNIX time
origin, passing it to the generator with `--started-at`. Then:

```sh
python3 traffic/compare.py --baseline .local/messages.jsonl \
  --cover .local/cover.jsonl --out .local/comparison.html
```

The charts show the mailbox component and that same component plus the measured
background component. They do not invent ordinary-traffic samples or assert
statistical indistinguishability. Send traffic only to controlled lab targets.
