package engine

import (
	"context"
	"sync"

	"qingfly/edu-mall/agent/capability"
)

// Executor runs a capability with args, returning an observation string. Interface so
// the engine is decoupled from concrete tool implementations (gRPC/MCP/local).
type Executor interface {
	Execute(ctx context.Context, c capability.Capability, args string) (string, error)
}

// Checkpoint captures resumable state after each iteration (human-in-the-loop / crash recovery).
type Checkpoint struct {
	RunID     string   `json:"run_id"`
	Prompt    string   `json:"prompt"`
	History   []string `json:"history"` // flattened thought/action/observation so far
	Iteration int      `json:"iteration"`
}

// CheckpointStore persists checkpoints keyed by runID (Redis/DB in prod; memory here).
type CheckpointStore interface {
	Save(ctx context.Context, cp *Checkpoint) error
	Load(ctx context.Context, runID string) (*Checkpoint, error)
	Delete(ctx context.Context, runID string) error
}

// MemoryCheckpointStore is a thread-safe in-memory store (offline/test/dev).
type MemoryCheckpointStore struct {
	mu sync.Mutex
	m  map[string]*Checkpoint
}

func NewMemoryCheckpointStore() *MemoryCheckpointStore {
	return &MemoryCheckpointStore{m: map[string]*Checkpoint{}}
}
func (s *MemoryCheckpointStore) Save(_ context.Context, cp *Checkpoint) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	c := *cp
	c.History = append([]string(nil), cp.History...)
	s.m[cp.RunID] = &c
	return nil
}
func (s *MemoryCheckpointStore) Load(_ context.Context, runID string) (*Checkpoint, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	cp, ok := s.m[runID]
	if !ok {
		return nil, ErrNoCheckpoint
	}
	c := *cp
	c.History = append([]string(nil), cp.History...)
	return &c, nil
}
func (s *MemoryCheckpointStore) Delete(_ context.Context, runID string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	delete(s.m, runID)
	return nil
}
