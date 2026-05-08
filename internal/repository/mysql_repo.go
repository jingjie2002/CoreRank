package repository

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"strings"
	"time"

	_ "embed"

	_ "github.com/go-sql-driver/mysql"
)

//go:embed mysql_schema.sql
var mysqlSchema string

var ErrPlayerNotFound = errors.New("player not found")

type MySQLRepository struct {
	db *sql.DB
}

type SQLPlayer struct {
	PlayerID    string
	RankScore   int64
	MMRScore    int64
	CreatedAtMS int64
	UpdatedAtMS int64
}

type RankSnapshotRow struct {
	PlayerID     string
	RankScore    int64
	RankPosition int64
	CapturedAtMS int64
}

func NewMySQLRepository(ctx context.Context, dsn string) (*MySQLRepository, error) {
	db, err := sql.Open("mysql", dsn)
	if err != nil {
		return nil, err
	}

	db.SetMaxOpenConns(10)
	db.SetMaxIdleConns(5)
	db.SetConnMaxLifetime(30 * time.Minute)

	if err := pingMySQLWithRetry(ctx, db, 15*time.Second); err != nil {
		_ = db.Close()
		return nil, err
	}

	repo := &MySQLRepository{db: db}
	if err := repo.InitSchema(ctx); err != nil {
		_ = db.Close()
		return nil, err
	}
	return repo, nil
}

func pingMySQLWithRetry(ctx context.Context, db *sql.DB, timeout time.Duration) error {
	deadline := time.Now().Add(timeout)
	var lastErr error

	for {
		if err := db.PingContext(ctx); err == nil {
			return nil
		} else {
			lastErr = err
		}

		if time.Now().After(deadline) {
			return lastErr
		}

		timer := time.NewTimer(500 * time.Millisecond)
		select {
		case <-ctx.Done():
			timer.Stop()
			if lastErr != nil {
				return lastErr
			}
			return ctx.Err()
		case <-timer.C:
		}
	}
}

func (r *MySQLRepository) Close() error {
	return r.db.Close()
}

func (r *MySQLRepository) InitSchema(ctx context.Context) error {
	statements := strings.Split(mysqlSchema, ";")
	for _, stmt := range statements {
		stmt = strings.TrimSpace(stmt)
		if stmt == "" {
			continue
		}
		if _, err := r.db.ExecContext(ctx, stmt); err != nil {
			return err
		}
	}
	if err := r.ensureColumn(ctx, "match_results", "server_id", "VARCHAR(80) NOT NULL DEFAULT '' AFTER room_id"); err != nil {
		return err
	}
	if err := r.ensureColumn(ctx, "match_results", "server_addr", "VARCHAR(255) NOT NULL DEFAULT '' AFTER server_id"); err != nil {
		return err
	}
	if err := r.ensureIndex(ctx, "match_results", "idx_match_results_server_id", "server_id"); err != nil {
		return err
	}
	return nil
}

func (r *MySQLRepository) ensureColumn(ctx context.Context, tableName, columnName, definition string) error {
	var count int
	err := r.db.QueryRowContext(ctx, `
SELECT COUNT(*)
FROM INFORMATION_SCHEMA.COLUMNS
WHERE TABLE_SCHEMA = DATABASE()
  AND TABLE_NAME = ?
  AND COLUMN_NAME = ?
`, tableName, columnName).Scan(&count)
	if err != nil {
		return err
	}
	if count > 0 {
		return nil
	}

	_, err = r.db.ExecContext(ctx, fmt.Sprintf("ALTER TABLE %s ADD COLUMN %s %s", tableName, columnName, definition))
	return err
}

func (r *MySQLRepository) ensureIndex(ctx context.Context, tableName, indexName, columnName string) error {
	var count int
	err := r.db.QueryRowContext(ctx, `
SELECT COUNT(*)
FROM INFORMATION_SCHEMA.STATISTICS
WHERE TABLE_SCHEMA = DATABASE()
  AND TABLE_NAME = ?
  AND INDEX_NAME = ?
`, tableName, indexName).Scan(&count)
	if err != nil {
		return err
	}
	if count > 0 {
		return nil
	}

	_, err = r.db.ExecContext(ctx, fmt.Sprintf("ALTER TABLE %s ADD INDEX %s (%s)", tableName, indexName, columnName))
	return err
}

func (r *MySQLRepository) UpsertPlayerScore(ctx context.Context, playerID string, score float64) error {
	now := time.Now().UnixMilli()
	rankScore := int64(math.Round(score))
	_, err := r.db.ExecContext(ctx, `
INSERT INTO players (player_id, rank_score, mmr_score, created_at_ms, updated_at_ms)
VALUES (?, ?, 0, ?, ?)
ON DUPLICATE KEY UPDATE
  rank_score = VALUES(rank_score),
  updated_at_ms = VALUES(updated_at_ms)
`, playerID, rankScore, now, now)
	return err
}

