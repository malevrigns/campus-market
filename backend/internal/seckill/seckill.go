// Package seckill 实现限时抢购（秒杀）。
//
// 请求链路（同步部分只碰 Redis，不碰数据库）：
//
//	HTTP ─► 令牌桶限流(用户 + 活动) ─► 本地售罄标记 ─► Lua 原子脚本 ─► 202 Accepted(返回预生成订单号)
//	                                               │ 校验时间窗 / 一人一单 / 扣 Redis 库存 / XADD 订单消息
//	                                               ▼
//	                       Redis Stream(消费组) ─► 落库 worker：事务内 UPDATE stock=stock-1 WHERE stock>0
//	                                               │ + INSERT orders（主键=预生成订单号，天然幂等）
//	                                               ├─ 成功：XACK，资格 q→ok，投递 15 分钟超时关单
//	                                               ├─ 业务失败：XACK，资格 q→fail，归还 Redis 库存
//	                                               └─ 系统失败：不 ACK，留在 PEL，由 XPENDING/XCLAIM 重投；
//	                                                  超过最大投递次数进死信流并补偿
//
// 一致性保障：
//   - 数据库库存是最终真相，Redis 库存只是"闸门"：即使 Redis 数据异常，DB 条件更新也不会超卖；
//   - 订单唯一键 (flash_sale_id, buyer_id) 在数据库层保证一人一单；
//   - 定时对账比较 "Redis 库存" 与 "DB 库存 - 排队中资格数"，连续两次一致的偏差才自动修正。
package seckill

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"log/slog"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/redis/go-redis/v9"

	"qingfly/edu-mall/internal/infra/mysqlx"
)

var (
	ErrNotFound    = errors.New("seckill: sale not found")
	ErrNotStarted  = errors.New("seckill: not started")
	ErrEnded       = errors.New("seckill: ended")
	ErrSoldOut     = errors.New("seckill: sold out")
	ErrOffline     = errors.New("seckill: offline")
	ErrUnavailable = errors.New("seckill: temporarily unavailable")
	ErrOwnSale     = errors.New("seckill: cannot buy own sale")
)

// Sale 是一场抢购活动。
type Sale struct {
	ID              string `json:"id"`
	SellerID        string `json:"seller_id"`
	SellerName      string `json:"seller_name"`
	Title           string `json:"title"`
	Description     string `json:"description"`
	Image           string `json:"image"`
	Campus          string `json:"campus"`
	OriginalPriceCt int64  `json:"original_price_ct"`
	PriceCt         int64  `json:"price_ct"`
	TotalStock      int    `json:"total_stock"`
	Stock           int    `json:"stock"`
	StartAt         int64  `json:"start_at"`
	EndAt           int64  `json:"end_at"`
	Status          string `json:"status"`
	CreatedAt       int64  `json:"created_at"`
	// Phase 由时间与状态计算：upcoming / active / ended / cancelled。
	Phase string `json:"phase"`
	// LiveStock 是 Redis 中仍可抢的数量（-1 表示未知）。
	LiveStock int `json:"live_stock"`
}

// Ticket 是用户在某场活动中的抢购资格。
type Ticket struct {
	SaleID  string `json:"sale_id"`
	OrderID string `json:"order_id,omitempty"`
	// State: queued 排队落单中 / success 已生成订单 / failed 未抢到 / cancelled 订单已取消 / none 未参与
	State       string `json:"state"`
	Reason      string `json:"reason,omitempty"`
	OrderStatus string `json:"order_status,omitempty"`
}

// CreatedOrder 在订单落库后回调给业务层。
type CreatedOrder struct {
	OrderID, SaleID, BuyerID, SellerID string
	ExpiresAt                          int64
}

type Hooks struct {
	OrderCreated func(ctx context.Context, o CreatedOrder)
	OrderFailed  func(ctx context.Context, saleID, buyerID, orderID, reason string)
}

type Config struct {
	Prefix        string
	Consumer      string
	Workers       int
	PaymentWindow time.Duration
	ClaimIdle     time.Duration
	MaxDeliveries int64
}

