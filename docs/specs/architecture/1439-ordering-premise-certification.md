# #1439 — Certify the pinned-pid ordering argument on its three premises, naming the void per failed premise (offline)

**Ticket:** [#1439](https://github.com/pyrycode/pyrycode/issues/1439) · size `s` · `security-sensitive`
**Split from:** #1436 (closed). Sibling: #1440 (the consumer). Adjacent: #1437 (owns `trailRunReadings`'s shape).

---

## Files to read first

Turn-1 data load. Read these before writing anything; the design below is expressed in their vocabulary.

| Path | What to extract |
|---|---|
| `internal/e2e/realclaude/trailer_admissibility_test.go:104-234` | The two shipped value spaces verbatim — seven `gate-*`, seven `admit-*`. AC4's rejection list draws from here, and the new `order-*` values must be pairwise-distinct from all fourteen. Also the house style for a value's doc comment: what it says, and what it explicitly refuses to say. |
| `internal/e2e/realclaude/trailer_admissibility_test.go:297-343` | `trailGateResult` and `trailAdmitResult`. **`trailAdmitResult` (`:340-343`) is the shape this ticket copies exactly** — `Value` + `Detail`, nothing else. Read `trailGateResult`'s field docs for why a third field has to earn its place. |
| `internal/e2e/realclaude/trailer_admissibility_test.go:345-354` | `trailDetail` — the only Detail formatter this family uses, and its 512-byte cap via `reachCapCommand`. |
| `internal/e2e/realclaude/trailer_admissibility_test.go:358-427` | `trailGate`'s doc block and its signature. `:361-365` is the purity contract AC1 names verbatim: no exec, no clock, no filesystem, no `*testing.T`, never fails a test. Copy its shape, not its words. |
| `internal/e2e/realclaude/trailer_admissibility_test.go:1164-1304` | `TestTrailAdmissibilityConstantsAreClosed` — the union map this ticket joins. Note it prints **no count literal** (`:1172-1176` says why), that the loop at `:1248-1261` is what catches an empty or colliding value, and that the zero-record walk at `:1263-1293` is per-record-type. Two prose sentences go stale when a fifth space joins — see § Scope. |
| `internal/e2e/realclaude/trail_run_outcome_test.go:100-220` | The thirteen `run-*` values. Read `:108-113` for the sub-namespace-prefix argument — it is the reason this ticket's values carry `order-`. |
| `internal/e2e/realclaude/trail_run_outcome_test.go:155-163` | `trailOutcomeVoidLivenessInstrument`'s doc — the "would manufacture a clean negative out of the instrument's breakage" sentence the ticket quotes. This spec applies the same reasoning one layer up. |
| `internal/e2e/realclaude/trail_run_outcome_test.go:222-270` | `trailRunReadings`. **Read-only for this ticket.** #1437 owns its shape; do not add a hold field here or anywhere in this file. |
| `internal/e2e/realclaude/trail_run_outcome_test.go:1571-1627` | `TestTrailRunOutcomeCarriesNoCapturedBytes`. The structural half at `:1609-1626` (marshal → decode to `map[string]json.RawMessage` → walk keys against `command`/`args`/`comm`/`argv`) is the shape AC5 names. Note the premise assertion at `:1594-1599` — the test asserts the fixture classified as expected *before* it asserts anything about the bytes. |
| `internal/e2e/realclaude/trail_run_outcome_test.go:1629-1678` | `trailRunOutcomeValues` + `TestTrailRunOutcomeValuesAgreeWithThePredicate`. **This is AC4's named shape**: list, both directions, a count asserted against the ticket's own enumeration, and a rejection loop over the adjacent spaces' values. |
| `internal/e2e/realclaude/background_trigger_probe_test.go:647-663` | `holdProbeFIFO`'s doc. `:652-655` is the load-bearing fact: the write end never leaves the helper and the only release is its own `t.Cleanup`, which runs after the subtest body. That is what makes "the hold was held for the whole of the wait" something a caller in the subtest body can *know* rather than assume. |
| `internal/e2e/realclaude/finding_stage_held_group_test.go:168-188` | `finStageSubject` — needles, pinned pids, a group, a row count. **No hold boolean.** Confirms the ticket's claim that no shipped gather records the fact, so it is a caller-supplied parameter. |
| `internal/e2e/realclaude/finding_stage_held_group_test.go:202-260` | `finStageHeldGroup` — how a hold is actually staged, and the defer-vs-`t.Cleanup` ordering argument. Context for what the caller is asserting when it passes `holdHeld=true`. |
| `internal/e2e/realclaude/finding_run_gather_test.go:372-384` | `finSighting` — the **wrong** input. Its `TerminalReason` / `StopReason` / `Subtype` / `KeyNames` marshal as `terminal_reason` / `stop_reason` / `subtype` / `trailer_keys`, none of which is `command`-shaped, so AC5's key sweep would not catch it. The parameter type is the enforcement. |
| `internal/e2e/realclaude/result_trailer_observation_test.go:267-299` | `trailWaitForTrailer` — the sighting supplier. This predicate consumes the *fact that* it returned a `trailSeen` observation, never the observation. |
| `internal/e2e/realclaude/result_trailer_observation_test.go:325` | `trailNeedle`, and the reason AC5 does **not** use it here. |
| `internal/e2e/realclaude/background_reach_probe_test.go:119-124` and `:945-950` | `reachMaxCommandBytes = 512`, `reachTruncationMarker`, `reachCapCommand`. Truncation is silent past the cap — the Details below are sized to stay well under it. |
| `internal/e2e/realclaude/finding_staging_gate_test.go:519-574` | `TestFinOutcomeConstantsAreClosed` — the *other* closure shape in this package (a self-contained per-family map). Read it to understand why this ticket joins the union map instead; § Design records the choice. |
| `CODING-STYLE.md` § Testing | Table-driven, `t.Run`, stdlib `testing` only, no testify. |

---

## Context

A staged probe run holds its command un-finishable — blocked on a FIFO nobody writes to — for the whole of the wait on pyry's exit. Three instants are then ordered by construction: the trailer is sighted on pyry's stdout, then pyry exits, then the rig re-reads a pid it pinned while the command was still reachable. That ordering is what lets a later reading of the pid say something about the earlier sighting.

The ordering is only *by construction* if three things actually held, and each can fail independently. Each failure is the rig's own, not pyry's — so each is an **unmeasured premise**, never a soft negative. Reporting a soft negative out of the rig's own breakage is the collapse this family already refuses in code (`trail_run_outcome_test.go:159-163`).

This ticket ships the predicate that certifies the ordering or refuses with a named reason. It is the smaller half of #1436's split; #1440 is the consumer that builds the aliveness claim on top of it.

**This predicate makes no claim about any command's liveness.** It says only that the three instants were ordered, or that they were not and which premise is missing. That is why it stands alone and is sound alone: it cannot publish a wrong verdict about a process, because it publishes no verdict about a process.

---

## Design

### Where it lives

One new file: **`internal/e2e/realclaude/trail_ordering_premises_test.go`**, carrying `//go:build e2e_realclaude` and `package realclaude`, with a file header in `trail_run_outcome_test.go:5-91`'s shape — the question this file answers, the doctrine, and what it reuses rather than rebuilds.

Plus a ~12-line edit to `trailer_admissibility_test.go`'s union map (§ Scope). **No other file is touched.** In particular `trail_run_outcome_test.go` is read-only here: `trailRunReadings`'s shape belongs to #1437.

### The value space — four values, `order-` prefixed

| Constant | Value | Means |
|---|---|---|
| `trailOrderCertified` | `order-certified` | All three premises held. The three instants were ordered by construction. |
| `trailOrderVoidUnsighted` | `order-void-trailer-unsighted` | The trailer was not sighted, so there is no earlier instant for anything to be about. |
| `trailOrderVoidNoExit` | `order-void-pyry-did-not-exit` | Pyry did not exit within its deadline, so the later read is not later than anything. |
| `trailOrderVoidUnheld` | `order-void-hold-released` | The hold was not held for the whole of the wait, so the pinned pid may no longer name the same process. |

Three naming constraints, each load-bearing:

1. **The `order-` prefix is a fourth sub-namespace, and it is not cosmetic.** `order-void-pyry-did-not-exit` and the shipped `run-void-pyry-did-not-exit` (`trail_run_outcome_test.go:147`) mean nearly the same words one layer apart. The prefix is what makes a copy-paste between them a visible mistake in a published record rather than a plausible line — the same argument `trail_run_outcome_test.go:108-113` makes for `run-`.

2. **The Go identifiers deliberately avoid the `trailOutcomeVoid…` shape.** `trailOrderVoidNoExit`, not `trailOrderVoidPyryDidNotExit`: the latter differs from the shipped `trailOutcomeVoidPyryDidNotExit` only in the middle, both autocomplete from `trailO`, and both are untyped strings, so the compiler catches nothing. Naming the void after the missing premise rather than after the event removes the pair.

3. **There is deliberately no `order-out-of-contract` value, and the file must say so.** Every sibling space has one, reached by a guard at the top. Here the parameter list is three `bool`s, so all eight inputs are readings by construction and there is nothing such a value could be about. An unreachable named value is worse than none: no fixture can reach it, so a row asserting it would be unreachable-red, and its presence would make the space's count a lie about what the predicate can answer. State this in the const block's doc so a reviewer reads a decision rather than an omission.

### The record

```go
type trailOrderResult struct {
	Value  string `json:"value"`
	Detail string `json:"detail"`
}
```

Exactly `trailAdmitResult`'s shape (`trailer_admissibility_test.go:340-343`). No third field: there is no reason to certify here, and the three input booleans reach the reader through the Detail's fixed clause below rather than as fields. Trap-free by construction and trivially so — no pointer, no embedded type, and the function that fills it can see no captured byte at all.

### The predicate

```go
func trailCertifyOrdering(trailerSighted, pyryExited, holdHeld bool) trailOrderResult
```

Pure over its input: no exec, no clock, no filesystem, no `*testing.T`, and it never fails a test — the same contract `trailGate` states at `trailer_admissibility_test.go:361-365`. Every arm is therefore driven offline from fixtures.

Three plain booleans, per AC1, and the parameter type is the enforcement rather than a convention. A `finSighting` parameter would carry `TerminalReason` / `StopReason` / `Subtype` / `KeyNames` straight into reach, and AC5's key sweep would not see them — they marshal as `terminal_reason` / `stop_reason` / `subtype` / `trailer_keys`, none `command`-shaped. Narrowing the parameter is what forecloses that; widening a denylist could not.

**Body shape: three guards then a fall-through, four return sites.** `if !holdHeld` → `if !trailerSighted` → `if !pyryExited` → certified. The certified arm is the fall-through, so no arm is a defensive default that no fixture reaches. Each arm returns through a `decide(value, format, args...)` closure that fills `Detail` via `trailDetail`, in `trailClassifyRun`'s shape (`trail_run_outcome_test.go:410-414`).

### Precedence — pinned, and argued

**hold → sighting → exit.**

The hold outranks both because its failure removes the *subject*, not merely an instant. Held on a FIFO nobody writes to, the command cannot finish, so the pinned pid still names the same process at the later read. Release the hold and the pid could have been retired and reissued between the sighting and the read — at which point the later reading is about some other process entirely. The other two failures leave the subject intact and remove one endpoint of the ordering. Same structural-outranks-situational rule `trailAdmitAttribution` applies when it puts `trailAdmitVoidBudgetFired` above every reap-side void (`trailer_admissibility_test.go:212-215`).

Sighting outranks exit because it is the earlier instant: with no earlier instant, whether the later one completed is moot.

The precedence is *chosen* here and *checked* by the eight-row table below — which is the point of enumerating all eight rather than one representative pair.

### The full assignment rule — all eight combinations

| # | `trailerSighted` | `pyryExited` | `holdHeld` | Result | Why |
|---|---|---|---|---|---|
| 1 | true | true | true | `trailOrderCertified` | All three premises held. |
| 2 | **false** | true | true | `trailOrderVoidUnsighted` | Single failure. |
| 3 | true | **false** | true | `trailOrderVoidNoExit` | Single failure. |
| 4 | true | true | **false** | `trailOrderVoidUnheld` | Single failure. |
| 5 | **false** | **false** | true | `trailOrderVoidUnsighted` | Sighting outranks exit. |
| 6 | **false** | true | **false** | `trailOrderVoidUnheld` | Hold outranks sighting. |
| 7 | true | **false** | **false** | `trailOrderVoidUnheld` | Hold outranks exit. |
| 8 | **false** | **false** | **false** | `trailOrderVoidUnheld` | Hold outranks both. |

Rows 5–8 are the four AC2 names: three double-failure pairs plus the triple. A single two-failure fixture would pin one of the four and leave three to judgement.

### Detail content rule — pinned, not left to judgement

Each Detail:

- **MAY** name the premise that decided, the value names in this space, and — when it outranked another failure — the precedence argument for why.
- **MUST NOT** claim anything about a command's liveness, or about the turn having been declared finished. Neither instant is established by this predicate, and a Detail claiming either would say one layer up exactly what the value space refuses to say.
- **MUST** end with the fixed clause `premises: trailer-sighted=%t pyry-exited=%t hold-held=%t`, filled from the three parameters. This is what carries the co-failures on rows 5–8 without a second field, a helper, or a per-row judgement call. It costs ~55 bytes.
- Cannot carry a command string, argv or trailer value, and this is structural rather than disciplined: the function can see none.

All four go through `trailDetail`, so all inherit the 512-byte cap (`background_reach_probe_test.go:123`), which truncates silently. Budget each arm's prose to ~350 bytes so the fixed clause plus the argument both survive. The nearest shipped comparable is 225 bytes (`finding_key_name_containment_test.go:559`), so the headroom is real — but `trailClassifyRun`'s own Details run 300–400 bytes, so this is a real budget and not a formality.

### Membership

```go
func trailOrderValues() []string      // the closed set as data, four entries
func trailIsOrderValue(v string) bool // a switch over the same four
```

`trailIsGateValue` (`trailer_admissibility_test.go:906`) and `trailIsRunOutcome` (`trail_run_outcome_test.go:309`) are the shapes. A value a reader of the published record cannot look up is a verdict they cannot interpret.

### Joining the union map, and why not a local closure test

The four values join `TestTrailAdmissibilityConstantsAreClosed`'s map (`trailer_admissibility_test.go:1197-1246`) as a fifth section, following #1271's precedent recorded at `:1178-1181`.

The alternative — a self-contained `TestTrailOrderConstantsAreClosed` in `TestFinOutcomeConstantsAreClosed`'s shape (`finding_staging_gate_test.go:521`) — was rejected. That shape is right for the `fin*` family, which is a separate namespace. Here the realistic mistake is a *cross-space* collision: `order-void-pyry-did-not-exit` sits one word from a shipped `run-` value, and a per-space closure test is scoped to one space per call and cannot see it. AC4's agreement test covers one direction (this predicate rejecting adjacent values); only the union map covers the other (a future value in another space colliding with an `order-` one) and pairwise distinctness within the four.

---

## Concurrency model

None. `trailCertifyOrdering` is a pure function over three booleans: no goroutine, no channel, no context, no shared state, no shutdown sequence. Nothing in this ticket starts, stops or coordinates anything.

The concurrency that *matters* is upstream and already shipped: `holdProbeFIFO`'s goroutine parks in `open(2)` and is released only by the `t.Cleanup` it registers itself (`background_trigger_probe_test.go:663-717`). That is what makes `holdHeld=true` a fact the caller can know rather than assume, and it is the reason this predicate takes the fact rather than re-deriving it. Do not build a second FIFO-hold helper.

---

## Error handling

The predicate has no error path and returns no error. That is the design, not an omission:

- **It never fails a test.** No `*testing.T`, no `t.Fatalf`, no panic. An instrument failure observed mid-turn is a datum to publish, not a reason to abort the turn — the contract `trailScan`, `trailGate`, `trailAdmitAttribution`, `tdnClassifyReapLog`, `pinReadState` and `fifoLiveRead` all ship.
- **Every input is a reading.** Three booleans admit eight inputs and all eight have an arm, so there is no unrepresentable input and no out-of-contract value (§ Design).
- **Every failure of the rig is a named void, never a negative.** The three void values *are* the error handling: they report that nothing was measured and which premise is missing, so no verdict may be read from them.
- **The zero `trailOrderResult` is not a value in the space.** `Value == ""` and `trailIsOrderValue("")` is false, so an unfilled record cannot read as a filled one. The union map's zero-record walk asserts this.

---

## Testing strategy

Four tests, all offline, all fixture-driven, no live claude, no credentials, no daemon, no env gate, no `t.Skip`. All names start with `TestTrailOrder`.

A test-only fixture type keeps the eight rows readable and stops a mislabelled row from passing silently:

```go
type trailOrderPremises struct{ Sighted, Exited, Held bool }
```

**1. `TestTrailOrderAllEightPremiseCombinations` (AC2).** Table-driven over the eight rows above, one `t.Run` each, asserting `Value`. Row names should state the rule the row pins, not the booleans — e.g. `"order: a released hold outranks an unsighted trailer"` for row 6, in `finding_staging_gate_test.go:500-508`'s style. Two extra assertions per row, each one line: `Detail` is non-empty, and `!strings.Contains(Detail, reachTruncationMarker)` so a silent truncation reddens here rather than in an operator's artifact.

Assert the **marker**, not a length against `512`. `reachCapCommand` returns its input unchanged at exactly `reachMaxCommandBytes` and appends `reachTruncationMarker` only past it, so a `len(Detail) < 512` check both false-fails at the boundary and pins a literal that drifts when the constant moves — the exact defect `finding_key_name_bounds_test.go`'s named bounds exist to prevent. The marker test is the property itself.

**2. `TestTrailOrderEachPremiseHasItsOwnVoid` (AC3).** Three subtests. Each starts from the all-true fixture, asserts as a *premise* that it certifies, then flips exactly one boolean and asserts the result is (a) not `trailOrderCertified` and (b) that premise's own void. The premise assertion is what stops the test passing by classifying garbage (`trail_run_outcome_test.go:1594-1599`'s discipline). These three are also the anti-swap test: any pairwise swap of the three parameters at the definition makes at least two of them red.

