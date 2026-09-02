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
| `ModelList` (#1848) | `TypeModelList` | `ModelListPayload{tc.ConversationID, models, ev.DroppedModels}` (`tc.TurnID`/`tc.Seq` ignored — one `initialize` exchange per child, not even per-turn, opens and closes no turn; `turnMarkFor` answers `turnMarkNone` by construction). `ev.Models` looped into `[]protocol.ModelOption`, nil left nil (`ModelListPayload.MarshalJSON` owns nil→`[]`, the `ToolStart`/`BackgroundTaskRoster` rule); each row's six fields cross verbatim, including `EffortLevels` (nil→`[]` via `ModelOption.MarshalJSON`) and `TruncatedFields` (nil stays nil — that field is deliberately exempt from normalisation, so an absence reaches the wire as `null`, the opposite polarity from `EffortLevels` one field over). `DroppedModels` is carried from the decode, never recomputed from `len(models)` and never a constant. No re-cap, re-order or charset check of any field — the producer already bounds all three dimensions (`streamsup`'s `maxModelListEntries`/`maxModelResolved`/`maxModelEffortLevel*`), and `internal/relay`'s `validModel`/`validEffort` bound a phone-supplied *inbound* value, not this outbound report. No suppression branch — a zero-value `ModelList` maps, `ModelAnnounced`'s posture unchanged | true |
| `SlashCommandList` (#2001) | `TypeSlashCommandList` | `SlashCommandListPayload{tc.ConversationID, commands, ev.DroppedCommands}` (`tc.TurnID`/`tc.Seq` ignored — the same `initialize`-exchange, not-even-per-turn addressing as `ModelList`; `turnMarkFor` answers `turnMarkNone`). `ev.Commands` looped into `[]protocol.SlashCommand`, nil left nil (`SlashCommandListPayload.MarshalJSON` owns nil→`[]`, the `ModelList`/`BackgroundTaskRoster` rule); each row's five fields cross verbatim, including `Aliases` (nil→`[]` via `SlashCommand.MarshalJSON`) and `TruncatedFields` (nil stays nil — deliberately exempt from normalisation, so an absence reaches the wire as `null`, the opposite polarity from `Aliases` one field over). `DroppedCommands` is the decode's count **plus** whatever the frame cut below drops, never recomputed from `len(commands)` alone and never a constant. No re-cap, re-order or charset check of any dimension the producer already bounds (`streamsup`'s `maxSlashCommandName`/`maxSlashCommandDescription`/`maxSlashCommandArgumentHint`/`maxSlashCommandAlias`/`maxSlashCommandAliasCount`/`maxSlashCommandListEntries`), and `Name` is not an identifier (`__remote-workflow` is in the committed capture) so no charset assumption belongs here either. No suppression branch — a zero-value `SlashCommandList` maps, `ModelList`'s posture unchanged. **Frame-size bound (#2002):** the producer's content caps alone allow a payload several times the 65519-byte v2 envelope, and no *count* cap can close that — the only entry count whose worst case fits (51) is the committed capture's own size, so it would fire on claude's ordinary output. The arm instead walks `e.Commands`, marshals each row as the wire `protocol.SlashCommand` (not the turnevent one — its `MarshalJSON` normalisations are part of what actually crosses), and takes the tail-cut prefix that fits under `maxSlashCommandListBytes` (64000 B), `break`ing rather than skipping on the first row that would not, so a client sees a shortened menu rather than one with holes | true |
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
- **Pinning "entry order is preserved" needs a non-monotonic fixture, not just two
  distinguishable entries.** `ModelList`'s outbound row (#1848) uses two entries
  whose `ResolvedModel`/`Value`/`DisplayName` all happen to sort ascending; the
  row's own comment claims a reversal *or a re-sort* is caught, but an ascending
  canonicalising sort is a no-op against an already-ascending fixture — only the
  reversal is. Code review found this by running the sort mutant, not by reading
  the comment. Two entries can only ever be monotonic or reversed; three
  non-monotonic entries (e.g. Beta, Alpha, Gamma) are the minimum that catches a
  sort in either direction. Unfixed as of #1848 (a single SHOULD FIX, below the
  review's three-finding action threshold) — the fixture and its overclaiming
  comment still stand; correct both the next time this row is touched.
- **A row pinning a mutate-through mapper needs its `ev` and `wantPayload` built
  from two separate slice literals, not one shared between them.** If the
  expected payload is built by re-slicing the same backing array as the input
  event, an in-place sort or dedupe inside the mapper's loop mutates both sides
  together and `reflect.DeepEqual` stays green. `ModelList`'s rows (#1848) spell
  the same effort-level and truncated-field slices out twice on purpose. This is
  more than a test nicety here: `sessionModelHold` (#1840) retains the same
  `turnevent.ModelList` without copying and is read on a relay-leg goroutine, so a
  mapper that mutated through would be a data race in production, not merely a
  wrong test result. The same asymmetric-nil hazard applies to a byte-level wire
  test: `ModelOption.EffortLevels` (nil→`[]`) and `TruncatedFields` (nil stays
  `null`) sit on adjacent fields with opposite rules, and copying either row's
  `want`/`notWant` pair onto the other passes against exactly the allocating
  mapper the test exists to catch.
- **A "fresh outer slice" pin cannot be written by aliasing through the mapped
  element, when the source and destination element types differ.** `SlashCommandList`'s
  arm (#2001) considered a row that writes a whole element through
  `payload.Commands[0]` and asserts the event's row is unchanged — the shape
  `TestMapEventSlashCommandListDoesNotMutateTheEvent` was drafted around. It can
  never fail: `turnevent.SlashCommand` and `protocol.SlashCommand` are distinct
  types, so nothing lets `payload.Commands[0]` alias `ev.Commands[0]`'s memory in
  the first place, and a check that is green against every possible
  implementation is not a pin. Cut before merge; the read-only half (assert the
  event is `reflect.DeepEqual` to a pre-call snapshot after `MapEvent` runs) is
  the one that actually reddens on a mutant, since the loop reads `e.Commands`
  by value into a fresh local rather than writing back through it. Before
  drafting a mutate-through assertion for a new arm, check whether the source and
  destination element types could even alias — if they can't, the assertion
  belongs on the source side, not the destination.
- **A tail-cut's `break`-vs-`continue` choice needs a fixture with rows of
  different sizes, not just more rows than the budget allows.** `SlashCommandList`'s
  frame-size cut (#2002) walks `e.Commands` and must stop at the first row that
  would overflow `maxSlashCommandListBytes` rather than skip it and try smaller
  rows behind it — skipping would hole-punch the menu and make the retained set
  order-dependent. An over-budget fixture built from uniformly worst-case-sized
  rows cannot catch a `break`→`continue` mutant: dropping *any one* row frees the
  same number of bytes, so the kept count and total size are identical either
  way, and `go test -overlay` proved the whole package green against that mutant.
  The fixture that catches it is decorrelated — several worst-case rows followed
  by a few tiny ones — so a skipping implementation admits a small row behind the
  cut and a prefix assertion (kept rows are a strict prefix of the input, in
  order) names it. Order the assertions so the prefix check runs before any count
  precondition: a skipping implementation keeps *more* rows, and a count guard
  checked first reports that as a stale fixture rather than as the bug.
- **A wire-size budget's reserve margin should track what is actually left
  unbounded outside it, not mimic a sibling constant's margin by default.**
  `maxDeltaTextBytes` and `maxInputTotalRunes` both reserve against an
  essentially unbounded input field. `maxSlashCommandListBytes` (#2002) can
  afford a tighter reserve (~2.7x its measured worst-case slack, against
  sibling margins several times that) because everything else in the payload
  and envelope is already bounded elsewhere — the one exception is
  `conversation_id`, which nothing in this package caps. Pick the margin from
  what is genuinely still unbounded, not from the neighbouring constant's
  number.
- **Measure a mapped list's wire cost against the *wire* type, not the
  internal one, when the two have different `MarshalJSON` behaviour.** The
  frame-size cut (#2002) marshals each row as `protocol.SlashCommand`, not
  `turnevent.SlashCommand` — the wire type's nil-`Aliases`→`[]` normalisation
  and JSON key names are both inside the bytes that actually cross, and a
  measurement taken against the internal type would under-count every row
  with a nil `Aliases`.

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
  `DiffContent`→`Path`, `TerminalContent`→`"terminal <id>"` — each truncated at
  `maxResultSummaryRunes` (10000, #1680), not `maxSummaryLen`. The cap is inert
  for the Diff/Terminal arms (neither approaches it) and applies to all three
  anyway — one rule is cheaper to read and test than a per-arm exception. `is_error`
  does not change the bound: the flag is derived at the `MapEvent` call site and
  never reaches `resultSummary`, so a failed tool's result is truncated exactly as
  a successful one's is. The live inbound producer (`internal/streamsup`'s
  `toolResultContent`) only ever emits `TextContent` or `nil`; the Diff/Terminal
  arms are unreachable today but handled (kept deliberately minimal) until a
  producer (the ACP adapter #600, or a refinement) emits them.
- **`truncate(s, max)`** — returns `s` unchanged at ≤ `max` runes; otherwise cuts at
  `max` runes (`[]rune`, not bytes) and appends `"…"`. Rune-aware so multibyte text
  never splits mid-rune.

`const maxSummaryLen = 200` bounds the **input** précis to one line of ≤ 200
runes — a **phone-display** bound, not a wire constraint (the envelope cap is far
larger); tunable if the mobile view wants a different cap. It stopped bounding the
result précis in #1680: results are an order of magnitude bigger than inputs
(11379 measured local tool results, mean 2959 characters, median 721; only 22%
survived the 200-rune cap whole) and the producer applies no cap of its own, so
the result side needed its own **wire** constraint rather than a display one.

`const maxResultSummaryRunes = 10000` is that constraint — deliberately
`maxDeltaTextBytes`'s number (`cmd/pyry`, the other free-text field on a v2
envelope), so the two don't drift apart for no reason. Its doc comment carries
the full six-bytes-per-rune arithmetic (`encoding/json`'s `SetEscapeHTML`
default makes `'<'`/`'>'`/`'&'`/control bytes cost 6 wire bytes each, a multibyte
rune is emitted raw so it never gets worse) and a never-raise rule with two
reasons: the measured worst case (a `'<'`-filled 64525-rune result with hostile
64-rune identity fields) is 61363 B against the 65519-byte envelope cap — 93.7%,
~4.2 KB of headroom — and `tool_result` is control-class in `internal/eventring`,
preferentially retained up to `MaxEventsPerConversation` (1024) per conversation
holding the marshalled bytes, so raising the rune cap also multiplies the ring's
worst-case per-conversation footprint (~1.4 MiB → ~59 MiB at 10000). 16000 was
measured and rejected at 96309 B, 47% over the cap — exceeding it doesn't
truncate the frame, it **loses** it, and `tool_result` is never-droppable, so the
operator would see an empty row rather than a shortened one.

**A worst-case envelope measurement has to pick the worst case of its `bool`
fields too.** `TestToolResultPayload_FitV2EnvelopeCap` (`internal/turnbridge`,
not `internal/protocol` — see the `maxInputFields` lesson below on why a test
that can't call the unexported helper doesn't stand on the constant) drives the
real `resultSummary` and logs 61363 B, one byte over the spec's hand-computed
61362: `"is_error":false` costs one more wire byte than `true`, and the field is
never `omitempty`. The test's two mutants (`maxResultSummaryRunes` → 16000; the
`TextContent` arm returns `v.Text` uncapped) die at different rungs — the
uncapped arm is caught by the test's rune-count precondition before anything is
marshalled, while only the raised-cap mutant reaches the `< 65519` byte
assertion. That split means the precondition is load-bearing coverage, not a
courtesy guard: deleting it as "redundant" would leave the uncapped-arm mutant to
die on a byte-count failure instead of a legible message naming the arm.

`docs/protocol-mobile.md` § `tool_result` documents the wire shape (the
10000-rune cap, the `…` marker, that `is_error` does not change the bound, and
the six-bytes-per-rune arithmetic) and states the same **display string, not a
capability** hazard § `tool_use` states for `Input` — a tool result is raw
command output or file contents, so a `'<'`-dense result is the ordinary case,
not the contrived one.

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
