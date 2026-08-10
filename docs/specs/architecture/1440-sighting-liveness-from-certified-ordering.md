# #1440 — Aliveness-at-a-trailer-sighting from a certified ordering and a pinned pid (offline)

Ticket: https://github.com/pyrycode/pyrycode/issues/1440 · Split from #1436 · Sibling: #1439 (merged, PR #1441)

## Files to read first

Turn-1 data load. Every line number below was re-verified against `176ba0d` (the #1439 merge) at the time this spec was written.

| Path | What to extract |
|---|---|
| `internal/e2e/realclaude/trail_ordering_premises_test.go` (all 618 lines) | **The template for this whole ticket.** Read it end to end before writing a line. Its header/value-space/record/predicate/membership/fixtures/tests layout is the layout to reproduce. Note especially `:572-590` — it names #1440 as the ticket that ships the needle sweep it deliberately omits. |
| `internal/e2e/realclaude/trail_ordering_premises_test.go:107-128` | The four `trailOrder*` values, and the doc pattern for arguing a prefix is load-bearing. |
| `internal/e2e/realclaude/trail_ordering_premises_test.go:158-161` | `trailOrderResult{Value, Detail}` — one of this ticket's two input records. Two strings; no pointer. |
| `internal/e2e/realclaude/trail_ordering_premises_test.go:242-284` | `trailCertifyOrdering` — guards-then-fall-through body shape, and the `decide` closure that wraps `trailDetail`. Reproduce both. |
| `internal/e2e/realclaude/trail_ordering_premises_test.go:320-333` | `trailOrderPremises`, its `certify()` method, and `trailOrderCertifiedPremises()` — **AC3 drives off these three, not off a hand-built value.** |
| `internal/e2e/realclaude/trail_ordering_premises_test.go:465-512` | `TestTrailOrderEachPremiseHasItsOwnVoid` — the exact premise-removal shape AC3 asks for, including the assert-the-base-certifies-first discipline. |
| `internal/e2e/realclaude/trail_ordering_premises_test.go:529-570` | `TestTrailOrderValuesAgreeWithThePredicate` — the both-directions + count + spelled-rejected-list shape AC4 asks for. |
| `internal/e2e/realclaude/process_pin_liveness_test.go:204-219` | The four `pinState*` verdicts, with the reason each exists. |
| `internal/e2e/realclaude/process_pin_liveness_test.go:238-253` | `pinStateOutcome` — the other input record. **Seven fields; three are string-bearing (`Detail`, `StateColumn`, `ToolStderr`).** |
| `internal/e2e/realclaude/process_pin_liveness_test.go:297-341` | `pinClassifyState`'s branch table. Branch 1 (`err != nil`, stderr non-empty) is the live capture route: `:341` assigns raw `ps` stderr to `ToolStderr`, and `:348-349` folds it into `Detail`. This is why AC5's plant is load-bearing. |
| `internal/e2e/realclaude/process_pin_liveness_test.go:265-295` | `pinReadState` — the no-`*testing.T`, never-fails-a-test contract this predicate inherits. Also `:268-271`: the direct-lookup argument (no parentage) the ticket's Context leans on. |
| `internal/e2e/realclaude/process_pin_liveness_test.go:1142` | `pinIsVerdict` — the shipped membership predicate over the four verdicts. **There is no `pinVerdicts()` list**; spell the four where you need them. |
| `internal/e2e/realclaude/trail_run_outcome_test.go:114-118` | `trailOutcomeRunningAtTrailer = "run-running-at-trailer"` and its doc. **This is the collision this ticket's naming must defeat.** |
| `internal/e2e/realclaude/trail_run_outcome_test.go:159-163` | `trailOutcomeVoidLivenessInstrument = "run-void-liveness-instrument-failed"` — the second, subtler near-collision (see § Naming). |
| `internal/e2e/realclaude/trail_run_outcome_test.go:309-323` | `trailIsRunOutcome` — the membership-predicate shape AC4 names. |
| `internal/e2e/realclaude/trail_run_outcome_test.go:1571-1627` | `TestTrailRunOutcomeCarriesNoCapturedBytes` — **the exact shape of AC5's sweep**, both halves. `:1586-1591` shows a `pinStateOutcome` planted with the needle in `Detail` + `ToolStderr`; `:1594-1599` is the premise-first discipline; `:1618-1626` is the forbidden-key half. |
| `internal/e2e/realclaude/trailer_admissibility_test.go:1164-1298` | `TestTrailAdmissibilityConstantsAreClosed` — the union closure map. § Closure-map manifest below gives the six exact edit sites. |
| `internal/e2e/realclaude/trailer_admissibility_test.go:340-360` | `trailAdmitResult` (Value+Detail) and `trailDetail(format, args...)`. `trailGateResult` is the Value+**Reason**+Detail precedent this ticket's record follows. |
| `internal/e2e/realclaude/background_reach_probe_test.go:123` | `reachMaxCommandBytes = 512` and `reachTruncationMarker` — the cap `trailDetail` inherits, and the marker the coverage test asserts absent. |
| `internal/e2e/realclaude/result_trailer_observation_test.go:325` | `trailNeedle` — the shipped needle constant. Reuse; do not define a second. |
| `internal/agentrun/streamrunner/runner.go:203-208` | The single reap call site inside `cmd.Cancel`, and the "It never fires on a clean exit" comment. This is *why* the ticket exists; quote it in the file header. |
| `internal/agentrun/ptyrunner/runner.go:387-398` | The contrasting `defer`-based reap that fires on every teardown. |

Do **not** read `finding_run_gather_test.go` for the input shape — `finSighting` (`:372-384`) is the wrong input and § Inputs says why.

## Context

`trailClassifyRun` ranks an admissible reap-log attribution above every point-in-time reading (`trail_run_outcome_test.go:364-365`), and argues at the point of use (`:602-606`) that the readings "are expected to be late … so resting a verdict on them manufactures a systematic false negative". `:845-847` makes that executable.

That top-ranked evidence **does not exist on `PYRY_USE_STREAMJSON=1`**. `streamrunner` reaps only inside `cmd.Cancel` (`runner.go:207-208`), which by its own comment "never fires on a clean exit". On a healthy stream-path run pyry writes no reap log at all — the attribution leg is not late, it is empty.

The ordering argument replaces it. #1439 shipped `trailCertifyOrdering` (`trail_ordering_premises_test.go:242`) certifying that three instants were ordered by construction. Given that ordering, a process can only die once, so a pid found **alive** after pyry's exit was necessarily alive at the earlier sighting. Lateness only strengthens this direction.

**What this predicate claims, and what it must never claim.** It establishes *alive when the trailer line was sighted on pyry's stdout* — not *alive when pyry declared the turn finished*. On this path no terminal reason is certified, so no declared-finished instant exists (`trail_run_outcome_test.go:557`, `:568-576`, `:582-591` each say so at their own void arm). Every value and every Detail names the **sighting** as the instant.

## Scope boundary

Ships: the value spaces, the record, the predicate, its fixtures, its tests, its own no-captured-bytes sweep, **and the join to the shared union closure map**.

Does not ship: any change to `trailClassifyRun`, any growth of the run-level outcome set, any change to the step-1 gate switch. That switch answers `trailGateAbsentOwesNone` with an unconditional `return` (`trail_run_outcome_test.go:565-576`), so a route wired below it would not fire on the one gate value it would exist to serve. Reaching it is #1437's work.

**Observation to record, not to chase:** the stream path leaves a backgrounded group unreaped where ptyrunner kills it. What survives teardown is #1231's question. Note it in the file header; do not build for it.

## Design

One new file, `internal/e2e/realclaude/trail_sighting_liveness_test.go`, plus a six-site join to `trailer_admissibility_test.go`. Build tag `//go:build e2e_realclaude`, package `realclaude`. Zero production files.

### Inputs — two whole records, and the parameter list is the enforcement

```go
func trailEstablishSighting(ordering trailOrderResult, pin pinStateOutcome) trailSightingResult
```

Both parameters are the **shipped records**, not their `Value`/`Verdict` strings. Two reasons, and they pull in opposite directions from #1439's:

- `pinStateOutcome` carries `Detail`, `StateColumn` and `ToolStderr`, all string-bearing, and `ToolStderr` takes raw `ps` stderr verbatim (`process_pin_liveness_test.go:341`). Narrowing the parameter to a bare verdict string would make AC5's byte sweep **unbuildable as specified** — there would be no route for a needle to travel, and a sweep that cannot fail measures nothing. This is the inverse of #1439, where three bools were the enforcement precisely *because* no byte could reach them.
- `finSighting` (`finding_run_gather_test.go:372-384`) is the nearest existing reduction of a sighting and is the **wrong** input: it carries `TerminalReason`, `StopReason`, `Subtype`, `KeyNames`. The forbidden-key half of AC5 would not catch it — those marshal as `terminal_reason` / `stop_reason` / `subtype` / `trailer_keys`, none `command`-shaped. **The parameter list is what forecloses it.**

Contract, inherited verbatim from `trailGate` / `pinReadState` / `trailCertifyOrdering`: pure over its input — no exec, no clock, no filesystem, no `*testing.T`, never fails a test. Consults no parentage; requires no reap line.

### Naming — the collision is the central risk

`trailOutcomeRunningAtTrailer = "run-running-at-trailer"` is documented as *"an admissible attribution proves the process group was alive when the trailer was written. THE FINDING, and the only path to one."* That is the **same English sentence** this predicate establishes, from the reap-log evidence class the stream path lacks. A value one word from it lets a consumer read the two evidence classes as one finding.

The prefix is `sighting-`, and the positive value names **both the instant and the evidence route**:

| Go identifier | String | Meaning |
|---|---|---|
| `trailSightingEstablished` | `sighting-alive-by-ordering` | Alive at the sighting, established from the ordering argument. |
| `trailSightingUnestablished` | `sighting-not-established` | The ordering was certified and the pid was not alive. |
| `trailSightingVoid` | `sighting-void` | Nothing was measured. Never a negative. |

Reason space, five values, one per per-pid verdict plus the pass-through:

| Go identifier | String | Fires when |
|---|---|---|
| `trailSightingReasonPidRunning` | `sighting-reason-pid-running` | certified + `pinStateRunning` |
| `trailSightingReasonPidReapedPending` | `sighting-reason-pid-reaped-pending` | certified + `pinStateExitedNotReaped` |
| `trailSightingReasonPidGone` | `sighting-reason-pid-gone` | certified + `pinStateNoSuchProcess` |
| `trailSightingReasonPidReadFailed` | `sighting-reason-pid-read-failed` | certified + `pinStateInstrumentFailed` (and any off-space verdict — see § The default arm) |
| `trailSightingReasonOrderingUncertified` | `sighting-reason-ordering-uncertified` | ordering is any `trailOrderVoid*`, regardless of verdict |

Eight new values. Two naming decisions to argue in the doc comments:

1. `sighting-alive-by-ordering` rather than anything containing `running-at-trailer`. Different prefix, different noun, and the `-by-ordering` suffix names the evidence class the collision is about.
2. `sighting-reason-pid-read-failed` rather than `…-liveness-instrument-failed`. The shipped `trailOutcomeVoidLivenessInstrument` is `"run-void-liveness-instrument-failed"`; a reason ending in the same four words would be distinct to the closure map and confusable to a reader. Naming it after *the pid read* keeps the run space's phrasing out of this one.

**No `sighting-out-of-contract` value.** Same decision #1439 made and for a checkable reason: see § The default arm below, which shows every off-space input already has a reachable, correct home. An unreachable named value would make this space's count a lie about what the predicate can answer.

### The record

```go
type trailSightingResult struct {
	Value  string `json:"value"`
	Reason string `json:"reason"`
	Detail string `json:"detail"`
}
```

Three strings, in `trailGateResult`'s Value+Reason+Detail shape (`trailAdmitResult` and `trailOrderResult` are the two-field variant; this space needs the named reason as a field per AC1). No pointer, no embedded type, **no `PID` field** — the pid reaches a reader through the Detail as `%d`, which keeps the marshalled surface at three keys and the sweep's forbidden-key walk over the smallest possible set.

### The assignment rule — 4 × 4, no combination left to judgement

| ordering \ verdict | `Running` | `ExitedNotReaped` | `NoSuchProcess` | `InstrumentFailed` |
|---|---|---|---|---|
| `trailOrderCertified` | **established** / pid-running | unestablished / pid-reaped-pending | unestablished / pid-gone | **void** / pid-read-failed |
| `trailOrderVoidUnsighted` | void / ordering-uncertified | ″ | ″ | ″ |
| `trailOrderVoidNoExit` | void / ordering-uncertified | ″ | ″ | ″ |
| `trailOrderVoidUnheld` | void / ordering-uncertified | ″ | ″ | ″ |

**One precedence decision, and it must be stated and checked:** on a non-certified ordering the void is reached *regardless* of the verdict, so `ordering-uncertified` outranks `pid-read-failed` on the (void ordering × instrument-failed) cell. The argument: a premise failure is not evidence about the command, and re-deciding a premise this predicate did not measure would be reading the ordering result as something other than whole. The ordering result is **passed through**, never re-derived from its Detail or re-litigated.

Two invariants the rule encodes, both worth a doc paragraph:

- **A premise failure never reads as evidence that the command had exited.** Void, never unestablished.
- **An instrument failure never reads as a clean negative.** Void, never unestablished — the same collapse `trailOutcomeVoidLivenessInstrument`'s doc (`trail_run_outcome_test.go:159-163`) refuses.

### Body shape — one guard, then a switch whose default is reachable

Four return sites. Guard on `ordering.Value != trailOrderCertified` first; then `switch pin.Verdict` with `pinStateRunning` → established, `pinStateExitedNotReaped` and `pinStateNoSuchProcess` → unestablished, and **`default:`** → void / pid-read-failed.

The `default` is deliberate and is **not** a defensive arm no fixture reaches: it is the home of both `pinStateInstrumentFailed` *and* any off-space verdict string (including the zero `""` of an unfilled `pinStateOutcome`). Two fixtures reach it by different routes, and AC2's table must contain both. Folding them together is correct rather than lazy — an unreadable verdict and a failed read are the same statement, *nothing was measured*, and both must fail safe to void.

Reuse #1439's `decide(value, reason, format, args...)` closure shape wrapping `trailDetail`, so all four arms inherit the 512-byte cap.

### Detail content rule

Each Detail MAY name the verdict that decided, this space's value and reason names, the pid, and — on the pass-through — which ordering value arrived. It MUST name **the sighting** as the instant its verdict is about. It MUST NEVER claim the turn was declared finished, and MUST NEVER quote `pin.Detail`, `pin.ToolStderr`, `pin.StateColumn` or `ordering.Detail` — that last prohibition is what AC5's sweep exists to enforce, and the natural mistake (`"the pinned pid read %s", pin.Detail`) is exactly what it catches.

A comparable shipped Detail on this tree measures 225 bytes, so the cap has real headroom; the coverage test asserts `reachTruncationMarker` is absent from every row rather than checking a length against 512 (`background_reach_probe_test.go:945-950` returns the input unchanged *at* the cap, so a length check both false-fails at the boundary and pins a drifting literal).

### Membership helpers

Two lists and two predicates, in `trailIsRunOutcome`'s shape (`trail_run_outcome_test.go:309`):

- `trailSightingValues() []string` / `trailIsSightingValue(v string) bool` — the three outcomes.
- `trailSightingReasons() []string` / `trailIsSightingReason(v string) bool` — the five reasons.

Kept as two spaces, not one merged list: a consumer asks "is this a verdict I can look up?" and "is this a reason I can look up?" as different questions, and merging them would let a reason pass where an outcome is expected.

### Fixtures

- `trailSightingPin(verdict string) pinStateOutcome` — a well-formed `pinStateOutcome` for one verdict, with a plausible `PID` and a `Detail`. One helper, not four literals.
- Reuse `trailOrderCertifiedPremises()` and `.certify()` from #1439 for every certified ordering. **Do not hand-build a `trailOrderResult{Value: trailOrderCertified}`** anywhere except the AC5 sweep, where a struct literal is required to plant a needle into `Detail`.

## Testing strategy

Five test functions, all offline, no `t.Skip`, no env gate. Run filter `^TestTrailSighting`.

**`TestTrailSightingAllSixteenCombinations`** (AC2). Generate the cross product from `trailOrderValues()` × the four spelled verdicts rather than hand-writing sixteen literal rows — a generated product makes "no combination left to judgement" structural instead of a count maintained by hand, and it is how the sixteen stay honest when a fifth ordering value lands. Carry #1439's distinctness guard (`len(seen) != 16`) so a generator bug that emits duplicates reddens. Per pair assert: the expected outcome, the expected reason, a non-empty Detail, no `reachTruncationMarker`, and that the Detail contains the word naming the sighting. Add two extra rows outside the product for the off-space verdicts — `""` and a junk string — both expecting void / pid-read-failed.

**`TestTrailSightingPremiseRemovalNeverEstablishes`** (AC3). Three subtests, one per premise, in `TestTrailOrderEachPremiseHasItsOwnVoid`'s shape (`trail_ordering_premises_test.go:465-512`):
- Start from `trailOrderCertifiedPremises()`; **assert it certifies before flipping anything** — without that premise a predicate that certified nothing would pass all three.
- Clear one field, call `.certify()`, feed the result plus a `pinStateRunning` pin to the predicate.
- Assert the outcome is `trailSightingVoid`, the reason is `trailSightingReasonOrderingUncertified`, and explicitly that it is **not** `trailSightingEstablished`.

The `pinStateRunning` pin is the point: it is the verdict that would otherwise establish, so this is what stops a caller bypassing the hold-as-precondition argument by fabricating a certification.

**`TestTrailSightingValuesAgreeWithThePredicate`** (AC4). Both spaces, both directions, in `TestTrailOrderValuesAgreeWithThePredicate`'s shape:
- `len(trailSightingValues()) != 3` and `len(trailSightingReasons()) != 5`, each with a message stating the count is this ticket's own enumeration.
- Every listed value accepted by its predicate; every listed reason accepted by its.
- Rejection, over a spelled list: `""`, all four `trailOrder*` values, the four `pinState*` verdicts, `trailOutcomeRunningAtTrailer` and `trailOutcomeVoidLivenessInstrument` (the two collisions this naming defeats), `trailGateUsable`, `trailAdmitProof`, `trailSeen`/`trailAbsent`/`trailAborted` — **and each space rejects the other's five/three values.** That last direction is the one that earns its place: it is what keeps a reason from passing as an outcome.

**`TestTrailSightingResultCarriesNoCapturedBytes`** (AC5). In `TestTrailRunOutcomeCarriesNoCapturedBytes`'s shape (`trail_run_outcome_test.go:1571-1627`):
- Plant `trailNeedle` into **`pin.Detail`, `pin.ToolStderr`, `pin.StateColumn`** — the two live routes plus the third string-bearing field, which costs one line.
- Plant it into `ordering.Detail` too, via a struct literal. **Note in the comment what this is and is not:** #1439 pinned its producer's inputs to three booleans and builds Detail through `trailOrderPremiseClause = "premises: … =%t …"` (`trail_ordering_premises_test.go:145`), so no captured byte can reach it *through its producer*. This plant is a discipline against a future field, not a live route — and it must not be allowed to stand in for the two that are.
- Premise first: assert the result is `trailSightingEstablished` before marshalling, so the sweep cannot pass by classifying garbage.
- `json.Marshal`, then `bytes.Contains(encoded, []byte(trailNeedle))` must be false.
- Structural half: decode to `map[string]json.RawMessage` and refuse any key containing `command`, `args`, `comm`, `argv`.

**`TestTrailSightingVoidsNeverReadAsNegative`** (the doctrine, made executable). One short loop over every row of the 4×4 that lands on void, asserting none carries `trailSightingUnestablished` and none of their Details contains the phrase the naming rule forbids (`declared finished` / `declared the turn finished`). Cheap, and it is the only check that the *prose* half of the naming rule holds.

### Verification

These files carry `//go:build e2e_realclaude`, so **`make check` does not compile them.** An exit code cannot tell a skip from a pass — read the count of tests that actually executed:

```bash
go test -race -tags e2e_realclaude -run '^TestTrailSighting' -v ./internal/e2e/realclaude/
go test -race -tags e2e_realclaude -run '^TestTrail' ./internal/e2e/realclaude/   # the shared file has five other consumers
```

The second command is not optional: the closure-map join touches a file shared with five other value spaces.

Nothing here needs a live claude, credentials, or a daemon.

## Closure-map manifest — `trailer_admissibility_test.go`, six exact sites

Per #1440's own doctrine: a per-space membership predicate is scoped to one space per call and **cannot see a pair**. `TestTrailAdmissibilityConstantsAreClosed` holds all 43 shipped values in one map and is the only instrument that can state this ticket's central claim. Join it; do not start a sixth closure test.

1. **`:1173-1176`** — the count history ends *"…#1434's two made thirty-nine, and #1439's four make forty-three."* Append this ticket's eight → **fifty-one**.
2. **`:1179-1181`** — *"#1439's ordering-premise values joined it rather than starting a **fifth**"* → sixth.
3. **`:1181-1185`** — *"**four spaces** now mean nearly the same words (an input state, the gate's view of it, the run's view of it, and the ordering premises' view …)"* → five, and extend the parenthetical with this space (the sighting's view of the same events, from a different evidence class). Add the sharper near-collision as the worked example: #1439's was one word apart in a *void* value; this one is the same claim in the *positive* value, `run-running-at-trailer`.
4. **`:1187-1191`** — the per-space consumer list ends *"…and `trailCertifyOrdering` (`trail_ordering_premises_test.go`) for the fifth."* Add `trailEstablishSighting` for the sixth.
5. **`:1247-1250`** — after the `"trailOrderVoidUnheld": trailOrderVoidUnheld,` entry, insert all **eight** new entries under a comment naming `run-running-at-trailer` as the reason they are here.
6. **`:1284` and `:1295-1297`** — add `var zeroSighting trailSightingResult` beside `zeroGate`/`zeroAdmit`/`zeroRun`/`zeroOrder`, and its `zeroSighting.Value == value` arm to the walk. Also add a `zeroSighting.Reason != ""` check beside the existing `zeroGate.Reason` one at `:1299-1302` — an unfilled reason reading as a filled one is the same failure mode, and this record is the second in the family to carry a `Reason`.

Expected churn: roughly +34 / −8, against #1439's measured +24 / −6 for half as many values.

## Size

**`s`, and the disagreement is recorded rather than absorbed.**

My independent bottom-up measures **~750 lines**, not the ticket's ~697. The gap is concentrated in two blocks the ticket's block-by-block priced at or below #1439's narrower versions: the 4×4 coverage (sixteen rows against #1439's eight, whose table measured 118) and the enumeration (two spaces against #1439's one, whose test measured 57). For scale, the family's fully-enumerated case table, `trailRunCases()` (`trail_run_outcome_test.go:776-1074`), is 298 lines. Prescribing a **generated** cross product rather than sixteen literal rows is what keeps that block near 85 instead of 170+.

~750 is over the ~600 red line. I am not splitting, and these are the reasons, in the order they carry weight:

1. **The seam check against the source finds nothing not already rejected.** Every candidate cut is on #1436's enumerated rejected-cuts list, and I verified each reason against the code rather than against its prose: the reason set cannot defer because AC1 puts the reason *in the record*; the sweep cannot defer because `pinStateOutcome.ToolStderr` provably carries raw `ps` stderr (`process_pin_liveness_test.go:341`), so the record it guards can carry captured bytes from the first commit; the instrument-failed arm cannot defer because an unhandled `pinStateInstrumentFailed` falls to unestablished, manufacturing a clean negative out of the instrument's own breakage.
2. **Splitting would make the total larger and add a conflict edge.** The fixed costs here do not divide: header ~100, record ~12, membership scaffolding ~53, closure-map join ~34 ≈ **199 lines duplicated into both children**. Two children come to ~950 against one ticket's ~750, and the second child re-opens `trailer_admissibility_test.go` — the shared-file overlap my own § 1.5 check exists to make structurally impossible.
3. **The direct sibling of this split settles the cost model empirically.** #1439 — same split, same file family, same package, merged today — delivered 618 lines in a new file plus +24/−6 on the shared one at `size:s`, with one `needs-rework:developer` on a correctness point and **no size rework, no `error:*`, no max_turns**. The ~600 line's stated evidence (#29/#40/#45/#75) is production tickets with consumer cascades, where cost is read/edit/verify churn across many files. This is single-file additive authoring against fixtures: zero production files, zero call sites, zero build cascade.

The honest residual: ~750 is ~16% above what the sibling actually delivered, not the ~13% the ticket claims. The mitigation is to pay discovery at architect time rather than developer time — the § Files to read first table, the six-site closure manifest with current text quoted, and the generated-cross-product prescription together remove the exploration and the grep-for-the-edit-site turns that are where this family's budget actually goes.

No other red line trips: 1 new file, 0 new exported types, 0 consumer call sites, 5 acceptance criteria, 6 reject/return branches (4 in the predicate, 2 map arms).

## Open questions

1. **Does the `default:` arm want to distinguish `pinStateInstrumentFailed` from an off-space verdict in the *Detail*?** The design says one reason for both (they are the same statement). The Detail can still render `pin.Verdict` — it is a value from this family's own closed space, not a captured byte, and `pinIsVerdict` (`process_pin_liveness_test.go:1142`) lets the arm say which case it saw. Recommended: yes, render it; it costs one `%q` and makes the two fixtures distinguishable in a failure message.
2. **Should `trailSightingReasons()` be spelled into the union closure map as five separate entries, or is the outcome space alone the collision surface?** AC4 says every value this ticket adds joins the map, and #1366's six `trailReason*` values are already in there as precedent, so: all eight. Flagged only because it makes the map's "every value has an arm in its consumer" comment cover reason values as well as outcome values — which it already does for #1366.

## Security review

**Verdict:** PASS

**Findings:**

- **[Trust boundaries]** MUST FIX → **fixed in this spec before commit.** The boundary is `pinStateOutcome`'s three string-bearing fields, which carry subprocess stderr into parent state (`process_pin_liveness_test.go:341`, `reachCapCommand(exitErr.Stderr)`). The first draft of § The record and § Detail content rule described the prohibition but did not name `StateColumn` alongside `Detail` and `ToolStderr`, which would have shipped a sweep planting two of the three live-ish routes and reading as complete. Both sections and AC5's test description now name all three. The boundary is explicit and single: `trailEstablishSighting` is the only function that reads these fields, and it is specified to read `pin.Verdict` and `pin.PID` only.
- **[Tokens, secrets, credentials]** No findings, and the category is the reason this ticket is labelled. The threat is concrete rather than theoretical: `ps` stderr can quote the operator's `CLAUDE_CODE_OAUTH_TOKEN` / `ANTHROPIC_API_KEY` if a column ever widened to environment, and this record is destined for a public GitHub issue. Three controls, all specified: the record has three keys and none is a capture (§ The record); the Detail rule forbids quoting any input string (§ Detail content rule); and AC5's sweep makes both halves checkable rather than advisory. `pinStateColumns = "pid=,ppid=,stat="` (`process_pin_liveness_test.go:232`) is the upstream control and this ticket does not widen it.
- **[File operations]** N/A by design decision — the predicate is specified pure over its input: no filesystem, no exec, no clock. No path is constructed anywhere in this ticket.
- **[Subprocess execution]** N/A by the same decision. This ticket adds no `exec` call site; it consumes `pinReadState`'s already-shipped one, and § Inputs forbids building a second liveness reader.
- **[Cryptographic primitives]** N/A — no randomness, no comparison against a secret. The one comparison is `ordering.Value != trailOrderCertified`, over this family's own closed value space, not over attacker-controlled input.
- **[Network & I/O]** No findings. The one bound that applies is the 512-byte Detail cap inherited from `trailDetail` → `reachCapCommand` (`background_reach_probe_test.go:123`), and the coverage test asserts the truncation marker is absent from every row so a Detail whose argument would be cut off reddens here rather than reaching an operator's artifact.
- **[Error messages, logs, telemetry]** SHOULD FIX, and specified rather than gated. The realistic leak is a Detail interpolating `pin.Detail` or `pin.ToolStderr` for a better failure message — which is precisely the mistake AC5's needle sweep catches, so the control is deterministic code and not an author's discipline. Code-review should confirm the sweep plants into all three `pinStateOutcome` string fields and not only the two the ticket body emphasises.
- **[Concurrency]** N/A — the predicate is pure and stateless, spawns no goroutine, takes no lock, and holds no shared state. Every test is driven from fixtures with no live process.
- **[Threat model alignment]** In scope and addressed: "published evidence must be pasteable into a public issue without operator review" is the standing obligation this family carries, and this ticket is the first in the ordering-argument line whose record can carry a captured byte at all — #1439 said so explicitly at `trail_ordering_premises_test.go:582-583`. Out of scope and named: what survives teardown on the stream path (#1231), and wiring this predicate into `trailClassifyRun`'s step 1 (#1437).

**Reviewer:** architect (self-review per `architect/security-review.md`)
**Date:** 2026-08-10