**3. `TestTrailOrderValuesAgreeWithThePredicate` (AC4).** In `TestTrailRunOutcomeValuesAgreeWithThePredicate`'s shape (`trail_run_outcome_test.go:1655`):
- `len(trailOrderValues()) == 4`, with the failure message stating that the count is the ticket's own enumeration and a change to it is a change to what the predicate can answer.
- Forward: every listed value satisfies `trailIsOrderValue`.
- Reverse: `trailIsOrderValue` rejects `""` and **every** value of the adjacent spaces, enumerated in full rather than sampled. AC4 says "the gate and admit values" — all seven of each, not a representative pair — so the rejection slice spells:
  - all seven gate values (`trailGateUsable`, `trailGateNoTrailer`, `trailGateScanAborted`, `trailGateBudgetFired`, `trailGateAbsentOwesNone`, `trailGatePresentOwesNone`, `trailGateOutOfContract`);
  - all seven admit values (`trailAdmitProof`, `trailAdmitVoidBudgetFired`, `trailAdmitVoidInstrument`, `trailAdmitVoidNoLine`, `trailAdmitVoidGroupUnnamed`, `trailAdmitVoidNotOneReapLine`, `trailAdmitOutOfContract`);
  - the four liveness verdicts (`pinStateRunning`, `pinStateExitedNotReaped`, `pinStateNoSuchProcess`, `pinStateInstrumentFailed`);
  - `trailSeen`, `trailAbsent`, `trailAborted` — the shipped shape includes the scan's input states (`trail_run_outcome_test.go:1672-1673`) and they are one tab-completion from a result;
  - `trailOutcomeVoidPyryDidNotExit` at minimum from the run space, because it is the near-collision the `order-` prefix exists to make visible; `trailOutcomeRunningAtTrailer` and `trailOutcomeOutOfContract` alongside it.

  Spell them rather than looping a helper: no `trailGateValues()` / `trailAdmitValues()` ships, and adding one would grow the edit to `trailer_admissibility_test.go` for no gain here. The liveness verdicts are the direction that earns its place — they keep this space from silently absorbing a value that *does* claim something about a process.

