// Command testfixture prepares an isolated Playwright database. It is never
// compiled into the application image and exposes no HTTP endpoints.
//
// 安全约束：只允许 APP_ENV=development，且数据库名、Redis 前缀都必须以 campus_e2e 开头；
// 每次运行都会重建该数据库并清空该前缀下的 Redis key。
package main

import (
	"context"
	"database/sql"
	"log"
	"os"
	"strings"
	"time"

	"github.com/go-sql-driver/mysql"
	"github.com/redis/go-redis/v9"
	"golang.org/x/crypto/bcrypt"

	"qingfly/edu-mall/internal/infra/mysqlx"
	"qingfly/edu-mall/internal/infra/redisx"
)

func main() {
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()
	dsn := os.Getenv("DATABASE_DSN")
	prefix := os.Getenv("REDIS_PREFIX")
	cfg, err := mysql.ParseDSN(dsn)
	if err != nil {
		log.Fatal(err)
	}
	if os.Getenv("APP_ENV") != "development" || !strings.HasPrefix(cfg.DBName, "campus_e2e") || !strings.HasPrefix(prefix, "campus_e2e") {
		log.Fatal("fixture requires APP_ENV=development, a campus_e2e* database and REDIS_PREFIX")
	}
	name := cfg.DBName
	cfg.DBName = ""
	admin, err := sql.Open("mysql", cfg.FormatDSN())
	if err != nil {
		log.Fatal(err)
	}
	for _, stmt := range []string{"DROP DATABASE IF EXISTS `" + name + "`", "CREATE DATABASE `" + name + "` CHARACTER SET utf8mb4 COLLATE utf8mb4_bin"} {
		if _, err = admin.ExecContext(ctx, stmt); err != nil {
			log.Fatal(err)
		}
	}
	admin.Close()
	db, err := mysqlx.Open(ctx, mysqlx.Options{DSN: dsn})
	if err != nil {
		log.Fatal(err)
	}
	defer db.Close()
	if err = mysqlx.Migrate(ctx, db); err != nil {
		log.Fatal(err)
	}
	hash, err := bcrypt.GenerateFromPassword([]byte("Studio-test-only-123"), bcrypt.DefaultCost)
	if err != nil {
		log.Fatal(err)
	}
	if _, err = db.ExecContext(ctx, "INSERT INTO users (id,email,name,password,created_at) VALUES(?,?,?,?,?)", "e2e_admin", "studio@example.test", "工作室测试员", string(hash), time.Now().UnixMilli()); err != nil {
		log.Fatal(err)
	}
	rdb, err := redisx.Open(ctx, redisx.Options{Addr: envOr("REDIS_ADDR", "127.0.0.1:6379"), Password: os.Getenv("REDIS_PASSWORD")})
	if err != nil {
		log.Fatal(err)
	}
	defer rdb.Close()
	if err = flushPrefix(ctx, rdb, prefix); err != nil {
		log.Fatal(err)
	}
}

func envOr(k, d string) string {
	if v := os.Getenv(k); v != "" {
		return v
	}
	return d
}

func flushPrefix(ctx context.Context, rdb *redis.Client, prefix string) error {
	iter := rdb.Scan(ctx, 0, prefix+":*", 500).Iterator()
	batch := []string{}
	for iter.Next(ctx) {
		batch = append(batch, iter.Val())
		if len(batch) == 500 {
			if err := rdb.Unlink(ctx, batch...).Err(); err != nil {
				return err
			}
			batch = batch[:0]
		}
	}
	if err := iter.Err(); err != nil {
		return err
	}
	if len(batch) > 0 {
		return rdb.Unlink(ctx, batch...).Err()
	}
	return nil
}
