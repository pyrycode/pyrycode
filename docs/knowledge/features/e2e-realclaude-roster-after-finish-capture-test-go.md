## roster_after_finish_capture_test.go (#2525)

The live capture answering the question desktop #1246 and desktop #1558 assumed opposite
answers to: does claude send a trailing `system/background_tasks_changed` after a backgrounded
task completes, or does the count only come down if a client clears on the status event instead?
Reuses #2247's start-then-finish staging (`tpcapHoldFIFO`, `tpcapCensus`, `bgIdlePrompt`, the
`dropcap*` machinery — called, not forked) and adds both `initialize` ask positions the repo's
prior evidence turns on: production's per-spawn ask (`RequestInitializeOnSpawn`) and a
mid-session `RequestInitialize`.

**Measured on claude 2.1.280**
(`testdata/roster_after_finish_v2.1.280.json`): verdict `roster-omits-the-finished-task`, prompt
`quiet-window` — an empty roster arrives **unprompted**, not in answer to any control request.
`task_updated`'s `patch` carries the terminal status first; `task_notification` repeats it one
line later. The per-spawn `initialize` ask is not followed by a roster; the mid-session ask is
answered by a second, identical empty roster. This is transcribed into
`emitBackgroundTaskRoster`'s doc comment (`internal/streamsup/parser.go`) and is the fact the
handoff cross-posts to pyrycode-desktop#1246 and #1558.

### `task_started` is not the backgrounding signal

The first live attempt released the held FIFO as soon as `system/task_started` appeared. At
2.1.280 that line fires about three seconds into what is still a **foreground** call, carrying
`is_backgrounded:false` — well before `BASH_DEFAULT_TIMEOUT_MS` would have moved anything.
Releasing on it let the command finish in the foreground, off every roster, so the run measured
nothing about the surface it was built to observe. The prompt now explicitly asks for
`run_in_background`, and the release waits for a `task_started` or `task_updated` that actually
reports `is_backgrounded:true` before it lets go. A probe staging a background task cannot treat
the task's opening line as proof the task is backgrounded — only a line that says so.

### A foreground-held FIFO never returns its tool result, even after the command exits

Releasing early had a second, compounding failure: a **foreground** `cat` of a FIFO the rig holds
does not return its tool result once the write side closes — the first turn never ends, and the
follow-on turn queues behind it until the whole run budget is spent.
[`task_notification_v2.1.259.json`](e2e-realclaude-task-notification-capture-test-go.md) records
the identical stall (`turn_seconds: 360.0`, `terminated_on: "budget"`), now understood
retroactively as the same defect rather than a property of that capture's claude version. Any
probe in this family that holds a FIFO under a foreground call and expects the turn to close on
release should stage the call as backgrounded first.

### Ordering a roster against the terminal-status line discards the one line the ticket needs

The original design judged every `background_tasks_changed` line by its position relative to the
line carrying the terminal status (`status: completed`). At 2.1.280 the empty, task-omitting
roster arrives **one line before** the `task_updated` that carries `status: completed` — so a
rule reading "after the terminal-status line" as its window would have discarded the single
roster line this whole ticket exists to find, and reported `none-after-terminal-status` off a
capture that actually measured the opposite. Rosters are judged from the FIFO **release** point
instead (`after_release`), which precedes both the terminal-status line and the roster by
construction; `offset_lines` against the terminal-status line is still recorded for reference, but
no longer decides what counts as "after." A capture design that orders evidence against one
observed line's position should treat that position as a fact to record, not as a boundary to
filter on, unless the two are independently known to be ordered the same way on every release.

### An absence-shaped capture needs its promotion rule keyed on the staging, not on the quarry firing

Every prior probe in this family refuses to promote a record holding zero quarry frames, treating
that as a vacuous run. Here the plausible real answer *is* zero roster lines, so copying that rule
verbatim would have shipped a probe that could only ever fail — no capture of "claude never sends
this line" could ever satisfy a gate built to reject exactly that shape. `fixtureWorthy` instead
promotes on whether the **staging** succeeded (a task was backgrounded, it reached a terminal
status, the quiet window was spent to its floor, a further turn was read) regardless of whether any
roster fired. A capture probe measuring for an absence needs this distinction made explicit before
the first live run, not discovered after one comes back empty and gets refused as broken.

### Related

- [`tool_progress_capture_test.go`](e2e-realclaude-tool-progress-capture-test-go.md) — `tpcapHoldFIFO`, `tpcapCensus`, reused here.
- [`task_notification_capture_test.go`](e2e-realclaude-task-notification-capture-test-go.md) — the sibling capture whose own stalled turn is the same foreground-FIFO defect, diagnosed here.
- [streamsup's per-subtype map](streamsup-package-system-maps-per-subtype-since-2026-08-07.md) — `emitBackgroundTaskRoster`'s doc comment now carries this measurement.
