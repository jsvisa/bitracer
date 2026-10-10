#!/usr/bin/env bash
# Build host artifacts for the docker image.
# Go cross-compiles to linux; adjust GOARCH if deploying on amd64.
# Note: the Dockerfile apk-adds chromium (graph snapshot), so the image
# build itself needs network.
set -euo pipefail
cd "$(dirname "$0")"
( cd web && pnpm install && pnpm build )
GOOS=linux GOARCH=arm64 CGO_ENABLED=0 go build -o build/bitracer ./cmd/bitracer
echo "build/bitracer + web/dist ready"
