#!/usr/bin/env bash
# Install the panel on this VPS (run as root, from the repo clone).
# Code goes to /opt/vps-panel, data to /var/lib/vps-panel (never touched
# by updates).
set -euo pipefail
cd "$(dirname "$0")/.."

[ "$(id -u)" = "0" ] || { echo "请用 root 运行"; exit 1; }

ARCH=$(uname -m)
case "$ARCH" in
  x86_64) ARCH=amd64 ;;
  aarch64|arm64) ARCH=arm64 ;;
  *) echo "不支持的架构 $ARCH"; exit 1 ;;
esac

BIN="dist/vps-panel-linux-$ARCH"
if [ ! -f "$BIN" ]; then
  if command -v go >/dev/null 2>&1; then
    ./scripts/build.sh
  elif ! ./scripts/fetch-release.sh; then
    echo "缺少 $BIN：本机没有 Go 工具链，且无法从 GitHub Release 下载（需要 gh 登录或 GITHUB_TOKEN）；请先在有 Go 的机器上运行 scripts/build.sh 后同步 dist/ 过来"
    exit 1
  fi
fi

mkdir -p /opt/vps-panel /var/lib/vps-panel
install -m 755 "$BIN" /opt/vps-panel/vps-panel

cat > /etc/systemd/system/vps-panel.service <<'EOF'
[Unit]
Description=VPS Panel
After=network-online.target
Wants=network-online.target

[Service]
ExecStart=/opt/vps-panel/vps-panel -addr :8080 -data /var/lib/vps-panel
Restart=always
RestartSec=3
User=root

[Install]
WantedBy=multi-user.target
EOF

systemctl daemon-reload
systemctl enable --now vps-panel
sleep 1
systemctl --no-pager status vps-panel | head -5 || true
echo
echo "面板已启动： http://<本机IP>:8080"
echo "首次启动的管理员密码见日志： journalctl -u vps-panel | grep 管理员密码"
echo "（或安装前用 PANEL_ADMIN_PASSWORD 环境变量预设密码重跑本脚本不会重置已有密码）"
