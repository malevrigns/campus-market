// Package mysqlrepo 是 MySQL 数据适配器：执行 SQL、映射持久化模型与领域实体，
// 并把存储错误翻译为领域错误。业务规则不在本包内。
package mysqlrepo

import (
	"context"
	"crypto/hmac"
	"database/sql"
	"errors"

	"qingfly/edu-mall/internal/domain"
	"qingfly/edu-mall/internal/infra/mysqlx"
	"qingfly/edu-mall/internal/port"
)

type AuthRepo struct{ *base }

// Tx 实现 port 下各个事务能力接口，由业务层通过 InTx 获得。
type Tx struct {
	tx *sql.Tx
}

// translate 把存储层错误转换为领域错误；业务错误原样返回。
func translate(err error) error {
	if err == nil {
		return nil
	}
	var de *domain.DuplicateError
	if errors.As(err, &de) {
		return err
	}
	if mysqlx.IsDuplicate(err) {
		return &domain.DuplicateError{Constraint: mysqlx.DuplicateKeyName(err), Err: err}
	}
	if mysqlx.IsRetryable(err) {
		return domain.Unavailable("系统繁忙，请稍后重试")
	}
	return err
}

func notFound(err error, message string) error {
	if errors.Is(err, sql.ErrNoRows) {
		return domain.NotFound(message)
	}
	return err
}

// ---------- 认证 ----------

func (s *AuthRepo) FindIdentity(ctx context.Context, channel, target string) (string, bool, error) {
	var uid string
	err := s.db.QueryRowContext(ctx, "SELECT user_id FROM auth_identities WHERE channel=? AND target=?", channel, target).Scan(&uid)
	if errors.Is(err, sql.ErrNoRows) {
		return "", false, nil
	}
	return uid, err == nil, err
}

func (s *AuthRepo) FindCredential(ctx context.Context, channel, target string) (string, string, bool, error) {
	var uid, hash string
	err := s.db.QueryRowContext(ctx, "SELECT u.id,u.password FROM users u JOIN auth_identities i ON i.user_id=u.id WHERE i.channel=? AND i.target=?", channel, target).Scan(&uid, &hash)
	if errors.Is(err, sql.ErrNoRows) {
		return "", "", false, nil
	}
	return uid, hash, err == nil, err
}

const userSelect = "SELECT u.id,u.name,COALESCE(e.target,''),COALESCE(p.target,''),COALESCE(e.verified_at,0)>0,COALESCE(p.verified_at,0)>0 FROM users u LEFT JOIN auth_identities e ON e.user_id=u.id AND e.channel='email' LEFT JOIN auth_identities p ON p.user_id=u.id AND p.channel='sms'"

func scanUser(row interface{ Scan(...any) error }) (domain.User, error) {
	var u domain.User
	err := row.Scan(&u.ID, &u.Name, &u.Email, &u.Phone, &u.EmailVerified, &u.PhoneVerified)
	return u, err
}

func (s *AuthRepo) GetUser(ctx context.Context, id string) (domain.User, error) {
	u, err := scanUser(s.db.QueryRowContext(ctx, userSelect+" WHERE u.id=?", id))
	return u, notFound(err, "内容不存在或无权访问")
}

func (s *AuthRepo) GetUserIdentity(ctx context.Context, userID, channel string) (string, int64, bool, error) {
	var target string
	var verified int64
	err := s.db.QueryRowContext(ctx, "SELECT target,verified_at FROM auth_identities WHERE user_id=? AND channel=?", userID, channel).Scan(&target, &verified)
	if errors.Is(err, sql.ErrNoRows) {
		return "", 0, false, nil
	}
	return target, verified, err == nil, err
}

