# Claude quota interface

Researched on 2026-09-29. The monitor needs the subscription allowance shown by
Claude Code's `/usage` command. Counts of tokens in local conversation logs cannot
reconstruct that allowance, and Anthropic API request limits describe separate
API billing and throughput.

## Account endpoint

Existing monitors use an authenticated, read-only request:

```http
GET https://api.anthropic.com/api/oauth/usage
Authorization: Bearer <saved Claude Code OAuth access token>
anthropic-beta: oauth-2025-04-20
Accept: application/json
User-Agent: claude-code/<installed CLI version>
```

The endpoint path is also present in the installed Claude Code 2.1.285 binary.
No model request, browser session cookie, or paid API call is needed. It is not a
published API contract. Official documentation and support pages returned HTTP
403 from the development environment, so the implementation details below rely
on inspectable quota-monitor code and fixtures.

The older response has `five_hour`, `seven_day`, and optional `seven_day_*`
objects with `utilization` and `resets_at`. Utilization is the percentage used;
the monitor displays `100 - utilization`. Null fields are unavailable, not
unlimited. Reset timestamps may contain fractional seconds and UTC offsets.

Newer responses also have a `limits` array with `kind`, `group`, `percent`,
`resets_at`, and `scope.model.display_name` or other scope details. Model limits
can be present even when `is_active` is false. The monitor retains those limits,
deduplicates entries that also appear in the older fields, and keeps unknown
durations unset. Unnamed scoped entries cannot replace the account total.

`extra_usage` describes an optional monthly spending cap. Its `monthly_limit`
and `used_credits` use the same provider units, generally minor currency units.
A remaining percentage can be calculated for a positive cap. A null or zero cap
does not establish a remaining allowance, and the response does not establish a
monthly reset timestamp.

## Credentials and polling

Linux Claude Code credentials use `claudeAiOauth.accessToken` inside
`~/.claude/.credentials.json`, with optional expiry in milliseconds, scopes,
and subscription type. `CLAUDE_CONFIG_DIR` selects another configuration.
macOS uses Claude Code's Keychain storage. The monitor reads the selected service
and account with `/usr/bin/security find-generic-password`, matching Claude Code.
macOS may ask for Keychain access if that executable is not already authorized.
It reads credentials only and leaves token refresh to Claude Code. CLIProxyAPI's
flat Claude OAuth files are also accepted.

A macOS investigation on 2026-10-01 found that the original JXA reader built an
empty query because Security's Core Foundation constants need conversion with
`ObjC.castRefToObject`. The query returned `errSecParam`, which the monitor
misreported as requiring interaction. After correcting the query, Keychain still
denied access to `osascript`, while Claude Code's `/usr/bin/security` reader
successfully read the same saved login. Claude Code's access does not authorize
`osascript`, so the monitor now uses Claude Code's reader instead.

A token with a known scope list must include `user:profile`. Inference-only
tokens, including those created by `claude setup-token`, cannot read plan usage.
OAuth credentials are kept out of snapshots, history, and error messages.

Claude Code's changelog describes sharing plan-usage readings made in the last
minute and backing off after rate limits or rejected logins. The monitor follows
that cadence in combined mode and respects `Retry-After` after HTTP 429. It
reports authentication failures instead of refreshing another application's
token or silently switching to a different saved account.

## Sources and verification limits

- [CodexBar OAuth usage fetcher and response types](https://github.com/steipete/CodexBar/blob/main/Sources/CodexBarCore/Providers/Claude/ClaudeOAuth/ClaudeOAuthUsageFetcher.swift)
- [CodexBar credential schema and profile-scope diagnostics](https://github.com/steipete/CodexBar/blob/main/Sources/CodexBarCore/Providers/Claude/ClaudeOAuth/ClaudeOAuthCredentialModels.swift)
- [CodexBar usage fixtures, including the newer scoped limits array](https://github.com/steipete/CodexBar/blob/main/Tests/CodexBarTests/ClaudeOAuthTests.swift)
- [Claude Code changelog](https://github.com/anthropics/claude-code/blob/main/CHANGELOG.md)
- [Published Claude Code 2.1.20 source, including configuration-scoped Keychain service names](https://cdn.jsdelivr.net/npm/@anthropic-ai/claude-code@2.1.20/cli.js)
- [Claude Code authentication documentation](https://code.claude.com/docs/en/authentication)
- [Claude Code cost and usage documentation](https://code.claude.com/docs/en/costs)

The offline tests cover credential discovery and rotation, both response shapes,
missing values, errors without secret disclosure, retry delays, cancellation,
combined snapshots, persisted history, and Claude's orange chart line. An
authenticated live endpoint check passed on Linux with five-hour, weekly, and
additional provider windows. On 2026-10-01, a macOS check with Claude Code 2.1.286
and a saved Pro login passed through `/usr/bin/security`, returning five-hour
and weekly quota windows.
