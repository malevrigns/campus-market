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
	u, e := s.scanUser(s.db.QueryRowContext(r.Context(), userSelect+" JOIN sessions s ON s.user_id=u.id WHERE s.hash=? AND s.expires_at>?", digest(c.Value), now()))
	if errors.Is(e, sql.ErrNoRows) {
		return nil, fail(401, "登录已过期，请重新登录")
	}
	return u, e
}
func (s *Server) newSession(tx *sql.Tx, r *http.Request, uid string) (string, time.Time, error) {
	token := id("")
	expires := time.Now().Add(7 * 24 * time.Hour)
	if _, e := tx.Exec("DELETE FROM sessions WHERE expires_at<?", now()); e != nil {
		return "", expires, e
	}
	if old, e := s.sessionCookie(r); e == nil {
		if _, e = tx.Exec("DELETE FROM sessions WHERE hash=? AND user_id=?", digest(old.Value), uid); e != nil {
			return "", expires, e
		}
	}
	_, e := tx.Exec("INSERT INTO sessions VALUES(?,?,?)", digest(token), uid, expires.UnixMilli())
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
	if e = tx.QueryRow("SELECT password FROM users WHERE id=?", u.ID).Scan(&currentHash); e != nil {
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
	if _, e = tx.Exec("INSERT INTO users VALUES(?,?,?,?,?)", uid, email, v.Name, string(hash), now()); e != nil {
		return e
	}
	if _, e = tx.Exec("INSERT INTO auth_identities VALUES(?,?,?,?)", uid, v.Channel, v.Target, now()); e != nil {
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
	if _, e = tx.Exec("INSERT INTO auth_identities VALUES(?,?,?,?) ON CONFLICT(user_id,channel) DO UPDATE SET target=excluded.target,verified_at=excluded.verified_at", u.ID, p.Channel, p.Target, now()); e != nil {
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
	}
	s.clearSessionCookie(w, r)
	return result(w, 200, map[string]bool{"logged_out": true})
}
