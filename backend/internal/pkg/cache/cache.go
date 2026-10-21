// Package cache 实现"本地 LRU(L1) + Redis(L2) + 数据库"的多级 Cache-Aside 缓存。
//
// 典型问题与对应手段：
//   - 缓存击穿（热点 key 过期瞬间大量请求打到 DB）：进程内 singleflight 合并同 key 回源，
//     N 个实例最多 N 次回源；
//   - 缓存穿透（查询不存在的数据）：回源结果为"不存在"时写入空值标记，短 TTL；
//   - 缓存雪崩（大量 key 同时过期）：写入 L2 时在基础 TTL 上加随机抖动；
//   - 数据一致性：写路径"先更新数据库，再删除缓存"，并延迟再删一次（延迟双删）
//     覆盖"读请求在删除前读到旧值、删除后才回填"的并发窗口；
//   - 多实例 L1 一致性：删除时通过 Redis Pub/Sub 广播，其他实例清理本地副本；
//     广播可能因断线丢失，所以 L1 TTL 很短（秒级）作为最终兜底。
//
// Redis 故障时 Fetch 自动降级为直接回源，不影响可用性。
package cache

import (
	"context"
	crand "crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"log/slog"
	"math/rand/v2"
	"sync/atomic"
	"time"

	"github.com/redis/go-redis/v9"
	"golang.org/x/sync/singleflight"
)

// ErrNotFound 由 loader 返回，表示数据源中不存在该数据（会被缓存为空值）。
var ErrNotFound = errors.New("cache: not found")

var nullMarker = []byte{0}

func isNull(b []byte) bool { return len(b) == 1 && b[0] == 0 }

type Options struct {
	Prefix            string
	LocalCapacity     int
	LocalTTL          time.Duration
	NullTTL           time.Duration
	Jitter            float64
	DoubleDeleteDelay time.Duration
	LoadTimeout       time.Duration
	// Channel 是失效广播的 Pub/Sub 频道。
	Channel string
}

type Stats struct {
	LocalHits, RemoteHits, Misses, Loads, LoadErrors, Degraded uint64
}

type Cache struct {
	rdb      redis.UniversalClient
	opt      Options
	l1       *LRU
	sf       singleflight.Group
	instance string

	localHits, remoteHits, misses, loads, loadErrors, degraded atomic.Uint64
	closed                                                     atomic.Bool
}

// Close 停止延迟双删等后台动作（进程退出时调用）。
func (c *Cache) Close() { c.closed.Store(true) }

func New(rdb redis.UniversalClient, opt Options) *Cache {
	if opt.LocalCapacity <= 0 {
		opt.LocalCapacity = 10000
	}
	if opt.LocalTTL <= 0 {
		opt.LocalTTL = 3 * time.Second
	}
	if opt.NullTTL <= 0 {
		opt.NullTTL = 30 * time.Second
	}
	if opt.Jitter <= 0 {
		opt.Jitter = 0.2
	}
	if opt.DoubleDeleteDelay <= 0 {
		opt.DoubleDeleteDelay = 500 * time.Millisecond
	}
	if opt.LoadTimeout <= 0 {
		opt.LoadTimeout = 3 * time.Second
	}
	if opt.Channel == "" {
		opt.Channel = opt.Prefix + ":invalidate"
	}
	b := make([]byte, 8)
	_, _ = crand.Read(b)
	return &Cache{rdb: rdb, opt: opt, l1: NewLRU(opt.LocalCapacity, 32), instance: hex.EncodeToString(b)}
}

func (c *Cache) key(k string) string { return c.opt.Prefix + ":" + k }

func (c *Cache) jittered(ttl time.Duration) time.Duration {
	return ttl + time.Duration(rand.Float64()*c.opt.Jitter*float64(ttl))
}

// Fetch 读取缓存，未命中时调用 load 回源并回填。load 返回 ErrNotFound 时缓存空值。
func Fetch[T any](ctx context.Context, c *Cache, key string, ttl time.Duration, load func(context.Context) (T, error)) (T, error) {
	var zero T
	raw, err := c.get(ctx, key, ttl, func(ctx context.Context) ([]byte, error) {
		v, err := load(ctx)
		if err != nil {
			return nil, err
		}
		return json.Marshal(v)
	})
	if err != nil {
		return zero, err
	}
	var v T
	if err = json.Unmarshal(raw, &v); err != nil {
		return zero, err
	}
	return v, nil
}

