# Go rewrite of codex-limits

Status: WIP. High-level plan committed as `07ab416`; implementation details added afterward.

## Why?

The user wants Go because Python is not good for distributing this tool. They
also want a polished repository and README, and setup that works on another
computer with a different Codex subscription. Replacing the Python runtime
dependency is required for the requested distribution approach. No production
urgency was given.

The user explicitly requested this order: write a high-level plan, commit it,
add implementation details, have Luna at xhigh implement it, and review the
result. The primary agent must not implement the rewrite itself.

## Outcome

Ship a standalone Go executable for codex-limits with the current terminal
experience: percentage remaining, a rolling four-hour Unicode chart, and a
five-second default refresh. Keep text and JSON snapshots and optional reset
details. Support the Codex account logged in on the user's computer without
hard-coding a subscription or this machine's proxy setup.

## Work

1. Define the CLI behavior, portable authentication approach, and distribution
   expectations in implementation details after this plan is committed.
2. Delegate the Go implementation, automated checks, and README to Luna at
   xhigh. Preserve the installed Python command until the replacement passes
   review.
3. Review Luna's result for correctness, account handling, terminal behavior,
   subprocess cleanup, and build portability. Send defects back to Luna.
4. Validate the reviewed binary, merge and commit the completed work, and
   switch the installed command to the Go executable.

## Validation

Use offline fixtures for different account responses, credential formats, and
quota windows. Check the live chart and clean exit in a terminal. Build
standalone binaries for the documented target platforms, and verify a real
read-only account request on this machine. Do not claim other accounts or
operating systems were tested live when they were only simulated or compiled.

## Workspace

- Repository: `/home/unmbp/projects/stuff/codex-limits`
- Worktree: `/home/unmbp/projects/stuff/socialspots/tmp/codex-limits-go`
- Branch: `feature/go-cli`, based on `main`
- Reference implementation: the committed Python CLI and the unfinished
  portability changes in the separate `codex-limits-portable` worktree.
- No Git remote exists yet; publishing releases is outside this task.

## Implementation details

### Ownership and structure

- Luna, model `gpt-5.6-luna` with reasoning effort `xhigh`, owns all Go code,
  tests, build scripts, and implementation fixes. The primary agent writes the
  plan, reviews changes, runs independent verification, and integrates them.
- Work only in the Go worktree. The Python portability worktree is unfinished
  reference material, not a dependency or a finished design to copy blindly.
- Use a small Go module with a CLI entry point and separable account fetching,
  snapshot normalization, and terminal rendering. A root command or
  `cmd/codex-limits` is fine. Keep package boundaries proportionate to the tool.
- Prefer the standard library. A small maintained dependency such as
  `golang.org/x/term` is reasonable for portable terminal sizing and detection.
  Pin dependencies and commit `go.sum` if dependencies are used.
- Remove the tracked Python executable when the Go replacement is complete.
  Generated binaries and local credential/cache files must stay ignored.

### CLI contract

- No arguments prints the current remaining allowances and reset information.
- `--json` prints one JSON snapshot and no progress text on stdout.
- `--live` shows the Unicode terminal dashboard, refreshed every five seconds.
- `--interval SECONDS` accepts positive finite seconds only with `--live`.
- Keep a fixed four-hour horizontal axis from the first frame, 0–100 percent
  remaining vertically, and the newest sample at the right. Real history starts
  empty and remains in memory only. Retain samples by age, not a 120-sample cap.
- Preserve distinct colored Braille lines, readable time ticks, remaining
  percentages, reset countdowns, adaptive sizing, and optional banked resets.
- Use an alternate screen for interactive terminals; restore the screen and
  cursor on Ctrl+C, termination, errors, and normal exit. Keep redirected
  output free of ANSI controls; honor `NO_COLOR` and `TERM=dumb`.
- Failed reads must not fabricate samples or connect lines across a missing
  refresh. Clearly identify stale values and retry without a tight loop.
- Retain `--source auto|codex|proxy` and `--auth-file PATH` from the portability
  work. Reject conflicting source options and `--json --live`.
- Add `--demo`, usable with snapshots and live mode, for a clearly marked
  synthetic preview that needs no login or network. A prefilled demo chart is
  useful for immediately seeing the layout.
- Include `--help` and a useful `--version` for distributed binaries.

### Authentication and account data

Use the installed Codex CLI's documented app-server interface as the default
when available. Codex owns file/keychain credentials, `CODEX_HOME`, and token
refresh; the monitor must not implement login or spend model tokens.

One persistent `codex app-server` child per connection should perform:

1. An `initialize` request with client metadata, then `initialized` notification.
2. `account/read` with `refreshToken: true`, checking for a ChatGPT account.
3. Repeated `account/rateLimits/read` calls for snapshots.

