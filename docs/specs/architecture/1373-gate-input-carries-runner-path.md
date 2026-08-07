# #1373 — The trailer gate's input carries a runner-path reading no arm reads

**Ticket:** [#1373](https://github.com/pyrycode/pyrycode/issues/1373) · **Size:** S · **Labels:** `size:s`, `security-sensitive`

Everything this slice touches is under the `e2e_realclaude` build tag in `internal/e2e/realclaude/`, and every file is a `*_test.go`. **Zero production source files change.** The whole slice runs offline:

```
go test -tags e2e_realclaude -run '^(TestTrail|TestFin|TestTdn)' ./internal/e2e/realclaude/
```

---

## Files to read first

Read these before writing anything. Each entry says what to extract; between them they are the whole surface this slice touches.

| Path | What to extract |
|---|---|
| `internal/e2e/realclaude/trailer_admissibility_test.go:164-188` | `trailGateResult`'s doc + the three fields. The doc's rule *"a future field added here must not be a copy of an input's captured bytes"* is the one this slice has to answer to. |
| `internal/e2e/realclaude/trailer_admissibility_test.go:210-322` | `trailGate`'s doc and its **seven** `return trailGateResult{…}` sites (`:246`, `:258`, `:266`, `:280`, `:291`, `:303`, `:314`). Every one of the seven gets one new line. |
| `internal/e2e/realclaude/trailer_admissibility_test.go:516-593` | `trailGateCase` (four fields: `name`, `in`, `want`, `reason`) and the eight rows of `trailGateCases()`. `in` is the field being retyped; the eight rows are one contiguous 50-line block. |
| `internal/e2e/realclaude/trailer_admissibility_test.go:727-781` | `TestTrailGate` and, at `:756-780`, AC3's sub-test — the three call sites at `:760`, `:765`, `:775` that must keep asserting `"nil trailer"` / `"terminal_reason is empty"` / `"NO LIVE REPRO EXISTS"` / the quoted state. |
| `internal/e2e/realclaude/trailer_admissibility_test.go:984-1039` | `TestTrailAdmissibilityRecordsCarryNoCapturedBytes`. The `bytes` and `encoding/json` imports it already uses are the ones the new test reuses — **no new imports for the sweep**. |
| `internal/e2e/realclaude/teardown_liveness_probe_test.go:757-790` | `tdnRunnerFromArgv` — the shipped reader. Read all **five** returned strings and the doc's reason for refusing `reachRunnerPathFromArgv`. |
| `internal/e2e/realclaude/teardown_liveness_probe_test.go:891-962` | `tdnFixturePtyArgv` (`:897`), `tdnFixtureStreamArgv` (`:902`) and `TestTdnRunnerFromArgv`'s six rows — including the `--append-system-prompt-file`-only argv at `:929`. Four of the five driving argvs come straight from here. |
| `internal/e2e/realclaude/trailer_terminal_reason_test.go:160-260` | `trailReasonAgainstPath` (#1366) — the sibling predicate over exactly this pair, **not consumed by this slice**. Its § *"No input byte interpolates into the output, at all"* (`:201-214`) is the doctrine AC4 is measured against. |
| `internal/e2e/realclaude/finding_run_record_test.go:231-238` | `finRecordInputs.ClaudeCommand`'s rule — *"READ, reduced … and NEVER RETAINED"*. The field doc this slice writes is that rule's mirror image at the gate's tier. |
| `internal/e2e/realclaude/finding_run_record_test.go:720-812` | `finRecordInputReaches` (the family's reflect walker) and the pin asserting `trailScanResult` is unreachable from `finRecordInputs` / `finRecordRun`. This is the pin Constraint 1 protects. |
| `internal/e2e/realclaude/finding_run_gather_test.go:537-546` | `finGatherReadings`' trailer leg — call site 1 of 2 in this file. |
| `internal/e2e/realclaude/finding_run_gather_test.go:765-788` | `finGatherAssertContract`'s C2 check — the **only whole-struct** `trailGateResult` comparison in the tree (`readings.Gate != want`, `:778`) and the stale-prose landing site at `:775`. |
| `internal/e2e/realclaude/trail_run_rig_test.go:120-195` | `trailRigGather` — call site at `:157`, and the doc explaining why nothing here hand-types a `trailGateResult`. |
| `internal/e2e/realclaude/trail_run_outcome_test.go:171-219` | `trailRunReadings` — read the doctrine *"nothing here is a value from which trailScanResult's trailer pointer is reachable"*. It gains **no field** in this slice. |
| `internal/e2e/realclaude/trail_run_outcome_test.go:474-511` | `trailClassifyRun`'s step-1 switch and its landmine comment. **Untouched** (AC5). |
| `internal/e2e/realclaude/trail_run_outcome_test.go:633-648, 1064-1130` | `trailRunCases`' `realGate` call site (`:641`) and `TestTrailRunComposesWithGateCases` (`:1091`, `:1117`) — the latter two are the sites that must end up with **zero diff**. |

---

## Context

`trailGate` (`trailer_admissibility_test.go:242`) is pure over one input, `trailScanResult`, and that input says nothing about which runner produced the line. It needs to, because `terminal_reason` is pyry's invention and the two runner paths owe it differently — the same trailer shape is a healthy run on the stream path and a broken record on the pty path. #1366 shipped `trailReasonAgainstPath`, the pure predicate over exactly that pair, and it has no decision-path caller because the gate it belongs in cannot see a path.

**This slice carries the path to the gate's input and changes no arm.** The decision lands in #1374. Splitting the shape change from the decision is what makes #1374's red a statement about the gate's arms rather than about a signature.

---

## Size check

Recorded so a reviewer can disagree with the accounting rather than reconstruct it.

**Call-site red line (the binding one).** `trailGate(` has 12 call sites. Under the shape chosen below, **four require no textual change at all** and **eight require a one-expression wrap**:

| Site | Expression today | After |
|---|---|---|
| `trailer_admissibility_test.go:730` | `trailGate(tc.in)` | **unchanged** |
| `trailer_admissibility_test.go:945` | `trailGate(tc.in)` | **unchanged** |
| `trail_run_outcome_test.go:1091` | `trailGate(tc.in)` | **unchanged** |
| `trail_run_outcome_test.go:1117` | `trailGate(tc.in).Value` | **unchanged** |
| `trailer_admissibility_test.go:760` | `trailGate(trailScanResult{State: trailSeen})` | wrapped |
| `trailer_admissibility_test.go:765` | `trailGate(trailScanResult{…})` | wrapped |
| `trailer_admissibility_test.go:775` | `trailGate(trailScanResult{State: "some-state…"})` | wrapped |
| `trailer_admissibility_test.go:995` | `trailGate(trailScanResult{…})` (multi-line) | wrapped |
| `trail_run_outcome_test.go:641` | `trailGate(trailScan(…))` | wrapped |
| `finding_run_gather_test.go:546` | `trailGate(obs.trailScanResult)` | wrapped |
| `finding_run_gather_test.go:778` | `trailGate(trailScan(seed))` | wrapped |
| `trail_run_rig_test.go:157` | `trailGate(obs.trailScanResult)` | wrapped |

The four survivors are not an argument that some edits are cheap — they are sites whose **text does not change**, because `trailGateCase.in` is retyped rather than the call rewritten. That is falsifiable: `git diff` must show zero hunks at those four lines. **8 ≤ 10, so the call-site red line does not trip.** It would trip under the second-parameter shape (12 sites + 8 rows), which is one reason that shape is rejected below.

Not counted as call sites, stated so the accounting is complete: the **eight fixture rows** in `trailGateCases()` (`:543-593`) each gain a wrap. They are one contiguous 50-line literal block inside the function being edited, not a fan-out across consumers.

**Line count, by the analogue instrument.** Bottom-up projects ~215 insertions / ~30 deletions. Bottom-up runs low in this package (doc-comment density is ~1:1), so the check that decides is the nearest **shape-class** analogue — a shipped pure function's parameter type changed with its call sites migrated:

- **#1320** (`finTrailerBuild takes the sighting carrier in place of the observation`, `eea9f91`) — **283 insertions / 82 deletions across 3 files** in this package.

#1373 is **not** strictly more work than #1320: #1320 narrowed an input type and therefore had to *decide* what happens to newly-reachable shapes (it changed behaviour and reworked 337 lines of an evidence file). #1373 changes no arm, no behaviour and no decision. Four files instead of three, fewer reworked assertions. So 365 churn is a ceiling here, not a floor, and the ~600-line red line is not in reach.

**The other red lines.** New files 0 (≤3) · new types 1, all package-private (≤5 exported) · reject branches in the gate 7, unchanged (<10) · ACs 5 (≤5) · production source files prescribed **0** (≤5, the § 4 self-check).

**File-overlap check (§ 1.5).** `git fetch origin --prune` then a diff of every `origin/feature/<N>` branch against `origin/main`: ten branches carry changes, none touches `trailer_admissibility_test.go`, `trail_run_outcome_test.go`, `finding_run_gather_test.go` or `trail_run_rig_test.go`. The nearest neighbour is `origin/feature/363` (`internal/e2e/realclaude/fixtures.go`) — a different file. **No block set.**

**Verdict: S, one ticket, no shim.** The shim seam PO named in Technical Notes is not taken; it would cost #1368 and #1374 a cycle each for an 8-site migration that fits.

---

## Design

### The shape

Two additions, both in `trailer_admissibility_test.go`.

**1. A wrapper input type.** The gate stops taking the scan result bare and takes a record carrying it alongside the reading:

```go
// trailGateInput is what the gate decides over: the scan's output, plus the
// runner path the run was observed to take. Kept OFF trailScanResult, which is
// trailScan's output over bytes alone.
type trailGateInput struct {
	Scan       trailScanResult
	RunnerPath string
}

func trailGate(in trailGateInput) trailGateResult
```

`RunnerPath` holds `tdnRunnerFromArgv`'s **output** and never its input — the mirror of the rule `finRecordInputs.ClaudeCommand` states at its own tier (`finding_run_record_test.go:235-238`). Its doc must say that in those words.

**2. The result echoes the reading.** `trailGateResult` gains a fourth field:

```go
	// RunnerPath is the reading the gate was HANDED, copied out unread. No arm
	// consults it; it is here so the reading that reached the gate is observable
	// from outside it. Not a copy of captured bytes: it is one of
	// tdnRunnerFromArgv's five constant answers, which is what keeps it inside
	// this record's no-captured-bytes rule.
	RunnerPath string `json:"runner_path,omitempty"`
```

Every one of the gate's **seven** return sites gains `RunnerPath: in.RunnerPath`. Nothing else in the body changes except `res.` → `in.Scan.`.

**3. One shared helper**, next to `trailGateCases`:

```go
// trailRunnerUnread is the honest reading for "the runner was not read from the
// process table" — tdnRunnerFromArgv's own answer to an empty command, obtained
// by CALLING it rather than by re-typing its prose. Both gathers and every
// fixture row use it, so the two sides of C2's equality cannot drift.
func trailRunnerUnread() string
```

A function rather than a package-level `var`, matching `trailRigHeldPGID()`'s shape in this family.

**4. `trailGateCase.in` is retyped** from `trailScanResult` to `trailGateInput`. Each of the eight rows becomes `in: trailGateInput{Scan: <what it says today>, RunnerPath: trailRunnerUnread()}`. This is what leaves the four `trailGate(tc.in)` call sites untouched, and it is the per-row path #1374 needs to distinguish rows *by* path.

### Data flow

```
argv (never enters this slice)
  └─ tdnRunnerFromArgv(claudeCommand)  ── the ONLY producer of a reading
        └─ trailGateInput{Scan, RunnerPath}
              └─ trailGate  ── seven arms, none reads RunnerPath
                    └─ trailGateResult{Value, Reason, Detail, RunnerPath}
                          └─ trailRunReadings.Gate  (both gathers, unchanged shape)
                                └─ #1374 reads .RunnerPath here
```

In this slice both gathers pass `trailRunnerUnread()`. Neither gains a parameter. #1374 replaces that with the reading derived from each gather's own argv scan.

### Why the reading lands on `trailGateResult`

The ticket left three shapes open. The decisive difference is what AC2's read-back can honestly assert.

- **On `trailGateResult` (chosen).** The read-back observes what *arrived*: the gate is pure, so an echoed field is the only channel through which the input's fill is visible downstream of the call. `trailRunReadings` gains no field and `trailClassifyRun` gains no contract check. Both shipped gathers hand #1374 `readings.Gate.RunnerPath` with zero new plumbing. PO wrote AC2's exemption clause for exactly this shape.
- **On `trailRunReadings` (rejected).** That type is `trailClassifyRun`'s input under nine contract checks, and its doctrine is that every field is contracted. A field no check covers is a gap a reviewer must litigate, and closing it means editing the contract block — which AC5 keeps shut. Worse, a value the *gather* assigns beside the gate call is not proven to be the value the gate received; the vacuity AC2 exists to close would move out of reach of a gate-level test.
- **A second parameter that is dropped (rejected).** Nothing survives the call for #1374 to assert against, and the migration grows to 12 sites + 8 rows.
- **On `trailScanResult` (forbidden by the ticket, and rightly).** It is `trailScan`'s output over bytes alone; a runner path is not a property of the line. It is also pinned unreachable from the published record (`finding_run_record_test.go:780-812`), and widening it puts that pin up for renegotiation for no gain.

### Reconciling with #1366's precedent

`trailReasonAgainstPath` takes a `runnerReading` and deliberately lets **no input byte interpolate into its output** (`trailer_terminal_reason_test.go:201-214`), noting *"nothing is lost: the consumer's record publishes the reading separately (`finRecordRun.RunnerFromArgv`)"*.

This design follows that precedent rather than departing from it. The rule there is about **Detail prose** — *"a Detail echoing the label would publish whatever a caller passed"* — and it is honoured here exactly: no arm's Detail gains a directive for the reading, and AC2's byte-identical sweep proves it over 8 rows × 5 readings. The reading travels in its own named field, which is the same place `finRecordRun.RunnerFromArgv` puts it. What #1366 achieves by review of its source, this slice achieves by a deterministic test.

### Rejected: splitting the decision into an inner `trailGateDecide(res trailScanResult)`

It would make "no arm reads the path" structural and save seven one-line edits, but it hands #1374 a restructuring — the arm that must read the path would first have to un-split the function. Avoiding exactly that is why this slice exists. It also adds a symbol whose doc has to justify itself, which this family refuses on principle (`finding_run_gather_test.go:553-556`; `trailer_terminal_reason_test.go:184`).

### Concurrency model

None. `trailGate` is pure over its input — no exec, no clock, no filesystem, no `*testing.T`, no goroutine — and stays that way. That purity is what lets every arm be driven offline with no credentials, and it is the property AC2's sweep depends on: five calls over one row must differ in nothing but the field under test.

### Error handling

**No new arms, and deliberately no contract check on `RunnerPath`.** The gate's existing seven arms and their ordering are untouched. A membership check on the reading would need the five answers as literals (which AC2 forbids re-typing) or a shipped membership predicate (there is none), and it would grow a gate value, which AC5 forbids. Per the pipeline's evidence-based rule: no observed failure mode calls for it.

The consequence, stated rather than hidden: `RunnerPath == ""` means *the field was not filled*, which no shipped producer emits — `tdnRunnerFromArgv` returns a non-empty string on every branch, including for `""`. Because no arm reads it, an unfilled field cannot misroute a decision; the worst case is a published record whose `runner_path` is absent, which reads as "not recorded". The decision about what an unfilled reading *means* belongs to #1374, which is where an arm first depends on it. Write that into the field's doc so #1374 inherits the question rather than rediscovering it.

---

## The migration, precisely

**`trailer_admissibility_test.go`**

- Add `trailGateInput`; add `trailGateResult.RunnerPath`; add `trailRunnerUnread()`.
- `trailGate`: signature, doc (a short § naming the new field as carried-not-read), `res.` → `in.Scan.`, and `RunnerPath: in.RunnerPath` on all seven returns.
- `trailGateCase.in` retyped; eight rows wrapped.
- `:760`, `:765`, `:775` wrapped — **their assertions do not change** (AC3).
- `:995` wrapped: `Scan:` takes today's needle-bearing literal, `RunnerPath: trailRunnerUnread()`.
- New test (below).

**`trail_run_outcome_test.go`** — `:641` only. `:1091` and `:1117` must show **zero diff**.

**`finding_run_gather_test.go`**

- `:546` wrapped, with a one-line comment saying the gather supplies the not-read reading in this slice and that #1374 replaces it with its own argv scan's.
- `:778` wrapped with **the same** `trailRunnerUnread()`, so C2's equality still compares like with like. This makes C2 slightly stronger for free: it now also asserts the gather passed the expected reading.
- `:775`'s comment says *"trailGateResult is three strings, so `==` suffices"* — **update to four**. `==` still suffices; all four fields are strings.

**`trail_run_rig_test.go`** — `:157` wrapped, same comment as the gather's.

**Deliberately not touched.** `docs/specs/architecture/1281-parameterised-run-gather.md:168` carries the same "three strings" phrasing. It is a frozen build artifact that was true when written; rewriting shipped specs is out of scope. Only the live code comment moves.

---

## Testing strategy

`make check` does not build this package (`e2e_realclaude` tag), so the gate is:

```
go test -tags e2e_realclaude -run '^(TestTrail|TestFin|TestTdn)' ./internal/e2e/realclaude/
```

It must be green offline with no credentials and **zero skips** — the ticket re-verified that on the post-#1366 tree (3.419 s, 147 tests, no `t.Skip`, no env gate). A skip here is a regression, not a pass.

### The new test — `TestTrailGateIgnoresTheRunnerPath` (AC2)

Bullet-pointed scenarios; write them in this file's idiom.

**Building the five readings.** Call `tdnRunnerFromArgv` over five argvs; never re-type an answer as a literal.

| Argv | Answer |
|---|---|
| `tdnFixturePtyArgv` (`:897`) | `ptyrunner (…--session-id)` |
| `tdnFixtureStreamArgv` (`:902`) | `streamrunner (…--input-format)` |
| `tdnFixturePtyArgv + " --input-format stream-json"` | `indeterminate (…BOTH…)` |
| an argv carrying neither marker — mirror `:929`'s `--append-system-prompt-file`-only line | `indeterminate (…NEITHER…)` |
| `""`, i.e. `trailRunnerUnread()` | `indeterminate (no single claude row was pinned…)` |

**Non-vacuity clause A — pairwise distinct.** Assert the five strings are pairwise distinct, naming which two collapsed on failure. If any two are equal the sweep is weaker than it claims, and the count of distinct answers is the thing the ticket had to correct from the shipped "three" (that three counts `finRecordRunnerLabel`'s leading tokens, not the function's returns).

**Non-vacuity clause B — read-back.** For every (row, reading) pair, assert `got.RunnerPath == reading` after the call. This is the arrival check: a row built without filling the path yields `""` and goes red here instead of passing forty identical comparisons. Note in the test's doc that this assertion is **total over the gate's seven return sites** — the eight rows reach all seven arms (`:246` twice, `:258`, `:266`, `:280`, `:291`, `:303`, `:314`), so a single arm that forgot to carry the field cannot hide.

**The invariance.** For each of the eight `trailGateCases()` rows, take the first reading's result as the baseline and, for the other four:

- `got.Value == base.Value`
- `got.Reason == base.Reason`
- `bytes.Equal([]byte(got.Detail), []byte(base.Detail))` — **as bytes**, printing both Details on failure. A Detail that silently acquired the path would pass a value check while changing what the published record says.

`RunnerPath` is the **sole** exemption from the comparison; clause B covers it instead.

**Keeping "every other field" true under later edits.** Pin `reflect.TypeOf(trailGateResult{}).NumField() == 4` with a message saying a fifth field must either join the compared set or state its own exemption. Six lines, and it is this family's own idiom for "a later edit cannot add a field silently" (`finding_run_record_test.go:780-782`, `finding_run_gather_test.go:2028-2031`).

**The premise, kept distinct from the claim.** Assert `base.Value == tc.want` and `base.Reason == tc.reason` before the sweep, labelled in its failure message as *the premise, shared with `TestTrailGate`* — otherwise a gate broken into returning one value for everything would keep this sweep vacuously green. Word the invariance failures differently ("the decision differs across readings") so a reviewer reading a red can tell which assertion caught what, and so a mutation matrix records the line a mutant actually lands on rather than only its pass/fail bit.

### The shipped tests that must still pass unchanged

- **`TestTrailGate`'s sub-test at `:756`** (AC3) — the out-of-contract Details still name `"nil trailer"`, `"terminal_reason is empty"`, `"NO LIVE REPRO EXISTS"` and quote the rejected state. Three of the eight edited call sites are inside it, so it is the mechanical edit's own proof.
- **`TestTrailAdmissibilityConstantsAreClosed`** (`:623`) — no new entry (AC5). Its zero-`trailGateResult` checks read `.Value` and `.Reason`; the new field is not a value space.
- **`TestTrailAdmissibilityRecordsCarryNoCapturedBytes`** (`:993`) — unchanged assertions. The marshalled result now carries `runner_path`, whose value is a constant answer and needle-free.
- **`TestTrailGateThenAdmit`** (`:934`), **`TestTrailRunComposesWithGateCases`** (`:1073`), **`TestTrailClassifyRun`**, `finGatherAssertContract`'s C2 (`:778`), and the reachability pins at `finding_run_record_test.go:780-812` / `finding_run_gather_test.go:2043`.

### AC coverage

| AC | Discharged by |
|---|---|
| 1 — input carries the reading, off `trailScanResult`, twelve sites compile, readable outside | `trailGateInput` + `trailGateResult.RunnerPath`; the twelve-site table above; the package builds under the tag |
| 2 — no arm reads it, proved | `TestTrailGateIgnoresTheRunnerPath`: 8 rows × 5 readings, value + reason + byte-identical Detail, clauses A and B, `NumField` pin |
| 3 — shipped Detail assertions unchanged | `TestTrailGate`'s `:756` sub-test, edited only at its three call expressions |
| 4 — only the reduced answer is carried | Structural argument below |
| 5 — closed sets unchanged | No constant added, no gate value added, `trailClassifyRun` untouched |

---

## Security review — category walk

Run because the ticket carries `security-sensitive`. Per `architect/security-review.md`, every category below carries either a concrete finding or the design decision that makes it inapplicable. The verdict block follows the walk.

**The asset** is the **published artifact**: these records are written to disk and pasted into public GitHub issues, so the threat is an operator secret — `CLAUDE_CODE_OAUTH_TOKEN`, `ANTHROPIC_API_KEY`, a prompt, a working-directory path — reaching a record documented as publishable unreviewed. argv is the carrier that would do it: a `ps` command column is how an operator's environment reaches a file. That is the whole reason `reachProc.Command` is confined to the rig level, and why `finRecordInputs.ClaudeCommand` is *"READ, reduced … and NEVER RETAINED"*.

### AC4 as a symbol list, not a grep

"Reads no argv" cannot be checked by grepping for `argv` — every route to argv in this package runs inside a helper, so the grep reads clean against a leaking design. The forbidden symbols, each with its reason. None may appear in `trailGate`, in `trailGateInput`, or at any fill site of `RunnerPath`:

| Symbol | Why forbidden here |
|---|---|
| `tdnClaudeCommand` (`teardown_liveness_probe_test.go:561`) | returns a raw claude command line |
| `reachProc.Command` | the verbatim `ps` command column |
| `pinScan.Matches` / `pinScanArgv` | the slice of rows carrying those columns |
| `reachRunnerPathFromArgv` (`background_reach_probe_test.go:1118`) | keys on `--append-system-prompt-file`, which both argv builders pass — it labels a correctly-wired stream run `ptyrunner`, so it is wrong on the merits as well as forbidden here |
| `tdnFixturePtyArgv` / `tdnFixtureStreamArgv` **inside `trailGate`** | argv literals. **Allowed in the new test**, where they are `tdnRunnerFromArgv`'s inputs and never reach the gate |

`tdnRunnerFromArgv` is the one **allowed** producer, called only in `trailRunnerUnread()` and in the new test. `trailGate` calls no argv reader at all.

### 1. Trust boundaries

The boundary is `tdnRunnerFromArgv`: tainted argv in, one of five source-authored constants out. It is explicit and single-function, and this slice keeps every fill on the trusted side of it — the gate never sees the untrusted side.

**F1 — SHOULD FIX (downstream): the type system does not signal which side of the boundary `RunnerPath` holds.** It is a plain `string`, so a future call site could pass `tdnClaudeCommand(scan)` where `tdnRunnerFromArgv(tdnClaudeCommand(scan))` was meant, and the record would publish raw argv. Not exploitable as designed — no call site in this slice reads argv at all — but the guard rail is a convention, not a type. Three independent bounds:

1. **No argv-shaped landing site inside the shape.** The gate takes no argv parameter and calls no argv reader (symbol list above). This is AC4's structural criterion, met literally.
2. **No Detail interpolates the reading**, proved deterministically by AC2's byte-identical sweep over 8 rows × 5 readings — the moment any arm's format grows a directive for it, the sweep goes red. Strictly stronger than #1366's equivalent guarantee, which is held by review of its source.
3. **This slice reads no argv at all.** Both gathers pass `trailRunnerUnread()`, so there is no live argv in the tree to leak. #1374 owns the runtime needle-plant, once a gather actually reads argv; a needle test here would have nothing to plant into.

**Carried to #1374 and to code-review:** the check is that every fill site passes `tdnRunnerFromArgv`'s **output**. A defined type (`type trailRunnerReading string`) was considered and rejected — converting argv is exactly as easy as converting an answer, so it buys friction sold as a guarantee, at the cost of a new type and 12 conversions.

### 2. Tokens, secrets, credentials

No token is generated, stored, rotated, revoked or compared — nothing in this slice has a lifecycle. The category is live only through F1: an operator credential can reach these records **only** as a substring of argv, which is why the boundary above is the whole security content of this ticket.

### 3. File operations

No path is constructed, opened, stat-ed or written by anything this slice adds; the gate is pure. **One real touchpoint:** `runner_path` becomes a new key in the artifact JSON the shipped writer already emits. File mode, atomicity and location are the writer's and are untouched, and the new key's value in this slice is always the "not read from the process table" constant. No traversal, TOCTOU, symlink or partial-write surface.

### 4. Subprocess / external command execution

The gate execs nothing and this slice adds no `exec.Command`, no `sh -c` and no env manipulation. This category is the *reason* for the symbol list above rather than being N/A: every forbidden symbol is a route to output that a `ps` exec produced, and the list — not a grep for `exec.` — is what makes the prohibition checkable.

### 5. Cryptographic primitives

Not applicable, by design: the gate is a pure classifier over strings with no randomness and no key material. The only equality this slice adds compares a driven reading to an arrived one, neither of which is a secret, so `crypto/subtle.ConstantTimeCompare` has nothing to protect and `==` is correct.

### 6. Network & I/O

No socket, no HTTP server, no TLS, no read deadline to set. The relevant input-size limit is `bufio.Scanner`'s 64 KiB default inside `trailScan`, whose overflow is already the `trailAborted` arm — unchanged.

**F2 — OUT OF SCOPE (→ #1374): `RunnerPath` is uncapped.** `reachCapCommand`'s 512-byte cap applies via `trailDetail` and so covers `Detail` only. Today the field is bounded in practice — five source constants, longest under 100 bytes — and it is not unprecedented on this record: `Reason` already carries an uncapped decoded value from the trailer. It becomes worth capping the moment a gather fills it from a live read, because under F1's mistake an unbounded argv string would land in an artifact with no cap between. **Recommended for #1374: route the fill through `reachCapCommand`.** Deliberately not done here — with no live read in the tree it is a no-op defense for an unobserved failure, and the evidence-based rule says defer it to the ticket that creates the exposure.

### 7. Error messages, logs, telemetry

The gate's seven Details are unchanged, and AC2 proves the reading enters none of them. The new surface is the new test's **failure messages**, which print `got.Detail` and `base.Detail` (both `trailDetail`-capped, both derived from `trailGateCases()`' fixtures) and the driven reading (a constant). Test output reaches transcripts and issues, so this matters: no needle-bearing or live-derived string is printed. `TestTrailAdmissibilityRecordsCarryNoCapturedBytes` keeps its own plant and its own unchanged assertions.

### 8. Concurrency

No goroutine, no lock, no shared mutable state; `trailGate` stays pure, which is the property AC2's sweep depends on.

**F3 — SHOULD FIX (developer): the five per-row copies share one `*resultTrailer`.** The sweep copies each row's `trailGateInput` five times to vary `RunnerPath`. A struct copy copies the *pointer*, so all five inputs for a row alias one `resultTrailer` — the same aliasing `trailRunWellFormed`'s doc warns about at `trail_run_outcome_test.go:610`. It is race-free as specified because the gate only reads `TerminalReason` through it. **The obligation: vary `RunnerPath` only, never mutate through the copied pointer**, and if `t.Parallel()` is added to the subtests, that rule becomes load-bearing rather than incidental.

### 9. Threat model alignment

No relay or protocol surface — `docs/protocol-mobile.md` § Security model has no applicable threat. The governing model is this file family's own, written at `trailGateResult`'s doc (`trailer_admissibility_test.go:174-179`): a record whose whole value is that it can be published unreviewed must carry no captured bytes.

**F4 — MUST ADDRESS IN THE SPEC (done): the `trailGateResult` doc's rule reads as violated.** The type says *"a future field added here must not be a copy of an input's captured bytes"*, and `RunnerPath` **is** a copy of an input string. The rule is not outgrown silently: the field's doc must state that the value is one of `tdnRunnerFromArgv`'s constant answers and therefore not captured bytes, in the same words `finRecordInputs.ClaudeCommand` uses at its tier. Already required in § Design; restated here because a reviewer must be able to see the rule was *answered* rather than skipped.

---

## Security review

**Verdict:** PASS

**Findings:**

- [Trust boundaries] SHOULD FIX — `RunnerPath` is a plain `string`, so the type system does not distinguish `tdnRunnerFromArgv`'s output from its argv input (F1). Not exploitable as designed: no call site in this slice reads argv, no Detail interpolates the reading (proved by AC2's byte sweep), and the gate calls no argv reader. Code-review must check that every fill site passes the reducer's output; a defined type was considered and rejected as friction without a guarantee.
- [Tokens] No findings — nothing generated, stored, rotated or compared. A credential can reach these records only as a substring of argv, which F1 bounds.
- [File operations] No findings — the gate constructs no path and opens no file. `runner_path` joins the artifact JSON the shipped writer already emits; its mode, atomicity and location are untouched.
- [Subprocess] No findings — nothing exec'd. The prohibition is made checkable by the forbidden-symbol list above rather than by a grep for `exec.`, because every route to argv here runs inside a helper.
- [Cryptography] Not applicable — pure classifier over strings, no randomness and no key material; the one equality added compares two non-secret readings, so constant-time comparison protects nothing.
- [Network & I/O] OUT OF SCOPE → #1374 — `RunnerPath` is uncapped (F2). Bounded in practice today (five constants, <100 bytes) and no worse than the already-uncapped `Reason`. Recommend routing the fill through `reachCapCommand` in #1374, when a live argv read first creates the exposure.
- [Errors & logs] No findings — the seven Details are unchanged and AC2 proves the reading enters none; the new test prints only capped, fixture-derived Details and constant readings.
- [Concurrency] SHOULD FIX — the five per-row input copies alias one `*resultTrailer` (F3). Race-free as specified because the gate only reads through it; the developer must vary `RunnerPath` alone and mutate nothing through the copied pointer, which becomes load-bearing if `t.Parallel()` is added.
- [Threat model] Addressed in the spec (F4) — `trailGateResult`'s "no copy of an input's captured bytes" rule must be answered in the new field's doc, stating that the value is one of `tdnRunnerFromArgv`'s constant answers, in `finRecordInputs.ClaudeCommand`'s own words.

**Reviewer:** architect (self-review per `architect/security-review.md`)
**Date:** 2026-08-07

---

## Open questions

1. **What does an unfilled `RunnerPath` mean at a decision?** Undecidable here, because no arm reads it. #1374 must answer it when it adds one — most likely as a contract check, since "" is a value no shipped producer emits. Recorded in the field's doc so #1374 inherits it.
2. **Should the five answers become a shipped closed set with a membership predicate?** Not in this slice: AC5 keeps the closed sets shut and AC2 forbids re-typing the answers as literals. If #1374's arm needs to reject a non-member, that is the ticket that earns the predicate — and it should extend `TestTdnRunnerFromArgv`'s own file rather than this one, since `tdnRunnerFromArgv` owns the space.
3. **`trailReasonAgainstPath` still has no decision-path caller** after this slice, by design. #1374 is where its callers stop being its own tests. If #1374 slips, the "every value has an arm in its consumer" comment at `trailer_admissibility_test.go:612-622` remains true only by that comment's own carve-out for #1366's fourth space.
