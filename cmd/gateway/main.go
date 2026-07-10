package main

import (
	"context"
	"fmt"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/prometheus/client_golang/prometheus/promhttp"
	"google.golang.org/grpc"
	"google.golang.org/grpc/credentials/insecure"
	"google.golang.org/grpc/health/grpc_health_v1"

	pb "github.com/jingjie2002/CoreRank/api/proto"
	"github.com/jingjie2002/CoreRank/internal/handler"
	appconfig "github.com/jingjie2002/CoreRank/pkg/config"
)

const (
	appName = "CoreRank Gateway"

	defaultGatewayHTTPAddr    = ":8081"
	defaultGatewayMetricsAddr = ":19080"
	defaultRankGRPCTarget     = "127.0.0.1:18081"
	defaultMatchGRPCTarget    = "127.0.0.1:18082"
	shutdownTimeout           = 5 * time.Second
	startupTimeout            = 10 * time.Second
)

func main() {
	printBanner()

	httpAddr := appconfig.String("GATEWAY_HTTP_ADDR", defaultGatewayHTTPAddr)
	metricsAddr := appconfig.String("GATEWAY_METRICS_ADDR", defaultGatewayMetricsAddr)
	rankTarget := appconfig.String("RANK_GRPC_TARGET", defaultRankGRPCTarget)
	matchTarget := appconfig.String("MATCH_GRPC_TARGET", defaultMatchGRPCTarget)
	apiKey := appconfig.String("CORERANK_API_KEY", "")

	rankConn, err := newGRPCClient(rankTarget)
	if err != nil {
		fmt.Printf("[%s] failed to configure rank-service client: %v\n", appName, err)
		os.Exit(1)
	}
	defer rankConn.Close()

	matchConn, err := newGRPCClient(matchTarget)
	if err != nil {
		fmt.Printf("[%s] failed to configure match-service client: %v\n", appName, err)
		os.Exit(1)
	}
	defer matchConn.Close()
	readiness := func(ctx context.Context) error {
		if _, err := grpc_health_v1.NewHealthClient(rankConn).Check(ctx, &grpc_health_v1.HealthCheckRequest{}); err != nil {
			return fmt.Errorf("rank-service: %w", err)
		}
		if _, err := grpc_health_v1.NewHealthClient(matchConn).Check(ctx, &grpc_health_v1.HealthCheckRequest{}); err != nil {
			return fmt.Errorf("match-service: %w", err)
		}
		return nil
	}
	startupCtx, cancelStartup := context.WithTimeout(context.Background(), startupTimeout)
	if err := readiness(startupCtx); err != nil {
		cancelStartup()
		fmt.Printf("[%s] backend readiness check failed: %v\n", appName, err)
		os.Exit(1)
	}
	cancelStartup()

	fmt.Printf("[%s] rank-service target: %s\n", appName, rankTarget)
	fmt.Printf("[%s] match-service target: %s\n", appName, matchTarget)

	httpServer := startHTTPServer(
		httpAddr,
		handler.RequireAPIKey(handler.NewGatewayHTTPHandlerWithReadiness(
			pb.NewRankServiceClient(rankConn),
			pb.NewMatchServiceClient(matchConn),
			readiness,
		), apiKey),
	)
	metricsServer := startMetricsServer(metricsAddr)

	fmt.Printf("[%s] service is ready\n", appName)
	waitForShutdown(httpServer, metricsServer)
}

func newGRPCClient(target string) (*grpc.ClientConn, error) {
	return grpc.NewClient(
		target,
		grpc.WithTransportCredentials(insecure.NewCredentials()),
	)
}

func startHTTPServer(addr string, httpHandler http.Handler) *http.Server {
	server := &http.Server{
		Addr:              addr,
		Handler:           httpHandler,
		ReadHeaderTimeout: 5 * time.Second,
		ReadTimeout:       10 * time.Second,
		WriteTimeout:      10 * time.Second,
		IdleTimeout:       60 * time.Second,
		MaxHeaderBytes:    16 * 1024,
	}

	go func() {
		fmt.Printf("[%s] HTTP gateway listening on http://localhost%s\n", appName, addr)
		if err := server.ListenAndServe(); err != nil && err != http.ErrServerClosed {
			fmt.Printf("[%s] HTTP gateway failed: %v\n", appName, err)
			os.Exit(1)
		}
	}()

	return server
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

func waitForShutdown(httpServer *http.Server, metricsServer *http.Server) {
	quit := make(chan os.Signal, 1)
	signal.Notify(quit, syscall.SIGINT, syscall.SIGTERM)
	sig := <-quit

	fmt.Printf("[%s] received %v, shutting down...\n", appName, sig)

	shutdownCtx, shutdownCancel := context.WithTimeout(context.Background(), shutdownTimeout)
	defer shutdownCancel()

	if err := httpServer.Shutdown(shutdownCtx); err != nil {
		fmt.Printf("[%s] HTTP gateway shutdown failed: %v\n", appName, err)
	} else {
		fmt.Printf("[%s] HTTP gateway stopped\n", appName)
	}

	if err := metricsServer.Shutdown(shutdownCtx); err != nil {
		fmt.Printf("[%s] metrics server shutdown failed: %v\n", appName, err)
	} else {
		fmt.Printf("[%s] metrics server stopped\n", appName)
	}
}

func printBanner() {
	fmt.Print(`
   ______                ____              __  
  / ____/___  ________  / __ \____ _____  / /__
 / /   / __ \/ ___/ _ \/ /_/ / __ '/ __ \/ //_/
/ /___/ /_/ / /  /  __/ _, _/ /_/ / / / / ,<   
\____/\____/_/   \___/_/ |_|\__,_/_/ /_/_/|_|  

  Gateway | HTTP API | gRPC Clients | Prometheus
`)
}
