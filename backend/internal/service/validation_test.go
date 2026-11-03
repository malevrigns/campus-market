package service

import (
	"testing"

	"qingfly/edu-mall/internal/domain"
)

func TestNormalizeIdentity(t *testing.T) {
	if v, err := normalizeIdentity("email", " Student@Example.COM "); err != nil || v != "student@example.com" {
		t.Fatalf("email normalization: %q %v", v, err)
	}
	if v, err := normalizeIdentity("sms", "+86 138-0013-8000"); err != nil || v != "+8613800138000" {
		t.Fatalf("sms normalization: %q %v", v, err)
	}
	for _, tc := range []struct{ channel, target string }{{"email", "not-an-email"}, {"sms", "12345"}, {"fax", "x"}} {
		if _, err := normalizeIdentity(tc.channel, tc.target); err == nil {
			t.Fatalf("accepted invalid identity %+v", tc)
		}
	}
}

func TestPasswordAndImageValidation(t *testing.T) {
	if !validPassword("Password1") || validPassword("short") || validPassword("        ") {
		t.Fatal("password policy mismatch")
	}
	if !validImageURL("https://images.example.com/a.jpg") || !validImageURL("/media/img_0123456789abcdef0123456789abcdef.jpg") {
		t.Fatal("valid image rejected")
	}
	for _, v := range []string{"http://images.example.com/a.jpg", "javascript:alert(1)", "/media/../etc/passwd", "https://user:pass@example.com/a.jpg"} {
		if validImageURL(v) {
			t.Fatalf("unsafe image accepted: %s", v)
		}
	}
}

func TestFlashPhase(t *testing.T) {
	s := domain.FlashSale{Status: "scheduled", StartAt: 100, EndAt: 200}
	if Phase(s, 50) != "upcoming" || Phase(s, 150) != "active" || Phase(s, 250) != "ended" {
		t.Fatal("phase calculation mismatch")
	}
	s.Status = "cancelled"
	if Phase(s, 150) != "cancelled" {
		t.Fatal("cancelled phase mismatch")
	}
}
