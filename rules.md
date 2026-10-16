# DDD Rules & Architecture Guidelines

## Domain-Driven Design Principles

### 1. Layered Architecture

```
┌─────────────────────────────────────┐
│  Presentation (Handlers/HTTP)       │  ← API endpoints, request/response
├─────────────────────────────────────┤
│  Application (Stories/Use Cases)    │  ← Business workflows, orchestration
├─────────────────────────────────────┤
│  Domain (Models, Contracts)         │  ← Pure business logic, entities
├─────────────────────────────────────┤
│  Infrastructure (Gates, Repos)      │  ← External services, databases
└─────────────────────────────────────┘
```

**Rules:**
- New tests / test updated: update ONLY success use cases. If old tests not work and it's not success case - remove. 
- Dependencies point **inward** (Presentation → Application → Domain ← Infrastructure)
- Domain layer has **NO dependencies** on outer layers
- Infrastructure implements domain interfaces (Dependency Inversion)

### 2. Domain Layer Purity

**DO:**
- Define entities, value objects, and domain events
- Define repository and gate **interfaces** (contracts)
- Implement pure business logic (no I/O, no time.Now(), no external dependencies)
- Use domain-specific language (ubiquitous language)

**DON'T:**
- Import infrastructure packages (database, HTTP, metrics)
- Call external services directly
- Use `time.Now()` - pass time as parameter for testability
- Access configuration or environment variables
- Implement persistence logic

**Example - CORRECT:**
```go
// internal/domain/entitlement/models.go
type Entitlement struct {
    AccessUntil time.Time
    IsPro       bool
}

// Domain logic is pure and testable
func (e Entitlement) HasAccess(now time.Time) bool {
    return !e.AccessUntil.IsZero() && now.Before(e.AccessUntil)
}
```

**Example - WRONG:**
```go
// DON'T DO THIS - domain depends on external time
func (e Entitlement) HasAccess() bool {
    return !e.AccessUntil.IsZero() && time.Now().Before(e.AccessUntil)
}
```

### 3. Application Layer (Stories)

Stories orchestrate domain objects to fulfill use cases.

**Rules:**
- One story = one use case
- Stories depend on domain interfaces, not implementations
- Stories coordinate multiple domain objects
- Stories handle transactions and cross-cutting concerns
- **Story contracts belong in the owning domain package** (for example `internal/domain/account/stories.go`)
- **Story I/O models belong in the owning domain package** (for example `internal/domain/linkedin/stories.go`)
- Stories require logger in context for structured logging

**Story Contract Pattern:**
```go
// internal/domain/account/stories.go
type LinkedInAuthStory interface {
    CompleteAuth(ctx context.Context, input CompleteAuthInput) (CompleteAuthOutput, error)
}

// internal/domain/account/stories.go
type CompleteAuthInput struct {
    Code         string
    RedirectURI  string
    CodeVerifier string
    StartedAt    time.Time
}

type CompleteAuthOutput struct {
    AccessToken string
    Profile     linkedinDomain.Profile
    ConnectedAt time.Time
}
```

**Story Implementation:**
```go
// internal/stories/complete_linkedin_auth.go
type completeLinkedInAuthStory struct {
    linkedInGate linkedinDomain.Gate      // Interface
    accountRepo  accountDomain.Repository // Interface
    trialRepo    trialDomain.Repository   // Interface
}

func NewLinkedInAuthStory(
    linkedInGate linkedinDomain.Gate,
    accountRepo accountDomain.Repository,
    trialRepo trialDomain.Repository,
) domainStories.LinkedInAuthStory {
    return &completeLinkedInAuthStory{
        linkedInGate: linkedInGate,
        accountRepo:  accountRepo,
        trialRepo:    trialRepo,
    }
}

func (s *completeLinkedInAuthStory) CompleteAuth(ctx context.Context, input domainStories.CompleteAuthInput) (domainStories.CompleteAuthOutput, error) {
    // Logger must be in context
    logger := logger.FromContext(ctx)
    
    // Orchestrate domain objects
    token, _ := s.linkedInGate.ExchangeCode(ctx, input.Code, input.RedirectURI, input.CodeVerifier)
    profile, _ := s.linkedInGate.UserInfo(ctx, token.AccessToken)
    s.accountRepo.Insert(ctx, token, profile)
    s.trialRepo.Ensure(ctx, profile.Email, profile.MemberID, time.Now().UTC())
    
    return domainStories.CompleteAuthOutput{
        AccessToken: token.AccessToken,
        Profile:     profile,
        ConnectedAt: time.Now().UTC(),
    }, nil
}
```

