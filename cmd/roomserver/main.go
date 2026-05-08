package main

import (
	"context"
	"fmt"
	"log"
	"net"
	"os"
	"os/signal"
	"strconv"
	"strings"
	"syscall"
	"time"

	"CoreRank/internal/roomserver"
)

const (
	defaultRoomServerID = "demo-room-1"
	defaultRoomAddr     = "127.0.0.1:7001"
	defaultCoreRankHTTP = "http://127.0.0.1:8081"
)

func main() {
	logger := log.New(os.Stdout, "[RoomServer] ", log.LstdFlags)
	config := roomserver.Config{
		ServerID:          envOrDefault("ROOM_SERVER_ID", defaultRoomServerID),
		Addr:              envOrDefault("ROOM_SERVER_ADDR", defaultRoomAddr),
		CoreRankHTTP:      envOrDefault("CORE_RANK_HTTP", defaultCoreRankHTTP),
		MatchMode:         envOrDefault("MATCH_MODE", roomserver.DefaultMatchMode),
		Capacity:          envInt64("CAPACITY", roomserver.DefaultCapacity),
		HeartbeatInterval: envDuration("HEARTBEAT_INTERVAL", envInt64("HEARTBEAT_INTERVAL_MS", 0)),
	}

	listener, err := net.Listen("tcp", config.Addr)
	if err != nil {
		logger.Fatalf("listen %s failed: %v", config.Addr, err)
	}
	defer listener.Close()

	server := roomserver.NewServer(config)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	if err := server.Register(ctx); err != nil {
		logger.Fatalf("register to CoreRank failed: %v", err)
	}
	if err := server.Heartbeat(ctx); err != nil {
		logger.Fatalf("initial heartbeat failed: %v", err)
	}

	go server.RunHeartbeat(ctx, logger)
	go func() {
		if err := server.Serve(ctx, listener); err != nil {
			logger.Printf("serve failed: %v", err)
			cancel()
		}
	}()

	logger.Printf("listening on %s, registered to %s as %s", config.Addr, config.CoreRankHTTP, config.ServerID)

	quit := make(chan os.Signal, 1)
	signal.Notify(quit, syscall.SIGINT, syscall.SIGTERM)
	select {
	case sig := <-quit:
		logger.Printf("received %v, shutting down", sig)
	case <-ctx.Done():
	}
	cancel()
	_ = listener.Close()
	logger.Println("stopped")
}

func envOrDefault(name, fallback string) string {
	if value := strings.TrimSpace(os.Getenv(name)); value != "" {
		return value
	}
	return fallback
}

func envInt64(name string, fallback int64) int64 {
	value := strings.TrimSpace(os.Getenv(name))
	if value == "" {
		return fallback
	}
	parsed, err := strconv.ParseInt(value, 10, 64)
	if err != nil {
		return fallback
	}
	return parsed
}

func envDuration(name string, fallbackMS int64) time.Duration {
	value := strings.TrimSpace(os.Getenv(name))
	if value != "" {
		parsed, err := time.ParseDuration(value)
		if err == nil {
			return parsed
		}
	}
	if fallbackMS > 0 {
		return time.Duration(fallbackMS) * time.Millisecond
	}
	return 0
}

func init() {
	if len(os.Args) > 1 && os.Args[1] == "-h" {
		fmt.Println("CoreRank roomserver")
		fmt.Println("env: ROOM_SERVER_ID, ROOM_SERVER_ADDR, CORE_RANK_HTTP, MATCH_MODE, CAPACITY, HEARTBEAT_INTERVAL")
		os.Exit(0)
	}
}
