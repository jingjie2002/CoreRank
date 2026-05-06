package handler

import (
	"encoding/json"
	"errors"
	"net/http"
	"strconv"
	"strings"

	"CoreRank/internal/repository"
	"CoreRank/internal/service"
)

// NewHTTPHandler exposes a small RESTful gateway on top of the same Redis-backed
// rank and match repository used by the gRPC service.
func NewHTTPHandler(rankService *service.RankService, playerRepo *repository.PlayerRepository) http.Handler {
	mux := http.NewServeMux()

	mux.HandleFunc("GET /health", func(w http.ResponseWriter, r *http.Request) {
		writeJSON(w, http.StatusOK, map[string]string{"status": "ok"})
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

func writeJSON(w http.ResponseWriter, status int, payload any) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(payload)
}

func writeError(w http.ResponseWriter, status int, err error) {
	writeJSON(w, status, map[string]string{"error": err.Error()})
}
