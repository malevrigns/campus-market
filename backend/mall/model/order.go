// Package model defines mall domain models and the order state machine.
package model

import "errors"

// OrderStatus is the lifecycle state of an order.
type OrderStatus int

const (
	OrderCreated OrderStatus = iota // 已创建未支付
	OrderPaid                       // 已支付待交付
	OrderDelivered                  // 权益已交付（可学习）
	OrderClosed                     // 超时/主动取消关闭
	OrderRefunding                  // 退款中
	OrderRefunded                   // 已退款完成
)

func (s OrderStatus) String() string {
	switch s {
	case OrderCreated:
		return "created"
	case OrderPaid:
		return "paid"
	case OrderDelivered:
		return "delivered"
	case OrderClosed:
		return "closed"
	case OrderRefunding:
		return "refunding"
	case OrderRefunded:
		return "refunded"
	default:
		return "unknown"
	}
}

// transitions is the allowed state machine graph (no reverse jumps).
var transitions = map[OrderStatus]map[OrderStatus]bool{
	OrderCreated:   {OrderPaid: true, OrderClosed: true},
	OrderPaid:      {OrderDelivered: true, OrderRefunding: true},
	OrderDelivered: {OrderRefunding: true},
	OrderRefunding: {OrderRefunded: true, OrderDelivered: true}, // 退款失败回滚
}

// ErrInvalidTransition is returned for a disallowed state jump.
var ErrInvalidTransition = errors.New("order: invalid state transition")

// CanTransition reports whether moving from s to t is legal.
func CanTransition(s, t OrderStatus) bool {
	return transitions[s][t]
}

// Item is one line in an order (a course SKU).
type Item struct {
	SKUID   string `json:"sku_id"`
	Name    string `json:"name"`
	PriceCt int64  `json:"price_ct"` // 单位：分，避免浮点
	Qty     int    `json:"qty"`
}

// Order is the aggregate root. Amounts are always in cents (int64), never float.
type Order struct {
	ID         string        `json:"id"`
	No         string        `json:"no"` // business order number (snowflake)
	UserID     string        `json:"user_id"`
	Status     OrderStatus   `json:"status"`
	Items      []Item        `json:"items"`
	AmountCt   int64         `json:"amount_ct"`
	PaidAt     int64         `json:"paid_at,omitempty"`
	IdempotencyKey string    `json:"-"` // dedup key, not serialized
	Version    int64         `json:"version"` // optimistic lock
}
