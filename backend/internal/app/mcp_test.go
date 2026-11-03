package app

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

	"qingfly/edu-mall/internal/domain"
	"qingfly/edu-mall/internal/pkg/toolschema"
)

func mcpFixture(t *testing.T, a *App, sse bool, hooks ...func(string) bool) (*atomic.Int32, *atomic.Int32) {
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
	a.SetMCPServers(map[string]domain.MCPServer{"campus": {URL: srv.URL, Token: "fixture-secret"}})
	return calls, headers
}

func TestMCPProtocolsSchemaAndCredentials(t *testing.T) {
	for _, sse := range []bool{false, true} {
		t.Run(fmt.Sprintf("sse=%t", sse), func(t *testing.T) {
			a, _ := testApp(t)
			calls, headers := mcpFixture(t, a, sse)
			tools, e := a.Agents.ListMCPTools(context.Background(), "campus")
			if e != nil || len(tools) != 1 {
				t.Fatalf("discover %v %v", tools, e)
			}
			cap := domain.AgentCapability{Kind: "mcp", Config: domain.CapabilityConfig{ServerID: "campus", Tool: "campus_hours", ReadOnly: true, InputSchema: tools[0].InputSchema}}
			out, e := a.Agents.CallMCPTool(context.Background(), cap, `{"query":"library"}`)
			if e != nil || out != "图书馆开放到22点" {
				t.Fatalf("call %q %v", out, e)
			}
			a.Agents.CallMCPTool(context.Background(), cap, `{"query":1}`)
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
	if _, e := toolschema.Compile(json.RawMessage(`{"type":"object","properties":{"x":{"$ref":"http://127.0.0.1/secrets"}}}`)); e == nil {
		t.Fatal("external schema reference accepted")
	}
	a, _ := testApp(t)
	destinationCalls := &atomic.Int32{}
	destination := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { destinationCalls.Add(1) }))
	defer destination.Close()
	redirect := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { http.Redirect(w, r, destination.URL, 307) }))
	defer redirect.Close()
	a.SetMCPServers(map[string]domain.MCPServer{"redirect": {URL: redirect.URL, Token: "secret"}})
	if _, e := a.Agents.ListMCPTools(context.Background(), "redirect"); e == nil {
		t.Fatal("redirect accepted")
	}
	if destinationCalls.Load() != 0 {
		t.Fatal("credentials sent to redirect target")
	}
}

func TestPublishedAgentCallsOnlyBoundMCPTool(t *testing.T) {
	a, _ := testApp(t)
	admin := catalogAdmin(t, a)
	buyer := account(t, a, "buyer")
	calls, _ := mcpFixture(t, a, true)
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
	a.SetDefaultModel(domain.ModelProvider{BaseURL: model.URL, APIKey: "local-model-secret", DefaultModel: "fixture-model"})
	body, _ := json.Marshal(map[string]any{"agent_id": aid, "prompt": "图书馆何时关门", "idempotency_key": "mcp-agent-run-key"})
	r := httptest.NewRequest("POST", "/api/v1/agent/runs", bytes.NewReader(body))
	r.AddCookie(buyer.cookie)
	w := httptest.NewRecorder()
	a.Handler().ServeHTTP(w, r)
	if w.Code != 200 || !strings.Contains(w.Body.String(), `"status":"completed"`) {
		t.Fatalf("agent MCP run %d %s", w.Code, w.Body.String())
	}
	if calls.Load() != 1 {
		t.Fatal("MCP tool was not called exactly once")
	}
	var count int
	a.DB.QueryRow("SELECT COUNT(*) FROM agent_tool_events").Scan(&count)
	if count != 2 {
		t.Fatalf("missing tool audit: %d", count)
	}
	var snap domain.AgentSnapshot
	var raw string
	a.DB.QueryRow("SELECT snapshot FROM agent_run_snapshots").Scan(&raw)
	json.Unmarshal([]byte(raw), &snap)
	if strings.Contains(raw, "secret") {
		t.Fatal("credential persisted in snapshot")
	}
	bad := domain.ToolCall{}
	bad.Function.Name = "my_orders"
	bad.Function.Arguments = "{}"
	if _, e := a.AgentRun.ExecuteBoundTool(context.Background(), &domain.User{ID: "any"}, "unused", snap, bad); e == nil {
		t.Fatal("unbound tool allowed")
	}
	a.DB.Exec("UPDATE agent_capabilities SET enabled=0 WHERE id=?", cap["id"])
	if e := a.AgentRun.CheckRunEnabled(context.Background(), snap); e == nil {
		t.Fatal("disabled capability still usable")
	}
}

