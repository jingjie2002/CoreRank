package service

import (
	"context"
	"fmt"
	"sync/atomic"
	"time"

	"CoreRank/internal/repository"
)

// ========================================================================
// 滑动窗口匹配策略
// ========================================================================
//
// 为什么需要滑动窗口？
//
// 在竞技游戏中，匹配系统需要平衡两个核心指标：
//
// 1. 【等待时长】玩家希望尽快开始游戏，等待时间越短，用户体验越好。
//
// 2. 【竞技公平性】玩家希望与实力相近的对手匹配，分数差距过大会导致
//    游戏体验下降（强者虐菜、弱者被虐）。
//
// 这两个指标往往是矛盾的：
// - 分数范围越小 → 匹配越公平 → 但等待越久
// - 分数范围越大 → 匹配越快速 → 但公平性下降
//
// 滑动窗口策略的核心思想：
// - 初始阶段：使用较小的分数窗口，优先保证公平性
// - 等待过久：逐步扩大窗口，牺牲部分公平性换取匹配速度
// - 分桶扫描：将整个分数范围切分为多个桶，轮询扫描，避免饥饿
//
// ========================================================================

// ScoreBucket 积分桶定义
//
// 将整个分数范围切分为多个桶，每个桶独立管理：
// - 当前搜索范围（可动态扩大）
// - 连续空匹配次数（用于触发范围扩大）
type ScoreBucket struct {
	// Name 桶名称，用于日志
	Name string

	// BaseMinScore 基础最小分数（不会改变）
	BaseMinScore int64

	// BaseMaxScore 基础最大分数（不会改变）
	BaseMaxScore int64

	// CurrentMinScore 当前搜索最小分数（可动态调整）
	CurrentMinScore int64

	// CurrentMaxScore 当前搜索最大分数（可动态调整）
	CurrentMaxScore int64

	// EmptyMatchCount 连续空匹配次数
	// 用于触发"扩大搜索范围"策略
	EmptyMatchCount int

	// ExpandThreshold 触发范围扩大的阈值
	// 连续 N 次匹配不到人时，扩大搜索范围
	ExpandThreshold int

	// ExpandStep 每次扩大的步长
	// 向上下各扩展多少分
	ExpandStep int64
}

// MatchWorker 匹配工作器
//
// 采用滑动窗口策略，平衡玩家的"等待时长"和"竞技公平性"。
// 将整个分数范围切分为多个积分桶，轮询扫描每个桶。
type MatchWorker struct {
	// playerRepo 玩家数据仓库
	playerRepo *repository.PlayerRepository

	// matchService 用于把匹配 Worker 摘取到的玩家写成可查询的匹配结果
	matchService *MatchService

	// matchInterval 匹配扫描间隔
	matchInterval time.Duration

	// playersPerMatch 每次匹配的玩家数量
	playersPerMatch int

	// buckets 积分桶列表
	// 采用分桶策略，避免高分段或低分段玩家饥饿
	buckets []*ScoreBucket

	// matchedTotal 累计匹配对数
	//
	// 【并发性能优化】
	// 在高并发场景下（如数万 QPS），使用 sync.Mutex 加锁会带来严重的锁竞争和
	// 上下文切换开销。使用 sync/atomic 基于 CPU 底层指令（CAS/XADD）实现，
	// 性能远优于互斥锁，是高性能计数器的首选方案。
	matchedTotal atomic.Int64
}

const timeoutSweepLimit = 100

