# #3020 — reply fallback cancellation lifecycle evidence

## Files read

- `cmd/pyry/reply_fallback.go` → `replyFallback.run`, `replyFallbackOutput`: bounded cancellation, private Wait seam, and completion-dependent unknown fields.
- `cmd/pyry/reply_fallback_test.go` → `TestReplyFallbackHelperProcess`, `TestReplyFallbackProcessEvidence`: readiness, subprocess proof and privacy assertions.
- `cmd/pyry/main.go` → `runSupervisor`: production constructs the fallback without test seams (CodeGraph blast radius).
- `docs/knowledge/features/streamsup-package-draining-turnevents-into-the-interactive-emitter.md` § One isolated Haiku attempt: caller bound is independent of child I/O; stderr has a joined, separate snapshot.
- `docs/knowledge/features/development-verification.md` § Prove that tests distinguish the change: gate completion to force cancellation; release gates before joining cleanup.
- `CODING-STYLE.md`: subprocess fixtures, channel synchronization, contract comments.

## Context

Scheduling can leave Wait unobserved at bounded return. The merged #3022–#3025 schema permits that result. Establish both outcomes deterministically without changing the 9800 ms attempt or 100 ms cancellation grace. No decision record is needed. The stale `feature/2882` overlaps these files with an earlier schema already superseded on main; no dependency remains.

## Design

Reuse the private `wait` seam to gate real `cmd.Wait` and completion delivery. Add a private cancellation-grace clock function accepting the existing duration; nil uses `time.After`. For the received outcome, the test clock signals entry into the cancellation grace, releases Wait and keeps the timeout unavailable until completion is selected. For the withheld outcome, retain the real timer while Wait is held until after return. Thus scheduler delay cannot choose the wrong evidence expectation.

Add a helper readiness pipe through `ExtraFiles`; readiness follows input/environment validation. Parent cancellation and deadline stimuli occur only after readiness. A test-owned context translates a triggered cancellation cause into the requested parent error; retain an actual timer-based deadline case in process evidence. Process evidence adapts assertions to observed completion and joins eventual Wait separately, including on failure. Preserve success, nonzero exit, own-deadline, output predicates and privacy coverage. Require exactly one lifecycle record and unknown completion fields (including `result_bytes`) when Wait is withheld.

## Concurrency model

The run worker, Wait worker and readiness reader use channels. Cleanup cancels the parent, releases all gates, kills the started group if necessary and joins run/Wait/readiness workers before examining process state. No test reads live output or ProcessState. Production's goroutines and shutdown semantics remain unchanged.

## State transitions and identity reuse

| Event | Race test |
| --- | --- |
| Parent cancellation, Wait received during grace | `TestReplyFallbackCancellationEvidence/parent_cancel/received` |
| Parent cancellation, Wait held beyond bounded return | `TestReplyFallbackCancellationEvidence/parent_cancel/withheld` |
| Parent deadline, Wait received during grace | `TestReplyFallbackCancellationEvidence/parent_deadline/received` |
| Parent deadline, Wait held beyond bounded return | `TestReplyFallbackCancellationEvidence/parent_deadline/withheld` |
| Actual own deadline and eventual child reaping | `TestReplyFallbackProcessEvidence/own_deadline` |
| Successful/nonzero completion before deferred cleanup | `TestReplyFallbackProcessEvidence` |

Each attempt owns a fresh subprocess and channels; no identifier is reused.

## Error handling

Cancellation returns empty text with `errReplyFallback`; termination requests and existing timeouts remain intact. Missing readiness/return fails with a bounded diagnostic, then cleanup releases gates and joins workers. Unknown Wait evidence never implies successful exit or valid output.

## Testing strategy

Write synchronized cases first and observe failure before adding the clock seam. Repeat focused race tests, including the actual 9.8-second deadline; run `go test -race ./cmd/pyry/...`, `go vet ./...`, and `go build ./cmd/pyry` (binary outside the worktree). The verifier owns the full-module gate. No live Claude proof is required.

## Open questions

None. Expected written work is about 350 lines including this plan, zero exported types, zero consumer migrations, three acceptance criteria, and no new rejection branches; all sizing limits hold.

## Security review

**Verdict:** PASS

**Findings:**

- [Trust boundaries] `replyFallbackOutput` continues gating completion predicates on Wait receipt; tests never infer trust from readiness or synchronized progress.
- [Tokens] Fixture token is synthetic; existing credential scrubbing and fresh provider lookup remain unchanged. Privacy checks exclude distinctive exchange, output, path and environment sentinels.
- [File operations] Readiness uses a private inherited pipe, avoiding path polling. Existing isolated cwd creation/removal remains unchanged.
- [Subprocesses] Only the test binary receives fixed fixture argv; `Setpgid` and real group SIGKILL remain exercised. Cleanup releases held Wait before joining.
- [Cryptography] No cryptography or secret comparison is added.
- [Network and I/O] No network path changes; readiness reads close during cleanup, and cancellation continues using the real 100 ms timer in withheld cases.
- [Errors/logs/telemetry] Lifecycle evidence remains content-free; daemon-only stderr handling is unchanged. Test failures do not print raw output or environment.
- [Concurrency] SHOULD FIX: cleanup must release every gate and join the run and Wait workers even after a fatal assertion, before reading process state.
- [Threat model] The private clock seam is unavailable through CLI, network input or environment. Existing fallback isolation and credential trust boundaries are preserved.

**Reviewer:** builder (self-review per the security-review checklist)
**Date:** 2026-10-09
