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
the conversation or supplying partial items. Such views have no usable
watermark; registered usable zero is authoritative. The no-relay composition
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

Each existing conversation coordinator owns one publication cursor. It starts
from a complete usable `Store.Observe` baseline, sending full additions to
eligible peers present at publication, then follows only proven `Store.Changes`
ranges. It does not initialize connections admitted after that baseline.
`BaselineRequired`, epoch change, a withdrawn usable view or encoding failure
retires prior consumers through `RetireLiveState`, forgets the cursor and
reacquires a usable baseline. Same-epoch reload cannot establish retained-range
continuity; full additions over stale peer state cannot repair a suppressed row.
See [observation readiness](thread-package-background-store.md#loading-and-replay-readiness)
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

A caught-up usable fold unloads only when `Snapshot.Active` is false and
`shadowQuiescent` finds producer/transition processing quiescent. Active
background or unresolved private work, queue entries, channel writes, pending
runtime boundaries, unconsumed stream output and placement commands prevent
retirement. Idle sleep must publish and fold its closure/divider facts first.
Unload releases items, private joins and pending reports while coordination
retains the consumed version. Later commits reload through cache validation and
fresh log replay, including a direct post or an append racing `Unload`; cached
public items never seed continuation state. Capture the consumed snapshot
boundary before publication can wait on delivery, then retain that boundary at
unload. Reading a newer boundary after held delivery can mark an unpublished
commit consumed and prevent its reload. `TestDaemonThreadIdleCommit` exercises
this race; reload must still prove the publication range or retire consumers.

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
`thread-cache.json` and `thread-recovery.json` beside the history segments.
The cache stores detached items, consumed version, schema and folding-rule
revisions, epoch, complete-progress flag and run/coordinator references. The
recovery marker names the store lifetime that opened the conversation; its
coordinator conversation holds `thread-run-<token>.json`. `EnsureLogDir` can
create an empty contained directory without fabricating history entries.

Every load or retry reconstructs a fresh fold from readable history with bounded
`ForwardReader.Walk` chunks. Cached items are compared with fresh replay at their
consumed version before their epoch can be retained; they never seed the fold.
**Visible items are insufficient continuation state:** `Fold.Items` omits
unresolved children, pending reports and private joins. Full replay restores
unfinished text, queued sends and that hidden evidence before usability. The
same reader then tails history, consuming commits during recovery/handoff once
in order and converging on later commits through the supplied history store's
notifications. See [loading and replay readiness](thread-package-background-store.md#loading-and-replay-readiness).

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
from history rather than cached items or the maximum final shown `Rev`.
A usable empty conversation has both `Version` and `LastShownVersion` zero.
Consumers determine unread with `LastShownVersion > ReadUpTo`, using the
existing single read mark in the same history-ID space; the fold/store does
not persist read marks. See
[ADR 042's read marks](../decisions/042-daemon-built-thread.md#sessions-agents-messages-read-marks).

Each usable `Snapshot.Epoch` is a 128-bit random identifier encoded as 32 hex
characters, separate from deterministic item equality. Across store lifetimes,
reuse requires a compatible complete cache, matching recovery marker, completed
run certificate and matching replayed items. Changed folding rules, an unclean
stop, missing/corrupt/incompatible metadata, mismatched items or a cache ahead of
surviving history require a new epoch. Before replay/publication, the store
establishes an incomplete run certificate and replaces the conversation's
recovery marker, invalidating its previous clean claim. Failure leaves the view
unavailable. Folding semantics changes must increment `foldingRules`.

Choose the revision against the current merged rules: concurrent semantic changes
can otherwise select the same revision. A missing-field cache regression can
still pass because replayed content differs, concealing that collision.
`TestStoreQueueHandleCacheRules` also supplies old-rule caches with matching
rows and requires a new epoch, proving rule incompatibility independently of
content mismatch. Accepted-send handle semantics use rules revision 3; caches
from revisions 1 or 2 rebuild from raw history and cannot hide recorded handles.

Recovery remains `StateRebuilding` until replay and a complete checkpoint
succeed. Missing or unusable cache data rebuilds from history; genuinely missing
or empty history gives an empty usable view at version zero. Unreadable history
never yields a cache-only view. History/cache I/O, containment and unsafe-leaf
failures instead leave `StateUnavailable` with generic `ErrUnavailable`;
repair followed by `Retry` creates a new reader and fold. Completed progress is
checkpointed before the first usable recovery publication. Tail items, consumed
version, `LastShownVersion` and change notifications publish without waiting for
the checkpoint deadline, so a usable view may be newer than its saved cache.
A failed initial or scheduled checkpoint withdraws usability with generic
`ErrUnavailable`; lifecycle results retain `ErrPersistence` and prevent clean
certification.

Dirty progress means a fully folded version newer than the saved version,
including entries that change no public item. Each conversation's deadline is
30 seconds after its last successful checkpoint. Further appends never postpone
it: dirty progress flushes at that deadline even if appends stop, or immediately
when it first becomes dirty after the deadline has passed. Unchanged progress
arms no timer and causes no periodic replacement. Active-to-idle, unload and
shutdown flush completed dirty progress as exceptions to the interval bound;
successful lifecycle checkpoints start a new interval. Already-inactive
publications follow the bounded schedule. Activity includes private unresolved
work, as described under
[isolation and lifecycle](thread-package-background-store.md#isolation-and-lifecycle).
After an unclean stop, committed history newer than the cache is replayed along
with earlier pending child/report evidence; items, revisions, consumed version
and last-shown watermark converge with full replay, while the epoch rotates.

Writes use synced, closed `0600` temporary files and atomic replacement; reads
use no-follow regular-file checks. Each operation validates IDs and re-resolves history's
exact directory containment, rejecting sibling/outside redirects and symlink or
nonregular leaves. Errors contain no content, payloads or host paths, and cache
operations preserve history bytes and its existing append durability contract.

`Unload` checkpoints completed dirty progress where available, cancels/joins the
worker, unregisters tailing and releases items and private fold state, retaining
only scalar lifecycle records. Compatible unload/reload in one store lifetime
preserves the epoch even though that run's certificate remains incomplete;
unload never certifies a clean exit. `Shutdown() error` closes admission, joins
all workers, revalidates/replaces final checkpoints for loaded and previously
unloaded conversations, then completes the shared run certificate last. Only
successful completion permits epoch reuse in a later lifetime. Failed replay,
cancellation before recovery completes or failed/interrupted checkpoint or final
certificate writes cannot certify clean shutdown. Lifecycle methods report
failures explicitly; repeated/concurrent shutdown callers share one result.
See [isolation and lifecycle](thread-package-background-store.md#isolation-and-lifecycle).

Epoch reuse establishes compatible item identity; retained change-range
continuity needs separate proof. `Store.Observe` acquires a detached complete
baseline, and `Store.Changes` proves a range only from retained publication
boundaries in the current worker, limited to 64 batches and 1 MiB. Unload/reload,
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
cached items never establish the range or seed continuation.

Recovery records public touches and appearances entry by entry, independently
of replay chunks. A child's first public appearance may follow its final `Rev`,
so revision-only selection loses it after restart. Reading all surviving history
also cannot establish continuity across missing IDs; the greatest missing ID
sets a floor for usable nonzero catch-up. Suppression of an earlier public
projection requires reset because clients cannot remove it, while hidden/dropped
rows still belong to `Fold.Items`. See the
[bounded query contract](thread-package-background-store.md#bounded-queries)
for full-state/active/parent closure, reset metadata and zero-version windows,
and the
[observation contract](thread-package-background-store.md#loading-and-replay-readiness)
for bounded retention and linked delivery suppression.
