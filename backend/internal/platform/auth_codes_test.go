package platform

import (
	"context"
	"errors"
	"net/http/httptest"
	"testing"
)

func testCode(t *testing.T, c *testClient, channel, target, purpose string) map[string]any {
	t.Helper()
	status, d := c.call("POST", "/api/v1/auth/codes", map[string]any{"channel": channel, "target": target, "purpose": purpose})
	if status != 200 {
		t.Fatalf("request code: %d %v", status, d)
	}
	status, inbox := c.call("POST", "/api/v1/auth/dev-inbox", map[string]any{"challenge_id": d["challenge_id"], "delivery_token": d["delivery_token"]})
	if status != 200 {
		t.Fatalf("inbox %d %v", status, inbox)
	}
	return map[string]any{"channel": channel, "target": target, "challenge_id": d["challenge_id"], "code": inbox["code"]}
}
func TestVerifiedEmailRegistrationAndReplay(t *testing.T) {
	s, _ := testApp(t)
	c := &testClient{t: t, app: s.Handler()}
	body := map[string]any{"channel": "email", "target": "student@example.test", "name": "王", "password": "Password1"}
	if status, _ := c.call("POST", "/api/v1/auth/register", body); status != 400 {
		t.Fatalf("register without proof: %d", status)
	}
	proof := testCode(t, c, "email", "student@example.test", "register")
	for k, v := range proof {
		body[k] = v
	}
	status, u := c.call("POST", "/api/v1/auth/register", body)
	if status != 201 || u["email_verified"] != true {
		t.Fatalf("verified registration %d %v", status, u)
	}
	if status, _ := c.call("POST", "/api/v1/auth/register", body); status != 409 {
		t.Fatalf("registration replay %d", status)
	}
	var stored string
	s.db.QueryRow("SELECT code_hash FROM auth_codes WHERE id=?", proof["challenge_id"]).Scan(&stored)
	if stored == proof["code"] {
		t.Fatal("plaintext verification code persisted")
	}
}
func TestSMSRegisterCodeLoginAndPasswordReset(t *testing.T) {
	s, _ := testApp(t)
	c := &testClient{t: t, app: s.Handler()}
	proof := testCode(t, c, "sms", "13800138000", "register")
	proof["name"] = "手机同学"
	proof["password"] = "OldPass123"
	status, u := c.call("POST", "/api/v1/auth/register", proof)
	if status != 201 || u["phone"] != "+8613800138000" || u["email"] != "" || u["phone_verified"] != true {
		t.Fatalf("phone registration %d %v", status, u)
	}
	originalCookie := c.cookie
	s.db.Exec("UPDATE auth_codes SET created_at=created_at-61000")
	loginProof := testCode(t, c, "sms", "+86 13800138000", "login")
	if status, _ := c.call("POST", "/api/v1/auth/code-login", loginProof); status != 200 {
		t.Fatalf("sms login %d", status)
	}
	if status, _ := c.call("POST", "/api/v1/auth/code-login", loginProof); status != 400 {
		t.Fatalf("code reused %d", status)
	}
	s.db.Exec("UPDATE auth_codes SET created_at=created_at-61000")
	reset := testCode(t, c, "sms", "13800138000", "reset_password")
	reset["password"] = "NewPass456"
	if status, _ := c.call("POST", "/api/v1/auth/password/reset", reset); status != 200 {
		t.Fatalf("reset %d", status)
	}
	c.cookie = originalCookie
	if status, _ := c.call("GET", "/api/v1/auth/me", nil); status != 401 {
		t.Fatal("password reset did not revoke sessions")
	}
	if status, _ := c.call("POST", "/api/v1/auth/login", map[string]string{"account": "13800138000", "password": "OldPass123"}); status != 401 {
		t.Fatal("old password still works")
	}
	if status, _ := c.call("POST", "/api/v1/auth/login", map[string]string{"account": "13800138000", "password": "NewPass456"}); status != 200 {
		t.Fatalf("new password login %d", status)
	}
}
func TestCodeLimitsExpiryAndPurpose(t *testing.T) {
	s, _ := testApp(t)
	c := &testClient{t: t, app: s.Handler()}
	p := testCode(t, c, "email", "limits@example.test", "register")
	if status, _ := c.call("POST", "/api/v1/auth/codes", map[string]string{"channel": "email", "target": "limits@example.test", "purpose": "register"}); status != 429 {
		t.Fatalf("resend cooldown %d", status)
	}
	p["name"] = "同学"
	p["password"] = "Password1"
	good := p["code"].(string)
	bad := "000000"
	if good == bad {
		bad = "111111"
	}
	p["code"] = bad
	for i := 0; i < 5; i++ {
		if status, _ := c.call("POST", "/api/v1/auth/register", p); status != 400 {
			t.Fatalf("wrong guess %d %d", i, status)
		}
	}
	p["code"] = good
	if status, _ := c.call("POST", "/api/v1/auth/register", p); status != 429 {
		t.Fatalf("attempt lock %d", status)
	}
	q := testCode(t, c, "email", "expired@example.test", "register")
	q["name"] = "同学"
	q["password"] = "Password1"
	s.db.Exec("UPDATE auth_codes SET expires_at=0 WHERE id=?", q["challenge_id"])
	if status, _ := c.call("POST", "/api/v1/auth/register", q); status != 400 {
		t.Fatalf("expired code %d", status)
	}
	user := account(t, s, "purpose")
	s.db.Exec("UPDATE auth_codes SET created_at=created_at-61000")
	wrongPurpose := testCode(t, user, "email", "purpose@campus.test", "login")
	wrongPurpose["password"] = "Change123"
	if status, _ := user.call("POST", "/api/v1/auth/password/reset", wrongPurpose); status != 400 {
		t.Fatalf("login code used for reset %d", status)
	}
}

