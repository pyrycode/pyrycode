# Recovery wire workload coordination (#3468)

## Files read
- `cmd/pyry/daemon_thread_queries_test.go` → `TestDaemonThreadQueriesRecovery`, `testDaemonThreadQueryWire`, `testDaemonThreadReply`, `testDaemonThreadLoad`: unchanged recovery fixture, provider installation and reply assembly.
- `cmd/pyry/agent_switch_frames_test.go` → `switchWire.open`, `receive`, `read`: authenticated handshake and ordered receive defaults.
- `cmd/pyry/daemon_live_relay_test.go` → `testDaemonRelayWire`: buffered capture, manager startup and joined cancellation.
- `internal/relay/v2session_catchup.go` → `runThreadQuery`: provider and complete reply encoding precede first delivery.
- `docs/knowledge/features/thread-package.md` § Shadow lifecycle and evidence: preserve reopened epoch, bounded-query coverage and inert escaped content.
- `docs/knowledge/features/development-verification.md` § Diagnosing reply timeouts: matched contention, absolute completion bound and buffered-output negative controls.
- `CODING-STYLE.md`; builder `security-review.md`: concurrency and authenticated-wire review requirements.

## Context
The shared three-second per-frame wait includes expensive provider rendering and encoding before first transport. At base `07b6b35e14aad0de688326fb36d81c54303c691c`, a diagnostics-only overlay failed four of four recovery executions under four race binaries and 32 CPU contenders pinned to CPU 0. Recovery finished and both handshakes completed (legacy completion 650–748 ms after wire setup); first catch-up entered at 748–784 ms, without returning before frame expiry. This demonstrates insufficient provider scheduling headroom in this experiment. The downloaded #3453 builder evidence contains truncated excerpts, not complete phase logs, so historical attribution remains unknown. No production repair or ADR is proposed.

One deliverable, estimated 350 written lines including plan/evidence, zero exported types, seven reply-helper consumers, three acceptance criteria, zero production reject branches. All five limits hold before committing this plan. No fetched feature branch changes either target test file. #3467 metadata readiness is independent.

## Design
Keep the shared wire's default waits and handshake behavior. Extract its existing authenticated envelope decoder so query helpers can consume the same ordered pending/output stream under a query-specific absolute deadline. `testDaemonThreadReply` uses one workload deadline for every fragment and the final coverage marker, checking expiry before buffered frames and after reconstruction. Do not reset the deadline on progress. Select the finite bound from the matched contention measurement (provisional measurement ceiling 30 seconds).

Add content-free provider-entry/completion, handshake and transport/reconstruction phase diagnostics. Retain every current fixture and assertion. Add a focused recovery variant that holds the first provider behind a bounded release gate beyond the old frame wait, then requires the identical complete recovered reply. Add focused deadline controls for absent output and already-buffered output; scratch overlays will also remove the final marker and suppress all transport while retaining the complete expected fixture.

## Concurrency model
The manager and provider workers keep their existing cancellation/join paths. Cipher receive ownership stays on the test goroutine. Diagnostic timestamps are immutable; no payload is logged. Controlled provider delay uses a cancellable gate with bounded release and cleanup; no worker may survive test cleanup. Pending frames retain their original nonce order.

## State transitions and identity reuse
| Event | Race test |
| --- | --- |
| Clean shutdown/reopen reuses saved epoch | `TestDaemonThreadQueriesRecovery` |
| Repeated catch-up/page requests on the same peer | `TestDaemonThreadQueriesRecovery` |
| First provider held then released; complete recovery reply | `TestDaemonThreadQueriesRecoveryDelayedProvider` |
| Missing/expired buffered frames preserve one bound | `TestDaemonThreadReplyDeadline` |

## Error handling
Timeouts identify the query and frame/reconstruction phase with elapsed duration/counts. Missing transport, partial replies and missing final marker fail at the same finite deadline. Malformed, unauthenticated or uncorrelated output continues to fail immediately. Other shared-wire callers retain three-second defaults.

