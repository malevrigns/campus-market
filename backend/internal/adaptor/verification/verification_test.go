package verification

import (
	"bufio"
	"context"
	"crypto/tls"
	"crypto/x509"
	"encoding/base64"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"net/mail"
	"net/textproto"
	"net/url"
	"strconv"
	"strings"
	"testing"
	"time"

	"qingfly/edu-mall/internal/domain"
)

func deliverySMSConfig() Config {
	return Config{Mode: "live", SMSAccessKeyID: "testid", SMSAccessKeySecret: "testsecret", SMSSignName: "校园集市", SMSTemplateCode: "SMS_TEST"}
}

func TestDeliveryModesAndConfig(t *testing.T) {
	for _, mode := range []string{"", "disabled"} {
		sender, err := NewSender(Config{Mode: mode}, true)
		if err != nil {
			t.Fatal(err)
		}
		if !errors.Is(sender.Send(context.Background(), domain.VerificationMessage{}), ErrNotConfigured) {
			t.Fatal("disabled delivery must fail")
		}
	}
	if _, err := NewSender(Config{Mode: "local"}, true); err == nil {
		t.Fatal("production accepted local mode")
	}
	sender, err := NewSender(Config{Mode: "local"}, false)
	if err != nil || sender.Send(context.Background(), domain.VerificationMessage{}) != nil {
		t.Fatal("local delivery unavailable", err)
	}
	for _, cfg := range []Config{
		{Mode: "unknown"}, {Mode: "live"}, {Mode: "live", SMTPHost: "smtp.example.com"},
		{Mode: "live", SMTPHost: "smtp.example.com", SMTPFrom: "app@example.com", SMTPTLSMode: "none"},
		{Mode: "live", SMTPHost: "smtp.example.com", SMTPFrom: "app@example.com\r\nBcc: evil@example.com"},
		{Mode: "live", SMSAccessKeyID: "id"},
	} {
		if _, err := NewSender(cfg, false); err == nil {
			t.Fatalf("accepted incomplete/unsafe config: %+v", cfg)
		}
	}
	email, sms := Availability(deliverySMSConfig())
	if email || !sms {
		t.Fatalf("wrong availability %v %v", email, sms)
	}
}

func TestAliyunSignatureReferenceVector(t *testing.T) {
	// Input from the official RPC example; expected digest independently verified
	// with Python hashlib/hmac. The documentation's displayed digest does not
	// match its own input, so it is deliberately not copied here.
	// https://www.alibabacloud.com/help/en/ros/signature-method
	values, _ := url.ParseQuery("AccessKeyId=testid&Action=DescribeRegions&Format=XML&SignatureMethod=HMAC-SHA1&SignatureNonce=3ee8c1b8-83d3-44af-a94f-4e0ad82fd6cf&SignatureVersion=1.0&Timestamp=2019-08-23T12%3A46%3A24Z&Version=2019-09-10")
	if got := aliyunRPCSignature("GET", values, "testsecret"); got != "u5GLRDKD9xTcL8TpK+1XvnDlVx8=" {
		t.Fatalf("signature = %q", got)
	}
	values = url.Values{"Action": {"SendSms"}, "SignName": {"校园 集市~*"}, "TemplateParam": {`{"code":"123456"}`}, "PhoneNumbers": {"13800138000"}, "Signature": {"must-be-excluded"}}
	if got := aliyunRPCSignature("POST", values, "testsecret"); got != "R1jvTB/dSiHzAFKNNsoFjtAsvsQ=" {
		t.Fatalf("POST RFC3986 signature = %q", got)
	}
}

type deliveryRoundTripper func(*http.Request) (*http.Response, error)

func (f deliveryRoundTripper) RoundTrip(r *http.Request) (*http.Response, error) { return f(r) }

