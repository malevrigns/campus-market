package service

import (
	"context"
	"database/sql"
	"fmt"
	"strconv"
	"testing"
	"time"

	"github.com/redis/go-redis/v9"

	"qingfly/edu-mall/internal/domain"
	"qingfly/edu-mall/internal/pkg/delayqueue"
	"qingfly/edu-mall/internal/pkg/snowflake"
	"qingfly/edu-mall/internal/port"
	mysqlrepo "qingfly/edu-mall/internal/repository/mysql"
	redisrepo "qingfly/edu-mall/internal/repository/redis"
	"qingfly/edu-mall/internal/testenv"
)

type testOrderIDs struct{ node *snowflake.Node }

func (o testOrderIDs) Next() (string, error) {
	n, err := o.node.Next()
	if err != nil {
		return "", err
	}
	return "CM" + strconv.FormatInt(n, 10), nil
}

type flashFixture struct {
	t      *testing.T
	sql    *sql.DB
	stores *mysqlrepo.Stores
	rdb    *redis.Client
	prefix string
	svc    *FlashService
	redis  *redisrepo.FlashStore
}

func setupFlash(t *testing.T) *flashFixture {
	t.Helper()
	db := testenv.MySQL(t)
	rdb, prefix := testenv.Redis(t)
	stores := mysqlrepo.New(db)
	repos := redisrepo.New(rdb, prefix, "test", 15*time.Minute)
	t.Cleanup(repos.Close)
	node, _ := snowflake.NewNode(1)
	queue := redisrepo.NewTimeoutQueue(delayqueue.New(rdb, prefix, "order-timeout", delayqueue.Options{Visibility: 30 * time.Second, Workers: 2}))
	limiter := redisrepo.NewLimiter(rdb, prefix)
	svc := NewFlash(stores.Flash, repos.Cache, repos.Flash, stores.Order, testOrderIDs{node: node}, queue, repos.Live, limiter, 15*time.Minute)
	f := &flashFixture{t: t, sql: db, stores: stores, rdb: rdb, prefix: prefix, svc: svc, redis: repos.Flash}
	f.user("organizer")
	return f
}

func (f *flashFixture) user(id string) {
	f.t.Helper()
	if _, err := f.sql.Exec("INSERT INTO users (id,email,name,password,created_at) VALUES (?,?,?,?,?)", id, id+"@campus.test", id, "x", 1); err != nil {
		f.t.Fatal(err)
	}
}

func (f *flashFixture) sale(stock int, start, end time.Time) domain.FlashSale {
	f.t.Helper()
	sale, err := f.svc.Create(context.Background(), &domain.User{ID: "organizer", Name: "主办方"}, domain.CreateFlashSaleInput{Title: "社团义卖 · 台灯", Description: "限量", Image: "https://example.test/lamp.jpg", Campus: "东区", OriginalPriceCt: 9900, PriceCt: 100, TotalStock: stock, StartAt: start.UnixMilli(), EndAt: end.UnixMilli()})
	if err != nil {
		f.t.Fatal(err)
	}
	return sale
}

func (f *flashFixture) waitOrders(saleID string, want int) {
	f.t.Helper()
	deadline := time.Now().Add(15 * time.Second)
	for {
		var n int
		_ = f.sql.QueryRow("SELECT COUNT(*) FROM orders WHERE flash_sale_id=?", saleID).Scan(&n)
		if n == want {
			return
		}
		if time.Now().After(deadline) {
			f.t.Fatalf("orders persisted %d, want %d", n, want)
		}
		time.Sleep(30 * time.Millisecond)
	}
}

func (f *flashFixture) stocks(saleID string) (db, cache int) {
	f.t.Helper()
	if err := f.sql.QueryRow("SELECT stock FROM flash_sales WHERE id=?", saleID).Scan(&db); err != nil {
		f.t.Fatal(err)
	}
	cache, _ = f.rdb.Get(context.Background(), f.key(saleID, "stock")).Int()
	return
}

func (f *flashFixture) key(saleID, part string) string {
	return f.prefix + ":flash:" + saleID + ":" + part
}

