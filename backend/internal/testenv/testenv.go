// Package testenv 为集成测试提供隔离的 MySQL 数据库与 Redis key 空间。
//
//	TEST_MYSQL_DSN=campus:campus@tcp(127.0.0.1:3306)/   需要建库权限
//	TEST_REDIS_ADDR=127.0.0.1:6379
//
// 每个测试独占一个随机命名的数据库（结束后删除）和一个随机 Redis 前缀（结束后清理），
// 因此测试可以并行，也不会污染开发数据。未设置环境变量时跳过（make test 会自动启动依赖）。
package testenv

import (
	"context"
	"crypto/rand"
	"database/sql"
	"encoding/hex"
	"os"
	"testing"
	"time"

	"github.com/go-sql-driver/mysql"
	"github.com/redis/go-redis/v9"

	"qingfly/edu-mall/internal/infra/mysqlx"
	"qingfly/edu-mall/internal/infra/redisx"
)

func suffix() string {
	b := make([]byte, 5)
	_, _ = rand.Read(b)
	return hex.EncodeToString(b)
}

// MySQL 返回一个已执行迁移的独立数据库。
func MySQL(t testing.TB) *sql.DB {
	t.Helper()
	dsn := os.Getenv("TEST_MYSQL_DSN")
	if dsn == "" {
		t.Skip("TEST_MYSQL_DSN not set (run `make test`, which starts MySQL/Redis via docker compose)")
	}
	cfg, err := mysql.ParseDSN(dsn)
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), time.Minute)
	defer cancel()
	cfg.DBName = ""
	admin, err := sql.Open("mysql", cfg.FormatDSN())
	if err != nil {
		t.Fatal(err)
	}
	name := "campus_t_" + suffix()
	if _, err = admin.ExecContext(ctx, "CREATE DATABASE `"+name+"` CHARACTER SET utf8mb4 COLLATE utf8mb4_bin"); err != nil {
		admin.Close()
		t.Fatal(err)
	}
	cfg.DBName = name
	db, err := mysqlx.Open(ctx, mysqlx.Options{DSN: cfg.FormatDSN(), MaxOpenConns: 30, MaxIdleConns: 10})
	if err != nil {
		t.Fatal(err)
	}
	if err = mysqlx.Migrate(ctx, db); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		db.Close()
		ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
		defer cancel()
		_, _ = admin.ExecContext(ctx, "DROP DATABASE IF EXISTS `"+name+"`")
		admin.Close()
	})
	return db
}

// Redis 返回客户端和本测试专用的 key 前缀。
func Redis(t testing.TB) (*redis.Client, string) {
	t.Helper()
	addr := os.Getenv("TEST_REDIS_ADDR")
	if addr == "" {
		t.Skip("TEST_REDIS_ADDR not set (run `make test`, which starts MySQL/Redis via docker compose)")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	c, err := redisx.Open(ctx, redisx.Options{Addr: addr, Password: os.Getenv("TEST_REDIS_PASSWORD"), PoolSize: 32})
	if err != nil {
		t.Fatal(err)
	}
	prefix := "t" + suffix()
	t.Cleanup(func() {
		ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		iter := c.Scan(ctx, 0, prefix+":*", 1000).Iterator()
		keys := []string{}
		for iter.Next(ctx) {
			keys = append(keys, iter.Val())
		}
		if len(keys) > 0 {
			c.Unlink(ctx, keys...)
		}
		c.Close()
	})
	return c, prefix
}
