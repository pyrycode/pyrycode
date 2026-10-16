# Durable incremental checkpoints (#3504)

## Files read
- `internal/thread/checkpoint_read.go` → `readCheckpoint`, `readCheckpointItems`: committed format and retry authority.
- `internal/thread/checkpoint_export.go` → `ExportCheckpoint`, `checkpointRecord`: detached full inventory, including recipes.
- `internal/thread/checkpoint_import.go` → `ImportCheckpoint`: continuation graph validation.
- `internal/thread/cache.go` → `openCacheFile`, `writeCacheFile`, `syncCacheDir`, `removeLegacy`: containment, leaf admission and replacement rules.
- `internal/thread/store.go` → `Store`, `checkpoint`: scalar certificates and existing replay remain separate.
- `internal/history/sync.go` → `Sync`; `cache_dir.go` → `EnsureLogDir`: history durability and contained empty directories.
- `internal/thread/checkpoint_resume_test.go` → `testResumeProof`: immediate and per-entry continuation oracle.
- `docs/knowledge/features/thread-package.md` § Cache and epochs: public rows omit required private continuation state; unloaded reads remain scalar-only.
- `docs/knowledge/features/thread-package-main-thread-folding-validation.md` § Validation and testing: reuse resume proofs.
- `docs/knowledge/features/history-package.md`, `CODING-STYLE.md`, ADR 045: history ownership and durability order.

## Context
One deliverable: a package-local durable transaction callable by the later worker integration. Store loading still replays history. ADR 045 already records the decision; no new ADR is needed.
Sizing: about 760 written lines (280 production, 410 tests, 70 plan), zero exported types, zero migrated consumers, four acceptance criteria, and no dispatch state machine. No overlapping remote branches touch the proposed new writer/test files.

## Design
Add `checkpoint_write.go` with a package-local writer holding Store, conversation, source and an acknowledged state. `commit` accepts detached complete fold state/inventory, caller-selected changed records and scalar certificate evidence. The caller supplies all private/recipe changes and serializes attempts for this conversation.
Validate the complete candidate and certificate before side effects. Sync history covering the candidate, then call `readCheckpoint` on every attempt. Missing/invalid state selects a fresh random generation and full inventory; persistence errors fail. A valid named generation supplies the reconciliation boundary, regardless of prior acknowledgement. Reject candidates older than surviving state.
Open the admitted index with no-follow/nonblocking flags and descriptor regular-file checks; fresh generations use exclusive creation at 0600. Recheck exact `LogDir` containment before mutations. Truncate only excess bytes beyond validated committed length, append checked length/CRC frames, sync and close, then sync the directory before state publication (also on retry).
Write complete state through a 0600 same-directory temporary file, checking short writes, sync and close. Recheck destination admission and containment, rename, then sync the directory. Advance acknowledgement only on complete success. Failed clean attempts conservatively invalidate named checkpoint state with best-effort directory sync; dirty post-rename failures may retain the complete newer state for retry. Never touch the scalar certificate.
Local operation/write seams inject faults without changing Store-wide behavior. No generation cleanup or compaction.

## Concurrency model
Synchronous transaction; no goroutines or locks added. Its owner serializes fold exports, changed-record selection and commits outside `Store.mu`. History methods retain their own locking.

## State transitions and identity reuse
| Event | Race-enabled proof |
| --- | --- |
| First checkpoint / empty inventory | `TestWriteCheckpointResume` |
| Repeated incremental checkpoints and recipe-only changes | `TestWriteCheckpointResume` |
| Partial append then retry against surviving old state | `TestWriteCheckpointFaults` |
| Rename succeeds, directory sync fails, retry preserves newer named boundary | `TestWriteCheckpointFaults` |
| Missing/invalid state then fresh full generation | `TestWriteCheckpointRecovery` |
| Failed clean attempt invalidates clean claim | `TestWriteCheckpointFaults` |

## Error handling
Return `ErrPersistence` for I/O failures without acknowledging progress. Validation failures never write. Surviving old/new state must remain internally complete; absent/invalid state yields no restored fold. Cleanup closes/removes unpublished temporary bytes best effort; failed clean-claim invalidation cannot authorize publication or certification.

## Testing strategy
Write tests first. Reuse `testResumeProof` through representative saved cuts and each suffix entry, checking exact appended frame bytes and unchanged prefix. Table-driven faults cover history, truncation, partial/short append, index sync/close/directory sync, temporary write/sync/close, rename and final directory sync with retry. Security cases reject redirected directories and symlink/directory/FIFO leaves, preserving history and outside bytes. Run focused red/green checks, then `go test -race ./internal/thread ./internal/history`, `go vet ./...`, and `go build -o /tmp/builder-3504/pyry ./cmd/pyry` on macOS. The verifier owns `make check`; Linux is outside this run's verification scope.

## Open questions
None.

## Documentation handoff
Pending documentation stage: update `docs/knowledge/features/thread-package.md`, “Cache and epochs”, and `docs/knowledge/features/thread-package-main-thread-folding-validation.md`, “Validation and testing”: describe history/index/state durability order, complete state rewrites versus changed-record appends, interruption/retry behavior and transaction round-trip proof. State scalar certificates remain separate, Store loading still replays history, and #3501 delivers scheduling/final Store certification.

## Security review
**Verdict:** PASS
- Trust boundaries: `commit` validates detached exports with `ImportCheckpoint`; retry authority crosses through `readCheckpoint`, never remembered offsets.
- Tokens/cryptography: generation uses existing crypto-random `randomToken`; CRC-32C detects corruption, not hostile authentication. No credentials added.
- Files: MUST FIX addressed in design: never truncate from stale acknowledgement after uncertain rename. Enforce package leaf admission, exact containment rechecks, no-follow regular descriptors, exclusive fresh creation and private modes.
- Subprocesses/network: none added; source reads reuse bounded committed-frame validation.
- Errors/logs: generic persistence errors; no content, paths or secrets logged.
- Concurrency: caller serializes per-conversation writes; no new goroutine or Store lock held over I/O.
- Threat model: local cache integrity follows existing cache admission. History-bound restoration belongs to #3337; scheduling and clean certification policy belong to #3501.
**Reviewer:** builder (self-review)
**Date:** 2026-10-16
