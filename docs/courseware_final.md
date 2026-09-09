# Go 微服务实战：商城 × AI Agent × IM 生产级全栈项目
## 课程学习手册 · 定稿 v2.0

> **本手册由两个 AI 对抗式共写而成。**
> - **v1（GenericAgent）**：严格依据 qingfly.com 官网真实采集的 **317 课时 / 36 章完整目录树、官方课程介绍、五大子系统技术栈、14 份配套资料清单**搭建骨架，保证"完全按照他的课程来写"。
> - **v2（Kimi）**：以资深 Go 架构师视角对 v1 逐条攻击，指出 6 个结构性缺陷，并补齐 v1 缺失的**核心数据表设计要点、关键接口签名、高频面试考点、易错点、Definition of Done（验收标准）、以及 Agent 实现与微服务统一鉴权两段深入拆解**。
> - **定稿**：融合两者——保留真实的课时结构与老师命名风格，注入 Kimi 的生产级深度与可验收口径。
>
> ⚠️ 阅读约定：凡标注「设计要点 / 示例签名」处为**基于技术栈的合理还原**（因未购买无法读取源码 zip），字段名以实际授课为准，但语义与验收口径不可缺。

---

## 0. 这门课到底在做什么

一句话：**把「商城、IM、WebSocket、AI Agent」四套独立系统，用微服务架构拼成一个能真正上线的完整系统，最后部署到 qingfly.com 生产环境。**

它不是 demo，而是五个各自独立仓库、独立 CICD、独立上线的子系统的合集：

| # | 子系统 | 定位 | 仓库 | 关键技术 |
|---|--------|------|------|----------|
| 1 | 🛒 Go 商城 | 在线教育商城（已在 qingfly.com 商用） | edu.mall.backend / customer / frontend | Go+Gin+GORM, React19+AntD Pro |
| 2 | 🔌 WebSocket 连接层 | 只做推送+在线状态，不碰业务 | edu.mall.websocket | go-zero zrpc, OpenResty+Nchan, Redis Pub/Sub |
| 3 | 💬 IM 即时通讯 | 好友/群聊/会话/消息全链路 | edu.mall.webchat / webchatui | Go+Gin+React, WebSocket 推送 |
| 4 | 🤖 AI Agent | eino 原生 Agent，能力可后台配置 | edu.agent.code | eino, MCP, Milvus, SSE, OpenTelemetry |
| 5 | 🔗 微服务融合直播 | 五系统打通并上线 | （跨仓库改造） | gRPC 统一鉴权, Docker Compose, CNB CICD |

### 0.1 全课数据流全景
```
[用户] ──HTTP──▶ 商城backend(Gin) ──gRPC──▶ Etcd注册发现
   │                              │
   │(登录/下单/支付)               ├─▶ MySQL(商品/订单/RBAC)
   │                              ├─▶ Redis(会话/缓存/在线态)
   ▼                              ▼
[IM webchat] ◀──gRPC透传token── [WebSocket连接层] ◀──Nchan长连接──▶ [用户浏览器]
   │                                    ▲
   └──@机器人触发────────────▶ [AI Agent(eino)] ──MCP/工具──▶ 外部能力
                                              └─RAG(Milvus向量库)
```

### 0.2 技术栈基线（锁死版本，避免环境漂移）
> Kimi 指出：标题写 React19、技术栈写 React18–19，Go 写 1.23–1.25 —— 学员会装出五种环境。**定稿固定如下基线：**

| 层级 | 推荐基线 | 说明 |
|------|---------|------|
| Go | **1.24.x – 1.25.x** | 课程覆盖 1.23–1.25，建议用最新稳定 |
| Web 框架 | Gin | C端/管理台 HTTP |
| ORM | GORM + gentool | model 自动生成 |
| 微服务 | go-zero zrpc + gRPC + Etcd | 连接层 & 服务间通信 |
| AI Agent | eino + MCP + Milvus | 字节 eino 框架 |
| 数据库 | MySQL（主）+ SQLite（Agent轻量）+ Redis | — |
| 前端 | **React 19** + Ant Design Pro + Vite | 固定 19 |
| 实时通信 | WebSocket + SSE | WS 双向 / SSE 流式 |
| 可观测 | OpenTelemetry + traceId + 结构化日志 | 贯穿全课 |
| 部署 | Docker Compose + CNB CICD | 打 tag 自动构建→推送→拉起 |

