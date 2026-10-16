# `internal/thread` — validation and retention

[Main-thread folding overview](thread-package-main-thread-folding.md).

### Validation and testing

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

`decode` checks object shape, required discriminating fields and known DTO field
types before items, pending pairs, scope or attribution change. Unsupported or
malformed payloads affect version only. **DTO unmarshalling alone is insufficient:**
Go's JSON decoder accepts `null` into scalar fields and scalar array elements as
zero values, including values inside string-valued maps. Checking only decoded
values would let an invalid `next_agent` alter boundary state, a null saved
answer enter a notice, or `input: {"path": null}` split text and create a tool
call. `validRecordedFields` validates known fields recursively, including
case-insensitive keys, nested saved answers and map elements. Nullable pointers,
maps and slices remain valid; their scalar elements cannot be null. Unknown
fields stay inert. Nullable optional token counts therefore preserve absent,
null and explicit zero source content.

`mainDecode` also validates parent fields, identity-loss markers and supplied
opening references before changing joins or text runs. Decoding only the public
DTO would miss these optional recorded fields and could admit truncated identity
or turn a malformed recovery reference into an unrelated closure.

`TestStandaloneItems` checks whole items with Claude, Codex, unknown and explicit
no-child provenance. `TestFoldReplay` compares every two-chunk partition and
independent owners;
`TestFoldOwnership` checks ID bounds and detached snapshots.
`TestBoundaryPairing`, `TestBoundaryAttribution` and
`TestBoundaryMalformedNeutrality` cover pairing, successor changes, old answers
and eviction. `TestMalformedRecordedFields` asserts version-only consumption and
unchanged pending pairs, scope and attribution before later matching and replay;
`TestNullableRecordedFields` protects legitimate nullable content.

`TestReferenceRendering` compares reference and legacy items across feed sizes,
including exact JSON formatting, unknown fields, detached content and the empty
user-summary fallback. `TestReferenceWorkRendering` compares every observation
and public touch in both modes through empty/nonempty appends, early terminals,
child closure/reopening and late text, then resolves old text and terminal IDs
after recent-cache eviction. `TestReferenceToolClassification` checks lifecycle
selection across Claude and Codex tools. `TestReferenceWorkSourceFailure` requires
atomic unavailable rendering/observations for missing creating, text or terminal
entries; `TestStoreReferenceWorkRetry` covers missing/unreadable text and enrichment
reads during replay and affected tail publication, followed by repair and `Retry`.
`TestReferenceEndingOutcomes` checks pre-creation reports, lane endings and creation
revision floors; `TestReferenceShellCompanion` checks distinct order/content IDs
in both companion arrival orders. `TestReferenceInheritedEnding` compares public
touches and observations through nested closure, late text and reopening after
cache eviction, including selected children whose ancestor is omitted.
`TestStoreCapturedInheritedEnding` retains a captured query root and detached
results across late text and reopening, including reference-backed nested agents.
`TestReferenceEndingDependencyFailure` rejects missing/unreadable/malformed
ancestor dependencies atomically; `TestStoreReferenceEndingRetry` checks repair
and `Retry` for an evicted child-ending dependency.

