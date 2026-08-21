# `internal/turnbridge` — outbound event-to-wire adapter

A pure value-to-value adapter mapping the neutral internal turn-event model
([`internal/turnevent`](turnevent-package.md), #606) OUT to the v2 interactive
mobile wire payloads ([`internal/protocol`](protocol-package.md), #607):
`MapEvent` shapes one `turnevent.Event` into a typed payload, `BuildTurnState`
shapes the `turn_state` payload the lifecycle machine drives. No I/O, no state,
no envelope-ID minting, no clock read, no sealing — all of that belongs to the
consumer, `cmd/pyry/interactive_turn_v2.go`'s `interactiveTurnEmitterV2`
(`.emit` wraps a payload into an `Envelope`; `.Handle`/`.transitionTo` own the
turn-lifecycle machine that calls `MapEvent`/`BuildTurnState`).

**History — the package used to bridge in both directions.** An **inbound
producer** (`producer.go` + `mapper.go`, #615, #679) drained the supervised
claude session's PTY-hosted tui-driver `Events()` stream and mapped each event
INTO the `turnevent.Event` model — the exact mirror of what `MapEvent` now does
OUT. #1348 deleted every terminal-driving claude path that fed and consumed it
(including `internal/supervisor` itself and `cmd/pyry/interactive_turn_stream_v2.go`'s
wiring), which orphaned both files without touching them; **#1543** deleted
`producer.go`, `producer_test.go`, `mapper.go` and `mapper_test.go` (dropping the
package's `tui-driver` dependency) and re-homed the package doc onto
`outbound.go`. There is no dedicated `docs/knowledge/codebase/1543.md` record —
that directory froze 2026-08-19, the day before #1543 landed — but the
producer's own history stays readable in the frozen ticket records linked under
§ Related below. The live inbound counterpart today is
[`internal/streamsup`](streamsup-package.md)'s stream-json parser — a
structurally different path with no `turnbridge` re-mapping step, since the
parser emits `turnevent.Event` directly.

## Files

```
internal/turnbridge/
├── outbound.go       MapEvent / BuildTurnState + summary helpers (#627)
└── outbound_test.go  table-driven model→payload + drop tests; inputSummary / resultSummary / truncate / BuildTurnState
```

## Public API

```go
// TurnContext is the per-event turn addressing the consumer supplies. The adapter
// never derives these — which conversation / turn / seq applies is a lifecycle
// decision owned by the consumer.
type TurnContext struct {
    ConversationID string
    TurnID         string
    Seq            int // per-turn assistant-delta order; consumed ONLY by TextChunk
}

// TurnState is the coarse lifecycle state BuildTurnState shapes into a turn_state
// payload. String-backed so the call site is enum-safe.
type TurnState string
const (
    StateThinking   TurnState = "thinking"
    StateResponding TurnState = "responding"
    StateIdle       TurnState = "idle"
)

func MapEvent(ev turnevent.Event, tc TurnContext) (typ string, payload any, ok bool)
func BuildTurnState(conversationID string, state TurnState) (typ string, payload protocol.TurnStatePayload)
```

Two exported types (`TurnContext`, `TurnState`), two functions, three state
constants — the entire public surface, since #1543.

## The outbound adapter (`MapEvent` / `BuildTurnState`)

`MapEvent` (#627) is a pure type-switch over the sealed `turnevent.Event`,
shaping one event + an explicit `TurnContext` into the matching v2 interactive
wire payload (#607). Every field is carried verbatim from `tc` + the event:

| `ev` concrete type | `typ` | `payload` | `ok` |
|---|---|---|---|
| `TextChunk` | `TypeAssistantDelta` | `AssistantDeltaPayload{tc.ConversationID, tc.TurnID, tc.Seq, ev.Text}` | true |
| `ToolStart` | `TypeToolUse` | `ToolUsePayload{…, ToolUseID: ev.ToolCallID, Name: ev.Title, InputSummary: inputSummary(ev.RawInput), Input: inputFields(ev.RawInput)}` (#1678) | true |
| `ToolUpdate` | `TypeToolResult` | `ToolResultPayload{…, ToolUseID: ev.ToolCallID, IsError: ev.Status == ToolStatusFailed, ResultSummary: resultSummary(ev.Content)}` | true |
| `TurnEnd` | `TypeTurnEnd` | `TurnEndPayload{…, StopReason: string(ev.Reason)}` | true |
| `Stall` (#639) | `TypeStall` | `StallPayload{tc.ConversationID}` (`tc.TurnID`/`tc.Seq` ignored — not turn-scoped, not a delta) | true |
| `ApiRetry` (#1074) | `TypeApiRetry` | `ApiRetryPayload{tc.ConversationID, ev.Active, ev.Current, ev.Total}` (`tc.TurnID`/`tc.Seq` ignored) | true |
| `Compacting` (#1074) | `TypeCompacting` | `CompactingPayload{tc.ConversationID, ev.Active}` (`tc.TurnID`/`tc.Seq` ignored) | true |
| `Unrecognized` | `TypeUnrecognizedMessage` | `UnrecognizedMessagePayload{tc.ConversationID, ev.Site, ev.Kind, ev.Raw, ev.Truncated}` (`tc.TurnID`/`tc.Seq` ignored — an unrecognized message has no turn we can honestly attribute it to) | true |
| `BackgroundTaskStarted` (#1394) | `TypeBackgroundTaskStarted` | `BackgroundTaskStartedPayload{tc.ConversationID, ev.TaskID, ev.ToolCallID, ev.Description, ev.TaskType, ev.TruncatedFields}` (`tc.TurnID`/`tc.Seq` ignored — a background task outlives the turn that spawned it) | true |
| `BackgroundTaskUpdated` (#1394) | `TypeBackgroundTaskUpdated` | `BackgroundTaskUpdatedPayload{tc.ConversationID, ev.TaskID, ev.Patch, ev.TruncatedFields}` (`tc.TurnID`/`tc.Seq` ignored) | true |
| `BackgroundTaskRoster` (#1394) | `TypeBackgroundTaskRoster` | `BackgroundTaskRosterPayload{tc.ConversationID, tasks, ev.DroppedTasks}` — `ev.Tasks` looped into `[]protocol.BackgroundTask`, nil left nil (the payload's own `MarshalJSON` owns nil→`[]`) | true |
| `ThinkingProgress` (#1386) | `TypeThinkingProgress` | `ThinkingProgressPayload{tc.ConversationID, ev.EstimatedTokens, ev.EstimatedTokensDelta}` (`tc.TurnID`/`tc.Seq` ignored — a periodic reading of an inference request in flight, not a turn-scoped fact) | true |
| `RateLimited` (#1410) | `TypeRateLimited` | `RateLimitedPayload{tc.ConversationID, ev.Status, ev.LimitType, ev.ResetsAt, ev.TruncatedFields}` (`tc.TurnID`/`tc.Seq` ignored — a usage-limit window is a condition of the account, orthogonal to whichever turn observed it). Nil `TruncatedFields` left nil, and here that nil is what reaches the wire as `null`: unlike `BackgroundTaskRosterPayload` two rows up, `RateLimitedPayload` deliberately has **no** `MarshalJSON`, because nothing-was-cut is an absence. `ResetsAt` crosses unclamped and unvalidated in both directions; neither string is re-capped (the producer bounded both at construction) | true |
| `ModelAnnounced` (#1638) | `TypeModelAnnounced` | `ModelAnnouncedPayload{tc.ConversationID, ev.Model, ev.Truncated}` (`tc.TurnID`/`tc.Seq` ignored — an announced model is a property of the turn's configuration, not a turn boundary: claude emits its `init` line once per turn, and `TestTurnMarkFor_TotalOverEveryVariant` pins the lifecycle answer as `turnMarkNone`). `Model` crosses byte-for-byte — no lowercasing, no re-cap, no charset check: the producer already bounds it at `streamsup`'s `maxModelField`, and `internal/relay`'s `validModel` is a deliberately different rule (it bounds a phone-supplied override, not a claude-supplied report). No suppression branch — a zero-value `ModelAnnounced` maps rather than dropping, the same posture `ThinkingProgress` and `RateLimited` both state. `ModelAnnouncedPayload` has no slice field and, unlike `RateLimitedPayload` one row up, no `MarshalJSON`, so the nil-vs-`[]` hazard does not arise here | true |
| `ThoughtChunk` | `""` | `nil` | **false** (drop) |
| nil / unknown | `""` | `nil` | false (drop) |

- `payload` is `any` because the payload structs share no marker interface; the
  consumer `json.Marshal`s it directly (same path as `MessagePayload`). It is always
  one of the concrete `protocol.*Payload` value structs, or `nil` when `!ok`.
- **Zero-value-safe.** A nil `ev` falls to the default → drop. Because #607's
  payloads carry no `omitempty`, boundary zero-values (`seq:0`, `is_error:false`)
  are always serialized — they reach the wire rather than vanishing.
- **Internal-only fields are not forwarded.** `ToolStart.Kind`/`Locations` and
  `*.MessageID` have no #607 wire home and are correctly dropped.
- **`is_error = (Status == ToolStatusFailed)`** — `completed`/`pending`/`in_progress`
  all map to `false`. Round-trips with the inbound `toolStatus` (failed↔error,
  completed↔success).
- **`ThoughtChunk` drops (ADR 025).** #607 defines no thought-text envelope and
  ADR 025 classes thinking as screen-sourced; so the thought *text is not forwarded*.
  The thinking **state** surfaces via `BuildTurnState(convID, StateThinking)`, which
  the **consumer's** lifecycle machine calls when it observes a `ThoughtChunk` —
  deciding "a ThoughtChunk means we are thinking" is a lifecycle decision, kept out of
  the pure mapper. The mapper supplies the *builder*; the consumer owns the *decision
  to call it*.
- **A new row's sentinel has to falsify what the row claims, not just differ from
  `tc`.** `RateLimited`'s rows use short, all-lowercase sentinels — fine for its own
  hazards, but silent on "no lowercasing": an all-lowercase `Model` sentinel survives a
  `strings.ToLower` mapper. `ModelAnnounced`'s rows (#1638) needed a mixed-case
  sentinel to kill that mutant, and a >256-rune one to beat both the producer's
  `maxModelField` (256) and this file's own `maxSummaryLen` (200) for a re-cap mutant
  at either bound to go red.
- **A "value never reaches a log" test needs a positive control that the value
  traversed the path at all**, or a consumer `Handle` arm that silently drops the
  event passes the test for the wrong reason. `ModelAnnounced`'s extension (#1638) to
  `cmd/pyry`'s `TestInteractiveTurnEmitterV2_NoAppOutputLogLeak` pairs log-absence
  with a decoded-payload presence assertion on the recorded push; the payload half is
  what actually caught the arm dropping the event silently — the log-absence half
  alone stayed green throughout.

### `BuildTurnState` — the lifecycle payload builder

Returns `TypeTurnState` + `TurnStatePayload{conversationID, string(state)}`. Concrete
return type (not `any`) because it is monomorphic — no consumer type assertion. The
consumer's lifecycle machine decides *which* state applies (thinking / responding /
idle) and calls this; the adapter only shapes the payload.

### Summary derivation (`inputSummary` / `resultSummary` / `truncate`)

The wire envelopes carry a human-readable **précis** (not the raw input/output). Three
pure helpers derive a bounded, single-line summary:

- **`inputSummary(json.RawMessage)`** — `json.Compact` (whitespace → one line) then
  `truncate`. Empty/nil **and** invalid-JSON both yield `""` — `RawInput` is
  best-effort/opaque (#606), so a malformed blob is a précis-less `tool_use`, not an
  error.
- **`resultSummary(turnevent.ToolContent)`** — **exhaustive** over the sealed
  `ToolContent` sum type so a future producer variant cannot silently vanish:
  `nil`→`""` (the legal status-only `ToolUpdate`), `TextContent`→its text,
  `DiffContent`→`Path`, `TerminalContent`→`"terminal <id>"` — each truncated. The
  live inbound producer (`internal/streamsup`'s `toolResultContent`) only ever
  emits `TextContent` or `nil`; the Diff/Terminal arms are unreachable today but
  handled (kept deliberately minimal) until a producer (the ACP adapter #600, or
  a refinement) emits them.
- **`truncate(s, max)`** — returns `s` unchanged at ≤ `max` runes; otherwise cuts at
  `max` runes (`[]rune`, not bytes) and appends `"…"`. Rune-aware so multibyte text
  never splits mid-rune.

`const maxSummaryLen = 200` bounds the précis to one line of ≤ 200 runes — a
**phone-display** bound, not a wire constraint (the envelope cap is far larger);
tunable if the mobile view wants a different cap.

### Per-field input extraction (`inputFields` / `inputValue`, #1678)

`inputSummary` flattens the whole tool input to one 200-rune line, which buries
the field an operator actually wants (an `Edit`'s `file_path`) inside the bulk
text it precedes. `inputFields(ev.RawInput)` sends the input's own top-level
fields instead, each bounded independently, and populates `ToolUsePayload.Input`
alongside — not instead of — the unchanged `inputSummary`. `RawInput` is read
twice on the same `ToolStart` arm and the two readings are deliberately
independent: a summary derived *from* the capped fields would silently change
`inputSummary`'s value, which this ticket must not do.

Behaviour: `len(raw) == 0`, a non-object (array/number/string/malformed), or an
empty object all yield `nil` — `inputSummary`'s existing "malformed blob is a
field-less tool_use, not an error" posture, extended to the map. Every value is
the input's own value **verbatim**: a JSON string is *decoded* (so an embedded
newline is a newline, not a re-quoted JSON literal), any other JSON type is its
compact JSON form. Entries are admitted **shortest value first**, ties broken by
key, against `maxInputTotalRunes`; an entry that does not fit whole is dropped,
never shortened, and the walk stops there. Shortest-first is what actually fixes
the reported bug — for a `Write{content, file_path}` or an `Edit{file_path,
new_string, old_string}`, the short identifying field is admitted before the
bulk text can spend the budget. **Sorted-key order silently reproduces the
original complaint**: `content` sorts before `file_path` and the budget is spent
before the identifying field is ever considered — this is a real trap, not a
hypothetical one, and any future rework of the admission order needs a
regression case shaped like it (`TestInputFields`'s `Write`-shaped row pins it).

**A `json.Unmarshal` trap that shipped correct only because a test happened to
catch it:** deciding "is this value a JSON string" by *trying* `json.Unmarshal(v,
&s)` and checking the error is wrong — a JSON `null` unmarshals into a string
successfully and leaves `""`, silently rendering `null` as an empty value instead
of the literal `"null"` its non-string sibling values get. `inputValue`
discriminates on the value's **leading `"` byte** instead, which is the only form
immune to this. The bug was caught in code review, not by design — the one table
row in `TestInputFields` covering non-string values happened to include a `null`
alongside a number, bool, array and nested object. A future edit to that row that
drops the `null` case would let a regression back in unnoticed.

Four new constants beside `maxSummaryLen`, each carrying its escaped-byte
arithmetic in its own doc comment in `maxDeltaTextBytes`'s form (`cmd/pyry`):
`maxInputValueRunes` (4000, per-value), `maxInputKeyRunes` (128, drops rather
than truncates an over-long key — a cut key would misname the field, where a
cut value is honestly marked with `…`), `maxInputFields` (16, an entry-count
cap the rune budget alone cannot substitute for, since per-entry JSON
structure costs bytes a content budget cannot see), and `maxInputTotalRunes`
(8500, keys and values summed, measured against the 65519-byte envelope cap by
`protocol.TestToolUsePayload_FitV2EnvelopeCap`). **`maxInputFields` shipped
correct but with no test standing on it**: code review found that deleting its
guard leaves both `internal/turnbridge` and `internal/protocol` fully green —
`TestToolUsePayload_FitV2EnvelopeCap` lives in `protocol`, builds its map by
hand, and cannot call the unexported `inputFields` at all, so it measures the
*envelope*, not the *producer's* entry-count guard. Not fixed as part of #1678
(non-blocking); a future touch of `inputFields` should add the missing
`TestInputFields` row (an input with more than `maxInputFields` entries,
asserting exactly `maxInputFields` survive) rather than assume the envelope test
already covers it.

`docs/protocol-mobile.md` § `tool_use` documents the wire shape (per-value cap,
total bound, `…` marker, empty-map polarity, and that `Input` values are
**display strings, not capabilities** — a `file_path` is model-authored text the
daemon neither resolved nor validated, and a client must not open or execute one
on its own).

### What the outbound adapter does NOT do (the seam)

`MapEvent`/`BuildTurnState` produce only the typed payload + discriminant. The
consumer (integration slice, building on #616's fan-out) owns the envelope `ID` mint,
`TS` clock read, `json.Marshal`, AEAD seal, `Push`, the drop-log for un-mappable
events, **and** every lifecycle decision (which conversation/turn/seq/state applies,
turn-id assignment, seq advancement, coalescing). See
`cmd/pyry/session_transition_v2.go` for the existing shape that wraps a payload into
an `Envelope`; the structurally-identical v2 coarse emitter `assistant_turn_v2.go`
was removed in [#699](../codebase/699.md), and the v1 coarse bridge
`cmd/pyry/assistant_turn.go` this paragraph originally also pointed at was removed in
[#913](../codebase/913.md). This is why the adapter is pure: every clock read,
counter, and I/O lives in the consumer.

## Concurrency model

None. `MapEvent`/`BuildTurnState` are pure synchronous functions — no goroutines,
channels, shared state, or shutdown sequence. Before #1543 this package also hosted
the only concurrent machinery it ever had (the deleted producer's `Run`/`drain`
re-subscribe loop plus its per-subscription Wait- and switch-watcher goroutines);
deleting it reduced the package's concurrency surface to zero. That history is in
[codebase/615.md](../codebase/615.md) and [codebase/679.md](../codebase/679.md).

## Not `security-sensitive`

A pure value-to-value mapper: no untrusted-party input, no capability decision, no
dispatch. All capability enforcement lives in the consumer slice (#616,
`cmd/pyry/interactive_turn_v2.go`), which carries the `security-sensitive` label.
Labelling this package would track data lineage rather than the security-relevant
design decision.

## Related

- [ADR 025](../decisions/025-mobile-remote-head-interactive-session.md) — § Phase 2
  structured streaming, § "The event model", § Backpressure.
- [turnevent-package.md](turnevent-package.md) (#606) — the pivot model `MapEvent`
  maps OUT of.
- [protocol-package.md](protocol-package.md) (#607) — the v2 interactive wire types
  `MapEvent` maps OUT to.
- [streamsup-package.md](streamsup-package.md) — the live inbound counterpart on the
  stream-json path (structurally different: no `turnbridge` re-mapping step; the
  parser emits `turnevent.Event` directly). Its `toolResultContent` is what
  `resultSummary`'s doc comment names above.
- [acpbridge-package.md](acpbridge-package.md) (#769) — the sibling outbound ACP
  adapter, the exact ACP mirror of this package's `MapEvent` (same neutral source,
  different wire framing).
- [codebase/627.md](../codebase/627.md) — this file's own ticket record (patterns +
  lessons).
- [codebase/639.md](../codebase/639.md), [codebase/1074.md](../codebase/1074.md) —
  the stall / api-retry / compacting `MapEvent` rows landed by those tickets.
- **History of the removed inbound producer** (deleted #1543; orphaned by #1348):
  [codebase/615.md](../codebase/615.md) (the producer), [codebase/609.md](../codebase/609.md)
  (the flush-signal coalescing seam it offered), [codebase/679.md](../codebase/679.md)
  (the follow-active subscriber), [codebase/686.md](../codebase/686.md) (per-conversation
  JSONL directory), [codebase/854.md](../codebase/854.md) (bootstrap resolver),
  [codebase/1062.md](../codebase/1062.md) (mid-turn switch fix),
  [codebase/1243.md](../codebase/1243.md) (the interrupt-marker signal) — all frozen
  and still searchable via `mcp__qmd__query(collection: "pyrycode-docs", …)`.
