# Bloomary — API

[Bloomary](https://bloomary.life) helps you write LinkedIn posts that create opportunity. This repository is the API, built with Go.

Other parts: [backend](https://github.com/bloomary-life/backend) · [ios](https://github.com/bloomary-life/ios) · [desktop](https://github.com/bloomary-life/desktop) · [android](https://github.com/bloomary-life/android) · [platform](https://github.com/bloomary-life/platform) · [landing](https://github.com/bloomary-life/landing)

## Test

```sh
go test ./...
```

## Optional features

| # | Env var | Turns on |
|---|---|---|
| 1 | `BLOOMARY_APP_TOKEN`, `BLOOMARY_USER_BOT_WEBHOOK_SECRET`, `BLOOMARY_USER_BOT_USERNAME` (default `bloomary_app`) | Telegram bot (`POST /api/telegram/webhook`): insights, scheduled-post previews with Skip, comment ideas; off while the token is empty |
| 2 | `DEEPGRAM_API_KEY` | Voice notes to the bot are transcribed (audio is never stored) |

MCP tools (`/mcp`): `list_drafts`, `get_draft`, `create_draft`, `update_draft`, `attach_image`, `schedule_draft`, `notify_scheduled`, `notify_draft`, `list_edit_requests`, `mark_edit_applied`, `list_insights`, `mark_insight_used`, `send_suggestion`, `list_feedback`. Scopes: `drafts:read`, `drafts:write`, `insights:read`, `insights:write`.

## Contributing

Bloomary is open source and welcomes edits. Before you start, check open issues and pull requests and ask in an issue or in the [Bloomary community on Telegram](https://t.me/+NDQcpuABme9iYTU0) whether the change is planned or already taken. See [CONTRIBUTING.md](CONTRIBUTING.md). Contributors sign the [CLA](CLA.md) on their first pull request: copyright in contributions is assigned to DailyMind LTD.

## License

[AGPL-3.0-only](LICENSE) © 2026 DailyMind LTD. DailyMind LTD owns the copyright and also licenses Bloomary commercially (app stores, paid hosted service at bloomary.life). "Bloomary" and its logo are trademarks of DailyMind LTD and are not licensed: forks must use another name and logo. Details: [NOTICE](NOTICE).
