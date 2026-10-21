// Package mysqlx 封装 MySQL 连接池、启动期迁移与事务重试。
package mysqlx

import (
	"context"
	"database/sql"
	"embed"
	"errors"
	"fmt"
	"io/fs"
	"math/rand/v2"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/go-sql-driver/mysql"
)

//go:embed migrations/*.sql
var migrations embed.FS

// Options 是连接池参数。默认值按"单实例 2C4G + MySQL max_connections=500，
// 预留多实例扩容"估算：每实例最多 50 个连接，空闲保留 25 个。
type Options struct {
	DSN             string
	MaxOpenConns    int
	MaxIdleConns    int
	ConnMaxLifetime time.Duration
	ConnMaxIdleTime time.Duration
}

// Open 创建连接池并等待数据库可用（容器编排时 MySQL 往往比应用晚就绪）。
func Open(ctx context.Context, o Options) (*sql.DB, error) {
	cfg, err := mysql.ParseDSN(o.DSN)
	if err != nil {
		return nil, fmt.Errorf("parse DATABASE_DSN: %w", err)
	}
	if cfg.Params == nil {
		cfg.Params = map[string]string{}
	}
	cfg.Loc = time.UTC
	cfg.Collation = "utf8mb4_unicode_ci"
	// utf8mb4 下客户端插值是安全的，可以把一次查询从 prepare+execute 两次往返降为一次。
	cfg.InterpolateParams = true
	cfg.MultiStatements = false
	if cfg.Timeout == 0 {
		cfg.Timeout = 5 * time.Second
	}
	if cfg.ReadTimeout == 0 {
		cfg.ReadTimeout = 30 * time.Second
	}
	if cfg.WriteTimeout == 0 {
		cfg.WriteTimeout = 30 * time.Second
	}
	if _, ok := cfg.Params["sql_mode"]; !ok {
		cfg.Params["sql_mode"] = "'STRICT_TRANS_TABLES,NO_ENGINE_SUBSTITUTION,ONLY_FULL_GROUP_BY,ERROR_FOR_DIVISION_BY_ZERO'"
	}
	connector, err := mysql.NewConnector(cfg)
	if err != nil {
		return nil, err
	}
	db := sql.OpenDB(connector)
	db.SetMaxOpenConns(orDefault(o.MaxOpenConns, 50))
	db.SetMaxIdleConns(orDefault(o.MaxIdleConns, 25))
	// 小于 MySQL wait_timeout（默认 8h）且足够短，避免拿到被中间件/服务端关闭的连接。
	db.SetConnMaxLifetime(orDuration(o.ConnMaxLifetime, 30*time.Minute))
	db.SetConnMaxIdleTime(orDuration(o.ConnMaxIdleTime, 5*time.Minute))
	for attempt := 0; ; attempt++ {
		pingCtx, cancel := context.WithTimeout(ctx, 3*time.Second)
		err = db.PingContext(pingCtx)
		cancel()
		if err == nil {
			return db, nil
		}
		if attempt >= 20 || ctx.Err() != nil {
			db.Close()
			return nil, fmt.Errorf("mysql not reachable: %w", err)
		}
		select {
		case <-ctx.Done():
		case <-time.After(time.Second):
		}
	}
}

func orDefault(v, d int) int {
	if v > 0 {
		return v
	}
	return d
}
func orDuration(v, d time.Duration) time.Duration {
	if v > 0 {
		return v
	}
	return d
}

// Migrate 按文件名顺序执行未应用的迁移。多实例同时启动时用 GET_LOCK 互斥，
// 只有拿到锁的实例执行 DDL，其余实例等待后发现已应用直接跳过。
func Migrate(ctx context.Context, db *sql.DB) error {
	conn, err := db.Conn(ctx)
	if err != nil {
		return err
	}
	defer conn.Close()
	var got sql.NullInt64
	if err = conn.QueryRowContext(ctx, "SELECT GET_LOCK('campus_schema_migrate', 60)").Scan(&got); err != nil {
		return err
	}
	if !got.Valid || got.Int64 != 1 {
		return errors.New("timeout waiting for migration lock")
	}
	defer conn.ExecContext(context.WithoutCancel(ctx), "SELECT RELEASE_LOCK('campus_schema_migrate')")
	if _, err = conn.ExecContext(ctx, "CREATE TABLE IF NOT EXISTS schema_migrations (version INT NOT NULL PRIMARY KEY, name VARCHAR(255) NOT NULL, applied_at BIGINT NOT NULL) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4"); err != nil {
		return err
	}
	applied := map[int]bool{}
	rows, err := conn.QueryContext(ctx, "SELECT version FROM schema_migrations")
	if err != nil {
		return err
	}
	for rows.Next() {
		var v int
		if err = rows.Scan(&v); err != nil {
			rows.Close()
			return err
		}
		applied[v] = true
	}
	rows.Close()
	if err = rows.Err(); err != nil {
		return err
	}
	files, err := fs.Glob(migrations, "migrations/*.sql")
	if err != nil {
		return err
	}
	sort.Strings(files)
	for _, file := range files {
		name := strings.TrimPrefix(file, "migrations/")
		version, err := strconv.Atoi(strings.SplitN(name, "_", 2)[0])
		if err != nil {
			return fmt.Errorf("migration %s must start with a number", name)
		}
		if applied[version] {
			continue
		}
		raw, err := migrations.ReadFile(file)
		if err != nil {
			return err
		}
		for i, stmt := range SplitStatements(string(raw)) {
			if _, err = conn.ExecContext(ctx, stmt); err != nil {
				return fmt.Errorf("migration %s statement %d: %w", name, i+1, err)
			}
		}
		if _, err = conn.ExecContext(ctx, "INSERT INTO schema_migrations (version, name, applied_at) VALUES (?, ?, ?)", version, name, time.Now().UnixMilli()); err != nil {
			return err
		}
	}
	return nil
}

