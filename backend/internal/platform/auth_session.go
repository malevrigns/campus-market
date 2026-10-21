package platform

import (
	"context"
	"encoding/json"
	"log/slog"
	"net/http"
	"strings"
	"time"
)

// Cookies do not have port scope. Development instances on one hostname must
// use distinct names, otherwise signing into a test database logs out the app.
func (s *Server) sessionCookieName(r *http.Request) string {
	if s.cfg.Production {
		return "__Host-campus_session"
	}
	return "campus_session_" + digest(strings.ToLower(r.Host))[:12]
}
func (s *Server) sessionCookie(r *http.Request) (*http.Cookie, error) {
	c, e := r.Cookie(s.sessionCookieName(r))
	if e == nil {
		return c, nil
	}
	// Read existing cookies during migration; new logins only write scoped names.
	return r.Cookie("campus_session")
}

// 会话缓存：每个登录请求都要校验会话（原来是 sessions/users/auth_identities 三表 JOIN），
// 改为先查 Redis，命中则不访问数据库。
//
// 一致性关键点：吊销（退出登录、改密码）写入 "revoked" 墓碑而不是简单 DEL，
// 回填使用 SET NX。这样"读请求刚从 DB 读到有效会话 → 用户退出 → 读请求再回填缓存"
// 的并发顺序下，回填会因为墓碑存在而失败，已吊销的会话不会被缓存复活。
const (
	sessionCacheTTL = 30 * time.Second
	sessionTomb     = "revoked"
)

type cachedSession struct {
	User    User  `json:"u"`
	Expires int64 `json:"e"`
}

func (s *Server) cachedSession(ctx context.Context, hash string) (*User, bool, bool) {
	raw, err := s.rdb.Get(ctx, s.keys.Of("sess", hash)).Bytes()
	if err != nil {
		return nil, false, false
	}
	if string(raw) == sessionTomb {
		return nil, false, true
	}
	var cs cachedSession
	if json.Unmarshal(raw, &cs) != nil || cs.Expires <= now() {
		return nil, false, false
	}
	u := cs.User
	u.IsAdmin = s.isAgentAdmin(u.ID)
	return &u, true, false
}

func (s *Server) storeSession(ctx context.Context, hash string, u *User, expires int64) {
	ttl := min(sessionCacheTTL, time.Duration(expires-now())*time.Millisecond)
	if ttl <= 0 {
		return
	}
	b, _ := json.Marshal(cachedSession{User: *u, Expires: expires})
	pipe := s.rdb.Pipeline()
	pipe.SetNX(ctx, s.keys.Of("sess", hash), b, ttl)
	pipe.SAdd(ctx, s.keys.Of("sess-user", u.ID), hash)
	pipe.Expire(ctx, s.keys.Of("sess-user", u.ID), 8*24*time.Hour)
	if _, err := pipe.Exec(ctx); err != nil {
		slog.Debug("session cache write failed", "error", err)
	}
}

// revokeSessions 为已删除的会话写入墓碑，墓碑存活时间大于缓存 TTL。
func (s *Server) revokeSessions(ctx context.Context, hashes ...string) {
	if len(hashes) == 0 {
		return
	}
	ctx = context.WithoutCancel(ctx)
	pipe := s.rdb.Pipeline()
	for _, h := range hashes {
		pipe.Set(ctx, s.keys.Of("sess", h), sessionTomb, 2*sessionCacheTTL)
	}
	if _, err := pipe.Exec(ctx); err != nil {
		slog.Warn("session revoke cache failed", "error", err)
	}
}

// refreshUserSessions 在用户资料变更后删除该用户的会话缓存（下次请求回源拿到新资料）。
// 这里是普通删除而非墓碑，并发回填最多让旧资料多存活一个缓存 TTL，延迟再删一次进一步缩小窗口。
func (s *Server) refreshUserSessions(ctx context.Context, uid string) {
	ctx = context.WithoutCancel(ctx)
	run := func() {
		cctx, cancel := context.WithTimeout(ctx, time.Second)
		defer cancel()
		hashes, err := s.rdb.SMembers(cctx, s.keys.Of("sess-user", uid)).Result()
		if err != nil || len(hashes) == 0 {
			return
		}
		keys := make([]string, len(hashes))
		for i, h := range hashes {
			keys[i] = s.keys.Of("sess", h)
		}
		// 不能删除墓碑，否则已吊销的会话可能被重新缓存。
		vals, err := s.rdb.MGet(cctx, keys...).Result()
		if err != nil {
			return
		}
		live := []string{}
		for i, v := range vals {
			if str, ok := v.(string); ok && str != sessionTomb {
				live = append(live, keys[i])
			}
		}
		if len(live) > 0 {
			s.rdb.Del(cctx, live...)
		}
	}
	run()
	time.AfterFunc(500*time.Millisecond, run)
}
