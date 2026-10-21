package seckill

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"strconv"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/redis/go-redis/v9"

	"qingfly/edu-mall/internal/pkg/snowflake"
	"qingfly/edu-mall/internal/testenv"
)

type fixture struct {
	t       *testing.T
	db      *sql.DB
	rdb     *redis.Client
	e       *Engine
	created atomic.Int32
	failed  atomic.Int32
}

func setup(t *testing.T, cfg Config) *fixture {
	t.Helper()
	f := &fixture{t: t, db: testenv.MySQL(t)}
	var prefix string
	f.rdb, prefix = testenv.Redis(t)
	cfg.Prefix = prefix
	node, _ := snowflake.NewNode(1)
	nextID := func() (string, error) {
		n, err := node.Next()
		return "CM" + strconv.FormatInt(n, 10), err
	}
	f.e = New(f.db, f.rdb, cfg, nextID, Hooks{
		OrderCreated: func(context.Context, CreatedOrder) { f.created.Add(1) },
		OrderFailed:  func(context.Context, string, string, string, string) { f.failed.Add(1) },
	})
	f.user("organizer")
	return f
}

func (f *fixture) user(id string) {
	f.t.Helper()
	if _, err := f.db.Exec("INSERT INTO users (id,email,name,password,created_at) VALUES (?,?,?,?,?)", id, id+"@campus.test", id, "x", 1); err != nil {
		f.t.Fatal(err)
	}
}

func (f *fixture) sale(stock int, start, end time.Time) Sale {
	f.t.Helper()
	s, err := f.e.Create(context.Background(), Sale{ID: fmt.Sprintf("fs_%d", time.Now().UnixNano()), SellerID: "organizer", Title: "社团义卖 · 台灯", Description: "限量", Image: "https://example.test/lamp.jpg", Campus: "东区", OriginalPriceCt: 9900, PriceCt: 100, TotalStock: stock, StartAt: start.UnixMilli(), EndAt: end.UnixMilli()})
	if err != nil {
		f.t.Fatal(err)
	}
	return s
}

func (f *fixture) run() {
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() {
		defer close(done)
		_ = f.e.Run(ctx)
	}()
	f.t.Cleanup(func() {
		cancel()
		<-done
	})
}

func (f *fixture) waitOrders(saleID string, want int) {
	f.t.Helper()
	deadline := time.Now().Add(15 * time.Second)
	for {
		var n int
		_ = f.db.QueryRow("SELECT COUNT(*) FROM orders WHERE flash_sale_id=?", saleID).Scan(&n)
		if n == want {
			return
		}
		if time.Now().After(deadline) {
			f.t.Fatalf("orders persisted %d, want %d", n, want)
		}
		time.Sleep(30 * time.Millisecond)
	}
}

func (f *fixture) stocks(saleID string) (db, cache int) {
	f.t.Helper()
	if err := f.db.QueryRow("SELECT stock FROM flash_sales WHERE id=?", saleID).Scan(&db); err != nil {
		f.t.Fatal(err)
	}
	cache, _ = f.rdb.Get(context.Background(), f.e.key(saleID, "stock")).Int()
	return
}

