# CoreRank API 文档

本文档记录 CoreRank 当前已经实现的 RESTful、gRPC、TCP roomserver 和 metrics 接口。它只描述当前代码中真实存在的接口；当前已包含本地可验证的 Redis-backed 房间服资源分配 v1 和最小 TCP 房间服 v1，但不包含完整 WebSocket 房间服、真实战斗服进程、账号鉴权或匹配结果主动通知。

## 端口

默认端口：

| 服务 | 默认地址 | 说明 |
|---|---|---|
| gRPC | `:8080` | 排行榜和匹配生命周期接口 |
| RESTful | `:8081` | 调试、联调和演示接口 |
| Metrics | `:9091` | Prometheus `/metrics` |

可通过环境变量调整：

```powershell
$env:GRPC_ADDR="127.0.0.1:18080"
$env:HTTP_ADDR="127.0.0.1:18081"
$env:METRICS_ADDR="127.0.0.1:19091"
go run ./cmd/server
```

## RESTful API

RESTful 接口主要用于本地调试、脚本演示和面试展示。请求和响应均使用 JSON。

### 健康检查

```http
GET /health
```

响应示例：

```json
{
  "status": "ok"
}
```

### 注册房间服/战斗服资源

```http
POST /api/servers
```

请求体：

```json
{
  "server_id": "demo-room-1",
  "server_type": "room",
  "addr": "127.0.0.1:7001",
  "region": "local",
  "match_mode": "duel",
  "capacity": 8,
  "current_load": 0,
  "status": "active"
}
```

说明：

- `server_type` 支持 `room` / `battle`，为空时默认 `room`。
- `status` 支持 `active` / `draining` / `unhealthy`，只有 `active` 会被分配。
- `capacity` 表示可预留的玩家槽位数，本地演示时可以把它理解成这台 server 能承载多少个匹配玩家。

### 查询房间服/战斗服资源

```http
GET /api/servers?match_mode=duel
```

### 房间服/战斗服心跳

```http
POST /api/servers/{server_id}/heartbeat
```

请求体可为空，也可以更新状态或当前负载：

```json
{
  "status": "active",
  "current_load": 2
}
```

### 更新排行榜分数

```http
POST /api/rank/score
```

请求体：

```json
{
  "player_id": "p1",
  "score": 1200,
  "leaderboard_type": "season:ss25"
}
```

响应示例：

```json
{
  "PlayerID": "p1",
  "Score": 1200,
  "Rank": 1
}
```

说明：

- `player_id` 必填。
- `score` 是排行榜分数。
- `leaderboard_type` 可选，默认是 `global`；可使用 `season:ss25`、`event:spring` 这类值区分赛季榜、活动榜和小游戏榜。
- 也可以通过查询参数覆盖：`POST /api/rank/score?leaderboard_type=season:ss25`。
- 当前 RESTful 返回的是 Go 结构体默认 JSON 字段名，因此字段为 `PlayerID`、`Score`、`Rank`。

### 查询 TopN 排行榜

```http
GET /api/rank/top?n=10
```

赛季榜或活动榜示例：

```http
GET /api/rank/top?n=10&leaderboard_type=season:ss25
```

响应示例：

```json
[
  {
    "PlayerID": "p1",
    "Score": 1200,
    "Rank": 1
  }
]
```

说明：

- `n` 小于等于 0 时，服务端默认查询前 10 名。
- `leaderboard_type` 可选，默认查询全局榜。

### 查询单个玩家排名

```http
GET /api/rank/player/{player_id}
```

示例：

```http
GET /api/rank/player/p1
```

查询玩家在赛季榜中的名次：

```http
GET /api/rank/player/p1?leaderboard_type=season:ss25
```

响应示例：

```json
{
  "PlayerID": "p1",
  "Score": 1200,
  "Rank": 1
}
```

### 加入传统匹配池

```http
POST /api/match/pool
```

请求体：

```json
{
  "player_id": "p1",
  "mmr_score": 1500
}
```

响应示例：

```json
{
  "player_id": "p1",
  "mmr_score": 1500,
  "queued": true
}
```

说明：

- 这是早期匹配池调试接口。
- 当前更完整的匹配生命周期建议使用 `POST /api/match/tickets`。

### 移出传统匹配池

```http
DELETE /api/match/pool/{player_id}
```

响应示例：

```json
{
  "player_id": "p1",
  "queued": false
}
```

### 创建匹配票据

```http
POST /api/match/tickets
```

请求体：

```json
{
  "player_id": "p1",
  "mmr_score": 1500,
  "match_mode": "default",
  "max_wait_ms": 30000
}
```

响应状态码：

- `201 Created`：创建成功。
- `409 Conflict`：玩家已有 queued 票据。
- `400 Bad Request`：请求参数错误或其他匹配错误。

响应示例：

