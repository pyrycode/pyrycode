# `internal/thread` — query demand, retirement and capture evidence

[Shadow lifecycle overview](thread-package-shadow-lifecycle-and-evidence.md).

## Idle retirement, query demand and evidence

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

Offscreen summary recovery needs a notification as well as valid evidence.
During `Unload`, `Store.Status` deliberately reports `StateNotLoaded` while the
retiring worker remains retained inside the Store; unloaded certificate lookup
rejects that worker. After joining, a racing hidden append can still leave the
clean certificate behind current raw progress. A list read at either boundary
correctly omits `last_shown_version`, but without a later producer refresh an
offscreen row can stay without authoritative unread metadata even after completed
catch-up and restored eligibility. This sequence is demonstrated by the controlled
Core regression; the historical Mobile trace establishes missing metadata, not
that trace's exact internal interleaving.

After each successful quiet unload, `threadShadow.follow` releases its retirement
gate and calls `refreshThreadSummary`. It builds an ordinary unsolicited
`conversation_updated` from the current registry row and workspace label for
eligible authenticated interactive thread peers. `V2SessionManager.threadSummary`
resolves the watermark through the installed provider at delivery. The refresh
can still omit an ineligible reading; a racing suffix is automatically reloaded
and a later successful retirement refresh restores the reading when eligible.
No chat opening, extra catch-up or read command drives recovery. Stored read
marks and other summary fields retain their values; non-thread publication is
unchanged. See the [loaded/retiring/unloaded authority boundary](thread-package-cache-and-epochs.md#scalar-certificates-and-unloaded-metadata).

Refresh admission takes the shadow mutex before the publisher mutex and joins
the publisher wait group before stop can seal admission. Registry lookup and
relay delivery run outside those locks and `quietMu`, following both follower
and publisher cancellation. Failed unloads and missing registry rows emit no
success refresh. Encoding/push diagnostics carry fixed events without payloads,
names, paths or raw errors; one failed peer does not prevent healthy fanout.
Delivery is live-only; reconnect listing remains the recovery path after a
failed push.

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
history over two cycles. It checks one unsolicited thread item update per
projection on the original connection IDs and successful later queries from both
peers; no new handshake or baseline inventory repairs the live state. It uses the
real history Store, thread Store, shadow follower and session manager, without
live Claude.

Query and item-publication fixtures must tolerate unsolicited
`conversation_updated` alongside correlated replies and item deltas. Summary
notifications carry no item version: counting or decoding them as item changes
would reject valid retirement recovery. Keep exact item counts, original
connection ownership and the separate successful query completion marker intact.

Legacy delivery fixtures must also account for public-row suppression. Publishing
an unlinked delivery message before its `send_delivered` outcome can require a
producer-gap reset when the outcome claims that row. To isolate attachment
retention, `TestDeliveredAttachmentWire` publishes acceptance/written boundaries,
then loads the linked legacy message/outcome pair together across a clean idle
unload under shadow coordination. This preserves reset behavior and the original
acceptance identity; the later fresh-daemon reopening separately proves durable
names and bytes. New messages with an explicit acceptance reference use the
[message-time join](thread-package-main-thread-folding-sources.md#accepted-sends-and-durable-outcomes).

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

`TestDaemonThreadOffscreenShownRecovery` exercises fresh daemon-created A/B,
actual send/history and assistant/completion producers, installed thread
providers and authenticated relay projection. The phone completes A catch-up
with its separate success marker, then reads the list during a barrier-controlled
retirement. Diagnostics distinguish registry membership, readiness, query demand,
retirement and raw/certificate eligibility. The proof then checks joined stale
V/H, automatic suffix reload and a producer refresh restoring A's shown > read
without another catch-up/read/open. B remains not unread, both stored read marks
stay zero and stored summary fields survive. Hidden entries and completion-only
revisions leave the shown boundary unchanged. `TestDaemonThreadSummaryRefreshLifecycle`
covers stopped/removed/cancelled refreshes, peer filtering and fanout past a failed
push with content-free diagnostics. The controlled failing-before and passing-after
Linux race evidence is retained in the [repair plan](../../specs/architecture/3710-offscreen-shown-watermark.md)
and [PR review](https://github.com/pyrycode/pyrycode/pull/3713).

Mobile acceptance remains pending in [Mobile #2246](https://github.com/pyrycode/pyrycode-mobile/issues/2246).
Core producer proof and the Core live-Claude gate cannot replace the entire
unchanged `InteractiveStreamE2ETest.interactiveTurn_attentionDot_followsARealTurn`
through managed Linux/Android live/capture admission against the repaired daemon.
That capture must preserve A Unread/B Idle and retain counted results, Mobile/Core
revisions, APK/binary hashes and negotiated route/host/conversation/connection
ownership receipts. Historical failures remain failed; pending, skipped or zero
execution is not a pass. Other operating systems are outside this ticket's
verification scope.

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
catch-up, which can suppress duplicate publication. Newly produced accepted
deliveries establish their permanent identity from the message's durable
acceptance reference before any public delivery row appears (#3725). Message
and outcome can therefore reach separate follower batches without a join-induced
baseline/reset or retirement. Delivery and outcome may each patch the same item;
wire fixtures must consume every retained change before their correlated query
response. `TestQueuedSendHandleActions` permits replacement-peer recovery, so
it alone cannot prove that a fresh refusal reached the original joined peer.

`TestAcceptedDeliveryLiveRefusal` uses real producer appends, `ForwardReader`,
source-backed Store folding/publication and `daemonThreadPublisher`. It publishes
an accepted baseline, then uses barriers for coalesced delivery/outcome and
separated publication with an actual segment cut. Both retain the original peer
and publish exactly one fresh refusal addition with no duplicate delivery root.
Interrupted-prefix and settled replay, independent source-backed reopening,
catch-up and retained observations must agree. Reopening copies only raw
segments into an independent instance: two live Store owners must not share
derived checkpoint files. The committed
[Linux evidence](../../../cmd/pyry/testdata/accepted_delivery/evidence.json)
pins raw before/after Go JSON, revisions and counts; the separated schedule
failed before repair and both schedules passed afterwards, with zero skips or
real Claude turns. The [verifier review](https://github.com/pyrycode/pyrycode/pull/3726#issuecomment-6322486428)
also confirms the repaired written-prefix checks and current-head gates.

Older unlinked message/outcome suppression and genuine gaps still require
retirement before dependent output; `TestStoreObservationDelivery`,
`TestCatchUpBoundedRecovery` and `TestDaemonThreadGapReasons` retain those checks.
A replacement peer obtains earlier refusals only through explicit history or
catch-up, without unsolicited live replay or a fresh recovery offer. The Core
regression proves this publisher boundary; post-merge phone/transport acceptance
belongs to [Mobile #2374](https://github.com/pyrycode/pyrycode-mobile/issues/2374).

Nil or failed shadow storage leaves supported live state and surviving legacy
producers operational; it cannot restore retired interactive content.
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
