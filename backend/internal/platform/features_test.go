package platform

import (
	"fmt"
	"sync"
	"testing"
	"time"
)

func userID(t *testing.T, c *testClient) string {
	t.Helper()
	s, d := c.call("GET", "/api/v1/auth/me", nil)
	if s != 200 {
		t.Fatalf("me %d", s)
	}
	return d["id"].(string)
}

func eventually(t *testing.T, d time.Duration, what string, cond func() bool) {
	t.Helper()
	deadline := time.Now().Add(d)
	for !cond() {
		if time.Now().After(deadline) {
			t.Fatalf("timeout waiting for %s", what)
		}
		time.Sleep(50 * time.Millisecond)
	}
}

// TestFlashSaleEndToEnd 走完整 HTTP 链路：管理员建活动 → 并发抢购 → 异步落单 → 查询结果 →
// 取消订单归还库存 → 其他用户重新抢到。
func TestFlashSaleEndToEnd(t *testing.T) {
	a, db := testApp(t)
	runWorkers(t, a)
	admin := account(t, a, "organizer")
	a.cfg.AdminUserIDs = userID(t, admin)
	start := now() - 1000
	s, d := admin.call("POST", "/api/v1/admin/flash-sales", map[string]any{"title": "毕业季台灯 1 元抢", "description": "社团义卖，限量 3 件", "image": "https://images.unsplash.com/photo-1507473885765-e6ed057f782c", "campus": "东区", "original_price_ct": 6500, "price_ct": 100, "total_stock": 3, "start_at": start, "end_at": start + 3600_000})
	if s != 201 {
		t.Fatalf("create sale %d %v", s, d)
	}
	sid := d["id"].(string)
	buyers := []*testClient{}
	for i := 0; i < 5; i++ {
		buyers = append(buyers, account(t, a, fmt.Sprintf("fan%d", i)))
	}
	if s, _ := buyers[0].call("POST", "/api/v1/orders", map[string]any{"product_id": sid, "campus": "东区", "idempotency_key": "not-a-product"}); s != 404 {
		t.Fatalf("flash sale must not be bought as a product: %d", s)
	}
	codes := make([]int, len(buyers))
	tickets := make([]map[string]any, len(buyers))
	var wg sync.WaitGroup
	for i, b := range buyers {
		wg.Add(1)
		go func(i int, b *testClient) {
			defer wg.Done()
			codes[i], tickets[i] = b.call("POST", "/api/v1/flash-sales/"+sid+"/attempts", map[string]any{})
		}(i, b)
	}
	wg.Wait()
	winners, losers := []int{}, []int{}
	for i, c := range codes {
		switch c {
		case 202:
			winners = append(winners, i)
		case 409:
			losers = append(losers, i)
		default:
			t.Fatalf("attempt %d: %d %v", i, c, tickets[i])
		}
	}
	if len(winners) != 3 || len(losers) != 2 {
		t.Fatalf("winners=%v losers=%v", winners, losers)
	}
	w := buyers[winners[0]]
	oid := tickets[winners[0]]["order_id"].(string)
	eventually(t, 10*time.Second, "flash order persisted", func() bool {
		_, r := w.call("GET", "/api/v1/flash-sales/"+sid+"/attempts/me", nil)
		return r["state"] == "success" && r["order_status"] == "pending"
	})
	if s, d := w.call("GET", "/api/v1/orders/"+oid, nil); s != 200 || d["flash_sale_id"] != sid || d["amount_ct"] != float64(100) {
		t.Fatalf("flash order detail %d %v", s, d)
	}
	if s, d := w.call("POST", "/api/v1/flash-sales/"+sid+"/attempts", map[string]any{}); s != 200 || d["order_id"] != oid {
		t.Fatalf("repeat attempt must be idempotent: %d %v", s, d)
	}
	_, detail := w.call("GET", "/api/v1/flash-sales/"+sid, nil)
	if sale := detail["sale"].(map[string]any); sale["live_stock"] != float64(0) || sale["phase"] != "active" {
		t.Fatalf("sale detail %v", sale)
	}
	if s, d := w.call("POST", "/api/v1/orders/"+oid+"/cancel", map[string]any{}); s != 200 {
		t.Fatalf("cancel flash order %d %v", s, d)
	}
	var stock int
	if e := db.QueryRow("SELECT stock FROM flash_sales WHERE id=?", sid).Scan(&stock); e != nil || stock != 1 {
		t.Fatalf("db stock after cancel %d %v", stock, e)
	}
	// 一人一单：取消后不能再抢；但库存回到池子里，之前没抢到的人可以抢到。
	if s, _ := w.call("POST", "/api/v1/flash-sales/"+sid+"/attempts", map[string]any{}); s != 200 {
		t.Fatalf("cancelled buyer should get its cancelled ticket back, got %d", s)
	}
	time.Sleep(600 * time.Millisecond) // 本地售罄标记过期
	if s, d := buyers[losers[0]].call("POST", "/api/v1/flash-sales/"+sid+"/attempts", map[string]any{}); s != 202 {
		t.Fatalf("released stock not available to others: %d %v", s, d)
	}
	if s, _ := admin.call("POST", "/api/v1/flash-sales/"+sid+"/attempts", map[string]any{}); s != 400 {
		t.Fatalf("organizer bought own sale: %d", s)
	}
	if s, _ := buyers[0].call("POST", "/api/v1/admin/flash-sales", map[string]any{}); s != 403 {
		t.Fatalf("non-admin created sale: %d", s)
	}
}

