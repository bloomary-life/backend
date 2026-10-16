# HTTP v0 Logging Rules

- `requestLoggingMiddleware` is the only place that emits request lifecycle logs.
- Handlers do not call `.Info("api_request")`.
- Handlers only add request context fields for the final `request_done` log.
- Middleware owns generic fields: `method`, `path`, `query_params`, `content_length`, `status`, `duration_ms`, `data`, `data_truncated`.
- Handlers own domain fields: `route`, `outcome`, booleans/counters, IDs/hashes that are safe to log.
- Do not add `error_code`; `outcome` is the canonical request result.
- For real errors, use `logger.Err(err)`.

No error:

```go
logger.SetFields(r.Context(), fields).
	SetField("outcome", "missing_linkedin_auth")
```

With error:

```go
logger.SetFields(r.Context(), fields, logger.Err(err)).
	SetField("outcome", "draft_sync_failed")
```

With extra fields:

```go
logger.SetFields(r.Context(), fields, logger.Err(err)).
	SetField("outcome", "delete_failed").
	SetField("deleted", false)
```
