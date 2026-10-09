# Background conversation store

## Files read
- `internal/history/forward.go` → `Forward`, `Walk`, `Tail`: bounded raw chunks, callback progress and registered-before-catch-up notifications.
- `internal/history/log.go` → `LatestEntryID`, `AppendWithMetadata`: capture the committed bound without introducing another durable cursor.
- `internal/conversations/id.go` → `ValidID`: public-boundary canonical conversation validation.
- `internal/thread/fold.go` → `Feed`, `Items`, `Version`: serialized ownership, partial failure and detached content.
- `internal/thread/child.go` → `restoreChildren`, `resolveChildren`: private unresolved parent/report state must survive tailing, but cannot be restored from public items.
- `internal/thread/child_test.go`, `agent_test.go`, `fold_test.go`: existing pending-report, parent-repair and metadata fixtures.
- `docs/knowledge/features/thread-package.md`: raw hidden facts and private continuation state are necessary for replay equivalence.
- `docs/knowledge/features/history-package.md`, `history-package-shape.md`: exact directory containment, per-call resolution and nonblocking tail notifications.
- `docs/knowledge/decisions/042-daemon-built-thread.md` § Storage: history is the only durable record.
- `CODING-STYLE.md`, `docs/knowledge/features/development-verification.md`: bounded goroutine lifetimes and synchronized cancellation proofs.

## Context
Add independently usable in-memory thread views following committed history. Cache persistence/epochs (#3064) and daemon wiring (#3049) remain downstream. No new decision record is needed. No overlapping feature branches touch the proposed files.

## Design
`NewStore(*history.Store) *Store` constructs the owner. `Load(ctx, conversationID) error` starts replay asynchronously; duplicate loads are idempotent. The context governs that loaded worker's lifetime. `Retry(ctx, conversationID) error` only restarts an unavailable conversation, with a fresh reader and fold. Invalid IDs return the content-free history validation sentinel.
`Snapshot(conversationID) Snapshot` returns `State`, `Items`, `Version`, and generic `Err`: not loaded, rebuilding, usable or unavailable. Only usable snapshots carry items/version. Snapshot lookup does no history I/O, and copies immutable published items/content outside the map lock.
Each worker captures H using `LatestEntryID`, walks a zero-position forward reader through H with `Fold.Feed`, and publishes only on successful completion with `Fold.Version() == H`. Continue with that same reader's `Tail`; successful bounded chunks publish matching items/version. Raw hidden facts are consumed. Missing/empty logs publish version zero.
An unexported Walk/Tail interface and reader factory permit tests to gate actual history callbacks and inject fold failures without changing the public API. Production delegates to `history.Store.Forward`.
`Unload(conversationID)` cancels and joins the worker, then removes its identity. `Shutdown()` cancels and joins all workers, clears loaded state, and rejects Load/Retry. Both are safe to repeat.

## Concurrency model
One goroutine privately owns each reader/fold. A store mutex protects worker identities, retirement flags and immutable publications; it is never held during history I/O, folding, snapshot copying or joining. Retirement blocks replacement until join completes. Publication checks identity, retirement, closed state and cancellation under the mutex. Completion publishes unavailable and closes the done channel under the same lock, so retry cannot race an older worker's completion. Cancellation publishes unavailable unless unloading/shutting down. Tail unregisters through its existing defer.

## State transitions and identity reuse
| Event | Race-detector test |
| --- | --- |
| Duplicate load during paused replay; commits during replay/handoff and later tail | `TestStoreReplayTailIsolation` |
| Pending reports/unresolved children across replay cut points and tail; cross-conversation reuse | `TestStoreContinuationReopen` |
| Read/fold failure after partial replay; explicit fresh retry and repeated retry rejection | `TestStoreFailureRetry` |
| Unload during replay, tail callback or waiting; repeat and same-ID reopen | `TestStoreLifecycle` |
| Shutdown during active work; repeated/concurrent shutdown and new-work rejection | `TestStoreLifecycle` |
| Parent context cancellation without unloading | `TestStoreLifecycle` |

## Error handling
Public errors are fixed generic sentinels. Raw history/fold errors are never wrapped, retained or logged. Read/fold failure discards published usable state, leaving unavailable until explicit retry. A captured bound not fully consumed is also unavailable. Store processing never writes history.

## Testing strategy
Write tests first and observe compilation failure before adding the store. Gate real forward callbacks to prove append and other conversation progress before releasing replay, bounded chunks, captured-bound readiness, exactly-once order and replay-to-tail handoff. Compare snapshots including revisions with fresh folds of the committed raw entries. Mutate returned content and items. Exercise failed chunk partial consumption and real containment/corrupt reads with byte preservation. Reopen with pending private state and committed tails. Gate cancellation to prove join and absence of late publication.
Run `go test -race ./internal/thread ./internal/history`, `go vet ./...`, and `go build -o /tmp/builder-3063/pyry ./cmd/pyry`. The dispatcher owns the full-module `make check` gate. No live Claude checks are required.

## Open questions
None.

## Documentation handoff
Pending for documentation stage: in `docs/knowledge/features/thread-package.md`, add “Background store” describing the actual store API, bounded replay/tail handoff, consumed-version readiness, snapshot states and retry, per-conversation isolation, unload/reopen and worker shutdown. State that cache persistence, epochs and daemon wiring remain downstream.

## Security review
**Verdict:** PASS
**Findings:**
- [Trust boundaries] `Load`/`Retry` validate canonical IDs; callers establish authorization. Raw entries cross into the existing conversation-bound `Fold.Feed`; snapshots remain inert display data.
- [Tokens/secrets] No credential creation/storage. Snapshot content intentionally contains authorized history; errors expose only fixed sentinels.
- [File operations] Existing `LatestEntryID`/`Forward` preserve per-read exact directory containment and leaf checks. No new writes, permissions or persistent paths.
- [Subprocesses/cryptography] No subprocess or cryptographic operations are introduced.
- [Network/I/O] Reader callbacks remain capped by `history.MaxPageEntries`; no sockets are introduced. OUT OF SCOPE: cache persistence belongs to #3064 and daemon admission policy to #3049.
- [Errors/logs/telemetry] Raw errors can include paths/payloads. Decision: replace all worker failures with a fixed unavailable sentinel and introduce no logs or telemetry.
- [Concurrency] Retirement plus identity/cancellation checks prevent stale publication. Join occurs outside the sole store lock; Fold access stays worker-private.
- [Threat model] Authorization and network transport are caller responsibilities, with daemon wiring owned by #3049. This slice introduces no capability or transport surface.
**Reviewer:** builder (self-review per security-review checklist)
**Date:** 2026-10-09

Sizing recheck: one deliverable, approximately 750 total lines, 3 exported types, 0 migrated consumers, 4 acceptance criteria, at most 10 reject/failure branches.