**4. `TestTrailOrderResultCarriesNoCapturedBytes` (AC5).** In `trail_run_outcome_test.go:1571-1626`'s shape, structural half only: classify the all-true fixture, assert as a premise that it certified, `json.Marshal` the result, decode into `map[string]json.RawMessage`, and fail on any key containing `command`, `args`, `comm` or `argv`.

**The `trailNeedle` byte-sweep is genuinely absent, not deferred.** With the input pinned to three booleans, this predicate can see no captured byte at all, so a needle planted anywhere upstream has no route into this record. Do not plant one — a needle that cannot reach the output makes a sweep that goes green against a *correct* build and against a broken one alike. #1440's record is the first one a captured byte can enter, and it ships the sweep. Write this reasoning into the test's doc comment so a reviewer reads a decision rather than a gap.

### Verifying

These files carry `//go:build e2e_realclaude`, so **`make check` does not compile them** — a green `make check` says nothing about this ticket. Run, and read the count of tests that actually executed (an exit code cannot tell a skip from a pass):

```bash
go vet -tags e2e_realclaude ./internal/e2e/realclaude/
go test -race -tags e2e_realclaude -run '^TestTrailOrder' -v ./internal/e2e/realclaude/
go test -race -tags e2e_realclaude -run '^TestTrailAdmissibilityConstantsAreClosed$' -v ./internal/e2e/realclaude/
```

