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

## Revisions

### 2026-10-20 — retained evidence requires two behavior slices

No forwarding, deadline, fixture or completion behavior was changed. Diagnosis resolved the open questions with two distinct failing invariants, rather than one shared completion defect:

- On `29b7757da5a39fc7e349a9af57451a5809d8b5f1` and `7d8e087f18f7cceb8b789aeae01ea887e7fac932`, `clear-wrapup-skipped` rejects an extra `user` stdin frame after all 25 recorded frames. The latter diagnostic reports `match-input`, player 25/25, 1172 pending input bytes, accepted action and no EOF. The original root still proves skipped wrap-up and unchanged prior note. Its fixture has no output timeline; source inspection shows the interrupt acknowledgement precedes an attempted wrap-up write under the original 1ms deadline. A timing-sensitive replay contract is implicated; native timing must be measured rather than fabricated or replaced with a sleep.
- `clear-reset` passes four focused runs, each with all 83 bound frames, then reproduces on the third additional repeat at `7d8e087f18f7cceb8b789aeae01ea887e7fac932`: `forward-provider-input`, `broken-pipe`, 82 forwarded frames, player advanced to 83/83, 105 pending input bytes, accepted action and no sampled EOF. The final `get_context_usage` stdin write hits EPIPE before its released response can be forwarded. The original root subsequently cannot recall from the successor. This is an actual replay provider-pipe lifetime defect, independent of the first scenario's extra user frame.

Evidence is outside disposable homes under `/tmp/builder-3665/`: `baseline/`, `baseline-kind/`, `reset-repeat-1/`, `reset-repeat-2/`, and `reset-repeat-3/`. Each scenario directory has original test2json output plus per-role diagnostics, source/daemon binary hashes and allowlisted job, role, action, acknowledgement and completion witnesses. Original earlier failures on `03eda6c1b9586c4389b11b0cf86d97a954d97ec0` remain preserved in the #3660 evidence path from the issue. No claim about session-rotation inheritance was made.

The affected package race run executes 721 leaf tests: 720 pass, one fails, zero skip. Its sole failure is the existing stalled-transfer control race, filed separately as #3670. The new retention test passes 1/1 without skips. Both tagged harness packages and the ordinary module vet/build pass on Linux. Full acceptance remains unexecuted and belongs to the dispatcher/verifier.

Scope reassessment under `behaviour-slices-v1`: the issue has no parent or grandparent, so split-depth does not prevent a split. Proposed independently useful children, each retaining security review, diagnostics, actual original scenario proof and all relevant hostile/physical controls:

1. Repair replay provider-pipe completion after original rotation, proved by `clear-reset`, with a controlled death-before-final-query regression. Keep shared diagnostics/retention with this consumer; expected 600–750 written lines, at most one exported diagnostic type and no production change assumed.
2. Repair the bounded wrap-up replay contract, proved by `clear-wrapup-skipped`, preserving the 1ms deadline and strict native input evidence. Establish and retain native phase/timing evidence before choosing capture/timeline changes; never accept an unrecorded user frame or invent a delay. Forecast 400–600 lines including native timing schema/fixture deltas, subject to refinement against actual capture needs.

These can land and be verified independently while the application remains functional. Returning the original combined ticket for refinement preserves both failures and their verification floors. Withdraw the diagnostic-only branch diff with an ordinary revert before handback; its committed history and retained patch remain available to the children. No PR represents a completed repair.
