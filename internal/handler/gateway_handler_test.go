package handler

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"

	pb "github.com/jingjie2002/CoreRank/api/proto"

	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

type fakeRankClient struct {
	updateReq *pb.UpdateScoreRequest
	settleReq *pb.SettleMatchRequest
	topReq    *pb.GetTopRankRequest
	playerReq *pb.GetPlayerRankRequest
}

func (c *fakeRankClient) SettleMatch(_ context.Context, req *pb.SettleMatchRequest, _ ...grpc.CallOption) (*pb.SettleMatchResponse, error) {
	c.settleReq = req
	entries := make([]*pb.RankEntry, 0, len(req.GetScores()))
	for i, score := range req.GetScores() {
		entries = append(entries, &pb.RankEntry{
			Rank:   int64(i + 1),
			Player: &pb.Player{PlayerId: score.GetPlayerId(), RankScore: score.GetScore()},
		})
	}
	return &pb.SettleMatchResponse{
		MatchId: req.GetMatchId(), LeaderboardType: req.GetLeaderboardType(), UpdatedPlayers: entries,
	}, nil
}

func (c *fakeRankClient) UpdateScore(_ context.Context, req *pb.UpdateScoreRequest, _ ...grpc.CallOption) (*pb.UpdateScoreResponse, error) {
	c.updateReq = req
	return &pb.UpdateScoreResponse{
		Success: true,
		Player: &pb.Player{
			PlayerId:  req.GetPlayerId(),
			RankScore: req.GetNewScore(),
		},
		CurrentRank: 3,
	}, nil
}

func (c *fakeRankClient) GetTopRank(_ context.Context, req *pb.GetTopRankRequest, _ ...grpc.CallOption) (*pb.GetTopRankResponse, error) {
	c.topReq = req
	return &pb.GetTopRankResponse{
		Entries: []*pb.RankEntry{
			{
				Rank: 1,
				Player: &pb.Player{
					PlayerId:  "p2",
					RankScore: 2000,
				},
			},
		},
	}, nil
}

func (c *fakeRankClient) GetPlayerRank(_ context.Context, req *pb.GetPlayerRankRequest, _ ...grpc.CallOption) (*pb.GetPlayerRankResponse, error) {
	c.playerReq = req
	if req.GetPlayerId() == "missing" {
		return &pb.GetPlayerRankResponse{Found: false}, nil
	}
	return &pb.GetPlayerRankResponse{
		Found: true,
		Player: &pb.Player{
			PlayerId:  req.GetPlayerId(),
			RankScore: 1800,
		},
		CurrentRank: 2,
	}, nil
}

type fakeMatchClient struct {
	createReq    *pb.CreateMatchTicketRequest
	registerReq  *pb.RegisterGameServerRequest
	heartbeatReq *pb.HeartbeatGameServerRequest
	listReq      *pb.ListGameServersRequest
	createErr    error
	cancelErr    error
}

func (c *fakeMatchClient) CreateMatchTicket(_ context.Context, req *pb.CreateMatchTicketRequest, _ ...grpc.CallOption) (*pb.CreateMatchTicketResponse, error) {
	c.createReq = req
	if c.createErr != nil {
		return nil, c.createErr
	}
	return &pb.CreateMatchTicketResponse{
		Ticket: &pb.MatchTicket{
			TicketId:  "ticket-1",
			PlayerId:  req.GetPlayerId(),
			MmrScore:  req.GetMmrScore(),
			MatchMode: req.GetMatchMode(),
			Status:    "queued",
			CreatedAt: 10,
			UpdatedAt: 10,
			ExpiresAt: 20,
		},
	}, nil
}

func (c *fakeMatchClient) GetMatchTicket(context.Context, *pb.GetMatchTicketRequest, ...grpc.CallOption) (*pb.GetMatchTicketResponse, error) {
	return nil, status.Error(codes.NotFound, "match ticket not found")
}

func (c *fakeMatchClient) CancelMatchTicket(context.Context, *pb.CancelMatchTicketRequest, ...grpc.CallOption) (*pb.CancelMatchTicketResponse, error) {
	if c.cancelErr != nil {
		return nil, c.cancelErr
	}
	return nil, status.Error(codes.FailedPrecondition, "match ticket is not queued")
}

