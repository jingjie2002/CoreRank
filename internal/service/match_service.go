package service

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"time"

	"CoreRank/internal/repository"
)

const (
	defaultMatchMode    = "default"
	defaultMaxWait      = 30 * time.Second
	matchPlayersPerRoom = 2
	defaultMatchDelta   = 200
)

type CreateMatchTicketRequest struct {
	PlayerID  string
	MMRScore  int64
	MatchMode string
	MaxWait   time.Duration
}

type MatchService struct {
	playerRepo    *repository.PlayerRepository
	mysqlRepo     *repository.MySQLRepository
	roomAllocator RoomAllocator
}

func NewMatchService(playerRepo *repository.PlayerRepository) *MatchService {
	return &MatchService{
		playerRepo:    playerRepo,
		roomAllocator: NewIDRoomAllocator(),
	}
}

func (s *MatchService) SetMySQLRepository(mysqlRepo *repository.MySQLRepository) {
	s.mysqlRepo = mysqlRepo
}

func (s *MatchService) SetRoomAllocator(roomAllocator RoomAllocator) {
	if roomAllocator == nil {
		s.roomAllocator = NewIDRoomAllocator()
		return
	}
	s.roomAllocator = roomAllocator
}

func (s *MatchService) CreateTicket(ctx context.Context, req CreateMatchTicketRequest) (*repository.MatchTicket, error) {
	if req.PlayerID == "" {
		return nil, errors.New("player_id is required")
	}
	if req.MatchMode == "" {
		req.MatchMode = defaultMatchMode
	}
	if req.MaxWait <= 0 {
		req.MaxWait = defaultMaxWait
	}

	now := time.Now()
	ticket := repository.MatchTicket{
		TicketID:  newID("ticket"),
		PlayerID:  req.PlayerID,
		MMRScore:  req.MMRScore,
		MatchMode: req.MatchMode,
		Status:    repository.MatchStatusQueued,
		CreatedAt: now.UnixMilli(),
		UpdatedAt: now.UnixMilli(),
		ExpiresAt: now.Add(req.MaxWait).UnixMilli(),
	}

	if err := s.playerRepo.CreateMatchTicket(ctx, ticket, req.MaxWait+5*time.Minute); err != nil {
		return nil, err
	}
	if s.mysqlRepo != nil {
		if err := s.mysqlRepo.UpsertMatchTicket(ctx, ticket); err != nil {
			return nil, err
		}
	}

	_, err := s.TryCompleteMatch(ctx, req.MMRScore, req.MatchMode)
	if err != nil {
		return nil, err
	}

	latest, err := s.playerRepo.GetMatchTicket(ctx, ticket.TicketID)
	if err != nil {
		return nil, err
	}
	if s.mysqlRepo != nil {
		if err := s.mysqlRepo.UpsertMatchTicket(ctx, *latest); err != nil {
			return nil, err
		}
	}
	return latest, nil
}

func (s *MatchService) GetTicket(ctx context.Context, ticketID string) (*repository.MatchTicket, error) {
	if ticketID == "" {
		return nil, repository.ErrTicketNotFound
	}
	return s.playerRepo.GetMatchTicket(ctx, ticketID)
}

func (s *MatchService) CancelTicket(ctx context.Context, ticketID string) (*repository.MatchTicket, error) {
	if ticketID == "" {
		return nil, repository.ErrTicketNotFound
	}
	ticket, err := s.playerRepo.CancelMatchTicket(ctx, ticketID, time.Now().UnixMilli())
	if err != nil {
		return nil, err
	}
	if s.mysqlRepo != nil {
		if err := s.mysqlRepo.UpsertMatchTicket(ctx, *ticket); err != nil {
			return nil, err
		}
	}
	return ticket, nil
}

func (s *MatchService) TimeoutExpiredTickets(ctx context.Context, now time.Time, limit int64) ([]*repository.MatchTicket, error) {
	if now.IsZero() {
		now = time.Now()
	}
	tickets, err := s.playerRepo.TimeoutExpiredMatchTickets(ctx, now.UnixMilli(), limit)
	if err != nil {
		return nil, err
	}
	if s.mysqlRepo != nil {
		for _, ticket := range tickets {
			if err := s.mysqlRepo.UpsertMatchTicket(ctx, *ticket); err != nil {
				return nil, err
			}
		}
	}
	return tickets, nil
}

func (s *MatchService) GetResult(ctx context.Context, matchID string) (*repository.MatchResult, error) {
	if matchID == "" {
		return nil, repository.ErrResultNotFound
	}
	return s.playerRepo.GetMatchResult(ctx, matchID)
}

func (s *MatchService) TryCompleteMatch(ctx context.Context, centerScore int64, matchMode string) (*repository.MatchResult, error) {
	players, err := s.playerRepo.SearchAndPickTicketPlayers(
		ctx,
		centerScore-defaultMatchDelta,
		centerScore+defaultMatchDelta,
		matchPlayersPerRoom,
	)
	if err != nil {
		return nil, err
	}
	return s.CompletePickedPlayers(ctx, players, matchMode)
}

func (s *MatchService) CompletePickedPlayers(ctx context.Context, players []string, matchMode string) (*repository.MatchResult, error) {
	if len(players) < matchPlayersPerRoom {
		return nil, nil
	}
	if matchMode == "" {
		matchMode = defaultMatchMode
	}

	now := time.Now().UnixMilli()
	roomID := s.roomAllocator.AllocateRoom(ctx, RoomAllocationRequest{
		MatchMode: matchMode,
		PlayerIDs: append([]string(nil), players...),
	})

	result := repository.MatchResult{
		MatchID:   newID("match"),
		RoomID:    roomID,
		MatchMode: matchMode,
		Status:    repository.MatchStatusMatched,
		CreatedAt: now,
	}
	completed, tickets, err := s.playerRepo.CompleteMatch(ctx, players, result)
	if err != nil || completed == nil {
		return completed, err
	}
	if s.mysqlRepo != nil {
		if err := s.mysqlRepo.UpsertMatchResult(ctx, *completed); err != nil {
			return nil, err
		}
		for _, ticket := range tickets {
			if err := s.mysqlRepo.UpsertMatchTicket(ctx, *ticket); err != nil {
				return nil, err
			}
		}
	}
	return completed, nil
}

func newID(prefix string) string {
	var raw [8]byte
	if _, err := rand.Read(raw[:]); err == nil {
		return fmt.Sprintf("%s_%s", prefix, hex.EncodeToString(raw[:]))
	}
	return fmt.Sprintf("%s_%d", prefix, time.Now().UnixNano())
}