// TestOrderTimeoutViaDelayQueue：订单到期后由延迟队列自动关单，商品详情缓存同步失效。
func TestOrderTimeoutViaDelayQueue(t *testing.T) {
	a, _ := testAppWith(t, Config{AuthDelivery: DeliveryConfig{Mode: "local"}, PaymentWindow: 1500 * time.Millisecond})
	runWorkers(t, a)
	seller := account(t, a, "seller")
	buyer := account(t, a, "buyer")
	pid := publish(t, seller)
	if _, d := buyer.call("GET", "/api/v1/products/"+pid, nil); d["status"] != "available" {
		t.Fatalf("product %v", d)
	}
	s, o := order(buyer, pid, "timeout-order-key")
	if s != 201 {
		t.Fatalf("order %d", s)
	}
	if _, d := buyer.call("GET", "/api/v1/products/"+pid, nil); d["status"] != "reserved" {
		t.Fatalf("cache not invalidated after order: %v", d)
	}
	eventually(t, 8*time.Second, "order expiry", func() bool {
		_, d := buyer.call("GET", "/api/v1/orders/"+o["id"].(string), nil)
		return d["status"] == "cancelled"
	})
	eventually(t, 2*time.Second, "product released", func() bool {
		_, d := buyer.call("GET", "/api/v1/products/"+pid, nil)
		return d["status"] == "available"
	})
	ready, inflight, dead, _ := a.timeouts.Stats(t.Context())
	if ready+inflight+dead != 0 {
		t.Fatalf("delay queue not drained %d %d %d", ready, inflight, dead)
	}
}

func TestProductCacheInvalidationAndHotRank(t *testing.T) {
	a, _ := testApp(t)
	seller := account(t, a, "seller")
	fan := account(t, a, "fan")
	anon := &testClient{t: t, app: a.Handler()}
	pid := publish(t, seller)
	other := publish(t, seller)
	for i := 0; i < 3; i++ {
		fan.call("GET", "/api/v1/products/"+pid, nil) // 同一访客当天只计一次
	}
	anon.call("GET", "/api/v1/products/"+pid, nil)
	fan.call("PUT", "/api/v1/favorites/"+pid, nil)
	fan.call("PUT", "/api/v1/favorites/"+pid, nil) // 重复收藏不重复加分
	anon.call("GET", "/api/v1/products/"+other, nil)
	_, hot := anon.call("GET", "/api/v1/products/hot?limit=5", nil)
	items := hot["items"].([]any)
	if len(items) != 2 || items[0].(map[string]any)["id"] != pid || items[0].(map[string]any)["score"] != float64(5) {
		t.Fatalf("hot list %v", items)
	}
	if s, _ := seller.call("DELETE", "/api/v1/products/"+pid, nil); s != 200 {
		t.Fatalf("withdraw %d", s)
	}
	if _, d := anon.call("GET", "/api/v1/products/"+pid, nil); d["status"] != "withdrawn" {
		t.Fatalf("stale product cache after withdraw: %v", d)
	}
	if s, _ := anon.call("GET", "/api/v1/products/p_doesnotexist", nil); s != 404 {
		t.Fatalf("missing product %d", s)
	}
}