func TestAliyunDeliveryRequestAndErrors(t *testing.T) {
	for _, tc := range []struct {
		name    string
		status  int
		body    string
		network bool
		wantErr bool
	}{
		{"accepted", 200, `{"Code":"OK","BizId":"test"}`, false, false},
		{"provider rejection", 200, `{"Code":"isv.BUSINESS_LIMIT_CONTROL","Message":"private details"}`, false, true},
		{"http error", 503, `{"Code":"OK"}`, false, true},
		{"invalid json", 200, `oops`, false, true},
		{"missing code", 200, `{}`, false, true},
		{"network failure", 0, "", true, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			instance, err := NewSender(deliverySMSConfig(), true)
			if err != nil {
				t.Fatal(err)
			}
			sender := instance.(*liveSender)
			calls := 0
			sender.httpClient.Transport = deliveryRoundTripper(func(r *http.Request) (*http.Response, error) {
				calls++
				if r.Method != "POST" || r.URL.String() != "https://dysmsapi.aliyuncs.com/" {
					t.Fatalf("unsafe endpoint/method %s %s", r.Method, r.URL)
				}
				if err := r.ParseForm(); err != nil {
					t.Fatal(err)
				}
				if r.Form.Get("PhoneNumbers") != "13800138000" || r.Form.Get("TemplateParam") != `{"code":"123456"}` || r.Form.Get("Version") != "2017-05-25" || r.Form.Get("Action") != "SendSms" || r.Form.Get("RegionId") != "cn-hangzhou" {
					t.Fatalf("bad SMS request %v", r.Form)
				}
				if r.Form.Get("Signature") == "" || r.Form.Get("SignatureNonce") == "" {
					t.Fatal("unsigned request")
				}
				if tc.network {
					return nil, errors.New("testsecret must not leak")
				}
				return &http.Response{StatusCode: tc.status, Body: io.NopCloser(strings.NewReader(tc.body)), Header: make(http.Header)}, nil
			})
			err = sender.Send(context.Background(), domain.VerificationMessage{Channel: "sms", Target: "+8613800138000", Purpose: "register", Code: "123456"})
			if (err != nil) != tc.wantErr {
				t.Fatalf("error %v", err)
			}
			if err != nil && (strings.Contains(err.Error(), "testsecret") || strings.Contains(err.Error(), "private details")) {
				t.Fatal("error leaked sensitive provider details")
			}
			if calls != 1 {
				t.Fatalf("retried a non-idempotent SMS request: %d", calls)
			}
		})
	}
}

func TestDeliveryRejectsBadInputWithoutSending(t *testing.T) {
	instance, err := NewSender(deliverySMSConfig(), false)
	if err != nil {
		t.Fatal(err)
	}
	sender := instance.(*liveSender)
	sender.httpClient.Transport = deliveryRoundTripper(func(*http.Request) (*http.Response, error) { t.Fatal("unexpected network call"); return nil, nil })
	for _, message := range []domain.VerificationMessage{
		{Channel: "email", Target: "recipient@example.com", Code: "123456"},
		{Channel: "fax", Target: "13800138000", Code: "123456"},
		{Channel: "sms", Target: "13800138000,13900139000", Code: "123456"},
		{Channel: "sms", Target: "13800138000", Code: "123456\r\n"},
	} {
		if sender.Send(context.Background(), message) == nil {
			t.Fatalf("accepted %+v", message)
		}
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if !errors.Is(sender.Send(ctx, domain.VerificationMessage{Channel: "sms", Target: "13800138000", Code: "123456"}), context.Canceled) {
		t.Fatal("cancellation was not returned")
	}
}

func TestSMTPDeliveryTLSAndRejection(t *testing.T) {
	certificateServer := httptest.NewTLSServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {}))
	defer certificateServer.Close()
	roots := x509.NewCertPool()
	roots.AddCert(certificateServer.Certificate())
	for _, tc := range []struct{ name, mode, reject string }{
		{"implicit TLS", "implicit", ""}, {"STARTTLS", "starttls", ""},
		{"recipient rejected", "implicit", "RCPT"}, {"DATA rejected", "implicit", "DATA"},
		{"TLS downgrade refused", "starttls", "STARTTLS"},
		{"authentication rejected", "implicit", "AUTH"},
		{"accepted despite QUIT rejection", "implicit", "QUIT"},
		{"accepted despite disconnect after DATA", "implicit", "DISCONNECT"},
		{"untrusted certificate", "implicit", "CERT"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			listener, err := net.Listen("tcp", "127.0.0.1:0")
			if err != nil {
				t.Fatal(err)
			}
			defer listener.Close()
			done := make(chan error, 1)
			received := make(chan string, 1)
			go func() {
				conn, err := listener.Accept()
				if err != nil {
					done <- err
					return
				}
				defer conn.Close()
				_ = conn.SetDeadline(time.Now().Add(3 * time.Second))
				tlsCfg := certificateServer.TLS.Clone()
				if tc.mode == "implicit" {
					conn = tls.Server(conn, tlsCfg)
				}
				reader := bufio.NewReader(conn)
				_, _ = fmt.Fprint(conn, "220 local test SMTP\r\n")
				for {
					line, err := reader.ReadString('\n')
					if err != nil {
						done <- nil
						return
					}
					switch {
					case strings.HasPrefix(line, "EHLO"):
						if tc.mode == "starttls" && tc.reject != "STARTTLS" {
							_, _ = fmt.Fprint(conn, "250-localhost\r\n250-STARTTLS\r\n250 AUTH PLAIN\r\n")
						} else {
							_, _ = fmt.Fprint(conn, "250-localhost\r\n250 AUTH PLAIN\r\n")
						}
					case strings.HasPrefix(line, "STARTTLS"):
						_, _ = fmt.Fprint(conn, "220 begin TLS\r\n")
						conn = tls.Server(conn, tlsCfg)
						reader = bufio.NewReader(conn)
					case strings.HasPrefix(line, "AUTH"):
						if _, ok := conn.(*tls.Conn); !ok {
							done <- errors.New("credentials transmitted before TLS")
							return
						}
						if tc.reject == "AUTH" {
							_, _ = fmt.Fprint(conn, "535 rejected\r\n")
						} else {
							_, _ = fmt.Fprint(conn, "235 authenticated\r\n")
						}
					case strings.HasPrefix(line, "MAIL FROM"):
						_, _ = fmt.Fprint(conn, "250 OK\r\n")
					case line == "*\r\n":
						_, _ = fmt.Fprint(conn, "501 authentication canceled\r\n")
					case strings.HasPrefix(line, "RCPT TO"):
						if tc.reject == "RCPT" {
							_, _ = fmt.Fprint(conn, "550 recipient rejected\r\n")
						} else {
							_, _ = fmt.Fprint(conn, "250 OK\r\n")
						}
					case strings.HasPrefix(line, "DATA"):
						_, _ = fmt.Fprint(conn, "354 send data\r\n")
						raw, err := io.ReadAll(textproto.NewReader(reader).DotReader())
						if err != nil {
							done <- err
							return
						}
						received <- string(raw)
						if tc.reject == "DATA" {
							_, _ = fmt.Fprint(conn, "554 not accepted\r\n")
						} else {
							_, _ = fmt.Fprint(conn, "250 queued\r\n")
							if tc.reject == "DISCONNECT" {
								done <- nil
								return
							}
						}
					case strings.HasPrefix(line, "QUIT"):
						if tc.reject == "QUIT" {
							_, _ = fmt.Fprint(conn, "500 failure\r\n")
						} else {
							_, _ = fmt.Fprint(conn, "221 bye\r\n")
						}
						done <- nil
						return
					default:
						done <- fmt.Errorf("unexpected SMTP command %q", line)
						return
					}
				}
			}()
			_, portText, _ := net.SplitHostPort(listener.Addr().String())
			port, _ := strconv.Atoi(portText)
			instance, err := NewSender(Config{Mode: "live", SMTPHost: "127.0.0.1", SMTPPort: port, SMTPUser: "user", SMTPPassword: "password", SMTPFrom: "校园集市 <app@example.com>", SMTPTLSMode: tc.mode}, true)
			if err != nil {
				t.Fatal(err)
			}
			sender := instance.(*liveSender)
			if tc.reject != "CERT" {
				sender.smtpTLSConfig.RootCAs = roots
			}
			err = sender.Send(context.Background(), domain.VerificationMessage{Channel: "email", Target: "receiver@example.com", Code: "123456", Purpose: "reset_password"})
			accepted := tc.reject == "" || tc.reject == "QUIT" || tc.reject == "DISCONNECT"
			if (err == nil) != accepted {
				t.Fatalf("unexpected error: %v", err)
			}
			if serverErr := <-done; serverErr != nil {
				t.Fatal(serverErr)
			}
			if accepted {
				raw := <-received
				message, err := mail.ReadMessage(strings.NewReader(raw))
				if err != nil {
					t.Fatal(err)
				}
				for _, header := range []string{"From", "To", "Subject", "Date", "Message-ID", "MIME-Version", "Content-Type", "Content-Transfer-Encoding"} {
					if message.Header.Get(header) == "" {
						t.Fatalf("missing header %s", header)
					}
				}
				body, err := io.ReadAll(base64.NewDecoder(base64.StdEncoding, message.Body))
				if err != nil {
					t.Fatal(err)
				}
				if !strings.Contains(string(body), "123456") || !strings.Contains(string(body), "重置密码") {
					t.Fatalf("bad email body %q", body)
				}
			}
		})
	}
}

