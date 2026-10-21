// Package redisx 创建 Redis 客户端并统一 key 命名。
package redisx

import (
	"context"
	"fmt"
	"strings"
	"time"

	"github.com/redis/go-redis/v9"
)

type Options struct {
	Addr     string
	Password string
	DB       int
	PoolSize int
}

// Open 创建连接池并等待 Redis 就绪。读写超时设得较短：Redis 抖动时快速失败，
// 让调用方走降级逻辑（缓存回源、本地限流），而不是把 HTTP 请求一起拖住。
func Open(ctx context.Context, o Options) (*redis.Client, error) {
	c := redis.NewClient(&redis.Options{
		Addr:            o.Addr,
		Password:        o.Password,
		DB:              o.DB,
		PoolSize:        o.PoolSize,
		MinIdleConns:    4,
		DialTimeout:     3 * time.Second,
		ReadTimeout:     time.Second,
		WriteTimeout:    time.Second,
		PoolTimeout:     2 * time.Second,
		ConnMaxIdleTime: 5 * time.Minute,
	})
	var err error
	for attempt := 0; attempt < 20; attempt++ {
		pingCtx, cancel := context.WithTimeout(ctx, 2*time.Second)
		err = c.Ping(pingCtx).Err()
		cancel()
		if err == nil {
			return c, nil
		}
		select {
		case <-ctx.Done():
			c.Close()
			return nil, ctx.Err()
		case <-time.After(time.Second):
		}
	}
	c.Close()
	return nil, fmt.Errorf("redis not reachable: %w", err)
}

// Keys 生成带业务前缀的 key，例如 Keys("campus").Of("flash", id, "stock") => "campus:flash:<id>:stock"。
// 统一前缀便于多环境共用一个 Redis、按前缀做容量统计和清理。
type Keys string

func (k Keys) Of(parts ...string) string {
	return string(k) + ":" + strings.Join(parts, ":")
}