type Engine struct {
	db      *sql.DB
	rdb     redis.UniversalClient
	cfg     Config
	hooks   Hooks
	newID   func() (string, error)
	soldOut sync.Map // saleID -> 本地售罄标记过期时间(unix ms)
	stream  string
	dlq     string
	group   string

	mu         sync.Mutex
	mismatches map[string][2]int
}

func New(db *sql.DB, rdb redis.UniversalClient, cfg Config, newID func() (string, error), hooks Hooks) *Engine {
	if cfg.Workers <= 0 {
		cfg.Workers = 4
	}
	if cfg.PaymentWindow <= 0 {
		cfg.PaymentWindow = 15 * time.Minute
	}
	if cfg.ClaimIdle <= 0 {
		cfg.ClaimIdle = 30 * time.Second
	}
	if cfg.MaxDeliveries <= 0 {
		cfg.MaxDeliveries = 5
	}
	if cfg.Consumer == "" {
		cfg.Consumer = "worker"
	}
	return &Engine{db: db, rdb: rdb, cfg: cfg, hooks: hooks, newID: newID,
		stream: cfg.Prefix + ":flash:orders", dlq: cfg.Prefix + ":flash:orders:dlq", group: "order-writer",
		mismatches: map[string][2]int{}}
}

func (e *Engine) key(saleID, part string) string { return e.cfg.Prefix + ":flash:" + saleID + ":" + part }

func nowMs() int64 { return time.Now().UnixMilli() }

// Phase 根据时间计算活动阶段。
func Phase(s Sale, now int64) string {
	switch {
	case s.Status == "cancelled":
		return "cancelled"
	case now < s.StartAt:
		return "upcoming"
	case now >= s.EndAt:
		return "ended"
	default:
		return "active"
	}
}

func (e *Engine) retention(s Sale) int64 {
	// 活动结束后还要保留资格信息一段时间：用户查结果、订单取消归还库存都要用到。
	return s.EndAt + e.cfg.PaymentWindow.Milliseconds() + 24*time.Hour.Milliseconds()
}

const saleSelect = "SELECT f.id,f.seller_id,u.name,f.title,f.description,f.image,f.campus,f.original_price_ct,f.price_ct,f.total_stock,f.stock,f.start_at,f.end_at,f.status,f.created_at FROM flash_sales f JOIN users u ON u.id=f.seller_id"

type scanner interface{ Scan(...any) error }

func scanSale(row scanner) (Sale, error) {
	var s Sale
	err := row.Scan(&s.ID, &s.SellerID, &s.SellerName, &s.Title, &s.Description, &s.Image, &s.Campus, &s.OriginalPriceCt, &s.PriceCt, &s.TotalStock, &s.Stock, &s.StartAt, &s.EndAt, &s.Status, &s.CreatedAt)
	if errors.Is(err, sql.ErrNoRows) {
		return s, ErrNotFound
	}
	s.Phase = Phase(s, nowMs())
	s.LiveStock = -1
	return s, err
}

// Create 写入活动并预热 Redis。
func (e *Engine) Create(ctx context.Context, s Sale) (Sale, error) {
	s.Stock = s.TotalStock
	s.Status = "scheduled"
	s.CreatedAt = nowMs()
	_, err := e.db.ExecContext(ctx, "INSERT INTO flash_sales (id,seller_id,title,description,image,campus,original_price_ct,price_ct,total_stock,stock,start_at,end_at,status,created_at,updated_at) VALUES (?,?,?,?,?,?,?,?,?,?,?,?,?,?,?)",
		s.ID, s.SellerID, s.Title, s.Description, s.Image, s.Campus, s.OriginalPriceCt, s.PriceCt, s.TotalStock, s.Stock, s.StartAt, s.EndAt, s.Status, s.CreatedAt, s.CreatedAt)
	if err != nil {
		return s, err
	}
	if err = e.Preheat(ctx, s, true); err != nil {
		// 预热失败不回滚活动：后台 Warm 任务会重试，未预热前抢购接口返回 503。
		slog.Warn("flash sale preheat failed", "sale", s.ID, "error", err)
	}
	return e.Get(ctx, s.ID)
}

