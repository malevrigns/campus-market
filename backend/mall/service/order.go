// Package service contains the mall business logic (the "service" layer).
// It orchestrates repositories and enforces invariants: oversell prevention,
// payment-callback idempotency (verify sign AND amount), and the order state machine.
package service

import (
	"context"
	"errors"
	"fmt"

	"qingfly/edu-mall/internal/pkg/snowflake"
	"qingfly/edu-mall/mall/model"
	"qingfly/edu-mall/mall/repository"
)

// Errors surfaced to handlers (mapped to response codes).
var (
	ErrInsufficientStock   = errors.New("order: insufficient stock")
	ErrBadSign             = errors.New("payment: bad signature")
	ErrAmountMismatch      = errors.New("payment: amount mismatch")
	ErrAlreadyProcessed    = errors.New("payment: already processed (idempotent)")
	ErrUnknownNotifyStatus = errors.New("payment: unknown notify status")
)

// dec records a successful stock decrement for compensation on later failure.
type dec struct {
	sku string
	n   int
}

// OrderService implements order lifecycle operations.
type OrderService struct {
	orders repository.OrderStore
	stock  repository.StockStore
	idgen  *snowflake.Node
}

// NewOrderService wires dependencies (constructor injection).
func NewOrderService(orders repository.OrderStore, stock repository.StockStore, idgen *snowflake.Node) *OrderService {
	return &OrderService{orders: orders, stock: stock, idgen: idgen}
}

// CreateOrderRequest is the input to place an order. Prices come from a server-side
// SKU catalog, never from the client (avoids client-price tampering).
type CreateOrderRequest struct {
	UserID           string
	Items            []model.Item
	IdempotencyKey   string // e.g. a client-generated UUID; dedups retries
	ServerSidePrices map[string]int64 // skuID -> unit price in cents (authoritative)
}

func (s *OrderService) GetByNo(ctx context.Context, no string) (*model.Order, error) {
	return s.orders.GetByNo(ctx, no)
}

// CreateOrder places an order with conditional stock decrement (no oversell).
// Flow: validate prices -> decrement all stock -> persist (idempotent by key).
// If any step fails after decrements, already-decremented stock is restored.
func (s *OrderService) CreateOrder(ctx context.Context, req *CreateOrderRequest) (*model.Order, error) {
	if req.UserID == "" {
		return nil, errors.New("order: user required")
	}
	var total int64
	for _, it := range req.Items {
		price, ok := req.ServerSidePrices[it.SKUID]
		if !ok {
			return nil, fmt.Errorf("order: unknown sku %s", it.SKUID)
		}
		total += price * int64(it.Qty)
	}
	// 1) conditionally decrement every line; collect for compensation on failure.
	done := make([]dec, 0, len(req.Items))
	for _, it := range req.Items {
		ok, err := s.stock.Decrement(ctx, it.SKUID, it.Qty)
		if err != nil || !ok {
			s.compensate(ctx, done)
			if err != nil {
				return nil, err
			}
			return nil, ErrInsufficientStock
		}
		done = append(done, dec{sku: it.SKUID, n: it.Qty})
	}
	// 2) build & persist order (idempotent by key).
	id64, err := s.idgen.Next()
	if err != nil {
		s.compensate(ctx, done)
		return nil, err
	}
	o := &model.Order{
		ID:             fmt.Sprintf("%d", id64),
		No:             fmt.Sprintf("NO%d", id64),
		UserID:         req.UserID,
		Status:         model.OrderCreated,
		Items:          req.Items,
		AmountCt:       total,
		IdempotencyKey: req.IdempotencyKey,
	}
	if err := s.orders.Create(ctx, o); err != nil {
		s.compensate(ctx, done)
		return nil, err
	}
	return o, nil
}

// compensate restores previously-decremented stock when a later step fails.
func (s *OrderService) compensate(ctx context.Context, done []dec) {
	for _, d := range done {
		_ = s.stock.Restore(ctx, d.sku, d.n)
	}
}