Only these read-only methods are needed. Do not start threads/turns, consume
banked resets, or send account notifications. Ignore asynchronous notifications
while matching response IDs. Bound startup and request waits. Reap the exact
child process on failure or exit, with no leftover readers or subprocesses.
Do not expose raw credential or RPC payloads in error messages.

With automatic selection, a missing CLI or an explicit no-ChatGPT-login result
may use existing saved credentials. A transient failure on an authenticated
Codex account must not silently switch to a different proxy account.
`--source codex` must not fall back. `--source proxy` deliberately selects the
most recently modified active `codex-*.json` under
`~/.config/cliproxyapi/auth`. `--auth-file` selects exactly that file, supporting
both native nested `tokens.access_token` / `tokens.account_id` and proxy flat
`access_token` / `account_id` fields. If Codex CLI is unavailable, support the
native file under `CODEX_HOME` or `~/.codex` before trying proxy credentials.
Give actionable login guidance when only an API key or no account exists.

The existing direct-file fallback uses read-only ChatGPT account endpoints.
Treat its reset-credit details endpoint as optional, so absent/unsupported
credits never hide otherwise valid limits. Do not implement custom OAuth token
refresh or mutate credential files.

Normalize app-server fields without subscription-specific assumptions:

- `rateLimits` is the legacy primary bucket; `rateLimitsByLimitId` is the
  multi-bucket map. Avoid duplicating the primary bucket.
- `primary` / `secondary` contain `usedPercent`, `windowDurationMins`, and
  optional `resetsAt`. Convert to percentage remaining. Display all returned
  durations, including 15-minute or hourly windows, not only 5h and weekly.
- `planType` is optional descriptive data, not a switch for supported plans.
- `rateLimitResetCredits` and its `credits` detail rows may be absent, null,
  incomplete, or capped. `availableCount` is authoritative. Missing expiration
  details are not a failed snapshot.
- An account with no reported quota windows is unknown/unavailable, not 100%
  remaining or unlimited. Missing percentages must not become zero by default.
- Preserve established five-hour/weekly JSON fields where present. Add a
  documented generic windows list for other durations; omit unknown values.

Official documentation inspected during this task:

- https://developers.openai.com/codex/auth/
- https://developers.openai.com/codex/app-server/

The current machine was verified against the unfinished portability code:
Codex reported no ChatGPT login, and automatic selection successfully used its
existing proxy OAuth file and returned three quota windows. No other account
or OS has been tested live.

### Distribution and README

- Produce standalone binaries with `CGO_ENABLED=0` for Linux amd64/arm64,
  macOS amd64/arm64, and Windows amd64 if the implementation supports that
  platform. Document actual supported targets and limitations honestly.
- Provide repeatable local build/test/install and release-archive commands.
  Include version information and checksums in local release artifacts.
  No Python, npm, proxy service, or Go toolchain should be needed to run a
  downloaded binary. A normal Codex CLI login is the recommended prerequisite.
- Document that the repository is currently local and releases have not been
  published. Do not invent a GitHub owner, remote URL, or downloadable release.
- Rewrite README around the Go binary: what it does, a visual preview or clear
  example, quick start on a new machine, authentication choices, every flag,
  JSON fields, history behavior, troubleshooting, building, and validation.
- Include `--demo --live` as the fastest way to inspect the display without
  credentials. Clearly label any synthetic image or example.
- Leave the current installed command untouched during implementation. The
  primary agent will switch the existing `~/.local/bin/codex-limits` symlink
  after review. Keep a rollback copy of the Python command.

### Acceptance checks

- `gofmt`, `go vet ./...`, and meaningful Go tests; use the race detector on
  the host where available.
- Fixture coverage for native/proxy credentials, explicit-account precedence,
  missing login, API-key-only login, optional/null/partial reset credits,
  multiple buckets and durations, empty quotas, and malformed responses.
- A fake app-server subprocess should verify initialization, request IDs,
  notification handling, timeout/error cleanup, and repeated reads without
  spawning a new process per refresh. It must reject any non-read-only method.
- Check four-hour timestamps, remaining-percentage orientation, missing-data
  gaps, retention beyond four hours, narrow/wide layouts, redirected output,
  retry behavior, and terminal cleanup. Verify `--demo` makes no requests.
- Build the documented release targets. A cross-build is not a runtime test.
- Primary review must run the host binary, check at least two real live
  refreshes and Ctrl+C in a PTY, and confirm the installed command resolves to
  the reviewed Go build. Do not print credentials during verification.
- Commit implementation with a detailed message referencing this plan, but do
  not mark it completed before review and acceptance checks pass. Send review
  defects to Luna rather than having the primary agent implement fixes.
