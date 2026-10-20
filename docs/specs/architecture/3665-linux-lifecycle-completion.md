# Linux lifecycle broker completion

## Files read

- `internal/e2e/recorded_lifecycle_suite_test.go` → `runRecordedLifecycleSuite`: original-root execution, broker ownership, immutable audit and cleanup barriers.
- `internal/e2e/realclaude/lifecycle_recording_test.go` → `TestLifecycleTapeHelperProcess`, `TestLifecycleTapeBrokerProcess`, `finishLifecycleRecording`: source-bound launch and completion evidence.
- `internal/e2e/internal/claudetape/lifecycle_broker.go` → `RunLifecycleBroker`: nonblocking forwarding, action acceptance, provider EOF and terminal completion.
- `internal/e2e/internal/claudetape/lifecycle_exec.go` → `execLifecycleChild`: descriptor handoff, stand-in lifetime and broker acknowledgement.
- `internal/e2e/internal/claudetape/lifecycle_artifacts.go` → `AuditLifecycleArtifacts`, `ValidateLifecycleLaunch`: closed inventory and kernel-peer/source identity validation.
- `internal/e2e/internal/claudetape/lifecycle_test.go` → `TestLifecycleExecKeepsActualPIDAndJournalsAfterKill`: positive and hostile controls.
- `internal/e2e/recorded_pool_test.go` → `buildRecordedPoolBinary`: race-instrumented original tests and source stamp.
- `docs/knowledge/features/e2e-harness.md`: spawned binaries require uncached execution; focused root proof must actually reach the child.
- `docs/knowledge/features/development-verification.md`: retain source identities and original failed evidence.

## Context

The clear scenarios fail at an inherited Linux broker boundary. Existing receipts hide the failing operation. One deliverable is a broker that completes the original clear scenarios with genuine ownership and terminal evidence. Diagnosis and regression checks stay with that repair. No overlapping feature branches currently edit the target files. No decision record is needed.

## Design

First add a bounded terminal broker diagnostic callback, separate from authoritative receipts. It reports a static stage, classified error, frame and pending-byte counts, player progress, action/seal state and EOF booleans; it never reports protocol bytes or raw errors. The parent retains these diagnostics and an allowlisted set of existing role/job/action/completion witnesses outside temporary homes on both success and failure, with revision and executable hashes.

Run the two original race-enabled recorded scenarios before changing forwarding or completion behavior. Use the retained operation and ownership evidence to establish the defect, then append the concrete repair to Revisions before implementing it. Repair only demonstrated pipe handoff/completion behavior; do not widen deadlines, fabricate evidence, change fixture content, skip assertions or weaken terminal audit. A separate substantial cause requires scope reassessment.

Expected total work: 500–650 written lines including plan/tests; at most one exported diagnostic type, no changed consumer signatures, three acceptance criteria and no new state-machine reject branches. Rechecked before plan commit: within all five limits.

## Concurrency model

The broker remains a single forwarding loop. Diagnostics run synchronously at return. Outer-owned brokers remain joined through `LifecycleBrokerServer`; retention runs after abort/join cleanup and before temporary-home deletion. No new goroutine or lock ordering.

## State transitions and identity reuse

| Event | Race-enabled proof |
| --- | --- |
| Original reset kills predecessor, successor reuses conversation identity | `TestRecordedClaude/clear-wrapup-skipped`, `TestRecordedClaude/clear-reset` |
| EOF and pipe closure complete the current broker exactly once | `TestLifecycleExecKeepsActualPIDAndJournalsAfterKill` and focused regression selected from diagnosis |
| Repeated or foreign action/launch and cancellation | Existing lifecycle broker/server controls, including unauthorized kill, foreign PID, abort and blocked-pipe variants |

## Error handling

Diagnostic failures must fail retention visibly, never authorize completion. Keep generic authoritative receipt errors and all physical cancellation, peer, role-limit and audit checks. Retention uses exclusive private files, bounded reads and a fixed witness allowlist; no native transcripts, environment or credentials are copied.

## Testing strategy

Retain failing and passing original Linux scenario logs with exact revisions, binary hashes and executed/passed/failed/skipped counts. Add a controlled regression for the demonstrated cause and observe failure before repair. Run affected claudetape controls under race detection, the two original recorded scenarios, `go vet ./...`, and a build of `cmd/pyry` outside the worktree. The dispatcher/verifier owns the complete suite; other operating systems are outside this ticket's verification scope.

## Open questions

- Which broker stage first fails in each original scenario? Resolve with retained diagnostics before behavioral edits.
- Is there one shared handoff/completion cause? Reassess only if evidence proves independent larger repairs.

## Security review

**Verdict:** PASS

**Findings:**

- Trust boundaries: `ValidateLifecycleLaunch` continues binding the kernel peer, source hashes and fixed role paths. Diagnostics supply no authority to the artifact audit.
- Tokens/secrets/logging: SHOULD FIX during build: classify errors without serializing raw errors or protocol content; test this boundary. Replay retains its credential scrub.
- File operations: use fixed role/suffix allowlists, existing private bounded readers, exclusive 0600 destinations and 0700 directories. Do not follow source symlinks or copy arbitrary home files.
- Subprocesses: preserve direct exec arguments, owned PID identity, physical cancellation and join paths. No new shell execution.
- Cryptography: existing SHA-256 source identity remains unchanged; no new keys, nonces or secret comparisons.
- Network/I/O: preserve Unix peer verification and broker descriptor limits. No new network interface; diagnostic output is one bounded terminal record per role.
- Concurrency: synchronous diagnostics and cleanup retention add no worker; broker/owner closure ordering must be proved by regression controls.
- Threat model: this is offline test infrastructure; production mobile/relay security contracts remain enforced by existing code. No newly deferred security finding.

**Reviewer:** builder (self-review)
**Date:** 2026-10-20
