// Command flashbench 是限时抢购的压测与一致性校验工具（只用于本地/测试环境）。
//
// 它直接在数据库里批量创建压测用户和会话、通过 seckill.Engine 创建并预热一场活动，
// 然后用 N 个并发连接模拟 M 个用户同时抢购，统计 QPS、延迟分位与状态码分布，
// 最后等待异步落单完成，校验"订单数 == 抢到人数 ≤ 库存、DB/Redis 库存一致、无一人多单"。
//
//	go run ./cmd/flashbench -users 2000 -stock 100 -c 200
//
// 服务端需设置 TRUSTED_PROXY_CIDRS=127.0.0.1/32：工具为每个虚拟用户带上不同的 X-Real-IP，
// 模拟来自不同客户端的流量（否则单 IP 限流会先生效）。
package main

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"flag"
	"fmt"
	"io"
	"log"
	"net/http"
	"os"
	"sort"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"qingfly/edu-mall/internal/infra/mysqlx"
	"qingfly/edu-mall/internal/infra/redisx"
	"qingfly/edu-mall/internal/pkg/snowflake"
	"qingfly/edu-mall/internal/seckill"
)

func main() {
	addr := flag.String("addr", "http://127.0.0.1:8081", "server base URL")
	dsn := flag.String("dsn", envOr("DATABASE_DSN", "campus:campus@tcp(127.0.0.1:3306)/campus"), "MySQL DSN of the server database")
	redisAddr := flag.String("redis", envOr("REDIS_ADDR", "127.0.0.1:6379"), "Redis address")
	prefix := flag.String("prefix", envOr("REDIS_PREFIX", "campus"), "Redis key prefix used by the server")
	users := flag.Int("users", 2000, "virtual users (each attempts once)")
	stock := flag.Int("stock", 100, "flash sale stock")
	conc := flag.Int("c", 200, "concurrent connections")
	flag.Parse()
	if os.Getenv("APP_ENV") == "production" {
		log.Fatal("flashbench must not run against production")
	}
	ctx := context.Background()
	db, err := mysqlx.Open(ctx, mysqlx.Options{DSN: *dsn})
	if err != nil {
		log.Fatal(err)
	}
	defer db.Close()
	rdb, err := redisx.Open(ctx, redisx.Options{Addr: *redisAddr})
	if err != nil {
		log.Fatal(err)
	}
	defer rdb.Close()

	run := strconv.FormatInt(time.Now().Unix(), 36)
	log.Printf("preparing %d users ...", *users)
	tokens := make([]string, *users)
	organizer := "bench_" + run + "_org"
	exec := func(q string, args ...any) {
		if _, err := db.ExecContext(ctx, q, args...); err != nil {
			log.Fatal(err)
		}
	}
	exec("INSERT INTO users (id,email,name,password,created_at) VALUES (?,?,?,?,?)", organizer, organizer+"@bench.invalid", "压测主办方", "disabled", time.Now().UnixMilli())
	const batch = 500
	for i := 0; i < *users; i += batch {
		var u, s strings.Builder
		ua, sa := []any{}, []any{}
		for j := i; j < min(i+batch, *users); j++ {
			id := fmt.Sprintf("bench_%s_%d", run, j)
			tokens[j] = randHex(16)
			h := sha256.Sum256([]byte(tokens[j]))
			if j > i {
				u.WriteString(",")
				s.WriteString(",")
			}
			u.WriteString("(?,?,?,?,?)")
			s.WriteString("(?,?,?)")
			ua = append(ua, id, id+"@bench.invalid", "压测用户", "disabled", time.Now().UnixMilli())
			sa = append(sa, hex.EncodeToString(h[:]), id, time.Now().Add(time.Hour).UnixMilli())
		}
		exec("INSERT INTO users (id,email,name,password,created_at) VALUES "+u.String(), ua...)
		exec("INSERT INTO sessions (hash,user_id,expires_at) VALUES "+s.String(), sa...)
	}
	node, _ := snowflake.NewNode(1023)
	engine := seckill.New(db, rdb, seckill.Config{Prefix: *prefix}, func() (string, error) {
		n, err := node.Next()
		return "CM" + strconv.FormatInt(n, 10), err
	}, seckill.Hooks{})
	start := time.Now().Add(2 * time.Second)
	sale, err := engine.Create(ctx, seckill.Sale{ID: "fs_bench_" + run, SellerID: organizer, Title: "压测活动", Description: "flashbench", Image: "https://example.test/bench.jpg", Campus: "东区", OriginalPriceCt: 1000, PriceCt: 100, TotalStock: *stock, StartAt: start.UnixMilli(), EndAt: start.Add(time.Hour).UnixMilli()})
	if err != nil {
		log.Fatal(err)
	}
	log.Printf("sale %s stock=%d starts in 2s, firing %d attempts with %d connections", sale.ID, *stock, *users, *conc)
	time.Sleep(time.Until(start) + 50*time.Millisecond)

	client := &http.Client{Timeout: 10 * time.Second, Transport: &http.Transport{MaxIdleConns: *conc, MaxIdleConnsPerHost: *conc, MaxConnsPerHost: *conc}}
	latencies := make([]time.Duration, *users)
	var statusMu sync.Mutex
	statuses := map[int]int{}
	var next atomic.Int64
	var wg sync.WaitGroup
	t0 := time.Now()
	for w := 0; w < *conc; w++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for {
				i := int(next.Add(1) - 1)
				if i >= *users {
					return
				}
				req, _ := http.NewRequest("POST", *addr+"/api/v1/flash-sales/"+sale.ID+"/attempts", strings.NewReader("{}"))
				req.Header.Set("Content-Type", "application/json")
				req.Header.Set("Cookie", "campus_session="+tokens[i])
				req.Header.Set("X-Real-IP", fmt.Sprintf("10.%d.%d.%d", i>>16&255, i>>8&255, i&255))
				begin := time.Now()
				resp, err := client.Do(req)
				code := -1
				if err == nil {
					io.Copy(io.Discard, resp.Body)
					resp.Body.Close()
					code = resp.StatusCode
				}
				latencies[i] = time.Since(begin)
				statusMu.Lock()
				statuses[code]++
				statusMu.Unlock()
			}
		}()
	}
	wg.Wait()
	elapsed := time.Since(t0)
	sort.Slice(latencies, func(a, b int) bool { return latencies[a] < latencies[b] })
	pct := func(p float64) time.Duration { return latencies[min(len(latencies)-1, int(float64(len(latencies))*p))] }
	fmt.Printf("\n=== flash attempt load ===\nrequests %d in %v  ->  %.0f req/s\nlatency  p50=%v p90=%v p99=%v max=%v\nstatus   %v  (202=抢到排队中 409=售罄 429=限流削峰)\n",
		*users, elapsed.Round(time.Millisecond), float64(*users)/elapsed.Seconds(), pct(.5), pct(.9), pct(.99), latencies[len(latencies)-1], statuses)

	won := statuses[202]
	deadline := time.Now().Add(60 * time.Second)
	var orders int
	for time.Now().Before(deadline) {
		_ = db.QueryRowContext(ctx, "SELECT COUNT(*) FROM orders WHERE flash_sale_id=?", sale.ID).Scan(&orders)
		if orders >= won {
			break
		}
		time.Sleep(100 * time.Millisecond)
	}
	persistLag := time.Since(t0)
	var dbStock, multi int
	_ = db.QueryRowContext(ctx, "SELECT stock FROM flash_sales WHERE id=?", sale.ID).Scan(&dbStock)
	_ = db.QueryRowContext(ctx, "SELECT COUNT(*) FROM (SELECT buyer_id FROM orders WHERE flash_sale_id=? GROUP BY buyer_id HAVING COUNT(*)>1) t", sale.ID).Scan(&multi)
	live, _ := engine.Get(ctx, sale.ID)
	ok := orders == won && orders <= *stock && dbStock == *stock-orders && live.LiveStock == dbStock && multi == 0
	fmt.Printf("\n=== consistency ===\nwinners %d  orders %d  stock %d  db_stock_left %d  redis_stock_left %d  multi_order_users %d\nall orders persisted after %v\nresult: %s\n",
		won, orders, *stock, dbStock, live.LiveStock, multi, persistLag.Round(time.Millisecond), map[bool]string{true: "PASS (no oversell, one order per user)", false: "FAIL"}[ok])
	if !ok {
		os.Exit(1)
	}
}

func randHex(n int) string {
	b := make([]byte, n)
	_, _ = rand.Read(b)
	return hex.EncodeToString(b)
}

func envOr(k, d string) string {
	if v := os.Getenv(k); v != "" {
		return v
	}
	return d
}
