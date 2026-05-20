# CoreRank Agent 接入说明

CoreRank 已补齐 Agent-ready 基础能力，供后续 `GameServerProjectAgent` 通过统一规范识别、审查和诊断。

## 接入入口

| 项目 | 路径 |
|---|---|
| 项目声明 | `agent.yaml` |
| 健康检查 | `GET /healthz` |
| 旧健康检查兼容 | `GET /health` |
| 能力声明 | `GET /api/agent/capabilities` |
| 指标 | `GET /metrics`，默认 metrics 端口 `9091` |
| Agent events | `GET /api/agent/events` |
| Agent logs | `GET /api/agent/logs` |
| Smoke test | `python scripts/agent_smoke.py` |

## Agent 可做

- 读取 `agent.yaml`、README 和 docs。
- 调用 `/healthz` 判断 CoreRank 是否在线。
- 调用 `/api/agent/capabilities` 获取项目能力表。
- 在自动审查模式运行 `go test ./...`、`go vet ./...` 和 Agent smoke test。
- 读取排行榜、玩家排名、匹配票据、匹配结果和房间服列表，用于诊断匹配超时、房间资源不足或排行榜异常。

## Agent 不默认做

- 不直接删除 Redis 数据。
- 不在生产环境自动写排行榜分数。
- 不在生产环境自动执行比赛结算。
- 不自动部署或重启生产服务。

这些操作后续即使接入 Agent，也必须进入 `完全访问权限`，并由用户明确确认。

## 本地验证

服务运行后：

```powershell
python scripts\agent_smoke.py
```

只验证本地声明文件，不要求服务已启动：

```powershell
python scripts\agent_smoke.py --offline
```
