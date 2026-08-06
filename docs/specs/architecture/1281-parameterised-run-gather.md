# #1281 — Probe instrument: parameterise the run-outcome gather on stderr and a pinned group set (offline)

**Size:** S (confirmed — see § Scope check)
**New files:** `internal/e2e/realclaude/finding_run_gather_test.go` (one, new)
**Files modified:** none
**Identifier prefix:** `finGather*` (census re-run at `6272256`: `finGather` = 0, `fin[A-Z]` = 52, control `trail[A-Z]` = 880 — all three match the ticket)

---

## Files to read first

Read in this order. Everything is in the same package under the same `e2e_realclaude` build tag, so every symbol below is directly callable.

| Path | What to extract |
|---|---|
| `internal/e2e/realclaude/trail_run_rig_test.go:121-195` | `trailRigGather` — **the template**. Its doc comment already maps which contract check shapes which leg. Copy the shape; change the two hardcodings. |
| `internal/e2e/realclaude/trail_run_rig_test.go:26-57` | The header sections this file's header mirrors: why the finding is unreachable there, and the **failure-message content rule**. |
| `internal/e2e/realclaude/finding_attribution_fanout_test.go:181-301` | `finAttributeFanOut` — signature, purity, and why `pgids` is `[]int`. This is the attribution leg. |
| `internal/e2e/realclaude/finding_attribution_fanout_test.go:93-125` | `finAttributeRecord` — `Conditions` (:98) is the field a consumer branches on; `Selected` (:106-115) states the caller obligation this ticket discharges. |
| `internal/e2e/realclaude/finding_attribution_fanout_test.go:61-75, :461-468` | `finAttributeNoGroups` and `finAttributeHasCondition` — AC4's two symbols. |
| `internal/e2e/realclaude/finding_attribution_fanout_test.go:697-767` | `TestFinAttributeRecordCarriesNoCapturedBytes` — the shape AC5 extends, **and the flat-only key scan AC5 must not repeat** (it walks top-level keys only). |
| `internal/e2e/realclaude/trail_run_outcome_test.go:171-219` | `trailRunReadings` field by field. Note: **no json tags** — the marshalled keys are Go field names, which drives AC5's key-scan design. |
| `internal/e2e/realclaude/trail_run_outcome_test.go:344-472` | `trailClassifyRun`'s contract block. C2 `:377`, C3 `:390`, C4 `:402`, C8 `:451`, C9 `:463` are AC3's five obligations. |
| `internal/e2e/realclaude/trail_run_outcome_test.go:513-593` | Steps 2–8. Step 2 (`:523`) returning before Steps 4/5/7/8 is AC2's ordering claim. |
| `internal/e2e/realclaude/trailer_admissibility_test.go:242-322` | `trailGate` — the three non-certifying arms, and `:302-321` where `Reason` is spliced `%q` into `Detail` on both certifying arms. |
| `internal/e2e/realclaude/trailer_admissibility_test.go:356-485` | `trailAdmitAttribution` — the empty-`certified` contract check (`:413`), the budget arm (`:425-435`, quotes `certified`), the proof arm (`:466-476`, quotes `certified`). |
| `internal/e2e/realclaude/trailer_admissibility_test.go:199-208, :530-536` | `trailDetail` (reuse, do not twin) and `trailReapLine(count, pgids)` (the synthetic stderr renderer). |
| `internal/e2e/realclaude/result_trailer_observation_test.go:96-137` | `trailScanResult.Line` (capped, operator-review) / `.Trailer` (**`resultTrailer` has no `result` member**) and `trailObservation`'s embedding. |
| `internal/e2e/realclaude/result_trailer_observation_test.go:164-274` | `trailScan` and `trailWaitForTrailer`. Note `trailAborted` returns **immediately** (`:264-266`) — that is what makes AC3's C4 row instant. |
| `internal/e2e/realclaude/result_trailer_observation_test.go:276-317` | `trailFixtureTrailer`, `trailNeedle`, `trailPaddedTrailer`, `trailOverlongPad`. |
| `internal/e2e/realclaude/process_pin_liveness_test.go:120-197, :199-275` | `pinScan`'s two counts, `pinScanArgv`'s zero-on-error contract (C9's subject), `pinStateColumns`' never-add-`command` prohibition, `pinStateOutcome`. |
| `internal/e2e/realclaude/teardown_liveness_test.go:112-219` | `tdnReapOutcome` (its `Line` is pyry's stderr) and `tdnClassifyReapLog`'s `heldPGID <= 1` guard (`:147`). |
| `internal/e2e/realclaude/background_trigger_probe_test.go:131, :722-741` | `probePollInterval`, `probeSyncBuffer` (mutex-guarded, `Bytes()` returns a copy). |
| `internal/e2e/realclaude/background_reach_probe_test.go:117-123, :873-882, :945-950` | `reachMaxCommandBytes = 512`, `reachScanArgv`'s constant `ps` argv, `reachCapCommand`. |
| `docs/specs/architecture/1268-trail-run-rig.md` | The rig spec this one parameterises. |
| `docs/specs/architecture/1280-attribution-fanout.md` | Why `pgids` is `[]int` and why the empty set is a condition, not a value. |

---

## Context

`trailRigGather` (`trail_run_rig_test.go:150`) is the correct composition wired to the wrong two inputs. It passes a **`nil` literal** as the reap-log stderr and keys the attribution on **the test process's own** process group. `tdnClassifyReapLog(nil, …)` can only reach `tdnReapNoLine`, which `trailAdmitAttribution` answers with `trailAdmitVoidNoLine`, so `trailAdmitProof` — and with it `trailOutcomeRunningAtTrailer`, the only outcome that is a finding — is **structurally unreachable**. A probe built on that gather reports a clean negative on every run, forever, with no symptom.

This ticket builds the parameterised gather and proves, offline from synthetic stdout and synthetic stderr, that **both** the finding and a genuine negative come out of its own composition. No subject process, no live claude, no credentials, no turn.

The blocker has shipped: `finAttributeFanOut(stderr []byte, pgids []int, certified string) finAttributeRecord` (#1280, merged at `6272256`) owns the many-groups → one-`trailAdmitResult` reduction. This spec's gather is the caller whose obligation `Selected`'s doc comment (`:106-114`) states: *branch on `Conditions`, never copy a zero `Selected` into `trailRunReadings.Admit`.*

---

## Design

### The one new function

```go
// finGatherReadings assembles a complete trailRunReadings from the four inputs a
// live probe holds, using only shipped producers.
func finGatherReadings(stdout *probeSyncBuffer, needles []string, stderr []byte,
	pinned []int) (trailRunReadings, finAttributeRecord)
```

Four parameters, three legs, two returns. Behaviour per leg:

1. **Trailer leg.** `obs := trailWaitForTrailer(stdout, finGatherTrailerWait)`; assign `readings.BoundFrom = obs.BoundFrom` and `readings.Gate = trailGate(obs.trailScanResult)`. The observation's embedded scan result is **reused, not re-scanned** — that is the composition a live probe performs. `obs` is a function-local intermediate and is **never returned** (see § The return type is an AC, below). `Staleness` is not read.

2. **Attribution leg.** Guarded on `readings.Gate.Reason != ""` — the exact condition C3 and C4 split on, and the condition `trailAdmitAttribution`'s own contract block (`:413`) rejects the negation of. Inside the guard: `record = finAttributeFanOut(stderr, pinned, readings.Gate.Reason)`, then assign `readings.Admit = record.Selected` **only when** `!finAttributeHasCondition(record, finAttributeNoGroups)`. Outside the guard the fan-out is not called at all and `record` stays the zero `finAttributeRecord`.

3. **Argv leg.** One `pinScanArgv(needles, nil)` call. `ArgvScanErrored` from its `err`, `MatchCount` and `RowsScanned` from the same returned `pinScan`. One `pinReadState(match.PID)` appended to `Liveness` per matched pid.

Then the two staged values, exactly as #1268 staged them and for the same reasons: `PyryExited = true` (no pyry runs here; left zero, Step 3 would void every row and the two rows would agree) and `ClaudeState = ""` (C7's explicit "not read"). **See § Open questions — this is the one thing #1282 must not inherit silently.**

### The two changed inputs, and why they are two

The consumer stages a command held un-finishable during a turn, then waits for pyry's trailer. `ptyrunner.Run`'s pinned teardown order (`runner.go:479-485`, reap defer at `:398`) runs the reap **immediately after** the trailer is written, completing in the time of one `ps` exec. So a scan taken after the trailer is observed matches nothing on a healthy run — that is the predicted reading, not a failure. The pgid therefore cannot come from a post-trailer scan; it must be pinned **during** the turn while the hold guarantees the command is alive. Two scans, two roles, two parameters:

- `pinned []int` — the during-turn pin. The join key into pyry's reap log.
- `needles []string` → the post-trailer `pinScanArgv` — the content join plus corroboration.

`pinned` is `[]int` and **never** `[]reachProc` or `[]pinMatch`. `finAttributeFanOut`'s doc (`finding_attribution_fanout_test.go:195-202`) states why: `reachProc.Command` is verbatim argv off the ambient process table, and *"a `[]reachProc` signature that recorded only `.PGID` would pass every test in this file while reopening the channel."* The signature is the enforcement; a check inside the function is not. **A caller holding `pinScan.Matches` converts at its own call site, taking `.PGID` and nothing else** — that conversion is #1282's, not this ticket's.

### The return type is an AC, not a convenience

AC5 forbids the gather from returning *"nothing from which `trailScanResult.Trailer` is reachable."* `trailObservation` **embeds** `trailScanResult` (`result_trailer_observation_test.go:125-126`), so returning the observation promotes `.Trailer` and `.Line` straight back into the caller's reach. This is where this gather deliberately diverges from `trailRigGather`, which returns it because #1268's AC4 needed `Staleness`. Nothing here does: `BoundFrom` is on the readings and `Staleness` is not a classifier input.

The second return is `finAttributeRecord` — #1280's own record, returned **whole**. It is proven to carry no captured bytes (`TestFinAttributeRecordCarriesNoCapturedBytes`) and it is where `Conditions` lives, which is what AC4 requires the gather to surface. Returning it means this ticket defines **no new record type at all**.

### Data flow

```
stdout ─▶ trailWaitForTrailer ─▶ obs ─▶ trailGate ────────────▶ readings.Gate
                                  └────────────────────────────▶ readings.BoundFrom
                                        (obs dies here — never returned)

readings.Gate.Reason != "" ?
   yes ─▶ finAttributeFanOut(stderr, pinned, Reason) ─▶ record
             ├─ Conditions holds finAttributeNoGroups ─▶ Admit LEFT ZERO
             └─ otherwise ────────────────────────────▶ readings.Admit = record.Selected
   no  ─▶ fan-out not called; record stays zero; Admit LEFT ZERO   (C4)

needles ─▶ pinScanArgv ─▶ scan ─┬─▶ readings.ArgvScanErrored (from err)
                                ├─▶ readings.MatchCount / RowsScanned
                                └─▶ pinReadState per match ─▶ readings.Liveness
```

### The empty pinned set is a condition, never a reading

Both alternatives publish a falsehood, and `TestFinAttributeEmptySetAlternativesArePublishedFalsehoods` (`:664`) already prices both one layer down: a zero `Admit` under a certifying gate reaches `trailOutcomeOutOfContract` via C3 (a caller bug dressed as a reading), and `trailAdmitOutOfContract` has no arm in the classifier and falls to Step 8's `trailOutcomeNoRowMatched` (a clean negative about a run whose attribution never happened). So the gather **passes the condition out** and leaves `Admit` zero, and the caller branches. The outcome tier that names such runs is #1278's, not this ticket's.

Note the resulting shape is honest in both directions: on a **non-certifying** gate the record is also zero, `finAttributeHasCondition` is false, and a caller that proceeds to classify correctly lands on one of Step 1's voids. Only the certifying-gate-plus-empty-set pair is unclassifiable, and only that pair raises the condition.

### The certified terminal reason crosses verbatim — state the exclusion, do not fight it

`terminal_reason` reaches the returned readings **twice**, by design:

- `trailGate` fills `Reason` from `res.Trailer.TerminalReason` and splices it `%q` into `Detail` on both certifying arms (`:306`, `:317`); `readings.Gate` takes that result whole.
- The gather hands the same `Reason` to the fan-out as `certified`, and `trailAdmitAttribution` splices it `%q` into `Detail` on the budget arm (`:428`) and the proof arm (`:470`); `readings.Admit` takes that result whole.

So following #1271's "plant the needle in every string-bearing input" would put it in `terminal_reason` and go **red against two shipped predicates**, with the only fix being to stop quoting the reason — out of scope, and it would delete the field the operator reads to interpret the gate. #1280 met this one layer down and resolved it by planting only in the stderr (`:730-731`). This spec states the exclusion explicitly (AC5 requires the *test* to state it): **the needle's home is the trailer's `result` field and the reap-log stderr; it may not enter `terminal_reason`.**

### `finGatherNeedleTrailer` — why a new fixture rather than `trailPaddedTrailer`

`trailPaddedTrailer` renders `terminal_reason: "max_turns"`, which makes the gate **budget-fired**. On that path `trailAdmitAttribution` returns its structural void *before it reads the verdict at all* (`:425-435`), so the needle-bearing anchored line's classification is discarded and AC5's stderr half loses its premise: the test could not assert `trailAdmitProof`, and would go green even against a leak on the proof arm. `trailPaddedTrailer` is therefore the wrong fixture here.

This file renders its own: an **ordinary** trailer in `emitter.go:456-468`'s pinned wire order — `subtype "success"`, `is_error false`, `terminal_reason "completed"` — whose `result` field carries `trailNeedle`. Two properties are load-bearing and must be stated in its doc comment:

- **The needle sits INSIDE the 512-byte cap**, so `trailScanResult.Line` genuinely carries it. Placing it past the cap (as `trailPaddedTrailer(2000)` does) would make the stdout half of AC5 vacuous — the only leak it could then catch is a record that stored the line *in full*, and `trailRunReadings` has no field that could. The test asserts this premise (see AC5's scenarios) so a later edit cannot silently push the needle past the cap.
- **`terminal_reason` stays clean.** That is the exclusion above, made structural by the fixture rather than left to the plant site.

### Constants

| Name | Value | Why named |
|---|---|---|
| `finGatherTrailerWait` | `10 * time.Second` | The trailer poll's timeout, matching `trailRigTrailerWait`. **No row ever waits it out**: the certifying rows pre-seed the buffer so the first poll hits, and the non-certifying row uses the aborted-scan path, which `trailWaitForTrailer` returns from immediately (`:264-266`). |
| `finGatherNamedPGID` | `7788` | The group the synthetic reap line names. Matches the fan-out file's fixture value so the two chains read as one. |
| `finGatherUnnamedPGID` | `4242` | A pinned group the reap line does **not** name — the negative row's one varied dimension. |
| `finGatherNeedleName` | `"fin-gather-subject"` | Basename joined onto `t.TempDir()` for the argv needle. Unique per run; no process's command line carries it, including the test binary's own (whose argv is `-test.run=…` and nothing more). Nothing is ever created at that path — it is a match pattern, not a file. |

---

## Testing strategy

One new file, build-tagged `e2e_realclaude`, offline, **no `t.Skip`** (a skip exits 0 and reads as a pass — the #1168 false green).

```
go test -race -tags e2e_realclaude -run '^TestFinGather' -v ./internal/e2e/realclaude/
```

### `TestFinGatherComposesTheFindingAndTheNegative` — AC2 + AC3

A three-row table (`finGatherCase` / `finGatherCases()` — a **function**, not a package var, for `trailRunWellFormed`'s stated reason at `:609-610`: the rows carry slices). Each row seeds a fresh `probeSyncBuffer`, runs the gather, passes the assert helper below, then classifies with `trailClassifyRun` and compares. **No row hand-builds a `trailRunReadings`.**

| Row | stdout seed | stderr | `pinned` | Gate | Admit | Outcome |
|---|---|---|---|---|---|---|
| the reap log names a pinned group | `trailFixtureTrailer` | `trailReapLine(1, "[7788]")` | `{7788}` | usable / `"completed"` | `trailAdmitProof` | `trailOutcomeRunningAtTrailer` |
| the reap log names a different group | `trailFixtureTrailer` | `trailReapLine(1, "[7788]")` | `{4242}` | usable / `"completed"` | `trailAdmitVoidGroupUnnamed` | `trailOutcomeNoRowMatched` |
| a non-certifying gate leaves the attribution unclassified | `trailPaddedTrailer(trailOverlongPad)` | `trailReapLine(1, "[7788]")` | `{7788}` | scan-aborted / `""` | zero | `trailOutcomeVoidTrailerScanAborted` |

Scenarios the rows assert, beyond the outcome value:

- **The ordering proof (AC2), asserted rather than described.** Both of the first two rows run at `MatchCount == 0` — asserted explicitly on each — and still separate. That is Step 2 (`:523`) outranking Step 4, Step 5, Step 7 and Step 8, demonstrated by the gather's own composition. The failure message on the finding row must say why the match count is irrelevant: the reap already killed the group before the scan ran.
- **The negative is genuine, never a `run-void-*`.** The second row asserts equality with `trailOutcomeNoRowMatched`, and its failure message names why each neighbouring void would be the wrong answer.
- **`RowsScanned > 0` on every row.** Asserted first on the negative row: at zero the reading is `trailOutcomeVoidNoRowsParsed`, a nothing-was-measured masquerading as the negative.
- The two rows differ in **exactly one dimension** — the pinned pgid. Same stdout, same stderr, same needles.

### `finGatherAssertContract` — AC3's five obligations, one row each

A helper run by **every** row of the table above, so each obligation is discharged on every composition rather than once. Each check's failure message names the contract check it discharges.

- **C2 (`:377`) — the gate is fed a real scanned trailer.** Assert `readings.Gate == trailGate(trailScan(seedBytes))`, byte for byte against the shipped producers over the same bytes. `trailGateResult` is comparable (three strings), so `==` suffices. A hand-built `trailGateResult{Value: trailGateUsable}` is exactly the fixture C2 exists to reject, and this equality is what rules it out.
- **C3 (`:390`) — the predicate is called exactly when the gate certified.** On a certifying row, assert `trailIsAdmitValue(readings.Admit.Value)` **and** `readings.Admit == finAttributeFanOut(stderr, pinned, readings.Gate.Reason).Selected`. The fan-out is pure, so the recomputation is deterministic; a gather that passed a different `certified`, or that re-derived the selection rule, goes red here.
- **C4 (`:402`) — `Admit` is left zero when the gate certified nothing.** On the non-certifying row, assert `readings.Admit == trailAdmitResult{}`. This is the zero-value comparison AC1 explicitly permits (`trail_run_outcome_test.go:664`, `finding_attribution_fanout_test.go:667`) — a comparison against an existing consumer's idiom, not a hand-built reading the gather produced.
- **C8 (`:451`) — the counts are one scan's own.** Assert `MatchCount >= 0`, `RowsScanned >= 0`, `MatchCount <= RowsScanned`. `{MatchCount: 1}` with `RowsScanned` unfilled is the hand-typed fixture C8 exists to catch.
- **C9 (`:463`) — the errored flag comes from the same call.** Assert the consistency `pinScanArgv` guarantees by construction: errored implies both counts are zero; not-errored implies `RowsScanned > 0`. State in the comment that the errored arm has **no live repro** in an offline rig — the package's own idiom for an unproducible contract arm (`trailer_admissibility_test.go:293-299`) — so what is pinned is the invariant, not the arm.
- Free corroboration on every row: `trailIsRunOutcome(outcome.Value)` and `outcome.Value != trailOutcomeOutOfContract`. A composition that assembled a record its own producers cannot emit shows up here first.

### `TestFinGatherEmptyPinnedSetIsNotAReading` — AC4

Two subtests, both with a certifying stdout (`trailFixtureTrailer`) and a real reap line, differing only in the pinned set:

- **`nil` pinned set** — the caller that pinned nothing, or pinned before the subject execed.
- **`{0, 1}`** — every group unreportable (`reap.go:52` skips `pgid <= 1`). Same condition by a different route; included because it is what separates a gather that reads `Conditions` from one that special-cases `len(pinned) == 0`.

Each asserts:

- `finAttributeHasCondition(record, finAttributeNoGroups)` is true — **read through the shipped helper**, per AC4.
- `record.Selected == trailAdmitResult{}` and `len(record.Entries) == 0` — the mutual exclusion `Selected`'s doc states.
- `readings.Admit == trailAdmitResult{}` — the gather did not copy the zero `Selected` in.
- **The pin that no `trailRunOutcome` is produced from them**, made checkable by pricing the alternative exactly as `TestFinAttributeEmptySetAlternativesArePublishedFalsehoods` does one layer down: classifying these readings anyway reaches `trailOutcomeOutOfContract`, and its `Detail` carries C3's own sentence (`"no run condition under which a certifying gate arrives"`). Assert the value **and** the sentence — the arm reached matters as much as the value, because three other contract checks also answer `trailOutcomeOutOfContract`. That is the demonstration that the readings are unclassifiable and that the caller's obligation is to branch, not to publish.
- The second subtest additionally asserts `finAttributeGroupUnreportable` is present and `record.Unreportable` is `{0, 1}` — the groups were surfaced, not classified.

### `TestFinGatherReturnsNoCapturedBytes` — AC5

One row, both plants, following `TestFinAttributeRecordCarriesNoCapturedBytes`'s shape and **fixing its flat-only key scan**.

Inputs: stdout seeded from `finGatherNeedleTrailer()` (needle in `result`, `terminal_reason "completed"`); stderr `trailReapLine(1, "[7788] "+trailNeedle)` — the needle **on the anchored line, after the pgids list**, which is #1280's plant position and the only non-vacuous one (`tdnParsePGIDs` stops at the first `]`; a needle on a non-anchored line is skipped before any field is filled, and the test would go green over a record that captured everything); `pinned = {7788}`.

Premises, asserted **first**, each of which turns a vacuous plant into a `Fatalf`:

- `readings.Gate.Value == trailGateUsable` and `readings.Gate.Reason == "completed"` — the trailer leg read the needle-bearing line as an ordinary trailer, and the needle is **not** in `terminal_reason`. This assertion is where the exclusion AC5 demands is stated in code rather than in prose alone.
- `strings.Contains(trailScan(seedBytes).Line, trailNeedle)` — the needle survives the 512-byte cap in the retained copy, so the channel the gather held genuinely carried it. Guards a later fixture edit from silently pushing the plant past the cap.
- `readings.Admit.Value == trailAdmitProof` — the needle-bearing anchored line was recognised, parsed and found to name the pinned group, and `certified` is not the budget reason.

Checks, over **both** returns (`readings` and `record` — AC5 binds everything the gather returns):

- Marshal each; assert `!bytes.Contains(encoded, trailNeedle)`.
- **Recursive** forbidden-key walk (`finGatherForbiddenKeys`) over each marshalled value: descend into every object and every array element, not only the top-level keys. Forbidden substrings `command`, `args`, `comm`, `argv`, `line`, `stderr`, matched against the **lowercased** key. Two design points the developer must not skip:
  - **Lowercasing is required here and was not in #1280.** `trailRunReadings` carries **no json tags**, so its marshalled keys are Go field names (`Gate`, `Admit`, `Liveness`, …). A case-sensitive scan over lowercase needles would miss a future `Command string` field entirely.
  - **One named exemption: the exact key `argvscanerrored`.** `ArgvScanErrored` contains `argv` and is a bool discriminator that records **that** the scan failed and never what it said (`trail_run_outcome_test.go:186-193`). The exemption is a single named key with that citation, not a prefix rule — a future `ArgvMatches` must still trip.
  - The recursion is the point: `Gate`, `Admit`, each `Liveness` entry, and the record's `Entries[]` are all nested, and a flat scan examines none of their keys. #1280's transplanted flat scan is the precedent this closes.

---

## Concurrency model

None introduced. The gather spawns no goroutine, takes no lock and starts no process of its own; `pinScanArgv` and `pinReadState` are synchronous `ps` execs through shipped helpers. The only shared state is `probeSyncBuffer`, whose `Write` and `Bytes` are mutex-guarded and whose `Bytes()` returns a copy, so `trailScan` always runs over a private snapshot. This file, unlike `trail_run_rig_test.go`, runs its gather on the test goroutine on every row — nothing here needs the concurrent-append shape #1268's AC4 used. `go test -race` must be clean without any new synchronisation.

## Error handling

Every producer this gather calls has the package's shared contract: takes no `*testing.T`, returns no error the caller must branch on, and names every way of not producing a reading. `pinScanArgv` is the one exception and its error is taken as a **discriminator** (`ArgvScanErrored`) and never as its text — the same reason `pinScan.Matches` never enters the readings.

**Failure-message content rule** — state it in the file header, mirroring `trail_run_rig_test.go:49-57`. A `t.Fatalf` here MAY name counts, pgids, verdicts, outcome values, gate and admit values, `BoundFrom`, and `finAttribute*` condition names. It MAY NEVER name `pinScan.Matches`, the function-local observation's `Line`, or a `trailScanResult`'s trailer. Printing `readings` or `record` wholesale is safe **because** `TestFinGatherReturnsNoCapturedBytes` proves it — that test is what licenses the rest of the file's messages, so it is not merely one AC among five.

---

## Scope check

| Red line | This ticket |
|---|---|
| > 3 new files | 1 (`finding_run_gather_test.go`) |
| > ~600 LOC total written | ~510 projected (header ~55, consts ~25, fixture ~14, gather ~70, table + runner ~100, assert helper ~75, AC4 test ~55, AC5 test ~70, key walk ~35, imports ~10) |
| > 5 new exported types | 0 — every symbol is file-local and lowercase; the only new named type is `finGatherCase` |
| > 10 consumer call sites | **0** — purely additive, one new file. The ticket forbids editing `trail_run_rig_test.go` and `finding_attribution_fanout_test.go`, and nothing else needs to change |
| > 5 acceptance criteria | exactly 5 |
| > ~10 reject branches in a state machine | **0** — no new outcome value, no new enum, no new reject arm. The gather has two conditionals |

Production source files created or modified (excluding `*_test.go`, `*.md`, and this spec): **0** — the ≥ 5 self-check does not fire.

The historical binding constraint for this package is edit fan-out, and it is absent: this is one new file with no consumer cascade. Projected LOC sits below the package's own `size:s` record (`finding_attribution_fanout_test.go` at 768, `trail_run_rig_test.go` at 595).

---

## Open questions

1. **`PyryExited` is still staged `true`, and #1282 must not inherit that silently.** This ticket parameterises the two inputs the ticket body names, and keeps #1268's two staged values with their original justification. But the same argument that condemns the `nil` stderr applies one field over: fed a live pyry, a gather that hardcodes `PyryExited = true` would never let Step 3's `trailOutcomeVoidPyryDidNotExit` fire, and a run where pyry hung would report a scan-side answer — or the finding — instead of the staging void. That is a **false-positive** direction, worse than the false negative this ticket closes. The gather's doc comment must say so and name #1282 as the owner; #1282 should promote `PyryExited` (and `ClaudeState`) to parameters or fill them from real producers before it feeds this gather a live pyry. Deliberately not done here: the ticket's out-of-scope section reserves all live-side wiring, and widening the signature now would change what #1282 inherits without a test in this file able to exercise either value.

2. **`tdnClassifyReapLog` walks the whole stderr once per distinct reportable group**, so the fan-out is `O(len(stderr) × groups)` (documented at `finding_attribution_fanout_test.go:204-208`, with no cap on the group count by design). Offline the stderr is one line and the point is moot. A live caller passing an unbounded captured stderr with a wide pinned set should bound one of the two; that decision belongs to the ticket that produces the live stderr, not here.

3. **`finGatherNeedleTrailer` duplicates the wire order** of `trailFixtureTrailer` / `trailPaddedTrailer`. Deriving it by substitution from `trailFixtureTrailer` would single-source the order but couple the fixture to another constant's internal spelling. Written out explicitly here, matching `trailPaddedTrailer`'s precedent; if a fourth wire-order fixture appears, extracting a shared renderer becomes worth its own ticket.

---

## Security review

**Verdict:** PASS

**Findings:**

- **[Trust boundaries]** No MUST FIX. Three untrusted sources cross into this gather: the model's stdout (`trailScanResult.Line` is verbatim model output, marked operator-review-before-paste), pyry's stderr (`tdnReapOutcome.Line`), and the ambient process table (`reachProc.Command` is verbatim argv). All three boundaries are explicit and each is closed by a *type*, not a check: the gather returns neither the observation nor the `pinScan` (only `BoundFrom`, two counts, a bool, and the two trap-free admissibility records); `finAttributeFanOut`'s `pgids []int` signature refuses `[]reachProc` outright; `pinStateColumns` is `pid=,ppid=,stat=` and carries a never-add-`command` prohibition with its own enforcing test. `TestFinGatherReturnsNoCapturedBytes` is the enforcement that the boundary held, and its recursive key walk closes the nested-field gap #1280's flat scan left open.
- **[Trust boundaries]** SHOULD FIX — the `[]reachProc` → `[]int` conversion happens at the **caller**, and this ticket ships no caller. The gather's doc comment must state that a caller holding `pinScan.Matches` takes `.PGID` and carries nothing else; code-review on #1282 must check the conversion site, because that is where the channel would reopen.
- **[Tokens, secrets, credentials]** No findings. The concrete threat is `CLAUDE_CODE_OAUTH_TOKEN` / `ANTHROPIC_API_KEY` reaching an artifact destined for a public issue. This ticket reads no environment: both `ps` calls use explicit column lists, neither uses `-E` / `-Eww` / BSD `eww`, and `reachScanArgv`'s widening flag is `-ww`, which does not touch the environment. No token is generated, stored, compared or logged anywhere in the design.
- **[Tokens, secrets, credentials]** Accepted, stated rather than left implicit: AC5's failure message prints the marshalled encoding when the check fires (#1280's precedent at `:748`). That is safe **here specifically** because this file is driven entirely from synthetic fixtures — the only thing that can leak into CI logs is the synthetic `trailNeedle` constant. The same message shape in a live probe would print real captured bytes, so it must not be copied forward without re-deciding. Named for #1282.
- **[File operations]** No findings — no file is created, opened, stat-ed or removed. The `t.TempDir()`-derived needle is a *match pattern* compared against `ps` output in Go and is never used as a path. No traversal surface, no TOCTOU, no mode question, no symlink handling, no atomic-write question. (Unlike #1268's rig, this ticket stages no FIFO.)
- **[Subprocess / external command execution]** No findings. Two `ps` execs, both through shipped helpers, both with a **compile-time-constant argv** (`ps -axww -o pid=,ppid=,pgid=,command=` and the `pinStateColumns` read) under a `context.WithTimeout`. Verified: `needles` never reaches `exec` — `reachScanArgv` passes them to `reachMatchArgvRows`, which matches over the returned table bytes in Go (`background_reach_probe_test.go:873-882`). No `sh -c` anywhere in this design; this ticket starts no child process, so there is no signal-handling or double-fork question.
- **[Cryptographic primitives]** Not applicable — no randomness, no hashing, no key material, no comparison against a secret. Nothing in the design is security-relevant-random; the one uniqueness requirement (the argv needle) is met by `t.TempDir()`, whose uniqueness is a test-isolation property and not a security one.
- **[Network & I/O]** No findings on the ticket's own surface — no socket, no listener, no HTTP server. Input size limits are inherited and explicit: `bufio.Scanner`'s 64 KiB default bounds the trailer scan (deliberately not raised — reading `scanner.Err()` is what separates "no trailer" from "unreadable"), and `reachCapCommand` caps every retained string at 512 bytes. The **`stderr []byte` parameter is unbounded**, and the fan-out walks it once per distinct group; see Open question 2 — deferred to whichever ticket produces a live stderr, since offline it is one line.
- **[Error messages, logs, telemetry]** No MUST FIX, one obligation made explicit. The file header must carry the failure-message content rule (§ Error handling): counts, pgids, verdicts, outcome and condition names are permitted; `pinScan.Matches`, the observation's `Line` and a `trailScanResult`'s trailer are not. The observation being a function-local rather than a return value is what makes the rule enforceable — the caller cannot print what it was never handed. No telemetry, no metrics, no user-identifiable aggregation.
- **[Concurrency]** No findings. The gather spawns no goroutine (so no leak is possible), takes no lock (so no ordering question arises), and mutates no shared state. `probeSyncBuffer` is mutex-guarded and `Bytes()` returns a copy, so the scan never reads memory another goroutine is appending to. No check-then-mutate. Shutdown safety is not in play: nothing here persists state, so no partial write can survive a signal. `go test -race` must be clean with no new synchronisation.
- **[Threat model alignment]** The operative threat for `internal/e2e/realclaude` is not in `protocol-mobile.md` but is stated across this package's headers: *a record whose whole value is that it can be published unreviewed must not inherit the operator-review-before-paste obligation.* The design addresses it three ways — a return type with no field that could hold captured bytes, an upstream signature (`[]int`) that closes the credential channel structurally, and an enforcing test that walks nested fields. Out of scope and named: the live probe's own redaction obligations (#1282) and the publishable-record / staging-gate outcome tier (#1278).

**Reviewer:** architect (self-review per `architect/security-review.md`)
**Date:** 2026-08-04