func fixtureToolRun(t *testing.T, a *App, c domain.AgentCapability) (domain.AgentSnapshot, *domain.User, string) {
	t.Helper()
	u := account(t, a, "tooluser")
	_, me := u.call("GET", "/api/v1/auth/me", nil)
	uid := me["id"].(string)
	raw, _ := json.Marshal(c.Config)
	if _, e := a.DB.Exec("INSERT INTO agent_capabilities VALUES(?,?,?,?,?,1,1,?)", c.ID, c.Kind, c.Name, c.Description, string(raw), now()); e != nil {
		t.Fatal(e)
	}
	if _, e := a.DB.Exec("INSERT INTO agent_threads VALUES('tool-thread',?,'tools',?)", uid, now()); e != nil {
		t.Fatal(e)
	}
	if _, e := a.DB.Exec("INSERT INTO agent_runs(id,thread_id,user_id,idem,prompt,status,created_at) VALUES('tool-run','tool-thread',?,'tool-fixture-key','test','running',?)", uid, now()); e != nil {
		t.Fatal(e)
	}
	snap, e := a.Agents.Snapshot(context.Background(), domain.DefaultAgentID)
	if e != nil {
		t.Fatal(e)
	}
	snap.Tools = []domain.AgentCapability{c}
	return snap, &domain.User{ID: uid}, "tool-run"
}

func TestMCPReportedErrorIsAuditedFailed(t *testing.T) {
	a, _ := testApp(t)
	mcpFixture(t, a, false, func(method string) bool { return method == "tools/call" })
	tools, e := a.Agents.ListMCPTools(context.Background(), "campus")
	if e != nil {
		t.Fatal(e)
	}
	c := domain.AgentCapability{ID: "cap_fixture", Kind: "mcp", Name: "test", Config: domain.CapabilityConfig{ServerID: "campus", Tool: "campus_hours", ReadOnly: true, InputSchema: tools[0].InputSchema}}
	snap, u, rid := fixtureToolRun(t, a, c)
	call := domain.ToolCall{}
	call.Function.Name = "tool_" + c.ID
	call.Function.Arguments = `{"query":"library"}`
	_, e = a.AgentRun.ExecuteBoundTool(context.Background(), u, rid, snap, call)
	if e == nil {
		t.Fatal("remote isError reported as success")
	}
	var status string
	a.DB.QueryRow("SELECT status FROM agent_tool_events ORDER BY id DESC LIMIT 1").Scan(&status)
	if status != "failed" {
		t.Fatalf("wrong tool audit %s", status)
	}
}

func TestMCPDisabledDuringDiscoveryNeverCallsTool(t *testing.T) {
	a, _ := testApp(t)
	armed := &atomic.Bool{}
	started, release := make(chan bool), make(chan bool)
	calls, _ := mcpFixture(t, a, false, func(method string) bool {
		if method == "tools/list" && armed.Load() {
			close(started)
			<-release
		}
		return false
	})
	tools, e := a.Agents.ListMCPTools(context.Background(), "campus")
	if e != nil {
		t.Fatal(e)
	}
	c := domain.AgentCapability{ID: "cap_fixture", Kind: "mcp", Name: "test", Config: domain.CapabilityConfig{ServerID: "campus", Tool: "campus_hours", ReadOnly: true, InputSchema: tools[0].InputSchema}}
	snap, u, rid := fixtureToolRun(t, a, c)
	call := domain.ToolCall{}
	call.Function.Name = "tool_" + c.ID
	call.Function.Arguments = `{"query":"library"}`
	armed.Store(true)
	done := make(chan error, 1)
	go func() { _, e := a.AgentRun.ExecuteBoundTool(context.Background(), u, rid, snap, call); done <- e }()
	<-started
	a.DB.Exec("UPDATE agent_capabilities SET enabled=0 WHERE id=?", c.ID)
	close(release)
	if e := <-done; e == nil {
		t.Fatal("disabled capability invoked after discovery")
	}
	if calls.Load() != 0 {
		t.Fatal("remote tool ran after revocation")
	}
}
