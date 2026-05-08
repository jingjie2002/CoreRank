# CoreRank 2026-05-06 验证记录

## 环境

- OS: Windows
- Go: 1.25.0
- Redis: 本机 `127.0.0.1:6379`
- 说明：Go 构建缓存使用项目内 `.gocache`

## 命令

```powershell
$env:GOCACHE=(Join-Path (Get-Location) '.gocache')
go test ./...
go build ./cmd/server
go build ./cmd/robot
python .\scripts\rest_demo.py
```

## 结果

- `go test ./...` 通过。
- `go build ./cmd/server` 通过。
- `go build ./cmd/robot` 通过。
- RESTful API 演示通过：
  - 更新玩家分数。
  - 查询 TopN 排行榜。
  - 查询单个玩家排名。
  - 玩家加入匹配池。
- gRPC Robot 压测通过：
  - 总请求数：10,000。
  - 成功请求数：10,000。
  - 失败请求数：0。
  - 成功率：100%。
  - 总耗时：368.4696ms。
  - TPS：27,139.28 req/sec。
  - 平均延迟：3.51ms。

## 简历使用边界

可以写：

- 使用 Go + gRPC/RESTful + Redis ZSet/Lua 实现匹配池和排行榜服务。
- 提供 Robot 压测与 Prometheus 指标暴露。
- 本机 10,000 次 gRPC 请求验证成功率 100%，平均延迟约 3.5ms。

不建议写：

- 已生产落地。
- 已支持 Redis Cluster。
- 已完成完整游戏服务器。
- 把 2026-05-06 本轮平均延迟说成 P99；后续本机 Prometheus 分位数记录见 `docs/benchmark.md`。
