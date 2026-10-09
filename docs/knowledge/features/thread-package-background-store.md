# `internal/thread` — background store

## Background store

### Loading and replay readiness

`NewStore(*history.Store) *Store` creates an in-memory owner. History remains the
only durable record, and store processing never writes history bytes.
`Load(ctx, conversationID) error` starts one background worker and returns
without waiting for history I/O or replay. Duplicate loads preserve the existing
worker, including an unavailable view; recovery requires `Retry`. The context
governs the loaded worker's entire lifetime. `Load` and `Retry` validate canonical
conversation IDs and return the bare `history.ErrInvalidID` sentinel for invalid
IDs. Callers must already be authorized for the conversation; existing history
reads retain exact directory containment and leaf checks.

The worker captures H with `history.Store.LatestEntryID`, creates
`history.Store.Forward(conversationID, 0)` and feeds raw entries through H into a
private `Fold` using `ForwardReader.Walk`. Each chunk contains at most
`history.MaxPageEntries`, including hidden facts and continuation evidence.
Readiness requires successful completion and `Fold.Version() == H`; partial
replay publishes no usable view. A missing or empty log becomes usable with no
items and version zero. Items, revisions and version match fresh full replay
through the published version.

The same reader then runs `ForwardReader.Tail`. Registration precedes catch-up,
so entries committed during replay or the handoff are consumed exactly once in
ID order; later commits converge through buffered notifications. Successful tail
chunks publish updated items and their consumed version together. Notifications
cover commits through the supplied history store only; another store or external
writer supplies no wakeup. See [forward consumption](history-package-shape.md#forward-consumption).

### Snapshots and retry

`Snapshot(conversationID) Snapshot` performs no history I/O. Its fields are
`State`, `Items`, `Version` and `Err`:

| State | Meaning |
| --- | --- |
| `StateNotLoaded` | No loaded view, including while retiring or after shutdown. |
| `StateRebuilding` | Background replay has started but has not completed through H. |
| `StateUsable` | Detached items/content and an atomically consistent consumed version; newer appends may still be pending. |
| `StateUnavailable` | The worker has ended; `Err` is the fixed `ErrUnavailable` sentinel and explicit retry can recover. |

Only `StateUsable` carries items/version. Lookup copies an immutable publication's
items and content outside the store lock, so mutation of a returned snapshot
cannot affect later snapshots. A read/fold failure, incomplete replay through H
or worker-context cancellation withdraws any usable publication and leaves the
conversation unavailable. Raw errors are neither exposed, retained nor logged:
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
publications; history I/O, folding, snapshot copying and joining happen outside
it. Paused replay therefore leaves appends and other conversations' processing
and snapshot lookup free to progress.

`Unload(conversationID)` marks the worker retiring, clears its publication,
cancels and joins it, then removes its identity. Tail consumption unregisters on
exit; items and private continuation state are released. Other conversations
continue independently. `Load`/`Retry` return `ErrUnloading` during retirement.
After unload returns, lookup is not loaded unless explicitly reopened
concurrently. Reopening with `Load` replays history, including commits made while
unloaded. **Public items cannot seed a resumed fold:** `Fold.Items` omits
unresolved children and private pending evidence needed by later joins. Full
replay restores that state before following the tail again.

`Shutdown()` closes admission, cancels and joins all workers, and clears loaded
state. New `Load`/`Retry` work is rejected with `ErrClosed` for valid IDs. Repeated
unloads and repeated/concurrent shutdowns are safe. Publication checks worker
identity, retirement, shutdown and cancellation, preventing late callbacks from
restoring released state. Cancelling a worker context alone leaves an unavailable
view until retry or unload; it does not shut down other conversations.

Cache persistence and recoverable epochs remain
[#3064](https://github.com/pyrycode/pyrycode/issues/3064). Daemon construction,
startup reconciliation and lifecycle wiring remain
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
