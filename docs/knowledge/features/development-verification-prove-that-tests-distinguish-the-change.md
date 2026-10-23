# Prove that tests distinguish the change

A real capture can already exercise an older matching branch. Check the test against
the pre-change implementation. Remove the older discriminator while preserving the
new one, and confirm that the input mutation actually changed the bytes.

For combined predicates, exercise each branch independently. A shared log site cannot
prove which branch was taken if both inputs satisfy the first condition. Use an
independent observable result of the path under test.

`reset.wrapup.write_failed` can mean the shared context expired before
`writeResetWrapUp` called the injected writer. Stale-note persistence consumes
that same budget before resolution. A matching event alone can therefore let
a delivery-error test pass without exercising its injected failure.
`TestResetThenRotate_FailedHandoffIsStale` checks resolution, dormant activation
and the first delivery attempt before expiry, and checks which callbacks ran.
Its idle case retains the busy state and observes the interrupt. Give immediate
failure classifications finite headroom separately from deliberate readiness,
idle and completion expiry; retain the stale bytes, warning/fence, secret
exclusion, bounded rotation and successful-recovery assertions. Permission-based
persistence skips must remain visible as skips.

A FIFO barrier proves that preceding queued work drained only after the
asynchronous producer has finished enqueueing the work being asserted. Join
producer completion before inserting the barrier, for both positive delivery
and zero-frame assertions. Receipt by one fanout recipient does not establish
that another recipient's frame was queued: a direct barrier push can interleave
with publication. `TestRelayAgentSwitchEncryptedFrames` awaits bounded adapter
completion, including fanout and `FlushPushes`, before the legacy-peer barrier
in both directions. Without that ordering, Claude restoration can falsely fail
and Codex withholding can falsely pass. See
[agent-switch handover testing](conversation-session-binding.md#testing-agent-switch-handover).

Shared relay output must preserve established peers' frames while awaiting a
new connection's handshake; consuming or reordering them can lose assertions or
break receive-nonce order. See the `switchWire.open` regression in
[agent-switch handover testing](conversation-session-binding.md#testing-agent-switch-handover).

Receiving the Noise handshake response establishes neither push-queue registration
nor supplied live-state snapshot capture: `V2SessionManager.handleNoiseInit` sends
the response before registering the queue and calling `ThreadLiveState`, while
`switchWire.open` returns after `ReadResp`. An immediate off-`Run` `Push` can
therefore return `ErrConnNotFound` even after successful Noise response receipt;
that alone does not prove an admitted connection was retired.

For readiness-dependent pushes after `open`, `testDaemonRelayAdmission` requests
one `ActiveConns` snapshot serialized on `Run` after the handshake finishes. It
requires the exact connection ID and checks context errors before and after the
snapshot, with a three-second upper bound or the caller's earlier cancellation.
`ActiveConns` returns no error, so cancellation must not be mistaken for an empty
successful snapshot. A missing ID fails immediately; no sleep or failed-push
retry establishes readiness. The snapshot does not prevent subsequent teardown.
See [relay concurrency](v2-session-manager-concurrency.md).

`TestDaemonRelayWireAdmissionBeforePush` publishes the successful `noise_resp`
then holds its outbound callback before queue registration. The raw wire helper
returns, and a diagnostic early push observes `ErrConnNotFound` while `Run` is
held and cannot perform teardown. Admission must expire during the hold, honor
an already-canceled caller, then succeed after release before envelope 20 is
pushed and decrypted with its exact type and payload. Removing the admission
check makes the held-deadline assertion fail. The hold selects on cancellation;
a deferred idempotent release precedes fixture cleanup joining `Run` and the
publisher. Keep wire reads and phone ciphers on the test thread. The sibling
`TestDaemonRelayWireQueuedPushDuringHandshake` preserves the four queued frames
for phone/legacy/phone/legacy, IDs 10–13, expected type and exact payload, before
checking delivery of envelope 20 to the newly admitted again.

This controlled ordering proves unfinished admission can cause the missing
queue; it does not attribute the historical intermittent failure. The original
scoped failure followed a passing run, and ten isolated repetitions subsequently
passed. Neither those passes nor the repaired tests' 20/20 race repetitions
identify the original scheduling. Retain contrary results and incidental process
overlap separately from controlled scheduling in the
[investigation evidence](../../specs/architecture/3483-queued-handshake-admission.md#investigation-evidence).

For supplied live-state replacement tests, synchronize admission and held
delivery on bounded signals from the snapshot callback itself. Otherwise a
valid initial reading can arrive before the replacement and falsely fail a
fresh-only assertion. Sleeping or discarding stale frames would obscure the
delivery proof. See the
[connect-gap regression](streamsup-package-draining-turnevents-into-the-interactive-emitter-daemon-retained-live-state.md#relay-delivery).

A retired-snapshot absence assertion must establish snapshot evaluation completion
before queueing its identified wire barrier: `drainOnce` lets queued pushes
overtake snapshot reads in `drainLiveOnce`, so a marker can otherwise arrive with
the cursor still unvisited. `TestDaemonLiveRelayRetiredSnapshot` uses bounded
handshake and capture gates, captures the real cursor before removing its owner,
then waits for cursor exhaustion and validates that the removed model reading
was visited. An empty cursor cannot prove stale-state rejection. Only then does
it verify the same phone remains admitted, push the trailing barrier and require
receipt on that phone of the decrypted barrier with its exact type, ID and
payload, rejecting every `model_announced` frame and unrelated output.
Preserve Noise receive order; sleeps or retrying `Push` until
success cannot establish these preconditions. See the
[retired-snapshot fixture plan](../../specs/architecture/3420-retired-snapshot-fixture.md).

A later frame on `Frames` cannot prove that `V2SessionManager.Run` consumed an
asynchronous result on a separate buffered channel: either ready select arm can
win. For a silent completion, await an observation emitted after the completion
handler's reply decision, then take a Run barrier before counting replies.
`TestV2Session_SwitchAgentLateOutcomes` waits for `v2.switch_agent.completed`
before opening its barrier connection; without that wait, a forbidden late
acknowledgement could arrive after the zero-reply assertion. To prove teardown
escapes a completion handoff, fill its channel first so sending cannot also win;
`TestV2Session_SwitchAgentHandoffEscapes` forces requester-close and manager-cancel
paths separately. See [relay concurrency](v2-session-manager-concurrency.md).

The same ordering trap applies to a withheld supplied live reply. A later
inbound frame cannot prove an asynchronous provider has queued its result.
`TestSuppliedLiveReplyWithheld` first observes the content-free
`v2.live_reply.queued` event, then pushes a permitted reading into that same
FIFO delivery queue and awaits its decrypted clear/fresh output. Only that
barrier proves the withheld predecessor passed delivery without emitting;
otherwise a forbidden late clear could arrive after a zero-frame assertion.

An already-cancelled context does not force an error when a `select` can also
observe completion: either ready case can win. Hold completion behind a
test-owned gate when proving the cancellation path. A runner gated during
teardown must be released before cleanup cancels and waits for its pool.
`t.Cleanup` runs in reverse registration order, so register the gate release
after the pool helper's cleanup; otherwise shutdown waits on a runner whose
release cannot run until that wait ends. See [session eviction fixtures](sessions-package-testing.md#cancellation-and-completed-eviction)
for the `gatedRunner` and `runPoolReady` example.

A custom cancellation fixture must propagate its declared error through derived
contexts. Overriding only `Err` on a wrapped cancel context can leave
`context.WithTimeout` using the underlying `context.Canceled` through Go's
cancel-context optimization. `testReplyFallbackContext` hides the wrapped
cancel-context value with `Value` returning nil and returns the triggered cause
from `Err`, so derived contexts observe `context.DeadlineExceeded` in the
deadline case. This test-only wrapper lets the stimulus follow child readiness;
retain a real timer-driven deadline case separately. See
[reply fallback lifecycle evidence](streamsup-package-draining-turnevents-into-the-interactive-emitter-native-reply-suggestions.md#native-reply-suggestions-after-the-result-2831).

A `context.WithCancel` stand-in cannot prove OS-signal shutdown classification:
`signal.NotifyContext` can record a signal-specific cause instead of
`context.Canceled`. Send real SIGTERM and SIGINT in isolated helper subprocesses,
since signal handlers are process-wide. Exercise both an operator-only stop and
a fatal cause recorded before the signal, asserting that the fatal sentinel
survives. `TestFatalCauseSignals` covers these four cases: removing signal-cause
matching fails the operator cases, while replacing it with `sigCtx.Err() != nil`
fails the fatal-first cases. Either half alone leaves the other bug undetected.
`TestRelay_OperatorSignalExitsZero` also signals actual daemons connected to the
fake relay, asserting exit 0, INFO `pyrycode stopped`, and absence of
`pyrycode fatal shutdown`; a control-plane `pyry stop` test never exercises the
signal parent's cause. See [daemon shutdown and exit status](cli-verb-dispatch.md#daemon-shutdown-and-exit-status).

Read the consumer above a mapper. A pure payload mapper does not decide whether a
frame is emitted. A whole message family can be ignored before subtype handling, so
absence of an error frame cannot prove which subtype branch ran. Child stdout,
client-visible frames and internal turn state are separate observations.

For an event specified as lifecycle-neutral, test both an open turn and idle state.
An open-turn assertion can prove the event did not transition or close that turn but
still stays green if the handler opens a new turn from idle. The idle case must assert
the lifecycle fields and that no synthetic lifecycle frame was emitted.

Confidentiality assertions do not prove lifecycle neutrality. A frame can reveal
no thinking text yet incorrectly publish `turn_state: thinking` for a subagent.
`TestStreamTurnDrainV2_AttributedTextExcludesThinkingAndSignature` also pins the
allowed envelope types: its earlier expectation admitted the unwanted thinking
frame while every secrecy assertion passed. Check frame types/order and state
alongside secret exclusion; see [main-turn classification](streamsup-package-per-conversation-turn-busy-tracking.md).

A verifier can use Go's `-overlay` option with replacement files outside the worktree
to test a mutation without editing the branch. Assert that each replacement applied.
Run the named test being evaluated so a neighbouring assertion cannot mask its weakness.

For a graph-complexity proof backed by a disk fixture, count resolution work after
reading the fixture and give integration its own finite elapsed allowance.
Installation can spend that allowance on copies, syncs, repeated scans and
descheduling before resolution observes cancellation. Resolve the deepest
consumer with a cold cache first: warming intermediate entries can hide the
cost of missing completed-resolution reuse. The
[retained-build fixture](apps-package-testing.md#retained-build-fixtures) asserts
the resolved path and equal independently checked build IDs as well as bounded
work, so a cheap but incorrect resolution cannot satisfy the proof.

Retain expected mutation failures and phase timings alongside passing runs. The
[expansion experiment](../../../internal/apps/testdata/3473-expansion-experiment.py)
compares unchanged and repaired race binaries under matched contention, injects
an installation delay, and separately disables completed-cache reuse. Its
[native evidence](../../../internal/apps/testdata/3473-macos-results.json) records
133/512 checkpoints with memoization and cancellation at 512 without reuse.
On macOS arm64, installation took 91–138 ms while in-memory resolution took
0.073–0.170 ms. These elapsed measurements include scheduling and do not isolate
individual filesystem syscall costs; checkpoint counts establish the work bound.

A controlled delay can demonstrate a deadline dependency without identifying a
historical timeout's cause. Here, a 1.1-second delay at installation's first
cancellation checkpoint failed the unchanged test with the historical error and
passed the repair, while 20 ordinary repetitions per arm did not reproduce the
timeout. Preserve that distinction in conclusions; see the
[investigation](../../specs/architecture/3473-retained-expansion-proof.md#native-recovery-evidence-2026-10-16)
for revisions, commands, counts and failed attempts.

For asynchronous state resets, acknowledge completion of result processing before
releasing the next phase. Entry into delivery or classification can precede the
reset itself. Also observe terminal failure while awaiting progress, so a mutation
that abandons work fails on abandonment rather than a coordination timeout. The
[queue pending-reset proof](msgqueue-package-give-up.md#testing) combines that
ordering with controlled elapsed time while retaining real retry timers.

A content-free log assertion must not search for a short, common fragment of the
secret. Timestamp and metadata text can contain the same digits and make an unrelated
wall-clock value look like a disclosure. Use a distinctive sentinel or inspect the
structured attribute being protected rather than substring-matching the whole log line.

A fixture asserting that a count is independent of a list — an explicit dropped or
omitted count, never inferred by measuring what was retained — must make the count
differ from that list's own length, not merely from its sibling counts. "Mutually
distinct" is the weaker property and passes even when a count equals `len(list)`,
which is exactly the shape indistinguishable from inference; #2370's first fixture
picked `dropped_categories: 2` beside two retained rows and had to be corrected once
a stronger assertion caught it.

A test proving a check runs *before* a side effect must not stop at "the side effect
didn't happen" — a check that moved to run *after* that side effect, for an unrelated
reason, would leave the same empty end state. Make the ordering itself the
observable: give the request a second flaw that a later step would catch by a
*different*, distinguishable error, and confirm the refusal names the check under
test, not the later step. #2665's refusal cases pass `spawnDir: "/"` — which
`resolveSpawnDir` rejects — alongside the model/effort value under test; getting the
settings sentinel back, not `ErrSpawnDirRejected`, is what shows the membership
check ran first (see
[conversation-session-binding-create.md § Requested model and effort](conversation-session-binding-create.md#requested-model-and-effort-2665)).

When every refusal collapses to the same boolean or wire code, a valid registry
cannot distinguish an early filename rejection from one after lookup.
`TestWorkspaceFileReader_SecretNames` also supplies a nil registry with a denied
requested leaf: moving the check after `Registry.Get` becomes observable rather
than returning the same generic refusal. See [the live reader's filename checks](v2-session-manager-state-machine-inbound-read-workspace-file-workspacefileread.md#testing-the-filename-checks).

Case-variation filesystem fixtures need paths that differ by more than letter
case. On macOS's case-insensitive filesystem, upper/lowercase names can share a
file or symlink and fail during setup before exercising the predicate.
`TestWorkspaceFileReader_SecretNames` uses distinct `-lower-case` and
`-upper-case` directories while preserving the actual leaf spellings under test.

A generic refusal does not prove that a rejected file was never read.
`TestRunnerPrivateReadReplacement` substitutes the leaf after the real `Lstat`
through the per-call `runnerPrivateReadWithOpen` seam. A duplicated descriptor
shares the opened file's offset, so checking that it remains zero detects reads
hidden by the eventual rejection; separately assert that the rejected descriptor
is closed. Retain the original inode and replace its name with a symlink back to
it: identity checks alone would accept that link, so require open itself to fail
with `ELOOP`. Run potentially blocking replacements in deadline-bounded child
processes that are killed and waited on, with parent-owned temporary files.
See the [private artifact contract](e2e-harness-recorded-lifecycle-timing-and-settlement.md#recorded-lifecycle-timing-and-settlement).

No-follow filesystem tests need a canonical fixture root before deriving paths
and exact-root references. `t.TempDir()` can inherit a symlinked `TMPDIR`; then an
ancestor rejection prevents valid-state checks from reaching the operation and
can make containment refusals pass for the wrong reason. Resolve only the
fixture root, retain deliberate unsafe leaves, and exercise successful states
alongside refusal cases under a controlled alias. See
[the apps snapshot fixture](apps-package-concurrency-and-testing.md#testing).

Unix-socket fixtures also need a short, test-owned root independent of inherited
`TMPDIR` length. Canonicalize the allocated root before deriving the socket path,
then assert the complete path is fewer than 104 bytes to leave room for termination
in macOS's `sun_path`. A real missing-socket classification fixture must require
`errors.Is(err, syscall.ENOENT)` from `net.Dial` before testing the classifier:
an oversized address can produce `EINVAL` instead, exercising path rejection
rather than the intended missing-socket case. Keep the real dial; a synthetic
ENOENT would bypass this fixture precondition. See
[control socket paths and cleanup](control-plane-testing.md#socket-paths-and-cleanup).

For a socket-path regression, run the named test in separate processes with a
deliberately long physical `TMPDIR` and a symlink alias to it, using `-race -count=1`.
Both roots must make the old fixture's canonical socket path exceed the bound;
an alias alone is not evidence of an overlong canonical path. Record both roots,
canonical old-fixture paths, commands and results, including the old allocation's
failure under the new bound assertion.

A test whose premise is "past a named cap" must build its fixture by computing from
that constant, not by restating a literal believed to be past it. A hardcoded number
keeps passing for the wrong reason after the cap changes, or silently stops testing
the boundary at all — [`maxModelVocabularyFile`'s oversized-file
test](streamsup-package-retaining-the-decoded-model-list-for-the-session.md#holding-codexs-model-families-beside-claudes-2627)
derives its fixture from the constant for exactly this reason.

Before removing a log record a test syncs on, grep for the record's field values, not
only its event name. A test can wait on one discriminant of a structured log line
(`kind=mcp_status`, a session id, a conversation id) without the event name itself
ever appearing in the test file, so a name-only grep for the record being removed
misses it. #2739 removed the stream drain's `stream_turn.not_active` drop and its
list of affected tests named every call site that synced on the record's name, but
missed four e2e tests that synced on `kind=mcp_status` specifically — found only once
`make check` turned red on the rework.

Queue placement tests must compare history payloads/timestamps, live arrival
and replay/event-id order through the wired drain and operator broadcaster.
Sequential ring appends cannot expose a reply overtaking an asynchronous user
push. When withholding `OnDelivered`, await a callback-completed signal after
the recording call before asserting that release added nothing: a barrier on
the stream drain proves only that drain's progress, not the queue goroutine's.
See [history producers](history-package-producers.md#producers-2114-2115) and
`TestOrdinaryQueuePlacement_OrderWithDelayedConfirmation`; its foreign-conversation
barrier alone leaves this late-callback assertion under-synchronized (#2820).

## Broker diagnostic failure cleanup

A passing original-root replay cannot prove resources survive assertion-failure
cleanup. `TestRecordedLifecycleDiagnosticCleanup` re-execs the test binary and
uses real Unix seven-pipe transfer with `recordedLifecycleDiagnostic`, the same
helper as `runRecordedLifecycleSuite`. An intentionally failing inner subtest
triggers actual LIFO cleanup. Channel barriers hold the launch callback across
cleanup, including beyond both bounded `Join` waits, and prove the diagnostic
remains writable before release. A separate running-child case observes readiness
through a transferred descriptor before cancellation; retain the source pipes
until outer probe cleanup so closing them cannot substitute for killing the child.

Require the original failure marker and failed helper verdict, and reject extra
lifetime failures and race reports. After handler completion, verify retained
content, a closed descriptor, exactly one owner close and child reaping. Cover
success, start failure, launch rejection, cancellation before start and
cancellation after start. Sleeps
or a separate cleanup model cannot establish this ordering. See the
[handler ownership contract](e2e-harness-recorded-lifecycle-timing-and-settlement.md).

The [retained evidence](../../../internal/e2e/testdata/3835-broker-diagnostics/results.json)
separates the four old-order lifetime failures from the five repaired cleanup
passes and the passing isolated session-resume replay under the race detector.
That replay does not erase the historical #3737 verifier failure preserved by
[#3833](https://github.com/pyrycode/pyrycode/issues/3833), or establish the
predecessor-retirement repair tracked by
[#3834](https://github.com/pyrycode/pyrycode/issues/3834).

[Development verification overview](development-verification.md).
