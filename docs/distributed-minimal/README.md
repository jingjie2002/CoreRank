# CoreRank 最小分布式改造文档

本目录保存 CoreRank 从当前“单进程多模块”升级为“本地最小分布式服务”的实施前文档。

当前文档用于方案确认。确认前不进入业务代码改造。

## 文档列表

- [01-requirements.md](./01-requirements.md)：需求文档，说明目标、范围、角色、用例、验收标准和明确不做项。
- [02-project-design.md](./02-project-design.md)：项目设计文档，说明目标架构、服务职责、数据结构、状态流转、接口边界和配置。
- [03-upgrade-plan.md](./03-upgrade-plan.md)：升级方案，说明施工顺序、影响范围、风险、验证方式和需要用户确认的决策点。

## 当前结论

CoreRank 当前已经有匹配、排行榜、Redis Lua、MySQL、RoomServer、REST、gRPC 和 Prometheus 基础。最小分布式改造不需要重写核心算法，重点是：

1. 把对外接入层拆成 `gateway`。
2. 把排行榜能力拆成 `rank-service`。
3. 把匹配、票据、房间分配和后台 worker 拆成 `match-service`。
4. 保留 `roomserver` 作为独立房间服示例。
5. 继续使用 Redis 作为跨进程共享状态中心。
6. 通过 gRPC 完成内部服务调用。
7. 用 Docker Compose 验证本地多进程链路。

## 当前已验证状态

截至 2026-07-08，当前分支已经完成并验证：

1. `gateway`、`rank-service`、`match-service` 三个独立进程入口。
2. `gateway` 通过 gRPC 调用 `rank-service` 和 `match-service`。
3. Docker Compose 可以构建并启动 Redis、MySQL、三个 Go 服务、Prometheus 和 Grafana。
4. Prometheus 可以抓取 `gateway`、`rank-service`、`match-service` 三个 metrics target。
5. Grafana provisioning 可以加载 `CoreRank Overview` dashboard。

本地验证命令：

```powershell
docker compose build corerank-rank-service corerank-match-service corerank-gateway corerank-roomserver
docker compose up -d corerank-redis corerank-rank-service corerank-match-service corerank-gateway corerank-roomserver prometheus grafana
powershell -ExecutionPolicy Bypass -File scripts\distributed_smoke.ps1
```

验证通过后可以打开：

- Gateway: `http://127.0.0.1:8081`
- RoomServer TCP: `127.0.0.1:7001`
- Prometheus: `http://127.0.0.1:9090`
- Grafana: `http://127.0.0.1:3000`

`scripts/distributed_smoke.ps1` now waits for `compose-room-1`, verifies duplicate/cancel ticket conflict paths, verifies match result `ServerID` / `ServerAddr`, and checks the roomserver TCP `join -> ready -> room_started -> leave` flow.

## 本次明确不做

- 不做 Kubernetes。
- 不做 Redis Cluster。
- 不做 Nacos / Consul / Etcd。
- 不做消息队列。
- 不做完整账号系统、JWT 鉴权、反作弊。
- 不做完整战斗服帧同步。
- 不宣称生产级高并发或大规模在线。
