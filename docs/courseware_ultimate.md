## 【套课】Go微服务实战：商城 × AI Agent × IM 生产级全栈项目

独立仓库：`edu.mall.backend` · `edu.mall.customer` · `edu.mall.frontend` · `edu.websocket` · `edu.webchat` · `edu.webchatui` · `edu.agent.code`

# Part 0 工程地基与课程契约

## 0.1 业务全景与数据流

五大子系统不是五个 Demo，而是一条生产链路：

| 子系统 | 仓库示例 | 核心职责 | 生产硬指标 |

| --- | --- | --- | --- |
| 商城 | edu.mall.backend / edu.mall.customer / edu.mall.frontend | 商品、订单、购物车、RBAC、多端登录 | 支付回调幂等、订单状态机、越权负例测试 |
| WebSocket 连接层 | edu.websocket | 只做推送与在线状态 | OpenResty + Nchan 扛长连接，单机 4 万连接压测 |
| IM | edu.webchat / edu.webchatui | 好友、群聊、会话、消息全链路 | 消息 seq、多端同步、离线拉取、已读回执可控 |
| AI Agent | edu.agent.code | Eino 原子能力、多轮对话、工具调用、SSE、RAG、人机协作 | 能力后台组合、改完灰度生效、中断可恢复 |
| 微服务融合直播 | 跨仓库集成 | gRPC 统一鉴权、IM 用户体系改造、群机器人 @ 触发 Agent、前端五入口、Docker Compose + CICD | 任一服务回滚后身份与消息链路仍一致 |

统一数据流：TextCopy```
用户/浏览器/移动端
   │
   ├─ React19 前端：商城前台 / 商城管理后台 / webchatui / Agent 控制台 / 融合入口
   │
   ├─ OpenResty + Nchan：长连接接入、首包鉴权、在线状态、推送出口
   │
   ├─ edu.websocket：go-zero zrpc，仅暴露 WebSocketSend / IsOnlineUsers / GetOnlineCount
   │       └─ Redis Pub/Sub：跨进程广播
   │
   ├─ edu.mall.backend：商品/订单/购物车/RBAC/多端登录/支付回调幂等状态机
   │
   ├─ edu.webchat：IM 好友/群/会话/消息，gRPC 调用统一鉴权 backend
   │
   └─ edu.agent.code：Eino Agent、工具/MCP、Milvus RAG、SQLite/OTel、人机协作中断恢复
             │
             └─ 不自己签发 token，透传 backend 统一验证；群机器人 @ 触发时按绑定关系收窄 Scope
```

## 0.2 仓库、分支、版本与环境矩阵

