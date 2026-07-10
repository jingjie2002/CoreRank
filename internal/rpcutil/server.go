package rpcutil

import (
	"context"
	"time"

	"google.golang.org/grpc"
	"google.golang.org/grpc/health"
	"google.golang.org/grpc/health/grpc_health_v1"
)

const (
	MaxRequestBytes       = 64 * 1024
	MaxConcurrentStreams  = 256
	DefaultRequestTimeout = 3 * time.Second
)

func TimeoutUnaryServerInterceptor(timeout time.Duration) grpc.UnaryServerInterceptor {
	if timeout <= 0 {
		timeout = DefaultRequestTimeout
	}
	return func(ctx context.Context, req any, info *grpc.UnaryServerInfo, handler grpc.UnaryHandler) (any, error) {
		if _, hasDeadline := ctx.Deadline(); hasDeadline {
			return handler(ctx, req)
		}
		deadlineCtx, cancel := context.WithTimeout(ctx, timeout)
		defer cancel()
		return handler(deadlineCtx, req)
	}
}

func ServerOptions() []grpc.ServerOption {
	return []grpc.ServerOption{
		grpc.MaxRecvMsgSize(MaxRequestBytes),
		grpc.MaxConcurrentStreams(MaxConcurrentStreams),
		grpc.ChainUnaryInterceptor(TimeoutUnaryServerInterceptor(DefaultRequestTimeout)),
	}
}

type HealthPinger interface {
	Ping(context.Context) error
}

func MonitorDependencyHealth(ctx context.Context, pinger HealthPinger, server *health.Server, interval time.Duration) {
	if pinger == nil || server == nil {
		return
	}
	if interval <= 0 {
		interval = 2 * time.Second
	}
	ticker := time.NewTicker(interval)
	defer ticker.Stop()
	check := func() {
		pingCtx, cancel := context.WithTimeout(ctx, time.Second)
		err := pinger.Ping(pingCtx)
		cancel()
		status := grpc_health_v1.HealthCheckResponse_SERVING
		if err != nil {
			status = grpc_health_v1.HealthCheckResponse_NOT_SERVING
		}
		server.SetServingStatus("", status)
	}
	check()
	for {
		select {
		case <-ctx.Done():
			server.SetServingStatus("", grpc_health_v1.HealthCheckResponse_NOT_SERVING)
			return
		case <-ticker.C:
			check()
		}
	}
}
