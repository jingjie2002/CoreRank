# CoreRank 验证指南

本文档记录 CoreRank 当前阶段推荐的验证方式。验证目标是确认代码、服务启动、分布式链路、roomserver TCP 流程和观测栈都能按当前项目设计工作。

## 1. 测试环境

当前最小依赖：

- Go 1.25.x
- Redis 7.x
- Python 3

推荐完整本地验证依赖：

- Docker Desktop / Docker Compose
- Prometheus
- Grafana
- 可选 MySQL

MySQL 是可选持久化层。未设置 DSN 时，MySQL 集成测试会跳过；设置 `CORERANK_TEST_MYSQL_DSN` 后会验证真实读写。运行服务端时，MySQL 连接失败不会阻断 Redis 主链路，除非设置 `CORERANK_MYSQL_REQUIRED=true`。

重启后或长时间运行前，先执行稳定性前置检查：

```powershell
powershell -NoProfile -ExecutionPolicy Bypass -File scripts\stability_preflight.ps1 -CheckEndpoints
```

该脚本不会启动服务，只输出内存、磁盘、端口、Docker 相关进程和可选端点状态。若可用内存低、磁盘空间不足或端口被占用，应先处理这些问题，再启动 Compose。

## 2. Go 测试与静态检查

在项目根目录执行：

```powershell
$env:GOCACHE = Join-Path (Get-Location) ".gocache"
go test ./...
go vet ./...
```

验证含义：

- `go test ./...`：确认所有包可编译，并执行已有测试。
- `go vet ./...`：执行 Go 官方静态检查。

Redis 集成测试覆盖：

- `SearchAndPickPlayers` 查询并删除候选玩家。
- 被匹配玩家不会再次被取出。
- Redis ZSet 排行榜顺序。
- 玩家排名查询。
- 匹配票据生命周期。
- roomserver registry、心跳、容量预留和释放。
- roomserver 不可用、heartbeat 过期、容量不足时的分配失败和重排队。

MySQL 集成测试：

```powershell
$env:CORERANK_TEST_MYSQL_DSN="corerank:<password>@tcp(127.0.0.1:3306)/corerank_test?parseTime=true&charset=utf8mb4&loc=Local"
go test ./...
```

MySQL 测试覆盖：

- 初始化表结构。
- 玩家分数落库和查询。
- 匹配票据落库和查询。
- 匹配票据超时状态同步。
- 匹配结果落库和查询。
- 榜单快照写入。
- Service 层在 MySQL 写入失败时继续返回 Redis 主链路结果。

## 3. 分布式 Compose 验证

执行 Compose build/up 前建议先运行 `scripts\stability_preflight.ps1`。如果脚本显示常用端口已经被其他进程占用，先确认占用来源，避免多个服务栈叠在一起运行。

构建镜像：

```powershell
docker compose build corerank-rank-service corerank-match-service corerank-gateway corerank-roomserver
```

启动分布式栈：

```powershell
docker compose up -d corerank-redis corerank-rank-service corerank-match-service corerank-gateway corerank-roomserver prometheus grafana
```

检查状态：

```powershell
docker compose ps
```

期望：

- `corerank-redis` 为 `healthy`。
- `corerank-rank-service` 为 `Up`。
- `corerank-match-service` 为 `Up`。
- `corerank-gateway` 为 `Up`。
- `corerank-roomserver` 为 `Up`。
- `prometheus` 和 `grafana` 为 `Up`。

## 4. 分布式 Smoke 脚本

执行：

```powershell
powershell -ExecutionPolicy Bypass -File scripts\distributed_smoke.ps1
```

验证链路：

- `GET /healthz`
- gateway、rank-service、match-service metrics
- `GET /api/servers?match_mode=duel`
- `POST /api/match/tickets`
- `DELETE /api/match/tickets/{ticket_id}`
- `GET /api/match/tickets/{ticket_id}`
- `GET /api/match/results/{match_id}`
- TCP roomserver `join`
- TCP roomserver `ready`
- TCP roomserver `room_started`
- TCP roomserver `leave`
- `POST /api/matches/{match_id}/settle`
- `GET /api/rank/top`
- `GET /api/rank/player/{player_id}`
- Prometheus targets
- Grafana dashboard search

通过标准：

- 能等待到 `compose-room-1` 注册为 active。
- 同一玩家重复创建 queued ticket 会返回 HTTP 409。
- queued ticket 可以取消，已取消 ticket 再次取消会返回 HTTP 409。
- 两个玩家创建 ticket 后可以匹配成功。
- 匹配结果包含 `RoomID`、`ServerID` 和 `ServerAddr`。
- TCP 客户端可以连接 `ServerAddr` 并完成房间流程。
- 结算后排行榜更新。
- TopN 和玩家排名查询正确。
- Prometheus 中 `corerank-gateway`、`corerank-rank-service`、`corerank-match-service` targets 均为 `up`。
- Grafana 能搜索到 `corerank-overview` dashboard。

## 5. 受控长稳验证

执行：

