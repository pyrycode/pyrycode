# Idle/stall watchdog — receive-side, emit-not-kill (#1094), arms from the send side (#1504)

The production interactive Claude runner uses `streamsup.Watchdog` to report
silence while an assistant response is owed. `WatchdogConfig.Idle == 0` selects
240 seconds; the poll interval is `idle/8`, clamped to `[5ms, 5s]`. A poll fires
when owed silence exceeds Idle, once per contiguous stall episode. Detection
emits and never kills: `Watchdog` holds no process handle or cancellation
function. An approval can legitimately block Claude for minutes, so silence
alone neither warrants termination nor confirms an OS-permission cause.

```go
type WatchdogConfig struct {
    Idle              time.Duration               // 0 → 240s
    PendingPermission func() bool                 // nil → false; annotation only
    OnStall           func(pendingPermission bool) // onset callback; nil → no-op
    Logger            *slog.Logger                 // nil → slog.Default
}

func NewWatchdog(cfg WatchdogConfig) *Watchdog
func (w *Watchdog) Writer() io.Writer
func (w *Watchdog) TrackUserTurn(write func() error) error
func (w *Watchdog) UserTurnSent()
func (w *Watchdog) ChildExited()
func (w *Watchdog) Start(ctx context.Context)
func (w *Watchdog) Wait()
```

## Production ownership and Run reactivation

[`newStreamRunnerFactory`](streamsup-package-constructing-a-streamrunner-newstreamrunnerfacto.md)
constructs one watchdog per runner and sets
`scfg.Stdout = io.MultiWriter(watchdog.detector.Writer(), parser)`. The tracker
receives the same stdout bytes before the parser. `scfg.ControlParser = parser`
preserves control-response handling and parser cleanup that runner construction
could otherwise lose when the concrete parser is hidden by `io.MultiWriter`.
`Config.Stdout` stays fixed across child respawns, so constructing a watchdog per
child would leave the replacement disconnected from its tracker.

`streamRunner.Run` starts the poller with a derived context, drives the underlying
runner, then cancels and joins the poller with `Wait` before returning.
`Watchdog.Start` allocates a fresh completion channel on each start; callers must
cancel and join the previous lifetime first. This supports `Session.runActive`
reusing the same adapter after eviction/reactivation, with one poller during each
Run and the same tracker across child respawns and sequential Run calls.
Concurrent starts are outside this lifecycle contract.

## Arming, stdout progress and exit ordering

A fresh tracker starts not-awaiting. `system`/`init` is activity only, so a newly
spawned child can idle indefinitely without firing. Claude does not echo a
delivered prompt as a `user` line on this surface; stdout alone cannot arm a turn
that produces nothing. `streamRunner.WriteUserTurn` therefore wraps delivery in
`streamRunnerWatchdog.write`, which calls `Watchdog.TrackUserTurn`. Only a
successful write can arm owed silence and restart its clock. Refusals such as
`ErrNoLiveChild` and `turncommit.ErrDropped`, failed pipe writes and control
requests do not arm it; write errors retain their underlying meaning.

Successful delivery must preserve any response already consumed on stdout.
Calling `UserTurnSent` unconditionally after the write can overwrite an
`assistant` or completed `result` observed before the writer resumes, falsely
stalling a healthy tool wait or completed turn. `TrackUserTurn` snapshots the
tracker's owed-state revision before delivery and commits arming only if delivery
succeeds and that revision is unchanged. `assistant`, `result`, `user`,
`tool_result` and child exit advance the revision, even when awaiting itself does
not change. Activity-only lines do not advance it and cannot prevent a silent
turn from arming. `UserTurnSent` remains available for callers that already order
arming before stdout responses. No tracker lock spans the delivery callback or
stdin I/O.

