// Package handler is the API layer for the Agent service (Part 4 of the course).
// The run endpoint streams SSE events (thought / tool_call / observation / final) as
// they happen, matching the course's "SSE 流式输出" requirement.
package handler

import (
	"encoding/json"
	"net/http"

	"qingfly/edu-mall/agent/engine"
	"qingfly/edu-mall/internal/pkg/response"
)

// Agent exposes Agent HTTP endpoints.
type Agent struct{ eng *engine.Engine }

// New builds the Agent handler set.
func New(eng *engine.Engine) *Agent { return &Agent{eng: eng} }

// Routes returns route -> handler map.
func (a *Agent) Routes() map[string]http.HandlerFunc {
	return map[string]http.HandlerFunc{
		"/v1/agent/run": a.run,
	}
}

type runReq struct {
	RunID   string   `json:"run_id"`
	AgentID string   `json:"agent_id"`
	Prompt  string   `json:"prompt"`
	Scopes  []string `json:"scopes"` // principal scopes for per-tool policy checks
}

type scoper struct{ scopes []string }

func (s scoper) Scopes() []string { return s.scopes }

// run streams an agent run as SSE.
func (a *Agent) run(w http.ResponseWriter, r *http.Request) {
	var req runReq
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil || req.Prompt == "" || req.RunID == "" {
		response.Write(w, response.ErrBadRequest, nil)
		return
	}
	w.Header().Set("Content-Type", "text/event-stream")
	w.Header().Set("Cache-Control", "no-cache")
	w.Header().Set("Connection", "keep-alive")
	fl, ok := w.(http.Flusher)
	if !ok {
		http.Error(w, "streaming unsupported", http.StatusInternalServerError)
		return
	}
	res, err := a.eng.Run(r.Context(), engine.RunRequest{
		RunID: req.RunID, AgentID: req.AgentID, Prompt: req.Prompt, Caller: scoper{scopes: req.Scopes},
	}, w)
	if err != nil {
		return // error already surfaced via stream events inside engine
	}
	_ = json.NewEncoder(w).Encode(map[string]interface{}{"done": true, "result": res})
	fl.Flush()
}
