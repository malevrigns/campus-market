package platform

import (
	"context"
	"encoding/json"
	"errors"
	"time"
)

type toolResultError struct{ message string }

func (e *toolResultError) Error() string { return e.message }

func capabilityFunction(c AgentCapability) string { return "tool_" + c.ID }
func toolSchema(c AgentCapability) json.RawMessage {
	if c.Kind == "mcp" {
		return c.Config.InputSchema
	}
	if c.Config.Tool == "search_products" {
		return json.RawMessage(`{"type":"object","properties":{"query":{"type":"string","maxLength":100,"description":"短关键词；空字符串浏览所有好物"}},"required":["query"],"additionalProperties":false}`)
	}
	return json.RawMessage(`{"type":"object","properties":{},"additionalProperties":false}`)
}
func snapshotToolDefinitions(snap AgentSnapshot) []map[string]any {
	out := []map[string]any{}
	for _, c := range snap.Tools {
		out = append(out, map[string]any{"type": "function", "function": map[string]any{"name": capabilityFunction(c), "description": c.Name + "：" + c.Description, "parameters": toolSchema(c)}})
	}
	return out
}
func boundToolLabel(snap AgentSnapshot, name string) string {
	for _, c := range snap.Tools {
		if capabilityFunction(c) == name {
			return c.Name
		}
	}
	return "未授权工具"
}
func (s *Server) checkRunEnabled(ctx context.Context, snap AgentSnapshot) error {
	var enabled bool
	var archived bool
	if e := s.db.QueryRowContext(ctx, "SELECT enabled,archived FROM agent_definitions WHERE id=?", snap.AgentID).Scan(&enabled, &archived); e != nil || !enabled || archived {
		return errors.New("agent disabled")
	}
	for _, c := range append([]AgentCapability{snap.Model}, snap.Tools...) {
		if e := s.db.QueryRowContext(ctx, "SELECT enabled FROM agent_capabilities WHERE id=?", c.ID).Scan(&enabled); e != nil || !enabled {
			return errors.New("bound capability disabled")
		}
	}
	return nil
}
func (s *Server) executeBoundTool(ctx context.Context, u *User, runID string, snap AgentSnapshot, call toolCall) (string, error) {
	if e := s.checkRunEnabled(ctx, snap); e != nil {
		return "", e
	}
	var bound *AgentCapability
	for i := range snap.Tools {
		if capabilityFunction(snap.Tools[i]) == call.Function.Name {
			bound = &snap.Tools[i]
			break
		}
	}
	if bound == nil {
		return "", errors.New("tool not bound to this published agent")
	}
	if _, e := s.db.ExecContext(ctx, "INSERT INTO agent_tool_events(run_id,capability_id,name,status,created_at) VALUES(?,?,?,?,?)", runID, bound.ID, bound.Name, "started", now()); e != nil {
		return "", e
	}
	var out string
	var e error
	if _, validationErr := validateToolArgs(toolSchema(*bound), call.Function.Arguments); validationErr != nil {
		e = &toolResultError{"参数不符合工具 schema"}
	} else if bound.Kind == "mcp" {
		out, e = s.callMCPTool(ctx, *bound, call.Function.Arguments, func() error { return s.checkRunEnabled(ctx, snap) })
	} else {
		call.Function.Name = bound.Config.Tool
		out, e = s.readTool(ctx, u, call)
	}
	status := "completed"
	if e != nil {
		status = "failed"
	}
	saveCtx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	if _, saveErr := s.db.ExecContext(saveCtx, "INSERT INTO agent_tool_events(run_id,capability_id,name,status,created_at) VALUES(?,?,?,?,?)", runID, bound.ID, bound.Name, status, now()); saveErr != nil && e == nil {
		e = saveErr
	}
	return out, e
}
