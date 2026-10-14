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
boundary before forwarding. The owner has no broadcaster or history dependency;
the relay composition installs one bounded publisher described below. Retaining
in the legacy emitter would leave readings waiting behind connection enumeration:
source admission progresses even while legacy delivery is held or the bounded
fan-in refuses an event.

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
deletion tombstones. Old cursors keep detached records after cleanup, but relay
delivery checks current ownership before sending unsolicited state.

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

Relay prompt snapshots hold only outstanding eligible prompts with their
existing answer IDs; missing or false captured eligibility withholds shows.
`Answerable` is separate from the unchanged payload: permission eligibility is
`!RequiresUserInteraction`, matching `RemoteAnswerable`; interaction-required
permissions remain fail-closed. Question batches keep their existing remotely
answerable contract. Answer, timeout or dismissal removes only that identity,
preserving concurrent prompts and existing answered-prompt history.
`daemonLiveBindings.dismiss` receives the original dismissal payload at the
producer boundary: permission `modal_id`, `outcome` and `source`, or the question
batch's original ID. The winning terminal path publishes that source-bearing
dismissal, which is never retained in snapshots.

Capture prompt ownership and correlation before the registry one-shot, then
release `streamApprovalBridge.mu` before consuming. Only the registry winner
uses that detached capture for actuation, history and dismissal. Capturing after
consumption loses the source if concurrent retirement removes the bridge maps;
holding the bridge lock across consumption blocks eligibility and source writers
behind registry latency. `TestDaemonLiveRelayPermissionRetirementGap` covers
allow, reject and cancel, and
`TestStreamApprovalBridge_InteractionRequiredRefusalSurvivesConcurrentRetire`
checks that eligibility remains independent of a held consume. Captured
`Answerable` controls delivery, while existing device and resolver gates still
authorize answers.

Optional daemon-local results such as `GetLive`, `runSettingsReading` and
`liveSettingsUpdater.UpdateLive` expose detached source-bearing envelopes while
preserving legacy provider signatures, refusal/fallback behavior and model
capability filtering. `installDaemonLiveRelay` installs
`ContextUsageReadingFor`, `MCPStatusReadingFor`, `ModelListReadingFor`,
`SessionSettingsReadingFor` and `UpdateSettingsReading` from those results.
Shared implementations preserve a single query or mutation and the settings
read's single binding/usage lookup; relay settings enrichment keeps captured
evidence. A selected live provider never retries through the legacy provider.
Supplied correlation fields belong to the reply, never to retained identity;
equal-state requests still receive their own correlated result.
`daemonLiveOperation` reserves the family revision before queries or reset tails
start. An overtaken completion can still answer the legacy request but cannot
replace a newer stream/update reading. `daemonLiveOperation.complete`'s boolean
reports retention acceptance, not provider availability: a valid equal or stale
result remains a reply candidate under relay ordering. Treating that boolean as
unavailability would discard a valid correlated answer. Reset phases advance
their reservation only while they still own that family; bound emitters share
the parent counter and broadcaster, since copying the counter would restart
legacy envelope IDs.

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
retention holds the unprojected Claude inventory and its source. The adapter's
`ModelListUnprojected` metadata makes relay projection apply separately to
snapshots, pushes and replies for each recipient's negotiated capability.
Retaining the requester's merged menu would leak agent rows to basic clients;
requiring a legacy `EventID` would omit Codex options from detached snapshots.
Basic clients keep the existing Codex-only fallback refusal, and model clears
stay `{}`. Producer evidence survives transition clears with the cache and is
removed with the conversation; previously stored readings without evidence
remain unresolved, never assigned the current producer. Returning cached evidence
cannot change the operation's reserved
generation or promote predecessor bytes into successor retention.

Borrowed bootstrap vocabulary is a distinct reply-only case.
`inventoryResultSource` preserves the foreign cache's original evidence;
`liveInventoryResult` combines its producer session tag with the target request's
captured positive generation/revision and sets `daemonLiveOperation.replyOnly`.
Generation zero would silently fail `PushLiveState` admission, while copying a
foreign generation could accidentally match the target and grant retention
authority. Explicit reply-only completion prevents retention/publication even
when counters coincide. Target clears still suppress delayed replies; cached
predecessor evidence from the same conversation keeps its producing generation.
`TestDaemonLiveRelayBorrowedBootstrapVocabulary` exercises actual parser/hold
ingress, both recipient capabilities, unchanged legacy payloads and reused-ID
clears on authenticated decrypted wire.

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

