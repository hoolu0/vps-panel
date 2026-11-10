#!/usr/bin/env bash
# Build agent binaries (embedded into the panel), the panel and a
# standalone agent into dist/.
set -euo pipefail
cd "$(dirname "$0")/.."
export CGO_ENABLED=0 GOTOOLCHAIN="${GOTOOLCHAIN:-local}"
mkdir -p dist internal/panel/agentbin

for arch in amd64 arm64; do
  echo "building agent linux/$arch"
  GOOS=linux GOARCH=$arch go build -trimpath -ldflags "-s -w" \
    -o dist/vps-agent-linux-$arch ./cmd/agent
  cp dist/vps-agent-linux-$arch internal/panel/agentbin/vps-agent-linux-$arch
done

for arch in amd64 arm64; do
  echo "building panel linux/$arch (with embedded agents)"
  GOOS=linux GOARCH=$arch go build -trimpath -ldflags "-s -w" \
    -o dist/vps-panel-linux-$arch ./cmd/panel
done

# restore committed placeholders so the working tree stays clean
printf 'placeholder - replaced by scripts/build.sh\n' > internal/panel/agentbin/vps-agent-linux-amd64
printf 'placeholder - replaced by scripts/build.sh\n' > internal/panel/agentbin/vps-agent-linux-arm64

echo "done: $(ls dist/)"
