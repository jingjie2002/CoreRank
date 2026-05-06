// Package main 是 CoreRank gRPC 压力测试机器人
//
// 本程序通过 gRPC 协议对 CoreRank 系统进行压力测试，模拟高并发场景：
// - 100 个并发协程（Goroutine）模拟 100 个同时在线玩家
// - 每个协程发送 100 次 RPC 调用，总计 10,000 次请求
// - 实时统计 TPS、成功率、延迟分布
//
// 使用方式：
//
//	# 先启动服务端
//	go run ./cmd/server
//
//	# 运行压力测试
//	go run ./cmd/robot
package main

import (
	"context"
	"fmt"
	"math/rand"
	"os"
	"strconv"
	"sync"
	"sync/atomic"
	"time"

	// gRPC 客户端
	"google.golang.org/grpc"
	"google.golang.org/grpc/credentials/insecure"

	// 导入生成的 proto 代码
	pb "CoreRank/api/proto"
)

// ============================================================================
// 压力测试配置
// ============================================================================

const (
	// serverAddr gRPC 服务器地址
	defaultServerAddr = "localhost:8080"

	// workerCount 并发协程数量
	// 每个协程模拟一个持续发送请求的客户端
	defaultWorkerCount = 100

	// requestsPerWorker 每个协程发送的请求数
	// 总请求数 = workerCount × requestsPerWorker = 10,000
	defaultRequestsPerWorker = 100

	// minScore 随机分数下限
	minScore = 0

	// maxScore 随机分数上限
	maxScore = 5000

	// statsInterval 统计打印间隔
	statsInterval = 1 * time.Second
)

// 统计计数器（使用原子操作保证并发安全）
var (
	successCount int64
	errorCount   int64
	totalLatency int64 // 累计延迟（纳秒）
)

func main() {
	printBanner()
	serverAddr := envOrDefault("ROBOT_GRPC_ADDR", defaultServerAddr)
	workerCount := envIntOrDefault("ROBOT_WORKERS", defaultWorkerCount)
	requestsPerWorker := envIntOrDefault("ROBOT_REQUESTS_PER_WORKER", defaultRequestsPerWorker)

	// =========================================================================
	// 第一步：建立 gRPC 连接
	// =========================================================================
	//
	// grpc.Dial 创建到服务器的连接。
	// 注意：Dial 是懒连接，实际连接在首次 RPC 调用时建立。
	//
	// 【连接选项说明】
	// - WithTransportCredentials(insecure.NewCredentials()): 禁用 TLS（仅开发环境）
	// - WithBlock(): 阻塞直到连接建立（可选，用于启动时验证连接）

	fmt.Printf("[Robot] 正在连接 gRPC 服务器: %s\n", serverAddr)

	conn, err := grpc.NewClient(
		serverAddr,
		grpc.WithTransportCredentials(insecure.NewCredentials()),
	)
	if err != nil {
		fmt.Printf("[Robot] ❌ 无法连接到服务器: %v\n", err)
		os.Exit(1)
	}
	defer conn.Close()

	fmt.Println("[Robot] ✅ gRPC 连接已建立")

	// =========================================================================
	// 第二步：创建 RankService 客户端
	// =========================================================================
	//
	// NewRankServiceClient 是由 protoc-gen-go-grpc 生成的工厂函数，
	// 返回一个类型安全的客户端，可以直接调用 RPC 方法。

	client := pb.NewRankServiceClient(conn)
	fmt.Println("[Robot] ✅ RankServiceClient 已创建")

	// =========================================================================
	// 第三步：启动实时统计打印
	// =========================================================================
	//
	// 在独立的 goroutine 中每秒打印一次统计数据，
	// 让用户实时观察压测进度。

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	go printStats(ctx)

	// =========================================================================
	// 第四步：执行压力测试
	// =========================================================================
	//
	// 使用 sync.WaitGroup 协调多个并发协程

	fmt.Println()
	fmt.Printf("[Robot] 🚀 开始 gRPC 压力测试\n")
	fmt.Printf("[Robot] 📊 测试参数：\n")
	fmt.Printf("        - 服务器地址: %s\n", serverAddr)
	fmt.Printf("        - 并发协程数: %d\n", workerCount)
	fmt.Printf("        - 每协程请求数: %d\n", requestsPerWorker)
	fmt.Printf("        - 总请求数: %d\n", workerCount*requestsPerWorker)
	fmt.Println()

	var wg sync.WaitGroup
	startTime := time.Now()

	wg.Add(workerCount)

	for i := 0; i < workerCount; i++ {
		go func(workerID int) {
			defer wg.Done()

			// 每个协程使用独立的随机数生成器，避免锁竞争
			rng := rand.New(rand.NewSource(time.Now().UnixNano() + int64(workerID)))

			for j := 0; j < requestsPerWorker; j++ {
				// 生成随机玩家 ID 和分数
				playerID := fmt.Sprintf("grpc_robot_%d_%d", workerID, j)
				score := int64(rng.Intn(maxScore-minScore) + minScore)

				// 记录请求开始时间
				reqStart := time.Now()

				// 调用 gRPC UpdateScore 方法
				_, err := client.UpdateScore(context.Background(), &pb.UpdateScoreRequest{
					PlayerId:   playerID,
					NewScore:   score,
					ChangeType: "ABSOLUTE",
				})

				// 记录延迟
				latency := time.Since(reqStart).Nanoseconds()
				atomic.AddInt64(&totalLatency, latency)

				if err != nil {
					atomic.AddInt64(&errorCount, 1)
				} else {
					atomic.AddInt64(&successCount, 1)
				}
			}
		}(i)
	}

	// 等待所有协程完成
	wg.Wait()

	// 停止统计打印
	cancel()

	// 计算最终结果
	elapsed := time.Since(startTime)
	total := successCount + errorCount
	tps := float64(total) / elapsed.Seconds()
	successRate := float64(successCount) / float64(total) * 100
	avgLatency := float64(totalLatency) / float64(total) / 1e6 // 转换为毫秒

	// =========================================================================
	// 第五步：输出最终报告
	// =========================================================================

	fmt.Println()
	fmt.Println("[Robot] ========================================")
	fmt.Println("[Robot] 📈 gRPC 压力测试最终报告")
	fmt.Println("[Robot] ========================================")
	fmt.Printf("[Robot] 总请求数:       %d\n", total)
	fmt.Printf("[Robot] 成功请求数:     %d\n", successCount)
	fmt.Printf("[Robot] 失败请求数:     %d\n", errorCount)
	fmt.Printf("[Robot] 成功率:         %.2f%%\n", successRate)
	fmt.Printf("[Robot] 总耗时:         %v\n", elapsed)
	fmt.Printf("[Robot] TPS:            %.2f req/sec\n", tps)
	fmt.Printf("[Robot] 平均延迟:       %.2f ms\n", avgLatency)
	fmt.Println("[Robot] ========================================")
	fmt.Println()

	evaluatePerformance(tps, successRate, avgLatency)
}