## Testing strategy
Write the controlled-delay and deadline regression first and observe failure against old behavior. Compare baseline and candidate using the same four processes, race instrumentation, fixture, CPU 0 and 32 CPU-only contenders, joining all contenders before ordinary checks. Record exact revisions, commands, named counts and phase timings here. Run uncached focused repetitions, all seven helper consumers, complete `go test -race ./cmd/pyry/...`, `go vet ./...`, `go build ./cmd/pyry`, and the requested `go test -race -timeout=15m ./internal/thread ./cmd/pyry ./internal/relay`. Report unrelated failures separately; no live Claude is needed.

## Open questions
- Resolve the workload bound from complete provider/transport/reconstruction measurements before handoff.

## Security review
**Verdict:** PASS

**Findings:**
- Trust boundaries/cryptography: `switchWire.open` still authenticates test peers using Noise; the extracted decoder keeps the existing sequential receive cipher, with no key or nonce reuse.
- Tokens/errors/logging: generated test keys and fixture tokens are unchanged. New phase diagnostics contain only operation names, durations and counts, never plaintext or ciphertext.
- Files/subprocesses: only allowed test sources and this plan change. Scratch overlays, logs and fixed-command contention drivers stay under `/tmp/builder-3468`; contenders are terminated and joined.
- Network/I/O: hermetic buffered channels retain existing size caps. Query completion gains an absolute finite bound including buffered output; shared handshake behavior remains unchanged.
- Concurrency: no shared cipher access is added; provider gate cancellation must release before the manager's joined cleanup. No new product locks or goroutines.
- Threat model: protocol confidentiality, pairing and inert-content assertions remain intact; no product security boundary changes or deferred security findings.

**Reviewer:** builder self-review. **Date:** 2026-10-15.

## Revisions
- 2026-10-15: Complete measurement under the identical four race binaries plus 32 CPU-only Python contenders on CPU 0 passed 4/4, zero skips. The scratch variant changed only the first `switchWire.receive` timeout to a provisional 30-second per-frame measurement ceiling. First provider completion occurred 5.112–5.246 seconds after wire setup, versus entry at 0.659–0.786 seconds. The largest replies needed roughly 44 seconds from request through reconstruction. Select a **60-second absolute reply bound**, leaving about 36% scheduling headroom, rather than adopting the provisional per-frame policy. The open bound question is resolved. Recovery preparation itself took up to 13 seconds under contention and remains separately measured; handshake/admission retains its original finite wait.
- Recovery now also takes a bounded `ActiveConns` snapshot after both handshakes: response receipt alone does not prove admission. Shared-wire changes only extract the existing decoder; the query helper owns deadline-aware pending/output consumption. Seven existing reply consumers keep their signatures.
- The first controlled-delay run failed to compile because the deadline helper had not yet been introduced; this is not regression evidence. After a temporary old-behavior helper stub, `go test -race -count=1 -run '^TestDaemonThreadQueriesRecoveryDelayedProvider$' ./cmd/pyry` executed once and failed with `missing switch frame` in 3.87 seconds. This run incidentally overlapped the tail of the measurement experiment; the timer-held provider independently exceeds the old wait, so no isolated contention attribution is claimed. The repaired race run of recovery, delayed provider and deadline controls executed/passed seven tests/subtests, failed/skipped zero. All intentional contention had been joined before that run.

### Matched contention evidence
Environment: Linux/amd64, Go 1.26.9. No explicit GOMAXPROCS override. Baseline source `07b6b35e14aad0de688326fb36d81c54303c691c` plus scratch diagnostics only: provider entry/return, handshake start/return, each transport callback and reconstructed-frame receipt. The provisional variant adds only a 30-second first `switchWire.receive` timeout. Candidate source `2d3aefe1ee64b9e55f205ff82b48902fdeb518e6`. Commands:

