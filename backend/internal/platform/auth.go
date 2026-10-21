package platform

import (
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"errors"
	"golang.org/x/crypto/bcrypt"
	"net/http"
	"strings"
	"time"
	"unicode/utf8"

	"qingfly/edu-mall/internal/infra/mysqlx"
)

type User struct {
	IsAdmin       bool   `json:"is_admin"`
	ID            string `json:"id"`
	Name          string `json:"name"`
	Email         string `json:"email"`
	Phone         string `json:"phone"`
	EmailVerified bool   `json:"email_verified"`
	PhoneVerified bool   `json:"phone_verified"`
}

const userSelect = "SELECT u.id,u.name,COALESCE(e.target,''),COALESCE(p.target,''),COALESCE(e.verified_at,0)>0,COALESCE(p.verified_at,0)>0 FROM users u LEFT JOIN auth_identities e ON e.user_id=u.id AND e.channel='email' LEFT JOIN auth_identities p ON p.user_id=u.id AND p.channel='sms'"

func digest(token string) string { h := sha256.Sum256([]byte(token)); return hex.EncodeToString(h[:]) }
func (s *Server) scanUser(row scanner) (*User, error) {
	u := &User{}
	e := row.Scan(&u.ID, &u.Name, &u.Email, &u.Phone, &u.EmailVerified, &u.PhoneVerified)
	u.IsAdmin = s.isAgentAdmin(u.ID)
	return u, e
}
func (s *Server) currentUser(r *http.Request) (*User, error) {
	c, e := s.sessionCookie(r)
	if e != nil {
		return nil, fail(401, "请先登录")
	}
	hash := digest(c.Value)
	if u, ok, revoked := s.cachedSession(r.Context(), hash); revoked {
		return nil, fail(401, "登录已过期，请重新登录")
	} else if ok {
		return u, nil
	}
	u := &User{}
	var expires int64
	e = s.db.QueryRowContext(r.Context(), "SELECT u.id,u.name,COALESCE(e.target,''),COALESCE(p.target,''),COALESCE(e.verified_at,0)>0,COALESCE(p.verified_at,0)>0,s.expires_at FROM sessions s JOIN users u ON u.id=s.user_id LEFT JOIN auth_identities e ON e.user_id=u.id AND e.channel='email' LEFT JOIN auth_identities p ON p.user_id=u.id AND p.channel='sms' WHERE s.hash=? AND s.expires_at>?", hash, now()).Scan(&u.ID, &u.Name, &u.Email, &u.Phone, &u.EmailVerified, &u.PhoneVerified, &expires)
	if errors.Is(e, sql.ErrNoRows) {
		return nil, fail(401, "登录已过期，请重新登录")
	}
	if e != nil {
		return nil, e
	}
	s.storeSession(r.Context(), hash, u, expires)
	u.IsAdmin = s.isAgentAdmin(u.ID)
	return u, nil
}
func (s *Server) newSession(tx *sql.Tx, r *http.Request, uid string) (string, time.Time, error) {
	token := id("")
	expires := time.Now().Add(7 * 24 * time.Hour)
	if old, e := s.sessionCookie(r); e == nil {
		if _, e = tx.Exec("DELETE FROM sessions WHERE hash=? AND user_id=?", digest(old.Value), uid); e != nil {
			return "", expires, e
		}
		s.revokeSessions(r.Context(), digest(old.Value))
	}
	_, e := tx.Exec("INSERT INTO sessions (hash,user_id,expires_at) VALUES(?,?,?)", digest(token), uid, expires.UnixMilli())
	return token, expires, e
}
func (s *Server) writeSession(w http.ResponseWriter, r *http.Request, token string, expires time.Time) {
	http.SetCookie(w, &http.Cookie{Name: s.sessionCookieName(r), Value: token, Path: "/", HttpOnly: true, Secure: s.cfg.Production, SameSite: http.SameSiteStrictMode, Expires: expires, MaxAge: 7 * 86400})
}
func (s *Server) setSession(w http.ResponseWriter, r *http.Request, u *User, verifiedHash string) error {
	tx, e := s.db.BeginTx(r.Context(), nil)
	if e != nil {
		return e
	}
	defer tx.Rollback()
	var currentHash string
	// FOR UPDATE 是"当前读"：RR 隔离级别下普通 SELECT 读的是事务快照，可能看不到刚提交的改密。
	if e = tx.QueryRow("SELECT password FROM users WHERE id=? FOR UPDATE", u.ID).Scan(&currentHash); e != nil {
		return e
	}
	if currentHash != verifiedHash {
		return fail(401, "密码已更新，请使用新密码重新登录")
	}
	token, expires, e := s.newSession(tx, r, u.ID)
	if e != nil {
		return e
	}
	if e = tx.Commit(); e != nil {
		return e
	}
	s.writeSession(w, r, token, expires)
	u.IsAdmin = s.isAgentAdmin(u.ID)
	return nil
}
func validPassword(p string) bool {
	return utf8.RuneCountInString(p) >= 8 && len(p) <= 72 && strings.TrimSpace(p) != ""
}
func normalizeProof(p *CodeProof, email, phone string) error {
	if p.Channel == "" {
		if email != "" {
			p.Channel = "email"
			p.Target = email
		} else if phone != "" {
			p.Channel = "sms"
			p.Target = phone
		}
	}
	if p.Target == "" {
		if p.Channel == "email" {
			p.Target = email
		} else {
			p.Target = phone
		}
	}
	target, e := normalizeIdentity(p.Channel, p.Target)
	if e != nil {
		return e
	}
	p.Target = target
	return nil
}
func (s *Server) register(w http.ResponseWriter, r *http.Request, _ *User) error {
	var v struct {
		CodeProof
		Name     string `json:"name"`
		Email    string `json:"email"`
		Phone    string `json:"phone"`
		Password string `json:"password"`
	}
	if e := decode(r, &v); e != nil {
		return e
	}
	v.Name = strings.TrimSpace(v.Name)
	if !validText(v.Name, 1, 24) {
		return fail(400, "昵称需要 1–24 个字")
	}
	if !validPassword(v.Password) {
		return fail(400, "密码至少 8 个字符，且不超过 72 字节")
	}
	if e := normalizeProof(&v.CodeProof, v.Email, v.Phone); e != nil {
		return e
	}
	hash, e := bcrypt.GenerateFromPassword([]byte(v.Password), bcrypt.DefaultCost)
	if e != nil {
		return e
	}
	tx, e := s.db.BeginTx(r.Context(), nil)
	if e != nil {
		return e
	}
	defer tx.Rollback()
	var exists int
	if e = tx.QueryRow("SELECT COUNT(*) FROM auth_identities WHERE channel=? AND target=?", v.Channel, v.Target).Scan(&exists); e != nil {
		return e
	}
	if exists > 0 {
		return fail(409, "该邮箱或手机号已注册，请直接登录或找回密码")
	}
	if e = s.consumeCode(tx, v.CodeProof, "register", ""); e != nil {
		return e
	}
	uid := id("u_")
	email := v.Target
	if v.Channel == "sms" {
		email = "phone+" + uid + "@accounts.invalid"
	}
	if _, e = tx.Exec("INSERT INTO users (id,email,name,password,created_at) VALUES(?,?,?,?,?)", uid, email, v.Name, string(hash), now()); e != nil {
		if mysqlx.IsDuplicate(e) {
			return fail(409, "该邮箱或手机号已注册，请直接登录或找回密码")
		}
		return e
	}
	if _, e = tx.Exec("INSERT INTO auth_identities (user_id,channel,target,verified_at) VALUES(?,?,?,?)", uid, v.Channel, v.Target, now()); e != nil {
		if mysqlx.IsDuplicate(e) {
			return fail(409, "该邮箱或手机号已注册，请直接登录或找回密码")
		}
		return e
	}
	token, expires, e := s.newSession(tx, r, uid)
	if e != nil {
		return e
	}
	u, e := s.scanUser(tx.QueryRow(userSelect+" WHERE u.id=?", uid))
	if e != nil {
		return e
	}
	if e = tx.Commit(); e != nil {
		return e
	}
	s.writeSession(w, r, token, expires)
	return result(w, 201, u)
}

