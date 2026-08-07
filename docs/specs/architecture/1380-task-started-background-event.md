# #1380 — Map claude's `task_started` background-task subtype to a bounded daemon event

**Size:** S (PO's `size:s` confirmed, not overridden)
**Labels:** `security-sensitive`, `needs-real-claude`
**Touches:** `internal/turnevent/event.go`, `internal/streamsup/parser.go`, plus tests

---

## Files to read first

Turn-1 reading list. Each entry says what to extract; read the range, not the file.

| Path | What to extract |
|---|---|
| `internal/streamsup/parser.go:38-83` | `ignoredLineTypes` + its 2026-07-27 measurement comment. This is **correction site 1** and the canonical note. Read the whole comment — the send-vs-draw argument replaces one paragraph of it, not the block. |
| `internal/streamsup/parser.go:245-283` | `consumeLine`. The `default:` arm's `ignoredLineTypes` branch (`:269-278`) is the **only** edit site in the dispatch. `:281`'s `emitUnrecognized` is the top-level alarm AC5 protects. **Correction site 2** is the inline comment at `:270-275`. |
| `internal/streamsup/parser.go:285-316` | `emitUnrecognized` + `truncateRaw`. The exact precedent this ticket follows: cap **at construction**, content-free Debug log, `strings.ToValidUTF8` after a byte-slice cut. Mirror both. |
| `internal/streamsup/parser.go:21-36` | `maxUnrecognizedRaw`'s doc comment. The house style for justifying a cap **number** (envelope arithmetic + "more than a human reads"). The two new caps are documented the same way. |
| `internal/streamsup/parser.go:188-197` | `streamLine`. Declares `Type`, `Subtype`, `Message` only. Do **not** widen it — it is the line-level segmentation struct. The payload gets its own shape. |
| `internal/turnevent/event.go:99-143` | `UnrecognizedSite` + `Unrecognized`. The variant + doc-comment style to match. `Unrecognized`'s doc at `:124-127` is **correction site 4**. |
| `internal/turnevent/event.go:24-30, 152-174` | `Event` interface doc + the `isTurnEvent()` / `_ Event =` blocks. A new variant adds one line to each of the three lists. |
| `internal/streamsup/parser_test.go:36-45` | `collectEvents` — the one-line-in, events-out helper every new test row uses. |
| `internal/streamsup/parser_test.go:498-520` | `TestParser_IgnoredLineTypesStaySilent` — the table AC3/AC4 **extend** rather than rebuild. Its in-body comment at `:509-511` is **correction site 3**. |
| `internal/streamsup/parser_test.go:415-496` | `TestParser_UnrecognizedTruncation` — the shape of a cap test in this package, including the mid-rune / valid-UTF-8 assertion. AC2's table mirrors it. |
| `internal/streamsup/parser_test.go:522-576` | `logRecorder` + `withMessage` — reuse verbatim for the content-free-logging test. Already in the package; write no new recorder. |
| `internal/streamsup/parser_test.go:621-632` | `TestParser_IgnoredLineTypesIsTheMeasuredSet` — the `reflect.DeepEqual` pin on `{system, rate_limit_event}`. It must stay **green and unedited**; that is the check that proves the design kept `system` on the list. |
| `internal/streamsup/parser_test.go:22-34` | `harnessNudgeFixture`'s doc — the "a fixture built from the constant it validates asserts nothing" rule. Applies to the new cap tests: build over-cap fixtures from literals/arithmetic, never from the production constants. |
| `internal/e2e/realclaude/testdata/dropped_lines_v2.1.220.json` | The capture. `dropped_lines` is an array of records; `payload` is a **JSON string holding the whole line**, `payload_encoding: "json-string"`. Top-level `is_capture`, `census`, `expected_absent` are what the reader asserts against. |
| `docs/protocol-mobile.md:304` | The 65519-byte v2 application-envelope cap — the arithmetic the new cap numbers are justified against. (Verified 2026-08-07, not inherited.) |
| `internal/turnbridge/outbound.go:129`, `internal/acpbridge/outbound.go:175`, `cmd/pyry/stream_turn_busy.go:160`, `cmd/pyry/interactive_turn_v2.go:240,435` | The five downstream type-switches. **Read only to confirm each has a `default:` arm — change none of them.** See § Downstream. |

**Do not open** `internal/e2e/realclaude/*.go`. They carry `//go:build e2e_realclaude` (verified on all three files named in the ticket), so they do not compile under `make check` and nothing there goes red. Their staleness is #1379.

---

## Context

`internal/streamsup/parser.go` drops every `system` line through `ignoredLineTypes`. That discards claude's background-task lifecycle, which is why #1240's symptom exists: `turn_end`/`end_turn` and state `idle` while a command claude started is provably alive, with nothing reaching a client that separates it from a genuine finish.

This ticket maps the **first** captured subtype, `system/task_started`, and introduces the two mechanisms the siblings ride: the `system` subtype dispatch (#1382 `task_updated`, #1381 `background_tasks_changed`) and the construction-time bound on claude-derived text.

### Compatibility with the 2026-07-27 decision

The wholesale-drop decision is recorded in `ignoredLineTypes`' own comment and is sound: `system` is claude's catch-all namespace and its highest-rate emitter (`init` once per turn, `thinking_tokens` ~10× — the committed capture measures **33** across one session), so surfacing unknown subtypes would put a noise row on every turn.

That argument is about **what to draw**, and it was used to decide **what to send**. Those are separate decisions and only the second belongs to the daemon's callers. Mapping one measured subtype into the daemon's own vocabulary changes what is sent; it leaves the drawing decision entirely with the client and leaves the measurement standing. **This is a refinement of the 2026-07-27 note, not a reversal of it** — and the note has to say so in place, because as written a future reader would be right to restore the old behaviour from it.

### Provenance

Built from `internal/e2e/realclaude/testdata/dropped_lines_v2.1.220.json` (#1260; claude 2.1.220, `is_capture: true`, `outcome: fired`, `absence_claim_valid: true`; independently reproduced by an operator 2026-08-06). Census re-verified against the committed file on 2026-08-07: `system/task_started` 1, `system/task_updated` 1, `system/background_tasks_changed` 1, `system/init` 1, `system/thinking_tokens` 33, `user` 1, `rate_limit_event` 1. `expected_absent` holds exactly `["task_notification"]`.

The captured line, verbatim from the record's `payload` (235 bytes, redacted):

```
{"type":"system","subtype":"task_started","task_id":"bybi8g8i8","tool_use_id":"toolu_01ENMNP5P4d3pqjTg9LgCxgZ","description":"cat $FIFO","task_type":"local_bash","uuid":"c8f0ec50-0362-4dad-9418-5fd911cf3d62","session_id":"$SESSION_ID"}
```

`task_notification` has **no captured payload** — measured absent on the interactive surface in both runs, seen once on the *headless* surface on 2026-07-30. Subtype-count parity between surfaces does not follow from top-level type parity (#1218's `thinking_tokens` split is the precedent). It is therefore in the **drop** set here, never the mapped set, and its test row is necessarily synthesized.

---

## Design

### 1. The event: `turnevent.BackgroundTaskStarted`

A new variant of the sealed `Event` sum type in `internal/turnevent`.

**Name.** `BackgroundTaskStarted`, not `TaskStarted`. "Task" alone collides with ACP tool-call vocabulary; "background task" is the domain concept — work that outlives a turn — and is the daemon's word for it, so one claude release renaming `task_started` lands in the parser and nowhere else.

**Fields** (all derived from the capture; no invented structure):

| Event field | Go type | claude key | Note |
|---|---|---|---|
| `TaskID` | `string` | `task_id` | claude's task handle. Opaque; the join key the siblings use. |
| `ToolCallID` | `string` | `tool_use_id` | **The translation.** `ToolStart`/`ToolUpdate` already call this identifier `ToolCallID`, and it is the same identifier — so a consumer joins the background task to the `ToolStart` that spawned it with no vocabulary lookup. |
| `Description` | `string` | `description` | The literal Bash command line. The only free-text field, and the security-relevant one. |
| `TaskType` | `string` | `task_type` | `local_bash` in the capture. Plain `string`, **not** a typed enum — one observation does not earn a closed set (same discipline `harnessNoOutputNudge` applies). |
| `TruncatedFields` | `[]string` | — | The cut report. See § 3. |

**Two captured keys are deliberately NOT carried, and the doc comment must say why:**

- `session_id` — claude's session identity, not the daemon's conversation identity. Carrying it would invite a consumer to treat it as one. The ticket's Security note is explicit on this; § Security below makes it a test.
- `uuid` — claude's per-line message id. Nothing in the daemon reads it, and carrying it would add a fourth claude-derived string to the truncation surface for no consumer. If #1377 finds a use, it is added then with a reason.

This is a documented mapping decision, not an omission: AC1 forbids **invented** field structure, and both drops are recorded in the type doc and proven by test.

**Wiring.** One `isTurnEvent()` method, one `_ Event = BackgroundTaskStarted{}` assertion, and the variant named in the `Event` interface's doc comment at `event.go:24-30`. Pure value type, standard library only — the package's existing constraint.

### 2. The dispatch (no new decode for the subtype; a new shape for the payload)

`streamLine` already declares `Subtype` and the `result` arm already reads it (`parser.go:267`), so the subtype match is a new arm and nothing more.

**Placement is load-bearing.** The match goes **inside** the `default:` arm's `ignoredLineTypes` branch (`parser.go:269-278`), as a guard clause ahead of the existing Debug-log-and-return:

```go
if ignoredLineTypes[sl.Type] {
    if sl.Type == "system" && p.emitSystemSubtype(sl.Subtype, line) {
        return
    }
    p.log.Debug("streamsup: dropping stdout line", "type", sl.Type)
    return
}
```

Three inserted lines, one nesting level, and the existing branch is otherwise untouched. Two structural consequences follow, and both are acceptance criteria:

- **AC5 holds by construction.** `emitUnrecognized` at `:281` is reachable only when `!ignoredLineTypes[sl.Type]`. `system` stays on the list, so **no `system` line can reach the unrecognized lane at all**, whatever its subtype. A genuinely new top-level type still reaches it unchanged.
- **`TestParser_IgnoredLineTypesIsTheMeasuredSet` (`parser_test.go:624`) stays green and unedited.** It pins top-level types by `reflect.DeepEqual`; a subtype match inside the branch does not disturb it. A design that removed `system` from the list would go red there *and* hand every unmapped `system` subtype to the unrecognized lane — the failure AC4 forbids.

The `sl.Type == "system"` guard at the call site is deliberate: `rate_limit_event` carries no subtype today, and the guard keeps that true by construction rather than by luck if a future `ignoredLineTypes` entry ever gains a colliding subtype name.

**`emitSystemSubtype(subtype string, line []byte) bool`** — switches on `subtype`; `case "task_started"` calls the constructor and returns `true`; `default` returns `false` (the caller then Debug-logs and drops). Its doc comment carries the AC4 reason:

> Unknown `system` subtypes fall through to silence **deliberately**. Surfacing them would reintroduce the per-turn noise row the 2026-07-27 measurement forbade, and would break the live zero-unrecognized gate (`internal/e2e/realclaude/interactive_stream_liveness_test.go:253`) on the next claude release that adds a chatty subtype.

**The payload needs its own decode target.** `streamLine` declares three fields; every mapped field here is new. Add an unexported struct in `parser.go` — `systemTaskStartedLine` — with the four mapped keys and nothing else, decoded by a **second** `json.Unmarshal` over the same `line` bytes (still in scope in the `default:` arm). Do not widen `streamLine`, which is the line-level segmentation struct.

**The second decode's input must be `line` itself — the top-level bytes — never a nested field.** `streamLine`'s doc (`parser.go:188-192`) claims that segmentation keys on the top-level `type` only and nested content is never re-scanned, which is what makes a `{"type":"result"}` inside tool output unable to forge a turn boundary. Decoding the new payload from anywhere but `line` would make `system/task_started` forgeable from claude's tool output. See § Security.

**Decode failure → drop, content-free Debug, no event.** A `task_started` line whose `task_id` is (say) a number is dropped in silence, exactly as the whole line is dropped today. It does **not** become an `Unrecognized`: that would break the AC5 structural guarantee above, which is worth more than surfacing a malformed known subtype. The tradeoff is recorded in the constructor's doc comment.

### 3. The bound (AC2)

Every claude-derived field is truncated **at construction**, before the event reaches the sink — following `emitUnrecognized`'s precedent exactly, so an oversized payload never enters the event stream, a queue, or a log.

**One helper**, mirroring `truncateRaw`: takes a value and a cap, returns the (possibly cut) string and whether it cut, and applies `strings.ToValidUTF8` so a mid-rune byte cut cannot ship invalid UTF-8 on a field that rides a JSON string downstream. `encoding/json` already replaces invalid input bytes with U+FFFD on decode, so the only mid-rune hazard is our own cut; the scrub stays unconditional for symmetry with `truncateRaw`. Boundary is `<=`: a field exactly at the cap is **not** truncated.

**Two cap constants**, because the two field classes have different reasoning:

- `maxTaskFieldID = 256` — for `TaskID`, `ToolCallID`, `TaskType`. Machine-generated tokens; the capture's longest is `tool_use_id` at 29 bytes, so 256 is ~9× the observed maximum and still cuts nonsense hard.
- `maxTaskDescription = 4 << 10` — for `Description`. A model-authored command line, genuinely variable. The capture's redacted description reads 9 bytes, but the record's `payload_len_bytes_captured` (377) minus `payload_len_bytes` (235) gives 142 bytes of redaction across two sites (`$SESSION_ID`→uuid, +25; `$FIFO`→path, +117), so the **real** description was ≈126 bytes. 4096 is ~32× that.

Worst-case event text: `3 × 256 + 4096 = 4864` bytes, which is 7.4% of the 65519-byte v2 application-envelope cap (`docs/protocol-mobile.md:304`, verified) and under a third of `maxUnrecognizedRaw`'s 16 KiB whole-line precedent. That leaves room for the envelope's other fields plus JSON escaping when #1377 puts this on the wire. Both constants carry that arithmetic in their doc comments, in `maxUnrecognizedRaw`'s style.

**The cut report: `TruncatedFields []string`.**

- Holds the **daemon's** field names in snake_case: `"task_id"`, `"tool_call_id"`, `"description"`, `"task_type"`. Note `tool_call_id`, not claude's `tool_use_id` — the report names the field it describes, consistent with the translation rule.
- Appended in **declaration order**, so the value is deterministic and pinnable.
- **`nil` when nothing was cut**, never an empty non-nil slice — so `reflect.DeepEqual` against `nil` works and #1377's wire adapter can emit the field as absent rather than `[]`.

Chosen over per-field booleans (which AC2 explicitly declines to mandate, and which would be four bools growing to six across the siblings) and over a bitmask (opaque at a call site and on the wire). A slice of names extends to #1382's `patch` and #1381's roster without a type change.

---

## Concurrency model

Unchanged. The parser's single-writer invariant (`parser.go:130-136`) holds: `Write` → `consumeLine` → the new constructor → `p.emit` all run on the one os/exec forwarder goroutine. No new goroutine, no new shared state, no lock. The parser stays **turn-stateless** — `BackgroundTaskStarted` is emitted per line and accumulated nowhere, so it cannot leak across a turn boundary.

## Error handling

| Failure | Behaviour |
|---|---|
| `system` line, unmapped subtype (incl. `task_notification`, `init`, `thinking_tokens`, `status`, never-seen) | Drop; existing content-free Debug (`"type"` only). No event. |
| `task_started` line whose payload will not decode into the new shape | Drop; content-free Debug naming the subtype only. No event, and **no** `Unrecognized` (§ 2). |
| `task_started` line with a field over its cap | Event emitted with the field cut and named in `TruncatedFields`. Never dropped — a long command line is exactly the case a client most needs to see. |
| `task_started` line missing a mapped field | Event emitted with that field empty. Absence is claude's to choose; there is no captured negative case, so no validation is invented. |
| Non-`system` unknown top-level type | Unchanged: `Unrecognized` at `parser.go:281`. |

## Downstream

**No consumer edit. Read to confirm, change nothing.** All five type-switches over `turnevent.Event` have `default:` arms, verified 2026-08-07:

- `internal/turnbridge/outbound.go:129` → `("", nil, false)`; no wire frame. Correct — #1377 owns the wire.
- `internal/acpbridge/outbound.go:175` → no notification. Correct — #1262 owns ACP.
- `cmd/pyry/stream_turn_busy.go:160` → the opener set is a whitelist, so the new variant neither opens nor closes a turn. **This is the behaviour you want unexamined**: a background task by definition outlives the turn.
- `cmd/pyry/interactive_turn_v2.go:240` (emitter dispatch) → content-free Debug `"relay: interactive-turn drop; unknown event"`.
- `cmd/pyry/interactive_turn_v2.go:435` (`eventKind`) → returns the literal `"unknown"`.

The last two mean one content-free Debug line per background task until #1377. That is accepted, and **`eventKind` is deliberately not extended here** — naming the variant there is #1377's edit, not a third production file in this ticket. Flagged so its absence reads as a decision rather than a miss.

---

## Testing strategy

All of it runs in `make check`. The capture is data, not a package: a test in `internal/streamsup` reads it at `../e2e/realclaude/testdata/dropped_lines_v2.1.220.json` with no build tag and no credentials. The `e2e_realclaude` tag belongs to that package, not to the file.

### The capture reader — new file `internal/streamsup/capture_test.go`

One reusable helper, because #1381 and #1382 read the same file from the same package.

`capturedSystemLine(t *testing.T, subtype string) []byte` — decodes the capture, asserts top-level `is_capture == true` (a hand-built file swapped in fails here, which is AC1's provenance rule enforced at the reader), selects `dropped_lines` records with `type == "system"` and the given `subtype`, asserts `payload_encoding == "json-string"`, asserts **exactly one** match (two would force a deliberate decision rather than a silent first-wins), and returns `[]byte(payload)`. `t.Fatalf` on every failure — a missing record is a broken premise, not a skip.

### Scenarios

**AC1 — the mapping, from the capture (`TestParser_TaskStartedMapsFromCapture`)**

- Drive the shipped parser via `collectEvents` with `capturedSystemLine(t, "task_started")`. Assert exactly one event, of type `turnevent.BackgroundTaskStarted`.
- Assert **routing, not literals**: decode the captured payload into a `map[string]any` inside the test and compare `TaskID`↔`task_id`, `ToolCallID`↔`tool_use_id`, `Description`↔`description`, `TaskType`↔`task_type`. A derived assertion still catches a field swap, and unlike a pinned literal it is not pinning `$FIFO`/`$SESSION_ID` redaction placeholders (the ticket's warning).
- One pinned literal as a canary that the reader picked the right record: `TaskType == "local_bash"`.
- `TruncatedFields` is `nil` — the captured line is far under every cap.
- **The two deliberate drops:** no event field equals the payload's `session_id` value, and none equals its `uuid` value. Reading both from the decoded map keeps the assertion honest if the capture is ever re-taken unredacted.

**AC2 — the bound, from synthesized lines (`TestParser_TaskStartedFieldCaps`, table-driven)**

Fixtures are built from literals and arithmetic, never from the production constants (`harnessNudgeFixture`'s rule at `parser_test.go:22-34`) — a fixture built from the constant it validates asserts nothing. These lines invent **no field structure**: same four keys as the capture, oversized values.

- `description` at cap+1 → cut to exactly the cap; `TruncatedFields == ["description"]`; the other three intact and unnamed.
- `task_id` over cap → cut; `TruncatedFields == ["task_id"]`.
- `tool_use_id` over cap → cut; report names **`"tool_call_id"`** (the daemon's name, not claude's). This row is the one that pins the translation.
- `task_type` over cap → cut; `TruncatedFields == ["task_type"]`.
- Two fields over cap at once → both named, in declaration order (`task_id` before `description`).
- Every field exactly at its cap → `TruncatedFields == nil` (the `<=` boundary).
- `description` over cap ending in multi-byte runes straddling the boundary → the cut value is `utf8.ValidString`, mirroring `parser_test.go:492`.

**Assert exact cut length on ASCII fixtures only.** `truncateRaw`'s precedent scrubs with an **empty** replacement (`strings.ToValidUTF8(s, "")`), so a mid-rune cut *deletes* the partial rune rather than replacing it — the multi-byte row's result is legitimately 1–3 bytes **shorter** than the cap. Build the exact-length rows from ASCII and assert only validity (plus `len <= cap`) on the multi-byte row, or the assertion goes red for the wrong reason.

**AC3 + AC4 — the drop set (extend `TestParser_IgnoredLineTypesStaySilent`, `parser_test.go:503`)**

Extend the existing table; do not rebuild it. Keep the five existing rows (`init`, `thinking_tokens`, `status`, `a_subtype_invented_next_year`, `rate_limit_event`) and add three:

- `task_updated` — **driven from the captured line** (it has a payload; `capturedSystemLine(t, "task_updated")`).
- `background_tasks_changed` — **driven from the captured line**, as AC3 requires by name.
- `task_notification` — **synthesized**, `{"type":"system","subtype":"task_notification"}`. It has no captured payload (`expected_absent`), so a synthesized row is the only honest option and it invents no field structure.

The table's element type changes from `string` to a small struct (or the literals are appended as strings alongside `string(capturedSystemLine(...))`) — whichever keeps the diff smaller. `a_subtype_invented_next_year` is AC4's never-seen row and already exists; it must stay.

**AC5 — the top-level alarm survives**

- `TestParser_LineMapping`'s existing `"unknown top-level type surfaces"` row (`parser_test.go:~264`) must stay green **unedited**. That is the `make check` half of AC5.
- `TestParser_IgnoredLineTypesIsTheMeasuredSet` (`:624`) must stay green **unedited**. That is what proves `system` never reaches the unrecognized lane.
- The live clause — a normal turn produces zero unrecognized events (`internal/e2e/realclaude/interactive_stream_liveness_test.go:253` fatals on one) — **is discharged only by an operator's live run**. `make e2e-realclaude` SKIPS without a claude login and a skip exits 0, so a green suite in the dispatch environment is **not** evidence. The ticket carries `needs-real-claude` for exactly this.

**Security — content-free logging (`TestParser_TaskStartedDropIsLoggedContentFree`)**

Reuse `logRecorder` (`parser_test.go:531`) verbatim. Drive (a) the captured line and (b) a malformed `task_started` line, and assert **no captured record's message or attr value contains the description text**. This is the assertion that catches the realistic later break — someone appending `"description", d.Description` to the Debug line. The package's standing rule: content crosses the wire, not the log.

---

## The four comment corrections (AC6)

None of these fails a build when it goes stale, and two siblings will extend the mapped set. So: **one canonical statement, three pointers, and the mapped set enumerated only where it cannot go stale** — in `emitSystemSubtype`'s `case` arms, because adding a subtype *is* adding a case.

**Site 1 — `ignoredLineTypes`' comment (`parser.go:38-79`) — the canonical correction.** Replace the "system is ignored WHOLESALE rather than per-subtype on purpose" paragraph (`:56-60`). Keep the 2026-07-27 measurement, the observed-types block, and the 2026-07-30 amendment as they stand. The replacement records, in this order:

1. `system` is **no longer ignored wholesale** — `emitSystemSubtype` matches subtypes inside the ignored branch (#1380).
2. The 2026-07-27 argument was about **what to draw**, and it was used to decide **what to send**. Those are separate decisions; the per-turn noise row it forbade is a rendering decision the daemon's callers own. The measurement **stands** — this refines it rather than reversing it.
3. What is still dropped: every `system` subtype `emitSystemSubtype` does not match, including never-seen ones and `task_notification`, plus `rate_limit_event` whole.
4. **The list itself is unchanged and stays top-level-types-only.** `system` stays on it, which is what keeps `emitUnrecognized` structurally unreachable from any `system` line and `TestParser_IgnoredLineTypesIsTheMeasuredSet` green.
5. A pointer to `emitSystemSubtype` as the enumeration of the mapped set — **do not restate the list here.**

**Site 2 — the `default:` arm's inline comment (`parser.go:270-275`).** "tolerated and dropped, silently, exactly as before" is false at the edit site itself. Correct to: dropped in silence **except** the subtypes `emitSystemSubtype` maps, with a pointer to `ignoredLineTypes` for the full statement. Two lines; no restatement.

**Site 3 — `TestParser_IgnoredLineTypesStaySilent`'s in-body comment (`parser_test.go:509-511`).** "Ignoring `system` wholesale, not per-subtype, is deliberate" is now false. Correct to: `system` maps per-subtype since #1380; every subtype **not** mapped still stays silent, which is what this table asserts and why a new subtype must not become a per-turn noise row. Pointer to `ignoredLineTypes`.

**Site 4 — `Unrecognized`'s doc comment (`turnevent/event.go:124-127`).** "Types we knowingly ignore (system/\*, rate_limit_event) stay silent exactly as before" is now false for `system/*`. Correct to: `rate_limit_event` and every **unmapped** `system` subtype stay silent; the mapped ones become their own event variants. Prose pointer to `streamsup`'s `ignoredLineTypes` — a comment reference, **not** an import; `turnevent` must not depend on `streamsup`.

---

## Open questions

- **`TaskType` as a closed set.** One observation (`local_bash`) is not a measurement. If #1381/#1382 or a later capture surface a second value, that is the point to consider a typed kind with a fallback — not now.
- **`TruncatedFields` naming across siblings.** #1382's `patch` is an object, not a string, so its cut report may not be a field name at all. The `[]string` shape accommodates a name like `"patch"`; whether that is the right granularity is #1382's call, and this spec does not bind it.
- **A `local_bash` description is a shell command line.** Rendering it as text is safe; executing or re-shelling it is not. Out of scope here (the daemon only carries it), but worth stating for #1377 and #1262 — see § Security.

---

## Out of scope — do not touch

- **The mobile v2 wire** (#1377) and **the desktop ACP surface** (#1262). No protocol type, no payload struct, no route. This ticket stops at the event layer.
- **`internal/e2e/realclaude/`** (#1379). `parserIgnoredTypes` in `ptyrunner_byte_equivalence_test.go`, the two comments at `ptyrunner_byte_equivalence_test.go:144` and `interactive_stream_unrecognized_test.go:9`, and `dropped_line_capture_test.go:1511`'s `"system is ignored wholesale"` all go stale on this change, deliberately. All three files carry `//go:build e2e_realclaude`, so nothing in this ticket's `make check` goes red because of them (verified 2026-08-07).
- **`docs/knowledge/codebase/1380.md`** — the documentation phase writes it after merge.
- **`cmd/pyry/interactive_turn_v2.go`'s `eventKind`** — #1377's edit (§ Downstream).

---

## Security review

**Verdict:** PASS

**Findings:**

- **[Trust boundaries]** No MUST FIX. The boundary is explicit and single: claude's stdout bytes → `consumeLine` → `emitBackgroundTaskStarted`. Every claude-derived value crosses it as a bounded `string` on a pure value type, and the construction-time cap means no downstream holder can ever receive an unbounded one. Downstream callers hold `turnevent` value types, which is the package's existing "trusted, already-bounded" signal — unchanged by this ticket.
- **[Trust boundaries — forge resistance]** SHOULD FIX → **constraint added to § 2.** `streamLine`'s doc (`parser.go:188-192`) claims a load-bearing property: segmentation keys on the **top-level** `type` only, nested content is never re-scanned, so a tool result whose text is literally `{"type":"result"}` cannot forge a turn boundary. This ticket adds a *second* `json.Unmarshal`, which is exactly the shape that could break that claim. It does not, because the second decode is over **`line`** — the same top-level bytes — never over a nested field. A developer who instead decoded from a nested value would make `system/task_started` forgeable from tool output. The spec states the constraint explicitly; code-review should check the decode's input is `line`.
- **[Trust boundaries — identity confusion]** No MUST FIX; verified. claude's `session_id` on this line is *not* the daemon's conversation identity, and a variant carrying a field named `SessionID` would invite a consumer (or #1377's wire mapper) to route on it. The design drops it (and `uuid`) from the outset, per the ticket's Security note. What this pass confirmed is that the drop is enforced by an **assertion** in the AC1 test — no event field equals the payload's `session_id` or `uuid` — rather than by a comment, which is what makes it survive #1382 and #1381 extending the same event family.
- **[Tokens, secrets, credentials]** No findings. No token is generated, stored, compared, or logged. `task_id` / `tool_use_id` are claude-generated opaque handles with no authorisation meaning — they authorise nothing on either side, so entropy and constant-time comparison are not applicable. Neither is persisted.
- **[File operations]** Not applicable, and this is a design property rather than an omission: the parser opens, reads, watches, and resolves no file — its only input is the `Write` bytes (`parser.go:117-122`). The capture file is read by **test** code only, at a fixed relative path with no caller-supplied component.
- **[Subprocess / external command execution]** **The category worth the most attention here, and the finding is that nothing executes.** `Description` is a literal Bash command line lifted from claude's output. Nothing in this ticket passes it to `exec.Command`, `sh -c`, a template, or any interpreter — it is carried as an opaque `string` and never interpreted. The risk is entirely downstream: a future consumer that renders it into a shell, a copy-to-clipboard-and-run affordance, or an HTML surface without escaping. Recorded in § Open questions as an explicit warning to #1377 and #1262 rather than silently assumed.
- **[Cryptographic primitives]** Not applicable — no randomness, no hashing, no key material, no comparison against a secret anywhere in the design.
- **[Network & I/O — resource exhaustion]** No MUST FIX. This is the category AC2 exists for, and the design's answer is quantified rather than asserted: per-field caps of 256 / 256 / 256 / 4096 bound one event's claude-derived text at 4864 bytes, 7.4% of the 65519-byte v2 application-envelope cap (`docs/protocol-mobile.md:304`, verified). The cap is at **construction**, so an oversized payload never reaches the event stream, the push queue, or a log — the ordering that makes the bound real rather than cosmetic. `TruncatedFields` is bounded by construction at 4 elements with a closed set of literal values, so the report itself cannot be grown by a hostile payload.

  Two limits of that bound, named rather than glossed:

  1. **The cap bounds *retained* memory, not *transient*.** `json.Unmarshal` materialises the full `Description` string before the truncation runs, so a 4 MiB `task_started` line still allocates 4 MiB transiently — and the new second decode makes that allocation happen twice for this one subtype. That is unchanged in kind from today (the first `streamLine` decode already runs on every line) and the real line-level ceiling remains `defaultMaxParseBuf` at 4 MiB (`parser.go:19`). AC2's requirement is about what "enters the event stream, a queue, or a log" — i.e. retained and forwarded — and the design meets it at 4864 bytes. Tightening the transient path would mean streaming-decoding the line, which is disproportionate to a locally-spawned child and out of scope.
  2. **Event *rate* is not bounded by this ticket**, and is not newly exposed either: the emission rate is claude's line rate, which already governs every other mapped variant, and `stream_turn_busy.go`'s whitelist means these events cannot wedge a turn open.
- **[Error messages, logs, telemetry]** No MUST FIX. MUST-NOT-log: `Description`, `TaskID`, `ToolCallID`, `TaskType` — the package's standing rule (`emitUnrecognized` logs site, type, byte count only). MUST-log: nothing new on the success path; the decode-failure and unmapped-subtype drops log the subtype string only. The subtype is claude-derived but is a message-name keyword, not payload — the same class as `sl.Type` in the existing drop log at `:276`, so this introduces no new category of logged content. Enforced by test, not by comment (`TestParser_TaskStartedDropIsLoggedContentFree` sweeps **every** captured record's message and attrs for the description text, so a leak into some other record on the same path is caught too). Verified that the two `cmd/pyry/interactive_turn_v2.go` default arms this event will reach log `eventKind(ev)`, which returns the literal `"unknown"` — content-free.
- **[Concurrency]** No findings. No lock is taken, so lock ordering is vacuous. No shared state is read or mutated, so there is no TOCTOU surface. No goroutine is spawned, so there is nothing to leak. The single-writer invariant (`parser.go:130-136`) is inherited unchanged and the parser stays turn-stateless, so a mid-write signal loses at most the in-flight line — the pre-existing behaviour, with no new partial state to recover.
- **[Threat model alignment]** `docs/protocol-mobile.md` § Security model applies only once these events reach the wire, which is #1377 — **named as the ticket that picks it up**, not silently deferred. The one threat that lands *here* is the envelope-size cap, addressed above with arithmetic. The daemon-local threat — claude's output influencing daemon control flow — is addressed by the design's central property: no field on this event is ever branched on, only carried.

**Reviewer:** architect (self-review per `architect/security-review.md`)
**Date:** 2026-08-07