// Preheat 把活动时间窗与库存写入 Redis。force=true 时覆盖库存（仅用于新建活动）。
func (e *Engine) Preheat(ctx context.Context, s Sale, force bool) error {
	status := "on"
	if s.Status != "scheduled" {
		status = "off"
	}
	f := "0"
	if force {
		f = "1"
	}
	return preheatScript.Run(ctx, e.rdb, []string{e.key(s.ID, "info"), e.key(s.ID, "stock")}, s.StartAt, s.EndAt, status, s.Stock, f, e.retention(s)).Err()
}

// Warm 预热所有未结束的活动（启动时、以及对账时发现 Redis 数据丢失）。
func (e *Engine) Warm(ctx context.Context) error {
	rows, err := e.db.QueryContext(ctx, saleSelect+" WHERE f.end_at>? ORDER BY f.start_at LIMIT 200", nowMs()-e.cfg.PaymentWindow.Milliseconds())
	if err != nil {
		return err
	}
	sales := []Sale{}
	for rows.Next() {
		s, err := scanSale(rows)
		if err != nil {
			rows.Close()
			return err
		}
		sales = append(sales, s)
	}
	rows.Close()
	if err = rows.Err(); err != nil {
		return err
	}
	for _, s := range sales {
		if err = e.Preheat(ctx, s, false); err != nil {
			return err
		}
	}
	return e.ensureGroup(ctx)
}

func (e *Engine) ensureGroup(ctx context.Context) error {
	err := e.rdb.XGroupCreateMkStream(ctx, e.stream, e.group, "0").Err()
	if err != nil && !strings.Contains(err.Error(), "BUSYGROUP") {
		return err
	}
	return nil
}

func (e *Engine) Get(ctx context.Context, id string) (Sale, error) {
	s, err := scanSale(e.db.QueryRowContext(ctx, saleSelect+" WHERE f.id=?", id))
	if err != nil {
		return s, err
	}
	e.FillLiveStock(ctx, &s)
	return s, nil
}

// List 返回未结束和最近一天内结束的活动。
func (e *Engine) List(ctx context.Context) ([]Sale, error) {
	rows, err := e.db.QueryContext(ctx, saleSelect+" WHERE f.end_at>? AND f.status='scheduled' ORDER BY f.start_at LIMIT 50", nowMs()-24*time.Hour.Milliseconds())
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []Sale{}
	for rows.Next() {
		s, err := scanSale(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, s)
	}
	if err = rows.Err(); err != nil {
		return nil, err
	}
	ptrs := make([]*Sale, len(out))
	for i := range out {
		ptrs[i] = &out[i]
	}
	e.FillLiveStock(ctx, ptrs...)
	return out, nil
}

// FillLiveStock 用 MGET 一次取回多个活动的 Redis 实时库存。
func (e *Engine) FillLiveStock(ctx context.Context, sales ...*Sale) {
	if len(sales) == 0 {
		return
	}
	keys := make([]string, len(sales))
	for i, s := range sales {
		keys[i] = e.key(s.ID, "stock")
	}
	vals, err := e.rdb.MGet(ctx, keys...).Result()
	if err != nil {
		return
	}
	for i, v := range vals {
		if str, ok := v.(string); ok {
			if n, err := strconv.Atoi(str); err == nil {
				sales[i].LiveStock = max(n, 0)
			}
		}
	}
}

// Cancel 下线活动：已生成的订单不受影响，新的抢购请求立即被 Lua 拒绝。
func (e *Engine) Cancel(ctx context.Context, id string) error {
	res, err := e.db.ExecContext(ctx, "UPDATE flash_sales SET status='cancelled',updated_at=? WHERE id=? AND status='scheduled'", nowMs(), id)
	if err != nil {
		return err
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return ErrNotFound
	}
	return e.rdb.HSet(ctx, e.key(id, "info"), "status", "off").Err()
}

