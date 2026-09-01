# #1905 — the slash-command description cap's boundary matrix and its captured decode

**Ticket:** [#1905](https://github.com/pyrycode/pyrycode/issues/1905) · `size:s` · `security-sensitive`
**Scope:** test-only. No production file is touched.

## Files to read first

| Path | Symbol | What to extract |
| --- | --- | --- |
| `internal/streamsup/parser_test.go` | `TestParser_SlashCommandNamesAreCapped` | the table this ticket extends: the row struct (`commandsOnly`, `entries`, `want`, `why`), the rung derivation in the subtest body, and the per-entry assertion block the new rows are graded by |
| `internal/streamsup/parser_test.go` | `commandEntryWithFixture` | the one helper that adds a key to a command entry — **use it; `commandEntryFixture` must not be widened** |
| `internal/streamsup/parser_test.go` | `slashCommandDescriptionCapFixture`, `slashCommandNameCapFixture` | the literal cap fixtures and the doc block above them explaining why each field owns one |
| `internal/streamsup/parser_test.go` | `TestParser_InitializeControlResponseCountsTheCapturedCommands` | the capture-pin idiom the new test copies: guard the capture's own shape first (naming the arm), then replay, then compare against the capture's bytes. Its `TruncatedFields` comment carries the forward reference this ticket discharges |
| `internal/streamsup/parser_test.go` | `TestParser_InitializeControlResponseDecodesTheCapturedModels` | the *"a guard is a proof a re-capture could silently take away"* shape — `sawAutoModeTrue`/`sawAutoModeAbsent` is the pattern the new guards mirror |
| `internal/streamsup/parser_test.go` | `capturedCommandEntries`, `capturedCommandString` | the per-entry readers — literal key strings, never the production decode target |
| `internal/streamsup/parser_test.go` | `capturedInitializeLine`, `slashCommandNamePreview` | the replayed line, and the bounded preview used in every failure message that prints a capped value |
| `internal/streamsup/initialize_capture_test.go` | `initCaptureArms`, `initCaptureArmNoRequest`, `initCaptureArmLabel`, `capturedInitializePayload` | the arm list, the arm the loop must `continue` past, and the subtest label |
| `internal/streamsup/parser.go` | `truncateField` | the whole contract in five lines: `<=` keeps, then `strings.ToValidUTF8(s, "")` — a **deletion**, on *both* branches |
| `internal/streamsup/parser.go` | `maxSlashCommandDescription` | the derivation and, specifically, its `MID-RUNE IS ON THE LIVE PATH` paragraph — the mid-rune reachability measurement the new rows **cross-reference and must not restate** |
| `internal/streamsup/parser.go` | `emitSlashCommandList` | the `bound` closure and the two sequential `truncateField` calls whose order `TruncatedFields` inherits |
| `docs/knowledge/features/streamsup-package-the-commands-only-rung-s-emit-lands.md` | § `Description` joins the per-entry shape (#1904) | what the sibling settled, and its lesson about a capture pin asserting a narrower claim than its assertion's scope covers. **Read-only — the documentation phase owns this file** |

## Context

#1904 landed `Description` on `commandEntryLine` and `turnevent.SlashCommand`, set `maxSlashCommandDescription = 256`, and shipped **liveness rows only** — over-cap-and-fits, both-fields-cut, and the null carve-out. That is #1877's deliberate pattern: the bound is never unproven for a merge window, and the boundary matrix follows one ticket behind. This ticket is that follow-on, and it is #1878's slice replayed one field over.

Three things are therefore already true in the tree and are constraints here, not deliverables:

- `commandEntryWithFixture` exists and #1904's rows use it.
- `slashCommandDescriptionCapFixture`, `descAtCap`, `twoByteRune` and `fourByteRune` all exist.
- The mid-rune reachability measurement lives in `maxSlashCommandDescription`'s doc.

**No ADR is warranted.** This adds no decision — it pins one already recorded in `maxSlashCommandDescription`'s doc.

### The capture figures were re-derived on 2026-09-01 against the committed bytes

Confirmed independently of the ticket body, over all four `internal/e2e/realclaude/testdata/initialize_control_v2.1.239*.json` files:

- Three responding arms carry byte-identical `commands` arrays of **51** entries; `_control_no_request` carries none.
- **Exactly 10** descriptions exceed 256 bytes. In **claude's own array order** they are: `design`, `dataviz`, `artifact-capabilities`, `update-config`, `verify`, `code-review`, `doctor`, `claude-api`, `run`, `run-skill-generator`. (The developer should re-derive this list from the capture rather than transcribe it — but the literal it writes into the test must match this, and must be in whichever order the derivation beside it produces.)
- **No capture entry cuts mid-rune at 256** — all ten land on a rune boundary, so all ten emit at exactly 256 bytes.
- `dataviz` is 1145 bytes; `claude-api` is 1078 with `0x0a` at byte offsets **152 and 755**. At the cut the first survives and the second is discarded, so the emitted value carries **exactly one** newline.

The ticket body lists the ten longest-first. **This spec specifies array order instead**, because a single pass over the capture produces it for free and it pins claude's ordering as a side effect; a longest-first literal would need a sort that proves nothing extra.

## Design

Three deliverables, all inside `internal/streamsup/parser_test.go` plus six one-token cite edits.

### 1. Three rows in the existing table

Appended after #1904's description rows, in #1904's separate-row shape. **Do not widen the existing rows** — the name rows deliberately carry no description.

| Row | `entries` | `want` |
| --- | --- | --- |
| a description exactly at the cap is NOT truncated | `commandEntryWithFixture(commandEntryFixture("deep-research"), "description", descAtCap)` and `commandEntryWithFixture(commandEntryFixture("design"), "description", "plan a change")` | `{Name: "deep-research", Description: descAtCap}`, `{Name: "design", Description: "plan a change"}` — both with nil `TruncatedFields` |
| a description cut landing mid-rune deletes the partial rune (two-byte) | one entry: `commandEntryWithFixture(commandEntryFixture("deep-research"), "description", strings.Repeat("d", slashCommandDescriptionCapFixture-1)+twoByteRune+"z")` | `{Name: "deep-research", Description: strings.Repeat("d", slashCommandDescriptionCapFixture-1), TruncatedFields: []string{"description"}}` |
| a description cut landing mid-rune deletes the partial rune (four-byte) | one entry, `…CapFixture-3` d's + `fourByteRune` + `"z"` | `Description` is `…CapFixture-3` d's, `TruncatedFields: []string{"description"}` |

The mid-rune rows carry a single entry, mirroring the name's mid-rune rows exactly. The at-cap row carries the trailing short entry its name-side twin carries, for the same non-vacuity reason.

**The `why` fields — and the one trap in them.** The name at-cap row's `why` claims to be *"the only one that reddens on truncateField's `<=` becoming `<`."* **The description at-cap row must not copy that claim, because it is false for this field.** `truncateField` is shared: flipping its operator reddens the name at-cap row too, so the description row is *a* red, never the sole one. The same holds for the mid-rune rows against a scrub that replaced instead of deleted — the name's mid-rune rows already redden on it.

What each new row **is** the sole red for is an **inlined per-field cut**: a future edit that replaces the description's `bound` call in `emitSlashCommandList` with its own length test and its own scrub, leaving the name's path on the shared helper. Under that mutant the name rows all stay green.

- The at-cap row is the sole red on that inline written with `<` (or any off-by-one at the boundary).
- The mid-rune rows are the sole reds on that inline scrubbing with a replacement rather than a deletion, or omitting the scrub.

Each `why` must state that mutant and say plainly that the shared-`truncateField` mutant reddens the name rows too. This family's code review mutates every sole-red claim (see `docs/knowledge/features/streamsup-package-the-commands-only-rung-s-emit-lands.md`), and an overstated one is a finding.

The mid-rune rows additionally record **why they are constructed rather than drawn from the capture** — at 256 no capture entry is mid-rune reachable — and **point at `maxSlashCommandDescription`'s doc** for that measurement. Do not restate the numbers: a second copy is a second thing to go stale.

The rows stay on the models rung (`commandsOnly` unset), where the whole matrix is measured. The commands-only row remains the single rung measurement; a second copy of it would be a second copy of a cap rather than a second proof of it.

### 2. The `Description` UTF-8 assertion in the shared harness

The per-entry block asserts `utf8.ValidString` on `Name` but not on `Description`, and its comment says so deliberately — *"the description's mid-rune rows are the sibling ticket's, so one here would have nothing to grade."* Those rows land here, so the check lands with them, mirroring `Name`'s placement and phrasing, and that comment is replaced by the `Name` check's own framing: live on the two mid-rune rows, a no-op on the rest, with the byte length above it the assertion that actually carries them.

### 3. `TestParser_InitializeControlResponseCutsTheCapturedDescriptions`

A **new** test function, not an addition to `TestParser_InitializeControlResponseCountsTheCapturedCommands` — whose own comment defers exactly this pin and calls asserting it there *"that pin in the wrong test."* It runs inside `make check`: no file in `internal/streamsup` carries a build tag, and `e2e_realclaude` governs the other package's Go files, not its JSON bytes. No live claude, no credentials.

Structure, copying `TestParser_InitializeControlResponseCountsTheCapturedCommands`'s skeleton:

```go
func TestParser_InitializeControlResponseCutsTheCapturedDescriptions(t *testing.T)
// t.Parallel(); for arm := range initCaptureArms { continue on initCaptureArmNoRequest;
//   t.Run(initCaptureArmLabel(arm), parallel subtest) }
```

Inside each subtest, in order:

**(a) The capture-side guard, before the capture is used as an expectation.** One pass over `capturedCommandEntries(t, arm)` reading each `description` with `capturedCommandString`, building `[]string` of the names whose description exceeds `slashCommandDescriptionCapFixture` bytes. `slices.Equal` against the committed ten-name literal in array order, fatal, naming the arm. This is the pin a re-capture must break loudly — the `nameOutsideSlug` precedent one field over.

The same pass computes, per over-cap entry, the length the value cuts to — `len(truncateField-equivalent)`, i.e. the byte prefix after the invalid-sequence deletion — and asserts it is exactly `slashCommandDescriptionCapFixture` for all ten. That single assertion **is** the "no capture entry is mid-rune reachable" guard: it needs no separate check and no restatement of the constant's measurement, and it is the precondition the `dataviz` pin below depends on.

**(b) The `claude-api` control-character guard.** Assert the capture's own `claude-api` description carries **exactly two** `0x0a` bytes, the first below the cap and the second at or above it. Fatal, with a message saying a re-capture changed the one control-character proof this test rests on. Derive the surviving offset here; do not transcribe 152 or 755.

**(c) Replay and shape.** `p.Write(capturedInitializeLine(t, arm) + "\n")`; expect 2 events, `events[1].(turnevent.SlashCommandList)`, `len(list.Commands) == len(want)` (fatal — the loop indexes both sides).

**(d) AC 2, first clause — exactly those ten and no others.** Collect `got.Name` for every emitted entry where `slices.Contains(got.TruncatedFields, "description")`, and compare with `slices.Equal` against the same ten-name literal. **Exact equality of the whole set, never a per-entry `Contains` sweep**: equality is what makes "and no others" structural, so deleting or widening the cut reddens this and nothing else has to be enumerated.

**(e) AC 2, second clause — `dataviz` arrives cut and self-reported.** Pinned **by name** rather than left to ride anonymously inside (d), for `nameOutsideSlug`'s stated reason: assert the capture's own `dataviz` description is over the cap by a wide margin (its uncut length, read from the capture, not transcribed), that the emitted `Description` is exactly `slashCommandDescriptionCapFixture` bytes, and that it equals the capture's own first `slashCommandDescriptionCapFixture` bytes — a **verbatim prefix**, not merely a length. Use `slashCommandNamePreview` in the failure message; a bare `%q` of 256 bytes is two screens.

**(f) AC 2, third clause — the surviving newline crosses verbatim.** On the emitted `claude-api` entry: exactly one `\n`, at the offset (b) derived from the capture, and the value is the capture's own 256-byte prefix. The second newline being cut away is what "exactly one" states.

Its doc comment says what it pins, that it is the pin `TestParser_InitializeControlResponseCountsTheCapturedCommands` defers, and why each guard exists. **It does not restate `maxSlashCommandDescription`'s derivation** — one sentence pointing at that doc.

### 4. The rename and its cites

`TestParser_SlashCommandNamesAreCapped` → **`TestParser_SlashCommandFieldsAreCapped`**, mirroring `TestParser_ModelListFieldsAreCapped` one array over. Its doc's opener (*"the per-name bound's whole boundary matrix PLUS the description bound's LIVENESS rows"*) becomes both bounds' whole matrices, and the sentence deferring the description's matrix and capture pin to the sibling ticket goes — the sibling is this ticket.

25 references across 8 files. Edits:

- `internal/streamsup/parser_test.go` — 5 references, one `replace_all`.
- Six `docs/specs/architecture/*.md` build artifacts (#1878, #1885, #1886, #1890, #1891, #1904) — 19 references, one `replace_all` each. **The symbol token only; no prose changes.**
- `docs/knowledge/features/streamsup-package-the-commands-only-rung-s-emit-lands.md` — **leave alone.** The documentation phase owns it and runs on this ticket after code review.

**`slashCommandNamePreview` is deliberately not renamed.** Its doc already reads *"bounds one name or description"*, so the name is a mild wart rather than a stale claim, and renaming it is cascade the ticket did not ask for.

### The three in-tree forward references this ticket discharges

All three are in `parser_test.go`; none is in production, so the no-production-edits rule is not in tension with any of them. Grep-verified 2026-09-01 — these are the only three.

1. The `TestParser_SlashCommandNamesAreCapped` doc's *"is the sibling ticket's and is deliberately not here."*
2. The harness's *"the description's mid-rune rows are the sibling ticket's, so one here would have nothing to grade."*
3. `TestParser_InitializeControlResponseCountsTheCapturedCommands`'s *"How many captured descriptions report, and which, is the description's committed-capture pin — the sibling ticket's, and asserting it here would be that pin in the wrong test."* → name `TestParser_InitializeControlResponseCutsTheCapturedDescriptions`. **A shipped forward reference that outlives the ticket it points at misdescribes it**; leaving this one is a guaranteed code-review finding.

## Concurrency model

None introduced. Both tests are `t.Parallel()` at the top level and inside each arm subtest, matching every sibling in the file. The capture readers are pure functions over committed bytes; `collectEvents` and the per-arm `NewParser` are per-subtest locals with no shared state.

## Error handling

Test-only, so this is failure-message discipline:

- **Capture guards are `t.Fatalf` and name the arm.** They run before the capture is used as an expectation, so a re-capture fails at the guard saying what proof it took away, rather than silently narrowing what the comparison below proves.
- **Length assertions precede value assertions**, and every message printing a capped value goes through `slashCommandNamePreview`.
- **The entry-count check is fatal**, both tests indexing both sides at `i`.
- **Set comparisons are `slices.Equal` over the whole set**, so the failure prints both sets and the diff is readable.

## Testing strategy

`make check` is the whole gate: `internal/streamsup` carries no build tag and the capture is committed JSON. `make e2e-realclaude` is **not** needed and must not be invoked — nothing here needs claude or credentials.

Mutants the developer should confirm before opening the PR, each expected to redden exactly what its row's `why` claims:

- Replace the description's `bound` call in `emitSlashCommandList` with an inlined `<` test → **only** the description at-cap row reddens.
- Inline that cut with a `�` replacement instead of the deletion → **only** the two description mid-rune rows redden.
- Flip `truncateField`'s `<=` to `<` → the description at-cap row **and** the name at-cap row redden. This is the measurement that keeps the `why` fields honest; a `why` claiming sole-redness here is wrong.
- Drop one name from the ten-name literal → the capture guard **and** the emitted-set comparison both redden, and nothing else does.
- Strip newlines from `Description` in the emitter → **only** the `claude-api` clause reddens.

Restore production after each; **no production change ships on this branch.** If a row cannot be made red without a production edit, that is a finding about #1904's implementation and belongs in a comment on this ticket.

## Open questions

- **Array order vs longest-first for the ten-name literal.** This spec picks claude's array order (cheaper, pins ordering). If the developer finds the derivation reads better sorted by length, either is acceptable *provided the literal and the derivation agree* — a literal in one order compared against a set built in the other is a green test that proves nothing.
- **Whether the six spec-artifact cite edits are worth making at all** is PO's call, already made in AC 3: closed tickets' build artifacts describe the tree as it was. This spec follows the AC. If code review objects to rewriting historical artifacts, that is a conversation for the ticket, not a developer decision.

## Size check

Re-counted against this written spec, not against the sketch:

| Boundary | Limit | This spec |
| --- | --- | --- |
| Production source files created or modified | ≤ 3 | **0** — test file and `.md` only |
| Total written work | ≤ 400 | **~300** (3 rows ≈ 60, harness check + comment ≈ 12, doc-framing rewrites ≈ 20, new capture test ≈ 160, 24 cite edits ≈ 24) |
| New exported types or interfaces | ≤ 5 | **0** |
| Consumer call sites needing simultaneous update | ≤ 10 | **5** Go references, all in the one file already being edited |
| Acceptance criteria | ≤ 5 | **3** |
| Distinct error/reject branches in a state machine | ≤ 10 | **0** |

**On the call-site line, stated plainly so a reviewer can check the call.** The rename has 25 total references, and 24 are in scope. Five are Go — all inside `parser_test.go`, the file this ticket edits anyway, resolved by one `replace_all`. The other nineteen are **markdown prose citations** across six spec artifacts: they are counted under the line budget (24 lines, six `replace_all` edits), not under the call-site column. The column's calibrating failures — pyrycode #75's 26 `NewServer` call sites and #29's interface rename across five test files — were both Go cascades where each site needed surrounding-code reasoning and a rebuild, and where build failures sent the agent back file by file. A markdown sentence naming a test has no build, no cascade and no per-site semantics. This is a category call about what the column measures, **not** a "they're mechanical" discount of edits that do belong in it; if the reviewer disagrees with the category, the cut is to lift the rename and its cites into a child of their own, leaving the rows and the capture pin where they are.

Nearest analogue, re-derived: #1904's `56b131b8` test-file half is **163** insertions+deletions for three rows in this same table plus the `commandEntryWithFixture` helper. This ticket's three rows are cheaper — helper, both cap fixtures and both rune fixtures all exist — and the capture pin is scaffolded on `capturedCommandEntries`, `capturedCommandString`, `initCaptureArms` and `initCaptureArmLabel` rather than building them, which is what makes #1878's `2a1094ac` (**469**, one file, test-only, shipped `size:s`) an overstated upper bound here.

The live risk is not the count — it is **prose density**. #1904's own lesson records a spec estimating 265–350 landing at ~500, almost entirely in doc prose, because ten `//`-corrections cost a paragraph each. This ticket's correction surface is three forward references and one doc opener, and its production doc surface is zero. Keep the new test's doc comment to what it pins and one pointer at `maxSlashCommandDescription`; do not re-argue the derivation.

## Security review

**Verdict:** PASS

**Findings:**

- **[Trust boundaries]** No new boundary; this ticket pins an existing one. `commands` entries are **workspace-authored** strings that crossed the subprocess trust boundary at `Parser.Write` and are bounded at construction in `emitSlashCommandList` via `truncateField`. The boundary is explicit and single-sited — the `bound` closure inside the per-entry loop — and this ticket's whole purpose is to keep it that way: the mid-rune and at-cap rows are the sole reds on an **inlined per-field cut**, which is precisely the refactor that would scatter the boundary across two code paths.
- **[Network & I/O — input size limits]** No findings, and this is the category the ticket lives in. `maxSlashCommandDescription = 256` is the cap; the retained-bytes ceiling argument against `maxUnrecognizedRaw`'s 16 KiB is in the constant's doc. The **at-cap row is the assertion that keeps the bound from loosening by one byte**, and the capture pin is what keeps a real 1145-byte workspace value from riding uncut into a retained `turnevent.SlashCommandList`. Before this ticket both claims were prose in `maxSlashCommandDescription`'s doc with nothing red on them.
- **[Error messages, logs, telemetry]** SHOULD FIX, and the spec already addresses it: every failure message printing a capped value goes through `slashCommandNamePreview`, whose head is itself scrubbed with `truncateField`'s replacement. A bare `%q` of a captured description would put up to 1145 bytes of **workspace-authored content** into test output; the two capture-pinned values (`dataviz`, `claude-api`) are the longest in the capture and `claude-api` contains raw newlines that would break the message across lines. Code review should check no new message prints a description unbounded.
- **[Injection / content-crossing — the newline clause]** **MUST NOT OVERSTATE, and the spec is written to prevent it.** `0x0a` is the only sub-`0x20` byte anywhere across the entries' string fields (two occurrences, both in `claude-api`). The assertion is therefore *"this surviving newline crosses verbatim at its original position"* and never *"control characters survive"* — the capture cannot show the second claim, and a test asserting it would be pinning breadth the evidence does not carry. Sanitization is the client's render boundary, per #1600's verbatim rule; nothing here loosens that.
- **[Subprocess / external command execution]** Not applicable by construction. Test-only, no `exec.Command`, no new argv, no environment. The captured payload is committed JSON replayed through `Parser.Write`; no live claude and no credentials are involved on any path this ticket touches.
- **[Tokens, secrets, credentials]** Not applicable. No secret material is read, written, logged or compared. The capture files are committed workspace command inventories with no credential content.
- **[File operations]** Not applicable. No path is constructed from any input; the only files read are the four committed `initialize_control_v2.1.239*.json` fixtures, through the existing `capturedInitializePayload`.
- **[Cryptographic primitives]** Not applicable. No randomness, no comparison against a secret. Set comparisons use `slices.Equal` on plain names, which is correct — these are not secrets and constant-time comparison would be cargo-culted here.
- **[Concurrency]** No findings. Both tests are `t.Parallel()` with per-subtest parser instances; no lock is taken and no state is shared beyond the read-only committed fixtures.
- **[Threat model alignment]** In scope and addressed: the threat is a workspace-authored value of unbounded length reaching a retained event and, from there, a push queue. Out of scope and named: the **entry-count** bound on this array is #1826's, and the wire publication of the field is #1720's. Neither is weakened by anything here.

**Reviewer:** architect (self-review per the security-review checklist)
**Date:** 2026-09-01