func TestFlashRedeliveryIsIdempotent(t *testing.T) {
	f := setupFlash(t)
	f.user("buyer")
	ctx := context.Background()
	s := f.sale(3, time.Now().Add(-time.Second), time.Now().Add(time.Hour))
	ticket, _, err := f.svc.Attempt(ctx, &domain.User{ID: "buyer"}, s.ID)
	if err != nil {
		t.Fatal(err)
	}
	msg := domain.FlashOrderMessage{SaleID: s.ID, UserID: "buyer", OrderID: ticket.OrderID}
	if !f.svc.handleMessage(ctx, msg) || !f.svc.handleMessage(ctx, msg) {
		t.Fatal("handle must succeed for duplicates")
	}
	if dbStock, _ := f.stocks(s.ID); dbStock != 2 {
		t.Fatalf("db stock %d, want 2", dbStock)
	}
	got, err := f.svc.Result(ctx, &domain.User{ID: "buyer"}, s.ID)
	if err != nil || got.State != "success" || got.OrderStatus != "pending" {
		t.Fatalf("result %+v %v", got, err)
	}
}

func TestFlashRejectedMessageCompensatesRedis(t *testing.T) {
	f := setupFlash(t)
	f.user("buyer")
	ctx := context.Background()
	s := f.sale(3, time.Now().Add(-time.Second), time.Now().Add(time.Hour))
	tk, _, _ := f.svc.Attempt(ctx, &domain.User{ID: "buyer"}, s.ID)
	if !f.svc.handleMessage(ctx, domain.FlashOrderMessage{SaleID: s.ID, UserID: "buyer", OrderID: tk.OrderID}) {
		t.Fatal("first order")
	}
	// 模拟 Redis 数据丢失后用户再次抢到资格（新订单号）。
	f.rdb.HDel(ctx, f.key(s.ID, "tickets"), "buyer")
	tk2, _, err := f.svc.Attempt(ctx, &domain.User{ID: "buyer"}, s.ID)
	if err != nil || tk2.OrderID == tk.OrderID {
		t.Fatal(tk2, err)
	}
	_, before := f.stocks(s.ID)
	if !f.svc.handleMessage(ctx, domain.FlashOrderMessage{SaleID: s.ID, UserID: "buyer", OrderID: tk2.OrderID}) {
		t.Fatal("rejection must ack")
	}
	dbStock, after := f.stocks(s.ID)
	if after != before+1 || dbStock != 2 {
		t.Fatalf("compensation: redis %d->%d db=%d", before, after, dbStock)
	}
	if got, _ := f.svc.Result(ctx, &domain.User{ID: "buyer"}, s.ID); got.State != "failed" {
		t.Fatalf("ticket state %+v", got)
	}
}

func TestFlashCrashedConsumerIsReclaimed(t *testing.T) {
	f := setupFlash(t)
	f.redis.Configure(200*time.Millisecond, 5)
	f.user("buyer")
	ctx := context.Background()
	s := f.sale(3, time.Now().Add(-time.Second), time.Now().Add(time.Hour))
	if _, _, err := f.svc.Attempt(ctx, &domain.User{ID: "buyer"}, s.ID); err != nil {
		t.Fatal(err)
	}
	if err := f.redis.EnsureGroup(ctx); err != nil {
		t.Fatal(err)
	}
	// “崩溃的消费者”读走消息但不处理、不 ACK。
	if _, err := f.rdb.XReadGroup(ctx, &redis.XReadGroupArgs{Group: "order-writer", Consumer: "crashed", Streams: []string{f.prefix + ":flash:orders", ">"}, Count: 10}).Result(); err != nil {
		t.Fatal(err)
	}
	time.Sleep(250 * time.Millisecond)
	if err := f.svc.reclaim(ctx); err != nil {
		t.Fatal(err)
	}
	f.waitOrders(s.ID, 1)
	if p, _ := f.rdb.XPending(ctx, f.prefix+":flash:orders", "order-writer").Result(); p.Count != 0 {
		t.Fatalf("pending after reclaim: %d", p.Count)
	}
}

