# 后端分层重构说明

本次重构把 `backend/internal/platform` 这个“HTTP + 业务 + SQL”混合包拆成明确的应用层、业务层、契约层和数据适配层，
同时保持现有 HTTP 契约、数据库 schema、Redis key、后台任务语义与部署方式不变。

## 1. 目标结构

```text
backend/
├── cmd/server                 # 唯一应用入口：配置 → 基础设施 → 组合根 → HTTP/后台任务
├── cmd/flashbench             # 抢购压测：直接使用新的仓储与 Redis 闸门
├── cmd/testfixture            # E2E 数据库初始化
└── internal/
    ├── transport/http         # 应用层：路由、中间件、请求 DTO、身份、响应与错误映射、SSE/WS
    ├── service                # 业务层：用例编排、业务规则、限流、事务边界、领域事件
    ├── port                   # 能力契约：仓储、事务、缓存、推送、支付、验证码、LLM、MCP
    ├── domain                 # DO：实体、枚举、值对象、业务错误
    ├── repository/mysql       # MySQL 适配器：SQL、持久化模型映射、错误翻译
    ├── repository/redis       # Redis 适配器：缓存、会话、在线状态、热度、抢购 Lua/Stream、推送
    ├── adaptor/alipay         # 支付宝 RSA2 签名/验签/查单/关单
    ├── adaptor/verification   # SMTP / 阿里云短信
    ├── adaptor/llm            # OpenAI 兼容流式模型
    ├── adaptor/mcp            # MCP Streamable HTTP 客户端
    ├── adaptor/media          # 图片校验、重编码与读取
    ├── job                    # 后台任务编排（每实例任务 + 分布式锁选主任务）
    ├── app                    # 组合根；集成测试也通过它装配
    ├── infra                  # 连接池、迁移、事务重试等资源提供者
    ├── pkg                    # cache/lock/ratelimit/delayqueue/snowflake/idgen/toolschema
    └── testenv                # 每用例独立 MySQL 数据库与 Redis 前缀
```

## 2. 各层职责

| 层 | 负责 | 不负责 |
|---|---|---|
| `transport/http` | HTTP 方法/路径、严格 JSON 绑定、Cookie、状态码、SSE/WS 编码、入口限流与 CSRF | 业务判断、SQL、Redis 命令、第三方签名 |
| `service` | 用例顺序、业务校验、幂等/并发语义、事务边界、事务提交后的缓存/推送/定时任务 | HTTP 状态码、SQL、Redis key、密钥 |
| `port` | 业务需要的能力：输入、输出、错误与原子性语义 | 具体实现、框架类型 |
| `repository/*` | SQL/Lua/Stream、持久化模型 ↔ DO 映射、把存储错误翻译为领域错误 | 业务规则、HTTP |
| `adaptor/*` | 协议、签名、流解析、供应商错误收敛 | 业务状态机 |
| `app` | 选择具体实现、构造顺序、资源所有权、启动/关闭顺序 | 业务逻辑 |

`transport` 的 controller 持有具体 service（与参考项目相同的取舍），但 service 只依赖 `port` 中的接口；
仓储与适配器由 `app` 在构造期注入。接口不自动带来可替换性：替换实现仍需在组合根选择，并满足契约声明的错误、原子性与副作用。

## 3. 核心能力契约

