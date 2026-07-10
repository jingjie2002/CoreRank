package handler

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"

	"github.com/jingjie2002/CoreRank/internal/repository"
	"github.com/jingjie2002/CoreRank/internal/service"
)

const maxJSONBodyBytes = 64 * 1024

// NewHTTPHandler exposes a small RESTful gateway on top of the same Redis-backed
// rank and match repository used by the gRPC service.
func NewHTTPHandler(rankService *service.RankService, playerRepo *repository.PlayerRepository, matchService *service.MatchService) http.Handler {
	mux := http.NewServeMux()

	mux.HandleFunc("GET /health", handleHealth)
	mux.HandleFunc("GET /healthz", handleHealth)
	mux.HandleFunc("GET /readyz", handleHealth)
	mux.HandleFunc("GET /api/agent/capabilities", handleAgentCapabilities)
	mux.HandleFunc("GET /api/agent/events", handleAgentEvents)
	mux.HandleFunc("GET /api/agent/logs", handleAgentLogs)

	mux.HandleFunc("POST /api/servers", func(w http.ResponseWriter, r *http.Request) {
		var req struct {
			ServerID    string `json:"server_id"`
			ServerType  string `json:"server_type"`
			Addr        string `json:"addr"`
			Region      string `json:"region"`
			MatchMode   string `json:"match_mode"`
			Capacity    int64  `json:"capacity"`
			CurrentLoad int64  `json:"current_load"`
			Status      string `json:"status"`
		}
		if err := readJSON(r, &req); err != nil {
			writeError(w, http.StatusBadRequest, err)
			return
		}

		server, err := matchService.RegisterGameServer(r.Context(), repository.GameServer{
			ServerID:    req.ServerID,
			ServerType:  req.ServerType,
			Addr:        req.Addr,
			Region:      req.Region,
			MatchMode:   req.MatchMode,
			Capacity:    req.Capacity,
			CurrentLoad: req.CurrentLoad,
			Status:      req.Status,
		})
		if err != nil {
			writeRoomServerError(w, err)
			return
		}
		writeJSON(w, http.StatusCreated, server)
	})

	mux.HandleFunc("GET /api/servers", func(w http.ResponseWriter, r *http.Request) {
		servers, err := matchService.ListGameServers(r.Context(), r.URL.Query().Get("match_mode"))
		if err != nil {
			writeRoomServerError(w, err)
			return
		}
		writeJSON(w, http.StatusOK, servers)
	})

	mux.HandleFunc("POST /api/servers/", func(w http.ResponseWriter, r *http.Request) {
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
		server, err := matchService.HeartbeatGameServer(r.Context(), serverID, repository.GameServerHeartbeat{
			Status:      req.Status,
			CurrentLoad: req.CurrentLoad,
		})
		if err != nil {
			writeRoomServerError(w, err)
			return
		}
		writeJSON(w, http.StatusOK, server)
	})

	mux.HandleFunc("POST /api/rank/score", func(w http.ResponseWriter, r *http.Request) {
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
		if err := rankService.UpdatePlayerScoreInLeaderboard(r.Context(), leaderboardType, req.PlayerID, float64(req.Score)); err != nil {
			writeRankHTTPError(w, err)
			return
		}
		player, err := rankService.GetPlayerRankInLeaderboard(r.Context(), leaderboardType, req.PlayerID)
		if err != nil {
			writeError(w, http.StatusInternalServerError, err)
			return
		}
		writeJSON(w, http.StatusOK, player)
	})

	mux.HandleFunc("GET /api/rank/top", func(w http.ResponseWriter, r *http.Request) {
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
		players, err := rankService.GetTopPlayersPage(r.Context(), rankLeaderboardType(r, ""), topN, offset)
		if err != nil {
			writeRankHTTPError(w, err)
			return
		}
		writeJSON(w, http.StatusOK, players)
	})

	mux.HandleFunc("GET /api/rank/player/", func(w http.ResponseWriter, r *http.Request) {
		playerID := strings.TrimPrefix(r.URL.Path, "/api/rank/player/")
		if playerID == "" {
			writeError(w, http.StatusBadRequest, errors.New("player_id is required"))
			return
		}
		player, err := rankService.GetPlayerRankInLeaderboard(r.Context(), rankLeaderboardType(r, ""), playerID)
		if err != nil {
			if errors.Is(err, service.ErrInvalidLeaderboardType) {
				writeError(w, http.StatusBadRequest, err)
				return
			}
			writeError(w, http.StatusInternalServerError, err)
			return
		}
		if player == nil {
			writeError(w, http.StatusNotFound, errors.New("player not found in rank"))
			return
		}
		writeJSON(w, http.StatusOK, player)
	})

	mux.HandleFunc("POST /api/match/pool", func(w http.ResponseWriter, r *http.Request) {
		var req struct {
			PlayerID string `json:"player_id"`
			MMRScore int64  `json:"mmr_score"`
		}
		if err := readJSON(r, &req); err != nil {
			writeError(w, http.StatusBadRequest, err)
			return
		}
		if req.PlayerID == "" {
			writeError(w, http.StatusBadRequest, errors.New("player_id is required"))
			return
		}
		if err := playerRepo.AddPlayerToPool(r.Context(), req.PlayerID, req.MMRScore); err != nil {
			writeError(w, http.StatusInternalServerError, err)
			return
		}
		writeJSON(w, http.StatusOK, map[string]any{
			"player_id": req.PlayerID,
			"mmr_score": req.MMRScore,
			"queued":    true,
		})
	})

	mux.HandleFunc("POST /api/match/tickets", func(w http.ResponseWriter, r *http.Request) {
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

		ticket, err := matchService.CreateTicket(r.Context(), service.CreateMatchTicketRequest{
			PlayerID:  req.PlayerID,
			MMRScore:  req.MMRScore,
			MatchMode: req.MatchMode,
			MaxWait:   time.Duration(req.MaxWaitMS) * time.Millisecond,
		})
		if err != nil {
			writeMatchError(w, err)
			return
		}
		writeJSON(w, http.StatusCreated, ticket)
	})

	mux.HandleFunc("GET /api/match/tickets/", func(w http.ResponseWriter, r *http.Request) {
		ticketID := strings.TrimPrefix(r.URL.Path, "/api/match/tickets/")
		ticket, err := matchService.GetTicket(r.Context(), ticketID)
		if err != nil {
			writeMatchError(w, err)
			return
		}
		writeJSON(w, http.StatusOK, ticket)
	})

	mux.HandleFunc("DELETE /api/match/tickets/", func(w http.ResponseWriter, r *http.Request) {
		ticketID := strings.TrimPrefix(r.URL.Path, "/api/match/tickets/")
		ticket, err := matchService.CancelTicket(r.Context(), ticketID)
		if err != nil {
			writeMatchError(w, err)
			return
		}
		writeJSON(w, http.StatusOK, ticket)
	})

	mux.HandleFunc("GET /api/match/results/", func(w http.ResponseWriter, r *http.Request) {
		matchID := strings.TrimPrefix(r.URL.Path, "/api/match/results/")
		result, err := matchService.GetResult(r.Context(), matchID)
		if err != nil {
			writeMatchError(w, err)
			return
		}
		writeJSON(w, http.StatusOK, result)
	})

	mux.HandleFunc("POST /api/matches/", func(w http.ResponseWriter, r *http.Request) {
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
		scores := make([]repository.SettlementScore, 0, len(req.Scores))
		for _, score := range req.Scores {
			scores = append(scores, repository.SettlementScore{PlayerID: score.PlayerID, Score: score.Score})
		}
		result, err := rankService.SettleMatch(r.Context(), matchID, leaderboardType, scores)
		if err != nil {
			writeSettlementError(w, err)
			return
		}
		updated := make([]gatewayRankPlayer, 0, len(result.Players))
		for _, player := range result.Players {
			updated = append(updated, gatewayRankPlayer{PlayerID: player.PlayerID, Score: player.Score, Rank: player.Rank})
		}

		writeJSON(w, http.StatusOK, map[string]any{
			"match_id":         matchID,
			"leaderboard_type": result.LeaderboardType,
			"updated_players":  updated,
			"idempotent":       result.Idempotent,
		})
	})

	mux.HandleFunc("DELETE /api/match/pool/", func(w http.ResponseWriter, r *http.Request) {
		playerID := strings.TrimPrefix(r.URL.Path, "/api/match/pool/")
		if playerID == "" {
			writeError(w, http.StatusBadRequest, errors.New("player_id is required"))
			return
		}
		if err := playerRepo.RemovePlayerFromPool(r.Context(), playerID); err != nil {
			writeError(w, http.StatusInternalServerError, err)
			return
		}
		writeJSON(w, http.StatusOK, map[string]any{
			"player_id": playerID,
			"queued":    false,
		})
	})

	return withGatewayProtection(mux)
}