```powershell
powershell -NoProfile -ExecutionPolicy Bypass -File scripts\distributed_soak.ps1 -DurationMinutes 10 -IntervalSeconds 30
```

默认通过标准：

- 初始稳定性前置检查没有低内存或低磁盘警告。
- gateway `/healthz` 持续可达。
- Prometheus `/-/ready` 持续可达。
- Grafana `/api/health` 持续可达。
- Prometheus 中三个 CoreRank targets 持续存在且为 `up`。

如果希望同时跑完整业务链路，可以加：

```powershell
powershell -NoProfile -ExecutionPolicy Bypass -File scripts\distributed_soak.ps1 -DurationMinutes 10 -IntervalSeconds 30 -RunSmokeAtStart -RunSmokeAtEnd
```

如果输出 `blocked` 或 `aborted`，先处理 JSON 中的 `warnings`，不要继续启动更多服务或压测。

## 6. 单进程 REST 验证

执行：

```powershell
python scripts\rest_demo.py
```

脚本会自动构建并启动一个临时单进程服务端，端口为：

| 服务 | 地址 |
|---|---|
| gRPC | `127.0.0.1:18080` |
| REST | `127.0.0.1:18081` |
| Metrics | `127.0.0.1:19091` |

验证内容：

- 排行榜写入和查询。
- 多榜单 `leaderboard_type`。
- 早期匹配池接口。
- room server 注册。
- 匹配票据创建。
- 匹配结果查询。
- queued 票据超时。
- metrics 指标存在。

## 7. TCP RoomServer 验证

执行：

```powershell
python scripts\room_tcp_demo.py
```

通过标准：

- roomserver 能注册到 `/api/servers` 并发送 heartbeat。
- 两个玩家能通过 `POST /api/match/tickets` 匹配成功。
- 匹配结果包含 `RoomID`、`ServerID` 和 `ServerAddr`。
- TCP 客户端能连接 `ServerAddr`，完成 `join -> ready -> room_started -> leave`。

相关单元测试：

```powershell
go test ./internal/roomserver
```

## 8. gRPC Robot 验证

先启动单进程服务端：

```powershell
go run ./cmd/server
```

另开终端：

```powershell
go run ./cmd/robot
```

默认参数：

```text
100 workers
100 requests per worker
10000 total requests
```

可选环境变量：

```powershell
$env:ROBOT_GRPC_ADDR="localhost:8080"
$env:ROBOT_WORKERS="100"
$env:ROBOT_REQUESTS_PER_WORKER="100"
go run ./cmd/robot
```

记录结果时建议包含：

- 总请求数。
- 成功请求数。
- 失败请求数。
- 成功率。
- 总耗时。
- TPS。
- 平均延迟。
- 测试机器和 Redis 位置。

注意：

- 当前 Robot 只统计平均延迟，不统计 P95/P99。
- 本机 TPS 只能代表当前机器和当前参数。
- Windows 环境若出现 `Path/PATH` 重复导致启动器异常，可以分开终端手动启动服务端和 Robot。

## 9. gRPC 匹配生命周期测试

当前 gRPC 匹配生命周期由测试覆盖：

```powershell
go test ./internal/handler
```

覆盖链路：

- `RegisterGameServer`
- `CreateMatchTicket`
- `GetMatchTicket`
- `GetMatchResult`
- gateway 对重复 ticket、非 queued ticket 取消等 gRPC 错误的 HTTP 状态码映射。

测试方式：

- 使用内存 `bufconn` 启动 gRPC Server。
- 复用 Redis 测试环境。
- 注册测试 roomserver。
- 创建两个分数接近的玩家票据。
- 验证两个票据进入同一个 `match_id`。
- 验证匹配结果包含 roomserver 分配信息。

## 10. Prometheus 和 Grafana 验证

分布式栈启动后访问：

```text
http://127.0.0.1:9090/targets
```

当前应存在：

- `corerank-gateway`
- `corerank-rank-service`
- `corerank-match-service`

Grafana dashboard：

```text
http://127.0.0.1:3000
Dashboards -> CoreRank -> CoreRank Overview
```

关键指标名：

```text
corerank_grpc_requests_total
corerank_grpc_request_latency_seconds
corerank_matcher_match_total
corerank_matcher_ticket_events_total
corerank_matcher_lifecycle_duration_seconds
corerank_matcher_queued_tickets
corerank_room_assignment_total
corerank_room_assignment_failures_total
corerank_room_server_load
```

更多 PromQL 查询见 `docs/observability.md`。

## 11. CI 验证

CI 基线位于：

```text
.github/workflows/ci.yml
```

CI 目标：

- 启动 Redis 服务。
- 启动 MySQL 服务。
- 执行 `go test ./...`。
- 执行 `go vet ./...`。
- 构建 `cmd/server`。
- 构建 `cmd/roomserver`。
- 构建 `cmd/robot`。

CI 使用临时 MySQL 容器验证集成测试，但不代表生产环境配置。

## 12. 后续待补验证

- Linux 容器环境下的长时间持续运行记录。
