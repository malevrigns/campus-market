// Package app 是组合根：读取配置 → 构造基础设施 → 构造仓储/适配器 → 组装业务服务 →
// 注册 HTTP 路由与后台任务。它也是集成测试的装配入口。
package app

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"os"
	"strconv"
	"strings"
	"time"

	"github.com/redis/go-redis/v9"

	"qingfly/edu-mall/internal/adaptor/alipay"
	"qingfly/edu-mall/internal/adaptor/llm"
	"qingfly/edu-mall/internal/adaptor/mcp"
	"qingfly/edu-mall/internal/adaptor/media"
	"qingfly/edu-mall/internal/adaptor/verification"
	"qingfly/edu-mall/internal/config"
	"qingfly/edu-mall/internal/domain"
	"qingfly/edu-mall/internal/job"
	"qingfly/edu-mall/internal/pkg/delayqueue"
	"qingfly/edu-mall/internal/pkg/idgen"
	"qingfly/edu-mall/internal/pkg/lock"
	"qingfly/edu-mall/internal/pkg/snowflake"
	"qingfly/edu-mall/internal/port"
	mysqlrepo "qingfly/edu-mall/internal/repository/mysql"
	redisrepo "qingfly/edu-mall/internal/repository/redis"
	"qingfly/edu-mall/internal/service"
	httpapi "qingfly/edu-mall/internal/transport/http"
)

type App struct {
	DB       *sql.DB
	Redis    redis.UniversalClient
	HTTP     *httpapi.Server
	Auth     *service.AuthService
	Catalog  *service.CatalogService
	Orders   *service.OrderService
	Payments *service.PaymentService
	Chat     *service.ChatService
	Flash    *service.FlashService
	Agents   *service.AgentCatalogService
	AgentRun *service.AgentRunService
	Admin    *service.AdminService
	Timeouts port.TimeoutQueue
	Cache    *redisrepo.CacheStore
	Live     *redisrepo.LiveHub
	Session  *redisrepo.SessionCache

	cfg      config.App
	redis    *redisrepo.Repos
	runner   *job.Runner
	lease    *snowflake.Lease
	node     *snowflake.Node
	registry *service.RuntimeConnections
}

// Option 允许测试或运维在组合根替换外部适配器。
type Option func(*options)

type options struct {
	sender  port.VerificationSender
	gateway port.PaymentGateway
}

func WithVerificationSender(sender port.VerificationSender) Option {
	return func(o *options) { o.sender = sender }
}

func WithPaymentGateway(gateway port.PaymentGateway) Option {
	return func(o *options) { o.gateway = gateway }
}

func defaultInstance() string {
	host, _ := os.Hostname()
	if host == "" {
		host = "campus"
	}
	return fmt.Sprintf("%s-%d-%s", host, os.Getpid(), idgen.New("")[:6])
}

