# #1270 — Is this run's trailer and reap-log evidence admissible?

**Ticket:** [#1270](https://github.com/pyrycode/pyrycode/issues/1270) — split from #1267.
**Consumer:** [#1271](https://github.com/pyrycode/pyrycode/issues/1271) (blocked by this ticket; consumes both results and owns the run-level outcome set).
**Blocker:** none. #1266 has merged (PR #1269); everything this ticket consumes is on `main`.
**Size:** S. One new file, purely additive, offline-provable, zero call sites changed.

---

## Files to read first

| Path | What to extract |
|---|---|
| `internal/e2e/realclaude/result_trailer_observation_test.go:57-90` | The two shipped closed value spaces, verbatim. Your constants must be pairwise-distinct from all six of these strings, and AC5's closure test asserts it. |
| `internal/e2e/realclaude/result_trailer_observation_test.go:98-137` | `trailScanResult` (the gate's input) and `trailObservation`. Note the **embedding** at `:126` — that is why AC2 pins the gate's *output* rather than claiming the gate owns the only read. |
| `internal/e2e/realclaude/result_trailer_observation_test.go:108-119` | The pointer trap's own doc comment. `Trailer` is nil unless `State == trailSeen`, deliberately, and the comment states why a value type would be worse. This ticket is that comment's first consumer. |
| `internal/e2e/realclaude/result_trailer_observation_test.go:164-208` | `trailScan`'s three return sites. Confirm with your own eyes that `Trailer` is set on exactly one of them — the gate's contract checks exist for hand-built fixtures, not for anything this function emits. |
| `internal/e2e/realclaude/result_trailer_observation_test.go:282-317` | `trailFixtureTrailer` (`terminal_reason: "completed"`), `trailPaddedTrailer` (`"max_turns"`, `error_max_turns`, `is_error: true`), `trailNeedle`, `trailOverlongPad`. **Reuse these; do not write new trailer fixtures.** |
| `internal/e2e/realclaude/result_trailer_observation_test.go:326-372` | `TestTrailConstantsAreClosed` — the shape AC5 says to follow, including the zero-value pin. Read how its inner `closed` helper is scoped (a closure, one space per call) so you understand why cross-space distinctness needs a different structure, not a third call. |
| `internal/e2e/realclaude/teardown_liveness_test.go:96-127` | `tdnReapHeldPGIDKilled` / `HeldPGIDAbsent` / `NoLine` / `InstrumentFailed` and the `tdnReapOutcome` record. `LineCount` (json `reap_lines_seen`) is the field AC3's "exactly one reap line" reads off. |
| `internal/e2e/realclaude/teardown_liveness_test.go:144-219` | `tdnClassifyReapLog`'s body — specifically **which (Verdict, LineCount) pairs it can emit**. This is the authority for the predicate's contract checks; read it before writing them. |
| `internal/e2e/realclaude/teardown_liveness_test.go:309-314` | `tdnDetail`. Your family gets its own equivalent — see § Design, "One detail helper". |
| `internal/e2e/realclaude/teardown_liveness_test.go:330-358` | The positive-allowlist constant block: kebab-case, each constant carrying the argument for why it is not a collapse of its neighbour. Both your blocks follow this. |
| `internal/e2e/realclaude/tool_loop_test.go:194-206` | `resultTrailer` — `TerminalReason` is the only field the gate reads. Confirm there is no `result` member; that absence is why the gate's output is publishable. |
| `internal/agentrun/streamjson/emitter.go:375-391` | The chokepoint that makes a blank `terminal_reason` unreachable on today's pyry. Cite **this**, not `wireFields`' empty `default`, in the gate's empty-reason comment. |
| `internal/agentrun/streamjson/emitter.go:428-437` | `wireFields` — `ExitReasonMaxTurns → ("error_max_turns", "max_turns", true)`. The source of the `"max_turns"` literal, and of § Open questions Q1's unpinnable-rename gap. |
| `internal/e2e/realclaude/background_reach_probe_test.go:111-125, :945-950` | `reachMaxCommandBytes`, `reachTruncationMarker`, `reachCapCommand`. The cap every `Detail` goes through. |

Not code, read before writing prose: `docs/specs/architecture/1266-result-trailer-observation.md` (§ Design and § Security review) and `docs/knowledge/codebase/1266.md`.

---

## Size decision — recorded, because two red lines nominally trip

Projected total written work: **~650–730 lines in one new file**, from **twelve** named outcomes (five gate, seven predicate — see the correction below). That is over the architect's ~600-line red line, and twelve outcomes is over the ~10-branch red line.

Proceeding as one ticket anyway, on a measurement rather than on a re-count. The five nearest comparables are the same shape as this ticket — one new `internal/e2e/realclaude` test file, purely additive, zero production code, zero consumer call sites, offline-provable from fixtures — and every one shipped in a single developer run with no `error:developer:max_turns` label:

| Ticket | PR | Additions |
|---|---|---|
| #1230 | #1232 | +2331 |
| #1251 | #1256 | +2089 |
| #1235 | #1248 | +1886 |
| #1253 | #1257 | +1446 |
| #1266 | #1269 | +1029 |

This ticket projects **smaller than all five**. The ~600-line red line is calibrated on 2026-05-16's salvages (#432 at 14 files, #446 at 6 files, #445 at 596 production lines plus a test cascade) — multi-file tickets with production code and consumer edits. That shape is absent here.

The cut that *would* be available is gate-vs-predicate, one function each. It is declined: it duplicates the file header, the fixture set and the closure test (~100 lines of pure duplication) and buys nothing the measurement above says is needed. The cut along the outcome set is refused outright, for the reason the ticket gives and #1267's architecture run already established — closure is asserted over the union, so each half grows an everything-else arm.

**This is a rules-calibration observation, not a per-ticket size argument** (same conclusion recorded at #1254): the red line and this package's demonstrated single-run capacity disagree by 2–3×, and that disagreement should be fixed in the sizing rules rather than relitigated every ticket.

### Correction to the ticket's count

Technical Notes say "Five results and six respectively." The gate is five. **The predicate is seven**, and AC3's own text is what yields it: proof, plus the five voids it names (`tdnReapInstrumentFailed`; the reaper ran without naming the group; no reap line at all; more than one reap line; budget-fired), plus the out-of-contract value it also requires ("A verdict outside the documented four is its own out-of-contract value"). AC5 confirms it by saying "including the out-of-contract one**s**", plural. Build seven. The ACs are the contract; the Technical Notes' count is an arithmetic slip.

---

## Context

The probe's strongest claim rests on pyry's own reap log, and that claim is **admissible on some code paths and void on others** with nothing in the captured bytes to tell them apart. `ptyrunner`'s pinned teardown order (`runner.go:479-485`) puts `emitter.Close()` — which writes the trailer — before the reap defer (`:398`). On that path a process group named in the reap log was alive strictly after the trailer was written, and therefore alive when it was written: a deterministic proof where a point-in-time `ps` offers a guess. But the budget's `Terminate` hook reaps *inside* the hook (`:492-503`), before the trailer, and a third reap runs on operator-cancel (`:313-316`). The reap line lands on stderr and the trailer on stdout — separate pipes, separate copier goroutines — so the bytes carry no ordering between them.

The one signal that distinguishes the paths is the trailer's own `terminal_reason`. This ticket ships the two pure predicates that read it and decide admissibility, so the run-level classifier (#1271) never has to.

Three things make a naive version wrong, and each maps to a design decision below:

1. **The trailer's `terminal_reason` sits behind a deliberate nil pointer.** `trailScanResult.Trailer` is nil unless `State == trailSeen`, chosen so a careless consumer panics loudly rather than reading `TerminalReason == ""` as a real terminal reason. This ticket is that trap's first consumer, and it cannot be the trap's last — `trailObservation` embeds `trailScanResult`, so anything holding an observation reaches `.Trailer` by promotion, and shipped code already does exactly that (`:563`, `:613`). What the gate *can* guarantee is that **its own output** is trap-free.

2. **A certification that can certify `""` reintroduces the defect one layer up.** Pyry cannot render a blank `terminal_reason` today: `Close` substitutes the recorded detail or `"unclassified"` before marshalling (`emitter.go:383-391`), so `wireFields`' empty `default` arm never reaches the wire. **Do not hunt for a live run that produces one — there isn't one.** The arm is a contract check on the gate's *input*, for two reasons: the gate's tests are fixture-driven and a hand-built `resultTrailer` with an unfilled `TerminalReason` is the likeliest fixture a developer types by accident; and a value space closed only because an `if` in another package holds is closed by coincidence, not by construction.

3. **The attribution's asymmetry inverts if a void is reported as a negative.** An attribution hit proves aliveness; an attribution miss proves nothing. Every way of failing to attribute therefore needs its own name, and none of them may read as "the group had exited."

---

## Design

One new file: **`internal/e2e/realclaude/trailer_admissibility_test.go`**, `//go:build e2e_realclaude`, package `realclaude`. Nothing else in the repository is edited — not `result_trailer_observation_test.go`, not `teardown_liveness_test.go`, not `tool_loop_test.go`, and no production code.

### Naming — census re-run at spec time

`trail*` is the family. The fifteen names #1266 took are unchanged; re-verified against `main`:

```
git grep -hoP '\btrail[A-Z]\w*' -- internal/ | sort -u     # exactly the fifteen
git grep -cP '\btrailGate|\btrailAdmit|\btrailDetail' -- internal/ cmd/   # 0, against a working -P control
```

Use `-P`. This repo's `git grep -E` does not support `\b`, so an `-E` census reports zero for a symbol with hundreds of hits and verifies nothing — confirm your own recipe finds things by running it against `\btdn[A-Z]` first.

Two sub-namespaces, `trailGate*` and `trailAdmit*`, and every string value carries a `gate-` or `admit-` prefix. That prefix is load-bearing, not cosmetic: `trailAbsent` and `trailAborted` are the scan's **input states**, while the gate produces **results** meaning "no trailer line was written" and "the trailer scan aborted". A predicate whose whole purpose is refusing to collapse distinct things must not ship a namespace where an input state and a result are one tab-completion apart, and the prefix makes a copy-paste between the two spaces a visible mistake rather than a plausible line.

### The shape both functions share: validate, then decide

Each function opens with a **contract block** that rejects any input its documented producer cannot emit, and only then decides. This is the single structural idea in the ticket and it is what makes every later arm's precondition true by construction — no arm has to defend against an impossible input, because the impossible inputs were already named and returned.

It is also what satisfies AC1's and AC3's "no value is the catch-all": the out-of-contract value is not a fall-through at the bottom of a switch, it is a **guard at the top**. A fall-through catch-all is exactly the collapse these two functions exist to refuse.

### The gate

```go
// trailGate decides whether a trailer scan result can support a claim, and
// certifies the terminal reason when it can. Pure: no exec, no clock, no
// filesystem, no *testing.T.
func trailGate(res trailScanResult) trailGateResult
```

Five values, all `gate-` prefixed:

| Constant | Meaning |
|---|---|
| `trailGateUsable` | The trailer is usable and carries a non-empty terminal reason. |
| `trailGateNoTrailer` | No trailer line was written. |
| `trailGateScanAborted` | The trailer scan aborted (the instrument, never an answer about pyry). |
| `trailGateBudgetFired` | The trailer reports a budget-fired run (`terminal_reason` is `max_turns`). Carries a certified reason. |
| `trailGateOutOfContract` | The input is not a reading. |

Decision order — behaviour only, no body:

1. **Contract.** `State` outside `{trailSeen, trailAbsent, trailAborted}` → out-of-contract. Catches the zero `trailScanResult`, whose `State` is `""`.
2. `trailAbsent` → no-trailer. `trailAborted` → scan-aborted. Neither touches `Trailer`.
3. **Contract, `trailSeen` only.** `Trailer == nil` → out-of-contract. Then, and only then, read `TerminalReason`; `""` → out-of-contract.
4. `TerminalReason == trailBudgetTerminalReason` → budget-fired, `Reason` set.
5. Otherwise → usable, `Reason` set.

Steps 1 and 3 are the whole of AC2's nil-safety claim. Read AC2 as its second clause states it — *"it dereferences no nil pointer on **any** input"* — rather than as "no deref happens before the value is decided": the seen arm must read `TerminalReason` to choose between steps 4 and 5, because AC1 requires the budget arm to be keyed on that field. What is guaranteed is that a nil `Trailer` is answered at step 3 and never reaches step 4.

The budget arm keys on `terminal_reason` **alone**. A `max_turns` run also renders `subtype: "error_max_turns"` and `is_error: true` (`emitter.go:428-437`), and consulting all three would introduce a fourth question — what to do when they disagree — for no gain. `terminal_reason` is the field the teardown path is documented against; one field, one decision.

### The gate's output is the trap-free value

```go
type trailGateResult struct {
	Value  string `json:"value"`
	Reason string `json:"terminal_reason,omitempty"` // non-empty iff Value is usable or budget-fired
	Detail string `json:"detail"`
}
```

Three properties, each a decision:

- **No `*resultTrailer` is reachable from it, directly or through an embedded field.** No pointer, no embedding, no `trailScanResult`. A consumer branching on this value holds nothing to dereference. This is the property AC2 asks to be pinned, and it is the one that is actually true — the gate cannot make itself the trap's last consumer, because promotion through `trailObservation` puts `.Trailer` back in reach of anyone holding an observation.
- **`Reason` is a plain `string`, non-empty exactly on the two arms that certify.** Deliberately not a defined type: AC2 asks for a plain non-empty string, and AC4 puts the composition's proof obligation on the tests, so a defined type would buy a weaker guarantee than the test already gives while arguing with AC2's wording.
- **Neither new record carries `trailScanResult.Line` or `tdnReapOutcome.Line`** — not as a field, and not quoted into a `Detail`. Those two strings are verbatim model output and pyry's own stderr respectively, both marked operator-review-before-paste, and copying either would propagate that obligation onto records whose whole value is that they can be published unreviewed. § Testing T5 makes this checkable rather than advisory.

### The predicate

```go
// trailAdmitAttribution decides whether pyry's reap-log attribution is
// admissible as proof that the held process group was alive when the trailer
// was written. Pure over its two inputs.
func trailAdmitAttribution(reap tdnReapOutcome, certified string) trailAdmitResult
```

`certified` is a terminal reason the gate has certified — see § Composition. `trailAdmitResult` is `{Value, Detail}`, same discipline as above.

Seven values, all `admit-` prefixed:

| Constant | Reached when |
|---|---|
| `trailAdmitProof` | Verdict is `tdnReapHeldPGIDKilled`, `LineCount == 1`, reason is not `max_turns`. **The only admissible value.** |
| `trailAdmitVoidBudgetFired` | The certified reason is `max_turns`. |
| `trailAdmitVoidInstrument` | Verdict is `tdnReapInstrumentFailed`. |
| `trailAdmitVoidNoLine` | Verdict is `tdnReapNoLine`. |
| `trailAdmitVoidGroupUnnamed` | Verdict is `tdnReapHeldPGIDAbsent` — the reaper ran and did not name the group. |
| `trailAdmitVoidNotOneReapLine` | Verdict is `tdnReapHeldPGIDKilled` with `LineCount > 1`. |
| `trailAdmitOutOfContract` | The input is not a reading. |

Decision order:

1. **Contract**, three checks, all against what `tdnClassifyReapLog` can actually emit (`teardown_liveness_test.go:144-219` is the authority — read it before writing these):
   - `Verdict` outside the documented four → out-of-contract. Catches the zero `tdnReapOutcome`.
   - `Verdict` is `tdnReapNoLine` with `LineCount != 0` → out-of-contract.
   - `Verdict` is `tdnReapHeldPGIDKilled` or `tdnReapHeldPGIDAbsent` with `LineCount < 1` → out-of-contract.
2. `certified == trailBudgetTerminalReason` → budget-fired void.
3. `tdnReapInstrumentFailed` → instrument void. `tdnReapNoLine` → no-line void. `tdnReapHeldPGIDAbsent` → group-unnamed void.
4. `tdnReapHeldPGIDKilled`: `LineCount == 1` → proof; `LineCount > 1` → not-one-line void.

Two orderings carry arguments that belong in the doc comment:

**The budget void beats every reap-side void (step 2 before step 3).** It is *structural*: when the run was budget-fired, the reap ran inside the `Terminate` hook **before** the trailer was written, so no reap line on that path could ever prove aliveness-at-trailer — the reap record's contents are irrelevant, including whether they parsed. The reap-side voids are *incidental*: had the instrument worked, or had a second line not appeared, the answer might have been proof. Reporting `trailAdmitVoidInstrument` for a budget-fired run would imply that fixing the instrument would yield proof. It would not.

**The contract block beats the budget void (step 1 before step 2).** A caller handing the predicate a record its producer cannot emit has a bug that must surface regardless of which path the run took. Same rule as the gate's step 1, applied to the same class of defect.

AC3 mandates only the first of the three contract checks. The other two are that rule applied symmetrically, and they are worth the four lines for the same reason AC1 spells out the gate's `trailSeen`-with-nil-`Trailer` case: these tests are fixture-driven, and a hand-built `tdnReapOutcome` with a plausible `Verdict` and an unfilled `LineCount` is exactly what a developer types. Without them, such a fixture reports `trailAdmitVoidNoLine` for a record that saw lines, or `trailAdmitVoidGroupUnnamed` for a record that saw none — a misreport dressed as a reading.

**A group not named is never evidence it had exited.** `trailAdmitVoidGroupUnnamed`'s doc comment must say this in those words. `tdnReapHeldPGIDAbsent` means the reaper emitted its line and the group was not on it, which is consistent with the group having exited *and* with the reaper never having reached it. The asymmetry is the point: a hit proves aliveness, a miss proves nothing.

### Composition — AC4

The predicate takes a `string` the gate certified, not a `trailGateResult`. Taking the gate result would force the predicate to answer for the three reason-less gate values and grow an eighth outcome — the exact collapse the ticket refuses.

So the composition is a **test-level** obligation, which is where AC4 puts it: *"the tests prove that composition."* T4 drives gate-then-predicate over the gate's five values and asserts that the predicate is called only when `Reason != ""`, i.e. never through `trailGateNoTrailer`, `trailGateScanAborted` or `trailGateOutOfContract`.

The budget-fired value is what makes the predicate's `max_turns` arm **live rather than unreachable-by-construction**: it is a gate value that *does* carry a certified reason, and that reason is `max_turns`, so T4 reaches `trailAdmitVoidBudgetFired` through a real gate result rather than through a hand-typed string.

### One detail helper, one budget literal

- `trailDetail(format string, args ...any) string` — `reachCapCommand(fmt.Sprintf(...))`, three lines. Twelve arms make a helper worth it where #1266's three did not, and it is deliberately **not** a call to `tdnDetail`: the `trail*` family stays out of the `tdn*` teardown classifier's reach, which is the point of a distinct prefix. Cap anyway even though these `Detail`s quote no captured bytes — `tdnReapOutcome.PGIDs` is unbounded, and the family's rule is that every retained operator-visible string is capped.
- `trailBudgetTerminalReason = "max_turns"` — a **string literal in this file**, with a doc comment citing `emitter.go:429-431` and naming the inversion risk in § Open questions Q1. Same discipline as `tdnReapMessage` (`teardown_liveness_test.go:81-89`): a rename in production must not be silently followed.

---

## Concurrency model

None, and by design rather than omission. Both functions are pure over their arguments: no goroutine is spawned, no channel is used, no lock is taken, no shared state is read or written. They take no `*testing.T` and never fail a test — the same contract as `trailScan`, `tdnClassifyReapLog`, `pinReadState` and `fifoLiveRead` — because an instrument failure observed mid-turn is a datum to publish, not a reason to abort the turn, and because that purity is what lets every one of the twelve arms be driven offline from fixtures.

Unlike #1266, this ticket touches no `probeSyncBuffer` and spawns no polling goroutine, so no test here needs `-race` to cover a write-while-read boundary. Run `-race` anyway for consistency with the family's recipe; it is expected to be uninformative.

---

## Error handling

Neither function returns an `error`. Every failure mode is a **named value in a closed set** — that is the ticket. The mapping, and why each is not its neighbour:

| Condition | Value | Why not the neighbouring value |
|---|---|---|
| `State` outside the documented three (incl. the zero value) | `trailGateOutOfContract` | Reporting it as absent would file a caller's bug under "pyry never finished the turn". |
| `State == trailSeen`, `Trailer == nil` | `trailGateOutOfContract` | `trailScan` cannot emit this. Reporting usable would then deref nil; reporting absent would contradict the state the record itself claims. |
| `State == trailSeen`, `TerminalReason == ""` | `trailGateOutOfContract` | Certifying `""` reintroduces, one layer up, the exact defect the nil pointer was chosen to prevent. No live repro exists (`emitter.go:383-391`); this is a contract check on the input, and the comment must say so rather than implying pyry can emit it. |
| `State == trailAborted` | `trailGateScanAborted` | Distinct from no-trailer because `bufio.Scanner` overflow and a genuinely absent trailer are otherwise indistinguishable — #1266's whole reason for existing. |
| `terminal_reason == "max_turns"` | `trailGateBudgetFired` | Not usable: on that path the reap ran before the trailer, so the attribution is void, **not negative**. Still certifies its reason, because the predicate needs it. |
| `Verdict` outside the documented four (incl. the zero value) | `trailAdmitOutOfContract` | A void by default would let a caller's bug read as a measured void. |
| `Verdict` / `LineCount` pair `tdnClassifyReapLog` cannot emit | `trailAdmitOutOfContract` | Otherwise a fixture with an unfilled `LineCount` reports a void that describes lines it never saw. |
| certified reason is `max_turns` | `trailAdmitVoidBudgetFired` | Structural: no reap line on that path could prove aliveness-at-trailer, so this outranks every incidental void. |
| `tdnReapInstrumentFailed` | `trailAdmitVoidInstrument` | A broken instrument is never a statement about pyry. Collapsing it into group-unnamed manufactures a leak finding out of the instrument's own breakage. |
| `tdnReapNoLine` | `trailAdmitVoidNoLine` | Ambiguous by construction (`reap.go:64` guards the emit on `len(reaped) > 0`), so it is a void — never evidence the group had exited. |
| `tdnReapHeldPGIDAbsent` | `trailAdmitVoidGroupUnnamed` | **Never** evidence the group had exited. Consistent with the group exiting *and* with the reaper not reaching it. |
| `tdnReapHeldPGIDKilled`, `LineCount > 1` | `trailAdmitVoidNotOneReapLine` | Two anchored lines leave it unestablished which reap named the group, so the ordering argument that makes the hit a proof does not close. Not the same as no line at all. |

---

## Testing strategy

All test functions carry the `TestTrail` prefix so `-run '^TestTrail'` stays a zero-SKIP suite across this file and #1266's. Every case is offline: hand-built records and #1266's shipped fixtures, no credentials, no live claude, no `t.Skip` anywhere in the file.

**T1 — `TestTrailAdmissibilityConstantsAreClosed`.** AC5's structural claim, executable. A *new* test in the new file following `TestTrailConstantsAreClosed`'s shape — do not edit #1266's test.

- Build **one** name→value map over all eighteen constants: the five gate values, the seven admit values, and the six shipped ones (`trailSeen`/`trailAbsent`/`trailAborted`, `trailBoundFromMiss`/`trailBoundFromStart`/`trailBoundNone`). Assert every value is non-empty and no two names share a value. One loop covers within-space, cross-space *and* against-shipped distinctness — which is why this is a single union map rather than a third call to a one-space-at-a-time helper.
- Pin the zero `trailGateResult` and the zero `trailAdmitResult`: neither `.Value` may equal any of the eighteen. The failure mode is an unfilled field reading as a filled one, not two constants colliding.
- Assert the zero `trailGateResult.Reason` is empty, so an uncertified record can never read as certified.

**T2 — `TestTrailGate`**, table-driven, one row minimum per value and one per named sub-case:

- `trailScan(trailFixtureTrailer)`'s result → usable, `Reason == "completed"`. Feed it through the real `trailScan`, not a hand-built record, so the happy path is pinned to what the shipped scan actually emits.
- `trailScan(trailPaddedTrailer(2000))`'s result → budget-fired, `Reason == "max_turns"`. Also through the real scan.
- `trailScan(trailFixtureNoTrailer)`'s result → no-trailer, `Reason == ""`.
- `trailScan(trailPaddedTrailer(trailOverlongPad))`'s result → scan-aborted, `Reason == ""`.
- Hand-built `trailScanResult{State: "some-state-nobody-defined"}` → out-of-contract.
- The zero `trailScanResult{}` → out-of-contract. Named separately from the row above because the zero value is the realistic accident.
- Hand-built `trailScanResult{State: trailSeen, Trailer: nil}` → out-of-contract, **and the test must not panic**. This row is AC2's headline.
- Hand-built `trailScanResult{State: trailSeen, Trailer: &resultTrailer{Type: "result"}}` (empty `TerminalReason`) → out-of-contract.
- Every row also asserts `Detail != ""`, and that `Reason` is non-empty **iff** the value is usable or budget-fired.

**T3 — `TestTrailAdmitAttribution`**, table-driven, one row per value plus the sub-cases:

- `{Verdict: tdnReapHeldPGIDKilled, LineCount: 1}` with reason `"completed"` → proof.
- The same record with reason `"max_turns"` → budget void. Proves step 2 outranks a record that would otherwise be proof.
- `{Verdict: tdnReapInstrumentFailed, LineCount: 1}` with reason `"max_turns"` → budget void, **not** instrument void. This row is the ordering argument's regression guard; without it the two orderings are indistinguishable.
- `{Verdict: tdnReapInstrumentFailed, LineCount: 1}` with `"completed"` → instrument void.
- `{Verdict: tdnReapNoLine, LineCount: 0}` → no-line void.
- `{Verdict: tdnReapHeldPGIDAbsent, LineCount: 1}` → group-unnamed void.
- `{Verdict: tdnReapHeldPGIDKilled, LineCount: 2}` → not-one-line void.
- The zero `tdnReapOutcome{}` → out-of-contract.
- `{Verdict: "some-verdict-nobody-defined"}` → out-of-contract.
- `{Verdict: tdnReapNoLine, LineCount: 3}` → out-of-contract.
- `{Verdict: tdnReapHeldPGIDKilled, LineCount: 0}` → out-of-contract.
- Every row asserts `Detail != ""`.

At least one row should build its `tdnReapOutcome` by calling `tdnClassifyReapLog` over synthetic stderr rather than by hand, so the predicate is pinned to a record its real producer emits — the same reason T2 routes its four main rows through `trailScan`.

**T4 — `TestTrailGateThenAdmit`.** AC4's composition, driven over the gate's five values:

- For each gate fixture from T2: if `Reason == ""`, assert the value is one of no-trailer / scan-aborted / out-of-contract and assert the predicate is **not** called. If `Reason != ""`, call the predicate with a fixed admissible `tdnReapOutcome` and record the value reached.
- Assert the usable path reaches `trailAdmitProof` and the budget-fired path reaches `trailAdmitVoidBudgetFired` — the latter being AC4's demand that the `max_turns` arm is reached through a real gate result, not a hand-typed string.
- Assert that across the whole sweep the predicate was invoked exactly twice.

**T5 — `TestTrailAdmissibilityRecordsCarryNoCapturedBytes`.** AC5's operator-review obligation, made checkable:

- Run the gate over a `trailScanResult` whose `Line` carries `trailNeedle` and whose `Trailer` is well-formed. `json.Marshal` the gate result; assert the needle appears nowhere in the bytes.
- Run the predicate over a `tdnReapOutcome` whose `Line` carries `trailNeedle`. `json.Marshal` the result; assert the same.
- Both assertions are deterministic: the needle can only reach either record by a field copy or a `Detail` quoting the input's captured string, and neither record does either.

**Verification recipe** (run all five; the ticket is not done until each is green):

```bash
go vet -tags e2e_realclaude ./internal/e2e/realclaude/
gofmt -l internal/e2e/realclaude/trailer_admissibility_test.go    # must print NOTHING
go test -race -tags e2e_realclaude -run '^TestTrail' -v ./internal/e2e/realclaude/
git grep -nP '\.Trailer\b' -- internal/ cmd/ | grep -v result_trailer_observation_test.go   # must print NOTHING
git diff --name-only origin/main    # exactly the new file + this spec
```

Notes on the recipe, each worth heeding:

- `gofmt -l` is **dirty on `main`** for three unrelated files in this package. Scope the check to the new file and do not "fix" the others.
- In the `-v` output grep for both `--- PASS` and `--- SKIP`. A `TestTrail` subtest that skips is a failure of the offline claim, not a pass.
- The `.Trailer` grep is AC2's check. Its baseline was measured at spec time: **fifteen hits, all in `result_trailer_observation_test.go`**, which AC2 exempts as #1266's own tests of the scan. Run it with `-P`, and confirm the command finds things by dropping the `grep -v` first — an empty result from a broken command is the failure mode this recipe exists to avoid. Note the consequence for your own code: **the gate reads `Trailer` and must therefore live in the new file**, and any helper you are tempted to factor out that also reads it must live there too.
- The whole suite runs with both credential variables unset. The package's `TestMain` (`fixtures_test.go:348-354`) gates only the `GO_TEST_HELPER_PROCESS` re-exec, never credentials, so the skip is per-test and none of these tests is gated.

---

## Open questions

1. **The `"max_turns"` literal has no executable pin to its producer, and the failure direction is the bad one.** `wireFields` (`emitter.go:428-437`) is unexported, so no test in this package can assert `trailBudgetTerminalReason` equals what pyry actually renders. If someone renames the wire value, this gate silently stops recognising budget-fired runs and reports them as `trailGateUsable` — a **false proof**, the worst direction, with nothing going red. Pinning it would need either an exported mapping (a production change, out of scope for a probe-family ticket) or a trailer captured from a real budget-fired run (no such fixture exists; #1266's `trailPaddedTrailer` is hand-built, and capturing one is live-probe territory). Named here rather than papered over. If a downstream finding ever turns on this, it needs its own ticket.
2. **Should the two `Detail` vocabularies be shared?** Deferred. Twelve arms across two functions could share phrasing helpers, but the two spaces are deliberately non-confusable and a shared vocabulary would work against that. Revisit only if #1271's own outcome details end up duplicating these verbatim.
3. **Does `trailAdmitVoidNotOneReapLine` want to distinguish two lines from twenty?** No. `tdnReapOutcome` already carries `LineCount` and `PGIDs`, so the count is in the `Detail` without a second constant, and no consumer has asked to branch on the magnitude.

---

## Out of scope

- The run-level classifier that consumes both results and maps a run's observations onto a closed outcome set — **#1271**, blocked by this ticket. Do not anticipate its outcome set here.
- Observing the trailer and bounding its lateness — landed in #1266. Consume `trailScan` / `trailScanResult` including the aborted state; do not re-derive them, and do not edit `parseResultTrailer` or any of its nine call sites.
- Reading pyry's reap log — shipped in #1253. Consume `tdnClassifyReapLog`'s answer including its no-line arm and its line count; do **not** re-parse pyry's stderr here.
- Live staging, artifact publication, and the finding itself — the live probes downstream of #1271.
- `process_pin_liveness_test.go:603-613`'s known guard-quality gap from #1235, recorded in `docs/knowledge/codebase/1235.md`.
- Any `ps` read, and therefore any `command`/`args`/`comm` column. `pinStateColumns`' prohibition (`process_pin_liveness_test.go:232`) is not engaged by this design because it spawns no process at all.

Per the architect's standing rule, `docs/knowledge/codebase/1270.md` is **not** a deliverable of this ticket — the documentation phase writes it from this spec plus the merged diff.

---

## Security review

**Verdict:** PASS

**Findings:**

- **[Trust boundaries]** This ticket sits **downstream of every boundary in the family and crosses none of its own.** Both functions take already-parsed Go values — a `trailScanResult` and a `tdnReapOutcome` — and the parsers that produced them (`trailScan`, `tdnClassifyReapLog`) are the boundary, are unedited here, and carry their own reviews. The adversarial question is therefore not "can hostile bytes get in" but **"can hostile bytes get *through*"**: the inputs both carry a captured string (`trailScanResult.Line` is verbatim model output, `tdnReapOutcome.Line` is pyry's stderr), and either would inherit the operator-review-before-paste obligation onto a record whose entire value is that it can be published unreviewed. The design answers structurally — neither output record has a field for either string, and no `Detail` quotes one — and T5 asserts it over `json.Marshal` with a needle placed in both inputs. Recorded as a property to preserve: **a future field added to either result record must not be a copy of an input's captured bytes.** No finding.

- **[Tokens, secrets, credentials]** The specific exposure this family guards is the operator's `CLAUDE_CODE_OAUTH_TOKEN` / `ANTHROPIC_API_KEY` reaching a public issue through verbatim model output or a `ps` argv row. Both routes are closed here and by different mechanisms, which is why they are named separately: the model-output route is closed by the field-set argument above (and #1266's own [Tokens] finding, which capped `Line` at 512 bytes with a truncation marker, is *inherited* and not widened — this ticket copies neither the capped string nor its obligation); the argv route is closed because this ticket spawns no process and reads no process table, so `pinStateColumns`' prohibition is not engaged at all. `Reason` is pyry's own closed enum (`completed` / `max_turns` / `unclassified` / a recorded detail), never model text. `Detail` strings quote counts, pgids, verdicts and the terminal reason — never a captured line — and still go through `reachCapCommand`, because `tdnReapOutcome.PGIDs` is unbounded and defence in depth costs three lines. No finding.

- **[File operations]** Not applicable by design, not by omission: neither function opens, stats, creates or names a path, and neither takes a path-shaped argument. No traversal surface, no TOCTOU, no mode question, no symlink question.

- **[Subprocess / external command execution]** Not applicable by design: this ticket spawns nothing. No `exec.Command`, no `sh -c`, no signal handling, no environment-inheritance decision. As in `result_trailer_observation_test.go`, the safe design here is *not having the capability*.

- **[Cryptographic primitives]** Not applicable: no randomness, no hashing, no key material, and no comparison against a secret. The only comparisons are against non-secret closed-set constants, so constant-time comparison is not relevant.

- **[Network & I/O]** No I/O of any kind — no socket, no reader, no size limit to set, because nothing is read. The input-size question was answered upstream by `trailScan`'s deliberate refusal to raise `bufio.Scanner`'s 64 KiB default, and this ticket consumes the *result* of that decision (`trailGateScanAborted`) rather than re-opening it. Both functions are O(1) over fields already in memory, so there is no resource-exhaustion surface even under a hostile input record.

- **[Error messages, logs, telemetry]** No logging, no metrics, no telemetry — deliberately, since both functions must stay pure to be driven offline. The only strings either produces are `Detail`s, every one built through `trailDetail` and therefore capped. Two content rules are pinned above rather than left to judgement: a `Detail` may name `Verdict`, `LineCount`, `Count`, `PGIDs`, `HeldPGID`, `State` and the terminal reason; it may **never** quote `trailScanResult.Line` or `tdnReapOutcome.Line`. T5 is the enforcing test. Neither function returns an `error`, so there is no error path whose text could carry input bytes.

- **[Concurrency]** No goroutine, no channel, no lock, no shared state, no `time.Now()` — so no lifecycle question, no leak question, no lock-ordering question, and no TOCTOU, since there is no gap between check and use to exploit. Purity here is a security property and not only a testability one: a function with no clock and no I/O has no state an adversary can race. `-race` stays in the recipe for family consistency and is expected to be uninformative.

- **[Threat model alignment]** SHOULD FIX, accepted and documented as § Open questions Q1. The package rules this design is measured against are the redaction rule (`background_reach_probe_test.go:111-125` — followed; nothing captured is retained) and the environment-column prohibition (`process_pin_liveness_test.go:232` — not engaged). No threat in `docs/protocol-mobile.md` is reachable from two pure functions over in-memory structs. The one *integrity* risk this ticket adds to the family is not a leak but an **inversion**: `trailBudgetTerminalReason` is a string literal with no executable pin to `wireFields` (`emitter.go:428-437`, unexported), so a rename there would make budget-fired runs report `trailGateUsable` and turn a structural void into a false proof, silently. That is the direction that manufactures a finding rather than suppressing one, which is why it is named in the spec, in Q1, and in the constant's own doc comment rather than being treated as a routine literal. Closing it needs either a production change (exporting the mapping) or a captured budget-fired trailer, both out of scope for a probe-family ticket that edits no production code; it is handed to #1271 and the live probes as a known limit on what an admissible-vs-void answer rests on.

**Reviewer:** architect (self-review per `architect/security-review.md`)
**Date:** 2026-08-03
