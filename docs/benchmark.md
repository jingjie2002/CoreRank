# CoreRank 压测记录

本文档记录 CoreRank 当前可复现的本机压测结果。这里的数字只代表本机开发环境，不代表生产承诺。

## 本轮结果

- 记录时间：2026-05-06 23:21
- 测试机器：Windows 本机
- Go 版本：`go1.25.0 windows/amd64`
- Redis：本机 `127.0.0.1:6379`
- CoreRank Server：本机临时端口 `127.0.0.1:18280`
- MySQL：本轮 Robot 压测未启用 MySQL，验证 Redis 热路径和 gRPC 排行榜写入能力
- Robot 参数：`ROBOT_WORKERS=100`，`ROBOT_REQUESTS_PER_WORKER=100`
- 总请求数：`10000`
- 成功请求数：`10000`
- 失败请求数：`0`
- 成功率：`100.00%`
- 总耗时：`334.2623ms`
- TPS：`29916.63 req/sec`
- 平均延迟：`3.22 ms`

## 执行命令

先启动服务端：

```powershell
$env:GRPC_ADDR="127.0.0.1:18280"
$env:HTTP_ADDR="127.0.0.1:18281"
$env:METRICS_ADDR="127.0.0.1:19291"
go run ./cmd/server
```

另开一个终端运行 Robot：

```powershell
$env:ROBOT_GRPC_ADDR="127.0.0.1:18280"
$env:ROBOT_WORKERS="100"
$env:ROBOT_REQUESTS_PER_WORKER="100"
go run ./cmd/robot
```

## 指标证据

Robot 压测后，Prometheus metrics 端点记录到：

```text
corerank_grpc_requests_total{method="UpdateScore",status="ok"} 10000
corerank_grpc_request_latency_seconds_count{method="UpdateScore"} 10000
```

这说明 10000 次 `UpdateScore` gRPC 请求被服务端指标记录。该次 Robot 直接输出平均延迟；P95/P99 已在后续 Docker 观测栈验证中用 Prometheus histogram 和 PromQL 补充，见下方 2026-05-08 记录。

## PromQL 分位数查询

如果使用本地观测栈，可以在 Prometheus 或 Grafana 中查询：

```promql
histogram_quantile(0.95, sum by (le, method) (rate(corerank_grpc_request_latency_seconds_bucket[5m])))
```

```promql
histogram_quantile(0.99, sum by (le, method) (rate(corerank_grpc_request_latency_seconds_bucket[5m])))
```

只有实际查询到结果后，才能把 P95/P99 作为本地压测记录补充进本文档；当前本文档仅记录已经实际查询到的本机结果。

## 2026-05-08 Docker 观测栈结果

本轮用于验证 Docker Compose 本地观测栈、MySQL 持久化、Prometheus 抓取和 Grafana dashboard provisioning。

- 记录时间：2026-05-08 12:39
- 测试机器：Windows 本机
- 依赖：Docker Desktop 4.50.0，Docker Engine 28.5.1
- Redis：Docker Compose `corerank-redis`，映射 `127.0.0.1:6379`
- MySQL：Docker Compose `corerank-mysql`，映射 `127.0.0.1:3307`
- Prometheus：Docker Compose `corerank-prometheus`，`http://localhost:9090`
- Grafana：Docker Compose `corerank-grafana`，`http://localhost:3000`
- CoreRank Server：本机临时进程，`CORERANK_MYSQL_REQUIRED=true`
- Robot 参数：先 1 次预热请求，再 `ROBOT_WORKERS=20`、`ROBOT_REQUESTS_PER_WORKER=50`
- 正式 Robot 总请求数：`1000`
- 成功率：`100.00%`
- TPS：`1068.04 req/sec`
- Robot 平均延迟：`18.22 ms`

Prometheus 查询结果：

```text
Prometheus target: corerank-server up
sum by (method) (increase(corerank_grpc_request_latency_seconds_count[1m]))
UpdateScore = 1029.4779050736495
```

```text
histogram_quantile(0.95, sum by (le, method) (rate(corerank_grpc_request_latency_seconds_bucket[1m])))
UpdateScore = 0.03539978806442485s
```

```text
histogram_quantile(0.99, sum by (le, method) (rate(corerank_grpc_request_latency_seconds_bucket[1m])))
UpdateScore = 0.04930218001414296s
```

换算后，本轮本机观测值约为：

| 指标 | 结果 |
|---|---:|
| P95 | 35.40 ms |
| P99 | 49.30 ms |

注意：这里的分位数来自本机 Docker + MySQL + Prometheus 的短窗口演示验证。为了让 Prometheus 的 `rate()` 正确计算，流程包含“预热一次请求、等待 Prometheus 抓初始样本、再运行 Robot、再等待抓取”的步骤。这个结果可以作为本地演示证据，不能写成生产延迟承诺。

## 解释边界

- 可以说：本机 Robot 压测 10000 次 gRPC `UpdateScore` 请求，成功率 100%，平均延迟约 3.22ms。
- 可以说：本机 Docker 观测栈验证中，Prometheus 成功采集 `UpdateScore` P95/P99，Grafana dashboard 已完成本地 provisioning。
- 必须说明：这是 Windows 本机 + 本机 Redis 或本机 Docker Compose 的开发环境结果。
- 不要说：生产 TPS、生产 P99、Redis Cluster 性能、云服务器性能。
- 不要把单次本机结果写成固定承诺；如果换机器、换 Redis、开 MySQL、开 Docker 或上云，结果都会变化。
