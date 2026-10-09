# `internal/history` — legacy compatibility and producer verification

Part of [history producers](history-package-producers.md#producers-2114-2115).

## Legacy eligibility and explicit visibility (#2965)

`legacyHistoryType` is a fixed allowlist of the existing producer vocabulary
from `turnbridge.MapEvent`/`MapState`, operator messages and session transitions.
A new history-only type's raw payload is excluded from every connection's legacy
history, live fan-out and reconnect replay, whether its stored `Shown` is nil,
false or true. Only the validated runtime/startup and
[agent/task facts](history-package-producers-runtime-lifecycle.md#agenttask-attribution-and-reported-endings-3030)
have a receipt projection.
Visibility controls the raw
[unread watermark](history-package-watermarks.md#unread-state-uses-a-separate-lazily-recovered-watermark-2954),
not permission to reach a transport. Conversely, an eligible legacy event still
reaches its existing recipients and history pages when `Shown` is false,
including normal turn ends, info banners and live status readings. This preserves
[ADR 042's compatibility boundary](../decisions/042-daemon-built-thread.md#compatibility):
old apps treat unknown history entries as read-mark barriers.

`interactiveTurnEmitterV2.emit` appends once before checking this allowlist,
including when no clients are connected. Unsupported facts return before ring
publication unless `legacyRuntimeReceipt` validates their receipt projection.
Successful appends remain readable through raw `Store.Page` after reopening.
Raw storage accepts arbitrary types; the transport vocabulary is closed independently.

**Account known runtime and agent/task IDs without certifying unseen content (#3026, #3030, #3031, #3032).**
`legacyRuntimeReceipt` handles exactly `main_turn_opened`, `main_tool_interrupted`,
`main_turn_interrupted`, `session_divider`, `agent_call_observed`,
`agent_call_result`, `agent_call_denied`, `background_task_observed`,
`background_task_linked`, `background_task_outcome`, `background_task_gone` and
`agent_ended_with_session`.
It emits `banner` with all five required fields:
`{conversation_id, level:"info", text:"", truncated:false,
stops_turn:false}`. It requires a nonzero successful durable ID, nonzero entry
timestamp, matching conversation and nonzero `occurred_at`. Openings and turn
interruptions need `turn_id`; tool interruptions also need `tool_call_id`.
Dividers need `cause` and a predecessor session, except `daemon_restart` dividers.
`validAgentHistoryFact` requires matching conversation and nonzero `occurred_at`;
any supplied `lifetime_id` must be valid. Observations, results, denials, task
links, outcomes and gone require that lifetime. Observed calls need a call ID and an
`Agent`/`Task` tool name; results need a call ID and `completed`/`failed`; denials
need a call ID and `denied`. Task observations need a task ID, links need both
task and call IDs, and outcomes need a task ID and any nonempty status. Gone
requires both task and call IDs and exactly `status: gone`, retaining its
[unknown-outcome meaning](history-package-producers-runtime-lifecycle.md#gone-requires-a-later-complete-roster-3031).
Ended-with-session facts need a nonempty cause and at least one call/task
identity, plus either a valid lifetime or an explicit original observation
reference. `call_observed_entry_id` requires `tool_call_id`, and
`task_observed_entry_id` requires `task_id`; a reference cannot stand in for its
missing identity. This admits legacy recovery with unavailable lifetime/source
attribution without fabricating it. Receipt validation checks this shape;
[recovery matching](history-package-producers-runtime-lifecycle.md#recover-agenttask-work-and-original-identities-3032)
separately uses original observations and identities to suppress repeated endings.
Malformed facts and arbitrary unknown types stay holes, never harmless receipts.
The unchanged mobile decoder counts valid info receipts toward legacy latest
without drawing content or changing turn, permission, model or status state.
Receipt-only delivery gives no sight; malformed/unidentified receipts and
unaccounted holes remain barriers. Receipt accounting alone never advances a
read mark or grants foreground presentation.

**Runtime writers bypass `emit`.** Changing only its allowlist would leave live
and reconnect accounting incomplete. `recordRuntimeFact`, `recordAgentFact`,
`closeAgentHistory` and `sessionTransitionEmitterV2.broadcast` append each raw
fact once, then use `publishLegacyHistory` for the receipt. Runtime dividers use the replay ring
installed on the runtime sink before workers start. History, direct recipients
and replay share the original ID and timestamp, with the same receipt bytes;
there is no second durable append or ID allocator. Existing interactive and
agent/isolation gates still apply. Nil/failed storage produces no receipt, while
eligible legacy delivery and replay continue without durable identity. Ordinary
`session_transition` frames retain their separate, non-ring behavior.
Agent/task facts also persist in history-only operation. Lifetime mint/marshal
failures log fixed discriminants; append failures retain the common content-free
classifier, without supplied call/task IDs, parent, status, prose or storage paths.
`closeStartupAgentWork` appends before the restart divider without publishing
live traffic or backfilling replay; its valid endings become receipts when paged.

Raw visibility and legacy unread targets differ (#3029): the
[legacy watermark](history-package-watermarks.md#unread-state-uses-a-separate-lazily-recovered-watermark-2954)
counts validated receipts regardless of `Shown`. Raw facts, metadata and store
watermarks remain unchanged. The
[pager](history-package.md#reader-2116) retains one bounded raw page's cursor and
`AtStart`; accounting never authorizes scanning ahead to fill a page.

`TestLegacyRuntimeReceipts_CompletedTurn` uses the real store and `HandleFor`
normal text/completion with runtime facts disabled as a control; omitting the
enabled opening ID leaves a checkpoint hole despite presenting the reply.
`TestLegacyRuntimeReceipts_LiveReplay` checks shared identity and overlap.
An exact live banner count includes these receipts: retain every banner and
validate expected receipts separately from refusals rather than ignoring info
banners, which would hide duplicate receipts. `TestInteractiveStreamHookBlockedBannerReachesTheClient`
keeps the full refusal/liveness window and checks the opening receipt before text.

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
| Runtime/startup history-only facts | `main_turn_opened` is hidden; `main_tool_interrupted` and `main_turn_interrupted` are shown; `session_divider` is hidden for `idle_sleep` and `daemon_restart`. Raw payloads stay off legacy transports; validated nonvisual receipts count toward legacy latest. |
| Agent/task history-only facts | Hidden: `agent_call_observed`, `background_task_observed`, `background_task_linked`. Shown: `agent_call_result`, `agent_call_denied`, `background_task_outcome`, `background_task_gone`, `agent_ended_with_session`. Raw payloads stay off legacy transports; validated receipts count toward legacy latest for either visibility value and confer no foreground presentation. |
| [Accepted-send history-only facts](history-package-producers.md#accepted-sends-and-linked-outcomes-2972) | Shown: `send_accepted`, `send_delivered`, `send_lost`. Hidden: `send_dropped` for removal/give-up. All four stay excluded from legacy pages, live traffic, replay and receipts. |
| New history-only types | Hidden by default in the common append seam, and ineligible for legacy delivery independently of explicit visibility supplied by another producer. |

**Task-update status is an open terminal-notification contract.** Any nonempty
status counts as shown, including unfamiliar values; restricting the classifier
to known terminal words would silently hide future completion facts. Conditional
legacy payloads that fail decoding conservatively classify as shown; classification
does not rewrite or reject their payloads.

Previously stored entries retain raw visibility semantics. Legacy targets count
eligible types regardless of `Shown`, except `turn_state`, `stall`, `api_retry`,
`compacting` and `session_transition`, which never count. Unsupported entries
keep absent/true counting and false exclusion without certifying receipts.
Raw pages retain hidden entries and their IDs; no metadata is rewritten.

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
posts retain their missing-id behavior, and ordinary `session_transition` frames
remain outside the ring; runtime-divider receipts use it.
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

- **Restart local numbering and report from idle to prove durable attribution.**
  `TestAgentHistory_Isolation` reuses call/task IDs across conversations, sources
  and child incarnations, then replaces the emitter and reopens the real store.
  Testing only a warm emitter would miss joins that collide after restart.
  `TestAgentHistory_ReportedOrdering` permutes result/link/outcome arrival and
  follows main-turn completion with background reports; running-only checks
  would miss lost attribution or reopened main work. `TestAgentHistory_SealedReports`
  adds first starts after sealing with and without preceding main work, so an
  originating-turn-address predicate cannot silently drop results/denials.
- **Exercise joins through the hold before testing roster absence.**
  `TestAgentHistory_GoneRosters` sends incomplete originating starts through
  `sessionBackgroundTaskHold.Sink`, then complete roster rows and a later empty
  refresh. Testing only already-enriched rows would miss a lost task truncation
  marker that turns an invalid join into inference eligibility. It also checks
  refresh-before-link/call ordering, task IDs distinct from call IDs, launch
  results and repeated/late reports in warm/reopened stores.
  `TestAgentHistory_GoneHousekeeping` prunes joins with an incomplete roster,
  reads retained state and resets the hold before a fresh complete refresh:
  housekeeping must neither infer gone nor erase the emitter's retained links.
- **Respawn with the same source/incarnation to test lifetime retirement.**
  `TestAgentHistory_GoneChildLifetimeReset` covers unsealed child exit and
  conversation close, reusing IDs after gone, reported and denied endings and
  checking old links with an unrelated denial. New source IDs alone would miss
  stale joins or terminal markers surviving a retired lifetime.
  `TestAgentHistory_GoneSealedLifetime` checks the complementary boundary:
  sealed predecessors retain call and terminal evidence, so late rosters cannot
  infer gone after a session ending while successor work stays isolated. Both
  inspect warm/reopened real stores.
- **Close background work after main-turn completion and partial recovery.**
  `TestAgentSessionEndings_Runtime` checks closure with result-before-link and
  link-before-result, terminal evidence, unlinked identities and repeated/late
  reports after the main turn ends. `TestAgentSessionEndings_Isolation` checks
  stale exits, replacement incarnations and Codex exclusion.
  `TestAgentSessionEndings_Startup` forces raw pagination and segment rollover,
  legacy ID reuse, missing attribution and truncated identities through reopened
  stores; `TestAgentSessionEndings_ReportedRecovery` checks late reports and
  source/lifetime reuse. `TestAgentSessionEndings_RosterReferenceIsolation`
  saves only one ending for a two-task legacy roster, then reuses both IDs after
  a delimiter. Linked/unlinked and attributed/unattributed cases prove that each
  original task closes once across repeated reconciliation. A fully successful
  first recovery would hide observation-only suppression of the sibling task.
  `TestAgentSessionEndings_DrainOrdering` asserts ending < divider < successor
  by durable ID through the production drain, and excludes raw ending replay.
  `TestAgentSessionEndings_ReceiptValidation` requires each recovery reference's
  corresponding identity and rejects receipts without a successful durable ID.
- **Validate every receipt shape independently of raw visibility.**
  `TestAgentHistory_ReceiptValidation` removes each fact's required fields and
  checks warm/reopened legacy readers with both `Shown` values.
  `TestAgentHistory_LegacyCompatibility` uses the independent checkpoint/latest
  fixtures, comparing page/live/replay identity and bytes as well as malformed
  barriers and receipt-only sight. Raw visibility checks alone would miss
  broken unread accounting or accidental foreground presentation.
  `TestAgentHistory_GoneReceiptBarriers` rejects malformed gone facts, incorrect
  gone statuses, unrelated unknown types and absent storage identities; a valid
  receipt bridges its own durable checkpoint hole only.
- **Reuse legacy IDs after recovery, not just across repeated starts.**
  `TestStartupHistoryReferenceIsolation` saves full and tool-only recovery before
  newer scoped work reuses the turn/tool IDs, then checks that the newer work
  still closes. Repeated starts without new work miss references that also
  terminate their append-time scope. Its late-fact case checks the opposite:
  output in the original scope must not reopen an interrupted turn.
- **Prove the intended storage failure happened.**
  `TestStartupHistoryFailures` asserts the write failure's append event or the
  read failure's canonical conversation ID as well as content-free logs and
  continued reconciliation of a healthy conversation. An invalid registry ID
  can produce a safe warning before touching storage; absence of sensitive
  content alone would leave the intended read/write failure untested.
  To prove complete startup reads precede loss inference, keep a valid newer
  appendable segment with an unresolved acceptance and more than one raw page,
  then corrupt an older segment reached on a later page. Assert no loss or
  divider anywhere. `TestQueuedSendHistoryFailures` corrupts the active segment,
  which also prevents appends: unchanged bytes alone cannot distinguish the
  complete-read guard from storage refusing an incorrectly attempted closure.
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
