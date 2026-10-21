-- 校园集市 MySQL 8 schema（同时兼容 MariaDB 10.6+，CI 与生产以 MySQL 8 为准）。
-- 约定：
--   * 表默认 utf8mb4_bin：ID、幂等键、令牌摘要必须大小写敏感比较；
--     需要模糊搜索的标题/描述单独使用 utf8mb4_unicode_ci。
--   * 时间统一存毫秒时间戳 BIGINT，接口直接返回，避免时区换算。
--   * 每条语句以分号 + 换行结束，迁移器按此切分（DDL 在 MySQL 中隐式提交，无法放进事务）。

CREATE TABLE IF NOT EXISTS users (
  id         VARCHAR(64)  NOT NULL,
  email      VARCHAR(254) NOT NULL,
  name       VARCHAR(64)  NOT NULL,
  password   VARCHAR(100) NOT NULL,
  created_at BIGINT       NOT NULL,
  PRIMARY KEY (id),
  UNIQUE KEY uk_users_email (email)
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4 COLLATE=utf8mb4_bin;

CREATE TABLE IF NOT EXISTS sessions (
  hash       CHAR(64)    NOT NULL,
  user_id    VARCHAR(64) NOT NULL,
  expires_at BIGINT      NOT NULL,
  PRIMARY KEY (hash),
  KEY idx_sessions_user (user_id),
  KEY idx_sessions_expiry (expires_at),
  CONSTRAINT fk_sessions_user FOREIGN KEY (user_id) REFERENCES users (id)
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4 COLLATE=utf8mb4_bin;

CREATE TABLE IF NOT EXISTS auth_identities (
  user_id     VARCHAR(64)  NOT NULL,
  channel     VARCHAR(8)   NOT NULL,
  target      VARCHAR(254) NOT NULL,
  verified_at BIGINT       NOT NULL DEFAULT 0,
  PRIMARY KEY (channel, target),
  UNIQUE KEY uk_identity_user_channel (user_id, channel),
  CONSTRAINT fk_identity_user FOREIGN KEY (user_id) REFERENCES users (id),
  CONSTRAINT chk_identity_channel CHECK (channel IN ('email', 'sms'))
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4 COLLATE=utf8mb4_bin;

CREATE TABLE IF NOT EXISTS auth_codes (
  id          VARCHAR(64)  NOT NULL,
  channel     VARCHAR(8)   NOT NULL,
  target      VARCHAR(254) NOT NULL,
  purpose     VARCHAR(24)  NOT NULL,
  subject_id  VARCHAR(64)  NOT NULL,
  code_hash   CHAR(64)     NOT NULL,
  created_at  BIGINT       NOT NULL,
  expires_at  BIGINT       NOT NULL,
  attempts    INT          NOT NULL DEFAULT 0,
  consumed_at BIGINT       NOT NULL DEFAULT 0,
  state       VARCHAR(16)  NOT NULL,
  PRIMARY KEY (id),
  KEY idx_codes_target_time (channel, target, purpose, created_at),
  KEY idx_codes_subject_state (subject_id, state),
  KEY idx_codes_expiry (expires_at)
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4 COLLATE=utf8mb4_bin;

-- listed 是生成列：status IN ('available','reserved') 时为 1。
-- 首页"最新/分类/校区"列表都是 listed=1 + ORDER BY created_at DESC,
-- 用 (listed, created_at, id) 这类等值前缀索引可以让排序直接走索引、避免 filesort。
CREATE TABLE IF NOT EXISTS products (
  id             VARCHAR(64)   NOT NULL,
  seller_id      VARCHAR(64)   NOT NULL,
  title          VARCHAR(120)  CHARACTER SET utf8mb4 COLLATE utf8mb4_unicode_ci NOT NULL,
  description    TEXT          CHARACTER SET utf8mb4 COLLATE utf8mb4_unicode_ci NOT NULL,
  price_ct       BIGINT        NOT NULL,
  category       VARCHAR(32)   NOT NULL,
  campus         VARCHAR(32)   NOT NULL,
  item_condition VARCHAR(16)   NOT NULL,
  image          VARCHAR(2048) NOT NULL,
  status         VARCHAR(16)   NOT NULL DEFAULT 'available',
  demo           TINYINT(1)    NOT NULL DEFAULT 0,
  created_at     BIGINT        NOT NULL,
  updated_at     BIGINT        NOT NULL,
  listed         TINYINT(1) AS (IF(status IN ('available', 'reserved'), 1, 0)) STORED,
  PRIMARY KEY (id),
  KEY idx_products_listed_created (listed, created_at, id),
  KEY idx_products_listed_price (listed, price_ct, id),
  KEY idx_products_category (category, listed, created_at),
  KEY idx_products_campus (campus, listed, created_at),
  KEY idx_products_seller (seller_id, created_at),
  CONSTRAINT fk_products_seller FOREIGN KEY (seller_id) REFERENCES users (id),
  CONSTRAINT chk_products_price CHECK (price_ct > 0)
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4 COLLATE=utf8mb4_bin;

CREATE TABLE IF NOT EXISTS favorites (
  user_id    VARCHAR(64) NOT NULL,
  product_id VARCHAR(64) NOT NULL,
  created_at BIGINT      NOT NULL DEFAULT 0,
  PRIMARY KEY (user_id, product_id),
  KEY idx_favorites_product (product_id),
  CONSTRAINT fk_favorites_user FOREIGN KEY (user_id) REFERENCES users (id),
  CONSTRAINT fk_favorites_product FOREIGN KEY (product_id) REFERENCES products (id)
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4 COLLATE=utf8mb4_bin;

-- 限时抢购活动。stock 是数据库侧"最终可售库存"，Redis 中的库存只是预扣闸门；
-- 异步落单时 UPDATE ... SET stock=stock-1 WHERE stock>0 作为防超卖的最后一道防线。
CREATE TABLE IF NOT EXISTS flash_sales (
  id                VARCHAR(64)   NOT NULL,
  seller_id         VARCHAR(64)   NOT NULL,
  title             VARCHAR(120)  CHARACTER SET utf8mb4 COLLATE utf8mb4_unicode_ci NOT NULL,
  description       TEXT          CHARACTER SET utf8mb4 COLLATE utf8mb4_unicode_ci NOT NULL,
  image             VARCHAR(2048) NOT NULL,
  campus            VARCHAR(32)   NOT NULL,
  original_price_ct BIGINT        NOT NULL,
  price_ct          BIGINT        NOT NULL,
  total_stock       INT           NOT NULL,
  stock             INT           NOT NULL,
  start_at          BIGINT        NOT NULL,
  end_at            BIGINT        NOT NULL,
  status            VARCHAR(16)   NOT NULL DEFAULT 'scheduled',
  created_at        BIGINT        NOT NULL,
  updated_at        BIGINT        NOT NULL,
  PRIMARY KEY (id),
  KEY idx_flash_end (end_at),
  CONSTRAINT fk_flash_seller FOREIGN KEY (seller_id) REFERENCES users (id),
  CONSTRAINT chk_flash_stock CHECK (stock >= 0 AND stock <= total_stock),
  CONSTRAINT chk_flash_price CHECK (price_ct > 0 AND original_price_ct >= price_ct),
  CONSTRAINT chk_flash_window CHECK (end_at > start_at)
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4 COLLATE=utf8mb4_bin;

-- 订单号使用雪花 ID（趋势递增，B+ 树顺序追加写，避免随机主键的页分裂）。
-- uk_orders_flash_buyer：普通订单 flash_sale_id 为 NULL，唯一索引允许多个 NULL；
-- 抢购订单则在数据库层保证"一人一单"。
CREATE TABLE IF NOT EXISTS orders (
  id            VARCHAR(64)   NOT NULL,
  buyer_id      VARCHAR(64)   NOT NULL,
  seller_id     VARCHAR(64)   NOT NULL,
  product_id    VARCHAR(64)   NULL,
  flash_sale_id VARCHAR(64)   NULL,
  title         VARCHAR(120)  NOT NULL,
  image         VARCHAR(2048) NOT NULL,
  amount_ct     BIGINT        NOT NULL,
  campus        VARCHAR(32)   NOT NULL,
  status        VARCHAR(24)   NOT NULL,
  idem          VARCHAR(128)  NOT NULL,
  created_at    BIGINT        NOT NULL,
  updated_at    BIGINT        NOT NULL,
  expires_at    BIGINT        NOT NULL,
  PRIMARY KEY (id),
  UNIQUE KEY uk_orders_idem (buyer_id, idem),
  UNIQUE KEY uk_orders_flash_buyer (flash_sale_id, buyer_id),
  KEY idx_orders_buyer (buyer_id, created_at),
  KEY idx_orders_seller (seller_id, created_at),
  KEY idx_orders_status_expiry (status, expires_at),
  KEY idx_orders_product (product_id),
  CONSTRAINT fk_orders_buyer FOREIGN KEY (buyer_id) REFERENCES users (id),
  CONSTRAINT fk_orders_seller FOREIGN KEY (seller_id) REFERENCES users (id),
  CONSTRAINT fk_orders_product FOREIGN KEY (product_id) REFERENCES products (id),
  CONSTRAINT fk_orders_flash FOREIGN KEY (flash_sale_id) REFERENCES flash_sales (id),
  CONSTRAINT chk_orders_amount CHECK (amount_ct > 0),
  CONSTRAINT chk_orders_source CHECK ((product_id IS NULL) <> (flash_sale_id IS NULL))
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4 COLLATE=utf8mb4_bin;

CREATE TABLE IF NOT EXISTS order_events (
  id         BIGINT      NOT NULL AUTO_INCREMENT,
  order_id   VARCHAR(64) NOT NULL,
  status     VARCHAR(24) NOT NULL,
  actor      VARCHAR(64) NOT NULL,
  created_at BIGINT      NOT NULL,
  PRIMARY KEY (id),
  KEY idx_order_events_order (order_id, id),
  CONSTRAINT fk_order_events_order FOREIGN KEY (order_id) REFERENCES orders (id)
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4 COLLATE=utf8mb4_bin;

CREATE TABLE IF NOT EXISTS payments (
  order_id     VARCHAR(64)  NOT NULL,
  trade_no     VARCHAR(128) NULL,
  checkout_url TEXT         NOT NULL,
  created_at   BIGINT       NOT NULL,
  PRIMARY KEY (order_id),
  UNIQUE KEY uk_payments_trade (trade_no),
  CONSTRAINT fk_payments_order FOREIGN KEY (order_id) REFERENCES orders (id)
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4 COLLATE=utf8mb4_bin;

CREATE TABLE IF NOT EXISTS payment_checks (
  order_id   VARCHAR(64) NOT NULL,
  checked_at BIGINT      NOT NULL,
  PRIMARY KEY (order_id),
  CONSTRAINT fk_payment_checks_order FOREIGN KEY (order_id) REFERENCES orders (id)
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4 COLLATE=utf8mb4_bin;

CREATE TABLE IF NOT EXISTS payment_callbacks (
  notify_id  VARCHAR(191) NOT NULL,
  order_id   VARCHAR(64)  NOT NULL,
  trade_no   VARCHAR(191) NOT NULL,
  status     VARCHAR(32)  NOT NULL,
  amount_ct  BIGINT       NOT NULL,
  created_at BIGINT       NOT NULL,
  PRIMARY KEY (notify_id),
  KEY idx_payment_callbacks_order (order_id),
  CONSTRAINT fk_payment_callbacks_order FOREIGN KEY (order_id) REFERENCES orders (id)
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4 COLLATE=utf8mb4_bin;

CREATE TABLE IF NOT EXISTS refund_requests (
  order_id   VARCHAR(64) NOT NULL,
  reason     TEXT        NOT NULL,
  created_at BIGINT      NOT NULL,
  PRIMARY KEY (order_id),
  CONSTRAINT fk_refund_requests_order FOREIGN KEY (order_id) REFERENCES orders (id)
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4 COLLATE=utf8mb4_bin;

-- seq 是会话内单调递增的消息序号：发送时先 UPDATE seq=seq+1 拿到行锁再写消息，
-- 并发发送在行锁上串行化，(conversation_id, seq) 唯一键兜底。
CREATE TABLE IF NOT EXISTS conversations (
  id           VARCHAR(64)  NOT NULL,
  product_id   VARCHAR(64)  NOT NULL,
  buyer_id     VARCHAR(64)  NOT NULL,
  seller_id    VARCHAR(64)  NOT NULL,
  seq          BIGINT       NOT NULL DEFAULT 0,
  last_message VARCHAR(255) NOT NULL DEFAULT '',
  updated_at   BIGINT       NOT NULL,
  PRIMARY KEY (id),
  UNIQUE KEY uk_conversations_product_buyer (product_id, buyer_id),
  KEY idx_conversations_buyer (buyer_id, updated_at),
  KEY idx_conversations_seller (seller_id, updated_at),
  CONSTRAINT fk_conversations_product FOREIGN KEY (product_id) REFERENCES products (id),
  CONSTRAINT fk_conversations_buyer FOREIGN KEY (buyer_id) REFERENCES users (id),
  CONSTRAINT fk_conversations_seller FOREIGN KEY (seller_id) REFERENCES users (id)
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4 COLLATE=utf8mb4_bin;

CREATE TABLE IF NOT EXISTS messages (
  id              VARCHAR(64)  NOT NULL,
  conversation_id VARCHAR(64)  NOT NULL,
  sender_id       VARCHAR(64)  NOT NULL,
  client_msg_id   VARCHAR(128) NOT NULL,
  seq             BIGINT       NOT NULL,
  content         TEXT         NOT NULL,
  created_at      BIGINT       NOT NULL,
  PRIMARY KEY (id),
  UNIQUE KEY uk_messages_seq (conversation_id, seq),
  UNIQUE KEY uk_messages_client (conversation_id, sender_id, client_msg_id),
  CONSTRAINT fk_messages_conversation FOREIGN KEY (conversation_id) REFERENCES conversations (id),
  CONSTRAINT fk_messages_sender FOREIGN KEY (sender_id) REFERENCES users (id)
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4 COLLATE=utf8mb4_bin;

CREATE TABLE IF NOT EXISTS read_cursors (
  conversation_id VARCHAR(64) NOT NULL,
  user_id         VARCHAR(64) NOT NULL,
  seq             BIGINT      NOT NULL DEFAULT 0,
  PRIMARY KEY (conversation_id, user_id),
  CONSTRAINT fk_read_cursors_conversation FOREIGN KEY (conversation_id) REFERENCES conversations (id),
  CONSTRAINT fk_read_cursors_user FOREIGN KEY (user_id) REFERENCES users (id)
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4 COLLATE=utf8mb4_bin;

CREATE TABLE IF NOT EXISTS agent_threads (
  id         VARCHAR(64)  NOT NULL,
  user_id    VARCHAR(64)  NOT NULL,
  title      VARCHAR(128) NOT NULL,
  created_at BIGINT       NOT NULL,
  PRIMARY KEY (id),
  KEY idx_agent_threads_user (user_id, created_at),
  CONSTRAINT fk_agent_threads_user FOREIGN KEY (user_id) REFERENCES users (id)
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4 COLLATE=utf8mb4_bin;

-- MySQL 没有部分索引（SQLite 的 WHERE status='running'），
-- 用生成列 running_user：只有 running 状态才有值，其余为 NULL，唯一索引允许多个 NULL，
-- 从而在数据库层保证"每个用户同时只有一个运行中的 Agent 任务"。
CREATE TABLE IF NOT EXISTS agent_runs (
  id           VARCHAR(64)  NOT NULL,
  thread_id    VARCHAR(64)  NOT NULL,
  user_id      VARCHAR(64)  NOT NULL,
  idem         VARCHAR(128) NOT NULL,
  prompt       TEXT         NOT NULL,
  answer       MEDIUMTEXT   NOT NULL DEFAULT (''),
  status       VARCHAR(16)  NOT NULL,
  error        VARCHAR(512) NOT NULL DEFAULT '',
  created_at   BIGINT       NOT NULL,
  running_user VARCHAR(64) AS (IF(status = 'running', user_id, NULL)) STORED,
  PRIMARY KEY (id),
  UNIQUE KEY uk_agent_runs_idem (user_id, idem),
  UNIQUE KEY uk_agent_runs_one_running (running_user),
  KEY idx_agent_runs_thread (user_id, thread_id, created_at),
  CONSTRAINT fk_agent_runs_thread FOREIGN KEY (thread_id) REFERENCES agent_threads (id),
  CONSTRAINT fk_agent_runs_user FOREIGN KEY (user_id) REFERENCES users (id)
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4 COLLATE=utf8mb4_bin;

CREATE TABLE IF NOT EXISTS agent_capabilities (
  id          VARCHAR(64)  NOT NULL,
  kind        VARCHAR(16)  NOT NULL,
  name        VARCHAR(128) NOT NULL,
  description TEXT         NOT NULL,
  config      MEDIUMTEXT   NOT NULL,
  enabled     TINYINT(1)   NOT NULL,
  revision    INT          NOT NULL,
  updated_at  BIGINT       NOT NULL,
  PRIMARY KEY (id)
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4 COLLATE=utf8mb4_bin;

CREATE TABLE IF NOT EXISTS agent_definitions (
  id                VARCHAR(64) NOT NULL,
  draft             MEDIUMTEXT  NOT NULL,
  enabled           TINYINT(1)  NOT NULL,
  archived          TINYINT(1)  NOT NULL DEFAULT 0,
  revision          INT         NOT NULL,
  published_version INT         NOT NULL DEFAULT 0,
  updated_at        BIGINT      NOT NULL,
  PRIMARY KEY (id)
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4 COLLATE=utf8mb4_bin;

CREATE TABLE IF NOT EXISTS agent_versions (
  agent_id     VARCHAR(64) NOT NULL,
  version      INT         NOT NULL,
  snapshot     MEDIUMTEXT  NOT NULL,
  published_by VARCHAR(64) NOT NULL,
  published_at BIGINT      NOT NULL,
  PRIMARY KEY (agent_id, version),
  CONSTRAINT fk_agent_versions_agent FOREIGN KEY (agent_id) REFERENCES agent_definitions (id)
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4 COLLATE=utf8mb4_bin;

CREATE TABLE IF NOT EXISTS agent_thread_bindings (
  thread_id VARCHAR(64) NOT NULL,
  agent_id  VARCHAR(64) NOT NULL,
  PRIMARY KEY (thread_id),
  CONSTRAINT fk_agent_bindings_thread FOREIGN KEY (thread_id) REFERENCES agent_threads (id),
  CONSTRAINT fk_agent_bindings_agent FOREIGN KEY (agent_id) REFERENCES agent_definitions (id)
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4 COLLATE=utf8mb4_bin;

CREATE TABLE IF NOT EXISTS agent_run_snapshots (
  run_id   VARCHAR(64) NOT NULL,
  agent_id VARCHAR(64) NOT NULL,
  version  INT         NOT NULL,
  snapshot MEDIUMTEXT  NOT NULL,
  PRIMARY KEY (run_id),
  CONSTRAINT fk_agent_snapshots_run FOREIGN KEY (run_id) REFERENCES agent_runs (id)
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4 COLLATE=utf8mb4_bin;

CREATE TABLE IF NOT EXISTS agent_audit (
  id         BIGINT      NOT NULL AUTO_INCREMENT,
  actor_id   VARCHAR(64) NOT NULL,
  action     VARCHAR(48) NOT NULL,
  target_id  VARCHAR(64) NOT NULL,
  revision   INT         NOT NULL,
  created_at BIGINT      NOT NULL,
  PRIMARY KEY (id)
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4 COLLATE=utf8mb4_bin;

CREATE TABLE IF NOT EXISTS agent_tool_events (
  id            BIGINT       NOT NULL AUTO_INCREMENT,
  run_id        VARCHAR(64)  NOT NULL,
  capability_id VARCHAR(64)  NOT NULL,
  name          VARCHAR(128) NOT NULL,
  status        VARCHAR(16)  NOT NULL,
  created_at    BIGINT       NOT NULL,
  PRIMARY KEY (id),
  KEY idx_agent_tool_events_run (run_id),
  CONSTRAINT fk_agent_tool_events_run FOREIGN KEY (run_id) REFERENCES agent_runs (id)
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4 COLLATE=utf8mb4_bin;
