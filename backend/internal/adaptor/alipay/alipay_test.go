package alipay

import (
	"crypto"
	"crypto/rand"
	"crypto/rsa"
	"crypto/sha256"
	"crypto/x509"
	"encoding/base64"
	"encoding/pem"
	"net/url"
	"testing"
)

func testKeys(t *testing.T) (*rsa.PrivateKey, string, string) {
	t.Helper()
	key, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		t.Fatal(err)
	}
	priv, _ := x509.MarshalPKCS8PrivateKey(key)
	pub, _ := x509.MarshalPKIXPublicKey(&key.PublicKey)
	return key, string(pem.EncodeToMemory(&pem.Block{Type: "PRIVATE KEY", Bytes: priv})), string(pem.EncodeToMemory(&pem.Block{Type: "PUBLIC KEY", Bytes: pub}))
}

func TestVerifyNotificationSignatureAndAmount(t *testing.T) {
	key, privPEM, pubPEM := testKeys(t)
	g, err := New(Config{AppID: "app", SellerID: "merchant", PrivateKey: privPEM, PublicKey: pubPEM, PublicOrigin: "https://example.test"})
	if err != nil {
		t.Fatal(err)
	}
	v := url.Values{"app_id": {"app"}, "seller_id": {"merchant"}, "notify_id": {"n1"}, "trade_no": {"t1"}, "out_trade_no": {"CM1"}, "trade_status": {"TRADE_SUCCESS"}, "total_amount": {"123.45"}, "sign_type": {"RSA2"}}
	h := sha256.Sum256([]byte(canonical(v, true)))
	sig, err := rsa.SignPKCS1v15(rand.Reader, key, crypto.SHA256, h[:])
	if err != nil {
		t.Fatal(err)
	}
	v.Set("sign", base64.StdEncoding.EncodeToString(sig))
	evidence, err := g.VerifyNotification(v)
	if err != nil || evidence.AmountCt != 12345 || evidence.OrderID != "CM1" {
		t.Fatalf("verify: %+v %v", evidence, err)
	}
	v.Set("total_amount", "0.01")
	if _, err = g.VerifyNotification(v); err == nil {
		t.Fatal("tampered amount accepted")
	}
}

func TestParseMoneyAndConfig(t *testing.T) {
	for _, v := range []string{"1.001", "-1.00", "NaN", "1e3", "1.", "+1.00", "0.0.1"} {
		if _, err := ParseMoney(v); err == nil {
			t.Errorf("accepted %q", v)
		}
	}
	for v, want := range map[string]int64{"1": 100, "1.2": 120, "0.01": 1, "123.45": 12345} {
		if got, err := ParseMoney(v); err != nil || got != want {
			t.Errorf("%s %d %v", v, got, err)
		}
	}
	if _, err := New(Config{AppID: "a", SellerID: "m", PrivateKey: "x", PublicKey: "y", PublicOrigin: "http://example.test"}); err == nil {
		t.Fatal("non-HTTPS origin accepted")
	}
}