func TestFlashPoisonMessageGoesToDeadLetter(t *testing.T) {
	f := setupFlash(t)
	f.redis.Configure(50*time.Millisecond, 3)
	ctx := context.Background()
	s := f.sale(3, time.Now().Add(-time.Second), time.Now().Add(time.Hour))
	if _, _, err := f.svc.Attempt(ctx, &domain.User{ID: "ghost"}, s.ID); err != nil { // ghost 不在 users 表
		t.Fatal(err)
	}
	if err := f.redis.EnsureGroup(ctx); err != nil {
		t.Fatal(err)
	}
	if _, err := f.rdb.XReadGroup(ctx, &redis.XReadGroupArgs{Group: "order-writer", Consumer: "c", Streams: []string{f.prefix + ":flash:orders", ">"}, Count: 10}).Result(); err != nil {
		t.Fatal(err)
	}
	for i := 0; i < 6; i++ {
		time.Sleep(60 * time.Millisecond)
		if err := f.svc.reclaim(ctx); err != nil {
			t.Fatal(err)
		}
	}
	if n := f.rdb.XLen(ctx, f.prefix+":flash:orders:dlq").Val(); n != 1 {
		t.Fatalf("dead letters %d", n)
	}
	if _, redisStock := f.stocks(s.ID); redisStock != 3 {
		t.Fatalf("redis stock not compensated: %d", redisStock)
	}
}

func TestFlashReleaseAndReconcile(t *testing.T) {
	f := setupFlash(t)
	f.user("buyer")
	ctx := context.Background()
	s := f.sale(2, time.Now().Add(-time.Second), time.Now().Add(time.Hour))
	tk, _, _ := f.svc.Attempt(ctx, &domain.User{ID: "buyer"}, s.ID)
	f.svc.handleMessage(ctx, domain.FlashOrderMessage{SaleID: s.ID, UserID: "buyer", OrderID: tk.OrderID})
	// 业务层取消订单：事务内归还 DB 库存，提交后归还 Redis 库存。
	if err := f.stores.Order.InTx(ctx, func(tx port.OrderTx) error {
		return tx.ReleaseFlashStock(ctx, s.ID, now())
	}); err != nil {
		t.Fatal(err)
	}
	f.redis.Release(ctx, s.ID, "buyer", tk.OrderID)
	f.redis.Release(ctx, s.ID, "buyer", tk.OrderID)
	if dbStock, redisStock := f.stocks(s.ID); dbStock != 2 || redisStock != 2 {
		t.Fatalf("after release db=%d redis=%d", dbStock, redisStock)
	}
	// 人为制造偏差：对账需要连续两轮观察到同样的偏差才修正。
	f.rdb.Set(ctx, f.key(s.ID, "stock"), 7, 0)
	_ = f.svc.Reconcile(ctx)
	if _, redisStock := f.stocks(s.ID); redisStock != 7 {
		t.Fatal("first observation must not fix yet")
	}
	_ = f.svc.Reconcile(ctx)
	if _, redisStock := f.stocks(s.ID); redisStock != 2 {
		t.Fatalf("reconcile did not repair stock: %d", redisStock)
	}
	f.rdb.Del(ctx, f.key(s.ID, "stock"), f.key(s.ID, "info"))
	_ = f.svc.Reconcile(ctx)
	if _, redisStock := f.stocks(s.ID); redisStock != 2 {
		t.Fatalf("re-preheat stock: %d", redisStock)
	}
}

func TestFlashTrimKeepsUnackedMessages(t *testing.T) {
	f := setupFlash(t)
	ctx := context.Background()
	if err := f.redis.EnsureGroup(ctx); err != nil {
		t.Fatal(err)
	}
	stream := f.prefix + ":flash:orders"
	for i := 0; i < 5; i++ {
		f.rdb.XAdd(ctx, &redis.XAddArgs{Stream: stream, Values: map[string]any{"sale": "x", "uid": "u", "oid": fmt.Sprint(i)}})
	}
	msgs, _ := f.rdb.XReadGroup(ctx, &redis.XReadGroupArgs{Group: "order-writer", Consumer: "c", Streams: []string{stream, ">"}, Count: 5}).Result()
	ids := []string{}
	for _, m := range msgs[0].Messages {
		ids = append(ids, m.ID)
	}
	f.rdb.XAck(ctx, stream, "order-writer", ids[0], ids[1], ids[3], ids[4])
	if err := f.redis.Trim(ctx); err != nil {
		t.Fatal(err)
	}
	left, _ := f.rdb.XRange(ctx, stream, ids[2], ids[2]).Result()
	if len(left) != 1 {
		t.Fatal("unacked message was trimmed")
	}
}