Historical retrieval: `python3 /work/Projects/pyrycode-agents/dispatcher/scripts/fleet-task.py evidence 3453 --out /tmp/builder-3468/evidence-3453` downloaded the Mac builder bundle `6d69a887e50228e31a29bb4dd147c466947628c0393f7a2a5edd565e0848edeb`. Its agent log names `/tmp/builder-3453-gate-race.log` and `/tmp/builder-3453-daemon-rechecks.log`, but their displayed command outputs are truncated; neither complete log is included and `pointers.json` is empty. It confirms the reported failure/recheck sequence without supplying missing provider/transport phase evidence.

```sh
go test -race -c -overlay=/tmp/builder-3468/diagnostic.json -o /tmp/builder-3468/baseline.test ./cmd/pyry
go test -race -c -overlay=/tmp/builder-3468/measurement.json -o /tmp/builder-3468/measurement.test ./cmd/pyry
go test -race -c -o /tmp/builder-3468/candidate.test ./cmd/pyry
python3 /tmp/builder-3468/contend.py baseline /tmp/builder-3468/baseline.test 32
python3 /tmp/builder-3468/contend.py measurement /tmp/builder-3468/measurement.test 32
python3 /tmp/builder-3468/contend.py candidate /tmp/builder-3468/candidate.test 32
```

Each driver starts 32 `taskset -c 0 python3 -c 'while True: pass'` contenders, then four simultaneous `taskset -c 0 BINARY -test.v -test.count=1 -test.timeout=4m -test.run=^TestDaemonThreadQueriesRecovery$` processes, captures separate logs, waits for every test process, and terminates/joins every contender. There are no deliberate contenders during ordinary checks. Counts are named tests/subtests, including parent tests:

| Variant | Executed | Passed | Failed | Skipped |
| --- | ---: | ---: | ---: | ---: |
| Diagnostic baseline | 4 | 0 | 4 | 0 |
| Provisional measurement | 4 | 4 | 0 | 0 |
| Committed candidate | 4 | 4 | 0 | 0 |

Candidate peer admission completed 657–765 ms after wire setup; first catch-up provider completed at 5.283–5.370 s. Query 40 first reconstructed frame was 12.249–12.425 s after request; all 16 frames, including the separate marker, completed in 33.765–34.066 s. Query 41 first reconstructed frame was 14.623–15.146 s; all 15 frames completed in 34.988–35.359 s. The earlier roughly 44-second estimate conservatively included independent expected-row rendering between requests; actual whole-reply headroom at 60 seconds is about 70%. Both providers and encoding/serial transport consume time; handshakes and completed recovery do not establish reply readiness. The full escaped fixture, old-revision appearance, parent closure, saved epoch, bounds and legacy assertions passed in every candidate execution.

First application transport entry was 12.906–13.138 seconds after wire setup, independently recorded by the sender callback before forwarding. Reconstruction timings above have their own origin at reply collection immediately after request submission. This separates provider completion, first transport and final reconstruction rather than assigning all delay to one phase.

### Focused checks and negative controls
At candidate `2d3aefe1`, `go test -race -json -count=10 -timeout=5m -run '^TestDaemonThreadQueriesRecovery$' ./cmd/pyry` executed/passed **10/10**, failed/skipped zero. `go test -race -json -count=1 -timeout=5m -run '^Test(DaemonThreadQueries|DaemonThreadReplyDeadline|BoundQueueActions|QueuedSendHandle|RelayAgentSwitch|SwitchWire)' ./cmd/pyry` executed/passed **71/71**, failed/skipped zero. This covers the seven reply-helper consumers: recovery, reset, bounded, join, queued-send handle queries/actions, and `testBoundAction` via bound-queue restart/devices. Shared switch-wire encrypted frames/wiring checks also passed. No skip reasons exist for these runs.

Three scratch overlays retain the same recovery test and actual 60-second deadline. The no-output overlay returns success from the installed `OutboundContext` without forwarding. The no-final overlay decrypts all frames but continues rather than accepting `p.Final`. The buffered-expiry overlay waits until the deadline plus one millisecond inside the final-marker branch before its completion check. For each label `no-output`, `no-final`, `buffered-expiry`, the exact command was `go test -race -json -count=1 -timeout=3m -overlay=/tmp/builder-3468/LABEL.json -run '^TestDaemonThreadQueriesRecovery$' ./cmd/pyry`. Each executed once, failed once as expected, and skipped zero. Missing output failed at **60.000779 s with zero frames**. Missing completion failed at **60.000076 s with 16 frames and six reconstructed rows**. Buffered late completion failed at **60.001071 s with all 16 frames**. These mutations are scratch-only; no recorded/expected output is discarded in the committed test. Ordinary checks overlapped each other without CPU pinning or synthetic contenders; this incidental overlap is distinct from the matched experiment.

