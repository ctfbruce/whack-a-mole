#!/usr/bin/env bash
# Container-only supervisor; its network, addresses and processes die together.
set -euo pipefail
umask 077
mkdir -p /state /app/.local
chmod 700 /state
/dbin/debuglet-dispatcher -init-database /state/dispatcher.sqlite >/state/init-dispatcher.log 2>&1
/dbin/debuglet-executor -init-database /state/executor.sqlite >/state/init-executor.log 2>&1
if [[ ! -f /app/.local/runtime.token ]]; then head -c 32 /dev/urandom | base64 > /app/.local/runtime.token; fi
pids=()
cleanup() {
  trap - EXIT TERM INT
  for pid in "${pids[@]}"; do kill -TERM "$pid" 2>/dev/null || true; done
  for pid in "${pids[@]}"; do wait "$pid" 2>/dev/null || true; done
}
trap cleanup EXIT TERM INT
/dbin/debuglet-dispatcher -config /app/demo/dispatcher.toml -ready-file /state/dispatcher-ready.json > /state/dispatcher.log 2>&1 &
pids+=("$!")
/dbin/debuglet-executor -config /app/demo/executor.toml -ready-file /state/executor-ready.json > /state/executor.log 2>&1 &
pids+=("$!")
for i in $(seq 1 100); do
  if [[ -f /state/dispatcher-ready.json && -f /state/executor-ready.json ]]; then break; fi
  sleep 0.2
done
[[ -f /state/executor-ready.json ]] || { echo 'Debuglet daemons did not become ready' >&2; exit 1; }
/app/.local/node > /state/node.log 2>&1 &
pids+=("$!")
/app/.local/mailbox serve -state /app/.local/core -runtime http://127.0.0.1:9100 -runtime-token /app/.local/runtime.token -listen 127.0.0.1:9200 -ttl "${SESSION_TTL:-10m}" > /state/core.log 2>&1 &
pids+=("$!")
for i in $(seq 1 50); do
  if curl --noproxy '*' -fsS http://127.0.0.1:9200/health >/dev/null; then echo 'Mailbox core and Debuglet runtime ready'; break; fi
  sleep 0.2
done
wait -n "${pids[@]}"
