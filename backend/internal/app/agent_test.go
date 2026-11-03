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
	"time"

	"qingfly/edu-mall/internal/domain"
)

func modelStub(t *testing.T, a *App, reason string) *atomic.Int32 {
	t.Helper()
	calls := &atomic.Int32{}
	up := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls.Add(1)
		w.Header().Set("Content-Type", "text/event-stream")
		fmt.Fprintf(w, "data: {\"choices\":[{\"delta\":{\"content\":\"你好，同学\"},\"finish_reason\":null}]}\n\ndata: {\"choices\":[{\"delta\":{},\"finish_reason\":%q}]}\n\ndata: [DONE]\n\n", reason)
	}))
	t.Cleanup(up.Close)
	a.SetDefaultModel(domain.ModelProvider{BaseURL: up.URL, APIKey: "test-only", DefaultModel: "stub"})
	return calls
}

func catalogAdmin(t *testing.T, a *App) *testClient {
	t.Helper()
	c := account(t, a, "operator")
	a.SetAdminUserIDs(userID(t, c))
	return c
}

func draftBody() map[string]any {
	return map[string]any{"name": "验货助手", "description": "帮助同学检查相机", "system_prompt": "你是验货助手，请先询问相机型号。", "welcome": "今天想检查什么好物？", "suggestions": []string{"检查相机快门"}, "model_capability_id": "cap_default_model", "tool_capability_ids": []string{"cap_search_products"}, "max_iterations": 3, "max_tokens": 800, "timeout_seconds": 30, "enabled": true}
}

func agentRequest(c *testClient, key string) *http.Request {
	b, _ := json.Marshal(map[string]string{"prompt": "推荐一本书", "idempotency_key": key})
	r := httptest.NewRequest("POST", "/api/v1/agent/runs", bytes.NewReader(b))
	r.AddCookie(c.cookie)
	return r
}

func TestAgentCatalogAuthorizationAndPublishing(t *testing.T) {
	a, _ := testApp(t)
	normal := account(t, a, "normal")
	admin := catalogAdmin(t, a)
	anon := &testClient{t: t, app: a.Handler()}
	if status, _ := anon.call("GET", "/api/v1/admin/agent-catalog", nil); status != 401 {
		t.Fatalf("anonymous admin %d", status)
	}
	if status, _ := normal.call("GET", "/api/v1/admin/agent-catalog", nil); status != 403 {
		t.Fatalf("ordinary user admin %d", status)
	}
	status, d := admin.call("POST", "/api/v1/admin/agents", draftBody())
	if status != 201 {
		t.Fatalf("draft %d %v", status, d)
	}
	aid := d["id"].(string)
	status, list := normal.call("GET", "/api/v1/agents", nil)
	if status != 200 || len(list["items"].([]any)) != 1 {
		t.Fatalf("draft was published %d %v", status, list)
	}
	status, p := admin.call("POST", "/api/v1/admin/agents/"+aid+"/publish", map[string]any{"revision": d["revision"]})
	if status != 200 || p["published_version"] != float64(1) {
		t.Fatalf("publish %d %v", status, p)
	}
	_, list = normal.call("GET", "/api/v1/agents", nil)
	if len(list["items"].([]any)) != 2 {
		t.Fatal("new agent not visible immediately")
	}
	edit := draftBody()
	edit["name"] = "新的草稿名称"
	edit["revision"] = p["revision"]
	status, updated := admin.call("PUT", "/api/v1/admin/agents/"+aid, edit)
	if status != 200 {
		t.Fatalf("update %d %v", status, updated)
	}
	if status, _ := admin.call("PUT", "/api/v1/admin/agents/"+aid, edit); status != 409 {
		t.Fatalf("stale edit accepted %d", status)
	}
	_, list = normal.call("GET", "/api/v1/agents", nil)
	raw, _ := json.Marshal(list)
	if strings.Contains(string(raw), "新的草稿名称") {
		t.Fatal("unpublished edit leaked")
	}
	if status, _ := admin.call("DELETE", "/api/v1/admin/capabilities/cap_search_products", nil); status != 409 {
		t.Fatalf("referenced capability deleted %d", status)
	}
	status, _ = admin.call("DELETE", "/api/v1/admin/agents/"+aid, map[string]any{"revision": updated["revision"]})
	if status != 200 {
		t.Fatalf("archive %d", status)
	}
	_, list = normal.call("GET", "/api/v1/agents", nil)
	if len(list["items"].([]any)) != 1 {
		t.Fatal("archived agent visible")
	}
}

func TestAgentRunStoresPublishedSnapshot(t *testing.T) {
	a, _ := testApp(t)
	admin := catalogAdmin(t, a)
	c := account(t, a, "buyer")
	modelStub(t, a, "stop")
	_, d := admin.call("POST", "/api/v1/admin/agents", draftBody())
	aid := d["id"].(string)
	_, p := admin.call("POST", "/api/v1/admin/agents/"+aid+"/publish", map[string]any{"revision": d["revision"]})
	body := map[string]any{"prompt": "检查我的相机", "agent_id": aid, "idempotency_key": "snapshot-run-key"}
	b, _ := json.Marshal(body)
	r := httptest.NewRequest("POST", "/api/v1/agent/runs", bytes.NewReader(b))
	r.AddCookie(c.cookie)
	w := httptest.NewRecorder()
	a.Handler().ServeHTTP(w, r)
	if w.Code != 200 {
		t.Fatalf("run %d %s", w.Code, w.Body.String())
	}
	var snapshot string
	if e := a.DB.QueryRow("SELECT snapshot FROM agent_run_snapshots").Scan(&snapshot); e != nil {
		t.Fatal(e)
	}
	if !strings.Contains(snapshot, "验货助手") {
		t.Fatal("missing immutable snapshot")
	}
	edit := draftBody()
	edit["system_prompt"] = "修改后的提示词"
	edit["revision"] = p["revision"]
	_, changed := admin.call("PUT", "/api/v1/admin/agents/"+aid, edit)
	admin.call("POST", "/api/v1/admin/agents/"+aid+"/publish", map[string]any{"revision": changed["revision"]})
	var after string
	a.DB.QueryRow("SELECT snapshot FROM agent_run_snapshots").Scan(&after)
	if after != snapshot {
		t.Fatal("old run snapshot mutated")
	}
}

