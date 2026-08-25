# #1763 — drive all three `initialize` send-point arms against a live claude

Ticket: https://github.com/pyrycode/pyrycode/issues/1763 · Size: **S** · Labels: `security-sensitive`, `needs-real-claude`

Test-only. Two files change — `internal/e2e/realclaude/initialize_control_probe_test.go` and
`internal/e2e/realclaude/initialize_control_names_test.go` — plus three artifacts the live run
generates under `internal/e2e/realclaude/testdata/`. **No production file changes.**

---

## Files to read first

Symbols, not lines. Resolve each with `codegraph_search` / `codegraph_node`, then Read what you need.

| Where | Symbol | What to extract |
|---|---|---|
| `internal/e2e/realclaude/initialize_control_probe_test.go` | `runInitControlChild` | The driver being parameterised. Its doc carries three rules this slice must obey verbatim: a **fresh redactor per arm** (unlocked counters, a shared one races and misattributes), a **shared scanner is safe** (append-only during construction, read-only after), and **never format the scanner** (it holds two live credentials). Read all three before touching the signature. |
| same file | `initControlChildBudget`, `initControlTurnBudget`, `initControlControlBudget` | The three constants AC 3's assertion derives both sides from. One of them changes value; the other two do not. |
| same file | `initControlPrompt`, `initControlMaxTurns`, `initControlRequestID`, `initControlModel`, `initControlWorkdirName` | The prompt constant becomes a pair; the other four are unchanged and their docs say why. |
| same file | `TestRealClaude_InitializeControl_Capture` | **Deleted.** Read its `len(rec.ControlResponses) == 0` fatal — AC 2 deliberately overrides it. |
| same file | `TestInitControlProbedArm_IsExactlyOneDeclaredNonEmptyArm` | **Deleted** with its subject. |
| same file | `TestInitControlScanApplied_RecordsAnArmedNothingClassForAnAbsentPath`, `TestInitControlSummarize_ReadsAllThreePlacements` | The two offline tests that stay, and the shape (`t.Parallel`, no spawn, no disk) the new offline budget test copies. |
| `internal/e2e/realclaude/initialize_control_names_test.go` | `initControlArm`, `initControlArms` | The table gaining two columns. Its doc names itself as the growth point for per-arm drive-sequence behaviour and bans a second table keyed by these ids. |
| same file | `initControlProbedArm` | **Deleted.** Its own doc and the `probed` field's doc both say they should not survive this work. |
| same file | `initControlArmFixtureName` | Mints the per-arm path. AC 5's write-set assertion compares against exactly this namer. Unchanged. |
| same file | `TestInitControlArmFixtureName_AvoidsCommittedNamesStaysDistinctAndContained` | Ranges `initControlArms` for its `arms` slice and its distinctness subtest. Already green; must stay green — the field additions must not disturb it. |
| `internal/e2e/realclaude/set_permission_mode_probe_test.go` | `runSetModeChild` | **The model for this slice's driver.** The two-turn sequence, the conditional control write on a control arm, the `baseline := rec.resultCount()` idiom for turn 2's wait, and why turn 2 is driven unconditionally on control arms. |
| same file | `TestRealClaude_SetPermissionMode_InBandProbe` | The sequential per-arm `t.Run` loop and the `missing` guard that refuses to compute a cross-arm claim from a partial run. |
| same file | `setModePromptOne`, `setModePromptTwo` | Why the two prompts must differ from each other. |
| same file | `setModeWaitFor`, `setModeTurnLine`, `setModeRecorder`, `setModeResponseIDMatches`, `setModeScanMax` | Reused verbatim; none of them changes. |
| `internal/e2e/realclaude/initialize_control_window_test.go` | `initControlReadWindow` | The window read. Unchanged. Its precondition paragraph (`0 <= anchor <= len(lines)`, both edges legitimate) is what makes anchor 0 correct for `before_first_turn`. |
| `internal/e2e/realclaude/initialize_control_record_test.go` | `initControlFixtureRecord` | **Read-only in this slice.** Its `SendPointIndex` paragraph and its `control_no_request` paragraph state the contract AC 4 asks you to implement. Its "DO NOT ADD A PRESENCE FLAG BESIDE THIS ANCHOR" instruction applies here. |
| `internal/e2e/realclaude/initialize_control_writer_test.go` | `writeInitControlFixture`, `scanInitControlFixture` | Unchanged. Read the deny-scan-before-the-first-filesystem-call ordering and the fact that the writer returns the path it minted — AC 5 consumes that return value. |
| `docs/knowledge/features/e2e-realclaude.md` | § "What's there today" → the `initialize_control_*_test.go` entries | The family's history in one place: why the arm dimension was collapsed and then restored, and what each committed artifact holds. |

