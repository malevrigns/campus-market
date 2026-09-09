// Package handler is the API (router/api) layer for the mall service. It uses the
// standard library net/http so the core builds offline; swap to Gin in production by
// mirroring these handlers. Every response goes through pkg/response (unified body).
package handler

import (
	"context"
	"encoding/json"
	"net/http"
	"strconv"

	"qingfly/edu-mall/internal/pkg/response"
	"qingfly/edu-mall/mall/model"
	"qingfly/edu-mall/mall/service"
)

// Mall exposes mall HTTP endpoints.
type Mall struct {
	svc    *service.OrderService
	secret []byte // gateway HMAC secret for verifying payment callbacks
}

// New builds the mall handler set.
func New(svc *service.OrderService, secret []byte) *Mall { return &Mall{svc: svc, secret: secret} }

// Routes returns a path->handler map ready to mount on an http.ServeMux.
func (m *Mall) Routes() map[string]http.HandlerFunc {
	return map[string]http.HandlerFunc{
		"POST /api/v1/orders":       m.createOrder,
		"POST /payments/notify":     m.paymentNotify,
		"GET /api/v1/orders/{no}":   m.getOrder, // simplified; real router extracts path param
	}
}

// createOrder: POST /api/v1/orders
// Body: {"user_id":"u1","items":[{"sku_id":"s1","qty":1,"price_ct":99900}],
//        "idempotency_key":"client-uuid"}
func (m *Mall) createOrder(w http.ResponseWriter, r *http.Request) {
	var req struct {
		UserID           string             `json:"user_id"`
		IdempotencyKey   string             `json:"idempotency_key"`
		Items            []model.Item       `json:"items"`
		ServerSidePrices map[string]int64   `json:"server_side_prices"` // authoritative unit price per SKU (cents)
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		response.Write(w, response.ErrBadRequest, nil)
		return
	}
	if req.UserID == "" || len(req.Items) == 0 {
		response.Write(w, response.ErrBadRequest, "user_id and items required")
		return
	}
	o, err := m.svc.CreateOrder(r.Context(), &service.CreateOrderRequest{
		UserID: req.UserID, Items: req.Items, IdempotencyKey: req.IdempotencyKey,
		ServerSidePrices: req.ServerSidePrices,
	})
	if err != nil {
		switch err {
		case service.ErrInsufficientStock:
			response.Write(w, response.Code(40901), "insufficient stock")
		default:
			response.Write(w, response.ErrInternal, err.Error())
		}
		return
	}
	response.JSON(w, o)
}

// paymentNotify: POST /payments/notify — gateway callback (verify sign + amount + idempotent).
func (m *Mall) paymentNotify(w http.ResponseWriter, r *http.Request) {
	var n service.PaymentNotify
	if err := json.NewDecoder(r.Body).Decode(&n); err != nil {
		response.Write(w, response.ErrBadRequest, nil)
		return
	}
	ok, err := m.svc.HandlePaymentNotify(r.Context(), &n, m.secret)
	switch {
	case err == service.ErrBadSign:
		response.Write(w, response.Code(40002), "bad signature")
	case err == service.ErrAmountMismatch:
		response.Write(w, response.Code(40003), "amount mismatch")
	case err != nil && !isIdempotent(err):
		response.Write(w, response.ErrInternal, err.Error())
	default:
		// success or idempotent re-notify => ack so gateway stops retrying
		response.JSON(w, map[string]interface{}{"accepted": ok})
	}
}

// getOrder: GET order by business number (query ?no=).
func (m *Mall) getOrder(w http.ResponseWriter, r *http.Request) {
	no := r.URL.Query().Get("no")
	if no == "" {
		response.Write(w, response.ErrNotFound, "order not found")
		return
	}
	o, err := m.svc.GetByNo(r.Context(), no)
	if err != nil {
		response.Write(w, response.ErrNotFound, "order not found")
		return
	}
	response.JSON(w, o)
}

func isIdempotent(err error) bool { return err == service.ErrAlreadyProcessed }

var _ = context.Background
var _ = strconv.Itoa
