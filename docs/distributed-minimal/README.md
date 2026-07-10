# 最小分布式改造历史记录

本目录保存 CoreRank 从 legacy 单进程形态拆分为 gateway、rank-service、match-service 和 roomserver 的设计基线与执行计划。

这些文件用于解释演进过程，不是当前 API 或部署事实。当前实现请阅读：

- [项目 README](../../README.md)
- [当前架构](../architecture.md)
- [当前 API](../api.md)
- [当前验证指南](../verification.md)

历史文件：

- `01-requirements.md`：改造前需求与范围。
- `02-project-design.md`：实施前架构设计。
- `03-upgrade-plan.md`：当时的升级步骤与风险。
