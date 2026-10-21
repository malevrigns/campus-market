package platform

import (
	"context"
	"crypto"
	"crypto/rand"
	"crypto/rsa"
	"crypto/sha256"
	"crypto/x509"
	"database/sql"
	"encoding/base64"
	"encoding/json"
	"encoding/pem"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"sort"
	"strconv"
	"strings"
	"time"
)

// Alipay implements the public-key RSA2 website and mobile website payment
// protocols. No private key, signature generation, amount calculation, or
// callback authority is exposed to the browser.
type Alipay struct {
	appID, sellerID, gateway, origin string
	private                          *rsa.PrivateKey
	public                           *rsa.PublicKey
}

func NewAlipay(c Config) (*Alipay, error) {
	if c.AlipayAppID == "" && c.AlipayPrivateKey == "" && c.AlipayPublicKey == "" && c.AlipaySellerID == "" {
		return nil, nil
	}
	if c.AlipayAppID == "" || c.AlipaySellerID == "" || c.AlipayPrivateKey == "" || c.AlipayPublicKey == "" || c.PublicOrigin == "" {
		return nil, errors.New("Alipay requires app ID, seller ID, private/public PEM keys and PUBLIC_ORIGIN")
	}
	u, e := url.Parse(c.PublicOrigin)
	if e != nil || u.Scheme != "https" || u.Host == "" || u.Path != "" {
		return nil, errors.New("Alipay requires HTTPS PUBLIC_ORIGIN without path")
	}
	privBlock, _ := pem.Decode([]byte(c.AlipayPrivateKey))
	pubBlock, _ := pem.Decode([]byte(c.AlipayPublicKey))
	if privBlock == nil || pubBlock == nil {
		return nil, errors.New("invalid Alipay PEM keys")
	}
	var priv *rsa.PrivateKey
	if v, e := x509.ParsePKCS8PrivateKey(privBlock.Bytes); e == nil {
		priv, _ = v.(*rsa.PrivateKey)
	} else {
		priv, _ = x509.ParsePKCS1PrivateKey(privBlock.Bytes)
	}
	v, e := x509.ParsePKIXPublicKey(pubBlock.Bytes)
	if e != nil {
		return nil, e
	}
	pub, ok := v.(*rsa.PublicKey)
	if priv == nil || !ok || priv.N.BitLen() < 2048 || pub.N.BitLen() < 2048 {
		return nil, errors.New("Alipay requires RSA keys of at least 2048 bits")
	}
	gateway := c.AlipayGateway
	if gateway == "" {
		gateway = "https://openapi.alipay.com/gateway.do"
	}
	if gateway != "https://openapi.alipay.com/gateway.do" && gateway != "https://openapi-sandbox.dl.alipaydev.com/gateway.do" {
		return nil, errors.New("unsupported Alipay gateway")
	}
	if c.Production && gateway != "https://openapi.alipay.com/gateway.do" {
		return nil, errors.New("sandbox gateway forbidden in production")
	}
	return &Alipay{appID: c.AlipayAppID, sellerID: c.AlipaySellerID, gateway: gateway, origin: c.PublicOrigin, private: priv, public: pub}, nil
}
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
func money(ct int64) string { return fmt.Sprintf("%d.%02d", ct/100, ct%100) }
func parseMoney(v string) (int64, error) {
	parts := strings.Split(v, ".")
	if len(parts) > 2 || len(parts) == 0 || len(parts[0]) == 0 || len(parts[0]) > 8 {
		return 0, errors.New("invalid amount")
	}
	for _, c := range v {
		if (c < '0' || c > '9') && c != '.' {
			return 0, errors.New("invalid amount")
		}
	}
	whole, e := strconv.ParseInt(parts[0], 10, 64)
	if e != nil {
		return 0, e
	}
	fraction := int64(0)
	if len(parts) == 2 {
		if len(parts[1]) < 1 || len(parts[1]) > 2 {
			return 0, errors.New("invalid decimals")
		}
		fraction, e = strconv.ParseInt(parts[1], 10, 64)
		if e != nil {
			return 0, e
		}
		if len(parts[1]) == 1 {
			fraction *= 10
		}
	}
	return whole*100 + fraction, nil
}
func (p *Alipay) checkout(o Order, mobile bool) (string, error) {
	method, code := "alipay.trade.page.pay", "FAST_INSTANT_TRADE_PAY"
	if mobile {
		method, code = "alipay.trade.wap.pay", "QUICK_WAP_WAY"
	}
	biz, _ := json.Marshal(map[string]any{"out_trade_no": o.ID, "total_amount": money(o.AmountCt), "subject": o.Title, "product_code": code, "time_expire": time.UnixMilli(o.ExpiresAt).In(time.FixedZone("CST", 8*3600)).Format("2006-01-02 15:04:05")})
	params := url.Values{"app_id": {p.appID}, "method": {method}, "format": {"JSON"}, "charset": {"utf-8"}, "sign_type": {"RSA2"}, "timestamp": {time.Now().In(time.FixedZone("CST", 8*3600)).Format("2006-01-02 15:04:05")}, "version": {"1.0"}, "notify_url": {p.origin + "/api/v1/payments/alipay/notify"}, "return_url": {p.origin + "/?order=" + o.ID + "#orders"}, "biz_content": {string(biz)}}
	hash := sha256.Sum256([]byte(canonical(params, false)))
	sig, e := rsa.SignPKCS1v15(rand.Reader, p.private, crypto.SHA256, hash[:])
	if e != nil {
		return "", e
	}
	params.Set("sign", base64.StdEncoding.EncodeToString(sig))
	return p.gateway + "?" + params.Encode(), nil
}
func (s *Server) startPayment(w http.ResponseWriter, r *http.Request, u *User) error {
	if s.pay == nil {
		return fail(503, "在线支付尚未开通，请稍后再试")
	}
	var v struct {
		Method string `json:"method"`
		Mobile bool   `json:"mobile"`
	}
	if e := decode(r, &v); e != nil {
		return e
	}
	if v.Method != "alipay" {
		return fail(400, "不支持该支付方式")
	}
	tx, e := s.db.BeginTx(r.Context(), nil)
	if e != nil {
		return e
	}
	defer tx.Rollback()
	o, e := scanOrder(tx.QueryRow(orderSelect+" WHERE id=? AND buyer_id=? FOR UPDATE", r.PathValue("id"), u.ID))
	if e != nil {
		return e
	}
	if o.Status != "pending" || o.ExpiresAt <= now() {
		return fail(409, "订单已支付、关闭或超时")
	}
	// Stable out_trade_no deduplicates sessions at Alipay. A fresh signed URL lets
	// the same order resume on desktop or mobile without a second payment order.
	checkout, e := s.pay.checkout(o, v.Mobile)
	if e != nil {
		return e
	}
	if _, e = tx.Exec("INSERT INTO payments (order_id,checkout_url,created_at) VALUES(?,?,?) ON DUPLICATE KEY UPDATE checkout_url=VALUES(checkout_url)", o.ID, checkout, now()); e != nil {
		return e
	}
	if e = tx.Commit(); e != nil {
		return e
	}
	return result(w, 200, map[string]any{"checkout_url": checkout, "order_id": o.ID, "amount_ct": o.AmountCt})
}
func (s *Server) paymentNotify(w http.ResponseWriter, r *http.Request) {
	if s.pay == nil {
		http.Error(w, "failure", 503)
		return
	}
	if e := r.ParseForm(); e != nil {
		http.Error(w, "failure", 400)
		return
	}
	if e := s.acceptPayment(r.PostForm); e != nil {
		status := 400
		var a *apiError
		if errors.As(e, &a) {
			status = a.status
		}
		http.Error(w, "failure", status)
		return
	}
	w.Header().Set("Content-Type", "text/plain; charset=utf-8")
	_, _ = w.Write([]byte("success"))
}
func (s *Server) acceptPayment(v url.Values) error {
	p := s.pay
	if p == nil {
		return fail(503, "payment disabled")
	}
	for _, values := range v {
		if len(values) != 1 {
			return fail(400, "duplicate callback fields")
		}
	}
	if v.Get("sign_type") != "RSA2" || v.Get("app_id") != p.appID || v.Get("seller_id") != p.sellerID || v.Get("notify_id") == "" || v.Get("trade_no") == "" || len(v.Get("notify_id")) > 128 || len(v.Get("trade_no")) > 128 {
		return fail(400, "invalid merchant or callback")
	}
	sig, e := base64.StdEncoding.DecodeString(v.Get("sign"))
	if e != nil {
		return fail(400, "bad signature")
	}
	hash := sha256.Sum256([]byte(canonical(v, true)))
	if rsa.VerifyPKCS1v15(p.public, crypto.SHA256, hash[:], sig) != nil {
		return fail(400, "bad signature")
	}
	return s.applyPaymentEvidence(v)
}

