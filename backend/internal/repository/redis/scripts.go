package redisrepo

import "github.com/redis/go-redis/v9"

// 所有 Lua 脚本在 Redis 中单线程原子执行，“校验时间窗 → 一人一单 → 扣库存 → 写队列”
// 不会被其他请求插入，因此不需要分布式锁，也不会出现超卖。

var attemptScript = redis.NewScript(`
local info = redis.call('HMGET', KEYS[1], 'start', 'end', 'status')
if not info[1] then return {-1, ''} end
if info[3] ~= 'on' then return {-5, ''} end
local t = redis.call('TIME')
local now = tonumber(t[1]) * 1000 + math.floor(tonumber(t[2]) / 1000)
if now < tonumber(info[1]) then return {-2, ''} end
if now >= tonumber(info[2]) then return {-3, ''} end
local prev = redis.call('HGET', KEYS[3], ARGV[1])
if prev then
  local sep = string.find(prev, '|', 1, true)
  if string.sub(prev, sep + 1) ~= 'fail' then return {1, prev} end
end
local stock = tonumber(redis.call('GET', KEYS[2]) or '0')
if stock <= 0 then return {-4, ''} end
redis.call('DECR', KEYS[2])
redis.call('HSET', KEYS[3], ARGV[1], ARGV[2] .. '|q')
redis.call('PEXPIREAT', KEYS[3], ARGV[4])
redis.call('XADD', KEYS[4], '*', 'sale', ARGV[3], 'uid', ARGV[1], 'oid', ARGV[2], 'ts', tostring(now))
return {0, ARGV[2] .. '|q'}
`)

// preheatScript 写入活动元数据；库存默认只在不存在时写入（SET NX），
// 避免活动进行中重启/重复预热把已经扣减的库存覆盖回初始值。
var preheatScript = redis.NewScript(`
redis.call('HSET', KEYS[1], 'start', ARGV[1], 'end', ARGV[2], 'status', ARGV[3])
if ARGV[5] == '1' then
  redis.call('SET', KEYS[2], ARGV[4])
else
  redis.call('SET', KEYS[2], ARGV[4], 'NX')
end
redis.call('PEXPIREAT', KEYS[1], ARGV[6])
redis.call('PEXPIREAT', KEYS[2], ARGV[6])
return tonumber(redis.call('GET', KEYS[2]))
`)

// transitionScript 只在资格仍是指定订单的指定状态时才迁移，并按需归还 Redis 库存。
var transitionScript = redis.NewScript(`
local cur = redis.call('HGET', KEYS[1], ARGV[1])
if not cur then return 0 end
local sep = string.find(cur, '|', 1, true)
if string.sub(cur, 1, sep - 1) ~= ARGV[2] then return 0 end
local state = string.sub(cur, sep + 1)
local allowed = false
for i = 5, #ARGV do
  if ARGV[i] == state then allowed = true end
end
if not allowed then return 0 end
redis.call('HSET', KEYS[1], ARGV[1], ARGV[2] .. '|' .. ARGV[3])
local n = tonumber(ARGV[4])
if n > 0 and redis.call('EXISTS', KEYS[2]) == 1 then
  redis.call('INCRBY', KEYS[2], n)
end
return 1
`)

var inspectScript = redis.NewScript(`
local stock = redis.call('GET', KEYS[1])
if not stock then return {-1, 0} end
local queued = 0
for _, v in ipairs(redis.call('HVALS', KEYS[2])) do
  if string.sub(v, -2) == '|q' then queued = queued + 1 end
end
return {tonumber(stock), queued}
`)

var casStockScript = redis.NewScript(`
if redis.call('GET', KEYS[1]) == ARGV[1] then
  redis.call('SET', KEYS[1], ARGV[2], 'KEEPTTL')
  return 1
end
return 0
`)
