# #1862 — re-point `streamsup` and `turnevent`'s "nothing publishes ModelList" claims

**Comments only.** No signature, no assertion, no behaviour change. Four files, thirteen edit
regions, zero production logic touched.

## Files to read first

Read these before editing anything. The first four are the edit surface; the rest are the ground
truth every rewritten sentence has to be checked against.

- `internal/turnevent/event.go` → `ModelList` (the type doc), `ModelOption.EffortLevels`,
  `ModelOption.SupportsAutoMode`, `ModelOption.TruncatedFields` — five of the thirteen regions.
  Read each doc comment **whole**; in every case the surviving conclusion is a neighbouring
  sentence to the false premise.
- `internal/streamsup/parser.go` → `maxModelResolved` (its final paragraph), `emitModelList`,
  `logControlResponse` — five regions. Same rule: read the whole comment.
- `internal/streamsup/parser_test.go` → the two subtests whose names are
  `"the record names both numbers"` and `"the record names how many levels were dropped"` — two
  regions, both comment-only.
- `internal/e2e/internal/fakeclaude/initialize_control_test.go` → the file's leading package
  comment — one region, one clause.
- `internal/turnbridge/outbound.go` → `MapEvent`, its `turnevent.ModelList` arm — **the arm exists
  and every field crosses 1:1**, `DroppedModels` carried and `TruncatedFields` crossed as the slice
  it is. This is what falsifies "MapEvent's default drops the variant".
- `cmd/pyry/interactive_turn_v2.go` → `interactiveTurnEmitterV2.Handle`'s `turnevent.ModelList`
  case, and `emitMapped` (the emit path, which calls the ring's `Append` before the per-conn
  fan-out). This is the live-lane delivery **and** the eventring append, in one place. Note the
  arm's own "TWO QUEUES, TWO ANSWERS" paragraph — the live send is **droppable** at the fan-in.
