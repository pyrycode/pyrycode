# Independent conversation certificates

## Files read
- `internal/thread/cache.go`: `recoveryCandidate`, `writeCacheFile`, `entryChecksum`; preserve checksum encoding and contained regular-file operations.
- `internal/thread/store.go`: `run`, `Unload`, `Shutdown`; worker joins and fully consumed progress own retirement.
- `internal/thread/cache_test.go`, `cache_compact_test.go`, `checkpoint_test.go`: recovery, security, continuation and lifecycle proofs to adapt.
- `cmd/pyry/thread_shadow.go`: `follow`, `shutdown`; caller-owned catch-up remains before store shutdown.
- `cmd/pyry/channel_delivery_session_test.go`: `TestChannelDelivery_SessionDaemonWiring`; startup replay and final consumed-version certification through the daemon.
- `internal/history/log.go`, `cache_dir.go`: `LogDir`, `EnsureLogDir`; exact containment and readable history bounds.
- `docs/knowledge/features/thread-package.md` (Cache and epochs) and `thread-package-background-store.md`: retain warm-history truncation, incomplete-walk rejection and private continuation replay lessons.
- `CODING-STYLE.md`, `docs/knowledge/features/development-verification.md`: persistence and executed-test boundaries.

## Context
Replace shared store-lifetime certification with independent conversation retirement.
History remains authoritative and replay starts at entry 1. ADR 045 already records
the broader design; resumable checkpoints remain future work.
Prerequisite #3382 is merged. No other fetched feature branch touches these files.
Sizing: one deliverable, about 750 written lines including plan and adapted tests,
zero new exports, zero changed production consumer call sites, five criteria,
and fewer than ten distinct lifecycle rejection outcomes. Documentation is handed off.

## Design
- Persist `thread-certificate.json`, schema 1: rules, epoch, consumed version,
  last-shown version, checksum and `Clean`; no items or shared-run references.
- Read only this leaf for recovery. Validate required evidence, compatible rules,
  token, clean status and readable bound; replay at saved V must match checksum
  and reconstructed last-shown before epoch reuse. Otherwise mint a fresh epoch.
- Before replay, atomically replace evidence with a durable dirty record using
  the candidate's progress/epoch, or fresh zero progress. Dirty evidence is never
  reusable; replay may subsequently reject its candidate and rotate in memory.
- After that successful write, remove only allow-listed regular legacy leaves
  in the same contained directory. Reject redirects/special files without following
  them; unrelated leaves and history remain untouched. Cleanup failures withhold usability.
- No publication/idle/timed persistence. Remove coordinator, run records, recovery
  markers and timer scheduling. Keep scalar progress/results for repeated lifecycle calls.
- Joined retirement writes clean evidence from the last complete captured fold,
  even at unchanged V. Shutdown joins all remaining workers independently and
  aggregates failure; it never rewrites unloaded certificates or reads their new history.

## Concurrency model
One worker owns each fold and synchronous reader callbacks. Context cancellation
stops replay/tail; retirement joins `done`. Store mutex protects identity and
publication only; metadata I/O remains outside it. Concurrent shutdowns share
one result and concurrent unloads join the same worker. No timer or tail helper
goroutine remains. Parent-context cancellation alone cannot certify clean.

## State transitions and identity reuse
| Event | Race-enabled proof |
| --- | --- |
| Clean reopen, unload/reload and unchanged retirement | `TestStoreCheckpointLifecycle`, `TestStoreCacheCompact` |
| Dirty load/retry, no tail/idle/elapsed writes | `TestStoreCertificateWrites`, `TestStoreCheckpointFailures` |
| Context stop or interrupted recovery | `TestStoreCheckpointUncleanContinuation`, `TestStoreCacheInterrupted` |
| Cancellation during tail chunk | `TestStoreCheckpointCancelledFold` |
| Independent final failure and concurrent shutdown | `TestStoreCacheShutdown` |
| Unloaded clean survives another loaded unclean stop | `TestStoreCacheFinalCertificate` |
| Saved version replacement/truncation and continuation | prerequisite checksum/warm-truncation/partial-feed/continuation tests |
| Legacy migration, unsafe cleanup and dirty durability | `TestStoreCertificateMigration`, `TestStoreCertificateDirectorySync`, `TestStoreCacheSecurity` |

## Error handling
Snapshot failures expose only `StateUnavailable`/`ErrUnavailable`. Metadata failures
retain `ErrPersistence` for lifecycle callers; shutdown reports persistence failure
when any worker or recorded retirement failed, after attempting healthy workers.
Incomplete replay never becomes clean. Failed dirty writes retain no usable view.

## Testing strategy
Write lifecycle replacement-count and durability/migration scenarios first and
observe failures. Adapt obsolete shared-run and scheduled-checkpoint assertions.
Retain full replay/tail parity, checksum-at-V, missing fields, warm truncation and
partial-feed tests. Run `go test -race ./internal/thread ./internal/history`,
`go vet ./...` and `go build -o /tmp/builder-3383/pyry ./cmd/pyry`.
The dispatcher verifier owns `make check` (the full-module gate).

## Open questions
None.

## Documentation handoff
Pending for documentation stage: update `docs/knowledge/features/thread-package.md`,
"Cache and epochs", and `docs/knowledge/features/thread-package-background-store.md`,
"Loading and replay readiness", "Snapshots and retry" and "Isolation and lifecycle".
Describe certificate fields, checksum-based clean epoch validation, durable dirty
marking before usability, independent clean unload/shutdown, removal of publication,
idle and timed checkpoints/shared-run state, legacy cleanup and rollback. Retain
full replay, history-derived last-shown reconstruction, caller-owned draining and
separate change-range continuity rules. ADR 045 resumable checkpoints remain future work.

## Security review
**Verdict:** PASS

- Trust boundaries: `recoveryCandidate` validates untrusted disk evidence; replay alone establishes reusable identity.
- Tokens/cryptography: preserve crypto/rand 128-bit epochs and framed SHA-256 evidence; epochs are public identities, not authentication secrets.
- Files: MUST FIX addressed by design: dirty rename must sync its directory before usability. Preserve `0600`, temp sync/close, atomic replacement, `cacheLeaf`, `LogDir` and no-follow regular reads. Cleanup checks every legacy leaf and never traverses redirects.
- Subprocesses/network: no new subprocess or network boundary; only existing local history I/O.
- Errors/logging: generic sentinels only; no new payload/path logging.
- Concurrency: worker-owned writes and joined retirement prevent late certification; no store lock across I/O or joins.
- Threat model: local corrupt/redirected metadata is covered; authenticated transport contracts are unchanged. Hostile concurrent replacement of ancestor directories retains the existing containment model.

**Reviewer:** builder (self-review)
**Date:** 2026-10-15

## Revisions
- 2026-10-15: `TestStoreCertificateFinalSyncFailure` showed that a failed
  post-rename directory sync could leave readable clean evidence. Invalidate
  that replacement with contained best-effort removal and directory sync before
  returning `ErrPersistence`. Parent-context cancellation is checked separately
  from retirement cancellation so racing lifecycle calls cannot authorize it.
- 2026-10-15: Verifier finding 1 identified stale daemon integration assertions.
  `TestChannelDelivery_SessionDaemonWiring` reads `thread-certificate.json` and
  requires `Clean` at consumed version 2 after startup replay/idle retirement and
  after joined shutdown. Production design and security boundaries are unchanged.
  Rework validation includes the focused race test, `make check` and `make build`.
