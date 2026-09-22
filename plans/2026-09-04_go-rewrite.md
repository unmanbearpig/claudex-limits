# Go rewrite of codex-limits

Completed in September 2026. This is a historical design record; the README
describes the current behavior and supported platforms.

## Why?

The project moved from Python to Go so users could run a downloaded executable
without installing a language runtime. The rewrite also made account discovery
work with a normal Codex login instead of requiring a proxy setup.

## Design decisions

- Use the Go standard library and ship executables built with `CGO_ENABLED=0`.
- Prefer the Codex app-server account API. Keep one child process for repeated
  reads, bound request waits, and stop the child on interruption or exit.
- Read existing OAuth files as a fallback. Let Codex handle login and token
  refresh; never start a model turn or consume a banked reset.
- Preserve text and JSON snapshots. Normalize quota windows by their reported
  durations so new account plans do not require hard-coded rules.
- Keep a four-hour terminal chart with percentage remaining on the vertical
  axis. Show missed refreshes as gaps and label stale values.
- Offer a synthetic demo that needs no account or network connection.

The original rewrite kept history in memory. A later change added local
history files so recent readings survive restarts.

## Verification

The rewrite passed unit tests, vet, and race checks on Linux. Fake app-server
processes covered protocol calls, repeated reads, interruption, and cleanup.
Fixtures covered account formats, quota windows, missing values, and optional
reset details. Live Linux checks covered account reads, terminal resizing,
chart rendering, and cursor restoration.

Other platforms were cross-built rather than tested on real machines. Current
release targets are Linux and macOS on amd64 and arm64. Windows is unsupported.
