package platform

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"log/slog"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"sync"
	"time"
	"unicode/utf8"

	"github.com/coder/websocket"
	"github.com/redis/go-redis/v9"

	"qingfly/edu-mall/internal/infra/mysqlx"
)

// LiveHub 管理本实例的 WebSocket 连接，并通过 Redis Pub/Sub 在多实例间转发事件：
// 用户 A 连在实例 1、用户 B 连在实例 2 时，实例 1 上产生的消息通知经 Redis 广播到所有实例，
// 每个实例只投递给自己持有连接的用户。Redis 不可用时退化为只投递本实例。
//
// WebSocket 只推送"有新消息/订单变化"的轻量信号，客户端收到后按 seq 游标拉取，
// 推送丢失也不会丢消息（拉取是权威数据源），这也是 IM 常见的"推拉结合"。
type LiveHub struct {
	mu       sync.Mutex
	clients  map[string]map[chan []byte]bool
	rdb      redis.UniversalClient
	channel  string
	instance string
}

type liveEnvelope struct {
	Origin string          `json:"o"`
	Users  []string        `json:"u"`
	Event  json.RawMessage `json:"e"`
}

func NewLiveHub(rdb redis.UniversalClient, channel, instance string) *LiveHub {
	return &LiveHub{clients: map[string]map[chan []byte]bool{}, rdb: rdb, channel: channel, instance: instance}
}
func (h *LiveHub) subscribe(uid string) (chan []byte, bool) {
	h.mu.Lock()
	defer h.mu.Unlock()
	if len(h.clients[uid]) >= 5 {
		return nil, false
	}
	if h.clients[uid] == nil {
		h.clients[uid] = map[chan []byte]bool{}
	}
	c := make(chan []byte, 32)
	h.clients[uid][c] = true
	return c, true
}
func (h *LiveHub) unsubscribe(uid string, c chan []byte) {
	h.mu.Lock()
	defer h.mu.Unlock()
	if h.clients[uid][c] {
		delete(h.clients[uid], c)
		close(c)
	}
	if len(h.clients[uid]) == 0 {
		delete(h.clients, uid)
	}
}

// Notify 向用户推送事件（跨实例）。
func (h *LiveHub) Notify(users []string, event any) {
	b, e := json.Marshal(event)
	if e != nil {
		return
	}
	h.deliver(users, b)
	if h.rdb == nil {
		return
	}
	env, _ := json.Marshal(liveEnvelope{Origin: h.instance, Users: users, Event: b})
	ctx, cancel := context.WithTimeout(context.Background(), 500*time.Millisecond)
	defer cancel()
	if err := h.rdb.Publish(ctx, h.channel, env).Err(); err != nil {
		slog.Warn("live event publish failed", "error", err)
	}
}

// deliver 投递给本实例的连接。慢消费者（缓冲区满）直接断开，客户端重连后按游标补拉。
func (h *LiveHub) deliver(users []string, b []byte) {
	h.mu.Lock()
	defer h.mu.Unlock()
	for _, u := range users {
		for c := range h.clients[u] {
			select {
			case c <- b:
			default:
				delete(h.clients[u], c)
				close(c)
			}
		}
	}
}

// Listen 订阅其他实例的事件，直到 ctx 结束。
func (h *LiveHub) Listen(ctx context.Context) error {
	sub := h.rdb.Subscribe(ctx, h.channel)
	defer sub.Close()
	ch := sub.Channel()
	for {
		select {
		case <-ctx.Done():
			return nil
		case m, ok := <-ch:
			if !ok {
				return nil
			}
			var env liveEnvelope
			if json.Unmarshal([]byte(m.Payload), &env) != nil || env.Origin == h.instance {
				continue
			}
			h.deliver(env.Users, env.Event)
		}
	}
}

// 在线状态：连接期间每次心跳刷新 online:{uid}（TTL 60s），断开时不立即删除（可能还有其他标签页/实例连接），
// 依靠 TTL 自然过期。
func (s *Server) touchPresence(ctx context.Context, uid string) {
	ctx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 300*time.Millisecond)
	defer cancel()
	_ = s.rdb.Set(ctx, s.keys.Of("online", uid), 1, 60*time.Second).Err()
}

