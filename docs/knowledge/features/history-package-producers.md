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
The existing legacy emitters resolve the four event values — conversation
id, wire type, marshalled payload, one hoisted timestamp — for the ring
append or the fan-out itself, so their log append needed no new mapping, only
a nil-guarded call before the per-conn loop in each. Operator history resolves them
from a safe `msgqueue.QueuedMessage`, available before the write through
`msgqueue.DeliveryMessage` and again at confirmation through `OnDelivered`,
not from an envelope in flight (see
[delivery ordering](history-package-producers-legacy-compatibility.md#legacy-eligibility-and-explicit-visibility-2965)).

### Producer topics

| Document | Topics |
| --- | --- |
| [Interactive provenance](history-package-producers-interactive-provenance.md) | Captured source, retained text and child lanes, conversation phase and channel posts. |
| [Runtime and startup lifecycle](history-package-producers-runtime-lifecycle.md) | Boundaries, interrupted main work, agent/task identities and endings, linked-work deduplication and exactly-once restart recovery. |
| [Legacy compatibility and verification](history-package-producers-legacy-compatibility.md) | Explicit raw visibility, nonvisual receipts, legacy unread targets, delivery ordering and producer tests. |

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
After successful storage, #3026's
validated nonvisual receipt retains the original durable ID and timestamp;
\#3029 counts it toward the legacy unread target independently of raw visibility.
That accounting confers no foreground presentation or read-mark advancement by
itself. See [receipt validation](history-package-producers-legacy-compatibility.md#legacy-eligibility-and-explicit-visibility-2965).

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
unsupported agent facts leave provenance absent while eligible legacy delivery
continues. A nonempty unsupported captured agent is not replaced by a lookup.
Delayed `broadcast` never looks up an agent or substitutes a replacement
session for provenance, even if an unavailable lookup later becomes available;
it neither defaults to Claude nor invents `none`.

A nonempty captured `ConversationID` is authoritative for both history and
live routing after another rotation, pool removal or registry replacement.
`toWirePayload` retains that owner. Only when ownership is absent may
`broadcast` use the existing exact-session conversation resolver; if ownership
remains unresolved, it drops the event without guessing a conversation.
See [captured lifecycle facts and switch publication](sessions-package-key-types-transition-observer.md)
and [ADR 042's session provenance](../decisions/042-daemon-built-thread.md#sessions-agents-messages-read-marks).

Raw `Store.Page` exposes captured metadata in warm and reopened stores; old
untagged entries remain untouched. Visibility stays `clear` shown and
`idle_evict` hidden. Legacy live/page payload meanings, durable identities,
unread watermarks and recipient gates retain their behavior, including eviction's
mirrored wire session IDs. Nil/failed storage still allows eligible fan-out with
absent history identity. The compatibility enqueue remains nonblocking and drops
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
facts feed canonical thread items and explicit catch-up. Neutral receipts keep
their original durable identity and existing eligible recipients, and failed
storage cannot mint one.

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
automatically re-enqueued or replayed. Abandonment appends no `send_delivered`,
publishes no delivered receipt and invents no child-exit loss reason.
Later callbacks or predecessor/successor events cannot revive the placement.

An actually empty binding is explicit `kind: none` with no session ID. Unknown
or unavailable source facts remain absent. Delivery retains the
[receiving-session snapshot](#operator-delivery-provenance-2983), even after
rebinding; startup never substitutes a replacement session for the acceptance.
Hidden drops do not raise the raw displayable watermark. The original four raw payloads
stay off legacy history, live publication and reconnect replay. After a
successful append, `legacyRuntimeReceipt` projects validated facts as empty
info banners at their original durable IDs and timestamps (#3167). Acceptance
requires `accepted_at`; outcomes require `occurred_at`, `accepted_entry_id` and
the exact reason in the table, with `delivery_entry_id` also required for
delivery. Sender/message fields remain optional. Malformed/unknown facts stay
holes. Receipts carry no send text, attachments, sender fields or raw facts and
confer no sight or lifecycle changes. Receipt acceptance supplies no shown-thread
or read-mark authority, including for hidden removal/give-up. Raw visibility,
provenance, internal watermarks and ID allocation retain their contracts. See
[legacy validation and publication](history-package-producers-legacy-compatibility.md#legacy-eligibility-and-explicit-visibility-2965).

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

**Keep receipt notification off the peer fan-out lock (#3167).** Acceptance
appends and enqueues its receipt before releasing `ready`; placement waits for
that barrier. If notification acquired `operatorMessageEmitterV2`'s fan-out
mutex, a stalled peer could block acceptance and therefore placement.
`notifyReceipt` instead uses a separate mutex for a bounded mailbox and a
nonblocking wake. `Run` drains pending receipts through `broadcast` under the
fan-out mutex. After recording the source and linked delivered outcome,
`operatorMessageHistory` submits only that outcome's neutral receipt through
`queuedSendHistory.publish`, independently of any raw user frame. Receipt
notification never acquires the fan-out mutex or performs peer I/O; fan-out
holds the mailbox mutex only while taking pending items. The worker preserves
acceptance-before-outcome receipt order, while unrelated assistant traffic can
interleave. A full mailbox logs a content-free discriminant and leaves explicit
thread catch-up as recovery. Parent cancellation stops the worker and cleanup
joins it; late producers can only fill the bounded unread mailbox. Delayed
confirmation and late echoes add no duplicate source or receipt.

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
delivery/removal and retained placement. Nil/failed appends mint no receipt
identity. Failed acceptance writes release the placement gate without inventing
an ID, message acceptance reference or linked outcome; a surviving operator
message remains an ordinary delivery. A failed message append creates no
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
is visible and active with status `written`, at its unchanged acceptance order.
Placement on echo or normal completion alone supplies final delivery chronology.
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

**Validated answers account for their original IDs (#3222).**
`legacyRuntimeReceipt` delegates to `validPromptAnswerFact`, then reconstructs
only the five-field nonvisual `banner`:
`{conversation_id, level:"info", text:"", truncated:false, stops_turn:false}`.
It requires a nonzero durable ID and entry timestamp, matching conversation,
nonempty original `correlation_id`, exactly `source: remote` and nonzero
`resolved_at` equal to the entry timestamp. All producer fields above except
`session_id` must be present and nonnull, including explicit boolean
`session_grant` and `truncated`. Only `allow`/`answer` with `behavior: allow`
and `deny`/`cancel`/`refusal` with `behavior: deny` qualify; denied decisions
cannot claim a session grant.

`context` must be an object whose encoded size is at most 16 KiB. Typed decoding
rejects unknown context/question/value fields and malformed shapes. Optional
`tool` and `class` are strings; optional `questions` contains at most four
questions with consecutive zero-based `index`, string `text` and explicit
boolean `multi_select`. Each question may retain at most 128 values, each with
required string `text` and optional string `meaning`. An empty context object
is valid. Optional asking-session identity and original metadata provenance
remain optional; validation never consults today's active session or stored
`shown`. Invalid, foreign, unknown-source/decision, missing-field or unidentified
facts remain holes. Raw decisions, prompt/question/tool context, answers,
correlation/session identities and provenance never cross in the receipt.

**Serialize successful append and replay admission together.** `recordPromptAnswer`
uses `promptAnswerPublisher`'s dedicated mutex across its single durable append
and `enqueue`, outside bridge and registry locks. `enqueue` records the validated
receipt into the shared replay ring before admitting live delivery, even without
connected clients. One ordered worker, `promptAnswerPublisher.deliver`, takes
receipts under that mutex and releases it before `ActiveConns` or `Push`.
Concurrent answers therefore retain append/replay order without waiting for
connection discovery or peer delivery.

The mailbox holds at most 256 pending live receipts. Overflow logs a fixed,
content-free warning and drops only live delivery; the successful history append
and replay admission remain intact. Context cancellation releases blocked
delivery and clears pending live work without removing history or replay.
Neither held delivery nor cancellation blocks recording later answers.
`startRelayV2` installs the publisher and its shared ring
before manager answer workers start: attaching the ring at the later interactive
emitter setup would race early answers. Existing interactive, conversation
membership and agent-access gates apply to the captured original conversation.
Nil/failed appends and duplicate/losing resolutions produce no receipt.

**Connection discovery must not run on the answer-recording path (#3279).**
The relay manager dispatch loop can itself resolve a prompt; synchronous
`publishLegacyHistory` fan-out from `recordPromptAnswer` then waits for
`ActiveConns` to query that same loop. The child can receive its verdict while
dismissals and subsequent relay traffic deadlock. Merely releasing bridge and
registry locks does not break that cycle. Delivery must leave both the recording
lock and the manager dispatch call stack.

`TestPromptAnswerRelayRemainsResponsive` exercises all five decisions through
the encrypted manager path and checks continued relay progress.
`TestPromptAnswerBlockedDeliveryDoesNotBlockNextAnswer` holds connection discovery
while two answers complete, then checks ordered original history/event identities
or cancellation with both history/replay entries retained. These race regressions
prove the repaired boundary; they do not establish real-Claude acceptance.
A merged repair or offline pass leaves named live acceptance pending until a
counted full gate records each required leaf, including subtests, against the
actual daemon source and Claude version. See
[the separate #3256 live evidence](../../specs/architecture/3256-shared-live-repair-evidence.md#revisions).

History, live publication and reconnect replay use identical receipt bytes and
the original durable ID/timestamp, without another append. Valid receipts count
toward legacy latest regardless of absent/false/true visibility, but confer no
presentation, prompt actuation or read-mark advancement. The raw pager is
retired; source history, metadata and authoritative thread behavior remain. See
[visibility versus transport eligibility](history-package-producers-legacy-compatibility.md#legacy-eligibility-and-explicit-visibility-2965)
and [the durable join](../../protocol-mobile.md#legacy-receipt-identity).
Mobile [#2098](https://github.com/pyrycode/pyrycode-mobile/issues/2098) owns the
pending live notification gate; deterministic daemon accounting does not prove
background completion, FCM delivery or notification preview privacy.

`TestPromptAnswerHistoryPermission` and `TestPromptAnswerHistoryQuestion` use
the production resolvers/bridge and real store to check decisions, original
ownership, exactly-once capture, raw legacy exclusion and safe receipts. The
after-consumption expiry fixture in `TestPromptAnswerHistoryLosingResolution` distinguishes
surface success from a winning parked resolution. `TestPromptAnswerHistoryRetirement`
checks that retirement cannot erase captured ownership;
`TestPromptAnswerHistoryUnauthenticatedCancel` prevents invented operator
answers. Bounded-projection and storage tests cover escaping, unchanged child
answers and content-free failures. `TestPromptAnswerReceiptsValidation` exercises
all decision pairs and rejected fields through the retained receipt projection.
`TestPromptAnswerReceiptsPublication` compares projected source/live/replay bytes
and identity with and without clients, including rebinding and duplicate attempts;
`TestPromptAnswerReceiptsConcurrent` checks serialized order under concurrent
duplicate answers. These tests retain receipt behavior without a raw wire pager.
`TestInteractiveStreamStdioModalAllow`,
`TestInteractiveStreamStdioPermissionDeny`, `TestInteractiveStreamQuestionAnswer`
and `TestInteractiveStreamQuestionRefusal` assert the raw durable fact after
actual Claude resolution; the deterministic resolver tests also cover Codex.
