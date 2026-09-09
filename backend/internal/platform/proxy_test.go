package platform

import (
	"net/http/httptest"
	"net/netip"
	"testing"
)

func TestTrustedProxyOnly(t *testing.T) {
	s, _ := testApp(t)
	s.trusted = []netip.Prefix{netip.MustParsePrefix("127.0.0.1/32")}
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
func TestTrafficCannotExhaustCallbackBucket(t *testing.T) {
	s, _ := testApp(t)
	for i := 0; i < 601; i++ {
		w := httptest.NewRecorder()
		s.Handler().ServeHTTP(w, httptest.NewRequest("GET", "/api/v1/config", nil))
	}
	w := httptest.NewRecorder()
	s.Handler().ServeHTTP(w, httptest.NewRequest("POST", "/api/v1/payments/alipay/notify", nil))
	if w.Code == 429 {
		t.Fatal("shop traffic exhausted payment callback bucket")
	}
}
