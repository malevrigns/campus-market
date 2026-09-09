// Package auth implements the unified identity contract shared by all subsystems.
// Principle (Part 0 of the courseware): only the mall backend issues & strongly
// verifies tokens; IM / Agent / WebSocket never mint their own — they propagate
// the principal via gRPC metadata and verify against backend. Here we provide a
// self-contained HS256 JWT implementation (stdlib only) plus scope checks.
package auth

import (
	"crypto/hmac"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"errors"
	"strings"
	"time"
)

// Principal is the authoritative "who, in what context, with what capabilities".
type Principal struct {
	UserID   string   `json:"sub"`
	TenantID string   `json:"tid,omitempty"`
	Roles    []string `json:"roles,omitempty"`
	Scopes   []string `json:"scp"`
	Via      string   `json:"via,omitempty"` // e.g. web / app / bot:agent-7
	Kid      string   `json:"kid,omitempty"` // key id for JWKS rotation
	Iss      string   `json:"iss,omitempty"`
	Aud      string   `json:"aud,omitempty"` // mall | im | agent | websocket
	Iat      int64    `json:"iat"`
	Exp      int64    `json:"exp"`
}

// Errs returned by verification.
var (
	ErrMalformed = errors.New("auth: malformed token")
	ErrBadSig    = errors.New("auth: bad signature")
	ErrExpired   = errors.New("auth: token expired")
	ErrAudience  = errors.New("auth: audience mismatch")
)

func b64(b []byte) string        { return base64.RawURLEncoding.EncodeToString(b) }
func unb64(s string) ([]byte, error) { return base64.RawURLEncoding.DecodeString(s) }

// Sign produces a compact HS256 JWT for p using secret.
func Sign(p Principal, secret []byte) (string, error) {
	if p.Iat == 0 {
		p.Iat = time.Now().Unix()
	}
	if p.Exp == 0 {
		p.Exp = p.Iat + 3600
	}
	header := map[string]string{"alg": "HS256", "typ": "JWT"}
	if p.Kid != "" {
		header["kid"] = p.Kid
	}
	hb, _ := json.Marshal(header)
	pb, err := json.Marshal(p)
	if err != nil {
		return "", err
	}
	signing := b64(hb) + "." + b64(pb)
	return signing + "." + hmacSHA(signing, secret), nil
}

// Verify validates signature, expiry and (optionally) audience, returning claims.
func Verify(token string, secret []byte, wantAud string) (*Principal, error) {
	parts := strings.Split(token, ".")
	if len(parts) != 3 {
		return nil, ErrMalformed
	}
	signing := parts[0] + "." + parts[1]
	want := hmacSHA(signing, secret)
	if !hmac.Equal([]byte(want), []byte(parts[2])) {
		return nil, ErrBadSig
	}
	payload, err := unb64(parts[1])
	if err != nil {
		return nil, ErrMalformed
	}
	var p Principal
	if err := json.Unmarshal(payload, &p); err != nil {
		return nil, ErrMalformed
	}
	now := time.Now().Unix()
	if p.Exp != 0 && now > p.Exp {
		return nil, ErrExpired
	}
	if wantAud != "" && p.Aud != "" && p.Aud != wantAud {
		return nil, ErrAudience
	}
	return &p, nil
}

// HasScope reports whether p carries the required scope (default-deny).
func HasScope(p *Principal, scope string) bool {
	for _, s := range p.Scopes {
		if s == scope {
			return true
		}
	}
	return false
}

func hmacSHA(s string, secret []byte) string {
	mac := hmac.New(sha256.New, secret)
	mac.Write([]byte(s))
	return b64(mac.Sum(nil))
}
