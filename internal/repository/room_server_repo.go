package repository

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"strconv"
	"time"

	"github.com/redis/go-redis/v9"
)

const (
	GameServerTypeRoom   = "room"
	GameServerTypeBattle = "battle"

	GameServerStatusActive    = "active"
	GameServerStatusDraining  = "draining"
	GameServerStatusUnhealthy = "unhealthy"

	RoomAssignmentStatusAssigned = "assigned"

	roomServerInfoPrefix       = "server:info:"
	roomServerIndexPrefix      = "server:index:"
	roomServerLoadPrefix       = "server:load:"
	RoomServerHeartbeatKey     = "server:heartbeat"
	roomAssignmentPrefix       = "room:assignment:"
	defaultRoomAssignmentTTL   = 24 * time.Hour
	defaultServerHeartbeatAge  = 30 * time.Second
	defaultRoomServerMatchMode = "default"
)

var (
	ErrGameServerNotFound    = errors.New("game server not found")
	ErrNoAvailableRoomServer = errors.New("no available room server")
)

type GameServer struct {
	ServerID        string `json:"server_id"`
	ServerType      string `json:"server_type"`
	Addr            string `json:"addr"`
	Region          string `json:"region"`
	MatchMode       string `json:"match_mode"`
	Capacity        int64  `json:"capacity"`
	CurrentLoad     int64  `json:"current_load"`
	Status          string `json:"status"`
	LastHeartbeatAt int64  `json:"last_heartbeat_at"`
	UpdatedAt       int64  `json:"updated_at"`
}

type GameServerHeartbeat struct {
	Status      string
	CurrentLoad *int64
}

type RoomAssignment struct {
	MatchID     string   `json:"match_id"`
	RoomID      string   `json:"room_id"`
	ServerID    string   `json:"server_id"`
	ServerAddr  string   `json:"server_addr"`
	MatchMode   string   `json:"match_mode"`
	PlayerIDs   []string `json:"player_ids"`
	Status      string   `json:"status"`
	CurrentLoad int64    `json:"current_load"`
	CreatedAt   int64    `json:"created_at"`
}

type RoomServerAllocationRequest struct {
	MatchID          string
	RoomID           string
	MatchMode        string
	PlayerIDs        []string
	HeartbeatTimeout time.Duration
	NowMS            int64
}

type RoomServerRepository struct {
	client *redis.Client
}

func NewRoomServerRepository(client *redis.Client) *RoomServerRepository {
	return &RoomServerRepository{client: client}
}

func (r *RoomServerRepository) RegisterGameServer(ctx context.Context, server GameServer) (*GameServer, error) {
	now := time.Now().UnixMilli()
	normalized, err := normalizeGameServer(server, now)
	if err != nil {
		return nil, err
	}

	if err := r.client.HSet(ctx, roomServerInfoKey(normalized.ServerID), normalized.toHash()).Err(); err != nil {
		return nil, err
	}
	if err := r.client.SAdd(ctx, roomServerIndexKey(normalized.MatchMode), normalized.ServerID).Err(); err != nil {
		return nil, err
	}
	if err := r.client.ZAdd(ctx, RoomServerHeartbeatKey, redis.Z{
		Score:  float64(normalized.LastHeartbeatAt),
		Member: normalized.ServerID,
	}).Err(); err != nil {
		return nil, err
	}
	if err := r.updateServerLoad(ctx, normalized); err != nil {
		return nil, err
	}

	return &normalized, nil
}

