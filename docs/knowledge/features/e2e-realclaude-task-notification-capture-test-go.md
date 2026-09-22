## task_notification_capture_test.go (#2247)

The live half of the fourth background-task subtype's capture: releases a FIFO **mid-test**
(reusing `tpcapHoldFIFO` from
[`tool_progress_capture_test.go`](e2e-realclaude-tool-progress-capture-test-go.md)) so a
backgrounded `cat` sees EOF and the background task reaches a terminal state, because
`task_notification` only fires when a background task completes, fails or is stopped — a state the
earlier `dropped_lines_v2.1.220.json` capture could never reach with its FIFO release pinned to
`t.Cleanup` alone.

### A companion subtype's "wait for `resultSeen`" is the wrong wait for this one

The first draft reused the existing companion wait, which returns as soon as
`recorder.resultSeen` closes — correct for a line the assistant emits inside its own turn, wrong
here because a background task by construction outlives the turn that started it. `resultSeen` is
a `sync.Once` latch, so once fired the wait collapses to an immediate `snapshot()` regardless of
the bound requested. The consequence was not a missed line but a false verdict: the staging
classifier would report "nothing arrived in the seconds that followed" when those seconds were
never spent, reading as a finding about the surface when it was really an untried wait. Any future
probe waiting on an event that fires *after* backgrounding needs its own bounded poll keyed on
`recorder.snapshot()` that does not treat `resultSeen` as terminal.

### Prose describing a deny-scan's coverage can fail the scan it describes

`dropcapScanner`'s deny-scan is fail-closed: a hit means the whole record is refused, nothing
written, no partial capture. A record field whose *subject is the scan itself* — documenting which
path-prefix classes it covers — is bytes going through that same scan, so spelling out the literal
needles inline trips every one of them at once and silently discards a real capture after a
multi-minute live turn that spent real tokens. `dropcapRedactor`'s substitution table doesn't save
this: it replaces known operator-controlled paths (the temp `$HOME`, the workdir, the FIFO), not
narrative text explaining the scanner's own literals. The fix is to name the mechanism by symbol
(`dropcapFixedNeedles`) in any prose destined for a committed record and never quote what it
searches for, backed by a deterministic offline test that scans the record's own rig-authored
constants — not a synthetic fixture — against the real needle list. An offline suite that only
ever exercises synthetic records will not catch this class at all.

### Every blob actually written must be scanned as itself

A record assembled in stages (measured fields filled in after an initial marshal — e.g. staging
detail appended once `git add` has run) must be re-scanned after each marshal that changes its
bytes. Scanning an earlier marshalling and assuming a later-appended field inherits that clearance
is false, and the gap is exactly the class of string a declared substitution table has no entry
for (a `git` error naming a real repository path). Relatedly, staging must run **after** the
fixture file is written, not before: staging first fails with "pathspec did not match any files"
on precisely the run this family of probe exists to promote — a first capture, when the fixture is
absent by design.

### Fixture status

Landed: `testdata/task_notification_v2.1.259.json` is committed and
`taskNotificationPinnedKeys` in `internal/streamsup/task_notification_capture_test.go` is filled.
The committed run itself stalled — `turn_seconds: 360.0`, `terminated_on: "budget"` — because the
FIFO it held was released under a still-**foreground** call, which never returns its tool result
even after the write side closes; #2525's `roster_after_finish_capture_test.go` diagnosed that
retroactively as the same defect a naive `task_started`-triggered release reproduces, not a
property of this claude version. See
[the roster-after-finish capture](e2e-realclaude-roster-after-finish-capture-test-go.md#a-foreground-held-fifo-never-returns-its-tool-result-even-after-the-command-exits)
for the fix (stage the call as backgrounded, wait for `is_backgrounded:true` before releasing).
See [the compaction capture's history](e2e-realclaude-compaction-capture-test-go.md) for what can
happen even after a run does fire clean: a gate-only run in a throwaway worktree can still lose the
fixture, and the recovery pattern used there (an opportunistic commit riding an unrelated ticket) is
worth reusing rather than reinventing if a future capture lands the same way.

### Related

- [`tool_progress_capture_test.go`](e2e-realclaude-tool-progress-capture-test-go.md) — `tpcapHoldFIFO`, reused here for the mid-test FIFO release.
- [`compaction_capture_test.go`](e2e-realclaude-compaction-capture-test-go.md) — the fixture-lost-after-a-green-gate pattern and its recovery.
- [`roster_after_finish_capture_test.go`](e2e-realclaude-roster-after-finish-capture-test-go.md) — diagnosed this capture's own 360s turn stall as a foreground-held-FIFO defect, not a claude property.
- [streamsup's per-subtype map](streamsup-package-system-maps-per-subtype-since-2026-08-07.md) — the mapping this capture's bytes are for (#2245), not yet written.