func parseTicket(saleID, raw string) Ticket {
	t := Ticket{SaleID: saleID, State: "none"}
	oid, state, ok := strings.Cut(raw, "|")
	if !ok {
		return t
	}
	t.OrderID = oid
	switch state {
	case "q":
		t.State = "queued"
	case "ok":
		t.State = "success"
	case "fail":
		t.State = "failed"
	case "cancel":
		t.State = "cancelled"
	}
	return t
}

// Attempt 是抢购入口：只访问 Redis，成功时返回 queued 状态的资格与预生成订单号。
func (e *Engine) Attempt(ctx context.Context, sale Sale, userID string) (Ticket, error) {
	if sale.SellerID == userID {
		return Ticket{}, ErrOwnSale
	}
	// 本地售罄标记：售罄后的海量请求直接在本进程拒绝，不再打 Redis。
	// 标记只保留 500ms，因为订单取消/对账可能把库存补回来。
	if until, ok := e.soldOut.Load(sale.ID); ok && until.(int64) > nowMs() {
		return Ticket{}, ErrSoldOut
	}
	oid, err := e.newID()
	if err != nil {
		return Ticket{}, err
	}
	res, err := attemptScript.Run(ctx, e.rdb, []string{e.key(sale.ID, "info"), e.key(sale.ID, "stock"), e.key(sale.ID, "tickets"), e.stream}, userID, oid, sale.ID, e.retention(sale)).Slice()
	if err != nil {
		// Redis 不可用时拒绝抢购，而不是降级直连数据库：秒杀流量会瞬间压垮 DB。
		slog.Warn("flash attempt script failed", "sale", sale.ID, "error", err)
		return Ticket{}, ErrUnavailable
	}
	if len(res) != 2 {
		return Ticket{}, ErrUnavailable
	}
	code, _ := res[0].(int64)
	raw, _ := res[1].(string)
	switch code {
	case 0, 1:
		t := parseTicket(sale.ID, raw)
		if t.State == "success" || t.State == "cancelled" {
			t.OrderStatus = e.orderStatus(ctx, t.OrderID, userID)
		}
		return t, nil
	case -1:
		return Ticket{}, ErrUnavailable
	case -2:
		return Ticket{}, ErrNotStarted
	case -3:
		return Ticket{}, ErrEnded
	case -4:
		e.soldOut.Store(sale.ID, nowMs()+500)
		return Ticket{}, ErrSoldOut
	case -5:
		return Ticket{}, ErrOffline
	}
	return Ticket{}, fmt.Errorf("seckill: unexpected script result %d", code)
}

func (e *Engine) orderStatus(ctx context.Context, orderID, userID string) string {
	var status string
	if err := e.db.QueryRowContext(ctx, "SELECT status FROM orders WHERE id=? AND buyer_id=?", orderID, userID).Scan(&status); err != nil {
		return ""
	}
	return status
}

// Result 查询用户的抢购结果，供前端轮询（也会通过 WebSocket 主动推送）。
func (e *Engine) Result(ctx context.Context, saleID, userID string) (Ticket, error) {
	raw, err := e.rdb.HGet(ctx, e.key(saleID, "tickets"), userID).Result()
	if errors.Is(err, redis.Nil) {
		// Redis 资格信息过期后以数据库为准。
		var oid, status string
		err = e.db.QueryRowContext(ctx, "SELECT id,status FROM orders WHERE flash_sale_id=? AND buyer_id=?", saleID, userID).Scan(&oid, &status)
		if errors.Is(err, sql.ErrNoRows) {
			return Ticket{SaleID: saleID, State: "none"}, nil
		}
		if err != nil {
			return Ticket{}, err
		}
		return Ticket{SaleID: saleID, OrderID: oid, State: "success", OrderStatus: status}, nil
	}
	if err != nil {
		return Ticket{}, ErrUnavailable
	}
	t := parseTicket(saleID, raw)
	if t.State == "queued" || t.State == "success" || t.State == "cancelled" {
		if st := e.orderStatus(ctx, t.OrderID, userID); st != "" {
			t.OrderStatus = st
			if t.State == "queued" {
				t.State = "success"
			}
		}
	}
	if t.State == "failed" {
		t.Reason = "库存已被抢完或系统繁忙，请重试"
	}
	return t, nil
}

