package cache

import (
	"container/list"
	"hash/maphash"
	"sync"
	"time"
)

// LRU 是分段加锁的本地 LRU 缓存（L1）。
// 单把互斥锁在高并发读时会成为热点，按 key 哈希拆成多个分段，各自独立加锁，降低锁竞争。
type LRU struct {
	seed   maphash.Seed
	shards []*lruShard
}

type lruShard struct {
	mu    sync.Mutex
	cap   int
	ll    *list.List
	items map[string]*list.Element
}

type lruEntry struct {
	key     string
	value   []byte
	expires int64
}

// NewLRU 创建容量约为 capacity 的缓存，shards 会向上取到 2 的幂。
func NewLRU(capacity, shards int) *LRU {
	n := 1
	for n < shards {
		n <<= 1
	}
	per := capacity / n
	if per < 1 {
		per = 1
	}
	l := &LRU{seed: maphash.MakeSeed(), shards: make([]*lruShard, n)}
	for i := range l.shards {
		l.shards[i] = &lruShard{cap: per, ll: list.New(), items: map[string]*list.Element{}}
	}
	return l
}

func (l *LRU) shard(key string) *lruShard {
	return l.shards[maphash.String(l.seed, key)&uint64(len(l.shards)-1)]
}

func (l *LRU) Get(key string) ([]byte, bool) {
	s := l.shard(key)
	s.mu.Lock()
	defer s.mu.Unlock()
	el, ok := s.items[key]
	if !ok {
		return nil, false
	}
	e := el.Value.(*lruEntry)
	if time.Now().UnixNano() > e.expires {
		s.ll.Remove(el)
		delete(s.items, key)
		return nil, false
	}
	s.ll.MoveToFront(el)
	return e.value, true
}

func (l *LRU) Set(key string, value []byte, ttl time.Duration) {
	s := l.shard(key)
	s.mu.Lock()
	defer s.mu.Unlock()
	exp := time.Now().Add(ttl).UnixNano()
	if el, ok := s.items[key]; ok {
		e := el.Value.(*lruEntry)
		e.value, e.expires = value, exp
		s.ll.MoveToFront(el)
		return
	}
	s.items[key] = s.ll.PushFront(&lruEntry{key: key, value: value, expires: exp})
	for s.ll.Len() > s.cap {
		last := s.ll.Back()
		s.ll.Remove(last)
		delete(s.items, last.Value.(*lruEntry).key)
	}
}

func (l *LRU) Delete(key string) {
	s := l.shard(key)
	s.mu.Lock()
	defer s.mu.Unlock()
	if el, ok := s.items[key]; ok {
		s.ll.Remove(el)
		delete(s.items, key)
	}
}

func (l *LRU) Len() int {
	n := 0
	for _, s := range l.shards {
		s.mu.Lock()
		n += s.ll.Len()
		s.mu.Unlock()
	}
	return n
}