### Complete package checks
At source `2d3aefe1`, `go test -race -json -count=1 -timeout=15m ./internal/thread ./cmd/pyry ./internal/relay` completed with:

| Package | Executed | Passed | Failed | Skipped |
| --- | ---: | ---: | ---: | ---: |
| `internal/thread` | 1117 | 1117 | 0 | 0 |
| `cmd/pyry` | 16564 | 16561 | 1 | 2 |
| `internal/relay` | 1530 | 1530 | 0 | 0 |

Recovery executed once and **passed in 5.36 seconds**; delayed-provider and deadline controls also passed. The only failure was `TestDaemonThreadMetadataAllocations`, `long-thread replay did not finish` (11.87 seconds), already tracked independently by **#3467**. Its fixture/readiness code is unchanged here; the issue explicitly says recovery-wire work is not its prerequisite. This combined command is **red**, not a claimed full pass. It incidentally overlapped focused repetitions, negative controls, vet and build, all without synthetic contention. Those other commands were finished before the separate complete cmd run reached metadata readiness.

The two cmd skips were `TestRunAgentRun_RealClaude` (requires `PYRY_E2E_REAL_CLAUDE=1`) and `TestMemoryPathsCaseInsensitive` (case-sensitive filesystem cannot resolve alternate casing). Counts include named parent/subtests and report skipped starts separately. `go vet ./...` and `go build -o /tmp/builder-3468/pyry ./cmd/pyry` passed. The verifier owns the later full-module gate; no live-Claude proof is needed for this ticket.

The separate `go test -race -json -count=1 -timeout=10m ./cmd/pyry/...` run at the same candidate source completed in 263.61 seconds: **16,566 executed, 16,564 passed, zero failed, two skipped**, with the same skip reasons above. `TestDaemonThreadMetadataAllocations` passed in 7.09 seconds and `TestDaemonThreadQueriesRecovery` passed in 3.00 seconds. This run began while the combined command was finishing its last tests; all focused repetitions, negative controls, vet and build had already finished, and the combined command finished before this run reached metadata readiness. It was not a fully isolated process experiment. No additional attribution is inferred from its pass.

Final diff review: only the plan and three allowed test files changed; no production, recordings, expected outputs or documentation-stage files changed. All open design questions are resolved, all test/gate processes and contention workers have finished, and the written scope remains below every sizing limit. Subsequent evidence-only commits do not change the tested source revision.

### 2026-10-15 verifier finding 1: deterministic deadline controls
The verifier reproduced a scheduling-dependent first buffered receive in the `partial` subtest of `TestDaemonThreadReplyDeadline`: one failure among 80 parent executions under four race binaries and 32 CPU-only contenders pinned to CPU 0. A later 400-parent pass did not invalidate that failure. The original 30 ms wall-clock success requirement was itself unsafe.

Run each deadline scenario inside `testing/synctest.Test`, following the existing virtual-clock pattern in `TestTurnBusy_SendNowCarry_FoldedWriteClosesAfterGrace` (`cmd/pyry/send_now_test.go`). Virtual time cannot advance merely because the OS deschedules setup or the first buffered receive. In `partial`, advance 20 ms of the original 30 ms budget before receiving the first frame, then require the missing remainder to expire exactly at the original deadline. Also assert exact immediate expiry for already-expired pending/buffered output and exact deadline expiry for missing output. This changes only regression-test coordination; the reply helper, finite 60-second reply budget, authenticated-wire behavior and complete fixture remain unchanged.

