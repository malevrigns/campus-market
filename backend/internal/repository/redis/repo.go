// Package redisrepo 是 Redis 数据适配器：缓存、会话、在线状态、热度榜、
// 实时推送与抢购库存/消息流。业务规则由服务层持有。
package redisrepo

import (
	"context"
	"encoding/json"
	"log/slog"
	"sync"
	"time"

	"github.com/redis/go-redis/v9"

	"qingfly/edu-mall/internal/domain"
	"qingfly/edu-mall/internal/infra/redisx"
	"qingfly/edu-mall/internal/pkg/cache"
	"qingfly/edu-mall/internal/port"
)

// Repos 组合所有 Redis 适配器，共享客户端、key 前缀与实例标识。
type Repos struct {
	Session  *SessionCache
	Live     *LiveHub
	Cache    *CacheStore
	Heat     *HeatStore
	Presence *PresenceStore
	Flash    *FlashStore
	keys     redisx.Keys
	rdb      redis.UniversalClient
}

func New(rdb redis.UniversalClient, prefix, instance string, paymentWindow time.Duration) *Repos {
	keys := redisx.Keys(prefix)
	c := cache.New(rdb, cache.Options{Prefix: prefix + ":cache", LocalTTL: 3 * time.Second, NullTTL: 30 * time.Second})
	return &Repos{
		Session:  &SessionCache{rdb: rdb, keys: keys},
		Live:     NewLiveHub(rdb, prefix+":live", instance),
		Cache:    &CacheStore{rdb: rdb, cache: c},
		Heat:     &HeatStore{rdb: rdb, keys: keys},
		Presence: &PresenceStore{rdb: rdb, keys: keys},
		Flash:    NewFlashStore(rdb, prefix, instance, paymentWindow),
		keys:     keys,
		rdb:      rdb,
	}
}

// Close 关闭缓存本地层。
func (r *Repos) Close() { r.Cache.cache.Close() }

// CacheStore 实现商品/信息流/热度榜/抢购元数据缓存。
type CacheStore struct {
	rdb   redis.UniversalClient
	cache *cache.Cache
}

const (
	feedCacheKey = "products:feed:v1"
	hotCacheKey  = "products:hot:v1"
)

func productKey(id string) string { return "product:" + id }
func flashKey(id string) string   { return "flash:" + id }

func (c *CacheStore) Product(ctx context.Context, id string, ttl time.Duration, load func(context.Context) (domain.Product, error)) (domain.Product, error) {
	p, err := cache.Fetch(ctx, c.cache, productKey(id), ttl, func(ctx context.Context) (domain.Product, error) {
		p, err := load(ctx)
		if err != nil {
			if domain.KindOf(err) == domain.KindNotFound {
				return p, cache.ErrNotFound
			}
			return p, err
		}
		return p, nil
	})
	if err == cache.ErrNotFound {
		return p, domain.NotFound("内容不存在或无权访问")
	}
	return p, err
}

func (c *CacheStore) Feed(ctx context.Context, key string, ttl time.Duration, load func(context.Context) (domain.ProductPage, error)) (domain.ProductPage, error) {
	return cache.Fetch(ctx, c.cache, feedCacheKey, ttl, load)
}

func (c *CacheStore) Hot(ctx context.Context, load func(context.Context) ([]domain.HotProduct, error)) ([]domain.HotProduct, error) {
	return cache.Fetch(ctx, c.cache, hotCacheKey, time.Minute, load)
}

func (c *CacheStore) InvalidateProducts(ctx context.Context, ids ...string) {
	keys := []string{feedCacheKey}
	for _, id := range ids {
		if id != "" {
			keys = append(keys, productKey(id))
		}
	}
	c.cache.Invalidate(context.WithoutCancel(ctx), keys...)
}

func (c *CacheStore) Stats() any { return c.cache.Stats() }

