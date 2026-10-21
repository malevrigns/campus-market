package platform

import (
	"context"
	"database/sql"
	"errors"
	"log/slog"
	"net/http"
	"slices"
	"strings"
	"time"

	"qingfly/edu-mall/internal/infra/mysqlx"
	"qingfly/edu-mall/internal/seckill"
)

type Order struct {
	ID          string       `json:"id"`
	BuyerID     string       `json:"buyer_id"`
	SellerID    string       `json:"seller_id"`
	ProductID   string       `json:"product_id"`
	FlashSaleID string       `json:"flash_sale_id,omitempty"`
	Title       string       `json:"title"`
	Image       string       `json:"image"`
	AmountCt    int64        `json:"amount_ct"`
	Campus      string       `json:"campus"`
	Status      string       `json:"status"`
	CreatedAt   int64        `json:"created_at"`
	UpdatedAt   int64        `json:"updated_at"`
	ExpiresAt   int64        `json:"expires_at"`
	Events      []OrderEvent `json:"events,omitempty"`
}
type OrderEvent struct {
	Status    string `json:"status"`
	CreatedAt int64  `json:"created_at"`
}

const orderColumns = "id,buyer_id,seller_id,COALESCE(product_id,'') AS product_id,COALESCE(flash_sale_id,'') AS flash_sale_id,title,image,amount_ct,campus,status,created_at,updated_at,expires_at"
const orderSelect = "SELECT " + orderColumns + " FROM orders"

func scanOrder(row scanner) (Order, error) {
	var o Order
	e := row.Scan(&o.ID, &o.BuyerID, &o.SellerID, &o.ProductID, &o.FlashSaleID, &o.Title, &o.Image, &o.AmountCt, &o.Campus, &o.Status, &o.CreatedAt, &o.UpdatedAt, &o.ExpiresAt)
	return o, e
}
func keyValid(v string) bool { return len(v) >= 8 && len(v) <= 128 && strings.TrimSpace(v) == v }
func addEvent(tx *sql.Tx, oid, status, actor string) error {
	_, e := tx.Exec("INSERT INTO order_events (order_id,status,actor,created_at) VALUES(?,?,?,?)", oid, status, actor, now())
	return e
}

func (s *Server) notifyOrder(o Order) {
	s.live.Notify([]string{o.BuyerID, o.SellerID}, map[string]any{"type": "order", "order_id": o.ID})
}

// scheduleTimeout 把订单投递到延迟队列，到期后自动关单。
// 入队失败只告警：低频的兜底扫描任务最终也会关闭超时订单。
func (s *Server) scheduleTimeout(ctx context.Context, oid string, expiresAt int64) {
	if e := s.timeouts.Add(context.WithoutCancel(ctx), oid, time.UnixMilli(expiresAt)); e != nil {
		slog.Warn("order timeout enqueue failed, fallback scan will handle it", "order", oid, "error", e)
	}
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
	if !keyValid(v.Idem) || !slices.Contains(campuses, v.Campus) || len(v.ProductID) > 64 {
		return fail(400, "请指定面交校区和有效幂等键")
	}
	ctx := r.Context()
	idempotent := func() (Order, error) {
		old, e := scanOrder(s.db.QueryRowContext(ctx, orderSelect+" WHERE buyer_id=? AND idem=?", u.ID, v.Idem))
		if e != nil {
			return old, e
		}
		if old.ProductID != v.ProductID || old.Campus != v.Campus {
			return old, fail(409, "同一幂等键不能用于不同订单")
		}
		return old, nil
	}
	if old, e := idempotent(); e == nil {
		return result(w, 200, old)
	} else if !errors.Is(e, sql.ErrNoRows) {
		return e
	}
	if !s.allow(r, "order:"+u.ID, 60, time.Minute) {
		return fail(429, "下单太频繁，请稍后再试")
	}
	oid, e := s.newOrderID()
	if e != nil {
		return e
	}
	var o Order
	e = mysqlx.WithTx(ctx, s.db, func(tx *sql.Tx) error {
		p, e := scanProduct(tx.QueryRowContext(ctx, productSelect+" WHERE p.id=?", v.ProductID))
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
		// 防超卖：条件更新是"当前读"，InnoDB 给这一行加排他锁，并发下单在行锁上串行化，
		// 只有第一个事务能把 available 改成 reserved，其余事务拿到锁后 WHERE 不再成立，影响行数为 0。
		res, e := tx.ExecContext(ctx, "UPDATE products SET status='reserved',updated_at=? WHERE id=? AND status='available'", now(), p.ID)
		if e != nil {
			return e
		}
		if n, _ := res.RowsAffected(); n != 1 {
			return fail(409, "商品已被其他同学预订")
		}
		t := now()
		o = Order{ID: oid, BuyerID: u.ID, SellerID: p.SellerID, ProductID: p.ID, Title: p.Title, Image: p.Image, AmountCt: p.PriceCt, Campus: v.Campus, Status: "pending", CreatedAt: t, UpdatedAt: t, ExpiresAt: t + s.cfg.PaymentWindow.Milliseconds()}
		if _, e = tx.ExecContext(ctx, "INSERT INTO orders (id,buyer_id,seller_id,product_id,flash_sale_id,title,image,amount_ct,campus,status,idem,created_at,updated_at,expires_at) VALUES(?,?,?,?,NULL,?,?,?,?,?,?,?,?,?)", o.ID, o.BuyerID, o.SellerID, o.ProductID, o.Title, o.Image, o.AmountCt, o.Campus, o.Status, v.Idem, o.CreatedAt, o.UpdatedAt, o.ExpiresAt); e != nil {
			return e
		}
		return addEvent(tx, o.ID, "pending", u.ID)
	})
	if mysqlx.IsDuplicate(e) && mysqlx.DuplicateKeyName(e) == "uk_orders_idem" {
		// 同一幂等键的并发重试：另一个请求已经成功落单，返回那张订单。
		if old, err := idempotent(); err == nil {
			return result(w, 200, old)
		}
	}
	if e != nil {
		return e
	}
	s.productsChanged(ctx, o.ProductID)
	s.scheduleTimeout(ctx, o.ID, o.ExpiresAt)
	s.recordHeat(ctx, o.ProductID, "", heatOrder)
	s.notifyOrder(o)
	return result(w, 201, o)
}