func (r *RoomServerRepository) HeartbeatGameServer(ctx context.Context, serverID string, heartbeat GameServerHeartbeat) (*GameServer, error) {
	server, err := r.GetGameServer(ctx, serverID)
	if err != nil {
		return nil, err
	}

	now := time.Now().UnixMilli()
	if heartbeat.Status != "" {
		if !isValidGameServerStatus(heartbeat.Status) {
			return nil, errors.New("status must be active, draining, or unhealthy")
		}
		server.Status = heartbeat.Status
	}
	if heartbeat.CurrentLoad != nil {
		if *heartbeat.CurrentLoad < 0 {
			return nil, errors.New("current_load must be greater than or equal to 0")
		}
		if *heartbeat.CurrentLoad > server.Capacity {
			return nil, errors.New("current_load must not exceed capacity")
		}
		server.CurrentLoad = *heartbeat.CurrentLoad
	}
	server.LastHeartbeatAt = now
	server.UpdatedAt = now

	if err := r.client.HSet(ctx, roomServerInfoKey(server.ServerID), server.toHash()).Err(); err != nil {
		return nil, err
	}
	if err := r.client.ZAdd(ctx, RoomServerHeartbeatKey, redis.Z{
		Score:  float64(server.LastHeartbeatAt),
		Member: server.ServerID,
	}).Err(); err != nil {
		return nil, err
	}
	if err := r.updateServerLoad(ctx, *server); err != nil {
		return nil, err
	}
	return server, nil
}

func (r *RoomServerRepository) GetGameServer(ctx context.Context, serverID string) (*GameServer, error) {
	values, err := r.client.HGetAll(ctx, roomServerInfoKey(serverID)).Result()
	if err != nil {
		return nil, err
	}
	if len(values) == 0 {
		return nil, ErrGameServerNotFound
	}
	server, err := gameServerFromHash(values)
	if err != nil {
		return nil, err
	}
	return &server, nil
}

func (r *RoomServerRepository) ListGameServers(ctx context.Context, matchMode string) ([]GameServer, error) {
	serverIDs, err := r.client.ZRange(ctx, RoomServerHeartbeatKey, 0, -1).Result()
	if err != nil {
		return nil, err
	}

	servers := make([]GameServer, 0, len(serverIDs))
	for _, serverID := range serverIDs {
		server, err := r.GetGameServer(ctx, serverID)
		if err != nil {
			if errors.Is(err, ErrGameServerNotFound) {
				continue
			}
			return nil, err
		}
		if matchMode != "" && server.MatchMode != matchMode {
			continue
		}
		servers = append(servers, *server)
	}
	return servers, nil
}

func (r *RoomServerRepository) AllocateRoomServer(ctx context.Context, req RoomServerAllocationRequest) (*RoomAssignment, error) {
	if req.MatchMode == "" {
		req.MatchMode = defaultRoomServerMatchMode
	}
	if req.HeartbeatTimeout <= 0 {
		req.HeartbeatTimeout = defaultServerHeartbeatAge
	}
	if req.NowMS <= 0 {
		req.NowMS = time.Now().UnixMilli()
	}
	if len(req.PlayerIDs) == 0 {
		return nil, errors.New("player_ids is required")
	}

	serverIDs, err := r.client.ZRange(ctx, roomServerLoadKey(req.MatchMode), 0, -1).Result()
	if err != nil {
		return nil, err
	}
	if len(serverIDs) == 0 {
		return nil, ErrNoAvailableRoomServer
	}

	reserveSlots := int64(len(req.PlayerIDs))
	for _, serverID := range serverIDs {
		server, err := r.GetGameServer(ctx, serverID)
		if err != nil {
			if errors.Is(err, ErrGameServerNotFound) {
				continue
			}
			return nil, err
		}
		if server.MatchMode != req.MatchMode {
			continue
		}

		result, err := ReserveRoomServerScript.Run(
			ctx,
			r.client,
			[]string{roomServerInfoKey(server.ServerID), roomServerLoadKey(server.MatchMode)},
			server.ServerID,
			req.NowMS,
			req.HeartbeatTimeout.Milliseconds(),
			GameServerStatusActive,
			reserveSlots,
		).Result()
		if err != nil {
			return nil, err
		}
		values, ok := result.([]interface{})
		if !ok || len(values) < 3 {
			return nil, fmt.Errorf("unexpected room server reserve script result: %#v", result)
		}
		if scriptInt(values[0]) != 1 {
			continue
		}

		currentLoad := scriptInt(values[1])
		return &RoomAssignment{
			MatchID:     req.MatchID,
			RoomID:      req.RoomID,
			ServerID:    server.ServerID,
			ServerAddr:  server.Addr,
			MatchMode:   req.MatchMode,
			PlayerIDs:   append([]string(nil), req.PlayerIDs...),
			Status:      RoomAssignmentStatusAssigned,
			CurrentLoad: currentLoad,
			CreatedAt:   req.NowMS,
		}, nil
	}

	return nil, ErrNoAvailableRoomServer
}

