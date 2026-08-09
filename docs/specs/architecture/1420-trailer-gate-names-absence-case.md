# #1420 — The trailer gate names which absence case fired

**Size:** S (upper end). One package, one primary file, three comment-only sibling edits, no new
exported symbols, no signature change, no call-site migration.

**Blocked by:** #1419 (merged `ee6fe0f`). Every measurement in this spec was taken at `ee6fe0f`
on 2026-08-09 via `go test -overlay` with no worktree writes.

---

## Files to read first

| Path | What to extract |
|---|---|
| `internal/e2e/realclaude/trailer_admissibility_test.go:370-412` | The `reason == ""` block. `:389-401` is the absence arm you split three ways; `:402-411` is the present-and-empty arm you must not touch. `:384-388` is a claim inside this block that this ticket falsifies. |
| `internal/e2e/realclaude/trailer_terminal_reason_test.go:215-272` | `trailReasonAgainstPath` — signature, the `present` computation at `:216`, the three absence answers, and the fall-through shape at `:265-271` you will mirror. |
| `internal/e2e/realclaude/trailer_terminal_reason_test.go:201-214` | The structural no-echo guarantee that makes embedding its Detail safe. Cite this, do not re-argue it. |
| `internal/e2e/realclaude/trailer_terminal_reason_test.go:80-126` | The six `trailReason*` constants. The three you switch on, and the exact strings the new test asserts. |
| `internal/e2e/realclaude/trailer_admissibility_test.go:959-1028` | `TestTrailGate`'s "out-of-contract details name their own sub-case" subtest. `:991` and `:1004` are re-pointed; `:979`, `:983`, `:998`, `:1009`, `:1014` must keep passing unweakened. |
| `internal/e2e/realclaude/trailer_admissibility_test.go:1031-1166` | `trailGateRunnerReadings()` and `TestTrailGateIgnoresTheRunnerPath` — the five readings, clause A, the four-field pin, clause B, and the two comparisons AC2 re-scopes. |
| `internal/e2e/realclaude/trailer_admissibility_test.go:676-796` | `trailGateAbsentReasonScan()`, `trailGateEmptyReasonScan()`, `trailRunnerUnread()`, and `trailGateCases()`' nine rows incl. row nine at `:781-794`. |
| `internal/e2e/realclaude/trailer_admissibility_test.go:1448-1514` | The key-name sweep. Its `marker` at `:1466` pins the literal phrase your rewritten prose must retain. |
| `internal/e2e/realclaude/background_reach_probe_test.go:123-124, :945-950` | `reachMaxCommandBytes = 512`, `reachTruncationMarker` (29 B), and `reachCapCommand`'s truncate-and-mark behaviour — the basis of the headroom assertion. |
| `internal/e2e/realclaude/trail_run_outcome_test.go:366-390` | `trailClassifyRun`'s C1 and C2. Read once to confirm they key on `Value` and the certify-iff-reason invariant only, then leave them alone. |
| `internal/e2e/realclaude/trail_ptyrunner_composition_test.go:28-34` | The **tenth** doc site, outside AC3's file scope, whose claim this ticket falsifies. See § Scope note. |
| `internal/e2e/realclaude/finding_run_gather_test.go:546-551`, `trail_run_rig_test.go:157-161` | The two gather supply comments. Attribution-only edits. |

---

## Context

`trailGate`'s absence arm (`:389-401`) collapses three situations under one Detail: the field absent
from a path that owes one, absent from a path that owes none, and absent from a path naming no
runner. All three still answer `trailGateOutOfContract` and still certify nothing — what changes is
that the published record says which case fired.

`trailReasonAgainstPath` is the shipped reduction and has no decision-path caller. This ticket is
where that stops.

---

## Design

### D1. The gate calls the predicate on the absence branch and has exactly ten return sites

Inside the existing `reason == ""` block, on the branch where `trailReasonKeyName` is **not** in
`in.Scan.KeyNames`:

```go
against := trailReasonAgainstPath(in.RunnerPath, in.Scan.KeyNames, reason)
switch against.Value {
case trailReasonAbsentOwesOne:
    return trailGateResult{Value: trailGateOutOfContract, Detail: /* D2 */, RunnerPath: in.RunnerPath}
case trailReasonAbsentOwesNone:
    return trailGateResult{Value: trailGateOutOfContract, Detail: /* D2 */, RunnerPath: in.RunnerPath}
}
// trailReasonPathUnnamed — the fall-through, NOT a default guard. See D1.2.
return trailGateResult{Value: trailGateOutOfContract, Detail: /* D2 */, RunnerPath: in.RunnerPath}
```

