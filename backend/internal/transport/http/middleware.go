package httpapi

import (
	"bufio"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"log/slog"
	"net"
	"net/http"
	"strconv"
	"strings"
	"time"

	"qingfly/edu-mall/internal/pkg/idgen"
)

// statusWriter 记录状态码与字节数，并透传 Flush/Hijack/Unwrap，
// 保证 SSE 与 WebSocket 升级不被中间件包装破坏。
type statusWriter struct {
	http.ResponseWriter
	status int
	bytes  int
}

func (w *statusWriter) WriteHeader(code int) {
	if w.status == 0 {
		w.status = code
	}
	w.ResponseWriter.WriteHeader(code)
}
func (w *statusWriter) Write(b []byte) (int, error) {
	if w.status == 0 {
		w.status = http.StatusOK
	}
	n, err := w.ResponseWriter.Write(b)
	w.bytes += n
	return n, err
}
func (w *statusWriter) Flush() {
	if f, ok := w.ResponseWriter.(http.Flusher); ok {
		f.Flush()
	}
}
func (w *statusWriter) Hijack() (net.Conn, *bufio.ReadWriter, error) {
	if h, ok := w.ResponseWriter.(http.Hijacker); ok {
		w.status = http.StatusSwitchingProtocols
		return h.Hijack()
	}
	return nil, nil, errors.New("hijack not supported")
}
func (w *statusWriter) Unwrap() http.ResponseWriter { return w.ResponseWriter }

// Handler 是完整中间件链：请求 ID → panic 恢复 → 访问日志 → 安全头/来源校验 →
// 分布式限流 → 请求体大小限制 → 路由。
func (s *Server) Handler() http.Handler {
	return http.HandlerFunc(func(rw http.ResponseWriter, r *http.Request) {
		start := time.Now()
		reqID := idgen.New("req_")
		w := &statusWriter{ResponseWriter: rw}
		w.Header().Set("X-Request-ID", reqID)
		w.Header().Set("X-Content-Type-Options", "nosniff")
		w.Header().Set("Referrer-Policy", "same-origin")
		w.Header().Set("Cache-Control", "no-store")
		defer func() {
			if e := recover(); e != nil {
				slog.Error("request panic", "path", r.URL.Path, "error", e, "request_id", reqID)
				if w.status == 0 {
					writeError(w, domainInternal())
				}
			}
			if r.URL.Path == "/api/v1/health" {
				return
			}
			elapsed := time.Since(start)
			level := slog.LevelInfo
			if w.status >= 500 {
				level = slog.LevelError
			} else if elapsed > 500*time.Millisecond && w.status != http.StatusSwitchingProtocols && !strings.HasPrefix(w.Header().Get("Content-Type"), "text/event-stream") {
				level = slog.LevelWarn
			}
			slog.Log(r.Context(), level, "http", "method", r.Method, "path", r.URL.Path, "status", w.status, "bytes", w.bytes, "ms", elapsed.Milliseconds(), "ip", s.clientIP(r), "request_id", reqID)
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
				writeError(w, domainForbidden("请求来源不被允许"))
				return
			}
		}
		if r.URL.Path != "/api/v1/health" && !s.allowRequest(w, r) {
			return
		}
		maxBody := int64(1 << 20)
		if r.URL.Path == "/api/v1/uploads" {
			maxBody = 6 << 20
		}
		r.Body = http.MaxBytesReader(w, r.Body, maxBody)
		s.mux.ServeHTTP(w, r)
	})
}

// allowRequest 是入口级限流：所有接口按 IP 令牌桶；凭证接口按 IP 滑动窗口；
// 支付回调单独计数，避免与普通流量互相影响。
func (s *Server) allowRequest(w http.ResponseWriter, r *http.Request) bool {
	ip := s.clientIP(r)
	ctx := r.Context()
	var retry time.Duration
	var allowed bool
	switch {
	case r.URL.Path == "/api/v1/payments/alipay/notify":
		retry, allowed = s.deps.Limiter.Bucket(ctx, "ip:"+ip+":payment-callback", 20, 1200)
	case strings.HasPrefix(r.URL.Path, "/api/v1/auth/") && r.Method == "POST" && r.URL.Path != "/api/v1/auth/codes" && r.URL.Path != "/api/v1/auth/dev-inbox":
		retry, allowed = s.deps.Limiter.Window(ctx, "ip:"+ip+":auth", 30, time.Minute)
	default:
		retry, allowed = s.deps.Limiter.Bucket(ctx, "ip:"+ip, 50, 500)
	}
	if allowed {
		return true
	}
	seconds := int(retry.Seconds() + 0.999)
	if seconds < 1 {
		seconds = 1
	}
	w.Header().Set("Retry-After", strconv.Itoa(seconds))
	writeError(w, domainRetryable(429, "请求过于频繁，请稍后重试", seconds))
	return false
}

func digestToken(token string) string {
	h := sha256.Sum256([]byte(token))
	return hex.EncodeToString(h[:])
}
