package service

import "context"

type RoomAllocationRequest struct {
	MatchMode string
	PlayerIDs []string
}

type RoomAllocator interface {
	AllocateRoom(ctx context.Context, req RoomAllocationRequest) string
}

type IDRoomAllocator struct{}

func NewIDRoomAllocator() IDRoomAllocator {
	return IDRoomAllocator{}
}

func (IDRoomAllocator) AllocateRoom(context.Context, RoomAllocationRequest) string {
	return newID("room")
}
