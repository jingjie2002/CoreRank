package handler

import (
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"strconv"
	"strings"
	"time"

	"CoreRank/internal/repository"
	"CoreRank/internal/service"
)

// NewHTTPHandler exposes a small RESTful gateway on top of the same Redis-backed
// rank and match repository used by the gRPC service.
func NewHTTPHandler(rankService *service.RankService, playerRepo *repository.PlayerRepository, matchService *service.MatchService) http.Handler {
	mux := http.NewServeMux()

	mux.HandleFunc("GET /health", func(w http.ResponseWriter, r *http.Request) {
		writeJSON(w, http.StatusOK, map[string]string{"status": "ok"})
	})

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
			PlayerID string  `json:"player_id"`
			Score    float64 `json:"score"`
		}
		if err := readJSON(r, &req); err != nil {
			writeError(w, http.StatusBadRequest, err)
			return
		}
		if req.PlayerID == "" {
			writeError(w, http.StatusBadRequest, errors.New("player_id is required"))
			return
		}
		if err := rankService.UpdatePlayerScore(r.Context(), req.PlayerID, req.Score); err != nil {
			writeError(w, http.StatusInternalServerError, err)
			return
		}
		player, err := rankService.GetPlayerRank(r.Context(), req.PlayerID)
		if err != nil {
			writeError(w, http.StatusInternalServerError, err)
			return
		}
		writeJSON(w, http.StatusOK, player)
	})

	mux.HandleFunc("GET /api/rank/top", func(w http.ResponseWriter, r *http.Request) {
		topN, _ := strconv.ParseInt(r.URL.Query().Get("n"), 10, 64)
		players, err := rankService.GetTopPlayers(r.Context(), topN)
		if err != nil {
			writeError(w, http.StatusInternalServerError, err)
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
		player, err := rankService.GetPlayerRank(r.Context(), playerID)
		if err != nil {
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

	return mux
}

func readJSON(r *http.Request, target any) error {
	defer r.Body.Close()
	decoder := json.NewDecoder(r.Body)
	decoder.DisallowUnknownFields()
	return decoder.Decode(target)
}

func readOptionalJSON(r *http.Request, target any) error {
	defer r.Body.Close()
	decoder := json.NewDecoder(r.Body)
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(target); err != nil {
		if errors.Is(err, io.EOF) {
			return nil
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
	case errors.Is(err, repository.ErrPlayerAlreadyQueued):
		writeError(w, http.StatusConflict, err)
	case errors.Is(err, repository.ErrTicketNotFound), errors.Is(err, repository.ErrResultNotFound):
		writeError(w, http.StatusNotFound, err)
	case errors.Is(err, repository.ErrTicketNotQueued):
		writeError(w, http.StatusConflict, err)
	default:
		writeError(w, http.StatusBadRequest, err)
	}
}

func writeRoomServerError(w http.ResponseWriter, err error) {
	switch {
	case errors.Is(err, repository.ErrGameServerNotFound):
		writeError(w, http.StatusNotFound, err)
	case errors.Is(err, repository.ErrNoAvailableRoomServer):
		writeError(w, http.StatusConflict, err)
	default:
		writeError(w, http.StatusBadRequest, err)
	}
}
