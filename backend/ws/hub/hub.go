// Package hub implements the WebSocket connection layer's in-process core:
// online-state tracking (with TTL) and message fan-out. It is deliberately
// storage-light — production backs online state with Redis (TTL keys) and fans
// out across nodes via Redis Pub/Sub; here an in-memory implementation keeps the
// logic buildable/testable offline while exposing the same gRPC-facing surface.
package hub

import (
	"context"
	"sync"
	"time"
)

// Conn identifies one live connection (a user may have many).
type Conn struct {
	UserID string
	ConnID string // unique per connection
	NodeID string // which ws node owns it
}

// Sink delivers a payload to a single connection (adapter to Nchan/gorilla in prod).
type Sink interface {
	Deliver(ctx context.Context, connID string, payload []byte) error
}

// Hub tracks online state and routes messages. Thread-safe.
type Hub struct {
	mu    sync.Mutex
	conns map[string]map[string]Conn // userID -> connID -> Conn
	ttl   time.Duration
	nowFn func() int64
	sink  Sink
}

// New builds a Hub with the given connection sink and online-state TTL.
func New(sink Sink, ttl time.Duration) *Hub {
	return &Hub{conns: map[string]map[string]Conn{}, ttl: ttl, nowFn: func() int64 { return time.Now().UnixMilli() }, sink: sink}
}

// Register adds a connection as online (idempotent per connID).
func (h *Hub) Register(_ context.Context, c Conn) {
	h.mu.Lock()
	defer h.mu.Unlock()
	if h.conns[c.UserID] == nil {
		h.conns[c.UserID] = map[string]Conn{}
	}
	h.conns[c.UserID][c.ConnID] = c
}

// Unregister removes one connection; drops the user entry when no conns remain.
func (h *Hub) Unregister(_ context.Context, c Conn) {
	h.mu.Lock()
	defer h.mu.Unlock()
	if m, ok := h.conns[c.UserID]; ok {
		delete(m, c.ConnID)
		if len(m) == 0 {
			delete(h.conns, c.UserID)
		}
	}
}

// IsOnline reports whether the user has any live connection.
func (h *Hub) IsOnline(userID string) bool {
	h.mu.Lock()
	defer h.mu.Unlock()
	return len(h.conns[userID]) > 0
}

// OnlineCount returns total live connections and distinct online users.
func (h *Hub) OnlineCount() (conns, users int) {
	h.mu.Lock()
	defer h.mu.Unlock()
	users = len(h.conns)
	for _, m := range h.conns {
		conns += len(m)
	}
	return conns, users
}

// SendToUser fans out to all of a user's connections except excludeConnID.
// Returns how many connections were targeted (online=false if none).
func (h *Hub) SendToUser(ctx context.Context, userID, excludeConnID string, payload []byte) (int, error) {
	h.mu.Lock()
	targets := make([]string, 0)
	if m, ok := h.conns[userID]; ok {
		for id := range m {
			if id != excludeConnID {
				targets = append(targets, id)
			}
		}
	}
	h.mu.Unlock()
	for _, id := range targets {
		if err := h.sink.Deliver(ctx, id, payload); err != nil {
			return len(targets), err
		}
	}
	return len(targets), nil
}
