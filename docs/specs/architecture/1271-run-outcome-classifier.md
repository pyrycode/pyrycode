# #1271 — One probe run's observations resolve to exactly one named outcome

**Ticket:** [#1271](https://github.com/pyrycode/pyrycode/issues/1271) — split from #1267.
**Blocker:** [#1270](https://github.com/pyrycode/pyrycode/issues/1270), **merged** (PR #1272). Everything this ticket consumes is on `main`.
**Consumer:** [#1268](https://github.com/pyrycode/pyrycode/issues/1268) — blocked by this ticket; stages the classifier against real processes.
**Size:** S. One new file plus one bounded edit to #1270's closure test; zero production code, zero call sites changed, offline-provable.

---

## Files to read first

| Path | What to extract |
|---|---|
| `internal/e2e/realclaude/trailer_admissibility_test.go:102-160` | The two shipped value spaces verbatim — five `gate-*`, seven `admit-*`. Your eleven values must be pairwise-distinct from all twelve, and AC5's closure test asserts it. |
| `internal/e2e/realclaude/trailer_admissibility_test.go:164-197` | `trailGateResult` and `trailAdmitResult`. Both are documented trap-free by construction — that is why this ticket's input record may hold them whole. |
| `internal/e2e/realclaude/trailer_admissibility_test.go:242-320` | `trailGate`'s decision order. **The shape to mirror**: the out-of-contract value is a guard at the top, never a switch default. |
| `internal/e2e/realclaude/trailer_admissibility_test.go:356-465` | `trailAdmitAttribution` — its two ordering arguments, and the exact preconditions of the proof arm (`Killed` + `LineCount == 1` + reason not `max_turns`). § Composition below depends on these. |
| `internal/e2e/realclaude/trailer_admissibility_test.go:473-492` | `trailIsGateValue` / `trailIsAdmitValue`. AC1's out-of-contract arm **calls** these; it does not re-derive membership. |
| `internal/e2e/realclaude/trailer_admissibility_test.go:518-573` | `trailGateCases()` — eight shipped gate fixtures. Reuse them for the composition rows rather than hand-building gate results. |
| `internal/e2e/realclaude/trailer_admissibility_test.go:586-656` | `TestTrailAdmissibilityConstantsAreClosed` — **the test AC5 says to extend**, not to copy. Its union map, its zero-record pin, and why one map beats a third one-space-at-a-time call. |
| `internal/e2e/realclaude/trailer_admissibility_test.go:856-904` | `TestTrailGateThenAdmit` — the composition precedent, including the "invoked exactly twice" assertion. |
| `internal/e2e/realclaude/trailer_admissibility_test.go:915-961` | `TestTrailAdmissibilityRecordsCarryNoCapturedBytes` — the marshal-and-search shape AC2 requires, and the `trailNeedle` reuse. |
| `internal/e2e/realclaude/result_trailer_observation_test.go:73-90` | The three `trailBoundFrom*` values and why `trailBoundFromStart` bounds nothing. AC3's discriminator. |
| `internal/e2e/realclaude/result_trailer_observation_test.go:98-137` | `trailScanResult` and — at `:125-126` — `trailObservation`'s **embedding**. This is why corroboration takes `BoundFrom` as a plain value and never the observation. |
| `internal/e2e/realclaude/result_trailer_observation_test.go:300` | `trailNeedle`. Reuse it; do not mint a second needle. |
| `internal/e2e/realclaude/process_pin_liveness_test.go:120-135` | `pinScan` — `Matches` (verbatim argv), `MatchCount`, `RowsScanned`. Take the two counts; never the slice. |
| `internal/e2e/realclaude/process_pin_liveness_test.go:178-197` | `pinScanArgv`'s **zero-`pinScan`-on-error** contract, and its own note that a consumer's error gate must not cross-assign with the per-pid verdict. AC4 is that obligation landing on this classifier. |
| `internal/e2e/realclaude/process_pin_liveness_test.go:204-232` | The four `pinState*` values, and `pinStateColumns`' never-add-`command` prohibition that AC2 mirrors onto this outcome record. |
| `internal/e2e/realclaude/process_pin_liveness_test.go:245-253` | `pinStateOutcome` — carries no command column **by construction**. That is what makes it admissible as an input here. |
| `internal/e2e/realclaude/process_pin_liveness_test.go:1142-1149` | `pinIsVerdict` — the shipped membership predicate over the four states. **The ticket's reuse inventory missed it**; call it, do not write a fresh `switch`. |
| `internal/e2e/realclaude/teardown_liveness_probe_test.go:623-753` | `tdnDecideAfter` — the classifier this one must **not** be. Nine `tdnDispositionSkipped` returns in one body (verified: 9 in this function, 20 in the file). |
| `internal/e2e/realclaude/teardown_liveness_probe_test.go:858-868` | `tdnVerdictSummary` — renders `pid=N verdict` and nothing else. Reuse for the corroboration summary. |
| `internal/e2e/realclaude/teardown_liveness_probe_test.go:404-438` | The real producer of the pyry-exit observation: the SIGTERM, then `probePyryExitGrace`, then "any after-reading would be about a live pyry". |
| `internal/e2e/realclaude/teardown_liveness_test.go:112-127` | `tdnReapOutcome` — `LineCount` is what makes the proof arm's "exactly one anchored line" precondition checkable. |
| `internal/e2e/realclaude/background_reach_probe_test.go:111-125, :945-950` | `reachMaxCommandBytes`, `reachCapCommand`. Every `Detail` goes through `trailDetail`, which wraps this. |

Not code, read before writing prose: `docs/knowledge/codebase/1270.md` (**especially § Lessons learned** — the parked SHOULD FIX this ticket inherits), `docs/specs/architecture/1270-trailer-and-attribution-admissibility.md` § Design and § Security review, and `docs/knowledge/codebase/1266.md`.

---

## Size decision — recorded, because two red lines trip

Projected: **~900–950 lines in one new file**, plus **~25 lines edited** in `trailer_admissibility_test.go` for AC5's closure test. Eleven outcomes and nine contract sub-cases give **twenty** reject branches. Both the ~600-line red line and the ~10-branch red line trip.

**There is no split.** The ticket forbids the only division on the table — AC1's closure is asserted over the union, so halving the outcome set leaves each half with an everything-else arm, the exact collapse the ticket exists to refuse. I checked for other seams and none helps:

- *Contract block vs. decision* — the contract half has no answers, so its "everything else" is the entire decision.
- *Corroboration recording vs. the outcome decision* — a genuine seam (corroboration by definition changes no outcome), but it moves ~120 lines and adds a second file's ~200-line fixed cost. Net worse across two tickets, and it is [[architect-1267-lifting-seams-does-not-shrink-an-outcome-table]] playing out again.

So the choice is one ticket or a route-back with no productive destination. Proceeding as one, on a **measurement rather than a re-count**. The six nearest comparables are this ticket's shape exactly — one additive `internal/e2e/realclaude` test file, zero production code, zero consumer edits, offline-provable — and every one landed in a single developer run with no `max_turns` label:

| Ticket | PR | Additions |
|---|---|---|
| #1230 | #1232 | +2331 |
| #1251 | #1256 | +2089 |
| #1235 | #1248 | +1886 |
| #1253 | #1257 | +1446 |
| #1266 | #1269 | +1029 |
| **#1270** | **#1272** | **+1544** (961-line file; 12 outcomes, 18 branches) |

This ticket projects **smaller than #1270**, its own blocker and closest twin. The ~600-line red line is calibrated on 2026-05-16's salvages — multi-file tickets with production code and consumer cascades. That shape is absent here.

**This is a rules-calibration observation, not a per-ticket size argument** — the same conclusion recorded at #1254, #1251 and #1270. The generic red line and this package's demonstrated single-run capacity disagree by 2–3×, and that belongs in the sizing rules, not in a paragraph on every ticket. What I *can* do about the turn budget is spend it here instead: the reading list above, the exact constant strings below, and the decision order stated arm-by-arm are what kept #1270 to one run.

---

## Context

The probe asks: **was a backgrounded command still running when pyry declared the turn finished?** A run produces a handful of raw observations, and almost every way the run can go wrong produces an observation that *looks like a negative answer*. This ticket is the piece that refuses that collapse.

Three facts shape the whole design, and each one maps to a decision below.

1. **The attribution is asymmetric and it is the only thing that can prove yes.** `ptyrunner`'s pinned teardown order writes the trailer (`emitter.Close()`) before the reap defer SIGKILLs claude's descendant groups, so a group named in pyry's reap log was alive strictly after the trailer was written. A hit proves aliveness; a **miss proves nothing**. #1270 owns deciding when a hit is admissible; this ticket owns never inverting the asymmetry.

2. **The point-in-time reads are expected to be late.** The reap completes in the time of one `ps` exec while the observation of the trailer trails the write by up to a poll interval, so a liveness read taken at observation time will usually find the group already reaped. Treated as the answer it manufactures a systematic false negative. The claude-still-alive reading is worse than useless as a timeliness witness: the reap runs *between* the trailer and claude's SIGTERM, so in exactly the window the probe exists to catch, it reports "not late". Both stay in the record; neither certifies anything.

3. **Every way of measuring nothing needs its own name.** `tdnDecideAfter` (`teardown_liveness_probe_test.go:645`) is the counter-example, and it is this ticket's whole point: nine distinct nothing-was-measured conditions in that one function — twenty across its file — all return `tdnDispositionSkipped`, separated only by a prose `DispositionDetail`. Prose is decorative; the enum value is what a consumer branches on. This classifier must not reuse `tdnDispositionSkipped` and must not be unified with `tdnDecideAfter`.

---

## Design

One new file: **`internal/e2e/realclaude/trail_run_outcome_test.go`**, `//go:build e2e_realclaude`, package `realclaude`. One bounded edit: AC5's union map in `trailer_admissibility_test.go` (§ Testing T1), plus the four-line fold-in in § The parked SHOULD FIX. Nothing else in the repository is touched — no production code, no other test file, no call site.

### Naming — census re-run at spec time

`trail*` is the family and **thirty-eight names are taken** (fifteen from #1266, twenty-three from #1270); the ticket's list is accurate, re-verified against `main`:

```
git grep -hoP '\btrail[A-Z]\w*' -- internal/ | sort -u     # exactly the thirty-eight
git grep -cP '\btrailOutcome' -- internal/ cmd/            # exits 1, no hits
git grep -cP '\btdn[A-Z]' -- internal/                     # the control: hits in five files
```

Use `-P`. This repo's `git grep -E` does not support `\b`, so an `-E` census reports zero for a symbol with hundreds of hits and verifies nothing — run the `tdn` control first so a zero for a new name is a measurement rather than a broken command.

**Sub-namespace `trailOutcome*`, string values prefixed `run-`.** The third prefix is load-bearing for the same reason `gate-`/`admit-` are: `trailAbsent`/`trailAborted` are the scan's *input states*, `trailGateNoTrailer`/`trailGateScanAborted` are the *gate's* view of them, and this space holds the *run's* view. Three spaces now mean nearly the same words, so a copy-paste between them must be a visible mistake rather than a plausible line.

New identifiers, none colliding with the thirty-eight: `trailClassifyRun`, `trailRunReadings`, `trailRunOutcome`, `trailIsRunOutcome`, `trailIsBoundFrom`, the eleven constants, and the test-side `trailRunCase` / `trailRunCases`. **Not** `trailRunObservations` — one letter from #1266's `trailObservation`, which is precisely the confusion the prefix discipline exists to prevent.

### The one doctrine: the input carries discriminators and publishable records, never captured bytes

This is the file's structural idea, and it is what makes AC2 true by construction rather than by ordering discipline. Sweep it over **every** input field, not just the one the ticket names:

| Observation | What the input holds | Why not the richer value |
|---|---|---|
| trailer admissibility | `trailGateResult` **whole** | Documented trap-free by construction (`trailer_admissibility_test.go:164-179`): no `*resultTrailer` reachable, no `Line`. |
| attribution admissibility | `trailAdmitResult` **whole** | Same, `:190-197`. |
| trailer lateness | `BoundFrom string` | `trailObservation` *embeds* `trailScanResult` (`:125-126`), so taking the observation promotes `.Trailer` straight back into reach. AC2's named live trap. |
| argv scan | `MatchCount`, `RowsScanned` ints + `ArgvScanErrored bool` | `pinScan.Matches` holds verbatim argv. A record whose value is that it can be published unreviewed must not inherit the operator-review obligation by copying one in. The error is taken as a **discriminator**, not as its text, for the same reason. |
| per-pid liveness | `[]pinStateOutcome` | Carries no command column *by construction* — `pinStateColumns` is `pid=,ppid=,stat=` with an enforcing test. This is the family's already-publishable per-pid record. |
| claude still alive | `ClaudeState string` | A `pinIsVerdict` value or `""` for "not read". Corroboration only. |
| pyry exit | `PyryExited bool` | See below. |

Consequence worth stating in the doc comment: **`Staleness` is not an input at all.** AC3 asks that corroboration discriminate on `BoundFrom` and never on `Staleness != 0`; the strongest form of that is a classifier that could not read a staleness if it wanted to.

`.Trailer` appears nowhere in the new file. Check it with a command demonstrated to find things:

```
git grep -cF '.Trailer' -- internal/e2e/realclaude/     # 15 + 4 in the two files entitled to it, 0 in the new one
```

### The records

```go
// trailRunReadings is one probe run's observations. Discriminators and
// already-publishable records only — see the table above.
type trailRunReadings struct {
	Gate            trailGateResult   // #1270
	Admit           trailAdmitResult  // #1270; the zero value means "not classified"
	ArgvScanErrored bool
	MatchCount      int
	RowsScanned     int
	Liveness        []pinStateOutcome
	PyryExited      bool
	BoundFrom       string
	ClaudeState     string
}

// trailRunOutcome is the answer plus the provenance a reader needs to interpret
// it. Counts, never rows; no command string; nothing from which .Trailer is
// reachable.
type trailRunOutcome struct {
	Value           string `json:"value"`
	Detail          string `json:"detail"`
	Gate            string `json:"gate_value"`
	Admit           string `json:"admit_value,omitempty"`
	MatchCount      int    `json:"match_count"`
	RowsScanned     int    `json:"rows_scanned"`
	Bounded         bool   `json:"lateness_bounded"`
	BoundFrom       string `json:"lateness_bound_from"`
	LivenessSummary string `json:"liveness,omitempty"`
	ClaudeState     string `json:"claude_state,omitempty"`
}
```

`Bounded` is `BoundFrom == trailBoundFromMiss` and nothing else: `trailBoundFromStart` carries a real duration that bounds nothing, and `trailBoundNone` is the honest no-bound. `LivenessSummary` is `tdnVerdictSummary(readings.Liveness)` — `pid=N verdict` pairs, no command column reachable. Reusing that renderer is not the thing #1270 declined: it declined `tdnDetail` to keep the `trail*` family out of the `tdn*` classifier's *format policy*; `tdnVerdictSummary` is a pure renderer over `pin*` records and rebuilding it would be the reuse-inventory rule failing.

**Two bools, and their zero values point in opposite directions — say so in their comments.** An unfilled `PyryExited` reads as "did not exit", which is a **void**: safe. An unfilled `ArgvScanErrored` reads as "the scan is trustworthy", which is the unsafe direction — but `pinScanArgv` returns the *zero* `pinScan` on error, so a forgotten flag arrives with both counts at zero and lands on the no-rows-parsed **void**, not on an answer. Void-to-misnamed-void is the whole exposure, and contract check C8 catches the hand-built inconsistency that would escape it. That is why neither needs to be a fourth closed string space.

### The eleven outcomes

Three answers and eight voids. The enumeration was re-derived here independently and lands at eleven, agreeing with the ticket.

| Constant | Value | Meaning |
|---|---|---|
| `trailOutcomeRunningAtTrailer` | `run-running-at-trailer` | An admissible attribution proves the group was alive when the trailer was written. **The finding.** |
| `trailOutcomeMatchedUnattributed` | `run-matched-not-attributed` | A row matched, and nothing was attributed. Never "it was leaked", never "it exited". |
| `trailOutcomeNoRowMatched` | `run-scan-matched-no-row` | The scan ran over well-formed rows and none matched. A statement about the scan, **not** about the command having exited. |
| `trailOutcomeVoidBudgetFired` | `run-void-budget-fired` | The trailer reports `max_turns`: the reap ran *before* the trailer, so no attribution on that path could prove aliveness-at-trailer. |
| `trailOutcomeVoidNoTrailer` | `run-void-no-trailer` | No trailer line was written, so there is no "when the turn was declared finished" instant to speak of. |
| `trailOutcomeVoidTrailerScanAborted` | `run-void-trailer-scan-aborted` | The trailer scan aborted — the instrument, never an answer about pyry. |
| `trailOutcomeVoidPyryDidNotExit` | `run-void-pyry-did-not-exit` | Pyry did not exit within its deadline, so every staged reading is about a live pyry. |
| `trailOutcomeVoidArgvScanErrored` | `run-void-argv-scan-errored` | The argv scan errored as an instrument. |
| `trailOutcomeVoidNoRowsParsed` | `run-void-no-rows-parsed` | The scan ran and parsed no well-formed rows at all. |
| `trailOutcomeVoidLivenessInstrument` | `run-void-liveness-instrument-failed` | A per-pid read failed as an instrument rather than answering. |
| `trailOutcomeOutOfContract` | `run-out-of-contract` | An input value outside the closed set its own producer documents, or a pair no correct composition can produce. |

Three clauses that do not look like they add values do. "No catch-all" forces the out-of-contract arm; AC4 forces **two** argv-scan voids because `pinScanArgv`'s zero-on-error means `RowsScanned == 0` cannot separate "never ran" from "ran and parsed nothing"; and #1270's aborted-≠-absent separation forces a third trailer arm.

**Gate-out-of-contract maps onto `trailOutcomeOutOfContract`, and that is not a collapse.** The gate already said "this record is not a reading"; the run-level answer is the same sentence. #1270's own precedent is explicit: three sub-cases (unknown `State`, nil `Trailer`, empty reason) share one `trailGateOutOfContract`, separated by `Detail`. What may never share a value is two *measured* nothings — and none of the eight voids does.

### The classifier

```go
// trailClassifyRun maps one run's observations onto exactly one outcome.
// Pure over its input: no exec, no clock, no filesystem, no *testing.T.
func trailClassifyRun(readings trailRunReadings) trailRunOutcome
```

**Contract block first — nine checks, all returning `trailOutcomeOutOfContract` with distinct `Detail`s.** Same structural idea as #1270: the out-of-contract value is a guard at the top, so every later arm's precondition is true by construction and no arm is a fall-through.

- **C1** `!trailIsGateValue(Gate.Value)`. A **call**, per AC1 — not a re-derivation. Catches the zero `trailGateResult`.
- **C2** `Gate.Reason != ""` must hold exactly when `Gate.Value` is `trailGateUsable` or `trailGateBudgetFired`. #1270 pins this invariant on its own output; re-checking it here is a check on the **fixture**, which is what a hand-built `trailGateResult{Value: trailGateUsable}` with no reason actually is.
- **C3** Gate certifies (`Reason != ""`) ⇒ `trailIsAdmitValue(Admit.Value)`. Not-classified is unproducible here: `tdnClassifyReapLog` is pure over already-captured bytes and *always* returns a record, instrument-failed included, so there is no run condition under which a certifying gate arrives with an unclassified attribution.
- **C4** Gate does not certify ⇒ `Admit.Value == ""`. This is #1270's test-level composition obligation (`TestTrailGateThenAdmit`) made a **checked contract at the layer that holds both values**.
- **C5** `Admit.Value == trailAdmitProof` ⇒ `Gate.Value == trailGateUsable`. Proof is producible only from a usable gate: a budget-fired gate certifies `max_turns`, which `trailAdmitAttribution` answers with the budget void. Together with C4 this closes the false-proof path § The parked SHOULD FIX describes.
- **C6** `!trailIsBoundFrom(BoundFrom)`. New six-line helper over #1266's three values; `""` is unfilled, and `trailBoundNone` is the honest report.
- **C7** every `Liveness[i].Verdict` satisfies `pinIsVerdict` (`process_pin_liveness_test.go:1142` — **call it**).
- **C8** `MatchCount < 0 || RowsScanned < 0 || MatchCount > RowsScanned`. `pinPartition` cannot emit any of these; `{MatchCount: 1}` with `RowsScanned` unfilled is the likeliest hand-typed fixture, and without this check it reaches an *answer*.
- **C9** `ArgvScanErrored && (MatchCount != 0 || RowsScanned != 0)` — `pinScanArgv` returns the **zero** `pinScan` on error (`:191-196`), so this pair is unproducible.
- `ClaudeState`, when non-empty, satisfies `pinIsVerdict`; fold into C7's block.

**No alignment check between `len(Liveness)` and `MatchCount`,** deliberately. `tdnDecideAfter` needs one because it indexes liveness against a pinned-pid list; this classifier never indexes, so a length mismatch degrades the corroboration summary and cannot mislabel a decision. Evidence-based: no observed failure, no defence.

**Then decide, in this order.** Each step's argument belongs in the doc comment.

1. `Gate.Value != trailGateUsable` → the trailer-side answer: budget-fired / no-trailer / scan-aborted / out-of-contract. **Structural, so it outranks everything.** Without a usable trailer there is no certified instant, and C5 has already made a proof unreachable from any of these values, so this ordering is forced rather than chosen.
2. `Admit.Value == trailAdmitProof` → **`trailOutcomeRunningAtTrailer`**. AC3's core: the deterministic proof is consulted **before** any point-in-time reading, because the point-in-time reads are expected to be late and must never be what the verdict rests on.
3. `!PyryExited` → `trailOutcomeVoidPyryDidNotExit`.
4. `ArgvScanErrored` → `trailOutcomeVoidArgvScanErrored`.
5. `RowsScanned == 0` → `trailOutcomeVoidNoRowsParsed`. Reachable only with `!ArgvScanErrored`, which is exactly AC4's separation.
6. any `Liveness[i].Verdict == pinStateInstrumentFailed` → `trailOutcomeVoidLivenessInstrument`.
7. `MatchCount > 0` → `trailOutcomeMatchedUnattributed`.
8. otherwise → `trailOutcomeNoRowMatched`.

**Why the proof outranks the pyry-exit void (step 2 before step 3), against the neighbouring rig's ordering.** `tdnDecideAfter`'s rig skips the run when pyry misses its exit grace (`teardown_liveness_probe_test.go:430-437`, "any after-reading would be about a live pyry") — and it is right to, because *its* verdict rests on the after-snapshot. This classifier's proof does not. It rests on an ordering argument internal to the run's own logs: the trailer was written, then the reaper named the group. Voiding it for a staging failure would **suppress a finding the run genuinely established**, and the proof arm's own preconditions rule out the confusion a still-running pyry could introduce — a second agent run's reap would push `LineCount` to 2, which `trailAdmitAttribution` answers with `trailAdmitVoidNotOneReapLine`, never proof. Every *other* reading here is staged after teardown and means nothing before it, which is why the void still outranks steps 4–8. Give this its own regression row (§ Testing T2), in `TestTrailAdmitAttribution`'s tradition: without it the two orderings are indistinguishable.

**Why the liveness instrument void is a void and the liveness verdicts are not.** An instrument failure is the absence of a reading, so it is a named nothing-was-measured. A *verdict* — running, zombie, no-such-process — is corroboration and never moves the outcome. That distinction is why step 6 tests one value and ignores the other three.

**Why the two count-based answers are named for what was observed.** `trailOutcomeNoRowMatched` says the scan matched no row. It does **not** say the command exited, and its `Detail` must say so in those words: the reap is expected to have already run by observation time, so a clean negative here is the *predicted* reading on a healthy run and on a leaking one alike. AC4's "'not observed' never falls through to 'exited normally'" is enforced by there being **no** exited-normally value in the space at all.

### The `switch` has no default, and the reason is a guard, not an omission

The gate arm is a five-case switch with no `default`: C1 already proved membership, so the five cases are total. A sixth gate value added to #1270 *and* to `trailIsGateValue` would fall through into step 2 — named in § Open questions Q2 rather than papered over with a bottom-of-function catch-all, which is the shape AC1 forbids.

### Composition, and the parked SHOULD FIX this ticket inherits

`docs/knowledge/codebase/1270.md` § Lessons learned records a code-review SHOULD FIX that survived to merge: `trailAdmitAttribution`'s `certified string` parameter gets **zero** contract checks while its sibling `reap` parameter gets three, so `certified == ""` reaches `trailAdmitProof` — a false proof from a run the gate never certified. The doc asks the next ticket touching that file to fold in the four-line fix or explicitly re-decline it. AC5 already edits that file, so the obligation is engaged, and this ticket is the gap's **first consumer**.

**Do both, because they are different defences and one is strictly stronger.**

- **Fold in** the four-line fix at #1270's layer: `certified == ""` → `trailAdmitOutOfContract`, placed with the existing contract block, with a `Detail` naming the false-proof direction. No new constant. No existing row in `TestTrailGate`, `TestTrailAdmitAttribution`, `TestTrailGateThenAdmit` or the needle test changes value — every one passes a non-empty reason. Add one row to `TestTrailAdmitAttribution`: a record that is proof on its own, with `certified: ""` → out-of-contract.
- **C4 + C5 here** are the stronger check, because they reject the *pair*: they also catch a certified reason the gate never issued, and a budget-fired gate arriving with a proof. That is the composition obligation `TestTrailGateThenAdmit` currently asserts only over its own sweep, made structural for every future caller.

This is belt-and-suspenders with different fabric only in placement, not in kind — both are code. State that honestly in the spec-facing comment rather than claiming a stochastic/deterministic split that is not there.

---

## Concurrency model

None, by design rather than omission. `trailClassifyRun` is pure over its argument: no goroutine, no channel, no lock, no shared state, no clock. It takes no `*testing.T` and never fails a test — the same contract as `trailScan`, `trailGate`, `trailAdmitAttribution`, `tdnClassifyReapLog`, `pinReadState` and `fifoLiveRead`, because an instrument failure observed mid-turn is a datum to publish, not a reason to abort the turn, and because that purity is what lets all twenty branches be driven offline from fixtures.

Run `-race` anyway for family consistency; it is expected to be uninformative — no test here spawns anything.

---

## Error handling

`trailClassifyRun` returns no `error`. Every failure mode is a named value in a closed set — that is the ticket. Each condition, and why it is not its neighbour:

| Condition | Outcome | Why not the neighbouring value |
|---|---|---|
| Any C1–C9 violation | `trailOutcomeOutOfContract` | A caller's bug reported as a void would read as a measured void — the same defect #1270's out-of-contract values exist to prevent, one layer up. |
| `Gate.Value == trailGateBudgetFired` | `…VoidBudgetFired` | Not a negative: on that path the reap ran *before* the trailer, so the attribution is void. Reporting a scan-side answer here would imply a better instrument could have proved something. |
| `Gate.Value == trailGateNoTrailer` | `…VoidNoTrailer` | Distinct from scan-aborted because a `bufio.Scanner` overflow and a genuine absence are otherwise indistinguishable — #1266's whole reason for existing, and collapsing it here spends it. |
| `Gate.Value == trailGateScanAborted` | `…VoidTrailerScanAborted` | The instrument's own breakage, never an answer about pyry. |
| `Admit.Value == trailAdmitProof` | `…RunningAtTrailer` | The only path to a finding. Every other admit value is a void or a non-answer and may never produce this. |
| `!PyryExited` | `…VoidPyryDidNotExit` | A staging fault: the argv scan and per-pid reads are about a live pyry. Ranked below the proof, above every reading — see the ordering argument. |
| `ArgvScanErrored` | `…VoidArgvScanErrored` | A failed scan must neither relabel a genuine match nor suppress a genuine "ran, nothing matched" (AC4). Its counts are zero by `pinScanArgv`'s contract, which is exactly why it cannot share a value with the next row. |
| `RowsScanned == 0`, scan did not error | `…VoidNoRowsParsed` | The instrument produced no well-formed rows. Distinct from the row above *and* from "no row matched": all three would otherwise read as an empty match set. |
| a per-pid read is `pinStateInstrumentFailed` | `…VoidLivenessInstrument` | A half-run instrument publishing an absence is a measured defect (#1230 PR #1232). Collapsing it into "no row matched" manufactures a clean negative out of the instrument's breakage. |
| `MatchCount > 0`, nothing attributed | `…MatchedUnattributed` | An inadmissible attribution is **never** evidence the group had exited (AC3). This says a row matched and the attribution is silent — no more. |
| `MatchCount == 0`, rows were scanned | `…NoRowMatched` | A statement about the scan. There is no "exited normally" value in the space, so this cannot decay into one. |

---

## Testing strategy

All test functions carry the `TestTrail` prefix so `-run '^TestTrail'` stays a zero-SKIP suite across this file and #1266's and #1270's. Every case is offline: hand-built readings plus #1270's shipped gate fixtures. No credentials, no live claude, no `t.Skip` anywhere in the file.

**T1 — extend `TestTrailAdmissibilityConstantsAreClosed`** (`trailer_admissibility_test.go:586`). Do **not** start a third closure test; #1270's is already `TestTrailConstantsAreClosed`'s shape carried across spaces, which is the property AC5 wants.

- Add this ticket's eleven constants to the union map, taking it from eighteen to **twenty-nine**. One loop still covers non-emptiness, within-space, cross-space and against-shipped distinctness.
- Add `var zeroRun trailRunOutcome` to the zero-record loop: `zeroRun.Value` may equal none of the twenty-nine. The failure mode is an unfilled field reading as a filled one.
- Assert the zero `trailRunOutcome` carries no bound: `Bounded == false` and `BoundFrom == ""`, so an unbounded record can never read as bounded.

**T2 — `TestTrailClassifyRun`**, table-driven over `trailRunCases()`. One row per outcome plus the named sub-cases; every row also asserts `trailIsRunOutcome(got.Value)` and `Detail != ""`.

- One row per answer: proof → running-at-trailer; usable gate + non-proof admit + `MatchCount: 1` → matched-unattributed; same with `MatchCount: 0, RowsScanned: 12` → no-row-matched.
- One row per void, each reached through the input that names it.
- **The ordering regression row**: a proof-shaped record with `PyryExited: false` → running-at-trailer, *not* the pyry void. Without it, steps 2 and 3 are indistinguishable.
- **The second ordering row**: a non-proof record with `PyryExited: false` *and* `ArgvScanErrored: true` → the pyry void, proving the staging fault outranks the instrument fault.
- **The late-read row**: proof, with every `Liveness` entry `pinStateNoSuchProcess` and `ClaudeState: pinStateRunning` → still running-at-trailer. This is the systematic-false-negative the ticket exists to prevent, made executable.
- **The AC4 separation pair**: `{ArgvScanErrored: true}` → scan-errored void; `{ArgvScanErrored: false, RowsScanned: 0}` → no-rows-parsed void. Both have `RowsScanned == 0`; the row comments must say that is why the flag exists.
- `MatchCount: 3, RowsScanned: 40` on the matched-unattributed row — a match set above one is an **ordinary input**, not an error (AC5).
- One row per contract check C1–C9, each named for the fixture mistake it catches (the zero gate result; a usable gate with no reason; a certifying gate with an unclassified attribution; a non-certifying gate carrying an admit value; a budget-fired gate carrying a proof; an unfilled `BoundFrom`; an invented liveness verdict; `{MatchCount: 1}` with `RowsScanned` unfilled; an errored scan reporting rows).
- At least one row builds its inputs through the **real producers** — `trailGate(trailScan(trailFixtureTrailer + "\n"))` and `trailAdmitAttribution(tdnClassifyReapLog(…), reason)` — so the happy path stays pinned to what the shipped functions emit, the same reason `TestTrailGate` routes its main rows through `trailScan`.

**T3 — `TestTrailRunCorroborationNeverFlips`.** AC3's second half, executable. Over a fixed decisive record, sweep `ClaudeState` across the four `pinState*` values and `""`, `BoundFrom` across the three values, and the liveness verdicts across the three that are **not** `pinStateInstrumentFailed` — assert `Value` is identical across the whole cross-product, and that `Bounded` is true exactly on `trailBoundFromMiss`. Run it over a proof record *and* over a no-row-matched record. The excluded fourth liveness verdict gets a comment saying why it is excluded: an instrument failure is the absence of a reading, not a disagreeing one.

**T4 — composition through #1270's fixtures.** Sweep `trailGateCases()` (`trailer_admissibility_test.go:518`), building readings the way a correct consumer would — call the predicate exactly when `gate.Reason != ""`, leave `Admit` zero otherwise — and assert that no gate fixture produces `trailOutcomeOutOfContract` *by way of C3 or C4*. That is the composition contract stated as a property of the real gate's whole value space rather than of a hand-kept list.

**T5 — `TestTrailRunOutcomeCarriesNoCapturedBytes`.** AC2's operator-review obligation, in `TestTrailAdmissibilityRecordsCarryNoCapturedBytes`'s shape (`:915`) and reusing `trailNeedle`.

- Place the needle in every string-bearing input the classifier can see: `Gate.Detail`, `Admit.Detail`, and a `pinStateOutcome`'s `Detail` and `ToolStderr`.
- `json.Marshal` the outcome; assert the needle appears nowhere.
- Assert the premise first — the row must reach the outcome it was built for — so the test cannot pass by classifying garbage.
- One structural assertion alongside the needle: the marshalled outcome contains no `command`-shaped key. The record has no field for one; this is the check that a future field does not quietly add it.

**Also add to #1270's `TestTrailAdmitAttribution`**: one row, proof-shaped record with `certified: ""` → `trailAdmitOutOfContract` (§ The parked SHOULD FIX).

**Verification recipe** — run all of it; the ticket is not done until each is green:

```bash
go vet -tags e2e_realclaude ./internal/e2e/realclaude/
gofmt -l internal/e2e/realclaude/trail_run_outcome_test.go internal/e2e/realclaude/trailer_admissibility_test.go
go test -race -tags e2e_realclaude -run '^TestTrail' -v ./internal/e2e/realclaude/
git grep --untracked -cF '.Trailer' -- internal/e2e/realclaude/
git diff --name-only origin/main
```

Notes on the recipe, each earned:

- `gofmt -l` is **dirty on `main`** for three unrelated files in this package. Scope the check to the two files you touch and do not "fix" the others.
- In the `-v` output grep for both `--- PASS` and `--- SKIP`. A `TestTrail` subtest that skips is a failure of the offline claim, not a pass.
- The `.Trailer` census must be run with `--untracked` — plain `git grep` skips your new file, which is a **vacuous pass** that flips to a failure the moment you commit (#1270 hit exactly this; `docs/knowledge/codebase/1270.md` § Lessons learned). Expected: 15 in `result_trailer_observation_test.go`, 4 in `trailer_admissibility_test.go`, **0** in the new file.
- `git diff --name-only` must list exactly three paths: the new test file, `trailer_admissibility_test.go`, and this spec.
- The whole suite runs with both credential variables unset. `TestMain` (`fixtures_test.go:348`) gates only the `GO_TEST_HELPER_PROCESS` re-exec, so no test here is credential-gated.

---

## Open questions

1. **`trailBudgetTerminalReason`'s unpinned literal is inherited, not fixed.** #1270's Q1 stands: `wireFields` (`emitter.go:428-437`) is unexported, so a rename there makes budget-fired runs report `trailGateUsable` — and this classifier would then report `trailOutcomeRunningAtTrailer` where the honest answer is `trailOutcomeVoidBudgetFired`. The consumer inherits a **false finding**, the worst direction, with nothing going red. Named here because #1271 is where the inversion becomes visible as a published verdict rather than as an intermediate value. Closing it needs an exported mapping (production change) or a captured budget-fired trailer (no fixture exists); either is its own ticket.
2. **A sixth gate or admit value added without touching this file falls through.** C1/C3 call #1270's membership predicates, so a new value that is registered there passes the guard and then finds no arm in step 1. The closure test catches a *colliding* value, not an *unhandled* one. Mitigation available today is a comment on the union map — "every value in this map has an arm in `trailClassifyRun`" — and it is a comment, not a check. Recorded rather than solved; if the value spaces ever grow, this is the first thing to revisit.
3. **Should `trailOutcomeMatchedUnattributed` distinguish which admit void it came from?** No, deliberately: `Admit` is carried verbatim in the outcome record, so the seven-way distinction is already in the artifact without a second constant space, and no consumer has asked to branch on it. Revisit only if #1268 needs to.

---

## Out of scope

- Evidence admissibility over the trailer and the reap log — **#1270**, merged. Consume `trailGateResult` and `trailAdmitResult`; do not re-derive the gate, the allowlist, or the `max_turns` literal, and do not read `trailScanResult.Trailer` here.
- Observing the trailer and bounding its lateness — landed in #1266. Take `BoundFrom` as a plain value; do not re-derive the bound and do not take the observation.
- Demonstrating the classifier flipping against real processes, live staging, artifact publication, and the finding itself — **#1268**, blocked by this ticket.
- Reading pyry's reap log — shipped in #1253. Consume `tdnClassifyReapLog`'s answer; do not re-parse pyry's stderr.
- Any `ps` read, and therefore any `command`/`args`/`comm` column. `pinStateColumns`' prohibition (`process_pin_liveness_test.go:232`) is not engaged by this design because it spawns no process at all — but AC2 carries the *matching* obligation onto the outcome record, which § Design answers structurally.
- `tdnDecideAfter` and `tdnDispositionSkipped` — not refactored, not unified, not reused. The two classifiers share their inputs but not their staging path or their outcome set, and merging outcome sets whose separation is the point would undo the ticket.
- `process_pin_liveness_test.go:603-613`'s known guard-quality gap from #1235, recorded in `docs/knowledge/codebase/1235.md`.

Per the architect's standing rule, `docs/knowledge/codebase/1271.md` is **not** a deliverable of this ticket — the documentation phase writes it from this spec plus the merged diff.

---

## Security review

**Verdict:** PASS

**Findings:**

- **[Trust boundaries]** This ticket **crosses no boundary of its own and sits downstream of every boundary in the family.** Its input is five already-parsed Go values whose parsers — `trailScan`, `tdnClassifyReapLog`, `reachScanArgv`, `pinClassifyState` — are the boundaries, are unedited here, and carry their own reviews. The adversarial question is therefore not "can hostile bytes get in" but **"can hostile bytes get through"**, and the answer is structural in both directions: the input record has no field for a command string, no field for `trailScanResult.Line`, and nothing from which `.Trailer` is reachable (`trailObservation` is refused precisely because its embedding would restore that reach); the outcome record copies no `Detail` from any input and quotes no captured string. The one residual is the corroboration summary, which routes through `tdnVerdictSummary` — a renderer that emits `pid=N verdict` and can emit nothing else, because `pinStateColumns` refuses a command column at the source. T5 asserts the whole property over `json.Marshal` with a needle placed in **four** input strings. Recorded as a property to preserve: **a future field on `trailRunOutcome` must not be a copy of an input's captured bytes.** No finding.

- **[Tokens, secrets, credentials]** The concrete exposure this family guards is the operator's `CLAUDE_CODE_OAUTH_TOKEN` / `ANTHROPIC_API_KEY` reaching a public issue through verbatim model output or a `ps` argv row. Both routes are closed here, by *different* mechanisms, which is why they are named separately. The model-output route: `trailScanResult.Line` is not an input, and #1270's two results are documented to carry no quote of it — this ticket copies neither the capped string nor its obligation. The argv route: `pinScan.Matches` is deliberately **not** an input; only `MatchCount` and `RowsScanned` cross, which is AC2's "carry counts, not rows" and is the one place a careless design would have inherited the operator-review obligation onto a record whose entire value is that it can be published unreviewed. The scan's *error* is taken as a bool for the same reason — an error string wrapping a `ps` invocation is the kind of value that grows an argv quote later. This ticket spawns no process and reads no process table, so `pinStateColumns`' prohibition is not engaged at all. No finding.

- **[File operations]** Not applicable **by design, not by omission**: the classifier opens, stats, creates and names no path, and takes no path-shaped argument. No traversal surface, no TOCTOU, no mode question, no symlink question. The artifact that eventually carries the outcome is written by #1268, under the family's existing artifact discipline.

- **[Subprocess / external command execution]** Not applicable by design: this ticket spawns nothing. No `exec.Command`, no `sh -c`, no signal handling, no environment-inheritance decision. As in `result_trailer_observation_test.go` and `trailer_admissibility_test.go`, the safe design is *not having the capability* — and it is what makes the `pinStateColumns` environment-column question unreachable rather than merely answered.

- **[Cryptographic primitives]** Not applicable: no randomness, no hashing, no key material, no comparison against a secret. Every comparison is against a non-secret closed-set constant, so constant-time comparison is not relevant.

- **[Network & I/O]** No I/O of any kind, so there is no size limit to set because nothing is read. The input-size question was answered upstream by `trailScan`'s deliberate refusal to raise `bufio.Scanner`'s 64 KiB default, and this ticket consumes the *result* of that decision (`trailOutcomeVoidTrailerScanAborted`) rather than re-opening it. The classifier is O(len(Liveness)) over a slice already in memory, with no allocation proportional to any attacker-influenced length beyond it, so there is no exhaustion surface even under a hostile input record.

- **[Error messages, logs, telemetry]** No logging, no metrics, no telemetry — deliberately, since purity is what lets all twenty branches run offline. The only strings produced are `Detail`s, each built through `trailDetail` and therefore capped by `reachCapCommand`. Two content rules are pinned rather than left to judgement: a `Detail` may name outcome values, gate and admit values, counts, pids, verdicts and `BoundFrom`; it may **never** quote `Gate.Detail`, `Admit.Detail`, a `pinStateOutcome.Detail` or its `ToolStderr`. That second rule is the one a developer is most likely to break by copying `tdnDecideAfter`, which *does* quote `out.Detail` (`teardown_liveness_probe_test.go:678`) — legitimately, because its record is not this one. T5 is the enforcing test and is the reason the needle goes into four inputs rather than one. No finding.

- **[Concurrency]** No goroutine, no channel, no lock, no shared state, no `time.Now()` — so no lifecycle question, no leak question, no lock-ordering question, and no TOCTOU, because there is no gap between check and use. Purity is a security property here and not only a testability one: a function with no clock and no I/O has no state an adversary can race. `-race` stays in the recipe for family consistency and is expected to be uninformative.

- **[Threat model alignment]** SHOULD FIX, accepted and documented as § Open questions Q1, and it is an **integrity** risk rather than a leak. The package rules this design is measured against are the redaction rule (`background_reach_probe_test.go:111-125` — followed; nothing captured is retained) and the environment-column prohibition (`process_pin_liveness_test.go:232` — not engaged). No threat in `docs/protocol-mobile.md` is reachable from a pure function over in-memory structs. What this ticket *changes* about the family's risk posture is that it is the first layer to publish a **verdict** rather than an intermediate value, which promotes two inherited inversions from latent to visible: `trailBudgetTerminalReason`'s unpinned literal (Q1) and the certified-`""` false proof (§ The parked SHOULD FIX). The second is closed here — twice, at #1270's layer and again as a pair check at this one. The first cannot be closed without a production change and is handed to #1268 and the live probes as a named limit on what a finding rests on. Naming it in the spec, in Q1 and in the constant's own doc comment is the mitigation available to a ticket that edits no production code.

**Reviewer:** architect (self-review per `architect/security-review.md`)
**Date:** 2026-08-04
