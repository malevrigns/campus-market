// Package verification 是验证码外部投递适配器：SMTP 邮件与阿里云短信。
// 验证码明文、模板参数与密钥只在本包内使用，错误信息不包含供应商敏感细节。
package verification

import (
	"context"
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha1"
	"crypto/tls"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"mime"
	"net"
	"net/http"
	"net/mail"
	"net/smtp"
	"net/url"
	"regexp"
	"strconv"
	"strings"
	"time"

	"qingfly/edu-mall/internal/domain"
	"qingfly/edu-mall/internal/port"
)

type Config struct {
	Mode               string
	SMTPHost           string
	SMTPPort           int
	SMTPUser           string
	SMTPPassword       string
	SMTPFrom           string
	SMTPTLSMode        string
	SMSAccessKeyID     string
	SMSAccessKeySecret string
	SMSSignName        string
	SMSTemplateCode    string
	SMSRegion          string
}

var ErrNotConfigured = errors.New("verification delivery is not configured")

type inactiveSender struct{ local bool }

func (s inactiveSender) Send(ctx context.Context, _ domain.VerificationMessage) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	if s.local {
		return nil
	}
	return ErrNotConfigured
}

type liveSender struct {
	cfg           Config
	email, sms    bool
	smtpTLSConfig *tls.Config
	httpClient    *http.Client
}

// Availability 只表示“配置是否齐全”，不代表供应商已接受凭证或消息可达。
func Availability(cfg Config) (email, sms bool) {
	switch strings.ToLower(strings.TrimSpace(cfg.Mode)) {
	case "local":
		return true, true
	case "live":
		email = cfg.SMTPHost != "" && cfg.SMTPFrom != "" && (cfg.SMTPUser == "") == (cfg.SMTPPassword == "")
		sms = cfg.SMSAccessKeyID != "" && cfg.SMSAccessKeySecret != "" && cfg.SMSSignName != "" && cfg.SMSTemplateCode != ""
	}
	return
}

func NewSender(cfg Config, production bool) (port.VerificationSender, error) {
	cfg.Mode = strings.ToLower(strings.TrimSpace(cfg.Mode))
	switch cfg.Mode {
	case "", "disabled":
		return inactiveSender{}, nil
	case "local":
		if production {
			return nil, errors.New("local verification delivery is forbidden in production")
		}
		return inactiveSender{local: true}, nil
	case "live":
	default:
		return nil, errors.New("verification delivery mode must be disabled, local, or live")
	}
	email, sms := Availability(cfg)
	smtpPresent := cfg.SMTPHost != "" || cfg.SMTPFrom != "" || cfg.SMTPUser != "" || cfg.SMTPPassword != ""
	smsPresent := cfg.SMSAccessKeyID != "" || cfg.SMSAccessKeySecret != "" || cfg.SMSSignName != "" || cfg.SMSTemplateCode != ""
	if smtpPresent && !email {
		return nil, errors.New("SMTP configuration is incomplete")
	}
	if smsPresent && !sms {
		return nil, errors.New("Aliyun SMS configuration is incomplete")
	}
	if !email && !sms {
		return nil, ErrNotConfigured
	}
	if email {
		if strings.ContainsAny(cfg.SMTPHost, "/\\\r\n\t ") {
			return nil, errors.New("invalid SMTP host")
		}
		if _, err := deliveryMailAddress(cfg.SMTPFrom); err != nil {
			return nil, errors.New("invalid SMTP from address")
		}
		cfg.SMTPTLSMode = strings.ToLower(strings.TrimSpace(cfg.SMTPTLSMode))
		switch cfg.SMTPTLSMode {
		case "":
			cfg.SMTPTLSMode = "starttls"
		case "starttls", "implicit":
		case "tls":
			cfg.SMTPTLSMode = "implicit"
		default:
			return nil, errors.New("SMTP requires starttls or implicit TLS")
		}
		if cfg.SMTPPort == 0 {
			cfg.SMTPPort = 587
			if cfg.SMTPTLSMode == "implicit" {
				cfg.SMTPPort = 465
			}
		}
		if cfg.SMTPPort < 1 || cfg.SMTPPort > 65535 {
			return nil, errors.New("invalid SMTP port")
		}
	}
	if cfg.SMSRegion == "" {
		cfg.SMSRegion = "cn-hangzhou"
	}
	return &liveSender{cfg: cfg, email: email, sms: sms,
		smtpTLSConfig: &tls.Config{ServerName: cfg.SMTPHost, MinVersion: tls.VersionTLS12},
		httpClient:    &http.Client{Timeout: 15 * time.Second, CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }},
	}, nil
}

