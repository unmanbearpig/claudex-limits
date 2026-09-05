# codex-limits

`codex-limits` is a small standalone Go command that reads the signed-in Codex
account's rate limits and prints the percentage left in each quota window. It
can print one snapshot or keep a four-hour chart in a terminal. History stays
in memory for the current run.

The repository is local and has no published releases or download URL yet.

## Quick start

For a source build, install Go 1.26 or newer and the Codex CLI. From this
checkout, build and install the binary first:

```sh
make build VERSION=dev
mkdir -p "$HOME/.local/bin"
install -m 0755 codex-limits "$HOME/.local/bin/codex-limits"
codex login
codex-limits
codex-limits --live
```

Ensure `~/.local/bin` is on your `PATH`. The Go compiler is only needed for this local build. There is no published
binary yet. A future release archive will run without Go after unpacking.

To inspect the display without an account or network access, run the synthetic
preview:

```sh
codex-limits --demo --live
```

The heading labels this data as `DEMO (synthetic)`. A preview looks like this
after the first frame:

```text
  CODEX LIMITS · DEMO (synthetic)   ·   LIVE   ·   0.2s refresh   ·   23:07:20
  LAST 4 HOURS   ·   0–100% left   ·   newest at right
      ┌───────────────────────────────────────────────────────────────────────────────────────────────────────────────┐
 100% │┈┈┈┈┈┈┈┈┈┈┈┈┈┈┈┈┈┈┈┈┈┈┈┈┈┈┈┈┈┈┈┈┈┈┈┈┈┈┈┈┈┈┈┈┈┈┈┈┈┈┈┈┈┈┈┈┈┈┈┈┈┈┈┈┈┈┈┈┈┈┈┈┈┈┈┈┈┈┈┈┈┈┈┈┈┈┈┈┈┈┈┈┈┈┈┈┈┈┈┈┈┈┈┈┈┈┈┈┈┈┈│
      │                            ┊                          ┊                           ┊                           │
      │                            ┊                          ┊                           ┊                           │
      │                            ┊                          ┊                           ┊                           │
      │                            ┊                          ┊                           ┊                           │
  75% │┈┈┈┈┈┈┈┈┈┈┈┈┈┈┈┈┈┈┈┈┈┈┈┈┈┈┈┈┈┈┈┈┈┈┈┈┈┈┈┈┈┈┈┈┈┈┈┈┈┈┈┈┈┈┈┈┈┈┈┈┈┈┈┈┈┈┈┈┈┈┈┈┈┈┈┈┈┈┈┈┈┈┈┈┈┈┈┈┈┈┈┈┈┈┈┈┈┈┈┈┈┈┈┈┈┈┈┈┈┈┈│
      │                         ⣀⡠⠔⠒⠒⠒⠒⠢⠤⠤⠤⠤⣀⣀⣀⣀⣀⣀⣀⣀⣀         ┊               ⣀⡠⠔⠒⠒⠒⠒⠢⠤⠤⠤⠤⣀⣀⣀⣀⣀⣀⣀⣀⣀                   │
      │         ⠤⠤⠤⠤⠤⣀⣀⣀⣀⡀ ⣀⡠⠔⠒⠉   ┊                 ⠉⠉⠉⠉⠑⠒⠒⠒⠒⠤⠤⠤⠤⠤⣀⣀⣀⣀⡀ ⣀⡠⠔⠒⠉            ┊        ⠉⠉⠉⠉⠉⠒⠒⠒⠒⠢        ⠈│
      │                  ⠈⠉        ┊                          ┊        ⠈⠉                 ┊                           │
      │                            ┊                          ┊                           ┊                           │
  50% │┈┈┈┈┈┈┈┈┈┈┈┈┈┈┈┈┈┈┈┈┈┈┈┈⢀⣀⠤⠔⠒⠒⠒⠒⠒⠒⠒⠒⠒⠤⠤⠤⠤⢄⣀⣀⣀⣀┈┈┈┈┈┈┈┈┈┈┈┈┈┈┈┈┈┈┈┈┈┈┈┈⢀⣀⠤⠔⠒⠒⠒⠒⠒⠒⠒⠒⠒⠤⠤⠤⠤⢄⣀⣀⣀⣀┈┈┈┈┈┈┈┈┈┈┈┈┈┈┈┈┈┈┈│
      │         ⠒⠒⠒⠒⠒⠤⠤⠤⠤⢄⣀⠤⠔⠒⠉⠁   ┊                 ⠉⠉⠉⠉⠉⠉⠉⠉⠉⠒⠒⠒⠒⠒⠤⠤⠤⠤⢄⣀⠤⠔⠒⠉⠁            ┊        ⠉⠉⠉⠉⠉⠉⠉⠉⠉⠑        ⠈│
      │                            ┊                          ┊                           ┊                           │
      │                            ┊                          ┊                           ┊                           │
  25% │┈┈┈┈┈┈┈┈┈┈┈┈┈┈┈┈┈┈┈┈┈┈┈┈┈┈┈┈┈┈┈┈┈┈┈┈┈┈┈┈┈┈┈┈┈┈┈┈┈┈┈┈┈┈┈┈┈┈┈┈┈┈┈┈┈┈┈┈┈┈┈┈┈┈┈┈┈┈┈┈┈┈┈┈┈┈┈┈┈┈┈┈┈┈┈┈┈┈┈┈┈┈┈┈┈┈┈┈┈┈┈│
      │                            ┊                          ┊                           ┊                           │
      │                            ┊                          ┊                           ┊                           │
      │                            ┊                          ┊                           ┊                           │
      │                            ┊                          ┊                           ┊                           │
   0% │┈┈┈┈┈┈┈┈┈┈┈┈┈┈┈┈┈┈┈┈┈┈┈┈┈┈┈┈┈┈┈┈┈┈┈┈┈┈┈┈┈┈┈┈┈┈┈┈┈┈┈┈┈┈┈┈┈┈┈┈┈┈┈┈┈┈┈┈┈┈┈┈┈┈┈┈┈┈┈┈┈┈┈┈┈┈┈┈┈┈┈┈┈┈┈┈┈┈┈┈┈┈┈┈┈┈┈┈┈┈┈│
      └───────────────────────────────────────────────────────────────────────────────────────────────────────────────┘
       19:07:20                                           21:07:20                                            23:07:20
  ━━  5h   64.4% left  ·  resets in 1h 59m
  ━━  Weekly   44.8% left  ·  resets in 3d 23h 59m
  Banked resets 2
  Ctrl+C quit  ·  history from this run  ·  gaps = missed refreshes
```