func (s *AuthRepo) GetUserBySession(ctx context.Context, hash string) (domain.User, int64, error) {
	u := domain.User{}
	var expires int64
	err := s.db.QueryRowContext(ctx, "SELECT u.id,u.name,COALESCE(e.target,''),COALESCE(p.target,''),COALESCE(e.verified_at,0)>0,COALESCE(p.verified_at,0)>0,s.expires_at FROM sessions s JOIN users u ON u.id=s.user_id LEFT JOIN auth_identities e ON e.user_id=u.id AND e.channel='email' LEFT JOIN auth_identities p ON p.user_id=u.id AND p.channel='sms' WHERE s.hash=? AND s.expires_at>?", hash, domain.NowMillis()).Scan(&u.ID, &u.Name, &u.Email, &u.Phone, &u.EmailVerified, &u.PhoneVerified, &expires)
	return u, expires, notFound(err, "登录已过期，请重新登录")
}

func (s *AuthRepo) LastCodeAt(ctx context.Context, channel, target, purpose string) (int64, bool, error) {
	var recent int64
	err := s.db.QueryRowContext(ctx, "SELECT created_at FROM auth_codes WHERE channel=? AND target=? AND purpose=? ORDER BY created_at DESC LIMIT 1", channel, target, purpose).Scan(&recent)
	if errors.Is(err, sql.ErrNoRows) {
		return 0, false, nil
	}
	return recent, err == nil, err
}

func (s *AuthRepo) CreateCode(ctx context.Context, code domain.AuthCode) error {
	return translate(mysqlx.WithTx(ctx, s.db, func(tx *sql.Tx) error {
		if _, err := tx.ExecContext(ctx, "UPDATE auth_codes SET state='superseded' WHERE channel=? AND target=? AND purpose=? AND state='sent'", code.Channel, code.Target, code.Purpose); err != nil {
			return err
		}
		_, err := tx.ExecContext(ctx, "INSERT INTO auth_codes(id,channel,target,purpose,subject_id,code_hash,created_at,expires_at,state) VALUES(?,?,?,?,?,?,?,?,?)", code.ID, code.Channel, code.Target, code.Purpose, code.SubjectID, code.CodeHash, code.CreatedAt, code.ExpiresAt, code.State)
		return err
	}))
}

// ConsumeCode 原子消费验证码：锁定行、校验用途/主体/过期/次数与摘要；
// 错误尝试会先提交 attempts 再返回业务错误（与 WithTx 的回滚配合，保证计数不丢）。
func (s *AuthRepo) ConsumeCode(ctx context.Context, proof domain.CodeProof, purpose, subject, expectedHash string, at int64) error {
	return translate(mysqlx.WithTx(ctx, s.db, func(tx *sql.Tx) error {
		var hash, storedSubject, state string
		var expires int64
		var attempts int
		err := tx.QueryRowContext(ctx, "SELECT code_hash,subject_id,expires_at,attempts,state FROM auth_codes WHERE id=? AND channel=? AND target=? AND purpose=? FOR UPDATE", proof.ChallengeID, proof.Channel, proof.Target, purpose).Scan(&hash, &storedSubject, &expires, &attempts, &state)
		if errors.Is(err, sql.ErrNoRows) {
			return domain.Invalid("验证码无效或用途不匹配，请重新获取")
		}
		if err != nil {
			return err
		}
		if expires <= at || state != "sent" || storedSubject != subject {
			return domain.Invalid("验证码已过期或失效，请重新获取")
		}
		if attempts >= 5 {
			return domain.Retryable(domain.KindTooMany, "验证码尝试次数过多，请重新获取", 0)
		}
		if !hmac.Equal([]byte(hash), []byte(expectedHash)) {
			if _, err = tx.ExecContext(ctx, "UPDATE auth_codes SET attempts=attempts+1 WHERE id=?", proof.ChallengeID); err != nil {
				return err
			}
			if err = tx.Commit(); err != nil {
				return err
			}
			return domain.Invalid("验证码不正确，请检查后重试")
		}
		_, err = tx.ExecContext(ctx, "UPDATE auth_codes SET state='used',consumed_at=? WHERE id=?", at, proof.ChallengeID)
		return err
	}))
}

func (s *AuthRepo) SetCodeState(ctx context.Context, id, state string) (bool, error) {
	res, err := s.db.ExecContext(ctx, "UPDATE auth_codes SET state=? WHERE id=? AND state='sending'", state, id)
	if err != nil {
		return false, translate(err)
	}
	n, _ := res.RowsAffected()
	return n == 1, nil
}

