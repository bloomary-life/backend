package tests

import (
	"testing"
	"time"

	"github.com/stretchr/testify/suite"
)

type EntitlementSuite struct {
	BaseSuite
}

func TestEntitlementSuite(t *testing.T) {
	suite.Run(t, new(EntitlementSuite))
}

func (s *EntitlementSuite) TestReadsLocalProUntil() {
	// Insert test data
	s.InsertEntitlement("member-1", "ctm_test", time.Now().Add(24*time.Hour))

	// Query
	entitlement, err := s.EntitlementRepo.ByMemberID(s.Ctx, "member-1")
	s.Require().NoError(err)
	s.False(entitlement.AccessUntil.IsZero())
}

func (s *EntitlementSuite) TestTrialCreation() {
	// Insert trial
	s.InsertTrial("test@example.com", "member-2")

	// Verify trial was created
	record, err := s.TrialRepo.Ensure(s.Ctx, "test@example.com", "member-2", time.Now())
	s.Require().NoError(err)
	s.NotEmpty(record.PseudonymousIdentifier)
}
