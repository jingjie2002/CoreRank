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
