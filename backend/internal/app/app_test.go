package app

import (
	"bytes"
	"context"
	"database/sql"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"qingfly/edu-mall/internal/adaptor/verification"
	"qingfly/edu-mall/internal/config"
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

func configApp() config.App {
	return config.App{AuthDelivery: verification.Config{Mode: "local"}}
}

func testApp(t *testing.T) (*App, *sql.DB) {
	t.Helper()
	return testAppWith(t, configApp())
}

// testAppWith 为每个测试创建独立数据库和 Redis 前缀。
func testAppWith(t *testing.T, cfg config.App, opts ...Option) (*App, *sql.DB) {
	t.Helper()
	db := testenv.MySQL(t)
	rdb, prefix := testenv.Redis(t)
	cfg.RedisPrefix = prefix
	cfg.NodeID = 0
	cfg.Instance = "test-" + prefix
	cfg.UploadDir = t.TempDir()
	if cfg.AuthDelivery.Mode == "" {
		cfg.AuthDelivery.Mode = "local"
	}
	a, err := New(db, rdb, cfg, opts...)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { a.Close(context.Background()) })
	return a, db
}

// runWorkers 启动后台任务（延迟队列、抢购落库等），测试结束时停止。
func runWorkers(t *testing.T, a *App) {
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

func account(t *testing.T, a *App, n string) *testClient {
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

func now() int64 { return time.Now().UnixMilli() }

func userID(t *testing.T, c *testClient) string {
	t.Helper()
	s, d := c.call("GET", "/api/v1/auth/me", nil)
	if s != 200 {
		t.Fatalf("me %d", s)
	}
	return d["id"].(string)
}

func eventually(t *testing.T, d time.Duration, what string, cond func() bool) {
	t.Helper()
	deadline := time.Now().Add(d)
	for !cond() {
		if time.Now().After(deadline) {
			t.Fatalf("timeout waiting for %s", what)
		}
		time.Sleep(50 * time.Millisecond)
	}
}
