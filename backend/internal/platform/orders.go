package platform

import (
	"database/sql"
	"errors"
	"net/http"
	"slices"
	"strings"
	"time"
)

type Order struct {
	ID        string       `json:"id"`
	BuyerID   string       `json:"buyer_id"`
	SellerID  string       `json:"seller_id"`
	ProductID string       `json:"product_id"`
	Title     string       `json:"title"`
	Image     string       `json:"image"`
	AmountCt  int64        `json:"amount_ct"`
	Campus    string       `json:"campus"`
	Status    string       `json:"status"`
	CreatedAt int64        `json:"created_at"`
	UpdatedAt int64        `json:"updated_at"`
	ExpiresAt int64        `json:"expires_at"`
	Events    []OrderEvent `json:"events,omitempty"`
}
type OrderEvent struct {
	Status    string `json:"status"`
	CreatedAt int64  `json:"created_at"`
}

const orderSelect = "SELECT id,buyer_id,seller_id,product_id,title,image,amount_ct,campus,status,created_at,updated_at,expires_at FROM orders"

func scanOrder(row scanner) (Order, error) {
	var o Order
	e := row.Scan(&o.ID, &o.BuyerID, &o.SellerID, &o.ProductID, &o.Title, &o.Image, &o.AmountCt, &o.Campus, &o.Status, &o.CreatedAt, &o.UpdatedAt, &o.ExpiresAt)
	return o, e
}
func keyValid(v string) bool { return len(v) >= 8 && len(v) <= 128 && strings.TrimSpace(v) == v }
func addEvent(tx *sql.Tx, oid, status, actor string) error {
	_, e := tx.Exec("INSERT INTO order_events(order_id,status,actor,created_at) VALUES(?,?,?,?)", oid, status, actor, now())
	return e
}
func (s *Server) createOrder(w http.ResponseWriter, r *http.Request, u *User) error {
	var v struct {
		ProductID string `json:"product_id"`
		Campus    string `json:"campus"`
		Idem      string `json:"idempotency_key"`
	}
	if e := decode(r, &v); e != nil {
		return e
	}
	if !keyValid(v.Idem) || !slices.Contains(campuses, v.Campus) {
		return fail(400, "请指定面交校区和有效幂等键")
	}
	tx, e := s.db.BeginTx(r.Context(), nil)
	if e != nil {
		return e
	}
	defer tx.Rollback()
	old, e := scanOrder(tx.QueryRow(orderSelect+" WHERE buyer_id=? AND idem=?", u.ID, v.Idem))
	if e == nil {
		if old.ProductID != v.ProductID || old.Campus != v.Campus {
			return fail(409, "同一幂等键不能用于不同订单")
		}
		_ = tx.Rollback()
		return result(w, 200, old)
	}
	if !errors.Is(e, sql.ErrNoRows) {
		return e
	}
	p, e := scanProduct(tx.QueryRow(productSelect+" WHERE p.id=?", v.ProductID))
	if e != nil {
		return e
	}
	if p.SellerID == u.ID {
		return fail(400, "不能购买自己的商品")
	}
	if p.Demo {
		return fail(409, "这是一件展示商品，暂不支持交易")
	}
	if p.Status != "available" {
		return fail(409, "商品已被预订或售出")
	}
	res, e := tx.Exec("UPDATE products SET status='reserved' WHERE id=? AND status='available'", p.ID)
	if e != nil {
		return e
	}
	n, _ := res.RowsAffected()
	if n != 1 {
		return fail(409, "商品已被其他同学预订")
	}
	o := Order{ID: id("CM"), BuyerID: u.ID, SellerID: p.SellerID, ProductID: p.ID, Title: p.Title, Image: p.Image, AmountCt: p.PriceCt, Campus: v.Campus, Status: "pending", CreatedAt: now(), UpdatedAt: now(), ExpiresAt: time.Now().Add(15 * time.Minute).UnixMilli()}
	_, e = tx.Exec("INSERT INTO orders VALUES(?,?,?,?,?,?,?,?,?,?,?,?,?)", o.ID, o.BuyerID, o.SellerID, o.ProductID, o.Title, o.Image, o.AmountCt, o.Campus, o.Status, v.Idem, o.CreatedAt, o.UpdatedAt, o.ExpiresAt)
	if e != nil {
		return e
	}
	if e = addEvent(tx, o.ID, "pending", u.ID); e != nil {
		return e
	}
	if e = tx.Commit(); e != nil {
		return e
	}
	return result(w, 201, o)
}
func (s *Server) orders(w http.ResponseWriter, r *http.Request, u *User) error {
	rows, e := s.db.QueryContext(r.Context(), orderSelect+" WHERE buyer_id=? OR seller_id=? ORDER BY created_at DESC LIMIT 200", u.ID, u.ID)
	if e != nil {
		return e
	}
	defer rows.Close()
	items := []Order{}
	for rows.Next() {
		o, e := scanOrder(rows)
		if e != nil {
			return e
		}
		items = append(items, o)
	}
	if e = rows.Err(); e != nil {
		return e
	}
	return result(w, 200, map[string]any{"items": items})
}
func (s *Server) ownedOrder(r *http.Request, u *User) (Order, error) {
	return scanOrder(s.db.QueryRowContext(r.Context(), orderSelect+" WHERE id=? AND (buyer_id=? OR seller_id=?)", r.PathValue("id"), u.ID, u.ID))
}
func (s *Server) getOrder(w http.ResponseWriter, r *http.Request, u *User) error {
	o, e := s.ownedOrder(r, u)
	if e != nil {
		return e
	}
	rows, e := s.db.QueryContext(r.Context(), "SELECT status,created_at FROM order_events WHERE order_id=? ORDER BY id", o.ID)
	if e != nil {
		return e
	}
	defer rows.Close()
	o.Events = []OrderEvent{}
	for rows.Next() {
		var ev OrderEvent
		if e = rows.Scan(&ev.Status, &ev.CreatedAt); e != nil {
			return e
		}
		o.Events = append(o.Events, ev)
	}
	if e = rows.Err(); e != nil {
		return e
	}
	return result(w, 200, o)
}
func (s *Server) cancelOrder(w http.ResponseWriter, r *http.Request, u *User) error {
	tx, e := s.db.BeginTx(r.Context(), nil)
	if e != nil {
		return e
	}
	defer tx.Rollback()
	o, e := scanOrder(tx.QueryRow(orderSelect+" WHERE id=? AND buyer_id=?", r.PathValue("id"), u.ID))
	if e != nil {
		return e
	}
	if o.Status == "cancelled" {
		_ = tx.Rollback()
		return result(w, 200, o)
	}
	if o.Status != "pending" {
		return fail(409, "当前状态不能取消")
	}
	var sessions int
	if e = tx.QueryRow("SELECT COUNT(*) FROM payments WHERE order_id=?", o.ID).Scan(&sessions); e != nil {
		return e
	}
	if sessions > 0 {
		return fail(409, "已打开支付通道，请等待通道关单确认；库存仍保留，避免重复交易")
	}
	if _, e = tx.Exec("UPDATE orders SET status='cancelled',updated_at=? WHERE id=?", now(), o.ID); e != nil {
		return e
	}
	if _, e = tx.Exec("UPDATE products SET status='available' WHERE id=? AND status='reserved'", o.ProductID); e != nil {
		return e
	}
	if e = addEvent(tx, o.ID, "cancelled", u.ID); e != nil {
		return e
	}
	if e = tx.Commit(); e != nil {
		return e
	}
	o.Status = "cancelled"
	return result(w, 200, o)
}
func (s *Server) transition(w http.ResponseWriter, r *http.Request, u *User, from, to, role string) error {
	tx, e := s.db.BeginTx(r.Context(), nil)
	if e != nil {
		return e
	}
	defer tx.Rollback()
	o, e := scanOrder(tx.QueryRow(orderSelect+" WHERE id=? AND "+role+"=?", r.PathValue("id"), u.ID))
	if e != nil {
		return e
	}
	if o.Status == to {
		_ = tx.Rollback()
		return result(w, 200, o)
	}
	if o.Status != from {
		return fail(409, "订单状态已变化，请刷新后重试")
	}
	if _, e = tx.Exec("UPDATE orders SET status=?,updated_at=? WHERE id=?", to, now(), o.ID); e != nil {
		return e
	}
	if e = addEvent(tx, o.ID, to, u.ID); e != nil {
		return e
	}
	if e = tx.Commit(); e != nil {
		return e
	}
	o.Status = to
	s.live.Notify([]string{o.BuyerID, o.SellerID}, map[string]any{"type": "order", "order_id": o.ID})
	return result(w, 200, o)
}
func (s *Server) handover(w http.ResponseWriter, r *http.Request, u *User) error {
	return s.transition(w, r, u, "paid", "handover", "seller_id")
}
func (s *Server) complete(w http.ResponseWriter, r *http.Request, u *User) error {
	return s.transition(w, r, u, "handover", "completed", "buyer_id")
}
func (s *Server) refund(w http.ResponseWriter, r *http.Request, u *User) error {
	var v struct {
		Reason string `json:"reason"`
	}
	if e := decode(r, &v); e != nil {
		return e
	}
	v.Reason = strings.TrimSpace(v.Reason)
	if len(v.Reason) < 6 || len(v.Reason) > 1500 {
		return fail(400, "请填写退款原因（2–500 字）")
	}
	tx, e := s.db.BeginTx(r.Context(), nil)
	if e != nil {
		return e
	}
	defer tx.Rollback()
	o, e := scanOrder(tx.QueryRow(orderSelect+" WHERE id=? AND buyer_id=?", r.PathValue("id"), u.ID))
	if e != nil {
		return e
	}
	if o.Status == "refund_requested" {
		_ = tx.Rollback()
		return result(w, 200, o)
	}
	if o.Status != "paid" && o.Status != "handover" && o.Status != "completed" {
		return fail(409, "当前状态不能申请退款")
	}
	if _, e = tx.Exec("INSERT INTO refund_requests VALUES(?,?,?)", o.ID, v.Reason, now()); e != nil {
		return e
	}
	if _, e = tx.Exec("UPDATE orders SET status='refund_requested',updated_at=? WHERE id=?", now(), o.ID); e != nil {
		return e
	}
	if e = addEvent(tx, o.ID, "refund_requested", u.ID); e != nil {
		return e
	}
	if e = tx.Commit(); e != nil {
		return e
	}
	o.Status = "refund_requested"
	return result(w, 200, o)
}

