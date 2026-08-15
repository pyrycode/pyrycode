# #1462 — `finGatherReadings` carries the sighting route's certified ordering from its caller

**Ticket:** [#1462](https://github.com/pyrycode/pyrycode/issues/1462) · **Size:** S · **Split from:** #1457 (CLOSED)
**Package:** `internal/e2e/realclaude` (behind the `e2e_realclaude` build tag)
**Offline throughout.** No live claude, no credentials, no `make e2e-realclaude`.

---

## Files to read first

Symbols, not line numbers — resolve each with `codegraph_search` / `codegraph_node`, then Read the declaration.

| Where | Symbol | What to extract |
|---|---|---|
| `finding_run_gather_test.go` | `finGatherInputs` | The struct **and its whole doc block**. The doc block is AC 5's entire subject; the struct is where AC 1's field is appended. Note the per-field doc shape: producer rule, zero-polarity argument, travels-whole argument. |
| `finding_run_gather_test.go` | `finGatherReadings` | The "caller's own three readings, carried WHOLE" block near the end — where AC 1's carriage line goes. Read the paragraph above it: it is why nothing is normalised, defaulted or re-decided at this tier. |
| `finding_run_gather_test.go` | `TestFinGatherHalfStagedRouteMovesNoOutcome` | AC 3's subject. Its doc asks to be revisited here. Its body is also the **recipe** AC 2 must reuse — seed, needles, `RunnerPath`, and the `Fatalf` gate premise. |
| `finding_run_gather_test.go` | `finGatherPinnedReadings` | The file's own idiom for pinned-read fixture shapes, and its doc's SHAPES-not-verdicts argument. AC 2 needs two verdicts this set does not carry. |
| `finding_run_gather_test.go` | `finGatherSeed`, `finGatherNeedles`, `finGatherPinnedReadPID` | The three helpers AC 2's rows are built from. `finGatherNeedles` returns a `t.TempDir()` path nothing is staged at — that is what makes the argv leg deterministic. |
| `trail_run_outcome_test.go` | `trailClassifyRun` | The `trailGateAbsentOwesNone` arm: the step-1 ordering guard, the established arm, the unestablished arm, the void fall-through. Also holds **two** AC 4 sweep sites — the no-C10 note and the "Testing both fields with `&&`" note. |
| `trail_run_outcome_test.go` | `trailRunReadings` | The `Ordering` field's own doc — the contract AC 1's new field feeds. Note it already says the value is taken WHOLE. |
| `trail_run_outcome_test.go` | `trailOutcomeVoidSightingRouteNotStaged` | Its const doc carries two AC 4 sweep claims that resolve **in opposite directions**. |
| `trail_run_outcome_test.go` | `trailRunAbsentOwesNoneReadings`, `trailRunSightingEstablishedReadings` | Two AC 4 sweep sites, and the classifier-tier fixtures whose `trailCertifyOrdering(true, true, true)` call AC 2 mirrors at the gather tier. |
| `trail_run_outcome_test.go` | `trailRunCases`, `TestTrailRunComposesUnderAnAbsentReasonOnAPathThatOwesNone` | Two more AC 4 sweep sites. |
| `trail_run_outcome_test.go` | `TestTrailRunComposesUnderANamedReasonOnAPathThatOwesNone` | An AC 4 sweep **hit that is not an edit** — see § Sweep, group C. |
| `trail_sighting_liveness_test.go` | `trailEstablishSighting` | The consumer. Its guard (`ordering.Value != trailOrderCertified`), its three verdict arms, and the "passed through WHOLE and never re-decided here" sentence AC 1 cites as the reason the field is not narrowed. |
| `trail_sighting_liveness_test.go` | `trailSightingPin` | Read it to know **not** to use it — see § Design, "The pin fixture". |
| `trail_ordering_premises_test.go` | `trailCertifyOrdering`, `trailOrderResult` | The producer and its output type. Its signature is three `bool`s — the whole basis of this ticket's captured-bytes posture. Its doc's "premises are supplied, not recovered" note is an AC 4 **verify-no-edit** site. |
| `process_pin_liveness_test.go` | `pinClassifyState` | The arms that fill `pinStateExitedNotReaped` and `pinStateNoSuchProcess`. AC 2's two refutation rows must carry those arms' real shapes, not a verdict string with everything else zero. |
| `CODING-STYLE.md` § "Comments — Citing Other Code" | — | Cite the symbol. `make cite-guard` is a build gate. |

---

## Context

`trailClassifyRun`'s `trailGateAbsentOwesNone` arm consults the pinned-pid sighting route. Its step-1 guard is **single-sided**: it reads `readings.Ordering.Value` alone and, when unfilled, answers `trailOutcomeVoidSightingRouteNotStaged` before the route is consulted at all.

`trailRunReadings` has carried an `Ordering` field since #1439/#1446. What has never existed is a way for a **gather** to fill it. #1458 staged the route's other input — the pinned-pid read — and no outcome moved, precisely because the guard reads the ordering alone. So today every live run through `finGatherReadings` reaches the arm **half-staged**: pin filled, ordering unfilled, answer unchanged.

This ticket adds the field the ordering arrives on, carries it whole onto `trailRunReadings.Ordering`, and proves offline that a certified ordering unblocks the arm — reaching `trailOutcomeAliveAtSightingByOrdering` and `trailOutcomeVoidPinnedPidDidNotEstablish`, two outcomes reachable today only from hand-built classifier fixtures.

### It wires no live caller, and that is structural

`trailCertifyOrdering` needs `trailerSighted`. The trailer is sighted by `trailWaitForTrailer`, whose two non-test call sites are inside `finGatherReadings` itself and inside `trailRigGather`. **Neither hands its caller the sighting before the call the premise would have to be passed to** — `finGatherReadings` reports the state on its third return, `trailRigGather` returns the observation, both *after*. Recovering the premise with an earlier `trailWaitForTrailer` at the call site degrades the gather's own observation: the later of two calls matches on its first poll and reports `trailBoundFromStart`, a discriminator whose own doc says it BOUNDS NOTHING — and here the degraded one would be the observation that fills the published `BoundFrom`, the `Staleness` and the gate.

Resolving that circularity is design work for a ticket that **does not exist yet**. `finExitRunProbe` is left untouched. #1458's precedent — wire the probe at step 3b — points the wrong way here and must not be followed.

---

## Design

### 1. The field

Append to `finGatherInputs`, **after `PinnedPid`, as the struct's last field**:

```go
// Ordering is the sighting route's certified-ordering result, PRODUCED AT THE
// CALL SITE by trailCertifyOrdering and handed in whole.
Ordering trailOrderResult
```

Placement is not cosmetic. Inserting mid-struct displaces every in-file bare `:NNN` reference below it — the tail that burned #1452's budget and cost #1458 four of its five code-review rounds. Appending puts the insertion below every field.

Because every `finGatherInputs` literal in the package is **keyed** (14 sites; verified none positional), appending a field requires **zero call-site edits**.

The field's doc block must carry four claims, in the shape its neighbours already use:

- **One admissible producer: `trailCertifyOrdering`'s own output.** Never a `trailOrderResult{Value: trailOrderCertified}` literal. A hand-built certification lets everything downstream pass against an ordering predicate that certifies nothing — the bypass `trailSightingPremisesFor` closed on the fixture side, and the reason `trailRunSightingEstablishedReadings` calls the real predicate rather than typing the value.
- **It travels WHOLE.** Not narrowed to its `Value` string. `trailEstablishSighting` takes a `trailOrderResult` whole and says so in its own body ("passed through WHOLE and never re-decided here"); a narrowed field would have to be reconstituted by literal at the point of use, which is the same bypass one step along. This is `PinnedPid`'s travels-whole argument, and the field doc should say so rather than restate it from scratch.
- **The zero is admissible and degrades to a named reading.** A zero `trailOrderResult` carries `Value ""`, which is exactly what the arm's step-1 guard reads to answer `trailOutcomeVoidSightingRouteNotStaged`. An unfilled field degrades to a named not-staged reading, never to a claim — the same zero-polarity argument the other four staged fields make.
- **No live caller fills it, and none may be added here.** State the circularity from § Context in one or two sentences and say the resolving ticket does not exist yet. Do **not** name a number. Name the consequence too, because it is what makes the prohibition more than style: a probe that recovered `holdHeld` by guessing would publish `run-alive-at-sighting-by-ordering` on a run where the hold was released — and a released hold means the pinned pid could have been retired and reissued between the sighting and the later read, so the artifact would name a process it cannot show is the same one. That is precisely the case `trailOrderVoidUnheld` exists to refuse, and it outranks the other two premises for that reason.

**Captured-bytes posture — say it here.** `trailCertifyOrdering` takes three `bool`s, so no captured byte reaches a `trailOrderResult` through its producer at all. That is why this field needs no producer prohibition of the kind `RunnerPath` and `PinnedPid` carry ("NEVER a raw ps column", "NEVER `finLivePinReading.ClaudeCommand`") — there is no argv-shaped or `ps`-shaped value that could arrive here through the admissible route.

Two further fences already ship, and they are why the gather-tier sweep can be **#1463's** without leaving a gap open in between:

- `trailEstablishSighting` is forbidden from quoting `ordering.Detail`, and `TestTrailSightingResultCarriesNoCapturedBytes` **already plants the needle into `ordering.Detail`** and asserts the marshalled `trailSightingResult` does not carry it. The sighting tier is swept today.
- `trailClassifyRun` composes `trailRunOutcome` from `Gate.Value`, `Admit.Value`, `MatchCount`, `RowsScanned`, `BoundFrom`, `tdnVerdictSummary(Liveness)` and `ClaudeState`. It **never renders `Ordering` at all**. Verify this before writing the field doc rather than taking it from here.

So the deferral is safe on measured grounds rather than assumed: no producer can emit captured bytes into the field, no consumer quotes its `Detail`, and no published record renders it. #1463 fences a future field and needs this route to exist first — a sweep whose needle has no route to travel measures nothing.

### 2. The carriage

One line in `finGatherReadings`, in the block that today carries three readings:

```go
readings.Ordering = in.Ordering
```

Whole-value assignment. No default, no zero-value rewrite, no fill-in-if-unset, no normalisation, no `trailIsOrderValue` call. The block's existing comment argues this for `ClaudeState` and `PinnedPid`; it becomes four readings and the comment's "three" must move with it.

**Do not certify inside the gather, even though it looks possible.** The gather already holds two of `trailCertifyOrdering`'s three premises: `in.PyryExited` is an input, and the gather sights the trailer itself (`obs.State == trailSeen`). `holdHeld` is the one it cannot know — it is a fact about a FIFO the caller holds. A certification computed from two premises and a guess is worse than none, and splitting one certification across two sites is how two computations required to agree stop agreeing — the argument this same function already makes about its own key-name cap.

### 3. The `finGatherInputs` doc-block sweep (AC 5)

Six edits in one block. A **numeral-and-ordinal** sweep is owed, not a word sweep: `\bfour\b` does not match "fourth". The sites do not all resolve the same way.

| Site (in the doc block) | Today | After | Why |
|---|---|---|---|
| "All four staged fields keep the readings' own names, types and ZERO-POLARITY" | four | **five** | The staged set gains `Ordering`. |
| "carries the closing sentence below over a fourth field" | fourth | **fifth** | Same set, ordinal form — this is the site a word sweep misses. |
| "Because all four zeros land on a named nothing-was-measured" | four | **five** | Same set. |
| The four zero-polarity bullets | 4 bullets | **5 bullets** | `Ordering` earns one, and it has an argument: its zero `Value ""` is what the step-1 guard reads to answer `run-void-sighting-route-not-staged`. |
| Opening enumeration: "#1281's four values, the two #1282 left staged…, the runner-path reading #1452 routed in…, and the pinned-pid read #1458 routed in from the same place" | 8 fields described | **9** | See below. |
| The eight-argument positional shorthand (prose count + the illustrative call) | eight | **nine** | See below. |

**The opening enumeration is the trap.** Its count literal — "#1281's four values" — is a **historical attribution** and stays TRUE; #1281 did contribute four values. What goes stale is the *list*: its clauses sum to exactly the eight fields shipped today, so after this change the sentence describes eight fields of a nine-field struct. It gains a ninth clause in the same `#NNNN routed in from …` form its last two clauses already use. Do not touch the "four".

**"Routed in" is not "staged" — and AC 4 turns on the difference.** The new clause says #1462 *routed the field in*, which is true: this ticket adds it. It must not say #1462 *stages* the ordering, which is false — no live caller fills it. AC 4 forbids exactly that second sentence, and the enumeration clause is where a developer is most likely to write it by reflex.

**The eight-argument shorthand: update both halves to nine.** Decided explicitly, not by omission. The prose argues *from* the count ("Across eight positional arguments that identity is a discipline retyped at each call site"), so the count must be live rather than a historical snapshot — a reader cannot tell which it is otherwise, and the argument gets *stronger* at nine. The illustrative call gains a ninth positional argument in field order (the ordering last), which also strengthens its closing point about a reader having no way to know what an opaque positional asserts: there are now two struct-valued positionals, not one.

### 4. The comment sweep (AC 4)

Two claims live in these sites and **they are no longer the same claim**:

- **Claim A — "no gather stages the sighting route's ordering" / "`finGatherInputs` carries no field the ordering could arrive on."** Goes **flatly FALSE**. A gather now carries a field the ordering can arrive on.
- **Claim B — "the not-staged answer is what every live run reaching this arm produces."** Stays **TRUE**, because no live caller fills the field. It must **not** be "corrected" into a claim that live runs now reach the finding.

Most sites carry both, welded into one sentence. Splitting them is the work.

**Re-measure; do not inherit the list.** The ticket's own list was measured at `3ade615`. Grep the discriminating substring `gather` — **in both files**, since #1458 put two sites outside `trail_run_outcome_test.go`. Do not grep any one phrasing: each site words the claim differently. Expect the sweep to surface hits that are *not* edits (group C) — classify every hit rather than assuming the count.

**Group A — sites in `trail_run_outcome_test.go`, by enclosing symbol:**

| Enclosing symbol | What moves |
|---|---|
| `trailOutcomeVoidSightingRouteNotStaged` (const doc) | Two claims. The "no shipped gather fills the ordering" sentence goes false; the "every run reaching this arm lands here" sentence survives and needs its basis restated. Also carries a stale **#1457** attribution. |
| `trailClassifyRun` — the no-C10 note | The load-bearing site. See below. |
| `trailClassifyRun` — the "Testing both fields with `&&`" note | "#1457 has not yet staged the ordering" goes stale as attribution; the half-staged pair is still what every live run produces, and that is the sentence's actual point. |
| `trailRunAbsentOwesNoneReadings` (doc) | The never-staged fixture's basis: it stays the fixture that proves an unstaged pair lands on a reading. |
| `trailRunSightingEstablishedReadings` (doc) | "No shipped gather stages the ORDERING — #1458 gave the finding gather the pin half and #1457 owes the other." Both halves move: a gather now *carries* the ordering, and #1457 is closed. |
| `trailRunCases` — the unstaged-sighting-route row's comment | Same shape: what a shipped gather produces has moved; the row deliberately keeps the empty pair. |
| `TestTrailRunComposesUnderAnAbsentReasonOnAPathThatOwesNone` (doc) | Same. The fixture stays unchanged and deliberately so. |

**Group B — sites in `finding_run_gather_test.go`:** `TestFinGatherHalfStagedRouteMovesNoOutcome`'s doc — see § 5, which subsumes them.

**Group C — hits that are NOT edits. Verify, expect no change:**

- The two-tier plant note inside `TestTrailRunComposesUnderANamedReasonOnAPathThatOwesNone`: "Until that ticket the sentence was free: no shipped gather filled either input." Past-tense **history** about the basis #1458 moved; true as history. The ordering side stays clean for a reason of its own rather than by luck — `trailCertifyOrdering` takes three `bool`s, so no captured byte reaches a `trailOrderResult` through its producer at all. The block's two-tier claim is about the pin.
- `trailCertifyOrdering`'s doc, "The premises are supplied, not recovered" (`trail_ordering_premises_test.go`): its claim is that no gather records the `holdHeld` **premise**. This change makes it *more* true, not less — the premises go to `trailCertifyOrdering` at the call site and `trailRunReadings` gains no premise field.

**Five sites name #1457 as the ticket that will stage the ordering.** #1457 is CLOSED — it is this ticket's parent. The attribution is stale, and the obvious repair, "#1462 stages it", is precisely the correction AC 4 forbids. **No ticket for the live wiring exists yet.** These sites should name the carriage as landed and the staging as still unowned, rather than point at a number. Inventing a placeholder number is worse than naming no number.

#### The no-C10 note is where the conclusion is actually at risk

Inside `trailClassifyRun`. It argues that no contract check exists over `Ordering` or `PinnedPid` because such a check "would answer run-out-of-contract ON EVERY RUN THAT EXISTS TODAY". Its supporting sentence — "`finGatherInputs` still carries no field the ordering could arrive on" — **goes flatly false**, and it is the last premise on the ordering side.

The conclusion still stands, and on the note's own next paragraph rather than on a new argument: the ordering side is decided by the **step-1 guard** rather than by a contract check, and that is "a READING and not a contract violation". This is the same move the note already made on the pin side when #1458 landed ("the premise has moved and the conclusion has not").

**Do not add a C10.** State what is now true about why one is still not owed. `trailRunAbsentOwesNoneReadings` remains the fixture that proves an unstaged pair lands on the reading rather than on `run-out-of-contract`.

### 5. Revising `TestFinGatherHalfStagedRouteMovesNoOutcome` (AC 3)

The test's rows do not change. Its **doc** does, and one assertion gains a stated meaning.

Today `Ordering` is unfilled **structurally** — no field exists. Afterwards it is unfilled **by choice**. The test must state which of the two it is now proving, so a reader cannot mistake a compile-time impossibility for a checked property. Its own doc already asks to be revisited here.

- The doc's "Ordering is unfilled AUTOMATICALLY: `finGatherInputs` has no field for it yet. When #1457 adds one, this test needs revisiting" paragraph is the one being discharged. Replace it: the field exists, the rows leave it zero deliberately, and the existing `readings.Ordering.Value != ""` premise stops being a tautology and becomes the check that the rows really are half-staged.
- The doc's "#1457 has not yet staged the ordering" sentence carries the same stale attribution as group A.
- The claim itself is unchanged and must stay: a caller filling one input without the other must not defeat the single-sided guard. #1448 chose that guard for this reason.
- The row supplying **neither** input is already driven (`unstagedReadings`) and still answers the same value. Keep it.

Do not weaken this test into a compile-time observation. It is now a **behavioural** check with a field that could have been filled, which is strictly stronger than what it was.

### 6. The new test (AC 2)

`TestFinGatherStagedOrderingReachesTheSightingOutcomes` (name is a suggestion; match the file's `TestFinGather<Subject><Claim>` idiom).

**The recipe is not optional.** Reaching `trailGateAbsentOwesNone` at all needs what `TestFinGatherHalfStagedRouteMovesNoOutcome` already uses: seed `trailKeyNamesNoTerminalReason()` — a trailer with no `terminal_reason` key at all — under `RunnerPath: tdnRunnerFromArgv(tdnFixtureStreamArgv)`, which reduces to streamrunner. Without **both** halves the gate lands elsewhere and every assertion is about a different arm. That is why the gate value is a `Fatalf` premise and not a corroboration.

**The ordering comes from the shipped producer:** `trailCertifyOrdering(true, true, true)`. Not a literal. The three `true`s are the caller's premises, and the fixture is honest about them — the trailer *is* sighted (the gather sights it), pyry *did* exit (`PyryExited: true` in the base), and `holdHeld` is the caller's own fact that no gather can recover. Say that in the test's doc; it is the same "supplied, not recovered" statement `trailCertifyOrdering`'s doc makes.

**The pin fixture: not `trailSightingPin`.** `finGatherInputs.PinnedPid`'s doc rules it out by name — it "sets no `StateColumn` and no `ToolStderr`", precisely the shape a live *failing* read produces. The classifier-tier fixtures may use it; this tier may not. Build each row's read the way `finGatherPinnedReadings` builds its shapes, and match what `pinClassifyState` actually fills on each arm:

- `pinStateRunning` — `PID`, `PPID`, a live `StateColumn`, `Detail`. `finGatherPinnedReadings` already carries this exact shape; reuse it rather than write a fourth spelling.
- `pinStateExitedNotReaped` — `pinClassifyState` sets `PPID` and `StateColumn` *before* the zombie check, so a real one carries both, with a zombie state column and `ExitStatus` 0.
- `pinStateNoSuchProcess` — `pinClassifyState` returns before parsing a row, so a real one carries `PID`, a non-zero `ExitStatus` and `Detail`, and **no** `PPID` or `StateColumn`.

Getting these shapes right is not decoration: a row whose pin is a verdict string with everything else zero proves the gather routes a *verdict*, not that it routes the *value*.

**Rows (bulleted scenarios, not code):**

1. *A certified ordering beside a still-running pinned pid establishes aliveness at the sighting.*
   Expect `trailOutcomeAliveAtSightingByOrdering`, route `trailRouteSighting`, route reason `trailSightingReasonPidRunning`.
2. *A certified ordering beside a pid awaiting a reap does not establish it.*
   Expect `trailOutcomeVoidPinnedPidDidNotEstablish`, route `trailRouteSighting`, route reason `trailSightingReasonPidReapedPending`.
3. *A certified ordering beside a pid no longer in the table does not establish it either.*
   Expect `trailOutcomeVoidPinnedPidDidNotEstablish`, route `trailRouteSighting`, route reason `trailSightingReasonPidGone`.

Rows 2 and 3 share an outcome value and differ in reason — which is why the reason is asserted rather than only the value.

**Premises first (each a `Fatalf`, each turning a vacuous comparison into a stop):**

- The gather's `readings.Gate.Value == trailGateAbsentOwesNone`. At any other gate value step 1 answers first and the row is about a different arm.
- `readings.Ordering == in.Ordering` — whole-value equality, not `.Value`. Without the reading having crossed **whole**, the claim below is about a field the gather narrowed.
- `readings.PinnedPid == in.PinnedPid`. Same reason, and it is what makes the row half-vs-fully-staged distinction real.

**Claims, compared against the shipped classifier's output over the row's own readings — never against a typed-in outcome:**

- `trailClassifyRun(readings).Value` equals the row's declared outcome **constant** (never a string literal).
- `.Route` equals `trailRouteSighting`, and `.RouteReason` equals the row's declared reason constant. The arm publishes both; a build that reached the right value by another path would move one of them.
- **The non-vacuity cross-check:** the row's declared reason must equal `trailEstablishSighting(readings.Ordering, readings.PinnedPid).Reason`. This pins that the constant the row declares is the shipped predicate's own answer rather than one that drifted, and it is taken over the readings **the gather produced** rather than over the inputs — so it also witnesses the carriage. Without it, a row could declare a reason the predicate no longer emits and still agree with a classifier that hardcoded the same stale string.

**What the test must not do:**

- No `make e2e-realclaude`, no live claude, no credentials.
- No needle plant and no captured-bytes sweep. That is **#1463's**, and #1452's precedent is that a new route gets a sibling test of its own rather than a third plant on an existing one.
- No cross-row comparison of `RowsScanned` or `MatchCount`. This package makes many `t.Parallel()` calls and re-execs itself, so a sibling's child appearing between two gather calls moves the count. `TestFinGatherPinnedPidDoesNotReachTheLiveness`'s doc states this measurement; per-row is the only honest form.
- No `trailAdmitProof` beside these rows. C4 and C5 would answer first and the test would re-prove the contract block instead of the arm.

---

## Concurrency model

Nothing concurrent is introduced. `finGatherReadings` gains one struct-field assignment; `trailCertifyOrdering` is pure over three `bool`s — no exec, no clock, no filesystem, which is what lets every arm be driven with no live turn.

The one standing constraint the new test inherits: `go test -race` runs this package's tests in parallel and several re-exec the test binary. Fixture builders that return slices are **functions rather than package-level vars** for that reason (`trailRunWellFormed`'s stated rule). The new test's rows carry only scalars and a `trailOrderResult` of two strings, so a table literal is fine — but the shared `probeSyncBuffer` must be per-test, exactly as `TestFinGatherHalfStagedRouteMovesNoOutcome` does it.

---

## Error handling

No error paths are added; nothing here returns an `error`. The failure modes are classification failures, and each has a named landing:

| Failure mode | Where it lands | Why it is safe |
|---|---|---|
| Caller stages nothing | `Ordering` zero, `Value ""` | Step-1 guard → `trailOutcomeVoidSightingRouteNotStaged`. A named reading, not a claim. |
| Caller stages the pin but not the ordering (today's live shape) | Same | The guard is single-sided **by design**. `TestFinGatherHalfStagedRouteMovesNoOutcome` is the net. |
| Caller stages the ordering but not the pin | `trailEstablishSighting`'s reachable `default` arm | `trailSightingReasonPidReadFailed`'s doc names the zero `""` of an unfilled `pinStateOutcome` among the shapes it answers for. Void, never a clean negative. |
| Caller hands a non-empty `Value` nobody defined | Falls past the guard into `trailEstablishSighting`, whose `!= trailOrderCertified` guard answers `trailSightingReasonOrderingUncertified` | Deliberate: the guard is narrower than `!trailIsOrderValue(...)` on purpose. `""` is what an unfilled field carries; an undefined non-empty value is a caller's bug, and filing it as "never staged" would be the #1417 collapse mirrored. **This shape keeps today's answer — do not change it.** |
| Caller hand-builds `trailOrderResult{Value: trailOrderCertified}` | Nothing catches it | Held by the field's doc and by review, exactly as `RunnerPath`'s and `PinnedPid`'s producer rules are. There is no check this type can make. Stated as a statement about the gather rather than a licence for its caller. |

**No contract check (C10) is added.** See § 4's no-C10 note.

---

## Testing strategy

`make check` does **not** compile these files — they sit behind the `e2e_realclaude` build tag. `done:qa` alone does not cover this diff. Verify with:

```bash
go vet -tags e2e_realclaude ./internal/e2e/realclaude/
go test -tags e2e_realclaude -race -run 'TestFinGather|TestTrailRun|TestTrailSighting' ./internal/e2e/realclaude/
make cite-guard
```

No live claude and no credentials are needed for any of these.

**The new test is not exec-free, and a grep for `exec.` will wrongly say it is.** Every `finGatherReadings` call reaches `ps` through `pinScanArgv`, and through `pinReadState` once per matched pid. The new test calls the gather three times, so it runs `ps` three times. No `exec.Command` appears in the test's own body — the calls are inside those two helpers — so a reviewer grepping the test file finds nothing and could conclude the wrong thing. The surface is unchanged from every sibling test in the file, and it carries no caller-controlled argv: the needles come from `finGatherNeedles`, a `t.TempDir()` path nothing is ever staged at, which is also why the argv leg matches nothing and the `pinReadState` loop never runs.

**Coverage this ticket owes:**

- The new test's three rows (§ 6) — the carriage and both unblocked outcomes.
- `TestFinGatherHalfStagedRouteMovesNoOutcome`, unchanged in rows, revised in doc (§ 5) — the guard is not defeated by a half-staged pair, and the empty pair reaches the same value.

**Coverage this ticket does not owe:** the captured-bytes sweep over this route (#1463), and any live-caller wiring (no ticket yet).

**Mutation checks worth running before calling it done** — use `go test -overlay=<abs-path json>` so no worktree writes are needed:

- Narrow the carriage to `readings.Ordering = trailOrderResult{Value: in.Ordering.Value}`. The new test's whole-value `readings.Ordering == in.Ordering` premise must go RED. If it stays green, the premise is comparing `.Value` and the travels-whole claim is unproven.
- Make the step-1 guard `&&` over both fields. `TestFinGatherHalfStagedRouteMovesNoOutcome` must go RED. This is the exact defeat the guard's doc argues about, and it is the row that keeps AC 3 non-vacuous.
- Swap row 2's expected reason for row 3's. The reason assertion must go RED — otherwise the two `trailOutcomeVoidPinnedPidDidNotEstablish` rows are indistinguishable and one of them is dead weight.

---

## Cost notes for the developer

The historical cost driver in this family is the **citation tail**, and for this ticket it is measured rather than assumed:

- `trail_run_outcome_test.go` carries **27 inbound named citations, 21 of them below the first sweep edit point.** That is the displacement risk the ticket's own Technical Note points at.
- **It is not your obligation.** `cite-guard` is diff-scoped: it checks only lines this branch added or modified, and says so in its own failure output. A citation displaced by your edits is out of scope. #1458's five review rounds predate the 2026-08-11 rescoping that fixed exactly this at the source. Do **not** spend turns re-pointing displaced references — that laundering is what cost #1458 a rework round.
- What *is* checked: citations on lines you write or rewrite. The edit regions carry very few — three named citations and two bare `(:NNN` refs across the whole of `finding_run_gather_test.go`'s edit regions, one named citation across all seven of `trail_run_outcome_test.go`'s. Get those right; the guard will tell you.
- `finding_run_gather_test.go` has **one** inbound named citation from `internal/`, above every insertion point. The file gaining the field is not where the tail is.

---

## Open questions

1. **The ninth clause's wording in the opening enumeration.** § 3 fixes the *form* (`#1462 routed in from …`) and the prohibition (not "stages"). The exact phrasing is the developer's, and it must survive AC 4's own sweep — i.e. it must not become a site that claims live runs now reach the finding.
2. **How the five stale #1457 attributions read after repair.** The constraint is fixed (name the carriage as landed, the staging as unowned, no number). Whether that is one shared sentence or five site-specific ones is a readability call at each site. Prefer the shorter repair: these are already dense blocks.
3. **Whether the new test shares `finGatherPinnedReadings` or carries its own rows.** § 6 assumes its own rows, because the two refutation verdicts are not in that set and its doc is written around a different claim (SHAPES the whole value must cross). If adding two entries there turns out cleaner, check first that it does not move `TestFinGatherPinnedPidDoesNotReachTheLiveness` or `TestFinGatherPinnedPidCarriesNoCapturedBytes`, which both sweep that set — if it does, keep them separate.

---

## Security review

**Verdict:** PASS

The standing threat for this package: an operator's `CLAUDE_CODE_OAUTH_TOKEN` or `ANTHROPIC_API_KEY` reaching an artifact destined for a **public issue**, via verbatim argv or raw `ps` stderr. Every category below is walked against that.

**Findings:**

- **[Trust boundaries] No findings — the boundary is explicit and the new field is the strongest-typed crossing yet.** Data crosses caller → `finGatherInputs` → `finGatherReadings` → `trailRunReadings` → `trailClassifyRun` → published `trailRunOutcome`. The new field crosses unvalidated, matching `RunnerPath` and `PinnedPid`; the gather validates nothing and the field doc says so as a statement about the gather rather than a licence for its caller. What makes this crossing *safer* than its two neighbours: `RunnerPath` is a plain `string` that cannot tell a label from an argv, so its safety rests on a doc rule at the call site. `trailOrderResult` is already the reduced type, and its only admissible producer takes three `bool`s — so the reduction is structural rather than disciplinary. Downstream holders are signalled by the type, not by convention.

- **[Tokens, secrets, credentials] No findings — verified three independent fences, not assumed.** (1) `trailCertifyOrdering`'s parameters are three `bool`s, so no captured byte can reach a `trailOrderResult` through its producer. This is the same *narrow-the-parameter* remedy its own doc argues for, and it is why no denylist is owed. (2) `trailEstablishSighting` is forbidden from quoting `ordering.Detail`, and `TestTrailSightingResultCarriesNoCapturedBytes` already plants the needle into `ordering.Detail` and asserts the marshalled result does not carry it — shipped today, not deferred. (3) `trailClassifyRun` never renders `Ordering` into `trailRunOutcome` at all; the composition was read field-by-field to confirm. No token lifecycle is introduced (no generation, storage, rotation or revocation).

- **[Tokens — the deferral to #1463] OUT OF SCOPE, and the gap is measured rather than argued.** The gather-tier captured-bytes sweep over this route is **#1463's**. Between this ticket landing and #1463's, the only way captured bytes enter `Ordering` is a caller hand-building `trailOrderResult{Detail: <captured>}` — and *no live caller fills the field at all*, structurally (see § Context). Even a hostile hand-build reaches no published record, per fence (3). #1463 fences a future field and needs this route to exist first.

- **[Subprocess execution] No findings, but a review trap is named.** This change adds no `exec` call. The new test nonetheless runs `ps` three times, inside `pinScanArgv` — a grep for `exec.` in the test body finds nothing and says the wrong thing, which is why § Testing strategy names the reaching symbols instead of relying on that grep. No caller-controlled value reaches an argument: needles come from `finGatherNeedles`, a `t.TempDir()` path nothing is staged at. No `sh -c`. No environment inheritance change.

- **[Error messages, logs, telemetry] No findings — the exposure is fixture-only and unchanged.** The new test's `Fatalf` messages render `PinnedPid` with `%+v`, which on a *live* value would print `ToolStderr` (raw `ps` stderr) and `Detail`. Every value in these rows is a source-authored fixture, and the spec forbids a needle plant here (deferred to #1463), so nothing capturable is in scope to print. This matches `TestFinGatherHalfStagedRouteMovesNoOutcome`'s existing shape — no new exposure, and no `slog` call is added.

- **[Correctness-as-security — the wrongly-wired live caller] SHOULD FIX, addressed in the spec.** The realistic hostile actor here is a confused developer following #1458's precedent, wiring `finExitRunProbe` at step 3b with a **guessed** `holdHeld`. The result is not a leak but a *false published claim*: `run-alive-at-sighting-by-ordering` on a run where the hold was released, meaning the pinned pid could have been retired and reissued between the two instants — the artifact names a process it cannot show is the same one. § Context states the structural impossibility, and § Design now requires the field doc to name this consequence rather than only the prohibition. Code review should treat any new `trailCertifyOrdering` call site outside a test as a blocker.

- **[Correctness-as-security — the two tempting "hardenings"] SHOULD FIX, both named in the spec.** (a) Adding a C10 contract check over `Ordering` would answer `run-out-of-contract` on **every run that exists today**, masking genuine out-of-contract conditions behind a universal false positive — a signal-quality regression in the one value that names a caller's bug. § 4 forbids it and says what to write instead. (b) Widening the step-1 guard from `== ""` to `!trailIsOrderValue(...)` would file an undefined non-empty value as "the route was never staged", laundering a caller's bug into a routine reading — the #1417 collapse mirrored. § Error handling pins the current shape as deliberate.

- **[File operations] Not applicable by design.** No path is constructed, opened, created or removed. The only filesystem contact is `t.TempDir()` inside `finGatherNeedles`, which is the test framework's own and is never written to.

- **[Cryptographic primitives] Not applicable by design.** No randomness, hashing, key material or comparison against a secret. `trailCertifyOrdering` is pure over three `bool`s.

- **[Network & I/O] Not applicable by design.** No socket, no HTTP server, no reader over untrusted input. The one size cap in reach is `trailDetail`'s 512-byte budget, which this change does not approach: no new `Detail` is composed, and the two arms the new test drives are already budgeted at 492 and 482 bytes against 512 with that measurement recorded at the arm.

- **[Concurrency] No findings.** No goroutine, lock or shared mutable state is introduced. `go test -race` runs this package in parallel and several tests re-exec the binary; the new test allocates its own `probeSyncBuffer` per run, matching the sibling it copies. Its row table holds scalars and a two-string struct, so the `trailRunWellFormed` function-not-var rule does not bind, and § Concurrency model says why.

- **[Threat model alignment] Aligned.** The relevant channel is the e2e-realclaude artifact → public issue path this package exists to keep shut, held by `Pinned []int`, by `RunnerPath`'s reduce-at-the-call-site rule, and by the per-tier captured-bytes sweeps. This ticket adds a crossing that is closed at the producer, at the consumer, and at the publisher. The one threat left open — a live caller that fills the field wrongly — is out of scope for this ticket and **has no ticket yet**; it is named here so it is not lost.

**Reviewer:** architect (self-review per `architect/security-review.md`)
**Date:** 2026-08-14