func TestAgentTruncationIsNotSuccess(t *testing.T) {
	for _, reason := range []string{"length", "content_filter"} {
		t.Run(reason, func(t *testing.T) {
			a, _ := testApp(t)
			c := account(t, a, "buyer")
			modelStub(t, a, reason)
			w := httptest.NewRecorder()
			a.Handler().ServeHTTP(w, agentRequest(c, "truncated-run"))
			var status string
			if e := a.DB.QueryRow("SELECT status FROM agent_runs").Scan(&status); e != nil {
				t.Fatal(e)
			}
			if status != "failed" {
				t.Fatalf("finish_reason %s saved as %s", reason, status)
			}
		})
	}
}

func TestAgentStaleRunRecovery(t *testing.T) {
	a, _ := testApp(t)
	c := account(t, a, "buyer")
	calls := modelStub(t, a, "stop")
	var uid string
	a.DB.QueryRow("SELECT id FROM users WHERE email='buyer@campus.test'").Scan(&uid)
	a.DB.Exec("INSERT INTO agent_threads VALUES('stale-thread',?,'stale',?)", uid, now()-200000)
	a.DB.Exec("INSERT INTO agent_runs(id,thread_id,user_id,idem,prompt,status,created_at) VALUES('old','stale-thread',?,'stale-run-key','old','running',?)", uid, now()-200000)
	w := httptest.NewRecorder()
	a.Handler().ServeHTTP(w, agentRequest(c, "new-run-key"))
	if w.Code != 200 || calls.Load() != 1 {
		t.Fatalf("stale run blocks recovery: %d %s", w.Code, w.Body.String())
	}
}

type blockingStream struct {
	header  http.Header
	started chan bool
	release chan bool
	once    atomic.Bool
}

func (w *blockingStream) Header() http.Header { return w.header }
func (w *blockingStream) WriteHeader(int)     {}
func (w *blockingStream) Flush()              {}
func (w *blockingStream) Write(b []byte) (int, error) {
	if w.once.CompareAndSwap(false, true) {
		close(w.started)
		<-w.release
	}
	return len(b), nil
}
func TestReplayDoesNotHoldDatabase(t *testing.T) {
	a, _ := testApp(t)
	c := account(t, a, "buyer")
	calls := modelStub(t, a, "stop")
	w := httptest.NewRecorder()
	a.Handler().ServeHTTP(w, agentRequest(c, "replay-agent-key"))
	blocked := &blockingStream{header: http.Header{}, started: make(chan bool), release: make(chan bool)}
	finished := make(chan bool)
	go func() { a.Handler().ServeHTTP(blocked, agentRequest(c, "replay-agent-key")); close(finished) }()
	<-blocked.started
	ctx, cancel := context.WithTimeout(context.Background(), 150*time.Millisecond)
	defer cancel()
	var one int
	e := a.DB.QueryRowContext(ctx, "SELECT 1").Scan(&one)
	close(blocked.release)
	<-finished
	if e != nil {
		t.Fatalf("SSE replay locked database: %v", e)
	}
	if calls.Load() != 1 {
		t.Fatal("replay invokes model twice")
	}
}

func TestAgentHistoryAndToolIdentityIsolation(t *testing.T) {
	a, _ := testApp(t)
	owner := account(t, a, "owner")
	other := account(t, a, "other")
	modelStub(t, a, "stop")
	w := httptest.NewRecorder()
	a.Handler().ServeHTTP(w, agentRequest(owner, "private-thread-key"))
	var tid string
	if e := a.DB.QueryRow("SELECT thread_id FROM agent_runs").Scan(&tid); e != nil {
		t.Fatal(e)
	}
	if status, _ := other.call("GET", "/api/v1/agent/threads/"+tid, nil); status != 404 {
		t.Fatalf("private history disclosed: %d", status)
	}
	if status, _ := other.call("POST", "/api/v1/agent/runs", map[string]any{"prompt": "读取别人的历史", "thread_id": tid, "idempotency_key": "wrong-owner-key"}); status != 404 {
		t.Fatalf("foreign thread accepted: %d", status)
	}
	if status, _ := owner.call("POST", "/api/v1/agent/runs", map[string]any{"prompt": "test", "idempotency_key": "forged-scope-key", "scopes": []string{"admin"}}); status != 400 {
		t.Fatalf("client scopes accepted: %d", status)
	}
	c := domain.ToolCall{}
	c.Function.Name = "my_orders"
	c.Function.Arguments = `{"user_id":"another-user"}`
	out, e := a.AgentRun.ReadTool(context.Background(), &domain.User{ID: "current-user"}, c)
	if e != nil || out != `{"error":"no identity arguments allowed"}` {
		t.Fatalf("tool trusted model identity: %q %v", out, e)
	}
}
