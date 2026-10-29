#!/usr/bin/env bash
# Run on the Linux Docker host. Source is pinned; no changes to Debuglet.
set -euo pipefail
cd "$(dirname "$0")/.."
: "${DEBUGLET_SOURCE:?Set DEBUGLET_SOURCE to the Debuglet source checkout}"
expected=a060c5c03a200feff4ecc17c09d7dcba21e20617
[[ $(git -C "$DEBUGLET_SOURCE" rev-parse HEAD) == "$expected" ]] || { echo 'Wrong Debuglet source revision' >&2; exit 1; }
mkdir -p .local
chmod 700 .local
image=${GO_IMAGE:-debuglet-ci-tools:20261021-1}
docker run --rm --network none --cpus 2 --memory 3g \
  -e GOTOOLCHAIN=local -e GOMODCACHE=/go/pkg/mod -e GOCACHE=/root/.cache/go-build \
  -v debuglet-ci-go-mod:/go/pkg/mod -v debuglet-ci-go-build:/root/.cache/go-build \
  -v "$PWD:/app" -v "$DEBUGLET_SOURCE:/debuglet:ro" -w /app "$image" \
  sh -eu -c '
    export PATH=/usr/local/go/bin:$PATH
    go build -o .local/mailbox .
    cd relay
    cp go.mod ../.local/relay.mod
    test ! -f go.sum || cp go.sum ../.local/relay.sum
    go mod edit -modfile ../.local/relay.mod -replace github.com/netsec-ethz/debuglet=/debuglet
    go build -mod=mod -modfile ../.local/relay.mod -o ../.local/node ./cmd/node
    GOOS=wasip1 GOARCH=wasm go build -mod=mod -modfile ../.local/relay.mod -o ../.local/relay.wasm .
  '
echo 'Built core, controller and real Debuglet WASM relay.'
