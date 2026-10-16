package account

import (
	"context"
	"errors"
)

// ErrNotFound is returned when an account cannot be found by the given criteria.
var ErrNotFound = errors.New("account not found")

type OAuthStateStore interface {
	Start(client string) (string, error)
	Get(state string) *OAuthState
	Finish(state string, result []byte)
	ConsumeDone(state string) []byte
}

type Repository interface {
	Insert(ctx context.Context, token Token, profile Profile) error
	ByMemberID(ctx context.Context, memberID string) (Profile, error)
	UniqueMemberIDByEmail(ctx context.Context, email string) (EmailFallbackMatch, error)
	Delete(ctx context.Context, memberID string) error
	AllActive(ctx context.Context) ([]TokenPair, error)
	ActiveByMemberID(ctx context.Context, memberID string) (TokenPair, error)
}
