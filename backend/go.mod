module qingfly/edu-mall

go 1.23

// 说明：本模块的核心业务逻辑（订单状态机、支付回调幂等、IM 消息一致性、
// Agent 引擎、统一鉴权、雪花 ID）均以 Go 标准库实现并保证 `go build ./...` 通过，
// 便于在无外网环境验证。Gin/GORM/go-zero/eino/Nchan 等框架依赖在各服务的
// adapter 层以接口形式接入（见各 internal/* 的 interface.go），生产部署时
// 替换为真实驱动即可，不影响核心逻辑与测试。