// ReleaseTx 在业务层取消/超时关闭抢购订单的事务中调用，归还数据库库存。
func ReleaseTx(tx *sql.Tx, saleID string) error {
	_, err := tx.Exec("UPDATE flash_sales SET stock=stock+1,updated_at=? WHERE id=? AND stock<total_stock", nowMs(), saleID)
	return err
}

// ReleaseRedis 在取消事务提交后归还 Redis 库存。失败只告警：对账任务会把 Redis 库存修正回来。
func (e *Engine) ReleaseRedis(ctx context.Context, saleID, userID, orderID string) {
	err := transitionScript.Run(ctx, e.rdb, []string{e.key(saleID, "tickets"), e.key(saleID, "stock")}, userID, orderID, "cancel", 1, "q", "ok").Err()
	if err != nil {
		slog.Warn("flash redis release failed, reconcile will fix", "sale", saleID, "order", orderID, "error", err)
	}
	e.soldOut.Delete(saleID)
}

type message struct {
	ID      string
	SaleID  string
	UserID  string
	OrderID string
}

func parseMessage(m redis.XMessage) (message, bool) {
	get := func(k string) string { v, _ := m.Values[k].(string); return v }
	msg := message{ID: m.ID, SaleID: get("sale"), UserID: get("uid"), OrderID: get("oid")}
	return msg, msg.SaleID != "" && msg.UserID != "" && msg.OrderID != ""
}

// errRejected 表示业务上不能成单（DB 库存已无、活动下线、重复购买），消息应 ACK 并补偿。
type errRejected struct{ reason string }

func (e errRejected) Error() string { return "rejected: " + e.reason }

var errAlreadyPersisted = errors.New("order already persisted")

// persist 在一个事务里扣减 DB 库存并创建订单。订单主键是预生成的订单号，重复消费时主键冲突，天然幂等。
func (e *Engine) persist(ctx context.Context, m message) (CreatedOrder, error) {
	var out CreatedOrder
	err := mysqlx.WithTx(ctx, e.db, func(tx *sql.Tx) error {
		t := nowMs()
		res, err := tx.ExecContext(ctx, "UPDATE flash_sales SET stock=stock-1,updated_at=? WHERE id=? AND stock>0 AND status='scheduled'", t, m.SaleID)
		if err != nil {
			return err
		}
		if n, _ := res.RowsAffected(); n != 1 {
			return errRejected{"sold_out"}
		}
		var seller, title, image, campus string
		var price int64
		if err = tx.QueryRowContext(ctx, "SELECT seller_id,title,image,campus,price_ct FROM flash_sales WHERE id=?", m.SaleID).Scan(&seller, &title, &image, &campus, &price); err != nil {
			return err
		}
		if seller == m.UserID {
			return errRejected{"own_sale"}
		}
		expires := t + e.cfg.PaymentWindow.Milliseconds()
		_, err = tx.ExecContext(ctx, "INSERT INTO orders (id,buyer_id,seller_id,product_id,flash_sale_id,title,image,amount_ct,campus,status,idem,created_at,updated_at,expires_at) VALUES (?,?,?,NULL,?,?,?,?,?,'pending',?,?,?,?)",
			m.OrderID, m.UserID, seller, m.SaleID, title, image, price, campus, "flash:"+m.SaleID, t, t, expires)
		if mysqlx.IsDuplicate(err) {
			if mysqlx.DuplicateKeyName(err) == "PRIMARY" {
				return errAlreadyPersisted
			}
			return errRejected{"already_bought"}
		}
		if err != nil {
			return err
		}
		if _, err = tx.ExecContext(ctx, "INSERT INTO order_events (order_id,status,actor,created_at) VALUES (?,?,?,?)", m.OrderID, "pending", m.UserID, t); err != nil {
			return err
		}
		out = CreatedOrder{OrderID: m.OrderID, SaleID: m.SaleID, BuyerID: m.UserID, SellerID: seller, ExpiresAt: expires}
		return nil
	})
	if errors.Is(err, errAlreadyPersisted) {
		var seller string
		var expires int64
		if qerr := e.db.QueryRowContext(ctx, "SELECT seller_id,expires_at FROM orders WHERE id=?", m.OrderID).Scan(&seller, &expires); qerr != nil {
			return out, qerr
		}
		return CreatedOrder{OrderID: m.OrderID, SaleID: m.SaleID, BuyerID: m.UserID, SellerID: seller, ExpiresAt: expires}, nil
	}
	return out, err
}