func (s *Server) websocket(w http.ResponseWriter, r *http.Request) {
	u, e := s.currentUser(r)
	if e != nil {
		writeError(w, e)
		return
	}
	ch, ok := s.live.subscribe(u.ID)
	if !ok {
		writeError(w, fail(429, "连接数过多，请关闭其他标签页"))
		return
	}
	defer s.live.unsubscribe(u.ID, ch)
	opts := &websocket.AcceptOptions{}
	if s.cfg.PublicOrigin != "" {
		origin, _ := url.Parse(s.cfg.PublicOrigin)
		opts.OriginPatterns = []string{origin.Host}
	}
	c, e := websocket.Accept(w, r, opts)
	if e != nil {
		return
	}
	defer c.CloseNow()
	c.SetReadLimit(4096)
	ctx := c.CloseRead(r.Context())
	s.touchPresence(ctx, u.ID)
	ticker := time.NewTicker(25 * time.Second)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case b, open := <-ch:
			if !open {
				_ = c.Close(websocket.StatusTryAgainLater, "resync")
				return
			}
			writeCtx, cancel := context.WithTimeout(ctx, 5*time.Second)
			e = c.Write(writeCtx, websocket.MessageText, b)
			cancel()
			if e != nil {
				return
			}
		case <-ticker.C:
			if _, e = s.currentUser(r); e != nil {
				_ = c.Close(websocket.StatusPolicyViolation, "session expired")
				return
			}
			s.touchPresence(ctx, u.ID)
			pingCtx, cancel := context.WithTimeout(ctx, 10*time.Second)
			e = c.Ping(pingCtx)
			cancel()
			if e != nil {
				return
			}
		}
	}
}

type Conversation struct {
	ID           string `json:"id"`
	ProductID    string `json:"product_id"`
	ProductTitle string `json:"product_title"`
	Image        string `json:"image"`
	PeerID       string `json:"peer_id"`
	PeerName     string `json:"peer_name"`
	PeerOnline   bool   `json:"peer_online"`
	Seq          int64  `json:"seq"`
	Unread       int64  `json:"unread"`
	LastMessage  string `json:"last_message"`
	UpdatedAt    int64  `json:"updated_at"`
}
type Message struct {
	ID             string `json:"id"`
	ConversationID string `json:"conversation_id"`
	SenderID       string `json:"sender_id"`
	ClientMsgID    string `json:"client_msg_id"`
	Seq            int64  `json:"seq"`
	Content        string `json:"content"`
	CreatedAt      int64  `json:"created_at"`
}

const messageSelect = "SELECT id,conversation_id,sender_id,client_msg_id,seq,content,created_at FROM messages"

func scanMessage(row scanner) (Message, error) {
	var m Message
	e := row.Scan(&m.ID, &m.ConversationID, &m.SenderID, &m.ClientMsgID, &m.Seq, &m.Content, &m.CreatedAt)
	return m, e
}
func (s *Server) createConversation(w http.ResponseWriter, r *http.Request, u *User) error {
	var v struct {
		ProductID string `json:"product_id"`
	}
	if e := decode(r, &v); e != nil {
		return e
	}
	p, e := s.loadProduct(r.Context(), v.ProductID)
	if e != nil {
		return e
	}
	if p.SellerID == u.ID {
		return fail(400, "这是你自己发布的商品")
	}
	if p.Demo {
		return fail(409, "展示商品暂不支持联系卖家")
	}
	var existing string
	e = s.db.QueryRowContext(r.Context(), "SELECT id FROM conversations WHERE product_id=? AND buyer_id=?", p.ID, u.ID).Scan(&existing)
	if e == nil {
		return result(w, 200, map[string]string{"id": existing})
	}
	if !errors.Is(e, sql.ErrNoRows) {
		return e
	}
	conv := id("c_")
	_, e = s.db.ExecContext(r.Context(), "INSERT INTO conversations (id,product_id,buyer_id,seller_id,updated_at) VALUES(?,?,?,?,?)", conv, p.ID, u.ID, p.SellerID, now())
	if mysqlx.IsDuplicate(e) {
		// 并发创建同一会话：唯一键 (product_id, buyer_id) 兜底，返回已存在的会话。
		if err := s.db.QueryRowContext(r.Context(), "SELECT id FROM conversations WHERE product_id=? AND buyer_id=?", p.ID, u.ID).Scan(&existing); err == nil {
			return result(w, 200, map[string]string{"id": existing})
		}
	}
	if e != nil {
		return e
	}
	return result(w, 201, map[string]string{"id": conv})
}