**Handler Responsibilities (Thin Handlers):**
Handlers should ONLY:
1. Validate HTTP concerns (method, content-type, size limits)
2. Parse and validate request data
3. Call story with validated input
4. Map story output to HTTP response
5. Handle HTTP-specific errors

Handlers should NOT:
- Contain business logic
- Orchestrate multiple domain objects
- Make decisions about domain rules
- Directly access repositories (except through stories)

**CORRECT - Thin Handler:**
```go
func (s *service) LinkedInToken(w http.ResponseWriter, r *http.Request) {
    // 1. Validate HTTP
    if r.Method != http.MethodPost {
        writeAPIError(w, http.StatusMethodNotAllowed, "method_not_allowed", "method not allowed")
        return
    }
    
    // 2. Parse request
    var req tokenRequest
    if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
        writeAPIError(w, http.StatusBadRequest, "bad_request", "bad request")
        return
    }
    
    // 3. Call story
    output, err := s.linkedInAuth.CompleteAuth(r.Context(), domainStories.CompleteAuthInput{
        Code:         req.Code,
        RedirectURI:  req.RedirectURI,
        CodeVerifier: req.CodeVerifier,
        StartedAt:    time.Now(),
    })
    
    // 4. Handle errors
    if err != nil {
        writeAPIError(w, http.StatusInternalServerError, "auth_failed", "authentication failed")
        return
    }
    
    // 5. Map response
    w.Header().Set("Content-Type", "application/json")
    json.NewEncoder(w).Encode(output)
}
```

**WRONG - Fat Handler:**
```go
func (s *service) LinkedInToken(w http.ResponseWriter, r *http.Request) {
    // DON'T - business logic in handler
    token, err := s.linkedIn.ExchangeCode(r.Context(), req.Code, ...)
    profile, err := s.linkedIn.UserInfo(r.Context(), token.AccessToken)
    s.accountRepo.Insert(r.Context(), token, profile)
    s.trialRepo.Ensure(r.Context(), profile.Email, profile.MemberID, time.Now())
    // ... 100 more lines of business logic
}
```

### 4. Infrastructure Layer

**Gates:** External service adapters (LinkedIn, Resend, Recaptcha)
**Repositories:** Persistence adapters (SQLite, PostgreSQL)

**Rules:**
- Implement domain interfaces
- Handle I/O, serialization, error translation
- No business logic - just adaptation
- Can depend on platform packages (logger, metrics, httpclient)

### 5. Dependency Injection

**All dependencies must be injected through constructors.**

**Constructor Validation:**
All required dependencies MUST be validated in the constructor. Never check for nil at runtime — fail fast at construction time.

**No panics in application code:** constructors return detailed errors; `main` logs fatal setup errors.

**CORRECT:**
```go
func NewLinkedInPostStory(
    linkedIn linkedinDomain.Gate,
    quota dailyDomain.QuotaConsumer,
    productEvents domainMetrics.PostPublishRepository,
) (domainStories.LinkedInPostStory, error) {
    if linkedIn == nil {
        return nil, fmt.Errorf("linkedin post story: linkedin gate is required")
    }
    if quota == nil {
        return nil, fmt.Errorf("linkedin post story: quota consumer is required")
    }
    if productEvents == nil {
        return nil, fmt.Errorf("linkedin post story: product events repo is required")
    }
    return &linkedInPostStory{
        linkedIn:      linkedIn,
        quota:         quota,
        productEvents: productEvents,
    }, nil
}
```

**WRONG:**
```go
// DON'T - nil checks at runtime hide configuration errors
func (s *linkedInPostStory) CreatePost(...) {
    if s.productEvents != nil {  // Should have failed in constructor
        s.productEvents.RecordPostPublished(...)
    }
}
```

**CORRECT:**
```go
func NewService(
    linkedInGate linkedinDomain.Gate,
    accountRepo accountDomain.Repository,
    config Config,
) (*service, error) {
    if linkedInGate == nil {
        return nil, fmt.Errorf("service: linkedin gate is required")
    }
    if accountRepo == nil {
        return nil, fmt.Errorf("service: account repo is required")
    }

    return &service{
        linkedInGate: linkedInGate,
        accountRepo:  accountRepo,
        config:       config,
    }, nil
}
```

