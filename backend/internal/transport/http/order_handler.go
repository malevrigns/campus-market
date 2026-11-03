package httpapi

import (
	"net/http"

	"qingfly/edu-mall/internal/domain"
)

func (s *Server) createOrder(w http.ResponseWriter, r *http.Request, u *domain.User) error {
	var v struct {
		ProductID string `json:"product_id"`
		Campus    string `json:"campus"`
		Idem      string `json:"idempotency_key"`
	}
	if err := decode(r, &v); err != nil {
		return err
	}
	o, created, err := s.deps.Orders.Create(r.Context(), u, domain.CreateOrderInput{ProductID: v.ProductID, Campus: v.Campus, Idem: v.Idem})
	if err != nil {
		return err
	}
	status := http.StatusOK
	if created {
		status = http.StatusCreated
	}
	return result(w, status, o)
}

func (s *Server) orders(w http.ResponseWriter, r *http.Request, u *domain.User) error {
	items, err := s.deps.Orders.List(r.Context(), u)
	if err != nil {
		return err
	}
	return result(w, 200, map[string]any{"items": items})
}

func (s *Server) getOrder(w http.ResponseWriter, r *http.Request, u *domain.User) error {
	o, err := s.deps.Orders.Get(r.Context(), u, r.PathValue("id"))
	if err != nil {
		return err
	}
	return result(w, 200, o)
}

func (s *Server) cancelOrder(w http.ResponseWriter, r *http.Request, u *domain.User) error {
	o, err := s.deps.Orders.Cancel(r.Context(), u, r.PathValue("id"))
	if err != nil {
		return err
	}
	return result(w, 200, o)
}

func (s *Server) handover(w http.ResponseWriter, r *http.Request, u *domain.User) error {
	o, err := s.deps.Orders.Handover(r.Context(), u, r.PathValue("id"))
	if err != nil {
		return err
	}
	return result(w, 200, o)
}

func (s *Server) complete(w http.ResponseWriter, r *http.Request, u *domain.User) error {
	o, err := s.deps.Orders.Complete(r.Context(), u, r.PathValue("id"))
	if err != nil {
		return err
	}
	return result(w, 200, o)
}

func (s *Server) refund(w http.ResponseWriter, r *http.Request, u *domain.User) error {
	var v struct {
		Reason string `json:"reason"`
	}
	if err := decode(r, &v); err != nil {
		return err
	}
	o, err := s.deps.Orders.Refund(r.Context(), u, r.PathValue("id"), v.Reason)
	if err != nil {
		return err
	}
	return result(w, 200, o)
}

func (s *Server) startPayment(w http.ResponseWriter, r *http.Request, u *domain.User) error {
	var v struct {
		Method string `json:"method"`
		Mobile bool   `json:"mobile"`
	}
	if err := decode(r, &v); err != nil {
		return err
	}
	out, err := s.deps.Payments.Start(r.Context(), u, r.PathValue("id"), v.Method, v.Mobile)
	if err != nil {
		return err
	}
	return result(w, 200, out)
}
