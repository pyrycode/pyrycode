# #1428 — the presence driver's mutant × row matrix names only live detectors

**Size: xs.** Comments only, in two files, both already-existing test files. No production
source file is touched. The cost of this ticket is the *measurements*, not the prose.

## Files to read first

Symbols, not lines — every one resolves with `codegraph_search` / `codegraph_node`.

- `internal/e2e/realclaude/trailer_admissibility_test.go` → `TestTrailGateNamesThePresenceCaseOnAPathThatOwesNone` — **the whole doc comment is the deliverable.** Read the "# The mutant x row matrix" section, the paragraph after it (the ptyrunner-award mutant), and the "Deferred to #1428" paragraph. Two of the four sites live here.
- `internal/e2e/realclaude/trailer_admissibility_test.go` → `trailGate` — the presence arm is the `against.Value == trailReasonPresentOwesNone` guard returning `trailGatePresentOwesNone`. Four of the six mutants are one-token edits to that guard. The block comment above it states the arm's contract; do not edit it (out of scope) but read it — it is where the ordering arguments already live, so the matrix must not restate them.
- `internal/e2e/realclaude/trailer_admissibility_test.go` → `trailRunnerUnread` — site 3. The dangling pointer is the closing sentence of its "IT IS NO LONGER WHAT A LIVE RUN READS" paragraph.
- `internal/e2e/realclaude/trailer_terminal_reason_test.go` → `trailReasonAgainstPath` — the reduction. Its `finRecordRunnerLabel` switch has the `case "ptyrunner"` arm that mutant **M4** widens, and its fall-through returns `trailReasonPathUnnamed`. Read `trailIsReasonValue` for the closed value set.
- `internal/e2e/realclaude/trail_run_outcome_test.go` → `trailClassifyRun` — site 4. The `readings.Admit.Value == trailAdmitProof` arm returning `trailOutcomeRunningAtTrailer` carries the 68-byte claim; its `decide(...)` format string is what mutant **M6** grows.
- `internal/e2e/realclaude/trail_run_outcome_test.go` → `TestTrailClassifyRun` — the surviving candidate detector. Its per-row loop asserts `strings.Contains(got.Detail, reachTruncationMarker)` — a **truncation** check, not a headroom check. Extract: what bound it actually enforces.
- `internal/e2e/realclaude/trail_run_outcome_test.go` → `trailRunProofReadings` — the fixture that reaches the proof arm; it builds on `trailRunWellFormed`, which supplies the certified reason interpolated into that Detail.
- `internal/e2e/realclaude/trailer_admissibility_test.go` → `TestTrailGateReadsTheRunnerPathOnlyWhereARowDeclaresIt` — the sweep that owns both surviving reds for the ptyrunner-award mutant. Its two relevant sub-tests are the row named `an ordinary trailer is usable and certifies its reason` (from `trailGateCases`) and the sub-test `the usable arm diverts to the presence case under one reading`.
- `internal/e2e/realclaude/background_reach_probe_test.go` → `reachCapCommand`, `reachMaxCommandBytes` — extract the exact truncation predicate: a command of length **exactly** `reachMaxCommandBytes` is returned unchanged; the marker is appended only *past* it. AC4 turns on this boundary.
- `cmd/cite-guard/main.go` → its package doc — the citation rule the corrected prose must satisfy, and (see § Why these four survived) the thing it does **not** check.
- `CODING-STYLE.md` § "Comments — Citing Other Code" — the rule in prose.
- `docs/knowledge/codebase/1434.md` — the shape AC1's measurement record must follow, and its own lesson "A stale byte figure beside a `<=` assertion passes silently", which applies directly to AC4.

## Context

`TestTrailGateNamesThePresenceCaseOnAPathThatOwesNone`'s header is the one surface whose stated
job is to say what the presence driver is worth. Four claims in it are false at `c270cca`, from
two directions:

1. **A deferral that describes a gap the tree does not have.** Its "Deferred to #1428" paragraph
   names three mutants as uncovered. #1434 — the blocker that wrote the deferral — shipped the
   coverage instead, so two of them are dead, and the third is not one mutant but two distinct
   confusions, each caught somewhere different.
2. **Three live comments naming `TestTrailComposesUnderAPtyrunnerReading`, which #1348 deleted**
   along with `trail_ptyrunner_composition_test.go` and the ptyrunner runner it pinned.