---

## Context

The daemon will ask its supervised child for claude's model list by writing a `control_request` with
subtype `initialize` on the child's held-open stdin. #1688 measured that round trip at **one** send
point — after a completed turn — and committed the bytes. What is still unmeasured is **which send
points are answered at all**: the daemon would rather ask once at spawn than pay for a turn first,
and #1689's trigger is placed on whichever answer the measurement returns.

`initControlArms` has declared the three arms since #1712, and `initControlArmFixtureName` has minted
one path per arm since #1722. What is missing is the run: the driver still sends at the single row
`initControlArms` marks `probed`, and both that column and `initControlProbedArm` exist only so a
one-arm run can name its arm without a second spelling of the identifier. The table's own doc says
neither should survive this work.

Two facts shape the design more than the loop does:

1. **Claude emits `system`/`init` per turn, not at spawn.** So "no further init line after the
   request" observed without driving a further turn is empty by construction. `runSetModeChild`
   records this in its own words and drives turn 2 on its control arms for exactly that reason.
   Every arm here — the no-request control included — therefore drives a full turn after its send
   point.
2. **The outer deadline is currently smaller than what the drive sequence can spend.**
   `initControlChildBudget` is 3 minutes; a two-turn writing arm's per-step waits sum to
   `2×90s + 45s = 225s`, and the no-request arm's to `2×90s = 180s`, which already equals the
   deadline before spawn is counted. Every per-step budget exists so an absence means absence rather
   than impatience — `initControlControlBudget`'s doc says so — and a deadline that can fire first
   voids that guarantee for precisely the reading this ticket takes. AC 2 makes a tripped deadline a
   *passing* recorded outcome, so nothing reddens when it happens; AC 3's derived assertion is what
   earns that leniency.

**This work does not warrant an ADR.** It implements a contract two existing docs already state
(`initControlArm`'s growth-point paragraph, `initControlFixtureRecord`'s `control_no_request`
paragraph); the durable knowledge is the three committed artifacts and the package overview entry the
documentation phase folds in.

---

## Design

### 1. `initControlArm` grows two columns; `probed` is deleted

The arms vary on exactly two things: **where in the two-turn sequence the send point sits**, and
**whether a control request is actually written there**. Both go on the row, per that table's own
instruction. No second table, no `switch` on `id`, no index into the slice.

```go
type initControlArm struct {
	id                      string
	sendPointAfterFirstTurn bool // false: before turn 1's line. true: between the two turns.
	sendsRequest            bool // whether a control_request is written at that point.
}

var initControlArms = []initControlArm{
	{id: "before_first_turn", sendsRequest: true},
	{id: "after_completed_turn", sendPointAfterFirstTurn: true, sendsRequest: true},
	{id: "control_no_request", sendPointAfterFirstTurn: true},
}
```

The identifiers, their order and their meaning are unchanged — this is the same vocabulary with two
behaviour columns added, the way #1722 added `probed` and this slice removes it.

Doc-comment obligations on the new columns (each is the reason a later reader will need):

