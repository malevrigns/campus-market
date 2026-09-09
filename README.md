# 校园集市 · Campus Market

校园二手商城：真实商品发布与收藏、后端订单/支付、双人即时聊天、AI 购物助手，以及桌面和手机端界面。

**当前交付是可运行、可测试的单节点版本。支付和 AI 没有配置时明确不可用，不会模拟成功。动态 Agent 工作室与受控 MCP 工具已实现；商户真实收款验收、卖家资金结算、退款处理后台、校园身份验证、群聊/好友、RAG 和高可用运维仍需后续建设。** 完整边界见 [交付与上线说明](docs/production-readiness.md)。

## 本地运行

需要 Node.js 22、Go 1.26.8（`go.mod` 已声明自动工具链）。在两个终端分别执行。

后端，PowerShell：

```powershell
cd backend
$env:SEED_DEMO = 'true'
$env:AUTH_DELIVERY_MODE = 'local'
go run ./cmd/server
```

前端：

```powershell
cd frontend
npm ci
npm run dev
```

打开 **http://127.0.0.1:5173**。前端通过同源代理访问后端 `127.0.0.1:8081`。不需要复制 token 或手工初始化账号。

`SEED_DEMO=true` 只生成明确标记的展示商品，它们不能购买或联系卖家。注册两个自己的测试账号，上传真实照片，即可体验发布、聊天、下单与取消。已有数据不会被启动脚本清空。

数据保存在 `backend/data/campus.db`，照片保存在 `backend/data/uploads`。目录已加入 Git 忽略。退出登录会吊销服务端会话；重启保留用户、收藏、订单和消息。

## 已实现

- 商城：搜索、分类/校区筛选、排序/分页、收藏、我的发布、JPG/PNG 上传、商品详情、下架。
- 身份：邮箱/手机号验证码注册、密码或验证码登录、找回密码、联系方式验证绑定、bcrypt 散列、隔离的 HttpOnly 会话、用途绑定和单次验证码。支持 SMTP 与阿里云短信，详见 [账号验证](docs/account-verification.md)。
- 订单：后端整数分计价、商品快照、事务预占防超卖、用户隔离幂等、15 分钟未付款超时、取消释放、面交/收货状态与审计记录。
- 支付：后端支付宝 PC/H5 签名入口、RSA2 回调验签、商户/金额/交易号校验、持久去重、主动查单补偿、过期关单。**前端只提交商品和面交校区、打开支付地址并查询服务器状态。**
- IM：真实双人会话、WebSocket 通知、持久消息、服务端 seq、发送者维度幂等、游标补拉、未读水位、断线重连及失败重发。
- Agent：动态模型/内置工具/MCP 能力目录、管理员工作室、草稿/发布/版本恢复/停用、用户侧助手切换、逐 token SSE、多轮持久对话、运行快照、工具失败审计。未配置模型时没有假回答。详见 [动态 Agent 使用说明](docs/dynamic-agents.md)。
- 界面：参考 [MotionSites 的公开 OYLA 电商预览](https://motionsites.ai/?prompt=oyla) 的商品摄影与留白，支持明暗主题切换与记忆。AI 通过可拖动悬浮图标在当前页对话。移动端双列商品、全屏 IM、底栏、安全区、减少动态效果、键盘焦点均有适配。

## 验证

```powershell
cd backend
go test ./...
go vet ./...
go build ./cmd/server
go run golang.org/x/vuln/cmd/govulncheck@v1.8.0 ./...
```

```powershell
cd frontend
npm test
npm run build
npx playwright install chromium
npm run test:e2e
```

Windows 如需使用已安装的 Chrome：先执行 `$env:PLAYWRIGHT_CHANNEL='chrome'`。端到端测试会启动独立的 `18091/15183` 服务和独立数据库，不调用真实收款、短信、邮件或模型。Linux 可运行 `CGO_ENABLED=1 go test -race ./...`。

## 目录

```text
backend/cmd/server/          唯一受支持的应用入口
backend/internal/platform/  身份、商城、订单、支付、IM、Agent、SQLite、上传
frontend/src/components/    商城、登录/发布、订单、消息、Agent 及结算界面
frontend/src/lib/           API 与 SSE 客户端、金额展示/录入
frontend/e2e/               真实双账号浏览器验收
deploy/Caddyfile            HTTPS、SPA、API/WS/SSE 反向代理
Dockerfile + compose.yaml   前后端镜像、数据卷、健康检查
docs/api.md                 API 契约
docs/production-readiness.md 运行、支付接入、数据备份与未完成范围
```

原 `backend/{mall,im,ws,agent}` 包保留为课程研究代码，**没有接入当前应用路由**。原四个 `cmd` 入口默认拒绝启动，并在生产模式下始终拒绝运行。不要使用旧调试控制台进行真实交易。

## 部署

从项目根目录复制 `.env.example` 到 `.env`，设置已解析到服务器的 `DOMAIN`。然后运行：

```sh
docker compose up -d --build
docker compose ps
docker compose logs --tail=100 api web
```

Caddy 管理 HTTPS；数据库和上传目录使用持久卷，API 不对公网暴露端口。上线前请阅读 [支付、模型和生产差距](docs/production-readiness.md)，不能把镜像能启动等同于业务上线验收。
