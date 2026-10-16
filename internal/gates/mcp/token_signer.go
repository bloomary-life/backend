package mcp

import (
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"errors"
	"strings"
	"time"

	mcpDomain "bloomary-api/internal/domain/mcp"
)

type SigningKey struct {
	ID, Secret  string
	VerifyUntil time.Time
}
type TokenSigner struct {
	current  SigningKey
	previous *SigningKey
}

const RotationOverlap = 30 * 24 * time.Hour

func NewTokenSigner(current SigningKey, previous *SigningKey) (*TokenSigner, error) {
	if current.ID == "" || current.Secret == "" {
		return nil, errors.New("MCP token signer: current key ID and secret are required")
	}
	if previous != nil && (previous.ID == "" || previous.Secret == "" || previous.ID == current.ID || previous.VerifyUntil.IsZero() || !previous.VerifyUntil.After(time.Now()) || previous.VerifyUntil.After(time.Now().Add(RotationOverlap))) {
		return nil, errors.New("MCP token signer: previous key must have a distinct ID, secret, and expiry within the 30-day overlap")
	}
	return &TokenSigner{current: current, previous: previous}, nil
}

type claims struct {
	Issuer   string   `json:"iss"`
	Subject  string   `json:"sub"`
	GrantID  string   `json:"gid"`
	Audience string   `json:"aud"`
	IssuedAt int64    `json:"iat"`
	Expires  int64    `json:"exp"`
	Scope    []string `json:"scope"`
}

func (s *TokenSigner) Sign(_ context.Context, in mcpDomain.SignInput) (string, error) {
	if in.MemberID == "" || in.GrantID == "" || in.Issuer == "" || in.Audience == "" || !in.ExpiresAt.After(in.IssuedAt) {
		return "", errors.New("MCP token signer: invalid claims")
	}
	header, _ := json.Marshal(map[string]string{"alg": "HS256", "typ": "JWT", "kid": s.current.ID})
	payload, err := json.Marshal(claims{Issuer: in.Issuer, Subject: in.MemberID, GrantID: in.GrantID, Audience: in.Audience, IssuedAt: in.IssuedAt.Unix(), Expires: in.ExpiresAt.Unix(), Scope: in.Scopes})
	if err != nil {
		return "", err
	}
	unsigned := base64.RawURLEncoding.EncodeToString(header) + "." + base64.RawURLEncoding.EncodeToString(payload)
	mac := hmac.New(sha256.New, []byte(s.current.Secret))
	_, _ = mac.Write([]byte(unsigned))
	return unsigned + "." + base64.RawURLEncoding.EncodeToString(mac.Sum(nil)), nil
}

func (s *TokenSigner) Verify(token, issuer, audience string, now time.Time) (string, []string, string, error) {
	parts := strings.Split(token, ".")
	if len(parts) != 3 {
		return "", nil, "", errors.New("invalid MCP token")
	}
	var header struct {
		Algorithm string `json:"alg"`
		Type      string `json:"typ"`
		Kid       string `json:"kid"`
	}
	decoded, err := base64.RawURLEncoding.DecodeString(parts[0])
	if err != nil || json.Unmarshal(decoded, &header) != nil || header.Algorithm != "HS256" || header.Type != "JWT" {
		return "", nil, "", errors.New("invalid MCP token header")
	}
	key := s.key(header.Kid, now)
	if key == nil {
		return "", nil, "", errors.New("unknown MCP token key")
	}
	mac := hmac.New(sha256.New, []byte(key.Secret))
	_, _ = mac.Write([]byte(parts[0] + "." + parts[1]))
	sig, err := base64.RawURLEncoding.DecodeString(parts[2])
	if err != nil || !hmac.Equal(sig, mac.Sum(nil)) {
		return "", nil, "", errors.New("invalid MCP token signature")
	}
	payload, err := base64.RawURLEncoding.DecodeString(parts[1])
	var c claims
	if err != nil || json.Unmarshal(payload, &c) != nil {
		return "", nil, "", errors.New("invalid MCP token claims")
	}
	if c.Issuer != issuer || c.Subject == "" || c.GrantID == "" || c.Audience != audience || c.Expires <= now.Unix() || c.IssuedAt > now.Unix() {
		return "", nil, "", errors.New("expired or invalid MCP token claims")
	}
	if !hasScope(c.Scope, "drafts:read") && !hasScope(c.Scope, "drafts:write") {
		return "", nil, "", errors.New("MCP token scope is insufficient")
	}
	return c.Subject, c.Scope, c.GrantID, nil
}
func (s *TokenSigner) key(id string, now time.Time) *SigningKey {
	if id == s.current.ID {
		return &s.current
	}
	if s.previous != nil && id == s.previous.ID && (s.previous.VerifyUntil.IsZero() || now.Before(s.previous.VerifyUntil)) {
		return s.previous
	}
	return nil
}
func hasScope(scopes []string, value string) bool {
	for _, scope := range scopes {
		if scope == value {
			return true
		}
	}
	return false
}

var _ mcpDomain.AccessTokenSigner = (*TokenSigner)(nil)
var _ mcpDomain.AccessTokenVerifier = (*TokenSigner)(nil)
