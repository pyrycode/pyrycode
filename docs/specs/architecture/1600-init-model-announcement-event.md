# Spec — #1600: map claude's `system`/`init` model announcement to a bounded daemon event

**Size:** `s` (PO's label, confirmed — see § Sizing).
**Labels:** `security-sensitive` (security review below), `needs-real-claude` (AC 5 edits `internal/e2e/realclaude`, which `make check` never compiles).

---

## Files to read first

Generated from `codegraph_context`, pruned to what the design actually needs. Read in this order; symbol names resolve with `codegraph_search` / `codegraph_node`.

**The shape you are copying** — read all three, they are the same design at three levels of complexity:

- `internal/streamsup/parser.go` → `emitSystemSubtype` — the switch you add one arm to. Its doc states the rule the whole family rests on: *the case arms are the ONE enumeration of the mapped set*.
- `internal/streamsup/parser.go` → `emitBackgroundTaskStarted` — the family's canonical emitter. Extract: the `bound` closure, the sequential-statements-not-composite-literal rule, the undecodable Debug's exact message and attribute shape, and the doc paragraph on decoding from top-level bytes.
- `internal/streamsup/parser.go` → `emitRateLimit` — the precedent for **suppressing on an empty field** (`case "":`). This is the arm AC 3 follows, *not* `emitBackgroundTaskStarted`'s emit-with-empty-field rule.
- `internal/streamsup/parser.go` → `emitThinkingProgress` — the precedent for a **silent consumed-drop** (its `EstimatedTokensDelta <= 0` arm returns `true` and logs nothing). This is the drop-path logging you copy.

**The decode targets and the caps:**

- `internal/streamsup/parser.go` → `streamLine` — the line-level segmentation struct. Extract: why the subtype's shape stays a *separate* struct, and the top-level-bytes-only invariant that stops a tool result from forging a control shape.
- `internal/streamsup/parser.go` → `systemThinkingTokensLine` — the simplest sibling decode target (two fields, no container). Your struct is one field and mirrors its doc structure.
- `internal/streamsup/parser.go` → `maxTaskFieldID`, `maxRateLimitField` — the two identifier-shaped caps AC 2 names. Extract: both are 256, both are separate constants *even though they are equal*, and the doc states why folding them would be wrong.
- `internal/streamsup/parser.go` → `truncateField` — the cut helper. Extract: `<=` boundary (a field of exactly `limit` bytes is **not** truncated), and that the returned value can come out 1–3 bytes under the limit when a cut lands mid-rune.

**The event package:**

- `internal/turnevent/event.go` → `RateLimited` — the doc style for a claude-derived, producer-bounded variant.
- `internal/turnevent/event.go` → `Unrecognized` — the `Truncated bool` report shape you adopt (see § Design, decision 3).
- `internal/turnevent/event.go` → `isTurnEvent` markers and the `var (_ Event = …)` block — both need a line.

**The daemon side:**

- `cmd/pyry/interactive_turn_v2.go` → `eventKind` — one arm. Read `ThinkingProgress`'s and `RateLimited`'s arms: their docs state *why* the arm exists (the other call sites' `unknown`), which AC 4 restates.
- `cmd/pyry/stream_turn_busy.go` → `turnMarkFor` — read the `DISCHARGED 2026-08-09 (#1404)` paragraph. **No change here.** Confirms the whitelist absorbs a new variant, and this ticket discharges the prediction a second time.
- `internal/turnbridge/outbound.go` → `MapEvent` — read the `default` arm only. **No change here.** No wire frame appears until the client-facing slice adds one.

**Tests you must change or read:**

- `internal/streamsup/parser_test.go` → `TestParser_IgnoredLineTypesStaySilent` — **this test goes RED if you do nothing.** Its `system/init` row's fixture carries `"model":"claude-opus-5"`, so under AC 1 it now emits. See § Design, decision 6.
- `internal/streamsup/parser_test.go` → `TestParser_RateLimitIsLoggedContentFree` — the no-log sweep you copy exactly. Extract: the sweep covers the **emit** path as well as the drop paths, the distinctive sentinel values, and the `t.Fatalf` guard that fails when the sweep would be vacuous.
- `internal/streamsup/parser_test.go` → `collectEvents`, `logRecorder`, `taskFieldIDCapFixture` — existing helpers. The cap-fixture block's doc states the rule: a fixture written as the constant it validates asserts nothing about the number.
- `internal/streamsup/capture_test.go` → `capturedSystemLine` — the exactly-one reader. **Verified: the capture holds exactly one `system`/`init` record**, so this works unchanged; no plural reader and no helper generalization is needed.
- `internal/e2e/realclaude/dropped_line_capture_test.go` → `dropcapClassify`, `TestDropcapClassification` — read `dropcapClassify` to see *why* AC 3's pin holds: it runs the shipped parser and derives `dropped` from "zero events emitted", so it needs no mirror update.
- `cmd/pyry/interactive_turn_v2_test.go` → `TestInteractiveTurnEmitterV2_NoAppOutputLogLeak` — the `cmd/pyry`-side no-log precedent, referenced but **not extended** by this ticket (see § Testing).

---

## Context

claude emits a `system` line with subtype `init` once **per turn**, carrying a top-level `model`. `emitSystemSubtype` has no arm for it, so it falls through to the `ignoredLineTypes["system"]` drop in `consumeLine` and the value never becomes an event.

The value is worth having because it is the only thing that answers *what is actually running*. The daemon already carries a `model` on `protocol.ScreenSnapshotPayload` and `protocol.SessionSettingsPayload`, but both mean the **per-session override** — `""` means "inherited default, no override". So in the ordinary case the daemon publishes an empty string while claude has named a concrete model on every turn. The daemon knows what it asked for; only claude knows what it got.

This is the sixth member of a landed family: #1380 (`task_started`), #1382 (`task_updated`), #1381 (`background_tasks_changed`), #1385 (`thinking_tokens`), #1404 (`rate_limit_event`). Every structural choice below is one of theirs, and the two places this ticket *diverges* from the majority sibling are called out explicitly.

**Scope boundary.** This ticket produces a neutral turn event and stops. No wire frame, no protocol type, no client. `turnbridge.MapEvent`'s `default` drops the variant, so nothing reaches a phone until a later slice adds a case.

---

## Design

Three production files. No new files.

### 1. `internal/streamsup/parser.go` — the cap

Add `maxModelField`, a new package-level constant, value **256**.

Doc must state:

- **Measured, not chosen.** Three observations across two claude versions and three spawn shapes: `claude-haiku-4-5-20251001` (25 bytes, in the committed capture), `claude-haiku-4-5` (16 bytes, the `permission_protocol_*` captures), `claude-sonnet-5` (15 bytes, #1582's recorded run). 256 is ~10× the observed maximum — the same ratio `maxTaskFieldID` has to its 29-byte observed maximum.
- **A separate constant even though it equals `maxTaskFieldID` and `maxRateLimitField`.** `maxRateLimitField`'s doc already makes this argument and it applies verbatim: they bound different fields for different reasons, and folding them would let a future change to the task-id budget silently move this one.
- **Deliberately not `validModel`'s 64** (`internal/relay/v2session_settings.go`). That validator bounds a *phone-supplied override the daemon accepts*, and it also enforces a charset. This cap bounds what *claude announces*, which the daemon does not control and must not reject. Unifying them would make a claude that echoes a longer identifier look like a malformed client request.
- Applied at **construction**, exactly as the sibling caps are, so an oversized value never enters the event stream, the push queue, or any log.

### 2. `internal/streamsup/parser.go` — the decode target

```go
// systemInitLine is the decoded payload of one system/init line.
type systemInitLine struct {
	Model string `json:"model"`
}
```

**One field, and the omissions are the point.** The captured init line carries 22 keys: `type`, `subtype`, `cwd`, `session_id`, `tools`, `mcp_servers`, `model`, `permissionMode`, `slash_commands`, `apiKeySource`, `claude_code_version`, `output_style`, `agents`, `skills`, `plugins`, `capabilities`, `analytics_disabled`, `product_feedback_disabled`, `uuid`, `memory_paths`, `fast_mode_state`, `fast_mode_disabled_reason`. Twenty-one are deliberately absent from the target.

The doc must name **`cwd`** and **`session_id`** specifically as the two that matter — `cwd` is the operator's filesystem path and `session_id` is claude's session identity, not the daemon's conversation identity. Use the family's standing formulation: *absent from the DECODE TARGET is a stronger guarantee than the test's reflection sweep, because a field that is never declared cannot leak.*

A non-string `model` fails the whole decode and takes the undecodable path — the same asymmetry `systemTaskUpdatedLine.TaskID` documents. This is the **only** reachable undecodable case: `consumeLine` has already decoded the line into `streamLine`, so malformed JSON never reaches here. State that, because it is what tells you how to build the fixture.

### 3. `internal/turnevent/event.go` — the variant

```go
type ModelAnnounced struct {
	Model     string
	Truncated bool
}
```

Plus `func (ModelAnnounced) isTurnEvent() {}` and a `_ Event = ModelAnnounced{}` line in the assertion block.

**Decision: `Truncated bool`, not `TruncatedFields []string`.** AC 2 offers both existing shapes. The payload is a single string, so `TruncatedFields` would be permanently either `nil` or `["model"]` — a variable-length container carrying one bit, plus a name the reader must check against the only field there is. `Unrecognized` is the precedent for exactly this case: one bounded string, one bool. The sibling variants use the slice because they bound *two to four* fields and the report has to say **which**.

Doc must state:

- `Model` is claude's announced identifier **verbatim**: no lowercasing, no alias expansion, no date-stamping, no family mapping, no lookup against any published model list. The measured rule is that claude *echoes an identifier at least as specific as the one it was given* — it dates a bare family alias (`haiku` → `claude-haiku-4-5-20251001`) and passes through anything already fully formed. It is **not** reliably dated, and `claude-haiku-4-5` does not appear in any published list. That is an argument for carrying the value untouched, not for repairing it here.
- Never empty: the producer's gate does not emit on an empty model.
- **Per line, not per session.** #1582 measured three init lines in one session — `[claude-sonnet-5, claude-sonnet-5, claude-haiku-4-5-20251001]` — because the `/model` turn emits its own init and *that one still reports the OLD model*. A consumer that latches the first announcement shows a stale value. This is a documented consumer hazard on the variant, in the manner of `ThinkingProgress`'s accumulator-residue hazard; it is not this ticket's problem to solve.
- Bounded by the producer at construction (`streamsup`'s `maxModelField`), following `Unrecognized`'s precedent.
- **Bounded and UTF-8-valid is all it is.** `truncateField` scrubs invalid UTF-8 (its cut can land mid-rune) but does **not** strip control characters or terminal escape sequences, and nothing else on this path does either. The value is claude's echo of an identifier the operator or the phone supplied, and only the phone-supplied path is charset-validated (`validModel`); a `--model` flag or a config default is not. No consumer renders it today — `MapEvent`'s `default` drops the variant — so this is a note for the client-facing slice, which owes the sanitization at its own render boundary. Say it on the field, because that is where a future consumer reads.
- Like every variant here it carries no conversation identity — the bridge injects that.

### 4. `internal/streamsup/parser.go` — the arm and the emitter

Add to `emitSystemSubtype`:

```go
case "init":
	return p.emitModelAnnounced(line)
```

New method, contract only:

```go
// emitModelAnnounced decodes a system/init line and emits at most one
// turnevent.ModelAnnounced, reporting that it CONSUMED the line either way.
func (p *Parser) emitModelAnnounced(line []byte) bool
```

Behaviour, in order:

1. `json.Unmarshal(line, &il)` — the **top-level** bytes, never a nested field.
2. Decode failure → `p.log.Debug("streamsup: dropping undecodable system line", "subtype", "init")`, return `true`. Message and attribute shape **byte-identical** to `emitBackgroundTaskStarted`'s and `emitBackgroundTaskUpdated`'s, so this adds no new log category.

   **The `err` is deliberately not logged, and this is the likeliest slip in the whole ticket.** `encoding/json` quotes the offending input bytes into its error text, so `"err", err` on a line whose `model` is a long or revealing string puts that value in the daemon log and defeats AC 4 through a channel no per-path `reason` check can see. The repo's house idiom points the *other* way (CLAUDE.md: wrap errors with context), which is exactly why this needs stating rather than assuming. The precedent that says it outright is `cmd/pyry/interactive_turn_v2.go`'s `emit` marshal-error path: *"Never echo the payload or `err.Error()` (encoding/json quotes invalid input bytes into its error)"*. Both task siblings' undecodable arms already follow it. The Debug carries a fixed message and the literal subtype keyword — nothing else, ever.
3. `il.Model == ""` (absent and present-but-empty land here identically) → return `true` with **no log at all**.
4. Otherwise `truncateField(il.Model, maxModelField)` → emit `turnevent.ModelAnnounced{Model: …, Truncated: …}`, return `true`.

Three decision points worth writing down in the doc:

- **Why the empty case is silent rather than logged.** `emitRateLimit` logs a `reason` keyword on each of its rungs because it fires once per *run* and has three distinguishable rungs. `init` fires once per **turn** and has one non-undecodable drop reason, so a Debug there would put a record in the daemon log on every turn — reinstating in the log the per-turn noise row `TestParser_IgnoredLineTypesStaySilent`'s doc exists to prevent in the event stream. `emitThinkingProgress`'s `delta <= 0` arm is the precedent: consumed, silent, no log. **Note the observable consequence:** today every init produces `"streamsup: dropping stdout line" type=system`; after this change a model-carrying init produces no log record at all and a model-less one produces none either. That is one *fewer* Debug per turn, and no test pins the record that disappears.
- **Why empty suppresses rather than emitting an empty-field event.** This diverges from the task handlers, which treat a missing field as claude's choice and emit with the field empty. Here the model *is* the whole payload — an event carrying an empty model asserts "claude announced a model" while naming none, which is a claim the line did not make. `emitRateLimit`'s `case "":` rung is the precedent, and its formulation carries over: absence, a present-but-empty value, and a line carrying no such key all land here and are answered identically, which is what makes a plain-string decode target sufficient.
- **Why nothing is surfaced as `Unrecognized` on any path.** Keeping `system` whole on `ignoredLineTypes` is what makes "no system line reaches the unrecognized lane" structural, and that guarantee is worth more than surfacing a malformed line of a subtype we already know. Breaking it would put a bad payload in front of the live zero-unrecognized gate for a line the daemon does in fact recognise.

Also in this file: the `ignoredLineTypes` drop-branch comment in `consumeLine` currently names `init` as a dropped example ("system (init / status / any subtype `emitSystemSubtype` does not map)"). Correct it in the family's `CORRECTED YYYY-MM-DD (#N)` style — `init` is mapped now, `status` remains the measured-and-dropped example. The paragraph below it about init being a per-turn marker and *not* a state-reset boundary for the `#1385` accumulator **stays true and stays**: mapping the line changes what is sent, not where the accumulator's boundary is.

### 5. `cmd/pyry/interactive_turn_v2.go` — the discriminant

One arm in `eventKind`:

```go
case turnevent.ModelAnnounced:
	return "model_announced"
```

The variant **name** only. The doc must say what `RateLimited`'s arm says and one thing more: `Model` is precisely the field the #833 posture — restated across `internal/relay/v2session_settings.go` and `internal/sessions/pool.go` as *"model / effort / YOLO values are NEVER logged at any level"* — exists to keep out of logs, and it is not returned here.

**No `Handle` arm, and this is deliberate.** Adding one would mean `flushDelta` + `emitMapped`, and `MapEvent`'s `default` returns `ok == false`, so `emitMapped` would log `interactive_turn.unmapped` and drop — a *second* content-free Debug plus a delta-flush ordering change, in exchange for nothing. Without an arm the variant lands in `Handle`'s `default`, which logs `interactive_turn.unknown` with `kind=model_announced` — content-free, and correct for a variant with no wire mapping yet. The client-facing slice adds both together.

### 6. `internal/streamsup/parser_test.go` — the row that flips

`TestParser_IgnoredLineTypesStaySilent`'s `system/init` row is `{"type":"system","subtype":"init","session_id":"s1","model":"claude-opus-5"}`. **It carries a model, so under AC 1 it emits and the test goes red.**

Follow the family's documented move exactly — #1385 and #1404 each did this once, and their `CORRECTED` paragraphs in that test's doc are the template:

- Remove the row from the table.
- Re-home the line **verbatim** in the new mapping test, where it is no longer silent and the doc says so.
- Add a `CORRECTED 2026-08-19 (#1600)` paragraph to the test's doc. Note that the doc's **opening sentence** uses `system/init` as the argument for the table's existence ("system/init fires once per TURN, so if it surfaced as an Unrecognized every conversation would grow a row per turn"). That argument is now spent for `init` in the same way #1385's was spent for `thinking_tokens`: the subtype never reached an `Unrecognized`, and what replaced the drop is a *bounded, per-line* event. Rewrite the opener around `system/status` and the never-seen-subtype row, which are what the table still asserts.

Two neighbouring fixtures **stay green unmodified** and are the natural counterpart pins — verify, do not edit:

- `parser_test.go`'s `"system init is a no-op (per-turn marker, not session-open)"` row: its line carries **no** `model`, so AC 3 keeps it silent.
- `roundtrip_test.go`'s two-events-per-turn assertion, driven by `helper_test.go`'s canned init line, which also carries no `model`.

---

## Data flow

```
claude stdout
  → Parser.Write → consumeLine
      → json.Unmarshal(line, &streamLine)          [segmentation only]
      → ignoredLineTypes["system"] branch
          → emitSystemSubtype("init", line)
              → emitModelAnnounced(line)           [TOP-LEVEL bytes, second decode]
                  ├ undecodable → Debug{subtype:"init"}, consumed
                  ├ model == "" → silent,            consumed
                  └ else → truncateField(model, maxModelField)
                           → emit turnevent.ModelAnnounced{Model, Truncated}
  → sinkFor: turnMarkFor → default → turnMarkNone   [droppable, no change]
  → interactiveTurnEmitterV2.Handle → default       [content-free Debug, no change]
  → turnbridge.MapEvent → default → !ok             [no wire frame, no change]
```

The second decode reads `line`, the top-level bytes, **not** a nested field. `streamLine`'s doc states the invariant this preserves: control shapes are read from the top level only and nested content is never re-scanned, which is what stops a tool result whose text is literally `{"type":"result"}` from forging a turn boundary. Decoding this payload from anywhere else would let claude's own tool output announce a model the daemon never ran.

---

## Concurrency model

**No new concurrency.** `emitModelAnnounced` runs on the existing single `Parser.Write` goroutine (os/exec's stdout forwarder, one per live runner) and touches no parser state — unlike `emitThinkingProgress`, it holds no accumulator, so the parser's one piece of cross-line state and its `result`-arm boundary are untouched. No goroutines, no channels, no locks, no shutdown sequencing.

Rate: one event per turn maximum. The variant is not a droppable delta (the droppable set is `assistant_delta` only, #610), so it holds a queue slot — the same accepted cost the five sibling variants carry, bounded by the same existing backpressure, at a *lower* rate than `ThinkingProgress`. No second cap is imposed and none is owed.

---

## Error handling

| Input | Behaviour | Log |
|---|---|---|
| `model` a non-string (`5`, `{}`, `[]`) | consumed, no event | one Debug, `subtype: "init"`, no value |
| `model` key absent | consumed, no event | none |
| `model` present but `""` | consumed, no event | none |
| `model` ≤ 256 bytes | one event, `Truncated: false` | none |
| `model` > 256 bytes | one event, cut to ≤ 256, `Truncated: true` | none |

No path emits `turnevent.Unrecognized`. No path returns `false` from `emitModelAnnounced`, so an init line never falls through to `consumeLine`'s generic drop Debug.

`truncateField`'s `<=` boundary means a value of exactly 256 bytes is **not** truncated, and a cut landing mid-rune deletes the partial rune, so a truncated value can come out 1–3 bytes under the cap. Both are existing documented behaviour; the tests below assert the boundary rather than assuming it.

---

## Testing strategy

All hermetic, inside `make check`. Field mapping comes from the committed capture (`internal/e2e/realclaude/testdata/dropped_lines_v2.1.220.json`, read via `capturedSystemLine(t, "init")`), never from a hand-built payload — reading it from `internal/streamsup` is what keeps the mapping proof inside `make check` rather than behind a gate that exits 0 with no claude login.

**Mapping, from the capture:**

- The captured init line produces exactly one `turnevent.ModelAnnounced` with `Model == "claude-haiku-4-5-20251001"` and `Truncated == false`.
- **Byte-for-byte:** assert against the value read out of the capture's own payload at test time, not against a copied literal — a literal would pass even if the mapping lowercased a value the capture happened to hold in lowercase.
- **And nothing else from the line:** the event carries no `cwd`, no `session_id`, no `uuid`. Assert this against the *whole* event value (`reflect.DeepEqual` against the expected struct), so a field added to the variant later cannot silently start carrying something.
- Two init lines in sequence produce two events (AC 1's *per line*).

**The cap:**

- A synthesized line with a 257-byte model → one event, `len(Model) == 256`, `Truncated == true`. The capture's real value is 25 bytes and reaches no defensible cap, so this fixture is necessarily synthesized; it invents no field structure beyond the one key.
- Exactly 256 bytes → `Truncated == false`, value unchanged (`truncateField`'s `<=` boundary).
- A cut landing mid-rune → result is valid UTF-8 (`utf8.ValidString`), and may be 1–3 bytes short of 256.
- **The fixture's `257` and `256` are written as literals, never as `maxModelField`.** Follow `taskFieldIDCapFixture`'s stated rule: a fixture built from the constant it validates asserts nothing about the number — halve the constant and a constant-derived fixture follows it green.

**The drop paths:**

- `{"type":"system","subtype":"init","model":5}` → no event, no `Unrecognized`, one Debug carrying `subtype: "init"` and nothing else. Assert the record has **exactly** the expected attributes — a sweep for "does it contain the value" cannot catch `"err", err`, because the decoder's error text quotes the input rather than reproducing it verbatim. Drive this with a distinctive non-string value (e.g. a long digit run) and assert the digits appear in no attribute.
- `{"type":"system","subtype":"init","session_id":"s"}` (no `model`) → no event, no `Unrecognized`, **no log record at all**.
- `{"type":"system","subtype":"init","model":""}` → same as above.
- In every case the line is consumed: no `turnevent.Unrecognized` is emitted and `consumeLine`'s generic drop Debug does not fire.

**The no-log sweep** — copy `TestParser_RateLimitIsLoggedContentFree`'s shape exactly, because its doc names the failure a per-path check cannot see: *an implementation that sets the reason correctly AND also logs the value passes every one of them*.

- Drive the **emit** path first (a distinctive sentinel model value), then each drop path, through one parser with one `logRecorder`.
- Sweep **every** record's message and **every** attribute value for: the sentinel model, the capture's real `model`, and the capture's `cwd` and `session_id` — the two most sensitive keys on the line, which the decode target does not declare. Sweeping them proves the omission holds end to end, not just at the struct.
- Include the `t.Fatalf` vacuity guard: fail if the capture does not actually carry a non-empty `cwd`/`session_id`, or the sweep proves nothing.
- The emit path must log **nothing**, which is the half of this test the drop-path assertions cannot see.

**`cmd/pyry`:** assert `eventKind(turnevent.ModelAnnounced{Model: "<sentinel>"}) == "model_announced"` and that the returned string does not contain the sentinel. Do **not** extend `TestInteractiveTurnEmitterV2_NoAppOutputLogLeak` — no `Handle` arm is being added, so there is no new emitter path to sweep.

**Regression, verify unmodified:**

- `TestDropcapClassification`'s `system/init` row (`internal/e2e/realclaude`) stays green **unmodified**. Its fixture carries no `model`, so AC 3 is what keeps it a drop. `dropcapClassify` runs the *shipped* parser and derives `dropped` from "zero events emitted", so it needs no mirror update.
- **Prove the pin with a mutant:** make `emitModelAnnounced` emit for a model-less line and confirm that row goes red. Run it via `go test -overlay=<abs-path json>` so no worktree write is needed, and remember `internal/e2e/realclaude` is behind the `e2e_realclaude` build tag — a bare `go test ./internal/e2e/realclaude/` runs zero tests. Use the `make e2e-realclaude` target's flags and report the `=== RUN` count.
- `parser_test.go`'s `"system init is a no-op"` row and `roundtrip_test.go`'s two-events-per-turn assertion stay green unmodified (both fixtures carry no `model`).

---

## Corrections this change owes (AC 5)

Every claim below is falsified by the arm landing. `internal/e2e/realclaude` must still **compile** under the `e2e_realclaude` tag afterwards — that package failing to build while every `make check` stays green is an observed failure in this repo (2026-08-16), not a hypothetical.

| Where | What is now false | Minimal correction |
|---|---|---|
| `internal/streamsup/parser.go`, `consumeLine`'s drop-branch comment | names `init` as a dropped example | `CORRECTED (#1600)`: `init` is mapped; `status` remains the measured-and-dropped example. The per-turn-marker / accumulator-boundary paragraph below it stays. |
| `internal/streamsup/parser_test.go`, `TestParser_IgnoredLineTypesStaySilent` doc | the opening sentence argues the table's purpose *from* `system/init` | `CORRECTED (#1600)` paragraph; rewrite the opener around `system/status` and the never-seen-subtype row. |
| `internal/e2e/realclaude/dropped_line_capture_test.go`, `TestDropcapClassification`'s `system/init` row `why:` string | says init's subtype "is not one `streamsup.emitSystemSubtype` maps" — test data that stays green while becoming false | the subtype **is** mapped now; the verdict is unchanged and *derived*: this fixture carries no `model`, so the shipped parser emits nothing for it. Mirror `rate_limit_event`'s adjacent `CORRECTED (#1404)` row, which made exactly this move. |
| `internal/e2e/realclaude/interactive_stream_inband_model_test.go`, header block | "emitSystemSubtype has arms for … only, so every other system subtype — init among them — falls through"; and "Adding an arm, or a log line carrying the value, is barred" | init has an arm now. The arm half is false; the **log** half stays true and is the half that matters — restate it as: the value reaches a daemon *event*, and still reaches no daemon *log*. The test's own assertions are unaffected (it taps `streamsup.Config.Stdout` upstream of the parser) and must stay green unmodified. |
| `internal/e2e/realclaude/interactive_stream_unrecognized_test.go`, header | "every subtype but the handful `streamsup.emitSystemSubtype` carves out **into background-task events**" | already imprecise since #1385 (`thinking_tokens` is not a background-task event) and now more so. Drop the "into background-task events" characterisation; the switch is the enumeration site. One line. |

**Not a developer AC** — `docs/knowledge/features/e2e-realclaude.md` carries "*`emitSystemSubtype` has no arm for `init`, by the #833 posture that keeps model/effort/YOLO values out of the daemon log at every level*". This becomes false, and its **reasoning was already wrong**: the arm's absence was never entailed by the log posture (an event is not a log — that is this ticket's whole premise). It belongs to the documentation phase, which owns `docs/knowledge/`. Flagged here so it is not lost; do not put it in the implementation budget.

---

## Sizing

Confirmed at `s` against the nearest analogues rather than by feel. Measured non-comment added lines on the landed commits:

| ticket | non-comment | raw | production files | outcome |
|---|---|---|---|---|
| #1382 `task_updated` | 333 | 694 | 2 | landed `s` |
| #1385 `thinking_tokens` | 431 | 1103 | 3 | landed `s` |
| #1404 `rate_limit_event` | 450 | 1032 | 3 (+1 comment-only) | landed `s` |

None hit `max_turns`. This ticket is the plainest member of the family: one string field, no gate rungs, no accumulator, no array, no nested container, and — verified — no capture-helper generalization (`capturedSystemLine` already reads the single init record). Projection: **~40 production non-comment lines across 3 files, ~300 test non-comment lines, ~650 raw.**

Red lines, checked against the design rather than the ticket body:

- new files: **0**
- new exported types: **1** (`turnevent.ModelAnnounced`)
- consumer call sites needing simultaneous update: **0** — verified, not assumed. `turnMarkFor` is a whitelist (`default` → `turnMarkNone`), `turnbridge.MapEvent` has a dropping `default`, `interactiveTurnEmitterV2.Handle` has a dropping `default`, and `internal/turnevent`'s test-local `eventKind` is non-exhaustive (it omits the background-task variants already).
- reject branches: **3**
- acceptance criteria: **5**

No seam to split on either: the only candidate children are "add an unproduced variant" (dead code, no non-tautological AC) and "map init to it" (needs the first), and deferring AC 5 would merge a tree carrying knowingly-false test data.

---

## Open questions

1. **Variant name.** `ModelAnnounced` reads as a noun phrase in the family's style (`BackgroundTaskStarted`, `RateLimited`). If the client-facing slice wants `ModelReported` or `SessionModel`, renaming later costs one `eventKind` arm and one `MapEvent` case — cheap, and no wire name exists yet to be bound by it. Not worth deferring on.
2. **`Handle` arm timing.** Deliberately deferred to the client-facing slice, which adds the `Handle` arm, the `MapEvent` case, and the `protocol` payload together. Until then the emitter's `default` logs one content-free `interactive_turn.unknown` per turn at Debug. If that proves noisy in practice, the fix is the client slice, not a `Handle` arm that flushes and drops.
3. **The three-init-lines-per-session hazard** (#1582: the `/model` turn's own init still reports the OLD model) is documented on the variant and left to the consumer. A producer-side dedup would need turn state the parser deliberately does not hold, and a consumer that renders the latest announcement rather than the first has no problem to solve.

---

## Security review

**Verdict:** PASS (one MUST FIX found and fixed inline before this section was written — see [Error messages, logs, telemetry] below).

**Findings:**

- **[1 · Trust boundaries]** One boundary, and it is the whole ticket: claude's subprocess stdout → daemon state. It is explicit and single-sited — `emitModelAnnounced` is the only place an init line becomes daemon data, and it decodes `line`, the **top-level** bytes, never a nested field. That is what makes the value unforgeable from claude's own tool output: `streamLine`'s invariant is that control shapes are read from the top level only and nested content is never re-scanned, so a tool result whose text is literally `{"type":"system","subtype":"init","model":"…"}` cannot announce a model the daemon never ran. Downstream there is no trusted/untrusted confusion to create, because there is no downstream: `turnbridge.MapEvent`'s `default` drops the variant, so the value reaches no wire frame and steers no decision.

- **[1 · Trust boundaries]** SHOULD FIX, **addressed inline**. `truncateField` scrubs invalid UTF-8 but strips no control characters or terminal escapes, and the value's provenance is only partly validated — a phone-supplied override passes `validModel`'s charset check, but a `--model` flag or machine default does not. Unreachable today (no consumer renders it), so this is a hand-off rather than a defect: the variant's field doc now states that bounded-and-UTF-8-valid is *all* the value is, and that the client-facing slice owes sanitization at its render boundary. Stated on the field, where a future consumer actually reads.

- **[2 · Tokens, secrets, credentials]** No findings, and the reason is structural rather than conventional. The init line is the most credential-adjacent line claude emits — the capture's own redaction rationale calls it "the operator's local claude configuration inventory" — and it carries `apiKeySource`, `mcp_servers`, `memory_paths`, `cwd` and `session_id`. The decode target declares **one** field. A field that is never declared cannot leak, on any path, regardless of what a later log or sweep does; that is a stronger guarantee than the test's reflection sweep and it is why the design refuses to widen `systemInitLine` for convenience.

- **[3 · File operations]** Not applicable, by a decision rather than by absence: the design opens nothing, writes nothing, and canonicalises no path. The one filesystem-shaped value in play is the init line's `cwd` — the operator's working directory — and it is deliberately absent from the decode target.

- **[4 · Subprocess / external command execution]** No `exec.Command`, no argv, no environment handling. The adversarial question worth naming is the **feedback loop this ticket must not create**: an announced model must never become a spawn argument. claude's echo is a *report* of what ran; treating it as an input to the next spawn would let a claude that renames or misreports an identifier steer the daemon's own argv, and would fight `Pool.UpdateSettings`' in-band `/model` delivery (#1581) for ownership of the same value. Out of scope here, and named so nobody builds it later by analogy.

- **[5 · Cryptographic primitives]** Not applicable. No randomness, no keys, no hashing. The one comparison on the path is `il.Model == ""`, which is a presence test against a constant, not an attacker-controlled value against a secret — so constant-time comparison is not owed.

- **[6 · Network & I/O]** No findings. Input size is capped at construction (`maxModelField`, 256 bytes), which is the substance of AC 2. Amplification from input to retained bytes is near zero: `systemInitLine` holds one scalar and no array, so a 4 MiB line (`defaultMaxParseBuf`, the existing bound on the transient allocation) yields at most 256 retained bytes plus one bool. Envelope arithmetic in `maxTaskDescription`'s style: 256 bytes is 0.4% of the v2 application-envelope cap of 65519 bytes — half `RateLimited`'s 0.8% and the smallest contribution in the family. No rate bound and none owed: one event per turn is below `ThinkingProgress`'s already-accepted ~1–2 per turn, and the variant is not a droppable delta, so it holds a queue slot under the same existing backpressure the five siblings do.

- **[7 · Error messages, logs, telemetry]** **MUST FIX — found and fixed inline.** The undecodable arm must not log the decoder's error. `encoding/json` quotes the offending input bytes into its error text, so `"err", err` would put the model value into the daemon log through a channel no per-path `reason` assertion can see — defeating AC 4 and the #833 posture (*"model / effort / YOLO values are NEVER logged at any level"*) in the one place a developer is most likely to reach for the house idiom (CLAUDE.md: wrap errors with context). The spec now states the omission explicitly, cites the precedent that already says it (`cmd/pyry/interactive_turn_v2.go`'s `emit` marshal-error path), and the drop-path test now asserts the record's attributes exactly rather than merely sweeping for the value — because the decoder's error text *quotes* input rather than reproducing it verbatim, so a contains-check can miss it. MUST-NOT-log on this path: the model, and every other key on the line. MUST-log: nothing. The emit path logs no record at all, and the empty-model path logs none either.

- **[8 · Concurrency]** No findings. No goroutine is spawned, no channel is added, no lock is taken, and no parser state is touched — unlike `emitThinkingProgress`, this emitter holds no accumulator, so the parser's single piece of cross-line state keeps its single `result`-arm boundary. Everything runs on the existing single `Parser.Write` goroutine. No lifetime hazard either: `encoding/json` allocates a fresh string on decode, so `Model` does not alias `p.buf` and nothing outlives the buffer it came from.

- **[9 · Threat model alignment]** Nothing new crosses the wire, so `docs/protocol-mobile.md` § Security model is untouched by construction — `MapEvent`'s `default` is the enforcement, not a convention. The posture this ticket *does* engage is #833's, restated across `internal/relay/v2session_settings.go` and `internal/sessions/pool.go`, and AC 4 is its enforcement on both the parser and the `eventKind` discriminant sites. #833 is scoped to **logs** and says nothing about the event stream — which is the ticket's premise, and is why an `emitSystemSubtype` arm does not violate it. The wire-side threat alignment is deferred to the client-facing slice, which owns the protocol type and the render boundary.

**Reviewer:** architect (self-review per `architect/security-review.md`)
**Date:** 2026-08-19
