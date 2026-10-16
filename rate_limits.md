# Third-Party API Rate Limits

Per-service limits and Bloomary implementation status.

## 1. LinkedIn

| Service | Official limit | Per-account/day key | Bloomary impl | Storage | Notes |
|---------|---------------|---------------------|---------------|---------|-------|
| **OAuth token exchange** (`POST /oauth/v2/accessToken`) | 150/member/day, 100K/app/day | `linkedin_token` | ✅ Implemented | In-memory (global) | Configurable via env — default 2 RPS / burst 4. Tracks cooldown on 429. |
| **UserInfo** (`GET /v2/userinfo`) | Same as above | `linkedin_userinfo` | ✅ Implemented | In-memory (global) | Configurable rate limiter + 429 handling with `Retry-After`. |
| **Post publish** (`POST /rest/posts`) | Same as above | `linkedin_post` | ✅ Implemented | In-memory (global) | Full 429 → cooldown path; logs rate-limit headers. |
| **Media upload init** (`POST .../initializeUpload`) | Same as above | `linkedin_initialize_upload/<image\|document>` | ✅ Implemented | In-memory (global) | Separate per-operation cooldown stats. |
| **Media upload** (`PUT <uploadUrl>`) | Same as above | `linkedin_upload` | ✅ Implemented | In-memory (global) | Via `do()` helper; respects cooldown & rate limiter. |
| **Member-level daily quota** | ~150/day (per published docs) | Per member_id | ✅ Implemented | SQLite (`linkedin_daily_requests`) | Hard 20 requests/member/day at app level. Enforced via `ConsumeQuota` → SQLite `INSERT … ON CONFLICT … WHERE count < ?`. Resets per UTC day. |

**Config:**
```
LINKEDIN_RATE_LIMIT_RPS     = 2     (default)
LINKEDIN_RATE_LIMIT_BURST   = 4     (default)
LINKEDIN_DAILY_REQUEST_LIMIT = 20   (default, per member/day)
```

**Key detail:** The in-memory `RateLimiter` is global across all members (a single bucket). Member-specific daily limits are enforced separately in SQLite. Access tokens are now stored in DB + cache-warmed on startup (no more blind LinkedIn API calls).

---

## 2. Resend (email)

| Service | Official limit | Tier | Bloomary impl | Storage | Notes |
|---------|---------------|------|---------------|---------|-------|
| **Send email** (`POST /emails`) | 10 req/sec/team (shared across all keys) | Pro €20/mo (no daily cap) | ✅ Implemented | None (no local limit) | Only used for feedback emails (~few/day). 429 tracked via metrics (`RecordRateLimit(RateLimitResendAPI)`). Daily/monthly quotas handled by Resend, not locally. |

**Note:** Free tier = 100 emails/day, 3000/month. Pro (€20/mo) removes daily limit. We are on Pro. No local enforcement needed.

---

## 3. reCAPTCHA (Google)

| Service | Official limit | Free tier | Bloomary impl | Storage | Notes |
|---------|---------------|-----------|---------------|---------|-------|
| **Verify** (`POST /api/siteverify`) | 60K/min, unlimited per-day | 10K assessments/month (calendar month) | ✅ Implemented | None (no local limit) | 429 tracked via metrics (`RecordRateLimit(RateLimitRecaptchaAPI)`). Used only during feedback submission. At current volume (< 100/day) well within free quota. |

**Warning:** Google cut free tier from 1M/month → 10K/month in April 2024. Premium: $8 flat for 10K–100K, then $1/1K over. If growth exceeds ~300 verifications/day sustained, consider upgrade or migration (e.g., Turnstile/FriendlyCaptcha).

---

## 4. Cloudflare R2 (object storage)

| Service | Official limit | Free tier | Bloomary impl | Storage | Notes |
|---------|---------------|-----------|---------------|---------|-------|
| **Presigned URL generation** (server-side) | N/A — S3-compatible API | 10 GB-month free, 10M GET/month free | ✅ Implemented | None | We use presigned URLs; client uploads directly to R2. Server only generates URLs (cheap, fast). |
| **R2 control plane** (REST API) | 1,200 req / 5 min | N/A | ❌ Not implemented | N/A | We don't use control plane — only data plane via S3 SDK. |
| **Data plane** (S3 API PUT/GET) | 50 ops/sec/bucket (control), no hard cap on data plane (r2.dev test domain only) | 1M Class A free/month, 10M Class B free/month | ✅ Implemented | None | Draft images stored here. Volume negligible. |

