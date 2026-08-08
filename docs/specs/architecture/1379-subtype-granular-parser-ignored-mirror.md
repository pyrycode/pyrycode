# #1379 — Make the realclaude mirror of the parser's ignored types subtype-granular

**Size:** S (confirmed; 3 test files, **0 production files**, ~150 LOC total written work)
**Labels:** `needs-real-claude` (dispatcher runs the live gate), no `security-sensitive` (security-review pass skipped)
**Blocked by:** nothing. Premise complete as of ce8f135 (#1381 merged 2026-08-08).

---

## Files to read first

Ordered by when the developer needs them. Everything is in one package plus one parser file.

| Path | What to extract |
|---|---|
| `internal/e2e/realclaude/ptyrunner_byte_equivalence_test.go:125-146` | The mirror: doc comment (125-142), `var parserIgnoredTypes` (143), the `system` entry comment (144) and the `rate_limit_event` entry (145). **All four criteria land here or downstream of here.** |
| `internal/e2e/realclaude/ptyrunner_byte_equivalence_test.go:177-244` | `envelopeShape` (194) already declares `Subtype`; `shapeFilterTypes` (199-222) builds the union of three tables; `extractShapes` (224-244) already decodes `subtype` at 233 and then throws it away at the filter lookup (238). **The change is the filter's key, not a new decode.** |
| `internal/e2e/realclaude/ptyrunner_byte_equivalence_test.go:918-972` | `TestExtractShapes_FiltersParserIgnoredTypes` — the stream fixture (922-928), `want` (934-937), the comment AC5 inverts (941-942), the survivor assertion (943-948), the `unknown_type_survives` negative arm (950-971). |
| `internal/e2e/realclaude/ptyrunner_byte_equivalence_test.go:974-1046` | `parserIgnoredTypeFixtures` (978-987), the pin test (1003-1033) with its member-has-fixture check (1005), orphan check (1019-1023), control arm (1028-1032), and `parseOne` (1038-1046). |
| `internal/e2e/realclaude/ptyrunner_byte_equivalence_test.go:319-380` | `additiveDriftViolations` — read it once to confirm it never touches `parserIgnoredTypes`. That is AC3's whole content. |
| `internal/e2e/realclaude/ptyrunner_byte_equivalence_test.go:870-911` | The two `system_one_sided_*` arms — AC3's existing evidence. **Do not modify.** |
| `internal/streamsup/parser.go:500-536` | `consumeLine`'s drop branch. `ignoredLineTypes[sl.Type]` gates, then `sl.Type == "system" && p.emitSystemSubtype(...)` at 518 carves the mapped subtypes out from **inside** the branch. This is the shape the mirror must reproduce. |
| `internal/streamsup/parser.go:538-564` | `emitSystemSubtype` — the case arms (555-560) are the ONE enumeration of the mapped set. Its doc states the pointer-not-a-list doctrine AC5 applies. |
| `internal/streamsup/parser.go:247-250` | `ignoredLineTypes` itself — still `{"system": true, "rate_limit_event": true}`, deliberately top-level-only (rationale at 222-246). This ticket does **not** touch it. Also the correct cite replacing `parser.go:80-83`. |
| `internal/streamsup/parser.go:587-595` | `emitBackgroundTaskStarted`'s undecodable arm: `return true` with **zero events**. This is the vacuity trap's mechanism. (`emitBackgroundTaskRoster:716-726` is identical in shape.) |
| `internal/streamsup/parser.go:70-91` | Proof the old cite is stale: `parser.go:80-83` now sits inside `maxTaskPatch`'s doc comment, mid-sentence about envelope arithmetic. |
| `internal/e2e/realclaude/interactive_stream_unrecognized_test.go:5-18` | The `system/*` claim at 8-9 and the measurement paragraph at 14-18 it sits above. |
| `internal/e2e/realclaude/dropped_line_capture_test.go:1053-1074` | `dropcapClassify` — **read this before touching anything in that file.** It runs every line through the real parser (`parseOne`, 1063) and returns `dropped == false` on a non-zero emission. The classifier is already subtype-correct by construction; only the prose is stale. |
| `internal/e2e/realclaude/dropped_line_capture_test.go:1508-1512` | The `system/init` row whose `why` is false on both halves. |

---

## Context

`parserIgnoredTypes` (`ptyrunner_byte_equivalence_test.go:143`) is keyed by top-level stream-json type. `shapeFilterTypes()` unions it with the two one-sided allowlists, and `extractShapes` drops every line whose **top-level type** is in that union. `system` is a member, so every `system` line is dropped from the runner-equivalence **sequence** comparison.

Since #1380/#1381 the parser maps three `system` subtypes into daemon events. The mirror still drops all three, so the two runners could diverge on any of them with the gate green.

**The premise reproduces on `main` (ce8f135, 2026-08-08).** Measured this run via `go test -overlay` against a real `streamsup.Parser`, one line each:

| line | events |
|---|---|
| `{"type":"system","subtype":"init","session_id":"abc"}` | 0 |
| `{"type":"system","subtype":"thinking_tokens"}` | 0 |
| `{"type":"system","subtype":"status"}` | 0 |
| `{"type":"system","subtype":"task_started"}` | 1 — `BackgroundTaskStarted` |
| `{"type":"system","subtype":"task_updated"}` | 1 — `BackgroundTaskUpdated` |
| `{"type":"system","subtype":"background_tasks_changed"}` | 1 — `BackgroundTaskRoster` |
| `{"type":"system","subtype":"some_unmapped_future_thing"}` | 0 |
| `{"type":"rate_limit_event"}` | 0 |

**The vacuity trap also reproduces**, and is worth seeing before picking fixtures:

| line | events |
|---|---|
| `{"type":"system","subtype":"task_started","task_id":123}` | **0** — numeric `task_id` fails the decode; the emitter returns "consumed" and emits nothing |
| `{"type":"system","subtype":"background_tasks_changed","tasks":"not-an-array"}` | **0** — same |
| `{"type":"system","subtype":"task_updated","patch":"str…"}` | 1 — `patch` is `json.RawMessage`, so a string still decodes. **Not** a reliable trap probe |
| `{"type":"system","subtype":"background_tasks_changed","tasks":[]}` | 1 — empty array still emits |

The three bare `type`+`subtype` lines are the right fixtures: minimal, and each emits exactly one event today.

---

## Design

### The one structural decision

The parser's drop rule is **"type-keyed, with a per-type set of subtype exceptions that escape the drop."** That is literally the code at `parser.go:512-520`: the `ignoredLineTypes[sl.Type]` gate first, then `emitSystemSubtype` carving out three subtypes from **inside** it. The mirror reproduces that shape.

It is **not** a composite `type/subtype` key. A composite key inverts the default — every `system` subtype nobody wrote down would start surviving into the sequence comparison, and `system/thinking_tokens` alone fires ~10×/turn. The ignored side is an open set and must stay one.

```go
// parserMappedSubtypes is the set of subtypes the shipped parser MAPS out of an
// otherwise-dropped top-level type — the EXCEPTIONS to that type's drop, never
// the drops. nil = no exceptions, the whole type is dropped.
type parserMappedSubtypes map[string]struct{}

var parserIgnoredTypes = map[string]parserMappedSubtypes{
	"system":           {"task_started": {}, "task_updated": {}, "background_tasks_changed": {}}, // + per-entry comment, see AC4
	"rate_limit_event": nil,                                                                     // + existing comment, kept
}
```

Keeping the outer key set identical (`system`, `rate_limit_event`) is what lets the member-has-fixture check (`:1005`) and the orphan check (`:1020`) keep working with no change to their expressions.

### The predicate

One function, placed immediately after the mirror so data and reader are adjacent:

```go
// parserDropsShape reports whether the shipped streamsup parser drops a line of
// this top-level type and subtype in silence. Mirrors parser.go:512-520: the
// type gate first, then the mapped-subtype carve-out from inside it. A subtype
// in no table at all is DROPPED — the ignored side is an open set.
func parserDropsShape(typ, subtype string) bool
```

Behaviour: type absent from the mirror → `false`. Type present and subtype in its `parserMappedSubtypes` → `false`. Otherwise → `true`.

### The filter

`shapeFilterTypes()` returned a flat union set because one lookup per line sufficed. With subtype granularity it cannot, so it is **replaced** by a predicate of the same three tables:

```go
// shapeFilterDrops reports whether extractShapes drops this (type, subtype)
// from the compared sequence. Three tables feed it, answering two different
// questions — see the doc comments on each. The two one-sided tables are looked
// up by TYPE ALONE and that is correct: they answer "is this one-sided emission
// tolerated?", and one-sidedness is a property of the type. Only the third
// answers "does the shipped parser map anything out of this line?", and only
// that question has a subtype-dependent answer.
func shapeFilterDrops(typ, subtype string) bool
```

Behaviour: `true` if `typ` is in `expectedPtyRunnerOnly.Events` or `expectedStreamRunnerOnly.Events`, else `parserDropsShape(typ, subtype)`.

`extractShapes` (`:224`) then loses its `filter := shapeFilterTypes()` prelude and its `filter[env.Type]` lookup (`:238`) becomes `shapeFilterDrops(env.Type, env.Subtype)`. **No decode change** — `env.Subtype` is already unmarshalled at `:233`. Update the `envelopeShape` doc's forward reference (`:187-190`, "see shapeFilterTypes") to the new name.

### Why the parser's own table stays as it is

`ignoredLineTypes` keeping `system` whole is load-bearing on the parser side: it is what makes `emitUnrecognized` (`parser.go:534`) structurally unreachable from any `system` line whatever its subtype. Making it subtype-granular would put unknown `system` subtypes back on the unrecognized lane and break the live zero-unrecognized gate. **This ticket touches no production file.** The mirror aligns to the parser's *effective drop behaviour*, not to the shape of the parser's table.

### Why the mirror may duplicate the mapped set when prose may not

`emitSystemSubtype`'s doc forbids restating the mapped set, because no comment fails a build when it goes stale. The mirror is in another package and the enumeration site is unexported, so it necessarily restates — and that duplicate is legitimate precisely because `TestParserIgnoredTypesMatchesStreamsupParser` **fails a build** when it drifts. Hence the split: the mirror's *data* may restate; the four prose comments must *point*.

**Corollary for AC5's cites — prefer a symbol, not a line number.** The third comment's cite went stale in one ticket (`parser.go:80-83` → now inside `maxTaskPatch`'s doc). Replacing one line-number cite with another just resets the clock. Cite `streamsup.emitSystemSubtype` by name; add the file where it helps, but no line number.

---

## Changes, by criterion

### AC1 — the filter (`TestExtractShapes_FiltersParserIgnoredTypes`, `:918`)

Extend the stream fixture (`:922-928`) with two lines, placing the mapped one **before** `assistant` so survivor order is exercised:

- `{"type":"system","subtype":"task_started"}` — mapped, must survive
- `{"type":"system","subtype":"some_unmapped_future_thing"}` — in no table at all, must be dropped

`want` (`:934-937`) gains `{Type: "system", Subtype: "task_started"}` as its **first** element. The existing `init` / `thinking_tokens` lines cover "the ignored subtypes the fixtures name"; the new never-seen line covers "a subtype that appears in no table at all".

The survivor assertion (`:944`) becomes `if parserDropsShape(sh.Type, sh.Subtype)`. **This is forced** — left as `parserIgnoredTypes[sh.Type]` the new fixture makes it fire. Its message should name both fields.

The comment at `:941-942` is **not** forced by anything and currently asserts the opposite of this design ("a subtype-grained filter would leave system/init standing, which is the mistake this test exists to catch"). See AC5.

`unknown_type_survives` (`:955-971`) is unaffected — `synthetic_unknown_event` is in no table, so `shapeFilterDrops` returns false. Leave it.

### AC2 — the pin test (`:1003`) and its fixtures (`:978`)

Add three lines to `parserIgnoredTypeFixtures["system"]`, exactly as measured:

```
{"type":"system","subtype":"task_started"}
{"type":"system","subtype":"task_updated"}
{"type":"system","subtype":"background_tasks_changed"}
```

The table's **shape stays `map[string][]string`**. The expectation is derived, not declared: decode each fixture line's `subtype` with a small `t.Helper()` decoder (same two-field anonymous struct `extractShapes` uses) and branch on `parserDropsShape(typ, subtype)`.

Restructured test, as scenarios:

- **member has a fixture** (`:1005`) — unchanged.
- **each fixture, mirror says DROPPED** — assert 0 events. Keep today's message (`:1013`) verbatim; it is the one that names the hazard correctly.
- **each fixture, mirror says MAPPED** — assert ≥1 event. New failure message must name **both** readings: either the mirror claims a mapping the parser does not have, **or** the fixture does not decode (`emitBackgroundTask*`'s undecodable arm returns consumed-with-zero-events) and therefore proves nothing.
- **each mapped subtype has a fixture** — new, mirrors `:1005`'s doctrine one level down: for every subtype in `parserIgnoredTypes[typ]`, some fixture line for `typ` must declare it. Without this, a future fourth mapped subtype can be added to the mirror with no fixture exercising it.
- **orphan fixture entry** (`:1019-1023`) — unchanged; `_, ok := parserIgnoredTypes[typ]` still compiles against the new value type.
- **control arm** (`:1028-1032`) — keep. The mapped rows are now non-zero controls too, but `result` controls a type *outside* the mirror, which they do not.

Consider naming subtests by subtype rather than index — the RED/GREEN transcript is the AC's evidence and reads far better as `system/task_started` than `system/3`.

Update the test's doc comment (`:989-1002`): it now guards **both** directions of the mirror, not just "listing a type the parser maps".

### AC3 — the one-sided tables keep governing the SET check

**No code change.** `additiveDriftViolations` (`:319-380`) reads `expectedStreamRunnerOnly.Events` / `expectedPtyRunnerOnly.Events` only and never consults `parserIgnoredTypes`; this change does not alter that. The existing evidence is the pair of arms at `:881` (`system_one_sided_streamrunner`) and `:897` (`system_one_sided_ptyrunner`), which drive a one-sided `system/init` and demand exactly one violation naming the right table.

Deliverable: those two arms stay green in the GREEN run, unmodified. Cite them by name in the PR body.

### AC4 — the mirror's doc comment (`:125-142`) and `system` entry comment (`:144`)

Rewrite in place. Five things the comment must now say, none of them restating the mapped set:

1. **What it mirrors** — the streamsup parser's **effective drop behaviour**, not the shape of `streamsup.ignoredLineTypes`. (Read as a table-shape claim it would point at the wrong file.)
2. **At what granularity** — top-level type, plus a per-type set of subtypes the parser MAPS out of it. The ignored side stays an **open set**: any subtype in no table is dropped.
3. **Why it is no longer top-level-only** — the parser maps three `system` subtypes since #1380/#1381; a top-level key drops those mapped envelopes from the sequence comparison, and the two runners could diverge on any of them with the gate green.
4. **Why `streamsup.ignoredLineTypes` nonetheless stays top-level-keyed** — keeping `system` whole on that list is what makes `emitUnrecognized` structurally unreachable from any `system` line whatever its subtype. Different table, different job.
5. **Why the one-sided tables stay top-level-keyed too** — they answer "is this one-sided emission tolerated?" and govern the SET check as well as the sequence filter. Both runners emit `system/init`, so a `system` entry there would be factually false **and** would pre-authorise exactly the divergence `additiveDriftViolations` exists to catch.

Point at `streamsup.emitSystemSubtype` as the enumeration site. Keep the existing paragraph about `map[string]struct{}` matching the surrounding file (now `map[string]parserMappedSubtypes`, same reasoning).

The `system` entry comment (`:144`) keeps the ~10 `thinking_tokens`/turn vs 1 `init`/turn measurement — it is still true and it is the argument for the open-set default. Replace "The parser ignores the family wholesale" with the subtype-carve-out statement plus the `emitSystemSubtype` pointer.

### AC5 — the three falsified comments

Each points at `streamsup.emitSystemSubtype`; none restates the mapped set; none adds a new line-number cite.

1. **`ptyrunner_byte_equivalence_test.go:941-942`** — inverted by this ticket: subtype-grained is now the requirement. Rewrite to state the hazard the comment was actually reaching for — the **open-set** one. A composite `type/subtype` key would invert the default and let every unwritten `system` subtype through, and `system/thinking_tokens` alone fires ~10×/turn, so that failure is noisy as well as wrong. The loop below it is what catches that, and with `parserDropsShape` it now genuinely does: the fixture's never-seen subtype trips it if it ever survives.
2. **`interactive_stream_unrecognized_test.go:8-9`** — *"Types we knowingly ignore (system/\*, rate_limit_event) stay silent."* `system/*` is now false for three subtypes. Correct to: most `system` subtypes and `rate_limit_event` stay silent, three `system` subtypes map to background-task events, enumerated at `streamsup.emitSystemSubtype`. **Leave the measurement paragraph (`:14-18`) alone** — it describes how `ignoredLineTypes` was seeded, which is still accurate history.
3. **`dropped_line_capture_test.go:1511`** — `why: "system is ignored wholesale (parser.go:80-83)"`. **Both halves are false.** The claim: `system/init` is dropped because its subtype is not one `emitSystemSubtype` maps, not because `system` is dropped wholesale. The cite: `ignoredLineTypes` is at `parser.go:247-250`; `parser.go:80-83` is now mid-sentence inside `maxTaskPatch`'s doc comment. Replace with a symbol-anchored pointer.

---

## Testing strategy

### The RED/GREEN evidence run (AC2's deliverable)

Both target tests are **offline** — no claude binary, no credentials, no network — despite the `e2e_realclaude` build tag. Verified this run on `main`: the command below executes and passes in 0.33s with no `claude` on the path.

```
go test -tags e2e_realclaude -count=1 -v \
  -run 'TestExtractShapes_FiltersParserIgnoredTypes|TestParserIgnoredTypesMatchesStreamsupParser' \
  ./internal/e2e/realclaude/
```

`-tags e2e_realclaude` is **required**; without it the package has no files and `go test` reports "build constraints exclude all Go files", which reads like a clean run and is not one. `-v` is required too: the AC's evidence is the per-subtest `--- PASS`/`--- FAIL` lines. A run showing `no tests to run` or `[no test files]` is not evidence.

**Produce the RED run with `go test -overlay`, not with a worktree edit.** Write a copy of the finished `ptyrunner_byte_equivalence_test.go` with the `system` entry's mapped set emptied (`"system": nil`) to a scratch path, point an overlay JSON at it, and add `-overlay=<abs-path>/overlay.json` to the command above. An empty mapped set **is** top-level-keyed semantics exactly, so this is the honest before-state, and nothing is written into the worktree.

Expected RED (both tests, one mutation):

- `TestParserIgnoredTypesMatchesStreamsupParser` — the three mapped fixtures classify as dropped, expect 0, parser emits 1. Three subtest failures.
- `TestExtractShapes_FiltersParserIgnoredTypes` — the `task_started` line is filtered out, `shapes` no longer DeepEqual `want`. One failure.

Expected GREEN: the same command without `-overlay`, all subtests pass.

**The vacuity trap is self-detecting under this design, but check it anyway.** A fixture that fails to decode emits 0 events; under the shipped mirror it classifies as MAPPED, so the ≥1 assertion fires and the GREEN run is red. It cannot be shipped silently. The RED run is still what proves the fixture *discriminates* — a green RED run means the fixture was wrong, not that the work is done. The three bare `type`+`subtype` lines in the measured table above are known-good.

### The acceptance gate

`make e2e-realclaude` (the dispatcher runs it — `needs-real-claude` is applied). `make check` must also stay green; it does not compile this package (tag), so it is a regression check only.

---

## Deliberately NOT in scope — checked, and correct as they stand

Listed so a developer sweeping for "system is ignored wholesale" claims does not widen the diff.

- **`dropped_line_capture_test.go:1053-1074` (`dropcapClassify`)** — already subtype-correct **by construction**: line 1063 runs every captured line through the real parser via `parseOne` and returns `dropped == false` on any non-zero emission, so a `system/task_started` line is already classified as mapped. Only the `why` **prose** on the `system/init` row is stale. Do not restructure the classifier.
- **`ptyrunner_byte_equivalence_test.go:870-878`** — *"filtering `system` out of the SEQUENCE comparison must buy no silence in the SET check."* Still accurate: the mechanism it describes is unchanged and both arms below it drive `system/init`, which is still filtered. Leave it.
- **`internal/streamsup/parser.go`** — no production change. `ignoredLineTypes` stays top-level-keyed by design (see AC4 item 4).
- **`docs/knowledge/codebase/1379.md`** — owned by the documentation phase, written after merge. Not a developer deliverable.

---

## Open questions

1. **Subtest naming.** `fmt.Sprintf("%s/%d", typ, i)` (`:1011`) vs including the decoded subtype. Subtype names make the RED/GREEN transcript self-explanatory, which matters because that transcript *is* AC2's evidence; the index form is what exists. Developer's call — leaning subtype, with the index kept as a tiebreaker if a subtype ever gets two fixture lines.
2. **`nil` vs `{}` for `rate_limit_event`'s value.** Both behave identically on lookup. `nil` reads as "no exceptions"; `{}` reads as "an empty set of exceptions". Recommend `nil` with the entry comment saying so.
3. **`parserMappedSubtypes` as a defined type vs an inline `map[string]struct{}`.** The defined type lets the inner composite literals elide their type and gives the concept a name the doc comment can use. If it reads as ceremony in context, the inline form is fine — no behaviour depends on it.
