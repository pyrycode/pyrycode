# `internal/thread` — checkpoint validation

[Validation overview](thread-package-main-thread-folding-validation.md#validation-and-testing).

## Checkpoint validation and resume proof

### Export, import and ownership

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
persisted package reader, durable transaction and worker checkpoint cadence.
Store restoration/history-bound validation remains #3337; Store loading still
replays history. See
[Cache and epochs](thread-package-cache-and-epochs.md#cache-and-epochs).

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

### Persisted checkpoint reader

`Store.readCheckpoint` reads the independently versioned state envelope and
generation index described in [Cache and epochs](thread-package-cache-and-epochs.md#cache-and-epochs).
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

### Durable transaction round-trip and fault proof

`TestWriteCheckpointResume` exercises the actual `checkpointWriter.commit` and
`Store.readCheckpoint` transaction round trip at empty, early, middle and full
cuts of the synthetic corpus, send-delivery and unresolved-child histories.
`testResumeProof` checks saved-boundary equality immediately and after every
suffix feed against uninterrupted folding, including hidden, unresolved and
suppressed inventory. Exact index bytes must equal the unchanged committed prefix
plus only changed frames, with the same generation on valid incremental commits;
new state/index leaves must be `0600`.

`TestWriteCheckpointOrderAndRecipe` pins the history/reconciliation/index/state
operation order. A first commit with no supplied deltas still writes the complete
inventory. A recipe-only change leaves public metadata/revision unchanged;
omitting it must fail before index mutation, while supplying it appends exactly
its frame and preserves later continuation. The scalar certificate stays absent.
Revision-only change selection would miss this case.

`TestWriteCheckpointFaults` injects history-sync, reconciliation/truncation,
partial/short append, index sync/close/directory sync, temporary-state
write/sync/close, rename and post-rename directory-sync failures. Each failure
leaves acknowledgement unchanged; surviving old/new state must restore a complete
fold, and every case retries. In the critical A → B → C case, B's rename succeeds
but directory sync fails. C supplies only its own delta, so successful restoration
proves retry preserved B's indispensable record despite stale acknowledgement A.
Other cases start with excess bytes beyond A to prove reconciliation of a torn
append. Failed clean attempts cannot leave a trusted clean claim.
`TestWriteCheckpointFreshFaults` repeats failures before any prior named state,
including complete newer state surviving the final directory-sync failure.
`TestWriteCheckpointRecovery` removes state or corrupts its JSON, graph or index;
no fold restores until retry writes a fresh generation from the full inventory.

`TestWriteCheckpointSecurity` rejects symlink/directory/FIFO state and index leaves,
outside/sibling directory redirects and a redirect during temporary-state writing,
while preserving history and outside bytes. The during-write case uses an outside
file with the temporary's own leaf name: checking containment only before creation
would miss unsafe deferred cleanup. Legacy-cleanup and scalar unloaded-read
bounds remain covered by the separate reader/Store tests above.

Worker scheduling and final Store certification have separate
[cadence and lifecycle proofs](thread-package-cache-and-epochs.md#worker-checkpoint-cadence-and-retained-changes).
Store checkpoint loading/history-bound validation remains #3337; Store loading
still replays history and scalar certificates remain separate. Native transaction
acceptance evidence is recorded in
the [transaction plan](../../specs/architecture/3504-durable-checkpoints.md#rework-validation).

### Resume oracle and exhaustive corpus

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

### Runtime measurements and evidence limits

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

### Focused and recorded continuation proofs

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
[recorded corpus and evidence limits](thread-package-shadow-lifecycle-and-evidence.md#reusable-fold-regression-corpus).
