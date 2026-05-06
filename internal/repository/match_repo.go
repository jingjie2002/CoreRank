package repository

import (
	"context"
	"encoding/json"
	"errors"
	"strconv"
	"time"

	"github.com/redis/go-redis/v9"
)

const (
	MatchStatusQueued    = "queued"
	MatchStatusMatched   = "matched"
	MatchStatusCancelled = "cancelled"
	MatchStatusTimeout   = "timeout"

	defaultResultTTL = 24 * time.Hour
)

var (
	ErrPlayerAlreadyQueued = errors.New("player already has a queued ticket")
	ErrTicketNotFound      = errors.New("match ticket not found")
	ErrTicketNotQueued     = errors.New("match ticket is not queued")
	ErrResultNotFound      = errors.New("match result not found")
)

type MatchTicket struct {
	TicketID  string
	PlayerID  string
	MMRScore  int64
	MatchMode string
	Status    string
	MatchID   string
	RoomID    string
	CreatedAt int64
	UpdatedAt int64
	ExpiresAt int64
}

type MatchResult struct {
	MatchID   string
	RoomID    string
	MatchMode string
	PlayerIDs []string
	Status    string
	CreatedAt int64
}

func (r *PlayerRepository) CreateMatchTicket(ctx context.Context, ticket MatchTicket, ttl time.Duration) error {
	playerKey := playerTicketKey(ticket.PlayerID)
	ok, err := r.client.SetNX(ctx, playerKey, ticket.TicketID, ttl).Result()
	if err != nil {
		return err
	}
	if !ok {
		return ErrPlayerAlreadyQueued
	}

	ticketKey := matchTicketKey(ticket.TicketID)
	if err := r.client.HSet(ctx, ticketKey, ticket.toHash()).Err(); err != nil {
		_ = r.client.Del(context.Background(), playerKey).Err()
		return err
	}
	if err := r.client.Expire(ctx, ticketKey, ttl).Err(); err != nil {
		_ = r.client.Del(context.Background(), playerKey, ticketKey).Err()
		return err
	}
	if err := r.addPlayerToPool(ctx, MatchTicketPoolKey, ticket.PlayerID, ticket.MMRScore); err != nil {
		_ = r.client.Del(context.Background(), playerKey, ticketKey).Err()
		return err
	}
	if err := r.client.ZAdd(ctx, MatchTicketExpiryKey, redis.Z{
		Score:  float64(ticket.ExpiresAt),
		Member: ticket.TicketID,
	}).Err(); err != nil {
		_ = r.client.Del(context.Background(), playerKey, ticketKey).Err()
		_ = r.client.ZRem(context.Background(), MatchTicketPoolKey, ticket.PlayerID).Err()
		return err
	}

	return nil
}

func (r *PlayerRepository) GetMatchTicket(ctx context.Context, ticketID string) (*MatchTicket, error) {
	values, err := r.client.HGetAll(ctx, matchTicketKey(ticketID)).Result()
	if err != nil {
		return nil, err
	}
	if len(values) == 0 {
		return nil, ErrTicketNotFound
	}
	return matchTicketFromHash(values)
}

func (r *PlayerRepository) CancelMatchTicket(ctx context.Context, ticketID string, now int64) (*MatchTicket, error) {
	ticket, err := r.GetMatchTicket(ctx, ticketID)
	if err != nil {
		return nil, err
	}
	if ticket.Status != MatchStatusQueued {
		return nil, ErrTicketNotQueued
	}

	ticket.Status = MatchStatusCancelled
	ticket.UpdatedAt = now

	pipe := r.client.TxPipeline()
	pipe.HSet(ctx, matchTicketKey(ticketID), map[string]any{
		"status":     ticket.Status,
		"updated_at": ticket.UpdatedAt,
	})
	pipe.Del(ctx, playerTicketKey(ticket.PlayerID))
	pipe.ZRem(ctx, MatchTicketPoolKey, ticket.PlayerID)
	pipe.ZRem(ctx, MatchTicketExpiryKey, ticketID)
	_, err = pipe.Exec(ctx)
	if err != nil {
		return nil, err
	}

	return ticket, nil
}

func (r *PlayerRepository) GetPlayerTicketID(ctx context.Context, playerID string) (string, error) {
	ticketID, err := r.client.Get(ctx, playerTicketKey(playerID)).Result()
	if err != nil {
		return "", err
	}
	return ticketID, nil
}

func (r *PlayerRepository) CompleteMatch(ctx context.Context, playerIDs []string, result MatchResult) (*MatchResult, []*MatchTicket, error) {
	tickets := make([]*MatchTicket, 0, len(playerIDs))
	for _, playerID := range playerIDs {
		ticketID, err := r.GetPlayerTicketID(ctx, playerID)
		if err != nil {
			if err == redis.Nil {
				continue
			}
			return nil, nil, err
		}
		ticket, err := r.GetMatchTicket(ctx, ticketID)
		if err != nil {
			if err == ErrTicketNotFound {
				continue
			}
			return nil, nil, err
		}
		if ticket.Status == MatchStatusQueued {
			tickets = append(tickets, ticket)
		}
	}

	if len(tickets) < 2 {
		return nil, nil, nil
	}

	matchedPlayerIDs := make([]string, 0, len(tickets))
	pipe := r.client.TxPipeline()
	for _, ticket := range tickets {
		ticket.Status = MatchStatusMatched
		ticket.MatchID = result.MatchID
		ticket.RoomID = result.RoomID
		ticket.UpdatedAt = result.CreatedAt
		matchedPlayerIDs = append(matchedPlayerIDs, ticket.PlayerID)
		pipe.HSet(ctx, matchTicketKey(ticket.TicketID), map[string]any{
			"status":     ticket.Status,
			"match_id":   ticket.MatchID,
			"room_id":    ticket.RoomID,
			"updated_at": ticket.UpdatedAt,
		})
		pipe.Del(ctx, playerTicketKey(ticket.PlayerID))
		pipe.ZRem(ctx, MatchTicketExpiryKey, ticket.TicketID)
	}

	result.PlayerIDs = matchedPlayerIDs
	playersJSON, err := json.Marshal(result.PlayerIDs)
	if err != nil {
		return nil, nil, err
	}
	pipe.HSet(ctx, matchResultKey(result.MatchID), map[string]any{
		"match_id":   result.MatchID,
		"room_id":    result.RoomID,
		"match_mode": result.MatchMode,
		"player_ids": string(playersJSON),
		"status":     result.Status,
		"created_at": result.CreatedAt,
	})
	pipe.Expire(ctx, matchResultKey(result.MatchID), defaultResultTTL)

	if _, err := pipe.Exec(ctx); err != nil {
		return nil, nil, err
	}

	return &result, tickets, nil
}

