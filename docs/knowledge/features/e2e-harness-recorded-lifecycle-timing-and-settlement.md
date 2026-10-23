# Recorded lifecycle timing and settlement

Diagnostic lifetime follows the broker handler, including failure cleanup.
`recordedLifecycleDiagnostic` exclusively creates the retained 0600 file and
attaches it to the command. A non-nil `io.Closer` returned by the launch callback
transfers to `LifecycleBrokerServer.handle`, even with a callback error or an
invalid command. The handler closes it exactly once after its final use, through
`Cmd.Start` and `Cmd.Wait` when a child starts, before signaling handler completion.
Closure is best effort and preserves the original launch or child verdict.

Registering diagnostic closure in a launch callback's `t.Cleanup` makes LIFO
failure cleanup close the file before the earlier server join. Moving that
closure after bounded `Join` is still insufficient: a callback can outlast both
join waits. The callback retains the file until return, then the handler owns
closure; if cancellation already began, the abort guard prevents starting the
late command. `Join` cancels only actual server-owned children and its timeout
remains a failure, never permission to close an in-flight handler's diagnostic.
Kernel-peer, seven-pipe and allowed-job validation, terminal receipts and artifact
audits remain separate requirements. See the
[synchronized failure-cleanup proof](development-verification-prove-that-tests-distinguish-the-change.md#broker-diagnostic-failure-cleanup).

Private artifact validation must precede reading. An `Lstat` followed by an
ordinary open leaves a replacement window: a FIFO can wait for a writer, and a
symlink can expose target bytes before a later identity rejection. Inode equality
also accepts a symlink back to the inspected original. `runnerPrivateRead` opens
with `O_NOFOLLOW|O_NONBLOCK`, then checks the descriptor is regular, has mode
exactly 0600 (including absence of special mode bits), fits the caller's size
limit and matches the inspected inode before consuming bytes. Rejected
descriptors are closed; accepted reads retain the limit-plus-one bound and
content-free error contract. Parent-directory ownership and final artifact
audits remain separate requirements. See the
[read-ordering controls](development-verification-prove-that-tests-distinguish-the-change.md).

`TestRecordedClaude` runs the original scenario assertions through recorded
transport. `RunLifecycleBroker` keeps real input pipes and owned processes in
replay. Strict `Player.Input` matching releases responses independently of the
stand-in's stdin lifetime. Writing a matched final query into that pipe before
delivering its responses can lose every released response to EPIPE after owned
predecessor exit, even with an exhausted player. Capture retains bounded input
forwarding and pipe-error rejection; replay still invokes its input observer
through `forwardLifecycleFrame`'s exactly-once callback contract.

Startup request order is a capture timing outcome. In replay, initialize
completion alone does not mean the player expects its first user input: an
asynchronous `mcp_status` request may still precede it. `ReplayTurnReady`
signals the first `Player.AwaitsTurn` boundary; the send-file driver waits for
its private one-shot marker before triggering input. Keep strict frame matching
and the original modal, tool-reference and stored-attachment assertions.
`TestRecordedMCPSendFileWaitsForStartup` delays initialize to expose this ordering
without changing capture or live Claude behavior.

Response delivery, action acknowledgement, player exhaustion, terminal receipts
and predecessor disappearance are separate observations. For idle resume,
receipt publication may precede daemon reaping; see the
[resume boundary](e2e-realclaude-interactive-stream-resume-after-eviction-tes.md#resume-snapshot-boundary).
Kill/rotation/eviction completion requires authorized
owned termination, both provider output EOFs, complete strict input with no
partial frames, terminal checks and receipts, and the outer artifact audit.
Both EOFs can precede a final query, so an incomplete replay waits within the
existing deadline while original input remains open. Input EOF with missing
frames still fails. `TestLifecycleReplayAfterProviderExit` kills and reaps its
owned child before the final query, checks every released response and holds
stdout and stderr open separately to reject early completion. Its live-exhausted
arm also proves that action acknowledgement and player exhaustion cannot replace
real teardown; changed/partial/missing input, foreign/missing actions and failed
terminal checks remain rejection controls. This is offline replay proof; see the
[rotation contract](../../testing/daemon-recordings.md#session-rotation-and-native-transcript-snapshots).

The readiness origin is established after startup validation; a causal
user/input epoch is recorded after capture forwarding or strict replay matching
and its input observer, before completion observation and `OnInput` witness work.
Subsequent outputs reuse their recorded
anchor and absolute offset. Re-anchoring an existing phase, compressing gaps or
retiming a fixture would erase the timing assertion.

`waitLifecycleOutput` checks the absolute due time before and after waiting;
`forwardLifecycleOutput` checks again after forwarding. All three retain the
100 ms allowance, and the actual pipe write deadline is capped at due + 100 ms.
`forwardPreparedLifecycleOutput` restores immutable bytes with `DaemonBinding.Frame`
before waiting. Waiting first wastes the captured gap and puts preparation
inside the forwarding allowance. Physical `BeforeOutput` observation, replay
projection and actual forwarding stay at delivery; moving physical observation
early would change the schedule. `safeCLIJSON` retains duplicate-key and privacy
validation before physical parsing without a redundant outer duplicate-key scan.
`TestComposerOutputPreparationSchedule` gates real restoration of the reviewed
Composer response and charges 120 ms against its 34.33 ms offset: wait-first
misses due + 100 ms, while prepare-first fits. No observer or write runs early
or after preparation failure; preparation overrun still rejects. See the
[controlled proof and historical attribution limit](../../testing/daemon-recordings.md#physical-background-task-boundary).
Late input, scheduler overshoot and final-write backpressure must still fail.
`TestLifecycleTimelineUsesAbsoluteDueTimes`,
`TestLifecycleTimelineRejectsLateFinalForward`,
`TestLifecycleInputTimelineBindsActualPrecedingUser` and
`TestLifecycleInputTimelineDoesNotResetAnExistingUserPhase` cover these boundaries
and stale/future anchors.

For the two conversation scenarios, `NewConversationCompletionObserver` collects
every successfully forwarded frame in capture and replay, but calls
`completedConversationExchange` only at a stdout `result` before publication.
The cheap result-header check only admits full validation: failed, malformed,
incomplete or foreign sources cannot authorize a receipt. Revalidating the whole
growing prefix between streaming frames can consume the captured gaps even when
the absolute waits are correct. `TestConversationCompletionPreservesOutputSchedule`
charges 35 ms per real derivation against a 20 ms cadence in both modes and
independently validates the resulting receipt.

Reply arrival alone does not establish terminal settlement.
`CheckConversationSettlement` still requires the owned completion and worker
evidence before sealing; `ValidateConversationCompletion` independently derives
the receipt prefix and final tape, and final ownership, source and I/O audits
remain required. See [timing receipt propagation](e2e-realclaude-test-infrastructure.md#recorded-lifecycle-failure-receipts)
and the [measured Linux diagnosis](development-verification-test-execution-and-artifact-survival.md#diagnosing-recorded-lifecycle-output-timing).

On Linux and macOS, session-resume and Composer Stop launch ownership must reach
the actual outer-owned invocation.
Checking a helper against a daemon PID supplied by a control file accepted an
unrelated daemon's otherwise qualifying helper even with a real SIGKILL/Wait.
`StartFallbackLaunchAudit` binds the command immediately after Start;
`FallbackLaunchAudit.ownDaemon` retains and rechecks the complete current kernel
ancestry to the outer process before observing children. PID, start identity,
parent and group matter together: an unknown chain or changed identity cannot
authorize a cancelled launch. `TestFallbackCancelledLaunchOwnership` synchronizes
owned and unrelated process trees before real cancellation; macOS uses
`TestDarwinLaunchOwnership` for the same boundary.

The macOS sampler uses bounded libproc queries through cgo plus kernel sysctls.
It checks the actual executable path and mapped vnode, closed helper/worker argv
and private cwd vnode, then rechecks worker and ancestry identities. Unreadable,
dying or missed observations remain unknown; changed retained identities invalidate
the audit. Without cgo, native queries remain unknown and cannot authorize a
cancellation. `proc_listchildpids` takes buffer bytes but returns a PID count;
treating that result as bytes silently discards launches despite passing static
checks. `TestDarwinCancelledLaunch` holds a real helper before registration until
the normal sampler observes it, so a broken adapter cannot pass through direct
post-sampler observations alone.

Production can complete a cancellation Wait before the helper registers a job.
`ReadLifecycleWorkers` accepts that boundary on Linux and macOS for session-resume
and Composer Stop only with the independent current-launch audit, observed absent
registration, a completed production SIGKILL Wait with group cancellation, and
process/group death. Every Wait joins once;
cancelled launches consume the two-attempt allowance but add no native exchange.
`TestFallbackCancelledLaunchBoundary` synchronizes a current owned helper before
registration, then cancels and reaps it; one launch and one Wait with zero
registered workers must pass only with the independent audit. Wait-only, unknown,
duplicate, foreign and unmatched evidence still fails. Exact reviewed exchange
cardinality, registered-worker source/peer checks and complete literal native
I/O/EOF remain required. The `live-group` control in `TestDarwinLaunchOwnership`
reaps the worker while retaining another owned group member: rejecting only a
live worker would leave the group-death check unproved. See the
[recording contract](../../testing/daemon-recordings.md#idle-eviction-and-literal-native-resume-inputs),
[Composer Stop boundary](../../testing/daemon-recordings.md#physical-background-task-boundary)
and [post-stop evidence retention](e2e-realclaude-test-infrastructure.md#recorded-lifecycle-failure-receipts).

Failure cleanup must reach the actual owning daemon before its sole worker Wait.
A reported worker PID can be reused after reaping even while the outer command
remains alive. `testStartLaunchOwner` closes its control pipe so
`testRunLaunchDaemon` cancels its owned worker group before reaping; it never
signals a reported PID.
`TestDarwinLaunchCleanup` expires evidence reads before cancellation and after
reaping, requiring bounded cleanup of an owned surviving group member while an
unrelated live group remains untouched.

A `total-wait-cardinality` rejection identifies a failed inventory join, but
cannot establish cancellation before registration once the original home and
counts are gone. Preserve `FallbackLaunchAudit.Save` diagnostics in an external
private `PYRY_CLAUDE_TAPE_EVIDENCE` directory: current launch identities,
registration states, registered owners and actual Wait flags/statuses distinguish
unknown from absent without copying worker content. A passing replay with zero
launches cannot explain an earlier mismatch. Synchronized controls prove the
accounting boundary; offline replay establishes no new live-provider behavior.

Recover the original failure stage before selecting a repair. A forwarding miss
before inventory cannot be diagnosed as a failed worker join. `LifecycleDiagnostic`
retains four output-stage maxima with ordinals and at most 64 numeric timing
samples, including process CPU elapsed (`-1` means unknown). CPU includes other
broker goroutines; neither it nor passing replays can attribute an older miss
without substage evidence. Diagnostics confer no completion authority.
