# 1284 — The staging-gate outcome tier and the rule that an unstaged run is never classified

**Size:** S (confirmed; see § Size check)
**Scope:** one new file, `internal/e2e/realclaude/finding_staging_gate_test.go`. Nothing else in the repo is modified.
**Build tag:** `e2e_realclaude`, package `realclaude`. Offline in full — no live claude, no credentials, no daemon, no process read, no `t.Skip`.

---

## Files to read first

Read these before writing anything. This is the turn-1 data load; every design decision below is anchored in one of them.

| Path | What to extract |
|---|---|
| `internal/e2e/realclaude/trail_run_outcome_test.go:100-169` | The eleven `run-*` values and the third-sub-namespace argument at `:108-113`. This spec's seven mirror that argument; do not re-derive the eleven. |
| `internal/e2e/realclaude/trail_run_outcome_test.go:252-268` | `trailIsRunOutcome` — the exact predicate shape to mirror, and the function AC3/AC4 call. |
| `internal/e2e/realclaude/trail_run_outcome_test.go:1190-1233` | `trailRunOutcomeValues()` + `TestTrailRunOutcomeValuesAgreeWithThePredicate`. **Reuse `trailRunOutcomeValues()` directly** for AC3's second direction — do not hand-copy the eleven. |
| `internal/e2e/realclaude/trail_run_outcome_test.go:1132-1188` | `TestTrailRunOutcomeCarriesNoCapturedBytes` — the shape AC5 names: premise check, `json.Marshal`, `bytes.Contains(trailNeedle)`, then the forbidden-key scan over `map[string]json.RawMessage`. |
| `internal/e2e/realclaude/trail_run_outcome_test.go:186-193, 204-207` | `ArgvScanErrored` (a discriminator, not its text) and `PyryExited`'s safe-zero argument. Both are cited by ACs here. |
| `internal/e2e/realclaude/result_trailer_observation_test.go:296-300` | `trailNeedle` and why it is placed past the byte cap. |
| `internal/e2e/realclaude/result_trailer_observation_test.go:320-372` | `TestTrailConstantsAreClosed` — the empty-string and duplicate-value checks AC1 mirrors, including the zero-value read-back at `:361-371`. |
| `internal/e2e/realclaude/trailer_admissibility_test.go:199-208` | `trailDetail` — `fmt.Sprintf` + `reachCapCommand`'s cap. **Reuse it. Do not mint a `finDetail`.** |
| `internal/e2e/realclaude/trailer_admissibility_test.go:242-270` | `trailGate` — the contract-guard-at-the-top idiom and the house Detail prose style. |
| `internal/e2e/realclaude/trailer_admissibility_test.go:487-512` | `trailIsGateValue` / `trailIsAdmitValue` — the allowlist switch shape. |
| `internal/e2e/realclaude/trailer_admissibility_test.go:516-544` | `trailGateCase` / `trailGateCases()` — the shared-fixture-table idiom the gate test uses so one case list drives several tests. |
| `internal/e2e/realclaude/finding_attribution_fanout_test.go:1-44` | The most recent sibling's file header, and at `:37-44` the settled `trailDetail`-not-`finDetail` argument. Match this header's register. |
| `internal/e2e/realclaude/trail_run_rig_test.go:506-556` | The staged hold command's construction — `t.TempDir()` + `exec.LookPath`. This is the evidence that the *staged* operand is a captured string too (AC5). |
| `internal/e2e/realclaude/sigterm_mid_tool_use_test.go:1486-1504` | `findBashToolUse` returns `(id, index)` and no command. Confirms the issued command must arrive as a supplied string. |
| `internal/e2e/realclaude/background_reach_probe_test.go:945-950` | `reachCapCommand` — the 512-byte cap `trailDetail` wraps. |

---

## Context

`trailClassifyRun` (`trail_run_outcome_test.go:344`) owns one probe run's outcome over a closed set of eleven values. It assumes the run *staged* — that a Bash call was issued, that it was the rig's hold command, that the rendezvous completed. This ticket builds the tier below it: the conditions under which a run never got that far, and the rule that such a run is never handed over.

