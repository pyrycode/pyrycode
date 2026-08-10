# #1447 — A measured non-establishment reaches a run-level value of its own

**Ticket:** [#1447](https://github.com/pyrycode/pyrycode/issues/1447) · **Size:** `s` (confirmed, not overridden) · **Blocker:** #1446 (merged, `7d5c6cf`) · **Blocks:** #1448
**Package:** `internal/e2e/realclaude` · offline, fixture-driven, no live claude, no credentials, no daemon, no `t.Skip`.

Everything measured in this spec was measured **at `7d5c6cf`** with `go test -overlay` against an
unmodified worktree. Where a number appears, it came out of a run, not an estimate.

---

## Files to read first

Read these before writing anything. This is the turn-1 data load; the rest of the spec assumes it.

| Path | What to extract |
|---|---|
| `internal/e2e/realclaude/trail_run_outcome_test.go:100-113` | The value space's header doctrine: positive allowlist, the `run-` prefix, why the third sub-namespace is load-bearing. The count sentence lives here. |
| `…/trail_run_outcome_test.go:185-210` | `trailOutcomeVoidPathOwesNoReason`'s doc — **the shape to copy** for the new value, and the paragraph at `:192-197` that this ticket falsifies. |
| `…/trail_run_outcome_test.go:250-301` | The evidence-route space: two values, `trailRunRouteValues`, `trailIsRunRoute`. `trailRouteSighting`'s doc at `:270-273` says "Set on `trailOutcomeAliveAtSightingByOrdering` **alone**" — this ticket makes that false. |
| `…/trail_run_outcome_test.go:376-414` | `trailRunOutcome`, the Detail **content rule**, and the `Route` field doc at `:396-401`. |
| `…/trail_run_outcome_test.go:650-672` | The "no C10 over `Ordering`/`PinnedPid`" note. Do not add one; it would answer out-of-contract on every run that exists. |
| `…/trail_run_outcome_test.go:676-690` | Why step 1's switch has **no default arm** and why an evidence route must be consulted *inside* an arm, never by falling through. |
| `…/trail_run_outcome_test.go:711-756` | **The arm this ticket edits.** Both of its outcomes. |
| `…/trail_run_outcome_test.go:1308-1391` | `TestTrailClassifyRun`: the `wantRoute` map (`:1319-1322`), the three provenance assertions (`:1352-1366`), the truncation-marker check (`:1345`), the coverage loop (`:1386-1390`). |
| `…/trail_run_outcome_test.go:1854-1982` | `TestTrailRunOutcomeCarriesNoCapturedBytes`, and specifically the REFUTED block at `:1954-1981` — **this test is RED before your edit**, see § AC5. |
| `…/trail_run_outcome_test.go:2036-2084` | `trailDeclaredFinishInstantClause` (117 B), why it sits at the end of the file, and the exchange-not-append arithmetic. |
| `…/trail_sighting_liveness_test.go:138-204` | The sighting space's three values and five reasons. `trailSightingUnestablished` (`:159-164`) is the input this ticket keys on; its doc states the claim limit you inherit **verbatim**. |
| `…/trail_sighting_liveness_test.go:330-404` | `trailEstablishSighting` — the route. Consumed whole, never re-decided. Note which reason pairs with which value. |
| `…/trail_sighting_liveness_test.go:474` | `trailSightingPin(verdict)` — the fixture the new row needs. No new fixture is required. |
| `…/trail_run_instant_clause_test.go:30-129` | **Not named in the ticket.** `trailRunCertifiesNothingArms()` and the test that makes "exactly these arms carry the clause" checkable. The new value joins the list. |
| `…/trailer_admissibility_test.go:352` | `trailDetail` → `reachCapCommand`, the silent 512-byte truncation every Detail inherits. |
| `…/trailer_admissibility_test.go:1214-1297` | The union map. Equality-based, **blind to containment** — hence § Naming below. |
| `…/finding_trailer_evidence_test.go:783-785, :861-871` | **Not named in the ticket.** `finTrailerOutcomeValues()` derives from `trailRunOutcomeValues()`, so `len(distinct) != 21` at `:865` goes RED on your edit. |
| `…/trail_ptyrunner_composition_test.go:180-251` | AC2's pin. Read it; do not edit it. It drives `trailGateUsable` + `trailAdmitProof` and never reaches the arm you are changing. |
| `docs/specs/architecture/1446-sighting-route-on-the-absent-owes-none-arm.md` | The blocker's spec. Its § on the exchange-not-append Detail budget is the direct precedent for § Detail budget below. |

---

## Context

`trailClassifyRun`'s step-1 arm for `trailGateAbsentOwesNone` (`trail_run_outcome_test.go:711-756`)
consults #1440's pinned-pid sighting route and has two outcomes. #1446 gave the *established* case
a value of its own (`trailOutcomeAliveAtSightingByOrdering`) and deliberately left **everything the
route did not establish** on the pre-existing `trailOutcomeVoidPathOwesNoReason`, so that no run in
existence changed answer.

Three different things now share that value:

| the route… | `sighting.Value` | should say |
|---|---|---|
| measured a pinned pid and did not establish aliveness | `sighting-not-established` | **this ticket's new value** |
| measured nothing (a read that did not answer) | `sighting-void` | stays — #1448 |
| was never staged (ordering uncertified) | `sighting-void` | stays — #1448 |

The first is a **measured refutation of this evidence route**. Publishing it under
`run-void-path-owes-no-reason` — whose doc says nothing was certified and whose Detail says the
route "was consulted and answered X" — files a measurement that ran under the name of one that
never happened. This ticket takes that case off the blanket. The other two are #1448's.

**Reachability, stated so it is not rediscovered cold.** No shipped gather produces
`trailGateAbsentOwesNone` today: both fill the gate's runner-path field with `trailRunnerUnread()`
by construction (`finding_run_gather_test.go:552`, `:789`; `trail_run_rig_test.go:162`), so the
gate's absence branch falls to the path-unnamed case and answers `trailGateOutOfContract`. **This
ticket is offline and does not close that gap.** Do not add a comment claiming otherwise, and do
not attempt to make the arm live-reachable.

---

## Design

### 1. The new value

Add one constant to the run-outcome space, in the const block, **immediately after**
`trailOutcomeVoidPathOwesNoReason` (`:210`) and before `trailOutcomeVoidReasonNotOwedByPath`:

```go
trailOutcomeVoidPinnedPidDidNotEstablish = "run-void-pinned-pid-did-not-establish"
```

Its doc follows `trailOutcomeVoidPathOwesNoReason`'s shape (`:185-210`) and must argue, at minimum:

- **What it is** — the gate read `trailGateAbsentOwesNone`, and the sighting route *measured* a
  pinned pid and did not establish aliveness at the trailer's sighting. A reading, and still a
  void: nothing was certified.
- **Not an it-exited verdict.** Inherit `trailSightingUnestablished`'s claim limit verbatim
  (`trail_sighting_liveness_test.go:159-164`): a statement about **this evidence route**, not about
  the command having been dead at the sighting — the read is late by construction, so it cannot
  rule the earlier instant out. There is deliberately no "exited before the sighting" value in this
  space for it to decay into. `trailOutcomeNoRowMatched`'s doc (`:144-150`) is the run space's own
  precedent for that discipline; say so.
- **Deliberately NOT `trailOutcomeVoidPathOwesNoReason`** — its sibling and the value it forks from.
  That one is now the route having measured *nothing* (or never having been staged); this one is a
  pid that **was** read. Same void-ness, two different readings.
- **Deliberately NOT a `run-`-prefixed transform of `trailSightingUnestablished`.** The union map
  compares for **equality** and is blind to containment, so `"run-sighting-not-established"` would
  read as distinct to the map and as a duplicate to a reader. Same trap #1446 documented for
  `trailOutcomeAliveAtSightingByOrdering` (`:133-138`).
- **Not `trailOutcomeVoidLivenessInstrument`** — that is the argv scan's per-pid read failing *as
  an instrument*. Here the read answered; it just did not establish.

> **Trap — the phrase `aliveness-at-trailer` is source-swept and capped at two.**
> `TestTrailRunBareAlivenessAtTrailerIsBudgetFiredOnly`
> (`trail_run_instant_clause_test.go:164-201`) reads `trail_run_outcome_test.go` **off disk** and
> asserts the hyphenated phrase appears at **exactly 2 sites** (`:153` and `:698`), each carrying
> the budget-fired argument on the same line. The natural sentence for this doc — *"deliberately
> not `trailOutcomeRunningAtTrailer`, which is an aliveness-at-trailer claim"* — adds a third site
> and reddens that test **twice** (the per-site argument check and the count), in a file the ticket
> never names. Make the same point with unhyphenated words: "aliveness at the trailer", or better,
> name the *instant* the way the rest of this family does. Verified at `7d5c6cf`: the count is
> exactly 2 today.

**Naming — verified, not asserted.** `run-void-pinned-pid-did-not-establish` (37 B) was checked
both ways against every value in `TestTrailAdmissibilityConstantsAreClosed`'s map plus both closed
sets fed to `finTrailerOutcomeValues()`: **0 collisions, 0 containments in either direction.** If
you change the string, re-run that check before committing — see § Testing strategy for the recipe.
Two constraints on any substitute: it must carry the `run-` prefix, and it must not contain, or be
contained by, `sighting-not-established`.

### 2. The arm

`trailClassifyRun`'s `case trailGateAbsentOwesNone:` (`:711-756`) gains a middle branch. The shape:

- `sighting.Value == trailSightingEstablished` → unchanged (`out.Route = trailRouteSighting`,
  `trailOutcomeAliveAtSightingByOrdering`).
- `sighting.Value == trailSightingUnestablished` → **new**: `out.Route = trailRouteSighting`, then
  `decide(trailOutcomeVoidPinnedPidDidNotEstablish, …)`.
- everything else → the existing `trailOutcomeVoidPathOwesNoReason` return, **as the fall-through**.

**No default arm, and no third explicit case.** `trailEstablishSighting` returns one of exactly
three values, so making `trailSightingVoid` the fall-through keeps the arm total with no return
site a fixture cannot reach. An explicit `case trailSightingVoid:` plus a defensive tail would add
an unreachable return and break the totality claim — the same reason this file's step-1 switch has
no default. Whether you write it as two `if`s plus a trailing `return` or as a `switch` on
`sighting.Value` with two cases and a trailing `return` is your call; both satisfy the above.

The arm still reads `sighting.Value` and `sighting.Reason` **and nothing else** from the result.
Do not read `sighting.Detail`; do not re-decide the route; do not add a contract check over
`Ordering` or `PinnedPid` (see `:650-672`).

### 3. The evidence route on a void

AC3 puts `trailRouteSighting` in the published record's `Route` field on the new value. That makes
a **void** carry a route for the first time, which falsifies four prose claims. Correct all four:

| Site | Claim that goes false | Correction |
|---|---|---|
| `:252-253` | route space names "WHICH EVIDENCE CLASS PRODUCED **A FINDING**" | a *verdict* — a finding or a measured non-establishment |
| `:270-273` | `trailRouteSighting` "Set on `trailOutcomeAliveAtSightingByOrdering` **alone**" | set on that value and on the new one |
| `:396-401` | `Route` … "the two answers that carry a route"; `""` "on every arm that reached no evidence route" | see the rule below |
| `:1314-1318` | `wantRoute`'s doc: "a route is published on exactly **the two** values reached through an evidence route" | three |

**The rule to write into `:396-401`, because "reached no evidence route" is now ambiguous:** the
route is published exactly where **the route's own measurement decided the value**. On the new arm
it did. On the fall-through the route contributed no measurement and the verdict rests on the gate
reading alone, so `""` is the honest answer there — and it stays `""` in this ticket. #1448 owns
whether the remaining cases ever name a route.

`wantRoute` (`:1319-1322`) gains one entry:
`trailOutcomeVoidPinnedPidDidNotEstablish: trailRouteSighting`. The invariant at `:1358` then
enforces it on every row for free; a new value publishing a route with no map entry is red on
every row it reaches.

### 4. Detail budget — measured, and the binding constraint

`trailDetail` renders through `reachCapCommand`, which truncates at
`reachMaxCommandBytes` = 512 **silently** (`background_reach_probe_test.go:945-950`,
`:124`). Measured over `trailRunCases()` at `7d5c6cf`:

| row | value | rendered | headroom |
|---|---|---|---|
| the row this ticket re-points | `run-void-path-owes-no-reason` | 472 B | 40 B |
| the unstaged-pair row that stays | `run-void-path-owes-no-reason` | 461 B | 51 B |
| the established answer | `run-alive-at-sighting-by-ordering` | 469 B | 43 B |
| the mirror arm | `run-void-reason-not-owed-by-path` | 501 B | 11 B |

**The sibling arm's Detail does NOT grow a "kept apart from" clause naming the new value.** This is
measured, not a preference: 461 B leaves 51 B, and the shortest honest clause naming a 37-byte
value costs 60–80 B. A draft that appended one rendered **541 B and truncated**, cutting off the
argument the Detail exists to make. The separation obligation is discharged where it costs nothing
— in the new value's own doc, in the new arm's Detail, and in the correction to
`trailOutcomeVoidPathOwesNoReason`'s doc paragraph at `:192-197`. That paragraph currently reads
"it refuted, it measured nothing, or it was never staged … separating them into values of their
own is #1447's work"; rewrite it to say the refuted case now has its own value, name it, and leave
the remaining two as #1448's. **Leave the sibling's format string alone otherwise** — it still
interpolates `sighting.Value`, which is now always `trailSightingVoid`, and that is correct: a
future sighting value would be carried rather than mislabelled.

**The new arm's Detail** is a new format string and pays no exchange, but inherits the ceiling. Its
mandatory content, with measured fixed costs:

| content | cost | why mandatory |
|---|---|---|
| `trailDeclaredFinishInstantClause` | 117 B | The arm forecloses a claim on certifies-nothing grounds; `TestTrailRunCertifiesNothingArmsNameTheInstant` requires it once the value joins the carrier list (§ 5). Splice the constant; do not hand-write it. |
| `sighting.Value` | 24 B | AC1: "names the route's own value". |
| `sighting.Reason` | 25–**34** B | Two verdicts share `trailSightingUnestablished`, so the value alone cannot separate reaped-pending from gone (`trail_sighting_liveness_test.go:171-177`). The answer arm names both; so does this one. |
| `trailGateAbsentOwesNone` | 28 B | The reading half. |
| `trailOutcomeVoidPathOwesNoReason` | 28 B | The kept-apart clause. |

That is **231 B of interpolated and fixed content, leaving 281 B of prose.** A draft carrying all
five, plus a sentence stating the claim limit, measured **486 B / 26 B headroom** on the
`pid-gone` reason and **496 B / 16 B headroom** on the longer `pid-reaped-pending` reason.

> **Budget against the LONGER reason.** `sighting-reason-pid-reaped-pending` is 9 bytes longer than
> `sighting-reason-pid-gone`, and only the shorter one has a table row today. § 6 adds the row that
> makes the worst case checkable rather than trusted.

The Detail must **not** claim the command had exited before the sighting (AC1), and must obey the
record's existing content rule (`:380-390`): it may name outcome/gate/admit/route values, the
sighting route's own value and reason, counts, pids, verdicts and `BoundFrom`; it may **never**
quote `Gate.Detail`, `Admit.Detail`, `Ordering.Detail`, a `pinStateOutcome`'s `Detail`,
`StateColumn` or `ToolStderr`, or the sighting result's `Detail`.

### 5. Closed-set registration

Five sites, all mandatory, all in the same commit — an unregistered value is red at
`TestTrailRunOutcomeValuesAgreeWithThePredicate` and unlookuppable to a reader:

1. `trailIsRunOutcome` (`:422-434`) — add to the case list.
2. `trailRunOutcomeValues()` (`:1988-2005`) — add to the list, in const-block order.
3. `TestTrailAdmissibilityConstantsAreClosed`'s union map
   (`trailer_admissibility_test.go:1214-1297`) — one entry, after `:1252`.
4. `trailRunCertifiesNothingArms()` (`trail_run_instant_clause_test.go:53-59`) — **not named in
   the ticket.** The new arm argues from the gate having certified nothing, so it carries
   `trailDeclaredFinishInstantClause` and must be listed here; the list is the *only* thing that
   makes the clause checkable on it.
5. `finTrailerOutcomeValues()` needs **no** edit — it is
   `append(trailRunOutcomeValues(), finOutcomeValues()...)` (`finding_trailer_evidence_test.go:783-785`)
   — but its size assertion does; see § Count literals.

The step-1 switch stays **total over the gate's seven values with no default arm.**

### 6. Rows

`trailRunCases()` (`:987`) — the fixture already exists (`sightingRefuted`, `:1118-1119`).

- **Re-point the existing row** (`:1193-1202`): `want` → `trailOutcomeVoidPinnedPidDidNotEstablish`.
  Its neighbouring comment (`:1194-1198`) explains why the row exists — amend it to say what it now
  separates: the arm's *measured* refusal from its unmeasured one.
- **Add one row** driven by `trailSightingPin(pinStateExitedNotReaped)`, same `want`. Two things
  come for free from it, and neither is available without a row: the truncation-marker check
  (`:1345`) runs on the **longer** reason, which is the Detail's worst case; and the arm is pinned
  to key on `sighting.Value` rather than on the pin verdict, since both refuting verdicts must
  reach the same run value. `trailSightingPin` takes the verdict as a parameter — no new fixture.

The coverage loop (`:1386-1390`) is satisfied by the re-pointed row, so no third row is needed.

---

## Concurrency model

None. Every function touched is pure over its inputs: `trailClassifyRun` and
`trailEstablishSighting` take records and return records, with no goroutine, no channel, no
context, no I/O and no process spawn. All tests are fixture-driven and offline. There is no
shutdown sequence because there is nothing running.

---

## Error handling

This package's classifiers do not return errors — they return *values from closed sets*, and the
error-handling design is which value an unhappy reading lands on. Three rules bind this change:

- **A measured non-establishment is not an instrument failure.** The pid read answered. Landing it
  on `trailOutcomeVoidLivenessInstrument` would report a working instrument as a broken one.
- **A measured non-establishment is not an out-of-contract record.** The inputs are in their closed
  spaces; the caller did nothing wrong. Landing it on `trailOutcomeOutOfContract` would file a
  genuine measurement as a caller's bug — the collapse #1417 and #1434 exist to prevent.
- **A measured non-establishment is not an exit claim.** The pinned pid is re-read *after* pyry
  exits, so it is late by construction and rules the earlier instant neither in nor out. Neither
  the value string nor the Detail may say, or imply, that the command had exited.

The one genuinely new failure mode this change introduces is **silent Detail truncation** at 512
bytes. It is handled deterministically rather than by care: `TestTrailClassifyRun:1345` asserts
`reachTruncationMarker` is absent from every row's Detail, and § 6's second row extends that check
to the longest reason the arm can render.

---

## Testing strategy

Verify with (these files carry `//go:build e2e_realclaude`, so **`make check` does not compile
them**):

```
go test -race -tags e2e_realclaude -run 'TestTrailClassifyRun|TestTrailRunOutcomeCarriesNoCapturedBytes|TestTrailRunOutcomeValuesAgreeWithThePredicate|TestTrailAdmissibilityConstantsAreClosed|TestTrailRunCertifiesNothingArmsNameTheInstant|TestTrailComposesUnderAPtyrunnerReading|TestFinTrailerRecordOutcomeIsConsumedAsHanded' -v ./internal/e2e/realclaude/
```

Then compile the whole tag once — `go vet -tags e2e_realclaude ./internal/e2e/realclaude/` — and
run the package's full tagged suite. **Read the count of tests that executed**; an exit code cannot
tell a skip from a pass.

### AC5 is a RED, not a green

`TestTrailRunOutcomeCarriesNoCapturedBytes`'s REFUTED block (`:1954-1981`) **fails before your
edit**. #1446 added it deliberately, and it pins the two things this ticket moves:

- `:1970` asserts `trailOutcomeVoidPathOwesNoReason` → becomes the new value.
- `:1975` asserts `got.Route == ""` → becomes `trailRouteSighting`.

Do not go looking for a way to keep it green. Amend it: the same three plants stay
(`Ordering.Detail`, `PinnedPid.Detail`, `PinnedPid.ToolStderr`), the premise assertions move to the
new value and the sighting route, and the block's closing comment — "the record's OTHER published
key set, the one with no route key in it" — must be rewritten, because the route key is now
present.

**Keep the `Route` premise assertion — re-point it, do not delete it.** It is not decoration: it
is what guarantees `sweep()` runs over the record's *route-bearing* key set. Deleting it (the
tempting move, since `""` is no longer the expected value) would leave the key walk unanchored, and
a future arm that dropped the route would silently shrink the swept key set without reddening
anything. Same shape as the hole #1446 was reworked for: a per-arm sweep that covers one of the
arm's two outcomes measures less than it appears to.

