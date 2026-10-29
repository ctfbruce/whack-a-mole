#!/usr/bin/env bash
set -euo pipefail
cd "$(dirname "$0")/../.."
umask 077
lab=ephemeral-mailbox-lab
network=ephemeral-mailbox-ipv6
client=ephemeral-mailbox-browser
[[ $(docker inspect "$lab" --format '{{.State.Running}} {{index .Config.Labels "research.ephemeral-mailbox"}}' 2>/dev/null) == 'true true' ]] || { echo 'Start the owned lab with demo/run.sh up first.' >&2; exit 1; }
[[ $(docker network inspect "$network" --format '{{index .Labels "research.ephemeral-mailbox"}}' 2>/dev/null) == true ]] || { echo 'The owned IPv6 lab network is missing.' >&2; exit 1; }
[[ -z $(docker ps -aq --filter "name=^/$client$") ]] || { echo 'A browser check is already present; finish it first.' >&2; exit 1; }
evidence="$PWD/.local/browser-evidence"
mkdir -p "$evidence"
echo 'Preparing the cached browser image…'
docker build -t ephemeral-mailbox-browser:local tests/browser >"$evidence/build.log" 2>&1
echo 'Creating two private test invitations…'
docker exec "$lab" /app/.local/mailbox create -state /app/.local/core \
  -names 'Browser source,Browser reporter' -out /app/.local/browser-invitations.json >/dev/null
cleanup() {
  if [[ $(docker inspect "$client" --format '{{index .Config.Labels "research.ephemeral-mailbox"}}' 2>/dev/null) == true ]]; then docker rm -f "$client" >/dev/null; fi
}
trap cleanup EXIT
echo 'Checking verified TLS, message exchange and the next entrance…'
timeout --signal=TERM --kill-after=10s 180s docker run --rm --name "$client" \
  --label research.ephemeral-mailbox=true --network "$network" --cpus=2 --memory=2g --pids-limit=256 \
  -v "$PWD/.local/core/ca.pem:/inputs/ca.pem:ro" \
  -v "$PWD/.local/browser-invitations.json:/inputs/invitations.json:ro" \
  -v "$evidence:/evidence" ephemeral-mailbox-browser:local | tee "$evidence/run.log"
echo 'Browser checks passed. Private screenshots and results: .local/browser-evidence'
