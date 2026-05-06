package service

import (
	"context"
	"database/sql"
	"os"
	"testing"
	"time"

	"CoreRank/internal/repository"
)

const mysqlTestLockName = "corerank_mysql_tests"

func newTestMySQLForService(t *testing.T) (*repository.MySQLRepository, func()) {
	t.Helper()

	dsn := os.Getenv("CORERANK_TEST_MYSQL_DSN")
	if dsn == "" {
		t.Skip("CORERANK_TEST_MYSQL_DSN is not set")
	}
	lockCleanup := acquireMySQLTestLock(t, dsn)

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	mysqlRepo, err := repository.NewMySQLRepository(ctx, dsn)
	if err != nil {
		lockCleanup()
		t.Fatalf("connect mysql: %v", err)
	}
	if err := mysqlRepo.ResetTestData(ctx); err != nil {
		_ = mysqlRepo.Close()
		lockCleanup()
		t.Fatalf("reset mysql test data: %v", err)
	}

	cleanup := func() {
		_ = mysqlRepo.ResetTestData(context.Background())
		_ = mysqlRepo.Close()
		lockCleanup()
	}
	return mysqlRepo, cleanup
}

func acquireMySQLTestLock(t *testing.T, dsn string) func() {
	t.Helper()

	lockDB, err := sql.Open("mysql", dsn)
	if err != nil {
		t.Fatalf("open mysql lock connection: %v", err)
	}
	lockDB.SetMaxOpenConns(1)
	lockDB.SetMaxIdleConns(1)

	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	var acquired int
	if err := lockDB.QueryRowContext(ctx, "SELECT GET_LOCK(?, 25)", mysqlTestLockName).Scan(&acquired); err != nil {
		_ = lockDB.Close()
		t.Fatalf("acquire mysql test lock: %v", err)
	}
	if acquired != 1 {
		_ = lockDB.Close()
		t.Fatalf("timed out acquiring mysql test lock")
	}

	return func() {
		releaseCtx, releaseCancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer releaseCancel()

		var released sql.NullInt64
		_ = lockDB.QueryRowContext(releaseCtx, "SELECT RELEASE_LOCK(?)", mysqlTestLockName).Scan(&released)
		_ = lockDB.Close()
	}
}

func TestMatchServicePersistsLifecycleToMySQL(t *testing.T) {
	matchService, redisCleanup := newTestMatchService(t)
	defer redisCleanup()

	mysqlRepo, mysqlCleanup := newTestMySQLForService(t)
	defer mysqlCleanup()
	matchService.SetMySQLRepository(mysqlRepo)

	ctx := context.Background()
	first, err := matchService.CreateTicket(ctx, CreateMatchTicketRequest{
		PlayerID: "mysql-p1",
		MMRScore: 1200,
		MaxWait:  time.Minute,
	})
	if err != nil {
		t.Fatalf("create first ticket: %v", err)
	}
	second, err := matchService.CreateTicket(ctx, CreateMatchTicketRequest{
		PlayerID: "mysql-p2",
		MMRScore: 1210,
		MaxWait:  time.Minute,
	})
	if err != nil {
		t.Fatalf("create second ticket: %v", err)
	}

	savedFirst, err := mysqlRepo.GetMatchTicket(ctx, first.TicketID)
	if err != nil {
		t.Fatalf("get first ticket from mysql: %v", err)
	}
	if savedFirst.Status != repository.MatchStatusMatched || savedFirst.MatchID != second.MatchID {
		t.Fatalf("unexpected persisted first ticket: %#v", savedFirst)
	}

	savedResult, err := mysqlRepo.GetMatchResult(ctx, second.MatchID)
	if err != nil {
		t.Fatalf("get match result from mysql: %v", err)
	}
	if savedResult.RoomID != second.RoomID || len(savedResult.PlayerIDs) != 2 {
		t.Fatalf("unexpected persisted result: %#v", savedResult)
	}
}
