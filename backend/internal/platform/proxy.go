package platform

import (
	"net"
	"net/http"
	"net/netip"
)

// Only a configured proxy may provide X-Real-IP. Edge proxy config must overwrite
// that header, never append or forward client input. API ports stay private.
func (s *Server) clientIP(r *http.Request) string {
	host, _, e := net.SplitHostPort(r.RemoteAddr)
	if e != nil {
		return r.RemoteAddr
	}
	addr, e := netip.ParseAddr(host)
	if e != nil {
		return host
	}
	for _, prefix := range s.trusted {
		if prefix.Contains(addr) {
			if original, e := netip.ParseAddr(r.Header.Get("X-Real-IP")); e == nil {
				return original.String()
			}
			break
		}
	}
	return host
}
