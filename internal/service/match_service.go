package service

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"log"
	"time"

	"CoreRank/internal/metrics"
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
	playerRepo     *repository.PlayerRepository
	mysqlRepo      *repository.MySQLRepository
	roomServerRepo *repository.RoomServerRepository
	roomAllocator  RoomAllocator
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

func (s *MatchService) SetRoomServerRepository(roomServerRepo *repository.RoomServerRepository) {
	s.roomServerRepo = roomServerRepo
	if roomServerRepo != nil {
		s.roomAllocator = NewRedisRoomAllocator(roomServerRepo)
	}
}

func (s *MatchService) RegisterGameServer(ctx context.Context, server repository.GameServer) (*repository.GameServer, error) {
	if s.roomServerRepo == nil {
		return nil, errors.New("room server registry is not enabled")
	}
	return s.roomServerRepo.RegisterGameServer(ctx, server)
}

func (s *MatchService) HeartbeatGameServer(ctx context.Context, serverID string, heartbeat repository.GameServerHeartbeat) (*repository.GameServer, error) {
	if s.roomServerRepo == nil {
		return nil, errors.New("room server registry is not enabled")
	}
	return s.roomServerRepo.HeartbeatGameServer(ctx, serverID, heartbeat)
}

func (s *MatchService) ListGameServers(ctx context.Context, matchMode string) ([]repository.GameServer, error) {
	if s.roomServerRepo == nil {
		return nil, errors.New("room server registry is not enabled")
	}
	return s.roomServerRepo.ListGameServers(ctx, matchMode)
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
	defer s.refreshQueuedTicketGauge(ctx)
	metrics.RecordMatchTicketEvents(req.MatchMode, repository.MatchStatusQueued, 1)

	if s.mysqlRepo != nil {
		if err := s.mysqlRepo.UpsertMatchTicket(ctx, ticket); err != nil {
			log.Printf("[CoreRank] MySQL match ticket persist failed; continuing with Redis hot path: %v", err)
		}
	}

	_, err := s.TryCompleteMatch(ctx, req.MMRScore, req.MatchMode)
	if err != nil {
		if errors.Is(err, repository.ErrNoAvailableRoomServer) {
			log.Printf("[CoreRank] room server allocation unavailable; ticket stays queued: %v", err)
		} else {
			return nil, err
		}
	}

	latest, err := s.playerRepo.GetMatchTicket(ctx, ticket.TicketID)
	if err != nil {
		return nil, err
	}
	if s.mysqlRepo != nil {
		if err := s.mysqlRepo.UpsertMatchTicket(ctx, *latest); err != nil {
			log.Printf("[CoreRank] MySQL match ticket refresh persist failed; returning Redis ticket result: %v", err)
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
			log.Printf("[CoreRank] MySQL match ticket cancellation persist failed; returning Redis ticket result: %v", err)
		}
	}
	metrics.RecordMatchCancelled(ticket.MatchMode)
	metrics.RecordMatchTicketEvents(ticket.MatchMode, ticket.Status, 1)
	metrics.ObserveMatchLifecycle(ticket.MatchMode, ticket.Status, ticket.CreatedAt, ticket.UpdatedAt)
	s.refreshQueuedTicketGauge(ctx)
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
				log.Printf("[CoreRank] MySQL match ticket timeout persist failed; returning Redis timeout result: %v", err)
			}
		}
	}
	for _, ticket := range tickets {
		metrics.RecordMatchTimeout(ticket.MatchMode)
		metrics.RecordMatchTicketEvents(ticket.MatchMode, ticket.Status, 1)
		metrics.ObserveMatchLifecycle(ticket.MatchMode, ticket.Status, ticket.CreatedAt, ticket.UpdatedAt)
	}
	if len(tickets) > 0 {
		s.refreshQueuedTicketGauge(ctx)
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
	return s.completePickedPlayers(ctx, players, matchMode, true)
}

func (s *MatchService) CompletePickedPlayers(ctx context.Context, players []string, matchMode string) (*repository.MatchResult, error) {
	return s.completePickedPlayers(ctx, players, matchMode, true)
}

