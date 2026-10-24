# Requirements — Bloomary backend

Format and evidence rules: [CONTRIBUTING.md](CONTRIBUTING.md). Check: `scripts/trace-check.sh REQUIREMENTS.md .`.

| ID | Requirement (When X, Y.) | Source | Status |
|---|---|---|---|
| BE-001 | When a member publishes a draft manually with its `draftId`, that draft's pending, retry or needs-review schedule is cancelled, so the worker never posts it twice. | Kat, 2026-10-22 | active |
| BE-002 | When a member sends `DELETE /api/linkedin/schedule?draftId=…`, that draft's pending, retry or needs-review schedule is cancelled and leaves the active schedule list. | Kat, 2026-10-22 | active |
| BE-003 | When no custom root pool is configured, StoreKit JWS chains are trusted only if they end at Apple Root CA - G3, so a leaf from any other public CA is rejected. | Nightly review 2026-10-24 | active |
| BE-004 | When a scheduled post hits the LinkedIn daily limit, the job is marked failed at once instead of being retried. | Nightly review 2026-10-24 | active |
| BE-005 | When a post is published or feedback is sent, `posts_publish_total` and `feedback_total` grow by one (recorded by the gate only, not again by the story). | Nightly review 2026-10-24 | active |
<!-- Add rows like:
| BE-001 | When <condition>, <system> <observable result>. | <issue / spec link> | active |
-->