Security re-review: PASS. `synctest` is a standard-library test-only clock; the isolated deadline scenarios create only local in-memory channels/timers and no application workers. Bubble exit joins its goroutines. No new trust boundary, key/nonce use, token/log content, filesystem operation, subprocess or network I/O is introduced. No security findings require remediation or deferral.

Validation will repeat the verifier's four-process, 32-contender CPU-0 experiment with matched baseline/candidate settings, join every process before ordinary checks, then rerun focused regressions, helper callers, complete cmd race coverage, the requested three-package race command, vet and build. Rework remains within the existing five sizing limits: one deliverable, no new exported types or consumers, and no production branches.

### Rework validation at `662b97de`
Environment remains Linux/amd64, Go 1.26.9, no explicit GOMAXPROCS override. The original verifier logs remain at `/tmp/verifier-3470/deadline-contention-{0,1,2,3}.log`; the downloaded verifier bundle at `/tmp/builder-3468/rework-evidence/d37cfcdc94205946e8b52c5699d8493df9dc2375cc164034af506b1874e68162/` retains its verdict and counts. Its first reproduction executed 400 named tests/subtests, passed 398, failed the parent and `partial` child, and skipped zero.

The scratch generator `python3 /tmp/builder-3468/rework-overlays.py` reads baseline `1e1e9d8a` with `git show` and candidate test source. Both overlays create a real-time 60 ms release goroutine/channel outside the virtual-clock bubble, then wait for release during `partial` setup after creating the deadline. The external channel cannot advance virtual time; its goroutine is joined before return. Exact commands, one parent plus one child each:

```sh
go test -race -json -count=1 -timeout=2m -overlay=/tmp/builder-3468/rework-baseline-setup.json -run '^TestDaemonThreadReplyDeadline$/^partial$' ./cmd/pyry
go test -race -json -count=1 -timeout=2m -overlay=/tmp/builder-3468/rework-candidate-setup.json -run '^TestDaemonThreadReplyDeadline$/^partial$' ./cmd/pyry
```

Baseline executed/failed **2/2**, passed/skipped zero, with `context deadline exceeded` on the first receive after 60 ms. Candidate executed/passed **2/2**, failed/skipped zero, despite the same real setup pause, and checked exact original virtual expiry. These scratch-only overlays retain the helper and all assertions. A separate scratch mutation, generated by `python3 /tmp/builder-3468/rework-reset-overlay.py`, replaces only the helper timer's remaining duration with a fresh 30 ms. `go test -race -json -count=1 -timeout=2m -overlay=/tmp/builder-3468/rework-reset-budget.json -run '^TestDaemonThreadReplyDeadline$/^partial$' ./cmd/pyry` executed/failed **2/2**, passed/skipped zero: expiry was virtual 50 ms rather than the original 30 ms. This proves the regression rejects restarting the budget after partial output.

Matched contention used the verifier's baseline binary `/tmp/verifier-3470/current.test` (built at `1e1e9d8a`) and a fresh `go test -race -c -o /tmp/builder-3468/rework.test ./cmd/pyry` binary with the candidate source. Exact driver commands:

```sh
python3 /tmp/builder-3468/rework-contend.py baseline-matched /tmp/verifier-3470/current.test
python3 /tmp/builder-3468/rework-contend.py candidate /tmp/builder-3468/rework.test
```

Each starts 32 `taskset -c 0 python3 -c 'while True: pass'` contenders and four simultaneous `taskset -c 0 BINARY -test.v -test.count=100 -test.timeout=90s -test.run=^TestDaemonThreadReplyDeadline$` processes. Both baseline and candidate executed/passed **2,000/2,000** named tests/subtests (400 parent repetitions), failed/skipped zero. The baseline failure did **not** reproduce in this matched rerun; the retained verifier failure and controlled setup pause establish the scheduling assumption independently. An earlier baseline preflight also passed 2,000/2,000 while setup-overlay compilation incidentally overlapped; it is separate from this matched comparison. Every deliberate contender and test process was terminated/joined before ordinary validation. The four per-process logs and driver summaries are retained under `/tmp/builder-3468/rework-*-contention-*.log` and `/tmp/builder-3468/rework-*-summary.log`.

