package platform

import (
	"context"
	"log/slog"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/redis/go-redis/v9"

	"qingfly/edu-mall/internal/pkg/cache"
)

// 热门商品榜：
//   - 每天一个 ZSET（rank:hot:20260101），浏览 +1、收藏 +3、下单 +5；
//   - 浏览按"访客 + 商品 + 天"去重：每个商品每天一个 HyperLogLog，PFADD 返回 1（新访客）才计分，
//     12KB 封顶的内存就能统计 UV，避免刷新页面刷榜；
//   - 榜单 = 最近 3 天按 1 / 0.5 / 0.25 加权合并（ZUNIONSTORE WEIGHTS），实现简单的时间衰减；
//   - 合并结果缓存 60 秒，避免每次请求都做 ZUNIONSTORE。
type heatKind int

const (
	heatView heatKind = iota
	heatFavorite
	heatOrder
)

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

func (s *Server) heatDayKey(t time.Time) string {
	return s.keys.Of("rank", "hot", t.In(cst).Format("20060102"))
}

// recordHeat 记录热度。只用 200ms 的独立超时：统计失败不影响主流程。
func (s *Server) recordHeat(ctx context.Context, pid, visitor string, kind heatKind) {
	ctx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 200*time.Millisecond)
	defer cancel()
	day := s.heatDayKey(time.Now())
	ttl := int(heatRetention.Seconds())
	var err error
	switch kind {
	case heatView:
		uv := s.keys.Of("rank", "uv", time.Now().In(cst).Format("20060102"), pid)
		err = viewScript.Run(ctx, s.rdb, []string{uv, day}, visitor, pid, ttl).Err()
	case heatFavorite, heatOrder:
		score := 3.0
		if kind == heatOrder {
			score = 5
		}
		pipe := s.rdb.Pipeline()
		pipe.ZIncrBy(ctx, day, score, pid)
		pipe.Expire(ctx, day, heatRetention)
		_, err = pipe.Exec(ctx)
	}
	if err != nil {
		slog.Debug("heat record failed", "product", pid, "error", err)
	}
}

func (s *Server) removeHeat(ctx context.Context, pid string) {
	ctx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 300*time.Millisecond)
	defer cancel()
	pipe := s.rdb.Pipeline()
	for i := 0; i < 3; i++ {
		pipe.ZRem(ctx, s.heatDayKey(time.Now().Add(-time.Duration(i)*24*time.Hour)), pid)
	}
	pipe.ZRem(ctx, s.keys.Of("rank", "hot", "3d"), pid)
	_, _ = pipe.Exec(ctx)
}

type hotItem struct {
	Product
	Score float64 `json:"score"`
}

func (s *Server) hotProducts(w http.ResponseWriter, r *http.Request, _ *User) error {
	limit, _ := strconv.Atoi(r.URL.Query().Get("limit"))
	if limit <= 0 || limit > 50 {
		limit = 10
	}
	items, e := cache.Fetch(r.Context(), s.cache, "products:hot:v1", time.Minute, s.buildHotList)
	if e != nil {
		return e
	}
	if len(items) > limit {
		items = items[:limit]
	}
	return result(w, 200, map[string]any{"items": items})
}

func (s *Server) buildHotList(ctx context.Context) ([]hotItem, error) {
	t := time.Now()
	dest := s.keys.Of("rank", "hot", "3d")
	keys := []string{s.heatDayKey(t), s.heatDayKey(t.Add(-24 * time.Hour)), s.heatDayKey(t.Add(-48 * time.Hour))}
	pipe := s.rdb.TxPipeline()
	pipe.ZUnionStore(ctx, dest, &redis.ZStore{Keys: keys, Weights: []float64{1, 0.5, 0.25}, Aggregate: "SUM"})
	top := pipe.ZRevRangeWithScores(ctx, dest, 0, 79)
	pipe.Expire(ctx, dest, 5*time.Minute)
	if _, e := pipe.Exec(ctx); e != nil {
		return nil, e
	}
	scored := top.Val()
	items := []hotItem{}
	if len(scored) == 0 {
		return items, nil
	}
	ids := make([]any, 0, len(scored))
	for _, z := range scored {
		ids = append(ids, z.Member)
	}
	rows, e := s.db.QueryContext(ctx, productSelect+" WHERE p.listed=1 AND p.id IN (?"+strings.Repeat(",?", len(ids)-1)+")", ids...)
	if e != nil {
		return nil, e
	}
	defer rows.Close()
	byID := map[string]Product{}
	for rows.Next() {
		p, e := scanProduct(rows)
		if e != nil {
			return nil, e
		}
		byID[p.ID] = p
	}
	if e = rows.Err(); e != nil {
		return nil, e
	}
	for _, z := range scored {
		pid, _ := z.Member.(string)
		if p, ok := byID[pid]; ok {
			items = append(items, hotItem{Product: p, Score: z.Score})
		}
		if len(items) == 50 {
			break
		}
	}
	return items, nil
}
