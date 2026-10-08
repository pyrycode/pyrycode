# `internal/history` — history producers

Part of [the history package overview](history-package.md).

## Producers (#2114, #2115)

Interactive output, session transitions, runtime lifecycle facts and delivered
operator messages in `cmd/pyry` append through one seam,
`appendConversationHistory` (`cmd/pyry/conversation_history.go`): the
interactive emitter's `emit` chokepoint
(`cmd/pyry/interactive_turn_v2.go`), session transitions' `broadcast`
(`cmd/pyry/session_transition_v2.go`), and (#2115) `newOperatorMessageHistory`
(`cmd/pyry/operator_message_history.go`), used by queued stream placement and
`msgqueue.Config.OnDelivered`. Runtime opening/interruption facts use
`interactiveTurnEmitterV2.recordRuntimeFact` through the same append seam.
The existing legacy emitters resolve the four event values — conversation
id, wire type, marshalled payload, one hoisted timestamp — for the ring
append or the fan-out itself, so their log append needed no new mapping, only
a nil-guarded call before the per-conn loop in each. Operator history resolves them
from a safe `msgqueue.QueuedMessage`, available before the write through
`msgqueue.DeliveryMessage` and again at confirmation through `OnDelivered`,
not from an envelope in flight (see below).

### Interactive provenance (#2981)

Capture the producer before fan-in and retain it through flush.
`newStreamRunnerFactory` and `newCodexRunnerFactory` supply fixed `claude` and
`codex` kinds to `streamTurnSink.sinkForSessionTag`, which reads the daemon routing tag
once per event into `streamTurnEnvelope.source`. The drain resolves that
envelope's session, including historical bindings, before passing its captured
provenance and runtime incarnation to `interactiveTurnEmitterV2.handleForSource`;
`HandleFor` retains the incarnation-zero entry surface. Unresolved sessions still
drop. Neither the active cursor nor subprocess fields supply attribution.
An append-time `CurrentSessionID` lookup would assign a delayed predecessor's
event to its successor. These IDs are daemon routing IDs, distinct from
conversation IDs and Codex thread IDs.

`convTurnState` retains provenance by value, keyed by conversation, agent kind
and session ID, plus producer incarnation when runtime history is enabled.
Main/child buffered text, child lane identities and lifecycle
state therefore keep their source through timer flush, rotation and closure.
Reused message or parent IDs cannot merge different
sources. Separate buffers alone would still join old-source text across an
intervening successor event and reorder timer output: `HandleFor` flushes the
preceding source before selecting another, leaving at most one text buffer per
conversation. Other conversations' buffers remain independent.

**Project conversation phase separately from source lifecycle.** `turn_end`
keeps its producing source, but `turn_state` describes the most recent retained
main-phase activity in the conversation. Ending another source preserves the
running projection; ending its owner restores the latest remaining running
phase with that source's provenance. Repeated activity in the same phase
updates ownership before conversation-level deduplication. Deduplicating only
within each source would let a delayed predecessor end clear the successor's
reconnect snapshot, while subsequent responding chunks silently skip repair.
The compatibility conversation-wide close processes older phases first and emits
one final idle without inventing a `turn_end`. Runtime exits and boundaries close
only the captured dying source, preserving replacement work and other sources.
See [connect-time phase reconciliation](v2-session-manager-state-machine-connect-time-turn-phase-reconcile-running.md)
and [retained emitter state](streamsup-package-draining-turnevents-into-the-interactive-emitter.md).

`appendConversationHistory` combines optional captured provenance with existing
visibility metadata. Raw `Store.Page` exposes it in warm and reopened stores.
Omitted or unknown source stays absent, never inferred as `none`; legacy entries
remain untagged and are not rewritten. Nil/failed storage still permits eligible
legacy delivery with absent history identity.

Relay correlation identity is the authenticated pairing's bound Noise public
key (`dispatch.Conn.Auth().StaticKey`), in its existing lowercase-hex form,
separate from display name and the verbatim app message id (#2971).
Names can change or coincide; distinct keys distinguish senders with equal
names and app ids, while renaming or reconnecting the same install preserves
identity. Missing authentication or an empty key yields empty identity, with
no fallback to name, connection id, token or token hash. Client-authored fields
cannot override it, and identity and credentials stay out of logs.
`handlers.SendMessage` offers this metadata through the optional
`EnqueueIdentified` path; legacy-only queues still receive `EnqueueSent`.
The production `suggestionEnqueuer` wrapper and daemon/history adoption belong
to [#2972](https://github.com/pyrycode/pyrycode/issues/2972). This seam adds no
deduplication, new wire field or sender identity to legacy event payloads.
See [relay sender metadata](relay-package-handlers.md#send_message-grows-a-ninth-seam-sender-identity-and-tap-time-2704)
and [ADR 042's item model](../decisions/042-daemon-built-thread.md#item-model).

Channel posts are a fourth writer: `channelDelivery.deliver` bypasses the
common seam and calls `AppendWithMetadata` through `channelDeliveryHistory`
for each assistant delta and its `channelPostTurnEndPayload`. It uses the
same `historyVisibilityMetadata` classifier: post text is shown, completion
is hidden. All history writes must succeed before publication; see
[channel delivery and recovery](control-plane-channel-post-live-delivery.md#live-announcements-bounded-replay-and-durable-recovery).

`channelDelivery.accept` captures session provenance once, after `channelPoster`
resolves or creates the conversation (#2984). `channelPostSession` reads the
resolved conversation afresh through `Registry.Get`, so an earlier name-match
row cannot supply a stale binding. A nonempty `Conversation.CurrentSessionID`
is the daemon session routing ID; `Pool.HarnessFor` supplies its `claude` or
`codex` kind from live or dormant state without starting or reviving a child.
This ID is distinct from the conversation ID and Codex thread ID; client
payloads supply none of these provenance facts. See
[session binding](conversation-session-binding.md) and
[the pool's agent lookup](sessions-package-key-types-pool-settingsfor.md#poolharnessfor-2629).

A known empty binding captures explicit `none` with no session ID. Missing
conversation or agent facts, an unavailable lookup, lookup errors and
unsupported agents leave provenance absent/unknown, without refusing the post
or defaulting to Claude. Acceptance copies the snapshot into
`channelDeliveryPost.Session` and persists it with the pending post. Every newly
appended delta chunk and completion uses that same snapshot through deferred
delivery, partial-write retry and restart, even after rebinding or switching
agents. Loading validates a captured kind/ID pair but never looks up the current
binding: older pending records stay absent, and explicit `none` stays `none`.
Raw `Store.Page` exposes the saved metadata in warm and reopened stores; legacy
history remains untagged and is never inferred or rewritten.

**Reconcile payloads independently of metadata.** A stored untagged prefix can
coexist with a pending post's captured snapshot. Requiring provenance equality
would duplicate that prefix. `channelDelivery.deliver` matches chunk and
completion payloads across every raw page, appends only missing entries with
the saved provenance and leaves existing entries unchanged. A fully recorded
post requires cleanup only, with no repeated announcement.

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

### Runtime boundaries and main-work closure (#3013)

#### Divider causes and reset outcomes

Runtime facts use `SessionTransition.Cause` independently of legacy `Reason`.
`runtimeDivider` accepts a captured owner, a nonempty predecessor and a changed
routing pair; first creation, equal-ID/no-op and refused transitions produce no
divider. Unknown causes and unresolved ownership do not invent one. Each actual
boundary appends one `session_divider`, retaining `conversation_id`, `cause`,
`occurred_at`, known `previous_session_id` / `new_session_id` and
`previous_agent` / `next_agent`. A switch retains both actual agents. Divider
metadata names the captured predecessor when its routing ID and harness are
known; unavailable source metadata remains absent. The facts use daemon routing
IDs, never native agent thread IDs or a later active binding.

| Runtime divider cause | Explicit visibility | Legacy delimiter |
| --- | --- | --- |
| `operator_reset` | Shown | `clear` |
| `claude_clear` | Shown | `clear` |
| `agent_switch` | Shown | `clear` |
| `recovery` | Shown | None |
| `workspace_change` | Shown when supplied | None |
| `idle_sleep` | Hidden | `idle_evict` |
| `capacity_eviction` | Shown | `idle_evict` |

Capacity eviction retains its own closure cause even though its legacy delimiter
is the same hidden `idle_evict` as sleep. Both lack a successor in the raw
divider; legacy wire payloads still mirror the evicted ID into both fields.
Workspace change is vocabulary with no current producer. Recovery onto a new
session can record a divider, but `RotateBootstrapForSelfHeal` remains uncalled
in production: this history path adds no recovery or self-heal policy.

`activeSessionStarter.resetThenRotate` passes the completed wrap-up's actual
boolean to `Pool.RotateForNewSessionWithHandoff`: `written` when a note was
written and `skipped` otherwise. Only an operator-reset divider carries the
optional `reset_handoff_outcome`. Callers without that observation, including
`RotateForNewSession`, leave it absent/unknown. Invalid values such as `pending`
or `assumed` are omitted; neither timing nor a successful rotation proves that
a handoff note was written. The outcome carries no note text.

#### Hidden openings and interrupted main work

`startTurnIfNeeded` writes hidden `main_turn_opened` once with the conversation,
minted turn ID, occurrence time and captured source metadata. Thinking alone can
open the turn; the fact preserves its identity without persisting thought text,
tool inputs or prompts. Live-state readings still do not become thread items.
`trackRuntimeTool` retains unfinished ordinary main-thread tools by their existing
tool call ID and name. Successful/failed results and denied calls retire them;
an ordinary turn end retires the turn. Agent/Task launchers and child tools are
excluded from this closure vocabulary; their lifecycle and background-task
endings remain [#2969](https://github.com/pyrycode/pyrycode/issues/2969).

The drain's `closeRuntimeSource` flushes buffered predecessor text, writes shown
`main_tool_interrupted` for each unfinished ordinary main call, then writes one
shown `main_turn_interrupted` for the open turn before the divider. Endings retain
the existing tool/turn identities, actual closure cause, occurrence time and
source metadata. Repeated closure adds no second ending; already ended turns,
completed/failed tools and denied calls are not interrupted again. A child crash
retaining the session closes preceding work with `child_exit` and no divider.
Exit epochs protect newer same-session work and delivery reservations from stale
exits, including reservations made before replacement output. Rotation retains
the retiring source for the exit callback: reading only the current routing tag
or last output source can instead identify the successor.

#### Publication ordering

Ordinary callbacks capture and retain boundaries under the short
output-acceptance lock, without storage,
network I/O or waiting for the drain. Each boundary waits for its predecessor's
accepted-output watermark; delayed A→B→C facts retain both routing pairs and their
captured owners. Evictions also wait for the matching consumed producer stop,
including late parsed tails, so closure retains the eviction cause rather than
falling back to `child_exit`. Interactive output, confirmation-only operator
placement and channel posts share drain/publication coordination: old buffered
text precedes interruption, interruption precedes the divider, and successor
entries follow it in durable entry-ID order. Confirmation placement retains its
whole-queue fence. Never wait for the drain while holding the post gate.

A pending channel hold lasts until boundary publication finishes, even after
the boundary leaves its pending queue; releasing it at dequeue lets a post pass
the divider. Delivery release checks retained open main turns, not just running
phase projection: a denial can open successor work without a running phase.
Committed switches wait through transition publication, row publication and
transport sealing before reset exclusion releases. Both relay configurations
install this runtime path, including history-only operation.

#### Producer incarnation isolation

Idle sleep and capacity eviction reactivate under the same routing ID.
`beginRuntimeProducer` allocates a fresh
daemon-local incarnation before each Claude/Codex `Run` starts its output
goroutines. Envelopes, retained stops, source state, accepted-output watermarks
and captured boundaries carry it; durable/wire provenance keeps the existing
routing identity. Sealing only the ID would suppress the replacement's hidden
opening and terminal idle. `streamSessionTag.Rotate` and successful
`CompareAndSwap` register every routing alias under the same acceptance lock as
boundary capture, before any output. Registering only on first output leaves a
chained boundary or pre-output eviction targeting incarnation zero, reopening
old work or waiting forever for the wrong stop. Refused announced clears preserve
another live tag's destination ownership; older unbound callers retain zero.

Delayed sealed-predecessor events keep their captured provenance and closed turn
address without opening work or changing successor busy/idle placement. Late text
retains chunk bounds and sequence progression. Other conversations stay untouched.

#### Legacy exclusion and write failures

All four runtime types are history-only: hidden openings, shown interruptions
and even shown dividers remain excluded from legacy pages, ring replay and live
traffic by `legacyHistoryType`. Visibility never grants transport eligibility.
Best-effort nil/failed storage does not change closure, sealing, eligible legacy
publication, payload meanings or recipient gates. Append failures use the existing
content-free discriminants, never payloads or filesystem error text. Daemon-start
reconciliation remains [#3014](https://github.com/pyrycode/pyrycode/issues/3014).
See [ADR 042](../decisions/042-daemon-built-thread.md#sessions-agents-messages-read-marks)
and [drain source lifecycle](streamsup-package-draining-turnevents-into-the-interactive-emitter.md).

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

### Legacy eligibility and explicit visibility (#2965)

`legacyHistoryType` is a fixed allowlist of the existing producer vocabulary
from `turnbridge.MapEvent`/`MapState`, operator messages and session transitions.
A new history-only type is excluded by default from every connection's legacy
history, live fan-out and reconnect replay, whether its stored `Shown` is nil,
false or true. Visibility controls the
[unread watermark](history-package-watermarks.md#unread-state-uses-a-separate-lazily-recovered-watermark-2954),
not permission to reach a transport. Conversely, an eligible legacy event still
reaches its existing recipients and history pages when `Shown` is false,
including normal turn ends, info banners and live status readings. This preserves
[ADR 042's compatibility boundary](../decisions/042-daemon-built-thread.md#compatibility):
old apps treat unknown history entries as read-mark barriers.

`interactiveTurnEmitterV2.emit` appends once before checking this allowlist,
including when no clients are connected. An excluded fact returns before
`Ring.AppendWithHistoryID` or recipient enumeration, so it cannot enter live
delivery or reconnect replay, even if storage is absent or fails. Successful
appends remain readable through raw `Store.Page` after reopening. Raw storage
accepts arbitrary types; the transport vocabulary is closed independently.

All four existing writers now use `AppendWithMetadata` with explicit
`Metadata.Shown`, classified from the already-marshalled payload by
`historyEntryShown`. Interactive output, channel posts, session transitions and
delivered operator messages also use captured `Metadata.Session` when source
facts are available; unknown provenance stays absent.
Neither `shown` nor `session` is added to legacy wire payloads. Stored payloads, timestamps, durable IDs and existing
recipient gates keep their original meaning.

| Entry | Explicit visibility for new writes |
| --- | --- |
| `turn_end` | Hidden only when `StopReason == "end_turn"`, `IsError == false`, `Outcome` is absent/empty or `success`, `TerminalReason` is absent/empty or `completed`, and `ErrorCategory` is absent/empty. All other ends are shown: cancellation, limits, unknown stop reasons, `end_turn` with `error_max_turns`, or `success` with `is_error: true`. Channel-post completion satisfies the hidden case. |
| `banner` | Hidden only for `Level == "info"` with `StopsTurn == false`; stopping info banners, other levels and unknown levels are shown. |
| Live readings | Hidden: `turn_state`, `stall`, `api_retry`, `compacting`, `tool_progress`, `thinking_progress`, `background_task_roster`, `background_task_progress`, `rate_limited`, `context_usage`, `model_announced`, `session_facts`, `mcp_status`, `model_list`, `slash_command_list`. |
| `background_task_updated` | Hidden for patch-only updates; shown when `Status` or `Summary` is nonempty. |
| Content | Shown: `message`, `assistant_delta`, `tool_use`, `tool_result`, `tool_denied`, `background_task_started`, `compaction_boundary`, `model_refusal_fallback`, `model_refusal_no_fallback`, `unrecognized_message`. |
| `session_transition` | `clear` is shown; `idle_evict` is hidden. |
| Runtime history-only facts | `main_turn_opened` is hidden; `main_tool_interrupted` and `main_turn_interrupted` are shown; `session_divider` is hidden only for `idle_sleep`. All remain ineligible for legacy delivery. |
| New history-only types | Hidden by default in the common append seam, and ineligible for legacy delivery independently of explicit visibility supplied by another producer. |

**Task-update status is an open terminal-notification contract.** Any nonempty
status counts as shown, including unfamiliar values; restricting the classifier
to known terminal words would silently hide future completion facts. Conditional
legacy payloads that fail decoding conservatively classify as shown; classification
does not rewrite or reject their payloads.

Previously stored entries are neither rewritten nor reclassified. Their absent
visibility retains the store's legacy type fallback, which excludes only
`turn_state`, `stall`, `api_retry`, `compacting` and `session_transition`.
Explicit visibility governs the same unread watermark in warm and reopened
stores. Raw pages still include hidden entries and retain their durable IDs.

**Carry the append result, not a second lookup or another counter (#2861).**
`appendConversationHistory` returns the successful `Store.AppendWithMetadata`
id as an immutable `*uint64`, shared by every direct live recipient as
`Envelope.HistoryEntryID`. The operator commit carries it with the safe payload
and placement timestamp through `operatorMessage` to
`operatorMessageEmitterV2.broadcast`; reconstructing it at broadcast would lose
the association with that exact append. Append once before fan-out, even without
recipients. A transition has no ring id, and an operator emitter needs no ring
to carry this durable identity. The connection counter and ring cursor cannot
substitute for the stored per-conversation id. See
[envelope identities](protocol-package-types-envelope.md#replay-cursors-and-durable-read-marks)
for read-mark use and reconnect replay's retained identity.

**History must finish before ring publication (#2909).**
`interactiveTurnEmitterV2.emit` obtains the append result before
`Ring.AppendWithHistoryID` publishes the complete event; publishing first and
adding metadata later lets concurrent replay observe an incomplete record.
`operatorMessageEmitterV2.broadcast` passes the already-committed id through the
same ring seam without another history write. Both paths retain the id even with
no live recipients. Replay uses that original identity and never appends history;
absent/failed storage leaves zero ring metadata and an omitted wire key. Channel
posts retain their missing-id behavior, and transitions remain outside the ring.
See [ring publication](eventring-package.md#concurrency).

**Why this producer reads `text`, never the delivered payload.** Since #2038
a queued message carries two strings — `text` (client-readable) and
`delivery` (what reaches claude's stdin, which for an attachment-bearing
message names an on-host path `docs/protocol-mobile.md` § Error codes
forbids serving to a paired device). `newInboundDeliver` sees only
`delivery` in its payload argument. `OnDelivered` and the attempt-context
accessor `DeliveryMessage` carry `QueuedMessage` instead, which declares no
`delivery` field, making the omission structural rather than a filter this producer could forget.
Full detail:
[msgqueue-package.md § Delivered notification (#2115)](msgqueue-package-lifecycle.md#delivered-notification-2115).

**The stored entry keeps its attachment ids too, by the same structural argument (#2596).** `QueuedMessage.AttachmentIDs` is the ids a `send_message` named, as `internal/relay/handlers.resolveAttachments` resolved them — deduplicated, each past the canonical-shape check — copied onto the queue record independently of `delivery`. This producer sets `protocol.MessagePayload.AttachmentIDs` straight from `msg.AttachmentIDs`; since it still reads only `QueuedMessage`, which has no `delivery` field, no on-host path is reachable here no matter what changes upstream. A message that named none stores nothing (`omitempty` elides the key), so every pre-#2596 entry a client already decoded is untouched.

**The stored entry also keeps who sent it and when they tapped Send (#2704), by the same `QueuedMessage`-only structural argument.** `DeviceName` and `ClientVersion` come straight off `msg.DeviceName`/`msg.ClientVersion` — the paired device record's name and the admitted app version off that connection's hello, both captured by `internal/relay/handlers.SendMessage` at enqueue time, not re-derived here. `ClientSentAt` is `msg.ClientSentAt.Format(time.RFC3339Nano)` when non-zero, else `""` (`omitempty` elides the key) — the daemon's own re-formatting of a value the handler already parsed out of the client's optional `client_sent_at`; this producer never sees the client's raw string, the same way it never sees an on-host attachment path. Every pre-#2704 entry, and every entry from a connection with no paired-device record, stores none of the three.

**Confirmation-only recording is not gap-free across shutdown.** `msgqueue`'s drain
tests `ctx.Err() != nil` before its confirmed-delivery branch, so a delivery
that confirms in the same instant the daemon shuts down leaves the head
queued and fires neither `q.notify` nor `OnDelivered` — the message reached
claude's stdin but a producer relying only on that callback never runs for it.
Stream placement no longer waits for the callback, but it still requires the
drain to process an echo or fallback before shutdown. A client reading this
log after a restart should not assume it is gap-free across that boundary.

**Why the write point is the envelope, not `internal/turnevent`.** An
earlier draft proposed writing the log from `turnevent`'s representation.
That cannot work: `turnevent`'s variant set carries no operator message, no
conversation id, no session transition and no question batch — the operator's
own typed text goes `send_message` → msgqueue → delivery → the child's stdin,
and the content this producer stores and pushes always comes from
`QueuedMessage.Text`, never from `turnevent`. (**Since #2730, claude does echo
the delivered text back** under `--replay-user-messages`, as a digest-only
`turnevent.UserEcho` — see [streamsup-package-turn-io-envelope-write-stdout-parser.md
§ Replayed user echoes](streamsup-package-turn-io-envelope-write-stdout-parser.md#replayed-user-echoes-carry-a-digest-never-the-text-2730).
That echo carries no text at all, so it still cannot be this producer's
source; it only times a queued commit this producer was already going to
make — see below.) A log written purely from `turnevent` would hold
assistant text and tool rows and none of what the operator typed.

**Echo placement must own history, live publication and replay together
(#2730, #2820).** Committing history at an echo while handing the live push
to `operatorMessageEmitterV2.Run` on another goroutine still lets reply
frames overtake the user message in live arrival and replay event-id order.
Queue-backed stream Claude writes therefore prepare their safe commit before
writing, and `sendNowPlacement.echo` commits synchronously on the stream
drain, through `streamTurnSink.publishOperator` and the late-bound
`operatorMessageEmitterV2.broadcast` sharing the interactive emitter's ring.
One placement timestamp and payload serve history, ring and every connection.
The later `OnDelivered` callback acknowledges the managed entry; it cannot
delay, move or repeat placement, even after the answering turn has ended.

An echoed ordinary message opens its answering turn: after the preceding
`turn_end`, if any, and before its first `turn_state`, assistant or tool frame.
Without an echo, `sendNowPlacement.idle` commits a confirmed ordinary write
once before the closing event releases idle; fallback commands also run on
the drain. A late echo or callback adds nothing. Send-now retains placement
after the interrupted tool result or at the next turn's opening, and its
[carry grace window](streamsup-package-per-conversation-turn-busy-track-send-now-carry.md)
keeps the idle fallback from firing between turns. Codex/no-stream and
unregistered callers retain confirmation-at-write recording. A nil placement
commits immediately; legacy send-now callers without queue metadata still
use `expect`/`attach` and commit when both echo and commit are available.

**Match final write bytes, keep only safe queued content.**
`sendNowPlacement.write` serializes registration and the writer call per
conversation, after attachment/channel composition. It matches a private
digest of those final bytes, while the commit reads only `QueuedMessage`.
Equal payloads and repeated client `message_id` values match their own echoes
in actual write order, with distinct `queued_msg_id` values. The producing
session resolves the conversation; another conversation's echo cannot place
an entry here. Failed writes retire their registration, so a retry can record
once without recording the failed attempt. The echo text, composed bytes,
host paths and digest never become client/history payloads or log content.
See [Queue (v2)](../../protocol-mobile.md#queue-v2) for identity and timing,
and [the stream drain](streamsup-package-draining-turnevents-into-the-interactive-emitter.md)
for publication and write-outcome lock ordering.

**The store is nil-tolerant and a concrete pointer, never an interface** —
the same trap `session_transition_v2.go`'s `busy` field already documents:
a typed-nil store boxed into an interface is non-nil at the interface level
and would sail past a `== nil` guard. `Store` is not nil-receiver-safe
on either append path, so `appendConversationHistory` checks
explicitly rather than relying on a nil-receiver method, and every emitter
test that builds an emitter with no store keeps working unchanged. A
failing append never suppresses an eligible legacy wire emit or ring append.
It returns nil identity; an absent store also returns nil, omitting
`history_entry_id` rather than encoding null or zero. The failure is logged at `Warn`
with an `errors.Is`-derived discriminant (`invalid_id` / `invalid_payload`
/ `write`), never the error's own text: `history`'s errors format absolute
filesystem paths (`open segment %q`), and the log's own MUST-NOT-log-content
rule would be defeated by relaying them.

**Test-shape traps worth knowing before touching these producers
again:**

- **Rotate before successor output and reactivate the same routing ID.**
  `TestRuntimeHistoryRotationBeforeOutput` delays B across A→B→C;
  `TestRuntimeHistoryEvictionBeforeOutput` supplies late parsed tails and both
  stop lanes before releasing operator/channel writes. Output before boundary
  notification would populate the alias map and conceal missing registration.
  `TestRuntimeHistoryEvictionReactivation` and
  `TestRuntimeHistoryRunnerReactivationIncarnation` cover both eviction causes
  and both harnesses, checking fresh turn identities, terminal idle and isolation
  from old text, endings and exits. Different routing IDs alone miss permanent
  sealing of a reused ID. `TestRuntimeHistoryRoutingRegistrationRefusals` checks
  that a refused rotation preserves another producer's destination.
- **Assert retained work as well as phase and placement.**
  `TestRuntimeHistorySealedPredecessorCannotCloseSuccessor` opens successor work
  with a denial, then supplies late predecessor endings; a phase-only assertion
  misses an open turn with no running phase.
  `TestRuntimeHistoryStaleExitCannotPlaceSuccessorDelivery` reserves delivery
  before any successor output, so a declined exit must skip idle placement too.
  `TestRuntimeHistoryWriterOrdering` checks raw entry order across interactive,
  operator and channel writers and preserves another conversation.
- **Check hidden openings and history-only isolation at the real writers.**
  `TestRuntimeHistorySourceClosure` opens with thinking, closes an unfinished
  ordinary tool exactly once, excludes completed/denied calls and Agent launchers,
  and crosses the late-text chunk bound without reopening the predecessor.
  `TestRuntimeHistoryDividerMapping`, `TestRuntimeHistoryActualResetHandoff` and
  `TestRuntimeHistoryResetOutcomeAndNoops` check distinct causes, visibility and
  actual/unknown outcomes. `TestRuntimeHistoryFactsLegacyIsolation` uses healthy,
  nil and failed storage while checking pages, ring/live eligibility and
  content-free logs; successful storage alone cannot prove legacy exclusion.
- **Delay both delivery and recording to prove the capture boundary.**
  `TestOperatorDeliveryProvenance_ReceivingSessionRotation` rotates the actual
  resolved session during activation, rebinds the conversation, then rotates
  again before confirmation. A rebind only after writing would miss a cached
  resolution-time ID. `TestOperatorDeliveryProvenance_Placement` makes the
  conversation-kind lookup disagree with the receiving writer and delays
  callbacks across echo/idle placement; correct metadata alone would miss the
  wrong placement path. `TestOperatorDeliveryProvenance_WaitsAndRetry` changes
  the source during activation/idle waits and rejects the first write, so a
  successful retry must carry its own source.
- **Delay a path the captured event still reaches.**
  Captured conversation ownership bypasses the old resolver, so holding that
  resolver no longer delays switch publication. `TestRelayAgentSwitchDelayedPublication`
  holds broadcaster `ActiveConns` snapshotting and transport sealing separately
  to keep both consumer-delay and reset-exclusion witnesses. A delay on an
  unused resolver would leave the ordering test without its intended witness.
  `TestSessionTransitionHandoff_CapturesBeforeDelay` replaces lookup facts after
  enqueue; `TestSessionTransitionHandoff_RemovedExactSession` removes the actual
  pool entry and replaces the registry owner. Check raw provenance and live
  routing together: correct session metadata alone would miss misfiled history.
- **Check retained sources and conversation phases together.**
  `TestInteractiveProvenanceRetainedState` reuses main/child IDs across sources,
  interleaves another conversation and checks ordered text and provenance after
  timer, end and lifecycle flushes in warm/reopened stores.
  `TestInteractiveProvenancePhaseProjection` ends either source across both
  agent rotations and switches, continues the survivor in its same phase, and
  compares live phases, reconnect snapshots and raw lifecycle provenance.
  Correct text attribution alone would miss a false conversation idle.
- **Check actual writes, transport and unread recovery together.**
  `TestHistoryProjection_InteractiveMetadata` drives `emit` across the full
  classification, comparing raw metadata with unchanged wire/ring payloads,
  identities and recipient gates. `TestHistoryProjection_OtherProducerMetadata`
  drives clear/idle transitions, operator text and channel delivery. Both verify
  unread watermarks in warm and reopened stores; the interactive cases also
  preserve an older entry's absent visibility. A classifier-only test would
  miss a writer still using `Append`, and a correct raw metadata assertion alone
  would miss hidden eligible events disappearing from legacy delivery.
- **Recovery must not fill in missing provenance.**
  `TestChannelDelivery_SessionLegacyRecovery` loads pending records with absent,
  explicit `none` and bound snapshots alongside an untagged stored prefix. It
  rejects any recovery lookup and checks that the prefix survives unchanged.
  `TestChannelDelivery_SessionRetryRecovery` fails delta/completion writes and
  rebinds before retry or restart, checking original attribution, unchanged
  chunk identity and publication only after all writes succeed.
- **Separate the id sequences to prove provenance.** When history, ring and
  envelope counters coincide, substituting either live counter for the stored id
  stays green. `TestLiveProducers_HistoryEntryID` keeps history id 8, ring id 4
  and envelope ids 101–103 distinct, then compares the stored type, payload and
  timestamp across all three producers. Include no recipients, absent/failed
  storage and an operator without a ring: tests only with recipients miss skipped
  appends when nobody is connected, while requiring a ring misses the independent
  history-to-push handoff.
- **A shared-timestamp assertion needs three fan-out targets, not two.**
  `broadcast` used to mint `time.Now()` inside its per-conn loop; with only
  two connections, a per-conn timestamp and a correctly hoisted one are
  often indistinguishable, since the clock may not tick between two calls
  on a fast machine. Three targets compared for byte-identical payloads and
  `TS.Equal` is what makes the hoist's absence a deterministic test failure
  instead of an occasional flake.
- **A "nothing was appended" assertion is only meaningful below the drop's
  own log level.** The append failure path logs at `Warn` while
  `broadcast`'s pre-existing drops (unknown reason, unresolvable
  conversation) log at `Debug`; asserting an empty log buffer on a
  default-level handler only proves "no append was attempted" because the
  drop lines are filtered out separately. Two log statements at the same
  level would make that assertion vacuous.
