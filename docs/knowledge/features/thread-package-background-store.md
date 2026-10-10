# `internal/thread` — background store

## Background store

### Loading and replay readiness

`NewStore(h *history.Store, beforeFold ...func(context.Context, conversations.ConversationID) error) *Store`
creates a background cache owner. Ordinary production callers supply only `h`.
The first optional callback runs before each actual replay/tail chunk; it must
return promptly or honor cancellation, and failure withdraws the view. This
lets daemon tests pause real fold work rather than merely delaying `Load`
admission, and lets removal cancel and join that paused work. History remains
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
log becomes usable with no items and both `Version` and `LastShownVersion` zero.
The initial page read is required even at H zero: `ForwardReader.Walk(0)` performs
no storage read and cannot establish that empty history is readable. Items,
revisions and version match fresh full replay through the published version.

The same reader then runs `ForwardReader.Tail`. Registration precedes catch-up,
so entries committed during replay or the handoff are consumed exactly once in
ID order; later commits converge through buffered notifications. Successful tail
chunks checkpoint and publish updated items, consumed version, last-shown
watermark and epoch together. Notifications cover commits through the supplied
history store only; another store or external writer supplies no wakeup. See
[forward consumption](history-package-shape.md#forward-consumption).

`Store.Observe(conversationID)` acquires a consistent complete baseline without
history I/O. Once usable, pass its `Epoch` and `Version` to
`Store.Changes(ctx, conversationID, epoch, after)` for subsequent progress. Each
range covers `(FromVersion, Version]` and carries the epoch and last-shown
watermark at its end. At the latest published boundary, `Changes` waits for
progress or cancellation. Ranges start only at retained publication boundaries;
an arbitrary entry ID inside a chunk need not be a valid starting point.
Entries that change no items still advance the consumed version and wake waiters.

For standalone folding, `Fold.Observe()` supplies a detached baseline and
`Fold.FeedChanges(entries)` returns progress from the pre-feed version after
private joins resolve. Fold access must remain serialized; its observations
have no store epoch. `FeedChanges` preserves `Feed`'s partial-error behavior,
describing entries successfully consumed before an invalid ID; the store never
publishes that partial failed chunk. Existing `Feed`/`Snapshot` callers need no
migration.

Each `Change` identifies `ID`, `PreviousRev`, new `Rev` and consumed `Version`.
The change's version is its publication boundary and may exceed the item's
revision. `Addition` supplies a complete new item; otherwise apply named
`Fields` replacements using exported `Item` field names, then `TextAppend` to
message `Content.text`, then `Rev`. Replacements include zero/null values and
content clearing; suffixes preserve other content fields. Applying an applicable
contiguous range to its stated baseline reproduces `Fold.Items`, including
multiple affected rows and newly publishable children. Progress carries changes
rather than full `Items`.

An unprovable range returns `BaselineRequired` with no applicable changes or
items: acquire a new `Observe` baseline. This includes missing/evicted boundaries,
a requested version ahead of publication, epoch mismatch and suppression of a
previously published row. **There is no remove operation.** A delivery message
can appear in one publication before a later `send_delivered` outcome links it
to the permanent acceptance item and suppresses the standalone delivery row.
Field changes cannot repair that earlier baseline; `TestStoreObservationDelivery`
keeps delivery and linking in separate publications to constrain this case.
Hiding a queued item with `Shown == false` retains its row and can use ordinary
field changes. Rebuilding/unavailable observations expose their state, with no
partial usable items, epoch, versions or fabricated watermark.

### Snapshots and retry

`Snapshot(conversationID) Snapshot` performs no history I/O. Its fields are
`Active`, `State`, `Items`, `Version`, `LastShownVersion`, `Epoch` and `Err`:

| State | Meaning |
| --- | --- |
| `StateNotLoaded` | No loaded view, including while retiring or after shutdown. |
| `StateRebuilding` | Background recovery/replay has started; no complete checkpoint is ready for publication. |
| `StateUsable` | Detached items/content, consumed version, last-shown watermark and conversation epoch from one consistent publication; newer appends may still be pending. |
| `StateUnavailable` | The worker has ended; `Err` is the fixed `ErrUnavailable` sentinel and explicit retry can recover. |

Only `StateUsable` carries items/versions/epoch. The last-shown watermark follows
the [history-reconstructed rule](thread-package.md#cache-and-epochs), and unread
is `LastShownVersion > ReadUpTo`. The random epoch is independent of
deterministic item equality and follows the [recovery compatibility rules](thread-package.md#cache-and-epochs).
`Active` is computed from every private fold item at the same publication,
including unresolved children omitted from `Items`. An empty public item list
therefore cannot establish unload eligibility. `TestStorePrivateActiveWork`
checks an invisible active child and its later closure; daemon retirement also
requires [producer quiescence](thread-package.md#shadow-lifecycle-and-evidence).
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

### Bounded queries

`Store.HistoryPage(ctx, conversationID, upperOrder, limit) (QueryResult, error)`
reads the newest ordered public range below an exclusive upper order.
`Store.NewestWindow(ctx, conversationID, limit) (QueryResult, error)` reads the
newest ordered range below the publication's consumed `Version + 1`, plus every
active public item. Callers must already be authorized for the conversation.
Both validate canonical IDs with `history.ErrInvalidID`, reject zero or negative
limits with `ErrInvalidLimit`, and clamp positive limits to `MaxQueryItems = 256`.
The limit counts ordered selection rows; lower-order ties, active extras and
parents can make the returned item count larger than 256.

Public membership is exactly `Fold.Items`, including hidden (`Shown == false`)
and dropped items. Private unresolved children and claimed standalone delivery
rows are omitted. Selection follows current `Item.Order`, not creation-ID order:
an accepted queued message keeps its permanent ID when delivery gives it a later
order. Each result retains every returned item's current full fields and content,
deduplicated by permanent ID, with no presentation-order guarantee.

For a nonempty ordered selection, `LowerOrder` is its inclusive oldest order
and `UpperOrder` is the exclusive requested bound. Every ordered public item in
`[LowerOrder, UpperOrder)` is returned, including all ties at the lower bound;
cutting a tie to enforce the limit would leave a hole in the range.
`Continuation == LowerOrder`: pass it as the next history page's exclusive
upper order. `OlderExists` is authoritative for that publication and true exactly
when an ordered public item exists strictly below `LowerOrder`. Neither the
number of returned items nor the oldest returned parent can establish that flag.

Both reads include complete public parent closure, including shared and
multi-level ancestors outside the selected range. A history page includes
unordered (`Order == 0`) rows only as required parents. A newest window also
includes every active public row, including unordered queued messages and old
active work outside the newest ordered range, then the parents of those rows.
Active and parent extras do not consume the limit or change bounds, continuation
or older-exists. If no ordered item is eligible, including a history page with
upper order zero, `LowerOrder` and `Continuation` are zero and `OlderExists` is
false. `UpperOrder` still carries the usable query's requested bound (or
`Version + 1` for newest windows). Newest windows can return active extras even
with that empty ordered range.

`QueryResult` carries `State`, `Items`, `Epoch`, consumed `Version`, `LowerOrder`,
`UpperOrder`, `Continuation`, `OlderExists` and `Err`. Items/content and range
metadata are detached from one immutable usable publication, captured with its
epoch/version under the store mutex; newer commits may still await folding.
Rebuilding and unavailable queries return the existing snapshot state, with
`ErrUnavailable` in `QueryResult.Err` for failure. Unloaded, retiring and closed
stores return `StateNotLoaded`. Nonusable results expose no items, epoch,
version or bounds/continuation. Validation and cancellation errors use the
separate returned error; cancellation discards partial results and leaves the
conversation worker running. Queries never load a conversation, wait for
recovery or read history.

`newQueryIndex` prepares permanent-ID/parent lookup, current-order sorting and
active-row lookup from committed public items outside the global lock.
`Store.publish` installs it atomically with the matching snapshot, rebuilding
on recovery/retry and each successful publication. Failure, unload and shutdown
release worker references; a reader that already captured a publication can
finish coherently while it is replaced or retired. Binary seeks, selection,
parent traversal and returned-content copying run outside the lock, preserving
history-writer and other-conversation progress.

Request work is `O(log N + selected range + active rows + unique parents +
returned content bytes)`; active rows apply only to newest windows. Index
preparation is separately `O(N log N)` per publication, in addition to fold/replay
and checkpoint work. Bounded reads do not make recovery or publication cost
independent of conversation length. They select complete current rows by order,
whereas `Snapshot`/`Observe` clone full baselines and `Changes` supplies retained
live change ranges by consumed version. A bounded result is not a complete
baseline for applying those ranges. These daemon-internal reads implement the
[ADR 042 update-range contract](../decisions/042-daemon-built-thread.md#update-messages);
durable catch-up (#3087) and wire handlers/capability activation (#2963) are
separate consumers.

### Isolation and lifecycle

One goroutine owns each conversation's reader and fold, serializing `Feed`,
`Items`, `Version` and fold observations. Items, pending reports, unresolved
parents and join state are private to that conversation. The store mutex protects
worker identities, publications and scalar lifecycle records; history/cache I/O,
folding, snapshot copying and joining happen outside it. Paused replay therefore leaves appends
and other conversations' processing and snapshot lookup free to progress.

`Changes` captures the immutable publication, retained ranges and notification
channel under the same mutex used by publication. Publication closes/replaces
that channel, so a commit between acquiring a baseline and registering a wait
is found by the version lookup. Waiting creates no worker goroutine, performs no
history I/O and holds no lock. Cancellation returns the consumer's context error
without stopping the conversation worker. Failure, unload and shutdown notify
waiters and discard ranges; retirement returns `StateNotLoaded`, while failure
returns `StateUnavailable`. Slow consumers never hold back history writers or
other conversations.

Each conversation worker retains at most 64 publication batches and 1 MiB of
JSON-encoded changes. Older batches are evicted until both limits hold; an
oversize batch clears retained ranges while the complete baseline remains
usable. A count limit alone cannot bound large additions, and JSON escaping can
make a change exceed the byte limit even when its raw history payload fits.
Baselines, additions, replacement maps and content bytes are detached from
later observations; differences, encoding and returned copies run outside the
publication lock. `difference` clones each text suffix into its own allocation:
encoded byte accounting cannot bound a substring that pins an accumulated
message's backing allocation.

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
reload retains no earlier change batches, so epoch equality alone cannot prove
continuity from a pre-unload baseline. Use the reloaded publication as a new
baseline or require `Changes` to prove the requested range. Unload alone never
marks the lifetime clean. `Unload` validates IDs, returns
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

The daemon owns [startup discovery, retirement and final committed-version
draining](thread-package.md#shadow-lifecycle-and-evidence). `Store.Shutdown`
cancels workers; callers must finish producer draining and tail catch-up before
invoking it to preserve final commits.

### Store verification

`TestQuerySelection` and `TestQueryLimits` constrain continuous ties, empty and
oldest bounds, hidden/dropped membership, shared/multi-level parents, active
unordered extras, detached content and exact/clamped/nonpositive limits.
`TestStoreQueriesUpdates` checks delivery order placement and row suppression,
active work settling and newly public children after each committed publication.
To prove late child resolution, repair attribution to an existing older parent:
a newly created newer parent cannot join under the
[parent identity rule](thread-package-agents-and-background-work.md#parent-repair).

`TestStoreQueriesBounded` uses 36,000 committed entries across initial load,
clean reopen and unload/reload. It holds the real reader at Tail, then checks
early/middle/newest pages and newest windows against full items while counting
actual history read bytes/segment opens, index visits and allocations. Measure
both APIs: page-only allocation checks would miss a full baseline clone in
newest-window reads. Keep preparation counters/timing separate from requests.
Nested children near the beginning force `resolveChildren` to rebuild public
visibility for subsequent facts; putting active nested extras late in history,
but still outside the newest window, preserves query coverage without making
replay dominate the fixture. `TestStoreQueriesLifecycle` and
`TestStoreQueriesRetirementIsolation` force cancellation during index access and
hold captured results across publication/retirement while writers and another
conversation progress.

`TestFoldObservations`, `TestFoldObservationChildren` and
`TestObservationReplacement` independently apply additions, replacements and
suffixes and compare with full items, including clearing, private joins and
entry/chunk watermark equality. `TestStoreObservations` checks the commit before
wait-registration case, detached changes and progress with no item changes.
`TestStoreObservationRetention`, `TestStoreObservationRecovery` and
`TestStoreObservationLifecycle` constrain eviction/oversize outcomes, watermark
reconstruction, epoch reuse without old ranges, cancellation and retirement.
Retention counters alone would stay green while substring suffixes pinned full
messages. `TestStoreObservationRetainedSuffixMemory` checks actual live heap
growth for 64 single-byte appends to a 1 MiB message in a cancellable helper
process; process isolation keeps unrelated tests' allocations out of the proof.

`TestStoreReplayTailIsolation` retains 4,098 committed raw replay facts
(`history.MaxPageEntries + 2`) through real history readers, with four message
items at the beginning and across the chunk boundary; unsupported durable facts
fill the remaining entries. It pauses after the first successful chunk feed,
before the callback returns, proving a partially folded replay remains rebuilding
with no published items/version. Duplicate `Load` must preserve the worker's
identity. While A is paused, appends finish and B reaches usable publication.
The Tail handoff snapshot equals only the captured replay bound. Messages
committed during replay, handoff and later tail match independent full replay;
successful delivery consumes every raw ID exactly once in order, with at least
two replay chunks and no chunk exceeding `history.MaxPageEntries`. Mutating
returned item fields/content must leave subsequent snapshots unchanged.

Keep raw reader-bound coverage separate from growing-item folding cost.
`resolveChildren` and `recordShown` traverse item collections per fact; an
all-message fixture makes an isolation proof pay for thousands of growing rows
and repeated full comparisons. Original-fixture measurements found folding
dominated handoff work: 1.52s of 1.67s focused, and 2.24s of 2.37s under the
unchanged parallel thread/history suite. The historical five-second expiry was
not reproduced, so these measurements identify the dominant measured phase,
not the exact cause of that occurrence. Bounding public items preserves raw
delivery coverage without increasing the deadline or changing production
folding. See the [phase evidence and synchronization revisions](../../specs/architecture/3094-store-replay-tail-isolation.md#revisions).

The test's local channel waits retain five-second deadlines and name each
barrier. They distinguish worker completion from a deadline with the last
observed replay/checkpoint/publication phase, consumed count, chunks and snapshot
state/version. Verbose output separates release-to-handoff folding, walk,
checkpoint and publication timing; snapshot waits have named log context.
Append/load work returns errors and its appended entry through a buffered result
channel for the test goroutine to check. Failure cleanup releases both gates,
cancels the test-owned context and joins that operation before the earlier
`testThreadStore` cleanup joins store workers through `Store.Shutdown`.

Published Tail progress does not establish completion of test-owned delivery
bookkeeping: `Store.run` publishes inside the wrapped feed callback, while the
wrapper records successful IDs after feed returns. A snapshot can already
match full replay while the wrapper's count is short. A mutex protects recorded
data but cannot await bookkeeping that has yet to acquire it. Close/replace a
notification under the same mutex as the successful-ID count; capture the count
and current notification atomically after final publication. If incomplete, use
the finite worker-aware `final tail delivery bookkeeping` barrier before exact
count/order assertions. The serial reader completes earlier callbacks before
publishing the final chunk, so the next notification covers remaining delivery;
the shared mutex prevents a missed wakeup.

`TestStoreContinuationReopen` compares replay cut points and tails with a fresh
fold, including hidden pending reports and unresolved parents.
`TestStoreLifecycle` gates cancellation to prove joins, retirement, absence of
late publication and replay of commits made while unloaded.

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
