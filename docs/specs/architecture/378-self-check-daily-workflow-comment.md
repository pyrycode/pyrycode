# #378 — Refresh self-check-daily workflow header comment

## Files to read first

- `.github/workflows/self-check-daily.yml` (whole file, ~35 lines) — the only file
  edited. Lines 3–12 are the stale header comment to be rewritten; the rest of
  the file is the load-bearing workflow definition and must stay untouched
  unless `workflow_dispatch` (see § Verification) reveals a real mismatch.
- `cmd/pyry/agent_run_selfcheck.go` (lines 16–24, 71–89) — the operator-facing
  prose the new comment must align with. In particular: the FAIL message
  identifies the contract as `--allowed-tools "Read"
  --dangerously-skip-permissions` in stream-json mode (line 75), and
  references read `#329 (Phase A spike), #336 (predecessor, superseded), #375
  (this rewrite)` (lines 87–88). The new workflow comment should use the same
  reference shape.
- `internal/agentrun/selfcheck/selfcheck.go` (lines 1–13, 22–28, 59–69) —
  the package-level contract statement and the two sentinels (`ErrBashInvoked`,
  `ErrTimeout`) that drive the exit-code mapping. Confirms the verified
  property is "no `tool_use` event with name==\"Bash\" appears in claude's
  stream-json stdout".

No QMD / lessons / decisions lookup needed — this is a stale-prose fix scoped
to one comment block. The reference state lives entirely in the three files
above.

## Context

`.github/workflows/self-check-daily.yml` runs `pyry agent-run --self-check` on
a daily cron. #375 (closed) rewrote what the self-check verifies: the
load-bearing property changed from "`permissions.defaultMode: \"deny\"` in a
per-spawn settings file refuses Bash in PTY-interactive mode" to "claude
launched with `--allowed-tools \"Read\" --dangerously-skip-permissions` refuses
Bash, observed via stream-json stdout". The workflow's `run:` step is
unchanged (`./pyry agent-run --self-check`), but its header comment still
describes the pre-#375 property and points at #336 alone. A future on-call
debugging a red badge will read that stale prose first.

This ticket fixes the comment. There is no production code change.

## Design

### The new header comment

Replace lines 3–12 (the existing `# Belt-and-suspenders …` block, terminating
at the blank line before `on:`) with prose that satisfies AC #1, #2, and #3:

- States the load-bearing property in post-#375 terms: claude launched with
  `--allowed-tools "Read" --dangerously-skip-permissions` must refuse Bash,
  observed via stream-json stdout (no `tool_use` event with name=="Bash").
- Drops all references to `permissions.defaultMode: "deny"`, "per-spawn"
  settings, and "interactive mode".
- Updates the issue reference to lead with #375 (the current rewrite) and
  retain #336 as "superseded by #375" — mirrors the reference shape already
  used in `agent_run_selfcheck.go:87-88`.
- Preserves the existing operator-affordance sentence ("exits 0 on PASS,
  non-zero on FAIL or inconclusive; operator monitors the badge").

Length target: comparable to the current 10-line block; no expansion beyond
~12 lines. The comment is signage, not documentation.

### Out of scope

The workflow's behavioural surface is unchanged unless the AC #5
`workflow_dispatch` run on the branch surfaces a concrete problem:

- `run:` step stays `./pyry agent-run --self-check`.
- `env:` stays `ANTHROPIC_API_KEY` only.
- `timeout-minutes: 5` stays — `defaultSelfCheckTimeout` in selfcheck.go is
  90s, so 5min of job budget covers checkout + setup-go + `npm install -g
  @anthropic-ai/claude-code` + `go build` + the self-check itself with
  generous slack.
- Cron schedule (`13 6 * * *`) stays.
- Go version (`1.26.x`) and the `actions/checkout@v6` / `actions/setup-go@v6`
  pins stay.

If `workflow_dispatch` reveals a real surface mismatch (e.g., the self-check
needs a flag the workflow doesn't pass, or 5min is now tight), fix it in the
same PR per AC #6. Worst case "no functional change needed" is the expected
outcome per the ticket's own technical notes.

## Concurrency model

N/A — workflow file edit.

## Error handling

N/A — workflow file edit. The self-check's exit-code contract (PASS=0,
FAIL/inconclusive non-zero) is already wired through `runAgentRunSelfCheck`
in `cmd/pyry/agent_run_selfcheck.go:25-66` and is not in scope.

## Testing strategy / Verification

This is a comment edit; there is no Go test to write. Verification is via the
`workflow_dispatch` trigger that the workflow already declares (line 19):

1. Push the branch (the dispatcher's safety-net does this automatically after
   the developer commits).
2. Trigger via `gh workflow run "agent-run self-check (daily)" --ref
   feature/378`.
3. Wait for the run to finish (`gh run watch <run-id>` or `gh run list
   --workflow="agent-run self-check (daily)" --limit 1`).
4. Confirm conclusion is `success` (the self-check itself returned PASS,
   non-zero exit on FAIL/inconclusive — see exit-code mapping in
   `agent_run_selfcheck.go:44-65`).

If the run is `success`, AC #5 is satisfied and the PR can be opened. If the
run fails on something other than the deny-default check itself (e.g., a
missing flag the post-#375 self-check needs), per AC #6 fix it in the same
PR; otherwise leave the workflow behaviour alone.

The `ANTHROPIC_API_KEY` repo secret already exists (the workflow is in
production), so `workflow_dispatch` on the branch will pick it up.

## Open questions

None. The technical notes call out the realistic "no functional change"
outcome as acceptable, and the post-#375 reference points
(`agent_run_selfcheck.go`, `selfcheck.go`) are concrete and pin the new
comment's wording.
