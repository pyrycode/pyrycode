# `internal/history` — history producers

Part of [the history package overview](history-package.md).

## Producers (#2114, #2115)

Interactive output, session transitions, runtime/startup and agent/task facts,
accepted-send facts, remote prompt answers and delivered operator messages in
`cmd/pyry` append through one seam, `appendConversationHistory`
(`cmd/pyry/conversation_history.go`): the
interactive emitter's `emit` chokepoint
(`cmd/pyry/interactive_turn_v2.go`), session transitions' `broadcast`
(`cmd/pyry/session_transition_v2.go`), and `operatorMessageHistory`
(`cmd/pyry/operator_message_history.go`), used by queued stream placement and
`msgqueue.Config.OnDelivered`. The compatible `newOperatorMessageHistory`
helper delegates to that operator producer. Runtime opening/interruption facts use
`interactiveTurnEmitterV2.recordRuntimeFact` through the same append seam.
Claude attribution, reported endings and roster-inferred gone facts use
`recordAgentFact` at that seam, without copying the mapped reports' prose.
`closeAgentHistory` and `closeStartupAgentWork` append `agent_ended_with_session`
through the same seam before session dividers (#3032), retaining unfinished
agent/background work even after the main turn ends.
Emitters resolve conversation ID, source type, marshalled payload and one hoisted
timestamp before the append attempt; publication eligibility is a separate step.
Operator history resolves them
from a safe `msgqueue.QueuedMessage`, available before the write through
`msgqueue.DeliveryMessage` and again at confirmation through `OnDelivered`,
not from an envelope in flight (see
[delivery ordering](history-package-producers-legacy-compatibility.md#legacy-eligibility-and-explicit-visibility-2965)).

Interactive assistant/tool/completion facts still pass through `emit`'s single
history-append attempt, even without connected clients (#3734). Successful
appends retain payloads, timestamps, visibility and captured producing-session
provenance. The five types `assistant_delta`, `tool_use`, `tool_result`,
`tool_denied` and `turn_end` then return before legacy projection, replay
insertion or fan-out for every app, including peers without `thread`; no
substitute receipt is produced. Canonical folding and explicit catch-up consume
these retained sources. Nil/failed storage cannot restore raw content delivery;
supported live state continues independently, with content-free diagnostics.
Coalescing, chunking and source-switch/boundary flush order remain recording
contracts. Channel-post deltas/completion use their separate durable writer;
their raw publication is also retired (#3737). See [interactive provenance](history-package-producers-interactive-provenance.md#interactive-provenance-2981)
and [thread publication](thread-package-shadow-lifecycle-and-evidence-publication.md#background-discovery-and-publication).

Background-task start/update/roster reports also retain their single append
attempt, payloads, timestamps, visibility and captured source even without
clients, then return before projection, replay or fan-out (#3735).
`recordAgentFact` and `closeAgentHistory` retain neutral attribution, outcomes,
gone and session-ending appends without publishing compatibility receipts.
Canonical thread items and explicit catch-up consume both mapped and neutral
sources. This retirement applies to every app and nil/failed storage; task
progress and bound-session stop controls remain supported. The runner's retained
roster/join state still serves controls, but connect-time roster publication and
its exclusive provider/wiring are removed. Stop acceptance supplies no outcome.

Session transitions and content notices retain their source appends while raw
publication is retired for every app (#3736). `sessionTransitionEmitterV2.broadcast`
records `session_divider` and accepted `session_transition` sources without raw
fan-out or substitute transition-divider receipts. Both refusal types,
`compaction_boundary` and `unrecognized_message` return from `emit` after their
single append attempt, before projection, replay or delivery. Payloads,
timestamps, explicit visibility and captured conversation/agent/session
provenance remain unchanged; unknown reasons and unresolved owners stay rejected.
The rule holds without clients, without `thread`, and with nil/failed storage;
failure cannot restore publication and diagnostics expose no content.

Canonical live items and explicit catch-up consume these sources without
duplicating session dividers, refusal notices, compaction or unsupported-content
items. Pre-divider closure, switch callbacks and flush/completion barriers,
cancellation and busy release remain intact. Delayed predecessor output retains
its source and cannot clear or close the successor. Supported live
model/settings/catalogs, prompts, progress, retry and compaction readings/controls
retain their source tags and ordered clears. A readable source is not evidence
that earlier live frames have drained: reset tests compare daemon timestamps,
and switch tests pause the retained committed-row callback.

The [counted Linux proof](thread-package-shadow-lifecycle-and-evidence-retirement.md#session-and-content-notice-retirement-3736)
on `9ebba3462074` records 28121/28121 race-unit, 1963/1963 fake integration and
2268/2268 configured live leaf tests, zero failed, with 27/2/27 skipped
respectively; historical failed results remain failed. It also distinguishes
the 163/163 focused parent/subtest events from leaf counts and tagged compilation
from live acceptance. They cover #3736, not parent #3718's final family acceptance.

Standalone banners and runtime/startup facts now retain source recording only
(#3737). `emit` returns after its banner append attempt; `recordRuntimeFact`
appends directly and returns. Values, timestamps, visibility and captured source
remain unchanged, including with no clients. No app receives raw banners,
substitute runtime/prompt-answer receipts or new content replay copies, on
connect, live output or reconnect, including peers without `thread` and absent/
failed storage. Saved facts feed canonical thread items and explicit catch-up.
Open prompts, dismissals, settings/catalogs, progress, retry, compaction live
state and task controls remain supported with their source identity.

Channel delivery retains accepted-post persistence/carry, ordered chunk
reconciliation and the four-field `channelPostTurnEndPayload` as durable recovery
input. A new successful completion triggers the existing asynchronous wake only
after recording. Failed completion stays pending without a wake; cleanup-only
retry/reload and catch-up never repeat it. Partial writes, cleanup failure and
restart preserve one canonical completed post and original provenance. Active
turns, write reservations and teardown still bound pending delivery.

**Retain shared state when deleting exclusive publishers.** `publishLegacyHistory`,
`legacyRuntimeReceipt` and prompt/post publication workers, queues and counters
are removed. `historyVisibilityMetadata`/`historyEntryShown`, source payloads,
constructor seams and ordered `daemonLiveState`/`daemonThreadPublisher` delivery
remain. The interactive ring/counter still serve supported live state; replay
admission/pump retirement belongs to #3738 and shared ring/`SetReplaySource`
removal to #3739. Neither this cleanup nor its
[counted evidence](thread-package-shadow-lifecycle-and-evidence-retirement.md#residual-content-retirement-3737)
completes #3718's final family acceptance.

### Producer topics

| Document | Topics |
| --- | --- |
| [Interactive provenance](history-package-producers-interactive-provenance.md) | Captured source, retained text and child lanes, conversation phase and channel posts. |
| [Runtime and startup lifecycle](history-package-producers-runtime-lifecycle.md) | Boundaries, interrupted main work, agent/task identities and endings, linked-work deduplication and exactly-once restart recovery. |
| [Legacy compatibility and verification](history-package-producers-legacy-compatibility.md) | Stored visibility versus retired publication, retained live-state ordering and producer test traps. |

Claude Agent/Task calls and background reports retain attribution alongside their
existing mapped reports. See [agent/task attribution and reported endings](history-package-producers-runtime-lifecycle.md#agenttask-attribution-and-reported-endings-3030)
for launch-result meaning and durable source/lifetime isolation, and
[legacy eligibility](history-package-producers-legacy-compatibility.md#legacy-eligibility-and-explicit-visibility-2965)
for the separate raw visibility, unread-target and presentation contracts.

`background_task_gone` means an **unknown outcome** (#3031). It requires an
observed Claude Agent/Task call with a complete task-to-call join and a later
received complete roster omitting that task in the same conversation, captured
source and durable child lifetime. An empty reported roster qualifies. No refresh,
retained read or join pruning alone implies an ending. Codex produces no gone
facts. See [roster completeness and ordering](history-package-producers-runtime-lifecycle.md#gone-requires-a-later-complete-roster-3031).

`agent_ended_with_session` retains known call/task/parent/tool identities, dying
source, durable lifetime, occurrence time and actual boundary cause. A usable
call/task link closes as one work item; unlinked identities close independently.
Runtime endings precede the divider and successor traffic; `child_exit` closes
the selected child lifetime without a divider. Startup uses `daemon_restart` and
original observation references, preserving legacy scope and unavailable
attribution without minting missing IDs. See
[session endings](history-package-producers-runtime-lifecycle.md#unfinished-agenttask-session-endings-3032)
and [agent recovery](history-package-producers-runtime-lifecycle.md#recover-agenttask-work-and-original-identities-3032).

Gone and ended-with-session facts have explicit raw `shown: true`. Their raw
payloads stay excluded from legacy live delivery and replay.
No compatibility receipt is published for these agent facts (#3735). Their
original durable IDs, timestamps, explicit visibility and source/lifetime facts
remain fold inputs for canonical items and explicit catch-up. Runtime and
prompt-answer receipts are also retired and the exclusive adapters removed
(#3737). See
[legacy eligibility](history-package-producers-legacy-compatibility.md#legacy-eligibility-and-explicit-visibility-2965).

### Legacy transition provenance (#2982)

Capture source facts at the observer/publication handoff, before either
publication lane can delay. `sessionTransitionEmitterV2.Enqueue` captures
ordinary transitions; `publishSwitch` captures committed switches on their
dedicated lane. Both use `capture` to fill missing conversation ownership and
agent facts by exact session ID. Nonempty captured `ConversationID`,
`NextAgent` and `PreviousAgent` facts take precedence over lookup results.
Both relay and history-only installations use
`startSessionTransitionStreamV2WithHarness` with `relayWiring.sessionHarness`,
backed by `Pool.HarnessFor` for live or dormant sessions without starting a
child. Conversation-level agent lookup could instead read a replacement binding.

| Legacy boundary | Producing session and agent |
| --- | --- |
| `clear` (`ReasonClear`), including committed switches | Successor `NewID` and `NextAgent` |
| `idle_evict` (`ReasonEviction`) | Evicted `PreviousID` and `PreviousAgent`; no successor exists |

These are daemon session routing IDs, distinct from conversation IDs and
agent-native IDs. `transitionProvenance` supplies `history.SessionProvenance`
through `appendConversationHistory` only for a nonempty session ID paired with
actual `claude` or `codex` facts. Missing IDs, unavailable lookups and unknown or
unsupported agent facts leave provenance absent; raw transition publication
remains retired (#3736). A nonempty unsupported captured agent is not replaced
by a lookup.
Delayed `broadcast` never looks up an agent or substitutes a replacement
session for provenance, even if an unavailable lookup later becomes available;
it neither defaults to Claude nor invents `none`.

A nonempty captured `ConversationID` is authoritative for source ownership and
canonical routing after another rotation, pool removal or registry replacement.
`toWirePayload` retains that owner. Only when ownership is absent may
`broadcast` use the existing exact-session conversation resolver; if ownership
remains unresolved, it drops the event without guessing a conversation.
See [captured lifecycle facts and switch publication](sessions-package-key-types-transition-observer.md)
and [ADR 042's session provenance](../decisions/042-daemon-built-thread.md#sessions-agents-messages-read-marks).

Raw `Store.Page` exposes captured metadata in warm and reopened stores; old
untagged entries remain untouched. Visibility stays `clear` shown and
`idle_evict` hidden. Retained source payloads and durable identities keep their
meaning, including eviction's mirrored legacy source session IDs. Neither
nil/failed storage nor absent provenance restores raw fan-out or divider receipts
(#3736). The compatibility enqueue remains nonblocking and drops
on full; runtime enqueue retains each captured boundary behind a coalescing wake
without waiting for storage or publication.
Committed switches retain their transition/history, row and sealing order.

### Operator delivery provenance (#2983)

Operator provenance names the successful receiving session, not the binding
at enqueue time. `writeOperatorTurn` captures `history.SessionProvenance` by
value immediately before the receiving writer's `WriteUserTurn`, after
activation, idle waits and Claude placement write serialization.
`boundSession.operatorProvenance` reads that session's live `Session.ID()` and
its construction-fixed `claude` or `codex` kind supplied by `Pool.HarnessFor`.
The ID is the daemon's routing session ID, never a conversation ID, Codex
thread ID or client field. A message queued under A but written to B names B;
later rotation, rebinding or agent switching cannot replace that snapshot.

Resolution precedes activation and idle waits, so even a resolution-time ID
can predate a rotation. Placement selection must also use the receiving
writer's fixed kind: a later conversation-kind lookup could choose the
successor's placement path while the original writer still receives the turn.
Unknown writers retain the existing placement behavior without inventing
provenance. See [session binding](conversation-session-binding.md) and
[ADR 042's session provenance](../decisions/042-daemon-built-thread.md#sessions-agents-messages-read-marks).

Successful Claude writes retain the snapshot on the managed placement entry
before signaling the write outcome. Placement passes it to the safe producer
when committing on an echo or normal completion, with Send now's carried-close
grace for no-echo completion. Successful Codex/no-stream writes retain it by
conversation and queue ID until
`sendNowPlacement.takeSource` consumes it at confirmation. Failed writes retain
no source and create no delivered entry; a retry captures its own receiving
session. Existing placement order and exactly-once recording, including late
echoes and callbacks, remain intact.

`newOperatorMessageHistory` builds delivery source content only
from the safe `msgqueue.QueuedMessage` projection: client-readable text,
attachment IDs and existing client metadata. Composed prompts, echoed delivery
text, host paths and private matching digests remain absent from payloads and
logs. `appendConversationHistory` combines the captured source with existing
visibility; raw pages expose it in warm and reopened stores. Legacy entries
stay untagged, and callers without source information remain usable with
absent provenance, never inferred as `none`. Raw user-message fan-out and newly
appended reconnect replay are retired for every app (#3732); retained source
facts feed canonical thread items and explicit catch-up. Queued acceptance,
delivery, drop and loss facts produce no legacy receipts (#3733).

### Accepted sends and linked outcomes (#2972)

`runSupervisor` installs one `queuedSendHistory` on the existing queue and
placement paths. `OnAccepted` writes `send_accepted` from the safe
`msgqueue.QueuedMessage` projection: `conversation_id`, authenticated opaque
`device_id`, app `message_id`, client-readable `text`, `attachment_ids`,
`accepted_at` from the queue's acceptance timestamp, and `client_sent_at` when
the reported time was parsed. Reported time uses UTC RFC 3339 with nanosecond
precision. Optional absent values are omitted. Composed delivery bytes, echoed
prompts and host attachment paths never enter these facts.

`queuedSendHistory.accepted` also records optional `attachments`, whose entries
contain exactly `attachment_id` and `filename` (#3693). Before queue startup,
`runSupervisor` installs `storedQueuedAttachmentName`: it requires registered
conversation membership before `attachments.ResolvePath`, then retains only
`filepath.Base` of the stored path. The stored, sanitized basename is authoritative;
send-supplied names and the in-flight upload registry cannot supply durable facts.
See [stored-path resolution](attachments-package-directory-resolution-and-creation.md)
and the [full thread item contract](../../protocol-mobile.md#full-thread-item).

Keep the safe projection's `attachment_ids` unchanged; send intake already
deduplicates them in first-occurrence order. Resolve each distinct ID once in
that order, recording one fact for each successful lookup. An unavailable name
after validation omits only its fact, preserving accepted text and all IDs;
no resolved facts omits `attachments`. Lookup errors can carry host paths, so
the resolver discards them without diagnostics. Neither filenames nor paths,
send content or composed prompts belong in operational logs. Invalid/missing
IDs still refuse the send before acceptance, and malformed payload shapes keep
their existing refusal.

Capture names at acceptance rather than on history reads: reopened thread
content and startup loss retain the recorded facts even if the stored leaf
later changes. Older ID-only facts remain unchanged without inferred filenames.
Delivery content replacement retention belongs to
[#3694](https://github.com/pyrycode/pyrycode/issues/3694).

The production `suggestionEnqueuer.EnqueueIdentified` forwards the authenticated
identity and invalidates suggestions only after successful enqueue. Rejected
enqueue writes nothing. Legacy callers and adapters remain usable with absent
identity; identity introduces no deduplication, so equal app message IDs from
different devices produce distinct acceptances.

Every successful acceptance append supplies its own durable history entry ID.
Outcomes refer to that ID through `accepted_entry_id` within the same
conversation, never to an app message ID or a queue ID across daemon runs.
The daemon-local reference map uses conversation and queue ID only to join
callbacks and placement during that run; it retires the reference on delivery
or drop. Outcomes carry `conversation_id`, `occurred_at` and `reason`.

| Fact | Link and meaning | Raw visibility and source |
| --- | --- | --- |
| `send_accepted` | Durable identity for a successful enqueue, with the fields above. | Shown; bound session captured at acceptance observation. |
| `send_written` | `accepted_entry_id`; reason `written` after a successful Send now stdin pipe write. No final placement link. | Shown; bound session observed at the successful write callback. |
| `send_delivered` | `accepted_entry_id` plus `delivery_entry_id` naming the existing operator `message`; reason `delivered` for ordinary and send-now delivery. | Shown; captured receiving session, shared with the operator message. |
| `send_dropped` | `accepted_entry_id`; reason `removed` or `give_up` only after actual removal or retry give-up. | Hidden; bound session captured at terminal observation. |
| `send_lost` | `accepted_entry_id`; reason `daemon_restart` for a surviving acceptance without a durable outcome. | Shown; original acceptance's source. |

For queued Claude writes, successful stdin pipe delivery establishes neither
consumption nor completion (#3683). Placement requires a matching echo or
supported normal answering-turn completion from the receiving child. Ordinary
no-echo completion commits on that close; Send now's no-echo fallback also waits
for its carried-close grace. `WaitIdle` reports readiness, so child exit,
teardown, daemon cancellation, an unrelated close or successor activity cannot
confirm a write, including exit during Send now's grace. Legitimate confirmation
already parsed before exit retains its stream order. See
[placement ownership and notification](msgqueue-package-lifecycle.md#delivered-notification-2115).

Abandoning an unconfirmed successful write preserves the original acceptance
entry ID, device/message/queue identities, text and attachment IDs. Ordinary
history remains `queued`; Send now remains `written`. These are unresolved
history states, not runnable FIFO entries: possibly consumed input is never
automatically re-enqueued or replayed. Abandonment appends no `send_delivered`
and invents no child-exit loss reason.
Later callbacks or predecessor/successor events cannot revive the placement.

An actually empty binding is explicit `kind: none` with no session ID. Unknown
or unavailable source facts remain absent. Delivery retains the
[receiving-session snapshot](#operator-delivery-provenance-2983), even after
rebinding; startup never substitutes a replacement session for the acceptance.
Hidden drops do not raise the raw displayable watermark. All five queued fact
types stay off legacy live publication and newly appended reconnect replay,
including their receipt projection and connections without `thread` (#3733).
Queued sends are observed through canonical thread items and explicit catch-up;
queued acceptance, delivery, drop and loss facts produce no legacy receipts.
`legacyRuntimeFact` excludes them, and `legacyRuntimeReceipt` rejects them even
when valid, durably identified and explicitly shown. Source validation remains:
acceptance requires `accepted_at`; outcomes require `occurred_at`,
`accepted_entry_id` and the exact reason in the table, with `delivery_entry_id`
also required for delivery. `send_written` requires reason `written` and no
delivery link. Sender/message fields remain optional. Raw visibility,
provenance, internal watermarks and ID allocation retain their contracts. See
[legacy eligibility](history-package-producers-legacy-compatibility.md#legacy-eligibility-and-explicit-visibility-2965).

**Gate placement itself on acceptance completion.** Queue callback ordering
cannot protect the earlier commit on echo or normal completion: placement can
reach the producer before acceptance observation and before a delayed `OnDelivered`.
`queuedSendHistory.beforeDelivery` waits for the synchronous acceptance append
to finish, including failure, before `operatorMessageHistory` appends the
operator message and then its linked delivery fact. Waiting holds no observer
mutex; acceptance never waits for the stream drain. After a successful acceptance
append, the message itself carries that durable `accepted_entry_id` (#3725).
The fold can therefore establish permanent delivery identity from the committed
message before publishing any prefix, even when `ForwardReader.Walk` separates
message and outcome across chunks or segments. Coalescing callbacks cannot
provide that guarantee. The later `send_delivered` enriches the same item.

The source message retains its stream position, timestamp, receiving provenance
and exactly-once recording; `delivery_entry_id` points to that delivery-order
record. Raw user transport is retired; source bytes, including the inert
acceptance reference, remain inputs to thread folding. Older messages without
that field retain their existing outcome-based join behavior; no identity is
inferred from text, timestamps, app IDs or reusable queue handles. See
[delivery reference validation and prefixes](thread-package-main-thread-folding-sources.md#accepted-sends-and-durable-outcomes).
Later confirmation acknowledges placement without appending or publishing again.

**Readiness protects recording and binding, independently of transport.**
`queuedSendHistory.accepted` appends and calls `bindItem` with the successful
durable ID before closing `ref.ready`. Removing its receipt notification must
retain both steps: placement can arrive before the acceptance callback, and a
queue action can take the item while acceptance is incomplete. No queue receipt
emitter, mailbox, worker or relay wiring remains. Queue callbacks perform no
peer I/O; source appends retain thread wakeups. Placement stays serialized with
stream output through `sendNowPlacement.bindQueued` and the stream drain's
placement command queue. Delayed confirmation and late echoes add no duplicate
source or canonical item. See
[placement capacity](msgqueue-package-lifecycle.md#delivered-notification-2115).

**Use the arbitrated terminal outcome, not a removal return value.** An
in-flight `Remove` returning true can express a deferred request. Confirmed
delivery still wins; only the queue's actual removed/give-up `OnTerminal`
outcome writes a drop. Retry, refused removal/send-now and shutdown alone
leave acceptance unresolved. See
[queue confirmation and races](msgqueue-package-lifecycle.md#delivered-notification-2115).

`reconcileStartupHistory` reads surviving registered conversation logs through
all raw `Store.Page` pages before any closure or restart divider and before
producers or inbound traffic start. `readStartupSends` resolves acceptance
entry IDs against durable delivery/drop/loss facts; old operator messages
without acceptances stay untouched. Failed/incomplete reads or malformed send
facts infer no loss for that conversation. `closeStartupSends` appends losses
without re-enqueueing, live publication or replay backfill. Valid losses become
fold sources after raw-query retirement. A successfully recorded loss resolves the acceptance on
subsequent starts; a failed loss write remains eligible for another startup
attempt. See
[the queue durability boundary](msgqueue-package.md#durability-boundary-in-scope-vs-out)
and [ADR 042](../decisions/042-daemon-built-thread.md).

Storage remains best effort: nil/failed history preserves queue acceptance,
delivery/removal and retained placement. Failed acceptance writes release the
placement gate without inventing an ID, message acceptance reference or linked
outcome; a surviving operator message remains an ordinary delivery. A failed message append creates no
`send_delivered`, leaving only the committed acceptance or intermediate written
fact. A failed `send_delivered` append leaves a committed linked message
delivered/inactive under its acceptance ID, with no fabricated `outcome` object
and no wait for a missing companion. `TestAcceptedDeliveryAppendFailures` covers
all three failures, including delivery after `send_written` with a failed final
append. Startup loss with reason `daemon_restart` records missing durable
confirmation or resolution, not proven non-consumption; the existing
[shutdown/confirmed-write gap](msgqueue-package-lifecycle.md#concurrency-model)
means it cannot prove the agent never received the bytes. This is no durable
backlog or gap-free crash guarantee. Failure logs use fixed event/reason
discriminants without raw errors, message content, sender keys or host paths.

`send_written` is an additional intermediate fact (#3508). Only successful Send
now queue delivery emits it; failed writes retain the backlog. Its native item
is visible and active with status `written`, retaining its acceptance ID and
no `order`. Placement on echo or normal completion alone supplies final delivery
chronology.
A per-acceptance leaf lock deduplicates write callbacks and orders them before
a racing final outcome without holding the global reference-map lock across
history I/O.
This fact has no legacy runtime receipt. It does not resolve a surviving
acceptance at startup: restart marks unconfirmed written input lost and never
replays it. A late/duplicate write cannot regress delivered or lost state.

### Resolved remote prompt answers (#2973)

`startRelayV2` wires the existing history store and exact-session harness lookup
into `streamApprovalBridge`. Remote permission resolutions through
`modalResolverV2` and question resolutions through `questionResolverV2` append
one `prompt_answered` fact per modal or question batch, through
`recordPromptAnswer` and `appendConversationHistory`. This covers the supported
Claude and Codex remote paths. Local control-socket approvals remain outside
this producer; see
[ADR 042's prompt decision](../decisions/042-daemon-built-thread.md#the-six-approved-decisions).

The payload carries `conversation_id`, the modal/batch `correlation_id`, the
asking session's daemon routing `session_id` when available, UTC `resolved_at`,
actor `source: remote`, `decision`, effective child `behavior`, `session_grant`,
safe `context` and explicit `truncated`. `resolved_at` also supplies the entry
timestamp. The payload's actor source is separate from optional
`Metadata.Session` provenance: known `claude` or `codex` plus the asking routing
ID. Unknown or unavailable harness provenance stays absent; it is never
inferred from the current conversation or defaulted to Claude or `none`.

`Surface` captures the original conversation, asking routing ID, harness and
permission tool/class or cloned question context beside the surface correlation.
Resolution snapshots that immutable owner under the bridge mutex before
actuating the parked approval, then appends outside bridge and registry locks.
Cursor movement, rebinding and agent switches cannot attribute an old answer
to a successor. Retirement removes live ownership, but cannot erase the snapshot
of a winning answer. A new surface reusing a parked tool ID gets a fresh
correlation and owner.

**The parked approval's winning result is the persistence boundary.**
`ResolveStream`, `AnswerQuestionDiagnostic` and `RefuseQuestionDiagnostic`
record only when `permbridge.Registry.Resolve` returns true. A consumed question
surface can still return success and emit its required dismissal after expiry
has already resolved the parked approval. Saving from that consumption or
dismissal would falsely retain an answer the child never accepted. Open prompts
and reconnect snapshots remain live-only. Unauthorized, invalid, stale,
duplicate and unrouted attempts, automatic timeout, disconnect and session
teardown write no answer fact; existing authorization, child verdicts and
single-dismissal contracts remain intact. See
[the parked registry arbiter](permbridge-package.md) and
[question surface ownership](questionbridge-package.md).

| Resolution | Saved decision and context |
| --- | --- |
| Permission allow | `decision: allow`, `behavior: allow`; `context.tool` and `context.class`. `session_grant` reflects the effective verdict's session grant or applied permission updates, rather than merely the client's request for always-allow. |
| Permission deny/cancel | Distinct `decision: deny` or `cancel`, both with `behavior: deny` and no session grant; retain tool/class. |
| Question answer | `decision: answer`, `behavior: allow`; each question has its zero-based `index`, `text`, `multi_select` and selected `values`. Each value retains `text`; a surfaced option-label match also retains its description as `meaning`. Unmatched values remain free text, and multi-select retains multiple values. |
| Question refusal | `decision: refusal`, `behavior: deny`; retain the question context without inventing selected values. |

A nil-device cancellation still performs the existing fail-closed deny and
dismissal, but cannot establish an authenticated remote operator decision, so
it writes no fact. A dismissal's remote source alone is insufficient evidence
of an operator answer.

`boundPromptAnswerProjection` caps the serialized `context` at **16 KiB,
including JSON escaping**. Question values are limited to 128 per question in
the saved projection, and text/meaning fields are shortened until its encoding
fits. Any cut sets the fact's `truncated: true`; the child receives the full
validated answer unchanged. This cap covers the context/answer projection,
not the outer fact's identity and decision fields.

The allowlist omits bearer/answer tokens, device credentials, raw transport and
updated-tool-input envelopes, permission reasons/descriptions/blocked paths,
grant-rule inputs and all permission tool inputs, including host paths.
Question text and selected option descriptions are intentionally retained in
the bounded question projection. Operational logs carry no prompt or answer
content, including forged option IDs; storage failures use fixed event/reason
discriminants without raw errors or storage paths.

Writes are best effort. Nil storage is silent; failed storage preserves
resolution and dismissal without retrying the answer or inventing a durable
success. Child delivery and storage are not crash-atomic. `historyEntryShown`
marks the fact explicitly `shown: true`, raising the raw shown-entry watermark
in warm and reopened logs. The raw `prompt_answered` payload
remains outside `legacyHistoryType` and all legacy transports.

**Answers are source facts, without receipt publication (#3737).**
`recordPromptAnswer` makes one best-effort append for each winning authenticated
answer and returns. `promptAnswerPublisher`, its serialization mutex, mailbox,
worker and receipt-only validation are removed. The bounded saved context and
original asking-session provenance remain; canonical thread notices and explicit
catch-up consume the fact. No app receives an inert banner or new answer replay
copy, including peers without `thread`. Failed storage never retries actuation
or restores publication.

**Connection discovery must not run on the answer-recording path (#3279).**
The relay manager dispatch loop can itself resolve a prompt. A synchronous
`ActiveConns` query from that stack waits on the same loop: the child may receive
its verdict while dismissal and later relay traffic deadlock. Releasing bridge
and registry locks alone cannot break that cycle. Removing receipt delivery
eliminates the connection query and its serialization from recording entirely.
`TestPromptAnswerRelayRemainsResponsive` exercises all five decisions through
the encrypted manager path and checks dismissal and continued dispatch.
`TestPromptAnswerBlockedDeliveryDoesNotBlockNextAnswer` holds an unrelated
connection query while consecutive answers complete, including cancellation and
failed history. Both retain their winning verdicts and one dismissal each;
healthy storage retains original history without receipt delivery. These race
regressions are deterministic proofs, separate from counted real-Claude
acceptance. See the
[retirement evidence](thread-package-shadow-lifecycle-and-evidence-retirement.md#residual-content-retirement-3737).

The raw pager remains retired. Source metadata and shown-thread authority keep
their contracts; receipt arrival is no longer an accounting or presentation path.
See [stored visibility versus transport eligibility](history-package-producers-legacy-compatibility.md#legacy-eligibility-and-explicit-visibility-2965).
Daemon prompt acceptance alone establishes neither FCM delivery nor mobile
notification preview privacy.

`TestPromptAnswerHistoryPermission` and `TestPromptAnswerHistoryQuestion` use
the production resolvers/bridge and real store to check decisions, original
ownership, exactly-once capture and raw/receipt publication exclusion. The
after-consumption expiry fixture in `TestPromptAnswerHistoryLosingResolution` distinguishes
surface success from a winning parked resolution. `TestPromptAnswerHistoryRetirement`
checks that retirement cannot erase captured ownership;
`TestPromptAnswerHistoryUnauthenticatedCancel` prevents invented operator
answers. Bounded-projection and storage tests cover escaping, unchanged child
answers and content-free failures. `TestPromptAnswerPublicationRetired` checks
all five decisions, connected/disconnected clients, rebinding and duplicate
attempts while checking canonical answer notices under the original asking-session
provenance. `TestPromptAnswerConcurrent` checks that
concurrent duplicate answers produce one source fact per winning approval.
`TestInteractiveStreamStdioModalAllow`,
`TestInteractiveStreamStdioPermissionDeny`, `TestInteractiveStreamQuestionAnswer`
and `TestInteractiveStreamQuestionRefusal` assert the raw durable fact after
actual Claude resolution; the deterministic resolver tests also cover Codex.
