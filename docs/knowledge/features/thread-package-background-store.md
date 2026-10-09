# `internal/thread` — background store

## Background store

### Loading and replay readiness

`NewStore(*history.Store) *Store` creates a background cache owner. History remains
the only durable source, and store processing never writes history bytes. See
[cache and epochs](thread-package.md#cache-and-epochs) for on-disk recovery records.
`Load(ctx, conversationID) error` starts one background worker and returns
without waiting for history I/O or replay. Duplicate loads preserve the existing
worker, including an unavailable view; recovery requires `Retry`. The context
governs the loaded worker's entire lifetime. `Load` and `Retry` validate canonical
conversation IDs and return the bare `history.ErrInvalidID` sentinel for invalid
IDs. Callers must already be authorized for the conversation; existing history
reads retain exact directory containment and leaf checks.

The worker captures the surviving readable boundary H with
`history.Store.Page(conversationID, "", 1)`, using the newest entry's ID or zero
for no entries. **A warm append cursor is not a recovery boundary:**
`LatestEntryID` can remain ahead after history truncation. Recovery rejects
caches ahead of H and rotates their epoch even on unload/reload in one lifetime.
The worker establishes interrupted-run detection, creates
`history.Store.Forward(conversationID, 0)` and feeds raw entries through H into a
private `Fold` using `ForwardReader.Walk`. Each chunk contains at most
`history.MaxPageEntries`, including hidden facts and continuation evidence.
Readiness requires successful completion, `Fold.Version() == H` and a persisted
complete checkpoint; partial replay publishes no usable view. A missing or empty
log becomes usable with no items and version zero. The initial page read is
required even at H zero: `ForwardReader.Walk(0)` performs no storage read and
cannot establish that empty history is readable. Items, revisions and version
match fresh full replay through the published version.

The same reader then runs `ForwardReader.Tail`. Registration precedes catch-up,
so entries committed during replay or the handoff are consumed exactly once in
ID order; later commits converge through buffered notifications. Successful tail
chunks checkpoint and publish updated items, consumed version and epoch together.
Notifications cover commits through the supplied history store only; another
store or external writer supplies no wakeup. See
[forward consumption](history-package-shape.md#forward-consumption).

### Snapshots and retry

`Snapshot(conversationID) Snapshot` performs no history I/O. Its fields are
`State`, `Items`, `Version`, `Epoch` and `Err`:

| State | Meaning |
| --- | --- |
| `StateNotLoaded` | No loaded view, including while retiring or after shutdown. |
| `StateRebuilding` | Background recovery/replay has started; no complete checkpoint is ready for publication. |
| `StateUsable` | Detached items/content, consumed version and conversation epoch from one consistent publication; newer appends may still be pending. |
| `StateUnavailable` | The worker has ended; `Err` is the fixed `ErrUnavailable` sentinel and explicit retry can recover. |

Only `StateUsable` carries items/version/epoch. The random epoch is independent of
deterministic item equality and follows the [recovery compatibility rules](thread-package.md#cache-and-epochs).
Lookup copies an immutable publication's items and content outside the store
lock, so mutation of a returned snapshot
cannot affect later snapshots. A history/cache read or write failure, unsafe
cache leaf, failed fold, incomplete replay through H or worker-context
cancellation withdraws any usable publication and leaves the conversation
unavailable. `Snapshot.Err` remains `ErrUnavailable`; `Unload` reports
`ErrPersistence` for cache/marker I/O failure and `ErrUnavailable` for failed or
incomplete recovery. Raw errors are neither exposed, retained nor logged:
diagnostics contain no conversation content, raw payloads or host paths.

`Retry(ctx, conversationID) error` replaces only an unavailable worker with fresh
replay under the new context. An absent conversation returns `ErrNotLoaded`;
rebuilding or usable workers return `ErrNotUnavailable`. **Recreate both reader
and fold after failure:** `Fold.Feed` can consume earlier entries in a failing
chunk while the reader leaves its position unchanged. Redelivering into that
partially advanced fold would repeat consumed IDs. Completion records unavailable
state and closes the worker's done channel under the same lock, preventing an old
completion from overwriting its replacement.

### Isolation and lifecycle

One goroutine owns each conversation's reader and fold, serializing `Feed`,
`Items` and `Version`. Items, pending reports, unresolved parents and join state
are private to that conversation. The store mutex protects worker identities and
publications and scalar lifecycle records; history/cache I/O, folding, snapshot
copying and joining happen outside it. Paused replay therefore leaves appends
and other conversations' processing and snapshot lookup free to progress.

`Unload(conversationID) error` marks the worker retiring, clears its publication,
cancels and joins it, checkpointing completed progress where available before
removing its identity. Tail consumption unregisters on exit; items and private
continuation state are released while scalar lifecycle records remain for final
shutdown. Other conversations continue independently. `Load`/`Retry` return
`ErrUnloading` during retirement.
After unload returns, lookup is not loaded unless explicitly reopened
concurrently. Reopening with `Load` replays history, including commits made while
unloaded. **Public items cannot seed a resumed fold:** `Fold.Items` omits
unresolved children and private pending evidence needed by later joins. Full
replay restores that state before following the tail again.
Compatible recovery metadata preserves the epoch on reload in the same lifetime;
unload alone never marks the lifetime clean. `Unload` validates IDs, returns
`history.ErrInvalidID` for invalid ones and reports generic persistence/recovery
errors; repeated calls preserve the recorded result until another load.

`Shutdown() error` closes admission, cancels and joins all workers, and clears
loaded state. It revalidates and atomically rewrites completed checkpoints for
every conversation opened in the lifetime, including those unloaded earlier,
then completes the coordinator's run certificate last. Incomplete recovery or
any final persistence failure returns `ErrPersistence` and leaves the lifetime
uncertified; successful checkpoint writes for other conversations cannot certify
a partial clean exit. New `Load`/`Retry` work is rejected with `ErrClosed` for
valid IDs. Repeated unloads and repeated/concurrent shutdowns are safe; shutdown
callers join the same operation and receive the same result. Publication checks
worker identity, retirement, shutdown and cancellation, preventing late callbacks
from restoring released state. Cancelling a worker context alone leaves an
unavailable view until retry or unload; it does not shut down other conversations.

Daemon construction, startup reconciliation and lifecycle wiring remain
[#3049](https://github.com/pyrycode/pyrycode/issues/3049).

### Store verification

`TestStoreReplayTailIsolation` gates real history callbacks to prove bounded
chunks, captured-bound readiness, append/other-conversation progress, ordered
handoff and detached snapshots. `TestStoreContinuationReopen` compares replay cut
points and tails with a fresh fold, including hidden pending reports and
unresolved parents. `TestStoreLifecycle` gates cancellation to prove joins,
retirement, absence of late publication and replay of commits made while unloaded.

`TestStoreFailureRetry` must commit a new tail entry before injecting failure:
an empty tail can pass without exercising partial fold advancement or proving
fresh replay. `TestStoreReadSecurity` checks containment, corruption, generic
errors and byte preservation. Its snapshot-I/O proof holds the reader at a
cancellation-aware tail barrier before moving the history directory; otherwise
legitimate background reads can fail and falsely implicate snapshot lookup.
Run the offline checks with `go test -race ./internal/thread ./internal/history`.

`TestStoreCacheContinuation` compares every reopen cut point and subsequent tail
publication with fresh replay, checking epoch retention separately from item
equality. `TestStoreCacheEpochs` covers clean/unclean exits and incompatible,
missing, corrupt, partial and ahead caches. `TestStoreCacheInterrupted`,
`TestStoreCacheShutdown` and `TestStoreCacheFinalCertificate` cover incomplete
recovery and failure to certify clean shutdown, including unloaded participants.
`TestStoreCacheSecurity` checks unsafe leaves, containment, interrupted atomic
replacement, history byte preservation and repair/retry.

**Keep the supplied history store warm in truncation tests.** Constructing a
fresh history store hides a stale append cursor: recovery can pass while daemon
reopen/retry remains unavailable. `TestStoreCacheWarmHistoryTruncation` keeps the
same store across clean reopen, unload/reload and retry at zero/nonzero surviving
versions, proving epoch rotation and continuation/tail equality. Keep the
`Fold.Version() == H` guard too: a successful walk can still be incomplete.
`TestStoreCacheZeroHistoryValidation` gates the tail so a later read failure
cannot mask a premature empty usable publication over unreadable history;
nonempty replay tests alone cannot catch that path.