var deliveryCodePattern = regexp.MustCompile(`^[0-9]{6}$`)
var deliveryPhonePattern = regexp.MustCompile(`^1[3-9][0-9]{9}$`)

func (s *liveSender) Send(ctx context.Context, message domain.VerificationMessage) (err error) {
	if err = ctx.Err(); err != nil {
		return err
	}
	if !deliveryCodePattern.MatchString(message.Code) {
		return errors.New("verification code must be six digits")
	}
	ctx, cancel := context.WithTimeout(ctx, 15*time.Second)
	defer cancel()
	defer func() {
		if err != nil && ctx.Err() != nil {
			err = ctx.Err()
		}
	}()
	switch message.Channel {
	case "email":
		if !s.email {
			return ErrNotConfigured
		}
		return s.sendEmail(ctx, message)
	case "sms":
		if !s.sms {
			return ErrNotConfigured
		}
		return s.sendSMS(ctx, message)
	default:
		return errors.New("unsupported verification delivery channel")
	}
}

func deliveryMailAddress(raw string) (*mail.Address, error) {
	if strings.ContainsAny(raw, "\r\n\x00") {
		return nil, errors.New("invalid email address")
	}
	address, err := mail.ParseAddress(raw)
	if err != nil || !strings.Contains(address.Address, "@") || strings.ContainsAny(address.Address, "\r\n\x00") {
		return nil, errors.New("invalid email address")
	}
	return address, nil
}

func (s *liveSender) sendEmail(ctx context.Context, message domain.VerificationMessage) error {
	to, err := deliveryMailAddress(message.Target)
	if err != nil {
		return err
	}
	from, err := deliveryMailAddress(s.cfg.SMTPFrom)
	if err != nil {
		return err
	}
	body, err := verificationEmail(from, to, message)
	if err != nil {
		return err
	}
	address := net.JoinHostPort(s.cfg.SMTPHost, strconv.Itoa(s.cfg.SMTPPort))
	conn, err := (&net.Dialer{}).DialContext(ctx, "tcp", address)
	if err != nil {
		return errors.New("SMTP connection failed")
	}
	defer conn.Close()
	rawConn := conn
	stop := context.AfterFunc(ctx, func() { _ = rawConn.Close() })
	defer stop()
	if deadline, ok := ctx.Deadline(); ok {
		if err := conn.SetDeadline(deadline); err != nil {
			return errors.New("SMTP deadline setup failed")
		}
	}
	if s.cfg.SMTPTLSMode == "implicit" {
		tlsConn := tls.Client(conn, s.smtpTLSConfig.Clone())
		if err := tlsConn.HandshakeContext(ctx); err != nil {
			return errors.New("SMTP TLS handshake failed")
		}
		conn = tlsConn
	}
	client, err := smtp.NewClient(conn, s.cfg.SMTPHost)
	if err != nil {
		return errors.New("SMTP greeting failed")
	}
	defer client.Close()
	if s.cfg.SMTPTLSMode == "starttls" {
		if ok, _ := client.Extension("STARTTLS"); !ok {
			return errors.New("SMTP server does not support required STARTTLS")
		}
		if err := client.StartTLS(s.smtpTLSConfig.Clone()); err != nil {
			return errors.New("SMTP STARTTLS failed")
		}
	}
	if state, ok := client.TLSConnectionState(); !ok || !state.HandshakeComplete {
		return errors.New("SMTP TLS is required")
	}
	if s.cfg.SMTPUser != "" {
		if err := client.Auth(smtp.PlainAuth("", s.cfg.SMTPUser, s.cfg.SMTPPassword, s.cfg.SMTPHost)); err != nil {
			return errors.New("SMTP authentication failed")
		}
	}
	if err := client.Mail(from.Address); err != nil {
		return errors.New("SMTP sender rejected")
	}
	if err := client.Rcpt(to.Address); err != nil {
		return errors.New("SMTP recipient rejected")
	}
	writer, err := client.Data()
	if err != nil {
		return errors.New("SMTP DATA rejected")
	}
	if _, err := writer.Write(body); err != nil {
		return errors.New("SMTP message write failed")
	}
	if err := writer.Close(); err != nil {
		return errors.New("SMTP message was not accepted")
	}
	_ = client.Quit()
	return nil
}