// NewMatchWorker 创建匹配工作器实例
//
// 初始化多个积分桶，覆盖常见的分数范围：
// - 青铜段位：0-1000
// - 白银段位：1001-2000
// - 黄金段位：2001-3000
// - 铂金段位：3001-4000
// - 钻石段位：4001-5000
func NewMatchWorker(playerRepo *repository.PlayerRepository) *MatchWorker {
	// 初始化积分桶
	// 每个桶负责一个分数段的匹配
	buckets := []*ScoreBucket{
		{
			Name:            "青铜 (0-1000)",
			BaseMinScore:    0,
			BaseMaxScore:    1000,
			CurrentMinScore: 0,
			CurrentMaxScore: 1000,
			EmptyMatchCount: 0,
			ExpandThreshold: 3, // 连续3次空匹配则扩大范围
			ExpandStep:      200,
		},
		{
			Name:            "白银 (1001-2000)",
			BaseMinScore:    1001,
			BaseMaxScore:    2000,
			CurrentMinScore: 1001,
			CurrentMaxScore: 2000,
			EmptyMatchCount: 0,
			ExpandThreshold: 3,
			ExpandStep:      200,
		},
		{
			Name:            "黄金 (2001-3000)",
			BaseMinScore:    2001,
			BaseMaxScore:    3000,
			CurrentMinScore: 2001,
			CurrentMaxScore: 3000,
			EmptyMatchCount: 0,
			ExpandThreshold: 3,
			ExpandStep:      200,
		},
		{
			Name:            "铂金 (3001-4000)",
			BaseMinScore:    3001,
			BaseMaxScore:    4000,
			CurrentMinScore: 3001,
			CurrentMaxScore: 4000,
			EmptyMatchCount: 0,
			ExpandThreshold: 3,
			ExpandStep:      200,
		},
		{
			Name:            "钻石 (4001-5000)",
			BaseMinScore:    4001,
			BaseMaxScore:    5000,
			CurrentMinScore: 4001,
			CurrentMaxScore: 5000,
			EmptyMatchCount: 0,
			ExpandThreshold: 3,
			ExpandStep:      200,
		},
	}

	return &MatchWorker{
		playerRepo:      playerRepo,
		matchInterval:   100 * time.Millisecond,
		playersPerMatch: 2,
		buckets:         buckets,
	}
}

func (w *MatchWorker) SetMatchService(matchService *MatchService) {
	w.matchService = matchService
}

// Start 启动匹配工作器
//
// 核心设计：
// - 使用 select 语句同时监听 ticker.C 和 ctx.Done()
// - 每次 tick 轮询所有积分桶，执行匹配扫描
// - 支持通过 context 优雅退出
func (w *MatchWorker) Start(ctx context.Context) {
	ticker := time.NewTicker(w.matchInterval)
	// 性能统计定时器，每 5 秒触发一次
	statsTicker := time.NewTicker(5 * time.Second)

	go func() {
		defer ticker.Stop()
		defer statsTicker.Stop()

		// 记录上一次统计时的状态，用于计算瞬时 TPS
		var lastMatchedTotal int64
		lastStatsTime := time.Now()

		fmt.Println("[Matcher] 匹配工作器已启动（滑动窗口模式）")
		fmt.Printf("[Matcher] 积分桶数量: %d，扫描间隔: %v\n", len(w.buckets), w.matchInterval)

		for {
			select {
			case <-ctx.Done():
				// ================================================================
				// 优雅退出
				// ================================================================
				// ctx.Done() 触发条件：
				// - 调用 cancel() 函数
				// - context 超时
				// - 父 context 被取消
				//
				// 收到信号后立即退出，不再处理新的匹配请求。
				// 注意：正在进行中的 Redis 操作会继续完成。
				fmt.Println("[Matcher] 收到关闭信号，匹配工作器正在退出...")
				return

			case <-ticker.C:
				// ================================================================
				// 定时扫描所有积分桶
				// ================================================================
				// 采用轮询方式扫描每个桶，确保所有分段的玩家都有机会被匹配。
				// 这避免了"饥饿"问题：高分段玩家少，但仍能得到及时匹配。
				w.sweepExpiredTickets(ctx)
				w.scanAllBuckets(ctx)

			case <-statsTicker.C:
				// ================================================================
				// 实时性能报告
				// ================================================================
				currentTotal := w.matchedTotal.Load()
				now := time.Now()

				// 计算时间间隔（秒）和新增匹配数
				duration := now.Sub(lastStatsTime).Seconds()
				delta := currentTotal - lastMatchedTotal

				// 计算 TPS (Transactions Per Second)
				var tps float64
				if duration > 0 {
					tps = float64(delta) / duration
				}

				fmt.Printf("[Stats] 📊 当前引擎运行中... 累计完成匹配对数: %d，当前处理频率约为: %.2f matches/sec\n",
					currentTotal, tps)

				// 更新状态
				lastMatchedTotal = currentTotal
				lastStatsTime = now
			}
		}
	}()
}

func (w *MatchWorker) sweepExpiredTickets(ctx context.Context) {
	if w.matchService == nil {
		return
	}
	tickets, err := w.matchService.TimeoutExpiredTickets(ctx, time.Now(), timeoutSweepLimit)
	if err != nil {
		fmt.Printf("[Matcher] ⚠️ 匹配票据超时扫描失败: %v\n", err)
		return
	}
	if len(tickets) > 0 {
		fmt.Printf("[Matcher] ⏱️ 已标记 %d 个超时匹配票据\n", len(tickets))
	}
}