- `sendPointAfterFirstTurn` — it positions the send point, **not** the request. `control_no_request`
  carries `true` with no request: its anchor is read at the equivalent point its drive sequence
  reaches, which is what makes the three arms' windows comparable. That is
  `initControlFixtureRecord`'s stated contract and AC 4's placement requirement.
- `sendsRequest` — false marks a control arm. Same role `targetMode == ""` plays in `setModeArm`,
  spelled as its own column here because this family's request payload does not vary per arm.
- The two columns are **independent on purpose** and nothing checks them against `id`. An arm whose
  id says one thing and whose columns do another produces an artifact that says so — a
  `control_no_request` row with `sendsRequest` would commit `control_request_sent` non-null under
  that name. Do not add a substring check on `id` to guard it: that is a second spelling of the
  identifier, which is what the distinctness subtest of
  `TestInitControlArmFixtureName_AvoidsCommittedNamesStaysDistinctAndContained` exists to prevent.

Deleted in the same edit: the `probed` field, `initControlProbedArm`, and
`TestInitControlProbedArm_IsExactlyOneDeclaredNonEmptyArm`.

**Two count claims rot with this deletion and must be corrected**, not left:

- The function-local comment inside `TestInitControlFixtureName_AvoidsCommittedFamiliesAndStaysContained`
  that enumerates "seven identifiers … `initControlProbedArm` and the two tests" — one fewer now.
- The `-run` regex in `initialize_control_probe_test.go`'s header, which names
  `TestInitControlProbedArm_`, and its sentence "The three non-live tests in this file — the
  summariser's table, the probed arm's and the arming completion's". Still three; a different three.

### 2. The driver takes its arm as a parameter

```go
func runInitControlChild(t *testing.T, claudeBin, workdir string, arm initControlArm,
	red *dropcapRedactor, scanner dropcapScanner, versionRaw, versionToken string) (*initControlFixtureRecord, string)
```

`arm` sits after `workdir`, mirroring `runSetModeChild`. The local `arm := initControlProbedArm()`
goes away — this is the change its doc reserved.

**The second return value is the written path**, which the writer already mints and returns and which
the driver currently discards into a log line. AC 5's write-set assertion consumes it.
**Do not put the path on `initControlFixtureRecord` instead**: it is an absolute path under the
operator's pinned `$HOME`, so a field would commit an operator path into a public artifact and put a
new string-bearing field in front of the redaction pass and the deny-scan for no gain.

### 3. The drive sequence

One sequence, shared by all three arms, with the send point placed by the row:

```
[send point if !arm.sendPointAfterFirstTurn]
turn 1  → wait for result   (initControlTurnBudget)
[send point if  arm.sendPointAfterFirstTurn]
turn 2  → wait for result   (initControlTurnBudget, from a baseline count)
stdin close → cmd.Wait() → reader join
```

The send point is one closure, called from exactly one of two `if`s, and it does two things in this
order:

1. **Read the anchor** — `sendPointIndex = len(rec.snapshotLines())`. Unconditional: the control arm
   reads it too, and reading it *before* any write is what keeps the residual on the safe side.
   #1762's paragraph at the existing anchor read states the invariant and must survive the move
   verbatim in substance: nothing claude writes in response to the request may fall before the
   anchor, and only a read taken before the write guarantees that.
2. **If `arm.sendsRequest`** — mint the line with `initControlLine`, log it, write it, then
   `setModeWaitFor(rec.controlResponseCount, 1, initControlControlBudget)` and keep the result in
   `withinWait`.

For a control arm the closure stops after step 1: no request id, no line, no wait. `ControlRequestID`
stays `""`, `ControlRequestSent` stays nil, `withinWait` stays false — the same shape `runSetModeChild`
produces for its control arms, and `setModeResponseIDMatches` already returns false for an empty id
without a branch of its own.