// printStats 实时打印统计数据
func printStats(ctx context.Context) {
	ticker := time.NewTicker(statsInterval)
	defer ticker.Stop()

	var lastSuccess, lastError int64

	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			currentSuccess := atomic.LoadInt64(&successCount)
			currentError := atomic.LoadInt64(&errorCount)

			// 计算本周期的请求数
			deltaSuccess := currentSuccess - lastSuccess
			deltaError := currentError - lastError
			deltaTotal := deltaSuccess + deltaError

			fmt.Printf("[Robot] 📡 实时: %d req/sec (成功: %d, 失败: %d) | 累计: %d\n",
				deltaTotal, deltaSuccess, deltaError, currentSuccess+currentError)

			lastSuccess = currentSuccess
			lastError = currentError
		}
	}
}

// evaluatePerformance 评估测试性能
func evaluatePerformance(tps float64, successRate float64, avgLatency float64) {
	fmt.Println("[Robot] 📋 性能评估：")

	// TPS 评估
	switch {
	case tps >= 5000:
		fmt.Println("[Robot] ✅ TPS 优秀（≥5,000），gRPC 通道性能卓越")
	case tps >= 1000:
		fmt.Println("[Robot] ✅ TPS 良好（≥1,000），满足大多数游戏场景")
	default:
		fmt.Println("[Robot] ⚠️ TPS 一般，建议检查网络或服务端性能")
	}

	// 延迟评估
	switch {
	case avgLatency < 5:
		fmt.Println("[Robot] ✅ 延迟极低（<5ms），玩家体验极佳")
	case avgLatency < 20:
		fmt.Println("[Robot] ✅ 延迟正常（<20ms），满足实时性要求")
	default:
		fmt.Println("[Robot] ⚠️ 延迟较高，建议优化服务端或网络")
	}

	// 成功率评估
	if successRate >= 99.9 {
		fmt.Println("[Robot] ✅ 成功率优秀（≥99.9%），系统稳定性极佳")
	} else if successRate >= 99.0 {
		fmt.Println("[Robot] ⚠️ 成功率一般（≥99.0%），存在少量错误")
	} else {
		fmt.Println("[Robot] ❌ 成功率较低，需检查服务端错误日志")
	}

	fmt.Println()
}

func envOrDefault(name string, fallback string) string {
	if value := os.Getenv(name); value != "" {
		return value
	}
	return fallback
}

func envIntOrDefault(name string, fallback int) int {
	value := os.Getenv(name)
	if value == "" {
		return fallback
	}
	parsed, err := strconv.Atoi(value)
	if err != nil || parsed <= 0 {
		return fallback
	}
	return parsed
}

// printBanner 打印测试程序 Banner
func printBanner() {
	banner := `
   ______                ____              __      ____        __          __ 
  / ____/___  ________  / __ \____ _____  / /__   / __ \____  / /_  ____  / /_
 / /   / __ \/ ___/ _ \/ /_/ / __ '/ __ \/ //_/  / /_/ / __ \/ __ \/ __ \/ __/
/ /___/ /_/ / /  /  __/ _, _/ /_/ / / / / ,<    / _, _/ /_/ / /_/ / /_/ / /_  
\____/\____/_/   \___/_/ |_|\__,_/_/ /_/_/|_|  /_/ |_|\____/_.___/\____/\__/  
                                                                              
  gRPC Stress Testing Robot for CoreRank
  Go 1.25 | Configurable Goroutines | gRPC Calls
`
	fmt.Println(banner)
}
