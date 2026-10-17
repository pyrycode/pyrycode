# Worker checkpoints

## Files read
- `internal/thread/store.go` → `run`, `Unload`, `Shutdown`: complete progress, retirement and joined results.
- `internal/thread/publication.go` → `publicationCapture.capture`: clears publication candidates, including private slots.
- `internal/thread/checkpoint_export.go` → `ExportCheckpoint`: complete detached inventory and private state without rendering.
- `internal/thread/checkpoint_write.go` → `checkpointWriter.commit`, `replaceState`: history/index/state durability and disk reconciliation.
- `internal/thread/checkpoint_read.go` → `readCheckpoint`: validated restoration for reconciliation and test proofs.
- `internal/thread/cache.go` → `writeCacheFile`, `recoveryCandidate`, `UnloadedLastShownVersion`: contained dirty/clean scalar evidence.
- `internal/thread/certificate_test.go`, `checkpoint_write_test.go`, `checkpoint_resume_test.go` → lifecycle fault seams and `testResumeProof`.
- `docs/knowledge/features/thread-package-background-store-lifecycle.md` → cancelled pre-feed barriers preserve preceding progress; parent cancellation cannot certify clean.
- `docs/knowledge/features/history-package.md` → serialized ForwardReader use and no-follow sync obligations.
- ADR 045 and thread cache/replay overviews: history remains authoritative; Store restoration belongs to #3337.

## Context
Activate one per-worker checkpoint lifecycle using the merged reader/writer. Loading still refolds history from entry one. No new decision record is needed. No fetched feature branch overlaps the planned production files.

## Design
The worker owns a checkpoint accumulator and exports only at completed feed/publication boundaries. Compare complete internal records against the last successful export, retaining pending IDs independently of publication capture; this includes hidden, suppressed and recipe-only changes. A failed attempt cannot acknowledge IDs. Complete export/validation may traverse all rows, but only selected record frames are appended. The first successful worker transaction uses a fresh generation even if another worker left valid state; failed attempts reconcile any surviving renamed state before retrying.

After initial replay, arm a 30-second timer. Publications never reset it. Each elapsed interval checkpoints a changed consumed version, including version-only progress; unchanged intervals do no I/O. Reset after processing each deadline, preventing retry bursts. Emit one fixed warning per failed periodic attempt and continue publication. Success logs framed live-record bytes, committed index bytes and complete replacement state bytes separately.

Before publication dirty-mark any existing readable checkpoint state without changing its boundary, and dirty-mark the scalar certificate. Final retirement always commits the complete eligible boundary (even unchanged/zero), then writes its scalar clean certificate. Failure invalidates both clean surfaces and returns `ErrPersistence`. Failed/partial feeds cannot export older progress from a mutated fold.

## Concurrency model
One worker alone mutates, captures and exports the fold. A single tail goroutine forwards one bounded borrowed chunk over an unbuffered channel and waits for acknowledgement before returning its callback. Cancellation releases both send and acknowledgement waits. The worker cancels and joins the tail goroutine and stops its timer before final persistence and closing done. All I/O and joins stay outside `Store.mu`.

## State transitions and identity reuse
| Event | Race-enabled coverage |
| --- | --- |
| Continuous publications, quiet changed progress, unchanged and version-only deadlines | `TestStoreCheckpointTimer` |
| Failed periodic transaction then later changes/retry | `TestStoreCheckpointPending` |
| Full refold after retirement, fresh generation and saved-boundary/suffix restoration | `TestStoreCheckpointPending` |
| Dirty replacement/directory failure before publication | `TestStoreCheckpointDirty` |
| Final checkpoint/scalar failure, empty/unchanged retirement | `TestStoreCheckpointFinal`, existing certificate lifecycle tests |
| Cancellation while tail forwards, interrupted pre-feed barrier, repeated/concurrent joins | `TestStoreCheckpointTailJoin`, existing `TestStoreCheckpointCancelledFold` and store lifecycle tests |

## Error handling
Periodic persistence failures retain pending IDs and usability. Dirty marking fails closed. Final failures invalidate trusted clean claims independently per conversation. Parent cancellation, source/render failures and incomplete feed/recovery cannot certify clean. Existing generic sentinels remain the public error contract.

## Testing strategy
Write controlled timer/handshake tests first, observe their failure, then implement. Reuse transaction fault seams and `testResumeProof` to compare saved state and continued suffixes. Compare incremental frame bytes for identical changes on small/large inventories and report state replacement bytes separately. Update certificate-only counters to distinguish checkpoint writes. Run admitted race tests for thread/history, `go vet ./...` and a binary build on Linux; the dispatcher owns the full hermetic gate.

## Open questions
None. Sizing: approximately 760 added/modified lines including this plan, zero exported types/interfaces, zero public migrations, five acceptance criteria and fewer than ten lifecycle failure categories; one independently checkable worker persistence deliverable.

## Documentation handoff
Pending for the documentation stage: update `docs/knowledge/features/thread-package-cache-and-epochs.md` § Cache and epochs, `docs/knowledge/features/thread-package-background-store-replay.md` § Loading and replay readiness, and `docs/knowledge/features/thread-package-background-store-lifecycle.md` § Isolation and lifecycle. Document the 30-second changed-version cadence, retained changes/retry, independent joined final checkpoints and dirty/clean evidence. State that state-file rewrites are complete, only item-record appends are incremental, and Store loading still performs full replay pending #3337. Keep `docs/knowledge/features/thread-package.md` § Cache and epochs summary consistent.

## Security review
**Verdict:** PASS

**Findings:**
- Trust boundaries: exports are internal; `ImportCheckpoint` and `readCheckpoint` validate persisted graphs before reuse. History remains the source, and checkpoint loading is deferred to #3337.
- Tokens/secrets/cryptography: reuse `randomToken` (crypto/rand) for generations; no new credentials or cryptographic operations.
- File operations: reuse contained, allow-listed, no-follow regular-file reads and writer checks, 0600 leaves and synced atomic replacement. Invalidation checks containment and regular leaves before removal; it never follows redirects.
- Subprocess/network: no subprocesses, listeners or network reads are introduced.
- Errors/logs: fixed diagnostics and numeric byte counts only; no IDs, raw errors, paths, recipes or payloads.
- Concurrency: only the worker accesses the fold; forwarding waits honor cancellation, are joined, and never hold Store.mu. Failed feeds disable final export so old evidence cannot certify partially mutated state.
- Threat model: local derived-cache durability is the boundary; incomplete or failed persistence must leave no trusted clean evidence. Store restoration remains explicitly outside this ticket under #3337.

**Reviewer:** builder (self-review per the security-review checklist)
**Date:** 2026-10-17
