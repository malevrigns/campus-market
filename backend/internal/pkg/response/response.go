// Package response 定义全系统统一的 HTTP 响应结构与全局错误码规范。
// 约定：所有接口返回 {code, msg, data}，code==0 表示成功；前端据此统一处理。
// 仅依赖标准库，便于离线构建与单测；Gin 等框架通过 adapter 调用本包即可。
package response

import (
	"encoding/json"
	"net/http"
)

// Body 是全站统一的响应体。
type Body struct {
	Code int         `json:"code"` // 0 成功，非 0 为业务/系统错误码
	Msg  string      `json:"msg"`  // 人类可读信息（可国际化，不泄露内部细节）
	Data interface{} `json:"data"` // 业务数据，可为 nil
}

// Code 常用错误码（分段：1xxx 通用 / 2xxx 鉴权 / 3xxx 商城 / 4xxx IM / 5xxx Agent）。
type Code int

const (
	OK              Code = 0
	ErrBadRequest   Code = 10001 // 参数错误
	ErrNotFound     Code = 10404
	ErrInternal     Code = 15000
	ErrUnauthorized Code = 20001 // 未登录/token 无效
	ErrForbidden    Code = 20003 // 无权限（scope 不足）
	ErrOrderState   Code = 30001 // 订单状态机非法流转
	ErrStock        Code = 30002 // 库存不足/超卖
	ErrPaySign      Code = 30003 // 支付回调验签失败
)

func (c Code) String() string {
	switch c {
	case OK:
		return "OK"
	case ErrBadRequest:
		return "bad request"
	case ErrNotFound:
		return "not found"
	case ErrInternal:
		return "internal error"
	case ErrUnauthorized:
		return "unauthorized"
	case ErrForbidden:
		return "forbidden"
	case ErrOrderState:
		return "illegal order state transition"
	case ErrStock:
		return "insufficient stock"
	case ErrPaySign:
		return "payment sign mismatch"
	default:
		return "error"
	}
}

// statusFor 依据业务码映射 HTTP 状态码（业务码仍放在 body.code）。
func statusFor(c Code) int {
	switch c {
	case ErrUnauthorized:
		return http.StatusUnauthorized
	case ErrForbidden:
		return http.StatusForbidden
	case ErrNotFound:
		return http.StatusNotFound
	default:
		return http.StatusOK
	}
}

// Write 写出统一响应。
func Write(w http.ResponseWriter, code Code, data interface{}) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.WriteHeader(statusFor(code))
	_ = json.NewEncoder(w).Encode(Body{Code: int(code), Msg: code.String(), Data: data})
}

// JSON 写出成功响应。
func JSON(w http.ResponseWriter, data interface{}) { Write(w, OK, data) }
