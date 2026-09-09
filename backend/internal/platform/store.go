package platform

import (
	"database/sql"
	"fmt"
	_ "modernc.org/sqlite"
	"net/url"
	"os"
	"path/filepath"
)

// OpenStore uses a single connection to serialize short write transactions. WAL
// permits a separate backup/reader connection. This deployment is single-node.
func OpenStore(path string) (*sql.DB, error) {
	if path == "" {
		return nil, fmt.Errorf("database path required")
	}
	if err := os.MkdirAll(filepath.Dir(path), 0700); err != nil {
		return nil, err
	}
	absolute, err := filepath.Abs(path)
	if err != nil {
		return nil, err
	}
	uriPath := filepath.ToSlash(absolute)
	if filepath.VolumeName(absolute) != "" {
		uriPath = "/" + uriPath
	}
	u := url.URL{Scheme: "file", Path: uriPath}
	db, err := sql.Open("sqlite", u.String()+"?_pragma=busy_timeout(10000)&_pragma=foreign_keys(1)&_pragma=journal_mode(WAL)&_pragma=synchronous(FULL)")
	if err != nil {
		return nil, err
	}
	db.SetMaxOpenConns(1)
	if _, err = db.Exec(schema); err != nil {
		db.Close()
		return nil, err
	}
	return db, nil
}

