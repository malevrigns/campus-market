package service

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"strings"
	"sync"
	"time"

	"qingfly/edu-mall/internal/domain"
	"qingfly/edu-mall/internal/pkg/idgen"
	"qingfly/edu-mall/internal/port"
)

type FlashService struct {
	store         port.FlashStore
	cache         port.FlashCache
	redis         port.FlashRedis
	orders        port.OrderStore
	ids           port.OrderIDs
	timeouts      port.TimeoutQueue
	live          port.LiveNotifier
	limiter       port.RateLimiter
	paymentWindow time.Duration

	mu         sync.Mutex
	mismatches map[string][2]int
}

func NewFlash(store port.FlashStore, cache port.FlashCache, redis port.FlashRedis, orders port.OrderStore, ids port.OrderIDs, timeouts port.TimeoutQueue, live port.LiveNotifier, limiter port.RateLimiter, paymentWindow time.Duration) *FlashService {
	if paymentWindow <= 0 {
		paymentWindow = 15 * time.Minute
	}
	return &FlashService{store: store, cache: cache, redis: redis, orders: orders, ids: ids, timeouts: timeouts, live: live, limiter: limiter, paymentWindow: paymentWindow, mismatches: map[string][2]int{}}
}

// Phase 根据时间计算活动阶段。
func Phase(s domain.FlashSale, now int64) string {
	switch {
	case s.Status == "cancelled":
		return "cancelled"
	case now < s.StartAt:
		return "upcoming"
	case now >= s.EndAt:
		return "ended"
	default:
		return "active"
	}
}

func (s *FlashService) getSale(ctx context.Context, id string) (domain.FlashSale, error) {
	sale, err := s.cache.Sale(ctx, id, 30*time.Second, func(ctx context.Context) (domain.FlashSale, error) {
		return s.store.Get(ctx, id)
	})
	if err != nil {
		return sale, err
	}
	sale.Phase = Phase(sale, now())
	return sale, nil
}

func (s *FlashService) List(ctx context.Context) (domain.FlashList, error) {
	items, err := s.cache.List(ctx, 5*time.Second, func(ctx context.Context) ([]domain.FlashSale, error) {
		return s.store.List(ctx, now()-24*time.Hour.Milliseconds(), 50)
	})
	if err != nil {
		return domain.FlashList{}, err
	}
	ptrs := make([]*domain.FlashSale, len(items))
	for i := range items {
		items[i].Phase = Phase(items[i], now())
		ptrs[i] = &items[i]
	}
	s.redis.FillLiveStock(ctx, ptrs...)
	return domain.FlashList{Items: items, ServerTime: now()}, nil
}

func (s *FlashService) Get(ctx context.Context, id string) (domain.FlashDetail, error) {
	sale, err := s.getSale(ctx, id)
	if err != nil {
		return domain.FlashDetail{}, err
	}
	s.redis.FillLiveStock(ctx, &sale)
	return domain.FlashDetail{Sale: sale, ServerTime: now()}, nil
}

func (s *FlashService) Attempt(ctx context.Context, user *domain.User, saleID string) (domain.Ticket, bool, error) {
	if len(saleID) > 64 {
		return domain.Ticket{}, false, domain.NotFound("活动不存在")
	}
	if retry, allowed := s.limiter.Bucket(ctx, "flash:user:"+user.ID, 2, 5); !allowed {
		return domain.Ticket{}, false, domain.Retryable(domain.KindTooMany, "操作太快了，请稍后再试", int(retry.Seconds()+0.999))
	}
	if retry, allowed := s.limiter.Bucket(ctx, "flash:sale:"+saleID, 2000, 4000); !allowed {
		return domain.Ticket{}, false, domain.Retryable(domain.KindTooMany, "当前抢购人数过多，请稍后重试", int(retry.Seconds()+0.999))
	}
	sale, err := s.getSale(ctx, saleID)
	if err != nil {
		return domain.Ticket{}, false, err
	}
	if sale.SellerID == user.ID {
		return domain.Ticket{}, false, domain.Invalid("不能抢购自己发起的活动")
	}
	orderID, err := s.ids.Next()
	if err != nil {
		return domain.Ticket{}, false, err
	}
	ticket, err := s.redis.Attempt(ctx, sale, user.ID, orderID)
	if err != nil {
		return domain.Ticket{}, false, err
	}
	if ticket.State == "success" || ticket.State == "cancelled" {
		if o, found, err := s.orders.FlashOrder(ctx, saleID, user.ID); err == nil && found {
			ticket.OrderStatus = o.Status
		}
	}
	return ticket, ticket.State == "queued", nil
}

