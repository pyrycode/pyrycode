# #1382 — Map claude's `system/task_updated` subtype to a bounded daemon event

**Size:** S (confirmed, not overridden). **Labels:** `security-sensitive`, `needs-real-claude`.

Sibling of #1380 (`task_started`, merged) and #1381 (`background_tasks_changed`, open). This ticket adds one arm to the dispatch #1380 built, one variant to the `turnevent` sum type, and one cap constant. It touches two production files.

---

## Files to read first

Read these before writing anything. This list is the turn-1 data load; everything the design references is here with a line range and what to take from it.

| Path | What to extract |
|---|---|
| `internal/streamsup/parser.go:21-66` | The three existing cap constants (`maxUnrecognizedRaw`, `maxTaskFieldID`, `maxTaskDescription`) and — more important — the **comment style** for a cap: observed size, multiple, envelope arithmetic against 65519, escaping note. `maxTaskPatch`'s comment must match this shape. |
| `internal/streamsup/parser.go:243-269` | `streamLine` (the segmentation struct — do **not** widen it) and `systemTaskStartedLine` (the per-subtype decode target this ticket's mirrors). Note the doc's "field set is exactly what the capture shows and nothing invented" and the named drops. |
| `internal/streamsup/parser.go:340-364` | `consumeLine`'s `default:` arm — where the `sl.Type == "system"` guard and the `emitSystemSubtype` call already live. **No edit here.** |
| `internal/streamsup/parser.go:367-389` | `emitSystemSubtype` — the one enumeration site. This ticket adds exactly one `case`. Read its doc: it explains why unknown subtypes fall through to silence. |
| `internal/streamsup/parser.go:391-448` | `emitBackgroundTaskStarted` — the function to mirror. Take: the top-level-`line` decode argument, the undecodable path's content-free Debug + `return true`, the `bound` closure, the sequential-statement ordering rationale. |
| `internal/streamsup/parser.go:450-467` | `truncateField` — reused as-is. Note the `<=` boundary and the **empty** replacement in `strings.ToValidUTF8` (a mid-rune cut *deletes* the partial rune, so a cut value can land 1–3 bytes under the cap). Its doc's "the scrub is a no-op on the untruncated path" claim needs a correction — see § Truncation semantics. |
| `internal/turnevent/event.go:74-125` | `BackgroundTaskStarted` — the doc structure to mirror (name rationale, the deliberate-drops list, the bounded-at-construction paragraph) and `TruncatedFields`' contract. |
| `internal/turnevent/event.go:172-206` | `Unrecognized`'s doc and struct. The load-bearing sentence is at `:191-193` — *"Raw is a plain string, not `json.RawMessage`, because the producer truncates it at construction: a truncated blob is no longer valid JSON, so typing it as raw JSON would be a lie."* This is the precedent that settles `Patch`'s type. |
| `internal/turnevent/event.go:23-30, 216-236` | The `Event` interface doc (lists the variants — needs the new name) and the two marker blocks (`isTurnEvent()` + `_ Event = …`). |
| `internal/streamsup/capture_test.go` (whole file, 80 lines) | `capturedSystemLine(t, subtype)` — **reuse, do not write a second reader.** Its doc already names #1382 as an intended caller. |
| `internal/streamsup/parser_test.go:498-541` | `TestParser_IgnoredLineTypesStaySilent` — the silence table. Line 532's `task_updated` row is deleted here and the comment at 529-531 rewritten. See § Comment obligations. |
| `internal/streamsup/parser_test.go:543-587` | `taskStartedCapCheat` (literal cap fixtures — follow the rule), `taskStartedLineFixture`, `taskStartedEvent`. The new fixtures mirror these. `taskFieldIDCapFixture` (256) is reused, not redeclared. |
| `internal/streamsup/parser_test.go:589-672` | `TestParser_TaskStartedMapsFromCapture` — the derive-don't-pin rule and, critically, the **reflection drop sweep with the `swept` guard** at 649-671. This ticket inherits that mechanism. |
| `internal/streamsup/parser_test.go:674-828` | `TestParser_TaskStartedFieldCaps` — the cap table's row shape, the declaration-order row, the exactly-at-cap row, the mid-rune subtest. |
| `internal/streamsup/parser_test.go:830-~920` | `TestParser_TaskStartedDropIsLoggedContentFree` — the `logRecorder` sweep over every log record, driven on both the success and the decode-failure path. Mirror it. |
| `internal/e2e/realclaude/testdata/dropped_lines_v2.1.220.json` | The capture. The one `task_updated` record is at `dropped_lines[15]`. Do not read it by hand-copying the payload into a fixture — go through `capturedSystemLine`. |
| `docs/knowledge/codebase/1380.md` | The predecessor's decisions, especially the two deliberate key drops and why the drop proof is a reflection sweep. |

*Codegraph note:* `codegraph_context` returned only `Parser` and `emitSystemSubtype` for this task — the #1380 symbols (`emitBackgroundTaskStarted`, `systemTaskStartedLine`, `truncateField`) merged hours ago and are not yet in the index. The table above was completed by reading. Do not conclude from a thin codegraph result that those symbols are absent.

---

## Context

`internal/streamsup`'s parser drops every `system` line through `ignoredLineTypes`. #1380 changed that for one subtype: it added a subtype match *inside* the drop branch, mapped `system/task_started` to `turnevent.BackgroundTaskStarted`, and bounded every claude-derived string at construction. Two mechanisms landed with it — subtype dispatch, and a construction-time cap — and both siblings ride them.

This ticket maps the second scalar subtype. The capture (`claude 2.1.220`, `is_capture: true`, census re-verified 2026-08-07: `task_updated` ×1) shows:

```
system/task_updated   task_id, patch{is_backgrounded}, uuid, session_id
```

170 bytes on the wire; `task_id` is 9 bytes, `patch` serializes to 24.

Two of those four keys are already decided: `session_id` and `uuid` are **not carried** (#1380 — claude's session identity is not the daemon's conversation identity; claude's per-line message id has no daemon reader). The mapped set here is `task_id` and `patch`.

The new question is `patch`: an object, observed with one key, whose future keys are claude's to choose. That is a typing decision, and it is what separates this ticket from #1380's six plain strings.

---

## Design

### 1. The event variant — `turnevent.BackgroundTaskUpdated`

New variant of the sealed `Event` sum type in `internal/turnevent/event.go`, placed immediately after `BackgroundTaskStarted`.

```go
type BackgroundTaskUpdated struct {
    TaskID          string
    Patch           string
    TruncatedFields []string
}
```

Contract per field:

- **`TaskID`** — claude's `task_id`, the join key to the `BackgroundTaskStarted` that opened the task. Same name, no translation.
- **`Patch`** — claude's `patch` object, serialized, carried **whole and unparsed**. A `string`, not `json.RawMessage` — see § 2. Empty when claude omits the key (absence is claude's to choose, not an error; `""` and `"{}"` stay distinguishable for free).
- **`TruncatedFields`** — the cut report, daemon field names in **declaration order** (`"task_id"` before `"patch"`), `nil` when nothing was cut. Neither name is translated here (contrast `BackgroundTaskStarted`'s `tool_use_id` → `"tool_call_id"`); do not invent a translation.

Plus the two membership lines (`func (BackgroundTaskUpdated) isTurnEvent() {}` and `_ Event = BackgroundTaskUpdated{}`) and the variant name added to the `Event` interface doc at `event.go:23-30`.

The doc comment must carry, mirroring `BackgroundTaskStarted`'s structure: the name-is-the-daemon's rationale; the two deliberate key drops with their reasons; the bounded-at-construction paragraph naming `maxTaskPatch` / `maxTaskFieldID`; and the `Patch`-is-a-string paragraph from § 2. It opens and closes no turn — same as its sibling, and the reason the turn-busy tracker's `default:` arm is the correct behaviour unexamined.

### 2. Why `Patch` is a `string` and not `json.RawMessage`

Two separate decisions, and conflating them is the trap:

**At the decode boundary, the bytes are raw.** `systemTaskUpdatedLine.Patch` is a `json.RawMessage`, because that is what carries claude's object verbatim without parsing it. Decoding into `map[string]any` and re-marshalling would be lossy in two ways that matter: it normalizes key order, and it rounds every number through `float64` (a large integer id in a future patch loses precision). `streamMessage.Content`'s existing doc states the same reasoning for the same reason — declaring a shape *discards every unknown field*, which is precisely what a key-enumerating `patch` mapping would do.

Verified: `json.RawMessage`'s `UnmarshalJSON` **copies** its input, so the value does not alias the parser's `p.buf`. There is no retention hazard from `line` being a subslice of the buffer.

**At the event boundary, the value is a string.** `Unrecognized.Raw` settled this and the reasoning transfers verbatim: *the producer truncates at construction, and a truncated blob is no longer valid JSON, so typing it as raw JSON would be a lie.* A bounded `patch` is a string with a truncation flag.

So the pipeline is: `line []byte` → `json.RawMessage` (verbatim) → `truncateField` → `string` on the event. This is the same shape `emitUnrecognized` already has (`raw []byte` → `truncateRaw` → `Raw string`), one field down.

### 3. The cap — `maxTaskPatch = 4 << 10`

Neither existing constant fits, and the number needs an argument rather than a copy.

**Not `maxTaskFieldID` (256).** That caps machine-generated identifiers, which have a bounded format. `patch` has none. 256 bytes is roughly three short keys; a patch carrying one prose-ish value (an error string, a status message) would be cut on arrival. An identifier cap applied to a non-identifier.

**Not a multiple of the observation.** The observed patch is 24 bytes with one key. `maxTaskDescription` could anchor on its observation (~126 bytes real, ×32) because a command line's size distribution is something we can reason about. A single-key patch tells us nothing about a two-key patch. The observation is too weak an anchor to multiply, so the binding constraint has to come from the other side: the envelope.

**Envelope arithmetic** (the form `maxTaskDescription`'s comment uses — reproduce it in the constant's comment):

- Worst case, one `BackgroundTaskUpdated` carries `maxTaskFieldID + maxTaskPatch` = `256 + 4096` = **4352 bytes** of claude-derived text.
- That is **6.6%** of the v2 application-envelope cap of 65519 bytes (`docs/protocol-mobile.md` § Application-envelope size cap) — deliberately in line with `BackgroundTaskStarted`'s 4864 bytes / 7.4%, so the event family has one worst case a reader can hold rather than a per-variant number to re-derive.
- Escaping: `patch` is already JSON text, so on the way out its control characters arrive pre-escaped as printable pairs and the growth is quotes and backslashes, not a `\u00XX` expansion of every byte (`maxUnrecognizedRaw`'s argument, which applies here verbatim). Pathological all-quote content roughly doubles it — ~8.7 KB, 13.3% of the envelope. Still comfortable.
- 4096 is a quarter of `maxUnrecognizedRaw`'s whole-line 16 KiB, which is the ordering that must hold: **one field of one known line must not approach the cap on an entire unknown line.**

**A separate constant even though it equals `maxTaskDescription`.** They bound different fields for different reasons. Folding them into one would make a future change to the command-line budget silently move the patch budget. The test pins each independently as a literal (see § Testing), which is the mechanism that makes them genuinely separate.

### 4. Dispatch and decode

**One new arm** in `emitSystemSubtype` (`parser.go:382-389`):

```go
case "task_updated":
    return p.emitBackgroundTaskUpdated(line)
```

Nothing else in `consumeLine` changes. `system` stays on `ignoredLineTypes`, so `TestParser_IgnoredLineTypesIsTheMeasuredSet`'s `reflect.DeepEqual` pin on `{system, rate_limit_event}` stays green untouched — it pins top-level types, not subtypes — and "no `system` line reaches the unrecognized lane" stays structural rather than asserted.

**New decode target**, beside `systemTaskStartedLine`:

```go
type systemTaskUpdatedLine struct {
    TaskID string          `json:"task_id"`
    Patch  json.RawMessage `json:"patch"`
}
```

Two fields, which is the whole mapped set. `uuid` and `session_id` are absent from the *decode target itself* — a stronger guarantee than the test's sweep, since a field that is never declared cannot leak. Say so in the doc comment, as `systemTaskStartedLine`'s does.

Note the asymmetry and state it in the comment: `TaskID` stays a `string`, so a non-string `task_id` still fails the whole decode and takes the undecodable path (this is what the test's malformed fixture relies on). `Patch` as `json.RawMessage` accepts **any** valid JSON value — a string, a number, `null`. That permissiveness is deliberate: "whatever claude puts there" is the point, and inventing a validation rule for a shape we have one observation of is exactly what #1380 declined to do for missing fields.

**New emit function**, mirroring `emitBackgroundTaskStarted`:

`func (p *Parser) emitBackgroundTaskUpdated(line []byte) bool` — decodes `line` (the **top-level bytes**, never a nested field), emits one `BackgroundTaskUpdated`, reports `true` either way.

- The `line` argument is load-bearing for the same reason it is on the sibling: decoding from a nested field would make a background-task event forgeable out of claude's own tool output. Restate the reason in the doc; do not just repeat the call shape.
- Undecodable payload → content-free `Debug` naming the subtype keyword only (`"subtype", "task_updated"`), **no event, no `Unrecognized`**, `return true`. Surfacing a malformed line of a known subtype in the unrecognized lane would break the structural guarantee above.
- Bound both fields via the same local `bound` closure shape the sibling uses, as **sequential statements** rather than a composite literal, so `TruncatedFields`' order is visible to a reader rather than resting on Go's left-to-right operand rule.

**Do not extract a shared bounding helper.** Two call sites with a six-line closure do not earn an abstraction, #1381's payload is a different shape (an array) that would not fit it anyway, and refactoring `emitBackgroundTaskStarted` would put #1380's tests in this PR's blast radius for no gain. Keep the diff additive.

### 5. Truncation semantics — one genuinely new property

`truncateField` is reused unchanged. But its doc currently claims the UTF-8 scrub is *"a no-op in practice on the untruncated path: `encoding/json` already replaces invalid input bytes with U+FFFD on decode."*

**That claim is false for a `json.RawMessage`-sourced value, and `Patch` is the first one.** Verified empirically: given a line whose `task_id` and `patch` both carry a raw `0xff` inside a JSON string value, the decoder replaces the byte with U+FFFD in the `string` field (result valid UTF-8) but copies it verbatim into the `RawMessage` (result invalid UTF-8). So for `Patch` the scrub is load-bearing even when nothing is truncated.

Two obligations follow:

1. Amend `truncateField`'s doc to scope the no-op claim to string-decoded fields and name `Patch` as the exception. Comment-only; no behaviour change.
2. State the consequence on `BackgroundTaskUpdated.Patch`'s doc: because the scrub uses an **empty** replacement, invalid bytes are *deleted*, and `TruncatedFields` reports the **cap cut only** — not scrub removals. A `Patch` can therefore differ from claude's bytes without being listed as truncated.

That second point is a deliberate, stated limitation, not an oversight. Changing `truncateField`'s return contract to also report scrub removals would drag #1380's tests into this PR, and a consumer cannot act differently either way — the value is a display blob whose JSON validity is already not guaranteed.

---

## Concurrency model

Unchanged, and worth stating because it is what makes this ticket cheap. `Parser.Write` is driven serially by `os/exec`'s single stdout-forwarding goroutine (the single-writer invariant documented at `parser.go:185-191`). The parser is turn-stateless; the new arm adds no field to `Parser`, no goroutine, no lock, and no cross-line state. Each line maps independently.

Downstream: `BackgroundTaskUpdated` opens and closes no turn. `cmd/pyry/stream_turn_busy.go:153-161`'s `default:` arm is the correct unexamined behaviour — exactly as `Unrecognized` and `BackgroundTaskStarted` already are.

---

## Error handling

| Condition | Behaviour | Why |
|---|---|---|
| Line does not decode into `systemTaskUpdatedLine` (e.g. numeric `task_id`) | Content-free `Debug` naming the subtype keyword only; no event; `return true` (consumed) | Keeps `system` structurally unable to reach the unrecognized lane. A malformed line of a *known* subtype is not news. |
| `patch` absent | `Patch` is `""`; no error, no report | Absence is claude's to choose; #1380 set this rule and there is no captured negative case to justify a validation rule. |
| `patch` is a non-object JSON value | Carried as its serialized text | Deliberate permissiveness; the cap is what makes it safe, not a shape check. |
| `task_id` over `maxTaskFieldID` | Cut to 256 bytes, `"task_id"` appended to `TruncatedFields` | |
| `patch` over `maxTaskPatch` | Cut to 4096 bytes, `"patch"` appended; result is a `string` that is no longer valid JSON | Which is the whole reason the field is not typed `json.RawMessage`. |
| Both over cap | Both named, `"task_id"` then `"patch"` | Declaration order, pinned. |
| Invalid UTF-8 inside `patch` | Deleted by the scrub; **not** reported in `TruncatedFields` | Stated limitation, § 5. |

No new failure mode reaches a caller: every path either emits one event or drops silently, and one bad line never affects the next.

---

## Testing strategy

All of it lands in `internal/streamsup/parser_test.go` and runs under `make check`. The capture is read by relative path through the existing `capturedSystemLine`; no build tag, no credentials. Keeping the central assertion out of `make e2e-realclaude` is deliberate — that suite SKIPs (exit 0) with no claude login, so a green run there would be no evidence at all.

### Silence table — move the row, don't delete it

`TestParser_IgnoredLineTypesStaySilent`: delete the `system/task_updated` row at line 532 and rewrite the comment above it (see § Comment obligations). Every other row stays, including `background_tasks_changed` (#1381's), `task_notification` (necessarily synthesized — the capture's `expected_absent` holds exactly it), the never-seen subtype, `init`, `thinking_tokens`, `status`, and `rate_limit_event`. **A red here is the expected first signal of the change, not a bug.** (AC3.)

### `TestParser_TaskUpdatedMapsFromCapture` (AC1)

Driven from `capturedSystemLine(t, "task_updated")` through the shipped parser; expects exactly one `BackgroundTaskUpdated`.

- `TaskID` equals the capture's own `task_id`, **derived from the decoded payload, not pinned** — the capture is redacted, so a pinned expectation risks pinning a placeholder. (`task_id` itself is not redacted, but the derive-don't-pin rule is the family's and still catches the failure worth catching: a field swap.)
- `Patch` re-decodes to a value `reflect.DeepEqual` to the payload's own `patch` — the semantic carriage assertion, immune to key ordering.
- One literal canary: `Patch` is exactly `{"is_backgrounded":true}`. This proves the reader selected the `task_updated` record rather than another `system` line that happens to decode into the same two-field shape — the role `task_type == "local_bash"` plays for the sibling.
- `TruncatedFields` is `nil` — 170 bytes on the wire, far under every cap.
- **The drop sweep**, inherited from `TestParser_TaskStartedMapsFromCapture:649-671`: reflect over **every string field** on the event, for each of the capture's own `session_id` and `uuid` values, failing if any field carries either. Both values read from the decoded payload, and a `t.Fatalf` if either is empty so the assertion cannot go vacuous.
- **The sweep guard, sharpened.** #1380's floor is `swept < len(routes)*2`, which fits only because its route count equals its string-field count. Here there is one route (`TaskID`) but two string fields (`TaskID`, `Patch`), so `len(routes)*2` would be a floor of 2 against an actual 4 — it would pass even if the sweep visited only one field. Use a literal floor of `2 string fields × 2 dropped keys = 4`, with a comment noting that a later ticket adding a field only raises the count, while a field *leaving* string kind drops it below the floor and goes red — which is exactly the weakening worth catching.

### `TestParser_TaskUpdatedCarriesPatchWhole` (AC1's "whole rather than key-enumerated")

**The capture cannot prove this clause, and that needs saying out loud.** Its `patch` has exactly one key, so an implementation that declared `struct{ IsBackgrounded bool }` and re-marshalled would satisfy every capture-derived assertion above. This is the same shape as the ticket's own "the capture proves the mapping and cannot prove the bound" argument, one level down: whole-ness needs a second input.

A synthesized line is legitimate here under AC1's own carve-out. What AC1 forbids is *invented field structure* — a mapping claiming claude sends a field it does not. This design claims **nothing** about patch-internal keys; it carries them unparsed. A synthesized multi-key patch therefore asserts the opposite of an invented mapping: that unknown keys survive un-mapped. The line-level keys stay exactly the capture's (`type`, `subtype`, `task_id`, `patch`).

Scenarios:

- A patch carrying `is_backgrounded` **plus** keys claude has never shown (a string, a nested object, an array) round-trips to a `reflect.DeepEqual` value. A key-enumerating implementation drops the unknown keys and goes red.
- A patch carrying a large integer (e.g. `12345678901234567890`) keeps its digits verbatim in `Patch`. This is the row that falsifies a `map[string]any` decode-and-re-marshal implementation, which would round it through `float64`. Byte-level assertion, not `DeepEqual`.
- `patch` absent from the line → `Patch == ""`, `TruncatedFields == nil`, still exactly one event.

### `TestParser_TaskUpdatedFieldCaps` (AC2)

Table-driven, mirroring `TestParser_TaskStartedFieldCaps`. Needs one new **literal** cap fixture, `taskPatchCapFixture = 4096`, added beside `taskStartedCapCheat`'s two — deliberately not referencing `maxTaskPatch`, so halving the constant goes red instead of dragging the test green with it. `taskFieldIDCapFixture` (256) is reused, not redeclared.

Needs a fixture builder taking the patch as raw JSON (so a test can hand it an oversized object) and a one-event extractor, both mirroring `taskStartedLineFixture` / `taskStartedEvent`.

Rows:

- Over-cap `patch` → `len(Patch) == 4096`, `TruncatedFields == ["patch"]`, `TaskID` intact and **unnamed** (a cut must not smear across the event).
- Over-cap `task_id` → `len(TaskID) == 256`, `["task_id"]`, `Patch` intact.
- Both over cap → `["task_id", "patch"]`, in that order — the declaration-order pin.
- Both exactly at their caps → `TruncatedFields == nil`, nothing cut (the `<=` boundary).
- **The typing proof.** On the over-cap-`patch` row, assert `json.Valid([]byte(ev.Patch))` is **false**. Build the fixture so this is deterministic rather than incidental: a patch object whose single long string value runs past 4096 bytes, so the cut lands inside a string literal and leaves it unterminated. This is the assertion that makes "truncated to a string, not raw JSON" falsifiable instead of decorative — it is the concrete evidence for AC2's last sentence.
- Mid-rune cut: an over-cap patch whose string value ends in multi-byte runes yields valid UTF-8, `len(Patch) <= 4096` (short of it, not at it — the empty replacement deletes the partial rune), and `["patch"]` reported.

### `TestParser_TaskUpdatedDropIsLoggedContentFree` (Security)

The package's standing rule applied to the new path: nothing derived from claude's output reaches a log. Mirrors `TestParser_TaskStartedDropIsLoggedContentFree`, reusing `logRecorder`.

- Drive **both** paths: the captured line (success) and a malformed line whose `task_id` is numeric so the decode fails. Give the malformed line's patch a distinctive literal value that must appear in no log record — the decode-failure handler is the one most tempted to explain itself.
- Sweep every log record's message and attributes for the patch content and for the capture's `session_id` value.
- Assert the Debug record names the subtype keyword only.

### Untouched and expected green (AC4, `make check` half)

- `TestParser_IgnoredLineTypesIsTheMeasuredSet` (`parser_test.go:997`) — pins top-level types, not subtypes.
- The main table's `unknown top-level type surfaces` row (`parser_test.go:157-167`) — a genuinely new top-level type still reaches `UnrecognizedLineType`.
- `internal/turnevent`'s tests — no exhaustiveness pin over the variant set exists, confirmed; #1380 added a variant without touching them.
- Every downstream type switch — `internal/turnbridge/outbound.go:129`, `internal/acpbridge/outbound.go:175`, `cmd/pyry/stream_turn_busy.go:160`, `cmd/pyry/interactive_turn_v2.go:256` and `:440` — has a `default:` arm; `cmd/pyry/acp_turn_stream.go:65` falls through to `acpbridge.MapUpdate`'s defensive `!ok` Debug. **Zero consumer edits.** Verified, matching #1380, which touched four files and no consumer.

### AC4's live clause — not dischargeable here

`internal/e2e/realclaude/interactive_stream_liveness_test.go:253` fatals on a single unrecognized event during a normal turn. That assertion must still hold with both scalar subtypes mapped, and **only an operator's live run discharges it.** `make e2e-realclaude` SKIPs with no claude login and a skip exits 0, so a green suite in the dispatch environment is not evidence. The ticket carries `needs-real-claude` for exactly this.

### Do not touch

`internal/e2e/realclaude/` — `ptyrunner_byte_equivalence_test.go`'s `parserIgnoredTypes` mirror and the comments at `ptyrunner_byte_equivalence_test.go:144`, `interactive_stream_unrecognized_test.go:9`, `dropped_line_capture_test.go:1511` and `:159` are **#1379's scope**. All carry `//go:build e2e_realclaude`; nothing in this build goes red because of them.

---

## Comment obligations (AC5)

AC5 says to verify rather than assume. I swept every Go comment mentioning `task_updated`, `task_started`, `task_notification`, `background_tasks_changed`, or `#1382`. Result — **one site to edit**, and the reasoning for each site left alone:

| Site | Verdict |
|---|---|
| `parser_test.go:529-531` | **EDIT.** Reads *"The two siblings … mapping task_updated is #1382 and background_tasks_changed is #1381, neither of them here, so both must still be silent."* False the moment the row moves. Rewrite to cover `background_tasks_changed` / #1381 only. |
| `parser.go:86-115` (`ignoredLineTypes`) | No edit. #1380 collapsed the enumeration; this comment restates no set, it points at `emitSystemSubtype`. Line 95's *"two siblings extend it (#1381, #1382)"* is the justification for not restating — this ticket makes that argument stronger, not false. Line 107-110's still-dropped statement is defined *by reference* to `emitSystemSubtype` and names `task_notification`, which stays dropped. |
| `parser.go:367-381` (`emitSystemSubtype`) | No edit. Its doc says the case arms are the one enumeration; adding an arm *is* the update. |
| `parser.go:340-360` (`consumeLine`'s `default:`) | No edit. Points at `emitSystemSubtype`, restates no set. |
| `turnevent/event.go:172-193` (`Unrecognized`) | No edit. Same — prose pointer, no set. |
| `capture_test.go:47-49` | No edit. *"#1381 … and #1382 … map the sibling subtypes out of the same file"* becomes present tense for one of them; it names no mapped-or-dropped set. |
| `parser_test.go:643-646` | No edit. *"a field added later — by #1381 or #1382 extending this event family — is covered"* describes the sweep mechanism, not the mapped set. This ticket adds a **peer variant carrying its own identical sweep** rather than fields to `BackgroundTaskStarted`, which is the design intent the sentence records, so the PR does not falsify it. Leaving it also keeps #1380's test out of this diff. |

Two further comment edits this design *mandates* (not AC5 sites, but obligations):

- `truncateField`'s doc — scope the "scrub is a no-op on the untruncated path" claim, which `Patch` falsifies (§ 5).
- `turnevent/event.go:23-30` — the `Event` interface doc lists the variants and needs the new name.

---

## Open questions

1. **Does `patch` ever arrive as a non-object?** One observation says object. The design tolerates any JSON value rather than validating, so nothing blocks on this — but if a second capture ever shows a scalar `patch`, that is worth a note in `codebase/<N>.md` rather than a code change.
2. **Is 4096 right if `patch` grows into a whole task record?** The escape hatch is the truncation report, which makes an under-sized cap visible rather than silent. `#1381`'s array bound is the ticket that would surface this first, since a roster element is the same content at greater volume. Revisit then, not now.
3. **`TruncatedFields` does not report scrub removals** (§ 5). Stated limitation. If a consumer ever needs byte-fidelity signalling, that is a change to `truncateField`'s contract across the whole family — a separate ticket, not a local patch here.

---

## Security review

**Verdict:** PASS

**Findings:**

- **[Trust boundaries]** No findings. The boundary is explicit and single: `emitBackgroundTaskUpdated` is the only place `system/task_updated` bytes become daemon state, and it decodes from `line` — the **top-level bytes** — never from a nested field. That is what keeps the event unforgeable out of claude's own tool output, and it is the property `streamLine`'s doc already states for turn boundaries. Downstream holds a `turnevent.BackgroundTaskUpdated` whose every string is producer-bounded; the doc comment says so at the type, which is the convention signal consumers read. The decode target declares two fields, so the untrusted line's other keys cannot cross the boundary at all.
- **[Tokens, secrets, credentials]** No findings, and one deliberate non-carry. claude's `session_id` is session identity — not a credential, but also **not the daemon's conversation identity**, and a field of that name would invite a consumer or a later wire mapper to route on it. It is absent from the decode target, absent from the event, and the absence is enforced by the reflection sweep rather than by review. `task_id` is claude's opaque handle, not an authorization token — nothing in the daemon grants access on it, and #1377/#1262 (the wire and ACP surfaces) are the tickets that would have to keep it that way.
- **[File operations]** N/A by design. The only file this ticket reads is the committed capture, and only from a test, by a constant relative path (`capture_test.go:20`) with no caller-controlled component. No production path opens, stats, or writes a file.
- **[Subprocess execution]** No findings, but one genuine hazard worth naming. `patch` may in future carry command text (its sibling's `Description` already does — `local_bash`'s literal command line). `BackgroundTaskStarted.Description`'s doc states the rule — *safe to RENDER as text, never to execute or re-shell* — and `BackgroundTaskUpdated.Patch`'s doc must carry the same warning, because a patch is the more tempting shape to feed somewhere structured. Nothing in this ticket execs; the risk is entirely in what a future consumer does, so the doc is the correct place for it. **SHOULD FIX:** developer adds that sentence to `Patch`'s doc; code-review checks it is there.
- **[Cryptographic primitives]** N/A. No randomness, no comparison against a secret, no key material anywhere in this path.
- **[Network & I/O]** No findings — and this category is the ticket's substance rather than an afterthought. Input size is capped at construction, before the value enters the event stream, a queue, or a log: `maxTaskFieldID` (256) on `task_id`, `maxTaskPatch` (4096) on `patch`, worst case 4352 bytes = 6.6% of the 65519-byte v2 application envelope, ~13.3% under pathological escaping (§ 3). The whole-line accumulator is separately bounded by `defaultMaxParseBuf` (4 MiB, `parser.go:19`), so an unterminated line cannot exhaust memory ahead of the field caps. The permissive `json.RawMessage` decode is what makes the cap load-bearing rather than cosmetic — it is the *reason* to bound, exactly as the ticket's security note says.
- **[Error messages, logs, telemetry]** No findings, with one new leak surface closed by test. The standing rule is that nothing derived from claude's output reaches a log; the undecodable path logs the subtype **keyword** only (a message name, the same class as `sl.Type` in the existing drop log), and none of the decoded fields. `patch` is the most attractive thing to log while debugging a malformed line, which is why `TestParser_TaskUpdatedDropIsLoggedContentFree` drives **both** the success and the decode-failure path and sweeps every record's message and attributes. Content crosses the wire, not the log.
- **[Concurrency]** No findings. No new goroutine, no lock, no shared mutable state; `Parser.Write` keeps its documented single-writer invariant (`parser.go:185-191`) and the parser stays turn-stateless. There is no check-then-mutate here — the mapping is a pure function of one line. Verified that `json.RawMessage` copies its input rather than aliasing `p.buf`, so no value outlives the buffer it was decoded from.
- **[Threat model alignment]** Aligned. The relevant constraint is `docs/protocol-mobile.md` § Application-envelope size cap, addressed above with explicit arithmetic. Carrying these events onto the mobile v2 wire is **out of scope — #1377**; the desktop ACP surface is **#1262**. This ticket adds no protocol type, payload struct, or route, so no wire-side threat is opened or deferred silently here.

**Reviewer:** architect (self-review per `architect/security-review.md`)
**Date:** 2026-08-07
