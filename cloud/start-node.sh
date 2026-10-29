#!/usr/bin/env bash
# Runs only on a disposable relay pod, with no core state or TLS private keys.
set -euo pipefail
cd /workspace/mailbox
pids=()
cleanup() {
  trap - EXIT TERM INT
  for pid in "${pids[@]}"; do kill -TERM "$pid" 2>/dev/null || true; done
  for pid in "${pids[@]}"; do wait "$pid" 2>/dev/null || true; done
}
trap cleanup EXIT TERM INT
if [[ ! -f state/executor.sqlite ]]; then
  bin/debuglet-executor -init-database state/executor.sqlite > state/init.log 2>&1
fi
rm -f state/executor-ready.json
bin/debuglet-executor -config executor.toml -ready-file state/executor-ready.json > state/executor.log 2>&1 &
pids+=("$!")
for _ in $(seq 1 100); do
  [[ -f state/executor-ready.json ]] && break
  sleep 0.2
done
[[ -f state/executor-ready.json ]] || { echo 'Executor not ready' >&2; exit 1; }
read -r executor < executor-id
read -r public_url < public-url
read -r backend_port < backend-port
bin/node -dispatcher http://127.0.0.1:19000 -executor "$executor" \
  -wasm bin/relay.wasm -token runtime.token -public-url "$public_url" \
  -backend-port "$backend_port" > state/controller.log 2>&1 &
pids+=("$!")
wait -n "${pids[@]}"
