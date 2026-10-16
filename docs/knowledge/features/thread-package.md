# `internal/thread` — conversation-owned history fold

`Fold` builds deterministic items from supplied raw `history.Entry` values for
one conversation. `Store` owns background replay and tailing for independently
loaded conversations. Both implement the history-backed storage design of
[ADR 042](../decisions/042-daemon-built-thread.md). The daemon publishes usable
fold observations and serves bounded catch-up/pages to eligible apps that
negotiate `thread`. Apps initialize each open conversation with an explicit
named catch-up request; handshake and listing do not query items.
Use [raw history and its metadata](history-package-shape.md#shape), including
hidden facts; legacy receipt projections discard facts needed by the fold.

| Topic | Contents |
| --- | --- |
| [Background store](thread-package-background-store.md) | Loading, bounded replay/tail handoff, snapshot readiness, retry, isolation and joined lifecycle. |
| [Main-thread folding](thread-package-main-thread-folding.md) | Item identity/order/version, standalone entries, accepted sends, main work, visibility and recorded provenance. |
| [Agents and background work](thread-package-agents-and-background-work.md) | Agent and shell lifecycles, scoped joins, recovery references, parent repair and offline evidence. |

## Shadow lifecycle and evidence

`runSupervisor` composes `thread.NewStore` with the same `history.Store` used by
interactive emission, session transitions, operator sends/queue facts and direct
channel posts. After `reconcileStartupHistory` and completion of `startRelay`'s
runtime-history construction, `threadShadow.start` schedules registered old logs
in the background without waiting for replay. Construction must finish before
shadow quiescence readers enter: `installRuntimeHistory` assigns
`busy.runtimeSink`, which those readers subsequently treat as immutable.
Folding consumes raw entries, including hidden facts and captured source
provenance; it never attributes old entries through the active session cursor.

Background discovery and per-conversation coordination inspect committed history
versions. This covers conversations created after startup and activity after
unload, including `channelDelivery.deliver`, which bypasses
`appendConversationHistory`. Waking only from that helper would miss direct
posts. Loaded workers use the same reader for replay and committed tailing;
settled items/revisions match full replay through the consumed version.
Replay/cache I/O, publication and joins run outside history writers and producer
gates. Held peer delivery cannot block history commits.

`startRelayV2` installs `installDaemonThreadRelay` after the retained live-state
providers and starts `startDaemonThreadPublisher` before connection admission.
`installDaemonThreadRelay` binds `ThreadCatchUp` and `ThreadPage` directly to
`Store.CatchUp` and `Store.HistoryPage` on the publisher's real store, preserving
the request context, named conversation and complete captured `QueryResult`.
`ThreadReady` requires both queries, the authoritative watermark lookup,
`ThreadLiveState`/`LiveStateCurrent` delivery and a running publisher on that
same store. Missing providers withhold negotiation; readiness does not promise
that every conversation has completed replay. Queries on not-loaded, rebuilding
or unavailable views fail explicitly with `thread.query_failed`, without loading
the conversation or supplying partial items. `ThreadLastShownVersion` checks
registry membership before any certificate access. Loaded usable conversations
read `Store.Status` without certificate I/O or copying a baseline; rebuilding
and unavailable views supply no watermark. Unloaded registered conversations
use `Store.UnloadedLastShownVersion` only when clean, validated certificate
progress exactly equals independently known current raw history progress
(V = H); see [certificate eligibility](#cache-and-epochs). Unregistered or
removed IDs, stale/invalid evidence and unknown progress supply no watermark.
Eligible zero is authoritative, including explicit empty evidence at V = H = 0.
The lookup starts no worker, traverses/copies no items and writes nothing;
unloaded reads open at most one certificate and no history segment, regardless
of history size. This bound applies to `ThreadLastShownVersion`; the list
handler's separate [legacy watermark scan](history-package-watermarks.md#unread-state-uses-a-separate-lazily-recovered-watermark-2954)
can still read history. The no-relay composition
continues folding without wire publication; legacy connections retain their
existing stream and access gates.

Reconnect skips event-ring replay/resync only for negotiated thread peers.
Each open conversation requires explicit catch-up from its last completed
epoch/version, or a zero-version newest window, with explicit older pages as needed.
Only all assembled batches followed by the separate successful `final: true`
marker certify completion; reset, error, disconnect and incomplete fragments
leave the checkpoint unchanged. Live publication racing an unfinished query
can cancel it; retry from the last completed checkpoint to converge. See
[query/live ordering](../../protocol-mobile.md#querylive-ordering).

Each existing conversation coordinator owns one publication cursor.
`daemonThreadPublisher.publish` checks `Store.Status` first; an unchanged usable
epoch/version returns without reading or traversing items/content. Changed
publication follows proven `Store.Changes` ranges. Full `Store.Observe` baseline
reads occur only for initial publication or a required replacement, sending
every item as a full addition to eligible peers present at publication. It does
not initialize connections admitted after that baseline.
`BaselineRequired`, epoch change, a withdrawn usable view or encoding failure
retires prior consumers through `RetireLiveState`, forgets the cursor and
reacquires a usable baseline. Same-epoch reload cannot establish retained-range
continuity; full additions over stale peer state cannot repair a suppressed row.
Status is only a hint, not continuity evidence or a transaction across reads.
Wire metadata, items/changes and cursor advancement must all come from the same
captured `Changes` or `Observe` result. Recheck baseline usability and retire
consumers before replacement even if withdrawal/reload races the status read.
See [observation readiness](thread-package-background-store-replay.md#loading-and-replay-readiness)
and [relay gap quarantine and recipient gates](v2-session-manager-state-machine-capability-negotiation-on-the-handshake.md#thread-delivery-and-summary-projection).

`daemonThreadUpdates` maps exported fold field names through `ThreadItem`'s JSON
tags. Copying `EndedOrder` or `NoChild` directly would produce unusable wire
patches instead of `ended_order` or `no_child`. A combined field/text change
emits replacements first, using `PreviousRev` as `base_rev` and the final `Rev`,
then the suffix with that same final revision as both `base_rev` and `rev`.
Both carry the same consumed version; no intermediate history revision exists.
Every logical update uses `EncodeThreadUpdate`, with consecutive continuation
parts before the next update. Discovery and following each wait at most 50 ms;
store batches coalesce text without an additional timer. Ordered updates flush
before dependent non-text changes, adding at most 100 ms of scheduled waiting
within the total 250 ms publication/coalescing budget. This bounds waiting to
enqueue, not replay, encoding or network delivery time. See the
[wire assembly contract](../../protocol-mobile.md#bounded-thread-encoding-and-assembly).

A caught-up usable fold unloads only when `Status.Active` is false and
`shadowQuiescent` finds producer/transition processing quiescent. Active
background or unresolved private work, queue entries, channel writes, pending
runtime boundaries, unconsumed stream output and placement commands prevent
retirement. Idle sleep must publish and fold its closure/divider facts first.
Unload releases items, private joins and pending reports while coordination
retains the consumed version. Later commits reload through cache validation and
fresh log replay, including a direct post or an append racing `Unload`; cached
public items never seed continuation state. Capture the consumed status
boundary before publication can wait on delivery, then retain that boundary at
unload. Reading a newer boundary after held delivery can mark an unpublished
commit consumed and prevent its reload. `TestDaemonThreadIdleCommit` exercises
this race; reload must still prove the publication range or retire consumers.

Authenticated thread queries own temporary daemon demand: they load cold or
idle registered conversations, wait cancelably for a usable view, and keep it
loaded through bounded selection and rendering. Concurrent queries share the
worker; cancellation releases only the caller's demand. Removal and shutdown
cancel and join all demand. Unknown and unavailable conversations fail closed.
A successful coordinator-owned idle unload retains the publication cursor while
the cache is absent or rebuilding, so reading an unchanged checkpoint does not
disconnect the peer. Changed publications must still prove continuity through
`Changes`; an actual gap or failure retains the normal consumer retirement.

`dropRingOnConversationDelete` retains the registry's single removal observer
for explicit deletion and sweep, including with the relay disabled. It preserves
ring/reply-suggestion cleanup, tombstones the ID, joins shadow coordination and
unloads the store worker. Registry membership checks and tombstones prevent late
commits, callbacks or discovery from reopening removed conversations.
Publication operations follow both conversation cancellation and publisher
retirement. Publisher stop withdraws readiness, seals admission, cancels and
joins admitted operations before freeing the installation slot; freeing it
before joining permits duplicate publishers. `TestDaemonThreadRetirement`
checks held delivery, removal, shutdown and duplicate installation.

Shutdown seals producer admission and joins/drains writers before final history
versions are established. Shadow contexts are detached from daemon cancellation
so producer drains and error-return cleanup can commit their final facts.
[`Server.Seal`](control-plane-server-and-deadlines.md#handler-admission-and-ownership)
joins admitted control writers while the listener retains ownership through
final cache persistence. Shadow shutdown discovers final new activity, stops
discovery/admission, joins each remaining coordinator after final catch-up, then
calls `Store.Shutdown`. History/fold failures end final waiting; they cannot
certify a clean cache exit even if persistence succeeds. Final discovery must
also establish every remaining registered conversation's boundary: checking only
existing coordinators would overlook a conversation that failed before one was
created. `thread_shadow.clean_exit` requires both complete final folding and
successful `Store.Shutdown` persistence. Fixed failure reasons and validated IDs
are the diagnostic boundary; content, payloads and host paths are excluded.

`TestDaemonThreadPublication` decrypts simultaneous authenticated thread and
legacy peers, reconstructing complete fold items after additions, field/text
changes, dropped sends, dividers and channel posts. It checks combined
replacement-before-append delivery, both base revisions, the waiting bound,
escaping-heavy continuation assembly and retained current readings, while
legacy traffic keeps its expected shape. Mapper-only assertions would miss
wire ordering or recipient projection errors. `TestDaemonThreadReadiness`,
`TestDaemonThreadUnusable` and `TestDaemonThreadContinuity` cover usable zero,
missing providers/views and lost ranges. `TestDaemonThreadQueriesReadiness`
checks complete query installation, request cancellation and nonloading reads.
`TestDaemonThreadQueriesRecovery` checks authenticated clean-reopen catch-up,
old-revision first appearances, escaped multiframe replies, page metadata and
simultaneous legacy raw history. `TestDaemonThreadQueriesReset` checks explicit
nonusable failure, reset evidence and the 1024/1025 gap boundary.
`TestDaemonThreadQueriesBounded` exercises installed queries over 36,000
committed entries after recovery, measuring preparation separately from adapter
allocations. `TestDaemonThreadQueriesJoin` reconstructs full current states
through race refusal, successful retry and subsequent real publisher updates.

`TestDaemonThreadMetadataAllocations` checks unchanged publication and the
installed list lookup on a loaded long thread. Pair allocation checks with
`TestThreadMetadataReadSources`: allocation counts alone stay green for an item
traversal that allocates nothing. Source checks must select
`daemonThreadPublisher.publish` by receiver; `threadShadow.publish` shares its
method name and only delegates. `TestDaemonThreadStatusInterleaving` forces
advancement, withdrawal and same-epoch reload after the status hint, checking
captured output/cursors and retirement before replacement.

`TestDaemonThreadLastShownCertificates` covers certificate eligibility and
warm/cold progress; `TestDaemonThreadLastShownLifecycle` checks hidden append
invalidation and restoration after load/fold and joined unload.
`TestDaemonThreadLastShownIO` observes the installed callback's actual file
operations on Linux. Adjacent identical inotify open events coalesce, so an
open-only watch can accept two reads as one. `IN_CLOSE_NOWRITE` separates the
opens; a deliberate two-open control must fail the same one-open bound used
for callback checks. Portable worker/copy/traversal guards remain separate
from this filesystem observation.

`testThreadMetadataFixture`, shared with `BenchmarkDaemonThreadMetadata`, owns
an independent worker context for `Store.Load`. `testing.TB.Context` is cancelled
before cleanup: using it as the load parent can let both allocation subtests
pass while parent cleanup fails clean certification with `ErrPersistence`.
Register cleanup before loading; join `Store.Shutdown`, report its error, then
cancel the owned context with a defer inside cleanup so fixture failures also
release it. Keep the load parent alive through the join to preserve the
[retirement certification contract](thread-package-background-store-lifecycle.md#isolation-and-lifecycle).

Wire lifecycle tests must read the live acceptance before completing same-version
catch-up, which can suppress duplicate publication. A delivered message and its
outcome can reach separate fold batches; suppressing the earlier public message
then requires a baseline and retires the peer. `TestQueuedSendHandleActions`
checks retained-range continuity, asserting the live terminal patch when usable
or authenticated replacement-peer catch-up when retirement is required. Waiting
only for a terminal patch would mistake valid retirement for missing content.

Nil or failed shadow storage leaves legacy traffic operational.
`TestThreadShadowLegacy` checks an authenticated Noise peer through
the production session manager, comparing live frames, reconnect replay, history
requests and negotiated capabilities with enabled, nil and failed storage.
Fake broadcaster capture or direct ring/pager reads alone miss those client
boundaries. `TestThreadShadowLongReplay` pauses an actual 36,000-entry refold
while another conversation appends and publishes, then checks full-replay
equality; `TestThreadShadowRemoval` pauses actual replay/tail work to prove joined
retirement and continued progress elsewhere. These are deterministic wiring
proofs; authenticated history evidence supplements them before protocol migration.

The retained pair is
[`internal/e2e/realclaude/testdata/thread_shadow_history.json`](../../../internal/e2e/realclaude/testdata/thread_shadow_history.json)
(raw durable history, including hidden facts and source metadata) and
[`internal/e2e/realclaude/testdata/thread_shadow_expected.json`](../../../internal/e2e/realclaude/testdata/thread_shadow_expected.json)
(observed legacy envelopes and exact checkpoint items/revisions).
`TestThreadShadowHistoryCapture` drives the production daemon with a connected
authenticated legacy client: marked text/Bash/text, ordinary acceptance during
busy work followed by delivery and reply, an Agent child's parent-owned text,
then reset during a third active Bash turn. Missing scenarios fail capture.

| Checkpoint | Retained history version | Evidence |
| --- | --- | --- |
| `before_delivery` | 14 | Accepted queued item keeps creating ID 14, status `queued` and order zero. |
| `main_complete` | 42 | Main text/tool/text and child text are retained; main foreground work is settled, while the accepted message is still queued. |
| `after_delivery` | 48 | Linked delivery outcome keeps item ID 14 and places it at delivered message order 47. |
| `session_closed` | 85 | Affected active work settles before the visible raw session divider, with closure content and ownership retained. |

Controlled markers, exact raw-to-legacy text equality, raw acceptance/delivery
payloads and authenticated sender fields independently constrain the expected
rows. Raw creation, first-terminal, task-link and source/lifetime facts check
complete text/tool/Agent content, status, activity and parent ownership. Unique
creating IDs, valid revisions, nonzero placement and older existing parents are
checked at each prefix. Main completion does not require unrelated background
agents to finish. Closure must retain a settled non-success outcome before the
divider; Claude's cancellation may record `failed` before daemon interruption,
so the checkpoint name cannot imply `interrupted`.

`TestThreadShadowRetainedEvidence` runs without credentials in default builds.
Each checkpoint restores the sanitized raw prefix into history, loads a fresh
`Store`, compares its exact-version snapshot with both the retained baseline and
fresh full `Fold` replay, and joins the worker with `Shutdown`. Equality alone
can preserve the same fold error: independent raw/legacy checks and synthetic
mutation controls must also reject lost content, incorrect lifecycle and missing
parents, including on already completed children.

Both artifacts identify capture `shadow-1791611968246776504`, Claude Code
2.1.280, daemon source commit `4744817f5f8628fce7a1d4187c854cbff60d7743` and the
same SHA-256 over 86 retained raw entries. Their `GateReport` pins the
[counted authenticated capture report](https://github.com/pyrycode/pyrycode/issues/3068#issuecomment-6094441127):
`before_delivery`, `main_complete`, `after_delivery`, `session_closed` and
`scenario_evidence` each executed once; all five and their parent passed, with
zero failures or skips. Consistent sanitization preserves IDs, order, joins and
source provenance; complete serialized bytes pass credential/host-path denial.
The later [full live-suite report](https://github.com/pyrycode/pyrycode/issues/3068#issuecomment-6094643687)
counts 1932 passed, zero failed and 27 skipped; with both artifacts present,
the capture test validates/replays the pair rather than recording a new conversation.

`TestThreadAgentRecordedReplay` and
`TestThreadShellRecordedReplay` remain separate offline parser-to-history proofs.
Their recorded frames come from `parent_tool_use_v2.1.259.json`,
`task_notification_v2.1.259.json` and `roster_after_finish_v2.1.280.json`.
The shell proof adds explicitly synthetic creation/result/turn-end context;
those additions and in-memory rejection controls are never live-history evidence.
The earlier Agent recording has no forwarded child text or nesting-depth proof;
the retained pair adds observed child text, without claiming arbitrary nesting
coverage. See [recorded replay limits](thread-package-agents-and-background-work.md#agent-validation-and-testing)
and [capture/recovery requirements](e2e-realclaude.md#retained-shadow-history-evidence).

### Reusable fold regression corpus

`testFoldCorpus` and `testFoldGoldenCheck` are same-package `internal/thread`
test helpers for the [agent](https://github.com/pyrycode/pyrycode/issues/3351),
[visibility](https://github.com/pyrycode/pyrycode/issues/3352) and
[text](https://github.com/pyrycode/pyrycode/issues/3353) optimizations, and the
[publication](https://github.com/pyrycode/pyrycode/issues/3328) and
[rendering](https://github.com/pyrycode/pyrycode/issues/3333) work that consumes
their evidence. `fold-scenarios-v1/100-entries` repeats fixed 100-entry scenarios;
requesting a larger count preserves the smaller prefix and adds scenarios with
distinct turn/call/task identities and bounded payloads. Each scenario has its
own producer lifetime, with multiple calls/tasks inside its agent group. Sharing
one lifetime across repetitions made cross-scenario group recomputation dominate
the gate; separate lifetimes keep the corpus practical while retaining group
joins and long-history visibility work.

The scenarios retain genuine Agent/Task child work, late links and parent
enrichment, nested closure/reopening, mapped/durable companions, queued-send
delivery/suppression and escaped/Unicode deltas. Bash/local_bash remain ordinary
tools and cannot parent children. `TestFoldCorpus` independently checks lifecycle
boundaries, prefix stability and bounded payloads; digest equality supplements
these assertions.

`TestFoldBaselines` pins generated checkpoints at versions 6, 10, 11, 12, 14,
15, 18, 21, 22, 23, 28, 33, 100, 1,000 and 30,000 in
[`corpus_golden.json`](../../../internal/thread/testdata/corpus_golden.json).
Early checkpoints surround child appearance, closure/reopening, links, endings
and send delivery/suppression; the sparse long-history checkpoints avoid testing
every split point as `testMainReplay` does for small fixtures. It also consumes
the unchanged raw shadow history through checkpoints in
[`shadow_golden.json`](../../../internal/thread/testdata/shadow_golden.json).
`cmd/pyry.TestThreadRecordedBaselines` derives the three recordings through the
same `testRecordedFrames` helper as the independent recorded replay tests and
retains every emitted version. All checkpoints use fresh whole-prefix feeds,
deterministic chunks cycling through 7/31/113 entries and single-entry feeds.
These default-build offline gates require no credentials or recapture; preserve
the separate provenance and raw/legacy semantic assertions above. See
[baseline writing and digest conventions](thread-package-agents-and-background-work.md#agent-validation-and-testing).
Canonical item hashing streams individually encoded rows in sorted field order.
Only their Content requires generic JSON normalization, avoiding a second
complete metadata object tree. `TestFoldItemsDigestCanonical` compares this
encoding with the independent generic encoder for every Item field, nested
content, nil and empty slices. Existing golden bytes and the separate exact
Content-byte hash remain unchanged.

`TestThreadRecordedBaselines` additionally proves checkpoint resume equivalence
for all three committed histories: `parent_tool_use_v2.1.259.json`,
`task_notification_v2.1.259.json` and `roster_after_finish_v2.1.280.json`.
`testRecordedCheckpointResume` covers every emitted entry cut from zero through
full, including hidden facts and companion emissions within frames, then compares
immediately and after every suffix entry with uninterrupted reference folding.
Both export parts cross JSON separately; import uses the same owner and a
prefix-limited history source, makes no history lookups and receives only suffix
feeds. Direct comparisons retain exact Content bytes, every Item field/revision,
the complete internal metadata/recipe inventory and exported continuation state.
Rendered equality alone misses lost ever-published membership; JSON-normalized
digests can conceal Content byte differences. See the
[comparison contract and negative controls](thread-package-main-thread-folding-validation.md#validation-and-testing).
The retained synthetic shell creation, launch-result and turn-end context is
explicitly test-supplied. This offline gate uses the committed frames without
credentials or recapture and adds no live-capture evidence. Existing recorded
semantic assertions and golden expectations remain unchanged; checkpoint
persistence and Store startup loading remain separate work (#3336/#3337).

### Full-feed benchmark

`BenchmarkFoldCorpus30000` measures fresh fold construction and complete
`Fold.Feed` for 30,000 generated entries per iteration. Generation, snapshots
and digests are outside measured time/allocation. Use a single measured iteration:

```bash
go test ./internal/thread -run '^$' -bench '^BenchmarkFoldCorpus30000$' -benchtime=1x -count=1 -timeout 12m
```

The [retained pre-optimization baseline](https://github.com/pyrycode/pyrycode/pull/3374)
measured **19.271536560 s**, **4,499,987,800 B/op** and
**14,158,715 allocs/op**, producing **4,200 items from 30,000 entries**.
It used Go **1.26.9** in a **Linux/amd64 container**, an **Intel Core i7-3770
@ 3.40 GHz** and **8 logical CPUs**. Production source was
`a920b90cc6bfd2f1fb8461e9f85ce6f19934afa6` (including the agent-only parent,
accepted-send handle and targeted child-repair fixes); the committed test
harness was `92c58fe5a596c5ed056c8216caf2d7baa8412565`. This was one measured
iteration with other builder checks running concurrently on the same machine;
retain that load qualification when comparing later measurements.

## Cache and epochs

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
[fold/source heap proof](thread-package-main-thread-folding-validation.md#validation-and-testing)
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

In-memory reference folds support `ImportCheckpoint` from the two JSON export
parts, with a caller-supplied `HistorySource`, without prefix replay or history
I/O. Restoration preserves the complete internal inventory and continuation
graph; it does not restore a Store epoch or establish certificate eligibility.
See [checkpoint validation and resume proof](thread-package-main-thread-folding-validation.md#validation-and-testing).
Checkpoint persistence remains in #3336 and Store loading in #3337 under
[ADR 045](../decisions/045-thread-index-over-history.md).

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

Only after the first durable certificate write does `removeLegacy` remove this
conversation's regular `thread-cache.json`, `thread-recovery.json` and allow-listed
`thread-run-<token>.json` leaves. It rechecks containment, rejects redirects and
special files without following them, preserves history/unrelated files and
syncs the directory after cleanup. Cleanup failure withholds usability.
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
See the [export contract and encoded-size measurements](thread-package-main-thread-folding-validation.md#validation-and-testing)
for schema/rules versions, completeness and shared identities. `ImportCheckpoint`
restores in-memory reference folds; synthetic and committed-recording proofs
cover resume equivalence. File persistence (#3336) and Store checkpoint loading
(#3337) remain follow-ups. Store loading still replays from entry 1; the existing
scalar certificates neither resume
folding nor implement the proposed checkpoint schedule. Callers still finish
producer draining and tail catch-up before `Store.Shutdown`.

Epoch reuse establishes compatible item identity; retained change-range
continuity needs separate proof. `Store.Observe` acquires a detached complete
baseline, and `Store.Changes` proves a range only from retained publication
boundaries in the current worker, limited to 64 batches and 1 MiB of JSON-encoded
changes. Retained text suffixes have detached storage so they cannot pin an
accumulated message beyond that accounting. Unload/reload,
retry and a new store lifetime retain no old batches, even when recovery
preserves the epoch. Missing live ranges or a different epoch explicitly return
`BaselineRequired`; live consumers acquire a fresh complete baseline.

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
