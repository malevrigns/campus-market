// Package domain 定义跨层复用的业务实体、值对象与业务错误。
//
// 领域层不依赖 HTTP、SQL、Redis 或任何具体框架：
//   - 业务服务返回 *domain.Error，由应用层统一映射为 HTTP 状态码；
//   - 数据层把 sql.ErrNoRows、唯一键冲突等存储错误翻译成领域错误；
//   - 外部适配器把网关/网络错误翻译成领域错误或保留原始错误供上层记录。
package domain

import (
	"errors"
	"fmt"
)

// ErrorKind 是业务错误的稳定分类，数值与 HTTP 状态码对齐，方便应用层映射。
type ErrorKind int

const (
	KindInvalid      ErrorKind = 400
	KindUnauthorized ErrorKind = 401
	KindForbidden    ErrorKind = 403
	KindNotFound     ErrorKind = 404
	KindConflict     ErrorKind = 409
	KindGone         ErrorKind = 410
	KindTooMany      ErrorKind = 429
	KindBadGateway   ErrorKind = 502
	KindUnavailable  ErrorKind = 503
	KindTimeout      ErrorKind = 504
	KindInternal     ErrorKind = 500
)

// Error 是对外可展示的业务错误。Message 会原样返回给前端，因此不得包含内部细节。
type Error struct {
	Kind       ErrorKind
	Message    string
	RetryAfter int // 秒；仅 429/503 等需要 Retry-After 的响应使用
	Err        error
}

func (e *Error) Error() string { return e.Message }
func (e *Error) Unwrap() error { return e.Err }

// NewError 创建业务错误。
func NewError(kind ErrorKind, message string) *Error {
	return &Error{Kind: kind, Message: message}
}

// Errorf 创建带格式化消息的业务错误。
func Errorf(kind ErrorKind, format string, args ...any) *Error {
	return &Error{Kind: kind, Message: fmt.Sprintf(format, args...)}
}

func Invalid(message string) *Error { return NewError(KindInvalid, message) }
func Invalidf(format string, a ...any) *Error {
	return Errorf(KindInvalid, format, a...)
}
func Unauthorized(message string) *Error { return NewError(KindUnauthorized, message) }
func Forbidden(message string) *Error    { return NewError(KindForbidden, message) }
func NotFound(message string) *Error     { return NewError(KindNotFound, message) }
func Conflict(message string) *Error     { return NewError(KindConflict, message) }
func Gone(message string) *Error         { return NewError(KindGone, message) }
func Unavailable(message string) *Error  { return NewError(KindUnavailable, message) }

// Retryable 创建带 Retry-After 秒数的错误。
func Retryable(kind ErrorKind, message string, seconds int) *Error {
	return &Error{Kind: kind, Message: message, RetryAfter: seconds}
}

// KindOf 返回错误链中第一个 *Error 的分类；非业务错误返回 KindInternal。
func KindOf(err error) ErrorKind {
	var e *Error
	if errors.As(err, &e) {
		return e.Kind
	}
	return KindInternal
}

// RetryAfterOf 返回业务错误携带的 Retry-After 秒数。
func RetryAfterOf(err error) int {
	var e *Error
	if errors.As(err, &e) {
		return e.RetryAfter
	}
	return 0
}

// DuplicateError 由数据层在唯一键冲突时返回，保留约束名供业务层做幂等恢复。
type DuplicateError struct {
	Constraint string
	Err        error
}

func (e *DuplicateError) Error() string { return "duplicate key: " + e.Constraint }
func (e *DuplicateError) Unwrap() error { return e.Err }

// DuplicateConstraint 返回唯一键冲突的约束名；不是该错误时返回空字符串。
func DuplicateConstraint(err error) string {
	var e *DuplicateError
	if errors.As(err, &e) {
		return e.Constraint
	}
	return ""
}

// AsError 把任意错误转换为业务错误，便于应用层统一处理。
func AsError(err error) (*Error, bool) {
	var e *Error
	ok := errors.As(err, &e)
	return e, ok
}

// ToolResultError 表示工具执行的可恢复失败：模型可以收到错误观察并继续回答。
type ToolResultError struct{ Message string }

func (e *ToolResultError) Error() string { return e.Message }