func (c *fakeMatchClient) GetMatchResult(context.Context, *pb.GetMatchResultRequest, ...grpc.CallOption) (*pb.GetMatchResultResponse, error) {
	return &pb.GetMatchResultResponse{
		Result: &pb.MatchResult{
			MatchId:    "match-1",
			RoomId:     "room-1",
			ServerId:   "room-server-1",
			ServerAddr: "127.0.0.1:7001",
			MatchMode:  "duel",
			PlayerIds:  []string{"p1", "p2"},
			Status:     "matched",
			CreatedAt:  30,
		},
	}, nil
}

func (c *fakeMatchClient) RegisterGameServer(_ context.Context, req *pb.RegisterGameServerRequest, _ ...grpc.CallOption) (*pb.RegisterGameServerResponse, error) {
	c.registerReq = req
	server := req.GetServer()
	server.LastHeartbeatAt = 100
	server.UpdatedAt = 100
	return &pb.RegisterGameServerResponse{Server: server}, nil
}

func (c *fakeMatchClient) HeartbeatGameServer(_ context.Context, req *pb.HeartbeatGameServerRequest, _ ...grpc.CallOption) (*pb.HeartbeatGameServerResponse, error) {
	c.heartbeatReq = req
	return &pb.HeartbeatGameServerResponse{
		Server: &pb.GameServer{
			ServerId:    req.GetServerId(),
			ServerType:  "room",
			Addr:        "127.0.0.1:7001",
			MatchMode:   "duel",
			Capacity:    8,
			CurrentLoad: req.GetCurrentLoad(),
			Status:      req.GetStatus(),
		},
	}, nil
}

func (c *fakeMatchClient) ListGameServers(_ context.Context, req *pb.ListGameServersRequest, _ ...grpc.CallOption) (*pb.ListGameServersResponse, error) {
	c.listReq = req
	return &pb.ListGameServersResponse{
		Servers: []*pb.GameServer{
			{
				ServerId:    "room-1",
				ServerType:  "room",
				Addr:        "127.0.0.1:7001",
				MatchMode:   req.GetMatchMode(),
				Capacity:    8,
				CurrentLoad: 2,
				Status:      "active",
			},
		},
	}, nil
}

func TestGatewayRankScoreUsesGRPCClient(t *testing.T) {
	rankClient := &fakeRankClient{}
	handler := NewGatewayHTTPHandler(rankClient, &fakeMatchClient{})
	req := httptest.NewRequest(http.MethodPost, "/api/rank/score", jsonBody(map[string]any{
		"player_id": "p1",
		"score":     1500,
	}))
	rec := httptest.NewRecorder()

	handler.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("expected status 200, got %d body=%s", rec.Code, rec.Body.String())
	}
	if rankClient.updateReq == nil || rankClient.updateReq.GetPlayerId() != "p1" || rankClient.updateReq.GetNewScore() != 1500 {
		t.Fatalf("unexpected UpdateScore request: %#v", rankClient.updateReq)
	}

	var payload gatewayRankPlayer
	if err := json.NewDecoder(rec.Body).Decode(&payload); err != nil {
		t.Fatalf("decode response: %v", err)
	}
	if payload.PlayerID != "p1" || payload.Score != 1500 || payload.Rank != 3 {
		t.Fatalf("unexpected response: %#v", payload)
	}
}

func TestGatewayRankScoreForwardsLeaderboardType(t *testing.T) {
	rankClient := &fakeRankClient{}
	handler := NewGatewayHTTPHandler(rankClient, &fakeMatchClient{})
	req := httptest.NewRequest(http.MethodPost, "/api/rank/score?leaderboard_type=season:ss25", jsonBody(map[string]any{
		"player_id": "p1",
		"score":     1500,
	}))
	rec := httptest.NewRecorder()

	handler.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("expected status 200, got %d body=%s", rec.Code, rec.Body.String())
	}
	if rankClient.updateReq == nil || rankClient.updateReq.GetLeaderboardType() != "season:ss25" {
		t.Fatalf("unexpected leaderboard type: %#v", rankClient.updateReq)
	}
}

