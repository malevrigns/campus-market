// Package ratelimit 提供基于 Redis 的分布式限流，以及 Redis 故障时的单机降级。
//
//   - 滑动窗口（ZSET 记录每次请求时间戳）：精确，适合登录、验证码、发消息等低频敏感接口；
//   - 令牌桶（HASH 记录令牌数与上次补充时间）：允许突发、平滑限速，适合抢购入口、全局 IP 限流；
//
// 两个脚本都用 Redis TIME 取时间，避免多实例之间的时钟偏差。
// Redis 不可用时降级为进程内固定窗口：宁可放宽限流，也不让限流组件把全站打挂（fail-open + 本地兜底）。
package ratelimit

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"log/slog"
	"sync"
	"sync/atomic"
	"time"

	"github.com/redis/go-redis/v9"
)

var slidingWindowScript = redis.NewScript(`
local key = KEYS[1]
local window = tonumber(ARGV[1])
local limit = tonumber(ARGV[2])
local t = redis.call('TIME')
local now = tonumber(t[1]) * 1000 + math.floor(tonumber(t[2]) / 1000)
redis.call('ZREMRANGEBYSCORE', key, '-inf', now - window)
local count = redis.call('ZCARD', key)
if count < limit then
  redis.call('ZADD', key, now, ARGV[3])
  redis.call('PEXPIRE', key, window)
  return {1, limit - count - 1, 0}
end
local oldest = redis.call('ZRANGE', key, 0, 0, 'WITHSCORES')
local retry = window
if oldest[2] then retry = window - (now - tonumber(oldest[2])) end
return {0, 0, retry}
`)

var tokenBucketScript = redis.NewScript(`
local key = KEYS[1]
local rate = tonumber(ARGV[1])
local burst = tonumber(ARGV[2])
local cost = tonumber(ARGV[3])
local t = redis.call('TIME')
local now = tonumber(t[1]) * 1000 + math.floor(tonumber(t[2]) / 1000)
local data = redis.call('HMGET', key, 'tokens', 'ts')
local tokens = tonumber(data[1])
local ts = tonumber(data[2])
if tokens == nil then
  tokens = burst
  ts = now
end
tokens = math.min(burst, tokens + math.max(0, now - ts) * rate / 1000)
local allowed = 0
local retry = 0
if tokens >= cost then
  tokens = tokens - cost
  allowed = 1
else
  retry = math.ceil((cost - tokens) * 1000 / rate)
end
redis.call('HSET', key, 'tokens', tostring(tokens), 'ts', now)
redis.call('PEXPIRE', key, math.ceil(burst * 1000 / rate) + 1000)
return {allowed, math.floor(tokens), retry}
`)

// Result 是一次限流判定的结果。
type Result struct {
	Allowed    bool
	Remaining  int
	RetryAfter time.Duration
	// Degraded 表示本次判定走的是单机降级逻辑。
	Degraded bool
}

type Limiter struct {
	rdb      redis.Scripter
	prefix   string
	local    *Local
	lastWarn atomic.Int64
}

func New(rdb redis.Scripter, prefix string) *Limiter {
	return &Limiter{rdb: rdb, prefix: prefix, local: NewLocal(20000)}
}

func member() string {
	b := make([]byte, 8)
	_, _ = rand.Read(b)
	return hex.EncodeToString(b)
}

func (l *Limiter) degrade(key string, limit int, window time.Duration, err error) Result {
	if t := time.Now().Unix(); l.lastWarn.Swap(t) != t {
		slog.Warn("rate limiter degraded to local mode", "error", err)
	}
	return Result{Allowed: l.local.Allow(key, limit, window), Degraded: true}
}

// Window 滑动窗口限流：window 时间内最多 limit 次。
func (l *Limiter) Window(ctx context.Context, key string, limit int, window time.Duration) Result {
	k := l.prefix + ":sw:" + key
	vals, err := slidingWindowScript.Run(ctx, l.rdb, []string{k}, window.Milliseconds(), limit, member()).Int64Slice()
	if err != nil || len(vals) != 3 {
		return l.degrade(k, limit, window, err)
	}
	return Result{Allowed: vals[0] == 1, Remaining: int(vals[1]), RetryAfter: time.Duration(vals[2]) * time.Millisecond}
}

// Bucket 令牌桶限流：每秒补充 rate 个令牌，桶容量 burst。
func (l *Limiter) Bucket(ctx context.Context, key string, rate float64, burst int) Result {
	k := l.prefix + ":tb:" + key
	vals, err := tokenBucketScript.Run(ctx, l.rdb, []string{k}, rate, burst, 1).Int64Slice()
	if err != nil || len(vals) != 3 {
		window := time.Duration(float64(burst) / rate * float64(time.Second))
		if window < time.Second {
			window = time.Second
		}
		return l.degrade(k, burst, window, err)
	}
	return Result{Allowed: vals[0] == 1, Remaining: int(vals[1]), RetryAfter: time.Duration(vals[2]) * time.Millisecond}
}

// Local 是进程内固定窗口计数器，用于降级。容量有上限，超过时先清理过期窗口。
type Local struct {
	mu      sync.Mutex
	max     int
	entries map[string]*localWindow
}
type localWindow struct {
	start time.Time
	n     int
}

func NewLocal(max int) *Local { return &Local{max: max, entries: map[string]*localWindow{}} }

func (l *Local) Allow(key string, limit int, window time.Duration) bool {
	l.mu.Lock()
	defer l.mu.Unlock()
	now := time.Now()
	if len(l.entries) >= l.max {
		for k, v := range l.entries {
			if now.Sub(v.start) > window {
				delete(l.entries, k)
			}
		}
		if len(l.entries) >= l.max {
			return false
		}
	}
	w := l.entries[key]
	if w == nil || now.Sub(w.start) > window {
		w = &localWindow{start: now}
		l.entries[key] = w
	}
	w.n++
	return w.n <= limit
}
