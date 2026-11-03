// Package job 编排后台任务：多实例各跑一份的订阅/消费任务，
// 以及通过分布式锁选主的兜底扫描、对账和清理任务。
package job

import (
	"context"
	"errors"
	"log/slog"
	"sync"
	"time"

	"qingfly/edu-mall/internal/pkg/lock"
	"qingfly/edu-mall/internal/port"
	"qingfly/edu-mall/internal/service"
)

type CacheListener interface {
	Listen(ctx context.Context) error
}

type Runner struct {
	locks    *lock.Client
	live     port.LiveNotifier
	cache    CacheListener
	timeouts port.TimeoutQueue
	expire   func(context.Context, string) error
	orders   *service.OrderService
	payments *service.PaymentService
	flash    *service.FlashService
	auth     *service.AuthService
}

func New(locks *lock.Client, live port.LiveNotifier, cache CacheListener, timeouts port.TimeoutQueue, orders *service.OrderService, payments *service.PaymentService, flash *service.FlashService, auth *service.AuthService) *Runner {
	return &Runner{locks: locks, live: live, cache: cache, timeouts: timeouts, expire: orders.Expire, orders: orders, payments: payments, flash: flash, auth: auth}
}

func (r *Runner) Run(ctx context.Context) {
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
				err := r.locks.Run(ctx, "job:"+name, every*2, func(ctx context.Context) error {
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
	spawn("live-hub", r.live.Listen)
	spawn("cache-invalidation", r.cache.Listen)
	spawn("order-timeout", func(ctx context.Context) error { return r.timeouts.Run(ctx, r.expire) })
	spawn("flash-consumer", r.flash.Run)
	periodic("flash-warm", time.Minute, r.flash.Warm)
	periodic("flash-reconcile", 15*time.Second, r.flash.Reconcile)
	periodic("expiry-scan", time.Minute, r.orders.ExpireUnpaid)
	periodic("payment-reconcile", 30*time.Second, r.payments.Reconcile)
	periodic("cleanup", 10*time.Minute, r.auth.Cleanup)
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
