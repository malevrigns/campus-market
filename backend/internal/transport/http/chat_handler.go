package httpapi

import (
	"net/http"
	"strconv"

	"qingfly/edu-mall/internal/domain"
)

func (s *Server) createConversation(w http.ResponseWriter, r *http.Request, u *domain.User) error {
	var v struct {
		ProductID string `json:"product_id"`
	}
	if err := decode(r, &v); err != nil {
		return err
	}
	out, created, err := s.deps.Chat.CreateConversation(r.Context(), u, v.ProductID)
	if err != nil {
		return err
	}
	status := http.StatusOK
	if created {
		status = http.StatusCreated
	}
	return result(w, status, out)
}

func (s *Server) conversations(w http.ResponseWriter, r *http.Request, u *domain.User) error {
	items, err := s.deps.Chat.List(r.Context(), u)
	if err != nil {
		return err
	}
	return result(w, 200, map[string]any{"items": items})
}

func (s *Server) messages(w http.ResponseWriter, r *http.Request, u *domain.User) error {
	after := int64(0)
	if raw := r.URL.Query().Get("after"); raw != "" {
		v, err := strconv.ParseInt(raw, 10, 64)
		if err != nil || v < 0 {
			return domain.Invalid("消息游标无效")
		}
		after = v
	}
	page, err := s.deps.Chat.Messages(r.Context(), u, r.PathValue("id"), after)
	if err != nil {
		return err
	}
	return result(w, 200, page)
}

func (s *Server) sendMessage(w http.ResponseWriter, r *http.Request, u *domain.User) error {
	var v struct {
		ClientMsgID string `json:"client_msg_id"`
		Content     string `json:"content"`
	}
	if err := decode(r, &v); err != nil {
		return err
	}
	m, created, err := s.deps.Chat.Send(r.Context(), u, r.PathValue("id"), v.ClientMsgID, v.Content)
	if err != nil {
		return err
	}
	status := http.StatusOK
	if created {
		status = http.StatusCreated
	}
	return result(w, status, m)
}

func (s *Server) readMessages(w http.ResponseWriter, r *http.Request, u *domain.User) error {
	var v struct {
		Seq int64 `json:"seq"`
	}
	if err := decode(r, &v); err != nil {
		return err
	}
	if err := s.deps.Chat.Read(r.Context(), u, r.PathValue("id"), v.Seq); err != nil {
		return err
	}
	return result(w, 200, domain.ReadState{Read: true})
}
