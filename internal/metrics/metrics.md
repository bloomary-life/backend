# Metrics

No PII, tokens, member IDs, emails, or high-cardinality IDs in labels.

## Infra (`:4444`, localhost only)

- `/healthcheck` — app health, SQLite, version
- `/version` — build-time version
- `/metrics` — Prometheus scrape

## HTTP (inbound)

| Metric | Type | Labels |
|--------|------|--------|
| `bloomary_http_requests_total` | counter | route, method, status |
| `bloomary_http_request_duration_seconds` | histogram | route, method, status |

Emitted by `InstrumentHTTP` wrapping the mux in `main`.

## Outbound HTTP

| Metric | Type | Labels |
|--------|------|--------|
| `bloomary_outbound_http_requests_total` | counter | client, host, path, method, status |
| `bloomary_outbound_http_request_duration_seconds` | histogram | client, host, path, method, status |
| `bloomary_outbound_http_in_flight` | gauge | client, host, path, method, status |

Emitted by `internal/platform/httpclient`. Clients: `linkedin`, `resend`, `recaptcha`.

## Product Counters

| Metric | Labels |
|--------|--------|
| `bloomary_posts_publish_total` | result: `success`, `error` |
| `bloomary_linkedin_auth_total` | result: `success`, `error` |
| `bloomary_feedback_total` | result: `success`, `error`; kind: `feedback`, `confirmation` |
| `bloomary_rate_limit_total` | scope: `daily_linkedin`, `linkedin_api`, `resend_api`, `recaptcha_api`, `entitlement` |
| `bloomary_paddle_webhook_total` | event_type: `subscription.created/updated/canceled`, `unknown`; result: `success`, `error`, `ignored`, `user_not_found` |

## Product Gauges (daily, no labels, 24h cache)

| Metric | Source |
|--------|--------|
| `bloomary_signups_yesterday` | connected LinkedIn yesterday |
| `bloomary_activated_yesterday` | first published post yesterday |
| `bloomary_active_yesterday` | published yesterday |
| `bloomary_users_4_posts_last_7d` | ≥4 posts in last 7 days |

Derived from `linkedin_auth` and `metric_post_publishes` tables. UTC calendar days.

## Domain Layer

- `domain/metrics/contracts.go` — `Recorder` interface (stories depend on this, not concrete types)
- `metrics.ProductCounters` — implements `Recorder`
- Handlers record rate limits (429) and Paddle webhook outcomes directly

## Rules

- Counters and histograms for events; gauges for daily snapshots.
- Labels: route, method, status, result — all bounded. Route templates, not full paths.
- Alert on symptoms: 5xx rate, slow publish, auth failure spike, health failure.
- SQLite health via `/healthcheck`, not metrics labels.
- Sentry for stack traces; Prometheus for trends.
