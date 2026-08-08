# #1381 — Map claude's `system/background_tasks_changed` subtype to a size-bounded daemon event

**Size:** S (confirmed, not overridden). **Labels:** `security-sensitive`, `needs-real-claude`.

Third and last child of #1378, after #1380 (`task_started`, merged) and #1382 (`task_updated`, merged). It adds one arm to the dispatch #1380 built, two variants to the `turnevent` package, and two cap constants. It touches two production files and two test files, and no consumer.

---

## Files to read first

This list is the turn-1 data load. Everything the design references is here with a line range and what to take from it. Line numbers are read off `9bbc540` and were re-verified by reading the tree on 2026-08-08; re-check before trusting any of them.

| Path | What to extract |
|---|---|
| `internal/streamsup/parser.go:21-106` | The four existing cap constants and — more important — the **comment shape** for a cap: observed size, the multiple, envelope arithmetic against 65519, the escaping note, the ordering against `maxUnrecognizedRaw`. `maxTaskPatch:68-106` is the closest model. Note its family-doctrine sentence at `:89-92`; § 2 below is the decision that qualifies it. |
| `internal/streamsup/parser.go:108-160` | `ignoredLineTypes`' doc. `:132-137` is the `CORRECTED 2026-08-07 (#1380)` block; `:135`'s *"two siblings extend it (#1381, #1382)"* is comment-obligation site 3. `:147-150`'s still-dropped statement stays true — no edit. |
| `internal/streamsup/parser.go:294-338` | `systemTaskStartedLine` and `systemTaskUpdatedLine` — the two decode targets this ticket's mirrors. Take the "field set is exactly what the capture shows and nothing invented" doc rule and the named structural drops. |
| `internal/streamsup/parser.go:386-434` | `consumeLine`'s `default:` arm — the `sl.Type == "system"` guard and the `emitSystemSubtype` call. **No edit here.** |
| `internal/streamsup/parser.go:436-460` | `emitSystemSubtype` — the ONE enumeration of the mapped set. This ticket adds exactly one `case`, and that addition is what discharges AC5's enumeration clause. |
| `internal/streamsup/parser.go:521-580` | `emitBackgroundTaskUpdated` — the function to mirror. Take: the top-level-`line` decode argument and *why*, the undecodable path's content-free `Debug` + `return true`, the `bound` closure, the sequential-statement ordering rationale. |
| `internal/streamsup/parser.go:582-609` | `truncateField` — reused unchanged. Note the `<=` boundary and the **empty** replacement (a mid-rune cut *deletes* the partial rune, so a cut value can land 1–3 bytes under the cap). Its `CORRECTED (#1382)` `json.RawMessage` exception does **not** apply here — every field on these entries decodes into a Go `string`. |
| `internal/turnevent/event.go:75-126` | `BackgroundTaskStarted` — the three carried fields this ticket's entry mirrors, the deliberate-drops list, `TruncatedFields`' contract (declaration order, daemon snake_case names, nil never empty). |
| `internal/turnevent/event.go:128-197` | `BackgroundTaskUpdated` — the doc structure to mirror and the render-never-execute warning on `Patch`, which this ticket's `Description` inherits. |
| `internal/turnevent/event.go:23-31, 288-312` | The `Event` interface doc (lists the variants — needs the new name) and the two marker blocks (`isTurnEvent()` at `:288-298`, `_ Event = …` at `:301-311`). |
| `internal/streamsup/capture_test.go` (whole file, 83 lines) | `capturedSystemLine(t, subtype)` — **reuse, do not write a second reader.** Its doc at `:47-52` is comment-obligation site 2. |
| `internal/streamsup/parser_test.go:498-543` | `TestParser_IgnoredLineTypesStaySilent`. `:535` is the `background_tasks_changed` row that MOVES; `:529-534` is comment-obligation site 1. |
| `internal/streamsup/parser_test.go:545-559` | `taskStartedCapCheat` — the LITERAL cap fixtures and the rule behind them. Two more literals are added here. |
| `internal/streamsup/parser_test.go:561-595` | `taskStartedLineFixture` / `taskStartedEvent` — the fixture-builder and one-event-extractor shapes to mirror. |
| `internal/streamsup/parser_test.go:597-680` | `TestParser_TaskStartedMapsFromCapture` — the derive-don't-pin rule and the reflection drop sweep at `:657-679`. `:651-653` is comment-obligation site 4. |
| `internal/streamsup/parser_test.go:944-1052` | `TestParser_TaskUpdatedMapsFromCapture` — the destination shape, the provenance note at `:948-950` to mirror, and the **literal sweep floor** at `:1041-1051` with the reasoning that replaced `len(routes)*2`. |
| `internal/streamsup/parser_test.go:1054-…` | `TestParser_TaskUpdatedCarriesPatchWhole` — the precedent for a synthesized-input test that AC1's build-from-the-capture rule permits, and the paragraph justifying it. |
| `internal/e2e/realclaude/testdata/dropped_lines_v2.1.220.json` | The capture. The one `background_tasks_changed` record is `dropped_lines[12]` (its own `index` field reads 14). Reach it through `capturedSystemLine`, never by copying the payload into a fixture. |
| `docs/protocol-mobile.md:304` | The v2 application-envelope cap: 65519 bytes. The denominator in every arithmetic below. |
| `docs/knowledge/codebase/1380.md`, `docs/knowledge/codebase/1382.md` | The two predecessors' decisions — the deliberate key drops, why the drop proof is a reflection sweep, and #1382's floor-literal correction. |

