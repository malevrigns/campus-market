package platform

import (
	"context"
	"crypto/rand"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/netip"
	"net/url"
	"os"
	"strconv"
	"strings"
	"time"

	"github.com/redis/go-redis/v9"

	"qingfly/edu-mall/internal/infra/mysqlx"
	"qingfly/edu-mall/internal/infra/redisx"
	"qingfly/edu-mall/internal/pkg/cache"
	"qingfly/edu-mall/internal/pkg/delayqueue"
	"qingfly/edu-mall/internal/pkg/lock"
	"qingfly/edu-mall/internal/pkg/ratelimit"
	"qingfly/edu-mall/internal/pkg/snowflake"
	"qingfly/edu-mall/internal/seckill"
)

type Config struct {
	AuthDelivery                                                                  DeliveryConfig
	AuthCodeSecret                                                                string
	AdminUserIDs                                                                  string
	ModelProviders                                                                map[string]ModelProvider
	MCPServers                                                                    map[string]MCPServer
	TrustedProxyCIDRs                                                             string
	UploadDir                                                                     string
	PublicOrigin                                                                  string
	Production                                                                    bool
	AlipayAppID, AlipaySellerID, AlipayPrivateKey, AlipayPublicKey, AlipayGateway string
	ModelURL, ModelKey, ModelName                                                 string
	// RedisPrefix 是所有 Redis key 的前缀，多个环境共用一个 Redis 时用来隔离。
	RedisPrefix string
	// Instance 标识当前进程（Stream 消费者名、Pub/Sub 来源），默认 hostname-pid。
	Instance string
	// NodeID 是雪花算法节点号；小于 0 表示通过 Redis 租约自动分配。
	NodeID int
	// PaymentWindow 是订单待支付时长，默认 15 分钟。
	PaymentWindow time.Duration
}

