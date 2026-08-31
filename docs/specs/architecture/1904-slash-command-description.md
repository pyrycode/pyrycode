# #1904 — Decode each slash command's description, cap it, carry it on the emitted entry

**Size: s** (re-checked against this written spec in § Size re-check.) `security-sensitive` — § Security review below is part of the deliverable's evidence, not the developer's work.

## Files to read first

Read these before writing anything. Every entry names a symbol; resolve it with `codegraph_search` / `codegraph_node`, not with a line number.

| File | Symbol | What to extract |
| --- | --- | --- |
| `internal/streamsup/parser.go` | `commandEntryLine` | The decode target this slice adds a second field to. Read the WHOLE doc — six of the ten corrections live in it. |
| `internal/streamsup/parser.go` | `emitSlashCommandList` | The per-entry construction loop this rides. Note the `// No bound closure` comment (item 3) and the `argument_hint (#1833)` cite (AC 4's re-point). |
| `internal/streamsup/parser.go` | `maxSlashCommandName` | The sibling cap. Its doc-shape is what the new constant's derivation mirrors; its THE PER-ENTRY TERM paragraph is item 4. |
| `internal/streamsup/parser.go` | `maxTaskRosterDescription` | **The doc-shape precedent.** The multiplication argument and the ROLE argument the derivation has to weigh. |
| `internal/streamsup/parser.go` | `maxModelListEntries` | Where the retired-8192 correction and the "re-derive your own fraction per shape" rule are stated. The ceiling argument is copied from here, not from the roster. |
| `internal/streamsup/parser.go` | `truncateField` | The cut helper: `<=` boundary, empty-replacement mid-rune **deletion** (a cut value can land 1–3 bytes under the limit). |
| `internal/streamsup/parser.go` | `emitModelList` | The `bound` closure inside its per-entry loop — the mechanism § Design adopts, including *why the closure is declared inside the loop*. |
| `internal/streamsup/parser.go` | `logControlResponse` | Item 9's `commands` attribute paragraph. The record itself gains nothing. |
| `internal/turnevent/event.go` | `SlashCommand` | Type doc (item 1) + `TruncatedFields` doc (item 2) + `Name`'s doc as the model for `Description`'s. |
| `internal/turnevent/event.go` | `SlashCommandList` | Its SECURITY paragraph — the statement `Description`'s own doc must NOT restate, only inherit. |
| `internal/protocol/interactive.go` | `SlashCommand` | Declaration order (`Name`, `ArgumentHint`, `Description`, `Aliases`, `TruncatedFields`) and the `description` JSON tag. Its SECURITY paragraph carries the newline measurement. |
| `internal/streamsup/parser_test.go` | `commandEntryFixture` | **Do not widen it.** 26 calls across 21 lines. |
| `internal/streamsup/parser_test.go` | `modelEntryWithFixture` | The sibling-helper pattern to copy, verbatim in shape and in the reason its doc states. |
| `internal/streamsup/parser_test.go` | `TestParser_SlashCommandNamesAreCapped` | The existing per-entry cap table. Liveness rows go here; read the row struct's `commandsOnly` / `entries` / `want` / `why` fields and the `slashCommandNameCapFixture` convention. |
| `internal/streamsup/parser_test.go` | `TestParser_InitializeControlResponseRejectBranches` | Where AC 5's non-string case goes — the `"an entry's name is a number"` row is the template. |
| `cmd/pyry/interactive_turn_v2_test.go` | `emitterSlashCommandListSentinels`, `emitterSlashCommandListFixture` | Item 10: the enumeration, the fixture's two entries, and the sentinel-shape rule (`qq-…-alpha-sentinel` / `zz-…-beta-sentinel`). |
| `docs/knowledge/features/` | the streamsup package overview | Read-only. The documentation phase owns it; do **not** write one. |

## Context

The bounded slash-command list is built and emitting. `commandEntryLine` decodes one key (#1853), `turnevent.SlashCommandList` carries the daemon-internal shape (#1854), `emitSlashCommandList` constructs one `turnevent.SlashCommand` per entry with `Name` cut at `maxSlashCommandName` (#1877, moved to its own emitter in #1886), and a commands-only success rung emits it with no model list beside it (#1890 / #1891).

This slice adds the second of claude's four per-entry keys. It is the first test of `turnevent.SlashCommand`'s FIXING THE ORDER BEFORE THE SECOND FIELD EXISTS promise — the field lands in its **mirrored position**, not appended — and the first point at which `TruncatedFields` has an ORDER to decide.

**No ADR is warranted.** The cap derivation belongs in the constant's own doc, which is where every peer in this family carries its argument; a decision record would be a second copy of it. The doc-shape precedent (`maxTaskRosterDescription` rather than the identifier-cap family) is stated in the constant's doc as this family already states such things.

Every figure below is the ticket's, re-derived during refinement on 2026-08-31. **Do not re-measure the capture** — this ticket and its sibling both explicitly disclaim needing to.

## Design

### 1. `commandEntryLine` gains one field

```go
type commandEntryLine struct {
	Name        string `json:"name"`
	Description string `json:"description"`
}
```

That declaration alone satisfies both halves of AC 5, and the spec says so rather than the developer writing guard code:

- **null / absent** → `""`. `encoding/json` documents unmarshalling a null into a non-pointer Go value as a no-op producing no error. Same carve-out `Name` already relies on; an ordinary counted entry, no panic, no `unrecognized_message`, no stream failure.
- **non-string** → the **whole line** fails to decode and takes `emitModelList`'s undecodable rung, exactly as a non-string `name` and a non-string `models` entry do. All-or-nothing at the line is `modelOptionLine`'s rule and is inherited, not re-implemented.

**No validation, no sanitization.** #1600's verbatim rule governs: no lowercasing, no trimming, no charset filtering, and — stated explicitly because one captured description carries two of them — **no newline stripping**. The cap is the only judgement made about the bytes.

### 2. New constant: `maxSlashCommandDescription = 256`

Placed immediately after `maxSlashCommandName`, in the family's declaration-order convention.

**The value is 256, and the derivation is AC 3's deliverable.** The doc must carry all of the following; it is the substance of this slice, not decoration.

**Multiplicand — the per-entry term as the SUM OF CAPS.** `maxSlashCommandName + maxSlashCommandDescription = 256 + 256 = 512 bytes exactly`. A worst case, not a description of one capture — that is how `maxModelListEntries` derives its product. 512 is also a unit a reader can hold, the virtue `maxTaskRosterEntries`' arithmetic paragraph claims by name for its own 1024, and no other candidate in the trade table lands on one (64 → 320, 128 → 384, 512 → 768).

**Ceiling — `maxUnrecognizedRaw`'s whole-line 16 KiB.** State it as the ceiling and state the fraction arithmetic; do **not** state 8192 as a landmark. `maxModelListEntries`' doc records why: 8192 was `maxTaskRosterEntries`' *product*, which merely *noticed* it lands on half of `maxUnrecognizedRaw`, and inheriting a noticed landmark as a constraint is the mistake that doc undoes. What survives is the **rule** — a whole KNOWN event must not approach the cap on an entire UNKNOWN line, measured retained-against-retained — with the fraction stated per shape (roster 1/2, model list 5/8).

**This slice does NOT fix this shape's fraction.** `maxModelListEntries`' doc says the next aggregate variant re-derives its own rather than inheriting 5/8, and the fraction is only decidable together with the entry count — which is #1826's. What this doc owes instead is the arithmetic handed forward: at the 512-byte term, 16384 × 1/2 = 8192 leaves **16 entries**, and 16384 × 5/8 = 10240 leaves **20**. #1826 picks the fraction and the count from that, adding to one derived figure rather than re-deriving it.

**Floor — the measurement.** 51 entries; `description` present and non-empty on all 51; longest 1145 bytes (`dataviz`), then 1078, 1075, 1023, 797; mean 207.5, median 69; **10 over 256, 16 over 128, 28 over 64**; 10,580 bytes in total. Names, for contrast, total 494 with a 24-byte longest. So 256 is ~1.2x the mean and ~3.7x the median — a much thinner multiple than `maxSlashCommandName`'s 10.7x over the same capture, and deliberately so.

**Why thinner: `maxTaskRosterDescription`'s multiplication argument, with the multiplier worse.** This is the family's second field whose budget is multiplied by a count claude chooses, and a multiplied field earns a smaller unit budget than the same field carried once. The roster multiplies by 8; this list multiplies by claude's observed 51. That pushes **below** the roster's 512, and 256 is where it lands.

**Why not lower: the ROLE argument inverts.** A roster description is recoverable from the `BackgroundTaskStarted` its `task_id` joins back to, so a cut there loses nothing a consumer holding that event cannot recover. **A cut slash-command description is recoverable from nothing.** Bounded by the named consumer — pyrycode-desktop#694's type-ahead renders one row per command as a name, an argument hint and a description — 128 would cut 16 of 51 and 64 would cut 28, i.e. over half the menu arriving truncated for the one consumer the field exists for. 256 cuts 10.

**What the observed capture actually costs at this cap, and it is the sharpest figure in the derivation.** Retained name+description across all 51 entries, computed through `truncateField` itself (byte cut *then* the empty-replacement scrub, not a naive byte-cut sum): **6,117 bytes**. That is under 8,192 (1/2) *and* under 10,240 (5/8), so today's real workspace fits inside **both** established fractions after the cut. No larger candidate does: 512 retains 8,233 and clears only 5/8.

**A cap firing on claude's ordinary output is unavoidable here, and the doc must say so rather than claim otherwise.** `maxModelListEntries`' doc rejects a count cap that cuts the menu everyone sees; that argument cannot be reached for. The descriptions alone total 10,580 — already past 10,240 and past 8,192 before a single name is counted — and one is 1145 bytes by itself. **State it against the fraction, not the whole 16 KiB**: uncut name+description is 11,074, over both established fractions but *under* 16,384, so "past the whole ceiling uncut" is false and would hand a reviewer a premise to knock down. The truncated-fields report is what makes the cut honest.

**Mid-rune is on the live path.** 14 of the 51 descriptions carry non-ASCII (no name does), so `truncateField`'s mid-rune deletion is ordinary here rather than a corner case — a cut can come out 1–3 bytes under. At **256 no capture entry is mid-rune reachable**; at 128 `dataviz` is (loses 1 byte), at 64 `artifact-capabilities` is (loses 2). Say which, so the sibling ticket's boundary matrix has a stated starting point.

**Escaping is mild**, for `maxUnrecognizedRaw`'s reason: these are JSON string values, so the growth is quotes and backslashes rather than a `\u00XX` expansion of every byte. Name the one wrinkle measured here — `claude-api` carries two `0x0a` bytes, the **only** sub-0x20 bytes anywhere across the entries' string fields — and note they arrive pre-escaped as two printable bytes each. Do not generalise that to "control characters"; the capture does not show that breadth.

**A separate constant even though it currently equals `maxSlashCommandName`** (and `maxModelResolved`, `maxModelField`, `maxModelValue`): `maxRateLimitField`'s one-constant-per-meaning rule, so a future change to one budget cannot silently move this one. **The new doc names its peers; the three existing peer enumerations stay unedited** — #1877 added `maxSlashCommandName` = 256 and amended none of them, and that precedent is this field's own neighbour.

### 3. `turnevent.SlashCommand` gains `Description` in its mirrored position

```go
type SlashCommand struct {
	Name            string
	Description     string
	TruncatedFields []string
}
```

Between `Name` and `TruncatedFields`, matching `protocol.SlashCommand`'s declaration order and leaving the gap `ArgumentHint` fills later. Its doc mirrors `Name`'s: verbatim per #1600 (adding *no newline stripping* to the list), bounded by the producer at construction, never sanitized — and **inheriting** `SlashCommandList`'s SECURITY paragraph by reference rather than restating it, which is the rule that paragraph states about itself.

**Every existing composite literal is keyed** (`{Name: …}` in `internal/streamsup/parser.go`, `internal/streamsup/parser_test.go`, `cmd/pyry/interactive_turn_v2_test.go`, `cmd/pyry/stream_turn_busy_test.go`), verified by grep. Adding a field breaks none of them, and the zero value `""` is what every existing `want` literal already asserts. **Consumer call sites needing simultaneous update: zero.**

### 4. The construction loop: adopt `emitModelList`'s `bound` closure

The ticket leaves the mechanism to the architect. **Use the `bound` closure**, declared **inside** the per-entry loop.

- It is the in-file precedent for a multi-field per-entry truncation report, so the two loops read alike and a reader who has read `emitModelList`'s needs no second mechanism.
- It removes the duplicated `if truncated { cut = append(…) }` block two sequential `truncateField` calls would require, so "declaration order == call order" has one place to check rather than two.
- Item 3's own sentence says the closure is what a one-field entry does not need. The moment there are two, the premise it rests on is gone, and the closure is what it points at.
- **Inside the loop, not hoisted**, for `emitModelList`'s stated reason verbatim: `cut` is per entry, and that scope is the whole of what keeps one entry's cut off the entries after it.

Call order: `Name` first, `Description` second — which **is** the declaration order, and is what makes AC 2's `[]string{"name", "description"}` a property of the call sequence rather than a sort. The name appended for the second field is `"description"`, the DAEMON's snake_case name, which for this field again coincides with `protocol.SlashCommand`'s wire tag — so a later mapping stays a copy rather than a translation. **Keep the sentence noting that the distinction starts mattering at `argument_hint`, and re-point its cite from #1833 to #1830** (AC 4).

The composite literal gains `Description: description` between `Name` and `TruncatedFields`.

### 5. Nothing else moves

No entry-count cap, no `DroppedCommands` — #1826's, and `turnevent.SlashCommandList`'s doc fixes that sequencing. `logControlResponse` stays at six attributes and gains no decoded content. No wire frame, no protocol change, no client. `emitSlashCommandList`'s WHERE THE CONSTRUCTED LIST GOES enumeration is unchanged: the list reaches `interactiveTurnEmitterV2.Handle`'s default and returns, so it never reaches `turnbridge.MapEvent` at all.

## Doc corrections (AC 4) — the ten, and how each changes

Each is a symbol-anchored `//` comment carrying no line number, verified verbatim in the tree on 2026-08-31. Correct all ten in this change.

**`internal/turnevent/event.go`**

1. **`SlashCommand`'s type doc** — *"Its two fields are the FIRST TWO of protocol.SlashCommand's five"* and *"The three between them — ArgumentHint, Description and Aliases — are deliberately absent"*. Now three fields; two of protocol's five are between them. The FIXING THE ORDER paragraph gets its first evidence — say that this field landed in its mirrored position rather than appended, which is the promise being kept.
2. **`SlashCommand.TruncatedFields`'s doc** — *"TODAY THE ONLY NAME IT CAN CARRY IS \"name\""*. Now `"name"` and `"description"`, in declaration order. The paragraph's own "THE ENUMERATION GROWS WITH THE FIELD SET" sentence predicted this; extend it as `ModelOption.TruncatedFields` was extended for `"effort_levels"` in #1827.

**`internal/streamsup/parser.go`**

3. **`emitSlashCommandList`'s no-`bound`-closure paragraph** — *"one field means there is no TruncatedFields ORDER for a composite literal to decide"*. False as of this slice. Replace with the closure's own reason (§ Design 4), including why it is declared inside the loop.
4. **`maxSlashCommandName`'s THE PER-ENTRY TERM paragraph** — re-state it with this field added, so #1830 and #1825 add to one derived figure. Today one entry costs at most `maxSlashCommandName + maxSlashCommandDescription` = 512 bytes. The entry-count factor is still #1826's. **Its `#1833` cite must not survive** — #1833 is closed.
5. **`commandEntryLine`'s ONE FIELD, AND THE OMISSION IS THE POINT paragraph** — *"claude sends four keys per entry … and three are deliberately absent"*, plus its closing *"A later reader must not \"complete\" this struct"*. **The omission was slice-scoped, not permanent.** Now two of four are declared; say which omissions survive (`argumentHint` → #1830, `aliases` → #1825) rather than reading as a standing prohibition. Rewrite the header sentence — "ONE FIELD" is no longer the point.
6. **The same paragraph's memory arithmetic, and it is the sharpest of these.** *"the captured array is 14,277 bytes compact and the fifty-one `name` strings inside it total 494, so declaring one field is what keeps 96% of a workspace-authored payload from ever becoming a Go string."* Re-derived: 494 + 10,580 = **11,074 of 14,277**, so **77.6% now becomes a Go string and 22.4% is kept out**. **This is not a number swap.** The omission stops being "the whole of the memory story" — it is now a minority of it, and what carries the weight instead is the per-field cap one step downstream (§ Design 2), which bounds what is *retained* regardless of what is decoded. Write the argument, not the replacement percentage alone.
7. **`commandEntryLine`'s transient arithmetic** — *"this struct is ONE field where modelOptionLine is five, so per densest-legal element the worst-case transient is a fraction of the already-accepted one"*. Two fields where `modelOptionLine` is five. The conclusion survives (still a fraction, still bounded one level up by `defaultMaxParseBuf`'s 4 MiB whole-line cap, still unable to maximise both on a single line) but the arithmetic is restated, not left at "ONE".
8. **`commandEntryLine`'s A PER-FIELD CAP DOES EXIST paragraph** plus two enumerations in the same doc:
   - the cap paragraph names `maxSlashCommandName` bounding `Name` as the single downstream cap → now two caps, both applied at construction in `emitSlashCommandList`;
   - the element-level all-or-nothing enumeration (*"a `name` that is not a string"*) → add `description`;
   - the null carve-out (*"a null `name` lands as \"\""*) → add `description`.
9. **`logControlResponse`'s `commands` attribute doc** — *"No name, no argumentHint, no description and no alias reaches this record on any rung, and commandEntryLine's single field is what makes three of those four unreachable rather than merely unwritten."* Now **two** of the four are unreachable by omission, and the description moves into the *unwritten* category the sentence explicitly contrasts against. **Redraw the distinction, do not renumber it**: the record still carries no decoded content, but for the description that is now a property of what this function chooses to log rather than of what the decode target can hold. Related, at the commands-only rung's call site: *"one decode target (commandEntryLine), one construction site, one cap (maxSlashCommandName)"* — the cap count becomes two.

**`cmd/pyry/interactive_turn_v2_test.go`**

10. **`emitterSlashCommandListSentinels`' doc — a test-coverage hole, not a stale sentence.** *"… and so the enumeration keeps covering the fields turnevent.SlashCommand grows."* The derived-from-the-fixture half is true; the grows-with-the-field-set half is not, because the body names fields one at a time (`c.Name`, then `c.TruncatedFields...`). **Without this edit the new field gets no log-leak assertion on the `cmd/pyry` lane — the lane AC 1's "no log line gains decoded content" is about.** The § Security review pass classifies this as a **security control rather than a doc fix**: a workspace-authored description is arbitrary operator-adjacent text that may carry secrets, and this enumeration is what makes the no-log guarantee a deterministic test instead of an advisory sentence. **Non-droppable scope.** Three things and no more:
    - a `Description` sentinel on **each** of the fixture's two entries, obeying the fixture's own rule (conspicuous, not natural language, not a substring of the log's own text, and the two entries distinguishable in every field) — `qq-description-alpha-sentinel` / `zz-description-beta-sentinel` match the existing shape;
    - one line in the enumeration (`out = append(out, c.Description)`);
    - the claim amended to say what it actually guarantees: derivation stops a *sentinel* silently dropping out, but a new *field* must add its own line here, and this is the slice that proves it.

### Deliberately excluded — do not widen

- `emitModelAnnounced`'s copy of the same no-`bound`-closure sentence. It is `ModelAnnounced.Model`'s claim about its own single field; this field does not touch it. Item 3 is the copy inside `emitSlashCommandList` and nothing else.
- `boundEach`'s *"one call site and one field, so parameterizing buys nothing"* — about the effort-levels closure in `emitModelList`. Unaffected.
- The three `"A separate constant even though it currently equals …"` peer enumerations. **Declaration-time only; do not reopen**, even though this cap lands on 256. #1877's precedent is exact.
- `docs/specs/**`'s nine further `#1833` mentions. Specs are frozen per-ticket build artifacts; editing them would spend this budget on history. **Only the two in-tree `#1833` cites move**, both in `internal/streamsup/parser.go` (item 4's and the `argument_hint` one), confirmed by `git grep '#1833' -- internal/` returning exactly two hits.

**One consequence of the developer's own test edit, and it is capped at one sentence.** `TestParser_SlashCommandNamesAreCapped`'s doc opens *"the per-name bound's whole boundary matrix"*. Adding description liveness rows makes that scope sentence incomplete, so keep it true: the table is the name's whole boundary matrix **plus** the description's liveness rows, the description's own boundary matrix being the sibling ticket's. This is not an eleventh enumerated item — it is the ordinary duty to keep the doc of a thing you edit accurate. One sentence; do not expand the table's doc further.

## Testing strategy

Scenarios, not test code. Write them in this package's existing idiom.

### Helper — a sibling fixture, not a widened one

`commandEntryFixture` has **26 calls across 21 lines** and must **not** be widened; that constraint is load-bearing for this ticket's size. Add a sibling in `modelEntryWithFixture`'s exact shape — returns a **copy** of one entry carrying an extra claude key, `value any` so a row can put a non-string where a string belongs. Its doc states the not-widening reason as `modelEntryWithFixture`'s does. A generic `(entry, key, value)` signature rather than a description-specific one, so #1830 and #1825 reuse it instead of adding a third helper.

### `TestParser_SlashCommandNamesAreCapped` — liveness rows only

Three rows. **The description's boundary matrix — the exactly-at-cap row, the mid-rune rows, the committed-capture pin — is deliberately NOT here.** It is the sibling ticket, blocked by this one. Add a `slashCommandDescriptionCapFixture` literal constant alongside `slashCommandNameCapFixture`, its own fixture for that constant's stated reason (a shared fixture would let a change to one silently retarget the other's proof).

1. **A description over the cap is cut and reported; one that fits is not.** Two entries: the first with a description of `slashCommandDescriptionCapFixture+1` bytes → cut to the cap, `TruncatedFields: []string{"description"}`; the second with a short description → uncut, `TruncatedFields` nil. The second entry is what makes the first non-vacuous against a producer that names `"description"` on every entry regardless, and is simultaneously the "a cut on one entry does not appear on the entries AFTER it" pin — the existing first row's device, verbatim.
2. **Both fields cut on ONE entry → `[]string{"name", "description"}`.** The row AC 2's declaration-order claim is observable through. It is also the only row that reddens on the two `bound` calls being swapped, which is the whole reason the ORDER became a live question. Keep a second, uncut entry beside it for the same non-vacuity reason.
3. **A null description is an ordinary counted entry.** JSON `null` under `description` → `Description: ""`, no report, the entry counted and positioned. Uses the new helper with `nil`. A trailing real entry pins positions, as the existing absent-name row does.

Existing rows need no edit: `commandEntryFixture` carries no `description` key, so `Description` decodes as `""` and every existing `want` literal already asserts that zero value.

### `TestParser_InitializeControlResponseRejectBranches` — AC 5's second half

One row: an entry whose `description` is a **number** → `wantReason: "undecodable"`. The existing `"an entry's name is a number"` row is the template, and the pair together is what shows the all-or-nothing rule is the *struct's* and not the `name` field's.

### `cmd/pyry` — item 10's negative

No new test. The `Description` sentinels flow into `emitterSlashCommandListSentinels`, `assertSlashCommandListKindLeaksNothing` appends them to `leakable`, and `TestInteractiveTurnEmitterV2` fails if any appears in the captured log. That is AC 1's "no log line gains decoded content" on the lane the list actually reaches.

### Gate

`make check`. This touches no `e2e_realclaude`-tagged file, and the capture JSON is read by an ordinary test, so `make check` is the honest gate here. Nothing in this change writes a fixture that needs committing.

## Concurrency model

Unchanged. `emitSlashCommandList` runs on the parser's single line-reading goroutine and its output is handed to `p.emit`. This slice adds one string field and one `truncateField` call inside an existing loop; no new goroutine, no new channel, no shared state, no shutdown-sequence change.

## Error handling

No new failure mode, and that is the design rather than an omission.

| Input | Outcome | Mechanism |
| --- | --- | --- |
| `description` absent | `""`, ordinary counted entry | Go zero value |
| `description: null` | `""`, ordinary counted entry | `encoding/json`'s documented null no-op |
| `description` non-string | whole **line** fails decode → undecodable rung | all-or-nothing unmarshal, `modelOptionLine`'s rule |
| `description` over the cap | cut to the cap, `"description"` in that entry's `TruncatedFields` | `truncateField` + the `bound` closure |
| cut lands mid-rune | partial rune **deleted**, so the value lands 1–3 bytes under; still reported as truncated | `truncateField`'s empty replacement; the bool says only whether the cap cut |

No new reject branch in the state machine, so no new per-reject log call. The `commands` count on `logControlResponse` still counts what DECODED, and with no entry-count cap that is still the emitted count — six attributes, unchanged.

## Security review

**Verdict:** PASS

This pass was run against this spec before commit, per `$AGENTS_REPO_PATH/architect/security-review.md`. **It does not inherit `Name`'s answer.** `commandEntryLine`'s THAT VALIDATION QUESTION paragraph answers the no-sink question by **enumerating reached sinks**, and that enumeration is a claim about `Name` established by inspection — not a property of the path. It was re-walked for this field, and the sink claims below were verified by grep over `internal/` and `cmd/` rather than copied.

**Findings:**

- **[Trust boundaries]** No findings. The boundary is single and explicit: `json.Unmarshal` into `commandEntryLine` inside `emitModelList`. `Description` is **workspace-authored** — a command defined in a repository was written by whoever wrote that repository, and the daemon reads it in whatever directory the operator points a session at — so it is a *lower*-trust origin than claude's own strings, the distinction `turnevent.SlashCommandList`'s SECURITY paragraph draws. Downstream holders get the untrusted signal from that paragraph rather than from the type system, which is this whole family's posture and is why `Description`'s own doc **inherits** it rather than restating it (restating is what `Name`'s doc explicitly refuses, to keep one copy). One scatter exists and is *not* widened here: `systemInitLine` decodes the same inventory as bare strings under `slash_commands`, a separate path this field is not added to.
- **[Tokens, secrets, credentials]** No tokens in the design — but the category is **not** vacuous here, and this is the pass's most useful observation. A workspace-authored description is arbitrary operator-adjacent text and may contain secrets the operator never meant to emit; that is a second, independent reason the no-log rule matters beyond log hygiene. **Item 10 is therefore a security control, not a doc fix** — it is what makes "no log line gains decoded content" a deterministic test on the `cmd/pyry` lane rather than an advisory sentence. If item 10 slips, the guarantee AC 1 states is untested for this field. Treat it as non-droppable scope.
- **[File operations]** N/A by design, verified rather than assumed. No path is constructed from this value: `git grep` for `filepath.Join` / `filepath.Match` in `internal/streamsup` returns only `runner.go`'s session-file helper (which builds from a validated stem, not from parsed events) and the doc comment carrying the enumeration itself. No file is created, no mode is chosen, no symlink is followed, no check-then-use occurs on this path.
- **[Subprocess / external command execution]** N/A, verified. The only `exec.CommandContext` in the package is `runner.go`'s claude spawn, and its argv comes from `r.cfg` — daemon configuration — with no path from `p.emit`'s output back into it. Cross-checked the other way too: no non-test file that consumes `turnevent.*` outside `internal/streamsup` reaches `exec.Command`, `filepath.Match` or `regexp.MustCompile`. **The enumeration is the answer rather than a judgement about the bytes**, and deliberately so: `[`, `*` and `?` are syntax to `filepath.Match` and to `regexp` with no shell anywhere in sight, and this field is prose — 14 of 51 captured descriptions carry non-ASCII — so "these look like identifiers" was never available here.
- **[Cryptographic primitives]** N/A. No randomness, no key material, no comparison against a secret. The one comparison this slice adds is `truncateField`'s `len(s) <= limit`, a length test on non-secret data, so constant-time comparison is not applicable.
- **[Network & I/O — resource exhaustion]** No MUST FIX, and the adversarial arithmetic is worth writing down because the intuitive answer is wrong. The per-entry retained worst case doubles (256 → 512 bytes), but **the absolute retained bound does not move**: every retained byte came from an input byte, so amplification is near 1 and `defaultMaxParseBuf`'s 4 MiB whole-line cap bounds retained text at ~4 MiB before and after this change. What this slice actually changes is the *fraction* of a hostile line that becomes retained Go string — which is exactly item 6's 96% → 22.4% correction, restated as a security property. The struct-count vector (very many tiny entries) is a **cardinality** issue and is untouched by this field.
- **[Network & I/O — input size limits]** No findings. Two bounds, both documented in § Design 2: `defaultMaxParseBuf` (4 MiB, whole line, transient) and `maxSlashCommandDescription` (256 bytes, per field, applied at **construction** so an oversized value never enters the event stream, the push queue, or any log). No new socket read, no timeout surface, no TLS surface.
- **[Error messages, logs, telemetry]** No findings, with one trigger-surface note. `logControlResponse` writes six attributes — daemon-computed integers and a daemon-authored keyword — and this field is not among them; item 9's correction is what keeps that claim honestly worded now that the description is *unwritten* rather than *unreachable*. The one path on which claude's bytes do cross is pre-existing: a non-string `description` fails the whole-line decode and the raw line rides `turnevent.Unrecognized.Raw`, bounded by `maxUnrecognizedRaw` at 16 KiB, with `emitUnrecognized` logging site/type/bytes/truncated and never content. **This slice widens the trigger for that path from one key to two**, which is the intended all-or-nothing semantics rather than a new exposure, and the bound is unchanged.
- **[Logs — newlines]** No finding, and the measurement must not be overstated. One description (`claude-api`) carries two `0x0a` bytes, and `0x0a` is the **only** sub-0x20 byte anywhere across the entries' string fields; `protocol.SlashCommand`'s doc already carries that. They reach no log attribute, so no log-injection sink exists to reason about, and they are **not stripped** — #1600's verbatim rule governs and the client render boundary owns sanitization. **A doc written here must not claim "control characters"**: that is a breadth the capture does not show, and claiming it would be an unearned generalisation a reviewer can falsify against the capture.
- **[Concurrency]** N/A with reason. `emitSlashCommandList` runs on the parser's single line-reading goroutine; this slice adds one string field and one `truncateField` call inside an existing loop. No lock is taken, no goroutine is spawned, no shared state is mutated, no shutdown sequence changes. The one scope-sensitive detail is called out in § Design 4 and is correctness-shaped rather than security-shaped: the `bound` closure and `cut` are declared **inside** the loop so one entry's cut cannot appear on the entries after it.
- **[Threat model alignment]** OUT OF SCOPE, owner named. `docs/threat-model.md` does not exist; the governing document is `docs/protocol-mobile.md` § Security model, and **nothing publishes this list today** — the value reaches `interactiveTurnEmitterV2.Handle`'s default and returns, so `emitMapped` never runs for it and it does not reach `turnbridge.MapEvent` at all, no eventring append and no wire frame. The live threat is therefore the **client-side render boundary** (untrusted text into an HTML sink, an attribute, or a URL), which is **#1720's** and is unreachable from this slice.
- **[RE-OPEN TRIGGER]** Restated for this field rather than inherited: the first slice that gives this value a SYNTAX sink — a path element, a match pattern, a regexp, an argv element, a log attribute — or renders it into an HTML sink, an attribute or a URL inherits the validation question open again. #1720 is the nearest such slice.

No MUST FIX. The one finding that changes the developer's obligations is the **[Tokens]** entry: item 10 is load-bearing security scope, not optional housekeeping.

**Reviewer:** architect (self-review per the security-review checklist)
**Date:** 2026-08-31

## Size re-check (§ 4)

| Boundary | Limit | This spec |
| --- | --- | --- |
| Production source files created or modified | ≤ 3 | **2** — `internal/streamsup/parser.go`, `internal/turnevent/event.go` |
| Total written work | ≤ 400 | **~265–350** (see below) |
| New exported types or interfaces | ≤ 5 | **0 types**, 1 exported field |
| Consumer call sites needing simultaneous update | ≤ 10 | **0** — every `SlashCommand` literal is keyed; `commandEntryFixture` is not widened |
| Acceptance criteria | ≤ 5 | **5** |
| Distinct error/reject branches | ≤ 10 | **0 new** |

Total is counted, not waved through: `event.go` ~55–70 (field + doc, items 1–2); `parser.go` ~130–155 (constant + derivation ~50, `commandEntryLine` field + items 5–8 ~45, loop + item 3 + the cite re-point ~30, item 9 ~12); `parser_test.go` ~70–90 (helper, cap fixture, three rows with their `why` prose, one reject row); `cmd/pyry` ~10–14 (item 10). The ten doc corrections are ~70 lines of that and are **counted, not discounted as prose**.

Sanity-checked against the three analogues, each re-derived from `git show --stat` (insertions + deletions): `ff01345d` #1819 **328** (one field, no cap constant, no truncation rows), `32578cdf` #1877 **384** (the whole emitter, the name cap, two liveness rows, plus a 41-line `cmd/pyry` production change this ticket does not have), `0eceb5a0` #1827 **614** (one field + its cap + a new bound closure + the **full** cap table). This is #1819's shape plus a derived cap constant plus ten doc corrections, **without** #1827's boundary matrix — between #1819 and #1877, as the ticket's own estimate says.

## Open questions

- **The fraction of the 16 KiB ceiling this shape settles on is deliberately not decided here** — `maxModelListEntries`' doc requires the next aggregate variant to re-derive its own, and the fraction is only meaningful together with a count. This slice hands #1826 the 512-byte per-entry term and the two arithmetics (16 entries at 1/2, 20 at 5/8). #1826 chooses.
- **Whether the daemon-internal value keeps the absent/empty distinction** is settled for this field by the plain `string` — absent, null and `""` are one reading — which is `Name`'s existing carve-out. It is **not** a precedent for `aliases`, whose absent/empty question is #1825's and whose frequencies are inverted from the effort levels'.
- **If the derivation doc and the ten corrections together outgrow the boundary while writing**, the split seam is the `commandEntryLine` doc rewrite (items 5–8). Take that decision before writing, not after — but the count above says it should not be needed.
