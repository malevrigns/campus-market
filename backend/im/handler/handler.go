// Package handler is the API layer for the IM service (net/http; swap to Gin in prod).
package handler

import (
	"encoding/json"
	"net/http"
	"strconv"

	"qingfly/edu-mall/im/model"
	"qingfly/edu-mall/im/service"
	"qingfly/edu-mall/internal/pkg/response"
)

// IM exposes IM HTTP endpoints.
type IM struct{ svc *service.Service }

// New builds the IM handler set.
func New(svc *service.Service) *IM { return &IM{svc: svc} }

// Routes returns path->handler map.
func (h *IM) Routes() map[string]http.HandlerFunc {
	return map[string]http.HandlerFunc{
		"POST /api/v1/conv":        h.createConv,
		"POST /api/v1/messages":    h.send,
		"GET /api/v1/messages/sync": h.sync,
	}
}

// createConv: POST /api/v1/conv {"id":"c1","type":"p2p","members":["u1","u2"]}
func (h *IM) createConv(w http.ResponseWriter, r *http.Request) {
	var req struct {
		ID      string   `json:"id"`
		Type    string   `json:"type"`
		Members []string `json:"members"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil || req.ID == "" {
		response.Write(w, response.ErrBadRequest, nil)
		return
	}
	c, err := h.svc.EnsureConversation(r.Context(), req.ID, req.Type, req.Members)
	if err != nil {
		response.Write(w, response.ErrInternal, err.Error())
		return
	}
	response.JSON(w, c)
}

// send: POST /api/v1/messages {"sender":"u1","conv_id":"c1","client_msg_id":"uuid",
//                               "type":0,"content":"hi"}
func (h *IM) send(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Sender      string `json:"sender"`
		ConvID      string `json:"conv_id"`
		ClientMsgID string `json:"client_msg_id"`
		Type        int    `json:"type"`
		Content     string `json:"content"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil || req.Sender == "" || req.ConvID == "" {
		response.Write(w, response.ErrBadRequest, nil)
		return
	}
	msg, isNew, err := h.svc.SendMessage(r.Context(), req.Sender, req.ConvID, req.ClientMsgID, model.MsgType(req.Type), req.Content)
	if err != nil {
		switch err {
		case service.ErrNotMember:
			response.Write(w, response.ErrForbidden, "not a member")
		case service.ErrDupClientMsg:
			response.JSON(w, map[string]interface{}{"message": msg, "duplicate": true}) // idempotent ack
		default:
			response.Write(w, response.ErrInternal, err.Error())
		}
		return
	}
	response.JSON(w, map[string]interface{}{"message": msg, "duplicate": !isNew})
}

// sync: GET /api/v1/messages/sync?user=u1&conv=c1&after_seq=0&limit=50
func (h *IM) sync(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query()
	user := q.Get("user")
	conv := q.Get("conv")
	afterSeq, _ := strconv.ParseInt(q.Get("after_seq"), 10, 64)
	limit, _ := strconv.Atoi(q.Get("limit"))
	if limit <= 0 {
		limit = 50
	}
	msgs, lastSeq, err := h.svc.Sync(r.Context(), user, conv, afterSeq, limit)
	if err != nil {
		response.Write(w, response.ErrNotFound, "conversation not found")
		return
	}
	response.JSON(w, map[string]interface{}{"messages": msgs, "last_seq": lastSeq})
}
