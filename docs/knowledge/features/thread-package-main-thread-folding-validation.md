# `internal/thread` — validation and retention

[Main-thread folding overview](thread-package-main-thread-folding.md).

### Validation and testing

`Fold.ExportCheckpoint() (CheckpointState, []CheckpointItem, error)` exports a
reference fold at a completed `Feed` boundary under its existing serialized-owner
contract. The two parts encode to JSON separately: private continuation state and
metadata/unrendered recipes for every internal item. Explicit mirror structs with
exported fields and JSON tags define the format independently of private field
names. `checkpointSchema` is 1 and must increase when the format changes;
`CheckpointState.Rules` separately records the current `foldingRules`, alongside
the conversation owner and consumed version. Legacy inline `New` folds reject
export and retain their existing behavior. In-memory restoration and synthetic
and committed-recording resume-equivalence proofs are available, along with the
persisted package reader. Durable writing belongs to #3504, scheduling to #3501
and Store restoration to #3337; Store loading still replays history. See
[Cache and epochs](thread-package.md#cache-and-epochs).

`HistorySource.Lookup([]uint64) ([]history.Entry, error)` supplies immutable
entries for the conversation. `NewReference(conversationID, source) *Fold`
constructs a reference-backed baseline;
`ImportCheckpoint(conversationID, source, state, items) (*Fold, error)` restores
the exported state with the expected conversation owner and a non-nil source.
The caller serializes fold/source access and keeps the source available for
later `Feed` and `Render`. Import neither looks up history nor renders content
or feeds the prefix; source contents are checked when subsequently used.

Records follow internal creation-ID order, independently of public `Order` or
visibility, including unresolved, hidden and delivery-suppressed rows.
`CheckpointState.ItemIDs` is the complete ordered manifest: import compares
all records against it to detect an omission even when no continuation
reference names the missing row. Continuation state includes watermarks, touches,
ever-published/current visibility membership, scopes and pending boundaries,
accepted sends/delivery matching, historical turn generations and original
openings, unfinished text, scoped child joins, agent lifetimes and observations.
Reversible children retain intrinsic/previous metadata and recipes; final children,
main calls and reports retain compact markers. Terminal-order and content-source
IDs stay separate, and equality digests survive export. Derived child indexes,
task-ID membership and text slots are rebuilt from the retained facts; constructor
initialization supplies runtime scratch without resolving or changing saved rows.

One-based identity tables (zero means nil) preserve shared turns, main/agent
shells, outcomes and report slots while keeping independent equal values distinct.
Item indexes remain zero-based with their existing negative sentinels. Stable root
traversal gives reproducible table IDs. `checkpointRegistry.ref` reserves identity
before following cyclic shell links and assigns the converted record by index
after recursion. Keeping a slice-element address across recursive table growth
could instead write into an obsolete backing array. Recipe fields omit absent
`json.RawMessage` literals with `omitempty`: encoding absence as explicit `null`
would decode into non-nil literal `null` bytes and change the document. Actual
scalar null literals remain explicit and detached.

Import allocates identity-table objects before wiring their edges. The original
fold, document and restored fold own independent nested maps, slices, snapshots,
recipes and literal bytes in both mutation directions. Within the restored
graph, shared turns, reciprocal shell owners and report slots remain shared;
equal-but-independent objects remain distinct. Value-based deduplication would
silently couple future mutations of independent report slots.

Unsupported schema or folding rules return `*CheckpointCompatibilityError`,
identifiable with `errors.As`. Wrong ownership, incomplete/duplicate/reordered
records, dangling references, invalid item indexes/ID relationships and malformed
SHA-256 digests return an error and nil fold. History-ID gaps and field-specific
negative sentinels remain valid. Equality digests do not authenticate a checkpoint;
file integrity and recovery belong to persistence and Store loading.

**An in-range index of the right kind does not establish ownership.**
`checkpointImporter.turnText` checks unique text ownership, turn/source, retained
main-opening generation and child group/parent lane. Swapping two valid assistant
indexes would otherwise send later deltas into the wrong reply. Finished-call
markers require an ordinary tool row or exactly the `-2` launcher sentinel and a
non-nil terminal; accepting `-1` or an assistant row breaks later child claims.
First-lifetime shell promotion can move the reciprocal ordinary owner into a
named lifetime while retaining its original child-call route. Requiring those
groups to be equal rejects a legitimate export: import checks the matching
source/tag, legacy-to-lifetime transition, destination call membership and
reciprocal shell identity instead.

`TestCheckpointEmpty`, `TestCheckpointRelationships`, `TestCheckpointRecords` and
`TestCheckpointLifecycleFacts` inspect JSON-round-tripped records and identities,
including historical openings, shared shells/report slots, pending joins and
otherwise unreferenced rows. `TestCheckpointOwnership` mutates nested exported
maps, slices and recipe literal bytes, checks the source fold against an
independent replay, and verifies later feeds cannot change an earlier document.
Export performs no history lookup or content rendering; failing lookup fixtures
guard that boundary. The document excludes history sources/caches, legacy inline
payload storage, counters and scratch buffers, and metadata has no `Content`.

`TestCheckpointImport` checks exact re-export and rendering after both parts
cross JSON, with history lookup forbidden during import.
`TestCheckpointImportInvalid` includes a missing otherwise-unreferenced row and
a legitimate history gap. `TestCheckpointImportOwnership` and
`TestCheckpointSnapshotIsolation` check graph identity and nested isolation under
document mutation and later feeding in both directions.
`TestCheckpointImportSourceFailure` uses a failing/counting source to prove zero
import lookups, then requires `ErrUnavailable` with no partial rendered results
or usable observation, and resumes by feeding only the suffix after repair.
It also checks that restoration adds no inline payload cache and preserves
legacy `New` output.

`Store.readCheckpoint` reads the independently versioned state envelope and
generation index described in [Cache and epochs](thread-package.md#cache-and-epochs).
It requires explicit non-null committed length, clean flag, fold owner/version/
last-shown and certificate epoch/version/watermark/checksum evidence. Explicit
zero progress and `Clean: false` are valid. Schema/rules, epoch/checksum shape,
owner and certificate/export version/watermark must agree before import. Missing
state, unsafe generations, short files, invalid lengths, malformed frame JSON,
CRC-32C failures within committed bytes and inconsistent inventories/graphs
return no restored fold. EOF/UnexpectedEOF mean invalid checkpoint data;
other header/payload I/O failures retain `ErrPersistence`. Previously decoded
rows never escape an error. CRC detects corruption, not authenticity, and this
reader performs no history-bound/checksum equality check or epoch decision.

`TestReadCheckpointResume` writes reverse-ordered snapshots with repeated changed
IDs, requiring last-record selection and manifest ordering. It uses the synthetic
corpus, send-delivery and unresolved-child fixtures at empty, early, middle and
full cuts, with both clean and dirty evidence. `testResumeProof` compares the
restored fold immediately and after every suffix feed, including hidden,
unresolved and delivery-suppressed rows. Restoration forbids history lookup and
prefix replay. Complete newer and torn uncommitted tails remain ignored and
byte-for-byte unchanged.

`TestReadCheckpointInvalid` applies absent/null evidence mutations to both
version-one and empty version-zero fixtures: nonzero value mismatches alone
could conceal a broken presence check. It also exercises compatibility,
manifests/graphs and committed framing corruption. `TestReadCheckpointItemsReadErrors`
injects header and payload failures after a decoded row to distinguish EOF,
non-EOF I/O and CRC errors while asserting no partial output.
`TestReadCheckpointSecurity` rejects redirected directories, symlink/directory/FIFO
leaves and generation escapes while preserving history and outside bytes.
`TestCheckpointLeavesSurviveLegacyCleanup` proves that read admission does not
authorize deletion of checkpoint leaves. Scalar certificate compatibility and
bounded unloaded lookup remain separate from checkpoint restoration.

The reusable same-package `testCheckpointResume(t, entries, cuts)` prepares one
uninterrupted reference baseline. For each cut it JSON-round-trips both export
parts, imports, and compares immediately and after **every** suffix entry. Its
proof includes exact rendered Content bytes, every Item field/revision, internal
creation order and recipes, touches, ever-published/current visibility and
consumed/last-shown, suppression and unproven watermarks. Each import hashes its
complete inventory, including hidden, unresolved and delivery-suppressed rows;
post-join affected candidates update the per-item signatures after suffix feeds.
Hashing only JSON containing `json.RawMessage` normalizes Content whitespace and
escaping, concealing byte differences. `testResumeProof.update` encodes complete
rendered metadata, checkpoint recipes and private publication facts once, then
appends an eight-byte big-endian Content length and the exact raw bytes. A
separate `ContentNil` fact distinguishes nil from empty Content. It borrows
immutable Content through `Fold.renderShared` while synchronously owning the
fold, avoiding detached payload copies and normalized/base64 re-encoding.
Selection metadata belongs to the proof: `renderShared` overwrites its selection,
so borrowing `f.items` would corrupt the oracle. The proof clears its selected
items and row scratch after use. `TestCheckpointProofContentBytes` retains the
exact-byte negative control; `TestCheckpointProofSensitivity`,
`TestCheckpointProofContentIdentity` and `TestCheckpointProofReuse` cover every
Item field, private facts, nil/empty/length distinctions and incremental updates
against fresh full recomputation. `TestCheckpointProofScratch` rejects another
payload-sized allocation in a warmed proof.

`TestCheckpointResumeCorpus` covers 201 distinct cuts of `testFoldCorpus(30000)`:
empty plus every boundary from 29,801 through 30,000, for 50,101 immediate/suffix
comparisons. Eight joined workers each import the shared base once and advance
their private preparation fold through increasing assigned cuts; cut zero uses
an independent empty fold. Restored suffix feeds never mutate preparation.
`TestCheckpointWorkerPreparation` checks stable preparation identity/version,
equality with freshly fed prefix exports and suffix detachment. Completion
counters reject both dropped cuts and dropped comparisons.

Each cut separately exports and JSON-round-trips both complete parts into fresh
zero destinations, checks deep equality, imports a detached fold without prefix
replay, and freshly renders/hashes its complete inventory before feeding its
suffix. Worker-owned `testResumeJSON` resets encoding storage between parts;
decoded maps/slices are never reused, preserving absent and nil/empty facts.
A full `testResumeProof.update` clears both row signatures and the aggregate
before recomputing: retaining either can include rows removed by the next
restoration. No rendered result or signature is reused across restorations.
`TestCheckpointJSONReuse` and `TestCheckpointProofRestorationReuse` compare
reused scratch with fresh computation after removed rows/private facts and
nil/empty/exact-Content changes; the latter fails against the uncleared helper.
The exhaustive corpus test uses `t.Parallel()` and requires
`PYRY_EXHAUSTIVE_TESTS=1`. `make test-exhaustive` runs it with its own package
timeout, without using a cached pass. `make preship` always requires it.
Diagnostics separate fixture preparation, uninterrupted baseline feed/proof,
prefix feed/export and joined-worker elapsed time, and report worker count,
`GOMAXPROCS`, completed cuts and comparisons. `BenchmarkCheckpointResumePhases`
separately measures fixture, feed, export, both JSON round trips, import, full
internal rendering and full proof; the proof phase includes rendering.

`make test` and `make check` retain the 30000-entry history in
`TestCheckpointResumeRepresentativeCorpus`. It restores nine cuts around late
links, queued delivery, shell promotion, text growth and turn closure, including
the independent empty-fold suffix. `TestCheckpointResumeFocused` still restores
every cut in the smaller scenario vocabulary. Sampling adds a bounded routine
proof; it does not establish exhaustive equivalence.

`make check` and the verifier require the complete sweep for changed restoration
dependencies or build inputs. Unknown inputs require it too. See
[release validation](../../release-tooling.md#full-production-timeout-proofs).
Export preallocates nonempty inventories and retains empty nil slices.

The [maintainer's Linux/amd64 report for #3503](https://github.com/pyrycode/pyrycode/pull/3506)
at `46118afa` (Go 1.26.9) records an uncached complete `make check` in 1,046.706s:
366.048s for ordinary units, 420.980s for all 201 corpus cuts/50,101 comparisons,
and 205.027s for configured fake-daemon e2e; subsequent `make build` passed.
The final verifier accepted separate green dispatcher gates; those gate receipts
have no per-test counts and do not independently recount the maintainer report.
macOS is outside this Linux ticket's verification scope. These measurements
establish completion for that run, not a runtime ceiling.

Repeated validation of inert object fields was a measured rendering cost:
`foldDecoder.decode` now checks object syntax directly for empty DTOs. Matched
Linux race profiles at `4d98f075` and `d0ca0353` took 250.728s and 220.544s for
the corpus package, respectively, both completing 201 cuts/50,101 comparisons.
Go 1.26.9 linux/amd64
ran on an i7-8700 (6 cores/12 logical CPUs), with an eight-CPU cgroup quota,
default `GOMAXPROCS=8` and test parallelism 8; neither eight-worker admission nor
`t.Parallel()` changed. Cumulative sampled CPU seconds fell from 139.57 to 50.15
in `foldDecoder.decode`, 246.83 to 159.26 in `Fold.render`, and 327.72 to 239.27
in `testResumeProof.update` (inclusive figures, not additive). Checkpoint JSON
round-trip CPU stayed nearly unchanged. The full-render phase fell from 1.709s
to 0.993s and 899,714 to 415,397 allocations; full proof fell from 2.291s to
1.644s. Sampled CPU/wall ratios stayed near 6.18/6.17 CPUs. These measurements
identify reduced executed validation work; host activity was uncontrolled, and
even the unchanged fixture's phase timing varied. They do not establish the
cause of the earlier host's ten-minute timeout or guarantee a runtime ceiling.
See the [matched measurements and commands](../../specs/architecture/3494-resume-corpus-runtime.md#revisions).

Uncached Linux race checks at implementation revision `2385022d` and the
full-module gate at `14517e33` passed with Go's default timeout:

| Check | thread elapsed | cmd/pyry elapsed | relay elapsed |
| --- | ---: | ---: | ---: |
| `go test -race -count=1 ./internal/thread` | 337.905s | — | — |
| `go test -race -count=1 ./internal/thread ./cmd/pyry ./internal/relay` | 375.708s | 345.521s | 46.591s |
| Uncached `make check` | 383.920s | 331.493s | 47.689s |

Both scoped thread reports confirm 201 cuts/50,101 comparisons and passing
`TestCheckpointProofContentBytes`; the full-module gate has package results but
no per-test counts. Its fake-daemon e2e package took 187.406s, and vet,
staticcheck, substrate-guard and build passed. See the
[verifier's evidence review](https://github.com/pyrycode/pyrycode/pull/3497#issuecomment-6207992285).
Suite/package scheduling contention remains a hypothesis for the additional
joint-run elapsed time; those runs do not isolate it or identify the historical
timeout's cause. The complete proof remains in the ordinary offline race gate.
macOS is outside this Linux verification scope.

Further Linux measurements on an i7-3770 with eight logical CPUs/eight-CPU quota,
Go 1.26.9 and default concurrency isolated redundant work in #3496. These are
separate matched comparisons at successive repair stages, using race builds and
one-iteration phase/lifecycle benchmarks:

| Work measured | Before → after seconds | Before → after bytes/op |
| --- | ---: | ---: |
| Full proof including rendering, reduced payload serialization | 2.691 → 2.181 | 124,756,008 → 81,730,088 |
| Complete feed, root-field map reused for validation | 19.650 → 12.537 | about 433 million → 297 million |
| Preparation for 25 increasing cuts, worker fold reused | 2.481 → 0.135 | 456,083,080 → 18,551,856 |

The 65,547-byte payload scratch control fell from 592,402 to 2,415 B/op.
JSON encoding buffers are worker-owned for safe reuse, but the five-iteration
matched JSON comparison measured 2.127/2.137s and 64.762/67.341 MB/op for
fresh/reused storage: no JSON microbenchmark speed or allocation gain is
established. The worker repair left standalone feed/render/full-proof phase
costs roughly stable; its preparation benchmark isolates the repeated imports
removed. Host activity was uncontrolled. These observations do not establish
historical contention causation or a runtime ceiling.

Earlier #3496 full/joint race aggregates still timed out at ten minutes, including
after the proof and decoder reductions and after merging main's test-cost
changes. They remain failed/incomplete results; isolated or focused passes do
not replace them. At `c15e2183`, the final isolated/joint thread runs passed in
518.337/508.616s with all 201 cuts/50,101 comparisons. At reviewed head
`58729724`, the verifier's uncached full-module race run passed 27,929 of 27,954
named records, with 25 existing skips and zero failures/unterminated records;
thread took 511.995s and again completed all cuts/comparisons with eight workers.
The dispatcher separately passed complete `make check` and subsequent
`make build` at that head; its nonverbose output proves package outcomes only.
See the [commands, profiles, measurements and failed gate history](../../specs/architecture/3496-accepted-send-cancellation.md#revisions)
and [final verifier evidence review](https://github.com/pyrycode/pyrycode/pull/3498#issuecomment-6211726633).
Other operating systems are outside this Linux verification scope.

Focused histories use every cut across unfinished text, late tool results after
turn end, late reports after genuine agent end, compaction followed by a divider,
background shell completion, sends, hidden children, late task links, reversible
ancestor closure and reused source/lifetime/legacy identities.

`TestCheckpointResumeCompanions` and `TestCheckpointEqualCompanionProvenance`
cover both mapped/durable arrival orders and directly assert content-source
replacement separately from terminal order, revisions and touches. Equal-content
provenance replacement generally leaves the rendered item unchanged, but a
durable-only shell result first becomes foreground eligible when its mapped
companion supplies the matching turn; that transition legitimately revises and
touches the row. `TestCheckpointLifecycleAssertions` independently checks
intermediate closure/reopening revisions and genuine endings.
`TestCheckpointResumeShellPromotion`, `TestCheckpointTurnTextOwnership` and
`TestCheckpointFinishedMarkerValidation` combine corruption rejection with
valid all-cut continuation, including late child claims. These default-build
offline tests retain the existing independent semantic assertions and unchanged
golden expectations.

`cmd/pyry.TestThreadRecordedBaselines` also calls `testRecordedCheckpointResume`
for `parent_tool_use_v2.1.259.json`, `task_notification_v2.1.259.json` and
`roster_after_finish_v2.1.280.json`, derived through the real parsers/emitter by
`testRecordedFrames` and `testNormalizeRecorded`. Each history covers cut zero
through every emitted entry, including full, hidden facts and companion emissions
within a frame. At each cut both export parts separately cross JSON before
`ImportCheckpoint` restores the same owner with a prefix-limited `HistorySource`.
Import must make zero lookups; only suffix entries are fed afterward. Comparisons
run immediately and after every suffix entry against detached snapshots from one
uninterrupted `NewReference` fold, without Store loading.

`testRecordedCheckpointCompare` directly compares every rendered Item field and
exact Content bytes, the complete exported item inventory (including hidden,
unresolved and suppressed metadata/recipes), and all `CheckpointState` facts:
touches, current visibility, ever-published membership, consumed/last-shown versions
and suppression/unproven watermarks. **Visible equality cannot prove continuation
state.** Clearing `Published` left rendered output unchanged but failed the state
comparison; whitespace-only Content corruption separately failed byte equality.
JSON-normalized item digests alone would miss the latter. Completion counters
require every cut and comparison: the three histories emit 27/12/18 entries,
covering 60 cuts and 687 immediate/suffix comparisons.

This default-build offline regression gate retains explicitly synthetic shell
creation, launch-result and turn-end context, unchanged semantic assertions and
committed goldens. Only the supplied frames are captured evidence; running this
proof adds no live Claude capture or recapture evidence. See the
[recorded corpus and evidence limits](thread-package.md#reusable-fold-regression-corpus).

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

For struct DTO roots other than `time.Time`, `foldDecoder.decode` passes its
already parsed root field map to `validRecordedObject`; reparsing the root in
`validRecordedFields` duplicated scanning and payload allocation. Typed
unmarshalling, exact required-key lookup, case-insensitive recorded-field
matching and recursive validation remain separate requirements. Non-struct
roots keep the recursive path, nullable containers remain valid and unknown
fields remain inert. `TestDecodeRecordedFields` compares acceptance and accepted
DTOs with the retained root-reparse control, including malformed/null values,
containers, case variants and unknown fields. `TestDecodeRecordedAllocations`
requires a relative allocation reduction; the Linux race control fell from
42 to 28 allocations and fails without reuse. The same-binary
`BenchmarkDecodeRecordedFields` measured reparse/reuse at 12.967/9.363 ms,
215,856/141,224 B/op and 62/44 allocations. These isolate removed root work
without weakening the untrusted JSON boundary.

For a non-nil `*struct{}` destination with no required fields,
`foldDecoder.decode` needs exactly one valid JSON object; decoding unknown fields
into maps and an empty DTO adds cost without validating anything more. It checks
the opening object byte and calls `json.Valid` on the **original** bytes. Validating
only trimmed bytes would admit non-JSON whitespace. Typed DTOs and required-field
checks retain recursive validation. `TestDecodeEmptyObject` compares acceptance
with map/DTO decoding, including malformed/trailing input, null, non-objects,
unknown null fields and duplicate keys, and rejects a nil destination.

`TestDecodeEmptyObjectAllocations` permits up to four scanner/depth allocations
for its fixed nested fixture: `json.Valid` pools its scanner, and race builds can
discard pooled values, so zero allocations is an unreliable assertion. The old
field-map path took 32 allocations and fails this bound. The same-process
`BenchmarkDecodeEmptyObject` named-empty-DTO control keeps that generic path;
the matched Linux race observation was 26,406 versus 3,060 ns/op and 32 versus
1 allocation/op. This isolates validation cost from between-run scheduling.

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
