# 配置、发布和运行动态 Agent

Agent 工作室现在可以管理模型能力、内置工具能力、可信 MCP 工具，以及组合这些能力的助手。配置存入数据库，发布生成不可变版本；新请求读取最新发布版本，已有 run 保存自己的版本快照。无需改代码或重新构建镜像即可创建、编辑、发布、停用和恢复助手。

## 打开管理员工作室

1. 正常注册自己的账号。在浏览器开发者工具的同源控制台执行以下只读请求，获得自己的用户 ID：

   ```javascript
   fetch('/api/v1/auth/me').then(r => r.json()).then(r => console.log(r.data.id))
   ```

2. 在后端设置 `ADMIN_USER_IDS` 为这个已有 ID，多个 ID 用英文逗号分隔。使用 ID 而不是未经验证的邮箱，避免注册相同邮箱形式就获得管理权。没有默认生产管理员，也不会自动提权首位用户。
3. 重启后端并刷新登录态。在「我的 → Agent 工作室」进入管理界面，或访问 `/#agent-admin`。所有 `/admin/*` 接口也独立校验管理员身份，隐藏菜单不作为权限保护。

普通用户只能看到已发布且启用的助手、公开说明和工具显示名称，看不到系统提示词、模型连接细节或连接凭据。

## 添加模型和工具

最简单的模型连接沿用 `MODEL_BASE_URL`、`MODEL_API_KEY`、`MODEL_NAME`，在能力目录中叫 `default`。新增“模型能力”时可指定该连接支持的其他模型名，也可留空使用连接默认模型。

需要多个模型提供商或 MCP 服务时，复制 [连接配置样例](../deploy/agent-connections.example.json) 到服务器私有配置目录，修改地址与环境变量名称，设置 `AGENT_CONNECTIONS_FILE` 指向该文件。Compose 中可放在 `./secrets/agent-connections.json`，对应 `/run/secrets/agent-connections.json`。额外凭据变量如 `VISION_MODEL_API_KEY`、`CAMPUS_KNOWLEDGE_TOKEN` 放入私有 `.env`；Compose 只将它们传给 API。

连接文件只保存密钥的环境变量名称，真实密钥不会进入能力表、发布历史、运行快照或浏览器。生产只接受 HTTPS；本地测试只允许 loopback HTTP；MCP 禁止跨地址重定向。新增网络连接属于运维信任边界，需要重新加载后端配置；已有连接中的模型名、工具与助手组合均可在工作室动态管理。

内置工具包括：

| 工具 | 数据边界 |
|---|---|
| `search_products` | 当前可售商品；参数只有搜索词；金额单位为分 |
| `my_orders` | 当前会话用户自己的订单；不接受 user_id 或其他身份参数 |

MCP 工具的操作顺序是：添加能力 → 选择可信服务 → 读取工具目录 → 选择工具 → 确认仅访问公开只读数据 → 保存。后端从实际 `tools/list` 结果获取参数 schema，不信任浏览器伪造的 schema。

MCP 仅允许运维声明 `public_data_only:true` 的连接、工具声明 `readOnlyHint:true`，并由管理员确认只读后使用。**这些声明不能代替 MCP 服务端的数据授权**：连接凭据必须仅能读取适合平台用户访问的公开资料，不能使用可读所有用户私密资料或可操作资金的高权限凭据。平台不会向 MCP 透传用户 Cookie、支付凭据或内部身份令牌。

## 发布、停用与恢复

草稿包含名称、介绍、系统提示、欢迎语、最多四条建议问题、一个模型能力、最多八个工具，以及运行限额。参数范围为 1–8 轮、每轮 128–4096 输出 token、10–90 秒总时限。

- **保存草稿**：不改变用户侧已发布名称、提示词和工具。卡片会提示未发布更改。
- **发布**：事务中解析绑定能力并复制为快照，生成 v1、v2 等版本。模型未配置密钥时仍可完成配置发布，用户侧明确显示暂不可用。
- **修改能力**：已有发布版本保留原配置；重新发布引用它的助手后采用新配置。
- **停用助手/能力**：运行前、每轮模型请求前、工具调用前重新检查；MCP 目录请求完成后、实际 `tools/call` 前再次检查。已发出的远程读取不能被平台回滚。
- **恢复历史版本**：复制历史配置创建一个新版本，不覆盖原历史；引用的能力已删除或停用时拒绝恢复。
- **移除助手**：归档并禁用，保留用户对话与审计记录。
- **删除能力**：仍被非归档助手草稿或当前发布版本引用时返回 409。紧急情况下可先停用。

