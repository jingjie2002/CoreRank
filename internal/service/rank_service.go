package service

import (
	"context"
	"errors"
	"fmt"
	"log"
	"strings"
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

var ErrInvalidLeaderboardType = errors.New("invalid leaderboard_type")

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
	return s.UpdatePlayerScoreInLeaderboard(ctx, repository.GlobalLeaderboardType, playerID, score)
}

// UpdatePlayerScoreInLeaderboard 更新玩家分数到指定排行榜维度。
func (s *RankService) UpdatePlayerScoreInLeaderboard(ctx context.Context, leaderboardType string, playerID string, score float64) error {
	leaderboardType, err := NormalizeLeaderboardType(leaderboardType)
	if err != nil {
		return err
	}
	if err := s.playerRepo.UpdatePlayerScoreInLeaderboard(ctx, leaderboardType, playerID, score); err != nil {
		return err
	}
	if s.mysqlRepo != nil && leaderboardType == repository.GlobalLeaderboardType {
		if err := s.mysqlRepo.UpsertPlayerScore(ctx, playerID, score); err != nil {
			log.Printf("[CoreRank] MySQL player score persist failed; continuing with Redis hot path: %v", err)
		}
	}
	return nil
}

// GetTopPlayers 获取排行榜前 N 名玩家
func (s *RankService) GetTopPlayers(ctx context.Context, topN int64) ([]PlayerInfo, error) {
	return s.GetTopPlayersInLeaderboard(ctx, repository.GlobalLeaderboardType, topN)
}

// GetTopPlayersInLeaderboard 获取指定排行榜维度前 N 名玩家。
func (s *RankService) GetTopPlayersInLeaderboard(ctx context.Context, leaderboardType string, topN int64) ([]PlayerInfo, error) {
	leaderboardType, err := NormalizeLeaderboardType(leaderboardType)
	if err != nil {
		return nil, err
	}
	if topN <= 0 {
		topN = 10
	}

	results, err := s.playerRepo.GetLeaderboardRank(ctx, leaderboardType, topN)
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

	if s.mysqlRepo != nil && leaderboardType == repository.GlobalLeaderboardType {
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
	return s.GetPlayerRankInLeaderboard(ctx, repository.GlobalLeaderboardType, playerID)
}

// GetPlayerRankInLeaderboard 获取指定玩家在某个排行榜维度中的排名信息。
func (s *RankService) GetPlayerRankInLeaderboard(ctx context.Context, leaderboardType string, playerID string) (*PlayerInfo, error) {
	leaderboardType, err := NormalizeLeaderboardType(leaderboardType)
	if err != nil {
		return nil, err
	}

	rank, err := s.playerRepo.GetPlayerRankInLeaderboard(ctx, leaderboardType, playerID)
	if err != nil {
		if err == redis.Nil {
			return nil, nil // 玩家不在排行榜中
		}
		return nil, err
	}

	score, err := s.playerRepo.GetPlayerScoreInLeaderboard(ctx, leaderboardType, playerID)
	if err != nil {
		return nil, err
	}

	return &PlayerInfo{
		PlayerID: playerID,
		Score:    score,
		Rank:     rank + 1, // 转换为 1-based 排名
	}, nil
}

// NormalizeLeaderboardType validates a lightweight board/scope identifier.
func NormalizeLeaderboardType(leaderboardType string) (string, error) {
	value := strings.ToLower(strings.TrimSpace(leaderboardType))
	if value == "" {
		return repository.GlobalLeaderboardType, nil
	}
	if len(value) > 64 {
		return "", fmt.Errorf("%w: length must be <= 64", ErrInvalidLeaderboardType)
	}
	for _, r := range value {
		if (r >= 'a' && r <= 'z') || (r >= '0' && r <= '9') || r == '_' || r == '-' || r == ':' {
			continue
		}
		return "", fmt.Errorf("%w: only letters, digits, underscore, hyphen and colon are allowed", ErrInvalidLeaderboardType)
	}
	return value, nil
}
