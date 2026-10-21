package platform

import (
	"context"
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"errors"
	"fmt"
	"math/big"
	"net"
	"net/http"
	"net/mail"
	"regexp"
	"strings"
	"sync"
	"time"
)

type verificationState struct {
	sender            VerificationSender
	secret            []byte
	email, sms, local bool
	mu                sync.Mutex
	inbox             map[string]previewCode
}
type previewCode struct {
	code, tokenHash, target string
	expires                 int64
}
type CodeProof struct {
	Channel     string `json:"channel"`
	Target      string `json:"target"`
	ChallengeID string `json:"challenge_id"`
	Code        string `json:"code"`
}

var phonePattern = regexp.MustCompile(`^1[3-9][0-9]{9}$`)
var codePattern = regexp.MustCompile(`^[0-9]{6}$`)

func normalizeIdentity(channel, target string) (string, error) {
	target = strings.TrimSpace(target)
	switch channel {
	case "email":
		target = strings.ToLower(target)
		a, e := mail.ParseAddress(target)
		if e != nil || a.Address != target || len(target) > 254 || !strings.Contains(target, ".") {
			return "", fail(400, "请输入有效的邮箱地址")
		}
		for _, c := range target {
			if c > 127 || c < 33 {
				return "", fail(400, "邮箱地址只能使用英文字母、数字和常见符号")
			}
		}
		return target, nil
	case "sms":
		target = strings.NewReplacer(" ", "", "-", "").Replace(target)
		target = strings.TrimPrefix(target, "+86")
		if len(target) == 13 && strings.HasPrefix(target, "86") {
			target = target[2:]
		}
		if !phonePattern.MatchString(target) {
			return "", fail(400, "请输入有效的中国大陆 11 位手机号")
		}
		return "+86" + target, nil
	default:
		return "", fail(400, "请选择邮箱或手机验证")
	}
}
func maskedTarget(channel, target string) string {
	if channel == "sms" && len(target) > 7 {
		return target[:5] + "****" + target[len(target)-4:]
	}
	parts := strings.SplitN(target, "@", 2)
	if len(parts) == 2 {
		return parts[0][:1] + "***@" + parts[1]
	}
	return "***"
}
func (s *Server) initVerification() error {
	s.cfg.AuthDelivery.Mode = strings.ToLower(strings.TrimSpace(s.cfg.AuthDelivery.Mode))
	sender, e := NewVerificationSender(s.cfg.AuthDelivery, s.cfg.Production)
	if e != nil {
		return e
	}
	secret := []byte(s.cfg.AuthCodeSecret)
	if len(secret) < 32 {
		if s.cfg.Production && s.cfg.AuthDelivery.Mode == "live" {
			return errors.New("AUTH_CODE_SECRET must have at least 32 bytes in production")
		}
		secret = make([]byte, 32)
		if _, e = rand.Read(secret); e != nil {
			return e
		}
	}
	email, sms := DeliveryAvailability(s.cfg.AuthDelivery)
	s.verification = &verificationState{sender: sender, secret: secret, email: email, sms: sms, local: s.cfg.AuthDelivery.Mode == "local", inbox: map[string]previewCode{}}
	_, e = s.db.Exec("INSERT IGNORE INTO auth_identities (user_id,channel,target,verified_at) SELECT u.id,'email',lower(u.email),0 FROM users u LEFT JOIN auth_identities i ON i.user_id=u.id AND i.channel='email' WHERE i.user_id IS NULL AND u.email NOT LIKE '%@accounts.invalid'")
	return e
}
func (s *Server) codeDigest(cid, channel, target, purpose, code string) string {
	mac := hmac.New(sha256.New, s.verification.secret)
	mac.Write([]byte(strings.Join([]string{cid, channel, target, purpose, code}, "\x00")))
	return hex.EncodeToString(mac.Sum(nil))
}
func (s *Server) requestCode(w http.ResponseWriter, r *http.Request, _ *User) error {
	var v struct {
		Channel string `json:"channel"`
		Target  string `json:"target"`
		Purpose string `json:"purpose"`
	}
	if e := decode(r, &v); e != nil {
		return e
	}
	target, e := normalizeIdentity(v.Channel, v.Target)
	if e != nil {
		return e
	}
	if v.Purpose != "register" && v.Purpose != "login" && v.Purpose != "reset_password" && v.Purpose != "bind" {
		return fail(400, "无效的验证码用途")
	}
	if (v.Channel == "email" && !s.verification.email) || (v.Channel == "sms" && !s.verification.sms) {
		return fail(503, "该验证码发送服务尚未配置，请使用已有账号密码登录")
	}
	subject := ""
	e = s.db.QueryRowContext(r.Context(), "SELECT user_id FROM auth_identities WHERE channel=? AND target=?", v.Channel, target).Scan(&subject)
	if e != nil && !errors.Is(e, sql.ErrNoRows) {
		return e
	}
	if v.Purpose == "register" && subject != "" {
		return fail(409, "该邮箱或手机号已注册，请直接登录或找回密码")
	}
	if v.Purpose == "bind" {
		u, e := s.currentUser(r)
		if e != nil {
			return e
		}
		if subject != "" && subject != u.ID {
			return fail(409, "这个邮箱或手机号已被其他账号使用")
		}
		subject = u.ID
	}
	n, e := rand.Int(rand.Reader, big.NewInt(1000000))
	if e != nil {
		return e
	}
	code := fmt.Sprintf("%06d", n.Int64())
	cid := id("verify_")
	expires := now() + 5*60*1000
	tx, e := s.db.BeginTx(r.Context(), nil)
	if e != nil {
		return e
	}
	defer tx.Rollback()
	var recent int64
	e = tx.QueryRow("SELECT created_at FROM auth_codes WHERE channel=? AND target=? AND purpose=? ORDER BY created_at DESC LIMIT 1", v.Channel, target, v.Purpose).Scan(&recent)
	if e != nil && !errors.Is(e, sql.ErrNoRows) {
		return e
	}
	if now()-recent < 60000 {
		seconds := (60000 - (now() - recent) + 999) / 1000
		w.Header().Set("Retry-After", fmt.Sprint(seconds))
		return fail(429, fmt.Sprintf("请在 %d 秒后重新获取验证码", seconds))
	}
	// 短信/邮件防刷配额改用 Redis 滑动窗口：单号码 10 次/天、单 IP 60 次/小时、单通道 500 次/天（控制短信费用上限）。
	for _, q := range []struct {
		k      string
		n      int
		window time.Duration
	}{{"target:" + digest(v.Channel+target), 10, 24 * time.Hour}, {"ip:" + digest(s.clientIP(r)), 60, time.Hour}, {"total:" + v.Channel, 500, 24 * time.Hour}} {
		if !s.limiter.Window(r.Context(), "authcode:"+q.k, q.n, q.window).Allowed {
			return fail(429, "验证码请求过于频繁，请稍后再试")
		}
	}
	if _, e = tx.Exec("UPDATE auth_codes SET state='superseded' WHERE channel=? AND target=? AND purpose=? AND state='sent'", v.Channel, target, v.Purpose); e != nil {
		return e
	}
	if _, e = tx.Exec("INSERT INTO auth_codes(id,channel,target,purpose,subject_id,code_hash,created_at,expires_at,state) VALUES(?,?,?,?,?,?,?,?,?)", cid, v.Channel, target, v.Purpose, subject, s.codeDigest(cid, v.Channel, target, v.Purpose, code), now(), expires, "sending"); e != nil {
		return e
	}
	if e = tx.Commit(); e != nil {
		return e
	}
	ctx, cancel := context.WithTimeout(r.Context(), 12*time.Second)
	e = s.verification.sender.Send(ctx, VerificationMessage{Channel: v.Channel, Target: target, Purpose: v.Purpose, Code: code})
	cancel()
	state := "sent"
	if e != nil {
		state = "failed"
	}
	saveCtx, saveCancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer saveCancel()
	res, saveErr := s.db.ExecContext(saveCtx, "UPDATE auth_codes SET state=? WHERE id=? AND state='sending'", state, cid)
	if saveErr != nil {
		return saveErr
	}
	if changed, _ := res.RowsAffected(); changed != 1 {
		return fail(409, "账号状态发生变化，请重新获取验证码")
	}
	if e != nil {
		return fail(502, "验证码发送失败，请稍后重试；没有发送成功的提示不代表已送达")
	}
	out := map[string]any{"challenge_id": cid, "expires_in": 300, "retry_after": 60, "target": maskedTarget(v.Channel, target), "delivery": "sent"}
	if s.verification.local {
		token := id("")
		s.verification.mu.Lock()
		for k, entry := range s.verification.inbox {
			if entry.expires < now() {
				delete(s.verification.inbox, k)
			}
		}
		s.verification.inbox[cid] = previewCode{code: code, tokenHash: digest(token), target: target, expires: expires}
		s.verification.mu.Unlock()
		out["delivery"] = "local"
		out["delivery_token"] = token
	}
	return result(w, 200, out)
}
func (s *Server) devInbox(w http.ResponseWriter, r *http.Request, _ *User) error {
	host, _, e := net.SplitHostPort(r.RemoteAddr)
	if e != nil || !net.ParseIP(host).IsLoopback() || !s.verification.local || s.cfg.Production {
		return fail(404, "页面不存在")
	}
	if original := r.Header.Get("X-Campus-Client-IP"); original != "" && !net.ParseIP(original).IsLoopback() {
		return fail(404, "测试收件箱仅允许本机访问")
	}
	var v struct {
		ChallengeID string `json:"challenge_id"`
		Token       string `json:"delivery_token"`
	}
	if e := decode(r, &v); e != nil {
		return e
	}
	s.verification.mu.Lock()
	entry, ok := s.verification.inbox[v.ChallengeID]
	s.verification.mu.Unlock()
	if !ok || entry.expires <= now() || !hmac.Equal([]byte(entry.tokenHash), []byte(digest(v.Token))) {
		return fail(404, "测试验证码不存在或已过期")
	}
	return result(w, 200, map[string]any{"code": entry.code, "target": entry.target, "expires_at": entry.expires, "notice": "本地测试收件箱，没有发送真实邮件或短信"})
}

