package service

import (
	"context"
	"errors"
	"log/slog"
	"slices"
	"strings"
	"time"

	"qingfly/edu-mall/internal/domain"
	"qingfly/edu-mall/internal/port"
)

type OrderService struct {
	store         port.OrderStore
	cache         port.CatalogCache
	rank          port.RankStore
	timeouts      port.TimeoutQueue
	live          port.LiveNotifier
	flash         port.FlashRedis
	ids           port.OrderIDs
	limiter       port.RateLimiter
	paymentWindow time.Duration
}

func NewOrder(store port.OrderStore, cache port.CatalogCache, rank port.RankStore, timeouts port.TimeoutQueue, live port.LiveNotifier, flash port.FlashRedis, ids port.OrderIDs, limiter port.RateLimiter, paymentWindow time.Duration) *OrderService {
	if paymentWindow <= 0 {
		paymentWindow = 15 * time.Minute
	}
	return &OrderService{store: store, cache: cache, rank: rank, timeouts: timeouts, live: live, flash: flash, ids: ids, limiter: limiter, paymentWindow: paymentWindow}
}

var (
	errAlreadyDone = errors.New("already in target state")
	errNotDue      = errors.New("order not yet expired")
)

func (s *OrderService) Create(ctx context.Context, user *domain.User, input domain.CreateOrderInput) (domain.Order, bool, error) {
	if !keyValid(input.Idem) || !slices.Contains(domain.Campuses, input.Campus) || len(input.ProductID) > 64 {
		return domain.Order{}, false, domain.Invalid("请指定面交校区和有效幂等键")
	}
	old, err := s.store.FindByIdem(ctx, user.ID, input.Idem)
	if err == nil {
		if old.ProductID != input.ProductID || old.Campus != input.Campus {
			return domain.Order{}, false, domain.Conflict("同一幂等键不能用于不同订单")
		}
		return old, false, nil
	}
	if domain.KindOf(err) != domain.KindNotFound {
		return domain.Order{}, false, err
	}
	if retry, allowed := s.limiter.Window(ctx, "order:"+user.ID, 60, time.Minute); !allowed {
		return domain.Order{}, false, domain.Retryable(domain.KindTooMany, "下单太频繁，请稍后再试", int(retry.Seconds()+0.999))
	}
	oid, err := s.ids.Next()
	if err != nil {
		return domain.Order{}, false, err
	}
	var order domain.Order
	err = s.store.InTx(ctx, func(tx port.OrderTx) error {
		p, err := tx.LockProduct(ctx, input.ProductID)
		if err != nil {
			return err
		}
		if p.SellerID == user.ID {
			return domain.Invalid("不能购买自己的商品")
		}
		if p.Demo {
			return domain.Conflict("这是一件展示商品，暂不支持交易")
		}
		if p.Status != "available" {
			return domain.Conflict("商品已被预订或售出")
		}
		ok, err := tx.ReserveProduct(ctx, p.ID, now())
		if err != nil {
			return err
		}
		if !ok {
			return domain.Conflict("商品已被其他同学预订")
		}
		t := now()
		order = domain.Order{ID: oid, BuyerID: user.ID, SellerID: p.SellerID, ProductID: p.ID, Title: p.Title, Image: p.Image, AmountCt: p.PriceCt, Campus: input.Campus, Status: "pending", CreatedAt: t, UpdatedAt: t, ExpiresAt: t + s.paymentWindow.Milliseconds()}
		if err = tx.InsertOrder(ctx, order, input.Idem); err != nil {
			return err
		}
		return tx.InsertEvent(ctx, order.ID, "pending", user.ID, t)
	})
	if err != nil {
		if domain.DuplicateConstraint(err) == "uk_orders_idem" {
			if again, queryErr := s.store.FindByIdem(ctx, user.ID, input.Idem); queryErr == nil {
				return again, false, nil
			}
		}
		return domain.Order{}, false, err
	}
	s.cache.InvalidateProducts(ctx, order.ProductID)
	s.scheduleTimeout(ctx, order.ID, order.ExpiresAt)
	s.rank.Record(ctx, order.ProductID, "", domain.HeatOrder)
	s.notify(order)
	return order, true, nil
}

func (s *OrderService) List(ctx context.Context, user *domain.User) ([]domain.Order, error) {
	return s.store.List(ctx, user.ID)
}

