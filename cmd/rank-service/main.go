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

	"github.com/prometheus/client_golang/prometheus/promhttp"
	"google.golang.org/grpc"
	"google.golang.org/grpc/health"
	"google.golang.org/grpc/health/grpc_health_v1"
	"google.golang.org/grpc/reflection"

	pb "CoreRank/api/proto"
	"CoreRank/internal/handler"
	"CoreRank/internal/repository"
	"CoreRank/internal/rpcutil"
	"CoreRank/internal/service"
	appconfig "CoreRank/pkg/config"
	redisclient "CoreRank/pkg/redis"
)

const (
	appName = "CoreRank RankService"

	defaultRankGRPCAddr    = ":18081"
	defaultRankMetricsAddr = ":19081"
	startupTimeout         = 10 * time.Second
	shutdownTimeout        = 5 * time.Second
)

func main() {
	printBanner()

	grpcAddr := appconfig.String("RANK_GRPC_ADDR", defaultRankGRPCAddr)
	metricsAddr := appconfig.String("RANK_METRICS_ADDR", defaultRankMetricsAddr)

	startupCtx, startupCancel := context.WithTimeout(context.Background(), startupTimeout)
	defer startupCancel()

	fmt.Printf("[%s] initializing Redis client...\n", appName)
	redisConfig := redisclient.DefaultConfig()
	redisConfig.Addr = appconfig.String("REDIS_ADDR", redisConfig.Addr)

	client := redisclient.NewClient(redisConfig)
	defer func() {
		fmt.Printf("[%s] closing Redis connection...\n", appName)
		_ = client.Close()
	}()

	if err := client.Ping(startupCtx); err != nil {
		fmt.Printf("[%s] Redis connection failed: %v\n", appName, err)
		os.Exit(1)
	}
	fmt.Printf("[%s] Redis is ready at %s\n", appName, redisConfig.Addr)

	playerRepo := repository.NewPlayerRepository(client.GetRawClient())
	rankService := service.NewRankService(playerRepo)

	mysqlRequired := appconfig.Bool("CORERANK_MYSQL_REQUIRED", false)
	if mysqlDSN := appconfig.String("CORERANK_MYSQL_DSN", ""); mysqlDSN != "" {
		mysqlRepo, err := repository.NewMySQLRepository(startupCtx, mysqlDSN)
		if err != nil {
			if mysqlRequired {
				fmt.Printf("[%s] MySQL connection failed and CORERANK_MYSQL_REQUIRED is enabled: %v\n", appName, err)
				os.Exit(1)
			}
			fmt.Printf("[%s] MySQL connection failed; continuing in Redis-only mode: %v\n", appName, err)
		} else {
			defer func() {
				fmt.Printf("[%s] closing MySQL connection...\n", appName)
				_ = mysqlRepo.Close()
			}()
			rankService.SetMySQLRepository(mysqlRepo)
			fmt.Printf("[%s] MySQL persistence is enabled\n", appName)
		}
	} else {
		fmt.Printf("[%s] MySQL persistence is disabled; set CORERANK_MYSQL_DSN to enable it\n", appName)
	}

	metricsServer := startMetricsServer(metricsAddr)

	listener, err := net.Listen("tcp", grpcAddr)
	if err != nil {
		fmt.Printf("[%s] failed to listen on %s: %v\n", appName, grpcAddr, err)
		os.Exit(1)
	}

	grpcServer := grpc.NewServer(rpcutil.ServerOptions()...)
	pb.RegisterRankServiceServer(grpcServer, handler.NewRankHandler(rankService))
	healthServer := health.NewServer()
	grpc_health_v1.RegisterHealthServer(grpcServer, healthServer)
	healthServer.SetServingStatus("", grpc_health_v1.HealthCheckResponse_SERVING)
	healthCtx, stopHealth := context.WithCancel(context.Background())
	defer stopHealth()
	go rpcutil.MonitorDependencyHealth(healthCtx, client, healthServer, 2*time.Second)
	reflection.Register(grpcServer)

	go func() {
		fmt.Printf("[%s] gRPC server listening on %s\n", appName, grpcAddr)
		if err := grpcServer.Serve(listener); err != nil {
			fmt.Printf("[%s] gRPC server stopped: %v\n", appName, err)
			os.Exit(1)
		}
	}()

	fmt.Printf("[%s] service is ready\n", appName)
	waitForShutdown(grpcServer, healthServer, metricsServer, stopHealth)
}

func startMetricsServer(addr string) *http.Server {
	mux := http.NewServeMux()
	mux.Handle("/metrics", promhttp.Handler())

	server := &http.Server{
		Addr:              addr,
		Handler:           mux,
		ReadHeaderTimeout: 5 * time.Second,
	}

	go func() {
		fmt.Printf("[%s] metrics listening on http://localhost%s/metrics\n", appName, addr)
		if err := server.ListenAndServe(); err != nil && err != http.ErrServerClosed {
			fmt.Printf("[%s] metrics server failed: %v\n", appName, err)
			os.Exit(1)
		}
	}()

	return server
}

func waitForShutdown(grpcServer *grpc.Server, healthServer *health.Server, metricsServer *http.Server, stopHealth context.CancelFunc) {
	quit := make(chan os.Signal, 1)
	signal.Notify(quit, syscall.SIGINT, syscall.SIGTERM)
	sig := <-quit

	fmt.Printf("[%s] received %v, shutting down...\n", appName, sig)

	shutdownCtx, shutdownCancel := context.WithTimeout(context.Background(), shutdownTimeout)
	defer shutdownCancel()

	if err := metricsServer.Shutdown(shutdownCtx); err != nil {
		fmt.Printf("[%s] metrics server shutdown failed: %v\n", appName, err)
	} else {
		fmt.Printf("[%s] metrics server stopped\n", appName)
	}

	stopHealth()
	healthServer.SetServingStatus("", grpc_health_v1.HealthCheckResponse_NOT_SERVING)
	gracefulStopGRPC(grpcServer, shutdownCtx)
	fmt.Printf("[%s] gRPC server stopped\n", appName)
}

func gracefulStopGRPC(server *grpc.Server, ctx context.Context) {
	done := make(chan struct{})
	go func() {
		server.GracefulStop()
		close(done)
	}()
	select {
	case <-done:
	case <-ctx.Done():
		server.Stop()
	}
}

func printBanner() {
	fmt.Print(`
   ______                ____              __  
  / ____/___  ________  / __ \____ _____  / /__
 / /   / __ \/ ___/ _ \/ /_/ / __ '/ __ \/ //_/
/ /___/ /_/ / /  /  __/ _, _/ /_/ / / / / ,<   
\____/\____/_/   \___/_/ |_|\__,_/_/ /_/_/|_|  

  Rank Service | gRPC | Redis ZSet | Prometheus
`)
}