The hazard is specific and asymmetric. On an unstaged run the argv scan still runs over a healthy process table, parses rows and matches nothing, so `trailClassifyRun` falls past its arms to `trailOutcomeNoRowMatched` (`:129`) — one of its three *answers*, whose own comment says there is "deliberately no 'exited normally' value in this space for it to decay into." That comment is true of a run where a command existed. Published about a run where none ever did, the answer is a false negative wearing an answer's label.

So: two tiers, kept apart. This ticket builds the lower one and the gate that enforces the separation.

---

## Design

### Identifier namespace

Prefix **`finOutcome*`** throughout. Census re-run at `6272256` against a known-taken control:

```
grep -rn '\bfinOutcome[A-Z]' internal/e2e/realclaude/ | wc -l   →  0
grep -rn '\btrail[A-Z]'      internal/e2e/realclaude/ | wc -l   →  880   (control, non-zero)
```

`finDetail` is **not declared anywhere** — one hit, a comment at `finding_attribution_fanout_test.go:37` explaining why it was never created. Reuse `trailDetail`.

> **Do not reach for `finStage*`.** It is the natural name for the input struct and it is **reserved for a sibling ticket** (as are `finRecord*`, `finWrite*`, `finGather*`, `finAttribute*`). The input struct below is `finOutcomeStaging` — prefix `finOutcome`, not `finStage`. Census confirms `finStage[A-Z]` is currently 0, so a collision would compile locally today and break when the sibling merges.

### The seven values

Value strings carry a **`stage-`** sub-namespace, distinct from the eleven's `run-`, for the reason `trail_run_outcome_test.go:108-113` gives for its own third namespace: several spaces now mean nearly the same words, so a copy-paste between them must read as a visible mistake rather than a plausible line.

| Constant | Value | Means |
|---|---|---|
| `finOutcomeNoBashCall` | `stage-no-bash-call` | The model never issued the Bash call. |
| `finOutcomeCommandNotStaged` | `stage-command-not-staged` | A Bash call was issued and it was not the staged hold command. |
| `finOutcomeTriggerDidNotFire` | `stage-trigger-did-not-fire` | The trigger did not fire. |
| `finOutcomeRendezvousIncomplete` | `stage-rendezvous-incomplete` | The rendezvous never completed. |
| `finOutcomePinScanErrored` | `stage-pin-scan-errored` | The during-turn pin scan errored as an instrument. |
| `finOutcomePinCountUnexpected` | `stage-pin-count-unexpected` | The during-turn pin matched a count other than the one expected. |
| `finOutcomeReadyToClassify` | `stage-ready-to-classify` | **Pass-through.** The run staged; its readings are to be classified. |

The pass-through's value is deliberately spelled out rather than left as `""`. Six of the seven are failures; if the pass-through were the zero value, a `finOutcomeResult` nobody filled would read as *"this run staged fine, go classify it"* — the unsafe direction and precisely the collapse this tier exists to prevent. `TestTrailConstantsAreClosed` (`result_trailer_observation_test.go:320-334`) already refuses an empty value in a closed space for the same reason, and `trailRunReadings.PyryExited` (`:204-207`) documents the same discipline for a bool.

**There is no out-of-contract value here, and none is to be added.** Seven is seven. The two inputs that could otherwise want one are closed by the guard conditions in the gate below.

### Records

Two small unexported structs. Neither carries a command string, and neither is a `trailRunReadings`.

```go
// finOutcomeStaging is what the rig knows about whether the run staged. Pure
// conditions: bools, counts, and the two command strings the identity check
// compares. No transcript, no process handle, no *testing.T.
//
// INPUT ONLY — NEVER PUBLISHED. It carries the two captured strings, and it is
// deliberately the one record in this file with NO json tags: it must not be
// embedded in, marshalled into, or quoted by any published record. Only
// finOutcomeResult crosses into publishable space.
type finOutcomeStaging struct {
    BashIssued     bool
    IssuedCommand  string
    StagedCommand  string
    TriggerFired   bool
    RendezvousDone bool
    PinScanErrored bool
    PinMatchCount  int
    PinWantCount   int
}

// finOutcomeResult is the staging tier's decision. Flat, two scalar fields,
// no field from which either command is reachable.
type finOutcomeResult struct {
    Value  string `json:"value"`
    Detail string `json:"detail"`
}
```

