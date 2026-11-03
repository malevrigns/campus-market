package mysqlrepo

import (
	"context"
	"database/sql"

	"qingfly/edu-mall/internal/domain"
	"qingfly/edu-mall/internal/infra/mysqlx"
	"qingfly/edu-mall/internal/port"
)

type ChatRepo struct{ *base }

func (s *ChatRepo) FindByProductBuyer(ctx context.Context, productID, buyerID string) (string, error) {
	var id string
	err := s.db.QueryRowContext(ctx, "SELECT id FROM conversations WHERE product_id=? AND buyer_id=?", productID, buyerID).Scan(&id)
	return id, notFound(err, "内容不存在或无权访问")
}

func (s *ChatRepo) Create(ctx context.Context, id, productID, buyerID, sellerID string, at int64) error {
	_, err := s.db.ExecContext(ctx, "INSERT INTO conversations (id,product_id,buyer_id,seller_id,updated_at) VALUES(?,?,?,?,?)", id, productID, buyerID, sellerID, at)
	return translate(err)
}

const conversationColumns = "c.id AS id,c.product_id AS product_id,p.title AS product_title,p.image AS image,peer.id AS peer_id,peer.name AS peer_name,c.seq AS seq,c.seq-COALESCE(rc.seq,0) AS unread,c.last_message AS last_message,c.updated_at AS updated_at"

func (s *ChatRepo) List(ctx context.Context, userID string) ([]domain.Conversation, error) {
	q := "SELECT * FROM ((SELECT " + conversationColumns + " FROM conversations c JOIN products p ON p.id=c.product_id JOIN users peer ON peer.id=c.seller_id LEFT JOIN read_cursors rc ON rc.conversation_id=c.id AND rc.user_id=? WHERE c.buyer_id=? ORDER BY c.updated_at DESC LIMIT 200)" +
		" UNION ALL (SELECT " + conversationColumns + " FROM conversations c JOIN products p ON p.id=c.product_id JOIN users peer ON peer.id=c.buyer_id LEFT JOIN read_cursors rc ON rc.conversation_id=c.id AND rc.user_id=? WHERE c.seller_id=? ORDER BY c.updated_at DESC LIMIT 200)) t ORDER BY updated_at DESC LIMIT 200"
	rows, err := s.db.QueryContext(ctx, q, userID, userID, userID, userID)
	if err != nil {
		return nil, translate(err)
	}
	defer rows.Close()
	items := []domain.Conversation{}
	for rows.Next() {
		var c domain.Conversation
		if err := rows.Scan(&c.ID, &c.ProductID, &c.ProductTitle, &c.Image, &c.PeerID, &c.PeerName, &c.Seq, &c.Unread, &c.LastMessage, &c.UpdatedAt); err != nil {
			return nil, translate(err)
		}
		items = append(items, c)
	}
	return items, translate(rows.Err())
}

func (s *ChatRepo) Seq(ctx context.Context, conversationID, userID string) (int64, error) {
	var seq int64
	err := s.db.QueryRowContext(ctx, "SELECT seq FROM conversations WHERE id=? AND (buyer_id=? OR seller_id=?)", conversationID, userID, userID).Scan(&seq)
	return seq, notFound(err, "内容不存在或无权访问")
}

func (s *ChatRepo) Messages(ctx context.Context, conversationID string, after int64, limit int) ([]domain.Message, error) {
	rows, err := s.db.QueryContext(ctx, "SELECT id,conversation_id,sender_id,client_msg_id,seq,content,created_at FROM messages WHERE conversation_id=? AND seq>? ORDER BY seq LIMIT ?", conversationID, after, limit)
	if err != nil {
		return nil, translate(err)
	}
	defer rows.Close()
	items := []domain.Message{}
	for rows.Next() {
		var m domain.Message
		if err := rows.Scan(&m.ID, &m.ConversationID, &m.SenderID, &m.ClientMsgID, &m.Seq, &m.Content, &m.CreatedAt); err != nil {
			return nil, translate(err)
		}
		items = append(items, m)
	}
	return items, translate(rows.Err())
}

func (s *ChatRepo) MessageByClientID(ctx context.Context, conversationID, senderID, clientMsgID string) (domain.Message, error) {
	var m domain.Message
	err := s.db.QueryRowContext(ctx, "SELECT id,conversation_id,sender_id,client_msg_id,seq,content,created_at FROM messages WHERE conversation_id=? AND sender_id=? AND client_msg_id=?", conversationID, senderID, clientMsgID).Scan(&m.ID, &m.ConversationID, &m.SenderID, &m.ClientMsgID, &m.Seq, &m.Content, &m.CreatedAt)
	return m, notFound(err, "内容不存在或无权访问")
}

func (s *ChatRepo) MarkRead(ctx context.Context, conversationID, userID string, seq int64) error {
	_, err := s.db.ExecContext(ctx, "INSERT INTO read_cursors (conversation_id,user_id,seq) VALUES(?,?,?) ON DUPLICATE KEY UPDATE seq=GREATEST(seq,VALUES(seq))", conversationID, userID, seq)
	return translate(err)
}

func (s *ChatRepo) InTx(ctx context.Context, fn func(port.ChatTx) error) error {
	return translate(mysqlx.WithTx(ctx, s.db, func(tx *sql.Tx) error {
		return fn(&Tx{tx: tx})
	}))
}

// ---------- ChatTx ----------

func (t *Tx) Increment(ctx context.Context, conversationID, userID, preview string, at int64) (string, string, int64, error) {
	res, err := t.tx.ExecContext(ctx, "UPDATE conversations SET seq=seq+1,updated_at=?,last_message=? WHERE id=? AND (buyer_id=? OR seller_id=?)", at, preview, conversationID, userID, userID)
	if err != nil {
		return "", "", 0, err
	}
	if n, _ := res.RowsAffected(); n != 1 {
		return "", "", 0, sql.ErrNoRows
	}
	var buyer, seller string
	var seq int64
	if err = t.tx.QueryRowContext(ctx, "SELECT buyer_id,seller_id,seq FROM conversations WHERE id=?", conversationID).Scan(&buyer, &seller, &seq); err != nil {
		return "", "", 0, err
	}
	return buyer, seller, seq, nil
}

func (t *Tx) InsertMessage(ctx context.Context, m domain.Message) error {
	_, err := t.tx.ExecContext(ctx, "INSERT INTO messages (id,conversation_id,sender_id,client_msg_id,seq,content,created_at) VALUES(?,?,?,?,?,?,?)", m.ID, m.ConversationID, m.SenderID, m.ClientMsgID, m.Seq, m.Content, m.CreatedAt)
	return duplicate(err)
}
