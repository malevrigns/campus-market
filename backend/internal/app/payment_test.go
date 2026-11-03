package app

import (
	"context"
	"crypto"
	"crypto/rand"
	"crypto/rsa"
	"crypto/sha256"
	"crypto/x509"
	"encoding/base64"
	"encoding/pem"
	"errors"
	"fmt"
	"net/http/httptest"
	"net/url"
	"sort"
	"strings"
	"testing"

	"qingfly/edu-mall/internal/adaptor/alipay"
	"qingfly/edu-mall/internal/domain"
)

func canonical(v url.Values, notification bool) string {
	keys := []string{}
	for k := range v {
		if k == "sign" || (notification && k == "sign_type") || v.Get(k) == "" {
			continue
		}
		keys = append(keys, k)
	}
	sort.Strings(keys)
	out := []string{}
	for _, k := range keys {
		out = append(out, k+"="+v.Get(k))
	}
	return strings.Join(out, "&")
}

func rsaPEM(t *testing.T) (string, string, *rsa.PrivateKey) {
	t.Helper()
	key, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		t.Fatal(err)
	}
	priv := pem.EncodeToMemory(&pem.Block{Type: "PRIVATE KEY", Bytes: mustPKCS8(t, key)})
	pub := pem.EncodeToMemory(&pem.Block{Type: "PUBLIC KEY", Bytes: mustPKIX(t, &key.PublicKey)})
	return string(priv), string(pub), key
}

func mustPKCS8(t *testing.T, key *rsa.PrivateKey) []byte {
	t.Helper()
	b, err := x509.MarshalPKCS8PrivateKey(key)
	if err != nil {
		t.Fatal(err)
	}
	return b
}

func mustPKIX(t *testing.T, key *rsa.PublicKey) []byte {
	t.Helper()
	b, err := x509.MarshalPKIXPublicKey(key)
	if err != nil {
		t.Fatal(err)
	}
	return b
}

func signForm(t *testing.T, key *rsa.PrivateKey, v url.Values) {
	t.Helper()
	h := sha256.Sum256([]byte(canonical(v, true)))
	sig, err := rsa.SignPKCS1v15(rand.Reader, key, crypto.SHA256, h[:])
	if err != nil {
		t.Fatal(err)
	}
	v.Set("sign", base64.StdEncoding.EncodeToString(sig))
}

func TestRSACallbackValidationAndReplay(t *testing.T) {
	privPEM, pubPEM, key := rsaPEM(t)
	pay, err := alipay.New(alipay.Config{AppID: "app", SellerID: "merchant", PrivateKey: privPEM, PublicKey: pubPEM, PublicOrigin: "https://example.test"})
	if err != nil {
		t.Fatal(err)
	}
	a, _ := testAppWith(t, configApp(), WithPaymentGateway(pay))
	seller := account(t, a, "seller")
	buyer := account(t, a, "buyer")
	pid := publish(t, seller)
	_, o := order(buyer, pid, "payment-test-key")
	oid := o["id"].(string)
	if _, err = a.DB.Exec("INSERT INTO payments(order_id,checkout_url,created_at) VALUES(?,?,?)", oid, "https://example.test", now()); err != nil {
		t.Fatal(err)
	}
	v := url.Values{"app_id": {"app"}, "seller_id": {"merchant"}, "notify_id": {"callback-1"}, "trade_no": {"trade-1"}, "out_trade_no": {oid}, "trade_status": {"TRADE_SUCCESS"}, "total_amount": {"123.45"}, "sign_type": {"RSA2"}}
	signForm(t, key, v)
	notify := func() int {
		r := httptest.NewRequest("POST", "/api/v1/payments/alipay/notify", strings.NewReader(v.Encode()))
		r.RemoteAddr = "127.0.0.1:12345"
		r.Header.Set("Content-Type", "application/x-www-form-urlencoded")
		w := httptest.NewRecorder()
		a.Handler().ServeHTTP(w, r)
		return w.Code
	}
	v.Set("total_amount", "0.01")
	if code := notify(); code == 200 {
		t.Fatal("tampered amount accepted")
	}
	signForm(t, key, v)
	signForm(t, key, v)
	v.Set("total_amount", "0.01")
	signForm(t, key, v)
	if code := notify(); code == 200 {
		t.Fatal("signed wrong amount accepted")
	}
	v.Set("total_amount", "123.45")
	signForm(t, key, v)
	for i := 0; i < 3; i++ {
		if code := notify(); code != 200 {
			t.Fatalf("notify %d -> %d", i, code)
		}
	}
	var count int
	if err = a.DB.QueryRow("SELECT count(*) FROM order_events WHERE order_id=? AND status='paid'", oid).Scan(&count); err != nil || count != 1 {
		t.Fatalf("replay %d %v", count, err)
	}
	v.Set("trade_no", "different-trade")
	signForm(t, key, v)
	if code := notify(); code != 409 {
		t.Fatalf("trade replacement accepted: %d", code)
	}
}