func writeSettlementError(w http.ResponseWriter, err error) {
	switch {
	case errors.Is(err, repository.ErrResultNotFound):
		writeError(w, http.StatusNotFound, err)
	case errors.Is(err, repository.ErrSettlementConflict):
		writeError(w, http.StatusConflict, err)
	case errors.Is(err, repository.ErrInvalidIdentifier), errors.Is(err, service.ErrInvalidLeaderboardType),
		errors.Is(err, service.ErrInvalidSettlementPlayers), errors.Is(err, service.ErrDuplicateSettlementPlayer),
		errors.Is(err, service.ErrInvalidSettlementScore):
		writeError(w, http.StatusBadRequest, err)
	default:
		writeError(w, http.StatusInternalServerError, errors.New("internal settlement error"))
	}
}

func writeRankHTTPError(w http.ResponseWriter, err error) {
	if errors.Is(err, service.ErrInvalidLeaderboardType) || errors.Is(err, service.ErrInvalidRankScore) ||
		errors.Is(err, service.ErrInvalidRankPage) || errors.Is(err, repository.ErrInvalidIdentifier) {
		writeError(w, http.StatusBadRequest, err)
		return
	}
	writeError(w, http.StatusInternalServerError, errors.New("internal rank service error"))
}

func rankLeaderboardType(r *http.Request, bodyValue string) string {
	if value := r.URL.Query().Get("leaderboard_type"); value != "" {
		return value
	}
	if value := r.URL.Query().Get("board"); value != "" {
		return value
	}
	return bodyValue
}

