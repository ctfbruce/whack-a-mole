#!/usr/bin/env bash
# Run on a Linux amd64 Docker host. No changes to the Debuglet checkout.
set -euo pipefail
cd "$(dirname "$0")/.."
: "${DEBUGLET_SOURCE:?Set DEBUGLET_SOURCE to the pinned Debuglet source checkout}"
expected=a060c5c03a200feff4ecc17c09d7dcba21e20617
[[ $(git -C "$DEBUGLET_SOURCE" rev-parse HEAD) == "$expected" ]] || { echo 'Wrong Debuglet source revision' >&2; exit 1; }
mkdir -p .local
chmod 700 .local
image=${GO_IMAGE:-ephemeral-mailbox-tools:local}
if ! docker image inspect "$image" >/dev/null 2>&1; then
  docker build -t "$image" -f demo/Dockerfile demo
fi
# Dependency downloads are allowed during compilation, never in the live lab.
docker run --rm --network "${BUILD_NETWORK:-bridge}" --cpus 2 --memory 3g \
  -e GOTOOLCHAIN=local -e GOMODCACHE=/go/pkg/mod -e GOCACHE=/root/.cache/go-build \
  -e BUILD_DEBUGLET="${BUILD_DEBUGLET:-1}" \
  -v "${MAILBOX_GO_MOD:-ephemeral-mailbox-go-mod}:/go/pkg/mod" \
  -v "${MAILBOX_GO_BUILD:-ephemeral-mailbox-go-build}:/root/.cache/go-build" \
  -v "$PWD:/app" -v "$DEBUGLET_SOURCE:/debuglet:ro" -w /app "$image" \
  sh -eu -c '
    export PATH=/usr/local/go/bin:$PATH
    go build -o .local/mailbox .
    cd relay
    cp go.mod ../.local/relay.mod
    cp go.sum ../.local/relay.sum
    go mod edit -modfile ../.local/relay.mod -replace github.com/netsec-ethz/debuglet=/debuglet
    go build -mod=mod -modfile ../.local/relay.mod -o ../.local/node ./cmd/node
    GOOS=wasip1 GOARCH=wasm go build -mod=mod -modfile ../.local/relay.mod -o ../.local/relay.wasm .
    if [ "$BUILD_DEBUGLET" = 1 ]; then
      cd /debuglet
      mkdir -p /app/.local/debuglet
      go build -mod=readonly -o /app/.local/debuglet/debuglet-dispatcher ./cmd/dispatcher
      go build -mod=readonly -o /app/.local/debuglet/debuglet-executor ./cmd/executor
    fi
  '
echo 'Built core, controller and real Debuglet WASM relay.'
