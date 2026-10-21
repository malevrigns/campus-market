package lock_test

import (
	"context"
	"errors"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"qingfly/edu-mall/internal/pkg/lock"
	"qingfly/edu-mall/internal/testenv"
)

func TestMutualExclusion(t *testing.T) {
	rdb, prefix := testenv.Redis(t)
	c := lock.New(rdb, prefix+":lock")
	var inside, maxInside, total atomic.Int32
	var wg sync.WaitGroup
	for i := 0; i < 20; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
			defer cancel()
			l, err := c.Acquire(ctx, "counter", 2*time.Second, 10*time.Millisecond)
			if err != nil {
				t.Error(err)
				return
			}
			n := inside.Add(1)
			for {
				m := maxInside.Load()
				if n <= m || maxInside.CompareAndSwap(m, n) {
					break
				}
			}
			time.Sleep(5 * time.Millisecond)
			total.Add(1)
			inside.Add(-1)
			if err := l.Release(ctx); err != nil {
				t.Error(err)
			}
		}()
	}
	wg.Wait()
	if maxInside.Load() != 1 || total.Load() != 20 {
		t.Fatalf("critical section overlap: max=%d total=%d", maxInside.Load(), total.Load())
	}
}

func TestReleaseOnlyOwnLockAndFencing(t *testing.T) {
	rdb, prefix := testenv.Redis(t)
	c := lock.New(rdb, prefix+":lock")
	ctx := context.Background()
	first, err := c.TryAcquire(ctx, "job", 100*time.Millisecond)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = c.TryAcquire(ctx, "job", time.Second); !errors.Is(err, lock.ErrNotAcquired) {
		t.Fatalf("second holder: %v", err)
	}
	time.Sleep(150 * time.Millisecond) // first 的 TTL 过期（模拟 GC 停顿/业务超时）
	second, err := c.TryAcquire(ctx, "job", time.Second)
	if err != nil {
		t.Fatal(err)
	}
	if second.Fence <= first.Fence {
		t.Fatalf("fencing token must increase: %d -> %d", first.Fence, second.Fence)
	}
	// 旧持有者"醒来"后释放，不能删掉新持有者的锁。
	if err = first.Release(ctx); !errors.Is(err, lock.ErrLost) {
		t.Fatalf("stale release: %v", err)
	}
	if _, err = c.TryAcquire(ctx, "job", time.Second); !errors.Is(err, lock.ErrNotAcquired) {
		t.Fatal("stale holder released someone else's lock")
	}
	if err = second.Release(ctx); err != nil {
		t.Fatal(err)
	}
}

func TestKeepAliveExtendsLease(t *testing.T) {
	rdb, prefix := testenv.Redis(t)
	c := lock.New(rdb, prefix+":lock")
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	l, err := c.TryAcquire(ctx, "long", 300*time.Millisecond)
	if err != nil {
		t.Fatal(err)
	}
	lost := l.KeepAlive(ctx)
	time.Sleep(time.Second)
	select {
	case <-lost:
		t.Fatal("watchdog lost the lock")
	default:
	}
	if _, err = c.TryAcquire(ctx, "long", time.Second); !errors.Is(err, lock.ErrNotAcquired) {
		t.Fatal("lease expired despite watchdog")
	}
}

func TestRunCancelsWorkWhenLockIsLost(t *testing.T) {
	rdb, prefix := testenv.Redis(t)
	c := lock.New(rdb, prefix+":lock")
	start := time.Now()
	err := c.Run(context.Background(), "steal", 300*time.Millisecond, func(ctx context.Context) error {
		rdb.Del(context.Background(), prefix+":lock:steal") // 模拟锁被运维误删/主从切换丢失
		<-ctx.Done()
		return nil
	})
	if !errors.Is(err, lock.ErrLost) {
		t.Fatalf("expected ErrLost, got %v", err)
	}
	if time.Since(start) > 2*time.Second {
		t.Fatal("lost lock detected too late")
	}
	if err = c.Run(context.Background(), "busy", time.Second, func(ctx context.Context) error {
		return c.Run(ctx, "busy", time.Second, func(context.Context) error { return nil })
	}); !errors.Is(err, lock.ErrNotAcquired) {
		t.Fatalf("nested run must not acquire: %v", err)
	}
}
