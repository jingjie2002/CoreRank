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

这说明 10000 次 `UpdateScore` gRPC 请求被服务端指标记录。当前 Robot 直接输出平均延迟；P95/P99 需要后续用 Prometheus histogram 和 PromQL 计算后再写入简历。

## 解释边界

- 可以说：本机 Robot 压测 10000 次 gRPC `UpdateScore` 请求，成功率 100%，平均延迟约 3.22ms。
- 必须说明：这是 Windows 本机 + 本机 Redis 的开发环境结果。
- 不要说：生产 TPS、生产 P99、Redis Cluster 性能、云服务器性能。
- 不要把单次本机结果写成固定承诺；如果换机器、换 Redis、开 MySQL、开 Docker 或上云，结果都会变化。
