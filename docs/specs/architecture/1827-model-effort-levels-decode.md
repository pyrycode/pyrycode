# #1827 — decode each model's reasoning-effort levels, with each level string bounded

`feat(streamsup)` · size **s** · `security-sensitive`

## Files to read first

Production:

- `internal/streamsup/parser.go` → `emitModelList` — the emitter, and the per-entry `bound` closure
  this slice extends. Read the closure and the three sequential `bound` calls below it: the
  declaration-order contract is those calls, not the struct.
- `internal/streamsup/parser.go` → `modelOptionLine` — the decode target. Read its doc in full; three
  of its sentences are rewritten here, including the one naming the closed #1820.
- `internal/streamsup/parser.go` → `truncateField` — the cut helper. Two properties are load-bearing:
  the boundary is `<=`, and a cut landing mid-rune DELETES the partial rune, so a cut value can come
  back 1–3 bytes under the limit.
- `internal/streamsup/parser.go` → `maxModelResolved` — the three-string measurement, the
  separate-constant rule, and the "THE ENVELOPE ARITHMETIC IS COMPLETE" paragraph this slice makes
  false. Read it as the paragraph the new cap inherits AND as an AC 4 edit target.
- `internal/streamsup/parser.go` → `maxModelListEntries` — the written-out derivation (`768`
  multiplicand, `8192 / 768 = 10.67, so 10`). Second AC 4 edit target.
- `internal/streamsup/parser.go` → `logControlResponse` — the four-attribute record. **Nothing is
  added to it**; that is how AC 2's "no log line carries decoded content" is met.
- `internal/turnevent/event.go` → `ModelOption` — the daemon's type. Its first paragraph and its
  omissions paragraph both name the closed #1820 and are rewritten here.
- `internal/turnevent/event.go` → `ModelOption.SupportsAutoMode` — read its whole doc. Its final
  paragraph is AC 3's re-derive instruction, re-pointed at #1828; its shape is the model the new
  field's doc deliberately does NOT copy.
- `internal/turnevent/event.go` → `ModelOption.TruncatedFields` — the snake_case names, declaration
  order, nil-not-empty. Its name list is exhaustive today and gains a fourth entry.
- `internal/turnevent/event.go` → `ModelList` — the `Models` field doc's "Both dimensions bounded"
  sentence. It makes the same aggregate claim AC 4 is about, one package up. See § Doc edits.
- `internal/protocol/interactive.go` → `ModelOption` and `ModelOption.MarshalJSON` — **not edited
  here.** Read them for two things: the SECURITY paragraph already asserts that "every string in
  `EffortLevels`" is bounded by *the producer, decided at construction* (this slice is that producer,
  and today no producer exists), and `MarshalJSON`'s doc is the tree's idiom for stating a position at
  one layer while expressly leaving the daemon-internal reading to a named follow-up.
- `internal/relay/v2session_settings.go` → `validEffort` — read it only to confirm what NOT to do. It
  is a CLOSED enum bounding a phone-supplied override on an INBOUND path. It is not applied to
  claude's outbound list, is not widened, and is not narrowed.

Tests:

- `internal/streamsup/parser_test.go` → `TestParser_InitializeControlResponseDecodesTheCapturedModels`
  — AC 1's pin. Extended, not duplicated. Read the two coverage guards already in it (the key-count
  assertion and #1819's `sawAutoModeTrue` / `sawAutoModeAbsent` pair) — the new guard is a third of
  that shape.
- `internal/streamsup/parser_test.go` → `capturedModelString`, `capturedModelBool` — the accessor
  family the new one joins, including `capturedModelBool`'s `(value, present)` shape.
- `internal/streamsup/parser_test.go` → `capturedModelEntries` — read its doc. It decodes with LITERAL
  key strings rather than through the production target and says why; that idiom is mandatory for the
  new accessor and for the fixture guard.
- `internal/streamsup/parser_test.go` → `fixtureAutoMode` — #1819's fixture-wire guard. This slice
  generalises it (§ Testing strategy) rather than writing a second copy.
- `internal/streamsup/parser_test.go` → `TestParser_ModelListFieldsAreCapped` — the table AC 2's rows
  join, and the `modelResolvedCapFixture` family of test-local cap constants beside it.