The third is not optional: the union-map edit is outside `-run '^TestTrailOrder'`, so nothing else proves the four new entries neither collide nor read as a zero record.

**Precedence sanity check (verification, not an AC).** Before finishing, confirm the eight-row table actually pins the precedence rather than merely covering it: reorder the three guards in the function body and re-run test 1. Rows 5–8 must redden. Use `go test -overlay` with an absolute-path JSON manifest so nothing is written into the worktree. If the reorder is green, the table is not doing AC2's job.

---

## Scope

Two files. No production (non-test) Go source is touched; no exported identifier is added.

**1. `internal/e2e/realclaude/trail_ordering_premises_test.go`** — new, ~470 lines: file header (~45), four values with docs (~60), the record (~20), the predicate (~70), the two membership helpers (~30), the fixture type and the all-true base (~25), four tests (~220).

**2. `internal/e2e/realclaude/trailer_admissibility_test.go`** — ~12 lines edited:

- Four entries plus a `// #1439's ordering-premise values.` section comment in the map at `:1197-1246`, appended after `#1366`'s block (`:1236`) and before `#1266`'s (`:1237`). **No count literal to update** — the test reads the map's size off the map (`:1172-1176`).
- `var zeroOrder trailOrderResult` beside the three existing zero records (`:1267-1269`), and one `if zeroOrder.Value == value { … }` inside the walk at `:1270-1280`.
- Two prose sentences go stale and must be corrected in the same commit — leaving them is the defect, not the fix:
  - `:1178-1181` — "three spaces now mean nearly the same words (an input state, the gate's view of it, and the run's view of it)". The `order-` space genuinely extends this: `order-void-pyry-did-not-exit` sits one word from `run-void-pyry-did-not-exit`. Update the count and the parenthetical.
  - `:1183-1186` — "…`trailClassifyRun` for the third, `trailReasonAgainstPath` … for the fourth." Add the fifth clause naming `trailCertifyOrdering`.

