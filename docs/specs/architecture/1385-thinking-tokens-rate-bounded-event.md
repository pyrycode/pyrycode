# #1385 — Map claude's `system/thinking_tokens` subtype to a rate-bounded daemon event

**Size:** S (confirmed, not overridden). **Labels:** `security-sensitive`.

Fourth arm on the dispatch #1380 built, after #1382 (`task_updated`) and #1381 (`background_tasks_changed`). One new `turnevent` variant, one new bound constant, one new decode target, and — this is what separates it from the three siblings — **the parser's first piece of cross-line state**.

It also has a consequence the ticket body does not name: adding the subtype to the realclaude mirror's mapped set un-filters ~10 lines/turn from the cross-runner **sequence** comparison and re-opens #1218's RED on the live pre-ship gate. See § 6. That is the highest-risk part of this ticket; do not skip it.

---

## Files to read first

This is the turn-1 data load. Everything the design references is here with a line range and what to take from it.

| Path | What to extract |
|---|---|
| `internal/streamsup/parser.go:284-327` | `Parser`'s doc and struct. The **turn-stateless** paragraph (`:291-295`) is the claim this ticket amends, and the single-writer invariant (`:297-303`) is what licenses an unlocked field. |
| `internal/streamsup/parser.go:488-536` | `consumeLine`. Two edits land here: the `result` arm (`:505-510`) gains the accumulator reset, and the drop comment (`:521-528`) is corrected (AC4). |
| `internal/streamsup/parser.go:538-564` | `emitSystemSubtype` — the ONE enumeration site of the mapped set. This ticket adds exactly one `case`. |
| `internal/streamsup/parser.go:566-623` | `emitBackgroundTaskStarted` — the emit function to mirror structurally: top-level-`line` decode argument, undecodable path's content-free `Debug` + `return true`. **Do not** mirror the `bound`/`truncateField` apparatus; two integers need no caps. |
| `internal/streamsup/parser.go:366-381` | `systemTaskStartedLine` — the per-subtype decode-target doc shape, including "the field set is exactly what the capture shows and nothing invented" and the two named drops (`uuid`, `session_id`). AC5 is discharged by mirroring this. |
| `internal/streamsup/parser.go:38-66` | `maxTaskFieldID` / `maxTaskDescription` — the **comment style** for a bound: observed number, multiple, the argument for the multiple. `minThinkingTokensPerEvent`'s comment matches this shape, but its arithmetic is against the burst data, not the envelope (§ 3). |
| `internal/streamsup/parser.go:179-250` | `ignoredLineTypes` and its doc. The list itself is **unchanged**. The `CORRECTED 2026-08-07 (#1380)` block at `:203-209` carries a count this ticket falsifies — see § Comment obligations. The 2026-07-27 measurement block at `:186-195` stays. |
| `internal/streamsup/parser.go:686-715` | `emitBackgroundTaskRoster`'s doc. Its last paragraph (`:711-715`) makes "the parser holds no cross-line state" the *enforcement mechanism* for refusing to synthesize a task-finish event. This ticket falsifies the literal claim and must re-scope it without weakening the refusal. |
| `internal/turnevent/event.go:23-32` | The `Event` interface doc — it lists the variants and needs the new name. |
| `internal/turnevent/event.go:129-198` | `BackgroundTaskUpdated` — the doc structure to mirror: name-is-the-daemon's rationale, the deliberate-drops list, the per-field contract paragraphs. Nearest sibling. |
| `internal/turnevent/event.go:379-404` | The two marker blocks (`isTurnEvent()` + `_ Event = …`). Two lines each. |
| `internal/streamsup/capture_test.go` (whole file, 85 lines) | `capturedSystemLine` — the provenance-at-the-reader rule (`is_capture`, `payload_encoding`, exactly-one). This ticket extracts a **plural** reader from it; see § 5. |
| `internal/streamsup/parser_test.go:140-150` | The main table's `system thinking_tokens tolerated` row. Its line uses `"tokens":42` — not the capture's key — so it stays silent under the new arm, but for a new reason. Moves; see § Comment obligations. |
| `internal/streamsup/parser_test.go:499-542` | `TestParser_IgnoredLineTypesStaySilent` — the silence table. Its `thinking_tokens` row moves out (family precedent), and its doc carries two sentences this ticket falsifies. |
| `internal/streamsup/parser_test.go:544-565` | `taskStartedCapCheat` — the **literal-fixture rule**: a fixture built from the constant it validates asserts nothing. AC3 is this rule applied to a count instead of a byte length. |
| `internal/streamsup/parser_test.go:589-620` | `taskStartedEvent` — the one-line-in, one-event-out extractor shape. The new tests need a multi-line variant (§ 5). |
| `internal/e2e/realclaude/ptyrunner_byte_equivalence_test.go:69-123` | `expectedStreamRunnerOnly` / `expectedPtyRunnerOnly` — the one-sided tables, **keyed by top-level type**, which also govern the SET check via `additiveDriftViolations`. § 6 turns on why `system` cannot go in either. |
| `internal/e2e/realclaude/ptyrunner_byte_equivalence_test.go:131-207` | `parserIgnoredTypes`, `parserMappedSubtypes`, `parserDropsShape`. The mapped set gains `thinking_tokens`. Read `:159-170` — it is the argument § 6 has to work around. |
| `internal/e2e/realclaude/ptyrunner_byte_equivalence_test.go:246-307` | `envelopeShape`, `shapeFilterDrops`, `extractShapes`. **`shapeFilterDrops` is where § 6's fix lands.** Note it consults the one-sided tables by type alone and `parserDropsShape` with the subtype. |
| `internal/e2e/realclaude/ptyrunner_byte_equivalence_test.go:977-1023` | `TestExtractShapes_FiltersParserIgnoredTypes` — its fixture already carries two `thinking_tokens` lines and its `want` excludes them. With § 6's fix this test stays **green and unchanged**; without it, RED. |
| `internal/e2e/realclaude/ptyrunner_byte_equivalence_test.go:1049-1074` | `parserIgnoredTypeFixtures` and the bare-fixture convention comment. One fixture line changes and the comment gains the exception. |
| `internal/e2e/realclaude/ptyrunner_byte_equivalence_test.go:1095-1183` | `TestParserIgnoredTypesMatchesStreamsupParser` + `parseOne`. **`parseOne` writes ONE line to a fresh parser** — which is why the mapped fixture must cross the bound on its own. |
| `internal/e2e/realclaude/testdata/dropped_lines_v2.1.220.json` | The capture. 33 `thinking_tokens` records at stream indices 2–10, 17–24, 27–32, 36–45. Read it through the plural reader, never by hand-copying a payload. |
| `docs/knowledge/codebase/1380.md`, `1382.md` | The two predecessors' decisions: why `uuid`/`session_id` are dropped, why the drop proof is a reflection sweep, why the undecodable path is content-free. |