**`ControlResponseWithinWait` is false for the control arm and that is honest, not ambiguous.** No
wait ran, so none was satisfied. A reader separates "the wait expired" from "no wait ran" by `arm`
and by `control_request_sent: null`. **Do not add a presence flag or a fourth state**; the record's
`SendPointIndex` paragraph bans exactly this move one field away, for the same reason.

Turn 2 uses the baseline idiom, not a hardcoded 2: read `baseline := rec.resultCount()` before the
write and wait for `baseline+1`. Copied from `runSetModeChild`, and it is what keeps the wait correct
when turn 1 produced no result line inside its budget.

Both turn waits log-and-continue on expiry exactly as the single turn does today. The existing log
text ("this capture is OFF the one arrangement the ticket pins") describes a one-arm world and must
be reworded: a turn that produced no result line means the *next* step is not reached from a
completed turn, and `turn_boundaries` is what tells a reader which turns closed.

### 4. Two prompts

`initControlPrompt` becomes `initControlPromptOne` / `initControlPromptTwo`. Both stay **tool-free** —
a tool-free turn cannot stall on a permission prompt it can never receive, which is why this run needs
no `--dangerously-skip-permissions` and cannot hit the `default`-posture hang #1595 budgets two
minutes for. They must **differ from each other** so turn 2 is a fresh request rather than one claude
can answer with "I already did that"; `setModePromptOne`/`setModePromptTwo` carry that reason. Ask for
a different single word in each.

`Prompts` in the record literal becomes `[]string{initControlPromptOne, initControlPromptTwo}`.

`initControlMaxTurns` stays `"4"` — ~2× headroom over the two assistant turns two tool-free probes
need. `initControlRequestID` stays the fixed literal: the two writing arms are separate children, so
there is no correlation ambiguity, and a fixed literal keeps the artifacts diffable. Do not mint a
per-arm id.

### 5. The budget arithmetic (AC 3)

A pure helper beside the budget constants, and one offline test.

```go
// initControlArmWaitSum returns the largest total arm's drive sequence can spend
// in PER-STEP WAITS. It MIRRORS runInitControlChild's sequence and must be grown
// with it: two turn waits, plus the control wait on an arm that sends a request.
func initControlArmWaitSum(arm initControlArm) time.Duration
```

`initControlChildBudget` rises from `3 * time.Minute` to **`5 * time.Minute`**. 300s > 225s with 75s
of residual, which is what covers the steps that are *not* per-step waits — `cmd.Start`, the stdin
close, `cmd.Wait` and the inter-step overhead — none of which the sum counts and none of which the
assertion demands. Its doc comment currently says the per-step waits "can sum past it on a fully
stalling run"; that sentence is now false by design and must be replaced: the deadline dominates the
sum, so a trip can only mean a genuinely stalling claude, which is itself a measurement. Name the new
test in that doc as the thing enforcing it.

`initControlTurnBudget`'s doc says "The one probe turn" — now two.

### 6. The live test

`TestRealClaude_InitializeControl_Capture` is **deleted**; `TestRealClaude_InitializeControl_SendPointArms`
replaces it. The deletion is a correctness requirement, not an economy: since #1722 the writer mints
its path from `initControlArmFixtureName(rec.ClaudeVersion, rec.Arm)`, so that test and this run's
`after_completed_turn` arm write the **same filename** — two live children racing for one path, last
writer wins, nothing red.

Shape (mirroring `TestRealClaude_SetPermissionMode_InBandProbe`):

- `resolveClaudeBin`, `WithWorktreeAuthenticated`, workdir `MkdirAll`, `captureClaudeVersion` —
  unchanged, once.
- `newDropcapScanner(home, "", workdir)` — **once, outside the loop.** Shared across arms is safe and
  the driver's doc says why. The empty middle slot is `artifactDir` and the emptiness is the fact the
  record records; do not fill it.
- Sequential `for _, arm := range initControlArms` with `t.Run(arm.id, …)`. No `t.Parallel`: one
  child, one reader goroutine and one pinned `$HOME` at a time.