**Out of scope, deliberately:**

- Any change to `trail_run_outcome_test.go`. `trailRunReadings` has no hold field and gains none here — that record's shape belongs to **#1437**.
- Any consumer of `trailCertifyOrdering`. Nothing calls it in this ticket; **#1440** is the consumer and composes the aliveness claim on top of the certified ordering.
- `docs/knowledge/codebase/1439.md`. Written by the documentation phase from this spec and the merged diff, after the PR lands.

---

## Open questions

1. **Detail budget under the 512-byte cap.** The four Details are budgeted at ~350 bytes of prose plus a ~55-byte fixed clause, which fits comfortably. If an arm's argument genuinely will not fit — the hold arm has the most to say, since it carries the whole pid-reissue reasoning — shorten the prose rather than raise the cap or drop the clause. Test 1's `len(Detail) < 512` assertion is what makes the choice visible instead of silent.

2. **The fifth section's position in the union map.** Placed after #1366's block and before #1266's, so the map reads newest-`trail*`-space-last among the result spaces with the two input-state spaces still trailing. #1440 will very likely append a sixth section in the same region; if both branches are open at once this is a one-block merge conflict whose resolution is "keep both". Flagged here so #1440's architect run sees it rather than discovers it at integration.

3. **Whether the precedence should be `hold → sighting → exit` or chronological.** Settled as the former (§ Design) on the structural-outranks-situational rule. Recorded as an open question only because the eight-row table makes the choice cheap to revisit: changing it changes four `want` values and one doc paragraph, and nothing else in the tree depends on the order — #1440 consumes the certified/void distinction, not which void.

