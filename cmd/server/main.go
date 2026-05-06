// Package main 是 CoreRank 服务的主入口
//
// CoreRank 是一个高并发游戏匹配与排行榜系统，本文件负责：
// - 初始化核心组件（Redis 客户端）
// - 执行启动前健康检查
// - 启动 gRPC 服务器（监听 8080 端口）
// - 启动 Prometheus 指标暴露端点（监听 9091 端口）
//
// 启动流程设计原则：
// 1. 快速失败（Fail-Fast）：关键依赖不可用时立即退出
// 2. 优雅启动：按依赖顺序初始化，确保上游就绪后再接受请求
// 3. 可观测性：暴露 Prometheus 指标，便于监控告警
package main

import (
	"context"
	"fmt"
	"net"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"

	// gRPC 相关
	"google.golang.org/grpc"
	"google.golang.org/grpc/reflection"

	// Prometheus 相关
	"github.com/prometheus/client_golang/prometheus/promhttp"

	// 导入项目内部包
	pb "CoreRank/api/proto"
	"CoreRank/internal/handler"
	"CoreRank/internal/repository"
	"CoreRank/internal/service"
	redisclient "CoreRank/pkg/redis"
)

const (
	// 应用名称，用于日志前缀
	appName = "CoreRank"

	// 默认 gRPC 服务端口
	defaultGRPCAddr = ":8080"

	// 默认 Prometheus 指标暴露端口
	defaultMetricsAddr = ":9091"

	// 默认 RESTful API 端口
	defaultHTTPAddr = ":8081"

	// 启动超时时间
	startupTimeout = 10 * time.Second
)

