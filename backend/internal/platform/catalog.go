package platform

import (
	"net/http"
	"net/url"
	"slices"
	"strconv"
	"strings"
	"unicode/utf8"
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

const productSelect = "SELECT p.id,p.seller_id,u.name,p.title,p.description,p.price_ct,p.category,p.campus,p.condition,p.image,p.status,p.demo,p.created_at FROM products p JOIN users u ON u.id=p.seller_id"

type scanner interface{ Scan(...any) error }

func scanProduct(row scanner) (Product, error) {
	var p Product
	e := row.Scan(&p.ID, &p.SellerID, &p.SellerName, &p.Title, &p.Description, &p.PriceCt, &p.Category, &p.Campus, &p.Condition, &p.Image, &p.Status, &p.Demo, &p.CreatedAt)
	return p, e
}
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
	orderBy := "p.created_at DESC,p.id"
	switch q.Get("sort") {
	case "price-asc":
		orderBy = "p.price_ct ASC,p.id"
	case "price-desc":
		orderBy = "p.price_ct DESC,p.id"
	}
	where := " WHERE p.status IN ('available','reserved') AND (?='' OR instr(lower(p.title||p.description),lower(?))>0) AND (?='' OR p.category=?) AND (?='' OR p.campus=?)"
	args := []any{keyword, keyword, q.Get("category"), q.Get("category"), q.Get("campus"), q.Get("campus")}
	if q.Get("mine") == "1" || q.Get("saved") == "1" {
		u, e := s.currentUser(r)
		if e != nil {
			return e
		}
		where = strings.Replace(where, "p.status IN ('available','reserved')", "1=1", 1)
		if q.Get("mine") == "1" {
			where += " AND p.seller_id=?"
			args = append(args, u.ID)
		}
		if q.Get("saved") == "1" {
			where += " AND EXISTS(SELECT 1 FROM favorites f WHERE f.product_id=p.id AND f.user_id=?)"
			args = append(args, u.ID)
		}
	}
	var total int
	if e := s.db.QueryRowContext(r.Context(), "SELECT COUNT(*) FROM products p"+where, args...).Scan(&total); e != nil {
		return e
	}
	rows, e := s.db.QueryContext(r.Context(), productSelect+where+" ORDER BY "+orderBy+" LIMIT 24 OFFSET ?", append(args, offset)...)
	if e != nil {
		return e
	}
	defer rows.Close()
	items := []Product{}
	for rows.Next() {
		p, e := scanProduct(rows)
		if e != nil {
			return e
		}
		items = append(items, p)
	}
	if e = rows.Err(); e != nil {
		return e
	}
	return result(w, 200, map[string]any{"items": items, "total": total, "has_more": offset+len(items) < total})
}
func (s *Server) product(w http.ResponseWriter, r *http.Request, _ *User) error {
	p, e := scanProduct(s.db.QueryRowContext(r.Context(), productSelect+" WHERE p.id=?", r.PathValue("id")))
	if e != nil {
		return e
	}
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
	im, e := url.Parse(v.Image)
	validImage := e == nil && ((im.Scheme == "https" && im.Host != "" && im.User == nil) || localImage.MatchString(v.Image)) && len(v.Image) <= 2048
	if utf8.RuneCountInString(v.Title) < 2 || utf8.RuneCountInString(v.Title) > 80 || utf8.RuneCountInString(v.Description) > 3000 || v.PriceCt < 1 || v.PriceCt > 10000000 || !slices.Contains(categories, v.Category) || !slices.Contains(campuses, v.Campus) || !slices.Contains(conditions, v.Condition) || !validImage {
		return fail(400, "请检查标题、金额、分类、校区、成色和 HTTPS 图片地址")
	}
	p := Product{ID: id("p_"), SellerID: u.ID, SellerName: u.Name, Title: v.Title, Description: v.Description, PriceCt: v.PriceCt, Category: v.Category, Campus: v.Campus, Condition: v.Condition, Image: v.Image, Status: "available", CreatedAt: now()}
	_, e = s.db.ExecContext(r.Context(), "INSERT INTO products VALUES(?,?,?,?,?,?,?,?,?,?,?,?)", p.ID, u.ID, p.Title, p.Description, p.PriceCt, p.Category, p.Campus, p.Condition, p.Image, p.Status, 0, p.CreatedAt)
	if e != nil {
		return e
	}
	return result(w, 201, p)
}
func (s *Server) withdraw(w http.ResponseWriter, r *http.Request, u *User) error {
	res, e := s.db.ExecContext(r.Context(), "UPDATE products SET status='withdrawn' WHERE id=? AND seller_id=? AND status='available'", r.PathValue("id"), u.ID)
	if e != nil {
		return e
	}
	n, _ := res.RowsAffected()
	if n != 1 {
		return fail(409, "只能下架本人未被预订的商品")
	}
	return result(w, 200, map[string]bool{"withdrawn": true})
}
func (s *Server) favorites(w http.ResponseWriter, r *http.Request, u *User) error {
	rows, e := s.db.QueryContext(r.Context(), "SELECT product_id FROM favorites WHERE user_id=?", u.ID)
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
	var exists int
	if e := s.db.QueryRowContext(r.Context(), "SELECT 1 FROM products WHERE id=?", r.PathValue("id")).Scan(&exists); e != nil {
		return e
	}
	_, e := s.db.ExecContext(r.Context(), "INSERT OR IGNORE INTO favorites VALUES(?,?)", u.ID, r.PathValue("id"))
	if e != nil {
		return e
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
