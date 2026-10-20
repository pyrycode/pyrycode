# Linux lifecycle output timing

## Files read

- `internal/e2e/internal/claudetape/lifecycle_timeline.go` → `waitLifecycleOutput`, `forwardLifecycleOutput`: absolute deadlines and three 100 ms checks.
- `internal/e2e/internal/claudetape/lifecycle_broker.go` → `RunLifecycleBroker`: readiness, causal input epochs, synchronous witnesses and released outputs.
- `internal/e2e/internal/claudetape/lifecycle_timeline_test.go` → existing late-input, overshoot, forwarding and anchor controls.
- `internal/e2e/internal/claudetape/conversation_settlement.go` → `NewConversationCompletionObserver`: completion receipts after native forwarding.
- `internal/e2e/realclaude/lifecycle_recording_test.go` → `TestLifecycleTapeBrokerProcess`: private receipt error filtering.
- `internal/e2e/realclaude/rotation_recording_test.go` → `lifecycleRecordingInputWitness`: synchronous private delivery publication.
- `internal/e2e/recorded_lifecycle_suite_test.go` → `runRecordedLifecycleSuite`: original roots, failure preservation and cleanup.
- `internal/e2e/recorded_pool_test.go` → `buildRecordedPoolBinary`: race-instrumented original assertions and source stamp.
- `docs/knowledge/features/e2e-harness.md`, `e2e-realclaude.md`, `development-verification.md`: original root ownership, counted evidence and artifact survival; focused passes cannot replace full gates.

## Context

Both conversation replay roots delivered replies but failed settlement on candidate
#3660 and unchanged main `03eda6c1`. Existing messages identify a missed absolute
deadline without showing where its budget was consumed. This ticket preserves
one invariant: forwarding recorded output on its genuine causal absolute schedule.
Diagnosis and its regression belong with the repair. No fixture or daemon changes.
Historical host contention and preceding sequences remain unknown unless recorded.
No decision record is needed.

## Design

First add typed, bounded numeric timing failure evidence: output ordinal, causal
input anchor, origin-relative due/current nanoseconds, signed lateness, failure
stage, readiness startup work and completion/input-witness elapsed work. Preserve
it through the broker receipt and outer test log before temporary HOME cleanup.
Never include frame bytes, paths, arbitrary errors or environment values. Log the
current source revision and actual helper/daemon SHA256 for failed and passing runs.

Reproduce both originals with race detection, fresh evidence directories and exact
selectors. Use the measurements to isolate the staging boundary before implementing
the repair. Record the resulting concrete repair contract in a prospective Revisions
entry before changing scheduling. Keep epochs, causal order, absolute offsets and
all three 100 ms rejection checks. Add a controlled regression at the demonstrated
boundary, using existing owned process/pipe helpers when integration is necessary.

No feature branch currently overlaps the four proposed existing files after fetch.
#3665 completion and #3667 worker inventory are separate concerns; do not change them.
Sizing: one behaviour, four acceptance criteria, approximately 500–650 written
lines including this plan, at most two exported diagnostic types, two consumer
updates, and at most five new reject branches. Reassess before the repair revision.

## Concurrency model

The broker currently owns timeline origins and forwarding in its single event loop.
Diagnostics remain local to that loop and cross the process boundary in its final
private receipt. No diagnostic goroutines or additional process owners. Any measured
scheduling repair must retain bounded shutdown and publication failure propagation.

## State transitions and identity reuse

| Event | Race proof |
| --- | --- |
| Readiness to first native output | Existing absolute deadline controls plus original conversation roots |
| Forwarded user creates an epoch; subsequent outputs reuse it | `TestLifecycleInputTimelineDoesNotResetAnExistingUserPhase` |
| Invalid stale/future anchors | `TestLifecycleInputTimelineBindsActualPrecedingUser` |
| Late input, scheduler overshoot, final write backpressure | `TestLifecycleTimelineUsesAbsoluteDueTimes`, `TestLifecycleTimelineRejectsLateFinalForward` |

## Error handling

Timing errors fail closed at their original check with numeric evidence. Arbitrary
provider and I/O errors retain generic receipt wording. Final settlement and owned
process cleanup remain required; no retries that reset an epoch and no skipped roots.

## Testing strategy

Write diagnostic controls first and observe failure, then implement diagnostics.
Retain original failed race runs, diagnose the cause, write its failing regression,
then implement and run affected claudetape controls and both unchanged originals
with `-race -count=1` on Linux through test admission. Retain source identities,
binary hashes, exact selection, execution/pass/fail/skip counts and evidence paths.
Run `go vet ./...`, tagged touched-package vet and `go build -o /tmp/builder-3666/pyry ./cmd/pyry`.
Full module and complete acceptance gates belong to the verifier/dispatcher.

## Open questions

- Which measured stage consumes the native output budget? Resolve from numeric
  diagnostics and record the concrete repair before implementing it.

## Security review

**Verdict:** PASS

**Findings:**

- Trust boundaries: `DecodeLifecycle` still validates recordings; diagnostic fields
  are numeric values and a closed stage enum generated by broker timing checks.
- Tokens/secrets: no credential access; no provider content or raw errors in the
  diagnostic. Replay keeps `recordedPoolOfflineEnv` credential stripping.
- Files: existing private control publication and bounded receipt reader are reused;
  no new user-derived paths, file modes or filesystem authority.
- Subprocesses: existing fixed helper entrypoints, peer/owner checks and bounded
  cleanup remain; no shell invocation or new process launch.
- Cryptography: no changes; SHA256 records executable identity, not authorization.
- Network/I/O: no network changes; real pipes and bounded forwarding remain.
- Errors/logs: SHOULD FIX: only the typed timing diagnostic may survive filtering;
  arbitrary errors must continue to become the generic lifecycle boundary failure.
- Concurrency: single broker owner; no shared diagnostic mutation or new locks.
- Threat model: offline test support only; live credential and relay policy unchanged.

**Reviewer:** builder (self-review)
**Date:** 2026-10-20
