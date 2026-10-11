# Bound transcript replay matrix runtime

## Files read

- `cmd/pyry/memory_transcript_reply_reuse_test.go` → `testReplyLauncherReplay`: exhaustive partition loop and independent identity, terminal, closure and publication expectations.
- `cmd/pyry/memory_transcript_reply_shell_test.go` → `TestMemoryTranscriptReplyShellObservation`, `TestVerifierRejectedRuntimeTransfer`: source/tool/recovery controls using the helper.
- `cmd/pyry/memory_transcript_reply_recovery_test.go` → `TestMemoryTranscriptReplyRecoveryOwnership`, `TestMemoryTranscriptReplyRecoveryValidation`: recorded ownership, rejection and interruption controls, including parallel scenarios.
- `cmd/pyry/memory_transcript_fold.go` → `memoryTranscriptReader.feed`: processes entries individually irrespective of caller chunk size; no production change is needed.
- `cmd/pyry/memory_transcript_replay_test.go` → `TestMemoryTranscriptPersistedReplay`, `TestMemoryTranscriptPublicationRecovery`: separate persisted worker/publication proof remains enabled.
- `docs/knowledge/features/history-package-shape.md` § Testing replay and publication: independent expectations must precede replay comparisons, and publication hooks require a completion barrier.
- `docs/knowledge/features/development-verification.md` § Test execution and artifact survival: report actual executed counts and distinguish capped evidence from a pass.
- `CODING-STYLE.md`: table tests, contract comments and scoped race checks.

## Context

The helper repeats the same entry-wise reduction for all 2^(n-1) partitions.
On pyrybox's container `ced36cb6ca66`, base commit
`e4887b5f10609ebf0bc35e9730bd5a6d25d76c93`, the six affected families under
`-race -count=1 -json -timeout=90s` timed out after 90.031 package seconds
(153.397 command seconds including compilation). A scratch Go overlay only
adds completed-partition work logging; expectations and enumeration are unchanged.
The baseline is a completed-work lower bound, not a successful run.

One deliverable: bounded ordinary test cost with full partition proof still available.
Estimate: under 300 written lines including this plan; zero production files,
zero new exported types/interfaces, six unchanged callers, four acceptance criteria,
zero production error/state-machine branches. Recount before plan commit: all limits
remain within the builder ceiling. No decision record is needed.
Fetched feature branches have no overlap with the helper file.

## Design

Add a local `testReplyPartitions(entries int, exhaustive bool) []int` selector in
`memory_transcript_reply_reuse_test.go`. For nonempty histories, ordinary coverage
is the distinct union of mask zero (whole history), the all-cut mask (single entries),
and every one-bit mask (each two-chunk cut). Enumeration is linear in history
length, with at most n+1 partitions. Preserve binary `split=` subtest identities.

`PYRY_TRANSCRIPT_EXHAUSTIVE=1` selects every original mask, in its original order.
Both modes use the existing fresh reader per partition and the exact same
independent assertions at every selected intermediate prefix. No scenarios or
expectations change, including source/generation isolation and separate text/delivery
boundaries. Emit a per-helper summary of completed partitions, replayed entries,
and checked prefixes, identifying the completion/interruption control.

Hermetic full-proof command (default unchanged ten-minute package budget):

```sh
PYRY_TRANSCRIPT_EXHAUSTIVE=1 go test -count=1 -v -run '^(TestVerifier(RepeatedMainLauncher|RejectedRuntimeTransfer)|TestMemoryTranscriptReply(ReusedLauncherOwnership|ShellObservation|RecoveryOwnership|RecoveryValidation))$' ./cmd/pyry
```

## Concurrency model

No new goroutines or shared mutable counters. Metrics belong to each helper call;
existing parallel scenarios keep their own readers. Read the environment only.

## State transitions and identity reuse

The test selector holds no lifecycle state. Existing completion/interruption,
reused ownership, producer lifetime, generation and recovery scenarios retain their
prefix assertions in `TestVerifierRepeatedMainLauncher`,
`TestMemoryTranscriptReplyReusedLauncherOwnership`, `TestVerifierRejectedRuntimeTransfer`,
`TestMemoryTranscriptReplyShellObservation`, `TestMemoryTranscriptReplyRecoveryOwnership`
and `TestMemoryTranscriptReplyRecoveryValidation`, all run under `go test -race`.
Persisted replay and publication recovery remain covered by their existing named tests.

