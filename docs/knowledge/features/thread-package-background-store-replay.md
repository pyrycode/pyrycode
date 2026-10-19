# `internal/thread` — replay and publications

Part of the [background store](thread-package-background-store.md) overview.

## Loading and replay readiness

`NewStore(h *history.Store, beforeFold ...func(context.Context, conversations.ConversationID) error) *Store`
creates a background cache owner. Ordinary production callers supply only `h`.
The first optional callback runs before each actual replay/tail chunk; it must
return promptly or honor cancellation, and failure withdraws the view. This
lets daemon tests pause real fold work rather than merely delaying `Load`
admission, and lets removal cancel and join that paused work. History remains
the only durable source, and store processing never writes history bytes. See
[cache and epochs](thread-package-cache-and-epochs.md#cache-and-epochs) for on-disk recovery records.
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
`LatestEntryID` can remain ahead after history truncation. `Store.resumeCheckpoint`
imports the complete saved graph without history I/O, then checks V <= H and,
for nonzero V, the stored-entry checksum at exactly V. Missing/changed V rejects
the checkpoint even if rendered items remain unchanged.

Valid clean checkpoint state retains its epoch; valid dirty state restores its
fold with a new epoch. Missing/corrupt/inconsistent state, wrong owner, changed
rules, incompatible file/fold schemas (including newer ones), V beyond H or
missing/changed V causes full refold with a new epoch. Scalar-only installations
migrate by refolding once; a scalar certificate cannot authorize restoration or
override dirty/rejected state. Actual history/I/O/containment failures withhold
usability rather than permitting fallback.

Before first publication, the worker durably writes `thread-certificate.json`
with `Clean: false` and dirties any existing readable checkpoint state without
advancing its saved fold boundary. Epoch selection uses the original clean flag.
Absent state needs no eager checkpoint. Replacement or directory-sync failure
on either surface prevents usability. After validation and dirty marking,
`reconcileResume` truncates only through a contained, no-follow/nonblocking
regular-file descriptor to the selected generation's committed length, checking
Truncate, Sync and Close. Complete/torn uncommitted tails are ignored then removed.
Cleanup rechecks the surviving state's named generation and removes only other
exact package generation leaves admitted as contained regular files, syncing
the directory after removals. Unsafe leaves/directories fail closed; history,
unrelated files, named generation and redirected targets survive. Full-refold
fallback preserves the old named generation until fresh replacement state commits.
Legacy cleanup remains separately allow-listed.

The worker seeds publication slots, recipes, private/public activity and complete
row-byte/writer acknowledgements from the restored inventory, including hidden
or recipe-only rows. This initializes query metadata even with no suffix and
avoids rendering every restored row just to classify initial changes. Production
`history.Store.Forward(conversationID, V)` positions after the saved boundary;
only `(V, H]` enters `Fold.Feed` through bounded `ForwardReader.Walk` chunks of
at most `history.MaxPageEntries`. Full-refold fallback starts at zero. Wrapped
fixture readers retain `Store.forward`'s signature and filter only entries at
or below the immutable saved boundary; later duplicate IDs still fail.

Readiness requires durable dirty marking, safe reconciliation/cleanup, successful
suffix/full replay and `Fold.Version() == H`; partial recovery publishes no
usable view. Recovery stays `StateRebuilding` until these conditions hold.
A missing or empty log becomes usable with no items and both `Version` and
`LastShownVersion` zero. The initial page read remains required at H zero:
`ForwardReader.Walk(0)` performs no storage read and cannot prove readability.
Initial results, private continuation and later tail/retirement match fresh full
replay. See the [validated load order and read accounting](thread-package-cache-and-epochs.md#store-replay-and-public-touches).

The same reader then runs `ForwardReader.Tail`. Registration precedes catch-up,
so entries committed during replay or the handoff are consumed exactly once in
ID order; later commits converge through buffered notifications. Successful tail
chunks publish updated items, consumed version, last-shown watermark and epoch
together without scalar-certificate writes. After initial replay the worker
arms a 30-second checkpoint timer, serialized with completed feed/capture steps.
Deadlines persist changed consumed versions, including version-only changes
and changes followed by silence; publications cannot postpone them. Unchanged
periodic progress causes no write. A failed periodic checkpoint retains pending
latest internal records across publications, emits one fixed content-free
warning and retries at the next interval while usability and publication
continue. Only item-record appends are incremental; state-file replacements
include the complete manifest/private maps. Active-to-idle transitions do not
flush. Checksum and watermark evidence advance in memory only with successfully
folded and captured progress; a partially failed or interrupted feed cannot
advance retirement evidence. Dirty-write failure leaves
`StateUnavailable` with generic `ErrUnavailable`; after repair, `Retry`
revalidates checkpoint/history and writes dirty again. Successful joined retirement commits the final
eligible boundary and certifies both checkpoint state and scalar certificate
clean, including unchanged and empty version-zero progress. See
[cache and epochs](thread-package-cache-and-epochs.md#cache-and-epochs)
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

`Store.Observe(conversationID)` renders a detached complete baseline from one
immutable publication; resolving its recipes can read history outside `Store.mu`.
Failure returns unavailable state without partial items; repair requires `Retry`.
Once usable, pass its `Epoch` and `Version` to
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

For a combined field/text change, `Fields["Content"]`, if present, contains the
replacement's non-text facts with the previous text; it can also be absent.
Sending that replacement alone loses the suffix. `Change.ResultContent` carries
the complete final rendered content for consumers that need an atomic content
replacement; appending `TextAppend` to it again would duplicate the suffix.
`itemDifference` supplies it only for combined changes. Suffix-only changes keep
just the detached suffix: retaining full content per delta would pin accumulated
messages and defeat bounded retention. Public fold results and `copyObservation`
detach result bytes; internal shared publications remain immutable.
`TestObservationResultContent` checks both replacement shapes, the existing
Fields-then-TextAppend contract and byte ownership;
`TestStoreObservationRetainedSuffixMemory` protects suffix retention.

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
combined replacements/text become one atomic changed message with complete
`ResultContent`, while suffix-only changes become one append. Both use the actual
previous/resulting item revisions with strict advancement. See
[daemon publication](thread-package-shadow-lifecycle-and-evidence.md#shadow-lifecycle-and-evidence).

**A fresh baseline cannot repair an existing consumer silently.** Unload/reload
and retry release retained live batches even when clean checkpoint validation
preserves the epoch. Nonusable state, epoch mismatch, evicted ranges or linked-delivery
suppression require `RetireLiveState` for affected thread consumers before the
publisher forgets its cursor and obtains a fresh usable `Observe`. Pending
dependent deltas are quarantined immediately, rather than drained while gap
teardown waits on relay Run. No `base_rev` is guessed and no partial recovery
is published. `Store.CatchUp` proves a separate reconstructed query range;
the [installed daemon providers](thread-package-shadow-lifecycle-and-evidence.md#shadow-lifecycle-and-evidence)
serve catch-up/pages from this same store. Reconnect initialization requires
explicit requests for each open conversation.

## Snapshots and retry

Production recovery validates the complete saved graph, certificate/fold
watermark agreement and stored-entry checksum at V instead of comparing rendered
items or refolding the prefix to reconstruct its watermark.
Publication capture uses `renderShared` on affected public rows to resolve the
[standalone](thread-package-main-thread-folding-sources.md#standalone-source-entries),
[assistant/ordinary-tool](thread-package-main-thread-folding-lifecycle.md#main-work-source-entries),
[accepted-send](thread-package-main-thread-folding-sources.md#accepted-sends-and-durable-outcomes)
and [agent/shell lifecycle recipes](thread-package-agents-and-background-work.md#agent-lifecycle)
after excluding suppressed delivery rows and children without public parents.
Before/after rendering is transient: comparison slots, current publications and
query rows retain metadata and immutable recipes, with no converted `Content`
or derived user/assistant `Summary`. Unchanged item pointers are reused; affected
rows get independent allocations so one row cannot pin an obsolete item array.
Captured recipes remain stable across later feeds, including equal-content
companion replacements that change provenance without public revisions/touches.
All production item kinds retain references; legacy `New` remains inline.
Store publication/query recipes and the detached change ring are separate from
the [fold/source heap measurement](thread-package-main-thread-folding-validation-replay.md#replay-rendering-and-ingestion-validation);
that ratio excludes publications. See [cache and epochs](thread-package-cache-and-epochs.md#cache-and-epochs).
Private or withdrawn slots are nil; `detachPublication` skips them and preserves
nil `Items` when no public row survives. Slot count is not public item count.

Public `Fold.Render`, `Fold.Items`, `Fold.Observe`, `Fold.FeedChanges` and all
Store item/change/query results still return complete detached content with
their existing signatures. `Snapshot` and `Observe` capture metadata, slots and
recipes under `Store.mu`, then render all public rows through `renderShared`
using request-local history lookup outside the mutex. They never read the live
fold or mutable recent cache. Returned content is detached from other results
and publications. Load/retirement certificates contain only scalar evidence;
they cannot supply a cache-only view or bypass reference validation.
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

`Snapshot(conversationID) Snapshot` can read referenced history. Its fields are
`Active`, `State`, `Items`, `Version`, `LastShownVersion`, `Epoch` and `Err`.

`Store.Status(conversationID) Snapshot` captures the same metadata and readiness
from one publication under `Store.mu`, with nil `Items` in every state. It does
no history/cache I/O, loading, item traversal or copying and allocates zero bytes.
Use it for progress/readiness; `Snapshot` and `Observe` render complete baselines.

Both reads retain these readiness rules; the items described below are returned
by `Snapshot`:

| State | Meaning |
| --- | --- |
| `StateNotLoaded` | No loaded view, including while retiring or after shutdown. |
| `StateRebuilding` | Validation, durable dirty marking, safe cleanup or suffix/full replay is pending; no usable publication exists. |
| `StateUsable` | Detached items/content, consumed version, last-shown watermark and conversation epoch from one consistent publication; newer appends may still be pending. |
| `StateUnavailable` | The view is withdrawn; its producer may still be exiting. `Err` is the fixed `ErrUnavailable` sentinel; explicit retry can recover. |

Only `StateUsable` carries versions/epoch, including an empty usable conversation;
only `StateUnavailable` carries `ErrUnavailable`, and every other state has nil `Err`.
`Status.Active` preserves private activity even with no public items, so nil
`Items` cannot establish inactivity or readiness. The last-shown watermark follows
the [restored or history-reconstructed rule](thread-package-cache-and-epochs.md#cache-and-epochs), and unread
is `LastShownVersion > ReadUpTo`. The random epoch is independent of
deterministic item equality and follows the [recovery compatibility rules](thread-package-cache-and-epochs.md#cache-and-epochs).
An active-index set tracks affected private/public rows without a whole-fold
scan. `TestStorePrivateActiveWork` checks an invisible active child and its later
closure; an empty public list cannot establish unload eligibility. Retirement also
requires [producer quiescence](thread-package-shadow-lifecycle-and-evidence.md#shadow-lifecycle-and-evidence).

A missing/unreadable reference during baseline, query or live-change rendering
discards the whole output, even after successful partial lookup. Withdrawal
clears publication/query/change state, cancels the producer and notifies waiters.
Only `StateUnavailable` with generic `ErrUnavailable` remains: no items, version,
watermark or epoch. `Snapshot` preserves this State/Err failure channel.
`withdraw` checks the captured worker's identity under `Store.mu`; an old read
cannot invalidate a Retry replacement. Context-taking requests discard cancelled
output without withdrawing the worker. Recheck cancellation under the mutex:
a check before lock acquisition becomes stale while waiting. A history read,
reference-rendering or load-time cache I/O failure, unsafe cache leaf, failed
fold, incomplete replay through H or worker-context cancellation withdraws any
usable publication and leaves the conversation unavailable. `Snapshot.Err`
remains `ErrUnavailable`; `Unload` reports
`ErrPersistence` for dirty marking, final checkpoint/certificate or legacy-cleanup
I/O failure and `ErrUnavailable` for failed or incomplete recovery. Periodic
checkpoint failures preserve usability and retry on the next interval. Raw errors
are neither exposed, retained nor logged:
diagnostics contain no conversation content, raw payloads or host paths.

`Retry(ctx, conversationID) error` replaces only an unavailable worker under the
new context after repair. It creates a new reader/recent source and revalidates
persisted checkpoint/history through the same Load path, importing a fresh fold
or refolding rather than reusing partial in-memory progress. It durably dirties
both evidence surfaces again before usability; only validated checkpoint state
can seed continuation. An absent conversation returns
`ErrNotLoaded`; rebuilding or usable workers return `ErrNotUnavailable`.
**Recreate both reader and fold after failure:** a failed `Fold.Feed` can consume
entries without advancing the reader; redelivery would repeat IDs. Completion
records unavailable state and closes `done` under the same lock. Request-triggered
withdrawal can precede producer exit: `Store.start` joins outside `Store.mu`, then
rechecks the current worker's identity, unavailable state and completion together
before replacement. A concurrent Retry can make an earlier join stale; repeat
admission checks and join the actual current worker. Render failure also blocks
clean certification if Unload races the producer's cancellation.
