# CoreRank

CoreRank 是一个面向竞技游戏场景的 Go 匹配与排行榜服务。项目提供 gRPC 和 RESTful 两种接入方式，使用 Redis 保存匹配池、匹配票据和排行榜热数据，并通过 Redis Lua 脚本减少候选玩家被重复匹配的风险。

这个仓库用于演示一条轻量的服务端链路：玩家更新积分、进入匹配队列、生成匹配结果、分配房间资源，并通过接口查询排行榜和匹配结果。

## 功能

- 排行榜：更新玩家分数、查询 TopN、查询个人名次。
- 多榜单维度：支持全局榜、赛季榜、活动榜等 `leaderboard_type`。
- 匹配票据：创建、取消、查询票据状态和匹配结果。
- Redis Lua：将候选玩家查询和移除合并为一次原子执行。
- 房间资源分配：注册 room server，按匹配模式和容量选择可用服务。
- TCP 房间服示例：`cmd/roomserver` 支持入房、准备、离开和心跳。
- 可选 MySQL 持久化：玩家分数、匹配票据、匹配结果和榜单快照。
- Prometheus 指标：请求耗时、匹配成功、取消、超时、队列数量和房间分配状态。
- 本地观测栈：Docker Compose 提供 Redis、MySQL、Prometheus 和 Grafana。
- 演示脚本：提供 RESTful 演示、TCP 房间服闭环演示和 gRPC robot 压测脚本。

## 技术栈

- Go 1.25
- gRPC / Protobuf
- RESTful HTTP
- Redis / Redis Lua / Redis ZSet
- MySQL
- Prometheus / Grafana
- Docker Compose

## 目录结构

```text
cmd/server/          CoreRank 服务端入口
cmd/roomserver/      TCP 房间服示例
cmd/robot/           gRPC 请求压测脚本
api/proto/           Protobuf 协议与生成代码
internal/handler/    gRPC、RESTful 和 Agent 接口
internal/service/    排行榜、匹配和房间分配业务
internal/repository/ Redis、MySQL 和 Lua 脚本封装
internal/metrics/    Prometheus 指标
pkg/redis/           Redis 客户端封装
scripts/             本地演示脚本
docs/                API、架构、部署和验证文档
grafana/             Grafana dashboard 与 provisioning 配置
```

## 快速开始

### 1. 启动依赖

最小运行依赖是 Redis：

```powershell
docker compose up -d corerank-redis
```

如果需要同时启动 Redis、MySQL、Prometheus 和 Grafana：

```powershell
docker compose up -d corerank-redis corerank-mysql prometheus grafana
```

### 2. 启动服务

```powershell
go run ./cmd/server
```

默认端口：

| 服务 | 默认地址 |
|---|---|
| gRPC | `:8080` |
| RESTful | `:8081` |
| Prometheus metrics | `:9091` |
| Prometheus | `http://localhost:9090` |
| Grafana | `http://localhost:3000` |

可以通过环境变量修改监听地址：

```powershell
$env:GRPC_ADDR="127.0.0.1:18080"
$env:HTTP_ADDR="127.0.0.1:18081"
$env:METRICS_ADDR="127.0.0.1:19091"
go run ./cmd/server
```

### 3. 启用 MySQL 持久化

MySQL 是可选持久化层。未配置 DSN 时，服务仍会使用 Redis 主链路处理排行榜和匹配请求。

```powershell
$env:CORERANK_MYSQL_DSN="corerank:corerank_demo@tcp(127.0.0.1:3307)/corerank?parseTime=true&charset=utf8mb4&loc=Local"
go run ./cmd/server
```

如果希望启动时强制要求 MySQL 可用：

```powershell
$env:CORERANK_MYSQL_REQUIRED="true"
```

## Demo

RESTful 演示：

```powershell
python scripts\rest_demo.py
```

TCP 房间服闭环演示：

```powershell
python scripts\room_tcp_demo.py
```

`room_tcp_demo.py` 会自动构建并启动临时 CoreRank Server 和 `cmd/roomserver`，完成 room server 注册、玩家创建匹配票据、返回房间地址、TCP 客户端入房和准备流程。

gRPC robot：

```powershell
go run ./cmd/robot
```

robot 默认使用 100 个 goroutine，每个 goroutine 发送 100 次 `UpdateScore`。可以通过环境变量调整：

```powershell
$env:ROBOT_GRPC_ADDR="localhost:8080"
$env:ROBOT_WORKERS="100"
$env:ROBOT_REQUESTS_PER_WORKER="100"
go run ./cmd/robot
```

## 验证

推荐在修改后执行：

```powershell
$env:GOCACHE = Join-Path (Get-Location) ".gocache"
go test ./...
go vet ./...
python scripts\rest_demo.py
python scripts\room_tcp_demo.py
```

MySQL 集成测试需要显式提供测试 DSN：

```powershell
$env:CORERANK_TEST_MYSQL_DSN="corerank:<password>@tcp(127.0.0.1:3306)/corerank_test?parseTime=true&charset=utf8mb4&loc=Local"
go test ./...
```

## Agent 接入

CoreRank 提供一组只读的 Agent 接入口，方便外部工具读取项目状态和能力声明。

| 能力 | 入口 |
|---|---|
| 项目声明 | `agent.yaml` |
| 健康检查 | `GET /healthz`，兼容旧 `GET /health` |
| 能力声明 | `GET /api/agent/capabilities` |
| Agent events | `GET /api/agent/events` |
| Agent logs | `GET /api/agent/logs` |
| Agent smoke test | `python scripts\agent_smoke.py` |

离线检查：

```powershell
python scripts\agent_smoke.py --offline
```

服务启动后检查：

```powershell
python scripts\agent_smoke.py --base-url http://127.0.0.1:8081
```

## 文档

- [验证指南](./docs/verification.md)
- [Agent 接入说明](./docs/agent-integration.md)
- [API 文档](./docs/api.md)
- [架构文档](./docs/architecture.md)
- [部署与结算说明](./docs/deployment-and-settlement.md)
- [本地测试与演示指南](./docs/demo-guide.md)
- [本地观测栈](./docs/observability.md)
- [测试策略](./docs/optimization-and-testing-plan.md)
- [压测记录](./docs/benchmark.md)
- [2026-05-06 验证记录](./docs/verification-2026-05-06.md)

## 当前限制

- 不包含完整账号系统、JWT 鉴权或反作弊。
- 不包含完整战斗服逻辑、帧同步或断线重连。
- 未做 Redis Cluster 部署验证。
- 未提供 Kubernetes 或线上服务发现配置。
- 性能脚本结果只代表本机环境和测试参数，不代表生产承诺。

## License

未指定。