## Error handling

Assertion failures retain their scenario and partition paths. Only the exact opt-in
value `1` enables exhaustive enumeration; ordinary checks require no environment setup.
No production errors or recovery contracts change.

## Testing strategy

First add selector tests and confirm the existing exhaustive behavior fails bounded
coverage. Assert exact masks for short histories, no duplicates, every cut and
single-entry coverage, a linear bound on a longer history, and full masks in opt-in
mode. Then run the six affected families under race, the explicit full command,
uncached `go test -race -count=1 ./cmd/pyry/...`, `go vet ./...`, and a binary build.
Record commits, host, elapsed time, scenarios, partitions, entries and prefix checks
in the PR. Verify actual execution of both persisted replay/publication tests.
The dispatcher verifier owns full-module `go test -race -count=1 ./...` and
`make check`; hand off their elapsed times and counts without claiming they passed.

## Open questions

None. Full proof is explicitly opt-in and hermetic; production semantics are unchanged.

## Documentation handoff

Pending for documentation stage: update `docs/knowledge/features/history-package-shape.md`,
“Testing replay and publication”, to state exactly which partition coverage ordinary
checks retain. Replace unconditional “every replay partition” with the default/full
distinction and include the exact executable command above and its unchanged
ten-minute package budget. Preserve independent expectations and the persisted
worker/publication proof requirements.

## Security review

**Verdict:** PASS

**Findings:**

- [Trust boundaries] `testReplyLauncherReplay` continues to assert accepted opening/user identity and recorded ownership against fixture expectations; neither mode derives expectations from production output.
- [Tokens, secrets, credentials] The opt-in reads only a test-mode switch; all fixtures are synthetic and require no credentials or agent execution.
- [File operations] The selector and metrics perform no file I/O; existing persisted worker/publication tests retain their storage assertions.
- [Subprocesses] No subprocess or shell execution is added to Go tests; the explicit command invokes only hermetic test families.
- [Cryptography] No cryptographic code or identifiers change; first-text identity and timestamp assertions remain in the common replay path.
- [Network and I/O] The selected matrices use in-memory readers and no network; enumeration is linear by default and opt-in is deliberate.
- [Errors, logs, telemetry] New diagnostics contain synthetic scenario identities and work counts; existing failure diagnostics remain local to tests.
- [Concurrency] Per-helper counters are used synchronously by non-parallel partition subtests; parallel parent scenarios share no metrics or readers.
- [Threat model] This changes test cost only. Ownership, first-terminal precedence, permanent interruption withholding, fold closure and isolation proofs remain active; no production threat boundary is modified.

**Reviewer:** builder (self-review per the security-review checklist)
**Date:** 2026-10-11

## Revisions

- 2026-10-11: Use `-json` rather than `-v` in the documentation handoff's full-proof command so execution and replay-work counts can be collected directly. This changes reporting only; the original scenario selector, assertions and unchanged ten-minute package timeout remain the same. The executed command below passed at implementation commit `e097c4b303f6d43222bef01e5ff8810e2de62cdc`, exercising 594 helper scenarios, 92,544 partitions, 795,264 entries and 443,904 prefixes in 129.585 package seconds (140.997 command seconds).

```sh
PYRY_TRANSCRIPT_EXHAUSTIVE=1 go test -count=1 -json -run '^(TestVerifier(RepeatedMainLauncher|RejectedRuntimeTransfer)|TestMemoryTranscriptReply(ReusedLauncherOwnership|ShellObservation|RecoveryOwnership|RecoveryValidation))$' ./cmd/pyry
```

- 2026-10-11: State transitions and identity reuse clarification: None: partition selection and its call-local counters introduce no lifecycle or identity state. The existing lifecycle/identity assertions named in that section remain unchanged and passed under the race detector in all 594 helper scenarios.
- 2026-10-11: Baseline census at the capped timeout: 159 scenario paths had completed work, with 21,247 passed partitions, 173,432 replayed entries and 97,336 checked prefixes. A baseline scenario path can contain both completion and interruption helper invocations; resulting runs report 594 helper invocations across 564 scenario paths. The bounded/full census comparison verified identical scenario paths, interruption controls and history lengths, and independently checked each summary's partition, entry and prefix totals against the selected coverage contract.
