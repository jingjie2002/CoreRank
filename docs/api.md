# CoreRank API

本文档对应当前分布式本地演示栈：HTTP gateway、RankService gRPC、MatchService gRPC 和 TCP roomserver。所有 HTTP JSON 字段统一使用 `snake_case`。

## 地址与通用约束

| 组件 | 默认地址 |
|---|---|
| Gateway HTTP | `http://127.0.0.1:8081` |
| RankService gRPC | `127.0.0.1:18081` |
| MatchService gRPC | `127.0.0.1:18082` |
| RoomServer TCP | `127.0.0.1:7001` |

- `GET /healthz` 是进程存活检查；`GET /readyz` 会检查两个 gRPC 后端及其 Redis health 状态。
- HTTP body 最大 64 KiB，gateway 最多同时处理 256 个请求，后端 RPC 默认 3 秒超时。
- 设置 `CORERANK_API_KEY` 后，所有 `/api/*` 请求必须携带 `X-CoreRank-API-Key` 或 `Authorization: Bearer <key>`。
- `player_id`、`ticket_id`、`match_id`、`server_id` 都有长度和字符集限制。
- 排名分数为整数，范围 `[-1000000000, 1000000000]`；MMR 范围 `[0, 10000]`。
- `match_mode` 会转成小写，最长 32 字符，只允许字母、数字、`_`、`-`、`:`。

## 排行榜

### 写入绝对分数

```http
POST /api/rank/score
Content-Type: application/json

{"player_id":"p1","score":1200,"leaderboard_type":"season:ss25"}
```

```json
{"player_id":"p1","score":1200,"rank":1}
```

当前只支持绝对分数写入；gRPC `change_type` 为空或 `ABSOLUTE` 时有效，其他值返回 `InvalidArgument`。

### 查询排行榜

```http
GET /api/rank/top?n=10&offset=0&leaderboard_type=season:ss25
```

`n` 默认 10、最大 100，`offset` 范围为 0–10000。gRPC 响应的 `total_players` 是该榜单真实总人数，不是本页条目数。

### 查询玩家排名

```http
GET /api/rank/player/p1?leaderboard_type=season:ss25
```

## 匹配票据

### 创建

```http
POST /api/match/tickets
Content-Type: application/json

{"player_id":"p1","mmr_score":1500,"match_mode":"duel","max_wait_ms":30000}
```

```json
{
  "ticket_id":"ticket_xxx",
  "player_id":"p1",
  "mmr_score":1500,
  "match_mode":"duel",
  "status":"queued",
  "match_id":"",
  "room_id":"",
  "created_at":1777990000000,
  "updated_at":1777990000000,
  "expires_at":1777990030000
}
```

`max_wait_ms` 为 0 时使用 30 秒默认值；显式值必须在 1 秒到 10 分钟之间。不同 `match_mode` 使用独立 Redis 队列和独立滑动窗口，不能互相匹配。

### 查询与取消

```http
GET /api/match/tickets/{ticket_id}
DELETE /api/match/tickets/{ticket_id}
```

状态机为 `queued -> matched | cancelled | timeout`。创建、取消、完成和超时迁移均由 Redis Lua 保护；只有 `queued` 可取消。

早期 `/api/match/pool` 接口只存在于 legacy 单进程入口，分布式 gateway 明确返回 501，不属于当前 v1 API。

### 查询匹配结果

```http
GET /api/match/results/{match_id}
```

```json
{
  "match_id":"match_xxx",
  "room_id":"room_xxx",
  "server_id":"compose-room-1",
  "server_addr":"127.0.0.1:7001",
  "match_mode":"duel",
  "player_ids":["p1","p2"],
  "status":"matched",
  "created_at":1777990001000,
  "join_token":"join_xxx"
}
```

`join_token` 是本地演示用房间分配令牌，不应写日志或提交到仓库。

## 原子结算

```http
POST /api/matches/{match_id}/settle
Content-Type: application/json

{
  "leaderboard_type":"season:ss25",
  "scores":[
    {"player_id":"p1","score":1260},
    {"player_id":"p2","score":1210}
  ]
}
```

```json
{
  "match_id":"match_xxx",
  "leaderboard_type":"season:ss25",
  "updated_players":[
    {"player_id":"p1","score":1260,"rank":1},
    {"player_id":"p2","score":1210,"rank":2}
  ],
  "idempotent":false
}
```

约束：

- `scores` 必须无重复地覆盖匹配结果中的全部玩家，不能包含 outsider，也不能缺少参赛者。
- 排名批量写入、结算指纹、匹配结算状态和房服预留释放在同一个 Redis Lua 脚本中提交。
- 同一 payload 重放返回成功且 `idempotent=true`；不同 payload 重放返回冲突，不改分。
- MySQL 启用时仍是 Redis 成功后的可选同步旁路写入，不是 Redis 的恢复源，也不属于该 Lua 原子边界。

## RoomServer 注册与负载

```http
POST /api/servers
GET /api/servers?match_mode=duel
POST /api/servers/{server_id}/heartbeat
```

返回对象中：

- `current_load`：分配器持有的预留槽位，只能由分配/释放流程改变。
- `observed_load`：roomserver 心跳实报的当前连接玩家数。

心跳不再覆盖预留容量。重复注册同一 server 会保留预留值，且不允许偷偷改变 `match_mode`。reservation 使用 2 小时 lease；异常遗留 assignment 到期后由 MatchWorker 幂等释放，正常结算或失败释放会提前移除 lease。

## TCP RoomServer

协议为每行一个 JSON 对象。join 必须携带匹配结果中的身份信息：

```json
{"type":"join","match_id":"match_xxx","room_id":"room_xxx","player_id":"p1","join_token":"join_xxx"}
```

roomserver 会通过 gateway 校验 match、room、server、player 和 token。成功后可发送：

```json
{"type":"ready","room_id":"room_xxx","player_id":"p1"}
{"type":"leave","room_id":"room_xxx","player_id":"p1"}
{"type":"ping"}
```

单条消息最大 64 KiB，连接空闲 30 秒会超时，默认最多 1024 个并发 TCP 连接；断连会清理该连接加入的临时玩家状态。

## gRPC

协议源文件是 `api/proto/rank.proto`。当前 RPC：

- RankService：`UpdateScore`、`SettleMatch`、`GetTopRank`、`GetPlayerRank`。
- MatchService：`CreateMatchTicket`、`GetMatchTicket`、`CancelMatchTicket`、`GetMatchResult`、`RegisterGameServer`、`HeartbeatGameServer`、`ListGameServers`。
- 两个服务都注册标准 gRPC health service，gateway 启动和 `/readyz` 使用它检查后端。

生成代码：

```powershell
scripts\generate_proto.ps1
```

Linux/macOS：

```sh
sh scripts/generate_proto.sh
```

## 错误映射

| HTTP | 典型含义 |
|---:|---|
| 400 | 字段、范围、JSON 或榜单分页参数错误 |
| 401 | 配置 API key 后缺失或错误 |
| 404 | ticket、match 或 server 不存在 |
| 409 | 重复排队、非法状态迁移、结算 payload 冲突 |
| 503 | 后端未就绪或并发保护触发 |
| 504 | 后端 RPC 超时 |
| 502 | 未分类的内部 gRPC 错误 |

本项目仍是本地分布式演示实现，不包含账号/JWT、外网 TLS/mTLS、完整战斗服、Redis Cluster、服务发现或 Kubernetes。
