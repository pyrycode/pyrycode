# `internal/thread` — shadow lifecycle and evidence

[Thread package overview](thread-package.md).

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
that every conversation has completed replay. Authenticated queries load cold or
idle registered conversations and wait cancelably for readiness; unavailable
views fail with `thread.query_failed` without certifying partial items.
`ThreadLastShownVersion` checks
registry membership before any certificate access. Loaded usable conversations
read `Store.Status` without certificate I/O or copying a baseline; rebuilding
and unavailable views supply no watermark. Unloaded registered conversations
use `Store.UnloadedLastShownVersion` only when clean, validated certificate
progress exactly equals independently known current raw history progress
(V = H); see [certificate eligibility](thread-package-cache-and-epochs.md#cache-and-epochs). Unregistered or
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
marker certify completion; reset, error, cancellation, disconnect and incomplete
fragments leave the checkpoint unchanged. Live publication racing an unfinished query
can cancel it; retry from the last completed checkpoint to converge. Stable-history idle-reload
fixtures must install the publisher before discovery/initial retirement so its
initial cursor exists before the phone connects. Otherwise first live publication
can cancel the catch-up legitimately, masking the final-response assertion;
`TestDaemonThreadDemandWireAfterIdle` preserves the final, version-1, one-row proof. See
[query/live ordering](../../protocol-mobile.md#querylive-ordering).

Each conversation coordinator owns one publication cursor, distinct from a
client's certified recovery checkpoint.
`daemonThreadPublisher.publish` checks `Store.Status` first; an unchanged usable
epoch/version returns without reading or traversing items/content. Changed
publication follows proven `Store.Changes` ranges. Initial and replacement
acquisition use `Store.Changes` with an empty epoch to capture usable metadata
without copying or broadcasting historical items. Subsequent proven changes
continue live; each client initializes through explicit bounded catch-up and
older paging, including clients already connected at acquisition.
`BaselineRequired`, epoch change, a withdrawn usable view or encoding failure
retires recorded recipients and currently eligible consumers of the affected
conversation through `RetireLiveState`, then forgets and reacquires the boundary.
Tracking only successful live pushes misses clients initialized solely by queries.
Retirement closes the connection before dependent output can escape; existing
interactive/thread and agent eligibility gates still apply. Same-epoch reload
cannot establish retained-range continuity; clients reconnect and explicitly
catch up, or acquire a newest window after reset, rather than receive a full
live inventory. An unchanged coordinator-owned idle checkpoint can survive
unload as described below.
Status is only a hint, not continuity evidence or a transaction across reads.
Wire metadata, changes and cursor advancement must all come from the same
captured `Changes` result. Recheck captured usability and retire
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
Queries enroll the same coordinator even for an already-loaded conversation.
Before query capture, `threadShadow.publishThread` acquires a missing publisher
boundary through a cancelable per-conversation gate shared with following.
Otherwise a query could complete before first publication silently skips an
intervening commit. Preparation leaves an existing cursor to following: forcing
changed publication here could make a query trigger its own live-race refusal.
Never wait for this gate while holding shadow or quiescence mutexes; cancellation
must release a query waiting behind held fanout without waiting for that fanout.
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

`TestDaemonThreadBoundedRecovery` compares 128 and 8333 settled rows: initial and
replacement acquisitions emit zero historical item frames/bytes, while a new
append emits one live item frame. It recovers idle reload, retained-range loss
and epoch replacement through authenticated queries and reconstructs the full
inventory through older pages. Silence alone cannot prove bounded output: wait
for the successful query marker and completed publisher fanout, then drain the
delivery lane with an observable FIFO barrier. `TestDaemonThreadRecoveryCommit`
and `TestDaemonThreadQueryBoundary` hold replay/query capture around commits to
prove recovery-to-live coverage. `TestDaemonThreadPublicationCancellation`
checks cancellation while the publication gate is held.

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
[comparison contract and negative controls](thread-package-main-thread-folding-validation-checkpoints.md#checkpoint-validation-and-resume-proof).
The retained synthetic shell creation, launch-result and turn-end context is
explicitly test-supplied. This offline gate uses the committed frames without
credentials or recapture and adds no live-capture evidence. Existing recorded
semantic assertions and golden expectations remain unchanged. The
[persisted reader and durable transaction](thread-package-main-thread-folding-validation-checkpoints.md#durable-transaction-round-trip-and-fault-proof)
have separate round-trip and interruption/retry proofs. Scheduling/final Store
certification (#3501) and Store startup restoration (#3337) remain follow-ups.

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