const conversationColumns = "c.id,c.product_id,p.title,p.image,peer.id,peer.name,c.seq,c.seq-COALESCE(rc.seq,0),c.last_message,c.updated_at"

func (s *Server) conversations(w http.ResponseWriter, r *http.Request, u *User) error {
	// 同订单列表：OR 条件拆成两个走各自索引的子查询 UNION ALL。
	q := "SELECT * FROM ((SELECT " + conversationColumns + " FROM conversations c JOIN products p ON p.id=c.product_id JOIN users peer ON peer.id=c.seller_id LEFT JOIN read_cursors rc ON rc.conversation_id=c.id AND rc.user_id=? WHERE c.buyer_id=? ORDER BY c.updated_at DESC LIMIT 200)" +
		" UNION ALL (SELECT " + conversationColumns + " FROM conversations c JOIN products p ON p.id=c.product_id JOIN users peer ON peer.id=c.buyer_id LEFT JOIN read_cursors rc ON rc.conversation_id=c.id AND rc.user_id=? WHERE c.seller_id=? ORDER BY c.updated_at DESC LIMIT 200)) t ORDER BY updated_at DESC LIMIT 200"
	rows, e := s.db.QueryContext(r.Context(), q, u.ID, u.ID, u.ID, u.ID)
	if e != nil {
		return e
	}
	defer rows.Close()
	items := []Conversation{}
	for rows.Next() {
		var c Conversation
		if e = rows.Scan(&c.ID, &c.ProductID, &c.ProductTitle, &c.Image, &c.PeerID, &c.PeerName, &c.Seq, &c.Unread, &c.LastMessage, &c.UpdatedAt); e != nil {
			return e
		}
		items = append(items, c)
	}
	if e = rows.Err(); e != nil {
		return e
	}
	if len(items) > 0 {
		keys := make([]string, len(items))
		for i, c := range items {
			keys[i] = s.keys.Of("online", c.PeerID)
		}
		if vals, err := s.rdb.MGet(r.Context(), keys...).Result(); err == nil {
			for i, v := range vals {
				items[i].PeerOnline = v != nil
			}
		}
	}
	return result(w, 200, map[string]any{"items": items})
}
func (s *Server) messages(w http.ResponseWriter, r *http.Request, u *User) error {
	after := int64(0)
	var e error
	if v := r.URL.Query().Get("after"); v != "" {
		after, e = strconv.ParseInt(v, 10, 64)
		if e != nil || after < 0 {
			return fail(400, "消息游标无效")
		}
	}
	var seq int64
	e = s.db.QueryRowContext(r.Context(), "SELECT seq FROM conversations WHERE id=? AND (buyer_id=? OR seller_id=?)", r.PathValue("id"), u.ID, u.ID).Scan(&seq)
	if e != nil {
		return e
	}
	rows, e := s.db.QueryContext(r.Context(), messageSelect+" WHERE conversation_id=? AND seq>? ORDER BY seq LIMIT 100", r.PathValue("id"), after)
	if e != nil {
		return e
	}
	defer rows.Close()
	items := []Message{}
	next := after
	for rows.Next() {
		m, e := scanMessage(rows)
		if e != nil {
			return e
		}
		items = append(items, m)
		next = m.Seq
	}
	if e = rows.Err(); e != nil {
		return e
	}
	return result(w, 200, map[string]any{"items": items, "next_seq": next, "has_more": next < seq})
}

func preview(content string) string {
	r := []rune(strings.Join(strings.Fields(content), " "))
	if len(r) > 60 {
		r = append(r[:60], '…')
	}
	return string(r)
}

