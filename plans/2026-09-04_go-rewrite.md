# Go rewrite of codex-limits

Status: WIP. High-level plan, before implementation details.

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
