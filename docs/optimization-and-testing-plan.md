# CoreRank 优化方案与测试策略定稿

## 1. 最终定位

`CoreRank` 定位为：

```text
Go 游戏匹配与排行榜中台
```

它不是完整游戏服务器，也不是游戏客户端。它是一个运行在服务端的中间服务，负责处理竞技游戏服务端中常见的匹配、排行榜、匹配结果和数据沉淀能力。

最终简历定位建议：

```text
CoreRank 游戏匹配与排行榜中台（Go）
基于 Go 实现面向竞技游戏服务端的匹配与排行榜服务，提供 gRPC / RESTful 双协议接入；使用 Redis ZSet 与 Lua 脚本承载匹配池、排行榜和候选玩家原子摘取，设计 MatchTicket / MatchResult 状态流转支持入队、取消、超时和匹配结果查询；使用 MySQL 持久化玩家、匹配票据和对局结果，并接入 Prometheus 指标、CI 和 Robot 压测脚本完成可复现验证。
```

注意：这段完整表述中的 MySQL、匹配生命周期、超时扫描、CI、本地 Prometheus/Grafana 观测栈和 Redis-backed 房间资源分配 v1 已有可验证基线；真实 TCP/WebSocket 房间服进程、Linux 服务器部署和生产高可用仍不能写成已完成。

## 2. 中台到底跑在哪里

一般来说，这类“中台”跑在服务端，不跑在玩家电脑或手机里。

实际公司里的典型部署关系是：

```text
玩家客户端
  -> 游戏网关 / 长连接服 / 房间服
  -> CoreRank 匹配与排行榜中台
  -> Redis / MySQL
  -> Prometheus / Grafana
```

CoreRank 对外通常有两类入口：

- `gRPC`：给游戏网关、房间服、战斗服、后台服务等内部服务调用。
- `RESTful`：给管理后台、联调脚本、测试工具、运营工具调用。

三种环境可以这样理解：

| 环境 | 跑在哪里 | 目的 |
|---|---|---|
| 本地开发环境 | 你的 Windows / WSL / 本机 Docker | 写代码、跑单测、跑 demo、验证接口 |
| 测试/演示环境 | 一台 Linux 云服务器或本机 Docker Compose | 模拟真实服务部署，给面试和演示用 |
| 生产环境 | 公司内网服务器、容器平台或 Kubernetes | 多实例部署、接入真实游戏网关和数据库 |

对简历项目来说，不需要真的做生产环境。最合理目标是：

- 本地能一键跑。
- Docker Compose 能拉起依赖。
- 可选在一台 Linux 云服务器上演示。
- GitHub 上有 README、CI、测试脚本和验证记录。

这已经足够支撑校招/实习项目可信度。

## 3. 最终优化路线

优化路线分 4 个阶段执行。

### 阶段 0：可信展示基线

目标：先让项目能安全公开展示，避免“简历写了但 GitHub 看不到”的风险。

要做：

- 整理当前 Git 工作树。
- 确认是否正式删除重复目录 `CoreRank/api/proto/`。
- 将 RESTful、测试、脚本、docs、README 调整形成正式提交。
- README 降风险，删除或降级过满表达：
  - 不写“完全消除竞态条件”。
  - 不写“Redis Cluster 已落地”。
  - 不写“ACID 特性”。
  - 不写生产级 P99 承诺。
  - 性能数据必须注明本机环境、命令和边界。
- 新增 GitHub Actions：
  - `go test ./...`
  - `go vet ./...`
- 补一份快速验证文档。

验收标准：

- `git status` 可解释。
- GitHub README 和本地能力一致。
- CI 通过。
- 用户能按 README 在本机跑通基础验证。

### 阶段 1：匹配生命周期闭环

目标：从“匹配池入队/取人”升级为“完整匹配服务”。

当前执行状态：

- RESTful 和 gRPC 最小闭环已完成：创建票据、查询票据、取消票据、查询匹配结果。
- Redis 已短期保存 `MatchTicket` 与 `MatchResult`。
- 超时扫描、房间分配抽象和 Redis-backed 房间资源分配 v1 已补；真实 TCP/WebSocket 房间服进程仍待补。

新增核心模型：

```text
MatchTicket
MatchResult
GameServer
RoomAssignment
```

建议状态：

```text
queued
matched
cancelled
timeout
expired
```

新增 RESTful 接口：

```text
POST   /api/match/tickets
GET    /api/match/tickets/{ticket_id}
DELETE /api/match/tickets/{ticket_id}
GET    /api/match/results/{match_id}
POST   /api/servers
GET    /api/servers
POST   /api/servers/{server_id}/heartbeat
```

