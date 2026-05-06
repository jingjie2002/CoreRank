// Package service 提供 CoreRank 核心业务逻辑实现
//
// 本包是系统的业务核心层，负责：
// - 匹配逻辑（Matchmaking）：玩家配对算法
// - 排行榜逻辑（Leaderboard）：分数更新与排名查询
// - 状态管理（State）：玩家在线/匹配状态
//
// 架构说明：
// 本包位于 internal 目录下，表示这是内部实现，不对外暴露。
// 对外的 API 层（gRPC Handler）在 /api 目录，负责协议转换和参数校验。
package service

// 注意：RankService 的实现位于 rank_service.go。
// 匹配生命周期实现位于 match_service.go。
