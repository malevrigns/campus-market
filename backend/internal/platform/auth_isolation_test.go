package platform

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/cookiejar"
	"net/http/httptest"
	"testing"
)

func TestLoginSessionsDoNotCollideAcrossLocalPorts(t *testing.T) {
	first, _ := testApp(t)
	second, _ := testApp(t)
	account(t, first, "portone")
	account(t, second, "porttwo")
	one, two := httptest.NewServer(first.Handler()), httptest.NewServer(second.Handler())
	defer one.Close()
	defer two.Close()
	jar, _ := cookiejar.New(nil)
	client := &http.Client{Jar: jar}
	for _, entry := range []struct{ url, email string }{{one.URL, "portone@campus.test"}, {two.URL, "porttwo@campus.test"}} {
		body, _ := json.Marshal(map[string]string{"email": entry.email, "password": "Testing-strong-123"})
		resp, e := client.Post(entry.url+"/api/v1/auth/login", "application/json", bytes.NewReader(body))
		if e != nil {
			t.Fatal(e)
		}
		resp.Body.Close()
		if resp.StatusCode != 200 {
			t.Fatalf("login: %d", resp.StatusCode)
		}
	}
	for _, base := range []string{one.URL, two.URL} {
		resp, e := client.Get(base + "/api/v1/auth/me")
		if e != nil {
			t.Fatal(e)
		}
		resp.Body.Close()
		if resp.StatusCode != 200 {
			t.Fatalf("another local environment overwrote login cookie: %d", resp.StatusCode)
		}
	}
}
