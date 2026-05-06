# CoreRank 验证指南

本文档记录 CoreRank 当前阶段推荐的验证方式。它的目标不是制造很大的测试声势，而是保证项目每个可写进简历的能力都有真实证据。

## 1. 测试环境

当前最小依赖：

- Go 1.25.x
- Redis 7.x
- Python 3，用于 RESTful 演示脚本

可选依赖：

- Docker Compose
- Prometheus
- Grafana

当前项目已接入可选 MySQL 持久化。未设置 DSN 时，MySQL 集成测试会跳过；设置 `CORERANK_TEST_MYSQL_DSN` 后会验证真实读写。

## 2. 本地基础验证

在项目根目录执行：

```powershell
cd F:\AI编程\简历\CoreRank
$env:GOCACHE = Join-Path (Get-Location) ".gocache"
go test ./...
go vet ./...
```

验证含义：

- `go test ./...`：确认所有包可编译，并执行已有测试。
- `go vet ./...`：做 Go 官方静态检查。

当前 Redis 测试位于：

```text
internal/repository/player_repo_test.go
```

测试覆盖：

- `SearchAndPickPlayers` 查询并删除候选玩家。
- 被匹配玩家不会再次被取出。
- Redis ZSet 排行榜顺序。
- 个人名次查询。

如果 Redis 不可用，测试会跳过 Redis 集成测试。正式验收时不应依赖跳过结果。

MySQL 集成测试：

```powershell
$env:CORERANK_TEST_MYSQL_DSN="corerank:<password>@tcp(127.0.0.1:3306)/corerank_test?parseTime=true&charset=utf8mb4&loc=Local"
go test ./...
```

测试覆盖：

- 初始化 MySQL 表结构。
- 玩家分数落库和查询。
- 匹配票据落库和查询。
- 匹配结果落库和查询。
- 榜单快照写入。
- Service 层匹配生命周期写入 MySQL。

## 3. 启动依赖

只启动 Redis：

```powershell
docker compose up -d corerank-redis
```

启动完整本地观测环境：

```powershell
docker compose up -d
```

端口：

| 服务 | 地址 |
|---|---|
| Redis | `127.0.0.1:6379` |
| Prometheus | `http://localhost:9090` |
| Grafana | `http://localhost:3000` |
| CoreRank metrics | `http://localhost:9091/metrics` |

## 4. RESTful 演示

执行：

```powershell
python scripts\rest_demo.py
```

脚本会自动构建并启动一个临时服务端，端口为：

| 服务 | 地址 |
|---|---|
| gRPC | `127.0.0.1:18080` |
| RESTful | `127.0.0.1:18081` |
| Metrics | `127.0.0.1:19091` |

验证链路：

- `POST /api/rank/score`
- `GET /api/rank/top`
- `GET /api/rank/player/{player_id}`
- `POST /api/match/pool`
- `POST /api/match/tickets`
- `GET /api/match/tickets/{ticket_id}`
- `GET /api/match/results/{match_id}`

通过标准：

- 能写入 3 个玩家分数。
- TopN 顺序正确。
- 个人名次正确。
- 玩家可加入匹配池。
- 两个分数接近的玩家创建票据后可生成 `match_id` 和 `room_id`。
- 可通过 `match_id` 查询匹配结果。

## 5. gRPC Robot 验证

先启动服务端：

```powershell
go run ./cmd/server
```

另开终端：

```powershell
go run ./cmd/robot
```

Robot 默认参数：

```text
100 goroutines
100 requests per goroutine
10000 total requests
```

记录结果时必须包含：

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
- 不要把本机 TPS 写成生产承诺。
- Windows 环境若出现 `Path/PATH` 重复导致启动器异常，应分开终端手动启动服务端和 Robot。

## 6. gRPC 匹配生命周期验证

当前 gRPC 匹配生命周期由测试覆盖：

```powershell
go test ./internal/handler
```

覆盖链路：

- `CreateMatchTicket`
- `GetMatchTicket`
- `GetMatchResult`

测试方式：

- 使用内存 `bufconn` 启动 gRPC Server，不占用真实端口。
- 复用 Redis 测试环境。
- 创建两个分数接近的玩家票据。
- 验证两个票据进入同一个 `match_id`。
- 验证可通过 gRPC 查询匹配结果。

## 7. Prometheus 验证

服务端启动后访问：

```text
http://localhost:9091/metrics
```

当前可检查：

- gRPC 请求计数。
- gRPC 延迟直方图。
- 已注册的匹配相关指标。

后续阶段应补：

- 匹配成功数。
- 匹配取消数。
- 匹配超时数。
- 匹配池人数。
- 匹配耗时。

## 8. CI 验证

当前 CI 基线位于：

```text
.github/workflows/ci.yml
```

CI 目标：

- 启动 Redis 服务。
- 启动 MySQL 服务。
- 执行 `go test ./...`。
- 执行 `go vet ./...`。
- 构建 `cmd/server`。
- 构建 `cmd/robot`。

CI 会使用临时 MySQL 容器验证集成测试，但不代表生产环境可用。

## 9. 后续阶段测试要求

### 匹配生命周期阶段

RESTful 和 gRPC 最小闭环已覆盖：

- 创建匹配票据。
- 重复入队拒绝。
- 取消匹配票据。
- 匹配成功生成 `match_id` 和 `room_id`。
- 查询匹配结果。

仍需补：

- 超时测试。
- 真实房间服或战斗服分配。

### MySQL 阶段

已覆盖：

- 初始化 SQL 或迁移脚本验证。
- 玩家表读写测试。
- 匹配票据落库测试。
- 匹配结果落库测试。
- 榜单快照写入。
- Service 层匹配生命周期落库。

仍需补：

- 更完整的索引解释文档。
- 事务失败回滚测试。
- MySQL 故障时的降级策略测试。

### 压测阶段

必须补：

- 压测环境说明。
- 压测命令。
- 请求量。
- 成功率。
- 平均延迟。
- P95/P99，只有真实采集后才能写。
- Redis/MySQL 是否本机或远程。

## 10. 面试演示建议

面试时推荐演示顺序：

1. 打开 README，说明项目定位和未实现边界。
2. 跑 `go test ./...`。
3. 跑 `python scripts\rest_demo.py`。
4. 展示 `internal/repository/lua_scripts.go`。
5. 展示 Robot 压测记录。
6. 展示优化方案和后续 MySQL/匹配生命周期设计。

不要现场临时搭复杂环境。演示重点是稳定、可解释、边界清楚。
