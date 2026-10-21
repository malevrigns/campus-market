# 常用命令（Linux/macOS/WSL；Windows PowerShell 用法见根目录 README）。
COMPOSE        ?= docker compose -f ../compose.dev.yaml
DEV_DSN        ?= campus:campus@tcp(127.0.0.1:3306)/campus
TEST_MYSQL_DSN ?= campus:campus@tcp(127.0.0.1:3306)/
TEST_REDIS_ADDR ?= 127.0.0.1:6379

.PHONY: deps deps-down run test test-short vet bench

deps: ## 启动本地 MySQL + Redis
	$(COMPOSE) up -d --wait

deps-down:
	$(COMPOSE) down

run: deps ## 启动后端（带演示数据、本地验证码收件箱）
	DATABASE_DSN='$(DEV_DSN)' REDIS_ADDR=$(TEST_REDIS_ADDR) SEED_DEMO=true AUTH_DELIVERY_MODE=local go run ./cmd/server

test: deps ## 全量测试（集成测试会为每个用例创建独立数据库与 Redis 前缀）
	TEST_MYSQL_DSN='$(TEST_MYSQL_DSN)' TEST_REDIS_ADDR=$(TEST_REDIS_ADDR) CGO_ENABLED=1 go test -race -count=1 ./...

test-short: ## 只跑不依赖外部服务的单元测试
	go test -count=1 ./...

vet:
	go vet ./...

bench: ## 抢购压测（先用 TRUSTED_PROXY_CIDRS=127.0.0.1/32 启动服务）
	DATABASE_DSN='$(DEV_DSN)' go run ./cmd/flashbench -users 2000 -stock 100 -c 200
