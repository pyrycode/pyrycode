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
- Rework file review (finding 1): `ReadWrapUpObservationJSON` must open with `O_NOFOLLOW|O_NONBLOCK`, then validate descriptor regularity, size and identity against its initial `Lstat` before any read. A replaced FIFO must not block opening; a replaced symlink must not be followed. Deterministic replacement controls run in a bounded helper process. The original check-then-open design did not meet this contract.
- Rework retention review (finding 3): retain only the bounded valid prefix on event/time exhaustion, with a closed serialized truncation reason. Partial diagnostics may be saved, but both complete broker validation and the root's phase validator must reject any truncated record. No truncation can authorize input, ownership or settlement.

**Reviewer:** builder (self-review per security-review checklist)
**Date:** 2026-10-20

## Revisions

2026-10-20, capture retention: publish the reviewed-candidate paths `internal/e2e/realclaude/testdata/clear-wrapup-skipped-native.json`, `internal/e2e/realclaude/testdata/clear-wrapup-skipped-native-root.json` and `internal/e2e/realclaude/testdata/clear-wrapup-skipped-native-suite.json` only after successful native root, outer audit and complete validated observations. Durable originals remain under `/tmp/pyry-3673-native-*` with `driver-counts.json` and `clear-wrapup-skipped-capture.jsonl`. The missing broker fixture arms capture; a present but incomplete or mismatched trio fails. The builder must recover and review all three with their source hashes before replay changes, rather than rely on files surviving a gate checkout.

Historical recovery verified all 160 witnesses pinned by the original index with zero missing/mismatched hashes; 161 files including the index are copied under `/tmp/builder-3673/historical`. This adds availability evidence only. Native timing and the repair contract remain open pending the dispatcher capture.

The managed launcher rejects the ticket's double-anchored subtest expression. The supported `^TestRecordedClaude/clear-wrapup-skipped$` selects the same recorded scenario (and the sibling startup control); retain actual counts and both selector outcomes. No timing, input matching or settlement behavior changes in this pass.

Capture provenance also logs only source revision and helper/daemon SHA-256 values from `runRecordedLifecycleSuite`, so a failed replay retains binary identity after its private home and helper executable are removed.

Focused publication control exposed that `PublishLifecycleControl` requires a 0700 parent, while repository `testdata` and `testing.TempDir` need not have that mode. Reviewed Git files also become 0644 on checkout. Use a separate observation-only atomic exclusive publisher and bounded no-symlink JSON reader for sanitized diagnostic fixtures. Keep private authorization-control readers and directory requirements unchanged. Add publication/round-trip, overwrite and symlink controls; diagnostic files still cannot authorize any lifecycle event.

2026-10-20, prospective verifier rework on `5aebc9f9`:

- Finding 1: isolate the safely opened descriptor boundary in `openWrapUpObservation`; check regular-file identity and size before reading. `TestWrapUpObservationFileReplacement` will replace an initially inspected file with another regular file, symlink, FIFO or directory. A bounded `TestWrapUpObservationFileProcess` child prevents a blocking-open regression from hanging the parent. Existing publication/round-trip and symlink controls remain.
- Finding 3: replace the in-memory overflow flag with a closed `Truncated` classification (`event_limit` or `elapsed_limit`). Stop collecting when either bound is reached without appending an invalid timestamp or discarding the prefix. `Validate(false)` and `Save` retain those diagnostics; `Validate(true)` and `wrapUpRootObservation.validate` reject them. Add round-trip retention controls for both limits and rejection of an unknown truncation class, including a truncated otherwise-complete root.
- Finding 2: merge `bdb5a8e1` incorporates main's reviewed #3667 cancellation accounting and its independent kernel ownership/registration audit. Preserve the verifier's candidate **1/0/1/0**, candidate comparison **1/0/1/0** and unchanged-base **1/1/0/0** `session-resume` results; they establish a comparative failure without its cause. Run that exact original race replay and inspect its retained launch/Wait evidence before claiming resolution. Do not weaken cardinality or invent a native worker exchange.
- Scope remains the same bounded native observation contract; replay behavior and fixtures still await dispatcher capture. Forecast roughly 230 additional written lines, no new exported types and three reader consumers. Combined work exceeds the original 800-line ceiling; the verification floor keeps this sole-consumer observation repair and its distinguishing security/retention controls together. No other remote feature branch touches the observation files. Full gates and live capture remain dispatcher-owned; Linux only.

2026-10-20, completed rework validation on `e212cdd6`: the 26 focused observation/file/root race controls passed without failures or skips. The exact original `session-resume` race replay and its outer audit passed **1/1/0/0**; retained launch evidence identifies one independently owned helper with absent registration joined to exactly one completed production SIGKILL/group-cancellation Wait. This exercises the boundary repaired by #3667, incorporated in `bdb5a8e1`; the verifier's historical comparative failures remain failed and their precise original timing remains unknown. Evidence and binary identities are retained under `/tmp/builder-3673/resume-current-session*`.

The current original `clear-wrapup-skipped` race replay remains **1/0/1/0**, after its original reset/quiet-window/note assertions, with bound frame 25 failing completion. Preserve `/tmp/builder-3673/resume-current-clear*`; this accepted inherited failure does not authorize a replay change. The native trio, measured-contract revision, subsequent replay repair and final live acceptance still require the dispatcher-owned capture/return. These validation records introduce no additional behavior, state, exported types or consumers.

2026-10-20, verifier metadata rework on `c2d9dcbd`: PR #3676's `Live tests` section now contains only `TestInteractiveStreamClearWrapUpSkipped`; all capture paths, readers and recovery instructions move below `Native capture and recovery`. The dispatcher's actual parser rejects the retained original body (seven invalid lines, no required names) and accepts the published replacement with exactly that required test and no invalid lines. This changes gate metadata only; native capture, reviewed artifacts, the measured-contract revision and replay repair remain pending.

Merge `2fe9d92c` preserves main's reviewed #3672 diagnostics and replay pipe repair alongside this ticket's observation hooks; comparison with its main parent shows only additive observation lines in both conflicted files. Linux race checks passed **909/909/0/0** claudetape leaf controls and **7/7/0/0** native root observation controls; daemon build, module vet and tagged native-package vet passed. The exact original wrap-up replay on that clean merge remains **1/0/1/0**, nested original root **1/0/1/0**, startup-inclusive total **2/1/1/0**. Reset edges, the clear transition, quiet window and unchanged 248-byte note passed; the bound diagnostic retains `match-input/rejected`, user at player 25/25, 1172 pending bytes and accepted/acknowledged action. This inherited failure still establishes no native timing contract.

Retain `/tmp/builder-3673/merge-linux-broker.jsonl`, `merge-linux-root.jsonl`, `merge-linux-clear.jsonl`, their stderr and `/tmp/builder-3673/merge-linux-clear/` with broker diagnostics, source/inventory and original-root log. Replay helper SHA-256 is `e61db1b6f0d4096d51110e6cf1e6cf8c44678eb968b2ca7e7579b68afc80acfd`; daemon SHA-256 is `ea77b7e00634f6c99bdd260c093713ff8e7bce2c059cf302835f821f550e3a72`; fixture SHA-256 remains `c43f952e2b68e64e0efea65ff9c60dc989fbd86cf4807480256e787b684b1ea6`. Published parser proof is `/tmp/builder-3673/live-selection-published-validation.json`. Full gates and live capture remain dispatcher-owned; both live labels and the documentation handoff remain pending.
