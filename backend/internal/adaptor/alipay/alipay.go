// Package alipay 实现支付宝 RSA2 网页/手机网站支付适配器。
// 私钥、签名、金额计算与回调验签均不暴露给浏览器。
package alipay

import (
	"context"
	"crypto"
	"crypto/rand"
	"crypto/rsa"
	"crypto/sha256"
	"crypto/x509"
	"encoding/base64"
	"encoding/json"
	"encoding/pem"
	"errors"
	"io"
	"net/http"
	"net/url"
	"sort"
	"strings"
	"time"

	"qingfly/edu-mall/internal/domain"
)

type Config struct {
	AppID        string
	SellerID     string
	PrivateKey   string
	PublicKey    string
	Gateway      string
	PublicOrigin string
	Production   bool
}

type Gateway struct {
	appID, sellerID, gateway, origin string
	private                          *rsa.PrivateKey
	public                           *rsa.PublicKey
	client                           *http.Client
}

func New(c Config) (*Gateway, error) {
	if c.AppID == "" && c.PrivateKey == "" && c.PublicKey == "" && c.SellerID == "" {
		return nil, nil
	}
	if c.AppID == "" || c.SellerID == "" || c.PrivateKey == "" || c.PublicKey == "" || c.PublicOrigin == "" {
		return nil, errors.New("Alipay requires app ID, seller ID, private/public PEM keys and PUBLIC_ORIGIN")
	}
	u, err := url.Parse(c.PublicOrigin)
	if err != nil || u.Scheme != "https" || u.Host == "" || u.Path != "" {
		return nil, errors.New("Alipay requires HTTPS PUBLIC_ORIGIN without path")
	}
	privBlock, _ := pem.Decode([]byte(c.PrivateKey))
	pubBlock, _ := pem.Decode([]byte(c.PublicKey))
	if privBlock == nil || pubBlock == nil {
		return nil, errors.New("invalid Alipay PEM keys")
	}
	var priv *rsa.PrivateKey
	if v, err := x509.ParsePKCS8PrivateKey(privBlock.Bytes); err == nil {
		priv, _ = v.(*rsa.PrivateKey)
	} else {
		priv, _ = x509.ParsePKCS1PrivateKey(privBlock.Bytes)
	}
	v, err := x509.ParsePKIXPublicKey(pubBlock.Bytes)
	if err != nil {
		return nil, err
	}
	pub, ok := v.(*rsa.PublicKey)
	if priv == nil || !ok || priv.N.BitLen() < 2048 || pub.N.BitLen() < 2048 {
		return nil, errors.New("Alipay requires RSA keys of at least 2048 bits")
	}
	gateway := c.Gateway
	if gateway == "" {
		gateway = "https://openapi.alipay.com/gateway.do"
	}
	if !validGateway(gateway, c.Production) {
		return nil, errors.New("unsupported Alipay gateway")
	}
	if c.Production && gateway != "https://openapi.alipay.com/gateway.do" {
		return nil, errors.New("sandbox gateway forbidden in production")
	}
	return &Gateway{appID: c.AppID, sellerID: c.SellerID, gateway: gateway, origin: c.PublicOrigin, private: priv, public: pub, client: &http.Client{Timeout: 10 * time.Second}}, nil
}

func (g *Gateway) Enabled() bool { return g != nil }

// validGateway 允许官方生产/沙箱网关；开发环境额外允许本机 HTTP 网关用于联调与测试。
// 生产环境仍强制官方生产网关。
func validGateway(raw string, production bool) bool {
	if raw == "https://openapi.alipay.com/gateway.do" || raw == "https://openapi-sandbox.dl.alipaydev.com/gateway.do" {
		return true
	}
	if production {
		return false
	}
	u, err := url.Parse(raw)
	if err != nil || u.Scheme != "http" || u.User != nil || u.RawQuery != "" || u.Fragment != "" {
		return false
	}
	return u.Hostname() == "localhost" || u.Hostname() == "127.0.0.1" || u.Hostname() == "::1"
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
	out := make([]string, 0, len(keys))
	for _, k := range keys {
		out = append(out, k+"="+v.Get(k))
	}
	return strings.Join(out, "&")
}

// Money 把分格式化为支付宝要求的元字符串。
func Money(ct int64) string { return domain.Money(ct) }

func ParseMoney(v string) (int64, error) { return domain.ParseMoney(v) }

func (g *Gateway) Checkout(_ context.Context, o domain.Order, mobile bool) (string, error) {
	method, code := "alipay.trade.page.pay", "FAST_INSTANT_TRADE_PAY"
	if mobile {
		method, code = "alipay.trade.wap.pay", "QUICK_WAP_WAY"
	}
	biz, _ := json.Marshal(map[string]any{"out_trade_no": o.ID, "total_amount": Money(o.AmountCt), "subject": o.Title, "product_code": code, "time_expire": time.UnixMilli(o.ExpiresAt).In(time.FixedZone("CST", 8*3600)).Format("2006-01-02 15:04:05")})
	params := url.Values{"app_id": {g.appID}, "method": {method}, "format": {"JSON"}, "charset": {"utf-8"}, "sign_type": {"RSA2"}, "timestamp": {time.Now().In(time.FixedZone("CST", 8*3600)).Format("2006-01-02 15:04:05")}, "version": {"1.0"}, "notify_url": {g.origin + "/api/v1/payments/alipay/notify"}, "return_url": {g.origin + "/?order=" + o.ID + "#orders"}, "biz_content": {string(biz)}}
	hash := sha256.Sum256([]byte(canonical(params, false)))
	sig, err := rsa.SignPKCS1v15(rand.Reader, g.private, crypto.SHA256, hash[:])
	if err != nil {
		return "", err
	}
	params.Set("sign", base64.StdEncoding.EncodeToString(sig))
	return g.gateway + "?" + params.Encode(), nil
}

