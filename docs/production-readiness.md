# 交付与上线说明

本次把原本前后端脱节的演示改造成了可运行的校园二手交易闭环。用户确认保留校园二手业务；课程资料用于约束金额、身份、消息一致性与 Agent 权限，未把项目转换为课程商城。

**当前适合本地体验、集成测试和单节点试运行；不能直接承诺“完整生产级商城 / IM / Agent 平台”。** 真实商户、资金结算、校园认证、退款处理与运维条件仍需补齐。下面分别记录已实现、已验证和未完成内容。

## 支付边界

前端仅提交 `product_id`、面交校区、幂等键；在得到订单后展示服务器金额，再请求支付入口并跳转。前端没有支付定时器、生成订单号、伪造回调、计算服务费或直接标记付款成功的逻辑。

后端以整数分保存商品价格和订单快照；数据库事务串行预占一件商品，按买家隔离幂等键。支付密钥只从服务器 PEM 文件读取。

已实现支付宝电脑网站/手机网站支付适配。需要商户开通相应产品，并设置以下环境变量：

| 配置 | 作用 |
|---|---|
| `PUBLIC_ORIGIN` | 不带路径的 HTTPS 站点 origin，生成回调和返回地址 |
| `ALIPAY_APP_ID` | 当前商户应用 ID |
| `ALIPAY_SELLER_ID` | 收款商户 ID，回调必须匹配 |
| `ALIPAY_PRIVATE_KEY_FILE` | 商户 RSA 私钥 PEM 文件，≥2048 位 |
| `ALIPAY_PUBLIC_KEY_FILE` | 支付宝 RSA 公钥 PEM 文件，≥2048 位 |
| `ALIPAY_GATEWAY` | 默认正式网关；开发可设置 `https://openapi-sandbox.dl.alipaydev.com/gateway.do`，生产禁止沙箱 |

Compose 密钥路径为容器内 `/run/secrets/...`，在宿主 `./secrets` 放入文件，授予 UID 10001 只读权限。不要把私钥填写到前端环境变量或提交到 Git。当前实现是**公钥模式**，尚未支持支付宝证书模式与微信支付。商户字段全空时支付不可用，部分填写会拒绝启动。

回调必须通过 RSA2 验签以及 app/seller/金额核对；交易号绑定、回调 ID、订单状态都在同一事务持久化。浏览器返回页面不算支付成功。主动查单会校验网关响应签名，修复回调丢失；超时关单后再次查询，确认关闭才释放商品。对账按最近尝试时间轮转，避免老故障订单长期堵塞后续订单。

退款目前只记录申请与状态，**没有真实退款执行、审核后台、拒绝/回退流程或卖家打款**。商户网站支付也不等于平台托管或合规分账，因此页面已去掉托管和“7 天可退”承诺。正式收款前必须确定校园平台的收款主体、卖家结算方式、售后规则，并接好对应渠道能力。

## Agent 与 IM

`MODEL_BASE_URL` 配置兼容 Chat Completions 的服务根路径，例如以 `/v1` 结束的 HTTPS 地址；后端追加 `/chat/completions`。同时设置 `MODEL_API_KEY` 与 `MODEL_NAME`。服务需支持 SSE `stream=true`、function tools 和标准 `finish_reason`。

动态 Agent 工作室已支持模型、内置工具、可信只读 MCP 工具的管理与组合，以及草稿、原子发布、版本恢复、停用和运行快照。配置 `ADMIN_USER_IDS` 后在「我的」进入工作室，详见 [动态 Agent 使用说明](dynamic-agents.md)。

模型可按已发布权限查商品和当前用户本人订单，不能付款、退款、修改数据或访问任意用户身份。客户端不能传 scopes。运行限额可配置，硬上限 8 轮、90 秒；截断/过滤/异常不记成功，遗留 running 记录 2 分钟后自动回收。真实服务商未提供，因此只用本地确定性模型/MCP 端点验证协议和失败状态，**未完成真实模型效果/费用验收**。

当前 IM 是交易场景双人文本聊天。消息持久化先于通知，服务器分配序号；发送者和客户端消息 ID 联合去重，重连靠数据库补拉。WebSocket 在线的是连接状态，不会伪装对方在线或自动冒充卖家回复。

## 运行与备份

API 默认只监听本机。生产 Compose 仅开放 Caddy 的 80/443；TLS 证书和业务数据分别持久化。`APP_ENV=production` 要求 HTTPS origin，Cookie 设 Secure；服务端提供健康检查、超时、优雅关闭、请求 ID、JSON 错误日志、限流以及图片解码大小/并发限制。

`TRUSTED_PROXY_CIDRS` 默认空，只信任直连地址。Compose 明确限定应用专属网络 `172.30.250.0/24`，Caddy 覆盖 `X-Real-IP`。如果修改 Docker 网络，请同步修改该配置；禁止直接把任意客户端转发头当作真实 IP。普通流量与支付回调采用独立限流桶。

