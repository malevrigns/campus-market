package mysqlx_test

import (
	"context"
	"database/sql"
	"errors"
	"sync"
	"sync/atomic"
	"testing"

	"qingfly/edu-mall/internal/infra/mysqlx"
	"qingfly/edu-mall/internal/testenv"
)

func TestSplitStatements(t *testing.T) {
	got := mysqlx.SplitStatements("-- comment;\nCREATE TABLE a (\n  id INT -- inline\n);\n\nCREATE TABLE b (id INT);\n")
	if len(got) != 2 || got[1] != "CREATE TABLE b (id INT)" {
		t.Fatalf("unexpected split: %q", got)
	}
}

func TestEscapeLike(t *testing.T) {
	if got := mysqlx.EscapeLike(`50%_off\`); got != `50\%\_off\\` {
		t.Fatalf("escape: %s", got)
	}
}

func TestMigrateIsIdempotentAndConcurrent(t *testing.T) {
	db := testenv.MySQL(t)
	var wg sync.WaitGroup
	errs := make(chan error, 4)
	for i := 0; i < 4; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			errs <- mysqlx.Migrate(context.Background(), db)
		}()
	}
	wg.Wait()
	close(errs)
	for e := range errs {
		if e != nil {
			t.Fatal(e)
		}
	}
	var n int
	if err := db.QueryRow("SELECT COUNT(*) FROM schema_migrations").Scan(&n); err != nil || n < 1 {
		t.Fatalf("migrations recorded: %d %v", n, err)
	}
}

func TestDuplicateKeyNameAndRetry(t *testing.T) {
	db := testenv.MySQL(t)
	if _, err := db.Exec("INSERT INTO users (id,email,name,password,created_at) VALUES ('u1','a@x.test','a','x',1)"); err != nil {
		t.Fatal(err)
	}
	_, err := db.Exec("INSERT INTO users (id,email,name,password,created_at) VALUES ('u2','a@x.test','b','x',1)")
	if !mysqlx.IsDuplicate(err) || mysqlx.DuplicateKeyName(err) != "uk_users_email" {
		t.Fatalf("duplicate detection: %v / %q", err, mysqlx.DuplicateKeyName(err))
	}
	_, err = db.Exec("INSERT INTO users (id,email,name,password,created_at) VALUES ('u1','b@x.test','b','x',1)")
	if mysqlx.DuplicateKeyName(err) != "PRIMARY" {
		t.Fatalf("primary detection: %q", mysqlx.DuplicateKeyName(err))
	}
	// WithTx 不重试业务错误，并回滚。
	sentinel := errors.New("business")
	var calls atomic.Int32
	err = mysqlx.WithTx(context.Background(), db, func(tx *sql.Tx) error {
		calls.Add(1)
		if _, err := tx.Exec("UPDATE users SET name='changed' WHERE id='u1'"); err != nil {
			return err
		}
		return sentinel
	})
	if !errors.Is(err, sentinel) || calls.Load() != 1 {
		t.Fatalf("business error must not retry: %v calls=%d", err, calls.Load())
	}
	var name string
	if err = db.QueryRow("SELECT name FROM users WHERE id='u1'").Scan(&name); err != nil || name != "a" {
		t.Fatalf("rollback: %q %v", name, err)
	}
}

// TestDeadlockIsRetried 构造经典的交叉加锁死锁：InnoDB 选择一个事务回滚（1213），WithTx 重试后两个事务都成功。
func TestDeadlockIsRetried(t *testing.T) {
	db := testenv.MySQL(t)
	for _, id := range []string{"a", "b"} {
		if _, err := db.Exec("INSERT INTO users (id,email,name,password,created_at) VALUES (?,?,?,?,1)", id, id+"@x.test", id, "x"); err != nil {
			t.Fatal(err)
		}
	}
	start := make(chan struct{})
	locked := sync.WaitGroup{}
	locked.Add(2)
	var attempts atomic.Int32
	run := func(first, second string) error {
		once := sync.Once{}
		return mysqlx.WithTx(context.Background(), db, func(tx *sql.Tx) error {
			attempts.Add(1)
			if _, err := tx.Exec("UPDATE users SET name=CONCAT(name,'1') WHERE id=?", first); err != nil {
				return err
			}
			once.Do(func() {
				locked.Done()
				locked.Wait()
				<-start
			})
			_, err := tx.Exec("UPDATE users SET name=CONCAT(name,'2') WHERE id=?", second)
			return err
		})
	}
	errs := make(chan error, 2)
	go func() { errs <- run("a", "b") }()
	go func() { errs <- run("b", "a") }()
	locked.Wait()
	close(start)
	for i := 0; i < 2; i++ {
		if err := <-errs; err != nil {
			t.Fatalf("transaction failed after retry: %v", err)
		}
	}
	if attempts.Load() < 3 {
		t.Fatalf("expected a deadlock retry, attempts=%d", attempts.Load())
	}
}
