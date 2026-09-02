# #1958 — the argument hint's mid-rune rows, its UTF-8 check, and its committed-capture pin

Test-only. No production file is touched.

## Files read

| Path | Symbol | Why it matters |
|---|---|---|
| `internal/streamsup/parser.go` | `maxSlashCommandArgumentHint` | Owns the measurement this slice cites rather than restates: 51 hints, 33 empty, longest 121 bytes, none over 128, and **MID-RUNE IS UNREACHABLE ON THE LIVE PATH at this cap**. The mid-rune rows' "why constructed" clause points here. |
| `internal/streamsup/parser.go` | `truncateField` | The scrub is a byte cut plus `strings.ToValidUTF8(_, "")` — a **deletion**, not a replacement. That is what the two new rows grade, and it is SHARED by all three fields. |
| `internal/streamsup/parser.go` | `emitSlashCommandList` | The three `bound` calls in declaration order; the hint's is the second. The inlining mutants below are mutations of its third statement. |
| `internal/streamsup/parser_test.go` | `TestParser_SlashCommandFieldsAreCapped` | The matrix the two rows join, and the per-entry assertion loop the UTF-8 check joins. Its hint block's comment is the one cite this widening falsifies. |
| `internal/streamsup/parser_test.go` | `TestParser_InitializeControlResponseCutsTheCapturedDescriptions` | The shape to copy: guard the capture FIRST naming the arm, then replay, then compare the reporting set by **exact equality** and not a per-entry `Contains` sweep. Its header states why it is a test of its own. |
| `internal/streamsup/parser_test.go` | `TestParser_InitializeControlResponseCountsTheCapturedCommands` | The other candidate home. Its own comment calls a cap-outcome assertion placed there "that pin in the wrong test", and it carries the `nameOutsideSlug` pin-by-name idiom the non-ASCII entry copies. |
| `internal/streamsup/parser_test.go` | `capturedCommandEntries`, `capturedCommandString`, `commandEntryWithFixture`, `capturedInitializeLine`, `initCaptureArms`, `initCaptureArmNoRequest`, `slashCommandArgumentHintCapFixture` | Every helper this slice needs already exists and already takes claude's key as a parameter. Nothing is widened and no helper is minted. |
| `docs/knowledge/features/streamsup-package-producing-turnevent-slashcommandlist.md` | whole | Three lessons that shape this slice: *a row's claimed exclusivity needs checking against its ASSERTIONS, not just its inputs*; *build the mutant before writing the counterfactual*; *a falsified `//` claim phrased about a category is the one grep misses*. |
| `docs/specs/architecture/1957-slash-command-argument-hint.md` | § "On sole-redness claims", § "Scope boundary with #1958" | Names this slice's contents exactly, and mandates measuring a sole-red claim via `go test -overlay=<abs-path json>` rather than reasoning it. |

## Context

`maxSlashCommandArgumentHint` shipped in #1957 with its liveness, at-cap, empty/`null`/omitted and two declaration-order rows. Two things were left, and this slice is both: the **mid-rune pair** with the UTF-8 check that grades them, and the **committed-capture pin**, which does not exist for this field in any form.

This field's captured pin is a *nothing was cut* pin. Re-derived against the three responding arms at `c58995bb`: 51 entries, `argumentHint` present on all and absent from none, 33 present-and-empty, longest 121 bytes on `auto-mode-setup` (the only hint carrying non-ASCII), 530 bytes in total, **none over 128**, and the three arms byte-identical on this field. At the shipped cap of 256 no captured hint is cut at all. So where the description's pin proves a cut lands correctly, this one proves the array survives whole.

No ADR is warranted: this slice introduces no decision, it pins one already made and documented on `maxSlashCommandArgumentHint`.

## Design

### Where the pin lives — its own test, and the precedent points that way

