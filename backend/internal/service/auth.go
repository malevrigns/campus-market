package service

import (
	"context"
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"math/big"
	"net"
	"net/mail"
	"regexp"
	"strings"
	"sync"
	"time"

	"golang.org/x/crypto/bcrypt"

	"qingfly/edu-mall/internal/domain"
	"qingfly/edu-mall/internal/pkg/idgen"
	"qingfly/edu-mall/internal/port"
)

var phonePattern = regexp.MustCompile(`^1[3-9][0-9]{9}$`)
var codePattern = regexp.MustCompile(`^[0-9]{6}$`)

const (
	sessionTTL   = 7 * 24 * time.Hour
	codeTTL      = 5 * time.Minute
	codeCooldown = time.Minute
)

type AuthConfig struct {
	Secret         string
	Production     bool
	LocalDelivery  bool
	EmailAvailable bool
	SMSAvailable   bool
	RequireSecret  bool
}

type AuthService struct {
	store   port.AuthStore
	cache   port.SessionCache
	sender  port.VerificationSender
	limiter port.RateLimiter
	admins  *AdminSet
	cfg     AuthConfig
	secret  []byte

	mu    sync.Mutex
	inbox map[string]previewCode
}

type previewCode struct {
	code, tokenHash, target string
	expires                 int64
}

func NewAuth(store port.AuthStore, cache port.SessionCache, sender port.VerificationSender, limiter port.RateLimiter, admins *AdminSet, cfg AuthConfig) (*AuthService, error) {
	secret := []byte(cfg.Secret)
	if len(secret) < 32 {
		if cfg.RequireSecret {
			return nil, errors.New("AUTH_CODE_SECRET must have at least 32 bytes in production")
		}
		secret = make([]byte, 32)
		if _, err := rand.Read(secret); err != nil {
			return nil, err
		}
	}
	return &AuthService{store: store, cache: cache, sender: sender, limiter: limiter, admins: admins, cfg: cfg, secret: secret, inbox: map[string]previewCode{}}, nil
}

type RequestCodeInput struct {
	Channel string
	Target  string
	Purpose string
}

type RegisterInput struct {
	domain.CodeProof
	Name     string
	Email    string
	Phone    string
	Password string
}

type LoginInput struct {
	Account  string
	Email    string
	Password string
}

type ResetPasswordInput struct {
	domain.CodeProof
	Password string
}

type DevInboxInput struct {
	ChallengeID string
	Token       string
}

// Init 在启动时回填历史邮箱身份；失败会阻止服务启动。
func (s *AuthService) Init(ctx context.Context) error {
	return s.store.BackfillEmailIdentities(ctx)
}

func (s *AuthService) Delivery() (email, sms, local bool) {
	return s.cfg.EmailAvailable, s.cfg.SMSAvailable, s.cfg.LocalDelivery
}

func (s *AuthService) IsAdmin(uid string) bool { return s.admins.IsAdmin(uid) }

func (s *AuthService) SetAdminUserIDs(csv string) { s.admins.SetCSV(csv) }

