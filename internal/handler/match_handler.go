package handler

import (
	"context"
	"errors"
	"time"

	pb "CoreRank/api/proto"
	"CoreRank/internal/repository"
	"CoreRank/internal/service"

	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

type MatchHandler struct {
	pb.UnimplementedMatchServiceServer

	matchService *service.MatchService
}

func NewMatchHandler(matchService *service.MatchService) *MatchHandler {
	return &MatchHandler{matchService: matchService}
}

func (h *MatchHandler) CreateMatchTicket(ctx context.Context, req *pb.CreateMatchTicketRequest) (*pb.CreateMatchTicketResponse, error) {
	ticket, err := h.matchService.CreateTicket(ctx, service.CreateMatchTicketRequest{
		PlayerID:  req.GetPlayerId(),
		MMRScore:  req.GetMmrScore(),
		MatchMode: req.GetMatchMode(),
		MaxWait:   time.Duration(req.GetMaxWaitMs()) * time.Millisecond,
	})
	if err != nil {
		return nil, matchError(err)
	}
	return &pb.CreateMatchTicketResponse{Ticket: toPBMatchTicket(ticket)}, nil
}

func (h *MatchHandler) GetMatchTicket(ctx context.Context, req *pb.GetMatchTicketRequest) (*pb.GetMatchTicketResponse, error) {
	ticket, err := h.matchService.GetTicket(ctx, req.GetTicketId())
	if err != nil {
		return nil, matchError(err)
	}
	return &pb.GetMatchTicketResponse{Ticket: toPBMatchTicket(ticket)}, nil
}

func (h *MatchHandler) CancelMatchTicket(ctx context.Context, req *pb.CancelMatchTicketRequest) (*pb.CancelMatchTicketResponse, error) {
	ticket, err := h.matchService.CancelTicket(ctx, req.GetTicketId())
	if err != nil {
		return nil, matchError(err)
	}
	return &pb.CancelMatchTicketResponse{Ticket: toPBMatchTicket(ticket)}, nil
}

func (h *MatchHandler) GetMatchResult(ctx context.Context, req *pb.GetMatchResultRequest) (*pb.GetMatchResultResponse, error) {
	result, err := h.matchService.GetResult(ctx, req.GetMatchId())
	if err != nil {
		return nil, matchError(err)
	}
	return &pb.GetMatchResultResponse{Result: toPBMatchResult(result)}, nil
}

func matchError(err error) error {
	switch {
	case errors.Is(err, repository.ErrPlayerAlreadyQueued):
		return status.Error(codes.AlreadyExists, err.Error())
	case errors.Is(err, repository.ErrTicketNotFound), errors.Is(err, repository.ErrResultNotFound):
		return status.Error(codes.NotFound, err.Error())
	case errors.Is(err, repository.ErrTicketNotQueued):
		return status.Error(codes.FailedPrecondition, err.Error())
	default:
		return status.Error(codes.InvalidArgument, err.Error())
	}
}

func toPBMatchTicket(ticket *repository.MatchTicket) *pb.MatchTicket {
	if ticket == nil {
		return nil
	}
	return &pb.MatchTicket{
		TicketId:  ticket.TicketID,
		PlayerId:  ticket.PlayerID,
		MmrScore:  ticket.MMRScore,
		MatchMode: ticket.MatchMode,
		Status:    ticket.Status,
		MatchId:   ticket.MatchID,
		RoomId:    ticket.RoomID,
		CreatedAt: ticket.CreatedAt,
		UpdatedAt: ticket.UpdatedAt,
		ExpiresAt: ticket.ExpiresAt,
	}
}

func toPBMatchResult(result *repository.MatchResult) *pb.MatchResult {
	if result == nil {
		return nil
	}
	return &pb.MatchResult{
		MatchId:   result.MatchID,
		RoomId:    result.RoomID,
		MatchMode: result.MatchMode,
		PlayerIds: append([]string(nil), result.PlayerIDs...),
		Status:    result.Status,
		CreatedAt: result.CreatedAt,
	}
}
