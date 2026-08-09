# #1415 — Pin the ptyrunner trailer → attribution → run composition over fixtures

**Ticket:** [#1415](https://github.com/pyrycode/pyrycode/issues/1415) · **Size:** S · **Split from** #1368 ← #1351 ← #1237

Add one offline test that drives `trailGate` → `trailAdmitAttribution` → `trailClassifyRun` from fixtures under a runner-path reading that reduces to `ptyrunner`, asserts the run-level outcome, and pins the three published `Detail`s. No production change, no shipped test change, no fixture edit.

---

## Files to read first

| Path | What to extract |
|---|---|
| `internal/e2e/realclaude/trail_run_outcome_test.go:1065-1131` | `TestTrailRunComposesWithGateCases` — **the shape to copy.** A correct consumer: call the attribution predicate when and only when the gate certified a reason. |
| `internal/e2e/realclaude/trail_run_outcome_test.go:523-531` | `trailClassifyRun`'s proof arm. The Detail format string mutations 2, 6 and 9 edit; the ordering sentence assertion A8 keys on. |
| `internal/e2e/realclaude/trail_run_outcome_test.go:344-362` | The `out` initializer (`Gate:`, `Admit:` provenance) + the `decide` closure. Mutations 7 and 8 edit here. |
| `internal/e2e/realclaude/trail_run_outcome_test.go:605-631` | `trailRunWellFormed()` / `trailRunProofReadings()` — what they supply (counts, `Liveness`, `PyryExited`, `BoundFrom`, `ClaudeState`) and what must be overwritten (`Gate`, `Admit`). |
| `internal/e2e/realclaude/trail_run_outcome_test.go:911-960` | `TestTrailClassifyRun` — already asserts `got.Gate`/`got.Admit` provenance on every row (`:945-950`) and no-truncation (`:938`). This is the collateral surface for mutations 7, 8 and 9. |
| `internal/e2e/realclaude/trailer_admissibility_test.go:393-402` | `trailGate`'s usable arm. Mutations 1 and 4 edit here; assertion A6 keys on its closing clause. |
| `internal/e2e/realclaude/trailer_admissibility_test.go:546-556` | `trailAdmitAttribution`'s proof arm. Mutation 5 edits here; A7 keys on its ordering sentence. |
| `internal/e2e/realclaude/trailer_admissibility_test.go:264-266` | `trailDetail` = `reachCapCommand(fmt.Sprintf(...))`. **Every Detail in this chain is capped.** |
| `internal/e2e/realclaude/trailer_admissibility_test.go:301-314` | The gate's "the runner path is CARRIED, never read" doctrine, and clause B's requirement that `RunnerPath` arrive intact — why the needle sweep is over `Detail` alone and never the marshalled record. |
| `internal/e2e/realclaude/trailer_admissibility_test.go:904-1031` | `trailGateRunnerReadings` + `TestTrailGateIgnoresTheRunnerPath` — the drive-the-shipped-reader rule this ticket inherits, and the test mutation 1 reddens as collateral. |
| `internal/e2e/realclaude/trailer_admissibility_test.go:1033-1044` | `TestTrailAdmitAttribution`'s fixture premise — the `tdnClassifyReapLog` recipe and the reason it is classified rather than typed. |
| `internal/e2e/realclaude/teardown_liveness_probe_test.go:772-790` | `tdnRunnerFromArgv` — the five constant answers. |
| `internal/e2e/realclaude/teardown_liveness_probe_test.go:891-906` | `tdnFixturePtyArgv` — the shipped ptyrunner argv. |
| `internal/e2e/realclaude/finding_run_record_test.go:256-272` | `finRecordRunnerLabel` — the leading-token reduction the ptyrunner premise uses. |
| `internal/e2e/realclaude/result_trailer_observation_test.go:303-338` | `trailFixtureTrailer` (**use this**) vs `trailPaddedTrailer` (**max_turns — do not use**). |
| `internal/e2e/realclaude/background_reach_probe_test.go:120-125, 945-950` | `reachMaxCommandBytes = 512`, `reachTruncationMarker`, `reachCapCommand`. |

---

## Context

The live exit-path probe reached `run-running-at-trailer` on `admit-proof` on 2026-08-06 under `PYRY_USE_STREAMJSON=0` — the ptyrunner path. The three pure functions that produced that reading are all driveable offline, but nothing pins their composition **under a ptyrunner reading**:

- `TestTrailRunComposesWithGateCases` drives the whole chain, but all eight `trailGateCases()` rows carry `trailRunnerUnread()` — the indeterminate answer.
- `TestTrailGateIgnoresTheRunnerPath` drives all five readings, but only over `trailGate`, and compares each row **against itself**. An edit that moved a Detail under all five readings alike passes it untouched.

Two queued tickets edit the gate's arms. This spec closes the gap.

**The pin is green the day it lands.** No arm reads the runner path today, so the composition cannot vary by it. Its value is entirely in what it discriminates, which is why § Discrimination requires nine demonstrated mutations rather than an assertion of usefulness.

**Scope limit that must be stated in the test's own doc comment.** Both shipped gathers fill the gate's runner-path field with `trailRunnerUnread()` by construction (`tdnClaudeCommand` skips any row whose needle list lacks `tdnClaudeNeedle`, and `finding_exit_path_probe_test.go:264-272` forbids adding it). This pin therefore reproduces the composition **given** a ptyrunner reading; over a live run today the reading always names no runner. Nothing in the test may claim otherwise.

---

## Design

### Placement

**New file: `internal/e2e/realclaude/trail_ptyrunner_composition_test.go`** (build tag `//go:build e2e_realclaude`, package `realclaude`).

A new file rather than an append to `trail_run_outcome_test.go`, for two reasons: `git diff --stat` showing a single added file is the cheapest possible demonstration of AC4's "nothing else moves", and this package's norm is already one concern per file (`finding_key_name_bounds_test.go`, `finding_key_name_containment_test.go`, …). All helpers are package-scoped, so nothing needs exporting.

**Test name: `TestTrailComposesUnderAPtyrunnerReading`.** It must begin with `TestTrail` — AC5's green command is `-run '^TestTrail'`.

### The chain

Signature-level sketch; the developer writes the body.

```go
reading := tdnRunnerFromArgv(tdnFixturePtyArgv)          // the reading, DRIVEN not typed
classified := tdnClassifyReapLog([]byte(trailReapLine(1, "[7788]")+"\n"), 7788)
gate := trailGate(trailGateInput{
    Scan:       trailScan([]byte(trailFixtureTrailer + "\n")),
    RunnerPath: reading,
})
admit := trailAdmitAttribution(classified, gate.Reason)
in := trailRunProofReadings()                            // supplies C6-C9's rest
in.Gate, in.Admit = gate, admit                          // overwrite the hand-built two
out := trailClassifyRun(in)
```

`trailRunProofReadings()` is taken as a copy and its `Gate` / `Admit` overwritten, exactly as the ticket's Technical Notes require: what it usefully supplies is `MatchCount`, `RowsScanned`, `Liveness`, `PyryExited`, `BoundFrom` and `ClaudeState`, which satisfy contract checks C6–C9. Its hand-built `Gate` (`Detail: "usable"`) and `Admit` (`Detail: "the proof"`) are precisely what AC1 requires the real functions to produce.

This composition was run under overlay during spec authoring and reaches `run-running-at-trailer` through `gate-trailer-usable` / `admit-proof`. The pin is green on landing, as predicted.

### Measured facts the test rests on

Every number below was measured at `29215f8` by driving the shipped functions under `go test -overlay`, not derived from the prose.

| Quantity | Value |
|---|---|
| `tdnRunnerFromArgv(tdnFixturePtyArgv)` | `"ptyrunner (claude argv carries --session-id)"` — **44 bytes** |
| `finRecordRunnerLabel(reading)` | `"ptyrunner"` |
| `reachMaxCommandBytes` | 512 (`reachTruncationMarker` adds 29 on truncation) |
| `gate.Detail` | 229 bytes → **283 bytes of cap headroom** |
| `admit.Detail` | 341 bytes → 171 bytes of headroom |
| `out.Detail` | 444 bytes → **68 bytes of cap headroom** |

### The needle

The needle is **the whole 44-byte constant answer**, never a bare `ptyrunner` token: a Detail may legitimately name a runner in fixed prose, and only an interpolated reading is the defect.

Assert over the `Detail` **string alone** and never over the marshalled record. `trailGateResult` carries the reading in its own `RunnerPath` field by design (`trailer_admissibility_test.go:314`), and clause B of `TestTrailGateIgnoresTheRunnerPath` (`:996-1000`) requires it to arrive intact — a whole-record sweep for this needle is **red against a correct build**.

### Two Details carry the no-echo claim, not three

- `trailGate` is handed the reading directly via `trailGateInput.RunnerPath`. → assert.
- `trailClassifyRun` is handed it inside `readings.Gate.RunnerPath`, because `trailRunReadings.Gate` is the whole `trailGateResult` (`trail_run_outcome_test.go:180`). → assert.
- `trailAdmitAttribution(reap tdnReapOutcome, certified string)` is **handed no runner path at all**. A byte assertion on its Detail is green by construction — the vacuous rung AC3 forbids. **It is dropped, and the test's doc comment must state why**, at the point a reader will look for it.

### The keyed phrases

Mutations 5 and 6 delete near-identical prose from two different arms (both end "alive strictly AFTER the trailer was written"; all three arms cite `runner.go:479-485`). Each assertion is therefore scoped to **one** `Detail` variable and keyed on a phrase unique to that arm. Uniqueness was measured across all three Details:

| Assertion | Detail | Keyed phrase | gate / admit / run |
|---|---|---|---|
| A6 | `gate.Detail` | `a reap-log attribution can be proof` | ✓ / ✗ / ✗ |
| A7 | `admit.Detail` | `alive strictly AFTER the trailer was written — and therefore alive` | ✗ / ✓ / ✗ |
| A8 | `out.Detail` | `The group was alive strictly AFTER the trailer was written, and therefore alive when it was written.` | ✗ / ✗ / ✓ |

The em-dash in A7's phrase and the comma in A8's are what separate the two neighbours. Note the bare `alive strictly AFTER the trailer was written` matches **both** admit and run — it must not be used.

**Never assert over a joined or concatenated Detail string.** That is the shape in which mutations 5 and 6 would each redden two assertions and neither would be a sole red.

### The cap-headroom clause (why it exists, and why it is an `else if`)

`out.Detail` has only **68 bytes** of headroom against a 44-byte needle. Measured under mutation 2 (bare appended `%s`), the mutated Detail is 489 bytes — the echo survives intact and A5's no-echo branch fires correctly. But a later edit adding 25 more bytes of prose to that arm would push an echo past the cap, and the no-echo assertion would go on passing while an echo was being silently truncated away. That is #1284's cap-vacuity failure, on a margin thin enough to be a live risk rather than a speculative one.

`gate.Detail` has 283 bytes of headroom — it would have to more than double before the cap could hide a 44-byte echo. **No headroom clause there**: an unobserved failure mode does not earn a defence.

Structure the run-side check as one assertion site with two mutually exclusive branches:

```go
if strings.Contains(out.Detail, reading) {
    t.Errorf(...)          // A5a — the echo itself
} else if room := reachMaxCommandBytes - len(out.Detail); room < len(reading) {
    t.Errorf(...)          // A5b — the claim above is untestable at this length
}
```

`if` / `else if` rather than two `if`s deliberately: any mutation that echoes the reading also eats the headroom, so two independent checks would make mutation 2 redden two assertions and lose its sole red.

### Premises vs claims

This package already distinguishes them (`trailer_admissibility_test.go:1005-1012` "The premise, shared with `TestTrailGate` and asserted before the claim"; `trail_run_outcome_test.go:1156` "The premise first, so the test cannot pass by classifying garbage"). AC3's sole-red obligation is discharged over the **claims**. The premises are `t.Fatalf` guards over the test's own **inputs**, and each must carry a one-line comment saying so:

1. `finRecordRunnerLabel(reading) == "ptyrunner"` — the reading reduces to ptyrunner. Reddened by a reword of `tdnRunnerFromArgv`, which is the vacuity AC1 names; that reader is outside this chain and out of scope.
2. `classified.Verdict == tdnReapHeldPGIDKilled && classified.LineCount == 1` — the real producer emits this record (`TestTrailAdmitAttribution:1040-1044`'s recipe and its reason).
3. `gate.Reason != ""` before calling `trailAdmitAttribution` — the correct-consumer obligation made explicit, and what keeps `""` out of the predicate's contract arm.
4. **The reading carries no argv value.** Assert the reading contains neither `/tmp/s.json` nor `11111111-` — the two *values* in `tdnFixturePtyArgv` that sit after a flag. See § Security review [Trust boundaries] for why this is load-bearing: the reader may legitimately name the flag `--session-id` in its constant answer, but it may never echo what follows one. Both literals are repo-authored fixture substrings, never captures.

**A gate-value premise is deliberately not among them.** Any mutation to `trailGate`'s usable arm's *value* cascades: a different in-space value trips C5 (`trail_run_outcome_test.go:416-422`), an out-of-space one trips C1, and either way the run-level outcome moves too. So `gate.Value == trailGateUsable` can never be a sole red and would be an assertion AC3 requires a mutation for and cannot have. AC1's "reached through `trailGateUsable` and `trailAdmitProof`" is instead asserted on the **published provenance fields** `out.Gate` / `out.Admit` (A2, A3) — which is both AC2's published-bytes framing and mutation-discriminable.

---

## Discrimination

Nine assertions, nine mutations, 1:1. Sole-red is claimed **within this test's own assertion set**; collateral reds in shipped tests are listed and are expected, not defects.

| # | Assertion | Sole red for | Collateral (shipped) |
|---|---|---|---|
| A1 | `out.Value == trailOutcomeRunningAtTrailer` | **M3** | — |
| A2 | `out.Gate == trailGateUsable` | **M7** | `TestTrailClassifyRun:945` |
| A3 | `out.Admit == trailAdmitProof` | **M8** | `TestTrailClassifyRun:948` |
| A4 | `!strings.Contains(gate.Detail, reading)` | **M1** | `TestTrailGateIgnoresTheRunnerPath` |
| A5a | `!strings.Contains(out.Detail, reading)` | **M2** | — |
| A5b | `else if` headroom `< len(reading)` | **M9** | — |
| A6 | `strings.Contains(gate.Detail, <A6 phrase>)` | **M4** | — |
| A7 | `strings.Contains(admit.Detail, <A7 phrase>)` | **M5** | — |
| A8 | `strings.Contains(out.Detail, <A8 phrase>)` | **M6** | — |

### The mutations

Six are the ticket's; M7–M9 are the ones A2, A3 and A5b exist to catch.

1. **`trailGate` usable arm interpolates the reading into its Detail.** Append ` %s` + `in.RunnerPath`. 229 → 273 bytes, no truncation, the arm's own closing sentence intact → A4 alone.
2. **`trailClassifyRun` proof arm interpolates `readings.Gate.RunnerPath`.** Append a **bare** ` %s` — measured 489 bytes, echo intact, ordering sentence intact → A5a alone. *Framing prose here is a trap:* more than 24 bytes of it pushes the line past 512 and the cap truncates the tail of the needle, making the mutation silently green.
3. **`trailClassifyRun` proof arm returns another outcome, Detail format string left intact** (e.g. `trailOutcomeMatchedUnattributed`) → A1 alone. Moving the arm or editing its Detail as well would redden two and would not show the value itself is pinned.
4. **Gate usable arm loses its certification sentence** (`trailer_admissibility_test.go:396-399`) → A6 alone.
5. **`trailAdmitAttribution` proof arm loses its ordering sentence** (`:549-554`) → A7 alone.
6. **`trailClassifyRun` proof arm loses its ordering sentence** (`trail_run_outcome_test.go:524-530`) → A8 alone.
7. **Drop `Gate: readings.Gate.Value` from the `out` initializer** (`:349`) → A2 alone. Step 2 branches on `readings.Admit.Value`, not on `out`, so the decision and Detail are untouched.
8. **Drop `Admit: readings.Admit.Value`** (`:350`) → A3 alone, same reason.
9. **Pad the proof arm's Detail by ~40 literal bytes** → 484 bytes: **no truncation** (so `TestTrailClassifyRun:938` stays green), headroom 28 < 44 → A5b alone. Padding it past 512 instead would truncate and redden that shipped guard as collateral — 40 bytes is the value that targets exactly the gap between the shipped truncation guard and this claim.

### Running a mutation

No worktree writes. Copy the file, edit the copy, overlay it. This recipe was validated end to end on mutation 2 during spec authoring:

```bash
SP=<scratch>; W=<worktree>; P=$W/internal/e2e/realclaude
cp "$P/trail_run_outcome_test.go" "$SP/mut.go"
perl -0pi -e 's/<exact old>/<exact new>/' "$SP/mut.go"    # one edit
printf '{"Replace": {"%s/trail_run_outcome_test.go": "%s/mut.go"}}\n' "$P" "$SP" > "$SP/ov.json"
cd "$W" && go test -tags e2e_realclaude -overlay="$SP/ov.json" -run '^TestTrail' ./internal/e2e/realclaude/
```

Record, per mutation, which assertions fired. A mutation that reddens two of this test's assertions, or none, means the assertion pair or the mutation is wrong — fix it before landing.

**The mutated copy goes in the scratch directory, never in the package directory.** A stray `*_test.go` left under `internal/e2e/realclaude/` is picked up by the dispatcher's unconditional post-run auto-commit and pushed to `feature/1415`.

---

## Testing strategy

- **Green, offline, no credentials:** `go test -tags e2e_realclaude -run '^TestTrail' ./internal/e2e/realclaude/` — baseline re-verified at `29215f8` on 2026-08-09: green in 3.36 s. No `t.Skip`, no env gate, no `needs-real-claude`.
- **Race:** `-race` over the same `-run` filter. The test is pure and sequential; no `t.Parallel()`.
- **Vet:** `go vet` and `staticcheck` in `make check` run **without** `-tags e2e_realclaude` and never see this file. Run `go vet -tags e2e_realclaude ./internal/e2e/realclaude/` once manually.
- **gofmt:** `gofmt -l internal/e2e/realclaude/` is already dirty on `main` (`permission_protocol_spike_test.go`, `resilience_test.go`, `tool_loop_test.go`). It is not a useful package-wide signal — check the new file alone.
- **Nothing else moves:** `git diff --name-only origin/main...` must list exactly the new test file and this spec.

## Error handling & concurrency

None. Every function in the chain is pure over its inputs — no exec, no clock, no filesystem, no `*testing.T` — which is the whole reason this is driveable offline. The test adds no goroutine, no timeout and no cleanup.

## Nothing else moves

No production behaviour changes. No closed set grows. `TestTrailGateIgnoresTheRunnerPath`, `TestTrailRunComposesWithGateCases` and `TestTrailGateThenAdmit` are untouched; no shipped test's expectations change; no existing fixture is edited (`trailRunProofReadings()` is **called and copied**, never edited).

Do not use `trailPaddedTrailer` — it renders a **`max_turns`** trailer and composes to `trailGateBudgetFired` → `trailAdmitVoidBudgetFired` → `trailOutcomeVoidBudgetFired`, which `trailer_admissibility_test.go:661-666` already pins. Driving it as a row of this proof composition is red against a correct build.

**Failure messages name the reading, never the argv it was driven over.** `tdnFixturePtyArgv` is a repo constant, so printing it would leak nothing here — but this family goes out of its way to make "the command string is consumed, never retained" structural (`tdnClaudeCommand` hands back a constant answer; `pinStateColumns` refuses a `command` column), and a test message that prints an argv is where that erodes. Print `reading`, counts, values and `Detail`s; never `tdnFixturePtyArgv`.

No value from the trailer and no verbatim command string enters any record or literal this ticket adds. Every expected string it holds is this repo's own prose. `tdnFixturePtyArgv` is a repo-authored fixture argv, not a capture. The existing no-captured-bytes sweeps (`trailer_admissibility_test.go:1243`, `trail_run_outcome_test.go:1142`) must still pass.

Eight shipped comments in this package name the cancelled #1374 (`trailer_admissibility_test.go:193, :243, :314, :606, :635, :933`; `finding_run_gather_test.go:548`; `trail_run_rig_test.go:158`). Three of them are #1414's; all eight are out of scope here. **Touch none.**

Per the pipeline rule, `docs/knowledge/codebase/1415.md` is **not** a developer deliverable — the documentation phase writes it after the PR merges.

## Open questions

1. **A5b's threshold is `len(reading)`, i.e. 44.** That is the needle this test drives. If a future ticket drives a longer reading through the same arm, the clause needs re-deriving rather than a bumped literal — write it as `len(reading)`, never as `44`.
2. **The gate side gets no headroom clause.** Recorded as a deliberate omission on 283 bytes of measured slack, not an oversight. If a later ticket grows that arm's prose materially, the clause becomes owed.

---

## Security review

**Verdict:** PASS

The threat this package is built against is not a network attacker: it is that records from the probe family are **pasted into public GitHub issues**, so any captured byte — verbatim model output, pyry's stderr, or a claude argv that in a live run carries `CLAUDE_CODE_OAUTH_TOKEN` / `ANTHROPIC_API_KEY` — must never reach one. Categories below are walked against that.

**Findings:**

- **[Trust boundaries] SHOULD FIX — addressed in this spec (premise 4).** This ticket is the first to drive a **real runner argv** (`tdnFixturePtyArgv`) through `tdnRunnerFromArgv` into `trailGateInput.RunnerPath` — a field that **is marshalled** into the published gate record (`trailer_admissibility_test.go:1252-1255`). The boundary that keeps a command string out of that record is a single explicit function: `tdnRunnerFromArgv` (`teardown_liveness_probe_test.go:772-790`) returns one of four constant string literals and interpolates **nothing** from its argument — verified by reading all four return sites. That non-interpolation is load-bearing for this design and is nowhere pinned: `TestTdnRunnerFromArgv` asserts by `strings.HasPrefix` against the three leading tokens, which a reader that appended the argv would still satisfy. Worse, this test would not catch it either — its needle *is* the reader's output, so an argv-bearing reading would simply become an argv-bearing needle and A4/A5 would go on passing. The shipped needle sweep does not close it: `TestTrailAdmissibilityRecordsCarryNoCapturedBytes` passes `trailRunnerUnread()`, the **empty-argv** answer, so the `runner_path` field has never been swept under a reading derived from a real argv. Closed by premise 4 (4 lines): the reading must contain neither `/tmp/s.json` nor `11111111-`, the two values in the fixture argv that sit after a flag. A reader may name the flag `--session-id`; it may never echo what follows one.
- **[Tokens, secrets, credentials] No findings.** No token is generated, stored, rotated or revoked. The one secret-adjacent exposure is category 1's, above. The design asserts over `Detail` **strings alone** and never marshals anything — a constraint arrived at for needle-sweep correctness (`trailGateResult.RunnerPath` legitimately carries the reading, so a whole-record sweep is red against a correct build) that also means the test serialises no record at all.
- **[File operations] No findings.** The test performs no file I/O. The mutation recipe writes only to the session scratch directory and reaches the package through `go test -overlay`'s absolute-path map; no worktree write, no traversal, no check-then-use, no created file whose mode matters. The one operational hazard — a mutated `*_test.go` left in the package directory and swept up by the dispatcher's unconditional auto-commit — is named explicitly in § Running a mutation.
- **[Subprocess / external command execution] No findings.** Nothing on this path execs. `trailGate`, `trailAdmitAttribution` and `trailClassifyRun` are pure over their inputs by documented contract (no exec, no clock, no filesystem, no `*testing.T`); `tdnRunnerFromArgv` takes a string and switches on `strings.Contains`; `tdnClassifyReapLog` parses already-captured bytes. No `sh -c`, no env inheritance, no signal handling — this is the property that makes the whole chain driveable offline.
- **[Cryptographic primitives] N/A.** No randomness of any kind is security-relevant here, and none is used: the test is fully deterministic over fixed fixtures and a fixed pgid (7788). No `math/rand`, no clock, no nonce, no comparison against a secret.
- **[Network & I/O] No findings.** No socket, no HTTP, no upgrade, no timeout surface. The one cap in play is `reachMaxCommandBytes = 512` on published `Detail` content, and it is the subject of assertion A5b rather than an unexamined assumption: `out.Detail` sits at 444 bytes against a 44-byte needle, and the clause fails loudly if a later edit shrinks that margin below the point where an echo would be truncated away — the #1284 cap-vacuity failure, on a measured 24-byte slack.
- **[Error messages, logs, telemetry] SHOULD FIX — addressed in this spec.** The test's `t.Errorf` messages print `Detail` strings, which are this repo's own prose plus counts, pids and terminal reasons — safe by the record family's design. The gap was that nothing stopped a failure message from printing `tdnFixturePtyArgv`; harmless for a repo constant, but it is the first crack in the "command string consumed, never retained" discipline that `tdnClaudeCommand` and `pinStateColumns` enforce structurally. § Nothing else moves now states the rule. The test writes no artifact — `tdnFinish` / `writeTdnArtifacts` are not on this path — so AC5's "publishes nothing that needs review" holds structurally rather than by review.
- **[Concurrency] No findings.** No goroutine, no `t.Parallel()`, no shared mutable state, no lock. Two aliasing traps in this family are avoided by construction rather than by luck: copying a `trailGateInput` copies the `*resultTrailer` pointer (`trailer_admissibility_test.go:955-962`) and `trailRunProofReadings()` is a function because `Liveness` is a slice (`trail_run_outcome_test.go:609-610`). The design takes exactly one copy of each, mutates only `Gate` and `Admit` by whole-value assignment, and writes through neither the pointer nor the slice — which is what a later `t.Parallel()` would need, and why the reason is recorded here rather than left implicit.
- **[Threat model alignment] No findings.** `docs/protocol-mobile.md` § Security model is not the relevant model — no relay, no wire format, no device identity is touched. The applicable threat is this package's own paste-into-a-public-issue exposure, addressed above; the trust-boundary gap was the only one this spec newly opened, and premise 4 closes it. Pinning `tdnRunnerFromArgv`'s non-interpolation at its own definition (rather than at this consumer) is **out of scope** here — AC4 forbids growing a closed set or changing a shipped test's expectations, and it belongs with whichever ticket next edits that reader.

**Reviewer:** architect (self-review per `architect/security-review.md`)
**Date:** 2026-08-09