The unchanged
[generated/shadow and recorded corpus checkpoints](thread-package.md#reusable-fold-regression-corpus)
constrain rendered items, revisions, consumed version and last-shown watermark.
`TestReferenceLifecycleGuard` recursively walks retained `Fold` types through
pointers, maps, slices, arrays and structs, rejecting private `json.RawMessage`
fields. Public `Item`, recipes, the bounded source and legacy compatibility
containers are explicit boundaries; transient decoding DTOs are outside retained
state. Runtime `testNoLifecyclePayload` requires empty item Content and empty
Content in any retained child snapshots, empty legacy storage and scalar-only
recipe literals. The earlier payload-removal measurement with
`TestReferenceRetainedBytes` used unchanged `testFoldCorpus(6738)`: item Content
748,225 → 0 bytes, child Content 452,847 → 0, lifecycle compatibility payload
291,215 → 0, assistant summaries including child copies 68,907 → 0, user summaries
2,448 → 0; private outcome and parent-ending copies remain zero. Logical
fold/child recipes cost 367,360 bytes and lifecycle facts including fingerprints
115,328 bytes; recent cache costs 90,482 bytes. Logical counts exclude container
overhead and shared-storage deduplication.

`TestReferenceDiskHeap` isolates post-GC measurement in a helper process using
`testFoldCorpus(10000)`, an 8,192-byte unknown string per entry, 100-entry chunks
and a disk JSONL source: **85,441,272 encoded bytes**. Input/chunks and rendered
snapshots are released; fold/source/cache and disk offsets stay live.

| Revision | Pre-fold heap | Post-GC heap | Retained delta | Log fraction |
| --- | ---: | ---: | ---: | ---: |
| `44fe69498699cd69b0cc54543e7dabe0703c28a7` | 271,560 | 54,760,816 | 54,489,256 | 63.7739% |
| `476c7664d5b9ababd54135b0bb48b3de765dacba` | 271,288 | 5,497,320 | 5,226,032 | 6.1165% |

Run alone with `go test ./internal/thread -run '^TestReferenceDiskHeap$' -count=1 -v -args -measure-thread-heap`;
ordinary tests skip this opt-in measurement. The under-10% fold/source
boundary is workload-specific. Store publications are excluded: `TestStoreReferencePublication` separately reports
zero copied Content/summary/private payload and 242,580 logical public recipe
bytes on the unchanged corpus, not a Store heap ratio. See the
[measurement and baseline reproduction](../../specs/architecture/3453-agent-shell-references.md#offline-retention-evidence)
and [Store verification](thread-package-background-store-verification.md#store-verification).

`TestMainTextRuns`, `TestMainToolTerminals` and `TestMainEndings` cover text
splits, retained evidence, early and conflicting outcomes, activity and visibility
with Claude, Codex and metadata-free fixtures. `TestMainScopeAndRecovery`,
`TestMainReferenceNeutrality` and `TestMainScopedEndAndLateTerminal` cover reused
identities, original-opening references, source isolation and inactive late work.
`TestMainValidationAndLaunchers` checks child/launcher exclusions;
source-specific launcher fixtures matter because Codex treats the same names as
ordinary tools. `TestMainMalformedStateNeutrality` compares the whole fold state
after malformed input, catching damaged pending joins even when current rows
look unchanged. `testMainReplay` checks every two-chunk partition against full
replay; `TestMainReplayAndOwnership` checks copied pending reports, detached
content and independent owners. Run the offline package checks with
`go test -race ./internal/thread`.

`TestMainTextClosureTracking` checks retained generations, legacy scopes,
inactive late text, original-opening recovery and child isolation, including
unchanged settled revisions and `LastShownVersion`. `TestMainTextClosureWork`
compares fixed ingestion after fourfold growth in closed openings and historical
open-run peaks. Counting only successful closures or yielded map keys would miss
historical-capacity scans. Measure registration/removal, skipped inspections,
tracking growth/copying and public ordering/cleanup too, including repeated
open/close cycles and empty closure. See the related
[public-touch capacity regression](thread-package.md#cache-and-epochs).

`TestTextAppendBytes` compares exact JSON and summary output with the former
map-based append, including case variants, escapes, control characters,
per-payload UTF-8 replacement and input/snapshot ownership.
`TestTextAppendChildren` and `TestTextEmptyProvisionalClosure` check append and
empty-delta behavior through provisional closure and reopening.
`TestAffectedPublicationOwnership` and `TestAffectedPublicationTextLifecycle`
retain internal publications and query roots after append-buffer initialization,
then check immutable JSON, borrowed Content, exact suffix changes, concurrent
reads and detached public results through later appends and child reopening.
Capturing only before initialization misses mutable-buffer corruption because
the first nonempty append replaces the creating payload's allocation; retaining
only detached `Fold.Items` output cannot prove internal publication ownership.
`TestTextBufferPublicationOwnership` forces spare capacity and growth, checks
detachment before writes and subsequent private reuse, and requires the
allocation/copy work to be counted. The fixed-size scenarios in `testFoldCorpus`
alone cannot detect quadratic work within one long
reply. `TestTextAppendScaling` therefore grows delta counts from 256 to 1,024 in
one main run and child runs while open, provisionally closed, reopened and across
all three phases; `TestFoldIngestionScaling` grows the corpus from 1,000 to 4,000
entries. Both require less than twice the work per delta/entry. Counters include
initialization, inspected/skipped candidates, lookups, ordering/maintenance,
JSON/summary bytes, comparisons and buffer allocation/growth/copying; input
generation, explicit snapshots and digests are excluded.

`TestPublicTouchesReference` compares entry-scoped bookkeeping with an independent
full-walk oracle. `TestPublicTouchAffectedWork` grows unrelated items and trees
fourfold while keeping affected entries fixed. A foreground launch result can
close descendants and advance their revisions, concealing revision-only
appearance tracking. Its late-appearance fixture first links the parent to
background work, leaving a descendant's revision unchanged when its ancestor
becomes public; omission and reappearance also preserve the earlier watermark.

`TestSendDelivery`, `TestSendTerminalStates` and `TestSendLinksAndTerminals`
cover permanent identity, delivered content/order, supported reasons,
unresolved acceptances, linked/unmatched messages, conflicting terminals,
message reuse and equal app IDs across devices with Claude, Codex and
metadata-free fixtures. `TestSendOwnershipAndProvenance` checks receiving
fallback across session switches, explicit no-child attribution, independent
owners reusing durable IDs and detached input/snapshots. `TestSendTextRuns`
checks text splits and later main-work updates after message suppression.
`TestSendMinimalAndCasing` covers absent sender fields, nullable attachments and
deterministic duplicate case variants. `TestSendMalformedNeutrality` compares
whole fold state before later valid resolution, catching damaged joins or
attribution even when rows appear unchanged; wrongly typed/null scalars or links
and unsupported reasons change version only. These scenarios use `testMainReplay`
to compare full replay with every two-chunk partition, including the gap between
message and outcome.

`TestReferenceDeliveryEvicted` proves the delivery left both the current chunk
and recent cache and asserts its authoritative lookup before comparing merged
acceptance bytes, provenance, suppression, revisions, touches and watermarks with
legacy folding. Separate message/outcome chunks alone can remain cache hits and
leave the eviction path untested. `TestReferenceDeliveryFailure` checks missing,
unreadable, malformed and null acceptance/delivery sources before claiming either
identity. `TestReferenceSendRendering` compares complete observations, changes,
touches, items and watermarks with legacy through each lifecycle and after
eviction, asserts zero retained send content/summary or recipe payload literals,
and checks detached results. `TestReferenceSendSourceFailure` withholds entire
renders and observations for acceptance/delivery/outcome faults after eviction.

`TestStoreReferenceSendRetry` proves repair and fresh replay after missing or
unreadable acceptance/delivery reads in replay and tail, and outcome reads in
replay. **A terminal outcome is still in the current chunk on its only tail
mutation.** Ignored tail entries reuse unchanged publications and cannot force
an outcome lookup; exercise evicted-outcome failure through replay or explicit
rendering. For tail acceptance/delivery faults, consume 256 ignored entries
before appending the terminal fact. Waiting for that consumed version proves
FIFO eviction independently of tail chunk scheduling, without full-page padding.
