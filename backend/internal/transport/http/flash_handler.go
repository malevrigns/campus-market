package httpapi

import (
	"net/http"

	"qingfly/edu-mall/internal/domain"
)

func (s *Server) flashSales(w http.ResponseWriter, r *http.Request, _ *domain.User) error {
	out, err := s.deps.Flash.List(r.Context())
	if err != nil {
		return err
	}
	return result(w, 200, out)
}

func (s *Server) flashSale(w http.ResponseWriter, r *http.Request, _ *domain.User) error {
	out, err := s.deps.Flash.Get(r.Context(), r.PathValue("id"))
	if err != nil {
		return err
	}
	return result(w, 200, out)
}

func (s *Server) flashAttempt(w http.ResponseWriter, r *http.Request, u *domain.User) error {
	ticket, queued, err := s.deps.Flash.Attempt(r.Context(), u, r.PathValue("id"))
	if err != nil {
		return err
	}
	status := http.StatusOK
	if queued {
		status = http.StatusAccepted
	}
	return result(w, status, ticket)
}

func (s *Server) flashResult(w http.ResponseWriter, r *http.Request, u *domain.User) error {
	ticket, err := s.deps.Flash.Result(r.Context(), u, r.PathValue("id"))
	if err != nil {
		return err
	}
	return result(w, 200, ticket)
}

func (s *Server) createFlashSale(w http.ResponseWriter, r *http.Request, u *domain.User) error {
	var v struct {
		Title           string `json:"title"`
		Description     string `json:"description"`
		Image           string `json:"image"`
		Campus          string `json:"campus"`
		OriginalPriceCt int64  `json:"original_price_ct"`
		PriceCt         int64  `json:"price_ct"`
		TotalStock      int    `json:"total_stock"`
		StartAt         int64  `json:"start_at"`
		EndAt           int64  `json:"end_at"`
	}
	if err := decode(r, &v); err != nil {
		return err
	}
	sale, err := s.deps.Flash.Create(r.Context(), u, domain.CreateFlashSaleInput{Title: v.Title, Description: v.Description, Image: v.Image, Campus: v.Campus, OriginalPriceCt: v.OriginalPriceCt, PriceCt: v.PriceCt, TotalStock: v.TotalStock, StartAt: v.StartAt, EndAt: v.EndAt})
	if err != nil {
		return err
	}
	return result(w, 201, sale)
}

func (s *Server) cancelFlashSale(w http.ResponseWriter, r *http.Request, _ *domain.User) error {
	if err := s.deps.Flash.Cancel(r.Context(), r.PathValue("id")); err != nil {
		return err
	}
	return result(w, 200, map[string]bool{"cancelled": true})
}
