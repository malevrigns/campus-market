package platform

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"github.com/coder/websocket"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"sync"
	"time"
	"unicode/utf8"
)

type LiveHub struct {
	mu      sync.Mutex
	clients map[string]map[chan []byte]bool
}

func NewLiveHub() *LiveHub { return &LiveHub{clients: map[string]map[chan []byte]bool{}} }
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
func (h *LiveHub) Notify(users []string, event any) {
	b, e := json.Marshal(event)
	if e != nil {
		return
	}
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
	tx, e := s.db.BeginTx(r.Context(), nil)
	if e != nil {
		return e
	}
	defer tx.Rollback()
	p, e := scanProduct(tx.QueryRow(productSelect+" WHERE p.id=?", v.ProductID))
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
	e = tx.QueryRow("SELECT id FROM conversations WHERE product_id=? AND buyer_id=?", p.ID, u.ID).Scan(&existing)
	if e == nil {
		_ = tx.Rollback()
		return result(w, 200, map[string]string{"id": existing})
	}
	if !errors.Is(e, sql.ErrNoRows) {
		return e
	}
	conv := id("c_")
	if _, e = tx.Exec("INSERT INTO conversations(id,product_id,buyer_id,seller_id,updated_at) VALUES(?,?,?,?,?)", conv, p.ID, u.ID, p.SellerID, now()); e != nil {
		return e
	}
	if e = tx.Commit(); e != nil {
		return e
	}
	return result(w, 201, map[string]string{"id": conv})
}
func (s *Server) conversations(w http.ResponseWriter, r *http.Request, u *User) error {
	rows, e := s.db.QueryContext(r.Context(), `SELECT c.id,c.product_id,p.title,p.image,peer.id,peer.name,c.seq,c.seq-COALESCE(rc.seq,0),COALESCE((SELECT content FROM messages WHERE conversation_id=c.id ORDER BY seq DESC LIMIT 1),''),c.updated_at FROM conversations c JOIN products p ON p.id=c.product_id JOIN users peer ON peer.id=CASE WHEN c.buyer_id=? THEN c.seller_id ELSE c.buyer_id END LEFT JOIN read_cursors rc ON rc.conversation_id=c.id AND rc.user_id=? WHERE c.buyer_id=? OR c.seller_id=? ORDER BY c.updated_at DESC LIMIT 200`, u.ID, u.ID, u.ID, u.ID)
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
	rows, e := s.db.QueryContext(r.Context(), "SELECT id,conversation_id,sender_id,client_msg_id,seq,content,created_at FROM messages WHERE conversation_id=? AND seq>? ORDER BY seq LIMIT 100", r.PathValue("id"), after)
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
	if !s.limits.allow("msg:"+u.ID, 120) {
		return fail(429, "消息发送太快，请稍后再试")
	}
	tx, e := s.db.BeginTx(r.Context(), nil)
	if e != nil {
		return e
	}
	defer tx.Rollback()
	var buyer, seller string
	var seq int64
	e = tx.QueryRow("SELECT buyer_id,seller_id,seq FROM conversations WHERE id=? AND (buyer_id=? OR seller_id=?)", r.PathValue("id"), u.ID, u.ID).Scan(&buyer, &seller, &seq)
	if e != nil {
		return e
	}
	old, e := scanMessage(tx.QueryRow("SELECT id,conversation_id,sender_id,client_msg_id,seq,content,created_at FROM messages WHERE conversation_id=? AND sender_id=? AND client_msg_id=?", r.PathValue("id"), u.ID, v.ClientMsgID))
	if e == nil {
		if old.Content != v.Content {
			return fail(409, "消息 ID 已被其他内容使用")
		}
		_ = tx.Rollback()
		return result(w, 200, old)
	}
	if !errors.Is(e, sql.ErrNoRows) {
		return e
	}
	m := Message{ID: id("m_"), ConversationID: r.PathValue("id"), SenderID: u.ID, ClientMsgID: v.ClientMsgID, Seq: seq + 1, Content: v.Content, CreatedAt: now()}
	if _, e = tx.Exec("INSERT INTO messages VALUES(?,?,?,?,?,?,?)", m.ID, m.ConversationID, m.SenderID, m.ClientMsgID, m.Seq, m.Content, m.CreatedAt); e != nil {
		return e
	}
	if _, e = tx.Exec("UPDATE conversations SET seq=?,updated_at=? WHERE id=?", m.Seq, m.CreatedAt, m.ConversationID); e != nil {
		return e
	}
	if e = tx.Commit(); e != nil {
		return e
	}
	s.live.Notify([]string{buyer, seller}, map[string]any{"type": "message", "conversation_id": m.ConversationID})
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
	_, e := s.db.ExecContext(r.Context(), "INSERT INTO read_cursors VALUES(?,?,?) ON CONFLICT(conversation_id,user_id) DO UPDATE SET seq=MAX(seq,excluded.seq)", r.PathValue("id"), u.ID, v.Seq)
	if e != nil {
		return e
	}
	return result(w, 200, map[string]bool{"read": true})
}
