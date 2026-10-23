# `internal/history` — legacy compatibility and producer verification

Part of [history producers](history-package-producers.md#producers-2114-2115).

## Legacy eligibility and explicit visibility (#2965)

`legacyHistoryType` retains the existing source vocabulary, including content
that is no longer published. It is also used by stored-value classification;
its legacy name does not make it disposable. `historyVisibilityMetadata` and
`historyEntryShown` preserve explicit visibility for new writes and the meaning
of older untagged entries. Visibility never grants transport eligibility.
Summary/read authority comes from
[shown-thread evidence](history-package-watermarks.md#unread-state-uses-a-separate-lazily-recovered-watermark-2954).

**Content publication is retired for every app (#3732–#3737).** Operator messages,
assistant/tool/completion facts, agent start/update/roster reports, session
transitions, content notices and standalone banners remain source inputs for
canonical thread items and explicit catch-up. They produce no raw app frames,
new content replay copies or substitute receipts, including for connections
without `thread`. Runtime/startup, agent/task, queued-send and
[resolved remote prompt-answer facts](history-package-producers.md#resolved-remote-prompt-answers-2973)
also have no receipt publication. Connect-time agent roster snapshots are gone.
Nil/failed storage and no connected clients never restore retired publication.

`interactiveTurnEmitterV2.emit` makes one source append attempt before returning
for retired content types, regardless of their allowlist membership. Runtime
writers bypass `emit`: `recordRuntimeFact`, `recordAgentFact`, `closeAgentHistory`
and startup reconciliation retain their source appends and original identities.
`sessionTransitionEmitterV2.broadcast` still records dividers/transitions and
runs committed-switch completion. `recordPromptAnswer` records only a winning
authenticated remote resolution, outside bridge and registry locks.
`publishLegacyHistory`, `legacyRuntimeReceipt`, the receipt-only prompt validator
and `promptAnswerPublisher` are removed with their exclusive delivery state.

**Supported live state has a separate owner.** Open prompts, dismissals,
model/settings/catalogs, progress, retries, compaction readings and task controls
remain usable with conversation/session/generation identity. Ordered
`daemonLiveState` and `daemonThreadPublisher` delivery remain intact. The
interactive counter/ring still serve supported state through `publishLiveState`;
replay admission/pump retirement belongs to #3738, shared event-ring storage and
`SetReplaySource` removal to #3739.
[Integrated Linux acceptance](thread-package-shadow-lifecycle-and-evidence-retirement.md#integrated-retirement-acceptance-3718)
records #3718's combined proof separately from the child results. Constructor
APIs remain stable; the channel announcement
seam is inert and its completion hook only wakes after a new durable completion.

**Source visibility and publication need independent assertions.** A hidden info
banner must still be saved, even though neither it nor a shown warning reaches
raw live/replay delivery. `TestResidualContentRetired` applies canonical live
updates and compares explicit catch-up across conversations with reused turn and
answer identities; `TestResidualContentRetiredStorage` checks nil/failed storage,
no clients and content-free diagnostics. Raw `Store.Page` still exposes original
payloads, timestamps, durable IDs and metadata after reopening. Arbitrary stored
types do not become eligible transport content.

Queued recording still binds acceptance before readiness; safe delivery sources
embed that reference before followers read them, then record linked outcomes.
Echo placement, delayed confirmation and restart loss remain durable source
contracts, without queue receipts or publication workers. See
[accepted sends](history-package-producers.md#accepted-sends-and-linked-outcomes-2972).

**Do not query connections on the answer stack.** Synchronous discovery can wait
on the relay dispatch loop currently resolving the answer. Removing receipt
publication also removes its serialization mutex and worker, so later answers
and dismissals no longer depend on receipt delivery, even with cancelled context
or failed history. Keep original asking-session ownership and the 16 KiB saved
projection; see [resolved remote answers](history-package-producers.md#resolved-remote-prompt-answers-2973).

**Migrate mixed oracles as well as exclusive compatibility tests.** Default-tag
compilation misses raw-channel assertions in `e2e` files and receipt checks in
shared real-Claude scenarios. The hook-window proof now checks the retained
refusal source and rejects raw banners, while retaining the no-read settle
window, correlated ACK, later live phase, canonical queued delivery and original
source identity. Dropping receipt assertions must not drop those liveness
witnesses. Offline tagged fixture execution and compilation do not themselves
establish a live prompt/hook round trip. See
[retirement evidence](thread-package-shadow-lifecycle-and-evidence-retirement.md#residual-content-retirement-3737).

The common append seam uses `AppendWithMetadata` with explicit
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
| Runtime/startup history-only facts | `main_turn_opened` is hidden; `main_tool_interrupted` and `main_turn_interrupted` are shown; `session_divider` is hidden for `idle_sleep` and `daemon_restart`. Source facts stay off raw transports and have no receipt publication; hidden openings and restart/sleep dividers do not become unread. |
| Agent/task history-only facts | Hidden: `agent_call_observed`, `background_task_observed`, `background_task_linked`. Shown: `agent_call_result`, `agent_call_denied`, `background_task_outcome`, `background_task_gone`, `agent_ended_with_session`. Source facts stay off raw transports and have no receipt publication; canonical shown-thread evidence governs unread state. |
| [Accepted-send history-only facts](history-package-producers.md#accepted-sends-and-linked-outcomes-2972) | Shown: `send_accepted`, `send_delivered`, `send_lost`. Hidden: `send_dropped` for removal/give-up. All queued facts, including `send_written`, remain thread sources with no legacy receipt projection or live/replay publication. Hidden drops do not raise shown progress; restart loss stays visible/not delivered under the original acceptance ID. |
| [Resolved remote prompt answers](history-package-producers.md#resolved-remote-prompt-answers-2973) | `prompt_answered` is shown for the fold, with optional original asking-session provenance. Canonical answer notices and explicit catch-up consume it; no raw payload or receipt is published. Open prompts and dismissals remain live-only; shown-thread authority and foreground presentation govern read marks. |
| New history-only types | Hidden by default in the common append seam, and ineligible for legacy delivery independently of explicit visibility supplied by another producer. |

**Task-update status is an open terminal-notification contract.** Any nonempty
status counts as shown, including unfamiliar values; restricting the classifier
to known terminal words would silently hide future completion facts. Conditional
legacy payloads that fail decoding conservatively classify as shown; classification
does not rewrite or reject their payloads.

**Producer classification is not a summary watermark lookup.**
`historyEntryShown` supplies `Metadata.Shown` for new source writes.
`historyVisibilityMetadata` and raw store APIs remain internal contracts;
summary delivery and read clamping use `ThreadLastShownVersion` instead of
rescanning source payloads. Neither stored visibility nor a durable ID restores
raw content delivery or establishes foreground presentation. See
[shown-thread authority and separate test oracles](history-package-watermarks.md#unread-state-uses-a-separate-lazily-recovered-watermark-2954).

**Carry the append result, not a second lookup or another counter (#2861).**
For retained legacy live-state delivery, `appendConversationHistory` returns
the successful `Store.AppendWithMetadata` ID as an immutable `*uint64`.
`publishLiveState` carries that exact result through `Ring.AppendWithHistoryID`
before fan-out, even without recipients. Publishing first and adding metadata
later would let concurrent replay observe an incomplete record. Replay uses the
original ID/timestamp without another history append. Nil/failed storage leaves
zero ring metadata and an omitted wire key for supported state; it cannot
restore retired content. The connection counter and ring cursor are different
namespaces from the durable per-conversation ID. Channel posts have no new raw
ring/live events; their completion payload remains durable recovery input.
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
and the content this producer stores always comes from
`QueuedMessage.Text`, never from `turnevent`. (**Since #2730, claude does echo
the delivered text back** under `--replay-user-messages`, as a digest-only
`turnevent.UserEcho` — see [streamsup-package-turn-io-envelope-write-stdout-parser.md
§ Replayed user echoes](streamsup-package-turn-io-envelope-write-stdout-parser.md#replayed-user-echoes-carry-a-digest-never-the-text-2730).
That echo carries no text at all, so it still cannot be this producer's
source; it only times a queued commit this producer was already going to
make — see below.) A log written purely from `turnevent` would hold
assistant text and tool rows and none of what the operator typed.

**Echo placement owns source order (#2730, #2820, #3732, #3733).**
Queue-backed stream Claude writes prepare their safe
commit before writing, and `sendNowPlacement.echo` commits synchronously on the
stream drain. Moving source recording off that drain would let reply facts
overtake the delivery and corrupt canonical thread order. The source append
wakes thread followers; queued receipts and their worker are retired. Thread
batch arrival and replay event IDs do not determine item placement.
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

- **Wait for recording after removing a transport callback (#3732).**
  A raw-publication callback may also be a fixture's completion signal. Removing
  it can make canonical assertions race the source append even when queue
  acceptance and the command ack have finished. `TestDaemonThreadOffscreenShownRecovery`
  signals after `operatorMessageHistory` returns and waits before checking
  catch-up content. Keep that recording barrier when converting mixed tests
  away from retired transport.
- **Change stored metadata to prove acceptance facts are durable (#3693).**
  `TestQueuedAttachmentWire` renames the stored leaf after acceptance, then
  reopens history/thread state and compares fresh daemon content with the
  originally recorded names and normal restart-loss status. Leaving the file
  unchanged would let an incorrect read-time name lookup pass. Retrieval
  independently compares stored bytes and the new name. Initialize the named
  compact subscription with catch-up before checking live acceptance, and read
  that publication before same-version catch-up can suppress it; see
  [query/live ordering](thread-package-shadow-lifecycle-and-evidence-publication.md#background-discovery-and-publication).
  Reopened content uses complete catch-up because compact interval certificates
  are not retained across store reopening.
- **Keep storage broken through delivery and drop, not only acceptance (#3167).**
  `TestQueuedSendHistoryFailures` repairs storage before user delivery, so it
  cannot prove failed delivery/drop appends leave canonical state unchanged.
  `TestQueuedCanonicalStorageFailure` persists acceptance, then blocks storage
  through delivery and removal, comparing reopened state and durable allocation.
  `TestAcceptedDeliveryAppendFailures` separately covers failed acceptance,
  delivery and companion outcome appends without inventing linked identity.
- **Pair queued transport silence with positive canonical lifecycle proof.**
  `TestQueuedReceiptsRetired` first creates valid, durable facts for all five
  queued types, then checks that `legacyHistoryType` rejects each one.
  `TestQueuedSourceValidation` separately checks required source fields/reasons.
  `TestQueuedCanonicalCompletedTurn` retains assistant source/lifecycle controls,
  rejects queued publication for thread and non-thread connections, and compares
  stable acceptance-owned delivery with reopened state.
  `TestQueuedCanonicalSendNowDelayedConfirmation` checks final placement despite
  a delayed callback and repeated late-echo deduplication. Visibility alone cannot
  prove transport retirement or identity continuity; read-clamping proofs belong to
  the [read handler](relay-package-handlers-mark-conversation-read.md).
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
  `TestAgentSessionEndings_Startup` independently requires at least two nonempty
  raw pages at the production reader's 128-entry size and two nonempty regular
  on-disk segments before recovery, with the first reaching
  `history.MaxSegmentBytes`. Reopened reads require exactly two distinct endings:
  the original linked task with its call, parent and exact available
  source/lifetime, and the unattributed call reused after a session transition.
  They exclude truncated identities and invented legacy lifetimes, check
  `daemon_restart`, occurrence time and shown metadata, and require daemon latest
  to equal the newest visible ending, below the hidden restart divider's ID. Repeated
  recovery through reopened stores must preserve the complete ending entries.
  Count-only checks could accept two copies of one ending while losing the other.

  Size padding by encoded disk bytes rather than character count. JSON escaped
  the fixture's 8192 NULs per entry to roughly six times their input size;
  printable ASCII keeps 140 entries, two raw pages and real 1 MiB rollover at
  roughly 1.16 MB instead of 6.9 MB. A few large entries can roll over without
  paging; many small entries can page without rolling over. Independent scratch
  controls removing each boundary failed its own assertion. The
  [measured comparison](../../specs/architecture/3431-startup-recovery-runtime.md#revisions)
  supports reducing fixture scan cost without attributing the non-reproduced
  historical timeout to daemon recovery. See
  [runtime diagnosis](development-verification-test-execution-and-artifact-survival.md#diagnosing-startup-recovery-runtime).

  `TestAgentSessionEndings_ReportedRecovery` checks late reports and source/lifetime
  reuse. `TestAgentSessionEndings_RosterReferenceIsolation`
  saves only one ending for a two-task legacy roster, then reuses both IDs after
  a delimiter. Linked/unlinked and attributed/unattributed cases prove that each
  original task closes once across repeated reconciliation. A fully successful
  first recovery would hide observation-only suppression of the sibling task.
  `TestAgentSessionEndings_DrainOrdering` asserts ending < divider < successor
  by durable ID through the production drain, and excludes raw ending replay.
- **Separate source classification from retired transport (#3737).**
  `TestHistoryProjection_InteractiveMetadata` has independent ring and recipient
  oracles. Exclude banners from both while keeping all four stored banner
  visibility cases; changing only the ring oracle leaves a raw-delivery promise
  hidden in recipient counts. The shared classifier still serves durable facts.
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
  nil and failed storage while checking source records, ring/live eligibility and
  content-free logs; successful storage alone cannot prove legacy exclusion.
  In the failed-storage case, require a nonempty log before checking redaction:
  empty logs contain no secrets but would also hide a silently dropped append.
  Suppressing the append warning must fail this assertion independently of
  any content/path canaries (#3715).
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
  resolver no longer delays switch completion. After raw transition retirement,
  `TestRelayAgentSwitchDelayedPublication` pauses the retained committed-row
  callback to keep its delay and reset-exclusion witnesses. Holding an unused
  resolver or removed fan-out path would leave the ordering test without its
  intended witness.
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
- **Check actual writes, transport and raw visibility recovery together.**
  `TestHistoryProjection_InteractiveMetadata` drives `emit` across the full
  classification, comparing raw metadata with supported-state wire/ring payloads,
  identities and recipient gates while rejecting retired content publication. `TestHistoryProjection_OtherProducerMetadata`
  drives clear/idle transitions, operator text and channel delivery. Both verify
  internal metadata-aware watermarks in warm and reopened stores; the interactive
  cases also preserve an older entry's absent visibility. A classifier-only test would
  miss a writer still using `Append`, and a correct raw metadata assertion alone
  would miss a supported-state delivery regression or restored raw content.
- **Recovery must not fill in missing provenance.**
  `TestChannelDelivery_SessionLegacyRecovery` loads pending records with absent,
  explicit `none` and bound snapshots alongside an untagged stored prefix. It
  rejects any recovery lookup and checks that the prefix survives unchanged.
  `TestChannelDelivery_SessionRetryRecovery` fails delta/completion writes and
  rebinds before retry or restart, checking original attribution, unchanged
  chunk identity and canonical completed content after partial-write retry,
  cleanup failure and restart. A synthetic delta fan-out observer cannot prove
  this contract after retirement: inspect durable chunks and completion directly,
  retaining maximum-text splitting, turn identity and carry assertions.
- **Separate the id sequences to prove provenance.** When history, ring and
  envelope counters coincide, substituting either live counter for the stored id
  stays green. `TestLiveProducers_HistoryEntryID` keeps history id 8, ring id 4
  and envelope ids 101–103 distinct, then compares the stored type, payload and
  timestamp across the interactive and transition producers. Include no
  recipients and absent/failed storage: tests only with recipients miss skipped
  appends when nobody is connected. `TestOperatorPublicationRetired` retains
  safe source content and canonical identity checks without a live user emitter.
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
