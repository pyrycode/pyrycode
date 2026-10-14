# Watchdog latch-rearm synchronization (#3398)

## Files read

- `internal/streamsup/watchdog.go` → `Watchdog.Start`, `stallTracker.snapshot`, `shouldFire`: snapshots precede the poll clock call; only a non-stalling poll clears the local latch.
- `internal/streamsup/watchdog_test.go` → `TestWatchdog_LatchReArms`, `fakeClock`, `recvStall`: the existing fixture assumes a poll occurs during 60 ms of recent activity.
- `docs/knowledge/features/streamsup-package.md`: owning package and watchdog topic map.
- `docs/knowledge/features/streamsup-package-idle-stall-watchdog-receive-side-emit-not-kill.md`: once-per-continuous-episode contract; system activity does not clear the owed turn.
- `docs/knowledge/features/development-verification.md` § Prove that tests distinguish the change: await producer completion before asserting absence; cancellation must release held gates before joining.
- `CODING-STYLE.md`: channel coordination, race checks, and contract comments.

## Context

The historical failure at `220ed01d` is an observation, not an established cause.
The source permits a delayed poll to miss the recent-activity window entirely.
Use a controlled scheduling experiment on starting revision
`96b56597b37fc7588214b8f6fbdbc77b0c8134ad` to distinguish that schedule from
one which observes activity. No production behavior or decision record changes.

One deliverable: repair this fixture's proof. Estimated written work is about
220 lines including this plan, zero exported types, zero consumer updates,
three acceptance criteria, and no production reject branches. All sizing limits
remain satisfied. Fetched feature branches have no overlap on the test file.

## Design

Keep all test code in `internal/streamsup/watchdog_test.go`. Use the existing
fake clock for tracker timestamps. After constructing the watchdog, install a
poll-only `WatchdogConfig.now` gate, before `Start`, returning the same fake
clock values. Each poll announces its arrival after taking its snapshot and
waits for a supplied timestamp. Arrival at the next gate proves that the prior
poll completed its predicate, latch update, and synchronous callback.

Exercise two schedules as subtests. Both arm a turn, advance beyond idle, and
check one callback followed by several completed polls without another callback.
Write a system line while a poll is held with its pre-activity snapshot. In the
missed-window control, advance beyond idle before releasing that poll: subsequent
polls remain stalling and no second callback is expected. In the repaired
schedule, release it without advancing, then complete a poll whose snapshot
includes the activity while time remains recent. Only then advance beyond idle,
expect one new callback, and prove several further polls emit nothing.

## Concurrency model

Only the existing watchdog goroutine is added. Unbuffered poll-arrival and clock
release channels establish ordering. Callback delivery and both clock-gate waits
select on cancellation. Test-side waits have a two-second bound. Cleanup always
cancels and calls `Wait`, including on fatal assertions. Install cleanup before
any assertion after `Start`.

## State transitions and identity reuse

| Event | Race-enabled proof |
| --- | --- |
| First owed-turn stall and repeated polls | `TestWatchdog_LatchReArms`, both schedules: exactly one callback |
| Activity window missed by delayed poll | `TestWatchdog_LatchReArms/poll_misses_activity_window`: latch stays set |
| Activity observed while turn remains owed, then second idle and repeated polls | `TestWatchdog_LatchReArms/poll_observes_activity_window`: exactly one new callback |
| Cancellation while a poll gate or callback is held, including fatal failure | Cleanup in both subtests; callback-suppression mutation exercises fatal cleanup |

## Error handling

Missing poll arrivals or clock-release progress fail within two seconds.
Missing or extra callbacks fail after the next poll's completion barrier, without
relying on a silence timeout. Cancellation releases every goroutine-owned gate.

## Testing strategy

First force the missed-window schedule while retaining the old expectation of a
second callback, and observe the failure. Retain both discriminating schedules
in the repaired fixture and record the experiment command and sequence below.
Run at least 20 race-enabled repetitions and one uncached complete streamsup
race suite, recording executed/passed/failed/skipped counts and skip reasons.
Use temporary test-file overlays outside the worktree to suppress the second
callback and to simulate a stuck latch and per-poll emission; require failures
within the declared bound. Run `go vet ./...` and `go build ./cmd/pyry` (with
output outside the worktree). The verifier owns the full-module gate; #3399 owns
the aggregate offline follow-up. No live-Claude check is required.

## Open questions

- Does the controlled schedule distinguish a missed window from observed
  non-stalling activity? Resolve through the experiment before handoff.
- Historical attribution remains limited unless that exact gate failure is
  reproduced; a passing rerun cannot resolve it.
