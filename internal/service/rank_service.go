package service

import (
	"context"
	"log"
	"time"

	"github.com/redis/go-redis/v9"

	"CoreRank/internal/repository"
)

// PlayerInfo 玩家信息
type PlayerInfo struct {
	PlayerID string
	Score    float64
	Rank     int64
}

// RankService 排行榜服务
type RankService struct {
	playerRepo *repository.PlayerRepository
	mysqlRepo  *repository.MySQLRepository
}

// NewRankService 创建 RankService 实例
func NewRankService(playerRepo *repository.PlayerRepository) *RankService {
	return &RankService{
		playerRepo: playerRepo,
	}
}

func (s *RankService) SetMySQLRepository(mysqlRepo *repository.MySQLRepository) {
	s.mysqlRepo = mysqlRepo
}

// UpdatePlayerScore 更新玩家分数到排行榜
func (s *RankService) UpdatePlayerScore(ctx context.Context, playerID string, score float64) error {
	if err := s.playerRepo.UpdatePlayerScore(ctx, playerID, score); err != nil {
		return err
	}
	if s.mysqlRepo != nil {
		if err := s.mysqlRepo.UpsertPlayerScore(ctx, playerID, score); err != nil {
			log.Printf("[CoreRank] MySQL player score persist failed; continuing with Redis hot path: %v", err)
		}
	}
	return nil
}

// GetTopPlayers 获取排行榜前 N 名玩家
func (s *RankService) GetTopPlayers(ctx context.Context, topN int64) ([]PlayerInfo, error) {
	if topN <= 0 {
		topN = 10
	}

	results, err := s.playerRepo.GetGlobalRank(ctx, topN)
	if err != nil {
		return nil, err
	}

	players := make([]PlayerInfo, 0, len(results))
	for i, z := range results {
		players = append(players, PlayerInfo{
			PlayerID: z.Member.(string),
			Score:    z.Score,
			Rank:     int64(i + 1),
		})
	}

	if s.mysqlRepo != nil {
		now := time.Now().UnixMilli()
		rows := make([]repository.RankSnapshotRow, 0, len(players))
		for _, player := range players {
			rows = append(rows, repository.RankSnapshotRow{
				PlayerID:     player.PlayerID,
				RankScore:    int64(player.Score),
				RankPosition: player.Rank,
				CapturedAtMS: now,
			})
		}
		if err := s.mysqlRepo.SaveRankSnapshot(ctx, rows); err != nil {
			log.Printf("[CoreRank] MySQL rank snapshot persist failed; returning Redis rank result: %v", err)
		}
	}

	return players, nil
}

// GetPlayerRank 获取指定玩家的排名信息
func (s *RankService) GetPlayerRank(ctx context.Context, playerID string) (*PlayerInfo, error) {
	rank, err := s.playerRepo.GetPlayerRank(ctx, playerID)
	if err != nil {
		if err == redis.Nil {
			return nil, nil // 玩家不在排行榜中
		}
		return nil, err
	}

	score, err := s.playerRepo.GetPlayerScore(ctx, playerID)
	if err != nil {
		return nil, err
	}

	return &PlayerInfo{
		PlayerID: playerID,
		Score:    score,
		Rank:     rank + 1, // 转换为 1-based 排名
	}, nil
}
