# API 契约

应用路由统一前缀 `/api/v1`。成功返回 `{ "code": 0, "msg": "OK", "data": ... }`；失败使用真实 HTTP 状态和 `{code,msg,data:null}`。JSON 严格拒绝未知字段和尾随对象。ID 为不透明字符串；金额为整数分；时间为 Unix 毫秒。

## 登录与商品

| 方法/路径 | 请求 | 说明 |
|---|---|---|
| POST `/auth/register` | `name,email,password` | 昵称 2–24 字，密码 10–72 字节；创建 HttpOnly 会话 |
| POST `/auth/login` | `email,password` | 登录；与所有模块共享会话 |
| GET `/auth/me` | 无 | 返回当前 `id,name,email`；匿名 401 |
| POST `/auth/logout` | `{}` | 删除服务端会话，清除 Cookie |
| GET `/products` | `q,category,campus,sort,offset` | 24 条分页；sort 为 new/price-asc/price-desc |
| GET `/products?saved=1` | 同上 | 当前用户收藏，包括已售/下架商品 |
| GET `/products?mine=1` | 同上 | 当前用户发布，包含下架/已售商品 |
| GET `/products/{id}` | 无 | 完整商品与卖家公开昵称 |
| POST `/uploads` | multipart `image` | JPG/PNG ≤5 MB，最长边 ≤6000、≤1600 万像素；重新编码去元数据 |
| POST `/products` | `title,description,price_ct,category,campus,condition,image` | 身份只从会话读取；image 为上传返回路径或 HTTPS 图片地址 |
| DELETE `/products/{id}` | 无 | 仅卖家可下架 available 商品 |
| GET `/favorites` | 无 | 当前用户收藏 ID 列表 |
| PUT/DELETE `/favorites/{id}` | 无 | 设置/取消收藏，幂等 |

`/media/img_<32位hex>.jpg` 提供上传图片，固定路径校验，不能遍历目录。当前上传是公开商品照片，不适合上传私密文件。

## 订单与支付

创建订单只接受：

```json
{"product_id":"p_商品ID","campus":"东区","idempotency_key":"客户端生成的UUID"}
```

`user_id`、金额、费用、卖家、支付成功等字段一律拒绝。幂等键长 8–128 字节，在买家范围内唯一；同键不同商品/校区返回 409。金额由后端查询商品，并与库存预占在同一事务中落库。

| 方法/路径 | 行为 |
|---|---|
| POST `/orders` | 创建订单，201；相同请求重放 200 |
| GET `/orders` | 当前用户买/卖订单，最近 200 条 |
| GET `/orders/{id}` | 买/卖双方可查，含状态事件；其他人 404 |
| POST `/orders/{id}/pay` | `{method:"alipay",mobile:boolean}`；返回服务端签名 `checkout_url`、服务器金额；未配置 503 |
| POST `/orders/{id}/cancel` | 仅买家取消 pending；已创建支付入口时保留库存，等待渠道关单，返回 409 |
| POST `/orders/{id}/handover` | 仅卖家：paid → handover |
| POST `/orders/{id}/complete` | 仅买家：handover → completed |
| POST `/orders/{id}/refund` | `{reason:"退款原因"}`；记录 refund_requested，**不等于已退款** |
| POST `/payments/alipay/notify` | 支付宝表单回调；RSA2、app_id、seller_id、订单金额/交易号校验后入库；成功应答纯文本 `success` |

状态：`pending → paid → handover → completed`，未付可 `cancelled`；已付可申请 `refund_requested`。没有浏览器可直接设置 paid 的接口。每 30 秒处理未付款超时与支付主动查单；对账按尝试时间轮转，单次网关请求 10 秒、每轮 25 秒。验签失败或网络不确定时不会释放预占库存。

## 双人 IM

| 方法/路径 | 请求/响应 |
|---|---|
| POST `/conversations` | `{product_id}`；为当前买家与该商品卖家建立/复用会话 |
| GET `/conversations` | 最近 200 会话、对方昵称、商品摘要、最后消息、未读数 |
| POST `/conversations/{id}/messages` | `{client_msg_id,content}`；1–4000 字；201 新消息，200 幂等重发 |
| GET `/conversations/{id}/messages?after=N` | 最多 100 条、`next_seq`、`has_more`，按游标反复补拉 |
| POST `/conversations/{id}/read` | `{seq}`，水位只能单调上升且不得超过会话当前 seq |
| GET `/ws` | Cookie 鉴权升级；每用户最多 5 连接；25 秒心跳；退出后下次心跳关闭旧连接 |

消息 ID 幂等范围为 `(conversation_id,sender_id,client_msg_id)`；同键不同内容 409。非成员读取或发送均返回 404。WebSocket 只广播 `{type:"message",conversation_id}` / `{type:"order",order_id}` 等通知，HTTP 数据库接口是权威来源；慢客户端队列满时断开并补拉，避免静默丢消息。

## Agent

动态助手目录和完整管理员接口见 [动态 Agent API](dynamic-agents.md#api)。用户通过 `GET /agents` 选择已发布助手，`POST /agent/runs` 可携带 `agent_id`；新会话绑定助手，跨助手复用线程返回 409。返回的 run 包含 `agent_id`、`agent_version` 和 `agent_name`。

`GET /agent/threads` 获取当前用户历史；`GET /agent/threads/{id}` 返回最近 30 轮，跨用户 404。

`POST /agent/runs`：

```json
{"prompt":"300 元以内找一个耳机","thread_id":"可省略以新建对话","idempotency_key":"客户端UUID"}
```

SSE 格式是 `event: <type>\ndata: <JSON>\n\n`。事件为 `start`（run/thread ID）、`delta`（`text`）、`tool`（名称与状态）、`error`（用户可读错误）、`done`（持久化终态）。客户端用 AbortController 停止生成，后端保存已有文本及 cancelled；超时/输出截断/过滤记 failed。重放已完成请求不会再次调用模型；并发运行返回 409。90 秒运行期限，2 分钟后回收遗留 running 记录。

模型只能调用当前发布版本绑定的工具，函数名由能力 ID 生成，客户端不能指定权限。内置实现包括 `search_products({query})`、`my_orders({})`，也可绑定后端可信 MCP 服务的公开只读工具。身份在内置工具执行时从服务器注入，客户端 scopes 和模型提供的 user_id 均无效。原始模型推理不作为界面事件输出。没有可用模型配置或绑定能力停用时请求返回 503。

## 运行边界

当前 API 是单节点服务：SQLite 事务与进程内 WebSocket Hub；不支持多副本共享一个数据库文件。订单/会话列表目前是最近 200 条上限，需更深历史时应增加游标分页。动态 Agent 管理台与 MCP 已开放；群聊、图片消息、好友申请、RAG 和商城运营后台尚未提供。
