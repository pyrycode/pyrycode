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

## Revisions

### 2026-10-20 — measured completion work consumes captured output gaps

Diagnostic-only source `ad4c8b1fe0671a6f33d07d9e36807c2e14ff5a78`
reproduced both originals. Bare output 21 was 107056170 ns late; prompted output
20 was 106920991 ns late. Active-switch outputs 21 and 18 were 109860659 and
121002554 ns late. All failed before waiting, with user anchor 1. The immediately
preceding output completion validation took 29–43 ms, repeatedly exceeding the
roughly 20 ms captured output gaps. Input observation was at most 25.6 ms and
input-witness work was zero for these profiles. Startup work was before readiness.
Raw logs and original root events are under `/tmp/builder-3666/`.

`NewConversationCompletionObserver` calls `completedConversationExchange` after
every frame. That repeatedly invokes `FallbackExchanges`, full tape validation,
safety scans and production parsing on the growing prefix. Only a native result
can establish successful completion. Keep collecting every actual forwarded frame,
but derive the exchange only after a stdout result. Keep the complete derivation,
publication and final independent audit; malformed or failed results still cannot
complete. No epoch, schedule or tolerance changes.

Add a private constructor with an injected derivation function for the controlled
clock regression `TestConversationCompletionPreservesOutputSchedule`: charge 35 ms
for a real full-prefix derivation while replaying a 20 ms output cadence. The old
observer must miss its deadline; result-boundary derivation must keep every due
time and publish the same independently validated completion. Existing missing,
failed, foreign and repeated-publication controls remain required in both modes.
This adds `conversation_settlement.go` and `conversation_settlement_test.go` to
Files read and the touched surface. #3665 concerns session-clear EOF/action
completion, not this conversation observer; no branch dependency was found.

Revised size: approximately 400 written lines, one exported diagnostic type,
two receipt consumers, four acceptance criteria, no new state-machine rejects.
Security pass remains PASS: result header inspection is only a cheap admission
guard; full source validation still decides whether publication is authorized.
No frame data is added to diagnostics. Resolve the open timing question above
with this measured cause and verify the unchanged originals after repair.

### 2026-10-20 — verification and retained evidence

The open timing question is resolved by the measured repeated-prefix derivation.
The controlled failing tree is preserved at `b668be39`; it failed at output 7 in
both modes. Repair `08f448c5daf09cadcefa1a7753b90375624233db` passed all 794
affected-package test events (724 leaves) and both unchanged original roots:
2 executed, 2 passed, 0 failed, 0 skipped. Outer replay events were 3/3/0/0.
All tests used Linux, race detection and disabled caching. Module vet, tagged
vet for both touched test packages and the binary build passed.

`internal/e2e/internal/claudetape/testdata/lifecycle_timing_3666.json` retains
the numeric failures, executable/source identities, fixture hashes, exact commands,
counts, conditions and hashed external evidence paths. The evidence file changes
no executable behaviour. Historical failures remain failed. Full-module and
complete acceptance gates remain pending with the verifier/dispatcher.
