package roomserver

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log"
	"net"
	"net/http"
	"sort"
	"strings"
	"sync"
	"time"
)

const (
	defaultHeartbeatInterval = 10 * time.Second
	requestMaxBytes          = 1024 * 1024
	httpRequestTimeout       = 3 * time.Second
)

type Server struct {
	config Config
	client *http.Client

	mu          sync.Mutex
	rooms       map[string]*roomState
	playerRooms map[string]string
}

func NewServer(config Config) *Server {
	if config.MatchMode == "" {
		config.MatchMode = DefaultMatchMode
	}
	if config.Capacity <= 0 {
		config.Capacity = DefaultCapacity
	}
	if config.HeartbeatInterval <= 0 {
		config.HeartbeatInterval = defaultHeartbeatInterval
	}
	return &Server{
		config:      config,
		client:      &http.Client{Timeout: httpRequestTimeout},
		rooms:       make(map[string]*roomState),
		playerRooms: make(map[string]string),
	}
}

func (s *Server) Config() Config {
	return s.config
}

func (s *Server) Serve(ctx context.Context, listener net.Listener) error {
	go func() {
		<-ctx.Done()
		_ = listener.Close()
	}()

	for {
		conn, err := listener.Accept()
		if err != nil {
			if ctx.Err() != nil || errors.Is(err, net.ErrClosed) {
				return nil
			}
			return err
		}
		go s.handleConn(ctx, conn)
	}
}

func (s *Server) Register(ctx context.Context) error {
	if strings.TrimSpace(s.config.CoreRankHTTP) == "" {
		return nil
	}
	payload := GameServerRegistration{
		ServerID:    s.config.ServerID,
		ServerType:  DefaultServerType,
		Addr:        s.config.Addr,
		Region:      DefaultRegion,
		MatchMode:   s.config.MatchMode,
		Capacity:    s.config.Capacity,
		CurrentLoad: s.currentLoad(),
		Status:      "active",
	}
	return s.postJSON(ctx, "/api/servers", payload)
}

func (s *Server) Heartbeat(ctx context.Context) error {
	if strings.TrimSpace(s.config.CoreRankHTTP) == "" {
		return nil
	}
	payload := GameServerHeartbeat{
		Status:      "active",
		CurrentLoad: s.currentLoad(),
	}
	return s.postJSON(ctx, fmt.Sprintf("/api/servers/%s/heartbeat", s.config.ServerID), payload)
}

func (s *Server) RunHeartbeat(ctx context.Context, logger *log.Logger) {
	ticker := time.NewTicker(s.config.HeartbeatInterval)
	defer ticker.Stop()

	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			if err := s.Heartbeat(ctx); err != nil && logger != nil {
				logger.Printf("roomserver heartbeat failed: %v", err)
			}
		}
	}
}

func (s *Server) RoomSnapshot(roomID string) (RoomSnapshot, bool) {
	s.mu.Lock()
	defer s.mu.Unlock()

	room, ok := s.rooms[roomID]
	if !ok {
		return RoomSnapshot{}, false
	}
	return room.snapshot(), true
}

func (s *Server) currentLoad() int64 {
	s.mu.Lock()
	defer s.mu.Unlock()
	return int64(len(s.playerRooms))
}

func (s *Server) handleConn(ctx context.Context, conn net.Conn) {
	defer conn.Close()

	reader := bufio.NewScanner(conn)
	reader.Buffer(make([]byte, 0, 4096), requestMaxBytes)
	writer := bufio.NewWriter(conn)

	for reader.Scan() {
		select {
		case <-ctx.Done():
			return
		default:
		}

		var req Request
		if err := json.Unmarshal(reader.Bytes(), &req); err != nil {
			writeResponse(writer, Response{Type: TypeError, Message: "invalid json request"})
			continue
		}
		for _, resp := range s.handleRequest(req) {
			writeResponse(writer, resp)
		}
	}
	if err := reader.Err(); err != nil && !errors.Is(err, io.EOF) {
		_ = writeResponse(writer, Response{Type: TypeError, Message: err.Error()})
	}
}

func (s *Server) handleRequest(req Request) []Response {
	switch req.Type {
	case TypeJoin:
		return s.join(req.RoomID, req.PlayerID)
	case TypeReady:
		return s.ready(req.RoomID, req.PlayerID)
	case TypeLeave:
		return s.leave(req.RoomID, req.PlayerID)
	case TypePing:
		return []Response{{Type: TypePong}}
	default:
		return []Response{{Type: TypeError, Message: "unsupported request type"}}
	}
}

