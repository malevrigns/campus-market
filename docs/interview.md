# 面试讲解手册（Go 后端方向）

这份文档帮助你把项目讲清楚：先讲业务和整体架构，再挑 2–3 个亮点深挖，每个亮点都准备好"为什么这么做 / 替代方案 / 出问题怎么办"。
所有结论都能在代码和测试里找到对应实现，面试时可以直接说"这块我写了测试验证"。

> 诚实原则：这是校园场景的项目，没有真实线上大流量。压测数字来自本地环境，讲的时候说清楚测试条件，重点讲**设计思路和验证方法**，不要编造线上数据。

## 1. 一分钟介绍

> 校园二手交易平台，Go 模块化单体，MySQL + Redis。功能上有商品发布与搜索、订单与支付宝支付、买卖双方即时聊天、限时抢购和 AI 导购助手。
> 我重点做了三件事：
> 一是**限时抢购**，同步链路只走 Redis Lua 原子预扣库存，订单通过 Redis Stream 异步落库，数据库条件扣减和唯一索引兜底，配合失败补偿和库存对账，压测 3000 人抢 200 件零超卖、一人一单；
> 二是**多级缓存**，本地 LRU + Redis，处理了击穿、穿透、雪崩和缓存一致性，多实例之间用 Pub/Sub 广播失效；
> 三是把订单超时关单从定时扫表改成 **Redis ZSET 可靠延迟队列**，配合分布式锁和分布式限流，服务本身无状态、可以多实例部署。

## 2. 简历写法（可按需删减）

- 基于 Go + MySQL + Redis 实现校园二手交易平台（模块化单体），覆盖商品、订单、支付宝支付、IM、限时抢购与 AI 导购，服务无状态可水平扩容。
- **限时抢购**：Lua 脚本原子完成"时间窗校验 → 一人一单 → 扣减库存 → 写入 Stream"，同步链路不访问数据库；消费组异步落单，订单号预生成保证重复消费幂等，PEL 超时重投 + 死信 + 补偿，DB 条件扣减与唯一索引兜底，定时对账修正 Redis/DB 库存偏差；本地 2 核环境压测 3000 并发用户抢 200 件，约 1500 req/s、P99 约 210 ms，零超卖。
- **多级缓存**：分段锁 LRU + Redis 两级 Cache-Aside，singleflight 防击穿、空值缓存防穿透、TTL 随机抖动防雪崩，延迟双删 + Pub/Sub 广播保证多实例一致性，Redis 故障自动降级回源。
- **可靠延迟队列**：基于 ZSET + Lua 实现原子领取、可见性超时重投、指数退避与死信，替代 30 秒全表扫描关单，配合低频兜底扫描保证最终一致。
- **分布式组件**：Redis 分布式锁（token 校验释放、看门狗续期、fencing token），滑动窗口/令牌桶分布式限流（Redis 故障降级本地限流），Redis 租约分配雪花算法节点号。
- **MySQL**：InnoDB 事务与行锁防超卖，生成列 + 联合索引让列表排序走索引，游标分页解决深分页，唯一键实现接口幂等，死锁自动重试；集成测试每个用例独立建库，`-race` 全量通过。

## 3. 推荐讲解顺序

1. 画架构图（[architecture.md](architecture.md) 第一张图），说清楚"为什么是单体而不是微服务"。
2. 讲抢购链路（时序图），停在面试官感兴趣的地方深挖。
3. 讲缓存或延迟队列二选一。
4. 讲你怎么验证正确性：并发测试、压测工具、`-race`、故障场景测试。

---

## 4. 限时抢购

**代码**：`internal/service/flash.go` + `internal/repository/redis/flash.go`，HTTP 层 `internal/transport/http/flash_handler.go`，压测 `cmd/flashbench`，端到端测试 `internal/app/features_test.go`。

### 4.1 链路

```
令牌桶限流(用户 2/s、活动 2000/s) → 本地售罄标记 → Lua(时间窗/一人一单/扣库存/XADD) → 202
                                                                    ↓
                    Stream 消费组 → 事务(UPDATE stock=stock-1 WHERE stock>0 + INSERT orders) → XACK
```

### 4.2 高频追问

**Q：为什么用 Lua，而不是分布式锁或 WATCH/MULTI？**
Redis 单线程执行 Lua，"读库存→判断→扣减→记录资格→入队"整体原子，没有锁竞争，一次网络往返。
分布式锁会把所有请求串行化在一把锁上，吞吐极低；WATCH 是乐观锁，高并发下大量事务冲突重试。