func TestGatewayRankTopUsesGRPCClient(t *testing.T) {
	rankClient := &fakeRankClient{}
	handler := NewGatewayHTTPHandler(rankClient, &fakeMatchClient{})
	req := httptest.NewRequest(http.MethodGet, "/api/rank/top?n=5", nil)
	rec := httptest.NewRecorder()

	handler.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("expected status 200, got %d body=%s", rec.Code, rec.Body.String())
	}
	if rankClient.topReq == nil || rankClient.topReq.GetTopN() != 5 {
		t.Fatalf("unexpected GetTopRank request: %#v", rankClient.topReq)
	}

	var payload []gatewayRankPlayer
	if err := json.NewDecoder(rec.Body).Decode(&payload); err != nil {
		t.Fatalf("decode response: %v", err)
	}
	if len(payload) != 1 || payload[0].PlayerID != "p2" || payload[0].Rank != 1 {
		t.Fatalf("unexpected response: %#v", payload)
	}
}

func TestGatewayRankPlayerUsesGRPCClient(t *testing.T) {
	rankClient := &fakeRankClient{}
	handler := NewGatewayHTTPHandler(rankClient, &fakeMatchClient{})
	req := httptest.NewRequest(http.MethodGet, "/api/rank/player/p1?leaderboard_type=season:ss25", nil)
	rec := httptest.NewRecorder()

	handler.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("expected status 200, got %d body=%s", rec.Code, rec.Body.String())
	}
	if rankClient.playerReq == nil || rankClient.playerReq.GetPlayerId() != "p1" || rankClient.playerReq.GetLeaderboardType() != "season:ss25" {
		t.Fatalf("unexpected GetPlayerRank request: %#v", rankClient.playerReq)
	}

	var payload gatewayRankPlayer
	if err := json.NewDecoder(rec.Body).Decode(&payload); err != nil {
		t.Fatalf("decode response: %v", err)
	}
	if payload.PlayerID != "p1" || payload.Score != 1800 || payload.Rank != 2 {
		t.Fatalf("unexpected response: %#v", payload)
	}
}

func TestGatewayRankPlayerNotFound(t *testing.T) {
	handler := NewGatewayHTTPHandler(&fakeRankClient{}, &fakeMatchClient{})
	req := httptest.NewRequest(http.MethodGet, "/api/rank/player/missing", nil)
	rec := httptest.NewRecorder()

	handler.ServeHTTP(rec, req)

	if rec.Code != http.StatusNotFound {
		t.Fatalf("expected status 404, got %d body=%s", rec.Code, rec.Body.String())
	}
}

func TestGatewaySettlementUsesSingleAtomicRPC(t *testing.T) {
	rankClient := &fakeRankClient{}
	handler := NewGatewayHTTPHandler(rankClient, &fakeMatchClient{})
	req := httptest.NewRequest(http.MethodPost, "/api/matches/match-1/settle", jsonBody(map[string]any{
		"leaderboard_type": "global",
		"scores": []map[string]any{
			{"player_id": "p1", "score": 1200},
			{"player_id": "p2", "score": 1100},
		},
	}))
	rec := httptest.NewRecorder()

	handler.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("expected status 200, got %d body=%s", rec.Code, rec.Body.String())
	}
	if rankClient.settleReq == nil || rankClient.settleReq.GetMatchId() != "match-1" || len(rankClient.settleReq.GetScores()) != 2 {
		t.Fatalf("unexpected SettleMatch request: %#v", rankClient.settleReq)
	}
	if rankClient.updateReq != nil {
		t.Fatalf("settlement must not issue per-player UpdateScore calls: %#v", rankClient.updateReq)
	}
}

func TestGatewayAPIKeyAndBodyLimit(t *testing.T) {
	protected := RequireAPIKey(NewGatewayHTTPHandler(&fakeRankClient{}, &fakeMatchClient{}), "test-secret")

	unauthorized := httptest.NewRequest(http.MethodGet, "/api/rank/top", nil)
	unauthorizedRec := httptest.NewRecorder()
	protected.ServeHTTP(unauthorizedRec, unauthorized)
	if unauthorizedRec.Code != http.StatusUnauthorized {
		t.Fatalf("expected API key rejection, got %d", unauthorizedRec.Code)
	}

	largeBody := bytes.Repeat([]byte("x"), maxJSONBodyBytes+1)
	largeReq := httptest.NewRequest(http.MethodPost, "/api/rank/score", bytes.NewReader(largeBody))
	largeReq.Header.Set("X-CoreRank-API-Key", "test-secret")
	largeRec := httptest.NewRecorder()
	protected.ServeHTTP(largeRec, largeReq)
	if largeRec.Code != http.StatusBadRequest {
		t.Fatalf("expected oversized body rejection, got %d", largeRec.Code)
	}
}

