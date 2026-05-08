package roomserver

import "time"

const (
	TypeJoin        = "join"
	TypeJoined      = "joined"
	TypeReady       = "ready"
	TypeRoomStarted = "room_started"
	TypeLeave       = "leave"
	TypeLeft        = "left"
	TypePing        = "ping"
	TypePong        = "pong"
	TypeError       = "error"

	DefaultServerType = "room"
	DefaultRegion     = "local"
	DefaultMatchMode  = "duel"
	DefaultCapacity   = 8
)

type Request struct {
	Type     string `json:"type"`
	RoomID   string `json:"room_id,omitempty"`
	PlayerID string `json:"player_id,omitempty"`
}

type Response struct {
	Type         string   `json:"type"`
	RoomID       string   `json:"room_id,omitempty"`
	PlayerID     string   `json:"player_id,omitempty"`
	Players      []string `json:"players,omitempty"`
	ReadyPlayers []string `json:"ready_players,omitempty"`
	Message      string   `json:"message,omitempty"`
}

type GameServerRegistration struct {
	ServerID    string `json:"server_id"`
	ServerType  string `json:"server_type"`
	Addr        string `json:"addr"`
	Region      string `json:"region"`
	MatchMode   string `json:"match_mode"`
	Capacity    int64  `json:"capacity"`
	CurrentLoad int64  `json:"current_load"`
	Status      string `json:"status"`
}

type GameServerHeartbeat struct {
	Status      string `json:"status,omitempty"`
	CurrentLoad int64  `json:"current_load"`
}

type Config struct {
	ServerID          string
	Addr              string
	CoreRankHTTP      string
	MatchMode         string
	Capacity          int64
	HeartbeatInterval time.Duration
}

type RoomSnapshot struct {
	RoomID       string
	Players      []string
	ReadyPlayers []string
	Started      bool
	CreatedAt    time.Time
	UpdatedAt    time.Time
}

type roomState struct {
	roomID       string
	players      map[string]struct{}
	readyPlayers map[string]struct{}
	started      bool
	createdAt    time.Time
	updatedAt    time.Time
}
