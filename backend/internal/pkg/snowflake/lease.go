package snowflake

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"math/rand/v2"
	"time"

	"qingfly/edu-mall/internal/pkg/lock"
)

// Lease 通过 Redis 锁为当前进程独占一个节点号，并在后台续约。
// 多实例部署时无需手工配置 NODE_ID；进程退出时释放，其他实例可复用。
type Lease struct {
	Node  *Node
	lock  *lock.Lock
	Lost  <-chan struct{}
	close context.CancelFunc
}

// AcquireLease 从随机起点开始尝试占用节点号 [0, MaxNode]。
func AcquireLease(ctx context.Context, locks *lock.Client, ttl time.Duration) (*Lease, error) {
	start := rand.Int64N(MaxNode + 1)
	for i := int64(0); i <= MaxNode; i++ {
		id := (start + i) % (MaxNode + 1)
		l, err := locks.TryAcquire(ctx, fmt.Sprintf("snowflake:node:%d", id), ttl)
		if errors.Is(err, lock.ErrNotAcquired) {
			continue
		}
		if err != nil {
			return nil, err
		}
		node, err := NewNode(id)
		if err != nil {
			return nil, err
		}
		kctx, cancel := context.WithCancel(context.Background())
		lost := l.KeepAlive(kctx)
		go func() {
			select {
			case <-lost:
				// 租约丢失意味着可能有其他实例拿到同一节点号。这里只告警：
				// 订单号在数据库有主键约束兜底，冲突会以唯一键错误暴露，而不会静默覆盖。
				slog.Error("snowflake node lease lost", "node", id)
			case <-kctx.Done():
			}
		}()
		slog.Info("snowflake node acquired", "node", id)
		return &Lease{Node: node, lock: l, Lost: lost, close: cancel}, nil
	}
	return nil, errors.New("snowflake: no free node id")
}

func (l *Lease) Release(ctx context.Context) {
	l.close()
	_ = l.lock.Release(ctx)
}
