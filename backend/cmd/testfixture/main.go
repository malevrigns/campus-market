// Command testfixture prepares an isolated Playwright database. It is never
// compiled into the application image and exposes no HTTP endpoints.
package main

import (
	"golang.org/x/crypto/bcrypt"
	"log"
	"os"
	"path/filepath"
	"qingfly/edu-mall/internal/platform"
	"strings"
	"time"
)

func main() {
	path := os.Getenv("DATABASE_PATH")
	if os.Getenv("APP_ENV") != "development" || !strings.HasPrefix(filepath.Base(path), "e2e-") {
		log.Fatal("fixture requires development and an e2e-* database")
	}
	db, e := platform.OpenStore(path)
	if e != nil {
		log.Fatal(e)
	}
	defer db.Close()
	hash, e := bcrypt.GenerateFromPassword([]byte("Studio-test-only-123"), bcrypt.DefaultCost)
	if e != nil {
		log.Fatal(e)
	}
	if _, e = db.Exec("INSERT OR IGNORE INTO users VALUES(?,?,?,?,?)", "e2e_admin", "studio@example.test", "工作室测试员", string(hash), time.Now().UnixMilli()); e != nil {
		log.Fatal(e)
	}
}
