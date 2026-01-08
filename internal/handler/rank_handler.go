// Package handler 实现 CoreRank gRPC 服务处理器
//
// 本包是 gRPC 请求的入口层，负责：
// 1. 接收并验证请求参数
// 2. 调用底层 Service 层处理业务逻辑
// 3. 记录 Prometheus 监控指标
// 4. 构造并返回响应
//
// 设计原则：Handler 层应保持轻薄，复杂逻辑下沉到 Service 层。
package handler

import (
	"context"
	"time"

	pb "CoreRank/api/proto"
	"CoreRank/internal/metrics"
	"CoreRank/internal/service"
)

// RankHandler 排行榜 gRPC 服务处理器
//
// 实现 pb.RankServiceServer 接口，处理排行榜相关的 gRPC 请求。
// 内部持有 RankService 引用，将实际业务逻辑委托给 Service 层。
type RankHandler struct {
	// 嵌入 UnimplementedRankServiceServer 实现向前兼容
	// 当 proto 新增 RPC 方法时，已有代码不会因缺少方法而编译失败
	pb.UnimplementedRankServiceServer

	// rankService 排行榜业务服务
	rankService *service.RankService
}

// NewRankHandler 创建 RankHandler 实例
func NewRankHandler(rankService *service.RankService) *RankHandler {
	return &RankHandler{
		rankService: rankService,
	}
}

// UpdateScore 更新玩家分数
//
// 这是排行榜系统最核心的写入接口。
// 在高并发场景下（如游戏结算高峰期），此接口需要承受极高的 QPS。
//
// 【监控埋点】
// 1. 记录请求延迟（Histogram）- 用于计算 P99 延迟
// 2. 记录请求计数（Counter）- 用于计算 QPS 和成功率
//
// 【性能要求】
// - 目标：P99 延迟 < 10ms
// - 实现：使用 Redis ZADD O(log N) 复杂度操作
func (h *RankHandler) UpdateScore(ctx context.Context, req *pb.UpdateScoreRequest) (*pb.UpdateScoreResponse, error) {
	// ========================================================================
	// 第一步：开始计时
	// ========================================================================
	// 使用 time.Now() 记录请求开始时间，用于计算延迟。
	// 在 defer 中计算耗时并记录到 Histogram。
	startTime := time.Now()

	// defer 确保无论成功或失败都会记录延迟
	defer func() {
		// 计算耗时（秒），Prometheus Histogram 使用秒为单位
		latency := time.Since(startTime).Seconds()
		metrics.ObserveLatency("UpdateScore", latency)
	}()

	// ========================================================================
	// 第二步：参数验证
	// ========================================================================
	if req.GetPlayerId() == "" {
		metrics.RecordRequest("UpdateScore", "invalid_argument")
		return &pb.UpdateScoreResponse{
			Success: false,
		}, nil
	}

	// ========================================================================
	// 第三步：调用 Service 层
	// ========================================================================
	err := h.rankService.UpdatePlayerScore(ctx, req.GetPlayerId(), float64(req.GetNewScore()))
	if err != nil {
		metrics.RecordRequest("UpdateScore", "error")
		return &pb.UpdateScoreResponse{
			Success: false,
		}, err
	}

	// ========================================================================
	// 第四步：记录成功指标
	// ========================================================================
	metrics.RecordRequest("UpdateScore", "ok")

	return &pb.UpdateScoreResponse{
		Success: true,
		Player: &pb.Player{
			PlayerId:  req.GetPlayerId(),
			RankScore: req.GetNewScore(),
		},
	}, nil
}

// GetTopRank 获取排行榜
//
// 查询排行榜 Top N 数据，是典型的读多写少场景。
// 可以通过缓存优化减少 Redis 压力。
//
// 【性能要求】
// - 目标：P99 延迟 < 50ms（Top 100 场景）
// - 实现：使用 Redis ZREVRANGE O(log N + M) 复杂度操作
func (h *RankHandler) GetTopRank(ctx context.Context, req *pb.GetTopRankRequest) (*pb.GetTopRankResponse, error) {
	startTime := time.Now()

	defer func() {
		latency := time.Since(startTime).Seconds()
		metrics.ObserveLatency("GetTopRank", latency)
	}()

	// 默认返回 Top 10
	topN := int64(req.GetTopN())
	if topN <= 0 {
		topN = 10
	}

	// 调用 Service 层获取排行榜数据
	players, err := h.rankService.GetTopPlayers(ctx, topN)
	if err != nil {
		metrics.RecordRequest("GetTopRank", "error")
		return nil, err
	}

	// 构造响应
	entries := make([]*pb.RankEntry, 0, len(players))
	for _, p := range players {
		entries = append(entries, &pb.RankEntry{
			Rank: p.Rank,
			Player: &pb.Player{
				PlayerId:  p.PlayerID,
				RankScore: int64(p.Score),
			},
		})
	}

	metrics.RecordRequest("GetTopRank", "ok")

	return &pb.GetTopRankResponse{
		Entries:   entries,
		UpdatedAt: time.Now().UnixMilli(),
	}, nil
}