- `cmd/pyry/session_model_hold.go` → `sessionModelHold`, `sessionModelHold.Sink`,
  `sessionModelHold.ModelList` — the per-session retention (#1840), which sits **above** the
  droppable send.
- `cmd/pyry/session_model_list.go` → `resolveBoundModelList` — the retained list shaped into a
  `protocol.ModelListPayload`. **It has no production caller**; only tests call it. Do not describe
  it as a delivery path.
- `internal/relay/v2session_modelreconcile.go` → `reconcileModelLists`, and
  `internal/relay/v2session_seams.go` → `V2SessionConfig.RetainedModelLists` — the connect-time
  seam (#1863). `reconcileModelLists` returns immediately when `RetainedModelLists` is nil, and
  **nothing in the tree sets it**. This is what a comment must not describe as shipped.
- `internal/protocol/interactive.go` → `ModelListPayload`, `ModelListPayload.DroppedModels`,
  `ModelOption.MarshalJSON` — the wire's side. `MarshalJSON` normalises `EffortLevels` nil→`[]`
  and **deliberately exempts `TruncatedFields`**, so nothing-was-cut reaches the wire as `null`.
- `docs/knowledge/features/streamsup-package.md` § the `emitModelList` / retention passages — the
  package overview is already current on the shipped state (it names #1848's arm, #1849's `Handle`
  case and #1840's hold). Read it rather than re-deriving; **do not edit it**, the documentation
  phase owns it.
- The precedent: `git show d27ec837` (#1639) did this exact sweep for `model_announced`, and
  `git show c639ac8b` (#1861) did it three days ago for `internal/protocol`'s comments about *this
  very value*. #1861's diff is the house phrasing to match — past tense for the premise, present
  tense for what shipped, and an explicit statement of what is still outstanding.

## Context

`internal/streamsup` decodes claude's `initialize` reply into `turnevent.ModelList`. Both packages'
comments were written while three things were true: `turnbridge.MapEvent`'s `default` dropped the
variant, nothing retained the value, and no client could see it. All three went false between
2026-08-25 and 2026-08-26. Twelve `#1693` references and three numberless claims survive across
four files, and `#1693` is `CLOSED / NOT_PLANNED` — a split parent, not landed work.

**What shipped (all verified `CLOSED / COMPLETED` at `c639ac8b`):**

| Slice | What it ships | Where to see it |
|---|---|---|
| #1840 | Per-session retention of the decoded value | `sessionModelHold` |
| #1848 | The `turnevent.ModelList` → `protocol.ModelListPayload` mapping | `MapEvent`'s arm |
| #1849 | The emit on the live interactive turn lane, **and** the eventring append | `interactiveTurnEmitterV2.Handle`'s arm → `emitMapped` |
| #1845 | End-to-end proof it reaches a connected client | the realclaude tier |
| #1857 | The retained list shaped into a wire-ready payload | `resolveBoundModelList` |
| #1863 | The relay-side connect-time reconcile seam | `reconcileModelLists`, `V2SessionConfig.RetainedModelLists` |

**What has not shipped:** delivery to a client that connects *after* the `initialize` exchange.
#1857's resolver has no production caller and #1863's seam is nil in every production wiring; the
two are not joined. **#1867** (`OPEN`) is the daemon-side producer that fills the seam, **#1868**
(`OPEN`) proves it.

**This is why every rewritten sentence has to name a delivery path.** "Clients receive it" is as
wrong as "nothing publishes it" — the two paths have different answers.

**No ADR.** This is a correction to prose whose subject already has an ADR-free design record in
`docs/knowledge/features/streamsup-package.md`. Nothing here decides anything new.

## The three delivery facts every rewrite is checked against

State these precisely. Sloppy paraphrase is how the next stale claim gets written.

1. **Live lane, shipped.** A client interactive-connected at the moment claude answers `initialize`
   receives the frame: `MapEvent`'s arm shapes it (#1848) and `interactiveTurnEmitterV2.Handle`
   emits it (#1849). It is **best-effort** — `turnMarkFor` answers `turnMarkNone`, so the fan-in
   classes the event droppable and can refuse it at `droppableCap` under load, as that arm's own
   doc says. So "a client receives it" is true of the path, not guaranteed of any given exchange.
2. **Retention, shipped, two of them.** `sessionModelHold` holds the decoded value for the
   session's life, replaced on respawn, above the droppable send (#1840). `emitMapped`'s
   `ring.Append` holds the **mapped payload** per conversation (#1849), which is the replay source
   for a phone that reconnects.
3. **Connect-time delivery, not shipped.** A client that connects after the exchange gets nothing
   today, by any path. #1867 is outstanding. Eventring replay does not close this: it is a
   reconnect mechanism driven by a `last_event_id` the client must already have, so a client that
   was never connected for the exchange has nothing to advertise.

**Scope of the streamsup comments.** `internal/streamsup` does not know which lane it is on. Its
comments must attribute delivery to `cmd/pyry`'s wiring by name (`sessionModelHold`,
`interactiveTurnEmitterV2.Handle`) rather than assert "the value reaches a client" as a property of
the parser. `internal/turnevent`'s `ModelList` doc is the variant's own type doc and is the right
place for the fuller two-path statement.

## Citation rules for this ticket

- **Citable, all verified at `c639ac8b`:** #1840, #1848, #1849, #1863, #1867. #1845 and #1857 are
  also verified landed and may be named where a comment actually describes the thing they ship.
- **Banned:** #1693, #1858, #1864, #1837, #1690, #1719. Every one is `CLOSED / NOT_PLANNED` — a
  split parent. Writing one is a fresh instance of the defect this ticket removes.
- **`#1683` and `#1692` in the fakeclaude header are not this ticket's**; leave both untouched.
- **Name the symbol, never the line.** `make cite-guard` is diff-scoped and checks every comment
  line this branch adds or modifies. A `file.go:NNN` — or a range, or a bare `:NNN` — in new prose
  is a red gate. Write ``turnbridge.MapEvent's ModelList arm``, not a location.
- Most sites are **not** number substitutions. Only four are (regions G, H, I, M below).

## The thirteen regions

Each row: what is false, what survives, and what the replacement must assert. Find each by symbol
and by the quoted fragment — line numbers in the ticket body were re-derived at `b9731050` and this
branch starts from `c639ac8b`.

### `internal/streamsup/parser.go`

**A — `maxModelResolved`, the "Amplification from input to retained bytes" paragraph.**
Three facts are offered; **two are false**.
- *Fact 1* — `defaultMaxParseBuf` caps the line at 4 MiB, densest legal entry is `{}`, one
  pathological line is order 100 MB of transient. **True. Keep verbatim.**
- *Fact 2* — *"it is TRANSIENT, not retained, because turnbridge.MapEvent's default drops the event
  and frees it, one in flight at a time"*. **Numberless and false twice**: the `default` no longer
  sees the variant, and the capped result is retained per session and per conversation.
- *Fact 3* — *"nothing puts it in the eventring or on the wire until #1693"*. **False on both
  halves.**

The paragraph's conclusion survives and is what the rewrite must keep: the cap is applied **after**
`json.Unmarshal`, so a hostile array is materialised before any of it is bounded, and the spike is
real and bounded by `defaultMaxParseBuf`. Restate the transient/retained split rather than deleting
it — it is now a genuinely two-sided fact and states the bound more strongly:

- **What is transient** is the unbounded decoded array, alive only until `emitModelList` applies
  the caps, then garbage. Nothing downstream ever sees it.
- **What is retained** is only the **capped result** — at most `maxModelListEntries` entries, each
  bounded by the three string caps and by `maxModelEffortLevel` × `maxModelEffortLevelCount`. Point
  at `maxModelListEntries`, which already carries the aggregate arithmetic; do not restate the
  number here (this paragraph's own doctrine is that the multiplicand lives here and the product
  lives there).
- Name the two retainers by symbol: `sessionModelHold` (#1840) and the eventring append in
  `emitMapped` (#1849). The retained figure is the product, not the spike.

**B — `emitModelList`, the "IT RECOGNISES A SHAPE, NOT A CORRELATED REPLY" paragraph.** The sharpest
site in the sweep: it is the stated bound on an untrusted-input path, and the thing that made a
mis-read inventory harmless was precisely that nobody saw it.
- The bound *"the value would still be claude's own claim about itself, bounded by the same three
  caps, retained by nothing, published to nobody, and acted on nowhere"* is **numberless and false
  in all three closing clauses**. The opening clause — *claude's own claim about itself, bounded by
  the same three caps* — is **true and is the half the rewrite is built on**.
- *"That trade is worth revisiting at #1693, when the value first reaches a client and provenance
  starts to matter"* is a **revisit trigger that has fired**, not a pending number.
- *"it is not worth new parser state in a slice nothing publishes"* rests on a false premise.

The conclusion survives — recognise the shape, hold no cross-object parser state for provenance —
and must now be stated as a decision **taken with clients able to see the result**, not deferred
until they can. The replacement must:

1. Say what a mis-read ack would now reach: `sessionModelHold` retains it as the session's menu
   (#1840), `MapEvent` maps it (#1848), and `Handle` emits it to any interactive conn and appends
   it to the eventring (#1849). A menu the daemon never asked for would be presented as one it did.
2. Say why the trade still lands the same way. Correlating `request_id` would prove **which reply**
   the bytes answered; it would not make the **content** more trustworthy, because the subprocess
   that could plant a `models` array in an unrelated ack is the same subprocess that authors the
   `initialize` reply. The content bound is unchanged: three caps, nothing from the payload logged
   (`logControlResponse`), and the render boundary owed by the client
   (`protocol.ModelOption`'s SECURITY paragraph).
3. Keep the cost side verbatim — `Runner.nextControlID` mints the id inline and nothing retains it,
   its two sibling writers discard theirs, so the parser holds no link to it.
4. Record that the revisit **happened and was re-taken here**. Do not point it at a new ticket.

**C — `emitModelList`, rung 3 of the FOUR RUNGS list.** *"The safe failure direction here is the
false NEGATIVE, and nothing publishes the value until #1693, so no client can read a false zero in
the meantime."* The second clause is false. The choice stands; give it its footing:
- A false **zero** would now reach a client's menu on the live lane as "claude offers no models".
  A false **negative** shows the client no menu at all, and `sessionModelHold` holds nothing, so
  `resolveBoundModelList` refuses rather than answering an empty one.
- Both outcomes are a *missing* menu; only the false positive is a *wrong* one. That asymmetry is
  the footing, and it is stronger now than when nobody could see either.
- `emitRateLimit`'s rung 3 stays the precedent, and the rung's own first argument — a `ModelList`
  carrying zero entries names no model, so it cannot serve the purpose the variant exists for —
  is untouched.

**D — `logControlResponse`, the `dropped` paragraph.** *"Until #1693 publishes the event, this
record is the ONLY observable the cap has"* is false: `DroppedModels` crosses `MapEvent`'s arm
verbatim (#1848) and reaches the wire as `dropped_models` (#1849), where
`protocol.ModelListPayload.DroppedModels` documents it as client-facing. The record keeps its own
reason and the rewrite must state it:
- **An operator-facing signal is not a client-facing one.** The wire field tells a phone its menu
  is short; the record tells an operator, on the daemon's own timeline.
- **The wire field is not a reliable observable of the cap.** No conn need be interactive when the
  `initialize` exchange happens, and the live send is droppable at the fan-in — so a cap can fire
  with no frame reaching anyone. The record always exists.
- **The clause *"a cap firing in production is a cap nobody can know fired"* needs the same
  qualification and carries no number.** With `dropped_models` on the wire, a *client* can now know
  — so what survives is that no **operator** can, without a phone having been connected and having
  reported back. Qualify it; do not leave it absolute and do not delete it.
- Keep the surviving consequence: without it, the first evidence that 10 is the wrong number would
  arrive as a user's short menu. Keep the `emitBackgroundTaskRoster` comparison unchanged.

**E — `logControlResponse`, the `levels_dropped` paragraph.** *"nothing reads
turnevent.ModelOption.TruncatedFields either until #1693"* is false — `MapEvent`'s arm crosses it
as the slice it is, and `protocol.ModelOption.MarshalJSON` deliberately exempts it so
nothing-was-cut reaches the wire as `null`. **But the argument survives almost intact, and the
reason is a distinction the rewrite must not blur:**
- What reaches the wire is a **name**, `"effort_levels"`, at most once per entry, and it says the
  same thing whether one level was cut or ninety were dropped.
- The **magnitude reaches nowhere else**. `turnevent.ModelOption.EffortLevels`' own "WHAT THAT
  GIVES UP" paragraph states exactly this: the true level count is not recoverable from the event,
  where `ModelList`'s true entry count is recoverable as `len(Models) + DroppedModels`.
- So `levels_dropped` is still the only place the **number** appears, and "the first evidence that
  `maxModelEffortLevelCount` is the wrong number would arrive as a user's short effort menu"
  survives unchanged. Add the operator-vs-client and best-effort points from D by reference rather
  than restating them.
- Leave the admissibility half alone — a daemon-computed integer carrying none of claude's bytes,
  and the #833 posture on the level strings.

### `internal/turnevent/event.go`

**F — `ModelOption.EffortLevels`, the second of the "Three facts close it".** *"a kept distinction
would have a lifetime of one call: nothing publishes ModelList yet, and its first consumer maps
into protocol.ModelOption, whose MarshalJSON already normalises nil to []"*. **Numberless.** Both
the one-call lifetime and the no-publisher premise are false — #1840 retains the value and the
eventring holds the mapped payload per conversation.

The conclusion survives on the second half, and the replacement restates it as **unobservable
rather than short-lived**, which is the true and stronger form:
- The distinction now survives far longer than one call — in `sessionModelHold` for the session's
  life, and `MapEvent`'s arm crosses `EffortLevels` as the slice it is, nil left nil, so it even
  survives the mapping.
- It still dies **at the wire**, because every path to a client ends at
  `protocol.ModelOption.MarshalJSON`, which normalises nil to `[]` and states at its own type that
  an empty effort list on the wire is a COLLAPSE rather than a positive statement (#1704).
- So a kept distinction would be one **no client could ever observe, however long the daemon held
  it**. Same conclusion, honest footing. #1861's protocol-side wording — the two having landed on
  the same collapse, #1848's mapping needed no fork to bridge — is the sentence this one now
  mirrors from the daemon side.
- Facts one and three (claude's observed absence drops the whole capability block; `slices.Equal`
  cannot see the distinction) are untouched.

**G — `ModelOption.EffortLevels`, the "WHAT THAT GIVES UP" paragraph.** *"preserved only long enough
for the mapping (#1693) to discard it"* — **attribution only, substance true**:
`protocol.ModelOption` still has no field for a per-entry dropped-level integer, so the mapping
does discard it. **#1848.** One number.

**H — `ModelOption.SupportsAutoMode`, the first of "Three facts support it".** *"a pointer here
would preserve a distinction only long enough for the mapping (#1693) to discard it"* —
**attribution only, substance true**: `protocol.ModelOption.SupportsAutoMode` is a plain bool and
`MapEvent`'s arm crosses the field 1:1. **#1848.** One number.

**I — `ModelList` type doc, the "An Event rather than parser-held session state" paragraph.**
*"Session state would oblige the publishing slice (#1693) to build a second parser→relay path
beside the one every other interactive payload already uses."* Attribution, and the prediction
**came true** — #1849 emits through `interactiveTurnEmitterV2.Handle`, the same emitter every other
interactive payload uses. Re-point at **#1849** and put the clause in the past tense: it *would
have* obliged, and it did not, because the design held. Do not turn it into a claim about the
future.

**J — `ModelList` type doc, the "NOTHING PUBLISHES IT YET" paragraph.** Both premises false. This
is the variant's own type doc, so it is the right home for **the fullest two-path statement in the
sweep**, and the only place that statement belongs in full. It must say:
- `MapEvent`'s `ModelList` arm maps it onto `protocol.ModelListPayload`, `DroppedModels` included
  (#1848).
- `interactiveTurnEmitterV2.Handle` emits the mapped frame on the live interactive turn lane and
  that emit appends it to the eventring, so it is retained per conversation for reconnect replay
  (#1849). #1845 proves it end to end.
- `sessionModelHold` retains the decoded value per session, above the droppable send (#1840).
- **A client that connects after the `initialize` exchange receives nothing today.** #1863 landed
  the relay-side connect seam (`V2SessionConfig.RetainedModelLists`, drained by
  `reconcileModelLists`); nothing fills it, and **#1867** is the outstanding slice that does.
- The paragraph's own conclusion is kept and re-footed: the producer takes the false NEGATIVE on
  every ambiguous line rather than emitting a list it did not observe. Same footing as region C —
  a missing menu beats a wrong one — now that a client can read the result.

Keep this to the paragraph. The neighbouring paragraphs (`turnMarkFor` answers `turnMarkNone`;
claude's `session_id` deliberately not a field) are true and out of scope.

### `internal/streamsup/parser_test.go` — comments only

**K — the `"the record names both numbers"` subtest.** *"this record is the only observable the cap
has until #1693."* The assertion's own reason is untouched and must stay: this is the **only** place
`dropped` is non-zero, so without it a producer hard-coding 0 stays green everywhere else. Replace
the stale clause with region D's footing in one sentence — the entry count does reach a client as
`dropped_models`, and the record exists because it is the operator-facing signal that survives a
cap firing with no interactive conn present.

**L — the `"the record names how many levels were dropped"` subtest.** *"this record is the only
observable the level bound has until #1693."* Same treatment with region E's footing, and here the
sharper form is available and should be used: the wire carries only the per-entry
`"effort_levels"` **name**, so this record is still the only observable of **how many**. Everything
below it — the three-entry arrangement, the four counter-scope mutants, the exact-map-equality
sweep — is untouched.

**Neither subtest changes an assertion, a fixture or a name.**

### `internal/e2e/internal/fakeclaude/initialize_control_test.go`

**M — the leading package comment.** *"so #1693's publishing path and #1683's command list can be
proven"* — plain attribution. **#1849** is the publishing path the hermetic tier proves. The
`#1692` opening and the `#1683` half are untouched, and so is everything from the "Untagged on
purpose" paragraph down.

## What not to touch

- `docs/knowledge/features/streamsup-package.md`, `e2e-realclaude.md` and every other file under
  `docs/knowledge/` — the documentation phase folds this work in after code review. Excluded by
  design, not oversight. `docs/` and `docs/specs/` are likewise out of scope; the only file this
  branch adds outside `internal/` is this spec.
- `internal/protocol` — #1861 corrected it three days ago at `c639ac8b`.
- `docs/protocol-mobile.md` — #1860 corrected it.
- `cmd/pyry/relay_guard_test.go` — the ticket body reserves its `#1693` references for #1867.
  **They are already gone at `c639ac8b`**; a grep finds none. Nothing to do, and nothing to add.
- `internal/e2e/realclaude/interactive_stream_inband_model_test.go`'s *"nothing EMITS the frame
  until #1617"* — that is `model_announced`, already corrected by #1639. A wording grep hits it.
  Leave it.

## Testing strategy

There is nothing to assert: no behaviour changes and no assertion may change. Verification is
mechanical and the developer should run all four checks.

- `make check` must stay green, which for this branch proves only that nothing was broken.
- `make cite-guard` must pass. It is the one gate this ticket can genuinely fail — every rewritten
  comment line is a diff-scoped citation candidate.
- **Number sweep:** `git grep -n '#1693\|#1858\|#1864\|#1837\|#1690\|#1719' -- internal/streamsup
  internal/turnevent internal/e2e/internal/fakeclaude` must return nothing.
- **Wording sweep, because three of the stale claims carry no number.** Run the same
  case-insensitive alternation over the four files that found them:
  `nothing publishes`, `publishes the event`, `publishes the value`, `nothing puts it`,
  `not retained`, `retained by nothing`, `published to nobody`, `acted on nowhere`,
  `nothing emits`, `no client`, `only observable`, `drops the event`, `drops the variant`,
  `in flight at a time`, `lifetime of one call`, `slice nothing publishes`, `nobody`.
  Every surviving hit must be one you deliberately kept and rewrote. `logControlResponse`'s
  `nobody` hit is **not** a free pass: it is region D's fourth bullet, and it has to come out of
  this ticket qualified rather than untouched.
- **Read-back check:** for each of the thirteen regions, read the finished comment whole and ask
  whether a reader could take from it either of the two banned readings — that nothing publishes
  the value, or that a client connecting after the exchange receives it.

## Open questions

None that block. Two judgement calls left to the developer:

- **How much of the two-path statement to repeat outside region J.** The recommendation is: state
  it in full once, in `ModelList`'s type doc, and elsewhere name only the fact each paragraph
  needs. Regions A, D and E need "retained, and reaches the wire"; region B needs the full reach of
  a mis-read inventory; region C needs only "a client can read the result now".
- **Whether region B keeps a forward pointer at all.** It should not. The trigger fired and the
  answer was re-taken; naming #1867 there would set a second trigger for a question #1867 does not
  answer — #1867 changes who receives the value, not whether the parser can correlate a reply.