**D1.1 — presence still comes from the gate's own key-name read.** The enclosing
`!slices.Contains(in.Scan.KeyNames, trailReasonKeyName)` at `:389` stays exactly as #1419 landed it.
Do not take presence from `against.Value`, and never from `decodedReason != ""`. On an
indeterminate reading `trailReasonAgainstPath` reaches its default *before consulting presence at
all* (`:216` computes it, `:265-271` never reads it), so absent and present-and-empty both answer
`trailReasonPathUnnamed` — a gate that sourced presence there could not tell them apart on exactly
the reading both shipped gathers supply.

**D1.2 — the path-unnamed case is the fall-through, and an unreachable `default` guard is
forbidden.** The reachable answer set here is exactly the three absence values, because `present` is
false by the enclosing branch. Adding a fourth `default` return site for "some value outside the
three" would create a return site **no fixture row can reach**, which breaks clause B's totality
claim in `TestTrailGateIgnoresTheRunnerPath` — the claim would have to weaken from "the rows reach
all N" to "reach most of". That is the structural reason the count is ten and not eleven. State the
by-construction argument in the comment above the switch.

**D1.3 — one return site would have been simpler and is rejected.** All three cases produce the same
`Value` and the same empty `Reason`, so a single site interpolating `against.Detail` would compile
and pass. It is rejected because AC2's totality argument is built on the absence arm being three
sites that each must echo `RunnerPath: in.RunnerPath`, and because each case gets its own byte
budget (D2). Do not "simplify" this back to one site.

**Re-derived return-site count:** eight today (`:323`, `:336`, `:345`, `:360`, `:390`, `:402`,
`:415`, `:427`), four of them `trailGateOutOfContract`. After: **ten**, **six** of them
`trailGateOutOfContract`.

### D2. The Detail is REWRITTEN to fit, per case, and never appended to

Measured at `ee6fe0f`:

| string | bytes | gate's remaining budget |
|---|---|---|
| `reachMaxCommandBytes` | 512 | — |
| shipped absence Detail | 480 | 32 B of headroom — appending overflows |
| `trailReasonAbsentOwesOne`'s Detail | 264 | **248** |
| `trailReasonAbsentOwesNone`'s Detail | 269 | **243** |
| `trailReasonPathUnnamed`'s Detail | 286 | **226** |

The shipped 480 B contains ~250 B explaining the streamrunner path. The embedded owes-none Detail
now supplies that explanation *per case and correctly*, so cutting it from the gate's prose is a
correction, not a loss. The `NOT DECIDED AT THIS ARM` tail goes away — the arm decides.

Constraints on the rewritten prose, each with a test that enforces it:

- **MUST retain the literal `NO terminal_reason key on the line`** — pinned by `:998` and by the
  key-name sweep's `marker` at `:1466`.
- **MUST NOT contain `terminal_reason is empty`** (`:1009`) or `NO LIVE REPRO EXISTS` (`:1014`).
  Verified: none of the three embedded Details carries either phrase, so both stay green.
- **MUST NOT contain `NOT DECIDED AT THIS ARM`** — retired.
- **MUST pass the embedded Detail as a `fmt` ARGUMENT**, never concatenated into the format string.
  `trailDetail` is `fmt.Sprintf` + cap; a `%` reaching the format string would be interpreted.
- Each case's prose says what its case means for interpretation. It must **not** say whether the
  absence is acceptable — that is #1417's and #1369's, and this arm stays silent about it. Do not
  name either ticket.

A skeleton at ~160 B leaves ~66 B for case-specific wording inside the tightest (226 B) budget:
`state %s carries a trailer with NO terminal_reason key on the line and nothing is certified. WHICH
absence, decided against the observed runner path: %s`.

### D3. `trailGateCases()` keeps its nine rows; the per-case proof gets its own driver

Adding path-varying rows to that slice would land them inside `TestTrailGateIgnoresTheRunnerPath`,
`TestTrailGateThenAdmit` and `TestTrailRunComposesWithGateCases` at once. Keep nine rows and add a
new top-level test. This is also what keeps the row counts at `:667`, `:723`, `:1074` and `:1077`
unchanged under re-derivation.

