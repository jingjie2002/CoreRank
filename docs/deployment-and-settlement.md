# 本地部署与结算

## 分布式本地栈

```powershell
Copy-Item .env.example .env
docker compose build corerank-rank-service corerank-match-service corerank-gateway corerank-roomserver
docker compose up -d corerank-redis corerank-rank-service corerank-match-service corerank-gateway corerank-roomserver prometheus grafana
powershell -ExecutionPolicy Bypass -File scripts\distributed_smoke.ps1
```

Compose 端口默认绑定 `127.0.0.1`。若设置 `CORERANK_API_KEY`，roomserver 和验证脚本必须使用同一值。

验收点：gateway `/readyz`、两个 gRPC health service、三个 metrics target、按模式匹配、授权 TCP join、原子结算、Prometheus 和 Grafana。

## 结算语义

```http
POST /api/matches/{match_id}/settle
```

该入口只接收整数绝对分数。RankService 会验证 scores 与 MatchResult 玩家集合完全相等，拒绝重复、缺失和 outsider；Redis Lua 原子批量写榜、保存幂等指纹、标记 MatchResult 并释放 room reservation。

同一 payload 可安全重放，不同 payload 会返回冲突。接口不实现战斗帧、技能伤害、ELO 计算或可信战斗服身份；API key 只是本地轻量保护。外部部署需要正式的服务身份和 TLS/mTLS。

MySQL 仍是 Redis 结算成功后的可选旁路写入，不在 Lua 原子事务中，也不是 Redis 恢复源。