**WRONG:**
```go
// DON'T - creates hidden dependencies
func NewService() *service {
    return &service{
        linkedInGate: linkedin.New(...),  // Hidden dependency
        accountRepo:  account.New(...),  // Hidden dependency
    }
}
```

### 6. Interface Segregation

**Small, focused interfaces are better than large ones.**

**CORRECT:**
```go
type AccountReader interface {
    ByMemberID(ctx context.Context, memberID string) (Profile, error)
}

type AccountWriter interface {
    Insert(ctx context.Context, token Token, profile Profile) error
    Delete(ctx context.Context, memberID string) error
}

type Repository interface {
    AccountReader
    AccountWriter
}
```

**WRONG:**
```go
// Too large - violates ISP
type Repository interface {
    ByMemberID(...)
    Insert(...)
    Delete(...)
    Update(...)
    List(...)
    Count(...)
    // ... 20 more methods
}
```

### 7. Metrics Pattern

**Use story-based metrics collection (like wallet-core).**

**CORRECT:**
```go
// internal/metrics/collect_post_metrics.go
type collectPostMetrics struct {
    postRepo    postDomain.Repository
    metricsRepo clustermetrics.Pusher
}

func NewCollectPostMetricsStory(
    postRepo postDomain.Repository,
    metricsRepo clustermetrics.Pusher,
) *collectPostMetrics {
    return &collectPostMetrics{
        postRepo:    postRepo,
        metricsRepo: metricsRepo,
    }
}

func (s *collectPostMetrics) Execute(ctx context.Context) error {
    counts, _ := s.postRepo.Counts(ctx)
    for status, count := range counts {
        s.metricsRepo.Push(ctx, count, []string{status.String()})
    }
    return nil
}
```

**WRONG:**
```go
// DON'T - scatters metrics logic across codebase
func (s *service) PublishPost(...) {
    // ... business logic
    s.counters.PostsPublish.WithLabelValues("success").Inc()  // Leaky abstraction
}
```

### 8. Error Handling

**Use typed errors for domain-specific failures.**

**CORRECT:**
```go
// Domain errors
var (
    ErrNotFound      = errors.New("not found")
    ErrUnauthorized  = errors.New("unauthorized")
    ErrRateLimited   = errors.New("rate limited")
)

// Infrastructure translates to typed errors
func (r *SQLiteRepository) ByMemberID(...) (Profile, error) {
    err := r.db.QueryRow(...).Scan(...)
    if errors.Is(err, sql.ErrNoRows) {
        return Profile{}, domain.ErrNotFound
    }
    return Profile{}, err
}
```

### 9. File Organization

```
internal/
├── domain/              # Pure business logic
│   ├── account/
│   │   ├── contracts.go # Interfaces
│   │   └── models.go    # Entities, value objects
│   ├── entitlement/
│   │   ├── contracts.go
│   │   └── models.go
│   └── stories/         # Story contracts (interfaces + I/O models)
│       ├── contracts.go # Story interfaces
│       └── models.go    # Input/output models
├── stories/             # Story implementations
│   ├── complete_linkedin_auth.go
│   ├── feedback.go
│   └── sync_drafts.go
├── gates/               # External adapters
│   ├── linkedin/
│   │   └── api.go
│   └── repositories/
│       └── account/
│           └── sqlite.go
├── handlers/            # HTTP/Presentation (THIN)
│   └── http/v0/
│       ├── service.go   # Mux setup + DI only (~150 lines)
│       ├── linkedin_auth.go  # Thin handlers
│       └── feedback.go
├── platform/            # Cross-cutting concerns
│   ├── logger/
│   ├── httpclient/
│   └── error/
└── mocks/               # Generated test doubles (NOT in domain!)
    ├── domain/
    │   ├── account/
    │   │   └── mock_Repository.go
    │   └── linkedin/
    │       └── mock_Gate.go
    └── platform/
        └── httpclient/
            └── mock_Client.go
```

**Critical: Mock Placement**
- ✅ **CORRECT:** `internal/mocks/domain/account/mock_Repository.go`
- ❌ **WRONG:** `internal/domain/account/mocks/mock_Repository.go`

