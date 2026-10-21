package delayqueue_test

import (
	"context"
	"errors"
	"fmt"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/redis/go-redis/v9"

	"qingfly/edu-mall/internal/pkg/delayqueue"
	"qingfly/edu-mall/internal/testenv"
)

func run(t *testing.T, q *delayqueue.Queue, h delayqueue.Handler) {
	t.Helper()
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() {
		defer close(done)
		_ = q.Run(ctx, h)
	}()
	t.Cleanup(func() {
		cancel()
		<-done
	})
}

func waitFor(t *testing.T, d time.Duration, cond func() bool) {
	t.Helper()
	deadline := time.Now().Add(d)
	for time.Now().Before(deadline) {
		if cond() {
			return
		}
		time.Sleep(20 * time.Millisecond)
	}
	t.Fatal("condition not met in time")
}

func TestDeliversOnlyWhenDue(t *testing.T) {
	rdb, prefix := testenv.Redis(t)
	q := delayqueue.New(rdb, prefix, "t", delayqueue.Options{Poll: 20 * time.Millisecond})
	ctx := context.Background()
	start := time.Now()
	var got sync.Map
	run(t, q, func(_ context.Context, m string) error {
		got.Store(m, time.Since(start))
		return nil
	})
	if err := q.Add(ctx, "later", start.Add(600*time.Millisecond)); err != nil {
		t.Fatal(err)
	}
	if err := q.Add(ctx, "now", start); err != nil {
		t.Fatal(err)
	}
	waitFor(t, 3*time.Second, func() bool { _, ok := got.Load("later"); return ok })
	if v, _ := got.Load("later"); v.(time.Duration) < 600*time.Millisecond {
		t.Fatalf("delivered early: %v", v)
	}
	if _, ok := got.Load("now"); !ok {
		t.Fatal("due task not delivered")
	}
	ready, inflight, dead, _ := q.Stats(ctx)
	if ready+inflight+dead != 0 {
		t.Fatalf("queue not drained: %d %d %d", ready, inflight, dead)
	}
}

func TestRetryThenDeadLetter(t *testing.T) {
	rdb, prefix := testenv.Redis(t)
	q := delayqueue.New(rdb, prefix, "t", delayqueue.Options{Poll: 20 * time.Millisecond, Backoff: 30 * time.Millisecond, MaxAttempts: 3})
	ctx := context.Background()
	var flaky, poison atomic.Int32
	run(t, q, func(_ context.Context, m string) error {
		if m == "flaky" && flaky.Add(1) < 2 {
			return errors.New("db timeout")
		}
		if m == "poison" {
			poison.Add(1)
			return errors.New("always fails")
		}
		return nil
	})
	_ = q.Add(ctx, "flaky", time.Now())
	_ = q.Add(ctx, "poison", time.Now())
	waitFor(t, 5*time.Second, func() bool {
		_, _, dead, _ := q.Stats(ctx)
		return dead == 1 && flaky.Load() >= 2
	})
	if poison.Load() != 3 {
		t.Fatalf("poison attempts %d, want 3", poison.Load())
	}
	if n, _ := rdb.ZScore(ctx, prefix+":dq:t:dead", "poison").Result(); n == 0 {
		t.Fatal("poison not in dead letter set")
	}
}

// TestCrashedConsumerTaskIsRedelivered：任务被领取后消费者崩溃（未 ACK），可见性超时后重新投递。
func TestCrashedConsumerTaskIsRedelivered(t *testing.T) {
	rdb, prefix := testenv.Redis(t)
	ctx := context.Background()
	// 直接把任务放进 inflight，score 为已过去的可见性截止时间，模拟"领取后进程被 kill"。
	if err := rdb.ZAdd(ctx, prefix+":dq:t:inflight", redis.Z{Score: float64(time.Now().Add(-time.Second).UnixMilli()), Member: "orphan"}).Err(); err != nil {
		t.Fatal(err)
	}
	q := delayqueue.New(rdb, prefix, "t", delayqueue.Options{Poll: 20 * time.Millisecond})
	var got atomic.Bool
	run(t, q, func(_ context.Context, m string) error {
		if m == "orphan" {
			got.Store(true)
		}
		return nil
	})
	waitFor(t, 3*time.Second, got.Load)
}

// TestCompetingConsumersNoDuplicate：两个实例并发消费，每个任务恰好处理一次。
func TestCompetingConsumersNoDuplicate(t *testing.T) {
	rdb, prefix := testenv.Redis(t)
	ctx := context.Background()
	var mu sync.Mutex
	seen := map[string]int{}
	h := func(_ context.Context, m string) error {
		mu.Lock()
		seen[m]++
		mu.Unlock()
		return nil
	}
	for i := 0; i < 2; i++ {
		run(t, delayqueue.New(rdb, prefix, "t", delayqueue.Options{Poll: 10 * time.Millisecond, Batch: 7}), h)
	}
	for i := 0; i < 300; i++ {
		_ = delayqueue.New(rdb, prefix, "t", delayqueue.Options{}).Add(ctx, fmt.Sprint("job-", i), time.Now())
	}
	waitFor(t, 5*time.Second, func() bool {
		mu.Lock()
		defer mu.Unlock()
		return len(seen) == 300
	})
	time.Sleep(100 * time.Millisecond)
	mu.Lock()
	defer mu.Unlock()
	for m, n := range seen {
		if n != 1 {
			t.Fatalf("%s delivered %d times", m, n)
		}
	}
}