func (c *Cache) get(ctx context.Context, key string, ttl time.Duration, load func(context.Context) ([]byte, error)) ([]byte, error) {
	full := c.key(key)
	if b, ok := c.l1.Get(full); ok {
		c.localHits.Add(1)
		if isNull(b) {
			return nil, ErrNotFound
		}
		return b, nil
	}
	b, err := c.rdb.Get(ctx, full).Bytes()
	if err == nil {
		c.remoteHits.Add(1)
		c.l1.Set(full, b, c.opt.LocalTTL)
		if isNull(b) {
			return nil, ErrNotFound
		}
		return b, nil
	}
	remoteOK := errors.Is(err, redis.Nil)
	if !remoteOK {
		c.degraded.Add(1)
	}
	c.misses.Add(1)
	// singleflight：同一进程内同一个 key 只有一个 goroutine 回源，其余等待结果。
	// 回源使用独立 context（脱离单个请求的取消），避免"第一个请求被取消导致所有等待者一起失败"。
	ch := c.sf.DoChan(full, func() (any, error) {
		lctx, cancel := context.WithTimeout(context.WithoutCancel(ctx), c.opt.LoadTimeout)
		defer cancel()
		c.loads.Add(1)
		v, err := load(lctx)
		if errors.Is(err, ErrNotFound) {
			if remoteOK {
				if e := c.rdb.Set(lctx, full, nullMarker, c.opt.NullTTL).Err(); e != nil {
					slog.Debug("cache null write failed", "key", full, "error", e)
				}
			}
			c.l1.Set(full, nullMarker, min(c.opt.LocalTTL, c.opt.NullTTL))
			return nil, ErrNotFound
		}
		if err != nil {
			c.loadErrors.Add(1)
			return nil, err
		}
		if remoteOK {
			if e := c.rdb.Set(lctx, full, v, c.jittered(ttl)).Err(); e != nil {
				slog.Debug("cache write failed", "key", full, "error", e)
			}
		}
		c.l1.Set(full, v, c.opt.LocalTTL)
		return v, nil
	})
	select {
	case <-ctx.Done():
		return nil, ctx.Err()
	case r := <-ch:
		if r.Err != nil {
			return nil, r.Err
		}
		return r.Val.([]byte), nil
	}
}

type invalidation struct {
	Origin string   `json:"o"`
	Keys   []string `json:"k"`
}

// Invalidate 在数据库提交之后调用：删除 L2、L1，广播其他实例，并在延迟后再删一次。
func (c *Cache) Invalidate(ctx context.Context, keys ...string) {
	if len(keys) == 0 {
		return
	}
	full := make([]string, len(keys))
	for i, k := range keys {
		full[i] = c.key(k)
	}
	c.evict(ctx, full)
	time.AfterFunc(c.opt.DoubleDeleteDelay, func() {
		if c.closed.Load() {
			return
		}
		dctx, cancel := context.WithTimeout(context.Background(), time.Second)
		defer cancel()
		c.evict(dctx, full)
	})
}

func (c *Cache) evict(ctx context.Context, full []string) {
	for _, k := range full {
		c.l1.Delete(k)
	}
	if c.closed.Load() {
		return
	}
	// 删除失败只能告警：缓存仍会在 TTL 后过期，这也是 TTL 必须设置的原因。
	if err := c.rdb.Del(ctx, full...).Err(); err != nil && !errors.Is(err, redis.ErrClosed) {
		slog.Warn("cache delete failed", "keys", full, "error", err)
	}
	msg, _ := json.Marshal(invalidation{Origin: c.instance, Keys: full})
	if err := c.rdb.Publish(ctx, c.opt.Channel, msg).Err(); err != nil && !errors.Is(err, redis.ErrClosed) {
		slog.Warn("cache invalidation broadcast failed", "error", err)
	}
}

// Listen 订阅失效广播，清理本实例 L1，直到 ctx 结束。
func (c *Cache) Listen(ctx context.Context) error {
	sub := c.rdb.Subscribe(ctx, c.opt.Channel)
	defer sub.Close()
	ch := sub.Channel()
	for {
		select {
		case <-ctx.Done():
			return nil
		case m, ok := <-ch:
			if !ok {
				return nil
			}
			var inv invalidation
			if json.Unmarshal([]byte(m.Payload), &inv) != nil || inv.Origin == c.instance {
				continue
			}
			for _, k := range inv.Keys {
				c.l1.Delete(k)
			}
		}
	}
}

func (c *Cache) Stats() Stats {
	return Stats{LocalHits: c.localHits.Load(), RemoteHits: c.remoteHits.Load(), Misses: c.misses.Load(), Loads: c.loads.Load(), LoadErrors: c.loadErrors.Load(), Degraded: c.degraded.Load()}
}
