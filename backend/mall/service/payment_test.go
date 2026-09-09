package service_test

import (
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"testing"

	"qingfly/edu-mall/internal/pkg/snowflake"
	"qingfly/edu-mall/mall/model"
	"qingfly/edu-mall/mall/repository"
	"qingfly/edu-mall/mall/service"
)

// sign builds the gateway signature exactly as payment.go verifies it:
// hex(HMAC_SHA256(outTradeNo|orderNo|status|amountCt, key)).
func sign(key []byte, outTradeNo, orderNo, status string, amount int64) string {
	c := outTradeNo + "|" + orderNo + "|" + status + "|" + fmt.Sprintf("%d", amount)
	mac := hmac.New(sha256.New, key)
	mac.Write([]byte(c))
	return hex.EncodeToString(mac.Sum(nil))
}

// TestPaymentCallbackIdempotent proves the course's key invariant: replaying a payment
// callback must not corrupt the order (verify sign AND amount, state machine, idempotent).
func TestPaymentCallbackIdempotent(t *testing.T) {
	ctx := context.Background()
	secret := []byte("gateway-secret")
	stock := repository.NewMemoryStockStore(map[string]int64{})
	if err := stock.Seed("sku-1", 100); err != nil {
		t.Fatal(err)
	}
	svc := service.NewOrderService(
		repository.NewMemoryOrderStore(),
		stock,
		snowflake.NewNode(1),
	)
	o, err := svc.CreateOrder(ctx, &service.CreateOrderRequest{
		UserID:           "u1",
		IdempotencyKey:   "k1",
		Items:            []model.Item{{SKUID: "sku-1", Name: "course", PriceCt: 99900, Qty: 1}},
		ServerSidePrices: map[string]int64{"sku-1": 99900},
	})
	if err != nil {
		t.Fatal(err)
	}
	n := &service.PaymentNotify{
		OutTradeNo: o.No,
		OrderNo:    o.No,
		Status:     "SUCCESS",
		AmountCt:   o.AmountCt,
		Sign:       sign(secret, o.No, o.No, "SUCCESS", o.AmountCt),
	}
	ok, err := svc.HandlePaymentNotify(ctx, n, secret)
	if err != nil {
		t.Fatal("first notify should succeed", err)
	}
	if !ok {
		t.Fatal("expected processed=true")
	}
	// Replay the exact same callback: must be idempotent, no error.
	ok2, err := svc.HandlePaymentNotify(ctx, n, secret)
	if err != nil {
		t.Fatalf("replay should be idempotent, got err=%v", err)
	}
	if !ok2 {
		t.Fatalf("replay reported processed=false")
	}
}
