# Native bounded wrap-up replay

## Files read

- `internal/e2e/realclaude/interactive_stream_clear_wrapup_skipped_test.go` → `TestInteractiveStreamClearWrapUpSkipped`: unchanged native assertions, 1ms deadline and eight-second quiet window.
- `cmd/pyry/session_reset.go` → `wrapUpTextContext`, `writeResetWrapUp`: interrupt precedes possible prompt delivery; skipped storage does not prove absent input.
- `internal/e2e/realclaude/reset_recording_test.go` → `lifecycleRecordingBeforeReset`: owned action acknowledgement before original send.
- `internal/e2e/realclaude/lifecycle_recording_test.go` → `TestLifecycleTapeBrokerProcess`: actual detached broker and receipt publication.
- `internal/e2e/recorded_lifecycle_suite_test.go` → `runRecordedLifecycleSuite`: counted original root, private retention and complete artifact audit.
- `internal/e2e/internal/claudetape/lifecycle_broker.go` → `RunLifecycleBroker`: strict inputs, actual pipe forwarding, action, EOF and terminal settlement.
- `internal/e2e/internal/claudetape/lifecycle_timeline.go` → `forwardLifecycleOutput`: absolute schedules and drift rejection; no clear-profile timeline today.
- `internal/e2e/internal/claudetape/lifecycle_artifacts.go` → `AuditLifecycleArtifacts`: closed per-role file inventory must remain unchanged.
- `internal/e2e/recorded_pool_test.go` → `buildRecordedPoolBinary`: revision-stamped original helper compilation.
- `docs/knowledge/features/e2e-realclaude.md`, `e2e-harness.md`, `development-verification-captures-and-live-evidence.md`: missing-artifact arming, counted proof and durable retention.
- `docs/testing/daemon-recordings.md` § Session rotation and native transcript snapshots: reviewed fixture and physical termination contract.

## Context

One deliverable is faithful bounded clear replay, including its strict regression and sole-consumer native measurement. The current fixture has no timeline; historical diagnostic source `7d8e087f18f7cceb8b789aeae01ea887e7fac932` rejected a user frame at player 25/25 with 1172 pending bytes. This does not identify a native timing cause. Original fixture SHA-256 is `c43f952e2b68e64e0efea65ff9c60dc989fbd86cf4807480256e787b684b1ea6`; preserve its bytes and historical failure identity.

The prior host originals under `/tmp/builder-3665` are available here. Retained copies and a digest manifest live under `/tmp/builder-3673/historical`; fleet parent bundle `b9e2dc27d81a8968c5d333020e68ba6e2e60e0c10384d3d0259314f2d4ef1c42` contains the run log, not those witnesses. Recovery does not turn historical failures into native timing evidence.

Overlap: #3672 changes broker diagnostics and replay pipe completion in `lifecycle_broker.go`; this probe adds local observation hooks without consuming that repair. No dependency on #3666 or #3667. No daemon production changes. No ADR needed until native observations determine whether a new timing contract is necessary.

Sizing: forecast 750 total written lines including plan, capture hooks, readers and tests, with the later repair contingent on measurement; at most three exported support types, fewer than ten consumers, three acceptance criteria, no new replay reject branches in this first pass. Reassess actual fixture delta before repair; the verification floor keeps sole-consumer capture with this ticket.

## Design

First pass only: prospectively instrument native capture; do not change replay behavior, timing/schema acceptance, fixture frames or deadlines. Add a bounded observation collector in claudetape with fixed stage/kind/error enums and numeric timestamps/byte counts. The broker records complete input arrival, actual forwarding success/failure, complete output forwarding, action acknowledgement and each actual EOF. Correlate interrupt response IDs only in memory; never serialize IDs or provider content. Cap records, reject unknown enums and out-of-range numbers, and deny-scan before exclusive private publication.

The original root automatically delegates to the existing counted lifecycle capture harness when `testdata/clear-wrapup-skipped-native.json` is absent and no recording config owns this invocation. Compile the e2e driver, run only its clear-wrapup-skipped case, and let its existing stamped helper execute the unchanged native root. No live run belongs to this builder. Use a fresh durable `/tmp/pyry-3673-native-*` directory, never a disposable HOME, for candidate recording, counted root log, broker observations and root observations. Do not overwrite a reviewed fixture. Preserve diagnostics even when native capture fails; fail the probe loudly without a usable record.