- `newInitControlRedactor(realHome, home, workdir, os.TempDir())` — **inside the loop, fresh per
  arm.** Mandatory: `dropcapRedactor`'s counters are unlocked and its census accumulates across
  calls, so a shared one would both race and report one arm's substitutions against another's.
- Collect `(record, path)` per arm id.

Then AC 5's write-set assertion, over the paths the run actually wrote:

- If any declared arm produced no path — a `-run` filter, or an instrument fatal in a subtest — log
  that the write-set check is **UNAVAILABLE**, name the missing arms, and return without asserting.
  A filtered run is not "one suite run"; `TestRealClaude_SetPermissionMode_InBandProbe`'s `missing`
  guard is the precedent, and computing a set claim from a partial run is how a green run comes to
  mean nothing.
- Otherwise assert: exactly `len(initControlArms)` paths, pairwise distinct, and each equal to
  `filepath.Join(packageDir(t), "testdata", initControlArmFixtureName(versionToken, arm.id))`.
  Failure message names the arm and both paths.

Per-arm logging stays the driver's job — it already logs arm, line count, `within_wait`, subtype,
models, exit and the redacted responses. Add at most one per-arm summary line at the top level.
**No cross-arm verdict here**: subtracting the control arm from the two measurement arms is #1764's,
and a verdict computed in two places is a second source of truth.

### 7. What each arm records

| arm | anchor | `control_request_sent` | window covers |
|---|---|---|---|
| `before_first_turn` | before turn 1's line (0 in practice, read not assumed) | the request | the whole run — both turns' `init` lines and both `result` trailers |
| `after_completed_turn` | between the turns | the request | turn 2 |
| `control_no_request` | between the turns | `null` | turn 2 |

The last row is what makes the other two readable: the `init` count and trailer list in the control
arm's window are what #1764 subtracts. `initControlReadWindow` is called once per arm with that arm's
anchor and is otherwise untouched.

---

## Concurrency model

Unchanged in structure; three times sequentially instead of once.

- **One child at a time.** Arms run in sequence inside `t.Run`, no `t.Parallel` at any level of the
  live test. The workdir and the pinned `$HOME` are shared across arms, as `runSetModeChild`'s four
  arms already share theirs.
- **One reader goroutine per child**, closing `readerDone` on EOF, which follows either the stdin
  close or the context kill. Nothing outlives its child, so there is never more than one reader
  alive. `scannerErr` is written only before that close and read only after it; that ordering is the
  whole synchronisation for it, and it stays a per-call local.
- **`setModeRecorder` is per child** — a fresh `&setModeRecorder{}` per driver call, already true.
  Its own mutex covers the reader goroutine's `add` against the main goroutine's snapshot and count
  accessors, including the anchor read.
- **The redactor is per arm** (unlocked counters). **The scanner is shared** (append-only during
  construction, read-only after: `addDynamic`/`addDynamicPath` are pointer-receiver and run only
  inside `newDropcapScanner`, while `applied` and `scan` are value receivers that allocate their own
  results). Do not copy the redactor's rule to the scanner, and do not read the scanner's rule as
  licence to share a redactor.
- **`initControlArms` stays read-only.** It is ranged from `t.Parallel()` tests in the names file and
  now from the live loop; never append to it, never reassign it, and do not add the hostile arms from
  the namer's lock to it.

## Error handling

The run **passes on every recorded outcome**. AC 2's fail-set is exactly the fatals that exist today,
and the count of fatal branches does not grow:

| condition | behaviour | why |
|---|---|---|
| pipe / spawn / marshal failure | `t.Fatalf` | broken instrument; nothing to capture |
| credential value in the child's stderr (`initControlScrubbed`) | `t.Fatalf`, naming the variable and nothing else | fail-closed refusal; the message must never quote the leak |
| zero stdout lines captured | `t.Fatalf` | no artifact to commit |
| deny-scan hit in `scanInitControlFixture` | `t.Fatalf` from the writer, before its first filesystem call | fail-closed; nothing is written, not even a `.tmp` |
| **no `control_response` within the wait** | recorded — `control_response_within_wait: false` | **the measurement this ticket exists to take.** The old fatal is deleted |
| `subtype: "error"` refusal | recorded | the error text is #1689's input |
| turn produced no result line in budget | logged, `turn_boundaries` shows which turns closed | recorded, not fatal |
| `context_deadline_tripped` | recorded | AC 2 keeps it passing on purpose. Now that the deadline dominates the per-step sum, a trip means a genuinely stalling claude — a measurement. **Do not tighten this into a fatal.** |
| an arm's subtest fatals | that subtest fails, the remaining arms still run, the write-set check reports UNAVAILABLE | `t.Fatalf` in a subtest ends that goroutine only |

Ordering that must not move: `initControlScrubbed` runs **after** the reader join (so a fatal cannot
strand the goroutine) and **before** both the zero-lines fatal and the write (so a poisoned fixture
never reaches disk at all). The redaction pass sits between the record literal and the write, and
every log site downstream reads the **record's own** fields — never the pre-pass locals — because the
pass assigns fresh values rather than writing through what it was handed.

## Testing strategy

**Offline — must PASS, not SKIP, on a machine with no claude and no credentials.**

New: `TestInitControlChildBudget_ExceedsEveryArmsPerStepWaitSum` in
`initialize_control_probe_test.go`, `t.Parallel`, spawning nothing and reading nothing off disk.
Written as scenarios, both sides derived from the constants — no literal durations anywhere:

- **Subject.** `initControlChildBudget` is strictly greater than the maximum of
  `initControlArmWaitSum` over `initControlArms`. **RED against the constants as they stand**
  (180s vs 225s), green after the raise. The failure message states both totals and says what a
  deadline that can fire first costs: an absent `control_response` then means impatience rather than
  absence, which is the reading this family exists to take.
- **Vacuity control A.** That maximum equals `2*initControlTurnBudget + initControlControlBudget`.
  Sole red for a helper that returns zero, counts one turn, or drops the control term — each of which
  makes the subject pass for the wrong reason.
- **Vacuity control B.** Some declared arm sums to `2*initControlTurnBudget` exactly. Sole red for a
  helper that ignores `sendsRequest`, and for a table that has lost its control arm.

Unchanged and must stay green: `TestInitControlSummarize_ReadsAllThreePlacements`,
`TestInitControlScanApplied_RecordsAnArmedNothingClassForAnAbsentPath`,
`TestInitControlReadWindow_ReadsAnAnchoredWindowOfChildOutput`,
`TestInitControlArmFixtureName_AvoidsCommittedNamesStaysDistinctAndContained` (its `arms` slice
ranges the table you are editing), the writer's round trip, and `TestFinOfflineFilesReachNoExecHelper`
(no `offline_exec_ban_test.go` entry changes — the edits add no I/O to the names file and the probe
file correctly carries no entry).

```
go test -tags e2e_realclaude -race -count=1 -v \
  -run 'TestInitControl|TestFinOfflineFilesReachNoExecHelper' ./internal/e2e/realclaude/
```

**Live — one run, three children, two turns each.** #1688's one-turn child completed in 3.3s wall
clock, so budget seconds per arm, not minutes. **Do not iterate against a live child**: the
wall-clock and token risk here is the run, not the typing. Get the offline half green first, re-read
the driver end to end, then run once.

```
go test -tags e2e_realclaude -race -count=1 -v \
  -run 'TestRealClaude_InitializeControl_SendPointArms' ./internal/e2e/realclaude/
```

After the run, verify before committing:

- `git status` shows exactly three new files under `internal/e2e/realclaude/testdata/`, one per arm.
- `git diff --stat` shows **`initialize_control_v2.1.239.json` unmodified** — AC 4's byte-identity
  clause. If it moved, something still writes the one-arm name.
