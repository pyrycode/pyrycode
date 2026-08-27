# #1821 — Bound the per-model effort-level count and re-derive the model-list envelope budget

**Size:** s (re-checked against this spec — see § Scope check)
**Packages:** `internal/streamsup`, `internal/turnevent`
**Labels:** `security-sensitive` (§ Security review ran)

---

## Files to read first

Read these before writing anything. Every entry names a **symbol**; resolve it with
`codegraph_search` / `codegraph_node` rather than a line number.

| File | Symbol | What to extract |
|---|---|---|
| `internal/streamsup/parser.go` | `maxTaskRosterEntries` | Where the 8192 number ACTUALLY comes from: it is this constant's PRODUCT (8 × 1024), which its doc *noticed* lands on half of `maxUnrecognizedRaw`. Read the four-bullet arithmetic block — it is the template every derivation in this family follows, and § Decision reuses its shape verbatim |
| `internal/streamsup/parser.go` | `maxUnrecognizedRaw` | The whole-line UNKNOWN budget (`16 << 10`) and the ordering rule stated against it. This is the ceiling § Decision re-anchors on |
| `internal/streamsup/parser.go` | `maxModelResolved` | The per-entry multiplicand (768) and the "ENVELOPE ARITHMETIC IS NO LONGER COMPLETE" paragraph — gap site 1 of 6 |
| `internal/streamsup/parser.go` | `maxModelEffortLevel` | The per-ELEMENT 32-byte cap, why it is 32, and why it is NOT sized to `validEffort` — gap site 2 of 6. **This constant does not move** |
| `internal/streamsup/parser.go` | `maxModelListEntries` | The full derivation, the `N ≤ 1.6` arithmetic, the "NOT 8" paragraph and the "NOT a power of two" paragraph — gap site 3 of 6, and the largest single rewrite in this ticket. **This constant does not move either**; its DOC does |
| `internal/streamsup/parser.go` | `emitModelList` | The four-rung classification, the entry-count cap's placement argument, and the doc paragraph describing the level list's cap and report |
| `internal/streamsup/parser.go` | `boundEach` (closure inside `emitModelList`) | The three load-bearing properties, the zero-length arm (#1828's collapse), and the sentence naming #1821 — gap site 4 of 6 and the only production behaviour change |
| `internal/streamsup/parser.go` | `truncateField` | The `<=` boundary convention the count bound mirrors |
| `internal/streamsup/parser.go` | `logControlResponse` | The FIXED four-attribute set, the admission rule (daemon-computed integers in, claude's bytes out), and `dropped`'s "the only observable the cap has" argument — § Decision extends this to five |
| `internal/turnevent/event.go` | `ModelOption.EffortLevels` | Six paragraphs; two need work — the "EACH ELEMENT IS BOUNDED AND THE COUNT IS NOT" paragraph (gap site 5 of 6) and the "A CUT LEVEL IS NOT A LEVEL CLAUDE PUBLISHED" paragraph, which gains the drop case |
| `internal/turnevent/event.go` | `ModelOption.TruncatedFields` | The convention, and the `"effort_levels"` paragraph whose "however many were CUT" now has to cover a shortened list too |
| `internal/turnevent/event.go` | `ModelList.Models` | The "THREE DIMENSIONS SINCE #1827 AND ONLY TWO OF THEM ARE BOUNDED" paragraph — gap site 6 of 6 |
| `internal/turnevent/event.go` | `ModelList.DroppedModels` | Why the entry-count dimension reports as a COUNT and at the LIST level — the asymmetry § Decision has to justify departing from |
| `internal/streamsup/parser_test.go` | `modelListEntriesCapFixture` and the const block's doc | The literal-fixture rule and its stated reason. The new count fixture goes here |
| `internal/streamsup/parser_test.go` | `TestParser_ModelListEntryCountIsBounded` | The shape the new test follows: a row table plus a record subtest, the tail-truncation pin, the "exactly at the cap" row's equivalent-mutant comment, and the "empty and absent are deliberately NOT rows here" exclusion |
| `internal/streamsup/parser_test.go` | `TestParser_ModelListFieldsAreCapped` | The per-element level rows and the aggregation pin ("THREE over-long levels … name effort_levels ONCE") |
| `internal/streamsup/parser_test.go` | `modelEntryFixture`, `modelEntryWithFixture`, `modelEntriesFixture`, `modelListLineFixture` | The fixture builders. A level-list fixture is the one new helper |
| `internal/streamsup/parser_test.go` | `TestParser_ControlResponseAckIsConsumedSilently`, `TestParser_InitializeControlResponseRejectBranches`, `TestParser_ModelListIsLoggedContentFree` | The three OTHER `reflect.DeepEqual(attrs, wantAttrs)` sites that must gain the fifth attribute |
| `docs/knowledge/features/streamsup-package.md` | § "The per-entry byte budget is a floor, not a total, since #1827" | Carries the same gap statement and the same `N ≤ 1.6` arithmetic. **Read it; do NOT edit it** — the documentation phase owns that file |

---

## Context

`emitModelList` decodes claude's `initialize` reply into a `turnevent.ModelList`. Three
dimensions bound its retained size: the per-entry text caps (#1811), the entry COUNT
(#1812), and the number of effort LEVELS per entry. #1827 landed the level list and
bounded each level *string* at 32 bytes; #1828 settled how a zero-length one reads. Both
deliberately left the level COUNT unbounded and recorded the gap in **six places**, four in
`internal/streamsup/parser.go` and two in `internal/turnevent/event.go`.

Leaving it open is not cosmetic. The committed per-entry multiplicand (768 bytes) and the
committed product (`10 × 768 = 7680`) describe a budget the code no longer has: the honest
per-entry figure is `768 + 32N` with `N` chosen by claude. `maxModelListEntries`' own doc
already writes out why a level-count cap alone cannot repair it —
`10 × (768 + 32N) ≤ 8192` requires `N ≤ 1.6` — and hands the choice of lever to this ticket.

This ticket closes the dimension **and** repairs the arithmetic. The arithmetic repair is
the substance: § Decision makes every number choice so the developer implements rather than
re-derives.

**ADR-worthy, and this spec does not write it.** The generalisable lesson is *an aggregate
variant's product is a RESULT, not a ceiling for the next variant.* `maxTaskRosterEntries`
derived 8 from an observation and then *noticed* its product landed on half of
`maxUnrecognizedRaw`; `maxModelListEntries` inherited that noticed landmark as a
**constraint**, and this ticket has to demote it back. The next aggregate variant will face
the same temptation with 5/8. If the documentation phase judges that worth a decision
record, this paragraph is the source; **the developer writes no file under
`docs/knowledge/`.**

---

## Decision — the numbers

This section is the ticket's substance. **Take these numbers as given.** Do not re-derive
them; do transcribe the arguments into the doc comments § Doc sites requires.

### The proof that the ceiling must move

Let `E` = the entry-count cap, `L` = the level-count cap. Per-entry claude-derived text is
`3 × 256 + 32L = 768 + 32L`. The product is `E × (768 + 32L)`.

Both factors have a committed doctrine floor:

- **`L ≥ 6`.** claude sends five levels. A cap AT the observation fires the moment claude
  ships a sixth, which is the failure `maxModelListEntries`' "NOT 8" paragraph rejects by
  name at its own scale ("a cap firing on claude's ORDINARY output").
- **`E ≥ 10`.** The observation is six entries. `maxModelListEntries`' doc rejects 8 by
  name for the same reason; 10 is the committed value and nothing new argues it down.

Minimum product consistent with both floors: `10 × (768 + 192) = 9600`. That is **greater
than 8192**. So *no* pair of caps satisfying the family's own doctrine fits the inherited
ceiling — the ceiling is not a lever among three, it is the only one left, and the ticket's
framing is confirmed rather than assumed.

Also closed, because the next reader will ask:

- **Lowering `maxModelEffortLevel` (32 → 10)** is what 8192 would need at `E=10, L=5`. Its
  own doc argues 32 against spellings claude has not shipped (`ultrathink` is 10 bytes,
  `extended-thinking` 17), and `maxModelListEntries`' doc already rejects 10 bytes as a cap
  that "would fire on ORDINARY output".
- **Raising `maxUnrecognizedRaw`** would loosen the bound on an UNKNOWN line to make room
  for a KNOWN one. That inverts the ordering rule it exists to state. Reject by name.

### The chosen numbers

| Constant | Value | Status |
|---|---|---|
| `maxModelEffortLevelCount` | **8** | NEW |
| `maxModelEffortLevel` | 32 | unchanged, doc edited |
| `maxModelResolved` / `maxModelValue` / `maxModelDisplayName` | 256 | unchanged, one doc edited |
| `maxModelListEntries` | **10, unchanged** | value unchanged, doc substantially rewritten |
| the ceiling | **`maxUnrecognizedRaw` (16 KiB), fraction stated per shape** | 8192 demoted from constraint back to the roster's product |

**Why `L = 8`:**

- Three slots above an observation of five — a 1.6× multiple, essentially
  `maxModelListEntries`' own 1.67× (10 over 6), and thinner than the text caps' 10× for the
  reason that constant already states: a cardinality overflow is REPORTED, and the reported
  failure mode is what buys the thinner margin.
- A power of two, matching every constant in the family except `maxModelListEntries`
  (whose doc explains why it alone is decimal).
- It gives the level LIST a budget of `8 × 32 = 256` bytes — **exactly one string field's
  cap** — so the per-entry multiplicand becomes `4 × 256 = 1024` bytes. That is
  `maxTaskRosterEntries`' per-entry unit, to the byte. The family now has ONE entry unit
  across both of its aggregate variants, which is a stronger and more legible invariant
  than the products agreeing.

Write the doc in `maxTaskRosterEntries`' rhetorical order: doctrine first, landmark second
("8 is 8x the observation — … and the number that makes the product land on a clean 8 KiB"
is the sentence shape). The landmark is a confirmation, not the reason.

**Why `E` stays 10:** its own doc rejects 8 by name on evidence that has not changed, and
trading a bound on a dimension claude has never inflated (levels: 5 of 8) for a tighter
bound on the dimension claude is closest to (entries: 6 of 10) is the wrong direction.

**The new product and the restated ceiling:**

- Per entry: `maxModelResolved + maxModelValue + maxModelDisplayName +
  (maxModelEffortLevelCount × maxModelEffortLevel) = 256 + 256 + 256 + 256 = 1024` bytes.
- Product: `10 × 1024 = 10240` bytes = **10 KiB**, ten entries at the family's
  one-kibibyte entry unit.
- Against the v2 application-envelope cap of 65519 bytes (`docs/protocol-mobile.md`
  § Application-envelope size cap): **15.6%**. The family reads 7.4%, 6.6%, 12.5%, 15.6%.
- Against `maxUnrecognizedRaw`'s whole-line 16 KiB: **5/8**. Retained-vs-retained, which is
  the comparison the ordering rule is about.
- Escaping, in `maxUnrecognizedRaw`'s words verbatim: JSON string values, so the growth is
  quotes and backslashes rather than a `\u00XX` expansion of every byte; pathological
  all-quote content roughly doubles it to ~20 KB, ~31% of the envelope. **State this
  against the ENVELOPE only** — the `maxUnrecognizedRaw` comparison is on retained bytes
  and mixing the two would compare a doubled wire figure against a retained cap.

**The ceiling paragraph the doc must carry.** 8192 was never derived as a ceiling: it was
`maxTaskRosterEntries`' PRODUCT, whose doc *noticed* it landed on half of
`maxUnrecognizedRaw`, and `maxModelListEntries` inherited the noticed landmark as a
constraint. What survives is the rule the roster actually stated — *a whole KNOWN event
must not approach the cap on an entire UNKNOWN line* — and the fraction is stated per
SHAPE rather than fixed: roster 1/2, model list 5/8. Say explicitly that **the next
aggregate variant re-derives its fraction rather than inheriting 5/8**, which is exactly
the mistake this commit is undoing. That sentence is the one piece of this ticket that pays
forward.

---

## Design

### 1. The new constant — `maxModelEffortLevelCount`

Declared in `internal/streamsup/parser.go`, immediately after `maxModelEffortLevel` and
before `maxModelListEntries`, so the reading order is element cap → count cap → entry cap.

```go
const maxModelEffortLevelCount = 8
```

**The name is `…Count`, not `maxModelEffortLevels`, and the doc must say why.** The plural
form differs from `maxModelEffortLevel` by one character, both are `int`, and both bound the
same field. Swapping them at a call site compiles: an 8-byte element cap would cut `medium`
— claude's ordinary output, on every child — and a 32-level count cap would bound nothing.
The `Entries` suffix the family's two other cardinality caps use is not available without
`maxModelEffortLevelEntries`, which is worse. This is the same reasoning `maxTaskPatch`
uses for staying a separate constant from `maxTaskDescription`: prevent a silent coupling
before it can happen.

Doc content: the derivation of § Decision (doctrine floor, the 1.6× multiple, the power of
two, the 256-byte list budget landing the per-entry unit on 1024), the construction-time
application, and the REPORTED-overflow property. Follow `maxTaskRosterEntries`' four-bullet
arithmetic shape. The aggregate arithmetic lives at `maxModelListEntries` — cross-reference
it rather than restating it, exactly as `maxModelResolved` does today.

### 2. The count bound — inside `boundEach`

`boundEach`'s signature does **not** change and its one call site is **not** edited. The
count constant is read from package scope inside the closure; only the element `limit`
stays a parameter. State the reason in the doc: a fourth `int` parameter beside `limit`
puts the two caps adjacent at the call site, which is the swap the naming decision above
already spends a paragraph preventing, and `boundEach` — unlike `bound`, which serves three
fields with three limits — has exactly one call site and one field, so parameterizing the
second limit buys nothing.

The name `boundEach` is **retained**. Its current doc says it is "named for the ELEMENT the
cap applies to, because what bounds the list's LENGTH is nothing yet", which reads as an
invitation to rename; answer it instead. Bounding each element is still what the closure
does per value; the count bound is one statement before the loop and the report is still
one name. A rename would also rot two by-symbol citations in other declarations
(`emitModelList`'s doc and `turnevent.ModelOption.EffortLevels` both name `boundEach`) for
no behavioural gain.

Statement order inside the closure, all three properties load-bearing:

1. **The zero-length arm stays FIRST and unchanged.** #1828's collapse is untouched: an
   absent key, a JSON `null` and a published `[]` still return `nil` before anything else
   runs and still name nothing in `TruncatedFields`.
2. **Then the count bound.** `len(values) > maxModelEffortLevelCount` → slice to
   `[:maxModelEffortLevelCount]`, and record that a drop happened. Truncation is **from the
   tail**, for `emitModelList`'s stated reason: claude's order is preserved because no
   ranking is invented, its ordering semantics being unobserved. The `>` boundary matches
   `truncateField`'s `<=` convention and the entry cap's.
3. **Then the element loop, unchanged**, over the (possibly shortened) slice.

The placement argument transfers verbatim from the entry-count cap and should be stated:
a cap of `>= 1` can neither create an empty list nor rescue one, so it cannot move a rung's
classification, and — the half specific to this level — it cannot turn a non-empty list into
the empty one whose reading #1828 settled, nor rescue an empty one into a non-empty reading.
The two mechanisms are independent by construction, not by care.

### 3. The report — one name, `TruncatedFields`, at most once per entry

`"effort_levels"` is appended to the entry's `cut` slice **once**, if the count bound fired
OR any element was cut OR both. Concretely: the existing `cutAny` flag is set by the count
bound as well as by the element loop, and the single post-loop append is unchanged.

**Why a flag here when the entry-count dimension reports a COUNT.** The asymmetry is real
and has to be argued rather than glossed:

- **Publishability decides it.** `protocol.ModelOption` has no per-entry count field and
  this slice adds no wire field, so a per-entry dropped-level integer would be discarded by
  the mapping at #1693. That is the argument `ModelOption.SupportsAutoMode`'s doc already
  makes against a `*bool` — "a pointer here would preserve a distinction only long enough
  for the mapping (#1693) to discard it" — and `ModelOption.EffortLevels`' #1828 paragraph
  makes a second time. The house has settled this shape twice.
- **The consumer instruction is identical either way.** `DroppedModels` earns a count
  because "6 of 40 models" and "6 of 7 models" are different menus to render. For levels
  the rule is already absolute: a list that is not claude's whole published menu **must not
  be offered as one**, whether one level was cut or ninety were dropped. A count would be
  decoration on a decision already made.

**What it gives up, stated rather than waved away:** the true level count is not
recoverable from the event, where `len(Models) + DroppedModels` recovers the true entry
count. That is the accepted price of adding no wire field, and reopening it costs a ticket
plus a `protocol.ModelOption` field at #1693. `ModelOption.EffortLevels`' doc already has a
"WHAT IT GIVES UP" paragraph idiom — put it there.

**The consumer-visible strengthening.** `EffortLevels`' existing paragraph says "A CUT
LEVEL IS NOT A LEVEL CLAUDE PUBLISHED". After this ticket the name `"effort_levels"` also
means *levels claude published may be ABSENT from this list entirely*. Same instruction,
strictly stronger claim; fold it into that paragraph rather than adding a seventh.

### 4. The observable — a fifth attribute on `logControlResponse`

**This is a deliberate extension beyond AC 1's letter, and the reason is AC-adjacent rather
than scope drift.** `logControlResponse`'s doc argues, for `dropped`: *"Until #1693
publishes the event, this record is the ONLY observable the cap has: without it, a cap
firing in production is a cap nobody can know fired, and the first evidence that 10 is the
wrong number would arrive as a user's short menu."* That argument applies to the level cap
verbatim — nothing reads `TruncatedFields` either, until #1693. A `security-sensitive`
ticket shipping a cap on subprocess-supplied data with no operational signal is the same
blind spot #1812 spent a paragraph closing, one commit ago, on the same function.

- Signature becomes `logControlResponse(reason string, models, dropped, levelsDropped int)`.
- Attribute `"levels_dropped"`: the **total number of level strings dropped across the
  RETAINED entries**. Entries removed by the entry cap are already counted by `dropped`;
  do not double-count them.
- Admissible under the function's own stated rule: DAEMON-computed integer, carries none of
  claude's bytes. #833's posture (model / effort / YOLO values are NEVER logged at any
  level) is untouched — no level STRING goes anywhere near this record.
- All four call sites change; the three non-emitting rungs pass a third `0`. The doc's
  "attribute set is FIXED at four on every rung" sentence becomes FIVE, with the same
  reason for why the non-emitting rungs pass zeros rather than omitting keys.
- Accumulate the total in `emitModelList`'s per-entry loop. `boundEach` returns
  `[]string`; have it record the per-entry drop into a per-entry variable the loop adds up,
  in the same way `cut` is a per-entry slice the loop consumes. Do **not** hoist a
  running counter into the closure's captured state in a way that outlives the entry — the
  "a cut on one entry does not appear on the entries AFTER it" row exists because that
  mistake is the realistic one on this surface.

---

## Doc sites to close

AC 2 requires `git grep '#1821' -- internal/` to find **nothing** afterwards, and requires
each site **closed rather than restated**. Verify with that exact command before committing.

| Symbol | What the paragraph must become |
|---|---|
| `maxModelResolved` | The "ENVELOPE ARITHMETIC IS NO LONGER COMPLETE" block becomes a COMPLETE one. Multiplicand 1024, not 768 — four terms, all bounded. It still hands the multiplicand forward to `maxModelListEntries` rather than restating the aggregate |
| `maxModelEffortLevel` | The closing "It bounds the ELEMENT and nothing bounds the COUNT" paragraph becomes a pointer to `maxModelEffortLevelCount`, and states the one thing that constant's existence changes about this one: this cap is now multiplied by a KNOWN factor, which is what turns "generosity here multiplies" from a warning into an arithmetic term |
| `maxModelListEntries` | The largest rewrite. The multiplicand bullet becomes 1024; the ceiling bullet carries § Decision's demotion of 8192 and the restated ordering rule with its 5/8 fraction; the product bullet becomes 10240 / 15.6% / 5/8 with the escaping note against the envelope; the `N ≤ 1.6` gap paragraph is **replaced** by the impossibility proof (both doctrine floors → 9600 > 8192 → the ceiling is the only lever) and by which numbers moved and why the others could not. The "NOT 8" and "NOT a power of two" paragraphs survive and are the reason `E` did not move — say so |
| `boundEach` | The "named for the ELEMENT … nothing yet — see maxModelEffortLevel and #1821" sentence becomes the retained-name answer of § Design 2. The three properties become four (zero-length arm, count bound from the tail, per-element bound preserving order, one name at most once). Bullet 2's "cutting an element neither drops it nor disturbs claude's order" must be scoped to the ELEMENT cap — it is no longer true of the closure as a whole |
| `turnevent.ModelOption.EffortLevels` | The "EACH ELEMENT IS BOUNDED AND THE COUNT IS NOT" paragraph becomes "both are bounded", naming `maxModelEffortLevelCount`. The "A CUT LEVEL IS NOT A LEVEL CLAUDE PUBLISHED" paragraph gains the drop case. A "WHAT IT GIVES UP" sentence records the unrecoverable true count. The other four paragraphs (purpose/verbatim, #1828's collapse, the `validEffort` separation, untrusted-text) stay |
| `turnevent.ModelList.Models` | The "THREE DIMENSIONS … ONLY TWO OF THEM ARE BOUNDED" paragraph becomes all three bounded, naming the producer constant for each and where each reports |

Two further sentences, not `#1821` sites but made wrong by this change — closing them is
part of the ticket:

- **`emitModelList`'s doc**, the paragraph beginning "The level LIST goes through a cap like
  the three strings": "however many of that entry's levels were cut" must cover a shortened
  list too, and the paragraph should name the count cap beside the element cap.
- **The `EffortLevels:` field comment inside `emitModelList`'s composite literal**: "claude's
  order and the list's cardinality survive a cut untouched" is now true of the ELEMENT cap
  only. Scope it and name what the count cap does instead.

- **`turnevent.ModelOption.TruncatedFields`**: the `"effort_levels"` paragraph says the name
  reports "a cut on ONE OR MORE VALUES". It now reports a cut, a shortened list, or both —
  still at most once per entry, still last in declaration order.

**Out of scope, deliberately:** `docs/knowledge/features/streamsup-package.md` carries the
same gap statement and the same `N ≤ 1.6` arithmetic. It belongs to the documentation
phase. Do not edit it, and do not add a knowledge-base AC.

---

## Concurrency model

Unchanged, and nothing here introduces one. `emitModelList` runs on the parser's single
line-consuming goroutine; `boundEach` is a closure over per-entry locals with no shared
state, no lock and no channel. The one concurrency-shaped hazard is the per-entry
accumulation described in § Design 4 — a level-drop counter that outlives one entry is the
same leak the existing "a cut on one entry does not appear on the entries AFTER it" row
catches, and it is a scoping bug rather than a race.

---

## Error handling

No new failure mode and no new error value. The count bound is total: any `[]string`
either fits or is shortened, and both outcomes are reported through the existing per-entry
convention. The four-rung classification of `emitModelList` is untouched — the count bound
sits below rung 3 and, being `>= 1`, can neither create an empty list nor rescue one, so it
cannot move a rung.

The transient-memory exposure is unchanged and already argued at `maxModelResolved`: the
cap is applied AFTER `json.Unmarshal`, `defaultMaxParseBuf` caps the whole line at 4 MiB
first, and what this cap bounds is what is RETAINED and what crosses the wire. Adding the
level-count bound **reduces** that retained figure; it does not change the transient one.

---

## Testing strategy

Scenarios, not code. Write them in the package's idiom — table-driven, `t.Parallel()`,
stdlib only.

### New fixture constant

Add `modelEffortLevelCountCapFixture = 8` to the existing const block beside
`modelListEntriesCapFixture`, and extend that block's doc with one sentence in
`modelListEntriesCapFixture`'s own words: a count fixture written as the production
constant would follow it green if someone halved it. **The `want` in every row below is
this literal or an expression over it — never `maxModelEffortLevelCount`.**

### New helper

One builder producing a level list of `n` elements, each individually identifiable (the
`modelEntriesFixture` shape — e.g. `level-000`, `level-001`, …) so tail-truncation is
PINNED rather than assumed from a length.

### New test — `TestParser_ModelListEffortLevelCountIsBounded`

Follow `TestParser_ModelListEntryCountIsBounded`'s two-subtest structure.

Row table (one entry per line, each row a level list of the stated size):

- **`cap + 1` levels** → `cap` retained, `TruncatedFields` is `{"effort_levels"}`.
- **exactly `cap` levels** → all retained, `TruncatedFields` is `nil`. *This is the
  sole-red row for a cap that fires one early and for a halved constant* — with the
  literal fixture, lowering `maxModelEffortLevelCount` to 7 makes this row and only this
  row fail. Carry the equivalent-mutant note the entry-count test already carries: `>` and
  `>=` are indistinguishable at `len == cap`, because both compute a zero drop and slice to
  identity.
- **`cap - 1` levels** → all retained, `nil`. Under the cap, so the bound is not proven
  only at its own boundary.
- **a large list (e.g. 100 levels)** → `cap` retained, `{"effort_levels"}`. Pin every
  surviving element against the fixture's first `cap` in order — the row that fails a
  head-truncating `values[len(values)-cap:]`.

Interaction rows, which the entry-count test has no analogue for:

- **Over the cap, every surviving element short** → the name appears exactly ONCE, from
  the DROP alone. This is the row proving the count bound reports at all; without it the
  report is only ever exercised by the element cap.
- **Over the cap, at least one surviving element over-long** → the name still appears
  exactly ONCE. The aggregation pin extended to two mechanisms.
- **Over the cap on entry 1, a clean entry 2, a clean entry 3** → entries 2 and 3 report
  `nil`. Same forward-leak shape as the existing "a cut on one entry does not appear on the
  entries AFTER it" row, now for the drop path.
- **A list at the cap whose elements are the capture's five real levels plus three
  synthetic ones** — optional, and only if it costs nothing: it states that claude's
  observed menu is comfortably inside the cap.

Record subtest, following "the record names both numbers":

- One line whose entries' level lists exceed the cap → the record's attrs compare
  `reflect.DeepEqual` against a five-key map with `"levels_dropped"` non-zero. This is the
  **only** place that attribute is non-zero; without it a producer hard-coding `0` stays
  green everywhere else, which is precisely why `dropped` has such a row.

### Explicitly NOT rows here

Empty, `null` and absent level lists. They return at `boundEach`'s zero-length arm before
the count bound runs, and #1828's table already covers them. A row here would assert the
count bound against an input it never sees — the exclusion `TestParser_ModelListEntryCountIsBounded`
states for the ack rung, at this level.

### Existing tests that must be updated

Four `reflect.DeepEqual(attrs, wantAttrs)` sites gain `"levels_dropped": "0"`:
`TestParser_ControlResponseAckIsConsumedSilently`,
`TestParser_InitializeControlResponseRejectBranches`,
`TestParser_ModelListEntryCountIsBounded`'s record subtest, and
`TestParser_ModelListIsLoggedContentFree`. Nothing else in the suite should move; if
something does, that is a signal the change reached further than designed.

### Gate

`make check`. The committed-capture pin runs inside it (#1810's reader reads the bytes from
this package), so the six-entry / five-level capture is exercised without a live run. **No
`make e2e-realclaude` and no re-capture is owed** — nothing here touches the live suite.

---

## Scope boundaries

- **No wire frame, no `protocol` type, no client.** `turnbridge.MapEvent`'s `default` drops
  the variant until #1693.
- **No new event field.** The report rides the existing `ModelOption.TruncatedFields`.
- **`internal/relay`'s `validEffort` is not read, widened or narrowed.** It bounds a
  phone-supplied override on an INBOUND path and is deliberately a different rule, as both
  `maxModelEffortLevel` and `ModelOption.EffortLevels` already state.
- **No knowledge-base file.** `docs/knowledge/features/streamsup-package.md` is the
  documentation phase's.
- **`maxModelEffortLevel` (32) and `maxModelListEntries` (10) keep their values.** Only
  their docs move. If you find yourself editing either literal, stop — § Decision explains
  why neither is the lever.

---

## Scope check

Re-applied to this written spec, not to the sketch:

| Boundary | Limit | This spec |
|---|---|---|
| Production source files created or modified | ≤ 3 | **2** — `internal/streamsup/parser.go`, `internal/turnevent/event.go` |
| Total written work | ≤ 400 lines | **~350** — ~195 production (one new constant with its derivation doc, ~5 lines of closure code, one signature + 4 call sites, six doc-site rewrites plus three scoping sentences) and ~155 test (one fixture constant, one helper, one test of ~9 rows plus a record subtest, four one-line `wantAttrs` additions) |
| New exported types or interfaces | ≤ 5 | **0** |
| Consumer call sites needing simultaneous update | ≤ 10 | **5** — four `logControlResponse` calls plus its declaration. `boundEach`'s single call site is deliberately untouched |
| Acceptance criteria | ≤ 5 | **4** |
| Distinct error/reject branches in a state machine | ≤ 10 | **4** rungs, unchanged; `boundEach` gains one branch |

Sized against the nearest analogue, re-derived rather than quoted: #1812 (`db1f2631`) is
the same cardinality-cap-plus-report shape at **313 insertions / 64 deletions across 5
files**, two of which were the `protocol` wire pair this slice excludes — so ~355 changed
lines across the same three files, of which ~333 are in scope here. This ticket trades
#1812's new event field, wire field and log attribute for six doc-site rewrites plus one
log attribute, which lands in the same band. Every boundary holds; no line needs a
rationalization to fit, and none is offered.

---

## Open questions

1. **The ticket body's sole-redness sentence reads backwards against the committed code, and
   the code wins.** The body says *"The row that discriminates a cap firing one early is
   `cap - 1`, not `cap`."* `TestParser_ModelListEntryCountIsBounded`'s "exactly at the cap"
   row says the opposite in its own comment, and the arithmetic confirms it: lower the
   constant by one and the `cap - 1` row stays green (`cap-1 ≤ cap-1`) while the `cap` row
   goes red. § Testing includes **both** rows with the roles stated, so the spec is correct
   either way. Flagged so a reviewer reading the ticket does not file it as a deviation.
2. **`"levels_dropped"` is an extension beyond AC 1's letter**, argued in § Design 4. If
   code review judges it out of scope, it drops cleanly: remove the parameter, the four
   call sites' third zero, the four `wantAttrs` keys and the record subtest. Nothing else
   depends on it. The spec's position is that a cap with no observable, on a
   `security-sensitive` path, is the blind spot `dropped`'s own doc exists to close.
3. **The `1024` per-entry unit is now shared by two variants with different field sets**
   (roster: 256 + 256 + 512; model: 4 × 256). That is a coincidence of arithmetic presented
   as a family invariant. It is a good invariant while it holds, and the doc should present
   it as *the number both landed on*, not as a rule the next variant must satisfy — the same
   demotion this ticket performs on 8192, applied pre-emptively so it is not inherited as a
   constraint next time.

---

## Security review

**Verdict:** PASS

**Findings:**

- **[Trust boundaries] No findings — the boundary is unchanged and this ticket tightens it.**
  Untrusted data crosses at exactly one place: claude's subprocess stdout → `emitModelList`,
  via `json.Unmarshal` into `controlResponseLine`. Downstream code holds
  `turnevent.ModelOption`, whose `EffortLevels` doc states in terms what a holder has —
  "BOUNDED AND UTF-8-VALID IS ALL THEY ARE" — and names the client as the render boundary
  owing sanitization. This ticket adds no boundary and moves none; it closes the one
  dimension of that boundary that was still a function of a number claude chooses. The
  `validEffort` separation (inbound closed enum vs outbound verbatim list) is restated in
  § Scope boundaries as not-to-be-touched, because merging the two rules in either
  direction is the live mistake on this surface and both existing docs say so.

- **[Network & I/O] The category this ticket IS, and the finding is that the pre-change
  state was the exploitable one.** Before: retained bytes per `ModelList` were
  `10 × (768 + 32N)` with `N` unbounded — a subprocess publishing 10 000 effort levels per
  entry yields a ~3.2 MB retained event, bounded only by `defaultMaxParseBuf`'s 4 MiB line
  cap. After: `10 × 1024 = 10240` bytes, hard. Three exposures re-checked and none is a
  MUST FIX:
  - *Transient memory.* The cap is applied after `json.Unmarshal`, so a hostile array is
    materialised before it is shortened. Unchanged from `maxTaskRosterEntries`' accepted
    trade and bounded by `defaultMaxParseBuf` at 4 MiB per line, one line in flight.
  - *Retained memory beyond one event.* `eventring` retains control-class events per
    conversation, so a per-event cap is also a per-conversation multiplier. This ticket
    moves that multiplier's factor **down** from unbounded to 10 KiB; it does not raise it.
    Worth naming because the ceiling in § Decision moved UP (8192 → 10240), and a reader
    could mistake that for a loosening. It is not: the number that moved is a ceiling on an
    already-bounded pair of dimensions, while the dimension that was actually unbounded is
    being closed. Net retained worst case falls from ~3.2 MB to 10 KiB.
  - *Wire.* 10240 bytes is 15.6% of the v2 application-envelope cap of 65519 bytes, ~31%
    after pathological escaping. Nothing publishes the variant until #1693 in any case.

- **[Error messages, logs, telemetry] No findings, and one new attribute audited.**
  `"levels_dropped"` is a daemon-computed integer derived from slice lengths. It carries no
  byte of claude's payload, which is the admission rule `logControlResponse`'s own doc
  states, and #833's posture (model / effort / YOLO values are NEVER logged at any level) is
  untouched. The realistic way this rule breaks is a drop site explaining itself with the
  value it dropped — a `"level", level` appended beside the counter — and
  `TestParser_ModelListIsLoggedContentFree` sweeps every record for planted sentinels, so
  that mistake is already gated. No level string, no `resolvedModel`, no `request_id`, no
  unmarshal error text enters any record on any of the four rungs.

- **[Concurrency] No findings — no new shared state.** `emitModelList` runs on the parser's
  single consuming goroutine; the count bound is a local slice reslice inside a per-entry
  closure. The one scoping hazard — a level-drop counter that outlives one entry — is named
  in § Design 4 and § Concurrency model, and the existing "a cut on one entry does not appear
  on the entries AFTER it" row is the shape of test that catches it. No lock, no goroutine,
  no channel added.

- **[Threat model alignment] Addressed by construction; one item named out of scope.** The
  relevant threat is a compromised or malfunctioning `claude` subprocess inflating a decoded
  event, which `docs/protocol-mobile.md` § Application-envelope size cap bounds at the wire
  and which this family bounds at construction — the stronger position, since it also covers
  the eventring and the push queue, neither of which the wire cap reaches. **Out of scope
  and named:** provenance. `emitModelList`'s gate recognises a SHAPE (`subtype == success`
  AND a non-empty `models` array), not a correlated reply to the daemon's own
  `WriteInitialize`, so a future claude putting a `models` array in some other successful
  control response would have it read as an inventory. That limit is stated in the function's
  own doc and in `docs/knowledge/features/streamsup-package.md`; it is unchanged by this
  ticket and is revisited when #1693 first lets a client read the value.

- **[Tokens, secrets, credentials] Not applicable — no credential material on this path.**
  The decoded payload is a model inventory; the one identifier it carries (`request_id`) is
  already excluded from every record by `logControlResponse`'s fixed attribute set, and this
  ticket adds an integer to that set rather than a string.

- **[File operations] Not applicable — this path performs no filesystem access.**
  `emitModelList` reads a line from the parser's buffer and emits an in-memory event. The
  only file involved is the committed test capture, which is read by the test binary.

- **[Subprocess / external command execution] Not applicable to the change.** The subprocess
  is the source of the untrusted data rather than a target of it: nothing in this design
  passes a decoded value to `exec.Command`, to `claude`'s stdin, or anywhere else. The
  values are bounded, retained, and — until #1693 — read by nothing.

- **[Cryptographic primitives] Not applicable — no randomness, no comparison against a
  secret, no key material.** The one comparison on this path is
  `cr.Response.Subtype != controlResponseSuccess`, a byte-exact match against a
  daemon-authored constant with no secret on either side.

**Reviewer:** architect (self-review per the security-review checklist)
**Date:** 2026-08-27
