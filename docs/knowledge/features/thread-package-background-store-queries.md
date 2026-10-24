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
`Active` and `Shown`. `Rev` remains the source item revision accepted by
`ToolBody` and by `ChildPage` when that row becomes the requested parent.
`Headline` and known `ErrorSummary` are plain valid UTF-8, bounded to 200 Unicode
code points including a truncation ellipsis. `childHeaderText` treats whitespace
and controls as separators, collapses them to single interior spaces, trims
boundaries and repairs invalid UTF-8. Truncation keeps 199 code points plus `…`;
normalization can inspect beyond the former 256-byte input prefix, including
leading separators. Blank headlines use `Tool call` for `tool_call`, `Agent` for
`agent`, and `Thread item` otherwise.

Optional facts distinguish unknown (`nil`) from known empty/zero/false:

| Fact | Captured meaning |
| --- | --- |
| `TaskID` | Exact computed task association for a canonical `agent` or `tool_call` creating row. Nonempty names the selected task, empty means known none at this revision, and nil means unknown. A matching authoritative creating recipe establishes known absence when it has no computed association. |
| `ErrorSummary` | Own `failed`/`denied` status supplies a nonempty diagnostic, currently the status itself. Inactive `done`/`completed`/`finished` supplies known empty text. Other outcomes, including an active row with a success status, remain unknown. Descendant failures do not become ancestor errors. |
| `PublicChildCount`, `HasPublicChildren` | Public direct-child count and existence; known existence is exactly count > 0. A usable public child index establishes known zero/false for a leaf. Hidden and dropped rows count; unresolved private children and claimed standalone delivery rows do not. `NoChild` describes producing-child attribution, independently of inventory. |
| `SubtreeVersion` | Epoch-scoped freshness of the row and its public subtree, independently of source `Rev`. |

