package tests

import (
	"context"
	"database/sql"
	"fmt"
	"os"
	"time"

	"github.com/stretchr/testify/suite"

	paddleDomain "bloomary-api/internal/domain/paddle_entitlement"
	accountRepo "bloomary-api/internal/gates/repositories/account"
	dailyRepo "bloomary-api/internal/gates/repositories/daily"
	draftRepo "bloomary-api/internal/gates/repositories/draft"
	paddleEntitlementRepo "bloomary-api/internal/gates/repositories/paddle_entitlement"
	trialRepo "bloomary-api/internal/gates/repositories/trial"
)

type BaseSuite struct {
	suite.Suite
	Ctx    context.Context
	DB     *sql.DB
	DBPath string

	// Repositories
	AccountRepo     *accountRepo.SQLiteRepository
	DraftRepo       *draftRepo.SQLiteRepository
	EntitlementRepo *paddleEntitlementRepo.SQLiteRepository
	DailyRepo       *dailyRepo.SQLiteRepository
	TrialRepo       *trialRepo.SQLiteRepository
}

func (s *BaseSuite) SetupSuite() {
	s.Ctx = context.Background()
	s.DBPath = os.Getenv("SQLITE_TEST_DB_PATH")
	if s.DBPath == "" {
		s.DBPath = fmt.Sprintf("./test_bloomary_%d.db", time.Now().UnixNano())
	}
	s.DB = SQLiteConnect(s.Ctx, s.T(), s.DBPath)

	// Initialize repositories
	var err error
	s.AccountRepo, err = accountRepo.NewSQLiteRepository(s.DB)
	s.Require().NoError(err)
	s.DraftRepo, err = draftRepo.NewSQLiteRepository(s.DB)
	s.Require().NoError(err)
	s.EntitlementRepo, err = paddleEntitlementRepo.NewSQLiteRepository(s.DB)
	s.Require().NoError(err)
	s.DailyRepo, err = dailyRepo.NewSQLiteRepository(s.DB)
	s.Require().NoError(err)
	s.TrialRepo, err = trialRepo.NewSQLiteRepository(s.DB, 14, "test-hmac-secret")
	s.Require().NoError(err)

	// Initialize schema
	s.initSchema()
}

func (s *BaseSuite) TearDownSuite() {
	CleanupSQLite(s.T(), s.DB, s.DBPath)
}

func (s *BaseSuite) initSchema() {
	err := s.AccountRepo.Init(s.Ctx)
	s.Require().NoError(err, "error initializing account schema")

	err = s.DraftRepo.Init(s.Ctx)
	s.Require().NoError(err, "error initializing draft schema")

	err = s.EntitlementRepo.Init(s.Ctx)
	s.Require().NoError(err, "error initializing entitlement schema")

	err = s.DailyRepo.Init(s.Ctx)
	s.Require().NoError(err, "error initializing daily schema")

	err = s.TrialRepo.Init(s.Ctx)
	s.Require().NoError(err, "error initializing trial schema")
}

// Helper methods for inserting test data

func (s *BaseSuite) InsertTrial(email, memberID string) {
	_, err := s.TrialRepo.Ensure(s.Ctx, email, memberID, time.Now())
	s.Require().NoError(err, "error inserting trial")
}

func (s *BaseSuite) InsertEntitlement(memberID, paddleCustomerID string, until time.Time) {
	_, err := s.EntitlementRepo.Apply(s.Ctx, paddleDomain.Update{
		MemberID:         memberID,
		SubscriptionID:   "test-subscription-" + memberID,
		PaddleCustomerID: paddleCustomerID,
		ProUntil:         until,
		EventOccurredAt:  time.Now().UTC(),
	})
	s.Require().NoError(err, "error inserting entitlement")
}
