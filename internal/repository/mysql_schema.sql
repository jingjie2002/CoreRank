CREATE TABLE IF NOT EXISTS players (
  player_id VARCHAR(64) PRIMARY KEY,
  rank_score BIGINT NOT NULL DEFAULT 0,
  mmr_score BIGINT NOT NULL DEFAULT 0,
  created_at_ms BIGINT NOT NULL,
  updated_at_ms BIGINT NOT NULL
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4 COLLATE=utf8mb4_0900_ai_ci;

CREATE TABLE IF NOT EXISTS match_tickets (
  ticket_id VARCHAR(80) PRIMARY KEY,
  player_id VARCHAR(64) NOT NULL,
  mmr_score BIGINT NOT NULL,
  match_mode VARCHAR(32) NOT NULL,
  status VARCHAR(32) NOT NULL,
  match_id VARCHAR(80) NOT NULL DEFAULT '',
  room_id VARCHAR(80) NOT NULL DEFAULT '',
  created_at_ms BIGINT NOT NULL,
  updated_at_ms BIGINT NOT NULL,
  expires_at_ms BIGINT NOT NULL,
  INDEX idx_match_tickets_player_status (player_id, status),
  INDEX idx_match_tickets_match_id (match_id)
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4 COLLATE=utf8mb4_0900_ai_ci;

CREATE TABLE IF NOT EXISTS match_results (
  match_id VARCHAR(80) PRIMARY KEY,
  room_id VARCHAR(80) NOT NULL,
  server_id VARCHAR(80) NOT NULL DEFAULT '',
  server_addr VARCHAR(255) NOT NULL DEFAULT '',
  match_mode VARCHAR(32) NOT NULL,
  player_ids JSON NOT NULL,
  status VARCHAR(32) NOT NULL,
  created_at_ms BIGINT NOT NULL,
  INDEX idx_match_results_room_id (room_id),
  INDEX idx_match_results_server_id (server_id)
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4 COLLATE=utf8mb4_0900_ai_ci;

CREATE TABLE IF NOT EXISTS rank_snapshots (
  snapshot_id BIGINT AUTO_INCREMENT PRIMARY KEY,
  player_id VARCHAR(64) NOT NULL,
  rank_score BIGINT NOT NULL,
  rank_position BIGINT NOT NULL,
  captured_at_ms BIGINT NOT NULL,
  INDEX idx_rank_snapshots_captured_at (captured_at_ms),
  INDEX idx_rank_snapshots_player_id (player_id)
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4 COLLATE=utf8mb4_0900_ai_ci;