### 0.3 学完后的简历亮点
- 独立搭建 Go 微服务架构（gRPC + Etcd + Docker Compose）
- 设计并实现 eino Agent 自定义引擎（工具/MCP/RAG/SKILL 可配置、改完即生效）
- 实现 WebSocket + SSE 双通道实时通信（单机 4 万连接压测通过）
- 完成一套含统一鉴权、IM、AI、商城的全栈项目
- 把 qingfly.com 作为简历上的作品集



---
---

# Part 0 · 工程地基与课程契约（贯穿全课的前置约束）

> ⭐ 这是 Kimi 对抗后**新增的最关键一层**。v1 把"统一鉴权"压到最后的直播阶段，Kimi 一针见血：**"IM 和 Agent 不自己签 token、透传 backend 统一验证"不是融合阶段的补丁，而是第二章就该立的规矩。** 否则前面所有登录、多端、WebSocket、群机器人 @触发 Agent 都会各自长出鉴权逻辑，最后返工。所以定稿把它提到 Part 0。

## 0.4 统一身份模型（全课先立此规矩）
所有子系统消费**同一套身份语义**，谁都不自签：

- **Principal（主体）**：`{ user_id, roles[], device, platform, scope[] }` —— 一次登录产出的权威身份对象。
- **Token**：由**商城 backend 唯一签发与校验**；IM / Agent / WebSocket 一律通过 gRPC Metadata 或首包**透传**原始 token，交给 backend 验证。
- **JWKS / 公钥**：校验方拉取并缓存 `kid → public_key`，支持轮换。
- **scope**：细粒度权限点，工具调用/接口访问都按 scope 判定，deny by default。
- **TraceID**：从 HTTP 入口生成，经 gRPC metadata 全程贯穿，落到结构化日志与 OTel span。

```
商城C端登录 ─┐
WebSocket首包 ─┤──▶ backend.VerifyToken(token, audience, scope) ──▶ Principal
IM gRPC ──────┤                                                    │
Agent工具调用 ─┘◀───────────────────────────────────────────────────┘
```

## 0.5 CI 门禁与 CD 回滚（五大仓库通用）
每个独立仓库的流水线：`lint → unit test(覆盖率门禁) → migrate check → build image → deploy → smoke test`，失败即阻断；CD 保留上一版本镜像，**一键回滚**。CNB 上打 tag 自动触发。

## 0.6 SLO / 告警 / 压测报告 / 故障演练模板
- **SLO 示例**：连接层 P99 推送延迟 < 200ms、可用率 99.9%；支付回调处理 < 500ms。
- **压测报告模板**：连接数梯度 × 消息 QPS × P99/P999 × 错误率 × 资源占用，附瓶颈定位。
- **故障演练**：Redis 抖动、下游 gRPC 超时、节点滚动发布时的连接迁移、模型服务降级。

> **DoD（Part 0）**：能画出五系统数据流图；固定好 Go1.25/React19 版本矩阵；统一 Principal/Token/JWKS/scope/TraceID 规范落地到代码骨架；CI 门禁 + CD 回滚跑通；SLO 与压测模板就绪。



---
---

# Part 1 · 🛒 Go 商城系统（课时 1–109，18 章）

> 整套系统的底座，已在 qingfly.com 生产商用。后端 `edu.mall.backend`（Go+Gin+GORM），C 端 `edu.mall.customer`，管理后台 `edu.mall.frontend`（React + Ant Design Pro）。
> **方法论贯穿全课：先方案 → 后框架 → 逐功能开发 → 部署上线。**
> ⚠️ Kimi 提醒：这里卖的是"课程 SKU"这类虚拟商品，需讲清**开通权限、学习进度、发票、退款后权限回收**的边界——别把它写成通用实物电商。

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

# Part 2 · 🔌 WebSocket 高并发连接层（课时 110–141，3 章）

