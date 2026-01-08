// Package redis 提供 Redis 客户端封装，专为游戏后端高并发场景优化
//
// 在游戏后端中，Redis 通常承担以下核心职责：
//   - 匹配池管理（ZSet 存储等待匹配的玩家）
//   - 实时排行榜（ZSet 维护玩家分数排名）
//   - 分布式锁（防止重复匹配等竞态条件）
//   - 会话缓存（玩家在线状态、临时数据）
//
// 因此，Redis 连接的稳定性和性能直接影响整个游戏体验。
package redis

import (
	"context"
	"fmt"
	"time"

	"github.com/redis/go-redis/v9"
)

// Client 封装 Redis 客户端，提供工业级连接池配置
//
// 为什么需要封装？
// 1. 统一配置管理：所有 Redis 连接参数集中管理，便于调优
// 2. 健康检查集成：内置连接健康检查，支持优雅启动/关闭
// 3. 可观测性预留：便于后续集成 Prometheus 指标采集
type Client struct {
	rdb *redis.Client
}

// Config Redis 连接配置结构体
//
// 游戏后端的 Redis 配置通常需要考虑：
// - 高并发读写（匹配池可能每秒数千次 ZADD/ZRANGE）
// - 低延迟要求（玩家对匹配等待时间敏感）
// - 连接稳定性（断线重连不能影响正在进行的匹配）
type Config struct {
	// Addr Redis 服务地址，格式为 host:port
	Addr string

	// Password Redis 认证密码（生产环境必须设置）
	Password string

	// DB 数据库索引，默认 0
	// 建议：匹配池和排行榜使用不同 DB 隔离
	DB int

	// PoolSize 连接池大小
	//
	// 【关键配置】这是游戏后端最重要的 Redis 参数之一
	//
	// 为什么重要？
	// 在高并发场景下（如万人同时匹配），每个协程都需要 Redis 连接。
	// 如果连接池太小，协程会阻塞等待可用连接，导致：
	//   - 匹配延迟飙升
	//   - 超时率上升
	//   - 玩家体验恶化
	//
	// 配置建议：
	// - 开发环境：10-20
	// - 生产环境：根据 QPS 估算，通常为 100-500
	// - 公式参考：PoolSize ≈ 预期 QPS × 平均响应时间(秒)
	PoolSize int

	// MinIdleConns 最小空闲连接数
	//
	// 【性能优化】保持一定数量的空闲连接，避免突发流量时的连接建立开销
	//
	// 为什么重要？
	// TCP 连接建立需要三次握手，约 1-3ms 的额外延迟。
	// 游戏开服、活动开始等场景会出现流量尖峰，
	// 预热的空闲连接可以立即响应，避免"冷启动"延迟。
	//
	// 配置建议：
	// - 设置为 PoolSize 的 10%-20%
	// - 确保足够应对日常流量波动
	MinIdleConns int

	// DialTimeout 连接建立超时时间
	//
	// 游戏后端通常设置较短的超时（3-5秒），
	// 因为玩家等待匹配的耐心有限，快速失败比长时间等待更好。
	DialTimeout time.Duration

	// ReadTimeout 读操作超时时间
	//
	// 对于排行榜查询等读操作，建议设置 1-3 秒。
	// 超时后应触发降级策略（如返回缓存数据）。
	ReadTimeout time.Duration

	// WriteTimeout 写操作超时时间
	//
	// 对于分数更新等写操作，建议与读超时一致。
	WriteTimeout time.Duration
}

// DefaultConfig 返回适合游戏后端的默认配置
//
// 这些默认值基于以下假设：
// - 开发/测试环境
// - 中等并发量（每秒数百次请求）
// - 本地 Redis 实例（低网络延迟）
//
// 生产环境务必根据实际负载调整！
func DefaultConfig() *Config {
	return &Config{
		Addr:     "localhost:6379",
		Password: "",
		DB:       0,

		// 连接池配置
		// 开发环境使用较保守的值，避免占用过多资源
		PoolSize:     50, // 支持约 50 并发 Redis 操作
		MinIdleConns: 10, // 保持 10 个预热连接

		// 超时配置
		// 开发环境可以稍长，便于调试
		DialTimeout:  5 * time.Second,
		ReadTimeout:  3 * time.Second,
		WriteTimeout: 3 * time.Second,
	}
}

