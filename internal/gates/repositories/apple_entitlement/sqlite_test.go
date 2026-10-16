package apple_entitlement

import (
	"context"
	"database/sql"
	"testing"
	"time"

	"github.com/stretchr/testify/suite"
	_ "modernc.org/sqlite"

	appleiapDomain "bloomary-api/internal/domain/appleiap"
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

	s.Repo, err = NewSQLiteRepository(s.DB)
	s.Require().NoError(err)
	s.Require().NoError(s.Repo.Init(context.Background()))
}

func (s *RepositorySuite) TearDownSuite() {
	s.Require().NoError(s.DB.Close())
}

func (s *RepositorySuite) SetupTest() {
	_, err := s.DB.ExecContext(context.Background(), "DELETE FROM apple_entitlements")
	s.Require().NoError(err)
}

func (s *RepositorySuite) TestApplyCreatesActiveEntitlement() {
	ctx := context.Background()
	now := time.Now().UTC()

	applied, err := s.Repo.Apply(ctx, update("member-1", "original-1", "transaction-1", appleiapDomain.StatusActive, now.Add(30*24*time.Hour), now))
	s.Require().NoError(err)
	s.True(applied)

	entitlement, err := s.Repo.ByMemberID(ctx, "member-1", now)
	s.Require().NoError(err)
	s.True(hasAppleAccess(entitlement, now))
}

func (s *RepositorySuite) TestApplyIgnoresOlderEvent() {
	ctx := context.Background()
	now := time.Now().UTC()

	_, err := s.Repo.Apply(ctx, update("member-1", "original-1", "transaction-new", appleiapDomain.StatusActive, now.Add(30*24*time.Hour), now))
	s.Require().NoError(err)

	applied, err := s.Repo.Apply(ctx, update("member-1", "original-1", "transaction-old", appleiapDomain.StatusExpired, now.Add(-time.Hour), now.Add(-time.Hour)))
	s.Require().NoError(err)
	s.False(applied)

	entitlement, err := s.Repo.ByMemberID(ctx, "member-1", now)
	s.Require().NoError(err)
	s.True(hasAppleAccess(entitlement, now))
}

func (s *RepositorySuite) TestRefundRevokesAccess() {
	ctx := context.Background()
	now := time.Now().UTC()

	_, err := s.Repo.Apply(ctx, update("member-1", "original-1", "transaction-1", appleiapDomain.StatusActive, now.Add(30*24*time.Hour), now))
	s.Require().NoError(err)

	applied, err := s.Repo.Apply(ctx, update("member-1", "original-1", "transaction-2", appleiapDomain.StatusRefunded, now.Add(30*24*time.Hour), now.Add(time.Hour)))
	s.Require().NoError(err)
	s.True(applied)

	entitlement, err := s.Repo.ByMemberID(ctx, "member-1", now.Add(time.Hour))
	s.Require().NoError(err)
	s.False(hasAppleAccess(entitlement, now.Add(time.Hour)))
}

func (s *RepositorySuite) TestExpiredEntitlementHasNoAccess() {
	ctx := context.Background()
	now := time.Now().UTC()

	_, err := s.Repo.Apply(ctx, update("member-1", "original-1", "transaction-1", appleiapDomain.StatusActive, now.Add(-time.Hour), now))
	s.Require().NoError(err)

	entitlement, err := s.Repo.ByMemberID(ctx, "member-1", now)
	s.Require().NoError(err)
	s.False(hasAppleAccess(entitlement, now))
}

func (s *RepositorySuite) TestLatestEntitlementReturnsRawRecordWithoutAccessCheck() {
	ctx := context.Background()
	now := time.Now().UTC()

	_, err := s.Repo.Apply(ctx, update("member-1", "original-1", "transaction-1", appleiapDomain.StatusActive, now.Add(-time.Hour), now))
	s.Require().NoError(err)

	entitlement, found, err := s.Repo.LatestEntitlement(ctx, "member-1")
	s.Require().NoError(err)
	s.True(found)
	s.False(hasAppleAccess(entitlement, now))
	s.Equal(appleiapDomain.StatusActive, entitlement.Status)
}

func (s *RepositorySuite) TestLatestEntitlementReturnsFalseWhenNoRecord() {
	ctx := context.Background()
	now := time.Now().UTC()

	entitlement, found, err := s.Repo.LatestEntitlement(ctx, "member-1")
	s.Require().NoError(err)
	s.False(found)
	s.Zero(entitlement)
	s.False(hasAppleAccess(entitlement, now))
}

func update(
	memberID, originalTransactionID, transactionID string,
	status appleiapDomain.Status,
	accessUntil, eventOccurredAt time.Time,
) appleiapDomain.EntitlementUpdate {
	return appleiapDomain.EntitlementUpdate{
		MemberID:              memberID,
		AccountID:             "account-1",
		AppAccountToken:       "app-account-token-1",
		OriginalTransactionID: originalTransactionID,
		TransactionID:         transactionID,
		ProductID:             "bloomary.pro.monthly",
		AccessUntil:           accessUntil,
		Status:                status,
		Environment:           "Sandbox",
		EventOccurredAt:       eventOccurredAt,
	}
}