type Server struct {
	verification *verificationState
	uploadSlots  chan struct{}
	trusted      []netip.Prefix
	db           *sql.DB
	rdb          redis.UniversalClient
	keys         redisx.Keys
	cfg          Config
	mux          *http.ServeMux
	pay          *Alipay
	live         *LiveHub
	limiter      *ratelimit.Limiter
	locks        *lock.Client
	cache        *cache.Cache
	timeouts     *delayqueue.Queue
	flash        *seckill.Engine
	ids          *snowflake.Node
	lease        *snowflake.Lease
	client       *http.Client
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

func defaultInstance() string {
	host, _ := os.Hostname()
	if host == "" {
		host = "campus"
	}
	return fmt.Sprintf("%s-%d-%s", host, os.Getpid(), id("")[:6])
}

// NewServer 组装应用。db 与 rdb 由调用方创建并负责关闭；后台任务需要另外调用 Run 启动。
func NewServer(db *sql.DB, rdb redis.UniversalClient, cfg Config) (*Server, error) {
	if db == nil || rdb == nil {
		return nil, errors.New("database and redis are required")
	}
	if cfg.UploadDir == "" {
		cfg.UploadDir = "data/uploads"
	}
	if cfg.RedisPrefix == "" {
		cfg.RedisPrefix = "campus"
	}
	if cfg.Instance == "" {
		cfg.Instance = defaultInstance()
	}
	if cfg.PaymentWindow <= 0 {
		cfg.PaymentWindow = 15 * time.Minute
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
	prefix := cfg.RedisPrefix
	s := &Server{db: db, rdb: rdb, keys: redisx.Keys(prefix), cfg: cfg, mux: http.NewServeMux(), pay: pay, client: &http.Client{Timeout: 90 * time.Second}}
	s.uploadSlots = make(chan struct{}, 2)
	s.locks = lock.New(rdb, prefix+":lock")
	s.limiter = ratelimit.New(rdb, prefix+":rl")
	s.cache = cache.New(rdb, cache.Options{Prefix: prefix + ":cache", LocalTTL: 3 * time.Second, NullTTL: 30 * time.Second})
	s.timeouts = delayqueue.New(rdb, prefix, "order-timeout", delayqueue.Options{Visibility: 30 * time.Second, Workers: 4})
	s.live = NewLiveHub(rdb, prefix+":live", cfg.Instance)
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	if cfg.NodeID >= 0 {
		if s.ids, e = snowflake.NewNode(int64(cfg.NodeID)); e != nil {
			return nil, e
		}
	} else {
		if s.lease, e = snowflake.AcquireLease(ctx, s.locks, 30*time.Second); e != nil {
			return nil, e
		}
		s.ids = s.lease.Node
	}
	s.flash = seckill.New(db, rdb, seckill.Config{Prefix: prefix, Consumer: cfg.Instance, PaymentWindow: cfg.PaymentWindow}, s.newOrderID, seckill.Hooks{OrderCreated: s.onFlashOrderCreated, OrderFailed: s.onFlashOrderFailed})
	if e = s.initVerification(); e != nil {
		return nil, e
	}
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
	if _, e = db.Exec("UPDATE agent_runs SET status='failed',error='服务重启，请重新发送' WHERE status='running' AND created_at<?", now()-120000); e != nil {
		return nil, e
	}
	s.route("GET /api/v1/health", false, s.health)
	s.route("GET /api/v1/config", false, s.config)
	s.route("POST /api/v1/auth/register", false, s.register)
	s.route("POST /api/v1/auth/login", false, s.login)
	s.route("POST /api/v1/auth/codes", false, s.requestCode)
	s.route("POST /api/v1/auth/code-login", false, s.codeLogin)
	s.route("POST /api/v1/auth/password/reset", false, s.resetPassword)
	s.route("POST /api/v1/auth/dev-inbox", false, s.devInbox)
	s.route("POST /api/v1/auth/bind", true, s.bindIdentity)
	s.route("GET /api/v1/auth/me", true, s.me)
	s.route("POST /api/v1/auth/logout", true, s.logout)
	s.route("GET /api/v1/products", false, s.products)
	s.route("GET /api/v1/products/hot", false, s.hotProducts)
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
	s.route("GET /api/v1/flash-sales", false, s.flashSales)
	s.route("GET /api/v1/flash-sales/{id}", false, s.flashSale)
	s.route("POST /api/v1/flash-sales/{id}/attempts", true, s.flashAttempt)
	s.route("GET /api/v1/flash-sales/{id}/attempts/me", true, s.flashResult)
	s.admin("POST /api/v1/admin/flash-sales", s.createFlashSale)
	s.admin("POST /api/v1/admin/flash-sales/{id}/cancel", s.cancelFlashSale)
	s.admin("GET /api/v1/admin/runtime", s.runtimeStats)
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

// Close 释放进程级资源（雪花节点租约）。
func (s *Server) Close(ctx context.Context) {
	s.cache.Close()
	if s.lease != nil {
		s.lease.Release(ctx)
	}
}

// Instance 返回实例标识（日志、Stream 消费者名）。
func (s *Server) Instance() string { return s.cfg.Instance }

func (s *Server) newOrderID() (string, error) {
	n, e := s.ids.Next()
	if e != nil {
		return "", e
	}
	return "CM" + strconv.FormatInt(n, 10), nil
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

func (s *Server) admin(pattern string, fn endpoint) {
	s.route(pattern, true, func(w http.ResponseWriter, r *http.Request, u *User) error {
		if !s.isAgentAdmin(u.ID) {
			return fail(403, "仅管理员可执行该操作")
		}
		return fn(w, r, u)
	})
}

// allow 是业务接口级限流（滑动窗口）。
func (s *Server) allow(r *http.Request, key string, limit int, window time.Duration) bool {
	return s.limiter.Window(r.Context(), key, limit, window).Allowed
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
		switch {
		case errors.Is(e, sql.ErrNoRows):
			a = &apiError{404, "内容不存在或无权访问"}
		case mysqlx.IsDuplicate(e):
			// 并发请求撞上唯一约束（幂等键、一人一单等），由数据库约束兜底后返回冲突。
			a = &apiError{409, "请求冲突，请刷新后重试"}
		case mysqlx.IsRetryable(e):
			a = &apiError{503, "系统繁忙，请稍后重试"}
		case errors.Is(e, context.DeadlineExceeded):
			a = &apiError{504, "处理超时，请稍后重试"}
		default:
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
	if e := s.rdb.Ping(ctx).Err(); e != nil {
		return fail(503, "缓存不可用")
	}
	return result(w, 200, map[string]any{"status": "ok"})
}
func (s *Server) config(w http.ResponseWriter, r *http.Request, _ *User) error {
	return result(w, 200, map[string]any{"payments": map[string]bool{"alipay": s.pay != nil}, "agent_available": s.cfg.ModelKey != "" && s.cfg.ModelName != "" && s.cfg.ModelURL != "", "categories": categories, "campuses": campuses, "auth": map[string]any{"email_code": s.verification.email, "sms_code": s.verification.sms, "local_inbox": s.verification.local, "password_min_length": 8}})
}

// runtimeStats 给管理员查看缓存命中、延迟队列积压、抢购消息流状态。
func (s *Server) runtimeStats(w http.ResponseWriter, r *http.Request, _ *User) error {
	ready, inflight, dead, e := s.timeouts.Stats(r.Context())
	if e != nil {
		return e
	}
	db := s.db.Stats()
	return result(w, 200, map[string]any{
		"instance":      s.cfg.Instance,
		"snowflake":     s.ids.ID(),
		"cache":         s.cache.Stats(),
		"order_timeout": map[string]int64{"ready": ready, "inflight": inflight, "dead": dead},
		"flash":         s.flash.StreamStats(r.Context()),
		"db_pool":       map[string]any{"open": db.OpenConnections, "in_use": db.InUse, "idle": db.Idle, "wait_count": db.WaitCount, "wait_ms": db.WaitDuration.Milliseconds()},
	})
}