**Q：同步扣 Redis、异步写 DB，怎么保证不超卖？**
三层：Redis Lua 原子扣减挡住绝大多数请求；落库时 `UPDATE flash_sales SET stock=stock-1 WHERE id=? AND stock>0` 是数据库层的条件扣减，即使 Redis 数据错乱也不会扣成负数（还有 `CHECK(stock>=0)`）；`UNIQUE(flash_sale_id, buyer_id)` 保证一人一单。测试 `TestNoOversellUnderContention` 用 400 个并发用户抢 50 件验证。

**Q：消息重复消费怎么办？**
订单号在 Lua 执行前预生成（雪花 ID），作为订单主键写入消息。重复消费时 `INSERT` 主键冲突，识别为"已处理"，直接 ACK，DB 库存扣减在同一事务里随之回滚，不会重复扣。见 `TestRedeliveryIsIdempotent`。

**Q：消费者处理一半崩溃了呢？消息会丢吗？**
不 ACK 的消息留在消费组的 PEL（Pending Entries List）。回收协程定期 `XPENDING` 找出空闲超过 30 秒的消息，`XCLAIM` 到自己名下重试；投递超过 5 次进死信流并补偿 Redis 库存。见 `TestCrashedConsumerMessageIsReclaimed`、`TestPoisonMessageGoesToDeadLetter`。

**Q：落库失败，用户的资格和 Redis 库存怎么办？**
分两种：业务失败（DB 库存不足、已经买过）→ ACK + 补偿；系统失败（DB 超时）→ 不 ACK，等重试。
补偿用 Lua "比较后修改"：只有资格仍是"该订单号 + 排队中"才改成失败并 `INCRBY` 库存，重复补偿不会多加库存。DB 库存不足导致的失败不归还 Redis 库存（说明 Redis 多放了，归还会更糟）。

**Q：Redis 宕机 / 数据丢了怎么办？**
抢购接口直接返回 503，**不降级直连数据库**，否则秒杀流量会瞬间压垮 DB。Redis 恢复后：预热任务每分钟把活动元数据写回（库存用 `SET NX`，不会覆盖进行中的库存）；对账任务发现库存 key 缺失时从数据库重新预热。资格数据丢失后用户可能再次抢到，但数据库唯一索引会拒绝，落库 worker 走补偿。见 `TestRejectedMessageCompensatesRedis`、`TestReleaseAndReconcile`。

**Q：对账怎么做，怎么避免误判？**
期望值 = DB 库存 − 仍在排队落库的资格数。对账读 DB 和读 Redis 之间消费者可能刚好落了一单，所以**同一个偏差连续两轮观察到**才修正，修正用 CAS 脚本（库存仍等于观察值才覆盖）。

**Q：为什么用 Redis Stream 而不是 Kafka/RocketMQ？**
项目已经依赖 Redis，Stream 有消费组、ACK、PEL、XCLAIM，满足"至少一次 + 多消费者竞争"。代价是持久化依赖 AOF（最多丢 1 秒）和容量受内存限制。如果业务规模再大、需要长时间堆积和回溯，会换 Kafka/RocketMQ，并用事务消息/本地消息表保证"扣 Redis 与发消息"的原子性（现在两者在同一个 Lua 里，天然原子）。

**Q：Stream 会无限增长吗？**
不能用 `XADD MAXLEN` 截断——积压时会把还没消费的订单删掉。做法是取"消费组已投递位置"和"PEL 最小 ID"的较小值，`XTRIM MINID` 只删已全部确认的前缀。见 `TestTrimKeepsUnackedMessages`。

**Q：本地售罄标记有什么用？会不会误判？**
库存抢完后还有大量请求，进程内 `sync.Map` 标记 500ms，直接返回"已抢光"，不打 Redis。只保留 500ms 是因为订单取消/超时会归还库存。已经抢到的用户重复点击时仍会读一次自己的资格（O(1) 的 HGET）。

**Q：热点行问题？**
落库 worker 并发更新同一活动的库存行，InnoDB 在这一行上排队。因为是异步、worker 数可控（每实例 4 个），不会出现成千上万个事务同时等锁。进一步优化：worker 批量拉取后合并扣减（一次 `stock=stock-N` + 批量插入），或者把库存拆成多行分段扣减。