// handle 处理一条消息，返回 true 表示可以 ACK。
func (e *Engine) handle(ctx context.Context, m message) bool {
	created, err := e.persist(ctx, m)
	var rej errRejected
	switch {
	case err == nil:
		_ = transitionScript.Run(ctx, e.rdb, []string{e.key(m.SaleID, "tickets"), e.key(m.SaleID, "stock")}, m.UserID, m.OrderID, "ok", 0, "q").Err()
		if e.hooks.OrderCreated != nil {
			e.hooks.OrderCreated(ctx, created)
		}
		return true
	case errors.As(err, &rej):
		e.compensate(ctx, m, rej.reason)
		return true
	default:
		slog.Warn("flash order persist failed, will retry", "order", m.OrderID, "error", err)
		return false
	}
}

// compensate 标记资格失败并归还 Redis 库存。DB 库存不足导致的失败不归还（说明 Redis 多放了，归还会更糟）。
func (e *Engine) compensate(ctx context.Context, m message, reason string) {
	give := 1
	if reason == "sold_out" {
		give = 0
	}
	if err := transitionScript.Run(ctx, e.rdb, []string{e.key(m.SaleID, "tickets"), e.key(m.SaleID, "stock")}, m.UserID, m.OrderID, "fail", give, "q").Err(); err != nil {
		slog.Warn("flash compensation failed", "order", m.OrderID, "error", err)
	}
	slog.Info("flash order rejected", "sale", m.SaleID, "order", m.OrderID, "reason", reason)
	if e.hooks.OrderFailed != nil {
		e.hooks.OrderFailed(ctx, m.SaleID, m.UserID, m.OrderID, reason)
	}
}

// Run 启动落库 worker、超时消息回收与流修剪，直到 ctx 结束。
func (e *Engine) Run(ctx context.Context) error {
	if err := e.ensureGroup(ctx); err != nil {
		return err
	}
	var wg sync.WaitGroup
	for i := 0; i < e.cfg.Workers; i++ {
		wg.Add(1)
		go func(name string) {
			defer wg.Done()
			e.consume(ctx, name)
		}(fmt.Sprintf("%s-%d", e.cfg.Consumer, i))
	}
	wg.Add(1)
	go func() {
		defer wg.Done()
		ticker := time.NewTicker(5 * time.Second)
		defer ticker.Stop()
		for {
			select {
			case <-ctx.Done():
				return
			case <-ticker.C:
				if err := e.reclaim(ctx); err != nil && ctx.Err() == nil {
					slog.Warn("flash reclaim failed", "error", err)
				}
			}
		}
	}()
	wg.Wait()
	return nil
}

func (e *Engine) consume(ctx context.Context, name string) {
	for ctx.Err() == nil {
		streams, err := e.rdb.XReadGroup(ctx, &redis.XReadGroupArgs{Group: e.group, Consumer: name, Streams: []string{e.stream, ">"}, Count: 16, Block: 2 * time.Second}).Result()
		if errors.Is(err, redis.Nil) {
			continue
		}
		if err != nil {
			if ctx.Err() != nil {
				return
			}
			if strings.Contains(err.Error(), "NOGROUP") {
				_ = e.ensureGroup(ctx)
			}
			slog.Warn("flash stream read failed", "error", err)
			time.Sleep(time.Second)
			continue
		}
		for _, s := range streams {
			for _, xm := range s.Messages {
				e.process(ctx, xm)
			}
		}
	}
}

