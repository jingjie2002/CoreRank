// Package metrics 定义 CoreRank 系统的 Prometheus 监控指标
//
// Prometheus 是云原生时代最流行的监控系统，CoreRank 通过暴露关键业务指标，
// 实现了可观测性的核心三大支柱之一：Metrics（指标）。
//
// 监控指标设计原则：
// 1. RED 方法：Rate（速率）、Errors（错误）、Duration（延迟）
// 2. 使用标签（Label）区分不同维度
// 3. 避免高基数标签（如 player_id）导致指标爆炸
package metrics

import (
	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/promauto"
)

// ============================================================================
// 匹配相关指标
// ============================================================================

// MatchTotalCounter 累计匹配次数计数器
//
// 【Counter 类型】
// Counter 是只增不减的计数器，适合统计累计事件数。
// 使用场景：匹配成功次数、请求总数、错误次数等。
//
// 【标签设计】
// - bucket: 积分桶名称（如 "bronze", "silver"）
// - status: 匹配结果（"success", "timeout", "cancelled"）
//
// 【查询示例】
// 过去 5 分钟的匹配成功率：
// rate(corerank_match_total{status="success"}[5m]) / rate(corerank_match_total[5m])
var MatchTotalCounter = promauto.NewCounterVec(
	prometheus.CounterOpts{
		Namespace: "corerank",
		Subsystem: "matcher",
		Name:      "match_total",
		Help:      "累计匹配次数，按积分桶和结果状态分类",
	},
	[]string{"bucket", "status"},
)

// MatchTicketEventCounter 记录匹配票据生命周期事件。
//
// 标签保持低基数：
// - match_mode: 匹配模式，例如 default、duel
// - status: queued、matched、cancelled、timeout
var MatchTicketEventCounter = promauto.NewCounterVec(
	prometheus.CounterOpts{
		Namespace: "corerank",
		Subsystem: "matcher",
		Name:      "ticket_events_total",
		Help:      "匹配票据生命周期事件总数，按匹配模式和状态分类",
	},
	[]string{"match_mode", "status"},
)

// MatchLifecycleDurationHistogram 记录匹配票据从创建到终态的耗时。
var MatchLifecycleDurationHistogram = promauto.NewHistogramVec(
	prometheus.HistogramOpts{
		Namespace: "corerank",
		Subsystem: "matcher",
		Name:      "lifecycle_duration_seconds",
		Help:      "匹配票据从创建到 matched/cancelled/timeout 的耗时分布（秒）",
		Buckets: []float64{
			0.05,
			0.1,
			0.25,
			0.5,
			1,
			2.5,
			5,
			10,
			30,
			60,
		},
	},
	[]string{"match_mode", "status"},
)

// QueuedTicketsGauge 记录当前仍在匹配票据池中的排队票据数量。
var QueuedTicketsGauge = promauto.NewGaugeVec(
	prometheus.GaugeOpts{
		Namespace: "corerank",
		Subsystem: "matcher",
		Name:      "queued_tickets",
		Help:      "当前匹配票据池中的 queued 票据数量",
	},
	[]string{"match_mode"},
)

// ============================================================================
// 排行榜服务指标
// ============================================================================

// RequestTotalCounter gRPC 请求总数计数器
//
// 按方法名和状态码分类统计所有 gRPC 请求。
var RequestTotalCounter = promauto.NewCounterVec(
	prometheus.CounterOpts{
		Namespace: "corerank",
		Subsystem: "grpc",
		Name:      "requests_total",
		Help:      "gRPC 请求总数，按方法和状态分类",
	},
	[]string{"method", "status"},
)