// applyPaymentEvidence is private and only accepts evidence from verified
// notifications or an authenticated, signature-verified gateway query.
func (s *Server) applyPaymentEvidence(v url.Values) error {
	status := v.Get("trade_status")
	if status != "TRADE_SUCCESS" && status != "TRADE_FINISHED" && status != "TRADE_CLOSED" {
		return fail(400, "unsupported trade status")
	}
	amount, e := parseMoney(v.Get("total_amount"))
	if e != nil || amount <= 0 {
		return fail(400, "bad amount")
	}
	tx, e := s.db.Begin()
	if e != nil {
		return e
	}
	defer tx.Rollback()
	// 支付回调与用户取消、超时关单可能并发：锁住订单行后再判断状态。
	o, e := scanOrder(tx.QueryRow(orderSelect+" WHERE id=? FOR UPDATE", v.Get("out_trade_no")))
	if e != nil {
		return e
	}
	if amount != o.AmountCt {
		return fail(400, "amount mismatch")
	}
	var trade sql.NullString
	if e = tx.QueryRow("SELECT trade_no FROM payments WHERE order_id=?", o.ID).Scan(&trade); e != nil {
		return e
	}
	if trade.Valid && trade.String != v.Get("trade_no") {
		return fail(409, "trade mismatch")
	}
	var previousOrder, previousTrade, previousStatus string
	var previousAmount int64
	e = tx.QueryRow("SELECT order_id,trade_no,status,amount_ct FROM payment_callbacks WHERE notify_id=?", v.Get("notify_id")).Scan(&previousOrder, &previousTrade, &previousStatus, &previousAmount)
	if e == nil {
		if previousOrder != o.ID || previousTrade != v.Get("trade_no") || previousStatus != status || previousAmount != amount {
			return fail(409, "callback identity reused")
		}
		return nil
	}
	if !errors.Is(e, sql.ErrNoRows) {
		return e
	}
	next := o.Status
	if status == "TRADE_CLOSED" {
		if o.Status == "pending" {
			next = "cancelled"
		} else if o.Status != "cancelled" {
			return fail(409, "paid trade closure requires refund reconciliation")
		}
	} else if o.Status == "pending" {
		next = "paid"
	} else if o.Status == "cancelled" {
		return fail(409, "closed order cannot be paid")
	}
	if _, e = tx.Exec("UPDATE payments SET trade_no=? WHERE order_id=?", v.Get("trade_no"), o.ID); e != nil {
		return e
	}
	if _, e = tx.Exec("INSERT INTO payment_callbacks (notify_id,order_id,trade_no,status,amount_ct,created_at) VALUES(?,?,?,?,?,?)", v.Get("notify_id"), o.ID, v.Get("trade_no"), status, amount, now()); e != nil {
		return e
	}
	if next != o.Status {
		if _, e = tx.Exec("UPDATE orders SET status=?,updated_at=? WHERE id=?", next, now(), o.ID); e != nil {
			return e
		}
		if next == "cancelled" {
			if e = releaseInventory(tx, o); e != nil {
				return e
			}
		} else if o.ProductID != "" {
			if _, e = tx.Exec("UPDATE products SET status='sold',updated_at=? WHERE id=?", now(), o.ProductID); e != nil {
				return e
			}
		}
		if e = addEvent(tx, o.ID, next, "alipay"); e != nil {
			return e
		}
	}
	if e = tx.Commit(); e != nil {
		return e
	}
	ctx := context.Background()
	switch {
	case next == o.Status:
	case next == "cancelled":
		s.afterRelease(ctx, o)
		return nil
	default:
		_ = s.timeouts.Remove(ctx, o.ID)
		s.productsChanged(ctx, o.ProductID)
	}
	s.notifyOrder(o)
	return nil
}