- Each new artifact carries its own `arm`, its own `send_point_index`, and the two
  `after_send_point_*` keys.
- **Read each artifact before committing it** — it is child output going to a public repo. The
  deny-scan is the fail-closed net, but an empty `redaction` census is *not* a clean bill of health:
  `dropcapRedactor` installs no rule for an empty value, so a class armed with `""` reports `[]`
  honestly while the file still carries the path.

**Read the count of tests that ran, never the exit code.** This package is behind the
`e2e_realclaude` tag: `make check` never compiles it, and the suite exits 0 both on a build failure
and on a full credentials skip.

## Open questions

- **Whether a pre-turn `initialize` is answered at all is the unknown this run resolves.** If
  `before_first_turn` comes back unanswered, that is a green run and a recorded outcome; #1689 places
  its trigger accordingly. Do not treat an unanswered arm as a failed run.
- **The version token is whatever the operator's binary reports.** If it is no longer 2.1.239 the
  three artifacts are named for the newer token; AC 4 pins the arm identifiers and the byte-identity
  of the committed file, not the version.
- **The `#1715` cites in `initialize_control_record_test.go` and `initialize_control_writer_test.go`
  are out of scope** for this slice's two-file budget. Within the two files you touch, a cite that
  describes work *this* slice lands should name #1763. The documentation phase folds the rest into
  `docs/knowledge/features/e2e-realclaude.md`.

---

## Security review

**Verdict:** PASS

**Findings:**

- **[Trust boundaries]** No findings. Exactly one boundary and it is unchanged by this slice: the
  child's stdout crosses into the harness through `setModeRecorder.add`, and everything downstream of
  it — `initControlSummarize`, `initControlReadWindow`, `setModeResponseIDMatches` — is total over
  hostile bytes by construction. `initControlReadWindow`'s doc states the obligation (a line it
  cannot decode is skipped, a field it cannot decode leaves its destination at the zero value, and
  nothing returns an error). This slice adds no new reader and calls that one three times instead of
  once. The one *new* untrusted surface is the `before_first_turn` window, which is now the **whole
  run** rather than a tail — `initControlReadWindow` is already total over an anchor of 0, and its
  offline table carries that row explicitly ("an anchor of zero reads every line").
- **[Tokens, secrets, credentials]** MUST-FIX-class hazards exist in this family and the spec
  addresses each; none is left to the developer's judgement.
  - *Fresh redactor per arm.* `dropcapRedactor`'s counters are unlocked and its census accumulates
    across calls. A shared redactor across three arms both races under `-race` and commits one arm's
    substitution counts into another arm's artifact — a census that misreports which classes fired is
    an audit trail that lies. The spec mandates construction **inside** the loop.
  - *Never format the scanner.* `newDropcapScanner` reads `CLAUDE_CODE_OAUTH_TOKEN` and
    `ANTHROPIC_API_KEY` into its needles, so a `%v`/`%+v`/`%q` on the scanner, a needle, or the needle
    slice prints a live credential into a run log this pipeline salvages. `initControlScrubbed` does
    not catch it — that guard reads the *child's* stderr. The spec adds three new log sites' worth of
    surface (per-arm summary, write-set failure message, budget-test failure message) and none of
    them may name the scanner. `initControlScanApplied`'s map is the safe diagnostic: keyed by class,
    valued by bool.
  - *No new record field.* The written path is returned beside the record rather than stored on it,
    precisely because it is an absolute path under the operator's pinned `$HOME`. A field would
    commit an operator path to a public repo and add a string-bearing field the redaction pass visits
    by hand and would silently miss.
  - *Three artifacts instead of one.* The deny-scan in `scanInitControlFixture` runs per write, so
    the fail-closed net scales with the arm count with no change. Its refusal message deliberately
    prints neither the offending value nor its offset.
