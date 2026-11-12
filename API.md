# 对外 API（/api/v1）

给 Hermes 等外部系统管理舰队用。所有接口与面板自身 UI 共用实现，区别只是鉴权方式。

## 鉴权

在面板「设置 → API Token」中创建 Token，请求时携带：

```
Authorization: Bearer vp_xxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxx
```

## 接口

| 方法 | 路径 | 说明 |
|---|---|---|
| GET | `/api/v1/nodes` | 节点列表（含在线状态、最新状态快照、已部署协议与分享链接） |
| GET | `/api/v1/nodes/{id}` | 单个节点详情 |
| POST | `/api/v1/nodes/{id}/deploy` | 下发协议部署任务 |
| DELETE | `/api/v1/nodes/{id}/deployments/{depId}` | 卸载一个协议部署 |
| POST | `/api/v1/nodes/{id}/probe` | 立即执行一次三网延迟探测 |
| GET | `/api/v1/tasks/{taskId}` | 查询任务状态与日志 |

任务类接口返回 `{"task": {...}}`，任务异步执行，用任务查询接口轮询 `status`
（queued → sent → running → done/failed）与 `logs`。

## 示例

列出节点：

```bash
curl -H "Authorization: Bearer $TOKEN" https://panel.example.com/api/v1/nodes
```

给节点部署 VLESS+Reality（端口 0 或省略 = 随机空闲端口）：

```bash
curl -X POST -H "Authorization: Bearer $TOKEN" \
  -d '{"module":"vless-reality","port":0}' \
  https://panel.example.com/api/v1/nodes/node-xxxx/deploy
```

部署 Shadowsocks-2022：

```bash
curl -X POST -H "Authorization: Bearer $TOKEN" \
  -d '{"module":"ss2022","port":8388}' \
  https://panel.example.com/api/v1/nodes/node-xxxx/deploy
```

### 部署对外端点覆盖（NAT 场景）

NAT 机器的外部映射端点与 agent 监听端点不一致时，部署可带
`public_host` / `public_port`（可选）：agent 仍监听 `port`，但面板拼
分享链接（及二维码）时改用对外端点。部署后也可单独修改：

```bash
# 部署时指定对外端点
curl -X POST -H "Authorization: Bearer $TOKEN" \
  -d '{"module":"ss2022","port":8388,"public_host":"203.0.113.9","public_port":18443}' \
  https://panel.example.com/api/v1/nodes/node-xxxx/deploy

# 修改已有部署的对外端点（Web 接口；空字符串 + 0 表示清除覆盖）
curl -X PUT -H "Authorization: Bearer $TOKEN" \
  -d '{"public_host":"203.0.113.9","public_port":18443}' \
  https://panel.example.com/api/nodes/node-xxxx/deployments/dep-xxxx/endpoint
```

`PUT .../endpoint` 返回 `{"deployment": {...}}`，含用新端点重拼的 `link`。

## 手动安装接入（无 SSH 通道的机器）

没有 SSH 映射的机器（如 NAT 小鸡）可走手动安装：先在面板创建节点拿到
安装命令，再在 VPS 上以 root 执行，agent 自动上线。

```bash
# 1) 创建手动节点（面板 Web 接口，需登录会话或 Token）
curl -X POST -H "Authorization: Bearer $TOKEN" \
  -d '{"name":"nat-01","ip":"203.0.113.9","timezone":"Asia/Shanghai"}' \
  https://panel.example.com/api/nodes/manual
# → {"node": {...}, "install_command": "curl -fsSL https://panel.example.com/install-agent.sh | bash -s -- --panel https://panel.example.com --token <节点令牌>"}

# 2) 随时重新获取安装命令
curl -H "Authorization: Bearer $TOKEN" https://panel.example.com/api/nodes/node-xxxx/install-command
```

`install_command` 含节点令牌，只在上述已鉴权接口返回，不要外传、不要写日志。

两个公开（免登录）分发路径，供安装命令在目标机上拉取：

| 方法 | 路径 | 说明 |
|---|---|---|
| GET | `/install-agent.sh` | agent 安装脚本（bash，不含任何令牌；参数 `--panel` / `--token`，或环境变量 `PANEL_URL` / `AGENT_TOKEN`） |
| GET | `/dl/agent/{arch}` | 内嵌 agent 二进制，arch 只接受 `amd64` / `arm64`，其余 404 |

查任务结果（部署成功后 result 中含连接参数，节点详情里含完整分享链接）：

```bash
curl -H "Authorization: Bearer $TOKEN" https://panel.example.com/api/v1/tasks/task-xxxx
```

## 初始化模板（可勾选步骤）

手动重跑初始化（Web 接口 `POST /api/nodes/{id}/init`）可带 `steps`
数组只执行选中的步骤；不传或传空 = 全部步骤（接入后的自动初始化
始终是全量）。未选中的步骤会在任务日志里逐项记为「未选择，跳过」。

| step id | 含义 |
|---|---|
| `update` | 系统更新 |
| `tools` | 常用工具 |
| `timezone` | 时区 |
| `timesync` | 时间同步 |
| `ufw` | ufw 防火墙 |
| `fail2ban` | fail2ban |
| `ssh_port` | 修改 SSH 端口（需同时给 `new_ssh_port`） |
| `bbr` | BBR |
| `swap` | 小内存自动 swap |
| `agent_autostart` | agent 开机自启 |

```bash
# 只跑 BBR 与时区两项
curl -X POST -H "Authorization: Bearer $TOKEN" \
  -d '{"timezone":"Asia/Tokyo","steps":["bbr","timezone"]}' \
  https://panel.example.com/api/nodes/node-xxxx/init
```
