package repository

import (
	"context"
	"errors"
	"os"
	"reflect"
	"testing"
	"time"
)

func newTestMySQLRepository(t *testing.T) (*MySQLRepository, func()) {
	t.Helper()

	dsn := os.Getenv("CORERANK_TEST_MYSQL_DSN")
	if dsn == "" {
		t.Skip("CORERANK_TEST_MYSQL_DSN is not set")
	}

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	repo, err := NewMySQLRepository(ctx, dsn)
	if err != nil {
		t.Fatalf("connect mysql: %v", err)
	}
	if err := repo.ResetTestData(ctx); err != nil {
		_ = repo.Close()
		t.Fatalf("reset mysql test data: %v", err)
	}

	cleanup := func() {
		_ = repo.ResetTestData(context.Background())
		_ = repo.Close()
	}
	return repo, cleanup
}

func TestMySQLRepositoryPersistsPlayerTicketResultAndSnapshot(t *testing.T) {
	repo, cleanup := newTestMySQLRepository(t)
	defer cleanup()

	ctx := context.Background()
	if err := repo.UpsertPlayerScore(ctx, "mysql-player", 1666); err != nil {
		t.Fatalf("upsert player score: %v", err)
	}
	player, err := repo.GetPlayer(ctx, "mysql-player")
	if err != nil {
		t.Fatalf("get player: %v", err)
	}
	if player.PlayerID != "mysql-player" || player.RankScore != 1666 {
		t.Fatalf("unexpected player: %#v", player)
	}

	ticket := MatchTicket{
		TicketID:  "ticket_mysql_1",
		PlayerID:  "mysql-player",
		MMRScore:  1500,
		MatchMode: "duel",
		Status:    MatchStatusMatched,
		MatchID:   "match_mysql_1",
		RoomID:    "room_mysql_1",
		CreatedAt: 1000,
		UpdatedAt: 2000,
		ExpiresAt: 3000,
	}
	if err := repo.UpsertMatchTicket(ctx, ticket); err != nil {
		t.Fatalf("upsert match ticket: %v", err)
	}
	savedTicket, err := repo.GetMatchTicket(ctx, ticket.TicketID)
	if err != nil {
		t.Fatalf("get match ticket: %v", err)
	}
	if savedTicket.Status != MatchStatusMatched || savedTicket.MatchID != ticket.MatchID {
		t.Fatalf("unexpected ticket: %#v", savedTicket)
	}

	result := MatchResult{
		MatchID:   ticket.MatchID,
		RoomID:    ticket.RoomID,
		MatchMode: ticket.MatchMode,
		PlayerIDs: []string{"mysql-player", "mysql-opponent"},
		Status:    MatchStatusMatched,
		CreatedAt: 2000,
	}
	if err := repo.UpsertMatchResult(ctx, result); err != nil {
		t.Fatalf("upsert match result: %v", err)
	}
	savedResult, err := repo.GetMatchResult(ctx, result.MatchID)
	if err != nil {
		t.Fatalf("get match result: %v", err)
	}
	if !reflect.DeepEqual(savedResult.PlayerIDs, result.PlayerIDs) {
		t.Fatalf("unexpected result players: %#v", savedResult.PlayerIDs)
	}

	if err := repo.SaveRankSnapshot(ctx, []RankSnapshotRow{{
		PlayerID:     "mysql-player",
		RankScore:    1666,
		RankPosition: 1,
		CapturedAtMS: 3000,
	}}); err != nil {
		t.Fatalf("save rank snapshot: %v", err)
	}
}

func TestMySQLRepositoryReturnsDomainNotFoundErrors(t *testing.T) {
	repo, cleanup := newTestMySQLRepository(t)
	defer cleanup()

	ctx := context.Background()
	if _, err := repo.GetPlayer(ctx, "missing"); !errors.Is(err, ErrPlayerNotFound) {
		t.Fatalf("expected ErrPlayerNotFound, got %v", err)
	}
	if _, err := repo.GetMatchTicket(ctx, "missing"); !errors.Is(err, ErrTicketNotFound) {
		t.Fatalf("expected ErrTicketNotFound, got %v", err)
	}
	if _, err := repo.GetMatchResult(ctx, "missing"); !errors.Is(err, ErrResultNotFound) {
		t.Fatalf("expected ErrResultNotFound, got %v", err)
	}
}