| 契约 | 业务语义 | MySQL/Redis 实现 |
|---|---|---|
| `port.AuthStore` / `AuthTx` | 账号、身份、验证码原子消费、会话创建/吊销 | `repository/mysql.AuthRepo` |
| `port.CatalogStore` / `CatalogCache` / `RankStore` | 商品查询与分页、发布/下架、收藏、缓存、热榜 | `CatalogRepo` + `repository/redis.CacheStore/HeatStore` |
| `port.OrderStore` / `OrderTx` | 下单幂等、库存预占、状态机、超时关单、抢购落库 | `OrderRepo` + 事务 `Tx` |
| `port.PaymentStore` / `PaymentTx` / `PaymentGateway` | 支付会话、回调幂等、主动查单、库存释放 | `PaymentRepo` + `adaptor/alipay` |
| `port.ChatStore` / `ChatTx` / `PresenceStore` / `LiveNotifier` | 会话成员校验、会话内 seq、已读水位、在线状态、跨实例推送 | `ChatRepo` + Redis 会话/在线/LiveHub |
| `port.AgentStore` / `AgentTx` / `MCPGateway` / `LLMClient` | 助手目录、发布快照、运行幂等、工具授权、流式模型 | `AgentRepo` + `adaptor/mcp/llm` |
| `port.FlashStore` / `FlashCache` / `FlashRedis` | 活动 CRUD、Redis 闸门、资格、Stream 消费/重投/死信、对账 | `FlashRepo` + Redis Lua/Stream |
| `port.SessionCache` / `RateLimiter` / `TimeoutQueue` / `MediaStore` | 会话缓存与墓碑、限流、延迟队列、图片存储 | Redis/延迟队列/文件系统适配器 |

### 事务归属

| 用例 | 事务所有者 | 说明 |
|---|---|---|
| 注册/登录/改密/绑定 | `AuthService` 通过 `AuthStore.InTx` | 验证码消费由 `AuthStore.ConsumeCode` 原子完成；错误尝试先提交计数再返回业务错误 |
| 创建/取消/流转/退款/超时关单 | `OrderService` 通过 `OrderStore.InTx` | 库存预占与订单/事件同事务；缓存、推送、延迟任务在提交后执行 |
| 支付回调/对账 | `PaymentService` 通过 `PaymentStore.InTx` | 锁订单、核对金额/交易号、写回调、更新订单与库存同事务 |
| 发送消息 | `ChatService` 通过 `ChatStore.InTx` | `UPDATE seq=seq+1` 拿会话行锁，同事务插入消息 |
| 抢购落库 | `FlashService` 通过 `OrderStore.InTx` | 条件扣库存 + 预生成订单主键插入，重复消费天然幂等；Redis 补偿在事务后 |
| 助手草稿/发布/归档 | `AgentCatalogService` 通过 `AgentStore.InTx` | 乐观锁 revision + 不可变版本快照 + 审计 |
| 创建 Agent 运行 | `AgentRunService` 通过 `AgentStore.InTx` | 新线程/绑定/运行/快照同事务，提交后才开始流式输出 |

## 4. 构造链与请求链

构造链：

```text
main → config.Load → mysqlx.Open/Migrate + redisx.Open
     → app.New
       → mysqlrepo.New(Stores)
       → redisrepo.New(Repos) + Limiter + TimeoutQueue
       → alipay / verification / llm / mcp / media 适配器
       → Auth/Catalog/Order/Payment/Chat/Flash/AgentCatalog/AgentRun/Admin 服务
       → httpapi.New(路由 + 依赖)
       → job.Runner
     → SeedDemo（可选）→ 启动 workers → HTTP Server
```

普通下单请求链：

```text
POST /api/v1/orders
→ httpapi 中间件（请求 ID/CSRF/入口限流/Body 上限）
→ OrderHandler 绑定 DTO、读取会话用户
→ OrderService.Create（幂等键、限流、订单号）
→ OrderStore.InTx
  → OrderTx.LockProduct → ReserveProduct → InsertOrder → InsertEvent
→ 提交后：商品缓存失效 → 延迟队列 → 热度 → WebSocket 通知
→ 应用层输出 {code:0,msg:"OK",data:Order}
```

抢购请求链：

```text
POST /flash-sales/{id}/attempts
→ FlashService（用户/活动令牌桶、活动元数据缓存、自购校验、订单号）
→ FlashRedis.Attempt（Lua：时间窗→一人一单→扣 Redis 库存→XADD）
→ 202 {state:"queued",order_id}
后台：FlashService.Run → XREADGROUP → OrderStore.InTx（DB 条件扣库存 + INSERT 订单）
→ Redis q→ok / q→fail（补偿）→ 超时任务/WebSocket；PEL 超时 XCLAIM，超限死信 + 补偿
```

