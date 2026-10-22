# Resume snapshot predecessor retirement

## Files read

- `internal/e2e/realclaude/resume_recording_test.go` → `lifecycleRecordingResumeSnapshot`, `lifecycleRecordingResumeRole`: snapshot publication and resumed-role ownership checks.
- `internal/e2e/realclaude/lifecycle_recording_test.go` → `waitLifecycleReceipt`, `finishLifecycleRecording`: receipt validation and other lifecycle consumers, which remain unchanged.
- `internal/e2e/realclaude/daemon_recording_test.go` → `daemonRecordingRead`: private, bounded, strict receipt decoding.
- `internal/e2e/realclaude/interactive_stream_resume_after_eviction_test.go` → `TestInteractiveStreamResumeAfterEviction`: original WARN gate, five-second idle timer and turn deadlines.
- `internal/e2e/recorded_lifecycle_suite_test.go` → `runRecordedLifecycleSuite`: replay isolation and external retained evidence.
- `docs/knowledge/features/e2e-harness.md` § lifecycle observations: receipt publication, EOF and teardown are separate observations; preserve strict native input and final audits.
- `docs/knowledge/features/e2e-realclaude.md` and its resume-after-eviction topic: tagged original-root coverage and native resume contract.
- `docs/knowledge/features/development-verification-prove-that-tests-distinguish-the-change.md`: synchronize the regression and retain old-code failure.
- `docs/testing/daemon-recordings.md` § Idle eviction and literal native resume inputs: existing ownership, EOF and deadline contract.

CodeGraph exploration did not resolve the tagged helper symbols; their current source was read directly. Fetched feature branches have no overlap with the planned files.

## Context

A successful bootstrap receipt can precede daemon reaping. The current immediate PID check incorrectly rejects that valid ordering. This is one behaviour slice with its regression, not a change to production supervision or other lifecycle boundaries. No decision record is needed.

Sizing: approximately 350 written lines including tests and this plan; zero exported types, one consumer, two acceptance criteria and fewer than ten reject branches. This fits the five ceilings, checked again against this plan before commit.

## Design

Add `waitLifecycleResumePredecessor(path, owner, deadline, observe) (LifecycleReceipt, error)` beside the snapshot hook. The hook supplies one deadline of now plus 15 seconds and `syscall.Kill`; tests can synchronize the signal-zero observation. Read the receipt with the existing strict reader. Require success, no error, nonzero frames and the exact job owner before examining the process. Only ESRCH completes; nil remains pending and all other observations fail. Receipt waiting and retirement polling consume the same deadline; never restart the budget. Return a validated receipt for the existing delivery and snapshot-stage audits.

Keep snapshot read/install/publication after this barrier and retain the original resume send after the hook. Preserve the shared receipt helper, kernel claims, EOF, strict inputs, prefix and cleanup audits. Send no termination signal, retry no scenario and use no fixed settling sleep. A short bounded polling interval only schedules fresh observations.

## Concurrency model

The original test goroutine polls synchronously. No new production or recording goroutine is introduced. The regression owns one child process, held by stdin and released by EOF, and one waiter goroutine. Bounded channel operations and cleanup join both on every path.

## State transitions and identity reuse

| Event | Race-enabled regression |
| --- | --- |
| Matching receipt while predecessor remains present, then retirement | `TestResumePredecessorWaitsForRetirement`: synchronized signal-zero observations of a real owned child; completion only after EOF and Wait |
| Invalid owner or terminal status | `TestResumePredecessorRejectsBoundary`: wrong owner, failed status, error text and zero frames reject before observation |
| Unknown process observation | `TestResumePredecessorRejectsBoundary`: EPERM rejects, never interpreted as disappearance |
| Receipt absent or predecessor still present at expiry | `TestResumePredecessorDeadline`: one short absolute deadline bounds both phases |

The original bootstrap job pins the owner throughout this single boundary; no ID allocation, reuse or signal-based termination is added. A reused PID remains present and cannot authorize publication.

## Error handling

Return descriptive boundary errors to the snapshot hook, which fails the original test. Unavailable/invalid receipt reads remain pending under the existing reader semantics. Decodable failed or foreign receipts and unknown process observations fail immediately. Expiry fails without snapshot publication or resume send.

## Testing strategy

Write the synchronized regression first, extracting the existing immediate-check semantics into the new helper so its old-code failure can be retained before the polling repair. Run focused uncached race controls with `e2e_realclaude`; no live credentials are needed. Run the named `TestRecordedClaude/session-resume` replay through managed admission with fresh isolated homes and external evidence. Preserve exact revisions, selections, JSON counts, original failure and repaired evidence; post the causal conclusion to the issue. Run vet and build, including tagged vet for the affected helper. Full gates remain dispatcher-owned. Linux only; macOS is outside this ticket's verification scope.

## Open questions

None.

## Documentation handoff

Pending documentation stage: in `docs/testing/daemon-recordings.md`, “Idle eviction and literal native resume inputs”, clarify that successful bootstrap receipt publication can precede provider disappearance; the bounded owned-predecessor retirement barrier must finish before snapshot publication or the resume send. Preserve the existing EOF, ownership and deadline wording.

## Security review

**Verdict:** PASS (builder self-review, 2026-10-22).

- Trust boundaries: `daemonRecordingRead` retains strict private bounded receipt decoding; match the receipt owner to the original job before any process observation.
- Tokens/secrets: no credentials, payloads or new secret handling; errors contain only boundary classifications.
- Files: existing native snapshot paths, no-follow installation, exclusive publication and exact inventories remain unchanged; no new file writes in the barrier.
- Subprocesses: observation uses signal zero only on the existing owned job PID. The regression retires its own child using stdin EOF and joins Wait; no termination signal is introduced.
- Cryptography: no cryptographic changes; snapshot hashes and kernel claims remain intact.
- Network/I/O: no network change; polling and receipt decoding retain finite bounds.
- Errors/logs: foreign/failed receipts and unknown process states fail closed; ESRCH alone authorizes retirement.
- Concurrency: one absolute deadline covers receipt and retirement, with no background recording task. Test channels are bounded and joined during cleanup.
- Threat model: no relay/auth contract changes. PID reuse cannot produce a false disappearance because a present PID remains pending. Existing final source, ownership and cleanup audits remain mandatory.

No MUST FIX, SHOULD FIX or deferred security findings.

## Revisions

- 2026-10-22: added an expiry control for ESRCH observed after the deadline; a successful observation must itself finish within the shared budget. No contract or deadline changed. QMD timed out, so the runtime-authorized repository/document search fallback supplied the topic context. The managed selector rejects per-component regex anchors; focused replay uses exact `^TestRecordedClaude$` with the sole `session-resume` scenario selected by environment. The baseline's accepted prefix selector also ran the startup control; preserve its actual counts separately.
