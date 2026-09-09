# 校园集市 · Campus Market

一个为校园而生的二手流转平台前端。发布、浏览、沟通、面交，在指尖完成每一次高效交易。

视觉设计对齐 [motionsites.ai](https://motionsites.ai) 的 shadcn/ui 设计系统：深黑底 + 单一电光蓝强调色 + 克制动效，去 AI 味的极简高级感。

## ✨ 功能特性

- **Hero 首屏**：视差光晕 + LIVE 状态徽章 + 数字滚动统计
- **好物集市**：分类筛选 / 搜索 / 排序 / 只看收藏，单列瀑布流商品卡
- **商品详情抽屉**：图片轮播 / 成色标签 / 卖家信息 / 收藏 / 私聊 / 立即购买
- **完整支付闭环**：安全结算 → 选支付方式（校园钱包/微信/支付宝）→ 面交校区选择 → 费用明细 → 支付成功页（订单号 + 状态时间线）
- **IM 聊天面板**：与卖家实时沟通
- **发布闲置**：表单 + 拖拽上传
- **多端适配**：Web PC 为主，移动端 H5 响应式

## 🛠 技术栈

| 类别 | 选型 |
|------|------|
| 框架 | React 19 |
| 构建 | Vite |
| UI 组件库 | Ant Design 5（深色主题）|
| 样式 | Tailwind CSS |
| 图标 | @ant-design/icons |

## 🎨 设计规范（motionsites.ai token）

```css
--background: #121212   /* 深黑底 */
--card:       #292929   /* 深灰卡片 */
--foreground: #f5f5f7   /* 主文字 */
--primary:    #1a40ff   /* 唯一强调色：电光蓝 */
--border:     #303030
--radius:     1rem      /* 统一圆角 16px */
```

克制动效：`shimmer` / `fade-up` / `pulse-dot` / `marquee`，transition 全部 `all .3s ease-out`。

## 🚀 快速开始

```bash
npm install
npm run dev        # http://localhost:5173
```

### 构建生产版本

```bash
npm run build      # 输出到 dist/
npm run preview    # 本地预览构建产物
```

## 📁 目录结构

```
src/
├── main.jsx              # 入口
├── index.css             # 设计系统核心（token + 动效）
├── App.jsx               # 状态中枢 + AntD 深色主题配置
├── data/products.js      # 模拟商品数据
├── utils/ripple.js       # 按钮点击涟漪
└── components/
    ├── Aurora.jsx         # 背景（网格 + 微弱光晕）
    ├── Navbar.jsx         # 顶部导航
    ├── Hero.jsx           # 首屏（视差 + 数字滚动）
    ├── Market.jsx         # 集市网格 + 筛选
    ├── ProductCard.jsx    # 商品卡片
    ├── DetailDrawer.jsx   # 详情抽屉
    ├── Checkout.jsx       # 支付结算
    ├── ChatPanel.jsx      # IM 聊天
    ├── PublishModal.jsx   # 发布闲置
    ├── Reveal.jsx         # scroll 入场动画
    └── CountUp.jsx        # 数字滚动
```

## 📄 License

MIT
