# Requirements — Bloomary backend

Format and evidence rules: [CONTRIBUTING.md](CONTRIBUTING.md). Check: `scripts/trace-check.sh REQUIREMENTS.md .`.

| ID | Requirement (When X, Y.) | Source | Status |
|---|---|---|---|
| BE-001 | When a member publishes a draft manually with its `draftId`, that draft's pending, retry or needs-review schedule is cancelled, so the worker never posts it twice. | Kat, 2026-10-22 | active |
| BE-002 | When a member sends `DELETE /api/linkedin/schedule?draftId=…`, that draft's pending, retry or needs-review schedule is cancelled and leaves the active schedule list. | Kat, 2026-10-22 | active |
<!-- Add rows like:
| BE-001 | When <condition>, <system> <observable result>. | <issue / spec link> | active |
-->
