#!/usr/bin/env bash
# Build host artifacts for the docker image (network-free docker build).
# Go cross-compiles to linux; adjust GOARCH if deploying on amd64.
set -euo pipefail
cd "$(dirname "$0")"
( cd web && pnpm install && pnpm build )
GOOS=linux GOARCH=arm64 CGO_ENABLED=0 go build -o build/bitracer ./cmd/bitracer
echo "build/bitracer + web/dist ready"