func (r *PlayerRepository) TimeoutExpiredMatchTickets(ctx context.Context, now int64, limit int64) ([]*MatchTicket, error) {
	if limit <= 0 {
		limit = 100
	}

	ticketIDs, err := r.client.ZRangeByScore(ctx, MatchTicketExpiryKey, &redis.ZRangeBy{
		Min:    "-inf",
		Max:    strconv.FormatInt(now, 10),
		Offset: 0,
		Count:  limit,
	}).Result()
	if err != nil {
		return nil, err
	}
	if len(ticketIDs) == 0 {
		return nil, nil
	}

	timedOut := make([]*MatchTicket, 0, len(ticketIDs))
	for _, ticketID := range ticketIDs {
		ticket, err := r.timeoutMatchTicket(ctx, ticketID, now)
		if err != nil {
			return nil, err
		}
		if ticket != nil {
			timedOut = append(timedOut, ticket)
		}
	}
	return timedOut, nil
}

func (r *PlayerRepository) timeoutMatchTicket(ctx context.Context, ticketID string, now int64) (*MatchTicket, error) {
	ticket, err := r.GetMatchTicket(ctx, ticketID)
	if err != nil {
		if errors.Is(err, ErrTicketNotFound) {
			_ = r.client.ZRem(ctx, MatchTicketExpiryKey, ticketID).Err()
			return nil, nil
		}
		return nil, err
	}

	result, err := TimeoutMatchTicketScript.Run(
		ctx,
		r.client,
		[]string{
			matchTicketKey(ticketID),
			playerTicketKey(ticket.PlayerID),
			MatchTicketPoolKey,
			MatchTicketExpiryKey,
		},
		ticketID,
		now,
		MatchStatusQueued,
		MatchStatusTimeout,
	).Int()
	if err != nil {
		return nil, err
	}
	if result != 1 {
		return nil, nil
	}

	ticket.Status = MatchStatusTimeout
	ticket.UpdatedAt = now
	return ticket, nil
}

func (r *PlayerRepository) GetMatchResult(ctx context.Context, matchID string) (*MatchResult, error) {
	values, err := r.client.HGetAll(ctx, matchResultKey(matchID)).Result()
	if err != nil {
		return nil, err
	}
	if len(values) == 0 {
		return nil, ErrResultNotFound
	}
	return matchResultFromHash(values)
}

func (t MatchTicket) toHash() map[string]any {
	return map[string]any{
		"ticket_id":  t.TicketID,
		"player_id":  t.PlayerID,
		"mmr_score":  t.MMRScore,
		"match_mode": t.MatchMode,
		"status":     t.Status,
		"match_id":   t.MatchID,
		"room_id":    t.RoomID,
		"created_at": t.CreatedAt,
		"updated_at": t.UpdatedAt,
		"expires_at": t.ExpiresAt,
	}
}

func matchTicketFromHash(values map[string]string) (*MatchTicket, error) {
	ticket := &MatchTicket{
		TicketID:  values["ticket_id"],
		PlayerID:  values["player_id"],
		MatchMode: values["match_mode"],
		Status:    values["status"],
		MatchID:   values["match_id"],
		RoomID:    values["room_id"],
	}

	var err error
	if ticket.MMRScore, err = parseIntField(values, "mmr_score"); err != nil {
		return nil, err
	}
	if ticket.CreatedAt, err = parseIntField(values, "created_at"); err != nil {
		return nil, err
	}
	if ticket.UpdatedAt, err = parseIntField(values, "updated_at"); err != nil {
		return nil, err
	}
	if ticket.ExpiresAt, err = parseIntField(values, "expires_at"); err != nil {
		return nil, err
	}

	return ticket, nil
}

func matchResultFromHash(values map[string]string) (*MatchResult, error) {
	result := &MatchResult{
		MatchID:   values["match_id"],
		RoomID:    values["room_id"],
		MatchMode: values["match_mode"],
		Status:    values["status"],
	}

	if err := json.Unmarshal([]byte(values["player_ids"]), &result.PlayerIDs); err != nil {
		return nil, err
	}

	createdAt, err := parseIntField(values, "created_at")
	if err != nil {
		return nil, err
	}
	result.CreatedAt = createdAt

	return result, nil
}

func parseIntField(values map[string]string, field string) (int64, error) {
	return strconv.ParseInt(values[field], 10, 64)
}

func matchTicketKey(ticketID string) string {
	return "match:ticket:" + ticketID
}

func matchResultKey(matchID string) string {
	return "match:result:" + matchID
}

func playerTicketKey(playerID string) string {
	return "match:player_ticket:" + playerID
}