`PinScanErrored` mirrors `trailRunReadings.ArgvScanErrored` (`trail_run_outcome_test.go:186-193`): it records *that* the scan failed, never what it said. `ps` stderr is a captured string on the same footing as argv.

**The asymmetry in json tags is load-bearing, not incidental.** `finOutcomeResult` carries tags because it is published; `finOutcomeStaging` carries none because it must not be. Every sibling record in this family states its content rule at the type — `trailRunReadings:173-176`, `trailRunOutcome:221-232`, `trailGateResult:164-179` — and this one states the converse rule for the same reason. A developer adding tags to `finOutcomeStaging` "for symmetry" is taking the first step toward publishing two captured strings into a public issue; the comment exists to make that read as a visible mistake.

**The two commands are compared as opaque bytes.** They are never parsed, split on whitespace, shell-lexed, path-resolved, or executed. The gate answers one question about them — are they the same string — and the purity contract's "no exec" clause is what keeps a later "let me just check the staged binary still exists" from turning an identity check into a filesystem read or a subprocess spawn.

### The gate

```go
// finOutcomeStagingGate decides, from the staging conditions alone, one of the
// six failure outcomes or the pass-through.
func finOutcomeStagingGate(s finOutcomeStaging) finOutcomeResult
```

Same purity contract as `trailGate`, `trailClassifyRun`, `trailAdmitAttribution` and `tdnClassifyReapLog`: no exec, no clock, no filesystem, no `*testing.T`, and it never fails a test. That purity is what lets all seven arms be driven offline from synthetic inputs.

**The structural closure AC4 asks for is the signature itself.** `finOutcomeStagingGate` neither takes nor returns a `trailRunReadings`, and `finOutcomeResult` has no field one is reachable from. A failure arm therefore holds nothing a `trailClassifyRun` call could be made from — the call is not merely discouraged inside the gate, it is unwritable there. This is the "shape the next consumer cannot quietly undo" form, preferred over a comment asking for one. The pass-through's `finOutcomeReadyToClassify` is the signal the caller (#1285) branches on; wiring that caller is out of scope here.

Because the violation is unwritable, the test pins the **observable** consequence instead, per AC4: each of the six failure decisions returns a value `trailIsRunOutcome` rejects.

#### Arm order, and why

Most-upstream-first, so every later arm's precondition holds by construction. The order is load-bearing at two points and both must be preserved:

1. `!BashIssued` → `finOutcomeNoBashCall`. Nothing downstream is meaningful without a call.
2. `IssuedCommand != StagedCommand || StagedCommand == ""` → `finOutcomeCommandNotStaged`.
3. `!TriggerFired` → `finOutcomeTriggerDidNotFire`.
4. `!RendezvousDone` → `finOutcomeRendezvousIncomplete`.
5. `PinScanErrored` → `finOutcomePinScanErrored`.
6. `PinMatchCount != PinWantCount || PinWantCount < 1` → `finOutcomePinCountUnexpected`.
7. → `finOutcomeReadyToClassify`.

**Identity before behaviour (2 before 3 and 4).** The trigger and rendezvous conditions are claims *about the staged command*. If claude issued something else, reporting "the trigger did not fire" is true but files "the model ran the wrong thing" under "our trigger is broken."

**Instrument failure before its result (5 before 6).** `pinScanArgv` returns the **zero** `pinScan` on error (`process_pin_liveness_test.go:191-196`), so an errored scan arrives with `PinMatchCount == 0`. Checking the count first would report "matched an unexpected count" about a scan that never ran — the same defect `trailOutcomeVoidArgvScanErrored` is kept distinct from `trailOutcomeVoidNoRowsParsed` to avoid.

#### The two guard conditions, and the holes they close

These are not decoration. Each closes a reachable input state that would otherwise reach the pass-through:

- **`|| StagedCommand == ""` in arm 2.** Without it, a caller who fills the bools but leaves both command fields empty gets `"" == ""` → equal → **pass-through**. An empty staged command means the rig staged no hold command, so whatever was issued certainly was not it. The clause also absorbs `BashIssued == true` with an empty `IssuedCommand`, which is reachable because `findBashToolUse` answers *whether* a Bash call was issued without yielding its command.
- **`|| PinWantCount < 1` in arm 6.** Without it, an unfilled `PinWantCount` matches an unfilled `PinMatchCount` at zero → **pass-through**. This probe never stages an expectation of zero matches; a want below 1 is an unfilled field, and it must fail rather than pass.

Both are one-condition changes that point the tier's failure the safe way, consistent with the pass-through-is-not-the-zero-value discipline one level up. **Each needs its own test row** (see AC4's scenarios).

### Details: what they may and may not say

Every Detail goes through `trailDetail(format, args...)`, keeping the 512-byte cap single-sourced.

A Detail **may** name: outcome values, the booleans as booleans, `PinMatchCount`, `PinWantCount`.

A Detail **may never** interpolate `IssuedCommand` or `StagedCommand` — in any form, including a length or a prefix. Arm 2's Detail is therefore a **fixed sentence with no interpolation site for either operand**, and should say so in its own comment: neither operand is quoted because both are captured strings — the issued one is verbatim model output, and the staged one embeds a `t.TempDir()` path and an `exec.LookPath` result (`trail_run_rig_test.go:506-556`), which is the same operator-filesystem-path leak class `pinStateColumns` refuses a `command` column for.

Scoping the rule to only the model's string would leave the recipe and the rule disagreeing about the staged one, and the AC5 test below plants the needle in **both** precisely so that a developer resolving that disagreement cannot do it by dropping an operand from the plant set.

### Data flow

```
rig staging observations                     (this ticket)
  │
  ├─ finOutcomeStaging ── finOutcomeStagingGate ──> finOutcomeResult
  │                                                    │
  │                      six failure values ───────────┴──> recorded; STOP.
  │                        (trailIsRunOutcome rejects each)      no readings in
  │                                                              scope to classify
  │
  └─ finOutcomeReadyToClassify ──> caller (#1285) consults ──> trailClassifyRun
                                                                (eleven values,
                                                                 taken as returned)
```

---

## Concurrency model

None. Every symbol here is a pure function or a plain struct, and every test drives synthetic inputs on the calling goroutine. No goroutines, no channels, no timers, no shutdown sequence. `go test -race` passes trivially; the race detector has nothing to observe.

This is deliberate and is the same contract the four sibling deciders hold. The probe's concurrency lives in the rig (`trail_run_rig_test.go`) and the live paths, not in the classifiers.

---

## Error handling

The gate has no error return and never fails a test — an instrument failure observed mid-turn is a datum to publish, not a reason to abort the turn. Failure modes are *values*, not errors:

| Failure mode | Recovery |
|---|---|
| Instrument broke (`PinScanErrored`) | Named as its own outcome, ranked above the count arm so a zero-on-error count cannot be reported as a count. |
| Caller left fields unfilled | Lands on a failure arm by construction: the zero `finOutcomeStaging` has `BashIssued == false` → arm 1; the two guard conditions catch the partially-filled cases. |
| A seventh-and-a-half condition appears later | Add a value **and** the predicate entry **and** the `finOutcomeValues()` entry. The AC2 test goes red if any of the three drifts. |

There is no panic path: nothing here dereferences a pointer, indexes a slice, or reads a map.

---

## Testing strategy

Six tests, all offline, all synthetic. Scenarios, not code — write them in the file's own idiom.

**T1 — the closed space (AC1).** Mirrors `TestTrailConstantsAreClosed`'s `closed` helper (`result_trailer_observation_test.go:326-343`).
- All seven values pairwise distinct; a duplicate names both constants in the failure.
- None is the empty string.
- Explicitly and separately: `finOutcomeReadyToClassify != ""`, with a message stating that a zero meaning "staged, go classify" points the tier's failure the unsafe way.
- Read back off the record: a zero `finOutcomeResult` has a `Value` that is none of the seven.
- Assert the count is exactly 7 — the enumeration is the ticket's own, and a change to it is a change to what the tier can conclude (mirrors `:1216-1219`).