Agent 请求链：

```text
POST /agent/runs
→ AgentRunService.Stream（校验/幂等/发布快照/运行中限制）
→ AgentStore.InTx（线程/绑定/运行/快照）
→ sink("start") → llm.Stream（delta）→ 工具调用（内置仓储或 MCP）
→ 持久化终态 → sink("error"/"done")
→ httpapi 只负责 SSE 编码与 Flush
```

## 5. 兼容性

- HTTP 路径、方法、成功/失败状态码与 `{code,msg,data}` 包装保持不变；Cookie 名、CSRF、入口限流、body 上限保持不变。
- 数据库 schema、迁移、索引、唯一约束与幂等键语义保持不变；没有新增表或列。
- Redis key 前缀、缓存 key、会话墓碑、在线状态、热度 ZSET/HLL、抢购 key/Stream/group/DLQ、雪花租约保持不变，旧数据可继续使用。
- 后台任务名称与周期保持不变；多实例运行/选主语义保持不变。
- 前端无需改接口；`docs/api.md` 与新增 `docs/openapi.yaml` 是同一契约的两种表达。

## 6. 功能扩展影响矩阵（示例：新增“管理员订单分页查询”）

| 层 | 改动 |
|---|---|
| DTO / 应用层 | 新增 `GET /api/v1/admin/orders` 查询参数与响应；管理员中间件；错误映射复用 |
| 业务层 | `OrderService.ListAdmin(ctx, actor, query)`：权限、分页边界、稳定排序、可见范围 |
| 契约 | `OrderStore` 新增 `ListForAdmin(ctx, query)`；若不想扩大现有接口，可新增聚焦接口 |
| MySQL 适配器 | 新增筛选/COUNT/分页 SQL 与索引检查；返回 `[]Order,total` |
| 装配 | 复用现有 `OrderRepo` 实例并注册路由；不需要新资源 |
| 验证 | 分页边界、空结果、权限拒绝、总数一致性；现有下单/取消测试保持通过 |
| 兼容性 | 新增接口不破坏旧调用；向 Go 接口加方法会影响所有实现者与测试替身 |

## 7. 微服务演进

当前仍是模块化单体。抢购落库已经通过 Redis Stream 与请求链路解耦，是最适合独立部署的模块；
订单/支付/库存的权威写入仍在 MySQL 事务中。若拆分，应保持前端 `/api/v1` 契约由网关/BFF 维持，
把 `OrderStore`/`PaymentStore` 的本地能力替换为远程客户端，并为超时、重试、幂等键、事件重复/乱序和最终一致性单独设计；
Go 接口本身不会提供这些分布式语义。详见 `backend-skill` 的微服务演进参考。

## 8. 验证

```sh
cd backend
gofmt -l .
go vet ./...
go test -count=1 ./...
go test -race -count=1 ./...   # 需要 TEST_MYSQL_DSN / TEST_REDIS_ADDR
go build ./cmd/server
```

本次重构在 WSL Ubuntu + Go 1.26.8 + MySQL 8.4 + Redis 7.4 环境完成了：

- `gofmt -l .` 无输出；`go vet ./...` 通过；
- `go test -count=1 ./...` 全量通过（含 HTTP、Auth、订单、支付回调/对账、IM、Agent/MCP、抢购端到端，以及重投幂等/失败补偿/PEL 回收/死信/Trim 的抢购底层测试）；
- `CGO_ENABLED=1 go test -race -count=1 ./...` 全量通过；
- `cmd/flashbench -users 1000 -stock 100 -c 100`：100 人抢到、100 张订单落库、DB/Redis 库存归零、无一人多单，`PASS`。

Windows 本机没有 gcc 时 `-race` 无法构建，这是 cgo 工具链限制，不是测试失败；CI 的 Linux runner 会执行 `make test` 中的 `-race`。
