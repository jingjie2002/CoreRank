# Agent 接入说明

`agent.yaml` 使用相对项目根目录，不依赖开发者机器上的绝对路径。

推荐只读入口：

| 能力 | 地址 |
|---|---|
| 后端就绪 | `GET http://127.0.0.1:8081/readyz` |
| gateway 存活 | `GET http://127.0.0.1:8081/healthz` |
| gateway metrics | `GET http://127.0.0.1:19080/metrics` |
| rank metrics | `GET http://127.0.0.1:19081/metrics` |
| match metrics | `GET http://127.0.0.1:19082/metrics` |
| 能力清单 | `GET /api/agent/capabilities` |

设置 `CORERANK_API_KEY` 后，`/api/*` 请求必须携带 `X-CoreRank-API-Key`。Agent 默认不应执行结算、排行榜写入、Redis 清理、生产部署或任何不可恢复操作；需要写入时必须由使用者明确确认目标环境与数据范围。

验证命令：

```powershell
go test ./...
go vet ./...
powershell -ExecutionPolicy Bypass -File scripts\distributed_smoke.ps1
```

`/api/agent/events` 和 `/api/agent/logs` 只返回可读取的数据源说明，不代表项目实现了事件缓冲或持久日志系统。
