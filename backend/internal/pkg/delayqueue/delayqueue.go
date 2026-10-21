// Package delayqueue 是基于 Redis ZSET 的可靠延迟队列（至少一次投递）。
//
//	ready    ZSET  member=任务ID  score=到期时间(ms)
//	inflight ZSET  member=任务ID  score=可见性超时时间(ms)
//	attempts HASH  member -> 已失败次数
//	dead     ZSET  超过最大重试次数的任务，人工排查
//
// 取任务用 Lua 原子地 "ZRANGEBYSCORE ready + ZREM ready + ZADD inflight"，多个实例并发消费也不会重复领取；
// 处理成功后 ZREM inflight（ACK）。消费者崩溃时任务停留在 inflight，可见性超时后由 requeue 脚本放回 ready，
// 因此处理函数必须幂等。
//
// 与 "每 30 秒全表扫描超时订单" 相比：时效从 30s 降到 ~1s，数据库只按主键处理到期订单；
// 扫描任务仍保留为低频兜底（Redis 数据丢失、入队失败时最终也能关单）。
package delayqueue

import (
	"context"
	"errors"
	"log/slog"
	"strconv"
	"sync"
	"time"

	"github.com/redis/go-redis/v9"
)

var popScript = redis.NewScript(`
local t = redis.call('TIME')
local now = tonumber(t[1]) * 1000 + math.floor(tonumber(t[2]) / 1000)
local items = redis.call('ZRANGEBYSCORE', KEYS[1], '-inf', now, 'LIMIT', 0, tonumber(ARGV[1]))
for _, m in ipairs(items) do
  redis.call('ZREM', KEYS[1], m)
  redis.call('ZADD', KEYS[2], now + tonumber(ARGV[2]), m)
end
return items
`)

var requeueScript = redis.NewScript(`
local t = redis.call('TIME')
local now = tonumber(t[1]) * 1000 + math.floor(tonumber(t[2]) / 1000)
local items = redis.call('ZRANGEBYSCORE', KEYS[2], '-inf', now, 'LIMIT', 0, 200)
for _, m in ipairs(items) do
  redis.call('ZREM', KEYS[2], m)
  redis.call('ZADD', KEYS[1], 'NX', now, m)
end
return #items
`)

// failScript 记录失败：未超过上限则按退避时间延后重试，否则移入死信。
var failScript = redis.NewScript(`
local n = redis.call('HINCRBY', KEYS[3], ARGV[1], 1)
redis.call('ZREM', KEYS[2], ARGV[1])
if n >= tonumber(ARGV[2]) then
  redis.call('HDEL', KEYS[3], ARGV[1])
  redis.call('ZADD', KEYS[4], ARGV[3], ARGV[1])
  return -1
end
redis.call('ZADD', KEYS[1], 'NX', tonumber(ARGV[3]) + tonumber(ARGV[4]) * (2 ^ (n - 1)), ARGV[1])
return n
`)

type Options struct {
	// Visibility 是任务被领取后的处理时限，超时未 ACK 会重新投递。
	Visibility time.Duration
	// MaxAttempts 失败重试上限，超过后进入死信集合。
	MaxAttempts int
	// Backoff 首次失败后的重试间隔，之后指数增长。
	Backoff time.Duration
	Batch   int
	Workers int
	// Poll 是队列空闲时的轮询间隔。
	Poll time.Duration
}

type Queue struct {
	rdb                             redis.Cmdable
	ready, inflight, attempts, dead string
	opt                             Options
}

func New(rdb redis.Cmdable, prefix, name string, opt Options) *Queue {
	if opt.Visibility <= 0 {
		opt.Visibility = 30 * time.Second
	}
	if opt.MaxAttempts <= 0 {
		opt.MaxAttempts = 8
	}
	if opt.Backoff <= 0 {
		opt.Backoff = 2 * time.Second
	}
	if opt.Batch <= 0 {
		opt.Batch = 64
	}
	if opt.Workers <= 0 {
		opt.Workers = 4
	}
	if opt.Poll <= 0 {
		opt.Poll = 500 * time.Millisecond
	}
	base := prefix + ":dq:" + name
	return &Queue{rdb: rdb, ready: base + ":ready", inflight: base + ":inflight", attempts: base + ":attempts", dead: base + ":dead", opt: opt}
}

// Add 投递任务，at 时刻到期。重复投递同一 member 只会更新到期时间。
func (q *Queue) Add(ctx context.Context, member string, at time.Time) error {
	return q.rdb.ZAdd(ctx, q.ready, redis.Z{Score: float64(at.UnixMilli()), Member: member}).Err()
}

