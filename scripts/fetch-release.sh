#!/usr/bin/env bash
# Download the panel binary for this machine from the latest GitHub release.
# Used by install.sh / update.sh when there is no Go toolchain and no
# prebuilt binary in dist/ (dist/ is gitignored, so a plain `git clone`
# never contains it). The repo is public, so the primary path is a plain
# unauthenticated curl of the release asset — no gh, no token. The gh and
# GITHUB_TOKEN paths remain as fallbacks. Exits 0 once the binary exists.
set -euo pipefail
cd "$(dirname "$0")/.."

ARCH=$(uname -m)
case "$ARCH" in
  x86_64) ARCH=amd64 ;;
  aarch64|arm64) ARCH=arm64 ;;
  *) echo "不支持的架构 $ARCH"; exit 1 ;;
esac

BIN="dist/vps-panel-linux-$ARCH"
[ -f "$BIN" ] && exit 0
mkdir -p dist

REPO="hoolu0/vps-panel"
ASSET="vps-panel-linux-$ARCH"

if curl -fsSL -o "$BIN" "https://github.com/$REPO/releases/latest/download/$ASSET"; then
  echo "==> 已从 GitHub Release 下载 $BIN（免登录）"
elif command -v gh >/dev/null 2>&1; then
  echo "==> 从 GitHub Release 下载 $BIN（gh）"
  gh release download --repo "$REPO" --pattern "$ASSET" --dir dist --clobber
elif [ -n "${GITHUB_TOKEN:-}" ]; then
  echo "==> 从 GitHub Release 下载 $BIN（GITHUB_TOKEN）"
  URL=$(curl -fsSL -H "Authorization: Bearer $GITHUB_TOKEN" \
    -H "Accept: application/vnd.github+json" \
    "https://api.github.com/repos/$REPO/releases/latest" |
    python3 -c "import json,sys; print([a['url'] for a in json.load(sys.stdin)['assets'] if a['name'] == '$ASSET'][0])")
  curl -fsSL -H "Authorization: Bearer $GITHUB_TOKEN" \
    -H "Accept: application/octet-stream" -o "$BIN" "$URL"
else
  echo "从 GitHub Release 下载失败：请检查本机到 github.com 的网络"
  exit 1
fi

[ -f "$BIN" ] || { echo "下载后仍缺少 $BIN"; exit 1; }
chmod +x "$BIN"
echo "已下载 $BIN"
