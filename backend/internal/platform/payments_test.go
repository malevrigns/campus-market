package platform

import (
	"crypto"
	"crypto/rand"
	"crypto/rsa"
	"crypto/sha256"
	"encoding/base64"
	"net/url"
	"testing"
)

func TestRSACallbackValidationAndReplay(t *testing.T) {
	a, _ := testApp(t)
	private, e := rsa.GenerateKey(rand.Reader, 2048)
	if e != nil {
		t.Fatal(e)
	}
	a.pay = &Alipay{appID: "app", sellerID: "merchant", public: &private.PublicKey}
	seller := account(t, a, "seller")
	buyer := account(t, a, "buyer")
	pid := publish(t, seller)
	_, o := order(buyer, pid, "payment-test-key")
	oid := o["id"].(string)
	if _, e = a.db.Exec("INSERT INTO payments(order_id,checkout_url,created_at) VALUES(?,?,?)", oid, "https://example.test", now()); e != nil {
		t.Fatal(e)
	}
	v := url.Values{"app_id": {"app"}, "seller_id": {"merchant"}, "notify_id": {"callback-1"}, "trade_no": {"trade-1"}, "out_trade_no": {oid}, "trade_status": {"TRADE_SUCCESS"}, "total_amount": {"123.45"}, "sign_type": {"RSA2"}}
	sign := func() {
		h := sha256.Sum256([]byte(canonical(v, true)))
		sig, e := rsa.SignPKCS1v15(rand.Reader, private, crypto.SHA256, h[:])
		if e != nil {
			t.Fatal(e)
		}
		v.Set("sign", base64.StdEncoding.EncodeToString(sig))
	}
	sign()
	v.Set("total_amount", "0.01")
	if e = a.acceptPayment(v); e == nil {
		t.Fatal("tampered amount accepted")
	}
	sign()
	if e = a.acceptPayment(v); e == nil {
		t.Fatal("signed wrong amount accepted")
	}
	v.Set("total_amount", "123.45")
	sign()
	for i := 0; i < 3; i++ {
		if e = a.acceptPayment(v); e != nil {
			t.Fatal(e)
		}
	}
	var count int
	if e = a.db.QueryRow("SELECT count(*) FROM order_events WHERE order_id=? AND status='paid'", oid).Scan(&count); e != nil || count != 1 {
		t.Fatalf("replay %d %v", count, e)
	}
	v.Set("trade_no", "different-trade")
	sign()
	if e = a.acceptPayment(v); e == nil {
		t.Fatal("trade replacement accepted")
	}
}
func TestMoneyParsing(t *testing.T) {
	for _, v := range []string{"1.001", "-1.00", "NaN", "1e3", "1.", "+1.00", "0.0.1"} {
		if _, e := parseMoney(v); e == nil {
			t.Errorf("accepted %q", v)
		}
	}
	for v, want := range map[string]int64{"1": 100, "1.2": 120, "0.01": 1, "123.45": 12345} {
		if got, e := parseMoney(v); e != nil || got != want {
			t.Errorf("%s %d %v", v, got, e)
		}
	}
}
