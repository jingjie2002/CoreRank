# CoreRank 本地运行与验证指南

本文档记录 CoreRank 在本机环境中的推荐运行方式。当前主路径是 Docker Compose 分布式栈；单进程入口仍保留用于兼容调试。

## 1. 环境要求

- Go 1.25.x
- Docker Desktop / Docker Compose
- Python 3，用于本地脚本
- PowerShell，用于 Windows 本地命令示例

可选：

- MySQL 客户端工具
- Redis 客户端工具

## 2. 服务组件

| 组件 | 说明 | 默认地址 |
|---|---|---|
| `gateway` | HTTP API 入口 | `http://127.0.0.1:8081` |
| `rank-service` | 排行榜 gRPC 服务 | `127.0.0.1:18081` |
| `match-service` | 匹配 gRPC 服务 | `127.0.0.1:18082` |
| `roomserver` | TCP JSON-line 房间服 | `127.0.0.1:7001` |
| Redis | 共享状态存储 | `127.0.0.1:6379` |
| MySQL | 可选持久化 | `127.0.0.1:3307` |
| Prometheus | 指标采集 | `http://127.0.0.1:9090` |
| Grafana | 指标看板 | `http://127.0.0.1:3000` |

## 3. 启动分布式栈

如果电脑刚重启，或之前运行本项目时出现过卡死、死机、Docker Desktop 异常，先执行低负载前置检查：

```powershell
powershell -NoProfile -ExecutionPolicy Bypass -File scripts\stability_preflight.ps1 -CheckEndpoints
```

该脚本只检查内存、磁盘、常用端口、Docker 相关进程和可选 HTTP 端点，不会启动 Docker、不构建镜像、不写入业务数据。

构建服务镜像：

```powershell
docker compose build corerank-rank-service corerank-match-service corerank-gateway corerank-roomserver
```

启动分布式栈：

```powershell
docker compose up -d corerank-redis corerank-rank-service corerank-match-service corerank-gateway corerank-roomserver prometheus grafana
```

查看服务状态：

```powershell
docker compose ps
```

预期结果：

- `corerank-redis` 为 `healthy`。
- `corerank-rank-service`、`corerank-match-service`、`corerank-gateway`、`corerank-roomserver` 均为 `Up`。
- Prometheus 和 Grafana 为 `Up`。

## 4. 运行分布式 Smoke

```powershell
powershell -ExecutionPolicy Bypass -File scripts\distributed_smoke.ps1
```

脚本会验证：

1. gateway healthz。
2. gateway、rank-service、match-service metrics。
3. `compose-room-1` roomserver 自动注册和可用状态。
4. 重复创建 ticket 返回冲突错误。
5. queued ticket 可取消，重复取消返回冲突错误。
6. 两个玩家创建匹配票据并匹配成功。
7. 匹配结果包含 `ServerID` 和 `ServerAddr`。
8. TCP roomserver 完成 `join -> ready -> room_started -> leave`。
9. 比赛结算写入排行榜。
10. TopN 和玩家排名查询。
11. Prometheus targets 为 `up`。
12. Grafana dashboard 可搜索。

通过后会输出一段 JSON 结果，其中包含 `cancel_repeat_status_code`、`duplicate_repeat_status_code`、`match_id`、`room_id`、`match_server_id`、`match_server_addr`、`leaderboard_type` 等证据字段。

## 5. 受控长稳探测

```powershell
powershell -NoProfile -ExecutionPolicy Bypass -File scripts\distributed_soak.ps1 -DurationMinutes 10 -IntervalSeconds 30
```

脚本会先执行稳定性前置检查。如果可用内存或磁盘空间低于阈值，会输出 `blocked` 并停止，不继续制造请求。资源满足时，它会按间隔检查 gateway、Prometheus、Grafana 和 Prometheus targets。

可选在开始或结束时执行完整 smoke：

