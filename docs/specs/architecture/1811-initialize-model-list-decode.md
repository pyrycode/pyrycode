# #1811 — decode the initialize response's model identifiers into a bounded daemon value

## Files to read first

Cited by SYMBOL, never by line — `make cite-guard` bans line citations in `//` comments at any
depth, with no range and no depth exemption, so the names below are also the shape the developer's
own comments must use.

- `internal/streamsup/parser.go` → `Parser.consumeLine`, its `case "control_response"` arm — the
  33-line doc block this ticket must REWRITE. It currently argues *for* reading nothing below the
  top-level `type`, and names the `subtype:"error"` NAK gap as accepted because discriminating it
  "would cost a decode target for the nested object". This ticket builds that decode target, so the
  block's stated reason expires with it. Extract: the reasons that SURVIVE (its own arm rather than
  an `ignoredLineTypes` member; `emitUnrecognized` unreachable BY MATCHING; type-only in the record).
- `internal/streamsup/parser.go` → `Parser.emitRateLimit` — the emitter shape to copy: a nested
  decode off the TOP-LEVEL bytes, a closed set of daemon-authored drop-reason keywords, the `bound`
  closure that accumulates `cut`, and the sequential-statements rule that fixes `TruncatedFields`
  order. Its rung-3 doctrine (an event with no evidence behind it is a claim the line did not make)
  is the argument for dropping an empty `models` array.
