# Requirements — Bloomary backend

Format and evidence rules: [CONTRIBUTING.md](CONTRIBUTING.md). Check: `scripts/trace-check.sh REQUIREMENTS.md .`.

| ID | Requirement (When X, Y.) | Source | Status |
|---|---|---|---|
| BE-001 | When a member publishes a draft manually with its `draftId`, that draft's pending, retry or needs-review schedule is cancelled, so the worker never posts it twice. | Kat, 2026-10-22 | active |
| BE-002 | When a member sends `DELETE /api/linkedin/schedule?draftId=…`, that draft's pending, retry or needs-review schedule is cancelled and leaves the active schedule list. | Kat, 2026-10-22 | active |
| BE-003 | When no custom root pool is configured, StoreKit JWS chains are trusted only if they end at Apple Root CA - G3, so a leaf from any other public CA is rejected. | Nightly review 2026-10-24 | active |
| BE-004 | When a scheduled post hits the LinkedIn daily limit, the job is marked failed at once instead of being retried. | Nightly review 2026-10-24 | active |
| BE-005 | When a post is published or feedback is sent, `posts_publish_total` and `feedback_total` grow by one (recorded by the gate only, not again by the story). | Nightly review 2026-10-24 | active |
| BE-006 | When a draft sync request has an invalid upsert (missing id, body too long), it is rejected before any stored draft image is deleted. | Nightly review 2026-10-24 | active |
| BE-007 | When a response body is logged, draft text, profile names and pictures, MCP content, URL signatures and any non-JSON body are redacted. | Nightly review 2026-10-24 (GDPR) | active |
| BE-008 | When an MCP client with `insights:read` calls `list_insights`, it gets only the signed-in member's insights (filtered by `since` and `unused_only`); `mark_insight_used` with `insights:write` links an insight to that member's own draft. | Content engine plan, Kat 2026-10-30 | active |
| BE-009 | When a signed-in member starts a Telegram link in Bloomary, the bot links that Telegram only after the member taps the number Bloomary showed; webhook calls without the `X-Telegram-Bot-Api-Secret-Token` secret are rejected. | Content engine plan, Kat 2026-10-30 | active |
| BE-010 | When a linked member sends the bot text or a voice note, it is stored as an insight (voice transcribed by Deepgram, audio kept in memory only and never stored); an unlinked user is told to link Bloomary. | Content engine plan, Kat 2026-10-30 | active |
| BE-011 | When an MCP client with `drafts:write` calls `attach_image` with base64 data or a public https URL, a JPEG, PNG or GIF (type sniffed from the bytes, max 5 MB) is stored and linked to the member's unpublished draft, replacing its old image; URLs that resolve to non-public addresses are refused. | Content engine plan, Kat 2026-10-30 | active |
| BE-012 | When an MCP client with `insights:write` calls `send_suggestion` (kind `comment`), the linked member gets a Telegram message with the comment, an Open post button, a Copy button (comments up to 256 characters), rating buttons 1–5 and Posted; the suggestion, rating and posted flag are stored. | Content engine plan, Kat 2026-10-30 | active |
| BE-013 | When an MCP client schedules a draft with `schedule_draft`, it publishes at its time; `notify_scheduled` sends the linked member a Telegram preview (text, image, time) with an Edit link and a Skip button, and Skip cancels the schedule unless the post is already going out. | Content engine plan, Kat 2026-10-30 (no approvals) | active |
| BE-014 | When an MCP client with `insights:read` calls `list_feedback`, it gets each notified post with its outcome (scheduled, published, skipped, failed, ...), the text sent and the current text with a word diff of the member's edits, and comment suggestions with ratings and posted flags. | Content engine plan, Kat 2026-10-30 | active |
| BE-015 | When a member deletes their account or disconnects LinkedIn, their insights, Telegram link, scheduled-post notices and comment suggestions are deleted too. | GDPR, content engine plan 2026-10-30 | active |
<!-- Add rows like:
| BE-001 | When <condition>, <system> <observable result>. | <issue / spec link> | active |
-->
