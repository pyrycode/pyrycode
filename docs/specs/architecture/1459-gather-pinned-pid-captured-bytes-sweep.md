# #1459 — Sweep the gather's pinned-pid route for captured bytes

**Size:** S (confirmed; one new sibling test plus six comment sites, all inside `internal/e2e/realclaude`, no production file)
**Measured against:** `a9d3ed5` (the merge of #1458)

## Files to read first

Everything below is behind the `e2e_realclaude` build tag. Resolve every symbol with
`codegraph_search` / `codegraph_node`; no line numbers are given because none survive the
insertion this ticket makes.

| Where | Symbol | What to extract |
|---|---|---|
| `internal/e2e/realclaude/finding_run_gather_test.go` | `TestFinGatherRunnerPathCarriesNoCapturedBytes` | **The shape to follow.** Premises first, per-channel naming, published Details named individually, the marshal sweep over the returns. |
| same file | `finGatherReadings`, `finGatherInputs` | The route: `readings.PinnedPid = in.PinnedPid`, whole and unnormalised. `PinnedPid`'s field doc is comment site 3. |
| same file | `TestFinGatherPinnedPidDoesNotReachTheLiveness`, `finGatherPinnedReadings`, `finGatherPinnedReadPID` | The sibling this one sits beside; its doc is site 5, `finGatherPinnedReadings`' is site 4. Note the "FIXTURES AND NOT LIVE `pinReadState` CALLS" paragraph — the new plant needs the same disclaimer. |
| same file | `finGatherNegativeInputs` | The fixture the new test builds on: usable gate certifying `completed`, an attribution that is not proof, `PyryExited` true. |
| same file | `TestFinGatherReturnsNoCapturedBytes` | Site 1, the doc that currently says no sweep covers `PinnedPid`. Also the test that marshals `readings` — the reason a third plant here is red against correct code. |
| same file | `TestFinGatherPyryExitIsObservableAtTheOutcome` | Proof that `finGatherNegativeInputs` with `PyryExited` true classifies to `trailOutcomeNoRowMatched`. That is the arm the control's byte budget is measured on. |
| `internal/e2e/realclaude/trail_run_outcome_test.go` | `trailClassifyRun` | The consumer. Read the step-8 arm (`trailOutcomeNoRowMatched`), the `decide` closure, the provenance struct literal at the top, and the no-C10 note. Confirm for yourself that no arm reads `readings.PinnedPid` except through `trailEstablishSighting`. |
| same file | `TestTrailRunOutcomeCarriesNoCapturedBytes` | Site 2. Also the classifier-tier sweep whose `plantedPin` closure the new plant mirrors, and whose doc records the budget-kill-wearing-a-leak-kill's-clothes measurement. |
| same file | `trailRunOutcome`, `trailRunReadings` | Field sets and json tags: `pinStateOutcome` marshals `detail`, `state_column`, `tool_stderr`; `trailRunOutcome` publishes no pin field at all. |
| `internal/e2e/realclaude/trailer_admissibility_test.go` | `trailDetail` | `reachCapCommand(fmt.Sprintf(...))` — the single rendering path the control must travel. |
| `internal/e2e/realclaude/background_reach_probe_test.go` | `reachCapCommand`, `reachMaxCommandBytes`, `reachTruncationMarker` | The 512-byte cap and the marker the control must be shown *not* to carry. |
| `internal/e2e/realclaude/process_pin_liveness_test.go` | `pinStateOutcome`, `pinClassifyState` | The instrument-failed branch: `ToolStderr = reachCapCommand(stderr)` folded into `Detail`. The shape the plant stands in for. |
| `internal/e2e/realclaude/result_trailer_observation_test.go` | `trailNeedle` | The needle to reuse — 42 bytes, which the control's budget has to pay for. |
| `internal/e2e/realclaude/finding_exit_path_probe_test.go` | `finExitRunProbe` | Site 6, the live caller's `PinnedPid:` comment. |
| `docs/knowledge/codebase/1458.md` | § "From five rounds of code review" | The cite-tail method. Read it before touching any comment. |
| `cmd/cite-guard/main.go` | package doc | What the guard flags (explicit `file.go:NNN` into a declaration) and what it deliberately does not (bare `:NNN`, ranges). |

## Context

#1458 opened a route: the live probe reads a pinned pid with `pinReadState` after pyry exits and
hands the result to `finGatherReadings`, which carries it whole onto `trailRunReadings.PinnedPid`.
`pinClassifyState`'s instrument-failed branch puts raw `ps` stderr into `ToolStderr` and folds it
into `Detail`, so an operator's `CLAUDE_CODE_OAUTH_TOKEN` can enter that value on a live run. The
artifact this feeds is destined for a public issue.

The route is a **future-edit exposure and not a live leak**, and the ticket measured that rather
than asserting it: `trailClassifyRun` renders `PinnedPid` through nothing, and the one arm that
consults the pin reads `sighting.Value` and `sighting.Reason` only. This ticket builds the fence
before the natural edit — an arm interpolating `pin.Detail` for a better failure message — arrives.

Two facts decide the design, both re-verified here against `a9d3ed5`:

1. **`trailRunReadings` carries the needle by design.** `readings.PinnedPid = in.PinnedPid` is a
   whole-value pass-through, and `TestFinGatherPinnedPidDoesNotReachTheLiveness` asserts that
   equality on purpose. So the sweep is a **sibling test**, not a third plant on
   `TestFinGatherReturnsNoCapturedBytes` — that test marshals `readings`, and a `PinnedPid` plant
   added to it would be red against a correct build.
2. **`finGatherNegativeInputs` with `PyryExited` true classifies to `trailOutcomeNoRowMatched`**
   (step 8), which is the only arm with room for the control. Measured: the shipped step-8 `Detail`
   renders **379 bytes** at a two-digit `RowsScanned` against `reachMaxCommandBytes` = 512, leaving
   ~132 bytes of headroom. The neighbouring arms that a fixture could reach spend 461–492 of the
   512 by their own documented budgets, so a control built on one of those overruns the cap and
   reproduces exactly the budget-kill-wearing-a-leak-kill's-clothes mistake AC3 forbids.

## Design

One new test, `TestFinGatherPinnedPidCarriesNoCapturedBytes`, in `finding_run_gather_test.go`,
placed immediately after `TestFinGatherPinnedPidDoesNotReachTheLiveness` — inside the
`--- the pinned-pid reading ---` section, before `TestFinGatherHalfStagedRouteMovesNoOutcome`.
Placement is free here; see § Citation discipline for the measurement that says so.

### The plant

A local closure returning a `pinStateOutcome`, mirroring `TestTrailRunOutcomeCarriesNoCapturedBytes`'
own `plantedPin` (a function and not a value, for `trailRunWellFormed`'s stated reason). Not a
fourth row on `finGatherPinnedReadings()` — that table is the sibling's subject and a needle-bearing
row there puts a needle through a test that does not sweep for one. Not `trailSightingPin`, whose
own doc says it sets no `StateColumn` and no `ToolStderr`.

- Base shape: `pinClassifyState`'s instrument-failed branch — `Verdict: pinStateInstrumentFailed`,
  `PID: finGatherPinnedReadPID`, `ExitStatus: 1`.
- `trailNeedle` in **all three** string-bearing members: `Detail`, `StateColumn`, `ToolStderr`. The
  rule being enforced is "no Detail here quotes any input's captured string", not "do not copy the
  one field the ticket named".
- The doc must say that filling all three at once is a shape **no single `pinClassifyState` arm
  emits** — the failing arm leaves `StateColumn` empty — and that this is deliberate surface
  maximisation, in the same register as `finGatherPinnedReadings`' "FIXTURES AND NOT LIVE
  `pinReadState` CALLS" paragraph. A later reader must not read the fixture as a producer claim.
- **Keep the member prose short.** `trailNeedle` is 42 bytes and `Detail` is the member the control
  budget pays for; see § The control.

### Inputs

`finGatherNegativeInputs(t, &stdout)` with `PinnedPid` set to the plant, and nothing else varied.
That fixture is what the sibling beside this test already drives, so the two differ in the
assertion and not in the record.

### Premises, stated before any negative

Each is a `Fatalf`, because each turns a silently vacuous sweep into a named failure.

- **The plant carries the needle in each of the three members.** Without it every negative below
  passes over a record that never held captured bytes.
- **`readings.PinnedPid == plant`.** Whole-value equality; `pinStateOutcome` is four strings and
  three ints, so `==` compares all of it. This is the sibling's *subject* and this test's
  *premise*: a pass-through narrowed to `Verdict` leaves the needle no route to travel, and every
  absence below would then be a fact about the gather having dropped the value rather than about
  the classifier not republishing it.
- **`trailClassifyRun(readings).Value == trailOutcomeNoRowMatched`.** The control's byte budget is
  this arm's, so a fixture that drifted to another arm invalidates the control rather than the
  sweep.
- **`outcome.Detail` carries no `reachTruncationMarker`.** The shipped rendering is untruncated, so
  composing the control off it reproduces the one-shot rendering byte-for-byte.

### The negative — six channels, each named individually

A failure must say which channel leaked, so the marshal sweep does not stand in for the named
Details (the ticket's own instruction, and `TestFinGatherRunnerPathCarriesNoCapturedBytes`' shape):

1. the marshalled `trailRunOutcome` that `trailClassifyRun` builds from these readings
2. `outcome.Detail`
3. `readings.Gate.Detail`
4. `readings.Admit.Detail`
5. the marshalled `finAttributeRecord`
6. the marshalled `finSighting`

Rows 3–6 are expected to pass **structurally** — `trailGate` and `trailAdmitAttribution` never see
the pin, and the gather touches neither the record nor the sighting with `in.PinnedPid`. They are
live guards against a future gather or a future arm folding the pin in, which is the same standing
`TestFinGatherReturnsNoCapturedBytes` gives its own sighting row. Say so, so a later reader does not
delete them as vacuous.

**No forbidden-key walk.** This route publishes no key at any of the six subjects — `PinnedPid`
reaches none of them — and the key fabric over these exact values is already held by
`TestFinGatherReturnsNoCapturedBytes` (record, sighting) and `TestTrailRunOutcomeCarriesNoCapturedBytes`
(outcome). One sentence in the doc; do not re-walk.

**Deliberately not asserted: the marshalled `trailRunReadings`** (AC2). Those bytes ride that value
by design and an absence assertion there is red against correct code. One sentence in the test's
doc, in its own right so the next reader does not "fix" the omission.

### The control (AC3)

The counterfactual is "this arm appended the pin's `Detail` for a better failure message". Build it
through the **shipped** `trailDetail` and off the **shipped** rendered `outcome.Detail`, never from
a re-typed copy of the arm's format string:

```go
// contract sketch, not the implementation
control := trailDetail("%s pinned read: %s", outcome.Detail, readings.PinnedPid.Detail)
```

Three assertions, which together are the discrimination AC3 asks to be demonstrated rather than
claimed:

- `strings.Contains(control, trailNeedle)` — the needle survives a republishing rendering.
- `len(control) < reachMaxCommandBytes` — it fits, so a real republishing edit would have leaked
  rather than truncated.
- `!strings.Contains(control, reachTruncationMarker)` — no silent truncation happened.

The failure message on the last two must name the trap in words: an overrun here is a **budget
kill, not a leak kill**, and the fix is to shorten the plant's `Detail`, never to weaken the sweep.
Negative row 2 (`outcome.Detail` carries no needle) is the discriminating half: same rendering path,
same arm, same budget — the only difference is whether the classifier republished its input.

Budget arithmetic to respect (measured, `a9d3ed5`): 512 − 380 (step-8 `Detail`, three-digit
`RowsScanned`) = ~132 bytes for the scaffolding plus the planted `Detail`. A 15-byte scaffolding and
a `Detail` of `"ps: " + trailNeedle`-scale prose (~46–75 bytes) lands at 441–470 with slack. Prose
much beyond that overruns, and the assertions above are what make the overrun say so.

## The six comment sites (AC4)

`grep -rn "1459" internal/` on `a9d3ed5` finds exactly these six. Anchor replacements on the
**claims**, and name symbols — no new `file.go:NNN`.

| # | Site | Claim today | Required after |
|---|---|---|---|
| 1 | `TestFinGatherReturnsNoCapturedBytes`' doc | "THE THIRD INPUT IS #1458's `PinnedPid`, AND NO SWEEP COVERS IT YET … #1459 owes that sweep and re-points this note at it when it lands." | **Goes false — must change.** The third input is swept, by name, at `TestFinGatherPinnedPidCarriesNoCapturedBytes`. Keep the no-third-plant-here decision and **strengthen its reason**: a `PinnedPid` plant on *this* test is red against a correct build, because this test marshals `readings` and the pin rides that value by design. That is a harder reason than the #1452 precedent the note currently gives, and this is the test where a future reader would add the plant. |
| 2 | `TestTrailRunOutcomeCarriesNoCapturedBytes`' doc | "THE ROUTE IS OPEN AND IS NOT YET SWEPT AT THE GATHER TIER … until it lands, this block is the whole of the coverage … #1459 re-points this paragraph once its own sweep exists." | **Goes false — must change.** The route is swept at the gather tier by the named sibling; this block is now the classifier-tier half of a two-tier fabric rather than the whole of it. **Leave the preceding paragraph alone** — "NO LIVE LEAK ROUTE REACHES A PUBLISHED RECORD THROUGH THE NEW INPUTS" stays true, and the ticket's own notes record an earlier body having misidentified it. **Write this rewrite line-count-neutral**; see § Citation discipline. |
| 3 | `finGatherInputs.PinnedPid`'s field doc | "would … make the gather-tier sweep #1459 owes unbuildable" | Correct the tense and name the shipped symbol: narrowing would make `TestFinGatherPinnedPidCarriesNoCapturedBytes` unbuildable. The substance is unchanged and is now checkable — that test's whole-value premise is what goes red. |
| 4 | `finGatherPinnedReadings`' doc | "are exactly the route the gather-tier sweep #1459 owes has to travel" | Same correction: the sweep exists; name it. |
| 5 | `TestFinGatherPinnedPidDoesNotReachTheLiveness`' doc | "NO NEEDLE PLANT AND NO CAPTURED-BYTES SWEEP HERE. The gather tier's sweep over this route is #1459's …" | Recommended correction: name the sibling that now holds it, "beside this one". The #1452-precedent sentence stays. Leaving it is defensible on the letter — the sweep *is* #1459's — but a reader of this doc is one line from the test they want, and a ticket number does not give it to them. |
| 6 | `finExitRunProbe`'s `PinnedPid:` comment (`finding_exit_path_probe_test.go`) | "narrowing it to a verdict string would leave #1459's gather-tier sweep no route to build" | Same correction as 3 and 4 — all three make one claim about narrowing, and leaving one in the future tense makes a reader wonder which is right. |

## Citation discipline and placement

`make cite-guard` is **green on `a9d3ed5`** (verified). It flags explicit `file.go:NNN` cites that
resolve into a declaration; it deliberately does not flag bare `:NNN` or ranges. So the guard cannot
catch what this diff can break, and the measurement below is the fabric.

Measured on `a9d3ed5` by scanning every `:NNN` occurrence in the package's Go comments — all three
forms (`file.go:NNN`, bare `(:NNN)` inheriting the last-named **file or symbol**, and
`<Symbol>:NNN`) — for endpoints below each edit point:

- **`finding_run_gather_test.go`: the tail below the edits is empty.** Nothing in `internal/` cites
  this file at any line at or below the site-1 doc (~the `TestFinGatherReturnsNoCapturedBytes`
  header) in any of the three forms. The only inbound explicit cite is from
  `finding_stage_held_group_test.go` at `:116-124`, far above. The two bare cites *inside* this file
  that carry high numbers resolve to `trail_run_outcome_test.go`, not to itself. **So the insertion
  point is free and the site-1 rewrite may change line count.**
- **`trail_run_outcome_test.go`: four endpoints sit below the site-2 doc.**
  `trail_run_outcome_test.go:2308-2309` and `:2348-2351` (cited from `finding_staging_gate_test.go`),
  a bare `(:2468)` (from `finding_trailer_evidence_test.go`) and a bare `(:2531)` (from
  `finding_attribution_fanout_test.go`). **Therefore: write the site-2 rewrite line-count-neutral.**
  Verify with `git diff -U0` and check **per hunk**, not per file — equal totals across a file still
  displace everything between a `+2` hunk and a `-2` one.
- **`(:2468)` is already stale on `main`.** Its prose names `trailRunOutcomeValues`, whose
  declaration is at 2532; 2468 lands inside a `t.Fatalf` in an unrelated block. **Do not re-point
  it, do not repair it.** It is a member of the pre-existing-stale family `docs/knowledge/codebase/1458.md`
  surfaced, and re-pointing a cite that was already wrong launders it into a verified-looking one —
  the exact move that cost #1458 a rework round. If Δ ends up non-zero despite the instruction
  above, shift the three accurate endpoints by Δ and leave this one where it is, saying why in the
  PR body.
- `finding_exit_path_probe_test.go` (site 6): the only inbound code cite is `:86-87`, above the edit.
  Free.

For the new test's own comments: name symbols, never `file.go:NNN`. `codegraph` indexes this package
including the build-tagged files, so every symbol named here resolves on demand.

## Concurrency model

None new. `finGatherReadings` runs `trailWaitForTrailer` against an in-process buffer and `ps`
through `pinScanArgv` / `pinReadState`; the new test adds no goroutine, no `t.Parallel()` (matching
every top-level test in this file), and no clock reading. `RowsScanned` is live and varies between
calls, which is why it is only ever read *within* one gather here and never compared across two —
the sibling's own note states that measurement.

## Error handling

No new failure modes. The test asserts, it does not recover: premises are `Fatalf` (a broken premise
makes every later check meaningless), the six negatives and the control are `Errorf`/`Fatalf` per
the shape of `TestFinGatherRunnerPathCarriesNoCapturedBytes`. `finGatherReadings` returns no error
and `trailClassifyRun` cannot fail — both are pure over their inputs by their own contracts, which
is what makes the whole test offline.

## Testing strategy

`make check` does **not** compile these files (build tag `e2e_realclaude`). Verify with:

```bash
go vet -tags e2e_realclaude ./internal/e2e/realclaude/
go test -race -tags e2e_realclaude -run '^TestFinGatherPinnedPidCarriesNoCapturedBytes$' ./internal/e2e/realclaude/
go test -race -tags e2e_realclaude -run '^(TestFinGatherReturnsNoCapturedBytes|TestFinGatherPinnedPidDoesNotReachTheLiveness|TestFinGatherRunnerPathCarriesNoCapturedBytes|TestTrailRunOutcomeCarriesNoCapturedBytes)$' ./internal/e2e/realclaude/
make check && make cite-guard
```

No live claude, no credentials, no `make e2e-realclaude`.

**Prove the sweep reddens.** The in-test control discharges AC3; a mutation run is the harder
evidence that the six negatives are not vacuous, and it costs one run with **no worktree writes**:

```bash
# overlay a copy of trail_run_outcome_test.go whose step-8 decide() interpolates
# readings.PinnedPid.Detail, then run only the new test.
go test -overlay=/abs/path/overlay.json -tags e2e_realclaude \
  -run '^TestFinGatherPinnedPidCarriesNoCapturedBytes$' ./internal/e2e/realclaude/
```

Expect: the marshalled-outcome row and the `outcome.Detail` row go red **on the needle**, the
control's three assertions stay green, and no premise fires. If instead the control reddens, the
plant is too long — shorten it; that is the budget kill AC3 rules out. Record the result in the PR
body.

## Open questions

- **Whether site 5 is corrected or left** is the developer's call, and both readings are defensible
  (§ The six comment sites gives the recommendation and the reason). Whatever is chosen, say why in
  the comment or the PR body — AC4 requires the choice to be defensible, not a particular choice.
- **#1457 will stage `Ordering`**, at which point a gather can reach the arms that consult
  `trailEstablishSighting`, and those arms render 461–492 of the 512-byte budget. The control built
  here is measured on step 8 and does not transfer. That is a note for #1457's architect, not work
  for this ticket; the new test's doc should say the budget figure belongs to the arm the premise
  pins, so a later fixture change cannot silently inherit it.

## Security review

**Verdict:** PASS

**Findings:**

- **[Trust boundaries] No findings, and this ticket *is* one.** The untrusted→trusted crossing is
  `ps` stderr → `pinClassifyState` → `pinStateOutcome.ToolStderr`/`Detail` → the caller →
  `finGatherInputs.PinnedPid` → `trailRunReadings.PinnedPid`. The boundary is explicit and single at
  the gather (`finGatherReadings`' whole-value pass-through), and the *publication* boundary — the
  one this ticket fences — is `trailClassifyRun`, which renders the pin through nothing. This spec
  adds a check and no field, so it widens no boundary. The residual is stated where the value lives:
  the field doc says the gather validates nothing and that the shape is the caller's obligation.
- **[Tokens, secrets, credentials] No findings; this is the category the ticket exists for.** The
  credential at risk is a real one — `CLAUDE_CODE_OAUTH_TOKEN` / `ANTHROPIC_API_KEY` in `ps` output
  or its stderr. The design plants a needle in all three string-bearing members rather than the one
  the ticket named, which is what keeps the check honest if a future arm quotes `StateColumn`
  instead of `Detail`. The sweep asserts absence at six named channels including the two marshalled
  records that reach `run.md`. No token is generated, stored or compared here.
- **[File operations] Not applicable, by a design decision rather than by luck.** The test writes no
  file: the fixture is an in-process `probeSyncBuffer` and the mutation proof runs through
  `go test -overlay`, which mutates nothing in the worktree. The artifact-writing tier
  (`finRecordBuild` and below) is not touched.
- **[Subprocess / external command execution] No findings.** The new test execs nothing directly;
  `finGatherReadings` reaches `ps` through the shipped `pinScanArgv` / `pinReadState`, unchanged, and
  the needle set is a `t.TempDir()` path nothing is staged at, so the argv leg matches nothing
  deterministically. No `sh -c`, no user-controlled argv, no new environment inheritance.
- **[Cryptographic primitives] Not applicable.** No randomness, no keys, no comparison against a
  secret. `trailNeedle` is a source-authored constant and its comparisons are plain `strings.Contains`
  — correct here, because the needle is a marker rather than a secret and timing carries nothing.
- **[Network & I/O] Not applicable.** No socket, no HTTP, no relay. The one size limit in play is
  `reachMaxCommandBytes` = 512 via `reachCapCommand`, and this spec's control asserts the rendering
  stays *under* it rather than changing it.
- **[Error messages, logs, telemetry] MUST-NOT-LEAK, and the spec is deliberate about it — SHOULD
  FIX for the developer to hold.** The failure messages this test prints must name the *channel* and
  may print the marshalled subject (which, on a red run, is the leak itself — that is the point and
  matches the siblings). What they must **not** do is `%+v` the plant into a message on a *passing*
  path, and no premise message may print `readings` whole: `readings.PinnedPid` carries the needle by
  design, so a `%+v` there would put captured bytes into every green run's verbose output. The
  sibling's premise messages print the two values being compared (`readings.PinnedPid` against the
  plant) only inside a failure, which is the correct standing. Code-review should check this
  explicitly — it is the one place where following the analogue's shape mechanically could go wrong,
  because the analogue's planted value is a runner-path label and this one is a credential stand-in.
- **[Concurrency] No findings.** No goroutine, no lock, no `t.Parallel()`. The one shared value is the
  `probeSyncBuffer`, read via `Bytes()` which returns a copy; `RowsScanned` non-determinism is handled
  by never comparing it across gathers.
- **[Threat model alignment] In scope and addressed.** The threat is "an operator's credential reaches
  an artifact destined for a public issue"; the fence is a checkable absence at the six published
  channels plus a control proving the check is not vacuous. **Named out of scope:** the arms that
  consult `trailEstablishSighting` cannot be reached from a shipped gather until #1457 stages
  `Ordering`, so this ticket sweeps the step-8 arm and #1457's architect inherits the budget question
  (§ Open questions). Also out of scope and unchanged: the `json.MarshalIndent` fence-escape at the
  artifact tier, which this diff neither creates nor widens.

**Reviewer:** architect (self-review per `architect/security-review.md`)
**Date:** 2026-08-14
