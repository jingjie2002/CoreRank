package service

import (
	"context"
	"errors"
	"os"
	"reflect"
	"testing"
	"time"

	"CoreRank/internal/repository"
	"CoreRank/internal/testutil"

	"github.com/redis/go-redis/v9"
)

func newTestMatchService(t *testing.T) (*MatchService, func()) {
	t.Helper()

	addr := os.Getenv("CORERANK_TEST_REDIS_ADDR")
	if addr == "" {
		addr = "127.0.0.1:6379"
	}

	client := redis.NewClient(&redis.Options{Addr: addr, DB: 0})
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()

	if err := client.Ping(ctx).Err(); err != nil {
		t.Skipf("redis is unavailable at %s: %v", addr, err)
	}

	releaseLock := acquireRedisTestLock(t, client)
	if err := cleanMatchServiceTestKeys(ctx, client); err != nil {
		releaseLock()
		_ = client.Close()
		t.Fatalf("clean redis keys: %v", err)
	}

	repo := repository.NewPlayerRepository(client)
	cleanup := func() {
		_ = cleanMatchServiceTestKeys(context.Background(), client)
		releaseLock()
		_ = client.Close()
	}
	return NewMatchService(repo), cleanup
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

func cleanMatchServiceTestKeys(ctx context.Context, client *redis.Client) error {
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

func TestMatchTicketsCreateMatchedResult(t *testing.T) {
	matchService, cleanup := newTestMatchService(t)
	defer cleanup()

	ctx := context.Background()
	first, err := matchService.CreateTicket(ctx, CreateMatchTicketRequest{
		PlayerID: "p1",
		MMRScore: 1200,
		MaxWait:  time.Minute,
	})
	if err != nil {
		t.Fatalf("create first ticket: %v", err)
	}
	if first.Status != repository.MatchStatusQueued {
		t.Fatalf("first ticket should wait, got %s", first.Status)
	}

	second, err := matchService.CreateTicket(ctx, CreateMatchTicketRequest{
		PlayerID: "p2",
		MMRScore: 1210,
		MaxWait:  time.Minute,
	})
	if err != nil {
		t.Fatalf("create second ticket: %v", err)
	}
	if second.Status != repository.MatchStatusMatched {
		t.Fatalf("second ticket should be matched, got %s", second.Status)
	}
	if second.MatchID == "" || second.RoomID == "" {
		t.Fatalf("matched ticket should include match and room ids: %#v", second)
	}

	refreshedFirst, err := matchService.GetTicket(ctx, first.TicketID)
	if err != nil {
		t.Fatalf("get first ticket: %v", err)
	}
	if refreshedFirst.Status != repository.MatchStatusMatched || refreshedFirst.MatchID != second.MatchID {
		t.Fatalf("first ticket should share matched result, got %#v", refreshedFirst)
	}

	result, err := matchService.GetResult(ctx, second.MatchID)
	if err != nil {
		t.Fatalf("get match result: %v", err)
	}
	if result.RoomID != second.RoomID {
		t.Fatalf("result room mismatch: %#v", result)
	}
	if !reflect.DeepEqual(result.PlayerIDs, []string{"p1", "p2"}) {
		t.Fatalf("unexpected matched players: %#v", result.PlayerIDs)
	}
}

func TestMatchTicketCanBeCancelled(t *testing.T) {
	matchService, cleanup := newTestMatchService(t)
	defer cleanup()

	ctx := context.Background()
	ticket, err := matchService.CreateTicket(ctx, CreateMatchTicketRequest{
		PlayerID: "cancel-me",
		MMRScore: 1500,
		MaxWait:  time.Minute,
	})
	if err != nil {
		t.Fatalf("create ticket: %v", err)
	}

	cancelled, err := matchService.CancelTicket(ctx, ticket.TicketID)
	if err != nil {
		t.Fatalf("cancel ticket: %v", err)
	}
	if cancelled.Status != repository.MatchStatusCancelled {
		t.Fatalf("ticket should be cancelled, got %s", cancelled.Status)
	}

	_, err = matchService.CancelTicket(ctx, ticket.TicketID)
	if !errors.Is(err, repository.ErrTicketNotQueued) {
		t.Fatalf("second cancellation should fail as not queued, got %v", err)
	}
}

func TestDuplicateQueuedTicketIsRejected(t *testing.T) {
	matchService, cleanup := newTestMatchService(t)
	defer cleanup()

	ctx := context.Background()
	_, err := matchService.CreateTicket(ctx, CreateMatchTicketRequest{
		PlayerID: "dupe",
		MMRScore: 1500,
		MaxWait:  time.Minute,
	})
	if err != nil {
		t.Fatalf("create first ticket: %v", err)
	}

	_, err = matchService.CreateTicket(ctx, CreateMatchTicketRequest{
		PlayerID: "dupe",
		MMRScore: 1500,
		MaxWait:  time.Minute,
	})
	if !errors.Is(err, repository.ErrPlayerAlreadyQueued) {
		t.Fatalf("duplicate ticket should be rejected, got %v", err)
	}
}
