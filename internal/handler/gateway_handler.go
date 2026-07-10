package handler

import (
	"context"
	"crypto/subtle"
	"errors"
	"net/http"
	"strconv"
	"strings"
	"time"

	pb "CoreRank/api/proto"

	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

type gatewayRankPlayer struct {
	PlayerID string  `json:"player_id"`
	Score    float64 `json:"score"`
	Rank     int64   `json:"rank"`
}

const (
	gatewayRPCTimeout = 3 * time.Second
	maxConcurrentHTTP = 256
)

type gatewayGameServer struct {
	ServerID        string `json:"server_id"`
	ServerType      string `json:"server_type"`
	Addr            string `json:"addr"`
	Region          string `json:"region"`
	MatchMode       string `json:"match_mode"`
	Capacity        int64  `json:"capacity"`
	CurrentLoad     int64  `json:"current_load"`
	ObservedLoad    int64  `json:"observed_load"`
	Status          string `json:"status"`
	LastHeartbeatAt int64  `json:"last_heartbeat_at"`
	UpdatedAt       int64  `json:"updated_at"`
}

type gatewayMatchTicket struct {
	TicketID  string `json:"ticket_id"`
	PlayerID  string `json:"player_id"`
	MMRScore  int64  `json:"mmr_score"`
	MatchMode string `json:"match_mode"`
	Status    string `json:"status"`
	MatchID   string `json:"match_id"`
	RoomID    string `json:"room_id"`
	CreatedAt int64  `json:"created_at"`
	UpdatedAt int64  `json:"updated_at"`
	ExpiresAt int64  `json:"expires_at"`
}

type gatewayMatchResult struct {
	MatchID    string   `json:"match_id"`
	RoomID     string   `json:"room_id"`
	ServerID   string   `json:"server_id"`
	ServerAddr string   `json:"server_addr"`
	MatchMode  string   `json:"match_mode"`
	PlayerIDs  []string `json:"player_ids"`
	Status     string   `json:"status"`
	CreatedAt  int64    `json:"created_at"`
	JoinToken  string   `json:"join_token"`
}

// NewGatewayHTTPHandler exposes the REST API through gRPC clients. It is used
// by cmd/gateway, while NewHTTPHandler keeps serving the legacy in-process mode.
func NewGatewayHTTPHandler(rankClient pb.RankServiceClient, matchClient pb.MatchServiceClient) http.Handler {
	return NewGatewayHTTPHandlerWithReadiness(rankClient, matchClient, nil)
}

func NewGatewayHTTPHandlerWithReadiness(rankClient pb.RankServiceClient, matchClient pb.MatchServiceClient, readiness func(context.Context) error) http.Handler {
	mux := http.NewServeMux()

	mux.HandleFunc("GET /health", handleHealth)
	mux.HandleFunc("GET /healthz", handleHealth)
	mux.HandleFunc("GET /readyz", func(w http.ResponseWriter, r *http.Request) {
		if readiness != nil {
			if err := readiness(r.Context()); err != nil {
				writeError(w, http.StatusServiceUnavailable, errors.New("backend services are not ready"))
				return
			}
		}
		writeJSON(w, http.StatusOK, map[string]string{"status": "ready"})
	})
	mux.HandleFunc("GET /api/agent/capabilities", handleAgentCapabilities)
	mux.HandleFunc("GET /api/agent/events", handleAgentEvents)
	mux.HandleFunc("GET /api/agent/logs", handleAgentLogs)

	mux.HandleFunc("POST /api/rank/score", func(w http.ResponseWriter, r *http.Request) {
		if rankClient == nil {
			writeGatewayUnavailable(w, "rank-service client is not configured")
			return
		}

		var req struct {
			PlayerID        string `json:"player_id"`
			Score           int64  `json:"score"`
			LeaderboardType string `json:"leaderboard_type"`
		}
		if err := readJSON(r, &req); err != nil {
			writeError(w, http.StatusBadRequest, err)
			return
		}
		if req.PlayerID == "" {
			writeError(w, http.StatusBadRequest, errors.New("player_id is required"))
			return
		}
		leaderboardType := rankLeaderboardType(r, req.LeaderboardType)

		resp, err := rankClient.UpdateScore(r.Context(), &pb.UpdateScoreRequest{
			PlayerId:        req.PlayerID,
			NewScore:        req.Score,
			ChangeType:      "ABSOLUTE",
			LeaderboardType: leaderboardType,
		})
		if err != nil {
			writeGatewayGRPCError(w, err)
			return
		}
		if !resp.GetSuccess() {
			writeError(w, http.StatusBadGateway, errors.New("rank-service rejected score update"))
			return
		}

		playerID := req.PlayerID
		score := float64(req.Score)
		if player := resp.GetPlayer(); player != nil {
			if player.GetPlayerId() != "" {
				playerID = player.GetPlayerId()
			}
			score = float64(player.GetRankScore())
		}
		writeJSON(w, http.StatusOK, gatewayRankPlayer{
			PlayerID: playerID,
			Score:    score,
			Rank:     resp.GetCurrentRank(),
		})
	})

	mux.HandleFunc("GET /api/rank/top", func(w http.ResponseWriter, r *http.Request) {
		if rankClient == nil {
			writeGatewayUnavailable(w, "rank-service client is not configured")
			return
		}

		leaderboardType := rankLeaderboardType(r, "")
		topN, err := parseOptionalInt64(r.URL.Query().Get("n"))
		if err != nil {
			writeError(w, http.StatusBadRequest, errors.New("n must be an integer"))
			return
		}
		offset, err := parseOptionalInt64(r.URL.Query().Get("offset"))
		if err != nil {
			writeError(w, http.StatusBadRequest, errors.New("offset must be an integer"))
			return
		}
		if topN < 0 || topN > 100 || offset < 0 || offset > 10000 {
			writeError(w, http.StatusBadRequest, errors.New("n must be between 1 and 100 and offset must be between 0 and 10000"))
			return
		}
		resp, err := rankClient.GetTopRank(r.Context(), &pb.GetTopRankRequest{
			LeaderboardType: leaderboardType,
			TopN:            int32(topN),
			Offset:          offset,
		})
		if err != nil {
			writeGatewayGRPCError(w, err)
			return
		}

		players := make([]gatewayRankPlayer, 0, len(resp.GetEntries()))
		for _, entry := range resp.GetEntries() {
			player := entry.GetPlayer()
			players = append(players, gatewayRankPlayer{
				PlayerID: player.GetPlayerId(),
				Score:    float64(player.GetRankScore()),
				Rank:     entry.GetRank(),
			})
		}
		writeJSON(w, http.StatusOK, players)
	})

	mux.HandleFunc("GET /api/rank/player/", func(w http.ResponseWriter, r *http.Request) {
		if rankClient == nil {
			writeGatewayUnavailable(w, "rank-service client is not configured")
			return
		}

		playerID := strings.TrimPrefix(r.URL.Path, "/api/rank/player/")
		if playerID == "" {
			writeError(w, http.StatusBadRequest, errors.New("player_id is required"))
			return
		}
		resp, err := rankClient.GetPlayerRank(r.Context(), &pb.GetPlayerRankRequest{
			PlayerId:        playerID,
			LeaderboardType: rankLeaderboardType(r, ""),
		})
		if err != nil {
			writeGatewayGRPCError(w, err)
			return
		}
		if !resp.GetFound() {
			writeError(w, http.StatusNotFound, errors.New("player not found in rank"))
			return
		}
		writeJSON(w, http.StatusOK, gatewayRankPlayer{
			PlayerID: resp.GetPlayer().GetPlayerId(),
			Score:    float64(resp.GetPlayer().GetRankScore()),
			Rank:     resp.GetCurrentRank(),
		})
	})

	mux.HandleFunc("POST /api/match/tickets", func(w http.ResponseWriter, r *http.Request) {
		if matchClient == nil {
			writeGatewayUnavailable(w, "match-service client is not configured")
			return
		}

		var req struct {
			PlayerID  string `json:"player_id"`
			MMRScore  int64  `json:"mmr_score"`
			MatchMode string `json:"match_mode"`
			MaxWaitMS int64  `json:"max_wait_ms"`
		}
		if err := readJSON(r, &req); err != nil {
			writeError(w, http.StatusBadRequest, err)
			return
		}
		if req.PlayerID == "" {
			writeError(w, http.StatusBadRequest, errors.New("player_id is required"))
			return
		}

		resp, err := matchClient.CreateMatchTicket(r.Context(), &pb.CreateMatchTicketRequest{
			PlayerId:  req.PlayerID,
			MmrScore:  req.MMRScore,
			MatchMode: req.MatchMode,
			MaxWaitMs: req.MaxWaitMS,
		})
		if err != nil {
			writeGatewayGRPCError(w, err)
			return
		}
		writeJSON(w, http.StatusCreated, toGatewayMatchTicket(resp.GetTicket()))
	})

	mux.HandleFunc("GET /api/match/tickets/", func(w http.ResponseWriter, r *http.Request) {
		if matchClient == nil {
			writeGatewayUnavailable(w, "match-service client is not configured")
			return
		}

		ticketID := strings.TrimPrefix(r.URL.Path, "/api/match/tickets/")
		if ticketID == "" {
			writeError(w, http.StatusBadRequest, errors.New("ticket_id is required"))
			return
		}
		resp, err := matchClient.GetMatchTicket(r.Context(), &pb.GetMatchTicketRequest{TicketId: ticketID})
		if err != nil {
			writeGatewayGRPCError(w, err)
			return
		}
		writeJSON(w, http.StatusOK, toGatewayMatchTicket(resp.GetTicket()))
	})

	mux.HandleFunc("DELETE /api/match/tickets/", func(w http.ResponseWriter, r *http.Request) {
		if matchClient == nil {
			writeGatewayUnavailable(w, "match-service client is not configured")
			return
		}

		ticketID := strings.TrimPrefix(r.URL.Path, "/api/match/tickets/")
		if ticketID == "" {
			writeError(w, http.StatusBadRequest, errors.New("ticket_id is required"))
			return
		}
		resp, err := matchClient.CancelMatchTicket(r.Context(), &pb.CancelMatchTicketRequest{TicketId: ticketID})
		if err != nil {
			writeGatewayGRPCError(w, err)
			return
		}
		writeJSON(w, http.StatusOK, toGatewayMatchTicket(resp.GetTicket()))
	})

	mux.HandleFunc("GET /api/match/results/", func(w http.ResponseWriter, r *http.Request) {
		if matchClient == nil {
			writeGatewayUnavailable(w, "match-service client is not configured")
			return
		}

		matchID := strings.TrimPrefix(r.URL.Path, "/api/match/results/")
		if matchID == "" {
			writeError(w, http.StatusBadRequest, errors.New("match_id is required"))
			return
		}
		resp, err := matchClient.GetMatchResult(r.Context(), &pb.GetMatchResultRequest{MatchId: matchID})
		if err != nil {
			writeGatewayGRPCError(w, err)
			return
		}
		writeJSON(w, http.StatusOK, toGatewayMatchResult(resp.GetResult()))
	})

	mux.HandleFunc("POST /api/matches/", func(w http.ResponseWriter, r *http.Request) {
		if rankClient == nil {
			writeGatewayUnavailable(w, "rank-service client is not configured")
			return
		}

		path := strings.TrimPrefix(r.URL.Path, "/api/matches/")
		if !strings.HasSuffix(path, "/settle") {
			writeError(w, http.StatusNotFound, errors.New("match settlement endpoint not found"))
			return
		}
		matchID := strings.Trim(strings.TrimSuffix(path, "/settle"), "/")
		if matchID == "" || strings.Contains(matchID, "/") {
			writeError(w, http.StatusBadRequest, errors.New("match_id is required"))
			return
		}
		var req struct {
			LeaderboardType string `json:"leaderboard_type"`
			Scores          []struct {
				PlayerID string `json:"player_id"`
				Score    int64  `json:"score"`
			} `json:"scores"`
		}
		if err := readJSON(r, &req); err != nil {
			writeError(w, http.StatusBadRequest, err)
			return
		}
		if len(req.Scores) == 0 {
			writeError(w, http.StatusBadRequest, errors.New("scores is required"))
			return
		}
		leaderboardType := rankLeaderboardType(r, req.LeaderboardType)

		settlementScores := make([]*pb.SettlementScore, 0, len(req.Scores))
		for _, score := range req.Scores {
			if score.PlayerID == "" {
				writeError(w, http.StatusBadRequest, errors.New("player_id is required"))
				return
			}
			settlementScores = append(settlementScores, &pb.SettlementScore{PlayerId: score.PlayerID, Score: score.Score})
		}
		resp, err := rankClient.SettleMatch(r.Context(), &pb.SettleMatchRequest{
			MatchId:         matchID,
			LeaderboardType: leaderboardType,
			Scores:          settlementScores,
		})
		if err != nil {
			writeGatewayGRPCError(w, err)
			return
		}
		updated := make([]gatewayRankPlayer, 0, len(resp.GetUpdatedPlayers()))
		for _, entry := range resp.GetUpdatedPlayers() {
			updated = append(updated, gatewayRankPlayer{
				PlayerID: entry.GetPlayer().GetPlayerId(),
				Score:    float64(entry.GetPlayer().GetRankScore()),
				Rank:     entry.GetRank(),
			})
		}

		writeJSON(w, http.StatusOK, map[string]any{
			"match_id":         matchID,
			"leaderboard_type": leaderboardType,
			"updated_players":  updated,
			"idempotent":       resp.GetIdempotent(),
		})
	})

	mux.HandleFunc("POST /api/match/pool", func(w http.ResponseWriter, _ *http.Request) {
		writeUnsupported(w, "POST /api/match/pool is legacy Redis-local API; use POST /api/match/tickets")
	})
	mux.HandleFunc("DELETE /api/match/pool/", func(w http.ResponseWriter, _ *http.Request) {
		writeUnsupported(w, "DELETE /api/match/pool is legacy Redis-local API; use DELETE /api/match/tickets/{ticket_id}")
	})
	mux.HandleFunc("POST /api/servers", func(w http.ResponseWriter, r *http.Request) {
		if matchClient == nil {
			writeGatewayUnavailable(w, "match-service client is not configured")
			return
		}

		var req gatewayGameServer
		if err := readJSON(r, &req); err != nil {
			writeError(w, http.StatusBadRequest, err)
			return
		}
		resp, err := matchClient.RegisterGameServer(r.Context(), &pb.RegisterGameServerRequest{
			Server: fromGatewayGameServer(req),
		})
		if err != nil {
			writeGatewayGRPCError(w, err)
			return
		}
		writeJSON(w, http.StatusCreated, toGatewayGameServer(resp.GetServer()))
	})
	mux.HandleFunc("GET /api/servers", func(w http.ResponseWriter, r *http.Request) {
		if matchClient == nil {
			writeGatewayUnavailable(w, "match-service client is not configured")
			return
		}

		resp, err := matchClient.ListGameServers(r.Context(), &pb.ListGameServersRequest{
			MatchMode: r.URL.Query().Get("match_mode"),
		})
		if err != nil {
			writeGatewayGRPCError(w, err)
			return
		}
		servers := make([]gatewayGameServer, 0, len(resp.GetServers()))
		for _, server := range resp.GetServers() {
			servers = append(servers, toGatewayGameServer(server))
		}
		writeJSON(w, http.StatusOK, servers)
	})
	mux.HandleFunc("POST /api/servers/", func(w http.ResponseWriter, r *http.Request) {
		if matchClient == nil {
			writeGatewayUnavailable(w, "match-service client is not configured")
			return
		}

		path := strings.TrimPrefix(r.URL.Path, "/api/servers/")
		if !strings.HasSuffix(path, "/heartbeat") {
			writeError(w, http.StatusNotFound, errors.New("server heartbeat endpoint not found"))
			return
		}
		serverID := strings.Trim(strings.TrimSuffix(path, "/heartbeat"), "/")
		if serverID == "" || strings.Contains(serverID, "/") {
			writeError(w, http.StatusBadRequest, errors.New("server_id is required"))
			return
		}

		var req struct {
			Status      string `json:"status"`
			CurrentLoad *int64 `json:"current_load"`
		}
		if err := readOptionalJSON(r, &req); err != nil {
			writeError(w, http.StatusBadRequest, err)
			return
		}

		heartbeat := &pb.HeartbeatGameServerRequest{
			ServerId: serverID,
			Status:   req.Status,
		}
		if req.CurrentLoad != nil {
			heartbeat.CurrentLoad = *req.CurrentLoad
			heartbeat.UpdateCurrentLoad = true
		}
		resp, err := matchClient.HeartbeatGameServer(r.Context(), heartbeat)
		if err != nil {
			writeGatewayGRPCError(w, err)
			return
		}
		writeJSON(w, http.StatusOK, toGatewayGameServer(resp.GetServer()))
	})

	return withGatewayProtection(mux)
}

func parseOptionalInt64(value string) (int64, error) {
	if strings.TrimSpace(value) == "" {
		return 0, nil
	}
	return strconv.ParseInt(value, 10, 64)
}

func withGatewayProtection(next http.Handler) http.Handler {
	semaphore := make(chan struct{}, maxConcurrentHTTP)
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		select {
		case semaphore <- struct{}{}:
			defer func() { <-semaphore }()
		default:
			writeError(w, http.StatusServiceUnavailable, errors.New("too many concurrent requests"))
			return
		}
		ctx, cancel := context.WithTimeout(r.Context(), gatewayRPCTimeout)
		defer cancel()
		next.ServeHTTP(w, r.WithContext(ctx))
	})
}

