package platform

import (
	"context"
	"database/sql"
	"errors"
	"net/http"
	"net/url"
	"slices"
	"strconv"
	"strings"
	"time"
	"unicode/utf8"

	"qingfly/edu-mall/internal/infra/mysqlx"
	"qingfly/edu-mall/internal/pkg/cache"
)

var categories = []string{"数码电子", "教材书籍", "生活日用", "服饰鞋包", "运动户外", "美妆个护"}
var campuses = []string{"东区", "西区", "南湖校区", "未来城"}
var conditions = []string{"全新", "九九新", "九成新", "八成新", "七成新"}

type Product struct {
	ID          string `json:"id"`
	SellerID    string `json:"seller_id"`
	SellerName  string `json:"seller_name"`
	Title       string `json:"title"`
	Description string `json:"description"`
	PriceCt     int64  `json:"price_ct"`
	Category    string `json:"category"`
	Campus      string `json:"campus"`
	Condition   string `json:"condition"`
	Image       string `json:"image"`
	Status      string `json:"status"`
	Demo        bool   `json:"demo"`
	CreatedAt   int64  `json:"created_at"`
}

const productSelect = "SELECT p.id,p.seller_id,u.name,p.title,p.description,p.price_ct,p.category,p.campus,p.item_condition,p.image,p.status,p.demo,p.created_at FROM products p JOIN users u ON u.id=p.seller_id"

const pageSize = 24

type scanner interface{ Scan(...any) error }

func scanProduct(row scanner) (Product, error) {
	var p Product
	e := row.Scan(&p.ID, &p.SellerID, &p.SellerName, &p.Title, &p.Description, &p.PriceCt, &p.Category, &p.Campus, &p.Condition, &p.Image, &p.Status, &p.Demo, &p.CreatedAt)
	return p, e
}

type productPage struct {
	Items      []Product `json:"items"`
	Total      int       `json:"total"`
	HasMore    bool      `json:"has_more"`
	NextCursor string    `json:"next_cursor,omitempty"`
}

// 列表缓存只缓存首页默认信息流（最热的一个查询），任何商品变更都会删除它；
// 带搜索/筛选/翻页的查询组合太多、命中率低，直接查库（均有对应索引）。
const feedCacheKey = "products:feed:v1"

func (s *Server) products(w http.ResponseWriter, r *http.Request, _ *User) error {
	q := r.URL.Query()
	keyword := strings.TrimSpace(q.Get("q"))
	if utf8.RuneCountInString(keyword) > 100 {
		return fail(400, "搜索词过长")
	}
	offset, _ := strconv.Atoi(q.Get("offset"))
	if offset < 0 || offset > 10000 {
		return fail(400, "分页超出范围")
	}
	sortBy := q.Get("sort")
	cursor := q.Get("cursor")
	personal := q.Get("mine") == "1" || q.Get("saved") == "1"
	if !personal && keyword == "" && q.Get("category") == "" && q.Get("campus") == "" && (sortBy == "" || sortBy == "new") && offset == 0 && cursor == "" {
		page, e := cache.Fetch(r.Context(), s.cache, feedCacheKey, 30*time.Second, func(ctx context.Context) (productPage, error) {
			return s.queryProducts(ctx, q, nil, keyword, 0, "")
		})
		if e != nil {
			return e
		}
		return result(w, 200, page)
	}
	var u *User
	if personal {
		var e error
		if u, e = s.currentUser(r); e != nil {
			return e
		}
	}
	page, e := s.queryProducts(r.Context(), q, u, keyword, offset, cursor)
	if e != nil {
		return e
	}
	return result(w, 200, page)
}

