# Composer foreground witness readiness

## Files read

- `internal/e2e/internal/claudetape/physical_protocol_test.go` → `TestPhysicalProtocolComposerForegroundStopsBeforeActualBackgroundRelease`: original Composer physical proof and premature file-existence barrier.
- `internal/e2e/internal/claudetape/physical_owned_test.go` → `physicalControlWaitFile`, Composer and witness rejection controls: first readable file may still be empty.
- `internal/e2e/internal/claudetape/physical_owned.go` → `RequireForegroundWitness`, `StopContext`, `StartPhysicalOperation`: complete owned PID validation, pinned process group and actual Wait9.
- `internal/e2e/internal/claudetape/physical_protocol.go` → `BeforeInput`, `Finish`: interruption validates ownership before signalling; completion needs native and physical evidence.
- `docs/knowledge/features/development-verification-prove-that-tests-distinguish-the-change.md`: controlled ordering can prove a defect without attributing a historical failure.
- `docs/knowledge/features/e2e-harness.md` and `e2e-realclaude-test-infrastructure.md`: physical process evidence and bounded fixture cleanup.

## Context

One behaviour: submit Composer interruption after the actual foreground witness is ready, retaining foreground Wait9 and background Wait0 proof. Shell redirection creates an empty witness before printf writes the PID. The existing file helper returns on that empty read. `RequireForegroundWitness` correctly rejects it; weakening that check would mask ownership failures.

The reported failure at `4aafaccc03c0add2b3a3f82fba43e5e5f6ebafd0` remains failed (named test 1/0/1/0; affected check 985/984/1/0 events, 889/888/1/0 leaves). PR #3680's same-tree 1/1/0/0 rerun establishes non-reproduction only. This builder's starting `d8984ad1eccb1000060da29faa0e9c2c2bcd6cf6` named race run also passed 1/1/0/0. Historical load/base cause remains unknown.

## Design

Keep production protocol, closed command grammar and operation ownership unchanged. Make test polling accept a readiness predicate, retain its five-second bound, and use a foreground barrier requiring exact current PID plus newline and `RequireForegroundWitness` success. Use it in the original protocol and owned-operation Composer controls and before witness mutations. Generic execution-marker waits retain their existing meaning.

Add a synchronized regression around the file polling helper. A real child shell opens its witness empty, acknowledges that boundary through a pipe, and waits for permission to publish its own PID. A read seam returns the actual empty bytes while releasing publication, so unchanged file-existence polling deterministically returns too soon. The repaired poll retries and returns the complete actual PID. Kill and reap the real child and check Wait9. No fabricated worker status, sleeps as ordering barriers, larger timeout or skipped assertion.

The original Composer rig still proves interruption acceptance, actual foreground Wait9/reaping, background liveness through cancelled native turn, FIFO-only successful Wait0 and complete Finish proof. Strengthen the explicit foreground Wait/reap assertion there.

## Concurrency model

Readiness polling stays on the test goroutine. Regression child readiness and publication use pipes with bounded deadlines and cleanup that kills/reaps the child on any failure. Existing owned-operation wait/watchdog goroutines and pinned group shutdown are unchanged.

## State transitions and identity reuse

| Event | Race coverage |
| --- | --- |
| Empty witness → complete shell PID | `TestPhysicalForegroundWitnessWaitsForPublication` |
| Owned foreground → interrupted and reaped, background still held → native cancellation → real FIFO release | `TestPhysicalProtocolComposerForegroundStopsBeforeActualBackgroundRelease` |
| Released, foreign, symlink, FIFO or oversize witness | `TestOwnedPhysicalForegroundWitnessRejectsReleaseAndForeignReads` |
| Repeated stop and retired process group | Existing owned-operation and pinned-group controls |

## Error handling

Witness rejection remains fail-closed in production. Polling failure reports its last observation within the existing budget. Process/pipe cleanup runs even when the regression fails. Unowned controls and extra selectors retain existing rejection tests.

## Testing strategy

First run the named original race control through managed admission. Commit this plan before tests or helper changes. Add the synchronized regression with behaviour-preserving extraction of the existing read loop and observe the empty-witness failure; then repair readiness. Run the new and original controls under race instrumentation, then the affected claudetape package, `go vet ./...` and `go build ./cmd/pyry` on Linux. Record exact revisions, settings, commands, counts and evidence references here. Complete suites/final acceptance remain dispatcher-owned; no live provider or second OS checks apply.

## Open questions

- Confirm the controlled empty-read boundary independently of historical attribution; retain both failed and passing results.

## Scope sizing

One repair and its regression: approximately 300 written lines including this plan/evidence, zero new exported types, three test consumer updates, three acceptance criteria, no new production rejection branches. All ceilings hold. No remote feature branch currently overlaps the two intended test files. The smaller test-helper scope supersedes the estimate's two production files.

## Security review

**Verdict:** PASS

**Findings:**

- Trust boundaries: `RequireForegroundWitness` remains the authority for current PID, running shell and unreleased gate; test readiness adds no signalling authority.
- Tokens/secrets: no credentials or live provider are used; evidence records only toolchain, test counts and controlled process observations.
- File operations: private `t.TempDir` witnesses and existing `O_NOFOLLOW`, regular-file, same-inode and size checks remain. The new barrier must also require the complete PID record; readiness cannot replace the production recheck.
- Subprocesses: original `PhysicalInputCommand` allowlist and pinned process groups stay unchanged. Regression shell arguments are fixed and paths are positional arguments; cleanup kills and waits for its owned child.
- Cryptography: no crypto operation or key material is involved.
- Network/I/O: no network added; readiness pipes and polling are bounded.
- Errors/logs: only controlled fixture diagnostics and evidence references are retained, with no environment dump.
- Concurrency: readiness/publication pipe handshake forces the empty read before publication; deadlines and kill/reap cleanup bound every blocked child operation.
- Threat model: direct offline physical fixture only; no relay, mobile or CLI authorization contract changes. No deferred security finding.

**Reviewer:** builder (self-review per security-review checklist)
**Date:** 2026-10-20