---

## Security review

**Verdict:** PASS (second pass; the first found one SHOULD FIX in category 6 and one under-satisfied criterion in § Testing, both revised inline before this verdict)

**Findings:**

- **[1. Trust boundaries]** No findings, and the boundary is the design's central claim. The predicate's *entire* input surface is three `bool` parameters, so no untrusted datum crosses into it — not a trailer value, not argv, not a process-table row, not a byte read from a subprocess's stdout. The ticket's own Technical Notes name the concrete near-miss: `finSighting` (`finding_run_gather_test.go:372-384`) carries `TerminalReason` / `StopReason` / `Subtype` / `KeyNames`, and passing it would move the boundary *inside* this function while AC5's key sweep stayed green, because those fields marshal as `terminal_reason` / `stop_reason` / `subtype` / `trailer_keys` — none `command`-shaped. § Design pins the parameter type as the enforcement and records why widening the denylist is the wrong repair. Downstream callers hold `trailOrderResult`, whose two fields are a constant from a four-value closed set and a Detail this function composed from its own format strings; nothing in it is reachable to any input's bytes.

  **Named assumption, and the one place soundness rests on a caller rather than on structure:** the predicate cannot verify any of its three premises — it certifies what it is told. The dangerous direction is a false `order-certified`, which needs all three booleans true, and `holdHeld` is the one a caller could get wrong without noticing, since no shipped gather records it (`finding_stage_held_group_test.go:175-188` carries no such field). This is the ticket's stated design and is not a defect: `holdProbeFIFO` hands the caller a receive-only channel and keeps the write end, releasing it only in the `t.Cleanup` it registers itself (`background_trigger_probe_test.go:652-655`), so a wait taken in the subtest body is held for its whole duration *by construction* and the caller asserts something it can know. It is recorded here rather than left implicit because it is the assumption a future caller outside that staging shape would silently break, and because the containment is real: a wrong certification here still claims only that three instants were ordered — never that any process was alive — so it cannot on its own produce a false finding. **#1440 is where a wrong `holdHeld` could reach a claim about a process, and its own security pass owns that step.**

