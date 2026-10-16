# AGENTS.md — Bloomary backend

Instructions for AI agents. Humans: [CONTRIBUTING.md](CONTRIBUTING.md); same rules.

## Repository

| # | Item | Value |
|---|---|---|
| 1 | What | Bloomary API (Go) |
| 2 | Stack | Go 1.25, SQLite, Paddle, Apple IAP, LinkedIn API |
| 3 | Test | `go test ./...` |
| 4 | Trace check | `scripts/trace-check.sh REQUIREMENTS.md .` |
| 5 | Requirements | [REQUIREMENTS.md](REQUIREMENTS.md), IDs `BE-NNN` |
| 6 | Related repos | backend, ios, desktop, android, platform, landing in github.com/bloomary-life |

## Hard rules

| # | Rule |
|---|---|
| 1 | Never invent facts, APIs, file paths or test results. Unknown → say so in the PR. |
| 2 | Before any work: search open issues and pull requests (`gh issue list`, `gh pr list`). If the task is taken, not planned, or has no maintainer go-ahead, stop and ask the human (go-ahead can come from an issue or the Bloomary Telegram community, https://t.me/+NDQcpuABme9iYTU0). |
| 3 | Before coding: restate the requirement as `When X, Y.` with its ID. |
| 4 | Smallest diff that satisfies the requirement. No drive-by refactors or reformatting. |
| 5 | Every behaviour change gets a test tagged `Verifies: <ID>` that fails if the requirement is false. |
| 6 | Never delete, skip or weaken a failing test to pass CI. |
| 7 | Run tests yourself; paste real output. Never claim a run you did not do. |
| 8 | No secrets, tokens, `.env`, server IPs or hostnames in code, docs or tests. |
| 9 | Do not edit `LICENSE`, `CONTRIBUTING.md`, `AGENTS.md`, `.github/` unless that is the task. |

## Done = PR contains

| # | Field | Content |
|---|---|---|
| 1 | Requirement | ID + sentence |
| 2 | Code | Files / functions |
| 3 | Evidence | Test names + output (bug fix: fail before, pass after) |
| 4 | Not verified | What and why |
| 5 | AI use | Tool + model |
