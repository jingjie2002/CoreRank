package service

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"log"
	"math"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/redis/go-redis/v9"

	"github.com/jingjie2002/CoreRank/internal/repository"
)

// PlayerInfo 玩家信息
type PlayerInfo struct {
	PlayerID string  `json:"player_id"`
	Score    float64 `json:"score"`
	Rank     int64   `json:"rank"`
}

// RankService 排行榜服务
type RankService struct {
	playerRepo *repository.PlayerRepository
	mysqlRepo  *repository.MySQLRepository
}

var ErrInvalidLeaderboardType = errors.New("invalid leaderboard_type")

var (
	ErrInvalidSettlementPlayers  = errors.New("settlement players must exactly match the players in the match result")
	ErrDuplicateSettlementPlayer = errors.New("settlement contains a duplicate player_id")
	ErrInvalidSettlementScore    = errors.New("settlement score must be between -1000000000 and 1000000000")
	ErrInvalidRankScore          = errors.New("rank score must be a finite number between -1000000000 and 1000000000")
	ErrInvalidRankPage           = errors.New("top_n must be between 1 and 100 and offset must be between 0 and 10000")
)

type MatchSettlementResult struct {
	MatchID         string
	LeaderboardType string
	Players         []PlayerInfo
	Idempotent      bool
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

func (s *RankService) SettleMatch(ctx context.Context, matchID, leaderboardType string, scores []repository.SettlementScore) (*MatchSettlementResult, error) {
	if err := repository.ValidateIdentifier("match_id", matchID, 96); err != nil {
		return nil, err
	}
	board, err := NormalizeLeaderboardType(leaderboardType)
	if err != nil {
		return nil, err
	}
	matchResult, err := s.playerRepo.GetMatchResult(ctx, matchID)
	if err != nil {
		return nil, err
	}
	if matchResult.Status != repository.MatchStatusMatched {
		return nil, repository.ErrMatchNotSettleable
	}

	expected := make(map[string]struct{}, len(matchResult.PlayerIDs))
	for _, playerID := range matchResult.PlayerIDs {
		expected[playerID] = struct{}{}
	}
	if len(scores) != len(expected) || len(scores) == 0 {
		return nil, ErrInvalidSettlementPlayers
	}
	seen := make(map[string]struct{}, len(scores))
	for _, score := range scores {
		if err := repository.ValidateIdentifier("player_id", score.PlayerID, 64); err != nil {
			return nil, err
		}
		if _, duplicate := seen[score.PlayerID]; duplicate {
			return nil, ErrDuplicateSettlementPlayer
		}
		if _, participant := expected[score.PlayerID]; !participant {
			return nil, ErrInvalidSettlementPlayers
		}
		if score.Score < -1_000_000_000 || score.Score > 1_000_000_000 {
			return nil, ErrInvalidSettlementScore
		}
		seen[score.PlayerID] = struct{}{}
	}

	fingerprint := settlementFingerprint(matchID, board, scores)
	idempotent, err := s.playerRepo.SettleMatch(ctx, repository.MatchSettlement{
		MatchID:         matchID,
		LeaderboardType: board,
		Fingerprint:     fingerprint,
		Scores:          append([]repository.SettlementScore(nil), scores...),
		SettledAt:       time.Now().UnixMilli(),
	})
	if err != nil {
		return nil, err
	}

	players := make([]PlayerInfo, 0, len(scores))
	for _, score := range scores {
		player, err := s.GetPlayerRankInLeaderboard(ctx, board, score.PlayerID)
		if err != nil {
			return nil, err
		}
		if player != nil {
			players = append(players, *player)
		}
		if !idempotent && s.mysqlRepo != nil && board == repository.GlobalLeaderboardType {
			if err := s.mysqlRepo.UpsertPlayerScore(ctx, score.PlayerID, float64(score.Score)); err != nil {
				log.Printf("[CoreRank] MySQL settlement score persist failed; Redis settlement remains authoritative: %v", err)
			}
		}
	}

	return &MatchSettlementResult{
		MatchID:         matchID,
		LeaderboardType: board,
		Players:         players,
		Idempotent:      idempotent,
	}, nil
}

func settlementFingerprint(matchID, leaderboardType string, scores []repository.SettlementScore) string {
	canonical := append([]repository.SettlementScore(nil), scores...)
	sort.Slice(canonical, func(i, j int) bool { return canonical[i].PlayerID < canonical[j].PlayerID })
	var builder strings.Builder
	builder.WriteString(matchID)
	builder.WriteByte('\n')
	builder.WriteString(leaderboardType)
	for _, score := range canonical {
		builder.WriteByte('\n')
		builder.WriteString(score.PlayerID)
		builder.WriteByte('=')
		builder.WriteString(strconv.FormatInt(score.Score, 10))
	}
	sum := sha256.Sum256([]byte(builder.String()))
	return hex.EncodeToString(sum[:])
}

// UpdatePlayerScore 更新玩家分数到排行榜
func (s *RankService) UpdatePlayerScore(ctx context.Context, playerID string, score float64) error {
	return s.UpdatePlayerScoreInLeaderboard(ctx, repository.GlobalLeaderboardType, playerID, score)
}

// UpdatePlayerScoreInLeaderboard 更新玩家分数到指定排行榜维度。
func (s *RankService) UpdatePlayerScoreInLeaderboard(ctx context.Context, leaderboardType string, playerID string, score float64) error {
	if err := repository.ValidateIdentifier("player_id", playerID, 64); err != nil {
		return err
	}
	if math.IsNaN(score) || math.IsInf(score, 0) || score < -1_000_000_000 || score > 1_000_000_000 {
		return ErrInvalidRankScore
	}
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
	return s.GetTopPlayersPage(ctx, leaderboardType, topN, 0)
}

func (s *RankService) GetTopPlayersPage(ctx context.Context, leaderboardType string, topN, offset int64) ([]PlayerInfo, error) {
	leaderboardType, err := NormalizeLeaderboardType(leaderboardType)
	if err != nil {
		return nil, err
	}
	if topN <= 0 {
		topN = 10
	}
	if topN > 100 || offset < 0 || offset > 10000 {
		return nil, ErrInvalidRankPage
	}

	results, err := s.playerRepo.GetLeaderboardRankPage(ctx, leaderboardType, topN, offset)
	if err != nil {
		return nil, err
	}

	players := make([]PlayerInfo, 0, len(results))
	for i, z := range results {
		players = append(players, PlayerInfo{
			PlayerID: z.Member.(string),
			Score:    z.Score,
			Rank:     offset + int64(i) + 1,
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
	if err := repository.ValidateIdentifier("player_id", playerID, 64); err != nil {
		return nil, err
	}
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

func (s *RankService) CountPlayersInLeaderboard(ctx context.Context, leaderboardType string) (int64, error) {
	board, err := NormalizeLeaderboardType(leaderboardType)
	if err != nil {
		return 0, err
	}
	return s.playerRepo.CountLeaderboardPlayers(ctx, board)
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