// queryProducts 支持两种分页：
//   - offset：兼容旧客户端，最多翻到 1 万条；
//   - cursor（游标/keyset 分页）：WHERE (created_at,id) < (上一页最后一条)，无论翻多深都只扫描一页数据，
//     避免 LIMIT 10000,24 先扫 10024 行再丢弃的深分页问题，也不会因为新商品插入导致翻页重复/漏数据。
func (s *Server) queryProducts(ctx context.Context, q url.Values, u *User, keyword string, offset int, cursor string) (productPage, error) {
	where := []string{}
	args := []any{}
	if u == nil {
		where = append(where, "p.listed=1")
	}
	if keyword != "" {
		// 中文分词全文检索在 MySQL 可以用 FULLTEXT ngram，规模更大应接 ES；校园规模下 LIKE + 筛选索引足够，
		// 并对 % _ 做转义，避免用户输入通配符。
		pattern := "%" + mysqlx.EscapeLike(keyword) + "%"
		where = append(where, "(p.title LIKE ? OR p.description LIKE ?)")
		args = append(args, pattern, pattern)
	}
	if c := q.Get("category"); c != "" {
		where = append(where, "p.category=?")
		args = append(args, c)
	}
	if c := q.Get("campus"); c != "" {
		where = append(where, "p.campus=?")
		args = append(args, c)
	}
	if u != nil && q.Get("mine") == "1" {
		where = append(where, "p.seller_id=?")
		args = append(args, u.ID)
	}
	if u != nil && q.Get("saved") == "1" {
		where = append(where, "EXISTS(SELECT 1 FROM favorites f WHERE f.product_id=p.id AND f.user_id=?)")
		args = append(args, u.ID)
	}
	orderCol, dir, cmp := "p.created_at", "DESC", "<"
	switch q.Get("sort") {
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
	if e := s.db.QueryRowContext(ctx, "SELECT COUNT(*) FROM products p WHERE "+cond, args...).Scan(&total); e != nil {
		return productPage{}, e
	}
	pageCond, pageArgs := cond, append([]any{}, args...)
	if cursor != "" {
		key, cid, ok := strings.Cut(cursor, "_")
		value, err := strconv.ParseInt(key, 10, 64)
		if !ok || err != nil || cid == "" {
			return productPage{}, fail(400, "分页游标无效")
		}
		pageCond += " AND (" + orderCol + cmp + "? OR (" + orderCol + "=? AND p.id" + cmp + "?))"
		pageArgs = append(pageArgs, value, value, cid)
		offset = 0
	}
	// 多取一条判断是否还有下一页，不需要再依赖 COUNT 结果。
	rows, e := s.db.QueryContext(ctx, productSelect+" WHERE "+pageCond+" ORDER BY "+orderCol+" "+dir+",p.id "+dir+" LIMIT ? OFFSET ?", append(pageArgs, pageSize+1, offset)...)
	if e != nil {
		return productPage{}, e
	}
	defer rows.Close()
	page := productPage{Items: []Product{}, Total: total}
	for rows.Next() {
		p, e := scanProduct(rows)
		if e != nil {
			return productPage{}, e
		}
		page.Items = append(page.Items, p)
	}
	if e = rows.Err(); e != nil {
		return productPage{}, e
	}
	if len(page.Items) > pageSize {
		page.Items = page.Items[:pageSize]
		page.HasMore = true
		last := page.Items[pageSize-1]
		key := last.CreatedAt
		if orderCol == "p.price_ct" {
			key = last.PriceCt
		}
		page.NextCursor = strconv.FormatInt(key, 10) + "_" + last.ID
	}
	return page, nil
}

func productKey(id string) string { return "product:" + id }

// loadProduct 走多级缓存读取商品详情。
func (s *Server) loadProduct(ctx context.Context, pid string) (Product, error) {
	p, e := cache.Fetch(ctx, s.cache, productKey(pid), 10*time.Minute, func(ctx context.Context) (Product, error) {
		p, e := scanProduct(s.db.QueryRowContext(ctx, productSelect+" WHERE p.id=?", pid))
		if errors.Is(e, sql.ErrNoRows) {
			return p, cache.ErrNotFound
		}
		return p, e
	})
	if errors.Is(e, cache.ErrNotFound) {
		return p, sql.ErrNoRows
	}
	return p, e
}

// productsChanged 在商品状态变更的事务提交后调用：删详情缓存 + 首页信息流缓存。
func (s *Server) productsChanged(ctx context.Context, ids ...string) {
	keys := []string{feedCacheKey}
	for _, pid := range ids {
		if pid != "" {
			keys = append(keys, productKey(pid))
		}
	}
	s.cache.Invalidate(context.WithoutCancel(ctx), keys...)
}

func (s *Server) product(w http.ResponseWriter, r *http.Request, _ *User) error {
	if len(r.PathValue("id")) > 64 {
		return sql.ErrNoRows
	}
	p, e := s.loadProduct(r.Context(), r.PathValue("id"))
	if e != nil {
		return e
	}
	visitor := "ip:" + s.clientIP(r)
	if c, err := s.sessionCookie(r); err == nil {
		visitor = "s:" + digest(c.Value)[:16]
	}
	s.recordHeat(r.Context(), p.ID, visitor, heatView)
	return result(w, 200, p)
}
func (s *Server) publish(w http.ResponseWriter, r *http.Request, u *User) error {
	var v struct {
		Title       string `json:"title"`
		Description string `json:"description"`
		PriceCt     int64  `json:"price_ct"`
		Category    string `json:"category"`
		Campus      string `json:"campus"`
		Condition   string `json:"condition"`
		Image       string `json:"image"`
	}
	if e := decode(r, &v); e != nil {
		return e
	}
	v.Title = strings.TrimSpace(v.Title)
	v.Description = strings.TrimSpace(v.Description)
	if !validImageURL(v.Image) || utf8.RuneCountInString(v.Title) < 2 || utf8.RuneCountInString(v.Title) > 80 || utf8.RuneCountInString(v.Description) > 3000 || v.PriceCt < 1 || v.PriceCt > 10000000 || !slices.Contains(categories, v.Category) || !slices.Contains(campuses, v.Campus) || !slices.Contains(conditions, v.Condition) {
		return fail(400, "请检查标题、金额、分类、校区、成色和 HTTPS 图片地址")
	}
	if !s.allow(r, "publish:"+u.ID, 60, time.Hour) {
		return fail(429, "发布太频繁，请稍后再试")
	}
	p := Product{ID: id("p_"), SellerID: u.ID, SellerName: u.Name, Title: v.Title, Description: v.Description, PriceCt: v.PriceCt, Category: v.Category, Campus: v.Campus, Condition: v.Condition, Image: v.Image, Status: "available", CreatedAt: now()}
	_, e := s.db.ExecContext(r.Context(), "INSERT INTO products (id,seller_id,title,description,price_ct,category,campus,item_condition,image,status,demo,created_at,updated_at) VALUES(?,?,?,?,?,?,?,?,?,?,0,?,?)", p.ID, u.ID, p.Title, p.Description, p.PriceCt, p.Category, p.Campus, p.Condition, p.Image, p.Status, p.CreatedAt, p.CreatedAt)
	if e != nil {
		return e
	}
	s.productsChanged(r.Context(), p.ID)
	return result(w, 201, p)
}

func validImageURL(v string) bool {
	im, e := url.Parse(v)
	return e == nil && ((im.Scheme == "https" && im.Host != "" && im.User == nil) || localImage.MatchString(v)) && len(v) <= 2048
}

func (s *Server) withdraw(w http.ResponseWriter, r *http.Request, u *User) error {
	res, e := s.db.ExecContext(r.Context(), "UPDATE products SET status='withdrawn',updated_at=? WHERE id=? AND seller_id=? AND status='available'", now(), r.PathValue("id"), u.ID)
	if e != nil {
		return e
	}
	n, _ := res.RowsAffected()
	if n != 1 {
		return fail(409, "只能下架本人未被预订的商品")
	}
	s.productsChanged(r.Context(), r.PathValue("id"))
	s.removeHeat(r.Context(), r.PathValue("id"))
	return result(w, 200, map[string]bool{"withdrawn": true})
}
func (s *Server) favorites(w http.ResponseWriter, r *http.Request, u *User) error {
	rows, e := s.db.QueryContext(r.Context(), "SELECT product_id FROM favorites WHERE user_id=? ORDER BY created_at DESC LIMIT 1000", u.ID)
	if e != nil {
		return e
	}
	defer rows.Close()
	ids := []string{}
	for rows.Next() {
		var v string
		if e = rows.Scan(&v); e != nil {
			return e
		}
		ids = append(ids, v)
	}
	if e = rows.Err(); e != nil {
		return e
	}
	return result(w, 200, map[string]any{"ids": ids})
}
func (s *Server) favorite(w http.ResponseWriter, r *http.Request, u *User) error {
	pid := r.PathValue("id")
	if _, e := s.loadProduct(r.Context(), pid); e != nil {
		return e
	}
	res, e := s.db.ExecContext(r.Context(), "INSERT IGNORE INTO favorites (user_id,product_id,created_at) VALUES(?,?,?)", u.ID, pid, now())
	if e != nil {
		return e
	}
	if n, _ := res.RowsAffected(); n == 1 {
		s.recordHeat(r.Context(), pid, "", heatFavorite)
	}
	return result(w, 200, map[string]bool{"saved": true})
}
func (s *Server) unfavorite(w http.ResponseWriter, r *http.Request, u *User) error {
	_, e := s.db.ExecContext(r.Context(), "DELETE FROM favorites WHERE user_id=? AND product_id=?", u.ID, r.PathValue("id"))
	if e != nil {
		return e
	}
	return result(w, 200, map[string]bool{"saved": false})
}