func (s *AuthRepo) DeleteSession(ctx context.Context, hash string) error {
	_, err := s.db.ExecContext(ctx, "DELETE FROM sessions WHERE hash=?", hash)
	return translate(err)
}

func (s *AuthRepo) Cleanup(ctx context.Context, now, codeBefore int64) error {
	for _, q := range []struct {
		stmt   string
		before int64
	}{{"DELETE FROM sessions WHERE expires_at<? LIMIT 1000", now}, {"DELETE FROM auth_codes WHERE expires_at<? LIMIT 1000", codeBefore}} {
		for i := 0; i < 20; i++ {
			res, err := s.db.ExecContext(ctx, q.stmt, q.before)
			if err != nil {
				return translate(err)
			}
			if n, _ := res.RowsAffected(); n < 1000 {
				break
			}
		}
	}
	return nil
}

func (s *AuthRepo) BackfillEmailIdentities(ctx context.Context) error {
	_, err := s.db.ExecContext(ctx, "INSERT IGNORE INTO auth_identities (user_id,channel,target,verified_at) SELECT u.id,'email',lower(u.email),0 FROM users u LEFT JOIN auth_identities i ON i.user_id=u.id AND i.channel='email' WHERE i.user_id IS NULL AND u.email NOT LIKE '%@accounts.invalid'")
	return translate(err)
}

// ---------- AuthTx ----------

func (t *Tx) FindIdentity(ctx context.Context, channel, target string) (string, bool, error) {
	var uid string
	err := t.tx.QueryRowContext(ctx, "SELECT user_id FROM auth_identities WHERE channel=? AND target=?", channel, target).Scan(&uid)
	if errors.Is(err, sql.ErrNoRows) {
		return "", false, nil
	}
	return uid, err == nil, err
}

func (t *Tx) FindCodeForUpdate(ctx context.Context, id, channel, target, purpose string) (domain.AuthCode, error) {
	var c domain.AuthCode
	err := t.tx.QueryRowContext(ctx, "SELECT code_hash,subject_id,expires_at,attempts,state,created_at,consumed_at FROM auth_codes WHERE id=? AND channel=? AND target=? AND purpose=? FOR UPDATE", id, channel, target, purpose).Scan(&c.CodeHash, &c.SubjectID, &c.ExpiresAt, &c.Attempts, &c.State, &c.CreatedAt, &c.ConsumedAt)
	c.ID, c.Channel, c.Target, c.Purpose = id, channel, target, purpose
	return c, notFound(err, "验证码无效或用途不匹配，请重新获取")
}

func (t *Tx) MarkCodeUsed(ctx context.Context, id string, at int64) error {
	_, err := t.tx.ExecContext(ctx, "UPDATE auth_codes SET state='used',consumed_at=? WHERE id=?", at, id)
	return err
}

func (t *Tx) IncrementCodeAttempts(ctx context.Context, id string) error {
	_, err := t.tx.ExecContext(ctx, "UPDATE auth_codes SET attempts=attempts+1 WHERE id=?", id)
	return err
}

func (t *Tx) InsertUser(ctx context.Context, id, email, name, passwordHash string, at int64) error {
	_, err := t.tx.ExecContext(ctx, "INSERT INTO users (id,email,name,password,created_at) VALUES(?,?,?,?,?)", id, email, name, passwordHash, at)
	return duplicate(err)
}

func (t *Tx) InsertIdentity(ctx context.Context, userID, channel, target string, verifiedAt int64) error {
	_, err := t.tx.ExecContext(ctx, "INSERT INTO auth_identities (user_id,channel,target,verified_at) VALUES(?,?,?,?)", userID, channel, target, verifiedAt)
	return duplicate(err)
}

func (t *Tx) GetUser(ctx context.Context, id string) (domain.User, error) {
	u, err := scanUser(t.tx.QueryRowContext(ctx, userSelect+" WHERE u.id=?", id))
	return u, notFound(err, "内容不存在或无权访问")
}

