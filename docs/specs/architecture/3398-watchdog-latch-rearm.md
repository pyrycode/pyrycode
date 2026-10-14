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

## Experiment and validation results

Executed on Linux with the production source at starting revision `96b56597`,
after the separate plan commit `226b90f0`. The fixture retains the experiment as
two subtests. Real ticks remain 7.5 ms; fake idle is 60 ms. All following times
are fake elapsed times from construction:

| Sequence | Missed window | Observed window |
| --- | --- | --- |
| Arm at 0; evaluate first poll at 120 ms | One callback | One callback |
| Complete three more polls at 120 ms | Zero further callbacks | Zero further callbacks |
| Write system activity at 120 ms while poll holds old snapshot | Owed turn remains set | Owed turn remains set |
| Process held old snapshot | At 240 ms: still stalling | At 120 ms: still stalling |
| Process snapshot containing activity | At 240 ms: still stalling | At 120 ms: non-stalling; re-arms |
| Poll at 240 ms and three subsequent polls | Zero new callbacks | One callback, then zero further callbacks |

The initial red experiment expected a second callback even in the missed-window
arm: `go test -race -count=1 -run '^TestWatchdog_LatchReArms$' -v ./internal/streamsup`
failed there with `callbacks=0, want 1` in 0.05 s; the observed-window arm passed.
The final fixture instead asserts the predicted absence in the missed-window
control. This resolves the first open question: delaying the poll can reproduce
the symptom, while observing non-stalling activity distinguishes the successful
schedule. It does **not** establish what the historical gate's scheduler did at
`220ed01d`; that exact historical failure was not reproduced.

For repeatable negative controls, temporary Go overlays mapped the absolute
original file path to a modified copy under `/tmp/builder-3398/`, with JSON shape
`{"Replace": {"<original absolute path>": "<modified absolute path>"}}`.
Each overlay changes only the following item, leaving the worktree untouched:

| Overlay name | Modification | Observed failure |
| --- | --- | --- |
| `missed-expects-second` | In the test, initialize `want` to 1 rather than 0 before `if observeActivity` | Missed-window arm: second idle callbacks 0, want 1; 0.05 s |
| `suppress-second-callback` | Add a subtest-local counter; increment in `OnStall` and return when it equals 2 | Observed-window arm: second idle callbacks 0, want 1; 0.08 s |
| `stuck-latch` | In a copy of `watchdog.go`, remove `fired = false` from `Watchdog.Start` | Observed-window arm: second idle callbacks 0, want 1; 0.08 s |
| `per-tick` | In a copy of `watchdog.go`, change `if fired` to `if false && fired` | Both arms: first continuous stall callbacks 1, want 0; 0.02 s each |

Command for each control (substitute its name for `<name>`):
`go test -race -count=1 -timeout=15s -overlay=/tmp/builder-3398/<name>.json -run '^TestWatchdog_LatchReArms$' -json ./internal/streamsup`.
All exited 1 due to the expected assertions, with no race reports or skips.
Each ran three test/subtest records: one pass/two failures including the parent
for the first three controls; zero passes/three failures for per-tick emission.
Callback suppression failed after the poll-completion barrier, within the
declared two-second bound on each synchronization wait (at most four seconds
across release and acknowledgement). Fatal cleanup cancelled and joined the
watchdog even though its next poll was held at the clock gate.

| Validation command | RUN / PASS / FAIL / SKIP records |
| --- | --- |
| `go test -race -count=20 -run '^TestWatchdog_LatchReArms$' -json ./internal/streamsup` | Target: 20 / 20 / 0 / 0; including both subtests: 60 / 60 / 0 / 0 |
| `go test -race -count=1 -json ./internal/streamsup/...` | Including subtests: 1708 / 1707 / 0 / 1; top-level: 510 / 509 / 0 / 1; package passed in 19.130 s |

The sole suite skip was `TestAccountTokenReadDeadline`: “full production token
deadline: run make test-slow”. The target and both schedules passed without skips
in the complete suite. `go vet ./...`,
`go build -o /tmp/builder-3398/pyry ./cmd/pyry`, and `git diff --check` passed.
The verifier's full-module gate and #3399's aggregate offline gate remain pending
for their owning stages. No live check or documentation change is required.
