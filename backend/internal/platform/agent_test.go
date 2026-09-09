package platform

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"
	"time"
)

func modelStub(t *testing.T, s *Server, reason string) *atomic.Int32 {
	t.Helper()
	calls := &atomic.Int32{}
	up := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls.Add(1)
		w.Header().Set("Content-Type", "text/event-stream")
		fmt.Fprintf(w, "data: {\"choices\":[{\"delta\":{\"content\":\"你好，同学\"},\"finish_reason\":null}]}\n\ndata: {\"choices\":[{\"delta\":{},\"finish_reason\":%q}]}\n\ndata: [DONE]\n\n", reason)
	}))
	t.Cleanup(up.Close)
	s.cfg.ModelURL = up.URL
	s.cfg.ModelKey = "test-only"
	s.cfg.ModelName = "stub"
	return calls
}
func agentRequest(c *testClient, key string) *http.Request {
	b, _ := json.Marshal(map[string]string{"prompt": "推荐一本书", "idempotency_key": key})
	r := httptest.NewRequest("POST", "/api/v1/agent/runs", bytes.NewReader(b))
	r.AddCookie(c.cookie)
	return r
}
func TestAgentTruncationIsNotSuccess(t *testing.T) {
	for _, reason := range []string{"length", "content_filter"} {
		t.Run(reason, func(t *testing.T) {
			s, _ := testApp(t)
			c := account(t, s, "buyer")
			modelStub(t, s, reason)
			w := httptest.NewRecorder()
			s.Handler().ServeHTTP(w, agentRequest(c, "truncated-run"))
			var status string
			if e := s.db.QueryRow("SELECT status FROM agent_runs").Scan(&status); e != nil {
				t.Fatal(e)
			}
			if status != "failed" {
				t.Fatalf("finish_reason %s saved as %s", reason, status)
			}
		})
	}
}
func TestAgentStaleRunRecovery(t *testing.T) {
	s, _ := testApp(t)
	c := account(t, s, "buyer")
	calls := modelStub(t, s, "stop")
	var uid string
	s.db.QueryRow("SELECT id FROM users WHERE email='buyer@campus.test'").Scan(&uid)
	s.db.Exec("INSERT INTO agent_threads VALUES('stale-thread',?,'stale',?)", uid, now()-200000)
	s.db.Exec("INSERT INTO agent_runs(id,thread_id,user_id,idem,prompt,status,created_at) VALUES('old','stale-thread',?,'stale-run-key','old','running',?)", uid, now()-200000)
	w := httptest.NewRecorder()
	s.Handler().ServeHTTP(w, agentRequest(c, "new-run-key"))
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
	s, _ := testApp(t)
	c := account(t, s, "buyer")
	calls := modelStub(t, s, "stop")
	w := httptest.NewRecorder()
	s.Handler().ServeHTTP(w, agentRequest(c, "replay-agent-key"))
	blocked := &blockingStream{header: http.Header{}, started: make(chan bool), release: make(chan bool)}
	finished := make(chan bool)
	go func() { s.Handler().ServeHTTP(blocked, agentRequest(c, "replay-agent-key")); close(finished) }()
	<-blocked.started
	ctx, cancel := context.WithTimeout(context.Background(), 150*time.Millisecond)
	defer cancel()
	var one int
	e := s.db.QueryRowContext(ctx, "SELECT 1").Scan(&one)
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
	s, _ := testApp(t)
	owner := account(t, s, "owner")
	other := account(t, s, "other")
	modelStub(t, s, "stop")
	w := httptest.NewRecorder()
	s.Handler().ServeHTTP(w, agentRequest(owner, "private-thread-key"))
	var tid string
	if e := s.db.QueryRow("SELECT thread_id FROM agent_runs").Scan(&tid); e != nil {
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
	c := toolCall{}
	c.Function.Name = "my_orders"
	c.Function.Arguments = `{"user_id":"another-user"}`
	out, e := s.readTool(context.Background(), &User{ID: "current-user"}, c)
	if e != nil || out != `{"error":"no identity arguments allowed"}` {
		t.Fatalf("tool trusted model identity: %q %v", out, e)
	}
}