配置编辑采用修订号乐观锁，过期提交返回 409，不静默覆盖别人刚刚保存的内容。

## 运行与协议

用户在 AI 页选择助手。新会话绑定助手 ID，不能把别人的线程或其他助手的线程复用到当前请求。每次运行记录 `agent_id`、`agent_version` 和实际配置快照；模型名从连接默认值解析后也写入运行快照，凭据不写入。

运行器只向模型暴露当前快照绑定的工具，并在执行时重新匹配白名单。即使模型自行构造 `my_orders` 或其他未绑定名称也会被拒绝。所有工具输入经过 JSON Schema 校验；禁止 schema 加载外部 HTTP/文件资源。

工具调用状态写入 `agent_tool_events`：started、completed、failed。MCP `isError` 和参数错误都记录 failed，并通过 SSE 呈现；可恢复的错误会作为工具观察交给模型继续回答，权限/连接故障会停止运行。不会把失败工具画成成功。

MCP 实现覆盖 Streamable HTTP 的 initialize、initialized 通知、tools/list 游标分页、tools/call、会话 ID 与协议版本头，以及 JSON/SSE 响应。单次 MCP 任务限制 20 秒，目录最多 128 项、8 页，schema 32 KiB、参数 8 KiB、结果 64 KiB；目录变化或只读声明消失时拒绝执行并要求重新发布。暂不支持旧 HTTP+SSE 传输、stdio、MCP OAuth 自动发现、服务端 sampling/elicitation，也不宣称是所有 MCP 扩展的通用客户端。

## API

所有接口沿用 HttpOnly 登录会话和统一 JSON 错误响应。管理写接口需要管理员角色，更新/发布/归档请求携带当前 `revision`。

| 方法/路径（统一 `/api/v1`） | 用途 |
|---|---|
| GET `/agents` | 已发布助手，含可用性、工具显示名称、版本、欢迎语与建议问题 |
| GET `/admin/agent-catalog` | 模型连接标识、MCP 连接标识、能力列表、助手草稿及未发布更改状态 |
| POST `/admin/capabilities` | 创建 model/tool/mcp 能力 |
| PUT `/admin/capabilities/{id}` | 修改能力配置、启停状态，携带 revision |
| DELETE `/admin/capabilities/{id}` | 删除未被引用能力 |
| POST `/admin/agents` | 创建草稿 |
| PUT `/admin/agents/{id}` | 更新草稿/启停，携带 revision |
| POST `/admin/agents/{id}/publish` | `{revision}` 发布；另传 `source_version` 可恢复历史配置为新版本 |
| GET `/admin/agents/{id}/versions` | 最近 100 发布版本及完整无密钥快照 |
| DELETE `/admin/agents/{id}` | `{revision}` 归档 |
| GET `/admin/mcp/{id}/tools` | 从后端可信连接发现工具 |
| GET `/admin/agent-audit` | 最近 100 条配置管理审计 |
| POST `/agent/runs` | 在原请求上增加 `agent_id`，省略时兼容默认小集；线程与幂等键仍受身份隔离 |

## 验证与边界

Go 测试覆盖管理权限、草稿隔离、乐观锁、发布后即时可见、快照稳定、工具引用保护、MCP 两种响应格式、参数校验、阻止重定向、实际工具调用、失败审计，以及读取目录过程中撤销权限。浏览器测试从管理员登录开始创建模型能力、组合新助手、发布、切换、在 390px 手机上修改/恢复版本/停用。

测试用管理员仅由独立 `cmd/testfixture` 写入 `e2e-*` 测试数据库；该命令要求 development 环境，不进入应用镜像，也没有 HTTP 初始化或提权接口。不要把测试账号用于生产。

这次补齐的是动态配置、组合、发布与受控执行，不包含 RAG、写操作人工审批、群机器人或 Eino 框架迁移。真实模型/MCP 服务凭据未提供，外部协议通过本地确定性服务验证。

协议依据：[MCP Streamable HTTP](https://modelcontextprotocol.io/specification/2025-06-18/basic/transports)、[MCP tools](https://modelcontextprotocol.io/specification/2025-06-18/server/tools)。