- `internal/streamsup/parser_test.go` → `TestParser_InitializeControlResponseRejectBranches` — where
  the undecodable rows go. Its `wantAttrs` `reflect.DeepEqual` is the exact-attribute comparison that
  makes any new log attribute red.
- `internal/streamsup/parser_test.go` → `modelEntryFixture`, `modelEntryWithFixture`,
  `modelListLineFixture` — the fixture builders. `modelEntryFixture`'s three-parameter signature is
  **not** widened; `modelEntryWithFixture` already takes `any` and is the entry point for a list value.

Read-only, for posture (do **not** edit — the documentation phase owns them):

- `docs/knowledge/features/streamsup-package.md` § "Decoding the initialize ack into
  `turnevent.ModelList` (#1811)" and the `SupportsAutoMode` section that follows it. Two things there
  change how this ticket is built: the *test-writing lesson* that a collapse-shaped design's table is
  vacuous unless a guard asserts what the built line ACTUALLY carries (it applies here verbatim, for a
  different pair of shapes — see § Testing strategy), and the per-entry accumulator-ordering lesson
  behind `TestParser_ModelListFieldsAreCapped`'s last row. The section also still names #1820 in two
  places; **leave it alone**, the documentation phase folds this slice in after code review.

## Context

`emitModelList` decodes claude's `initialize` reply into `turnevent.ModelList`. #1811 shipped the three
verbatim strings, #1812 bounded the entry count, #1819 added the first capability key. This slice adds
`supportedEffortLevels` — the reasoning-effort levels claude published per model — so a client's model
menu can offer an effort control with exactly the levels claude accepts.

It is the first field in this family that is **a list inside a list entry**, and that is the whole of
the design work. Three consequences, each handled below:

1. **The cut report has to aggregate.** `bound` appends a name once per call and each of the three
   strings calls it once. One name must now cover many values.
2. **The byte budget grows a third dimension** — entries × levels × level length — and this slice
   bounds only the third factor. The two shipped paragraphs that assert the per-entry budget is
   complete become false in this commit and are amended in it.
3. **Absent and published-empty are not obviously the same thing**, and unlike #1819's bool, the Go
   shape does not force an answer. This slice declines to answer and says so at the type.

**Evidence, re-derived 2026-08-27 against `main` at `a30422a7`.** The committed capture
`initialize_control_v2.1.239.json` carries six entries in three key sets: four carry
`supportedEffortLevels: ["low","medium","high","xhigh","max"]` (three eight-key entries plus the
nine-key `opus`), and two — `haiku`, `claude-haiku-4-5` — carry no capability key at all. Where the key
is present its value never varies, byte-identically across all three of #1763's arms that carry a
models array. The longest level string observed is **6 bytes** (`medium`), so a cap fixture has to be
synthesized exactly as `TestParser_ModelListFieldsAreCapped` already synthesizes the other three.

**No ADR is warranted, and one thing is worth the documentation phase's attention.** The decision here
is field-specific and belongs at the type where #1828 will read it. But the envelope gap this slice
opens (§ Caps) is a *package-level* fact — the model-list section of
`docs/knowledge/features/streamsup-package.md` currently states the per-entry worst case as 768 bytes,
and that number is now a floor rather than a total. The documentation phase owns that edit.

## Design

### Where the reading is left open, and why that is not a dodge

`turnevent.ModelOption` gains `EffortLevels []string`, placed between `DisplayName` and
`SupportsAutoMode` — `protocol.ModelOption`'s slot, keeping the type's standing claim that it mirrors
the wire row's declaration order true. After this slice the two types carry the same five
claude-authored fields, which makes `ModelOption`'s first paragraph strictly simpler (§ Doc edits).

**`encoding/json` does not collapse this field the way it collapsed #1819's bool.** An absent key
decodes to a nil slice; a published `[]` decodes to an empty non-nil slice. So unlike `SupportsAutoMode`,
where the field's shape WAS the decision, here the shape decides nothing on its own — and the daemon
gets to add a normalisation step or not.

**It adds none.** The producer applies the cap and assigns; no code turns nil into `[]string{}` or the
reverse. This is the only choice that is not an answer:

- Allocating an empty slice for an absent key would be the daemon *inventing a shape claude did not
  send*, which is a reading — the one that says absent and empty mean the same thing.
- Nilling out a published empty array would be the opposite reading.
- Both are #1828's to make, and #1828's argument is owed precisely because #1819's does not carry:
  `SupportsAutoMode`'s collapse rests on a *withheld grant* being safe to read as a refusal, and an
  empty effort menu is a different failure than a greyed checkbox. `ModelOption.SupportsAutoMode`'s
  own doc says so and forbids inheritance.

The field's doc must therefore state, in this order: what the field is (claude's published levels,
VERBATIM per #1600 — no lowercasing, no canonicalisation into the daemon's own effort enum, no
reordering); that the ELEMENTS are bounded and the COUNT is not, naming #1821; that whether an absent
key reads the same as a published empty one is **NOT settled here** and is **#1828's**, with the
one-sentence reason (#1819's argument is about a withheld grant and does not transfer); that the daemon
consequently adds no normalisation in either direction, so a reader must not read the Go shape as a
position; and that a CUT level is not a level claude published, so `TruncatedFields` naming
`effort_levels` means the list must not be treated as a menu.

**One security sentence belongs in that paragraph**, mirroring `SupportsAutoMode`'s: these are
claude-authored strings that crossed the subprocess trust boundary, bounded here and NOT sanitized —
no control-character or terminal-escape stripping happens on this path — so they are inert text for a
client to render and the render boundary owing the sanitization is the client's, exactly as
`protocol.ModelOption`'s SECURITY paragraph already states for the same strings.

**`internal/relay`'s `validEffort` is deliberately not consulted.** It is a closed enum bounding a
phone-supplied override on an inbound path; applying it outbound would silently drop a level claude
adds in future, which is the opposite of carrying claude's answer verbatim. The doc should say this
once, because the two sets look interchangeable and are not.

### The cut report: one name per entry, however many levels were cut

`emitModelList`'s per-entry closure gains a sibling. `bound` is untouched and its three call sites are
untouched; a second closure over the same per-entry `cut` slice handles the list:

```go
boundEach := func(values []string, name string, limit int) []string
```

Behaviour, in three sentences:

- `len(values) == 0` → return `values` unchanged and append nothing. No allocation, and nil stays nil
  while empty stays empty — the no-normalisation rule above, expressed as the absence of code.
- Otherwise every element goes through `truncateField` into a new slice of the same length, so the
  list's cardinality and claude's order are preserved even when elements were cut.
- `name` is appended to `cut` **at most once**, after the loop, iff at least one element was cut.

The name `boundEach` is chosen over `boundList` on purpose: it says the cap applies to each ELEMENT,
which is exactly the gap #1821 closes, and a reader asking "what bounds the list itself?" is asking the
right question.

The call goes **after** the three `bound` calls, as a fourth sequential statement:

```go
effortLevels := boundEach(entry.EffortLevels, "effort_levels", maxModelEffortLevel)
```

That placement is the declaration-order contract: `TruncatedFields` is ordered by these calls, which is
why `emitBackgroundTaskStarted`'s comment says sequential statements rather than a composite literal.
The struct field order and the call order must agree — `resolved_model`, `value`, `display_name`,
`effort_levels`.

### Decode target

`modelOptionLine` gains one field:

```go
EffortLevels []string `json:"supportedEffortLevels"`
```

The Go name mirrors `turnevent.ModelOption`'s and `protocol.ModelOption`'s; the tag is claude's key.
A `supportedEffortLevels` that is a string, a number, an object, **or an array carrying a non-string
element** fails the WHOLE-LINE decode and takes the undecodable rung. That last shape is new to this
family: it is the first time an ELEMENT-level type mismatch reaches that rung, and `modelOptionLine`'s
doc should name it, because "the array decoded but one element was wrong" is the shape a reader will
otherwise assume is tolerated. JSON `null` remains the carve-out the doc already states, and for this
field it lands as nil — which is the same zero-length reading an absent key gets, without that being a
statement about whether the two MEAN the same thing.

### Caps: one new constant, and a gap this slice states rather than closes

```go
const maxModelEffortLevel = 32
```

It caps ONE level string, not the list. `maxModelResolved`'s paragraph is inherited the way
`maxModelValue` inherits it — the separate-constant rule and the construction-time application are one
argument stated once — so the new constant's own doc is short, and carries only what is this field's:

- **Measured**: the longest level in the committed capture is 6 bytes (`medium`), so 32 is ~5×.
- **Why a thinner multiple than the siblings' ~10×.** Two reasons, and the second is the real one.
  A level is the most constrained shape in the family — a token from a menu claude publishes and a
  client renders as a control's options, not prose like `displayName`. And this cap is paid PER
  ELEMENT against a count claude chooses, so generosity here multiplies where the siblings' does not.
  32 still admits every plausible future spelling (`ultrathink` is 10 bytes, `extended-thinking` 17),
  which is the property that matters: a level claude adds later must decode, not be mangled.
- **Not `validEffort`'s enum.** The closed set's longest member is 6 bytes, and sizing the cap to it
  would be applying the inbound rule outbound by the back door.
- A power of two, matching the family.

**The envelope arithmetic is now incomplete, and this slice says so in the two places that assert
otherwise.** Per entry the retained claude-derived text is `768 + 32 × N` where N is the number of
levels claude sends — a number nothing bounds until #1821. The arithmetic worth writing into the doc,
because it shows the gap cannot be closed by adding a count cap alone:

> `maxModelListEntries` × per-entry bytes must stay under the 8192-byte ceiling
> (`maxUnrecognizedRaw`'s half, the package's ordering landmark). `10 × (768 + 32N) ≤ 8192` requires
> `N ≤ 1.6`, so **any** level-count cap of 2 or more breaks the current product.

That is a fact about the numbers, not a decision: #1821 must move `maxModelListEntries`, or the 8192
ceiling, or both, in addition to adding the count bound. **This spec does not choose which**, and the
developer must not — naming the two levers is the whole obligation. Note also that no honest
per-level cap avoids this: keeping 10 entries under 8192 with the observed five levels would need a cap
of 10 bytes, which would cut a level claude has not shipped yet and would fire on ordinary output.

**If you find yourself changing `maxModelListEntries`' value, stop.** That is #1821's, and something
has gone wrong here.

### Doc edits

Every one of these is a sentence this slice makes false or stale. None is a drive-by.

**AC 3 — the four comments naming the closed #1820** (a `git grep -- '#1820'` over production Go finds
exactly these four; the two hits in `docs/knowledge/features/streamsup-package.md` are the
documentation phase's and must be left alone):

1. `modelOptionLine`'s doc, final sentence of its first paragraph — "reduced to the four keys … and the
   one remaining capability key, supportedEffortLevels, is #1820's." Becomes five keys, with
   `supportedEffortLevels` decoded HERE (#1827) and the undecoded set stated as `description`,
   `supportsEffort`, `supportsAdaptiveThinking` and `supportsFastMode`.
2. `ModelOption`'s type doc, first paragraph — "Its four claude-authored fields — three strings and one
   bool — are exactly what protocol.ModelOption carries (#1704) **minus EffortLevels, which is #1820's
   slice**, in that type's declaration order." The exception clause DELETES: five fields, three strings
   plus one string list plus one bool, and the set is now exactly `protocol.ModelOption`'s with nothing
   missing and nothing invented.
3. `ModelOption`'s type doc, omissions paragraph, final sentence — "Of the two capability keys the
   richer entries do carry, supportsAutoMode is decoded here (#1819) and supportedEffortLevels is
   #1820's slice, not an omission." Both are decoded now; neither is an omission. The same paragraph's
   earlier clause "which the four richer entries carry beside **the key** this type does decode" reads
   singular and must become plural — one word, same sentence, and leaving it is the drift AC 3 exists
   to stop.
4. `ModelOption.SupportsAutoMode`'s doc, final paragraph — the re-derive instruction. **Re-pointed, not
   deleted**: the argument is still owed, only by #1828. It must now read as a statement about a field
   that SHIPPED (the levels are decoded; what an absent key MEANS is open) rather than about a future
   slice, and it must name #1828.

**AC 4 — the aggregate-bound claims:**

5. `maxModelResolved`'s envelope paragraph — "THE ENVELOPE ARITHMETIC IS COMPLETE, in two terms stated
   in two places." It is not, and it is this commit that opens the gap. State the third term
   (`maxModelEffortLevel` × a count claude chooses), state that the count is unbounded, and name #1821.
   The 768 figure stays as the three-string multiplicand but must be labelled as such rather than as
   the per-entry total.
6. `maxModelListEntries`' derivation — the `Multiplicand: 768` bullet and the `8192 / 768 = 10.67, so
   10` line. 768 becomes an explicit FLOOR on per-entry bytes and the `10 × 768 = 7680` product bounds
   only the three strings. Carry the `N ≤ 1.6` arithmetic above and name #1821 and the two levers.
   Its "768 counts claude-derived text only" paragraph is a separate, still-true sentence about
   `TruncatedFields`' daemon-authored names — do not conflate the two.

**Also AC 4's claim, one package up** (not in AC 4's anchor list because the AC names `parser.go`, but
it is the same assertion and this slice makes it false):

7. `ModelList.Models`' doc — "**Both dimensions bounded**, the list truncated FROM THE TAIL … The bool
   beside those strings is bounded by nothing and needs no cap." There are three dimensions now and two
   are bounded. The clause must say that each level STRING is bounded at construction
   (`maxModelEffortLevel`) while the level COUNT is not, and name #1821. Leaving a false aggregate-bound
   claim on the consumer-facing type while fixing it on the producer's would be the worse half to miss.

**Also rewritten (not a #1820 site, but made stale by addition):**

8. `ModelOption.TruncatedFields`' doc enumerates the cuttable names exhaustively — `"resolved_model"`,
   `"value"`, `"display_name"`. Add `"effort_levels"`, and one clause for what is genuinely new: this
   name reports a cut on ONE OR MORE of the entry's levels and appears at most once whatever the count,
   because the report names fields and a list is one field. The declaration-order sentence lives on
   these same lines and stays true — `effort_levels` is last because its `boundEach` call is last.

9. `emitModelList`'s per-entry paragraph already carries a clause about how the bool differs from the
   three strings. One more: the level list goes through a cap like the strings but reports at most once
   per entry, and what an absent list READS AS is `turnevent.ModelOption.EffortLevels`' to say, not
   this function's.

**Explicitly not edited.** The comments still deferring the absent/empty question to the closed #1690 —
`protocol.ModelOption.MarshalJSON`'s and `internal/e2e/internal/fakeclaude`'s `initializeModels`' — are
#1822's, which lands after the reading is settled so it can state one settled answer. The distinction
from AC 3 is exact: those defer a question that is genuinely still open, so they wait for the answer;
AC 3's four name a slice that no longer exists. **`internal/streamsup/initialize_capture_test.go`'s
"#1811, #1812, #1719 and #1809" list is stale by two slices now and is deliberately left alone** — it
is pre-existing drift in a fourth file this slice otherwise does not touch, nothing fails on it, and
the line budget is better spent on the tests below.

## Concurrency model

Unchanged. `emitModelList` runs on `Parser.Write`'s single caller goroutine, holds no lock, spawns
nothing. `boundEach` closes over the same per-entry `cut` slice `bound` does and is called from the
same loop body on the same goroutine — the closure cannot be hoisted out of the loop for the reason the
existing comment gives, and the new one is no different. The slice assigned into the event is freshly
allocated per entry (or is claude's own zero-length one), and the parser retains neither.

## Error handling

The four rungs of `emitModelList` are unchanged in count, order and classification. Rung 1
(`undecodable`, no event) gains reachable shapes:

- `supportedEffortLevels` is a string, a number or an object → `*json.UnmarshalTypeError`, whole line
  dropped, no partial list.
- `supportedEffortLevels` is an array containing a non-string element → same, and this is the new one.
  The decode is all-or-nothing at the LINE, not per element: there is no path that keeps the good
  elements and drops the bad one, and that is the correct behaviour here — a list that silently lost an
  element would be published as claude's complete menu.

`null` decodes cleanly to nil per `encoding/json`'s documented no-op, so it does not reach rung 1.

**The error text is still never logged.** `emitModelList`'s undecodable arm drops the `err` because
`encoding/json` quotes the offending input bytes into its message. A bad level element quotes claude's
bytes exactly as a bad `value` does, so that rule is load-bearing here and must not be softened.

## Testing strategy

Four pins. All run inside `make check`: `internal/streamsup` carries no build tag on any file, and the
`e2e_realclaude` tag governs that package's Go files rather than its testdata, which is #1810's whole
reason for existing.

### AC 1 — the capture pin (extend `TestParser_InitializeControlResponseDecodesTheCapturedModels`)

- **A new accessor** beside `capturedModelString` / `capturedModelBool`, in that family's shape:
  `capturedModelStrings(t, entry, key) (values []string, present bool)`. Literal key string, never
  `modelOptionLine` — `capturedModelEntries`' doc gives the reason and it applies unchanged. Fatal when
  the key is present but not an array, or when an element is not a string: a capture that changed that
  is a capture this decode was never proven against. `(nil, false)` when absent.
- **A third coverage guard** before the comparison loop, mirroring #1819's `sawAutoModeTrue` /
  `sawAutoModeAbsent` pair: the captured entries must include at least one carrying the full five
  levels and at least one carrying no capability key at all. `sonnet` and `haiku` supply them today —
  AC 1 names both. Without it, a re-capture in which every entry carried the key would silently narrow
  what the loop proves while the loop stayed green.
- **In the per-entry loop, one comparison, split by presence** — and this split is the AC 3 obligation
  landing in the test:
  - key present → `slices.Equal(got.EffortLevels, want)`. Verbatim and in claude's own order; the
    capture's order (`low, medium, high, xhigh, max`) is neither alphabetical nor sorted, so this is
    also the no-reordering pin.
  - key absent → `len(got.EffortLevels) == 0`, **not** `== nil`. That is what both readings of the
    absent/empty question share, and it is all this slice may assert. Say so in a comment naming #1828,
    so a later reviewer reads the weaker assertion as deliberate rather than sloppy.

### AC 2 — the cut report aggregates (rows in `TestParser_ModelListFieldsAreCapped`)

A test-local `modelEffortLevelCapFixture = 32` joins the `modelResolvedCapFixture` family. Entries are
built with `modelEntryWithFixture(base, "supportedEffortLevels", []any{...})` — `modelEntryFixture`'s
three-parameter signature is not widened.

| row | entry | `wantCut` |
| --- | --- | --- |
| one over-long level | one level, over cap | `{"effort_levels"}` |
| **THREE over-long levels on one entry** | three levels, all over cap | `{"effort_levels"}` — once |
| one over-long among two that fit | mixed | `{"effort_levels"}`, plus a `check` that the two fitting levels came back verbatim and the list is still length 3 |
| all levels fit | five levels, short | `nil` |
| a string cut AND a level cut | over-long `resolvedModel` + over-long level | `{"resolved_model", "effort_levels"}` |

The three-over-long row is **the** AC 2 pin and the only one with sole redness against the realistic
mutant — a `boundEach` that appends inside the element loop instead of after it. The one-level row
stays green under that mutant, which is why it cannot carry the AC. The all-fit row is the sole red
against an unconditional append. The last row is the sole red against a `boundEach` call placed before
the three `bound` calls, which is the declaration-order contract.

A byte-length `check` on one cut element pins the constant's value; `truncateField`'s `<=` boundary and
its mid-rune deletion are already pinned by the existing rows and are not re-tested per element.

### AC 2/AC 3 — what the decode reads (new table test)

`TestParser_ModelListEffortLevelsReadClaudesKey`. Rows declare **what the built line carries** and
**what the daemon reads**:

| row | wire shape | reads |
| --- | --- | --- |
| claude's five levels | `["low","medium","high","xhigh","max"]` | the same five, same order |
| a scrambled order | e.g. `["max","low","high"]` | the same three, same order — no sorting |
| absent | key not in the entry | zero-length |
| published empty | `[]` | zero-length |
| present and `null` | `null` | zero-length |

**Absent and published-empty both read zero-length, which is exactly the vacuity trap
`streamsup-package.md`'s test-writing lesson describes** for #1819's pair: a table that only inspected
the decoded value would be satisfied by a fixture builder that quietly dropped the key, proving one
shape twice and calling it two. The guard is the same one, generalised rather than copied: **rename
`fixtureAutoMode` to `fixtureEntryRaw(t, line, i, key)` taking the literal key as a parameter, and
update its one call site in `TestParser_ModelListSupportsAutoModeReadsClaudesKey`.** Each row declares a
`wantWire` string (`""` meaning the entry must not carry the key at all) checked before the event is
inspected.

Assert `len(...) == 0` on the three zero-length rows, never `== nil` and never `[]string{}`. The test's
doc comment must say why in one sentence naming #1828 — and must say the thing a reviewer would
otherwise go looking for: **no mutant separating absent from published-empty is expected to redden
anything here, because this slice deliberately pins neither reading.** That is the design's content,
not a coverage gap.

### AC 2 — the log

No new assertion. `TestParser_InitializeControlResponseRejectBranches`' `wantAttrs` is an exact map
comparison by `reflect.DeepEqual`, so any attribute added to `logControlResponse` turns every row red.
Note that in the test's doc so the coverage is visible rather than assumed.

### Rung 1 — the undecodable rows

Two rows in the existing reject table:

- `supportedEffortLevels` is a string (`"low"`) — the shape a hand-written client or a future claude is
  most likely to send.
- `supportedEffortLevels` is `["low", 5]` — the element-level mismatch, the first in this family.

An object or a number row would be padding; the existing `supportsAutoMode` rows already pin that the
rung catches non-matching scalar kinds.

### What is deliberately not tested

- **Anything distinguishing nil from an empty non-nil `EffortLevels`.** See above; that is #1828's.
- **A wire/protocol round-trip.** `turnbridge.MapEvent` carries no `ModelList` case, so its `default`
  drops the variant until #1693. `protocol.ModelOption.EffortLevels` and its `MarshalJSON`
  normalisation already have their own shipped tests and are untouched here.

## Open questions

None blocking. One thing for the reviewer rather than the developer: whether doc edit 7
(`ModelList.Models`' "Both dimensions bounded") should have been left for #1821 along with the
arithmetic itself. This spec includes it because the sentence becomes factually wrong in this commit
and it is the sentence a consumer of `turnevent` reads, but it is the one AC 4 does not name.

## Security review

**Verdict:** PASS

**Findings:**

- **[Trust boundaries] No findings; one property is required by this spec's own deliverable.** The
  boundary is unchanged and remains one explicit function: `emitModelList`'s `json.Unmarshal` of the
  child's stdout line into `controlResponseLine` / `modelOptionLine`, claude-controlled on the input
  side. What changes is that this is the first field on the path where a SINGLE key carries an
  unbounded NUMBER of claude-authored strings, so the boundary's per-entry guarantee is no longer
  "bounded" without qualification. The spec closes that by requiring the qualification to be written
  where each reader lands: at `maxModelResolved`, at `maxModelListEntries`, at `ModelList.Models` and at
  the field's own doc. A downstream reader who inherits the old sentence would conclude the retained
  size is bounded per entry, and it is not. Post-decode the value is `[]string` with every element
  bounded — no length, no charset, no injection surface beyond what the three sibling strings already
  carry, and `protocol.ModelOption`'s SECURITY paragraph already assigns the render-side sanitization to
  the client for exactly these strings. This slice makes that shipped sentence true rather than widening
  it.
- **[Network & I/O] SHOULD FIX — stated in the spec, closure is #1821's.** The retained per-entry size
  becomes `768 + 32 × N` with N unbounded, and the pre-cap transient is unchanged in kind but not in
  degree: `defaultMaxParseBuf` caps the line at 4 MiB before the decoder sees it, and the densest legal
  level array (`[""` repeated) materialises far more `string` headers than the previous three-string
  entry did, so `maxModelResolved`'s "order 100 MB of transient" figure is the right ORDER but is no
  longer derived from the same shape. The mitigation available in THIS slice is exactly what it does:
  bound the element (32 bytes, the smallest honest number), keep the retention transient — nothing
  reaches the eventring or the wire until #1693 — and refuse to leave the completeness claim standing.
  A count bound is the real fix and it cannot be added here without also re-deriving
  `maxModelListEntries`, which is #1821's ticket. Flagged rather than gated because nothing publishes
  or retains the value in the window.
- **[Network & I/O] OUT OF SCOPE — the eventring multiplier, #1693 with #1821.** `eventring`'s
  `MaxEventsPerConversation` is 1024, so once #1693 publishes this variant a per-event size becomes a
  per-conversation memory multiplier. An unbounded third factor is therefore a memory knob and not only
  an envelope question, which raises the priority of #1821 relative to #1693 but changes nothing this
  slice can do. Named here so the ordering constraint is on the record before either lands.
- **[Error messages, logs, telemetry] No findings.** `logControlResponse` stays at exactly four
  attributes, none carrying claude's bytes, and the new field is never logged on any rung. The
  `undecodable` arm's deliberate discarding of the `json.Unmarshal` error is load-bearing here for a
  NEW reason and the spec says so: an element-level type error quotes the offending array element, so
  logging the error would route claude's own level strings into the daemon log through a channel no
  per-attribute check sees. #833's posture — "model / effort / YOLO values are NEVER logged at any
  level" — names *effort* explicitly, so this is the field of the family that rule was written about.
  The exact-map `wantAttrs` comparisons are the deterministic net behind the prose rule.
- **[Trust boundaries / authorization] No findings, and one confusion is pre-empted.** These levels are
  claude's claim about itself and are a REPORT to a client's menu. The adjacent hazard is real:
  `internal/relay`'s `validEffort` gates a phone-supplied effort value on an inbound path, and a future
  reader could "unify" the two — either by validating claude's outbound list against the enum (silently
  dropping a level claude adds, and making the daemon's menu a lie) or by widening the enum to whatever
  claude published (letting the subprocess extend what an untrusted inbound frame may set, which is a
  subprocess authorizing an input path). The spec forbids both directions explicitly and requires the
  field's doc to say it, so the two rules stay separate by construction.
- **[Tokens, secrets, credentials] No findings.** No token, key or credential is read, minted, stored
  or compared. The decode target's FIELD SET remains the standing guarantee: the initialize payload's
  `account` key is never declared on `modelOptionLine` or `controlResponseLine`, and this slice declares
  exactly one `[]string` and nothing else, so `description`, `supportsEffort`,
  `supportsAdaptiveThinking`, `supportsFastMode` and `account` all stay undeclared and unreachable by
  any later sweep that forgets to check them.
- **[File operations] No findings.** No production path touches the filesystem. The test reads the
  committed capture through `capturedInitialize`, which mints its path from package constants and
  selects by an arm identifier from a closed set — no caller-supplied path reaches it, so no traversal,
  TOCTOU or symlink question arises. No file is created; no mode is set. No re-capture is owed: the
  capability key names are already recorded in the realclaude probe's `models_entry_fields` union.
- **[Subprocess / external command execution] No findings.** This slice writes nothing to the child.
  The request half of the exchange is `WriteInitialize`'s and is untouched; no `exec.Command` argument,
  no environment variable, no signal path is involved. The decoded levels never flow back to the child
  as an argument — and the one field of this family a client does send back is `Value`, gated inbound by
  `validModel`, which this slice neither reads nor changes.
- **[Cryptographic primitives] Not applicable.** No randomness, no hashing, no key material, no
  comparison of an attacker-controlled value against a secret. The `slices.Equal` comparisons in the
  tests are against test expectations derived from the capture, not against credentials, so
  constant-time comparison is not in question.
- **[Concurrency] No findings.** No lock is taken, no shared state is read or mutated, no goroutine is
  spawned. `boundEach` closes over the same per-entry `cut` slice as `bound`, on the same single
  `Parser.Write` caller goroutine, and is subject to the same must-not-hoist rule the existing comment
  states — a hoisted accumulator would leak a cut report FORWARD across entries, which is a correctness
  bug the existing three-entry table row already catches. There is no partial state to recover after a
  mid-write signal: the slice is built and assigned within one loop iteration and the parser retains
  nothing.
- **[Threat model alignment] No findings for this slice; two named boundaries.** The relevant threat is
  the subprocess-output trust boundary, covered above. Client-side rendering stays where
  `protocol.ModelOption`'s SECURITY paragraph puts it — untrusted, model-influenced text the daemon
  bounds but does not sanitize, with the render boundary owing the sanitization. Publication is #1693's,
  and the provenance limit `emitModelList`'s doc already states (a shape discriminant rather than a
  correlated `request_id`) is not made more urgent by this field: a forged inventory can already
  misstate every string in the list, and one more list of strings does not change that calculus.

**Reviewer:** architect (self-review per the security-review checklist)
**Date:** 2026-08-27