The class — a comment naming a symbol that no longer resolves — is exactly what `cite-guard`
does not check. It enforces "name the symbol, not the line"; nothing enforces that the named
symbol exists. That is how all four survived a green build gate. **The class belongs to #1424;
this ticket fixes the four instances.**

Verified for this spec at `de8d289`: the baseline is green offline in 3.35 s with no credentials,
and nothing in the trail family has moved since `c270cca` (the only realclaude change in that
range is `interactive_change_workspace_test.go`, from #1029). The ticket's measured numbers should
therefore still hold — but AC1 requires re-measurement on the branch regardless, and **where a
measurement disagrees with the ticket body, the measurement wins and the disagreement is recorded.**

## Design

There is no new code, no new type, and no interface change. The design is (a) a measurement
protocol, and (b) four comment rewrites whose content is decided by what the protocol measures.

### The measurement protocol

Six mutants and one control, all applied via `go test -overlay` so **no mutation ever reaches the
worktree** — which is also what keeps AC5's comments-only diff honest.

Recipe, per mutant:

1. Copy the target file to the scratchpad and apply the one-token edit there.
2. Write an overlay JSON anywhere outside the repo:
   `{"Replace": {"<abs original path>": "<abs mutated copy>"}}`
3. Run, from the worktree, in a single call:
   `go test -overlay=<abs json> -count=1 -v -tags e2e_realclaude -run '^TestTrail' ./internal/e2e/realclaude/`
4. Record every `--- FAIL:` line, sub-tests included.

`-v` is not optional. Three of these mutants red *inside* `TestTrailGateReadsTheRunnerPathOnlyWhereARowDeclaresIt`,
whose presence and absence companions are sub-tests of the same function — a top-level `FAIL` does
not say which side moved.

**Scope every count to what was run.** `-run '^TestTrail'` is the right filter: the rest of the
realclaude package needs live claude, and running it would mix credential failures into the red
set. So the matrix's counts are counts *within the `TestTrail` family*, and the corrected header
must say so once (in the matrix heading, so no row has to repeat it) rather than implying a
package-wide claim it never measured. Widening the filter is a **non-goal**.

Mutants, each named by the symbol it edits:

| id | where | the edit |
|---|---|---|
| **M1** | `trailGate`, the presence arm's guard | add `&& reason == "idle_stall"` |
| **M2** | same guard | key on the literal alone — drop the `against` consult, keep `_ = against` or the build fails on the unused variable |
| **M3** | same guard | add `\|\| against.Value == trailReasonPathUnnamed` — the indeterminate reading treated as **streamrunner** |
| **M4** | `trailReasonAgainstPath`, the `case "ptyrunner"` arm | widen to `case "ptyrunner", finRecordRunnerIndeterminate` — the indeterminate reading treated as **ptyrunner** |
| **M5** | `trailGate`, the presence arm's guard | add `\|\| against.Value == trailReasonNamedOwesOne` — the new answer awarded on a ptyrunner reading |
| **M6** | `trailClassifyRun`, the `trailAdmitProof` arm | grow that `decide(...)` format string by **exactly 68 bytes** |
| **M6′** | same | grow it by **69 bytes** — the control |

M1–M4 answer AC1 (the deferral's three mutants, with the two indeterminate confusions kept apart
as M3 and M4 rather than collapsed). M5 answers AC3. M6 with its control answers AC4.

**M6′ is mandatory, not a nicety.** A green M6 run and an overlay that silently failed to apply
look identical. M6′ is the control that proves the fixture was reachable — the same rule the
absence-side driver already applies to its own premises. Run M6′ whenever M6 measures green.

### Site 1 — the deferral becomes a fourth matrix section

Delete the "Deferred to #1428" paragraph. No `Deferred to #1428` text may remain anywhere in
`internal/e2e/realclaude`. In its place, state per mutant where it is caught. Two shapes are
allowed and each must be one or the other:

- **caught** — name the test(s) that went red, and nothing else;
- **unkillable on this side** — say so *and* say why, then name where it is caught instead.

The unkillability argument for M3 is structural and belongs in the prose, because it is what makes
the statement checkable: `trailReasonNamedOwesOne` and `trailReasonPathUnnamed` fall through to the
*same* usable return in `trailGate`, so no presence-side row can separate a ptyrunner reading from
an indeterminate one. M4 is the mirror confusion and is caught at the reduction's own driver and on
the absence side — the presence driver and the presence companion both staying green *is* the
finding, and the matrix must not present it as a gap.

M1 and M2 are dead for a reason worth one sentence, because it is what stops the deferral being
re-derived by the next reader: the reason column the deferral asked for already ships —
`trailFixtureTrailer`'s `terminal_reason` is non-empty, is neither `idle_stall` nor `max_turns`,
and is special-cased by no arm, while `trailReasonAgainstPath`'s streamrunner arm never reads
`decodedReason` at all. Verify that sentence against the tree before writing it; do not copy it
from the ticket.

### Site 2 — the ptyrunner-award paragraph

Currently names three detectors: the presence companion, the sweep's usable-row premise, and the
deleted test. The first two are the two sub-tests of `TestTrailGateReadsTheRunnerPathOnlyWhereARowDeclaresIt`
that M5 is expected to redden; only the third is dead. The correction is therefore **not** a
deletion of the claim — AC3 requires the mutant to keep a red-row claim. It is:

- name the two sub-tests, both located inside that one sweep;
- claim no third;
- make the stated count match the M5 run.

If M5 reddens something else as well, name it — a mutant caught in three places is caught in three
places, and this ticket exists because a table that misdescribes its own reds is worse than no
table. AC3's "claims no third" is a prohibition on inheriting the dead name, not a licence to drop
a measured red.

### Site 3 — `trailRunnerUnread`'s dangling pointer

The closing sentence "Full reason: `TestTrailComposesUnderAPtyrunnerReading`'s header." points at
nothing. **Default: delete the sentence.** The paragraph is already self-contained — it states that
since #1452 the caller reduces claude's own argv with `tdnRunnerFromArgv` and hands the answer in,
so a live run reaches whichever case its path earns, and the value stays the answer for
`trailRigGather` and any caller that stages nothing.

A replacement pointer is allowed only if it clears both bars: the symbol resolves in the tree
(`codegraph_search`, then confirm the `func`/`const` declaration), **and** it actually carries the
argument rather than merely mentioning it. `finGatherRunnerPath` is the one candidate worth
checking. If it does not carry the argument, delete and stop — a pointer that costs a reader a
lookup and returns them nothing is the failure mode this ticket is about.

### Site 4 — the 68-byte headroom claim

One honest answer, decided by M6 / M6′, never both branches:

- **If M6 reddens something** — name that assertion, and describe the bound *it* enforces rather
  than the bound the deleted test enforced. They are not the same: the deleted test asserted
  `reachMaxCommandBytes - len(out.Detail) >= len(reading)` (a **margin**), while every surviving
  candidate in `TestTrailClassifyRun` asserts absence of `reachTruncationMarker` (**fits at all**).
  If the two coincide at this arm, say they coincide and say it is arithmetic rather than intent.
- **If M6 stays green and M6′ reddens** — state that #1348 removed the only detector of the margin,
  that what survives bounds only the Detail fitting under the cap, and that nothing enforces the
  68 bytes today. Note the follow-up. **Adding a replacement detector is out of scope for this
  slice** — say so and stop.

Either way the arm's *behaviour* is untouched: the route stays out of the prose because the `Route`
field publishes it without spending cap, and that reason survives the deleted test just fine.

**Unverified arithmetic, offered because the boundary is a trap and the measurement decides.** That
format string is ~435 B before interpolation; with a 9-byte certified reason rendered by `%q` it
lands near 444 B, so a 68-byte spend lands at ~512 — *exactly* the cap, which `reachCapCommand`
returns unchanged. That is why M6 can be green while M6′ at 69 B is red, and why a green M6 must
never be reported without its control. Do not carry these numbers into a comment; if the corrected
prose states a byte figure at all, it must be one this branch measured — `1434.md`'s own lesson is
that a stale byte figure beside a `<=` assertion passes silently, and three sites carried a wrong
one for exactly that reason.

### What the corrected prose must satisfy

- **Name symbols, never `file.go:NNN`.** `cite-guard` runs inside `make check` and fails the build
  on a citation resolving to a declaration, at any depth. It walks the tree, so build-tagged files
  are covered.
- Test names are written bare (`TestTrailGate…`), sub-tests by their `t.Run` name. Every one must be
  a `func Test…` declared in the package, or a sub-test of one.
- Do not touch the file-cite strings inside shipped `Detail` format strings
  (`streamrunner/runner.go:177-179`, `runner.go:479-485`, …). Those are string literals, not
  comments — editing one breaks AC5's comments-only claim and moves an assertion's subject.

## Concurrency model

None. `trailGate`, `trailReasonAgainstPath` and `trailClassifyRun` are pure over their inputs — no
exec, no clock, no filesystem — which is what lets every mutant be measured offline with no live
turn. The only concurrency fact that bears on the work is why the fixtures in this family are
functions rather than package-level vars: `go test -race` runs these tests in parallel and a shared
backing array would let one row's mutation reach another's. Nothing in this ticket adds a fixture,
so nothing new is exposed to it.

## Error handling

The failure mode this ticket guards against is a *measurement that did not measure*:

- A run reporting no reds is only evidence once a control shows the fixture is reachable. M6′ is
  that control for AC4; for M3 and M4 the reds elsewhere in the same run are self-proving.
- `go test -overlay` fails loudly on a malformed JSON path — a run that compiles and passes without
  the mutant present is the silent case. If a mutant's red set is empty and unexplained, re-check
  that the overlay path is absolute and that the copy actually carries the edit before writing
  "unkillable".
- M2 will not build without `_ = against`. A build failure is not a red row; it is a mis-applied
  mutant.
- If a measured red set disagrees with the ticket body's table, the branch measurement wins. Record
  both and say which run produced which.

## Testing strategy

No test is added, removed or renamed — AC5 forbids it. Verification is:

- `go test -count=1 -tags e2e_realclaude -run '^TestTrail' ./internal/e2e/realclaude/` — green.
  **This is also the only compile gate for these files**: `make check`'s unit tier does not build
  anything behind the `e2e_realclaude` tag, so a green `make check` is not evidence this package
  compiles.
- `make check` — green, and it is the point of AC5 rather than a formality: it runs `cite-guard`,
  the gate this entire class of comment slips past.
- `gofmt -l` clean on the two touched files.
- `rg -n TestTrailComposesUnderAPtyrunnerReading internal/e2e/realclaude/` returns nothing.
- Every test name in the four corrected sites resolves: grep each for its `func Test…` declaration
  (or its `t.Run` string). This is the check `cite-guard` does not do, so it is done by hand here.
- `git diff -U0` over the two `.go` files shows every touched line is a comment line: no assertion,
  no fixture, no constant, no arm, no `func` declaration added, removed or renamed. Show the output.

## Deliverables

1. `internal/e2e/realclaude/trailer_admissibility_test.go` — sites 1, 2, 3 (comments only).
2. `internal/e2e/realclaude/trail_run_outcome_test.go` — site 4 (comments only).
3. `docs/knowledge/codebase/1428.md` — **the measurement log only**, in `1434.md`'s shape: one entry
   per mutant giving the exact command, the edit applied, and the recorded `--- FAIL:` lines.

**Note on deliverable 3, deliberately against the usual rule.** The developer normally writes no
`docs/knowledge/codebase/<N>.md` — the documentation phase owns it and writes it after merge. Here
AC1 makes the measurement record load-bearing acceptance evidence, and documentation cannot produce
it, because it does not re-run the overlays. So the developer writes the measurement log **and
nothing else** into that file; documentation extends the file after merge rather than creating it.
The comments-only demonstration (AC5) is a claim about the two `.go` files; this added markdown file
is outside it.

## Open questions

- **Does M6 red anything at all?** Genuinely unmeasured — this is the one number the ticket owes
  rather than inherits, and the arithmetic above says the outcome sits on a one-byte boundary. Both
  branches of site 4 are specified; the run picks.
- **Does anything outside the `TestTrail` family detect these mutants?** Not measured, and
  deliberately not: the rest of the realclaude package needs live claude. The corrected prose states
  its filter so the claim is true as scoped rather than silently package-wide.
- **Site 3's replacement pointer.** Whether `finGatherRunnerPath` carries the full argument is a
  read the developer makes; deletion is the specified default and needs no justification.

## Scope self-check

Production source files (`*.go` excluding `*_test.go`) created or modified: **0**. Test files
touched: 2, comments only. New files: 1 markdown. New exported types: 0. Consumer call sites: 0.
Reject branches: 0. The cost centre is 7 overlay runs, each one scripted command plus a `--- FAIL:`
extraction. Well inside `xs`.
