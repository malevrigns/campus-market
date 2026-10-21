// Command server 是校园集市唯一的应用入口（模块化单体）。
//
// 启动顺序：读取配置 → 连接 MySQL（等待就绪）→ 执行迁移 → 连接 Redis → 组装应用 →
// 启动后台任务 → 监听 HTTP。
// 退出顺序（SIGTERM）：停止接收新请求并等待在途请求结束 → 取消后台任务并等待其退出 →
// 释放雪花节点租约 → 关闭 Redis/MySQL。
package main

import (
	"context"
	"errors"
	"log/slog"
	"net"
	"net/http"
	"net/http/pprof"
	"os"
	"os/signal"
	"syscall"
	"time"

	"qingfly/edu-mall/internal/config"
	"qingfly/edu-mall/internal/infra/mysqlx"
	"qingfly/edu-mall/internal/infra/redisx"
	"qingfly/edu-mall/internal/platform"
)

func main() {
	if len(os.Args) > 1 && os.Args[1] == "--healthcheck" {
		os.Exit(healthcheck())
	}
	slog.SetDefault(slog.New(slog.NewJSONHandler(os.Stdout, nil)))
	if err := run(); err != nil {
		slog.Error("server exited", "error", err)
		os.Exit(1)
	}
}

func healthcheck() int {
	addr := os.Getenv("HTTP_ADDR")
	if addr == "" {
		addr = "127.0.0.1:8081"
	}
	host, port, err := net.SplitHostPort(addr)
	if err != nil {
		return 1
	}
	if host == "" || host == "0.0.0.0" {
		host = "127.0.0.1"
	}
	client := http.Client{Timeout: 3 * time.Second}
	resp, err := client.Get("http://" + net.JoinHostPort(host, port) + "/api/v1/health")
	if err != nil {
		return 1
	}
	resp.Body.Close()
	if resp.StatusCode != 200 {
		return 1
	}
	return 0
}

func run() error {
	cfg, err := config.Load()
	if err != nil {
		return err
	}
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	startCtx, cancel := context.WithTimeout(ctx, 2*time.Minute)
	defer cancel()
	db, err := mysqlx.Open(startCtx, cfg.MySQL)
	if err != nil {
		return err
	}
	defer db.Close()
	if err = mysqlx.Migrate(startCtx, db); err != nil {
		return err
	}
	rdb, err := redisx.Open(startCtx, cfg.Redis)
	if err != nil {
		return err
	}
	defer rdb.Close()
	app, err := platform.NewServer(db, rdb, cfg.App)
	if err != nil {
		return err
	}
	defer func() {
		closeCtx, done := context.WithTimeout(context.Background(), 2*time.Second)
		defer done()
		app.Close(closeCtx)
	}()
	if cfg.SeedDemo {
		if err = app.SeedDemo(); err != nil {
			return err
		}
	}

	workersCtx, stopWorkers := context.WithCancel(context.Background())
	workersDone := make(chan struct{})
	go func() {
		defer close(workersDone)
		app.Run(workersCtx)
	}()

	if cfg.PprofAddr != "" {
		go servePprof(cfg.PprofAddr)
	}

	srv := &http.Server{Addr: cfg.HTTPAddr, Handler: app.Handler(), ReadHeaderTimeout: 5 * time.Second, ReadTimeout: 15 * time.Second, WriteTimeout: 2 * time.Minute, IdleTimeout: 60 * time.Second, MaxHeaderBytes: 32 << 10}
	srv.BaseContext = func(net.Listener) context.Context { return ctx }
	serveErr := make(chan error, 1)
	go func() {
		slog.Info("campus server started", "address", srv.Addr, "instance", cfg.App.Instance)
		if err := srv.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
			serveErr <- err
		}
		close(serveErr)
	}()
	select {
	case <-ctx.Done():
	case err = <-serveErr:
	}
	shutdown, done := context.WithTimeout(context.Background(), 15*time.Second)
	defer done()
	if e := srv.Shutdown(shutdown); e != nil {
		slog.Error("http shutdown failed", "error", e)
	}
	stopWorkers()
	select {
	case <-workersDone:
	case <-shutdown.Done():
		slog.Warn("background workers did not stop in time")
	}
	slog.Info("campus server stopped")
	return err
}

// servePprof 只应监听内网/本机地址（例如 127.0.0.1:6060），用于线上排查 CPU/内存/goroutine 泄漏。
func servePprof(addr string) {
	mux := http.NewServeMux()
	mux.HandleFunc("/debug/pprof/", pprof.Index)
	mux.HandleFunc("/debug/pprof/cmdline", pprof.Cmdline)
	mux.HandleFunc("/debug/pprof/profile", pprof.Profile)
	mux.HandleFunc("/debug/pprof/symbol", pprof.Symbol)
	mux.HandleFunc("/debug/pprof/trace", pprof.Trace)
	srv := &http.Server{Addr: addr, Handler: mux, ReadHeaderTimeout: 5 * time.Second}
	slog.Info("pprof listening", "address", addr)
	if err := srv.ListenAndServe(); err != nil {
		slog.Warn("pprof server stopped", "error", err)
	}
}
