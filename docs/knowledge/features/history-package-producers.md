# `internal/history` — history producers

Part of [the history package overview](history-package.md).

## Producers (#2114, #2115)

Interactive output, session transitions, runtime/startup and agent/task facts,
accepted-send facts and delivered operator messages in `cmd/pyry` append through
one seam, `appendConversationHistory` (`cmd/pyry/conversation_history.go`): the
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
payloads stay excluded from legacy history pages, live delivery and replay.
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
before signaling the write outcome. Ordinary echo/idle placement and send-now
echo/idle fallback pass it to the safe producer when they commit. Successful
Codex/no-stream writes retain it by conversation and queue ID until
`sendNowPlacement.takeSource` consumes it at confirmation. Failed writes retain
no source and create no delivered entry; a retry captures its own receiving
session. Existing placement order and exactly-once recording, including late
echoes and callbacks, remain intact.

`newOperatorMessageHistory` still builds history and publication content only
from the safe `msgqueue.QueuedMessage` projection: client-readable text,
attachment IDs and existing client metadata. Composed prompts, echoed delivery
text, host paths and private matching digests remain absent from payloads and
logs. `appendConversationHistory` combines the captured source with existing
visibility; raw pages expose it in warm and reopened stores. Legacy entries
stay untagged, and callers without source information remain usable with
absent provenance, never inferred as `none`. Legacy live/replay/page payloads,
unread watermarks, durable identity and recipient gates retain their behavior,
including eligible publication with absent identity on nil/failed storage.

### Accepted sends and linked outcomes (#2972)

`runSupervisor` installs one `queuedSendHistory` on the existing queue and
placement paths. `OnAccepted` writes `send_accepted` from the safe
`msgqueue.QueuedMessage` projection: `conversation_id`, authenticated opaque
`device_id`, app `message_id`, client-readable `text`, `attachment_ids`,
`accepted_at` from the queue's acceptance timestamp, and `client_sent_at` when
the reported time was parsed. Reported time uses UTC RFC 3339 with nanosecond
precision. Optional absent values are omitted. Composed delivery bytes, echoed
prompts and host attachment paths never enter these facts.

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
| `send_delivered` | `accepted_entry_id` plus `delivery_entry_id` naming the existing operator `message`; reason `delivered` for ordinary and send-now delivery. | Shown; captured receiving session, shared with the operator message. |
| `send_dropped` | `accepted_entry_id`; reason `removed` or `give_up` only after actual removal or abandonment. | Hidden; bound session captured at terminal observation. |
| `send_lost` | `accepted_entry_id`; reason `daemon_restart` for a surviving acceptance without a durable outcome. | Shown; original acceptance's source. |

An actually empty binding is explicit `kind: none` with no session ID. Unknown
or unavailable source facts remain absent. Delivery retains the
[receiving-session snapshot](#operator-delivery-provenance-2983), even after
rebinding; startup never substitutes a replacement session for the acceptance.
Hidden drops do not raise unread watermarks. All four fact types stay excluded
from legacy history pages, live publication and reconnect replay, including
nonvisual receipts. `shown: true` supplies visibility for the future thread;
it does not authorize legacy transport or implement a thread fold/protocol.

**Gate placement itself on acceptance completion.** Queue callback ordering
cannot protect the earlier echo/idle commit: placement can reach the producer
before acceptance observation and before a delayed `OnDelivered`.
`queuedSendHistory.beforeDelivery` waits for the synchronous acceptance append
to finish, including failure, before `operatorMessageHistory` appends the
existing operator message and its linked delivery fact. Waiting holds no
observer mutex; acceptance never waits for the stream drain. The operator
message retains its stream position, timestamp and exactly-once legacy push;
`delivery_entry_id` points to that delivery-order record. Later confirmation
acknowledges placement without appending or publishing again.

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
without re-enqueueing, live publication or replay backfill. A successfully
recorded loss resolves the acceptance on subsequent starts; a failed loss
write remains eligible for another startup attempt. See
[the queue durability boundary](msgqueue-package.md#durability-boundary-in-scope-vs-out)
and [ADR 042](../decisions/042-daemon-built-thread.md).

Storage remains best effort: nil/failed history preserves queue acceptance,
delivery/removal and eligible legacy push. Failed acceptance writes release
the placement gate without inventing an ID or any linked outcome. Delivery
linkage additionally requires a successful operator-message append. A failed
outcome append can leave a durable acceptance unresolved. Startup loss records
that missing durable resolution; the existing
[shutdown/confirmed-write gap](msgqueue-package-lifecycle.md#concurrency-model)
means it cannot prove the agent never received the bytes. This is no durable
backlog or gap-free crash guarantee. Failure logs use fixed event/reason
discriminants without raw errors, message content, sender keys or host paths.
