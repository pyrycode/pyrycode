# Test execution and artifact survival

An exit code alone does not prove tests ran. Read named results, counts and skip reasons.
A local authentication failure says nothing about the separately launched live gate.
Use the gate's own results to establish what it exercised. Current role instructions
control whether agents or the dispatcher run each test tier.

Retain the granularity of gate evidence. Nonverbose package `ok` results establish
package outcomes, not individual test/subtest counts or skip reasons; do not infer
that a named test ran from them. Keep separately counted JSON results distinct
from the standard gate, and state missing concurrency settings explicitly. The
[relay admission investigation](../../specs/architecture/3485-relay-fixture-admission.md#standard-gate-result)
retains both forms without treating a gate pass as historical failure attribution.

State whether totals count leaf tests or all Go test events. Go JSON includes
parent tests as well as their subtests; a passing parent adds an event without
adding another leaf scenario. Keep skipped outcomes separate from executed
passes. Comparing a leaf tally with raw event totals can otherwise suggest
missing coverage where only the counting convention differs. For example, the
[queued-send retirement evidence](../../specs/architecture/3728-user-queue-retirement-acceptance.md#counted-proof-and-applicability)
has 2,255 passing leaves plus 276 passing parents and 27 skips: 2,558 run events,
2,531 passes, zero failures and 27 skips. Those are the same historical run,
not two independent executions.

Before accepting a baseline comparison as attribution, compare suite composition and
fixture state. Re-running one failing reader on the base without its fixture-producing
sibling is not the same experiment. Check for an existing issue before filing another.

### Diagnosing recorded lifecycle output timing

Measure work between forwarded frames before changing a replay deadline. In the
Linux conversation replay diagnosis, repeated full-prefix validation and parsing
took 29–43 ms immediately before missed outputs, against roughly 20 ms captured
gaps. Both original roots delivered replies but failed final settlement. All four
role failures were `before-wait`, anchored to user 1, with 107–121 ms lateness;
input observation was at most 25.6 ms, input-witness work was zero and startup
preceded readiness. This established accumulated observer work as the demonstrated
defect, rather than a reason to enlarge the 100 ms allowance. The
[result-boundary observer](e2e-harness.md#recorded-lifecycle-timing-and-settlement)
retains full validation and the final independent audit.

The [committed timing evidence](../../../internal/e2e/internal/claudetape/testdata/lifecycle_timing_3666.json)
retains failed/passing source revisions, executed helper/daemon SHA256 identities,
exact commands, numeric diagnostics, unchanged fixture hashes, report hashes and
external log paths. On Linux/amd64 with `-race -count=1`:

| Evidence | Source | Executed / passed / failed / skipped |
| --- | --- | --- |
| Original conversation roots, diagnostic-only code | `ad4c8b1f` | 2 / 0 / 2 / 0 |
| Controlled 35 ms derivation / 20 ms cadence, capture and replay | preserved `b668be39` | 2 / 0 / 2 / 0 leaf tests; both failed at output 7 |
| Affected claudetape package after repair | `08f448c5` | 794 / 794 / 0 / 0 events; 724 leaf tests |
| Unchanged original roots with final settlement after repair | `08f448c5` | 2 / 2 / 0 / 0; outer replay events 3 / 3 / 0 / 0 |

The roots are `conversation-system-prompt` and `conversation-active-switch`.
Baseline runs used separate fresh processes and homes; an accidentally
prefix-selected startup control ran afterward and passed. The passing run used
that listed scenario order. Build caches were warm; OS page-cache state was
unknown. Admission waits were recorded, but host load during execution was not
measured. Historical cold state, preceding suite sequence and contention remain
unknown, and historical failures remain failed. These focused results establish
the repair on unchanged recordings; complete-suite acceptance is separate. The
[accepted verifier review](https://github.com/pyrycode/pyrycode/pull/3669#issuecomment-6285112952)
records both conversation passes and the separately tracked inherited
`clear-wrapup-skipped` failure. Other operating systems are outside this Linux
ticket's verification scope.

### MCP proxy cancellation diagnosis

Elapsed `ProxyServer.Join` time alone does not establish parent-context expiry.
`Coordinator.ForwardRPC` calls its validator before `RPCSession.Observe` parses
the envelope again and before the forwarding write, all under the coordinator
mutex. A validation notification therefore cannot establish blocked output.
`ProxyServer.bad` must cancel owned I/O before `Coordinator.Abort` takes that
same mutex; distinguish validation work, descriptor shutdown and child Wait.

`TestMCPProxyActualSCMTransferCancellationAndBindings` exercises actual
SCM_RIGHTS transfer with both pipes and Unix-socket stdio. Its backpressure
leaves read the first byte of a two-MiB response, require the forwarding mutex
still held, and leave the remainder unread through Join. The owned context must
be live immediately before Join and cancellation must be observed no earlier
than its 25 ms budget. Join must return an error with the independent parent
still live and no active handlers. `runOwnedServer` collects all three pump
results and its actual child `exec.Cmd.Wait` before the handler is removed.
After Join, require partial output EOF, actual native proxy Wait with failure,
a still-live parent and rejection by `Coordinator.Finish`. Early completion or
parent cancellation cannot substitute for these observations.

The [retained Linux race evidence](../../specs/architecture/3690-mcp-proxy-cancellation.md#builder-evidence)
at Go source `641c1edf` used Go 1.26.9, GOMAXPROCS 8, 12 CPUs, default
parallelism, serial leaves and unset GOFLAGS/GORACE. Across 24 ordinary
backpressure observations, measured phases were:

| Phase | Observed duration |
| --- | --- |
| Validation notification to first forwarding byte | 0.513–0.566 s |
| Join start to owned cancellation observation | 25.27–25.84 ms |
| Join start to return, parent live and handlers joined | 27.74–31.56 ms |
| Post-Join output EOF / subsequent native proxy Wait | At most 0.462 / 0.057 ms |

Partial EOF retained 69,632 bytes for pipes and 219,264 for sockets, below the
payload size. A separate phase overlay observed every pump returning within
0.143 ms of cancellation-watcher entry into descriptor closure and actual owned
server Wait within 3.257 ms. The 15-second watchdog exceeds six times the longest
successful selected leaf (2.47 seconds), independently of the one-minute parent
deadline. It is a fixture deadlock bound, not a product latency guarantee.
Firing fails the test, cancels the parent, closes test-owned stdio and kills only
the actual proxy command. Cleanup joins controlled work, the cancellation
observer and any fired watchdog callback; fixture PIDs and foreign process
groups never become signal targets.

Both validation-delay leaves inject 1.2 seconds before forwarding. Restoring the
old notification readiness and one-second elapsed predicate failed at Join
1.729/1.724 seconds while both parents remained live; the repaired proof passed.
Disabling owned cancellation failed each ordinary transport at the watchdog and
released/joined controlled work. The affected package passed 145/145 events
(135 leaves); each ordinary backpressure leaf passed ten uncached repetitions,
with zero skips. Full named runs retained preceding complete-duplex and quiet
pipe/socket cases plus launch-binding and descriptor rejection. Exact commands,
hashes, controls and failed attempts remain in the
[evidence record](../../../internal/e2emcp/testdata/3690-cancellation-evidence.json),
independently checked in the [verifier review](https://github.com/pyrycode/pyrycode/pull/3702#issuecomment-6294131124).

These controls prove the old oracle defect, not a historical scheduler cause.
Ordinary focused runs did not reproduce the historical Linux failure; no runtime
cancellation defect was demonstrated. The original candidate and same-tree
comparison each failed both leaves (1 executed / 0 passed / 1 failed / 0 skipped
per leaf), while unchanged base passed each once. Their 21 local build inputs
and manifests were unchanged; that candidate/base difference remains unexplained.
Historical parent state, phase timing, cold state and host/race settings are
unavailable; the retrieved bundle preserves the original log tail and full-log
pointer/hash. Normal race-helper exit delay is measurable separately (0.010
versus 1.212 seconds with GORACE exit delays 0/1200 ms), but those zero-test
helper processes establish neither a test pass nor historical attribution.
All historical failures remain failed. The queued wider investigation supplies
no execution or acceptance evidence and this proof completes none of #3668's
unfinished gates. The same-binary helper proves offline ownership/I/O only;
it supplies no live-provider acceptance. macOS is outside this Linux ticket's
verification scope.

### Diagnosing dormant relay restart readiness

Process collection, relay deregistration, replacement registration and phone
handshake completion are separate milestones. `WaitBinary` only observes map
membership; the retiring registration can remain after its pumps stop and
admit a doomed phone. The [dormant restart proofs](e2e-harness-stream-interactive-harness-pattern-startstreamin.md#restartstreaminteractivewithrelay-and-the-dormant-reset-specs-2521)
stop the daemon, observe absence with `WaitBinaryOffline`, then start its
replacement before checking positive registration. Preserve generation and
upgrade/Noise phase evidence rather than sleeping or retrying a failed read.

The controlled `TestWaitBinaryOfflineRetiringRegistration` demonstrates this
dependency by holding deregistration after pump joins: a premature upgrade
succeeds, then its first handshake read fails with normal WebSocket closure.
It also proves routing after retirement and same-ID replacement. Replacing
the offline barrier with `WaitBinary` failed all five test/subtest records
(four leaf scenarios and the bounds parent). This establishes the missing
ordering, not the cause or exact close shape of the historical macOS EOF.
Unchanged and repaired Linux repetitions did not reproduce that EOF; later
passes cannot identify the original ordering.

The [committed investigation evidence](../../specs/architecture/3474-dormant-restart-relay-readiness.md#builder-evidence)
records Linux x86_64, Go 1.26.9, `GOMAXPROCS=2 GOFLAGS=-p=2`, uncached race
execution and unchanged ten-minute package timeouts. Twenty repetitions of
each dormant proof passed 40/40 records on unchanged fixtures at `ec136ff1`
and again on repaired fixtures at `167ea7b4`; controlled tests passed 100/100
records over 20 repetitions, with no skips. Complete `make check` passed: module race tests
27,123 executed / 27,100 passed / 0 failed / 23 skipped; fake-daemon e2e
715 / 713 / 0 / 2. `make build` passed. Exact commands, revisions, incidental
contention, skip reasons and lifecycle phases remain in the plan.

The [independent verifier](https://github.com/pyrycode/pyrycode/pull/3476#issuecomment-6208774542)
reviewed source and retrieved builder evidence; the dispatcher separately
passed guards, check and build at `6518e9f8`. That gate supplies no per-test
counts. Focused repetition totals are builder-recorded; their raw JSON events
were not independently recounted by the verifier. macOS candidate execution
is outside this Linux ticket's updated scope; the platform probe and withdrawn
task supply no candidate pass. The historical macOS EOF remains undiagnosed.

### Diagnosing API response gate failures

An unexpected `ErrUnavailable` in a response-limit or capacity arm establishes
target unavailability, not what initiated it. The historical #3399 log recorded
unavailable errors across overflow, capacity, truncation and the final request,
without probe or target-state evidence establishing the trigger. Baseline race
repetitions passed 360/360 records; passing physical and symlink TMPDIR reruns
therefore cannot assign a TMPDIR or timeout cause. Retain a
[controlled failure-class experiment](apps-package-testing.md#other-package-fixtures)
that separates rejection, header/body I/O and invalidation using exact errors,
reservation/read phases, target validity, requests and retained budget. Injected
invalidation reproduces the error pattern while leaving historical attribution
open; shared fixture registry roots already canonicalize symlinks.

Keep scoped API results separate from aggregate acceptance. The
[final #3399 verifier verdict](https://github.com/pyrycode/pyrycode/pull/3446#issuecomment-6196362331)
records dispatcher cite/docs guards, `make check` and build green at
`a4f94aedb3f5c704427b1ca4b65d8043728159a8`, with cached unit packages and no
per-test counts. The additional uncached, normal-concurrency `make check` at
that head remained **failing/incomplete**: 26984 executed / 26969 passed /
2 failed / 13 skipped test/subtest records. Main baseline `6ab3c311` also
failed: 26978 / 26963 / 2 / 13. Both failures were
`TestRelayAdmissionStorage/setgid_parent` and its parent, with leaf diagnostic
`valid=false, error=<nil>`, tracked in [#3458](https://github.com/pyrycode/pyrycode/issues/3458).
Those observations establish neither actual setgid-parent acceptance nor a
filesystem setup cause. Vet passed, but both uncached attempts stopped in the
race unit tier; staticcheck, substrate-guard and fake-daemon e2e ran only in the
separate dispatcher gate. The subsequent head build passed. No uncached e2e
count or green uncached aggregate is implied by the dispatcher result.

The verdict retains exact commands, supplied/resolved physical TMPDIR,
supported Node/npm selections, counts and thirteen skip reasons. All 24 API
response/diagnostic records passed without skips. Merged #3418's positive
retirement confirmation cleared the earlier retirement finding: all eighteen
`TestPublishRetirement` arms and parent passed (19/19, no skips). Earlier
`health/stop` and `health/intent` failures remain failed historical results;
they are distinct from #3458. The
[two-root rework evidence](../../specs/architecture/3399-api-response-verification.md#rework-scoped-verification-evidence)
at `8f543e1a` records 360/360 response records per root and complete apps suites
of 1029 executed / 1025 passed / 0 failed / 4 filesystem EILSEQ skips per root.
These focused successes do not complete the red aggregate. A separately
interrupted sandbox attempt with listener-bind `operation not permitted` errors
also supplied no completed gate or product attribution.

### Diagnosing startup recovery runtime

A test active when the package times out need not account for the package's
elapsed time. Retain its active interval separately from package runtime, with
phase-start diagnostics and durations for fixture construction, raw paging,
recovery, reopened reads and repeated recovery. Preserve timeout stacks when a
timeout occurs. Later passes establish non-reproduction, not the historical
cause. The [startup comparison](../../specs/architecture/3431-startup-recovery-runtime.md#revisions)
reduced isolated startup time from 28.48 to 5.78 seconds, while complete package
runs took 303.756 and 291.762 seconds. Neither ordinary runs nor four prebuilt
race binaries pinned to one CPU reproduced the historical timeout.

Compare baseline and candidate under identical bounded contention, retaining
each process's settings, revision, command, JSON results and status. Join the
controlled workload before ordinary package and combined-gate measurements;
record any incidental overlap separately. Focused passes cannot certify the
complete gate. Retain named execution/pass/fail/skip counts and skip reasons for
each test tier; aggregate green gates without named results cannot prove that a
particular regression ran.

`testThreadMetadataFixture` waits for usable status and a usable snapshot, both
with consumed `Version` and `LastShownVersion` 1,024, and 1,024 complete items
(4,096 text bytes each). Chunk progress renews a twenty-second inactivity watchdog;
an independent two-minute cap bounds readiness. Progress cannot certify usability.
A chunk plus final publication exceeded ten seconds under contention, so watchdog
headroom must cover that work. These are fixture bounds, not latency guarantees.
Cleanup joins `Store.Shutdown` before cancelling the independent load context.

Ordinary checks use a virtual clock to prove that progress renews the inactivity
bound, only complete usable publication ends readiness, and progress cannot extend
the caller deadline. `TestDaemonThreadMetadataReadiness` delays five real replay
chunks by five seconds each, defeating even a twenty-second total deadline. It
runs in `make test-slow`, which remains mandatory in `make preship`. The real
stalled regression stays in ordinary checks and proves bounded failure and joined
cleanup. This [controlled comparison](../../specs/architecture/3467-metadata-fixture-readiness.md#revisions)
establishes the total-deadline dependency; historical scheduling attribution
remains unknown. Phase timings stay outside benchmark measurements.

Fixture cost depends on encoded bytes. JSON escaping can multiply padding size,
and paging decodes whole segments repeatedly. Measure on-disk bytes and assert
raw pagination and physical rollover independently before accepting smaller
padding. See the [startup fixture coverage](history-package-producers-legacy-compatibility.md)
for the independent failure controls and retained recovery assertions.

### Diagnosing reply timeouts

For reset classification, record elapsed post-persistence resolution, first
delivery, rotation entry and completion using durations and counts without
content. The [stale-handoff comparison](../../specs/architecture/3428-stale-handoff-timing.md#revisions)
used identical path assertions in four concurrent race binaries pinned to one
CPU per policy. Both the old 35 ms policy and the repaired policy passed all
600 tests/subtests without skips: the historical failure was not reproduced.
The two-second classification allowance matches the fixture default; 500 ms
leaves over twenty times the largest measured pre-delivery delay while keeping
expiry real, and three seconds bounds reset and rotation. These finite regression bounds
are not product latency guarantees. Retain revisions, commands, phase timings
and execution/pass/fail/skip counts; later passes cannot establish historical
cause or completion of earlier gates.

A fast provider does not imply a fast first reply. Relay `runThreadQuery` completes
`EncodeThreadReply` before serial delivery, so a reply wait includes whole-result
encoding and scheduling under race instrumentation. Measure content-free provider
readiness, first transport entry and final reconstruction before changing a wait or
fixture. `v2Recorder.snapshot` copies routing-envelope headers, not accumulated
ciphertext; `outbound` copies each frame once on capture. Treating polling as repeated
frame copying would target the wrong cost.

Compare old and repaired waits under identical controlled contention, retaining the
escaped/multibyte fixture and complete protocol assertions. The
[large-page reproduction](../../specs/architecture/3364-large-thread-page-reply-wait.md#revisions)
records four race-test processes pinned to one CPU: all four old waits expired before
first transport, while all four repaired runs reconstructed the same reply in about
11.6 seconds, with first transport after four seconds. This establishes insufficient
scheduling headroom in that experiment; unavailable historical phase logs do not
establish a production regression or exact attribution for earlier gate failures.
Use one measured absolute deadline across all fragments and the separate final
coverage marker, checking expiry even with buffered replies. Resetting the budget
per fragment would let partial progress extend the missing-completion wait. The
15-second bound retains headroom over the measured reconstruction; scratch overlays
dropping all replies or only the final marker each failed at that bound. A focused
pass alone proves neither attribution nor bounded failure.

Recovery readiness and peer admission are separate from query completion.
`testDaemonThreadLoad` waits for a usable exact-version query before opening
peers; receiving both handshake responses still needs a bounded `ActiveConns`
snapshot to establish admission. Record recovery, handshake/admission, provider
entry/completion, first transport and complete reconstruction separately, using
durations and counts without content. The
[recovery-wire comparison](../../specs/architecture/3468-recovery-wire-wait.md#matched-contention-evidence)
ran four race binaries with 32 CPU-only contenders pinned to CPU 0. All four
baseline three-second waits expired after recovery and handshakes, before the
first provider returned; all four repaired runs passed with the full escaped
fixture. Recovery preparation took up to 13 seconds. Candidate admission took
0.657–0.765 seconds after wire setup, first provider completion 5.283–5.370
seconds and first transport 12.906–13.138 seconds. Separately timed from reply
collection, the first catch-up frame took 12.249–12.425 seconds and complete
catch-up/page reconstruction took 33.765–35.359 seconds. These measurements
establish scheduling headroom for this controlled experiment. The truncated
historical #3453 evidence cannot identify the cause of its earlier failure.

`testDaemonThreadReply` starts one 60-second absolute budget at reply collection
immediately after request submission, covering provider work, encoding, every
fragment and the separate final coverage marker. This leaves about 70% headroom
over the largest measured complete reply. `testDaemonThreadFrame` checks expiry
before consuming ordered pending or buffered output; the reply helper checks
again after decryption and final reconstruction. Progress never restarts the
budget. Keep shared `switchWire.receive` and handshake waits at their existing
three-second defaults, using `switchWire.decode` for the same authenticated,
nonce-ordered decoding under the query budget. This is a test workload bound,
not a product latency guarantee.

Preserve full rows and parent closure, saved epoch reuse, old-revision first
appearance, page bounds, correlation and legacy history when testing the wait.
`TestDaemonThreadQueriesRecoveryDelayedProvider` holds the first provider beyond
the old frame wait, then requires the identical complete recovered reply with
bounded, joined gate cleanup. The
[missing/late completion controls](../../specs/architecture/3468-recovery-wire-wait.md#focused-checks-and-negative-controls)
retain the fixture: suppressing all transport, withholding final completion
after 16 frames and delaying buffered final completion each failed at about
60 seconds. A larger allowance alone does not establish reliable completion or
finite failure.

Deadline controls must also avoid short wall-clock success assumptions.
`TestDaemonThreadReplyDeadline` originally required its first buffered receive
within 30 ms; contention could consume that budget during setup. A later passing
rerun did not erase the observed failure. Use `testing/synctest` for these local
timer/channel scenarios: consume 20 ms of the original 30 ms budget, receive the
buffered prefix, then require missing output to expire exactly at the original
deadline. Already-expired pending/buffered output must fail immediately. The
[deterministic controls](../../specs/architecture/3468-recovery-wire-wait.md#rework-validation-at-662b97de)
show that a real 60 ms setup pause fails the old test and passes the virtual-clock
test, while restarting the timer fails at virtual 50 ms instead of 30 ms. This
checks the deadline policy independently of OS scheduling.

`TestLiveStateRun` is a hermetic manager test despite its name. Its complete
snapshot needs `pushQueueCap*2+2` routing envelopes: the handshake, one generated
clear and every reading. Four race-test processes pinned to one CPU alone still
passed the old two-second wait; adding 32 CPU-only contenders on that CPU made
all four old waits expire after partial output. Under identical contention the
repaired workload completed all application decryption within 4.382 seconds in
the provisional measurement, establishing the eight-second bound with about 83%
headroom. This measures scheduling headroom, without attributing the historical
491/514 result to a cause its missing phase logs cannot establish. Keep ordinary
`waitForEnvelopes` callers on their existing default.

Start that single absolute budget before manager startup and snapshot-producing
connection admission. Log first, halfway and final transport, plus halfway and
complete decryption, using elapsed time and received/expected and
decrypted/expected counts only. Check expiry before processing buffered frames
and after each decryption, including the last: a timer checked only while idle
would let a buffered workload finish late. Progress must never restart the budget.
The [live-state negative controls](../../specs/architecture/3430-live-state-workload-deadline.md#revisions)
suppressed output after a partial prefix and failed at 8.0003 seconds with
257/514 received and 256/513 decrypted; delaying the final buffered decryption's
completion past the bound failed at 8.0102 seconds even with 514/514 received and
513/513 decrypted. Both executed once, failed once and skipped zero times.
Keep the full expected count and the
[authenticated connection-survival proof](v2-session-manager-test-surface-same-package-unit-tests-internal-relay-delivery-and-control.md#complete-live-state-workload);
reducing the fixture or accepting missing output as a skip would erase the proof.

### Test inputs and retained artifacts

Record the source of the daemon actually executed, not only the checkout's ancestry.
The historical #3071 candidate's `spawnPermissionDaemon` honored
`PYRY_E2E_READINESS_BIN` and `PYRY_E2E_READINESS_MCP_CONFIG`, changing the binary
and MCP fixture independently of the tested tree; the current harness builds
through `ensurePyryBuilt` without those overrides. A merged repair can coexist
with a stale overridden daemon when comparing historical runs;
retain the override inputs, actual binary source, Claude version and exact named
leaf outcomes alongside suite counts. See
[the prompt-answer recovery evidence](../../specs/architecture/3256-shared-live-repair-evidence.md#context).

A temporary worktree is not durable storage. For an agent-produced artifact, commit it.
For a gate-produced capture, retain an external recovery copy and use the documented
promotion step. Staging alone does not survive worktree removal. Require the committed
reader to reject present-but-unpinned evidence. Read the current release runbook before
claiming that a gate automatically commits generated files.

[Development verification overview](development-verification.md).
