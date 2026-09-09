package platform

import (
	"context"
	"crypto/rand"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"net/http"
	"net/netip"
	"net/url"
	"strings"
	"sync"
	"time"
)

type Config struct {
	AdminUserIDs                                                                  string
	ModelProviders                                                                map[string]ModelProvider
	MCPServers                                                                    map[string]MCPServer
	TrustedProxyCIDRs                                                             string
	UploadDir                                                                     string
	PublicOrigin                                                                  string
	Production                                                                    bool
	AlipayAppID, AlipaySellerID, AlipayPrivateKey, AlipayPublicKey, AlipayGateway string
	ModelURL, ModelKey, ModelName                                                 string
}
type Server struct {
	uploadSlots chan struct{}
	trusted     []netip.Prefix
	db          *sql.DB
	cfg         Config
	mux         *http.ServeMux
	pay         *Alipay
	live        *LiveHub
	limits      *limiter
	client      *http.Client
}
type apiError struct {
	status  int
	message string
}

func (e *apiError) Error() string       { return e.message }
func fail(status int, msg string) error { return &apiError{status, msg} }
func id(prefix string) string {
	b := make([]byte, 16)
	if _, e := rand.Read(b); e != nil {
		panic(e)
	}
	return prefix + hex.EncodeToString(b)
}
func now() int64 { return time.Now().UnixMilli() }
func NewServer(db *sql.DB, cfg Config) (*Server, error) {
	if cfg.UploadDir == "" {
		cfg.UploadDir = "data/uploads"
	}
	if cfg.ModelURL != "" {
		u, e := url.Parse(cfg.ModelURL)
		if e != nil || u.Host == "" || u.User != nil || (u.Scheme != "https" && (cfg.Production || u.Scheme != "http")) {
			return nil, errors.New("invalid MODEL_BASE_URL")
		}
	}
	if cfg.Production {
		u, e := url.Parse(cfg.PublicOrigin)
		if e != nil || u.Scheme != "https" || u.Host == "" || u.Path != "" {
			return nil, errors.New("production requires PUBLIC_ORIGIN=https://your-domain without path")
		}
	}
	pay, e := NewAlipay(cfg)
	if e != nil {
		return nil, e
	}
	s := &Server{db: db, cfg: cfg, mux: http.NewServeMux(), pay: pay, live: NewLiveHub(), limits: &limiter{entries: map[string]window{}}, client: &http.Client{Timeout: 90 * time.Second}}
	s.uploadSlots = make(chan struct{}, 2)
	if e = s.initAgentCatalog(); e != nil {
		return nil, e
	}
	s.agentCatalogRoutes()
	for _, cidr := range strings.Split(cfg.TrustedProxyCIDRs, ",") {
		if strings.TrimSpace(cidr) == "" {
			continue
		}
		p, e := netip.ParsePrefix(strings.TrimSpace(cidr))
		if e != nil {
			return nil, errors.New("invalid TRUSTED_PROXY_CIDRS")
		}
		s.trusted = append(s.trusted, p)
	}
	if _, e = db.Exec("UPDATE agent_runs SET status='failed',error='服务重启，请重新发送' WHERE status='running'"); e != nil {
		return nil, e
	}
	s.route("GET /api/v1/health", false, s.health)
	s.route("GET /api/v1/config", false, s.config)
	s.route("POST /api/v1/auth/register", false, s.register)
	s.route("POST /api/v1/auth/login", false, s.login)
	s.route("GET /api/v1/auth/me", true, s.me)
	s.route("POST /api/v1/auth/logout", true, s.logout)
	s.route("GET /api/v1/products", false, s.products)
	s.route("GET /api/v1/products/{id}", false, s.product)
	s.route("POST /api/v1/products", true, s.publish)
	s.route("POST /api/v1/uploads", true, s.upload)
	s.mux.HandleFunc("GET /media/{file}", s.media)
	s.route("DELETE /api/v1/products/{id}", true, s.withdraw)
	s.route("GET /api/v1/favorites", true, s.favorites)
	s.route("PUT /api/v1/favorites/{id}", true, s.favorite)
	s.route("DELETE /api/v1/favorites/{id}", true, s.unfavorite)
	s.route("POST /api/v1/orders", true, s.createOrder)
	s.route("GET /api/v1/orders", true, s.orders)
	s.route("GET /api/v1/orders/{id}", true, s.getOrder)
	s.route("POST /api/v1/orders/{id}/cancel", true, s.cancelOrder)
	s.route("POST /api/v1/orders/{id}/handover", true, s.handover)
	s.route("POST /api/v1/orders/{id}/complete", true, s.complete)
	s.route("POST /api/v1/orders/{id}/refund", true, s.refund)
	s.route("POST /api/v1/orders/{id}/pay", true, s.startPayment)
	s.mux.HandleFunc("POST /api/v1/payments/alipay/notify", s.paymentNotify)
	s.route("POST /api/v1/conversations", true, s.createConversation)
	s.route("GET /api/v1/conversations", true, s.conversations)
	s.route("GET /api/v1/conversations/{id}/messages", true, s.messages)
	s.route("POST /api/v1/conversations/{id}/messages", true, s.sendMessage)
	s.route("POST /api/v1/conversations/{id}/read", true, s.readMessages)
	s.route("GET /api/v1/agent/threads", true, s.agentThreads)
	s.route("GET /api/v1/agent/threads/{id}", true, s.agentHistory)
	s.route("POST /api/v1/agent/runs", true, s.agentRun)
	s.mux.HandleFunc("GET /api/v1/ws", s.websocket)
	return s, nil
}

