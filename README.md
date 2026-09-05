# codex-limits

A terminal view of your Codex account limits, including five-hour and weekly
allowances, additional model limits, and banked resets.

## Usage

```bash
codex-limits                       # Current remaining allowances
codex-limits --json                # Machine-readable snapshot
codex-limits --live                # Live chart, refreshed every 5 seconds
codex-limits --live --interval 2   # Refresh every 2 seconds
```

The live chart shows percentage **left** on the vertical axis and a rolling
**four-hour** time window on the horizontal axis. New samples appear at the
right edge. History is collected in memory during the current run, so the
chart starts empty and does not survive a restart.

Colored Unicode Braille lines provide finer detail than ordinary text cells.
The chart adjusts to the terminal size. Failed refreshes leave gaps and retry
automatically. Press Ctrl+C to exit and restore the terminal. Set `NO_COLOR=1`
to disable color.

`--live` and `--json` are mutually exclusive. `--interval` requires `--live`.

## Requirements

- Python 3.10 or later, using only the standard library.
- A terminal with Unicode support for the live chart.
- An existing active Codex OAuth file in `~/.config/cliproxyapi/auth/codex-*.json`,
  containing `access_token` and `account_id`. The most recently modified active
  file is selected. Credentials stay outside this repository.

The command reads the ChatGPT account usage and reset-credit endpoints. If the
saved OAuth token is rejected, run a Codex request through the existing login
setup to refresh it.

## Local installation

```bash
chmod +x codex-limits
mkdir -p ~/.local/bin
ln -s "$PWD/codex-limits" ~/.local/bin/codex-limits
```

Run these commands from the repository. If `~/.local/bin/codex-limits` already
exists, move it aside before creating the link. Ensure `~/.local/bin` is on
your `PATH`. With the symlink installed, repository edits update the command
directly.
