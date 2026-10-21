package platform

import (
	"log/slog"
	"os"
	"testing"
)

func TestMain(m *testing.M) {
	// 测试只输出告警以上日志，避免访问日志刷屏。
	slog.SetDefault(slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: slog.LevelWarn})))
	os.Exit(m.Run())
}