func TestGatewayReadinessReflectsBackendFailure(t *testing.T) {
	handler := NewGatewayHTTPHandlerWithReadiness(&fakeRankClient{}, &fakeMatchClient{}, func(context.Context) error {
		return errors.New("redis dependency unavailable")
	})
	readyReq := httptest.NewRequest(http.MethodGet, "/readyz", nil)
	readyRec := httptest.NewRecorder()
	handler.ServeHTTP(readyRec, readyReq)
	if readyRec.Code != http.StatusServiceUnavailable {
		t.Fatalf("expected readiness 503, got %d body=%s", readyRec.Code, readyRec.Body.String())
	}

	liveReq := httptest.NewRequest(http.MethodGet, "/healthz", nil)
	liveRec := httptest.NewRecorder()
	handler.ServeHTTP(liveRec, liveReq)
	if liveRec.Code != http.StatusOK {
		t.Fatalf("liveness must stay independent, got %d", liveRec.Code)
	}
}

func TestGatewayMatchTicketUsesGRPCClient(t *testing.T) {
	matchClient := &fakeMatchClient{}
	handler := NewGatewayHTTPHandler(&fakeRankClient{}, matchClient)
	req := httptest.NewRequest(http.MethodPost, "/api/match/tickets", jsonBody(map[string]any{
		"player_id":   "p3",
		"mmr_score":   1700,
		"match_mode":  "duel",
		"max_wait_ms": 30000,
	}))
	rec := httptest.NewRecorder()

	handler.ServeHTTP(rec, req)

	if rec.Code != http.StatusCreated {
		t.Fatalf("expected status 201, got %d body=%s", rec.Code, rec.Body.String())
	}
	if matchClient.createReq == nil || matchClient.createReq.GetPlayerId() != "p3" || matchClient.createReq.GetMatchMode() != "duel" {
		t.Fatalf("unexpected CreateMatchTicket request: %#v", matchClient.createReq)
	}

	var payload gatewayMatchTicket
	if err := json.NewDecoder(rec.Body).Decode(&payload); err != nil {
		t.Fatalf("decode response: %v", err)
	}
	if payload.TicketID != "ticket-1" || payload.PlayerID != "p3" || payload.Status != "queued" {
		t.Fatalf("unexpected response: %#v", payload)
	}
}

func TestGatewayDuplicateMatchTicketMapsToConflict(t *testing.T) {
	matchClient := &fakeMatchClient{
		createErr: status.Error(codes.AlreadyExists, "player already has a queued ticket"),
	}
	handler := NewGatewayHTTPHandler(&fakeRankClient{}, matchClient)
	req := httptest.NewRequest(http.MethodPost, "/api/match/tickets", jsonBody(map[string]any{
		"player_id":  "p3",
		"mmr_score":  1700,
		"match_mode": "duel",
	}))
	rec := httptest.NewRecorder()

	handler.ServeHTTP(rec, req)

	if rec.Code != http.StatusConflict {
		t.Fatalf("expected status 409, got %d body=%s", rec.Code, rec.Body.String())
	}
	if matchClient.createReq == nil || matchClient.createReq.GetPlayerId() != "p3" {
		t.Fatalf("unexpected CreateMatchTicket request: %#v", matchClient.createReq)
	}
}

func TestGatewayCancelNonQueuedTicketMapsToConflict(t *testing.T) {
	matchClient := &fakeMatchClient{
		cancelErr: status.Error(codes.FailedPrecondition, "match ticket is not queued"),
	}
	handler := NewGatewayHTTPHandler(&fakeRankClient{}, matchClient)
	req := httptest.NewRequest(http.MethodDelete, "/api/match/tickets/ticket-1", nil)
	rec := httptest.NewRecorder()

	handler.ServeHTTP(rec, req)

	if rec.Code != http.StatusConflict {
		t.Fatalf("expected status 409, got %d body=%s", rec.Code, rec.Body.String())
	}
}

