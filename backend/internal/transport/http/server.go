package httpapi

import (
	"context"
	"errors"
	"io"
	"net"
	"net/http"
	"net/netip"
	"net/url"
	"strings"
	"time"

	"github.com/coder/websocket"

	"qingfly/edu-mall/internal/domain"
	"qingfly/edu-mall/internal/port"
	"qingfly/edu-mall/internal/service"
)

type Config struct {
	Production        bool
	PublicOrigin      string
	TrustedProxyCIDRs string
}

type Deps struct {
	Auth         *service.AuthService
	Catalog      *service.CatalogService
	Orders       *service.OrderService
	Payments     *service.PaymentService
	Chat         *service.ChatService
	Flash        *service.FlashService
	AgentCatalog *service.AgentCatalogService
	AgentRun     *service.AgentRunService
	Admin        *service.AdminService
	Live         port.LiveNotifier
	Limiter      port.RateLimiter
	Media        port.MediaStore
	PingDB       func(context.Context) error
	PingCache    func(context.Context) error
}

type Server struct {
	cfg         Config
	deps        Deps
	mux         *http.ServeMux
	trusted     []netip.Prefix
	uploadSlots chan struct{}
}

// New 创建 HTTP 应用并注册路由。基础设施与业务服务已由组合根构造。
func New(cfg Config, deps Deps) (*Server, error) {
	s := &Server{cfg: cfg, deps: deps, mux: http.NewServeMux(), uploadSlots: make(chan struct{}, 2)}
	for _, cidr := range strings.Split(cfg.TrustedProxyCIDRs, ",") {
		if strings.TrimSpace(cidr) == "" {
			continue
		}
		p, err := netip.ParsePrefix(strings.TrimSpace(cidr))
		if err != nil {
			return nil, errors.New("invalid TRUSTED_PROXY_CIDRS")
		}
		s.trusted = append(s.trusted, p)
	}
	s.routes()
	return s, nil
}

func (s *Server) routes() {
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
	s.agentCatalogRoutes()
}

func (s *Server) route(pattern string, authenticated bool, fn endpoint) {
	s.mux.HandleFunc(pattern, func(w http.ResponseWriter, r *http.Request) {
		var user *domain.User
		var err error
		if authenticated {
			user, err = s.currentUser(r)
			if err != nil {
				writeError(w, err)
				return
			}
		}
		if err = fn(w, r, user); err != nil {
			writeError(w, err)
		}
	})
}

func (s *Server) admin(pattern string, fn endpoint) {
	s.route(pattern, true, func(w http.ResponseWriter, r *http.Request, u *domain.User) error {
		if !s.deps.Auth.IsAdmin(u.ID) {
			return domain.Forbidden("仅管理员可执行该操作")
		}
		return fn(w, r, u)
	})
}

func (s *Server) currentUser(r *http.Request) (*domain.User, error) {
	c, err := s.sessionCookie(r)
	if err != nil {
		return nil, domain.Unauthorized("请先登录")
	}
	u, err := s.deps.Auth.UserBySession(r.Context(), digestToken(c.Value))
	if err != nil {
		return nil, err
	}
	return &u, nil
}

func (s *Server) sessionCookieName(r *http.Request) string {
	if s.cfg.Production {
		return "__Host-campus_session"
	}
	return "campus_session_" + digestToken(strings.ToLower(r.Host))[:12]
}

func (s *Server) sessionCookie(r *http.Request) (*http.Cookie, error) {
	c, err := r.Cookie(s.sessionCookieName(r))
	if err == nil {
		return c, nil
	}
	return r.Cookie("campus_session")
}

func (s *Server) sessionHash(r *http.Request) string {
	if c, err := s.sessionCookie(r); err == nil {
		return digestToken(c.Value)
	}
	return ""
}

func (s *Server) writeSession(w http.ResponseWriter, r *http.Request, session domain.Session) {
	http.SetCookie(w, &http.Cookie{Name: s.sessionCookieName(r), Value: session.Token, Path: "/", HttpOnly: true, Secure: s.cfg.Production, SameSite: http.SameSiteStrictMode, Expires: time.UnixMilli(session.ExpiresAt), MaxAge: 7 * 86400})
}

func (s *Server) clearSessionCookie(w http.ResponseWriter, r *http.Request) {
	http.SetCookie(w, &http.Cookie{Name: s.sessionCookieName(r), Value: "", Path: "/", HttpOnly: true, Secure: s.cfg.Production, SameSite: http.SameSiteStrictMode, MaxAge: -1})
	if c, err := s.sessionCookie(r); err == nil && c.Name == "campus_session" {
		http.SetCookie(w, &http.Cookie{Name: "campus_session", Value: "", Path: "/", HttpOnly: true, Secure: s.cfg.Production, SameSite: http.SameSiteStrictMode, MaxAge: -1})
	}
}

func (s *Server) clientIP(r *http.Request) string {
	host, _, err := net.SplitHostPort(r.RemoteAddr)
	if err != nil {
		return r.RemoteAddr
	}
	addr, err := netip.ParseAddr(host)
	if err != nil {
		return host
	}
	for _, prefix := range s.trusted {
		if prefix.Contains(addr) {
			if original, err := netip.ParseAddr(r.Header.Get("X-Real-IP")); err == nil {
				return original.String()
			}
			break
		}
	}
	return host
}

func (s *Server) visitorID(r *http.Request) string {
	if c, err := s.sessionCookie(r); err == nil {
		return "s:" + digestToken(c.Value)[:16]
	}
	return "ip:" + s.clientIP(r)
}

