# 1877 — Bound each slash command's name and emit the command list on the model-list rung

## Files to read first

This is the turn-1 data load. Every entry names a **symbol**, never a line — resolve
each with `codegraph_search` / `codegraph_node`, and read the symbol's whole doc block
rather than grepping a phrase out of it. Three of the eight corrections below sit
inside paragraphs whose *surviving* sentences must be left verbatim, so a grep-sized
read is the failure mode this list exists to prevent.

| Where | Symbol | What to extract |
|---|---|---|
| `internal/streamsup/parser.go` | `emitModelList` | The four-rung classification, the placement of `commands := len(...)`, and where `logControlResponse` sits relative to `p.emit`. This is the only function whose body changes. |
| `internal/streamsup/parser.go` | `truncateField` | The cut-and-report helper you reuse unchanged: byte cut, `<=` boundary, scrub, `(string, bool)`. |
| `internal/streamsup/parser.go` | `commandEntryLine` | The decode target (`Name`, one field). Its doc block holds corrections **C3** and **C4** *and* one sentence that must stay verbatim. |
| `internal/streamsup/parser.go` | `controlResponseLine` | The nesting path to `Commands`, and why absent / null / empty are one reading. |
| `internal/streamsup/parser.go` | `maxModelResolved` | The doc shape your new constant copies: measurement, multiple, one-constant-per-meaning, per-entry term, transient-vs-retained. |
| `internal/streamsup/parser.go` | `maxModelEffortLevel` | The shorter end of the same shape — a per-element cap that delegates the aggregate elsewhere. Your constant is nearer this one in length. |
| `internal/streamsup/parser.go` | `logControlResponse` | Correction **C5**, and the sentence three lines from it that must stay verbatim. |
| `internal/streamsup/parser.go` | `emitModelAnnounced` | The one-field precedent: *no* `bound` closure, *no* sequential-statements rule, because one field decides no `TruncatedFields` order. Your loop follows this, not `emitModelList`'s models loop. |
| `internal/turnevent/event.go` | `SlashCommandList` | Corrections **C6** and **C7**; also the `IT IS PUBLISHED BY NO PATH TODAY` and `SECURITY` paragraphs, both of which survive. |
| `internal/turnevent/event.go` | `SlashCommand` | The two fields you construct, and `TruncatedFields`' nil-not-empty convention plus its `TODAY THE ONLY NAME IT CAN CARRY IS "name"` sentence (survives). |
| `cmd/pyry/interactive_turn_v2.go` | `eventKind` | Corrections **C8** (three clauses in the `ModelAnnounced` arm) and **C9** (two in the `SlashCommandList` arm). Read both arms whole. |
| `internal/protocol/interactive.go` | `SlashCommand` | Its `TruncatedFields` doc names the **wire** names — `"name"` is the one that matters here, and it coincides with the daemon's snake_case name. |
| `internal/protocol/interactive.go` | `SlashCommandListPayload.MarshalJSON` | The wire's `[]`-is-a-positive-statement position, which AC 3 weighs and does **not** inherit. |
| `internal/streamsup/parser_test.go` | `TestParser_InitializeControlResponseCountsTheCapturedCommands` | Keep-green update **T2**, and the comment inside it this change falsifies. |
| `internal/streamsup/parser_test.go` | `TestParser_InitializeControlResponseDecodesTheCapturedModels` | Keep-green update **T1**. |
| `internal/streamsup/parser_test.go` | `TestParser_ModelListIsLoggedContentFree` | Keep-green update **T3**; its `capturedCommand` leak sentinel is the record-unchanged pin and must stay green. |
| `internal/streamsup/parser_test.go` | `TestParser_InitializeControlResponseAckReportsTheCommandCount` | Read it, change **nothing**. It is AC 3's ack pin. |
| `internal/streamsup/parser_test.go` | `commandEntryFixture`, `initializeLineFixture` | The two builders your new over-cap test composes; no new fixture builder is needed. |
| `internal/streamsup/parser_test.go` | `TestParser_ModelListFieldsAreCapped` | The idiom for a cap test in this package — the shape **T4** follows, at two entries rather than its full matrix. |
| `docs/knowledge/features/streamsup-package.md` | the **"Decoding the initialize ack into `turnevent.ModelList` (#1811)"** block and the **"Reading the `initialize` ack's committed captures, in-package (#1810)"** block above it, both inside § `Turn I/O — envelope write + stdout parser (#1088)` | The accumulated lessons for this whole vertical — the caps, the per-entry `TruncatedFields` convention, the collapse decisions, and how the captures are read in-package. Read-only: the documentation phase owns this file. |

## Context

`emitModelList` decodes one top-level `control_response` line and classifies it onto
four rungs. Rung 4 — `subtype == "success"` **and** a non-empty decoded `models` array
— is the only rung that emits, and today it emits exactly one `turnevent.ModelList`.

Two halves of the slash-command path already exist and neither is a producer. #1853
landed the decode (`controlResponseLine.Commands`, `commandEntryLine`, and `commands`
as `logControlResponse`'s sixth attribute — a count, and nothing but the count leaves
the function). #1854 landed the value (`turnevent.SlashCommandList`,
`turnevent.SlashCommand`, `eventKind`'s arm, `turnMarkFor`'s totality row). **No
production code constructs the type. This slice is the producer.**