**Q：怎么防刷？**
用户维度令牌桶（2/s）、活动维度令牌桶（削峰）、IP 限流、必须登录、一人一单。生产还可以加验证码/风控、活动前随机化抢购地址。

**Q：倒计时为什么要服务器时间？**
客户端时间不可信。列表接口返回 `server_time`，前端算偏移；开抢判断在 Lua 里用 `redis.call('TIME')`，所有实例看到同一个时钟。

## 5. 缓存

**代码**：`internal/pkg/cache/`，测试 `cache_test.go`。

**Q：缓存击穿 / 穿透 / 雪崩分别怎么处理？**
击穿：`singleflight` 合并同 key 的并发回源（测试：100 个并发只回源 1 次）。穿透：不存在的数据缓存空值 30 秒（还可以加布隆过滤器）。雪崩：TTL 加 0–20% 随机抖动；Redis 整体不可用时降级回源 + 限流保护数据库。

**Q：singleflight 有什么坑？**
一是第一个调用者的 context 被取消会让所有等待者一起失败——所以回源用 `context.WithoutCancel` + 独立超时；二是等待者也要能响应自己的取消——用 `DoChan` + `select ctx.Done()`；三是它只在进程内生效，N 个实例最多 N 次回源，需要更强保护时可以加分布式锁。

**Q：缓存和数据库一致性怎么保证？**
先更新数据库，再删缓存（不是更新缓存：并发写下更新顺序可能乱）。删缓存后还有一个竞态：读请求在更新前读到旧值，在删除之后才写回缓存。所以 500ms 后再删一次（延迟双删，测试 `TestDelayedDoubleDeleteRepairsStaleBackfill` 复现了这个竞态）。更彻底的方案是订阅 binlog（Canal）异步删缓存，代价是引入额外组件。所有缓存都有 TTL 作为最终兜底。

**Q：本地缓存多实例怎么一致？**
删除时 Redis Pub/Sub 广播，其他实例删除本地副本。Pub/Sub 不持久化，断线期间的消息会丢，所以 L1 TTL 只有 3 秒。

**Q：本地 LRU 为什么要分段？**
单把互斥锁在高并发读时是热点（LRU 的读也要移动链表，必须加写锁）。按 key 哈希拆成 32 段，各自加锁。有 `BenchmarkLRUParallel`。

**Q：会话也缓存了，退出登录怎么立即生效？**
吊销时写"墓碑"值而不是删除，回填用 `SET NX`。这样"读到有效会话 → 用户退出 → 回填"的竞态下，回填会失败。

## 6. 延迟队列（订单超时关单）

**代码**：`internal/pkg/delayqueue/`，业务 `orders.go expireOrder`。

**Q：为什么不用定时任务扫表？**
扫表时效差（30 秒粒度），订单表大了以后每次都要扫描，多实例还要防重复执行。延迟队列只在到期时处理具体订单。扫描仍然保留为每分钟一次的兜底（走 `(status, expires_at)` 索引，分布式锁保证单实例）。

**Q：怎么保证不丢、不重复处理？**
领取是 Lua 原子的"ZRANGEBYSCORE + ZREM ready + ZADD inflight"，多实例不会领到同一个任务。处理成功才从 inflight 删除（ACK）；进程崩溃时任务留在 inflight，可见性超时后被放回 ready。所以是"至少一次"，处理函数必须幂等：`SELECT ... FOR UPDATE` 锁订单，非 pending 直接返回。

**Q：和 RabbitMQ 死信队列 / RocketMQ 延迟消息 / 时间轮比？**
MQ 方案更适合大规模，但多一个中间件；时间轮是单机内存结构，进程重启任务就没了，需要持久化。ZSET 方案持久化在 Redis、天然支持多消费者，代价是轮询（空闲时 500ms 一次）和 Redis 内存。

**Q：用户刚付款，关单任务也到期了，会不会把已付款订单关掉？**
不会。关单和支付回调都先 `SELECT ... FOR UPDATE` 锁订单行，串行执行；关单时还检查"是否已打开支付通道"，打开过的交给支付对账（主动查单）决定，不直接关。

## 7. 分布式锁

**代码**：`internal/pkg/lock/`。

**Q：实现要点？**
`SET key token NX PX ttl`；释放和续期用 Lua 先比较 token 再操作，防止误删别人的锁（锁过期后被别人拿到，旧持有者再去 DEL）。看门狗每 ttl/3 续期，续期失败（锁丢了）取消业务 context。测试覆盖了互斥、旧持有者释放、看门狗、锁丢失取消。

