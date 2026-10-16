package mcp

import (
	"context"
	"errors"
	"time"
)

var ErrInvalidAuthorization = errors.New("invalid or expired MCP authorization")
var ErrNoAppSession = errors.New("no active Bloomary app session")

type AuthorizationStory interface {
	Start(ctx context.Context, input StartInput) error
	ResumeSession(ctx context.Context, input ResumeSessionInput) (Consent, error)
	CompleteLogin(ctx context.Context, input CompleteLoginInput) (Consent, error)
	FailLogin(ctx context.Context, input FailLoginInput) (Authorization, error)
	DecideConsent(ctx context.Context, input DecideConsentInput) (AuthorizationCode, error)
	DenyConsent(ctx context.Context, input DenyConsentInput) (Authorization, error)
	ExchangeCode(ctx context.Context, input ExchangeCodeInput) (IssuedToken, error)
}

type AuthorizationStateStore interface {
	SaveAuthorization(context.Context, string, Authorization, time.Time) error
	TakeAuthorization(context.Context, string, time.Time) (Authorization, error)
	SaveConsent(context.Context, Consent) error
	TakeConsent(context.Context, string, time.Time) (Consent, error)
	SaveCode(context.Context, AuthorizationCode) error
	TakeCode(context.Context, string, time.Time) (AuthorizationCode, error)
}

// AppSessionResolver maps the current durable app-session credential to its member.
type AppSessionResolver interface {
	ResolveMember(context.Context, string) (string, error)
	EstablishMemberSession(context.Context, string, time.Time) (string, error)
}

type AccessTokenSigner interface {
	Sign(context.Context, SignInput) (string, error)
}
type AccessTokenVerifier interface {
	Verify(token, issuer, audience string, now time.Time) (string, []string, string, error)
}
type GrantStore interface {
	CreateGrant(context.Context, Grant) error
	GrantActive(context.Context, string) (bool, error)
	RevokeGrant(context.Context, string) error
}

type Authorization struct {
	ClientID, RedirectURI, State, Challenge, Resource string
	Scopes                                            []string
	CreatedAt                                         time.Time
}
type Consent struct {
	ID                          string
	Authorization               Authorization
	MemberID, SessionCredential string
	ExpiresAt                   time.Time
}
type AuthorizationCode struct {
	Value, ClientID, RedirectURI, Challenge, Resource, MemberID, State string
	Scopes                                                             []string
	ExpiresAt                                                          time.Time
}
type SignInput struct {
	Issuer, MemberID, Audience, GrantID string
	IssuedAt, ExpiresAt                 time.Time
	Scopes                              []string
}
type Grant struct {
	ID, MemberID, ClientID string
	Scopes                 []string
	CreatedAt              time.Time
}
type IssuedToken struct {
	Value     string
	ExpiresAt time.Time
	Scopes    []string
}
type StartInput struct {
	State         string
	Authorization Authorization
	ExpiresAt     time.Time
}
type CompleteLoginInput struct {
	State, MemberID, SessionCredential, ConsentID string
	Now                                           time.Time
}
type FailLoginInput struct {
	State string
	Now   time.Time
}
type DecideConsentInput struct {
	ConsentID, Code, SessionCredential string
	Approve                            bool
	Now, CodeExpiresAt                 time.Time
}
type ResumeSessionInput struct {
	Authorization                Authorization
	SessionCredential, ConsentID string
	Now                          time.Time
}
type DenyConsentInput struct {
	ConsentID string
	Now       time.Time
}
type ExchangeCodeInput struct {
	Code, ClientID, RedirectURI, Verifier, Resource string
	Now                                             time.Time
}
