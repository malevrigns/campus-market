package platform

import (
	"context"
	"errors"
	"net/http"
	"slices"
	"strings"
	"time"
	"unicode/utf8"

	"qingfly/edu-mall/internal/pkg/cache"
	"qingfly/edu-mall/internal/seckill"
)

// 限时抢购的 HTTP 层：参数校验、限流、错误码映射。核心逻辑见 internal/seckill。

func flashKey(id string) string { return "flash:" + id }

// saleMeta 读取活动元数据（标题/时间/价格），走缓存，抢购高峰时不打数据库。
// 实时库存单独从 Redis 读取，不进缓存。
func (s *Server) saleMeta(ctx context.Context, sid string) (seckill.Sale, error) {
	sale, e := cache.Fetch(ctx, s.cache, flashKey(sid), 30*time.Second, func(ctx context.Context) (seckill.Sale, error) {
		sale, e := s.flash.Get(ctx, sid)
		if errors.Is(e, seckill.ErrNotFound) {
			return sale, cache.ErrNotFound
		}
		return sale, e
	})
	if errors.Is(e, cache.ErrNotFound) {
		return sale, fail(404, "活动不存在")
	}
	if e == nil {
		sale.Phase = seckill.Phase(sale, now())
	}
	return sale, e
}

// flashSales 活动列表：元数据缓存 5 秒，实时库存每次从 Redis MGET。
// server_time 让客户端校准倒计时（不能信任用户本机时间）。
func (s *Server) flashSales(w http.ResponseWriter, r *http.Request, _ *User) error {
	items, e := cache.Fetch(r.Context(), s.cache, "flash:list", 5*time.Second, s.flash.List)
	if e != nil {
		return e
	}
	ptrs := make([]*seckill.Sale, len(items))
	for i := range items {
		items[i].Phase = seckill.Phase(items[i], now())
		ptrs[i] = &items[i]
	}
	s.flash.FillLiveStock(r.Context(), ptrs...)
	return result(w, 200, map[string]any{"items": items, "server_time": now()})
}

func (s *Server) flashSale(w http.ResponseWriter, r *http.Request, _ *User) error {
	sale, e := s.saleMeta(r.Context(), r.PathValue("id"))
	if e != nil {
		return e
	}
	s.flash.FillLiveStock(r.Context(), &sale)
	return result(w, 200, map[string]any{"sale": sale, "server_time": now()})
}

func (s *Server) flashAttempt(w http.ResponseWriter, r *http.Request, u *User) error {
	ctx := r.Context()
	sid := r.PathValue("id")
	if len(sid) > 64 {
		return fail(404, "活动不存在")
	}
	// 两级令牌桶：单用户 2 次/秒（挡住脚本连点），单活动 2000 次/秒（超出部分直接削峰，
	// 返回"排队人数过多"，保护 Redis 和下游）。
	if !s.limiter.Bucket(ctx, "flash:user:"+u.ID, 2, 5).Allowed {
		return fail(429, "操作太快了，请稍后再试")
	}
	if !s.limiter.Bucket(ctx, "flash:sale:"+sid, 2000, 4000).Allowed {
		return fail(429, "当前抢购人数过多，请稍后重试")
	}
	sale, e := s.saleMeta(ctx, sid)
	if e != nil {
		return e
	}
	t, e := s.flash.Attempt(ctx, sale, u.ID)
	switch {
	case e == nil:
		status := http.StatusAccepted
		if t.State != "queued" {
			status = http.StatusOK
		}
		return result(w, status, t)
	case errors.Is(e, seckill.ErrSoldOut):
		return fail(409, "已抢光")
	case errors.Is(e, seckill.ErrNotStarted):
		return fail(409, "活动尚未开始")
	case errors.Is(e, seckill.ErrEnded):
		return fail(410, "活动已结束")
	case errors.Is(e, seckill.ErrOffline):
		return fail(410, "活动已下线")
	case errors.Is(e, seckill.ErrOwnSale):
		return fail(400, "不能抢购自己发起的活动")
	case errors.Is(e, seckill.ErrUnavailable):
		w.Header().Set("Retry-After", "1")
		return fail(503, "抢购通道繁忙，请稍后重试")
	}
	return e
}

