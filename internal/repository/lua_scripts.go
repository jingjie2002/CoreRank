package repository

import "github.com/redis/go-redis/v9"

// AtomicMatchScript 用于在 ZSet 中原子化查询、提取并删除玩家
// KEYS[1]: ZSet 的 key
// ARGV[1]: 当前玩家的分数
// ARGV[2]: 分数差范围 (delta)
// ARGV[3]: 最大返回数量
// 返回: 匹配的玩家ID列表 (已从ZSet中删除)
var AtomicMatchScript = redis.NewScript(`
local key = KEYS[1]
local min_score = tonumber(ARGV[1])
local max_mmr_score = tonumber(ARGV[2])
local limit = tonumber(ARGV[3])

-- CompositeScoreScript adds a timestamp fraction in [0,1). Include the full
-- upper integer boundary so an MMR exactly equal to max_score is not skipped.
local max_score = max_mmr_score + 0.999999999

-- 查询分数范围内的玩家
local members = redis.call('ZRANGEBYSCORE', key, min_score, max_score, 'LIMIT', 0, limit)

if #members == 0 then
    return {}
end

if #members < limit then
    return {}
end

-- 原子删除匹配的玩家
for i, member in ipairs(members) do
    redis.call('ZREM', key, member)
end

return members
`)

// CompositeScoreScript 用于将分数和时间戳合并为复合分数
// KEYS[1]: ZSet 的 key
// ARGV[1]: 玩家ID
// ARGV[2]: 玩家分数 (整数部分)
// ARGV[3]: 时间戳 (用于小数部分，确保先入队的玩家优先匹配)
// 复合分数格式: score.timestamp (分数相同时，时间戳小的优先)
var CompositeScoreScript = redis.NewScript(`
local key = KEYS[1]
local player_id = ARGV[1]
local score = tonumber(ARGV[2])
local timestamp = tonumber(ARGV[3])

-- 将时间戳转换为小数部分 (归一化到0-1之间)
-- 使用较大的除数确保时间戳差异体现在小数部分
-- 时间戳越小，复合分数越小，优先级越高
local max_timestamp = 10000000000000  -- 足够大的值来归一化时间戳
local decimal_part = timestamp / max_timestamp

-- 复合分数 = 整数分数 + 时间戳小数部分
local composite_score = score + decimal_part

-- 添加玩家到 ZSet
redis.call('ZADD', key, composite_score, player_id)

return composite_score
`)

// CreateMatchTicketScript atomically creates every Redis structure needed by
// a queued ticket. A return value of 0 means the player already owns a ticket.
var CreateMatchTicketScript = redis.NewScript(`
local player_ticket_key = KEYS[1]
local ticket_key = KEYS[2]
local pool_key = KEYS[3]
local expiry_key = KEYS[4]
local modes_key = KEYS[5]

local ticket_id = ARGV[1]
local player_id = ARGV[2]
local mmr_score = tonumber(ARGV[3])
local match_mode = ARGV[4]
local queued_status = ARGV[5]
local created_at = tonumber(ARGV[6])
local updated_at = tonumber(ARGV[7])
local expires_at = tonumber(ARGV[8])
local ttl_ms = tonumber(ARGV[9])

if redis.call('SISMEMBER', modes_key, match_mode) == 0 and redis.call('SCARD', modes_key) >= 64 then
    return -2
end
if redis.call('SET', player_ticket_key, ticket_id, 'PX', ttl_ms, 'NX') == false then
    return -1
end

redis.call('HSET', ticket_key,
    'ticket_id', ticket_id,
    'player_id', player_id,
    'mmr_score', mmr_score,
    'match_mode', match_mode,
    'status', queued_status,
    'match_id', '',
    'room_id', '',
    'created_at', created_at,
    'updated_at', updated_at,
    'expires_at', expires_at)
redis.call('PEXPIRE', ticket_key, ttl_ms)

local composite_score = mmr_score + (created_at / 10000000000000)
redis.call('ZADD', pool_key, composite_score, player_id)
redis.call('ZADD', expiry_key, expires_at, ticket_id)
redis.call('SADD', modes_key, match_mode)
return 1
`)

// CancelMatchTicketScript performs a queued -> cancelled compare-and-set.
var CancelMatchTicketScript = redis.NewScript(`
local ticket_key = KEYS[1]
local player_ticket_key = KEYS[2]
local pool_key = KEYS[3]
local expiry_key = KEYS[4]
local modes_key = KEYS[5]

local ticket_id = ARGV[1]
local now_ms = tonumber(ARGV[2])
local queued_status = ARGV[3]
local cancelled_status = ARGV[4]
local match_mode = ARGV[5]

if redis.call('EXISTS', ticket_key) == 0 then
    return -1
end
if redis.call('HGET', ticket_key, 'status') ~= queued_status then
    return 0
end

local player_id = redis.call('HGET', ticket_key, 'player_id')
redis.call('HSET', ticket_key, 'status', cancelled_status, 'updated_at', now_ms)
if redis.call('GET', player_ticket_key) == ticket_id then
    redis.call('DEL', player_ticket_key)
end
if player_id then
    redis.call('ZREM', pool_key, player_id)
end
redis.call('ZREM', expiry_key, ticket_id)
if redis.call('ZCARD', pool_key) == 0 then
    redis.call('SREM', modes_key, match_mode)
end
return 1
`)