`trailGateCases()`' doc premise ("every row carries the same runner path") stays **true**; only its
*rationale* is falsified. Rewrite the reason: the gate now does make a path distinction, and the
slice holds the path fixed so the three sweeps that consume it stay comparable.

### D4. `TestTrailGateIgnoresTheRunnerPath` — narrowed claim, scoped exemption, companion proof

**Name and claim.** The test, its doc (`:1058-1097`) and its failure text (`:1153`) stop saying no
arm reads the path and state the narrower truth: *every arm except the absence one ignores it.*
Rename to reflect that (e.g. `TestTrailGateIgnoresTheRunnerPathExceptAtTheAbsenceArm`).

**How the exemption is keyed.** Add a `pathVaries bool` field to `trailGateCase` and set it on row
nine only. The row declares that its decision depends on the reading; a later absence-shaped row
that forgets to declare it goes red. Do not re-derive the gate's branch condition inside the test,
and do not detect the exemption from the Details differing — that is circular. `trailGateCase` has
no field-count pin (the `:1115` pin is on `trailGateResult`), so a fifth field is free.

**Scope of the exemption — narrower than AC2 permits, deliberately:**

- **Clause A** (`:1101-1110`, pairwise distinct readings) — unchanged, still runs first. It is what
  keeps the companion from proving variance across fewer labels than it claims.
- **The four-field pin** (`:1115-1119`) — unchanged.
- **Clause B** (`:1131-1136`) — runs **unconditionally, on every row under every reading**. This is
  the trap AC2 exists to prevent: skipping the row wholesale would withdraw clause B from three new
  return sites.
- **The value/`Reason` comparison** (`:1151-1156`) — **keep it running on `pathVaries` rows too.**
  All three cases answer `trailGateOutOfContract` with empty `Reason`, so it is green and strictly
  stronger than exempting it. State this in the doc comment so a reviewer reads it as deliberate
  rather than as under-delivery against AC2's "scoped to the value/`Reason` and `Detail`
  comparisons".
- **The `Detail` byte comparison** (`:1157-1162`) — the only thing skipped, and only when
  `tc.pathVaries`.

**Companion assertion** (a final `t.Run` in the same test). Drive `trailGateAbsentReasonScan()` —
row nine's own fixture helper, not an index into the slice — under all five
`trailGateRunnerReadings()` and assert:

- reading 0 → the Detail carries `trailReasonAbsentOwesOne`
- reading 1 → carries `trailReasonAbsentOwesNone`
- readings 2, 3, 4 → carry `trailReasonPathUnnamed`
- on every reading: `Value == trailGateOutOfContract` and `Reason == ""`
- the set of distinct Details across the five has cardinality **exactly 3**, so a degenerate arm
  cannot satisfy the markers vacuously

**Totality claim** (`:1079-1080`), re-derived: **ten** return sites; the nine rows reach all ten
**only when driven across the five readings** — at any single reading they reach **eight**, because
"a state nobody defined" and "the zero scan result" share one return site and the absence row
reaches exactly one of the three absence sites.

### D5. The new test — four cases, four enumerated mutants

`TestTrailGateNamesWhichAbsenceCaseFired`. Rows (scenarios, not code):

| Row | Input | Reading | Expected case |
|---|---|---|---|
| R1 | `trailGateAbsentReasonScan()` | `tdnRunnerFromArgv(tdnFixturePtyArgv)` | `trailReasonAbsentOwesOne` |
| R2 | `trailGateAbsentReasonScan()` | `tdnRunnerFromArgv(tdnFixtureStreamArgv)` | `trailReasonAbsentOwesNone` |
| R3 | `trailGateAbsentReasonScan()` | `trailRunnerUnread()` | `trailReasonPathUnnamed` |
| R4 | `trailGateEmptyReasonScan()` | `trailRunnerUnread()` | present-and-empty arm, path-invariant |

Per row, assert:

- `Value == trailGateOutOfContract` and `Reason == ""` (the non-vacuity precondition — six arms
  answer that value, so the marker check below is what says which one ran).
- The Detail carries the row's own case constant **and neither of the other two**. Mutual exclusion
  is what makes a swap of two arms red rather than merely "the Details differ".
- **Assert on the FULL constant, never a fragment.** `"one-path"` IS a substring of `"none-path"`,
  so a fragment assertion silently passes across the owes-one / owes-none swap. The full constants
  are not substrings of each other — verified.
