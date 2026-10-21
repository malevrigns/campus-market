package platform

import (
	"bytes"
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"

	"qingfly/edu-mall/internal/testenv"
)

type testClient struct {
	t      *testing.T
	app    http.Handler
	cookie *http.Cookie
}

func (c *testClient) call(method, path string, body any) (int, map[string]any) {
	c.t.Helper()
	b, _ := json.Marshal(body)
	r := httptest.NewRequest(method, path, bytes.NewReader(b))
	r.RemoteAddr = "127.0.0.1:12345"
	r.Header.Set("Content-Type", "application/json")
	if c.cookie != nil {
		r.AddCookie(c.cookie)
	}
	w := httptest.NewRecorder()
	c.app.ServeHTTP(w, r)
	var out struct {
		Data map[string]any `json:"data"`
		Msg  string         `json:"msg"`
	}
	_ = json.Unmarshal(w.Body.Bytes(), &out)
	if out.Data == nil {
		out.Data = map[string]any{"msg": out.Msg}
	}
	for _, k := range w.Result().Cookies() {
		if strings.HasPrefix(k.Name, "campus_session") || k.Name == "__Host-campus_session" {
			c.cookie = k
		}
	}
	return w.Code, out.Data
}
func testApp(t *testing.T) (*Server, *sql.DB) {
	t.Helper()
	return testAppWith(t, Config{AuthDelivery: DeliveryConfig{Mode: "local"}})
}

// testAppWith 为每个测试创建独立的数据库和 Redis 前缀。
func testAppWith(t *testing.T, cfg Config) (*Server, *sql.DB) {
	t.Helper()
	db := testenv.MySQL(t)
	rdb, prefix := testenv.Redis(t)
	cfg.RedisPrefix = prefix
	a, e := NewServer(db, rdb, cfg)
	if e != nil {
		t.Fatal(e)
	}
	t.Cleanup(func() { a.Close(context.Background()) })
	return a, db
}

// runWorkers 启动后台任务（延迟队列、抢购落库等），测试结束时停止。
func runWorkers(t *testing.T, a *Server) {
	t.Helper()
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() {
		defer close(done)
		a.Run(ctx)
	}()
	t.Cleanup(func() {
		cancel()
		<-done
	})
}
func account(t *testing.T, a *Server, n string) *testClient {
	t.Helper()
	c := &testClient{t: t, app: a.Handler()}
	proof := testCode(t, c, "email", n+"@campus.test", "register")
	proof["name"] = n
	proof["password"] = "Testing-strong-123"
	s, _ := c.call("POST", "/api/v1/auth/register", proof)
	if s != 201 {
		t.Fatalf("register: %d", s)
	}
	return c
}
func publish(t *testing.T, c *testClient) string {
	t.Helper()
	s, d := c.call("POST", "/api/v1/products", map[string]any{"title": "测试相机", "price_ct": 12345, "category": "数码电子", "campus": "东区", "condition": "九成新", "description": "无拆无修，可当面验机。", "image": "https://images.unsplash.com/photo-1516035069371-29a1b244cc32"})
	if s != 201 {
		t.Fatalf("publish %d %v", s, d)
	}
	return d["id"].(string)
}
func order(c *testClient, id, key string) (int, map[string]any) {
	return c.call("POST", "/api/v1/orders", map[string]any{"product_id": id, "campus": "东区", "idempotency_key": key})
}

