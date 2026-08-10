# #1434 — A `terminal_reason` on a runner path that owes none reaches a gate value and a run outcome of its own

**Ticket:** [#1434](https://github.com/pyrycode/pyrycode/issues/1434) — split from #1427 ← #1369 (closed NOT_PLANNED)
**Blocker:** #1433 (merged `a4506b2`, PR #1435) — landed the arm this ticket promotes
**Blocks:** #1428 — the reason × reading matrix ticket
**Size:** `s` (see § Sizing, below — measured, not assumed)
**Security-sensitive:** yes; the self-review pass is at the end of this document.

All line numbers below were re-derived at `a4506b2`. Every count stated as "currently N" was
re-measured against the tree, not carried from the ticket body.

---

## Files to read first

Turn-1 data load. Read these ranges before the first edit; the manifest in § The edit manifest
then applies mechanically.

**The two files that define the sets**

- `internal/e2e/realclaude/trailer_admissibility_test.go:89-166` — the gate's value space. The
  const block, `trailGateAbsentOwesNone`'s doc (`:123-148` — **the shape to copy**, including its
  claim limits and its "what it does NOT claim" paragraph), and `trailGateOutOfContract`'s
  enumeration (`:149-165`).
- `:322-390` — `trailGate`'s doc. The return-site accounting (`:355-371`) and the
  path-invariance argument the sweep rests on (`:382-386`).
- `:441-570` — the `reason == ""` branch: the key-name membership read, the absence switch, its
  three return sites and the present-and-blank fall-through. Not edited except for two prose
  pointers, but it is the structural model for the arm below it.
- `:585-690` — **the arm this ticket promotes.** `:585-666` is its 82-line doc (both ordering
  arguments, the byte-budget argument, the presence-is-the-reduction's-read argument); `:667-679`
  is the body; `:681-689` is the usable fall-through it sits in front of.
- `:859-875` — `trailIsGateValue` / `trailIsAdmitValue`, the membership predicates.
- `:1120-1257` — `TestTrailAdmissibilityConstantsAreClosed`: the union map (37 entries at
  `a4506b2`, verified by counting: 6 gate + 7 admit + 12 outcome + 6 reason + 6 shipped) and the
  paragraph at `:1139-1151` explaining why this test catches a **colliding** value and never an
  **unhandled** one.
- `:1393-1622` — `trailGateAbsenceCaseMarkers()` and `TestTrailGateNamesWhichAbsenceCaseFired`:
  **the absence-side driver whose argument shape this ticket inverts on the presence side.** Note
  `:1409-1420`'s "The value is a precondition on three rows and THE DISCRIMINATOR on one" heading
  — that is what the presence driver becomes.
- `:1624-1837` — `TestTrailGateNamesThePresenceCaseOnAPathThatOwesNone`: the driver this ticket
  rewrites. Doc `:1629-1681`, marker-containment guard `:1689-1701`, rows `:1720-1768`, assertions
  `:1770-1835`.
- `:2117-2198` — the runner-path sweep's second companion, whose hard-coded want map (`:2141-2151`)
  goes red at `:2164-2168`.
- `:2348-2406` — `TestTrailGateThenAdmit`: the non-certifying switch (`:2375-2382`) and the
  `calls != 2` pin (`:2392`) that does **not** move.
- `:2490-2634` — the leak sweep's four-row table, its per-row non-vacuity precondition
  (`:2595-2607`) and its output-side headroom assertions.

- `internal/e2e/realclaude/trail_run_outcome_test.go:100-188` — the run's value space;
  `trailOutcomeVoidPathOwesNoReason`'s doc (`:164-182`) is **the shape to copy** for the new
  outcome, including its two "deliberately NOT" paragraphs.
- `:271-298` — `trailIsRunOutcome`, `trailIsBoundFrom`.
- `:300-362` — `trailClassifyRun`'s doc: the nine contract checks and the eight-step ranking.
- `:383-493` — the contract block. **C2 (`:398-409`), C4 (`:423-431`) and C5 (`:433-443`) are the
  three that constrain what this ticket's tests may hand the classifier.** Read them before
  designing any fixture.
- `:495-552` — step 1's default-less switch and the note at `:497-511` explaining where an
  unhandled value actually falls (steps 3-8, **not** step 2).
- `:646-700` — `trailRunWellFormed()`, `trailRunProofReadings()`, `trailRunAbsentOwesNoneReadings()`
  (`:674-696` — **the fixture-helper shape to copy**, including why `Admit` is left zero).
- `:828-890` — `trailRunCases()`' rows; `:882-890` is #1417's mandatory-row comment.
- `:988-1049` — `TestTrailClassifyRun` and the coverage loop at `:1044-1048` that errors on any
  value of `trailRunOutcomeValues()` no row reaches.
- `:1207-1323` — `TestTrailRunComposesUnderAnAbsentReasonOnAPathThatOwesNone`. **This is the test
  to mirror**: its doc's four headings, its control-then-experiment sub-test, and its overlay
  demonstration paragraph.
- `:1383-1430` — `trailRunOutcomeValues()` and the `len(values) != 12` pin.

**The reduction and the passthrough (read-only; neither is edited)**

- `internal/e2e/realclaude/trailer_terminal_reason_test.go:88-110` — `trailReasonPresentOwesNone`'s
  doc. **The claim limit the new gate value inherits is already written here**; the new constant's
  doc restates it in the gate's own register rather than inventing one.
- `:203-206`, `:222-278` — `trailReasonAgainstPath`: presence comes from the key names, never from
  `decodedReason != ""`, and the fall-through to `trailReasonPathUnnamed`.
- `internal/agentrun/streamrunner/runner.go:177-179` — the tee-parse passthrough. **The cite that
  is the reason for the claim limit**: claude's own bytes reach the trailer unchanged, so no
  reading of the line can say pyry wrote it.
- `internal/agentrun/streamrunner/runner.go:250-253` and `watchdog.go:253,:280` — pyry's own
  synthesis on that path: unconditional *when it happens*, which is a statement about what pyry
  writes and never about what claude cannot.

**The consumers outside the pair**

- `internal/e2e/realclaude/finding_trailer_evidence_test.go:780-786` (`finTrailerOutcomeValues()`)
  and `:858-872` (the `len(distinct) != 19` pin — **the one hard red outside the pair**).
- `internal/e2e/realclaude/trail_ptyrunner_composition_test.go:29-40` — the "#1433 … reaches the
  presence arm and `trailGateOutOfContract` instead" claim.

---

## Context

`trailGate`'s presence arm — `terminal_reason` on the line, observed runner path owes none —
answers the shipped `trailGateOutOfContract` (`trailer_admissibility_test.go:670`), whose documented
meaning is *"the input is not a reading"*. That is wrong about what happened, in one way:
`streamrunner.Run` tee-parses claude's stdout for the watchdog and passes the bytes through
unchanged (`internal/agentrun/streamrunner/runner.go:177-179`), so the line **is** genuinely what
the run produced. Nothing is malformed and no caller erred. The record is a measurement filed as a
defect, and `trailClassifyRun`'s step-1 arm (`trail_run_outcome_test.go:545-551`) then publishes
`trailOutcomeOutOfContract` for it.

#1433 shipped that residual **stated rather than hidden**, and named this ticket as its resolution
in the arm's doc (`:597-604`) and in the arm's own published Detail (`:671-676`). Both are
obligations here, not context.

This is the #1417 step for the presence side. #1420 split the absence arm three ways with all three
still answering `trailGateOutOfContract`; #1417 promoted the owes-none one to
`trailGateAbsentOwesNone` + `trailOutcomeVoidPathOwesNoReason`, growing both closed sets. #1433
played #1420's role for presence. This ticket plays #1417's. **#1417 is the implementation model
throughout, including its two follow-on cite-renumbering commits `d9e31f2` and `1ab6fbe`.**

### Why both closed sets grow together, and why that is not negotiable

`trailClassifyRun`'s step-1 switch has **no default arm by design** (`trail_run_outcome_test.go:512`,
argued at `:497-511`). A new gate value registered in `trailIsGateValue` but unhandled there does
**not** fall through to step 2 — C4 forces `Admit` empty for any non-certifying value, so step 2's
`trailAdmitProof` test cannot fire. It falls through to **steps 3-8** and awards a scan-side answer
about pyry from a record the gate says certifies nothing.