func (s *Server) join(roomID, playerID string) []Response {
	if strings.TrimSpace(roomID) == "" {
		return []Response{{Type: TypeError, Message: "room_id is required"}}
	}
	if strings.TrimSpace(playerID) == "" {
		return []Response{{Type: TypeError, Message: "player_id is required"}}
	}

	s.mu.Lock()
	defer s.mu.Unlock()

	now := time.Now()
	if oldRoomID, ok := s.playerRooms[playerID]; ok && oldRoomID != roomID {
		s.removePlayerLocked(oldRoomID, playerID, now)
	}

	room := s.ensureRoomLocked(roomID, now)
	room.players[playerID] = struct{}{}
	room.updatedAt = now
	s.playerRooms[playerID] = roomID

	return []Response{{
		Type:     TypeJoined,
		RoomID:   roomID,
		PlayerID: playerID,
		Players:  sortedKeys(room.players),
	}}
}

func (s *Server) ready(roomID, playerID string) []Response {
	if strings.TrimSpace(roomID) == "" {
		return []Response{{Type: TypeError, Message: "room_id is required"}}
	}
	if strings.TrimSpace(playerID) == "" {
		return []Response{{Type: TypeError, Message: "player_id is required"}}
	}

	s.mu.Lock()
	defer s.mu.Unlock()

	room, ok := s.rooms[roomID]
	if !ok {
		return []Response{{Type: TypeError, Message: "room not found"}}
	}
	if _, ok := room.players[playerID]; !ok {
		return []Response{{Type: TypeError, Message: "player must join room before ready"}}
	}

	now := time.Now()
	room.readyPlayers[playerID] = struct{}{}
	room.updatedAt = now

	players := sortedKeys(room.players)
	readyPlayers := sortedKeys(room.readyPlayers)
	responses := []Response{{
		Type:         TypeReady,
		RoomID:       roomID,
		PlayerID:     playerID,
		ReadyPlayers: readyPlayers,
	}}
	if len(players) >= 2 && len(readyPlayers) == len(players) && !room.started {
		room.started = true
		responses = append(responses, Response{
			Type:    TypeRoomStarted,
			RoomID:  roomID,
			Players: players,
		})
	}
	return responses
}

func (s *Server) leave(roomID, playerID string) []Response {
	if strings.TrimSpace(roomID) == "" {
		return []Response{{Type: TypeError, Message: "room_id is required"}}
	}
	if strings.TrimSpace(playerID) == "" {
		return []Response{{Type: TypeError, Message: "player_id is required"}}
	}

	s.mu.Lock()
	defer s.mu.Unlock()

	now := time.Now()
	s.removePlayerLocked(roomID, playerID, now)
	return []Response{{
		Type:     TypeLeft,
		RoomID:   roomID,
		PlayerID: playerID,
	}}
}

func (s *Server) ensureRoomLocked(roomID string, now time.Time) *roomState {
	room, ok := s.rooms[roomID]
	if ok {
		return room
	}
	room = &roomState{
		roomID:       roomID,
		players:      make(map[string]struct{}),
		readyPlayers: make(map[string]struct{}),
		createdAt:    now,
		updatedAt:    now,
	}
	s.rooms[roomID] = room
	return room
}

func (s *Server) removePlayerLocked(roomID, playerID string, now time.Time) {
	room, ok := s.rooms[roomID]
	if !ok {
		if currentRoomID, mapped := s.playerRooms[playerID]; mapped && currentRoomID == roomID {
			delete(s.playerRooms, playerID)
		}
		return
	}
	delete(room.players, playerID)
	delete(room.readyPlayers, playerID)
	if currentRoomID, mapped := s.playerRooms[playerID]; mapped && currentRoomID == roomID {
		delete(s.playerRooms, playerID)
	}
	room.updatedAt = now
	if len(room.players) == 0 {
		delete(s.rooms, roomID)
	}
}

func (s *Server) postJSON(ctx context.Context, path string, payload any) error {
	body, err := json.Marshal(payload)
	if err != nil {
		return err
	}
	base := strings.TrimRight(s.config.CoreRankHTTP, "/")
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, base+path, bytes.NewReader(body))
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "application/json")

	resp, err := s.client.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()

	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return fmt.Errorf("CoreRank returned %s for %s", resp.Status, path)
	}
	return nil
}

func writeResponse(writer *bufio.Writer, resp Response) error {
	if err := json.NewEncoder(writer).Encode(resp); err != nil {
		return err
	}
	return writer.Flush()
}

func (r *roomState) snapshot() RoomSnapshot {
	return RoomSnapshot{
		RoomID:       r.roomID,
		Players:      sortedKeys(r.players),
		ReadyPlayers: sortedKeys(r.readyPlayers),
		Started:      r.started,
		CreatedAt:    r.createdAt,
		UpdatedAt:    r.updatedAt,
	}
}

func sortedKeys(values map[string]struct{}) []string {
	keys := make([]string, 0, len(values))
	for key := range values {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	return keys
}
