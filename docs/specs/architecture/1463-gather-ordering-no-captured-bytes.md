# #1463 — Probe instrument: sweep the gather's certified-ordering route for captured bytes

**Size:** S (one test file, one new test function, one comment re-point, one comment extension)
**Tier:** gather. Offline throughout — no live claude, no credentials, no `make e2e-realclaude`.

---

## Files to read first

All of these are behind the `e2e_realclaude` build tag. `make check` does **not** compile them; see § Verification.

| File | Symbol | What to extract |
|---|---|---|
| `internal/e2e/realclaude/finding_run_gather_test.go` | `TestFinGatherPinnedPidCarriesNoCapturedBytes` | **The model.** Read the whole doc comment and the whole body. This ticket is that test one input later. Every argument shape review will expect is already there: sibling-not-third-plant, the six named channels, the structurally-passing rows as guards rather than vacuities, the no-forbidden-key-walk note, and the control's budget argument. |
| `internal/e2e/realclaude/finding_run_gather_test.go` | `finGatherReadings` | The carriage. The bare `readings.Ordering = in.Ordering` at the tail, next to the three siblings it joins, and the block above it explaining why the ordering is **not** certified inside the gather. |
| `internal/e2e/realclaude/finding_run_gather_test.go` | `finGatherNegativeInputs` | The base fixture. Seeds `trailFixtureTrailer`, `PyryExited: true`, no `RunnerPath`, no `PinnedPid`. This is the arm the control's headroom belongs to — see § The arm. |
| `internal/e2e/realclaude/finding_run_gather_test.go` | `TestFinGatherStagedOrderingReachesTheSightingOutcomes` | Two things: its "What this test does not do" block is the **re-point site** (AC6), and its `base` fixture is the recipe that *does* move the outcome onto the expensive arms — the one this ticket must not copy. |
| `internal/e2e/realclaude/finding_run_gather_test.go` | `TestFinGatherPinnedPidDoesNotReachTheLiveness` | The **re-point precedent**. Its doc's closing paragraph is the resolved form AC6 must imitate: the test's name rather than a ticket number, plus the sentence saying why. |
| `internal/e2e/realclaude/finding_run_gather_test.go` | `TestFinGatherReturnsNoCapturedBytes` | The two count claims in its doc (`two of the three inputs…`, `THE THIRD INPUT IS #1458's PinnedPid`) and the paragraph below them. **Verify-site for the counts, edit-site for the paragraph** — see § The two count claims. |
| `internal/e2e/realclaude/finding_run_gather_test.go` | file header (the block above `import`, § "Three caller-supplied strings cross into the readings VERBATIM") | Read it, change nothing. It enumerates the inputs that have a *legitimate publication to defend*. The ordering is not one, and its counts are the kind that go stale when helpfully extended. |
| `internal/e2e/realclaude/trail_ordering_premises_test.go` | `trailOrderResult`, `trailCertifyOrdering`, `trailOrderCertified` | The type is two strings, so `==` compares the whole value. The producer takes three bools — that is the whole reason this plant can only be a struct literal. |
| `internal/e2e/realclaude/trail_run_outcome_test.go` | `trailClassifyRun` | The two sites that read `readings.Ordering`: the `Ordering.Value == ""` guard and the whole-value hand-off to `trailEstablishSighting`. Both sit under step 1's gate-absent branch. The step-8 arm — the `decide(trailOutcomeNoRowMatched, …)` fall-through — is the mutation site for the RED proof. |
| `internal/e2e/realclaude/trail_run_outcome_test.go` | `TestTrailRunOutcomeCarriesNoCapturedBytes` | Its doc records that `Ordering.Detail` was the one plant of three that reddened at the classifier tier, **on the truncation marker rather than on the needle**. That is this ticket's trap, on this exact field. |
| `internal/e2e/realclaude/trailer_admissibility_test.go` | `trailDetail` | The shipped renderer and the 512-byte cap the control is measured against. |
| `internal/e2e/realclaude/result_trailer_observation_test.go` | `trailNeedle` | 42 bytes. The needle every sweep in this family plants. |
| `internal/e2e/realclaude/background_reach_probe_test.go` | `reachMaxCommandBytes`, `reachTruncationMarker` | The cap (512) and the marker the control must not carry. |

