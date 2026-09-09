package platform

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
)

func mcpFixture(t *testing.T, s *Server, sse bool, hooks ...func(string) bool) (*atomic.Int32, *atomic.Int32) {
	t.Helper()
	calls, headers := &atomic.Int32{}, &atomic.Int32{}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method == "DELETE" {
			w.WriteHeader(204)
			return
		}
		var input struct {
			ID     string          `json:"id"`
			Method string          `json:"method"`
			Params json.RawMessage `json:"params"`
		}
		if e := json.NewDecoder(r.Body).Decode(&input); e != nil {
			t.Error(e)
			w.WriteHeader(400)
			return
		}
		if r.Header.Get("Authorization") != "Bearer fixture-secret" {
			t.Error("MCP missing backend credential")
		}
		if input.Method != "initialize" {
			if r.Header.Get("Mcp-Session-Id") != "session-test" || r.Header.Get("MCP-Protocol-Version") != "2025-06-18" {
				t.Error("MCP session/version missing")
			} else {
				headers.Add(1)
			}
		}
		var result any
		toolError := false
		for _, hook := range hooks {
			if hook(input.Method) {
				toolError = true
			}
		}
		switch input.Method {
		case "initialize":
			w.Header().Set("Mcp-Session-Id", "session-test")
			result = map[string]any{"protocolVersion": "2025-06-18", "capabilities": map[string]any{"tools": map[string]any{}}, "serverInfo": map[string]string{"name": "fixture", "version": "1"}}
		case "notifications/initialized":
			w.WriteHeader(202)
			return
		case "tools/list":
			result = map[string]any{"tools": []any{map[string]any{"name": "campus_hours", "description": "Public opening hours", "inputSchema": map[string]any{"type": "object", "properties": map[string]any{"query": map[string]any{"type": "string"}}, "required": []string{"query"}, "additionalProperties": false}, "annotations": map[string]bool{"readOnlyHint": true}}}}
		case "tools/call":
			calls.Add(1)
			var args struct {
				Name      string         `json:"name"`
				Arguments map[string]any `json:"arguments"`
			}
			json.Unmarshal(input.Params, &args)
			if args.Name != "campus_hours" || args.Arguments["query"] != "library" {
				t.Errorf("unexpected MCP call %s", input.Params)
			}
			result = map[string]any{"content": []any{map[string]string{"type": "text", "text": "图书馆开放到22点"}}, "isError": toolError}
		default:
			t.Errorf("unexpected method %s", input.Method)
			w.WriteHeader(400)
			return
		}
		body, _ := json.Marshal(map[string]any{"jsonrpc": "2.0", "id": input.ID, "result": result})
		if sse {
			w.Header().Set("Content-Type", "text/event-stream")
			fmt.Fprint(w, "event: message\ndata: {\"jsonrpc\":\"2.0\",\"method\":\"notifications/progress\",\"params\":{}}\r\n\r\n")
			fmt.Fprintf(w, "event: message\r\ndata: %s\r\n\r\n", body)
		} else {
			w.Header().Set("Content-Type", "application/json")
			w.Write(body)
		}
	}))
	t.Cleanup(srv.Close)
	s.cfg.MCPServers = map[string]MCPServer{"campus": {URL: srv.URL, Token: "fixture-secret"}}
	return calls, headers
}
func TestMCPProtocolsSchemaAndCredentials(t *testing.T) {
	for _, sse := range []bool{false, true} {
		t.Run(fmt.Sprintf("sse=%t", sse), func(t *testing.T) {
			s, _ := testApp(t)
			calls, headers := mcpFixture(t, s, sse)
			tools, e := s.listMCPTools(context.Background(), "campus")
			if e != nil || len(tools) != 1 {
				t.Fatalf("discover %v %v", tools, e)
			}
			cap := AgentCapability{Kind: "mcp", Config: CapabilityConfig{ServerID: "campus", Tool: "campus_hours", ReadOnly: true, InputSchema: tools[0].InputSchema}}
			out, e := s.callMCPTool(context.Background(), cap, `{"query":"library"}`)
			if e != nil || out != "图书馆开放到22点" {
				t.Fatalf("call %q %v", out, e)
			}
			s.callMCPTool(context.Background(), cap, `{"query":1}`)
			if calls.Load() != 1 {
				t.Fatal("invalid schema reached remote tool")
			}
			if headers.Load() < 4 {
				t.Fatal("session headers not exercised")
			}
		})
	}
}
func TestMCPExternalSchemaAndRedirectRejected(t *testing.T) {
	if _, e := compileToolSchema(json.RawMessage(`{"type":"object","properties":{"x":{"$ref":"http://127.0.0.1/secrets"}}}`)); e == nil {
		t.Fatal("external schema reference accepted")
	}
	s, _ := testApp(t)
	destinationCalls := &atomic.Int32{}
	destination := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { destinationCalls.Add(1) }))
	defer destination.Close()
	redirect := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { http.Redirect(w, r, destination.URL, 307) }))
	defer redirect.Close()
	s.cfg.MCPServers = map[string]MCPServer{"redirect": {URL: redirect.URL, Token: "secret"}}
	if _, e := s.listMCPTools(context.Background(), "redirect"); e == nil {
		t.Fatal("redirect accepted")
	}
	if destinationCalls.Load() != 0 {
		t.Fatal("credentials sent to redirect target")
	}
}
func TestPublishedAgentCallsOnlyBoundMCPTool(t *testing.T) {
	s, _ := testApp(t)
	admin := catalogAdmin(t, s)
	buyer := account(t, s, "buyer")
	calls, _ := mcpFixture(t, s, true)
	status, cap := admin.call("POST", "/api/v1/admin/capabilities", map[string]any{"kind": "mcp", "name": "图书馆时间", "description": "公开开放时间", "enabled": true, "config": map[string]any{"server_id": "campus", "tool": "campus_hours", "read_only": true}})
	if status != 201 {
		t.Fatalf("create MCP capability %d %v", status, cap)
	}
	draft := draftBody()
	draft["tool_capability_ids"] = []string{cap["id"].(string)}
	_, def := admin.call("POST", "/api/v1/admin/agents", draft)
	aid := def["id"].(string)
	admin.call("POST", "/api/v1/admin/agents/"+aid+"/publish", map[string]any{"revision": def["revision"]})
	turns := &atomic.Int32{}
	model := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var payload map[string]any
		if e := json.NewDecoder(r.Body).Decode(&payload); e != nil {
			t.Error(e)
		}
		if len(payload["tools"].([]any)) != 1 {
			t.Error("unbound tools exposed")
		}
		w.Header().Set("Content-Type", "text/event-stream")
		if turns.Add(1) == 1 {
			delta := map[string]any{"tool_calls": []any{map[string]any{"index": 0, "id": "call1", "type": "function", "function": map[string]string{"name": "tool_" + cap["id"].(string), "arguments": `{"query":"library"}`}}}}
			frame, _ := json.Marshal(map[string]any{"choices": []any{map[string]any{"delta": delta, "finish_reason": "tool_calls"}}})
			fmt.Fprintf(w, "data: %s\n\ndata: [DONE]\n\n", frame)
		} else {
			all, _ := json.Marshal(payload["messages"])
			if !strings.Contains(string(all), "图书馆开放到22点") {
				t.Error("missing tool observation")
			}
			fmt.Fprint(w, "data: {\"choices\":[{\"delta\":{\"content\":\"图书馆22点闭馆。\"},\"finish_reason\":\"stop\"}]}\n\ndata: [DONE]\n\n")
		}
	}))
	defer model.Close()
	s.cfg.ModelURL = model.URL
	s.cfg.ModelKey = "local-model-secret"
	s.cfg.ModelName = "fixture-model"
	body, _ := json.Marshal(map[string]any{"agent_id": aid, "prompt": "图书馆何时关门", "idempotency_key": "mcp-agent-run-key"})
	r := httptest.NewRequest("POST", "/api/v1/agent/runs", bytes.NewReader(body))
	r.AddCookie(buyer.cookie)
	w := httptest.NewRecorder()
	s.Handler().ServeHTTP(w, r)
	if w.Code != 200 || !strings.Contains(w.Body.String(), `"status":"completed"`) {
		t.Fatalf("agent MCP run %d %s", w.Code, w.Body.String())
	}
	if calls.Load() != 1 {
		t.Fatal("MCP tool was not called exactly once")
	}
	var count int
	s.db.QueryRow("SELECT COUNT(*) FROM agent_tool_events").Scan(&count)
	if count != 2 {
		t.Fatalf("missing tool audit: %d", count)
	}
	var snap AgentSnapshot
	var raw string
	s.db.QueryRow("SELECT snapshot FROM agent_run_snapshots").Scan(&raw)
	json.Unmarshal([]byte(raw), &snap)
	if strings.Contains(raw, "secret") {
		t.Fatal("credential persisted in snapshot")
	}
	bad := toolCall{}
	bad.Function.Name = "my_orders"
	bad.Function.Arguments = "{}"
	if _, e := s.executeBoundTool(context.Background(), &User{ID: "any"}, "unused", snap, bad); e == nil {
		t.Fatal("unbound tool allowed")
	}
	s.db.Exec("UPDATE agent_capabilities SET enabled=0 WHERE id=?", cap["id"])
	if e := s.checkRunEnabled(context.Background(), snap); e == nil {
		t.Fatal("disabled capability still usable")
	}
}