Mocks must live OUTSIDE the domain layer to keep domain pure. Use centralized mockery config:

```yaml
# .mockery.yaml
structname: Mock{{.InterfaceName}}
pkgname: mocks
filename: mock_{{.InterfaceName}}.go
template: testify
dir: "internal/mocks/{{.InterfaceDirRelative}}"
packages:
  bloomary-api/internal/domain/account:
    interfaces:
      Repository:
```

Generate with: `task generate` or `go run github.com/vektra/mockery/v3@v3.8.1`

### 10. Testing Strategy

**Test Types:**
1. **Unit Tests:** Test domain logic and stories in isolation with mocks
2. **Integration Tests:** Test gates with httptest.Server to validate HTTP contracts
3. **Repository Tests:** Test SQLite implementations with in-memory database
4. **E2E Tests:** Test handlers with real database (future)

**Test Location:** Co-located with code they test
- `internal/domain/account/models_test.go` - Domain logic
- `internal/stories/complete_linkedin_auth_test.go` - Story tests
- `internal/gates/repositories/account/sqlite_test.go` - Repository tests
- `internal/gates/linkedin/api_test.go` - Gate unit tests (mocks)
- `internal/gates/linkedin/api_integration_test.go` - Gate integration tests (httptest)

**Test Infrastructure:**
```go
// tests/base_suite.go - Reusable test suite with SQLite setup
type BaseSuite struct {
    suite.Suite
    Ctx  context.Context
    DB   *sql.DB
    DBPath string
    
    // Repositories
    AccountRepo     *accountRepo.SQLiteRepository
    EntitlementRepo *entitlementRepo.SQLiteRepository
    TrialRepo       *trialRepo.SQLiteRepository
}

func (s *BaseSuite) SetupSuite() {
    s.DB = SQLiteConnect(s.Ctx, s.T(), s.DBPath)
    // Initialize repositories...
}

func (s *BaseSuite) TearDownSuite() {
    CleanupSQLite(s.T(), s.DB, s.DBPath)
}
```

**Gate Integration Test Pattern:**
```go
// Use httptest.Server to validate actual HTTP request/response formats
func (s *APIIntegrationSuite) TestExchangeCode_RequestFormat() {
    var receivedRequest *http.Request
    var receivedBody []byte
    
    server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
        receivedRequest = r
        receivedBody, _ = io.ReadAll(r.Body)
        w.WriteHeader(http.StatusOK)
        w.Write([]byte(`{"access_token":"test-token",...}`))
    }))
    defer server.Close()
    
    api := New("client-id", "secret", server.URL, server.URL, client, nil, counters)
    token, err := api.ExchangeCode(s.ctx, "auth-code", "redirect", "verifier")
    
    // Verify request format
    s.Equal(http.MethodPost, receivedRequest.Method)
    s.Equal("/oauth/v2/accessToken", receivedRequest.URL.Path)
    s.Contains(string(receivedBody), "grant_type=authorization_code")
}
```

**Import Aliasing** 

linkedinDomain "bloomary-api/internal/domain/linkedin"
linkedinGate "bloomary-api/internal/gates/linkedin"
metricsRepo "bloomary-api/internal/gates/repositories/metrics"
oauthStateRepo "bloomary-api/internal/gates/repositories/oauth_state"


**Story Test Pattern:**
```go
// Use generated mocks for story dependencies
func (s *FeedbackStorySuite) TestSubmitFeedback_Success() {
    mockResendGate := new(resendMocks.MockGate)
    mockRecaptchaGate := new(recaptchaMocks.MockCaptcha)
    
    mockRecaptchaGate.On("Verify", s.Ctx, "valid-captcha", "").Return(nil)
    mockResendGate.On("SendFeedback", s.Ctx, mock.Anything).Return(nil)
    
    story := NewFeedbackStory(mockResendGate, mockRecaptchaGate, counters)
    
    err := story.SubmitFeedback(s.Ctx, domainStories.FeedbackInput{
        Name:              "John Doe",
        Message:           "I love using Bloomary!",
        RecaptchaResponse: "valid-captcha",
    })
    
    s.Require().NoError(err)
    mockResendGate.AssertExpectations(s.T())
}
```