func TestCursorPaginationAndFeedCache(t *testing.T) {
	a, _ := testApp(t)
	seller := account(t, a, "seller")
	for i := 0; i < 30; i++ {
		publish(t, seller)
	}
	anon := &testClient{t: t, app: a.Handler()}
	seen := map[string]bool{}
	_, page := anon.call("GET", "/api/v1/products", nil)
	for pages := 1; ; pages++ {
		for _, it := range page["items"].([]any) {
			id := it.(map[string]any)["id"].(string)
			if seen[id] {
				t.Fatalf("duplicate %s on page %d", id, pages)
			}
			seen[id] = true
		}
		if page["has_more"] != true {
			break
		}
		_, page = anon.call("GET", "/api/v1/products?cursor="+page["next_cursor"].(string), nil)
	}
	if len(seen) != 30 {
		t.Fatalf("cursor pagination returned %d/30", len(seen))
	}
	newest := publish(t, seller)
	_, page = anon.call("GET", "/api/v1/products", nil)
	if first := page["items"].([]any)[0].(map[string]any); first["id"] != newest || page["total"] != float64(31) {
		t.Fatalf("feed cache not invalidated on publish: %v total=%v", first["id"], page["total"])
	}
	if s, _ := anon.call("GET", "/api/v1/products?cursor=bad", nil); s != 400 {
		t.Fatalf("bad cursor %d", s)
	}
	if _, d := anon.call("GET", "/api/v1/products?q=%25", nil); d["total"] != float64(0) {
		t.Fatalf("LIKE wildcard not escaped: %v", d["total"])
	}
}

// TestConcurrentMessagesGetDistinctSeq：双方同时发送 20 条消息，seq 恰好是 1..20，不重不漏。
func TestConcurrentMessagesGetDistinctSeq(t *testing.T) {
	a, _ := testApp(t)
	seller := account(t, a, "seller")
	buyer := account(t, a, "buyer")
	pid := publish(t, seller)
	_, d := buyer.call("POST", "/api/v1/conversations", map[string]any{"product_id": pid})
	path := "/api/v1/conversations/" + d["id"].(string) + "/messages"
	var mu sync.Mutex
	seqs := map[float64]bool{}
	var wg sync.WaitGroup
	for i := 0; i < 20; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			c := buyer
			if i%2 == 1 {
				c = seller
			}
			cc := &testClient{t: t, app: c.app, cookie: c.cookie}
			s, m := cc.call("POST", path, map[string]any{"client_msg_id": fmt.Sprintf("concurrent-%02d", i), "content": fmt.Sprintf("第 %d 条", i)})
			if s != 201 {
				t.Errorf("send %d: %d %v", i, s, m)
				return
			}
			mu.Lock()
			seqs[m["seq"].(float64)] = true
			mu.Unlock()
		}(i)
	}
	wg.Wait()
	for i := 1; i <= 20; i++ {
		if !seqs[float64(i)] {
			t.Fatalf("missing seq %d in %v", i, seqs)
		}
	}
	_, list := seller.call("GET", "/api/v1/conversations", nil)
	conv := list["items"].([]any)[0].(map[string]any)
	if conv["seq"] != float64(20) || conv["last_message"] == "" || conv["unread"] != float64(20) {
		t.Fatalf("conversation summary %v", conv)
	}
}
