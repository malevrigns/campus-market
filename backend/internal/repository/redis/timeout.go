package redisrepo

import (
	"context"
	"time"

	"qingfly/edu-mall/internal/pkg/delayqueue"
	"qingfly/edu-mall/internal/port"
)

// TimeoutQueue 把通用延迟队列适配成订单超时端口。
type TimeoutQueue struct {
	q *delayqueue.Queue
}

func NewTimeoutQueue(q *delayqueue.Queue) *TimeoutQueue { return &TimeoutQueue{q: q} }

func (t *TimeoutQueue) Add(ctx context.Context, id string, at time.Time) error {
	return t.q.Add(ctx, id, at)
}

func (t *TimeoutQueue) Remove(ctx context.Context, id string) error {
	return t.q.Remove(ctx, id)
}

func (t *TimeoutQueue) Stats(ctx context.Context) (ready, inflight, dead int64, err error) {
	return t.q.Stats(ctx)
}

func (t *TimeoutQueue) Run(ctx context.Context, handler func(context.Context, string) error) error {
	return t.q.Run(ctx, delayqueue.Handler(handler))
}

var _ port.TimeoutQueue = (*TimeoutQueue)(nil)