`TestTrailAdmissibilityConstantsAreClosed` catches a **colliding** value and never an **unhandled**
one — its own comment says so and points at this exact hazard (`:1139-1151`). So the gate value, its
step-1 arm and the run outcome land in one commit. There is no seam here; see § Sizing.

---

## Design

### The two new constants

Both are unexported test constants in the file that owns their space. Neither is a new type.

| Constant | File | Value |
|---|---|---|
| `trailGatePresentOwesNone` | `trailer_admissibility_test.go`, in the gate const block after `trailGateAbsentOwesNone` | `"gate-present-reason-owes-none"` |
| `trailOutcomeVoidReasonNotOwedByPath` | `trail_run_outcome_test.go`, in the outcome const block after `trailOutcomeVoidPathOwesNoReason` | `"run-void-reason-not-owed-by-path"` |

**The `gate-` / `run-` prefixes are load-bearing** (`trailer_admissibility_test.go:97-103`,
`trail_run_outcome_test.go:108-113`): an input state, the gate's view of it and the run's view of it
must never be one tab-completion apart.

**Containment must be checked in both directions, and equality is not enough.** The union map's
collision check compares for equality; the drivers assert with `strings.Contains`. A new constant
that were a substring of — or contained — an existing marker would make a marker assertion silently
vacuous. Verify mechanically, both ways, against at least `trailGateAbsentOwesNone`,
`trailReasonPresentOwesNone`, the three `trailGateAbsenceCaseMarkers()`, and
`trailOutcomeVoidPathOwesNoReason`. The names above were chosen to pass; a rename must re-run the
check. `TestTrailGateNamesThePresenceCaseOnAPathThatOwesNone`'s existing guard (`:1689-1701`) is the
idiom to follow if a guard is wanted in code.

### What the gate value means, and the ceiling on what it may claim

`trailGatePresentOwesNone`'s doc is written in `trailGateAbsentOwesNone`'s shape (`:123-148`) — say
what the reading supports, then be explicit about what it does **not** claim. It must record:

1. The line is not that path's documented healthy shape.
2. **It certifies nothing.** `Reason` stays empty, which keeps `trailClassifyRun`'s C2
   (`trail_run_outcome_test.go:398-409`) green unamended and lets C4 force `Admit` empty.
3. **NEVER that pyry wrote it**, cited to the passthrough
   (`internal/agentrun/streamrunner/runner.go:177-179`) as the *reason*: claude's own output
   produces the same reading, and a value claiming more would let claude's output name pyry as its
   author. Pyry's own synthesis (`watchdog.go:280`, on a field with no `omitempty` at `:253`,
   reached only from `runner.go:250-253`) is unconditional **when it happens**, which is a statement
   about what pyry writes and never about what claude cannot.
4. It is a statement about **what the trailer carried**, never about whether a process was alive —
   there is no certified instant here for such a claim to be about.
