# 校园集市后端

当前应用入口为 `cmd/server`，使用 Go 1.26.8、SQLite WAL、bcrypt、真实 WebSocket 与 SSE。

```powershell
$env:SEED_DEMO='true'
go run ./cmd/server
```

默认监听 `127.0.0.1:8081`。`GET /api/v1/health` 检查数据库；`GET /api/v1/config` 只公开支付/模型可用性，不返回密钥。

配置、运行和边界请看根目录 [README](../README.md)、[API 文档](../docs/api.md) 及 [生产说明](../docs/production-readiness.md)。部署文件在根目录 `compose.yaml`，原来的基础设施占位 Compose 已移除。

`mall/`、`im/`、`ws/`、`agent/` 是原课程内核，测试仍保留，但其中的内存实现和 handler 不属于新应用。原 `cmd/mall`、`cmd/im`、`cmd/websocket`、`cmd/agent` 仅允许显式设置 `ALLOW_UNSAFE_LEGACY_DEMO=true` 的本地课程研究，`APP_ENV=production` 时仍禁止启动；不能用于真实身份、消息或资金处理。
