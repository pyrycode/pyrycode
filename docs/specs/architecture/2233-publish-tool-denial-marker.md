# 2233 — publish the tool-denial marker on the wire

## Files read

- `internal/turnevent/event.go` → `ToolCallDenied` — the seven fields, their cut-vs-drop
  answers, and the doc stating `DroppedFields` names `tool_call_id`, **not** claude's
  `tool_use_id`. That one token is the whole translation this ticket owes.
- `internal/streamsup/parser.go` → `emitToolCallDenied`'s `cutField` / `dropField` closures
  and `maxDenialProse` — confirms the exact tokens the producer appends
  (`tool_name`, `tool_call_id`, `message`, `decision_reason_type`, `decision_reason`) and
  that both slices are nil when nothing fired.
- `internal/protocol/interactive.go` → `ToolResultPayload` (the file's no-`omitempty` rule,
  and the `tool_use_id` wire key this frame joins on), `RateLimitedPayload` (nil
  `TruncatedFields` reaching the wire as `null`, and why that type deliberately has **no**
  `MarshalJSON`), `BackgroundTaskRosterPayload.MarshalJSON` (the opposite polarity — nil→`[]`
  — and why it does not generalise), `CompactionBoundaryPayload` (the shape of the last
  frame added).
- `internal/turnbridge/outbound.go` → `MapEvent`, `TurnContext`, the `ToolUpdate` and
  `CompactionBoundary` arms — turn-scoped vs conversation-scoped addressing, and the
  standing "the producer already bounded it, so this arm re-caps nothing" rule.
- `cmd/pyry/interactive_turn_v2.go` → `Handle`, `eventKind`, `emitMapped`, `emit` — `emit`
  is the single capability gate; `Handle` is a whitelist, so a missing arm is silent death.
- `cmd/pyry/relay_guard_test.go` → `excludedTypes`; `internal/protocol/compat_test.go` →
  `v2OnlyTypes` and `TestTypeConstants_V1V2Partition`. Both redden on an unclassified
  constant; they are the deterministic gates for AC 3's first half.
