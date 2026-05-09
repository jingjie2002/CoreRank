# CoreRank 部署与结算补强说明

## 1. 定位

本文档记录 CoreRank V1.5 的两个补强点：

- Linux / Docker 部署验证路线。
- 战斗结束后的最小积分结算入口。

当前仍不声明生产级高可用、Redis Cluster 或 Kubernetes 落地。

## 2. Linux / Docker 验证路线

推荐验证顺序：

```powershell
go test ./...
go vet ./...
go build -o tmp\corerank-server.exe ./cmd/server
python scripts\rest_demo.py
python scripts\room_tcp_demo.py
```

本地 Docker Compose：

```powershell
docker compose up -d corerank-redis corerank-mysql prometheus grafana
go run ./cmd/server
```

Linux 或 Linux 容器环境应验证：

- Redis 可连接。
- CoreRank `/health` 返回 `ok`。
- `/metrics` 可抓取。
- RESTful 排行榜更新成功。
- 匹配票据创建、取消、超时和结果查询成功。
- roomserver 注册后，匹配结果包含 `server_id` 和 `server_addr`。

## 3. 最小结算入口

新增 RESTful 接口：

```http
POST /api/matches/{match_id}/settle
```

请求示例：

```json
{
  "leaderboard_type": "season:ss25",
  "scores": [
    {"player_id": "p1", "score": 1260},
    {"player_id": "p2", "score": 1210}
  ]
}
```

这个接口只做一件事：

```text
根据战斗结算后的绝对分数，更新指定排行榜。
```

它不做：

- 战斗帧同步。
- 技能、伤害、掉落。
- 反作弊。
- 胜负 ELO 计算。
- 完整战斗服状态机。

## 4. 面试讲法

可以这样说：

```text
CoreRank 原本负责匹配和排行榜。V1.5 增加了最小结算入口，让链路从“匹配成功”延伸到“战斗结束后更新赛季榜”。战斗服仍然不是这个项目的范围，CoreRank 只接受可信后端传来的结算后分数。
```
