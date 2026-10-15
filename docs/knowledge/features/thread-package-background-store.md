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
certificates ahead of H and rotates their epoch even on unload/reload in one
lifetime. Before replay, it durably writes `thread-certificate.json` with
`Clean: false` and removes only allow-listed regular legacy metadata leaves;
failed dirty marking or cleanup withholds usability. The worker then creates
`history.Store.Forward(conversationID, 0)` and feeds raw entries through H into a
private `Fold` using `ForwardReader.Walk`. Each chunk contains at most
`history.MaxPageEntries`, including hidden facts and continuation evidence.
For eligible compact recovery metadata at V, replay must first reach exactly V
with the recorded stored-entry checksum and history-reconstructed last-shown
watermark before reusing the epoch. Invalid or mismatched evidence rotates the
epoch; replay continues through H either way. Eligibility requires a compatible
clean per-conversation certificate with valid fields and V within readable H;
legacy-only startup rotates once. Cached fields never seed the fold. Full replay
remains required; [ADR 045's resumable checkpoints and persistent index](../decisions/045-thread-index-over-history.md)
are future work. There is no shared-run eligibility or coordinator certificate.
Readiness requires durable dirty marking (including directory sync), successful
replay and `Fold.Version() == H`; partial replay publishes no usable view.
Recovery stays `StateRebuilding` until these conditions hold. A missing or empty
log becomes usable with no items and both `Version` and `LastShownVersion` zero.
The initial page read is required even at H zero: `ForwardReader.Walk(0)` performs
no storage read and cannot establish that empty history is readable. Items,
revisions and version match fresh full replay through the published version.

The same reader then runs `ForwardReader.Tail`. Registration precedes catch-up,
so entries committed during replay or the handoff are consumed exactly once in
ID order; later commits converge through buffered notifications. Successful tail
chunks publish updated items, consumed version, last-shown watermark and epoch
together without certificate writes. Changing or unchanged tail progress,
active-to-idle transitions and elapsed intervals perform no persistence; the
checkpoint timer is removed. Checksum and watermark evidence advance in memory
only with successfully folded and captured progress; a partially failed or
interrupted feed cannot advance retirement evidence. Dirty-write failure leaves
`StateUnavailable` with generic `ErrUnavailable`; `Retry` reconstructs from
history and writes dirty again. Joined retirement writes clean once, including
unchanged progress. See [cache and epochs](thread-package.md#cache-and-epochs)
for certificate fields, durability, migration/rollback and persistence errors.
Notifications cover commits through the supplied history
store only; another store or external writer supplies no wakeup. See
[forward consumption](history-package-shape.md#forward-consumption).

`recordShown` preserves deduplicated post-join candidates across the chunk for
`publicationCapture.capture`. Capture compares only those rows' final public
states against the previous publication, in creation-ID order, using
`itemDifference`'s replacement and suffix rules. Repeated touches coalesce;
late appearance, reparenting and suppression use membership at publication
boundaries, not intermediate entry states. Ordinary tail capture neither calls
whole-thread `Fold.Items` nor compares the whole thread with `difference`.
Copying the stable item-pointer slots and affected lookup buckets still scales
with the thread; query memberships are maintained from the captured affected
slots. A failed feed chunk never reaches capture or publication.

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

The daemon's installed `daemonThreadPublisher.publish` uses these observations
as its only item source: full additions from a usable baseline, then ordered
changes from its saved epoch/version. It never reconstructs items from legacy
emitter facts. Field replacements are translated to `ThreadItem` JSON keys;
combined replacements/text become consecutive changed/append messages with the
same final revision. See [daemon publication](thread-package.md#shadow-lifecycle-and-evidence).

**A fresh baseline cannot repair an existing consumer silently.** Unload/reload
and retry release retained live batches even if cache validation preserves the
epoch. Nonusable state, epoch mismatch, evicted ranges or linked-delivery
suppression require `RetireLiveState` for affected thread consumers before the
publisher forgets its cursor and obtains a fresh usable `Observe`. Pending
dependent deltas are quarantined immediately, rather than drained while gap
teardown waits on relay Run. No `base_rev` is guessed and no partial recovery
is published. `Store.CatchUp` proves a separate reconstructed query range;
the [installed daemon providers](thread-package.md#shadow-lifecycle-and-evidence)
serve catch-up/pages from this same store. Reconnect initialization requires
explicit requests for each open conversation.

### Snapshots and retry

Production recovery validates the stored-entry checksum and reconstructed
last-shown watermark at the saved version instead of comparing rendered items.
Publication capture uses `renderShared` on affected public rows to resolve the
[standalone](thread-package-main-thread-folding.md#standalone-source-entries),
[assistant/ordinary-tool](thread-package-main-thread-folding.md#main-work-source-entries)
and [accepted-send recipes](thread-package-main-thread-folding.md#accepted-sends-and-durable-outcomes)
after excluding suppressed delivery rows and children without public parents.
Internal capture borrows immutable source payload or inline `Content` bytes,
reuses unchanged item pointers and replaces affected public values. Folding
replaces content allocations rather than mutating published bytes. Each captured
item has its own allocation: pointers into one shared item array would let an
unchanged row pin replaced content belonging to every other row in that array.
Older publications and captured query indexes remain stable across later feeds.
Internal stable slots include nil entries for private or withdrawn rows;
`detachPublication` skips them at full-output boundaries and preserves nil
`Items` when no public row survives. Slot count is not public item count.
Any required missing reference or source error rejects the entire candidate
publication, including when another reference in the same batch resolved.

Public `Fold.Render`, `Fold.Items`, `Fold.Observe`, `Fold.FeedChanges` and all
Store item/change/query results still return complete detached content with
their existing signatures. Public rendering copies source payload bytes;
`Store.Snapshot` captures immutable pointers under the mutex and materializes
detached values outside it. Load/retirement certificates write only the compact
scalar evidence described under [cache and epochs](thread-package.md#cache-and-epochs),
without materializing detached items; they cannot supply a cache-only view or bypass
required reference rendering before usable publication.
Legacy folds created by `New` remain inline.

The worker-owned `recentSource` resolves IDs from the current feed chunk, then
its recent-entry FIFO, then `history.Store.Lookup` for remaining IDs only, bound
to the worker's conversation. Requests are sorted and deduplicated before
resolution, so authoritative Lookup receives ascending unique misses and no
feed/recent hits. An all-hit batch performs no authoritative Lookup or segment
reads. The sorted requests and increasing chunk are swept together: rescanning
the chunk for each ID makes even an all-hit batch quadratic. The chunk is
borrowed through folding/rendering and released after publication; production
folds retain no unbounded entry map.

The recent FIFO holds at most 256 entries within an inclusive 1 MiB retained
storage budget per loaded conversation. Accounting includes the source and
fixed entry backing storage, detached payload capacity, copied strings and
pointed session/visibility metadata. Entries that cannot fit are skipped before
copying. Retained payloads have exact-size backing allocations; strings and
metadata are detached, and timestamps are normalized to UTC to avoid retaining
arbitrary location graphs. Counting payload length alone would let a small
slice or substring pin a much larger feed allocation outside the budget.

Store snapshots and query rows still retain rendered content until
[#3386](https://github.com/pyrycode/pyrycode/issues/3386); the recent-entry budget
does not bound that content or deferred inline/private fold payloads.

`Snapshot(conversationID) Snapshot` performs no history I/O. Its fields are
`Active`, `State`, `Items`, `Version`, `LastShownVersion`, `Epoch` and `Err`.

`Store.Status(conversationID) Snapshot` returns the same metadata and readiness
with nil `Items` in every state. It captures `State`, `Version`, `Epoch`,
`Active`, `LastShownVersion` and `Err` from one publication under `Store.mu`,
which also serializes publication, retry replacement and retirement. Concurrent
reads therefore cannot mix metadata from different publications. It performs
no history/cache I/O, worker loading, item traversal or item/content copying,
and allocates zero bytes regardless of thread length. Use it for progress and
readiness checks; `Store.Snapshot` and `Store.Observe` remain complete detached
reads for consumers that need items or an observation baseline.

Both reads retain these readiness rules; the items described below are returned
by `Snapshot`:

| State | Meaning |
| --- | --- |
| `StateNotLoaded` | No loaded view, including while retiring or after shutdown. |
| `StateRebuilding` | Durable dirty marking, legacy cleanup or full replay is still pending; no usable publication exists. |
| `StateUsable` | Detached items/content, consumed version, last-shown watermark and conversation epoch from one consistent publication; newer appends may still be pending. |
| `StateUnavailable` | The worker has ended; `Err` is the fixed `ErrUnavailable` sentinel and explicit retry can recover. |

Only `StateUsable` carries versions/epoch, including an empty usable conversation;
only `StateUnavailable` carries `ErrUnavailable`, and every other state has nil `Err`.
`Status.Active` preserves private activity even with no public items, so nil
`Items` cannot establish inactivity or readiness. The last-shown watermark follows
the [history-reconstructed rule](thread-package.md#cache-and-epochs), and unread
is `LastShownVersion > ReadUpTo`. The random epoch is independent of
deterministic item equality and follows the [recovery compatibility rules](thread-package.md#cache-and-epochs).
`Active` is maintained by updating an active-index set from affected private and
public fold rows at each publication, including unresolved children omitted from
`Items`; it requires no whole-fold activity scan. An empty public item list
therefore cannot establish unload eligibility. `TestStorePrivateActiveWork`
checks an invisible active child and its later closure; daemon retirement also
requires [producer quiescence](thread-package.md#shadow-lifecycle-and-evidence).
Lookup copies an immutable publication's items and content outside the store
lock, so mutation of a returned snapshot
cannot affect later snapshots. A missing/unreadable reference during rendering
or a delivery join withholds the entire candidate publication, withdraws any
prior usable view and clears query/change state. Only `StateUnavailable` with
generic `ErrUnavailable` remains, with no items, version, last-shown watermark
or epoch. A history/cache read or write failure, unsafe
cache leaf, failed fold, incomplete replay through H or worker-context
cancellation withdraws any usable publication and leaves the conversation
unavailable. `Snapshot.Err` remains `ErrUnavailable`; `Unload` reports
`ErrPersistence` for certificate/legacy-cleanup I/O failure and `ErrUnavailable`
for failed or incomplete recovery. Raw errors are neither exposed, retained nor logged:
diagnostics contain no conversation content, raw payloads or host paths.

`Retry(ctx, conversationID) error` replaces only an unavailable worker with fresh
replay under the new context after the source is repaired. It creates a new
reader, reference fold and recent source rather than reusing partial progress.
It invalidates any previous clean claim with another durable dirty write before
usability; cached progress never seeds the fold. An absent conversation returns
`ErrNotLoaded`; rebuilding or usable workers return `ErrNotUnavailable`. **Recreate both reader
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

`Store.CatchUp(ctx, conversationID, epoch, afterVersion, limit) (QueryResult, error)`
requires the same caller authorization and canonical ID validation. For a
nonzero saved version V and a matching epoch, it returns current full states of
every public item touched in `(V, H]`, every active public item, and complete
public parent closure, once per permanent ID; H is the captured publication's
consumed `Version`. Touches include revision/state changes and first public
appearance, even when a child's creation and final revision predate V. Hidden
and dropped rows remain public; unresolved private children and claimed delivery
rows do not. Entries producing no items still advance H. V need not be a retained
live-publication boundary; V equal to H immediately returns active rows/parents.
Nonzero catch-up ignores `limit`, including nonpositive values, and never
truncates the required items to an ordered-window size.

`MaxCatchUpGap = 1024` is an inclusive history-ID-distance bound: `H - V == 1024`
is allowed, while 1025 requires reset. Nonzero reads reset for an empty or
mismatched epoch, V greater than H, an oversized gap, incomplete or mismatched
publication evidence, a missing history ID after V, a missing required public
parent, or suppression of a previously public row after V. Reading every
surviving entry does not prove continuity: `Fold.Feed` records the greatest
missing ID, and only queries with V at or beyond that floor avoid crossing it.
Projection suppression also survives replay: linking a delivery to its permanent
acceptance item removes the standalone public delivery row. Clients have no
remove operation, so current remaining states cannot repair that earlier view.
Changing `Shown` to false or dropping a queued send retains its row and does
not itself require reset.

For `afterVersion == 0`, an empty or matching epoch selects `NewestWindow` from
the same captured publication, including its page metadata, active extras and
parents. Positive-limit validation and the 256 ordered-row clamp still apply;
a nonempty mismatched epoch requires reset. This window is not subject to the
catch-up gap bound.

`QueryResult` carries `State`, `Items`, `Epoch`, consumed `Version`, `LowerOrder`,
`UpperOrder`, `Continuation`, `OlderExists`, `FromVersion`, `ResetRequired` and
`Err`. Successful nonzero catch-up certifies `(FromVersion, Version]` for its
epoch and leaves all page fields zero. A reset carries `StateUsable`, current
epoch/version and `ResetRequired: true`, with no items, covered range or page
metadata; the separate error is nil. Acquire a fresh newest window rather than
apply a partial catch-up. Items/content and range metadata are detached from
one immutable usable publication, captured with its
epoch/version under the store mutex; newer commits may still await folding.
Rebuilding and unavailable queries return the existing snapshot state, with
`ErrUnavailable` in `QueryResult.Err` for failure. Unloaded, retiring and closed
stores return `StateNotLoaded`. Nonusable results expose no items, epoch,
version, covered range, reset flag or page metadata. Validation and cancellation
errors use the separate returned error; cancellation discards partial results and leaves the
conversation worker running. Queries never load a conversation, wait for
recovery or read history.

Initial usable replay, including recovery/retry, builds permanent-ID/parent
lookup and ordered/active membership with `newPublicationIndex`, then adds
latest-public-touch and coverage/suppression evidence with `prepareCatchUp`.
`newQueryIndex` adapts value slices for test callers and fresh-build oracles.
After that, `queryIndex.advance` consumes the affected stable slots preserved by
`publicationCapture.capture`; ordinary tail publication neither rebuilds these
indexes, scans unrelated rows to discover changes nor sorts the whole index.
Permanent-ID lookup uses 256 hash-map buckets, cloning each affected bucket
once. Ordered, active and latest-public-touch membership use immutable AVL
trees with copied update/rotation paths and subtree sizes for bounded selection.
Hash lookup avoids a tree seek for each ancestor in parent closure.

Each affected row loses its old memberships before its current public state is
inserted. Withdrawn or claimed standalone delivery rows disappear from lookup
and selection; late-resolved children enter at their current order. A delivered
queued acceptance keeps its ID and moves to the delivery row's order. Older-order
appearances and deliveries are inserted at their proper positions, including
ties, rather than appended to the end. Touch ordering uses `Fold.touches`, so
first public appearance is selected even when creation ID and final `Item.Rev`
are older. Coverage, missing-ID and suppression evidence advances from fold
metadata even when an entry changes no public rows; invalid-touch evidence is
maintained without rescanning the index.

`Store.publish` installs the immutable index and matching snapshot scalars
atomically. `Store.captureQuery` captures both under `Store.mu`, then releases
it before rank/range selection, parent traversal or detached Content copying.
Old roots, lookup buckets and item bytes remain immutable: a captured read
finishes coherently through later feed, replacement or retirement. Failure,
unload and shutdown release worker references without invalidating readers.
Paused selection or copying leaves history writers and other conversations free
to progress.

Request work is `O(log N + selected range + active rows + unique parents +
returned content bytes)`; catch-up selects touched rows by version, and active
rows apply to catch-up and newest windows. No request scans the conversation,
reads history or clones a full `Snapshot`/`Observe` baseline. Index
construction is separately `O(N log N)` on initial usable replay. Tail semantic
index work follows affected rows and balanced-tree paths; copying stable slot
pointers and affected lookup-bucket storage can still grow with history and
belongs in publication allocation accounting. Capture copies no Content bytes.
Bounded reads do not make recovery or total publication cost independent of
conversation length. `HistoryPage`/`NewestWindow` select current
rows by order; `CatchUp` selects current states by reconstructed touch version.
`Snapshot`/`Observe` clone full baselines and `Changes` supplies retained
live change ranges by consumed version. A bounded result is not a complete
baseline for applying those ranges. These reads implement the
[ADR 042 update-range contract](../decisions/042-daemon-built-thread.md#update-messages).
Production `installDaemonThreadRelay` forwards `ThreadCatchUp` to `Store.CatchUp`
and `ThreadPage` to `Store.HistoryPage` on the real publisher store, using the
supplied request context and named conversation. It returns the captured result
unchanged: full items, epoch/version, coverage, reset/nonusable state and page
metadata. The adapters perform no load, raw-history read, full projection clone
or second selection. Relay owns authorization, mapping, framing and terminal
delivery under the [wire query contract](../../protocol-mobile.md#thread-query-messages).

Measure the installed request after the store becomes usable:
`TestDaemonThreadQueriesBounded` separates committed-history/recovery preparation
from requests over 36,000 entries and bounds adapter allocations by returned
rows. Its source check constrains the forwarding path; the retained
`TestCatchUpBoundedRecovery` and `TestStoreQueriesBounded` supply actual backend
history-read, visit and allocation proofs. An allocation bound alone would not
detect an allocation-free full scan or history read. These request bounds do
not include background recovery or publisher baseline preparation.

### Isolation and lifecycle

One worker owns each conversation's fold, completed progress and certificate
writes. It runs replay and the same reader's `Tail` synchronously, serializing
feed callbacks, capture and publication; no tail helper goroutine or checkpoint
timer remains. Items, pending reports, unresolved parents and join state are
private to that conversation. The store mutex protects worker identities,
publications and scalar lifecycle records; history/certificate I/O, folding,
snapshot copying and joining happen outside it. Paused replay therefore leaves
appends and other conversations' processing and snapshot lookup free to progress.

`threadShadow.follow` polls `Store.Status` every 50 ms for readiness, consumed
version and private activity without traversing or copying items/content.
`Status.Active` includes private unresolved work omitted from public `Items`;
nil items do not establish inactivity. Load/retry and quiescent unload decisions
use the captured status, taken before publication can wait on delivery. Retirement
retains that consumed boundary so a later append still triggers reload. Activity
transitions and elapsed time do not write certificates.

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
publication lock. `itemDifference` clones each text suffix into its own allocation:
encoded byte accounting cannot bound a substring that pins an accumulated
message's backing allocation.

`Unload(conversationID) error` marks the worker retiring, clears its publication,
cancels and joins it, certifying its last complete captured progress with
`Clean: true` before removing its identity. Unchanged V still requires this
replacement. Tail consumption unregisters on exit; items and private continuation
state are released while scalar lifecycle results remain. Other conversations
continue independently. `Load`/`Retry` return `ErrUnloading` during retirement.
After unload returns, lookup is not loaded unless explicitly reopened
concurrently. Reopening with `Load` replays history, including commits made while
unloaded. **Public items cannot seed a resumed fold:** `Fold.Items` omits
unresolved children and private pending evidence needed by later joins. Full
replay restores that state before following the tail again.
Compatible recovery metadata preserves the epoch on reload in the same lifetime;
reload retains no earlier change batches, so epoch equality alone cannot prove
continuity from a pre-unload baseline. After usable recovery, `CatchUp` can
prove an in-bound saved version through reconstructed history evidence even
without those batches. Live `Changes` still needs retained ranges or a new
complete `Observe` baseline. Successful unload independently certifies this
conversation; another loaded conversation's unclean stop cannot invalidate it.
`Unload` validates IDs, returns
`history.ErrInvalidID` for invalid ones and reports generic persistence/recovery
errors; repeated calls preserve the recorded result until another load.

Cancellation during `beforeFold` must retain `context.Canceled` internally:
mapping it to generic fold failure would prevent retirement from certifying the
preceding complete progress. `TestStoreCheckpointCancelledFold` checks the saved
version excludes the interrupted chunk. Certification also requires the original
load context to remain uncancelled: parent cancellation racing unload/shutdown
cannot authorize a clean write. Interrupted recovery and other fold failures
remain unavailable. Lifecycle persistence failures remain `ErrPersistence`,
including after the worker has been unloaded.

`Shutdown() error` closes admission, cancels and joins all workers, and clears
loaded state after independently certifying each remaining worker's complete
progress, including unchanged V. Already-unloaded certificates are never rewritten,
and history committed while unloaded is not folded until reopening. Shutdown
attempts healthy workers even when another final write fails: the failed
conversation stays uncertified while healthy ones become clean and retain their
epochs on reopen. Incomplete recovery or any recorded retirement/persistence
failure returns `ErrPersistence`; there is no final shared-run write.
New `Load`/`Retry` work is rejected with `ErrClosed` for
valid IDs. Repeated unloads and repeated/concurrent shutdowns are safe; shutdown
callers join the same operation and receive the same result. Publication checks
worker identity, retirement, shutdown and cancellation, preventing late callbacks
from restoring released state. Cancelling a worker context alone leaves an
unavailable view until retry or unload; it does not shut down other conversations.

The daemon owns [startup discovery, retirement and final committed-version
draining](thread-package.md#shadow-lifecycle-and-evidence). `Store.Shutdown`
cancels workers; callers must finish producer draining and tail catch-up before
invoking it to preserve final commits. Status-only polling preserves this order:
after producers finish, shadow shutdown discovers final activity and joins
coordinators once their usable consumed versions reach the final history
boundaries. History/fold failures end final waiting without certifying complete
draining; successful persistence alone cannot establish a clean daemon exit.

### Store verification

`TestReferenceSourceOrder` checks feed/recent/Lookup precedence, deduplicated
miss-only requests and the all-hit no-I/O path. `TestReferenceSourceBatch` uses
36,000 all-hit IDs with duplicate/unordered requests and stale recent data to
exercise the batch sweep and feed precedence. `TestReferenceSourceBound` checks
detached payload capacity and pointed metadata, eviction and oversized-entry
bypass. `TestReferenceMissingAtomic` requires no partial render for incomplete
or unreadable lookup results. `TestStoreReferenceRetry` forces creating-entry
eviction before missing/unreadable authoritative reads during replay and tail.
Its tail case uses a delivery join that must resolve the evicted message:
unrelated no-op tails reuse unchanged content and cannot exercise read failure.
It verifies withdrawal of all usable state and equality with fresh legacy replay
after repair and `Retry`. `TestStoreReferenceWorkRetry` extends this to text and
terminal enrichment references, including a completed ordinary child tool.
Its tail failure requires ancestor closure after recent-cache eviction to mark
the rows affected: ignored history alone reuses the immutable slots and can
leave a broken source unread, making a failure test pass without exercising it.

`TestAffectedPublicationOracle` compares affected capture with whole-thread
`difference` after single-entry and chunked corpus feeds, including private
activity, late children, reparenting, delivery/suppression and no-op entries.
`TestAffectedPublicationReplacement` checks clearing, null and mixed content
replacement/text append. `TestAffectedPublicationOwnership` retains old
publications, changes and query results across later feeds and concurrent reads;
it checks pointer reuse, borrowed content and mutation of detached results.
Retain internal pointers after text-buffer initialization to prove borrowed
Content ownership; detached `Fold.Items` snapshots can stay correct while
reusable buffers corrupt old publications. See the
[text-buffer ownership checks](thread-package-main-thread-folding.md#validation-and-testing).
`BenchmarkPublicationPhases` isolates capture/comparison from replay, checkpoint
I/O, query preparation and output encoding; see the
[bounded comparison evidence](../../specs/architecture/3388-immutable-publication.md#performance-evidence).

`TestIncrementalQueryOracle` compares incremental indexes with fresh construction
after every generated and retained shadow entry, including late children,
delivery suppression and no-item progress. `TestThreadRecordedBaselines` reuses
its recorded-frame derivation and normalization for per-entry tail versus fresh
replay comparisons across all three histories. Compare full item states and
result metadata, disregarding only item order (align independently generated
Store epochs). `TestIncrementalTreeOwnership` retains lookup buckets and roots
through concurrent reads and later membership/order updates, checking AVL
ordering, balance and subtree sizes.

`BenchmarkStorePublication` measures state-changing additions and fixed appends
with 50,000 prepared public items, including affected capture, Changes
construction/encoding/retention and immutable index/scalar installation. Fixture
setup, initial replay, checkpoint serialization and caller output copies are
excluded. `TestPublicationPreparationGrowth` separately counts semantic visits
and pointer/index-position copies at 12,500 and 50,000 public items: low
allocation alone cannot rule out whole-thread preparation. See the
[complete-publication measurements](https://github.com/pyrycode/pyrycode/pull/3426)
for before/after commits, environment and measured allocation/work counts.

Restart fixtures must rebuild the thread store when reopening its history
provider. Changing only the daemon's history field leaves the old `thread.Store`
attached to the former provider, whose cached high-water mark can remain behind
new acceptances and stall recovery. `TestBoundQueueActionsRestart` shuts down
the old store and constructs `thread.NewStore` with the reopened history before
loading item 205. This proves queue-handle reuse against the new run's acceptance
rather than against stale provider state; see
[current-run queue binding](msgqueue-package-api.md#exported-surface).

Certificate proofs count actual replacements through `Store.replace`, synchronized
with publication and completion. `TestStoreCertificateWrites` checks one dirty
write per load, no tail/no-item/active-to-idle replacements and one clean write
per retirement; `TestStoreCertificateDirectorySync` holds dirty directory sync
before usability and tests failure/retry. `TestStoreCheckpointLifecycle` checks
unchanged retirement and preservation of unloaded progress through shutdown;
`TestStoreCheckpointInitialReadiness` holds the dirty replacement before
usability. `TestStoreCheckpointFailures` checks generic snapshot errors and
retained lifecycle persistence failures. `TestStoreCertificateIsolation` requires
healthy conversations to retain their epochs after another final write fails.
`TestStoreCertificateMigration` covers legacy cleanup ordering, unsafe leaves
and preservation of unrelated files.

**Rename failure alone cannot prove failed clean certification.** A successful
rename followed by failed directory sync can leave readable clean evidence.
`TestStoreCertificateFinalSyncFailure` reopens after the injected failure and
requires epoch rotation; checking only `ErrPersistence` would miss unsafe reuse.
`TestChannelDelivery_SessionDaemonWiring` checks `thread-certificate.json`, `Clean`
and consumed version 2 both after startup replay/idle retirement and after joined
shutdown. Package-local tests can stay green while a daemon wait still polls a
removed metadata leaf, so both integration assertions must follow the format.
`TestStoreCheckpointUncleanContinuation` compares reopen and subsequent tailing
with fresh replay across pending child/report cut points, including revisions,
consumed version and `LastShownVersion`; epoch rotation is asserted separately.

`TestCatchUpPublications` spans more than 64 separate publications after live
range eviction, including no-item progress, arbitrary saved versions and an
immediate H read. `TestCatchUpContracts` checks exact/above gap boundaries,
epochs, future versions, missing query evidence and zero-version window parity.
`TestCatchUpEvidence` checks late public appearance despite older revisions,
hidden/dropped membership, detached content and suppression across clean restart
with whole/single-entry replay. `TestCatchUpMissingHistory` distinguishes crossing
a missing ID from starting at or beyond it.

`TestCatchUpBoundedRecovery` counts actual history reads, item/index visits and
allocations over 36,000 committed entries, separately measuring cold recovery
and publication preparation. It repeats queries after clean restart and compatible
unload/reload with a previously returned epoch/version and no retained live
ranges. Its reference includes repeated touches, old active work, shared/multi-level
parents and a late-publication child: revision-only selection would silently
miss that child. `TestCatchUpReadiness` and `TestCatchUpIsolation` check unavailable
recovery/retry and cancellation during actual index work, holding a captured
publication through replacement, retirement and shutdown while writers and
another conversation progress.

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
bookkeeping: `Store.run` publishes before returning from the tail feed callback,
while the wrapper records successful IDs after that callback returns. A snapshot can already
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
missing, corrupt, partial and ahead certificates. `TestStoreCacheInterrupted`,
`TestStoreCacheShutdown` and `TestStoreCacheFinalCertificate` cover incomplete
recovery, independent final failure and unloaded certificates surviving another
conversation's unclean stop.
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
