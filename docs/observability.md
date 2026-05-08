# CoreRank 本地观测栈

本文档说明如何在本机使用 Docker Compose 启动 Redis、MySQL、Prometheus 和 Grafana，并用 CoreRank `/metrics` 观察 gRPC 和匹配业务指标。

## 1. 组成

| 服务 | 默认地址 | 说明 |
|---|---|---|
| Redis | `127.0.0.1:6379` | 排行榜和匹配热数据 |
| MySQL | `127.0.0.1:3307` | 可选持久化层，避免占用本机已有 `3306` |
| Prometheus | `http://localhost:9090` | 抓取 CoreRank `/metrics` |
| Grafana | `http://localhost:3000` | 展示 CoreRank dashboard |
| CoreRank metrics | `http://localhost:9091/metrics` | 服务端指标端点 |

Grafana 默认账号密码：

```text
admin / admin
```

这是本地开发配置，不应作为生产密码使用。

## 2. 启动依赖

先确认 Docker Desktop 已启动。如果 Docker Desktop Service 处于 stopped 状态，`docker compose up` 会无法连接 `docker_engine`。

```powershell
docker compose up -d corerank-redis corerank-mysql prometheus grafana
```

查看容器状态：

```powershell
docker compose ps
```

## 3. 启动 CoreRank

如果只验证 Redis 热路径：

```powershell
go run ./cmd/server
```

如果要启用 Docker Compose 中的 MySQL：

```powershell
$env:CORERANK_MYSQL_DSN="corerank:corerank_demo@tcp(127.0.0.1:3307)/corerank?parseTime=true&charset=utf8mb4&loc=Local"
go run ./cmd/server
```

如果希望服务启动时强制要求 MySQL 可用：

```powershell
$env:CORERANK_MYSQL_REQUIRED="true"
```

## 4. 产生指标数据

RESTful 演示：

```powershell
python scripts\rest_demo.py
```

Robot 压测：

```powershell
go run ./cmd/robot
```

## 5. 打开 Grafana

浏览器访问：

```text
http://localhost:3000
```

进入：

```text
Dashboards -> CoreRank -> CoreRank Overview
```

当前 dashboard 包含：

- gRPC 请求速率。
- gRPC P95/P99 延迟。
- 匹配票据事件。
- queued 票据数量。
- 匹配生命周期 P95/P99。

## 6. PromQL 查询

gRPC 请求速率：

```promql
sum by (method, status) (rate(corerank_grpc_requests_total[1m]))
```

gRPC P95 延迟：

```promql
histogram_quantile(0.95, sum by (le, method) (rate(corerank_grpc_request_latency_seconds_bucket[5m])))
```

gRPC P99 延迟：

```promql
histogram_quantile(0.99, sum by (le, method) (rate(corerank_grpc_request_latency_seconds_bucket[5m])))
```

匹配票据事件：

```promql
sum by (status) (increase(corerank_matcher_ticket_events_total[5m]))
```

匹配生命周期 P95：

```promql
histogram_quantile(0.95, sum by (le, status) (rate(corerank_matcher_lifecycle_duration_seconds_bucket[5m])))
```

当前 queued 票据数量：

```promql
corerank_matcher_queued_tickets
```

## 7. 关闭

```powershell
docker compose down
```

如果要清理本地容器数据，请先确认不再需要 `data/` 下的 Redis、MySQL、Prometheus、Grafana 数据。该目录已被 `.gitignore` 忽略。

## 8. 边界

- 这套观测栈用于本地开发和面试演示。
- 当前没有验证 Linux 云服务器部署。
- 当前没有生产级告警规则。
- P95/P99 必须来自本地 Prometheus 查询结果，不能直接写成生产承诺。