`TestControlLiveQuestionEnvelopeBound` is enabled after
[#3109](https://github.com/pyrycode/pyrycode/issues/3109). The producer now applies
both raw-input and encoded-payload bounds, reserving envelope metadata space;
see [question producer bounds](questionbridge-package.md#bounds). The regression
finds the supported parser boundary, checks owner admission and complete reading/
clear envelopes with hostile escaping and maximum metadata, and resolves answer
and refusal through the original batch ID. A raw cap alone would admit input
whose encoded envelope cannot fit. See
[protocol boundary testing](development-verification.md#protocol-boundaries).
MCP's enabled `TestDaemonLiveMappedInventoryBounds` assertion is separate evidence.

## Relay delivery

`startRelayV2` installs `ThreadLiveState` through `daemonRelaySnapshot`, a finite
cursor across owner conversations returning one detached eligible reading per
call. `daemonRelayReading` preserves conversation, positive generation/revision,
reading identity, captured eligibility, payload session fields and the original
omitted/null/string envelope session tag. Snapshot and push delivery include
explicit inactive retry/compaction values; slash-command inventories use the same
retained snapshot/publication path. Correlated replies use the five installed
providers above. Source evidence is never reconstructed from legacy pushes.

Accepted stream/control admissions, clears and prompt dismissals hand immutable
records to `startDaemonLivePublisher` independently of legacy enumeration, fan-in
acceptance or delivery success. Its one cancellation-owned worker and 256-reading
handoff keep admission nonblocking. Selection of each reading precedes recipient
enumeration; a connection admitted later snapshots it or receives a subsequent
replacement. Reusing one recipient list across an unbounded drain leaves a new
connection with neither its overtaken snapshot nor the replacement push.

`TestDaemonLiveRelayPublisherConnectGap` holds publisher delivery across that
admission and observes two signals from its `ThreadLiveState` wrapper, each with
a three-second deadline. It waits for `phone` to capture an empty snapshot before
admitting `old`; `again` then captures `old` and synchronously admits `fresh`
before signaling that held delivery may resume. Both peers must receive a decoded
`ModelAnnouncedPayload` exactly equal to model `fresh` for conversation `conv`,
with envelope session `a`. Stale or wrong-source/conversation readings fail
immediately; a missing replacement fails the bounded wire read. The publisher
hold escapes on release or cancellation, and deferred idempotent release precedes
fixture cleanup joining the publisher. Awaiting only the Noise response would
allow the first snapshot to capture valid `old` and falsely fail this proof; see
[snapshot synchronization](development-verification.md#prove-that-tests-distinguish-the-change).

Recipient lookup and `PushLiveState` run outside source/owner locks, so held
publication cannot block stream/history writers. Handoff overflow retires thread
consumers with relay's existing overflow teardown, forcing fresh handshake
reconciliation. Shutdown cancels and joins the worker before releasing the
installation slot, then discards buffered records; freeing the slot first would
allow duplicate publishers. `TestDaemonLivePublicationRetirement` and
`TestDaemonLivePublicationShutdownInstallation` cover these boundaries.

`daemonLiveCurrent`, installed as `LiveStateCurrent`, checks unsolicited snapshots
and queued pushes again on relay Run. Removal/recreation or an overtaking update
can invalidate a detached cursor after capture. Ordinary readings compare the
retained revision, not an unfinished query reservation: a failed/refused query
must leave prior accepted state available. Dismissals compare terminal ordering;
clears compare current generation. Correlated replies keep captured source and
use relay watermarks rather than this retention check. See
[relay ordering and access gates](v2-session-manager-state-machine-capability-negotiation-on-the-handshake.md#supplied-live-state-ordering).

Hermetic `TestDaemonLiveRelayPublication`, `TestDaemonLiveRelayPrompts`,
`TestDaemonLiveRelayReplies` and the ordering/retirement regressions exercise
actual producers through authenticated decrypted wire, with explicit test-only
readiness and legacy-byte comparisons. Production `startRelayV2` now installs
`installDaemonThreadRelay` and starts `startDaemonThreadPublisher`, activating
`ThreadReady` only with the real fold/watermark and retained live-state providers.
`TestDaemonThreadPublication` exercises that installation with simultaneous
authenticated thread/legacy peers, complete item reconstruction and current
readings. Fold publication remains coordinator-owned and independent of source
admission and history writers; held peer delivery cannot block either producer.
Both publishers cancel and join before releasing their installation slots.
See [fold publication and gap recovery](thread-package.md#shadow-lifecycle-and-evidence)
and [relay quarantine](v2-session-manager-state-machine-capability-negotiation-on-the-handshake.md#thread-delivery-and-summary-projection).
Production live thread publication and supplied-state reconciliation are active;
connect/reconnect snapshots still initialize only transient readings. Client
thread history initialization, catch-up/pages and replay replacement remain
[#2963](https://github.com/pyrycode/pyrycode/issues/2963).
