# #1417 — The trailer gate admits an absent `terminal_reason` from a path that owes none

**Ticket:** https://github.com/pyrycode/pyrycode/issues/1417
**Size:** S (no split — see § Size check)
**Blockers:** #1419 and #1420, both merged at `93b2018`.
**Scope:** three files under `internal/e2e/realclaude/`, all `//go:build e2e_realclaude`. No production code. No new files.

Everything here runs offline: no live claude, no credentials, no daemon, no `t.Skip`, no env gate.
`go vet` and `staticcheck` in `make check` run **without** `-tags e2e_realclaude`, so neither analyses these files — the tagged test run is the only gate.

---

## Files to read first

Read these before editing. Every line range below was opened and confirmed at `93b2018`.

| Path | What to extract |
|---|---|
| `internal/e2e/realclaude/trailer_admissibility_test.go:104-131` | The gate's five values and the `gate-` prefix argument. `:124-129` is the out-of-contract doc whose "six sub-cases" count this ticket makes **five**. |
| `…:386-491` | `trailGate`'s empty-reason block. `:408-452` is the absence branch: the key-name membership read, the three-site switch on `trailReasonAgainstPath`'s answer, and the byte-budget paragraph. `:463-471` is the **one site whose value changes**. |
| `…:400-401`, `:438-446`, `:449-451` | Three inline claims this ticket falsifies: "no closed set grows and no consumer gains an arm", the append-vs-rewrite budget note, and "All three still answer out of contract; flipping one is a different ticket's decision". |
| `…:686-693` | `trailIsGateValue` — the membership predicate to extend. |
| `…:808-906` | `trailGateCases()`. Read `:814-823` (the uniform-path premise) — **D3 keeps it true and unedited**. |
| `…:910-1038` | `TestTrailAdmissibilityConstantsAreClosed`. `:917` says "twenty-nine" and the map holds **thirty-five**; `:920` says "eleven"; `:952` says "three answers and eight named voids". |
| `…:1069-1163` | `TestTrailGate`'s "out-of-contract details name their own sub-case". Its four probes all use `trailRunnerUnread()` → unchanged behaviour; only the counts at `:1070-1077` move. |
| `…:1166-1176` | `trailGateAbsenceCaseMarkers()` — **unchanged**, still three markers. |
| `…:1178-1343` | `TestTrailGateNamesWhichAbsenceCaseFired` — the driver AC2 amends. `:1184-1188` is the "six arms … `got.Value` says nothing about WHICH one ran" doc to correct; `:1200-1215` is the M1–M4 matrix to extend; `:1290-1300` is the precondition that becomes R2's discriminator. |
| `…:1372-1570` | `TestTrailGateIgnoresTheRunnerPathExceptAtTheAbsenceArm`. `:1413-1417` argues the value comparison "keeps running on the exempted row" — **this ticket makes that false**; `:1488-1500` is the code, `:1519-1569` the companion. |
| `…:1720-1771` | `TestTrailGateThenAdmit` — `:1741-1747` is the non-certifying switch that gains a fourth value. |
| `…:1852-1919` | The key-name leak sweep AC5 extends. Two rows today, both at `trailRunnerUnread()`. `:1894` says "six of trailGate's ten return sites". |
| `internal/e2e/realclaude/trail_run_outcome_test.go:100-169` | The eleven run outcomes. `:102` and `:136-138` (`trailOutcomeVoidNoTrailer`'s doc — the value the ticket forbids reusing, because a trailer *was* written). |
| `…:252-268` | `trailIsRunOutcome`. `:254` says "eleven". |
| `…:364-472` | Contract checks C1–C9. `:369-375` is C1's **published Detail** that both counts and enumerates all five gate values. `:402-422` is C4/C5 — the pair that makes an `Admit`-carrying test of the new arm impossible. |
| `…:474-511` | Step 1's switch, no default arm. `:476-483` states the hazard as "fall through to step 2"; § D5 re-derives it against what is *reachable*. |
| `…:596-632`, `:636-650` | `trailRunWellFormed()` / `trailRunProofReadings()` and `trailRunCases()`' producer-built row — the shapes the new fixture mirrors. |
| `…:914-975` | `TestTrailClassifyRun`. **`:970-974` iterates `trailRunOutcomeValues()` and errors on any outcome no row reaches** — a twelfth outcome without a `trailRunCases()` row is RED here. The ticket does not name this site; § D6 does. |
| `…:1074-1131` | `TestTrailRunComposesWithGateCases`. `:1126-1130` goes red on a `want` entry no fixture produced — which is why § D3 adds none. |
| `…:1191-1234` | `trailRunOutcomeValues()` and the `len != 11` pin. |
| `internal/e2e/realclaude/trailer_terminal_reason_test.go:80-126` | The six `reason-` values; `:83` is `trailReasonAbsentOwesNone`, the answer the new gate arm keys on. |
| `…:48-56` | The `# Scope` paragraph — already stale at HEAD and further falsified here. See § D7. |
| `…:196-272` | `trailReasonAgainstPath`'s two steps and its fixed-prose Detail guarantee — the reason the gate may safely embed `against.Detail`. |
| `internal/agentrun/streamrunner/runner.go:177-179` | The passthrough (`// Tee-parse stdout … bytes pass through unchanged` + `parser := newStreamParser(cfg.Stdout, nil)`). **Verified: 177-179, not the 170-176 two shipped comments cite.** |
| `internal/agentrun/streamrunner/runner.go:250-253` | The watchdog-only synthesis (`if wd.hasFired() { if !parser.hasSeenResult() { writeIdleStallResult(…)`). Verified. |
| `internal/e2e/realclaude/background_reach_probe_test.go:123`, `:945-950` | `reachMaxCommandBytes = 512`; `reachCapCommand` **truncates and marks**, never fails. |

---

## Context

`trailGate` answers `trailGateOutOfContract` for every trailer whose `terminal_reason` key is off the line, on every runner path. On the headless `PYRY_USE_STREAMJSON=1` path that shape is the *documented healthy* one: `streamrunner.Run` tees claude's stdout for the watchdog and passes the bytes through unchanged (`runner.go:177-179`), synthesising a trailer only when the idle-stall watchdog fired **and** claude emitted no result (`:250-253`). So a healthy stream run's trailer is claude's own `result` line and owes no `terminal_reason`.

`trailGateOutOfContract` feeds `trailClassifyRun`'s step-1 arm, whose whole documented purpose is that a caller's bug must not read as a measured void. Feeding it a correct reading from a correctly-wired run inverts that: a genuine measurement is filed as an instrument defect and the run reaches **no verdict about pyry at all**.

This ticket flips **exactly one** of `trailReasonAgainstPath`'s three absence answers — absence on a path that owes none — to a gate value of its own, and gives that value the run-level arm without which it would be an incomplete change rather than a smaller one.

**What is true after this ticket, and what is not.** Over fixtures the new value's arm and both refusals are reachable and provable. Over any *live* run today the reading always names no runner — both shipped gathers fill `RunnerPath` with `trailRunnerUnread()` (`finding_run_gather_test.go:552`, `:789`; `trail_run_rig_test.go:162`) — so the path-unnamed refusal is the only absence arm a live run can reach and the new value is unreachable from a live gather. **No comment added or edited here may claim this gate decides against the path a live run took.** Supply stays deliberately unowned; nothing here names a future supplier (#1420 already removed the cancelled #1374 from every comment).

---

## Size check

S. No red line trips.

- **New files:** 0. **New exported types:** 0 (two unexported string constants).
- **Total written work:** ~300–350 lines across 3 files, ~40 edit sites. The nearest analogue, #1420 (`faa3fbc`), landed 530 insertions / 115 deletions across 5 files in the same package and shipped clean at `size:s` with no `max_turns` salvage. This ticket projects *smaller* in lines and one file wider.
- **Consumer call sites:** no signature or type changes, so there is no edit fan-out to count. `codegraph_context` confirms `trailGate` and `trailClassifyRun` are the only entry points and neither's signature moves; every site below is an additive arm or a count re-derivation with its line number already resolved in this spec.
- **Reject branches:** `trailGate` keeps **ten** return sites (one changes its value, none is added); step 1's switch goes from five arms to six.
- **ACs:** five.

**No valid split seam exists.** The obvious cut — gate value in one child, classifier arm in another — ships a known hazard mid-way: a sixth value registered in `trailIsGateValue` with no step-1 arm falls through step 1, and because C4 forces `Admit` empty for a non-certifying value, step 2 cannot fire either; it reaches steps 3–8 and awards a **scan-side answer about pyry** from a record the gate says certifies nothing. The counts, the closed sets and the leak sweep all go false the instant the value lands, so none of them is a seam either.

**File-overlap check:** `git fetch origin --prune` then all 51 `origin/feature/<n>` branches diffed against `origin/main` — none touches `trailer_admissibility_test.go`, `trail_run_outcome_test.go` or `trailer_terminal_reason_test.go`. No blocker needed.

---

## Design

### D0. The byte budget — measure first, it is the binding constraint

Measured at `93b2018` by driving the shipped functions under `go test -overlay` (no worktree writes):

| absence reading | composed gate `Detail` | embedded `against.Detail` | headroom to 512 |
|---|---|---|---|
| ptyrunner | **461 B** | 264 B | 51 B |
| **streamrunner** | **460 B** | 269 B | **52 B** |
| indeterminate (×3) | **468 B** | 286 B | 44 B |
| present-and-empty | 442 B | — | 70 B |

`reachMaxCommandBytes` = 512. `trailNeedle` = 42 B. `reachTruncationMarker` = 29 B.

AC5 requires the new arm's leak row to assert **the needle would have fitted**. That fixes the ceiling:

> **The new arm's composed `Detail` must be ≤ 470 bytes** (`512 − 42`).

The embedded `against.Detail` is 269 B and fixed, so the gate's own prose has **201 B**. It spends 191 B today — **10 B of growth, total**. The prose is REWRITTEN, never appended to: #1420 recorded that appending truncates 1–5 bytes *past* the embedded value marker and publishes a severed sentence that still satisfies a marker assertion.

Measure before and after with the overlay recipe in § Testing strategy. Do not guess.

### D1. The new gate value

Add to the gate's const block (`trailer_admissibility_test.go:104-131`):

```go
trailGateAbsentOwesNone = "gate-absent-reason-owes-none"
```

Checked against every one of the 35 constants in the union map: distinct, and neither a substring nor a superstring of any of them — in particular not of `trailGateAbsenceCaseMarkers()`' three `reason-*` markers, which are matched with `strings.Contains` against composed Details.

Its doc records **what the reading supports and no more**:

- absence of `terminal_reason` is the streamrunner path's documented healthy shape, cited to the passthrough (`internal/agentrun/streamrunner/runner.go:177-179`) and to the watchdog-only synthesis (`:250-253`);
- it certifies no reason — `Reason` stays empty, which is what keeps `trailClassifyRun`'s C2 green without amendment;
- it says **nothing** about whether a process was alive, nothing about the sibling defect in #1369 (a trailer that *carries* a reason on a path that owes none — still `trailGateUsable` today, not this ticket's), and nothing about the path a *live* run takes.

`trailIsGateValue` (`:686-693`) gains the value. That is the only membership change on the gate side.

### D2. The gate arm — one return site changes its value

In `trailGate`'s absence branch (`:452-479`), the `case trailReasonAbsentOwesNone:` site (`:463-471`) returns `Value: trailGateAbsentOwesNone` with rewritten prose. Everything else about the branch is unchanged:

- presence is still the gate's **own** key-name membership read (`:408`), never `decodedReason != ""` — that is the collapse #1357's reading was landed to prevent (`trailer_terminal_reason_test.go:196-199`);
- **which** absence still comes from `trailReasonAgainstPath` **called**, never re-switched, and its `Detail` is still passed as a `fmt` argument (never concatenated into the format string, where a `%` in it would be interpreted);
- `Reason` stays empty; `RunnerPath: in.RunnerPath` is still echoed;
- the owes-one case and the path-unnamed fall-through keep `trailGateOutOfContract` with their own Details, unedited;
- the present-and-empty arm (`:481-490`) is untouched on every path.

The new prose must say, within the 470 B ceiling: the key is off the line, nothing is certified, and the observed path owes none — so this is that path's documented healthy shape and *not* a statement that the caller's record is broken. It must NOT copy the present-and-empty arm's `NO LIVE REPRO EXISTS` clause (that claim is true of a blank key and false of an absent one — `TestTrailGate:1149` already catches the copy-paste).

Three inline claims in the same function go false and are rewritten in place:

- `:400-401` "Every arm below answers `trailGateOutOfContract` and certifies nothing, so no closed set grows and no consumer gains an arm" — one arm now answers a value of its own; both closed sets grow; consumers gain arms.
- `:438-446` the append-vs-rewrite budget note — re-derive against § D0's measured numbers and the new 470 B ceiling.
- `:449-451` "All three still answer out of contract; flipping one is a different ticket's decision and this arm stays silent about it" — **this ticket is that decision.** Rewrite to say which one flipped and why, and keep the arm silent about #1369.

Also `:316-331` (the "carried to all ten return sites and READ at three" paragraph) — the numbers hold (ten sites, read at three), but the sentence describing what the three absence sites answer needs the new value named.

### D3. `trailGateCases()` keeps its nine rows and its uniform path

**Decision: no row is added to `trailGateCases()`.** This is the architect's call AC4 defers.

The alternative is structurally impossible rather than merely costly. `TestTrailGateIgnoresTheRunnerPathExceptAtTheAbsenceArm` overwrites `in.RunnerPath` with each of the five readings and checks the row's declared `want` against `readings[0]` (ptyrunner). An absence-shaped row wanting the new value would fail that premise at reading 0, where it correctly reaches `trailGateOutOfContract`. `trailGateCase.want` is one value; the new value is reachable only at one of the five readings. The two cannot both be true.

Consequences, all of them deliberate:

- `:814-823`'s uniform-path premise stays **true and unedited**. Every row keeps `trailRunnerUnread()`.
- `TestTrailRunComposesWithGateCases`' `want` map (`trail_run_outcome_test.go:1080-1086`) gains **no entry** — its coverage loop (`:1126-1130`) goes red on a `want` entry no fixture produces, so adding one would itself be the failure.
- `TestTrailGate`, `TestTrailGateThenAdmit` and `TestTrailRunComposesWithGateCases` keep their current row expectations unchanged.
- The new value is proved from its own drivers: `TestTrailGateNamesWhichAbsenceCaseFired`'s R2 (§ D4), the path sweep's companion (§ D4), the leak row (§ D8) and the classifier composition (§ D6).

`TestTrailGateThenAdmit`'s non-certifying switch (`:1741-1747`) **still gains the fourth value**, as AC4 mandates. No shipped row reaches it, so the arm is defensive: it is what keeps the sweep from fatalling the day a row does.

### D4. The two path-driven tests

**`TestTrailGateNamesWhichAbsenceCaseFired` (`:1228`) is amended, not replaced.** Its four rows stay; the table gains a per-row expected gate value.

- **R2** (`tdnFixtureStreamArgv`): `got.Value` stops being a shared precondition and becomes **the discriminator** — it must equal `trailGateAbsentOwesNone`. It keeps asserting its case marker as well (strictly stronger, and the companion below needs the marker present anyway).
- **R1, R3, R4**: expected value stays `trailGateOutOfContract`; all four rows keep the marker assertions, the `got.Reason == ""` assertion, the truncation-marker check and the length check.
- The doc's "Six arms answer `trailGateOutOfContract` after this ticket, so `got.Value` says nothing about WHICH one ran" (`:1184-1188`) is corrected: **five** arms answer it, and on R2 the value *is* the answer.
- The mutant matrix (`:1200-1215`) is **extended, not re-derived**. Its opening sentence "Every mutation is of `trailReasonAgainstPath`'s label switch" goes false — the new mutants are of the gate's own `against.Value` switch — and must be re-derived. Each new mutant with its sole red named:

  | | mutation | red rows | why |
  |---|---|---|---|
  | M5 | award the new value on the indeterminate answer | sole red **R3** | R3's value becomes the new one |
  | M6 | award it on the owes-one answer | sole red **R1** | R1's value becomes the new one |
  | M7 | award it on every absence answer at once | red **R1 and R3** | not a sole red for either — recorded so it is not mistaken for one |
  | M8 | key the new arm on the decoded reason rather than the key-name read | sole red **R4** | inside `reason == ""` the decoded test is constant, so present-and-empty routes into the absence switch |
  | M9 | let the new arm certify a reason | sole red **R2** | R2's `got.Reason == ""` assertion; `trailClassifyRun`'s C2 is the second, independent red |

  M1–M4 keep their shipped sole reds. Each new mutant is demonstrated under `go test -overlay` (§ Testing strategy) rather than asserted.

**`TestTrailGateIgnoresTheRunnerPathExceptAtTheAbsenceArm` (`:1435`) loses one clause on the exempted row.** After this ticket row nine's **value** varies by reading — `trailGateAbsentOwesNone` at reading 1, `trailGateOutOfContract` at the other four — so `:1491`'s `got.Value != base.Value` goes red against a correct build.

- The **certified-reason** comparison stays **unconditional** on every row. It is still invariant (`""` on all five absence readings) and it is what keeps a path-varying arm from starting to certify.
- The **value** comparison becomes exempt on `pathVaries` rows, alongside the byte comparison.
- Clause A (readings pairwise distinct), clause B (the arriving reading is read back, on every reading, unconditionally) and the `NumField() != 4` pin are untouched.
- `:1401-1424`'s doc — especially "The value and certified-reason comparison ALSO keeps running on the exempted row … strictly stronger than exempting it" — is rewritten to the narrower claim that is now true, and `:1488-1490`'s inline comment with it.

**The companion sub-test (`:1519-1569`)** currently asserts `got.Value != trailGateOutOfContract || got.Reason != ""` for all five readings. Its per-reading expectation becomes a pair: reading 1 → `trailGateAbsentOwesNone`, readings 0/2/3/4 → `trailGateOutOfContract`; `got.Reason == ""` stays unconditional across all five. The `len(details) != 3` pin (`:1564`) stays **3** — the three distinct Details are unchanged, one of them now belonging to a different gate value.

### D5. `trailClassifyRun` stays total

New outcome in `trail_run_outcome_test.go`'s const block:

```go
trailOutcomeVoidPathOwesNoReason = "run-void-path-owes-no-reason"
```

It is a **void**, not an answer: the trailer certifies no terminal reason, so there is no certified instant for a claim about aliveness-at-trailer to be about. Its doc must say why it is **not** `trailOutcomeVoidNoTrailer` — a trailer *was* written, and that value's own doc (`:136-138`) reads "no trailer line was written, so there is no 'when the turn was declared finished' instant to speak of".

Membership: `trailIsRunOutcome` (`:258`) and `trailRunOutcomeValues()` (`:1195`).

Step 1's switch (`:484-511`) gains a `case trailGateAbsentOwesNone:` arm returning that outcome. Its `Detail` names what decided: the gate read the path's documented healthy absence, so nothing is certified — and it does **not** claim the observed path is the path a live run took.

`:476-483`'s hazard note is **re-derived against what is reachable**, per the ticket. The shipped shorthand "would fall through to step 2 rather than be caught" is not the reachable shape for a value that certifies nothing: C4 (`:405-410`) rejects a non-certifying gate value arriving with any admissibility value, so `Admit` is forced empty and step 2's `trailAdmitProof` test cannot fire. An unhandled arm falls through to steps 3–8 and awards a **scan-side answer about pyry** from a record the gate says certifies nothing — the same hazard class, one step further down.

C1's published `Detail` (`:369-375`) both counts and enumerates all five gate values. It gains a sixth `%s` and its count word moves to six.

### D6. Proving the arm DECIDED

Two sites, sharing one fixture helper so they cannot drift:

```go
func trailRunAbsentOwesNoneReadings() trailRunReadings
```

mirroring `trailRunProofReadings()`' shape: `trailRunWellFormed()` with `Gate` built by the **real producers** — `trailGate(trailGateInput{Scan: trailGateAbsentReasonScan(), RunnerPath: tdnRunnerFromArgv(tdnFixtureStreamArgv)})` — and `Admit` left zero, which is exactly what a correct consumer leaves behind when the gate certified nothing (C4). Verified against every contract check: C1 ✓ (registered), C2 ✓ (`certifies` false, `Reason` empty), C3 skipped, C4 ✓, C5 ✓, C6–C9 ✓ from `trailRunWellFormed()`.

1. **`trailRunCases()` gains a row** using that helper, `want: trailOutcomeVoidPathOwesNoReason`. **This site is not named in the ticket and is mandatory**: `TestTrailClassifyRun:970-974` iterates `trailRunOutcomeValues()` and errors on any outcome no row reaches, so the twelfth outcome without a row is red there. (`TestTrailClassifyRun` also asserts every `Detail` is untruncated — the classifier arm's prose is short and has no budget problem.)

2. **A new end-to-end composition test**, `TestTrailRunComposesUnderAnAbsentReasonOnAPathThatOwesNone`, in `trail_run_outcome_test.go`. It asserts both halves AC3 asks for — that the gate reaches `trailGateAbsentOwesNone` from a real scan under a real reading, and that the composition reaches `trailOutcomeVoidPathOwesNoReason` — and carries the vacuity guard as a **controlled experiment inside the test**, not as a doc claim:

   - a **control** pair of readings differing only in the tail (`MatchCount` 1 vs 0) under a *usable* gate result with a non-proof `Admit`, asserted to reach **two different** outcomes (`trailOutcomeMatchedUnattributed` and `trailOutcomeNoRowMatched`) — this is what establishes the tails genuinely disagree;
   - the same two readings with only the gate answer swapped to the new value (and `Admit` zeroed, which is C4's correct-consumer obligation), asserted to **both** reach the new outcome.

   Invariance across tails that provably disagree is the proof that step 1 decided. The `-overlay` demonstration AC3 also asks for is recorded in the test's doc: with the arm deleted from step 1, the two rows fall through to their two *different* tail outcomes and go red naming different values.

   **It must not hand the classifier `trailAdmitProof` alongside the new value.** C4 and C5 reject that pair upstream of step 1, so such a test would re-prove the contract block and say nothing about the arm.

### D7. Comment corrections and count re-derivations

Every count is **re-derived from the set**, never adjusted by one. The known-false sites:

**Gate space five → six**
`trailer_admissibility_test.go:91`; `trail_run_outcome_test.go:370` (C1's published Detail — count **and** a sixth `%s`) and `:478`.

**Out-of-contract arms six → five**
`trailer_admissibility_test.go:124-129` (the whole "Six rather than four since #1420" paragraph — re-derive from the arms, do not decrement); `:1070-1077` ("since #1420 SIX arms" → five; "Four of the six arms are covered here. The other two are the absence cases" → four of five, and the one not covered is the **owes-one** case, since the path-unnamed one *is* covered by the `absentReason` probe); `:1184`; `:1894`.

**Non-certifying gate values three → four**
`trailer_admissibility_test.go:1737-1740` **and its `Fatalf` message at `:1744-1746`**, which enumerates three `%s`; plus `:529` in `trailAdmitAttribution`'s doc ("answer for the three gate values that certify nothing") — a residual the ticket does not list.

**Outcome space eleven → twelve**
`trail_run_outcome_test.go:102` (count **and** "three answers and eight named voids" → nine voids), `:254`, and the `len(values) != 11` pin at `:1217-1220`. Plus two residuals the ticket does not list: `trailer_admissibility_test.go:920` ("#1271's eleven run-level outcomes") and `:952` ("three answers and eight named voids").

**The union map's own size**
`trailer_admissibility_test.go:917` says "twenty-nine"; the map holds **thirty-five** today (5 gate + 7 admit + 11 run + 6 reason + 6 shipped) — it went stale when #1366 added six reason values. **Re-derive from the map**: after this ticket it holds **thirty-seven**. Do not add two to the printed number.

**The one stale scope paragraph, corrected deliberately**
`trailer_terminal_reason_test.go:48-56` says "Nothing here touches `trailGate` or `trailClassifyRun`, and nothing consumes this predicate yet", and states the sixth-gate-value hazard as future work owned by #1368/#1367. The first half was already false at HEAD (#1420 wired the predicate into `trailGate`); this ticket lands the sixth gate value and its arm, discharging the hazard and closing both named owners. Rewrite that paragraph to what is true. This is the one correction outside the ticket's own site list, and it is in scope because this ticket is what falsifies the rest of it — stated here so review does not read it as scope creep.

**Numerals.** Only one numeric pin moves: `trail_run_outcome_test.go:1217`. Confirmed by sweep — `NumField() != 4`, `len(details) != 3`, `rows != 9`, `len(seen) != 3`, `len(needles) != 3` and `len(needles) != 5` all stay.

The residual sweep in § Testing strategy is what confirms nothing else survives. **Run it; do not trust this list.** #1420 fixed all nine sites its spec listed and still shipped a stale count a thousand lines away in a test the spec never named.

### D8. The leak sweep must actually reach the new arm

`TestTrailAdmissibilityRecordsCarryNoCapturedBytes`' third sub-test (`:1852-1919`) has two hand-built rows, both driving `trailRunnerUnread()`. The absent-key row therefore reaches the **path-unnamed** fall-through and would go on passing without ever running the arm this ticket adds.

The row struct gains a `reading` field (explicit per row — the two shipped rows keep `trailRunnerUnread()`), and a third row is added:

- `keyNames: []string{"result", trailNeedle, "type"}` — no `trailReasonKeyName`, so the arm decides **absent**, with the needle planted exactly where the new arm reads;
- `reading: tdnRunnerFromArgv(tdnFixtureStreamArgv)`;
- `want: trailGateAbsentOwesNone` — the unique precondition for that arm;
- marker: `trailReasonAbsentOwesNone`, keeping the shipped "this row reaches the arm it is named for" pattern.

Because `reachCapCommand` **truncates rather than fails**, the row also asserts on the output:

- the `Detail` does not end in `reachTruncationMarker`; and
- `len(got.Detail) + len(trailNeedle) <= reachMaxCommandBytes` — otherwise the needle could not have fitted even had the arm leaked it, and the sweep below is vacuous.

Today that reads 460 + 42 = 502 against 512. **52 B of headroom is the whole margin and the rewritten prose spends it** (§ D0). Running both assertions on all three rows is acceptable and cheap.

### What must not move

- **`trailGateAbsenceCaseMarkers()`** — still the same three `reason-*` constants. Three cases; one of them now answers a different gate value.
- **`trailGateCases()`'** nine rows, their uniform `trailRunnerUnread()` path, and `:814-823`'s argument.
- **`TestTrailGate`'s** certification invariant (`:1057-1061`) — `certifies` is `Value == trailGateUsable || Value == trailGateBudgetFired`, so the new value with an empty `Reason` is green unamended. Same for `trailClassifyRun`'s C2.
- **`TestTrailRunComposesWithGateCases`'** `want` map and `predicateCalls != 2`.
- **The ptyrunner composition pin** (`trail_ptyrunner_composition_test.go`) — it drives `trailFixtureTrailer`, which reaches `trailGateUsable`, an arm that ignores the path. It must still reach `run-running-at-trailer` from the same inputs with its Details unchanged and green, including the cross-file cite it carries at `:56`.
- **No value from the trailer** enters any record added here; `trail_run_outcome_test.go:1142`'s sweep still passes.
- **The `170-176` cite** in `trailReasonAgainstPath`'s Details and doc (`trailer_terminal_reason_test.go:30`, `:91`, `:225`, `:234`) is stale — the passthrough is at `runner.go:177-179`, verified — but it is pre-existing debt introduced by #1366 and out of scope here. See § Open questions.
- **No doc under `docs/knowledge/`.** See § Handoff to documentation.

---

## Error handling

Nothing here can fail. `trailGate`, `trailReasonAgainstPath` and `trailClassifyRun` are pure over their inputs — no exec, no clock, no filesystem, no `*testing.T` — return no error and never fail a test; the new arms inherit that contract unchanged. The only failure surface is a `Detail` silently truncated by `reachCapCommand`, and § D8's output assertions are the tripwire.

The instrument's own failure modes stay named rather than collapsed: an unreadable scan is `trailGateScanAborted`, a record the producer cannot emit is `trailGateOutOfContract`, and the new value is neither — it is a **reading**, and the run-level answer it produces is a named void rather than an absence of one.

---

## Testing strategy

The suite runs offline. Verified at `93b2018` with `ANTHROPIC_API_KEY` and `CLAUDE_CODE_OAUTH_TOKEN` stripped: green in ~3.4 s, no `t.Skip`, no env gate — which is why this needs no `needs-real-claude`.

```bash
go test -race -tags e2e_realclaude -run '^TestTrail' -v ./internal/e2e/realclaude/
make check
```

**Measuring the byte budget, and running every mutant, with no worktree writes.** Write the throwaway file to the scratchpad and map it in with an overlay:

```bash
# overlay.json — the LHS path need not exist on disk
{"Replace":{"<repo>/internal/e2e/realclaude/zz_measure_test.go":"<scratch>/zz_measure_test.go"}}

go test -tags e2e_realclaude -overlay=<abs>/overlay.json -run '^TestTrail' ./internal/e2e/realclaude/
```

The same mechanism replaces a *shipped* file with a mutated copy: point the LHS at `trailer_admissibility_test.go` (or `trail_run_outcome_test.go`) and the RHS at a scratchpad copy carrying one mutation. Each of M5–M9 (§ D4) and the step-1-arm deletion (§ D6) is demonstrated this way, and the observed red rows recorded in the doc comment beside the matrix. A mutant that goes green is a missing row, not a lucky one.

**Scenarios the new and amended rows must cover** (bullets, not code — the developer writes these in the file's idiom):

- Absent key + streamrunner reading → `trailGateAbsentOwesNone`, `Reason` empty, `Detail` names the owes-none case and is untruncated with ≥ 42 B of headroom.
- Absent key + ptyrunner reading → `trailGateOutOfContract`, owes-one marker, no other marker.
- Absent key + each of the three indeterminate readings → `trailGateOutOfContract`, path-unnamed marker.
- Present-and-empty on every reading → `trailGateOutOfContract`, **no** absence marker, keeps `terminal_reason is empty` and `NO LIVE REPRO EXISTS`.
- The nine `trailGateCases()` rows under all five readings: value invariant except on the absence row, certified reason invariant everywhere, `RunnerPath` read back on every reading, Details byte-identical on the eight non-`pathVaries` rows.
- The absence row under all five readings: exactly three distinct Details; reading 1 alone reaches the new value.
- Composition: real gate over a real scan at a streamrunner reading → new outcome, under two tail variants that provably disagree with each other.
- Leak: needle planted as a key name, reaching the new arm, absent from the marshalled record.
- Coverage: every value in `trailRunOutcomeValues()` reached by a `trailRunCases()` row; every value in the union map distinct and non-empty.

**The residual count sweep**, run before commit:

```bash
rg -n -tgo '\b(three|four|five|six|seven|eight|nine|ten|eleven|twelve|twenty|thirty)[a-z-]*\b' \
   internal/e2e/realclaude/trail*.go
rg -n -tgo '(!=|==|<|>) *[0-9]+' internal/e2e/realclaude/trail*.go
```

Read every hit against the set it describes. (Use the `Grep` tool or plain `rg -n`; never bundle `-r`, which is *replace*, not recursive.)

**Commit message** states why each closed set grew, against the standing rule that they do not: the gate value because absence on a path that owes none is a reading rather than a caller's bug, and the run outcome because a gate value without a step-1 arm falls through to steps 3–8 and awards a scan-side answer about pyry from a record the gate says certifies nothing.

---

## Handoff to documentation

Not developer ACs — the developer's worktree mutates code, tests and this spec only. `docs/knowledge/features/e2e-realclaude.md` carries claims this ticket falsifies, at:

- `:789-796` and `:809` — "a five-value positive allowlist" and "the five-value gate allowlist and every downstream consumer are unchanged";
- `:803-808` — "arms still answer `trailGateOutOfContract` with an empty `Reason`";
- `:838-845` — "exactly one of eleven outcomes (three answers, eight named voids)";
- `:925` — "the eleven's `run-`".

---

## Open questions

1. **The `170-176` cite is stale.** `trailReasonAgainstPath`'s Details and doc cite `streamrunner/runner.go:170-176` for the passthrough; that range is the `childCtx` block, and the passthrough is at **177-179** (verified, and already cited correctly by #1420 at `trailer_admissibility_test.go:1153`). Introduced by #1366; the fix is byte-neutral (both ranges are 7 characters) at four sites. Left out of scope under AC5's "nothing else moves" — worth a follow-up ticket, not a silent fix here.
2. **Supply stays unowned.** No arm of the new value is reachable from a live gather, and nothing here may name a future supplier. The recorded route, when an arm first needs a live reading, is to pass the already-reduced reading in from the live driver's `h.Pin.ClaudeCommand`, which legitimately carries both needles and separates the populations afterwards.
3. **#1369 is the sibling.** A trailer that *carries* a `terminal_reason` on a path that owes none reaches `trailGateUsable` today and can be awarded a proof. `trailReasonAgainstPath` already folds present-and-empty and present-and-named together on that path under one value, so nothing here anticipates that split. The new arm's doc stays silent about it rather than half-answering it.

---

## Security review

**Verdict:** PASS

The asset under review is not a network surface. It is a **record marshalled into an artifact destined for a public GitHub issue and published unreviewed** — so the exposures that matter are what crosses into that record, what a `Detail` can be made to say, and whether a void can be made to read as an answer.

**Findings:**

- **[Trust boundaries]** No finding, but the boundary **moved** and is recorded. `trailScanResult.KeyNames` and `.Line` are claude's output (attacker-influenced in principle); `trailGateInput.RunnerPath` is `tdnRunnerFromArgv`'s reduced constant answer, never argv. The boundary stays a single function whose every `Detail` is fixed prose over its own constants — the new arm embeds `against.Detail`, whose fixed-prose guarantee is structural (`trailer_terminal_reason_test.go:201-214`), not a discipline. What moved: #1420 let the reading decide *which Detail*; #1417 lets it decide *which value*. Checked for a suppression channel and found none — a streamrunner absence yields no finding about pyry both before (`gate-out-of-contract` → `run-out-of-contract`) and after (`gate-absent-reason-owes-none` → `run-void-path-owes-no-reason`). Same suppression, honest name.

- **[Trust boundaries]** SHOULD FIX — **a hand-typed `RunnerPath` now reaches further than a Detail.** `trailGate`'s contract block checks `Scan.State` and the nil `Trailer`, never the reading, so a caller that hand-types `"streamrunner"` (rather than driving the shipped reader) reaches the new arm and publishes a reading from a record that is not one. No contract check is added, for two reasons stated rather than assumed: an eleventh return site would force clause B's totality argument in `TestTrailGateIgnoresTheRunnerPathExceptAtTheAbsenceArm` to weaken from "the nine rows reach all ten" — the trade #1420 documented and refused — and **no such failure has been observed**, every shipped caller going through `trailRunnerUnread()` or `tdnRunnerFromArgv` directly. The worst case is bounded and is the class this file already accepts: C4 forces `Admit` empty and step 1 answers before step 2, so a hand-typed reading can produce a **misnamed void**, never `run-running-at-trailer`. `reachRunnerPathFromArgv` cannot reach the arm at all — it has no streamrunner answer. Code-review should check the new arm's doc names what the reading is trusted to be.

- **[Tokens, secrets, credentials]** No finding. The family's doctrine exists because argv routes `CLAUDE_CODE_OAUTH_TOKEN` / `ANTHROPIC_API_KEY` into a public artifact, which is why `pinStateColumns` refuses a `command` column at the source. This ticket adds **no field to any record** (`NumField() != 4` stays pinned) and interpolates only `trailSeen`, this file's own constants and `against.Detail`. § D8's leak row and `TestTrailRunOutcomeCarriesNoCapturedBytes`' forbidden-key scan are the enforcing tests.

- **[File operations]** No finding — nothing added constructs, opens or writes a path. One process note: the mutation runs in § Testing strategy are mandated through `go test -overlay` with the copy in the scratchpad **specifically so a mutated shipped file cannot be left behind**. Mutating by editing the real file and forgetting to revert is the way this ticket ships a mutation; the overlay mandate is the mitigation.

- **[Subprocess execution]** No finding. `trailGate`, `trailReasonAgainstPath` and `trailClassifyRun` are pure — no exec, no clock, no filesystem, no `*testing.T` — and the new arms inherit that unchanged. Nothing added calls `tdnClaudeCommand`, `reachProc.Command`, `pinScan.Matches` or `reachRunnerPathFromArgv`. The suite runs with credentials stripped from the environment, re-verified at `93b2018`.

- **[Cryptographic primitives]** Not applicable — no randomness and no comparison against a secret. The `strings.Contains` marker checks compare published prose against published constants; nothing secret sits on either side, so constant-time comparison has no subject.

- **[Network & I/O]** **The finding that matters is input size, and it is the composed `Detail`.** `reachCapCommand` truncates and marks rather than failing, so an over-long `Detail` publishes a **severed sentence that still satisfies a marker assertion** — a false green on the exact assertion that names which arm ran. Mitigated by design rather than by care: § D0 fixes the ceiling at 470 B from measured values (460 B today, 269 B of it a fixed embedded string, so the Detail is bounded by construction and the cap is a backstop), and § D8 asserts on the **output** that the Detail is untruncated and that the 42 B needle would still have fitted. `KeyNames` is unbounded at its own tier (deferred to #1363 because `trailScanResult` is published by nothing) and `trailGateResult` **is** published — which is why no arm may interpolate a key name and why the leak row is a rung of its own.

- **[Error messages, logs, telemetry]** SHOULD FIX — **overclaim is the leak here, not bytes.** The new value's name, doc and `Detail` are published verbatim to a public issue, and the specific false claim to guard is "this gate decides against the path a live run took": no shipped gather knows the path, so such a sentence sends a reader hunting for a run that does not exist while mis-describing the one that does. That is the same defect `TestTrailGate:1098-1105` already catches for the sibling arm's `NO LIVE REPRO EXISTS` clause. Mandated in AC1/AC5 and in § Context and § D1; code-review must check every comment added or edited.

- **[Concurrency]** No finding, two invariants that must be preserved. `go test -race` runs this package's tests in parallel, so (a) the new fixture `trailRunAbsentOwesNoneReadings()` **must be a function, not a package-level var** — it carries a `Liveness` slice, and a shared backing array would let one row's mutation reach another's, which is why `trailRunWellFormed()`, `trailGateAbsentReasonScan()` and `trailGateAbsenceCaseMarkers()` are all functions; and (b) the amended companion sub-test must keep taking the scan **once** and assigning only `RunnerPath`, since a `trailGateInput` copy copies the `*resultTrailer` and the five inputs for a row alias one trailer. Nothing is ever written through that pointer, and that is what keeps the sharing race-free. No goroutine is spawned, so none can leak.

- **[Threat model alignment]** No finding. The applicable threats are the probe family's own, not `protocol-mobile.md`'s: *the artifact must be publishable unreviewed* (addressed — no value from the trailer enters any record added here, § D8) and *a void must never read as a negative* (addressed — the new run-level value is a named void that cannot decay into "the command had exited", and it is deliberately not `trailOutcomeVoidNoTrailer`, whose own doc says no trailer line was written). Named as out of scope with their owners: **#1369** (a trailer that *carries* a reason on a path that owes none — reaches `trailGateUsable` today and can be awarded a proof), and **supply of a live path reading**, deliberately unowned with the route recorded in § Open questions.

**Reviewer:** architect (self-review per `architect/security-review.md`)
**Date:** 2026-08-09
