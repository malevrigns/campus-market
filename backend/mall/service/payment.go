package service

import (
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"fmt"

	"qingfly/edu-mall/mall/model"
	"qingfly/edu-mall/mall/repository"
)

// PaymentNotify is a (simplified) gateway callback payload.
type PaymentNotify struct {
	OrderNo   string // business order number
	OutTradeNo string
	Status    string // "SUCCESS" | "REFUND" | ...
	AmountCt  int64  // paid amount in cents, from gateway
	Sign      string // hex(HMAC_SHA256(canonical, key))
}

// canonical builds the deterministic string to sign (fixed field order).
func (n *PaymentNotify) canonical() string {
	return n.OutTradeNo + "|" + n.OrderNo + "|" + n.Status + "|" + itoa(n.AmountCt)
}

func itoa(v int64) string { return fmt.Sprintf("%d", v) }

// HandlePaymentNotify processes a payment/refund callback idempotently.
// Invariants enforced:
//  1. verify signature (reject tampering),
//  2. verify amount matches the order (reject under/over-pay),
//  3. state machine transition with optimistic lock (replay is a no-op success).
// Returns processed=true even for replays so the gateway can stop retrying.
func (s *OrderService) HandlePaymentNotify(ctx context.Context, n *PaymentNotify, key []byte) (bool, error) {
	// 1) verify signature.
	mac := hmac.New(sha256.New, key)
	mac.Write([]byte(n.canonical()))
	if hex.EncodeToString(mac.Sum(nil)) != n.Sign {
		return false, ErrBadSign
	}
	o, err := s.orders.GetByNo(ctx, n.OrderNo)
	if err != nil {
		return false, err
	}
	// 2) verify amount.
	if n.Status == "SUCCESS" && o.AmountCt != n.AmountCt {
		return false, ErrAmountMismatch
	}
	switch n.Status {
	case "SUCCESS":
		// already paid/delivered -> idempotent no-op success
		if o.Status == model.OrderPaid || o.Status == model.OrderDelivered {
			return true, nil
		}
		if err := s.orders.UpdateStatus(ctx, o.ID, model.OrderCreated, model.OrderPaid, o.Version); err != nil {
			return false, err
		}
	case "REFUND":
		if o.Status == model.OrderRefunded {
			return true, nil
		}
		from := o.Status
		if from != model.OrderPaid && from != model.OrderDelivered && from != model.OrderRefunding {
			return false, model.ErrInvalidTransition
		}
		if from == model.OrderRefunding {
			_ = s.orders.UpdateStatus(ctx, o.ID, model.OrderRefunding, model.OrderRefunded, o.Version)
			return true, nil
		}
		// enter refunding then refunded (two-step to keep audit trail simple here)
		if err := s.orders.UpdateStatus(ctx, o.ID, from, model.OrderRefunding, o.Version); err != nil {
			return false, err
		}
		fresh, _ := s.orders.GetByNo(ctx, n.OrderNo)
		if fresh == nil {
			return false, repository.ErrNotFound
		}
		if err := s.orders.UpdateStatus(ctx, fresh.ID, model.OrderRefunding, model.OrderRefunded, fresh.Version); err != nil {
			return false, err
		}
	default:
		return false, ErrUnknownNotifyStatus
	}
	return true, nil
}
