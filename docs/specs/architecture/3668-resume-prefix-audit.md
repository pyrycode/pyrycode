# Session-resume prefix lifecycle investigation

## Files read

- `internal/e2e/realclaude/lifecycle_recording_test.go` → `readLifecycleRecordingConfig`, `finishLifecycleRecording`: source validation and seal ordering.
- `internal/e2e/realclaude/resume_recording_test.go` → `lifecycleRecordingResumeRole`, `lifecycleRecordingResumeSnapshot`: same-session ownership after actual eviction.
- `internal/e2e/realclaude/interactive_stream_resume_after_eviction_test.go` → `TestInteractiveStreamResumeAfterEviction`: original recall assertions and five-second activation timer.
- `internal/e2e/realclaude/fallback_worker_recording_test.go` → `settleLifecycleRecording`: session-resume has no added settlement wait.
- `internal/e2e/recorded_lifecycle_suite_test.go` → `runRecordedLifecycleSuite`: original process, evidence retention and complete audit.
- `internal/e2e/internal/claudetape/lifecycle_broker.go` → `RunLifecycleBroker`: live seal and real EOF requirements.
- `internal/e2e/internal/claudetape/lifecycle_artifacts.go` → `AuditLifecycleArtifacts`: exact inventory and pinned digest.
- `internal/e2e/internal/claudetape/action_files.go` → `publishClosedFile`: publication ordering.
- `docs/knowledge/features/e2e-harness.md`, `e2e-realclaude.md`, `development-verification.md`, `docs/testing/daemon-recordings.md`: preserve original failures, source ownership, literal inputs, timing and terminal proof.

## Context

One behavior: the original session-resume recording seals the live resumed
provider and retains its exact pinned prefix through original cleanup. Historical
failures have been recovered under `/tmp/builder-3668/historical/`; they remain
failed. Main includes #3667. No production defect is established. No ADR needed.
Branches #3672 and #3673 overlap the broker/recording files with additive,
different diagnostics; no dependency on either design is established.

## Design

First run the unchanged focused Linux replay. Add bounded content-free diagnostic
observations at original finish entry, after configuration validation, before
seal, after prefix audit and after cleanup. Preserve current provider/broker PIDs,
observed process liveness, prefix/receipt validity and exact expected/observed
child names/states outside disposable homes. Missing EOF observations stay
unknown; a terminal broker observation may establish actual EOF.

Examine cold helper startup, elapsed completion-to-seal and host load. Record
the failed predicate and causal evidence in a committed revision before repairing
behavior. A passing replay is not causal proof. The prospective repair and its
synchronized regression will be specified in that revision. Return for refinement
if independent causes or daemon changes are necessary.

No fixture, timeout, original action/order, kernel ownership check, permitted
inventory or receipt contract may be relaxed. Estimate: 550 total written lines,
at most 1 exported diagnostic type, 4 callers, 3 acceptance criteria, under 10
new reject branches. Reassess after investigation and before repair.

## Concurrency model

Original test and broker processes retain their current lifetimes and cleanup.
Diagnostic snapshots run synchronously at bounded stages; any broker observation
is emitted by its existing loop. No new background worker is planned.

## State transitions and identity reuse

| Event | Race proof |
| --- | --- |
| Bootstrap eviction and resumed launch under the same session UUID | `TestRecordedClaude/session-resume`, `TestResumeSnapshotAuditRequiresOwnedEvictionAndExactInput` |
| Live resumed prefix seal followed by actual original cleanup | `TestLifecycleArtifactsPinPrefixThroughOriginalCleanup`, synchronized regression named in causal revision |
| Repeated/foreign launch, missing or altered artifacts/receipts | Existing lifecycle launch and artifact rejection controls |

## Error handling

Diagnostics cannot authorize a provider or turn unknown observations into success.
Observation failures are retained as unknown. All existing audit and terminal
errors remain failures. No retry or added wait can substitute for a live seal.

## Testing strategy

Use managed admission, uncached race runs, external private evidence directories,
and `TestRecordedClaude/session-resume`. Run affected claudetape ownership,
snapshot and artifact controls, including the pinned-prefix cleanup test. Write
and observe a synchronized regression fail before the demonstrated repair, then
pass with it. Run Go vet/build and tagged compilation on Linux. Record source,
binary/fixture hashes and executed/pass/fail/skip counts. Full-module acceptance
belongs to the dispatcher; no live-provider capture is required.

