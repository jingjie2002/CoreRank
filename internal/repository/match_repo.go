package repository

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
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
	ErrMatchModeMismatch   = errors.New("match tickets use different match modes")
	ErrTooManyMatchModes   = errors.New("too many active match modes")
)

type MatchTicket struct {
	TicketID  string `json:"ticket_id"`
	PlayerID  string `json:"player_id"`
	MMRScore  int64  `json:"mmr_score"`
	MatchMode string `json:"match_mode"`
	Status    string `json:"status"`
	MatchID   string `json:"match_id"`
	RoomID    string `json:"room_id"`
	CreatedAt int64  `json:"created_at"`
	UpdatedAt int64  `json:"updated_at"`
	ExpiresAt int64  `json:"expires_at"`
}

type MatchResult struct {
	MatchID    string   `json:"match_id"`
	RoomID     string   `json:"room_id"`
	ServerID   string   `json:"server_id"`
	ServerAddr string   `json:"server_addr"`
	MatchMode  string   `json:"match_mode"`
	PlayerIDs  []string `json:"player_ids"`
	JoinToken  string   `json:"join_token"`
	Status     string   `json:"status"`
	CreatedAt  int64    `json:"created_at"`
}

func (r *PlayerRepository) CreateMatchTicket(ctx context.Context, ticket MatchTicket, ttl time.Duration) error {
	mode, err := NormalizeMatchMode(ticket.MatchMode)
	if err != nil {
		return err
	}
	ticket.MatchMode = mode
	ttlMS := ttl.Milliseconds()
	if ttlMS <= 0 {
		return errors.New("ticket ttl must be greater than zero")
	}

	result, err := CreateMatchTicketScript.Run(
		ctx,
		r.client,
		[]string{
			playerTicketKey(ticket.PlayerID),
			matchTicketKey(ticket.TicketID),
			MatchTicketPoolKeyForMode(ticket.MatchMode),
			MatchTicketExpiryKey,
			MatchTicketModesKey,
		},
		ticket.TicketID,
		ticket.PlayerID,
		ticket.MMRScore,
		ticket.MatchMode,
		ticket.Status,
		ticket.CreatedAt,
		ticket.UpdatedAt,
		ticket.ExpiresAt,
		ttlMS,
	).Int()
	if err != nil {
		return err
	}
	if result == -2 {
		return ErrTooManyMatchModes
	}
	if result != 1 {
		return ErrPlayerAlreadyQueued
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

	result, err := CancelMatchTicketScript.Run(
		ctx,
		r.client,
		[]string{
			matchTicketKey(ticketID),
			playerTicketKey(ticket.PlayerID),
			MatchTicketPoolKeyForMode(ticket.MatchMode),
			MatchTicketExpiryKey,
			MatchTicketModesKey,
		},
		ticketID,
		now,
		MatchStatusQueued,
		MatchStatusCancelled,
		ticket.MatchMode,
	).Int()
	if err != nil {
		return nil, err
	}
	if result == -1 {
		return nil, ErrTicketNotFound
	}
	if result != 1 {
		return nil, ErrTicketNotQueued
	}

	return r.GetMatchTicket(ctx, ticketID)
}

func (r *PlayerRepository) GetPlayerTicketID(ctx context.Context, playerID string) (string, error) {
	ticketID, err := r.client.Get(ctx, playerTicketKey(playerID)).Result()
	if err != nil {
		return "", err
	}
	return ticketID, nil
}

func (r *PlayerRepository) CompleteMatch(ctx context.Context, playerIDs []string, result MatchResult) (*MatchResult, []*MatchTicket, error) {
	if len(playerIDs) < 2 {
		return nil, nil, nil
	}
	mode, err := NormalizeMatchMode(result.MatchMode)
	if err != nil {
		return nil, nil, err
	}
	result.MatchMode = mode
	result.PlayerIDs = append([]string(nil), playerIDs...)
	playersJSON, err := json.Marshal(result.PlayerIDs)
	if err != nil {
		return nil, nil, err
	}

	args := []any{
		result.MatchMode,
		MatchStatusQueued,
		MatchStatusMatched,
		result.MatchID,
		result.RoomID,
		result.ServerID,
		result.ServerAddr,
		result.Status,
		result.CreatedAt,
		defaultResultTTL.Milliseconds(),
		string(playersJSON),
		result.JoinToken,
		len(result.PlayerIDs),
	}
	for _, playerID := range result.PlayerIDs {
		args = append(args, playerID)
	}

	raw, err := CompleteMatchScript.Run(
		ctx,
		r.client,
		[]string{
			matchResultKey(result.MatchID),
			MatchTicketPoolKeyForMode(result.MatchMode),
			MatchTicketExpiryKey,
			MatchTicketModesKey,
		},
		args...,
	).Result()
	if err != nil {
		return nil, nil, err
	}
	values, ok := raw.([]interface{})
	if !ok || len(values) == 0 {
		return nil, nil, fmt.Errorf("unexpected complete match script result: %#v", raw)
	}
	switch scriptInt(values[0]) {
	case -1:
		return nil, nil, ErrMatchModeMismatch
	case 0:
		return nil, nil, nil
	case 1:
	default:
		return nil, nil, fmt.Errorf("unexpected complete match script status: %#v", values[0])
	}

	tickets := make([]*MatchTicket, 0, len(values)-1)
	for _, value := range values[1:] {
		ticketID, ok := value.(string)
		if !ok || ticketID == "" {
			return nil, nil, fmt.Errorf("unexpected ticket id from complete match script: %#v", value)
		}
		ticket, err := r.GetMatchTicket(ctx, ticketID)
		if err != nil {
			return nil, nil, err
		}
		tickets = append(tickets, ticket)
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

func (r *PlayerRepository) CountQueuedMatchTickets(ctx context.Context, matchMode string) (int64, error) {
	return r.client.ZCard(ctx, MatchTicketPoolKeyForMode(matchMode)).Result()
}

func (r *PlayerRepository) RequeueMatchTicketPlayers(ctx context.Context, playerIDs []string) error {
	for _, playerID := range playerIDs {
		ticketID, err := r.GetPlayerTicketID(ctx, playerID)
		if err != nil {
			if errors.Is(err, redis.Nil) {
				continue
			}
			return err
		}

		ticket, err := r.GetMatchTicket(ctx, ticketID)
		if err != nil {
			if errors.Is(err, ErrTicketNotFound) {
				continue
			}
			return err
		}
		_, err = RequeueMatchTicketScript.Run(ctx, r.client, []string{
			matchTicketKey(ticket.TicketID),
			playerTicketKey(ticket.PlayerID),
			MatchTicketPoolKeyForMode(ticket.MatchMode),
			MatchTicketModesKey,
		}, ticket.TicketID, ticket.PlayerID, ticket.MatchMode, MatchStatusQueued, ticket.MMRScore, ticket.CreatedAt).Result()
		if err != nil {
			return err
		}
	}
	return nil
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
			MatchTicketPoolKeyForMode(ticket.MatchMode),
			MatchTicketExpiryKey,
			MatchTicketModesKey,
		},
		ticketID,
		now,
		MatchStatusQueued,
		MatchStatusTimeout,
		ticket.MatchMode,
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
		MatchID:    values["match_id"],
		RoomID:     values["room_id"],
		ServerID:   values["server_id"],
		ServerAddr: values["server_addr"],
		MatchMode:  values["match_mode"],
		JoinToken:  values["join_token"],
		Status:     values["status"],
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
