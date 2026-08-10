# #1448 — Separate a sighting route that was never staged from one whose premise failed and one whose pid read did not answer

**Size:** S (no split — see § Size check)
**Scope:** offline, `internal/e2e/realclaude/` only, `//go:build e2e_realclaude`
**Blocked by:** nothing. #1446 (closed 09:52Z) and #1447 (closed 12:20Z) are both merged at `45806e6`, which is this branch's base.

---

## Files to read first

Every line number below was re-resolved against `45806e6` on 2026-08-10. Read these before writing anything; the design is a five-line change surrounded by an enumeration cascade, and the cascade is where the turns go.

| Path | What to extract |
|---|---|
| `internal/e2e/realclaude/trail_run_outcome_test.go:745-878` | The step-1 gate switch. `trailGateAbsentOwesNone` is `:765-855` — the arm this ticket splits. Read all three of its exits (`:778-790` established, `:791-821` #1447's, `:822-855` the fall-through). |
| `internal/e2e/realclaude/trail_run_outcome_test.go:704-726` | The **no-C10 note**. Its closing sentence — "`trailRunAbsentOwesNoneReadings()` leaves both zero and is the row that proves it" — goes false under this ticket and must be corrected in place. |
| `internal/e2e/realclaude/trail_run_outcome_test.go:436-467` | `trailRunOutcome`'s fields and its Detail content rule. `Route`'s doc `:441-454` carries the delegation this ticket must discharge. |
| `internal/e2e/realclaude/trail_run_outcome_test.go:185-249` | `trailOutcomeVoidPathOwesNoReason` (`:185-213`) and `trailOutcomeVoidPinnedPidDidNotEstablish` (`:214-249`) — the two docs the new value must argue itself apart from, and the house idiom for a "deliberately NOT" block. |
| `internal/e2e/realclaude/trail_sighting_liveness_test.go:353-404` | `trailEstablishSighting`. The ordering guard is `:360-367`; note that it fires **regardless of the verdict**. |
| `internal/e2e/realclaude/trail_sighting_liveness_test.go:178-204` | The five reason constants. `trailSightingReasonPidReadFailed` (`:189-199`) **explicitly names the zero `""` of an unfilled `pinStateOutcome`**; `trailSightingReasonOrderingUncertified` (`:200-203`) names only #1439's three `trailOrderVoid*` values. That asymmetry is the whole design (§ Design, step 1). |
| `internal/e2e/realclaude/trail_sighting_liveness_test.go:432-458` | `trailSightingReasons()` / `trailIsSightingReason` — the membership predicate the new field's values are checked against. |
| `internal/e2e/realclaude/trail_run_outcome_test.go:1005-1054` | `trailRunAbsentOwesNoneReadings()` (the unstaged pair — **this fixture changes meaning**) and `trailRunSightingEstablishedReadings()`. |
| `internal/e2e/realclaude/trail_run_outcome_test.go:1431-1514` | `TestTrailClassifyRun`. The `wantRoute` map is `:1441-1445`; its doc `:1434-1440` contains a sentence that goes false. The truncation-marker check is `:1468-1471`, the coverage loop `:1509-1513`. |
| `internal/e2e/realclaude/trail_run_outcome_test.go:1716-1788` and `:1936-1947` | **Two drivers the ticket body does not name that go RED.** Both call `trailRunAbsentOwesNoneReadings()` and assert `trailOutcomeVoidPathOwesNoReason`. |
| `internal/e2e/realclaude/trail_run_outcome_test.go:1950-2119` | `TestTrailRunOutcomeCarriesNoCapturedBytes` — the three existing blocks and the `sweep` helper the two new blocks reuse verbatim. |
| `internal/e2e/realclaude/trail_run_instant_clause_test.go:36-139` | `trailRunCertifiesNothingArms()` and its conditional check. The new arm joins the list. |
| `internal/e2e/realclaude/trail_run_instant_clause_test.go:174-211` | `TestTrailRunBareAlivenessAtTrailerIsBudgetFiredOnly` — **a source-file sweep over `trail_run_outcome_test.go` that reddens if any new prose uses the phrase `aliveness-at-trailer` unqualified.** Read before writing the new value's doc. |
| `internal/e2e/realclaude/trailer_admissibility_test.go:1219-1349` | The union map (the new value joins at `:1258`-ish) and the zero-record walk `:1325-1370`. |
| `internal/e2e/realclaude/finding_trailer_evidence_test.go:860-872` | `len(distinct) != 22` — **executable**, goes red. |
| `internal/e2e/realclaude/background_reach_probe_test.go:123`, `:945-950` | `reachMaxCommandBytes = 512` and the fact that `reachCapCommand` truncates **silently**, appending the marker only past the cap. |
| `docs/specs/architecture/1447-measured-non-establishment-gets-its-own-run-value.md` | The immediately preceding ticket on this same arm. Its registration manifest and cite-sweep recipe transfer wholesale. |

---

## Context

The `trailGateAbsentOwesNone` arm of `trailClassifyRun` consults #1440's sighting route and, on `trailSightingVoid`, answers `run-void-path-owes-no-reason`. Three materially different runs reach that one published answer:

1. **The route was never staged.** No shipped gather fills `Ordering` or `PinnedPid`, so this is the shape every run produces today.
2. **The ordering was measured and a premise failed** — `sighting-reason-ordering-uncertified`.
3. **The pid read did not answer** — `sighting-reason-pid-read-failed`.

Today cases 1 and 2 are indistinguishable *even in the route's own reason*, and case 3 differs from them only inside the Detail's prose. Both facts were measured rather than inherited (`go test -overlay`, `45806e6`):

```
PROBE both unfilled                    -> sighting-void / sighting-reason-ordering-uncertified
PROBE ordering unfilled, pin running   -> sighting-void / sighting-reason-ordering-uncertified
PROBE ordering certified, pin unfilled -> sighting-void / sighting-reason-pid-read-failed
```

The first line is the defect: every run that exists today is reported under a reason whose own doc says the ordering "was any of #1439's three `trailOrderVoid*` values". `""` is not one of them. The record claims a premise was measured and failed on a run where nothing was staged.

---

## Design

Two changes, and the ticket's budget is exactly these two: **one new outcome value** for case 1, and **one new published field** carrying the route's own reason to separate cases 2 and 3.

### 1. The pre-decision is about the ORDERING alone, and which input is derived rather than chosen

The classifier decides one thing about its own inputs before consulting the route: **was the ordering staged?** It does *not* also test the pin, and that asymmetry is read off the route's shipped docs rather than picked:

- `trailSightingReasonPidReadFailed` already names the unfilled pin: *"…a verdict outside `pinIsVerdict`'s space **including the zero `""` of an unfilled `pinStateOutcome`**. Both say only that nothing was measured."* An unfilled pin therefore has a correct, argued home inside the route. **No guard is owed on that side.**
- `trailSightingReasonOrderingUncertified` names only *"#1439's three `trailOrderVoid*` values"*. An unfilled ordering has **no** home there, and lands on a reason that asserts a measurement. **The guard is owed on exactly this side.**

Testing both fields with `&&` would be worse, not merely redundant: on a half-staged pair (ordering unfilled, pin filled — line 2 of the probe above) an `&&` guard falls through and publishes `sighting-reason-ordering-uncertified`, which is the exact false claim this ticket exists to remove, one shape along. The single-input guard answers that shape correctly too.

**Contract:**

```go
// inside case trailGateAbsentOwesNone, BEFORE trailEstablishSighting is called
if readings.Ordering.Value == "" { return decide(trailOutcomeVoidSightingRouteNotStaged, …) }
```

The test on `""` is deliberately narrower than `!trailIsOrderValue(...)`. `""` is what an *unfilled* field carries and is the only shape any producer emits; a non-empty value nobody defined is a caller's bug, and filing one as "the route was never staged" would be the #1417 collapse mirrored. It keeps today's behaviour and is recorded in § Open questions.

The route is still **consumed whole and never re-decided** — this guard reads `Ordering.Value` and answers *before* the predicate is called, so it re-litigates nothing the predicate returned.

### 2. The new value

```go
trailOutcomeVoidSightingRouteNotStaged = "run-void-sighting-route-not-staged"
```

Swept both ways (`strings.Contains` in each direction) against all 66 values of the six spaces in `TestTrailAdmissibilityConstantsAreClosed`'s union map plus `finOutcomeValues()`: **0 collisions, 0 containments.** 34 bytes.

Its doc must argue three separations, in the house idiom of `:214-249`:

- **Deliberately NOT `trailOutcomeVoidPathOwesNoReason`**, the sibling it forks from: that one is the route staged and reporting it could measure nothing; this one is a route that had nothing to read. Publishing an unstaged pair under the sibling would file the absence of an instrument as a reading it produced.
- **Deliberately NOT `trailOutcomeOutOfContract`.** An unfilled input on a gather that never stages it is a routine reading, not a caller's bug. This is the collapse #1417 exists to prevent, one value along, and it is why no contract check is added (the no-C10 note at `:704-726` already carries the full argument — extend it, do not restate it).
- **Deliberately NOT a `run-`-prefixed transform of any sighting-space value**, for the containment reason `trailOutcomeAliveAtSightingByOrdering`'s doc states at `:133-138`: the union map compares for equality and is blind to containment.

> **Trap.** `TestTrailRunBareAlivenessAtTrailerIsBudgetFiredOnly` reads `trail_run_outcome_test.go` off disk and requires the phrase `aliveness-at-trailer` to appear at **exactly two** sites, each on a line also carrying `no attribution on that path could prove`. A doc sentence using that phrase reddens it twice (the count and the per-site argument check). Use "aliveness at the trailer's sighting" or the shared clause instead.

`Route` and `RouteReason` are both **`""`** on this arm. Argued rather than defaulted, which is the delegation at `:451-452`: `Route` names the evidence class *that produced the verdict*, and here no evidence class produced anything — the absence of one did. The value string is where a reader learns which route was missing.

### 3. The new field

```go
// on trailRunOutcome, immediately after Route
RouteReason string `json:"evidence_route_reason,omitempty"`
```

The key carries none of `command` / `args` / `comm` / `argv`, so the sweep's structural key walk (`:2021-2030`) passes unchanged.

**Published invariant, and it is a biconditional:**

> `RouteReason != ""` **iff** `Route == trailRouteSighting`, and when non-empty it is a member of `trailIsSightingReason`.

That holds because the reap-log route has no reason space at all — `trailAdmitResult` is a two-field record (`Value` + `Detail`) — so `trailRouteReapLog` publishes none and can never publish one. The field's doc should say so, since a reader seeing `evidence_route` without `evidence_route_reason` on `run-running-at-trailer` would otherwise wonder whether an arm dropped it.

The field's justification is #1440's own, quoted one layer up: a reason is *"a FIELD on the record rather than prose in the Detail … so a consumer tells [them apart] WITHOUT PARSING PROSE"* (`trail_sighting_liveness_test.go:171-177`). Cases 2 and 3 return the same value from the same route, so the run-level record needs the reason in the published bytes for the separation to exist at all.

### 4. Where each arm lands

| Case | Value | `Route` | `RouteReason` |
|---|---|---|---|
| route never staged (`Ordering.Value == ""`) | **`run-void-sighting-route-not-staged`** (new) | `""` | `""` |
| ordering measured, premise failed | `run-void-path-owes-no-reason` | `run-route-pinned-pid-sighting` | `sighting-reason-ordering-uncertified` |
| pid read did not answer | `run-void-path-owes-no-reason` | `run-route-pinned-pid-sighting` | `sighting-reason-pid-read-failed` |
| route established | `run-alive-at-sighting-by-ordering` (unchanged) | `run-route-pinned-pid-sighting` | `sighting-reason-pid-running` |
| route measured, did not establish | `run-void-pinned-pid-did-not-establish` (unchanged) | `run-route-pinned-pid-sighting` | `…-pid-reaped-pending` / `…-pid-gone` |
| reap-log proof | `run-running-at-trailer` (unchanged) | `run-route-reap-log` | `""` |

### 5. `Route`'s doc: the delegation, discharged

`:451-452` currently reads *"#1448 owns whether those remaining cases ever name a route."* That sentence is **replaced**, not left standing. The rule it amends:

> #1447 set it as "the route is named where the route's own measurement decided the value", and left `""` on the fall-through because the verdict rested on the gate reading alone. Since #1448 that is no longer true of the fall-through: once the never-staged case has a value of its own, the fall-through's value *means* "the route was staged, was consulted, and reported it could measure nothing" — and the route's own answer is what separates it from its new neighbour. So the route is named there, and `""` moves to the arm where no route was staged at all.

### 6. Detail budgets — measured, and the split IS the exchange

Measured over every row of `trailRunCases()` at `45806e6` via `go test -overlay` (nothing written to the worktree):

| arm | rendered | headroom |
|---|---|---|
| `run-void-reason-not-owed-by-path` | 501 | 11 |
| `run-void-pinned-pid-did-not-establish` (longer reason) | 492 | 20 |
| `run-alive-at-sighting-by-ordering` | 469 | 43 |
| **`run-void-path-owes-no-reason`** (the arm being split) | **461** | **51** |
| `run-running-at-trailer` | 444 | 68 |

The split arm decomposes as **117 clause + 60 interpolated + 284 prose**. Budgets for the two arms it becomes:

- **New never-staged arm.** Interpolates the gate value (28) and the sibling it is kept apart from (28) = 56, plus the 117-byte clause ⇒ **339 bytes of prose available**; target ≤ 461 rendered, i.e. ≤ 288 of prose. Room, but not much — this is an exchange, not an append.
- **Remaining fall-through arm.** Interpolating the gate value (28), `sighting.Value` (13) and the new neighbour (34) = 75, plus the clause ⇒ **320 bytes of prose available**. It must not grow: the reason it used to carry in prose now travels in the field, so it should land **shorter** than 461.

Both must keep naming `trailGateAbsentOwesNone` — `TestTrailRunComposesUnderAnAbsentReasonOnAPathThatOwesNone:1743` asserts it by `strings.Contains`.

`TestTrailClassifyRun:1468-1471`'s marker-absence check runs on every row, so an over-budget arm reddens as soon as it has a row rather than truncating silently. **Do not verify budgets by reading; render them.**

> **The never-staged arm's Detail MUST NOT interpolate `PinnedPid.Verdict`, and this is a prohibition rather than a preference.** The record's content rule at `:425-435` *permits* naming "verdicts", and with 55 bytes of slack the natural sentence to write is "the pair is unstaged — the pinned pid read %q". Do not write it. `PinnedPid` is the **one input this classifier has no contract check over** — the no-C10 note at `:704-726` argues that omission deliberately, and C7 validates only the `Liveness` verdicts, never this field. Every other string the run-level Details interpolate is either a closed-space constant validated upstream (gate, admit, route, sighting values) or an integer. `PinnedPid.Verdict` on this arm is neither: it is an arbitrary unvalidated string, and interpolating it would make this the first run-level arm to publish one. The permission in the content rule was written when `pinReadState`'s four constants were the only producer; it is not a licence here.
>
> The same applies to `Ordering.Value`, which the guard has already proved is `""` — printing it says nothing and re-opens the same class. The arm interpolates the gate value and the sibling value it is kept apart from, and nothing else.

---

## Concurrency model

None. `trailClassifyRun` and `trailEstablishSighting` are pure over their inputs — no exec, no clock, no filesystem, no goroutines, no `*testing.T`. Every fixture helper stays a **function** rather than a package-level var, for the reason `trailRunWellFormed()`'s doc gives at `:981-982`: `Liveness` is a slice and `go test -race` runs these tests in parallel, so a shared backing array would let one row's mutation reach another's.

---

## Error handling

There is no error path. An instrument failure observed mid-turn is a datum to publish, not a reason to abort — the standing contract of `trailGate`, `trailCertifyOrdering`, `trailScan`, `pinReadState` and `trailEstablishSighting`. The three cases this ticket separates are all *publishable readings*; **none of them may reach `trailOutcomeOutOfContract`, and no contract check fires on any of them** (AC1). The no-C10 note at `:704-726` is the standing argument for that and must be **amended, not restated**: its closing sentence claims the unfilled pair "reaches its ordering-uncertified void through that function's own first guard … and this arm keeps today's answer", which this ticket makes false.

---

## The edit manifest

Discovery is pre-paid here so the developer spends turns editing, not grepping. Counts verified by `grep` at `45806e6`.

### A. `trail_run_outcome_test.go` — the design surface

| Site | Change |
|---|---|
| `:185-213` | `trailOutcomeVoidPathOwesNoReason` doc: `:194-200` claims the value is "what remains of that arm once the route has answered" and that separating the remaining cases "is #1448's work". Both go false — rewrite to "the route was staged and reported it could measure nothing", and name the new sibling. |
| after `:249` | **New const + doc** (§ Design 2). Insert inside the value-space block, after `trailOutcomeVoidPinnedPidDidNotEstablish`. |
| `:441-454` | `Route` doc: discharge the delegation (§ Design 5). |
| after `:454` | **New field** `RouteReason` + doc (§ Design 3). |
| `:475-488` | `trailIsRunOutcome`: `+1` case entry. |
| `:704-726` | The no-C10 note: amend the closing paragraph (§ Error handling). |
| `:765-777` | Insert the guard *before* the `trailEstablishSighting` call, with its own argument block (§ Design 1). |
| `:783`, `:814` | Add `out.RouteReason = sighting.Reason` beside each existing `out.Route = trailRouteSighting`. |
| `:822-855` | Fall-through: set `out.Route` + `out.RouteReason`; re-budget the Detail (§ Design 6). Its `:834-836` sentence ("either because the pid read did not answer or because the ordering was never staged") goes false — the second disjunct now has a value of its own. |
| `:1005-1027` | `trailRunAbsentOwesNoneReadings()` doc: it is now the **never-staged** fixture. Say so. |
| after `:1054` | **Two new fixtures** (§ Testing strategy). |
| `:1229-1230`, `:1256` | Section comments: "the four answers" stays (the new value is a void); "the eleven voids" → twelve. |
| `:1297-1300` | Re-point this row's `want` to the new value. |
| after `:1322` | **Two new rows.** |
| `:1434-1445` | `wantRoute`: `+1` entry for `trailOutcomeVoidPathOwesNoReason`. Its doc's closing clause — "including on the fall-through … where the route was consulted, measured nothing, and must therefore publish none" — goes false; rewrite to name the never-staged arm as the `""` carrier. |
| in `TestTrailClassifyRun` | **New per-row reason assertions** (§ Testing strategy). |
| `:1730`, `:1776-1779` | Re-point to the new value. |
| `:1938-1945` | Re-point to the new value; the "they differ" claim survives. |
| `:1961-1977` | The three-blocks doc becomes five; state what each new block premises. |
| after `:2118` | **Two new sweep blocks.** |
| `:2125-2143` | `trailRunOutcomeValues()`: `+1` entry. |
| `:2151-2158` | `!= 15` → `!= 16`; rewrite the message for #1448. |
| `:102-103`, `:471`, `:874` | Count prose (see § B). |

### B. The count cascade — sweep by SENSE, not by word

The set grows **fifteen → sixteen**, and its void sub-count **eleven → twelve**. The union at `finding_trailer_evidence_test.go` grows **22 → 23**.

**Move (verified):**

| File | Lines |
|---|---|
| `trail_run_outcome_test.go` | `102`, `103`, `471`, `874`, `1256`, `2151` (**assertion**), `2152-2154` |
| `finding_trailer_evidence_test.go` | `62`, `261`, `865` (**assertion**), `866`, `868` |
| `finding_staging_gate_test.go` | `19`, `21`, `36`, `69`, `608`, `611`, `663`, `670` |
| `trailer_admissibility_test.go` | `1237` |
| `finding_exit_path_probe_test.go` | `149` |
| `finding_attribution_fanout_test.go` | `23` |
| `trail_run_instant_clause_test.go` | `83` — "five outcomes out of fifteen … the other ten" → "six out of sixteen … the other ten". **Ten is unchanged** (16 − 6 = 10); do not touch it. |

**Do NOT touch — same word, different sense:**

- `trail_sighting_liveness_test.go:315` — "fifteen" is `TestTrailSightingAllSixteenCombinations`' 4×4 grid minus one.
- "eleven" as `trailGate`'s **return sites**: `trailer_admissibility_test.go:389`, `:391`, `:532`, `:1139`, `:1755`, `:2034`, `:2040`, `:2746`.
- "eleven" as the trailer's **top-level envelope key names**: `trailer_key_names_test.go:110`, `:140`; `finding_key_name_bounds_test.go:105`, `:244`; `finding_key_name_containment_test.go:273`, `:359`, `:412`, `:538`, `:590`; `finding_artifact_write_test.go:121`; `finding_run_gather_test.go:408`; `finding_trailer_evidence_test.go:181`, `:201`.
- "eleven" as this rig's **instruments**: `teardown_liveness_probe_test.go:76`.
- **New trap this ticket introduces:** the set now *is* sixteen, so a `sixteen` grep hits three unrelated senses — `trail_run_outcome_test.go:2218` and `trail_run_instant_clause_test.go:17` are **file counts** (still 16 after this change; no new citing file is added), and `TestTrailSightingAllSixteenCombinations` is the 4×4 grid. Leave all three.
- Ordinal/numeral residuals need the same pass, case-insensitively and allowing a hyphen or space: `fifteenth`, `15`, `twenty-two`, `twenty two`, `sixteen-value`.

### C. Registrations

| File | Change |
|---|---|
| `trail_run_outcome_test.go:475-488` | `trailIsRunOutcome` `+1` |
| `trail_run_outcome_test.go:2125-2143` | `trailRunOutcomeValues()` `+1` |
| `trailer_admissibility_test.go:1245-1261` | Union map `+1` entry, `"trailOutcomeVoidSightingRouteNotStaged"` |
| `trail_run_instant_clause_test.go:62-70` | `trailRunCertifiesNothingArms()` `+1` — the new arm forecloses a claim on certifies-nothing grounds, exactly as `trailOutcomeVoidPathOwesNoReason` does, so it carries `trailDeclaredFinishInstantClause`. Amend the list's doc (`:36-61`) to say why. |
| `trailer_admissibility_test.go:1330-1361` | Add `zeroRun.RouteReason` to the zero-record walk, in the shape `zeroSighting.Reason` already has at `:1346-1348` and `:1358-1361`: an unfilled reason must never read as one a consumer can branch on. That consumer is precisely AC1's reader. |

**No reason value is added to any closed space.** Both published reasons already exist and are already in the union map at `trailer_admissibility_test.go:1290-1294`.

### D. The cite cascade

101 line-cites in 16 `.go` files point into `trail_run_outcome_test.go`. The const insertion sits at ~`:250`, so **~88 of them shift**. Cite counts per file, highest first: `finding_run_gather_test.go` 25, `finding_attribution_fanout_test.go` 12, `trail_sighting_liveness_test.go` 10, `finding_staging_gate_test.go` 10, `trailer_admissibility_test.go` 9, `trail_ordering_premises_test.go` 9, `finding_run_record_test.go` 6, `finding_trailer_evidence_test.go` 5, `trail_ptyrunner_composition_test.go` 3, then 2 or 1 each in `finding_stage_held_group_test.go`, `finding_key_name_containment_test.go`, `finding_exit_path_probe_test.go`, `finding_artifact_write_test.go`, `trailer_terminal_reason_test.go`, `trailer_key_names_test.go`, `finding_key_name_bounds_test.go`.

Recipe (#1447's, unchanged): compute a git-diff line map at `-U0` and re-point mechanically, then verify by resolving each cite's symbol. Three cite forms all need catching and a filename grep finds only the first: `file.go:NNN`, a bare `(:NNN)` **inheriting the last-named file**, and a symbol-anchored `<TypeName>:NNN`. A bare cite whose number exceeds the inherited file's `wc -l` is *misattributed*, not stale.

`docs/**` cites (~192) belong to the documentation phase. Leave them.

---

## Testing strategy

### New fixtures

Two, both built on `trailRunAbsentOwesNoneReadings()` and both driving the **real** producers rather than struct literals (the reason `trailRunSightingEstablishedReadings():1033-1039` gives: a hand-built `trailOrderResult{Value: trailOrderCertified}` would let a row pass against an ordering predicate that certifies nothing):

- **`trailRunSightingOrderingUncertifiedReadings()`** — `Ordering = trailCertifyOrdering(false, true, true)`, `PinnedPid = trailSightingPin(pinStateRunning)`. Verified to reach `order-void-trailer-unsighted` → `sighting-void` / `sighting-reason-ordering-uncertified`. A pid deliberately **running**, so the row proves the ordering outranks the verdict and cannot pass by keying on the pin.
- **`trailRunSightingPidReadFailedReadings()`** — `Ordering = trailCertifyOrdering(true, true, true)`, `PinnedPid = trailSightingPin(pinStateInstrumentFailed)`. Verified to reach `order-certified` → `sighting-void` / `sighting-reason-pid-read-failed`.

Each fixture's doc states the value **and reason** it is built to reach, so a row retargeted by a later edit reads as a mistake.

### `trailRunCases()` rows

- Re-point the existing row at `:1297-1300` to the new value. Its name should say the pair is unstaged rather than that the reason is absent.
- **New row:** ordering measured and refused, pid running → `run-void-path-owes-no-reason`. Without it, `trailRunOutcomeValues()`' coverage loop at `:1509` reddens on the fall-through value, which no row would otherwise reach.
- **New row:** ordering certified, pid read failed → the same value under the other reason. This is the row that makes the separation non-vacuous: a build that published one fixed reason on the arm passes with the first row alone.

### `trailRunCase` gains a `wantReason` field

A `map[value]reason` cannot express this claim: two rows reach one value under two reasons, which is the whole point of the ticket. A per-row column is the only shape available. Every non-sighting row leaves it at Go's zero `""`, which is also the required answer there, so the column costs one line on five rows and nothing elsewhere. The drift risk `wantRoute`'s doc (`:1434-1440`) avoided by using an invariant is bounded here by asserting, on **every** row:

- `got.RouteReason == tc.wantReason`
- `(got.RouteReason != "") == (got.Route == trailRouteSighting)` — the biconditional, with a message naming the reap-log route's lack of a reason space as the reason it holds
- `got.RouteReason == "" || trailIsSightingReason(got.RouteReason)` — a reason a reader cannot look up is a verdict they cannot interpret

### Re-pointed drivers

`TestTrailRunComposesUnderAnAbsentReasonOnAPathThatOwesNone` (`:1730`, `:1776-1779`) and the sub-test at `:1936-1947` both drive `trailRunAbsentOwesNoneReadings()` and assert the old value. Re-point the `want`s; do **not** swap the fixture. Their subject is the *gate* reading, which is unchanged, and the pairing with `trailRunPresentOwesNoneReadings()` (whose route is likewise unstaged) is what makes `:1936-1947`'s comparison honest.

### Two new no-captured-bytes blocks

Both reuse the existing `sweep` helper (`:2006-2031`) verbatim and both premise-assert value, `Route` and `RouteReason` before sweeping, in the shape of the three shipped blocks:

- **The never-staged arm.** Plant the needle in `Ordering.Detail` (leaving `Value` at `""`), in `PinnedPid.Detail` + `ToolStderr`, **and in `PinnedPid.Verdict` itself**. This is AC6's named obligation plus the deterministic half of § Design 6's prohibition. The `Verdict` plant is the one that matters and it is only possible here: the guard fires on `Ordering.Value` regardless of the verdict, so this arm is reachable with an arbitrary `Verdict` string, and `PinnedPid` is the input no contract check validates. A prose rule saying "do not interpolate the verdict" is a stochastic net over a stochastic author; this plant is the deterministic one, and it reddens on the needle rather than on the cap. Keep the planted string short for the reason the block below records. Premise: value is the new one, `Route == ""`, `RouteReason == ""`.
  Note what this block's **key walk** does and does not cover, so nobody later "strengthens" it by dropping a premise: both new fields are `omitempty` and both are empty here, so the walked key set is a strict **subset** of the one blocks 2, 3 and 5 walk. That is fine and needs no fourth field — the superset is walked three times over — but it means the `Route == ""` / `RouteReason == ""` premises here are asserting the *record shape*, not anchoring the walk. The anchoring premises are block 5's, and deleting those is the shape of the hole #1446 was reworked for (`:2099-2116`).
- **The two-remaining-cases arm.** Same three plants over `trailRunSightingOrderingUncertifiedReadings()`. Premise: `run-void-path-owes-no-reason`, `Route == trailRouteSighting`, `RouteReason == sighting-reason-ordering-uncertified`. The `RouteReason` premise is load-bearing for the same reason `:2099-2116` argues the `Route` one is: it anchors the key walk to the **field-bearing** key set, so an arm that later dropped the field would shrink the swept set without reddening anything.

State plainly, as `:1979-1987` does, what these blocks are: there is **no live leak route** through the new inputs today — the arm reads `sighting.Value` and `sighting.Reason` and nothing else. The plant is a discipline against a future edit that interpolates `pin.Detail` for a better failure message.

> Measured caution from #1447: a plant that overruns the 512-byte cap reddens the **truncation-marker** check rather than the needle check. That is a budget kill, not a leak kill — different fabric. Keep planted strings short enough that the needle is the thing that fails.

### AC3's pin — read it, do not amend it

`trail_ptyrunner_composition_test.go:180-251` must pass **unamended**. It is safe by construction under this design and that was checked, not assumed: step 2's arm is not touched, `run-running-at-trailer` renders 444 bytes (68 headroom) unchanged, and `RouteReason` stays `""` there because `trailAdmitResult` has no reason to publish. The headroom check at `:219-225` is over `out.Detail` only, so a new record **field** cannot affect it.

### Verification

`make check` does **not** compile these files — they carry `//go:build e2e_realclaude`. A green `make check` is evidence of nothing here.

```
go test -race -tags e2e_realclaude -run '^TestTrail|^TestFin' ./internal/e2e/realclaude/ -v
```

**Read the count of tests that actually executed.** An exit code cannot tell a skip from a pass. Nothing needs a live claude: the full `trailRunCases()` table was driven to completion at `45806e6` with no credentials while this spec was written. The `e2e_realclaude` tag is where these files live, not a dependency on a live turn.

For byte budgets and name sweeps, use `go test -overlay=<abs-path json>` with a scratch file outside the worktree — it renders real Details without writing anything the auto-commit would pick up.

---

## Size check

**S. No split.** Recorded so it can be second-guessed with the same evidence:

- **Refactor-shape precondition: unmet.** No rename, no signature change, no type replacement, no cross-package import flip. One additive const, one additive struct field, one additive `trailRunCase` field. Every existing caller compiles. The edit-fan-out red line therefore does not govern.
- **The nearest analogues, measured, not projected.** #1447 (`45806e6`) — the immediately preceding ticket on *this same arm*, adding exactly one run-level value — is 18 files, 400+/240−, of which 529 diff lines are comments (83%). #1446 (`7d5c6cf`) is 18 files, 654+/262−, 650 comment lines (71%). Both `size:s`; neither hit `max_turns` (checked via label history and the timeline, not inferred). #1440 shipped 1066 insertions at `size:s`.
- **This ticket projects between them:** ~450–550 insertions, ~700 total diff, 18–19 files, ~145 lines of non-comment design surface, ~88 shifted cites. It is #1447 plus one record field and its invariant; it is smaller than #1446, which added two `trailRunReadings` fields *and* the `Route` field *and* a value.
- **New files: 0. New exported types: 0. New reject branches: 1** (the step-1 switch stays at seven cases with no default). **ACs: 5.**
- **The seam was checked against the source, and it exists in only one order.** The candidate cut is *(B) the new value + count cascade* ‖ *(A) the `RouteReason` field*. A-first is **forbidden**: shipping the field without the value publishes `evidence_route_reason: sighting-reason-ordering-uncertified` on every run that exists today, promoting the current prose-level falsehood into a published field. B-first is valid — but both children insert above the same cited region of `trail_run_outcome_test.go`, so each pays its own ~88-cite sweep, turning ~700 lines into ~500 + ~350 plus a second full pipeline cycle. That is the same argument #1446's architect made and shipped clean on.

**File-overlap check (§1.5): clean.** `git fetch origin --prune` then all 54 remote `feature/*` branches diffed against `origin/main`: only `origin/feature/363` touches `internal/e2e/realclaude/`, and only `fixtures.go`, which this design does not touch and which carries no cite into `trail_run_outcome_test.go`. No `addBlockedBy` needed.

---

## Open questions

**Q1 — a non-empty ordering value nobody defined.** The guard tests `Ordering.Value == ""` exactly, so `trailOrderResult{Value: "order-nobody-defined"}` keeps today's behaviour: it reaches the fall-through and publishes `sighting-reason-ordering-uncertified`, a reason that asserts a measurement it cannot support. No producer emits it and no fixture in the family builds one, so this is a caller's-bug shape rather than a reading, and the honest home for it is a contract check — which this ticket cannot add without answering `run-out-of-contract` on shapes AC1 forbids it on. Left where it lands, deliberately; the same asymmetry `pinIsVerdict` has on the pin side, where the route's own doc already absorbs it. If a gather ever fills `Ordering` from something other than `trailCertifyOrdering`, this becomes a real ticket.

**Q2 — reachability is unchanged and this ticket does not close it.** No shipped gather can produce `trailGateAbsentOwesNone` today: both fill the gate's runner-path field with `trailRunnerUnread()` (`finding_run_gather_test.go:552`, `:789`; `trail_run_rig_test.go:162`), and with the runner unread the gate's absence branch answers `trailGateOutOfContract` (`trailer_admissibility_test.go:571-597`). Closing that is gated on `finding_exit_path_probe_test.go:264-272`, which forbids adding `tdnClaudeNeedle` to that gather's scan. This ticket is offline and makes the arm correct so the run that will eventually reach it has somewhere to land.

**Q3 — should `RouteReason` ever carry a reap-log-route reason?** Not until `trailAdmitResult` grows a `Reason` field, which is its own ticket and has no observed need. The biconditional in § Design 3 is the executable form of that decision, and it reddens if someone later publishes a reason beside `trailRouteReapLog` without widening the space.

---

## Security review

**Verdict:** PASS (after one MUST FIX and one SHOULD FIX, both addressed inline before commit)

The asset under threat in this family is stated in-code rather than assumed: the published `trailRunOutcome` is designed to be **pasted into a public GitHub issue unreviewed** (`trail_run_outcome_test.go:2024-2027`), and `ps` columns can route an operator's `CLAUDE_CODE_OAUTH_TOKEN` / `ANTHROPIC_API_KEY` into it. Every category below is walked against that.

**Findings:**

- **[Trust boundaries] MUST FIX — addressed.** This ticket adds a field to the published record sourced from an input record, which is a new crossing of the one boundary this family guards. The leak route is not the field's intended source (`sighting.Reason`, a closed five-value space) but the **new arm's Detail**: `trailRunOutcome`'s content rule at `:425-435` *permits* interpolating "verdicts", and `PinnedPid` is the one input the classifier has no contract check over (the no-C10 note at `:704-726` omits it deliberately; C7 validates only `Liveness`). The never-staged arm is reachable with an arbitrary `PinnedPid.Verdict` — the guard keys on `Ordering.Value` alone — so a Detail naming the verdict would make this the first run-level arm to publish an unvalidated string. The permission in the content rule was written when `pinReadState`'s four constants were the only producer. § Design 6 now forbids it explicitly, and § Testing strategy plants the needle **in `PinnedPid.Verdict` itself** so the prohibition has a deterministic net rather than a prose one — verified green on the current build (`go test -overlay`, no worktree writes), so the block passes on a correct implementation and reddens only on the substitution.
- **[Trust boundaries] No further findings.** The field's source is pinned three ways with different fabric: `trailIsSightingReason(got.RouteReason)` on every row (a membership predicate — `sighting.Detail` is not a member, so the natural "better failure message" substitution reddens deterministically), the `RouteReason != "" ⟺ Route == trailRouteSighting` biconditional, and the needle sweep on two arms. The new JSON key `evidence_route_reason` carries none of `command` / `args` / `comm` / `argv`, and the structural key walk runs over the actual marshalled keys, so it sees the field without being widened.
- **[Trust boundaries] SHOULD FIX — addressed.** Both new fields are `omitempty` and both are empty on the never-staged arm, so that block's key walk covers a strict **subset** of the route-bearing key set. That is sound (the superset is walked three times over) but it means those premises assert record shape rather than anchoring the walk — recorded in place so a later edit does not delete block 5's anchoring premises believing block 4 replaces them. Same shape as the hole #1446 was reworked for.
- **[Tokens, secrets, credentials] No findings.** No new path from `ps` output to the record. `pinStateOutcome.ToolStderr` — the field that takes raw `ps` stderr verbatim (`process_pin_liveness_test.go:341`) — is read by neither the arm nor the new guard, which reads `Ordering.Value` only. No token is generated, stored, compared or logged anywhere in this surface.
- **[File operations] Not applicable — by design, and checked.** No path is constructed, no file opened, no mode set, no symlink followed. The only file read anywhere in the affected surface is `TestTrailRunBareAlivenessAtTrailerIsBudgetFiredOnly`'s `os.ReadFile("trail_run_outcome_test.go")` — a fixed literal in the package directory, unchanged here; this spec's only interaction with it is a prohibition on a phrase that would redden it.
- **[Subprocess / external command execution] Not applicable — by design, and checked.** `trailClassifyRun`, `trailEstablishSighting` and the new guard are pure over their inputs: no exec, no clock, no filesystem. The `ps` exec that produced `pinStateOutcome` happens upstream in `pinScanArgv` and is untouched. No `sh -c`, no env inheritance, no signal handling added.
- **[Cryptographic primitives] Not applicable.** No randomness, no hashing, no key material, no comparison against a secret. `math/rand` is not reachable from this surface.
- **[Network & I/O] No findings.** No socket, listener, upgrade path or deadline. The one bound in play is `reachMaxCommandBytes = 512` on the Detail, which truncates **silently**; § Design 6 budgets both new arms against it from rendered measurements (339 B and 320 B of prose available), and `TestTrailClassifyRun:1468-1471`'s marker-absence check enforces it per row, so an over-budget arm fails the build instead of losing its closing argument to an operator.
- **[Error messages, logs, telemetry] No findings.** The published record *is* the telemetry, so this collapses into the trust-boundary finding above. Assertion failure messages print closed-space values and, on the sweep blocks, the planted Detail — the existing idiom (`:2044-2047`), where the needle is a test constant and not a secret.
- **[Concurrency] No findings.** Nothing is spawned; no lock is taken. The one real hazard is the previously-observed one recorded at `:981-982`: `Liveness` is a slice and `go test -race` runs these in parallel, so the two new fixtures must be **functions**, not package-level vars, or one row's mutation reaches another's. Stated in § Concurrency model and in § Testing strategy.
- **[Threat model alignment] No findings.** `docs/protocol-mobile.md` § Security model is relay-scoped and not in play — this ticket touches no wire format, no device identity and no relay path. The CLI-relevant threat it does touch is operator-credential leakage into a published artifact, addressed above. Out of scope and named: the reachability gap at § Open questions Q2 (`finding_exit_path_probe_test.go:264-272` forbids the gather change that would make this arm live), which is not this ticket's to close.

**Reviewer:** architect (self-review per `architect/security-review.md`)
**Date:** 2026-08-10
