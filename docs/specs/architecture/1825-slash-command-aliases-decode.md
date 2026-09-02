# #1825 — decode each slash command's aliases onto the bounded command list

## Files read

- `internal/streamsup/parser.go` → `commandEntryLine` — the decode target: three of four keys declared, its
  doc's THREE FIELDS OF FOUR paragraph and its null carve-out paragraph both name this slice by number and
  both go false here.
- `internal/streamsup/parser.go` → `emitSlashCommandList` — where the bound rides. Its `bound` closure is
  `string -> string` and closes over the per-entry `cut`; a `[]string` does not fit that signature.
- `internal/streamsup/parser.go` → `emitModelList` — carries `boundEach`, the family's ONE existing
  list-valued bounding closure: zero-length arm, count bound from the tail, per-element `truncateField`,
  report appended at most once. This slice's closure is that one's shape.
- `internal/streamsup/parser.go` → `truncateField` — the cut helper: `<=` boundary, empty-replacement scrub
  so a cut lands 1–3 bytes under the limit.
- `internal/streamsup/parser.go` → `maxModelEffortLevel`, `maxModelEffortLevelCount` — the only existing
  element-cap/count-cap PAIR on a nested list, and the doc shape the two new constants copy.
- `internal/streamsup/parser.go` → `maxSlashCommandName`, `maxSlashCommandDescription`,
  `maxSlashCommandArgumentHint` — the three scalar caps. The first two state the per-entry term (768) this
  slice's product adds to; the second also carries the fraction table #1826 reads; the third hands this
  slice the name-the-base obligation by name.
- `internal/streamsup/parser.go` → `logControlResponse` — six fixed attributes, and the doc paragraph whose
  "ONLY alias is UNREACHABLE by omission" sentence this slice falsifies.
- `internal/turnevent/event.go` → `SlashCommand`, `SlashCommandList` — the daemon-internal type: the slot
  between `Description` and `TruncatedFields`, and the SECURITY paragraph that owns the never-sanitized
  statement for every string on the type.
- `internal/protocol/interactive.go` → `SlashCommand`, `SlashCommand.MarshalJSON`,
  `SlashCommandListPayload` — the wire type. `MarshalJSON`'s doc states the wire's nil→`[]` normalisation,
  the 0-of-51 measurement, and hands the daemon-internal absent-vs-empty decision to this slice with
  `turnevent.ModelOption.EffortLevels` offered as a precedent to WEIGH.
- `internal/streamsup/parser_test.go` → `TestParser_InitializeControlResponseCountsTheCapturedCommands` —
  AC 5's subject: its guard witnesses "an undeclared key decodes rather than failing" with `aliases`.
- `internal/streamsup/parser_test.go` → `TestParser_SlashCommandFieldsAreCapped` — the boundary matrix for
  the three scalar caps, and where the alias rows land.
- `internal/streamsup/parser_test.go` → `TestParser_InitializeControlResponseCarriesTheCapturedArgumentHints`
  — #1958's capture pin, the shape the alias pin copies (guard the capture first, then compare index for
  index, then an exact report-set equality).
- `internal/streamsup/parser_test.go` → `commandEntryWithFixture`, `capturedCommandEntries`,
  `capturedCommandString`, `slashCommandNamePreview` — the fixture and capture helpers this slice reuses
  without widening.
- `docs/knowledge/features/streamsup-package-the-per-entry-byte-budget-s-third-dimension.md` — carries
  #1828's collapse argument AND the trap that decided it: `slices.Equal(nil, []string{})` reports `true`,
  so a design that tried to KEEP the distinction would carry a difference invisible to the comparison
  idiom every assertion on such a field already uses. That is this slice's sharpest test-design fact.
- `docs/knowledge/features/streamsup-package-producing-turnevent-slashcommandlist.md` — the emitter's own
  lessons: `emitSlashCommandList` logs nothing on any path; a `//`-claim sweep must work at the SENTENCE
  and by wording, not by grepping the new symbol's name.
- `internal/e2e/realclaude/testdata/initialize_control_v2.1.239.json` — the committed capture. Every
  measurement below was re-derived from it in this session rather than transcribed from the ticket.

## Context