// RequeueMatchTicketScript only restores a player when the mapping still
// points at the same queued ticket. This closes the cancel/requeue race.
var RequeueMatchTicketScript = redis.NewScript(`
local ticket_key = KEYS[1]
local player_ticket_key = KEYS[2]
local pool_key = KEYS[3]
local modes_key = KEYS[4]

local ticket_id = ARGV[1]
local player_id = ARGV[2]
local match_mode = ARGV[3]
local queued_status = ARGV[4]
local mmr_score = tonumber(ARGV[5])
local created_at = tonumber(ARGV[6])

if redis.call('EXISTS', ticket_key) == 0 or
   redis.call('HGET', ticket_key, 'status') ~= queued_status or
   redis.call('GET', player_ticket_key) ~= ticket_id then
    redis.call('ZREM', pool_key, player_id)
    return 0
end

local composite_score = mmr_score + (created_at / 10000000000000)
redis.call('ZADD', pool_key, composite_score, player_id)
redis.call('SADD', modes_key, match_mode)
return 1
`)

// CompleteMatchScript validates every player's queued ticket and match mode,
// then commits all ticket terminal states and the MatchResult in one script.
// Return code: 1=committed, 0=state changed/missing, -1=mode mismatch.
var CompleteMatchScript = redis.NewScript(`
local result_key = KEYS[1]
local pool_key = KEYS[2]
local expiry_key = KEYS[3]
local modes_key = KEYS[4]

local expected_mode = ARGV[1]
local queued_status = ARGV[2]
local matched_status = ARGV[3]
local match_id = ARGV[4]
local room_id = ARGV[5]
local server_id = ARGV[6]
local server_addr = ARGV[7]
local result_status = ARGV[8]
local created_at = tonumber(ARGV[9])
local result_ttl_ms = tonumber(ARGV[10])
local players_json = ARGV[11]
local join_token = ARGV[12]
local player_count = tonumber(ARGV[13])

local ticket_ids = {}
for i = 1, player_count do
    local player_id = ARGV[13 + i]
    local player_ticket_key = 'match:player_ticket:' .. player_id
    local ticket_id = redis.call('GET', player_ticket_key)
    if not ticket_id then
        return {0, 'missing_player_ticket', player_id}
    end
    local ticket_key = 'match:ticket:' .. ticket_id
    if redis.call('EXISTS', ticket_key) == 0 then
        return {0, 'missing_ticket', player_id}
    end
    if redis.call('HGET', ticket_key, 'status') ~= queued_status then
        return {0, 'ticket_not_queued', player_id}
    end
    if redis.call('HGET', ticket_key, 'match_mode') ~= expected_mode then
        return {-1, 'match_mode_mismatch', player_id}
    end
    ticket_ids[i] = ticket_id
end

for i = 1, player_count do
    local player_id = ARGV[13 + i]
    local ticket_id = ticket_ids[i]
    local player_ticket_key = 'match:player_ticket:' .. player_id
    local ticket_key = 'match:ticket:' .. ticket_id
    redis.call('HSET', ticket_key,
        'status', matched_status,
        'match_id', match_id,
        'room_id', room_id,
        'updated_at', created_at)
    if redis.call('GET', player_ticket_key) == ticket_id then
        redis.call('DEL', player_ticket_key)
    end
    redis.call('ZREM', expiry_key, ticket_id)
end

redis.call('HSET', result_key,
    'match_id', match_id,
    'room_id', room_id,
    'server_id', server_id,
    'server_addr', server_addr,
    'match_mode', expected_mode,
    'player_ids', players_json,
    'join_token', join_token,
    'status', result_status,
    'created_at', created_at)
redis.call('PEXPIRE', result_key, result_ttl_ms)
if redis.call('ZCARD', pool_key) == 0 then
    redis.call('SREM', modes_key, expected_mode)
end

local response = {1}
for i = 1, player_count do
    response[#response + 1] = ticket_ids[i]
end
return response
`)

// TimeoutMatchTicketScript 原子化地将过期且仍在排队的票据标记为 timeout。
// KEYS[1]: match:ticket:{ticket_id}
// KEYS[2]: match:player_ticket:{player_id}
// KEYS[3]: ticket pool ZSet
// KEYS[4]: ticket expiry ZSet
// KEYS[5]: queued match modes Set
// ARGV[1]: ticket_id
// ARGV[2]: now_ms
// ARGV[3]: queued status
// ARGV[4]: timeout status
// 返回: 1 表示成功超时，0 表示票据不存在、已非 queued 或尚未过期
var TimeoutMatchTicketScript = redis.NewScript(`
local ticket_key = KEYS[1]
local player_ticket_key = KEYS[2]
local pool_key = KEYS[3]
local expiry_key = KEYS[4]
local modes_key = KEYS[5]

local ticket_id = ARGV[1]
local now_ms = tonumber(ARGV[2])
local queued_status = ARGV[3]
local timeout_status = ARGV[4]
local match_mode = ARGV[5]

if redis.call('EXISTS', ticket_key) == 0 then
    redis.call('ZREM', expiry_key, ticket_id)
    return 0
end

local status = redis.call('HGET', ticket_key, 'status')
if status ~= queued_status then
    redis.call('ZREM', expiry_key, ticket_id)
    return 0
end

local expires_at = tonumber(redis.call('HGET', ticket_key, 'expires_at') or '0')
if expires_at > now_ms then
    return 0
end

local player_id = redis.call('HGET', ticket_key, 'player_id')

redis.call('HSET', ticket_key, 'status', timeout_status, 'updated_at', now_ms)
if redis.call('GET', player_ticket_key) == ticket_id then
    redis.call('DEL', player_ticket_key)
end
if player_id then
    redis.call('ZREM', pool_key, player_id)
end
redis.call('ZREM', expiry_key, ticket_id)
if redis.call('ZCARD', pool_key) == 0 then
    redis.call('SREM', modes_key, match_mode)
end

return 1
`)
