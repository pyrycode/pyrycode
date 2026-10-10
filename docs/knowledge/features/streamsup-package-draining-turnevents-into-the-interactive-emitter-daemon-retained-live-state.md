# Daemon-retained live state

`daemonLiveState` in `cmd/pyry` owns current stream and control readings in memory,
separately from [ADR 042's folded items](../decisions/042-daemon-built-thread.md#decision)
and legacy delivery. `startRelayV2` and the no-relay `startRelay` history path each
install one owner before workers start. It retains `turn_state`, `stall`,
`api_retry`, `compacting`, `thinking_progress`, `tool_progress`,
`background_task_progress`, `rate_limited`, `context_usage`, `model_announced`,
`session_facts`, `mcp_status`, `slash_command_list` and `model_list`, including
explicit inactive retry and compaction values. Task lifecycle/rosters and queue
content remain thread items. Retention and cursor operations add no history
write, folded item, catch-up change or last-shown version.

**Capture and admission precede queueing.** `sinkForProducer` captures the source
conversation, producer provenance, activation incarnation and positive generation
before fan-in. `streamTurnEnvelope.live` carries that capture through the drain;
`convTurnState.liveSource` keeps it with buffered source state. Detached envelope
`session_id` is a nonempty producer string, JSON `null` for positively known
no-session state, or omitted for unresolved provenance. Payload session fields
stay unchanged. Delayed predecessor output keeps its original attribution even
when a successor reuses the routing ID.

Capturing the generation after releasing the routing lock would let a transition
turn an old reading into successor state. Capture, activation, transitions
(including same-ID notifications), exit retirement and removal serialize on
`streamTurnSink.offerMu`, then the owner mutex. Conversation resolution runs
outside the owner mutex; fan-in, history and delivery run after releasing
`offerMu`. Inventory ingress maps and admits its bounded reading under that
boundary before forwarding. The owner has no broadcaster or history dependency
and adds no goroutine. Retaining in the legacy emitter would leave readings
waiting behind connection enumeration: source admission progresses even while
legacy delivery is held or the bounded fan-in refuses an event.

**Ordering belongs to the conversation and reading.** A detached
`daemonLiveReading` contains `Envelope`, `ConversationID`, `SessionGeneration`,
`ReadingID`, `Revision` and optional detached `Answerable` metadata. Singleton
identity is empty; tool/task progress uses its producing tool-call/task ID.
Positive revisions increase at source admission
per conversation/generation/family/identity, before delivery can be delayed.
Retired generations and overtaken revisions cannot replace current readings.
Conversations remain independent; session strings are not ordering counters.
Same-phase successor activity reaches retention while legacy phase sends remain
deduplicated.

Reset/clear, agent switch and recovery/reactivation advance the conversation's
generation, including same-ID activation and first output after a confirmed
stop within the same activation. Each transition retires predecessor readings
and admits a clear for every scoped family before fresh readings: its existing
envelope type, `session_state_cleared: true`, payload `{}`, successor provenance
and positive new-generation ordering. Unknown successor provenance stays omitted;
known no-session provenance is `null`. Captured boundaries keep detached clear
cursors, and source updates carry remaining family clears before fresh readings.
Returning A → Z → A needs a fresh successor binding even within one activation;
generation advancement releases cached bindings without altering queued captures.

**Scope and retirement follow producing work.** Admission mints main-turn and
child-lane identities that the legacy emitter adopts. Unassociated progress and
denial can open a main identity without changing their lifecycle-neutral busy
classification or emitting a phase. Main completion retires thinking and main
tool progress; child/background progress may outlive it. Completed/failed tools,
denials and task notifications with nonempty status retire the matching progress
identity; a task patch does not. Late progress stays suppressed until a new start.
Subsequent turn activity clears stalls; producer stop retires all progress.

Stops release producer/turn lifecycle maps; generation changes discard predecessor
revision maps. Conversation removal cleans the owner in both compositions. Keeping
retired maps for stale suppression would accumulate memory across activations:
generation/incarnation checks and detached records permit their release instead.
A generation high-water counter seeds recreated conversations without accumulating
deletion tombstones, and old cursors survive cleanup.

**Snapshots are finite and detached.** `snapshot` pins immutable record references,
without eagerly copying payload batches; `daemonLiveCursor.Next` returns at most
one deep-detached reading and never follows later updates. Its captured generation,
revision and readings remain available for consumer suppression. Cursor creation
and consumption invoke neither broadcasters nor history scans. Supplied and returned
payload/session bytes and correlation pointers cannot mutate retention or another
cursor.

Admission validates the complete envelope against
`protocol.MaxThreadEnvelopeBytes` (65519 bytes), including metadata and its clear
form; invalid or oversized input leaves retention unchanged. MCP's producer limits
(16 servers and 256 UTF-8 bytes of Error) alone do not bound the other strings or
JSON escaping. `turnbridge.mapMCPStatus` now retains the longest whole-row prefix
within a 63,000-byte encoded-array budget, stopping at the first rejected row.
Retained fields stay verbatim; mapping omissions add to the producer count with
`math.MaxInt` saturation. See [the mapping contract and envelope reserve](turnbridge-package-outbound-adapter-map-event.md).
`TestMCPStatusDecoderAndMapperOmissions` covers both cuts together: 19 hostile
source rows become 16 producer rows, then eight mapped rows with 11 omissions.

`TestDaemonLiveMappedInventoryBounds` requires the hostile sixteen-row MCP
reading and its `{}` family clear to be accepted and fit the complete-envelope
limit, including escaped source metadata and maximum identity counters; MCP is
no longer skipped. `TestDaemonLiveMetadataAndBound` separately checks rejection
of arbitrary oversized envelopes. An owner-only fixture would miss source
capture and shared-identity failures: `TestDaemonLiveCaptureTransitionOrdering`,
`TestDaemonLiveReturningRoutingID`, `TestDaemonLiveNeutralMainTurnIdentity` and
`TestDaemonLiveLegacyDelayAndIdentity` exercise those producer paths.

## Control and on-demand readings

Control producers retain before recipient enumeration or push, even with no
recipients or failed delivery. `streamApprovalBridge.Surface` captures the
prompt's conversation, producing session and generation in `promptHistoryOwner`;
reset dispatch and notification callbacks capture before asynchronous handoff.
A conversation-only callback has unresolved producer provenance, even if the
conversation is currently bound. The detached envelope preserves payload session
fields and the same omitted/null/nonempty-string provenance distinction as streams.

| Envelope variants | Logical family and identity |
|---|---|
| `modal_shown`, `modal_dismissed` | Permission family, original `modal_id`. |
| `question_shown`, `question_dismissed` | Question family, original `question_batch_id`. |
| `resetting`, `session_error`, `reply_suggestion` | One current singleton per family and conversation. |
| Requested/stream `context_usage`, `mcp_status`, `slash_command_list`, `model_list` | One shared singleton per family and conversation. |
| `session_settings`, `session_settings_updated` | One settings singleton; update retention follows successful writes only. |

Prompt snapshots hold only outstanding prompts with their existing answer IDs.
`Answerable` is separate from the unchanged payload: permission eligibility is
`!RequiresUserInteraction`, matching `RemoteAnswerable`; interaction-required
permissions remain fail-closed. Question batches keep their existing remotely
answerable contract. Answer, timeout or dismissal removes only that identity,
preserving concurrent prompts and existing answered-prompt history.

Optional daemon-local results such as `GetLive`, `runSettingsReading` and
`liveSettingsUpdater.UpdateLive` expose detached source-bearing envelopes while
preserving legacy provider signatures, refusal/fallback behavior and model
capability filtering. Supplied correlation fields belong to the reply, never to
retained identity; equal-state requests still receive their own correlated result.
`daemonLiveOperation` reserves the family revision before queries or reset tails
start. An overtaken completion can still answer the legacy request but cannot
replace a newer stream/update reading. Reset phases advance their reservation
only while they still own that family; bound emitters share the parent counter
and broadcaster, since copying the counter would restart legacy envelope IDs.

`contextUsageResolver.prepare` reserves before idle deferral or querying, and
collapsed requests reuse the original flight's reservation, including fallback.
Flight sharing compares captured generation and provenance as well as conversation;
a conversation-only key would let a successor join its predecessor, including
with a reused session ID. Caller cancellation and the existing collapse window
remain intact. Reserving again after a failed query would let old remembered
bytes overtake newer stream admission still waiting on its recorder.

Cached payload and source evidence are one snapshot: `contextUsageRecorder.snapshot`,
`sessionModelHold.ModelListLive` and `sessionSlashCommandHold.SlashCommandListLive`
return matching bytes/source pairs. Byte equality with current retained state
cannot establish cache ownership after clears or capability projection. Model
projection carries the unprojected inventory's source. Producer evidence survives
transition clears with the cache and is removed with the conversation; previously
stored readings without evidence remain unresolved, never assigned the current
producer. Returning cached evidence cannot change the operation's reserved
generation or promote predecessor bytes into successor retention.

`daemonInventoryIngress.prepare` captures source, incarnation and generation
once, atomically stores the hold's payload/source pair and admits the inventory
under `offerMu`, before decorators can delay forwarding. The immutable envelope
carries that admission through fan-in; a second downstream capture would retag
old bytes or give them a newer revision. Factory ingress attachments exist even
before owner installation and remain inert until it is available: bootstrap
runner construction precedes installation. `TestControlLiveInventoryIngress`
and `TestControlLiveInventoryIngressOvertaking` exercise the real parser/hold/
decorator/fan-in chain; cache tests with nil downstream sinks miss recapture.
`TestControlLiveInventoryLateOwner` also checks bootstrap capture and single
forwarding. Persistence and delivery remain outside the boundary.

Reset/clear, agent switch and recovery retire these families through the same
transition boundary and install their new-generation `{}` clears with
`session_state_cleared: true` before fresh readings. Old prompt dismissals, reset
completions, queued errors, fallback suggestions and query/cache results cannot
replace successor readings, even with reused session IDs. Same-generation
revisions reject overtaken work independently in each conversation. Leaf producer
locks may acquire the owner mutex but never `offerMu`; no lock waits on a query,
worker or relay push. Both compositions attach retention to their existing
producers without starting absent prompt/query paths.

The actual supported question boundary remains a producer limitation:
`TestControlLiveQuestionEnvelopeBound` is skipped pending
[#3109](https://github.com/pyrycode/pyrycode/issues/3109). Valid input near the
16384-byte raw cap can expand to a 98081-byte JSON payload; admission rejects it.
A raw input cap does not prove that an encoded envelope fits, especially with
worst-case escaping and source/correlation metadata. See
[protocol boundary testing](development-verification.md#protocol-boundaries).
MCP's enabled `TestDaemonLiveMappedInventoryBounds` assertion is separate evidence.

Relay provider installation, production activation and the final producer-to-relay
proof remain pending [#3077](https://github.com/pyrycode/pyrycode/issues/3077),
including the adapter, `PushLiveState`, `ThreadLiveState` installation and correlated
wire replies. These daemon-local attachments do not enable production thread
negotiation or change relay handlers.
