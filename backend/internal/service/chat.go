package service

import (
	"context"
	"strings"
	"time"
	"unicode/utf8"

	"qingfly/edu-mall/internal/domain"
	"qingfly/edu-mall/internal/pkg/idgen"
	"qingfly/edu-mall/internal/port"
)

type ChatService struct {
	store    port.ChatStore
	products port.CatalogStore
	presence port.PresenceStore
	live     port.LiveNotifier
	limiter  port.RateLimiter
}

func NewChat(store port.ChatStore, products port.CatalogStore, presence port.PresenceStore, live port.LiveNotifier, limiter port.RateLimiter) *ChatService {
	return &ChatService{store: store, products: products, presence: presence, live: live, limiter: limiter}
}

func (s *ChatService) CreateConversation(ctx context.Context, user *domain.User, productID string) (domain.ConversationCreated, bool, error) {
	p, err := s.products.Get(ctx, productID)
	if err != nil {
		return domain.ConversationCreated{}, false, err
	}
	if p.SellerID == user.ID {
		return domain.ConversationCreated{}, false, domain.Invalid("这是你自己发布的商品")
	}
	if p.Demo {
		return domain.ConversationCreated{}, false, domain.Conflict("展示商品暂不支持联系卖家")
	}
	existing, err := s.store.FindByProductBuyer(ctx, p.ID, user.ID)
	if err == nil {
		return domain.ConversationCreated{ID: existing}, false, nil
	}
	if domain.KindOf(err) != domain.KindNotFound {
		return domain.ConversationCreated{}, false, err
	}
	conv := idgen.New("c_")
	if err = s.store.Create(ctx, conv, p.ID, user.ID, p.SellerID, now()); err != nil {
		if domain.DuplicateConstraint(err) != "" {
			if existing, queryErr := s.store.FindByProductBuyer(ctx, p.ID, user.ID); queryErr == nil {
				return domain.ConversationCreated{ID: existing}, false, nil
			}
		}
		return domain.ConversationCreated{}, false, err
	}
	return domain.ConversationCreated{ID: conv}, true, nil
}

func (s *ChatService) List(ctx context.Context, user *domain.User) ([]domain.Conversation, error) {
	items, err := s.store.List(ctx, user.ID)
	if err != nil {
		return nil, err
	}
	if len(items) > 0 {
		peers := make([]string, len(items))
		for i, c := range items {
			peers[i] = c.PeerID
		}
		online := s.presence.Online(ctx, peers)
		for i := range items {
			items[i].PeerOnline = online[items[i].PeerID]
		}
	}
	return items, nil
}

func (s *ChatService) Messages(ctx context.Context, user *domain.User, conversationID string, after int64) (domain.MessagePage, error) {
	if after < 0 {
		return domain.MessagePage{}, domain.Invalid("消息游标无效")
	}
	seq, err := s.store.Seq(ctx, conversationID, user.ID)
	if err != nil {
		return domain.MessagePage{}, err
	}
	items, err := s.store.Messages(ctx, conversationID, after, 100)
	if err != nil {
		return domain.MessagePage{}, err
	}
	next := after
	for _, m := range items {
		next = m.Seq
	}
	return domain.MessagePage{Items: items, NextSeq: next, HasMore: next < seq}, nil
}

func preview(content string) string {
	r := []rune(strings.Join(strings.Fields(content), " "))
	if len(r) > 60 {
		r = append(r[:60], '…')
	}
	return string(r)
}

func (s *ChatService) Send(ctx context.Context, user *domain.User, conversationID, clientMsgID, content string) (domain.Message, bool, error) {
	content = strings.TrimSpace(content)
	if !keyValid(clientMsgID) || utf8.RuneCountInString(content) < 1 || utf8.RuneCountInString(content) > 4000 {
		return domain.Message{}, false, domain.Invalid("消息必须为 1–4000 字，并携带有效消息 ID")
	}
	if old, err := s.store.MessageByClientID(ctx, conversationID, user.ID, clientMsgID); err == nil {
		if old.Content != content {
			return domain.Message{}, false, domain.Conflict("消息 ID 已被其他内容使用")
		}
		return old, false, nil
	} else if domain.KindOf(err) != domain.KindNotFound {
		return domain.Message{}, false, err
	}
	if retry, allowed := s.limiter.Window(ctx, "msg:"+user.ID, 120, time.Minute); !allowed {
		return domain.Message{}, false, domain.Retryable(domain.KindTooMany, "消息发送太快，请稍后再试", int(retry.Seconds()+0.999))
	}
	var message domain.Message
	var buyer, seller string
	err := s.store.InTx(ctx, func(tx port.ChatTx) error {
		t := now()
		b, se, seq, err := tx.Increment(ctx, conversationID, user.ID, preview(content), t)
		if err != nil {
			return err
		}
		buyer, seller = b, se
		message = domain.Message{ID: idgen.New("m_"), ConversationID: conversationID, SenderID: user.ID, ClientMsgID: clientMsgID, Seq: seq, Content: content, CreatedAt: t}
		return tx.InsertMessage(ctx, message)
	})
	if err != nil {
		if domain.DuplicateConstraint(err) == "uk_messages_client" {
			if old, queryErr := s.store.MessageByClientID(ctx, conversationID, user.ID, clientMsgID); queryErr == nil {
				if old.Content != content {
					return domain.Message{}, false, domain.Conflict("消息 ID 已被其他内容使用")
				}
				return old, false, nil
			}
		}
		return domain.Message{}, false, err
	}
	s.live.Notify([]string{buyer, seller}, map[string]any{"type": "message", "conversation_id": message.ConversationID, "seq": message.Seq})
	return message, true, nil
}

func (s *ChatService) Read(ctx context.Context, user *domain.User, conversationID string, seq int64) error {
	current, err := s.store.Seq(ctx, conversationID, user.ID)
	if err != nil {
		return err
	}
	if seq < 0 || seq > current {
		return domain.Invalid("已读位置超出会话范围")
	}
	return s.store.MarkRead(ctx, conversationID, user.ID, seq)
}

// TouchPresence 由 WebSocket 心跳调用。
func (s *ChatService) TouchPresence(ctx context.Context, userID string) {
	s.presence.Touch(ctx, userID)
}