*Codegraph note:* `codegraph_context` resolved `Parser`, `truncateField`, `emitSystemSubtype`, `emitBackgroundTaskStarted`, `emitBackgroundTaskUpdated`, `BackgroundTaskStarted` and `BackgroundTaskUpdated` — the index is current through `9bbc540`. The table above adds the test-side and docs-side entries codegraph does not carry.

---

## Sizing note

**S, kept, not re-sliced** — and the raw line count is the one red line worth stating openly rather than discounting. Projected total written work is ~750–850 raw lines across four files, which is *above* the ~600 guideline. The decision rests on measured outcomes of the two nearest analogues rather than on re-counting those lines as "really" fewer:

- `da7cda6` (#1380): 743 insertions, 4 files, 2 production. Merged at `size:s`, no `max_turns`, no salvage.
- `c46ca54` (#1382): 694 insertions, 4 files, 2 production. Same.

This ticket is ~1.2× #1382 (a second exported type, a second cap constant, a loop with two-dimension cut reporting, one extra test). Every *structural* red line is clear: 0 new files, 2 new exported types, 0 consumer call sites (re-verified — every downstream type switch has a `default:` arm), 5 acceptance criteria, 3 reject branches. The package's cost driver is comment density, not logic; § "Prose budget" below caps that explicitly so the 1.2× does not compound.

The one available split — map now, bound the array later — would put an array whose length is claude's to choose onto the event stream in the gap between the children. That is a security regression on a `security-sensitive` ticket and is precisely what the ticket exists to prevent. The mapping, the bound and the proof are one property; there is no other seam.

---

## Context

`internal/streamsup`'s parser drops every `system` line through `ignoredLineTypes`. #1380 changed that for one subtype by matching subtypes *inside* the drop branch; #1382 added the second. Both mapped **scalar** subtypes, whose fixed field set means a per-field text cap bounds the whole event.

This ticket maps the third and last captured subtype, and it is the one whose payload is an **array**:

```
system/background_tasks_changed   tasks[]{task_id, task_type, description}, uuid, session_id
```

The captured line (212 bytes) holds exactly one entry with a 9-byte `description` (`cat $FIFO`). The redaction arithmetic is identical to the one `maxTaskDescription` was costed against: `payload_len_bytes_captured` 354 − `payload_len_bytes` 212 = 142 bytes across the same two sites (`$SESSION_ID`, `$FIFO`), which puts the real description at ~126 bytes.

Two things follow, and keeping them apart is the whole design:

1. **The field mapping comes from the capture and nowhere else.** Three per-entry keys, two line-level keys deliberately dropped.
2. **Neither bound can come from the capture.** One entry at 212 bytes proves nothing about a cap. Both bounds are proven by lines synthesized to exceed them — which AC1 explicitly permits, because such a line invents no field structure.

The genuinely new question is the second dimension: a per-entry text cap alone leaves the event's total size a function of a count claude chooses.

### Cross-record fact worth knowing

All three captured records carry the **same** `task_id` (`bybi8g8i8`) and the same task (`local_bash`, `cat $FIFO`). The roster entry, the `task_started` line and the `task_updated` line describe one task, which is what makes `task_id` genuinely the join key the family's docs claim. § Testing turns that into a falsifiable assertion.

---

## Design

### 1. The event variant — `turnevent.BackgroundTaskRoster` and `turnevent.BackgroundTask`

Two new types in `internal/turnevent/event.go`, placed immediately after `BackgroundTaskUpdated`.

```go
type BackgroundTask struct {
    TaskID          string
    TaskType        string
    Description     string
    TruncatedFields []string
}

type BackgroundTaskRoster struct {
    Tasks        []BackgroundTask
    DroppedTasks int
}
```

**Why `Roster`, not `Changed`.** The name is the daemon's, per the family rule, and this is the one place the translation earns more than claude-rename insulation. claude's `background_tasks_changed` names the *trigger*; the payload is a **snapshot** — the complete set of tasks claude is tracking at that moment. A variant called `…Changed` invites a consumer to read it as a delta, and a consumer reading a snapshot as a delta is one short step from inferring a finish the daemon has never observed. The name is the first line of AC3's defence; the doc comment is the second.

Field contracts:

- **`Tasks`** — the roster in claude's own order, truncated from the tail. `nil` for an empty roster and `nil` when claude omits the `tasks` key. An empty roster is **meaningful and is still emitted**: it says nothing is alive. Dropping the event in that case would discard exactly the signal a consumer needs.
- **`BackgroundTask.TaskID` / `.TaskType` / `.Description`** — declared in the captured entry's own key order, which is what `TruncatedFields` is ordered by. `TaskID` is the join key back to the `BackgroundTaskStarted` / `BackgroundTaskUpdated` events already emitted for the same task. `Description` inherits `BackgroundTaskStarted.Description`'s warning verbatim: for `local_bash` it is the literal command line, **safe to RENDER as text, never to execute or re-shell**.
- **`BackgroundTask.TruncatedFields`** — the text dimension's cut report, per entry, daemon snake_case names in declaration order (`"task_id"`, `"task_type"`, `"description"`), `nil` when nothing was cut. No name is translated here; claude's keys and the daemon's fields agree.
- **`DroppedTasks`** — the count dimension's cut report: how many entries claude sent beyond `maxTaskRosterEntries` that this event does **not** carry. `0` when nothing was dropped. A consumer recovers the roster's true size as `len(Tasks) + DroppedTasks`.

**Each dimension reports at the level where it happens** — text truncation is a property of one entry and rides that entry; count truncation is a property of the whole roster and rides the event. Do not fold the count report into a top-level `TruncatedFields{"tasks"}`: a name-only report loses *how many* were lost, and the count is the strictly more informative signal. There is deliberately no top-level `TruncatedFields` on this variant.

Plus the two membership lines per new-`Event` type (`func (BackgroundTaskRoster) isTurnEvent() {}` and `_ Event = BackgroundTaskRoster{}`) and the variant name in the `Event` interface doc at `event.go:23-31`. `BackgroundTask` is **not** an `Event` — it is an element type and gets no marker.

### 2. The two-dimension bound — and the family-doctrine collision it forces

This is the ticket's central judgement, and it does not close the way the siblings' did.

**The collision, stated plainly.** `maxTaskPatch`'s doc (`parser.go:89-92`) records a doctrine now on main: `BackgroundTaskUpdated`'s 4352-byte worst case was chosen *"deliberately in line with `BackgroundTaskStarted`'s 4864 bytes / 7.4% so the event family has ONE worst case a reader can hold rather than a per-variant number to re-derive."* Reusing the existing caps here puts one roster entry at `maxTaskFieldID + maxTaskFieldID + maxTaskDescription` = `256 + 256 + 4096` = **4608 bytes**, which means:

- **1 entry** is the only count that satisfies the doctrine as written — and a one-entry roster is not a roster.
- **14 entries** consume 64512 bytes = **98.5%** of the 65519-byte envelope.
- **15 entries** exceed it outright.

So the doctrine cannot hold unexamined, and the resolution has to be a decision on the record.

**Resolution: one worst case per SHAPE, not per variant.** The doctrine's stated purpose is legibility — a reader holding one number instead of re-deriving per variant. Its failure here is not accidental: an *aggregate* variant cannot share a *scalar* variant's worst case unless its cardinality is 1. Rather than force a fit by squeezing the observed content, the doctrine is qualified to read: a **scalar** background-task event is ≤ ~4.9 KB; the **roster** is ≤ 8 KiB. Two numbers, one per shape, both a small fraction of the envelope, and the rule stays a rule a reader can hold. This qualification is written into `maxTaskRosterEntries`' doc, and `maxTaskPatch`'s doctrine sentence is left **unedited** — it describes the scalar pair accurately and still does.

**The two constants**, beside the existing four in `parser.go`:

```go
const maxTaskRosterEntries    = 8
const maxTaskRosterDescription = 512
```

**Why a roster-specific description cap rather than reusing `maxTaskDescription` (4096).** Not thrift — the multiplication. This is the one field in the family whose budget gets multiplied by a count claude chooses, and a multiplied field earns a smaller unit budget than the same field carried once. It is also a different *role*: on `task_started` the description is the event's payload, the one thing the event is about; in a roster it is a label in a list whose authoritative full-length copy already crossed the wire on the `BackgroundTaskStarted` this entry's `task_id` joins back to. A cut here loses nothing a consumer holding that event cannot recover, and `TruncatedFields` says it happened. 512 is ~4× the ~126-byte observed real description — a thinner multiple than `maxTaskFieldID`'s 9× or `maxTaskDescription`'s 32×, and deliberately so, for the multiplication reason above.

**The arithmetic, in `maxUnrecognizedRaw`'s style** — reproduce this chain in the constants' comments:

- One entry: `256 + 256 + 512` = **1024 bytes** exactly. A clean unit a reader can hold.
- Worst case one `BackgroundTaskRoster`: `8 × 1024` = **8192 bytes** = **12.5%** of the 65519-byte v2 application-envelope cap. Compare the scalar pair at 4864 / 7.4% and 4352 / 6.6% — larger, per the qualification above, and still a fraction.
- 8192 is exactly **half** of `maxUnrecognizedRaw`'s whole-line 16 KiB, which preserves the package's stated ordering one level up: a whole KNOWN event must not approach the cap on an entire UNKNOWN line.
- Escaping is mild for `maxUnrecognizedRaw`'s reason, which applies verbatim: these are JSON string values, so control characters arrive pre-escaped as printable pairs and the growth is quotes and backslashes, not a `\u00XX` expansion of every byte. Pathological all-quote content roughly doubles it — ~16 KB, ~25% of the envelope. Still comfortable.
- The count itself: the observed roster holds **1** entry, so 8 is 8× the observation — the same multiple-of-observation form `maxTaskFieldID` uses, and the number that makes the product land on a clean 8 KiB. Overflow is reported, not silent, so an under-sized count is visible rather than a lie.

**Literal fixtures, as always.** Both new constants get a LITERAL fixture in `taskStartedCapCheat` (§ Testing). A fixture built from the constant it validates asserts nothing about the number.

### 3. Dispatch and decode

**One new arm** in `emitSystemSubtype` (`parser.go:451-460`), appended after `task_updated` — the two scalar per-task subtypes first, then the aggregate:

```go
case "background_tasks_changed":
    return p.emitBackgroundTaskRoster(line)
```

Nothing in `consumeLine` changes. `system` stays on `ignoredLineTypes`, so `TestParser_IgnoredLineTypesIsTheMeasuredSet` (`parser_test.go:1452`) stays green untouched — it pins **top-level types** by `reflect.DeepEqual`, not subtypes — and "no `system` line reaches the unrecognized lane" stays structural rather than asserted.

**Two new decode targets**, beside the existing two:

```go
type systemBackgroundTasksLine struct {
    Tasks []systemBackgroundTaskEntry `json:"tasks"`
}

type systemBackgroundTaskEntry struct {
    TaskID      string `json:"task_id"`
    TaskType    string `json:"task_type"`
    Description string `json:"description"`
}
```

The field set is exactly the capture's and nothing invented. `uuid` and `session_id` are absent from the **decode target itself** — a stronger guarantee than the test's sweep, because a field never declared cannot leak. Say so in the doc, as both siblings' targets do. All three entry fields are plain `string`, which is why `truncateField`'s `json.RawMessage` exception does not reach this ticket; state that too, so a reader does not go looking for it.

**No `tool_use_id` and no `patch` on these entries.** Mirroring either sibling's field set would invent a key claude does not send on this subtype. `maxTaskPatch` has no application here.

### 4. The emit function

`func (p *Parser) emitBackgroundTaskRoster(line []byte) bool` — decodes `line`, emits exactly one `BackgroundTaskRoster`, reports `true` either way. Peer of `emitBackgroundTaskUpdated`; every structural choice is the same one for the same reason, and the doc should say so rather than re-derive it:

- **The decode's input is `line` — the TOP-LEVEL bytes, never a nested field.** Restate the *reason*, not just the call shape: `streamLine`'s doc states that control shapes are read from the top level only and nested content is never re-scanned, which is what stops a tool result whose text is literally `{"type":"result"}` from forging a turn boundary. Decoding this payload from anywhere else would make a whole background-task roster forgeable out of claude's own tool output — and this variant is the most valuable one to forge, since it is the one that claims what is *alive*.
- **Undecodable payload** → content-free `Debug` naming the subtype keyword only, **no event, no `Unrecognized`**, `return true`.
- **The count bound runs before the loop.** Compute the drop count from `len(tl.Tasks)`, then iterate the surviving prefix. Truncation is **from the tail**, preserving claude's order: no ranking is invented, because claude's ordering semantics are unobserved.
- **The text bound runs per entry**, using the same six-line `bound` closure shape both siblings use — declared **inside** the loop body so `cut` is per-entry, which is the whole reason it cannot be hoisted. Sequential statements rather than a composite literal, for the siblings' reason: `TruncatedFields` is ordered by these calls, and inside a literal that order would rest on Go's left-to-right operand rule rather than on something a reader sees.

**Do not extract a shared bounding helper.** #1382 declined this at two call sites and named this ticket's array shape as the reason it would not fit; that call still holds. Refactoring the siblings' emitters would put #1380's and #1382's tests in this PR's blast radius for no gain. Keep the diff additive.

### 5. What is deliberately NOT built (AC3)

**No terminal, finish, or completion event is synthesized.** The capturing probe deliberately ended its turn with the task still alive, so nothing on record shows a task finishing. A task's disappearance from a later roster is the *available* finish signal, but that transition has never been observed, and the daemon does not report a finish it cannot detect.

Concretely, this forbids all of:

- emitting a `BackgroundTaskFinished`-shaped variant when an entry is absent from a later roster;
- keeping cross-line state in `Parser` to diff successive rosters (the parser is turn-stateless and stays that way — see § Concurrency);
- inferring anything from an empty `tasks` array beyond "the roster is empty", which is what the event already says structurally.

Diffing rosters is a legitimate thing for a **consumer** to do with the snapshot this event carries. It is not the daemon's inference to make, and the `Roster` name plus the type doc are what tell a consumer which side of that line it is on.

### Prose budget

This package's comment density is ~3:1 and it is the cost driver, so spend it where it is load-bearing:

- The two new cap constants get the full arithmetic chain from § 2 — that is the ticket's central judgement and it must be findable at the number.
- Everything else **points** rather than restates. The emit function's doc, the decode targets' docs and the event variants' docs should name their sibling and state only what differs; do not re-derive the family history, the 2026-07-27 measurement, or the drop reasons already written at `BackgroundTaskStarted`.

---

## Concurrency model

Unchanged, and stating it is what keeps this ticket cheap. `Parser.Write` is driven serially by `os/exec`'s single stdout-forwarding goroutine (the single-writer invariant documented at `parser.go:185-191`). The parser is **turn-stateless**: the new arm adds no field to `Parser`, no goroutine, no lock and no cross-line state. Each line maps independently.

That statelessness is not incidental here — it is § 5's enforcement mechanism. Detecting a task's disappearance would require remembering the previous roster, which is exactly the cross-line state this design refuses to add.

Downstream: `BackgroundTaskRoster` opens and closes no turn. `cmd/pyry/stream_turn_busy.go:160`'s `default:` arm is the correct unexamined behaviour, exactly as `Unrecognized` and both sibling events already are.

---

## Error handling

| Condition | Behaviour | Why |
|---|---|---|
| Line does not decode into `systemBackgroundTasksLine` (e.g. `tasks` is an object, or an entry's `task_id` is numeric) | Content-free `Debug` naming the subtype keyword only; no event; `return true` (consumed) | Keeps `system` structurally unable to reach the unrecognized lane. A malformed line of a *known* subtype is not news. |
| `tasks` key absent | `Tasks` is `nil`, `DroppedTasks` 0; one event still emitted | Absence is claude's to choose; #1380 set this rule and there is no captured negative case to justify a validation rule. |
| `tasks` is `[]` | `Tasks` is `nil`, `DroppedTasks` 0; one event still emitted | An empty roster is the signal that nothing is alive. Dropping it would discard the most informative case. |
| An entry omits a field | That field lands empty; no error, no report | Same rule. Not a truncation, so it is not in `TruncatedFields`. |
| `len(tasks)` > `maxTaskRosterEntries` | First 8 entries carried in claude's order; `DroppedTasks` = the remainder | The count bound. Tail truncation preserves order; no ranking is invented. |
| An entry's `task_id` / `task_type` over `maxTaskFieldID` | Cut to 256 bytes; that name appended to **that entry's** `TruncatedFields` | |
| An entry's `description` over `maxTaskRosterDescription` | Cut to 512 bytes; `"description"` appended to that entry's `TruncatedFields` | The full-length copy remains recoverable via `task_id` on the `BackgroundTaskStarted`. |
| Several fields of one entry over cap | Named in declaration order: `"task_id"`, `"task_type"`, `"description"` | Pinned by test. |
| Both dimensions exceeded at once | Independent: surviving entries still report their own text cuts, and `DroppedTasks` still counts the dropped ones | A cut in one dimension must not smear into the other. |
| Invalid UTF-8 in any entry field | Scrubbed with an empty replacement (deleted), **not** reported in `TruncatedFields` | Inherited stated limitation (`truncateField`, and `BackgroundTaskUpdated.Patch`'s doc). Unlike `Patch`, these are string-decoded, so `encoding/json` has already U+FFFD-replaced the input and our own cut is the only mid-rune hazard. |

No new failure mode reaches a caller: every path either emits exactly one event or drops silently, and one bad line never affects the next.

---

## Testing strategy

All of it lands in `internal/streamsup/parser_test.go` and runs under **`make check`**. The capture is read by relative path through the existing `capturedSystemLine`; no build tag, no credentials. Keeping the central assertion out of `make e2e-realclaude` is deliberate — that suite SKIPs (exit 0) with no claude login, so a green run there would be no evidence at all.

### Silence table — move the row, don't delete it

`TestParser_IgnoredLineTypesStaySilent`: delete the `system/background_tasks_changed` row at `:535` and the comment block above it (`:529-534`); every other row stays — `init`, `thinking_tokens`, `status`, the never-seen subtype, `task_notification`, `rate_limit_event`. **A red here is the expected first signal of the change, not a bug.** (AC4.)

After the move the table drives only synthesized lines. That is fine and no row should be re-pointed at the capture to compensate: `capturedSystemLine` requires **exactly one** matching record, and `thinking_tokens` has 33 — it would `t.Fatalf`. Leave the table's rows alone.

### `TestParser_BackgroundTaskRosterMapsFromCapture` (AC1)

Driven from `capturedSystemLine(t, "background_tasks_changed")` through the shipped parser; expects exactly one `BackgroundTaskRoster`. Mirror `TestParser_TaskUpdatedMapsFromCapture`'s structure, including its provenance note (*"This line used to be a row in `TestParser_IgnoredLineTypesStaySilent`"*) — that note is where the deleted comment block's history lands, and the fact that `task_updated` also once sat there is already recorded at `:948-950`, so nothing is lost by deleting rather than preserving the old block.

- `len(Tasks) == 1` and `DroppedTasks == 0` — the shape canary.
- Each of `Tasks[0].TaskID`, `.TaskType`, `.Description` equals the corresponding key of the payload's own `tasks[0]`, **derived from the decoded payload, not pinned** — the capture is redacted, so a pinned expectation risks pinning a placeholder while a derived one still catches a field swap. `t.Fatalf` if any is empty, so no route assertion can go vacuous.
- One literal canary: `Tasks[0].TaskType == "local_bash"`, proving the reader picked a background-task record at all.
- **The cross-record join proof.** Also read `capturedSystemLine(t, "task_started")`, decode it, and assert its `task_id` equals `Tasks[0].TaskID`. This is the only place in the family where the docs' repeated *"`task_id` is the join key"* claim becomes falsifiable rather than decorative, and the capture makes it deterministic (all three records describe one task).
- `Tasks[0].TruncatedFields == nil` — 212 bytes on the wire, far under every cap.
- **The drop sweep, re-derived for this shape.** Reflect for each of the capture's own `session_id` and `uuid` values, failing if any string field carries either. Both values read from the decoded payload with a `t.Fatalf` if empty (neither is: `uuid` is a real UUID and `session_id` is the non-empty placeholder `$SESSION_ID`, so neither premise is vacuous).
- **The traversal is the new work.** Top-level reflection does **not** descend into `[]BackgroundTask`, and this event's carried values live only inside one — `BackgroundTaskRoster` has *zero* top-level string fields. Sweep the event's fields and, for a slice-of-struct field, descend into each element. Count every string field visited at either level.
- **The floor is a LITERAL: `1 entry × 3 string fields × 2 dropped keys = 6`.** Follow #1382's correction (`:1041-1051`) — no expression derived from a route list. The floor is load-bearing in a way the siblings' was not: a top-level-only sweep visits **0** string fields here and would otherwise pass in silence, so this literal is exactly the guard that catches "the reflection never descended". Comment it as such. Re-taking the capture with more entries only raises the count; a field leaving string kind, or the descent regressing, drops below 6 and goes red.

### `TestParser_BackgroundTaskRosterBounds` (AC2)

Table-driven, mirroring `TestParser_TaskUpdatedFieldCaps`. Needs:

- Two new **literal** fixtures beside `taskStartedCapCheat`'s three: `taskRosterEntriesCapFixture = 8` and `taskRosterDescriptionCapFixture = 512`. Deliberately not referencing the constants, so halving either goes red instead of dragging the test green with it. Extend the const block's doc to cover the count fixture — it is the first fixture pinning a *cardinality* rather than a byte length, and the same reasoning applies for the same reason.
- A fixture builder taking an entry count and per-entry field values (so a row can hand it 9 entries, or one 5000-byte description), and a one-event extractor, mirroring `taskStartedLineFixture` / `taskUpdatedEvent`. It invents no field structure: the line-level and entry-level keys are exactly the capture's.

Rows — the count dimension:

- 9 entries → `len(Tasks) == 8`, `DroppedTasks == 1`, and the **first 8 in order** survive (assert an identifying field per entry, so tail-truncation is pinned rather than assumed).
- Exactly 8 entries → all 8 carried, `DroppedTasks == 0` — the boundary, matching `truncateField`'s `<=` convention.
- A large roster (e.g. 100 entries) → `len(Tasks) == 8`, `DroppedTasks == 92`. This is the row that makes the report a *count* rather than a flag.
- Empty `tasks` array, and `tasks` key absent → exactly one event, `Tasks` nil, `DroppedTasks == 0`. Both cases; they are different inputs.

Rows — the text dimension:

- Over-cap `description` on one entry → `len(Description) == 512`, that entry's `TruncatedFields == ["description"]`, its other fields intact and unnamed.
- Over-cap `task_id` / `task_type` → cut to 256, named, `description` intact.
- All three over cap on one entry → `["task_id", "task_type", "description"]` in that order — the declaration-order pin.
- All three exactly at their caps → `TruncatedFields == nil` (the `<=` boundary).
- Mid-rune cut: an over-cap description ending in multi-byte runes yields valid UTF-8 and `len(Description) <= 512` (short of it, not at it — the empty replacement deletes the partial rune), with `"description"` reported.

Rows — the dimensions crossing:

- 9 entries where an entry **in the surviving prefix** also has an over-cap description → `DroppedTasks == 1` **and** that entry's `TruncatedFields == ["description"]`. This is the row that proves neither report smears into the other, and it is the concrete evidence for AC2's "in both dimensions".

### `TestParser_BackgroundTaskRosterSynthesizesNoTerminalEvent` (AC3)

The clause the capture cannot prove, for the reason AC1 carves out: nothing on record shows a task finishing.

- Drive the parser over the **captured** roster, then over a second, synthesized roster the task has dropped out of. The second line's keys are exactly the capture's; only the array contents differ.
- Assert the second line produces **exactly one** event, that it is a `BackgroundTaskRoster`, and that no finish/terminal/completion event of any kind accompanies it. Assert on the *whole* emitted slice (`len(got) == 1`), not just on `got[0]` — a test that only inspects the first event cannot see a second one appended beside it, which is the exact failure this test exists to catch.
- Two shapes of disappearance, both asserted: the task replaced by a **different** task, and the roster going **empty**. The empty case is the one an implementation is most tempted to special-case into a finish.
- Both lines driven through **one** parser instance, so a hypothetical cross-line diff would have the state it needs to fire. Driving them through separate parsers would make the assertion vacuous.

### `TestParser_BackgroundTaskRosterDropIsLoggedContentFree` (Security)

The package's standing rule applied to the new path: nothing derived from claude's output reaches a log. Mirrors the two sibling tests, reusing `logRecorder`.

- Drive **both** paths: the captured line (success) and a malformed line whose `tasks` is not an array, so the decode fails.
- Give the malformed line a distinctive literal `description` that must appear in no log record — the decode-failure handler is the one most tempted to explain itself, and a *roster* is the most tempting thing to dump while debugging.
- Sweep every log record's message and attributes for that description, for the capture's `session_id` and `uuid` values, and for the entry count.
- Assert the Debug record names the subtype keyword only.

### Untouched and expected green

- `TestParser_IgnoredLineTypesIsTheMeasuredSet` (`parser_test.go:1452`) — pins top-level types by `reflect.DeepEqual`, not subtypes.
- The main table's unknown-top-level-type row — a genuinely new top-level type still reaches `UnrecognizedLineType` (AC4).
- `internal/turnevent/event_test.go` — **verified: no exhaustiveness pin.** `TestEvent_StreamTypeSwitch` (`:54-73`) drives a fixed eight-element `stream` slice and its `eventKind` helper (`:75-96`) has a `default:` arm. A new variant does not go red and needs no edit here.
- Every downstream type switch: `internal/turnbridge/outbound.go:129`, `internal/acpbridge/outbound.go:175`, `cmd/pyry/stream_turn_busy.go:160`, `cmd/pyry/interactive_turn_v2.go:256`/`:440` — all have `default:` arms (re-verified on `9bbc540`); `cmd/pyry/acp_turn_stream.go:65` falls through to `acpbridge.MapUpdate`'s defensive `!ok` Debug. **Zero consumer edits.**

### AC4's live clause — not dischargeable here

`internal/e2e/realclaude/interactive_stream_liveness_test.go:253` fatals on a single unrecognized event during a normal turn. That assertion must still hold with all three subtypes mapped, and **only an operator's live run discharges it.** `make e2e-realclaude` SKIPs with no claude login and a skip exits 0, so a green suite in the dispatch environment is not evidence. The ticket carries `needs-real-claude` for exactly this; report it as undischarged rather than claiming a green run.

---

## Comment obligations (AC5)

Adding the `emitSystemSubtype` case arm discharges the mapped-set enumeration — that switch is the one enumeration and every other site points at it rather than restating it. The four sites below were re-read against `9bbc540` on 2026-08-08 and each cite confirmed by reading; **re-check them against the tree before editing**, because this merge moves line numbers exactly as #1382's did.

| Site | Verdict |
|---|---|
| `parser_test.go:529-534` | **EDIT (delete).** Opens *"The remaining sibling, driven from its CAPTURED line: mapping `background_tasks_changed` is #1381, not here, so it must still be silent"* and continues into a `CORRECTED 2026-08-07 (#1382)` paragraph. False the moment the row moves. The row and the block both go; the provenance lands in the new mapping test's doc, and `task_updated`'s own history is already preserved at `parser_test.go:948-950`. Do not carry the dead block forward. |
| `capture_test.go:47-52` | **EDIT.** #1382 rewrote it to end *"`background_tasks_changed` is the one still driven through the drop table."* True today, falsified by exactly this ticket. Rewrite so it records that the reader is shared by three mapping tests and that no captured `system` subtype remains on the drop table. Do **not** write that no further caller will exist — a fourth subtype could arrive. |
| `parser.go:135` | **EDIT.** *"Do not restate that set here: two siblings extend it (#1381, #1382)"* — both have now shipped and no sibling remains to extend it. Keep the `CORRECTED 2026-08-07 (#1380)` block and the rule it states (single enumeration site, pointers to it); replace only the spent justification clause, and note that all three captured subtypes are now mapped. `:147-150`'s still-dropped statement stays **true** — no edit. |
| `parser_test.go:651-653` | **EDIT (one line).** *"a field added later — by #1381 or #1382 extending this event family — is covered"*. The mechanism claim is true and stays; the named example is now false by example — both tickets shipped and **neither** added a field to `BackgroundTaskStarted`. Replace the two spent ticket numbers with the general statement and leave the sweep and its `len(routes)*2` floor untouched (that expression is correct for *that* event — 4 routes, 4 string fields). #1382 left this site alone to keep #1380's test out of its diff; that argument does not transfer, because this ticket edits `parser_test.go` extensively anyway. |

Two further edits this design mandates, outside AC5's list:

- `internal/turnevent/event.go:23-31` — the `Event` interface doc lists the variants and needs `BackgroundTaskRoster`.
- `parser.go`'s new `maxTaskRosterEntries` doc carries the § 2 qualification of the family's single-worst-case doctrine. `maxTaskPatch`'s doctrine sentence (`:89-92`) is left **unedited** — it describes the scalar pair accurately and still does.

---

## Scope fences

- **Do not edit `internal/e2e/realclaude/`.** Its `parserIgnoredTypes` mirror and now-false comments are #1379's scope (blocked by this ticket). Everything there carries `//go:build e2e_realclaude`; nothing in this build goes red because of it. In particular `dropcapExpectedSubtypes` (`dropped_line_capture_test.go:159`) lists `background_tasks_changed`, but it records what #1247 *observed on the headless surface*, not what the parser maps — it does not go false on this merge.
- **Do not extend `cmd/pyry/interactive_turn_v2.go:417`'s `eventKind`.** It has no arm for `BackgroundTaskStarted` or `BackgroundTaskUpdated` either — a pre-existing gap from #1380, raised as a NIT on #1382's review and deliberately left. It is a log-field discriminant with a `default: return "unknown"`; adding arms drags `cmd/pyry` into this diff for no behavioural gain.
- **This ticket stops at the event layer.** Carrying these events onto the mobile v2 wire is #1377; the desktop ACP surface is #1262. No protocol type, no payload struct, no route.
- **No `docs/knowledge/codebase/1381.md`.** The documentation phase writes it from this spec plus the merged diff. It is not a developer deliverable.

---

## Open questions

1. **Is 8 the right roster size?** One observation says 1. The escape hatch is `DroppedTasks`, which makes an under-sized cap visible rather than silent, so nothing blocks on this. If a real roster is ever observed above 8, that is a note in `codebase/<N>.md` and a constant change, not a redesign.
2. **Is 512 enough for a roster description?** It is ~4× the one observed real command line, and the full-length copy is recoverable by joining `task_id` to the `BackgroundTaskStarted` event. The weak point is a consumer that never saw that event (connected mid-session, or the task predates the connection), for which 512 bytes is all there is. Revisit if truncation is observed in practice; `TruncatedFields` is what will surface it.
3. **Does a task's disappearance from a later roster mean it finished?** Unobserved, and deliberately not inferred (§ 5). The ticket that would resolve it is one that captures a terminal transition; until then a consumer may diff snapshots on its own terms and the daemon reports nothing.
4. **Does the family's one-worst-case doctrine need a written home?** It currently lives in `maxTaskPatch`'s comment and is qualified in `maxTaskRosterEntries`'. If a fourth variant arrives, that is the moment to lift both into a package doc rather than a third constant's comment.

---

## Security review

**Verdict:** PASS

**Findings:**

- **[Trust boundaries]** No findings, and the boundary is explicit and single: `emitBackgroundTaskRoster` is the only place `system/background_tasks_changed` bytes become daemon state, and it decodes from `line` — the **top-level bytes** — never from a nested field. That preserves the property `streamLine`'s doc already states (control shapes read from the top level only, nested content never re-scanned) and it matters more on this variant than on either sibling: a forged roster would let claude's own tool output assert *what work is alive*, which is precisely the claim #1240's consumers will act on. The decode target declares three per-entry fields and one line-level key, so the untrusted line's other keys cannot cross the boundary at all. Downstream holds a `turnevent.BackgroundTaskRoster` whose every string is producer-bounded, and the type doc says so — the convention signal consumers read.
- **[Tokens, secrets, credentials]** No findings, and two deliberate non-carries. claude's `session_id` is session identity — not a credential, but **not the daemon's conversation identity** either, and a field of that name would invite a consumer or a later wire mapper (#1377) to route on it. `uuid` has no daemon reader. Both are absent from the decode target, absent from both new types, and the absence is enforced by the reflection sweep rather than by review. `task_id` is claude's opaque handle, not an authorization token; nothing in the daemon grants access on it, and #1377/#1262 are the tickets that must keep it that way.
- **[File operations]** N/A by design. The only file this ticket reads is the committed capture, and only from a test, by a constant relative path (`capture_test.go:20`) with no caller-controlled component. No production path opens, stats, or writes a file.
- **[Subprocess execution]** No findings, one inherited hazard restated. Each entry's `Description` is, for the `local_bash` task type, the literal command line — the same content `BackgroundTaskStarted.Description` already carries, now arriving **once per entry, in a list**. A list of commands is a more tempting shape to feed somewhere structured than a single one, exactly as #1382 argued for `Patch`. Nothing in this ticket execs; the risk is entirely in what a future consumer does, so the doc is the correct place for it. **SHOULD FIX:** `BackgroundTask.Description`'s doc carries the *safe to RENDER as text, never to execute or re-shell* sentence verbatim; code-review checks it is there.
- **[Cryptographic primitives]** N/A. No randomness, no comparison against a secret, no key material anywhere on this path.
- **[Network & I/O]** No findings — and this category is the ticket's substance. Both dimensions are capped at **construction**, before the value enters the event stream, a queue, or a log: `maxTaskRosterEntries` (8) on the count, `maxTaskFieldID` (256) and `maxTaskRosterDescription` (512) on the text, worst case 8192 bytes = 12.5% of the 65519-byte v2 application envelope, ~25% under pathological escaping (§ 2). One subtlety worth naming rather than glossing: the count cap is applied **after** `json.Unmarshal`, so a hostile 100000-entry array is fully materialised in transient memory before it is shortened. That is bounded, not unbounded — `defaultMaxParseBuf` (4 MiB, `parser.go:19`) caps the whole line before it ever reaches the decoder, so the transient ceiling is a function of 4 MiB of input, not of claude's claimed count. The amplification is linear and near 1: the densest legal entry (`{"task_id":"","task_type":"","description":""},` ≈ 55 bytes) decodes to a ~64-byte struct, so 4 MiB of input yields ~5 MB of transient slice — not a quadratic or exponential blow-up, and the loop itself only ever walks the surviving 8. The caps here bound what is *retained* and what crosses the wire, which is what AC2 asks for; #1380's review made the identical distinction for the same reason. There is no path by which the count cap can be applied earlier without counting the array first. Rate, the other half of bytes-per-second, is not a new vector either: this subtype fires on roster change, far below `thinking_tokens`' ~10-per-turn or `TextChunk`'s per-token rate, both of which the existing downstream queue already absorbs.
- **[Error messages, logs, telemetry]** No findings, with one new leak surface closed by test. The standing rule is that nothing derived from claude's output reaches a log; the undecodable path logs the subtype **keyword** only (a message-name, the same class as `sl.Type` in the existing drop log). A roster is the most attractive thing to dump while debugging a malformed line — it is a list, and lists read as diagnostics — which is why `TestParser_BackgroundTaskRosterDropIsLoggedContentFree` drives **both** paths and sweeps every record's message and attributes for the description, for `session_id`, for `uuid`, and for the entry count. The count is included in that sweep deliberately: it is derived from claude's output too, and "just the length" is the leak a content-free rule is most often bent for.
- **[Concurrency]** No findings. No new goroutine, no lock, no shared mutable state; `Parser.Write` keeps its documented single-writer invariant (`parser.go:185-191`) and the parser stays turn-stateless. There is no check-then-mutate — the mapping is a pure function of one line. The refusal to keep cross-line roster state (§ 5) is a correctness decision that happens to also be the concurrency-simplest one; a design that diffed successive rosters would have introduced the package's first per-parser mutable state and its first ordering dependency between lines.
- **[Threat model alignment]** Aligned. The relevant constraint is `docs/protocol-mobile.md:304` § Application-envelope size cap, addressed above with explicit arithmetic against 65519 and against `maxUnrecognizedRaw`'s 16 KiB. Carrying these events onto the mobile v2 wire is **out of scope — #1377**; the desktop ACP surface is **#1262**. This ticket adds no protocol type, payload struct, or route, so no wire-side threat is opened or deferred silently. The one threat this ticket *creates* for #1377 is named for it: an 8 KiB event is the largest in the `turnevent` family, and #1377's envelope mapping must not assume the scalar pair's ~4.9 KB ceiling.

**Reviewer:** architect (self-review per `architect/security-review.md`)
**Date:** 2026-08-08