// Listen 订阅缓存失效广播，多实例共享 Redis 时保持本地缓存一致。
func (c *CacheStore) Listen(ctx context.Context) error { return c.cache.Listen(ctx) }

// Sale 缓存抢购活动元数据。
func (c *CacheStore) Sale(ctx context.Context, id string, ttl time.Duration, load func(context.Context) (domain.FlashSale, error)) (domain.FlashSale, error) {
	sale, err := cache.Fetch(ctx, c.cache, flashKey(id), ttl, func(ctx context.Context) (domain.FlashSale, error) {
		sale, err := load(ctx)
		if err != nil {
			if domain.KindOf(err) == domain.KindNotFound {
				return sale, cache.ErrNotFound
			}
			return sale, err
		}
		return sale, nil
	})
	if err == cache.ErrNotFound {
		return sale, domain.NotFound("活动不存在")
	}
	return sale, err
}

func (c *CacheStore) List(ctx context.Context, ttl time.Duration, load func(context.Context) ([]domain.FlashSale, error)) ([]domain.FlashSale, error) {
	return cache.Fetch(ctx, c.cache, "flash:list", ttl, load)
}

func (c *CacheStore) Invalidate(ctx context.Context, id string) {
	keys := []string{"flash:list"}
	if id != "" {
		keys = append(keys, flashKey(id))
	}
	c.cache.Invalidate(context.WithoutCancel(ctx), keys...)
}

// ---------- 会话缓存 ----------

const (
	sessionCacheTTL = 30 * time.Second
	sessionTomb     = "revoked"
)

type SessionCache struct {
	rdb  redis.UniversalClient
	keys redisx.Keys
}

type cachedSession struct {
	User    domain.User `json:"u"`
	Expires int64       `json:"e"`
}

func (s *SessionCache) Get(ctx context.Context, hash string) (domain.User, int64, port.SessionCacheState) {
	raw, err := s.rdb.Get(ctx, s.keys.Of("sess", hash)).Bytes()
	if err != nil {
		if err == redis.Nil {
			return domain.User{}, 0, port.SessionCacheMiss
		}
		return domain.User{}, 0, port.SessionCacheMiss
	}
	if string(raw) == sessionTomb {
		return domain.User{}, 0, port.SessionCacheRevoked
	}
	var cs cachedSession
	if json.Unmarshal(raw, &cs) != nil || cs.Expires <= domain.NowMillis() {
		return domain.User{}, 0, port.SessionCacheMiss
	}
	return cs.User, cs.Expires, port.SessionCacheHit
}

func (s *SessionCache) Store(ctx context.Context, hash string, user domain.User, expiresAt int64) {
	ttl := min(sessionCacheTTL, time.Duration(expiresAt-domain.NowMillis())*time.Millisecond)
	if ttl <= 0 {
		return
	}
	user.IsAdmin = false // 管理员状态每次鉴权时由服务层重新判定
	b, _ := json.Marshal(cachedSession{User: user, Expires: expiresAt})
	pipe := s.rdb.Pipeline()
	pipe.SetNX(ctx, s.keys.Of("sess", hash), b, ttl)
	pipe.SAdd(ctx, s.keys.Of("sess-user", user.ID), hash)
	pipe.Expire(ctx, s.keys.Of("sess-user", user.ID), 8*24*time.Hour)
	if _, err := pipe.Exec(ctx); err != nil {
		slog.Debug("session cache write failed", "error", err)
	}
}

// Revoke 写墓碑而不是 DEL，配合 SET NX 回填防止已吊销会话被并发读请求复活。
func (s *SessionCache) Revoke(ctx context.Context, hashes ...string) {
	if len(hashes) == 0 {
		return
	}
	ctx = context.WithoutCancel(ctx)
	pipe := s.rdb.Pipeline()
	for _, h := range hashes {
		pipe.Set(ctx, s.keys.Of("sess", h), sessionTomb, 2*sessionCacheTTL)
	}
	if _, err := pipe.Exec(ctx); err != nil {
		slog.Warn("session revoke cache failed", "error", err)
	}
}

