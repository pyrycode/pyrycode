# Turn I/O — envelope write + stdout parser (#1088)

`buildArgs` requests `--include-partial-messages`, `--forward-subagent-text`
and (#2730) `--replay-user-messages` in the fixed prefix for create
(`--session-id`) and resume (`--resume`) spawns. Production therefore
receives the nested `stream_event` lines, attributed subagent prose, and a
replayed `user` echo of every message claude reads, mapped below;
caller-supplied arguments do not need to opt into any of the three.
`buildArgs` also requests `--prompt-suggestions` on every persistent spawn
unless the child's last effective `CLAUDE_CODE_ENABLE_PROMPT_SUGGESTION` entry
is `false` (#2831). Claude honours its own `promptSuggestionEnabled: false`
setting. This prefix belongs only to the long-lived interactive child; the
separate `pyry agent-run` argv (`internal/agentrun/streamrunner`) is unchanged.
The turn I/O boundary fills
`Stdin()`/`Config.Stdout` with two additive seams:

```go
var ErrNoLiveChild = errors.New("streamsup: no live child")

func WriteTurn(ctx context.Context, w io.Writer, prompt []byte) error

type Parser struct { /* sink, byte buffer, maxBuf, logger */ }
func NewParser(sink func(turnevent.Event), logger *slog.Logger) *Parser
func (p *Parser) Write(b []byte) (int, error) // io.Writer; set as Config.Stdout
```

`Runner.ConfirmedPermissionMode` is a concurrency-safe, informational read of
the last permission mode Claude confirmed for the exact child running now. A
non-empty `system/init.permissionMode` supplies one confirmation source and is
retained through the same 256-byte `truncateField` bound used by the unchanged
`SessionFacts` event. The other source is a successful `SetPermissionMode`
write followed by the first response carrying that exact daemon-minted request
id, a `success` subtype, and an echoed mode equal to the request. Failed writes,
unknown ids, malformed or non-success replies, and missing or unequal echoes
leave the prior value unchanged; an exact-id reply is retired before its payload
is accepted, so a later duplicate cannot repair a rejected first reply. These
paths log no mode, request id, response content, or decode error.

Availability is child-scoped, not runner-scoped. Beginning a child clears the
predecessor's value and pending correlations before stdout can arrive; child
exit clears both again before backoff or successor setup. A runner without its
concrete `Parser`, a child before its first confirmation, and a runner between
children therefore all report `("", false)`. `SetPermissionMode` snapshots the
live writer and runner child generation before registering, then rechecks the
binding before writing. Registering first and looking up `Stdin` later would let
a call begun between children drift onto a successor after its correlation had
already been retired. An early reply also waits for the write verdict, so a
write that ultimately fails cannot confirm a mode; the parser generation check
keeps a predecessor reply from populating its successor.

Keep three permission concepts separate: stored settings and
`SetSpawnPermissionMode` express launch intent for a later child;
[`PostureGate`](streamsup-package-posture-gate-spawn-permission-mode-ack.md)
answers whether a turn may enter after the spawn-time acknowledgement and can
remain open across an in-band change; `ConfirmedPermissionMode` is only Claude's
latest bounded claim about the current child's running posture. The read never
falls back to settings or argv, never sends a control request or changes the
gate, and publishes no client frame.

`Runner.RequestContextUsage` and the parser form one solicited-response boundary.
When the runner's `Config.Stdout` is that concrete parser, the runner registers
its locally minted request ID before writing `get_context_usage`, removes the
registration if the write fails, and publishes the write result to any response
that arrived before the call returned. The parser accepts only an exact pending
ID and retires it before decoding subtype or payload, so an unknown ID, duplicate,
error reply, or malformed first reply cannot emit a later `ContextUsage` under
the same ID. A runner configured with another stdout writer can still send the
request but cannot establish the provenance needed to emit the event.

The daemon's `turnEndContextUsageRequester` wraps each session parser sink and
automatically calls that same runner's `RequestContextUsage("summary")` once after
forwarding every `turnevent.TurnEnd`; opener and non-terminal variants only pass
through. This policy must stay upstream of `streamTurnSink`: the shared drain
drops events from inactive sessions and may refuse events under pressure, so a
drain-side trigger would silently lose those sessions' readings. The turn-busy
tracker remains downstream lifecycle evidence, not the request owner. A teardown
write failure is absorbed as one content-free Debug record and cannot enter the
runner's restart or backoff path.

A successful reply emits Claude's `model`, `totalTokens`, `maxTokens`, and
`percentage` unchanged. `boundContextUsageEntries` ranks categories by descending
token count, retains at most 32 names of at most 256 bytes, and reports every
string rejection and count-cap omission in `DroppedCategories`. The same helper
independently ranks and caps the MCP-tool and memory-file inventories: each retains
at most 32 entries, rejects an entry if any of its strings exceeds 256 bytes, and
reports rejection plus count-cap omission in `DroppedMCPTools` or
`DroppedMemoryFiles`. MCP entries carry `Name`, `ServerName`, and `Tokens`; memory
entries carry `Path`, `Type`, and `Tokens`. An absent or empty inventory emits an
empty slice with zero dropped entries, and individual system-prompt sections are
not decoded or required. An overlong model drops the whole event. This reading is
informational and does not replace `contextwindow.Read`. Response-side decode
failures are deliberately silent: `emitModelList` remains the sole owner of the
existing content-free `logControlResponse` record, so raw response bytes, decoder
errors, tool and server names, and memory paths and types never enter daemon logs.

`decodeMCPStatus` recognises another informational control-response shape directly
from the complete top-level line, without request correlation. It requires
`response.subtype:"success"` and a present array at
`response.response.mcpServers`; a present empty array emits one `MCPStatus` with a
non-nil empty `Servers` slice, while a missing, null, non-array, or undecodable value
emits nothing. Because `consumeLine` dispatches only on the outer line type, a
control-shaped string nested in assistant content cannot enter this decoder. The
event preserves the first 16 servers in source order and reports the exact omitted
tail in `DroppedServers`. Each `MCPServerStatus` carries only the server object's
`name`, `status`, `error`, and `scope` plus `serverInfo.version`; request ids,
`config`, `tools`, `serverInfo.name`, and raw bytes are absent from the decode
targets. Optional values become empty strings, and only the free-form `Error` is cut
to 256 bytes with valid UTF-8 output. The new decoder has no logger:
`emitModelList` and `logControlResponse` remain the line's sole, content-free log
owner on success, rejection, and decode failure.

Shape validation does not establish that an MCP status reply is safe to publish:
a bypass child can author the same successful `mcpServers` shape while loading
private user- or project-scoped servers. The runner therefore installs one
`mcpStatusChildPolicy` on the concrete `Parser` before each `cmd.Start`, derived
from that child's completed argv snapshot. A child is eligible only when
`MCPStatusConfigPath` is non-empty, its argv contains exact
`--strict-mcp-config`, and its sole `--mcp-config` occurrence names that daemon
path (separate and joined forms are accepted). Missing, wrong, dangling, or
duplicate config flags fail closed. Because this decision uses the spawn
snapshot, changing the next spawn's args cannot reclassify the child already
running; [the factory](streamsup-package-constructing-a-streamrunner-newstreamrunnerfacto.md)
provides the daemon path beside the argv composition that may inject it.

For an eligible child, the first non-empty `ModelList` or `SlashCommandList`
emitted from a successful initialize-shaped reply triggers one
`Runner.RequestMCPStatus` attempt. The attempted latch is set before delivery,
so one reply containing both inventories still asks once, a failed write is not
retried on later inventories, and a replacement child receives a fresh latch.
Empty or unrecognised inventories trigger nothing. Delivery is best-effort: its
fixed Debug diagnostic carries no returned error or request/response content and
cannot enter child restart or backoff state. At the return boundary,
`Parser.emit` drops every `MCPStatus` from an installed ineligible-child policy
before the shared sink while forwarding all other event variants unchanged;
this also suppresses manually solicited status replies. A standalone parser has
no installed child policy and retains the shape decoder's existing behaviour.

`Runner.QueryMCPStatus` supplies the requester-only path for on-demand reads.
It snapshots the live child's writer, eligibility and generation under the runner
mutex. It registers an exact request id outside that lock, then rechecks the
generation before writing. This preserves the runner's leaf-lock rule while
rejecting a child replaced during registration.

`Runner.ReconnectMCPServer` and `Runner.SetMCPServerEnabled` (#2418) use this
same snapshot/register/recheck/write shape through `actuateMCP`, which delegates
to `actuateControl` with the MCP eligibility gate enabled. Their shared private
`mcpActuationIDPrefix` namespace keeps their acks from being
swallowed by `mcpStatusQueries.claim`, which consumes any unregistered id
carrying the status prefix. Both acks are a bare `control_response` with no
server list: a `success` subtype reports accepted, anything else — including
`error` — reports not-accepted, and a caller that wants a fresh inventory after
either call still issues a separate `QueryMCPStatus`. `Parser.claimMCPActuation`
sits beside `claimMCPStatusQuery` ahead of the shared control-response
consumers. It consumes unknown, duplicate and retired ids in the private
namespace without retaining tombstones; otherwise an ack carrying an
`MCPStatus`-shaped payload could reach the shared turn sink. Automatic numeric
ids and unrelated namespaces retain their existing paths. It decodes into the
same payload-free
`controlAckLine` target, so server names and claude-authored error text are
structurally unreachable from this path.

`Runner.StopTask(ctx, taskID) bool` (#2790), a concrete method outside
`sessions.Runner`, shares `actuateControl`, `registerMCPActuation` and
`claimMCPActuation`, including their private namespace. Overlapping stops and
MCP actuations each receive a distinct exact request id. `WriteStopTask` sends
one newline-terminated JSON line with `request.subtype:"stop_task"` and
`request.task_id` as string data. The `TaskID` pointer preserves an empty
string, structured encoding escapes metacharacters, and `task_id` remains
omitted from every other control subtype. A short write fails the write gate.
Stopping a task requires a live, non-rotating child and a correlation parser,
but omits `mcpStatusEligible`: MCP-config provenance does not establish whether
a child can stop a task. The MCP methods retain their eligibility gate.

The boolean reports Claude's acceptance, not evidence that the task finished.
Only a completed write and the first exact-id response with subtype `success`
can return true, with the captured child still current and the caller context
still live. Error, missing or unknown subtypes, failed writes, unavailable
children, rotation, cancellation and child boundaries all return false. An
early reply cannot override a failed write or an ended context. The parser
removes a matching registration before waiting for the write gate, so lifecycle
cleanup alone cannot fail an already-claimed reply; the final generation check
prevents it from reporting success after child replacement. Task ids, writer
errors and Claude's error text are neither logged nor returned by `StopTask`;
private replies reach neither shared event decoding nor its logging.

The caller context bounds writing and waiting, including a silent live child;
`StopTask` adds no timeout and requires no deadline. An already-ended context
prevents writing. During the write, a `context.AfterFunc` can call
`retireStdinGeneration` to close only the captured child's stdin and release a
blocked write; a replacement is untouched. The callback is stopped or joined
before resolving the write gate and waiting for the answer. Normal acceptance,
refusal and cancellation while awaiting a response preserve the child, current
reply and turn-busy state, sending no interrupt, restart or user turn. Write
cancellation is the retirement exception. Production's
`backgroundTaskStopper.StopBackgroundTask` owns membership and the 30-second
timeout, and `streamRunner.StopTask` forwards to this primitive (#2796); neither
method widens `sessions.Runner`. The
[mobile wire contract](../../protocol-mobile.md#stop-background-task-v2) keeps
acceptance separate from completion: the standing live proof observed roster
removal while the task's FIFO remained held. The initialize declaration makes
[Composer Stop turn-only](streamsup-package.md#composer-stop); explicit task
stop remains a separate operation.

`Parser.claimMCPStatusQuery` runs before the shared control-response consumers.
The first matching response retires the query, including an error or malformed
payload. A successful write must finish before an early reply can succeed.
The query reuses `decodeMCPStatus` and returns its event directly to the waiting
requester. It never calls the shared sink. The private id prefix also consumes
duplicates and late replies after cancellation or teardown without retaining a
growing list of completed ids. Automatic numeric ids keep their existing path.
No server text or request content is logged by the private path.

`Runner.QueryContextUsage` (#2430) and `Runner.QueryAppliedSettings` (#2505)
extend the same private snapshot/register/recheck/write pattern. They require a
live, non-rotating child, use disjoint locally minted id prefixes, and claim the
first match before shared control-response consumers. Their namespaces also
consume retired ids, keeping late predecessor replies out of successor results,
events and logs. An early reply waits for the writer's verdict, so a write that
later returns an error cannot yield a false success.

The payload contracts stay separate. `QueryContextUsage` shares the automatic
path's decoder and bounds, but not its unrelated MCP eligibility. Its `cmd/pyry`
resolver waits for turn idle, collapses nearby asks, and owns `detail:"full"`;
the post-turn path owns `"summary"`.

`QueryAppliedSettings` requires a caller deadline. In its
`(AppliedSettings, bool)` result, non-nil `Effort` preserves a bounded string,
nil with `bool=true` preserves explicit JSON `null`, and `bool=false` means
unavailable; a missing field never becomes `null`. `decodeAppliedSettings`
retains independently owned, bounded copies of only `applied.model` and
`applied.effort`, making effective settings, sources, paths, environment values
and credentials unreachable. The values are child-authored display data, not
authorization facts, and the private path does not log them. Its concrete
`cmd/pyry` adapter stays off `sessions.Runner`; client publication is separate.

A response-wait deadline alone does not bound a synchronous stdin write. If its
context ends while `WriteAppliedSettings` is blocked, the query retires the
captured generation, closes that child's stdin to release the write, and joins
the callback before returning; a replacement is untouched. Child start and exit
also drain pending reads. Sacrificing the unresponsive child avoids both an
unbounded waiter and a partial control envelope.

`New` infers correlation when `Config.Stdout` is directly the `*Parser`. When a
wrapper such as `io.MultiWriter` also observes raw output, `Config.ControlParser`
must name the exact parser receiving every child byte. Otherwise parsing works
but the query returns unavailable without sending `get_settings` — a live test
can appear wired while exercising nothing.

The 2026-09-20 credentialed gate ran two clean children before any user turn
with saved settings disabled. The inherited raw and production results agreed
on `claude-opus-5` / `high`; the explicit arm selected and reported the menu's
advertised `low`. `high` is an observation, never a daemon default: that arm
compares only with its independent raw `applied` decode.

An empty event sink cannot prove the private claim precedes logging, because its
id never entered the automatic request map. The non-vacuous test uses a capturing
logger and fails when the claim moves below `emitModelList`.

`emitStreamEvent` maps Claude's nested partial-message wire without changing the
downstream event contract. A valid `message_start` replaces the current message
ID and open-block state; a valid `content_block_start` records one index and
block type. Each `content_block_delta/text_delta` for that matching open text
block emits a `turnevent.TextChunk` immediately with the current message ID.
A `thinking_delta` for the matching open thinking block emits a
`turnevent.ThoughtChunk` with the message id, the emitting line's validated
`ParentToolCallID`, and an empty `Text`. The decode
target deliberately has no field for Claude's thinking bytes, so JSON decoding
discards them before the event exists; neither the event, a mapper, nor a log can
recover them. `turnbridge.MapEvent` still has no `ThoughtChunk` wire mapping;
only empty-parent thinking publishes lifecycle state. A recognised thinking
delta without a matching message and open thinking block is dropped silently
rather than routed through `Unrecognized`, whose raw diagnostic would otherwise
create a reasoning-text lane. Repeated empty-parent deltas may repeat this opener:
downstream state transitions de-duplicate publication and the busy tracker stores
membership rather than a count, so the parser needs no extra lifecycle latch.

The other measured lifecycle, signature, and partial tool-input events remain
content-free and silent, and partial JSON never competes with the completed
assistant `tool_use` block as the tool event's owner. Unknown, undecodable, or
unattributed inner events other than the recognised thinking case emit one
`Unrecognized`; text deltas are never accumulated or logged.

An empty-parent `ThoughtChunk` and a rate-bounded, empty-parent `ThinkingProgress`
are independent live evidence that a main interactive turn is running. Either
starts the turn identity and publishes `turn_state: thinking`; the progress path publishes
that state before its numeric reading. Later text or tool use joins the same turn
rather than minting another one. A client's optimistic message echo is not an
opener. See [per-conversation turn-busy tracking](streamsup-package-per-conversation-turn-busy-tracking.md)
for why the early turn cannot wedge delivery or published state.

The later completed assistant text is suppressed only for the captured
correlation: the line has exactly one text block, its message ID matches the
current stream message, and the corresponding open text block has already
emitted a delta. Multi-block assistant lines and text without delta evidence keep
the completed-text behavior. A `result` clears the whole stream-event composite,
so a later delta cannot inherit the prior message identity; `message_start`
replaces that identity between messages within the same turn.

Reset tests must isolate each field they claim to prove. To prove that `result`
clears message attribution, reopen a matching text block after the result before
sending the rejected delta. Without that setup, clearing only the block state is
enough to reject the delta, so a mutant that wrongly retains the old message ID
stays green.

`emitSystemSubtype` maps every `system/api_retry` line to
`turnevent.ApiRetry{Active:true}` with claude's `attempt` and `max_retries` values;
repeated active lines must not be coalesced because each advances the counter. The
parser's `apiRetryOpen` latch suppresses only duplicate falling edges:
`clearAPIRetry` emits one `ApiRetry{Active:false}` before the next successfully
decoded `assistant`, `user`, or `result` line's existing events. Missing or
non-integer counters still consume the known subtype and publish the active event
with `{Current:0, Total:0}`, rather than surfacing it as `Unrecognized`.

The latch deliberately belongs to the long-lived `Parser`, not a child lifecycle.
If a child dies without a result while retry is open, the state survives the
respawn and the next assistant/user/result line publishes the observable clear
before its own event. Resetting it on child exit would leave the client holding an
active state with no matching falling edge.

The unterminated-line remainder in `Parser.buf` is the opposite case: it does
*not* survive a child (#1503). Before #1503, a child killed mid-write (`killGrace`
SIGKILL, OOM, crash) left its fragment in `buf`, the respawned child's first line
was appended to it, `consumeLine` failed to decode the spliced bytes, and the
result was one `Unrecognized` event carrying both children's bytes with the real
first line lost. `Runner.spawnAndWait` now calls `(*Parser).dropPartialLine()`
once `cmd.Wait()` has returned, beside `takeStdin`. `Wait` joins the stdout copy
goroutine even past `WaitDelay`, so the dead child's last `Write` happens-before
the drop and the drop happens-before the next child's first `Write` — the
single-writer invariant holds with no added lock. The drop deliberately does not
live in `retireParserChild`: that helper also runs from `retireStdinGeneration`
on a caller's context while the child may still be writing, where dropping the
buffer would race `Write`. `stallTracker.childExited` (the watchdog's own copy of
this splice) keeps its buffer on purpose — there a spliced line only counts as
activity, so clearing it defends a failure mode never observed.

## Replayed user echoes carry a digest, never the text (#2730)

`--replay-user-messages` makes claude echo back, as an ordinary `user` line,
every message it reads — both a turn's opening message and one written
mid-turn by [`send_queued_now`](../../protocol-mobile.md#send_queued_now).
The #2728 capture ([e2e-realclaude-mid-turn-user-capture-test-go.md](e2e-realclaude-mid-turn-user-capture-test-go.md))
is what shows the echo lands right after the `tool_result` a mid-turn write
interrupted, which is the signal [history-package.md §
Producers](history-package-producers.md#producers-2114-2115) uses to place a send-now
message's push and history entry. The echo's content is a **block array**
(`{"type":"user","message":{"role":"user","content":[{"type":"text",…}]},…,"isReplay":true}`),
not the string content `dropHarnessProseLine` already filters — so turning
the flag on without a matching parser arm would have sent every echo to
`emitUnrecognized`, whose `Raw` is the delivery payload: for an
attachment-bearing message, a daemon-composed prompt naming on-host paths.
The flag and the arm below landed in the same commit on purpose.

`emitUser`'s replay arm catches a `text` block on a line with `IsReplay` true
and no `ParentToolUseID` — a subagent's own replay line still falls through to
the existing parent-id guard, unchanged — and emits **only**
`turnevent.UserEcho{TextSHA256: sha256.Sum256(text)}`, logging the site and
block type at Debug and never the text or even the digest (a digest of a
short message is itself a fingerprint of it). No downstream code holds echo
text: `turnMarkFor` classes `UserEcho` as an unrecognised variant (the
none/droppable default arm), so it is never an opener or a closer, and
`startStreamTurnDrainV2` hands it to a late-bound observer
(`streamTurnSink.setEchoObserver`, the `crashLoop` atomic-hook pattern) after
`busy.observe` and before the active-session gate — never to
`emitter.Handle`, so it reaches no client frame. `sendNowPlacement` is the
only installed observer, and it matches an echo to a pending send-now write
by digest equality alone; an ordinary message's opener echo matches no
pending entry and is silently dropped.

## Command lifecycle bookkeeping is consumed by state (#2877)

On 2026-10-06, Claude 2.1.280 was reported emitting `command_lifecycle`
`started`/`completed` pairs around peer-message turns in two conversations,
producing two Unrecognized cards per message. Only those two states were
observed; `queued`, `cancelled`, `discarded` and `refused` come from the
report's declared schema inspection. The [refinement evidence](https://github.com/pyrycode/pyrycode/issues/2877#issuecomment-6014442517)
contains one verbatim frame; the parser's two peer-message pairs use permitted
same-shape placeholder identifiers, not a new live capture.

`consumeLine` gives this type a separate arm outside `ignoredLineTypes`,
following [consume-by-matching](streamsup-package-tool-progress-consumed-by-matching.md).
It decodes only the top-level string `state`: `queued`, `started`, `completed`,
`cancelled`, `discarded` or `refused` emits no event and one Debug record with
exactly `site=line_type`, `type=command_lifecycle` and the matched `state`.
Identifiers, raw JSON and other payload values never enter that record.
Dropping the whole type would hide a new or malformed state from the parser-gap
sentinel. Missing, null, non-string, empty or unknown states instead produce
exactly one `turnevent.Unrecognized` with `Site=UnrecognizedLineType` and
`Kind=command_lifecycle`. `emitUnrecognized` retains the existing raw diagnostic
and `truncateRaw`'s 16 KiB cap with UTF-8 scrubbing; its Debug log contains only
site, type, byte count and truncation status, never the rejected state or decode
error. Matching neither trims nor changes the state string's case.

These frames read and write no parser lifecycle state. `started` may arrive
while idle and `completed` after `result`; neither opens or closes a turn,
clears a retry latch or resets the next turn's accumulators. Existing turn
events already represent the peer-message turn, and `marshalTurnEnvelope`
writes no command uuid on daemon user envelopes, so the bookkeeping identifiers
establish no daemon command correlation. `TestParser_CommandLifecycleStateNeutral`
checks all six states while idle, in an open turn and between turns, snapshots
parser state and compares subsequent normal events with a control parser.
An open-turn-only assertion would miss an accidental opener from idle.

## Claude's native prompt suggestion becomes one neutral event (#2829)

Claude can emit a top-level `prompt_suggestion` line after a turn's `result`
when prompt suggestions are enabled — the SDK's `SDKPromptSuggestionMessage`,
carrying `suggestion`, `uuid` and `session_id`. `consumeLine`'s `prompt_suggestion`
arm consumes every such line, valid or not, by matching on the top-level type
alone, so `emitUnrecognized` is structurally unreachable for it; nested
suggestion-shaped content inside an `assistant` block or a `stream_event` never
reaches this arm, since dispatch never looks past the outer `type`. The arm
reads and writes no parser state — no `clearAPIRetry`, no accumulator, no turn
boundary — because a suggestion arrives between turns and must neither close a
result already closed nor clear anything the next turn starts from.

`decodePromptSuggestion` is the pure decode gate behind `emitPromptSuggestion`.
It accepts only a JSON string `suggestion` field whose source bytes are valid
UTF-8 with no unpaired surrogate escape — both checked **before** string
decoding, because `encoding/json` silently turns an invalid byte or an unpaired
surrogate escape alike into U+FFFD, and a replacement character Claude never
sent must not be accepted as its text. A raw value longer than
`maxPromptSuggestionRaw` (the widest JSON spelling of 1024 decoded bytes: six
bytes per `\u00XX` escape plus the two quotes) is rejected before any Go string
is built from it, so an oversized value never costs a string allocation. After
decoding, the text must be non-blank (`unicode.IsSpace`-trimmed, not merely
ASCII-trimmed, so U+3000 and other Unicode whitespace reject the same as a
space), hold none of CR, LF, NEL (U+0085), LINE SEPARATOR (U+2028) or PARAGRAPH
SEPARATOR (U+2029), and be at most 1024 UTF-8 bytes. Accepted text is emitted
verbatim as `turnevent.PromptSuggestion{Text: text}` — never trimmed, truncated
or otherwise rewritten; missing, null, non-string, empty, whitespace-only,
over-length, invalid-UTF-8 and line-break-bearing values alike produce no event
and no `Unrecognized`. Every reject is silent on purpose: nothing about the
line is logged on any path, including the `json` decode error, which is not
logged because `encoding/json` quotes the offending input into its text.

`turnevent.PromptSuggestion` carries only `Text`. Claude's `uuid` and
`session_id` on the line are message identifiers, not turn identifiers, and
neither establishes daemon turn attribution, so neither is decoded or carried;
the producing session is already tagged by `streamTurnSink.sinkForTag`. The
type's own doc marks `Text` as claude-authored and untrusted: the decode gate
checks shape, not content, so control characters and bidi marks other than the
rejected line breaks pass through unchanged. The parser only recognises the
line and emits neutral text; the daemon's
[native suggestion owner](streamsup-package-draining-turnevents-into-the-interactive-emitter.md#native-reply-suggestions-after-the-result-2831)
decides eligibility, publishes current state and invalidates it on new work or
session lifecycle changes. Its message-ID tracking is separate from the
stateless parser because delivery confirmation can follow the result.
