# v0.1.0 真机首测清单

v0.1.0 只在沙箱里做过本地全链路验证，还没上过真 VPS。下面几项
是沙箱测不出来的，真机第一台按顺序过一遍就算稳定了，之后再做
移动端页面。

## 1. 面板部署（面板 VPS）

- [ ] `git clone https://github.com/hoolu0/vps-panel.git`，`sudo ./scripts/install.sh`
      （面板机无 Go 时走 Release 下载；数据在 `/var/lib/vps-panel`）
- [ ] `journalctl -u vps-panel | grep 管理员密码` 取首次密码并登录
- [ ] HTTPS（可选但建议先做，面板本身是明文 HTTP）：
      `sudo ./scripts/setup-caddy.sh <你的域名>`，然后面板 设置 →
      面板对外地址填 `https://<你的域名>`，agent 随即走 wss

## 2. 接入第一台真 VPS

- [ ] 面板添加节点：IP + SSH 端口 + 密码/密钥（二选一），agent 上线
- [ ] 初始化模板跑完，重点看顺序：ufw 先放行新 SSH 端口 → 改 sshd
      端口 → 面板验证新端口可连 → 关旧端口（别把自己锁外面）
- [ ] 确认 fail2ban、ufw、BBR、时区（手动指定）都按预期生效

## 3. 沙箱测不出来的三项（真机重点）

- [ ] 三网延迟出具体数值：沙箱禁了原始套接字所以全是"超时"，
      真 VPS 上 root agent 应能测出电信/联通/移动 IPv4+IPv6 延迟
- [ ] VLESS+Reality 对公网 TLS 目标：沙箱只对本地目标跑通过，
      真机上确认 Reality 握手走公网目标正常
- [ ] 去程线路数据源：设置里自定义 URL 模板能出数据

## 4. 协议与更新

- [ ] VLESS、SS2022 各部署一次，用面板回收的分享链接真跑流量
- [ ] 改端口 / 重启 / 卸载各走一次
- [ ] `sudo ./scripts/update.sh` 跑一次，确认拿到的是新版二进制
      （已修过旧二进制残留导致静默不更新的 bug），数据不动
