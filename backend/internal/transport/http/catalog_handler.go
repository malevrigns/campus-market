package httpapi

import (
	"net/http"
	"strconv"

	"qingfly/edu-mall/internal/domain"
	"qingfly/edu-mall/internal/service"
)

func (s *Server) products(w http.ResponseWriter, r *http.Request, _ *domain.User) error {
	q := r.URL.Query()
	offset, _ := strconv.Atoi(q.Get("offset"))
	query := domain.CatalogQuery{Keyword: q.Get("q"), Category: q.Get("category"), Campus: q.Get("campus"), Sort: q.Get("sort"), Offset: offset, Mine: q.Get("mine") == "1", Saved: q.Get("saved") == "1"}
	if raw := q.Get("cursor"); raw != "" {
		value, id, err := service.ParseCursor(raw)
		if err != nil {
			return err
		}
		query.CursorValue, query.CursorID = value, id
	}
	var viewer *domain.User
	if query.Mine || query.Saved {
		u, err := s.currentUser(r)
		if err != nil {
			return err
		}
		viewer = u
	}
	page, err := s.deps.Catalog.List(r.Context(), viewer, query)
	if err != nil {
		return err
	}
	return result(w, 200, page)
}

func (s *Server) product(w http.ResponseWriter, r *http.Request, _ *domain.User) error {
	p, err := s.deps.Catalog.Get(r.Context(), r.PathValue("id"), s.visitorID(r))
	if err != nil {
		return err
	}
	return result(w, 200, p)
}

func (s *Server) hotProducts(w http.ResponseWriter, r *http.Request, _ *domain.User) error {
	limit, _ := strconv.Atoi(r.URL.Query().Get("limit"))
	items, err := s.deps.Catalog.Hot(r.Context(), limit)
	if err != nil {
		return err
	}
	return result(w, 200, map[string]any{"items": items})
}

func (s *Server) publish(w http.ResponseWriter, r *http.Request, u *domain.User) error {
	var v struct {
		Title       string `json:"title"`
		Description string `json:"description"`
		PriceCt     int64  `json:"price_ct"`
		Category    string `json:"category"`
		Campus      string `json:"campus"`
		Condition   string `json:"condition"`
		Image       string `json:"image"`
	}
	if err := decode(r, &v); err != nil {
		return err
	}
	p, err := s.deps.Catalog.Publish(r.Context(), u, domain.PublishInput{Title: v.Title, Description: v.Description, PriceCt: v.PriceCt, Category: v.Category, Campus: v.Campus, Condition: v.Condition, Image: v.Image})
	if err != nil {
		return err
	}
	return result(w, 201, p)
}

func (s *Server) withdraw(w http.ResponseWriter, r *http.Request, u *domain.User) error {
	out, err := s.deps.Catalog.Withdraw(r.Context(), u, r.PathValue("id"))
	if err != nil {
		return err
	}
	return result(w, 200, out)
}

func (s *Server) favorites(w http.ResponseWriter, r *http.Request, u *domain.User) error {
	ids, err := s.deps.Catalog.Favorites(r.Context(), u)
	if err != nil {
		return err
	}
	return result(w, 200, map[string]any{"ids": ids})
}

func (s *Server) favorite(w http.ResponseWriter, r *http.Request, u *domain.User) error {
	out, err := s.deps.Catalog.AddFavorite(r.Context(), u, r.PathValue("id"))
	if err != nil {
		return err
	}
	return result(w, 200, out)
}

func (s *Server) unfavorite(w http.ResponseWriter, r *http.Request, u *domain.User) error {
	out, err := s.deps.Catalog.RemoveFavorite(r.Context(), u, r.PathValue("id"))
	if err != nil {
		return err
	}
	return result(w, 200, out)
}