// TestNoOversellUnderContention：400 个用户并发抢 50 件库存，恰好 50 人抢到、50 张订单落库，
// Redis 与数据库库存都归零，每人最多一单。
func TestNoOversellUnderContention(t *testing.T) {
	f := setup(t, Config{Workers: 4})
	const users, stock = 400, 50
	for i := 0; i < users; i++ {
		f.user(fmt.Sprintf("u%03d", i))
	}
	s := f.sale(stock, time.Now().Add(-time.Second), time.Now().Add(time.Hour))
	f.run()
	var won, soldOut atomic.Int32
	var wg sync.WaitGroup
	start := make(chan struct{})
	for i := 0; i < users; i++ {
		wg.Add(1)
		go func(uid string) {
			defer wg.Done()
			<-start
			tk, err := f.e.Attempt(context.Background(), s, uid)
			switch {
			case err == nil && tk.State == "queued":
				won.Add(1)
			case errors.Is(err, ErrSoldOut):
				soldOut.Add(1)
			default:
				t.Errorf("unexpected: %+v %v", tk, err)
			}
		}(fmt.Sprintf("u%03d", i))
	}
	close(start)
	wg.Wait()
	if won.Load() != stock || soldOut.Load() != users-stock {
		t.Fatalf("won=%d soldOut=%d", won.Load(), soldOut.Load())
	}
	f.waitOrders(s.ID, stock)
	dbStock, redisStock := f.stocks(s.ID)
	if dbStock != 0 || redisStock != 0 {
		t.Fatalf("stock db=%d redis=%d", dbStock, redisStock)
	}
	var dup int
	_ = f.db.QueryRow("SELECT COUNT(*) FROM (SELECT buyer_id FROM orders WHERE flash_sale_id=? GROUP BY buyer_id HAVING COUNT(*)>1) t", s.ID).Scan(&dup)
	if dup != 0 || f.created.Load() != stock {
		t.Fatalf("duplicate buyers=%d created hooks=%d", dup, f.created.Load())
	}
}

func TestOnePerUserAndTimeWindow(t *testing.T) {
	f := setup(t, Config{})
	f.user("buyer")
	ctx := context.Background()
	s := f.sale(5, time.Now().Add(-time.Second), time.Now().Add(time.Hour))
	first, err := f.e.Attempt(ctx, s, "buyer")
	if err != nil || first.State != "queued" {
		t.Fatal(first, err)
	}
	again, err := f.e.Attempt(ctx, s, "buyer")
	if err != nil || again.OrderID != first.OrderID {
		t.Fatalf("repeat attempt must return the same order: %+v %v", again, err)
	}
	if _, err = f.e.Attempt(ctx, s, "organizer"); !errors.Is(err, ErrOwnSale) {
		t.Fatalf("own sale: %v", err)
	}
	future := f.sale(5, time.Now().Add(time.Hour), time.Now().Add(2*time.Hour))
	if _, err = f.e.Attempt(ctx, future, "buyer"); !errors.Is(err, ErrNotStarted) {
		t.Fatalf("not started: %v", err)
	}
	if err = f.e.Cancel(ctx, future.ID); err != nil {
		t.Fatal(err)
	}
	if _, err = f.e.Attempt(ctx, future, "buyer"); !errors.Is(err, ErrOffline) {
		t.Fatalf("offline: %v", err)
	}
	if _, err = f.db.Exec("UPDATE flash_sales SET end_at=? WHERE id=?", time.Now().Add(300*time.Millisecond).UnixMilli(), s.ID); err != nil {
		t.Fatal(err)
	}
	s.EndAt = time.Now().Add(300 * time.Millisecond).UnixMilli()
	if err = f.e.Preheat(ctx, s, false); err != nil {
		t.Fatal(err)
	}
	time.Sleep(400 * time.Millisecond)
	f.user("late")
	if _, err = f.e.Attempt(ctx, s, "late"); !errors.Is(err, ErrEnded) {
		t.Fatalf("ended: %v", err)
	}
}

// TestRedeliveryIsIdempotent：同一条消息被投递两次（消费者处理完但 ACK 前崩溃），
// 订单主键冲突 → 视为已处理，不会重复扣减数据库库存。
func TestRedeliveryIsIdempotent(t *testing.T) {
	f := setup(t, Config{})
	f.user("buyer")
	ctx := context.Background()
	s := f.sale(3, time.Now().Add(-time.Second), time.Now().Add(time.Hour))
	tk, err := f.e.Attempt(ctx, s, "buyer")
	if err != nil {
		t.Fatal(err)
	}
	m := message{SaleID: s.ID, UserID: "buyer", OrderID: tk.OrderID}
	if !f.e.handle(ctx, m) || !f.e.handle(ctx, m) {
		t.Fatal("handle must succeed for duplicates")
	}
	dbStock, _ := f.stocks(s.ID)
	if dbStock != 2 {
		t.Fatalf("db stock %d, want 2", dbStock)
	}
	if got, _ := f.e.Result(ctx, s.ID, "buyer"); got.State != "success" || got.OrderStatus != "pending" {
		t.Fatalf("result %+v", got)
	}
}

