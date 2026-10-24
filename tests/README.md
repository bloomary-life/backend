# Bloomary Backend Test Infrastructure

## Overview

- **SQLite test database helpers** for isolated, file-based testing
- **BaseSuite** with pre-configured repositories and helper methods
- **Example tests** demonstrating usage patterns

## Directory Structure

```
tests/
├── db.go              # SQLite connection and cleanup helpers
├── base_suite.go      # BaseSuite with repository initialization
├── context.go         # TestContext(): context with discard logger
├── fakes.go           # FakePostPublishRepository test fake
├── example_test.go    # Example test suite
└── README.md          # This file
```

## Usage

### Using BaseSuite

Extend `BaseSuite` to get access to pre-configured repositories:

```go
package tests

import (
    "testing"
    "github.com/stretchr/testify/suite"
)

type MyFeatureSuite struct {
    BaseSuite
}

func TestMyFeatureSuite(t *testing.T) {
    suite.Run(t, new(MyFeatureSuite))
}

func (s *MyFeatureSuite) TestSomething() {
    // Use s.AccountRepo, s.EntitlementRepo, etc.
    // Use s.InsertTrial(), s.InsertEntitlement(), etc.
}
```

### Available Repositories

- `AccountRepo` - LinkedIn account management
- `DraftRepo` - Draft post management
- `EntitlementRepo` - Pro entitlement tracking (Paddle)
- `DailyRepo` - Daily post quota and post count
- `TrialRepo` - Free trial management

### Helper Methods

- `InsertTrial(email, memberID)` - Insert a trial record
- `InsertEntitlement(memberID, paddleCustomerID, until)` - Insert an entitlement

### Running Tests

```bash
# Run all tests
task test

# Run tests with isolated SQLite DB (unique path per run)
task test-sqlite

# Run tests with verbose output
task test-verbose

# Run specific test suite
go test -v ./tests -run TestEntitlementSuite
```

### Environment Variables

- `SQLITE_TEST_DB_PATH` - Custom path for test database (default: `./test_bloomary_<timestamp>.db`)

## Mock Generation

Mocks are generated using [mockery](https://github.com/vektra/mockery) for all domain interfaces.

### Generating Mocks

```bash
# Generate all mocks
task generate

# Or directly
go run github.com/vektra/mockery/v3@v3.8.1
```

### Mock Locations

Mocks are generated in `internal/mocks/` mirroring the package structure:
- `internal/mocks/domain/account/` - Account domain mocks
- `internal/mocks/domain/appleiap/` - Apple IAP domain mocks
- `internal/mocks/domain/draft/` - Draft domain mocks
- `internal/mocks/domain/entitlement/` - Entitlement domain mocks
- `internal/mocks/domain/feedback/` - Feedback domain mocks
- `internal/mocks/domain/linkedin/` - LinkedIn gate mocks
- `internal/mocks/domain/metrics/` - Metrics domain mocks
- `internal/mocks/domain/paddle_entitlement/` - Paddle entitlement domain mocks
- `internal/mocks/domain/recaptcha/` - Recaptcha gate mocks
- `internal/mocks/domain/resend/` - Resend gate mocks
- `internal/mocks/domain/daily/` - Daily quota domain mocks
- `internal/mocks/domain/scheduledpost/` - Scheduled post domain mocks
- `internal/mocks/domain/trial/` - Trial domain mocks
- `internal/mocks/domain/limiter/` - Limiter domain mocks
- `internal/mocks/platform/httpclient/` - HTTP client mocks

Placement rule (mocks outside the domain layer): [rules.md](../rules.md#9-file-organization).

### Using Mocks

```go
import (
    "testing"
    "github.com/stretchr/testify/mock"
    accountMocks "bloomary-api/internal/mocks/domain/account"
)

func TestWithMock(t *testing.T) {
    mockRepo := new(accountMocks.MockRepository)
    mockRepo.On("ByMemberID", mock.Anything, "member-1").
        Return(domainaccount.Profile{MemberID: "member-1"}, nil)
    
    // Use mockRepo in your test
}
```

## Cleanup

```bash
# Clean generated mocks
task clean-mocks

# Clean test databases
task clean-test-db

# Clean both
task clean
```

## Configuration

Mock generation is configured via `.mockery.yaml` in the backend root directory.

## Differences from wallet-core

| Aspect | wallet-core (Postgres) | bloomary (SQLite) |
|--------|------------------------|-------------------|
| Database | PostgreSQL | SQLite (modernc.org/sqlite) |
| Connection | `PG_CONNSTRING` env var | `SQLITE_TEST_DB_PATH` env var |
| Migrations | `app.Migrate()` from go-libs | Repository `Init()` methods |
| Test isolation | Truncate tables | Delete and recreate DB file |
| Mock tool | gowrap (decorators) | mockery (test mocks) |
| Test DB path | Shared postgres instance | Unique file per test run |
