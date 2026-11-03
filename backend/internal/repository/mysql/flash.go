package mysqlrepo

import (
	"context"
	"database/sql"
	"errors"

	"qingfly/edu-mall/internal/domain"
)

type FlashRepo struct{ *base }

const saleSelect = "SELECT f.id,f.seller_id,u.name,f.title,f.description,f.image,f.campus,f.original_price_ct,f.price_ct,f.total_stock,f.stock,f.start_at,f.end_at,f.status,f.created_at FROM flash_sales f JOIN users u ON u.id=f.seller_id"

func scanSale(row interface{ Scan(...any) error }) (domain.FlashSale, error) {
	var s domain.FlashSale
	err := row.Scan(&s.ID, &s.SellerID, &s.SellerName, &s.Title, &s.Description, &s.Image, &s.Campus, &s.OriginalPriceCt, &s.PriceCt, &s.TotalStock, &s.Stock, &s.StartAt, &s.EndAt, &s.Status, &s.CreatedAt)
	if errors.Is(err, sql.ErrNoRows) {
		return s, domain.NotFound("活动不存在")
	}
	return s, err
}

func (s *FlashRepo) Create(ctx context.Context, sale domain.FlashSale) error {
	_, err := s.db.ExecContext(ctx, "INSERT INTO flash_sales (id,seller_id,title,description,image,campus,original_price_ct,price_ct,total_stock,stock,start_at,end_at,status,created_at,updated_at) VALUES (?,?,?,?,?,?,?,?,?,?,?,?,?,?,?)", sale.ID, sale.SellerID, sale.Title, sale.Description, sale.Image, sale.Campus, sale.OriginalPriceCt, sale.PriceCt, sale.TotalStock, sale.Stock, sale.StartAt, sale.EndAt, sale.Status, sale.CreatedAt, sale.CreatedAt)
	return translate(err)
}

func (s *FlashRepo) Get(ctx context.Context, id string) (domain.FlashSale, error) {
	sale, err := scanSale(s.db.QueryRowContext(ctx, saleSelect+" WHERE f.id=?", id))
	return sale, translate(err)
}

func (s *FlashRepo) List(ctx context.Context, since int64, limit int) ([]domain.FlashSale, error) {
	rows, err := s.db.QueryContext(ctx, saleSelect+" WHERE f.end_at>? AND f.status='scheduled' ORDER BY f.start_at LIMIT ?", since, limit)
	if err != nil {
		return nil, translate(err)
	}
	defer rows.Close()
	out := []domain.FlashSale{}
	for rows.Next() {
		sale, err := scanSale(rows)
		if err != nil {
			return nil, translate(err)
		}
		out = append(out, sale)
	}
	return out, translate(rows.Err())
}

func (s *FlashRepo) WarmList(ctx context.Context, since int64, limit int) ([]domain.FlashSale, error) {
	rows, err := s.db.QueryContext(ctx, saleSelect+" WHERE f.end_at>? ORDER BY f.start_at LIMIT ?", since, limit)
	if err != nil {
		return nil, translate(err)
	}
	defer rows.Close()
	out := []domain.FlashSale{}
	for rows.Next() {
		sale, err := scanSale(rows)
		if err != nil {
			return nil, translate(err)
		}
		out = append(out, sale)
	}
	return out, translate(rows.Err())
}

func (s *FlashRepo) ReconcileList(ctx context.Context, now int64) ([]domain.FlashSale, error) {
	rows, err := s.db.QueryContext(ctx, "SELECT id,stock,start_at,end_at,status FROM flash_sales WHERE end_at>? AND status='scheduled'", now)
	if err != nil {
		return nil, translate(err)
	}
	defer rows.Close()
	out := []domain.FlashSale{}
	for rows.Next() {
		var sale domain.FlashSale
		if err := rows.Scan(&sale.ID, &sale.Stock, &sale.StartAt, &sale.EndAt, &sale.Status); err != nil {
			return nil, translate(err)
		}
		out = append(out, sale)
	}
	return out, translate(rows.Err())
}

func (s *FlashRepo) Cancel(ctx context.Context, id string, at int64) (bool, error) {
	res, err := s.db.ExecContext(ctx, "UPDATE flash_sales SET status='cancelled',updated_at=? WHERE id=? AND status='scheduled'", at, id)
	if err != nil {
		return false, translate(err)
	}
	n, _ := res.RowsAffected()
	return n == 1, nil
}
