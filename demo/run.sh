#!/usr/bin/env bash
# Owned IPv6 laboratory, no host address changes or published ports.
set -euo pipefail
cd "$(dirname "$0")/.."
name=ephemeral-mailbox-lab
network=ephemeral-mailbox-ipv6
image=${GO_IMAGE:-ephemeral-mailbox-tools:local}
owned() { [[ $(docker "$1" inspect "$2" --format '{{index .Labels "research.ephemeral-mailbox"}}' 2>/dev/null) == true ]]; }
case "${1:-}" in
  up)
    bin=${DEBUGLET_BIN:-"$PWD/.local/debuglet"}
    [[ -x .local/mailbox && -x .local/node && -f .local/relay.wasm && -x "$bin/debuglet-executor" && -x "$bin/debuglet-dispatcher" ]] || { echo 'Run demo/build.sh first' >&2; exit 1; }
    docker network create --internal --ipv6 --subnet fd6d:626f:7800:1::/64 --label research.ephemeral-mailbox=true "$network" >/dev/null
    trap 'if owned network "$network"; then docker network rm "$network" >/dev/null || true; fi' ERR
    docker run -d --name "$name" --label research.ephemeral-mailbox=true \
      --network "$network" --ip6 fd6d:626f:7800:1::2 --cap-add NET_ADMIN \
      --cpus 2 --memory 2g --pids-limit 256 --sysctl net.ipv6.conf.all.disable_ipv6=0 \
      -e SESSION_TTL="${SESSION_TTL:-10m}" -v "$PWD:/app" -v "$bin:/dbin:ro" \
      --tmpfs /state:rw,nosuid,nodev,size=128m "$image" \
      timeout --signal=TERM --kill-after=15s 4h bash /app/demo/start.sh >/dev/null
    trap - ERR
    for i in $(seq 1 100); do
      if docker exec "$name" curl --noproxy '*' -fsS http://127.0.0.1:9200/health >/dev/null 2>&1; then
        echo 'Lab ready; four-hour runtime ceiling. No public endpoint.'; exit 0
      fi
      sleep 0.3
    done
    echo 'Lab did not become ready; inspect its logs, then run demo/run.sh down.' >&2
    exit 1
    ;;
  down)
    if [[ -n $(docker ps -aq --filter "name=^/$name$") ]]; then
      [[ $(docker inspect "$name" --format '{{index .Config.Labels "research.ephemeral-mailbox"}}') == true ]] || { echo 'Refusing to remove an unowned container' >&2; exit 1; }
      docker rm -f "$name" >/dev/null
    fi
    if [[ -n $(docker network ls -q --filter "name=^$network$") ]]; then
      owned network "$network" || { echo 'Refusing to remove an unowned network' >&2; exit 1; }
      docker network rm "$network" >/dev/null
    fi
    [[ -z $(docker ps -aq --filter "name=^/$name$") && -z $(docker network ls -q --filter "name=^$network$") ]] || { echo 'Cleanup incomplete' >&2; exit 1; }
    echo 'Verified lab container and network removed; private local state retained.'
    ;;
  status) docker inspect "$name" --format '{{.State.Status}}';;
  *) echo 'Usage: demo/run.sh up|down|status' >&2; exit 2;;
esac