---

## 5. Paddle (payments)

| Service | Official limit | Standard tier | Bloomary impl | Storage | Notes |
|---------|---------------|---------------|---------------|---------|-------|
| **Webhook receipt** (incoming) | 240 req/min/IP | 240 req/min per IP | ✅ Handled | None | Webhooks come from Paddle, we respond quickly. 429 penalty: 60s IP block after exceeding. Preview endpoints: 1,000 req/min/IP (since 2026). |
| **Paddle.js** (client-side checkout) | Per-client-IP (each visitor has own IP) | Same | ✅ Client-side | N/A | Not server-side. No API calls from backend. |

**No outbound Paddle API calls.** We only receive webhooks and run client-side Paddle.js.

---

## 6. Apple App Store / StoreKit (in-app purchases)

| Service | Official limit | Production | Bloomary impl | Storage | Notes |
|---------|---------------|------------|---------------|---------|-------|
| **StoreKit JWS verification** (receipt validation) | 50 req/sec per endpoint (hourly enforcement) | Per-app | ✅ Implemented | In-memory | Done client-side (StoreKit 2). Backend stores transaction but doesn't verify via Apple API — StoreKit 2 does server-side verification already. |
| **App Store Server API** (optional, for receipt validation) | See table below | Per-app | ❌ Not implemented | N/A | We don't call this API — reliance on StoreKit 2 for in-app purchase flow. |

**App Store Server API rate limits** (if we ever integrate):
| Endpoint | Limit |
|----------|-------|
| Get Transaction Info, History, All Subscription Statuses | 50 req/sec |
| Finish Transaction, Send Consumption Info | 50 req/sec |
| Extend Subscription Renewal Date, Set Account Token | 20 req/sec |
| Refund Transaction | 5 req/sec |
| Notify Relevant Customer | 5 req/sec |

---

## Summary Table

| Service | Limit Type | Our Config / Value | Impl? | Storage |
|---------|-----------|-------------------|-------|---------|
| **LinkedIn OAuth/token/post/upload** | RPS/burst global + daily per-member | 2 RPS / burst 4 + 20/day/member | ✅ Global limiter + SQLite | In-memory limiter + `linkedin_daily_requests` table |
| **Resend** | 10 req/sec team + tier quotas | Pro €20/mo (no daily cap) | ✅ Metrics only | None needed |
| **reCAPTCHA** | 60K/min, 10K/month free | Free tier OK for now | ✅ Metrics only | None needed |
| **Cloudflare R2** | 50 ops/sec/bucket (control), no hard cap (data) | Negligible volume | ✅ Presigned URLs | None needed |
| **Paddle** | 240 req/min/IP (webhooks), preview 1K/min/IP | Incoming only | ✅ Handled | None needed |
| **Apple StoreKit** | 50 req/sec/endpoint (if ASASA integrated) | Not used (client-side) | ❌ N/A | N/A |

## Implementation Details

### In-Memory Rate Limiter (`platform/ratelimiter/ratelimit.go`)
- **Global limiter:** `rate.NewLimiter(rps, burst)` — applied to all LinkedIn HTTP requests
- **Keyed limiter:** `NewKeyedRateLimiter(config)` — per-key bucket, TTL-based cleanup (30min), refs counter
- LinkedIn gate uses the **global limiter** only
- Keyed limiter available for future per-member/per-IP rate limiting

### Per-Member Daily Quota (`gates/repositories/daily/sqlite.go`)
- SQLite table `linkedin_daily_requests`: `(member_id, day) PK`, `count`, `posted`
- Uses `ON CONFLICT … DO UPDATE SET count = count + 1 WHERE count < ?` — atomic safe
- Resets at UTC midnight (day key)
- Enforced in `stories/linkedin_post.go:consumeQuota()`

### IP-Based Daily Limiter (`gates/repositories/ratelimit/daily_ip_limiter.go`)
- In-memory per-domain, per-IP daily counter
- Resets at UTC midnight
- Available as generic daily limiter (currently unused outside feedback?)

### Error Handling
All third-party gates:
- Track rate limit hits via `metrics.ProductCounters.RecordRateLimit(scope)`
- Handle 429 with cooldown + `Retry-After` parsing (LinkedIn)
- Record metrics via `httpclient.PathLabel` for observability
