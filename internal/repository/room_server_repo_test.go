package repository

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/redis/go-redis/v9"
)

func newTestRoomServerRepository(t *testing.T) (*RoomServerRepository, func()) {
	t.Helper()

	playerRepo, baseCleanup := newTestRepository(t)
	ctx := context.Background()
	if err := cleanRoomServerTestKeys(ctx, playerRepo.client); err != nil {
		baseCleanup()
		t.Fatalf("clean room server keys: %v", err)
	}

	cleanup := func() {
		_ = cleanRoomServerTestKeys(context.Background(), playerRepo.client)
		baseCleanup()
	}
	return NewRoomServerRepository(playerRepo.client), cleanup
}

func cleanRoomServerTestKeys(ctx context.Context, client *redis.Client) error {
	for _, pattern := range []string{"server:*", "room:assignment:*"} {
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
				break
			}
		}
	}
	return nil
}

func TestRoomServerRepositoryAllocatesLowestLoadServer(t *testing.T) {
	repo, cleanup := newTestRoomServerRepository(t)
	defer cleanup()

	ctx := context.Background()
	_, err := repo.RegisterGameServer(ctx, GameServer{
		ServerID:    "room-busy",
		ServerType:  GameServerTypeRoom,
		Addr:        "127.0.0.1:7001",
		MatchMode:   "duel",
		Capacity:    4,
		CurrentLoad: 2,
		Status:      GameServerStatusActive,
	})
	if err != nil {
		t.Fatalf("register busy server: %v", err)
	}
	_, err = repo.RegisterGameServer(ctx, GameServer{
		ServerID:   "room-idle",
		ServerType: GameServerTypeRoom,
		Addr:       "127.0.0.1:7002",
		MatchMode:  "duel",
		Capacity:   4,
		Status:     GameServerStatusActive,
	})
	if err != nil {
		t.Fatalf("register idle server: %v", err)
	}

	assignment, err := repo.AllocateRoomServer(ctx, RoomServerAllocationRequest{
		MatchID:          "match_test_1",
		RoomID:           "room_test_1",
		MatchMode:        "duel",
		PlayerIDs:        []string{"p1", "p2"},
		HeartbeatTimeout: time.Minute,
	})
	if err != nil {
		t.Fatalf("allocate room server: %v", err)
	}
	if assignment.ServerID != "room-idle" || assignment.CurrentLoad != 2 {
		t.Fatalf("expected idle server to be selected and reserved, got %#v", assignment)
	}

	server, err := repo.GetGameServer(ctx, "room-idle")
	if err != nil {
		t.Fatalf("get reserved server: %v", err)
	}
	if server.CurrentLoad != 2 {
		t.Fatalf("expected reserved load 2, got %#v", server)
	}

	if err := repo.ReleaseRoomServer(ctx, *assignment); err != nil {
		t.Fatalf("release room server: %v", err)
	}
	server, err = repo.GetGameServer(ctx, "room-idle")
	if err != nil {
		t.Fatalf("get released server: %v", err)
	}
	if server.CurrentLoad != 0 {
		t.Fatalf("expected released load 0, got %#v", server)
	}
}

func TestRoomServerRepositorySkipsUnavailableServers(t *testing.T) {
	repo, cleanup := newTestRoomServerRepository(t)
	defer cleanup()

	ctx := context.Background()
	now := time.Now()
	servers := []GameServer{
		{
			ServerID:  "room-draining",
			Addr:      "127.0.0.1:7101",
			MatchMode: "duel",
			Capacity:  4,
			Status:    GameServerStatusDraining,
		},
		{
			ServerID:  "room-unhealthy",
			Addr:      "127.0.0.1:7102",
			MatchMode: "duel",
			Capacity:  4,
			Status:    GameServerStatusUnhealthy,
		},
		{
			ServerID:  "room-full",
			Addr:      "127.0.0.1:7103",
			MatchMode: "duel",
			Capacity:  1,
			Status:    GameServerStatusActive,
		},
		{
			ServerID:        "room-stale",
			Addr:            "127.0.0.1:7104",
			MatchMode:       "duel",
			Capacity:        4,
			Status:          GameServerStatusActive,
			LastHeartbeatAt: now.Add(-time.Minute).UnixMilli(),
		},
	}
	for _, server := range servers {
		if _, err := repo.RegisterGameServer(ctx, server); err != nil {
			t.Fatalf("register %s: %v", server.ServerID, err)
		}
	}

	_, err := repo.AllocateRoomServer(ctx, RoomServerAllocationRequest{
		MatchID:          "match_test_2",
		RoomID:           "room_test_2",
		MatchMode:        "duel",
		PlayerIDs:        []string{"p1", "p2"},
		HeartbeatTimeout: 10 * time.Millisecond,
		NowMS:            now.UnixMilli(),
	})
	if !errors.Is(err, ErrNoAvailableRoomServer) {
		t.Fatalf("expected no available room server, got %v", err)
	}
}
