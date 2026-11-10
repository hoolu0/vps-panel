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
if command -v go >/dev/null 2>&1; then
  # 上面已重新构建；构建失败会因 set -e 直接退出
  [ -f "$BIN" ] || { echo "构建后仍缺少 $BIN"; exit 1; }
else
  # 没有 Go 时必须重新下载：dist/ 里残留的是上次安装的旧二进制，
  # 不删掉它 fetch-release.sh 会直接退出，更新就等于没更新
  rm -f "$BIN"
  ./scripts/fetch-release.sh || { echo "缺少 $BIN（需要 Go 构建、预先同步 dist/，或从 GitHub Release 下载）"; exit 1; }
fi

echo "==> 替换二进制并重启（数据目录 /var/lib/vps-panel 不受影响）"
install -m 755 "$BIN" /opt/vps-panel/vps-panel
systemctl restart vps-panel
sleep 1
systemctl is-active vps-panel
echo "更新完成，版本： $(/opt/vps-panel/vps-panel -version)"
