package account

import "time"

type Token struct {
	AccessToken string
	Scope       string
	ExpiresIn   int64
}

type TokenPair struct {
	AccessToken string
	MemberID    string
	ExpiresAt   time.Time
}

type Profile struct {
	MemberID   string
	Name       string
	PictureURL string
	Email      string
}

type EmailFallbackMatch struct {
	MemberID   string
	MatchCount int
}

type OAuthState struct {
	CreatedAt time.Time
	Done      <-chan struct{}
	// Client names the surface that started the flow, so the shared OAuth
	// callback can redirect back to it. Empty means a native app.
	Client string
}