5. It is unreachable from either shipped live gather today: both fill `RunnerPath` with
   `trailRunnerUnread()` (`finding_run_gather_test.go:552, :789`; `trail_run_rig_test.go:162`), so
   over a live run the reading names no runner. State this the way `:142-147` states it for the
   absence sibling — the gate is correct when the path is known, and the shipped gathers do not
   know it. **Do not write a comment claiming otherwise.**

The prose for (3) and (4) already exists at `:606-613`, written into the arm's doc by #1433. Moving
it onto the constant is the natural shape; the arm's doc then cites the constant instead of
restating it.

### The arm

`trailer_admissibility_test.go:668-679`. The `if against.Value == trailReasonPresentOwesNone` guard,
its placement after the budget arm and before the usable fall-through, and the fact that it consults
exactly one of the reduction's answers are all **unchanged**. What changes is the returned `Value`
and the `Detail`.

```go
// contract sketch — not the body
if against.Value == trailReasonPresentOwesNone {
    return trailGateResult{Value: trailGatePresentOwesNone, /* Reason stays absent */
        Detail: trailDetail(...), RunnerPath: in.RunnerPath}
}
```

`Reason` stays absent. The two ordering decisions the arm records — the budget arm wins, the
present-and-blank arm keeps winning — are **unchanged and their arguments stay**; only the sentences
that describe the arm's *answer* move.

#### The Detail rewrite is constrained on three axes, all already enforced

- **It must keep the three phrases `TestTrailGateNamesThePresenceCaseOnAPathThatOwesNone`'s P1 row
  asserts** (`:1732-1741`): `"NEVER that pyry wrote it"`, `"streamrunner/runner.go:177-179"`,
  `"never whether a process was alive"`.
- **It must keep naming the case constant** `trailReasonPresentOwesNone`. Three shipped assertions
  key on it with `strings.Contains`: the driver's P1 marker (`:1793`), the driver's P3
  *must-be-absent* mirror, and the leak sweep's fourth row (`:2578`). Citing the 32 B constant
  rather than embedding the reduction's 395 B Detail is what buys the headroom — that argument
  (`:639-652`) stays true and stays.
- **It must stay under the arm's 470 B ceiling, not the 512 B cap.** The leak sweep's presence row
  asserts the 42 B `trailNeedle` would still have fitted, and `reachCapCommand` **truncates and
  marks** rather than failing (`background_reach_probe_test.go:945-950`), so an overflowing reword
  ships a Detail cut past its own marker. Headroom is asserted **on the output** at `:1827-1834` and
  `:2614-2619`, so an overflow is red rather than silent.

The rewrite has room. The shipped Detail measures 445 B; its closing clause *"The presence side of
the absence reading; a value of its own is #1434"* is ~69 B and is exactly what must go, leaving
~94 B of headroom for the replacement clause. **Re-measure the shipped output and update the byte
figures at `:1678-1681` and `:2526-2529`** — do not carry 445 forward.

### The step-1 arm and the run outcome

`trail_run_outcome_test.go`, in step 1's switch, placed adjacent to its sibling
(`case trailGateAbsentOwesNone:` at `:532`) and before `case trailGateOutOfContract:`. Switch-case
order is not semantic here; adjacency is for the reader.

```go
// contract sketch — not the body
case trailGatePresentOwesNone:
    return decide(trailOutcomeVoidReasonNotOwedByPath, "…")
```

`trailOutcomeVoidReasonNotOwedByPath`'s doc follows `trailOutcomeVoidPathOwesNoReason`'s shape
(`:164-182`), and must carry both "deliberately NOT" paragraphs, re-argued for this shape:

- **Not `trailOutcomeVoidNoTrailer`.** A trailer *was* written — it is claude's own result line —
  and that value's doc reads *"no trailer line was written"* (`:136-138`). Collapsing them would
  report a line that exists as one that does not.
- **Not `trailOutcomeOutOfContract`.** That value says the *caller's record* is not a reading, which
  is precisely the sentence this ticket exists to stop publishing about a genuine measurement.
- **Not `trailOutcomeVoidPathOwesNoReason`** — its sibling. That one is the trailer carrying *no*
  `terminal_reason` where the path owes none: the path's documented healthy shape. This one is a
  reason **present** where the path owes none: not that path's healthy shape. Same void-ness, two
  different readings, and the two must be separable in a published record.
- **Still a void**, because nothing was certified: with no certified terminal reason there is no
  "when the turn was declared finished" instant for a claim about aliveness-at-trailer to be about.

The arm's `Detail` names the gate answer that decided (`trailGatePresentOwesNone`), in the shape
`:538-544` uses.

### Concurrency

None. `trailGate`, `trailReasonAgainstPath` and `trailClassifyRun` are pure over their inputs — no
exec, no clock, no filesystem, no `*testing.T`, and they never fail a test. That purity is what lets
every arm be driven offline. **One concurrency rule does apply to the new fixture helper:** it is a
function and not a package-level `var`, because `trailRunReadings.Liveness` is a slice and
`go test -race` runs this package's tests in parallel — a shared backing array would let one
caller's mutation reach another's (`trail_run_outcome_test.go:650-651`, `:686-687`).

### Error handling

There is no error path. Both functions are total by construction: the gate's contract block is a
guard at the top and the out-of-contract value is never a switch fall-through, and the classifier's
nine contract checks precede its eight-step ranking. The failure mode this ticket must not create is
structural, not an error: **a gate value with no step-1 arm.** That is why the two constants land
together.

---

## The edit manifest

Every site below was re-derived at `a4506b2`. Line numbers drift as edits land — work top-down
within a file, or re-anchor on the quoted text.

