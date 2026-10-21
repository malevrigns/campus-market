# 校园集市后端

Go 1.26.8 模块化单体，MySQL 8 + Redis 7。唯一应用入口为 `cmd/server`。

```sh
docker compose -f ../compose.dev.yaml up -d --wait   # MySQL + Redis
make run                                              # 带演示数据与本地验证码收件箱
make test                                             # 集成测试（-race）
make bench                                            # 抢购压测（服务端需 TRUSTED_PROXY_CIDRS=127.0.0.1/32）
```

默认监听 `127.0.0.1:8081`。`GET /api/v1/health` 检查 MySQL 与 Redis；`GET /api/v1/config` 只公开支付/模型可用性，不返回密钥。

| 环境变量 | 默认值 | 说明 |
|---|---|---|
| `DATABASE_DSN` | `campus:campus@tcp(127.0.0.1:3306)/campus` | go-sql-driver DSN，启动时自动迁移 |
| `REDIS_ADDR` / `REDIS_PASSWORD` / `REDIS_DB` | `127.0.0.1:6379` | |
| `REDIS_PREFIX` | `campus` | 所有 key 的前缀，多环境共用 Redis 时隔离 |
| `DB_MAX_OPEN_CONNS` / `DB_MAX_IDLE_CONNS` | 50 / 25 | 连接池 |
| `PAYMENT_WINDOW_MINUTES` | 15 | 待支付时长 |
| `NODE_ID` | 自动 | 雪花节点号，不设置时通过 Redis 租约分配 |
| `INSTANCE_ID` | hostname-pid | 实例标识（Stream 消费者名、日志） |
| `PPROF_ADDR` | 关闭 | 例如 `127.0.0.1:6060` |

其余支付、模型、验证码配置见根目录 [README](../README.md)、[API 文档](../docs/api.md)、[架构](../docs/architecture.md) 与 [生产说明](../docs/production-readiness.md)。
