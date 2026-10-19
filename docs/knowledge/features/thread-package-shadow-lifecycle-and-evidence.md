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
`installDaemonThreadRelay` binds `ThreadCatchUp`/`ThreadPage` to
`Store.CatchUp`/`Store.HistoryPage` and `ThreadToolBody`/`ThreadChildren` to
`Store.ToolBody`/`Store.ChildPage` on the publisher's actual `thread.Store`.
`ThreadCompactCatchUp`/`ThreadCompactPage` bind `Store.CompactCatchUp`/
`Store.CompactHistoryPage` on that same Store through `queryDaemonThread`;
version-zero compact catch-up selects the newest public root window.
Adapters preserve the request context, named conversation and each captured
result, including body kind, epoch/version and revision. Child requests pass
the normalized limit and every cursor field; no adapter selects twice or reads a
full `Snapshot`/`Observe` baseline. Installation clears all six query callbacks
and `ThreadCompactReady` before checking the Store, preventing stale providers
or readiness after clearing a reused config.
`ThreadReady` requires both complete queries, the authoritative watermark lookup,
`ThreadLiveState`/`LiveStateCurrent` delivery and a running publisher on that
same Store. Expansion additionally requires both expansion providers and client
`thread_expansion`/`thread`/`interactive` capabilities; missing either provider
withholds expansion while complete-thread negotiation still works. Readiness
does not promise that every conversation has completed replay.
`ThreadCompactReady` additionally requires both compact queries and both
expansion reads; production grants `thread_compact` only alongside the granted
`thread`/`interactive`/`thread_expansion` capabilities. A stopped publisher or
missing expansion provider withholds it. Complete history/live delivery remains
the default; compact publication is installed, with Desktop #2080 and Mobile
\#2200 adoption downstream. See the
[expansion wire contract](../../protocol-mobile.md#bounded-expansion-reads).
Authenticated queries load cold or idle registered conversations and wait
cancelably for readiness; unavailable views fail with `thread.query_failed`
without certifying partial items.
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
preserves live continuity only with a validated clean checkpoint matching the
saved successful retirement boundary and a contiguous, representable suffix.
The Store reconstructs complete changes and compact root evidence from that
boundary; equality of epochs alone proves no range. Genuine gaps still retire
consumers, which reconnect and explicitly catch up or acquire a newest window
after reset. Acquisition never sends a full live inventory.
Status is only a hint, not continuity evidence or a transaction across reads.
Wire metadata, changes and cursor advancement must all come from the same
captured `Changes` result. Recheck captured usability and retire
consumers before replacement even if withdrawal/reload races the status read.
See [observation readiness](thread-package-background-store-replay.md#loading-and-replay-readiness)
and [relay gap quarantine and recipient gates](v2-session-manager-state-machine-capability-negotiation-on-the-handshake-thread-delivery-and-summary-projection.md#thread-delivery-and-summary-projection).

For journal diagnostics, each `daemonThreadPublisher.gap` invocation emits one
Warn with message `relay: thread gap`, event `daemon.thread.gap` and a fixed
`reason` identifying the rejecting branch:

| Reason | Trigger |
| --- | --- |
| `state_not_usable` | `publish` finds unusable status while a publication cursor exists. |
| `baseline_required` | The captured `Changes` result is unusable or requires a baseline. |
| `compact_reset_required` | The captured `CompactChanges` result is unusable or requires a reset. |
| `encode_failure` | `deliver` cannot encode an update or decode an encoded envelope. |

The record contains only the fixed event/reason fields and normal log metadata
(time, level and static message). It includes no history/content, payload bytes,
credentials, filesystem paths, peer/conversation IDs or raw errors. This warning
replaces `daemon.thread.encode_err`, so encoding failure emits one gap warning.
An unusable status without an existing publication cursor stays silent.
Logging preserves recipient selection, retirement and metadata-only cursor
recovery: `Changes` rejection attempts replacement acquisition in the same
pass; status, compact and encoding gaps leave acquisition to a later pass.
Recovery never broadcasts a historical baseline; clients use authenticated
catch-up and paging as described above.

Recipient projection is per connection and conversation. `ActiveThreadConns`
captures `ActiveConn.View` and `PendingView` together under `hostedMu`, with
the negotiated `ThreadCompact` flag. Complete-only deployments do no
`CompactChanges` work. Unjoined compact-capable peers participate in both
families' query cancellation, but the relay withholds compact frames until a
compact join exists. During a projection switch, both completed and pending
families participate. Selecting only the completed join loses updates: the
other family's frame cannot cancel a stale pending capture, which could then
certify while the publisher cursor moves past the change. A later query
admission crosses the publication gate before Store capture. Relay membership,
projection and cancellation checks remain authoritative immediately before
sealing; the daemon snapshot grants no content access. See
[query/live fencing](../../protocol-mobile.md#querylive-ordering).

For compact recipients, an advanced same-epoch observation permits one
`Store.CompactChanges` call under the operation context. Calling when the
cursor equals the current version would block; status and captured progress
must establish advancement first. Each interval yields one final replacement
per affected public root (tool/agent header or full non-tool/agent item), or
one consumed-progress update if no root changed. `threadShadow.follow`'s
existing 50 ms publication tick is the flush schedule, plus the pass's work.
The following changed pass flushes final state after activity stops, and an
unchanged pass emits nothing. Pending compact work is proportional to affected
roots, with no per-hidden-event queue. Oversized full root items can still
fragment. Complete update content and order remain unchanged.
Compact read errors leave the cursor for retry; unusable/reset results retire
through `gap`/`RetireLiveState`, as do complete continuity failures. Neither
acquisition nor retirement publishes a baseline. Compact recovery remains an
explicit query, even when compact catch-up can survive a lost complete
publication range. The publication gate and publisher wait group own both
families; publisher stop cancels and joins in-flight publication and reads.
Publication prunes disconnected tracked recipients before checking compact
continuity. Compact-gap retirement therefore excludes those already-pruned
peers while still including currently eligible query-only consumers; expecting
every previously tracked peer to retire would change existing recipient behavior.

`daemonThreadUpdates` maps exported fold field names through `ThreadItem`'s JSON
tags. Copying `EndedOrder` or `NoChild` directly would produce unusable wire
patches instead of `ended_order` or `no_child`. A combined field/text change
emits one atomic `thread_item_changed` containing all replacements and complete
`Change.ResultContent`, with the suffix already included. Suffix-only changes
emit one `thread_text_append`. Both use `PreviousRev` as `base_rev` and the actual
resulting `Rev`, requiring held-revision equality and `rev > base_rev`. A patch
followed by an equal-revision append would truncate text for strict clients;
inventing an extra revision would diverge from full-item reads. The
[internal observation contract](thread-package-background-store-replay.md#loading-and-replay-readiness)
still applies `Fields` before `TextAppend`; the wire replacement is already final.
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

All six authenticated thread reads share `queryDaemonThread`'s temporary
daemon demand; `threadShadow.queryThread` remains the complete-query wrapper.
They load cold or idle registered conversations, wait cancelably for a usable
view, and keep it loaded through bounded selection and rendering. Concurrent
queries share the worker with independent demand counts; cancellation releases
only the caller's demand without canceling another reader's worker. Removal and
shutdown cancel and join affected demand. Each admitted read also joins the
current publisher's wait group and follows its cancellation context through
capture/rendering. Canceling without joining would let publisher stop return
before demand release. Unknown and unavailable conversations fail closed.
Queries enroll the same coordinator even for an already-loaded conversation.
Before query capture, `threadShadow.publishThread` acquires a missing publisher
boundary through a cancelable per-conversation gate shared with following.
Otherwise a query could complete before first publication silently skips an
intervening commit. Preparation leaves an existing cursor to following: forcing
changed publication here could make a query trigger its own live-race refusal.
Never wait for this gate while holding shadow or quiescence mutexes; cancellation
must release a query waiting behind held fanout without waiting for that fanout.
A successful coordinator-owned idle unload retains the publication cursor while
the cache is absent or rebuilding. Reading an unchanged checkpoint and appending
durable history can both preserve the original connections. Before output, the
reloaded Store must prove the saved boundary's clean checkpoint/retirement match
and suffix coverage through `Changes` and, for compact recipients,
`CompactChanges`. Complete patches use applicable previous/resulting revisions;
compact updates contain only affected roots or consumed progress. Missing or
mismatched boundary evidence, invalid/dirty checkpoints, epoch change, lost
ranges, history gaps, published-row suppression, compact root removal or failed
recovery retain reset/retirement before dependent output. Deliberate unload
still releases worker memory, and the existing publication gate serializes
queries with cursor acquisition and delivery. Reconnect remains explicit
catch-up even when an idle reload can continue live.

`TestDaemonThreadDemandIdleReadKeepsConnection` retains the unchanged idle-read
proof. `TestDaemonThreadIdleAppendKeepsConnections` completes authenticated
complete and compact catch-up, observes actual idle unload, then appends durable
history over two cycles. It checks one unsolicited update per projection on the
original connection IDs and successful later queries from both peers; no new
handshake or baseline inventory repairs the live state. It uses the real history
Store, thread Store, shadow follower and session manager, without live Claude.

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
changes, dropped sends, dividers and channel posts. It checks atomic combined
replacements with complete text at the source revision, strict revision
advancement, the waiting bound, escaping-heavy continuation assembly and retained
current readings, while legacy traffic keeps its expected shape. Mapper-only assertions would miss
wire ordering or recipient projection errors. `TestDaemonThreadReadiness`,
`TestDaemonThreadUnusable` and `TestDaemonThreadContinuity` cover usable zero,
missing providers/views and lost ranges. `TestDaemonThreadQueriesReadiness`
checks complete query installation, request cancellation and nonloading reads.

`TestDaemonThreadGapReasons` enters through `publish`/`deliver`, checking all
four gap reasons, unique recipient retirement and metadata-only recovery.
Its exact JSON record allowlist also rejects unexpected fields; checking only
for a known secret would miss other input leaking into logs. Private-looking
content, identifiers and retirement errors must leave exactly one Warn.
`TestDaemonThreadGapNoCursor` preserves silent unusable startup. The defensive
envelope-decoding failure shares `encode_failure` by source review; the real
encoder returns valid JSON, so this branch has no injected runtime proof.

`TestDaemonThreadQueriesRecovery` checks authenticated clean-reopen catch-up,
old-revision first appearances, escaped multiframe replies, page metadata and
simultaneous legacy raw history. `TestDaemonThreadQueriesReset` checks explicit
nonusable failure, reset evidence and the 1024/1025 gap boundary.
`TestDaemonThreadQueriesBounded` exercises installed queries over 36,000
committed entries after recovery, measuring preparation separately from adapter
allocations. `TestDaemonThreadQueriesJoin` reconstructs full current states
through race refusal, successful retry and subsequent real publisher updates.

`TestDaemonThreadStreamingRevisions` applies encoded updates from real
`Fold.FeedChanges` under strict revision checks and compares every row, including
Summary, with full fold reads after each delta and metadata-only completion.
Both changed and append counts must be nonzero: frame-presence checks alone
would miss truncated text. Summary accumulates normalized text without length
truncation, so a long prefix cannot force suffix-only updates. Trailing
whitespace/control-only deltas leave Summary unchanged while extending exact
content bytes. `TestDaemonThreadChanges` checks additions, field-only changes,
unknown content and explicit clears; `TestDaemonThreadCombinedAssembly` proves
an oversized combined replacement stays atomic through bounded assembly.

`TestDaemonThreadExpansionWire` uses installed providers and authenticated
synthetic history for exact ordinary tool and agent-launch bodies, captured kind,
escaping-heavy assembly, immediate-child pages and separately opened nested
parents. It checks omitted detail/descendants, direct-parent ownership, a separate
complete-view client, subsequent live traffic and a second client/conversation.
`TestDaemonThreadExpansionRefusals` covers stale cursors after history advances,
actual epoch replacement, and missing/private or unknown-conversation refusal.
`TestDaemonThreadExpansionPartialInstallation` preserves complete queries when
either expansion provider is missing. `TestDaemonThreadExpansionDemand`,
`TestDaemonThreadExpansionIdleAndUnavailable` and
`TestDaemonThreadExpansionTeardown` cover shared-reader cancellation, idle reload,
unavailable history and joined removal/shutdown/publisher retirement.

`TestDaemonThreadExpansionBounded` separates preparation from installed requests.
Growing unopened descendants from 8 to 512 and their bodies from 64 to 4096 bytes
keeps the same fixed-limit page measurements: 40–53 delivered rows, two envelopes
and at most 65,022 fully stamped aggregate bytes, below the 65,519-byte cap.
Byte-shortened continuation enumerates all 301 direct children without skipping
selected but undelivered rows. Existing
[Store work-counter proofs](thread-package-background-store-queries.md#bounded-queries)
bound selected body references and direct-child index visits with zero
child-page body lookups; the installed adapter source guard rules out full
baselines or a second selection. The macOS race run executed and passed all 15 installed
expansion tests/subtests, with zero failures or skips. Live Claude and Linux were
outside this ticket's verification scope.

Expansion completion releases only its relay query slot; it neither writes
`threadJoins` nor advances a complete-thread checkpoint. Relay tests
`TestThreadToolBodyResults`, `TestThreadToolBodyFullQuerySlot` and
`TestThreadChildrenPages` check unchanged/absent joins separately. The installed
wire fixture uses a different client for complete catch-up, so its green alone
cannot prove same-client checkpoint invariance around expansion with an existing
join. Similarly, separate question/approval tests prove established channel
behavior but do not prove coexistence with installed expansion in that fixture.

Installed authenticated compact evidence uses the real Store and relay:
`TestDaemonThreadCompactInstallation` and `TestDaemonThreadCompactNegotiation`
check callback clearing, cancellation, readiness and capability refusals.
`TestDaemonThreadCompactBoundary` refuses a stale compact query racing live
publication, then compares retry headers with the Store.
`TestDaemonThreadCompactSwitch` exercises both projection switches, with capture
before publication and admission after the recipient snapshot.
`TestDaemonThreadCompactRecovery` checks retained-checkpoint reconnect and a
fetched child page invalidated by newer root `subtree_version`, then verifies a
fresh bounded read. Source `rev` alone cannot detect every nested change.
`TestDaemonThreadCompactRetirement` forces loss of 65 separately consumed
complete change batches and actual Store epoch replacement while compact-joined;
both retire without a baseline before explicit compact recovery.
`TestDaemonThreadCompactConversationIsolation` puts opposite complete/compact
joins on two conversations for the same peers and compares independent live
content, order and checkpoints with each Store projection.
`TestDaemonThreadCompactProgress` checks one hidden-only progress envelope and
silence on the following idle pass.

`TestDaemonThreadCompactWire` holds publication across nested bursts behind
fixed visible roots. Hidden children grow from 5 to 8333, tool/error bodies
from 64 to 4096 bytes, and bursts from 3 to 25 children, each with a tool use
and error result. Queries and final live headers match the Store and exclude
hidden identities/payloads. The final
[Linux verifier evidence for #3563](https://github.com/pyrycode/pyrycode/pull/3581#issuecomment-6240626533)
records physical envelopes / fully stamped bytes after publication completion
and ordered-lane drain:

| Compact delivery | Small history | 8333-child history |
| --- | ---: | ---: |
| Newest window | 2 / 1484 | 2 / 1511 |
| Older page | 2 / 1018 | 2 / 1040 |
| Catch-up | 2 / 1006 | 2 / 1025 |
| Live burst | 1 / 497 | 1 / 506 |

Frame counts stay 2/2/2/1 across both scales; byte totals vary slightly with
wire stamps and versions. These are bounded-root fixtures, not a constant-byte
promise for arbitrary top-level messages. `testCompactRead` preserves fragment
assembly across drain markers and counts every continuation envelope; resetting
assembly at a marker drops oversized complete items, while counting only
reassembled updates undercounts frames. Its drain goroutine allows wire output
to be consumed while flushing, and is cancelled and joined on every return.

Real prompt producers, the send-message handler and live-state publisher deliver
questions, approvals, correlated send acknowledgements and main-turn state while
publication is held halfway through each burst. The final verifier observed
11.72 ms and 7.69 ms, within the asserted three-second bound; the bound is checked
after wire reads as well as before them so a late batch cannot pass. Establish
the running producer before capturing prompt ownership: starting it afterwards
retires the captured prompt generation and tests the wrong lifecycle.
`TestDaemonThreadCompactLiveChannels` and both wire scale cases plus their parent
executed and passed in the Linux race gate (4 passed, zero failed/skipped).
No live Claude is involved; macOS is outside this ticket's verification scope.

Epoch replacement can legitimately retire the previous peer. Authenticated wire
helpers must consume that peer's close frame before decoding a replacement
peer's response, or valid retirement appears as malformed JSON. A publisher
fixture with an unstarted relay manager must supply an explicit empty active-peer
provider: otherwise shutdown can wait for a peer query no manager will service.

`TestDaemonThreadBoundedRecovery` compares 128 and 8333 settled rows: initial and
replacement acquisitions emit zero historical item frames/bytes, while a new
append emits one live item frame. Each size/event case owns its Store, wire,
publisher cursor and counters: a failed idle assertion must not leave unread
frames or state that contaminates later gap cases. After full baseline paging,
clean idle reload delivers exactly one applicable addition on the original
connection; applying it must equal replay. Actual retained-range loss and epoch
replacement still retire before historical output and recover through explicit
authenticated catch-up. All cases retain bounded older pages and a subsequent
live-append check. Silence alone cannot prove bounded output: wait
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
have separate round-trip and interruption/retry proofs, supplemented by
[worker cadence and joined certification](thread-package-cache-and-epochs.md#worker-checkpoint-cadence-and-retained-changes).
Store startup restoration remains #3337; loading still fully replays history.

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