如果构建环境无法访问 Go 默认下载源，可以在 `.env` 设置 `GOPROXY=https://goproxy.cn,direct`，再构建；仍保留 Go checksum 校验。

SQLite 使用 WAL + FULL 同步，适合单节点。**不要运行多个 API 副本，不要把 SQLite 文件放到 NFS/共享盘，不要将进程内 Hub 当作跨节点消息系统。** 开始多副本部署之前，应迁移事务存储与消息通知。

本地备份（先停止开发后端，PowerShell，项目根目录）：

```powershell
$backupDir = Join-Path 'D:\campus-backups' (Get-Date -Format 'yyyyMMdd-HHmmss')
New-Item -ItemType Directory -Path $backupDir -Force | Out-Null
Copy-Item -LiteralPath '.\backend\data' -Destination $backupDir -Recurse
```

复制完整数据目录，不要在服务运行时只复制 `campus.db` 而遗漏 WAL。生产卷备份建议停写后复制整个卷，或使用 SQLite 一致性备份工具；同时备份上传目录。恢复演练必须验证用户、订单事件、消息和图片完整，再开放写入。

## 验证证据

实际工具结果记录于本次会话，测试源码可重新运行：

| 验证 | 当前结果与边界 |
|---|---|
| Go 测试 / vet | 通过；含身份、越权、并发预占、金额篡改、回调重放、消息游标与幂等 |
| Agent 故障回归 | 已观察截断误判、遗留运行和数据库锁测试失败；修正后通过 |
| 支付对账 | 本地签名网关验证回调丢失补偿、验签失败保留库存、关闭不能逆转已付、31 单调度公平性 |
| Linux race 检测 | WSL Ubuntu `CGO_ENABLED=1 go test -race ./internal/platform` 通过 |
| 前端单测 / build | 通过；覆盖分段 UTF-8 SSE、CRLF 和金额分转换；构建已拆包 |
| 浏览器 | Chrome 1440/768/390/360 宽度；双账号上传、聊天、刷新、下单/取消通过；截图见 `frontend/test-results` |
| npm audit | 0 已知漏洞 |
| govulncheck | 已调用符号 0 漏洞；当前导入包 0 漏洞；一个未导入模块的漏洞不在当前可达调用路径 |
| Docker / Caddy | 两个镜像构建通过，Compose 与 Caddy 配置校验通过；本地 HTTPS 访问生产构建并成功注册，Secure/HttpOnly/SameSite=Strict 正确，无页面异常；使用 localhost 测试证书，公网证书/真实商户/模型尚未验收 |

Windows 本机没有启用 CGO，直接 `-race` 会失败；已使用 WSL 的 GCC 环境完成该检查，不把跳过当通过。测试网关与模型只存在于 Go 测试，生产代码没有模拟成功开关。

前端构建后主应用约 225 KB、UI 依赖约 304 KB，聊天/Agent/订单按需加载；压缩后分别约 73 KB 和 99 KB。桌面、手机、消息、Agent、结算和生产容器截图保存在 [screenshots](screenshots/)。镜像构建最初遭遇默认 Go 下载源超时，使用可配置的 `GOPROXY` 后成功；基础镜像已固定到本次实际验证的 digest。

## 与课程及生产目标的差距

| 范围 | 当前实现 | 上线前或后续工作 |
|---|---|---|
| 资金 | 商户支付宝收款协议、回调/查单 | 真实商户联调、分账结算、退款执行/审核、对账告警和争议处理 |
| 账号 | 邮箱/手机验证注册、密码/验证码登录、找回密码、联系方式绑定、会话隔离 | 真实投递验收、校园身份认证、设备管理、进一步风控与内容审核 |
| 商城后台 | 卖家自己的发布/下架、买卖订单 | RBAC 管理界面、运营审核、商品编辑、多图、完整历史分页 |
| IM | 双人文本、未读水位、重连 | 好友、群管理、多媒体、已读回执展示、群机器人、跨节点推送 |
| Agent | 多轮、SSE、动态能力目录与助手发布、MCP 只读工具、运行版本快照 | Eino 框架迁移、RAG、写操作人工审批、配额计费 |
| 架构 | 一个可部署 API，模块共享事务 | MySQL/Redis/gRPC 服务拆分、分布式身份/通知与灰度回滚 |
| 运维 | Compose、健康检查、CI 配置 | CI 远端实际执行、负载/SLO、监控告警、密钥轮转、定期备份与恢复演练 |
| 客户端 | 响应式 Web | 真机 Safari/Android 键盘与网络测试；未制作原生 App 或小程序 |

设计参考：实地打开并查看了 [MotionSites OYLA](https://motionsites.ai/?prompt=oyla) 的公开预览，参考截图为 `docs/motionsites-oyla.png`。商品摄影、非对称排版与层级用于校园业务适配，未获取付费模板源码。支付协议参照 [支付宝官方 SDK](https://github.com/alipay/alipay-sdk-python-all) 和[官方异步通知说明](https://aipay.alipay.com/docs/ai-web-app-payment-qianyi/api-list/async-notify-verify.html)。
