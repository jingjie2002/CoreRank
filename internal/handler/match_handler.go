package handler

import (
	"context"
	"errors"
	"time"

	pb "github.com/jingjie2002/CoreRank/api/proto"
	"github.com/jingjie2002/CoreRank/internal/repository"
	"github.com/jingjie2002/CoreRank/internal/service"

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

func (h *MatchHandler) RegisterGameServer(ctx context.Context, req *pb.RegisterGameServerRequest) (*pb.RegisterGameServerResponse, error) {
	server, err := h.matchService.RegisterGameServer(ctx, fromPBGameServer(req.GetServer()))
	if err != nil {
		return nil, matchError(err)
	}
	return &pb.RegisterGameServerResponse{Server: toPBGameServer(server)}, nil
}

func (h *MatchHandler) HeartbeatGameServer(ctx context.Context, req *pb.HeartbeatGameServerRequest) (*pb.HeartbeatGameServerResponse, error) {
	var currentLoad *int64
	if req.GetUpdateCurrentLoad() {
		value := req.GetCurrentLoad()
		currentLoad = &value
	}
	server, err := h.matchService.HeartbeatGameServer(ctx, req.GetServerId(), repository.GameServerHeartbeat{
		Status:      req.GetStatus(),
		CurrentLoad: currentLoad,
	})
	if err != nil {
		return nil, matchError(err)
	}
	return &pb.HeartbeatGameServerResponse{Server: toPBGameServer(server)}, nil
}

func (h *MatchHandler) ListGameServers(ctx context.Context, req *pb.ListGameServersRequest) (*pb.ListGameServersResponse, error) {
	servers, err := h.matchService.ListGameServers(ctx, req.GetMatchMode())
	if err != nil {
		return nil, matchError(err)
	}
	resp := &pb.ListGameServersResponse{
		Servers: make([]*pb.GameServer, 0, len(servers)),
	}
	for _, server := range servers {
		resp.Servers = append(resp.Servers, toPBGameServer(&server))
	}
	return resp, nil
}

func matchError(err error) error {
	switch {
	case errors.Is(err, context.DeadlineExceeded):
		return status.Error(codes.DeadlineExceeded, err.Error())
	case errors.Is(err, context.Canceled):
		return status.Error(codes.Canceled, err.Error())
	case errors.Is(err, repository.ErrPlayerAlreadyQueued):
		return status.Error(codes.AlreadyExists, err.Error())
	case errors.Is(err, repository.ErrTicketNotFound), errors.Is(err, repository.ErrResultNotFound), errors.Is(err, repository.ErrGameServerNotFound):
		return status.Error(codes.NotFound, err.Error())
	case errors.Is(err, repository.ErrTicketNotQueued), errors.Is(err, repository.ErrNoAvailableRoomServer), errors.Is(err, repository.ErrMatchModeMismatch):
		return status.Error(codes.FailedPrecondition, err.Error())
	case errors.Is(err, repository.ErrTooManyMatchModes):
		return status.Error(codes.ResourceExhausted, err.Error())
	case errors.Is(err, repository.ErrInvalidIdentifier), errors.Is(err, repository.ErrInvalidMatchMode),
		errors.Is(err, repository.ErrInvalidGameServer), errors.Is(err, service.ErrInvalidMMRScore),
		errors.Is(err, service.ErrInvalidMaxWait):
		return status.Error(codes.InvalidArgument, err.Error())
	default:
		return status.Error(codes.Internal, "internal match service error")
	}
}

func fromPBGameServer(server *pb.GameServer) repository.GameServer {
	if server == nil {
		return repository.GameServer{}
	}
	return repository.GameServer{
		ServerID:        server.GetServerId(),
		ServerType:      server.GetServerType(),
		Addr:            server.GetAddr(),
		Region:          server.GetRegion(),
		MatchMode:       server.GetMatchMode(),
		Capacity:        server.GetCapacity(),
		CurrentLoad:     server.GetCurrentLoad(),
		ObservedLoad:    server.GetObservedLoad(),
		Status:          server.GetStatus(),
		LastHeartbeatAt: server.GetLastHeartbeatAt(),
		UpdatedAt:       server.GetUpdatedAt(),
	}
}

func toPBGameServer(server *repository.GameServer) *pb.GameServer {
	if server == nil {
		return nil
	}
	return &pb.GameServer{
		ServerId:        server.ServerID,
		ServerType:      server.ServerType,
		Addr:            server.Addr,
		Region:          server.Region,
		MatchMode:       server.MatchMode,
		Capacity:        server.Capacity,
		CurrentLoad:     server.CurrentLoad,
		ObservedLoad:    server.ObservedLoad,
		Status:          server.Status,
		LastHeartbeatAt: server.LastHeartbeatAt,
		UpdatedAt:       server.UpdatedAt,
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
		MatchId:    result.MatchID,
		RoomId:     result.RoomID,
		MatchMode:  result.MatchMode,
		PlayerIds:  append([]string(nil), result.PlayerIDs...),
		Status:     result.Status,
		CreatedAt:  result.CreatedAt,
		ServerId:   result.ServerID,
		ServerAddr: result.ServerAddr,
		JoinToken:  result.JoinToken,
	}
}
