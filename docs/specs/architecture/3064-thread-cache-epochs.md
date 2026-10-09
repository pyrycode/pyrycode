# Persistent thread caches and conversation epochs

## Files read
- `internal/thread/store.go`: `run`, `publish`, `Unload`, `Shutdown` own serialized replay and joined lifecycle.
- `internal/thread/store_test.go`: existing continuation, isolation, lifecycle, retry and containment fixtures.
- `internal/thread/fold.go`: `Feed`, `Items`, `Version`; public items cannot restore hidden joins/reports.
- `internal/history/forward.go`: bounded `Walk` and ordered same-store `Tail`.
- `internal/history/log.go`: `LogDir`, `resolveDir`, `LatestEntryID`; exact directory equality must be checked on every operation.
- `docs/knowledge/features/thread-package-background-store.md`: retry must recreate both fold and reader; publication must fence cancellation and retirement.
- `docs/knowledge/features/history-package.md`: directory paths cannot be cached; regular leaves require no-follow opens.
- `CODING-STYLE.md`, `docs/knowledge/features/development-verification.md`, ADR 042: persistence, evidence and storage contracts.

## Context
History remains the sole durable source. Add disposable folded caches and random epochs without daemon wiring or a second item log. Full replay is intentional; no new ADR is needed. Feature branches #2873 and #2882 do not overlap. Verified parentage is #2961 → #3048 → #3064, with `needs-human:sizing` already present. The anticipated 950–1100 written lines exceed the ceiling under the grandchild exception; cache recovery and clean lifetime certification would otherwise be two candidate slices. One new history directory method, zero production consumer migrations, five acceptance criteria and roughly ten failure categories.

## Design
- Add `Snapshot.Epoch`, generated with 128 bits from `crypto/rand`, outside deterministic items.
- Store `thread-cache.json` and `thread-recovery.json` inside the conversation's existing history directory. Add `history.Store.EnsureLogDir` for secure creation of an empty directory without fabricating entries.
- Cache schema includes schema/folding-rule revisions, epoch, consumed version, detached items, complete-progress flag and run/coordinator references. Recovery metadata pairs the cache with the run that last opened the conversation.
- A run has one random token and coordinator conversation. Its `thread-run-<token>.json` starts incomplete before any usable publication. Every opened conversation's recovery marker is atomically replaced with that run before replay/publication, invalidating its previous clean claim.
- Across lifetimes a candidate epoch requires compatible complete cache, matching recovery metadata and the referenced completed run certificate. Within one lifetime the current incomplete run is accepted for normal unload/reload only.
- Always replay from history with a new reader/fold. Compare cached items at its consumed version with replay and reject caches ahead of readable history. Only then retain a compatible epoch; otherwise mint a new one. Replay through the captured bound before usability restores every private continuation field.
- Persist complete progress before each publication and on retirement. Keep only scalar record metadata for unloaded conversations. Shutdown revalidates and rewrites checkpoints for loaded and previously unloaded conversations, then atomically completes the single run certificate last.
- `Unload(id) error` and `Shutdown() error` expose generic persistence/lifecycle errors; concurrent shutdown callers join one shutdown and observe the same result. A failed/incomplete conversation prevents clean certification. Retrying can repair its record.

## Concurrency model
One goroutine still owns each reader/fold. Store mutex guards admission, worker identity, publication and scalar lifecycle records; no history/cache I/O or joins under that mutex. A separate initialization mutex serializes creation of the shared run certificate. Workers checkpoint before closing their done channel; shutdown joins workers before final metadata writes. Cancellation and worker identity fence every publication.

## State transitions and identity reuse
| Event | Race test |
| --- | --- |
| Clean reopen retains epoch and restores continuation | `TestStoreCacheContinuation`, `TestStoreCacheEpochs` |
| Unclean reopen, changed rules, unusable/ahead cache rotates epoch | `TestStoreCacheEpochs` |
| Unload/reload retains compatible epoch; unload alone is unclean | `TestStoreCacheEpochs`, `TestStoreLifecycle` |
| Replay-to-tail commits exactly once; detached publications | `TestStoreReplayTailIsolation` |
| Failure/retry restarts reader and fold | `TestStoreFailureRetry`, `TestStoreCacheSecurity` |
| Cancellation during replay cannot certify clean | `TestStoreCacheInterrupted`, `TestStoreLifecycle` |
| Repeated/concurrent unload/shutdown joins and releases | `TestStoreLifecycle`, `TestStoreCacheShutdown` |
| Interrupted replacement preserves prior complete bytes | `TestStoreCacheSecurity` |
| Failed final checkpoint leaves all participants unclean | `TestStoreCacheShutdown` |

## Error handling
Missing/corrupt/incompatible cache triggers full replay and epoch rotation. Nonregular/symlink leaves and I/O/containment failures produce generic recoverable unavailability or returned `ErrPersistence`. Missing/empty history is usable at zero; readable history is mandatory even with a cache. Failed or partial fold work never replaces complete progress or certifies shutdown. Never log or expose payloads, raw source errors or host paths. History files are never modified by cache operations.

## Testing strategy
Write failing offline tests first. Reuse existing fixtures at continuation cut points, compare every published version with a fresh fold, and test epoch preservation/rotation separately from item equality. Cover corrupt/truncated/missing/rule-changed/ahead caches, empty history, marker/checkpoint interruption, shutdown failure across unloaded conversations, symlinks/nonregular leaves, sibling/outside redirection and repair/retry. Run `go test -race ./internal/thread ./internal/history`, `go vet ./...`, and build `./cmd/pyry` to scratch; the dispatcher owns `make check`.

## Open questions
None. Full replay deliberately trades restart speed for a small recoverable contract. Run certificate files remain tiny lifetime records; reclamation is outside this ticket. Clean certification linearizes at the final successful atomic rename; there is no new machine-crash guarantee for history.

## Documentation handoff
Pending documentation stage:
- `docs/knowledge/features/thread-package.md`, “Cache and epochs”: actual on-disk location, recovery/continuation strategy, clean/unclean and folding-rule epoch rules, tail recovery, rebuilding/unavailable and I/O failure outcomes, unload/reload and clean-shutdown contracts.
- `docs/knowledge/features/thread-package-background-store.md`, “Snapshots and retry” and “Isolation and lifecycle”: epoch exposure, persistence-error reporting, and remove the statement that caches/epochs remain downstream.

## Security review
**Verdict:** PASS
- Trust boundaries: cache metadata never seeds the fold. Epoch reuse requires comparison against readable history; IDs are validated at lifecycle and file boundaries.
- Tokens/cryptography: epochs/run tokens use `crypto/rand`, 128 bits; they are identifiers rather than credentials.
- File operations: exact `LogDir` containment per operation, no-follow regular-file reads, reject nonregular destinations, owner-only temporary files, sync/close/atomic rename. Directory replacement has the existing owner-controlled state-directory threat boundary.
- Network/subprocesses: none introduced.
- Errors/telemetry: fixed sentinels only; no raw I/O errors, paths or payloads exposed/logged.
- Concurrency: single fold owner, no I/O with publication lock held, joined workers and final certificate last prevent partial clean exits.
- Threat model: caller retains conversation authorization; no transport/authentication changes. No new history machine-crash guarantee.
**Reviewer:** builder self-review, 2026-10-09.