func (s *FlashService) Result(ctx context.Context, user *domain.User, saleID string) (domain.Ticket, error) {
	ticket, found, err := s.redis.Ticket(ctx, saleID, user.ID)
	if err != nil {
		return domain.Ticket{}, err
	}
	if found {
		if ticket.State == "queued" || ticket.State == "success" || ticket.State == "cancelled" {
			if o, ok, _ := s.orders.FlashOrder(ctx, saleID, user.ID); ok {
				ticket.OrderStatus = o.Status
				if ticket.State == "queued" {
					ticket.State = "success"
				}
			}
		}
		if ticket.State == "failed" {
			ticket.Reason = "库存已被抢完或系统繁忙，请重试"
		}
		return ticket, nil
	}
	if o, ok, err := s.orders.FlashOrder(ctx, saleID, user.ID); err != nil {
		return domain.Ticket{}, err
	} else if ok {
		return domain.Ticket{SaleID: saleID, OrderID: o.ID, State: "success", OrderStatus: o.Status}, nil
	}
	return domain.Ticket{SaleID: saleID, State: "none"}, nil
}

func (s *FlashService) Create(ctx context.Context, user *domain.User, input domain.CreateFlashSaleInput) (domain.FlashSale, error) {
	var out domain.FlashSale
	input.Title = strings.TrimSpace(input.Title)
	input.Description = strings.TrimSpace(input.Description)
	t := now()
	if !validText(input.Title, 2, 80) || len([]rune(input.Description)) > 3000 || !validImageURL(input.Image) || !contains(domain.Campuses, input.Campus) {
		return out, domain.Invalid("请检查标题、描述、图片地址和校区")
	}
	if input.PriceCt < 1 || input.OriginalPriceCt < input.PriceCt || input.OriginalPriceCt > 10000000 || input.TotalStock < 1 || input.TotalStock > 100000 {
		return out, domain.Invalid("请检查价格（抢购价不高于原价）和库存（1–100000）")
	}
	if input.StartAt < t-60000 || input.EndAt <= input.StartAt || input.EndAt-input.StartAt > 7*24*time.Hour.Milliseconds() || input.StartAt-t > 30*24*time.Hour.Milliseconds() {
		return out, domain.Invalid("活动需在 30 天内开始，持续不超过 7 天")
	}
	sale := domain.FlashSale{ID: idgen.New("fs_"), SellerID: user.ID, SellerName: user.Name, Title: input.Title, Description: input.Description, Image: input.Image, Campus: input.Campus, OriginalPriceCt: input.OriginalPriceCt, PriceCt: input.PriceCt, TotalStock: input.TotalStock, Stock: input.TotalStock, StartAt: input.StartAt, EndAt: input.EndAt, Status: "scheduled", CreatedAt: t}
	if err := s.store.Create(ctx, sale); err != nil {
		return out, err
	}
	if err := s.redis.Preheat(ctx, sale, true); err != nil {
		slog.Warn("flash sale preheat failed", "sale", sale.ID, "error", err)
	}
	s.cache.Invalidate(ctx, sale.ID)
	detail, err := s.Get(ctx, sale.ID)
	return detail.Sale, err
}

func (s *FlashService) Cancel(ctx context.Context, saleID string) error {
	ok, err := s.store.Cancel(ctx, saleID, now())
	if err != nil {
		return err
	}
	if !ok {
		return domain.Conflict("活动不存在或已下线")
	}
	if err = s.redis.SetStatus(ctx, saleID, "off"); err != nil {
		slog.Warn("flash sale redis status update failed", "sale", saleID, "error", err)
	}
	s.cache.Invalidate(ctx, saleID)
	return nil
}

// Warm 预热所有未结束的活动（启动时、以及对账发现 Redis 数据丢失时）。
func (s *FlashService) Warm(ctx context.Context) error {
	sales, err := s.store.WarmList(ctx, now()-s.paymentWindow.Milliseconds(), 200)
	if err != nil {
		return err
	}
	for _, sale := range sales {
		if err = s.redis.Preheat(ctx, sale, false); err != nil {
			return err
		}
	}
	return s.redis.EnsureGroup(ctx)
}

type flashRejected struct{ reason string }

func (e flashRejected) Error() string { return "rejected: " + e.reason }

var errAlreadyPersisted = errors.New("order already persisted")

