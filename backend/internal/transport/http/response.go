// Package httpapi 是应用层：HTTP 路由、中间件、请求 DTO、身份获取、响应与错误映射。
// 业务规则与存储细节都在 service / port / adaptor 中。
package httpapi

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"net/http"
	"strconv"

	"qingfly/edu-mall/internal/domain"
)

type endpoint func(http.ResponseWriter, *http.Request, *domain.User) error

func decode(r *http.Request, v any) error {
	d := json.NewDecoder(r.Body)
	d.DisallowUnknownFields()
	if err := d.Decode(v); err != nil {
		return domain.Invalid("请求字段或格式不正确")
	}
	if err := d.Decode(&struct{}{}); err != io.EOF {
		return domain.Invalid("请求只能包含一个 JSON 对象")
	}
	return nil
}

func result(w http.ResponseWriter, status int, data any) error {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.WriteHeader(status)
	return json.NewEncoder(w).Encode(map[string]any{"code": 0, "msg": "OK", "data": data})
}

// writeError 把业务错误映射为真实 HTTP 状态；未知错误记录日志但不泄露内部细节。
func writeError(w http.ResponseWriter, err error) {
	var a *domain.Error
	status, message, retryAfter := 500, "服务暂时不可用，请稍后重试", 0
	logged := false
	switch {
	case errors.As(err, &a):
		status, message, retryAfter = int(a.Kind), a.Message, a.RetryAfter
	case domain.DuplicateConstraint(err) != "":
		status, message = 409, "请求冲突，请刷新后重试"
	case errors.Is(err, sql.ErrNoRows):
		status, message = 404, "内容不存在或无权访问"
	case errors.Is(err, context.DeadlineExceeded):
		status, message = 504, "处理超时，请稍后重试"
	default:
		slog.Error("request failed", "error", err)
		logged = true
	}
	if status >= 500 && status != 504 && !logged {
		slog.Error("request failed", "error", err)
	}
	if retryAfter > 0 {
		w.Header().Set("Retry-After", strconv.Itoa(retryAfter))
	}
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(map[string]any{"code": status, "msg": message, "data": nil})
}

func domainInternal() error {
	return domain.NewError(domain.KindInternal, "服务暂时不可用，请稍后重试")
}

func domainForbidden(message string) error { return domain.Forbidden(message) }

func domainRetryable(kind domain.ErrorKind, message string, seconds int) error {
	return domain.Retryable(kind, message, seconds)
}

func errorStatus(err error) int {
	var e *domain.Error
	if errors.As(err, &e) {
		return int(e.Kind)
	}
	return 500
}