**T2 — the values list agrees with the predicate (AC2).** Mirrors `TestTrailRunOutcomeValuesAgreeWithThePredicate` (`:1214`).
- `finOutcomeValues()` returns the seven as data so coverage loops can range over it.
- Every listed value is accepted by `finOutcomeIsValue`.
- `finOutcomeIsValue("")` is false.

**T3 — the two spaces are disjoint in both directions (AC3).**
- For each of `finOutcomeValues()`: `trailIsRunOutcome` reports false.
- For each of **`trailRunOutcomeValues()`** — call the shipped list, do not hand-copy the eleven — `finOutcomeIsValue` reports false.
- Failure messages should name which space absorbed which value, since a later rename is the drift this catches.

**T4 — the gate drives all seven (AC4).** Table-driven, one shared case list in `trailGateCases()`'s idiom (`trailer_admissibility_test.go:538`) so T5 and T6 can sweep the same fixtures.
- One row reaching each of the seven values; a coverage assertion that all seven were reached, so a row silently retargeted by an edit goes red.
- A row for **each guard hole**: both commands empty with every bool set → must reach `finOutcomeCommandNotStaged`, *not* the pass-through. `PinMatchCount == 0, PinWantCount == 0` with everything else staged → must reach `finOutcomePinCountUnexpected`, *not* the pass-through. These two rows are the point of the guard conditions; without them the guards are untested.
- An order row: `PinScanErrored == true` **and** a mismatched count → must reach `finOutcomePinScanErrored`, pinning arm 5 above arm 6.
- An order row: a wrong issued command **and** `TriggerFired == false` → must reach `finOutcomeCommandNotStaged`, pinning identity before behaviour.
- Every row's `Detail` is non-empty.