`go test -race -json -count=100 -timeout=2m -run '^TestDaemonThreadReplyDeadline$' ./cmd/pyry` executed/passed **500/500**, failed/skipped zero. `go test -race -json -count=1 -timeout=5m -run '^Test(DaemonThreadQueries|DaemonThreadReplyDeadline|BoundQueueActions|QueuedSendHandle|RelayAgentSwitch|SwitchWire)' ./cmd/pyry` executed/passed **71/71**, failed/skipped zero, covering all seven helper consumers and shared-wire checks. Recovery passed in 4.19 seconds. `go vet ./...` and `go build -o /tmp/builder-3468/rework-pyry ./cmd/pyry` passed. The focused family, callers, timer mutation, vet and build ran concurrently without synthetic contention; complete package runs follow only after these processes finish.

`go test -race -json -count=10 -timeout=5m -run '^Test(DaemonThreadQueriesRecovery|DaemonThreadQueriesRecoveryDelayedProvider|DaemonThreadReplyDeadline)$' ./cmd/pyry` completed in 148.839 seconds: **70 executed/passed, zero failed/skipped**. This includes ten full unchanged recovered fixtures, ten provider-delay regressions and ten deadline parents with all four scenarios. Full cmd coverage started only after every focused, mutation, caller, vet and build process finished; the requested three-package run is scheduled sequentially after it.

The separate `go test -race -json -count=1 -timeout=15m ./cmd/pyry/...` completed in **297.915 seconds**: **16,566 executed, 16,564 passed, zero failed, two skipped**. Recovery passed once in **6.17 seconds**; metadata allocations passed in **13.81 seconds**. Skips remain `TestRunAgentRun_RealClaude` (requires `PYRY_E2E_REAL_CLAUDE=1`) and `TestMemoryPathsCaseInsensitive` (case-sensitive filesystem). No focused process or synthetic contender overlapped this package run.

The subsequent `go test -race -json -count=1 -timeout=15m ./internal/thread ./cmd/pyry ./internal/relay` finished with **exit 1**. It overlapped no other command from this run except read-only status/evidence inspection and publication-text preparation, and no synthetic contenders. At the same tested source `662b97de`:

| Package | Executed | Passed | Failed | Skipped | Elapsed |
| --- | ---: | ---: | ---: | ---: | ---: |
| `internal/thread` | 1117 | 1117 | 0 | 0 | 355.184 s |
| `cmd/pyry` | 16564 | 16561 | 1 | 2 | 344.592 s |
| `internal/relay` | 1530 | 1530 | 0 | 0 | 96.209 s |

`TestDaemonThreadQueriesRecovery` executed/passed once in **11.07 seconds**, and delayed-provider plus all deadline scenarios passed. The sole failure was the inherited `TestDaemonThreadMetadataAllocations`, `long-thread replay did not finish`, in **13.84 seconds**, already accepted as independent in the verifier's verdict and tracked by open **#3467** (confirmed with `gh issue view --repo pyrycode/pyrycode 3467 --json title,state,url`). The two skips and reasons match the separate complete cmd run above. This combined check is **red**; the green separate cmd run does not erase it. No dependency or production repair is introduced for this unrelated failure.

All processes requested in this rework have exited. Full JSON logs, count summaries, scratch-only controls and the contention driver remain under `/tmp/builder-3468/rework-*`. Final diff review found only the assigned regression test and dated plan/evidence additions in this rework. The fixing commit is pushed as `662b97de`; subsequent evidence-only changes do not change the tested source. The required numbered answer is written to `/Users/juhanailmoniemi/.codex/publish/pyrycode/verdict-handoff/rework-answers-3468-1e1e9d8a0437.md`.

## Documentation handoff
- Pending documentation stage: in `docs/knowledge/features/development-verification.md` § Diagnosing reply timeouts, fold the recovery-specific phase measurements and absolute reply-budget practice into the section, linking `docs/specs/architecture/3468-recovery-wire-wait.md`. Preserve the distinction between controlled reproduction and historical attribution.
