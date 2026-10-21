package platform

import (
	"context"
	"crypto"
	"crypto/rand"
	"crypto/rsa"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"net/http"
	"net/url"
	"strings"
	"time"
)

type gatewayResponse struct {
	Code    string `json:"code"`
	SubCode string `json:"sub_code"`
	OrderID string `json:"out_trade_no"`
	TradeNo string `json:"trade_no"`
	Status  string `json:"trade_status"`
	Amount  string `json:"total_amount"`
}

func (p *Alipay) call(ctx context.Context, client *http.Client, method string, biz map[string]string) (gatewayResponse, error) {
	var out gatewayResponse
	content, _ := json.Marshal(biz)
	v := url.Values{"app_id": {p.appID}, "method": {method}, "format": {"JSON"}, "charset": {"utf-8"}, "sign_type": {"RSA2"}, "timestamp": {time.Now().In(time.FixedZone("CST", 8*3600)).Format("2006-01-02 15:04:05")}, "version": {"1.0"}, "biz_content": {string(content)}}
	h := sha256.Sum256([]byte(canonical(v, false)))
	sig, e := rsa.SignPKCS1v15(rand.Reader, p.private, crypto.SHA256, h[:])
	if e != nil {
		return out, e
	}
	v.Set("sign", base64.StdEncoding.EncodeToString(sig))
	ctx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()
	req, e := http.NewRequestWithContext(ctx, "POST", p.gateway, strings.NewReader(v.Encode()))
	if e != nil {
		return out, e
	}
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded;charset=utf-8")
	resp, e := client.Do(req)
	if e != nil {
		return out, e
	}
	defer resp.Body.Close()
	if resp.StatusCode != 200 {
		return out, errors.New("payment gateway unavailable")
	}
	raw, e := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if e != nil {
		return out, e
	}
	var envelope map[string]json.RawMessage
	if e = json.Unmarshal(raw, &envelope); e != nil {
		return out, e
	}
	body := envelope[strings.ReplaceAll(method, ".", "_")+"_response"]
	var responseSign string
	if e = json.Unmarshal(envelope["sign"], &responseSign); e != nil {
		return out, errors.New("unsigned gateway response")
	}
	signature, e := base64.StdEncoding.DecodeString(responseSign)
	if e != nil {
		return out, e
	}
	hash := sha256.Sum256(body)
	if rsa.VerifyPKCS1v15(p.public, crypto.SHA256, hash[:], signature) != nil {
		return out, errors.New("invalid gateway response signature")
	}
	e = json.Unmarshal(body, &out)
	return out, e
}

// ReconcilePayments closes the callback-loss gap. Unpaid gateway sessions retain
// stock until an authenticated query confirms closure (or nonexistence after the
// signed absolute expiration time). Network errors never release inventory.
func (s *Server) ReconcilePayments(ctx context.Context) error {
	if s.pay == nil {
		return nil
	}
	rows, e := s.db.QueryContext(ctx, "SELECT "+orderColumns+" FROM orders o WHERE o.status='pending' AND EXISTS(SELECT 1 FROM payments p WHERE p.order_id=o.id) ORDER BY COALESCE((SELECT c.checked_at FROM payment_checks c WHERE c.order_id=o.id),0),o.created_at LIMIT 30")
	if e != nil {
		return e
	}
	orders := []Order{}
	for rows.Next() {
		o, e := scanOrder(rows)
		if e != nil {
			rows.Close()
			return e
		}
		orders = append(orders, o)
	}
	e = rows.Err()
	rows.Close()
	if e != nil {
		return e
	}
	for _, o := range orders {
		if ctx.Err() != nil {
			return ctx.Err()
		}
		if _, e = s.db.ExecContext(ctx, "INSERT INTO payment_checks (order_id,checked_at) VALUES(?,?) ON DUPLICATE KEY UPDATE checked_at=VALUES(checked_at)", o.ID, now()); e != nil {
			return e
		}
		if e = s.reconcileOne(ctx, o); e != nil {
			slog.Warn("payment reconciliation pending", "order_id", o.ID, "error", e)
		}
	}
	return nil
}
func (s *Server) reconcileOne(ctx context.Context, o Order) error {
	q, e := s.pay.call(ctx, s.client, "alipay.trade.query", map[string]string{"out_trade_no": o.ID})
	if e != nil {
		return e
	}
	if q.Code == "40004" && q.SubCode == "ACQ.TRADE_NOT_EXIST" {
		if now() <= o.ExpiresAt+60000 {
			return nil
		}
		return s.applyPaymentEvidence(url.Values{"notify_id": {"query_absent_" + o.ID}, "out_trade_no": {o.ID}, "trade_no": {"absent_" + o.ID}, "trade_status": {"TRADE_CLOSED"}, "total_amount": {money(o.AmountCt)}})
	}
	if q.Code != "10000" || q.OrderID != o.ID || q.TradeNo == "" {
		return errors.New("gateway query did not confirm order")
	}
	amount, e := parseMoney(q.Amount)
	if e != nil || amount != o.AmountCt {
		return errors.New("gateway query amount mismatch")
	}
	if q.Status == "WAIT_BUYER_PAY" {
		if now() <= o.ExpiresAt+60000 {
			return nil
		}
		closed, e := s.pay.call(ctx, s.client, "alipay.trade.close", map[string]string{"out_trade_no": o.ID})
		if e != nil {
			return e
		}
		if closed.Code != "10000" || closed.OrderID != o.ID {
			return errors.New("gateway close not confirmed")
		}
		q, e = s.pay.call(ctx, s.client, "alipay.trade.query", map[string]string{"out_trade_no": o.ID})
		if e != nil {
			return e
		}
		amount, e = parseMoney(q.Amount)
		if e != nil || amount != o.AmountCt || q.Code != "10000" || q.OrderID != o.ID || q.TradeNo == "" {
			return errors.New("post-close query not confirmed")
		}
	}
	if q.Status == "WAIT_BUYER_PAY" {
		return nil
	}
	return s.applyPaymentEvidence(url.Values{"notify_id": {"query_" + q.TradeNo + "_" + q.Status}, "out_trade_no": {o.ID}, "trade_no": {q.TradeNo}, "trade_status": {q.Status}, "total_amount": {q.Amount}})
}
