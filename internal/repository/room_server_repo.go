package repository

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strconv"
	"strings"
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
	RoomAssignmentStatusReleased = "released"

	roomServerInfoPrefix       = "server:info:"
	roomServerIndexPrefix      = "server:index:"
	roomServerLoadPrefix       = "server:load:"
	RoomServerHeartbeatKey     = "server:heartbeat"
	roomAssignmentPrefix       = "room:assignment:"
	RoomAssignmentExpiryKey    = "room:assignment:expiry"
	defaultRoomAssignmentTTL   = 24 * time.Hour
	defaultRoomAssignmentLease = 2 * time.Hour
	defaultServerHeartbeatAge  = 30 * time.Second
	defaultRoomServerMatchMode = "default"
)

var (
	ErrGameServerNotFound    = errors.New("game server not found")
	ErrNoAvailableRoomServer = errors.New("no available room server")
	ErrInvalidGameServer     = errors.New("invalid game server")
)

type GameServer struct {
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
	if existing, getErr := r.GetGameServer(ctx, normalized.ServerID); getErr == nil {
		// Re-registration reports the process load but must not erase slots that
		// have already been reserved by the allocator.
		normalized.CurrentLoad = existing.CurrentLoad
		if normalized.MatchMode != existing.MatchMode {
			return nil, fmt.Errorf("%w: match_mode cannot change while a server is registered", ErrInvalidGameServer)
		}
	} else if !errors.Is(getErr, ErrGameServerNotFound) {
		return nil, getErr
	}

	serverHash := normalized.toHash()
	delete(serverHash, "current_load")
	if err := r.client.HSet(ctx, roomServerInfoKey(normalized.ServerID), serverHash).Err(); err != nil {
		return nil, err
	}
	if err := r.client.HSetNX(ctx, roomServerInfoKey(normalized.ServerID), "current_load", normalized.CurrentLoad).Err(); err != nil {
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
	latest, err := r.GetGameServer(ctx, normalized.ServerID)
	if err != nil {
		return nil, err
	}
	if err := r.updateServerLoad(ctx, *latest); err != nil {
		return nil, err
	}

	return latest, nil
}

func (r *RoomServerRepository) HeartbeatGameServer(ctx context.Context, serverID string, heartbeat GameServerHeartbeat) (*GameServer, error) {
	if err := ValidateIdentifier("server_id", serverID, 64); err != nil {
		return nil, err
	}
	server, err := r.GetGameServer(ctx, serverID)
	if err != nil {
		return nil, err
	}

	now := time.Now().UnixMilli()
	if heartbeat.Status != "" {
		if !isValidGameServerStatus(heartbeat.Status) {
			return nil, fmt.Errorf("%w: status must be active, draining, or unhealthy", ErrInvalidGameServer)
		}
		server.Status = heartbeat.Status
	}
	if heartbeat.CurrentLoad != nil {
		if *heartbeat.CurrentLoad < 0 {
			return nil, fmt.Errorf("%w: current_load must be greater than or equal to 0", ErrInvalidGameServer)
		}
		if *heartbeat.CurrentLoad > server.Capacity {
			return nil, fmt.Errorf("%w: current_load must not exceed capacity", ErrInvalidGameServer)
		}
		server.ObservedLoad = *heartbeat.CurrentLoad
	}
	server.LastHeartbeatAt = now
	server.UpdatedAt = now

	if err := r.client.HSet(ctx, roomServerInfoKey(server.ServerID), map[string]any{
		"status":            server.Status,
		"observed_load":     server.ObservedLoad,
		"last_heartbeat_at": server.LastHeartbeatAt,
		"updated_at":        server.UpdatedAt,
	}).Err(); err != nil {
		return nil, err
	}
	if err := r.client.ZAdd(ctx, RoomServerHeartbeatKey, redis.Z{
		Score:  float64(server.LastHeartbeatAt),
		Member: server.ServerID,
	}).Err(); err != nil {
		return nil, err
	}
	return r.GetGameServer(ctx, serverID)
}

func (r *RoomServerRepository) GetGameServer(ctx context.Context, serverID string) (*GameServer, error) {
	if err := ValidateIdentifier("server_id", serverID, 64); err != nil {
		return nil, err
	}
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
	if matchMode != "" {
		mode, err := NormalizeMatchMode(matchMode)
		if err != nil {
			return nil, err
		}
		matchMode = mode
	}
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
	mode, err := NormalizeMatchMode(req.MatchMode)
	if err != nil {
		return nil, err
	}
	req.MatchMode = mode
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
	playersJSON, err := json.Marshal(req.PlayerIDs)
	if err != nil {
		return nil, err
	}
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
			[]string{roomServerInfoKey(server.ServerID), roomServerLoadKey(server.MatchMode), roomAssignmentKey(req.MatchID), RoomAssignmentExpiryKey},
			server.ServerID,
			req.NowMS,
			req.HeartbeatTimeout.Milliseconds(),
			GameServerStatusActive,
			reserveSlots,
			req.MatchID,
			req.RoomID,
			req.MatchMode,
			string(playersJSON),
			RoomAssignmentStatusAssigned,
			defaultRoomAssignmentTTL.Milliseconds(),
			req.NowMS+defaultRoomAssignmentLease.Milliseconds(),
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
		[]string{roomServerInfoKey(assignment.ServerID), roomServerLoadKey(assignment.MatchMode), roomAssignmentKey(assignment.MatchID), RoomAssignmentExpiryKey},
		assignment.ServerID,
		now,
		len(assignment.PlayerIDs),
		RoomAssignmentStatusAssigned,
		RoomAssignmentStatusReleased,
		assignment.MatchID,
	).Result()
	return err
}

