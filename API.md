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

查任务结果（部署成功后 result 中含连接参数，节点详情里含完整分享链接）：

```bash
curl -H "Authorization: Bearer $TOKEN" https://panel.example.com/api/v1/tasks/task-xxxx
```