- `docs/protocol-mobile.md` → § `tool_result`, § `compaction_boundary`, §
  `unrecognized_message` (the `raw` inert-text rule this frame's prose fields inherit), and
  the three sentences reading "fifteen".
- `docs/knowledge/features/protocol-package-interactive-event-payloads.md` — two lessons
  that change how this is built: **declaration order IS the wire's key order**, because
  `roundTripEnvelope` re-marshals the decoded struct and compares against the raw fixture;
  and **a presence-preserving field needs two fixture rows, because the weaker row passes
  either way**.
- `docs/knowledge/features/turnbridge-package.md` → the `MapEvent` row table, and
  `TestMapEventSlashCommandListDoesNotMutateTheEvent` — the established shape for proving
  an arm leaves its input event alone.

## Context

#2232 landed the parser half: claude's `system/permission_denied` line becomes a
`turnevent.ToolCallDenied`. Nothing sends it. This slice is the wire half — declare, map,
fan out, document — the sequence #1405/#1406 gave `rate_limited` and #2237 gave
`compaction_boundary`.

It is **a frame of its own, not two fields on `tool_result`**, and two facts close that
question rather than taste. The line arrives *before* the tool result
(`assistant/tool_use` → `system/permission_denied` → `user/tool_result`), so folding it in
would need the parser to latch cross-line state keyed by `tool_use_id`; and #2234's
result-line recovery reports denials for calls whose `tool_result` frame has already
shipped, which a field on a sent frame cannot answer. `tool_use_id` is the join key the
client already has.

No ADR is warranted: this frame follows `compaction_boundary`'s precedent end to end and
decides nothing the family has not decided before. The one genuinely new thing — a
report-slice token renamed at the bridge — is a two-line consequence of the wire key
differing from the daemon's internal field name, and it belongs in the payload doc rather
than in a decision record.

## Design

**`internal/protocol/codes.go`** — a new const block, `TypeCompactionBoundary`'s shape:

```go
TypeToolDenied = "tool_denied" // binary → phone, outbound v2 tool-denial marker
```

v2-only; it must not enter `inboundAppTypeSet`.

**`internal/protocol/interactive.go`** — `ToolDeniedPayload`, nine fields, no `omitempty`
on any, declared in the AC's stated order because that order is the wire's key order:

| Field | Tag | Notes |
|---|---|---|
| `ConversationID` | `conversation_id` | bridge-supplied |
| `TurnID` | `turn_id` | turn-scoped — the denial belongs to the turn that made the call |
| `ToolUseID` | `tool_use_id` | byte-identical to `tool_use` / `tool_result` for the same call |
| `ToolName` | `tool_name` | claude's tool token, open set |
| `DecisionReasonType` | `decision_reason_type` | claude's word for *what* denied it |
| `DecisionReason` | `decision_reason` | claude's free-form explanation |
| `Message` | `message` | claude's rejection prose |
| `TruncatedFields` | `truncated_fields` | nil → `null`; **no** `MarshalJSON` |
| `DroppedFields` | `dropped_fields` | nil → `null`; **no** `MarshalJSON` |

Two shape decisions the type's doc comment must carry:

- **`tool_name`, not `name`.** `ToolUsePayload` spells the same value `name` because on
  *that* frame the tool is the subject. Here it is one of several named things, and — the
  binding reason — the report slices must name fields by keys the frame carries, so the
  producer's `tool_name` token and the wire key have to be the same string.
- **Both slices pass nil through as `null`, and neither gets a `MarshalJSON`.**
  `RateLimitedPayload` is the precedent and its absent marshaller is deliberate.
  `BackgroundTaskRosterPayload`'s nil→`[]` means the opposite and does not generalise: an
  empty roster is a positive statement, whereas nothing-was-cut is an absence. Here an
  allocated `[]` would tell a phone that claude's cut text is complete.

**`internal/turnbridge/outbound.go`** — a `ToolCallDenied` arm returning
`TypeToolDenied` + the payload, addressed with `tc.ConversationID` and `tc.TurnID`
(`tc.Seq` ignored, as everywhere but `TextChunk`). Every string crosses verbatim; nothing
is re-capped, since the producer bounded all five at construction.

The one transformation is a helper:

```go
// deniedReportKeys returns report with the daemon's tool_call_id token replaced by this
// frame's tool_use_id wire key. nil in, nil out; never mutates or aliases the input.
func deniedReportKeys(report []string) []string
```

`turnevent.ToolCallDenied` names the id `tool_call_id` — the daemon's field name — and this
frame carries it as `tool_use_id`. A token naming a key the frame does not carry is one a
client cannot look up, so the bridge translates exactly that one token; the other four are
already this frame's wire keys. The helper **copies** rather than rewriting in place, and
returns a fresh backing array even when no token changed: the event is also observed by the
history-append path and by the other `eventKind` call sites, so mutating or aliasing the
producer's slice would let this arm's output be visible to them.

**`cmd/pyry/interactive_turn_v2.go`** — two arms.

`Handle` is a whitelist; with no arm the event never reaches `emitMapped`. The arm takes
`ToolStart`/`ToolUpdate`'s **turn-scoped** shape — `startTurnIfNeeded`, then `flushDelta`,
then `emitMapped` — rather than the status peers' conversation-scoped one, because this
frame carries a `turn_id` and must land in the same turn as the `tool_use` it names. It
deliberately does **not** call `transitionTo`: a denial reports no lifecycle change, the
`ToolStart` before it already set `responding`, and emitting a redundant `turn_state`
would claim a transition the event does not report. No capability gate in the arm — `emit`
filters once for every frame type, and writing a second is how a single gate stops being
single.

`eventKind` returns the variant name `"tool_denied"` and **nothing else** — not the tool
name, not the message. It feeds log fields.

`stream_turn_busy.go` needs nothing: #2232 settled `ToolCallDenied` as `turnMarkNone`
through the default and pinned it with a row in the turn-mark totality guard.

**`docs/protocol-mobile.md`** — a § `tool_denied` section placed after § `tool_result`
(topical, following `compaction_boundary`'s insertion beside `compacting` rather than an
append), and the three sentences reading "fifteen" corrected to "seventeen". Re-counted by
the documented recipe: `#### ` headings from `turn_state` through `model_announced` number
**sixteen** today — the fifteen the sentences describe plus `compaction_boundary`, which
#2237 added on 2026-09-08 without touching them — so this frame makes seventeen.

## Concurrency model

None introduced. `MapEvent` is a pure synchronous function; `Handle` runs on the emitter's
existing single-goroutine event loop. No goroutine is spawned and no lock is taken. The
only shared-state question the change raises is slice aliasing between the event and the
payload, which `deniedReportKeys`' unconditional copy removes.

## Error handling

No new failure mode. The arm cannot fail: every field is a string or a slice already
constructed and bounded by the producer, and there is no parse, no allocation that can
fail meaningfully, and no I/O. An event that reaches `Handle` with no cursor is dropped
before the type switch, unchanged. A marshalling failure in `emit` is the existing path.

`MapEvent` returns `ok == true` unconditionally for this variant — there is no suppression
branch, matching `ModelAnnounced`'s and `CompactionBoundary`'s posture: a zero-value event
maps to an all-empty frame rather than vanishing, because an empty `tool_name` is a fact
the report slices make readable.

## Testing strategy

- `internal/protocol/interactive_test.go` — two golden round-trips through
  `roundTripEnvelope`, with fixtures under `testdata/`. **Two rows, not one, because the
  weaker row passes either way**: `tool_denied.json` carries populated report slices and
  non-empty reason fields; `tool_denied_empty_reasons.json` carries the case every
  committed capture actually produces — both reason fields `""` and both slices `null` —
  which is the row that would fail if a `MarshalJSON` or an `omitempty` were ever added.
- `internal/protocol/compat_test.go` — `TypeToolDenied` into `v2OnlyTypes` and into
  `TestTypeConstants_V1V2Partition`'s list.
- `cmd/pyry/relay_guard_test.go` — `"TypeToolDenied": "push"` in `excludedTypes`.
- `internal/turnbridge/outbound_test.go` — a `TestMapEventOutbound` row for the populated
  case; a test that the id token is renamed (`tool_call_id` in, `tool_use_id` out) while
  the other four tokens cross unchanged and a nil slice stays nil; and a
  does-not-mutate-the-event check on `TestMapEventSlashCommandListDoesNotMutateTheEvent`'s
  pattern, since the rename is the first arm in this file that rewrites slice contents.
- `cmd/pyry/interactive_turn_v2_test.go` — the denial reaches an interactive conn as one
  `tool_denied` frame carrying the live turn id, and a conn without the `interactive`
  grant receives nothing (AC 3's second half).
- Frame size needs no fit test. `ToolCallDenied` carries at most `3*256 + 2*2048 = 4864`
  bytes of claude-derived text — the arithmetic `maxDenialProse`'s own doc states — plus
  two slices of daemon-authored literal field names, against a 65519-byte v2 envelope cap.
  A 13× margin against caps applied at construction is not a number a test defends;
  #2237 took the same posture for the same reason.

## Open questions

1. Does the section belong after `tool_result` or appended after `model_announced`?
   Resolved in favour of topical placement before implementation, on
   `compaction_boundary`'s precedent (inserted beside `compacting`, not appended). The
   heading recipe's endpoints are unchanged either way, so the count of seventeen holds.
2. Should the emitter arm `transitionTo(StateResponding)` as `ToolStart`/`ToolUpdate` do?
   Resolved: no — see Design. To be re-checked in Phase B against what the surrounding
   arms actually emit, and recorded under `## Revisions` if it changes.

## Security review

**Verdict:** PASS

**Findings:**

- **[Trust boundaries]** No MUST FIX. The boundary is claude's subprocess stdout →
  `internal/streamsup`'s decode target `systemPermissionDeniedLine`, and it is explicit
  and singular: that struct declares five fields, so `encoding/json` structurally discards
  claude's `session_id` and `uuid` rather than a scrub having to remove them. Everything
  downstream of it — this frame included — holds bounded-but-unsanitized model-authored
  text, and the payload doc plus the protocol section must say so at both ends. Nothing in
  the daemon keys a behaviour on any field here: no retry, no routing, no teardown. The
  frame is a **report, never an actuator**, which is what keeps a fabricated denial a
  misleading label rather than a lever.
- **[Trust boundaries]** SHOULD FIX, and it is the finding with teeth. `message` and
  `decision_reason` are claude-authored prose, and a rule-based denial may quote **the
  command line that was refused** — operator-typed text arriving at a phone. Phase B must
  give both fields § `unrecognized_message`'s `raw` rule verbatim in the payload doc and in
  the protocol section: render as inert text attributed to claude, never into an HTML sink,
  an attribute, a URL, or anything that executes or re-shells it. `decision_reason_type`
  and `tool_name` are short tokens and take `turn_end`'s `outcome` rule instead — switch on
  them against known values — which is the *narrower* latitude, not the same rule; the
  distinction `CompactingPayload.ErrorText` and `CompactionBoundaryPayload.Trigger` already
  draw next door. The captured denials name absolute host paths of the session's allowed
  working directories, so this is observed content, not a hypothetical.
- **[Error messages, logs, telemetry]** No findings, and the design decision is what makes
  it so: `eventKind` returns the bare variant name. The temptation is `ToolName` or
  `Message` — precisely the fields a log line explaining a denial would reach for — and
  returning either would put claude-authored text into the daemon's own structured log,
  where it is neither bounded by the log's expectations nor attributed. The producer
  already bounds every string before the event exists, so no oversized value can enter a
  log via any path; nothing is added here that could.
- **[Network & I/O]** No findings. Worst-case payload is `3*256 + 2*2048 = 4864` bytes of
  claude-derived text (caps applied at construction by `maxTaskFieldID` and
  `maxDenialProse`) plus two report slices whose entries are daemon-authored literals —
  roughly 55 bytes, and claude cannot inflate them because they are chosen by the producer,
  not copied from the line. Against the 65519-byte v2 application-envelope cap that is a
  13× margin, and unlike `slash_command_list` there is no per-entry dimension claude
  controls, so no frame-size cut is owed. This arm re-caps nothing, deliberately: a second
  bound here would be a number to keep in step with one that already holds.
- **[Concurrency]** No MUST FIX, one design consequence stated so it cannot be lost. The
  arm takes no lock and spawns nothing, but it is the first `MapEvent` arm that rewrites
  slice **contents**. Rewriting in place, or returning the producer's slice with one
  element substituted, would make this arm's output observable to the other consumers of
  the same event — the history-append path and the remaining `eventKind` call sites — and
  the corruption would be a wrong field name in a report a client reads as authoritative.
  `deniedReportKeys` therefore copies unconditionally and never aliases, and Phase B pins
  it with a does-not-mutate-the-event test.
- **[Capability gating]** No findings, by refusing to add one. `emit` is the single filter
  that drops every structured frame for a conn whose `interactive` capability was not
  echoed in `hello_ack`; the arm adds no second check. A second gate is how a single gate
  stops being single — two places to keep correct, and the one nobody remembers is the one
  that leaks. AC 3's second half is pinned by a test against a non-interactive conn rather
  than by code in the arm.
- **[Tokens, secrets, credentials]** Not applicable: no credential, token or key is read,
  minted, stored, compared or logged on this path. The two claude-authored identity keys
  that ride the same line — `session_id` and `uuid` — never entered the event, per the
  decode-target finding above.
- **[File operations]** Not applicable: no path is constructed, opened, stat'd or written.
  `message` may *name* absolute host paths in its text, and that is exactly why the render
  rule above forbids a client opening one as a path on its own filesystem — but no
  filesystem operation happens in this daemon on any field here.
- **[Subprocess / external command execution]** Not applicable: nothing here reaches
  `exec.Command`. The inverse hazard — claude's refused command line flowing outward as
  text a client might re-shell — is the SHOULD FIX above, and it is a render-boundary
  constraint on the consumer rather than an execution path in the daemon.
- **[Cryptographic primitives]** Not applicable: no randomness, hashing, comparison against
  a secret, or key material. The turn id the arm addresses with is minted by the existing
  `startTurnIfNeeded`, unchanged.
- **[Threat model alignment]** `docs/protocol-mobile.md` § Security model threat 1
  (claude-authored text crossing outward to a phone) is the one that lands, and the section
  written in Phase B must reference it as `compaction_boundary`'s does. The sealed-transport
  and pairing threats are untouched: this frame rides the existing interactive lane with no
  new endpoint, no new handler and no inbound direction. Inbound handling of a denial —
  a phone asking to re-authorize a blocked call — is **out of scope** and belongs to no
  ticket yet; nothing here creates an inbound path, and `compat_test.go` keeping
  `TypeToolDenied` out of `inboundAppTypeSet` is the structural guarantee of that.

**Reviewer:** builder (self-review per the security-review checklist)
**Date:** 2026-09-08

## Revisions

### 2026-09-08 — four stale count sentences in live prose, not three

The ticket names three sentences reading "fifteen" (§ Interactive events,
§ `session_transition`, § `model_list`) and AC 4 asks for those three. A sweep of the
file found a **fourth** in live prose — § `slash_command_list` carries
`session_transition`'s and `model_list`'s sentence verbatim — so correcting only the
named three would have left the file self-contradicting on the same page, which is the
condition AC 4 exists to end rather than to enumerate. All four now read "seventeen".

The remaining ten occurrences of "fifteen" are **dated changelog entries** and were
deliberately left alone: they record what was true when written, and rewriting a dated
entry to match today's count destroys the record the entry exists to keep.

The count itself was re-derived rather than taken from the ticket, by the recipe AC 4
names: `#### ` headings from `turn_state` through `model_announced` numbered sixteen
before this change and seventeen after, verified against the file both times.

Neither Open Question changed the design. Question 1 (section placement) landed as
planned, after § `tool_result`. Question 2 (whether the emitter arm should
`transitionTo`) was re-checked against the surrounding arms during implementation and
confirmed: `startTurnIfNeeded` mints a turn without emitting anything, and only
`transitionTo` emits a `turn_state`, so omitting it costs the client no addressing and
avoids claiming a transition the event does not report.