- **[File operations]** No findings. Every path is minted by `initControlArmFixtureName`, whose
  containment guarantee is lexical (`versionSlug` folds `[^a-z0-9._-]+` to `_`, so no separator
  survives either column) and is locked by an offline test that spawns nothing. The writer's
  temp-file-plus-rename is unchanged, and the deny-scan runs **before** the first filesystem call, so
  a refused record strands nothing — not even a `.tmp`. The three arms write three distinct paths;
  distinctness is already proven offline, and AC 5's write-set assertion is the live confirmation.
  The one real path hazard this slice **removes** is the collision the ticket names: deleting
  `TestRealClaude_InitializeControl_Capture` is what stops two live children racing for
  `initialize_control_v<slug>_after_completed_turn.json`.
- **[Subprocess / external command execution]** No findings, and one thing deliberately not done.
  The argv is a fixed literal list plus two cost flags; no arm contributes an argument, so no
  arm-controlled value reaches `exec.CommandContext`. No `sh -c`. Both prompts stay **tool-free**, so
  this run keeps needing no `--dangerously-skip-permissions` — the sibling family's flag is exactly
  what must not be copied here, since it would hand three unsandboxed children tool access for no
  measurement gain. The credential reaches the child through the environment while the argv carries
  none, which is why `argv` is recorded and `env` is not and must never be. Kill path is unchanged:
  `exec.CommandContext` with the outer deadline, then stdin close → `cmd.Wait` → reader join.
- **[Cryptographic primitives]** Not applicable, and the reason is stated rather than assumed:
  `initControlRequestID` is a **correlation** token, not a security token — the same reason
  `(*Runner).Interrupt` mints its id from a monotonic counter. A fixed literal is correct here and
  `crypto/rand` would only make the committed artifacts undiffable. `initControlScrubbed` uses plain
  `strings.Contains` rather than `crypto/subtle` because the only party on the other side is the
  claude binary, which was handed the token.
- **[Network & I/O]** No findings. No sockets. The one unbounded-input surface is the child's stdout,
  capped per line at `setModeScanMax` (1 MiB) with an over-long line surfacing in `scanner_error`
  rather than truncating silently, and stderr capped at `stderrFixtureCap` **in the writer's own
  copy**, so the record in memory holds the raw string and the file on disk holds the capped one.
  Both are unchanged. Three arms triple the total bytes committed; that is diff size, not a security
  property.
- **[Error messages, logs, telemetry]** No findings, with one instruction the developer must not
  relax: **never `%+v` the record** into a log or a fatal, which would move up to `stderrFixtureCap`
  bytes of child output out of the bounded file and into an unbounded run log. `arm`, `claude_version`
  and `within_wait` are safe to name because none is child output; the new write-set failure message
  prints paths minted by the namer from those same values. The per-arm response loop must range
  `record.ControlResponses`, never the pre-pass local — that exact substitution leaked unredacted
  bytes past a correctly placed redaction pass once already, which is why the driver's doc states it
  per log site rather than per position.
- **[Concurrency]** No findings. One child, one reader goroutine and one recorder alive at a time;
  the reader exits on EOF (stdin close or context kill) and is joined before any fatal that could
  strand it. The recorder's own mutex covers the anchor read against the reader's appends, and the
  anchor is a single read of an append-only slice — which is also what makes
  `initControlReadWindow`'s `0 <= anchor <= len(lines)` precondition hold structurally rather than by
  a clamp. `initControlArms` is read-only under `t.Parallel()` readers. The redactor's per-arm
  construction is a race fix as much as a correctness fix.
- **[Threat model alignment]** Out of scope for this ticket. This is a test-only measurement harness
  that spawns a local `claude` under the operator's own credentials; it adds no daemon surface, no
  relay path and no wire format. The one threat it does carry — committing operator paths or
  credentials into a public artifact — is handled by #1733's redaction pass and #1748's deny-scan,
  both unchanged here and both scaled per arm by construction.

**Reviewer:** architect (self-review per the security-review checklist)
**Date:** 2026-08-25