// ExpireUnpaid only releases reservations which were never exposed to a gateway.
// Gateway sessions require reconciliation before inventory can be made available.
func (s *Server) ExpireUnpaid() error {
	tx, e := s.db.Begin()
	if e != nil {
		return e
	}
	defer tx.Rollback()
	q := "SELECT id,product_id FROM orders WHERE status='pending' AND expires_at<? AND id NOT IN (SELECT order_id FROM payments)"
	rows, e := tx.Query(q, now())
	if e != nil {
		return e
	}
	type expired struct{ id, pid string }
	list := []expired{}
	for rows.Next() {
		var o expired
		if e = rows.Scan(&o.id, &o.pid); e != nil {
			rows.Close()
			return e
		}
		list = append(list, o)
	}
	e = rows.Err()
	rows.Close()
	if e != nil {
		return e
	}
	for _, o := range list {
		if _, e = tx.Exec("UPDATE orders SET status='cancelled',updated_at=? WHERE id=?", now(), o.id); e != nil {
			return e
		}
		if _, e = tx.Exec("UPDATE products SET status='available' WHERE id=? AND status='reserved'", o.pid); e != nil {
			return e
		}
		if e = addEvent(tx, o.id, "cancelled", "expiry"); e != nil {
			return e
		}
	}
	return tx.Commit()
}