The description's pin is a test of its own because the count pin's subject is the fifty-one-entry **inventory** and its own is **the entries a cap fired on**. For a field whose pin asserts *survival* that separation reads the same way, and one step more sharply: the count pin's `TruncatedFields` assertion was narrowed to `slices.Contains(got.TruncatedFields, "name")` by #1904 precisely because a whole-slice cap claim does not belong there. A hint claim placed in that loop would be a second cap outcome asserted inside the inventory test — the thing that narrowing established as wrong. It would also have to be a per-entry `Contains` sweep to fit the loop, and this slice's AC asks for exact set equality, which is the cut pin's idiom and not the inventory's.

So: `TestParser_InitializeControlResponseCarriesTheCapturedArgumentHints`, a sibling of `…CutsTheCapturedDescriptions`. The verb states the fact — the hints are **carried**, not cut.

### The pin's shape

Per responding arm (`initCaptureArms`, skipping `initCaptureArmNoRequest`), guards on the capture first, each failure naming the arm:

- 51 entries, every one carrying a **string** `argumentHint`. `capturedCommandString` supplies the second half by failing when the key is absent or not a string, so "all 51 present, none absent" needs no guard beside it.
- exactly 33 empty, so "the empty hint is the ordinary case" is the capture's own fact and not a transcription.
- **none over the cap.** This is the guard that makes the empty expected report set a measurement of *nothing was cut* rather than a coincidence, and it is where "the capture changed" is separated from "the cut broke": a re-capture that lengthened a hint past 256 fails HERE, with a message saying so, instead of reddening the comparison below as though the emitter regressed.
- `auto-mode-setup` pinned **by name**, `nameOutsideSlug`'s idiom: 121 bytes, and carrying a rune whose encoding is more than one byte (derived by comparing byte length against rune count, transcribing no offset). A re-capture that swapped it out must fail with a message naming what was lost.

Then replay `capturedInitializeLine`, assert two events (`ModelList` then `SlashCommandList`), fatal on the entry count, and:

- every hint compared **index for index by exact equality** against the capture's own bytes — which carries "all 51 arrive verbatim", "the 33 empty ones arrive as entries carrying an empty hint" and "`auto-mode-setup`'s 121 bytes whole" in one comparison, since a defaulted, elided or re-encoded value differs from claude's;
- `auto-mode-setup` asserted again by name, so the non-ASCII proof does not ride anonymously inside fifty-one comparisons;
- the set of entries reporting `argument_hint` collected and compared to a nil `[]string` by `slices.Equal`, with the comment stating the one vacuity risk out loud: an empty expectation also passes against a producer reporting nothing at all, and what makes it a measurement is the **liveness row already in `TestParser_SlashCommandFieldsAreCapped`**, which proves the report can fire for this field.

### The two rows

Beside the description's, using `commandEntryWithFixture(commandEntryFixture("deep-research"), "argumentHint", …)` and the existing `twoByteRune` / `fourByteRune` constants:

- **one byte under** — `strings.Repeat("h", slashCommandArgumentHintCapFixture-1) + twoByteRune + "z"` → hint cut to the fill, `TruncatedFields: []string{"argument_hint"}`.
- **three bytes under** — the same with `fourByteRune` and `-3`, which is what makes "1–3 bytes under the cap" a range rather than a one-byte anecdote.

Inputs are **constructed**, and the `why` cites `maxSlashCommandArgumentHint`'s doc for the reason rather than restating the measurement.

Each `why` scopes its sole-red claim to the **pair**, per the AC and per #1957's warning that this exact claim has been plausible and wrong twice in this matrix. The claims are **measured in Phase B** and written after, never before — the package overview's own lesson. The mutants to run, each via `go test -overlay=<abs-path json>` so nothing is written into the worktree:

| Mutant | Claim under test |
|---|---|
| the hint's `bound` call inlined with a **replacing** scrub | the pair is the sole red |
| `truncateField`'s scrub made a replacement | the pair is *a* red and never the only one — the name's and description's mid-rune rows redden with it |
| a hint that fits stripped of non-ASCII | the captured pin's own sole red, no cap-table row carrying a multi-byte rune through **uncut** |
| an absent hint defaulted to a placeholder | which of the cap table's empty row and the captured pin catches it, so neither claims the other's ground |