*Codegraph note:* `codegraph_context` returned only `emitSystemSubtype` for this task — #1381/#1382's symbols merged within the last two days and are not in the index yet. The table above was completed by reading. Do not conclude from a thin codegraph result that those symbols are absent.

---

## Context

`internal/streamsup`'s parser maps three `system` subtypes and drops the rest. `thinking_tokens` is the highest-rate of the dropped ones and the only mid-turn proof of life claude puts on this surface: during a long assistant turn nothing else crosses stdout, so a client showing "thinking" cannot separate a slow answer from a wedge.

Three things make this ticket structurally different from its three siblings, and the design is mostly about those three:

1. **The payload is two integers, not capped strings.** None of `truncateField` / `TruncatedFields` / `max*` byte-cap apparatus is owed. A numeric field cannot blow the envelope, and a value too large to decode into `int` fails the whole decode and takes the existing content-free drop.
2. **The mapping is rate-bounded, so it is not a pure function of one line.** The parser needs cross-line state for the first time. That is the one genuinely new architectural property here (§ 4).
3. **claude's counter resets mid-turn.** `estimated_tokens` is cumulative within one *inference request*, not within a turn. The captured turn holds four bursts, each restarting near zero. Any rule keyed on the cumulative counter's high-water mark goes silent for a whole burst; the accumulator design (§ 2) is immune by construction because it only ever adds per-line deltas.

Nothing a client receives changes: `turnbridge.MapEvent`'s `default:` arm (`outbound.go:129`) returns `ok == false` for a variant it does not name, so the event stops at the daemon boundary. Carrying it to a phone is #1386, which this ticket blocks.

### The measurement this design is built on

Re-derived from `dropped_lines_v2.1.220.json` (claude 2.1.220, `is_capture: true`, `terminated_on: result` — one turn). All 33 records carry the identical key set `{type, subtype, estimated_tokens, estimated_tokens_delta, uuid, session_id}`; 166–169 bytes on the wire.

| burst | stream indices | lines | `estimated_tokens` | Σ delta |
|---|---|---|---|---|
| 1 | 2–10 | 9 | 5 → 184 | 184 |
| 2 | 17–24 | 8 | 4 → 167 | 167 |
| 3 | 27–32 | 6 | 3 → 126 | 126 |
| 4 | 36–45 | 10 | 1 → 197 | 197 |

Within each burst the deltas sum exactly to the final cumulative value, and the delta on a burst's first line equals that line's cumulative value. So summing `estimated_tokens_delta` is equivalent to tracking cumulative progress *and* survives the reset — which is the whole reason the rule keys on the delta.

Turn total: **674** tokens of delta across 33 lines, ~20/line.

---

## Design

### 1. The event variant — `turnevent.ThinkingProgress`

New variant of the sealed `Event` sum type in `internal/turnevent/event.go`, placed after `BackgroundTaskRoster` and before the internal-only status peers.

```go
type ThinkingProgress struct {
    EstimatedTokens      int
    EstimatedTokensDelta int
}
```

Two fields, both from the emitting line, both claude's own measurement (AC5). No `TruncatedFields`: nothing is cut, so a report of cuts would be a permanently-nil field claiming a bound that does not exist.

Contract per field, to be carried in the doc:

- **`EstimatedTokens`** — claude's estimate of tokens spent thinking, as of the emitting line. **Cumulative within one inference request, NOT within a turn**: it restarts near zero at each burst boundary, so a consumer must treat it as a progress reading, not a turn total, and must not assume it is monotonic. This is measured, not speculative — the table above shows four restarts in one turn.
- **`EstimatedTokensDelta`** — claude's per-line increment, as it appears on the emitting line. **These do not sum to the turn's total**: the rate bound drops most lines, so a consumer summing the deltas it receives undercounts by whatever the dropped lines carried (674 → 232 on the capture). It is a rate reading, not an accumulator input.

Both hazards are properties a consumer will get wrong if the doc does not say so, and #1386 is the ticket that would get them wrong first.

The doc must also carry, mirroring `BackgroundTaskUpdated`'s structure:

- The name-is-the-daemon's rationale. "Thinking" collides with `ThoughtChunk`, which carries the *content* of reasoning; this variant carries none — only that reasoning is happening and roughly how much. Say so explicitly, because the two names sit four lines apart in the same file.
- The two deliberate key drops (`uuid`, `session_id`) with #1380's reasons, verbatim in substance.
- **The rate bound, stated at the type** (AC4's "where a reader of the mapping finds it"): the producer emits at most one of these per `minThinkingTokensPerEvent` tokens of accumulated delta, so the event stream carries strictly fewer of them than claude emits lines, and **a consumer must not infer the absence of thinking from the absence of an event within any particular window**.
- **The stall note.** `turnevent.Stall` has exactly one producer — `internal/turnbridge/mapper.go:35`, from `tuidriver.EventKindStallDetected`, on the PTY surface, which never sees this parser. `streamsup.Watchdog` reads its own copy of raw stdout (`watchdog.go:55-58`, `:258`) and never reads parser events; it already counts every complete line as activity, `thinking_tokens` included. So this variant neither masks nor triggers stall detection today. One sentence in the doc, so a future wiring slice does not re-open the question the parent ticket raised and this one closed.

Plus `func (ThinkingProgress) isTurnEvent() {}`, `_ Event = ThinkingProgress{}`, and the name added to the `Event` interface doc at `event.go:23-32`.

### 2. The rule — accumulate deltas, emit on crossing, reset

One rule, four lines of behaviour, stated as the contract:

```
per line:  d := estimated_tokens_delta
           if d <= 0            -> consume, no event            (guard)
           if d >= bound - acc  -> emit(line's two values); acc = 0
           else                 -> acc += d
per result line:                -> acc = 0
```

**Write the crossing test as `d >= bound - acc`, not as `acc += d; if acc >= bound`.** The two are arithmetically equivalent for every value that fits, and the second overflows. `estimated_tokens_delta: 9223372036854775807` decodes into `int` cleanly, and `acc + d` on a nonzero residual wraps to a large negative — the comparison then reads false, the accumulator lands far below zero, and the turn's liveness signal is **silently dead until the next `result`**. In the subtracted form both operands of `bound - acc` are in `[1, bound]` by the invariant below, so no expression in the rule can overflow at all. This is not a clamp bolted on; it is the same rule written so the failure is unrepresentable. Say that in the comment, because the obvious refactor back to the additive form reintroduces it.

Four properties follow, and each is worth stating in the code because each is load-bearing:

**AC1 (no silent burst) holds by construction, not by tuning.** The accumulator only ever grows within a turn, so a burst whose own delta total reaches `bound` emits at least once *regardless of the residual it inherits* — a residual can only bring the emit forward. Since `bound = 64` and the smallest observed burst totals 126, every observed burst emits from a zero residual with ~2× to spare. This is why the rule keys on the delta and not on the cumulative counter: a high-water-mark rule over `estimated_tokens` emits nothing for burst 2 (167 never exceeds burst 1's 184), which is exactly the failure AC1 names.

**AC6 holds twice over.** The `d <= 0` guard is the direct reason a payload-free line emits nothing: an absent `estimated_tokens_delta` decodes to `0`, hits the guard, and returns consumed-with-no-event before touching the accumulator. Independently, the invariant `acc ∈ [0, bound-1]` holds after every line (the only way to reach `bound` is to emit and reset), so a zero-delta line can never *itself* trigger an emit even if the guard were removed. State both: the guard is what a reader sees, the invariant is what makes it structural.

**The invariant is also what makes the arithmetic safe.** `acc ∈ [0, bound-1]` is what puts `bound - acc` in `[1, bound]`, so the crossing test cannot overflow whatever claude sends. The invariant and the subtracted form hold each other up; note the dependency at the field, so neither is "simplified" alone.

**The guard also closes a permanent-silence hazard.** A negative delta would drive the accumulator down and could make it un-crossable for the rest of the turn — silence, which is the one outcome AC1 forbids. One comparison closes it. This is not a defence against an unobserved claude bug; it is what keeps the AC1 property true for inputs the capture does not constrain.

`>=` not `>`: a line that lands exactly on the bound has delivered the full quantum.

### 3. The bound — `minThinkingTokensPerEvent = 64`

A `min`, not a `max`: it is the smallest accumulated delta that earns an event, so the package's `max*` naming would read backwards.

The argument, to be reproduced in the constant's comment in `maxTaskDescription`'s style:

- **The ceiling is measured.** The smallest observed burst totals **126** tokens of delta. Any bound above that lets a whole burst go silent — at 127 burst 3 emits nothing while bursts 1, 2 and 4 still do, which is the silent-stretch failure AC1 rules out. So `bound ≤ 126` is a hard constraint from the data.
- **64 is roughly half of it**, so a future burst *half the size of the smallest one observed* still emits from a zero residual. The multiple is the safety margin, exactly as `maxTaskFieldID`'s 9× and `maxTaskDescription`'s 32× are.
- **It is a real rate reduction.** On the capture: 33 lines → **8** events, ~4×. The 2026-07-27 measurement's ~10 lines/turn (`ignoredLineTypes:186-195`) at ~20 tokens/line implies ~1–2 events on a typical turn.
- **Power of two, matching the package's constants.** Cosmetic, but it is the house style.

There is no envelope arithmetic here and the comment should say why: two `int` fields cannot grow, so the size argument every other constant in this file makes does not apply. This bound governs **frequency**, not size, and it is the first of its kind in the package.

### 4. Parser state — one int, and the turn-stateless claim it amends

`Parser` gains exactly one field:

```go
thinkingSinceEmit int   // accumulated estimated_tokens_delta since the last ThinkingProgress
```

Four properties, all of which belong in the amended `Parser` doc:

- **No lock.** The single-writer invariant (`parser.go:297-303`) already covers it: `os/exec` drives `Write` from one goroutine, and nothing else reads parser state. This field changes nothing about that argument — but it is the first field the argument actually has to carry, so restate it at the field rather than leaving it to the type doc alone.
- **Reset at the turn boundary.** `consumeLine`'s `result` arm sets it to zero. That is the one boundary the parser already recognizes, and both `result` subtypes (clean and `error_during_execution`) go through it, so a cancelled turn resets too.
- **Zero cross-turn bleed stays structural**, which is what the existing doc promises. The reset is unconditional on `result` and the field is the only cross-line state besides the partial-line buffer. Amend the doc's absolute phrasing ("no per-session accumulator") to name the one accumulator, its single boundary, and the fact that it is a *counter*, not a memory of anything claude said.
- **A child that dies without a `result`** leaves a residual `< bound` behind on a long-lived parser (`cmd/pyry/streamsup_runner.go:127` builds one per session, not per turn). The next turn's first event can therefore arrive up to `bound - 1` tokens early. Named, bounded, harmless — say so rather than adding a second reset path for it.

**Do not** reset on `system/init`. It is a per-turn marker and resetting there would double the boundary logic for no gain; `result` is the boundary and one boundary is the point.

### 5. Dispatch, decode, emit

**One new arm** in `emitSystemSubtype` (`parser.go:553-564`), which is the one enumeration site:

```go
case "thinking_tokens":
    return p.emitThinkingProgress(line)
```

Nothing else in `consumeLine`'s structure changes. `system` stays whole on `ignoredLineTypes`, so `TestParser_IgnoredLineTypesIsTheMeasuredSet`'s `reflect.DeepEqual` pin stays green untouched and "no `system` line reaches the unrecognized lane" stays structural.

**New decode target**, beside the three existing ones:

```go
type systemThinkingTokensLine struct {
    EstimatedTokens      int `json:"estimated_tokens"`
    EstimatedTokensDelta int `json:"estimated_tokens_delta"`
}
```

Two fields, which is the whole mapped set. `uuid` and `session_id` are absent from the **decode target itself** — a stronger guarantee than any test sweep, since a field never declared cannot leak. Say so in the doc, as `systemTaskStartedLine`'s does. Plain `int`, not `*int`: absence and zero are treated identically because the rule's response to both is the same (accumulate nothing, emit nothing), and a pointer would buy a distinction nothing acts on.

**New emit function**, mirroring `emitBackgroundTaskStarted` structurally:

`func (p *Parser) emitThinkingProgress(line []byte) bool` — decodes `line` (the **top-level bytes**, never a nested field), applies § 2's rule, reports `true` in every case.

- The `line` argument is load-bearing for the sibling's reason, and the doc must restate the reason rather than the call shape: `streamLine`'s doc pins that control shapes are read from the top level only and nested content is never re-scanned, which is what stops a tool result whose text is literally `{"type":"result"}` from forging a turn boundary. Decoding this payload from anywhere else would let claude's own tool output forge a liveness claim.
- Undecodable payload (a string `estimated_tokens`, say) → content-free `Debug` naming the subtype keyword only, no event, no `Unrecognized`, `return true`. Same reasoning as all three siblings; a numeric value too large for `int` lands here too.
- **Nothing numeric is logged**, on either path. The package rule is that nothing derived from claude's output reaches a log, and "just the token count" is exactly the kind of exception a content-free rule gets bent for — `emitBackgroundTaskRoster`'s doc already refuses the same bend for the entry count (`parser.go:719-724`). Follow it.

**Do not extract a shared helper** from the three sibling emitters. This one shares no bounding apparatus with them; the only common shape is `decode → Debug-on-error → return true`, which is four lines.

**Test-side reader.** The tests need all 33 lines in stream order, and `capturedSystemLine` requires exactly one match. Extract the file-read + `is_capture` + `payload_encoding` provenance into `capturedSystemLines(t, subtype) [][]byte` (stream order, `t.Fatalf` on zero matches) and make `capturedSystemLine` a wrapper that keeps its exactly-one rule. One reader, one provenance check — duplicating the provenance logic into a second reader is the failure worth avoiding here, and `capture_test.go:53-54` already anticipates a fourth caller.

### 6. The realclaude mirror — and the cascade the ticket body does not name

Adding `thinking_tokens` to `parserIgnoredTypes["system"]`'s mapped set has **two** consequences, not one. The ticket names the first.

**(a) The fixture must cross the bound.** `TestParserIgnoredTypesMatchesStreamsupParser` derives each fixture line's expectation from the table: a mapped subtype must emit **at least one** event. `parseOne` (`:1174`) writes **one** line to a **fresh** parser, so the fixture only emits if its own delta crosses the bound on its own. The existing bare line (`:1065`) emits zero and would drive the test RED on its "want at least 1" arm.

Replace it with a payload-bearing line whose delta is comfortably above the bound — `estimated_tokens` and `estimated_tokens_delta` both `1024`, 16× the bound, so a later change to the bound cannot silently break the mirror. Keys are the capture's; no field structure is invented.

Amend `parserIgnoredTypeFixtures`' comment (`:1054-1061`) to state the exception and why: the bare-`type`+`subtype` convention exists because "every field beyond those two is a chance to trip the emitters' undecodable arm", and it holds for the three background-task subtypes, which emit unconditionally once decoded. It **cannot** hold for a rate-bounded mapping, which emits zero for a payload-free line — by design, per AC6. The next reader should not have to rediscover that.

**(b) The cross-runner SEQUENCE comparison un-filters ~10 lines/turn, and the live gate goes RED.** This is the part the ticket body misses, and it is the expensive one.

`shapeFilterDrops` (`:278-286`) is what `extractShapes` uses to reduce each runner's stdout to a comparable shape sequence. It consults the two one-sided tables **by type alone** — `system` is in neither, because both runners emit `system/init` — and then falls through to `parserDropsShape(typ, subtype)`. Once `thinking_tokens` is mapped, `parserDropsShape` returns false, the lines survive the filter, and `TestPtyRunnerVsStreamRunner_StructuralEquivalence` (`:487`, live, in `make e2e-realclaude` and therefore in `make preship`) compares ~10 streamrunner `system/thinking_tokens` shapes against **zero** on the ptyrunner side. That asymmetry is measured — #1218 recorded it, and it is the exact RED #1218 was opened to fix.

Two non-fixes, and the reasons matter because they are the tempting ones:

- **Putting `system` in `expectedStreamRunnerOnly.Events`** is factually false (both runners emit `system/init`) and, because `additiveDriftViolations` reads that same table, would pre-authorise a genuinely one-sided `system` divergence in the future. The file's own comment (`:159-170`) already refuses this, for this reason.
- **Leaving `thinking_tokens` out of `parserIgnoredTypes`** keeps the sequence filter correct and the mirror a lie: the mirror would claim the parser drops a line the parser maps, which is the "dangerous direction" `TestParserIgnoredTypesMatchesStreamsupParser`'s own doc names (`:1098-1101`), and it would stay green only because the bare fixture emits zero.

**The fix** is a fourth, deliberately narrow table consulted by `shapeFilterDrops` only:

```go
var expectedStreamRunnerOnlySubtypes = map[string]map[string]struct{}{
    "system": {"thinking_tokens": {}}, // #1218 2026-07-28: streamrunner ~10/turn, ptyrunner exactly 0.
}
```

`shapeFilterDrops` gains one lookup, before or after the `parserDropsShape` call — order is immaterial, both return `true`. The doc must carry three points:

1. **Why it is separate from `expectedStreamRunnerOnly`** rather than a field on `additiveDriftAllowlist`: that struct is read by `additiveDriftViolations`, which governs the **SET** check, and widening it would tolerate a one-sided divergence in the check whose job is to catch one. This table governs the **SEQUENCE filter only**. `additiveDriftViolations` compares top-level types, and `system` is emitted by both runners, so nothing is lost by keeping it out.
2. **Why it is separate from `parserIgnoredTypes`**: that table states a fact about the shipped parser ("does it map this?"), and the answer is now *yes*. This one states a fact about the two runners ("do both emit this?"), and the answer is *no*. Conflating them is what forces a false statement into one of the two.
3. **The measurement and its ticket**, so a future reader can re-check it rather than inherit it.

With this in place `TestExtractShapes_FiltersParserIgnoredTypes` (`:981`) stays **green with its `want` unchanged** — its fixture's two `thinking_tokens` lines are still filtered, just by a different table. That is a trap: green before and green after proves nothing. The discriminating pin is § Testing's `TestShapeFilterKeepsThinkingTokensOutOfTheSequence`, which asserts the two tables deliberately disagree.

### 7. Downstream — zero consumer edits, verified

| Site | Behaviour | Verdict |
|---|---|---|
| `internal/turnbridge/outbound.go:129` | `default:` → `("", nil, false)` | Event stops at the daemon boundary. This is the ticket's "nothing a client receives changes". #1386. |
| `internal/acpbridge/outbound.go:175` | `default:` | Same, ACP side. #1262-family. |
| `cmd/pyry/stream_turn_busy.go:160` | `default:` → neither opens nor closes the turn | Correct unexamined: the turn is already open when thinking happens. Its comment says "any future variant" — no edit. |
| `cmd/pyry/interactive_turn_v2.go:256` | `default:` → one content-free `Debug` per event | ~8 Debug lines per turn instead of 0. `eventKind`'s own `default:` (`:440`) returns the literal `"unknown"`, so nothing claude-derived is logged. Identical to what all three background-task variants already do — pre-existing, unchanged, and #1386's to fix. Worth one line in the spec, not an edit. |

No test sweeps the `turnevent.Event` variant set by reflection (re-verified; #1380, #1381 and #1382 each added a variant with no `internal/turnevent` test edit). That is what keeps this ticket self-contained.

---

## Concurrency model

Unchanged in shape, extended in substance by one field. `Parser.Write` is driven serially by `os/exec`'s single stdout-forwarding goroutine (`parser.go:297-303`), so `thinkingSinceEmit` needs no mutex — and this is the first field for which that argument does real work rather than describing the buffer. No new goroutine, no channel, no lock, no clock read. The rule is a pure function of (previous accumulator, this line), which is what makes the count pin deterministic.

Contrast worth keeping in mind while reading `watchdog.go`: `streamrunner`'s `streamParser` locks because its watchdog goroutine reads its state. If a future slice gives this parser a concurrent reader, it adds the guard then — and this field is now the first thing that guard would have to cover.

---

## Error handling

| Condition | Behaviour | Why |
|---|---|---|
| Line does not decode into `systemThinkingTokensLine` (string or over-`int` `estimated_tokens`) | Content-free `Debug` naming the subtype keyword only; no event; `return true` | Keeps `system` structurally unable to reach the unrecognized lane. A malformed line of a *known* subtype is not news. |
| `estimated_tokens_delta` absent | Decodes to `0`, hits the guard: no accumulation, no event | Absence is claude's to choose (#1380's rule). AC6's second clause. |
| `estimated_tokens_delta` is `0` | Same arm | No progress, nothing to report. |
| `estimated_tokens_delta` is negative | Same arm — **not** accumulated | A negative would make the accumulator un-crossable and the turn permanently silent, which is the one outcome AC1 forbids. |
| `estimated_tokens_delta` is `math.MaxInt64` | Crosses the bound: one event, `acc = 0` | The subtracted crossing test (§ 2) has no term that can overflow. The additive form wraps negative here and kills the turn's signal silently. |
| `estimated_tokens` absent but delta present | Event emitted with `EstimatedTokens == 0` | The bound keys on the delta; the cumulative reading is carried, not required. No captured negative case justifies a validation rule. |
| Accumulator has not reached the bound | No event | The rate bound doing its job. |
| Turn ends (`result`, either subtype) | Accumulator reset to 0 | Zero cross-turn bleed stays structural. |
| Child dies with no `result` | Residual `< bound` survives on the long-lived parser | Named and bounded (§ 4); the next turn's first event can arrive at most `bound - 1` tokens early. |

No new failure mode reaches a caller: every path either emits one event or consumes silently, and one bad line never affects the next.

---

## Testing strategy

Everything except § 6's mirror work lands in `internal/streamsup/parser_test.go` and runs under `make check`. The capture is read by relative path through the plural reader; no build tag, no credentials. Keeping the central assertions out of `make e2e-realclaude` is deliberate — that suite SKIPs (exit 0) with no claude login, so a green run there is no evidence.

### `TestParser_ThinkingProgressCountFromCapture` (AC2, AC3)

Drive all 33 captured lines, in stream order, through **one** parser (not one per line — the accumulator is the thing under test).

- **The pinned count is `8`, written as a literal.** Not derived from `minThinkingTokensPerEvent`, not recomputed by the test — the `taskStartedCapCheat` rule (`parser_test.go:544-548`) applied to a count instead of a byte length. A test that recomputes its expectation from the constant puts the constant on both sides of the assertion and stays green under any mutation of it.
- **The evidence, to be recorded in the test's doc comment and reproduced by the developer before shipping:** at the shipped `64` the 33 lines produce **8** events; at `65` they produce **7**; at `128`, **4**; at `32`, **14**. The nearest *differing* neighbour is `65` — and the developer should note in the comment that no threshold in `[41, 125]` produces a different count at *both* `T-1` and `T+1` (the count is a step function with plateaus wider than one), so single-sided discrimination is the strongest available property, not a shortcut. Verify these numbers against the shipped parser rather than trusting this spec; if any differs, the rule was implemented differently and that is the finding.
- `33 > 8` is AC2's "strictly fewer", asserted rather than left to the reader.
- Assert the count of captured lines is **33** first, so the pin cannot go vacuous on a capture that stopped matching.

Driving the 33 lines alone is equivalent to driving the whole stream: the capture's `terminated_on` is `result` and its `outcome_detail` records the census as valid for one turn, so no `result` line falls between stream indices 2 and 45 and the accumulator is never reset mid-run. Say so in the test doc — it is the assumption the count rests on.

### `TestParser_ThinkingProgressCoversEveryBurst` (AC1)

The criterion that matters most, and the one a naive rule fails.

- **Derive the bursts from the capture, don't pin them.** A new burst starts wherever a line's `estimated_tokens` is less than or equal to the previous line's — the counter reset. Assert the derivation yields **4** bursts so it cannot go vacuous, then assert each burst contains **at least one** emitted event.
- Emitted events are located by driving the lines one at a time through the same parser and recording which input index produced output; at the shipped bound those are capture indices `6, 10, 20, 24, 31, 37, 42, 45` → per-burst `[2, 2, 1, 3]`. Assert the *per-burst minimum ≥ 1*, not the exact distribution — the exact distribution is what the count pin above already covers, and pinning it twice makes both brittle.
- **A control that proves the assertion can fail.** Re-run the same driver against a bound of `127` (a local test-only value, passed to a small helper that reproduces the rule — *not* a mutation of the shipped constant) and assert burst 3 gets **zero** events. Without this the "≥ 1 per burst" assertion is unfalsifiable from reading the test, and the whole criterion turns on that boundary being real.

### `TestParser_ThinkingProgressMapsFromCapture` (AC5)

One event's field mapping, driven from the capture.

- Pick the first emitting line, derive its expected `EstimatedTokens` / `EstimatedTokensDelta` from that line's own decoded payload rather than pinning literals — the capture is redacted, and the derive-don't-pin rule is the family's.
- **The drop sweep**, inherited from `TestParser_TaskStartedMapsFromCapture:649-671`: reflect over every field on the event, for each of the capture's own `session_id` and `uuid` values, failing if any carries either. `t.Fatalf` if either value reads empty so the assertion cannot go vacuous. Note that `ThinkingProgress` has **no string fields at all**, which makes the sweep trivially green today — so state the floor differently from #1380's: assert the swept field count is **2** (both `int`), with a comment that a later ticket adding a string field raises it and forces the sweep to do real work. A sweep that visits zero fields and passes is the failure worth catching.

### `TestParser_ThinkingProgressSilentWithoutPayload` (AC6)

Table-driven, one parser per row (no accumulator carry-over between rows), each expecting **zero** events:

- No payload keys at all: `{"type":"system","subtype":"thinking_tokens"}` — the realclaude mirror's old bare fixture.
- The wrong key: `{"type":"system","subtype":"thinking_tokens","tokens":42}` and `…,"tokens":128}` — the two lines deleted from `parser_test.go:145` and `:516`. Their evidence moves here rather than being dropped.
- `estimated_tokens` present, `estimated_tokens_delta` absent.
- `estimated_tokens_delta` exactly `0`.
- `estimated_tokens_delta` negative.
- Undecodable: `estimated_tokens` as a string.
- **A control row**: a single line whose delta is `1024` must emit exactly **1**, so the zero-event rows above cannot pass on a mis-wired sink.
- **A sub-turn control**: drive a payload-free line *after* accumulating `bound - 1` tokens, and assert still zero. This is the `acc ∈ [0, bound-1]` invariant — the property that makes a payload-free line unable to trigger an emit even indirectly.

### `TestParser_ThinkingAccumulatorSurvivesAnExtremeDelta` (§ 2's overflow property)

The one test that fails on the obvious-but-wrong additive implementation, so it must exist even though no capture motivates it.

- Accumulate `bound - 1` tokens, then drive one line with `estimated_tokens_delta: 9223372036854775807`: **exactly one** event, and the accumulator is left crossable.
- The proof that it is left crossable, and the assertion that actually discriminates: follow it with a line carrying `bound` tokens and require **one more** event. Under the additive form the accumulator has wrapped to roughly `-2^63` and this second line emits nothing — the whole rest of the turn goes silent, which is why the first assertion alone is not enough.
- One row driving the same extreme delta from a **zero** accumulator, for completeness: still exactly one event.

### `TestParser_ThinkingAccumulatorResetsAtTurnEnd` (§ 4)

- Accumulate `bound - 1` tokens, feed `{"type":"result","subtype":"success"}`, then feed one line carrying `1` token of delta: **no** `ThinkingProgress` follows. Without the reset it would emit.
- Same with `subtype: "error_during_execution"` — a cancelled turn resets too.
- The `TurnEnd` event itself is still emitted exactly once (the reset must not disturb the arm it lives in).

### `TestParser_ThinkingProgressDropIsLoggedContentFree` (security)

The package's standing rule on the new path, mirroring `TestParser_TaskStartedDropIsLoggedContentFree` and reusing `logRecorder`.

- Drive **both** paths: a captured line that emits, and a malformed line whose `estimated_tokens` is a distinctive string literal that must appear in no record.
- Sweep every record's message and attributes for that literal, for the capture's `session_id` and `uuid` values, and **for the decimal rendering of every token number the run used**. The last one is the new leak surface: a count feels harmless to log and is the thing this handler is most tempted to explain itself with.
- Drive the numeric rows with **distinctive** values (`70141` rather than `5` or `128`), so a substring sweep cannot false-positive on a timestamp, a byte count, or the bound itself appearing in unrelated record text — and so a real leak cannot hide behind a digit that was going to be there anyway.
- Assert the Debug record names the subtype keyword only.

### The realclaude mirror (§ 6) — build-tagged, reproduce offline

```
go test -tags e2e_realclaude -run 'TestParserIgnoredTypesMatchesStreamsupParser|TestExtractShapes_FiltersParserIgnoredTypes|TestShapeFilterKeepsThinkingTokensOutOfTheSequence' ./internal/e2e/realclaude/
```

Needs no claude and no network. **Record the run proving the fixture discriminates: RED before the arm lands, green after.** A fixture green both times has proven nothing — run it against the tree with the mirror table edited but the parser arm not yet added, and keep the output.

New test, `TestShapeFilterKeepsThinkingTokensOutOfTheSequence`, is § 6's discriminating pin and is two assertions:

- `parserDropsShape("system", "thinking_tokens")` is **false** — the parser maps it.
- `shapeFilterDrops("system", "thinking_tokens")` is **true** — the sequence comparison still excludes it.

With a doc comment saying the two answers disagree **deliberately**, naming #1218's measurement, and stating that removing either half re-opens a live-gate RED that costs a real-claude run to discover. This is the only assertion in the package that goes red if someone "simplifies" the fourth table away.

### Untouched and expected green

- `TestParser_IgnoredLineTypesIsTheMeasuredSet` — pins top-level types, not subtypes.
- The main table's `unknown top-level type surfaces` row (`parser_test.go:157-167`).
- `internal/turnevent`'s tests — no exhaustiveness pin over the variant set.
- `dropped_line_capture_test.go:1514`'s classification row — `dropcapClassify` (`:1063`) drives the real parser and its fixture is payload-free, so it still classifies as dropped. Its `why` string ("the highest-rate ignored subtype") is now imprecise; amending it is optional and belongs to #1260's instrument, not here. **Consequence worth recording, not fixing:** a *future* capture run will no longer list the payload-bearing `thinking_tokens` lines under `dropped_lines`, because they are mapped. That is correct, and the next reader of a census should not read it as a regression.
- Every mobile/ACP e2e suite — the event never reaches `MapEvent`'s mapped set.

---

## Comment obligations

Swept every Go comment mentioning `thinking_tokens`, `turn-stateless`, `cross-line state`, or the mapped-set count. Sites to edit, and the reasoning for each site left alone:

| Site | Verdict |
|---|---|
| `parser.go:521-528` (`consumeLine`'s drop comment) | **EDIT — AC4.** It names `thinking_tokens` among the subtypes `emitSystemSubtype` does not map. False the moment the arm lands. Also re-scope its parenthetical "(there is no session state to reset)" about `system/init`: the parser now holds one accumulator, and its boundary is `result`, not `init`. |
| `parser.go:203-209` (`ignoredLineTypes`, the #1380 CORRECTED block) | **EDIT.** *"As of #1381 all three subtypes the capture holds a payload for are mapped, and any fourth is a new case arm there."* This ticket is the fourth, and the count was already loose — the capture holds payloads for `init` and `thinking_tokens` too. Restate by reference to `emitSystemSubtype` with no number, which is what the surrounding paragraph already argues for. |
| `parser.go:284-303` (`Parser`'s doc) | **EDIT.** *"Turn-stateless. The parser holds no turn counter, no awaiting flag, no per-session accumulator."* Name the one accumulator, its single `result` boundary, and keep the zero-cross-turn-bleed promise by pointing at the reset rather than at the absence of state. |
| `parser.go:711-715` (`emitBackgroundTaskRoster`'s doc) | **EDIT.** *"The parser holding no cross-line state is that refusal's enforcement mechanism."* Re-scope without weakening the refusal: the parser holds no roster and no per-task memory; the one field it now holds is a token counter reset at the turn boundary, and synthesizing a task-finish event would still require remembering the previous roster. |
| `watchdog.go:60` | **EDIT (one clause).** *"The #1088 Parser is deliberately turn-stateless and holds no awaiting flag."* The awaiting-flag half stays true. Comment-only, in an unwired component. **This is not the `idleStall` defect** at `watchdog.go:13-23`, which the ticket puts out of scope — do not touch that one. |
| `parser_test.go:499-508` (silence-table doc) | **EDIT.** Two false sentences: the justification that `thinking_tokens` surfacing would grow "a row per turn" (it now surfaces, rate-bounded, ~4× fewer than the lines), and *"Do not re-point a row at the capture … `capturedSystemLine` requires exactly one matching record and `thinking_tokens` has 33"* — which this ticket falsifies by adding the plural reader. |
| `parser_test.go:516` and `:144-145` | **EDIT (move, don't delete).** Both rows assert a `thinking_tokens` line is dropped-as-ignored. The subtype is now mapped, so the reason changed even though the outcome did not. Move both lines verbatim into `TestParser_ThinkingProgressSilentWithoutPayload`, where the reason is visible. |
| `capture_test.go:36-54` | **EDIT.** The doc describes a singular reader shared by three mapping tests; it gains a plural sibling and a fourth caller. Keep the provenance-at-the-reader paragraph — it now guards both. |
| `ptyrunner_byte_equivalence_test.go:1054-1061` | **EDIT — the ticket's own note.** State that `thinking_tokens` is a deliberate exception to the bare-fixture convention and why (a rate-bounded mapping emits zero for a payload-free line). |
| `ptyrunner_byte_equivalence_test.go:181-186` | **EDIT.** The `"system"` entry's comment cites `~10 thinking_tokens/turn` as the reason a silent drop is the right default "for every subtype not carved out below". `thinking_tokens` is now carved out; the rate is now the reason for the *bound*, not for the drop. |
| `ptyrunner_byte_equivalence_test.go:260-277` (`shapeFilterDrops`'s doc) | **EDIT.** *"Three tables feed it, answering two different questions."* Four tables, three questions. Add § 6's three points. |
| `parser.go:186-195` (the 2026-07-27 measurement) | No edit. It records what was **observed** — `thinking_tokens` fired ~10 times per turn — which is still true and makes no mapped/dropped claim. |
| `parser.go:219-222` ("Still dropped in silence…") | No edit. Defined by reference to `emitSystemSubtype`; `task_notification` and the never-seen subtypes stay dropped. |
| `parser.go:538-552` (`emitSystemSubtype`'s doc) | No edit. It says the case arms are the one enumeration — adding an arm *is* the update. |
| `ptyrunner_byte_equivalence_test.go:145-149`, `:1009-1017` | No edit. Both point at `streamsup.emitSystemSubtype` and restate no set; `:1011`'s use of `thinking_tokens` as the example of a high-rate subtype an inverted default would leak is still a true illustration of the open-set argument. |
| `interactive_stream_unrecognized_test.go:34-37`, `dropped_line_capture_test.go:179` | No edit. Both are measurement prose about the observed rate, not claims about the mapped set. |
| `docs/protocol-mobile.md:641`, `docs/knowledge/features/streamsup-package.md:258, 282, 375` | **Not the developer's scope.** Four prose sites this ticket falsifies (the turn-stateless claim and the thinking-rate rationale). They belong to the documentation phase, which writes them from this spec plus the merged diff. Listed so nothing is lost, **not** as an acceptance criterion. |

---

## Open questions

1. **What if a future claude drops `estimated_tokens_delta` and keeps only the cumulative counter?** The mapping goes permanently silent. That is the safe failure direction — no noise, no liveness claim the daemon cannot support (AC6's spirit) — but it is silent, and nothing automatic catches it: the mirror fixture is synthesized, so it would stay green. The available mitigation is deriving the delta from the cumulative counter (`d = tokens > prev ? tokens - prev : tokens`, which handles the reset), at the cost of a second `Parser` field and duplicated logic. Deliberately not built: 33 of 33 captured lines carry both keys, and #1260's capture instrument is what would surface the change on the next probe. Revisit if a capture ever shows the key gone.
2. **Is 64 right for a model that thinks in much larger increments?** The four observed bursts run 126–197 tokens, so the bound is one-half to one-third of a burst. A model whose bursts are 20 tokens would emit once per burst instead of twice; a model whose bursts are 2000 would emit ~30 times. The rate is bounded below (never more than one event per 64 tokens) but not above in wall-clock terms. Nothing in this ticket needs it; #1386, which puts the event on the wire, is where a per-turn ceiling would earn its keep.
3. **Should `EstimatedTokensDelta` carry the accumulated delta since the last event instead of the emitting line's own?** The accumulated form would sum correctly across received events; the line's own form does not, which is a documented consumer hazard (§ 1). The line's own value is what AC5's "the two measured numeric fields" names, and it keeps the event a pure function of the emitting line. If #1386 finds a client wants a turn total, that is the ticket to revisit it in — and the change would be additive, a third field, not a re-typing.
4. **Does `system/thinking_tokens` remain one-sided across claude releases?** § 6's fourth table encodes #1218's 2026-07-28 measurement. If ptyrunner ever starts emitting the subtype, the table becomes a false statement that silently hides a real divergence — and nothing would catch it, because the filter's failure direction is quiet. Worth a re-measurement whenever the byte-equivalence suite is re-baselined against a new claude version.

---

## Security review

**Verdict:** PASS (first pass FAILED on one MUST FIX, now closed in § 2 — see [Trust boundaries]).

**Findings:**

- **[Trust boundaries]** **MUST FIX — found and closed before commit.** The first draft of § 2 wrote the rate rule as `acc += d; if acc >= bound`. `estimated_tokens_delta: 9223372036854775807` decodes into `int` cleanly, and on any nonzero residual that addition wraps to roughly `-2^63`: the comparison reads false, the accumulator lands in a region no realistic delta can climb out of, and **the turn's liveness signal dies silently until the next `result`** — an unrecoverable state in the one feature whose purpose is making a wedged turn visible, driven entirely by untrusted input. This is not "hostile claude denies its own signal" (it can do that by sending nothing); it is a single malformed or version-drifted line producing an intermittent, near-undiagnosable outage. § 2 now specifies the subtracted form `d >= bound - acc`, arithmetically identical for every representable value, with both operands in `[1, bound]` by the invariant — the failure is unrepresentable rather than guarded against. `TestParser_ThinkingAccumulatorSurvivesAnExtremeDelta` pins it, and its discriminating assertion is the *second* event after the extreme line, not the first.
- **[Trust boundaries]** No further findings. The boundary is single and explicit: `emitThinkingProgress` is the only place `system/thinking_tokens` bytes become daemon state, and it decodes from `line` — the **top-level** bytes — never a nested field. That property matters more here than on the three siblings, because this event is a **liveness claim**: were it decodable from nested content, a tool result whose text carried the right JSON could assert the daemon is alive while it is wedged. `streamLine`'s doc already pins the top-level-only rule; § 5 requires the emitter's doc to restate the *reason*, not the call shape. The decode target declares two fields, so the untrusted line's other keys cannot cross the boundary at all. Downstream holds a two-`int` value type with nothing to sanitize.
- **[Trust boundaries — new state]** No findings, and this is the category the ticket actually moves. The accumulator is the parser's first piece of untrusted-input-driven cross-line state. Its reachable range is pinned to `[0, bound-1]` after every line, it is reset unconditionally at the one turn boundary, and the worst a residual can do across a child crash is bring one event forward by `< bound` tokens (§ 4). Event rate is bounded **above** by the line rate — at most one event per line, strictly fewer in practice — so the mapping can only reduce what already crossed the parser, never amplify it.
- **[Tokens, secrets, credentials]** No findings, one deliberate non-carry, one deferral. `session_id` and `uuid` are absent from the **decode target itself**, which is stronger than a test sweep — a field never declared cannot leak. Neither is a credential, but `session_id` is claude's session identity and not the daemon's conversation identity, and a field of that name invites a consumer or a later wire mapper to route on it (#1380's reasoning, unchanged). Separately: a token count is a weak side channel about how much the model reasoned. It does not leave the daemon in this ticket (`MapEvent` drops the variant), so the question belongs to **#1386**, which is where it would reach a phone.
- **[File operations]** N/A by design, with one constraint on the refactor. The only file touched is the committed capture, read from tests by the constant relative path at `capture_test.go:20` with no caller-controlled component. **SHOULD FIX:** the `capturedSystemLines` extraction (§ 5) must keep the path a package constant and must not gain a path parameter — a plural reader is exactly the shape someone later generalizes into "read any capture", and the provenance checks (`is_capture`, `payload_encoding`) must stay inside it rather than moving to callers. No production path opens, stats, or writes a file.
- **[Subprocess execution]** N/A, and for a reason worth writing down rather than inheriting. `BackgroundTaskStarted.Description` and `BackgroundTaskUpdated.Patch` both carry the "safe to RENDER as text, never to execute or re-shell" warning because both can hold command text. This variant carries two integers and no text at any point — the warning has no target, and copying it in would be cargo-cult. If a future field on this type ever carries claude-authored text, the warning comes with it.
- **[Cryptographic primitives]** N/A. No randomness, no key material, no comparison against a secret anywhere on this path. The only comparison is `d >= bound - acc`, against a compile-time constant, on data that is not secret.
- **[Network & I/O]** No findings; the category is inverted relative to the three siblings and the spec must not paper over that. Every previous constant in this file bounds **size**; this one bounds **frequency**, because two `int` fields cannot grow and the envelope arithmetic has no term to compute. Resource exhaustion: the accumulator is one machine word; the whole-line buffer stays bounded by `defaultMaxParseBuf` (4 MiB, `parser.go:19`); the event rate is bounded above by the line rate and below by 1-per-64-tokens. **OUT OF SCOPE — #1386:** whether ~8 events/turn (measured, not estimated) is an acceptable load on the mobile push queue is that ticket's decision. Recorded here with the number so it inherits a measurement rather than a guess.
- **[Error messages, logs, telemetry]** No findings, with one new leak surface closed by test. The standing package rule is that nothing derived from claude's output reaches a log. The undecodable path logs the subtype **keyword** only — the same class as `sl.Type` in the existing drop log — and nothing numeric on either path. A token *count* is the exception this rule will be bent for first ("it's just a number"), which is why `TestParser_ThinkingProgressDropIsLoggedContentFree` drives both paths and sweeps every record for the decimal rendering of every number used, with distinctive values so the sweep cannot false-positive on a digit that was going to be there anyway. `emitBackgroundTaskRoster`'s doc already refuses the identical bend for the roster entry count; follow it.
- **[Concurrency]** No findings. No goroutine, no channel, no lock, no clock read. The single new field is read-modify-written only from `consumeLine`, which `Parser`'s documented single-writer invariant (`parser.go:297-303`) confines to `os/exec`'s one stdout-forwarding goroutine — this is the first field for which that invariant does real work rather than describing a buffer, and § 4 requires it restated at the field. There is no check-then-mutate across a boundary: the crossing test and the reset are one straight-line sequence in one goroutine. Shutdown mid-turn loses at most a residual `< bound`.
- **[Threat model alignment]** Aligned, and nothing deferred silently. `docs/protocol-mobile.md` § Application-envelope size cap is not reached: the variant is unnamed in `turnbridge.MapEvent`'s switch, so it stops at the daemon boundary and adds no protocol type, payload struct, or route. The mobile wire is **#1386** (which this ticket blocks) and the desktop ACP surface is **#1262**; both are named in the spec. One threat this ticket *closes* rather than defers: the parent's worry that a liveness signal could mask a hung turn was re-verified false in both directions (`Watchdog` reads its own stdout copy and has no production caller; `turnevent.Stall` has one producer on the PTY surface). § 1 requires that in the type doc so a future wiring slice does not reintroduce a suppression mechanism nothing needs.

**Reviewer:** architect (self-review per `architect/security-review.md`)
**Date:** 2026-08-09