func (r *MySQLRepository) GetPlayer(ctx context.Context, playerID string) (*SQLPlayer, error) {
	row := r.db.QueryRowContext(ctx, `
SELECT player_id, rank_score, mmr_score, created_at_ms, updated_at_ms
FROM players
WHERE player_id = ?
`, playerID)

	var player SQLPlayer
	if err := row.Scan(&player.PlayerID, &player.RankScore, &player.MMRScore, &player.CreatedAtMS, &player.UpdatedAtMS); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return nil, ErrPlayerNotFound
		}
		return nil, err
	}
	return &player, nil
}

func (r *MySQLRepository) UpsertMatchTicket(ctx context.Context, ticket MatchTicket) error {
	_, err := r.db.ExecContext(ctx, `
INSERT INTO match_tickets (
  ticket_id, player_id, mmr_score, match_mode, status, match_id, room_id,
  created_at_ms, updated_at_ms, expires_at_ms
)
VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
ON DUPLICATE KEY UPDATE
  player_id = VALUES(player_id),
  mmr_score = VALUES(mmr_score),
  match_mode = VALUES(match_mode),
  status = VALUES(status),
  match_id = VALUES(match_id),
  room_id = VALUES(room_id),
  updated_at_ms = VALUES(updated_at_ms),
  expires_at_ms = VALUES(expires_at_ms)
`, ticket.TicketID, ticket.PlayerID, ticket.MMRScore, ticket.MatchMode, ticket.Status, ticket.MatchID, ticket.RoomID,
		ticket.CreatedAt, ticket.UpdatedAt, ticket.ExpiresAt)
	return err
}

func (r *MySQLRepository) GetMatchTicket(ctx context.Context, ticketID string) (*MatchTicket, error) {
	row := r.db.QueryRowContext(ctx, `
SELECT ticket_id, player_id, mmr_score, match_mode, status, match_id, room_id,
       created_at_ms, updated_at_ms, expires_at_ms
FROM match_tickets
WHERE ticket_id = ?
`, ticketID)

	var ticket MatchTicket
	if err := row.Scan(&ticket.TicketID, &ticket.PlayerID, &ticket.MMRScore, &ticket.MatchMode, &ticket.Status,
		&ticket.MatchID, &ticket.RoomID, &ticket.CreatedAt, &ticket.UpdatedAt, &ticket.ExpiresAt); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return nil, ErrTicketNotFound
		}
		return nil, err
	}
	return &ticket, nil
}

func (r *MySQLRepository) UpsertMatchResult(ctx context.Context, result MatchResult) error {
	playersJSON, err := json.Marshal(result.PlayerIDs)
	if err != nil {
		return err
	}

	_, err = r.db.ExecContext(ctx, `
INSERT INTO match_results (match_id, room_id, server_id, server_addr, match_mode, player_ids, status, created_at_ms)
VALUES (?, ?, ?, ?, ?, CAST(? AS JSON), ?, ?)
ON DUPLICATE KEY UPDATE
  room_id = VALUES(room_id),
  server_id = VALUES(server_id),
  server_addr = VALUES(server_addr),
  match_mode = VALUES(match_mode),
  player_ids = VALUES(player_ids),
  status = VALUES(status)
`, result.MatchID, result.RoomID, result.ServerID, result.ServerAddr, result.MatchMode, string(playersJSON), result.Status, result.CreatedAt)
	return err
}

func (r *MySQLRepository) GetMatchResult(ctx context.Context, matchID string) (*MatchResult, error) {
	row := r.db.QueryRowContext(ctx, `
SELECT match_id, room_id, server_id, server_addr, match_mode, player_ids, status, created_at_ms
FROM match_results
WHERE match_id = ?
`, matchID)

	var result MatchResult
	var playersJSON string
	if err := row.Scan(&result.MatchID, &result.RoomID, &result.ServerID, &result.ServerAddr, &result.MatchMode, &playersJSON, &result.Status, &result.CreatedAt); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return nil, ErrResultNotFound
		}
		return nil, err
	}
	if err := json.Unmarshal([]byte(playersJSON), &result.PlayerIDs); err != nil {
		return nil, err
	}
	return &result, nil
}

func (r *MySQLRepository) SaveRankSnapshot(ctx context.Context, rows []RankSnapshotRow) error {
	if len(rows) == 0 {
		return nil
	}

	tx, err := r.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer func() {
		_ = tx.Rollback()
	}()

	stmt, err := tx.PrepareContext(ctx, `
INSERT INTO rank_snapshots (player_id, rank_score, rank_position, captured_at_ms)
VALUES (?, ?, ?, ?)
`)
	if err != nil {
		return err
	}
	defer stmt.Close()

	for _, row := range rows {
		if _, err := stmt.ExecContext(ctx, row.PlayerID, row.RankScore, row.RankPosition, row.CapturedAtMS); err != nil {
			return err
		}
	}

	return tx.Commit()
}

func (r *MySQLRepository) ResetTestData(ctx context.Context) error {
	statements := []string{
		"DELETE FROM rank_snapshots",
		"DELETE FROM match_results",
		"DELETE FROM match_tickets",
		"DELETE FROM players",
	}
	for _, stmt := range statements {
		if _, err := r.db.ExecContext(ctx, stmt); err != nil {
			return err
		}
	}
	return nil
}
