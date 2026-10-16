# Contributing to Bloomary

Thanks for helping. Bloomary is open source under AGPL-3.0 and maintained by DailyMind LTD.

## Use AI agents

We encourage it. Claude Code, Codex, Cursor, Copilot or any other agent: point it at this file first. You stay responsible for what you submit: read the diff, run the tests, and be ready to explain every line.

## Verification: prove the claim, not "tests are green"

Green tests alone say nothing about whether the code is right. A test counts only when it proves a stated requirement. We borrow this from requirements-based testing in safety-critical software (DO-178C): every change traces requirement → code → test → result.

Every pull request description contains this trace:

| Requirement | Issue link, or one sentence: "When X, the app does Y" |
|---|---|
| Code | Files and functions changed |
| Evidence | Test name(s) that check exactly this requirement, and their output. Bug fix: the test fails without your change and passes with it |
| Not verified | What you did not or could not check (device, platform, edge case) |

Rules:

1. Write tests from the requirement, not from the implementation.
2. A test that only checks a mock returns what the mock was told to return proves nothing; don't count it as evidence.
3. Every requirement in the PR has at least one test in Evidence.
4. Reviewers check the trace first, then the code.

Ask your agent: "Restate the requirement before coding. When done, give the trace table with real test output."

## Before you open a pull request

1. **Sign the CLA.** On your first pull request a bot asks you to sign the Contributor License Agreement. We can't merge without it.
2. **One change per pull request.** Small, focused diffs get reviewed fastest.
3. **Tests pass.** Run the test command from the README.
4. **No secrets.** Never commit tokens, keys or `.env` files.

## How changes get merged

Open a pull request from your fork against `main`. A maintainer reviews it; only maintainers can merge.

## Security issues

Do not open a public issue. Email info@dailymind.eu.