| 项 | 约定 |
| --- | --- |
| 仓库策略 | 五大系统独立仓库；共享契约只允许放在接口包/文档，不复制业务实现 |
| 分支模型 | main 可发布；develop 集成分支；feature/*、fix/*、hotfix/*；PR 必须过 CI |
| 版本标签 | v<major>.<minor>.<patch>；破坏性契约变更必须升 minor 并写迁移说明 |
| 环境 | local / dev / staging / prod；配置即代码；密钥只进环境变量或密钥服务 |
| Go | 统一 go 1.25.x；开启 go vet、staticcheck、gofumpt、govulncheck |
| 前端 | 统一 React 19；课程历史代码若出现 React 18，只在迁移章节讲解差异，不作为新代码基线 |
| 协议 | HTTP/JSON、WebSocket、SSE、gRPC；统一错误码；统一响应 {code,msg,data} |
| 日志 | 结构化 JSON；必须含 trace_id；禁止打印敏感信息 |
| 追踪 | OpenTelemetry Trace 贯穿 HTTP/gRPC/Redis/MySQL/Milvus/SQLite |

## 0.3 统一身份契约：Principal / Token / JWKS / Scope

IM 与 Agent 不自己签 token；backend 是唯一强校验方。| 概念 | 示例口径 | 说明 |

| --- | --- | --- |
| Principal | Subject、TenantID、Scopes、Via、Exp、Kid | 表示“谁在什么上下文拥有哪些能力” |
| Audience | mall、im、agent、websocket | token 必须有目标受众，禁止跨系统裸用 |
| Scope | order.read、order.refund、message.send、agent.run、agent.tool.sql_readonly | 默认拒绝；显式授权 |
| JWKS | kid -> public_key | backend 发布；服务侧缓存并后台刷新 |
| 机器人身份 | bot:agent-7 + bot_binding | 群机器人不是用户全权代理，只能按绑定关系行动 |

统一响应示例：JSONCopy```
{
  "code": 0,
  "msg": "ok",
  "data": {}
}
```

错误码建议：

| code | 含义 | 处理 |

| --- | --- | --- |
| 0 | 成功 | 正常返回 |
| 1001 | 参数错误 | 400，返回字段级错误 |
| 1002 | 未认证 | 401，刷新或重新登录 |
| 1003 | 无权限 | 403，审计留痕 |
| 1004 | 资源不存在 | 404 |
| 1005 | 冲突/幂等 | 409 或返回已存在资源 |
| 1006 | 依赖不可用 | 502/503，触发降级 |
| 1007 | 限流 | 429，带重试建议 |
| 2001 | 支付状态机非法 | 业务错误，记录审计 |
| 3001 | Agent 策略拒绝 | 返回可解释原因 |

Trace 上下文要求：TextCopy```
trace_id 由入口生成或从上游继承；
HTTP: `X-Trace-Id` / W3C Traceparent；
gRPC: metadata `x-trace-id` / OpenTelemetry propagator；
日志字段必须包含：trace_id, span_id, service, env, principal_hash, latency_ms。
```

## 0.4 CI/CD 门禁

| 阶段 | 门禁 |
| --- | --- |
| pre-commit | format、lint、import 分组、禁止提交密钥扫描 |
| CI | go test ./...、race 检测、覆盖率阈值、contract test、gosec/govulncheck |
| 集成 | Docker Compose 起依赖；跑迁移；跑关键链路冒烟 |
| CD | build image -> push registry -> staging 部署 -> 自动验收 -> 灰度 -> prod |
| 回滚 | 保留上一版本镜像；DB 迁移必须可回退或可补偿；回滚 SOP 一页纸 |

## 0.5 SLO、告警与故障演练模板

| 系统 | SLO 示例 | 核心告警 |
| --- | --- | --- |
| 商城 | 下单可用性 99.9%；支付回调处理延迟 P95 < 2s | 回调积压、状态机非法、库存不一致 |
| WebSocket | 推送成功率 99.95%；连接抖动 P99 可控 | 连接数突降、Pub/Sub 延迟、慢消费者 |
| IM | 消息发送成功率 99.95%；seq 空洞告警 | 离线积压、回执风暴、敏感词队列阻塞 |
| Agent | SSE 首 token 延迟 P95 < 3s；工具失败率可解释 | run 卡死、策略拒绝突增、RAG 引用缺失、成本突增 |

故障演练必做项：发布中断连、Redis 抖动、MySQL 主从延迟、Milvus 慢查询、JWKS 刷新失败、支付网关重复回调、Agent 工具超时。## Part 0 Definition of Done

- 能一键 `docker compose up` 拉起依赖。
- 五个服务都能输出带 `trace_id` 的结构化日志。
- 任一服务都能演示“未认证、错 Audience、缺 Scope”三类拒绝。
- CI 红灯时不能合并；CD 必须可回滚。

# Part 1 商城：从单体到可拆分边界（课时 1–109，18 章）

## 1.1 官网课时结构映射

| 课时范围 | 章节 | 手册学习重点 |
| --- | --- | --- |
| 1–12 | 基础准备 | 环境、版本矩阵、仓库结构、跑通骨架 |
| 13–24 | 技术方案设计 | PRD → 技术方案评审一/二/三；边界、术语、非功能指标 |
| 25–38 | 后端框架开发 | 分层 router -> api -> service -> dao；统一响应；错误码 |
| 39–47 | 管理后台登录 | 登录态、验证码、RBAC 前置、审计 |
| 48–56 | RBAC | user/role/permission 模型；菜单/按钮/接口三级权限 |
| 57–66 | 录播管理 | 内容资源建模；与虚拟商品 SKU 的关系 |
| 67–76 | 课程管理 | 课程作为商品 SPU 的权益边界；上架/下架 |
| 77–88 | 用户注册登录 C 端 | 多端登录、验证码、密码安全、登录设备管理 |
| 89–98 | 商品列表 | SPU/SKU、搜索、筛选、分页游标、库存展示口径 |
| 99–126 | 用户订单 | 28 节，全课程交易核心：创建、取消、支付、履约、售后 |
| 127–139 | 购物车 | 加购、勾选、价格快照、失效策略 |
| 140–149 | 用户管理 | C 端资料、地址、账号安全、风控基础 |
| 150–170 | 部署发布 | Docker Compose、CICD、迁移、回滚、监控 |
| 171–196 | 实战修复/加餐常态 | bug 修复、性能优化、资损防线、面试串讲 |
| 197–230 | 高阶扩展建议 | 订单超时关单、支付对账、补偿任务 |
| 231–280 | 拆分前边界治理 | 防腐层、领域事件雏形、避免跨库 join |
| 281–301 | 与 IM/Agent 的契约 | 商品问答、订单通知、机器人只读工具 |
| 302–317 | 融合直播回放 | 统一鉴权后商城身份被 IM/Agent 复用 |

虚拟商品/内容权益 SKU## 1.2 核心数据表设计要点表（示例）

| 域 | 表示例 | 设计要点 | 一致性 |
| --- | --- | --- | --- |
| 用户 | user_account、user_auth、user_login_device | user_auth 支持 password/phone/wechat 等；多端登录用 device/session 管理 | 密码只存 hash；登录事件审计 |
| RBAC | role、permission、role_permission、user_role | 权限点支持 api:order:list；菜单/按钮/接口三级映射 | 禁止把角色名写死进代码 |
| 商品 | spu、sku、sku_stock、sku_price_log | SKU 唯一约束；价格快照进订单；库存独立表 | 防超卖；价格变更留痕 |
| 内容权益 | course_spu、lesson、content_asset、user_course_entitlement | 课程/录播是权益，不等同普通实物 | 退款后回收权益；开通幂等 |
| 购物车 | cart_item | (user_id, sku_id) 唯一；记录 snapshot_price | 下单必须重新取价 |
| 订单 | order_main、order_item、order_status_log | 订单只存快照；状态机单向推进 | 状态迁移必须写日志 |
| 支付 | payment_order、payment_callback_record | callback_id/out_trade_no 幂等唯一；金额币种校验 | 验签 + 验金额 + 验状态 |
| 地址/物流 | user_address、order_delivery | 虚拟商品可弱化物流，但不能删字段语义 | 快照地址，避免用户改地址影响历史订单 |


> 📚 **完整真实课时清单**（依据官网目录逐节核对）：
## 1.1 项目基础准备（7 节）
产品需求分析 / 技术文档框架 / MySQL 使用 / git 协作流程 / 服务配置与环境变量。**先写 PRD 和技术文档骨架再动手**。

## 1.2 技术方案设计（7 节）
需求分析 → PRD 完善 → **技术方案评审一/二/三**（分三次收敛架构）→ 整体技术方案设计 → 整体技术方案讲解。**评审驱动设计**是老师标志性习惯。

## 1.3 后端框架开发（5 节）
框架初始化 → 分层 `router→api→service→dao` → **统一响应结构 `{code, msg, data}`** 与错误码规范。

## 1.4 管理后台登录（3 节）
登录页 → 登录接口 → JWT 签发与校验。**这是全课第一个 Principal 落地处**（呼应 Part 0 统一身份）。

## 1.5 RBAC 权限体系（4 节）
RBAC 概念与模型 → 权限表设计（`user_role/role_perm/perm`，**不用角色字符串硬编码**）→ 权限中间件 → 菜单动态渲染。

## 1.6 录播视频管理（6 节）
gentool 生成 model → CRUD → 上传腾讯云对象存储 → 列表分页 → 详情。**对象存储接入在此首次出现**（IM 会复用）。

## 1.7 课程管理（5 节）
课程表设计与 gentool → 课程 CRUD → 课程分类 → 上下架状态机。

## 1.8 C端用户注册登录（5 节）
C 端注册 → 手机号+验证码登录 → **多端登录与 token 刷新**（user_auth 支持多端/多登录方式）。

## 1.9 商品列表（3 节）
商品/SKU 查询接口 → 分页（游标防深翻页）→ 商品详情（价格快照）。

## 1.10 用户订单（28 节 ★最重）
下单（雪花 ID 订单号，二节）→ 订单超时取消（延迟队列/定时任务）→ 支付查询/权益交付/发起支付/主动取消/订单列表 → 接口完善优化/详情/已购优化/管理后台统计 → **退款四节** → 微信支付回调处理 → 微信支付退款回调处理（异步回调幂等、状态机流转）。

### 📊 商城核心数据表设计要点（Kimi 补强）

| 表 | 设计要点 |
|----|---------|
| spu / sku | 分离；sku 承载库存、价格、规格 |
| order / order_item | 一对多；order_item 存价格快照 |
| order_status_log | 状态机留痕，禁止逆向跳 |
| payment_callback | **幂等键唯一约束**（outTradeNo），防重放 |
| cart | 按 user_id + sku_id 唯一约束 |
| user_auth | 多端/多登录方式，token 短时 + refresh |
| RBAC | user_role / role_perm / perm 三表 |

### 🔌 关键接口签名示例
- `POST /api/v1/orders` → `CreateOrder(ctx, uid, items, idempotencyKey)`
- `POST /payments/notify` → `HandleNotify(ctx, outTradeNo, sign, status)`（验签**且验金额**）
- `GET /api/v1/skus?cursor=` → 游标分页

### 🎯 高频面试考点 & ⚠️ 易错点
- **考点**：超卖、库存扣减顺序、订单状态机、支付回调幂等、分布式事务、RBAC 模型、深翻页。
- **易错**：库存先扣后付导致资损；回调只验签不验金额；状态机允许逆向跳；购物车用客户端价格；权限用菜单码硬编码。

> **DoD（Part 1）**：支付回调重放不坏单；越权访问有负例测试；退款后权限正确回收；qingfly 商用边界（合规/隐私/日志脱敏）明确。


---
---
## 1.3 关键接口签名示例（Go）

goCopy```
type MallService interface {
    CreateOrder(ctx context.Context, req CreateOrderReq) (*OrderResp, error)
    CancelOrder(ctx context.Context, req CancelOrderReq) (*OrderResp, error)
    GetOrderDetail(ctx context.Context, req OrderDetailReq) (*OrderDetailResp, error)
    ListOrders(ctx context.Context, req OrderListReq) (*OrderListResp, error)
}

type CreateOrderReq struct {
    UserID         int64
    CartItemIDs    []int64
    AddressID      int64
    IdempotencyKey string
}

type PaymentCallbackReq struct {
    OutTradeNo string
    CallbackID string
    Amount     int64
    Currency   string
    Status     string
    Sign       string
}

type PaymentService interface {
    HandlePaymentCallback(ctx context.Context, req PaymentCallbackReq) error
    CloseExpiredOrders(ctx context.Context, before time.Time) (int, error)
}
```

统一分层：TextCopy```
router        # 只解析参数、绑定上下文、写响应
api           # DTO/校验/统一响应 code,msg,data
service       # 业务规则、状态机、事务边界、幂等
dao           # GORM 数据访问；复杂查询写清索引假设
model         # gentool 生成；手工改字段必须有迁移文件
```

## 1.4 高频面试考点

- 下单防超卖：库存扣减用条件更新还是事务？示例：`UPDATE sku_stock SET stock=stock-? WHERE sku_id=? AND stock>=?`。
- 支付回调幂等：为什么“验签”不够？还要验金额、商户单号、状态机。
- 订单状态机：哪些状态可逆？取消/超时关闭/退款/售后如何留痕？
- 购物车与订单价格不一致：下单必须重新计算价格并锁定快照。
- RBAC：为什么不用字符串角色判断？如何做按钮级与接口级权限？
- 深分页：为什么 `offset` 越大越慢？游标分页如何做稳定排序？
- 虚拟商品：退款后权益回收、学习进度是否保留、发票如何处理。

## 1.5 易错点

- 用客户端提交价格直接生成订单。
- 支付回调成功就只改订单状态，不记录 `payment_callback_record`。
- 订单状态机允许从 `paid` 跳回 `created`。
- 权限控制只拦前端菜单，不拦后端接口。
- 商品列表 N+1；SKU 库存与展示库存口径不一致。
- 把“课程/录播”当普通附件管理，导致订单、权益、退款语义混乱。

## Part 1 Definition of Done

- 重复支付回调不会造成重复发货/重复开通。
- 并发 100 下单同一 SKU 不出现负库存。
- RBAC 有正例与越权负例测试。
- 订单状态迁移全部落 `order_status_log`。
- `docker compose up` 可起商城，CI 冒烟覆盖登录/商品/下单/回调。

# Part 2 WebSocket 连接层：生产长连接（课时 110–141，3 章）

## 2.1 官网课时结构映射

| 课时范围 | 章节 | 手册学习重点 |
| --- | --- | --- |
| 110–118 | 入门：Nchan 选型 | 为什么长连接接入不直接写进 IM/Web 服务 |
| 119–131 | 方案架构：Session 管理 | 连接绑定、在线状态、心跳、首包鉴权 |
| 132–141 | 消息推送与性能测试 | WebSocketSend、Pub/Sub 广播、4 万连接压测与复盘 |

只做推送 + 在线状态## 2.2 核心数据表/存储设计要点表（示例）

| 存储 | 键/表示例 | 设计要点 | 风险 |
| --- | --- | --- | --- |
| Redis 在线状态 | ws:online:{user_id} hash: device/server_id/expire | TTL + 心跳续期；多端在线 | TTL 与心跳周期不一致导致误判 |
| Redis 连接路由 | ws:route:{user_id}:{device} -> server_id | 推送定位到进程 | 服务重启需清理或容忍脏路由 |
| Redis Pub/Sub | ws:broadcast:{channel} | 跨进程扇出；消息体短、可重放由业务层保证 | Pub/Sub 无 ACK，不能当可靠投递 |
| 消息扇出缓存 | ws:pending:{msg_id} 短 TTL | 仅做断线重连窗口内补偿 | 长期保存会膨胀连接层职责 |
| 压测结果 | ws_bench_result | 连接数、P99 延迟、CPU/内存/FD、发布抖动 | 只看连接数不看失败率 |


> 📚 **完整真实课时清单**（依据官网目录逐节核对）：
## 2.1 课程介绍和入门（10 节）
websocket 是什么/解决什么问题 → HTTP 短连接痛点 → 协议与握手 → **Nchan 与 OpenResty 选型**（为何用 Nginx 层扛长连接而非纯 Go）→ 课件准备。

## 2.2 技术方案和架构设计（6 节）
整体架构 → 服务边界（只推送不管业务）→ **Session 管理模型**（conn_id/user_id/device/platform/server_id/channel）→ Redis 在线态（带 TTL）。

## 2.3 消息路由和在线状态管理（9 节）
消息路由 → sub/unsub 全局 Session 管理（三节）→ **节点心跳与故障自动清理**（二节）：节点挂了自动摘除其名下连接，避免僵尸 session。

## 2.4 消息推送与性能测试（9 节）
gRPC/http 双发送接口 → **Redis Pub/Sub 在跨进程广播里的真实用途**（A 进程收消息→Pub→所有节点 Sub→各推本地连接）→ Nchan 发送封装 → 前端联调与异常场景（断线重连/心跳保活/**首包鉴权**）→ 全链路性能测试与瓶颈分析 / 单机四万连接 / 分布式多节点故障自愈。

### 📊 核心数据表设计要点（Kimi 补强）

| 项 | 要点 |
|----|------|
| 连接元数据 | conn_id / user_id / device / platform / server_id / channel |
| 在线状态 | 放 **Redis 带 TTL**，不长期落库 |
| 消息扇出 | 只存 msg_id/channel/payload/expire，**不长期落连接层** |
| 压测产物 | bench_conn / bench_msg 结果表 |

### 🏭 生产级运行态闭环（v1 缺，Kimi 补齐）
- **首包鉴权**：WS 握手后第一帧校验 token（复用 Part 0 统一身份），拒绝匿名长连接。
- **心跳 & 空闲超时**：双向 ping/pong，超时主动断开释放 fd。
- **背压 & 慢消费者**：写缓冲满则降级/断连，防止一个卡死的客户端拖垮 worker。
- **Pub/Sub 扇出与雪崩**：Redis Pub/Sub **无 ACK**，不能当可靠队列；需本地兜底 + 幂等去重。
- **Nchan worker 配置**：共享内存区大小、订阅者上限。
- **水平扩容 & 滚动发布不断连**：新节点加入 → Etcd 注册 → 灰度引流；旧节点优雅排空再下线，**连接迁移**。
- **监控指标 & SLO**：P99 推送延迟 < 200ms、可用率 99.9%、按 server_id 维度告警。

### 🔌 关键接口签名示例
```go
WebSocketSend(ctx, *SendReq) (*SendResp, error)
IsOnlineUsers(ctx, *OnlineReq) (*OnlineResp, error)
GetOnlineCount(ctx, *CountReq) (*CountResp, error)
```

### 🎯 高频面试考点 & ⚠️ 易错点
- **考点**：长连接网关选型、首包鉴权、心跳、背压、Pub/Sub 扇出、水平扩容、P99、发布不抖动。
- **易错**：把业务消息长期存在连接层；用"连接数"衡量成功；忽略慢消费者；把无 ACK 的 Pub/Sub 当可靠队列；WS 鉴权只靠 URL 参数。

> **DoD（Part 2）**：慢消费者不拖垮单机；Redis 抖动可观测可降级；滚动发布期间零掉线；4 万连接压测报告含 P99/P999 与瓶颈定位。


---
---
## 2.3 关键接口签名示例（Go / gRPC）

goCopy```
type WebSocketService interface {
    WebSocketSend(ctx context.Context, req *SendReq) (*SendResp, error)
    IsOnlineUsers(ctx context.Context, req *OnlineUsersReq) (*OnlineUsersResp, error)
    GetOnlineCount(ctx context.Context, req *OnlineCountReq) (*OnlineCountResp, error)
}

type SendReq struct {
    MsgID     string
    UserIDs   []int64
    Channel   string
    Payload   []byte
    EventType string
    TraceID   string
}

type SendResp struct {
    Accepted int
    Online   int
    Offline  int
}

type OnlineUsersReq struct{ UserIDs []int64 }
type OnlineUsersResp struct {
    Online map[int64]bool
}

type OnlineCountReq struct{ Channel string }
type OnlineCountResp struct{ Count int64 }
```

OpenResty + Nchan 职责边界：TextCopy```
client --wss--> OpenResty/Nchan --首包 token--> backend verify(audience=websocket)
Nchan 管理 subscriber channel；
edu.websocket 通过 zrpc 接收业务推送请求；
Redis Pub/Sub 完成 edu.websocket 多实例扇出；
业务失败重试不放在连接层核心循环，交由业务消息服务补偿。
```

## 2.4 高频面试考点

- Nchan/OpenResty 扛长连接的优势与坑：FD、内存、worker 连接均衡、慢消费者。
- WebSocket 鉴权：为什么不能用永久 query token？首包鉴权失败如何关连？
- Pub/Sub 无 ACK：跨进程广播如何做到“尽力推送 + 业务补偿”？
- 心跳与在线状态：TTL、空闲超时、假在线、多端互踢策略。
- 压测指标：连接建立速率、消息 P99、扇出延迟、FD 耗尽、发布断连率。

## 2.5 易错点

- 把 IM 业务消息直接长期写进连接层。
- Redis Pub/Sub 消息丢失后无业务补偿，导致已读未送达无法解释。
- 在线状态靠服务本地 map，扩容后立即失效。
- 只看单机 4 万连接，不看滚动发布与 Redis 抖动。
- 用连接数判断推送成功，而不是 `Accepted/Online/Offline` 三维指标。

## Part 2 Definition of Done

- 首包 token 错误时连接关闭，且有日志与指标。
- 心跳断开后 1–2 个 TTL 周期内在线状态收敛。
- 三实例 `edu.websocket` 广播同一 channel，消息可达。
- 慢消费者触发背压/降级，不拖垮其他连接。
- 压测报告包含：连接数、建立速率、推送 P95/P99、失败率、资源水位、发布抖动。

# Part 3 IM：消息一致性系统（课时 142–196，5 章）

## 3.1 官网课时结构映射

| 课时范围 | 章节 | 手册学习重点 |
| --- | --- | --- |
| 142–150 | 产品技术方案 | 好友/群聊/会话/消息边界；已读/离线/多端同步指标 |
| 151–160 | 项目基础架构 | Gin 分层、统一响应、TraceID、WebSocket 接入复用连接层 |
| 161–170 | 账号体系 | 复用商城统一 Principal；IM 不自己签发身份 |
| 171–197 | 好友群会话，约 27 节 | 好友申请状态机、群成员、会话、消息 seq、离线拉取 |
| 198–230 | 消息链路进阶 | 撤回、敏感词、扩散读写、历史可见性 |
| 231–280 | 前后端联调修复 | 断网重发、重复消息、回执风暴、实战修复 |
| 281–301 | 为融合做准备 | 群机器人 @ 触发 Agent 的输入契约；只读工具先行 |


> 📚 **完整真实课时清单**（依据官网目录逐节核对）：
## 3.1 产品和技术方案（7 节）
课程介绍和大纲 → 产品方案和需求 → 产品原型评审 → **技术方案评审一/二/三** → **数据模型与表设计**（38 分钟重点课）。

## 3.2 项目基础架构（7 节）
后端框架初始化（复用商城分层）→ gentool model → 统一响应 → Redis/MySQL 配置 → 前端 Vite + React 脚手架。

## 3.3 账号体系（5 节）
复用商城用户体系（呼应 Part 0 统一 Principal）→ IM 侧身份映射 → 登录态打通。

## 3.4 好友群会话和消息全链路（27 节 ★最大）
- **好友系统**：添加好友申请流程 → 好友列表和搜索 → 删除和黑名单。
- **群聊系统**：创建群和邀请 → 群信息修改 → 群成员管理 → 退出和踢人。
- **单聊与消息发送**：创建单聊会话 → **聊天消息发送六节递进**（文本→多媒体→已读→@→撤回→历史同步）。
- **会话置顶和免打扰 / 消息记录查询**。
- **腾讯云对象存储接入（42 分钟）** → 依赖对象存储/Websocket/好友申请的 TODO 逐个补全（先跑通骨架再填肉）。

## 3.5 前后端联调修复演示（9 节）
前端第一版 AICoding → 第 N 版修复 → 后端群信息和群成员数量修复 → 前端交互显示优化 → 图片语音视频消息优化 → 各类消息类型完善测试 → 整体联调收尾优化（二节）→ **IM 功能完整演示**。

### 📊 核心数据表设计要点（Kimi 补强）

| 表 | 要点 |
|----|------|
| message | msg_id, conv_id, sender_id, **seq**, type, body, status |
| conversation / conv_member | 会话 & 成员（含入群时间戳，影响可见性） |
| friend_request | 状态机（待处理/通过/拒绝） |
| read_receipt | (conv_id, user_id, last_read_seq) |
| offline_inbox / 拉取游标 | 离线消息或按 afterSeq 增量拉取 |
| 撤回/编辑 | 留审计记录，非物理删 |

### 🔌 关键接口签名示例
```go
SendMessage(ctx, msg *Message, clientMsgId)      // clientMsgId 幂等去重
SyncMessages(ctx, uid, convID, afterSeq, limit)  // 增量同步
AckRead(ctx, uid, convID, lastReadSeq)           // 已读回执
TriggerAgent(ctx, convID, botID, mention, senderPrincipal) // @机器人
```

### 🎯 高频面试考点 & ⚠️ 易错点
- **考点**：**seq 单调递增**、多端同步、离线拉取、已读回执风暴、群聊扩散写/读、消息一致性、敏感词链路、@机器人权限边界。
- **易错**：用时间戳当排序（并发下乱序）；客户端生成全局 ID 冲突；已读回执广播打爆大群；撤回只删发送者端；历史消息可见性忽略退群时间点。

> **DoD（Part 3）**：断网重发不重复（clientMsgId 幂等）；退群后历史可见性正确；已读回执风暴受控（合并/采样）；敏感词在入库前拦截。


---
---
## 3.2 核心数据表设计要点表（示例）

| 域 | 表示例 | 设计要点 | 一致性 |
| --- | --- | --- | --- |
| 好友 | friend_request、friend_relation | 申请状态机；双向关系落库 | 防并发重复申请 |
| 群 | group、group_member、group_member_log | 成员版本号；退群时间点影响历史可见性 | 变更审计 |
| 会话 | conversation、conversation_member | 单聊/群聊统一 conv；last_msg_seq | 会话快照与消息一致 |
| 消息 | message | msg_id 全局唯一；conv_id + seq 单调；client_msg_id 幂等 | seq 空洞告警 |
| 已读 | read_receipt | (conv_id,user_id,last_read_seq)；群聊回执聚合 | 防回执风暴 |
| 离线 | offline_inbox 或拉取游标 | 在线用推送；离线用游标补偿 | 不重复不丢失 |
| 风控 | sensitive_word、message_audit | 发送前/异步复核双链路 | 误杀与漏放都要有指标 |
| 机器人 | bot_binding | 群、机器人、Agent、允许 Scope 的绑定 | @触发不继承用户全权 |

## 3.3 关键接口签名示例（Go）

goCopy```
type IMService interface {
    SendMessage(ctx context.Context, req SendMessageReq) (*SendMessageResp, error)
    SyncMessages(ctx context.Context, req SyncMessagesReq) (*SyncMessagesResp, error)
    AckRead(ctx context.Context, req AckReadReq) error
    TriggerAgentByMention(ctx context.Context, req TriggerAgentReq) (*TriggerAgentResp, error)
}

type SendMessageReq struct {
    SenderPrincipal Principal
    ConvID          int64
    ClientMsgID     string
    MsgType         string
    Body            []byte
    MentionUserIDs  []int64
    TraceID         string
}

type SendMessageResp struct {
    MsgID int64
    Seq   int64
}

type SyncMessagesReq struct {
    UserID int64
    ConvID int64
    AfterSeq int64
    Limit int32
}

type AckReadReq struct {
    UserID int64
    ConvID int64
    LastReadSeq int64
}

type TriggerAgentReq struct {
    ConvID        int64
    GroupID       int64
    BotID         int64
    MentionText   string
    SenderPrincipal Principal
    IdempotencyKey string
}
```

消息发送骨架：goCopy```
func (s *imService) SendMessage(ctx context.Context, req SendMessageReq) (*SendMessageResp, error) {
    if err := s.policy.CanSend(ctx, req.SenderPrincipal, req.ConvID); err != nil {
        return nil, err
    }
    msgID, seq, err := s.dao.InsertMessageIdempotent(ctx, req.ConvID, req.ClientMsgID, func() MessageRow {
        return MessageRow{
            SenderID: req.SenderPrincipal.SubjectID(),
            Type:     req.MsgType,
            Body:     req.Body,
            TraceID:  req.TraceID,
        }
    })
    if err != nil { return nil, err }

    s.risk.AsyncAudit(ctx, msgID, req.Body)

    if err := s.conn.WebSocketSend(ctx, &SendReq{
        MsgID: fmt.Sprint(msgID),
        Payload: mustJSON(map[string]any{
            "conv_id": req.ConvID, "msg_id": msgID, "seq": seq,
        }),
        EventType: "im.message.new",
        TraceID: req.TraceID,
    }); err != nil {
        s.offline.EnqueueOrMark(ctx, req.ConvID, msgID) // 补偿，不阻塞发送成功语义
    }
    return &SendMessageResp{MsgID: msgID, Seq: seq}, nil
}
```

## 3.4 高频面试考点

- 消息顺序：为什么不能信任客户端时间？`seq` 由服务端单调生成。
- 幂等：`client_msg_id` 去重；断网重发不产生重复。
- 多端同步：在线推送 + 离线游标；未读数如何维护。
- 已读回执：群聊如何避免 O(n²) 广播？聚合回执与拉取结合。
- 历史可见性：退群时间点、邀请前消息、撤回消息展示策略。
- 扩散写/读：小群扩散写，大群/超级群扩散读如何取舍。
- 敏感词：同步拦截与异步复核；误杀处理流程。

## 3.5 易错点

- 用时间戳排序导致乱序与重复。
- 撤回只删自己端，其他端仍能展示。
- 群成员变更后历史消息可见性错误。
- 已读回执逐条广播打爆大群。
- 离线消息只存“最后一条”，导致中间空洞。
- IM 偷偷鉴权或签发 token，破坏 Part 0 契约。

## Part 3 Definition of Done

- 同一 `client_msg_id` 重试只产生一条消息。
- 断网重连后按 `AfterSeq` 拉取补洞，无重复无丢失。
- 大群已读回执有聚合策略，指标不随人数平方爆炸。
- 退群/被移出后历史可见性有明确规则与测试。
- 敏感词链路有同步拦截、异步复核、人工申诉记录。

# Part 4 AI Agent：可治理的动态能力面（课时 197–301，8 章）

## 4.1 官网课时结构映射

| 课时范围 | 章节 | 手册学习重点 |
| --- | --- | --- |
| 197–205 | 导学入门 | Agent 与聊天机器人的边界；生产风险地图 |
| 206–223 | Eino 框架入门，约 18 节 | 模型/工具/MCP 抽象为原子能力；组件生命周期 |
| 224–244 | 实战方案框架，约 21 节 | Registry/Policy/Planner/Executor/Checkpoint |
| 245–260 | 工具开发 | 工具 Schema、超时、幂等、风险分级、审计 |
| 261–289 | Agent 实现，约 29 节，全课程最大 | 多轮对话、工具调用、SSE、中断恢复、RAG |
| 290–301 | RAG 知识库 + SKILL & MCP + RAG 重排查漏 | Milvus、chunk、rerank、citation、评测回归 |


> 📚 **完整真实课时清单**（依据官网目录逐节核对）：
## 4.1 导学入门（11 节）——从后端视角理解 LLM
课程介绍导学 → 从 Go 后端视角拆解 Agent → 大模型 API 到底接收什么返回什么 → 用 Go 写一个大模型客户端 → Prompt 工程 → Token/上下文窗口 → Temperature → Function Calling → ReAct（边想边做）→ Planning Agent 任务拆解 → Memory 短期上下文和长期画像。

## 4.2 Eino 框架入门应用（18 节）
eino 核心概念 → Chain/Graph → Component → Model 接入 → Tool 定义 → Callback → 流式处理 → 组合编排。

## 4.3 实战方案和框架开发（21 节）
整体技术方案 → 框架分层设计 → **能力注册中心 Registry** → Policy 策略层 → Planner 规划器 → Executor 执行器 → Checkpoint 检查点 → Guardrail 护栏。

## 4.4 Agent 工具开发（6 节）
自定义 Tool → Schema 版本化 → 沙箱执行 → 超时/幂等 → MCP Server 实现。

## 4.5 Agent 实现（29 节 ★最大）
多轮对话状态机 → 工具调用循环 → SSE 流式输出 → 取消与背压 → 人机协作中断恢复 → run/checkpoint 幂等 → 可观测性。

### 🏗️ Agent 运行时架构（Kimi 深入拆解）
```
Registry(能力注册+版本) ─┐
Policy(scope/白名单)    ─┼─▶ Planner(LLM规划) ─▶ Executor(沙箱执行)
Guardrail(注入/PII/成本)─┘                          │
                                          Checkpoint(持久化,进程崩可恢复)
```
**Run 主循环（Go 伪代码）**：
```go
func Run(ctx, convID, input) {
  cp := checkpoint.Load(convID, runID)      // 幂等:conversationId+runId
  for step := 0; step < MaxStep; step++ {
    stream := llm.Stream(ctx, prompt, tools) // eino 流式
    for delta := range stream { sse.Emit(delta) }        // SSE 背压
    if tc := detectToolCall(stream); tc != nil {
      policy.Authorize(principal, tc.Name)             // scope 判定
      res := sandbox.Execute(tc, withTimeout, idempotent)
      if tc.NeedsHuman { savePendingHuman(runID); return } // 中断
    }
    persistSpans()                                       // OTel
  }
}
// Resume(ctx, runID, humanInput):从 checkpoint 恢复继续跑
```
**时序**：`客户端 → API → Agent Orchestrator → ModelClient/SSE → ToolBroker/MCP → VectorStore(Milvus) → DB / OTel`；人机中断时存 pending_human，用户回复后 `Resume`。

### 📊 核心数据表设计要点（Kimi 补强）

| 表 | 要点 |
|----|------|
| agent_run | run_id, conversation_id, **idempotency_key**, status, **checkpoint**, cost_tokens, error |
| tool_registry | name, **version**, schema, risk, enabled |
| capability_policy | agent_id, tool_name, scope |
| rag_doc / chunk / vector_ref | 文档→切块→向量引用 |
| human_interrupt | run_id, reason, payload, resolved_by |

### 🔌 关键接口签名示例
```go
Run(ctx, req *RunReq) (*RunResp, error)
Resume(ctx, runID, humanInput)
InvokeTool(ctx, principal, tool, args, traceID)
SSE: GET /v1/agent/runs/{runID}/events
```

## 4.6 RAG 知识库（9 节）
RAG 讲解和配置初始化 → **向量嵌入和召回对象封装三节**（embedding + Milvus）→ 知识库向量化实现三节（切分→嵌入→入库）→ 检索工具注入 → 检索增强完成。（补 rerank、citation 引用、**拒答**阈值）

## 4.7 SKILL & MCP 集成（6 节）
Skill 集成一/二/完成 → MCP 概念和技术框架 → MCP Server 实现一/完成（handshake/timeout）。

## 4.8 RAG 召回重排 & 查漏补缺（5 节）
召回重排实现/测试（rerank 提升质量）→ Agent 运行时可观测性 → 实战修复:最大迭代次数强制输出 → **结课验收总结**。

### 🎯 高频面试考点 & ⚠️ 易错点
- **考点**：ReAct vs Planner-Executor、工具 Schema、MCP 生命周期、RAG chunking/rerank/citation、流式背压、中断恢复、eval 与回归、prompt injection、成本治理。
- **易错**：LLM 输出直接拼 SQL/Shell；工具无超时/无幂等；RAG 只 topK 不拒答；SSE 无取消；checkpoint 存内存进程崩即丢；后台改能力无审批与灰度。

> **DoD（Part 4）**：后台改能力需灰度+审批；恶意网页不能诱导越权工具调用；进程崩溃后 run 可从 checkpoint 恢复；有 eval 集与成本/限流监控。


---
---
## 4.2 深入拆解：把 Agent 做成可治理运行时

Agent = Orchestrator + Capability Registry + Policy + Model Client + Tool Broker + Checkpoint Store + Evaluator是否执行由 Policy 决定；执行必须可追踪、可取消、可恢复、可复盘### 4.2.1 伪代码（示例）

goCopy```
type Principal struct {
    UserID   string
    TenantID string
    Scopes   map[string]bool
    Via      string // user | group_bot | internal_job
}

type Risk int

const (
    RiskRead Risk = iota
    RiskWrite
    RiskIrreversible
    RiskExternalSideEffect
)

type ToolCall struct {
    ID       string
    Name     string // 示例：order.search / faq.search
    Version  string
    ArgsJSON []byte
    Risk     Risk
}

type Agent struct {
    registry    CapabilityRegistry
    policy      PolicyEngine
    model       ModelClient
    tools       ToolBroker
    checkpoint  CheckpointStore
    memory      ConversationStore
    sse         SSEStreamer
    human       HumanInterruptNotifier
    evaluator   RunEvaluator
    maxStep     int
}

func (a *Agent) Run(ctx context.Context, req RunReq) (*RunResp, error) {
    runID := RunID(req.ConversationID, req.IdempotencyKey)
    if cp, ok := a.checkpoint.Load(ctx, runID); ok {
        return a.resume(ctx, cp, req.HumanInput)
    }

    run := NewRun(runID, req.ConversationID, req.Principal)
    defer a.persistFinal(ctx, run)

    for step := 0; step < a.maxStep; step++ {
        select {
        case <-ctx.Done():
            a.checkpoint.Save(ctx, run.Pending(ctx.Err()))
            return run.Cancelled()
        default:
        }

        out, err := a.model.StreamDecision(ctx, DecisionInput{
            Messages: a.memory.Load(ctx, req.ConversationID),
            Tools:    a.registry.VisibleTo(req.Principal), // 不是全量工具
            Policy:   a.policy.Bind(req.Principal),
            TraceID:  trace.ID(ctx),
        })
        if err != nil {
            run.Record(err)
            return a.fallbackOrFail(ctx, run, err)
        }

        for ev := range out.Events() {
            if emitErr := a.sse.Emit(ev); emitErr != nil { // 客户端断开/背压
                a.checkpoint.Save(ctx, run.Pending(emitErr))
                return run.SSEBackpressured()
            }

            switch ev.Type {
            case EventFinalAnswer:
                a.memory.AppendFinal(ctx, req.ConversationID, ev.Text, ev.Citations)
                run.Done()
                return run.Resp()

            case EventToolCall:
                call := ev.ToolCall
                if !a.policy.Allow(req.Principal, call) {
                    a.sse.Emit(EventPolicyDeny{CallID: call.ID, Reason: "scope not allowed"})
                    run.CountPolicyDeny(call.Name)
                    continue
                }
                if call.Risk >= RiskHumanInterruptThreshold {
                    cp := run.Interrupt(call.ID, "need_human_approval", req)
                    a.checkpoint.Save(ctx, cp)
                    a.human.Notify(ctx, cp)
                    return run.WaitingHuman()
                }
                res, err := a.tools.Invoke(ctx, ToolInvokeReq{
                    Principal: req.Principal,
                    Call:      call,
                    TraceID:   trace.ID(ctx),
                })
                if err != nil {
                    run.RecordToolError(call, err)
                    a.memory.AppendToolResult(ctx, call.ID, redact.SafeError(err))
                    continue
                }
                a.memory.AppendToolResult(ctx, call.ID, res.SafeSummary())
            }
        }
    }
    run.Fail(ErrMaxStep)
    return run.Resp()
}
```

### 4.2.2 时序图（示例）

TextCopy```
Client                 Agent API          Orchestrator        ModelClient        ToolBroker        RAG/Milvus        Checkpoint/OTel

  | POST /runs            |                    |                   |                  |                 |                  |
  |---------------------->| create run_id      |                   |                  |                 |                  |
  |<----------------------| SSE stream --------|                   |                  |                 |                  |
  |                       |                    | Load checkpoint   |                  |                 |                  |
  |                       |                    |------------------>|                  |                 |                  |
  |                       |                    | Stream decision   |                  |                 |                  |
  |                       |                    |<------------------|                  |                 |                  |
  |                       |<-- delta token ----|                   |                  |                 |                  |
  |                       |                    | if need_context ->|                  | query topK      |                  |
  |                       |                    |-------------------------------------->|                 |                  |
  |                       |                    |<--------------------------------------| chunks+citations|                  |
  |                       |                    | Policy allow?     |                  |                 |                  |
  |                       |                    |------ invoke tool ->|                 |                 |                  |
  |                       |                    |                   | InvokeTool(principal, tool, args, traceID)
  |                       |                    |                   |----------------->|                 |                  |
  |                       |                    |<-- SafeSummary ----|                  |                 |                  |
  |                       |<-- tool result -----|                   |                  |                 |                  |
  |                       |                    | Save checkpoint/run events ------------------------------->|                  |
  |<-- final/citations ----|                    |                   |                  |                 |                  |
```

## 4.3 核心数据表设计要点表（示例）

| 域 | 表示例 | 设计要点 | 风险 |
| --- | --- | --- | --- |
| 运行 | agent_run | run_id、conversation_id、idempotency_key 唯一；状态 running/waiting_human/done/failed/cancelled | 重复 run 资损/重复调用 |
| 检查点 | agent_checkpoint | run_id 主键；存 pending_tool_call、消息摘要、token 计数 | 进程崩后不可恢复 |
| 工具注册 | tool_registry | name/version/schema_json/risk/enabled/owner | 后台改完即生效需灰度与审计 |
| 策略 | capability_policy | (agent_id/bot_id, tool_name, scope) | 默认拒绝 |
| 中断 | human_interrupt | run_id/interrupt_id/reason/payload/resolved_by/resolved_at | 恢复必须幂等 |
| 会话 | conversation_message | SQLite 可起步；摘要与原始消息分离 | 长上下文成本爆炸 |
| RAG | rag_doc、rag_chunk、vector_ref | chunk 元数据；citation_id 回链 | 无引用事实触发拒答 |
| 观测 | agent_run_metric | tokens、tool latency、policy deny、fallback、cost | 成本失控无报警 |

## 4.4 关键接口签名示例（Go）

goCopy```
type AgentService interface {
    Run(ctx context.Context, req *RunReq) (*RunResp, error)
    Resume(ctx context.Context, req *ResumeReq) (*RunResp, error)
    InvokeTool(ctx context.Context, req *ToolInvokeReq) (*ToolInvokeResp, error)
    StreamEvents(ctx context.Context, runID string, lastEventID string) (<-chan Event, error)
}

type RunReq struct {
    ConversationID string
    IdempotencyKey string
    Principal      Principal
    Input          string
    ContextDocs    []string
}

type ResumeReq struct {
    RunID        string
    InterruptID  string
    HumanInput   string
    ResolvedBy   string
}

type ToolInvokeReq struct {
    Principal Principal
    Call      ToolCall
    TraceID   string
}

type ToolInvokeResp struct {
    CallID      string
    SafeSummary string
    CitationIDs []string
}
```

## 4.5 高频面试考点

- ReAct 与 Planner/Executor 的差异；什么时候不能自由规划。
- 工具 Schema 与版本兼容；后台改完即生效如何做灰度。
- MCP 生命周期：handshake、capability negotiation、timeout、取消。
- RAG：chunk 策略、embedding、Milvus topK、rerank、citation、拒答阈值。
- SSE：心跳、Last-Event-ID、背压、客户端断开后的 run 状态。
- 中断恢复：human-in-the-loop 的幂等恢复与审计。
- Prompt Injection：不可信文档不能直接生成工具参数。
- Eval：把 bug 修复变成回归集；离线评测与线上抽检。

## 4.6 易错点

- 让 LLM 直接拼 SQL/Shell/退款参数。
- 工具无超时、无幂等键、无风险分级。
- RAG 只 topK，不做引用与拒答，幻觉不可解释。
- checkpoint 存内存，进程重启即丢。
- 后台改能力无审批、无灰度、无回滚。
- SSE 没有取消与背压，客户端断开仍在烧 token。

## Part 4 Definition of Done

- `conversation_id + idempotency_key` 重复请求不重复执行副作用。
- SSE 断线可按 `Last-Event-ID` 续传或恢复 waiting 状态。
- 高风险工具默认进入 `human_interrupt`，恢复有 `resolved_by` 审计。
- RAG 回答带来 `citation_id`，无引用高风险事实触发拒答或追问。
- 每次 run 记录 tokens/tool latency/policy deny/fallback/cost，并有 eval 回归报告。

# Part 5 微服务融合直播：统一鉴权与五入口融合（课时 302–317）

## 5.1 官网课时结构映射

| 课时范围 | 章节 | 手册学习重点 |
| --- | --- | --- |
| 302–305 | 演示 | 端到端演示：商城登录 → IM 群 → @机器人 → Agent 调用工具 → SSE 回流 |
| 306–309 | 直播第一节 | gRPC 统一鉴权原则：IM/Agent 不自己签 token |
| 310–312 | 直播第二节 | backend VerifyToken、Audience/Scope、JWKS 缓存与吊销 |
| 313–315 | 直播第三节 | IM 用户体系改造；群机器人 @ 触发 Agent 授权链 |
| 316 | 直播第四节 | Agent 可配置化；前端五入口融合；Docker Compose 编排 |
| 317 | 直播第五节 | CICD、灰度、回滚、故障注入与验收基线 |


> 📚 **完整真实课时清单**（依据官网目录逐节核对）：
## 5.0 成果演示（2 节）
演示：微服务集成智能体 IM 系统一 / 二 —— 先看最终效果，建立目标感。

## 5.1 直播第一节 · 微服务集成技术方案（三节）
整体融合方案设计：服务边界、gRPC 拓扑、Etcd 注册发现、数据一致性策略。

## 5.2 直播第二节 · 统一 gRPC & 统一鉴权（二节）★关键改造
IM 和 Agent **不再自己签发 token**，全部透传给商城 backend 做统一验证（呼应 Part 0）。

### 🛡️ 统一鉴权深入拆解（Kimi）
**原则**：不自签；backend 唯一签发与校验；IM/Agent gRPC Metadata 透传；sidecar/拦截器校验；JWKS/公钥拉取缓存；scope 判定；service-to-service 用 mTLS/OAuth2 client creds；用户 token 短时+refresh。

```go
// gRPC unary interceptor
func AuthInterceptor(ctx, req, info, next) {
  tok := metadata.FromIncomingContext(ctx).Get("authorization")[0]
  p, err := backend.VerifyToken(ctx, tok, audience, scope) // deny by default
  if err != nil { return status.Error(codes.Unauthenticated, err) }
  ctx = WithPrincipal(ctx, p)
  return next(ctx, req)
}
// 出站传播:WithOutgoingPrincipal(ctx, p) → 塞回 outgoing metadata
// JWKS:按 kid+exp 缓存公钥;支持轮换刷新
```
**@机器人触发 Agent 的授权链**：IM identity → agent principal 映射，Agent tool allowlist 限定能代用户执行哪些工具。

### ⚠️ 失败模式清单（Kimi）
时钟漂移 · kid 轮换未刷新缓存 · 吊销只靠短 TTL · gRPC metadata 大小超限 · **Nchan/OpenResty 越权终结业务鉴权**（网关不该替业务判权限）· P99 抖动。

## 5.3 直播第三节 · IM 用户体系改造（三节）
IM 账号对齐商城统一 Principal，历史数据迁移。

## 5.4 直播第四节 · IM 群机器人 @触发 Agent（三节）
群里 @机器人 → TriggerAgent → 走 Part 4 Run 循环 → SSE 流式回填到会话。

## 5.5 直播第五节 · Agent 可配置化与前端融合（三节）
后台配置 Agent 能力即时生效；前端融合五入口（商城/IM/WebSocket/Agent 统一门户）。

## 5.6 部署上线（贯穿）
Docker Compose 编排（依赖启动顺序）→ CNB CICD（打 tag 自动构建→推送→拉起）→ 灰度 → 回滚 → 故障注入。

### 📊 核心数据表设计要点（Kimi 补强）

| 表 | 要点 |
|----|------|
| service_identity | audience / scope 定义 |
| bot_binding | im_group_id, agent_id, allowed_scope |
| token_jwks | kid, public_key, exp |
| authz_decision（可选审计） | 谁在何时被允许/拒绝做什么 |
| deployment / release_flag / rollback | 发布与灰度回滚 |

### 🔌 关键接口签名示例
```go
backend.VerifyToken(ctx, token, audience, scope) (*Principal, error)
IM 拦截器: AuthInterceptor(ctx)
Agent 出站: WithOutgoingPrincipal(ctx, p)
```

### 🎯 高频面试考点 & ⚠️ 易错点
- **考点**：统一鉴权 vs 每服务自签、JWKS 轮换与吊销、时钟漂移、mTLS/service-to-service、WS 首包认证、机器人 @触发授权边界、灰度与回滚。
- **易错**：IM/Agent 偷偷签 token；kid 轮换未缓存刷新；gRPC metadata 超限；Nchan/OpenResty 越权终结业务鉴权；Agent 代用户执行未授权工具。

> **DoD（Part 5）**：任一服务回滚后身份与消息链路仍一致；@机器人在大群不越权；滚动发布零掉线；CICD 一键回滚验证通过。


---
---
## 5.2 深入拆解：微服务统一鉴权

backend 是唯一身份签发方与强校验方；IM/Agent 只做传递、Audience/Scope 收窄、授权决策留痕。### 5.2.1 backend 校验接口（示例）

goCopy```
type VerifyReq struct {
    Token    string
    Audience string // mall / im / agent / websocket
    Scope    string // order.read / message.send / agent.run
}

type Principal struct {
    Subject  string // user:123 / bot:agent-7 / svc:im
    TenantID string
    Scopes   map[string]bool
    Exp      time.Time
    Kid      string
    RawToken string // 出站透传用；日志只存 hash
    BotBindingID string
}

type AuthService interface {
    VerifyToken(ctx context.Context, req VerifyReq) (*Principal, error)
    RotateKeys(ctx context.Context) error
    RevokeJTI(ctx context.Context, jti string, ttl time.Duration) error
}
```

### 5.2.2 gRPC 服务端拦截器与出站透传（示例）

goCopy```
func AuthUnaryServerInterceptor(auth AuthClient, audience string) grpc.UnaryServerInterceptor {
    return func(ctx context.Context, req any, info *grpc.UnaryServerInfo, handler grpc.UnaryHandler) (any, error) {
        md, _ := metadata.FromIncomingContext(ctx)
        token := first(md.Get("authorization"))
        scope := requiredScope(info.FullMethod)
        p, err := auth.VerifyToken(ctx, VerifyReq{Token: token, Audience: audience, Scope: scope})
        if err != nil {
            audit.AuthzDeny(ctx, info.FullMethod, err)
            return nil, status.Errorf(codes.Unauthenticated, "verify: %v", err)
        }
        ctx = WithPrincipal(ctx, p)
        return handler(ctx, req)
    }
}

func WithOutgoingPrincipal(ctx context.Context, p *Principal) context.Context {
    return metadata.AppendToOutgoingContext(ctx,
        "authorization", "Bearer "+p.RawToken,
        "x-caller", "svc:im",
        "x-bot-binding", p.BotBindingID,
    )
}
```

### 5.2.3 机器人 @ 触发授权链（示例）

goCopy```
func (s *imService) TriggerAgentByMention(ctx context.Context, req TriggerAgentReq) (*TriggerAgentResp, error) {
    binding, err := s.dao.GetBotBinding(ctx, req.GroupID, req.BotID)
    if err != nil { return nil, err }

    if err := s.policy.CanMentionBot(ctx, req.SenderPrincipal, binding); err != nil {
        return nil, err
    }

    limited := Principal{
        Subject:      fmt.Sprintf("bot:%d", binding.AgentID),
        TenantID:     req.SenderPrincipal.TenantID,
        Scopes:       binding.AllowedScopes, // 例如 faq.search / order.read_tenant_safe
        Via:          "group_bot",
        BotBindingID: binding.ID,
    }

    out, err := s.agent.Run(ctx, &agent.RunReq{
        ConversationID: fmt.Sprintf("im:%d:%s", req.ConvID, req.IdempotencyKey),
        IdempotencyKey: req.IdempotencyKey,
        Principal:      limited,
        Input:          req.MentionText,
    })
    if err != nil { return nil, err }

    return &TriggerAgentResp{RunID: out.RunID, Status: out.Status}, nil
}
```

### 5.2.4 统一鉴权时序图（示例）

TextCopy```
用户浏览器        Mall Backend        IM(gRPC)          Agent Orchestrator       OpenResty/Nchan

   | login -------->| issue short token                 |                        |
   |<---------------| audience=mall, scope=base         |                        |
   | open ws ------------------------------------------------------------->|
   | first message token --------------------------------------------------->|
   |                                      verify(audience=websocket) <-------|
   | IM send message --gRPC metadata authorization---------------------->|
   |                                      Verify(audience=im,scope=message.send)
   | group @bot ----->| build limited bot principal                     |
   |                                      Run(agent.run + tool scopes) ---->|
   |<-------------------------------- SSE final/citations ------------------|
```

必须讲透的失败模式：

| 失败模式 | 正确策略 | 严禁 |

| --- | --- | --- |
| JWKS/kid 轮换失败 | 缓存上次公钥 + 时钟宽限 + 后台刷新 | 全局放行 |
| 吊销 | 短 TTL 是底线；高风险操作加 jti 黑名单/token version | 认为短 TTL 可替代吊销 |
| 时钟漂移 | exp/iat 小 skew；Audience/Scope 不放宽 | 因漂移扩大授权 |
| WS 认证 | 首包认证，失败关连；重连重验 | 永久 URL token |
| OpenResty/Nchan | 只做传输与会话亲和，不终结业务授权 | 网关形成影子权限 |
| 机器人越权 | @ 触发按 bot_binding 收窄 scope/额度 | Agent 继承用户全权 |

## 5.3 核心数据表设计要点表（示例）

| 域 | 表示例 | 设计要点 | 风险 |
| --- | --- | --- | --- |
| 密钥 | token_jwks | kid/public_key/alg/exp/active | 轮换不一致导致大面积 401 |
| 吊销 | token_revocation_jti | jti + ttl；高风险操作校验 | 黑名单无限增长 |
| 服务身份 | service_identity | svc:im/svc:agent 与 mTLS/服务账号绑定 | 伪造 x-caller |
| 机器人绑定 | bot_binding | group_id、agent_id、allowed_scopes、rate_limit、enabled | 群机器人越权 |
| 审计 | authz_decision | principal_hash、method、scope、allow/deny、latency | 出事故无法复盘 |
| 发布 | release_flag、deployment | 灰度比例、回滚镜像、迁移版本 | 回滚与迁移不配套 |

## 5.4 关键接口签名示例（Go）

goCopy```
type FusionService interface {
    VerifyToken(ctx context.Context, req *VerifyTokenReq) (*VerifyTokenResp, error)
    TriggerAgentByMention(ctx context.Context, req *TriggerAgentReq) (*TriggerAgentResp, error)
    GetFrontendEntries(ctx context.Context, principal Principal) (*FrontendEntriesResp, error)
}

type VerifyTokenReq struct {
    Token    string
    Audience string
    Scope    string
}

type FrontendEntriesResp struct {
    Entries []FrontendEntry `json:"entries"`
}

type FrontendEntry struct {
    Key    string `json:"key"`    // mall / admin / im / agent / fusion
    Path   string `json:"path"`
    Scopes []string `json:"scopes"`
}
```

## 5.5 高频面试考点

- 为什么不能让 IM/Agent 自己签 token？密钥面、撤销、Audience 混乱。
- JWKS 缓存如何失效？kid 未知时如何刷新？刷新失败怎么办？
- gRPC metadata 传用户身份的边界；为什么不能信任 `x-user-id`？
- 机器人 @ 触发和用户主动调用 Agent 的授权差异。
- 服务间调用：service identity + mTLS 与用户身份透传的关系。
- 灰度与回滚：DB 迁移可逆、接口版本兼容、feature flag 生命周期。

## 5.6 易错点

- IM/Agent 本地校验 token，绕过 backend 审计。
- `x-caller` 未校验，任意服务可冒充 im。
- 机器人绑定后 Agent 可执行写操作，例如退款、改价。
- 回滚只回滚代码，不回滚或补偿 DB 迁移。
- 前端五入口各自做权限判断，没有服务端统一 Scope。

## Part 5 Definition of Done

- IM/Agent 代码库中不出现 token 签发逻辑；搜索 `jwt.Sign` 应为 0。
- 错 Audience、缺 Scope、吊销 token 三类请求全部被拒并审计。
- 群机器人 @ 触发只能调用绑定 Scope 内工具，并有 rate limit。
- Docker Compose 一键起五系统；滚动发布演示连接抖动可观测。
- 故障注入：停掉 backend 鉴权依赖时，IM/Agent 明确降级或拒绝，不误放行。

# 附录 A 代码风格方法论 11 条

1. **先文档后代码**：PRD → 技术方案评审一/二/三 → 接口契约 → 代码；文档变更必须触发代码 review。
2. **分层不越界**：`router` 不碰 DB；`api` 不写业务规则；`service` 不拼 SQL；`dao` 不感知 DTO。
3. **统一响应**：`{code,msg,data}`；错误码稳定；错误信息可国际化但不泄露内部细节。
4. **model 生成与迁移**：gentool 生成 model；手工改字段必须有 migration；禁止线上手工改表。
5. **复杂功能递进**：一跑通骨架，二补边界，三做治理；每步可回滚。
6. **幂等优先**：所有写接口考虑重试；外部回调必须有唯一键与状态机。
7. **可观测内置**：入口生成 trace_id；跨 HTTP/gRPC/Redis/MySQL/Milvus/SQLite 传播；日志结构化。
8. **先失败可见，再失败优雅**：错误有指标、有日志、有审计；降级策略写清触发条件。
9. **AI 出初版，人 review**：AI 生成代码必须过 lint/test/安全扫描；关键链路人工写测试。
10. **小 PR 常态**：一个 PR 一个意图；bug 修复/实战修复/加餐必须附带回归用例。
11. **生产基线**：每个模块有 DoD、SLO、回滚 SOP、压测或验收报告；没有 DoD 不算完成。

# 附录 B 学习路径 3 条

## B.1 业务交易路径：适合后端业务工程师

## B.2 中间件与连接层路径：适合基础架构/IM 工程师

## B.3 AI Agent 工程路径：适合 LLM 应用工程师

# 附录 C 面试冲刺：每模块 20 问 + 追问链（精选）

## C.1 商城 10 问

1. 下单如何防超卖？追问：条件更新失败如何返回给用户？
2. 支付回调如何幂等？追问：同一回调先成功后被重放会怎样？
3. 订单状态机如何设计？追问：取消和支付并发到达怎么裁决？
4. 购物车与订单价格不一致怎么办？追问：秒杀价如何锁定？
5. RBAC 为什么不用 role string？追问：按钮权限如何落到接口？
6. 深分页为什么慢？追问：游标分页排序键怎么选？
7. 虚拟商品退款如何处理权益？追问：学习进度是否保留？
8. 库存扣减与支付超时如何关单回补？追问：回补失败怎么办？
9. 如何发现资损？追问：对账任务跑多久、误差阈值多少？
10. qingfly 商用边界是什么？追问：日志里哪些字段必须脱敏？

## C.2 WebSocket 连接层 10 问

1. 为什么用 OpenResty+Nchan？追问：与自研网关取舍？
2. 首包鉴权怎么做？追问：URL token 为什么危险？
3. 心跳和 TTL 怎么设？追问：假在线如何产生？
4. 单机 4 万连接看哪些指标？追问：P99 扇出延迟如何压？
5. Redis Pub/Sub 丢了怎么办？追问：业务补偿在哪一层？
6. 慢消费者如何处理？追问：背压阈值怎么定？
7. 滚动发布如何不断连？追问：连接迁移怎么做？
8. 多端在线如何互踢？追问：移动端后台被杀如何识别？
9. 如何防 FD/内存耗尽？追问：ulimit 与 worker 配置？
10. 连接层为何不能存业务消息？追问：那历史消息放哪？

## C.3 IM 10 问

1. 消息全局 ID 与 seq 怎么生成？追问：seq 空洞说明什么？
2. 断网重发如何保证不重复？追问：client_msg_id 冲突怎么办？
3. 离线消息如何拉取？追问：已读未读如何维护？
4. 群聊回执风暴如何避免？追问：聚合回执字段怎么设计？
5. 退群后历史消息可见性？追问：被移出和主动退群是否一致？
6. 撤回如何做一致？追问：已推送端如何同步？
7. 敏感词链路如何设计？追问：误杀如何申诉？
8. 小群与大群扩散策略？追问：超级群如何存？
9. IM 与商城账号如何统一？追问：为何不能 IM 自己签发 token？
10. @机器人触发如何保证授权？追问：binding 里应有哪些字段？

## C.4 AI Agent 10 问

1. Planner/Executor 与 ReAct 差异？追问：何时禁止自由规划？
2. 工具 Schema 如何演进？追问：后台改完即生效如何灰度？
3. MCP handshake/timeout/cancel 怎么设计？追问：无响应工具如何熔断？
4. SSE 背压与恢复？追问：Last-Event-ID 语义是什么？
5. 中断恢复如何幂等？追问：resolved_by 审计有什么用？
6. RAG 如何拒答？追问：citation 缺失时模型该怎么做？
7. Prompt Injection 如何防？追问：网页内容进入工具参数前怎么处理？
8. run 成本如何控制？追问：max_step、max_tokens、超时怎么联动？
9. eval 集怎么建？追问：线上 bug 如何沉淀为回归？
10. Agent 与 IM 机器人边界？追问：bot_binding 权限如何最小化？

## C.5 统一鉴权 10 问

1. 为什么 backend 唯一校验？追问：IM 本地验 token 的风险？
2. Audience/Scope 如何设计？追问：跨 audience 复用 token 为何危险？
3. JWKS 缓存与轮换？追问：kid 未知时是否直接拒绝？
4. 吊销怎么做？追问：短 TTL 与黑名单如何取舍？
5. 时钟漂移影响？追问：skew 多少合理？
6. 服务间身份如何传？追问：x-caller 能信吗？
7. WS 首包鉴权失败处理？追问：重连是否重验？
8. 机器人 @ 触发授权链？追问：如何防群成员诱导越权？
9. 灰度与回滚如何配套 DB 迁移？追问：不可回退迁移怎么办？
10. 授权决策如何审计？追问：deny 也要记录吗？

# 附录 D 故障手册

| 故障 | 现象 | 定位入口 | 处置 | 预防 |
| --- | --- | --- | --- | --- |
| 支付重复回调 | 同一订单多次发货/开通 | payment_callback_record 唯一键冲突；订单状态日志 | 幂等返回；按状态机裁决；人工对账 | 回调唯一键；金额/状态校验 |
| 超卖 | 库存为负 | 条件更新影响行数；sku_price_log；订单快照 | 冻结批次；补偿库存；审计价格 | 库存扣减条件更新；下单重取价 |
| 长连接抖动 | 在线数突降、推送失败率升 | Nchan worker、FD、Redis Pub/Sub 延迟、慢消费者 | 降级扇出；扩容；踢慢连接 | 背压阈值；发布预案；SLO 告警 |
| 假在线 | 用户已离线仍显示在线 | Redis TTL、心跳周期、服务清理任务 | 修正 TTL；补清理 job | TTL/心跳比例；重启清理策略 |
| IM seq 空洞 | 客户端补洞失败 | message.seq 缺口；离线游标 | 修复游标；重放补偿；告警 | seq 单调生成；补洞压测 |
| 回执风暴 | 大群延迟飙升 | read_receipt 写入速率 | 聚合回执；采样；限流 | 聚合字段；拉取优先策略 |
| RAG 幻觉 | 回答无引用 | citation_id 缺失率；chunk 元数据 | 降阈值拒答；修正索引 | citation 强制；eval 集回归 |
| Agent run 卡死 | run 长时间 running | checkpoint、tool timeout、SSE backpressure | 标记 interrupted；恢复或取消 | 全链路 timeout；max_step |
| 工具越权 | Agent 执行未授权写操作 | capability_policy deny 率；bot_binding | 熔断工具；撤销授权；审计 | 默认拒绝；风险分级；人机审批 |
| JWKS 轮换失败 | 大量 401/验证失败 | token_jwks active/kid；刷新日志 | 回滚 kid；宽限恢复 | 后台刷新；kid 重叠期；演练 |
| 机器人越权 | 群成员通过 @bot 触发写工具 | authz_decision；bot_binding scope | 禁用绑定；修复 scope | 绑定最小化；rate limit |
| CD 回滚事故 | 代码回滚但 schema 不兼容 | deployment tag；migration 版本 | 前滚修复；数据补偿 | 迁移可回退；兼容窗口 |

# 附录 E 配套资料清单 14 份（按版本锁定，示例）

1. Go 1.25 官方文档：modules、testing、race、context、pprof。
2. Gin 官方文档：路由组、绑定校验、中间件、错误处理。
3. GORM 官方文档：migration、transaction、association、optimistic lock。
4. go-zero 官方文档：zrpc、service group、middleware、负载均衡。
5. gRPC 官方文档：metadata、interceptors、deadline、health checking。
6. OpenTelemetry Go：trace context、 propagator、 exporter、语义约定。
7. Eino 官方资料：model/tool/prompt/component 抽象与扩展点。
8. MCP 规范：capability、tool schema、lifecycle、安全边界。
9. Milvus 文档：collection、index、topK、hybrid search、性能调优。
10. Redis 文档：Pub/Sub、TTL、Lua、bitmap/hyperloglog、持久化边界。
11. Nchan/OpenResty 文档：subscriber channel、message buffer、worker 配置、限流。
12. React 19 / Ant Design Pro / Vite：并发渲染边界、路由权限、构建优化。
13. Docker Compose 与 CNB CICD：依赖启动顺序、健康检查、构建缓存、发布回滚。
14. 工程安全基线：OWASP ASVS、Secrets 管理、gosec/govulncheck、JWT/JWKS 安全实践。

# 最终验收基线（全课程）

- 搜索 `jwt.Sign`：在 `edu.webchat` 与 `edu.agent.code` 中必须为 0。
- 所有写接口：具备幂等键或天然唯一约束；所有回调：验签 + 验金额 + 状态机。
- 所有外部推送：记录 `msg_id/trace_id/accepted/online/offline`。
- 所有 IM 消息：服务端 `seq` 单调；断网重发不重复。
- 所有 Agent run：可恢复、可取消、可审计、可评估；高风险工具默认人工审批。
- 所有模块：DoD 通过；CI 绿；CD 可回滚；故障演练有报告。