```powershell
powershell -NoProfile -ExecutionPolicy Bypass -File scripts\distributed_soak.ps1 -DurationMinutes 10 -IntervalSeconds 30 -RunSmokeAtStart -RunSmokeAtEnd
```

## 6. 单进程兼容模式

单进程模式适合简单调试。它不会体现当前分布式服务边界。

启动 Redis：

```powershell
docker compose up -d corerank-redis
```

启动服务：

```powershell
go run ./cmd/server
```

默认地址：

| 组件 | 地址 |
|---|---|
| REST | `http://127.0.0.1:8081` |
| gRPC | `127.0.0.1:8080` |
| Metrics | `http://127.0.0.1:9091/metrics` |

## 7. REST 脚本

```powershell
python scripts\rest_demo.py
```

该脚本会自动构建并启动临时单进程服务端，验证：

- 排行榜写入和查询。
- 多榜单 `leaderboard_type`。
- room server 注册。
- 匹配票据创建。
- 匹配结果查询。
- 票据超时。
- metrics 指标存在。

## 8. TCP RoomServer 脚本

```powershell
python scripts\room_tcp_demo.py
```

该脚本会自动构建并启动临时 CoreRank Server 和 roomserver，验证：

- roomserver 注册和 heartbeat。
- 两个玩家创建匹配票据。
- 匹配结果返回 `RoomID`、`ServerID` 和 `ServerAddr`。
- 两个 TCP 客户端连接 roomserver 并完成 `join`、`ready`、`room_started`、`leave`。

## 9. gRPC Robot

先启动单进程服务：

```powershell
go run ./cmd/server
```

另开终端运行：

```powershell
go run ./cmd/robot
```

默认参数：

```text
100 workers
100 requests per worker
10000 total UpdateScore requests
```

可通过环境变量调整：

```powershell
$env:ROBOT_GRPC_ADDR="localhost:8080"
$env:ROBOT_WORKERS="20"
$env:ROBOT_REQUESTS_PER_WORKER="50"
go run ./cmd/robot
```

## 10. Go 测试与静态检查

```powershell
$env:GOCACHE = Join-Path (Get-Location) ".gocache"
go test ./...
go vet ./...
```

Redis 不可用时，部分 Redis 集成测试会跳过。完整验收应在 Redis 可用时执行。

## 11. MySQL 持久化验证

MySQL 是可选持久化层。使用 Docker Compose 中的 MySQL 时，DSN 示例：

```powershell
$env:CORERANK_MYSQL_DSN="corerank:corerank_demo@tcp(127.0.0.1:3307)/corerank?parseTime=true&charset=utf8mb4&loc=Local"
```

MySQL 集成测试需要单独设置测试 DSN：

```powershell
$env:CORERANK_TEST_MYSQL_DSN="corerank:<password>@tcp(127.0.0.1:3306)/corerank_test?parseTime=true&charset=utf8mb4&loc=Local"
go test ./...
```

如需启动时强制依赖 MySQL：

```powershell
$env:CORERANK_MYSQL_REQUIRED="true"
```

## 12. 常见问题

### Docker 命令权限

在部分 Windows 环境中，Docker 命令可能需要提升权限才能读取 Docker Desktop 配置或执行 build/up。

### 端口占用

默认端口包括 `6379`、`7001`、`8081`、`18081`、`18082`、`19080`、`19081`、`19082`、`9090`、`3000`。如果端口被占用，需要调整 Compose 映射或对应环境变量。

### Grafana 数据为空

先确认 Prometheus targets 是否为 `up`：

```text
http://127.0.0.1:9090/targets
```

再确认 `scripts/distributed_smoke.ps1` 已经产生过服务请求。

### roomserver 注册失败

检查：

- `corerank-gateway` 是否运行。
- `ROOM_SERVER_PUBLIC_ADDR` 是否是宿主机可访问地址。
- `CORE_RANK_HTTP` 是否指向 `http://corerank-gateway:8081`。