### A. `internal/e2e/realclaude/trailer_admissibility_test.go`

| # | Site | Change |
|---|---|---|
| A1 | `:91` | gate space "POSITIVE ALLOWLIST of six" → **seven** |
| A2 | after `:148` | **new** `trailGatePresentOwesNone` const + doc (§ Design) |
| A3 | `:136-142` | `trailGateAbsentOwesNone`'s sibling sentence: **two clauses go false** — the arm no longer answers `trailGateOutOfContract` under a streamrunner reading, and "#1434 is where it **may** get a value of its own" describes an open ticket. Re-point at the new constant. |
| A4 | `:149-165` | `trailGateOutOfContract`'s enumeration: **drop the present-and-named sub-case**; count six → **five** at `:155` and `:164`. The "That one is the FIRST PRESENCE case this value carries" sentence (`:160-164`) goes with it — after this ticket every remaining sub-case is again a record whose `terminal_reason` is missing, blank or moot. Extend the landing-order history with #1434. |
| A5 | `:267-270` | `trailGateResult.Reason`: "empty on the four that do not [certify]" → **five** |
| A6 | `:355-371` | `trailGate`'s return-site accounting. "**all eleven return sites and READ at four**" **does not move** — this ticket changes what a shipped site answers and adds none. What does move is `:364-371`'s VALUE-decided-by-the-reading prose: on the present-and-named shape the reading now picks between a **value of its own** and the usable one. |
| A7 | `:455-464` | the empty-reason branch's "Not every arm below answers `trailGateOutOfContract` any more" paragraph — re-derive its counts against the arms that remain. |
| A8 | `:518-530` | the absence switch's closing paragraph: "#1434 is where it may get a value of its own" → it has one. |
| A9 | `:585-666` | **the arm's doc.** Rewrite `:597-604` (the stated-residual paragraph — its subject is now landed) and fold `:606-613`'s claim limit onto the constant, citing it here. `:615-637`'s two ordering arguments and `:639-666`'s byte/presence-source arguments stay, with the answer-naming sentences updated. |
| A10 | `:668-679` | **the arm body**: `Value` → `trailGatePresentOwesNone`; Detail rewritten per § Design. |
| A11 | `:704-707` | `trailAdmitAttribution`'s doc: the non-certifying list gains the new value; "**FOUR** since #1417 added **the third of them**" → five, and **re-derive the ordinal** — it is a live positional claim, not just a count. |
| A12 | `:863-869` | `trailIsGateValue`'s switch — add the value. |
| A13 | `:1129-1131` | union-map size: "#1417's two make **thirty-seven**" → **thirty-nine** (verified: the map holds exactly 37 at `a4506b2`). |
| A14 | `:1153-1160` | union map — add `"trailGatePresentOwesNone"`. |
| A15 | `:1169-1182` | union map — add `"trailOutcomeVoidReasonNotOwedByPath"`; the comment's "three answers and **nine** named voids" → **ten**. |
| A16 | `:1288-1307` | `TestTrailGate`'s sub-test: "SIX arms answer it" → **five**; the enumeration drops the present-and-named case; "Four of the six … the **TWO** that are not" → four of the five, the **ONE** that is not (the owes-one absence, needing a ptyrunner reading). Extend the count-move history with #1434. |
| A17 | `:1411` | absence driver: "**Six** arms answer `trailGateOutOfContract` since #1433" → **five**, attributed to #1434. |
| A18 | `:1564-1570` | absence driver's `t.Fatalf`: "**six** arms answer it" → **five**. |
| A19 | `:1629-1642` | **presence driver — the argument inverts.** The heading *"The value is a precondition on two rows and never the discriminator"* is false after this ticket: exactly one arm answers the new value, so on P1 the value **is** the discriminator. Rewrite it in `TestTrailGateNamesWhichAbsenceCaseFired`'s shape (`:1409-1420`) — precondition on P3, discriminator on P1, assertion on P2 — rather than editing its numbers. Keep P1's marker assertion regardless: it is strictly stronger and the companion sweep needs the marker present. |
| A20 | `:1651-1654` | the mutant × row matrix — re-derive. The "P3 the arm swallowing present-and-empty" row's sole-red argument changes: after this ticket a swallowed P3 also moves the **value**, not only the Detail. |
| A21 | `:1670` | **"Deferred to #1434's matrix ticket" → #1428.** The matrix ticket is #1428, which is blocked by this one. |
| A22 | `:1674-1681` | headroom paragraph: **re-measure and update** "the three rows publish 445 / 298 / 442 B". |
| A23 | `:1712-1714` | `wantReason` field comment: "empty on both **out-of-contract** rows" — P1 is no longer one. Re-word to what stays true: empty on both rows that **certify nothing**. |
| A24 | `:1727-1731` | **P1 row's `wantValue`** → `trailGatePresentOwesNone`. |
| A25 | `:1774-1782` | the value `t.Fatalf`'s message now spans three cases (the new value / the budget arm / out-of-contract) rather than two. |
| A26 | `:1793-1799` | the marker assertion's message — its "two of the three rows reach %s, so the value cannot tell an arm that swallowed this row's shape from the arm that should have answered it" argument no longer holds for P1. |
| A27 | `:2141-2151` | **runner-path companion's want map**, row `1:` → `{trailGatePresentOwesNone, "", trailReasonPresentOwesNone}`. This is a **hard red** at `:2164-2168` otherwise. |
| A28 | `:2176-2183` | the companion's marker message inverts: "The value alone cannot discriminate on the diverting reading" becomes false. The marker assertion stays (a second detector of different fabric) but its justification is rewritten. |
| A29 | `:2364-2382` | `TestTrailGateThenAdmit`: "**FOUR** since #1417" → five; the non-certifying `switch` gains the value; the `t.Fatalf`'s **four `%s` → five**. The "no row reaches that fourth value … DEFENSIVE rather than exercised" argument widens from one value to **two** — every row carries `trailRunnerUnread()`, which names no runner, so neither path-keyed value can fire. |
| A30 | `:2392` | **`calls != 2` does NOT move.** Verified: every `trailGateCases()` row carries `trailRunnerUnread()`. Do not touch. |
| A31 | `:2522-2529` | leak sweep prose: **re-measure** "#1417's arm publishes 464 B and this ticket's presence arm 445 B". |
| A32 | `:2569-2579` | leak sweep's fourth row: `want` → `trailGatePresentOwesNone`. `marker` stays `trailReasonPresentOwesNone`. |
| A33 | `:2595-2599` | the non-vacuity precondition, **both halves move**: "**six** of `trailGate`'s eleven return sites answer it" → **five of eleven** (the eleven is unchanged), and "the **three** out-of-contract rows" → **two** (rows 1 and 2; row 4 now wants the new value). |

