package account

import (
	"context"
	"database/sql"
	"testing"

	"github.com/stretchr/testify/suite"
	_ "modernc.org/sqlite"

	accountDomain "bloomary-api/internal/domain/account"
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
	// Create in-memory SQLite database
	var err error
	s.DB, err = sql.Open("sqlite", ":memory:")
	s.Require().NoError(err)

	s.Repo, err = NewSQLiteRepository(s.DB)
	s.Require().NoError(err)
	err = s.Repo.Init(context.Background())
	s.Require().NoError(err)
}

func (s *RepositorySuite) TearDownSuite() {
	s.DB.Close()
}

func (s *RepositorySuite) SetupTest() {
	_, err := s.DB.ExecContext(context.Background(), "DELETE FROM linkedin_auth")
	s.Require().NoError(err)
}

func (s *RepositorySuite) TestInsertAndRetrieve() {
	ctx := context.Background()
	token := accountDomain.Token{
		Scope:     "r_liteprofile r_emailaddress",
		ExpiresIn: 5184000,
	}
	profile := accountDomain.Profile{
		MemberID: "member-1",
		Email:    "test@example.com",
		Name:     "Test User",
	}

	err := s.Repo.Insert(ctx, token, profile)
	s.Require().NoError(err)

	retrieved, err := s.Repo.ByMemberID(ctx, "member-1")
	s.Require().NoError(err)
	s.Equal("member-1", retrieved.MemberID)
	s.Equal("test@example.com", retrieved.Email)
	s.Equal("Test User", retrieved.Name)
}

func (s *RepositorySuite) TestByMemberIDNotFound() {
	ctx := context.Background()

	_, err := s.Repo.ByMemberID(ctx, "non-existent")
	s.Error(err)
	s.Contains(err.Error(), "not found")
}

func (s *RepositorySuite) TestDelete() {
	ctx := context.Background()
	token := accountDomain.Token{Scope: "r_liteprofile"}
	profile := accountDomain.Profile{
		MemberID: "member-delete",
		Email:    "delete@example.com",
		Name:     "Delete Me",
	}

	err := s.Repo.Insert(ctx, token, profile)
	s.Require().NoError(err)

	err = s.Repo.Delete(ctx, "member-delete")
	s.Require().NoError(err)

	_, err = s.Repo.ByMemberID(ctx, "member-delete")
	s.Error(err)
}

func (s *RepositorySuite) TestUniqueMemberIDByEmail() {
	ctx := context.Background()
	token := accountDomain.Token{Scope: "r_liteprofile"}
	profile := accountDomain.Profile{
		MemberID: "member-unique",
		Email:    "unique@example.com",
		Name:     "Unique User",
	}

	err := s.Repo.Insert(ctx, token, profile)
	s.Require().NoError(err)

	match, err := s.Repo.UniqueMemberIDByEmail(ctx, "unique@example.com")
	s.Require().NoError(err)
	s.Equal("member-unique", match.MemberID)
}