**Q：Redlock？fencing token？**
单节点/主从 Redis 在主从切换时可能丢锁，Redlock 也有时钟和 GC 停顿的争议。所以这个项目的原则是：**锁只用来减少重复劳动，正确性靠数据库约束**——被锁保护的任务（对账、扫描）本身都是幂等的。加锁时 `INCR` 一个单调递增的 fencing token，需要强一致时下游存储可以拒绝更小的 token。

**Q：用在哪？**
多实例中只需一个执行的定时任务（`lock.Run`）、雪花算法节点号租约。

## 8. 限流

**代码**：`internal/pkg/ratelimit/`，入口 `middleware.go`。

**Q：几种算法的区别？**
固定窗口有临界突刺；滑动窗口日志（ZSET 记录每次请求）精确但内存随请求数增长，适合低频敏感接口（登录、验证码）；令牌桶允许突发、平滑限速，适合抢购入口和全局 IP 限流；漏桶强制匀速输出。

**Q：分布式限流的时间怎么取？**
Lua 里用 `redis.call('TIME')`，避免各实例时钟不一致。

**Q：Redis 挂了限流怎么办？**
降级为进程内固定窗口（每个实例各自限流），宁可放宽也不能因为限流组件故障导致全站不可用。

**Q：按 IP 限流有什么问题？**
校园网/公司网大量用户共用出口 IP（NAT），按 IP 卡太紧会误伤。所以 IP 阈值较宽，精细限流放在登录后的用户维度。

## 9. MySQL

**Q：下单为什么不用 `SELECT ... FOR UPDATE` 再判断？**
条件更新 `UPDATE ... WHERE status='available'` 本身就是当前读 + 行锁，一条语句完成"判断 + 修改"，影响行数为 0 就是被别人抢了，比先锁再判断少一次往返。

**Q：快照读和当前读？**
RR 隔离级别下普通 `SELECT` 读事务开始时的快照（MVCC），看不到并发事务刚提交的修改；`UPDATE`/`SELECT ... FOR UPDATE` 是当前读。项目里几个地方专门用了当前读：校验验证码（防止一码两用）、登录时检查密码是否刚被修改、支付回调/关单/取消锁订单行。

**Q：幂等为什么"先插入、冲突再查"，而不是"先查、没有再插"？**
先查后插有并发窗口；RR 下用 `SELECT ... FOR UPDATE` 锁一个不存在的行会加间隙锁，两个事务都加了间隙锁再插入会互相等待形成死锁。直接 `INSERT` 靠唯一索引裁决最简单。

**Q：死锁怎么处理？**
InnoDB 检测到死锁会回滚其中一个事务（1213）。`mysqlx.WithTx` 对 1213/1205 做指数退避重试（最多 3 次）。测试 `TestDeadlockIsRetried` 构造了交叉加锁的死锁。根本上还要保证加锁顺序一致、事务尽量短。

**Q：列表的索引为什么要生成列？**
首页查询是 `status IN ('available','reserved') ORDER BY created_at DESC`。`IN` 两个值时 `(status, created_at)` 索引无法直接提供全局有序，会 filesort。生成列 `listed` 把条件变成等值 `listed=1`，`(listed, created_at, id)` 就能顺序扫描索引直接取前 24 条。

**Q：深分页？**
`LIMIT 10000, 24` 要先扫 10024 行再丢弃。游标分页用上一页最后一条的 `(created_at, id)` 做条件，翻多深都只扫一页；新数据插入也不会导致翻页重复。多取 1 条判断 `has_more`，不依赖 COUNT。

**Q：`buyer_id=? OR seller_id=?` 有什么问题？**
OR 跨两个索引列时优化器要么全表扫描、要么 index_merge。改写成两个各自走索引的子查询 `UNION ALL` 再排序。

**Q：清理过期数据要注意什么？**
不要一次 `DELETE` 几十万行（大事务、长时间持锁、主从延迟），分批 `LIMIT 1000` 循环。原代码在每次登录时顺带范围删除过期会话，并发登录会争抢间隙锁，已移到后台任务。

**Q：连接池参数怎么定？**
`MaxOpenConns` 受 MySQL `max_connections` 和实例数约束（例如 500 / 实例数，留余量）；`ConnMaxLifetime` 小于服务端 `wait_timeout`，避免拿到被服务端关闭的连接；`interpolateParams=true` 在 utf8mb4 下安全，可以把 prepare + execute 两次往返降为一次。

