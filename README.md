# Campus Market · 校园集市

一个为校园而生的二手流转平台，前后端一体的 monorepo。发布、浏览、沟通、面交，在指尖完成每一次高效交易。

- **前端** `frontend/`：React 19 + Ant Design 5 + Tailwind CSS + Vite，视觉对齐 [motionsites.ai](https://motionsites.ai) 的 shadcn/ui 设计系统（深黑底 + 单一电光蓝强调色 + 克制动效）
- **后端** `backend/`：Go 1.23，四个服务 —— 商城(mall) / IM(im) / WebSocket(ws) / Agent(agent)，核心业务逻辑用标准库实现，框架在 adapter 层以接口接入

## 📁 目录结构

```
campus-market/
├── frontend/                 # React 前端
│   ├── src/
│   │   ├── App.jsx           # 状态中枢 + AntD 深色主题
│   │   ├── index.css         # 设计系统核心
│   │   ├── data/products.js  # 模拟商品数据
│   │   ├── utils/ripple.js   # 点击涟漪
│   │   └── components/       # Aurora/Navbar/Hero/Market/ProductCard
│   │                         # DetailDrawer/Checkout/ChatPanel/PublishModal
│   │                         # Reveal/CountUp
│   ├── package.json
│   ├── vite.config.js
│   └── tailwind.config.js
└── backend/                  # Go 后端 (module qingfly/edu-mall)
    ├── cmd/                  # 四个服务入口
    │   ├── mall/main.go      #   商城 :8081
    │   ├── im/main.go        #   IM :8082
    │   ├── websocket/main.go #   WS :8083
    │   └── agent/main.go     #   Agent :8084
    ├── mall/                 # 订单状态机 + 支付回调幂等
    ├── im/                   # IM 消息一致性
    ├── ws/                   # WebSocket hub/server
    ├── agent/                # Agent 引擎 + capability/policy
    ├── internal/pkg/         # auth(统一鉴权)/response/snowflake(雪花ID)
    ├── console.html          # 调试控制台
    └── docker-compose.yml
```

## 🚀 快速开始

### 前端
```bash
cd frontend
npm install
npm run dev        # http://localhost:5173
npm run build      # 生产构建 → dist/
```

### 后端
```bash
cd backend
go build ./...     # 编译校验
go run ./cmd/mall          # 商城 :8081
go run ./cmd/im            # IM :8082
go run ./cmd/websocket     # WS :8083
go run ./cmd/agent         # Agent :8084
```

> 后端核心逻辑（订单状态机、支付回调幂等、IM 消息一致性、Agent 引擎、统一鉴权、雪花 ID）均以 Go 标准库实现并保证 `go build ./...` 通过；Gin/GORM/go-zero/eino/Nchan 等框架依赖在各服务的 adapter 层以接口形式接入，生产部署时替换为真实驱动即可。

## ✨ 功能特性

- **Hero 首屏**：视差光晕 + LIVE 状态徽章 + 数字滚动统计
- **好物集市**：分类筛选 / 搜索 / 排序 / 只看收藏，单列瀑布流商品卡
- **商品详情抽屉**：图片轮播 / 成色标签 / 卖家信息 / 收藏 / 私聊 / 立即购买
- **完整支付闭环**：安全结算 → 选支付方式（校园钱包/微信/支付宝）→ 面交校区选择 → 费用明细 → 支付成功页（订单号 + 状态时间线）
- **IM 聊天面板**：实时消息收发
- **响应式**：桌面端多列网格 / H5 单列全宽 / 聊天面板全屏化

## 📄 License

MIT