新增 gRPC 接口：

```text
CreateMatchTicket
CancelMatchTicket
GetMatchResult
```

Redis 设计：

```text
match:pool:{mode}              匹配池 ZSet
match:ticket:{ticket_id}       匹配票据状态
match:player_ticket:{player_id} 防重复入队
match:ticket_expiry            超时扫描索引
match:result:{match_id}        短期匹配结果
server:info:{server_id}        server 元数据
server:heartbeat               server 心跳索引
server:load:{match_mode}       server 负载索引
room:assignment:{match_id}     房间分配结果
```

验收标准：

- 能演示创建票据、取消票据、匹配成功、查询匹配结果。
- 重复入队会被拒绝或幂等处理。
- 匹配成功后能生成 `match_id`、`room_id`，并在 REST 查询结果中看到 `ServerID` / `ServerAddr`。
- 无可用 server 时，已摘取玩家会回到 ticket pool，不会静默丢失。
- 有测试覆盖重复入队、取消、匹配成功、超时、server 负载选择、不可用 server 过滤和分配失败回滚。

### 阶段 2：MySQL 持久化证据链

目标：补齐 JD 中的 MySQL，并让 Redis 热数据和 MySQL 持久化分工清楚。

当前执行状态：

- 已接入可选 MySQL 持久化，设置 `CORERANK_MYSQL_DSN` 后启用。
- 基础故障降级已完成：MySQL 连接失败或写入失败时默认继续 Redis 主链路；`CORERANK_MYSQL_REQUIRED=true` 可切换为强依赖模式。
- 已有表结构：`players`、`match_tickets`、`match_results`、`rank_snapshots`。
- 已有 MySQL repository 集成测试。
- GitHub Actions 已加入 MySQL 服务。

建议表：

```text
players
match_tickets
match_results
rank_snapshots
```

职责划分：

- Redis：匹配池、排行榜热数据、短期 ticket/result 状态。
- MySQL：玩家资料、匹配票据、匹配结果、榜单快照。

不做项：

- 不做分库分表。
- 不做 Redis Cluster。
- 不做复杂事务消息。
- 不做过度微服务拆分。

验收标准：

- Docker Compose 能拉起 Redis + MySQL。当前已在本机验证 Redis、MySQL、Prometheus 和 Grafana 运行。
- 有初始化 SQL 或迁移脚本。已完成 `internal/repository/mysql_schema.sql`。
- 匹配票据、超时状态和匹配结果能写入 MySQL。已完成基础覆盖。
- 有 MySQL repository 测试或集成测试。已完成。
- MySQL 写入失败不影响 Redis 主链路返回。已完成基础覆盖。
- 文档写清关键索引设计。仍需补充更完整说明。

### 阶段 3：可观测性、压测与公开文档

目标：让项目变成可被面试官信任的公开项目。

当前执行状态：

- HTTP/metrics server 优雅关闭已补基础实现。
- 真实匹配业务指标已接入 `MatchService` 的创建、匹配、取消和超时路径。
- `docs/api.md` 已补。
- `docs/architecture.md` 已补。
- `docs/benchmark.md` 已补本机 Robot 压测记录。
- `docs/demo-guide.md` 已补本地测试与面试演示指南。
- `docs/interview-notes.md` 已整理。
- Docker Compose 已补 MySQL service、Grafana datasource provisioning 和基础 dashboard，并完成本机运行验证。
- 本机 Prometheus 已采集 `UpdateScore` P95/P99 短窗口结果；服务器部署验证仍未完成。

要做：

- HTTP/metrics server 优雅关闭。已补基础实现。
- 接入真实匹配指标。已补基础实现：
  - 匹配成功数。
  - 取消数。
  - 超时数。
  - queued 票据数量。
  - 匹配票据生命周期耗时。
- 补 `docs/api.md`。已补。
- 补 `docs/architecture.md`。已补。
- 补 `docs/benchmark.md`。已补本机 Robot 压测记录。
- 补 `docs/demo-guide.md`。已补本地测试与面试演示指南。
- 整理 `docs/interview-notes.md`。
- 补 `docs/observability.md`。已补本地观测栈说明。
- 本机验证 Grafana dashboard 和 Prometheus P95/P99 查询。已补，见 `docs/benchmark.md`。

验收标准：