The live chart uses Braille cells, colors each quota line separately, places
the newest sample on the right, and keeps missed refreshes as visible gaps.
The horizontal axis always spans four hours. Resize the terminal to change the
chart width on Linux and macOS. Windows uses `COLUMNS` and `LINES` when set,
or an 80×24 fallback; use a Unicode/ANSI-capable terminal. Windows binaries
have been cross-built, but their terminal behavior has not been tested live.
Ctrl+C restores the cursor and the previous terminal screen.

## Flags

```text
--json                 print one JSON snapshot and exit
--live                 refresh the chart until Ctrl+C
--interval SECONDS     live refresh interval, default 5
--source auto|codex|proxy
                       choose account discovery mode, default auto
--auth-file PATH       use exactly this native or proxy OAuth file
--demo                 use synthetic data, without login or network
--version              print the build version
--help                 print flag help
```

`--interval` must be positive and finite, and only works with `--live`.
`--json` and `--live` cannot be combined. `--auth-file` cannot be combined
with `--source codex` or `--source proxy`; an explicit file already chooses the
account. `--demo` cannot be combined with an account source.

With `--source auto`, the command uses `codex app-server` when the installed
Codex CLI is available. If the app server reports that no ChatGPT account is
logged in, the command checks the native OAuth file and then the proxy OAuth
directory. A transient failure from an authenticated app server is reported
instead of silently switching accounts. `--source codex` never falls back.