- R4 additionally: the Detail carries none of the three case markers, and still carries
  `terminal_reason is empty` and `NO LIVE REPRO EXISTS`.
- **Headroom, on the OUTPUT, per row:** `len(Detail) <= reachMaxCommandBytes` **and**
  `!strings.Contains(Detail, reachTruncationMarker)`. The second is the direct tripwire — a
  truncated Detail is 541 B and carries that marker. The failure message reports the length and the
  remaining margin, so a later reword that overflows is red here rather than shipping a severed
  sentence whose surviving fragment is exactly the marker being asserted on.

**Mutant × row matrix** — enumerate in the test's doc comment, verify each with `go test -overlay`:

| Mutant (in `trailReasonAgainstPath`) | Sole red row | Wrong value produced |
|---|---|---|
| M1 indeterminate treated as `streamrunner` | R3 | `trailReasonAbsentOwesNone` |
| M2 indeterminate treated as `ptyrunner` | R3 | `trailReasonAbsentOwesOne` |
| M3 `ptyrunner` reading treated as `streamrunner` | R1 | `trailReasonAbsentOwesNone` |
| M4 `streamrunner` reading treated as `ptyrunner` | R2 | `trailReasonAbsentOwesOne` |

M1 and M2 **share R3** and no row separates them — they are told apart by the *wrong value each
produces*. The assertion and its failure message must therefore name got-vs-want, never
"is / is not the path-unnamed case", or that distinction is lost in the output. R1 is M3's only
evidence; R2 is M4's only evidence.

Overlay recipe (no worktree writes): copy the file to the scratchpad, apply one mutation, write
`{"Replace":{"<abs worktree path>":"<abs scratchpad path>"}}`, then
`go test -count=1 -tags e2e_realclaude -overlay=<json> -run '<Test>' ./internal/e2e/realclaude/`.

### D6. Re-pointing the two assertions keyed on the retired phrase

Both are in `TestTrailGate`'s subtest and neither may be deleted.

- **`:1004`** asserts the absence Detail carries `NOT DECIDED AT THIS ARM`. Its input carries
  `trailRunnerUnread()`, which reduces to `indeterminate` and therefore reaches the **path-unnamed**
  case. Re-point it to assert the Detail names that case (`trailReasonPathUnnamed`). It inherits the
  tail-phrase role the old marker held.
- **`:991`** asserts the present-and-empty Detail does **not** carry that phrase, and goes
  green-and-vacuous the moment the phrase leaves the file. Re-point it to assert the
  present-and-empty Detail carries **none of the three case markers**, keeping the mutual-exclusion
  check it exists to make.
- `:960`'s count re-derives: still four inputs, now **six** arms answer `trailGateOutOfContract`;
  this subtest covers four of the six and the new test covers the other two.

### D7. Comment corrections — the nine blocks, re-derived counts, and a tenth site

| Site | What is false / must move |
|---|---|
| `:123-129` | "four sub-cases" / "Four rather than three since #1419" → **six**. |
| `:194-200` | Heading "# No arm reads it, in this slice"; the `TestTrailGateIgnoresTheRunnerPath` proof claim; names #1374. |
| `:230-233` | "copied out unread. No arm consults it". |
| `:242-248` | **Load-bearing.** The safety argument ("Because no arm reads the field, an unfilled one cannot misroute a decision") is retired. Replace it with the answer it deferred to #1374: **verified — `finRecordRunnerLabel("")` returns `""`, which matches neither runner, so an unfilled reading routes to `trailReasonPathUnnamed`.** |
| `:305-318` | The whole "# The runner path is CARRIED, never read" section; "the **eight** arms" → ten; the byte-identical-across-five claim; names #1374. |
| `:384-388` | "Neither reads `in.RunnerPath`, and neither Detail interpolates a key name" — directly falsified, and easy to miss because it reads as a note about #1419's two arms. |
| `:714-723` | See D3 — premise stays true, rationale is rewritten. |
| `:781-794` | Row nine's comment; declares `pathVaries`; its clause-B totality argument is re-derived. |
| `:1058-1097` + `:1153` | See D4. |

**Count literals — re-derive each against the function you actually wrote, never adjust by one.**

