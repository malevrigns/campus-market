package domain

// User 是对外返回的账号视图，不包含密码摘要。
type User struct {
	IsAdmin       bool   `json:"is_admin"`
	ID            string `json:"id"`
	Name          string `json:"name"`
	Email         string `json:"email"`
	Phone         string `json:"phone"`
	EmailVerified bool   `json:"email_verified"`
	PhoneVerified bool   `json:"phone_verified"`
}

// CodeProof 是验证码证明：渠道、目标、挑战 ID 与 6 位验证码。
type CodeProof struct {
	Channel     string `json:"channel"`
	Target      string `json:"target"`
	ChallengeID string `json:"challenge_id"`
	Code        string `json:"code"`
}

// AuthCode 是验证码记录，CodeHash 为 HMAC 摘要，不保存明文。
type AuthCode struct {
	ID         string
	Channel    string
	Target     string
	Purpose    string
	SubjectID  string
	CodeHash   string
	CreatedAt  int64
	ExpiresAt  int64
	ConsumedAt int64
	Attempts   int
	State      string
}

// Session 是登录成功后由应用层写入 Cookie 的会话材料。
type Session struct {
	Token     string
	ExpiresAt int64
}

// CodeChallenge 是请求验证码后的响应。
type CodeChallenge struct {
	ChallengeID   string `json:"challenge_id"`
	ExpiresIn     int    `json:"expires_in"`
	RetryAfter    int    `json:"retry_after"`
	Target        string `json:"target"`
	Delivery      string `json:"delivery"`
	DeliveryToken string `json:"delivery_token,omitempty"`
}

// CodePreview 是本地测试收件箱返回的验证码。
type CodePreview struct {
	Code      string `json:"code"`
	Target    string `json:"target"`
	ExpiresAt int64  `json:"expires_at"`
	Notice    string `json:"notice"`
}

// RegisterResult 是注册成功后的会话与账号。
type RegisterResult struct {
	User    User
	Session Session
}

// ResetPasswordResult 是重置密码后的会话吊销结果。
type ResetPasswordResult struct {
	Reset     bool
	LoggedOut bool
	Revoked   []string // 需要在缓存中写墓碑的会话摘要
	Current   bool
}

// Identity 是账号绑定的一种联系方式。
type Identity struct {
	UserID     string
	Channel    string
	Target     string
	VerifiedAt int64
}

// VerificationMessage 是发送验证码的业务输入，凭证只在适配器内部使用。
type VerificationMessage struct {
	Channel string
	Target  string
	Purpose string
	Code    string
}