const schema = `
CREATE TABLE IF NOT EXISTS schema_version(version INTEGER PRIMARY KEY);
INSERT OR IGNORE INTO schema_version VALUES(1);
CREATE TABLE IF NOT EXISTS users(id TEXT PRIMARY KEY,email TEXT NOT NULL UNIQUE,name TEXT NOT NULL,password TEXT NOT NULL,created_at INTEGER NOT NULL);
CREATE TABLE IF NOT EXISTS sessions(hash TEXT PRIMARY KEY,user_id TEXT NOT NULL REFERENCES users(id),expires_at INTEGER NOT NULL);
CREATE INDEX IF NOT EXISTS sessions_expiry ON sessions(expires_at);
CREATE TABLE IF NOT EXISTS products(id TEXT PRIMARY KEY,seller_id TEXT NOT NULL REFERENCES users(id),title TEXT NOT NULL,description TEXT NOT NULL,price_ct INTEGER NOT NULL CHECK(price_ct>0),category TEXT NOT NULL,campus TEXT NOT NULL,condition TEXT NOT NULL,image TEXT NOT NULL,status TEXT NOT NULL DEFAULT 'available',demo INTEGER NOT NULL DEFAULT 0,created_at INTEGER NOT NULL);
CREATE INDEX IF NOT EXISTS products_created ON products(created_at DESC);
CREATE TABLE IF NOT EXISTS favorites(user_id TEXT NOT NULL REFERENCES users(id),product_id TEXT NOT NULL REFERENCES products(id),PRIMARY KEY(user_id,product_id));
CREATE TABLE IF NOT EXISTS orders(id TEXT PRIMARY KEY,buyer_id TEXT NOT NULL REFERENCES users(id),seller_id TEXT NOT NULL REFERENCES users(id),product_id TEXT NOT NULL REFERENCES products(id),title TEXT NOT NULL,image TEXT NOT NULL,amount_ct INTEGER NOT NULL CHECK(amount_ct>0),campus TEXT NOT NULL,status TEXT NOT NULL,idem TEXT NOT NULL,created_at INTEGER NOT NULL,updated_at INTEGER NOT NULL,expires_at INTEGER NOT NULL,UNIQUE(buyer_id,idem));
CREATE INDEX IF NOT EXISTS orders_buyer ON orders(buyer_id,created_at DESC);
CREATE INDEX IF NOT EXISTS orders_seller ON orders(seller_id,created_at DESC);
CREATE TABLE IF NOT EXISTS order_events(id INTEGER PRIMARY KEY AUTOINCREMENT,order_id TEXT NOT NULL REFERENCES orders(id),status TEXT NOT NULL,actor TEXT NOT NULL,created_at INTEGER NOT NULL);
CREATE TABLE IF NOT EXISTS payments(order_id TEXT PRIMARY KEY REFERENCES orders(id),trade_no TEXT UNIQUE,checkout_url TEXT NOT NULL,created_at INTEGER NOT NULL);
CREATE TABLE IF NOT EXISTS payment_checks(order_id TEXT PRIMARY KEY REFERENCES orders(id),checked_at INTEGER NOT NULL);
CREATE TABLE IF NOT EXISTS payment_callbacks(notify_id TEXT PRIMARY KEY,order_id TEXT NOT NULL REFERENCES orders(id),trade_no TEXT NOT NULL,status TEXT NOT NULL,amount_ct INTEGER NOT NULL,created_at INTEGER NOT NULL);
CREATE TABLE IF NOT EXISTS refund_requests(order_id TEXT PRIMARY KEY REFERENCES orders(id),reason TEXT NOT NULL,created_at INTEGER NOT NULL);
CREATE TABLE IF NOT EXISTS conversations(id TEXT PRIMARY KEY,product_id TEXT NOT NULL REFERENCES products(id),buyer_id TEXT NOT NULL REFERENCES users(id),seller_id TEXT NOT NULL REFERENCES users(id),seq INTEGER NOT NULL DEFAULT 0,updated_at INTEGER NOT NULL,UNIQUE(product_id,buyer_id));
CREATE TABLE IF NOT EXISTS messages(id TEXT PRIMARY KEY,conversation_id TEXT NOT NULL REFERENCES conversations(id),sender_id TEXT NOT NULL REFERENCES users(id),client_msg_id TEXT NOT NULL,seq INTEGER NOT NULL,content TEXT NOT NULL,created_at INTEGER NOT NULL,UNIQUE(conversation_id,seq),UNIQUE(conversation_id,sender_id,client_msg_id));
CREATE TABLE IF NOT EXISTS read_cursors(conversation_id TEXT NOT NULL REFERENCES conversations(id),user_id TEXT NOT NULL REFERENCES users(id),seq INTEGER NOT NULL DEFAULT 0,PRIMARY KEY(conversation_id,user_id));
CREATE TABLE IF NOT EXISTS agent_threads(id TEXT PRIMARY KEY,user_id TEXT NOT NULL REFERENCES users(id),title TEXT NOT NULL,created_at INTEGER NOT NULL);
CREATE TABLE IF NOT EXISTS agent_runs(id TEXT PRIMARY KEY,thread_id TEXT NOT NULL REFERENCES agent_threads(id),user_id TEXT NOT NULL REFERENCES users(id),idem TEXT NOT NULL,prompt TEXT NOT NULL,answer TEXT NOT NULL DEFAULT '',status TEXT NOT NULL,error TEXT NOT NULL DEFAULT '',created_at INTEGER NOT NULL,UNIQUE(user_id,idem));
CREATE UNIQUE INDEX IF NOT EXISTS agent_one_running ON agent_runs(user_id) WHERE status='running';
CREATE TABLE IF NOT EXISTS agent_capabilities(id TEXT PRIMARY KEY,kind TEXT NOT NULL,name TEXT NOT NULL,description TEXT NOT NULL,config TEXT NOT NULL,enabled INTEGER NOT NULL,revision INTEGER NOT NULL,updated_at INTEGER NOT NULL);
CREATE TABLE IF NOT EXISTS agent_definitions(id TEXT PRIMARY KEY,draft TEXT NOT NULL,enabled INTEGER NOT NULL,archived INTEGER NOT NULL DEFAULT 0,revision INTEGER NOT NULL,published_version INTEGER NOT NULL DEFAULT 0,updated_at INTEGER NOT NULL);
CREATE TABLE IF NOT EXISTS agent_versions(agent_id TEXT NOT NULL REFERENCES agent_definitions(id),version INTEGER NOT NULL,snapshot TEXT NOT NULL,published_by TEXT NOT NULL,published_at INTEGER NOT NULL,PRIMARY KEY(agent_id,version));
CREATE TABLE IF NOT EXISTS agent_thread_bindings(thread_id TEXT PRIMARY KEY REFERENCES agent_threads(id),agent_id TEXT NOT NULL REFERENCES agent_definitions(id));
CREATE TABLE IF NOT EXISTS agent_run_snapshots(run_id TEXT PRIMARY KEY REFERENCES agent_runs(id),agent_id TEXT NOT NULL,version INTEGER NOT NULL,snapshot TEXT NOT NULL);
CREATE TABLE IF NOT EXISTS agent_audit(id INTEGER PRIMARY KEY AUTOINCREMENT,actor_id TEXT NOT NULL,action TEXT NOT NULL,target_id TEXT NOT NULL,revision INTEGER NOT NULL,created_at INTEGER NOT NULL);
CREATE TABLE IF NOT EXISTS agent_tool_events(id INTEGER PRIMARY KEY AUTOINCREMENT,run_id TEXT NOT NULL REFERENCES agent_runs(id),capability_id TEXT NOT NULL,name TEXT NOT NULL,status TEXT NOT NULL,created_at INTEGER NOT NULL);
`