func (s *MatchService) completePickedPlayers(ctx context.Context, players []string, matchMode string, requeueOnFailure bool) (*repository.MatchResult, error) {
	if len(players) < matchPlayersPerRoom {
		return nil, nil
	}
	if matchMode == "" {
		matchMode = defaultMatchMode
	}

	now := time.Now().UnixMilli()
	matchID := newID("match")
	assignment, err := s.roomAllocator.AllocateRoom(ctx, RoomAllocationRequest{
		MatchID:   matchID,
		MatchMode: matchMode,
		PlayerIDs: append([]string(nil), players...),
	})
	if err != nil {
		metrics.RecordRoomAssignment(matchMode, "failed")
		metrics.RecordRoomAssignmentFailure(matchMode, "allocator")
		if requeueOnFailure {
			if requeueErr := s.playerRepo.RequeueMatchTicketPlayers(ctx, players); requeueErr != nil {
				return nil, errors.Join(err, requeueErr)
			}
			s.refreshQueuedTicketGauge(ctx)
		}
		return nil, err
	}
	if assignment.MatchID == "" {
		assignment.MatchID = matchID
	}
	if assignment.RoomID == "" {
		assignment.RoomID = newID("room")
	}

	result := repository.MatchResult{
		MatchID:    assignment.MatchID,
		RoomID:     assignment.RoomID,
		ServerID:   assignment.ServerID,
		ServerAddr: assignment.ServerAddr,
		MatchMode:  matchMode,
		Status:     repository.MatchStatusMatched,
		CreatedAt:  now,
	}
	completed, tickets, err := s.playerRepo.CompleteMatch(ctx, players, result)
	if err != nil || completed == nil {
		releaseErr := s.roomAllocator.ReleaseRoom(ctx, assignment)
		var requeueErr error
		if requeueOnFailure {
			requeueErr = s.playerRepo.RequeueMatchTicketPlayers(ctx, players)
			s.refreshQueuedTicketGauge(ctx)
		}
		return completed, errors.Join(err, releaseErr, requeueErr)
	}
	assignment.MatchID = completed.MatchID
	assignment.RoomID = completed.RoomID
	assignment.MatchMode = completed.MatchMode
	assignment.PlayerIDs = append([]string(nil), completed.PlayerIDs...)
	assignment.Status = repository.RoomAssignmentStatusAssigned
	assignment.CreatedAt = completed.CreatedAt
	if err := s.roomAllocator.SaveAssignment(ctx, assignment); err != nil {
		log.Printf("[CoreRank] room assignment persist failed; returning match result: %v", err)
	}
	if s.mysqlRepo != nil {
		if err := s.mysqlRepo.UpsertMatchResult(ctx, *completed); err != nil {
			log.Printf("[CoreRank] MySQL match result persist failed; returning Redis match result: %v", err)
		}
		for _, ticket := range tickets {
			if err := s.mysqlRepo.UpsertMatchTicket(ctx, *ticket); err != nil {
				log.Printf("[CoreRank] MySQL matched ticket persist failed; returning Redis match result: %v", err)
			}
		}
	}
	metrics.RecordMatchSuccess(matchMode)
	metrics.RecordRoomAssignment(matchMode, repository.RoomAssignmentStatusAssigned)
	if assignment.ServerID != "" {
		metrics.SetRoomServerLoad(assignment.ServerID, matchMode, assignment.CurrentLoad)
	}
	metrics.RecordMatchTicketEvents(matchMode, repository.MatchStatusMatched, len(tickets))
	for _, ticket := range tickets {
		metrics.ObserveMatchLifecycle(ticket.MatchMode, ticket.Status, ticket.CreatedAt, ticket.UpdatedAt)
	}
	s.refreshQueuedTicketGauge(ctx)
	return completed, nil
}

func (s *MatchService) refreshQueuedTicketGauge(ctx context.Context) {
	count, err := s.playerRepo.CountQueuedMatchTickets(ctx)
	if err != nil {
		log.Printf("[CoreRank] refresh queued ticket metric failed: %v", err)
		return
	}
	metrics.SetQueuedTickets("all", count)
}

func newID(prefix string) string {
	var raw [8]byte
	if _, err := rand.Read(raw[:]); err == nil {
		return fmt.Sprintf("%s_%s", prefix, hex.EncodeToString(raw[:]))
	}
	return fmt.Sprintf("%s_%d", prefix, time.Now().UnixNano())
}