**Q：订单号为什么用雪花 ID？**
InnoDB 聚簇索引按主键组织，随机字符串主键会导致页分裂和随机 IO；雪花 ID 趋势递增，顺序追加。原实现有个 bug：时钟回拨 ≤5ms 时沿用上次时间戳但没有递增序列号，会生成重复 ID，已修复并有回归测试。节点号用 Redis 租约分配，多实例不需要手工配置。

## 10. IM

**Q：消息 seq 怎么保证单调不重复？**
`UPDATE conversations SET seq=seq+1` 拿到会话行锁 → 同事务读出新 seq → 插入消息。同一会话的并发发送在行锁上排队；`UNIQUE(conversation_id, seq)` 兜底。测试：双方并发发 20 条，seq 恰好 1..20。也可以用 Redis `INCR` 分配 seq，但要处理 Redis 与 DB 的一致性。

**Q：多实例怎么推送？**
WebSocket 只推"有新消息"的信号，客户端拿 seq 游标去拉（推拉结合，推送丢了不丢消息）。实例之间用 Redis Pub/Sub 广播，每个实例只投递给自己持有连接的用户。测试 `TestRealtimeAcrossInstances` 用两个实例验证。用户量更大时可以维护"用户 → 实例"路由表，只发给目标实例。

**Q：客户端重发怎么去重？**
`(conversation_id, sender_id, client_msg_id)` 唯一，重复发送返回原消息。

## 11. Go 语言相关

- **优雅退出**：`signal.NotifyContext` → `http.Server.Shutdown` 等在途请求 → 取消后台任务 context → `WaitGroup` 等待退出；正在处理的消息用 `context.WithoutCancel` + 独立超时尽量完成。
- **goroutine 泄漏防护**：所有后台循环都 `select ctx.Done()`；`time.AfterFunc` 的延迟删除在进程关闭后不再执行（`cache.Close`）；后台任务 panic 被 `recover` 并自动重启。
- **context**：请求 context 只用于请求本身；需要"请求取消后仍要完成"的操作（ACK、补偿、缓存回填）用 `context.WithoutCancel`。
- **并发正确性**：全部测试 `go test -race` 通过；并发场景都有专门测试（抢购、下单、发消息、限流、锁、延迟队列多消费者）。
- **排查工具**：`PPROF_ADDR` 打开 pprof；压测时抓 CPU profile 发现服务进程只用了约 0.2 核，瓶颈在同机的数据库/压测客户端，而不是 Go 服务本身。

## 12. 测试与验证

| 层次 | 内容 |
|---|---|
| 单元/组件 | 锁、限流、延迟队列、缓存、雪花算法（真实 Redis） |
| 集成 | 每个用例独立 MySQL 库 + 独立 Redis 前缀，可并行；覆盖下单防超卖、幂等、支付回调验签与重放、对账、IM、Agent、抢购全流程 |
| 故障场景 | 消费者崩溃、毒消息、重复投递、Redis 数据丢失、锁丢失、Redis 不可用降级 |
| 压测 | `cmd/flashbench`：批量造用户与会话 → 并发抢购 → 统计 QPS/延迟分位/状态码 → 等待落库 → 校验订单数、DB/Redis 库存、一人一单 |
| CI | MySQL 8.4 + Redis 7.4 服务容器，`gofmt`/`vet`/`-race` 测试/抢购一致性门禁/前端构建/E2E/镜像构建 |

## 13. 被问"还能怎么优化"

- 抢购：按活动分 Stream；worker 合并扣减；库存分段；限流和抢购合并进一个 Lua 减少往返。
- 缓存：binlog 订阅（Canal）删除缓存；热 key 探测 + 本地缓存加长；大 key 拆分。
- 搜索：MySQL ngram 全文索引或 Elasticsearch。
- 可观测：Prometheus 指标（缓存命中率、队列积压、Stream lag 已在 `/api/v1/admin/runtime` 暴露）、OpenTelemetry 链路追踪。
- 高可用：Redis Sentinel/Cluster（Cluster 下活动相关 key 用 hash tag 放同一 slot）、MySQL 主从 + 读写分离（注意主从延迟对"写后读"的影响）。
- 数据量大后订单按买家 ID 分库分表，卖家视角用冗余表或 ES。