## Open questions

- Resolved by the 2026-10-20 causal investigation below.

## Revisions

### 2026-10-20 — causal evidence before repair

Unchanged source `998460bc38fb322aa03d4db0717fe2ca3c3e7f08` reproduced the
child-inventory failure after recall; retain `/tmp/builder-3668/baseline/`.
Diagnostic source `ee66a5762572295ce54569fb6353aec1e042a785` reproduced
the liveness failure, one original root executed/failed, zero passed/skipped;
retain `/tmp/builder-3668/observed/`. Helper SHA-256 was
`33b9ee281c99e17cd17e9158789e9811875a92d33f1910d9e83485ee0005a3bc`;
daemon SHA-256 was `0928376df3d28ce0405717b5f505041a41f3f8969fe1557dac8178d7db5f2f0f`.
Reviewed fixture SHA-256 remains
`1a1e6faee135a83b4deba4b2a84630bc846526e69c5db8c793e72b7eb80cb002`.

Recall ended at 08:16:41.088 UTC. At finish entry, resumed owner 196163 and
broker 196169 were present with no prefix/receipt. After configuration validation,
1.459 seconds after entry, both were absent and a failed receipt existed.
The broker independently observed stdout/stderr EOF and no seal. Bootstrap
owner 196035 had already ended successfully with broker 196084, as required.
The only work between these snapshots was `lifecycleRecordingCurrent`, whose
validator reopens and hashes the entire source executable on every hook.
The activation-based five-second timer continues during this read; cold resumed
helper/broker startup consumes most of that window. Host load observations were
approximately 10–15 (one-minute load); incidental contention is recorded, not
used to waive a failure. The historical inventory symptoms are consistent with
an early resumed receipt, but their exact missing/extra names remain unknown.

Repair contract: validate the current executable's content identity once per
process, before that process can pass its first configuration validation. Reuse
only that digest for subsequent hooks; still privately read, decode and validate
the complete config, root, revision, credentials and expected source digest on
every call. A different expected digest must reject even after a cache hit, and
a failed initial source read must stay failed. This removes repeated hashing
from seal entry without enlarging the idle window or changing any action.
No fixture, daemon, ownership or artifact-audit behavior changes.

Synchronized regression `TestLifecycleSourceValidationDoesNotRepeatAtSeal`
blocks the first source read until explicitly released, joins initial validation,
then makes any second read fail and checks seal-time validation succeeds using
the established identity. Foreign expected identities and initial-read failures
remain rejected, including concurrent callers. Observe it fail before caching
and pass after. Re-run the original replay and all affected artifact/ownership
controls. Strengthen observations with numeric eviction count and post-cleanup
stage, and retain an exact private JSON observation rather than relying on
test2json line fragmentation. Revised estimate remains below 650 written lines,
zero exported types, at most four consumers and no additional state-machine
reject branches.

## Security review

**Verdict:** PASS

**Findings:**

- Trust boundaries: `ValidateLifecycleLaunch` and kernel-authenticated broker peers remain the authority; diagnostics are observations only.
- Tokens: diagnostic fields exclude credentials, environment, argv, transcript and user content.
- File operations: external evidence uses private 0700 directories and exclusive 0600 files; closed artifact inventory and private readers remain strict. No diagnostic file belongs in `recording-children`.
- Subprocesses: retain existing owned processes, cancellation and EOF cleanup; no diagnostic PID may become a signal target.
- Cryptography: retain SHA-256 source and pinned-artifact validation; no keys or nonces change.
- Network/I/O: existing bounded broker pipes remain; diagnostics are capped by the closed role/name set with bounded unknown-name reporting.
- Errors/logs: fixed stages and numeric identities only; arbitrary file bytes and errors cannot enter diagnostics.
- Concurrency: stage snapshots do not certify atomicity; unknown states remain unknown. No new goroutines or lock order.
- Threat model: this is isolated test-harness evidence, with unchanged daemon/mobile security boundaries; unrelated product hardening is outside this ticket.

**Reviewer:** builder (self-review per security-review checklist)
**Date:** 2026-10-20