**Do not add a fourth sweep block to recover the no-route key set.** After this change all three
blocks publish a route, and the record with `evidence_route` present is a strict **superset** of
the one without: `Route` is the only field that changes between the pre- and post-edit refuted
records, and it is `omitempty`. A key walk over the superset cannot miss a `command`/`args`/
`comm`/`argv`-shaped key the subset would have caught. Coverage is preserved, not lost — say so in
the amended comment so a reviewer does not read the change as a hole.

### Scenarios to assert

Bullet-pointed, not pre-written — write them in the file's idiom.

**`trailRunCases()` rows (both flow through every assertion in `TestTrailClassifyRun`):**

- Certified ordering + `trailSightingPin(pinStateNoSuchProcess)` → the new value; `Gate` =
  `gate-absent-reason-owes-none`; `Admit` = `""`; `Route` = `run-route-pinned-pid-sighting`; Detail
  untruncated.
- Certified ordering + `trailSightingPin(pinStateExitedNotReaped)` → the same value and route, with
  the longer reason rendered and still untruncated.

**Unchanged, and each must be re-verified rather than assumed:**

- `trailRunSightingEstablishedReadings()` → still `trailOutcomeAliveAtSightingByOrdering`, still
  `trailRouteSighting`. The new branch must not swallow the established case.