The native file is `${CODEX_HOME}/auth.json` when `CODEX_HOME` is set, or
`~/.codex/auth.json`; the Codex CLI may also keep its credentials in the
platform keychain, which the app server reads for the monitor. The proxy
fallback selects the most recently modified active `codex-*.json` under
`~/.config/cliproxyapi/auth`. `--auth-file` accepts
the native nested fields `tokens.access_token` and `tokens.account_id`, and the
flat proxy fields `access_token` and `account_id`. The command reads these
files and never refreshes or edits them. A saved API key is not a ChatGPT
OAuth account, so use `codex login` for the app-server source.

## JSON output

Named fields preserve the original command's format:

- `five_hour_remaining_percent` and `weekly_remaining_percent` are percentages
  left. Their matching `*_reset` objects contain `after_seconds`, local `at`,
  and Unix `timestamp`.
- `additional_rate_limits` contains the named five-hour and weekly windows of
  other buckets.
- `banked_resets.available` is authoritative when present. `applicable` is
  included when reported. `expirations` contains known available credit
  expirations. `expiration_details_partial` means the account reported more
  credits than it described in detail.
- `windows` contains every recognized window, including 15-minute, hourly,
  and other durations. Each item has `name`, `remaining_percent` when known,
  `window_seconds` when known, and an optional `reset` object. Unknown values
  are omitted. An empty list means the account reported no quota windows; the
  command does not turn that into 100% or unlimited.
- `plan_type` is descriptive and may contain a plan value the command has not
  seen before. It does not decide which windows are supported.

Missing percentages remain missing. A failed live refresh keeps the last
successful values on screen, reports them as stale, and adds a gap without
inventing a sample. Chart history is retained by age for four hours, without a
sample-count cap, and is discarded when the process exits.

## Build and install

The downloaded executable needs no Python, npm, proxy service, or Go toolchain.
For a local build and install:

```sh
make test
make vet
make build VERSION=dev
mkdir -p "$HOME/.local/bin"
install -m 0755 codex-limits "$HOME/.local/bin/codex-limits"
```

Building from source requires Go 1.26 or newer. A release archive contains the
static executable and this README, so a recipient only needs to unpack it and
place the executable on `PATH`. No release archive has been published yet.

`make release VERSION=0.1.0` cross-builds static archives for Linux amd64 and
arm64, macOS amd64 and arm64, and Windows amd64. It writes the archives and a
`SHA256SUMS` file under `dist/`. Cross-builds verify compilation only. A
normal local build is the runtime test for this machine.

The release command uses `CGO_ENABLED=0`. The chart uses standard-library
terminal detection and has no native library dependency.

## Troubleshooting

- `no ChatGPT account is available`: run `codex login`, then retry. You can
  also pass a known OAuth file with `--auth-file PATH`.
- `ChatGPT rejected the saved OAuth token`: make a Codex request so the Codex
  CLI can refresh its own credential, then retry.
- `No quota windows reported`: the account response was valid but did not
  include recognized quota values. Check the account and try again later.
- A redirected `--live` stream has plain text frames and no ANSI controls.
  `NO_COLOR=1` disables color. `TERM=dumb` also disables the alternate screen.

## Development checks

```sh
gofmt -w *.go
go test ./...
go vet ./...
go test -race ./...
make release VERSION=dev
```

The tests cover native and proxy credential shapes, generic and multi-bucket
normalization, missing data, optional reset details, legacy HTTP responses,
chart orientation and gaps, history retention, and CLI validation. The real
Codex app-server request is read-only: initialization, account reads, and rate
limit reads. It never starts a thread or turn and never consumes a banked
reset.

## Account interface references

- [Codex authentication and credential storage](https://developers.openai.com/codex/auth/)
- [Codex app-server account API](https://developers.openai.com/codex/app-server/)
