package seckill

import "github.com/redis/go-redis/v9"

// 所有 Lua 脚本在 Redis 中单线程原子执行，"校验时间窗 → 一人一单 → 扣库存 → 写队列"
// 不会被其他请求插入，因此不需要分布式锁，也不会出现超卖。
// 时间统一用 Redis TIME，避免多实例时钟不一致导致有的实例提前开抢。

// attemptScript
//
//	KEYS[1]=info(HASH start,end,status) KEYS[2]=stock KEYS[3]=tickets(HASH uid->"oid|state") KEYS[4]=stream
//	ARGV[1]=uid ARGV[2]=新订单号 ARGV[3]=活动ID ARGV[4]=tickets 过期时间(ms)
//
// 返回 {code, ticket}：0 抢到并入队；1 已有资格（重复请求，返回原订单号）；
// -1 未预热；-2 未开始；-3 已结束；-4 售罄；-5 已下线。
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
//
//	KEYS[1]=info KEYS[2]=stock
//	ARGV[1]=start ARGV[2]=end ARGV[3]=status ARGV[4]=库存 ARGV[5]=是否强制覆盖库存 ARGV[6]=过期时间(ms)
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
// 用于：落单成功(q->ok)、落单失败补偿(q->fail, 归还库存)、订单取消(ok/q->cancel, 归还库存)。
// "先比较再修改"保证补偿只会执行一次，重复消息、重复取消都不会把库存加多。
//
//	KEYS[1]=tickets KEYS[2]=stock
//	ARGV[1]=uid ARGV[2]=oid ARGV[3]=目标状态 ARGV[4]=归还库存数 ARGV[5..]=允许的原状态
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

// inspectScript 返回 Redis 剩余库存与仍在排队落库的资格数，供对账使用。
var inspectScript = redis.NewScript(`
local stock = redis.call('GET', KEYS[1])
if not stock then return {-1, 0} end
local queued = 0
for _, v in ipairs(redis.call('HVALS', KEYS[2])) do
  if string.sub(v, -2) == '|q' then queued = queued + 1 end
end
return {tonumber(stock), queued}
`)

// casStockScript 对账修正：只有库存仍等于观察值时才覆盖，避免覆盖并发扣减。
var casStockScript = redis.NewScript(`
if redis.call('GET', KEYS[1]) == ARGV[1] then
  redis.call('SET', KEYS[1], ARGV[2], 'KEEPTTL')
  return 1
end
return 0
`)