### B. `internal/e2e/realclaude/trail_run_outcome_test.go`

| # | Site | Change |
|---|---|---|
| B1 | `:102-103` | "POSITIVE ALLOWLIST of **twelve**: three answers and **nine** named voids" → thirteen / ten |
| B2 | after `:182` | **new** `trailOutcomeVoidReasonNotOwedByPath` const + doc (§ Design) |
| B3 | `:273` | `trailIsRunOutcome`'s doc: "one of the **twelve**" → thirteen |
| B4 | `:279-284` | `trailIsRunOutcome`'s switch — add the value |
| B5 | `:385-396` | **C1**: "**six** of them since #1417" and the **published Detail** at `:390-395`, which both counts ("one of the six `trailGate` documents") **and enumerates all six as `%s`** → seven of each |
| B6 | `:497-511` | step 1's note: "these **six** cases are total" → seven |
| B7 | after `:544` | **new step-1 case arm** (§ Design) |
| B8 | `:548` | "none of the **nine** voids does" → ten |
| B9 | `:846` | `// --- the nine voids ---` → ten |
| B10 | after `:696` | **new** `trailRunPresentOwesNoneReadings()`, mirroring `trailRunAbsentOwesNoneReadings()` (`:674-696`): built by driving the **real** gate over `trailGateUsableScan()` at `tdnRunnerFromArgv(tdnFixtureStreamArgv)`, with `Admit` left **zero** — the shape a correct consumer produces and the only one C4 accepts |
| B11 | in `trailRunCases()`, with the voids | **new row** reaching the new outcome. Carry #1417's comment shape (`:882-890`): the coverage loop at `:1044-1048` errors on any `trailRunOutcomeValues()` member no row reaches, so this row is **mandatory** — and unlike #1417's, it **is** named by this ticket |
| B12 | `:1387-1402` | `trailRunOutcomeValues()` — add the value |
| B13 | `:1408-1416` | the **`len(values) != 12` pin → 13**, and its message (which explains "Twelve rather than eleven since #1417 …") re-derived for thirteen |
| B14 | after `:1323` | **new** `TestTrailRunComposesUnderANamedReasonOnAPathThatOwesNone` (§ Testing strategy) |

### C. The five consumers outside the pair

| # | Site | Change |
|---|---|---|
| C1 | `finding_trailer_evidence_test.go:865` | **`len(distinct) != 19` → 20.** The one **hard red** outside the pair: `finTrailerOutcomeValues()` is `append(trailRunOutcomeValues(), finOutcomeValues()...)` (`:783-785`), so a thirteenth run outcome makes it 20 |
| C2 | `finding_trailer_evidence_test.go:866-869` | the pin's message: "twelve run outcomes and seven staging values" and "Twelve rather than eleven since #1417 …" |
| C3 | `finding_trailer_evidence_test.go:62`, `:261` | prose "twelve" |
| C4 | `finding_staging_gate_test.go:19`, `:21`, `:36`, `:69`, `:608`, `:611`, `:663`, `:670` | prose "twelve" — **`:608` and `:663` are inside `t.Errorf` literals, so they are published strings** |
| C5 | `finding_exit_path_probe_test.go:149` | prose "twelve" |
| C6 | `finding_attribution_fanout_test.go:23` | prose "twelve" |
| C7 | `trail_ptyrunner_composition_test.go:33-35` | "Since #1433 the same fixture under a streamrunner reading reaches the presence arm and `trailGateOutOfContract` instead" — re-point at the new value. The paragraph's *point* (the ptyrunner premise is load-bearing) stays true and gets **stronger**. |

### D. The residual sweep — three forms, over the package

`make check` **cannot compile these files** (`//go:build e2e_realclaude`; `Makefile:41` passes no
such tag), so the sweep is the only instrument that finds a missed prose count.

Run it over `internal/e2e/realclaude/`, **not** over the two files above, in **three** forms —
number-words alone do not close it, because `\bfour\b` does not match `fourth` and the ordinal form
carries live claims:

1. **number-words** — `twelve`, `nine`, `six`, `seven`, `four`, `five`, `thirty-seven`, `nineteen`
2. **numerals** — `12`, `19`, `\b6\b`, `\b4\b` (the `len(...) != N` pins live here)
3. **ordinals** — `twelfth`, `fourth`, `third`, `sixth`, `fifth`. `:2370`'s *"that fourth value"* and
   `:704-706`'s *"the third of them"* are both live and both go stale.

