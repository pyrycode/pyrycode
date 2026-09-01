# #1957 — Decode each slash command's argument hint, cap it, and carry it on the emitted entry

**Ticket:** [#1957](https://github.com/pyrycode/pyrycode/issues/1957) · `size:s` · `security-sensitive`
**Parent:** #1830 (umbrella), grandparent #1824 — this is a depth-2 grandchild, so the split-depth gate applies and no further split may be proposed for it.
**Sibling that takes the committed-capture pin:** #1958 (natively blocked by this ticket).

---

## Files to read first

Symbols, not line numbers — resolve each with `codegraph_search` / `codegraph_node`, then Read the block whole. `make cite-guard` bans `file.go:NNN` in `//` comments at any depth, with no range and no 20-line exemption, so nothing below gives you one to copy.

| Where | Symbol | What to extract |
|---|---|---|
| `internal/streamsup/parser.go` | `maxSlashCommandDescription` | The doc SHAPE the new constant must copy: measurement → multiple → why it differs from its neighbours → ceiling fraction → one-constant-per-meaning. Also **three of the five stale figures** live in this doc. |
| `internal/streamsup/parser.go` | `maxSlashCommandName` | Its *THE PER-ENTRY TERM* paragraph is the two-term sum this slice makes three-term. Its 10.7x-over-the-longest is the multiple whose **base** this slice must name. |
| `internal/streamsup/parser.go` | `commandEntryLine` | The decode target gaining `ArgumentHint`. Seven separate claims inside its doc go stale — see § The correction manifest. Its sink-enumeration paragraphs are the model the hint's own security re-walk copies. |
| `internal/streamsup/parser.go` | `emitSlashCommandList` | The `bound` closure and the construction loop. The **call sequence** is what orders `TruncatedFields`; the new call goes BETWEEN the two existing ones, not after them. |
| `internal/streamsup/parser.go` | `logControlResponse` | Its `commands` paragraph draws an UNREACHABLE-by-omission vs UNWRITTEN distinction that this slice moves `argumentHint` across. A re-argument, not a sentence edit. |
| `internal/streamsup/parser.go` | `truncateField` | The `<=` boundary and the empty-replacement scrub (a cut lands 1–3 bytes short when it lands mid-rune). The cut semantics the new cap inherits unchanged. |
| `internal/turnevent/event.go` | `SlashCommand` | The type doc's fixed-declaration-order promise, and `TruncatedFields`' enumeration of what it may carry. `ArgumentHint` goes between `Name` and `Description`. |
| `internal/turnevent/event.go` | `SlashCommandList` | Its SECURITY paragraph owns the never-sanitized statement for every string on the type — the new field doc points at it rather than restating it. |
| `internal/protocol/interactive.go` | `SlashCommand` | The wire type, already declaring `ArgumentHint` with tag `argument_hint` and already enumerating it in `TruncatedFields`. **Read-only for this ticket** — no protocol change. |
| `internal/streamsup/parser_test.go` | `commandEntryWithFixture`, `commandEntryFixture` | The fixture builders this slice REUSES. `commandEntryFixture` builds the `name` key only, which is why no existing row gains a hint by accident. |
| `internal/streamsup/parser_test.go` | `TestParser_SlashCommandFieldsAreCapped` | The boundary matrix the liveness rows join. Read its rows' sole-redness `why` claims before adding beside them. |
| `internal/streamsup/parser_test.go` | `TestParser_InitializeControlResponseCountsTheCapturedCommands` | Its `slices.Contains(got.TruncatedFields, "name")` narrowing — #1904 had to make exactly that edit when the second field landed. Check whether a third field re-breaks it (it does not; see § Testing). |
| `cmd/pyry/interactive_turn_v2_test.go` | `emitterSlashCommandListFixture`, `emitterSlashCommandListSentinels` | The log-leak negative. Its doc **already states** that each field-adding slice owes a sentinel and a line here. Note the empty-string trap in § Testing. |
| `docs/knowledge/features/streamsup-package-the-commands-only-rung-s-emit-lands.md` | § `Description` joins the per-entry shape (#1904) | The predecessor's own lessons, including the one that says a ten-item correction list costs a paragraph each and names #1830 by number as the ticket that should cost it in. |
| `docs/knowledge/features/streamsup-package-producing-turnevent-slashcommandlist.md` | whole | Three lessons this spec leans on: a category-phrased `//` claim is the one grep misses; a spec-inherited sink enumeration still needs checking against the call graph; a sole-redness claim is measurable, not descriptive. |

---

## Context

`internal/streamsup`'s `control_response` arm decodes claude's `commands` array — the workspace's slash-command inventory — into a bounded daemon value. `commandEntryLine` declares two of the four keys claude publishes; `emitSlashCommandList` copies each into a `turnevent.SlashCommand` through a per-entry `bound` closure that cuts at a per-field byte cap and appends the cut field's daemon name to that entry's `TruncatedFields`.

`argumentHint` is the third key. Its position is dictated rather than chosen: `turnevent.SlashCommand`'s doc reserved the slot between `Name` and `Description` so the eventual mapping onto `protocol.SlashCommand` stays a field-for-field copy.

Three things make this slice more than "one more field":

1. **An empty hint is the ordinary case.** 33 of the capture's 51 entries carry `""` and none omits the key. `protocol.SlashCommand`'s doc already drew the consequence for the wire; this slice states on the daemon type *why* the `encoding/json` collapse of absent/null/present-empty costs nothing here, rather than leaving a reader to infer it.
2. **claude's key and the daemon's name stop coinciding here.** `name` and `description` are byte-identical as claude's key, as the daemon's report name and as the wire tag. This field is claude's `argumentHint` against the daemon and wire `argument_hint`. `emitSlashCommandList`'s `bound` comment already predicts this and names the wrong ticket for it.
3. **A third term lands in an arithmetic five shipped paragraphs already state as two.** The per-entry budget is `256 + 256 = 512` in five places; every figure that sum fed moves. This is the bulk of the work — see § The correction manifest, which pre-derives all of it so the developer does not have to.

No ADR is warranted: this slice makes no decision the family's existing doctrine does not already settle. The one genuinely new convention — naming the **population** a cap's multiple is taken over — belongs in the constant's own doc and in the package overview the documentation phase writes, not in a decision record.

---

## Design

### 1. The new cap constant

Declare `maxSlashCommandArgumentHint = 256` in `internal/streamsup/parser.go`, immediately after `maxSlashCommandDescription`.

The doc must carry, in `maxSlashCommandDescription`'s own shape:

**MEASURED** (all figures re-derived 2026-09-01 against `8bcd14b9`, which is current `origin/main`; the capture is `internal/e2e/realclaude/testdata/initialize_control_v2.1.239.json` read through `capturedInitializePayload`):

- `argumentHint` is **present on all 51 entries and absent from none**; **33 are present-and-empty**, 18 carry text.
- Longest **121 bytes** (`auto-mode-setup`, the only hint carrying non-ASCII); then 77, 55, 44, 41.
- Over the 18 non-empty: mean **29.4**, median **17.5** — an even count, so the median is the mean of the 9th and 10th of the sorted 18 (**16** and **19**), not the 10th alone.
- Over all 51: mean **10.4**, median **0**.
- **7 exceed 24 bytes, 5 exceed 32, 2 exceed 64, none exceeds 128.**
- Total across the 51: **530 bytes** — against the names' 494 and the descriptions' 10,580.
- No hint carries a sub-0x20 byte. Across the whole array `0x0a` appears only in `claude-api`'s description.

**NAME THE POPULATION.** This is the first field in the family where the choice is live, and the doc must say which base it used before it states a multiple. `maxSlashCommandName` states its multiple over the **longest** (`256 / 24 = 10.7x`); `maxSlashCommandDescription` states its over the **mean and median of all 51** (`~1.2x`, `~3.7x`). Neither had to flag the choice, because `name` and `description` are non-empty on every entry so the two populations coincide. Here they do not: **"median 17.5" and "median 0" are both true of this field**, and over all 51 the median is 0, so *no multiple over it exists at all*. This constant states its multiple over the **longest**, `256 / 121 = 2.1x` — `maxSlashCommandName`'s base, named explicitly so the figure is not read against the description's `3.7x`, which is over a different population. State the other bases' figures too (`8.7x` the non-empty mean, `14.6x` the non-empty median, `24.6x` the all-51 mean) so a later reader is not tempted to re-derive one and reach a different number. #1825 inherits whichever convention this slice sets, with `aliases` absent on 42 of 51 and the same problem worse.

**WHY NOT 128**, and this is the decision rather than an accident of it: 121 is **94.5% of 128**, so the cap would sit inside the observation's own noise. Every other cap in this family carries real headroom or deliberately accepts cutting; a cap whose entire margin over the longest observed value is 7 bytes would make "this cap does not fire on claude's ordinary output" a claim a `claude` point release can falsify. The field's length is an argument synopsis — a function of how many flags a workspace command takes — so it has a growth direction `name` does not, and `auto-mode-setup`'s 121 bytes are the committed evidence that workspace authors write long ones. Note also what 128 does NOT buy: no captured hint is cut at 128 either, so the two candidates are indistinguishable on the observation and separable only on headroom.

**WHY NOT 64 or below**, which is where this field's argument *diverges* from the description's rather than copying it: at 64 two captured hints (121 and 77) are cut, at 32 five, at 24 seven — so the cap fires on claude's ordinary output. `maxSlashCommandDescription`'s doc accepts exactly that outcome for itself and says so honestly, but only because it has no alternative: its descriptions total 10,580 bytes and one is 1,145 by itself. The hint has an alternative, so accepting the same outcome here would be a choice rather than a necessity.

**WHY NOT LARGER**: `maxSlashCommandDescription`'s MULTIPLICATION argument, verbatim — a multiplied field earns a smaller unit budget than the same field carried once, and this list multiplies by claude's observed 51. That is what pushed the description below `maxTaskRosterDescription`'s 512, and it applies here unchanged. What does not apply is the ROLE argument that kept the description from going lower: the hint's whole observed footprint is 530 bytes across the capture, **one twentieth of the descriptions'**, so nothing in the observation asks for more room than 2.1x the longest.

**MID-RUNE**: at 256 no captured hint is cut at all, so none is mid-rune reachable — the same property `maxSlashCommandDescription`'s doc records for itself at its own cap. `auto-mode-setup`'s is the only hint carrying non-ASCII, so it is the only captured value for which the question could arise.

**A SEPARATE CONSTANT** even though it equals `maxSlashCommandName` and `maxSlashCommandDescription` (and `maxModelResolved` / `maxModelField` / `maxModelValue`): `maxRateLimitField`'s paragraph applies verbatim — they bound different fields for different reasons, and folding them would make a future change to one budget silently move this one. The ticket names this obligation explicitly: this field gets its own constant even where the number matches.

### 2. `commandEntryLine` gains the decode target

```go
type commandEntryLine struct {
	Name         string `json:"name"`
	ArgumentHint string `json:"argumentHint"`
	Description  string `json:"description"`
}
```

Field order mirrors `turnevent.SlashCommand` and `protocol.SlashCommand`. The JSON tag is claude's **camelCase** `argumentHint`; the daemon's snake_case `argument_hint` appears only as the `TruncatedFields` report name and the wire tag. This is the first place in this family the two differ, and getting the tag wrong is silent: an entry would decode as `""` on every row and every liveness assertion below would go red with a message about the value rather than the key.

`ArgumentHint` inherits the struct's existing all-or-nothing decode rule with no branch of its own: a non-string `argumentHint` fails the WHOLE-LINE decode and takes `emitModelList`'s undecodable rung, exactly as a non-string `name` does. The rule is the struct's, not any one field's. The JSON null carve-out extends to it unchanged — `encoding/json` unmarshalling a null into a non-pointer string is a documented no-op producing `""`.

**Absent-versus-empty needs no decision here, and the doc must say so rather than leave it implicit.** Unlike `aliases` (#1825), where 42 of 51 entries omit the key, `argumentHint` is never absent in the capture — so `encoding/json` collapsing absent, null and present-empty onto `""` costs nothing observed at this layer. The distinguishability `protocol.SlashCommand`'s doc protects is the **wire's**, and it is protected there by declaring no `omitempty`. #1819 is the precedent for stating on the type *why* a collapse is safe.

### 3. `emitSlashCommandList` — the call goes in the middle

```go
name := bound(entry.Name, "name", maxSlashCommandName)
hint := bound(entry.ArgumentHint, "argument_hint", maxSlashCommandArgumentHint)
description := bound(entry.Description, "description", maxSlashCommandDescription)
```

`TruncatedFields` is ordered by this call sequence and by nothing else — the closure exists so there is ONE place the append happens. The new call is **inserted between** the two existing ones, so an entry with all three cut reports `["name", "argument_hint", "description"]`, matching the type's declaration order. Appending it after `description` would compile, pass any test that only checks membership, and silently break the declaration-order property `turnevent.SlashCommand`'s doc promises and `protocol.SlashCommand`'s mapping depends on. **This is the one place in the slice where a plausible edit produces a wrong result that only an order-sensitive assertion catches** — see § Testing.

The composite literal gains `ArgumentHint: hint` between `Name` and `Description`. Its comment states: claude's hint VERBATIM under #1600's rule (no lowercasing, no trimming, no charset filtering), its own cap (`maxSlashCommandArgumentHint`, not `Name`'s or `Description`'s) even though the three numbers agree, and that **an empty hint is carried as an empty string rather than dropped or defaulted**.

The `bound` comment's forward reference — *"The distinction starts mattering at argument_hint (#1830), whose claude key is argumentHint"* — becomes the live case and must be rewritten in the present tense, re-pointed at #1957.

### 4. `turnevent.SlashCommand` gains `ArgumentHint`

Between `Name` and `Description`. Its doc states: claude's hint verbatim per `ModelAnnounced.Model`'s rule; **an empty hint is the ordinary case, not missing data** (33 of 51), which is why a consumer must not treat empty as absent; bounded by the producer AT CONSTRUCTION under its own cap and never sanitized, pointing at `SlashCommandList`'s SECURITY paragraph rather than restating it.

`TruncatedFields`' doc gains `"argument_hint"` in its enumeration, in call order. The type doc's *"Its three fields are THREE of `protocol.SlashCommand`'s five"* becomes four of five, and *"The two still missing — ArgumentHint between the first two, Aliases between the last two"* narrows to one. The *FIXING THE ORDER BEFORE THE SECOND FIELD EXISTS* paragraph gains this field as its second piece of evidence — and this is the field the promise was actually written for, since `Description` landed in a position no reordering could have got wrong.

### Data flow (unchanged in shape)

```
claude stdout line
  → controlResponseLine decode (whole-line, all-or-nothing)
      → []commandEntryLine  { Name, ArgumentHint, Description }   ← transient, bounded by defaultMaxParseBuf's 4 MiB
  → emitModelList  (classifies the rung, writes the ONE log record)
      → emitSlashCommandList(entries)
          → per entry: bound(name) → bound(argument_hint) → bound(description)   ← the ONLY cap site
          → turnevent.SlashCommand{Name, ArgumentHint, Description, TruncatedFields}
      → p.emit(turnevent.SlashCommandList{...})
          → interactiveTurnEmitterV2.Handle  → DEFAULT (no case for this variant)
              → eventKind → variant NAME only → logged by kind, discarded
```

`turnbridge.MapEvent` is **not** on this path. Verified against the call graph rather than transcribed: `MapEvent` has exactly two production call sites, `resolveBoundModelList` (which hands it a `ModelList` explicitly) and `emitMapped` (reached only from `Handle`'s typed arms, none of which is `SlashCommandList`). Because `Handle`'s default returns, `emitMapped` never runs for this variant. The package overview records a code-review finding where a spec-inherited enumeration credited `MapEvent`'s default with reach it does not have; do not reintroduce it.

---

## The correction manifest

Every figure the two-term sum fed, pre-derived. **Re-derive each against the tree before editing** — sibling merges move them, and `origin/main` was at `8bcd14b9` when these were taken. All sites are in `internal/streamsup/parser.go` and `internal/turnevent/event.go`; nothing outside `cmd/` and `internal/` is in scope.

### The per-entry term

| | Two-term (shipped) | Three-term (this slice) |
|---|---|---|
| Per-entry sum | `256 + 256 = 512` | `maxSlashCommandName + maxSlashCommandArgumentHint + maxSlashCommandDescription = 256 + 256 + 256 = 768` |
| `16384 × 1/2 = 8192` leaves | 16 entries | **10** entries (`8192 / 768 = 10.67`) |
| `16384 × 5/8 = 10240` leaves | 20 entries | **13** entries (`10240 / 768 = 13.33`) |
| Observed capture retained through `truncateField` | 6,117 bytes | **6,647** bytes — still under 8192 AND under 10240 |
| Uncut across the 51 | 11,074 | **11,604** |
| Share of the 14,277-byte compact array | 77.6% | **81.3%** (omission now keeps 18.7% out, was 22.4%) |
| At a 512-byte description cap | retains 8,233 | retains **8,763** — over 8192, under 10240, so it still clears only 5/8 |

Term order in the sum follows declaration order: name, argument hint, description.

### Site-by-site

1. **`maxSlashCommandName` § THE PER-ENTRY TERM.** `256 + 256 = 512` → the three-term sum. *"The two field slices still outstanding — argumentHint (#1830) and aliases (#1825) — add their own caps to the 512 above"* → one slice outstanding, `aliases` (#1825), adding its cap to 768. The `#1830` cite here is one of the three re-pointed to #1957.
2. **`maxSlashCommandDescription` § THE MULTIPLICAND IS THE SUM OF CAPS.** Now 768. **This paragraph needs a re-argument, not a number swap.** Its roundness point — *"512 bytes exactly, a unit a reader can hold … No other candidate weighed here lands on one: 64 gives 320, 128 gives 384, 512 gives 768"* — collides with the new sum twice: 768 is now the real per-entry term, and at three terms the rejected 512 candidate would land on 1024, inverting the tiebreaker. Re-derive the candidate list at three terms (64 → 576, 128 → 640, 256 → 768, 512 → 1024) and state plainly that **roundness was a two-term tiebreaker that the third term dissolves**. It does **not** re-open `maxSlashCommandDescription`'s value: that cap was decided on the multiplication and role arguments, which are untouched, with roundness beside them rather than under them. Do not re-argue the description's number.
3. **`maxSlashCommandDescription` § THIS SHAPE'S FRACTION IS NOT FIXED HERE.** 16 and 20 → **10 and 13**. These are the two figures #1826 picks from; after this slice it multiplies one derived figure instead of re-deriving it.
4. **`maxSlashCommandDescription` § WHAT THE OBSERVED CAPTURE COSTS AT THIS CAP.** 6,117 → **6,647**, computed THROUGH `truncateField` and not as a naive byte-cut sum (it happens to equal the naive sum here because no hint is cut at 256, and saying so is cheaper than leaving a reader to wonder). The *"No larger candidate does: 512 retains 8,233"* sentence → **8,763**; the conclusion is unchanged.
5. **`maxSlashCommandDescription` § A CAP FIRING ON CLAUDE'S ORDINARY OUTPUT IS UNAVOIDABLE FOR THIS FIELD.** Its *"uncut name+description is 11,074, over both established fractions but UNDER 16384"* → **11,604**, still under 16384, so the premise the argument rests on survives. Check it rather than assuming: the sentence is built to be knock-down-able and a stale number is exactly the knock-down.
6. **`commandEntryLine` § TWO FIELDS OF FOUR.** → THREE OF FOUR. Only `aliases` (#1825) remains omitted. The `#1830` cite here is the second of the three.
7. **`commandEntryLine` § THE MEMORY ARITHMETIC INVERTED.** 11,074 of 14,277 = 77.6% → **11,604 of 14,277 = 81.3%**; the omission now keeps 18.7% out. A re-argument, not a swap: the paragraph's point is that the omission stopped being the whole memory story when the second field landed, and the third field makes the remaining omission a smaller minority still. The 6,117 → 6,647 figure appears here too.
8. **`commandEntryLine` § EVERY STRING HERE IS WORKSPACE-AUTHORED / the sink enumeration.** New paragraph: the hint's **own re-walk**, not an inheritance. See § Security review for the walk itself and the wording obligations.
9. **`commandEntryLine` § THE RE-OPEN TRIGGER.** *"covering BOTH declared strings"* → all three.
10. **`commandEntryLine` § THE DECODED SLICE IS UNCAPPED HERE.** *"this struct is TWO fields where modelOptionLine is five"* → three of five. The conclusion (the worst-case transient is a fraction of the already-accepted one, and a single 4 MiB line cannot maximise both) is restated, not re-argued — the fraction narrows again without changing direction, which is how #1904 handled the same sentence.
11. **`commandEntryLine` § TWO PER-FIELD CAPS DO EXIST.** → THREE, naming `maxSlashCommandArgumentHint` beside its two neighbours, still three constants rather than one shared limit for `maxRateLimitField`'s reason.
12. **`commandEntryLine` § the whole-line decode rule.** The enumeration *"a `name` or a `description` that is not a string"* gains `argumentHint`.
13. **`commandEntryLine` § the JSON null carve-out.** *"it applies at BOTH positions"* → all three; *"a null `name` or a null `description` lands as `""`"* gains the hint. This is also where the absent-versus-empty statement from § Design 2 lands.
14. **`emitSlashCommandList` § the `bound` closure comment.** The camelCase/snake_case sentence goes from prediction to present tense, re-pointed to #1957 — the third of the three `#1830` cites.
15. **`emitSlashCommandList` § the sequential-statements comment.** *"`[]string{"name", "description"}` on an entry with both cut"* → the three-name form, and the daemon-names-not-claude's-keys sentence stops being a claim about two fields that happen to coincide and becomes one about a field where they genuinely differ.
16. **`logControlResponse` § the `commands` paragraph.** A re-argument. *"No name, no argumentHint, no description and no alias reaches this record"* stays true, but the distinction behind it is redrawn again: with `argumentHint` declared, **only `alias` is UNREACHABLE by omission**, and the hint crosses into UNWRITTEN — held in a decoded struct and a constructed event, kept out by what this function chooses to log rather than by what the decode target can hold. #1904's own recorded lesson names this paragraph as one of its two expensive re-arguments; budget it as a paragraph.
17. **`turnevent.SlashCommand`'s type doc.** THREE of five → four of five; *"The two still missing"* → one; the FIXING THE ORDER paragraph gains this field as its second evidence.
18. **`turnevent.SlashCommand.TruncatedFields`' doc.** *"TODAY IT CAN CARRY "name" AND "description", IN THAT ORDER"* → the three-name enumeration in call order.
19. **`cmd/pyry`'s `emitterSlashCommandListSentinels` doc.** *"Description is the first one to pay it"* — the sentence stays true and gains this field as the second payment; the doc's claim that each field-adding slice owes a sentinel and a line is the obligation this slice discharges.

**The fourth `#1830` cite, on `commandEntryWithFixture` in `parser_test.go`, owes no edit.** It reads *"so #1830's argumentHint and #1825's aliases reuse this instead of adding a third helper"* — a prediction this slice satisfies by *using* the helper. Re-point it only if you also re-word it; do not mint a third helper.

**Two sweep rules from the package overview, because this manifest is a grep result and grep has a known blind spot here.** A falsified `//` claim phrased about a *category* (*"the two field slices still outstanding"*, *"four rungs"*, *"both declared strings"*) is the one a symbol grep misses — so read each named block whole rather than editing the matched line. And a cite that survives sits next to one that does not: the unit is the sentence, not the paragraph.

---

## Concurrency model

Unchanged, and no new concurrency is introduced. The decode and the whole construction run on the parser's own `Write` goroutine, synchronously, between `json.Unmarshal` and `emitModelList`'s return. `emitSlashCommandList` spawns nothing, takes no lock, and holds no state across calls: `cut` and the `bound` closure are declared **inside** the per-entry loop, which is what keeps one entry's report off the entries after it, and `ArgumentHint` inherits that scoping by being bound through the same closure rather than beside it.

The constructed `turnevent.SlashCommandList` is carried-never-mutated once emitted. Nothing retains it — there is no `sessionModelHold` analogue for this array — so the retention is the event's own lifetime.

---

## Error handling

**No new failure mode and no new rung.** The design deliberately adds zero reject branches:

- A non-string `argumentHint` (number, object, array, bool) fails the **whole-line** decode and takes `emitModelList`'s existing undecodable rung, exactly as a non-string `name` does. The rule belongs to `commandEntryLine`, not to any one field, which is why the third declared key inherits it rather than earning a branch.
- A JSON `null` `argumentHint` is the existing carve-out: `encoding/json` unmarshalling null into a non-pointer string is a documented no-op, so it lands as `""` — a counted entry, not a failed line. Absent, null and `""` are one reading and no consumer can branch on the difference.
- An over-cap hint is not an error: `bound` cuts it and reports `argument_hint` on that entry's `TruncatedFields`. `truncateField`'s `<=` boundary means a hint of exactly 256 bytes is not truncated, and its empty-replacement scrub deletes an invalid trailing sequence so a mid-rune cut lands 1–3 bytes short.
- **No log line gains anything.** `logControlResponse` stays at six attributes on every rung; there is no seventh, and no decoded content reaches it. The hint's bytes reach no log at any level.

---

## Testing strategy

Unit tier only. **No live claude, no credentials** — everything is provable against hand-built fixtures and the already-committed capture file, which `make check` can read because the `e2e_realclaude` build tag governs that package's Go files and not its testdata. The ticket does not carry `needs-real-claude` and the tests must not require it.

**Scope boundary with #1958.** What this slice owes is **liveness at the fixture level**: the hint round-trips, a cut is reported. The committed-capture pin is #1958's — all 51 hints verbatim, the 33 empty ones as entries carrying an empty hint, `auto-mode-setup`'s 121 bytes whole, and no entry reporting `argument_hint`. Writing it here would duplicate #1958's AC 3 and undercut the sole-red claims that slice is built to make. This is #1904/#1905's seam exactly.

### Scenarios (rows on the existing tables; `commandEntryWithFixture` builds every one)

- A hint carrying text arrives with that text, byte-for-byte — no lowercasing, no trimming, no charset filtering.
- A hint of `""` arrives as an entry whose hint is empty: present in the emitted entry, not dropped, not defaulted, and not made indistinguishable from a missing key.
- An **absent** `argumentHint` key arrives as `""` — one reading with the above, matching the null carve-out.
- A `null` `argumentHint` arrives as `""` and the entry is counted, not failed.
- A hint of exactly `maxSlashCommandArgumentHint` bytes is **not** truncated and reports nothing (the `<=` boundary).
- A hint one byte over the cap is cut to the cap and that entry reports `argument_hint` — the daemon's snake_case name, never claude's `argumentHint`.
- An entry cutting **both** the hint and the description reports `["argument_hint", "description"]` **in that order**; an entry cutting all three reports `["name", "argument_hint", "description"]`. **Compare the slice by exact equality, not by membership** — a membership check passes against a call inserted in the wrong position, which is the one plausible wrong edit in this slice.
- A cut on one entry does not appear on the entries after it (the per-entry `cut` scope).
- A non-string `argumentHint` (a number) fails the whole line and takes the undecodable rung — same rung, same reason keyword, same six-attribute record as a non-string `name`.
- The model-list and command-list behaviour is unchanged on **every** rung: the existing suppression table and rung classification rows stay green untouched.
- No log line carries the hint. On the `cmd/pyry` lane this is deterministic rather than advisory, via the sentinel sweep below.

### Two traps worth knowing before you write

**The empty-string sentinel trap.** `emitterSlashCommandListSentinels` feeds `strings.Contains(logs, value)`, and `strings.Contains(s, "")` is **always true** — so an entry whose `ArgumentHint` is empty turns the log-leak negative into an assertion that fails against a correct implementation. Give **every** entry in `emitterSlashCommandListFixture` a non-empty, conspicuous hint sentinel (the fixture's existing `qq-`/`zz-` scheme, distinguishable per entry per the fixture's own stated rule), then append `c.ArgumentHint` in the sentinel builder. Do not filter empties in the builder — that would hide a real future leak of an empty-valued field's neighbours.

**The whole-slice-absence trap, already sprung once.** `TestParser_InitializeControlResponseCountsTheCapturedCommands` asserted `TruncatedFields == nil` across the whole capture under a message about `Name`; declaring `Description` turned it red against a correct producer, and #1904 narrowed it to `slices.Contains(got.TruncatedFields, "name")`. **Check whether the third field re-breaks it — it does not**: no captured hint exceeds 128, so `argument_hint` never appears in the capture's reports at a 256-byte cap, and the narrowed form is already field-specific. Verify rather than assume, and if a re-capture has moved the array, that is a finding for #1958 and not a reason to widen this assertion.

### On sole-redness claims

If a new row's `why` claims to be the only row a given mutant reddens, **measure it** — the package overview records two rounds of that claim being plausible and wrong in exactly this matrix, once when the description joined the shared helper and again between the two mid-rune rows. Grade a new row against the rows already in the table *and* against the siblings landing beside it. Run mutants via `go test -overlay=<abs-path json>` so no worktree write is needed. If a row cannot honestly claim sole redness, say what it *does* separate rather than deleting the claim.

---

## Open questions

1. **`aliases` (#1825) inherits this slice's population-naming convention**, with the problem worse: the key is absent on 42 of 51 entries, so its all-51 median is 0 *and* its non-empty population is 9. Nothing here fixes its cap or its absent-vs-empty reading; both are #1825's. This spec fixes only the convention that a stated multiple must name its base.
2. **The per-entry sum stops being a round unit at three terms (768).** That is stated as an observation, not repaired. Whether the fourth term restores one is #1825's arithmetic and must not be pre-decided here by choosing this cap to make it come out even.
3. **#1826 picks the fraction and the entry count** from the 10-and-13 pair this slice hands forward, exactly as it would have picked from 16-and-20. Neither figure is a decision this slice takes.
4. **The re-open trigger for all three strings is unchanged**: the first slice that gives one a syntax sink, or renders it into an HTML sink, an attribute or a URL. #1720 is the nearest such slice and what it owes is the client-side render boundary.

---

## Size check

Re-applied to the written spec, per the six-number boundary.

| Limit | Boundary | This spec |
|---|---|---|
| Production source files created or modified | ≤ 3 | **2** — `internal/streamsup/parser.go`, `internal/turnevent/event.go` |
| Total written work | ≤ 400 | **~380 insertions** projected (see below) |
| New exported types or interfaces | ≤ 5 | **0** — one exported field, no new type |
| Consumer call sites needing simultaneous update | ≤ 10 | **2** — `emitterSlashCommandListFixture` and `emitterSlashCommandListSentinels` |
| Acceptance criteria | ≤ 5 | **4** |
| Distinct error/reject branches in a state machine | ≤ 10 | **0 new** — the non-string case inherits the existing whole-line undecodable rung |

**Why the call-site count is 2 and not larger.** Every `turnevent.SlashCommand{…}` literal in the tree is **keyed**, so a new field does not break one, and the zero value `""` is the correct expectation for a fixture carrying no hint. `commandEntryFixture` builds the `name` key **only** — it does not derive from the capture — so no existing table row gains a hint by accident. The two capture-reading tests compare field-by-field rather than whole-struct, so neither goes red on a correct producer. Counted with `grep -rn "turnevent.SlashCommand{" internal/ cmd/`: 15 sites, 13 of them requiring no edit.

**The line projection, and the tension in it stated plainly.** The shape-matched analogue is #1904 (`cc8c3616`) — the same slice one field over — which measured **418 insertions across 4 code files** (`parser.go` 223, `parser_test.go` 144, `event.go` 37, `cmd/pyry/interactive_turn_v2_test.go` 14), and whose recorded lesson says it landed at ~500 counting deletions against a spec that estimated 265–350. That lesson names #1830 by number as the ticket that should cost it in, so it is costed in here rather than discovered.

This slice is **strictly less** than #1904 on three axes and strictly more on one:

- **Less:** it does not create the `bound` closure (#1904 did, with the comment arguing the device); it does not create `commandEntryWithFixture` (#1904 did, ~26 lines with its doc); and its cap derivation is materially shorter than the description's — no "why thinner" multiplication/role inversion to construct, no "a cap firing on ordinary output is unavoidable" paragraph, and no mid-rune-on-the-live-path paragraph, since no captured hint is cut at 256.
- **More:** the correction manifest runs to 19 sites against #1904's 10, of which 3 are re-arguments against its 2.

Netting those against the measured 418 gives ~380. The honest tension: 418 is a *ceiling* here rather than a floor, but the two numbers are close enough that the developer's turns will go to the manifest and not to construction — which is why the manifest is pre-derived above rather than left as "sweep for stale figures". That is #1434's remedy applied deliberately: pay the discovery at architect time.

**No split, and none is available.** #1957 is a depth-2 grandchild (#1824 → #1830 → #1957), so the split-depth gate forbids proposing one; if the boundary were exceeded the only route would be `needs-human:sizing`, and it is not exceeded. There is also no seam left to cut on: decode ‖ cap is forbidden (a construction-time bound never splits from the payload it bounds — one merge window of unbounded workspace-authored text); the `turnevent` field ‖ the parser leaves a field nothing sets, which the one-consumer floor rule merges back; behaviour ‖ prose-corrections ships knowingly-false comments for a merge window; and behaviour ‖ the full proof matrix is **already cut**, to #1958.

---

## Security review

**Verdict:** PASS

**Findings:**

- **[Trust boundaries] MUST-FIX addressed in § Design and § Correction manifest site 8 — the enumeration is a re-walk, not an inheritance.** The boundary is explicit and single: `json.Unmarshal` into `commandEntryLine` on the parser's `Write` goroutine, with the cap applied one step downstream at the sole construction site in `emitSlashCommandList`. `ArgumentHint` is workspace-authored — a command defined in a repository was written by whoever wrote that repository, and the daemon reads it in whatever directory the operator points a session at — which is a **lower**-trust origin than claude's own strings. The spec requires `commandEntryLine`'s doc to walk this third string through the sink enumeration **on its own** rather than citing `Name`'s or `Description`'s answer, because that enumeration is a claim about a value established by inspection and not a property of the path. Walked here and required to be walked in the doc: the hint reaches one field of a daemon-internal struct, the parser's emit callback, `interactiveTurnEmitterV2.Handle`'s **default**, and `eventKind`, which returns the variant NAME only. It reaches no `exec.Command` argument, no `filepath.Join`, no `filepath.Match`, no `regexp`, no log attribute, no `eventring` append and no wire frame. **`turnbridge.MapEvent` is not reached** — verified against the call graph (its two production call sites are `resolveBoundModelList`, handed a `ModelList` explicitly, and `emitMapped`, reached only from `Handle`'s typed arms), not transcribed from a prior spec; the package overview records a code-review finding where exactly that transcription was wrong. Validation stays REFUSED and the reason is a **checked** NO SINK, never "these look like identifiers" — and this field is where that argument is least available of the three. Measured over the capture: **13 of the 18 non-empty hints contain `[` and `]`**, 10 contain `<` and `>`, 7 contain `|`, and `auto-mode-setup`'s contains `(` and `)` — `[` alone is a character class to `regexp` and a bracket expression to `filepath.Match`. (No captured hint contains `*`, `?` or a backslash; the argument does not need them and must not claim them.) Where `Name`'s enumeration had to reach for a single counter-example, `__remote-workflow`, this field's syntax characters are the **majority** case, which is why the walk records the frequency rather than one exemplar.
- **[Trust boundaries] No further finding — downstream holders are signalled.** `turnevent.SlashCommandList`'s SECURITY paragraph already states, for every string on the type, that the daemon bounds but does not sanitize and that the render boundary owing sanitization is the client's. The new field's doc points at that paragraph rather than diluting it into a restatement, so there is one copy to correct when #1720 lands a publish path.
- **[Resource exhaustion / input size limits] No findings — all three dimensions are bounded, and the one that is not is bounded elsewhere by design.** Transient: the whole line is capped at `defaultMaxParseBuf`'s 4 MiB before the decoder sees it, and adding a third declared string narrows the per-densest-legal-element fraction without changing its direction. Retained: `maxSlashCommandArgumentHint` at construction, the sole cap site. The **entry count** is deliberately unbounded here and is #1826's — named as out of scope with its owner, exactly as `maxSlashCommandName`'s doc names it. The worst case this slice hands #1826 is 768 bytes per entry, re-derived above; the observed capture retains 6,647 across 51 entries, inside both established fractions of `maxUnrecognizedRaw`'s 16 KiB.
- **[Integrity of the cut report] No findings — the type's own named lie-mode is satisfied on both sides.** `turnevent.SlashCommand.TruncatedFields`' doc names exactly one way this partial field set could produce a lie: *a name for a field this type does not declare must never appear*, because it would tell a consumer that text it holds is incomplete when the type never held that text. This slice reports `argument_hint` **and** declares `ArgumentHint`, in the same change, so the report and the carrier land together and the rule is never transiently violated. The report name also already exists in `protocol.SlashCommand.TruncatedFields`' enumerated wire set, so the eventual mapping is a copy and not a translation that could disagree. Per-entry the report slice takes at most three appends, bounded by the field count, so no payload can grow it.
- **[Error messages, logs, telemetry] No findings — and the negative is deterministic, not advisory.** `logControlResponse` stays at six attributes on every rung, all four integers daemon-computed from slice lengths and carrying none of claude's bytes. No seventh attribute; the hint's bytes reach no log at any level. `#833`'s posture is enforced on the `cmd/pyry` lane by the sentinel sweep, which this slice extends — and the spec pins the empty-string trap that would otherwise make that sweep fail against a correct implementation. **SHOULD FIX, and the reason it is a security finding rather than a test-hygiene one:** the natural repair for a sweep that fails on an empty sentinel is to skip empties in the builder, and that silently disarms leak detection for every future field whose fixture value happens to be empty — turning a deterministic `#833` enforcement back into an advisory one. The spec's remedy is the other direction: give every fixture entry a non-empty conspicuous hint and append unconditionally. Code review should check which of the two was done, because both make the suite green.
- **[Subprocess / external command execution] No findings — by enumeration, not by assumption.** No value on this path reaches an argv element. `turnevent.SlashCommandList`'s doc already states the family rule with the amendment that matters here: a client is meant to send a **Name** back as ordinary message text, because sending the slash command IS the feature; publishing a name does not make it trusted, and the inbound path does not consult this list as a command vocabulary. The hint is never sent back at all — it is a synopsis, not an invocable token — so it does not even inherit the amendment.
- **[Cryptographic primitives] Not applicable — no randomness, no keys, no comparison against a secret. Stated rather than skipped: no value on this path is compared to anything, so `crypto/subtle` has no site here.
- **[File operations] Not applicable — this slice opens, creates and writes no file. The capture file the derivation measures is read only by tests, through the existing `capturedInitializePayload` helper, from a fixed path under `testdata/` with no caller-supplied component.
- **[Concurrency] No findings.** No new goroutine, no lock, no shared mutable state. The per-entry `cut` slice and the `bound` closure are declared inside the loop, so one entry's report cannot reach the entries after it; the third field is bound through that same closure rather than beside it, inheriting the scoping rather than needing its own. The emitted list is carried-never-mutated and retained only for the event's lifetime.
- **[Network & I/O] Not applicable to this slice — no socket, no HTTP server, no TLS. The wire is out of scope by the ticket's own boundary: no frame, no protocol change, no client.
- **[Threat model alignment] The relevant threat is `docs/protocol-mobile.md` § Security model's workspace-authored-content class, and it is addressed at the two points that are this daemon's to address — a construction-time bound and an honest cut report. Rendering is out of scope and named: the client owns the render boundary, and #1720 is the slice that inherits the question the moment a publish path exists.
- **[Re-open trigger] Named rather than left to be noticed, covering all three declared strings: the first slice that gives one a SYNTAX sink — a path element, a match pattern, a regexp, an argv element, a log attribute — or that renders it into an HTML sink, an attribute or a URL.

**Reviewer:** architect (self-review per the security-review checklist)
**Date:** 2026-09-01
