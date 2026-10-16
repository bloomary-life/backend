# Backend DDD Isolation Review

## File Tree

```text
internal/
├── domain/                         OK: business language, models, errors, and ports.
│   ├── account/                    OK: repository/OAuth-state ports + auth/deletion contracts.
│   ├── appleiap/                   OK: Apple IAP bounded context.
│   ├── draft/                      OK: member-scoped repository operations + one MCP story interface per operation.
│   ├── entitlement/                OK: unified access aggregate (Paddle + Apple + Trial).
│   ├── feedback/                   OK: feedback use-case language.
│   ├── limiter/                    OK: rate-limit language.
│   ├── linkedin/                   OK: post result contains LinkedIn post data only.
│   ├── mcp/                         OK: authorization story, session, state-store, and signer contracts.
│   ├── metrics/                    OK: metrics recorder + post-publish repository ports.
│   ├── paddle_entitlement/         OK: Paddle-specific entitlement bounded context.
│   ├── recaptcha/                  OK: CAPTCHA boundary.
│   ├── resend/                     OK: email vendor boundary.
│   ├── daily/                      OK: daily activity (quota + posted flag share member_id+day key).
│   ├── scheduledpost/              OK: scheduled LinkedIn post contracts.
│   └── trial/                      OK: free-tier abuse-prevention.
│
├── gates/                          OK: adapters to outside world.
│   ├── appleiap/                   OK: StoreKit JWS verification.
│   ├── linkedin/                   OK: LinkedIn HTTP adapter.
│   ├── mcp/token_signer.go         OK: keyed MCP access-token signing and verification.
│   ├── objectstore/r2.go           OK: Cloudflare R2 S3-compatible adapter.
│   ├── recaptcha/                  OK: reCAPTCHA HTTP adapter.
│   ├── resend/                     OK: email vendor adapter.
│   └── repositories/               OK: persistence adapters.
│       ├── account/sqlite.go       OK: account repo + AllActive (token cache warm-up).
│       ├── apple_entitlement/      OK: Apple entitlement repo.
│       ├── draft/sqlite.go         OK: draft text + image metadata + member-scoped MCP operations.
│       ├── paddle_entitlement/     OK: Paddle entitlement repo.
│       ├── metrics/repository.go   OK: SQL-backed metrics + post publish counter.
│       ├── mcp_oauth/               OK: expiring SQLite authorization, consent, code, and app-session state.
│       ├── oauth_state/store.go    OK: in-memory LinkedIn OAuth state.
│       ├── quota/daily_quota.go    OK: quota adapter.
│       ├── ratelimit/              OK: daily IP limiter adapter.
│       ├── daily/sqlite.go         OK: quota counter + posted flag (post count).
│       └── trial/sqlite.go         OK: HMAC pseudonymous trial repo.
│
├── handlers/http/v0/               OK: MCP OAuth transport delegates authorization lifecycle to its story.
│   ├── service.go                  OK: retain token cache; inject MCP authorization story without process-local OAuth state.
│   ├── mcp.go                      OK: transport and token/scope checks; paid access stays in MCP stories.
│   ├── mcp_oauth.go                OK: parse requests, render consent, and redirect via MCP story.
│   ├── auth.go                     OK: 3-tier token verification (cache → DB → LinkedIn).
│   ├── entitlement.go              OK: returns tier, isPro, draftLimit, postCount (needsSignature always false).
│   ├── drafts.go                   OK: draft sync endpoint.
│   ├── draft_images.go             OK: presigned upload/download for R2.
│   ├── linkedin_post.go            OK: post publishing + post count recording.
│   └── ...                         TODO: keep handlers transport-focused; move use-case decisions to stories.
│
├── platform/                       OK: cross-cutting infrastructure.
│   ├── httpclient/                 OK: shared HTTP client.
│   ├── identity/                   OK: appAccountToken generation for Apple IAP.
│   ├── error/                      OK: typed errors (cross-cutting, allowed in domain).
│   └── logger/                     OK: structured logging (cross-cutting, allowed in stories).
│
└── stories/                        OK: flat business use cases; MCP story owns paid-plan enforcement.
    ├── complete_linkedin_auth.go   OK: stores access token in DB on OAuth completion.
    ├── entitlement.go              OK: unified access + Tier + DraftLimit + PostCount.
    ├── sync_drafts.go              OK: tier-aware sync (trial/pro 50; lapsed read-only, new drafts while < 5).
    ├── linkedin_post.go            OK: records post publish for post count.
    ├── mcp_oauth.go                OK: authorization lifecycle, paid access, consent, code exchange.
    ├── list_mcp_drafts.go          OK: member-scoped list + paid access check.
    ├── get_mcp_draft.go            OK: member-scoped get + paid access check.
    ├── create_mcp_draft.go         OK: idempotent member-scoped create + paid access check.
    ├── update_mcp_draft.go         OK: atomic update of unpublished member draft + paid access check.
    └── ...                         OK: stories depend on domain ports.
```

## Isolation Issues

### Current

- `handlers/http/v0/linkedin_post.go` translates `daily.ErrDailyLimitExceeded`; move outcome data and policy decisions behind the post story.
- `handlers/http/v0/mcp.go` implements token parsing/signing and fallback verification; delegate token lifecycle/verification to MCP ports/adapters, leaving claims-to-context and scope enforcement in transport.
- `stories/apple_transaction_sync.go` imports `platform/identity`; move app-account-token generation behind a domain port or into the identity adapter.
- `handlers/http/v0/service.go` aggregates every domain contract; split composition by feature if this keeps growing.

### Accepted

- SQLite repository/bootstrap wiring stays in `store.go`; persistence adapters remain under `gates/repositories/`.
- Stories import `platform/logger` and `platform/error` as cross-cutting dependencies.
- Gates may use shared `platform/httpclient`, `platform/logger`, and `platform/error`.

## Import Aliasing

```sh
linkedinDomain "bloomary-api/internal/domain/linkedin"
paddleDomain "bloomary-api/internal/domain/paddle_entitlement"
appleDomain "bloomary-api/internal/domain/apple_entitlement"
linkedinGate "bloomary-api/internal/gates/linkedin"
metricsRepo "bloomary-api/internal/gates/repositories/metrics"
oauthStateRepo "bloomary-api/internal/gates/repositories/oauth_state"
```
