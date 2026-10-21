// Package config 从环境变量读取配置（12-factor），启动时一次性校验，错误配置直接拒绝启动。
package config

import (
	"fmt"
	"os"
	"strconv"
	"time"

	"qingfly/edu-mall/internal/infra/mysqlx"
	"qingfly/edu-mall/internal/infra/redisx"
	"qingfly/edu-mall/internal/platform"
)

type Config struct {
	HTTPAddr  string
	PprofAddr string
	SeedDemo  bool
	MySQL     mysqlx.Options
	Redis     redisx.Options
	App       platform.Config
}

func env(name, fallback string) string {
	if v := os.Getenv(name); v != "" {
		return v
	}
	return fallback
}

func intEnv(name string, fallback int) (int, error) {
	raw := os.Getenv(name)
	if raw == "" {
		return fallback, nil
	}
	n, err := strconv.Atoi(raw)
	if err != nil {
		return 0, fmt.Errorf("%s must be an integer", name)
	}
	return n, nil
}

func keyFile(name string) (string, error) {
	path := os.Getenv(name)
	if path == "" {
		return "", nil
	}
	b, err := os.ReadFile(path)
	if err != nil {
		return "", fmt.Errorf("cannot read %s: %w", name, err)
	}
	return string(b), nil
}

// Load 读取全部配置。
func Load() (Config, error) {
	var c Config
	var err error
	c.HTTPAddr = env("HTTP_ADDR", "127.0.0.1:8081")
	c.PprofAddr = os.Getenv("PPROF_ADDR")
	c.SeedDemo = os.Getenv("SEED_DEMO") == "true"
	c.MySQL.DSN = env("DATABASE_DSN", "campus:campus@tcp(127.0.0.1:3306)/campus")
	if c.MySQL.MaxOpenConns, err = intEnv("DB_MAX_OPEN_CONNS", 50); err != nil {
		return c, err
	}
	if c.MySQL.MaxIdleConns, err = intEnv("DB_MAX_IDLE_CONNS", 25); err != nil {
		return c, err
	}
	c.Redis.Addr = env("REDIS_ADDR", "127.0.0.1:6379")
	c.Redis.Password = os.Getenv("REDIS_PASSWORD")
	if c.Redis.DB, err = intEnv("REDIS_DB", 0); err != nil {
		return c, err
	}
	if c.Redis.PoolSize, err = intEnv("REDIS_POOL_SIZE", 64); err != nil {
		return c, err
	}

	a := &c.App
	a.Production = os.Getenv("APP_ENV") == "production"
	a.PublicOrigin = os.Getenv("PUBLIC_ORIGIN")
	a.AlipayAppID = os.Getenv("ALIPAY_APP_ID")
	a.AlipaySellerID = os.Getenv("ALIPAY_SELLER_ID")
	if a.AlipayPrivateKey, err = keyFile("ALIPAY_PRIVATE_KEY_FILE"); err != nil {
		return c, err
	}
	if a.AlipayPublicKey, err = keyFile("ALIPAY_PUBLIC_KEY_FILE"); err != nil {
		return c, err
	}
	a.AlipayGateway = os.Getenv("ALIPAY_GATEWAY")
	a.ModelURL = os.Getenv("MODEL_BASE_URL")
	a.ModelKey = os.Getenv("MODEL_API_KEY")
	a.ModelName = os.Getenv("MODEL_NAME")
	a.UploadDir = env("UPLOAD_DIR", "data/uploads")
	a.TrustedProxyCIDRs = os.Getenv("TRUSTED_PROXY_CIDRS")
	a.AdminUserIDs = os.Getenv("ADMIN_USER_IDS")
	a.AuthCodeSecret = os.Getenv("AUTH_CODE_SECRET")
	a.RedisPrefix = env("REDIS_PREFIX", "campus")
	a.Instance = os.Getenv("INSTANCE_ID")
	if a.NodeID, err = intEnv("NODE_ID", -1); err != nil {
		return c, err
	}
	if a.NodeID > 1023 {
		return c, fmt.Errorf("NODE_ID must be in [0,1023]")
	}
	minutes, err := intEnv("PAYMENT_WINDOW_MINUTES", 15)
	if err != nil {
		return c, err
	}
	a.PaymentWindow = time.Duration(minutes) * time.Minute
	smtpPort, err := intEnv("SMTP_PORT", 0)
	if err != nil {
		return c, err
	}
	a.AuthDelivery = platform.DeliveryConfig{Mode: env("AUTH_DELIVERY_MODE", "disabled"), SMTPHost: os.Getenv("SMTP_HOST"), SMTPPort: smtpPort, SMTPUser: os.Getenv("SMTP_USER"), SMTPPassword: os.Getenv("SMTP_PASSWORD"), SMTPFrom: os.Getenv("SMTP_FROM"), SMTPTLSMode: os.Getenv("SMTP_TLS_MODE"), SMSAccessKeyID: os.Getenv("SMS_ACCESS_KEY_ID"), SMSAccessKeySecret: os.Getenv("SMS_ACCESS_KEY_SECRET"), SMSSignName: os.Getenv("SMS_SIGN_NAME"), SMSTemplateCode: os.Getenv("SMS_TEMPLATE_CODE"), SMSRegion: os.Getenv("SMS_REGION")}
	if err = platform.LoadAgentConnections(os.Getenv("AGENT_CONNECTIONS_FILE"), a); err != nil {
		return c, fmt.Errorf("agent connections configuration rejected: %w", err)
	}
	if c.SeedDemo && a.Production {
		return c, fmt.Errorf("SEED_DEMO forbidden in production")
	}
	return c, nil
}