func verificationEmail(from, to *mail.Address, message domain.VerificationMessage) ([]byte, error) {
	purpose := map[string]string{"register": "注册账号", "login": "登录账号", "reset_password": "重置密码", "bind": "绑定联系方式"}[message.Purpose]
	if purpose == "" {
		purpose = "身份验证"
	}
	nonce := make([]byte, 16)
	if _, err := rand.Read(nonce); err != nil {
		return nil, errors.New("email message ID generation failed")
	}
	content := fmt.Sprintf("您正在进行%s，验证码为：%s。验证码 5 分钟内有效，请勿向他人透露。\r\n如非本人操作，请忽略此邮件。\r\n", purpose, message.Code)
	encoded := base64.StdEncoding.EncodeToString([]byte(content))
	var body strings.Builder
	for len(encoded) > 76 {
		body.WriteString(encoded[:76])
		body.WriteString("\r\n")
		encoded = encoded[76:]
	}
	body.WriteString(encoded)
	body.WriteString("\r\n")
	headers := []string{
		"From: " + from.String(), "To: " + to.String(),
		"Subject: " + mime.QEncoding.Encode("UTF-8", "校园集市验证码"),
		"Date: " + time.Now().Format(time.RFC1123Z),
		"Message-ID: <" + hex.EncodeToString(nonce) + "@campus-market.local>",
		"MIME-Version: 1.0", "Content-Type: text/plain; charset=UTF-8", "Content-Transfer-Encoding: base64",
	}
	return []byte(strings.Join(headers, "\r\n") + "\r\n\r\n" + body.String()), nil
}

func aliyunRPCSignature(method string, values url.Values, secret string) string {
	canonical := make(url.Values, len(values))
	for key, items := range values {
		if key != "Signature" {
			canonical[key] = items
		}
	}
	escaped := strings.ReplaceAll(canonical.Encode(), "+", "%20")
	stringToSign := method + "&%2F&" + strings.ReplaceAll(url.QueryEscape(escaped), "+", "%20")
	mac := hmac.New(sha1.New, []byte(secret+"&"))
	_, _ = mac.Write([]byte(stringToSign))
	return base64.StdEncoding.EncodeToString(mac.Sum(nil))
}

func (s *liveSender) sendSMS(ctx context.Context, message domain.VerificationMessage) error {
	phone := strings.TrimPrefix(message.Target, "+86")
	if !deliveryPhonePattern.MatchString(phone) {
		return errors.New("invalid domestic SMS phone number")
	}
	nonce := make([]byte, 16)
	if _, err := rand.Read(nonce); err != nil {
		return errors.New("SMS nonce generation failed")
	}
	paramsJSON, err := json.Marshal(map[string]string{"code": message.Code})
	if err != nil {
		return errors.New("SMS template encoding failed")
	}
	values := url.Values{
		"Action": {"SendSms"}, "Version": {"2017-05-25"}, "Format": {"JSON"},
		"AccessKeyId": {s.cfg.SMSAccessKeyID}, "RegionId": {s.cfg.SMSRegion},
		"SignatureMethod": {"HMAC-SHA1"}, "SignatureVersion": {"1.0"},
		"Timestamp": {time.Now().UTC().Format("2006-01-02T15:04:05Z")}, "SignatureNonce": {hex.EncodeToString(nonce)},
		"PhoneNumbers": {phone}, "SignName": {s.cfg.SMSSignName}, "TemplateCode": {s.cfg.SMSTemplateCode}, "TemplateParam": {string(paramsJSON)},
	}
	values.Set("Signature", aliyunRPCSignature("POST", values, s.cfg.SMSAccessKeySecret))
	request, err := http.NewRequestWithContext(ctx, http.MethodPost, "https://dysmsapi.aliyuncs.com/", strings.NewReader(values.Encode()))
	if err != nil {
		return errors.New("SMS request creation failed")
	}
	request.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	response, err := s.httpClient.Do(request)
	if err != nil {
		return errors.New("SMS provider connection failed")
	}
	defer response.Body.Close()
	if response.StatusCode < 200 || response.StatusCode >= 300 {
		return fmt.Errorf("SMS provider returned HTTP %d", response.StatusCode)
	}
	data, err := io.ReadAll(io.LimitReader(response.Body, 64*1024+1))
	if err != nil {
		return errors.New("SMS provider response read failed")
	}
	if len(data) > 64*1024 {
		return errors.New("SMS provider response too large")
	}
	var result struct {
		Code string `json:"Code"`
	}
	if err := json.Unmarshal(data, &result); err != nil {
		return errors.New("SMS provider returned invalid JSON")
	}
	if result.Code != "OK" {
		return errors.New("SMS provider did not accept the verification message")
	}
	return nil
}