// orders 列出与我相关的订单。"buyer_id=? OR seller_id=?" 无法同时利用两个索引（最多走 index_merge），
// 拆成两个各自走索引的子查询再 UNION ALL，每个子查询只取前 200 条。
func (s *Server) orders(w http.ResponseWriter, r *http.Request, u *User) error {
	rows, e := s.db.QueryContext(r.Context(), "SELECT * FROM ((SELECT "+orderColumns+" FROM orders WHERE buyer_id=? ORDER BY created_at DESC LIMIT 200) UNION ALL (SELECT "+orderColumns+" FROM orders WHERE seller_id=? AND buyer_id<>? ORDER BY created_at DESC LIMIT 200)) t ORDER BY created_at DESC LIMIT 200", u.ID, u.ID, u.ID)
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

// releaseInventory 在关单事务内归还库存：普通商品恢复可售，抢购订单归还活动库存。
func releaseInventory(tx *sql.Tx, o Order) error {
	if o.FlashSaleID != "" {
		return seckill.ReleaseTx(tx, o.FlashSaleID)
	}
	_, e := tx.Exec("UPDATE products SET status='available',updated_at=? WHERE id=? AND status='reserved'", now(), o.ProductID)
	return e
}

// afterRelease 在关单事务提交后同步缓存/Redis 库存并取消超时任务。
func (s *Server) afterRelease(ctx context.Context, o Order) {
	ctx = context.WithoutCancel(ctx)
	if o.FlashSaleID != "" {
		s.flash.ReleaseRedis(ctx, o.FlashSaleID, o.BuyerID, o.ID)
	} else {
		s.productsChanged(ctx, o.ProductID)
	}
	_ = s.timeouts.Remove(ctx, o.ID)
	s.notifyOrder(o)
}

var errAlreadyDone = errors.New("already in target state")

func (s *Server) cancelOrder(w http.ResponseWriter, r *http.Request, u *User) error {
	var o Order
	e := mysqlx.WithTx(r.Context(), s.db, func(tx *sql.Tx) error {
		var e error
		// SELECT ... FOR UPDATE 锁住订单行：与超时关单、支付回调互斥，避免"刚付款成功又被取消"。
		o, e = scanOrder(tx.QueryRow(orderSelect+" WHERE id=? AND buyer_id=? FOR UPDATE", r.PathValue("id"), u.ID))
		if e != nil {
			return e
		}
		if o.Status == "cancelled" {
			return errAlreadyDone
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
		if e = releaseInventory(tx, o); e != nil {
			return e
		}
		return addEvent(tx, o.ID, "cancelled", u.ID)
	})
	if errors.Is(e, errAlreadyDone) {
		return result(w, 200, o)
	}
	if e != nil {
		return e
	}
	o.Status = "cancelled"
	s.afterRelease(r.Context(), o)
	return result(w, 200, o)
}
func (s *Server) transition(w http.ResponseWriter, r *http.Request, u *User, from, to, role string) error {
	var o Order
	e := mysqlx.WithTx(r.Context(), s.db, func(tx *sql.Tx) error {
		var e error
		o, e = scanOrder(tx.QueryRow(orderSelect+" WHERE id=? AND "+role+"=? FOR UPDATE", r.PathValue("id"), u.ID))
		if e != nil {
			return e
		}
		if o.Status == to {
			return errAlreadyDone
		}
		if o.Status != from {
			return fail(409, "订单状态已变化，请刷新后重试")
		}
		if _, e = tx.Exec("UPDATE orders SET status=?,updated_at=? WHERE id=?", to, now(), o.ID); e != nil {
			return e
		}
		return addEvent(tx, o.ID, to, u.ID)
	})
	if errors.Is(e, errAlreadyDone) {
		return result(w, 200, o)
	}
	if e != nil {
		return e
	}
	o.Status = to
	s.notifyOrder(o)
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
	var o Order
	e := mysqlx.WithTx(r.Context(), s.db, func(tx *sql.Tx) error {
		var e error
		o, e = scanOrder(tx.QueryRow(orderSelect+" WHERE id=? AND buyer_id=? FOR UPDATE", r.PathValue("id"), u.ID))
		if e != nil {
			return e
		}
		if o.Status == "refund_requested" {
			return errAlreadyDone
		}
		if o.Status != "paid" && o.Status != "handover" && o.Status != "completed" {
			return fail(409, "当前状态不能申请退款")
		}
		if _, e = tx.Exec("INSERT INTO refund_requests (order_id,reason,created_at) VALUES(?,?,?)", o.ID, v.Reason, now()); e != nil {
			return e
		}
		if _, e = tx.Exec("UPDATE orders SET status='refund_requested',updated_at=? WHERE id=?", now(), o.ID); e != nil {
			return e
		}
		return addEvent(tx, o.ID, "refund_requested", u.ID)
	})
	if errors.Is(e, errAlreadyDone) {
		return result(w, 200, o)
	}
	if e != nil {
		return e
	}
	o.Status = "refund_requested"
	s.notifyOrder(o)
	return result(w, 200, o)
}

var errNotDue = errors.New("order not yet expired")

// expireOrder 关闭一张超时未付款订单（延迟队列与兜底扫描共用，必须幂等）：
// 已支付/已取消直接视为完成；已打开支付通道的订单交给支付对账处理，避免"用户刚付款就被关单"。
func (s *Server) expireOrder(ctx context.Context, oid string) error {
	var o Order
	released := false
	e := mysqlx.WithTx(ctx, s.db, func(tx *sql.Tx) error {
		var e error
		o, e = scanOrder(tx.QueryRowContext(ctx, orderSelect+" WHERE id=? FOR UPDATE", oid))
		if errors.Is(e, sql.ErrNoRows) {
			return nil
		}
		if e != nil {
			return e
		}
		if o.Status != "pending" {
			return nil
		}
		if o.ExpiresAt > now() {
			return errNotDue
		}
		var sessions int
		if e = tx.QueryRowContext(ctx, "SELECT COUNT(*) FROM payments WHERE order_id=?", o.ID).Scan(&sessions); e != nil {
			return e
		}
		if sessions > 0 {
			return nil
		}
		if _, e = tx.ExecContext(ctx, "UPDATE orders SET status='cancelled',updated_at=? WHERE id=?", now(), o.ID); e != nil {
			return e
		}
		if e = releaseInventory(tx, o); e != nil {
			return e
		}
		released = true
		return addEvent(tx, o.ID, "cancelled", "expiry")
	})
	if errors.Is(e, errNotDue) {
		s.scheduleTimeout(ctx, o.ID, o.ExpiresAt)
		return nil
	}
	if e != nil {
		return e
	}
	if released {
		o.Status = "cancelled"
		s.afterRelease(ctx, o)
	}
	return nil
}

// ExpireUnpaid 是低频兜底扫描：只处理延迟队列漏掉的订单（Redis 数据丢失、入队失败）。
// 借助 (status, expires_at) 索引只扫描已过期的待支付订单，每批最多 200 条。
func (s *Server) ExpireUnpaid(ctx context.Context) error {
	rows, e := s.db.QueryContext(ctx, "SELECT o.id FROM orders o WHERE o.status='pending' AND o.expires_at<? AND NOT EXISTS (SELECT 1 FROM payments p WHERE p.order_id=o.id) ORDER BY o.expires_at LIMIT 200", now())
	if e != nil {
		return e
	}
	ids := []string{}
	for rows.Next() {
		var oid string
		if e = rows.Scan(&oid); e != nil {
			rows.Close()
			return e
		}
		ids = append(ids, oid)
	}
	rows.Close()
	if e = rows.Err(); e != nil {
		return e
	}
	for _, oid := range ids {
		if ctx.Err() != nil {
			return ctx.Err()
		}
		if e = s.expireOrder(ctx, oid); e != nil {
			return e
		}
	}
	if len(ids) > 0 {
		slog.Info("fallback expiry scan closed orders", "count", len(ids))
	}
	return nil
}