The three acts are one: the name is copied under its own byte cap, the entry is
constructed, and the list is emitted. Constructing without emitting leaves a value
nothing observes; emitting without the cap ships one merge window of unbounded
workspace-authored text retained in an event. Rung 4's emit acquires a second,
independent gate on the `commands` array; the models half is untouched.

**Producing is not publishing.** `turnbridge.MapEvent` gains no arm and
`interactiveTurnEmitterV2.Handle` gains no case — that is #1720's job, and #1720 is
open. `turnevent.SlashCommandList`'s `IT IS PUBLISHED BY NO PATH TODAY` paragraph and
`docs/protocol-mobile.md` § `slash_command_list`'s "nothing emits it yet (#1720)"
therefore both survive this slice. Verified this pass rather than inherited:
`internal/turnbridge` carries no `SlashCommandList` reference in production at all, and
`cmd/pyry`'s only two production references are `eventKind`'s arm and its neighbouring
comment.

**No ADR is warranted.** Every decision here is local to `emitModelList` and is
recorded in the symbols' own doc comments, which is where this vertical has kept its
reasoning across #1811 → #1853.

### Sizing — stated rather than absorbed

Measured, not projected. The eight `//` corrections were counted by reading the
paragraphs they live in; the constant and the emit were costed against #1811's own
`parser.go` breakdown for this file (constants 92 across two, `emitModelList` 150 for
the whole function).

| Item | Churn (ins+del) |
|---|---|
| Eight corrections across three files | ~200 |
| New cap constant + its doc | ~46 |
| The gate, the loop, the second emit + their doc | ~60 |
| Three keep-green test updates | ~70 |
| One over-cap test | ~65 |
| **Total** | **~440** |

Re-derived analogues in this same vertical, `ins+del`, excluding spec and knowledge
docs: #1828 207 · #1819 328 · #1812 377 · #1853 446 · #1827 614 · #1811 1050. The
shape-matched member is **#1827 (614)** — the nearest one adding a cap constant, a
construction, a report and `event.go` prose — and it did strictly *less*: it extended
an **existing** emit, constructed no new type, and touched no third file. Its test
surface (341) is what this ticket does not carry, #1878 having taken the matrix.

**~440 is over the 400-line boundary by ~10%, and there is no seam left.** The
partition was searched rather than asserted: a slice that reads and caps but does not
emit produces a value nothing observes; a slice that constructs and emits without the
cap ships unbounded workspace-authored text for a merge window; and all eight
falsified claims go false at the *same* instant — the moment a production producer
constructs and emits the type — so none of them can travel to a different child
without leaving a knowingly-false comment behind. This ticket is already child A of
#1875's split, whose seam was exactly "send the fixed prose cost with the change,
leave the proof matrix to #1878." Splitting a twice-split ticket with no remaining
seam does not make it smaller. The overage is recorded here so an operator can see it
and code review is not surprised by it; the mitigation is that the *edit-site* count
is low — one constant, one emit block, eight correction sites, four test sites — and
this spec pre-computes all fourteen so the developer's turns go to editing rather than
hunting.

## Design

### The new constant

Add one package-level constant to `internal/streamsup/parser.go`, beside the model
caps.

- **Name:** `maxSlashCommandName`. Follows `maxModelResolved`'s `max<Type><Field>`
  shape.
- **Value:** `256`.
- **Its own constant even though the number matches `maxModelResolved`,
  `maxModelField` and `maxModelValue`.** `maxRateLimitField`'s paragraph applies
  verbatim and `maxModelResolved`'s doc already states the rule: folding equal
  numbers together makes a future change to one budget silently move another.

The doc must carry, in `maxModelResolved`'s order:

1. **The measurement**, from the committed capture
   (`internal/e2e/realclaude/testdata/initialize_control_v2.1.239.json` and its two
   other responding arms, byte-identical): 51 entries, `name` present and non-empty on
   every one, longest **24 bytes** (`fewer-permission-prompts`), mean 9.69, median 8,
   494 bytes in total.
2. **The multiple:** 256 is ~10.7× the observation — the same multiple
   `maxModelField` and `maxModelResolved` take over an identifier-shaped string, and
   for their reason verbatim: room for a naming scheme claude has not shipped, still a
   hard cut on anything that has stopped being a name.
3. **The charset non-assumption.** One captured name is outside `[a-z0-9-]`
   (`__remote-workflow`), so the cap is a **byte** cut and nothing else — #1600's
   verbatim rule: no lowercasing, no trimming, no charset filtering, no `/` added or
   removed.
