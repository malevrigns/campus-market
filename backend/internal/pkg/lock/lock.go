// Package lock 实现基于 Redis 的分布式锁。
//
// 设计要点（也是面试常问点）：
//   - 加锁：SET key token NX PX ttl，token 为随机值，保证"谁加的锁谁来解"；
//   - 解锁/续期：Lua 脚本先比较 token 再 DEL/PEXPIRE，避免 GET+DEL 非原子导致误删别人的锁；
//   - 看门狗：持锁期间每 ttl/3 续期一次，业务执行时间不可预估时不会因 TTL 过期被别人抢走；
//     续期失败（锁已丢失）时取消业务 context，让业务尽快停止；
//   - fencing token：每次加锁同时 INCR 一个单调递增的围栏号，供下游存储做"只接受更大围栏号"的校验，
//     解决 GC 停顿/网络分区导致的"旧持有者以为自己还持有锁"问题。
//
// 单 Redis 节点（或主从）下，主从切换可能丢锁；对正确性要求极高的场景应依赖数据库约束兜底，
// 本项目所有被锁保护的任务（超时关单、对账）本身都是幂等的，锁只用来减少重复劳动。
package lock

import (
	"context"
	crand "crypto/rand"
	"encoding/hex"
	"errors"
	"math/rand/v2"
	"time"

	"github.com/redis/go-redis/v9"
)

var (
	ErrNotAcquired = errors.New("lock: not acquired")
	ErrLost        = errors.New("lock: lost")
)

var acquireScript = redis.NewScript(`
if redis.call('SET', KEYS[1], ARGV[1], 'NX', 'PX', ARGV[2]) then
  return redis.call('INCR', KEYS[2])
end
return 0
`)

var releaseScript = redis.NewScript(`
if redis.call('GET', KEYS[1]) == ARGV[1] then
  return redis.call('DEL', KEYS[1])
end
return 0
`)

var refreshScript = redis.NewScript(`
if redis.call('GET', KEYS[1]) == ARGV[1] then
  return redis.call('PEXPIRE', KEYS[1], ARGV[2])
end
return 0
`)

// Client 创建锁。prefix 例如 "campus:lock"。
type Client struct {
	rdb    redis.Scripter
	prefix string
}

func New(rdb redis.Scripter, prefix string) *Client { return &Client{rdb: rdb, prefix: prefix} }

// Lock 是一次成功的加锁。
type Lock struct {
	c     *Client
	key   string
	token string
	ttl   time.Duration
	// Fence 是单调递增的围栏号。
	Fence int64
}

func token() string {
	b := make([]byte, 16)
	if _, err := crand.Read(b); err != nil {
		panic(err)
	}
	return hex.EncodeToString(b)
}

// TryAcquire 尝试加锁一次，被占用时返回 ErrNotAcquired。
func (c *Client) TryAcquire(ctx context.Context, name string, ttl time.Duration) (*Lock, error) {
	l := &Lock{c: c, key: c.prefix + ":" + name, token: token(), ttl: ttl}
	fence, err := acquireScript.Run(ctx, c.rdb, []string{l.key, l.key + ":fence"}, l.token, ttl.Milliseconds()).Int64()
	if err != nil {
		return nil, err
	}
	if fence == 0 {
		return nil, ErrNotAcquired
	}
	l.Fence = fence
	return l, nil
}

// Acquire 在 ctx 结束前反复尝试加锁（带抖动的退避，避免惊群）。
func (c *Client) Acquire(ctx context.Context, name string, ttl, retry time.Duration) (*Lock, error) {
	for {
		l, err := c.TryAcquire(ctx, name, ttl)
		if err == nil || !errors.Is(err, ErrNotAcquired) {
			return l, err
		}
		jitter := rand.N(retry/2 + 1)
		select {
		case <-ctx.Done():
			return nil, ctx.Err()
		case <-time.After(retry/2 + jitter):
		}
	}
}

// Release 只释放自己持有的锁。锁已过期或被他人持有时返回 ErrLost。
func (l *Lock) Release(ctx context.Context) error {
	n, err := releaseScript.Run(ctx, l.c.rdb, []string{l.key}, l.token).Int64()
	if err != nil {
		return err
	}
	if n == 0 {
		return ErrLost
	}
	return nil
}

// Refresh 续期。
func (l *Lock) Refresh(ctx context.Context) error {
	n, err := refreshScript.Run(ctx, l.c.rdb, []string{l.key}, l.token, l.ttl.Milliseconds()).Int64()
	if err != nil {
		return err
	}
	if n == 0 {
		return ErrLost
	}
	return nil
}

// KeepAlive 启动看门狗，返回的 channel 在锁丢失时关闭；ctx 结束时看门狗退出。
// 网络抖动导致单次续期失败不立即判定丢锁，只要在 TTL 内续上即可。
func (l *Lock) KeepAlive(ctx context.Context) <-chan struct{} {
	lost := make(chan struct{})
	go func() {
		interval := l.ttl / 3
		if interval <= 0 {
			interval = time.Second
		}
		ticker := time.NewTicker(interval)
		defer ticker.Stop()
		lastOK := time.Now()
		for {
			select {
			case <-ctx.Done():
				return
			case <-ticker.C:
				rctx, cancel := context.WithTimeout(ctx, interval)
				err := l.Refresh(rctx)
				cancel()
				if err == nil {
					lastOK = time.Now()
					continue
				}
				if errors.Is(err, ErrLost) || time.Since(lastOK) >= l.ttl {
					close(lost)
					return
				}
			}
		}
	}()
	return lost
}

// Run 在持锁期间执行 fn：自动续期，锁丢失时取消 fn 的 ctx，结束后释放锁。
// 锁被占用时直接返回 ErrNotAcquired（用于"多实例中只需一个实例执行"的定时任务）。
func (c *Client) Run(ctx context.Context, name string, ttl time.Duration, fn func(ctx context.Context) error) error {
	l, err := c.TryAcquire(ctx, name, ttl)
	if err != nil {
		return err
	}
	runCtx, cancel := context.WithCancelCause(ctx)
	defer cancel(nil)
	lost := l.KeepAlive(runCtx)
	go func() {
		select {
		case <-lost:
			cancel(ErrLost)
		case <-runCtx.Done():
		}
	}()
	err = fn(runCtx)
	cancel(nil)
	releaseCtx, done := context.WithTimeout(context.WithoutCancel(ctx), time.Second)
	defer done()
	_ = l.Release(releaseCtx)
	if err == nil && errors.Is(context.Cause(runCtx), ErrLost) {
		return ErrLost
	}
	return err
}
