package httpapi

import (
	"encoding/json"
	"fmt"
	"net/http"
	"time"

	"qingfly/edu-mall/internal/domain"
	"qingfly/edu-mall/internal/service"
)

func (s *Server) agentCatalogRoutes() {
	s.route("GET /api/v1/agents", true, s.publicAgents)
	s.admin("GET /api/v1/admin/agent-catalog", s.adminCatalog)
	s.admin("POST /api/v1/admin/capabilities", s.createCapability)
	s.admin("PUT /api/v1/admin/capabilities/{id}", s.updateCapability)
	s.admin("DELETE /api/v1/admin/capabilities/{id}", s.deleteCapability)
	s.admin("POST /api/v1/admin/agents", s.createAgent)
	s.admin("PUT /api/v1/admin/agents/{id}", s.updateAgent)
	s.admin("DELETE /api/v1/admin/agents/{id}", s.archiveAgent)
	s.admin("POST /api/v1/admin/agents/{id}/publish", s.publishAgent)
	s.admin("GET /api/v1/admin/agents/{id}/versions", s.agentVersions)
	s.admin("GET /api/v1/admin/mcp/{id}/tools", s.discoverMCPTools)
	s.admin("GET /api/v1/admin/agent-audit", s.agentAudit)
}

func (s *Server) agentThreads(w http.ResponseWriter, r *http.Request, u *domain.User) error {
	items, err := s.deps.AgentRun.Threads(r.Context(), u)
	if err != nil {
		return err
	}
	return result(w, 200, map[string]any{"items": items})
}

func (s *Server) agentHistory(w http.ResponseWriter, r *http.Request, u *domain.User) error {
	out, err := s.deps.AgentRun.History(r.Context(), u, r.PathValue("id"))
	if err != nil {
		return err
	}
	return result(w, 200, out)
}

func streamEvent(w http.ResponseWriter, kind string, data any) error {
	b, err := json.Marshal(data)
	if err != nil {
		return err
	}
	if _, err = fmt.Fprintf(w, "event: %s\ndata: %s\n\n", kind, b); err != nil {
		return err
	}
	return http.NewResponseController(w).Flush()
}

func (s *Server) agentRun(w http.ResponseWriter, r *http.Request, u *domain.User) error {
	var v struct {
		AgentID  string `json:"agent_id"`
		Prompt   string `json:"prompt"`
		ThreadID string `json:"thread_id"`
		Idem     string `json:"idempotency_key"`
	}
	if err := decode(r, &v); err != nil {
		return err
	}
	started := false
	sink := func(ev domain.AgentEvent) error {
		if !started {
			started = true
			w.Header().Set("Content-Type", "text/event-stream; charset=utf-8")
			w.Header().Set("X-Accel-Buffering", "no")
			_ = http.NewResponseController(w).SetWriteDeadline(time.Now().Add(2 * time.Minute))
		}
		return streamEvent(w, ev.Type, ev.Data)
	}
	err := s.deps.AgentRun.Stream(r.Context(), u, domain.AgentRunInput{AgentID: v.AgentID, Prompt: v.Prompt, ThreadID: v.ThreadID, Idem: v.Idem}, sink)
	if err != nil && !started {
		return err
	}
	return nil
}

func (s *Server) publicAgents(w http.ResponseWriter, r *http.Request, _ *domain.User) error {
	items, err := s.deps.AgentCatalog.Public(r.Context())
	if err != nil {
		return err
	}
	return result(w, 200, map[string]any{"items": items})
}

func (s *Server) adminCatalog(w http.ResponseWriter, r *http.Request, _ *domain.User) error {
	out, err := s.deps.AgentCatalog.AdminCatalog(r.Context())
	if err != nil {
		return err
	}
	return result(w, 200, out)
}

func (s *Server) createCapability(w http.ResponseWriter, r *http.Request, u *domain.User) error {
	var v struct {
		Kind        string                  `json:"kind"`
		Name        string                  `json:"name"`
		Description string                  `json:"description"`
		Config      domain.CapabilityConfig `json:"config"`
		Enabled     bool                    `json:"enabled"`
		Revision    int                     `json:"revision"`
	}
	if err := decode(r, &v); err != nil {
		return err
	}
	c, err := s.deps.AgentCatalog.CreateCapability(r.Context(), u, service.CapabilityInput{Kind: v.Kind, Name: v.Name, Description: v.Description, Config: v.Config, Enabled: v.Enabled, Revision: v.Revision})
	if err != nil {
		return err
	}
	return result(w, 201, c)
}