4. **The per-entry term, stated so later slices can add to it:** today one entry costs
   at most `maxSlashCommandName` = 256 bytes of workspace-derived text. **The
   aggregate is deliberately not settled here.** The field that will dominate the
   per-entry budget is the description (#1833), and 51 × 24 = 1224 bytes of name text
   decides nothing on its own; the entry-count factor arrives with the count bound
   (#1826), exactly as `maxModelListEntries` supplied `maxModelResolved`'s missing
   factor.
5. **Transient vs retained**, which is what makes the bound the whole story:
   `defaultMaxParseBuf` caps the whole line at 4 MiB before the decoder sees it and
   bounds the transient decoded array; what is **retained** is only the capped copy
   inside the emitted event, and nothing downstream retains *that* — see § What
   retains the emitted event.

### The gate, the construction, the emit

All of it appends to `emitModelList` **after** the existing `logControlResponse` call
and **after** the existing `p.emit(turnevent.ModelList{...})`. Nothing above moves.

```go
// … rung 4 unchanged, through logControlResponse and p.emit(turnevent.ModelList{…}).

if len(cr.Response.Response.Commands) == 0 {
    return
}
// one turnevent.SlashCommand per entry, in claude's own order — see § The loop
p.emit(turnevent.SlashCommandList{Commands: slashCommands})
```

That sketch fixes the three things that would otherwise be ambiguous — placement, emit
order, and the gate — and nothing else. The loop is described below rather than
written out.

**Emission order: `ModelList` first, then `SlashCommandList`.** Taken deliberately,
because it is observable in existing assertions. The models array is the rung's own
**discriminant** — rung 4 exists because of it, and `THE DISCRIMINANT` paragraph is
scoped to it — so the rung's defining emit goes first and the second, independently
gated emit follows. Gate order and statement order are then one order a reader checks
once. That the three captured-line tests keep `events[0]` as the `ModelList` is a
consequence, not the reason.

**The record does not move and does not change.** `logControlResponse` still runs once
per `control_response`, with the same six attributes, the same values, and the same
closed reason set — `model_list` on this rung. Placing the new block strictly after it
is what makes that structurally true rather than merely intended.

### The loop

Per entry of `cr.Response.Response.Commands`, in claude's own order:

- `truncateField(entry.Name, maxSlashCommandName)` → the bounded name and whether it
  cut. `truncateField` is reused unchanged; its `<=` boundary means a name of exactly
  the cap is not truncated.
- If it cut, `TruncatedFields` carries exactly `"name"` — the **daemon's** snake_case
  name, which for this field coincides with the wire name
  `protocol.SlashCommand.TruncatedFields` documents. If it did not cut,
  `TruncatedFields` is **nil**, never an empty non-nil slice
  (`BackgroundTask.TruncatedFields` is the convention's single source).
- The report is **per entry**: a cut on one entry must not appear on the entries after
  it. Declare the report slice inside the loop, exactly as `emitModelList`'s models
  loop declares `cut` and `droppedLevels` inside its own — that scoping is the whole
  of what makes it correct.

**No `bound` closure and no sequential-statements rule.** `emitModelAnnounced`'s doc
states the reason and it holds here verbatim: one field means there is no
`TruncatedFields` **order** for a composite literal to decide, and deciding that order
is the only thing the closure and the rule protect. Do not import
`emitModelList`'s models-loop machinery for a one-field entry.

**No entry-count cap and no `DroppedCommands`.** Deliberate, and #1826's — see § What
is deliberately out of scope. The emitted entry count therefore *equals* the decoded
count, which is why `logControlResponse` needs no seventh attribute.

**Name collision to avoid.** `commands` is already taken in this function: it is the
`int` `logControlResponse` reports. The built slice needs a different name
(`slashCommands` reads correctly against the type it carries). Reusing `commands`
would shadow the count the record depends on.

### AC 3 — the empty-list gate is this slice's decision, and it suppresses

`turnevent.SlashCommandList.Commands`' doc hands the decision to the producer
explicitly. **This slice's answer: an empty or absent `commands` emits nothing.**

The decisive argument is *not* rung 3's false-negative asymmetry, and the spec says so
because inheriting the wrong argument is how this decision gets re-opened later.
Rung 3's asymmetry rests on both outcomes being observable to a client (#1849 made
them so for models); nothing publishes this frame today (#1720), so neither outcome is
observable and that footing is unavailable.

What decides it instead is that **the producer cannot make the statement an empty emit
would be making.** `protocol.SlashCommandListPayload.MarshalJSON` states that a wire
`[]` is a *positive* statement — claude offered nothing. But
`controlResponseLine.Commands` is a plain slice, chosen so that an absent key, a JSON
null and a published `[]` are answered identically, and `commandEntryLine`'s doc fixes
that reading. So by the time `emitModelList` could gate, the decode has already
discarded the distinction: the daemon has no way to tell "claude offered nothing" from
"claude said nothing about commands." Emitting an empty list would assert the first
from evidence that cannot distinguish it from the second — inventing a distinction
rather than reporting one. Suppression is the only answer the decode supports.

Two consequences to record with the gate:

- The wire's `[]` position is untouched by this. It governs how a list that **was**
  emitted serialises, which says nothing about whether to emit one — and #1720, which
  owns the publish, still reads it for that.
- `Commands`' spelling of the empty case (`nil`, never an empty non-nil slice) is
  fixed independently of the gate and stays fixed. It is unreachable through this
  producer, which is exactly what the gate guarantees.

### What retains the emitted event

Nothing does, and this is the bound the security review turns on. After `p.emit`:

- `turnbridge.MapEvent` has no arm, so its `default` returns `("", nil, false)` and
  drops the value.
- `interactiveTurnEmitterV2.Handle` has no case, so the event is logged **by kind**
  (`eventKind` returns the variant name only) and discarded. No eventring append is
  reached.
- There is no `sessionModelHold` analogue: unlike the models array, nothing holds this
  for the child's life.

So the retention is the event's own lifetime — and that lifetime is bounded too:
`startStreamTurnDrainV2`'s channel is fixed-cap and buffered with a droppable reserve,
so events cannot accumulate behind a slow consumer.

The exposure is bounded by `defaultMaxParseBuf` on the way in and by
`maxSlashCommandName` per field on the way out. The **magnitude** of the copy is worked
out in § Security review under resource exhaustion; the short version is that it adds
no new order of magnitude over the transient spike `maxModelResolved`'s doc already
accepts. That analysis belongs in this spec, **not** transcribed into the constant's
doc — point 5 of § The new constant is the whole of what the code comment owes.

## The eight corrections

Each is a `//` claim that stops being true the moment this change lands, and each
moves **with** the code in the same commit. This is the ticket's dominant cost and it
is fixed. Alongside each, the sentences in the *same* block that stay true — an
over-correction is a defect code review has to catch, and three of these paragraphs
interleave true and false sentences.

### `internal/streamsup/parser.go`

**C1 — `emitModelList`'s heading sentence.** "emits AT MOST ONE `turnevent.ModelList`"
is now half the story. It must say: at most one `ModelList` and, on that same rung and
under a second independent gate, at most one `turnevent.SlashCommandList`. The
neighbouring "never emits an `Unrecognized`" and "returns nothing" clauses stay true.

**C2 — `emitModelList`'s rung 4 line** in the `FOUR RUNGS` enumeration. "success and a
non-empty array → one ModelList" goes false and must be corrected **in place**, naming
both gates and which array each reads.

> **Stays verbatim in the same doc:** the `FOUR RUNGS` enumeration's *shape*. Four
> rungs, unchanged; rung 4 merely does more. Restating it as a two-array lattice is
> #1876's job. **`THE DISCRIMINANT`** and **`WHERE A MIS-READ INVENTORY NOW GOES`**
> also stay verbatim — both are scoped to the models array, the models gate is still
> conjunctive and untouched, and a mis-read models inventory still goes exactly where
> those cites say. Document the new gate **at the new emit**, not by rewriting either
> paragraph.

**C3 — `commandEntryLine`'s `NO PER-FIELD CAP` paragraph.** The most directly
falsified claim in the tree: the headline act of this slice is adding exactly a
per-field cap. Its `defaultMaxParseBuf` half stays true — that constant still bounds
the transient decoded array. What narrows is the justification, the
transient/retained contrast with the models array: the decoded slice is still
transient, but a **bounded copy is now retained in the emitted event** for that
event's lifetime, and the sentence contrasting this array's pure transience with the
models array's session-lifetime retention has to say so. The one-field-vs-five
worst-case-transient arithmetic is unaffected and stays.

**C4 — `commandEntryLine`'s `EVERY STRING HERE IS WORKSPACE-AUTHORED` paragraph.**
"Only its COUNT leaves `emitModelList`" is false: a byte-capped **copy** of `Name` now
leaves it inside a `turnevent.SlashCommand`. The paragraph's open question — "The
first slice that actually READS Name inherits the validation question OPEN, not
settled, and several of those sinks treat bytes as syntax with no shell anywhere in
sight" — must be **answered on the record**, not deleted:

- **Validation stays refused**, and the reason is still NO SINK — but now a *checked*
  no-sink rather than a structural impossibility, and the check is what the correction
  records. After this slice `Name` reaches: one field of a daemon-internal struct, the
  parser's emit callback, and from there `turnbridge.MapEvent`'s `default` (dropped)
  and `interactiveTurnEmitterV2.Handle`'s default (logged **by kind**, discarded). It
  reaches no `exec.Command` argument, no `filepath.Join`, no `filepath.Match`, no
  `regexp`, no log attribute, no eventring append, and no wire frame.
- The syntax-sink concern is named and answered rather than dropped: `[`, `*` and `?`
  are syntax to `filepath.Match` and `regexp` with no shell present, which is why the
  answer has to be an enumeration of *reached* sinks rather than "the bytes look
  safe."
- **The re-open trigger is named:** the first slice that gives this value a syntax
  sink — a path element, a match pattern, a regexp, an argv element, a log attribute —
  or that renders it into an HTML sink, an attribute or a URL, inherits the question
  open again. #1720 is the nearest such slice, and what it owes is the *client*-side
  render boundary `turnevent.SlashCommandList`'s `SECURITY` paragraph already assigns.

> **Stays verbatim in the same doc block:** "**A later reader must not 'complete' this
> struct.**" This slice reads the already-declared `Name` and declares no new field, so
> the absolutism is untouched; the widening question belongs to #1833 / #1830 / #1825.
> It sits immediately beside C3 and C4, which is precisely where an over-correction
> would happen.

**C5 — `logControlResponse`'s `commands` paragraph.** Two clauses acquire exceptions
and must be corrected without disturbing the third:

- "no wire field to complete, no event, no retention and no daemon-internal value" —
  the *wire field* half stays true (#1826 still owns `DroppedCommands`); there is now
  an event, a daemon-internal value, and retention for that event's lifetime.
- "nothing caps or retains this one" — a per-**field** cap now exists. State the
  consequence that keeps the attribute unchanged: because there is **no count cap**,
  the decoded count still equals the emitted count, so `commands` remains an
  unambiguous decode count with nothing to disambiguate, and **no seventh attribute is
  added**. Nothing becomes unobservable either — on the `model_list` rung a non-empty
  `commands` now always means emitted, and on the ack rung always not, so the existing
  six attributes already separate the two.

> **Stays verbatim:** the name-in-the-log sentence — "No name, no argumentHint, no
> description and no alias reaches this record on any rung, and `commandEntryLine`'s
> single field is what makes **three of those four** unreachable rather than **merely
> unwritten**." It already excludes `name` from the unreachable set, and already says
> keeping it out is a decision rather than an impossibility. It is three sentences from
> C5 and is **not** part of it.

### `internal/turnevent/event.go`

**C6 — `SlashCommandList`'s `DECLARED AHEAD OF ITS PRODUCER` paragraph.** Wrong four
ways after this slice. The corrected paragraph must: keep the sequencing observation
(the type was declared ahead of its producer — that is history and stays true); drop
the present-tense "Nothing in the tree constructs this type"; and re-point the
attributions correctly, because getting this wrong ships a fresh false claim in place
of a stale one —

| Job | Owner |
|---|---|
| the decode | #1853, already in the tree |
| the byte cap, the construction, the emit | **this ticket** |
| the entry-count bound (and `DroppedCommands` with it) | #1826 |
| the publish — `turnbridge.MapEvent` arm + `Handle` case | #1720, open |

`#1719` is **closed** and was the decode; it must stop being named as a future
producer.

> **Stays verbatim:** **`IT IS PUBLISHED BY NO PATH TODAY`**, verified this pass and
> not inherited — `turnbridge` has no arm for the variant in production, and `Handle`
> has no case. Constructing and emitting a daemon-internal value is not publishing a
> wire frame. The `SECURITY`, `NO DroppedCommands FIELD`, `THE COUNT IS WORKSPACE- AND
> VERSION-DEPENDENT` and `IT IS A REPORT, NEVER A CONTROL INPUT` paragraphs all stay
> too; the `NO DroppedCommands FIELD` one in particular already fixes the sequencing
> C6 restates, which is why this slice **must not** add that field.

**C7 — `SlashCommandList.Commands`' gate paragraph.** "here there is no producer, so a
claim about what reaches this field would have nothing behind it" — there is one now,
and AC 3 is its answer. The corrected paragraph records the producer's gate: the
producer suppresses the empty list, so nothing reaches this field with zero entries.

> **Stays true in the same paragraph:** the nil-not-empty spelling, and the sentence
> naming `protocol.SlashCommandListPayload.MarshalJSON`'s wire position as what a
> producer slice *reads*. Reading it is exactly what § AC 3 above did; the paragraph
> should reflect that the wire's position was weighed and did not govern, rather than
> deleting the pointer.

### `cmd/pyry/interactive_turn_v2.go`

Read both `eventKind` arms **whole**. True and false sentences are interleaved inside
each, and the two clauses a developer misses — (a) and (b) — make their claim about
"the production producer" in general rather than about `SlashCommandList` by name, in
the arm a reader would not think to check.

**C8 — the `ModelAnnounced` arm**, three clauses:

- **(a)** "so this file's `interactive_turn.unknown` Debug is no longer a live call
  site for it — **nor for anything else the production producer emits**." The trailing
  clause is false: `internal/streamsup` is the production producer, it now emits
  `SlashCommandList`, and `Handle` has no case for it, so that Debug becomes a live
  call site for a variant the production producer emits. The clause about
  `ModelAnnounced` **itself** (claimed by a `Handle` case since #1638) stays true.
- **(b)** "**'No variant the production producer emits' is the accurate claim rather
  than 'unreachable'** — a nil Event still lands in that default." This sentence exists
  only to justify (a)'s phrasing, and after this slice neither formulation holds: the
  default is now reached by a production-emitted variant, not only by a nil Event. The
  surviving sentences around it have to be rewritten to stand without it rather than
  the sentence merely deleted.
- **(c)** "SlashCommandList, which nothing produces yet — #1719 / #1720 own that
  producer." Re-point per C6's table: this ticket produces it, #1720 owns the publish,
  #1719 is closed.

> **Stays true, three sentences from (a)–(c):** "**Handle now has an arm for 16 of
> `turnevent.Event`'s 18 implementations**", and the naming of the two without as
> `PermissionRequest` and `SlashCommandList`. `Handle`'s arm count is unchanged by this
> slice — that is #1720's change. It is **not** one of the falsified clauses.

**C9 — the `SlashCommandList` arm**, two clauses:

- **(d)** "#1854 declares it and **nothing in the tree produces it**." False:
  `internal/streamsup`'s `emitModelList` produces it as of this ticket.
- **(e)** "**No production producer emits the variant yet, so no production path
  reaches any of those sites today.** The arm lands with the declaration anyway…"
  Load-bearing rather than cosmetic. That arm enumerates which drop sites are
  live-but-unreached for this variant — `interactive_turn.unknown`, the no-cursor
  drop, and `stream_turn_drain.go`'s sink-full droppable drop and not-active-session
  drop — and **this slice is what makes them reachable**. The correction must say so;
  the arm's justification for existing then becomes stronger, not weaker.

> **Stays true in the same arm:** "**NO Handle case claims this variant**" — verified,
> `Handle` gains no case here. And the not-reachable-for-this-variant list —
> `observe`'s unbound-session drop and `sinkFor`'s close drop, both gated on a
> non-`turnMarkNone` mark, and `emitMapped`'s unmapped drop, which nothing routes it to
> — stays accurate: `turnMarkFor` answers this variant `turnMarkNone` by construction,
> and with no `Handle` case the event never reaches `emitMapped`. The whole
> #833-posture paragraph about workspace-authored strings stays too.

## Error handling

No new error paths and no new failure modes. Every input shape is already classified:

- A `commands` that is a number, a string or an object; an element that is a bare
  string or a number; a `name` that is not a string → the **whole-line** decode fails
  and takes the existing undecodable rung (`commandEntryLine`'s all-or-nothing rule).
  Nothing new is emitted there.
- A null `commands` decodes to a nil slice and a null `name` to `""`, per
  `encoding/json`'s documented null no-op. A nil slice hits the new gate and emits
  nothing; an empty-string name becomes an entry with an empty `Name` and a nil
  `TruncatedFields` — absence is claude's to choose, and `emitModelList`'s existing "a
  per-entry field is never validated beyond its cap" rule governs unchanged.
- `truncateField` cannot fail; it returns a value and a bool. Its scrub can leave a cut
  value 1–3 bytes under the cap when the cut lands mid-rune, and a scrub **removal** is
  not reported as a truncation — that is its documented behaviour, inherited, not
  restated.
- Nothing here panics: the gate returns before any index, and the loop ranges.

## Concurrency model

Unchanged. No new goroutine, no new shared state, no lock. `Parser.emit` is the
existing synchronous callback and both emits happen on the same call, in the stated
order, before `emitModelList` returns. The parser holds no cross-object state for this
value — the built slice is local and is handed to the callback.

## Testing strategy

All of it runs inside `make check`: `internal/streamsup` carries no build tag and
`capturedInitializePayload` reads the committed captures as bytes.

**Do not write the full boundary matrix, the 51-name verbatim pin, the suppression
table or the record sweep here — they are #1878's.** What lands here is the keep-green
churn plus the bound's own liveness proof, so the cap is never unproven for a merge
window.

**T1 — `TestParser_InitializeControlResponseDecodesTheCapturedModels`.** Every
responding arm carries both arrays, so the captured line now produces **two** events.
Update the count assertion and keep the `ModelList` at index 0 per the stated order.
Everything else — the capture-shape guards, the per-entry byte-for-byte comparison,
`DroppedModels == 0` — stays.

**T2 — `TestParser_InitializeControlResponseCountsTheCapturedCommands`.** Same count
update, plus this ticket's own liveness assertion: `events[1]` is a
`turnevent.SlashCommandList` whose `Commands` length equals the capture's own array
length (read from the capture, never transcribed as `51`). Correct the comment reading
"nothing about the command inventory reaches an event", which this slice makes false.
**`wantAttrs` is untouched** — that is what the unchanged record buys, and it is the
expensive churn this slice avoids.

**T3 — `TestParser_ModelListIsLoggedContentFree`.** Only the captured line carries a
`commands` array, so the assertion moves from 2 events to 3 across the five written
lines. The `capturedCommand` leak sentinel sweep stays as it is and **must stay
green**: it is the record-unchanged pin on this path, and a bounded name reaching a log
attribute reddens it.

**T4 — the over-cap proof (new).** One test, one line, **two** entries in the same
`commands` array, built from the existing `commandEntryFixture` and
`initializeLineFixture`. Scenarios rather than code:

- an entry whose `name` is one byte over `maxSlashCommandName` → the emitted `Name` is
  exactly `maxSlashCommandName` bytes, and its `TruncatedFields` is exactly
  `["name"]`;
- a second entry in the same list, comfortably under the cap → its `Name` is verbatim
  and its `TruncatedFields` is **nil**, not an empty non-nil slice.

The second entry is what makes the row non-vacuous: without it, a mutant that names
`"name"` on every entry regardless of the cut stays green. Build the over-cap name
from the constant rather than from a literal length, so the row moves with the cap.
The `at exactly the cap` boundary row belongs to #1878 — `truncateField`'s `<=` is
already pinned at its own symbol.

**T5 — `TestParser_InitializeControlResponseAckReportsTheCommandCount` stays green
UNTOUCHED.** Both its rows assert zero events and neither carries `models`, so it is
exactly the ack case this slice defers, and it is AC 3's pin for the
commands-and-no-models case. **If it reddens, the ack rung was changed and the scope
boundary was crossed** — that is #1876's job, not this one's.

**T6 — the two `cmd/pyry` tests and the totality row stay green UNTOUCHED.**
`TestInteractiveTurnEmitterV2_SlashCommandListEventKindNamesTheVariant`,
`TestInteractiveTurnEmitterV2_SlashCommandListIsNotPublished` (with
`emitterSlashCommandListFixture` and its two shared helpers), and `turnMarkFor`'s
totality row in `cmd/pyry/stream_turn_busy_test.go`. `IsNotPublished` is a deliberate
tripwire whose own doc names the slice that gives `Handle` an arm as the one that will
redden it. **This is not that slice**; reddening it means #1720's scope was crossed.

**Citations.** `make cite-guard` runs inside `make check` and fails a line-number
citation added to a `//` comment, at any depth, with no range exemption. Every new or
corrected comment cites by **symbol**. This family's cites went stale twice in three
days as siblings merged.

## Open questions

- **The cap's value (256) is a recommendation, not a measurement of a constraint.**
  The capture's longest name is 24 bytes and the boundary is unreachable from the
  capture whatever number is chosen, so nothing in the tree can falsify 256 today. If
  the developer's read of `maxModelEffortLevel`'s tighter multiple (~5× its
  observation) argues for a smaller number, taking it is fine provided the doc states
  the multiple actually chosen and the derivation. What is **not** negotiable: it is
  its own constant, and it is a byte cut.
- **Nothing else.** The empty-list gate (AC 3), the emission order, the
  no-seventh-attribute decision and the validation answer are all settled above rather
  than deferred to implementation.

## What is deliberately out of scope

- **The ack rung is untouched.** A success payload carrying a non-empty `commands` and
  no `models` stays an ack emitting nothing. Splitting that rung, and widening
  `controlResponseMsg`'s closed reason set to name what was emitted across the whole
  two-array lattice, is **#1876**'s whole job — one keyword answering models-only,
  commands-only and both, decided once rather than half here.
- **The entry-count bound and `DroppedCommands` are #1826's**, and
  `turnevent.SlashCommandList`'s own doc fixes that sequencing. Do **not** add the
  field. Precedent is exact and in this same function: #1811 emitted
  `turnevent.ModelList` with no entry-count cap and #1812 added `maxModelListEntries`
  and `DroppedModels` in the next slice.
- **No wire frame, no protocol change, no client.** `turnbridge` gains no arm, `Handle`
  gains no case — #1720.
- **No `docs/` edit outside this spec.** `docs/protocol-mobile.md` is not touched (its
  § `slash_command_list` statements are about the frame #1720 owns), and
  `docs/knowledge/features/*.md` belongs to the pipeline's documentation phase.
- **`argumentHint`, `description` and `aliases` stay undeclared** on
  `commandEntryLine` — #1833 / #1830 / #1825.

## Security review

**Verdict:** PASS

**Findings:**

- **[Trust boundaries] No findings — the boundary is explicit and this slice narrows
  what crosses it.** Untrusted input is claude's `control_response` line, and the
  boundary is a single function, `emitModelList`, decoding top-level line bytes into
  `controlResponseLine`. Two properties hold and are stated in the spec rather than
  assumed: the decode's input is the **top-level** bytes and never a nested field
  (`streamLine`'s stated property, which is what stops a tool result whose text is
  literally a control shape from forging one), and `commandEntryLine` declares **one**
  field, so 96% of a 14,277-byte workspace-authored payload never becomes a Go string.
  Downstream holders know what they hold: `turnevent.SlashCommandList`'s `SECURITY`
  paragraph states that every string it carries is workspace-authored — a *lower*-trust
  origin than claude's own — that the daemon bounds but does not sanitize, and that the
  render boundary is the client's. That paragraph survives this slice verbatim.

- **[Trust boundaries] SHOULD FIX — the trust *level* of this data is stronger than
  the models array's, and the spec must not let the models precedent flatten it.**
  Workspace-authored means whoever wrote a repository, read in whatever directory the
  operator points a session at, and the arm is **live rather than latent**:
  `RequestInitializeOnSpawn` fires the ask once per spawned child and
  `mapStreamsupConfig` sets it true, so this runs against real workspaces immediately.
  Mitigated by design rather than gated on: the spec's C4 correction requires the
  validation answer to be an enumeration of *reached sinks* rather than a judgement
  about the bytes, and names the re-open trigger. Code review should check that the
  shipped C4 text enumerates sinks and does not settle the question by asserting the
  bytes are safe.

- **[Subprocess / external command execution] No findings — the sink enumeration was
  run, not assumed.** The bounded `Name` reaches no `exec.Command` argument, no
  `filepath.Join`, no `filepath.Match`, no `regexp` and no argv element. Verified this
  pass: `internal/turnbridge` carries no production reference to
  `turnevent.SlashCommandList`, and `cmd/pyry`'s only production references are
  `eventKind`'s arm and its neighbouring comment. `turnevent.SlashCommandList`'s
  `IT IS A REPORT, NEVER A CONTROL INPUT` paragraph states the standing rule — no field
  may reach a child as an argv element — and survives. The syntax-sink concern is
  real and is the reason the enumeration is the answer: `[`, `*` and `?` are syntax to
  `filepath.Match` and to `regexp` with no shell anywhere in sight, so "the bytes look
  like identifiers" would be the wrong argument. One captured name is already outside
  `[a-z0-9-]` (`__remote-workflow`), which is the committed proof that no charset may
  be assumed.

- **[Error messages, logs, telemetry] No findings — the record is unchanged and that
  is structurally enforced.** `logControlResponse` keeps exactly six attributes, the
  same values and the same closed reason set; the new block is placed strictly after
  it. No `name` reaches any record on any rung. The undecodable rung still does **not**
  log the unmarshal error, which is load-bearing here specifically: `encoding/json`
  quotes the offending input bytes into its error text, so `"err", err` on a line with
  a `commands` array would route workspace-authored names into the daemon log through
  a channel no per-attribute check can see. `eventKind` returns the variant name only,
  so the drop sites this slice makes reachable (C9) log a kind and nothing else — and
  `TestParser_ModelListIsLoggedContentFree`'s existing `capturedCommand` sentinel sweep
  is the deterministic proof, not a comment.

- **[Network & I/O — resource exhaustion] OUT OF SCOPE, with the exposure bounded
  meanwhile and the reasoning on the record.** This slice adds a per-**field** byte cap
  and **no entry-count cap**, so the emitted list carries however many entries claude
  sent — 51 in the capture, workspace- and version-dependent. The entry-count bound is
  **#1826**, which is transitively six tickets downstream (#1877 → #1878 → #1876 →
  #1833 → #1830 → #1825 → #1826), so the window is long and real. What bounds the
  exposure in the meantime: `defaultMaxParseBuf` caps the whole line at 4 MiB before
  the decoder sees it — already the entire bound on the transient decoded array today —
  and **nothing retains the emitted event**. `turnbridge.MapEvent`'s `default` returns
  `("", nil, false)` and drops it, `Handle` has no case so it is logged by kind and
  discarded, no eventring append is reached, and there is no `sessionModelHold`
  analogue holding it for the child's life. Retention is the event's own lifetime, and
  that lifetime is itself bounded: `startStreamTurnDrainV2`'s channel is a **fixed-cap
  buffered** channel with a droppable reserve, not an unbounded queue, so events cannot
  accumulate behind a slow consumer. Precedent is exact and in this same function:
  #1811 emitted `turnevent.ModelList` with no entry-count cap; #1812 added the cap and
  the drop report in the next slice.

- **[Network & I/O — resource exhaustion, magnitude] No findings, and this is the half
  "nothing retains it" does not answer.** The adversarial question is not only *how
  long* the copy lives but *how big* it can get, since without an entry-count cap the
  entry count is claude's to choose. The copy is bounded by `defaultMaxParseBuf`
  (4 MiB) on two independent terms. **Text:** each retained byte costs at least one
  input byte, and a name at the 256-byte cap costs ~270 input bytes, so the retained
  text is ≤ ~4 MB — amplification near 1, unlike the transient decode. **Struct
  overhead:** the densest legal element is `{}`, so the entry count peaks near 4 MiB /
  ~4 bytes, and at ~40 bytes per `turnevent.SlashCommand` (a string header plus a nil
  slice header) the copy peaks around 32 MB with every `Name` empty. Both terms are
  **below** the order-100 MB transient spike `maxModelResolved`'s doc already accepts
  for the models array on the same line, and the two cannot be maximised at once by one
  4 MiB line. So this slice adds no new order of magnitude, and `defaultMaxParseBuf`
  remains the whole of the bound until #1826.

- **[Network & I/O — envelope size] No findings, and deliberately no aggregate claim.**
  The per-entry term is stated (`maxSlashCommandName` = 256 bytes) so later field
  slices add to it and the count bound can multiply it, but the aggregate against the
  65519-byte v2 application-envelope cap is **not** settled here: the field that will
  dominate the per-entry budget is the description (#1833), and 51 × 24 = 1224 bytes of
  name text decides nothing on its own. Nothing crosses an envelope on this path today
  in any case — #1720 owns the frame, and it inherits the aggregate question with
  `maxModelResolved`'s arithmetic as the shape to copy.

- **[File operations] Not applicable — no path is constructed, opened, or matched.** The
  only file this ticket's tests read is the committed capture, through the existing
  `capturedInitializePayload` reader, on a path built from constants.

- **[Tokens, secrets, credentials] Not applicable — no token, secret or credential is
  generated, stored, compared or logged on this path.**

- **[Cryptographic primitives] Not applicable — no randomness and no comparison against
  a secret. The only comparisons are a length against a cap and a subtype against a
  constant.**

- **[Concurrency] No findings — no new goroutine, no new shared state, no lock.**
  `Parser.emit` is the existing synchronous callback; both emits happen on one call
  before `emitModelList` returns, and the built slice is local with no aliasing of the
  decoded array (`truncateField` returns a new string per entry). There is no
  check-then-mutate on shared state and nothing to leak at shutdown.

- **[Threat model alignment] No findings.** `docs/protocol-mobile.md` §
  `slash_command_list` states that nothing emits the frame yet (#1720) and that
  survives this slice, because producing a daemon-internal event is not publishing a
  wire frame. The client-side render obligation — inert text only, never an HTML sink,
  an attribute or a URL — is already assigned by
  `turnevent.SlashCommandList`'s `SECURITY` paragraph and by
  `protocol.SlashCommand`'s, both of which this slice leaves standing. Nothing here
  moves an obligation off the client or onto it.

**Reviewer:** architect (self-review per the security-review checklist)
**Date:** 2026-08-31
