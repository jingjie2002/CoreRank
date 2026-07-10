# CoreRank 本地可观测性

当前 Prometheus 抓取三个分布式进程，而不是旧单进程 `corerank-server:9091`：

| job | 地址 |
|---|---|
| `corerank-gateway` | `corerank-gateway:19080/metrics` |
| `corerank-rank-service` | `corerank-rank-service:19081/metrics` |
| `corerank-match-service` | `corerank-match-service:19082/metrics` |

本机端口都通过 Docker Compose 绑定到 `127.0.0.1`。Prometheus 为 `http://127.0.0.1:9090`，Grafana 为 `http://127.0.0.1:3000`。管理员密码通过 `.env` 的 `GF_SECURITY_ADMIN_PASSWORD` 配置，匿名访问默认关闭。

## 启动与验收

```powershell
docker compose up -d corerank-redis corerank-rank-service corerank-match-service corerank-gateway corerank-roomserver prometheus grafana
powershell -ExecutionPolicy Bypass -File scripts\distributed_smoke.ps1
```

smoke 会检查 gateway `/readyz`、三个 metrics 端点、Prometheus targets 和 Grafana dashboard provisioning。

## 关键指标

| 指标 | 含义 |
|---|---|
| `corerank_grpc_requests_total` | gRPC 请求数，按方法和状态分类 |
| `corerank_grpc_request_latency_seconds` | gRPC 延迟直方图 |
| `corerank_matcher_ticket_events_total` | ticket 生命周期事件 |
| `corerank_matcher_lifecycle_duration_seconds` | ticket 到终态的时长 |
| `corerank_matcher_queued_tickets` | 按 match mode 隔离的排队数 |
| `corerank_room_assignment_total` | 房服分配结果 |
| `corerank_room_assignment_failures_total` | 房服分配失败原因 |
| `corerank_room_server_load` | 分配器持有的预留玩家槽位 |

示例 PromQL：

```promql
sum by (method, status) (rate(corerank_grpc_requests_total[5m]))
```

```promql
histogram_quantile(0.95,
  sum by (le, method) (rate(corerank_grpc_request_latency_seconds_bucket[5m])))
```

```promql
sum by (match_mode, status) (increase(corerank_matcher_ticket_events_total[5m]))
```

```promql
corerank_matcher_queued_tickets
```

## 边界

该栈只提供本地指标和 dashboard，没有生产告警、分布式追踪、集中日志、长期保留或 SLO。P95/P99 必须引用具体一次测试的环境、时间窗和查询结果，不能写成生产性能承诺。
