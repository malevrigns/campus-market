# edu-mall · Go 微服务实战全栈项目骨架

> 依据 qingfly.com《Go 微服务实战：商城 × AI Agent × IM 生产级全栈项目》(id=8) 的真实技术栈与架构，
> 产出的**可编译、可运行、带核心逻辑单测**的生产级 Go 项目骨架。
>
> - `go build ./...` ✅ 全绿 · `go vet ./...` ✅ 全绿
> - 四个服务可独立运行：`mall` / `im` / `websocket` / `agent`
> - 核心不变量有单元测试证明：**支付回调幂等**、**IM seq 单调 + clientMsgId 去重**

## 设计原则（对齐课程方法论）


...[Truncated]...
e/engine` ReAct 循环 + Guardrail + Checkpoint + SSE |
| Part 5 融合 | 统一身份契约（backend 唯一签发校验）落在 `internal/pkg/auth`，各服务透传 Principal |

## 一句话总结
这套代码把课程的"先文档后代码、严格分层、统一响应、状态机、幂等、seq 一致性、Agent 治理"
等工程方法论落成了**能跑起来、测得过的真实 Go 代码**——既是学习范本，也是可直接扩展的生产起点。

