# `internal/thread` — continuation retention

[Validation overview](thread-package-main-thread-folding-validation.md#validation-and-testing).

## Continuation retention and accounting

Production reference folds keep `mainTurn` as a bounded cursor for its scoped
turn generation: recorded source/key, original opening, first ending and current
text-coalescing position. `compactMainCall` replaces created, terminal ordinary
calls with `mainCallMarker` records holding an item index, scope, lifetime and
first outcome reference; an item-index sentinel represents an itemless launcher.
The source comes from the owning turn. The finished index holds pointers to these
small records, never the former mutable `mainCall`; empty mutable call maps are
released. Marker storage contains no rendered content. Legacy `New` keeps its
mutable representation as the differential oracle.

**Duplicate suppression still needs continuation identity.** A finished call
can gain child-parent evidence later. `takeMainCall` reconstructs only that
addressed marker and transfers its original item/group/outcome to the child lane;
discarding those facts would lose the intrinsic first ending. Uncreated calls
keep pending outcomes until creation establishes their kind, and shared shells
keep their original mutable owner for [late links](thread-package-agents-and-background-work.md#background-shells).
Child lanes retain their existing mutable bookkeeping.

Later results cannot replace a call's first terminal or a turn's first ending.
Original-opening references still select the original source/turn generation
after ID reuse or a divider, rather than closing its successor. Inactive late
text still coalesces until a text-closing fact; even a duplicate creation for a
finished call must split that run. Bounds apply per scoped generation/call
identity, rather than to the number of distinct identities in a history.

`TestReferenceMainMarkers` rejects the former mutable finished-call collection,
checks the four marker fields and bounds marker entries and logical bytes across
64 rounds of redundant results/endings. Each redundant ending can still create
a public item, outside that continuation-state bound. Intermediate assertions
cover late result/creation/text, a divider, generation reuse and original-opening
recovery. `TestReferenceMainMarkerChild` checks late ownership transfer;
`TestReferenceMainMarkerPending` checks uncreated outcomes, launcher promotion,
lifetime mismatch and release of empty maps. `TestReferenceMainMarkerShell`
checks shared ownership through main closure, first-lifetime promotion, late
linking and completion with reused source/lifetime/legacy identities.
`testMainMarkerReplay` compares whole, five-entry and single-entry feeds with
legacy `New`, including complete items/Content/revisions, observations, touches,
publication visibility, consumed/last-shown versions, suppression and unproven
watermarks. Recorded replays and corpus golden expectations stay unchanged.

Production reference folds reduce genuinely final call/task histories through
`agentReports.reduce` in `applyAgent`, after `agentGroup.addReport` reconciles
companions. Four content-free marker slots retain the earliest non-result final,
earliest result, latest mapped nonterminal task report and latest mapped
status-empty update. Terminal order stays separate from the content-source ID;
digests preserve companion equality while a late mapped companion can replace
content provenance without changing `Rev`. Status-empty enrichment after a final
does not reopen work. Bounds apply per source/lifetime-or-legacy-scope and
call/task identity; they do not bound the number of identities in a history.
Legacy `New` retains its report collection as the differential oracle.

**An inactive row does not prove its reports are disposable.** A foreground
result can become launch evidence after a late task link. Reduction requires a
non-result terminal and a created owner with known kind/turn; uncreated calls,
unresolved joins and provisional work keep pending candidates. Unmatched durable
nonterminal companions also remain pending because later mapped content can
change enrichment order. Shells additionally retain eligible result/denial
evidence and earlier unmatched durable companions until mapped evidence fixes
foreground eligibility. See [background shells and parent repair](thread-package-agents-and-background-work.md#background-shells).

`TestReferenceFinalReportMarkers` rejects the old direct report slices, checks
intermediate provisional/link/genuine-final states and task reassociation, then
grows conflicting terminals, late denials and status-empty updates through 64
rounds for both Agent and Bash. It bounds retained candidates and pending
capacity independently of rendered payload length, comparing exact items,
observations, touches, publication membership, suppression and unproven
watermarks with legacy across whole, seven-entry and single-entry feeds.
`TestReferenceFinalReportIdentity` reuses IDs across five isolated source,
lifetime and legacy groups. `TestReferenceFinalReportsBeforeShellCreation`
protects creation-time shell eligibility; `TestReferenceEqualCompanion` covers
both companion arrival orders and captured recipe ownership. Existing generated,
shadow and recorded golden expectations remain unchanged.

`testChildMarkerShape` follows runtime values through pointers, interfaces,
structs, maps, slices and arrays, rejecting any retained `Item` or `contentRecipe`
snapshot even with empty `Content`. Checking only payload bytes would accept the
old redundant snapshots. `TestReferenceFinalReportAccounting` walks the entire
private `Fold` graph in a workload with no reversible children, so moving a
snapshot behind another index cannot bypass the guard. Only public `items`,
`recipes` and the independently bounded `source` are excluded. Fixtures that do
retain provisional children check their snapshots separately; optional snapshots
must be checked for presence before inspecting their Content.

`TestReferenceChildMarkers` bounds retained marker fields/entries through 32
rounds of redundant reports and enrichment, and asserts intermediate nested
closure/reopening, independent text/tool/agent finals and reparenting. Whole,
seven-entry and single-entry feeds compare legacy items/Content/revisions,
observations, touches, publication visibility, consumed/last-shown versions,
suppression and unproven watermarks, including captured publications.
`TestReferenceChildMarkerIdentity` reuses IDs across sources, lifetimes and legacy
scopes. `TestReferenceFinalChildReachability` checks publication of an already
final descendant after ancestor repair without revising that descendant.
`TestReferenceFinalChildTextEnrichment` grows 32 text fragments while retaining
only a marker privately; the required public text-ID list still grows.
[Equal-content companion replacements](thread-package-agents-and-background-work.md#agent-validation-and-testing)
must reach captured recipes without false revisions or touches. These checks
supplement the unchanged recorded replays and `testFoldCorpus` golden expectations.

`TestReferenceFinalReportAccounting` measures 16/64 repeated eight-entry finished
turns (128/512 entries), each with a main Read call/result, Agent launch,
independently finished child Read, task link/outcome and main ending. Each entry
has 16 or 4,096 padding bytes; each turn uses a distinct fixed-length `sNNNN`
source identity. Entries feed in eight-entry chunks and all four items per turn
must be inactive. `testFinalReportBytes` walks retained fields, separating public
metadata/recipes, child parent markers, reversible child snapshots, existing
main/agent markers, active/unresolved work and other lifecycle state. Logical
scalar sizes plus string bytes give the following before/after child-compaction
boundary; all values are bytes per turn except the total source cache:

| Turns | Padding bytes/entry | Private before → after | Public metadata/recipes | Child markers before → after | Reversible snapshots before → after | Other lifecycle | Source cache total |
| --- | ---: | ---: | ---: | ---: | ---: | ---: | ---: |
| 16 | 16 | 1,397 → 1,093 | 757 | 99 → 100 | 305 → 0 | 506 | 42,904 |
| 16 | 4,096 | 1,397 → 1,093 | 757 | 99 → 100 | 305 → 0 | 506 | 565,144 |
| 64 | 16 | 1,394 → 1,090 | 758 | 99 → 100 | 305 → 0 | 503 | 61,176 |
| 64 | 4,096 | 1,394 → 1,090 | 758 | 99 → 100 | 305 → 0 | 503 | 1,046,520 |

Every row additionally retains **174 main-marker, 71 agent-marker and 242
active/unresolved bytes per turn**, unchanged by child compaction. Private totals
sum those categories, child markers/snapshots and other lifecycle state, excluding
public metadata/recipes and source caches. Public and other-lifecycle values
round per turn; private totals round after summation.

The baseline walker counted child snapshot Items/recipes as metadata (1,061/1,062
bytes/turn), and parent facts/restoration state as other lifecycle (606/603).
The before columns reclassify 304 bytes of duplicate Items/recipes and one
restoration boolean into snapshots, and 99 bytes of parent marker/index facts
into child markers. The independent-finality boolean adds one marker byte,
yielding **304 fewer private bytes per finished turn**. Required public recipes,
including growing text-ID lists, are outside the private-marker bound. The bound
is per fixed scoped identity, independent of payload/text-list length; distinct
identities still need distinct markers.

Main-marker accounting includes turn/opening index keys, bounded turn cursor
facts and finished call/outcome records. Each workload turn retains two finished
call markers and no mutable main calls. Existing agent-report markers remain a
separate category. Active turns, mutable main calls (shared shells or uncreated
calls), pending child reports and agent-report candidates count as active or
unresolved work. Here all public items are finished: speculative child/result/
agent joins explain the unresolved bytes. Reversible child snapshots remain
necessary for unfinished or provisionally closed work, measured separately from
marker savings; this finished-child workload retains none. Other agent indexes
remain in other lifecycle state.

These are logical field bytes with rounded per-turn values, not a whole-Fold
heap target. Map/slice/pointer/string headers, capacity/alignment overhead,
authoritative fixture history and Store snapshots are excluded. Shared pointer
targets count once at their first owning traversal; strings count per field
without deducting shared backing storage. Source caches use their existing
independent bounded accounting. Main/agent and 100-byte child-marker costs are
fixed for this workload at both history and padding sizes. Entry references
are fixed-width uint64; item indexes use the host's 64-bit int, while identity
string lengths change logical cost. Recipe `ended_order` uses decimal JSON,
explaining the slight metadata increase with history size.

Container shape matters even outside logical accounting: inline marker values
would reserve a whole record in unused sparse-map slots. Pointer slots avoid
that cost without retaining old owners. The test separately logs host struct
sizes of 96 bytes for `mainCall`, 40 for `mainCallMarker`, 96 for `childState`
and 512 for the separate `childSnapshot`; final children retain no snapshot
allocation after resolution.
These host sizes include headers/alignment and are separate from logical totals
and allocator-level savings. See the
[child-snapshot measurement](../../specs/architecture/3481-final-child-snapshots.md#retention-measurements),
[main-marker measurement](../../specs/architecture/3480-main-markers.md#offline-retention-evidence),
[earlier agent-only measurement](../../specs/architecture/3479-final-agent-report-markers.md#offline-retention-evidence)
and [ADR 045](../decisions/045-thread-index-over-history.md).

The same unchanged `TestReferenceFinalReportAccounting` workload also measures
actual compact `json.Marshal` lengths of both checkpoint parts, separately from
the logical field bytes above. Each row below holds at both padding sizes:

| Finished turns | Padding bytes/entry | Private JSON bytes | Private bytes/turn | Separate item-record JSON bytes | Item bytes/turn |
| --- | --- | ---: | ---: | ---: | ---: |
| 16 | 16 and 4,096 | 51,536 | 3,221.00 | 31,642 | 1,977.62 |
| 64 | 16 and 4,096 | 207,070 | 3,235.47 | 127,498 | 1,992.16 |

At each turn count, increasing payload padding changes private encoded size by
**0 bytes**, with an asserted numerical bound of **0**: identities and table
memberships are unchanged and digests always encode as 64 hex characters.
Decimal JSON identity widths can change with turn count. These totals describe
the complete private continuation document, including its item manifest; the
metadata/recipe records have the separate cost shown. They are neither a heap
measurement nor a marker-only bound, and the finished workload retains no
reversible child snapshots. Distinct identities, unfinished work and required
item recipes still have their own costs.

