package platform

import (
	"crypto/sha256"
	"encoding/hex"
	"golang.org/x/crypto/bcrypt"
	"net/http"
	"net/mail"
	"strings"
	"time"
	"unicode/utf8"
)

type User struct {
	IsAdmin bool   `json:"is_admin"`
	ID      string `json:"id"`
	Name    string `json:"name"`
	Email   string `json:"email"`
}

func digest(token string) string { h := sha256.Sum256([]byte(token)); return hex.EncodeToString(h[:]) }
func (s *Server) currentUser(r *http.Request) (*User, error) {
	c, e := r.Cookie("campus_session")
	if e != nil {
		return nil, fail(401, "请先登录")
	}
	u := &User{}
	e = s.db.QueryRowContext(r.Context(), "SELECT u.id,u.name,u.email FROM users u JOIN sessions s ON s.user_id=u.id WHERE s.hash=? AND s.expires_at>?", digest(c.Value), now()).Scan(&u.ID, &u.Name, &u.Email)
	if e != nil {
		return nil, fail(401, "登录已过期，请重新登录")
	}
	u.IsAdmin = s.isAgentAdmin(u.ID)
	return u, nil
}
func (s *Server) setSession(w http.ResponseWriter, r *http.Request, u *User) error {
	u.IsAdmin = s.isAgentAdmin(u.ID)
	token := id("")
	expires := time.Now().Add(7 * 24 * time.Hour)
	tx, e := s.db.BeginTx(r.Context(), nil)
	if e != nil {
		return e
	}
	defer tx.Rollback()
	if _, e = tx.Exec("DELETE FROM sessions WHERE expires_at<?", now()); e != nil {
		return e
	}
	if old, e := r.Cookie("campus_session"); e == nil {
		if _, e = tx.Exec("DELETE FROM sessions WHERE hash=?", digest(old.Value)); e != nil {
			return e
		}
	}
	if _, e = tx.Exec("INSERT INTO sessions VALUES(?,?,?)", digest(token), u.ID, expires.UnixMilli()); e != nil {
		return e
	}
	if e = tx.Commit(); e != nil {
		return e
	}
	http.SetCookie(w, &http.Cookie{Name: "campus_session", Value: token, Path: "/", HttpOnly: true, Secure: s.cfg.Production, SameSite: http.SameSiteStrictMode, Expires: expires, MaxAge: 7 * 86400})
	return nil
}
func (s *Server) register(w http.ResponseWriter, r *http.Request, _ *User) error {
	var v struct {
		Name     string `json:"name"`
		Email    string `json:"email"`
		Password string `json:"password"`
	}
	if e := decode(r, &v); e != nil {
		return e
	}
	v.Name = strings.TrimSpace(v.Name)
	v.Email = strings.ToLower(strings.TrimSpace(v.Email))
	addr, e := mail.ParseAddress(v.Email)
	if e != nil || addr.Address != v.Email || len(v.Email) > 254 || utf8.RuneCountInString(v.Name) < 2 || utf8.RuneCountInString(v.Name) > 24 || len(v.Password) < 10 || len(v.Password) > 72 {
		return fail(400, "请填写有效邮箱、2–24 字昵称和 10–72 字节密码")
	}
	hash, e := bcrypt.GenerateFromPassword([]byte(v.Password), bcrypt.DefaultCost)
	if e != nil {
		return e
	}
	u := &User{ID: id("u_"), Name: v.Name, Email: v.Email}
	res, e := s.db.ExecContext(r.Context(), "INSERT INTO users VALUES(?,?,?,?,?) ON CONFLICT(email) DO NOTHING", u.ID, u.Email, u.Name, string(hash), now())
	if e != nil {
		return e
	}
	n, _ := res.RowsAffected()
	if n == 0 {
		return fail(409, "该邮箱已注册")
	}
	if e = s.setSession(w, r, u); e != nil {
		return e
	}
	return result(w, 201, u)
}

var dummyHash, _ = bcrypt.GenerateFromPassword([]byte("unused-dummy-password"), bcrypt.DefaultCost)

func (s *Server) login(w http.ResponseWriter, r *http.Request, _ *User) error {
	var v struct {
		Email    string `json:"email"`
		Password string `json:"password"`
	}
	if e := decode(r, &v); e != nil {
		return e
	}
	if len(v.Password) > 72 {
		return fail(401, "邮箱或密码不正确")
	}
	u := &User{}
	var hash string
	e := s.db.QueryRowContext(r.Context(), "SELECT id,name,email,password FROM users WHERE email=?", strings.ToLower(strings.TrimSpace(v.Email))).Scan(&u.ID, &u.Name, &u.Email, &hash)
	if e != nil {
		_ = bcrypt.CompareHashAndPassword(dummyHash, []byte(v.Password))
		return fail(401, "邮箱或密码不正确")
	}
	if bcrypt.CompareHashAndPassword([]byte(hash), []byte(v.Password)) != nil {
		return fail(401, "邮箱或密码不正确")
	}
	if e = s.setSession(w, r, u); e != nil {
		return e
	}
	return result(w, 200, u)
}
func (s *Server) me(w http.ResponseWriter, r *http.Request, u *User) error { return result(w, 200, u) }
func (s *Server) logout(w http.ResponseWriter, r *http.Request, _ *User) error {
	c, e := r.Cookie("campus_session")
	if e == nil {
		if _, e = s.db.ExecContext(r.Context(), "DELETE FROM sessions WHERE hash=?", digest(c.Value)); e != nil {
			return e
		}
	}
	http.SetCookie(w, &http.Cookie{Name: "campus_session", Value: "", Path: "/", HttpOnly: true, Secure: s.cfg.Production, SameSite: http.SameSiteStrictMode, MaxAge: -1})
	return result(w, 200, map[string]bool{"logged_out": true})
}