func (e *Engine) process(ctx context.Context, xm redis.XMessage) {
	m, ok := parseMessage(xm)
	if !ok {
		slog.Error("flash stream malformed message", "id", xm.ID)
		e.ack(ctx, xm.ID)
		return
	}
	// 落库使用独立超时：服务关闭时让正在处理的消息尽量完成，未完成的留在 PEL 等待重投。
	pctx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 10*time.Second)
	defer cancel()
	if e.handle(pctx, m) {
		e.ack(pctx, xm.ID)
	}
}

func (e *Engine) ack(ctx context.Context, id string) {
	if err := e.rdb.XAck(ctx, e.stream, e.group, id).Err(); err != nil {
		slog.Warn("flash xack failed", "id", id, "error", err)
	}
}

// reclaim 处理长时间未 ACK 的消息（消费者崩溃、DB 故障）：投递次数未超限则 XCLAIM 后重试，
// 超限则写入死信流、补偿资格并 ACK。随后修剪已全部确认的流数据。
func (e *Engine) reclaim(ctx context.Context) error {
	pending, err := e.rdb.XPendingExt(ctx, &redis.XPendingExtArgs{Stream: e.stream, Group: e.group, Idle: e.cfg.ClaimIdle, Start: "-", End: "+", Count: 50}).Result()
	if err != nil {
		if strings.Contains(err.Error(), "NOGROUP") {
			return e.ensureGroup(ctx)
		}
		return err
	}
	consumer := e.cfg.Consumer + "-reclaim"
	for _, p := range pending {
		msgs, err := e.rdb.XClaim(ctx, &redis.XClaimArgs{Stream: e.stream, Group: e.group, Consumer: consumer, MinIdle: e.cfg.ClaimIdle, Messages: []string{p.ID}}).Result()
		if err != nil {
			return err
		}
		for _, xm := range msgs {
			if p.RetryCount >= e.cfg.MaxDeliveries {
				e.deadLetter(ctx, xm, p.RetryCount)
				continue
			}
			e.process(ctx, xm)
		}
	}
	return e.trim(ctx)
}

func (e *Engine) deadLetter(ctx context.Context, xm redis.XMessage, deliveries int64) {
	values := map[string]any{"deliveries": deliveries, "source_id": xm.ID}
	for k, v := range xm.Values {
		values[k] = v
	}
	if err := e.rdb.XAdd(ctx, &redis.XAddArgs{Stream: e.dlq, MaxLen: 10000, Approx: true, Values: values}).Err(); err != nil {
		slog.Error("flash dead letter write failed", "id", xm.ID, "error", err)
		return
	}
	if m, ok := parseMessage(xm); ok {
		e.compensate(ctx, m, "system")
	}
	slog.Error("flash order moved to dead letter", "id", xm.ID, "deliveries", deliveries)
	e.ack(ctx, xm.ID)
}

// trim 只删除"所有消息都已被确认"的前缀：不能用 XADD MAXLEN 截断，否则积压时会丢掉未消费的订单。
func (e *Engine) trim(ctx context.Context) error {
	groups, err := e.rdb.XInfoGroups(ctx, e.stream).Result()
	if err != nil {
		return err
	}
	var minID string
	for _, g := range groups {
		if g.Name == e.group {
			minID = g.LastDeliveredID
		}
	}
	if minID == "" || minID == "0-0" {
		return nil
	}
	summary, err := e.rdb.XPending(ctx, e.stream, e.group).Result()
	if err != nil {
		return err
	}
	if summary.Count > 0 && lessID(summary.Lower, minID) {
		minID = summary.Lower
	}
	return e.rdb.XTrimMinIDApprox(ctx, e.stream, minID, 0).Err()
}

