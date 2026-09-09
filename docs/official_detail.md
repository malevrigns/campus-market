# Go 微服务实战：商城 × AI Agent × IM 生产级全栈项目课程【套课】

## 课程说明
【这是一套课程，从0到整套生产可用的系统设计，开发，部署】

这门课要做的，是把**商城、智能体、IM、WebSocket** 四套独立系统用微服务架构拼在一起，让它们跑成一个完整的微服务系统，最后把整套系统部署上线到 qingfly.com 生产环境。

课程包含了五个子系统，每个都是独立仓库、独立 CICD、独立上线：

### 🛒 商城系统
一套完整的在线教育商城，Go + Gin + GORM 做后端，React 19 + Ant Design 做两端前端。包含课程商品管理、订单系统、购物车、RBAC 权限体系、用户多端登录（手机号/微信/App）。管理后台动态路由 + 权限菜单、用户侧前端鉴权 ，订单，商品，课程推荐，学习追踪的完整在线教育体系。这是整套系统的底座，已经在 qingfly.com 上生产商用。

### 💬 IM 系统
一套功能完整的即时通讯系统，Go + Gin 后端，React 前端。好友关系模型、好友申请流程、群聊创建与管理、会话列表、消息收发，全部从数据库模型到前端交互完整体现。消息通过 WebSocket 实时推送，前端连接管理封装。

### 🤖 智能体系统
基于字节跳动 eino 框架构建的 AI Agent 服务。模型、工具、MCP 服务全部抽象为"原子能力"，在管理后台自由组合成 Agent，改完即时生效，不用发版。支持多轮对话、工具调用、SSE 流式输出。这套架构完全替代了传统写死在代码里的 Agent 做法。

### 🔌 WebSocket 连接层
基于 go-zero zrpc 构建的独立 WebSocket 连接服务，职责单一——只做消息推送和在线状态管理，不碰任何业务逻辑。已提供 `WebSocketSend`、`IsOnlineUsers`、`GetOnlineCount` 三个 gRPC 接口，通过 Redis Pub/Sub 实现跨进程消息广播。

### 🔗 微服务融合实战直播课
前面四套系统各自是独立的。这门融合课用 10 次直播把四套系统用微服务架构打通，做完直接上线 qingfly.com。

你在这 10 次课里会做完的事：
1. gRPC 统一鉴权：IM 和 Agent 不再自己签发 token，全部透传给商城 backend 做统一验证。一次登录，全系统通行；踢人、吊销只有一个入口。
2. IM 用户体系改造：IM 不再自存 nickname/avatar 副本，所有用户资料由 Backend 统一管理，IM 和 Agent 通过 gRPC 实时获取。
3. 群机器人 @ 触发 Agent：群里加一个机器人，绑定一个 Agent，@ 它就触发推理。回复通过 WebSocket 逐字推送到群里，群里所有人都能看到"它在思考"的过程。
4. Agent 可配置化：模型、工具、MCP 服务在管理后台自由增删，Agent 通过关联表自由组合，运营自助完成，无需程序员发版。
5. 前端融合：IM 聊天、Agent 对话、课程浏览，全部整合到一个用户端。顶部导航五个入口——首页、课程、智能体、消息、我的——各自独立场景，互不干扰。
6. Docker Compose 部署 + CICD：每个服务独立 Docker Compose、独立 CNB 流水线，打 tag 自动构建镜像、自动推送服务器、自动拉起容器。

## 学完后简历亮点
不是一个 demo，是一个真正在生产环境运行的中小型微服务系统。你可以：
- 独立搭建 Go 微服务架构（gRPC + Etcd + Docker Compose）
- 设计并实现 eino Agent 自定义引擎
- 实现 WebSocket + SSE 双通道实时通信系统
- 完成一套含鉴权、IM、AI、商城的全栈项目
- 把 qingfly.com 作为你简历上的作品集

## 技术栈全景
| 层级 | 技术 |
|------|------|
| 后端框架 | Go 1.23–1.25 + Gin + GORM |
| 微服务 | go-zero zrpc + gRPC + Etcd |
| AI Agent | eino + MCP + Milvus |
| 数据库 | MySQL + Redis + SQLite |
| 前端 | React 18/19 + Ant Design Pro + Vite |
| 实时通信 | WebSocket + SSE |
| 部署 | Docker Compose + CNB CICD |
