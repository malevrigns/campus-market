package ratelimit_test

import (
	"context"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/redis/go-redis/v9"

	"qingfly/edu-mall/internal/pkg/ratelimit"
	"qingfly/edu-mall/internal/testenv"
)

func TestSlidingWindow(t *testing.T) {
	rdb, prefix := testenv.Redis(t)
	l := ratelimit.New(rdb, prefix)
	ctx := context.Background()
	for i := 0; i < 5; i++ {
		if r := l.Window(ctx, "login:ip", 5, 300*time.Millisecond); !r.Allowed || r.Remaining != 4-i {
			t.Fatalf("request %d: %+v", i, r)
		}
	}
	r := l.Window(ctx, "login:ip", 5, 300*time.Millisecond)
	if r.Allowed || r.RetryAfter <= 0 || r.RetryAfter > 300*time.Millisecond {
		t.Fatalf("6th request must be limited with retry hint: %+v", r)
	}
	time.Sleep(320 * time.Millisecond)
	if r = l.Window(ctx, "login:ip", 5, 300*time.Millisecond); !r.Allowed {
		t.Fatal("window did not slide")
	}
}

// TestWindowIsAtomicUnderConcurrency：Lua 脚本内"计数+写入"原子，100 个并发请求恰好放行 limit 个。
func TestWindowIsAtomicUnderConcurrency(t *testing.T) {
	rdb, prefix := testenv.Redis(t)
	l := ratelimit.New(rdb, prefix)
	var allowed atomic.Int32
	var wg sync.WaitGroup
	for i := 0; i < 100; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			if l.Window(context.Background(), "burst", 20, time.Minute).Allowed {
				allowed.Add(1)
			}
		}()
	}
	wg.Wait()
	if allowed.Load() != 20 {
		t.Fatalf("allowed %d, want 20", allowed.Load())
	}
}

func TestTokenBucketBurstAndRefill(t *testing.T) {
	rdb, prefix := testenv.Redis(t)
	l := ratelimit.New(rdb, prefix)
	ctx := context.Background()
	for i := 0; i < 5; i++ {
		if !l.Bucket(ctx, "flash:user", 10, 5).Allowed {
			t.Fatalf("burst request %d limited", i)
		}
	}
	r := l.Bucket(ctx, "flash:user", 10, 5)
	if r.Allowed || r.RetryAfter <= 0 {
		t.Fatalf("bucket should be empty: %+v", r)
	}
	time.Sleep(250 * time.Millisecond) // 10 个/秒，约补充 2 个
	ok := 0
	for i := 0; i < 5; i++ {
		if l.Bucket(ctx, "flash:user", 10, 5).Allowed {
			ok++
		}
	}
	if ok < 1 || ok > 3 {
		t.Fatalf("refill out of range: %d", ok)
	}
}

func TestDegradesToLocalWhenRedisDown(t *testing.T) {
	dead := redis.NewClient(&redis.Options{Addr: "127.0.0.1:1", DialTimeout: 50 * time.Millisecond, MaxRetries: -1})
	defer dead.Close()
	l := ratelimit.New(dead, "x")
	allowed := 0
	for i := 0; i < 5; i++ {
		r := l.Window(context.Background(), "k", 3, time.Minute)
		if !r.Degraded {
			t.Fatal("expected degraded result")
		}
		if r.Allowed {
			allowed++
		}
	}
	if allowed != 3 {
		t.Fatalf("local fallback allowed %d, want 3", allowed)
	}
}