**T5 — no failure decision is a run outcome (AC4's observable half).**
- Sweep the six failure rows; assert `trailIsRunOutcome(got.Value)` is false for each.
- The message should state the consequence: "not observed" cannot arrive at `trailOutcomeNoRowMatched`, and an unpinnable command cannot arrive at any value meaning the command had exited.
- Assert the pass-through is *also* rejected by `trailIsRunOutcome` — it is a staging value, not a run outcome, and this is what stops a later edit from aliasing it onto one of the eleven.

**T6 — no captured bytes (AC5).** `TestTrailRunOutcomeCarriesNoCapturedBytes`'s shape (`:1141`).
- Sweep **every** case from T4's list. For each, splice `trailNeedle` into **both** `IssuedCommand` and `StagedCommand` in a way that preserves the arm under test: for arms requiring a match, set both to the *same* needle-bearing string; for `finOutcomeCommandNotStaged`, set them to two *different* needle-bearing strings; for `finOutcomeNoBashCall` the commands are ignored, plant both anyway.
- **Keep every planted command short — well under `reachCapCommand`'s 512-byte cap — and assert it.** This is the difference between a real instrument and a false green. `trailDetail` caps the *formatted* Detail at 512 bytes, so if a developer wrongly interpolated a command into a Detail and the planted command were long enough to push `trailNeedle` past the cap, the cap would truncate the needle away and this test would pass **against a leaking implementation**. `trailNeedle` is placed past the cap *on purpose* in `trailPaddedTrailer` (`result_trailer_observation_test.go:302-313`) for the opposite kind of test; here that placement would be the defect. Mirror `:1143-1151`, whose planted strings run ~73 bytes with the needle comfortably inside the cap, and add a guard assertion that each planted command's length is below `reachMaxCommandBytes` so a later edit cannot lengthen a fixture into vacuity.
- **Premise check first**, per `:1155-1160`: assert the row still reaches its expected value after planting, so the test cannot pass by decaying every row onto one arm.
- `json.Marshal` the result; assert `bytes.Contains(encoded, []byte(trailNeedle))` is false.
- The structural half: unmarshal into `map[string]json.RawMessage` and assert no key contains `command`, `args`, `comm` or `argv`. **This transplant is valid here** — `finOutcomeResult` is flat, two scalar fields, so a top-level key scan examines every key. (Contrast the nested-record hazard: a scan lifted onto a record with a slice-of-struct field would never examine the inner keys.) Neither shipped field is command-shaped, so the check is non-vacuous rather than colliding.
- Do **not** reduce the comparison to a caller-supplied bool. The two commands must reach the gate as strings and be compared there; a bool input would leave this test green by giving it nothing to plant into — a weaker instrument, not a safer gate.

**Mutation-check before commit.** For each of the two guard conditions, temporarily drop the `||` clause and confirm the corresponding T4 row goes red. A guard whose removal leaves the suite green is not tested.

Run: `go test -race -tags e2e_realclaude -run '^TestFinOutcome' -v ./internal/e2e/realclaude/`

---

## Size check

| Red line | Threshold | This ticket |
|---|---|---|
| New files | > 3 | **1** |
| Total LOC | ~600 | **~600–750 projected** |
| New exported types | > 5 | **2**, both unexported |
| Consumer call sites | > 10 | **0** — purely additive |
| Acceptance criteria | > 5 | **5** |
| Reject branches | ≥ 10 | **7** |

The LOC projection is the only marginal signal. The measured record for this exact package and family: the last six single-file tickets shipped 594 / 617 / 713 / 767 / 961 / 1233 lines at `size:s` (#1268, #1266, #1253, #1280, #1270, #1271) with **no `max_turns` salvage on any of them**. Every structural driver of developer turn cost — file count, call-site fan-out, cross-package coordination, branch count — is well inside bounds, and the file is additive with no cascade. Splitting would defend against a failure mode this family has not exhibited. Sized **S**.

The line count here is comment density, not logic: the family's house style gives every constant a five-to-ten line argument. This spec pre-decides every one of those arguments (arm order, both guard conditions, the Detail content rule, the needle-planting scheme) so they are transcription rather than re-derivation.

---

## File-overlap check

`git fetch origin --prune` then a branch-level sweep over `origin/feature/<N>` for files touching `internal/e2e/realclaude/`:

- #1260 → `dropped_line_capture_test.go`, `testdata/dropped_lines_v2.1.220.json`
- #1281 → `finding_run_gather_test.go`
- #363 → `fixtures.go`

No overlap with `finding_staging_gate_test.go`. Since #1281 adds a new file to the same package, identifier collision was checked separately: `fin(Outcome|Stage)[A-Z]` returns zero hits on `origin/feature/1281`, `origin/feature/1260` and `origin/feature/1277`. No block set.

---

## Open questions

1. **Is `PinWantCount` per-run or fixed?** Specced as caller-supplied, because `TestTrailRigCarriesMoreThanOneMatchedRow` (`trail_run_rig_test.go:506`) establishes a live run matching more than one row for a single held command — a shell wrapper and its forked `cat`. If #1285 finds the expectation is in fact constant, collapsing the field is a one-line follow-up; the guard condition `PinWantCount < 1` is written so it stays correct either way.
2. **Does the caller record the failure outcome, or also the staging conditions that produced it?** Out of scope — the publishable record is #1285. This ticket's contract is only that the six failure values are `trailIsRunOutcome`-rejecting and that the pass-through is the sole signal to proceed.
3. **Should `finOutcomeResult` gain a `Reason` field like `trailGateResult`?** Not now. `trailGateResult.Reason` exists to carry a *certified* terminal reason; nothing here certifies anything, and an unused field would be a place for a future edit to put a command string.

---

## Security review

**Verdict:** PASS (first pass FAILED on two MUST FIX findings; both revised inline and the checklist re-run from the top)

**Findings:**

- **[Trust boundaries] MUST FIX — fixed.** The design has one real boundary: `IssuedCommand` is verbatim model output and `StagedCommand`, though rig-authored, embeds an operator filesystem path (`t.TempDir()` + `exec.LookPath`, `trail_run_rig_test.go:506-556`). Both enter through the single named type `finOutcomeStaging`, are read by the single function `finOutcomeStagingGate`, and leave through `finOutcomeResult`, which has no field either is reachable from. **The hole:** the first draft shipped `finOutcomeStaging` with no rule about its own publication, while Open Question 2 explicitly invites #1285 to consider publishing the staging conditions — every sibling record in this family states its content rule at the type (`trailRunReadings:173-176`, `trailRunOutcome:221-232`, `trailGateResult:164-179`) and this one had none. Fixed: the struct is now documented INPUT ONLY — NEVER PUBLISHED, and the json-tag asymmetry (tags on the published record, none on the input record) is called out as load-bearing so adding tags "for symmetry" reads as a visible mistake.
- **[Network & I/O — input size limits] MUST FIX — fixed.** `trailDetail` caps formatted Details at `reachMaxCommandBytes = 512` (`background_reach_probe_test.go:123`). The first draft told the developer to plant `trailNeedle` in both commands without constraining their length. A long planted command would push the needle past that cap, so a Detail that *did* wrongly interpolate a command would be truncated before the needle — and AC5's test would pass **against a leaking implementation**. This is #1278's failure mode: `trailPaddedTrailer` places the needle past the cap deliberately, and copying that placement here inverts the test's meaning. Fixed: planted commands must stay well inside the cap (mirroring `:1143-1151`'s ~73-byte strings) with a guard assertion on their length so a later edit cannot lengthen a fixture into vacuity.
- **[Subprocess / external command execution] SHOULD FIX — fixed.** The struct holds two command strings, which makes "check the staged binary still resolves" an inviting wrong turn. No `exec.Command` and no `sh -c` appear in the design; the spec now states the two commands are compared as opaque bytes and are never parsed, split, shell-lexed, path-resolved or executed, backing the purity contract's no-exec clause with an explicit prohibition.
- **[Tokens, secrets, credentials] No findings.** Nothing is generated, stored, rotated or compared against a secret. The credential surface this package guards — `ps -E`/`-Eww` dumping `CLAUDE_CODE_OAUTH_TOKEN` and `ANTHROPIC_API_KEY` — is structurally absent: the design performs zero process reads, and the pin scan's failure enters as the bool `PinScanErrored` rather than as `ps` stderr, mirroring `trailRunReadings.ArgvScanErrored` (`:186-193`). A credential the model itself echoed into `IssuedCommand` is covered by the input-only rule plus AC5.
- **[File operations] No findings.** No path is constructed, canonicalised, opened, stat'd or written; there is no file to set a mode on and no TOCTOU window. Guaranteed by the purity contract ("no filesystem") shared with `trailGate`, `trailClassifyRun`, `trailAdmitAttribution` and `tdnClassifyReapLog`, and reinforced by the opaque-bytes rule above.
- **[Cryptographic primitives] No findings — and none are wanted.** `crypto/subtle.ConstantTimeCompare` is deliberately *not* used for `IssuedCommand != StagedCommand`: neither operand is a secret being guessed, no attacker occupies a timing-oracle position (this is an offline classifier over two strings already resident in memory), and the outcome publishes as a category rather than as a bit. Reaching for constant-time comparison here would be cargo-cult.
- **[Error messages, logs, telemetry] No findings.** `Detail` is the only message surface and its content rule is enumerated rather than left to judgement: it may name outcome values, booleans and the two counts; it may never interpolate either command in any form, including a length or a prefix. Arm 2's Detail is therefore a fixed sentence with no interpolation site for either operand. No `log/slog` call, no telemetry, no metrics.
- **[Concurrency] No findings.** No goroutines, channels, timers, locks or shared mutable state; every test drives synthetic inputs on the calling goroutine. There is no shutdown path to interrupt and no goroutine whose exit condition could be missed.
- **[Threat model alignment] No findings.** The governing threat for this family is documented in-package rather than in a protocol doc: these records are pasted into public GitHub issues, so a record whose whole value is that it can be published unreviewed must not inherit the operator-review-before-paste obligation (`trail_run_outcome_test.go:1170-1187`; `pinStateColumns` refuses a `command` column at source, `process_pin_liveness_test.go:232`). Addressed: `finOutcomeResult` is two scalar fields, neither command-shaped, enforced by AC5. Threats named as out of scope with their owners — the publishable record and its trailer read (#1285), the artifact writer and its redaction proof (#1286).

**Reviewer:** architect (self-review per `architect/security-review.md`)
**Date:** 2026-08-04
