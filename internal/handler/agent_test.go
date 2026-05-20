package handler

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestHealthzCompatibility(t *testing.T) {
	handler := NewHTTPHandler(nil, nil, nil)

	for _, path := range []string{"/health", "/healthz"} {
		req := httptest.NewRequest(http.MethodGet, path, nil)
		rec := httptest.NewRecorder()

		handler.ServeHTTP(rec, req)

		if rec.Code != http.StatusOK {
			t.Fatalf("%s expected status 200, got %d", path, rec.Code)
		}
		var payload map[string]string
		if err := json.NewDecoder(rec.Body).Decode(&payload); err != nil {
			t.Fatalf("%s decode response: %v", path, err)
		}
		if payload["status"] != "ok" {
			t.Fatalf("%s expected ok status, got %#v", path, payload)
		}
	}
}

func TestAgentCapabilities(t *testing.T) {
	handler := NewHTTPHandler(nil, nil, nil)
	req := httptest.NewRequest(http.MethodGet, "/api/agent/capabilities", nil)
	rec := httptest.NewRecorder()

	handler.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("expected status 200, got %d", rec.Code)
	}
	var payload struct {
		Project struct {
			ID   string `json:"id"`
			Name string `json:"name"`
		} `json:"project"`
		Health       map[string]string `json:"health"`
		Capabilities []string          `json:"capabilities"`
	}
	if err := json.NewDecoder(rec.Body).Decode(&payload); err != nil {
		t.Fatalf("decode capabilities: %v", err)
	}
	if payload.Project.ID != "corerank" || payload.Project.Name != "CoreRank" {
		t.Fatalf("unexpected project identity: %#v", payload.Project)
	}
	if payload.Health["primary"] != "/healthz" || payload.Health["legacy"] != "/health" {
		t.Fatalf("unexpected health aliases: %#v", payload.Health)
	}
	if !containsString(payload.Capabilities, "match_ticket_diagnose") {
		t.Fatalf("expected match_ticket_diagnose capability, got %#v", payload.Capabilities)
	}
	if !containsString(payload.Capabilities, "agent_events") || !containsString(payload.Capabilities, "agent_logs") {
		t.Fatalf("expected agent event/log capabilities, got %#v", payload.Capabilities)
	}
}

func TestAgentEventsAndLogs(t *testing.T) {
	handler := NewHTTPHandler(nil, nil, nil)
	for _, path := range []string{"/api/agent/events", "/api/agent/logs"} {
		req := httptest.NewRequest(http.MethodGet, path, nil)
		rec := httptest.NewRecorder()

		handler.ServeHTTP(rec, req)

		if rec.Code != http.StatusOK {
			t.Fatalf("%s expected status 200, got %d", path, rec.Code)
		}
		var payload map[string]any
		if err := json.NewDecoder(rec.Body).Decode(&payload); err != nil {
			t.Fatalf("%s decode response: %v", path, err)
		}
		if payload["status"] != "ok" || payload["project"] != "corerank" {
			t.Fatalf("%s unexpected payload: %#v", path, payload)
		}
	}
}

func containsString(values []string, target string) bool {
	for _, value := range values {
		if value == target {
			return true
		}
	}
	return false
}
