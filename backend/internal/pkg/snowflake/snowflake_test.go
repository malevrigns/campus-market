package snowflake

import (
	"context"
	"sync"
	"testing"
	"time"

	"qingfly/edu-mall/internal/pkg/lock"
	"qingfly/edu-mall/internal/testenv"
)

func TestUniqueAndMonotonicUnderConcurrency(t *testing.T) {
	n, _ := NewNode(7)
	const workers, per = 8, 20000
	var mu sync.Mutex
	seen := make(map[int64]bool, workers*per)
	var wg sync.WaitGroup
	for w := 0; w < workers; w++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			last := int64(0)
			local := make([]int64, 0, per)
			for i := 0; i < per; i++ {
				id, err := n.Next()
				if err != nil {
					t.Error(err)
					return
				}
				if id <= last {
					t.Errorf("not monotonic within goroutine: %d <= %d", id, last)
					return
				}
				last = id
				local = append(local, id)
			}
			mu.Lock()
			for _, id := range local {
				if seen[id] {
					t.Errorf("duplicate id %d", id)
				}
				seen[id] = true
			}
			mu.Unlock()
		}()
	}
	wg.Wait()
	if len(seen) != workers*per {
		t.Fatalf("got %d unique ids", len(seen))
	}
}

// TestSmallClockRollbackDoesNotDuplicate 是对旧实现 bug 的回归测试：
// 时钟回拨 ≤5ms 时沿用上次时间戳，必须继续递增序列号，否则会生成与上一个相同的 ID。
func TestSmallClockRollbackDoesNotDuplicate(t *testing.T) {
	n, _ := NewNode(1)
	clock := int64(Epoch + 1_000_000)
	n.now = func() int64 { return clock }
	a, _ := n.Next()
	clock -= 3
	b, err := n.Next()
	if err != nil || b <= a {
		t.Fatalf("rollback within tolerance: a=%d b=%d err=%v", a, b, err)
	}
	clock -= 10
	if _, err = n.Next(); err == nil {
		t.Fatal("large rollback must be rejected")
	}
}

func TestTimeRoundTrip(t *testing.T) {
	n, _ := NewNode(3)
	before := time.Now().Add(-time.Millisecond)
	id, _ := n.Next()
	if got := Time(id); got.Before(before) || got.After(time.Now().Add(time.Millisecond)) {
		t.Fatalf("decoded time %v", got)
	}
	if _, err := NewNode(MaxNode + 1); err == nil {
		t.Fatal("node out of range accepted")
	}
}

func TestLeasesAreExclusive(t *testing.T) {
	rdb, prefix := testenv.Redis(t)
	locks := lock.New(rdb, prefix+":lock")
	ctx := context.Background()
	a, err := AcquireLease(ctx, locks, time.Second)
	if err != nil {
		t.Fatal(err)
	}
	b, err := AcquireLease(ctx, locks, time.Second)
	if err != nil {
		t.Fatal(err)
	}
	defer a.Release(ctx)
	defer b.Release(ctx)
	if a.Node.ID() == b.Node.ID() {
		t.Fatalf("two processes share node %d", a.Node.ID())
	}
}
