// Package repository defines the data-access (DAO) layer for the mall service.
// The interface is storage-agnostic; a GORM implementation plugs in for MySQL in
// production. An in-memory implementation ships here so the core logic builds and
// tests offline without a database.
package repository

import (
	"context"
	"errors"

	"qingfly/edu-mall/mall/model"
)

// ErrNotFound / ErrConflict are DAO-level sentinels.
var (
	ErrNotFound = errors.New("repo: not found")
	ErrConflict = errors.New("repo: conflict") // e.g. idempotency key reused
)

// OrderStore is the persistence contract for orders.
type OrderStore interface {
	// Create inserts an order; returns ErrConflict if IdempotencyKey already used.
	Create(ctx context.Context, o *model.Order) error
	// GetByID loads an order by primary ID.
	GetByID(ctx context.Context, id string) (*model.Order, error)
	// GetByNo loads an order by business order number (used by payment callbacks).
	GetByNo(ctx context.Context, no string) (*model.Order, error)
	// UpdateStatus applies an optimistic-lock status change (version must match).
	UpdateStatus(ctx context.Context, id string, from, to model.OrderStatus, version int64) error
}

// StockStore handles inventory with conditional decrement to prevent oversell.
type StockStore interface {
	// Decrement atomically decrements stock by n only if enough remains.
	// Returns false when insufficient (caller should not charge).
	Decrement(ctx context.Context, skuID string, n int) (bool, error)
	// Seed sets initial stock for a SKU (used at startup / tests).
	Seed(skuID string, n int) error
	// Restore puts stock back on cancel/refund.
	Restore(ctx context.Context, skuID string, n int) error
}
