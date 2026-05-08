# CoreRank 技术报告

## 报告范围

本文档记录 CoreRank 当前已经实现并可验证的技术能力。它用于帮助阅读者快速理解项目架构、关键设计、验证结果和未完成边界。

本文档不宣称生产落地，不宣称 Redis Cluster、高可用、多实例部署、完整战斗服、WebSocket 房间服或生产级固定 P95/P99。

## 系统定位

CoreRank 是一个 Go 游戏匹配与排行榜中台。它面向游戏网关、房间服、后台工具或测试脚本提供 gRPC / RESTful 接入，内部使用 Redis 处理热数据，使用 MySQL 作为可选持久化层，并通过 Prometheus 暴露运行指标。当前还提供最小 TCP roomserver v1，用于演示匹配结果里的 `ServerAddr` 可以被真实 TCP 连接承接。

## 架构图

```mermaid
graph TB
    Client["游戏网关 / 房间服 / 后台工具"] -->|"gRPC / RESTful"| Server["CoreRank Server"]
    Robot["cmd/robot"] -->|"gRPC UpdateScore"| Server
    Demo["scripts/rest_demo.py"] -->|"RESTful"| Server
    RoomDemo["scripts/room_tcp_demo.py"] -->|"RESTful"| Server
    RoomDemo -->|"TCP JSON-line"| RoomServer["cmd/roomserver"]
    RoomServer -->|"register / heartbeat"| Server

    Server --> GRPC["gRPC Handlers"]
    Server --> HTTP["RESTful Handlers"]
    Server --> Metrics["/metrics"]

    GRPC --> RankService["RankService"]
    GRPC --> MatchService["MatchService"]
    HTTP --> RankService
    HTTP --> MatchService

    RankService --> Redis[("Redis")]
    MatchService --> Redis
    RankService --> MySQL[("MySQL 可选持久化")]
    MatchService --> MySQL
    MatchService -->|"RoomID / ServerAddr"| RoomServer
```

## 分层说明

| 层级 | 目录 | 职责 |
|---|---|---|
| 入口层 | `cmd/server` | 启动 Redis、gRPC、RESTful、metrics 和匹配 Worker |
| 房间服入口 | `cmd/roomserver` | 启动最小 TCP 房间服，注册到 CoreRank 并承接 `RoomID` |
| 协议层 | `api/proto` | 定义 gRPC 消息和服务 |
| Handler 层 | `internal/handler` | 处理 RESTful 和 gRPC 请求 |
| Service 层 | `internal/service` | 排行榜、匹配票据、超时扫描、房间分配抽象 |
| Repository 层 | `internal/repository` | Redis、Lua 脚本和 MySQL 读写 |
| 指标层 | `internal/metrics` | Prometheus 指标定义和记录 |
| 工具层 | `cmd/robot`、`scripts` | 压测和演示 |

## 核心技术点

### 1. Redis Lua 原子匹配

匹配系统的关键风险是重复摘取同一个玩家。CoreRank 使用 Redis Lua 脚本把候选查询和候选删除放进同一次 Redis 原子执行中，减少并发重复匹配风险。

核心价值：

- 避免 `ZRANGEBYSCORE` 和 `ZREM` 分离导致的竞态。
- 减少多次网络往返。
- 让匹配池状态变化集中在 Redis 内完成。

### 2. MatchTicket / MatchResult 生命周期

CoreRank 已经从简单匹配池升级为票据生命周期模型：

```text
CreateMatchTicket
  -> queued
  -> matched / cancelled / timeout
  -> GetMatchTicket / GetMatchResult
```

已实现状态：

- `queued`
- `matched`
- `cancelled`
- `timeout`

当前 `room_id` 是逻辑房间 ID。匹配成功后，CoreRank 会通过 Redis-backed server registry 选择可用 roomserver，并在结果中返回 `ServerAddr`。最小 TCP roomserver v1 已能承接该地址并完成 join/ready/leave，但这不代表完整战斗服调度、状态同步或断线重连已经完成。

### 3. Redis 与 MySQL 分工

Redis 负责：

- 排行榜热数据。
- 匹配池。
- 短期匹配票据。
- 短期匹配结果。
- 票据超时扫描索引。

MySQL 负责：

- 玩家分数持久化。
- 匹配票据持久化。
- 匹配结果持久化。
- 榜单快照。

MySQL 默认是可选持久化层。连接失败或写入失败时，服务会记录 warning 并继续返回 Redis 主链路结果。需要强依赖时可使用 `CORERANK_MYSQL_REQUIRED=true`。

### 4. Prometheus 指标

当前已经暴露：

- gRPC 请求计数。
- gRPC 请求延迟直方图。
- 匹配成功计数。
- 匹配取消计数。
- 匹配超时计数。
- 匹配票据事件计数。
- 匹配票据终态耗时直方图。
- queued 票据数量。