Known live ordinals are already in the manifest (A11, A29). The sweep's job is the ones that are
not. Use the `rg`/Grep tool, never a bare `rg -r` (that is `--replace`, not recursive, and silently
rewrites matches).

### E. The cite-renumbering sweep — last step before commit

`trailer_admissibility_test.go` and `trail_run_outcome_test.go` both gain substantial content, and
**every cross-file cite into them below an insertion point moves.** #1417 needed two follow-on
commits for exactly this (`d9e31f2`, `1ab6fbe`), both driven by code-review FAILs; #1433 took one
FAIL/rework round on the same thing. Budget for it explicitly.

Three cite forms exist and a single grep catches only the first:

1. `<file>.go:NNN` — filename-anchored
2. bare `(:NNN)` — **inherits the last `.go` file named to its left**, which is frequently a
   *different* file from the one the symbol lives in. `1ab6fbe`'s whole subject.
3. `<TypeName>:NNN` — symbol-anchored; evades both a filename grep and a bare-ref scan.

Method, from #1433's own lesson: **re-derive each cite from its `HEAD` number through a freshly
computed `HEAD`→working-tree line map, never from whatever number currently sits in the file** —
that is the idempotent form, and it is what makes a re-run after further prose edits correct rather
than doubly-shifted. Verify by content equality: the target text at `main@old` must equal the target
text at `branch@new`. Run it as the **last** step before commit, after all prose is final.

When a bare `(:NNN)` cite is fixed by naming its file, **that edit lengthens the line and can
displace cites below it** — sweep that displacement in the same commit (#1433's code-review MUST
FIX). Where a paragraph is a cite *target* itself, rewrap it to its original line count rather than
adding a line.

### Not in scope for the developer

- `docs/knowledge/features/e2e-realclaude.md` carries live counts that go false — `:883` ("exactly
  one of twelve outcomes (three answers, nine named voids)") and `:969` ("the twelve's `run-`"). It
  is **documentation-phase-owned**; flagged here so it is not missed, not as a developer AC.
- `docs/knowledge/codebase/1434.md` — documentation phase writes it from this spec plus the merged
  diff. Not a developer deliverable.

---

## Testing strategy

Everything runs offline. **`make check` proves nothing about this diff, including that it
compiles** — every file under `internal/e2e/realclaude/` carries `//go:build e2e_realclaude` and
`make check` passes no such tag. Run the tagged suite and **read the counts**, not the exit code:

```
go test -count=1 -tags e2e_realclaude -run '^TestTrail' ./internal/e2e/realclaude/
```

155 run / 155 pass / 0 skipped / 0 failed at `a4506b2`. Also run the package untagged-filter-free
(`-run ''`) once before commit, because the C-series edits are outside `^TestTrail`.

### The new composition test

`TestTrailRunComposesUnderANamedReasonOnAPathThatOwesNone`, mirroring
`TestTrailRunComposesUnderAnAbsentReasonOnAPathThatOwesNone` (`:1207-1323`). Scenarios, as bullets —
the developer writes them in the file's idiom:

- **Premise, from the real producers.** Drive `trailGate` over `trailGateUsableScan()` at
  `tdnRunnerFromArgv(tdnFixtureStreamArgv)`; require `trailGatePresentOwesNone`. Fatal if not — a
  different arm makes every later assertion a statement about something else.
- **It certifies nothing.** `gate.Reason` must be empty; a reason here would trip C2 rather than
  reach step 1.
- **The composition.** `trailClassifyRun(trailRunPresentOwesNoneReadings())` reaches
  `trailOutcomeVoidReasonNotOwedByPath`; gate provenance is the new gate value; `Admit` is empty
  (the gate certified nothing, so the predicate was never owed a call); the Detail names the gate
  answer that decided; the Detail is not truncated.
- **The vacuity guard — a controlled experiment, not a doc claim.** *Control:* the same two tails
  under a **usable** gate reach two **different** outcomes (`MatchCount 1` →
  `trailOutcomeMatchedUnattributed`, `MatchCount 0` → `trailOutcomeNoRowMatched`). Fatal if they
  agree — the experiment rests on them disagreeing. *Experiment:* the same two tails with only the
  gate answer swapped both reach the new outcome, while `MatchCount` is still reported as
  provenance. Invariance across tails that **provably** differ is what shows step 1 decided.

**Two shapes are explicitly refused, and the doc must say why:**

- **Do not hand the classifier `trailAdmitProof` beside the new value.** C4 (`:423-431`) and C5
  (`:433-443`) reject that pair *upstream of step 1*, so such a test re-proves the contract block
  and says nothing about the arm.
- **Do not assert the value "cannot reach step 2".** That is unreachable for **every**
  non-certifying value and would pass against a build with no arm at all.

### Why it is not a row of `trailGateCases()`

Every row there carries one runner path and is swept under all five readings by
`TestTrailGateReadsTheRunnerPathOnlyWhereARowDeclaresIt`, which checks each row's declared `want` at
reading 0 (ptyrunner). A row wanting the new value would fail that premise, where the fixture
correctly reaches `trailGateUsable`. `trailGateCase.want` is one value and the new value is reachable
at one of the five readings. Hence a driver of its own — and hence
`TestTrailRunComposesWithGateCases`' want map gains **no** entry (its coverage loop goes red on an
entry no fixture produces). Same reasoning as `:1213-1222`.

### The mutants this slice must discriminate, and the ones it must not

This slice ships a **deliberate coverage floor**, not a matrix. The full reason × reading table is
**#1428's**, which is blocked by this ticket. Do not build it here.