Root observations mark the original action/send boundary and completed reset assertions. After the root finishes, classify only existing daemon wrap-up log event names into a fixed outcome; do not retain log lines or arbitrary error text. The configured deadline is 1000000ns. Internal deadline activation/expiration and pre-write attempts remain unknown unless independently observed; broker arrival and successful pipe forwarding establish only their named boundaries. Actual provider stdout/stderr EOF and the existing ESRCH/receipt witnesses establish physical predecessor termination, not a synthetic completion flag.

Capture artifacts are observations, never replay authority. On return, inspect the complete native capture, root counts and hashes; append a prospective Revisions entry specifying the measured discrepancy and chosen replay contract BEFORE any replay repair. Commit reviewed artifacts with matching reader/schema changes and controlled regression. Preserve the old fixture if replacement is necessary. Existing absolute causal schedules and drift rejection remain intact.

## Concurrency model

Broker observations are appended on its existing single I/O loop, published once after it returns. No new goroutines. The native probe runs one bounded driver subprocess with cancellation, WaitDelay and a process-group shutdown path. Existing harness ownership, abort and broker Join remain authoritative.

## State transitions and identity reuse

| Event | Race coverage |
| --- | --- |
| Repeated observation, overflow, invalid or incomplete terminal evidence | `TestWrapUpObservationValidation` |
| Interrupt request/response correlation and user input arrival versus forwarding | `TestWrapUpObservationKinds` |
| Foreign action / peer / repeated launch | Existing lifecycle ownership and foreign-action controls |
| Original clear, predecessor termination, successor on same conversation | `TestRecordedClaude/clear-wrapup-skipped` after repair; original native root in dispatcher gate |
| Blocked pipes and detached helper failure cleanup | `TestLifecycleBlockedPipesFailWithinWriteDeadline`, `TestLifecycleFailedExecReapsDetachedBroker` |

## Error handling

Observation publication errors fail capture, never weaken replay or mark missing phases complete. Store static error classes only. Missing/invalid reviewed observations cannot disarm capture successfully. Native root skip, zero execution, missing candidate or failed broker/root audit is not usable evidence. Preserve partial failed records outside homes for diagnosis.

## Testing strategy

Write offline enum/bound/completion and correlation controls first, observe them fail before implementation. Run affected claudetape controls with Linux `-race -count=1`, tagged native-package focused controls and compilation, vet and binary build. Retain a current focused original replay failure before handoff. Native original root and complete suites belong to dispatcher; macOS is outside scope. After evidence returns, add the distinguishing replay regression, run affected broker/timeline controls and the exact original recorded case uncached under race.

## Open questions

- Did native wrap-up input arrive and reach the provider pipe? Await native records.
- Which measured causal schedule reconciles strict inputs with the 1ms deadline? Await native records; do not infer a delay.
- Does repair need timing-only metadata or a replacement native carrier? Reassess written work once measured.

## Documentation handoff

Pending documentation stage: update `docs/testing/daemon-recordings.md`, “Session rotation and native transcript snapshots”, the `clear-wrapup-skipped` paragraphs: describe the measured native phase/input contract used by replay, capture provenance and validation, retaining the 1ms deadline, strict input and physical termination guarantees. Distinguish measured observations from unknown phases and offline replay proof from live acceptance.

## Security review

**Verdict:** PASS

- Trust boundaries: observations cannot authorize frames or completion; existing `Player.Input`, kernel peer validation and `AuditLifecycleArtifacts` remain authoritative. Validate closed diagnostic enums and bounds before publication.
- Tokens/secrets: never serialize protocol text, request IDs, argv, environment or raw errors; IDs used for interrupt correlation stay in memory. Existing native tape privacy checks still apply independently.
- Files: private fresh durable directories and exclusive 0600 files; bounded no-symlink readers and deny scan. Diagnostic files live outside the closed artifact inventory and cannot masquerade as role receipts.
- Subprocesses: fixed test selector and package, argument vectors, no new shell interpolation; bounded context and group cancellation. Existing provider environment/auth handling remains unchanged.
- Cryptography: SHA-256 only for provenance; no new keys, authentication or secret comparisons.
- Network/I/O: no new listener or network surface; preserve existing broker pipe deadlines. Collector bounded by frame and byte limits.
- Errors/logs: fixed outcome/error enums only; failed publication is explicit, missing observations remain unknown.
- Concurrency: single broker loop owns collector; no new shared state or goroutines. Partial records cannot pass completion validation.
- Threat model: test-only probe measures owned native processes; remote protocol, daemon credential lifecycle and production reset behavior are unchanged.

**Reviewer:** builder (self-review per security-review checklist)
**Date:** 2026-10-20