```json
{
  "TicketID": "ticket_xxx",
  "PlayerID": "p1",
  "MMRScore": 1500,
  "MatchMode": "default",
  "Status": "queued",
  "MatchID": "",
  "RoomID": "",
  "CreatedAt": 1777990000000,
  "UpdatedAt": 1777990000000,
  "ExpiresAt": 1777990030000
}
```

说明：

- `match_mode` 为空时默认为 `default`。
- `max_wait_ms` 小于等于 0 时使用服务端默认等待时间。
- 当两个分数接近的 queued 票据满足匹配条件且存在可用 server 时，服务会生成 `match_id`、逻辑 `room_id` 和 server 分配信息。

### 查询匹配票据

```http
GET /api/match/tickets/{ticket_id}
```

响应示例：

```json
{
  "TicketID": "ticket_xxx",
  "PlayerID": "p1",
  "MMRScore": 1500,
  "MatchMode": "default",
  "Status": "matched",
  "MatchID": "match_xxx",
  "RoomID": "room_xxx",
  "CreatedAt": 1777990000000,
  "UpdatedAt": 1777990001000,
  "ExpiresAt": 1777990030000
}
```

状态说明：

| 状态 | 含义 |
|---|---|
| `queued` | 等待匹配 |
| `matched` | 已匹配成功 |
| `cancelled` | 已取消 |
| `timeout` | 已超时 |

### 取消匹配票据

```http
DELETE /api/match/tickets/{ticket_id}
```

响应示例：

```json
{
  "TicketID": "ticket_xxx",
  "PlayerID": "p1",
  "Status": "cancelled"
}
```

说明：

- 只有 `queued` 状态票据可以取消。
- 已匹配、已取消或已超时票据会返回冲突错误。

### 查询匹配结果

```http
GET /api/match/results/{match_id}
```

响应示例：

```json
{
  "MatchID": "match_xxx",
  "RoomID": "room_xxx",
  "ServerID": "demo-room-1",
  "ServerAddr": "127.0.0.1:7001",
  "MatchMode": "default",
  "PlayerIDs": [
    "p1",
    "p2"
  ],
  "Status": "matched",
  "CreatedAt": 1777990001000
}
```

说明：

- `RoomID` 是本次匹配生成的逻辑房间 ID。
- `ServerID` / `ServerAddr` 是 Redis-backed server registry 选出的房间服/战斗服资源。
- 当前只支持查询结果，不支持主动推送通知。

### 结算匹配并更新排行榜

```http
POST /api/matches/{match_id}/settle
```

请求体：

```json
{
  "leaderboard_type": "season:ss25",
  "scores": [
    {"player_id": "p1", "score": 1260},
    {"player_id": "p2", "score": 1210}
  ]
}
```

响应示例：

```json
{
  "leaderboard_type": "season:ss25",
  "match_id": "match_xxx",
  "updated_players": [
    {"PlayerID": "p1", "Score": 1260, "Rank": 1}
  ]
}
```

说明：

- `match_id` 必须能在 CoreRank 中查到匹配结果。
- `scores` 使用结算后的绝对排行榜分数。
- `leaderboard_type` 可选，默认写入 `global`；可以用于赛季榜、活动榜或小游戏榜。
- 该接口是最小战斗结算入口，只更新排行榜，不实现伤害、技能、掉落或完整战斗服逻辑。

## gRPC API

gRPC 协议定义位于：

```text
api/proto/rank.proto
```

### RankService

#### UpdateScore

```proto
rpc UpdateScore(UpdateScoreRequest) returns (UpdateScoreResponse);
```

请求字段：

| 字段 | 类型 | 说明 |
|---|---|---|
| `player_id` | `string` | 玩家 ID |
| `new_score` | `int64` | 新排行榜分数 |
| `change_type` | `string` | 当前实现主要按绝对分数写入 |

响应字段：

| 字段 | 类型 | 说明 |
|---|---|---|
| `success` | `bool` | 是否成功 |
| `player` | `Player` | 玩家信息 |
| `current_rank` | `int64` | 当前排名，1-based |

#### GetTopRank

```proto
rpc GetTopRank(GetTopRankRequest) returns (GetTopRankResponse);
```

请求字段：

| 字段 | 类型 | 说明 |
|---|---|---|
| `leaderboard_type` | `string` | 排行榜类型，当前主要使用全局榜 |
| `top_n` | `int32` | 查询前 N 名 |
| `offset` | `int64` | 预留分页字段 |

响应字段：

| 字段 | 类型 | 说明 |
|---|---|---|
| `entries` | `repeated RankEntry` | 排行榜条目 |
| `total_players` | `int64` | 当前响应内总数 |
| `updated_at` | `int64` | 响应时间戳 |

### MatchService

#### CreateMatchTicket

```proto
rpc CreateMatchTicket(CreateMatchTicketRequest) returns (CreateMatchTicketResponse);
```

请求字段：

