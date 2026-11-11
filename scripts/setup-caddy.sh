#!/usr/bin/env bash
# Put Caddy in front of the panel for automatic HTTPS (optional but
# recommended before the panel is exposed: the panel itself serves
# plain HTTP on :8080, so without this the admin password and agent
# tokens travel unencrypted).
#
# Usage (run as root on the panel VPS, Debian/Ubuntu):
#   sudo ./scripts/setup-caddy.sh panel.example.com
#
# What it does:
#   1. installs Caddy from the official Cloudsmith repo
#   2. writes /etc/caddy/Caddyfile: <domain> -> reverse_proxy 127.0.0.1:8080
#   3. reloads Caddy (it fetches a Let's Encrypt cert automatically)
# After that, set the panel's public address in Settings to
# https://<domain> so agents connect over wss.
set -euo pipefail

[ "$(id -u)" = "0" ] || { echo "请用 root 运行"; exit 1; }
[ $# -eq 1 ] || { echo "用法: $0 <域名，例如 panel.example.com>"; exit 1; }
DOMAIN="$1"
case "$DOMAIN" in
  *.*) ;;
  *) echo "需要一个域名（不能是 IP，自动 HTTPS 需要域名）: $DOMAIN"; exit 1 ;;
esac

apt-get update -qq
apt-get install -y -qq debian-keyring debian-archive-keyring apt-transport-https curl
curl -1sLf 'https://dl.cloudsmith.io/public/caddy/stable/gpg.key' \
  | gpg --dearmor -o /usr/share/keyrings/caddy-stable-archive-keyring.gpg
curl -1sLf 'https://dl.cloudsmith.io/public/caddy/stable/debian.deb.txt' \
  | tee /etc/apt/sources.list.d/caddy-stable.list >/dev/null
apt-get update -qq
apt-get install -y -qq caddy

cat > /etc/caddy/Caddyfile <<EOF
$DOMAIN {
	reverse_proxy 127.0.0.1:8080
}
EOF

systemctl reload caddy
sleep 2
echo "Caddy 已启动，正在为 $DOMAIN 申请证书"
echo "验证: curl -sI https://$DOMAIN | head -3"
echo "下一步: 面板 设置 -> 面板对外地址 填 https://$DOMAIN"