---

## Context

`finGatherReadings` gained `readings.Ordering = in.Ordering` in #1462. That is a new caller-staged input crossing the gather **whole and unnormalised**, and this family's standing rule — set by #1452, restated in `TestFinGatherReturnsNoCapturedBytes`' own doc — is that a new route gets a **sibling sweep** rather than a third plant on the shared one.

The reason it must be a sibling here is the same one that note gives for #1458's pid read: the shared sweep marshals **readings**, and the pass-through onto `trailRunReadings.Ordering` is whole by design, so a plant added there is red against a *correct* build whose only fix narrows the pass-through and leaves the needle no route at all.

**This plant is weaker than its two siblings, and the spec's job is to make the test say so accurately rather than overclaim it.** `trailCertifyOrdering` takes three booleans and builds its `Detail` from source-authored clauses, so no captured byte can reach a `trailOrderResult` through its producer. Downstream, `trailClassifyRun` reads `readings.Ordering` at exactly two sites and **no shipped arm renders `Ordering.Detail` into any published string** — the sighting arms render `sighting.Value` and `sighting.Reason`, which are the predicate's own answers. There is no live leak route to fence.

What this sweep *is*: a discipline against a **future** field on that record and a future arm that interpolates the ordering's `Detail` for a better failure message — the natural mistake, and the same framing #1440 used for its own `trailOrderResult` plant at the classifier tier.

---

## The arm, and the measurements the design is built on

**Every number below was re-measured at `ad12951` on this branch, through the shipped gather and the shipped classifier, via `go test -overlay`.** They reproduce the ticket's table exactly. Do not re-derive them from prose; if a fixture changes, re-measure.

The arm to pin is the one `finGatherNegativeInputs` already reaches: `trailOutcomeNoRowMatched` (step 8).

**Staging a certified ordering on that fixture does not move the arm.** Measured: with `Ordering` set, the outcome is still `run-scan-matched-no-row`. The arm is decided by the **gate**, not by the ordering — the whole sighting route sits behind the gate-absent branch of step 1, so on a usable-gate fixture `Ordering` is never consulted at all. (An earlier draft of the ticket claimed the opposite; the ticket body already corrects it, and the correction is confirmed here.)

Do **not** restructure the fixture onto `TestFinGatherStagedOrderingReachesTheSightingOutcomes`' recipe (a `trailKeyNamesNoTerminalReason()` seed plus a streamrunner `RunnerPath`). That recipe reaches the gate-absent arms, whose budgets are far tighter — the not-staged arm renders 453 bytes, `run-void-path-owes-no-reason` 459, and the classifier's own doc budgets `run-void-pinned-pid-did-not-establish` at 492 of `trailDetail`'s 512.

| quantity | measured at `ad12951` |
|---|---|
| `len(outcome.Detail)`, step-8 arm | **380** |
| `len(readings.Gate.Detail)` / `len(readings.Admit.Detail)` | **229 / 264** — both non-empty, so AC2's premise is satisfiable |
| `len(trailNeedle)` | 42 |
| plant `Detail` = `"ordering: " + trailNeedle` | 52 |
| control, clean tree | **443** — 69 under the 512 cap, no truncation marker, needle present |
| control, under the republishing mutant | **506** — 6 under the cap, still no marker |
| `len(trailCertifyOrdering(true, true, true).Detail)` | **403** |

Three consequences, and none is visible from the AC text:

1. **`len(outcome.Detail)` is not a constant.** That arm renders the live row count — *"the argv scan read 812 well-formed row(s) and none matched…"* — so its width moves with that number's digit count. The probe run for this spec measured 812 rows where the ticket's measured 798, and the byte total was 380 in both. **Do not hard-code 380 anywhere.** Assert headroom against the rendered value.

2. **The plant's `Detail` must stay short — about 54 bytes is the ceiling.** On a clean tree the control pays for the plant once; under the RED-proof mutant it pays **twice**, and at 52 bytes that lands at 506 with 6 to spare. A longer plant makes the RED run add a budget `Fatalf` on top of the needle diagnosis, which is exactly the confusion this family has already been fooled by once.