// RequestLatencyHistogram 接口响应延迟直方图
//
// 【Histogram 类型】
// Histogram 用于统计数据分布，自动计算分位数（P50, P90, P99）。
// 非常适合监控接口延迟，因为平均值无法反映长尾延迟问题。
//
// 【Bucket 设计】
// 延迟分桶从 0.5ms 到 1s 不等，覆盖游戏业务的典型延迟范围。
// - 游戏匹配：期望 P99 < 100ms
// - 排行榜查询：期望 P99 < 50ms
//
// 【查询示例】
// 过去 5 分钟 UpdateScore 接口的 P99 延迟：
// histogram_quantile(0.99, rate(corerank_grpc_request_latency_seconds_bucket{method="UpdateScore"}[5m]))
var RequestLatencyHistogram = promauto.NewHistogramVec(
	prometheus.HistogramOpts{
		Namespace: "corerank",
		Subsystem: "grpc",
		Name:      "request_latency_seconds",
		Help:      "gRPC 接口响应延迟分布（秒）",
		// Buckets 定义延迟分桶边界
		// 从 0.0005s (0.5ms) 到 1s，覆盖高频交易到复杂查询的全部场景
		Buckets: []float64{
			0.0005, // 0.5ms - 极快响应
			0.001,  // 1ms
			0.005,  // 5ms - 缓存命中
			0.01,   // 10ms - 正常 Redis 操作
			0.025,  // 25ms
			0.05,   // 50ms - 排行榜 Top100 查询
			0.1,    // 100ms - 匹配完成
			0.25,   // 250ms - 复杂聚合
			0.5,    // 500ms
			1.0,    // 1s - 超时边界
		},
	},
	[]string{"method"},
)

// ============================================================================
// 系统级指标
// ============================================================================

// ActivePlayersGauge 当前活跃玩家数量
//
// 【Gauge 类型】
// Gauge 可增可减，适合表示当前状态值。
// 使用场景：在线人数、队列长度、内存使用量等。
var ActivePlayersGauge = promauto.NewGaugeVec(
	prometheus.GaugeOpts{
		Namespace: "corerank",
		Subsystem: "pool",
		Name:      "active_players",
		Help:      "匹配池中的活跃玩家数量",
	},
	[]string{"bucket"},
)

// ============================================================================
// 辅助函数
// ============================================================================

// RecordMatchSuccess 记录匹配成功
func RecordMatchSuccess(bucket string) {
	MatchTotalCounter.WithLabelValues(bucket, "success").Inc()
}

// RecordMatchTimeout 记录匹配超时
func RecordMatchTimeout(bucket string) {
	MatchTotalCounter.WithLabelValues(bucket, "timeout").Inc()
}

// RecordMatchCancelled 记录匹配取消
func RecordMatchCancelled(bucket string) {
	MatchTotalCounter.WithLabelValues(bucket, "cancelled").Inc()
}

// RecordMatchTicketEvents 记录匹配票据生命周期事件
func RecordMatchTicketEvents(matchMode string, status string, count int) {
	if count <= 0 {
		return
	}
	MatchTicketEventCounter.WithLabelValues(normalizeLabel(matchMode), normalizeLabel(status)).Add(float64(count))
}

// ObserveMatchLifecycle 记录匹配票据从创建到终态的耗时
func ObserveMatchLifecycle(matchMode string, status string, createdAtMS int64, finishedAtMS int64) {
	if createdAtMS <= 0 || finishedAtMS < createdAtMS {
		return
	}
	seconds := float64(finishedAtMS-createdAtMS) / 1000
	MatchLifecycleDurationHistogram.WithLabelValues(normalizeLabel(matchMode), normalizeLabel(status)).Observe(seconds)
}

// SetQueuedTickets 记录当前排队票据数量
func SetQueuedTickets(matchMode string, count int64) {
	if count < 0 {
		count = 0
	}
	QueuedTicketsGauge.WithLabelValues(normalizeLabel(matchMode)).Set(float64(count))
}

// RecordRequest 记录 gRPC 请求
func RecordRequest(method, status string) {
	RequestTotalCounter.WithLabelValues(method, status).Inc()
}

// ObserveLatency 记录接口延迟
func ObserveLatency(method string, seconds float64) {
	RequestLatencyHistogram.WithLabelValues(method).Observe(seconds)
}

func normalizeLabel(value string) string {
	if value == "" {
		return "unknown"
	}
	return value
}
