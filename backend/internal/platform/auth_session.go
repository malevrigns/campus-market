package platform

import (
	"net/http"
	"strings"
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