> 独立仓库 `edu.mall.websocket`。**职责单一：只做消息推送和在线状态管理，不碰任何业务逻辑。**
> 对外三个 gRPC 接口：`WebSocketSend`、`IsOnlineUsers`、`GetOnlineCount`。
> 技术选型：**OpenResty + Nchan**（Nginx 层扛长连接）+ **go-zero zrpc**（Go 服务层）+ **Redis Pub/Sub**（跨进程广播）。目标：单机四万连接压测通过、分布式多节点故障自愈。
> ⚠️ Kimi 提醒：4 万连接只是起点，**真正的考点是发布、Redis 抖动、下游延迟时的稳定性**——不能只讲"Nchan 选型 + 推送 + 压测"。

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

# Part 3 · 💬 IM 即时通讯系统（课时 142–196，5 章）

> 独立仓库 `edu.mall.webchat`（后端）+ `edu.mall.webchatui`（前端）。Go + Gin + React。好友关系、好友申请、群聊、会话列表、消息收发，从数据库模型到前端交互完整落地；消息经 WebSocket（复用 Part 2）实时推送。特色：**大量 AICoding / VibeCoding** 演示用 AI 辅助写前后端代码。
> ⚠️ Kimi 提醒：IM 的核心**不是 CRUD，而是消息一致性**。别学成"聊天 Demo"——要按消息一致性系统设计。

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

# Part 4 · 🤖 AI Agent 智能体系统（课时 197–301，8 章）★最大最重

> 独立仓库 `edu.agent.code`。基于字节 **eino** 框架构建的 Go 原生 Agent 服务。核心理念：**模型、工具、MCP 服务全部抽象为"原子能力"，管理后台自由组合成 Agent，改完即时生效，不用发版。** 支持多轮对话、工具调用、SSE 流式输出、RAG 知识库、人机协作中断恢复。数据库 SQLite（轻量），链路追踪 OpenTelemetry，前端 VibeCoding 对接。
> ⚠️ Kimi 提醒："自由组合、改完即生效"在生产里等价于**动态执行面**。必须补：能力注册与版本化、Schema 校验、权限边界、工具白名单、Prompt Injection 防护、评测回归、成本治理——否则"后台改完即生效"会变成"后台改完即事故"。

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

# Part 5 · 🔗 微服务融合实战直播（课时 302–317，16 节）

> 前面四套系统各自独立。这 16 节课（10 次直播主题 + 演示/方案）把它们用**微服务架构打通并上线 qingfly.com**。命名风格：`演示：…` / `直播第 N 节：微服务集成 XX 改造 M（k）`——一个改造点拆多段连续推进。
> ⚠️ Kimi 提醒：这是"把最难的分布式接缝略过"的高危区——统一鉴权、IM 用户体系改造、群机器人触发 Agent、前端五入口、Docker Compose+CICD 全压在这 16 节，必须讲透。

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

# 附录 A · 老师的代码风格与工程方法论（从课时命名/结构反推）

> 这些是贯穿 317 课时的"隐性教材"，比任何单节课都更能代表老师的工程观：
1. **先文档后代码**：每模块都从 PRD → 技术方案评审一/二/三开始，评审驱动设计。
2. **严格分层**：`router → api → service → dao`，职责单一，禁止跨层调用。
3. **统一响应结构**：`{code, msg, data}` + 全局错误码规范，前端据此处理。
4. **gentool 生成 model**：表结构变更后用工具生成 model，不手写样板。
5. **复杂功能拆一/二/三递进**：如"聊天消息发送六节""订单退款四节"，一节一个可验证增量。
6. **先跑通骨架再填 TODO**：大量"依赖 XX 的 TODO 处理"课时，体现增量交付。
7. **bug 修复 / 实战修复 / 加餐常态化**：不回避真实问题，现场排障是最好的教学。
8. **AI 出初版、人 review**：AICoding/VibeCoding 生成初版后人工迭代，强调人的判断力。
9. **可观测性内建**：traceId + 结构化日志 + OpenTelemetry 全程贯穿。
10. **幂等与状态机是底线**：支付回调幂等、订单状态机、消息 clientMsgId 去重反复出现。
11. **生产级验收导向**：每个模块以压测/联调/演示收尾，而非"能跑就行"。

