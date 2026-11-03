package redisrepo

import (
	"context"
	"time"

	"github.com/redis/go-redis/v9"

	"qingfly/edu-mall/internal/pkg/ratelimit"
	"qingfly/edu-mall/internal/port"
)

// Limiter 把基础设施限流器适配成业务层的 RateLimiter 端口。
type Limiter struct {
	l *ratelimit.Limiter
}

func NewLimiter(rdb redis.UniversalClient, prefix string) *Limiter {
	return &Limiter{l: ratelimit.New(rdb, prefix+":rl")}
}

func (a *Limiter) Window(ctx context.Context, key string, limit int, window time.Duration) (time.Duration, bool) {
	res := a.l.Window(ctx, key, limit, window)
	return res.RetryAfter, res.Allowed
}

func (a *Limiter) Bucket(ctx context.Context, key string, rate float64, burst int) (time.Duration, bool) {
	res := a.l.Bucket(ctx, key, rate, burst)
	return res.RetryAfter, res.Allowed
}

var _ port.RateLimiter = (*Limiter)(nil)
