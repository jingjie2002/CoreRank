package repository

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/redis/go-redis/v9"
)

const (
	MatchSettlementStatusSettled = "settled"
	matchSettlementPrefix        = "match:settlement:"
	matchSettlementTTL           = 24 * time.Hour
)

var (
	ErrSettlementConflict = errors.New("match was already settled with a different payload")
	ErrMatchNotSettleable = errors.New("match is not settleable")
)

type SettlementScore struct {
	PlayerID string
	Score    int64
}

type MatchSettlement struct {
	MatchID         string
	LeaderboardType string
	Fingerprint     string
	Scores          []SettlementScore
	SettledAt       int64
}

// SettleMatch writes every score, records the idempotency fingerprint and
// releases the room reservation in one Redis script.
func (r *PlayerRepository) SettleMatch(ctx context.Context, settlement MatchSettlement) (bool, error) {
	assignmentValues, err := r.client.HGetAll(ctx, roomAssignmentKey(settlement.MatchID)).Result()
	if err != nil {
		return false, err
	}
	serverID := assignmentValues["server_id"]
	matchMode := assignmentValues["match_mode"]
	if matchMode == "" {
		matchMode = DefaultMatchMode
	}

	serverInfoKey := "match:settlement:no-server-info"
	serverLoadKey := "match:settlement:no-server-load"
	if serverID != "" {
		serverInfoKey = roomServerInfoKey(serverID)
		serverLoadKey = roomServerLoadKey(matchMode)
	}

	args := make([]interface{}, 0, 7+len(settlement.Scores)*2)
	args = append(args,
		settlement.Fingerprint,
		settlement.LeaderboardType,
		settlement.SettledAt,
		matchSettlementTTL.Milliseconds(),
		MatchStatusMatched,
		MatchSettlementStatusSettled,
		len(settlement.Scores),
	)
	for _, score := range settlement.Scores {
		args = append(args, score.PlayerID, score.Score)
	}

	result, err := SettleMatchScript.Run(ctx, r.client, []string{
		matchResultKey(settlement.MatchID),
		LeaderboardKey(settlement.LeaderboardType),
		matchSettlementKey(settlement.MatchID),
		roomAssignmentKey(settlement.MatchID),
		serverInfoKey,
		serverLoadKey,
		RoomAssignmentExpiryKey,
	}, args...).Result()
	if err != nil {
		return false, err
	}
	code := scriptInt(result)
	switch code {
	case 1:
		return false, nil
	case 2:
		return true, nil
	case -1:
		return false, ErrResultNotFound
	case -2:
		return false, ErrSettlementConflict
	case -3:
		return false, ErrMatchNotSettleable
	default:
		return false, fmt.Errorf("unexpected settlement script result: %v", result)
	}
}

func matchSettlementKey(matchID string) string {
	return matchSettlementPrefix + matchID
}

var SettleMatchScript = redis.NewScript(`
local result_key = KEYS[1]
local leaderboard_key = KEYS[2]
local settlement_key = KEYS[3]
local assignment_key = KEYS[4]
local server_info_key = KEYS[5]
local server_load_key = KEYS[6]
local assignment_expiry_key = KEYS[7]

local fingerprint = ARGV[1]
local leaderboard_type = ARGV[2]
local settled_at = tonumber(ARGV[3])
local ttl_ms = tonumber(ARGV[4])
local matched_status = ARGV[5]
local settled_status = ARGV[6]
local score_count = tonumber(ARGV[7])

local existing_fingerprint = redis.call('HGET', settlement_key, 'fingerprint')
if existing_fingerprint then
    if existing_fingerprint == fingerprint then
        return 2
    end
    return -2
end

if redis.call('EXISTS', result_key) == 0 then
    return -1
end
if redis.call('HGET', result_key, 'status') ~= matched_status then
    return -3
end

local arg_index = 8
for i = 1, score_count do
    local player_id = ARGV[arg_index]
    local score = tonumber(ARGV[arg_index + 1])
    redis.call('ZADD', leaderboard_key, score, player_id)
    arg_index = arg_index + 2
end

redis.call('HSET', settlement_key,
    'fingerprint', fingerprint,
    'leaderboard_type', leaderboard_type,
    'status', settled_status,
    'settled_at', settled_at)
redis.call('PEXPIRE', settlement_key, ttl_ms)
redis.call('HSET', result_key,
    'settlement_status', settled_status,
    'settled_at', settled_at)

if redis.call('EXISTS', assignment_key) == 1 and
   redis.call('HGET', assignment_key, 'status') == 'assigned' then
    local server_id = redis.call('HGET', assignment_key, 'server_id') or ''
    local match_mode = redis.call('HGET', assignment_key, 'match_mode') or 'default'
    if server_id ~= '' and redis.call('EXISTS', server_info_key) == 1 then
        local capacity = tonumber(redis.call('HGET', server_info_key, 'capacity') or '0')
        local current_load = tonumber(redis.call('HGET', server_info_key, 'current_load') or '0')
        local new_load = current_load - score_count
        if new_load < 0 then
            new_load = 0
        end
        redis.call('HSET', server_info_key, 'current_load', new_load, 'updated_at', settled_at)
        if capacity > 0 then
            redis.call('ZADD', server_load_key, new_load / capacity, server_id)
        end
    end
    redis.call('HSET', assignment_key, 'status', settled_status, 'settled_at', settled_at)
end
redis.call('ZREM', assignment_expiry_key, redis.call('HGET', result_key, 'match_id'))

return 1
`)