Whatever the measurements say is what the `why` blocks say. A claim that does not survive its mutant is rewritten to state what the row *does* separate.

### The UTF-8 check and the comments it falsifies

The hint's `utf8.ValidString` check lands after its equality block and before the description's block, holding the field's position on the type. The comment justifying its absence is replaced by one saying what it grades — live on the two new rows, a no-op elsewhere — and stating explicitly that the **byte-length assertion above it and not the check** is what carries them, a replacing scrub keeping the check green.

Two adjacent cites are also touched, both inside `TestParser_SlashCommandFieldsAreCapped`'s header:

- the sentence naming the description's committed-capture pin as the one that is "NOT here", widened to name both;
- the "**TWO** such rows rather than one … `truncateField` is SHARED by **both fields**" paragraph, which went stale when #1957 added the hint's at-cap row and now reads three ways wrong beside the new rows' own `why`. Corrected to three, minimally — this is the arity my rows' sole-red claims must agree with, so leaving it would make the file disagree with itself.

## Error handling

Nothing runs in production. Every failure path is a `t.Fatalf` or `t.Errorf`. The split is the sibling's: **fatal** for a broken precondition (a capture whose shape has moved, an entry count that would make the loop below index past the end), **error** for a producer disagreeing with a sound expectation, so one run reports every disagreement rather than the first.

## Testing strategy

`go test -race ./internal/streamsup/...`, `go vet ./...`, `go build ./cmd/pyry`. RED first: the two rows and the pin are written against the current tree and must fail for the right reason before anything else — but there is no production change here, so RED is established by **mutation** instead, which is the same evidence the sole-red claims need. No live claude, no credentials, no tokens: the `e2e_realclaude` build tag governs `internal/e2e/realclaude`'s Go files and not its testdata, and `internal/streamsup` carries no build tag.

## Open questions

1. **Do the mutant measurements support the pair's sole-red claim?** Resolved in Phase B by running them; the `why` blocks are written from the results, and a `## Revisions` entry records any claim the measurement contradicted.
2. **Does the captured pin have a sole red of its own, or only redundant coverage?** The candidate is a non-ASCII mangling of a value that fits, which no cap-table row can see because every hint row's surviving output is ASCII. Measured, not assumed.

## Revisions

**2026-09-02 — the four mutants were run; both open questions resolved, no design change.** Each was applied to `parser.go` through `go test -count=1 -overlay=<abs-path json> ./internal/streamsup/`, so nothing was written into the worktree. Results, which are what the `why` blocks now say:

| Mutant | Reddened | Verdict |
|---|---|---|
| the hint's `bound` call inlined with a **replacing** scrub | the two new rows and **nothing else in the package** | the pair's sole-red claim holds, scoped to the pair |
| `truncateField`'s scrub made a replacement | all six slash-command mid-rune rows **plus four other tests'** (`ModelList`, `ModelAnnounced`, `TaskStarted`, `TaskUpdated`, `BackgroundTaskRoster`) | the pair is *a* red there and never the only one, as written |
| a hint that fits stripped of non-ASCII | **only** the captured pin, on all three arms | **Open question 2: yes.** The pin has a sole red of its own — no cap-table row carries a wide rune through *uncut*, the mid-rune rows' rune being the one the cut deletes |
| an absent hint defaulted to a placeholder | the captured pin **and most of the cap table**, `commandEntryFixture` omitting the key on every entry that does not ask for one | the pin claims defaulting as coverage, never as its own; recorded in its doc |

Open question 1 is resolved by the first two rows of that table: the claim as drafted survived its mutant unchanged, so nothing was rewritten.

## Security review

**Verdict:** PASS

**Findings:**