func (s *Server) updateCapability(w http.ResponseWriter, r *http.Request, u *domain.User) error {
	var v struct {
		Kind        string                  `json:"kind"`
		Name        string                  `json:"name"`
		Description string                  `json:"description"`
		Config      domain.CapabilityConfig `json:"config"`
		Enabled     bool                    `json:"enabled"`
		Revision    int                     `json:"revision"`
	}
	if err := decode(r, &v); err != nil {
		return err
	}
	c, err := s.deps.AgentCatalog.UpdateCapability(r.Context(), u, r.PathValue("id"), service.CapabilityInput{Kind: v.Kind, Name: v.Name, Description: v.Description, Config: v.Config, Enabled: v.Enabled, Revision: v.Revision})
	if err != nil {
		return err
	}
	return result(w, 200, c)
}

func (s *Server) deleteCapability(w http.ResponseWriter, r *http.Request, u *domain.User) error {
	if err := s.deps.AgentCatalog.DeleteCapability(r.Context(), u, r.PathValue("id")); err != nil {
		return err
	}
	return result(w, 200, map[string]bool{"deleted": true})
}

func (s *Server) createAgent(w http.ResponseWriter, r *http.Request, u *domain.User) error {
	var v struct {
		domain.AgentDraft
		Revision int `json:"revision"`
	}
	if err := decode(r, &v); err != nil {
		return err
	}
	d, err := s.deps.AgentCatalog.CreateAgent(r.Context(), u, service.AgentInput{Draft: v.AgentDraft, Revision: v.Revision})
	if err != nil {
		return err
	}
	return result(w, 201, d)
}

func (s *Server) updateAgent(w http.ResponseWriter, r *http.Request, u *domain.User) error {
	var v struct {
		domain.AgentDraft
		Revision int `json:"revision"`
	}
	if err := decode(r, &v); err != nil {
		return err
	}
	d, err := s.deps.AgentCatalog.UpdateAgent(r.Context(), u, r.PathValue("id"), service.AgentInput{Draft: v.AgentDraft, Revision: v.Revision})
	if err != nil {
		return err
	}
	return result(w, 200, d)
}

func (s *Server) archiveAgent(w http.ResponseWriter, r *http.Request, u *domain.User) error {
	var v struct {
		Revision int `json:"revision"`
	}
	if err := decode(r, &v); err != nil {
		return err
	}
	if err := s.deps.AgentCatalog.ArchiveAgent(r.Context(), u, r.PathValue("id"), v.Revision); err != nil {
		return err
	}
	return result(w, 200, map[string]bool{"archived": true})
}

func (s *Server) publishAgent(w http.ResponseWriter, r *http.Request, u *domain.User) error {
	var v struct {
		Revision      int `json:"revision"`
		SourceVersion int `json:"source_version"`
	}
	if err := decode(r, &v); err != nil {
		return err
	}
	d, err := s.deps.AgentCatalog.PublishAgent(r.Context(), u, r.PathValue("id"), v.Revision, v.SourceVersion)
	if err != nil {
		return err
	}
	return result(w, 200, d)
}

func (s *Server) agentVersions(w http.ResponseWriter, r *http.Request, _ *domain.User) error {
	items, err := s.deps.AgentCatalog.Versions(r.Context(), r.PathValue("id"))
	if err != nil {
		return err
	}
	return result(w, 200, map[string]any{"items": items})
}

func (s *Server) discoverMCPTools(w http.ResponseWriter, r *http.Request, _ *domain.User) error {
	items, err := s.deps.AgentCatalog.DiscoverMCPTools(r.Context(), r.PathValue("id"))
	if err != nil {
		return err
	}
	return result(w, 200, map[string]any{"items": items})
}

func (s *Server) agentAudit(w http.ResponseWriter, r *http.Request, _ *domain.User) error {
	items, err := s.deps.AgentCatalog.Audit(r.Context())
	if err != nil {
		return err
	}
	return result(w, 200, map[string]any{"items": items})
}
