# #1446 — The `gate-absent-reason-owes-none` arm consults the pinned-pid sighting route and publishes under a route of its own

**Ticket:** [#1446](https://github.com/pyrycode/pyrycode/issues/1446) · **Size:** `s` · **Labels:** `security-sensitive`
**Base:** `4118101` (`main`, after #1443 merged at `252389f`)
**Everything here is offline.** No live claude, no credentials, no daemon, no env gate, no `t.Skip`. Every function this ticket touches is pure over its input.

---

## Files to read first

The developer's turn-1 data load. Read these before writing anything; the design below assumes all of them.

| Path | What to extract |
|---|---|
| `internal/e2e/realclaude/trail_run_outcome_test.go:100-220` | The run value space and its `run-` prefix convention. **`:164-182`** is the arm this ticket changes; **`:183-214`** is its mirror, which must not move. |
| `…/trail_run_outcome_test.go:222-301` | `trailRunReadings` (gains two fields) and `trailRunOutcome` (gains one). The Detail content rule at `:276-283`. |
| `…/trail_run_outcome_test.go:396-530` | `trailClassifyRun`'s preamble + the nine contract checks. **C7 (`:488-497`) is the trap** — see § The one thing that will break every run. |
| `…/trail_run_outcome_test.go:530-600` | Step 1's switch and the comment (`:530-544`) that forbids falling through to steps 2-8. `case trailGateAbsentOwesNone` is `:565-577`. |
| `…/trail_run_outcome_test.go:602-620` | Step 2, the reap-log proof arm. Gains `out.Route` and **nothing else** (see AC1 budget note). |
| `…/trail_run_outcome_test.go:715-771` | `trailRunProofReadings` / `trailRunAbsentOwesNoneReadings` / `trailRunPresentOwesNoneReadings` — the fixture idiom the new helper mirrors, including *why* they are functions and why `Admit` is left zero. |
| `…/trail_run_outcome_test.go:1074-1135` | `TestTrailClassifyRun`: the per-row marker-absence check (`:1098`), the Gate/Admit provenance assertions (`:1105-1110`) the Route field joins, and the coverage loop (`:1130-1134`) that reddens on a value with no row. |
| `…/trail_run_outcome_test.go:1580-1627` | `TestTrailRunOutcomeCarriesNoCapturedBytes` — AC4's subject. Note the premise assertion at `:1595` and the forbidden-key walk at `:1618-1626`. |
| `…/trail_run_outcome_test.go:1633-1664` | `trailRunOutcomeValues()` and the `len(values) != 13` hard site. |
| `…/trail_sighting_liveness_test.go:145-201` | #1440's three outcome values and five reasons — the closed spaces this arm consumes. |
| `…/trail_sighting_liveness_test.go:236-401` | `trailSightingResult` and `trailEstablishSighting`. **`:256-284` is why both parameters arrive as whole records**; `:285-300` is the 4×4 assignment rule; `:355-364` is the ordering guard that makes AC2's unstaged case free. |
| `…/trail_sighting_liveness_test.go:459-490` | `trailSightingPID` (4242) and `trailSightingPin(verdict)` — reuse this, do not build a second pin fixture. |
| `…/trail_ordering_premises_test.go:107-161` | `trailOrderCertified` and `trailOrderResult`'s two-field shape; the "trap-free by construction" claim the whole-record parameter rests on. |
| `…/trail_ordering_premises_test.go:242-284` | `trailCertifyOrdering(trailerSighted, pyryExited, holdHeld bool)` — the real producer the new fixture drives. |
| `…/trail_run_instant_clause_test.go:30-119` | `trailRunCertifiesNothingArms()` and the **two-sided** check. Read `:58-69` before deciding anything about the clause. |
| `…/trail_run_instant_clause_test.go:121-191` | `TestTrailRunBareAlivenessAtTrailerIsBudgetFiredOnly` — a **source-file sweep**. See § The naming traps. |
| `…/trail_ptyrunner_composition_test.go:104-251` | The pin AC1 requires to pass **unamended**, including the Detail-headroom `else if` at `:219-226`. |
| `…/trailer_admissibility_test.go:1164-1298` | The union closed-set map, its stale-count history (`:1172-1177`) and the zero-record walk (`:1304-1330`). |
| `…/finding_trailer_evidence_test.go:783-785`, `:860-871` | `finTrailerOutcomeValues()` (derives from `trailRunOutcomeValues()`, so it needs no edit) and the `len(distinct) != 20` hard site (which does). |
| `docs/knowledge/codebase/1440.md`, `1434.md`, `1417.md` | The three nearest predecessors' lessons. #1434's is the one that names the bare-cite trap. |

---

## Context

`trailClassifyRun`'s step-1 switch answers `trailGateAbsentOwesNone` with an unconditional `return` of `trailOutcomeVoidPathOwesNoReason` (`trail_run_outcome_test.go:565-577`). #1440 landed `trailEstablishSighting` — a complete evidence route for the headless stream path, which writes no reap log at all on a clean exit — but wired it to nothing, because a route added at steps 2-8 never fires on the one gate value it exists to serve. This ticket reaches it, from inside the arm.

Three facts shape every decision below, and all three are **measured on `4118101`**, not inherited:

1. **The arm's Detail has 22 bytes of headroom, and its mirror has 11.** Re-measured by driving every row of `trailRunCases()` through `trailClassifyRun`:

   | arm | rendered | headroom |
   |---|---|---|
   | `run-void-path-owes-no-reason` (this arm) | 490 B | **22 B** |
   | `run-void-reason-not-owed-by-path` (mirror) | 501 B | **11 B** |
   | `run-running-at-trailer` (step 2) | 444 B | 68 B |

   `trailDeclaredFinishInstantClause` is **117 bytes** (the ticket body says 121; 117 is the measured `len()`). Cap is `reachMaxCommandBytes` = 512, and `reachCapCommand` truncates **silently**.

2. **Nothing stages either new input today, and that is what makes this safe to land alone.** Both shipped gathers fill the gate's runner-path field with `trailRunnerUnread()` (`finding_run_gather_test.go:552`, `:789`; `trail_run_rig_test.go:162`), so no live run reaches `trailGateAbsentOwesNone` at all. Every run in existence keeps landing exactly where it lands now.

3. **The unstaged pair needs no new code.** A zero `trailOrderResult` has `Value: ""`, which is not `trailOrderCertified`, so `trailEstablishSighting`'s first guard (`:357`) answers `trailSightingVoid` / `sighting-reason-ordering-uncertified` — *regardless of the pinned pid*. The unfilled shape therefore falls to today's void through the shipped predicate's own arm. AC2's hardest-sounding clause is satisfied by consuming the route as written.

---

## Design

### The new run-level value

```go
// trailOutcomeAliveAtSightingByOrdering: the gate read an absent terminal_reason
// from a path that owes none, and the pinned-pid sighting route established the
// command was alive when the trailer was SIGHTED. The second answer, and the
// only one from an evidence class other than the reap log.
trailOutcomeAliveAtSightingByOrdering = "run-alive-at-sighting-by-ordering"
```

**Why not `run-sighting-alive-by-ordering`** (the obvious `run-`-prefixed transform of #1440's `sighting-alive-by-ordering`): it would contain #1440's value as a substring. The union map compares for **equality** and cannot see that, but this family has already been bitten by containment — `TestTrailRunComposesUnderANamedReasonOnAPathThatOwesNone:1470-1478` ships a hand-written both-ways guard for exactly this reason. `run-alive-at-sighting-by-ordering` contains no union-map value and is contained in none.

**Why not `run-running-at-trailer`** — AC1 forbids it, and the reason is #1440's stated central risk (`trail_sighting_liveness_test.go:67-86`): the reap-log route and this one state the *same English sentence* from different evidence classes. That is what the Route field below is for.

### The route provenance field

`trailRunOutcome` gains one field, modelled on `Admit` — same `omitempty`, same "empty means the question was never reached" semantics, same per-row assertion treatment:

```go
// Route names the EVIDENCE CLASS that produced the verdict, so a reader never
// infers it from the outcome value. "" on every arm that reached no evidence
// route, exactly as Admit is "" where the predicate was owed no call.
Route string `json:"evidence_route,omitempty"`
```

Two constants and the pair of membership helpers this family requires of every closed space (`trailIsRunRoute`, `trailRunRouteValues`):

| constant | value | set on |
|---|---|---|
| `trailRouteReapLog` | `run-route-reap-log` | `trailOutcomeRunningAtTrailer` (step 2) |
| `trailRouteSighting` | `run-route-pinned-pid-sighting` | `trailOutcomeAliveAtSightingByOrdering` (step 1) |

`evidence_route` contains none of `command` / `args` / `comm` / `argv`, so the forbidden-key walk at `:1618-1626` needs no amendment — but check it, don't assume it.

### The two new readings

`trailRunReadings` gains both inputs **as whole records**. Narrowing either is foreclosed by `trailEstablishSighting`'s own doc (`trail_sighting_liveness_test.go:256-284`), and the reasons pull in opposite directions:

```go
// Ordering is #1439's certified-ordering result, taken WHOLE for the reason Gate
// is: trap-free by construction (trail_ordering_premises_test.go:154-157).
Ordering trailOrderResult
// PinnedPid is the re-read of a pid pinned while the command was still
// reachable — NOT a member of Liveness, which is the argv scan's per-pid set and
// goes blind once the group re-parents to init.
PinnedPid pinStateOutcome
```

`PinnedPid` is deliberately **not** folded into `Liveness`: `Liveness` feeds `tdnVerdictSummary` into the published record and feeds step 6's instrument-failure void, and a pinned pid landing in either would change answers on runs that have nothing to do with this route.

### The arm

Contract, not implementation:

```go
case trailGateAbsentOwesNone:
        sighting := trailEstablishSighting(readings.Ordering, readings.PinnedPid)
        if sighting.Value == trailSightingEstablished {
                out.Route = trailRouteSighting
                return decide(trailOutcomeAliveAtSightingByOrdering, /* … */)
        }
        return decide(trailOutcomeVoidPathOwesNoReason, /* … */ sighting.Value)
```

Step 2 gains one line — `out.Route = trailRouteReapLog` before its existing `decide`. **Its format string does not change** (see § Budgets).

**Consulted inside the arm, never below it.** Step 1's own comment (`:530-544`) states that a record the gate says certifies nothing must not be awarded a scan-side answer about pyry — "the same hazard class, one step further down". Falling through would hand this record to steps 3-8. The switch stays total over the gate's seven values with no default arm.

### The Detail content rule for both arms

The arm **MAY** name: this space's own value names, `sighting.Value`, `sighting.Reason`, the pinned pid as `%d`, and the gate value.
It **MUST NEVER** quote `readings.Ordering.Detail`, `readings.PinnedPid.Detail`, `readings.PinnedPid.ToolStderr`, `readings.PinnedPid.StateColumn`, or `sighting.Detail`. The last is not a leak risk (`trailEstablishSighting` is itself swept) but it is a budget risk and a duplication.

The finding arm **MUST** name the trailer's **sighting** as the instant its verdict is about, in its own prose. Do **not** reuse `trailSightingInstantClause` — that constant is scoped to #1440's predicate, and importing it here creates a cross-space coupling nothing checks.

---

## The one thing that will break every run

**Do not add a contract check for either new field.**

C7 (`:488-497`) validates every `Liveness` verdict against `pinIsVerdict` and answers `trailOutcomeOutOfContract` on a miss. It sits three lines above where a tenth check would naturally go, and the developer's instinct — validate the new `pinStateOutcome` the same way — is exactly wrong. A zero `pinStateOutcome` has `Verdict: ""`, which `pinIsVerdict` rejects; a zero `trailOrderResult` has `Value: ""`, which `trailIsOrderValue` rejects. **No shipped gather stages either input**, so such a check would answer `run-out-of-contract` on *every run that exists today* — filing a routine reading as an instrument defect, which is the collapse #1417 exists to prevent, one value along.

Instead, the contract block gains a **comment** stating that the two fields are deliberately unvalidated and why, positioned so a reader adding C10 meets the argument first. AC2's row proves it: `trailRunAbsentOwesNoneReadings()` already leaves both zero and must keep reaching `run-void-path-owes-no-reason`.

---

## Budgets — measured, and two of them are hard constraints

**The void arm must EXCHANGE, not add.** 22 bytes does not hold a route clause that interpolates `sighting.Value` (longest member: `sighting-not-established`, 24 B). The exchange region is the arm's closing tail — verified by inspection that **nothing asserts on it**: `TestTrailClassifyRun` checks only non-emptiness and marker-absence; `TestTrailRunComposesUnderAnAbsentReasonOnAPathThatOwesNone:1364` asserts `strings.Contains(got.Detail, trailGateAbsentOwesNone)`, which lives in the arm's *first* sentence; `TestTrailRunCertifiesNothingArmsNameTheInstant` asserts the shared clause. The `Kept apart from run-out-of-contract, …` half of the tail is ~68 rendered bytes and its argument is preserved in the constant's own doc at `:178-181`.

**Do not touch `trailDeclaredFinishInstantClause`.** It is 117 B and shared with `run-void-reason-not-owed-by-path`, which has **11 bytes** of headroom. Lengthening it by 12 bytes reddens that arm at `:1098`.

**Do not grow `run-void-reason-not-owed-by-path`.** The Technical Notes flag the neighbouring arms' "kept apart from" enumerations as candidates for growth now that a 14th value exists. The answer is **no**: the new value is an *answer*, not a void, so it is not that arm's sibling and belongs in no "kept apart from" list. Any growth there reddens.

**Do not grow `run-running-at-trailer`'s Detail by more than 24 bytes.** `trail_ptyrunner_composition_test.go:219-226` computes `room := reachMaxCommandBytes - len(out.Detail)` and reddens when `room < len(reading)`. Measured: `len(reading)` = 44 (`"ptyrunner (claude argv carries --session-id)"`), `room` = 68. AC1 requires that test to pass unamended. Setting `out.Route` does not touch `Detail`; naming the route *in* the Detail would burn the margin. Don't.

The new finding arm has the full 512 B — it is a new format string.

---

## The naming traps

**`TestTrailRunBareAlivenessAtTrailerIsBudgetFiredOnly` reads `trail_run_outcome_test.go` as a source file** and requires the literal `aliveness-at-trailer` to appear at exactly **2 sites**, each with `no attribution on that path could prove` on the same line. Every new doc comment, const doc and format string this ticket adds to that file must avoid the phrase — in prose as well as in code, because the sweep is a fixed-string line scan and cannot tell one from the other. `aliveness-at-declared-finish` (inside the shared clause) and `aliveness-at-a-trailer-sighting` are both safe; `aliveness-at-trailer` is not.

**Verify containment both ways before committing.** Run a one-off check that the new value and both route constants neither contain nor are contained in any other union-map value. The map's own test compares for equality and is blind to it; the containment guard that exists today (`:1470-1478`) is hand-written and scoped to three values.

---

## The instant-clause decision — the new value JOINS `trailRunCertifiesNothingArms()`

`TestTrailRunCertifiesNothingArmsNameTheInstant` errors in **both** directions, so the new row goes red on one branch or the other and the decision is forced. It joins the list. Four reasons:

1. **AC3's wording is the clause's wording.** "never claims the turn was declared finished — the gate certified nothing, so that instant does not exist on this path" *is* `trailDeclaredFinishInstantClause`. Carrying the shared constant is the checked form of the AC; hand-written prose is not.
2. **This is the one arm where the misreading is live.** It is the only arm in the run space that publishes a *positive finding* from a gate that certified nothing. Putting the clause on three voids and off the single finding that could be mistaken for an aliveness-at-declared-finish claim inverts the point.
3. **The budget is free.** New format string, 512 B, and 117 of them is affordable.
4. **The arithmetic stays clean.** 13 values / 3 carriers left 10 non-carriers; 14 / 4 leaves 10. `trail_run_instant_clause_test.go:63`'s "would redden the other ten" **stays correct** — only the "three" and the "thirteen" on `:62-63` change. Excluding the value would make it eleven and churn more prose.

**The one tension, and how it is resolved.** The clause opens "Nothing is certified", while on this arm the *ordering* is certified (`trailOrderCertified`). The word belongs to the gate in all four carriers, and the resolution is that **the finding arm's Detail never names the ordering's certification** — it names `sighting.Value` and `sighting.Reason` (`sighting-alive-by-ordering` / `sighting-reason-pid-running`), neither of which contains "certified". On the void arm the two agree anyway: `sighting-reason-ordering-uncertified` and "Nothing is certified" say the same thing. Recorded as Open Question Q1.

---

## Registration manifest — measured on `4118101`

The most mechanical part of the ticket and the easiest to half-finish; both analogues finished at `rework-count:2` on exactly this.

**Hard, red the moment the value lands:**

| site | change |
|---|---|
| `trail_run_outcome_test.go:1657` | `len(values) != 13` → `14`, and the failure message's "Thirteen rather than twelve since #1434 …" sentence rewritten for #1446 |
| `finding_trailer_evidence_test.go:865` | `len(distinct) != 20` → `21`, same message rewrite |

`finTrailerOutcomeValues()` (`finding_trailer_evidence_test.go:783-785`) derives from `trailRunOutcomeValues()` and needs **no** edit — only its count assertion does.

**Structural:**

- `trailIsRunOutcome` (`:309-320`) — add the value
- `trailRunOutcomeValues` (`:1633-1649`) — add the value
- `trailer_admissibility_test.go:1208-1283` — add **three** entries (the outcome + both route constants) and extend the map-size narrative at `:1172-1177` (51 → 54) and the consumer list at `:1192-1196`
- `trail_run_instant_clause_test.go:44-50` — add the value to `trailRunCertifiesNothingArms()`

**Prose, silent, and false once the value lands.** Eighteen occurrences of "thirteen"/"Thirteen" across six files — `trail_run_outcome_test.go:102`, `:305`, `:1659`; `finding_staging_gate_test.go:19`, `:21`, `:36`, `:69`, `:608`, `:611`, `:663`, `:670`; `finding_trailer_evidence_test.go:62`, `:261`, `:866`, `:868`; `finding_attribution_fanout_test.go:23`; `finding_exit_path_probe_test.go:149`; `trail_run_instant_clause_test.go:63`.

**Plus four sites no word-sweep for "thirteen" finds** — this ticket's value is an *answer*, so the answer count changes and the void count does not:

| site | today | after |
|---|---|---|
| `trail_run_outcome_test.go:102-103` | "thirteen: three answers and ten named voids" | fourteen: **four** answers and ten named voids |
| `trail_run_outcome_test.go:904` | `// --- the three answers ---` | `// --- the four answers ---` |
| `trailer_admissibility_test.go:1225` | "three answers and ten named voids since #1434" | **four** answers and ten named voids |
| `trail_run_instant_clause_test.go:62` | "three outcomes out of" | **four** outcomes out of |

**Correct as written — do not touch:** `trail_run_outcome_test.go:596` ("none of the ten voids does"), `:921` (`// --- the ten voids ---`), `trail_run_instant_clause_test.go:63` ("the other ten").

**Run all three residual forms**, because a word sweep is insufficient twice over — the two hard sites are numerals, and the prose above changes "three" without changing "thirteen":

```bash
cd internal/e2e/realclaude
grep -rn "hirteen\|hree answers\|hree outcomes" *.go            # word residual
grep -rn "!= 13\|!= 20\|want 13\|want 20" *.go                  # numeral residual
grep -rn "hirteenth\|ourteenth" *.go                            # ordinal residual (zero today; keep it zero)
```

**Out of scope:** 162 further cites into `trail_run_outcome_test.go` live under `docs/`, and `docs/knowledge/features/e2e-realclaude.md` carries "a thirteenth outcome, a tenth void". Neither analogue's test commit touched `docs/`, and neither does this one — the documentation phase owns them after the PR merges. Do **not** write `docs/knowledge/codebase/1446.md`.

---

## Cite renumbering — budget it as work, not as scope

`trail_run_outcome_test.go` carries **100 inbound line cites from 16 files** in this package (verified: `finding_run_gather_test.go` 25, `finding_attribution_fanout_test.go` 12, `finding_staging_gate_test.go` 10, `trail_sighting_liveness_test.go` 10, `trail_ordering_premises_test.go` 9, `trailer_admissibility_test.go` 9, `finding_run_record_test.go` 6, `finding_trailer_evidence_test.go` 5, `trail_ptyrunner_composition_test.go` 3, six files with 1-2 each). Inserting a const in the value space near the top shifts most of them.

Measured on the analogue commits, this is roughly two-thirds of the diff and is why the ticket is `size:s` rather than larger:

| ticket | files | insertions | of those, cite-carrying |
|---|---|---|---|
| #1417 (`573aca0`) | 16 | 112 | 92 |
| #1434 (`7f2128c`) | 19 | 168 | 109 |

**Two traps, both paid by predecessors:**

1. **Bare `(:NNN)` refs inherit the last-named file.** #1434 needed a separate follow-up commit (`8bf84b0`, "re-point four bare cites by named symbol, not last-named file") because a filename-anchored sweep reads clean over them. Scan statefully: track the last-named file as you walk each doc block.
2. **A third, symbol-anchored form exists** — `<TypeName>:NNN` and `trailClassifyRun:NNN` — which evades both a filename grep and a bare-ref scan.

Re-point by locating the cited *content* at HEAD, not by adding a fixed offset: the shift is not uniform, because insertions land at several depths in the file.

---

## Testing strategy

Scenarios, not test code. The developer writes them in this package's idiom.

**`trailRunCases()` gains two rows** (one is mandatory, one earns its line):

- *The finding.* A new `trailRunSightingEstablishedReadings()` — `trailRunAbsentOwesNoneReadings()` with `Ordering = trailCertifyOrdering(true, true, true)` (the **real producer**, as its neighbours drive the real gate) and `PinnedPid = trailSightingPin(pinStateRunning)` (reuse #1440's helper; do not build a second pin fixture). Reaches `run-alive-at-sighting-by-ordering`. **Mandatory** — the coverage loop at `:1130-1134` reddens on a value no row reaches.
- *The route consulted and refuted.* Same fixture with `PinnedPid = trailSightingPin(pinStateNoSuchProcess)`. Reaches `run-void-path-owes-no-reason`. This is the row that proves the arm did not become a blanket answer once the inputs are filled; without it, a build that ignored `sighting.Value` and returned the finding whenever the ordering certified would pass.

The existing row built from `trailRunAbsentOwesNoneReadings()` (both inputs zero) stays and keeps wanting `run-void-path-owes-no-reason` — that is AC2's unstaged case and the regression guard for the C10 trap.

**`TestTrailClassifyRun`'s loop gains the Route assertion**, alongside the Gate and Admit ones at `:1105-1110`, as the Technical Notes ask. Assert the *invariant*, not a second table that could drift from the classifier:

- `got.Route` is non-empty **exactly** on the two finding values
- when non-empty, `trailIsRunRoute(got.Route)` holds
- `run-running-at-trailer` → `trailRouteReapLog`; `run-alive-at-sighting-by-ordering` → `trailRouteSighting`

Putting it in the loop rather than in a new file is right *here* specifically because the const insertion displaces every cite below it regardless — the marginal displacement is ~10 lines and buys per-row coverage. (`trail_run_instant_clause_test.go:14-21` explains the opposite choice for a ticket that had no other displacement.)

**AC4 — `TestTrailRunOutcomeCarriesNoCapturedBytes` gains a fixture.** Split it into two `t.Run` blocks:

- the existing one, unchanged, still asserting its `run-running-at-trailer` premise at `:1595`
- a new one landing on `run-alive-at-sighting-by-ordering`, with `trailNeedle` planted in `Ordering.Detail`, `PinnedPid.Detail` and `PinnedPid.ToolStderr`. Premise-assert the value first (the existing block's discipline: the sweep must not pass by classifying garbage), then marshal and check for the needle, then re-run the forbidden-key walk over the new key set.

State honestly in the test's doc *what this sweep is*: there is **no live leak route today** — the arm reads `sighting.Value` and `sighting.Reason` only, and `trailClassifyRun` renders `Liveness` (via `tdnVerdictSummary`) but never `PinnedPid`. The needle plant is a discipline against a future edit that interpolates `pin.Detail` for a better failure message — the same framing #1440 used for its `trailOrderResult` plant, and the natural mistake the sweep exists to catch. Do not claim it catches a live leak.

**`TestTrailRunCertifiesNothingArmsNameTheInstant`** needs no new assertion — adding the value to `trailRunCertifiesNothingArms()` puts the new row on the presence branch, and its second premise loop (`:113-118`) then requires a row that reaches it, which the finding row supplies.

**Verification.** These files carry `//go:build e2e_realclaude`, so **`make check` does not compile them**. An exit code cannot tell a skip from a pass — read the count of tests that actually executed:

```bash
go test -race -tags e2e_realclaude -run '^TestTrail|^TestFin' -v ./internal/e2e/realclaude/ 2>&1 | grep -c '^--- PASS'
```

Then run the full package with the tag. Demonstrate the two new rows are non-vacuous under `go test -overlay` (no worktree writes) rather than asserting it: with the `sighting.Value == trailSightingEstablished` branch deleted, the finding row must go red; with the branch made unconditional, the refuted row must go red.

---

## Open questions

**Q1 — "Nothing is certified" beside a certified ordering.** The shared clause's opening reads against the *gate* in all four carriers, and the finding arm's Detail avoids the word by naming `sighting.Value` / `sighting.Reason` instead. If the developer finds the two adjacent in one rendered Detail, the fix is to reword the *arm*, never the shared constant (11 B of headroom on its other carrier).

**Q2 — should the route constants be their own space or reuse an existing one?** Specified as their own two-value space with `trailIsRunRoute` / `trailRunRouteValues`, joining the union map. The alternative — deriving the route from the outcome value — is rejected by AC3's own words ("never inferred from its outcome value"). If a third route ever appears, `trailRouteNone` for the twelve non-finding arms becomes worth revisiting; today `""` matches `Admit`'s shipped semantics exactly.

**Q3 — a both-ways containment check over the whole union map.** Specified here as a one-off verification step rather than a shipped test, on the evidence rule: the containment hazard has been met once (#1434) and handled with a targeted three-value guard. If code-review disagrees, the shipped form is ~15 lines in `trailer_admissibility_test.go` and belongs to that map, not to this ticket.

---

## Security review

**Verdict:** PASS

**Findings:**

- **[Trust boundaries] MUST FIX — addressed in the spec above.** Two new inputs cross into the classifier from a producer chain (`pinReadState` → `ps` subprocess → `pinStateOutcome`) that *can* carry captured bytes: `ToolStderr` takes raw `ps` stderr verbatim (`process_pin_liveness_test.go:341`) and `Detail` folds it in (`:348-349`). The boundary is explicit and singular — the arm reads `sighting.Value`, `sighting.Reason` and `pin.PID`, and the § Detail content rule forbids the other four fields by name. The first draft left this implicit; it is now a named rule with the AC4 sweep as the control. `Ordering` is trap-free by construction (three bools in, `trail_ordering_premises_test.go:154-157`).
- **[Tokens, secrets, credentials] No findings, and this is the category the whole record exists for.** `ps` output columns route an operator's `CLAUDE_CODE_OAUTH_TOKEN` / `ANTHROPIC_API_KEY` into an artifact destined for a public issue — which is why `pinStateColumns` refuses a `command` column at source (`process_pin_liveness_test.go:232`) and why the forbidden-key walk (`:1618-1626`) exists. The new `evidence_route` key contains none of `command` / `args` / `comm` / `argv`, and its two values are fixed literals interpolating nothing. **Verified rather than assumed:** the walk is re-run over the new key set in AC4's second block.
- **[Subprocess / external command execution] No findings — by parameter type, not by denylist.** `trailClassifyRun` and `trailEstablishSighting` are pure: no exec, no clock, no filesystem. The spec explicitly foreclosed `finSighting` as an input (it carries `TerminalReason` / `StopReason` / `Subtype` / `KeyNames`, which marshal as keys no `command`-shaped denylist would catch) by pinning the parameters to the two shipped records. Narrowing the parameter is the enforcement; widening the denylist could not be.
- **[Error messages, logs, telemetry] SHOULD FIX — mitigated, worth code-review attention.** Every Detail here is destined for an operator artifact pasted into a public issue. The live risk is not a leak but a *silent truncation* that removes the argument while leaving the claim: `reachCapCommand` truncates at 512 B without erroring, and the arm being changed has 22 bytes of headroom. Mitigated by the per-row marker-absence check (`:1098`), which covers the new rows for free, and by the three explicit budget constraints in § Budgets. Code-review should re-measure rather than trust the numbers in this spec.
- **[Concurrency] No findings.** No goroutines, no locks, no shared state. The one hazard in this family is fixture aliasing — `Liveness` is a slice and `go test -race` runs this package in parallel — which is why every fixture is a function, not a package-level var. The new `trailRunSightingEstablishedReadings()` follows that rule; `PinnedPid` is a struct value and adds no new aliasing surface.
- **[File operations] Not applicable.** The one file read in scope is `TestTrailRunBareAlivenessAtTrailerIsBudgetFiredOnly`'s `os.ReadFile` of a fixed in-repo filename (`trail_run_instant_clause_test.go:155-156`), unchanged by this ticket and taking no caller input.
- **[Cryptographic primitives] Not applicable.** No randomness, no keys, no comparisons against secrets.
- **[Network & I/O] Not applicable.** Everything is offline and pure over its input; no socket, no reader, no deadline.
- **[Threat model alignment] No findings.** The relevant threat is the one this whole file family is built around — a published probe record that must be safe to paste unreviewed. This ticket adds one string field with a two-value closed space and two whole-record inputs whose string-bearing fields are forbidden by name. It opens no new path to an operator's credentials.

**Reviewer:** architect (self-review per `architect/security-review.md`)
**Date:** 2026-08-10