// TestRejectedMessageCompensatesRedis：数据库唯一键发现该用户已有订单（例如 Redis 故障恢复后资格丢失），
// 消息被拒绝，Redis 库存归还，资格标记为失败。
func TestRejectedMessageCompensatesRedis(t *testing.T) {
	f := setup(t, Config{})
	f.user("buyer")
	ctx := context.Background()
	s := f.sale(3, time.Now().Add(-time.Second), time.Now().Add(time.Hour))
	tk, _ := f.e.Attempt(ctx, s, "buyer")
	if !f.e.handle(ctx, message{SaleID: s.ID, UserID: "buyer", OrderID: tk.OrderID}) {
		t.Fatal("first order")
	}
	// 模拟 Redis 数据丢失后用户再次抢到资格（新订单号）。
	f.rdb.HDel(ctx, f.e.key(s.ID, "tickets"), "buyer")
	tk2, err := f.e.Attempt(ctx, s, "buyer")
	if err != nil || tk2.OrderID == tk.OrderID {
		t.Fatal(tk2, err)
	}
	_, before := f.stocks(s.ID)
	if !f.e.handle(ctx, message{SaleID: s.ID, UserID: "buyer", OrderID: tk2.OrderID}) {
		t.Fatal("rejection must ack")
	}
	dbStock, after := f.stocks(s.ID)
	if after != before+1 || dbStock != 2 || f.failed.Load() != 1 {
		t.Fatalf("compensation: redis %d->%d db=%d failed=%d", before, after, dbStock, f.failed.Load())
	}
	if got, _ := f.e.Result(ctx, s.ID, "buyer"); got.State != "failed" {
		t.Fatalf("ticket state %+v", got)
	}
}

// TestCrashedConsumerMessageIsReclaimed：消费者读到消息后崩溃（消息留在 PEL），
// 空闲超时后被 XCLAIM 并成功落库。
func TestCrashedConsumerMessageIsReclaimed(t *testing.T) {
	f := setup(t, Config{ClaimIdle: 200 * time.Millisecond})
	f.user("buyer")
	ctx := context.Background()
	s := f.sale(3, time.Now().Add(-time.Second), time.Now().Add(time.Hour))
	if _, err := f.e.Attempt(ctx, s, "buyer"); err != nil {
		t.Fatal(err)
	}
	if err := f.e.ensureGroup(ctx); err != nil {
		t.Fatal(err)
	}
	// "崩溃的消费者"读走消息但不处理、不 ACK。
	if _, err := f.rdb.XReadGroup(ctx, &redis.XReadGroupArgs{Group: f.e.group, Consumer: "crashed", Streams: []string{f.e.stream, ">"}, Count: 10}).Result(); err != nil {
		t.Fatal(err)
	}
	time.Sleep(250 * time.Millisecond)
	if err := f.e.reclaim(ctx); err != nil {
		t.Fatal(err)
	}
	f.waitOrders(s.ID, 1)
	if p, _ := f.rdb.XPending(ctx, f.e.stream, f.e.group).Result(); p.Count != 0 {
		t.Fatalf("pending after reclaim: %d", p.Count)
	}
}