func (s *OrderService) Get(ctx context.Context, user *domain.User, orderID string) (domain.Order, error) {
	o, err := s.store.GetOwned(ctx, orderID, user.ID)
	if err != nil {
		return o, err
	}
	events, err := s.store.Events(ctx, orderID)
	if err != nil {
		return o, err
	}
	o.Events = events
	return o, nil
}

func (s *OrderService) scheduleTimeout(ctx context.Context, orderID string, expiresAt int64) {
	if err := s.timeouts.Add(context.WithoutCancel(ctx), orderID, time.UnixMilli(expiresAt)); err != nil {
		slog.Warn("order timeout enqueue failed, fallback scan will handle it", "order", orderID, "error", err)
	}
}

func (s *OrderService) notify(o domain.Order) {
	s.live.Notify([]string{o.BuyerID, o.SellerID}, map[string]any{"type": "order", "order_id": o.ID})
}

// afterRelease 在关单事务提交后同步缓存/Redis 库存并取消超时任务。
func (s *OrderService) afterRelease(ctx context.Context, o domain.Order) {
	ctx = context.WithoutCancel(ctx)
	if o.FlashSaleID != "" {
		s.flash.Release(ctx, o.FlashSaleID, o.BuyerID, o.ID)
	} else {
		s.cache.InvalidateProducts(ctx, o.ProductID)
	}
	_ = s.timeouts.Remove(ctx, o.ID)
	s.notify(o)
}

func releaseInventory(tx port.OrderTx, ctx context.Context, o domain.Order) error {
	if o.FlashSaleID != "" {
		return tx.ReleaseFlashStock(ctx, o.FlashSaleID, now())
	}
	return tx.ReleaseProduct(ctx, o.ProductID, now())
}

func (s *OrderService) Cancel(ctx context.Context, user *domain.User, orderID string) (domain.Order, error) {
	var order domain.Order
	err := s.store.InTx(ctx, func(tx port.OrderTx) error {
		o, err := tx.LockOwnedOrder(ctx, orderID, user.ID, "buyer_id")
		if err != nil {
			return err
		}
		order = o
		if o.Status == "cancelled" {
			return errAlreadyDone
		}
		if o.Status != "pending" {
			return domain.Conflict("当前状态不能取消")
		}
		sessions, err := tx.CountPaymentSessions(ctx, o.ID)
		if err != nil {
			return err
		}
		if sessions > 0 {
			return domain.Conflict("已打开支付通道，请等待通道关单确认；库存仍保留，避免重复交易")
		}
		if err = tx.UpdateOrderStatus(ctx, o.ID, "cancelled", now()); err != nil {
			return err
		}
		if err = releaseInventory(tx, ctx, o); err != nil {
			return err
		}
		return tx.InsertEvent(ctx, o.ID, "cancelled", user.ID, now())
	})
	if errors.Is(err, errAlreadyDone) {
		return order, nil
	}
	if err != nil {
		return domain.Order{}, err
	}
	order.Status = "cancelled"
	s.afterRelease(ctx, order)
	return order, nil
}

func (s *OrderService) transition(ctx context.Context, user *domain.User, orderID, from, to, role string) (domain.Order, error) {
	var order domain.Order
	err := s.store.InTx(ctx, func(tx port.OrderTx) error {
		o, err := tx.LockOwnedOrder(ctx, orderID, user.ID, role)
		if err != nil {
			return err
		}
		order = o
		if o.Status == to {
			return errAlreadyDone
		}
		if o.Status != from {
			return domain.Conflict("订单状态已变化，请刷新后重试")
		}
		if err = tx.UpdateOrderStatus(ctx, o.ID, to, now()); err != nil {
			return err
		}
		return tx.InsertEvent(ctx, o.ID, to, user.ID, now())
	})
	if errors.Is(err, errAlreadyDone) {
		return order, nil
	}
	if err != nil {
		return domain.Order{}, err
	}
	order.Status = to
	s.notify(order)
	return order, nil
}

func (s *OrderService) Handover(ctx context.Context, user *domain.User, orderID string) (domain.Order, error) {
	return s.transition(ctx, user, orderID, "paid", "handover", "seller_id")
}

func (s *OrderService) Complete(ctx context.Context, user *domain.User, orderID string) (domain.Order, error) {
	return s.transition(ctx, user, orderID, "handover", "completed", "buyer_id")
}