// RequireAPIKey protects API routes when a key is configured. Health and
// readiness endpoints stay available for local orchestration probes.
func RequireAPIKey(next http.Handler, apiKey string) http.Handler {
	apiKey = strings.TrimSpace(apiKey)
	if apiKey == "" {
		return next
	}
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if !strings.HasPrefix(r.URL.Path, "/api/") {
			next.ServeHTTP(w, r)
			return
		}
		provided := strings.TrimSpace(r.Header.Get("X-CoreRank-API-Key"))
		if provided == "" {
			provided = strings.TrimSpace(strings.TrimPrefix(r.Header.Get("Authorization"), "Bearer "))
		}
		if subtle.ConstantTimeCompare([]byte(provided), []byte(apiKey)) != 1 {
			writeError(w, http.StatusUnauthorized, errors.New("valid API key is required"))
			return
		}
		next.ServeHTTP(w, r)
	})
}

func toGatewayMatchTicket(ticket *pb.MatchTicket) gatewayMatchTicket {
	if ticket == nil {
		return gatewayMatchTicket{}
	}
	return gatewayMatchTicket{
		TicketID:  ticket.GetTicketId(),
		PlayerID:  ticket.GetPlayerId(),
		MMRScore:  ticket.GetMmrScore(),
		MatchMode: ticket.GetMatchMode(),
		Status:    ticket.GetStatus(),
		MatchID:   ticket.GetMatchId(),
		RoomID:    ticket.GetRoomId(),
		CreatedAt: ticket.GetCreatedAt(),
		UpdatedAt: ticket.GetUpdatedAt(),
		ExpiresAt: ticket.GetExpiresAt(),
	}
}