func TestGatewayRoomServerRegisterUsesGRPCClient(t *testing.T) {
	matchClient := &fakeMatchClient{}
	handler := NewGatewayHTTPHandler(&fakeRankClient{}, matchClient)
	req := httptest.NewRequest(http.MethodPost, "/api/servers", jsonBody(map[string]any{
		"server_id":    "room-1",
		"server_type":  "room",
		"addr":         "127.0.0.1:7001",
		"match_mode":   "duel",
		"capacity":     8,
		"current_load": 1,
		"status":       "active",
	}))
	rec := httptest.NewRecorder()

	handler.ServeHTTP(rec, req)

	if rec.Code != http.StatusCreated {
		t.Fatalf("expected status 201, got %d body=%s", rec.Code, rec.Body.String())
	}
	if matchClient.registerReq == nil || matchClient.registerReq.GetServer().GetServerId() != "room-1" {
		t.Fatalf("unexpected RegisterGameServer request: %#v", matchClient.registerReq)
	}
	var payload gatewayGameServer
	if err := json.NewDecoder(rec.Body).Decode(&payload); err != nil {
		t.Fatalf("decode response: %v", err)
	}
	if payload.ServerID != "room-1" || payload.MatchMode != "duel" || payload.LastHeartbeatAt != 100 {
		t.Fatalf("unexpected response: %#v", payload)
	}
}

func TestGatewayRoomServerHeartbeatUsesGRPCClient(t *testing.T) {
	matchClient := &fakeMatchClient{}
	handler := NewGatewayHTTPHandler(&fakeRankClient{}, matchClient)
	req := httptest.NewRequest(http.MethodPost, "/api/servers/room-1/heartbeat", jsonBody(map[string]any{
		"status":       "active",
		"current_load": 3,
	}))
	rec := httptest.NewRecorder()

	handler.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("expected status 200, got %d body=%s", rec.Code, rec.Body.String())
	}
	if matchClient.heartbeatReq == nil ||
		matchClient.heartbeatReq.GetServerId() != "room-1" ||
		!matchClient.heartbeatReq.GetUpdateCurrentLoad() ||
		matchClient.heartbeatReq.GetCurrentLoad() != 3 {
		t.Fatalf("unexpected HeartbeatGameServer request: %#v", matchClient.heartbeatReq)
	}
}

func TestGatewayRoomServerListUsesGRPCClient(t *testing.T) {
	matchClient := &fakeMatchClient{}
	handler := NewGatewayHTTPHandler(&fakeRankClient{}, matchClient)
	req := httptest.NewRequest(http.MethodGet, "/api/servers?match_mode=duel", nil)
	rec := httptest.NewRecorder()

	handler.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("expected status 200, got %d body=%s", rec.Code, rec.Body.String())
	}
	if matchClient.listReq == nil || matchClient.listReq.GetMatchMode() != "duel" {
		t.Fatalf("unexpected ListGameServers request: %#v", matchClient.listReq)
	}
	var payload []gatewayGameServer
	if err := json.NewDecoder(rec.Body).Decode(&payload); err != nil {
		t.Fatalf("decode response: %v", err)
	}
	if len(payload) != 1 || payload[0].ServerID != "room-1" || payload[0].CurrentLoad != 2 {
		t.Fatalf("unexpected response: %#v", payload)
	}
}

func TestGatewayGRPCErrorMapping(t *testing.T) {
	handler := NewGatewayHTTPHandler(&fakeRankClient{}, &fakeMatchClient{})
	req := httptest.NewRequest(http.MethodGet, "/api/match/tickets/missing", nil)
	rec := httptest.NewRecorder()

	handler.ServeHTTP(rec, req)

	if rec.Code != http.StatusNotFound {
		t.Fatalf("expected status 404, got %d body=%s", rec.Code, rec.Body.String())
	}
}

func TestGatewayUnsupportedLegacyEndpoints(t *testing.T) {
	handler := NewGatewayHTTPHandler(&fakeRankClient{}, &fakeMatchClient{})
	req := httptest.NewRequest(http.MethodPost, "/api/match/pool", jsonBody(map[string]any{}))
	rec := httptest.NewRecorder()

	handler.ServeHTTP(rec, req)

	if rec.Code != http.StatusNotImplemented {
		t.Fatalf("expected status 501, got %d body=%s", rec.Code, rec.Body.String())
	}
}

func jsonBody(payload any) *bytes.Reader {
	raw, _ := json.Marshal(payload)
	return bytes.NewReader(raw)
}
