# Interactive event payloads (#607, #638, #1074, #2237, #2233, #2324)

The **v2 additive application events** — the wire representation of
`internal/turnevent`'s neutral turn-event model (#606). They are **binary → phone
only**, sent **only** to a phone whose `interactive` capability was echoed in
`hello_ack`; an old phone never sees them and keeps the coarse v1 `message`
fan-out. Spec source: `docs/protocol-mobile.md` § Interactive events. They map 1:1
to the `Type*` constants `TypeTurnState` / `TypeAssistantDelta` / `TypeToolUse` /
`TypeToolResult` / `TypeTurnEnd` (all #607), `TypeStall` (#638), `TypeApiRetry`
/ `TypeCompacting` (#1074), `TypeCompactionBoundary` (#2237, its own const
block), `TypeToolDenied` (#2233, also its own const block), and `TypeBanner`
(#2256, also its own const block), plus `TypeToolProgress` (#2324, its own const
block — see
[Envelope types](protocol-package-constants-codes-go-envelope-types.md)).
The first five are the wire form of ACP-shaped turn events; `stall`, `api_retry`,
and `compacting` are the wire form of **internal-only** signals (no ACP
equivalent) — `stall` added in #638, the other two in #1074 as PTY-derived status
peers of `stall`. `compaction_boundary` and `tool_denied` are neither: both are
claude-authored facts with no ACP mapping and no PTY lineage of their own —
`compaction_boundary` is conversation-scoped like `stall` and carries no
daemon-computed field at all (see below); `tool_denied` is turn-scoped like
`tool_use`/`tool_result`, the frame it joins on `tool_use_id`.

```go
type TurnStatePayload struct {
    ConversationID string `json:"conversation_id"`
    State          string `json:"state"` // "thinking" | "responding" | "idle"
}

type AssistantDeltaPayload struct {
    ConversationID string `json:"conversation_id"`
    TurnID         string `json:"turn_id"`
    Seq            int    `json:"seq"`  // per-turn, non-negative, resets each turn
    Text           string `json:"text"` // coalesced chunk, not per-token
}

type ToolUsePayload struct {
    ConversationID  string `json:"conversation_id"`
    TurnID          string `json:"turn_id"`
    ToolUseID       string `json:"tool_use_id"`
    ParentToolUseID string `json:"parent_tool_use_id"` // Agent/Task call's own ToolUseID, "" on the main thread (#2191)
    Name            string `json:"name"`
    InputSummary    string `json:"input_summary"` // human-readable précis, not raw input
    Input           map[string]string `json:"input"` // tool input's own fields, capped (#1678)
}

type ToolResultPayload struct {
    ConversationID  string `json:"conversation_id"`
    TurnID          string `json:"turn_id"`
    ToolUseID       string `json:"tool_use_id"` // matches the tool_use this completes
    ParentToolUseID string `json:"parent_tool_use_id"` // ToolUsePayload's field, same meaning (#2191)
    IsError         bool   `json:"is_error"`
    ResultSummary   string `json:"result_summary"` // human-readable précis, not raw output
    ResultDetail    string `json:"result_detail"` // daemon-composed display text, e.g. a read's line count (#2024)
}

// #2324 — a non-terminal reading for the ToolUsePayload row with the same id.
type ToolProgressPayload struct {
    ConversationID string `json:"conversation_id"`
    TurnID         string `json:"turn_id"`
    ToolUseID      string `json:"tool_use_id"`
    ElapsedSeconds int    `json:"elapsed_seconds"`
}

type TurnEndPayload struct {
    ConversationID string `json:"conversation_id"`
    TurnID         string `json:"turn_id"`
    StopReason     string `json:"stop_reason"` // turnevent.TurnEndReason values, verbatim
    // #2223 — claude's own stop shape, beside StopReason rather than instead of it.
    Outcome        string `json:"outcome"`         // claude's result subtype, verbatim, open-set
    IsError        bool   `json:"is_error"`        // claude's own is_error flag; NOT derivable from Outcome
    TerminalReason string `json:"terminal_reason"` // claude's finer-grained cause; open-set
    // #2224 — claude's API error category, read off an `assistant` line, not `result`.
    ErrorCategory  string `json:"error_category"`  // why the API call failed; independent of Outcome
    // #2260 — four more result-line numbers. DurationMS/NumTurns are per turn;
    // DurationAPIMS/CostUSDTotal are session running totals the daemon never differences.
    DurationMS     int     `json:"duration_ms"`
    DurationAPIMS  int     `json:"duration_api_ms"`  // running total; routinely LARGER than DurationMS
    NumTurns       int     `json:"num_turns"`
    CostUSDTotal   float64 `json:"cost_usd_total"`   // claude spells this total_cost_usd; the one respelling
}

// #638 — the wire form of the internal-only turnevent.Stall onset marker.
type StallPayload struct {
    ConversationID string `json:"conversation_id"`
}

// #1074 — the wire form of turnevent.ApiRetry, a PTY-derived status peer of
// Stall. Active is the show (true) / clear (false) edge; Current/Total are the
// parsed `attempt N/M` counter ({0,0} when unparsed).
type ApiRetryPayload struct {
    ConversationID string `json:"conversation_id"`
    Active         bool   `json:"active"`
    Current        int    `json:"current"`
    Total          int    `json:"total"`
}

// #1074 — the wire form of turnevent.Compacting, a PTY-derived status peer of
// Stall. Banner-only: Active is the only field beyond ConversationID because
// tui-driver streams no compaction progress payload.
type CompactingPayload struct {
    ConversationID string `json:"conversation_id"`
    Active         bool   `json:"active"`
}

// #2237 — the wire form of turnevent.CompactionBoundary. A separate frame from
// CompactingPayload, not a wider one: claude states trigger/pre_tokens/post_tokens
// on a line that arrives after Compacting's falling edge has already shipped, so a
// client applies this frame to the divider it has already drawn.
type CompactionBoundaryPayload struct {
    ConversationID string `json:"conversation_id"`
    Trigger        string `json:"trigger"`     // claude's compact_metadata.trigger, open-set, bounded+dropped not cut
    PreTokens      *int   `json:"pre_tokens"`  // NO omitempty — absence must reach the wire as null, never 0
    PostTokens     *int   `json:"post_tokens"` // claude's own shape marks this one optional
}

// #2233 — the wire form of turnevent.ToolCallDenied: claude REFUSED a tool call it had
// already announced. Turn-scoped like ToolUsePayload/ToolResultPayload, the two frames
// it joins on ToolUseID — unlike CompactionBoundaryPayload and the status peers above it.
// It is a frame of its own rather than two fields on ToolResultPayload: the denial line
// arrives BEFORE the tool result, and #2234's result-line recovery reports denials whose
// tool_result frame has already shipped, which a field on a sent frame cannot answer.
type ToolDeniedPayload struct {
    ConversationID     string   `json:"conversation_id"`
    TurnID             string   `json:"turn_id"`
    ToolUseID          string   `json:"tool_use_id"`           // byte-identical to tool_use / tool_result for the same call
    ToolName           string   `json:"tool_name"`              // NOT `name` — see below
    DecisionReasonType string   `json:"decision_reason_type"`   // empty in every captured denial
    DecisionReason     string   `json:"decision_reason"`        // empty in every captured denial
    Message            string   `json:"message"`                // claude's rejection prose
    TruncatedFields    []string `json:"truncated_fields"`       // nil -> null; NO MarshalJSON
    DroppedFields      []string `json:"dropped_fields"`         // nil -> null; NO MarshalJSON
}

// #2256 — the wire form of turnevent.Banner: operator-facing text claude prints
// ABOUT the session (a hook's block reason, a loop notification) with nowhere
// else on the wire to go. Conversation-scoped like CompactionBoundaryPayload —
// no turn_id — because neither producer has one to attribute it to: a
// hook-refused prompt is never answered, and a notification rides claude's own
// queue. **CORRECTED 2026-09-10 (#2319): has a producer.** `system/informational`
// maps onto it (a hook's block reason reaching the operator for the first time);
// `system/notification` (#2258) is CLOSED AS ANSWERED 2026-09-10 — recorded
// unobserved with zero frames, so it had no field set to map.
type BannerPayload struct {
    ConversationID string `json:"conversation_id"`
    Level          string `json:"level"`      // claude's own key, adopted verbatim; open-set, dropped not cut
    Text           string `json:"text"`       // claude's content, renamed; prose, cut not dropped, 4 KiB bound owed by the producer (#2319's maxBannerText)
    Truncated      bool   `json:"truncated"`  // the PRODUCER's answer about Text; never recomputed downstream
    StopsTurn      bool   `json:"stops_turn"` // claude's prevent_continuation, renamed; a REPORT, never an actuator
}
```

- **No `omitempty` on any field — the deliberate inverse of the handshake/optional
  discipline.** Every field is always present on the wire so the fixtures pin the
  full shape and boundary zero-values can't silently vanish: `assistant_delta` with
  `seq: 0` and `tool_result` with `is_error: false` are pinned exactly. Pick the tag
  by whether a field's absence is meaningful — here it never is.
- **`State` and `StopReason` stay plain `string`, not named enums.** Same
  `MessagePayload.Role` precedent: the closed-set guarantee belongs at the consumer,
  not in the wire type. `State` is documented (`thinking` / `responding` / `idle`)
  in the struct doc comment; #608 picks the exact internal-event → state mapping.
- **`StopReason` carries the `turnevent.TurnEndReason` strings verbatim** (`end_turn`
  / `max_tokens` / `max_turn_requests` / `refusal` / `cancelled`) **without
  importing `internal/turnevent`** — `protocol` stays a stdlib-only leaf, and #608
  produces the field via `string(turnevent.TurnEnd.Reason)`. The wire-value/taxonomy
  alignment is documented, not enforced by a shared type. ADR 025's base `turn_end`
  shape is `{conversation_id, turn_id}`; `stop_reason` is the #607 extension per the
  ticket title, following the "spec follows the code" convention (ADR 025
  § Consequences).
- **`Outcome`/`IsError`/`TerminalReason` (#2223) carry claude's own stop shape
  beside `StopReason`, never instead of it.** Two fields are spelled like a
  `stop_reason` and neither is the other: `StopReason` stays the daemon's
  unchanged two-value classification (byte-identical for every subtype — the
  producer applies the two new fields' cap only at the *publish* site, never to
  the value `resultTurnEndReason` reads), and `Outcome` is claude's own subtype
  token, carried verbatim and never reconciled with it — a turn that hit
  `--max-turns` is legitimately both `end_turn` and `error_max_turns`. claude's
  `result` line separately carries a key literally named `stop_reason`, which
  this daemon does not forward under any name — a `stop_reason` on this wire is
  always the daemon's. `IsError` is read from claude, not derived from
  `Outcome`: claude sends `success` with `is_error: true` when a turn ends on an
  API error (a context overflow is the documented case), so deriving the flag
  from the subtype would silently read that turn as clean. All three are
  **claude-authored, open-set, and bounded but not sanitized** at construction
  by `internal/streamsup`'s `maxTurnEndStopField` — an over-long value is
  **dropped, not truncated**, `ModelWindow.ModelID`'s reasoning applied to a
  matched token rather than a join key: a cut token matches nothing a client
  could act on, so it would misinform where an absent value only under-informs.
  The render boundary owing sanitization (no control-character or
  terminal-escape stripping happens here) is the client's — see
  `docs/protocol-mobile.md` § `turn_end` § Security model.
- **`ErrorCategory` (#2224) answers a different question from the three fields
  beside it, and is spelled `error_category` rather than `error` on purpose.**
  `Outcome`/`IsError`/`TerminalReason` all describe how the turn ended and are
  read off the `result` line; `ErrorCategory` describes why claude's API call
  failed and is read off the wrapper level of an `assistant` message — a
  sibling of `message`, never a content block. A bare `error` key beside
  `is_error` on the same frame would read as `is_error`'s detail; the daemon
  already renames on this path (`outcome` is claude's `subtype`, and claude's
  own `stop_reason` is not forwarded at all under any name). The two axes are
  independent — a frame can carry `outcome: "success"` and
  `error_category: "rate_limit"` together — so a client must not derive one
  from the other. Same bound, same drop-not-truncate rule, same
  unsanitized-render-boundary-is-the-client's posture as its three siblings,
  applied by the same producer constant (`maxTurnEndStopField`), which now
  covers all four fields rather than three.
- **`DurationMS`/`DurationAPIMS`/`NumTurns`/`CostUSDTotal` (#2260) are four
  more result-line numbers, and the pair that looks most alike is the pair
  that disagrees.** `DurationMS`/`NumTurns` describe the turn that just
  ended; `DurationAPIMS`/`CostUSDTotal` are session running totals that only
  grow, and the daemon differences neither — both cross exactly as claude
  sent them. **`DurationAPIMS` is routinely *larger* than `DurationMS`**,
  which is the reading a client must not have: it is a running total, not an
  inner slice of the turn's own wall clock, and differencing consecutive
  `turn_end` frames does not recover a per-turn API time either. **A zero on
  any of the four is a number claude sent, not a sign the daemon failed to
  read the line** — one observed capture reports `duration_api_ms: 0` and
  `num_turns: 0` beside a non-zero `duration_ms` and cost. Plain `int`/`float64`,
  no pointers, unlike `CompactionBoundaryPayload`'s counts: all four keys are
  present and numeric on every observed `result` line, so an absent one would
  mean a decode failure or a future claude, not an ordinary shape. **Nothing
  is clamped, range-checked or ordered** — in particular there is no
  `duration_api_ms <= duration_ms` check, because it would reject the
  majority of observed lines. **These four do NOT take `Outcome`'s
  sanitization rule** — a JSON number carries no control character, escape,
  markup or URL, so no bound and no render-boundary obligation exist for them
  the way they do for the three claude-authored strings above — but the
  *misattribution* half of the same threat still lands: `CostUSDTotal` is
  claude's own estimate, not a billing statement, and the daemon verifies
  none of it, so a client rendering it as its own accounting of the
  operator's spend presents model-authored data as trusted chrome. See
  `docs/protocol-mobile.md` § `turn_end` for the full per-turn/running-total
  table.
- **`Seq` is `int`, not `uint64`.** A per-turn counter that resets each turn (the
  package count-field idiom: `DebugBundleDonePayload.Total`); `uint64` is reserved
  for the session-monotonic `Envelope.ID`.
- **`StallPayload` (#638) carries `conversation_id` only — no `turn_id`.** Like
  `turn_state`, a stall is a coarse conversation-level signal, not turn-scoped. It
  is the wire form of the internal-only `turnevent.Stall` (an onset-only marker:
  no clearing field — the phone self-clears on the next turn activity). The
  internal `Stall` carries no identity, so the bridge (#608 / #624-B) supplies
  `ConversationID` at wire-mapping time. Same no-`omitempty` discipline as its five
  predecessors. `internal/protocol` does **not** import `internal/turnevent` — the
  two layers are decoupled, bridged only at the string value `"stall"`.
- **`ApiRetryPayload` / `CompactingPayload` (#1074) are PTY-derived status peers
  of `StallPayload`, not onset-only.** Unlike `stall`, both carry an explicit
  `active` clear edge (`false`) so a remote head can dismiss the indicator once
  claude recovers — `stall` has no such field because the phone self-clears it on
  the next turn activity instead. `ApiRetryPayload`'s `current`/`total` are the
  only screen-derived fields in this section: both are bounded ints the mapper
  reads from tui-driver's already-parsed `ApiRetryAttempt{Current, Total int}` —
  never a string, so raw banner/screen text is structurally unable to reach the
  wire. Same no-`omitempty`, no-`turn_id`, bridge-supplies-`ConversationID`
  discipline as `StallPayload`.
- **`CompactingPayload` is no longer PTY-derived, and as of #2236 it is no
  longer daemon-authored either.** The "banner-only (tui-driver streams no
  compaction progress)" doc comment was already false the moment #2227 gave the
  frame its first real producer — #1348 had deleted the driver it rested on —
  and #2236 replaced it rather than patch it. `Result`/`ErrorText` are claude's
  own `compact_result`/`compact_error` off the closing `system/status` line,
  which makes this the first frame in the section carrying claude-authored text
  where every prior field was the daemon's own (an id it assigned, a bool it
  computed). `Result` follows `TurnEndPayload.Outcome`'s open-set rule — an
  unrecognised token is unknown, not an error. `ErrorText` is free-form prose,
  not a token, and that shape difference is why it does **not** inherit
  `ErrorCategory`'s SECURITY paragraph verbatim: prose can carry newlines, ANSI
  escapes, markdown, or text impersonating daemon chrome inside its 256-byte
  cap, so a renderer must treat it as inert text — never fed to an HTML sink, an
  attribute, or a URL (`UnrecognizedMessagePayload.Raw`'s rule) — and always
  attribute it to claude, never show it as the daemon's own finding. Both
  fields read empty on a rising edge by construction (the arm that emits it has
  read only `Status`), and empty on a falling edge is one reading for three
  causes a client cannot and need not distinguish: absent, empty, or the
  daemon's own turn-boundary reset closing the edge with no claude line to read
  them off. Bounded at 256 bytes each by `internal/streamsup`'s
  `maxCompactField`, which **cuts** rather than drops — unlike
  `maxTurnEndStopField`'s drop-not-truncate rule for `Outcome`/`ErrorCategory`.
  The two packages disagree on purpose: a cut token can match no known value
  while still looking like one (bad for `Outcome`), but a cut sentence still
  reads as what it is (fine for prose), and `compact_result` is a short token in
  every observed line so it is not realistically cutable at all. See
  `docs/protocol-mobile.md` § `compacting` § Security model for the full
  argument.
- **`CompactionBoundaryPayload` (#2237) is a bigger class change than
  `CompactingPayload`'s, and the payload doc says so explicitly.**
  `CompactingPayload.Active` is the daemon's own bool, computed from a string
  comparison the daemon makes; here **every field is claude's, including the
  fact of the boundary itself** — the frame is produced even when no
  `compacting` edge preceded it, so it can be the *only* evidence a compaction
  happened. Render it as claude's assertion, attributed to claude, never as the
  daemon's own finding — nothing in the daemon acts on any field of it. `Trigger`
  takes `TurnEndPayload.Outcome`'s open-set-token rule (dropped past its bound,
  not cut) rather than `CompactingPayload.ErrorText`'s free-prose rule, since a
  client switches on it rather than displaying it.
- **`PreTokens`/`PostTokens` are `*int` with no `omitempty`, `SessionTransitionPayload.WorkspaceCwd`'s
  shape rather than the usual pointer-plus-omitempty pattern**, because a count
  claude omitted and a count of zero are different facts a client must not
  collapse — omitting the key would read as "absent" only by convention a client
  could get wrong; a literal `null` cannot be misread. **A presence-preserving
  field needs two test rows, not one, and the weaker row passes either way**: a
  plain `int` satisfies "explicit zero crosses as zero" and quietly fails "absent
  crosses as absent" — both rows green is the only state in which the distinction
  has actually survived, and the same pair matters again at the wire, where the
  real claim is that a nil pointer *encodes* back to a literal `null`, which only
  a byte-exact round-trip catches.
- **The allowlist decode (`streamsup`'s three-field `compactMetadata` target)
  makes a "no uuid crosses" leak test structurally true, which is worth stating
  rather than treating as a hard-won guarantee**: `encoding/json` discards every
  undeclared key, so the event cannot hold a uuid regardless of what claude adds
  to the line later. A sweep asserting no uuid appears in the marshalled event is
  a tripwire against a future field, not a proof about today's code — the actual
  guarantee is the three-field decode target itself.
- **`ToolDeniedPayload` (#2233) spells its tool token `tool_name`, not `name`, and the
  divergence from `ToolUsePayload.Name` is deliberate, not drift.** On `tool_use` the
  tool *is* the subject, so an unqualified `name` is unambiguous; here it is one named
  thing among several, and the binding reason is the two report slices below: they name
  fields by keys *this* frame carries, so the producer's token and the wire key have to
  be the same string.
- **`TruncatedFields`/`DroppedFields` make three states decidable for the same field,
  and that is the whole reason a `ToolDeniedPayload` needs two slices where
  `CompactionBoundaryPayload.Trigger` needed none.** For any of `tool_use_id`,
  `tool_name`, `decision_reason_type`, `decision_reason`, `message`: empty and named in
  neither slice means claude sent nothing; empty and named in `dropped_fields` means the
  daemon emptied an over-cap value; present and named in `truncated_fields` means the
  daemon cut claude's text to fit. `RateLimitedPayload.TruncatedFields` only ever needed
  the first two readings because nothing on that event drops; `trigger` needed none of
  the three because it is the only droppable value on its frame, so an empty `trigger`
  is already unambiguous. Both slices are `nil` when nothing fired and reach the wire as
  `null`, `RateLimitedPayload`'s posture rather than `BackgroundTaskRosterPayload`'s
  nil→`[]`: an allocated `[]` here would tell a phone claude's cut text is complete.
- **The report slices are the frame's own vocabulary, and the producer's field name for
  the join key is not it.** `turnevent.ToolCallDenied.TruncatedFields`/`DroppedFields`
  name the id `tool_call_id` — the daemon's internal field name — but this frame
  publishes it as `tool_use_id`. `internal/turnbridge`'s `deniedReportKeys` translates
  exactly that one token at the bridge and copies the slice unconditionally rather than
  aliasing the event's array (see [turnbridge-package.md](turnbridge-package.md)); a
  token naming a key the frame does not carry is one a client cannot look up. **When a
  payload's field name diverges from the producing event's, a report slice naming that
  field needs translating at the same seam** — nothing in the type system catches a
  missed rename, since both sides are plain `[]string` and a round-trip fixture is
  byte-equal either way.
- **A doc comment's claim that a report slice lists its tokens "in declaration order" is
  not provable by anything that runs, and shipped false here.** `ToolDeniedPayload`'s
  `TruncatedFields`/`DroppedFields` doc comments enumerate their tokens in *this
  struct's* field order, but the actual order is the producer's call order —
  `internal/streamsup`'s `emitPermissionDenied` appends `dropField`/`cutField` calls in
  its own sequence, and `deniedReportKeys` preserves index order while renaming one
  token — so the wire ships `["message","decision_reason"]` (the reverse of the
  declared order) and `["tool_name","tool_use_id","decision_reason_type"]` (a
  transposition of it), pinned by this ticket's own fixture and table test one screen
  away from the comment that contradicts them. Neither catches it: `roundTripEnvelope`'s
  byte-exact re-marshal proves *struct field* → JSON *key* order (the real "declaration
  order is wire order" rule, and it does hold — see below), not the *contents* of a
  `[]string` value; and nothing compares the slice against the false order because both
  slices are consumed by membership, not position. Flagged by code review as a
  non-blocking SHOULD FIX and left uncorrected as of #2233. The lesson generalises past
  this one struct: a report slice's runtime order is a fact about the producer's call
  sequence, never a fact inferable from the consuming struct's field declarations, and
  a future field-order claim on one needs re-deriving from the producer, not copied
  from a sibling.
- **Pure DTOs: no methods, no constructors, no `Validate()`.** Identical posture to
  every v1 slice. The intersection-of-capabilities trust decision, the
  internal-event → envelope mapping, and the capability-gated push all live in the
  consumer (#608).
- **`ToolUsePayload.Input` (#1678) sends the tool input's own top-level fields
  instead of one flattened, 200-rune-capped précis.** Each value is the input's
  value verbatim — a JSON string decoded, any other JSON type in its compact
  form — with the bounds owned entirely by the producer
  (`internal/turnbridge`'s `maxInputValueRunes` / `maxInputKeyRunes` /
  `maxInputFields` / `maxInputTotalRunes`); this struct re-decides no maximum of
  its own, the `RateLimitedPayload` precedent for not letting two layers
  disagree silently about a limit. Two things were considered and rejected
  before this shape: a `kind` discriminant (the client can already run the
  identical name-based switch `internal/streamsup`'s `toolKind` runs) and a
  pre-chosen `subject` field (that would make the daemon own a display
  decision — collapsed vs. expanded row — the client is better placed to
  make). `InputSummary` is untouched by this change: same meaning, same
  value, same cap, and it remains the whole-input fallback when the total
  budget drops a field.
- **`ParentToolUseID` (#2191) is a display and join hint, not a capability, and the security
  review made the daemon say so at both structs.** A client that reads a `tool_use_id`-shaped
  field could reasonably try to look it up or dereference it; nothing in the daemon does either
  — no code branches on the value, so a subagent that persuades claude to name an unrelated
  call's id mislabels one row's parent and nothing more. Bounded and dropped (not cut) at
  `maxTaskFieldID` by `internal/streamsup`'s `parentToolUseID`, the producer's own join-key
  reasoning: a cut id would still look like a real one and could join a row to the wrong parent,
  where an empty value degrades to top-level rendering, the pre-#2191 behaviour. See
  [turnevent-package.md](turnevent-package.md) § `ToolStart`/`ToolUpdate` for the field's origin
  and [turnbridge-package.md](turnbridge-package.md) for the straight-through mapping arm.
- **`ToolProgressPayload` (#2324) updates an existing tool row; it is not another
  lifecycle edge.** Reusing `tool_use_id` is load-bearing: a new id spelling would
  make a client create or miss a row instead of joining the `ToolUsePayload` it
  already holds. `ElapsedSeconds` is claude's signed report, so the daemon neither
  clamps it nor replaces it with its own clock, and `ToolResultPayload` remains the
  only close. Heartbeats follow claude's cadence and are independently droppable;
  absence, gaps, zero, and negative readings therefore prove nothing about whether
  the call ran, stopped, or restarted. The id and count are display data, never
  authority or timing evidence.
- **`ToolUsePayload.MarshalJSON` (#1678) is the file's second custom
  marshaller, following `BackgroundTaskRosterPayload`'s pattern exactly:** a
  nil `Input` normalises to `"input":{}`, never `"input":null`, because the
  payload deliberately does not distinguish an absent, empty, or non-object
  tool input — there is nothing for `null` to mean that `{}` does not, and
  `{}` is iterable without a branch in every client language. `Input`'s
  presence is also what makes `ToolUsePayload` non-comparable with `==`; every
  existing comparison already goes through `reflect.DeepEqual` or byte
  equality.
- **`ToolResultPayload.ResultDetail` (#2024, four more sidecar shapes folded in by #2025) needs
  no rune cap of its own, unlike its sibling `ResultSummary`.** The producer
  (`internal/streamsup`'s `toolResultDetail`, renamed from the single-shape `readLineCount`
  when #2025 turned it into a five-arm dispatch) formats bounded `int64`s and fixed literals —
  never claude's text carried through — so no byte decoded from claude's own bytes ever reaches
  the field. Its alphabet is no longer ASCII-only: `+10 −3` and `created · 54 lines` (#2025) use
  U+2212 MINUS SIGN and U+00B7 MIDDLE DOT, the field's first non-ASCII bytes. `encoding/json`
  escapes neither rune, so no JSON-escape cost enters the wire-size arithmetic and the field
  still needs no cap — the load-bearing claim was never "ASCII", it was "no claude-authored
  byte", and that one still holds. `resultSummary`'s cap exists because that field *is* claude's
  text, verbatim, with no bound of its own; a claude-derived field needs the same treatment only
  when the daemon is forwarding claude's bytes rather than formatting its own. The field is
  named generically rather than after the read shape because it now composes five shapes onto
  one wire field without minting a new type — see
  [streamsup-package-content-blocks-are-held-as-json-rawmessage.md](streamsup-package-content-blocks-are-held-as-json-rawmessage.md)
  for the dispatch itself and the fifth stale ASCII-alphabet claim #2025's security review found
  beyond the four the ticket had named.
- **`TestToolUsePayload_FitV2EnvelopeCap` (#1678) is a second instance of the
  measured-envelope pattern the background-task section below established:**
  fills every value with `'<'`, plus the two upstream-unbounded identity
  fields (`name` at a hostile 512 runes) the cap test cannot otherwise assume
  sane. Measured **56618 B, 86.4%** of the 65519-byte cap. Same never-raise
  rule: if the test ever fails, the fix is to lower `internal/turnbridge`'s
  constants, not this test's literal.

- **`BannerPayload` (#2256) is the first frame in this file whose vocabulary pin has
  to exclude an adopted-verbatim word from BOTH halves, not just the payload half
  every prior sibling pin already knew about.** `Level` is claude's own key kept
  unchanged, so `TestBannerType_IsNotClaudesVocabulary` cannot check it in the
  type-name half (asserting the type isn't derived from a word the frame
  deliberately keeps is backwards, even though it happens to read green against
  `banner` today) any more than in the payload half — there `level` is this
  struct's own wire key, exactly `TestSlashCommandListType_IsNotClaudesVocabulary`'s
  `commands` trap and `TestModelListType_IsNotClaudesVocabulary`'s `models` trap,
  both of which only ever bit the payload half because neither sibling's adopted
  word was also relevant to the name check. A future field adopted verbatim from
  claude needs both halves reasoned about, not just the one those two taught.
- **On a frame carrying free-form claude prose, a vocabulary pin's payload-bytes
  check may test only wire KEYS, never words claude merely SAID.** `Text` carries
  arbitrary prose, so adding `notification` or `informational` to the bytes check
  would pass every fixture and go red the day a real banner happens to use that
  word — a hazard no prior sibling pin faced, since none of them published
  free-form text alongside a claimed vocabulary. `TestBannerType_IsNotClaudesVocabulary`
  checks only `content`/`prevent_continuation` (structural keys this shape does not
  adopt), never the two claude subtypes the frame's own text could legitimately quote.
- **`Level`/`Text` take opposite bound rules on the same frame** —
  `CompactionBoundaryPayload.Trigger`'s single-field argument applied to a pair:
  `Level` is a token a client matches, so an over-long one is dropped, not cut (a
  cut token matches no known value while still looking like one); `Text` is prose,
  so an over-long one is cut, not dropped (a cut sentence still reads as what it
  is). `Truncated` reports only the producer's answer about `Text` — no
  `DroppedFields` companion for an emptied `Level` exists because an empty scalar
  is already directly observable, `CompactionBoundaryPayload.Trigger`'s unreported
  drop again. Both bounds are the producer's to enforce (#2319's `maxBannerText`);
  neither this struct nor `turnbridge.MapEvent` re-decides a maximum —
  `ToolDeniedPayload`'s one-cap-site rule, and see
  [turnbridge-package.md](turnbridge-package.md) for the mutation-test shape that
  actually proves the bridge doesn't.
- **`StopsTurn` is a report, never an actuator, and the field's own NAME is the
  trap.** It reads like a lever, and nothing in the daemon branches on it or on
  any other field of this payload — that is what keeps a fabricated banner a
  misleading label rather than a self-service turn abort a compromised claude
  could pull.

Golden round-trip tests in `interactive_test.go` decode each fixture through
`Envelope` → `Envelope.Payload` → per-type struct, assert each field (incl. the
boundary `Seq == 0` / `IsError == false`, `StopReason == "end_turn"`, and the
`api_retry` fixture's non-zero `current`/`total`), then re-marshal
byte-equivalently. `testdata/turn_end.json` gained `outcome`/`is_error`/
`terminal_reason` (#2223), then `error_category` (#2224), then
`duration_ms`/`duration_api_ms`/`num_turns`/`cost_usd_total` (#2260) — each in
`TurnEndPayload`'s declaration order, appended after the previous field rather
than interleaved, since `roundTripEnvelope` re-marshals the decoded struct and
compares to the raw fixture, so declaration order *is* the wire's key order.
\#2260's fixture values are chosen so `duration_api_ms > duration_ms`
deliberately — a fixture where the two agreed would round-trip cleanly while
pinning nothing about the reading the field exists to foreclose. A second
test decodes a pre-#2260 frame missing all four keys and asserts they read
their zero value while the other seven survive, the "optional means no
`omitempty`, not a pointer" contract this package states at
`ToolResultPayload`. The shared `roundTripEnvelope` helper re-marshals the
**decoded payload struct** (not the original `RawMessage`) back into the envelope —
that is what pins struct → wire shape, since a missing or reordered json tag only
surfaces when the bytes are actually re-encoded (the original-`RawMessage`-passthrough
variant cannot catch it). `TestToolUsePayload_RoundTrip`'s fixture
(`testdata/tool_use.json`) carries a `WebSearch` input whose one field is the
query, matching the pre-existing `input_summary` value; `Input`'s empty-map
polarity is pinned separately by `TestToolUsePayload_NilInputNormalises` (a
direct marshal, no fixture — mirroring `TestBackgroundTaskRosterPayload_NilTasksNormalises`
below) and by `TestToolUsePayload_FitV2EnvelopeCap` (above).
`tool_progress.json` uses pairwise-distinct identifiers and a negative count so a
field swap or clamp cannot hide behind equal-looking values; its zero-value sibling
inspects all four raw keys before decoding, so regenerating a fixture cannot quietly
turn an always-present zero into an omitted field.
