package mysqlrepo

import (
	"context"
	"database/sql"
	"strconv"
	"strings"

	"qingfly/edu-mall/internal/domain"
	"qingfly/edu-mall/internal/infra/mysqlx"
)

type CatalogRepo struct{ *base }

const productSelect = "SELECT p.id,p.seller_id,u.name,p.title,p.description,p.price_ct,p.category,p.campus,p.item_condition,p.image,p.status,p.demo,p.created_at FROM products p JOIN users u ON u.id=p.seller_id"

const productPageSize = 24

func scanProduct(row interface{ Scan(...any) error }) (domain.Product, error) {
	var p domain.Product
	err := row.Scan(&p.ID, &p.SellerID, &p.SellerName, &p.Title, &p.Description, &p.PriceCt, &p.Category, &p.Campus, &p.Condition, &p.Image, &p.Status, &p.Demo, &p.CreatedAt)
	return p, err
}

// List 支持 offset 兼容分页与 (created_at,id)/(price_ct,id) keyset 游标分页。
func (s *CatalogRepo) List(ctx context.Context, q domain.CatalogQuery, viewerID string) (domain.ProductPage, error) {
	where := []string{}
	args := []any{}
	if viewerID == "" || (!q.Mine && !q.Saved) {
		where = append(where, "p.listed=1")
	}
	if q.Keyword != "" {
		pattern := "%" + mysqlx.EscapeLike(q.Keyword) + "%"
		where = append(where, "(p.title LIKE ? OR p.description LIKE ?)")
		args = append(args, pattern, pattern)
	}
	if q.Category != "" {
		where = append(where, "p.category=?")
		args = append(args, q.Category)
	}
	if q.Campus != "" {
		where = append(where, "p.campus=?")
		args = append(args, q.Campus)
	}
	if viewerID != "" && q.Mine {
		where = append(where, "p.seller_id=?")
		args = append(args, viewerID)
	}
	if viewerID != "" && q.Saved {
		where = append(where, "EXISTS(SELECT 1 FROM favorites f WHERE f.product_id=p.id AND f.user_id=?)")
		args = append(args, viewerID)
	}
	orderCol, dir, cmp := "p.created_at", "DESC", "<"
	switch q.Sort {
	case "price-asc":
		orderCol, dir, cmp = "p.price_ct", "ASC", ">"
	case "price-desc":
		orderCol, dir = "p.price_ct", "DESC"
	}
	cond := "1=1"
	if len(where) > 0 {
		cond = strings.Join(where, " AND ")
	}
	var total int
	if err := s.db.QueryRowContext(ctx, "SELECT COUNT(*) FROM products p WHERE "+cond, args...).Scan(&total); err != nil {
		return domain.ProductPage{}, translate(err)
	}
	pageCond, pageArgs := cond, append([]any{}, args...)
	offset := q.Offset
	if q.CursorID != "" {
		pageCond += " AND (" + orderCol + cmp + "? OR (" + orderCol + "=? AND p.id" + cmp + "?))"
		pageArgs = append(pageArgs, q.CursorValue, q.CursorValue, q.CursorID)
		offset = 0
	}
	rows, err := s.db.QueryContext(ctx, productSelect+" WHERE "+pageCond+" ORDER BY "+orderCol+" "+dir+",p.id "+dir+" LIMIT ? OFFSET ?", append(pageArgs, productPageSize+1, offset)...)
	if err != nil {
		return domain.ProductPage{}, translate(err)
	}
	defer rows.Close()
	page := domain.ProductPage{Items: []domain.Product{}, Total: total}
	for rows.Next() {
		p, err := scanProduct(rows)
		if err != nil {
			return domain.ProductPage{}, translate(err)
		}
		page.Items = append(page.Items, p)
	}
	if err := rows.Err(); err != nil {
		return domain.ProductPage{}, translate(err)
	}
	if len(page.Items) > productPageSize {
		page.Items = page.Items[:productPageSize]
		page.HasMore = true
		last := page.Items[productPageSize-1]
		key := last.CreatedAt
		if orderCol == "p.price_ct" {
			key = last.PriceCt
		}
		page.NextCursor = strconv.FormatInt(key, 10) + "_" + last.ID
	}
	return page, nil
}

func (s *CatalogRepo) Get(ctx context.Context, id string) (domain.Product, error) {
	p, err := scanProduct(s.db.QueryRowContext(ctx, productSelect+" WHERE p.id=?", id))
	return p, notFound(err, "内容不存在或无权访问")
}

