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
local score = tonumber(ARGV[1])
local delta = tonumber(ARGV[2])
local limit = tonumber(ARGV[3])

local min_score = score - delta
local max_score = score + delta

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

// TimeoutMatchTicketScript 原子化地将过期且仍在排队的票据标记为 timeout。
// KEYS[1]: match:ticket:{ticket_id}
// KEYS[2]: match:player_ticket:{player_id}
// KEYS[3]: ticket pool ZSet
// KEYS[4]: ticket expiry ZSet
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

local ticket_id = ARGV[1]
local now_ms = tonumber(ARGV[2])
local queued_status = ARGV[3]
local timeout_status = ARGV[4]

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
redis.call('DEL', player_ticket_key)
if player_id then
    redis.call('ZREM', pool_key, player_id)
end
redis.call('ZREM', expiry_key, ticket_id)

return 1
`)