- `trailRunAbsentOwesNoneReadings()` (both inputs unstaged) → still
  `trailOutcomeVoidPathOwesNoReason`, still `Route == ""`. This is the case #1448 owns; it must not
  move here.
- `TestTrailComposesUnderAPtyrunnerReading` (`trail_ptyrunner_composition_test.go:180-251`) →
  **unamended**, per AC2. It drives `trailGateUsable` + `trailAdmitProof` and never reaches this
  arm; its three Detail-sentence assertions and its 68-byte headroom check at `:219-226` stay
  intact. If you find yourself editing that file for anything but a line cite, stop — something
  else went wrong.

**Clause carriers (`TestTrailRunCertifiesNothingArmsNameTheInstant`):** the new value's Detail
contains `trailDeclaredFinishInstantClause`; every non-carrier row's still does not. Both halves
already run off `trailRunCases()`, so adding the value to `trailRunCertifiesNothingArms()` plus the
two rows is the whole edit.

### Verifying the value string both ways

Before committing, check the chosen string against every value in the family — the union map is
equality-based and cannot see containment. Run it as a throwaway via `go test -overlay` so nothing
lands in the worktree ( `-overlay` maps a scratch file onto a path in the package; the file is
never written to the repo):

- Build the same value inventory `TestTrailAdmissibilityConstantsAreClosed` builds, plus
  `trailRunOutcomeValues()` and `finOutcomeValues()`.
