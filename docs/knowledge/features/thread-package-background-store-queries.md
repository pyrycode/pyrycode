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
lookup and ordered/active/direct-parent membership with `newPublicationIndex`,
then adds latest-public-touch and coverage/suppression evidence with `prepareCatchUp`.
`newQueryIndex` adapts value slices for test callers and fresh-build oracles.
After that, `queryIndex.advance` consumes the affected stable slots preserved by
`publicationCapture.capture`; ordinary tail publication neither rebuilds these
indexes, scans unrelated rows to discover changes nor sorts the whole index.
Permanent-ID lookup uses 256 hash-map buckets, cloning each affected bucket
once. Ordered, active, latest-public-touch and direct-parent membership use
immutable AVL trees with copied update/rotation paths and subtree sizes for
bounded selection.
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

`HistoryPage`/`NewestWindow`/`CatchUp` metadata selection is
`O(log N + selected range + active rows + unique parents)`;
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

### Revision-scoped direct-child summaries

`Store.ChildPage(ctx, conversationID, epoch, parent, revision, limit, continuation)
(ChildPageResult, error)` reads only public rows whose `Parent` equals the
requested parent. Pass nil continuation for the first page. Callers must already
be authorized for the canonical conversation ID; the Store adds no loading,
recovery wait or relay/daemon expansion wiring. The expected epoch must be
nonempty and the parent ID and expected parent revision nonzero. Any matching
public parent is eligible, regardless of kind.

Membership matches `Fold.Items`: hidden (`Shown == false`) and dropped rows
remain public; unresolved private children and claimed standalone delivery rows
are absent. Rows arrive ascending by `(Order, ID)`, including order-zero rows.
Positive limits clamp to `MaxQueryItems = 256` and impose a hard row ceiling,
even across equal orders. There is no parent closure, active extra, deeper
descendant or descendant inventory in the result.

Each detached `ChildSummary` preserves `ID`, `Order`, `Rev`, `EndedOrder`,
`Kind`, `Subtype`, `Session`, `Agent`, `NoChild`, `Turn`, `Parent`, `Status`,
`Active` and `Shown`. `Headline` is plain valid UTF-8, at most 256 output bytes.
`childHeadline` normalizes only the first 256 input bytes of captured
`Item.Summary`: whitespace collapses, controls disappear and invalid UTF-8 is
repaired before truncation at a rune boundary. If that bounded prefix normalizes
to empty, it uses a similarly bounded kind label with underscores replaced by
spaces, then `item` if necessary. It never searches the suffix for text.
Reference-backed items can lack a captured summary even when complete rendering
would produce one; use the fallback rather than hydrate their body.
`DetailOmitted` is always true and no `Content` is returned, distinguishing
omitted detail from an available empty body. Selection and headline generation
perform zero history-reference lookups.

`ChildPageResult` carries `State`, `ConversationID`, `Epoch`, consumed `Version`,
`Parent`, `ParentRev`, `Items`, `More`, `Complete` and `Err`. A successful usable
page identifies one captured publication and matching parent revision.
`More` authoritatively reports whether further direct children follow the last
selected tuple; `Complete` is its inverse for that capture. A matching public
parent with no children succeeds with no rows, `More: false` and
`Complete: true`. Neither an empty result nor nil separate error alone establishes
completion: require `StateUsable` and `Complete`.

`page.CursorAfter(n)` constructs a `ChildCursor` after any returned nonempty
prefix (`1 <= n <= len(page.Items)`); other lengths or nonusable results return
`ErrInvalidChildCursor`. The cursor binds `ConversationID`, `Epoch`, `Version`,
`Parent`, `ParentRev` and the last delivered `Order`/`ID`. Pass its address to
resume strictly after that tuple. A later byte packer must use the number of
actually delivered rows, so selected but undelivered rows remain eligible.
Page completion describes the whole selected page; it does not certify delivery
of rows omitted by packing.

Every request captures the then-current publication and validates scope anew.
A continuation must match the conversation, epoch and parent/revision arguments,
and its nonzero ID must name a captured public direct child with the exact order.
Its version must equal the current captured consumed version. Even an advance
unrelated to this parent invalidates continuation; a fresh request without a
cursor needs only the matching epoch and parent revision. Cursors establish
consistency, not authorization or a wire encoding. These pages certify direct
children within one capture; they supply neither the complete queries' parent
closure nor `CatchUp`'s consumed-version change-range coverage.

An already cancelled context returns its context error first. Otherwise
canonical-ID, positive-limit and scope validation precede capture. Nonusable
captures return their lifecycle state before epoch/parent/cursor checks; usable
captures check epoch, public parent and revision before validating continuation.
Refusals return a zero `ChildPageResult` and a separate Go error:

| Condition | Separate error |
| --- | --- |
| Noncanonical conversation ID | `history.ErrInvalidID` |
| Zero or negative limit | `ErrInvalidLimit` |
| Empty expected epoch, zero parent ID or zero expected revision | `ErrInvalidChildScope` |
| Missing or private parent in that conversation | `ErrChildParentNotFound` |
| Stale expected epoch/parent revision or continuation version | `ErrStaleChildPage` |
| Mismatched cursor scope, zero cursor ID or invalid child/order position | `ErrInvalidChildCursor` |

Rebuilding returns `StateRebuilding`; unloaded, retiring or closed stores return
`StateNotLoaded`. Unavailable captures or a usable capture missing its query
index return `StateUnavailable` with `ErrUnavailable` in the result's `Err`.
These lifecycle results have a nil separate error and carry no rows, capture
identity, `More` or `Complete`. Cancellation during selection discards partial
rows and completion, returning a zero result and context error without retiring
a healthy worker or cancelling another reader.