func TestSMTPInputAndCancellation(t *testing.T) {
	sender, err := NewSender(Config{Mode: "live", SMTPHost: "127.0.0.1", SMTPFrom: "app@example.com"}, false)
	if err != nil {
		t.Fatal(err)
	}
	for _, target := range []string{"a@example.com\r\nBcc: b@example.com", "a@example.com,b@example.com", "not-an-email"} {
		if err := sender.Send(context.Background(), domain.VerificationMessage{Channel: "email", Target: target, Code: "123456"}); err == nil || !strings.Contains(err.Error(), "address") {
			t.Fatalf("bad recipient not rejected before dial: %v", err)
		}
	}
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer listener.Close()
	accepted := make(chan struct{})
	go func() {
		conn, err := listener.Accept()
		if err != nil {
			return
		}
		defer conn.Close()
		close(accepted)
		_, _ = io.Copy(io.Discard, conn)
	}()
	_, portText, _ := net.SplitHostPort(listener.Addr().String())
	port, _ := strconv.Atoi(portText)
	sender, err = NewSender(Config{Mode: "live", SMTPHost: "127.0.0.1", SMTPPort: port, SMTPFrom: "app@example.com"}, false)
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go func() { <-accepted; cancel() }()
	start := time.Now()
	if err := sender.Send(ctx, domain.VerificationMessage{Channel: "email", Target: "a@example.com", Code: "123456"}); !errors.Is(err, context.Canceled) {
		t.Fatalf("cancellation error = %v", err)
	}
	if time.Since(start) > time.Second {
		t.Fatal("SMTP cancellation did not interrupt blocked greeting")
	}
}
