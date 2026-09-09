package platform

import (
	"context"
	"crypto"
	"crypto/rand"
	"crypto/rsa"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func gatewayStub(t *testing.T, s *Server, respond func(method, oid string) map[string]string, valid bool) {
	t.Helper()
	key, e := rsa.GenerateKey(rand.Reader, 2048)
	if e != nil {
		t.Fatal(e)
	}
	gateway := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if e := r.ParseForm(); e != nil {
			t.Error(e)
			return
		}
		var biz map[string]string
		if e = json.Unmarshal([]byte(r.Form.Get("biz_content")), &biz); e != nil {
			t.Error(e)
			return
		}
		method := r.Form.Get("method")
		body, _ := json.Marshal(respond(method, biz["out_trade_no"]))
		h := sha256.Sum256(body)
		sig, _ := rsa.SignPKCS1v15(rand.Reader, key, crypto.SHA256, h[:])
		if !valid {
			sig[0] ^= 1
		}
		fmt.Fprintf(w, `{"%s_response":%s,"sign":%q}`, strings.ReplaceAll(method, ".", "_"), body, base64.StdEncoding.EncodeToString(sig))
	}))
	t.Cleanup(gateway.Close)
	s.pay = &Alipay{appID: "test", sellerID: "merchant", private: key, public: &key.PublicKey, gateway: gateway.URL}
}
func paymentOrder(t *testing.T, s *Server, seller, buyer *testClient, key string) Order {
	t.Helper()
	pid := publish(t, seller)
	_, d := order(buyer, pid, key)
	oid := d["id"].(string)
	if _, e := s.db.Exec("INSERT INTO payments(order_id,checkout_url,created_at) VALUES(?,?,?)", oid, "https://alipay.example.test", now()); e != nil {
		t.Fatal(e)
	}
	o, e := scanOrder(s.db.QueryRow(orderSelect+" WHERE id=?", oid))
	if e != nil {
		t.Fatal(e)
	}
	return o
}
func TestSignedQueryRepairsLostCallback(t *testing.T) {
	s, _ := testApp(t)
	seller := account(t, s, "seller")
	buyer := account(t, s, "buyer")
	o := paymentOrder(t, s, seller, buyer, "lost-callback-key")
	gatewayStub(t, s, func(_, oid string) map[string]string {
		return map[string]string{"code": "10000", "out_trade_no": oid, "trade_no": "gateway-trade", "trade_status": "TRADE_SUCCESS", "total_amount": "123.45"}
	}, true)
	for i := 0; i < 2; i++ {
		if e := s.reconcileOne(context.Background(), o); e != nil {
			t.Fatal(e)
		}
	}
	fresh, e := scanOrder(s.db.QueryRow(orderSelect+" WHERE id=?", o.ID))
	if e != nil || fresh.Status != "paid" {
		t.Fatalf("reconcile: %v %v", fresh, e)
	}
}
func TestUntrustedQueryDoesNotReleaseReservation(t *testing.T) {
	s, _ := testApp(t)
	seller := account(t, s, "seller")
	buyer := account(t, s, "buyer")
	o := paymentOrder(t, s, seller, buyer, "untrusted-query-key")
	o.ExpiresAt = now() - 120000
	s.db.Exec("UPDATE orders SET expires_at=? WHERE id=?", o.ExpiresAt, o.ID)
	gatewayStub(t, s, func(_, oid string) map[string]string {
		return map[string]string{"code": "40004", "sub_code": "ACQ.TRADE_NOT_EXIST"}
	}, false)
	if e := s.reconcileOne(context.Background(), o); e == nil {
		t.Fatal("unsigned query accepted")
	}
	var status string
	s.db.QueryRow("SELECT status FROM products WHERE id=?", o.ProductID).Scan(&status)
	if status != "reserved" {
		t.Fatalf("untrusted query released %s", status)
	}
}
func TestReconciliationDoesNotStarveLaterOrders(t *testing.T) {
	s, _ := testApp(t)
	seller := account(t, s, "seller")
	buyer := account(t, s, "buyer")
	for i := 0; i < 31; i++ {
		paymentOrder(t, s, seller, buyer, fmt.Sprintf("fairness-key-%03d", i))
	}
	seen := map[string]bool{}
	gatewayStub(t, s, func(_, oid string) map[string]string {
		seen[oid] = true
		return map[string]string{"code": "10000", "out_trade_no": oid, "trade_no": "trade-" + oid, "trade_status": "WAIT_BUYER_PAY", "total_amount": "123.45"}
	}, true)
	for i := 0; i < 2; i++ {
		if e := s.ReconcilePayments(context.Background()); e != nil {
			t.Fatal(e)
		}
	}
	if len(seen) != 31 {
		t.Fatalf("only %d/31 payment orders reconciled", len(seen))
	}
}
func TestLateClosureCannotUndoPayment(t *testing.T) {
	s, _ := testApp(t)
	seller := account(t, s, "seller")
	buyer := account(t, s, "buyer")
	o := paymentOrder(t, s, seller, buyer, "paid-closure-key")
	gatewayStub(t, s, func(_, oid string) map[string]string {
		return map[string]string{"code": "10000", "out_trade_no": oid, "trade_no": "trade-one", "trade_status": "TRADE_SUCCESS", "total_amount": "123.45"}
	}, true)
	if e := s.reconcileOne(context.Background(), o); e != nil {
		t.Fatal(e)
	}
	gatewayStub(t, s, func(_, oid string) map[string]string {
		return map[string]string{"code": "10000", "out_trade_no": oid, "trade_no": "trade-one", "trade_status": "TRADE_CLOSED", "total_amount": "123.45"}
	}, true)
	if e := s.reconcileOne(context.Background(), o); e == nil {
		t.Fatal("late closure accepted")
	}
	var status string
	s.db.QueryRow("SELECT status FROM products WHERE id=?", o.ProductID).Scan(&status)
	if status != "sold" {
		t.Fatal("sold inventory released")
	}
}