func (r *RoomServerRepository) ReleaseRoomServer(ctx context.Context, assignment RoomAssignment) error {
	if assignment.ServerID == "" || len(assignment.PlayerIDs) == 0 {
		return nil
	}
	if assignment.MatchMode == "" {
		assignment.MatchMode = defaultRoomServerMatchMode
	}
	now := time.Now().UnixMilli()
	_, err := ReleaseRoomServerScript.Run(
		ctx,
		r.client,
		[]string{roomServerInfoKey(assignment.ServerID), roomServerLoadKey(assignment.MatchMode)},
		assignment.ServerID,
		now,
		len(assignment.PlayerIDs),
	).Result()
	return err
}

func (r *RoomServerRepository) SaveRoomAssignment(ctx context.Context, assignment RoomAssignment) error {
	if assignment.MatchID == "" {
		return errors.New("match_id is required")
	}
	playersJSON, err := json.Marshal(assignment.PlayerIDs)
	if err != nil {
		return err
	}
	key := roomAssignmentKey(assignment.MatchID)
	if err := r.client.HSet(ctx, key, map[string]any{
		"match_id":     assignment.MatchID,
		"room_id":      assignment.RoomID,
		"server_id":    assignment.ServerID,
		"server_addr":  assignment.ServerAddr,
		"match_mode":   assignment.MatchMode,
		"player_ids":   string(playersJSON),
		"status":       assignment.Status,
		"current_load": assignment.CurrentLoad,
		"created_at":   assignment.CreatedAt,
	}).Err(); err != nil {
		return err
	}
	return r.client.Expire(ctx, key, defaultRoomAssignmentTTL).Err()
}

func normalizeGameServer(server GameServer, now int64) (GameServer, error) {
	if server.ServerID == "" {
		return GameServer{}, errors.New("server_id is required")
	}
	if server.Addr == "" {
		return GameServer{}, errors.New("addr is required")
	}
	if server.ServerType == "" {
		server.ServerType = GameServerTypeRoom
	}
	if !isValidGameServerType(server.ServerType) {
		return GameServer{}, errors.New("server_type must be room or battle")
	}
	if server.MatchMode == "" {
		server.MatchMode = defaultRoomServerMatchMode
	}
	if server.Status == "" {
		server.Status = GameServerStatusActive
	}
	if !isValidGameServerStatus(server.Status) {
		return GameServer{}, errors.New("status must be active, draining, or unhealthy")
	}
	if server.Capacity <= 0 {
		return GameServer{}, errors.New("capacity must be greater than 0")
	}
	if server.CurrentLoad < 0 {
		return GameServer{}, errors.New("current_load must be greater than or equal to 0")
	}
	if server.CurrentLoad > server.Capacity {
		return GameServer{}, errors.New("current_load must not exceed capacity")
	}
	if server.LastHeartbeatAt <= 0 {
		server.LastHeartbeatAt = now
	}
	server.UpdatedAt = now
	return server, nil
}

func isValidGameServerType(serverType string) bool {
	return serverType == GameServerTypeRoom || serverType == GameServerTypeBattle
}

func isValidGameServerStatus(status string) bool {
	return status == GameServerStatusActive ||
		status == GameServerStatusDraining ||
		status == GameServerStatusUnhealthy
}

func (r *RoomServerRepository) updateServerLoad(ctx context.Context, server GameServer) error {
	return r.client.ZAdd(ctx, roomServerLoadKey(server.MatchMode), redis.Z{
		Score:  loadRatio(server.CurrentLoad, server.Capacity),
		Member: server.ServerID,
	}).Err()
}

func (s GameServer) toHash() map[string]any {
	return map[string]any{
		"server_id":         s.ServerID,
		"server_type":       s.ServerType,
		"addr":              s.Addr,
		"region":            s.Region,
		"match_mode":        s.MatchMode,
		"capacity":          s.Capacity,
		"current_load":      s.CurrentLoad,
		"status":            s.Status,
		"last_heartbeat_at": s.LastHeartbeatAt,
		"updated_at":        s.UpdatedAt,
	}
}

