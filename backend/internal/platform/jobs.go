package platform

import (
	"context"
	"errors"
	"log/slog"
	"sync"
	"time"

	"qingfly/edu-mall/internal/pkg/lock"
)

// Run 启动所有后台任务，直到 ctx 结束后等待它们退出。
//
// 任务分两类：
//   - 每个实例都运行：Pub/Sub 订阅（IM 推送、缓存失效）、延迟队列消费、抢购消息消费
//     （Redis 保证同一任务/消息只会被一个实例领取）；
//   - 全局只需一个实例运行：兜底关单扫描、支付对账、抢购库存对账、活动预热，
//     用分布式锁选出执行者（锁带看门狗续期，执行者宕机后 TTL 到期由其他实例接手）。
func (s *Server) Run(ctx context.Context) {
	var wg sync.WaitGroup
	spawn := func(name string, fn func(context.Context) error) {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for ctx.Err() == nil {
				err := safeRun(ctx, fn)
				if ctx.Err() != nil {
					return
				}
				slog.Warn("background worker stopped, restarting", "worker", name, "error", err)
				select {
				case <-ctx.Done():
				case <-time.After(2 * time.Second):
				}
			}
		}()
	}
	periodic := func(name string, every time.Duration, fn func(context.Context) error) {
		wg.Add(1)
		go func() {
			defer wg.Done()
			ticker := time.NewTicker(every)
			defer ticker.Stop()
			for {
				err := s.locks.Run(ctx, "job:"+name, every*2, func(ctx context.Context) error {
					jctx, cancel := context.WithTimeout(ctx, every)
					defer cancel()
					return safeRun(jctx, fn)
				})
				if err != nil && !errors.Is(err, lock.ErrNotAcquired) && ctx.Err() == nil {
					slog.Error("periodic job failed", "job", name, "error", err)
				}
				select {
				case <-ctx.Done():
					return
				case <-ticker.C:
				}
			}
		}()
	}
	spawn("live-hub", s.live.Listen)
	spawn("cache-invalidation", s.cache.Listen)
	spawn("order-timeout", func(ctx context.Context) error { return s.timeouts.Run(ctx, s.expireOrder) })
	spawn("flash-consumer", s.flash.Run)
	periodic("flash-warm", time.Minute, s.flash.Warm)
	periodic("flash-reconcile", 15*time.Second, s.flash.Reconcile)
	periodic("expiry-scan", time.Minute, s.ExpireUnpaid)
	periodic("payment-reconcile", 30*time.Second, s.ReconcilePayments)
	periodic("session-cleanup", 10*time.Minute, s.cleanupSessions)
	<-ctx.Done()
	wg.Wait()
}

func safeRun(ctx context.Context, fn func(context.Context) error) (err error) {
	defer func() {
		if p := recover(); p != nil {
			slog.Error("background task panic", "panic", p)
			err = errors.New("panic")
		}
	}()
	return fn(ctx)
}

// cleanupSessions 分批删除过期会话，避免一次大删除长时间持有锁、产生大事务。
func (s *Server) cleanupSessions(ctx context.Context) error {
	for i := 0; i < 20; i++ {
		res, err := s.db.ExecContext(ctx, "DELETE FROM sessions WHERE expires_at<? LIMIT 1000", now())
		if err != nil {
			return err
		}
		if n, _ := res.RowsAffected(); n < 1000 {
			return nil
		}
	}
	return nil
}
