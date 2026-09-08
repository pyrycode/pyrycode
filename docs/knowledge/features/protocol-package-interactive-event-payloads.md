# Interactive event payloads (#607, #638, #1074)

The **v2 additive application events** — the wire representation of
`internal/turnevent`'s neutral turn-event model (#606). All eight are **binary →
phone only**, sent **only** to a phone whose `interactive` capability was echoed in
`hello_ack`; an old phone never sees them and keeps the coarse v1 `message`
fan-out. Spec source: `docs/protocol-mobile.md` § Interactive events. They map 1:1
to the `Type*` constants `TypeTurnState` / `TypeAssistantDelta` / `TypeToolUse` /
`TypeToolResult` / `TypeTurnEnd` (all #607), `TypeStall` (#638), and `TypeApiRetry`
/ `TypeCompacting` (#1074). The first five are the wire form of ACP-shaped turn
events; `stall`, `api_retry`, and `compacting` are the wire form of
**internal-only** signals (no ACP equivalent) — `stall` added in #638, the other
two in #1074 as PTY-derived status peers of `stall`.

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
    ConversationID string `json:"conversation_id"`
    TurnID         string `json:"turn_id"`
    ToolUseID      string `json:"tool_use_id"`
    Name           string `json:"name"`
    InputSummary   string `json:"input_summary"` // human-readable précis, not raw input
    Input          map[string]string `json:"input"` // tool input's own fields, capped (#1678)
}

type ToolResultPayload struct {
    ConversationID string `json:"conversation_id"`
    TurnID         string `json:"turn_id"`
    ToolUseID      string `json:"tool_use_id"` // matches the tool_use this completes
    IsError        bool   `json:"is_error"`
    ResultSummary  string `json:"result_summary"` // human-readable précis, not raw output
    ResultDetail   string `json:"result_detail"` // daemon-composed display text, e.g. a read's line count (#2024)
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

Eight golden round-trip tests in `interactive_test.go` decode each fixture through
`Envelope` → `Envelope.Payload` → per-type struct, assert each field (incl. the
boundary `Seq == 0` / `IsError == false`, `StopReason == "end_turn"`, and the
`api_retry` fixture's non-zero `current`/`total`), then re-marshal
byte-equivalently. `testdata/turn_end.json` gained `outcome`/`is_error`/
`terminal_reason` (#2223) in `TurnEndPayload`'s declaration order — appended
after `StopReason`, never interleaved, since `roundTripEnvelope` re-marshals
the decoded struct and compares to the raw fixture, so declaration order *is*
the wire's key order. The shared `roundTripEnvelope` helper re-marshals the
**decoded payload struct** (not the original `RawMessage`) back into the envelope —
that is what pins struct → wire shape, since a missing or reordered json tag only
surfaces when the bytes are actually re-encoded (the original-`RawMessage`-passthrough
variant cannot catch it). `TestToolUsePayload_RoundTrip`'s fixture
(`testdata/tool_use.json`) carries a `WebSearch` input whose one field is the
query, matching the pre-existing `input_summary` value; `Input`'s empty-map
polarity is pinned separately by `TestToolUsePayload_NilInputNormalises` (a
direct marshal, no fixture — mirroring `TestBackgroundTaskRosterPayload_NilTasksNormalises`
below) and by `TestToolUsePayload_FitV2EnvelopeCap` (above).