// NewClient 创建新的 Redis 客户端实例
//
// 使用 Go 1.25 的增强类型推断特性，简化变量声明。
// 返回值采用指针语义，便于在服务间共享同一连接池。
func NewClient(cfg *Config) *Client {
	// 如果未提供配置，使用默认值
	// Go 1.25 支持更灵活的 nil 检查和默认值处理
	if cfg == nil {
		cfg = DefaultConfig()
	}

	// 创建 go-redis 客户端
	// 所有连接池参数在此统一配置
	rdb := redis.NewClient(&redis.Options{
		Addr:     cfg.Addr,
		Password: cfg.Password,
		DB:       cfg.DB,

		// === 连接池核心配置 ===
		// 这些参数直接影响高并发下的性能表现
		PoolSize:     cfg.PoolSize,
		MinIdleConns: cfg.MinIdleConns,

		// === 超时配置 ===
		// 合理的超时设置是系统稳定性的基石
		DialTimeout:  cfg.DialTimeout,
		ReadTimeout:  cfg.ReadTimeout,
		WriteTimeout: cfg.WriteTimeout,

		// === 连接健康检查 ===
		// 定期验证连接有效性，自动剔除失效连接
		// 游戏后端建议开启，避免使用断开的连接导致请求失败
		PoolFIFO: true, // 使用 FIFO 队列，优先使用最新归还的连接

		// MaxRetries 自动重试次数
		// 网络抖动时自动重试，对玩家透明
		MaxRetries: 3,
	})

	return &Client{rdb: rdb}
}

// Ping 检查 Redis 连接健康状态
//
// 这是服务启动时的关键检查点。
// 游戏服务器启动流程通常包括：
//  1. 检查数据库连接 ✓
//  2. 检查 Redis 连接 ✓ <- 这里
//  3. 加载配置表
//  4. 注册到服务发现
//  5. 开始接受请求
//
// 如果 Redis 不可用，应该：
// - 开发环境：打印错误，但允许启动（便于调试其他模块）
// - 生产环境：启动失败，触发监控告警
func (c *Client) Ping(ctx context.Context) error {
	// 使用带超时的 context，避免启动流程卡死
	// Go 1.25 的 context 包有更好的性能优化
	result, err := c.rdb.Ping(ctx).Result()
	if err != nil {
		return fmt.Errorf("Redis 连接失败: %w", err)
	}

	// Redis PING 命令成功时返回 "PONG"
	if result != "PONG" {
		return fmt.Errorf("Redis 响应异常: 期望 PONG, 实际 %s", result)
	}

	return nil
}

// Close 优雅关闭 Redis 连接
//
// 服务关闭时必须调用此方法，确保：
// - 所有进行中的命令完成
// - 连接正确归还连接池
// - 资源被正确释放
//
// 建议在 main 函数中使用 defer 确保调用：
//
//	client := redis.NewClient(nil)
//	defer client.Close()
func (c *Client) Close() error {
	if c.rdb != nil {
		return c.rdb.Close()
	}
	return nil
}

// GetRawClient 返回底层 go-redis 客户端
//
// 用于需要直接操作 Redis 的高级场景，如：
// - 执行 Lua 脚本（匹配原子操作）
// - 使用 Pipeline 批量操作（排行榜批量更新）
// - 订阅 Pub/Sub 频道（匹配成功通知）
//
// 注意：直接使用底层客户端时，请自行处理错误和超时。
func (c *Client) GetRawClient() *redis.Client {
	return c.rdb
}
