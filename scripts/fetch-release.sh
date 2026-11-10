#!/usr/bin/env bash
# Download the panel binary for this machine from the latest GitHub release.
# Used by install.sh / update.sh when there is no Go toolchain and no
# prebuilt binary in dist/ (dist/ is gitignored, so a plain `git clone`
# never contains it). The repo is private, so this needs either `gh`
# logged in (preferred) or GITHUB_TOKEN set. Exits 0 once the binary exists.
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

if command -v gh >/dev/null 2>&1; then
  echo "==> 从 GitHub Release 下载 $BIN（gh）"
  gh release download --pattern "vps-panel-linux-$ARCH" --dir dist --clobber
elif [ -n "${GITHUB_TOKEN:-}" ]; then
  echo "==> 从 GitHub Release 下载 $BIN（GITHUB_TOKEN）"
  URL=$(curl -fsSL -H "Authorization: Bearer $GITHUB_TOKEN" \
    -H "Accept: application/vnd.github+json" \
    https://api.github.com/repos/hoolu0/vps-panel/releases/latest |
    python3 -c "import json,sys; print([a['url'] for a in json.load(sys.stdin)['assets'] if a['name'] == 'vps-panel-linux-$ARCH'][0])")
  curl -fsSL -H "Authorization: Bearer $GITHUB_TOKEN" \
    -H "Accept: application/octet-stream" -o "$BIN" "$URL"
  chmod +x "$BIN"
else
  echo "下载 Release 需要 gh 登录或 GITHUB_TOKEN（仓库是私有的）"
  exit 1
fi

[ -f "$BIN" ] || { echo "下载后仍缺少 $BIN"; exit 1; }
echo "已下载 $BIN"
