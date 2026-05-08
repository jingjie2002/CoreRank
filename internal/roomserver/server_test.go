package roomserver

import (
	"context"
	"encoding/json"
	"net"
	"net/http"
	"net/http/httptest"
	"reflect"
	"testing"
	"time"
)

func TestRoomServerJoinReadyLeave(t *testing.T) {
	server := NewServer(Config{ServerID: "test-room-1", Addr: "127.0.0.1:0"})
	client, cleanup := startTestServer(t, server)
	defer cleanup()

	sendRequest(t, client, Request{Type: TypeJoin, RoomID: "room_1", PlayerID: "p4"})
	assertResponse(t, client, Response{
		Type:     TypeJoined,
		RoomID:   "room_1",
		PlayerID: "p4",
		Players:  []string{"p4"},
	})

	sendRequest(t, client, Request{Type: TypeJoin, RoomID: "room_1", PlayerID: "p5"})
	assertResponse(t, client, Response{
		Type:     TypeJoined,
		RoomID:   "room_1",
		PlayerID: "p5",
		Players:  []string{"p4", "p5"},
	})

	sendRequest(t, client, Request{Type: TypeReady, RoomID: "room_1", PlayerID: "p4"})
	assertResponse(t, client, Response{
		Type:         TypeReady,
		RoomID:       "room_1",
		PlayerID:     "p4",
		ReadyPlayers: []string{"p4"},
	})

	sendRequest(t, client, Request{Type: TypeReady, RoomID: "room_1", PlayerID: "p5"})
	assertResponse(t, client, Response{
		Type:         TypeReady,
		RoomID:       "room_1",
		PlayerID:     "p5",
		ReadyPlayers: []string{"p4", "p5"},
	})
	assertResponse(t, client, Response{
		Type:    TypeRoomStarted,
		RoomID:  "room_1",
		Players: []string{"p4", "p5"},
	})

	snapshot, ok := server.RoomSnapshot("room_1")
	if !ok {
		t.Fatal("expected room snapshot")
	}
	if !snapshot.Started {
		t.Fatal("expected room to be started")
	}

	sendRequest(t, client, Request{Type: TypeLeave, RoomID: "room_1", PlayerID: "p4"})
	assertResponse(t, client, Response{
		Type:     TypeLeft,
		RoomID:   "room_1",
		PlayerID: "p4",
	})
}

func TestRoomServerRejectsReadyBeforeJoin(t *testing.T) {
	server := NewServer(Config{ServerID: "test-room-1", Addr: "127.0.0.1:0"})
	client, cleanup := startTestServer(t, server)
	defer cleanup()

	sendRequest(t, client, Request{Type: TypeReady, RoomID: "room_1", PlayerID: "p4"})
	resp := readResponse(t, client)
	if resp.Type != TypeError {
		t.Fatalf("expected error response, got %#v", resp)
	}
	if resp.Message != "room not found" {
		t.Fatalf("unexpected error message: %s", resp.Message)
	}
}

func TestRoomServerPing(t *testing.T) {
	server := NewServer(Config{ServerID: "test-room-1", Addr: "127.0.0.1:0"})
	client, cleanup := startTestServer(t, server)
	defer cleanup()

	sendRequest(t, client, Request{Type: TypePing})
	assertResponse(t, client, Response{Type: TypePong})
}

func TestRoomServerRegisterAndHeartbeat(t *testing.T) {
	var requests []struct {
		Path string
		Body map[string]any
	}
	httpServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var body map[string]any
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			t.Fatalf("decode request body: %v", err)
		}
		requests = append(requests, struct {
			Path string
			Body map[string]any
		}{Path: r.URL.Path, Body: body})
		w.WriteHeader(http.StatusCreated)
		_, _ = w.Write([]byte(`{}`))
	}))
	defer httpServer.Close()

	server := NewServer(Config{
		ServerID:     "demo-room-1",
		Addr:         "127.0.0.1:7001",
		CoreRankHTTP: httpServer.URL,
		MatchMode:    "duel",
		Capacity:     8,
	})
	_ = server.handleRequest(Request{Type: TypeJoin, RoomID: "room_1", PlayerID: "p4"})

	if err := server.Register(context.Background()); err != nil {
		t.Fatalf("register: %v", err)
	}
	if err := server.Heartbeat(context.Background()); err != nil {
		t.Fatalf("heartbeat: %v", err)
	}

	if len(requests) != 2 {
		t.Fatalf("expected 2 requests, got %d", len(requests))
	}
	if requests[0].Path != "/api/servers" {
		t.Fatalf("unexpected register path: %s", requests[0].Path)
	}
	if requests[0].Body["server_id"] != "demo-room-1" || requests[0].Body["addr"] != "127.0.0.1:7001" {
		t.Fatalf("unexpected register payload: %#v", requests[0].Body)
	}
	if requests[1].Path != "/api/servers/demo-room-1/heartbeat" {
		t.Fatalf("unexpected heartbeat path: %s", requests[1].Path)
	}
	if requests[1].Body["current_load"] != float64(1) {
		t.Fatalf("unexpected heartbeat load: %#v", requests[1].Body)
	}
}

type testClient struct {
	conn    net.Conn
	decoder *json.Decoder
}

func startTestServer(t *testing.T, server *Server) (*testClient, func()) {
	t.Helper()

	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("listen: %v", err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() {
		done <- server.Serve(ctx, listener)
	}()

	conn, err := net.Dial("tcp", listener.Addr().String())
	if err != nil {
		cancel()
		_ = listener.Close()
		t.Fatalf("dial: %v", err)
	}

	cleanup := func() {
		_ = conn.Close()
		cancel()
		_ = listener.Close()
		select {
		case err := <-done:
			if err != nil {
				t.Fatalf("serve returned error: %v", err)
			}
		case <-time.After(2 * time.Second):
			t.Fatal("server did not stop")
		}
	}
	return &testClient{conn: conn, decoder: json.NewDecoder(conn)}, cleanup
}

func sendRequest(t *testing.T, client *testClient, req Request) {
	t.Helper()

	raw, err := json.Marshal(req)
	if err != nil {
		t.Fatalf("marshal request: %v", err)
	}
	_ = client.conn.SetWriteDeadline(time.Now().Add(2 * time.Second))
	if _, err := client.conn.Write(append(raw, '\n')); err != nil {
		t.Fatalf("write request: %v", err)
	}
}

func readResponse(t *testing.T, client *testClient) Response {
	t.Helper()

	var resp Response
	_ = client.conn.SetReadDeadline(time.Now().Add(2 * time.Second))
	if err := client.decoder.Decode(&resp); err != nil {
		t.Fatalf("read response: %v", err)
	}
	return resp
}

func assertResponse(t *testing.T, client *testClient, expected Response) {
	t.Helper()

	actual := readResponse(t, client)
	if !reflect.DeepEqual(actual, expected) {
		t.Fatalf("unexpected response\nwant: %#v\n got: %#v", expected, actual)
	}
}
