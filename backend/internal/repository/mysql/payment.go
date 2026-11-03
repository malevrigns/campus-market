package mysqlrepo

import (
	"context"
	"database/sql"
	"errors"

	"qingfly/edu-mall/internal/domain"
	"qingfly/edu-mall/internal/infra/mysqlx"
	"qingfly/edu-mall/internal/port"
)

type PaymentRepo struct{ *base }

func (s *PaymentRepo) ReconcileCandidates(ctx context.Context, now int64, limit int) ([]domain.Order, error) {
	rows, err := s.db.QueryContext(ctx, "SELECT "+orderColumns+" FROM orders o WHERE o.status='pending' AND EXISTS(SELECT 1 FROM payments p WHERE p.order_id=o.id) ORDER BY COALESCE((SELECT c.checked_at FROM payment_checks c WHERE c.order_id=o.id),0),o.created_at LIMIT ?", limit)
	if err != nil {
		return nil, translate(err)
	}
	defer rows.Close()
	orders := []domain.Order{}
	for rows.Next() {
		o, err := scanOrder(rows)
		if err != nil {
			return nil, translate(err)
		}
		orders = append(orders, o)
	}
	return orders, translate(rows.Err())
}

func (s *PaymentRepo) MarkChecked(ctx context.Context, orderID string, at int64) error {
	_, err := s.db.ExecContext(ctx, "INSERT INTO payment_checks (order_id,checked_at) VALUES(?,?) ON DUPLICATE KEY UPDATE checked_at=VALUES(checked_at)", orderID, at)
	return translate(err)
}

func (s *PaymentRepo) InTx(ctx context.Context, fn func(port.PaymentTx) error) error {
	return translate(mysqlx.WithTx(ctx, s.db, func(tx *sql.Tx) error {
		return fn(&Tx{tx: tx})
	}))
}

// ---------- PaymentTx ----------

func (t *Tx) PaymentTradeNo(ctx context.Context, orderID string) (string, bool, error) {
	var trade sql.NullString
	err := t.tx.QueryRowContext(ctx, "SELECT trade_no FROM payments WHERE order_id=?", orderID).Scan(&trade)
	if errors.Is(err, sql.ErrNoRows) {
		return "", false, nil
	}
	if err != nil {
		return "", false, err
	}
	return trade.String, true, nil
}

func (t *Tx) UpsertPayment(ctx context.Context, orderID, checkoutURL string, at int64) error {
	_, err := t.tx.ExecContext(ctx, "INSERT INTO payments (order_id,checkout_url,created_at) VALUES(?,?,?) ON DUPLICATE KEY UPDATE checkout_url=VALUES(checkout_url)", orderID, checkoutURL, at)
	return err
}

func (t *Tx) UpdatePaymentTradeNo(ctx context.Context, orderID, tradeNo string) error {
	_, err := t.tx.ExecContext(ctx, "UPDATE payments SET trade_no=? WHERE order_id=?", tradeNo, orderID)
	return err
}

func (t *Tx) FindCallback(ctx context.Context, notifyID string) (domain.PaymentCallback, bool, error) {
	var cb domain.PaymentCallback
	err := t.tx.QueryRowContext(ctx, "SELECT order_id,trade_no,status,amount_ct,created_at FROM payment_callbacks WHERE notify_id=?", notifyID).Scan(&cb.OrderID, &cb.TradeNo, &cb.Status, &cb.AmountCt, &cb.CreatedAt)
	if errors.Is(err, sql.ErrNoRows) {
		return cb, false, nil
	}
	cb.NotifyID = notifyID
	return cb, err == nil, err
}

func (t *Tx) InsertCallback(ctx context.Context, cb domain.PaymentCallback) error {
	_, err := t.tx.ExecContext(ctx, "INSERT INTO payment_callbacks (notify_id,order_id,trade_no,status,amount_ct,created_at) VALUES(?,?,?,?,?,?)", cb.NotifyID, cb.OrderID, cb.TradeNo, cb.Status, cb.AmountCt, cb.CreatedAt)
	return err
}
