package repository

import (
	"context"
	"time"

	"github.com/redis/go-redis/v9"
)

const (
	// MatchPoolKey 匹配池的 Redis Key
	MatchPoolKey = "{match:pool}"
	// MatchTicketPoolKey 匹配生命周期票据使用的 Redis Key
	MatchTicketPoolKey = "{match:ticket_pool}"
	// GlobalRankKey 全局排行榜的 Redis Key
	GlobalRankKey = "{rank:global}"
)

// PlayerRepository 玩家数据仓库
type PlayerRepository struct {
	client *redis.Client
}

// NewPlayerRepository 创建 PlayerRepository 实例
func NewPlayerRepository(client *redis.Client) *PlayerRepository {
	return &PlayerRepository{
		client: client,
	}
}

// AddPlayerToPool 将玩家添加到匹配池
// 使用 CompositeScoreScript 计算复合分数，确保先入队的玩家优先匹配
func (r *PlayerRepository) AddPlayerToPool(ctx context.Context, playerID string, score int64) error {
	return r.addPlayerToPool(ctx, MatchPoolKey, playerID, score)
}

func (r *PlayerRepository) addPlayerToPool(ctx context.Context, key string, playerID string, score int64) error {
	timestamp := time.Now().UnixMilli()

	_, err := CompositeScoreScript.Run(ctx, r.client,
		[]string{key},
		playerID,
		score,
		timestamp,
	).Result()

	return err
}

// SearchAndPickPlayers 原子化地从匹配池中查询、提取并删除玩家
// 返回匹配到的玩家ID列表
func (r *PlayerRepository) SearchAndPickPlayers(ctx context.Context, minScore, maxScore int64, count int) ([]string, error) {
	return r.searchAndPickPlayers(ctx, MatchPoolKey, minScore, maxScore, count)
}

func (r *PlayerRepository) SearchAndPickTicketPlayers(ctx context.Context, minScore, maxScore int64, count int) ([]string, error) {
	return r.searchAndPickPlayers(ctx, MatchTicketPoolKey, minScore, maxScore, count)
}

func (r *PlayerRepository) searchAndPickPlayers(ctx context.Context, key string, minScore, maxScore int64, count int) ([]string, error) {
	// 计算中心分数和差值范围
	centerScore := (minScore + maxScore) / 2
	delta := (maxScore - minScore) / 2

	result, err := AtomicMatchScript.Run(ctx, r.client,
		[]string{key},
		centerScore,
		delta,
		count,
	).Result()

	if err != nil {
		return nil, err
	}

	// 将结果转换为字符串切片
	members, ok := result.([]interface{})
	if !ok {
		return []string{}, nil
	}

	players := make([]string, 0, len(members))
	for _, m := range members {
		if playerID, ok := m.(string); ok {
			players = append(players, playerID)
		}
	}

	return players, nil
}

// GetGlobalRank 获取全局排行榜前 N 名
// 返回玩家ID和分数的列表，按分数从高到低排序
func (r *PlayerRepository) GetGlobalRank(ctx context.Context, topN int64) ([]redis.Z, error) {
	return r.client.ZRevRangeWithScores(ctx, GlobalRankKey, 0, topN-1).Result()
}

// UpdatePlayerScore 更新玩家在全局排行榜中的分数
func (r *PlayerRepository) UpdatePlayerScore(ctx context.Context, playerID string, score float64) error {
	return r.client.ZAdd(ctx, GlobalRankKey, redis.Z{
		Score:  score,
		Member: playerID,
	}).Err()
}

// RemovePlayerFromPool 从匹配池中移除玩家
func (r *PlayerRepository) RemovePlayerFromPool(ctx context.Context, playerID string) error {
	return r.client.ZRem(ctx, MatchPoolKey, playerID).Err()
}

// GetPlayerRank 获取玩家在全局排行榜中的排名 (0-based, 分数从高到低)
func (r *PlayerRepository) GetPlayerRank(ctx context.Context, playerID string) (int64, error) {
	return r.client.ZRevRank(ctx, GlobalRankKey, playerID).Result()
}

// GetPlayerScore 获取玩家在全局排行榜中的分数
func (r *PlayerRepository) GetPlayerScore(ctx context.Context, playerID string) (float64, error) {
	return r.client.ZScore(ctx, GlobalRankKey, playerID).Result()
}
