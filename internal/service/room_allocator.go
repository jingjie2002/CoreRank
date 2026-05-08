package service

import (
	"context"
	"time"

	"CoreRank/internal/repository"
)

type RoomAllocationRequest struct {
	MatchID   string
	MatchMode string
	PlayerIDs []string
}

type RoomAllocator interface {
	AllocateRoom(ctx context.Context, req RoomAllocationRequest) (repository.RoomAssignment, error)
	ReleaseRoom(ctx context.Context, assignment repository.RoomAssignment) error
	SaveAssignment(ctx context.Context, assignment repository.RoomAssignment) error
}

type IDRoomAllocator struct{}

func NewIDRoomAllocator() IDRoomAllocator {
	return IDRoomAllocator{}
}

func (IDRoomAllocator) AllocateRoom(_ context.Context, req RoomAllocationRequest) (repository.RoomAssignment, error) {
	now := time.Now().UnixMilli()
	return repository.RoomAssignment{
		MatchID:   req.MatchID,
		RoomID:    newID("room"),
		MatchMode: req.MatchMode,
		PlayerIDs: append([]string(nil), req.PlayerIDs...),
		Status:    repository.RoomAssignmentStatusAssigned,
		CreatedAt: now,
	}, nil
}

func (IDRoomAllocator) ReleaseRoom(context.Context, repository.RoomAssignment) error {
	return nil
}

func (IDRoomAllocator) SaveAssignment(context.Context, repository.RoomAssignment) error {
	return nil
}

type RedisRoomAllocator struct {
	repo             *repository.RoomServerRepository
	heartbeatTimeout time.Duration
}

func NewRedisRoomAllocator(repo *repository.RoomServerRepository) RedisRoomAllocator {
	return RedisRoomAllocator{
		repo:             repo,
		heartbeatTimeout: 30 * time.Second,
	}
}

func (a RedisRoomAllocator) AllocateRoom(ctx context.Context, req RoomAllocationRequest) (repository.RoomAssignment, error) {
	if req.MatchMode == "" {
		req.MatchMode = defaultMatchMode
	}
	assignment, err := a.repo.AllocateRoomServer(ctx, repository.RoomServerAllocationRequest{
		MatchID:          req.MatchID,
		RoomID:           newID("room"),
		MatchMode:        req.MatchMode,
		PlayerIDs:        append([]string(nil), req.PlayerIDs...),
		HeartbeatTimeout: a.heartbeatTimeout,
	})
	if err != nil {
		return repository.RoomAssignment{}, err
	}
	return *assignment, nil
}

func (a RedisRoomAllocator) ReleaseRoom(ctx context.Context, assignment repository.RoomAssignment) error {
	return a.repo.ReleaseRoomServer(ctx, assignment)
}

func (a RedisRoomAllocator) SaveAssignment(ctx context.Context, assignment repository.RoomAssignment) error {
	return a.repo.SaveRoomAssignment(ctx, assignment)
}