// TestPoisonMessageGoesToDeadLetter：持续失败（这里用不存在的用户触发外键错误）的消息
// 超过最大投递次数后进入死信流，并补偿 Redis 库存。
func TestPoisonMessageGoesToDeadLetter(t *testing.T) {
	f := setup(t, Config{ClaimIdle: 50 * time.Millisecond, MaxDeliveries: 3})
	ctx := context.Background()
	s := f.sale(3, time.Now().Add(-time.Second), time.Now().Add(time.Hour))
	if _, err := f.e.Attempt(ctx, s, "ghost"); err != nil { // ghost 不在 users 表
		t.Fatal(err)
	}
	_ = f.e.ensureGroup(ctx)
	_, _ = f.rdb.XReadGroup(ctx, &redis.XReadGroupArgs{Group: f.e.group, Consumer: "c", Streams: []string{f.e.stream, ">"}, Count: 10}).Result()
	for i := 0; i < 5; i++ {
		time.Sleep(60 * time.Millisecond)
		if err := f.e.reclaim(ctx); err != nil {
			t.Fatal(err)
		}
	}
	if n := f.rdb.XLen(ctx, f.e.dlq).Val(); n != 1 {
		t.Fatalf("dead letters %d", n)
	}
	if _, redisStock := f.stocks(s.ID); redisStock != 3 {
		t.Fatalf("redis stock not compensated: %d", redisStock)
	}
}

func TestReleaseAndReconcile(t *testing.T) {
	f := setup(t, Config{})
	f.user("buyer")
	ctx := context.Background()
	s := f.sale(2, time.Now().Add(-time.Second), time.Now().Add(time.Hour))
	tk, _ := f.e.Attempt(ctx, s, "buyer")
	f.e.handle(ctx, message{SaleID: s.ID, UserID: "buyer", OrderID: tk.OrderID})
	// 业务层取消订单：事务内归还 DB 库存，提交后归还 Redis 库存。
	tx, _ := f.db.Begin()
	if err := ReleaseTx(tx, s.ID); err != nil {
		t.Fatal(err)
	}
	_ = tx.Commit()
	f.e.ReleaseRedis(ctx, s.ID, "buyer", tk.OrderID)
	f.e.ReleaseRedis(ctx, s.ID, "buyer", tk.OrderID) // 重复取消不能多加库存
	if dbStock, redisStock := f.stocks(s.ID); dbStock != 2 || redisStock != 2 {
		t.Fatalf("after release db=%d redis=%d", dbStock, redisStock)
	}
	// 人为制造偏差：对账需要连续两轮观察到同样的偏差才修正。
	f.rdb.Set(ctx, f.e.key(s.ID, "stock"), 7, 0)
	_ = f.e.Reconcile(ctx)
	if _, redisStock := f.stocks(s.ID); redisStock != 7 {
		t.Fatal("first observation must not fix yet")
	}
	_ = f.e.Reconcile(ctx)
	if _, redisStock := f.stocks(s.ID); redisStock != 2 {
		t.Fatalf("reconcile did not repair stock: %d", redisStock)
	}
	// Redis 数据整体丢失时重新预热。
	f.rdb.Del(ctx, f.e.key(s.ID, "stock"), f.e.key(s.ID, "info"))
	_ = f.e.Reconcile(ctx)
	if _, redisStock := f.stocks(s.ID); redisStock != 2 {
		t.Fatalf("re-preheat stock: %d", redisStock)
	}
}

func TestTrimKeepsUnackedMessages(t *testing.T) {
	f := setup(t, Config{})
	ctx := context.Background()
	_ = f.e.ensureGroup(ctx)
	for i := 0; i < 5; i++ {
		f.rdb.XAdd(ctx, &redis.XAddArgs{Stream: f.e.stream, Values: map[string]any{"sale": "x", "uid": "u", "oid": fmt.Sprint(i)}})
	}
	msgs, _ := f.rdb.XReadGroup(ctx, &redis.XReadGroupArgs{Group: f.e.group, Consumer: "c", Streams: []string{f.e.stream, ">"}, Count: 5}).Result()
	ids := []string{}
	for _, m := range msgs[0].Messages {
		ids = append(ids, m.ID)
	}
	f.rdb.XAck(ctx, f.e.stream, f.e.group, ids[0], ids[1], ids[3], ids[4]) // ids[2] 未确认
	if err := f.e.trim(ctx); err != nil {
		t.Fatal(err)
	}
	left, _ := f.rdb.XRange(ctx, f.e.stream, ids[2], ids[2]).Result()
	if len(left) != 1 {
		t.Fatal("unacked message was trimmed")
	}
}
