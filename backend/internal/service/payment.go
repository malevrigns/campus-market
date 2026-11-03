package service

import (
	"context"
	"errors"
	"log/slog"
	"net/url"

	"qingfly/edu-mall/internal/domain"
	"qingfly/edu-mall/internal/port"
)

type PaymentService struct {
	store    port.PaymentStore
	gateway  port.PaymentGateway
	cache    port.CatalogCache
	timeouts port.TimeoutQueue
	live     port.LiveNotifier
	flash    port.FlashRedis
}

func NewPayment(store port.PaymentStore, gateway port.PaymentGateway, cache port.CatalogCache, timeouts port.TimeoutQueue, live port.LiveNotifier, flash port.FlashRedis) *PaymentService {
	return &PaymentService{store: store, gateway: gateway, cache: cache, timeouts: timeouts, live: live, flash: flash}
}

func (s *PaymentService) Enabled() bool {
	return s.gateway != nil && s.gateway.Enabled()
}

func (s *PaymentService) Start(ctx context.Context, user *domain.User, orderID, method string, mobile bool) (domain.PaymentStart, error) {
	var out domain.PaymentStart
	if !s.Enabled() {
		return out, domain.Unavailable("在线支付尚未开通，请稍后再试")
	}
	if method != "alipay" {
		return out, domain.Invalid("不支持该支付方式")
	}
	err := s.store.InTx(ctx, func(tx port.PaymentTx) error {
		o, err := tx.LockOwnedOrder(ctx, orderID, user.ID, "buyer_id")
		if err != nil {
			return err
		}
		if o.Status != "pending" || o.ExpiresAt <= now() {
			return domain.Conflict("订单已支付、关闭或超时")
		}
		checkout, err := s.gateway.Checkout(ctx, o, mobile)
		if err != nil {
			return err
		}
		if err = tx.UpsertPayment(ctx, o.ID, checkout, now()); err != nil {
			return err
		}
		out = domain.PaymentStart{CheckoutURL: checkout, OrderID: o.ID, AmountCt: o.AmountCt}
		return nil
	})
	return out, err
}

// Notify 处理已验签的支付宝表单回调。
func (s *PaymentService) Notify(ctx context.Context, values url.Values) error {
	if s.gateway == nil {
		return domain.Unavailable("payment disabled")
	}
	evidence, err := s.gateway.VerifyNotification(values)
	if err != nil {
		return err
	}
	return s.applyEvidence(ctx, evidence)
}

func (s *PaymentService) applyEvidence(ctx context.Context, evidence domain.PaymentEvidence) error {
	if evidence.Status != "TRADE_SUCCESS" && evidence.Status != "TRADE_FINISHED" && evidence.Status != "TRADE_CLOSED" {
		return domain.Invalid("unsupported trade status")
	}
	if evidence.AmountCt <= 0 {
		return domain.Invalid("bad amount")
	}
	var order domain.Order
	var next string
	var changed, replay bool
	err := s.store.InTx(ctx, func(tx port.PaymentTx) error {
		o, err := tx.LockOrder(ctx, evidence.OrderID)
		if err != nil {
			return err
		}
		if o.ID == "" {
			return domain.NotFound("内容不存在或无权访问")
		}
		order = o
		if evidence.AmountCt != o.AmountCt {
			return domain.Invalid("amount mismatch")
		}
		tradeNo, found, err := tx.PaymentTradeNo(ctx, o.ID)
		if err != nil {
			return err
		}
		if !found {
			return domain.NotFound("内容不存在或无权访问")
		}
		if tradeNo != "" && tradeNo != evidence.TradeNo {
			return domain.Conflict("trade mismatch")
		}
		previous, hasPrevious, err := tx.FindCallback(ctx, evidence.NotifyID)
		if err != nil {
			return err
		}
		if hasPrevious {
			if previous.OrderID != o.ID || previous.TradeNo != evidence.TradeNo || previous.Status != evidence.Status || previous.AmountCt != evidence.AmountCt {
				return domain.Conflict("callback identity reused")
			}
			replay = true
			return nil
		}
		next = o.Status
		if evidence.Status == "TRADE_CLOSED" {
			if o.Status == "pending" {
				next = "cancelled"
			} else if o.Status != "cancelled" {
				return domain.Conflict("paid trade closure requires refund reconciliation")
			}
		} else if o.Status == "pending" {
			next = "paid"
		} else if o.Status == "cancelled" {
			return domain.Conflict("closed order cannot be paid")
		}
		if err = tx.UpdatePaymentTradeNo(ctx, o.ID, evidence.TradeNo); err != nil {
			return err
		}
		if err = tx.InsertCallback(ctx, domain.PaymentCallback{NotifyID: evidence.NotifyID, OrderID: o.ID, TradeNo: evidence.TradeNo, Status: evidence.Status, AmountCt: evidence.AmountCt, CreatedAt: now()}); err != nil {
			return err
		}
		if next == o.Status {
			return nil
		}
		changed = true
		if err = tx.UpdateOrderStatus(ctx, o.ID, next, now()); err != nil {
			return err
		}
		if next == "cancelled" {
			if err = releaseInventory(tx, ctx, o); err != nil {
				return err
			}
		} else if o.ProductID != "" {
			if err = tx.MarkProductSold(ctx, o.ProductID, now()); err != nil {
				return err
			}
		}
		return tx.InsertEvent(ctx, o.ID, next, "alipay", now())
	})
	if err != nil {
		return err
	}
	if replay {
		return nil
	}
	postCtx := context.WithoutCancel(ctx)
	if next == "cancelled" {
		s.afterRelease(postCtx, order)
		return nil
	}
	if changed {
		_ = s.timeouts.Remove(postCtx, order.ID)
		if order.ProductID != "" {
			s.cache.InvalidateProducts(postCtx, order.ProductID)
		}
	}
	s.live.Notify([]string{order.BuyerID, order.SellerID}, map[string]any{"type": "order", "order_id": order.ID})
	return nil
}

