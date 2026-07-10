package repository

import (
	"context"
	"sort"
	"strings"
	"time"

	"github.com/redis/go-redis/v9"
)

const (
	MatchPoolKey          = "{match:pool}"
	MatchTicketPoolKey    = "{match:ticket_pool}" // legacy shared pool; new tickets do not use it
	MatchTicketModesKey   = "{match:ticket_modes}"
	MatchTicketExpiryKey  = "{match:ticket_expiry}"
	GlobalRankKey         = "{rank:global}"
	GlobalLeaderboardType = "global"
)

type PlayerRepository struct {
	client *redis.Client
}

func NewPlayerRepository(client *redis.Client) *PlayerRepository {
	return &PlayerRepository{client: client}
}

func (r *PlayerRepository) AddPlayerToPool(ctx context.Context, playerID string, score int64) error {
	return r.addPlayerToPool(ctx, MatchPoolKey, playerID, score)
}

func (r *PlayerRepository) addPlayerToPool(ctx context.Context, key, playerID string, score int64) error {
	_, err := CompositeScoreScript.Run(ctx, r.client, []string{key}, playerID, score, time.Now().UnixMilli()).Result()
	return err
}

func (r *PlayerRepository) SearchAndPickPlayers(ctx context.Context, minScore, maxScore int64, count int) ([]string, error) {
	return r.searchAndPickPlayers(ctx, MatchPoolKey, minScore, maxScore, count)
}

func (r *PlayerRepository) SearchAndPickTicketPlayers(ctx context.Context, matchMode string, minScore, maxScore int64, count int) ([]string, error) {
	return r.searchAndPickPlayers(ctx, MatchTicketPoolKeyForMode(matchMode), minScore, maxScore, count)
}

func (r *PlayerRepository) ListQueuedMatchModes(ctx context.Context) ([]string, error) {
	modes, err := r.client.SMembers(ctx, MatchTicketModesKey).Result()
	if err != nil {
		return nil, err
	}
	sort.Strings(modes)
	return modes, nil
}

func MatchTicketPoolKeyForMode(matchMode string) string {
	mode, err := NormalizeMatchMode(matchMode)
	if err != nil {
		mode = DefaultMatchMode
	}
	return "{match:ticket_pool:" + mode + "}"
}

func (r *PlayerRepository) searchAndPickPlayers(ctx context.Context, key string, minScore, maxScore int64, count int) ([]string, error) {
	result, err := AtomicMatchScript.Run(ctx, r.client, []string{key}, minScore, maxScore, count).Result()
	if err != nil {
		return nil, err
	}
	members, ok := result.([]interface{})
	if !ok {
		return []string{}, nil
	}
	players := make([]string, 0, len(members))
	for _, member := range members {
		if playerID, ok := member.(string); ok {
			players = append(players, playerID)
		}
	}
	return players, nil
}

func LeaderboardKey(leaderboardType string) string {
	leaderboardType = strings.TrimSpace(strings.ToLower(leaderboardType))
	if leaderboardType == "" || leaderboardType == GlobalLeaderboardType {
		return GlobalRankKey
	}
	return "{rank:" + leaderboardType + "}"
}

func (r *PlayerRepository) GetGlobalRank(ctx context.Context, topN int64) ([]redis.Z, error) {
	return r.GetLeaderboardRank(ctx, GlobalLeaderboardType, topN)
}

func (r *PlayerRepository) GetLeaderboardRank(ctx context.Context, leaderboardType string, topN int64) ([]redis.Z, error) {
	return r.GetLeaderboardRankPage(ctx, leaderboardType, topN, 0)
}

func (r *PlayerRepository) GetLeaderboardRankPage(ctx context.Context, leaderboardType string, topN, offset int64) ([]redis.Z, error) {
	return r.client.ZRevRangeWithScores(ctx, LeaderboardKey(leaderboardType), offset, offset+topN-1).Result()
}

func (r *PlayerRepository) CountLeaderboardPlayers(ctx context.Context, leaderboardType string) (int64, error) {
	return r.client.ZCard(ctx, LeaderboardKey(leaderboardType)).Result()
}

func (r *PlayerRepository) UpdatePlayerScore(ctx context.Context, playerID string, score float64) error {
	return r.UpdatePlayerScoreInLeaderboard(ctx, GlobalLeaderboardType, playerID, score)
}

func (r *PlayerRepository) UpdatePlayerScoreInLeaderboard(ctx context.Context, leaderboardType, playerID string, score float64) error {
	return r.client.ZAdd(ctx, LeaderboardKey(leaderboardType), redis.Z{Score: score, Member: playerID}).Err()
}

func (r *PlayerRepository) RemovePlayerFromPool(ctx context.Context, playerID string) error {
	return r.client.ZRem(ctx, MatchPoolKey, playerID).Err()
}

func (r *PlayerRepository) GetPlayerRank(ctx context.Context, playerID string) (int64, error) {
	return r.GetPlayerRankInLeaderboard(ctx, GlobalLeaderboardType, playerID)
}

func (r *PlayerRepository) GetPlayerRankInLeaderboard(ctx context.Context, leaderboardType, playerID string) (int64, error) {
	return r.client.ZRevRank(ctx, LeaderboardKey(leaderboardType), playerID).Result()
}

func (r *PlayerRepository) GetPlayerScore(ctx context.Context, playerID string) (float64, error) {
	return r.GetPlayerScoreInLeaderboard(ctx, GlobalLeaderboardType, playerID)
}

func (r *PlayerRepository) GetPlayerScoreInLeaderboard(ctx context.Context, leaderboardType, playerID string) (float64, error) {
	return r.client.ZScore(ctx, LeaderboardKey(leaderboardType), playerID).Result()
}