3. **The real producer's `Detail` is 403 bytes**, so `trailCertifyOrdering(true, true, true)` cannot be the plant even with its `Detail` overwritten in place: 380 + 11 + 403 = 794, three hundred bytes past the cap. This is the measured form of AC5's "the plant has to be a struct literal" — the constraint is arithmetic, not stylistic.

On a correct build the needle reaches none of the six channels, so the sweep is **green as specified** and the RED must come from a mutant.

---

## Design

One new test function, `TestFinGatherOrderingCarriesNoCapturedBytes`, in `internal/e2e/realclaude/finding_run_gather_test.go`, placed **immediately after** `TestFinGatherStagedOrderingReachesTheSightingOutcomes` and before `TestFinGatherSightingComesFromTheClassifiedPoll`. That placement is what makes AC6's re-point say "immediately below" truthfully, mirroring the `TestFinGatherPinnedPidDoesNotReachTheLiveness` → `TestFinGatherPinnedPidCarriesNoCapturedBytes` pair exactly.

**Structure it as `TestFinGatherPinnedPidCarriesNoCapturedBytes`, section for section.** That test is the contract; deviating from its shape is what will cost review cycles.

### The plant

A `trailOrderResult` struct literal built by a **function**, not a package-level value — same reason the pid sibling gives: a value shared across tests is a value a test can mutate for its neighbours.

