# #1291 — The publishable run record: process evidence and the runner path recorded as observed

**Ticket:** [#1291](https://github.com/pyrycode/pyrycode/issues/1291) · **Size:** S · **Labels:** `size:s`, `security-sensitive`
**Split from** #1285 · **Blocked by** #1290 (**merged** at `1b673b6`)

New file: `internal/e2e/realclaude/finding_run_record_test.go`. Everything is offline, pure, and under the `e2e_realclaude` build tag.

---

## Files to read first

Everything below is in `package realclaude` under the `e2e_realclaude` build tag, so every symbol is directly callable from the new file. Line refs verified at `1b673b6`; read, don't grep.

| Path | Extract |
|---|---|
| `internal/e2e/realclaude/finding_trailer_evidence_test.go:1-30` | The file-header shape this family uses, including the exact `go test -run` line and the standing "no live claude, no credentials, … no `t.Skip`" clause AC5 asks for. |
| `…/finding_trailer_evidence_test.go:142-155` | `finTrailerRecord` — the sub-record embedded **whole** here. Ten scalars, flat. |
| `…/finding_trailer_evidence_test.go:203-215` | `finTrailerBuild(outcome string, obs trailObservation)` — the builder contract this file mirrors (pure, no `*testing.T`, never fails a test). Note its comment says it is **not** trap-free by construction; §"Two properties, and only one is structural" below explains why this builder is in the same position. |
| `…/finding_trailer_evidence_test.go:623-700` | `TestFinTrailerRecordCarriesNoCapturedBytes` — **the template for AC4**: the non-vacuity precondition, the per-row headroom assertion, the named-per-channel failures. Read the closing comment at `:686-692` carefully — its top-level key scan is valid only because that record is flat. This one is not. See §"AC4 — what to copy and what not to". |
| `…/finding_trailer_evidence_test.go:472-478` | `finTrailerOutcomeValues()` — the precedent for a fixture helper that returns a closed value set by calling shipped lists. `finRecordLivenessValues()` mirrors its shape. |
| `internal/e2e/realclaude/finding_attribution_fanout_test.go:80-95` | `finAttributeEntry` — the "trap-free **by construction** rather than by an ordering discipline a later edit can break" argument, and how it is worded on the type. `finRecordProc` is the same move. |
| `…/finding_attribution_fanout_test.go:95-130` | `finAttributeRecord` — the second sub-record embedded whole. Its `Detail` content rule at `:116-124` (esp. "Quoting `Selected.Detail` is the likeliest slip") is the direct model for this record's. |
| `…/finding_attribution_fanout_test.go:209` | `finAttributeFanOut(stderr []byte, pgids []int, certified string) finAttributeRecord` — pure, no exec, no clock. Call it; do not rebuild an attribution and do not re-parse pyry's stderr. |
| `internal/e2e/realclaude/process_pin_liveness_test.go:203-236` | The four `pinState*` values and **why they are never collapsed**; `pinStateColumns` and its never-add-`command`/`args`/`comm` prohibition; `pinExitStatusUnknown = -1` at `:236`. |
| `…/process_pin_liveness_test.go:245-253` | `pinStateOutcome` — carried whole as an input. It already has `PID` and `PPID`, which is why AC1's "which row it belongs to" needs no new field. |
| `…/process_pin_liveness_test.go:275-300`, `:332`, `:1088-1099` | `pinReadState` execs `ps` (`:293`); `pinClassifyState` is pure but its `pinStateNoSuchProcess` arm needs a real `*exec.ExitError`; `pinExit1` execs `false` to borrow one. **All three are off this file's path** — see §"Hand-built liveness". |
| `internal/e2e/realclaude/teardown_liveness_probe_test.go:756-790` | `tdnRunnerFromArgv` — the argv read, its two discriminating markers, and the recorded reason `reachRunnerPathFromArgv` is not reused. Every arm returns a **constant** string; no input bytes cross into the return. |
| `…/teardown_liveness_probe_test.go:895-965` | `tdnFixturePtyArgv`, `tdnFixtureStreamArgv`, and `TestTdnRunnerFromArgv` — AC3's fixtures are already written. Note the assertion is `strings.HasPrefix` at `:951` plus a "has a parenthesised reason" check at `:957`. |
| `internal/e2e/realclaude/background_reach_probe_test.go:1095-1123` | `reachRunnerPathFromEnv` (`:1102`) — reads ambient `os.Getenv` **first**, delta second (`:1103-1108`); returns `"ptyrunner (interactive TUI, the agent-run default)"` / `"streamrunner (headless stream-json)"`. `reachRunnerPathFromArgv` at `:1118` — the helper AC3 forbids. |
| `…/background_reach_probe_test.go:115-125`, `:162-168`, `:945-951` | `reachMaxCommandBytes = 512` and the threat model at `:117-122` that AC4's sweep serves; `reachProc` (carries `Command` + `Needles` alongside the three integers); `reachCapCommand`. |
| `internal/e2e/realclaude/trailer_admissibility_test.go:199-208`, `:533-538` | `trailDetail` = `fmt.Sprintf` + `reachCapCommand`, the **only** detail formatter this file may use; `trailReapLine(count int, pgids string)` — `pgids` is a **string**. |
| `…/trailer_admissibility_test.go:538-542` | `trailGateCases`' stated rule: shipped producer for what it can emit, hand-built for what it cannot. That is the licence for §"Hand-built liveness". |
| `internal/e2e/realclaude/result_trailer_observation_test.go:294-302` | `trailNeedle` (42 bytes) — the plant for AC4. |
| `…/result_trailer_observation_test.go:108-125` | The discriminated optional: `trailScanResult.Trailer` is nil unless `State == trailSeen` and a consumer dereferencing it panics loudly. This record never reaches it — AC2's whole point. |
| `internal/e2e/realclaude/trail_run_outcome_test.go:193-214`, `:225-241` | The three shipped comments this record answers to: `MatchCount` "carried instead of its `Matches` slice, which holds verbatim argv"; `Liveness` "`pinStateOutcome` carries no command column by construction"; `BoundFrom` "would promote that pointer back into reach"; and `trailRunOutcome.Detail`'s content rule. |

---

## Context

A probe on the **ptyrunner default** path measures whether `pyry agent-run` reaches its normal exit path while a command it launched is still executing. This ticket builds the record that probe publishes — process evidence, liveness verdicts, reap-log attribution, and the runner path — and proves it offline, before a live turn is spent.

Three of the four things the record carries are already built and merged: `finTrailerRecord` (#1290), `finAttributeRecord` (#1280), and `[]pinStateOutcome`. This ticket is mostly **assembly plus one new reduction** (the runner-agreement verdict), and the design work is in what the record may *not* hold.

---

## Design

### The row type is where the argv prohibition becomes structural

```go
// finRecordProc is one matched row's identity: three integers and nothing else.
type finRecordProc struct {
	PID  int `json:"pid"`
	PPID int `json:"ppid"`
	PGID int `json:"pgid"`
}
```

A field typed `[]reachProc` would pass every test in this file while keeping argv one edit away — `reachProc` carries `Command` and `Needles` (`background_reach_probe_test.go:162-168`). `finAttributeEntry` is the precedent: it holds only what it may publish, so its no-captured-bytes property is true *by construction rather than by an ordering discipline a later edit can break*.

The three integers are not a compromise — they carry the whole claim. A live run of this shape matches two rows, the shell wrapper and the command; they share a pgid and one is the other's parent, which is exactly what distinguishes wrapper from command. A reader of the issue can check that from the integers alone, which the bare integer `2` would not permit.

### The record

```go
type finRecordRun struct {
	ExitCode        int                `json:"exit_code"`
	Rows            []finRecordProc    `json:"matched_rows,omitempty"`
	Liveness        []pinStateOutcome  `json:"liveness,omitempty"`
	Attribution     finAttributeRecord `json:"attribution"`
	Trailer         finTrailerRecord   `json:"trailer"`
	RunnerFromEnv   string             `json:"runner_from_env"`
	RunnerFromArgv  string             `json:"runner_from_argv"`
	RunnerAgreement string             `json:"runner_agreement"`
	ClaudeVersion   string             `json:"claude_version"`
	Detail          string             `json:"detail"`
}
```

Field notes the developer must carry into comments:

- **`ExitCode`** — pyry's own exit status. The zero value is a **real successful exit**, so a caller with no observed exit must hand `pinExitStatusUnknown` (`process_pin_liveness_test.go:236`), not leave the field unset. This is a documented caller obligation, not a validated one: no builder in this family validates its inputs, and no such miswrite has been observed. Reuse the shipped constant; do not mint a second sentinel.
- **`ClaudeVersion`** — caller-supplied, and **capped through `reachCapCommand` on the way in.** The family's stated rule is that *every retained operator-visible string is capped* (`trailer_admissibility_test.go:202-205`), and this is the one such string the record would otherwise retain uncapped. The live caller is `probeClaudeVersion`, whose error path returns `fmt.Sprintf("<unavailable: %v>", err)` (`background_trigger_probe_test.go:611`) — an `exec` error that interpolates the resolved binary path, i.e. an operator's home directory, into a record destined for a public issue. Capping is one call and costs no API.
- **`Liveness`** — `[]pinStateOutcome` carried whole, per `trailRunReadings.Liveness`'s stated reason: *"`pinStateOutcome` carries no command column by construction"*. `PID`/`PPID` are what tie each verdict to its row; **do not** add a row-index field and **do not** widen `pinStateColumns`.
- **`Attribution` / `Trailer`** — both sub-records embedded whole, both documented trap-free by their own enforcing tests. Never re-derived, never re-read.
- **`ToolStderr`** stays on the carried `pinStateOutcome`. #1281 settled that a forbidden-key sweep meeting that key defuses by **exact-key exemption, never a prefix rule** — that is #1286's problem, not a reason to strip the field here.

### The builder takes a named-field input struct

```go
type finRecordInputs struct {
	ExitCode      int
	Rows          []reachProc // argv-bearing; reduced to three integers
	Liveness      []pinStateOutcome
	Attribution   finAttributeRecord
	Trailer       finTrailerRecord
	RunnerFromEnv string
	ClaudeCommand string // read, reduced, never retained
	ClaudeVersion string
}

func finRecordBuild(in finRecordInputs) finRecordRun
```

`finAttributeFanOut` and `finTrailerBuild` take positional parameters, and this file departs from that for one reason: **four of the eight inputs are strings**, and two of them — `RunnerFromEnv` and `ClaudeCommand` — are adjacent, same-typed, and on opposite sides of the argv prohibition. Transposing them positionally would write verbatim claude argv into `runner_from_env` and publish it, with nothing going red. Named fields make that transposition a compile error. Same doctrine as the row type: prefer the shape that cannot be got wrong over the discipline that must not be.

`finRecordBuild` is pure over its inputs — no exec, no clock, no filesystem, no `*testing.T`, and it never fails a test — the same contract as `trailScan`, `trailGate`, `finTrailerBuild`, `finAttributeFanOut` and `pinReadState`.

### Two properties, and only one is structural

Worth stating plainly on the builder, because the two halves differ and a reader who conflates them will trust the wrong thing:

- **The record is trap-free by construction.** No field can hold argv, and `trailScanResult.Trailer` is unreachable because the builder takes neither a `trailObservation` nor a `trailScanResult` — the property `trailRunReadings.BoundFrom`'s comment states as its own reason for taking a plain value (*"would promote that pointer back into reach"*). This is the **stronger** property #1290 could not buy: `finTrailerBuild` takes the observation, so `Line` is in its reach.
- **The builder is not.** `in.Rows[i].Command` and `in.ClaudeCommand` are verbatim argv, in reach inside the function. The no-captured-bytes property across the *conversion* is held by the Detail content rule plus AC4's enforcing test — exactly `finTrailerBuild`'s posture, and it must be worded as such rather than claimed away.

### The runner path: two readings, one three-valued verdict

`reachRunnerPathFromEnv` reads the env the rig itself set, so it can only report the rig's own intent. It is recorded **as documentation, not as corroboration**, in the artifact and not only in a comment — exactly as #1230's record does (`background_reach_probe_test.go:346-349`). A reader must not be misled into counting two agreeing reads.

The evidential read is `tdnRunnerFromArgv` (`teardown_liveness_probe_test.go:772`), never `reachRunnerPathFromArgv`: the latter keys on `--append-system-prompt-file`, which `buildStreamRunnerClaudeArgs` (`cmd/pyry/agent_run.go:372`) emits alongside ptyrunner's `buildArgs`, so it answers "ptyrunner" on **both** paths and a silent switch reads as a correct label with nothing going red. `TestTdnRunnerFromArgv` already carries the regression row against re-adopting it.

**Agreement is decided on the label alone.** The two readings do not share a vocabulary — the env read answers `ptyrunner (interactive TUI, the agent-run default)` while an agreeing argv read answers `ptyrunner (claude argv carries --session-id)`. The full strings are therefore **never equal, not even when both name the same runner**, so a whole-string comparison reports a disagreement on every run and AC3's disagreement row would pass while discriminating nothing.

```go
const (
	finRecordRunnerAgrees        = "agree"
	finRecordRunnerDisagrees     = "disagree"
	finRecordRunnerIndeterminate = "indeterminate"
)

// finRecordRunnerLabel returns the leading token before the parenthesised reason.
func finRecordRunnerLabel(reading string) string

// finRecordRunnerAgreement compares two runner readings by label alone.
func finRecordRunnerAgreement(fromEnv, fromArgv string) string
```

Three contract points on `finRecordRunnerAgreement`, each pinned by a test row:

1. **The indeterminate arm is checked first.** `indeterminate` is a *third* answer, not a disagreement: it says the argv was not read, not that the run took the other path. Collapsing it into "disagree" would publish a claim about the runner that no reading supports. Ordering the check first is what makes that structural rather than incidental.
2. **Only the argv side can be indeterminate.** `reachRunnerPathFromEnv` returns exactly two values by construction. Do not add a dead arm for an indeterminate env reading.
3. **The label extractor's degenerate path fails toward `disagree`.** Given a reading with no `" ("`, return the whole string. Both shipped producers always emit a parenthesised reason — `TestTdnRunnerFromArgv:957` asserts it — so this path is defensive only, and the safe direction is that two whole strings compare unequal (disagree) rather than collapsing to a false agreement.

On the comparison primitive: `TestTdnRunnerFromArgv` uses `strings.HasPrefix` against a *known-expected* label, which is correct there. Here both operands are unknown at compile time, so use **exact equality of extracted labels**. Over the closed value space `{ptyrunner, streamrunner, indeterminate}` the two happen to agree, but prefix-matching two unknowns is the wrong primitive for the claim and should not be copied across.

### The Detail content rule, pinned rather than left to judgement

In `trailRunOutcome.Detail`'s shape (`trail_run_outcome_test.go:225-232`) and `finAttributeRecord.Detail`'s (`finding_attribution_fanout_test.go:116-124`).

It **MAY** name: the exit code, the row and liveness **counts**, the three runner readings and their agreement verdict, the attribution's selected admissibility value, and the carried outcome.

It **MAY NEVER** quote: an argv; a `pinStateOutcome`'s `Detail` or `ToolStderr`; an entry's `Admit.Detail`; the embedded `finTrailerRecord.Detail` or `finAttributeRecord.Detail`; or pyry's stderr.

Quoting a sub-record's `Detail` is **the likeliest slip here** — this record embeds two of them, it reads as helpful context, and it both duplicates a string the record already carries and spends the 512-byte cap on it. `#1280` names the same slip for one sub-record; this record doubles the surface.

**The concrete mechanism the argv prohibition is guarding against is a `%v` verb applied to an input.** `trailDetail("… rows: %v", in.Rows)` renders `[]reachProc` including every `Command` — the whole verbatim argv of every matched row, straight into the published string, from a line that reads as ordinary debug formatting. Same for `%v` on `in` itself, or on `in.Liveness`. Format the **derived scalars** (`len(in.Rows)`, the exit code, the three labels) and never an input struct or an input slice. This is the one place the prohibition is easy to break by accident rather than by intent, and it is the channel AC4's sweep exists to catch.

**Name counts, not every pid.** The record already publishes every row's three integers as fields, so enumerating them in the Detail adds nothing a reader cannot already see and makes the Detail's length grow with the input. That is what turns the headroom below from a structural property into a property of how many rows happened to be handed in. `trailDetail` caps at 512 (`reachCapCommand`), and a Detail that grows with its input is one that can be silently truncated.

**Headroom.** Every Detail must leave `len(trailNeedle)` = 42 bytes under `reachMaxCommandBytes` — so **under 470 bytes on every row**. This is not stylistic: a Detail that had wrongly interpolated an argv would be truncated before the needle if the surrounding prose left no room, and AC4's containment checks would then pass against a leaking implementation. That is the defect #1284 shipped and had to fix. The long-form argument belongs in a comment, which no cap applies to.

### Hand-built liveness inputs

AC5's no-exec rule closes both shipped producers: `pinReadState` execs `ps` (`process_pin_liveness_test.go:293`), and `pinClassifyState`'s `pinStateNoSuchProcess` arm needs a real `*exec.ExitError` that Go cannot synthesize — the helper that borrows one, `pinExit1` (`:1088`), execs `false`.

Hand-building is the correct answer here rather than a concession: this record **consumes** the verdicts and never derives them, so a hand-built row can assert nothing the classifier would have refused. Same position `finTrailerOutcomeValues()` occupies for #1290's outcome, and the same rule `trailGateCases` states — *shipped producer for what it can emit, hand-built for what it cannot* (`trailer_admissibility_test.go:538-542`).

```go
// finRecordLivenessValues returns the four pinState* values, naming the shipped
// constants rather than restating their strings.
func finRecordLivenessValues() []string
```

A **function, not a package-level `var`** — for the reason `trailRunWellFormed` states (`trail_run_outcome_test.go:608-610`) and `finAttributeOrder` and `trailRunOutcomeValues` both follow: a package-level slice's backing array is reachable from every test in this package, and `go test -race` runs these in parallel. Same shape, same reason; do not "optimise" it into a var.

### Data flow

```
finRecordInputs
  ├─ Rows []reachProc ──── drop Command+Needles ──▶ []finRecordProc   (3 ints/row)
  ├─ ClaudeCommand ──────▶ tdnRunnerFromArgv ──▶ RunnerFromArgv (constant string)
  │                                                    │
  ├─ RunnerFromEnv ───────────────────────────────────┼─▶ finRecordRunnerAgreement
  │                                                    │      (label only)
  │                                                    ▼
  │                                             RunnerAgreement
  ├─ Liveness []pinStateOutcome ──── carried whole ──▶ Liveness
  ├─ Attribution finAttributeRecord ─ carried whole ──▶ Attribution
  ├─ Trailer finTrailerRecord ─────── carried whole ──▶ Trailer
  ├─ ExitCode, ClaudeVersion ──────── carried ────────▶ ExitCode, ClaudeVersion
  └─────────────────────────────────── trailDetail ───▶ Detail  (counts only, ≤470 B)
```

`Command` and `Needles` are dropped at the single conversion point, and `ClaudeCommand` is reduced to one of three constant strings. Those are the only two places argv enters the builder, and neither has an outbound edge that carries bytes.

### Reused, not rebuilt

- `trailDetail` (`trailer_admissibility_test.go:206`) — **do not define `finDetail`.** Settled twice on the same reasoning (`finding_attribution_fanout_test.go:37-44`, `finding_staging_gate_test.go:73-81`): `trailDetail` carries no decision, so a twin would only fork the cap.
- `finAttributeFanOut` — call it; do not rebuild an attribution, do not re-parse pyry's stderr (`tdnClassifyReapLog` owns that).
- `trailReapLine(count int, pgids string)` — synthetic reap stderr for the attribution input. `pgids` is a **string** rendered into the slog line, not a slice.
- `tdnFixturePtyArgv` / `tdnFixtureStreamArgv` (`teardown_liveness_probe_test.go:897`, `:902`) — AC3's argv fixtures already exist. Do not write new ones.
- `trailRunOutcomeValues` / `finOutcomeValues` — if the carried outcome is checked at all, call the shipped lists; never restate the eleven or the seven.

---

## Concurrency model

None. Every symbol in this file is pure over its inputs: no goroutines, no channels, no context, no clock, no filesystem, no process table. The builder is safe to call from parallel subtests because it holds no state and mutates none of its inputs — the input slices are read and their elements copied, never retained by reference into a mutable shared structure.

One consequence worth a comment: `finRecordBuild` must **copy** `Rows` into a fresh `[]finRecordProc` (it is converting anyway) and should not alias the caller's `Liveness` slice backing array in a way that lets a later caller mutation reach a built record. Carrying the slice header is the family's shipped precedent (`trailRunReadings.Liveness`) and is fine; just do not document it as a defensive copy when it is not.

---

## Error handling

There are no errors to return. Following the family contract, the builder takes no `*testing.T`, returns no `error`, and never fails a test — *an instrument failure observed mid-turn is a datum to publish, not a reason to abort the turn*.

The failure modes that exist are **inputs that misrepresent a run**, and each is answered by design rather than by a check:

| Failure mode | Answer |
|---|---|
| A caller hands an unobserved exit as `0` | Documented caller obligation to hand `pinExitStatusUnknown`. Not validated — no such miswrite has been observed, and no builder in this family validates. |
| A caller transposes `RunnerFromEnv` and `ClaudeCommand` | Impossible: named fields on `finRecordInputs`. |
| The argv read returns `indeterminate` | A third verdict, published as such. Never collapsed into `disagree`. |
| Liveness collapsed to a boolean | Prevented by carrying the four-valued `pinState*` string. AC1's round-trip is the enforcing test. |
| The trailer observation is re-read | Impossible: the builder's input struct has no `trailObservation` and no `trailScanResult` field. |
| A Detail grows past the cap and truncates a leak away | Counts-not-pids rule + the per-row headroom assertion in AC4. |

---

## Testing strategy

Five test functions, mirroring the family's 4–6. All offline; run with:

```
go test -race -tags e2e_realclaude -run '^TestFinRecord' -v ./internal/e2e/realclaude/
```

### 1. `TestFinRecordCarriesEveryMatchedRow` (AC1)

Two-row input — a `zsh -c` wrapper and the command it launched, sharing a pgid, one the other's parent.

- Both rows present in `Rows`, each with its three integers unchanged.
- The row count is 2 — the count `#1268` mutation-tested (dropping the wrapper cut it 2→1).
- The pgid the two rows share is equal on both, and one row's `PPID` equals the other's `PID`. Assert the *relationship*, not just the values: that relationship is the claim the integers exist to let a reader check.
- Each liveness verdict is identified to a row. **Assert the cross-reference, not the echo:** every `Liveness[i].PID` is a member of the set of `Rows[*].PID`. Checking only that the handed-in outcomes come back is a tautology over a pass-through field; checking that they land inside the published row set is the claim AC1 is making.
- `ExitCode`, `ClaudeVersion` carried; `Attribution` and `Trailer` present.

### 2. `TestFinRecordLivenessIsConsumedAsHanded` (AC1, second half)

A row set driving all four `pinState*` values through the builder in one call, each pinned carried out unchanged and distinct.

This is a pass-through at the value level, and what it bites is at the **type** level: a record collapsing the read to a boolean maps `exited-but-not-yet-reaped` and `instrument-failed` onto the same "not alive" and cannot return both. Drive all four in a single record so the test fails to even express itself against a `bool` field. Assert the four carried verdicts are four *distinct* values, so "alive" stays distinguishable from "exited but not yet reaped", and an instrument failure from both.

Build the inputs by hand (§"Hand-built liveness"); source the four strings from `finRecordLivenessValues()` so a renamed constant is a compile error rather than a silently stale literal.

### 3. `TestFinRecordEmbedsTrailerRecordWhole` (AC2)

- Build a `finTrailerRecord` via `finTrailerBuild`, hand it in, and pin `rec.Trailer` equals it — `reflect.DeepEqual` is fine, the type is ten scalars.
- The structural half: pin that `finRecordInputs` has no `trailObservation` and no `trailScanResult` field, so `trailScanResult.Trailer` is not reachable from this record. A reflection walk over `reflect.TypeOf(finRecordInputs{})` asserting no field's type is either is the honest form; a comment alone is not, because the point is that a *later edit* cannot add one silently.

### 4. `TestFinRecordRunnerAgreement` (AC3)

Table, three rows, each driving `finRecordBuild` end to end rather than calling the comparison helper directly — the record is what AC3 is about. Hand `reachRunnerPathFromEnv` an explicit delta (below), never an empty one.

| Argv fixture | Env reading | Expect |
|---|---|---|
| `tdnFixtureStreamArgv` | ptyrunner | `disagree` — the record shows the disagreement, not a single label |
| `tdnFixturePtyArgv` | ptyrunner | `agree` — **the row that fails against a whole-string comparison**; assert the two full readings are *unequal* in the same subtest, so the test states why label-only is required |
| argv with neither marker | ptyrunner | `indeterminate` — not `disagree` |

**Pin `PYRY_USE_STREAMJSON` in the delta rather than relying on it being unset.** `reachRunnerPathFromEnv` reads the ambient `os.Getenv` first and only then lets the delta override (`background_reach_probe_test.go:1103-1108`), so an empty delta makes the env-side reading depend on the operator's shell. Hand it `[]string{"PYRY_USE_STREAMJSON=0"}` so the reading is the test's.

Also pin that `RunnerFromArgv` is one of `tdnRunnerFromArgv`'s three answers and that the argv string appears in neither runner field — the reduction is the point.

### 5. `TestFinRecordCarriesNoCapturedBytes` (AC4)

Modelled on `TestFinTrailerRecordCarriesNoCapturedBytes` (`finding_trailer_evidence_test.go:623`). Plant `trailNeedle` in the `Command` of **every** input row and in `ClaudeCommand`.

- **Non-vacuity precondition first.** Assert the needle is present in each planted input before building. Unlike #1290 there is no inbound cap to defeat — the plant is raw input — but a fixture that lost the needle makes the whole sweep theatre, and the precondition is what says so at the point of failure.
- **Headroom, asserted on the built record, per row.** `reachMaxCommandBytes - len(rec.Detail) >= len(trailNeedle)`. Failure message must name the record and say the long-form argument belongs in a comment.
- **Named per channel** so the failure says *which* leaked: `rec.Detail` separately from the marshalled sweep.
- **The byte sweep:** `json.Marshal(rec)` then `bytes.Contains(encoded, []byte(trailNeedle))`. This one is genuinely recursive — marshalling walks the embedded sub-records, the row slice and the liveness slice — so it covers the nested surface.

#### AC4 — what to copy and what not to

**Do not copy #1290's top-level forbidden-key scan.** Its own closing comment says why: that scan is valid *because* `finTrailerRecord` is flat — *"Lifted onto a record with a struct-valued field it would never examine the inner keys."* `finRecordRun` has four struct- or slice-valued fields, so the same loop here would inspect only ten top-level keys, miss every nested one, and read as a structural guarantee it is not providing. Vacuous coverage is worse than none.

The structural guarantee here comes from `finRecordProc` having three `int` fields — there is no argv-shaped key to find, at any depth. State that in the comment in place of the key scan. The full multi-input forbidden-key sweep across every artifact input is **#1286's**, including the `ToolStderr` exact-key exemption, and is not restated here.

### AC5 — offline, structurally

Held by construction and asserted in the file header in the family's wording (`finding_trailer_evidence_test.go:9-11`): no live claude, no credentials, no network, no daemon, no subject process, no process-table read, no env gate, no `t.Skip`.

Nothing on this path may exec. A `grep` for `exec.` is **not sufficient** on its own: every route off this path runs through a *shipped helper* that execs internally, so the check has to name the helpers, not the stdlib package. The full forbidden set, and what each one would drag in:

| Symbol | Why it is off the path |
|---|---|
| `probeClaudeVersion` (`background_trigger_probe_test.go:606`) | execs the binary. The version is a caller-supplied string instead. |
| `resolveClaudeBin` (`resilience_test.go:282`) | calls `t.Skipf` when claude is absent. |
| `WithWorktreeAuthenticated` (`fixtures.go:96`) | the credentials gate — AC5's "no credentials". |
| `pinReadState` (`process_pin_liveness_test.go:275`) | execs `ps` at `:293`. |
| `pinExit1` (`:1088`) | execs `false` to borrow an `*os.ProcessState`. |
| `pinScanArgv` (`:191`) | execs the process-table read. |
| `probeProcessSnapshot` (`background_trigger_probe_test.go:867`) | execs `ps`. |
| `tdnScan` (`teardown_liveness_probe_test.go:482`) | wraps `pinScanArgv`. |
| `holdProbeFIFO` (`background_trigger_probe_test.go:663`) | spawns a subject process and registers cleanup. |

Pure-over-bytes and therefore **allowed** if ever needed: `pinMatchArgvExcluding` (`:173`), `probeDescendantsFromPS` (`:891`), `pinClassifyState` (`:332`) — all take bytes rather than reading the table. None is needed by this design.

Verification greps — **use these exact forms**:

```
grep -nE '\bt\.Skipf?\(' internal/e2e/realclaude/finding_run_record_test.go   # expect no matches
grep -nE '\bexec\.'      internal/e2e/realclaude/finding_run_record_test.go   # expect no matches
grep -nE '\b(probeClaudeVersion|resolveClaudeBin|WithWorktreeAuthenticated|pinReadState|pinExit1|pinScanArgv|probeProcessSnapshot|tdnScan|holdProbeFIFO)\b' \
                         internal/e2e/realclaude/finding_run_record_test.go   # expect no matches
```

The word boundary and the open paren on the first are load-bearing. #1290's spec wrote it as a bare `t.Skip` grep, which matched that file's **own header prose** (`no t.Skip.`) and so could never report clean — a check that cannot pass is as useless as one that cannot fail.

---

## Scope

**In:** the record type, its row type, the input struct, the builder, the runner-agreement reduction and its label helper, the liveness fixture helper, and the five tests.

**Out** (from the ticket, restated so the developer does not drift):

- The trailer read and the outcome union — **#1290, merged, embedded whole.** Do not re-read the observation, do not re-derive the eleven outcomes or the staging tier's seven; both ship membership predicates.
- The artifact writer and its full multi-input no-captured-bytes sweep — **#1286.** AC4 here is this record's own construction claim only.
- Any live claude or staged turn. Turn accounting (`num_turns`, `--max-turns`) — #1234. The headless `PYRY_USE_STREAMJSON=1` path — #1237. The interactive turn-state surface — #1227. What survives the reap after pyry's exit — #1231.
- Re-parsing pyry's stderr for the reap line — `tdnClassifyReapLog` owns it, `finAttributeFanOut` is its consumer.
- Sibling prefixes `finWrite*`, `finGather*`, `finStage*` — do not define those here.
- **No `docs/knowledge/codebase/1291.md`.** The documentation phase owns it, after merge.

---

## Scope self-check

**Production source files** (`*.go` excluding `*_test.go`): **0.** Everything lands in one new `*_test.go` under a build tag, so the ≥5 gate does not bind. Because that gate reads as trivially satisfied for this whole ticket family, the size was calibrated empirically instead, against the three merged siblings rather than against a projection:

| Ticket | AC count | Shipped | Code lines (non-comment, non-blank) | PR commits |
|---|---|---|---|---|
| #1280 | 5 | 1 new file, 767 lines | 411 | 3 |
| #1284 | 5 | 1 new file, 790 lines | 399 | 3 |
| #1290 | 5 | 1 new file, 697 lines | 354 | 3 |
| **#1291** | **5** | **1 new file, ~700–780 projected** | **~400 projected** | — |

All three merged at the standard three-commit shape (spec / implementation / docs) with no salvage commit and no `error:*` label. #1291 is the same family, the same package, the same file shape, the same AC count, and is *less* novel than its siblings — three of the four things it carries are already built and merged, and the only new reduction is a label comparison over a three-value space.

The high total-line count is comment, not code: this package's house style runs ~40% essay-length doc comments, which is why the raw file lengths sit near the ~600-line advisory while the code sits at ~400. Recorded here so the next architect in this family does not have to re-derive it.

**Red lines:** 1 new file (≤3 ✓) · 3 new types (≤5 ✓) · 5 ACs (≤5 ✓) · no state machine, so no reject-branch fan-out ✓.

**Edit fan-out:** additive only. `codegraph_impact` was not needed — the new file introduces symbols under a prefix the census shows is unused and modifies no existing file, so there are **0 consumer call sites** (≤10 ✓).

**Identifier census** (re-run at `1b673b6`, against a known-taken control so a broken recipe cannot report a free prefix):

```
grep -rn '\bfinRecord'  internal/e2e/realclaude/ | wc -l   →    0   (free)
grep -rn '\btrail[A-Z]' internal/e2e/realclaude/ | wc -l   → 1048   (control, non-zero ✓)
```

Siblings on main, so a collision is a compile error rather than a latent conflict: `finAttribute` (53), `finOutcome` (153), `finTrailer` (30), `finDetail` (3). Unclaimed: `finWrite`, `finGather`, `finStage` (0 each — sibling tickets own them).

**File overlap:** `git fetch origin --prune` then a diff of every `origin/feature/N` branch against `main`. Three branches touch this package — #1260 (`dropped_line_capture_test.go`), #1281 (`finding_run_gather_test.go`), #363 (`fixtures.go`). None touches `finding_run_record_test.go`, which does not yet exist. No `addBlockedBy` needed.

---

## Open questions

1. **The `agree` / `disagree` / `indeterminate` value strings.** Spec'd as bare words. If #1286's artifact renderer wants them parenthesised-with-reason like the runner readings themselves, that is a renderer concern — the record's field is a verdict, not a sentence, and the Detail is where the reason goes.
2. **Whether `RunnerAgreement` should ever be recomputed by a consumer.** It should not: it is a reduction the record publishes, in the same position as `finAttributeRecord.Selected`. If #1286 finds itself re-deriving it, that is a signal the field is under-documented, not that the consumer is right.
3. **`ExitCode`'s unobserved-run sentinel is a caller obligation, not a validated one.** Consistent with the family (no builder validates), and no miswrite has been observed. If a live run ever publishes `exit_code: 0` for a pyry that did not exit, the fix is a `PyryExited bool` alongside it — mirroring `trailRunReadings.PyryExited`, whose zero value points the safe way — not validation inside the builder.

---

## Security review

**Verdict:** PASS *(first pass FAILed on three MUST FIX; all three are fixed above and the checklist was re-walked from the top)*

**Findings:**

- **[Trust boundaries]** No findings. One boundary, one crossing point: untrusted process-table bytes → a record pasted into a public issue. Both inbound argv channels are reduced inside `finRecordBuild` and nowhere else — `[]reachProc` → `[]finRecordProc` drops `Command`/`Needles`, and `ClaudeCommand` → `tdnRunnerFromArgv` returns one of three **constant** strings (every arm at `teardown_liveness_probe_test.go:773-789` returns a literal; no input byte reaches the return). Downstream holds only the reduced types. The three inherited sub-record channels were each checked rather than assumed: `pinStateOutcome.ToolStderr` **is** capped (`reachCapCommand` at `process_pin_liveness_test.go:341`), `StateColumn` is `pid=,ppid=,stat=` with no command column, and `finAttributeRecord` is documented trap-free by its own enforcing test.

- **[Tokens, secrets, credentials]** MUST FIX ×2, both fixed. (a) A local process's argv can carry a bearer token, and `in.Rows[i].Command` is in the builder's reach; the realistic slip is not a deliberate quote but a `%v` verb applied to an input struct or slice, which renders every `Command` verbatim from a line that reads as ordinary debug formatting. The Detail rule now names that mechanism explicitly and requires derived scalars only. (b) AC5's forbidden-symbol list omitted `WithWorktreeAuthenticated` (`fixtures.go:96`) — the credentials gate itself. Now listed. No token is generated, stored, compared or rotated by this design; `ps -E`/`-Eww` is never reached because nothing on this path reads the process table at all.

- **[File operations]** No findings — not applicable by design decision, not by omission: `finRecordBuild` and all five tests touch no filesystem. No path is constructed, canonicalised, opened or created. The artifact **writer** is #1286 and owns file mode, atomicity and path handling.

- **[Subprocess / external command execution]** MUST FIX, fixed. The original verification recipe was `grep -nE '\bexec\.'` plus four named symbols — **bypassable**, because every route off the offline path runs through a shipped helper that execs internally rather than through a visible `exec.` call. `pinScanArgv`, `probeProcessSnapshot`, `tdnScan` and `holdProbeFIFO` all execed and none was named. The forbidden set is now enumerated as a table with the reason per symbol, and separated from the three pure-over-bytes helpers (`pinMatchArgvExcluding`, `probeDescendantsFromPS`, `pinClassifyState`) that are safe if ever needed. No `sh -c`, no environment inheritance, no signal handling — nothing is spawned.

- **[Cryptographic primitives]** No findings — not applicable. No randomness (the file is a pure projection; even the fixture helper is deterministic), no hashing, no key material. `finRecordRunnerAgreement` compares two runner **labels**, not secrets, so constant-time comparison is not indicated.

- **[Network & I/O]** No findings. No sockets, no descriptors, no reads. The size-limit analogue is `reachMaxCommandBytes`, and it is addressed in two places: the per-row headroom assertion on `Detail`, and the counts-not-pids content rule that keeps `Detail`'s length independent of input size. `Rows` and `Liveness` are themselves unbounded in length — **considered and declined**: they come from the probe's own needle-matched scan (a live run of this shape matches two rows), capping them would discard the evidence the record exists to publish, and the exposure is artifact size rather than a leak.

- **[Error messages, logs, telemetry]** No findings. The record has no logging and emits no telemetry. `Detail` is its one operator-visible sentence and its content rule is pinned rather than left to judgement, with the likeliest slip (quoting an embedded sub-record's `Detail` — this record embeds **two**) named. One deliberate acceptance: AC4's failure message prints the leaking `Detail` on failure, matching `finding_trailer_evidence_test.go:668`. That is how a leak is diagnosed, the needle is synthetic, and this file's tests run offline against hand-built fixtures only.

- **[Concurrency]** No findings after one fix folded in. Everything is pure — no goroutines, no channels, no clock, no shared state. The one real hazard is Go-specific and easy to reintroduce: `finRecordLivenessValues` must be a **function, not a package-level `var`**, because a shared backing array is reachable from every test in a package that runs under `go test -race` in parallel. That is the stated reason for `trailRunWellFormed` (`trail_run_outcome_test.go:608-610`), `trailRunOutcomeValues` and `finAttributeOrder`; the spec now carries it so the shape is not "simplified" away.

- **[Threat model alignment]** No findings. The governing threat model is `reachMaxCommandBytes`' own (`background_reach_probe_test.go:117-122`): a local process can read the process table, so it could spawn a process carrying this run's needle in its argv to place **attacker-chosen text into an artifact the operator pastes into a public issue** — needing local code execution plus a race inside the held window, past the probe's threat model, with the 512-byte cap bounding the blast radius. This record answers it more strongly than a cap: `finRecordProc` is three `int` fields, so the matched-row channel is closed **structurally** rather than bounded. Two residual channels are named rather than closed here:
  - `finTrailerRecord.StopReason` — model-influenced and crossing **uncapped**, bounded by the wire's shape and by nothing this record does. Inherited: #1290 names it at `finding_trailer_evidence_test.go:128-133`, and AC2 requires the sub-record be carried whole. **OUT OF SCOPE — owner #1286**, whose multi-input sweep must not plant a needle in a field this record is required to carry verbatim.
  - `pinStateOutcome.ToolStderr` — capped, and #1281 settled that a forbidden-key sweep meeting that key defuses by **exact-key exemption, never a prefix rule**. **OUT OF SCOPE — owner #1286.**

**Reviewer:** architect (self-review per `architect/security-review.md`)
**Date:** 2026-08-04