| Site | Today | After | Note |
|---|---|---|---|
| `:125` | four sub-cases | **six** | |
| `:307` | the eight arms | **ten** | |
| `:786` | "the seven that carried it" | **seven** | 10 − 3 absence sites = 7, same as 8 − 1 today. **The numeral does not move; the surrounding prose does** ("the single return site" → three). AC3 says `:786` moves — read that as *is re-derived*. |
| `:960` | Four inputs / four arms | four inputs / **six** arms | |
| `:1079-1080` | eight return sites, nine rows reach all eight | **ten**, reached only across five readings; eight at any single reading | |
| `:667`, `:723`, `:1074`, `:1077` | nine rows / forty-five | **unchanged** | Re-derived: 9 rows × 5 readings = 45. Confirmed, not assumed. |
| `:824` | "its nine rows REACH is exactly the six" | re-derive | Included in AC3's numeral sweep. |

**#1374 is closed NOT_PLANNED.** Correct its attribution at `:197`, `:247`, `:318`, `:640`, `:669`,
`:1060`, plus `finding_run_gather_test.go:546-549` and `trail_run_rig_test.go:157-159`. The four
supply comments keep their content — supply stays **unowned**, not re-filed. No new ownership, no
behaviour change.

**AC3's closing sweep.** Search the file for `no arm`, `never read`, `unread`, `carried`, and the
arm-count numerals, and confirm no surviving assertion. Note `:135` ("ALLOWLIST of seven") and
`:451` ("an eighth outcome") are about the *admit* value space, not the gate — they are correct and
stay.

### Scope note — a tenth site, in a different file

`trail_ptyrunner_composition_test.go:30-34` states *"No arm reads the runner path, so the
composition cannot vary by it — which is the property being pinned rather than a gap."* AC3 scopes
to `trailer_admissibility_test.go`, so this is outside its letter — but the claim is **falsified by
this ticket**, and AC5 forbids an overclaiming comment. Narrow it: that test drives
`trailFixtureTrailer`, which reaches `trailGateUsable`, an arm that still ignores the path — so the
composition it pins genuinely cannot vary by it, but the general claim is gone.

Correct **only that block**. Its cites `(:963)`, `(:314)` and `(:996-1000)` inside the same
paragraph went stale under #1419's 248-line insertion; re-take them while editing. Other stale cites
elsewhere in that file are pre-existing and **out of scope** — do not sweep them.

### What must not move

- The present-and-empty arm (`:402-411`), its path-invariance, and its scoped `NO LIVE REPRO EXISTS`
  claim. Not reopened.
- `trailGateEmptyReasonScan()` keeps reaching it.
- `trailClassifyRun`'s C1 (`trail_run_outcome_test.go:369-375`) and C2 (`:377-388`) — verified to key
  on `Value` and the certify-iff-reason invariant only. No amendment.
- No closed set grows. No consumer gains an arm.
- No comment may claim the gate decides against the path a **live** run took: both shipped gathers
  supply `trailRunnerUnread()`, so a live run today reaches only the path-unnamed case. Say that the
  gate is correct about which case fired *when the path is known*, and that the shipped gathers do
  not know it.

---

## Error handling

`trailGate` remains pure over its input: no exec, no clock, no filesystem, no `*testing.T`, and it
never fails a test. It returns a value on every input, including ones its producer cannot emit. The
new branch adds no failure mode: `trailReasonAgainstPath` returns no error and cannot fail — an
unrecognised label is a fully-answered case, and a nil `keyNames` is absence rather than an error.

---

## Testing strategy

Offline. No live claude, no credentials, no `t.Skip`, no env gate.

```
go test -count=1 -race -tags e2e_realclaude -run '^TestTrail' ./internal/e2e/realclaude/
```

Then the full hermetic gate: `make check`. Note `go vet` and `staticcheck` run **without**
`-tags e2e_realclaude`, so neither analyses this file — the compile check comes from the tagged test
run above.

Must stay green unamended: `TestTrailAdmissibilityRecordsCarryNoCapturedBytes` (`:1395`, all three
sub-tests including the key-name rungs at `:1448`), the outcome-side sweep at
`trail_run_outcome_test.go:1142`, `TestTrailGateThenAdmit`, `TestTrailRunComposesWithGateCases`, and
`TestTrailComposesUnderAPtyrunnerReading`.

**Suggested order** (front-loads the byte-budget risk, which is the one thing that can force a
rewrite late):

1. Split the arm with placeholder prose; get the tagged run green.
2. Write the new test with the headroom assertions; iterate the prose against them until each case
   fits. Do this **before** the comment sweep — a prose rewrite invalidates comment work.