func (s *SessionCache) RefreshUser(ctx context.Context, userID string) {
	ctx = context.WithoutCancel(ctx)
	run := func() {
		cctx, cancel := context.WithTimeout(ctx, time.Second)
		defer cancel()
		hashes, err := s.rdb.SMembers(cctx, s.keys.Of("sess-user", userID)).Result()
		if err != nil || len(hashes) == 0 {
			return
		}
		keys := make([]string, len(hashes))
		for i, h := range hashes {
			keys[i] = s.keys.Of("sess", h)
		}
		vals, err := s.rdb.MGet(cctx, keys...).Result()
		if err != nil {
			return
		}
		live := []string{}
		for i, v := range vals {
			if str, ok := v.(string); ok && str != sessionTomb {
				live = append(live, keys[i])
			}
		}
		if len(live) > 0 {
			s.rdb.Del(cctx, live...)
		}
	}
	run()
	time.AfterFunc(500*time.Millisecond, run)
}

// ---------- 在线状态 ----------

type PresenceStore struct {
	rdb  redis.UniversalClient
	keys redisx.Keys
}

func (p *PresenceStore) Touch(ctx context.Context, userID string) {
	ctx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 300*time.Millisecond)
	defer cancel()
	_ = p.rdb.Set(ctx, p.keys.Of("online", userID), 1, 60*time.Second).Err()
}

func (p *PresenceStore) Online(ctx context.Context, userIDs []string) map[string]bool {
	out := map[string]bool{}
	if len(userIDs) == 0 {
		return out
	}
	keys := make([]string, len(userIDs))
	for i, id := range userIDs {
		keys[i] = p.keys.Of("online", id)
	}
	vals, err := p.rdb.MGet(ctx, keys...).Result()
	if err != nil {
		return out
	}
	for i, v := range vals {
		out[userIDs[i]] = v != nil
	}
	return out
}

// ---------- 实时推送 ----------

type LiveHub struct {
	mu       sync.Mutex
	clients  map[string]map[chan []byte]bool
	rdb      redis.UniversalClient
	channel  string
	instance string
}

type liveEnvelope struct {
	Origin string          `json:"o"`
	Users  []string        `json:"u"`
	Event  json.RawMessage `json:"e"`
}

func NewLiveHub(rdb redis.UniversalClient, channel, instance string) *LiveHub {
	return &LiveHub{clients: map[string]map[chan []byte]bool{}, rdb: rdb, channel: channel, instance: instance}
}

func (h *LiveHub) Subscribe(userID string) (chan []byte, bool) {
	h.mu.Lock()
	defer h.mu.Unlock()
	if len(h.clients[userID]) >= 5 {
		return nil, false
	}
	if h.clients[userID] == nil {
		h.clients[userID] = map[chan []byte]bool{}
	}
	c := make(chan []byte, 32)
	h.clients[userID][c] = true
	return c, true
}

func (h *LiveHub) Unsubscribe(userID string, c chan []byte) {
	h.mu.Lock()
	defer h.mu.Unlock()
	if h.clients[userID][c] {
		delete(h.clients[userID], c)
		close(c)
	}
	if len(h.clients[userID]) == 0 {
		delete(h.clients, userID)
	}
}

func (h *LiveHub) Notify(users []string, event any) {
	b, err := json.Marshal(event)
	if err != nil {
		return
	}
	h.deliver(users, b)
	if h.rdb == nil {
		return
	}
	env, _ := json.Marshal(liveEnvelope{Origin: h.instance, Users: users, Event: b})
	ctx, cancel := context.WithTimeout(context.Background(), 500*time.Millisecond)
	defer cancel()
	if err := h.rdb.Publish(ctx, h.channel, env).Err(); err != nil {
		slog.Warn("live event publish failed", "error", err)
	}
}