- **[2. Tokens, secrets, credentials]** No findings, and this is the category the family's byte discipline exists for. The realclaude suite runs with `CLAUDE_CODE_OAUTH_TOKEN` / `ANTHROPIC_API_KEY` in the environment, and the standing hazard is a probe artifact carrying one into a public issue (`background_reach_probe_test.go:119-124`; `process_pin_liveness_test.go:232` refuses a `command` column at the source for exactly this). Here the exposure is structurally zero: the function reads no environment, no process table and no file, and composes its Detail from compile-time format strings plus three booleans. AC5's marshal-and-walk test pins that a *future* field cannot quietly add a command-shaped key. No token is generated, stored, rotated or revoked by this ticket.

- **[3. File operations]** Not applicable by design, and the design decision is explicit rather than incidental: `trailCertifyOrdering` performs no filesystem access at all — the purity contract at AC1 forbids it, and the tests assert behaviour rather than touching disk. No path is constructed, so there is no traversal, TOCTOU, permission-mode, symlink or atomic-write question to answer. The FIFO whose hold this predicate takes a *fact about* is created by the shipped `holdProbeFIFO` at mode `0600` under `t.TempDir()` (`background_trigger_probe_test.go:665`); this ticket neither changes nor re-implements it, and § Concurrency forbids building a second one.

