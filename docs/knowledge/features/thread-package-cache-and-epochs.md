# `internal/thread` — cache and epochs

[Thread package overview](thread-package.md).

## Cache and epochs

### Scalar certificates and unloaded metadata

History is the only durable source of truth. Beneath the supplied history
instance directory, `conversations/<conversation-id>/history/` holds
one independent `thread-certificate.json` beside the history segments.
Schema-1 `cacheRecord` stores only `Schema`, `Rules`, `Epoch`, fully consumed
`Version` V, `LastShownVersion`, `Checksum` and `Clean`. It contains no items or
shared-run references and serializes to fewer than 1024 bytes regardless of
thread size. At V zero the checksum is empty and the last-shown watermark is
zero; otherwise the checksum is 64 lowercase SHA-256 hex characters and the
watermark cannot exceed V. `EnsureLogDir` can create an empty contained directory
without fabricating history entries.

Converted Store publication slots, comparison state and query rows retain only
metadata and immutable content recipes: no rendered `Content` or derived
user/assistant `Summary`. Consumed-boundary evidence and scalar certificates
retain no rendered content.
Baseline reads render all public rows; bounded queries select metadata first,
then resolve only required history references from the captured publication.
See [snapshots and retry](thread-package-background-store-replay.md#snapshots-and-retry).
All production item kinds, including agents and linked shells, use references;
private outcomes and parent endings retain no payload copies. Legacy `New`
compatibility storage is unused in production. The
[fold/source heap proof](thread-package-main-thread-folding-validation-replay.md#replay-rendering-and-ingestion-validation)
excludes Store publications, whose structural accounting remains separate.
The worker's recent-entry cache (256 entries, inclusive 1 MiB), history's bounded
caches and the detached change ring (64 batches, 1 MiB encoded) are separate
budgets. Transient rendering creates no unbounded rendered-item or entry cache.

For unloaded unread metadata, `Store.UnloadedLastShownVersion` holds the store
lock and rejects a closed store or any worker, including one still retiring.
It obtains H from `history.Store.CachedLatestEntryID`, which never warms a cold
cursor or reads segments. Missing/unknown progress and history lookup failures
return absence before certificate access. Loading an empty thread need not warm
that cursor: a clean zero-version certificate alone cannot establish H = 0.
An ordinary history operation must independently establish current progress.

`recoveryCandidate` then reads at most the one contained, no-follow certificate
leaf and checks current schema/rules, clean status, epoch and explicit non-null
evidence fields, watermark bounds and checksum shape. Missing, unreadable,
malformed, dirty, incompatible or invalid evidence returns absence. Metadata
eligibility additionally requires exact V = H, rejecting both older and newer
certificates. Valid empty evidence at V = H = 0 returns present zero. Any later
append invalidates the unloaded reading even if it would not raise
`LastShownVersion`; neither the shown watermark nor retained retired-fold
progress can substitute for H. Ordinary loading/folding and clean retirement
can restore eligibility. This metadata read validates checksum syntax only;
it proves neither replay/checksum equality nor catch-up continuity. Epoch reuse
requires the validated checkpoint recovery below; the scalar certificate alone
cannot authorize it.

`entryChecksum` hashes the stored entry at V with SHA-256. Its ordered input is
ID, type byte length, type bytes, timestamp Unix seconds, timestamp nanoseconds,
payload byte length and stored payload bytes. Every integer is a big-endian
uint64, including the converted Unix seconds; type and payload bytes are
length-prefixed. The timestamp identifies its instant independently of timezone
or in-memory monotonic time. Segment-line formatting is excluded, while stored
payload bytes remain opaque. This identifies entry V, not the whole prefix or
resumable fold state.

### Incremental checkpoint format

In-memory reference folds support `ImportCheckpoint` from the two JSON export
parts, with a caller-supplied `HistorySource`, without prefix replay or history
I/O. Restoration preserves the complete internal inventory and continuation
graph; it does not restore a Store epoch or establish certificate eligibility.
See [checkpoint validation and resume proof](thread-package-main-thread-folding-validation-checkpoints.md#checkpoint-validation-and-resume-proof).

The package reader `Store.readCheckpoint` implements the persisted format from
[ADR 045](../decisions/045-thread-index-over-history.md). Its
`thread-index-state.json` envelope uses `checkpointFileSchema` 1, independently
of the exported fold schema. It contains `schema`, `generation`, `valid_length`,
`certificate` evidence and the complete `fold` (`CheckpointState`), including
private maps and the ordered `ItemIDs` manifest. Generation leaves are exactly
`thread-index-<32 lowercase hex>.bin`. Each append-only index record has an
eight-byte header: big-endian uint32 JSON payload length, then big-endian uint32
CRC-32C (Castagnoli) of one `CheckpointItem` payload.

Only complete frames within the committed `valid_length` participate. The reader
checks that boundary against file size, then each header/payload length against
remaining committed bytes before allocation or reading. Extra bytes, including
a complete newer record or torn final frame, are ignored without truncation.
For repeated item IDs the last record in file order wins; the complete inventory
must match `CheckpointState.ItemIDs` and is reordered by that manifest before
`ImportCheckpoint` validates the graph. Missing, corrupt or inconsistent state
returns no fold. Both clean and dirty checkpoints, including explicit empty
version-zero checkpoints, can restore exactly the saved boundary without history
lookup. This validates persisted consistency only: history-bound/checksum equality,
epoch selection and fallback belong to `Store.resumeCheckpoint`, described
below. The reader alone never authorizes generation mutation or publication.

Checkpoint state/index reads share `openCacheFile` admission with scalar
certificates: `cacheLeaf` accepts only package-owned leaves, history supplies exact
directory containment, and Lstat, no-follow/nonblocking open and descriptor Stat
require regular files. `removeLegacy` selects its own legacy leaf family; widening
read admission must never widen deletion to checkpoint state or generations.
`thread-certificate.json` stays separate and scalar. Unloaded metadata opens at
most that one certificate, reads no checkpoint/index or history segments and
starts no worker. The reader performs no writes or truncation.

### Durable transaction and retry authority

`checkpointWriter.commit` implements the package-local durable transaction for
[ADR 045](../decisions/045-thread-index-over-history.md). Its owner serializes
attempts per conversation outside `Store.mu`, supplying detached complete
`CheckpointState`/`CheckpointItem` exports, certificate evidence and selected
changed internal records. Changes include private/recipe differences even when
public metadata and `Rev` are unchanged. Before index mutation, the writer checks
that surviving records plus supplied changes equal the complete export; an
omitted recipe-only change must fail rather than save inconsistent continuation.
Only index writes scale with changed records: validation can inspect the full
inventory, and every state replacement rewrites the complete private fold state.

Durable success requires this order:

1. Validate the candidate export/evidence, sync history with `history.Store.Sync`
   and require its latest entry bound to cover the candidate version.
2. Read and validate the surviving named checkpoint on **every** attempt through
   `Store.readCheckpoint`. Missing, incompatible or invalid saved state requires
   an exclusively created fresh generation containing the complete inventory,
   even when the supplied delta is empty. Persistence I/O errors fail the attempt;
   they do not authorize a delta-only rebuild. Reject an older candidate than the
   surviving checkpoint.
3. Reconcile the validated generation to its committed `valid_length`, truncating
   excess uncommitted bytes, then append the selected framed records. Sync and
   close the index and sync its directory before publishing state. Repeat the
   directory sync even for a reused generation.
4. Write the complete state to a same-directory temporary file, sync and close
   it, rename it over `thread-index-state.json`, then sync the directory. Only
   after that final success does the writer advance its in-memory acknowledgement.

Writes, including short writes, truncation, syncs, closes and rename are checked.
A failed attempt never advances acknowledged durable progress. Surviving named
state restores an old or new complete checkpoint; absent or invalid state restores
no fold. For a dirty attempt whose rename succeeds but final directory sync fails,
newer state B may survive while memory still acknowledges A. Retry must validate
B and preserve its committed records: truncating to A's remembered length would
destroy B. A partial append beyond A is instead discarded to A's validated length
before retry. If state has disappeared, retry writes a full fresh generation.
Failed clean attempts best-effort remove named state and sync its directory;
failed invalidation still cannot authorize clean certification.

Fresh index/state leaves are mode `0600`. Append, truncation, replacement and
cleanup retain package-leaf admission, exact history-directory containment and
no-follow regular-file checks. Deferred temporary cleanup must revalidate the
original directory before removal; after redirection it leaves the original
temporary in place rather than removing an outside file with the same leaf.
These checks do not provide descriptor-relative atomicity. After state
replacement, `cleanGenerations` removes obsolete admitted generation leaves,
preserving the newly named generation. Cleanup failure fails the attempt
without advancing acknowledgement. The transaction does not compact the retained
generation or write `thread-certificate.json`; the worker coordinates that separate
scalar certificate with final certification.

### Worker checkpoint cadence and retained changes

After complete initial replay, each worker arms a 30-second checkpoint timer.
At each deadline it persists a consumed version different from its last
successful checkpoint, including version-only progress and a change followed
by silence. Unchanged versions, including initial version zero, cause no
periodic write. Publications never reset an armed deadline. After processing
each deadline the worker starts another interval, so failures retry at the
next interval without immediate retries or catch-up bursts. Periodic checkpoints
remain dirty. Each failed attempt emits one fixed `thread checkpoint failed`
warning without raw errors, paths or content; usability and later folding and
publication continue.

`workerCheckpoint.commit` exports the complete detached state and inventory at
a completed feed/capture boundary. It compares encoded internal records with
the last successful export and retains pending IDs until durable success.
This is independent of `publicationCapture.capture` clearing its affected set:
multiple publications and failed attempts cannot lose the latest hidden,
unresolved, suppressed or recipe-only records. Public revisions alone cannot
detect these changes. The first checkpoint of each full-refold worker writes
the entire inventory into a fresh generation, even if a previous worker left
valid state. A resumed worker instead acknowledges every saved internal row,
including
hidden and recipe-only inventory, plus the saved version and writer state, so
unchanged rows need no append even on no-suffix retirement. Subsequent attempts
reconcile surviving named state before appending pending latest records; a
renamed state surviving failed directory sync must not be rolled back to an
in-memory acknowledgement.

Only item-record appends are incremental. Export, comparison and validation
may inspect every row, and `thread-index-state.json` is replaced in full,
including its complete manifest and private maps. Successful checkpoints log
`live_record_bytes` for the latest inventory and `committed_record_bytes` for
the generation's committed records, both including frame headers;
`state_rewrite_bytes` reports the complete state replacement separately.
`TestStoreCheckpointBytes` applies the same recipe-only change to small and
large inventories: index append bytes stay equal while state replacement
bytes grow. `TestStoreCheckpointPending` reads persisted boundaries through
`Store.readCheckpoint` and uses `testResumeProof` to check saved-state and
suffix-feed equivalence across publications and failed attempts.

### Store replay and public touches

`Store.run` uses the same validated recovery path for Load and Retry. Its order is:

1. Read `history.Store.Page(id, "", 1)` to establish the surviving readable bound
   H, then create the worker's bounded recent source. An append cursor can exceed
   surviving history and cannot substitute for this probe, even at H zero.
2. `Store.readCheckpoint` validates/imports the complete saved graph without
   history reads. Require saved V <= H; for nonzero V, look up the stored entry
   at exactly V and compare `entryChecksum`. A missing or changed V rejects the
   checkpoint, including reused IDs whose visible items are unchanged. Accepted
   state seeds complete durable row/version/writer acknowledgements.
3. Select the epoch from the original checkpoint clean flag, then durably dirty
   the scalar certificate and any existing readable state without advancing the
   saved fold boundary. Absent state needs no eager checkpoint transaction.
4. For accepted state, reconcile its selected generation through a contained,
   no-follow/nonblocking regular-file descriptor to exactly `valid_length`.
   Check descriptor Stat, containment/admission again before descriptor Truncate,
   then Sync and Close. Any failure withholds publication. Complete or torn
   trailing frames have no effect on restoration and are discarded here.
5. `cleanGenerations` confirms the surviving state still names that generation,
   then removes only other exact `thread-index-<32 lowercase hex>.bin` leaves.
   Recheck containment and regular-leaf admission before each removal and sync
   the directory after removals. Preserve named state/index, history, unrelated
   files and redirected targets; unsafe leaves/directories fail closed. On
   fallback, preserve the old named generation until fresh replacement state
   commits; successful writer commits repeat this cleanup.
6. Remove admitted legacy metadata, position production
   `history.Store.Forward(id, V)` and seed immutable publication slots, recipes
   and activity from the restored fold. Feed only `(V, H]`
   in bounded chunks, requiring `Fold.Version() == H` before first publication.
   Query metadata includes restored rows even with no suffix. Full-refold paths
   instead start at zero and force a complete fresh generation on checkpointing,
   retaining that policy across failed persistence attempts.

**Visible items are insufficient continuation state:** `Fold.Items` omits
unresolved children, pending reports and private joins. Import restores these
facts, touches, visibility/suppression and the last-shown watermark before suffix
folding. `publicationCapture.restore` seeds validated metadata directly; treating
all restored rows as initial changes would render the entire prefix only to
classify changes the initial publication does not use. Only suffix candidates
need that classification. The same reader then tails commits during recovery
and handoff once in order. See
[loading and replay readiness](thread-package-background-store-replay.md#loading-and-replay-readiness).

Within each entry, `markAffected` deduplicates stable indexes for additions,
intrinsic changes, reparenting, newly publishable descendants and suppression.
After `resolveChildren` settles private joins and expands affected descendants,
`recordShown` visits only those candidates in creation order, using retained
public reachability for parent dependencies. Unrelated settled items and child
trees require no visibility walk. Current reachability and ever-public appearance
are separate: withdrawal removes retained touches, while appearance history
survives, so reappearance alone cannot raise the shown watermark.

Candidate ordering and cleanup traverse a reusable dense slice; membership is
deleted only for its current indexes before resetting the slice length. Ranging
or clearing a membership map can scan storage retained from its largest past
entry even when few keys remain. Counting yielded keys or `len(map)` misses that
work. Store's chunk-wide publication candidates use the same dense-slice rule;
deduplication alone does not make enumeration affected-only after a large replay.
`TestPublicTouchHistoricalPeakWork` checks fixed appends and duplicate
registration after unrelated repaired trees grow from 4,096 to 16,384 children.

Full replay reconstructs `Snapshot.LastShownVersion` entry by entry, independent
of replay chunking; checkpoint resume restores that fact and advances it through
the suffix by the same rules. It is the history entry ID that most recently made
a shown item first appear in `Fold.Items`, after private joins resolve, or appended
nonempty message text to an already publishable shown item. An unresolved child
counts only when it first becomes publishable. Hidden additions, empty appends,
status/activity/visibility-only changes, delivery/order-only changes, live state
and malformed/unsupported/foreign entries do not raise it. Later revisions and
hiding or dropping a row never replace or lower the earlier watermark.

**Final item revisions cannot reconstruct unread state.** A queued send can
first appear shown, then be dropped with a newer `Rev` and `Shown == false`;
the earlier shown watermark still applies. Load/Retry restores it from validated
private continuation state or reconstructs it through full replay; neither the scalar watermark nor maximum final shown
`Rev` can reconstruct it.
A usable empty conversation has both `Version` and `LastShownVersion` zero.
Consumers determine unread with `LastShownVersion > ReadUpTo`, using the
existing single read mark in the same history-ID space; the fold/store does
not persist read marks. See
[ADR 042's read marks](../decisions/042-daemon-built-thread.md#sessions-agents-messages-read-marks).

#### Resume tests and history-read accounting

`TestStoreResumeCuts` compares initial results, subsequent appends and retirement
with full replay across zero, unfinished/private and no-suffix cuts, both clean
and dirty. It reuses `testResumeProof` for exact content and private continuation.
`TestStoreResumeReject` covers migration/rejection and fresh-generation recovery;
`TestStoreResumeReconcile` checks excess frames, unsafe leaves and descriptor
Truncate/Sync/Close failure followed by repair and Retry. Existing reader/writer
and `TestStoreCheckpointDirty` matrices cover persistence failure boundaries.

A checkpoint covering all history bypasses fold hooks entirely. Barrier/failure
fixtures must leave an uncheckpointed suffix to exercise recovery folding;
epoch-replacement fixtures must invalidate checkpoint authority as well as the
scalar certificate. Otherwise the intended failure/assertion may be unreachable.
See the [resume-aware daemon fixture review](https://github.com/pyrycode/pyrycode/pull/3566#issuecomment-6231086763).

No prefix **folding** does not imply no older history **reads**. The
[selective Forward contract](history-package-shape.md#forward-consumption) allows
bounded cold segment discovery; Page probes H, Lookup validates the checksum at
V, and requested rendering can resolve older references. `TestThreadResumeReadAccounting`
combines actual history segment counters with real Store suffix-feed assertions,
using a test-only adapter rather than a production diagnostics API. Its separate
6010-entry fixture saves V=6000 with suffix=10 across 13 segments/12,792,700 bytes.
The [builder evidence](https://github.com/pyrycode/pyrycode/pull/3566) records:

| Operation | Cold bytes / opens | Warm bytes / opens |
| --- | ---: | ---: |
| Forward first-entry discovery | 28,408 / 13 | 0 / 0 |
| Forward selected suffix segments | 198,039 / 1 | 198,039 / 1 |
| Page readable-bound probe | 198,039 / 1 | 198,039 / 1 |
| Checksum-at-V Lookup | 226,447 / 14 | 198,039 / 1 |
| Store load total | 820,564 / 17 | 792,156 / 4 |
| Requested snapshot rendering interval | 1,049,137 / 1 | 0 / 0 |

These are distinct probes/intervals, not additive phases; load/render intervals
may include Tail's initial recheck. Checkpoint-file reads are outside history
segment counters. Both real Store loads fed one suffix chunk. Keep validation,
discovery and rendering costs separate from the proof that no entry <= V enters
the resumed fold.

For the same synthetic 30000-entry warm history on Linux/amd64, Go 1.26.9 with
race instrumentation, saved V=29990/suffix=10, the matched builder run measured
resume **8.918240486s** versus full refold **19.599243459s**, one versus eleven
fold chunks. Command:

```sh
python3 scripts/test-admission.py -- go test -timeout=30m -race -v -run '^(TestStoreResume.*|TestThreadResumeReadAccounting|TestForwardResumeSelectiveReads)$' ./internal/thread ./internal/history
```

The boundary is `Store.Load` admission through first usable `Status`, including
validation, dirty marking, suffix folding and publication preparation; requested
snapshot rendering is excluded and its reads are reported separately above.
The [final verifier evidence](https://github.com/pyrycode/pyrycode/pull/3566#issuecomment-6231086763)
reports 11.061243097s versus 22.414381521s in the full race unit gate on the same
30000-entry shape. Measurements establish observations, not an absolute timing
threshold or a millisecond-readiness guarantee. macOS is outside this Linux
ticket's verification scope.

### Epoch recovery and scalar certificate lifecycle

Each usable `Snapshot.Epoch` is a 128-bit random identifier encoded as 32 hex
characters, separate from deterministic item equality. A compatible, completely
validated clean checkpoint retains its epoch, including when later history
supplies a suffix. Valid dirty state restores the same fold without prefix
folding but receives a new epoch. Select from the original clean flag before
dirty marking. The standalone scalar certificate cannot authorize restoration,
retain an epoch by itself or override dirty/rejected checkpoint state.

Absent pieces, committed corruption, inconsistent graphs, wrong ownership,
changed rules, incompatible file/fold schemas (including newer schemas), V > H
or missing/changed entry V require a full refold and new epoch. Scalar-only
installations migrate with that same one-time refold/new epoch; the next
successful checkpoint establishes a complete fresh generation. Other
conversations' recovery and retirement cannot authorize or invalidate this one.
Folding semantics changes must increment `foldingRules`.

Before suffix/full replay, each load/retry replaces the scalar certificate with
`Clean: false`, using validated saved progress and the selected epoch or a fresh
epoch at zero, then durably marks existing readable checkpoint state dirty
without changing its saved fold boundary. Temporary-file sync/close, atomic
replacement and containing-directory sync must succeed on both surfaces before
usability. This prevents a crash from restoring an older clean claim over lost
published history. Failure on either surface withholds usability; after repair,
Retry repeats validation and dirty marking. Absent state is not eagerly created.

**Version bounds and item equality cannot detect replaced tail entries.** A lost
unsupported entry can be replaced at the same ID without changing folded items.
`TestStoreResumeReject` changes an unsupported entry at V while preserving
visible equality and requires epoch rotation and a fresh generation.
`TestStoreResumeCuts` retains clean epochs across suffix appends. Resume fixtures
must checksum the entry returned by history Lookup: JSON escaping can change
stored payload bytes without changing rendered content, silently turning an
intended resume proof into a full-refold fallback.

Empty-history evidence must explicitly contain non-null `Version`,
`LastShownVersion` and `Checksum`. Ordinary JSON scalar decoding otherwise turns
missing/null fields into valid-looking zero progress. `cacheRecord.UnmarshalJSON`
tracks presence separately, and `recoveryCandidate` rejects absent evidence.
`TestStoreCacheMissingEmptyEvidence` removes or nulls each field of a clean
zero-version certificate and requires rotation on reopen; shutdown must leave
that already-unloaded record untouched.

Choose the revision against the current merged rules: concurrent semantic changes
can otherwise select the same revision. A missing-field cache regression can
still pass because replayed content differs, concealing that collision.
`TestStoreQueueHandleCacheRules` also supplies old-rule caches with matching
version, checksum and watermark and requires a new epoch, proving rule
incompatibility independently of evidence mismatch. Accepted-send handle
semantics use rules revision 3; caches from revisions 1 or 2 rebuild from raw
history and cannot hide recorded handles.

Recovery remains `StateRebuilding` until durable dirty marking, legacy cleanup
and suffix or full replay through the readable bound succeed. Missing or invalid
evidence rebuilds from history; genuinely missing or empty history gives an empty usable
view at version zero. Unreadable or incomplete history never yields a cache-only
view. Actual history/checkpoint/certificate I/O, containment and unsafe-leaf
failures leave `StateUnavailable` with generic `ErrUnavailable`; repair followed by `Retry`
recreates the reader/source and revalidates persisted state rather than reusing
a partially failed fold. Missing/invalid checkpoint data permits fallback;
actual I/O or containment errors never do. Lifecycle persistence failures retain
`ErrPersistence`.

Publication and active-to-idle transitions perform no persistence. The periodic
timer writes dirty checkpoint state for changed versions but does not rewrite
the scalar certificate. There is no shared run/coordinator/recovery marker
state. Each new successful load/retry writes one dirty scalar certificate; each
successful joined retirement writes one clean scalar certificate after its
final checkpoint, even at unchanged V or empty version zero.
Tail items, consumed version, `LastShownVersion` and change notifications publish
together in memory. Publication capture shares unchanged immutable item/content
storage and compares only final affected public rows. Retirement evidence pairs
the checksum with the version and watermark of the last successfully folded and
captured progress; a failed or interrupted chunk cannot advance it. Public
snapshots, observations and queries still detach their results; see
[snapshots and retry](thread-package-background-store-replay.md#snapshots-and-retry).
After an unclean stop, valid dirty state restores pending child/report evidence
and resumes its suffix with a new epoch. Absent/rejected state instead refolds.

Writes use synced, closed `0600` temporary files, atomic replacement and directory
sync; reads use no-follow regular-file checks. Each operation validates IDs,
`cacheLeaf` and history's exact directory containment, rejecting sibling/outside
redirects and symlink or nonregular metadata leaves. If directory sync fails
after replacement, `writeCacheFile` attempts contained removal and another
sync before returning `ErrPersistence`, so readable clean evidence is invalidated
where possible. Errors contain no content, payloads or host paths; metadata
operations preserve history bytes and its existing append durability contract.

Deferred temporary cleanup in `writeCacheFile` requires a fresh successful
`history.Store.LogDir` lookup equal to the original directory before attempting
removal. If replacement fails after that directory is renamed and its old path
redirected outside, the write returns `ErrPersistence`; cleanup intentionally
retains the temporary in the renamed original directory and preserves any
outside file with the same leaf name. These path rechecks do not provide
descriptor-relative atomicity between the check and removal.
`TestStoreCertificateTemporaryCleanup` checks unchanged outside marker, history,
certificate and retained temporary bytes after redirection. Its success and
contained replacement-failure controls require no leftover temporaries; the
failure control also preserves the existing certificate and history bytes.

Only after the first durable certificate write does `removeLegacy` remove this
conversation's regular `thread-cache.json`, `thread-recovery.json` and allow-listed
`thread-run-<token>.json` leaves. It rechecks containment, rejects redirects and
special files without following them, preserves checkpoints, history and unrelated
files, and syncs the directory after cleanup. Cleanup failure withholds usability.
Legacy-only startup rotates once; the next clean reopen retains the new epoch.
Rollback to pyry 0.39 finds no legacy cache, refolds history and mints a new epoch.

`Unload` marks retirement and cancels/joins the worker, unregistering tailing
and stopping its timer before final persistence. It commits the last fully
consumed eligible fold boundary through history/index/state durability, then
writes the scalar certificate, certifying both surfaces `Clean: true` before
releasing private state. Final transaction or scalar-certificate failure
invalidates both clean claims with contained best-effort removal and directory
sync, and returns `ErrPersistence`; failed invalidation never authorizes trust.
A successful unload stays clean even if the store later stops uncleanly with
another conversation loaded. `Shutdown() error` closes admission and joins all
remaining workers, certifying them independently. It preserves already-unloaded
certificates and never folds history appended while unloaded; the next load
replays those appends. One final write failure leaves that conversation
uncertified and returns `ErrPersistence` after healthy workers are attempted;
those workers can retain their epochs on reopen. Failed/incomplete recovery or
parent-context cancellation cannot authorize clean certification, even when
racing retirement. Repeated/concurrent lifecycle calls preserve their results.
See [isolation and lifecycle](thread-package-background-store-lifecycle.md#isolation-and-lifecycle).

[ADR 045](../decisions/045-thread-index-over-history.md) defines resumable private
fold checkpoints and a persistent index. Reference folds now provide the in-memory
`Fold.ExportCheckpoint` API: detached, separately JSON-encodable `CheckpointState`
and creation-ordered `[]CheckpointItem` metadata/recipes, including hidden,
unresolved and delivery-suppressed internal rows. Export runs at a completed
`Feed` boundary under serialized ownership without history reads or rendering.
See the [export contract](thread-package-main-thread-folding-validation-checkpoints.md#export-import-and-ownership)
for schema/rules versions, completeness and shared identities, and the
[encoded-size measurements](thread-package-main-thread-folding-validation-retention.md#continuation-retention-and-accounting). `ImportCheckpoint`
restores in-memory reference folds; synthetic and committed-recording proofs
cover resume equivalence. The package reader, durable transaction and worker
cadence add persisted round-trip, interruption/retry and final-certification
proofs. Store validates the readable bound and stored checksum at V before
resuming only the suffix; scalar certificates do not resume folding. Callers
still finish producer draining and tail catch-up before `Store.Shutdown`.

### Catch-up continuity

Epoch reuse establishes compatible item identity; retained change-range
continuity needs separate proof. `Store.Observe` acquires a detached complete
baseline, and `Store.Changes` proves a range only from retained publication
boundaries in the current worker, limited to 64 batches and 1 MiB of JSON-encoded
changes. Retained text suffixes have detached storage so they cannot pin an
accumulated message beyond that accounting. Unload/reload,
retry and a new store lifetime retain no old batches, even when recovery
preserves the epoch. Missing live ranges or a different epoch explicitly return
`BaselineRequired`; the daemon retires affected consumers and acquires a new
publisher boundary without broadcasting historical items. Clients recover
explicitly through bounded catch-up or a newest window after reset, with older
pages as needed; `Store.Observe` remains a complete-baseline API for other callers.

`Store.CatchUp` proves reconnect continuity separately from committed history
and the current fold. Clean epoch reuse supports persisted nonzero versions
within `MaxCatchUpGap = 1024` history IDs only after recovery becomes usable
and the requested range passes coverage/suppression checks. Compatible
unload/reload offers the same continuity from restored private facts and suffix
folding without retained live batches; epoch equality alone remains insufficient.
Saved versions need not coincide with publication boundaries. Catch-up indexes
remain disposable query data rebuilt on every load/retry from the validated fold. Checkpoints preserve touches, current/ever-public membership,
suppression and unproven-gap watermarks; suffix feeds extend them. The scalar
certificate establishes no range. Dirty/fallback recovery rotates the epoch,
requiring reset regardless of item equality. Coverage, gap and suppression rules
still apply after clean recovery, including when no suffix was fed.

Checkpoint recovery restores public touches and appearances; suffix/full replay
records them entry by entry, independently of replay chunks. A public item
receives a touch at the current consumed version when it has no retained touch or its `Rev` matches that entry. A child's first
public appearance may follow its final `Rev`, so revision-only selection loses
it after restart. Withdrawal, including claimed delivery-row suppression, deletes
the row's retained touch and advances the suppression boundary without lowering
`LastShownVersion`. Changing `Shown` to false or dropping a queued send retains
its public row and does not itself require reset. Reading all surviving history
also cannot establish continuity across missing IDs; the greatest missing ID
sets a floor for usable nonzero catch-up. Suppression of an earlier public
projection requires reset because clients cannot remove it, while hidden/dropped
rows still belong to `Fold.Items`. See the
[bounded query contract](thread-package-background-store-queries.md#bounded-queries)
for full-state/active/parent closure, reset metadata and zero-version windows,
and the
[observation contract](thread-package-background-store-replay.md#loading-and-replay-readiness)
for bounded retention and linked delivery suppression.

The compact-v1 projection's catch-up and live progress
(`Store.CompactCatchUp`/`Store.CompactChanges`, see
[compact catch-up and live progress](thread-package-background-store-queries.md#compact-catch-up-and-live-progress))
keep their own evidence rather than borrowing this complete-view proof.
`queryIndex.compactFrom` and `compactLost` are scoped to roots alone, distinct
from this section's `unprovenThrough`/`suppressed` watermarks, though both
pairs share the same `coveredThrough` index-current-with-snapshot check.
A same-epoch reload or rebuild advances `compactFrom` to its resume version
even though the epoch, and this section's complete-view continuity, are
unaffected: a compact reader sitting at an older version resets while a
complete reader at the same version and epoch can stay usable. Epoch reuse
establishes compatible item identity, but recovering either projection's
continuity needs its own separate proof — compact evidence is never replaced
by equal-epoch recovery, complete-thread certification, retained complete
changes, or a successful `ChildPage` expansion, including a clean reload
whose epoch is unchanged but whose compact evidence is absent. Compact
bounds and evidence certify compact root coverage only; they never
substitute for complete-thread certification above or for a child page's own
completion (`Store.ChildPage`, under
[revision-scoped direct-child summaries](thread-package-background-store-queries.md#revision-scoped-direct-child-summaries)).

### Send now before consumption (#3508)

A `send_written` fact advances a queued accepted user item to `written` after
the daemon successfully writes Send now input. It preserves identity, content
and acceptance order and stays active until echo/idle placement supplies the
linked message and final `send_delivered` fact. It grants no queue action and
claims no consumption or final order. Duplicate writes are inert; delivered or
lost items cannot become written again. A written item can become lost after
restart, but a later removed/give-up fact cannot hide input already written.