func (h *LiveHub) deliver(users []string, b []byte) {
	h.mu.Lock()
	defer h.mu.Unlock()
	for _, u := range users {
		for c := range h.clients[u] {
			select {
			case c <- b:
			default:
				delete(h.clients[u], c)
				close(c)
			}
		}
	}
}

func (h *LiveHub) Listen(ctx context.Context) error {
	sub := h.rdb.Subscribe(ctx, h.channel)
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
			var env liveEnvelope
			if json.Unmarshal([]byte(m.Payload), &env) != nil || env.Origin == h.instance {
				continue
			}
			h.deliver(env.Users, env.Event)
		}
	}
}

// ---------- 热度榜 ----------

type HeatStore struct {
	rdb  redis.UniversalClient
	keys redisx.Keys
}

var cst = time.FixedZone("CST", 8*3600)

const heatRetention = 4 * 24 * time.Hour

var viewScript = redis.NewScript(`
if redis.call('PFADD', KEYS[1], ARGV[1]) == 1 then
  redis.call('ZINCRBY', KEYS[2], 1, ARGV[2])
  redis.call('EXPIRE', KEYS[2], ARGV[3])
end
redis.call('EXPIRE', KEYS[1], ARGV[3])
return 1
`)

func (h *HeatStore) dayKey(t time.Time) string {
	return h.keys.Of("rank", "hot", t.In(cst).Format("20060102"))
}

func (h *HeatStore) Record(ctx context.Context, productID, visitor string, kind domain.HeatKind) {
	ctx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 200*time.Millisecond)
	defer cancel()
	day := h.dayKey(time.Now())
	ttl := int(heatRetention.Seconds())
	var err error
	switch kind {
	case domain.HeatView:
		uv := h.keys.Of("rank", "uv", time.Now().In(cst).Format("20060102"), productID)
		err = viewScript.Run(ctx, h.rdb, []string{uv, day}, visitor, productID, ttl).Err()
	case domain.HeatFavorite, domain.HeatOrder:
		score := 3.0
		if kind == domain.HeatOrder {
			score = 5
		}
		pipe := h.rdb.Pipeline()
		pipe.ZIncrBy(ctx, day, score, productID)
		pipe.Expire(ctx, day, heatRetention)
		_, err = pipe.Exec(ctx)
	}
	if err != nil {
		slog.Debug("heat record failed", "product", productID, "error", err)
	}
}

func (h *HeatStore) Remove(ctx context.Context, productID string) {
	ctx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 300*time.Millisecond)
	defer cancel()
	pipe := h.rdb.Pipeline()
	for i := 0; i < 3; i++ {
		pipe.ZRem(ctx, h.dayKey(time.Now().Add(-time.Duration(i)*24*time.Hour)), productID)
	}
	pipe.ZRem(ctx, h.keys.Of("rank", "hot", "3d"), productID)
	_, _ = pipe.Exec(ctx)
}

func (h *HeatStore) TopProductIDs(ctx context.Context, limit int) ([]port.ScoredID, error) {
	t := time.Now()
	dest := h.keys.Of("rank", "hot", "3d")
	keys := []string{h.dayKey(t), h.dayKey(t.Add(-24 * time.Hour)), h.dayKey(t.Add(-48 * time.Hour))}
	pipe := h.rdb.TxPipeline()
	pipe.ZUnionStore(ctx, dest, &redis.ZStore{Keys: keys, Weights: []float64{1, 0.5, 0.25}, Aggregate: "SUM"})
	top := pipe.ZRevRangeWithScores(ctx, dest, 0, int64(limit-1))
	pipe.Expire(ctx, dest, 5*time.Minute)
	if _, err := pipe.Exec(ctx); err != nil {
		return nil, err
	}
	scored := top.Val()
	out := make([]port.ScoredID, 0, len(scored))
	for _, z := range scored {
		id, ok := z.Member.(string)
		if !ok {
			continue
		}
		out = append(out, port.ScoredID{ID: id, Score: z.Score})
	}
	return out, nil
}