func New(db *sql.DB, rdb redis.UniversalClient, cfg config.App, opts ...Option) (*App, error) {
	if db == nil || rdb == nil {
		return nil, errors.New("database and redis are required")
	}
	var o options
	for _, opt := range opts {
		opt(&o)
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
		u, err := url.Parse(cfg.ModelURL)
		if err != nil || u.Host == "" || u.User != nil || (u.Scheme != "https" && (cfg.Production || u.Scheme != "http")) {
			return nil, errors.New("invalid MODEL_BASE_URL")
		}
	}
	if cfg.Production {
		u, err := url.Parse(cfg.PublicOrigin)
		if err != nil || u.Scheme != "https" || u.Host == "" || u.Path != "" {
			return nil, errors.New("production requires PUBLIC_ORIGIN=https://your-domain without path")
		}
	}

	gateway := o.gateway
	if gateway == nil {
		pay, err := alipay.New(cfg.Alipay)
		if err != nil {
			return nil, err
		}
		if pay != nil {
			gateway = pay
		}
	}
	sender := o.sender
	if sender == nil {
		cfg.AuthDelivery.Mode = strings.ToLower(strings.TrimSpace(cfg.AuthDelivery.Mode))
		var err error
		sender, err = verification.NewSender(cfg.AuthDelivery, cfg.Production)
		if err != nil {
			return nil, err
		}
	}
	emailAvailable, smsAvailable := verification.Availability(cfg.AuthDelivery)

	repos := redisrepo.New(rdb, cfg.RedisPrefix, cfg.Instance, cfg.PaymentWindow)
	limiter := redisrepo.NewLimiter(rdb, cfg.RedisPrefix)
	locks := lock.New(rdb, cfg.RedisPrefix+":lock")
	timeouts := redisrepo.NewTimeoutQueue(delayqueue.New(rdb, cfg.RedisPrefix, "order-timeout", delayqueue.Options{Visibility: 30 * time.Second, Workers: 4}))
	stores := mysqlrepo.New(db)

	startCtx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	var node *snowflake.Node
	var lease *snowflake.Lease
	if cfg.NodeID >= 0 {
		n, err := snowflake.NewNode(int64(cfg.NodeID))
		if err != nil {
			repos.Close()
			return nil, err
		}
		node = n
	} else {
		l, err := snowflake.AcquireLease(startCtx, locks, 30*time.Second)
		if err != nil {
			repos.Close()
			return nil, err
		}
		lease, node = l, l.Node
	}

	registry := service.NewRuntimeConnections(domain.ModelProvider{BaseURL: cfg.ModelURL, DefaultModel: cfg.ModelName, APIKey: cfg.ModelKey}, cfg.ModelProviders, cfg.MCPServers)
	httpClient := &http.Client{Timeout: 90 * time.Second}
	llmClient := llm.New(httpClient)
	mcpClient := mcp.New(&http.Client{}, registry)
	mediaStore := media.New(cfg.UploadDir)

	admins := service.NewAdminSet(cfg.AdminUserIDs)
	auth, err := service.NewAuth(stores.Auth, repos.Session, sender, limiter, admins, service.AuthConfig{Secret: cfg.AuthCodeSecret, Production: cfg.Production, LocalDelivery: cfg.AuthDelivery.Mode == "local", EmailAvailable: emailAvailable, SMSAvailable: smsAvailable, RequireSecret: cfg.Production && cfg.AuthDelivery.Mode == "live"})
	if err != nil {
		release(repos, lease)
		return nil, err
	}
	catalog := service.NewCatalog(stores.Catalog, repos.Cache, repos.Heat, limiter)
	orderIDs := orderIDs{node: node}
	orders := service.NewOrder(stores.Order, repos.Cache, repos.Heat, timeouts, repos.Live, repos.Flash, orderIDs, limiter, cfg.PaymentWindow)
	payments := service.NewPayment(stores.Payment, gateway, repos.Cache, timeouts, repos.Live, repos.Flash)
	chat := service.NewChat(stores.Chat, stores.Catalog, repos.Presence, repos.Live, limiter)
	flash := service.NewFlash(stores.Flash, repos.Cache, repos.Flash, stores.Order, orderIDs, timeouts, repos.Live, limiter, cfg.PaymentWindow)
	agentCatalog := service.NewAgentCatalog(stores.Agent, registry, mcpClient)
	agentRun := service.NewAgentRun(stores.Agent, agentCatalog, registry, llmClient, mcpClient, stores.Catalog, stores.Order, limiter)
	admin := service.NewAdmin(cfg.Instance, node.ID, func() map[string]any {
		st := db.Stats()
		return map[string]any{"open": st.OpenConnections, "in_use": st.InUse, "idle": st.Idle, "wait_count": st.WaitCount, "wait_ms": st.WaitDuration.Milliseconds()}
	}, repos.Cache, timeouts, flash)

	httpServer, err := httpapi.New(httpapi.Config{Production: cfg.Production, PublicOrigin: cfg.PublicOrigin, TrustedProxyCIDRs: cfg.TrustedProxyCIDRs}, httpapi.Deps{
		Auth: auth, Catalog: catalog, Orders: orders, Payments: payments, Chat: chat, Flash: flash,
		AgentCatalog: agentCatalog, AgentRun: agentRun, Admin: admin, Live: repos.Live, Limiter: limiter, Media: mediaStore,
		PingDB: db.PingContext, PingCache: func(ctx context.Context) error { return rdb.Ping(ctx).Err() },
	})
	if err != nil {
		release(repos, lease)
		return nil, err
	}
	a := &App{DB: db, Redis: rdb, HTTP: httpServer, Auth: auth, Catalog: catalog, Orders: orders, Payments: payments, Chat: chat, Flash: flash, Agents: agentCatalog, AgentRun: agentRun, Admin: admin, Timeouts: timeouts, Cache: repos.Cache, Live: repos.Live, Session: repos.Session, cfg: cfg, redis: repos, node: node, lease: lease, registry: registry}
	a.runner = job.New(locks, repos.Live, repos.Cache, timeouts, orders, payments, flash, auth)
	if err = auth.Init(startCtx); err != nil {
		release(repos, lease)
		return nil, err
	}
	if err = agentCatalog.Init(startCtx); err != nil {
		release(repos, lease)
		return nil, err
	}
	if err = agentRun.Recover(startCtx); err != nil {
		release(repos, lease)
		return nil, err
	}
	return a, nil
}

func release(repos *redisrepo.Repos, lease *snowflake.Lease) {
	repos.Close()
	if lease != nil {
		ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
		defer cancel()
		lease.Release(ctx)
	}
}

// orderIDs 把雪花节点适配成业务层的订单号生成器。
type orderIDs struct{ node *snowflake.Node }

func (o orderIDs) Next() (string, error) {
	n, err := o.node.Next()
	if err != nil {
		return "", err
	}
	return "CM" + strconv.FormatInt(n, 10), nil
}

func (a *App) Handler() http.Handler { return a.HTTP.Handler() }

func (a *App) Run(ctx context.Context) { a.runner.Run(ctx) }

func (a *App) Close(ctx context.Context) {
	a.redis.Close()
	if a.lease != nil {
		a.lease.Release(ctx)
	}
}

func (a *App) Instance() string { return a.cfg.Instance }

func (a *App) NodeID() int64 {
	if a.node == nil {
		return -1
	}
	return a.node.ID()
}

func (a *App) SetAdminUserIDs(csv string) { a.Auth.SetAdminUserIDs(csv) }

func (a *App) SetDefaultModel(provider domain.ModelProvider) {
	a.registry.SetDefaultModel(provider)
}

func (a *App) SetMCPServers(servers map[string]domain.MCPServer) {
	a.registry.ClearMCP()
	for k, v := range servers {
		a.registry.SetMCP(k, v)
	}
}

func (a *App) SeedDemo() error { return a.Catalog.SeedDemo(context.Background()) }

func (a *App) AgentAvailable() bool { return a.Agents.Available() }
