package roomserver

import (
	"bufio"
	"bytes"
	"context"
	"crypto/subtle"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log"
	"net"
	"net/http"
	"net/url"
	"sort"
	"strings"
	"sync"
	"time"
)

const (
	defaultHeartbeatInterval = 10 * time.Second
	requestMaxBytes          = 64 * 1024
	httpRequestTimeout       = 3 * time.Second
	connectionIdleTimeout    = 30 * time.Second
	defaultMaxConnections    = 1024
)

type Server struct {
	config Config
	client *http.Client

	mu             sync.Mutex
	rooms          map[string]*roomState
	playerRooms    map[string]string
	playerSessions map[string]string
	connections    chan struct{}
}

func NewServer(config Config) *Server {
	if config.MatchMode == "" {
		config.MatchMode = DefaultMatchMode
	}
	if strings.TrimSpace(config.PublicAddr) == "" {
		config.PublicAddr = config.Addr
	}
	if config.Capacity <= 0 {
		config.Capacity = DefaultCapacity
	}
	if config.HeartbeatInterval <= 0 {
		config.HeartbeatInterval = defaultHeartbeatInterval
	}
	if config.MaxConnections <= 0 {
		config.MaxConnections = defaultMaxConnections
	}
	return &Server{
		config:         config,
		client:         &http.Client{Timeout: httpRequestTimeout},
		rooms:          make(map[string]*roomState),
		playerRooms:    make(map[string]string),
		playerSessions: make(map[string]string),
		connections:    make(chan struct{}, config.MaxConnections),
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
		select {
		case s.connections <- struct{}{}:
			go func() {
				defer func() { <-s.connections }()
				s.handleConn(ctx, conn)
			}()
		default:
			_ = conn.Close()
		}
	}
}

func (s *Server) Register(ctx context.Context) error {
	if strings.TrimSpace(s.config.CoreRankHTTP) == "" {
		return nil
	}
	payload := GameServerRegistration{
		ServerID:    s.config.ServerID,
		ServerType:  DefaultServerType,
		Addr:        s.config.PublicAddr,
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
	sessionID := fmt.Sprintf("%s-%d", conn.RemoteAddr(), time.Now().UnixNano())
	joinedPlayers := make(map[string]string)
	defer func() {
		s.mu.Lock()
		defer s.mu.Unlock()
		now := time.Now()
		for playerID, roomID := range joinedPlayers {
			if s.playerSessions[playerID] == sessionID {
				s.removePlayerLocked(roomID, playerID, now)
			}
		}
	}()

	reader := bufio.NewScanner(conn)
	reader.Buffer(make([]byte, 0, 4096), requestMaxBytes)
	writer := bufio.NewWriter(conn)
	_ = conn.SetReadDeadline(time.Now().Add(connectionIdleTimeout))

	for reader.Scan() {
		_ = conn.SetReadDeadline(time.Now().Add(connectionIdleTimeout))
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
		for _, resp := range s.handleRequestWithSession(req, sessionID) {
			if resp.Type == TypeJoined {
				joinedPlayers[resp.PlayerID] = resp.RoomID
			} else if resp.Type == TypeLeft {
				delete(joinedPlayers, resp.PlayerID)
			}
			writeResponse(writer, resp)
		}
	}
	if err := reader.Err(); err != nil && !errors.Is(err, io.EOF) {
		_ = writeResponse(writer, Response{Type: TypeError, Message: err.Error()})
	}
}

func (s *Server) handleRequest(req Request) []Response {
	return s.handleRequestWithSession(req, "")
}

func (s *Server) handleRequestWithSession(req Request, sessionID string) []Response {
	switch req.Type {
	case TypeJoin:
		return s.join(req.RoomID, req.PlayerID, req.MatchID, req.JoinToken, sessionID)
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

func (s *Server) join(roomID, playerID, matchID, joinToken, sessionID string) []Response {
	if strings.TrimSpace(roomID) == "" {
		return []Response{{Type: TypeError, Message: "room_id is required"}}
	}
	if strings.TrimSpace(playerID) == "" {
		return []Response{{Type: TypeError, Message: "player_id is required"}}
	}
	if err := s.authorizeJoin(roomID, playerID, matchID, joinToken); err != nil {
		return []Response{{Type: TypeError, Message: err.Error()}}
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
	if sessionID != "" {
		s.playerSessions[playerID] = sessionID
	}

	return []Response{{
		Type:     TypeJoined,
		RoomID:   roomID,
		PlayerID: playerID,
		Players:  sortedKeys(room.players),
	}}
}

func (s *Server) authorizeJoin(roomID, playerID, matchID, joinToken string) error {
	if s.config.AllowUnverifiedJoin {
		return nil
	}
	if strings.TrimSpace(matchID) == "" || strings.TrimSpace(joinToken) == "" {
		return errors.New("match_id and join_token are required")
	}
	if strings.TrimSpace(s.config.CoreRankHTTP) == "" {
		return errors.New("match assignment verification is unavailable")
	}

	base := strings.TrimRight(s.config.CoreRankHTTP, "/")
	req, err := http.NewRequestWithContext(context.Background(), http.MethodGet,
		base+"/api/match/results/"+url.PathEscape(matchID), nil)
	if err != nil {
		return errors.New("invalid match assignment request")
	}
	s.addAPIKey(req)
	resp, err := s.client.Do(req)
	if err != nil {
		return errors.New("match assignment verification failed")
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return errors.New("match assignment was not found")
	}
	var assignment MatchAssignment
	decoder := json.NewDecoder(io.LimitReader(resp.Body, requestMaxBytes))
	if err := decoder.Decode(&assignment); err != nil {
		return errors.New("invalid match assignment response")
	}
	if assignment.MatchID != matchID || assignment.RoomID != roomID || assignment.ServerID != s.config.ServerID || assignment.Status != "matched" {
		return errors.New("match assignment does not target this room server")
	}
	if subtle.ConstantTimeCompare([]byte(assignment.JoinToken), []byte(joinToken)) != 1 {
		return errors.New("invalid join token")
	}
	for _, assignedPlayerID := range assignment.PlayerIDs {
		if assignedPlayerID == playerID {
			return nil
		}
	}
	return errors.New("player is not assigned to this match")
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
			delete(s.playerSessions, playerID)
		}
		return
	}
	delete(room.players, playerID)
	delete(room.readyPlayers, playerID)
	if currentRoomID, mapped := s.playerRooms[playerID]; mapped && currentRoomID == roomID {
		delete(s.playerRooms, playerID)
		delete(s.playerSessions, playerID)
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
	s.addAPIKey(req)

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

func (s *Server) addAPIKey(req *http.Request) {
	if apiKey := strings.TrimSpace(s.config.APIKey); apiKey != "" {
		req.Header.Set("X-CoreRank-API-Key", apiKey)
	}
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