func (s *Server) flashResult(w http.ResponseWriter, r *http.Request, u *User) error {
	t, e := s.flash.Result(r.Context(), r.PathValue("id"), u.ID)
	if errors.Is(e, seckill.ErrUnavailable) {
		return fail(503, "查询繁忙，请稍后重试")
	}
	if e != nil {
		return e
	}
	return result(w, 200, t)
}

func (s *Server) createFlashSale(w http.ResponseWriter, r *http.Request, u *User) error {
	var v struct {
		Title           string `json:"title"`
		Description     string `json:"description"`
		Image           string `json:"image"`
		Campus          string `json:"campus"`
		OriginalPriceCt int64  `json:"original_price_ct"`
		PriceCt         int64  `json:"price_ct"`
		TotalStock      int    `json:"total_stock"`
		StartAt         int64  `json:"start_at"`
		EndAt           int64  `json:"end_at"`
	}
	if e := decode(r, &v); e != nil {
		return e
	}
	v.Title = strings.TrimSpace(v.Title)
	v.Description = strings.TrimSpace(v.Description)
	t := now()
	if utf8.RuneCountInString(v.Title) < 2 || utf8.RuneCountInString(v.Title) > 80 || utf8.RuneCountInString(v.Description) > 3000 || !validImageURL(v.Image) || !slices.Contains(campuses, v.Campus) {
		return fail(400, "请检查标题、描述、图片地址和校区")
	}
	if v.PriceCt < 1 || v.OriginalPriceCt < v.PriceCt || v.OriginalPriceCt > 10000000 || v.TotalStock < 1 || v.TotalStock > 100000 {
		return fail(400, "请检查价格（抢购价不高于原价）和库存（1–100000）")
	}
	if v.StartAt < t-60000 || v.EndAt <= v.StartAt || v.EndAt-v.StartAt > 7*24*time.Hour.Milliseconds() || v.StartAt-t > 30*24*time.Hour.Milliseconds() {
		return fail(400, "活动需在 30 天内开始，持续不超过 7 天")
	}
	sale, e := s.flash.Create(r.Context(), seckill.Sale{ID: id("fs_"), SellerID: u.ID, Title: v.Title, Description: v.Description, Image: v.Image, Campus: v.Campus, OriginalPriceCt: v.OriginalPriceCt, PriceCt: v.PriceCt, TotalStock: v.TotalStock, StartAt: v.StartAt, EndAt: v.EndAt})
	if e != nil {
		return e
	}
	s.cache.Invalidate(r.Context(), "flash:list")
	return result(w, 201, sale)
}

func (s *Server) cancelFlashSale(w http.ResponseWriter, r *http.Request, _ *User) error {
	e := s.flash.Cancel(r.Context(), r.PathValue("id"))
	if errors.Is(e, seckill.ErrNotFound) {
		return fail(409, "活动不存在或已下线")
	}
	if e != nil {
		return e
	}
	s.cache.Invalidate(r.Context(), flashKey(r.PathValue("id")), "flash:list")
	return result(w, 200, map[string]bool{"cancelled": true})
}

// onFlashOrderCreated 在抢购订单异步落库后调用：投递超时关单任务并推送给买家。
func (s *Server) onFlashOrderCreated(ctx context.Context, o seckill.CreatedOrder) {
	s.scheduleTimeout(ctx, o.OrderID, o.ExpiresAt)
	s.live.Notify([]string{o.BuyerID}, map[string]any{"type": "flash", "sale_id": o.SaleID, "order_id": o.OrderID, "state": "success"})
	s.live.Notify([]string{o.SellerID}, map[string]any{"type": "order", "order_id": o.OrderID})
}

func (s *Server) onFlashOrderFailed(ctx context.Context, saleID, buyerID, orderID, reason string) {
	s.live.Notify([]string{buyerID}, map[string]any{"type": "flash", "sale_id": saleID, "order_id": orderID, "state": "failed", "reason": reason})
}