The adapter also serializes deliveries and `ChildExited` with its own mutex.
Teardown closes stdin before invoking the exit callback, allowing a blocked write
to finish before reset acquires that mutex. Reset voids the departed child's owed
state and restarts the clock before the supervisor can spawn a replacement.
Without this ordering, a successful predecessor write racing exit could arm an
idle replacement. Existing stdio-request retirement, task cleanup and the
session-tagged exit sink remain chained after watchdog reset. See the
[concurrency revision](../../specs/architecture/3682-production-stream-watchdog.md#revisions).

## Type-aware silence and conversation-scoped publication

`stallTracker` reads only each complete line's top-level `type`, never event
content. Every complete line refreshes activity, including malformed or unknown
lines. `assistant` clears awaiting: subsequent tool silence is healthy.
`user`/`tool_result` makes another assistant response owed; `result` clears it.
Partial-line buffering remains bounded.

The factory's `OnStall` sends `turnevent.Stall{}` through the parser's decorated
session sink, using the same live `streamSessionTag` as other events. The
[fan-in and drain](streamsup-package-draining-turnevents-into-the-interactive-emitter-fan-in.md)
route it to the producing conversation's retained live state/control readings
and interactive `stall` publication, even when another conversation is active.
`RestartFresh` rotates that tag, so later stalls carry the replacement session's
identity. Stall onset leaves the child alive and does not clear turn-busy or
delivery state; normal response, result and exit handling retain their roles.
The control socket and other conversations remain usable.

At firing, `PendingPermission` reads the live tag and calls the composition
root's `streamPendingPermission`. It resolves `conversationForSession` and asks
`approvalParkedReport`, installed from `streamApprovalBridge.ApprovalParked`
before pool execution. An unbound session or missing report answers false;
another conversation's pending approval cannot annotate this stall. Both pending
and non-pending states emit. The boolean appears only as `pending_permission`
in the content-free diagnostic alongside `idle_seconds`; the production callback
publishes the unchanged empty `turnevent.Stall` variant. `protocol.StallPayload`
gains no approval or cause field. The watchdog neither resolves approvals nor
changes their verdicts or timeouts, and pending approval does not establish the
cause of the silence. Publication remains onset-only, with no recovery wire
event; another onset requires a poll to observe the stall condition clear.

## Production regression coverage

`TestStreamWatchdog_ProductionLifecycle` uses the real factory, child stdin/stdout,
fan-in, retained state and emitter to check onset, unchanged PID/restart count,
busy retention, resumed output, child-exit cleanup, an idle replacement,
non-active conversation attribution, rotation and same-adapter Run reactivation.
It also checks control-socket and another conversation's responsiveness.
`TestStreamWatchdog_Controls` covers failed/refused writes and assistant/tool
silence; `TestStreamWatchdog_ApprovalScope` checks own-versus-other approval
annotation after rotation and that the stall leaves the approval parked.
Existing approval tests continue to cover verdicts and timeouts.

`TestStreamWatchdog_WriteExitOrdering` pauses a successful real pipe write before
arming, overlaps child exit, then proves the replacement stays idle and a later
silent turn still stalls. `TestStreamWatchdog_ResponseBeforeArming` instead waits
for production assistant-delta or turn-end publication before releasing the
writer. Because tracking precedes parsing, that publication proves the response
already reached the tracker. Both tool-wait and completed-result cases must stay
quiet, then detect a later silent turn with the same child. Ordinary race tests
without the forced pause passed while this false-stall path was broken.
`TestWatchdog_TrackUserTurn` adds activity-only and user/tool-result controls.

These factory regressions use a private 200 ms threshold seam; production keeps
the 240-second default. The retained Linux evidence in
[PR #3689](https://github.com/pyrycode/pyrycode/pull/3689) identifies exact revisions,
selectors and executed/passed/failed/skipped counts, including the missing-wiring
failure and response-before-arming failure before their repairs. It does not
claim an elapsed production-threshold run, live-Claude behavior or an
OS-permission cause. macOS is outside this ticket's verification scope.

## Poll observation and latch-rearm testing

`Watchdog.Start` clears its local latch only when a poll evaluates `shouldFire`
as false. A system line updates activity while leaving the turn owed, but a
delayed poll can miss the entire recent-activity window and keep the latch set.
Extending a callback receive timeout or inspecting `stallTracker.snapshot` from
the test cannot prove that the polling goroutine re-armed.

The poll calls `stallTracker.snapshot` **before** `WatchdogConfig.now`.
`TestWatchdog_LatchReArms` gates only the poll's clock after construction, keeping
tracker writes on the same fake clock without blocking them. When activity is
written while a poll is held there, that poll already has the old snapshot.
Complete it, then complete a poll containing recent activity while a turn remains
owed, before advancing fake time past idle. Arrival at the next clock gate proves
the previous predicate, latch update and synchronous `OnStall` callback finished;
only then count callbacks or assert their absence. Repeated completed polls in
each stall episode distinguish once-per-episode emission from per-tick emission,
and the second callback distinguishes rearming from a stuck latch. Gate waits
are bounded; goroutine-side gates and callback delivery release on cancellation,
with cleanup cancelling before `Wait` even after fatal assertions. See
[test completion barriers](development-verification-prove-that-tests-distinguish-the-change.md#prove-that-tests-distinguish-the-change).

Retain the missed-window schedule as a control: advancing past idle before the
poll observes activity must produce no second signal. The
[controlled experiment](../../specs/architecture/3398-watchdog-latch-rearm.md#experiment-and-validation-results)
demonstrates this mechanism and distinguishes it from observed activity; it does
not establish the scheduler sequence behind the historical failure at `220ed01d`,
which was not reproduced.
