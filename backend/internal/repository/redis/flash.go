package redisrepo

import (
	"context"
	"errors"
	"log/slog"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/redis/go-redis/v9"

	"qingfly/edu-mall/internal/domain"
	"qingfly/edu-mall/internal/port"
)

// FlashStore 是抢购的 Redis 闸门与消息流适配器：
//   - info/stock/tickets 由 Lua 原子维护；
//   - stream/dlq/group 承担异步落库与重投；
//   - 本地售罄标记在本进程内拒绝海量重复请求。
type FlashStore struct {
	rdb     redis.UniversalClient
	cfg     FlashConfig
	soldOut sync.Map
	stream  string
	dlq     string
	group   string
}

type FlashConfig struct {
	Prefix        string
	Consumer      string
	PaymentWindow time.Duration
	ClaimIdle     time.Duration
	MaxDeliveries int64
}

func NewFlashStore(rdb redis.UniversalClient, prefix, consumer string, paymentWindow time.Duration) *FlashStore {
	if paymentWindow <= 0 {
		paymentWindow = 15 * time.Minute
	}
	if consumer == "" {
		consumer = "worker"
	}
	return &FlashStore{
		rdb:    rdb,
		cfg:    FlashConfig{Prefix: prefix, Consumer: consumer, PaymentWindow: paymentWindow, ClaimIdle: 30 * time.Second, MaxDeliveries: 5},
		stream: prefix + ":flash:orders",
		dlq:    prefix + ":flash:orders:dlq",
		group:  "order-writer",
	}
}

func (f *FlashStore) key(saleID, part string) string {
	return f.cfg.Prefix + ":flash:" + saleID + ":" + part
}

func (f *FlashStore) retention(sale domain.FlashSale) int64 {
	return sale.EndAt + f.cfg.PaymentWindow.Milliseconds() + 24*time.Hour.Milliseconds()
}

func (f *FlashStore) Preheat(ctx context.Context, sale domain.FlashSale, force bool) error {
	status := "on"
	if sale.Status != "scheduled" {
		status = "off"
	}
	flag := "0"
	if force {
		flag = "1"
	}
	return preheatScript.Run(ctx, f.rdb, []string{f.key(sale.ID, "info"), f.key(sale.ID, "stock")}, sale.StartAt, sale.EndAt, status, sale.Stock, flag, f.retention(sale)).Err()
}

