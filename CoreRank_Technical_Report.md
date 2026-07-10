# CoreRank 技术说明入口

旧版技术报告基于单进程结构，已不再作为当前实现依据。请使用以下文档：

- [README](README.md)：项目定位、能力、快速开始和边界。
- [架构](docs/architecture.md)：gateway、rank-service、match-service、roomserver、Redis 和可选 MySQL 的职责。
- [API](docs/api.md)：当前 HTTP/gRPC/TCP 契约、限制与错误映射。
- [可观测性](docs/observability.md)：三个分布式 metrics target 与 PromQL。
- [验证](docs/verification.md)：测试、构建、smoke/soak 和环境边界。

`cmd/server` 仅为 legacy 单进程兼容入口；当前推荐形态是 Docker Compose 中的四进程应用栈。