func lessID(a, b string) bool {
	pa, pb := strings.SplitN(a, "-", 2), strings.SplitN(b, "-", 2)
	ta, _ := strconv.ParseInt(pa[0], 10, 64)
	tb, _ := strconv.ParseInt(pb[0], 10, 64)
	if ta != tb {
		return ta < tb
	}
	var sa, sb int64
	if len(pa) == 2 {
		sa, _ = strconv.ParseInt(pa[1], 10, 64)
	}
	if len(pb) == 2 {
		sb, _ = strconv.ParseInt(pb[1], 10, 64)
	}
	return sa < sb
}

// StreamStats 返回抢购消息流的积压情况，便于观察异步落库是否跟得上。
func (e *Engine) StreamStats(ctx context.Context) map[string]any {
	out := map[string]any{}
	if n, err := e.rdb.XLen(ctx, e.stream).Result(); err == nil {
		out["length"] = n
	}
	if n, err := e.rdb.XLen(ctx, e.dlq).Result(); err == nil {
		out["dead_letters"] = n
	}
	if p, err := e.rdb.XPending(ctx, e.stream, e.group).Result(); err == nil {
		out["pending"] = p.Count
	}
	if groups, err := e.rdb.XInfoGroups(ctx, e.stream).Result(); err == nil {
		for _, g := range groups {
			if g.Name == e.group {
				out["lag"] = g.Lag
				out["consumers"] = g.Consumers
			}
		}
	}
	return out
}

// Reconcile 对账：Redis 库存应等于 DB 库存减去仍在排队落库的资格数。
// 为避免与正在落库的消息竞争产生误判，同一偏差需连续两轮观察到才修正。
func (e *Engine) Reconcile(ctx context.Context) error {
	rows, err := e.db.QueryContext(ctx, "SELECT id,stock,start_at,end_at,status FROM flash_sales WHERE end_at>? AND status='scheduled'", nowMs())
	if err != nil {
		return err
	}
	type row struct {
		id         string
		stock      int
		start, end int64
		status     string
	}
	list := []row{}
	for rows.Next() {
		var r row
		if err = rows.Scan(&r.id, &r.stock, &r.start, &r.end, &r.status); err != nil {
			rows.Close()
			return err
		}
		list = append(list, r)
	}
	rows.Close()
	if err = rows.Err(); err != nil {
		return err
	}
	e.mu.Lock()
	defer e.mu.Unlock()
	seen := map[string]bool{}
	for _, r := range list {
		seen[r.id] = true
		vals, err := inspectScript.Run(ctx, e.rdb, []string{e.key(r.id, "stock"), e.key(r.id, "tickets")}).Int64Slice()
		if err != nil {
			return err
		}
		redisStock, queued := int(vals[0]), int(vals[1])
		if redisStock < 0 {
			slog.Warn("flash sale missing in redis, re-preheating", "sale", r.id)
			if err = e.Preheat(ctx, Sale{ID: r.id, Stock: r.stock, StartAt: r.start, EndAt: r.end, Status: r.status}, false); err != nil {
				return err
			}
			continue
		}
		expected := max(r.stock-queued, 0)
		if redisStock == expected {
			delete(e.mismatches, r.id)
			continue
		}
		obs := [2]int{redisStock, expected}
		if prev, ok := e.mismatches[r.id]; ok && prev == obs {
			fixed, err := casStockScript.Run(ctx, e.rdb, []string{e.key(r.id, "stock")}, redisStock, expected).Int()
			if err != nil {
				return err
			}
			slog.Warn("flash stock reconciled", "sale", r.id, "redis", redisStock, "expected", expected, "applied", fixed == 1)
			delete(e.mismatches, r.id)
			e.soldOut.Delete(r.id)
			continue
		}
		e.mismatches[r.id] = obs
	}
	for id := range e.mismatches {
		if !seen[id] {
			delete(e.mismatches, id)
		}
	}
	return nil
}
