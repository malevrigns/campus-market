package platform

import (
	"context"
	"encoding/json"
	"sync/atomic"
	"testing"
)

func fixtureToolRun(t *testing.T, s *Server, c AgentCapability) (AgentSnapshot, *User, string) {
	t.Helper()
	u := account(t, s, "tooluser")
	_, me := u.call("GET", "/api/v1/auth/me", nil)
	uid := me["id"].(string)
	raw, _ := json.Marshal(c.Config)
	if _, e := s.db.Exec("INSERT INTO agent_capabilities VALUES(?,?,?,?,?,1,1,?)", c.ID, c.Kind, c.Name, c.Description, string(raw), now()); e != nil {
		t.Fatal(e)
	}
	if _, e := s.db.Exec("INSERT INTO agent_threads VALUES('tool-thread',?,'tools',?)", uid, now()); e != nil {
		t.Fatal(e)
	}
	if _, e := s.db.Exec("INSERT INTO agent_runs(id,thread_id,user_id,idem,prompt,status,created_at) VALUES('tool-run','tool-thread',?,'tool-fixture-key','test','running',?)", uid, now()); e != nil {
		t.Fatal(e)
	}
	snap, e := s.loadSnapshot(context.Background(), s.db, defaultAgentID)
	if e != nil {
		t.Fatal(e)
	}
	snap.Tools = []AgentCapability{c}
	return snap, &User{ID: uid}, "tool-run"
}
func TestMCPReportedErrorIsAuditedFailed(t *testing.T) {
	s, _ := testApp(t)
	mcpFixture(t, s, false, func(method string) bool { return method == "tools/call" })
	tools, e := s.listMCPTools(context.Background(), "campus")
	if e != nil {
		t.Fatal(e)
	}
	c := AgentCapability{ID: "cap_fixture", Kind: "mcp", Name: "test", Config: CapabilityConfig{ServerID: "campus", Tool: "campus_hours", ReadOnly: true, InputSchema: tools[0].InputSchema}}
	snap, u, rid := fixtureToolRun(t, s, c)
	call := toolCall{}
	call.Function.Name = capabilityFunction(c)
	call.Function.Arguments = `{"query":"library"}`
	_, e = s.executeBoundTool(context.Background(), u, rid, snap, call)
	if e == nil {
		t.Fatal("remote isError reported as success")
	}
	var status string
	s.db.QueryRow("SELECT status FROM agent_tool_events ORDER BY id DESC LIMIT 1").Scan(&status)
	if status != "failed" {
		t.Fatalf("wrong tool audit %s", status)
	}
}
func TestMCPDisabledDuringDiscoveryNeverCallsTool(t *testing.T) {
	s, _ := testApp(t)
	armed := &atomic.Bool{}
	started, release := make(chan bool), make(chan bool)
	calls, _ := mcpFixture(t, s, false, func(method string) bool {
		if method == "tools/list" && armed.Load() {
			close(started)
			<-release
		}
		return false
	})
	tools, e := s.listMCPTools(context.Background(), "campus")
	if e != nil {
		t.Fatal(e)
	}
	c := AgentCapability{ID: "cap_fixture", Kind: "mcp", Name: "test", Config: CapabilityConfig{ServerID: "campus", Tool: "campus_hours", ReadOnly: true, InputSchema: tools[0].InputSchema}}
	snap, u, rid := fixtureToolRun(t, s, c)
	call := toolCall{}
	call.Function.Name = capabilityFunction(c)
	call.Function.Arguments = `{"query":"library"}`
	armed.Store(true)
	done := make(chan error, 1)
	go func() { _, e := s.executeBoundTool(context.Background(), u, rid, snap, call); done <- e }()
	<-started
	s.db.Exec("UPDATE agent_capabilities SET enabled=0 WHERE id=?", c.ID)
	close(release)
	if e := <-done; e == nil {
		t.Fatal("disabled capability invoked after discovery")
	}
	if calls.Load() != 0 {
		t.Fatal("remote tool ran after revocation")
	}
}