func (s *OrderService) Refund(ctx context.Context, user *domain.User, orderID, reason string) (domain.Order, error) {
	reason = strings.TrimSpace(reason)
	if len(reason) < 6 || len(reason) > 1500 {
		return domain.Order{}, domain.Invalid("请填写退款原因（2–500 字）")
	}
	var order domain.Order
	err := s.store.InTx(ctx, func(tx port.OrderTx) error {
		o, err := tx.LockOwnedOrder(ctx, orderID, user.ID, "buyer_id")
		if err != nil {
			return err
		}
		order = o
		if o.Status == "refund_requested" {
			return errAlreadyDone
		}
		if o.Status != "paid" && o.Status != "handover" && o.Status != "completed" {
			return domain.Conflict("当前状态不能申请退款")
		}
		if err = tx.InsertRefund(ctx, o.ID, reason, now()); err != nil {
			return err
		}
		if err = tx.UpdateOrderStatus(ctx, o.ID, "refund_requested", now()); err != nil {
			return err
		}
		return tx.InsertEvent(ctx, o.ID, "refund_requested", user.ID, now())
	})
	if errors.Is(err, errAlreadyDone) {
		return order, nil
	}
	if err != nil {
		return domain.Order{}, err
	}
	order.Status = "refund_requested"
	s.notify(order)
	return order, nil
}

// Expire 关闭一张超时未付款订单；延迟队列与兜底扫描共用，必须幂等。
func (s *OrderService) Expire(ctx context.Context, orderID string) error {
	var order domain.Order
	released := false
	err := s.store.InTx(ctx, func(tx port.OrderTx) error {
		o, err := tx.LockOrder(ctx, orderID)
		if err != nil {
			return err
		}
		if o.ID == "" || o.Status != "pending" {
			return nil
		}
		order = o
		if o.ExpiresAt > now() {
			return errNotDue
		}
		sessions, err := tx.CountPaymentSessions(ctx, o.ID)
		if err != nil {
			return err
		}
		if sessions > 0 {
			return nil
		}
		if err = tx.UpdateOrderStatus(ctx, o.ID, "cancelled", now()); err != nil {
			return err
		}
		if err = releaseInventory(tx, ctx, o); err != nil {
			return err
		}
		released = true
		return tx.InsertEvent(ctx, o.ID, "cancelled", "expiry", now())
	})
	if errors.Is(err, errNotDue) {
		s.scheduleTimeout(ctx, order.ID, order.ExpiresAt)
		return nil
	}
	if err != nil {
		return err
	}
	if released {
		order.Status = "cancelled"
		s.afterRelease(ctx, order)
	}
	return nil
}

// ExpireUnpaid 是低频兜底扫描，只处理延迟队列漏掉的订单。
func (s *OrderService) ExpireUnpaid(ctx context.Context) error {
	ids, err := s.store.ExpiredIDs(ctx, now(), 200)
	if err != nil {
		return err
	}
	for _, oid := range ids {
		if ctx.Err() != nil {
			return ctx.Err()
		}
		if err = s.Expire(ctx, oid); err != nil {
			return err
		}
	}
	if len(ids) > 0 {
		slog.Info("fallback expiry scan closed orders", "count", len(ids))
	}
	return nil
}

// PaymentWindow 暴露给支付服务与后台任务。
func (s *OrderService) PaymentWindow() time.Duration { return s.paymentWindow }

// NotifyOrder 供支付服务在状态变化后推送。
func (s *OrderService) NotifyOrder(o domain.Order) { s.notify(o) }

// InvalidateProduct 供支付服务在成交后失效缓存。
func (s *OrderService) InvalidateProduct(ctx context.Context, productID string) {
	s.cache.InvalidateProducts(ctx, productID)
}

// RemoveTimeout 供支付服务关闭超时任务。
func (s *OrderService) RemoveTimeout(ctx context.Context, orderID string) {
	_ = s.timeouts.Remove(ctx, orderID)
}

// ReleaseFlash 供支付服务关单后归还 Redis 库存。
func (s *OrderService) ReleaseFlash(ctx context.Context, o domain.Order) {
	s.flash.Release(ctx, o.FlashSaleID, o.BuyerID, o.ID)
}
