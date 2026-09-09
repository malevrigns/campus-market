package platform

import (
	"bytes"
	"encoding/json"
	"net/http/httptest"
	"strings"
	"testing"
)

func catalogAdmin(t *testing.T, s *Server) *testClient {
	t.Helper()
	c := account(t, s, "operator")
	_, u := c.call("GET", "/api/v1/auth/me", nil)
	s.cfg.AdminUserIDs = u["id"].(string)
	return c
}
func draftBody() map[string]any {
	return map[string]any{"name": "验货助手", "description": "帮助同学检查相机", "system_prompt": "你是验货助手，请先询问相机型号。", "welcome": "今天想检查什么好物？", "suggestions": []string{"检查相机快门"}, "model_capability_id": "cap_default_model", "tool_capability_ids": []string{"cap_search_products"}, "max_iterations": 3, "max_tokens": 800, "timeout_seconds": 30, "enabled": true}
}
func TestAgentCatalogAuthorizationAndPublishing(t *testing.T) {
	s, _ := testApp(t)
	normal := account(t, s, "normal")
	admin := catalogAdmin(t, s)
	anon := &testClient{t: t, app: s.Handler()}
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
	s, _ := testApp(t)
	admin := catalogAdmin(t, s)
	c := account(t, s, "buyer")
	modelStub(t, s, "stop")
	_, d := admin.call("POST", "/api/v1/admin/agents", draftBody())
	aid := d["id"].(string)
	_, p := admin.call("POST", "/api/v1/admin/agents/"+aid+"/publish", map[string]any{"revision": d["revision"]})
	body := map[string]any{"prompt": "检查我的相机", "agent_id": aid, "idempotency_key": "snapshot-run-key"}
	b, _ := json.Marshal(body)
	r := httptest.NewRequest("POST", "/api/v1/agent/runs", bytes.NewReader(b))
	r.AddCookie(c.cookie)
	w := httptest.NewRecorder()
	s.Handler().ServeHTTP(w, r)
	if w.Code != 200 {
		t.Fatalf("run %d %s", w.Code, w.Body.String())
	}
	var snapshot string
	if e := s.db.QueryRow("SELECT snapshot FROM agent_run_snapshots").Scan(&snapshot); e != nil {
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
	s.db.QueryRow("SELECT snapshot FROM agent_run_snapshots").Scan(&after)
	if after != snapshot {
		t.Fatal("old run snapshot mutated")
	}
}
