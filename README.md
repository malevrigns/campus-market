# 校园集市 · Campus Market

校园二手交易平台：商品发布与搜索、订单与支付宝支付、买卖双方即时聊天、**限时抢购**、AI 导购助手，桌面与手机端界面。

后端是 Go 模块化单体（不拆微服务），依赖 **MySQL 8 + Redis 7**；服务进程无状态，可以多副本部署。

- 架构与关键链路：[docs/architecture.md](docs/architecture.md)
- 面试讲解手册（亮点、追问与回答）：[docs/interview.md](docs/interview.md)
- API：[docs/api.md](docs/api.md) · 上线说明：[docs/production-readiness.md](docs/production-readiness.md)

## 技术亮点

| 方向 | 实现 | 代码 |
|---|---|---|
| 限时抢购 | Lua 原子"时间窗 / 一人一单 / 扣库存 / 写 Stream"，同步链路不碰数据库；消费组异步落单，预生成订单号保证重复消费幂等；PEL 超时重投、死信、补偿；DB 条件扣减 + 唯一索引兜底；定时对账 | `internal/seckill` |
| 多级缓存 | 分段锁 LRU + Redis；singleflight 防击穿、空值防穿透、TTL 抖动防雪崩；延迟双删 + Pub/Sub 广播失效；Redis 故障降级回源 | `internal/pkg/cache` |
| 可靠延迟队列 | ZSET + Lua 原子领取、可见性超时重投、指数退避、死信；替代 30 秒全表扫描关单，保留低频兜底扫描 | `internal/pkg/delayqueue` |
| 分布式锁 | SET NX PX + Lua 校验释放/续期 + 看门狗 + fencing token；用于单实例定时任务与雪花节点租约 | `internal/pkg/lock` |
| 分布式限流 | Redis Lua 滑动窗口 / 令牌桶（Redis TIME 统一时钟），Redis 故障降级本地限流 | `internal/pkg/ratelimit` |
| MySQL | 条件更新 + 行锁防超卖；唯一键幂等；生成列让列表排序走索引；游标分页；OR 改 UNION ALL；死锁自动重试；GET_LOCK 互斥迁移 | `internal/infra/mysqlx`、`migrations/` |
| IM | 会话行锁分配 seq；推拉结合；Redis Pub/Sub 跨实例推送；在线状态 | `internal/platform/chat.go` |
| 会话 | Redis 会话缓存，吊销写墓碑 + 回填 SET NX，避免已退出会话被缓存复活 | `internal/platform/auth_session.go` |
| 发号 | 雪花订单号（修复时钟回拨重复 bug），节点号 Redis 租约自动分配 | `internal/pkg/snowflake` |
| 工程 | 配置校验、优雅退出、pprof、结构化访问日志/慢请求、后台任务 panic 恢复；每个测试独立库 + 独立 Redis 前缀，`-race` 全量通过；抢购压测与一致性校验工具 | `cmd/`、`internal/testenv` |

抢购压测（2 核共享环境，数据库/Redis/服务/压测端同机）：3000 用户抢 200 件，约 1560 req/s，P99 约 210ms，200 单全部落库、零超卖、无一人多单。

## 本地运行

需要 Docker（启动 MySQL/Redis）、Go 1.26.8（`go.mod` 声明了自动工具链）、Node.js 22。

**1. 启动依赖**（项目根目录）：

```powershell
docker compose -f compose.dev.yaml up -d --wait
```

**2. 后端**，PowerShell：

```powershell
cd backend
$env:DATABASE_DSN = 'campus:campus@tcp(127.0.0.1:3306)/campus'
$env:REDIS_ADDR = '127.0.0.1:6379'
$env:SEED_DEMO = 'true'
$env:AUTH_DELIVERY_MODE = 'local'
go run ./cmd/server
```

启动时自动执行数据库迁移。Linux/macOS/WSL 可以直接 `cd backend && make run`。

**3. 前端**：

```powershell
cd frontend
npm ci
npm run dev
```

打开 **http://127.0.0.1:5173**。前端通过同源代理访问后端 `127.0.0.1:8081`。

`SEED_DEMO=true` 只生成明确标记的展示商品，不能购买或联系卖家。注册两个自己的测试账号（本地验证码收件箱会直接显示验证码），即可体验发布、聊天、下单与取消。

**体验限时抢购**：登录后在浏览器打开 `http://127.0.0.1:5173/api/v1/auth/me` 查看自己的用户 ID，设置 `$env:ADMIN_USER_IDS = '<该ID>'` 后重启后端，进入「限时抢购」页点击「发起活动」，再用其他账号抢购。

## 测试与压测

```powershell
docker compose -f compose.dev.yaml up -d --wait
cd backend
$env:TEST_MYSQL_DSN = 'campus:campus@tcp(127.0.0.1:3306)/'
$env:TEST_REDIS_ADDR = '127.0.0.1:6379'
go vet ./...
go test ./...
```

集成测试为每个用例创建独立数据库（`campus_t_*`）和独立 Redis key 前缀，结束后清理；未设置上面两个环境变量时自动跳过。
Linux/WSL 可运行 `make test`（带 `-race`）。

抢购压测（服务端需信任本机转发的客户端 IP，压测工具会为每个虚拟用户带不同的 `X-Real-IP`）：

```powershell
# 终端 1：在上面后端环境变量基础上
$env:TRUSTED_PROXY_CIDRS = '127.0.0.1/32'
go run ./cmd/server
# 终端 2
cd backend
go run ./cmd/flashbench -users 3000 -stock 200 -c 200
```

前端：

```powershell
cd frontend
npm test
npm run build
npx playwright install chromium
npm run test:e2e
```

端到端测试会启动独立的 `18091/15183` 服务，使用独立的 `campus_e2e_<pid>` 数据库与 Redis 前缀，不调用真实收款、短信、邮件或模型。

## 目录

```text
backend/cmd/server/          应用入口（配置、迁移、后台任务、优雅退出）
backend/cmd/flashbench/      抢购压测与一致性校验
backend/internal/seckill/    限时抢购引擎
backend/internal/pkg/        缓存、分布式锁、限流、延迟队列、雪花 ID
backend/internal/infra/      MySQL（连接池/迁移/事务重试）、Redis
backend/internal/platform/   HTTP 与业务：身份、商品、订单、支付、IM、Agent、上传
frontend/src/components/     商城、抢购、登录/发布、订单、消息、Agent、结算
frontend/e2e/                双账号浏览器验收
deploy/                      Caddy、MySQL 开发初始化脚本
compose.yaml                 生产：Caddy + API + MySQL + Redis
compose.dev.yaml             本地开发依赖
```

## 部署

从项目根目录复制 `.env.example` 到 `.env`，设置已解析到服务器的 `DOMAIN` 以及 `MYSQL_PASSWORD`、`MYSQL_ROOT_PASSWORD`、`REDIS_PASSWORD`。然后运行：

```sh
docker compose up -d --build
docker compose ps
docker compose logs --tail=100 api web
```

Caddy 管理 HTTPS；MySQL、Redis、上传目录使用持久卷，且不对公网暴露端口。上线前请阅读 [支付、模型和生产差距](docs/production-readiness.md)，不能把镜像能启动等同于业务上线验收。