func (s *PaymentService) afterRelease(ctx context.Context, o domain.Order) {
	if o.FlashSaleID != "" {
		s.flash.Release(ctx, o.FlashSaleID, o.BuyerID, o.ID)
	} else {
		s.cache.InvalidateProducts(ctx, o.ProductID)
	}
	_ = s.timeouts.Remove(ctx, o.ID)
	s.live.Notify([]string{o.BuyerID, o.SellerID}, map[string]any{"type": "order", "order_id": o.ID})
}

// Reconcile 关闭回调丢失的缺口：已打开支付通道的待支付订单主动查单。
func (s *PaymentService) Reconcile(ctx context.Context) error {
	if !s.Enabled() {
		return nil
	}
	candidates, err := s.store.ReconcileCandidates(ctx, now(), 30)
	if err != nil {
		return err
	}
	for _, o := range candidates {
		if ctx.Err() != nil {
			return ctx.Err()
		}
		if err = s.store.MarkChecked(ctx, o.ID, now()); err != nil {
			return err
		}
		if err = s.ReconcileOne(ctx, o); err != nil {
			slog.Warn("payment reconciliation pending", "order_id", o.ID, "error", err)
		}
	}
	return nil
}

// ReconcileOne 对单笔订单做签名查询；也供测试直接驱动。
func (s *PaymentService) ReconcileOne(ctx context.Context, o domain.Order) error {
	q, err := s.gateway.Query(ctx, o.ID)
	if err != nil {
		return err
	}
	if q.Code == "40004" && q.SubCode == "ACQ.TRADE_NOT_EXIST" {
		if now() <= o.ExpiresAt+60000 {
			return nil
		}
		return s.applyEvidence(ctx, domain.PaymentEvidence{NotifyID: "query_absent_" + o.ID, OrderID: o.ID, TradeNo: "absent_" + o.ID, Status: "TRADE_CLOSED", AmountCt: o.AmountCt, FromQuery: true})
	}
	if q.Code != "10000" || q.OrderID != o.ID || q.TradeNo == "" {
		return errors.New("gateway query did not confirm order")
	}
	amount, err := domain.ParseMoney(q.Amount)
	if err != nil || amount != o.AmountCt {
		return errors.New("gateway query amount mismatch")
	}
	if q.Status == "WAIT_BUYER_PAY" {
		if now() <= o.ExpiresAt+60000 {
			return nil
		}
		closed, err := s.gateway.Close(ctx, o.ID)
		if err != nil {
			return err
		}
		if closed.Code != "10000" || closed.OrderID != o.ID {
			return errors.New("gateway close not confirmed")
		}
		q, err = s.gateway.Query(ctx, o.ID)
		if err != nil {
			return err
		}
		amount, err = domain.ParseMoney(q.Amount)
		if err != nil || amount != o.AmountCt || q.Code != "10000" || q.OrderID != o.ID || q.TradeNo == "" {
			return errors.New("post-close query not confirmed")
		}
	}
	if q.Status == "WAIT_BUYER_PAY" {
		return nil
	}
	if q.Status != "TRADE_SUCCESS" && q.Status != "TRADE_FINISHED" && q.Status != "TRADE_CLOSED" {
		return errors.New("unsupported gateway trade status")
	}
	return s.applyEvidence(ctx, domain.PaymentEvidence{NotifyID: "query_" + q.TradeNo + "_" + q.Status, OrderID: o.ID, TradeNo: q.TradeNo, Status: q.Status, AmountCt: amount, FromQuery: true})
}