**Logger in Test Context:**
Stories require logger in context. Always set up logger in test SetupTest:
```go
func (s *StorySuite) SetupTest() {
    log, _ := logger.New(logger.Output(io.Discard))
    s.Ctx = log.ToContext(context.Background())
}
```

**Coverage Targets:**
- Domain: 90%+ (pure logic, easy to test)
- Stories: 80%+ (orchestration with mocks)
- Gates: 70%+ (unit + integration tests)
- Repositories: 70%+ (SQLite tests)
- Handlers: 60%+ (thin, mostly validation)
- **Overall: 50%+** (currently 22.9%)

## Anti-Patterns to Avoid

### 1. Anemic Domain Model
**WRONG:** Domain objects are just data bags with no behavior
```go
type User struct {
    ID   string
    Name string
}
// All logic in services/handlers
```

**CORRECT:** Rich domain models with behavior
```go
type User struct {
    id   string
    name string
}

func (u *User) ChangeName(newName string) error {
    if newName == "" {
        return ErrInvalidName
    }
    u.name = newName
    return nil
}
```

### 2. God Objects
**WRONG:** One service/handler does everything
```go
type service struct {
    // 50 dependencies
    // 100 methods
}
```

**CORRECT:** Small, focused services
```go
type authStory struct {
    linkedInGate linkedinDomain.Gate
    accountRepo  accountDomain.Repository
}
```

### 3. Leaky Abstractions
**WRONG:** Domain knows about infrastructure
```go
import "database/sql"

type Repository interface {
    Query(ctx context.Context, query string) (*sql.Rows, error)
}
```

**CORRECT:** Domain defines intent, infrastructure implements
```go
type Repository interface {
    ByMemberID(ctx context.Context, memberID string) (Profile, error)
}
```

### 4. Service Layer Anti-Pattern
**WRONG:** Service layer just passes data through
```go
func (s *UserService) GetUser(id string) (User, error) {
    return s.repo.GetUser(id)  // Just delegation
}
```

**CORRECT:** Service layer orchestrates and adds value
```go
func (s *GetUserStory) Execute(ctx context.Context, id string) (User, error) {
    user, err := s.repo.ByID(ctx, id)
    if err != nil {
        return User{}, err
    }
    s.metrics.RecordUserAccess(id)
    return user, nil
}
```

## Code Review Checklist

- [ ] Domain layer has no infrastructure imports?
- [ ] All dependencies are injected through constructors?
- [ ] Interfaces are small and focused (ISP)?
- [ ] Business logic is in domain/story layer, not handlers?
- [ ] Time is passed as parameter, not `time.Now()` in domain?
- [ ] Errors are typed and domain-specific?
- [ ] Tests are co-located with code?
- [ ] No god objects (files > 500 lines)?
- [ ] Metrics collected through stories, not scattered?
- [ ] Handlers only handle HTTP concerns, not business logic?
- [ ] Import aliases follow naming convention (see Import Aliasing below)?

## Static Checks

- golangci lint + config  + golangci-lint run ./...
- fieldalignment : go install golang.org/x/tools/go/analysis/passes/fieldalignment/cmd/fieldalignment@latest + ~/go/bin/fieldalignment ./...

---

## Import Aliasing

**Rule:** Use consistent import aliases to make the layer of each dependency obvious at a glance.

| Layer | Pattern | Example |
|-------|---------|---------|
| Domain | `<name>Domain` | `accountDomain "bloomary-api/internal/domain/account"` |
| Gate (external service) | `<name>Gate` | `linkedinGate "bloomary-api/internal/gates/linkedin"` |
| Repository (persistence) | `<name>Repo` | `accountRepo "bloomary-api/internal/gates/repositories/account"` |

**CORRECT:**
```go
import (
    accountDomain "bloomary-api/internal/domain/account"
    linkedinGate "bloomary-api/internal/gates/linkedin"
    accountRepo "bloomary-api/internal/gates/repositories/account"
)
```

**WRONG:**
```go
import (
    // Unclear what layer these are from
    "bloomary-api/internal/domain/account"
    "bloomary-api/internal/gates/linkedin"
    "bloomary-api/internal/gates/repositories/account"
)
```

**Why:** When reading code, `accountDomain.Repository` vs `accountRepo.SQLiteRepository` immediately tells you whether you're looking at a domain interface or an infrastructure implementation. This prevents accidental layer violations and makes code reviews faster.