type endpoint func(http.ResponseWriter, *http.Request, *User) error

func (s *Server) route(pattern string, authenticated bool, fn endpoint) {
	s.mux.HandleFunc(pattern, func(w http.ResponseWriter, r *http.Request) {
		var u *User
		var e error
		if authenticated {
			u, e = s.currentUser(r)
			if e != nil {
				writeError(w, e)
				return
			}
		}
		if e = fn(w, r, u); e != nil {
			writeError(w, e)
		}
	})
}
func (s *Server) Handler() http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("X-Request-ID", id("req_"))
		w.Header().Set("X-Content-Type-Options", "nosniff")
		w.Header().Set("Referrer-Policy", "same-origin")
		w.Header().Set("Cache-Control", "no-store")
		defer func() {
			if e := recover(); e != nil {
				slog.Error("request panic", "path", r.URL.Path, "error", e)
				writeError(w, fail(500, "服务暂时不可用"))
			}
		}()
		if r.Method != "GET" && r.Method != "HEAD" && r.URL.Path != "/api/v1/payments/alipay/notify" {
			origin := r.Header.Get("Origin")
			expected := s.cfg.PublicOrigin
			if expected == "" {
				scheme := "http"
				if r.TLS != nil {
					scheme = "https"
				}
				expected = scheme + "://" + r.Host
			}
			if (origin != "" && origin != expected) || r.Header.Get("Sec-Fetch-Site") == "cross-site" {
				writeError(w, fail(403, "请求来源不被允许"))
				return
			}
		}
		if r.URL.Path != "/api/v1/health" {
			key := s.clientIP(r)
			max := 600
			if strings.HasPrefix(r.URL.Path, "/api/v1/auth/") && r.Method == "POST" {
				key += "auth"
				max = 30
			}
			if r.URL.Path == "/api/v1/payments/alipay/notify" {
				key += ":payment-callback"
				max = 1200
			}
			if !s.limits.allow(key, max) {
				w.Header().Set("Retry-After", "60")
				writeError(w, fail(429, "请求过于频繁，请稍后重试"))
				return
			}
		}
		maxBody := int64(1 << 20)
		if r.URL.Path == "/api/v1/uploads" {
			maxBody = 6 << 20
		}
		r.Body = http.MaxBytesReader(w, r.Body, maxBody)
		s.mux.ServeHTTP(w, r)
	})
}
func decode(r *http.Request, v any) error {
	d := json.NewDecoder(r.Body)
	d.DisallowUnknownFields()
	if e := d.Decode(v); e != nil {
		return fail(400, "请求字段或格式不正确")
	}
	if e := d.Decode(&struct{}{}); e != io.EOF {
		return fail(400, "请求只能包含一个 JSON 对象")
	}
	return nil
}
func result(w http.ResponseWriter, status int, data any) error {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.WriteHeader(status)
	return json.NewEncoder(w).Encode(map[string]any{"code": 0, "msg": "OK", "data": data})
}
func writeError(w http.ResponseWriter, e error) {
	var a *apiError
	if !errors.As(e, &a) {
		if errors.Is(e, sql.ErrNoRows) {
			a = &apiError{404, "内容不存在或无权访问"}
		} else {
			slog.Error("request failed", "error", e)
			a = &apiError{500, "服务暂时不可用，请稍后重试"}
		}
	}
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.WriteHeader(a.status)
	_ = json.NewEncoder(w).Encode(map[string]any{"code": a.status, "msg": a.message, "data": nil})
}
func (s *Server) health(w http.ResponseWriter, r *http.Request, _ *User) error {
	ctx, cancel := context.WithTimeout(r.Context(), time.Second)
	defer cancel()
	if e := s.db.PingContext(ctx); e != nil {
		return fail(503, "数据库不可用")
	}
	return result(w, 200, map[string]any{"status": "ok"})
}
func (s *Server) config(w http.ResponseWriter, r *http.Request, _ *User) error {
	return result(w, 200, map[string]any{"payments": map[string]bool{"alipay": s.pay != nil}, "agent_available": s.cfg.ModelKey != "" && s.cfg.ModelName != "" && s.cfg.ModelURL != "", "categories": categories, "campuses": campuses})
}

type window struct {
	start time.Time
	n     int
}
type limiter struct {
	mu      sync.Mutex
	entries map[string]window
}

func (l *limiter) allow(key string, max int) bool {
	l.mu.Lock()
	defer l.mu.Unlock()
	t := time.Now()
	if len(l.entries) > 10000 {
		for k, v := range l.entries {
			if t.Sub(v.start) > time.Minute {
				delete(l.entries, k)
			}
		}
		if len(l.entries) > 10000 {
			return false
		}
	}
	v := l.entries[key]
	if t.Sub(v.start) > time.Minute {
		v = window{start: t}
	}
	v.n++
	l.entries[key] = v
	return v.n <= max
}