3. Re-scope the sweep + companion assertion.
4. Re-point `:991` / `:1004` / `:960`.
5. Verify M1–M4 under overlay.
6. Comment corrections, count re-derivation, and AC3's closing sweep last.

---

## Open questions

None blocking. Two judgement calls are made in this spec rather than left open: the per-case proof
lives in a new test rather than in new `trailGateCases()` rows (D3), and the exemption skips the
`Detail` comparison only rather than also the value/`Reason` one (D4).

---

## Security review

**Verdict:** PASS

**Findings:**

- **[Trust boundaries]** No MUST FIX. The untrusted input is claude's own `result` line. This ticket
  moves **one** new value across a boundary: `in.Scan.KeyNames` (names read off that line, unbounded
  at their tier per `trailer_key_names_test.go:55-59`) is now passed into `trailReasonAgainstPath`.
  That function is a sink, not a relay — every arm's Detail is fixed prose over its own file's
  constants and file cites, never a key name, never the decoded scalar, never the reading
  (`trailer_terminal_reason_test.go:201-214`, enforced by its own needle sweep at `:628`, `:640`).
  Verified empirically: none of the three absence Details contains any input-derived byte. The gate
  passes `decodedReason` as `""` on this branch by construction.
- **[Error messages, logs, telemetry]** No MUST FIX — this is the category that matters here, since
  `trailGateResult` is **published unreviewed** into a public issue artifact. Two live risks, both
  covered: (a) a Detail acquiring a key name — the existing sweep at `:1448` drives both #1419 arms
  with `trailNeedle` planted **as a key name** and asserts over the marshalled record; the absence
  row there reaches the new path-unnamed site, so it keeps covering the rewritten prose. (b) a
  Detail acquiring the runner reading — `trailGateResult.RunnerPath` carries it by design and
  clause B requires it intact, so a whole-record sweep would be red against a correct build; the
  Detail-only sweep in `trail_ptyrunner_composition_test.go` is the correct rung and is unaffected
  because the gate interpolates `against.Detail`, never `in.RunnerPath`.
- **[Network & I/O — input size limits]** SHOULD FIX, and D2 fixes it. The composed Detail is the
  one new unbounded-ish surface. `reachCapCommand` caps at 512 B, but capping is not the same as
  fitting: appending 264–286 B to a 480 B string truncates, and the truncation lands 1–5 B **past**
  the value marker, so the natural assertion passes against a severed sentence. The mitigation is
  mandatory and in D5: assert `len <= reachMaxCommandBytes` **and** absence of
  `reachTruncationMarker`, per case, on the output.
- **[Subprocess / external command execution]** N/A by design — `trailGate` and
  `trailReasonAgainstPath` are pure: no exec, no clock, no filesystem. The runner reading arrives
  already reduced to one of `tdnRunnerFromArgv`'s five constant answers; **verbatim argv never
  reaches this tier** (`finding_run_record_test.go:235-238`), and this ticket adds no argv reader.
- **[Cryptographic primitives]**, **[Tokens/secrets]**, **[File operations]** — N/A. No randomness,
  no credentials, no filesystem path is constructed, read, or written anywhere in this change.
- **[Concurrency]** No findings. `trailGate` takes no locks and spawns no goroutine. One inherited
  hazard is unchanged: `TestTrailGateIgnoresTheRunnerPath` copies each `trailGateInput` per reading
  and a struct copy copies the `*resultTrailer`, so all five share one trailer (`:1090-1097`). The
  new companion assertion drives `trailGateAbsentReasonScan()` and must likewise **assign only
  `RunnerPath` and never write through the pointer**, which is what keeps `go test -race` clean and
  what would become load-bearing if these subtests ever took `t.Parallel()`.
- **[Threat model alignment]** In scope and addressed: the published-artifact threat that
  `reachMaxCommandBytes` exists for (`background_reach_probe_test.go:118-124` — attacker-chosen text
  reaching an artifact an operator pastes into a public issue) is the one this change touches, via
  the Detail. Out of scope and named: whether an absence on a path that owes none should be
  *admitted* rather than merely *named* is **#1417**; a trailer that carries a `terminal_reason` on
  a path that owes none is **#1369**. This arm stays silent on both. Supplying a live runner reading
  to the gate stays **unowned** — the recorded route, when an arm first needs it, is to pass the
  already-reduced reading from the live driver's `h.Pin.ClaudeCommand`.

**Reviewer:** architect (self-review per `architect/security-review.md`)
**Date:** 2026-08-09