func (s *Server) health(w http.ResponseWriter, r *http.Request, _ *domain.User) error {
	ctx, cancel := context.WithTimeout(r.Context(), time.Second)
	defer cancel()
	if err := s.deps.PingDB(ctx); err != nil {
		return domain.Unavailable("数据库不可用")
	}
	if err := s.deps.PingCache(ctx); err != nil {
		return domain.Unavailable("缓存不可用")
	}
	return result(w, 200, map[string]any{"status": "ok"})
}

func (s *Server) config(w http.ResponseWriter, r *http.Request, _ *domain.User) error {
	email, sms, local := s.deps.Auth.Delivery()
	return result(w, 200, map[string]any{
		"payments":        map[string]bool{"alipay": s.deps.Payments.Enabled()},
		"agent_available": s.deps.AgentCatalog.Available(),
		"categories":      domain.Categories,
		"campuses":        domain.Campuses,
		"auth":            map[string]any{"email_code": email, "sms_code": sms, "local_inbox": local, "password_min_length": 8},
	})
}

func (s *Server) runtimeStats(w http.ResponseWriter, r *http.Request, _ *domain.User) error {
	stats, err := s.deps.Admin.Runtime(r.Context())
	if err != nil {
		return err
	}
	return result(w, 200, stats)
}

func (s *Server) upload(w http.ResponseWriter, r *http.Request, u *domain.User) error {
	select {
	case s.uploadSlots <- struct{}{}:
		defer func() { <-s.uploadSlots }()
	default:
		return domain.Retryable(domain.KindTooMany, "图片处理中，请稍后再上传", 0)
	}
	if retry, allowed := s.deps.Limiter.Window(r.Context(), "upload:"+u.ID, 15, time.Minute); !allowed {
		return domain.Retryable(domain.KindTooMany, "上传太频繁，请稍后重试", int(retry.Seconds()+0.999))
	}
	if err := r.ParseMultipartForm(6 << 20); err != nil {
		return domain.Invalid("请选择 5 MB 以内的图片")
	}
	defer r.MultipartForm.RemoveAll()
	f, _, err := r.FormFile("image")
	if err != nil {
		return domain.Invalid("请上传图片")
	}
	defer f.Close()
	b, err := io.ReadAll(io.LimitReader(f, 5<<20+1))
	if err != nil {
		return err
	}
	if len(b) > 5<<20 {
		return domain.NewError(domain.ErrorKind(413), "图片不能超过 5 MB")
	}
	url, err := s.deps.Media.SaveJPEG(r.Context(), b)
	if err != nil {
		return err
	}
	return result(w, 201, map[string]string{"url": url})
}

func (s *Server) media(w http.ResponseWriter, r *http.Request) {
	name := r.PathValue("file")
	reader, modTime, err := s.deps.Media.Open(name)
	if err != nil {
		http.NotFound(w, r)
		return
	}
	if closer, ok := reader.(io.Closer); ok {
		defer closer.Close()
	}
	w.Header().Set("Content-Type", "image/jpeg")
	w.Header().Set("Cache-Control", "public, max-age=31536000, immutable")
	http.ServeContent(w, r, name, modTime, reader)
}

func (s *Server) paymentNotify(w http.ResponseWriter, r *http.Request) {
	if err := r.ParseForm(); err != nil {
		http.Error(w, "failure", 400)
		return
	}
	if err := s.deps.Payments.Notify(r.Context(), r.PostForm); err != nil {
		http.Error(w, "failure", errorStatus(err))
		return
	}
	w.Header().Set("Content-Type", "text/plain; charset=utf-8")
	_, _ = w.Write([]byte("success"))
}

func (s *Server) websocket(w http.ResponseWriter, r *http.Request) {
	u, err := s.currentUser(r)
	if err != nil {
		writeError(w, err)
		return
	}
	ch, ok := s.deps.Live.Subscribe(u.ID)
	if !ok {
		writeError(w, domain.Retryable(domain.KindTooMany, "连接数过多，请关闭其他标签页", 0))
		return
	}
	defer s.deps.Live.Unsubscribe(u.ID, ch)
	opts := &websocket.AcceptOptions{}
	if s.cfg.PublicOrigin != "" {
		origin, err := url.Parse(s.cfg.PublicOrigin)
		if err == nil {
			opts.OriginPatterns = []string{origin.Host}
		}
	}
	c, err := websocket.Accept(w, r, opts)
	if err != nil {
		return
	}
	defer c.CloseNow()
	c.SetReadLimit(4096)
	ctx := c.CloseRead(r.Context())
	s.deps.Chat.TouchPresence(ctx, u.ID)
	ticker := time.NewTicker(25 * time.Second)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case b, open := <-ch:
			if !open {
				_ = c.Close(websocket.StatusTryAgainLater, "resync")
				return
			}
			writeCtx, cancel := context.WithTimeout(ctx, 5*time.Second)
			err = c.Write(writeCtx, websocket.MessageText, b)
			cancel()
			if err != nil {
				return
			}
		case <-ticker.C:
			if _, err = s.currentUser(r); err != nil {
				_ = c.Close(websocket.StatusPolicyViolation, "session expired")
				return
			}
			s.deps.Chat.TouchPresence(ctx, u.ID)
			pingCtx, cancel := context.WithTimeout(ctx, 10*time.Second)
			err = c.Ping(pingCtx)
			cancel()
			if err != nil {
				return
			}
		}
	}
}