Known count and freshness values are nonnegative and below 2^53; values outside
that domain stay unknown. `DetailOmitted` is always true and no `Content`, body
or descendant identities are returned. These are package-owned Store facts
matching the [header semantics](../../protocol-mobile.md#thread-query-messages).
`runThreadChildren` forwards these facts in bounded expansion replies, and
production compact delivery uses them through the real Store installation.
See [daemon compact wiring and evidence](thread-package-shadow-lifecycle-and-evidence-publication.md#background-discovery-and-publication).

`childHeader.taskAssociation` reads only the immutable recipe's computed
`task_id`, with a creating ID matching the row. Retained launch JSON can carry
the same key without establishing ownership: parsing rendered content would
grant that arbitrary value authority and fail on metadata-only restoration.
A missing/mismatched creating recipe or invalid/noncomputed scalar leaves the
fact unknown; a usable creating recipe without the scalar establishes known
empty. `prepareChildHeader` alone has no association authority.

The [scoped agent fold](thread-package-agents-and-background-work.md#identity-and-recovery)
owns validation by conversation, recorded source/session and lifetime or legacy
scope. `applyAgent` selects the greatest lexicographic task ID among a call's
validated associations; headers preserve that exact string without normalization,
truncation or a task inventory. Background flags, headlines and result prose
cannot establish links. On replacement, explicit empty clears a prior association;
nil remains unknown and supplies no authority to carry a prior value forward.
Existing row identity/kind, parent/order and lifecycle metadata provide placement
and navigation. `TaskID` establishes neither detail availability nor coverage;
see the [wire presence rules](../../protocol-mobile.md#bounded-expansion-reads).

Initial `Store.publish` initializes every public row's freshness from consumed
publication progress. Checkpoint restoration seeds public prefix rows without
marking them affected: initializing only affected headers, or retaining their
source revisions as freshness, misses newer progress with no item changes.
`TestChildHeaderInitialProgress` covers both complete-checkpoint and suffix
resumes. Incremental public additions, content/lifecycle/`Shown` changes,
completion, late public appearance, reparenting and withdrawal refresh surviving
affected rows and every affected public ancestor to the new consumed version.
Both old and new ancestor chains are collected independently, so an already
visited reparented row cannot hide its new ancestors. Unrelated branches and
no-item progress retain their freshness. An ancestor's `Rev` can remain unchanged
while `SubtreeVersion` advances; freshness does not alter authoritative items,
storage or complete-query coverage/certification.

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
replacement, allowing an in-flight read to finish against its capture. Header
refreshes copy ancestor lookup buckets as well as directly affected buckets;
optional result values are freshly allocated, so caller mutation cannot alter
the capture or another result. `TestChildHeaderFreshness` checks multi-level
changes, unchanged sibling branches and retained captures, alongside the
independent public membership/count oracle.

`publicationCapture.capture` prepares headline/outcome evidence from the already
rendered affected public rows, including reference-backed outcomes, then enriches
their headers with the matching fold recipe's task association. On cold resume,
`Store.initialPublicationIndex` also reconstructs the association for **every**
restored public prefix row from validated recipes, including rows absent from
the suffix's affected set. Preparing only affected rows would lose associations
after a complete checkpoint load. This step needs no body rendering or history
lookup; full replay and unload/reload use the same authority. `childHeader.apply`
allocates detached string pointers so mutations or subsequent reassociation
cannot alter an earlier capture or another caller's summary. Initial
`O(N log N)` index preparation establishes counts using child-tree ranks.
Incremental header maintenance follows affected rows and old/new ancestor paths,
refreshing counts by rank without rediscovering all public rows or enumerating
descendants. Text normalization and source rendering belong to preparation and
can inspect source text/content; they are outside the request bound. Existing
stable-slot pointer copying and affected lookup-bucket copying are accounted for
separately from semantic metadata visits and can still grow with history.

Each request uses `O(log N + limit)` metadata visits and copies only prepared
bounded headers, with zero body rendering or history-content lookups and no
descendant enumeration. Its work is independent of unrelated rows, growing
unopened/hidden descendants and body/summary sizes.
Complete queries additionally select ties/extras/closure and render selected
references/content as described above; their work and coverage bounds differ.
Recovery and total publication allocations remain outside the child-read bound.
`TestChildPageBoundedWork` counts metadata visits and zero history lookups as
unrelated rows, descendants and payloads grow; `TestChildPagePreparationWork`
separately checks affected-row/ancestor index maintenance.
`TestChildHeaderFacts` checks normalization, optional facts and detached values;
`TestChildHeaderReferenceOutcomes` proves reference-backed errors without read-time
hydration, and `TestChildHeaderUnknownFreshness` checks the numeric boundary.
`TestSummaryTaskLifecycle` compares late links, deterministic selection,
reassociation and terminal metadata with complete folded rows, including retained
captures and compact change/live inputs. `TestSummaryTaskScope` rejects unusable
or foreign evidence and isolates reused identities. `TestSummaryTaskResume`
checks full replay, both checkpoint cuts and unload/reload with root/child
association and lifecycle assertions while trapping closed-history reads.
`TestSummaryTaskFacts` checks unknown authority and detached optional values;
`TestSummaryTaskShell` rejects forged launch/result association evidence.
See the
[Store verification guidance](thread-package-background-store-verification.md#store-verification)
for independent membership oracles and capture barriers.

### Compact root windows and pages

`Store.CompactNewestWindow(ctx, conversationID, limit) (CompactQueryResult, error)`
and `Store.CompactHistoryPage(ctx, conversationID, upperOrder, limit)
(CompactQueryResult, error)` read public roots (`Parent == 0`) only, mirroring
`NewestWindow`/`HistoryPage`'s range and tie rules but never selecting or
rendering descendants or tool/agent bodies. Callers must already be authorized;
these reads add no loading or recovery wait, validate canonical IDs
(`history.ErrInvalidID`) and positive limits (`ErrInvalidLimit`), and clamp to
`MaxQueryItems`. `CompactQueryResult` carries `State`, `ConversationID`,
`Projection` (always `"compact-v1"`), `Epoch`, consumed `Version`, `Items`,
`Headers`, `LowerOrder`/`UpperOrder`/`Continuation`, `OlderExists` and `Err`;
only `StateUsable` carries identity, rows and bounds.

Membership matches the other bounded queries' `Fold.Items` rule: hidden
(`Shown == false`) and dropped roots remain public; unresolved private
children and claimed standalone delivery rows are absent. A root whose own
`Kind` is `tool_call` or `agent` never returns its `Item`; it returns a
detached `ChildSummary` instead, built the same way as `ChildPage`'s headers
(bounded `Headline`/known-empty `ErrorSummary`, `DetailOmitted: true`, no
`Content`, body or descendant identities, plus exact optional `TaskID` from the
same prepared recipe evidence). Initial/older roots and compact catch-up/live
headers preserve this fact through `threadSummaryFrom` and `daemonThreadSummary`;
bounded child replies use the former too. Every other root returns its full,
unchanged `Item`. Ordered selection, bounds and ties use the same algorithm as
`NewestWindow`/`HistoryPage`, but count only roots with nonzero `Order`:
`CompactNewestWindow`'s exclusive upper bound is the captured `Version + 1`;
`CompactHistoryPage` uses the supplied exclusive upper order. Every ordered
root in `[LowerOrder, UpperOrder)` is returned, including all lower-bound
ties; `Continuation == LowerOrder`, and `OlderExists` is true exactly when an
ordered root exists strictly below it. An empty ordered range still carries
the requested `UpperOrder` with zero `LowerOrder`/`Continuation` and false
`OlderExists`.

Only `CompactNewestWindow` adds extras, and only root extras: every public
root that is itself active or has an active public descendant at any depth,
including order-zero roots excluded from the ordered range above. A root's
own `Active` value is returned unchanged regardless of why it qualified.
`CompactHistoryPage` never adds extras. Extras are deduplicated against the
ordered selection by ID and never change the limit, bounds or continuation.
An ordered range that selects nothing can still return extras, so a newest
window is never empty solely because no root has a nonzero order.

Root activity is indexed, not walked per request. `queryIndex` keeps `roots`
(ordered, nonzero-order public roots), `activeRoots` (public roots whose
subtree has activity) and `activeBranches` (a parent-grouped tree used to rank
a subtree's active-member count without enumerating it). `compactMembership`
maintains these alongside the existing `ordered`/`active`/`children` trees.
`refreshRootActivity` runs after each publish: it visits only rows named in
the affected set, walks each one's ancestor chain once, and recomputes
`activeBranch` bottom-up from `groupRank` differences over `activeBranches` —
a parent is revisited only after all of its pending affected children have
been resolved, so no unaffected branch and no affected branch's unaffected
descendants are rescanned. `TestCompactPreparationWork` checks this stays
roughly proportional to the affected set, not total inventory: incremental
semantic visits grew less than 3x for a 64x larger, equally-affected store.
`TestCompactIncremental` covers activation, deactivation, reparenting (both
directions) and suppression/un-suppression each refreshing exactly the
affected root's freshness while an unrelated root's stays unchanged, against
an independent `Fold`-rebuilt oracle.

Request-time work does not grow with hidden inventory either: selecting a
root extra reads indexed membership, not its descendants, so a root with
thousands of inactive/hidden descendants costs the same as one with none.
`TestCompactBoundedWork` holds request visits near-constant (six, in one
run) from 124 to 8,188 active hidden descendants and asserts exactly one
history lookup — for the one non-`tool_call`/`agent` root actually selected.
A root's own large body is never a reason to hydrate it: an `agent` root with
a multi-kilobyte secret-bearing body is returned only as a header in the same
run, so growing or sensitive tool/agent content never reaches `renderItems`.

Lifecycle, capture and cancellation behavior are identical to the other
bounded queries: an already-cancelled context returns its error before ID or
limit validation; nonusable captures return their lifecycle state with no
rows or bounds; a required rendering failure collapses to generic
`StateUnavailable`/`ErrUnavailable` with no partial headers or items;
cancellation during selection or rendering discards the whole result without
retiring a healthy worker. `TestCompactCapture` confirms an in-flight compact
read keeps its captured publication's exact result across a newer publish,
an unload/reload replacement and a concurrent unrelated-conversation read,
and that an old failed reader cannot retire the replacement worker. Returned
`Items`/`Headers` are freshly built per call: mutating a caller's copy (as
`TestCompactSelection` and `TestCompactCapture` both do) cannot reach the
capture or another caller's result.

A compact window or page certifies only its own root range for one
publication — never complete-thread coverage, and never that a tool/agent
root's descendants or body were considered, let alone covered.

### Compact catch-up and live progress

`Store.CompactCatchUp(ctx, conversationID, epoch, afterVersion, limit)
(CompactQueryResult, error)` and `Store.CompactChanges(ctx, conversationID,
epoch, after) (CompactQueryResult, error)` extend compact-v1 with reconnect
catch-up and waiting live progress, implementing the
[compact-v1 interval contract](../../specs/architecture/3559-compact-thread-contract.md#design).
Both identify one immutable publication by `ConversationID`, `Projection`
(`"compact-v1"`), `Epoch` and consumed `Version`, and report the source
interval as exclusive `FromVersion` through inclusive `Version` on
`CompactQueryResult`. Neither selects or renders descendants, tool/agent
bodies or per-hidden-child updates. Neither widens `CatchUp`/`Changes`, the
complete-view reads. Each projection checks its own membership/removal evidence;
both reject missing-history coverage through `unprovenThrough`. Complete-view
continuity alone cannot authorize a compact interval.

`CompactCatchUp` at `afterVersion == 0` delegates to `CompactNewestWindow`
for any epoch, including a mismatched one, so version-zero recovery always
succeeds with the same validation and `MaxQueryItems` clamp; `FromVersion`
and `ResetRequired` stay zero/false, as they do for `CompactHistoryPage`. A
matching nonzero call ignores `limit` (even nonpositive), is bounded by the
same inclusive `MaxCatchUpGap = 1024` history-ID distance as `CatchUp`, and
returns every root whose compact state changed in `(afterVersion, Version]`
plus the indexed `activeRoots` extras, deduplicated. `CompactChanges` waits
while the captured snapshot is usable, the epoch matches and `after` equals
the current `Version` — the same locking and wake-up pattern as `Changes`,
capturing `changed` under `Store.mu` alongside the version check — then
returns the same interval without activity extras. A lifecycle change while
waiting returns that nonusable `{State, Err}`; retirement returns the zero
result; cancellation returns the zero result and the context error without
retiring a healthy worker.

Roots are selected from `queryIndex.rootTouches`, a persistent tree keying
every public root (`Parent == 0`) by `(header.subtreeVersion, ID)`.
`queryIndex.advance` already marks every affected row and its old and new
ancestor chains dirty and refreshes each one's `subtreeVersion` to the
publication's version (see
[revision-scoped direct-child summaries](#revision-scoped-direct-child-summaries)),
so a root's key is exactly the version its compact state — its own fields or
any descendant's header count, activity or freshness — last changed. A
nested burst under a stable root `Rev` moves the root's key without changing
`Rev`. Each root occupies one key, so a range read over `rootTouches` yields
each affected root exactly once with its final state, coalescing any number
of intervening updates. A source advance that touches no root, such as
hidden-only or otherwise nonvisual activity, still returns `StateUsable` with
the new `FromVersion`/`Version` and no items or headers: explicit consumed
progress, not an empty or failed read.

Compact root evidence is projection-scoped, kept on `queryIndex` as two scalars
copied by value through `advance`. `compactFrom` is the version at which
this index was constructed. Ordinary initialization uses the first publication's
version; a clean idle reload with validated matching retirement evidence
reconstructs the index at the saved boundary before advancing the suffix.
Touches before that boundary remain unknown, so an interval starting earlier
resets even when the epoch matches. `compactLost` is the latest
version at which a row left the root set — withdrawn, suppressed, or
reparented under another row; `membership` sets it whenever a row that was a
root stops being one. Clients have no root-removal operation, so an interval
spanning `compactLost` resets; nested suppression or reparenting below a
root is instead repaired by that root's own dirty-ancestor header refresh.
Shared `compactInterval` produces either the interval or a scoped reset —
`State`, `ConversationID`, `Projection`, `Epoch`, `Version` and
`ResetRequired: true`, with no rows, `FromVersion` or order bounds — when:
the epoch does not match; `after > Version`; `Version - after >
MaxCatchUpGap`; the query index is nil; `q.coveredThrough != Version`;
`after < q.compactFrom`; `q.compactLost > after`; or
`q.unprovenThrough > after` (missing history IDs). Recovery is always a
version-zero compact window. Complete-thread recovery (`CatchUp`/`Changes`)
is independent: it can stay usable while a compact reader resets, and the
reverse. A validated idle reload can cover its saved boundary and contiguous,
representable suffix without resetting or sending a full inventory. It restores
no older compact interval: epoch equality alone never substitutes for coverage,
matching [catch-up continuity](thread-package-cache-and-epochs.md#catch-up-continuity).

Request work does not grow with hidden inventory: selection and rendering
are bounded by affected roots and indexed extras only, with zero tool-body
lookups and no descendant enumeration. `TestCompactIntervalWork` holds
request visits near-constant (11, in one run) from 125 to 8,189 hidden
descendants. That bound depends on `refreshHeader` removing the *stored* row
rather than the row it was passed: `publish`'s first-index path applies a
row's replacement header before calling `refreshHeader`, so removal keyed by
the new header's `subtreeVersion` previously missed the `rootTouches` entry
for the old header and left one stale, unselected entry per row. The stale
keys sat at or below `compactFrom` and were never selected by any read, so
every correctness check passed regardless; only the work counter caught it,
growing from 35 to 59 visits across the same descendant range before the
fix. `TestCompactInterval` checks nested bursts, repeated activation, and
reparenting/suppression in both directions against an independently tracked
oracle of each root's last-changed version; `TestCompactCatchUpReload`
keeps the reset for an older interval lost at same-epoch reload, while
`TestStoreIdleReloadContinuity` and `TestStoreIdleReloadNestedCompact` check
saved-boundary suffix coverage and ancestor refresh. `TestCompactChangesWait`
checks a blocked reader released by publication, cancellation, unload and concurrent captures,
alongside unaffected concurrent complete `CatchUp`/`Changes` reads.

Relay/daemon delivery is installed: `daemonThreadPublisher.publish` reads
`CompactChanges` only after observed version advancement, under its cancellable
operation context, on the existing 50 ms publication schedule. Calling it at
the current version would wait instead of flushing. See
[daemon coalescing and recovery](thread-package-shadow-lifecycle-and-evidence-coordination.md#idle-retirement-query-demand-and-evidence).
The Store supplies interval facts and evidence only; the installed relay owns
authenticated delivery. Neither establishes a full inventory baseline or
expansion completion.

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
[body handler](v2-session-manager-state-machine-capability-negotiation-on-the-handshake-thread-delivery-and-summary-projection.md#thread-delivery-and-summary-projection)
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