- For each, assert `v != new && !strings.Contains(new, v) && !strings.Contains(v, new)`.
- `run-void-pinned-pid-did-not-establish` was measured clean on all three at `7d5c6cf`.

---

## Count literals this change invalidates

The set goes from **fourteen (four answers, ten voids) to fifteen (four answers, eleven voids)**,
and the carrier set from four to five. Word, numeral and ordinal forms all appear — sweep for all
three (`-i`, and `[ -]?` for hyphenated compounds; `\bfour\b` misses "fourth").

**Two are executable and will fail your build:**

| Site | Now | After |
|---|---|---|
| `trail_run_outcome_test.go:2013` | `if len(values) != 14` | `!= 15`, and the message at `:2014-2019` re-argued for #1447 |
| `finding_trailer_evidence_test.go:865` | `if len(distinct) != 21` | `!= 22`, and the message at `:866-870` — `finTrailerOutcomeValues()` derives from `trailRunOutcomeValues()`, so the union grows without touching the list |

**The rest are prose, and a stale one is a code-review FAIL:**

| File | Lines | Text |
|---|---|---|
| `trail_run_outcome_test.go` | `:102-103` | "POSITIVE ALLOWLIST of **fourteen**: four answers and **ten** named voids" |
| | `:418` | "one of the **fourteen** recorded outcomes" |
| | `:775` | "none of the **ten** voids does" |
| | `:1148` | `// --- the ten voids ---` |
| | `:2056` | "requires exactly these **four** arms' Details to carry it" |
| `trail_run_instant_clause_test.go` | `:30` | "the **four** step-1 outcomes" |
| | `:62` | "exactly these **four** arms" |
| | `:64`, `:121-122` | "stops **the three** DRIFTING" / "stopped producing one of **the three**" — **already stale at HEAD** (#1446 made them four). You are editing this comment block; correct them rather than leaving "three" beside a new "five" in the same paragraph. |
| | `:73` | "**four** outcomes out of **fourteen** … redden the other **ten**" |
| `trailer_admissibility_test.go` | `:1232-1238` | "four answers and **ten** named voids since #1446" |
| `finding_staging_gate_test.go` | `:19`, `:21` | "a closed set of **fourteen** values" / "no value among the **fourteen**" |
| | `:36` | "the classifier's **fourteen** consumed as returned" — **a third site in this file**, not in the ticket |
| `finding_trailer_evidence_test.go` | `:62` | "trailRunOutcomeValues (:1987) are the **fourteen**" |
| | `:261` | "trailClassifyRun's **fourteen**" |
| `finding_exit_path_probe_test.go` | `:149` | "the classifier's **fourteen**" — **a sixth file**, not in the ticket |

`trail_run_outcome_test.go:2071` ("#1446's **fourth** carrier is a NEW format string") is a
statement about #1446 and stays correct; leave it.

**Six files carry mandatory edits, not the three the ticket names.** All are `*_test.go` in one
package; none is a new file. This is the same shape as the blocker (#1446: 18 files, `size:s`).

---

## Cite renumbering

Inserting ~28 lines of const + doc near `:210` and ~20 lines in the arm near `:750` displaces every
inbound line cite below them. Measured at `7d5c6cf`: **~120 inbound cites into
`trail_run_outcome_test.go` across 16 other files in the package**, most of them below the
insertion points. This is where the two nearest analogues' `rework-count:2` round-trips went
(#1417: 114 of 742 added lines were cite renumbering; #1434: 115 of 673; #1446: 117 of 654). It is
**work, not scope** — budget turns for it.

Three traps, each of which has already cost this family a rework round:

1. **Bare `:NNN` refs inherit the LAST-NAMED FILE.** A filename-anchored grep
   (`grep 'trail_run_outcome_test\.go:[0-9]'`) reads clean while a bare `(:884)` two paragraphs
   later still points into this file. Measured: **~40 of the ~120 are bare.** Scan statefully.
2. **Symbol-anchored cites** of the form `<TypeName>:NNN` evade both a filename grep and a bare-ref
   scan.
3. **Fixing a bare cite by spelling the filename resets last-named-file inheritance forward** and
   breaks the next correct neighbour. Re-point by named symbol where the neighbour would break.

The reliable check is a **git-diff line map** rather than eyeballing: after your edits, build
`old-line → new-line` from `git diff -U0 origin/main -- internal/e2e/realclaude/` and re-resolve
every cite through it. Note git's `-a,0` insertion convention is off-by-one and fakes mismatches if
applied naively.

**Scope the sweep to `internal/`.** Cites living in `docs/` are the documentation phase's, exactly
as on #1417, #1434 and #1446. Do not edit `docs/knowledge/**` or `docs/PROJECT-MEMORY.md`.

One pre-existing off-by-one to leave alone unless your delta touches it:
`finding_trailer_evidence_test.go:62` and `finding_staging_gate_test.go:69` cite
`trailRunOutcomeValues` as `(:1987)`; the `func` line is `1988`. Shift it by your delta from its
current value; do not "fix" it to `1988` as a separate change.

---

## Sizing (why this ships as one ticket)

The edit-fan-out red line's **precondition is unmet**: this is not a rename, not a signature
change, not a type replacement, and not a cross-package import flip. `trailRunReadings` and
`trailRunOutcome` are untouched; the value set grows **additively** and every existing caller still
compiles. The ~25 mandatory edit sites are enumerations of a set whose size changed plus a cite
cascade — not consumers of a changed signature. So the LOC line is the operative one.

Measured against the nearest analogues (merge diffs, `internal/` only, at `7d5c6cf`):

| ticket | code | files | cite-renumber lines | net design |
|---|---|---|---|---|
| #1446 (this ticket's blocker, nearest shape) | 654 / 262 | 18 | 117 | ~537 |
| #1434 | 673 / 348 | 16 | 115 | ~558 |
| #1417 | 742 / 344 | 16 | 114 | ~628 |

All three shipped `size:s`. **#1447 is strictly less than every one of them**: no new struct
fields, no new value space, no new predicate, no new fixture, no new helper — one constant, one
branch, one re-pointed row, one added row, five registrations and a count sweep. Projected ~250–350
insertions of which ~110–120 are renumbering. `s` confirmed; the PO label is not overridden.

The **production-source file count is zero** — every file in `internal/e2e/realclaude` is
`*_test.go`, which the ≥5-file gate excludes by definition. It does not apply here in either
direction, and is recorded so a reviewer does not read its silence as an oversight.

---

## Open questions

1. **Does the sibling's Detail ever get to name its new neighbour?** Measured: not at 461 B with a
   37-byte value. #1448 removes two more cases from that arm and will rewrite its Detail anyway; if
   the prose shrinks there, the clause becomes affordable then. Deliberately deferred, not
   forgotten.
2. **Should the remaining fall-through cases publish `Route`?** Left `""` here. #1448 splits
   "measured nothing" from "never staged" and owns that call — at that point the route was
   consulted in one case and structurally absent in the other, which is exactly the distinction a
   route field could carry.
3. **The arm is still unreachable from any shipped gather** (both fill `RunnerPath` with
   `trailRunnerUnread()`). Closing that is neither this ticket's work nor #1448's; it needs a
   gather that reads the real runner path. Recorded here rather than in a code comment, so nothing
   in the package claims a reachability it does not have.

---

## Security review

**Verdict:** PASS

The asset under protection in this family is the **published record**: `trailRunOutcome` is
marshalled into an operator artifact destined for a public GitHub issue, and its whole value is
that it can be pasted **unreviewed**. The live threat is captured bytes — a command string, argv,
or raw `ps` stderr — reaching that record, because those carry the operator's
`CLAUDE_CODE_OAUTH_TOKEN` / `ANTHROPIC_API_KEY` out of the environment. Every category below is
walked against that.

**Findings:**

- **[Trust boundaries] No findings — and the reason is a measurable one, not a judgement.** The
  boundary is the single function `trailClassifyRun`, and the untrusted side of it is precisely
  four string-bearing inputs: `Gate.Detail`, `Admit.Detail`, `Ordering.Detail`, and
  `PinnedPid.{Detail,StateColumn,ToolStderr}` — the last of which takes raw `ps` stderr **verbatim**
  (`process_pin_liveness_test.go:341`) and is the sharpest crossing in the record. **This ticket
  moves no data across that boundary that #1446 had not already moved.** The new arm reads
  `sighting.Value` and `sighting.Reason` and nothing else, both of which are members of #1440's
  closed constant spaces — traced through `trailEstablishSighting`
  (`trail_sighting_liveness_test.go:353-404`), every one of its five reasons is a package constant
  and none is derived from input bytes, including on the off-space-verdict path, which interpolates
  `pin.Verdict` into its own `Detail` (swept at its own producer) and never into `Reason`. The
  established arm at `:730-735` already interpolates exactly this pair. Restated as a spec
  obligation in § 2 ("reads `sighting.Value` and `sighting.Reason` **and nothing else**") and in the
  Detail content rule in § 4.

- **[Tokens, secrets, credentials] No findings.** No token is generated, stored, compared, rotated
  or revoked. The credential exposure is indirect — via argv/env in captured process output — and
  is held off at two layers this ticket leaves intact: `pinStateColumns` is `pid=,ppid=,stat=` so
  `ps` never emits a `command` column at the source, and the key walk in
  `TestTrailRunOutcomeCarriesNoCapturedBytes` refuses any `command`/`args`/`comm`/`argv`-shaped key
  on the published record. `Route` is set from the constant `trailRouteSighting`, so the one new
  key this arm publishes carries no input-derived bytes.

- **[File operations] SHOULD FIX — addressed inline, § 1.** This change writes no files, but one
  shipped test **reads source off disk**: `TestTrailRunBareAlivenessAtTrailerIsBudgetFiredOnly`
  (`trail_run_instant_clause_test.go:164-201`) sweeps `trail_run_outcome_test.go` for
  `aliveness-at-trailer` and pins it at exactly 2 sites. The new value's doc comment is written in
  an idiom that reaches for that exact phrase, and a third site reddens the count *and* the
  per-site argument check — in a file the ticket never names. The first draft of this spec did not
  mention it. A trap block in § 1 now does, with the verified count at `7d5c6cf`. No path
  traversal, no TOCTOU, no permissions or symlink surface: the read is a fixed relative filename
  with no caller input in it.

- **[Subprocess / external command execution] No findings, stated as a design constraint rather
  than an absence.** The design reaches **no** exec-bearing helper. Naming them beats grepping for
  `exec.` — every route to a subprocess in this package is inside a helper, so an `exec.` grep over
  the diff reads clean by construction. Forbidden here: `pinReadState`, `pinScanArgv`,
  `spawnProbePyry`, `holdProbeFIFO`, and the `false`-exec'ing `ExitError` borrower at
  `process_pin_liveness_test.go:1088`. Permitted and sufficient: `trailEstablishSighting`,
  `trailSightingPin`, `trailDetail`, and the existing fixture constructors. No `sh -c`, no
  environment inheritance, no signal handling — the arm is a pure function over a record.

- **[Cryptographic primitives] No findings — the category is inapplicable by design, not by
  omission.** No randomness of any kind (`crypto/rand` or otherwise) is introduced; the tests are
  fixture-driven and deterministic, which is what makes the mutation-and-overlay checks in
  § Testing strategy meaningful. The only comparisons are `==` and `strings.Contains` over
  closed-set constants, none of which is a secret, so constant-time comparison would protect
  nothing.

- **[Network & I/O] No findings — and the input-size cap is the load-bearing one, so it is
  measured rather than asserted.** No socket, no HTTP server, no TLS, no deadline surface. The cap
  that *does* apply is `reachMaxCommandBytes` = 512 on every Detail, which truncates **silently**
  (`background_reach_probe_test.go:945-950`). Silent truncation is an integrity failure here, not a
  cosmetic one: it removes the closing argument the Detail exists to make, at the end, where the
  claim limits live — including the "never an it-exited verdict" sentence. Bounded adversarially:
  the arm interpolates four values, all from closed sets, with no caller-scaled content, so the
  worst case is finite and was **measured at 496 B / 16 B headroom** on the longest reason. § 4
  carries the budget and § 6 adds the row that makes the worst case fail the build rather than
  truncate quietly.

- **[Error messages, logs, telemetry] No findings.** The Detail *is* the operator-facing message
  and is governed by the record's shipped content rule (`trail_run_outcome_test.go:380-390`),
  restated in § 4. `TestTrailRunOutcomeCarriesNoCapturedBytes` is the enforcement, and § Testing
  strategy keeps all three needle plants and re-points rather than deletes the `Route` premise, so
  the sweep continues to run over the route-bearing key set. The one coverage question the change
  raises — that no sweep block exercises the *route-absent* key set afterwards — was checked rather
  than waved through: `Route` is the only differing key between the pre- and post-edit refuted
  records and it is `omitempty`, so the swept set is a strict **superset** of the one it replaces
  and no forbidden key can hide in the difference.

- **[Concurrency] No findings.** No goroutine is spawned, no channel opened, no lock taken, no
  shared state mutated: `trailClassifyRun` and `trailEstablishSighting` are pure over their inputs
  and return values. There is no shutdown sequence and no goroutine lifecycle to leak. The suite
  runs under `-race` regardless (§ Testing strategy).

- **[Threat model alignment] No findings; two items named as out of scope.** The applicable model
  is this package's paste-safety contract rather than `docs/protocol-mobile.md` — no relay, no
  device, no wire format is touched. Deferred and named: the two remaining fall-through cases (the
  route measured nothing / was never staged) keep today's value and `Route == ""` and are
  **#1448's**; making the arm reachable from a shipped gather needs a gather that reads the real
  runner path and has **no ticket yet** — recorded in § Open questions 3 rather than as a code
  comment, so nothing in the package claims a reachability it does not have.

**Reviewer:** architect (self-review per `architect/security-review.md`)
**Date:** 2026-08-10