func (s *CatalogRepo) ByIDs(ctx context.Context, ids []string, listedOnly bool) ([]domain.Product, error) {
	if len(ids) == 0 {
		return []domain.Product{}, nil
	}
	args := make([]any, 0, len(ids))
	for _, id := range ids {
		args = append(args, id)
	}
	q := productSelect + " WHERE p.id IN (?" + strings.Repeat(",?", len(ids)-1) + ")"
	if listedOnly {
		q += " AND p.listed=1"
	}
	rows, err := s.db.QueryContext(ctx, q, args...)
	if err != nil {
		return nil, translate(err)
	}
	defer rows.Close()
	out := []domain.Product{}
	for rows.Next() {
		p, err := scanProduct(rows)
		if err != nil {
			return nil, translate(err)
		}
		out = append(out, p)
	}
	return out, translate(rows.Err())
}

func (s *CatalogRepo) SearchAvailable(ctx context.Context, keyword string, limit int) ([]domain.Product, error) {
	pattern := "%" + mysqlx.EscapeLike(strings.TrimSpace(keyword)) + "%"
	rows, err := s.db.QueryContext(ctx, productSelect+" WHERE p.status='available' AND (p.title LIKE ? OR p.description LIKE ?) ORDER BY p.created_at DESC,p.id DESC LIMIT ?", pattern, pattern, limit)
	if err != nil {
		return nil, translate(err)
	}
	defer rows.Close()
	out := []domain.Product{}
	for rows.Next() {
		p, err := scanProduct(rows)
		if err != nil {
			return nil, translate(err)
		}
		out = append(out, p)
	}
	return out, translate(rows.Err())
}

func (s *CatalogRepo) Create(ctx context.Context, p domain.Product) error {
	_, err := s.db.ExecContext(ctx, "INSERT INTO products (id,seller_id,title,description,price_ct,category,campus,item_condition,image,status,demo,created_at,updated_at) VALUES(?,?,?,?,?,?,?,?,?,?,0,?,?)", p.ID, p.SellerID, p.Title, p.Description, p.PriceCt, p.Category, p.Campus, p.Condition, p.Image, p.Status, p.CreatedAt, p.CreatedAt)
	return translate(err)
}

func (s *CatalogRepo) Withdraw(ctx context.Context, id, sellerID string, at int64) (bool, error) {
	res, err := s.db.ExecContext(ctx, "UPDATE products SET status='withdrawn',updated_at=? WHERE id=? AND seller_id=? AND status='available'", at, id, sellerID)
	if err != nil {
		return false, translate(err)
	}
	n, _ := res.RowsAffected()
	return n == 1, nil
}

func (s *CatalogRepo) FavoriteIDs(ctx context.Context, userID string) ([]string, error) {
	rows, err := s.db.QueryContext(ctx, "SELECT product_id FROM favorites WHERE user_id=? ORDER BY created_at DESC LIMIT 1000", userID)
	if err != nil {
		return nil, translate(err)
	}
	defer rows.Close()
	ids := []string{}
	for rows.Next() {
		var v string
		if err := rows.Scan(&v); err != nil {
			return nil, translate(err)
		}
		ids = append(ids, v)
	}
	return ids, translate(rows.Err())
}

func (s *CatalogRepo) AddFavorite(ctx context.Context, userID, productID string, at int64) (bool, error) {
	res, err := s.db.ExecContext(ctx, "INSERT IGNORE INTO favorites (user_id,product_id,created_at) VALUES(?,?,?)", userID, productID, at)
	if err != nil {
		return false, translate(err)
	}
	n, _ := res.RowsAffected()
	return n == 1, nil
}

func (s *CatalogRepo) RemoveFavorite(ctx context.Context, userID, productID string) error {
	_, err := s.db.ExecContext(ctx, "DELETE FROM favorites WHERE user_id=? AND product_id=?", userID, productID)
	return translate(err)
}

func (s *CatalogRepo) SeedDemo(ctx context.Context, user domain.User, password string, products []domain.Product) error {
	return translate(mysqlx.WithTx(ctx, s.db, func(tx *sql.Tx) error {
		if _, err := tx.ExecContext(ctx, "INSERT IGNORE INTO users (id,email,name,password,created_at) VALUES(?,?,?,?,?)", user.ID, user.Email, user.Name, password, domain.NowMillis()); err != nil {
			return err
		}
		for _, p := range products {
			if _, err := tx.ExecContext(ctx, "INSERT IGNORE INTO products (id,seller_id,title,description,price_ct,category,campus,item_condition,image,status,demo,created_at,updated_at) VALUES(?,?,?,?,?,?,?,?,?,?,?,?,?)", p.ID, p.SellerID, p.Title, p.Description, p.PriceCt, p.Category, p.Campus, p.Condition, p.Image, p.Status, p.Demo, p.CreatedAt, p.CreatedAt); err != nil {
				return err
			}
		}
		return nil
	}))
}
