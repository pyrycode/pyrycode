# Populated liveness in the gather publication proof

## Files read

- `internal/e2e/realclaude/finding_run_gather_test.go` → `TestFinGatherReturnsNoCapturedBytes`, `finGatherReadings`, `finGatherExemptKeys`, `TestFinGatherForbiddenKeyWalkDescends`: existing publication sweep and exact-key walker contract.
- `internal/e2e/realclaude/finding_stage_held_group_test.go` → `finStageHeldGroup`, `finStageSubject`, `finStageAssertLiveness`: isolated FIFO-held subject, reduced group identity and failure-safe teardown.
- `internal/e2e/realclaude/process_pin_liveness_test.go` → `pinStateOutcome`, `pinReadState`, `pinStateArgs`, `pinStateColumns`: healthy reads fill Detail/StateColumn; optional ToolStderr belongs to diagnostic failures.
- `docs/knowledge/features/e2e-realclaude-finding-run-gather-test-go.md`: retained captured-byte plants and recursive key walk must cover all three gather returns.
- `docs/knowledge/features/e2e-realclaude-finding-stage-held-group-test-go.md` → opening entry: empty-liveness proof gap and whole-carried diagnostic limits.
- `docs/knowledge/features/e2e-realclaude.md`, `docs/knowledge/features/development-verification.md` → tagged offline tests and mutation checks distinguish exercised branches from vacuous success.
- `CODING-STYLE.md`: stdlib tests, race detection and symbol-based comments.

## Context

#1300 closes one publication-proof gap: the gather sweep has never visited populated healthy liveness, and ToolStderr's omitempty tag hides its exact-key exemption even on healthy reads. This is test coverage, not a diagnostic redaction policy; no decision record is needed.

Sizing: one deliverable, about 220 written lines including this plan, two test files, zero production files, zero exported types/interfaces, zero consumer updates, four acceptance criteria, no new state-machine rejection branches. All limits remain below the builder ceiling. No other fetched feature branch touches either target file.

## Design

Retain the no-subject sweep as a named subtest and share its existing gate, anchored attribution, retained stdout needle and three-return sweep assertions with a held-subject subtest. The latter calls finStageHeldGroup and hands finGatherReadings its actual needles and pinned groups; the synthetic reap line names that actual group. Both plant trailNeedle only in trailer result and anchored stderr, with completed terminal_reason and constant pinStateRunning ClaudeState.

Require a successful argv scan in both cases. The no-subject case retains zero matches/liveness. The held case requires positive MatchCount, exactly one liveness result per match, running verdicts, nonempty Detail and StateColumn, and empty ToolStderr from healthy reads. Missing liveness must fail before sweeping.

In TestFinGatherForbiddenKeyWalkDescends, marshal a real nested pinStateOutcome with a nonempty source-authored ToolStderr. Assert the diagnostic key is present after marshaling and the walk accepts it; add tool_stderr_tail beside it at the same depth and require exactly that forbidden path. Preserve the existing exemption and restricted ps columns unchanged. Update both headers and exemption rationale to distinguish healthy liveness coverage from synthetic optional-key coverage and arbitrary diagnostic strings.

## Concurrency model

No new goroutines or lifecycle helper. Reuse finStageHeldGroup's FIFO rendezvous, isolated process group and deferred group/direct kill and Wait; the FIFO cleanup follows the callback on success and Fatalf paths. Subtests run sequentially.

## Error handling

Staging, scan, liveness or marshal failures fail the offline test; they never skip. Missing reads are a failed premise, not a clean sweep. Whole-carried diagnostics receive no captured-byte plants or changed normalization.

## Testing strategy

Run the focused sweep and walker with race detection and e2e_realclaude tag, then all offline TestFinGather/TestFinStage tests. Use temporary Go overlays to remove the tool_stderr exemption and to omit liveness filling; the respective focused cases must turn red for those reasons. Run go vet ./..., tagged package vet and go build ./cmd/pyry with output outside the worktree. The dispatcher owns the full-module gate; no live Claude check is needed.

## Open questions

None. Healthy reads cannot exercise optional ToolStderr; synthetic key coverage explicitly handles that limit.

## Documentation handoff

Pending for the documentation stage: in `docs/knowledge/features/e2e-realclaude-finding-stage-held-group-test-go.md`, update the opening `finding_stage_held_group_test.go` entry's claim that the neighbouring proof runs with empty liveness. State that #1300 covers populated healthy liveness and separately exercises the optional diagnostic-key exemption; neither proves arbitrary diagnostic strings are redacted. Cite symbols rather than line numbers.

## Security review

**Verdict:** PASS

**Findings:**

- [Trust boundaries] finGatherReadings still reduces ambient argv to counts and per-pid reads. Tests sweep all three returns; no raw scan row crosses the boundary.
- [Tokens] finStageHeldGroup scrubs its child environment. Test fixtures carry source-authored needles and diagnostic text only; credentials are neither required nor read.
- [Files] Reuse private t.TempDir FIFO creation and cleanup; no new path construction or artifact publication.
- [Subprocesses] Reuse finStageHeldGroup's fixed shell script with positional FIFO arguments and isolated group teardown; no subprocess policy changes.
- [Cryptography] No cryptographic operation or key material involved.
- [Network and I/O] Offline tests use local ps and FIFO only, with existing scan/rendezvous timeouts; no new network boundary.
- [Errors/logs] Healthy read checks name scalar fields. Synthetic diagnostic-key coverage proves exact exemption only, not arbitrary stderr redaction; captured-byte plants stay out of whole-carried diagnostics.
- [Concurrency] No new goroutine, lock or channel; existing callback teardown runs even on Fatalf before FIFO cleanup.
- [Threat model] This is an offline publication-boundary proof with no relay, mobile or CLI product-contract change. Restricted pinStateColumns and exact-key exemption remain unchanged.

**Reviewer:** builder (self-review per the security-review checklist)
**Date:** 2026-10-07