func normalizeIdentity(channel, target string) (string, error) {
	target = strings.TrimSpace(target)
	switch channel {
	case "email":
		target = strings.ToLower(target)
		a, err := mail.ParseAddress(target)
		if err != nil || a.Address != target || len(target) > 254 || !strings.Contains(target, ".") {
			return "", domain.Invalid("请输入有效的邮箱地址")
		}
		for _, c := range target {
			if c > 127 || c < 33 {
				return "", domain.Invalid("邮箱地址只能使用英文字母、数字和常见符号")
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
			return "", domain.Invalid("请输入有效的中国大陆 11 位手机号")
		}
		return "+86" + target, nil
	default:
		return "", domain.Invalid("请选择邮箱或手机验证")
	}
}

func maskedTarget(channel, target string) string {
	if channel == "sms" && len(target) > 7 {
		return target[:5] + "****" + target[len(target)-4:]
	}
	parts := strings.SplitN(target, "@", 2)
	if len(parts) == 2 && len(parts[0]) > 0 {
		return parts[0][:1] + "***@" + parts[1]
	}
	return "***"
}

func normalizeProof(p *domain.CodeProof, email, phone string) error {
	if p.Channel == "" {
		if email != "" {
			p.Channel, p.Target = "email", email
		} else if phone != "" {
			p.Channel, p.Target = "sms", phone
		}
	}
	if p.Target == "" {
		if p.Channel == "email" {
			p.Target = email
		} else {
			p.Target = phone
		}
	}
	target, err := normalizeIdentity(p.Channel, p.Target)
	if err != nil {
		return err
	}
	p.Target = target
	return nil
}

func (s *AuthService) codeDigest(cid, channel, target, purpose, code string) string {
	mac := hmac.New(sha256.New, s.secret)
	mac.Write([]byte(strings.Join([]string{cid, channel, target, purpose, code}, "\x00")))
	return hex.EncodeToString(mac.Sum(nil))
}

// RequestCode 发放验证码：业务校验、冷却/配额、落库、发送、状态回写。
func (s *AuthService) RequestCode(ctx context.Context, clientIP string, actor *domain.User, input RequestCodeInput) (domain.CodeChallenge, error) {
	var out domain.CodeChallenge
	target, err := normalizeIdentity(input.Channel, input.Target)
	if err != nil {
		return out, err
	}
	switch input.Purpose {
	case "register", "login", "reset_password", "bind":
	default:
		return out, domain.Invalid("无效的验证码用途")
	}
	if (input.Channel == "email" && !s.cfg.EmailAvailable) || (input.Channel == "sms" && !s.cfg.SMSAvailable) {
		return out, domain.Unavailable("该验证码发送服务尚未配置，请使用已有账号密码登录")
	}
	subject, found, err := s.store.FindIdentity(ctx, input.Channel, target)
	if err != nil {
		return out, err
	}
	if input.Purpose == "register" && found {
		return out, domain.Conflict("该邮箱或手机号已注册，请直接登录或找回密码")
	}
	if input.Purpose == "bind" {
		if actor == nil {
			return out, domain.Unauthorized("请先登录")
		}
		if found && subject != actor.ID {
			return out, domain.Conflict("这个邮箱或手机号已被其他账号使用")
		}
		subject = actor.ID
	}
	recent, hasRecent, err := s.store.LastCodeAt(ctx, input.Channel, target, input.Purpose)
	if err != nil {
		return out, err
	}
	if hasRecent && now()-recent < codeCooldown.Milliseconds() {
		seconds := int((codeCooldown.Milliseconds() - (now() - recent) + 999) / 1000)
		return out, domain.Retryable(domain.KindTooMany, fmt.Sprintf("请在 %d 秒后重新获取验证码", seconds), seconds)
	}
	for _, q := range []struct {
		key    string
		limit  int
		window time.Duration
	}{
		{"authcode:target:" + digestToken(input.Channel+target), 10, 24 * time.Hour},
		{"authcode:ip:" + digestToken(clientIP), 60, time.Hour},
		{"authcode:total:" + input.Channel, 500, 24 * time.Hour},
	} {
		if retry, allowed := s.limiter.Window(ctx, q.key, q.limit, q.window); !allowed {
			return out, domain.Retryable(domain.KindTooMany, "验证码请求过于频繁，请稍后再试", int(retry.Seconds()+0.999))
		}
	}
	n, err := rand.Int(rand.Reader, big.NewInt(1000000))
	if err != nil {
		return out, err
	}
	code := fmt.Sprintf("%06d", n.Int64())
	cid := idgen.New("verify_")
	expires := now() + codeTTL.Milliseconds()
	if err = s.store.CreateCode(ctx, domain.AuthCode{ID: cid, Channel: input.Channel, Target: target, Purpose: input.Purpose, SubjectID: subject, CodeHash: s.codeDigest(cid, input.Channel, target, input.Purpose, code), CreatedAt: now(), ExpiresAt: expires, State: "sending"}); err != nil {
		return out, err
	}
	sendCtx, cancel := context.WithTimeout(ctx, 12*time.Second)
	sendErr := s.sender.Send(sendCtx, domain.VerificationMessage{Channel: input.Channel, Target: target, Purpose: input.Purpose, Code: code})
	cancel()
	state := "sent"
	if sendErr != nil {
		state = "failed"
	}
	saveCtx, saveCancel := context.WithTimeout(context.Background(), 3*time.Second)
	changed, saveErr := s.store.SetCodeState(saveCtx, cid, state)
	saveCancel()
	if saveErr != nil {
		return out, saveErr
	}
	if !changed {
		return out, domain.Conflict("账号状态发生变化，请重新获取验证码")
	}
	if sendErr != nil {
		return out, domain.NewError(domain.KindBadGateway, "验证码发送失败，请稍后重试；没有发送成功的提示不代表已送达")
	}
	out = domain.CodeChallenge{ChallengeID: cid, ExpiresIn: 300, RetryAfter: 60, Target: maskedTarget(input.Channel, target), Delivery: "sent"}
	if s.cfg.LocalDelivery {
		token := idgen.New("")
		s.mu.Lock()
		for k, entry := range s.inbox {
			if entry.expires < now() {
				delete(s.inbox, k)
			}
		}
		s.inbox[cid] = previewCode{code: code, tokenHash: digestToken(token), target: target, expires: expires}
		s.mu.Unlock()
		out.Delivery = "local"
		out.DeliveryToken = token
	}
	return out, nil
}

func isLoopback(remoteAddr string) bool {
	host, _, err := net.SplitHostPort(remoteAddr)
	if err != nil {
		host = remoteAddr
	}
	ip := net.ParseIP(host)
	return ip != nil && ip.IsLoopback()
}

func (s *AuthService) DevInbox(_ context.Context, remoteAddr, clientIP string, input DevInboxInput) (domain.CodePreview, error) {
	if !isLoopback(remoteAddr) || !s.cfg.LocalDelivery || s.cfg.Production {
		return domain.CodePreview{}, domain.NotFound("页面不存在")
	}
	if clientIP != "" && !isLoopback(net.JoinHostPort(clientIP, "0")) {
		return domain.CodePreview{}, domain.NotFound("测试收件箱仅允许本机访问")
	}
	s.mu.Lock()
	entry, ok := s.inbox[input.ChallengeID]
	s.mu.Unlock()
	if !ok || entry.expires <= now() || !hmac.Equal([]byte(entry.tokenHash), []byte(digestToken(input.Token))) {
		return domain.CodePreview{}, domain.NotFound("测试验证码不存在或已过期")
	}
	return domain.CodePreview{Code: entry.code, Target: entry.target, ExpiresAt: entry.expires, Notice: "本地测试收件箱，没有发送真实邮件或短信"}, nil
}

func (s *AuthService) newSession() (domain.Session, string) {
	token := idgen.New("")
	expires := time.Now().Add(sessionTTL).UnixMilli()
	return domain.Session{Token: token, ExpiresAt: expires}, digestToken(token)
}

func (s *AuthService) Register(ctx context.Context, input RegisterInput, oldSessionHash string) (domain.RegisterResult, error) {
	var out domain.RegisterResult
	input.Name = strings.TrimSpace(input.Name)
	if !validText(input.Name, 1, 24) {
		return out, domain.Invalid("昵称需要 1–24 个字")
	}
	if !validPassword(input.Password) {
		return out, domain.Invalid("密码至少 8 个字符，且不超过 72 字节")
	}
	if err := normalizeProof(&input.CodeProof, input.Email, input.Phone); err != nil {
		return out, err
	}
	hash, err := bcrypt.GenerateFromPassword([]byte(input.Password), bcrypt.DefaultCost)
	if err != nil {
		return out, err
	}
	if _, found, err := s.store.FindIdentity(ctx, input.Channel, input.Target); err != nil {
		return out, err
	} else if found {
		return out, domain.Conflict("该邮箱或手机号已注册，请直接登录或找回密码")
	}
	expected := s.codeDigest(input.ChallengeID, input.Channel, input.Target, "register", input.Code)
	if !codePattern.MatchString(input.Code) || input.ChallengeID == "" {
		return out, domain.Invalid("请输入收到的 6 位验证码")
	}
	if err = s.store.ConsumeCode(ctx, input.CodeProof, "register", "", expected, now()); err != nil {
		return out, err
	}
	uid := idgen.New("u_")
	email := input.Target
	if input.Channel == "sms" {
		email = "phone+" + uid + "@accounts.invalid"
	}
	session, sessionHash := s.newSession()
	var user domain.User
	err = s.store.InTx(ctx, func(tx port.AuthTx) error {
		if _, found, err := tx.FindIdentity(ctx, input.Channel, input.Target); err != nil {
			return err
		} else if found {
			return domain.Conflict("该邮箱或手机号已注册，请直接登录或找回密码")
		}
		if err := tx.InsertUser(ctx, uid, email, input.Name, string(hash), now()); err != nil {
			if domain.DuplicateConstraint(err) != "" {
				return domain.Conflict("该邮箱或手机号已注册，请直接登录或找回密码")
			}
			return err
		}
		if err := tx.InsertIdentity(ctx, uid, input.Channel, input.Target, now()); err != nil {
			if domain.DuplicateConstraint(err) != "" {
				return domain.Conflict("该邮箱或手机号已注册，请直接登录或找回密码")
			}
			return err
		}
		if oldSessionHash != "" {
			if err := tx.DeleteSession(ctx, oldSessionHash, uid); err != nil {
				return err
			}
		}
		if err := tx.InsertSession(ctx, sessionHash, uid, session.ExpiresAt); err != nil {
			return err
		}
		user, err = tx.GetUser(ctx, uid)
		return err
	})
	if err != nil {
		return out, err
	}
	user.IsAdmin = s.IsAdmin(user.ID)
	if oldSessionHash != "" {
		s.cache.Revoke(context.WithoutCancel(ctx), oldSessionHash)
	}
	out.User, out.Session = user, session
	return out, nil
}

func (s *AuthService) Login(ctx context.Context, input LoginInput, oldSessionHash string) (domain.RegisterResult, error) {
	var out domain.RegisterResult
	if input.Account == "" {
		input.Account = input.Email
	}
	channel := "sms"
	if strings.Contains(input.Account, "@") {
		channel = "email"
	}
	target, err := normalizeIdentity(channel, input.Account)
	if err != nil {
		return out, err
	}
	if len(input.Password) > 72 {
		return out, domain.Unauthorized("账号或密码不正确")
	}
	uid, hash, found, err := s.store.FindCredential(ctx, channel, target)
	if err != nil {
		return out, err
	}
	if !found {
		_ = bcrypt.CompareHashAndPassword(dummyHash, []byte(input.Password))
		return out, domain.Unauthorized("账号或密码不正确")
	}
	if bcrypt.CompareHashAndPassword([]byte(hash), []byte(input.Password)) != nil {
		return out, domain.Unauthorized("账号或密码不正确")
	}
	user, session, err := s.CreateSessionForVerifiedPassword(ctx, uid, hash, oldSessionHash)
	if err != nil {
		return out, err
	}
	out.User, out.Session = user, session
	return out, nil
}

var dummyHash, _ = bcrypt.GenerateFromPassword([]byte("unused-dummy-password"), bcrypt.DefaultCost)

// CreateSessionForVerifiedPassword 在密码摘要未变化的条件下创建会话；
// 登录与测试都复用这一条并发安全的路径。
func (s *AuthService) CreateSessionForVerifiedPassword(ctx context.Context, userID, verifiedHash, oldSessionHash string) (domain.User, domain.Session, error) {
	session, sessionHash := s.newSession()
	var user domain.User
	err := s.store.InTx(ctx, func(tx port.AuthTx) error {
		current, err := tx.GetUserPasswordForUpdate(ctx, userID)
		if err != nil {
			return err
		}
		if current != verifiedHash {
			return domain.Unauthorized("密码已更新，请使用新密码重新登录")
		}
		if oldSessionHash != "" {
			if err := tx.DeleteSession(ctx, oldSessionHash, userID); err != nil {
				return err
			}
		}
		if err := tx.InsertSession(ctx, sessionHash, userID, session.ExpiresAt); err != nil {
			return err
		}
		user, err = tx.GetUser(ctx, userID)
		return err
	})
	if err != nil {
		return user, session, err
	}
	user.IsAdmin = s.IsAdmin(user.ID)
	if oldSessionHash != "" {
		s.cache.Revoke(context.WithoutCancel(ctx), oldSessionHash)
	}
	return user, session, nil
}

func (s *AuthService) CodeLogin(ctx context.Context, proof domain.CodeProof, oldSessionHash string) (domain.RegisterResult, error) {
	var out domain.RegisterResult
	if err := normalizeProof(&proof, "", ""); err != nil {
		return out, err
	}
	if !codePattern.MatchString(proof.Code) || proof.ChallengeID == "" {
		return out, domain.Invalid("请输入收到的 6 位验证码")
	}
	uid, found, err := s.store.FindIdentity(ctx, proof.Channel, proof.Target)
	if err != nil {
		return out, err
	}
	if !found {
		return out, domain.Invalid("账号尚未注册，请先注册")
	}
	if err = s.store.ConsumeCode(ctx, proof, "login", uid, s.codeDigest(proof.ChallengeID, proof.Channel, proof.Target, "login", proof.Code), now()); err != nil {
		return out, err
	}
	session, sessionHash := s.newSession()
	var user domain.User
	err = s.store.InTx(ctx, func(tx port.AuthTx) error {
		if err := tx.UpdateIdentityVerified(ctx, proof.Channel, proof.Target, now()); err != nil {
			return err
		}
		if oldSessionHash != "" {
			if err := tx.DeleteSession(ctx, oldSessionHash, uid); err != nil {
				return err
			}
		}
		if err := tx.InsertSession(ctx, sessionHash, uid, session.ExpiresAt); err != nil {
			return err
		}
		user, err = tx.GetUser(ctx, uid)
		return err
	})
	if err != nil {
		return out, err
	}
	user.IsAdmin = s.IsAdmin(user.ID)
	if oldSessionHash != "" {
		s.cache.Revoke(context.WithoutCancel(ctx), oldSessionHash)
	}
	out.User, out.Session = user, session
	return out, nil
}

func (s *AuthService) ResetPassword(ctx context.Context, input ResetPasswordInput, currentSessionHash string) (domain.ResetPasswordResult, error) {
	var out domain.ResetPasswordResult
	if err := normalizeProof(&input.CodeProof, "", ""); err != nil {
		return out, err
	}
	if !validPassword(input.Password) {
		return out, domain.Invalid("新密码至少 8 个字符，且不超过 72 字节")
	}
	hash, err := bcrypt.GenerateFromPassword([]byte(input.Password), bcrypt.DefaultCost)
	if err != nil {
		return out, err
	}
	if !codePattern.MatchString(input.Code) || input.ChallengeID == "" {
		return out, domain.Invalid("请输入收到的 6 位验证码")
	}
	uid, found, err := s.store.FindIdentity(ctx, input.Channel, input.Target)
	if err != nil {
		return out, err
	}
	if !found {
		return out, domain.Invalid("验证码无效或账号不存在")
	}
	if err = s.store.ConsumeCode(ctx, input.CodeProof, "reset_password", uid, s.codeDigest(input.ChallengeID, input.Channel, input.Target, "reset_password", input.Code), now()); err != nil {
		return out, err
	}
	var revoked []string
	var current int
	err = s.store.InTx(ctx, func(tx port.AuthTx) error {
		sessions, err := tx.ListUserSessions(ctx, uid)
		if err != nil {
			return err
		}
		revoked = sessions
		if currentSessionHash != "" {
			if current, err = tx.CountSession(ctx, currentSessionHash, uid); err != nil {
				return err
			}
		}
		if err = tx.UpdatePassword(ctx, uid, string(hash)); err != nil {
			return err
		}
		if err = tx.UpdateIdentityVerified(ctx, input.Channel, input.Target, now()); err != nil {
			return err
		}
		if err = tx.DeleteUserSessions(ctx, uid); err != nil {
			return err
		}
		return tx.InvalidateUserCodes(ctx, uid)
	})
	if err != nil {
		return out, err
	}
	s.cache.Revoke(context.WithoutCancel(ctx), revoked...)
	out.Reset, out.LoggedOut, out.Revoked, out.Current = true, current > 0, revoked, current > 0
	return out, nil
}

func (s *AuthService) BindIdentity(ctx context.Context, actor *domain.User, proof domain.CodeProof) (domain.User, error) {
	if actor == nil {
		return domain.User{}, domain.Unauthorized("请先登录")
	}
	if err := normalizeProof(&proof, "", ""); err != nil {
		return domain.User{}, err
	}
	if !codePattern.MatchString(proof.Code) || proof.ChallengeID == "" {
		return domain.User{}, domain.Invalid("请输入收到的 6 位验证码")
	}
	owner, found, err := s.store.FindIdentity(ctx, proof.Channel, proof.Target)
	if err != nil {
		return domain.User{}, err
	}
	if found && owner != actor.ID {
		return domain.User{}, domain.Conflict("此邮箱或手机号已被其他账号使用")
	}
	oldTarget, verifiedAt, hasIdentity, err := s.store.GetUserIdentity(ctx, actor.ID, proof.Channel)
	if err != nil {
		return domain.User{}, err
	}
	if hasIdentity && verifiedAt > 0 && oldTarget != proof.Target {
		return domain.User{}, domain.Conflict("暂不支持直接替换已验证的联系方式")
	}
	if err = s.store.ConsumeCode(ctx, proof, "bind", actor.ID, s.codeDigest(proof.ChallengeID, proof.Channel, proof.Target, "bind", proof.Code), now()); err != nil {
		return domain.User{}, err
	}
	var fresh domain.User
	err = s.store.InTx(ctx, func(tx port.AuthTx) error {
		if err := tx.UpsertIdentity(ctx, actor.ID, proof.Channel, proof.Target, now()); err != nil {
			if domain.DuplicateConstraint(err) != "" {
				return domain.Conflict("此邮箱或手机号已被其他账号使用")
			}
			return err
		}
		if proof.Channel == "email" {
			if err := tx.UpdateUserEmail(ctx, actor.ID, proof.Target); err != nil {
				return err
			}
		}
		var err error
		fresh, err = tx.GetUser(ctx, actor.ID)
		return err
	})
	if err != nil {
		return fresh, err
	}
	fresh.IsAdmin = s.IsAdmin(fresh.ID)
	s.cache.RefreshUser(context.WithoutCancel(ctx), actor.ID)
	return fresh, nil
}

func (s *AuthService) UserBySession(ctx context.Context, hash string) (domain.User, error) {
	user, expires, state := s.cache.Get(ctx, hash)
	switch state {
	case port.SessionCacheRevoked:
		return domain.User{}, domain.Unauthorized("登录已过期，请重新登录")
	case port.SessionCacheHit:
		user.IsAdmin = s.IsAdmin(user.ID)
		return user, nil
	}
	user, expires, err := s.store.GetUserBySession(ctx, hash)
	if err != nil {
		if domain.KindOf(err) == domain.KindNotFound {
			return domain.User{}, domain.Unauthorized("登录已过期，请重新登录")
		}
		return domain.User{}, err
	}
	s.cache.Store(ctx, hash, user, expires)
	user.IsAdmin = s.IsAdmin(user.ID)
	return user, nil
}

func (s *AuthService) Logout(ctx context.Context, hash string) error {
	if hash != "" {
		if err := s.store.DeleteSession(ctx, hash); err != nil {
			return err
		}
		s.cache.Revoke(context.WithoutCancel(ctx), hash)
	}
	return nil
}

func (s *AuthService) Cleanup(ctx context.Context) error {
	return s.store.Cleanup(ctx, now(), now()-24*time.Hour.Milliseconds())
}
