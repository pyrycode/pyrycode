# #1828 — Settle how an absent `supportedEffortLevels` reads against a published empty one

**Size:** s (re-counted against this spec — see § Size re-check)
**Decision:** **COLLAPSE, to `nil`.** An absent key, a JSON `null` and a published `[]` are ONE
reading, and the daemon spells that reading `nil`.

---

## Files to read first

Read these before writing anything. Every entry is a symbol, not a line — resolve with
`codegraph_search` / `codegraph_node`.

| File | Symbol | What to extract |
|---|---|---|
| `internal/turnevent/event.go` | `ModelOption` (the type doc's omissions paragraph) | The `supportsEffort` clause that says "completely once #1828 settles what an empty list means" — one of the eight deferral sites, and § Decision ¶6 closes it |
| `internal/turnevent/event.go` | `ModelOption.EffortLevels` (field doc) | The open paragraph this slice REPLACES — it is the third of six — plus the **five that stay**: the purpose/verbatim rule, the per-element cap and unbounded count (#1821), the cut-level warning, the `validEffort` separation, and the untrusted-text paragraph. Exactly one paragraph is replaced |
| `internal/turnevent/event.go` | `ModelOption.SupportsAutoMode` (field doc) | The collapse argument and its closing RE-DERIVE instruction — the forward reference § Decision must resolve, and the argument it must NOT cite as precedent |
| `internal/turnevent/event.go` | `ModelOption.TruncatedFields` (field doc) | "nil when nothing was cut, never an empty non-nil slice; `BackgroundTask.TruncatedFields` is the convention's single source" — the house spelling that decides the collapse's DIRECTION |
| `internal/streamsup/parser.go` | `modelOptionLine` (type doc) | The JSON-`null` carve-out paragraph — deferral site; and the `json` tags, which do not change |
| `internal/streamsup/parser.go` | `emitModelList` (function doc + body) | The doc paragraph asserting no normalisation happens below (deferral site), and rung 3, which is untouched |
| `internal/streamsup/parser.go` | `boundEach` (the closure inside `emitModelList`) | Its zero-length early return — the ONE production line this slice changes — and its three-bullet doc, whose first bullet becomes false |
| `internal/streamsup/parser_test.go` | `TestParser_ModelListEffortLevelsReadClaudesKey` | The five rows, the `wantWire` guard, and the doc's "do NOT go looking for a mutant" paragraph — AC 2 amends it |
| `internal/streamsup/parser_test.go` | `TestParser_InitializeControlResponseDecodesTheCapturedModels` | The `else if len(got.EffortLevels) != 0` arm and the comment above it explaining why it is weak — AC 4 tightens both |
| `internal/streamsup/parser_test.go` | `fixtureEntryRaw`, `modelEntryFixture`, `modelEntryWithFixture` | The fixture helpers the rows already use. AC 3 asks that the `wantWire` guard SURVIVE, not that it grow |
| `internal/protocol/interactive.go` | `ModelOption.MarshalJSON` | The wire's already-shipped nil→`[]` normalisation and its stated reason. **Read it; do not touch it** (#1822 owns its `#1690` deferral) |
| `docs/knowledge/features/streamsup-package.md` | § the `SupportsAutoMode` collapse paragraph and the two testing-lesson paragraphs after it | #1827's measured lesson that sole-redness claims in test docs are usually wrong until run. **Read-only — the documentation phase owns this file** |

Not in the reading list on purpose: `internal/e2e/internal/fakeclaude`'s `initializeModels` and
`internal/e2e/realclaude`'s `initControlSummarize`. Both read claude's **wire bytes** through literal
key strings, never `turnevent.ModelOption.EffortLevels`, so this slice cannot break them and must not
edit them. Their `#1690` deferral comments belong to #1822.

---

## Context

`turnevent.ModelOption.EffortLevels` shipped in #1827 with its **reading deliberately left open**.
The field decodes claude's `supportedEffortLevels` verbatim and bounds each level string at
construction, but the type's own doc says in capitals that whether an absent key means the same as a
published empty list "IS NOT SETTLED HERE," and eight comments across three files defer the question
to this ticket by number. Nothing publishes `ModelList` yet — `turnbridge` carries no `ModelList`
reference at all, verified against `main` at `21738ac3` — so the reading can still be chosen freely,
and this is the last slice at which that is true.

The question is real rather than theoretical: in Go a nil slice and an empty non-nil slice are
distinguishable at no cost, and `encoding/json` lands an absent key and a `null` on nil and a `[]` on
an empty non-nil slice. So a decode that cannot tell the two apart has made a choice. This slice
makes the choice explicitly and exercises it.

**This slice does not warrant an ADR.** The decision's natural home is the field's own doc, beside
the sibling decision it must be read against, and that is where the ACs put it. The documentation
phase folds the argument into `docs/knowledge/features/streamsup-package.md`, whose `#1828` sentence
becomes false in the same cycle.

---

## Design

### The decision

**An absent `supportedEffortLevels`, a JSON `null` and a published `[]` are ONE reading, and the
daemon spells it `nil`.** The producer normalises a zero-length level list to nil at construction.

Nothing else about the field changes: claude's elements and claude's order still arrive verbatim,
each level string is still capped by `maxModelEffortLevel`, `"effort_levels"` is still named at most
once per entry in `TruncatedFields`, and `internal/relay`'s `validEffort` is still not consulted.

### The argument

The ACs require this to be argued on the field's own terms — an empty MENU, not a withheld GRANT —
and require that `SupportsAutoMode`'s collapse be engaged with rather than cited. The six claims
below are the design content; the developer turns them into prose in the file's voice.

**1. The observed absence is not "claude declined to answer"; it is "this entry has no capability
block."** Re-measured against the committed capture `initialize_control_v2.1.239.json` during this
run: the two entries that omit `supportedEffortLevels` (`haiku`, `claude-haiku-4-5`) omit the
*entire* capability block with it — no `supportsAutoMode`, no `supportsEffort`, no
`supportsAdaptiveThinking` — and stop at `value`, `resolvedModel`, `displayName`, `description`. The
four that carry any capability key carry **all** of them and publish the full five levels. **There is
no observed entry that answers capability questions and declines this one.** That matters because the
only reading under which absent and empty could differ is *absent means UNKNOWN, `[]` means
AFFIRMATIVELY NONE* — and the shape a genuine "claude declined this one question" would take is
exactly the shape the capture does not contain.

**2. The distinguishing reading has no legal action behind it.** If absent meant UNKNOWN, a consumer
acting on that would offer a fallback menu. The daemon's only effort vocabulary is `internal/relay`'s
`validEffort`, a closed enum bounding a phone-supplied override on an INBOUND path — and
`ModelOption.EffortLevels`'s own doc already forbids applying it to this list, in either direction,
for two separate reasons it states. So the daemon holds no menu to fall back to and has decided it
never will. UNKNOWN and AFFIRMATIVELY-NONE therefore issue the identical instruction to every
consumer that can exist: **this list is not a menu you may offer.** Not because the two mean the same
thing in the abstract, but because the daemon holds no vocabulary in which they could differ.

**This is the empty-MENU argument, and it is not the bool's.** `SupportsAutoMode` collapses because
both readings drive the same client CONTROL (grey the option out) and the safe direction is
asymmetric — the unsafe inverse being to grant on silence. `EffortLevels` collapses for a different
reason: both readings leave the daemon holding the same EMPTY HAND, and the only way to fill it is to
invent a level vocabulary this file has already ruled out twice. A withheld grant is settled by
asking which direction is safe; an empty menu is settled by asking what there is to offer, and the
answer is nothing either way.

**3. The distinction has a lifetime of one call.** Nothing publishes `ModelList` yet;
`turnbridge.MapEvent` carries no `ModelList` case, so its `default` drops the variant until #1693.
The first and only consumer will map into `protocol.ModelOption`, whose `MarshalJSON` (#1704) already
normalises nil→`[]` and states the position at the type: an empty effort list on the wire is a
COLLAPSE, not a positive statement. So a distinction kept here would survive from `emitModelList`'s
construction to that copy and no further. Distinguishing at the daemon and collapsing at the bridge
is a legitimate shape — but it is only worth its cost when something between the two branches on the
difference, and here the two points are adjacent.

**4. A kept distinction is one the house comparison idiom cannot see.** `slices.Equal(nil, []string{})`
reports **true** — it compares lengths and elements, not nilness — and `slices.Equal` is the idiom
every existing assertion about this field uses. `reflect.DeepEqual` reports **false** on the same
pair. So under DISTINGUISH the daemon would carry a distinction that its own default assertion is
blind to and its second-most-common one is not: the next person to write `slices.Equal` against this
field silently drops it, and no test goes red. That is a trap, and it is a fact about **slices**
rather than anything inherited from the bool.

**5. The collapse is TO NIL because nil is this struct's own spelling for a zero-length list.**
`ModelOption.TruncatedFields`'s doc names the convention and its single source
(`BackgroundTask.TruncatedFields`): nil when there is nothing, never an empty non-nil slice.
Collapsing to `[]string{}` would leave two list fields of one struct disagreeing about how "nothing"
is spelled, and would allocate on the common path — the absent shape is a third of the capture's
entries. Collapsing to nil costs no allocation and normalises only the unobserved shape.

**6. What the collapse does NOT do.** It does not touch #1600's verbatim rule, which governs the
ELEMENTS and their ORDER — no lowercasing, no canonicalisation, no reordering. A list with zero
elements has exactly the elements claude sent, and normalising how Go spells *zero* changes no
element and no position. This is worth stating rather than leaving to be noticed, because
`EffortLevels`'s doc opens by calling itself VERBATIM and a reader will otherwise take the
normalisation as the first exception to it. The decision does close the `ModelOption` omissions
paragraph's dangling clause: with zero-length meaning one thing, a nil `EffortLevels` says exactly
what `supportsEffort: false`-or-absent would say, so `EffortLevels` subsumes `supportsEffort`
**completely** and it stays undecoded with the other three omissions. The capture's six entries show
the two keys co-varying perfectly (`supportsEffort: true` ⟺ five levels; both absent together), which
is evidence for the subsumption, not a guarantee of it — claude could publish them apart tomorrow,
and the omission would still hold because nothing consumes `supportsEffort`.

### Honest statement of what is being given up

The collapse is an active statement, not the absence of one: it publishes ONE reading of a
zero-length effort menu. Should claude ever start sending `[]` deliberately *and* mean something by
it that differs from omitting the key, this decode has discarded the evidence, and reopening costs a
new ticket plus a `*[]string` or a companion bool. That is accepted here for the reasons above, and
the spec says so rather than presenting the collapse as free.

### Package structure, data flow, concurrency

Unchanged in every respect. No new package, no new type, no new exported symbol, no signature change,
no goroutine, no channel, no lock, no shutdown-sequence participant. `emitModelList` runs on the
parser's existing single consuming goroutine and this slice adds nothing to it. The data flow is the
one #1827 shipped:

```
claude stdout line → consumeLine → emitModelList → modelOptionLine.EffortLevels
                                       → boundEach (per-element cap; NOW ALSO nil-normalises zero length)
                                       → turnevent.ModelOption.EffortLevels → p.emit(ModelList)
```

### Production change

**One line, in one place.** `boundEach`'s zero-length early return stops returning its input unchanged
and returns nil instead. That arm is deliberately the site rather than the `EffortLevels:` assignment
in the composite literal: `boundEach` is the function whose doc currently *asserts* the
no-normalisation rule, so the code and the comment that describes it stay adjacent, and the
`TruncatedFields` path is untouched because the arm returns before the loop that appends the name.

Everything else in this slice is comments and test assertions.

### The eight deferral sites

All eight were re-verified against `main` at `21738ac3` during this run; the grep for `1828` across
the three files returns exactly these and nothing else. **None may survive pointing at a closed
ticket** (AC 1's mechanical check). Cite by symbol when rewriting — `make cite-guard` is diff-scoped
and will fail the branch on a new `file.go:NNN`, at any depth, with no range exemption.

**What AC 1's check actually forbids is a DEFERRAL, not the string `1828`.** A rewritten comment may
name #1828 as the ticket that settled the reading — that is how #1704, #1819 and #1827 are cited all
through these files, and dropping the provenance would be worse than keeping it. What may not survive
is any sentence leaving the question open, owed, or pointing forward: "is not settled here", "is
#1828's", "must re-derive", "is OPEN", "declines to answer". Read every surviving `1828` occurrence
and check the verb tense, rather than grepping the number to zero.

| Symbol | What it currently says | What it must say |
|---|---|---|
| `ModelOption` type doc, omissions ¶ | `supportsEffort` is subsumed "completely once #1828 settles" | Subsumed completely, full stop — § Design ¶6 |
| `ModelOption.EffortLevels`, the open ¶ | "NOT SETTLED HERE… what that MEANS is open" | **Replaced** by the argument: § Design ¶¶1–6 plus the give-up paragraph |
| `ModelOption.SupportsAutoMode`, closing ¶ | "#1828 must RE-DERIVE it rather than inherit this one" | The re-derivation HAPPENED and landed on collapse too — **by a different argument**, which the paragraph must name (empty hand vs asymmetric safe direction) so a reader does not read it as the bool's precedent having been followed |
| `modelOptionLine` type doc, null carve-out | nil "without that being a claim that the two MEAN the same thing, which is #1828's to settle" | Null lands on nil, which is now the one reading — point at `ModelOption.EffortLevels` for the argument, and note the producer normalises `[]` onto it |
| `emitModelList` doc, level-list ¶ | reading is "OPEN (#1828)… nothing below turns an absent key's nil into an empty slice or a published empty array into nil" | **Becomes false** — state the settled reading and that `boundEach` implements it |
| `boundEach` doc, first bullet | "An empty input is returned UNCHANGED… nil stays nil and empty stays empty. That is the no-normalisation rule expressed as the absence of code" | **Becomes false** — the bullet now describes the normalisation, why nil is the direction (`TruncatedFields`' convention), and that it runs before the append loop so no name is emitted |
| `TestParser_InitializeControlResponseDecodesTheCapturedModels`, the weak-assertion comment | explains why zero-length is all both readings share | The reading is settled; the expectation is `nil` and the comment says why (AC 4) |
| `TestParser_ModelListEffortLevelsReadClaudesKey` doc | "declines to answer… do NOT go looking for a mutant that separates absent from published-empty" | **Inverted** — a separating mutant now exists and is named (AC 2, § Testing) |

Two other sites are **out of scope and must not be touched**: `protocol.ModelOption.MarshalJSON`'s
`#1690` sentence and `fakeclaude`'s `initializeModels` doc. #1822 lands after this slice precisely so
it can state one settled answer, and `docs/knowledge/features/streamsup-package.md`'s `#1828`
sentence belongs to the documentation phase.

---

## Error handling

No new failure mode, and no rung moves.

- **The undecodable rung is unchanged.** A `supportedEffortLevels` that is a string, a number or an
  object, or an array carrying a non-string element, still fails the WHOLE-LINE decode and takes
  `emitModelList`'s undecodable rung. The collapse operates on a value that has already decoded.
- **Rung 3 cannot be reached differently.** `emitModelList` gates on `len(entries) == 0` at the LIST
  level, before the per-entry loop `boundEach` lives in. Normalising an entry's level list changes no
  entry count, so no line moves between rungs and no line that emitted an event stops emitting one.
- **The cut report is unaffected.** `boundEach` returns from the zero-length arm before the loop that
  can append `"effort_levels"`, so a zero-length list still names nothing and `TruncatedFields` is
  still nil when nothing was cut.
- **Nothing new is logged.** `logControlResponse` remains the single record and still carries no
  payload-derived strings; this slice adds no log call on any path.

---

## Testing strategy

`make check` is the gate. All four ACs are exercised by tests that already exist; this slice tightens
what they assert rather than adding fixtures. **Do not add fixtures that exist** — the `absent`,
`published empty` and `present and null` rows are all already there with their `wantWire` guards.

### AC 2 — the decode table asserts the settled spelling

In `TestParser_ModelListEffortLevelsReadClaudesKey`, the three zero-length rows stop asserting
`len(got) != 0` and assert **`got == nil`**. The two published-list rows are unchanged. Scenarios, as
behaviour rather than code:

- `absent` (entry stops at the three mapped keys) → `EffortLevels` is nil.
- `published empty` (`[]` on the wire) → `EffortLevels` is nil, i.e. the producer normalised it.
- `present and null` → `EffortLevels` is nil.
- claude's five levels, in claude's order → unchanged, `slices.Equal` against the literal.
- a scrambled order is carried, not sorted → unchanged.

Keep the existing `switch` shape rather than folding the nil rows into the `slices.Equal` arm:
**`slices.Equal(nil, []string{})` is true**, so an equality-only assertion cannot see the thing this
AC exists to pin. The failure message should say `want nil` explicitly and name the wire value, since
"got `[]`, want `[]`" is what a naive `%q` prints for the row that actually fails.

### AC 2 — the mutation story, measured rather than asserted

The test doc's "do NOT go looking for a mutant" paragraph inverts. Two one-line mutants of
`boundEach`'s zero-length arm cover the three rows between them:

- **M1 — revert the decision** (return the input unchanged): the `published empty` row goes red; the
  `absent` and `present and null` rows stay green, because their input is already nil.
- **M2 — collapse the other way** (return `[]string{}`): the `absent` and `present and null` rows go
  red together; `published empty` stays green.

**The developer must RUN both and write down what actually reddened, package-wide, rather than
inherit these predictions.** #1827's own folded lesson is that two sole-redness claims in this exact
test's doc were plausible and wrong until measured. M2 is expected to redden the capture pin as well
(haiku's entry is absent), so it is **not** package-wide sole-red and the doc must not claim it is.
M1 is expected to be sole-red package-wide because no other fixture in the tree feeds a published
`[]` — expected, not established; verify before writing it down. Run mutants via
`go test -overlay=<abs-path json>` so the worktree is never written.

Two hazards from #1827's lesson, both live here: run the mutants under the same flags the gate uses
(`-race`), and keep the length `t.Fatalf` before any index into a decoded slice — a mutant that
empties the slice turns a clean FAIL into a process-wide panic that kills the parallel subtests
before they report.

### AC 3 — the wire guard must survive untouched

The `wantWire` assertion through `fixtureEntryRaw` stays exactly as it is, and this AC binds *harder*
after the collapse than before: with all three rows now decoding to the identical `nil`, the wire
assertion is **the only thing making them three rows rather than one proved three times**. A fixture
builder that quietly dropped the key would otherwise be invisible. Do not simplify, merge or
skip the guard, and do not merge the three rows on the grounds that they now share an expectation —
that merge is exactly what the guard exists to prevent, and it is the review finding if it appears.

Verify non-vacuity the way #1827 did for the bool: with the guard removed, a row whose `entry` drops
the key must still be caught. State in the doc that the guard is the row-distinctness pin, not
decoration.

### AC 4 — the capture pin names the shape

In `TestParser_InitializeControlResponseDecodesTheCapturedModels`, the
`else if len(got.EffortLevels) != 0` arm becomes an `else if got.EffortLevels != nil` arm, and the
comment above it stops explaining why the assertion is weak and states the settled reading instead.

**Note this expectation is the same under either branch of the decision** — the capture carries
absent and full-five and never a published `[]` — so the capture pin does not discriminate the two
readings and the decode table is what does. Say so in the comment rather than letting a reader think
the capture proved the collapse. The existing `sawFiveEffortLevels` / `sawEffortLevelsAbsent` coverage
guard above the loop is unchanged and still earns its place.

### Regression surface — what must stay green untouched

- `TestParser_ModelListFieldsAreCapped` — feeds over-long levels; every input is non-empty, so
  `boundEach`'s changed arm is never entered.
- `TestParser_ModelListSupportsAutoModeReadsClaudesKey` — the bool's table is not this slice's, and
  its "no separating mutant exists" paragraph stays TRUE and must not be edited by analogy.
- `internal/protocol`'s `ModelOption.MarshalJSON` tests — a different type; nil→`[]` on the wire is
  unaffected and its assertions on `o.EffortLevels != nil` are about `protocol.ModelOption`.

### The tier hazard, stated explicitly

`internal/e2e/realclaude` is behind the `e2e_realclaude` build tag, so **`make check` never compiles
it** and its green says nothing about that package. This slice's blast radius was checked during the
run and that package reads claude's wire bytes through literal key strings
(`initControlSummarize`, `effortLevels`), never `turnevent.ModelOption.EffortLevels` — so no change is
needed there. If the developer nonetheless ends up touching a file under `internal/e2e`, `make check`
is not the gate that proves it; run the package's offline tests with a `-run` filter, and read the
count of tests executed rather than the exit code.

---

## Open questions

- **None blocking.** The direction (nil, not `[]string{}`) is decided in § Design ¶5 on
  `TruncatedFields`' stated convention, and the site (`boundEach`'s zero-length arm, not the
  `EffortLevels:` assignment) in § Production change on comment adjacency.
- **Deferred by name, not open here:** the level COUNT stays unbounded (#1821, which needs both the
  count cap and the per-entry envelope); the `#1690` deferral comments in `protocol` and `fakeclaude`
  are #1822's; publishing the variant is #1693's.
- **Worth the documentation phase's attention:** `docs/knowledge/features/streamsup-package.md`
  currently states in prose that `EffortLevels` gets "*no* collapse at all" and that the question is
  #1828's. That paragraph is false after this slice and is the documentation phase's to fix — not the
  developer's.

---

## Size re-check (§ 4, against this written spec)

| Limit | Boundary | This spec |
|---|---|---|
| Production source files created or modified | ≤ 3 | **2** — `internal/turnevent/event.go`, `internal/streamsup/parser.go` |
| Total written work (production + tests + helpers + per-branch logs + spec edits) | ≤ 400 | **~200** — 1 production line, ~90 lines of comment rewrite across the two, ~55 in `parser_test.go`, no new helper, no new log call |
| New exported types or interfaces | ≤ 5 | **0** |
| Consumer call sites needing simultaneous update | ≤ 10 | **0** — no signature changes; the only Go reader of the decoded field is `internal/streamsup` itself |
| Acceptance criteria | ≤ 5 | **4** |
| Distinct error/reject branches in a state machine | ≤ 10 | **0 new** — no rung moves |

Within every boundary. No mechanical-edit rationalisation was applied to reach it: the raw counts are
what is written above.

**File-overlap check (§ 1.5):** run against `origin` after `git fetch --prune` for
`internal/turnevent/event.go`, `internal/streamsup/parser.go` and `internal/streamsup/parser_test.go`.
No remote `feature/<N>` branch touches any of the three. No block set.

---

## Security review

**Verdict:** PASS

**Findings:**

- **[Trust boundaries] No findings, and the boundary MOVES NOWHERE.** The subprocess→daemon boundary
  for this data is `emitModelList`, which is where it already was. The collapse operates on a value
  `encoding/json` has already produced from claude's bytes; it introduces no new parse site, no
  second decode, and no place where a level string is re-read. The downstream signal that the data is
  untrusted is unchanged and explicit: `ModelOption.EffortLevels`'s BOUNDED-AND-UTF-8-VALID-IS-ALL-
  THEY-ARE paragraph, which this slice must leave standing, states that nothing on this path strips
  control characters or terminal escapes and that the render boundary owing the sanitization is the
  CLIENT's. **MUST NOT** be dropped while rewriting the neighbouring paragraph — it and the
  CUT-LEVEL-IS-NOT-A-LEVEL warning sit either side of the paragraph being replaced, and the realistic
  developer error in a doc-heavy slice is collateral deletion. Called out in § Design's site table
  and repeated here because it is the one security-relevant text in the blast radius.
- **[Trust boundaries — the inbound/outbound confusion] No finding, but it is the live hazard in this
  ticket and the spec addresses it head-on.** `internal/relay`'s `validEffort` is a CLOSED enum over
  `{low, medium, high, xhigh, max}` plus `""` bounding a **phone-supplied** override on an INBOUND
  path. § Design ¶2 leans on that enum's existence to argue the collapse — and a developer following
  that reasoning could plausibly conclude the two rules should be unified. **They must not be**, in
  either direction: narrowing claude's outbound list through `validEffort` would silently drop a level
  claude adds next, making the daemon's published menu a lie; widening `validEffort` to whatever
  claude published would let the subprocess extend what an untrusted inbound frame may set — a
  subprocess authorizing itself. That is a privilege-escalation shape, not a style question. #1827
  already states the separation at `ModelOption.EffortLevels`; this slice must not contradict it, and
  § Design ¶2 is written to cite the enum as an argument for why the daemon has no menu of its own,
  never as a menu it could use.
- **[Error messages, logs, telemetry] No findings.** No log call is added on any path. `emitModelList`
  emits exactly one `logControlResponse` record per `control_response`, carrying four
  non-payload-derived attributes, and the collapse changes none of them: `models` counts emitted
  entries and `dropped` counts what `maxModelListEntries` cut, neither of which a per-entry level
  normalisation can move. The undecodable rung's deliberate omission of `err` — because
  `encoding/json` quotes offending input bytes into its error text — is untouched. The new test
  failure messages print `EffortLevels` values, which are test-authored fixture strings, not claude's.
- **[Network & I/O — resource exhaustion] No new finding; the known gap is unchanged and correctly
  scoped out.** The level COUNT is still unbounded (`maxModelEffortLevel` caps each level string at
  32, nothing caps N), so the retained per-entry budget stays `768 + 32N`. The collapse **cannot
  worsen** this: it only ever replaces a zero-length slice, so it removes a (tiny) allocation on the
  published-`[]` path and adds none. The gap is #1821's, which needs both a count cap and a
  re-derived per-entry envelope — named as deferred rather than silently inherited. Related and worth
  the developer's awareness: control-class events are retained in `eventring`, so a per-frame cap is
  also a per-conversation memory multiplier — another reason this slice must not be read as having
  bounded anything it did not.
- **[Subprocess / external command execution] Not applicable by design.** This slice executes nothing
  and passes no value to `exec.Command`. `EffortLevels` is a REPORT to a client's menu and never an
  authorization input; nothing in the daemon may branch on it to decide what it may SEND claude. The
  collapse does not create such a branch — it removes the only Go-level distinction a future branch
  could have keyed on, which is the safe direction for this category.
- **[File operations] Not applicable.** No path is constructed, no file is opened, created or written
  by this slice. The capture fixture is read by an existing test helper whose behaviour is unchanged.
- **[Tokens, secrets, credentials] Not applicable.** No credential, token or key is read, derived,
  stored or logged on this path.
- **[Cryptographic primitives] Not applicable.** No randomness, hashing, key derivation or comparison
  against a secret. `slices.Equal` is used on test-authored fixture data, not on attacker-controlled
  values compared to secrets, so constant-time comparison is not in question.
- **[Concurrency] No findings, and one latent alias is removed.** No goroutine, channel, lock or
  shared mutable state is added. `boundEach` is a closure over one loop iteration's `cut` slice and
  cannot be hoisted; the changed arm returns before touching it, and `emitModelList` runs on the
  parser's existing single consuming goroutine. Walking the category did surface one thing worth
  recording: the arm as it stands today returns its INPUT, so for a published `[]` the emitted event
  aliased the decode target's backing array — harmless in fact, because `encoding/json` builds that
  slice at capacity 0 so nothing can be read or appended through the alias without reallocating, and
  because the decode target dies with the call. Returning nil removes even the theoretical share.
  This is a side effect of the decision, not a reason for it, and must not be argued as one in the
  committed doc.
- **[Threat model alignment] No relay-facing surface.** Nothing publishes `ModelList` yet
  (`turnbridge.MapEvent` has no case for it), so no frame reaches a phone as a result of this slice
  and no mobile-protocol threat is engaged. The wire's position was already stated and argued at
  `protocol.ModelOption.MarshalJSON` (#1704) and is unaffected either way, which is why the scope
  boundary forbids touching it here. Publishing is #1693's, and it is the slice at which the mobile
  threat model applies to this data.
- **[Fail-safe direction of the decision itself] SHOULD FIX → addressed in the spec, noted for
  code-review.** The decision's own security posture is worth stating because a collapse discards
  information: the direction chosen is the conservative one. Under the collapse, a zero-length list
  reads as "not a menu you may offer" for absent, `null` and `[]` alike, so no path can be talked into
  offering effort levels claude did not publish. The unsafe inverse — treating an absent key as
  "unknown, so offer a default set" — would require the daemon to author an effort vocabulary, which
  is exactly what § Design ¶2 shows it has twice refused to do. Code review should confirm the
  committed doc argues this direction rather than merely asserting the collapse.

**Reviewer:** architect (self-review per the security-review checklist)
**Date:** 2026-08-27