// sendMessage 分配会话内单调递增的 seq：
// 先 UPDATE conversations SET seq=seq+1（拿到会话行排他锁），再在同一事务内读出新 seq 写入消息。
// 同一会话的并发发送在行锁上排队，seq 不重复不跳号；不同会话互不影响。
// 客户端消息 ID (conversation, sender, client_msg_id) 唯一，实现发送端重试幂等。
func (s *Server) sendMessage(w http.ResponseWriter, r *http.Request, u *User) error {
	var v struct {
		ClientMsgID string `json:"client_msg_id"`
		Content     string `json:"content"`
	}
	if e := decode(r, &v); e != nil {
		return e
	}
	v.Content = strings.TrimSpace(v.Content)
	if !keyValid(v.ClientMsgID) || utf8.RuneCountInString(v.Content) < 1 || utf8.RuneCountInString(v.Content) > 4000 {
		return fail(400, "消息必须为 1–4000 字，并携带有效消息 ID")
	}
	cid := r.PathValue("id")
	ctx := r.Context()
	duplicate := func() (Message, bool, error) {
		old, e := scanMessage(s.db.QueryRowContext(ctx, messageSelect+" WHERE conversation_id=? AND sender_id=? AND client_msg_id=?", cid, u.ID, v.ClientMsgID))
		if errors.Is(e, sql.ErrNoRows) {
			return old, false, nil
		}
		if e != nil {
			return old, false, e
		}
		if old.Content != v.Content {
			return old, true, fail(409, "消息 ID 已被其他内容使用")
		}
		return old, true, nil
	}
	if old, found, e := duplicate(); e != nil {
		return e
	} else if found {
		return result(w, 200, old)
	}
	if !s.allow(r, "msg:"+u.ID, 120, time.Minute) {
		return fail(429, "消息发送太快，请稍后再试")
	}
	var m Message
	var buyer, seller string
	e := mysqlx.WithTx(ctx, s.db, func(tx *sql.Tx) error {
		t := now()
		res, e := tx.ExecContext(ctx, "UPDATE conversations SET seq=seq+1,updated_at=?,last_message=? WHERE id=? AND (buyer_id=? OR seller_id=?)", t, preview(v.Content), cid, u.ID, u.ID)
		if e != nil {
			return e
		}
		if n, _ := res.RowsAffected(); n != 1 {
			return sql.ErrNoRows
		}
		var seq int64
		if e = tx.QueryRowContext(ctx, "SELECT buyer_id,seller_id,seq FROM conversations WHERE id=?", cid).Scan(&buyer, &seller, &seq); e != nil {
			return e
		}
		m = Message{ID: id("m_"), ConversationID: cid, SenderID: u.ID, ClientMsgID: v.ClientMsgID, Seq: seq, Content: v.Content, CreatedAt: t}
		_, e = tx.ExecContext(ctx, "INSERT INTO messages (id,conversation_id,sender_id,client_msg_id,seq,content,created_at) VALUES(?,?,?,?,?,?,?)", m.ID, m.ConversationID, m.SenderID, m.ClientMsgID, m.Seq, m.Content, m.CreatedAt)
		return e
	})
	if mysqlx.IsDuplicate(e) && mysqlx.DuplicateKeyName(e) == "uk_messages_client" {
		if old, found, err := duplicate(); err != nil {
			return err
		} else if found {
			return result(w, 200, old)
		}
	}
	if e != nil {
		return e
	}
	s.live.Notify([]string{buyer, seller}, map[string]any{"type": "message", "conversation_id": m.ConversationID, "seq": m.Seq})
	return result(w, 201, m)
}
func (s *Server) readMessages(w http.ResponseWriter, r *http.Request, u *User) error {
	var v struct {
		Seq int64 `json:"seq"`
	}
	if e := decode(r, &v); e != nil {
		return e
	}
	var seq int64
	if e := s.db.QueryRowContext(r.Context(), "SELECT seq FROM conversations WHERE id=? AND (buyer_id=? OR seller_id=?)", r.PathValue("id"), u.ID, u.ID).Scan(&seq); e != nil {
		return e
	}
	if v.Seq < 0 || v.Seq > seq {
		return fail(400, "已读位置超出会话范围")
	}
	// 已读水位只前进不后退：并发/乱序的已读上报不会把水位改小。
	_, e := s.db.ExecContext(r.Context(), "INSERT INTO read_cursors (conversation_id,user_id,seq) VALUES(?,?,?) ON DUPLICATE KEY UPDATE seq=GREATEST(seq,VALUES(seq))", r.PathValue("id"), u.ID, v.Seq)
	if e != nil {
		return e
	}
	return result(w, 200, map[string]bool{"read": true})
}