func gameServerFromHash(values map[string]string) (GameServer, error) {
	server := GameServer{
		ServerID:   values["server_id"],
		ServerType: values["server_type"],
		Addr:       values["addr"],
		Region:     values["region"],
		MatchMode:  values["match_mode"],
		Status:     values["status"],
	}
	var err error
	if server.Capacity, err = parseOptionalIntField(values, "capacity"); err != nil {
		return GameServer{}, err
	}
	if server.CurrentLoad, err = parseOptionalIntField(values, "current_load"); err != nil {
		return GameServer{}, err
	}
	if server.LastHeartbeatAt, err = parseOptionalIntField(values, "last_heartbeat_at"); err != nil {
		return GameServer{}, err
	}
	if server.UpdatedAt, err = parseOptionalIntField(values, "updated_at"); err != nil {
		return GameServer{}, err
	}
	return server, nil
}

func parseOptionalIntField(values map[string]string, field string) (int64, error) {
	value := values[field]
	if value == "" {
		return 0, nil
	}
	return strconv.ParseInt(value, 10, 64)
}

func scriptInt(value interface{}) int64 {
	switch v := value.(type) {
	case int64:
		return v
	case int:
		return int64(v)
	case string:
		parsed, _ := strconv.ParseInt(v, 10, 64)
		return parsed
	case []byte:
		parsed, _ := strconv.ParseInt(string(v), 10, 64)
		return parsed
	default:
		return 0
	}
}

func loadRatio(currentLoad int64, capacity int64) float64 {
	if capacity <= 0 {
		return math.MaxFloat64
	}
	return float64(currentLoad) / float64(capacity)
}

func roomServerInfoKey(serverID string) string {
	return roomServerInfoPrefix + serverID
}

func roomServerIndexKey(matchMode string) string {
	return roomServerIndexPrefix + matchMode
}

func roomServerLoadKey(matchMode string) string {
	return roomServerLoadPrefix + matchMode
}

func roomAssignmentKey(matchID string) string {
	return roomAssignmentPrefix + matchID
}

var ReserveRoomServerScript = redis.NewScript(`
local info_key = KEYS[1]
local load_key = KEYS[2]

local server_id = ARGV[1]
local now_ms = tonumber(ARGV[2])
local max_heartbeat_age_ms = tonumber(ARGV[3])
local active_status = ARGV[4]
local reserve_slots = tonumber(ARGV[5])

if redis.call('EXISTS', info_key) == 0 then
    return {0, 0, 'not_found'}
end

local status = redis.call('HGET', info_key, 'status')
if status ~= active_status then
    return {0, 0, 'not_active'}
end

local last_heartbeat_at = tonumber(redis.call('HGET', info_key, 'last_heartbeat_at') or '0')
if last_heartbeat_at <= 0 or now_ms - last_heartbeat_at > max_heartbeat_age_ms then
    return {0, 0, 'stale'}
end

local capacity = tonumber(redis.call('HGET', info_key, 'capacity') or '0')
local current_load = tonumber(redis.call('HGET', info_key, 'current_load') or '0')
if capacity <= 0 or current_load + reserve_slots > capacity then
    return {0, current_load, 'capacity'}
end

local new_load = current_load + reserve_slots
redis.call('HSET', info_key, 'current_load', new_load, 'updated_at', now_ms)
redis.call('ZADD', load_key, new_load / capacity, server_id)

return {1, new_load, 'reserved'}
`)

var ReleaseRoomServerScript = redis.NewScript(`
local info_key = KEYS[1]
local load_key = KEYS[2]

local server_id = ARGV[1]
local now_ms = tonumber(ARGV[2])
local release_slots = tonumber(ARGV[3])

if redis.call('EXISTS', info_key) == 0 then
    return 0
end

local capacity = tonumber(redis.call('HGET', info_key, 'capacity') or '0')
local current_load = tonumber(redis.call('HGET', info_key, 'current_load') or '0')
local new_load = current_load - release_slots
if new_load < 0 then
    new_load = 0
end

redis.call('HSET', info_key, 'current_load', new_load, 'updated_at', now_ms)
if capacity > 0 then
    redis.call('ZADD', load_key, new_load / capacity, server_id)
end

return new_load
`)