- `internal/streamsup/parser.go` → `Parser.emitModelAnnounced` — the `il.Model == ""` rung, whose
  formulation ("absent, present-but-empty, and a line carrying no such key all land here and are
  answered identically, which is what makes a plain string decode target sufficient") carries over
  verbatim to a plain `[]entry` decode target here. Also the standing rule that `err` is never
  logged, because `encoding/json` quotes the offending input bytes into its error text.
- `internal/streamsup/parser.go` → `truncateField` — the cut helper. `<=` boundary, so a field of
  exactly the limit is NOT truncated; a cut landing mid-rune deletes the partial rune, so a cut
  value can come out 1–3 bytes UNDER the limit, and a scrub removal is not reported as a truncation.
  Five of the six captured `description` values carry non-ASCII, so mid-rune is a live path here.
- `internal/streamsup/parser.go` → `maxModelField`, `maxRateLimitField`, `rateLimitDropMsg` — the
  precedents this ticket's three new constants copy: separate constants at the same number, ONE
  rationale paragraph with the siblings saying it applies verbatim, and one drop message carrying a
  closed reason set.
- `internal/streamsup/parser.go` → `maxTaskRosterEntries` — read for the transient-materialisation
  paragraph ("the cap is applied AFTER `json.Unmarshal`, so a hostile array is materialised in
  transient memory before it is shortened"). This ticket ships that transient WITHOUT the cap; § Security
  review states why that is bounded, and #1812 inherits the arithmetic.
- `internal/streamsup/initialize_capture_test.go` → `capturedInitializePayload`,
  `capturedInitialize`, `initCaptureArms`, `initCaptureArmNoRequest`, `initCaptureArmLabel` — #1810's
  committed reader. Do NOT write a second one. `capturedInitializePayload` fatals on the absent case;
  `capturedInitialize` returns a nil payload for `initCaptureArmNoRequest`, which is AC 3's committed
  absent fixture.
- `internal/turnevent/event.go` → `BackgroundTask` — the entry type deliberately NOT an `Event`,
  carrying no `isTurnEvent()` marker, and the `TruncatedFields` convention: the DAEMON's snake_case
  names, in DECLARATION order, nil rather than an empty non-nil slice when nothing was cut.
- `internal/turnevent/event.go` → `BackgroundTaskRoster`, `RateLimited`, `ModelAnnounced` — the
  container-Event shape and the per-field doc density this package holds.
- `internal/protocol/interactive.go` → `ModelOption`, `ModelListPayload` — #1704's wire row. Its
  three claude-authored strings are EXACTLY this ticket's decode target, its declaration order is
  the one to mirror, and its `SECURITY:` paragraph states the untrusted-text posture the daemon side
  must not weaken. `ModelListPayload`'s doc names the seam: *"ConversationID is present and unfilled
  by this ticket — the producer supplies it at mapping time"* — mapping time is `turnbridge`, which
  is why the daemon value must be a `turnevent.Event` and not parser-held session state.
- `internal/turnbridge/outbound.go` → `MapEvent`, its `turnevent.ModelAnnounced` arm and its
  `default` — the `default` is what drops this event until #1693. Read the `ModelAnnounced` arm for
  the "the producer bounded it at construction, so a second cap here would be a second place the
  limit is decided" rule; this ticket is the producer.
- `cmd/pyry/interactive_turn_v2.go` → `eventKind` — the one consumer call site. Its `RateLimited`
  and `ModelAnnounced` arms state why an arm exists at all: the OTHER call sites would otherwise log
  `"unknown"` for a variant the daemon does recognize.
- `cmd/pyry/stream_turn_busy.go` → `turnMarkFor` — read ONLY to confirm no edit is needed. Its opener
  set is a whitelist and its `default` returns `turnMarkNone`, which is the CORRECT answer for a
  non-turn-scoped variant. Confirmed at spec time; do not edit this file.
- `internal/e2e/realclaude/testdata/initialize_control_v2.1.239.json` — the authority. Six entries,
  three key sets, at `control_responses[0].response.response.models`. Reached through #1810's reader,
  never by opening the file.
- `docs/knowledge/features/set-permission-mode-inband-probe.md` § "The `control_response` received,
  verbatim" — the two committed sibling ack shapes, transcribed in § Discriminant below.
- `docs/knowledge/features/streamsup-package.md` § "Turn I/O — envelope write + stdout parser" — the
  package overview for this surface.
- `docs/knowledge/features/turnevent-package.md` — the variant inventory and its conventions.

## Context

`Parser.consumeLine`'s `case "control_response"` arm consumes every control response content-free
(#1500): it logs the top-level type at debug and reads nothing below it. That is right for an
interrupt ack, whose entire content is "done". It is wrong for the initialize response — the reply to
the `control_request` that `WriteInitialize` puts on the child's held-open stdin (#1689) — which
carries the models array this feature exists to publish.

This slice turns that array into a bounded, typed, daemon-internal value and stops. Nothing publishes
it: `turnbridge.MapEvent`'s `default` drops it until #1693, so no client can read a false zero in the
window. The entry cap and its drop count are #1812; the capability fields (`effort_levels`,
`supports_auto_mode`) are #1809.

**No ADR is warranted.** Every decision below is an application of a precedent already recorded in
this package (`emitRateLimit`'s rungs, `maxModelField`'s sibling-constant rule, `BackgroundTask`'s
non-Event entry shape). The one genuinely new thing — the shape discriminant — belongs in
`docs/knowledge/features/streamsup-package.md`, which the documentation phase owns.

**Sizing, stated rather than suppressed.** Measured against the shape-matched anchor, this ticket is
at or below a member that merged. The anchor for "array decode, entry type + Event variant, per-field
text caps, pinned against a committed capture" is #1381 (`BackgroundTaskRoster`) at 922 insertions:
`parser.go` +202, `parser_test.go` +618, `event.go` +97, `capture_test.go` +14 — and #1381 was itself
the `xs` child of a split and still landed there. #1811 is strictly LESS on two axes (no cardinality
cap, no `DroppedX` and no aggregate arithmetic; #1810's reader is already committed, so the +14 is
free) and strictly MORE on one (the arm's doc block must be rewritten rather than a `case` added to
an existing dispatch). Projection ≈ 295 production + ~470 test. That is over the pipeline's 400-line
total-work boundary and under the shape-matched analogue, and the two disagree because this package
runs ~1:1 doc prose to code — the same tension #1642 recorded. It is reported here so an operator can
see it; it did not move the call, and the call was not made by relitigating the boundary. What made
it un-relitigable is that **no seam remains that yields two compliant children**: cutting by bound
dimension is forbidden (one merge window of unbounded claude text), a corrections-only child makes
the arm's comment false in the other direction, and the only real seam — declare the types ‖ decode
them — leaves the decode child at ~650, still over. A ticket twice-split with no remaining seam is
not made smaller by splitting it again; the answer is to pay discovery here, which is what the
reading list and § Discriminant above and below are for.

## Design

### Where the value surfaces — the open question, closed

**It is a `turnevent` Event variant, not parser-held session state.** The deciding fact is in
`ModelListPayload`'s own doc: *"ConversationID is present and unfilled by this ticket — the producer
supplies it at mapping time, the seam every v2 interactive payload uses."* Mapping time is
`turnbridge.MapEvent`, whose input is a `turnevent.Event`. Session state would oblige #1693 to build
a wholly new parser→relay path beside the one every other interactive payload already uses, which is
a larger slice than the one this decision was trying to keep small. It is also what the ticket's own
scope boundary already assumes when it says `MapEvent`'s `default` drops the value until #1693 — a
`default` only drops what reaches it.

The `BackgroundTask` precedent still applies, and it applies to the ENTRY type: `ModelOption` carries
no `isTurnEvent()` marker, exactly as `BackgroundTask` carries none. The container is the Event.

Cost check against the ≤3 production-file budget: `internal/turnevent/event.go`,
`internal/streamsup/parser.go`, `cmd/pyry/interactive_turn_v2.go`. `cmd/pyry/stream_turn_busy.go`
needs **no edit** — verified at spec time: `turnMarkFor`'s opener set is a whitelist and its `default`
returns `turnMarkNone`, which is the correct answer for a variant that neither opens nor closes a
turn. Three files.

### `internal/turnevent` — the two new types

```go
// ModelOption is one entry of a ModelList: the list's element type, NOT an Event,
// so it carries no marker. Declaration order mirrors protocol.ModelOption's.
type ModelOption struct {
    ResolvedModel   string
    Value           string
    DisplayName     string
    TruncatedFields []string
}

// ModelList is claude's inventory of selectable models, reported once per
// initialize exchange. Not a turn boundary and not per-turn.
type ModelList struct {
    Models []ModelOption
}

func (ModelList) isTurnEvent() {}
```

Doc obligations, at this package's density — these are the arguments that must be WRITTEN, not the
prose itself:

- `ModelOption.ResolvedModel`: what `Value` resolves to right now, the concrete identifier; the field
  a consumer wanting a dated identifier wants. Taken VERBATIM per #1600's rule — no lowercasing, no
  alias expansion, no date-stamping, no family mapping.
- `ModelOption.Value`: the argument you pass, and NOT a dated identifier — an alias (`sonnet`), a
  bracketed variant (`claude-fable-5[1m]`), or `default`. A consumer cannot derive a family by
  splitting it on `-`. Point at `protocol.ModelOption.Value` for the round-trip gap
  (`internal/relay`'s `validModel` rejects the bracketed forms) rather than restating it; **do not
  widen `validModel`, here or as a drive-by** — its charset is #845's argv-injection defense.
- `ModelOption.DisplayName`: claude's human LABEL. Prose, not an identifier. Safe to render as inert
  text; the daemon bounds it and does not sanitize it, so it stays untrusted model-influenced text.
- `ModelOption.TruncatedFields`: the daemon's snake_case names in DECLARATION order —
  `"resolved_model"`, `"value"`, `"display_name"`. nil when nothing was cut, never an empty non-nil
  slice. `BackgroundTask.TruncatedFields` is the single source of the convention; cite it, do not
  restate it.
- `ModelList.Models`: in claude's own order, unchanged. State explicitly that the COUNT is not
  bounded in this slice and that #1812 owns the cap and the drop count — an undeclared bound reads as
  "no bound was needed", which is a different claim from "the bound is the next slice's".
- `ModelList` itself: why it exists as an Event at all (the mapping seam above), and that it is NOT a
  turn marker — `turnMarkFor`'s whitelist default already answers it correctly, by construction.

Deliberately absent, and the absence is the guarantee: **`description` and `supportsFastMode` are
decoded nowhere.** A field decoded here that nothing publishes is untrusted prose bounded, retained
and carried for nothing; absence from the decode target is a stronger guarantee than a test sweep —
`systemInitLine`'s doc makes the argument, cite it there. `description` is the longest string in the
capture (66 bytes) and the most tempting to carry; it has no named consumer, and `protocol.ModelOption`
carries neither field at all.

### `internal/streamsup` — the discriminant

**The discriminant is shape-based, and it is conjunctive.** Correlating the daemon's own `request_id`
is rejected: `Runner.nextControlID` mints the id inline at the call site and nothing retains it, so
correlation would cost new cross-object state between the writer and the parser, which today share no
link. A shape discriminant reads only what is already on the line.

The three committed sibling shapes it must NOT fire on, transcribed from
`set-permission-mode-inband-probe.md` and from the ticket:

| line | `response.subtype` | `response.response` | outcome |
| --- | --- | --- | --- |
| `set_permission_mode` success | `success` | `{"mode":"default"}` — an object, no `models` | ack |
| `set_permission_mode` NAK | `error` | absent; `response.error` is a string instead | nak |
| interrupt ack | `success` | absent or empty | ack |
| initialize success | `success` | the payload object, `models` a 6-element array | model list |

The gate is: **`subtype == "success"` AND a non-empty decoded `models` array.** Two halves, and each
is load-bearing on its own.

- The `models` half alone already excludes all three siblings, since none carries the key.
- The `subtype` half is what stops a payload being read out of a response that reported FAILURE. The
  arm's existing doc accepted the NAK gap because discriminating it "would cost a decode target for
  the nested object"; this ticket builds that decode target, so the cost is gone and the reason
  expires. It is one comparison against a daemon-authored constant.

Non-`success` covers `error` and anything else, including empty — the classification is total.

### `internal/streamsup` — the decode target

```go
// Decoded from the TOP-LEVEL line bytes, never from a nested field.
type controlResponseLine struct {
    Response struct {
        Subtype  string `json:"subtype"`
        Response struct {
            Models []modelOptionLine `json:"models"`
        } `json:"response"`
    } `json:"response"`
}

type modelOptionLine struct {
    ResolvedModel string `json:"resolvedModel"`
    Value         string `json:"value"`
    DisplayName   string `json:"displayName"`
}
```

Three properties the doc must state:

1. **The input is `line` — the top-level bytes.** `streamLine`'s doc states the property this
   preserves: control shapes are read from the top level only and nested content is never re-scanned,
   which is what stops a tool result whose text is literally `{"type":"result"}` from forging a turn
   boundary. Decoding this payload from anywhere else would let claude's own tool output announce a
   model inventory the daemon never asked for. The struct spells the full nesting path for exactly
   that reason — `emitRateLimit`'s `rl.Info.Status` is the same shape.
2. **A plain `[]modelOptionLine` is sufficient**, and no pointer-to-slice is needed to separate absent
   from null from `[]`. `emitModelAnnounced`'s `il.Model == ""` rung is the precedent and its
   formulation carries over verbatim: absent, null, present-but-empty, and a line carrying no such key
   all land in the same rung and are answered identically. Which is also the answer to "should an
   empty array emit an empty list?" — no. `emitRateLimit`'s rung 3 states why: a `ModelList` carrying
   zero entries names no model, so it cannot serve the purpose the variant exists for, and emitting it
   would be the daemon reporting an inventory it never observed. The safe failure direction here is
   the false negative, and the ticket's own scope note ("no client can read a false zero") points the
   same way.
3. **`models` present but NOT an array** — a number, an object, a string — fails
   `json.Unmarshal` for the whole struct, which lands in the undecodable rung below. No panic, no
   partial state. Same for a non-object `response` or `response.response`. This is the ONLY way the
   unmarshal here can fail: `consumeLine` already unmarshalled the line into `streamLine`, so by the
   time the arm runs the bytes are known-valid JSON and only a TYPE mismatch remains.

### `internal/streamsup` — the caps

Three constants, all `256`, each named, per the ticket's rule that a matching number still earns its
own constant (`maxModelField` / `maxRateLimitField` are both 256 and deliberately separate).

| constant | field | longest observed | multiple |
| --- | --- | --- | --- |
| `maxModelResolved` | `resolvedModel` | 25 (`claude-haiku-4-5-20251001`) | ~10x |
| `maxModelValue` | `value` | 18 (`claude-fable-5[1m]`) | ~14x |
| `maxModelDisplayName` | `displayName` | 21 (`Default (recommended)`) | ~12x |

`maxModelResolved` carries the ONE rationale paragraph. The other two say the paragraph applies
verbatim and add only their single distinguishing sentence — `value` is not a dated identifier and
includes bracketed forms; `displayName` is prose rather than an identifier and so has the weakest
claim to a bounded natural length, which is why it does not get a SMALLER cap than the two it sits
beside. Three transcribed copies of the same reasoning is the shape that pushes this slice past its
size; `maxModelField`'s doc is the precedent for stating one argument once.

**The envelope arithmetic is deliberately PARTIAL, and the gap is named rather than left implicit.**
Per entry the worst case is `maxModelResolved + maxModelValue + maxModelDisplayName` = 768 bytes.
The AGGREGATE is a function of a count claude chooses and is NOT bounded in this slice — that is
#1812's cardinality cap. `maxTaskRosterEntries`' doctrine is the statement to cite: a per-entry text
cap alone leaves an aggregate's total size a function of a number claude chooses. Write the 768 into
`maxModelResolved`'s doc so #1812 inherits the multiplicand rather than re-deriving it, and note the
constraint it must satisfy: the observed list is SIX entries, so #1812's cap cannot be
`maxTaskRosterEntries`' 8 by analogy alone without checking that 6 fits under it.

### `internal/streamsup` — the emitter and the log

One method, `Parser.emitModelList(line []byte)`, called from the widened arm. It needs no bool
return: matching `case "control_response"` IS consuming the line, exactly as `emitRateLimit`'s doc
argues for its own type, and unlike the `emitSystemSubtype` family there is no "did you handle it?"
to report back.

One Debug record per `control_response`, carrying a daemon-authored `reason` from a CLOSED set and a
count — `rateLimitDropMsg`'s shape, which gives the tests one string to filter on:

```go
const (
    controlResponseMsg = "streamsup: consuming solicited control_response"

    controlResponseNAK         = "nak"          // subtype was not success
    controlResponseAck         = "ack"          // success, no model list on the line
    controlResponseUndecodable = "undecodable"  // the nested shape did not decode
    controlResponseModelList   = "model_list"   // one ModelList emitted
)
```

Attributes: `"type"` (already `"control_response"` by the match), `"reason"`, and `"models"` — the
entry count, `0` on the three non-emitting rungs. **Nothing else.** No `value`, no `resolvedModel`,
no `displayName`, no `err`, no `request_id`. The `err` exclusion is the sharpest one and must be
stated at the drop site, not assumed: `encoding/json` quotes the offending input bytes into its error
text, so `"err", err` would route claude's strings into the daemon log through a channel no
per-attribute check can see. `emitModelAnnounced`'s undecodable arm already says this; cite it.

**Deliberate, and to be flagged for review rather than discovered by it:** the message string is
unchanged and every `control_response` still gets exactly one record, but an ack's record now gains
`reason` and `models`. AC 1's "exactly as today" is a claim about BEHAVIOUR — consume, emit nothing —
and that is byte-for-byte unchanged for every non-initialize response. The added attributes are a
daemon keyword and an integer; neither is claude-derived content, so the content-free discipline is
intact. The side benefit is the gap the arm's own doc records as accepted: a NAK is no longer
indistinguishable from a success in the log.

### `cmd/pyry` — the one consumer call site

`eventKind` gains a `case turnevent.ModelList: return "model_list"` arm. The variant NAME only — the
`ModelAnnounced` and `RateLimited` arms state the rule and this arm cites it: `Value`,
`ResolvedModel` and `DisplayName` are precisely the fields #833's posture ("model / effort / YOLO
values are NEVER logged at any level") exists to keep out of a log, and none of them is returned.
The arm exists for the OTHER call sites (`acp_turn_stream.go`, `stream_turn_busy.go`,
`stream_turn_drain.go`), which would otherwise log `"unknown"` for a variant the daemon recognizes.

### Data flow

```
claude stdout line
  → Parser.consumeLine            (top-level json.Unmarshal into streamLine)
  → case "control_response"
  → Parser.emitModelList          (top-level json.Unmarshal into controlResponseLine)
      ├─ decode error ──────────► Debug{reason: undecodable, models: 0}   → nothing
      ├─ subtype != "success" ──► Debug{reason: nak,         models: 0}   → nothing
      ├─ len(Models) == 0 ──────► Debug{reason: ack,         models: 0}   → nothing
      └─ otherwise ─────────────► bound each of 3 fields per entry
                                  Debug{reason: model_list, models: N}
                                  p.emit(turnevent.ModelList{...})
                                    → drain → turnbridge.MapEvent default → DROPPED (until #1693)
```

## Concurrency model

**No new goroutines, no new shared state, no new locks.** `emitModelList` is a pure function of one
line plus `p.emit` and `p.log`, called only from `consumeLine`, which already runs on the parser's
single stdout-reading goroutine. It adds no field to `Parser` — in particular it does NOT touch
`p.thinkingSinceEmit`, the parser's only accumulator, whose sole boundary stays the `result` arm.
Giving one piece of state two boundaries to keep agreeing is the cost having a single boundary
avoids, and this line is not a turn boundary.

The emitted `ModelList` and every `ModelOption` in it are constructed fresh per line and handed to
`p.emit` by value; the backing array is never retained by the parser, so no consumer can mutate
anything the parser will read again. Shutdown is unchanged: the event rides the existing fan-in lane
and is dropped by `MapEvent`'s `default`, so a shutdown mid-decode loses at most this one report,
which is re-derivable from the next initialize exchange.

## Error handling

Four rungs, total over the input, none of which can panic and none of which produces an
`Unrecognized` or a stream failure:

| condition | record | event |
| --- | --- | --- |
| `controlResponseLine` unmarshal fails (type mismatch anywhere on the path) | `reason: undecodable` | none |
| `response.subtype != "success"` | `reason: nak` | none |
| `models` absent, null, `[]`, or decoded empty | `reason: ack` | none |
| `subtype == "success"` and `len(models) > 0` | `reason: model_list`, `models: N` | one `ModelList` |

Why never an `Unrecognized`: keeping `control_response` claimed by a `case` arm is what makes "no
solicited control response reaches the unrecognized lane" structural — and here that guarantee is
the STRONG form, by MATCHING rather than by `ignoredLineTypes` membership. Breaking it would put a
malformed payload in front of the live zero-unrecognized gate
(`internal/e2e/realclaude`'s `drainForCompletedTurn`) for a line the daemon does in fact recognise.
The `Unrecognized` frame's whole value is meaning "claude started emitting something NEW"; spending
it on a shape we already know is what #1500's arm exists to prevent.

A per-entry field is never validated beyond its cap. An entry whose `value` is empty, whose
`resolvedModel` is missing, or which carries no keys at all still becomes an entry: absence is
claude's to choose, and `emitBackgroundTaskStarted`'s doc is the standing answer — the field lands
empty rather than inventing a validation rule. The two four-key entries in the capture are the
committed proof that a partial key set is claude's normal output, not a malformation. The gate that
suppresses the event lives at the LIST level (empty list → no event), not the entry level.

## Testing strategy

All of it runs inside `make check`. The `e2e_realclaude` build tag governs that package's Go FILES,
not its testdata, so a decode proven against the committed bytes needs no live claude and no
credentials — which is #1810's whole reason for existing.

Scenarios, not test bodies. Table-driven, stdlib `testing`, `-race`, no mocking framework.

**The capture pin (AC 4).** Read the payload with `capturedInitializePayload`, wrap it back into a
synthetic top-level `control_response` line, run it through `consumeLine`, and assert the emitted
`ModelList`:

- Exactly six entries, in claude's own order.
- Each entry's three strings byte-for-byte equal to the capture's, read from the capture rather than
  transcribed into the test as literals — a transcribed expectation pins the transcription, not the
  decode.
- Every entry's `TruncatedFields` nil: no captured field is near 256 bytes, so the capture arm must
  prove the untruncated path.
- All three key sets exercised — the 8-key entries, the 9-key `opus` (its extra `supportsFastMode` is
  ignored, which is the assertion), and the two 4-key entries, whose missing capability keys must
  produce a complete `ModelOption` rather than a drop.
- Run it across all three RESPONDING arms via `initCaptureArms` / `initCaptureArmLabel`, so a
  re-capture that changes one arm reddens with the arm named.

**The absent case (AC 3).** `capturedInitialize(t, initCaptureArmNoRequest)` returns a nil payload.
Assert the reader gives nil, and assert separately that a `control_response` with no inner `response`
emits nothing — a committed fixture for the absent case, not a synthetic one.

**Reject branches (AC 3), synthetic lines, one row each:** undecodable (`"models": 5`,
`"models": {}`, `"response": "text"`); NAK (`subtype:"error"` with an `error` string, and a NAK that
also carries a well-formed `models` array — the row that proves the `subtype` half of the gate is
load-bearing rather than decorative); ack (`models` absent, `models: null`, `models: []`, and the two
verbatim `set_permission_mode` shapes from the probe doc). Every row asserts BOTH no event AND no
`Unrecognized`.

**Caps (AC 2).** Synthesize over-cap entries rather than hand-writing 256-byte literals:

- Each field cut independently → `TruncatedFields` names exactly that one field.
- Two fields cut on one entry → both named, in DECLARATION order (`resolved_model`, `value`,
  `display_name`), which is the assertion that pins the sequential-statements rule.
- Nothing cut → `TruncatedFields` is **nil**, not an empty non-nil slice. Assert nil-ness explicitly;
  `reflect.DeepEqual` against `[]string{}` and against `nil` disagree, and only one of them is the
  contract.
- Exactly-256 bytes → NOT truncated (`truncateField`'s `<=` boundary).
- A multi-byte rune straddling the cut → the value comes out 1–3 bytes UNDER 256 with the partial
  rune deleted, and `TruncatedFields` still names the field. The capture's five non-ASCII
  descriptions are why this is a live path and not a corner case.
- Cuts on entry 2 do not appear on entry 1's report — per-entry accumulation, not a shared slice.

**Log discipline (AC 5).** Capture the parser's `slog` output for each of the four rungs and assert
that no record's attributes contain any of the entry strings, using a sentinel value planted in the
synthetic line so the assertion cannot pass vacuously. Assert the `reason` value is one of the four
constants and that `models` matches the emitted entry count. `TestParser_IgnoredLineTypesStaySilent`
is the shape to follow.

**`eventKind`.** One row asserting `"model_list"`, in the existing table. `turnMarkFor` needs no test
change; if a row is added anyway it must assert `turnMarkNone`.

**What NOT to test**, so review does not read the absence as a gap: there is no wire round-trip, no
`turnbridge` mapping test, and no `protocol` test in this slice. `MapEvent`'s `default` dropping the
event is the CURRENT correct behaviour and pinning it would have to be un-pinned by #1693.

## Open questions

1. **Does #1812's cardinality cap belong above 6?** The observed list is six entries, so
   `maxTaskRosterEntries`' 8 is only barely above it and leaves no headroom for a claude that adds a
   model family. Not decided here — flagged so #1812 does not reach for 8 by analogy. The
   multiplicand it needs (768 bytes/entry) is written into `maxModelResolved`'s doc by this ticket.
2. **Does a re-capture at a claude version past 2.1.239 change the key sets?** `initCaptureVersion`
   is a hard-coded tripwire and `TestInitCaptureArms_CoverTheCaptureDirectory` fails loudly on a
   re-capture, so this resolves itself; named only so the developer recognises that failure as
   designed rather than as broken test infrastructure.
3. **Should `ModelList` carry claude's `session_id`?** No, and the decode target above omits it —
   `BackgroundTaskStartedPayload`'s reason: claude's session identity is not the daemon's
   conversation identity, and the conversation id is supplied at mapping time by #1693. Recorded
   because it is the field a reader of the payload's fourteen top-level keys will ask about first.

## Security review

**Verdict:** PASS

**Findings:**

- **[Trust boundaries] No findings.** The boundary is explicit and singular: `Parser.emitModelList`
  is the only place these bytes cross from claude's stdout into daemon state, and it is reached only
  from `consumeLine`'s `case "control_response"`. The decode reads the TOP-LEVEL line bytes through
  `controlResponseLine`, never a nested field, which preserves `streamLine`'s stated property that
  control shapes are read from the top level only — so claude's own tool output cannot forge a model
  inventory the way it could if the payload were re-scanned out of a nested string. Downstream
  holders are signalled by type and by doc: `turnevent.ModelOption`'s three strings each carry the
  untrusted-prose warning, and `protocol.ModelOption`'s `SECURITY:` paragraph — which this slice
  feeds — already states that the daemon bounds but does not sanitize, and that the render boundary
  owing the sanitization is the client's. This spec weakens neither.
- **[Discriminant provenance] SHOULD FIX — the shape gate proves SHAPE, not PROVENANCE, and the
  developer must write that limit down rather than let a reader infer correlation.** A shape
  discriminant answers "does this line carry a success-subtype model list", not "is this the reply to
  the initialize request THIS daemon sent". claude authors the inner `response` object on every
  control response, so a future claude that put a `models` array inside a `set_permission_mode` ack
  would have that ack decoded as a model list. The consequence is bounded and does not reach MUST
  FIX: the value would still be claude's own inventory claim about itself, bounded by the same three
  caps, retained by nothing and published to nobody, and the daemon takes no action on it — there is
  no privilege, no path and no command derived from it. The alternative, correlating
  `Runner.nextControlID`'s minted id, buys real provenance but costs cross-object state between the
  writer and the parser, which today share no link at all (`WriteInitialize(r.Stdin(),
  r.nextControlID())` discards the id inline, exactly as its two sibling writers discard theirs).
  That trade is worth revisiting at #1693, when the value first reaches a client and provenance
  starts to matter; it is not worth new parser state in a slice nothing publishes. State the limit in
  `emitModelList`'s doc — "this recognises a shape, not a correlated reply" — so #1693 inherits the
  open question rather than inheriting a false sense of correlation.
- **[Tokens, secrets, credentials] Not applicable — by decode target, not by inspection.** The
  initialize payload carries fourteen top-level keys including `account` and `pid`; the decode target
  reads exactly one, `models`, and within each entry exactly three strings. `account` is never
  decoded, never bounded, never retained and never logged, because it is absent from the struct.
  That absence is the guarantee — `systemInitLine`'s argument — and it is stronger than any sweep a
  test could run.
- **[Log injection / disclosure] MUST-NOT-log enumerated and enforced by construction.** Every rung
  logs exactly one record whose only variable attributes are a `reason` from a four-member closed
  constant set and an integer count. `value`, `resolvedModel`, `displayName`, `request_id` and the
  unmarshal `err` are all excluded, and the `err` exclusion is called out at the drop site because
  `encoding/json` quotes offending input bytes into its error text — a channel no per-attribute check
  can see, and precisely what #833's "model values are NEVER logged at any level" posture exists to
  keep out. `eventKind`'s new arm returns the variant NAME only, for the same reason its
  `ModelAnnounced` neighbour does. The one log-shape CHANGE (an ack's record gains `reason` and
  `models`) adds a daemon keyword and an integer, no claude-derived text.
- **[Resource exhaustion] SHOULD FIX — bounded, transient, and explicitly scheduled.** This slice
  bounds per-entry TEXT (3 × 256 = 768 bytes) but not entry COUNT, which is #1812. The exposure is a
  transient allocation: `defaultMaxParseBuf` caps one line at 4 MiB before the decoder sees it, and
  the densest legal entry is `{}` — about 3 input bytes for a ~72-byte `ModelOption` — so the
  amplification is roughly 24x, order 100 MB for one pathological line. Three facts bound it. It is
  TRANSIENT, not retained: the event is emitted, `turnbridge.MapEvent`'s `default` drops it, and it is
  freed — one such value in flight at a time, which is the same accepted transient
  `maxTaskRosterEntries`' doc already reasons about for the roster. It never becomes RETENTION,
  because nothing puts it in the eventring or on the wire until #1693, which lands after #1812. And
  the producer is the daemon's own subprocess running with the user's credentials, not a remote
  party; a claude that could send this could do worse directly. **The developer must not add a
  cardinality cap** — that is #1812's deliverable and splitting a bound across two slices in the
  other direction is what the scope boundary forbids — but must WRITE the 768-byte multiplicand into
  `maxModelResolved`'s doc so #1812 inherits the arithmetic instead of re-deriving it.
- **[Input validation] No findings.** The gate is conjunctive (`subtype == "success"` AND a non-empty
  `models` array) and the classification is total over the input — every line lands in exactly one of
  four rungs, and a type mismatch anywhere on the nesting path fails the unmarshal into the
  undecodable rung rather than panicking. The `subtype` half specifically refuses to read a payload
  out of a response that reported FAILURE, closing the gap `case "control_response"`'s current doc
  records as accepted-because-expensive. A `models` key that is a number, an object or a string is
  the undecodable rung, which AC 3 requires and which the test table covers with its own rows.
- **[Subprocess execution] No findings, and one active refusal.** Nothing decoded here is ever passed
  to `exec.Command`. `ModelOption.Value` is the one field a reader may be tempted to treat as
  round-trippable; it is not, `internal/relay`'s `validModel` rejects the bracketed forms
  (`claude-fable-5[1m]`), and **`validModel` must not be widened by this ticket, including as a
  drive-by**. Its charset is #845's argv-injection defense and admitting `[` and `]` is a security
  decision about an untrusted phone-supplied string, not a typo fix; it belongs to whichever slice
  first makes a client send one (#1693), with its own review. Publishing a value does not make it
  trusted — it is still claude's text arriving on an inbound path, re-validated at the boundary.
- **[File operations] Not applicable.** This slice opens no file on the production path. The capture
  is read only by tests, through #1810's `capturedInitialize`, which mints its own path from package
  constants and accepts an ARM SELECTOR from a closed set rather than a caller-supplied path — so no
  test can put an unchecked file behind those assertions.
- **[Cryptographic primitives] Not applicable.** No randomness, no keys, no comparison against a
  secret. The one string comparison (`subtype == "success"`) is against a daemon-authored constant,
  not a secret, so constant-time comparison is not the applicable tool.
- **[Network & I/O] Not applicable to this slice.** No socket is read or written. The v2
  application-envelope cap (65519 bytes) is the constraint the caps are sized against, but nothing
  here crosses it — #1693 owns that, and the per-entry 768-byte figure is recorded for it.
- **[Concurrency] No findings.** No new goroutine, no new lock, no new `Parser` field, no shared
  mutable state. `emitModelList` runs on the parser's existing single stdout-reading goroutine and
  hands a freshly constructed value to `p.emit` by value, so there is no backing array a consumer and
  the parser both hold. No check-then-mutate, so no TOCTOU. The parser's only accumulator keeps its
  single boundary at the `result` arm, untouched.
- **[Threat model alignment] No findings.** `docs/protocol-mobile.md` § Security model governs the
  phone-facing surface; this slice reaches none of it, and the fields it produces are already
  declared there through `protocol.ModelOption`, whose posture this design implements rather than
  amends. The one deferred item is the cardinality bound, named above and owned by #1812.

**Reviewer:** architect (self-review per the security-review checklist)
**Date:** 2026-08-26