| Mutant | Sole red |
|---|---|
| the new arm certifies a reason | the presence driver's P1 `wantReason` assertion (`:1786-1791`); C2 is a contract a layer up, not an observed red |
| the classifier arm fails to decide (arm deleted from step 1) | the new composition test's first assertion, reading `run-matched-not-attributed` — a scan-side answer about pyry from a record the gate says certifies nothing |
| the new value awarded on a **ptyrunner** reading | the runner-path companion at reading 0 (A27), the sweep's usable-row premise, and `TestTrailComposesUnderAPtyrunnerReading` |

**Demonstrate the second one under `go test -overlay=<abs-path json>`** rather than asserting it —
the overlay gives a mandated RED with no worktree writes. Record the demonstration in the test's doc,
in the shape `:1234-1242` already uses. Note the arm-deleted mutant lands **one step past** the
retired "falls through to step 2" shorthand: C4 forces `Admit` empty, so step 2 cannot fire and the
fall-through is to steps 3-8.

### What does not move — verified, do not amend

- **`trailGateCases()` gains no row** and `TestTrailGateThenAdmit`'s `calls != 2` (`:2392`) and
  `TestTrailRunComposesWithGateCases`' want map are **unamended**. Every row carries
  `trailRunnerUnread()`, which names no runner, so the new arm cannot fire there. The new value
  joins the non-certifying switch (A29) as a **defensive** arm rather than an exercised one.
- **`trailGate`'s return-site total stays eleven.** This ticket changes what a shipped site answers
  and adds none.
- The four `internal/agentrun/streamrunner/` cites are exact at `a4506b2` — that package was
  untouched by #1433 and is untouched here.

### The commit message

State **why each closed set grew**, against the standing rule that they do not: a gate value with no
arm in step 1's default-less switch falls through to steps 3-8 and awards a scan-side answer about
pyry from a record the gate says certifies nothing, and
`TestTrailAdmissibilityConstantsAreClosed` catches a colliding value, never an unhandled one.

---

## Sizing

`size:s`, held rather than escalated. The measurement, because the ~600-line red line is close
enough here that it needed one:

**Nearest analogue — #1417 (PR #1423), the identical move on the absence side.** Headline 1348/353
across 19 files, but that includes 378 lines of spec, 196 of knowledge doc and 32 of feature doc —
none of them developer work under this pipeline's rules. **Code only: 742 insertions / 346 deletions
across 17 package files** (`trailer_admissibility_test.go` +345/-151, `trail_run_outcome_test.go`
+221/-25, fifteen others +176/-170, almost entirely cite renumbering).

**This ticket against that floor.** Strictly *less* construction: the arm, its driver
(`TestTrailGateNamesThePresenceCaseOnAPathThatOwesNone`), the marker helper and the scan fixture all
exist — #1433 built them. Strictly *more* prose rewriting: an 82-line arm doc, a 55-line driver
argument that **inverts** rather than merely renumbering, and four byte figures to re-measure. Net,
comparable: ~600-750 code insertions. An independent bottom-up count (220 new + ~264 rewritten +
~80 count edits + ~60 cite sweep) lands at ~625.

**Red lines, checked:** 0 new files (limit 3); 0 new exported types (limit 5) — both new constants
are unexported test constants; 3 acceptance criteria (limit 5); no signature change and therefore no
consumer call-site cascade (the ~55 edit sites are additive prose and count edits, not a refactor
fan-out); 1 new switch arm, not a reject-branch fan-out. The spec's deterministic gate — production
source files excluding `*_test.go` — counts **zero**: every file here is a `_test.go`.

**Seam check, run against the code rather than the ticket's prose. There is no valid seam:**

- *gate value ‖ run outcome* — **forbidden.** `trail_run_outcome_test.go:512` is a switch with no
  `default` by design; an unhandled gate value falls to steps 3-8 and awards a scan-side answer
  about pyry from a record the gate says certifies nothing. Verified in the source, not taken from
  the ticket.
- *behaviour ‖ count sweep* — **forbidden.** `finding_trailer_evidence_test.go:865`'s
  `len(distinct) != 19` goes red the instant the thirteenth outcome exists, so the first child would
  not be green; and deferring the prose counts ships a knowingly-false tree for a merge window. A
  cut that leaves a false comment is not a cut.
- *Detail rewrite ‖ value flip* — **forbidden.** The shipped Detail literally ends *"a value of its
  own is #1434"*.

The ticket is already the product of two splits — #1427 → #1433 (the arm) + #1434 (the value), with
the reason × reading matrix carved out to #1428 — and this is the irreducible middle slice, whose
direct precedent measured 742 code insertions and landed inside the developer budget.

**Where the turns actually go, and the mitigation.** The binding cost is ~55 edit sites plus the
cite sweep, not new construction. That is why this spec pre-computes the complete line-numbered
manifest (§ The edit manifest) — the discovery cost is paid here, at architect time, rather than in
20-30 turns of developer greps.

---

## Open questions

1. **Constant names.** `trailGatePresentOwesNone` / `trailOutcomeVoidReasonNotOwedByPath` are
   chosen to satisfy the both-ways containment constraint against every marker they could shadow. If
   the developer prefers different names, the containment check in § Design must be re-run
   mechanically — equality (which the union map checks) is not sufficient, because the drivers
   assert with `strings.Contains`.
2. **Whether the new gate constant needs a containment guard in code**, alongside the existing
   `trailReasonPresentOwesNone` guard at `:1689-1701`. The composition test asserts
   `strings.Contains(got.Detail, trailGatePresentOwesNone)`, and that assertion goes vacuous if the
   constant is ever renamed into a substring relationship with `trailGateAbsentOwesNone`. A guard is
   cheap; whether it earns its line is the developer's call, and either answer should be stated in
   the test's doc rather than left silent.