// scanAllBuckets 扫描所有积分桶
//
// 轮询每个桶，执行匹配操作。
// 这种设计确保了公平性：每个分段都有相同的扫描机会。
func (w *MatchWorker) scanAllBuckets(ctx context.Context) {
	for _, bucket := range w.buckets {
		w.matchInBucket(ctx, bucket)
	}
}

// matchInBucket 在指定积分桶中执行匹配
//
// 核心逻辑：
// 1. 使用当前搜索范围查询玩家
// 2. 如果匹配成功，重置空匹配计数
// 3. 如果连续空匹配达到阈值，扩大搜索范围
//
// 这实现了"滑动窗口"的核心思想：
// - 平衡"等待时长"和"竞技公平性"
// - 初始范围小保证公平，等待过久则放宽限制
func (w *MatchWorker) matchInBucket(ctx context.Context, bucket *ScoreBucket) {
	// 调用 Repository 层，原子化查询并提取玩家
	players, err := w.playerRepo.SearchAndPickPlayers(
		ctx,
		bucket.CurrentMinScore,
		bucket.CurrentMaxScore,
		w.playersPerMatch,
	)

	if err != nil {
		fmt.Printf("[Matcher] [%s] 匹配扫描出错: %v\n", bucket.Name, err)
		return
	}

	// 检查是否匹配到足够的玩家
	if len(players) < w.playersPerMatch {
		// ================================================================
		// 空匹配处理：触发"扩大搜索范围"策略
		// ================================================================
		// 这是滑动窗口的核心机制：
		// - 连续多次匹配不到人，说明当前分段玩家稀少
		// - 此时应当扩大搜索范围，牺牲部分公平性换取匹配速度
		// - 这对玩家体验的权衡：宁可匹配到稍强/弱的对手，也不要无限等待

		bucket.EmptyMatchCount++

		if bucket.EmptyMatchCount >= bucket.ExpandThreshold {
			// 扩大搜索范围
			w.expandBucketRange(bucket)
		}
		return
	}

	// ================================================================
	// 匹配成功！
	// ================================================================
	// 1. 重置空匹配计数
	// 2. 重置搜索范围（恢复公平性优先）
	// 3. 打印成功日志

	bucket.EmptyMatchCount = 0

	// 恢复到基础范围，下次匹配优先保证公平性
	bucket.CurrentMinScore = bucket.BaseMinScore
	bucket.CurrentMaxScore = bucket.BaseMaxScore

	// 原子递增总匹配数
	// Add(1) 等价于 atomic.AddInt64(&val, 1)
	w.matchedTotal.Add(1)

	if w.matchService != nil {
		result, err := w.matchService.CompletePickedPlayers(ctx, players, defaultMatchMode)
		if err != nil {
			fmt.Printf("[Matcher] ⚠️ 匹配结果写入失败: %v\n", err)
			return
		}
		if result != nil {
			fmt.Printf("[Matcher] 🧾 匹配结果已生成: match_id=%s room_id=%s\n", result.MatchID, result.RoomID)
		}
	}

	fmt.Printf("[Matcher] ✅ [%s] 匹配成功！房间成员: %v\n", bucket.Name, players)
}

// expandBucketRange 扩大积分桶的搜索范围
//
// 【平衡策略】
// 当玩家等待时间过长时，适当放宽匹配条件，
// 在"竞技公平性"和"等待时长"之间取得平衡。
//
// 实现细节：
// - 向下扩展：CurrentMinScore -= ExpandStep
// - 向上扩展：CurrentMaxScore += ExpandStep
// - 边界保护：不能小于 0，不能超过最大分数
func (w *MatchWorker) expandBucketRange(bucket *ScoreBucket) {
	// 计算新的范围
	newMin := bucket.CurrentMinScore - bucket.ExpandStep
	newMax := bucket.CurrentMaxScore + bucket.ExpandStep

	// 边界保护
	if newMin < 0 {
		newMin = 0
	}
	// 假设最大分数为 10000
	if newMax > 10000 {
		newMax = 10000
	}

	// 打印日志，模拟"扩大搜索范围"动作
	fmt.Printf("[Matcher] ⚠️ [%s] 连续 %d 次空匹配，扩大搜索范围: [%d, %d] → [%d, %d]\n",
		bucket.Name,
		bucket.EmptyMatchCount,
		bucket.CurrentMinScore, bucket.CurrentMaxScore,
		newMin, newMax,
	)

	// 更新范围
	bucket.CurrentMinScore = newMin
	bucket.CurrentMaxScore = newMax

	// 重置计数，等待下一轮触发
	bucket.EmptyMatchCount = 0
}
