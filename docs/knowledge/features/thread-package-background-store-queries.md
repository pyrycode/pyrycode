# `internal/thread` — bounded queries

Part of the [background store](thread-package-background-store.md) overview.

## Bounded queries

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
errors use the separate returned error; cancellation discards partial results
and leaves the worker running, including while waiting to withdraw after a read
failure. Queries never load a conversation or wait for recovery. Rendering
failure returns only unavailable State/Err and withdraws the captured current
worker as described under [snapshots and retry](thread-package-background-store-replay.md#snapshots-and-retry).

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
it before rank/range selection and complete public parent closure. Only then does
`renderItems` collect selected rows' creating, text, delivery and enrichment
references, including inherited dependencies, and resolve them through
request-local `history.Store.Lookup`. History I/O runs outside `Store.mu`.
Old roots, lookup buckets and recipes remain immutable: selection, revisions,
epoch/version and rendering use one publication through later feed, replacement
or retirement. Failure, unload and shutdown release worker references without
invalidating readers. Reset and nonusable results resolve no references.

Metadata selection is `O(log N + selected range + active rows + unique parents)`;
rendering adds selected-reference lookup/decoding and returned content work.
Catch-up selects touched rows by version; active extras apply to catch-up and
newest windows. Selection scans no unrelated rows or full baseline. Index
construction is separately `O(N log N)` on initial usable replay. Tail semantic
index work follows affected rows and balanced-tree paths; copying stable slot
pointers and affected lookup-bucket storage can still grow with history and
belongs in publication allocation accounting. Slots retain no converted Content.
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
metadata. Adapters add no loading, history scan, full projection clone or second
selection. Relay owns authorization, mapping, framing and terminal
delivery under the [wire query contract](../../protocol-mobile.md#thread-query-messages).

Measure the installed request after the store becomes usable:
`TestDaemonThreadQueriesBounded` separates committed-history/recovery preparation
from requests over 36,000 entries and bounds selected rows. Its source check
constrains forwarding; `TestCatchUpBoundedRecovery`, `TestStoreQueriesBounded`
and `TestStoreSelectedLookup` constrain selected references and metadata visits.
Allocation ceilings cannot isolate selection from selected-segment decoding;
assert exact allowed reference IDs, including accepted-send terminal outcomes.
These bounds exclude background recovery and publisher baseline preparation.