func main() {
	// 打印启动 Banner
	printBanner()

	grpcAddr := envOrDefault("GRPC_ADDR", defaultGRPCAddr)
	httpAddr := envOrDefault("HTTP_ADDR", defaultHTTPAddr)
	metricsAddr := envOrDefault("METRICS_ADDR", defaultMetricsAddr)

	// 创建带超时的启动 Context
	ctx, cancel := context.WithTimeout(context.Background(), startupTimeout)
	defer cancel()

	// =========================================================================
	// 第一步：初始化 Redis 客户端
	// =========================================================================
	fmt.Printf("[%s] 正在初始化 Redis 客户端...\n", appName)

	redisConfig := redisclient.DefaultConfig()
	if addr := os.Getenv("REDIS_ADDR"); addr != "" {
		redisConfig.Addr = addr
	}

	client := redisclient.NewClient(redisConfig)
	defer func() {
		fmt.Printf("[%s] 正在关闭 Redis 连接...\n", appName)
		client.Close()
	}()

	// Redis 健康检查
	if err := client.Ping(ctx); err != nil {
		fmt.Printf("[%s] ❌ Redis 连接失败: %v\n", appName, err)
		fmt.Printf("[%s] 请确保 Redis 服务正在运行：docker-compose up -d\n", appName)
		os.Exit(1)
	}

	fmt.Println()
	fmt.Printf("[%s] ✅ Engine Synchronized. Ready for Matchmaking.\n", appName)
	fmt.Println()

	// =========================================================================
	// 第二步：初始化业务组件
	// =========================================================================
	fmt.Printf("[%s] 正在初始化业务组件...\n", appName)

	// Repository 层
	playerRepo := repository.NewPlayerRepository(client.GetRawClient())
	fmt.Printf("[%s] ✅ PlayerRepository 初始化完成\n", appName)

	// Service 层
	rankService := service.NewRankService(playerRepo)
	fmt.Printf("[%s] ✅ RankService 初始化完成\n", appName)

	matchService := service.NewMatchService(playerRepo)
	fmt.Printf("[%s] ✅ MatchService 初始化完成\n", appName)

	if mysqlDSN := os.Getenv("CORERANK_MYSQL_DSN"); mysqlDSN != "" {
		mysqlRepo, err := repository.NewMySQLRepository(ctx, mysqlDSN)
		if err != nil {
			fmt.Printf("[%s] ❌ MySQL 连接失败: %v\n", appName, err)
			os.Exit(1)
		}
		defer func() {
			fmt.Printf("[%s] 正在关闭 MySQL 连接...\n", appName)
			_ = mysqlRepo.Close()
		}()
		rankService.SetMySQLRepository(mysqlRepo)
		matchService.SetMySQLRepository(mysqlRepo)
		fmt.Printf("[%s] ✅ MySQL 持久化层已启用\n", appName)
	} else {
		fmt.Printf("[%s] ℹ️ MySQL 持久化层未启用，设置 CORERANK_MYSQL_DSN 后启用\n", appName)
	}

	// Handler 层（gRPC 处理器）
	rankHandler := handler.NewRankHandler(rankService)
	fmt.Printf("[%s] ✅ RankHandler 初始化完成\n", appName)

	matchHandler := handler.NewMatchHandler(matchService)
	fmt.Printf("[%s] ✅ MatchHandler 初始化完成\n", appName)

	// MatchWorker（匹配引擎）
	matchWorker := service.NewMatchWorker(playerRepo)
	matchWorker.SetMatchService(matchService)
	fmt.Printf("[%s] ✅ MatchWorker 初始化完成\n", appName)

	// RESTful API 网关
	httpHandler := handler.NewHTTPHandler(rankService, playerRepo, matchService)
	fmt.Printf("[%s] ✅ RESTful API Handler 初始化完成\n", appName)

	// =========================================================================
	// 第三步：启动 Prometheus 指标暴露端点
	// =========================================================================
	//
	// Prometheus 通过 HTTP 端点抓取指标数据。
	// 我们使用独立端口（9091）暴露 /metrics 端点，
	// 与业务端口（8080）分离，便于安全管控。
	//
	// 【为什么使用独立端口？】
	// 1. 安全隔离：监控端点通常只对内部网络开放
	// 2. 限流独立：避免监控抓取影响业务流量
	// 3. 灵活部署：可独立配置负载均衡策略

	go func() {
		http.Handle("/metrics", promhttp.Handler())
		fmt.Printf("[%s] 📊 Prometheus 指标暴露在 http://localhost%s/metrics\n", appName, metricsAddr)
		if err := http.ListenAndServe(metricsAddr, nil); err != nil {
			fmt.Printf("[%s] ⚠️ Prometheus HTTP 服务启动失败: %v\n", appName, err)
		}
	}()

	go func() {
		server := &http.Server{
			Addr:              httpAddr,
			Handler:           httpHandler,
			ReadHeaderTimeout: 5 * time.Second,
		}
		fmt.Printf("[%s] 🌐 RESTful API 暴露在 http://localhost%s\n", appName, httpAddr)
		if err := server.ListenAndServe(); err != nil && err != http.ErrServerClosed {
			fmt.Printf("[%s] ⚠️ RESTful API 服务启动失败: %v\n", appName, err)
		}
	}()

	// =========================================================================
	// 第四步：启动 gRPC 服务器
	// =========================================================================
	//
	// gRPC 服务器启动流程：
	// 1. 创建 TCP 监听器
	// 2. 创建 gRPC Server 实例
	// 3. 注册服务实现（RankHandler）
	// 4. 启用 Reflection（便于调试）
	// 5. 启动服务循环

	// 创建 TCP 监听器
	listener, err := net.Listen("tcp", grpcAddr)
	if err != nil {
		fmt.Printf("[%s] ❌ 无法监听端口 %s: %v\n", appName, grpcAddr, err)
		os.Exit(1)
	}
	fmt.Printf("[%s] 🔌 TCP 监听器已创建：%s\n", appName, grpcAddr)

	// 创建 gRPC Server
	//
	// grpc.NewServer() 创建一个新的 gRPC 服务器实例。
	// 可以传入 ServerOption 配置拦截器、TLS 等高级功能。
	grpcServer := grpc.NewServer()

	// 注册 RankService 到 gRPC 服务器
	//
	// RegisterRankServiceServer 是由 protoc-gen-go-grpc 生成的函数，
	// 它将我们的 Handler 实现绑定到 gRPC 服务器上。
	pb.RegisterRankServiceServer(grpcServer, rankHandler)
	fmt.Printf("[%s] ✅ RankService 已注册到 gRPC 服务器\n", appName)

	pb.RegisterMatchServiceServer(grpcServer, matchHandler)
	fmt.Printf("[%s] ✅ MatchService 已注册到 gRPC 服务器\n", appName)

	// 启用 gRPC Reflection
	//
	// Reflection 允许客户端在运行时发现服务端支持的 RPC 方法。
	// 非常适合调试工具（如 grpcurl、BloomRPC）使用。
	// 生产环境可根据安全需求禁用。
	reflection.Register(grpcServer)
	fmt.Printf("[%s] 🔍 gRPC Reflection 已启用\n", appName)

	// =========================================================================
	// 第五步：启动匹配引擎
	// =========================================================================
	runCtx, runCancel := context.WithCancel(context.Background())
	defer runCancel()

	matchWorker.Start(runCtx)
	fmt.Printf("[%s] ✅ 匹配引擎已启动\n", appName)

	// =========================================================================
	// 第六步：启动 gRPC 服务循环
	// =========================================================================
	//
	// grpcServer.Serve() 会阻塞当前 goroutine，持续处理传入的 RPC 请求。
	// 我们在独立的 goroutine 中运行，以便主 goroutine 可以监听关闭信号。

	go func() {
		fmt.Printf("[%s] 🚀 gRPC 服务器已启动，监听 %s\n", appName, grpcAddr)
		if err := grpcServer.Serve(listener); err != nil {
			fmt.Printf("[%s] ❌ gRPC 服务器错误: %v\n", appName, err)
		}
	}()

	// =========================================================================
	// 第七步：优雅关闭
	// =========================================================================
	//
	// 监听系统信号（SIGINT, SIGTERM），实现优雅关闭。
	// 这对游戏服务器特别重要：
	// 1. 完成正在进行的匹配，避免玩家卡在匹配中
	// 2. 确保数据正确写入 Redis
	// 3. 发送下线通知给服务发现

	fmt.Println()
	fmt.Printf("[%s] ✅ 服务已完全启动！\n", appName)
	fmt.Printf("[%s] 📡 gRPC 端口: %s\n", appName, grpcAddr)
	fmt.Printf("[%s] 🌐 RESTful API 端口: %s\n", appName, httpAddr)
	fmt.Printf("[%s] 📊 Prometheus 端口: %s\n", appName, metricsAddr)
	fmt.Printf("[%s] 💡 使用 grpcurl 测试: grpcurl -plaintext localhost%s list\n", appName, grpcAddr)
	fmt.Println()

	// 等待关闭信号
	quit := make(chan os.Signal, 1)
	signal.Notify(quit, syscall.SIGINT, syscall.SIGTERM)
	sig := <-quit

	fmt.Println()
	fmt.Printf("[%s] 收到信号 %v，正在优雅关闭...\n", appName, sig)

	// 优雅停止 gRPC 服务器
	// GracefulStop 会等待所有正在处理的 RPC 完成后再关闭
	grpcServer.GracefulStop()
	fmt.Printf("[%s] ✅ gRPC 服务器已关闭\n", appName)

	// 取消匹配引擎
	runCancel()
	fmt.Printf("[%s] ✅ 匹配引擎已关闭\n", appName)

	fmt.Printf("[%s] 👋 服务已完全关闭，再见！\n", appName)
}

func envOrDefault(name, fallback string) string {
	if value := os.Getenv(name); value != "" {
		return value
	}
	return fallback
}

// printBanner 打印应用启动 Banner
func printBanner() {
	banner := `
   ______                ____              __  
  / ____/___  ________  / __ \____ _____  / /__
 / /   / __ \/ ___/ _ \/ /_/ / __ '/ __ \/ //_/
/ /___/ /_/ / /  /  __/ _, _/ /_/ / / / / ,<   
\____/\____/_/   \___/_/ |_|\__,_/_/ /_/_/|_|  
                                                
  High-Performance Game Matchmaking & Leaderboard System
  Go 1.25 | Redis 7.2 | gRPC | Prometheus
`
	fmt.Println(banner)
}
