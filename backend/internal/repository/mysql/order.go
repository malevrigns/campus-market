package mysqlrepo

import (
	"context"
	"database/sql"
	"errors"

	"qingfly/edu-mall/internal/domain"
	"qingfly/edu-mall/internal/infra/mysqlx"
	"qingfly/edu-mall/internal/port"
)

type OrderRepo struct{ *base }

const orderColumns = "id,buyer_id,seller_id,COALESCE(product_id,'') AS product_id,COALESCE(flash_sale_id,'') AS flash_sale_id,title,image,amount_ct,campus,status,created_at,updated_at,expires_at"
const orderSelect = "SELECT " + orderColumns + " FROM orders"

func scanOrder(row interface{ Scan(...any) error }) (domain.Order, error) {
	var o domain.Order
	err := row.Scan(&o.ID, &o.BuyerID, &o.SellerID, &o.ProductID, &o.FlashSaleID, &o.Title, &o.Image, &o.AmountCt, &o.Campus, &o.Status, &o.CreatedAt, &o.UpdatedAt, &o.ExpiresAt)
	return o, err
}

func (s *OrderRepo) FindByIdem(ctx context.Context, buyerID, idem string) (domain.Order, error) {
	o, err := scanOrder(s.db.QueryRowContext(ctx, orderSelect+" WHERE buyer_id=? AND idem=?", buyerID, idem))
	return o, notFound(err, "内容不存在或无权访问")
}

func (s *OrderRepo) FlashOrder(ctx context.Context, saleID, buyerID string) (domain.Order, bool, error) {
	o, err := scanOrder(s.db.QueryRowContext(ctx, orderSelect+" WHERE flash_sale_id=? AND buyer_id=?", saleID, buyerID))
	if errors.Is(err, sql.ErrNoRows) {
		return o, false, nil
	}
	return o, err == nil, err
}

func (s *OrderRepo) List(ctx context.Context, userID string) ([]domain.Order, error) {
	rows, err := s.db.QueryContext(ctx, "SELECT * FROM ((SELECT "+orderColumns+" FROM orders WHERE buyer_id=? ORDER BY created_at DESC LIMIT 200) UNION ALL (SELECT "+orderColumns+" FROM orders WHERE seller_id=? AND buyer_id<>? ORDER BY created_at DESC LIMIT 200)) t ORDER BY created_at DESC LIMIT 200", userID, userID, userID)
	if err != nil {
		return nil, translate(err)
	}
	defer rows.Close()
	items := []domain.Order{}
	for rows.Next() {
		o, err := scanOrder(rows)
		if err != nil {
			return nil, translate(err)
		}
		items = append(items, o)
	}
	return items, translate(rows.Err())
}

func (s *OrderRepo) GetOwned(ctx context.Context, orderID, userID string) (domain.Order, error) {
	o, err := scanOrder(s.db.QueryRowContext(ctx, orderSelect+" WHERE id=? AND (buyer_id=? OR seller_id=?)", orderID, userID, userID))
	return o, notFound(err, "内容不存在或无权访问")
}

func (s *OrderRepo) Events(ctx context.Context, orderID string) ([]domain.OrderEvent, error) {
	rows, err := s.db.QueryContext(ctx, "SELECT status,created_at FROM order_events WHERE order_id=? ORDER BY id", orderID)
	if err != nil {
		return nil, translate(err)
	}
	defer rows.Close()
	events := []domain.OrderEvent{}
	for rows.Next() {
		var ev domain.OrderEvent
		if err := rows.Scan(&ev.Status, &ev.CreatedAt); err != nil {
			return nil, translate(err)
		}
		events = append(events, ev)
	}
	return events, translate(rows.Err())
}

func (s *OrderRepo) ExpiredIDs(ctx context.Context, now int64, limit int) ([]string, error) {
	rows, err := s.db.QueryContext(ctx, "SELECT o.id FROM orders o WHERE o.status='pending' AND o.expires_at<? AND NOT EXISTS (SELECT 1 FROM payments p WHERE p.order_id=o.id) ORDER BY o.expires_at LIMIT ?", now, limit)
	if err != nil {
		return nil, translate(err)
	}
	defer rows.Close()
	ids := []string{}
	for rows.Next() {
		var id string
		if err := rows.Scan(&id); err != nil {
			return nil, translate(err)
		}
		ids = append(ids, id)
	}
	return ids, translate(rows.Err())
}

func (s *OrderRepo) InTx(ctx context.Context, fn func(port.OrderTx) error) error {
	return translate(mysqlx.WithTx(ctx, s.db, func(tx *sql.Tx) error {
		return fn(&Tx{tx: tx})
	}))
}

// ---------- OrderTx ----------

func (t *Tx) LockProduct(ctx context.Context, id string) (domain.Product, error) {
	p, err := scanProduct(t.tx.QueryRowContext(ctx, productSelect+" WHERE p.id=?", id))
	return p, notFound(err, "内容不存在或无权访问")
}