// VerifyNotification 校验回调签名与商户身份，返回待业务层核对的支付证据。
func (g *Gateway) VerifyNotification(v url.Values) (domain.PaymentEvidence, error) {
	if g == nil {
		return domain.PaymentEvidence{}, domain.Unavailable("payment disabled")
	}
	for _, values := range v {
		if len(values) != 1 {
			return domain.PaymentEvidence{}, domain.Invalid("duplicate callback fields")
		}
	}
	if v.Get("sign_type") != "RSA2" || v.Get("app_id") != g.appID || v.Get("seller_id") != g.sellerID || v.Get("notify_id") == "" || v.Get("trade_no") == "" || len(v.Get("notify_id")) > 128 || len(v.Get("trade_no")) > 128 {
		return domain.PaymentEvidence{}, domain.Invalid("invalid merchant or callback")
	}
	status := v.Get("trade_status")
	if status != "TRADE_SUCCESS" && status != "TRADE_FINISHED" && status != "TRADE_CLOSED" {
		return domain.PaymentEvidence{}, domain.Invalid("unsupported trade status")
	}
	amount, err := ParseMoney(v.Get("total_amount"))
	if err != nil || amount <= 0 {
		return domain.PaymentEvidence{}, domain.Invalid("bad amount")
	}
	sig, err := base64.StdEncoding.DecodeString(v.Get("sign"))
	if err != nil {
		return domain.PaymentEvidence{}, domain.Invalid("bad signature")
	}
	hash := sha256.Sum256([]byte(canonical(v, true)))
	if rsa.VerifyPKCS1v15(g.public, crypto.SHA256, hash[:], sig) != nil {
		return domain.PaymentEvidence{}, domain.Invalid("bad signature")
	}
	return domain.PaymentEvidence{NotifyID: v.Get("notify_id"), OrderID: v.Get("out_trade_no"), TradeNo: v.Get("trade_no"), Status: status, AmountCt: amount}, nil
}

func (g *Gateway) Query(ctx context.Context, orderID string) (domain.GatewayOrder, error) {
	if g == nil {
		return domain.GatewayOrder{}, domain.Unavailable("payment disabled")
	}
	return g.call(ctx, "alipay.trade.query", map[string]string{"out_trade_no": orderID})
}

func (g *Gateway) Close(ctx context.Context, orderID string) (domain.GatewayOrder, error) {
	if g == nil {
		return domain.GatewayOrder{}, domain.Unavailable("payment disabled")
	}
	return g.call(ctx, "alipay.trade.close", map[string]string{"out_trade_no": orderID})
}

func (g *Gateway) call(ctx context.Context, method string, biz map[string]string) (domain.GatewayOrder, error) {
	var out domain.GatewayOrder
	content, _ := json.Marshal(biz)
	v := url.Values{"app_id": {g.appID}, "method": {method}, "format": {"JSON"}, "charset": {"utf-8"}, "sign_type": {"RSA2"}, "timestamp": {time.Now().In(time.FixedZone("CST", 8*3600)).Format("2006-01-02 15:04:05")}, "version": {"1.0"}, "biz_content": {string(content)}}
	h := sha256.Sum256([]byte(canonical(v, false)))
	sig, err := rsa.SignPKCS1v15(rand.Reader, g.private, crypto.SHA256, h[:])
	if err != nil {
		return out, err
	}
	v.Set("sign", base64.StdEncoding.EncodeToString(sig))
	ctx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, "POST", g.gateway, strings.NewReader(v.Encode()))
	if err != nil {
		return out, err
	}
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded;charset=utf-8")
	resp, err := g.client.Do(req)
	if err != nil {
		return out, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != 200 {
		return out, errors.New("payment gateway unavailable")
	}
	raw, err := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if err != nil {
		return out, err
	}
	var envelope map[string]json.RawMessage
	if err = json.Unmarshal(raw, &envelope); err != nil {
		return out, err
	}
	body := envelope[strings.ReplaceAll(method, ".", "_")+"_response"]
	var responseSign string
	if err = json.Unmarshal(envelope["sign"], &responseSign); err != nil {
		return out, errors.New("unsigned gateway response")
	}
	signature, err := base64.StdEncoding.DecodeString(responseSign)
	if err != nil {
		return out, err
	}
	hash := sha256.Sum256(body)
	if rsa.VerifyPKCS1v15(g.public, crypto.SHA256, hash[:], signature) != nil {
		return out, errors.New("invalid gateway response signature")
	}
	var rawResponse struct {
		Code    string `json:"code"`
		SubCode string `json:"sub_code"`
		OrderID string `json:"out_trade_no"`
		TradeNo string `json:"trade_no"`
		Status  string `json:"trade_status"`
		Amount  string `json:"total_amount"`
	}
	if err = json.Unmarshal(body, &rawResponse); err != nil {
		return out, err
	}
	return domain.GatewayOrder{Code: rawResponse.Code, SubCode: rawResponse.SubCode, OrderID: rawResponse.OrderID, TradeNo: rawResponse.TradeNo, Status: rawResponse.Status, Amount: rawResponse.Amount}, nil
}
