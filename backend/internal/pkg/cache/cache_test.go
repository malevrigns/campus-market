package cache_test

import (
	"context"
	"errors"
	"fmt"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/redis/go-redis/v9"

	"qingfly/edu-mall/internal/pkg/cache"
	"qingfly/edu-mall/internal/testenv"
)

type item struct {
	ID    string `json:"id"`
	Price int64  `json:"price"`
}

func TestSingleflightCollapsesConcurrentMisses(t *testing.T) {
	rdb, prefix := testenv.Redis(t)
	c := cache.New(rdb, cache.Options{Prefix: prefix})
	var loads atomic.Int32
	var wg sync.WaitGroup
	for i := 0; i < 100; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			v, err := cache.Fetch(context.Background(), c, "hot", time.Minute, func(context.Context) (item, error) {
				loads.Add(1)
				time.Sleep(50 * time.Millisecond) // 模拟慢查询
				return item{ID: "hot", Price: 100}, nil
			})
			if err != nil || v.Price != 100 {
				t.Errorf("fetch: %v %+v", err, v)
			}
		}()
	}
	wg.Wait()
	if loads.Load() != 1 {
		t.Fatalf("hot key loaded %d times, want 1", loads.Load())
	}
	ttl := rdb.TTL(context.Background(), prefix+":hot").Val()
	if ttl < time.Minute || ttl > 72*time.Second {
		t.Fatalf("ttl %v should be base + up to 20%% jitter", ttl)
	}
}

func TestNullCachingPreventsPenetration(t *testing.T) {
	rdb, prefix := testenv.Redis(t)
	c := cache.New(rdb, cache.Options{Prefix: prefix, LocalTTL: time.Millisecond})
	var loads atomic.Int32
	load := func(context.Context) (item, error) {
		loads.Add(1)
		return item{}, cache.ErrNotFound
	}
	for i := 0; i < 10; i++ {
		if _, err := cache.Fetch(context.Background(), c, "missing", time.Minute, load); !errors.Is(err, cache.ErrNotFound) {
			t.Fatalf("expected not found, got %v", err)
		}
		time.Sleep(2 * time.Millisecond)
	}
	if loads.Load() != 1 {
		t.Fatalf("missing key hit the database %d times", loads.Load())
	}
}

func TestInvalidateAcrossInstances(t *testing.T) {
	rdb, prefix := testenv.Redis(t)
	opts := cache.Options{Prefix: prefix, LocalTTL: time.Minute, DoubleDeleteDelay: 50 * time.Millisecond}
	a, b := cache.New(rdb, opts), cache.New(rdb, opts)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go b.Listen(ctx)
	time.Sleep(50 * time.Millisecond) // 等订阅建立
	price := atomic.Int64{}
	price.Store(1)
	load := func(context.Context) (item, error) { return item{Price: price.Load()}, nil }
	if v, _ := cache.Fetch(ctx, b, "p1", time.Minute, load); v.Price != 1 {
		t.Fatal("initial load")
	}
	price.Store(2) // 数据库更新
	a.Invalidate(ctx, "p1")
	deadline := time.Now().Add(2 * time.Second)
	for {
		v, _ := cache.Fetch(ctx, b, "p1", time.Minute, load)
		if v.Price == 2 {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("instance b kept serving stale L1 value")
		}
		time.Sleep(10 * time.Millisecond)
	}
}

// TestDelayedDoubleDeleteRepairsStaleBackfill 复现经典竞态：读请求在 DB 更新前读到旧值，
// 在缓存删除之后才回填。延迟双删会把这个脏值再删一次。
func TestDelayedDoubleDeleteRepairsStaleBackfill(t *testing.T) {
	rdb, prefix := testenv.Redis(t)
	c := cache.New(rdb, cache.Options{Prefix: prefix, LocalTTL: time.Millisecond, DoubleDeleteDelay: 100 * time.Millisecond})
	ctx := context.Background()
	c.Invalidate(ctx, "p")                                // 写请求：更新 DB 后删除缓存
	rdb.Set(ctx, prefix+":p", `{"price":1}`, time.Minute) // 慢读请求随后把旧值写回
	time.Sleep(200 * time.Millisecond)                    // 延迟的第二次删除
	if n := rdb.Exists(ctx, prefix+":p").Val(); n != 0 {
		t.Fatal("stale backfill survived delayed double delete")
	}
}

func TestDegradesToLoaderWhenRedisDown(t *testing.T) {
	dead := redis.NewClient(&redis.Options{Addr: "127.0.0.1:1", DialTimeout: 50 * time.Millisecond, MaxRetries: -1})
	defer dead.Close()
	c := cache.New(dead, cache.Options{Prefix: "x"})
	v, err := cache.Fetch(context.Background(), c, "k", time.Minute, func(context.Context) (item, error) { return item{Price: 7}, nil })
	if err != nil || v.Price != 7 {
		t.Fatalf("degraded fetch: %v %+v", err, v)
	}
	if c.Stats().Degraded == 0 {
		t.Fatal("degraded counter not incremented")
	}
}

func TestLRUEvictionAndTTL(t *testing.T) {
	l := cache.NewLRU(4, 1)
	for i := 0; i < 5; i++ {
		l.Set(fmt.Sprint(i), []byte{byte(i)}, time.Minute)
	}
	if _, ok := l.Get("0"); ok {
		t.Fatal("least recently used entry not evicted")
	}
	l.Get("1")
	l.Set("5", []byte{5}, time.Minute)
	if _, ok := l.Get("1"); !ok {
		t.Fatal("recently used entry evicted")
	}
	l.Set("short", []byte{1}, time.Millisecond)
	time.Sleep(5 * time.Millisecond)
	if _, ok := l.Get("short"); ok {
		t.Fatal("expired entry returned")
	}
}

func BenchmarkLRUParallel(b *testing.B) {
	l := cache.NewLRU(100000, 32)
	for i := 0; i < 10000; i++ {
		l.Set(fmt.Sprint(i), []byte("v"), time.Hour)
	}
	b.RunParallel(func(pb *testing.PB) {
		i := 0
		for pb.Next() {
			l.Get(fmt.Sprint(i % 10000))
			i++
		}
	})
}
