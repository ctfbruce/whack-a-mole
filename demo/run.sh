#!/usr/bin/env bash
# Owned IPv6 laboratory, no host address changes or published ports.
set -euo pipefail
cd "$(dirname "$0")/.."
name=ephemeral-mailbox-lab
network=ephemeral-mailbox-ipv6
image=${GO_IMAGE:-debuglet-ci-tools:20261021-1}
case "${1:-}" in
  up)
    : "${DEBUGLET_BIN:?Set DEBUGLET_BIN to the pinned installed package bin directory}"
    [[ -x .local/mailbox && -x .local/node && -f .local/relay.wasm ]] || { echo 'Run demo/build.sh first' >&2; exit 1; }
    docker network create --internal --ipv6 --subnet fd6d:626f:7800:1::/64 --label research.ephemeral-mailbox=true "$network" >/dev/null
    trap 'docker network rm "$network" >/dev/null 2>&1 || true' ERR
    docker run -d --name "$name" --label research.ephemeral-mailbox=true \
      --network "$network" --ip6 fd6d:626f:7800:1::2 --cap-add NET_ADMIN \
      --cpus 2 --memory 2g --pids-limit 256 --sysctl net.ipv6.conf.all.disable_ipv6=0 \
      -e SESSION_TTL="${SESSION_TTL:-10m}" -v "$PWD:/app" -v "$DEBUGLET_BIN:/dbin:ro" \
      --tmpfs /state:rw,nosuid,nodev,size=128m "$image" \
      timeout --signal=TERM --kill-after=15s 4h bash /app/demo/start.sh >/dev/null
    echo 'Lab started; hard runtime ceiling: four hours. No public endpoint.'
    ;;
  down)
    docker rm -f "$name" >/dev/null 2>&1 || true
    docker network rm "$network" >/dev/null 2>&1 || true
    echo 'Lab container and network removed; local mailbox state retained.'
    ;;
  status)
    docker inspect "$name" --format '{{.State.Status}}'
    ;;
  *) echo 'Usage: demo/run.sh up|down|status' >&2; exit 2;;
esac