`captureQuery` captures snapshot scalars and immutable roots together under
`Store.mu`; parent lookup and selection then run outside the mutex against that
capture, never the mutable worker Fold. `queryIndex.children` orders membership
by `(Parent, Order, ID)`. `childRank` and subtree sizes bound the parent range
and prove completion without enumerating descendants. `queryIndex.membership`
removes/reinserts affected public rows through late resolution, withdrawal,
order changes and reparenting; `queryIndex.advance` needs no publication-wide
membership rediscovery. Old roots survive feed, newer publication, unload and
replacement, allowing an in-flight read to finish against its capture.

After initial `O(N log N)` index preparation, each request uses
`O(log N + limit)` metadata visits and bounded prefix work per returned headline,
independent of unrelated rows, unopened descendants and body/summary sizes.
Complete queries additionally select ties/extras/closure and render selected
references/content as described above; their work and coverage bounds differ.
Recovery and total publication allocations remain outside the child-read bound.
`TestChildPageBoundedWork` counts metadata visits and zero history lookups as
unrelated rows, descendants and payloads grow; `TestChildPagePreparationWork`
separately checks affected-row index maintenance. See the
[Store verification guidance](thread-package-background-store-verification.md#store-verification)
for independent membership oracles and capture barriers.

### Revision-scoped tool bodies

`Store.ToolBody(ctx, conversationID, expectedEpoch, itemID, expectedRev)
(ToolBodyResult, error)` reads one public `tool_call` or `agent` body from the
named conversation. Public membership is the same `Fold.Items` membership used
by the other bounded queries: hidden and dropped rows remain public, while
unresolved private children and claimed standalone delivery rows are absent.
Public rows of other kinds still fail the body-kind check. Callers must already
be authorized; this API adds no peer authorization, conversation loading or
recovery wait. Relay/daemon expansion wiring is separate from this Store API.

`ToolBodyResult` carries `State`, `ID`, `Rev`, `Kind`, `Content`, `Epoch`, consumed
`Version` and `Err`. Only `StateUsable` carries identity and content. Its detached
`Content` is identical to the existing complete renderer's content for that row,
including input, output and unknown fields. Available empty input/output is
successful content, distinguishable from refusal by the usable state. No parent,
descendant, active-extra or unrelated row is returned. The expected epoch and item
revision must match the captured publication; an unrelated consumed-version
advance alone does not invalidate a matching body. `Version` reports the captured
consumed progress and is not an expected-version condition.

An already cancelled context returns its context error first. Otherwise canonical
conversation-ID validation and the nonempty epoch/nonzero item/revision checks
precede capture. Nonusable captures return their lifecycle state immediately;
usable captures check epoch, public membership, revision and kind in that order.
Validation/refusal errors return a zero `ToolBodyResult` and a separate Go error:

| Condition | Separate error |
| --- | --- |
| Noncanonical conversation ID | `history.ErrInvalidID` |
| Empty expected epoch, zero item ID or zero expected revision | `ErrInvalidBodyScope` |
| Mismatched epoch or revision | `ErrStaleBody` |
| Missing or nonpublic ID in that conversation | `ErrBodyNotFound` |
| Matching public row is neither `tool_call` nor `agent` | `ErrBodyKind` |

Rebuilding returns `StateRebuilding`; unloaded, retiring or closed stores return
`StateNotLoaded`. Unavailable captures or missing, unreadable or malformed
required content return `StateUnavailable` with only `ErrUnavailable` in the
result's `Err` and a nil separate error. Nonusable results carry no body,
ID/revision, kind, epoch or version, and expose no underlying source error.
Cancellation during rendering returns a zero result and the context error,
discarding partial work without retiring a healthy worker or cancelling
independent readers.

`captureQuery` captures the snapshot scalars and immutable index together under
`Store.mu`. `queryIndex.lookup` selects only the requested public ID;
`renderItems` and `renderSharedLookup` use recipes from that captured root, never
the mutating worker Fold. History lookup/rendering runs outside the mutex.
Feed, newer publications, unload or replacement cannot mix identities or recipes
into that read. `Kind` is the selected rendered row's `tool_call` or `agent` kind
from this same capture, alongside ID/revision/content/epoch/version. Inferring it
from content or rereading a newer publication could mislabel a valid captured
body; `TestStoreToolBodyParity` and `TestStoreToolBodyCapture` assert the kind
with the other capture fields. The relay's
[body handler](v2-session-manager-state-machine-capability-negotiation-on-the-handshake.md#thread-delivery-and-summary-projection)
passes it directly to the encoder. An already captured read can finish after
unload/replacement if its references remain available. A render failure uses
`Store.withdraw`'s worker identity and cancellation guards: an old failed reader
cannot retire a replacement worker.

Only the selected body's creating/enrichment references and inherited recipe
dependencies are resolved. A required ancestor can be resolved and rendered
internally without returning its row; descendants and unrelated rows are neither
selected nor rendered. Metadata selection and reference discovery do not grow
with unrelated/descendant inventory or body sizes. Rendering can scale with the
requested body and required dependencies. `TestStoreToolBodyBounded` counts exact
history-reference IDs and metadata visits after preparation: two visits for its
root body and three for its inherited body as unrelated/descendant inventory and
payload sizes grow. These request bounds exclude recovery and publication work;
see the [Store verification guidance](thread-package-background-store-verification.md#store-verification)
for parity and inherited-dependency fixture traps.
