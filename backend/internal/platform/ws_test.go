package platform

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/coder/websocket"
)

// TestRealtimeAcrossInstances 模拟两个应用实例共享 MySQL + Redis：
// 买家的 WebSocket 连在实例 A，卖家通过实例 B 发消息，事件经 Redis Pub/Sub 转发到实例 A 推送给买家。
func TestRealtimeAcrossInstances(t *testing.T) {
	a, db := testApp(t)
	b, e := NewServer(db, a.rdb, Config{AuthDelivery: DeliveryConfig{Mode: "local"}, RedisPrefix: a.cfg.RedisPrefix, NodeID: 1})
	if e != nil {
		t.Fatal(e)
	}
	t.Cleanup(func() { b.Close(context.Background()) })
	runWorkers(t, a)
	runWorkers(t, b)
	srvA := httptest.NewServer(a.Handler())
	defer srvA.Close()
	seller := account(t, b, "seller")
	buyer := account(t, a, "buyer")
	pid := publish(t, seller)
	_, d := buyer.call("POST", "/api/v1/conversations", map[string]any{"product_id": pid})
	conv := d["id"].(string)

	header := http.Header{}
	// 会话 cookie 名与 Host 绑定（开发环境不同端口互不干扰），这里使用兼容的通用名。
	header.Set("Cookie", "campus_session="+buyer.cookie.Value)
	header.Set("Origin", srvA.URL)
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	ws, resp, err := websocket.Dial(ctx, "ws"+strings.TrimPrefix(srvA.URL, "http")+"/api/v1/ws", &websocket.DialOptions{HTTPHeader: header, Host: strings.TrimPrefix(srvA.URL, "http://")})
	if err != nil {
		t.Fatalf("dial: %v %v", err, resp)
	}
	defer ws.CloseNow()
	time.Sleep(100 * time.Millisecond) // 等两个实例的订阅建立
	sellerOnB := &testClient{t: t, app: b.Handler(), cookie: seller.cookie}
	if s, m := sellerOnB.call("POST", "/api/v1/conversations/"+conv+"/messages", map[string]any{"client_msg_id": "cross-instance-1", "content": "还在，可以面交"}); s != 201 {
		t.Fatalf("send %d %v", s, m)
	}
	_, raw, err := ws.Read(ctx)
	if err != nil {
		t.Fatal(err)
	}
	var ev map[string]any
	_ = json.Unmarshal(raw, &ev)
	if ev["type"] != "message" || ev["conversation_id"] != conv || ev["seq"] != float64(1) {
		t.Fatalf("event %v", ev)
	}
	_, list := seller.call("GET", "/api/v1/conversations", nil)
	if peer := list["items"].([]any)[0].(map[string]any); peer["peer_online"] != true {
		t.Fatalf("buyer presence not visible to seller: %v", peer)
	}
}
