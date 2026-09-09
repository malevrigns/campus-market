package main

import (
	"context"
	"log/slog"
	"net"
	"net/http"
	"os"
	"os/signal"
	"qingfly/edu-mall/internal/platform"
	"syscall"
	"time"
)

func env(name, fallback string) string {
	if v := os.Getenv(name); v != "" {
		return v
	}
	return fallback
}
func keyFile(name string) string {
	path := os.Getenv(name)
	if path == "" {
		return ""
	}
	b, e := os.ReadFile(path)
	if e != nil {
		slog.Error("cannot read key file", "name", name, "error", e)
		os.Exit(1)
	}
	return string(b)
}
func main() {
	if len(os.Args) > 1 && os.Args[1] == "--healthcheck" {
		client := http.Client{Timeout: 3 * time.Second}
		resp, e := client.Get("http://127.0.0.1:8081/api/v1/health")
		if e != nil {
			os.Exit(1)
		}
		resp.Body.Close()
		if resp.StatusCode != 200 {
			os.Exit(1)
		}
		return
	}
	slog.SetDefault(slog.New(slog.NewJSONHandler(os.Stdout, nil)))
	cfg := platform.Config{Production: os.Getenv("APP_ENV") == "production", PublicOrigin: os.Getenv("PUBLIC_ORIGIN"), AlipayAppID: os.Getenv("ALIPAY_APP_ID"), AlipaySellerID: os.Getenv("ALIPAY_SELLER_ID"), AlipayPrivateKey: keyFile("ALIPAY_PRIVATE_KEY_FILE"), AlipayPublicKey: keyFile("ALIPAY_PUBLIC_KEY_FILE"), AlipayGateway: os.Getenv("ALIPAY_GATEWAY"), ModelURL: os.Getenv("MODEL_BASE_URL"), ModelKey: os.Getenv("MODEL_API_KEY"), ModelName: os.Getenv("MODEL_NAME")}
	cfg.UploadDir = env("UPLOAD_DIR", "data/uploads")
	cfg.TrustedProxyCIDRs = os.Getenv("TRUSTED_PROXY_CIDRS")
	cfg.AdminUserIDs = os.Getenv("ADMIN_USER_IDS")
	if e := platform.LoadAgentConnections(os.Getenv("AGENT_CONNECTIONS_FILE"), &cfg); e != nil {
		slog.Error("agent connections configuration rejected", "error", e)
		os.Exit(1)
	}
	db, e := platform.OpenStore(env("DATABASE_PATH", "data/campus.db"))
	if e != nil {
		slog.Error("database startup failed", "error", e)
		os.Exit(1)
	}
	defer db.Close()
	app, e := platform.NewServer(db, cfg)
	if e != nil {
		slog.Error("configuration rejected", "error", e)
		os.Exit(1)
	}
	if os.Getenv("SEED_DEMO") == "true" {
		if cfg.Production {
			slog.Error("SEED_DEMO forbidden in production")
			os.Exit(1)
		}
		if e = app.SeedDemo(); e != nil {
			slog.Error("seed failed", "error", e)
			os.Exit(1)
		}
	}
	srv := &http.Server{Addr: env("HTTP_ADDR", "127.0.0.1:8081"), Handler: app.Handler(), ReadHeaderTimeout: 5 * time.Second, ReadTimeout: 15 * time.Second, WriteTimeout: 2 * time.Minute, IdleTimeout: 60 * time.Second, MaxHeaderBytes: 32 << 10}
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	srv.BaseContext = func(net.Listener) context.Context { return ctx }
	go func() {
		ticker := time.NewTicker(30 * time.Second)
		defer ticker.Stop()
		for {
			select {
			case <-ctx.Done():
				return
			case <-ticker.C:
				if e := app.ExpireUnpaid(); e != nil {
					slog.Error("expiry failed", "error", e)
				}
				checkCtx, cancel := context.WithTimeout(ctx, 25*time.Second)
				if e := app.ReconcilePayments(checkCtx); e != nil {
					slog.Error("reconciliation failed", "error", e)
				}
				cancel()
			}
		}
	}()
	go func() {
		slog.Info("campus server started", "address", srv.Addr)
		if e := srv.ListenAndServe(); e != nil && e != http.ErrServerClosed {
			slog.Error("http failed", "error", e)
			stop()
		}
	}()
	<-ctx.Done()
	shutdown, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	if e = srv.Shutdown(shutdown); e != nil {
		slog.Error("shutdown failed", "error", e)
	}
}