# 附录 B · 推荐学习路径（三条线）
- **业务开发线**：Part0 → Part1(商城) → Part3(IM) → Part4(Agent) —— 想快速做出完整项目。
- **中间件/高并发线**：Part0 → Part2(WebSocket 连接层) → Part5(融合+统一鉴权+CICD) —— 冲高并发与分布式岗位。
- **AI 工程线**：Part0 → Part4(eino/RAG/MCP/SKILL 全部) —— 深耕 LLM 应用落地。
> 建议顺序仍以课程原始课时序为主（前面为后面铺路），以上按目标做重点取舍。

# 附录 C · 面试冲刺（每模块核心追问链示例）
- **商城**：超卖怎么防？→ 库存扣减顺序？→ 支付回调重放？→ 分布式事务选型？→ 深翻页优化？
- **连接层**：为什么用 Nchan 不用纯 Go？→ Pub/Sub 无 ACK 怎么保证可靠？→ 慢消费者处理？→ 滚动发布不断连怎么做？
- **IM**：seq 如何单调？→ 已读回执风暴？→ 退群后历史可见性？→ @机器人权限边界？
- **Agent**：ReAct vs Planner-Executor？→ checkpoint 为何要持久化？→ prompt injection 防护？→ RAG 何时拒答？
- **融合鉴权**：为什么不各服务自签？→ JWKS 轮换？→ 网关该不该终结业务鉴权？→ 时钟漂移影响？

# 附录 D · 故障手册（Kimi 补充的高危场景）
| 场景 | 现象 | 处置 |
|------|------|------|
| 连接风暴 | Nginx fd 打满 | 限流+背压断慢消费者+扩容 |
| Redis 抖动 | 在线态丢失/Pub-Sub 丢消息 | TTL 兜底+本地缓存+幂等重放 |
| 支付重放 | 重复发货 | outTradeNo 幂等键唯一约束 |
| RAG 幻觉 | 引用错误 | rerank+阈值拒答+citation |
| kid 轮换 | 校验失败潮 | JWKS 按 kid+exp 缓存刷新 |
| Agent 死循环 | MaxStep 耗尽 | 强制输出兜底+成本熔断 |

# 附录 E · 配套资料清单（14 份，随课发放）
| # | 文件 | 类型 | 说明 |
|---|------|------|------|
| 1 | edu.mall.websocket.zip | zip | WebSocket 连接层源码 |
| 2 | WebChat 技术方案文档.pdf | pdf | IM 技术方案 |
| 3 | Go 课程商城项目总结.pdf | pdf | 商城总结 |
| 4 | WebChat PRD.pdf | pdf | IM 需求文档 |
| 5 | WebChat 产品原型图.pdf | pdf | IM 原型 |
| 6 | WebChat 技术方案文档.pdf | pdf | IM 技术方案 |
| 7 | Websocket 实战整体技术方案.pdf | pdf | 连接层方案 |
| 8 | edu.mall.webchatui.zip | zip | IM 前端源码 |
| 9 | edu.mall.webchat.zip | zip | IM 后端源码 |
| 10 | edu.agent.code20260727.zip | zip | Agent 完整源码 |
| 11 | 完整详细技术方案.pdf | pdf | 总技术方案 |
| 12 | 商城后端源码edu.mall.backend.zip | zip | 商城后端源码 |
| 13 | 商城前端edu.mall.customer.zip | zip | C 端前端源码 |
| 14 | 商城管理后台前端edu.mall.frontend.zip | zip | 管理后台源码 |

---

## 📝 对抗式共写记录
| 版本 | 作者 | 贡献 |
|------|------|------|
| v1 | GenericAgent | 严格依据官网真实 317 课时目录树/技术栈/14 份资料搭建忠实骨架 |
| v2 | Kimi | 指出 6 个结构性缺陷；补齐数据表要点/接口签名/面试考点/易错点/DoD；提出"统一身份前置"与"能力域重排"；深入拆解 Agent 运行时与 gRPC 统一鉴权 |
| **v2.0 定稿** | 两者融合 | 保留真实课时结构与老师命名风格（忠于原课），注入 Kimi 的生产级深度与可验收口径 |

> （完 · 《Go 微服务实战》课程学习手册 定稿 v2.0 · 由 GenericAgent × Kimi 对抗共写）
