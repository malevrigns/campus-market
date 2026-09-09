package repository

import (
	"context"
	"sync"

	"qingfly/edu-mall/mall/model"
)

// MemoryOrderStore is a thread-safe in-memory OrderStore (offline/test/dev).
type MemoryOrderStore struct {
	mu      sync.Mutex
	orders  map[string]*model.Order
	idemKey map[string]string // idempotencyKey -> orderID
}

// NewMemoryOrderStore builds an empty store.
func NewMemoryOrderStore() *MemoryOrderStore {
	return &MemoryOrderStore{orders: map[string]*model.Order{}, idemKey: map[string]string{}}
}

func (s *MemoryOrderStore) Create(_ context.Context, o *model.Order) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if o.IdempotencyKey != "" {
		if _, exists := s.idemKey[o.IdempotencyKey]; exists {
			return ErrConflict
		}
		s.idemKey[o.IdempotencyKey] = o.ID
	}
	cp := *o
	o = &cp
	if o.Version == 0 {
		o.Version = 1
	}
	s.orders[o.ID] = o
	return nil
}

func (s *MemoryOrderStore) GetByID(_ context.Context, id string) (*model.Order, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	o, ok := s.orders[id]
	if !ok {
		return nil, ErrNotFound
	}
	cp := *o
	return &cp, nil
}

func (s *MemoryOrderStore) GetByNo(_ context.Context, no string) (*model.Order, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	for _, o := range s.orders {
		if o.No == no {
			cp := *o
			return &cp, nil
		}
	}
	return nil, ErrNotFound
}

func (s *MemoryOrderStore) UpdateStatus(_ context.Context, id string, from, to model.OrderStatus, version int64) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	o, ok := s.orders[id]
	if !ok {
		return ErrNotFound
	}
	if o.Status != from || o.Version != version {
		return ErrConflict
	}
	if !model.CanTransition(from, to) {
		return model.ErrInvalidTransition
	}
	o.Status = to
	o.Version++
	return nil
}

// MemoryStockStore is an in-memory StockStore with atomic conditional decrement.
type MemoryStockStore struct {
	mu    sync.Mutex
	stock map[string]int64
}

// NewMemoryStockStore builds a stock store seeded with the given levels.
func NewMemoryStockStore(seed map[string]int64) *MemoryStockStore {
	m := map[string]int64{}
	for k, v := range seed {
		m[k] = v
	}
	return &MemoryStockStore{stock: m}
}


func (s *MemoryStockStore) Seed(skuID string, n int) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.stock[skuID] = int64(n)
	return nil
}
func (s *MemoryStockStore) Decrement(_ context.Context, skuID string, n int) (bool, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	cur, ok := s.stock[skuID]
	if !ok || cur < int64(n) {
		return false, nil // insufficient: caller must not charge
	}
	s.stock[skuID] = cur - int64(n)
	return true, nil
}

func (s *MemoryStockStore) Restore(_ context.Context, skuID string, n int) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.stock[skuID] += int64(n)
	return nil
}
