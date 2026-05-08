# CoreRank 面试讲法

## 项目一句话

CoreRank 是一个 Go 游戏服务端中间件项目，聚焦匹配池和实时排行榜：服务层同时暴露 gRPC 和 RESTful API，底层使用 Redis ZSet 保存排行榜和匹配池，并用 Lua 脚本把“查询候选玩家 + 删除候选玩家”合并成原子操作，避免重复匹配。

## 为什么适合游戏服务端岗位

- 游戏服务端常见的两个基础模块是匹配和排行榜。
- 匹配池需要处理并发抢人问题，Redis Lua 能把多步状态变更收敛成单次原子执行。
- 排行榜天然适合 Redis ZSet，`ZADD`、`ZREVRANGE`、`ZREVRANK` 能覆盖更新、TopN 和个人名次查询。
- gRPC 适合服务间调用，RESTful API 适合后台、测试和外部系统接入。
- Prometheus 指标用于观察接口延迟、请求成功率、匹配成功/取消/超时、票据终态耗时和 queued 数量。

## 可以主动讲的技术点

- `Handler -> Service -> Repository` 分层，避免接口层直接写 Redis 细节。
- Redis 连接池配置、启动时 Ping 检查和优雅关闭。
- Lua 脚本解决 `ZRANGEBYSCORE` + `ZREM` 的 check-then-act 竞态。
- 排行榜用 Redis ZSet，分数更新和 TopN 查询复杂度适合高频读写场景。
- 匹配 Worker 按积分桶轮询扫描，连续空匹配后扩大搜索范围，平衡等待时长和公平性。
- RESTful API 是在 gRPC 服务之外补的网关层，两者复用同一套 Service 和 Repository。
- MySQL 作为可选持久化层沉淀玩家分数、匹配票据、匹配结果和榜单快照；Redis 仍承载高频热路径。
- MySQL 故障时默认降级到 Redis 主链路，避免可选持久化层中断核心匹配/排行榜请求。
- Robot 支持本机 gRPC 压测，当前压测记录写在 `docs/benchmark.md`，讲述时必须带上本机环境和参数。
- 本地 Docker 观测栈已经能展示 Prometheus 抓取和 Grafana `CoreRank Overview` dashboard；P95/P99 是本机短窗口观测值，不是生产承诺。

## 不要夸大的边界

- 当前是单 Redis 实例验证，不要说已经做成 Redis Cluster 生产方案。
- 当前已完成 Redis-backed 房间资源分配 v1，但没有真实 TCP/WebSocket 战斗服、房间服进程和客户端长连接，不要说是完整游戏服务器。
- README 里的压测数字需要以本机实际压测结果为准，简历上不要写固定 TPS，除非附上可复现实验条件。
- 当前 Redis 仍是高频热路径，MySQL 是可选持久化证据链；可以说已接入 MySQL 落库，但不要说 MySQL 承载实时匹配池。
- Grafana 只能说明本地观测演示栈已跑通，不要说生产监控和告警体系已经落地。

## 高频追问准备

### 为什么不用 Go map + mutex 做匹配池？

单进程可以，但游戏服务端通常会多实例部署。Go 内存状态无法跨进程共享，Redis 能作为集中状态层；Lua 脚本还能保证多个服务实例同时扫描时不会抢到同一批玩家。

### Lua 原子性解决了什么问题？

如果先查候选玩家再删除，两个 Worker 可能同时查到同一批玩家。Lua 脚本在 Redis 内部一次性执行查询和删除，中间不会被其他命令插入，从而避免重复匹配。

### 排行榜为什么用 ZSet？

排行榜需要按分数排序、取 TopN、查个人名次。ZSet 正好提供 `ZADD` 更新分数、`ZREVRANGE` 取 TopN、`ZREVRANK` 查名次，语义和复杂度都匹配。

### gRPC 和 RESTful 为什么都要有？

gRPC 适合内部微服务调用，协议紧凑、类型明确；RESTful 更适合后台管理、联调、外部系统和简单测试。CoreRank 中两者复用同一套业务层，避免重复实现。