func (r *RoomServerRepository) ReleaseExpiredRoomAssignments(ctx context.Context, nowMS int64, limit int64) (int, error) {
	if nowMS <= 0 {
		nowMS = time.Now().UnixMilli()
	}
	if limit <= 0 || limit > 1000 {
		limit = 100
	}
	matchIDs, err := r.client.ZRangeByScore(ctx, RoomAssignmentExpiryKey, &redis.ZRangeBy{
		Min: "-inf", Max: strconv.FormatInt(nowMS, 10), Offset: 0, Count: limit,
	}).Result()
	if err != nil {
		return 0, err
	}
	released := 0
	for _, matchID := range matchIDs {
		values, err := r.client.HGetAll(ctx, roomAssignmentKey(matchID)).Result()
		if err != nil {
			return released, err
		}
		if len(values) == 0 || values["status"] != RoomAssignmentStatusAssigned {
			if err := r.client.ZRem(ctx, RoomAssignmentExpiryKey, matchID).Err(); err != nil {
				return released, err
			}
			continue
		}
		var playerIDs []string
		if err := json.Unmarshal([]byte(values["player_ids"]), &playerIDs); err != nil {
			return released, fmt.Errorf("parse room assignment %s players: %w", matchID, err)
		}
		if err := r.ReleaseRoomServer(ctx, RoomAssignment{
			MatchID: matchID, ServerID: values["server_id"], MatchMode: values["match_mode"], PlayerIDs: playerIDs,
		}); err != nil {
			return released, err
		}
		released++
	}
	return released, nil
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
	if err := ValidateIdentifier("server_id", server.ServerID, 64); err != nil {
		return GameServer{}, err
	}
	server.Addr = strings.TrimSpace(server.Addr)
	if server.Addr == "" {
		return GameServer{}, fmt.Errorf("%w: addr is required", ErrInvalidGameServer)
	}
	if len(server.Addr) > 255 {
		return GameServer{}, fmt.Errorf("%w: addr length must be <= 255", ErrInvalidGameServer)
	}
	if server.ServerType == "" {
		server.ServerType = GameServerTypeRoom
	}
	if !isValidGameServerType(server.ServerType) {
		return GameServer{}, fmt.Errorf("%w: server_type must be room or battle", ErrInvalidGameServer)
	}
	matchMode, err := NormalizeMatchMode(server.MatchMode)
	if err != nil {
		return GameServer{}, err
	}
	server.MatchMode = matchMode
	if server.Status == "" {
		server.Status = GameServerStatusActive
	}
	if !isValidGameServerStatus(server.Status) {
		return GameServer{}, fmt.Errorf("%w: status must be active, draining, or unhealthy", ErrInvalidGameServer)
	}
	if server.Capacity <= 0 {
		return GameServer{}, fmt.Errorf("%w: capacity must be greater than 0", ErrInvalidGameServer)
	}
	if server.Capacity > 100000 {
		return GameServer{}, fmt.Errorf("%w: capacity must not exceed 100000", ErrInvalidGameServer)
	}
	if server.CurrentLoad < 0 {
		return GameServer{}, fmt.Errorf("%w: current_load must be greater than or equal to 0", ErrInvalidGameServer)
	}
	if server.CurrentLoad > server.Capacity {
		return GameServer{}, fmt.Errorf("%w: current_load must not exceed capacity", ErrInvalidGameServer)
	}
	if server.ObservedLoad == 0 && server.CurrentLoad > 0 {
		server.ObservedLoad = server.CurrentLoad
	}
	if server.ObservedLoad < 0 || server.ObservedLoad > server.Capacity {
		return GameServer{}, fmt.Errorf("%w: observed_load must be between 0 and capacity", ErrInvalidGameServer)
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
	_, err := UpdateRoomServerLoadScript.Run(ctx, r.client,
		[]string{roomServerInfoKey(server.ServerID), roomServerLoadKey(server.MatchMode)},
		server.ServerID,
	).Result()
	return err
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
		"observed_load":     s.ObservedLoad,
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
	if server.ObservedLoad, err = parseOptionalIntField(values, "observed_load"); err != nil {
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
local assignment_key = KEYS[3]
local assignment_expiry_key = KEYS[4]

local server_id = ARGV[1]
local now_ms = tonumber(ARGV[2])
local max_heartbeat_age_ms = tonumber(ARGV[3])
local active_status = ARGV[4]
local reserve_slots = tonumber(ARGV[5])
local match_id = ARGV[6]
local room_id = ARGV[7]
local match_mode = ARGV[8]
local players_json = ARGV[9]
local assigned_status = ARGV[10]
local assignment_ttl_ms = tonumber(ARGV[11])
local assignment_expires_at = tonumber(ARGV[12])

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
redis.call('HSET', assignment_key,
    'match_id', match_id,
    'room_id', room_id,
    'server_id', server_id,
    'server_addr', redis.call('HGET', info_key, 'addr') or '',
    'match_mode', match_mode,
    'player_ids', players_json,
    'status', assigned_status,
    'current_load', new_load,
    'created_at', now_ms)
redis.call('PEXPIRE', assignment_key, assignment_ttl_ms)
redis.call('ZADD', assignment_expiry_key, assignment_expires_at, match_id)

return {1, new_load, 'reserved'}
`)

var UpdateRoomServerLoadScript = redis.NewScript(`
local info_key = KEYS[1]
local load_key = KEYS[2]
local server_id = ARGV[1]
local capacity = tonumber(redis.call('HGET', info_key, 'capacity') or '0')
local current_load = tonumber(redis.call('HGET', info_key, 'current_load') or '0')
if capacity <= 0 then
    redis.call('ZREM', load_key, server_id)
    return 0
end
redis.call('ZADD', load_key, current_load / capacity, server_id)
return 1
`)

var ReleaseRoomServerScript = redis.NewScript(`
local info_key = KEYS[1]
local load_key = KEYS[2]
local assignment_key = KEYS[3]
local assignment_expiry_key = KEYS[4]

local server_id = ARGV[1]
local now_ms = tonumber(ARGV[2])
local release_slots = tonumber(ARGV[3])
local assigned_status = ARGV[4]
local released_status = ARGV[5]
local match_id = ARGV[6]

if redis.call('EXISTS', info_key) == 0 then
    redis.call('ZREM', assignment_expiry_key, match_id)
    return 0
end

if redis.call('EXISTS', assignment_key) == 1 and
   redis.call('HGET', assignment_key, 'status') ~= assigned_status then
    redis.call('ZREM', assignment_expiry_key, match_id)
    return tonumber(redis.call('HGET', info_key, 'current_load') or '0')
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
if redis.call('EXISTS', assignment_key) == 1 then
    redis.call('HSET', assignment_key, 'status', released_status, 'released_at', now_ms)
end
redis.call('ZREM', assignment_expiry_key, match_id)

return new_load
`)
