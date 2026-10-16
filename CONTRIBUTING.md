# Contributing to Bloomary

| # | Item | Value |
|---|---|---|
| 1 | License | [AGPL-3.0-only](LICENSE) © DailyMind LTD |
| 2 | Contributor terms | [CLA](CLA.md): you assign copyright in your contribution to DailyMind LTD and get a licence back. Signed via the CLA bot on your first PR. |
| 3 | Merge | Maintainers only, after review ([CODEOWNERS](.github/CODEOWNERS)) |
| 4 | Test command | `go test ./...` |
| 5 | Trace check | `scripts/trace-check.sh REQUIREMENTS.md .` |
| 6 | Security reports | [SECURITY.md](SECURITY.md) — never a public issue |
| 7 | AI agents | Welcome. Agents read [AGENTS.md](AGENTS.md). You own every line you submit. |

## Open source: edits welcome — ask first

| # | Step |
|---|---|
| 1 | Search open issues and pull requests: someone may already be working on it. |
| 2 | Comment on the issue (or open one) with what you plan to do, and wait for a maintainer's go-ahead. |
| 3 | Typos and broken links: no need to ask, open the pull request. |
| 4 | One person per issue. A claimed issue with no update for 14 days is free again. |
| 5 | Not sure it's planned? Ask in the issue first; a "not planned" answer saves you the work. |

## Evidence rule

Passing tests alone prove nothing. A change is accepted when evidence proves each requirement it touches.

| # | Counts as evidence | Does not count |
|---|---|---|
| 1 | Test tagged `Verifies: <ID>` asserting the observable result | Test asserting a mock returns what it was given |
| 2 | Bug fix: test output failing on `main` and passing on the branch | "All tests green" with no requirement IDs |
| 3 | HTTP round-trip test through the real router (API changes) | Coverage % |
| 4 | Screenshot / recording with device + OS (UI with no automated test) | "Verified by AI" with no artifact |

## Steps

| # | Step |
|---|---|
| 1 | Find or add the requirement in [REQUIREMENTS.md](REQUIREMENTS.md): ID + `When X, Y.` |
| 2 | Make the smallest change that satisfies it. One requirement or bug per PR. |
| 3 | Add or update a test tagged `Verifies: <ID>` that fails if the requirement is false. |
| 4 | Run `go test ./...` and the trace check. Paste real output in the PR. |
| 5 | Fill the PR template trace. List what you did not verify. |
| 6 | Disclose AI use (tool + model) in the PR. |
| 7 | First PR: sign the CLA when the bot asks. |

## Never

| # | Rule |
|---|---|
| 1 | Commit secrets, tokens, `.env` files, server addresses |
| 2 | Delete or weaken a failing test to make CI green |
| 3 | Reformat files you did not otherwise change |
| 4 | Paste output you did not run |
