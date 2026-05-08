package repository

import (
	"context"
	"os"
	"reflect"
	"testing"
	"time"

	"CoreRank/internal/testutil"

	"github.com/redis/go-redis/v9"
)

func newTestRepository(t *testing.T) (*PlayerRepository, func()) {
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
	if err := cleanRedisTestKeys(ctx, client); err != nil {
		releaseLock()
		_ = client.Close()
		t.Fatalf("clean redis keys: %v", err)
	}

	repo := NewPlayerRepository(client)
	cleanup := func() {
		_ = cleanRedisTestKeys(context.Background(), client)
		releaseLock()
		_ = client.Close()
	}
	return repo, cleanup
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

func cleanRedisTestKeys(ctx context.Context, client *redis.Client) error {
	if err := client.Del(ctx, MatchPoolKey, MatchTicketPoolKey, MatchTicketExpiryKey, GlobalRankKey).Err(); err != nil {
		return err
	}

	if err := cleanRedisKeysByPattern(ctx, client, "match:*"); err != nil {
		return err
	}
	return cleanRedisKeysByPattern(ctx, client, "{rank:*}")
}

func cleanRedisKeysByPattern(ctx context.Context, client *redis.Client, pattern string) error {
	var cursor uint64
	for {
		keys, nextCursor, err := client.Scan(ctx, cursor, pattern, 100).Result()
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

func TestSearchAndPickPlayersIsAtomicAndRemovesMembers(t *testing.T) {
	repo, cleanup := newTestRepository(t)
	defer cleanup()

	ctx := context.Background()
	if err := repo.AddPlayerToPool(ctx, "p1", 1200); err != nil {
		t.Fatalf("add p1: %v", err)
	}
	if err := repo.AddPlayerToPool(ctx, "p2", 1210); err != nil {
		t.Fatalf("add p2: %v", err)
	}

	players, err := repo.SearchAndPickPlayers(ctx, 1100, 1300, 2)
	if err != nil {
		t.Fatalf("search and pick: %v", err)
	}
	if !reflect.DeepEqual(players, []string{"p1", "p2"}) {
		t.Fatalf("unexpected picked players: %#v", players)
	}

	remaining, err := repo.SearchAndPickPlayers(ctx, 1100, 1300, 2)
	if err != nil {
		t.Fatalf("search remaining: %v", err)
	}
	if len(remaining) != 0 {
		t.Fatalf("players should have been removed atomically, got %#v", remaining)
	}
}

func TestSearchAndPickPlayersKeepsMembersWhenBelowRequiredCount(t *testing.T) {
	repo, cleanup := newTestRepository(t)
	defer cleanup()

	ctx := context.Background()
	if err := repo.AddPlayerToPool(ctx, "single", 1200); err != nil {
		t.Fatalf("add single: %v", err)
	}

	players, err := repo.SearchAndPickPlayers(ctx, 1100, 1300, 2)
	if err != nil {
		t.Fatalf("search and pick: %v", err)
	}
	if len(players) != 0 {
		t.Fatalf("should not pick below required count, got %#v", players)
	}

	players, err = repo.SearchAndPickPlayers(ctx, 1100, 1300, 1)
	if err != nil {
		t.Fatalf("search and pick one: %v", err)
	}
	if !reflect.DeepEqual(players, []string{"single"}) {
		t.Fatalf("single player should remain in pool, got %#v", players)
	}
}

func TestGlobalRankUsesRedisSortedSetOrder(t *testing.T) {
	repo, cleanup := newTestRepository(t)
	defer cleanup()

	ctx := context.Background()
	for playerID, score := range map[string]float64{
		"alice": 1200,
		"bob":   1500,
		"carol": 1300,
	} {
		if err := repo.UpdatePlayerScore(ctx, playerID, score); err != nil {
			t.Fatalf("update score %s: %v", playerID, err)
		}
	}

	top, err := repo.GetGlobalRank(ctx, 3)
	if err != nil {
		t.Fatalf("get global rank: %v", err)
	}
	if len(top) != 3 || top[0].Member != "bob" || top[1].Member != "carol" || top[2].Member != "alice" {
		t.Fatalf("unexpected top ranking: %#v", top)
	}

	rank, err := repo.GetPlayerRank(ctx, "carol")
	if err != nil {
		t.Fatalf("get player rank: %v", err)
	}
	if rank != 1 {
		t.Fatalf("expected zero-based rank 1 for carol, got %d", rank)
	}
}

func TestLeaderboardTypesAreIsolated(t *testing.T) {
	repo, cleanup := newTestRepository(t)
	defer cleanup()

	ctx := context.Background()
	if err := repo.UpdatePlayerScore(ctx, "global-top", 9999); err != nil {
		t.Fatalf("update global score: %v", err)
	}
	for playerID, score := range map[string]float64{
		"season-a": 2100,
		"season-b": 2500,
	} {
		if err := repo.UpdatePlayerScoreInLeaderboard(ctx, "season:ss25", playerID, score); err != nil {
			t.Fatalf("update season score %s: %v", playerID, err)
		}
	}

	topSeason, err := repo.GetLeaderboardRank(ctx, "season:ss25", 2)
	if err != nil {
		t.Fatalf("get season rank: %v", err)
	}
	if len(topSeason) != 2 || topSeason[0].Member != "season-b" || topSeason[1].Member != "season-a" {
		t.Fatalf("unexpected season ranking: %#v", topSeason)
	}

	if _, err := repo.GetPlayerRankInLeaderboard(ctx, "season:ss25", "global-top"); err != redis.Nil {
		t.Fatalf("global player should not be ranked in season leaderboard, err=%v", err)
	}
}
