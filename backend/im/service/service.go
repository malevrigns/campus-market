// Package service implements the IM message-consistency core:
// server-assigned monotonic seq per conversation, client-msg-id idempotency,
// and cursor-based incremental sync for offline catch-up.
package service

import (
	"context"
	"errors"
	"fmt"
	"sync"
	"time"

	"qingfly/edu-mall/im/model"
)

var (
	ErrNotMember    = errors.New("im: sender not a member of conversation")
	ErrDupClientMsg = errors.New("im: duplicate client_msg_id (idempotent)")
)

// Pusher is where delivered messages are fanned out (WebSocket layer). Interface so
// the IM core does not depend on the connection layer; production wires the Nchan/WS hub.
type Pusher interface {
	Push(ctx context.Context, convID, excludeUserID string, msg *model.Message) error
}

type convState struct {
	conv   *model.Conversation
	msgs   []*model.Message // ordered by seq ascending
	client map[string]string // clientMsgID -> message ID (dedup window)
}

// Service holds in-memory conversation state (offline/test/dev). Production would back
// this with MySQL (messages) + Redis (per-conv seq via INCR + dedup set with TTL).
type Service struct {
	mu     sync.Mutex
	convs  map[string]*convState
	idSeq  int64 // snowflake stand-in for message IDs
	nowFn  func() int64
	pusher Pusher
}

// NewService builds an IM service. pusher may be nil (no fan-out, e.g. tests).
func NewService(pusher Pusher) *Service {
	return &Service{convs: map[string]*convState{}, nowFn: func() int64 { return time.Now().UnixMilli() }, pusher: pusher}
}

// EnsureConversation creates a conversation if absent.
func (s *Service) EnsureConversation(ctx context.Context, id, typ string, members []string) (*model.Conversation, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if st, ok := s.convs[id]; ok {
		cp := *st.conv
		cp.Members = append([]string(nil), st.conv.Members...)
		return &cp, nil
	}
	c := &model.Conversation{ID: id, Type: typ, Members: members}
	s.convs[id] = &convState{conv: c, client: map[string]string{}}
	cp := *c
	return &cp, nil
}

// SendMessage assigns a monotonic seq and stores/pushes the message. Retries with the
// same ClientMsgID within a conversation are idempotent (return existing, no new seq).
func (s *Service) SendMessage(ctx context.Context, sender, convID, clientMsgID string, mtype model.MsgType, content string) (*model.Message, bool, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	st, ok := s.convs[convID]
	if !ok {
		return nil, false, fmt.Errorf("im: conversation %s not found", convID)
	}
	if !isMember(st.conv.Members, sender) {
		return nil, false, ErrNotMember
	}
	// idempotency by clientMsgID
	if clientMsgID != "" {
		if mid, dup := st.client[clientMsgID]; dup {
			for _, msg := range st.msgs {
				if msg.ID == mid {
					cp := *msg
					return &cp, true, ErrDupClientMsg
				}
			}
		}
	}
	st.conv.SeqCursor++
	s.idSeq++
	msg := &model.Message{
		ID:          fmt.Sprintf("m%d", s.idSeq),
		ClientMsgID: clientMsgID,
		ConvID:      convID,
		SenderID:    sender,
		Type:        mtype,
		Content:     content,
		Seq:         st.conv.SeqCursor,
		CreatedAt:   s.nowFn(),
	}
	st.msgs = append(st.msgs, msg)
	if clientMsgID != "" {
		st.client[clientMsgID] = msg.ID
	}
	if s.pusher != nil {
		_ = s.pusher.Push(ctx, convID, sender, msg)
	}
	cp := *msg
	return &cp, false, nil
}

// Sync returns messages with seq > afterSeq (incremental catch-up), capped at limit.
func (s *Service) Sync(ctx context.Context, userID, convID string, afterSeq int64, limit int) ([]*model.Message, int64, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	st, ok := s.convs[convID]
	if !ok {
		return nil, 0, fmt.Errorf("im: conversation %s not found", convID)
	}
	if !isMember(st.conv.Members, userID) {
		return nil, 0, ErrNotMember
	}
	out := make([]*model.Message, 0, limit)
	var lastSeq int64
	for _, msg := range st.msgs {
		if msg.Seq <= afterSeq || msg.RecalledAt > 0 {
			continue
		}
		out = append(out, msg)
		lastSeq = msg.Seq
		if len(out) >= limit {
			break
		}
	}
	return out, lastSeq, nil
}

func isMember(members []string, uid string) bool {
	for _, m := range members {
		if m == uid {
			return true
		}
	}
	return false
}