- **[4. Subprocess / external command execution]** Not applicable by design. No `exec.Command`, no `sh -c`, no signal, no environment manipulation anywhere in this ticket — enforced by the same purity contract. Worth stating positively because the *neighbouring* code is full of it: `finStageHeldGroup` runs `sh -c` with a compile-time-constant script and both operands as positional parameters precisely so no metacharacter is interpreted (`finding_stage_held_group_test.go:152-160`), and scrubs the environment to `[]string{}` so the subject cannot inherit a token (`:221-225`). This ticket consumes a boolean *about* that staging and must not grow a second copy of it; § Concurrency and the ticket's Technical Notes both say so.

- **[5. Cryptographic primitives]** Not applicable by design. No randomness of any kind — the eight fixtures are enumerated constants, not sampled, which is also what makes the suite deterministic. No hashing, no key material, no comparison against a secret, so no `crypto/subtle` question arises. The one comparison this ticket performs is `trailIsOrderValue`'s switch over four public constants; nothing there is attacker-controlled or secret.

- **[6. Network & I/O]** Not applicable by design. No socket, no HTTP server, no reader, no deadline and no connection to cap — the function's whole input is 3 bits. The one bounded-resource question that *does* reach this ticket is output size, and it is answered. **SHOULD FIX, fixed inline during this pass:** the first draft specified a per-row `len(Detail) < 512` assertion, which both false-fails at exactly `reachMaxCommandBytes` and pins a literal that drifts when the constant moves. § Testing now specifies `!strings.Contains(Detail, reachTruncationMarker)` — the property itself rather than a proxy for it — so a Detail whose argument would be silently cut off reddens in test 1 instead of reaching an operator's artifact truncated.

- **[7. Error messages, logs, telemetry]** No findings; one design decision worth recording. This family's Details are the artifact an operator pastes into a public issue, so "what goes in an error message" is the security question, and § Design pins the answer as a content rule rather than leaving it to judgement: a Detail may name the deciding premise, this space's value names and the precedence argument; it may never claim anything about a command's liveness or about the turn having been declared finished. The fixed `premises: trailer-sighted=%t pyry-exited=%t hold-held=%t` clause is three booleans and leaks nothing. No `slog` call, no metric, no telemetry is added — consistent with the family, which publishes records rather than logging.

- **[8. Concurrency]** No findings — the predicate is pure and single-threaded, spawns no goroutine, takes no lock and shares no state, so there is no lock ordering, no TOCTOU, no shutdown path and no leak to reason about. The one *genuine* concurrency hazard in the neighbourhood is upstream and already handled: `holdProbeFIFO`'s parked `open(2)` goroutine is released only by the `t.Cleanup` it registers itself, which by construction runs after the subtest body (`background_trigger_probe_test.go:652-655`, `:689-713`). That is precisely what makes `holdHeld=true` a fact a caller in the subtest body can assert rather than assume, and it is why § Concurrency forbids a second hold helper. Tests are `t.Run` subtests over a pure function and need no `t.Parallel()` coordination.

- **[9. Threat model alignment]** No findings. This is a test-only, offline instrument under `//go:build e2e_realclaude`; it ships in no binary, opens no port, and is unreachable from the relay and control-plane surfaces `docs/protocol-mobile.md` § Security model governs. The one threat class it *does* sit inside is the probe family's own — an artifact destined for a public issue inheriting the operator-review-before-paste obligation by copying a captured string (`background_reach_probe_test.go:119-124`) — and it is addressed in category 1, 2 and 7 above rather than deferred. **Named as out of scope:** whether a *composed* record built from this result plus a pinned pid can carry a captured byte. It can, and #1440 is the ticket that owns it: § Testing records that the `trailNeedle` sweep is genuinely absent here because the three-boolean input gives a planted needle no route in, and that #1440's record is the first one a captured byte can enter and the one that ships the sweep.

**Reviewer:** architect (self-review per `architect/security-review.md`)
**Date:** 2026-08-10