// Failed guesses commit their attempt counter. Successful consumption is left
// inside the caller's transaction so identity/password changes are atomic.
func (s *Server) consumeCode(tx *sql.Tx, p CodeProof, purpose, subject string) error {
	if !codePattern.MatchString(p.Code) || p.ChallengeID == "" {
		return fail(400, "请输入收到的 6 位验证码")
	}
	var hash, storedSubject, state string
	var expires int64
	var attempts int
	// FOR UPDATE 锁住验证码行：两个并发请求用同一验证码时，第二个会等第一个提交后读到 state='used'，
	// 防止一码多用（快照读下两个事务都会看到 state='sent'）。
	e := tx.QueryRow("SELECT code_hash,subject_id,expires_at,attempts,state FROM auth_codes WHERE id=? AND channel=? AND target=? AND purpose=? FOR UPDATE", p.ChallengeID, p.Channel, p.Target, purpose).Scan(&hash, &storedSubject, &expires, &attempts, &state)
	if errors.Is(e, sql.ErrNoRows) {
		return fail(400, "验证码无效或用途不匹配，请重新获取")
	}
	if e != nil {
		return e
	}
	if expires <= now() || state != "sent" || storedSubject != subject {
		return fail(400, "验证码已过期或失效，请重新获取")
	}
	if attempts >= 5 {
		return fail(429, "验证码尝试次数过多，请重新获取")
	}
	if !hmac.Equal([]byte(hash), []byte(s.codeDigest(p.ChallengeID, p.Channel, p.Target, purpose, p.Code))) {
		if _, e = tx.Exec("UPDATE auth_codes SET attempts=attempts+1 WHERE id=?", p.ChallengeID); e != nil {
			return e
		}
		if e = tx.Commit(); e != nil {
			return e
		}
		return fail(400, "验证码不正确，请检查后重试")
	}
	_, e = tx.Exec("UPDATE auth_codes SET state='used',consumed_at=? WHERE id=?", now(), p.ChallengeID)
	return e
}