3. **Whether `trailGateOutOfContract`'s enumeration should record the removal explicitly.** #1417's
   removal is recorded in the landing-order history at `:155-164`; this one should be too. Whether
   the history sentence has room to keep growing, or should be compressed as it takes its fifth
   entry, is a judgement left to the developer — the constraint is that the count stays derived from
   the arms that remain.

---

## Security review

**Verdict:** PASS

**Findings:**

- **[Trust boundaries] No findings, and the ticket's whole subject is one.** `terminal_reason` on a
  stream-path trailer is **untrusted**: `streamrunner.Run` tee-parses claude's stdout and passes the
  bytes through unchanged (`internal/agentrun/streamrunner/runner.go:177-179`), so claude's own
  output reaches the reading. The boundary is explicit and single: `trailReasonAgainstPath`
  (`trailer_terminal_reason_test.go:222`) is **called, never re-switched**, and presence comes from
  the key names (`:223`) and never from `decodedReason != ""`. The spec's binding constraint —
  requirement (3) of the constant's doc, and the `"NEVER that pyry wrote it"` phrase the P1 row
  already asserts (`:1736`) — is precisely that the new value may **not** claim provenance across
  that boundary. A value claiming pyry wrote the line would let claude's output name pyry as its
  author. **This is the finding the whole design is shaped around, and it is enforced by an assertion
  rather than by prose.**
- **[Error messages, logs, telemetry] No findings — this is the second live surface and it is
  fully covered.** `trailGateResult` is a **published** record. The spec forbids the new Detail from
  interpolating the decoded reason (an untrusted scalar) or any key name (unbounded and
  attacker-influenced in principle, `trailer_key_names_test.go:55-59`), and requires it to stay fixed
  prose over this file's own constants and file cites. Enforced deterministically by
  `TestTrailAdmissibilityRecordsCarryNoCapturedBytes`'s fourth row (A32), which plants the 42 B
  `trailNeedle` in the key-name channel at a streamrunner reading — the one combination that reaches
  this arm — and marshals the record. Belt-and-suspenders of **different fabric**: the byte sweep is
  deterministic code, not a second stochastic rule.
- **[Error messages] SHOULD FIX — the truncation channel, already instrumented, must not regress.**
  `reachCapCommand` **truncates and marks** rather than failing
  (`background_reach_probe_test.go:945-950`), so a Detail that outgrew its budget ships a severed
  sentence that can still satisfy a marker assertion — and, worse, could lose a planted needle *in
  the cut* rather than by never quoting it, making the leak sweep read clean for a reason it does not
  claim. The mitigations are shipped and this spec keeps them: headroom is asserted **on the output**
  at `:1827-1834` and `:2614-2619` against the arm's real **470 B** ceiling (512 B cap less the 42 B
  needle), not the 512 B cap. **The developer must re-measure the rewritten Detail and update
  `:1678-1681` and `:2526-2529` rather than carrying 445 B forward** — a stale byte figure beside a
  live assertion is the shape that goes quietly wrong. Code-review should check the figures against
  the shipped output.
- **[Subprocess / external command execution] Not applicable by construction, and it is worth
  stating positively rather than as an absence.** `trailGate`, `trailReasonAgainstPath` and
  `trailClassifyRun` are **pure**: no `exec`, no clock, no filesystem. `trailGate` calls no argv
  reader at all — not `tdnClaudeCommand`, not `reachProc.Command`, not `pinScan.Matches`, not
  `reachRunnerPathFromArgv` — and `trailGateInput.RunnerPath` holds `tdnRunnerFromArgv`'s **reduced
  output**, one of five constant answers, never its argv input
  (`trailer_admissibility_test.go:218-228`). Verbatim argv has no place to land on any record this
  ticket touches. This ticket adds no reader and no field, so the property is preserved rather than
  re-established.
- **[File operations] Not applicable.** No path is constructed, opened or written. Every fixture is
  in-memory; the tagged suite runs with no credentials, no daemon, no env gate and no `t.Skip`.
- **[Cryptographic primitives] Not applicable.** No randomness, no comparison against a secret, no
  key material. The one `strings.Contains` discipline that matters is a **vacuity** concern, not a
  timing one, and it is handled as Open Question 2.
- **[Network & I/O] Not applicable.** No socket, no reader, no deadline. The only size bound in play
  is the 512 B `reachMaxCommandBytes` Detail cap, covered above.
- **[Concurrency] No findings, one active obligation.** All three functions are pure and take no
  lock. The single race surface is fixture aliasing: `trailRunReadings.Liveness` is a slice and
  `go test -race` runs this package's tests in parallel, so **`trailRunPresentOwesNoneReadings()`
  must be a function and not a package-level `var`** (B10), matching `trailRunWellFormed()`'s stated
  reason (`trail_run_outcome_test.go:650-651`). A shared backing array would let one test's mutation
  reach another's. Called out in § Design § Concurrency as a requirement, not left to idiom.
- **[Threat model alignment] No findings.** The relevant threat is the family's own: a probe record
  destined for a public issue must be publishable **unreviewed**, so it may carry no operator-review
  obligation — no command column, no verbatim argv, no quote of claude's output or pyry's stderr.
  This ticket adds one constant to a published record's `Value` field and rewrites one `Detail`;
  both stay inside the shipped rule, and both are checked by the existing byte sweeps rather than by
  review.

**Reviewer:** architect (self-review per `architect/security-review.md`)
**Date:** 2026-08-10