type stubGateway struct {
	queryFn func(ctx context.Context, orderID string) (domain.GatewayOrder, error)
	closeFn func(ctx context.Context, orderID string) (domain.GatewayOrder, error)
}

func (s *stubGateway) Enabled() bool { return true }
func (s *stubGateway) Checkout(context.Context, domain.Order, bool) (string, error) {
	return "https://pay.example.test", nil
}
func (s *stubGateway) VerifyNotification(url.Values) (domain.PaymentEvidence, error) {
	return domain.PaymentEvidence{}, errors.New("not used")
}
func (s *stubGateway) Query(ctx context.Context, orderID string) (domain.GatewayOrder, error) {
	return s.queryFn(ctx, orderID)
}
func (s *stubGateway) Close(ctx context.Context, orderID string) (domain.GatewayOrder, error) {
	if s.closeFn != nil {
		return s.closeFn(ctx, orderID)
	}
	return domain.GatewayOrder{}, nil
}

func paymentOrder(t *testing.T, a *App, seller, buyer *testClient, key string) domain.Order {
	t.Helper()
	pid := publish(t, seller)
	_, d := order(buyer, pid, key)
	oid := d["id"].(string)
	if _, e := a.DB.Exec("INSERT INTO payments(order_id,checkout_url,created_at) VALUES(?,?,?)", oid, "https://alipay.example.test", now()); e != nil {
		t.Fatal(e)
	}
	_, me := buyer.call("GET", "/api/v1/auth/me", nil)
	user := domain.User{ID: me["id"].(string)}
	o, e := a.Orders.Get(context.Background(), &user, oid)
	if e != nil {
		t.Fatal(e)
	}
	return o
}

func TestSignedQueryRepairsLostCallback(t *testing.T) {
	gateway := &stubGateway{}
	a, _ := testAppWith(t, configApp(), WithPaymentGateway(gateway))
	seller := account(t, a, "seller")
	buyer := account(t, a, "buyer")
	o := paymentOrder(t, a, seller, buyer, "lost-callback-key")
	gateway.queryFn = func(_ context.Context, oid string) (domain.GatewayOrder, error) {
		return domain.GatewayOrder{Code: "10000", OrderID: oid, TradeNo: "gateway-trade", Status: "TRADE_SUCCESS", Amount: "123.45"}, nil
	}
	for i := 0; i < 2; i++ {
		if e := a.Payments.ReconcileOne(context.Background(), o); e != nil {
			t.Fatal(e)
		}
	}
	var status string
	if e := a.DB.QueryRow("SELECT status FROM orders WHERE id=?", o.ID).Scan(&status); e != nil || status != "paid" {
		t.Fatalf("reconcile: %s %v", status, e)
	}
}

