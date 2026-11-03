package httpapi

import (
	"net/http/httptest"
	"net/netip"
	"testing"
)

func TestTrustedProxyOnly(t *testing.T) {
	s := &Server{cfg: Config{}, trusted: []netip.Prefix{netip.MustParsePrefix("127.0.0.1/32")}}
	r := httptest.NewRequest("GET", "/", nil)
	r.RemoteAddr = "192.0.2.1:1234"
	r.Header.Set("X-Real-IP", "203.0.113.5")
	if s.clientIP(r) != "192.0.2.1" {
		t.Fatal("untrusted header accepted")
	}
	r.RemoteAddr = "127.0.0.1:1234"
	if s.clientIP(r) != "203.0.113.5" {
		t.Fatal("trusted proxy not recognized")
	}
}