func TestAuthAndServerAuthoritativeOrder(t *testing.T) {
	a, _ := testApp(t)
	seller := account(t, a, "seller")
	buyer := account(t, a, "buyer")
	stranger := account(t, a, "stranger")
	id := publish(t, seller)
	anon := &testClient{t: t, app: a.Handler()}
	if s, _ := order(anon, id, "anonymous-key"); s != 401 {
		t.Fatalf("anonymous order %d", s)
	}
	if s, _ := buyer.call("POST", "/api/v1/orders", map[string]any{"product_id": id, "campus": "东区", "idempotency_key": "forged-price", "price_ct": 1, "user_id": "seller"}); s != 400 {
		t.Fatalf("client fields accepted: %d", s)
	}
	s, d := order(buyer, id, "create-order-key")
	if s != 201 || d["amount_ct"] != float64(12345) {
		t.Fatalf("server price: %d %v", s, d)
	}
	no := d["id"].(string)
	if s, d2 := order(buyer, id, "create-order-key"); s != 200 || d2["id"] != no {
		t.Fatalf("retry %d %v", s, d2)
	}
	if s, _ := stranger.call("GET", "/api/v1/orders/"+no, nil); s != 404 {
		t.Fatalf("order disclosure %d", s)
	}
	if s, _ := order(stranger, id, "other-order-key"); s != 409 {
		t.Fatalf("oversell %d", s)
	}
	if s, _ := buyer.call("POST", "/api/v1/orders/"+no+"/pay", map[string]any{"method": "alipay", "mobile": false}); s != 503 {
		t.Fatalf("unconfigured payment %d", s)
	}
	if s, _ := buyer.call("POST", "/api/v1/orders/"+no+"/cancel", map[string]any{}); s != 200 {
		t.Fatalf("cancel %d", s)
	}
	if s, _ := order(stranger, id, "other-order-key"); s != 201 {
		t.Fatalf("stock not restored %d", s)
	}
}
func TestConcurrentInventoryAndPersistence(t *testing.T) {
	a, db := testApp(t)
	seller := account(t, a, "seller")
	id := publish(t, seller)
	clients := []*testClient{}
	for i := 0; i < 6; i++ {
		clients = append(clients, account(t, a, fmt.Sprintf("buyer%d", i)))
	}
	var wg sync.WaitGroup
	statuses := make(chan int, 6)
	for i, c := range clients {
		wg.Add(1)
		go func(i int, c *testClient) {
			defer wg.Done()
			s, _ := order(c, id, fmt.Sprintf("parallel-key-%d", i))
			statuses <- s
		}(i, c)
	}
	wg.Wait()
	close(statuses)
	won := 0
	for s := range statuses {
		if s == 201 {
			won++
		} else if s != 409 {
			t.Fatalf("unexpected %d", s)
		}
	}
	if won != 1 {
		t.Fatalf("sold %d times", won)
	}
	var count int
	if e := db.QueryRow("SELECT COUNT(*) FROM orders").Scan(&count); e != nil || count != 1 {
		t.Fatalf("durability: %d %v", count, e)
	}
}
func TestChatMembershipDedupAndCursor(t *testing.T) {
	a, _ := testApp(t)
	seller := account(t, a, "seller")
	buyer := account(t, a, "buyer")
	stranger := account(t, a, "stranger")
	id := publish(t, seller)
	s, d := buyer.call("POST", "/api/v1/conversations", map[string]any{"product_id": id})
	if s != 201 {
		t.Fatalf("conversation %d %v", s, d)
	}
	conv := d["id"].(string)
	path := "/api/v1/conversations/" + conv + "/messages"
	if s, _ := stranger.call("GET", path, nil); s != 404 {
		t.Fatalf("nonmember sync %d", s)
	}
	s, m := buyer.call("POST", path, map[string]any{"client_msg_id": "message-key-123", "content": "同学你好"})
	if s != 201 || m["seq"] != float64(1) {
		t.Fatalf("send %d %v", s, m)
	}
	if s, m := buyer.call("POST", path, map[string]any{"client_msg_id": "message-key-123", "content": "同学你好"}); s != 200 || m["seq"] != float64(1) {
		t.Fatalf("retry %d %v", s, m)
	}
	if s, m := seller.call("POST", path, map[string]any{"client_msg_id": "message-key-123", "content": "你好，还在的"}); s != 201 || m["seq"] != float64(2) {
		t.Fatalf("sender dedup collision %d %v", s, m)
	}
	if s, d := buyer.call("GET", path+"?after=1", nil); s != 200 || len(d["items"].([]any)) != 1 {
		t.Fatalf("cursor %d %v", s, d)
	}
	if s, _ := buyer.call("POST", "/api/v1/conversations/"+conv+"/read", map[string]any{"seq": 999}); s != 400 {
		t.Fatalf("future read %d", s)
	}
}
func TestLogoutRevokesAndOriginProtection(t *testing.T) {
	a, _ := testApp(t)
	c := account(t, a, "buyer")
	old := c.cookie
	r := httptest.NewRequest("POST", "/api/v1/auth/logout", bytes.NewBufferString("{}"))
	r.AddCookie(c.cookie)
	r.Header.Set("Origin", "https://evil.example")
	w := httptest.NewRecorder()
	a.Handler().ServeHTTP(w, r)
	if w.Code != 403 {
		t.Fatalf("CSRF %d", w.Code)
	}
	c.call("POST", "/api/v1/auth/logout", map[string]any{})
	c.cookie = old
	if s, _ := c.call("GET", "/api/v1/auth/me", nil); s != 401 {
		t.Fatalf("revoked session %d", s)
	}
}
func TestAgentUnavailableAndOwnership(t *testing.T) {
	a, _ := testApp(t)
	c := account(t, a, "buyer")
	if s, _ := c.call("POST", "/api/v1/agent/runs", map[string]any{"prompt": "帮我找相机", "idempotency_key": "agent-run-key"}); s != 503 {
		t.Fatalf("fake AI success %d", s)
	}
}
