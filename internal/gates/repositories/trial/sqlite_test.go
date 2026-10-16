package trial

import (
	"context"
	"database/sql"
	"testing"
	"time"

	"github.com/stretchr/testify/suite"
	_ "modernc.org/sqlite"
)

type RepositorySuite struct {
	suite.Suite
	DB   *sql.DB
	Repo *SQLiteRepository
}

func TestRepositorySuite(t *testing.T) {
	suite.Run(t, new(RepositorySuite))
}

func (s *RepositorySuite) SetupSuite() {
	var err error
	s.DB, err = sql.Open("sqlite", ":memory:")
	s.Require().NoError(err)

	s.Repo, err = NewSQLiteRepository(s.DB, 14, "test-hmac-secret")
	s.Require().NoError(err)
	err = s.Repo.Init(context.Background())
	s.Require().NoError(err)
}

func (s *RepositorySuite) TearDownSuite() {
	s.DB.Close()
}

func (s *RepositorySuite) SetupTest() {
	_, err := s.DB.ExecContext(context.Background(), "DELETE FROM trial_history")
	s.Require().NoError(err)
}

func (s *RepositorySuite) TestEnsureCreatesNewTrial() {
	ctx := context.Background()
	now := time.Now()

	record, err := s.Repo.Ensure(ctx, "test@example.com", "member-1", now)
	s.Require().NoError(err)
	s.NotEmpty(record.PseudonymousIdentifier)
	s.False(record.TrialUsedAt.IsZero())
	s.False(record.ExpiresAt.IsZero())
	s.True(record.ExpiresAt.After(now))
}

func (s *RepositorySuite) TestEnsureIdempotent() {
	ctx := context.Background()
	now := time.Now()

	record1, err := s.Repo.Ensure(ctx, "test@example.com", "member-1", now)
	s.Require().NoError(err)

	record2, err := s.Repo.Ensure(ctx, "test@example.com", "member-1", now.Add(1*time.Hour))
	s.Require().NoError(err)

	s.Equal(record1.PseudonymousIdentifier, record2.PseudonymousIdentifier)
	s.Equal(record1.TrialUsedAt, record2.TrialUsedAt)
}

func (s *RepositorySuite) TestEnsureDifferentMembers() {
	ctx := context.Background()
	now := time.Now()

	record1, err := s.Repo.Ensure(ctx, "user1@example.com", "member-1", now)
	s.Require().NoError(err)

	record2, err := s.Repo.Ensure(ctx, "user2@example.com", "member-2", now)
	s.Require().NoError(err)

	s.NotEqual(record1.PseudonymousIdentifier, record2.PseudonymousIdentifier)
}

func (s *RepositorySuite) TestEnsureUsesEmailForIdentifier() {
	ctx := context.Background()
	now := time.Now()

	record1, err := s.Repo.Ensure(ctx, "test@example.com", "member-1", now)
	s.Require().NoError(err)

	record2, err := s.Repo.Ensure(ctx, "test@example.com", "member-2", now)
	s.Require().NoError(err)

	s.Equal(record1.PseudonymousIdentifier, record2.PseudonymousIdentifier)
}

func (s *RepositorySuite) TestEnsureUsesMemberIDWhenNoEmail() {
	ctx := context.Background()
	now := time.Now()

	record1, err := s.Repo.Ensure(ctx, "", "member-1", now)
	s.Require().NoError(err)

	record2, err := s.Repo.Ensure(ctx, "", "member-1", now)
	s.Require().NoError(err)

	s.Equal(record1.PseudonymousIdentifier, record2.PseudonymousIdentifier)
}

func (s *RepositorySuite) TestTrialExpiration() {
	ctx := context.Background()
	now := time.Now()

	record, err := s.Repo.Ensure(ctx, "test@example.com", "member-1", now)
	s.Require().NoError(err)

	expectedExpiry := now.Add(14 * 24 * time.Hour)
	s.InDelta(expectedExpiry.Unix(), record.ExpiresAt.Unix(), 1.0)
}
