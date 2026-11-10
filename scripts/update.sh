#!/usr/bin/env bash
# One-command update on the panel VPS: pull, rebuild, replace the binary,
# restart. Data in /var/lib/vps-panel is never touched.
set -euo pipefail
cd "$(dirname "$0")/.."

[ "$(id -u)" = "0" ] || { echo "请用 root 运行"; exit 1; }

echo "==> 拉取最新代码"
git pull --ff-only

if command -v go >/dev/null 2>&1; then
  echo "==> 重新构建"
  ./scripts/build.sh
fi

ARCH=$(uname -m)
case "$ARCH" in
  x86_64) ARCH=amd64 ;;
  aarch64|arm64) ARCH=arm64 ;;
esac
BIN="dist/vps-panel-linux-$ARCH"
[ -f "$BIN" ] || { echo "缺少 $BIN（需要 Go 构建或预先同步 dist/）"; exit 1; }

echo "==> 替换二进制并重启（数据目录 /var/lib/vps-panel 不受影响）"
install -m 755 "$BIN" /opt/vps-panel/vps-panel
systemctl restart vps-panel
sleep 1
systemctl is-active vps-panel
echo "更新完成，版本： $(/opt/vps-panel/vps-panel -version)"