| 字段 | 类型 | 说明 |
|---|---|---|
| `player_id` | `string` | 玩家 ID |
| `mmr_score` | `int64` | 匹配分 |
| `match_mode` | `string` | 匹配模式 |
| `max_wait_ms` | `int64` | 最大等待时间，毫秒 |

#### GetMatchTicket

```proto
rpc GetMatchTicket(GetMatchTicketRequest) returns (GetMatchTicketResponse);
```

请求字段：

| 字段 | 类型 | 说明 |
|---|---|---|
| `ticket_id` | `string` | 票据 ID |

#### CancelMatchTicket

```proto
rpc CancelMatchTicket(CancelMatchTicketRequest) returns (CancelMatchTicketResponse);
```

请求字段：

| 字段 | 类型 | 说明 |
|---|---|---|
| `ticket_id` | `string` | 票据 ID |

#### GetMatchResult

```proto
rpc GetMatchResult(GetMatchResultRequest) returns (GetMatchResultResponse);
```

请求字段：

| 字段 | 类型 | 说明 |
|---|---|---|
| `match_id` | `string` | 匹配结果 ID |

## TCP Roomserver API

TCP 房间服入口位于：

```powershell
go run ./cmd/roomserver
```

默认配置：

| 环境变量 | 默认值 | 说明 |
|---|---|---|
| `ROOM_SERVER_ID` | `demo-room-1` | 注册到 CoreRank 的 server id |
| `ROOM_SERVER_ADDR` | `127.0.0.1:7001` | TCP 监听地址，也是匹配结果中的 `ServerAddr` |
| `CORE_RANK_HTTP` | `http://127.0.0.1:8081` | CoreRank RESTful API 地址 |
| `MATCH_MODE` | `duel` | 当前 roomserver 承接的匹配模式 |
| `CAPACITY` | `8` | 可承接玩家槽位数 |
| `HEARTBEAT_INTERVAL` | `10s` | 心跳间隔 |

协议是 JSON line，每条请求和响应用换行分隔。

### join

```json
{"type":"join","room_id":"room_xxx","player_id":"p1"}
```

响应：

```json
{"type":"joined","room_id":"room_xxx","player_id":"p1","players":["p1"]}
```

### ready

```json
{"type":"ready","room_id":"room_xxx","player_id":"p1"}
```

响应：

```json
{"type":"ready","room_id":"room_xxx","player_id":"p1","ready_players":["p1"]}
```

当同一房间至少 2 个玩家都 ready 后，服务端会额外返回：

```json
{"type":"room_started","room_id":"room_xxx","players":["p1","p2"]}
```

### leave

```json
{"type":"leave","room_id":"room_xxx","player_id":"p1"}
```

响应：

```json
{"type":"left","room_id":"room_xxx","player_id":"p1"}
```

### ping

```json
{"type":"ping"}
```

响应：

```json
{"type":"pong"}
```

演示脚本：

```powershell
python scripts\room_tcp_demo.py
```

## Metrics API

```http
GET /metrics
```

关键指标：

| 指标名 | 说明 |
|---|---|
| `corerank_grpc_requests_total` | gRPC 请求计数 |
| `corerank_grpc_request_latency_seconds` | gRPC 请求耗时直方图 |
| `corerank_matcher_match_total` | 匹配成功计数 |
| `corerank_matcher_ticket_events_total` | 匹配票据事件计数 |
| `corerank_matcher_lifecycle_duration_seconds` | 票据从创建到终态的耗时 |
| `corerank_matcher_queued_tickets` | 当前 queued 票据数量 |
| `corerank_room_assignment_total` | 房间资源分配成功/失败计数 |
| `corerank_room_assignment_failures_total` | 房间资源分配失败原因计数 |
| `corerank_room_server_load` | 当前 server 预留玩家槽位数 |

## 错误响应

RESTful 错误响应格式：

```json
{
  "error": "error message"
}
```

常见状态码：

| 状态码 | 场景 |
|---|---|
| `400` | 请求参数错误 |
| `404` | 票据、结果或玩家不存在 |
| `409` | 重复入队、取消非 queued 票据 |
| `500` | Redis 或服务端内部错误 |

## 当前 API 边界

- 当前没有 JWT 或账号鉴权。
- 当前没有匹配结果主动通知。
- 当前有最小结算入口，可按 `match_id` 更新全局榜、赛季榜或活动榜分数；它不是完整战斗结算系统。
- 当前有本地可验证的 Redis-backed 房间资源分配 v1 和最小 TCP 房间服 v1，但没有 WebSocket 房间服或完整战斗服进程。
- 当前没有 Kubernetes 或生产级服务发现。
- 当前 `room_id` 是逻辑 ID，`server_id/server_addr` 代表被选中的 roomserver；TCP v1 只维护本进程内的临时房间状态。
- 当前 RESTful 接口主要用于调试和演示，不是完整对外开放 API 网关。
