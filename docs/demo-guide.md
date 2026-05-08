# CoreRank 本地测试与面试演示指南

这份指南按“没有服务器经验也能讲清楚”的方式组织。你可以先把自己的电脑理解成一台小服务器：CoreRank 服务端、Redis、MySQL、Robot 客户端都可以先跑在本机。

## 你需要理解的角色

| 角色 | 在真实公司里 | 你本机演示时 |
|---|---|---|
| CoreRank Server | 跑在 Linux 服务器或容器里 | 跑在你的 Windows 终端里 |
| Redis | 独立缓存/状态服务 | 本机 Redis 或 Docker Redis |
| MySQL | 独立数据库 | 本机 MySQL 测试库 |
| Robot | 压测客户端 | 本机另一个终端 |
| Prometheus | 定时抓 `/metrics` | 你可以直接浏览器打开 `/metrics` |
| Grafana | 把 Prometheus 指标画成图 | 你可以打开 `http://localhost:3000` |

## 最稳的本地验收顺序

如果要演示 Grafana，请先启动 Docker Desktop，再启动本地观测栈。只跑 REST demo 和 Go 测试时，不需要 Grafana。

### 1. 先确认代码和 CI

```powershell
cd F:\AI编程\简历\CoreRank
git status
```

期望看到：

```text
On branch main
nothing to commit, working tree clean
```

GitHub Actions 通过，说明公开仓库能自动跑测试、静态检查和构建。

### 2. 跑自动测试

```powershell
$env:GOCACHE = Join-Path (Get-Location) ".gocache"
go test ./...
go vet ./...
```

这一步证明代码能编译，核心 Redis/MySQL 测试能跑。

### 3. 跑 REST demo

```powershell
python scripts\rest_demo.py
```

这一步会自动构建并启动临时服务端，然后演示：

- 排行榜写入和查询。
- 匹配票据创建。
- 两个玩家匹配成功并生成 `match_id`、`room_id`。
- 短等待票据变成 `timeout`。
- `/metrics` 中存在匹配业务指标。

### 4. 跑 Robot 压测

先启动服务端：

```powershell
go run ./cmd/server
```

另开一个终端：

```powershell
go run ./cmd/robot
```

默认会跑：

```text
100 个 goroutine
每个 goroutine 100 次请求
总计 10000 次 gRPC UpdateScore
```

如果只想快速演示，可以降低参数：

```powershell
$env:ROBOT_WORKERS="20"
$env:ROBOT_REQUESTS_PER_WORKER="50"
go run ./cmd/robot
```

## 面试时怎么演示

推荐顺序：

1. 打开 README，先说项目定位：Go 游戏匹配与排行榜中台。
2. 跑 `go test ./...`，证明不是只会讲。
3. 跑 `python scripts\rest_demo.py`，展示匹配生命周期。
4. 打开 `http://localhost:9091/metrics`，说明 Prometheus 指标。
5. 打开 `http://localhost:3000`，展示 CoreRank Overview dashboard。
6. 跑一次 Robot，展示 10000 次 gRPC 请求结果。
7. 打开 `docs/benchmark.md`，说明压测数字的环境和边界。

## 你可以怎么解释“服务器”

可以这样说：

```text
这个项目本质上是服务端中间服务。真实公司里会跑在 Linux 服务器、容器或 Kubernetes 里；我当前简历项目先保证本机和 GitHub CI 可复现，本地用 Redis/MySQL 模拟依赖，用 Robot 模拟内部服务请求。后续如果需要正式演示，可以把同样的服务端二进制或 Docker Compose 放到一台 Linux 云服务器上跑。
```

## 不要现场硬做的事

- 不要现场临时装 MySQL 或 Docker。
- 不要现场讲 Redis Cluster 已落地。
- 不要把本机 TPS 说成生产 TPS。
- 不要说已经有真实房间服/战斗服调度。
- Grafana 只能说明“本地观测演示栈”，不能说明生产监控已落地。

面试演示的重点不是把云服务器搭得多复杂，而是你能稳定复现、能解释架构边界、能讲清楚每个测试证明了什么。