`aliases` is the fourth and last key of the initialize reply's per-entry slash-command vocabulary. The
three scalar strings have landed (`name` #1877, `description` #1904, `argumentHint` #1957/#1958); this
slice decodes the fourth, bounds it under two constants of its own, and carries it onto
`turnevent.SlashCommand`.

It is not decoration. The desktop Actions menu offers **reset**, and `reset` is not a command name — it is
an alias of `clear`. A consumer matching menu entries against `Name` alone finds nothing for `reset` and
greys out a command that works. `protocol.SlashCommand` already declares the field and its wire tag, so
the wire position is settled; what is open is the daemon-internal one, and this decode decides it.

No ADR is warranted: this slice re-applies two decisions already recorded (ADR 036's aggregate-product
reading, and #1828's collapse) rather than making a new architectural one. The documentation phase folds
the lessons into `docs/knowledge/features/streamsup-package*.md`.

### Measurements (re-derived from the capture this session)

| Fact | Value |
|---|---|
| Entries | 51 |
| Entries carrying `aliases` | 9 |
| Entries omitting the key | 42 |
| Entries carrying an empty array | **0** |
| Distinct key sets in the capture | **2** (`{name,argumentHint,description}` and that plus `aliases`) |
| Aliases in total | 11, 64 bytes |
| Alias length | longest 9 (`proactive`), shortest 3, mean 5.82, median 5 |
| Aliases carrying non-ASCII | 0 |
| Per-carrier count | 7 carriers hold 1, 2 hold 2; max **2** |
| Per-entry count over all 51 | mean 0.216, median 0 |
| Carriers | `clear`→`reset`,`new`; `usage`→`cost`,`stats`; `code-review`→`review`; `doctor`→`checkup`; `loop`→`proactive`; `schedule`→`routines`; `config`→`settings`; `rename`→`name`; `list-agents`→`peers` |

Against the other three fields: names 494 bytes, hints 530, descriptions 10,580. Aliases are the cheapest
field by an order of magnitude.

**Measured counter-claim, because the obvious intuition is false.** An alias is not systematically shorter
than the name it aliases: 5 of the 11 are LONGER (`reset` for `clear`, `checkup` for `doctor`, `proactive`
for `loop`, `settings` for `config`, and `routines` ties `schedule`). So "aliases are short by nature" is
not available as an argument, and the population an alias is drawn from is the NAME population — which the
capture measures at a 24-byte longest (`__remote-workflow`). One captured alias (`name`, for `rename`) is
a token that could equally have been a command's own name. That measurement drives the byte cap below.

## Design

### 1. Absent-versus-empty: COLLAPSE, spelled `nil` (AC 2)

Decided at `turnevent.SlashCommand.Aliases`, with the 0-of-51 measurement stated there.

`encoding/json` already puts an absent key and a JSON `null` on one reading for a slice field — both leave
`commandEntryLine.Aliases` nil. What differs before the bound is a published `[]`, which arrives as an
empty non-nil slice. The bounding closure's zero-length arm normalises that to `nil` at construction, so
all three read as `nil` downstream.

Weighed and adopted rather than inherited, per `protocol.SlashCommand.MarshalJSON`'s instruction:

- **The distinction has never been observed.** Zero of the capture's 51 entries carry `[]`. Keeping it
  would preserve a difference between one observed shape and one that has never been sent.
- **The wire cannot express it anyway.** `protocol.SlashCommand.MarshalJSON` publishes `[]` for a nil
  `Aliases`, deliberately and for the consumer's benefit, so a daemon-internal distinction dies one hop
  downstream. Keeping it would be carrying a difference to a boundary that erases it.
- **The frequencies being inverted from `EffortLevels`' does not change the answer, and saying so is this
  slice's own obligation.** There the absence is the single exception (Haiku); here it is the majority (42
  of 51). What the frequency changes is how often the collapse FIRES, not what either shape MEANS: an
  entry with no aliases and an entry with the key absent issue the identical instruction to a consumer
  (*there is no alternative spelling to match against*), and no consumer behaviour branches on which of
  the two claude meant.
- **`nil` rather than `[]string{}` for #1828's reason verbatim:** `nil` is this struct's own spelling for
  an empty list — `TruncatedFields` uses it — and a struct whose two list fields disagreed on how to spell
  empty is worse than picking a direction once.

The collapse is what makes the WIRE's own guarantee cheap to keep: `MarshalJSON` has one shape to
normalise rather than two to reconcile.

### 2. Two constants

`maxSlashCommandAlias = 64` — caps ONE alias string.

- **Base named, per `maxSlashCommandArgumentHint`'s standing obligation, and here there are two.** Over
  the 11 observed aliases: 64 is 7.1x the longest (9), 11x the mean, 12.8x the median. Over the population
  an alias is DRAWN from — command names, per the counter-claim above — it is 2.67x the longest captured
  name (24). The second base is the one the derivation rests on; the first is stated so a reader
  re-deriving does not reach 7.1x and think this doc wrong.
- **Why not 256 (the name's own cap):** that cap's 10.7x is paid ONCE per entry. This one is paid up to
  `maxSlashCommandAliasCount` times, so copying it applies a singly-multiplied budget to a
  doubly-multiplied field. `maxSlashCommandDescription`'s multiplication argument, one dimension further
  down, and `maxModelEffortLevel`'s position in its own family.
- **Why not 32:** 32 is 3.6x the longest observed alias but only 1.33x the longest captured NAME, so the
  cap would sit inside the noise of the population aliases come from — `maxSlashCommandArgumentHint`'s
  WHY NOT 128 argument, with `__remote-workflow`'s 24 bytes as the committed evidence.
- **Why not 16:** it cuts a name-shaped alias outright.
- **Mid-rune is unreachable on the live path:** no captured alias carries non-ASCII and the longest is 9
  bytes, so a cut cannot occur at all, let alone inside a rune. Mid-rune is a constructed-fixture question
  here exactly as it was for the hint.

`maxSlashCommandAliasCount = 8` — caps how many aliases ONE entry retains.

- 4x the observed max of 2. A power of two, matching the family.
- **A THICKER multiple than the family's cardinality convention (1.6x–1.67x), deliberately, on two
  arguments the existing cardinality caps do not have.** First, those bound vocabularies CLAUDE controls —
  a published effort menu, a published model list — while this bounds a list a WORKSPACE author writes in
  a repository, so its growth direction is not bounded by anything claude ships. Second, and this is
  `maxSlashCommandDescription`'s ROLE argument pointing the other way: a cut description costs a truncated
  row, where a cut ALIAS costs a working command greyed out in the consumer's menu — the exact failure
  this feature exists to prevent. The observation is also small enough (2) that the thin multiple would
  land at 3 or 4, which `maxModelListEntries`' NOT 8 paragraph rejects by name at its own scale: a cap
  firing on ordinary output. A repository giving a command `r`, `rev`, `review` and `code-rev` is
  unremarkable.
- **Why not 16:** the product doubles for headroom nothing in the observation asks for, on the field with
  the smallest observed footprint in the entry.
- **The name ends in `Count`,** `maxModelEffortLevelCount`'s paragraph verbatim: the plural
  `maxSlashCommandAliases` would differ by one character, both are `int`, both bound the same field, and a
  swap at the call site compiles.

**The per-entry term becomes a sum plus a PRODUCT: 256 + 256 + 256 + 64 × 8 = 1280 bytes.** Stated once,
in `maxSlashCommandName`'s and `maxSlashCommandDescription`'s existing term paragraphs, which is where the
other three terms are stated. `maxSlashCommandDescription`'s fraction table moves with it: at 1280,
16384 × 1/2 = 8192 leaves **6** entries and 16384 × 5/8 = 10240 leaves **8**, replacing 10 and 13. #1826
picks the fraction and the count from the new pair.

**The fourth term was not chosen to make the sum come out round,** which
`maxSlashCommandDescription`'s doc asks explicitly. 1280 is where 64 and 8 land; the round candidate is
1024, reachable at a count cap of 4, and that candidate was rejected on the ROLE argument above rather
than on the sum. Recorded so a later reader can see the round number was available and declined.

**What the observed capture costs at these caps**, computed through `truncateField` as
`maxSlashCommandDescription`'s doc computes its own: all four fields over all 51 entries retain
6,647 + 64 = **6,711** bytes, under 8192 and under 10240, so today's real workspace still fits inside both
established fractions. No alias is cut at 64 and no entry exceeds 8, so the field contributes its full
observed 64 bytes.

### 3. The bounding closure

`bound` stays as it is. A sibling closure is added, declared INSIDE the per-entry loop for `cut`'s reason,
with `boundEach`'s four properties in the order the statements run:

```
boundAliases(values []string, name string, limit int) []string
```

- zero-length returns `nil` and appends nothing, BEFORE everything below — the collapse, and a zero-length
  list is neither counted against the count cap nor named in the report;
- the COUNT bound then runs, truncating FROM THE TAIL so claude's order survives, with a `>` boundary
  (`>=` would name a field nothing happened to);
- every SURVIVING element goes through `truncateField` into a slice of the same length, so an element cut
  neither drops it nor disturbs claude's order;
- the report name is appended AT MOST ONCE, after the loop, if the list was shortened or any element was
  cut or both.

**Named `boundAliases` rather than `boundEach`.** The model array's list closure is already `boundEach`,
in the same file and the same package; a second closure of that name makes every by-symbol citation to
either one ambiguous, which is the rot `make cite-guard` cannot see. It has one call site and one field,
so naming it for the field costs nothing. The `(values, name, limit)` signature is KEPT despite that,
because the report name at the CALL SITE is what makes declaration order visible in the call sequence —
the property `TruncatedFields`' exact-equality assertions rest on. The count constant is read from package
scope rather than passed as a fourth int, `boundEach`'s stated reason: a fourth int argument puts the two
caps adjacent at the call site, which is the swap the `Count` suffix spends itself preventing.

Call sequence, in declaration order — the alias call is APPENDED after the three, which is its correct
slot on both `turnevent.SlashCommand` and `protocol.SlashCommand`:

```
name        := bound(entry.Name, "name", maxSlashCommandName)
hint        := bound(entry.ArgumentHint, "argument_hint", maxSlashCommandArgumentHint)
description := bound(entry.Description, "description", maxSlashCommandDescription)
aliases     := boundAliases(entry.Aliases, "aliases", maxSlashCommandAlias)
```

`"aliases"` is the DAEMON's name and coincides byte-for-byte with claude's key and with the wire name
`protocol.SlashCommand.TruncatedFields` documents — unlike `argument_hint`, which is the field that made
that distinction live.

### 4. Types

- `commandEntryLine` gains `Aliases []string \`json:"aliases"\`` — fourth and last, completing the struct.
- `turnevent.SlashCommand` gains `Aliases []string` BETWEEN `Description` and `TruncatedFields`, the slot
  its own doc reserved.
- `protocol.SlashCommand` is UNCHANGED. It already declares the field and its tag; this slice touches no
  wire type, no frame and no client.

### 5. No log change

`logControlResponse` keeps its six attributes and gains no seventh. `emitSlashCommandList` logs nothing on
any path and the record is written by `emitModelList` BEFORE the emitter is called, so a dropped-alias
count would have to be threaded back out of a void-returning emitter with two call sites. The report
channel this AC asks for is `TruncatedFields`, which is per entry and already exists. The `levels_dropped`
precedent argues for a count only where the magnitude reaches nowhere else AND the record is the emit's
only observable; here the record predates the construction entirely. Deliberate non-change, recorded so it
reads as a decision.

### 6. Doc claims this slice falsifies

Each is edited in the same commit as the code; none is renumbered in place. Swept by WORDING and by
sentence, not by grepping the new symbol's name — the emitter's own recorded lesson.

- `commandEntryLine` — "THREE FIELDS OF FOUR, AND THE OMISSION IS STILL DELIBERATE" becomes the complete
  set; the memory arithmetic (81.3% / 18.7%) is re-derived; the sink enumeration gains an alias re-walk
  performed on its own rather than inheriting the three strings' answer; the null carve-out paragraph gains
  a fourth position that is a SLICE and not a string, so its reading is `nil` and not `""`; the "Contrast
  `aliases` (#1825)" sentence resolves to what was decided.
- `maxSlashCommandName` — "ONE field slice remains outstanding — aliases (#1825)" resolves; the per-entry
  term becomes a sum plus a product.
- `maxSlashCommandDescription` — the term, the fraction table (10/13 → 6/8), and the roundness paragraph's
  forward reference.
- `maxSlashCommandArgumentHint` — "#1825 inherits this convention with the problem worse" resolves.
- `logControlResponse` — "ONLY alias is UNREACHABLE by omission: it is not on the decode target" is now
  false. Redrawn, as that paragraph itself instructs: all four keys are declared, so all four are
  UNWRITTEN — kept out by what this function chooses to log rather than by what the decode target can hold
  — and there is no key left in the "unreachable by omission" category at all.
- `turnevent.SlashCommand.TruncatedFields` — "TODAY IT CAN CARRY name, argument_hint AND description"
  becomes four, and "A NAME FOR A FIELD THIS TYPE DOES NOT DECLARE MUST NEVER APPEAR" now has an empty
  extension, which is worth saying.
- `turnevent.SlashCommandList` — the four-way producer split paragraph.
- `protocol.SlashCommand.MarshalJSON` — "Whether the daemon-internal ALIAS value keeps the absent/empty
  distinction is #1825's call" resolves to the answer. Editing this ONE comment in `internal/protocol` is
  not a wire change.
- `parser_test.go` — `commandEntryWithFixture`'s "what #1825's aliases will";
  `TestParser_SlashCommandFieldsAreCapped`'s "ALL THREE per-field bounds";
  `TestParser_InitializeControlResponseCountsTheCapturedCommands`'s guard doc (AC 5).

## Concurrency model

None introduced. `emitSlashCommandList` runs on the parser's own single write-side goroutine, spawns
nothing, and holds no lock. The new closure is per-iteration, closes over the same per-entry `cut` slice
and a per-entry drop flag declared INSIDE the loop, and escapes nowhere — the scope is the whole of what
keeps one entry's report off the entries after it. The emitted `[]string` is freshly allocated per entry
and never aliases the decoded slice, so the event carries no reference into the decode buffer.

## Error handling

- **Non-string inside `aliases`, or `aliases` as a string / number / object** → the WHOLE-LINE decode
  fails and the line takes the undecodable rung. The rule is `commandEntryLine`'s and not any one field's;
  no branch is written for it. It repeats the transition the rung table already records: a key that was
  undeclared and silently ignored becomes a decode-failure source the moment it is declared.
- **`aliases: null`** → `encoding/json` sets a slice to nil with no error, so the entry is ORDINARY and
  counted, with `nil` aliases. This is the carve-out and NOT the undecodable rung; AC 4 groups the three
  shapes under "no panic and no value", and the shape-appropriate path differs between them. Both readings
  are pinned.
- **Oversized alias / oversized count** → cut at construction, reported once under `"aliases"`. Never a
  dropped ENTRY and never an error.
- No new error value, no new wrap, no panic path. Indexing is by `range` throughout.

## Testing strategy

`internal/streamsup` only. Every capture-backed test runs inside `make check` — the `e2e_realclaude` build
tag governs that package's Go FILES, not its testdata.

**A. `TestParser_InitializeControlResponseCarriesTheCapturedAliases`** (new; #1958's pin one field over,
and the AC 1 / AC 4-capture-half proof). Capture shape guarded FIRST, each guard a proof a re-capture
could silently take away, each literal written out rather than derived:

- 51 entries; exactly 9 carrying the key and 42 omitting it; **zero carrying an empty array**;
- exactly 2 distinct key sets, so the "no third key set" claim is the capture's own fact;
- 11 aliases in total, none over the byte cap, no entry over the count cap — which is what makes the
  empty expected report set below a MEASUREMENT rather than a coincidence, and where a changed CAPTURE is
  separated from a broken CUT;
- `clear`→`[reset new]` and `usage`→`[cost stats]` pinned BY NAME, `nameOutsideSlug`'s idiom: they are the
  only two-alias entries and the ORDER proof, and a re-capture that dropped one would otherwise leave the
  51 comparisons passing with nothing multi-element in them.

Then, index for index against the capture's own bytes: `slices.Equal` per entry for the carriers, the
exact nil report set for `"aliases"`, and `clear`/`usage` again by name.

**The vacuity trap, named because it is the one this design creates:** `slices.Equal(nil, []string{})`
reports `true`. So the 42 omitters' collapse to `nil` CANNOT be asserted with `slices.Equal` — that
assertion passes against a producer emitting `[]string{}` and the collapse would be untested. At least one
omitter is asserted with an explicit `got.Aliases != nil` check, and the matrix's runner compares with
`reflect.DeepEqual` (which separates the two) rather than `slices.Equal`.

**The empty-report-set vacuity risk** is #1958's, unchanged: an exact equality against a nil set also
passes against a producer that reports nothing at all. What makes it a measurement is the matrix's alias
LIVENESS row proving the report CAN fire under `"aliases"`.

**B. Rows in `TestParser_SlashCommandFieldsAreCapped`** (renamed in its doc from ALL THREE to ALL FOUR).
Scenarios, each with its own `why`:

- an alias over the byte cap is cut and reported; one that fits is not (LIVENESS; the second, uncut entry
  is the non-vacuity pin and the "a cut on one entry does not appear on the entries AFTER it" pin);
- an alias exactly at the byte cap is NOT truncated (the `<=` boundary operator; not sole-red, since
  `truncateField` is shared — sole-red for an INLINED alias cut written `<`);
- alias cut landing mid-rune, two-byte and four-byte (constructed, since no captured alias can reach that
  path);
- more aliases than the count cap: cut FROM THE TAIL, claude's order preserved among the survivors,
  reported once;
- exactly at the count cap: NOT reported (the `>` boundary — `>=` names a field nothing happened to);
- an over-long element AND an over-count list on ONE entry: `"aliases"` appears exactly ONCE (the
  append-inside-the-loop mistake);
- absent, `null` and `[]` all arrive as `nil`, are not reported, and are counted as entries (the collapse,
  with `reflect.DeepEqual` doing the nil-vs-empty separation);
- **all FOUR fields cut on one entry report `["name","argument_hint","description","aliases"]`** by exact
  equality — the whole declaration order in one slice, and the row that grades where the fourth `bound`
  call was placed. Four distinct fill bytes so a swap of any two VALUES reddens too.

**C. Rows in the reason table** (the undecodable rung): `aliases` is a string; an element of `aliases` is
a number. The fourth declared key taking the same rung as the other three, with no branch of its own.

**D. AC 5 — the falsified guard.**
`TestParser_InitializeControlResponseCountsTheCapturedCommands`'s `sawUndeclaredKey` witness is now false:
declaring the field makes `aliases` a DECLARED key, and the capture's two key sets leave no other key to
re-point it at. It is REPLACED, not deleted, by the capture-shape guard the new decode actually rests on —
9 carriers, 42 omitters, exactly two key sets — which is a stronger proof a re-capture could take away.
The tolerance claim the old guard carried ("an undeclared key decodes rather than failing") survives as a
CONSTRUCTED row: an entry carrying a key the daemon does not declare is tolerated. Moving the witness from
the capture to a fixture is the honest edit; deleting the claim outright would silently drop coverage this
slice did not earn the right to drop.

**Mutation checks to run** (claims of sole redness are empirical, per the emitter's own recorded lesson —
each is MEASURED before its `why` states it, via `go test -overlay`):

1. `boundAliases`' zero-length arm returning `[]string{}` instead of `nil` — must redden, and the check is
   that it reddens somewhere OTHER than a `slices.Equal` assertion.
2. the count bound's `>` → `>=` — must redden the exactly-at-the-count-cap row.
3. the report append moved INSIDE the element loop — must redden the both-dimensions row.
4. the `boundAliases` call moved before the description's — must redden the four-name order row.

## Open questions

1. **Is `64` right for the byte cap, or does the name-population argument over-reach?** Resolve by
   re-deriving both multiples against the capture during implementation and recording both bases in the
   constant's doc. If the name-population claim does not survive re-reading, 32 becomes the answer and the
   product halves — which moves the fraction table again.
2. **Does the count cap want the `Count` suffix or a different disambiguator?** Resolve at the constant's
   doc against `maxModelEffortLevelCount`'s naming paragraph.
3. **Does any test outside `internal/streamsup` construct a `turnevent.SlashCommand` in a way a fourth
   field breaks?** Checked during planning: the only construction site in production is
   `emitSlashCommandList`, and `cmd/pyry`'s two uses are a `turnMark` table row and a log-leak sentinel,
   neither of which reads the field set exhaustively. Re-confirm by building.

Each is resolved in Phase B and recorded under `## Revisions` if it changed the design.

## Security review

**Verdict:** PASS

**Findings:**

- **[Trust boundaries]** No MUST FIX. The boundary is explicit and single: subprocess stdout →
  `Parser.Write` → `controlResponseLine` → `commandEntryLine`, one decode target and one construction site
  (`emitSlashCommandList`). Aliases are WORKSPACE-authored — a lower-trust origin than claude, since a
  command defined in a repository was written by whoever wrote that repository — which
  `protocol.SlashCommand`'s SECURITY paragraph already credits for every string in `Aliases`.
  **SHOULD FIX:** the alias sink enumeration in `commandEntryLine`'s doc must be walked against the CALL
  GRAPH and not transcribed from the three strings' answer. The neighbouring enumeration credits
  `turnbridge.MapEvent`'s default with reach, and the package's own recorded lesson says code review found
  `SlashCommandList` never reaches `MapEvent` at all — it stops at `interactiveTurnEmitterV2.Handle`'s own
  default. Verify `MapEvent`'s call sites in Phase B before writing the alias's enumeration; do not copy
  the neighbouring clause.
- **[Tokens, secrets, credentials]** Not applicable, stated as a decision rather than an absence: an alias
  is an alternative spelling of a slash-command name, published by claude for a client to render and
  match against. No credential material, no entropy source, no lifecycle. Nothing in this slice reads,
  writes or derives a secret.
- **[File operations]** Not applicable. No alias reaches `filepath.Join`, `filepath.Match`, `os.Open` or
  any path construction; the decode reads an in-memory line buffer and the emitted value goes into an
  event struct. The argument is the EMPTY SINK SET and explicitly NOT "these look like identifiers" —
  `[`, `*` and `?` are syntax to `filepath.Match` and to `regexp`, and the capture's `__remote-workflow`
  is the committed proof that no charset may be assumed of anything in this vocabulary.
- **[Subprocess / external command execution]** No findings. No alias becomes an argv element, an
  environment variable or a `sh -c` fragment. The one inbound-shaped concern belongs to the NAME and is
  already answered on `protocol.SlashCommand`: a client sends a name back as ordinary MESSAGE TEXT on a
  path that does not treat it as a command vocabulary and does not consult this list. This slice publishes
  nothing and grants nothing; an alias is not sent back at all.
- **[Cryptographic primitives]** Not applicable. No randomness, no hashing, no key material, and no
  comparison of an attacker-controlled value against a secret — so no `crypto/subtle` obligation arises.
- **[Network & I/O — resource exhaustion]** No MUST FIX, two items:
  - **SHOULD FIX (ordering is load-bearing beyond its report semantics).** The COUNT bound must run BEFORE
    the per-element allocation. A single entry carrying a million aliases would otherwise
    `make([]string, 1_000_000)` and run `truncateField` a million times before anything was cut. The plan
    prescribes `boundEach`'s order already; Phase B must state in the code comment that the order bounds
    the ALLOCATION and not only which elements survive.
  - **SHOULD FIX (do not return the resliced input).** `boundAliases` must always copy into a freshly
    allocated `out`, even when every element fits. Returning `values[:n]` directly would make the emitted
    event retain the DECODER's backing array — up to a 4 MiB line's worth of string headers — for the
    event's lifetime. `boundEach` avoids this by construction; an "avoid the copy when nothing was cut"
    optimisation is the plausible wrong edit and must be refused by name in the comment.
  - The transient spike itself is unchanged in KIND: the cap is applied after `json.Unmarshal`, which is
    `maxTaskRosterEntries`' accepted family trade, bounded one level up by `defaultMaxParseBuf`'s 4 MiB
    whole-line cap. What DOES go stale is `commandEntryLine`'s "this struct is THREE fields where
    `modelOptionLine` is five" argument: the struct is four fields now, one of them a `[]string`, against
    `modelOptionLine`'s five including its own `[]string`. The conclusion survives (4 < 5, same densest
    element shape) but the argument must be re-stated with the new field count rather than left standing
    on a false premise.
- **[Error messages, logs, telemetry]** No MUST FIX. `emitSlashCommandList` logs nothing on any path and
  `logControlResponse` keeps six attributes, all daemon-computed integers carrying none of claude's bytes;
  this slice adds no seventh and therefore creates no site where an alias could be logged beside a count.
  **SHOULD FIX:** `cmd/pyry`'s `TestInteractiveTurnEmitterV2` pins the no-leak claim deterministically over
  `emitterSlashCommandListSentinels`. If that sentinel set is enumerated per FIELD, a fourth field without
  a fourth sentinel leaves the new field's leak unpinned while every existing assertion stays green — the
  vacuous-sweep shape this package has already been bitten by. Read the sentinel set in Phase B and extend
  it if it is field-enumerated. It is a test file, so it is outside the production file count.
- **[Concurrency]** No findings. No goroutine is spawned and none exists to leak; no lock is taken, so
  there is no ordering to document; the closure and its per-entry drop flag are declared INSIDE the loop,
  which is what keeps one entry's report and one entry's drop off the entries after it. The emitted slice
  is freshly allocated per entry (see the Network & I/O item), and the emitted strings are immutable Go
  strings the decoder allocated per field, so nothing is shared mutably with the parser's buffer.
- **[Availability — the cost of declaring a key]** Accepted, not a MUST FIX, and named rather than left to
  be discovered. Declaring `aliases` turns a shape claude could plausibly ship later — an object, or an
  array of objects — from a silently ignored unknown key into a WHOLE-LINE decode failure that also costs
  the model list on that line. That is `commandEntryLine`'s all-or-nothing rule, already accepted three
  times (#1853, #1904, #1957), and the rung table records the transition per key rather than hiding it.
  Refusing it here would mean a per-field tolerance design this slice has no mandate for, and would put
  the fourth key on a different rule from the other three.
- **[Threat model alignment]** `docs/protocol-mobile.md` § Security model is not engaged: this slice adds
  no frame, no wire type, no inbound verb and no client. The render-boundary sanitization obligation is
  declared and is the CLIENT's, on `protocol.SlashCommand`'s SECURITY paragraph. **OUT OF SCOPE:** the
  publish path and the client's render boundary are #1720's; the entry-count bound that would close the
  last unbounded dimension of this event — entries, which no cap covers today — is #1826's.

**Reviewer:** builder (self-review per the security-review checklist)
**Date:** 2026-09-02

## Revisions

### 2026-09-02 — Open questions resolved, and two test claims corrected by measurement

**Open question 1 (is 64 right for the byte cap?)** — resolved as 64, design unchanged. The
name-population argument survived re-reading and was strengthened by a measurement made during
implementation: five of the eleven captured aliases are LONGER than the name they alias, so "an alias is
a short form of the name" is false and the alias population cannot be its own base. Both bases are stated
on `maxSlashCommandAlias`.

**Open question 2 (the `Count` suffix)** — kept, on `maxModelEffortLevelCount`'s naming argument
verbatim. The one-character difference between the two constants is real and a swap at the call site
compiles.

**Open question 3 (external constructions)** — confirmed: `emitSlashCommandList` is the only production
construction site. `cmd/pyry`'s `emitterSlashCommandListFixture` needed a change, and it is the security
review's own SHOULD FIX rather than a surprise — `emitterSlashCommandListSentinels` is field-enumerated
by design and its doc says each field-adding slice owes it a sentinel. Every entry of that fixture now
carries at least one NON-EMPTY alias: an entry with an empty list contributes no needle at all and would
have been silently exempt from the leak sweep, which is a second under-sweep mode the scalar fields have
no analogue for.

**Two `why` claims in the boundary matrix were written as predictions and corrected by mutation**, per
the package's own recorded lesson that a sole-redness claim is empirical. Eight mutants were run through
`go test -overlay`:

- The **both-dimensions row** was predicted to be the sole red against the report being appended inside
  the per-element loop. It is the sole red for nothing measured. An unconditional in-loop append reddens
  the byte-liveness and count rows too; the faithful once-per-cut-element append reddens the COUNT row
  alone and leaves this row green; a second report raised inside the count block reddens this row and the
  count row together. The `why` now states the coverage it gives rather than a unique kill.
- The **at-cap byte row** was credited with catching a bound call that read `maxSlashCommandName` instead
  of `maxSlashCommandAlias`. Measured, that mutant leaves this row GREEN — its 64-byte input is untouched
  under a 256-byte cap, which is exactly what the row expects — and reddens the over-cap, both mid-rune
  and both multi-field rows instead. The `why` now says so, because the wrong guess is the plausible one.

Two claims were **confirmed** rather than corrected: the count-cap boundary row is the sole red for the
count bound's `>` becoming `>=`, and the four-name declaration-order row is the sole red for the alias
bound call moved before the description's. The count-liveness row picked up a measured claim it had not
been credited with: it is the sole red for both mutants that make a count cut SILENT.
