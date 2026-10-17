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
user/assistant `Summary`. Worker progress and certificates remain scalar-only.
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
still requires the full replay validation below.

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
epoch reuse and fallback remain with Store restoration in #3337.

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
These checks do not provide descriptor-relative atomicity. The transaction
neither writes `thread-certificate.json` nor compacts or removes old generations.
Scheduling, change tracking and final Store checkpoint certification belong to
[#3501](https://github.com/pyrycode/pyrycode/issues/3501).

### Store replay and public touches

`Store` loads and retries still reconstruct a fresh fold from entry 1 of
readable history with bounded `ForwardReader.Walk` chunks. Eligible metadata
retains its epoch only if replay reaches exactly V and both the consumed entry's
checksum and reconstructed last-shown watermark match the record. Replay continues through the surviving
readable bound before usability; cached fields never seed the fold.
**Visible items are insufficient continuation state:** `Fold.Items` omits
unresolved children, pending reports and private joins. Full replay restores
unfinished text, queued sends and that hidden evidence before usability. The
same reader then tails history, consuming commits during recovery/handoff once
in order and converging on later commits through the supplied history store's
notifications. See [loading and replay readiness](thread-package-background-store-replay.md#loading-and-replay-readiness).

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

Replay also reconstructs `Snapshot.LastShownVersion` entry by entry, independent
of replay chunking. It is the history entry ID that most recently made a shown
item first appear in `Fold.Items`, after private joins resolve, or appended
nonempty message text to an already publishable shown item. An unresolved child
counts only when it first becomes publishable. Hidden additions, empty appends,
status/activity/visibility-only changes, delivery/order-only changes, live state
and malformed/unsupported/foreign entries do not raise it. Later revisions and
hiding or dropping a row never replace or lower the earlier watermark.

**Final item revisions cannot reconstruct unread state.** A queued send can
first appear shown, then be dropped with a newer `Rev` and `Shown == false`;
the earlier shown watermark still applies. Every load/retry reconstructs it
from history rather than the cached watermark or the maximum final shown `Rev`.
A usable empty conversation has both `Version` and `LastShownVersion` zero.
Consumers determine unread with `LastShownVersion > ReadUpTo`, using the
existing single read mark in the same history-ID space; the fold/store does
not persist read marks. See
[ADR 042's read marks](../decisions/042-daemon-built-thread.md#sessions-agents-messages-read-marks).

### Epoch recovery and scalar certificate lifecycle

Each usable `Snapshot.Epoch` is a 128-bit random identifier encoded as 32 hex
characters, separate from deterministic item equality. Reuse requires this
conversation's compatible clean certificate and matching checksum/watermark at
exactly V in readable history. Missing, corrupt, dirty or incompatible evidence,
invalid fields, mismatches or V beyond surviving history require a new epoch.
Other conversations' recovery and retirement cannot authorize or invalidate it.
Folding semantics changes must increment `foldingRules`.

Before replay, every successful load/retry replaces the certificate with
`Clean: false`, using the candidate epoch/progress or a fresh epoch at zero.
The temporary file's sync/close, atomic replacement and containing-directory
sync must all succeed before usability. Otherwise a machine crash could restore
an older clean claim over lost published history. Replay can still reject the
candidate and rotate its epoch in memory; the disk record remains dirty until
retirement. Dirty-write failure withholds usability.

**Version bounds and item equality cannot detect replaced tail entries.** A lost
unsupported entry can be replaced at the same ID without changing folded items.
`TestStoreCacheReplacedTail` deliberately preserves item equality, requiring the
checksum mismatch to rotate the epoch. `TestStoreCacheChecksumAtVersion` checks
the saved V rather than a newer replay bound, so ordinary later appends retain
an eligible epoch.

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
and full replay through the readable bound succeed. Missing or invalid evidence
rebuilds from history; genuinely missing or empty history gives an empty usable
view at version zero. Unreadable or incomplete history never yields a cache-only
view. History/certificate I/O, containment and unsafe-leaf failures leave
`StateUnavailable` with generic `ErrUnavailable`; repair followed by `Retry`
creates a new reader and fold. Lifecycle persistence failures retain
`ErrPersistence`.

Publication, active-to-idle transitions and elapsed intervals write no
certificates. There is no checkpoint timer or shared run/coordinator/recovery
marker state. Each new successful load/retry writes one dirty certificate; each
successful joined retirement writes one clean certificate, even at unchanged V.
Tail items, consumed version, `LastShownVersion` and change notifications publish
together in memory. Publication capture shares unchanged immutable item/content
storage and compares only final affected public rows. Retirement evidence pairs
the checksum with the version and watermark of the last successfully folded and
captured progress; a failed or interrupted chunk cannot advance it. Public
snapshots, observations and queries still detach their results; see
[snapshots and retry](thread-package-background-store-replay.md#snapshots-and-retry).
After an unclean stop, full replay restores pending child/report evidence and
subsequent tail parity, while the epoch rotates for each still-loaded conversation.

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

`Unload` marks retirement, cancels/joins the worker and writes `Clean: true` at
its last complete progress, unregistering tailing and releasing private state.
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

[ADR 045](../decisions/045-thread-index-over-history.md) proposes resumable private
fold checkpoints and a persistent index. Reference folds now provide the in-memory
`Fold.ExportCheckpoint` API: detached, separately JSON-encodable `CheckpointState`
and creation-ordered `[]CheckpointItem` metadata/recipes, including hidden,
unresolved and delivery-suppressed internal rows. Export runs at a completed
`Feed` boundary under serialized ownership without history reads or rendering.
See the [export contract](thread-package-main-thread-folding-validation-checkpoints.md#export-import-and-ownership)
for schema/rules versions, completeness and shared identities, and the
[encoded-size measurements](thread-package-main-thread-folding-validation-retention.md#continuation-retention-and-accounting). `ImportCheckpoint`
restores in-memory reference folds; synthetic and committed-recording proofs
cover resume equivalence. The package reader and durable transaction add persisted round-trip and
interruption/retry proofs. Scheduling/final Store certification (#3501) and Store
checkpoint loading/history-bound validation (#3337) remain follow-ups under #3336. Store loading still replays from entry 1; the
existing scalar certificates neither resume folding nor implement the proposed
checkpoint schedule. Callers still finish
producer draining and tail catch-up before `Store.Shutdown`.

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
unload/reload offers the same reconstructed continuity without retained live
batches; epoch equality alone remains insufficient. Saved versions need not
coincide with publication boundaries. Catch-up evidence and indexes remain
disposable, history-derived query data, reconstructed on every load/retry;
compact recovery evidence never establishes the range or seeds continuation.

Recovery records public touches and appearances entry by entry, independently
of replay chunks. A public item receives a touch at the current consumed version
when it has no retained touch or its `Rev` matches that entry. A child's first
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

### Send now before consumption (#3508)

A `send_written` fact advances a queued accepted user item to `written` after
the daemon successfully writes Send now input. It preserves identity, content
and acceptance order and stays active until echo/idle placement supplies the
linked message and final `send_delivered` fact. It grants no queue action and
claims no consumption or final order. Duplicate writes are inert; delivered or
lost items cannot become written again. A written item can become lost after
restart, but a later removed/give-up fact cannot hide input already written.
