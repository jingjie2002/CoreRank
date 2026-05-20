package handler

import "net/http"

func handleHealth(w http.ResponseWriter, _ *http.Request) {
	writeJSON(w, http.StatusOK, map[string]string{"status": "ok"})
}

func handleAgentCapabilities(w http.ResponseWriter, _ *http.Request) {
	writeJSON(w, http.StatusOK, map[string]any{
		"agent_ready_version": "1.0",
		"project": map[string]string{
			"id":          "corerank",
			"name":        "CoreRank",
			"type":        "go-service",
			"description": "Game matchmaking and leaderboard backend service.",
		},
		"health": map[string]string{
			"primary": "/healthz",
			"legacy":  "/health",
		},
		"metrics": map[string]string{
			"prometheus": "/metrics",
			"default":    "http://127.0.0.1:9091/metrics",
		},
		"commands": []map[string]string{
			{"name": "test", "command": "go test ./...", "mode": "自动审查"},
			{"name": "vet", "command": "go vet ./...", "mode": "自动审查"},
			{"name": "agent_smoke", "command": "python scripts/agent_smoke.py", "mode": "自动审查"},
		},
		"capabilities": []string{
			"health",
			"metrics",
			"go_test",
			"go_vet",
			"leaderboard_query",
			"player_rank_query",
			"match_ticket_diagnose",
			"room_server_query",
			"agent_events",
			"agent_logs",
		},
		"read_tools": []string{
			"GET /healthz",
			"GET /metrics",
			"GET /api/agent/events",
			"GET /api/agent/logs",
			"GET /api/rank/top",
			"GET /api/rank/player/{player_id}",
			"GET /api/match/tickets/{ticket_id}",
			"GET /api/match/results/{match_id}",
			"GET /api/servers",
		},
		"write_tools": []map[string]string{
			{"endpoint": "POST /api/match/tickets", "mode": "完全访问权限", "note": "demo or confirmed diagnostic traffic only"},
			{"endpoint": "POST /api/matches/{match_id}/settle", "mode": "完全访问权限", "note": "requires explicit confirmation outside local demos"},
		},
		"forbidden": []string{
			"direct_redis_delete",
			"production_rank_write_without_confirm",
			"production_match_settlement_without_confirm",
			"production_deploy_without_confirm",
		},
		"docs": []string{
			"README.md",
			"docs/api.md",
			"docs/verification.md",
			"docs/agent-integration.md",
		},
	})
}

func handleAgentEvents(w http.ResponseWriter, _ *http.Request) {
	writeJSON(w, http.StatusOK, map[string]any{
		"status":  "ok",
		"project": "corerank",
		"events": []map[string]string{
			{"type": "health", "source": "GET /healthz"},
			{"type": "metrics", "source": "GET /metrics"},
			{"type": "match_ticket", "source": "GET /api/match/tickets/{ticket_id}"},
			{"type": "match_result", "source": "GET /api/match/results/{match_id}"},
		},
		"note": "CoreRank does not keep a local event buffer; this endpoint exposes agent-readable event sources.",
	})
}

func handleAgentLogs(w http.ResponseWriter, _ *http.Request) {
	writeJSON(w, http.StatusOK, map[string]any{
		"status":  "ok",
		"project": "corerank",
		"logs": []map[string]string{
			{"type": "http", "source": "process stdout/stderr"},
			{"type": "metrics", "source": "GET /metrics"},
		},
		"note": "CoreRank demo mode has no persisted application log store.",
	})
}
