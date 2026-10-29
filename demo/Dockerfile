# Linux amd64 development environment; runtime never joins a public network.
FROM golang:1.26.10-bookworm@sha256:2bba1f6b063d0e09c5297fbd0a7b9664c4931afc4476a779d7c9a3062704f949
RUN apt-get update && apt-get install -y --no-install-recommends \
    ca-certificates curl iproute2 python3 openssl \
    && rm -rf /var/lib/apt/lists/*