- 陌生人能按文档在 10 分钟内跑起项目。
- 压测报告包含环境、命令、结果和限制。已补本机记录。
- README 明确区分已实现和未实现。
- Prometheus 能看到核心指标。已补 gRPC 和匹配业务指标。
- Grafana 本地 dashboard 能打开并读取 Prometheus datasource。已验证 provisioning 和 API。

## 4. 测试策略总览

CoreRank 应该分层测试，不是只靠跑一次 demo。

推荐测试金字塔：

```text
手工演示 / 面试演示
E2E 流程测试
REST/gRPC API 测试
Redis/MySQL 集成测试
Service 单元测试
静态检查 / 构建检查
```

每一层负责不同问题。

| 测试层级 | 测什么 | 目的 |
|---|---|---|
| 静态检查 | `go vet ./...` | 提前发现明显代码问题 |
| 编译检查 | `go test ./...` 或 `go build` | 确认所有包能编译 |
| 单元测试 | 状态机、分数范围、参数校验 | 不依赖外部服务，快速验证业务规则 |
| Redis 集成测试 | Lua、ZSet、重复入队、原子摘取 | 验证 Redis 行为真实有效 |
| MySQL 集成测试 | 表结构、索引、事务、唯一约束 | 验证持久化链路 |
| API 测试 | REST/gRPC 请求响应 | 验证对外契约 |
| E2E 测试 | 入队、匹配、查结果、落库 | 验证完整业务流程 |
| 压测 | Robot / benchmark | 验证吞吐、成功率、平均延迟 |
| 故障测试 | Redis/MySQL 不可用、端口冲突、重复请求 | 验证失败路径和边界 |

## 5. 当前项目现阶段怎么测

当前 CoreRank 已有 MySQL 可选持久化、RESTful/gRPC 匹配生命周期最小闭环和 Redis-backed 房间资源分配 v1，所以现阶段测试范围应该诚实限定在：

- Go 编译与静态检查。
- Redis ZSet / Lua 测试。
- Redis server registry / room assignment 测试。
- RESTful 基础接口。
- gRPC `UpdateScore` 压测。
- MySQL repository 集成测试。
- Prometheus 端点可访问性。

推荐当前本机测试命令：

```powershell
cd F:\AI编程\简历\CoreRank

$env:GOCACHE = Join-Path (Get-Location) '.gocache'
go test ./...
go vet ./...
python scripts\rest_demo.py
```

如果要跑 gRPC Robot：

```powershell
go run ./cmd/server
```

另开一个终端：

```powershell
go run ./cmd/robot
```

Robot 支持演示参数：

```powershell
$env:ROBOT_GRPC_ADDR="localhost:8080"
$env:ROBOT_WORKERS="100"
$env:ROBOT_REQUESTS_PER_WORKER="100"
go run ./cmd/robot
```

如果 Windows 的 `Path/PATH` 环境变量导致 `Start-Process` 类启动器异常，可以使用普通终端分开启动，或者用 Python 子进程规避。这个是本机环境问题，不是 CoreRank 服务端逻辑失败。

## 6. 后续每阶段必须补的测试

### 阶段 0 测试清单

必须有：

```powershell
go test ./...
go vet ./...
python scripts\rest_demo.py
```

CI 必须跑：

```text
go test ./...
go vet ./...
```

验收重点：

- README 中每条验证命令都能跑。
- GitHub Actions 通过。
- 不需要 MySQL。

### 阶段 1 测试清单

新增单元测试：

- `MatchTicket` 状态流转。
- 重复入队。
- 取消匹配。
- 超时处理。
- 匹配成功生成 `match_id`、`room_id`、`ServerID` 和 `ServerAddr`。
- 房间资源分配失败时玩家不会从 ticket pool 中消失。
- 多个 server 时选择低负载 server，不可用 server 不会被选中。

新增集成测试：

- 两个玩家入队后被同一次匹配摘取。
- 被匹配玩家不会再次出现在池中。
- 取消后的玩家不会被匹配。
- 查询匹配结果能返回 `matched` 状态。
- 查询匹配结果能返回被分配的 server 信息。

建议命令：

```powershell
go test ./...
go test -race ./internal/service ./internal/repository
```

### 阶段 2 测试清单

新增 MySQL 测试：

- 初始化表结构。
- 玩家创建与查询。
- 匹配票据写入。
- 匹配结果写入。
- 唯一索引防重复。
- 事务失败回滚。

建议用 Docker Compose 提供测试数据库。

未来可以设计：

```powershell
docker compose up -d redis mysql
go test ./... -tags=integration
```

验收重点：