// persist 在一个事务里扣减 DB 库存并创建订单；预生成订单号保证重复消费幂等。
func (s *FlashService) persist(ctx context.Context, msg domain.FlashOrderMessage) (domain.CreatedFlashOrder, error) {
	var out domain.CreatedFlashOrder
	err := s.orders.InTx(ctx, func(tx port.OrderTx) error {
		t := now()
		ok, err := tx.DecrementFlashStock(ctx, msg.SaleID, t)
		if err != nil {
			return err
		}
		if !ok {
			return flashRejected{"sold_out"}
		}
		seller, title, image, campus, price, err := tx.FlashSaleOrderInfo(ctx, msg.SaleID)
		if err != nil {
			return err
		}
		if seller == msg.UserID {
			return flashRejected{"own_sale"}
		}
		expires := t + s.paymentWindow.Milliseconds()
		o := domain.Order{ID: msg.OrderID, BuyerID: msg.UserID, SellerID: seller, FlashSaleID: msg.SaleID, Title: title, Image: image, AmountCt: price, Campus: campus, Status: "pending", CreatedAt: t, UpdatedAt: t, ExpiresAt: expires}
		if err = tx.InsertOrder(ctx, o, "flash:"+msg.SaleID); err != nil {
			if constraint := domain.DuplicateConstraint(err); constraint != "" {
				if constraint == "PRIMARY" {
					return errAlreadyPersisted
				}
				return flashRejected{"already_bought"}
			}
			return err
		}
		if err = tx.InsertEvent(ctx, o.ID, "pending", msg.UserID, t); err != nil {
			return err
		}
		out = domain.CreatedFlashOrder{OrderID: o.ID, SaleID: msg.SaleID, BuyerID: msg.UserID, SellerID: seller, ExpiresAt: expires}
		return nil
	})
	if errors.Is(err, errAlreadyPersisted) {
		o, found, queryErr := s.orders.FlashOrder(ctx, msg.SaleID, msg.UserID)
		if queryErr != nil {
			return out, queryErr
		}
		if !found {
			return out, errors.New("persisted order missing")
		}
		return domain.CreatedFlashOrder{OrderID: o.ID, SaleID: msg.SaleID, BuyerID: msg.UserID, SellerID: o.SellerID, ExpiresAt: o.ExpiresAt}, nil
	}
	return out, err
}

func (s *FlashService) handleMessage(ctx context.Context, msg domain.FlashOrderMessage) bool {
	created, err := s.persist(ctx, msg)
	var rejected flashRejected
	switch {
	case err == nil:
		_ = s.redis.Transition(ctx, msg.SaleID, msg.UserID, msg.OrderID, "ok", 0, "q")
		s.onOrderCreated(ctx, created)
		return true
	case errors.As(err, &rejected):
		s.compensate(ctx, msg, rejected.reason)
		return true
	default:
		slog.Warn("flash order persist failed, will retry", "order", msg.OrderID, "error", err)
		return false
	}
}

func (s *FlashService) onOrderCreated(ctx context.Context, o domain.CreatedFlashOrder) {
	if err := s.timeouts.Add(context.WithoutCancel(ctx), o.OrderID, time.UnixMilli(o.ExpiresAt)); err != nil {
		slog.Warn("flash order timeout enqueue failed", "order", o.OrderID, "error", err)
	}
	s.live.Notify([]string{o.BuyerID}, map[string]any{"type": "flash", "sale_id": o.SaleID, "order_id": o.OrderID, "state": "success"})
	s.live.Notify([]string{o.SellerID}, map[string]any{"type": "order", "order_id": o.OrderID})
}

func (s *FlashService) compensate(ctx context.Context, msg domain.FlashOrderMessage, reason string) {
	give := 1
	if reason == "sold_out" {
		give = 0
	}
	if err := s.redis.Transition(ctx, msg.SaleID, msg.UserID, msg.OrderID, "fail", give, "q"); err != nil {
		slog.Warn("flash compensation failed", "order", msg.OrderID, "error", err)
	}
	slog.Info("flash order rejected", "sale", msg.SaleID, "order", msg.OrderID, "reason", reason)
	s.live.Notify([]string{msg.UserID}, map[string]any{"type": "flash", "sale_id": msg.SaleID, "order_id": msg.OrderID, "state": "failed", "reason": reason})
}

func (s *FlashService) process(ctx context.Context, msg domain.FlashOrderMessage) {
	pctx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 10*time.Second)
	defer cancel()
	if s.handleMessage(pctx, msg) {
		if err := s.redis.Ack(pctx, msg.StreamID); err != nil {
			slog.Warn("flash xack failed", "id", msg.StreamID, "error", err)
		}
	}
}

