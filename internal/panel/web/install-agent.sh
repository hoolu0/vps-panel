#!/usr/bin/env bash
# vps-agent 手动安装脚本：在 VPS 上以 root 执行，下载 agent 二进制并注册
# 为系统服务（systemd / OpenRC 自动检测；都没有时用 nohup 拉起）。
# 本脚本不含任何令牌；面板地址与节点令牌通过参数或环境
# 变量传入（见面板「添加 VPS → 手动安装」生成的命令）。
#
# 用法:
#   bash -s -- --panel <面板URL> --token <节点令牌>
#   PANEL_URL=<面板URL> AGENT_TOKEN=<节点令牌> bash install-agent.sh
# 可重复执行：覆盖二进制与配置并重启服务。
set -euo pipefail

PANEL_URL="${PANEL_URL:-}"
AGENT_TOKEN="${AGENT_TOKEN:-}"

while [ $# -gt 0 ]; do
  case "$1" in
    --panel) PANEL_URL="${2:-}"; shift 2 ;;
    --token) AGENT_TOKEN="${2:-}"; shift 2 ;;
    *) echo "未知参数: $1" >&2; exit 1 ;;
  esac
done

[ "$(id -u)" = "0" ] || { echo "请以 root 运行" >&2; exit 1; }
[ -n "$PANEL_URL" ] || { echo "缺少 --panel <面板URL>" >&2; exit 1; }
[ -n "$AGENT_TOKEN" ] || { echo "缺少 --token <节点令牌>" >&2; exit 1; }
PANEL_URL="${PANEL_URL%/}"

case "$(uname -m)" in
  x86_64) ARCH=amd64 ;;
  aarch64|arm64) ARCH=arm64 ;;
  *) echo "不支持的架构: $(uname -m)" >&2; exit 1 ;;
esac

BIN_DIR=/opt/vps-agent
CFG_DIR=/etc/vps-agent
DATA_DIR=/var/lib/vps-agent
mkdir -p "$BIN_DIR" "$CFG_DIR" "$DATA_DIR"

echo "下载 agent 二进制（$ARCH）…"
if command -v curl >/dev/null 2>&1; then
  curl -fsSL "$PANEL_URL/dl/agent/$ARCH" -o "$BIN_DIR/vps-agent.tmp"
elif command -v wget >/dev/null 2>&1; then
  wget -qO "$BIN_DIR/vps-agent.tmp" "$PANEL_URL/dl/agent/$ARCH"
else
  echo "需要 curl 或 wget 才能下载" >&2
  exit 1
fi
chmod +x "$BIN_DIR/vps-agent.tmp"
mv -f "$BIN_DIR/vps-agent.tmp" "$BIN_DIR/vps-agent"

cat > "$CFG_DIR/config.env" <<EOF
PANEL_URL=$PANEL_URL
AGENT_TOKEN=$AGENT_TOKEN
DATA_DIR=$DATA_DIR
EOF
chmod 600 "$CFG_DIR/config.env"

# 检测 init 系统：systemd / openrc（Alpine 等） / none。
# VPS_FORCE_INIT 可强制指定（测试用）。
detect_init() {
  case "${VPS_FORCE_INIT:-}" in
    systemd|openrc|none) echo "$VPS_FORCE_INIT"; return ;;
  esac
  if [ -d /run/systemd/system ] || [ "$(ps -p 1 -o comm= 2>/dev/null)" = "systemd" ]; then
    echo systemd
  elif command -v rc-service >/dev/null 2>&1 || [ -x /sbin/openrc-run ] || { [ "$(ps -p 1 -o comm= 2>/dev/null)" = "init" ] && [ -d /etc/init.d ]; }; then
    echo openrc
  else
    echo none
  fi
}
INIT_KIND="$(detect_init)"
echo "检测到 init 系统：$INIT_KIND"

case "$INIT_KIND" in
  systemd)
    mkdir -p /etc/systemd/system
    cat > /etc/systemd/system/vps-agent.service <<'EOF'
[Unit]
Description=VPS Panel Agent
After=network-online.target
Wants=network-online.target

[Service]
EnvironmentFile=/etc/vps-agent/config.env
ExecStart=/opt/vps-agent/vps-agent -panel ${PANEL_URL} -token ${AGENT_TOKEN} -data ${DATA_DIR}
Restart=always
RestartSec=3
User=root

[Install]
WantedBy=multi-user.target
EOF
    systemctl daemon-reload
    systemctl enable --now vps-agent
    systemctl restart vps-agent
    echo "vps-agent 安装完成，服务状态：$(systemctl is-active vps-agent)。节点稍后会自动在面板上线。"
    ;;
  openrc)
    # init 脚本运行时 source config.env 拼命令参数；脚本本身不含令牌明文，
    # 但仍设 700 权限，避免展开后的参数被旁路读取。
    cat > /etc/init.d/vps-agent <<'EOF'
#!/sbin/openrc-run
. /etc/vps-agent/config.env
name="vps-agent"
description="VPS Panel Agent"
command="/opt/vps-agent/vps-agent"
command_args="-panel $PANEL_URL -token $AGENT_TOKEN -data $DATA_DIR"
command_background=true
pidfile="/run/vps-agent.pid"
output_log="/var/log/vps-agent.log"
error_log="/var/log/vps-agent.log"

depend() {
	need net
}
EOF
    chmod 700 /etc/init.d/vps-agent
    rc-update add vps-agent default 2>/dev/null || true
    rc-service vps-agent stop >/dev/null 2>&1 || true
    rc-service vps-agent start
    echo "vps-agent 安装完成（OpenRC）。节点稍后会自动在面板上线。"
    ;;
  none)
    pkill -f "$BIN_DIR/vps-agent" 2>/dev/null || true
    nohup "$BIN_DIR/vps-agent" -panel "$PANEL_URL" -token "$AGENT_TOKEN" -data "$DATA_DIR" </dev/null >/var/log/vps-agent.log 2>&1 &
    echo "vps-agent 安装完成。无 systemd/OpenRC，已用 nohup 启动，重启不自动恢复。节点稍后会自动在面板上线。"
    ;;
esac