func readJSON(r *http.Request, target any) error {
	defer r.Body.Close()
	if r.ContentLength > maxJSONBodyBytes {
		return fmt.Errorf("request body exceeds %d bytes", maxJSONBodyBytes)
	}
	limited := &io.LimitedReader{R: r.Body, N: maxJSONBodyBytes + 1}
	decoder := json.NewDecoder(limited)
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(target); err != nil {
		return err
	}
	if err := ensureSingleJSONValue(decoder); err != nil {
		return err
	}
	if limited.N <= 0 {
		return fmt.Errorf("request body exceeds %d bytes", maxJSONBodyBytes)
	}
	return nil
}

func readOptionalJSON(r *http.Request, target any) error {
	defer r.Body.Close()
	if r.ContentLength > maxJSONBodyBytes {
		return fmt.Errorf("request body exceeds %d bytes", maxJSONBodyBytes)
	}
	limited := &io.LimitedReader{R: r.Body, N: maxJSONBodyBytes + 1}
	decoder := json.NewDecoder(limited)
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(target); err != nil {
		if errors.Is(err, io.EOF) {
			return nil
		}
		return err
	}
	if err := ensureSingleJSONValue(decoder); err != nil {
		return err
	}
	if limited.N <= 0 {
		return fmt.Errorf("request body exceeds %d bytes", maxJSONBodyBytes)
	}
	return nil
}

func ensureSingleJSONValue(decoder *json.Decoder) error {
	var extra any
	if err := decoder.Decode(&extra); !errors.Is(err, io.EOF) {
		if err == nil {
			return errors.New("request body must contain a single JSON object")
		}
		return err
	}
	return nil
}

func writeJSON(w http.ResponseWriter, status int, payload any) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(payload)
}

func writeError(w http.ResponseWriter, status int, err error) {
	writeJSON(w, status, map[string]string{"error": err.Error()})
}

func writeMatchError(w http.ResponseWriter, err error) {
	switch {
	case errors.Is(err, context.DeadlineExceeded):
		writeError(w, http.StatusGatewayTimeout, err)
	case errors.Is(err, repository.ErrPlayerAlreadyQueued):
		writeError(w, http.StatusConflict, err)
	case errors.Is(err, repository.ErrTicketNotFound), errors.Is(err, repository.ErrResultNotFound):
		writeError(w, http.StatusNotFound, err)
	case errors.Is(err, repository.ErrTicketNotQueued), errors.Is(err, repository.ErrMatchModeMismatch), errors.Is(err, repository.ErrNoAvailableRoomServer):
		writeError(w, http.StatusConflict, err)
	case errors.Is(err, repository.ErrTooManyMatchModes):
		writeError(w, http.StatusTooManyRequests, err)
	case errors.Is(err, repository.ErrInvalidIdentifier), errors.Is(err, repository.ErrInvalidMatchMode),
		errors.Is(err, repository.ErrInvalidGameServer), errors.Is(err, service.ErrInvalidMMRScore),
		errors.Is(err, service.ErrInvalidMaxWait):
		writeError(w, http.StatusBadRequest, err)
	default:
		writeError(w, http.StatusInternalServerError, errors.New("internal match service error"))
	}
}

func writeRoomServerError(w http.ResponseWriter, err error) {
	switch {
	case errors.Is(err, repository.ErrGameServerNotFound):
		writeError(w, http.StatusNotFound, err)
	case errors.Is(err, repository.ErrNoAvailableRoomServer):
		writeError(w, http.StatusConflict, err)
	case errors.Is(err, repository.ErrInvalidIdentifier), errors.Is(err, repository.ErrInvalidMatchMode), errors.Is(err, repository.ErrInvalidGameServer):
		writeError(w, http.StatusBadRequest, err)
	default:
		writeError(w, http.StatusInternalServerError, errors.New("internal room server error"))
	}
}
