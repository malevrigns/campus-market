# 注册、登录与验证码

现在支持邮箱/手机号验证码注册、账号密码登录、邮箱/短信验证码登录、忘记密码、联系方式验证/绑定。新注册必须先验证联系方式；原有邮箱账号仍可用密码登录，个人中心会提示补充验证。

## 本地体验

PowerShell 启动后端时明确设置：

```powershell
cd backend
$env:AUTH_DELIVERY_MODE='local'
$env:SEED_DEMO='true'
go run ./cmd/server
```

前端保持 `npm run dev`。注册或找回密码中点击「获取验证码」，再点击「查看测试验证码」。页面会显示并填入后端生成的验证码。**这是本地测试收件箱，不会发出真实邮件或短信。** 收件箱需要本次发送返回的随机访问令牌，并仅允许本机访问；生产模式拒绝启用 local。后端重启后测试收件箱清空，需要重新获取验证码。

自动浏览器测试使用独立 `15183/18091` 端口，不占用手动体验页面。原先 `5173` 与 `15173` 两套环境 Cookie 互相覆盖的问题已修复：开发环境按请求站点分别命名 Cookie；生产使用 Secure/HttpOnly 的 `__Host-campus_session`。

## 真实邮箱与短信

在后端配置 `AUTH_DELIVERY_MODE=live`。生产还必须设置长度至少 32 字节的随机 `AUTH_CODE_SECRET`，并在重启间保持一致；不要提交到 Git。Compose 的 API 会读取私有 `.env`，前端不会获取这些配置。

SMTP 邮件配置：

| 变量 | 含义 |
|---|---|
| `SMTP_HOST` | 发送邮箱的 SMTP 主机 |
| `SMTP_PORT` | 默认 STARTTLS 587；implicit TLS 通常为 465 |
| `SMTP_TLS_MODE` | starttls 或 implicit，禁止明文连接 |
| `SMTP_USER` / `SMTP_PASSWORD` | 邮箱账号和 SMTP 授权码；根据服务商要求配置 |
| `SMTP_FROM` | 已获准发送的邮箱地址 |

阿里云短信配置：

| 变量 | 含义 |
|---|---|
| `SMS_ACCESS_KEY_ID` / `SMS_ACCESS_KEY_SECRET` | 仅有短信发送权限的凭据 |
| `SMS_SIGN_NAME` | 审核通过的短信签名 |
| `SMS_TEMPLATE_CODE` | 审核通过的验证码模板，包含 `code` 变量 |
| `SMS_REGION` | 默认 cn-hangzhou |

当前手机渠道支持中国大陆 11 位手机号，后端统一存为 `+86` 格式；发送使用固定阿里云 HTTPS 地址，不接受客户端提供网关 URL。仅邮箱或仅短信完整配置均可启动，未配置的渠道会在前端显示不可用，部分凭据配置不完整会拒绝启动。

发送失败会返回错误，不会显示成功。SMTP 以 DATA 接受确认为提交成功，之后 QUIT 失败不会把已接受的验证码误标失效。实际投递是否进入邮箱收件箱、短信是否到达手机，仍需使用真实服务凭据和自己的收件设备完成验收。

## 接口

统一 `/api/v1` 前缀：

| 方法/路径 | 请求/行为 |
|---|---|
| POST `/auth/codes` | `{channel:"email"或"sms",target,purpose}`，purpose 为 register/login/reset_password/bind；返回 challenge_id、期限、重发冷却、掩码地址 |
| POST `/auth/register` | `{name,password,channel,target,challenge_id,code}`；创建已验证身份并在同一事务签发会话 |
| POST `/auth/login` | `{account,password}`；account 为邮箱或手机号，旧 email 参数仍兼容 |
| POST `/auth/code-login` | `{channel,target,challenge_id,code}`；仅已注册账号可登录 |
| POST `/auth/password/reset` | 同上加 password；原子修改密码、吊销所有会话和待用验证码 |
| POST `/auth/bind` | 需登录，提交验证证明后绑定/验证联系方式；不支持直接替换已经验证的联系方式 |
| POST `/auth/dev-inbox` | 仅 local、本机可用；`{challenge_id,delivery_token}` 获取对应测试码 |
| GET `/auth/me` | 增加 phone/email_verified/phone_verified |
| GET `/config` | auth 字段显示邮箱/短信能力、是否测试收件箱与密码最小长度；无密钥 |

验证码是密码学随机 6 位数字，5 分钟有效，最多 5 次错误尝试，成功后不能重放。摘要使用服务器 HMAC，数据库不保存明文验证码。验证码严格绑定地址、用途和发送时的账号 ID，登录验证码不能用于重置密码。相同地址、相同用途 60 秒重发冷却；地址每天 10 次、来源 IP 每小时 60 次、每渠道每天 500 次的持久化上限用于限制滥用。

密码重置与旧密码登录之间增加了事务内复查，避免刚撤销会话又被一个并发旧密码请求重新创建。前端也区分“密码错误”和“登录会话失效”，旧请求迟到的 401 不会把刚登录的新会话清除。请求兼容没有 AbortSignal.any/timeout 的浏览器。

## 体验调整

顶栏可以切换明暗主题，选择会保存在当前浏览器。AI 助手改为可拖动悬浮图标，点击在当前页面展开；位置记忆、拖动防误触、窗口缩放和手机底栏均有约束。进入 IM 聊天页时浮标自动避开消息发送区。旧 `#agent` 链接兼容打开浮窗；Agent 工作室依然是管理员专用页面。

验证区分实际后端闭环和 UI 模拟场景：账号、上传、商城、双人聊天、动态配置使用真实本地数据库和接口；主题/浮窗的可控 SSE、延迟身份响应使用模拟网络响应；SMTP/短信协议测试使用本地服务器或测试 HTTP 传输，未发送真实验证码。
