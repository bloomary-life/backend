package account

import (
	"context"
	"time"
)

type LinkedInAuthStory interface {
	CompleteAuth(ctx context.Context, input CompleteAuthInput) (CompleteAuthOutput, error)
	StartCLIAuth(ctx context.Context, input StartCLIAuthInput) (StartCLIAuthOutput, error)
	GetCLIResult(ctx context.Context, input GetCLIResultInput) (GetCLIResultOutput, error)
}

type AccountDeletionStory interface {
	DisconnectLinkedIn(ctx context.Context, input DisconnectLinkedInInput) error
	DeleteAccount(ctx context.Context, input DeleteAccountInput) error
}

type CompleteAuthInput struct {
	Code         string
	RedirectURI  string
	CodeVerifier string
	StartedAt    time.Time
}

type CompleteAuthOutput struct {
	AccessToken string
	TokenType   string
	Scope       string
	ExpiresIn   int64
	StatusCode  int
	Profile     Profile
	ConnectedAt time.Time
}

type StartCLIAuthInput struct {
	ClientID    string
	RedirectURI string
	Scope       string
	Fresh       bool
	// Client is echoed onto the stored OAuth state; see OAuthState.Client.
	Client string
}

type StartCLIAuthOutput struct {
	AuthURL string
	State   string
	PollURL string
}

type GetCLIResultInput struct {
	State       string
	WaitSeconds int
}

type GetCLIResultOutput struct {
	Pending   bool
	RawResult []byte
}

type DisconnectLinkedInInput struct {
	MemberID    string
	AccessToken string
}

type DeleteAccountInput struct {
	MemberID string
}