- Redis 和 MySQL 分工清楚。
- MySQL 表和索引能解释。
- 测试数据可清理。

### 阶段 3 测试清单

新增：

- HTTP/metrics server 收到退出信号后的优雅关闭验证。
- `/metrics` 可访问测试。已补 REST demo 指标检查。
- Robot 压测结果记录。已补 `docs/benchmark.md`。
- 压测后排行榜数据正确性检查。
- Prometheus 指标名称和标签检查。已补基础检查。

建议压测不要只看 TPS，要记录：

- 总请求数。
- 成功请求数。
- 失败请求数。
- 成功率。
- 平均延迟。
- P95/P99，只有通过真实采集后才写；当前已有本机 Prometheus 短窗口记录，仍不能写成生产性能承诺。
- 测试机器环境。
- Redis/MySQL 是否本机。

## 7. 本地测试、服务器测试和面试演示怎么分

### 本地测试

本地测试用于开发阶段。

适合做：

- `go test`
- `go vet`
- REST demo
- 小规模 Robot
- Redis/MySQL Docker Compose

本地测试的价值：

- 快速。
- 可重复。
- 适合写代码时随时跑。

限制：

- 本机性能不能代表生产。
- Windows 环境和 Linux 服务器会有差异。
- 不要把本机压测数字写成通用性能承诺。

### 服务器测试

服务器测试用于展示项目更接近真实部署。

推荐配置：

- 一台 Linux 云服务器。
- Docker Compose 启动 Redis、MySQL、Prometheus、Grafana。
- CoreRank 以二进制或 Docker 容器运行。

适合验证：

- Linux 部署。
- 端口和环境变量。
- Docker Compose。
- 远程访问 RESTful。
- Prometheus 抓取指标。

不必做：

- 不必上 Kubernetes。
- 不必做多机 Redis Cluster。
- 不必做生产级高可用。

### 面试演示

面试演示应该简短、稳定、可解释。

推荐演示路径：

1. 打开 README，说明项目定位。
2. 执行 `go test ./...`。
3. 执行 REST demo。
4. 展示一次 Robot 压测结果。
5. 展示 Prometheus 或 Grafana 本地观测结果。
6. 展示 Redis Lua 脚本。
7. 展示匹配生命周期或 MySQL 表设计文档。

不要现场演示过于复杂的部署。面试官更关心你是否讲得清楚，而不是你现场搭云服务器。

## 8. 为什么按这个顺序做

先做可信展示，是为了修复当前最大风险：

```text
本地增强能力已经存在，但公开仓库可能看不到。
```

再做匹配生命周期，是为了补核心业务深度：

```text
当前匹配只是入池和摘取，不足以称为完整匹配服务。
```

再做 MySQL，是为了让数据库能力服务于业务：

```text
先有 MatchTicket 和 MatchResult，再落库，MySQL 才自然。
```

最后做可观测性和压测，是为了防止文档先行导致夸大：

```text
先有真实功能，再写压测和文档，最不容易翻车。
```

## 9. 当前不应该做什么

暂时不做：

- 不重开新项目。
- 不先做 `ArenaGate`。
- 不直接改简历写未实现功能。
- 不宣称 Redis Cluster。
- 不宣称生产级高并发。
- 不写生产级 P99 承诺。
- 不做 Kubernetes。
- 不做复杂微服务拆分。

原因：

- 这些会分散第一项目深度。
- 实现成本高，但对当前投递提升不一定高。
- 容易被面试官追问穿。

## 10. 最终执行建议

当前阶段 0-3 主线和 Redis-backed 房间资源分配 v1 已完成。下一步不要继续扩大成完整游戏服务器，建议只补“服务器部署/演示证据”：

```text
Linux 服务端部署验证与演示材料
```

具体目标：

- 在一台 Linux 云服务器或本地 Linux 容器环境跑起 CoreRank。
- 记录启动命令、依赖、端口、REST demo 输出和 `/metrics` 抓取结果。
- 明确写清这仍是 demo server registry，不是真实战斗服进程。

这个阶段完成后，再考虑是否补真实 TCP/WebSocket 房间服进程。

## 11. 一句话回答测试问题

`CoreRank` 这种中台一般跑在服务端，本地开发时可以跑在你的电脑和 Docker Compose 里，正式演示可以跑在一台 Linux 云服务器上。测试时不要只测一个接口，要分层测：先 `go test/go vet`，再测 Redis Lua 和 REST/gRPC，再测完整匹配流程，最后用 Robot 做压测，并把环境、命令、结果和限制写清楚。