// Remove 取消任务（例如订单已支付，无需再关单）。
func (q *Queue) Remove(ctx context.Context, member string) error {
	pipe := q.rdb.TxPipeline()
	pipe.ZRem(ctx, q.ready, member)
	pipe.ZRem(ctx, q.inflight, member)
	pipe.HDel(ctx, q.attempts, member)
	_, err := pipe.Exec(ctx)
	return err
}

// Stats 返回各状态任务数，用于健康检查和监控。
func (q *Queue) Stats(ctx context.Context) (ready, inflight, dead int64, err error) {
	pipe := q.rdb.Pipeline()
	r := pipe.ZCard(ctx, q.ready)
	i := pipe.ZCard(ctx, q.inflight)
	d := pipe.ZCard(ctx, q.dead)
	if _, err = pipe.Exec(ctx); err != nil {
		return
	}
	return r.Val(), i.Val(), d.Val(), nil
}

// Handler 处理到期任务。返回 nil 即 ACK；返回错误则按退避重试。
type Handler func(ctx context.Context, member string) error

// Run 启动消费循环，直到 ctx 结束。所有实例都可以运行 Run，任务不会被重复领取。
func (q *Queue) Run(ctx context.Context, handle Handler) error {
	jobs := make(chan string)
	var wg sync.WaitGroup
	for i := 0; i < q.opt.Workers; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for m := range jobs {
				q.process(ctx, m, handle)
			}
		}()
	}
	defer func() {
		close(jobs)
		wg.Wait()
	}()
	lastRequeue := time.Time{}
	for {
		if ctx.Err() != nil {
			return nil
		}
		if time.Since(lastRequeue) > time.Second {
			if _, err := requeueScript.Run(ctx, q.rdb, []string{q.ready, q.inflight}).Result(); err != nil && ctx.Err() == nil {
				slog.Warn("delay queue requeue failed", "queue", q.ready, "error", err)
			}
			lastRequeue = time.Now()
		}
		items, err := popScript.Run(ctx, q.rdb, []string{q.ready, q.inflight}, q.opt.Batch, q.opt.Visibility.Milliseconds()).StringSlice()
		if err != nil && !errors.Is(err, redis.Nil) {
			if ctx.Err() != nil {
				return nil
			}
			slog.Warn("delay queue poll failed", "queue", q.ready, "error", err)
			sleep(ctx, 2*time.Second)
			continue
		}
		for _, m := range items {
			select {
			case jobs <- m:
			case <-ctx.Done():
				return nil
			}
		}
		if len(items) < q.opt.Batch {
			sleep(ctx, q.opt.Poll)
		}
	}
}

func (q *Queue) process(ctx context.Context, m string, handle Handler) {
	hctx, cancel := context.WithTimeout(ctx, q.opt.Visibility)
	err := safeHandle(hctx, m, handle)
	cancel()
	// ACK/失败记录用独立 context：进程退出时也尽量把结果写回，减少重复投递。
	actx, done := context.WithTimeout(context.WithoutCancel(ctx), 2*time.Second)
	defer done()
	if err == nil {
		pipe := q.rdb.TxPipeline()
		pipe.ZRem(actx, q.inflight, m)
		pipe.HDel(actx, q.attempts, m)
		if _, e := pipe.Exec(actx); e != nil {
			slog.Warn("delay queue ack failed", "member", m, "error", e)
		}
		return
	}
	n, e := failScript.Run(actx, q.rdb, []string{q.ready, q.inflight, q.attempts, q.dead}, m, q.opt.MaxAttempts, time.Now().UnixMilli(), q.opt.Backoff.Milliseconds()).Int64()
	if e != nil {
		slog.Warn("delay queue failure bookkeeping failed", "member", m, "error", e)
		return
	}
	if n < 0 {
		slog.Error("delay queue task moved to dead letter", "queue", q.ready, "member", m, "error", err)
	} else {
		slog.Warn("delay queue task failed, will retry", "queue", q.ready, "member", m, "attempt", strconv.FormatInt(n, 10), "error", err)
	}
}

func safeHandle(ctx context.Context, m string, handle Handler) (err error) {
	defer func() {
		if p := recover(); p != nil {
			err = errors.New("handler panic")
			slog.Error("delay queue handler panic", "member", m, "panic", p)
		}
	}()
	return handle(ctx, m)
}

func sleep(ctx context.Context, d time.Duration) {
	t := time.NewTimer(d)
	defer t.Stop()
	select {
	case <-ctx.Done():
	case <-t.C:
	}
}