func fromGatewayGameServer(server gatewayGameServer) *pb.GameServer {
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

func toGatewayGameServer(server *pb.GameServer) gatewayGameServer {
	if server == nil {
		return gatewayGameServer{}
	}
	return gatewayGameServer{
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

func toGatewayMatchResult(result *pb.MatchResult) gatewayMatchResult {
	if result == nil {
		return gatewayMatchResult{}
	}
	return gatewayMatchResult{
		MatchID:    result.GetMatchId(),
		RoomID:     result.GetRoomId(),
		ServerID:   result.GetServerId(),
		ServerAddr: result.GetServerAddr(),
		MatchMode:  result.GetMatchMode(),
		PlayerIDs:  append([]string(nil), result.GetPlayerIds()...),
		Status:     result.GetStatus(),
		CreatedAt:  result.GetCreatedAt(),
		JoinToken:  result.GetJoinToken(),
	}
}

func writeUnsupported(w http.ResponseWriter, message string) {
	writeError(w, http.StatusNotImplemented, errors.New(message))
}

func writeGatewayUnavailable(w http.ResponseWriter, message string) {
	writeError(w, http.StatusServiceUnavailable, errors.New(message))
}

func writeGatewayGRPCError(w http.ResponseWriter, err error) {
	st, ok := status.FromError(err)
	if !ok {
		writeError(w, http.StatusBadGateway, err)
		return
	}

	httpStatus := http.StatusBadGateway
	switch st.Code() {
	case codes.InvalidArgument:
		httpStatus = http.StatusBadRequest
	case codes.Unauthenticated:
		httpStatus = http.StatusUnauthorized
	case codes.PermissionDenied:
		httpStatus = http.StatusForbidden
	case codes.AlreadyExists, codes.FailedPrecondition:
		httpStatus = http.StatusConflict
	case codes.NotFound:
		httpStatus = http.StatusNotFound
	case codes.Unavailable:
		httpStatus = http.StatusServiceUnavailable
	case codes.ResourceExhausted:
		httpStatus = http.StatusTooManyRequests
	case codes.DeadlineExceeded:
		httpStatus = http.StatusGatewayTimeout
	case codes.Canceled:
		httpStatus = http.StatusRequestTimeout
	}
	writeError(w, httpStatus, errors.New(st.Message()))
}