- `Value: trailOrderCertified` — the honest value for this fixture (the gather sights the trailer, `PyryExited` is true in the base, and `holdHeld` is the caller's own fact), and the same value `TestFinGatherStagedOrderingReachesTheSightingOutcomes` stages. It also keeps the plant clear of the `Ordering.Value == ""` guard, which this arm does not reach but which a later fixture drift might.
- `Detail: "ordering: " + trailNeedle` — 52 bytes, for the arithmetic in § The arm.

`trailOrderResult` has exactly two members, so the plant fills **both** and there is no third-field surface to maximise — unlike the pid sibling, which fills three deliberately. Say so; a reader arriving from that test will look for the maximisation argument and must find its absence explained rather than assume an oversight.

### Fixture

`finGatherNegativeInputs(t, &stdout)`, with `in.Ordering` replaced and **nothing else varied**. `PinnedPid` stays at the base's zero.

### Premises (AC2) — each turns a vacuous sweep into a named failure

Assert, in this order, each with its own `t.Fatalf` naming what a pass would have meant:

- The planted `Detail` carries the needle. Without it every negative below passes over a record that never held captured bytes.
- `readings.Ordering == in.Ordering`. `trailOrderResult` is two strings, so `==` compares the whole value. This is `TestFinGatherStagedOrderingReachesTheSightingOutcomes`' subject arriving as this test's **premise**: against a narrowed pass-through every absence below would be a fact about the gather having dropped the value, not about the classifier not republishing it.
- `readings.Gate.Detail` and `readings.Admit.Detail` are both non-empty. A needle search over an empty string asserts nothing. (Measured 229 / 264.)
- `outcome.Value == trailOutcomeNoRowMatched` — AC4's arm pin. The `Fatalf` must say that the control's byte budget is *that arm's*, so a fixture that drifted invalidates the control rather than the sweep.
- `outcome.Detail` does not already carry `reachTruncationMarker`. If the shipped rendering were already truncated the control could not reproduce the one-shot rendering and its needle check would be measuring the cap.

### The negative (AC1) — six channels, and the one that is deliberately absent

Three marshalled subjects, each named so a failure says which leaked: **run outcome**, **attribution record**, **sighting**. Then the three published `Detail`s an operator actually reads, named individually rather than left to the marshal sweep: **the outcome's**, **the gate's**, **the attribution's**.

The marshalled `trailRunReadings` is **not** a row, and the doc must say so in its own right so the next reader does not "fix" the omission: the needle rides that value by design, and a row there is red against a correct build.

### The control (AC3)

Built through the shipped renderer off the shipped rendering, never off a re-typed copy of an arm's format string:

```go
control := trailDetail("%s ordering: %s", outcome.Detail, readings.Ordering.Detail)
```

Three checks, each with its own message:

- the control carries the needle — otherwise the negative above cannot tell a sealed channel from an unreachable one (`Fatalf`);
- `len(control) < reachMaxCommandBytes` — otherwise the control demonstrates a **budget** kill wearing a leak kill's clothes. The message must say *shorten the plant's Detail; never weaken the sweep*;
- the control does not carry `reachTruncationMarker` — same kill, same fix.

Measured: 443 clean, 506 under the mutant. Both silent.

---

## What the doc comment must say (AC5) — and how honest it has to be

This is the part most likely to be got wrong by writing the pid sibling's doc with the nouns swapped. Three claims, each of which is *weaker* here than there:

1. **The plant is a struct literal at the sweep site alone, and this is a statement about the producer.** `trailCertifyOrdering`'s whole input is three bools, so no captured byte can reach a `trailOrderResult` through it. There is no live producer that emits this shape; the literal is a fixture and must be labelled one, exactly as `finGatherPinnedReadings`' doc labels its own.

2. **All six channels pass structurally today, and none is a live leak.** This is a stronger statement of absence than the pid sibling's "four of six" and it must not be dressed up as the weaker one. On this arm the classifier never consults `Ordering` at all. The rows are still guards and not vacuities — they are the fabric a future arm folding the value in would break — and the pair where such an arm would show is **the marshalled outcome and its `Detail`**, which is where the RED-proof mutant lands.

3. **It does not stand in for #1458's pid-read plant.** That one is the live route, swept by `TestFinGatherPinnedPidCarriesNoCapturedBytes`. A reader who takes this test as coverage of a live channel has learned the wrong thing.

Also carry, in the pid sibling's shape:

- **Why a sibling and not a third plant** on `TestFinGatherReturnsNoCapturedBytes` — the harder reason (that sweep marshals readings; a plant there is red against a correct build) ahead of the precedent reason.
- **No forbidden-key walk, and why**: this route publishes no key at any of the six subjects, and the key fabric over these exact values is already held by `TestFinGatherReturnsNoCapturedBytes` for the record and the sighting and by `TestTrailRunOutcomeCarriesNoCapturedBytes` for the outcome.
- **The budget warning (AC4)**, in the form its sibling closes with: the byte budget belongs to the arm the premise pins and to no other; the arms that consult `trailEstablishSighting` render far more of the 512 by their own documented budgets, so a later fixture change that moved the outcome must re-measure rather than inherit this headroom. Name the precedent that makes it non-hypothetical for *this field*: `TestTrailRunOutcomeCarriesNoCapturedBytes`' own doc records that `Ordering.Detail` was the one plant of three that reddened at the classifier tier, and it reddened on the truncation marker rather than on the needle.

---

## The two count claims — verify, do not increment

`TestFinGatherReturnsNoCapturedBytes`' doc says *"two of the three inputs that could carry captured bytes into the returns"* and *"THE THIRD INPUT IS #1458's `PinnedPid`"*. Both were re-measured at `ad12951`: #1462 shifted them but left the prose intact and correct.

They are counts over inputs that **can** carry captured bytes. The ordering is not one — by construction of its producer — so **the counts survive and must not be incremented.** An ordering counted there would assert a live channel that does not exist. Record the reason where you verify it, in the new test's own doc.

**One edit is owed at that site, and it is small.** The paragraph below those counts (*"A `PinnedPid` PLANT ADDED HERE WOULD BE RED AGAINST A CORRECT BUILD…"*) makes an argument that now covers a second whole-carried value. Extend it by naming the ordering as that second value, with the distinction spelled out: not because a producer can put captured bytes there, but because a **hand-planted** one would ride it by design too, and the fix for such a red is the same narrowing that leaves the needle no route. Two sentences at most. That paragraph is where a later reader stands when they reach for the third plant, which is why the new test's own doc is not sufficient on its own.

**Do not touch the file header.** Its "Three caller-supplied strings cross into the readings VERBATIM, by design" list enumerates inputs with a *legitimate publication to defend* — a needle there is red against shipped code. The ordering has no such publication and no producer route, so it does not belong in that list, and its counts are precisely the kind that go stale when helpfully extended.

---

## The re-point (AC6)

Exactly **one** shipped comment names this ticket: inside `TestFinGatherStagedOrderingReachesTheSightingOutcomes`' "What this test does not do" block — *"The gather tier's sweep over the route is #1463's, on #1452's precedent…"*.

Replace the ticket number with the new test's **name**, and follow the resolved form `TestFinGatherPinnedPidDoesNotReachTheLiveness`' doc uses for its own pointer, including the sentence saying why: *a reader of this doc is one declaration from the test they want.* With the placement above, "immediately below" is literally true here too.

(An earlier draft of the ticket predicted six such comments by analogy with #1459. That was a forecast; the measurement is one. Verify with a `#1463` grep over `internal/e2e/realclaude/` before editing — if the count has changed since `ad12951`, re-point each and say so.)

---

## Citation discipline

`make cite-guard` fails on a comment citation whose target is a declaration or sits within 20 lines of one. **Name symbols, not line numbers**, in everything this ticket writes — the new doc comment, the extended paragraph, and the re-point. Where an existing nearby comment carries a line citation, leave it alone; this ticket is not a cite sweep.

---

## Testing strategy

The test *is* the deliverable, so "testing strategy" here means proving the instrument discriminates.

**Green, on the clean tree:**

```
go vet -tags e2e_realclaude ./internal/e2e/realclaude/
go test -tags e2e_realclaude -run TestFinGatherOrderingCarriesNoCapturedBytes ./internal/e2e/realclaude/
```

**RED, via a mutant — required, not optional.** Do not trust the sweep on inspection. Build the mutant with `go test -overlay` (a JSON overlay mapping `internal/e2e/realclaude/trail_run_outcome_test.go` to a modified copy outside the worktree, so nothing is written into the branch):

- Mutate `trailClassifyRun`'s step-8 fall-through — the `decide(trailOutcomeNoRowMatched, …)` call — to append the ordering's `Detail` to its rendered sentence, i.e. what a future arm folding the value in "for a better failure message" would look like.
- Expected under that mutant: the **marshalled run outcome** row and the **outcome's `Detail`** row go red on the needle. The other four stay green.
- Expected of the control under that mutant: **no additional `Fatalf`.** Measured, the control renders 506 against the 512 cap with no marker. If the control also fires, the plant's `Detail` grew past ~54 bytes — shorten the plant, never weaken the sweep.

Re-run the whole tagged package once at the end (`go test -tags e2e_realclaude ./internal/e2e/realclaude/`) to confirm the re-point and the extended paragraph broke no sibling.

---

## Verification

These files are behind the `e2e_realclaude` build tag, which **`make check` does not compile.** A green `make check` is not evidence this ticket built. Use the tagged `go vet` and `go test` invocations above.

No live claude and no credentials are needed for any check in this spec — every measurement here was taken that way.

---

## Error handling

Not applicable in the usual sense: `finGatherReadings`, `trailCertifyOrdering` and `trailClassifyRun` are all pure over their inputs and take no `*testing.T`, by their own stated contract — an instrument failure is a datum to publish, not a reason to abort a turn. The only failure surface this ticket adds is test assertions, and every one of them is specified above with the message it must carry.

## Concurrency model

None. The test drives the shipped gather synchronously on one goroutine, over one `probeSyncBuffer` it seeds itself. It adds no `t.Parallel()` call — and must not compare `RowsScanned` across gather calls, for the reason `TestFinGatherPinnedPidDoesNotReachTheLiveness`' doc records: this package makes many `t.Parallel()` calls and re-execs itself, so a sibling's child appearing between two calls moves the count.

---

## Open questions

None blocking. One judgement call the ticket explicitly delegated is resolved above: the paragraph below `TestFinGatherReturnsNoCapturedBytes`' count claims **does** get the two-sentence extension, while the counts themselves and the file header are verify-only.

---

## Scope check

Production source files (`*.go` excluding `*_test.go`) prescribed new or modified content: **0**. Files touched: **1** (`internal/e2e/realclaude/finding_run_gather_test.go`). New exported types: 0. Consumer call sites needing simultaneous update: 0. Projected total written: ~200 lines, of which most is the doc comment.

Nearest analogue: #1462 (`bd09812`) wrote 309 net lines into this same file plus 82 into `trail_run_outcome_test.go` in one developer run. This ticket is smaller — one test function in the shape of an existing 190-line sibling, plus two comment edits.

---

## Security review

**Verdict:** PASS

This ticket ships no production code. Its subject is an instrument that asserts a *negative* about what reaches a published artifact, so the categories are walked against two questions: does the design leave a leak unfenced, and can the instrument itself leak?

**Findings:**

- **[Trust boundaries]** No findings. The boundary this ticket is about is explicit and single: `finGatherReadings` is the one function where caller-staged readings cross into the values a published probe artifact is built from, and `trailClassifyRun` is the one function that renders them into an operator-visible `Detail`. The design fences the ordering's crossing at both ends — six named channels at the gather tier, with the classifier tier already held by `TestTrailRunOutcomeCarriesNoCapturedBytes`. The value is *untrusted by type* (`trailOrderResult.Detail` is a bare `string` nothing validates), and the spec makes that explicit rather than assumed: the plant is possible precisely because the type permits it, and the producer's three-bool input is what makes it unreachable in practice.

- **[Error messages, logs, telemetry]** The one category with real exposure here, and the reason the ticket is labelled at all. Two findings, both addressed in the design:
  - The `t.Fatalf`/`t.Errorf` messages this test adds print `outcome.Detail`, `readings.Gate.Detail`, `readings.Admit.Detail` and whole marshalled subjects. That is licensed by the file header's own failure-message rule — those are values `TestFinGatherReturnsNoCapturedBytes` proves clean — and the new messages introduce no field the header forbids. They must **never** name `pinScan.Matches`, a function-local observation's `Line`, or a `trailScanResult`'s trailer; none of the specified messages does.
  - The needle-bearing plant is printed on failure by design. That is safe and intended: `trailNeedle` is a source-authored sentinel, not captured bytes.

- **[Network & I/O]** No findings — MUST NOT REGRESS, and the design holds it. The 512-byte cap (`reachMaxCommandBytes`) is the input-size limit that matters at this tier, and this ticket's main trap is a control that *silently* exceeds it: `trailDetail` truncates without erroring, so an over-budget control would redden on the truncation marker and be misread as a leak kill. The design pins the plant at 52 bytes with measured headroom on both the clean tree (443/512) and the mutant (506/512), and specifies an explicit `reachTruncationMarker` check on top of the length check so a future budget change fails loudly rather than quietly.

- **[Subprocess / external command execution]** No findings. The route under test executes nothing: `trailCertifyOrdering` is pure over three bools, and the plant is a struct literal. `finGatherReadings` does reach `ps` via `pinScanArgv`/`pinReadState`, but this test neither adds nor changes any exec, and its needle set is `finGatherNeedles`' `t.TempDir()` path that nothing is staged at, so the argv leg matches nothing deterministically.

- **[Concurrency]** No findings. Single goroutine, no `t.Parallel()`, no shared mutable fixture — the plant is built by a function rather than a package-level value precisely so no sibling can mutate it. The spec forbids cross-call `RowsScanned` comparison, which is the one shared-state trap this package actually has.

- **[Tokens, secrets, credentials] / [File operations] / [Cryptographic primitives]** Not applicable, and by design rather than by omission: this ticket adds no credential handling, opens no file (the `t.TempDir()` path is never staged at), and performs no randomness or comparison of a secret. The whole run is offline — no credentials are needed for any check in this spec.

- **[Threat model alignment]** The threat this instrument serves is the operator-review-before-paste obligation: probe artifacts are pasted into **public** GitHub issues, so any captured byte reaching a published `Detail` is a disclosure. This ticket fences one newly-opened crossing at that boundary. **Named as out of scope, honestly:** there is no live leak route here today — `trailCertifyOrdering` cannot produce a needle-bearing `trailOrderResult`, so the design is a discipline against a future field and a future republishing arm, not a fix for an observed exposure. The spec requires the test to say exactly that (§ What the doc comment must say, claim 2), so the coverage is not mistaken for more than it is. The live route at this tier remains #1458's pid read, swept by `TestFinGatherPinnedPidCarriesNoCapturedBytes`.

**Reviewer:** architect (self-review per `architect/security-review.md`)
**Date:** 2026-08-14