指标可以通过 `http://localhost:9091/metrics` 查看。当前已经补充 Docker Compose 本地观测栈，Prometheus 能抓取 `corerank-server` target，Grafana 能通过 provisioning 创建 `Prometheus` datasource 和 `CoreRank Overview` dashboard。

### 5. 最小 TCP roomserver v1

`cmd/roomserver` 是为了补齐“匹配结果到房间服连接”的面试演示闭环。它启动后会：

- 调用 CoreRank RESTful API 注册自己。
- 定期发送 heartbeat。
- 监听 TCP JSON-line 请求。
- 支持 `join` / `ready` / `leave` / `ping`。
- 在同一房间至少 2 名玩家全部 ready 后返回 `room_started`。

该模块只维护进程内内存房间状态，不包含完整战斗逻辑、WebSocket、鉴权、断线重连或状态同步。

## 本机压测记录

当前可引用的压测记录来自 `docs/benchmark.md`：

| 指标 | 结果 |
|---|---|
| 测试环境 | Windows 本机 |
| Redis | 本机 `127.0.0.1:6379` |
| 请求类型 | gRPC `UpdateScore` |
| 总请求数 | 10000 |
| 成功请求数 | 10000 |
| 失败请求数 | 0 |
| 成功率 | 100.00% |
| TPS | 29916.63 req/sec |
| 平均延迟 | 3.22 ms |

边界说明：

- 这是本机开发环境结果。
- 本轮 Robot 压测未启用 MySQL。
- 这组高吞吐记录只记录平均延迟，不记录 P95/P99。
- 该数据不能写成线上吞吐或线上延迟承诺。

本地 Docker 观测栈验证记录：

| 指标 | 结果 |
|---|---|
| 测试环境 | Windows 本机 + Docker Compose |
| MySQL | Docker Compose `127.0.0.1:3307`，强依赖模式 |
| 请求类型 | gRPC `UpdateScore` |
| 正式 Robot 请求数 | 1000 |
| 成功率 | 100.00% |
| TPS | 1068.04 req/sec |
| Robot 平均延迟 | 18.22 ms |
| Prometheus P95 | 35.40 ms |
| Prometheus P99 | 49.30 ms |

边界说明：

- 本轮用于验证 Prometheus/Grafana/MySQL 本地链路，不用于宣称生产性能。
- P95/P99 采用短窗口 PromQL 查询，并包含预热请求以便 `rate()` 能得到有效变化率。

## 验证证据

当前已经执行过的验证包括：

- `go test ./...`
- `go test ./...`，带本机 MySQL 测试 DSN
- `go test -count=3 ./...`，带 Redis/MySQL
- `go vet ./...`
- `go build ./cmd/server`
- `go build ./cmd/roomserver`
- `go build ./cmd/robot`
- `python scripts\rest_demo.py`
- `python scripts\room_tcp_demo.py`
- Robot 本机压测
- GitHub Actions CI

详见：

- `docs/verification.md`
- `docs/benchmark.md`
- `docs/demo-guide.md`

## 未完成技术项

| 项目 | 当前状态 |
|---|---|
| 最小 TCP roomserver v1 | 已实现 |
| WebSocket 房间服 | 未实现 |
| 完整战斗服调度、状态同步、断线重连 | 未实现 |
| 匹配结果主动通知 | 未实现 |
| JWT / 账号鉴权 | 未实现 |
| Redis Cluster 实测 | 未实现 |
| Linux 云服务器部署验证 | 未实现 |
| 生产级 P95/P99 长时压测 | 未实现 |
| 多实例高可用 | 未实现 |

## 面试表达边界

可以说：

- 使用 Go 实现了匹配与排行榜中台。
- 使用 Redis ZSet 和 Lua 脚本降低重复匹配风险。
- 设计了 `MatchTicket` / `MatchResult` 状态流转。
- 通过 Redis-backed server registry 分配 roomserver，并用最小 TCP roomserver v1 演示 join/ready/leave 闭环。
- 接入了 MySQL 可选持久化和故障降级。
- 接入了 Prometheus 指标和本地 Grafana dashboard。
- 有 CI、REST demo、Robot 压测和验证文档。

不要说：

- 已经生产落地。
- 已经支持完整房间服、WebSocket 房间服或战斗服调度。
- 已经验证 Redis Cluster。
- 已经完成生产级 Grafana 告警体系。
- 已经采集生产 P99。
- 已经具备生产级高可用。

## 结论

CoreRank 当前已经从“排行榜 + 匹配池 demo”推进到“可验证的匹配与排行榜中台 + 最小 TCP 房间服原型”。它适合作为 Go 游戏服务端方向简历项目，但公开材料需要始终保持边界清楚：已完成的是本机可验证的中台核心能力、Redis-backed 房间资源分配、最小 TCP roomserver v1 和本地观测栈，后续仍需要补 Linux 部署、完整战斗服、生产级告警和长时压测。
