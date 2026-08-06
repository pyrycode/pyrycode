# #1302 — The run-outcome gather takes pyry-exit and claude-liveness from its caller

**Ticket:** [#1302](https://github.com/pyrycode/pyrycode/issues/1302) — split from #1279.
**Size:** S. Two files, both `_test.go` under the `e2e_realclaude` build tag. No production `.go` file changes.

---

## Files to read first

Read these before writing anything. The whole design lives in two files; the third is the consumer whose ordering the ticket forbids you to touch.

| Path | What to extract |
|---|---|
| `internal/e2e/realclaude/finding_run_gather_test.go:174-279` | `finGatherReadings`' doc comment and body. The two lines at `:275-276` are what this ticket deletes; the caller-obligations bullet at `:207-215` is the prose that dies with them. |
| `internal/e2e/realclaude/finding_run_gather_test.go:93-98` | File-header § *"The two staged values that are still staged, and who owns them"* — the second prose site AC1 kills. |
| `internal/e2e/realclaude/finding_run_gather_test.go:68-91` | File-header § *"The certified terminal reason crosses into the readings VERBATIM, by design"*. **`ClaudeState` joins this list.** Read it before touching `TestFinGatherReturnsNoCapturedBytes`. |
| `internal/e2e/realclaude/finding_run_gather_test.go:283-347` | `finGatherCase` + `finGatherCases()`. The three rows and the one-varied-dimension doctrine. |
| `internal/e2e/realclaude/finding_run_gather_test.go:378-396, 402-515, 524-612, 643-707` | The four call sites of the gather, and `finGatherAssertContract`'s per-row checks (`:502-506` reads `readings.PyryExited`). |
| `internal/e2e/realclaude/finding_stage_held_group_test.go:395-440` | `finStageRun` — the fifth call site, and the doc sentence at `:406-408` AC1 names explicitly. |
| `internal/e2e/realclaude/finding_stage_held_group_test.go:15-26` | Stage-file header; `:26` points at the gather doc's `:202-206`, a reference that moves when you edit that doc. |
| `internal/e2e/realclaude/trail_run_outcome_test.go:173-219` | `trailRunReadings`. `PyryExited`'s zero-polarity argument (`:204-207`) is the one you copy into the new inputs type; `ClaudeState`'s doc (`:215-218`) is the shipped meaning of `""`. |
| `internal/e2e/realclaude/trail_run_outcome_test.go:306-343, 434-449, 523-542` | The decision order, contract check C7 (both arms), Step 2 and Step 3. This is the ordering you must not change. |
| `internal/e2e/realclaude/trail_run_outcome_test.go:693-702, 817-832` | The shipped ordering rows. `proofPyryLive` (proof + `PyryExited: false`) wants `trailOutcomeRunningAtTrailer`. This row is why AC2's pair may not be built on the proof arm. |
| `internal/e2e/realclaude/finding_run_record_test.go:201-242` | `finRecordInputs` — the family's named-fields precedent and its *stated reason*, which you will restate differently here (see § Design). |
| `internal/e2e/realclaude/result_trailer_observation_test.go:242-274` | `trailWaitForTrailer` reads `stdout.Bytes()` and never writes. Load-bearing for AC2's pair — see § AC2. |
| `internal/e2e/realclaude/background_trigger_probe_test.go:722-742` | `probeSyncBuffer`. `Bytes()` returns a copy; the type only ever appends. |
| `internal/e2e/realclaude/process_pin_liveness_test.go:275`, `:1142` | `pinReadState`'s four documented verdicts and `pinIsVerdict`. The closed set the claude parameter's admissible producer draws from. |

**Out of scope, do not touch:** `internal/e2e/realclaude/trail_run_rig_test.go:38-47, 191-192`. It stages the same two values on a different rig under its own stated reason. The ticket says so; so does this spec.

---

## Context

`finGatherReadings` assembles a complete `trailRunReadings` from what a live probe holds. Four of its six fields come from parameters and shipped producers. Two do not — it ends by staging them:

```go
readings.PyryExited = true // no pyry runs here, so there is none to fail to exit
readings.ClaudeState = ""  // no claude runs here; C7 admits "" as "not read"
```

The gather's own doc filed the obligation to promote them *"BEFORE it feeds this gather a live pyry"*. #1282 fed it no pyry, so no test there could distinguish either setting, and the obligation was routed to the first live consumer. This ticket discharges it offline, ahead of any live run. **Nothing on the classifier side changes.** Both arms already exist and are already tested — Step 3's `trailOutcomeVoidPyryDidNotExit` and C7's `ClaudeState` out-of-contract arm. What changes is that the two values become *reachable* through the gather at all.

### What the promotion actually buys — and the sentence that must not survive

The gather's comment claims a hardcoding gather would make a hung-pyry run *"report a scan-side answer — or the finding — instead of the staging void."* **The "or the finding" half is backwards.** Step 2 is consulted before Step 3 by design, the classifier argues the point at `trail_run_outcome_test.go:323-334` (*"Voiding it for a staging failure would SUPPRESS A FINDING THE RUN GENUINELY ESTABLISHED"*), and a regression row pins it: `proofPyryLive` — proof-carrying with `PyryExited: false` — wants `trailOutcomeRunningAtTrailer` (`:695-696`, `:820-821`).

Enumerate the three classes to see exactly what moves:

| Composition | Who answers | Effect of an honest `PyryExited` |
|---|---|---|
| Gate not usable (4 values) | Step 1 | **None.** Answered before Step 3 is reached. |
| Gate usable, `Admit == trailAdmitProof` | Step 2 | **None.** Pinned by `proofPyryLive`. |
| Gate usable, attribution a valid non-proof value | Step 3 → Steps 4-8 | **This is the whole increment.** Reports a scan-side answer or a Step 4-6 void today; reports the staging void once the reading is honest. |

(A fourth class — a certifying gate with an attribution outside `trailIsAdmitValue`'s seven — is answered by C3 in the contract block, above every step.)

**Write the increment as the third row and nothing more.** Any comment, spec sentence or test name promising that the staging void displaces the finding is asking for the classifier's ordering to be broken, and that ordering is itself under a regression row. Do not paste the existing comment's phrasing forward, and do not paraphrase it into the same claim.

---

## Design

### The shape: an inputs struct

Add one package-internal type beside the gather and change the gather to take it:

```go
// finGatherInputs is what a probe holds when it composes one run's readings.
type finGatherInputs struct {
	Stdout      *probeSyncBuffer
	Needles     []string
	Stderr      []byte
	Pinned      []int
	PyryExited  bool
	ClaudeState string
}

func finGatherReadings(in finGatherInputs) (trailRunReadings, finAttributeRecord)
```

Two new fields; the four existing parameters move in unchanged. No other signature in the package changes.

**Why named fields rather than two extra positional parameters.** `finRecordInputs` (`finding_run_record_test.go:203-212`) is the family's precedent, but its stated reason — transposition of two adjacent same-typed strings on opposite sides of the argv prohibition — applies only weakly here: `bool` and `string` cannot transpose without a compile error. The reason that does apply is AC2's. AC2 demands a pair *"identical in stdout seed, needles, stderr and pinned set byte-for-byte, differing only in that value."* With six positional arguments, that identity is a discipline maintained by retyping four arguments the same way at two call sites, and a reviewer can only check it by reading. With a struct it is `copy the value, change one field` — the package's own vary-one-dimension idiom (`trailRunWellFormed()` plus a single field mutation, `trail_run_outcome_test.go:649-702`), and the identity holds by construction. Same doctrine `finRecordInputs` states at `:211-212`: prefer the shape that cannot be got wrong over the discipline that must not be. A secondary benefit: `finGatherReadings(&stdout, needles, stderr, pinned, true, "")` gives a reader no way to know what `true` asserts.

**Both new fields keep the readings' own names, types and zero-polarity**, and the type's doc comment must say so with the argument, not just the fact:

- `PyryExited` zero is `false` → "did not exit" → Step 3's void. This is the SAFE direction and it is the identical argument `trailRunReadings.PyryExited` makes for itself at `trail_run_outcome_test.go:204-207`. A caller who omits the field gets a void, never a finding.
- `ClaudeState` zero is `""`, which is C7's shipped "not read" — an honest report, not an unfilled field. A caller with no claude to read says so by saying nothing.

Because the two zeros land exactly where the readings' own zeros land, an incompletely-filled `finGatherInputs` degrades to a named nothing-was-measured rather than to a claim.

### The body

Delete the two staged lines and the comment above them. Replace with two verbatim copies, made obviously un-defaulted in one line each:

```go
readings.PyryExited = in.PyryExited
readings.ClaudeState = in.ClaudeState
```

**No `if`, no `!= ""` guard, no normalisation, no `pinIsVerdict` call.** AC3 is explicit: the claude verdict is not defaulted, not re-derived from anything else the gather read (in particular not from `readings.Liveness`), and not validated on the way. C7 is the classifier's check and it is already shipped; a second opinion in the gather would repair exactly the record C7 exists to reject, and the out-of-contract answer would become unreachable through this composition. The rest of the body — the trailer leg, the attribution leg with its `Gate.Reason` guard, the argv leg, the per-matched-pid liveness loop — is untouched.

### `finGatherCase` gets no new field

The Technical Notes flag a polarity hazard *if* the pyry-exit value becomes a `finGatherCase` field. **Do not add one.** All three existing rows want the value the constant held; `TestFinGatherComposesTheFindingAndTheNegative` sets `PyryExited: true` (and leaves `ClaudeState` at `""`) once, on the inputs it builds per row. There is then no case-struct zero to point anywhere, no per-row explicit value to keep in sync, and AC4 is discharged by construction rather than by re-deriving three expectations.

AC2's pair does not belong in `finGatherCases()` either: `finGatherAssertContract` runs on every row and would classify the void arm — its `:502-506` check reads `readings.PyryExited` precisely to reject a `trailOutcomeNoRowMatched` row that is secretly a staging void. AC2 gets its own test (below), and so does AC3, for the reason the ticket gives: `finGatherAssertContract` fails any row reaching `run-out-of-contract` (`:511-514`).

### Call sites

Five, all mechanical, all in the two files:

| Site | Passes |
|---|---|
| `finding_run_gather_test.go:384` (`TestFinGatherComposesTheFindingAndTheNegative`) | `PyryExited: true`, `ClaudeState` omitted |
| `finding_run_gather_test.go:553` (`TestFinGatherEmptyPinnedSetIsNotAReading`) | `PyryExited: true`, `ClaudeState` omitted |
| `finding_run_gather_test.go:648` (`TestFinGatherReturnsNoCapturedBytes`) | `PyryExited: true`, `ClaudeState: pinStateRunning` — see § The needle exclusion |
| `finding_stage_held_group_test.go:423` (`finStageRun`) | `PyryExited: true` with the original justifying comment, `ClaudeState` omitted |
| New AC2 test | both polarities |

**`finStageRun` does not gain parameters.** Its five callers never vary either value, and no pyry runs in that file — the original justification ("no pyry runs here, so there is none to fail to exit") is true of `finStageRun` and belongs at its call site as a comment. Threading a parameter through five call sites that all pass `true` is noise, and this ticket makes the value passable, not read. Do not cascade the promotion further than the gather.

### The needle exclusion — read this before touching `TestFinGatherReturnsNoCapturedBytes`

`ClaudeState` is now a caller-supplied **string input** to the gather. `finGatherReadings` copies it verbatim into `readings.ClaudeState`; `trailClassifyRun` copies that verbatim into `trailRunOutcome.ClaudeState` and publishes it as `claude_state` (`trail_run_outcome_test.go:249, 356`); C7 quotes an out-of-contract value into the published `Detail` (`:444-449`).

So the obvious reading of #1271's "plant the needle in every string-bearing input" is **red against correct code** here, exactly as it already is for `terminal_reason` (file header `:68-91`). `TestTrailRunOutcomeCarriesNoCapturedBytes` (`:1141`) plants no needle in that field for the same reason, one layer down.

Requirements:

1. `TestFinGatherReturnsNoCapturedBytes` passes `ClaudeState: pinStateRunning` — a value from `pinReadState`'s closed set. Making it a **constant from the closed set makes the exclusion structural in the fixture** rather than a discipline at the plant site, the same way `finGatherNeedleTrailer` renders `terminal_reason` `"completed"`. It also proves the new field's presence introduces no forbidden key, which `""` would not.
2. The file header's verbatim-crossing section (`:68-91`) gains `ClaudeState` as a named third crossing, with the reason: the classifier copies it whole and publishes it, so the only "fix" for a needle there would be to stop publishing the corroboration field a reader needs to interpret the run.
3. `pinStateRunning` does not move the outcome — corroboration never does (`trail_run_outcome_test.go:336-343`, and `TestTrailRunCorroborationNeverFlips` sweeps all five values at `:1029-1030`). The test's three existing premise assertions (gate usable + `"completed"`, needle within the cap, `Admit == trailAdmitProof`) stay exactly as they are.

The forbidden-key walk needs no change. `trailRunReadings` carries no json tags, so the marshalled key is `ClaudeState`, which matches none of `finGatherForbiddenKeys()`' six substrings; the fixture row at `:809-812` already lists `"ClaudeState":""` in the readings' own key set and stays correct.

### Prose that must not survive (AC1)

Six sites. AC1 is not discharged by deleting the two assignments alone.

| Site | Action |
|---|---|
| `finding_run_gather_test.go:93-98` (§ *"The two staged values that are still staged, and who owns them"*) | **Delete the section.** Nothing is staged any more. If a replacement is wanted, it belongs in the verbatim-crossing section as `ClaudeState`'s entry (above), not as a second account of the same two fields. |
| `finding_run_gather_test.go:176` | "from the four inputs a live probe holds" → six, and the sentence should name the type rather than a count. |
| `finding_run_gather_test.go:200-215` (§ *"The caller's obligations"*) | **Keep bullet 1** (`pinned` is `[]int`; the `pinScan.Matches` conversion obligation stands for every future caller). **Delete bullet 2 entirely** — it is the obligation this ticket discharges. |
| `finding_run_gather_test.go:274-276` | The `// The only two staged values…` comment and both assignments. |
| `finding_stage_held_group_test.go:406-408` | *"finGatherReadings stages PyryExited true, which closes Step 3"* is now false. Rewrite as: `finStageRun` **passes** `PyryExited` true, which closes Step 3. The surrounding argument (gate check closes Step 1; `MatchCount > 1` closes Steps 4 and 5; `finStageAssertLiveness` closes Step 6; what is left is Step 2 against Step 7) is unchanged and still correct. |
| `finding_stage_held_group_test.go:26` | *"the obligation finGatherReadings' own doc (:202-206) names as #1282's"* — the line reference moves when the bullet above it is deleted. Re-point it at the surviving bullet or reword to drop the line number. |

`finding_stage_held_group_test.go:73` ("with a certifying gate, PyryExited true, a clean scan and MatchCount > 0…") stays true and needs no edit — the arms still run at `PyryExited` true, and the sentence never said who set it.

**No replacement sentence may claim the staging void displaces the finding.** Write the third row of the table in § Context instead: the promotion moves the outcome of one class of run — usable gate, attribution not proof — and leaves the proof arm and the non-usable-gate arms exactly where they were.

Docs under `docs/knowledge/` quote the old signature (`features/e2e-realclaude.md:977`, `codebase/1281.md:22`). **Leave them alone** — they are the documentation phase's, written after merge.

---

## Testing strategy

Existing rows keep their claims unchanged. **The expected number of outcome changes across `TestFinGather*` and `TestFinStage*` is zero**, because every existing call site passes the values the constants held. AC4 is satisfied by that, not by re-deriving expectations. If a row's outcome *does* move, that is a defect in the promotion — find it, do not update the expectation.

Two new tests. Both offline: synthetic stdout, synthetic reap line, no live claude, no credentials, no daemon, no turn, no `t.Skip`.

### AC2 — the pyry-exit value is observable at the outcome

One test, two arms, built on the **negative** composition (usable gate, `trailAdmitVoidGroupUnnamed`, `MatchCount == 0`): the same shape as `finGatherCases()`' second row — `trailFixtureTrailer` seed, a reap line naming `finGatherNamedPGID`, pinned `[]int{finGatherUnnamedPGID}`.

- Build one `finGatherInputs` with `PyryExited: true`. Gather, classify. Expect `trailOutcomeNoRowMatched` (Step 8).
- Copy the struct, set `PyryExited: false`, change nothing else. Gather, classify. Expect `trailOutcomeVoidPyryDidNotExit` (Step 3).
- Assert the two outcomes differ, naming the one field that varied.

Four construction facts the arms depend on — get these wrong and the test is vacuous rather than red:

- **Share one `Stdout` buffer between the arms.** `trailWaitForTrailer` only ever calls `stdout.Bytes()`, which returns a copy of an append-only buffer (`result_trailer_observation_test.go:251`, `background_trigger_probe_test.go:727-742`). Reading it twice is non-destructive and yields the same bytes, so "identical stdout seed byte-for-byte" is literally true rather than "two buffers seeded from the same constant". Both arms observe on the first poll, so both get `BoundFrom: trailBoundFromStart`.
- **Share one `Needles` slice.** `t.TempDir()` returns a *new* directory on every call, so calling `finGatherNeedles(t)` twice would vary a second dimension. Call it once.
- **Assert the premises on the exited arm** before asserting the pair: gate is `trailGateUsable` certifying `"completed"`, `Admit.Value` is `trailAdmitVoidGroupUnnamed`, `MatchCount == 0`, `RowsScanned > 0`. Without the last two the "exited" arm could be sitting on Step 5's `run-void-no-rows-parsed` and the pair would separate two voids.
- **Do not assert the two `trailRunReadings` are equal.** `RowsScanned` is a live count off the ambient process table and legitimately differs between two calls. The AC constrains the four *inputs*, not the readings.

The test's doc comment must state why the proof arm cannot carry this pair: Step 2 returns on `trailAdmitProof` before Step 3 is consulted, `proofPyryLive` (`trail_run_outcome_test.go:695-696, 820-821`) pins that, and reordering the classifier to make the proof arm produce a pair would be a defect rather than a fix.

### AC3 — the claude verdict is carried exactly as handed in

One test, three scenarios. It must **not** go through `finGatherAssertContract` (that helper fails any row classifying `run-out-of-contract`).

- **A documented verdict crosses unchanged.** Hand in `pinStateRunning` over a certifying composition. Assert `readings.ClaudeState == pinStateRunning` (identity with what was handed in, not membership) and that `trailClassifyRun`'s published `ClaudeState` is the same value — the field is republished verbatim at `trail_run_outcome_test.go:356`. The outcome is whatever the composition reaches with `ClaudeState` at `""`; assert it is unchanged, which is the corroboration-never-flips property at this layer.
- **A value outside the documented set reaches the out-of-contract answer.** Hand in a verdict nobody defined. Assert `readings.ClaudeState` is that exact string — it was carried, not repaired or blanked — and that `trailClassifyRun` returns `trailOutcomeOutOfContract`. **Assert which arm fired**, via a distinctive fragment of C7's own ClaudeState sentence (`trail_run_outcome_test.go:445-448`), not the value alone: eight other contract checks answer with the same value, and a gather bug that broke the gate or the bound would satisfy a value-only assertion. The fixture string must be neither a `pinReadState` verdict nor `""`; give it a name distinct from the classifier layer's own `"some-verdict-nobody-defined"` (`:749`) so a failure names which layer produced it.
- **`""` stays reachable with its shipped meaning.** Hand in `""` explicitly over the same composition; assert `readings.ClaudeState == ""` and that the outcome is a real answer, not `run-out-of-contract`. C7 admits `""` as "not read" (`:444`), and a caller with no claude to read must be able to say so. This is the arm a `pinIsVerdict` validation in the gather would break first.

The third scenario overlaps with what the existing rows already exercise implicitly. Keep it anyway — it is the only place the empty value is asserted as a *deliberate caller statement* rather than as an omitted field.

### Commands

`make check` and `make build` never compile `e2e_realclaude`-tagged files, so a PR whose whole diff sits under that tag is a vacuous green. Run both of these and read the split:

```bash
go vet -tags e2e_realclaude ./internal/e2e/realclaude/...
go test -race -tags e2e_realclaude ./internal/e2e/realclaude/...
```

Expect a **PASS/SKIP split, not all-PASS** — the live specs in that package skip without credentials. `-run '^TestFinGather|^TestFinStage'` narrows to this ticket's rows during iteration, but the full-package run is what proves the signature change compiles against every caller.

---

## Error handling / failure modes

No new error paths. The gather returns no error today and does not start to; the two new fields are copied, never parsed. The failure modes the design deliberately routes:

- **A caller omits `PyryExited`** → `false` → Step 3's void. Loud in the offline rows (every outcome moves), and safe in a live probe (a void, never a false finding).
- **A caller omits `ClaudeState`** → `""` → C7's "not read". An honest report, so nothing is owed.
- **A caller passes an undocumented verdict** → carried whole → C7 → `run-out-of-contract` with the value quoted in `Detail`. The gather does not repair it; that is AC3's point and the classifier's job.
- **A caller reads a raw `ps` column into `ClaudeState`** → the parameter's admissible producer is `pinReadState`'s `Verdict` (a closed set; `pinStateColumns` carries no command column) and never a column read. AC3's "not validated on the way" is a statement about the gather, not a licence for its caller. State this in the type's doc comment — see § Security review, Trust boundaries.

## Concurrency model

None. `finGatherReadings` is synchronous and single-goroutine; the only shared state is `probeSyncBuffer`, whose mutex is already correct and whose read path this ticket does not touch. AC2's two arms run sequentially over one buffer, which is a read-only sharing of an append-only value.

## Open questions

- **The live consumer's fill site is still unwritten.** This ticket makes both values passable; the producer for the claude verdict will be `pinReadState` over the pid `probeWaitForDirectChild` returns (`background_trigger_probe_test.go:975`), and nothing here calls either. Whoever writes the first live consumer owns the `.PGID`-only conversion obligation that survives in the gather's bullet 1.
- **`trailRigGather` still stages both values** (`trail_run_rig_test.go:191-192`) under its own stated reason. Explicitly out of scope; if it is ever promoted it is a separate ticket with a separate argument, because that rig runs no pyry by construction rather than by circumstance.

---

## Security review

**Verdict:** PASS

**Findings:**

- **[Trust boundaries]** SHOULD FIX, addressed in the spec. The promotion opens a new caller→instrument boundary: `ClaudeState` becomes a caller-supplied string that crosses the gather verbatim, is republished as `claude_state`, and is quoted into a published `Detail` on C7's arm. That is three publication sites for a value the gather deliberately does not validate. The design keeps the boundary *explicit and narrow* — the admissible producer is `pinReadState`'s `Verdict`, a closed four-value set whose `pinStateColumns` (`pid=,ppid=,stat=`) carries no command column by construction, and the type's doc comment must say so at the field. The risk this closes is real and has a shipped precedent: a caller reading a raw `ps` column into this field would put verbatim argv — and with it an operator's `CLAUDE_CODE_OAUTH_TOKEN` or `ANTHROPIC_API_KEY` — into an artifact destined for a public issue, which is the same channel `pinned []int` is `[]int` to keep shut (`finding_run_gather_test.go:202-206`). Not a MUST FIX because the value never leaves the test process in this ticket (no live caller exists yet) and C7 rejects anything outside the closed set at the classifier, capping the leak at one quoted string inside a 512-byte `trailDetail`. The spec makes the constraint a documented field obligation rather than leaving it to the next author's memory.
- **[Error messages, logs, telemetry]** No findings, and one trap disarmed. `TestFinGatherReturnsNoCapturedBytes` is the shipped enforcement that neither return carries captured bytes, and the new string field is exactly the kind of input #1271's "plant the needle in every string-bearing input" rule would send a developer to plant in — where it is red against *correct* code, because the classifier copies and publishes the field whole. § The needle exclusion states this and makes it structural in the fixture by requiring `pinStateRunning` (a closed-set constant that cannot carry a needle) rather than a discipline at the plant site. The forbidden-key walk is unaffected: the marshalled key is `ClaudeState`, matching none of the six forbidden substrings, and it is already in the walk's own key-set fixture at `:809-812`.
- **[Subprocess / external command execution]** No findings — nothing in this change execs. The gather's own `pinScanArgv` call is untouched, and the ticket explicitly does not call `pinReadState` or `probeWaitForDirectChild`. AC3's out-of-contract fixture is a Go string literal handed to a pure function, never a filesystem or process operand.
- **[File operations]** Not applicable by design decision — this change creates, opens and writes no file. `finGatherNeedles`' `t.TempDir()` path is a match pattern handed to a Go-side matcher and nothing is ever created at it (`finding_run_gather_test.go:139-144`); AC2 reuses one such path across both arms rather than minting a second, which reduces rather than adds filesystem surface.
- **[Concurrency]** No findings. No goroutine is spawned and no lock is taken. The one shared value, `probeSyncBuffer`, is shared read-only between AC2's two sequential arms via `Bytes()`, which returns a copy under the existing mutex (`background_trigger_probe_test.go:736-742`); `trailWaitForTrailer` never writes to it (`result_trailer_observation_test.go:251`). No shutdown path, no partial state.
- **[Tokens, secrets, credentials]** No findings *in what this change handles* — it generates, stores and compares nothing. The credential exposure this package guards against is indirect (verbatim argv off the ambient process table reaching a published artifact), and it is covered under Trust boundaries above.
- **[Network & I/O]** Not applicable — no socket, no HTTP server, no external I/O. The only "input" is a caller-supplied in-process struct.
- **[Cryptographic primitives]** Not applicable — no randomness, hashing, comparison against a secret, or TLS anywhere in the change.
- **[Threat model alignment]** The relevant threat for this package is the one its own headers name: an instrument that publishes an artifact an operator pastes into a public issue. This change adds one publishable string field and constrains its producer to a closed set; it removes nothing from the existing enforcement (`TestFinGatherReturnsNoCapturedBytes`, C7, the forbidden-key walk). Out of scope and named: the live fill site, where `pinReadState`'s verdict is actually read, and where the `.PGID`-only conversion obligation lands — see § Open questions.

**Reviewer:** architect (self-review per `architect/security-review.md`)
**Date:** 2026-08-04