func TestUntrustedQueryDoesNotReleaseReservation(t *testing.T) {
	gateway := &stubGateway{}
	a, _ := testAppWith(t, configApp(), WithPaymentGateway(gateway))
	seller := account(t, a, "seller")
	buyer := account(t, a, "buyer")
	o := paymentOrder(t, a, seller, buyer, "untrusted-query-key")
	o.ExpiresAt = now() - 120000
	a.DB.Exec("UPDATE orders SET expires_at=? WHERE id=?", o.ExpiresAt, o.ID)
	gateway.queryFn = func(context.Context, string) (domain.GatewayOrder, error) {
		return domain.GatewayOrder{}, errors.New("unsigned query")
	}
	if e := a.Payments.ReconcileOne(context.Background(), o); e == nil {
		t.Fatal("unsigned query accepted")
	}
	var status string
	a.DB.QueryRow("SELECT status FROM products WHERE id=?", o.ProductID).Scan(&status)
	if status != "reserved" {
		t.Fatalf("untrusted query released %s", status)
	}
}

func TestReconciliationDoesNotStarveLaterOrders(t *testing.T) {
	gateway := &stubGateway{}
	a, _ := testAppWith(t, configApp(), WithPaymentGateway(gateway))
	seller := account(t, a, "seller")
	buyer := account(t, a, "buyer")
	for i := 0; i < 31; i++ {
		paymentOrder(t, a, seller, buyer, fmt.Sprintf("fairness-key-%03d", i))
	}
	seen := map[string]bool{}
	gateway.queryFn = func(_ context.Context, oid string) (domain.GatewayOrder, error) {
		seen[oid] = true
		return domain.GatewayOrder{Code: "10000", OrderID: oid, TradeNo: "trade-" + oid, Status: "WAIT_BUYER_PAY", Amount: "123.45"}, nil
	}
	for i := 0; i < 2; i++ {
		if e := a.Payments.Reconcile(context.Background()); e != nil {
			t.Fatal(e)
		}
	}
	if len(seen) != 31 {
		t.Fatalf("only %d/31 payment orders reconciled", len(seen))
	}
}

func TestLateClosureCannotUndoPayment(t *testing.T) {
	gateway := &stubGateway{}
	a, _ := testAppWith(t, configApp(), WithPaymentGateway(gateway))
	seller := account(t, a, "seller")
	buyer := account(t, a, "buyer")
	o := paymentOrder(t, a, seller, buyer, "paid-closure-key")
	gateway.queryFn = func(_ context.Context, oid string) (domain.GatewayOrder, error) {
		return domain.GatewayOrder{Code: "10000", OrderID: oid, TradeNo: "trade-one", Status: "TRADE_SUCCESS", Amount: "123.45"}, nil
	}
	if e := a.Payments.ReconcileOne(context.Background(), o); e != nil {
		t.Fatal(e)
	}
	gateway.queryFn = func(_ context.Context, oid string) (domain.GatewayOrder, error) {
		return domain.GatewayOrder{Code: "10000", OrderID: oid, TradeNo: "trade-one", Status: "TRADE_CLOSED", Amount: "123.45"}, nil
	}
	if e := a.Payments.ReconcileOne(context.Background(), o); e == nil {
		t.Fatal("late closure accepted")
	}
	var status string
	a.DB.QueryRow("SELECT status FROM products WHERE id=?", o.ProductID).Scan(&status)
	if status != "sold" {
		t.Fatal("sold inventory released")
	}
}

func TestMoneyParsing(t *testing.T) {
	for _, v := range []string{"1.001", "-1.00", "NaN", "1e3", "1.", "+1.00", "0.0.1"} {
		if _, e := domain.ParseMoney(v); e == nil {
			t.Errorf("accepted %q", v)
		}
	}
	for v, want := range map[string]int64{"1": 100, "1.2": 120, "0.01": 1, "123.45": 12345} {
		if got, e := domain.ParseMoney(v); e != nil || got != want {
			t.Errorf("%s %d %v", v, got, e)
		}
	}
}