// SplitStatements 去掉整行注释后按"行尾分号"切分语句。
func SplitStatements(src string) []string {
	var out []string
	var cur strings.Builder
	for _, line := range strings.Split(src, "\n") {
		trimmed := strings.TrimSpace(line)
		if trimmed == "" || strings.HasPrefix(trimmed, "--") {
			continue
		}
		cur.WriteString(line)
		cur.WriteString("\n")
		if strings.HasSuffix(trimmed, ";") {
			stmt := strings.TrimSuffix(strings.TrimSpace(cur.String()), ";")
			out = append(out, stmt)
			cur.Reset()
		}
	}
	if s := strings.TrimSpace(cur.String()); s != "" {
		out = append(out, s)
	}
	return out
}

// MySQL 错误码。
const (
	ErrDuplicateEntry  = 1062
	ErrLockWaitTimeout = 1205
	ErrDeadlock        = 1213
)

func code(err error) uint16 {
	var me *mysql.MySQLError
	if errors.As(err, &me) {
		return me.Number
	}
	return 0
}

// IsDuplicate 判断唯一键冲突。幂等写入的标准做法是"直接 INSERT，冲突后再查"，
// 而不是先 SELECT 再 INSERT（RR 隔离级别下快照读看不到并发插入，且 SELECT ... FOR UPDATE
// 锁不存在的行会加间隙锁，并发时容易死锁）。
func IsDuplicate(err error) bool { return code(err) == ErrDuplicateEntry }

// DuplicateKeyName 返回冲突的索引名（兼容 MySQL 8 的 "表名.索引名" 格式）。
func DuplicateKeyName(err error) string {
	var me *mysql.MySQLError
	if !errors.As(err, &me) || me.Number != ErrDuplicateEntry {
		return ""
	}
	i := strings.LastIndex(me.Message, "for key '")
	if i < 0 {
		return ""
	}
	key := strings.TrimSuffix(me.Message[i+len("for key '"):], "'")
	if j := strings.LastIndex(key, "."); j >= 0 {
		key = key[j+1:]
	}
	return key
}

// IsRetryable 判断事务是否可以整体重试：死锁被 InnoDB 选为牺牲者，或锁等待超时。
func IsRetryable(err error) bool {
	c := code(err)
	return c == ErrDeadlock || c == ErrLockWaitTimeout
}

// WithTx 执行事务，遇到死锁/锁等待超时时按指数退避 + 抖动重试（最多 3 次）。
// fn 必须是可重放的：除了数据库写入以外不要产生外部副作用。
func WithTx(ctx context.Context, db *sql.DB, fn func(*sql.Tx) error) error {
	var err error
	for attempt := 0; attempt < 3; attempt++ {
		err = runTx(ctx, db, fn)
		if err == nil || !IsRetryable(err) {
			return err
		}
		backoff := time.Duration(10<<attempt)*time.Millisecond + time.Duration(rand.IntN(10))*time.Millisecond
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(backoff):
		}
	}
	return err
}

func runTx(ctx context.Context, db *sql.DB, fn func(*sql.Tx) error) (err error) {
	tx, err := db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer func() {
		if p := recover(); p != nil {
			_ = tx.Rollback()
			panic(p)
		}
		if err != nil {
			_ = tx.Rollback()
		}
	}()
	if err = fn(tx); err != nil {
		return err
	}
	return tx.Commit()
}

// EscapeLike 转义 LIKE 通配符，防止用户输入 "%" / "_" 造成全表匹配或语义错误。
func EscapeLike(s string) string {
	return strings.NewReplacer(`\`, `\\`, `%`, `\%`, `_`, `\_`).Replace(s)
}