func (t *Tx) ReserveProduct(ctx context.Context, id string, at int64) (bool, error) {
	res, err := t.tx.ExecContext(ctx, "UPDATE products SET status='reserved',updated_at=? WHERE id=? AND status='available'", at, id)
	if err != nil {
		return false, err
	}
	n, _ := res.RowsAffected()
	return n == 1, nil
}

func (t *Tx) ReleaseProduct(ctx context.Context, id string, at int64) error {
	_, err := t.tx.ExecContext(ctx, "UPDATE products SET status='available',updated_at=? WHERE id=? AND status='reserved'", at, id)
	return err
}

func (t *Tx) LockOrder(ctx context.Context, id string) (domain.Order, error) {
	o, err := scanOrder(t.tx.QueryRowContext(ctx, orderSelect+" WHERE id=? FOR UPDATE", id))
	if errors.Is(err, sql.ErrNoRows) {
		return o, nil
	}
	return o, err
}

func (t *Tx) LockOwnedOrder(ctx context.Context, id, userID, role string) (domain.Order, error) {
	if role != "buyer_id" && role != "seller_id" {
		return domain.Order{}, domain.Invalid("无效的订单角色")
	}
	o, err := scanOrder(t.tx.QueryRowContext(ctx, orderSelect+" WHERE id=? AND "+role+"=? FOR UPDATE", id, userID))
	return o, notFound(err, "内容不存在或无权访问")
}

func (t *Tx) CountPaymentSessions(ctx context.Context, orderID string) (int, error) {
	var n int
	err := t.tx.QueryRowContext(ctx, "SELECT COUNT(*) FROM payments WHERE order_id=?", orderID).Scan(&n)
	return n, err
}

func (t *Tx) InsertOrder(ctx context.Context, o domain.Order, idem string) error {
	var productID, flashID any
	if o.ProductID != "" {
		productID = o.ProductID
	}
	if o.FlashSaleID != "" {
		flashID = o.FlashSaleID
	}
	_, err := t.tx.ExecContext(ctx, "INSERT INTO orders (id,buyer_id,seller_id,product_id,flash_sale_id,title,image,amount_ct,campus,status,idem,created_at,updated_at,expires_at) VALUES(?,?,?,?,?,?,?,?,?,?,?,?,?,?)", o.ID, o.BuyerID, o.SellerID, productID, flashID, o.Title, o.Image, o.AmountCt, o.Campus, o.Status, idem, o.CreatedAt, o.UpdatedAt, o.ExpiresAt)
	return duplicate(err)
}

func (t *Tx) InsertEvent(ctx context.Context, orderID, status, actor string, at int64) error {
	_, err := t.tx.ExecContext(ctx, "INSERT INTO order_events (order_id,status,actor,created_at) VALUES(?,?,?,?)", orderID, status, actor, at)
	return err
}

func (t *Tx) UpdateOrderStatus(ctx context.Context, id, status string, at int64) error {
	_, err := t.tx.ExecContext(ctx, "UPDATE orders SET status=?,updated_at=? WHERE id=?", status, at, id)
	return err
}

func (t *Tx) InsertRefund(ctx context.Context, orderID, reason string, at int64) error {
	_, err := t.tx.ExecContext(ctx, "INSERT INTO refund_requests (order_id,reason,created_at) VALUES(?,?,?)", orderID, reason, at)
	return err
}

func (t *Tx) MarkProductSold(ctx context.Context, productID string, at int64) error {
	_, err := t.tx.ExecContext(ctx, "UPDATE products SET status='sold',updated_at=? WHERE id=?", at, productID)
	return err
}

func (t *Tx) DecrementFlashStock(ctx context.Context, saleID string, at int64) (bool, error) {
	res, err := t.tx.ExecContext(ctx, "UPDATE flash_sales SET stock=stock-1,updated_at=? WHERE id=? AND stock>0 AND status='scheduled'", at, saleID)
	if err != nil {
		return false, err
	}
	n, _ := res.RowsAffected()
	return n == 1, nil
}

func (t *Tx) ReleaseFlashStock(ctx context.Context, saleID string, at int64) error {
	_, err := t.tx.ExecContext(ctx, "UPDATE flash_sales SET stock=stock+1,updated_at=? WHERE id=? AND stock<total_stock", at, saleID)
	return err
}

func (t *Tx) FlashSaleOrderInfo(ctx context.Context, saleID string) (string, string, string, string, int64, error) {
	var seller, title, image, campus string
	var price int64
	err := t.tx.QueryRowContext(ctx, "SELECT seller_id,title,image,campus,price_ct FROM flash_sales WHERE id=?", saleID).Scan(&seller, &title, &image, &campus, &price)
	return seller, title, image, campus, price, notFound(err, "活动不存在")
}

func (t *Tx) OrderByFlashBuyer(ctx context.Context, saleID, buyerID string) (domain.Order, bool, error) {
	o, err := scanOrder(t.tx.QueryRowContext(ctx, orderSelect+" WHERE flash_sale_id=? AND buyer_id=?", saleID, buyerID))
	if errors.Is(err, sql.ErrNoRows) {
		return o, false, nil
	}
	return o, err == nil, err
}
