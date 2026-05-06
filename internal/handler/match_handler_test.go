package handler

import (
	"context"
	"net"
	"os"
	"reflect"
	"testing"
	"time"

	pb "CoreRank/api/proto"
	"CoreRank/internal/repository"
	"CoreRank/internal/service"
	"CoreRank/internal/testutil"

	"github.com/redis/go-redis/v9"
	"google.golang.org/grpc"
	"google.golang.org/grpc/credentials/insecure"
	"google.golang.org/grpc/test/bufconn"
)

const grpcTestBufferSize = 1024 * 1024

func newTestMatchClient(t *testing.T) (pb.MatchServiceClient, func()) {
	t.Helper()

	addr := os.Getenv("CORERANK_TEST_REDIS_ADDR")
	if addr == "" {
		addr = "127.0.0.1:6379"
	}

	redisClient := redis.NewClient(&redis.Options{Addr: addr, DB: 0})
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()

	if err := redisClient.Ping(ctx).Err(); err != nil {
		t.Skipf("redis is unavailable at %s: %v", addr, err)
	}
	releaseLock := acquireRedisTestLock(t, redisClient)
	if err := cleanMatchHandlerTestKeys(ctx, redisClient); err != nil {
		releaseLock()
		_ = redisClient.Close()
		t.Fatalf("clean redis keys: %v", err)
	}

	listener := bufconn.Listen(grpcTestBufferSize)
	grpcServer := grpc.NewServer()
	repo := repository.NewPlayerRepository(redisClient)
	matchService := service.NewMatchService(repo)
	pb.RegisterMatchServiceServer(grpcServer, NewMatchHandler(matchService))

	go func() {
		_ = grpcServer.Serve(listener)
	}()

	dialer := func(context.Context, string) (net.Conn, error) {
		return listener.Dial()
	}
	conn, err := grpc.DialContext(
		context.Background(),
		"bufnet",
		grpc.WithContextDialer(dialer),
		grpc.WithTransportCredentials(insecure.NewCredentials()),
	)
	if err != nil {
		t.Fatalf("dial bufconn: %v", err)
	}

	cleanup := func() {
		_ = conn.Close()
		grpcServer.Stop()
		_ = listener.Close()
		_ = cleanMatchHandlerTestKeys(context.Background(), redisClient)
		releaseLock()
		_ = redisClient.Close()
	}

	return pb.NewMatchServiceClient(conn), cleanup
}

func acquireRedisTestLock(t *testing.T, client *redis.Client) func() {
	t.Helper()

	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	release, err := testutil.AcquireRedisTestLock(ctx, client)
	if err != nil {
		_ = client.Close()
		t.Fatalf("acquire redis test lock: %v", err)
	}

	return func() {
		releaseCtx, releaseCancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer releaseCancel()
		_ = release(releaseCtx)
	}
}

func cleanMatchHandlerTestKeys(ctx context.Context, client *redis.Client) error {
	if err := client.Del(ctx, repository.MatchPoolKey, repository.MatchTicketPoolKey, repository.GlobalRankKey).Err(); err != nil {
		return err
	}

	var cursor uint64
	for {
		keys, nextCursor, err := client.Scan(ctx, cursor, "match:*", 100).Result()
		if err != nil {
			return err
		}
		if len(keys) > 0 {
			if err := client.Del(ctx, keys...).Err(); err != nil {
				return err
			}
		}
		cursor = nextCursor
		if cursor == 0 {
			return nil
		}
	}
}

func TestMatchServiceGRPCLifecycle(t *testing.T) {
	client, cleanup := newTestMatchClient(t)
	defer cleanup()

	ctx := context.Background()
	first, err := client.CreateMatchTicket(ctx, &pb.CreateMatchTicketRequest{
		PlayerId:  "grpc-p1",
		MmrScore:  1200,
		MatchMode: "duel",
		MaxWaitMs: 30000,
	})
	if err != nil {
		t.Fatalf("create first ticket: %v", err)
	}
	if first.GetTicket().GetStatus() != repository.MatchStatusQueued {
		t.Fatalf("first ticket should be queued, got %s", first.GetTicket().GetStatus())
	}

	second, err := client.CreateMatchTicket(ctx, &pb.CreateMatchTicketRequest{
		PlayerId:  "grpc-p2",
		MmrScore:  1210,
		MatchMode: "duel",
		MaxWaitMs: 30000,
	})
	if err != nil {
		t.Fatalf("create second ticket: %v", err)
	}
	if second.GetTicket().GetStatus() != repository.MatchStatusMatched {
		t.Fatalf("second ticket should be matched, got %s", second.GetTicket().GetStatus())
	}

	refreshedFirst, err := client.GetMatchTicket(ctx, &pb.GetMatchTicketRequest{
		TicketId: first.GetTicket().GetTicketId(),
	})
	if err != nil {
		t.Fatalf("get first ticket: %v", err)
	}
	if refreshedFirst.GetTicket().GetMatchId() != second.GetTicket().GetMatchId() {
		t.Fatalf("tickets should share match id: first=%s second=%s",
			refreshedFirst.GetTicket().GetMatchId(),
			second.GetTicket().GetMatchId(),
		)
	}

	result, err := client.GetMatchResult(ctx, &pb.GetMatchResultRequest{
		MatchId: second.GetTicket().GetMatchId(),
	})
	if err != nil {
		t.Fatalf("get match result: %v", err)
	}
	if !reflect.DeepEqual(result.GetResult().GetPlayerIds(), []string{"grpc-p1", "grpc-p2"}) {
		t.Fatalf("unexpected matched players: %#v", result.GetResult().GetPlayerIds())
	}
}