type failedSender struct{}

func (failedSender) Send(context.Context, VerificationMessage) error {
	return errors.New("provider down")
}
func TestFailedDeliveryCannotVerify(t *testing.T) {
	s, _ := testApp(t)
	s.verification.sender = failedSender{}
	c := &testClient{t: t, app: s.Handler()}
	if status, _ := c.call("POST", "/api/v1/auth/codes", map[string]string{"channel": "sms", "target": "13900139000", "purpose": "register"}); status != 502 {
		t.Fatalf("provider failure falsely accepted %d", status)
	}
	var state string
	s.db.QueryRow("SELECT state FROM auth_codes").Scan(&state)
	if state != "failed" {
		t.Fatalf("failure state %s", state)
	}
}
func TestIdentityBindingAndInboxToken(t *testing.T) {
	s, _ := testApp(t)
	c := account(t, s, "binding")
	proof := testCode(t, c, "sms", "13700137000", "bind")
	if status, u := c.call("POST", "/api/v1/auth/bind", proof); status != 200 || u["phone_verified"] != true {
		t.Fatalf("bind %d %v", status, u)
	}
	if status, _ := c.call("POST", "/api/v1/auth/dev-inbox", map[string]any{"challenge_id": proof["challenge_id"], "delivery_token": "wrong"}); status != 404 {
		t.Fatal("inbox readable without delivery token")
	}
}

func TestResetRejectsPreviouslyAuthenticatedPasswordAndPendingCodes(t *testing.T) {
	s, _ := testApp(t)
	c := account(t, s, "reset_race")
	_, profile := c.call("GET", "/api/v1/auth/me", nil)
	uid := profile["id"].(string)
	var oldHash string
	if e := s.db.QueryRow("SELECT password FROM users WHERE id=?", uid).Scan(&oldHash); e != nil {
		t.Fatal(e)
	}
	pending := testCode(t, c, "email", "reset_race@campus.test", "login")
	proof := testCode(t, c, "email", "reset_race@campus.test", "reset_password")
	proof["password"] = "Updated123"
	if status, _ := c.call("POST", "/api/v1/auth/password/reset", proof); status != 200 {
		t.Fatalf("reset %d", status)
	}
	w := httptest.NewRecorder()
	r := httptest.NewRequest("POST", "/api/v1/auth/login", nil)
	if e := s.setSession(w, r, &User{ID: uid}, oldHash); e == nil {
		t.Fatal("previously checked old password recreated a session")
	}
	if status, _ := c.call("POST", "/api/v1/auth/code-login", pending); status != 400 {
		t.Fatalf("old pending OTP survived reset %d", status)
	}
	var count int
	s.db.QueryRow("SELECT COUNT(*) FROM sessions WHERE user_id=?", uid).Scan(&count)
	if count != 0 {
		t.Fatal("session survived password reset")
	}
}