- **[Trust boundaries]** SHOULD FIX — the boundary is real and this slice pins two of its properties. `emitSlashCommandList` is where claude's subprocess stdout, whose `argumentHint` values are **workspace-authored** and so attacker-influenceable on a hostile workspace, becomes a `turnevent.SlashCommand` retained in daemon state. The pin's per-entry equality over all 51 hints asserts they cross **verbatim**, and a comment phrased broadly — "every byte crosses untouched" — would claim a breadth these bytes cannot show and would read as a licence for a future consumer to skip escaping. Measured against the capture for this review: **zero codepoints below 0x20 in any hint**, and exactly one non-ASCII codepoint anywhere, U+2026, three occurrences, all inside `auto-mode-setup`. In Phase B the claim is scoped to those bytes explicitly, `TestParser_InitializeControlResponseCutsTheCapturedDescriptions`' own narrow newline claim being the precedent, and sanitization is named as the render boundary's — the same division `maxSlashCommandName`'s doc states.
- **[Tokens, secrets, credentials]** SHOULD FIX — no credential is read, written or generated on any path, and no capture is added; the file is committed and this slice only reads it. The one way this slice could newly expose content is a **failure message dumping a whole workspace-authored value into CI output**. `slashCommandNamePreview` bounds a print at 16 bytes plus a length, and every value print in the two rows and the pin — the capture-shape guard messages included, which is where an unbounded `%q` is easiest to write by habit — goes through it in Phase B.
- **[File operations]** No findings, by design rather than by assertion: the pin opens nothing. It reaches the capture through `capturedInitializePayload`, whose reader takes an **arm selector from the closed `initCaptureArms` set, never a path**, mints the path from package constants, and fatals on an arm it does not declare. There is no caller-supplied path component, so no traversal and no TOCTOU; nothing is created, so no mode decision exists.
- **[Subprocess / external command execution]** No findings — nothing is executed. `capturedInitializeLine` replays committed bytes through `p.Write`, which is the same code path a live `claude` line takes minus the process.
- **[Cryptographic primitives]** No findings — no randomness of any kind. Every fixture is `strings.Repeat` over a fixed literal, and both mid-rune inputs are deterministic by construction, which is also what makes the mutation measurements repeatable.
- **[Network & I/O]** No findings — and the resource bound is this slice's actual subject: `maxSlashCommandArgumentHint` is what stops a workspace hint riding unbounded into the event stream, the push queue and the logs. The one shape that could quietly stop guarding it is the **empty expected report set**, which passes against a producer reporting nothing at all. The design carries both mitigations: the "none over the cap" capture guard, which reddens naming the capture when a re-capture invalidates the premise, and the stated dependence on `TestParser_SlashCommandFieldsAreCapped`'s liveness row, which proves the report can fire for `argument_hint` at all.
- **[Error messages, logs, telemetry]** No findings beyond the print bound above — this test adds no log and asserts none. `collectEvents` builds its parser with `discardLogger()`, so no captured workspace value reaches a handler on any path here; the content-leak sweep over the rungs is a different test and is untouched.
- **[Concurrency]** No findings — `t.Parallel()` at the function and the subtest level, matching both siblings. Each subtest constructs its own parser and its own event slice, and the emit callback runs synchronously inside `p.Write`, so no goroutine is spawned and nothing needs a shutdown path. Arms cannot alias: `capturedInitialize` caches nothing, reading and decoding the file afresh per call, so two parallel arms hold two distinct maps.
- **[Threat model alignment]** OUT OF SCOPE — `turnevent.SlashCommandList` is **produced but not published**: `turnbridge.MapEvent` gains no arm and `interactiveTurnEmitterV2.Handle` no case, so no hint reaches a device today and `protocol-mobile.md` § Security model binds the future publishing slice rather than this one. The escaping obligation at that render boundary is named here precisely so this pin's verbatim claim is not later read as a finding that escaping is unnecessary. It is #1720's lineage, not #1825's, which is the next field in this family.

**Reviewer:** builder (self-review per the security-review checklist)
**Date:** 2026-09-02