func (t *Tx) GetUserPasswordForUpdate(ctx context.Context, id string) (string, error) {
	var hash string
	err := t.tx.QueryRowContext(ctx, "SELECT password FROM users WHERE id=? FOR UPDATE", id).Scan(&hash)
	return hash, notFound(err, "内容不存在或无权访问")
}

func (t *Tx) GetUserIdentity(ctx context.Context, userID, channel string) (string, int64, bool, error) {
	var target string
	var verified int64
	err := t.tx.QueryRowContext(ctx, "SELECT target,verified_at FROM auth_identities WHERE user_id=? AND channel=? FOR UPDATE", userID, channel).Scan(&target, &verified)
	if errors.Is(err, sql.ErrNoRows) {
		return "", 0, false, nil
	}
	return target, verified, err == nil, err
}

func (t *Tx) UpsertIdentity(ctx context.Context, userID, channel, target string, verifiedAt int64) error {
	_, err := t.tx.ExecContext(ctx, "INSERT INTO auth_identities (user_id,channel,target,verified_at) VALUES(?,?,?,?) ON DUPLICATE KEY UPDATE target=VALUES(target),verified_at=VALUES(verified_at)", userID, channel, target, verifiedAt)
	return duplicate(err)
}

func (t *Tx) UpdateUserEmail(ctx context.Context, userID, email string) error {
	_, err := t.tx.ExecContext(ctx, "UPDATE users SET email=? WHERE id=?", email, userID)
	return err
}

func (t *Tx) UpdatePassword(ctx context.Context, userID, passwordHash string) error {
	_, err := t.tx.ExecContext(ctx, "UPDATE users SET password=? WHERE id=?", passwordHash, userID)
	return err
}

func (t *Tx) UpdateIdentityVerified(ctx context.Context, channel, target string, at int64) error {
	_, err := t.tx.ExecContext(ctx, "UPDATE auth_identities SET verified_at=? WHERE channel=? AND target=?", at, channel, target)
	return err
}

func (t *Tx) InsertSession(ctx context.Context, hash, userID string, expiresAt int64) error {
	_, err := t.tx.ExecContext(ctx, "INSERT INTO sessions (hash,user_id,expires_at) VALUES(?,?,?)", hash, userID, expiresAt)
	return err
}

func (t *Tx) DeleteSession(ctx context.Context, hash, userID string) error {
	_, err := t.tx.ExecContext(ctx, "DELETE FROM sessions WHERE hash=? AND user_id=?", hash, userID)
	return err
}

func (t *Tx) ListUserSessions(ctx context.Context, userID string) ([]string, error) {
	rows, err := t.tx.QueryContext(ctx, "SELECT hash FROM sessions WHERE user_id=?", userID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []string{}
	for rows.Next() {
		var h string
		if err := rows.Scan(&h); err != nil {
			return nil, err
		}
		out = append(out, h)
	}
	return out, rows.Err()
}

func (t *Tx) CountSession(ctx context.Context, hash, userID string) (int, error) {
	var n int
	err := t.tx.QueryRowContext(ctx, "SELECT COUNT(*) FROM sessions WHERE hash=? AND user_id=?", hash, userID).Scan(&n)
	return n, err
}

func (t *Tx) DeleteUserSessions(ctx context.Context, userID string) error {
	_, err := t.tx.ExecContext(ctx, "DELETE FROM sessions WHERE user_id=?", userID)
	return err
}

func (t *Tx) InvalidateUserCodes(ctx context.Context, userID string) error {
	_, err := t.tx.ExecContext(ctx, "UPDATE auth_codes SET state='invalidated' WHERE subject_id=? AND state IN('sent','sending')", userID)
	return err
}

// InTx 在认证用例事务中执行 fn；死锁/锁等待超时由 mysqlx.WithTx 重试。
func (s *AuthRepo) InTx(ctx context.Context, fn func(port.AuthTx) error) error {
	return translate(mysqlx.WithTx(ctx, s.db, func(tx *sql.Tx) error {
		return fn(&Tx{tx: tx})
	}))
}