var dummyHash, _ = bcrypt.GenerateFromPassword([]byte("unused-dummy-password"), bcrypt.DefaultCost)

func (s *Server) login(w http.ResponseWriter, r *http.Request, _ *User) error {
	var v struct {
		Account  string `json:"account"`
		Email    string `json:"email"`
		Password string `json:"password"`
	}
	if e := decode(r, &v); e != nil {
		return e
	}
	if v.Account == "" {
		v.Account = v.Email
	}
	channel := "sms"
	if strings.Contains(v.Account, "@") {
		channel = "email"
	}
	target, e := normalizeIdentity(channel, v.Account)
	if e != nil {
		return e
	}
	if len(v.Password) > 72 {
		return fail(401, "账号或密码不正确")
	}
	var uid, hash string
	e = s.db.QueryRowContext(r.Context(), "SELECT u.id,u.password FROM users u JOIN auth_identities i ON i.user_id=u.id WHERE i.channel=? AND i.target=?", channel, target).Scan(&uid, &hash)
	if e != nil {
		if !errors.Is(e, sql.ErrNoRows) {
			return e
		}
		_ = bcrypt.CompareHashAndPassword(dummyHash, []byte(v.Password))
		return fail(401, "账号或密码不正确")
	}
	if bcrypt.CompareHashAndPassword([]byte(hash), []byte(v.Password)) != nil {
		return fail(401, "账号或密码不正确")
	}
	u, e := s.scanUser(s.db.QueryRowContext(r.Context(), userSelect+" WHERE u.id=?", uid))
	if e != nil {
		return e
	}
	if e = s.setSession(w, r, u, hash); e != nil {
		return e
	}
	return result(w, 200, u)
}
func (s *Server) codeLogin(w http.ResponseWriter, r *http.Request, _ *User) error {
	var p CodeProof
	if e := decode(r, &p); e != nil {
		return e
	}
	if e := normalizeProof(&p, "", ""); e != nil {
		return e
	}
	tx, e := s.db.BeginTx(r.Context(), nil)
	if e != nil {
		return e
	}
	defer tx.Rollback()
	var uid string
	e = tx.QueryRow("SELECT user_id FROM auth_identities WHERE channel=? AND target=?", p.Channel, p.Target).Scan(&uid)
	if errors.Is(e, sql.ErrNoRows) {
		return fail(400, "账号尚未注册，请先注册")
	}
	if e != nil {
		return e
	}
	if e = s.consumeCode(tx, p, "login", uid); e != nil {
		return e
	}
	if _, e = tx.Exec("UPDATE auth_identities SET verified_at=? WHERE channel=? AND target=?", now(), p.Channel, p.Target); e != nil {
		return e
	}
	token, expires, e := s.newSession(tx, r, uid)
	if e != nil {
		return e
	}
	u, e := s.scanUser(tx.QueryRow(userSelect+" WHERE u.id=?", uid))
	if e != nil {
		return e
	}
	if e = tx.Commit(); e != nil {
		return e
	}
	s.writeSession(w, r, token, expires)
	return result(w, 200, u)
}
func (s *Server) resetPassword(w http.ResponseWriter, r *http.Request, _ *User) error {
	var v struct {
		CodeProof
		Password string `json:"password"`
	}
	if e := decode(r, &v); e != nil {
		return e
	}
	if e := normalizeProof(&v.CodeProof, "", ""); e != nil {
		return e
	}
	if !validPassword(v.Password) {
		return fail(400, "新密码至少 8 个字符，且不超过 72 字节")
	}
	hash, e := bcrypt.GenerateFromPassword([]byte(v.Password), bcrypt.DefaultCost)
	if e != nil {
		return e
	}
	tx, e := s.db.BeginTx(r.Context(), nil)
	if e != nil {
		return e
	}
	defer tx.Rollback()
	var uid string
	e = tx.QueryRow("SELECT user_id FROM auth_identities WHERE channel=? AND target=?", v.Channel, v.Target).Scan(&uid)
	if errors.Is(e, sql.ErrNoRows) {
		return fail(400, "验证码无效或账号不存在")
	}
	if e != nil {
		return e
	}
	if e = s.consumeCode(tx, v.CodeProof, "reset_password", uid); e != nil {
		return e
	}
	var current int
	revoked := []string{}
	if rows, e := tx.Query("SELECT hash FROM sessions WHERE user_id=?", uid); e == nil {
		for rows.Next() {
			var h string
			if rows.Scan(&h) == nil {
				revoked = append(revoked, h)
			}
		}
		rows.Close()
	}
	if cookie, e := s.sessionCookie(r); e == nil {
		if e = tx.QueryRow("SELECT COUNT(*) FROM sessions WHERE hash=? AND user_id=?", digest(cookie.Value), uid).Scan(&current); e != nil {
			return e
		}
	}
	if _, e = tx.Exec("UPDATE users SET password=? WHERE id=?", string(hash), uid); e != nil {
		return e
	}
	if _, e = tx.Exec("UPDATE auth_identities SET verified_at=? WHERE channel=? AND target=?", now(), v.Channel, v.Target); e != nil {
		return e
	}
	if _, e = tx.Exec("DELETE FROM sessions WHERE user_id=?", uid); e != nil {
		return e
	}
	if _, e = tx.Exec("UPDATE auth_codes SET state='invalidated' WHERE subject_id=? AND state IN('sent','sending')", uid); e != nil {
		return e
	}
	if e = tx.Commit(); e != nil {
		return e
	}
	// 吊销写墓碑即可：墓碑之后的回填因 SET NX 失败，墓碑之前的回填被墓碑覆盖。
	s.revokeSessions(r.Context(), revoked...)
	if current > 0 {
		s.clearSessionCookie(w, r)
	}
	return result(w, 200, map[string]bool{"password_reset": true, "logged_out": current > 0})
}
func (s *Server) bindIdentity(w http.ResponseWriter, r *http.Request, u *User) error {
	var p CodeProof
	if e := decode(r, &p); e != nil {
		return e
	}
	if e := normalizeProof(&p, "", ""); e != nil {
		return e
	}
	tx, e := s.db.BeginTx(r.Context(), nil)
	if e != nil {
		return e
	}
	defer tx.Rollback()
	var owner string
	e = tx.QueryRow("SELECT user_id FROM auth_identities WHERE channel=? AND target=?", p.Channel, p.Target).Scan(&owner)
	if e != nil && !errors.Is(e, sql.ErrNoRows) {
		return e
	}
	if owner != "" && owner != u.ID {
		return fail(409, "此邮箱或手机号已被其他账号使用")
	}
	var oldTarget string
	var verified int64
	e = tx.QueryRow("SELECT target,verified_at FROM auth_identities WHERE user_id=? AND channel=?", u.ID, p.Channel).Scan(&oldTarget, &verified)
	if e != nil && !errors.Is(e, sql.ErrNoRows) {
		return e
	}
	if verified > 0 && oldTarget != p.Target {
		return fail(409, "暂不支持直接替换已验证的联系方式")
	}
	if e = s.consumeCode(tx, p, "bind", u.ID); e != nil {
		return e
	}
	if _, e = tx.Exec("INSERT INTO auth_identities (user_id,channel,target,verified_at) VALUES(?,?,?,?) ON DUPLICATE KEY UPDATE target=VALUES(target),verified_at=VALUES(verified_at)", u.ID, p.Channel, p.Target, now()); e != nil {
		if mysqlx.IsDuplicate(e) {
			return fail(409, "此邮箱或手机号已被其他账号使用")
		}
		return e
	}
	if p.Channel == "email" {
		if _, e = tx.Exec("UPDATE users SET email=? WHERE id=?", p.Target, u.ID); e != nil {
			return e
		}
	}
	fresh, e := s.scanUser(tx.QueryRow(userSelect+" WHERE u.id=?", u.ID))
	if e != nil {
		return e
	}
	if e = tx.Commit(); e != nil {
		return e
	}
	s.refreshUserSessions(r.Context(), u.ID)
	return result(w, 200, fresh)
}
func (s *Server) me(w http.ResponseWriter, r *http.Request, u *User) error { return result(w, 200, u) }
func (s *Server) clearSessionCookie(w http.ResponseWriter, r *http.Request) {
	http.SetCookie(w, &http.Cookie{Name: s.sessionCookieName(r), Value: "", Path: "/", HttpOnly: true, Secure: s.cfg.Production, SameSite: http.SameSiteStrictMode, MaxAge: -1})
	if c, e := s.sessionCookie(r); e == nil && c.Name == "campus_session" {
		http.SetCookie(w, &http.Cookie{Name: "campus_session", Value: "", Path: "/", HttpOnly: true, Secure: s.cfg.Production, SameSite: http.SameSiteStrictMode, MaxAge: -1})
	}
}
func (s *Server) logout(w http.ResponseWriter, r *http.Request, _ *User) error {
	if c, e := s.sessionCookie(r); e == nil {
		if _, e = s.db.ExecContext(r.Context(), "DELETE FROM sessions WHERE hash=?", digest(c.Value)); e != nil {
			return e
		}
		s.revokeSessions(r.Context(), digest(c.Value))
	}
	s.clearSessionCookie(w, r)
	return result(w, 200, map[string]bool{"logged_out": true})
}