// Run 启动抢购落库 worker、超时消息回收与流修剪，直到 ctx 结束。
func (s *FlashService) Run(ctx context.Context) error {
	if err := s.redis.EnsureGroup(ctx); err != nil {
		return err
	}
	var wg sync.WaitGroup
	for i := 0; i < 4; i++ {
		wg.Add(1)
		go func(name string) {
			defer wg.Done()
			s.consume(ctx, name)
		}(fmt.Sprintf("%s-%d", s.redis.Consumer(), i))
	}
	wg.Add(1)
	go func() {
		defer wg.Done()
		ticker := time.NewTicker(5 * time.Second)
		defer ticker.Stop()
		for {
			select {
			case <-ctx.Done():
				return
			case <-ticker.C:
				if err := s.reclaim(ctx); err != nil && ctx.Err() == nil {
					slog.Warn("flash reclaim failed", "error", err)
				}
			}
		}
	}()
	wg.Wait()
	return nil
}

func (s *FlashService) consume(ctx context.Context, name string) {
	for ctx.Err() == nil {
		msgs, err := s.redis.Read(ctx, name, 16, 2*time.Second)
		if err != nil {
			if ctx.Err() != nil {
				return
			}
			if strings.Contains(err.Error(), "NOGROUP") {
				_ = s.redis.EnsureGroup(ctx)
			}
			slog.Warn("flash stream read failed", "error", err)
			time.Sleep(time.Second)
			continue
		}
		for _, msg := range msgs {
			s.process(ctx, msg)
		}
	}
}

func (s *FlashService) reclaim(ctx context.Context) error {
	pending, err := s.redis.Pending(ctx, s.redis.ClaimIdle(), 50)
	if err != nil {
		if strings.Contains(err.Error(), "NOGROUP") {
			return s.redis.EnsureGroup(ctx)
		}
		return err
	}
	consumer := s.redis.Consumer() + "-reclaim"
	for _, p := range pending {
		msgs, err := s.redis.Claim(ctx, consumer, s.redis.ClaimIdle(), p.ID)
		if err != nil {
			return err
		}
		for _, msg := range msgs {
			if p.RetryCount >= s.redis.MaxDeliveries() {
				if err := s.redis.DeadLetter(ctx, msg, p.RetryCount); err != nil {
					slog.Error("flash dead letter write failed", "id", msg.StreamID, "error", err)
					continue
				}
				s.compensate(ctx, msg, "system")
				_ = s.redis.Ack(ctx, msg.StreamID)
				slog.Error("flash order moved to dead letter", "id", msg.StreamID, "deliveries", p.RetryCount)
				continue
			}
			s.process(ctx, msg)
		}
	}
	return s.redis.Trim(ctx)
}

// Reconcile 对账：Redis 库存应等于 DB 库存减去仍在排队落库的资格数。
// 同一偏差连续两轮观察到才修正，避免和正在落库的消息竞争。
func (s *FlashService) Reconcile(ctx context.Context) error {
	sales, err := s.store.ReconcileList(ctx, now())
	if err != nil {
		return err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	seen := map[string]bool{}
	for _, sale := range sales {
		seen[sale.ID] = true
		redisStock, queued, err := s.redis.Inspect(ctx, sale.ID)
		if err != nil {
			return err
		}
		if redisStock < 0 {
			slog.Warn("flash sale missing in redis, re-preheating", "sale", sale.ID)
			if err = s.redis.Preheat(ctx, sale, false); err != nil {
				return err
			}
			continue
		}
		expected := max(sale.Stock-queued, 0)
		if redisStock == expected {
			delete(s.mismatches, sale.ID)
			continue
		}
		obs := [2]int{redisStock, expected}
		if prev, ok := s.mismatches[sale.ID]; ok && prev == obs {
			fixed, err := s.redis.CASStock(ctx, sale.ID, redisStock, expected)
			if err != nil {
				return err
			}
			slog.Warn("flash stock reconciled", "sale", sale.ID, "redis", redisStock, "expected", expected, "applied", fixed)
			delete(s.mismatches, sale.ID)
			s.redis.ClearSoldOut(sale.ID)
			continue
		}
		s.mismatches[sale.ID] = obs
	}
	for id := range s.mismatches {
		if !seen[id] {
			delete(s.mismatches, id)
		}
	}
	return nil
}

func (s *FlashService) Stats(ctx context.Context) map[string]any { return s.redis.Stats(ctx) }