func (f *FlashStore) FillLiveStock(ctx context.Context, sales ...*domain.FlashSale) {
	if len(sales) == 0 {
		return
	}
	keys := make([]string, len(sales))
	for i, s := range sales {
		keys[i] = f.key(s.ID, "stock")
	}
	vals, err := f.rdb.MGet(ctx, keys...).Result()
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

func (f *FlashStore) Attempt(ctx context.Context, sale domain.FlashSale, userID, orderID string) (domain.Ticket, error) {
	if until, ok := f.soldOut.Load(sale.ID); ok && until.(int64) > domain.NowMillis() {
		if raw, err := f.rdb.HGet(ctx, f.key(sale.ID, "tickets"), userID).Result(); err == nil {
			if t := parseTicket(sale.ID, raw); t.State != "failed" && t.State != "none" {
				return t, nil
			}
		}
		return domain.Ticket{}, domain.Conflict("已抢光")
	}
	res, err := attemptScript.Run(ctx, f.rdb, []string{f.key(sale.ID, "info"), f.key(sale.ID, "stock"), f.key(sale.ID, "tickets"), f.stream}, userID, orderID, sale.ID, f.retention(sale)).Slice()
	if err != nil {
		slog.Warn("flash attempt script failed", "sale", sale.ID, "error", err)
		return domain.Ticket{}, domain.Retryable(domain.KindUnavailable, "抢购通道繁忙，请稍后重试", 1)
	}
	if len(res) != 2 {
		return domain.Ticket{}, domain.Retryable(domain.KindUnavailable, "抢购通道繁忙，请稍后重试", 1)
	}
	code, _ := res[0].(int64)
	raw, _ := res[1].(string)
	switch code {
	case 0, 1:
		return parseTicket(sale.ID, raw), nil
	case -1:
		return domain.Ticket{}, domain.Retryable(domain.KindUnavailable, "抢购通道繁忙，请稍后重试", 1)
	case -2:
		return domain.Ticket{}, domain.Conflict("活动尚未开始")
	case -3:
		return domain.Ticket{}, domain.Gone("活动已结束")
	case -4:
		f.soldOut.Store(sale.ID, domain.NowMillis()+500)
		return domain.Ticket{}, domain.Conflict("已抢光")
	case -5:
		return domain.Ticket{}, domain.Gone("活动已下线")
	}
	return domain.Ticket{}, domain.Retryable(domain.KindUnavailable, "抢购通道繁忙，请稍后重试", 1)
}

func (f *FlashStore) SetStatus(ctx context.Context, saleID, status string) error {
	return f.rdb.HSet(ctx, f.key(saleID, "info"), "status", status).Err()
}

func (f *FlashStore) Ticket(ctx context.Context, saleID, userID string) (domain.Ticket, bool, error) {
	raw, err := f.rdb.HGet(ctx, f.key(saleID, "tickets"), userID).Result()
	if errors.Is(err, redis.Nil) {
		return domain.Ticket{SaleID: saleID, State: "none"}, false, nil
	}
	if err != nil {
		return domain.Ticket{}, false, domain.Retryable(domain.KindUnavailable, "查询繁忙，请稍后重试", 1)
	}
	return parseTicket(saleID, raw), true, nil
}

func (f *FlashStore) Transition(ctx context.Context, saleID, userID, orderID, state string, give int, fromState string) error {
	return transitionScript.Run(ctx, f.rdb, []string{f.key(saleID, "tickets"), f.key(saleID, "stock")}, userID, orderID, state, give, fromState).Err()
}

func (f *FlashStore) Release(ctx context.Context, saleID, userID, orderID string) {
	if err := f.Transition(ctx, saleID, userID, orderID, "cancel", 1, "q"); err != nil {
		slog.Warn("flash redis release failed, reconcile will fix", "sale", saleID, "order", orderID, "error", err)
	}
	if err := f.Transition(ctx, saleID, userID, orderID, "cancel", 1, "ok"); err != nil {
		slog.Warn("flash redis release failed, reconcile will fix", "sale", saleID, "order", orderID, "error", err)
	}
	f.soldOut.Delete(saleID)
}

func (f *FlashStore) Inspect(ctx context.Context, saleID string) (int, int, error) {
	vals, err := inspectScript.Run(ctx, f.rdb, []string{f.key(saleID, "stock"), f.key(saleID, "tickets")}).Int64Slice()
	if err != nil {
		return 0, 0, err
	}
	if len(vals) != 2 {
		return -1, 0, nil
	}
	return int(vals[0]), int(vals[1]), nil
}

func (f *FlashStore) CASStock(ctx context.Context, saleID string, oldStock, newStock int) (bool, error) {
	n, err := casStockScript.Run(ctx, f.rdb, []string{f.key(saleID, "stock")}, oldStock, newStock).Int()
	return n == 1, err
}

func (f *FlashStore) ClearSoldOut(saleID string) { f.soldOut.Delete(saleID) }

func (f *FlashStore) EnsureGroup(ctx context.Context) error {
	err := f.rdb.XGroupCreateMkStream(ctx, f.stream, f.group, "0").Err()
	if err != nil && !strings.Contains(err.Error(), "BUSYGROUP") {
		return err
	}
	return nil
}

func parseTicket(saleID, raw string) domain.Ticket {
	t := domain.Ticket{SaleID: saleID, State: "none"}
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

func parseMessage(m redis.XMessage) (domain.FlashOrderMessage, bool) {
	get := func(k string) string { v, _ := m.Values[k].(string); return v }
	msg := domain.FlashOrderMessage{StreamID: m.ID, SaleID: get("sale"), UserID: get("uid"), OrderID: get("oid")}
	return msg, msg.SaleID != "" && msg.UserID != "" && msg.OrderID != ""
}

func (f *FlashStore) Read(ctx context.Context, consumer string, count int, block time.Duration) ([]domain.FlashOrderMessage, error) {
	streams, err := f.rdb.XReadGroup(ctx, &redis.XReadGroupArgs{Group: f.group, Consumer: consumer, Streams: []string{f.stream, ">"}, Count: int64(count), Block: block}).Result()
	if errors.Is(err, redis.Nil) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	out := []domain.FlashOrderMessage{}
	for _, s := range streams {
		for _, xm := range s.Messages {
			msg, ok := parseMessage(xm)
			if !ok {
				slog.Error("flash stream malformed message", "id", xm.ID)
				_ = f.rdb.XAck(ctx, f.stream, f.group, xm.ID).Err()
				continue
			}
			out = append(out, msg)
		}
	}
	return out, nil
}

func (f *FlashStore) Pending(ctx context.Context, minIdle time.Duration, count int) ([]port.FlashPending, error) {
	pending, err := f.rdb.XPendingExt(ctx, &redis.XPendingExtArgs{Stream: f.stream, Group: f.group, Idle: minIdle, Start: "-", End: "+", Count: int64(count)}).Result()
	if err != nil {
		return nil, err
	}
	out := make([]port.FlashPending, 0, len(pending))
	for _, p := range pending {
		out = append(out, port.FlashPending{ID: p.ID, RetryCount: p.RetryCount})
	}
	return out, nil
}

func (f *FlashStore) Claim(ctx context.Context, consumer string, minIdle time.Duration, messageID string) ([]domain.FlashOrderMessage, error) {
	msgs, err := f.rdb.XClaim(ctx, &redis.XClaimArgs{Stream: f.stream, Group: f.group, Consumer: consumer, MinIdle: minIdle, Messages: []string{messageID}}).Result()
	if err != nil {
		return nil, err
	}
	out := []domain.FlashOrderMessage{}
	for _, xm := range msgs {
		if msg, ok := parseMessage(xm); ok {
			out = append(out, msg)
		}
	}
	return out, nil
}

func (f *FlashStore) Ack(ctx context.Context, messageID string) error {
	return f.rdb.XAck(ctx, f.stream, f.group, messageID).Err()
}

func (f *FlashStore) DeadLetter(ctx context.Context, msg domain.FlashOrderMessage, deliveries int64) error {
	values := map[string]any{"deliveries": deliveries, "source_id": msg.StreamID, "sale": msg.SaleID, "uid": msg.UserID, "oid": msg.OrderID}
	return f.rdb.XAdd(ctx, &redis.XAddArgs{Stream: f.dlq, MaxLen: 10000, Approx: true, Values: values}).Err()
}

func (f *FlashStore) Trim(ctx context.Context) error {
	groups, err := f.rdb.XInfoGroups(ctx, f.stream).Result()
	if err != nil {
		return err
	}
	var minID string
	for _, g := range groups {
		if g.Name == f.group {
			minID = g.LastDeliveredID
		}
	}
	if minID == "" || minID == "0-0" {
		return nil
	}
	summary, err := f.rdb.XPending(ctx, f.stream, f.group).Result()
	if err != nil {
		return err
	}
	if summary.Count > 0 && lessID(summary.Lower, minID) {
		minID = summary.Lower
	}
	return f.rdb.XTrimMinIDApprox(ctx, f.stream, minID, 0).Err()
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

func (f *FlashStore) Stats(ctx context.Context) map[string]any {
	out := map[string]any{}
	if n, err := f.rdb.XLen(ctx, f.stream).Result(); err == nil {
		out["length"] = n
	}
	if n, err := f.rdb.XLen(ctx, f.dlq).Result(); err == nil {
		out["dead_letters"] = n
	}
	if p, err := f.rdb.XPending(ctx, f.stream, f.group).Result(); err == nil {
		out["pending"] = p.Count
	}
	if groups, err := f.rdb.XInfoGroups(ctx, f.stream).Result(); err == nil {
		for _, g := range groups {
			if g.Name == f.group {
				out["lag"] = g.Lag
				out["consumers"] = g.Consumers
			}
		}
	}
	return out
}

// Configure 允许运行环境调整 PEL 回收节奏与最大投递次数；未调用时使用默认值。
func (f *FlashStore) Configure(claimIdle time.Duration, maxDeliveries int64) {
	if claimIdle > 0 {
		f.cfg.ClaimIdle = claimIdle
	}
	if maxDeliveries > 0 {
		f.cfg.MaxDeliveries = maxDeliveries
	}
}

// ClaimIdle / MaxDeliveries 暴露给业务层的回收任务。
func (f *FlashStore) ClaimIdle() time.Duration { return f.cfg.ClaimIdle }
func (f *FlashStore) MaxDeliveries() int64     { return f.cfg.MaxDeliveries }
func (f *FlashStore) Consumer() string         { return f.cfg.Consumer }
