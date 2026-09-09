// Package server exposes the WebSocket connection layer's management surface over
// HTTP (the three gRPC-facing operations as REST for demo). Real deployment serves
// these via go-zero zrpc and terminates WS at OpenResty+Nchan; this keeps the routing
// logic runnable offline.
package server

import (
	"context"
	"encoding/json"
	"net/http"
	"time"

	"qingfly/edu-mall/internal/pkg/response"
	"qingfly/edu-mall/ws/hub"
)

// MemorySink logs deliveries (offline stand-in for Nchan/gorilla writer).
type MemorySink struct{}

func (MemorySink) Deliver(_ context.Context, connID string, payload []byte) error {
	return nil // in prod: write to the live WS conn / nchan channel
}

// Server wraps a Hub with HTTP handlers.
type Server struct{ hub *hub.Hub }

// New builds the server.
func New(h *hub.Hub) *Server { return &Server{hub: h} }

// Routes returns path->handler map.
func (s *Server) Routes() map[string]http.HandlerFunc {
	return map[string]http.HandlerFunc{
		"POST /api/v1/ws/connect":  s.connect,
		"POST /api/v1/ws/disconnect": s.disconnect,
		"GET /api/v1/ws/online":   s.online,
		"POST /api/v1/ws/send":     s.send,
	}
}

// connect: POST {"user_id":"u1","conn_id":"c1","node_id":"n1"} — first-packet auth happens upstream.
func (s *Server) connect(w http.ResponseWriter, r *http.Request) {
	var c hub.Conn
	if err := json.NewDecoder(r.Body).Decode(&c); err != nil || c.UserID == "" || c.ConnID == "" {
		response.Write(w, response.ErrBadRequest, nil)
		return
	}
	if c.NodeID == "" {
		c.NodeID = "local"
	}
	s.hub.Register(r.Context(), c)
	response.JSON(w, map[string]string{"status": "online"})
}

// disconnect: POST {"conn_id":"c1"} (user inferred not needed; hub tracks by conn).
func (s *Server) disconnect(w http.ResponseWriter, r *http.Request) {
	var req struct {
		UserID string `json:"user_id"`
		ConnID string `json:"conn_id"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil || req.ConnID == "" {
		response.Write(w, response.ErrBadRequest, nil)
		return
	}
	s.hub.Unregister(r.Context(), hub.Conn{UserID: req.UserID, ConnID: req.ConnID})
	response.JSON(w, map[string]string{"status": "offline"})
}

// online: GET ?user=u1 -> isOnline + global counts.
func (s *Server) online(w http.ResponseWriter, r *http.Request) {
	user := r.URL.Query().Get("user")
	conns, users := s.hub.OnlineCount()
	out := map[string]interface{}{"conns": conns, "users": users}
	if user != "" {
		out["is_online"] = s.hub.IsOnline(user)
	}
	response.JSON(w, out)
}

// send: POST {"user_id":"u1","payload":"..."} -> fan-out to all that user's conns.
func (s *Server) send(w http.ResponseWriter, r *http.Request) {
	var req struct {
		UserID string `json:"user_id"`
		Payload string `json:"payload"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil || req.UserID == "" {
		response.Write(w, response.ErrBadRequest, nil)
		return
	}
	n, err := s.hub.SendToUser(r.Context(), req.UserID, "", []byte(req.Payload))
	if err != nil {
		response.Write(w, response.ErrInternal, err.Error())
		return
	}
	response.JSON(w, map[string]int{"delivered_to": n})
}

var _ = time.Second
