# 1343 — Probe rig: assemble the eight-field staging record and return its gate's decision

**Ticket:** [#1343](https://github.com/pyrycode/pyrycode/issues/1343) — split from #1339
**Size:** S · **Labels:** `size:s`, `security-sensitive`
**Blocker:** #1342, merged at `fde02ff` (PR #1344)
**File:** new `internal/e2e/realclaude/finding_live_assembly_test.go`, `//go:build e2e_realclaude`

---

## Files to read first

Generated from `codegraph_context` plus direct reads. **Note the codegraph gap:** the index covers
the non-build-tagged tree well but under-reports `//go:build e2e_realclaude` test symbols — it
returned `internal/debugbundle.Assemble` as a top entry point for this task and surfaced only 4 of
the ~15 relevant `realclaude` symbols. Everything below was confirmed by direct read at `fde02ff`.
If a line number has shifted under your HEAD, trust the symbol name.

| Path + lines | What to extract |
|---|---|
| `internal/e2e/realclaude/finding_staging_gate_test.go:158-186` | `finOutcomeStaging` — the eight fields, in order, and the no-json-tags rule stated at the type. This is the record you assemble. |
| `internal/e2e/realclaude/finding_staging_gate_test.go:262-366` | `finOutcomeStagingGate` — all seven arms and their order. Read the count arm at `:343-357` closely; `\|\| s.PinWantCount < 1` is what row 3 pins. |
| `internal/e2e/realclaude/finding_staging_gate_test.go:188-201` | `finOutcomeResult` — the two-field publishable type your assembly returns unchanged, and the Detail content rule (it **may** name the two counts; it may never name either command). |
| `internal/e2e/realclaude/finding_staging_gate_test.go:389-409` | `finOutcomeStagedBase()` — the gate's own fixture at `2, 2`. **Contrast only. Never call it.** Its `2` is the gate file's, with its own stated reason. |
| `internal/e2e/realclaude/finding_staging_gate_test.go:411-515` | `finOutcomeGateCases()` — the shipped count mapping (`wrongCount` at `:438`, `zeroCounts` at `:443`). Your test must not re-assert any (counts → outcome) pair against the gate. Read `:433-436` for the house form of a *non-producible fixture stating its reason at itself* — row 4's constant follows it. |
| `internal/e2e/realclaude/finding_staging_fill_test.go:86-105` | `finTranscriptReading` — the three-field boundary type. Your caller-side facts type is its mirror image; copy its doc posture. |
| `internal/e2e/realclaude/finding_staging_fill_test.go:234-264` | `finTranscriptFill` — the signature you call, and `:260`'s `if call.Command == staged` (the second consumer of the staged string; the drift hazard). |
| `internal/e2e/realclaude/finding_staging_fill_test.go:266-292` | `finTranscriptTestDeadline` (10 ms) and `finTranscriptStagedID` — reuse both; declare neither. |
| `internal/e2e/realclaude/finding_staging_fill_test.go:298-349` | `finTranscriptBashBlock` / `finTranscriptAssistantLine` / `finTranscriptResultLine` — the three shipped builders your one transcript is made of. Grow no fourth. |
| `internal/e2e/realclaude/finding_staging_fill_test.go:351-363` | `finTranscriptStagedCaller` — **the fixture to contrast against, never to call.** Its `PinMatchCount: 1, PinWantCount: 1` is the poison AC1 forbids reaching the assembly. |
| `internal/e2e/realclaude/finding_staging_fill_test.go:538-602` | `TestFinTranscriptFill` — what is already proven, so you do not re-prove it. Note `:556-562`: the lengths-only failure-message form. |
| `internal/e2e/realclaude/finding_live_pin_test.go:103-140` | `finLivePinWantRows = 2` and its full reason. Row values only; the identifier must not appear in the assembly's body. |
| `internal/e2e/realclaude/finding_live_staging_test.go:118-137` | `finLiveStageCommand(fifoPath)` — the staged literal you use. Read `:131-134` for why `finOutcomeHoldCommand` is **not** it. |
| `internal/e2e/realclaude/finding_live_staging_test.go:193-206` | `finLiveStageFixtureFIFOPath` — the synthetic path, shipped for exactly this. |
| `internal/e2e/realclaude/finding_live_staging_test.go:1-77` | The blocker's file header — the house form for the doc header you will write. **Do not inherit its `WithWorktree` / `t.TempDir()` prohibition**; see § Prohibitions. |
| `internal/e2e/realclaude/fixtures.go:59-64` | `WithWorktree` — `t.TempDir()` + `t.Setenv("HOME", …)`. The `t.Setenv` is why no subtest here may be `t.Parallel()`. |
| `internal/e2e/realclaude/fixtures.go:148-167` | `ReadJSONL` — it `t.Fatalf`s on an unopenable transcript. That is the one failure path the assembly inherits; see § Error handling. |
| `internal/e2e/realclaude/fixtures_test.go:553-572` + `:21` | `writeFixtureLines` and `testSessionID`. |

---

## Context

A live probe of `pyry agent-run` stages one turn in which claude is asked to `cat` a FIFO that never
closes, the #1223 auto-background trigger fires, and the held processes are pinned during the turn.
Whether that staging actually happened is decided by `finOutcomeStagingGate` over the eight fields of
`finOutcomeStaging`.

Three of those eight are shipped (#1304's `finTranscriptFill` reads them from the run's own JSONL).
The pin reduction that produces two more is shipped (#1338). The staged literal is shipped (#1342).
**Nothing joins them.** The only thing filling the five caller-side fields today is
`finTranscriptStagedCaller`, a #1304 test fixture that hardcodes `PinMatchCount: 1, PinWantCount: 1`
— correct for its own file, and wrong for the rig, whose real expectation is `finLivePinWantRows = 2`.
An assembly that inherited that `1` would send every correctly-staged live run to
`finOutcomePinCountUnexpected`, at the cost of one burned claude turn per attempt.

This ticket ships the join, and only the join: no `pyry` spawn, no real claude, no `ps` exec, no FIFO.

### The hazard the design is shaped around

The staged command has **two** consumers, and they must be the same bytes:

- `finOutcomeStagingGate`'s identity arm compares `StagedCommand` against `IssuedCommand` (`:299`).
- `finTranscriptFill` only reads the trigger result when `call.Command == staged` (`:260`).

Arriving as two independent parameters they can drift, and drift is doubly silent: the identity arm
reports `finOutcomeCommandNotStaged` *and* `TriggerFired` is silently zeroed — one defect, two failure
arms, both discovered on a burned live turn. The design closes this **structurally**: there is exactly
one parameter carrying the staged string, so there is no second value for it to disagree with.

---

## Design

### Two types meet; the assembly is the only place they do

`finTranscriptReading` (shipped) is the three transcript-side fields as a type, and its doc states why:
"the composition returns a value from which the other five are unreachable, so 'the fill neither reads
nor invents them' is structural rather than asserted."

Ship the **mirror image** for the caller side:

```go
// finLiveAssembleFacts is the five caller-side fields of finOutcomeStaging, and
// only those five. NO JSON TAGS — it carries StagedCommand.
type finLiveAssembleFacts struct {
	StagedCommand  string
	RendezvousDone bool
	PinScanErrored bool
	PinMatchCount  int
	PinWantCount   int
}
```

Field names mirror `finOutcomeStaging`'s **exactly**. That is load-bearing, not cosmetic — see
§ The one gap no value-only test can close.

**Why a struct rather than five parameters.** Flat, the assembly takes ten arguments including three
adjacent `string`s, two adjacent `bool`s and two adjacent `int`s. A call site that transposes
`rendezvousDone` and `pinScanErrored` compiles and returns `finOutcomeRendezvousIncomplete` on a
correctly-staged run — a silent wrong verdict discovered on a burned turn, which is the exact failure
class this whole file family exists to move offline. Named fields at the call site remove it.

**Why not pass a partial `finOutcomeStaging`** (the shape `finTranscriptStagedCaller` returns): that
hands the assembly a value on which `BashIssued` / `IssuedCommand` / `TriggerFired` are settable, so
"the three transcript fields come from the transcript" stops being structural and becomes a
convention. The five-field type is what keeps the eight-field partition visible on both sides:
`finTranscriptReading` (3) + `finLiveAssembleFacts` (5) = `finOutcomeStaging` (8).

### The assembly

```go
// finLiveAssembleStaging fills all eight finOutcomeStaging fields — three from the
// run's own transcript, five from the rig — and returns finOutcomeStagingGate's
// decision AS RETURNED. It is not pure: it inherits *testing.T, a filesystem read
// and a poll-to-deadline from finTranscriptFill.
func finLiveAssembleStaging(t *testing.T, workdir, sessionID string,
	facts finLiveAssembleFacts, toolUseTimeout, resultTimeout time.Duration) finOutcomeResult
```

Body contract — four statements, no branches:

1. `t.Helper()`.
2. `r := finTranscriptFill(t, workdir, sessionID, facts.StagedCommand, toolUseTimeout, resultTimeout)`
   — `facts.StagedCommand` is the *only* source of the staged string, closing the drift hazard.
3. Build `finOutcomeStaging` as **one keyed composite literal naming all eight fields**, each
   right-hand side being `r.X` or `facts.X` and nothing else.
4. `return finOutcomeStagingGate(<that literal>)` — the result is returned as returned.

**Three rules on step 3, each with a named failure it prevents:**

- **All eight keys present.** This is AC1's "no field is left at its zero value by accident" made
  auditable: a missing key is a field silently at its zero, and four of the eight zero into failure
  arms while `PinWantCount: 0` zeroes into the guard.
- **No literal on any right-hand side.** `PinMatchCount: 1` (the fixture's poison) and
  `PinWantCount: finLivePinWantRows` (the driver's job, not the assembly's) are both forbidden by this
  one rule rather than by two prohibitions a reader has to remember. It is also what forbids
  `BashIssued: true`.
- **Name-for-name.** `PinMatchCount: facts.PinMatchCount`, never `facts.PinWantCount`.

**Returns `finOutcomeResult` only.** Not the eight-field record alongside it. `finOutcomeResult` is the
one publishable type in this family; handing a caller the record too would hand it a value carrying
two captured strings for no stated need, and would undo the asymmetry `finOutcomeStaging`'s own doc
establishes ("INPUT ONLY — NEVER PUBLISHED"). If #1340 needs more, that is #1340's argument to make.

**Neither `finLivePinReduce` nor `finLivePinWantRows` appears in the assembly's body.** Both are the
driver's to call (#1340). They may appear in this file's *test*, as row values — the rule is scoped by
layer, not by file. `finTranscriptStagedCaller` appears nowhere in this file at all.

### Data flow

```
run's JSONL ──► finTranscriptFill ──► finTranscriptReading (3 fields) ──┐
                       ▲                                                 ├──► finOutcomeStaging (8)
                       │ staged                                          │            │
              finLiveAssembleFacts (5 fields) ───────────────────────────┘            ▼
                       │  (StagedCommand is ONE value with TWO consumers)   finOutcomeStagingGate
                                                                                      │
                                                                          finOutcomeResult (returned
                                                                             unchanged, not re-derived)
```

---

## Testing strategy

One test, `TestFinLiveAssembleStagingForwardsTheCounts`, over one transcript and four rows.

### The transcript — one, built in the parent

`WithWorktree(t)` and `writeFixtureLines` are called **once, in the parent test**, not per subtest. All
four rows read the same correctly-staged transcript; the counts are the only thing that varies, and
one shared transcript makes that literal rather than asserted. Two lines, from the shipped builders,
in this order:

- `finTranscriptAssistantLine(t, finTranscriptBashBlock(finTranscriptStagedID, staged, false))`
- `finTranscriptResultLine(t, finTranscriptStagedID, "bg_staged", "5000")`

where `staged := finLiveStageCommand(finLiveStageFixtureFIFOPath)` — the blocker's real staged literal,
**not** `finOutcomeHoldCommand` (a gate fixture whose `sh -c … ; exit 0` carries a `;` into a string the
model is asked to reproduce byte-for-byte against a system prompt forbidding chaining).

The same `staged` local feeds both the transcript block and every row's `facts.StagedCommand`, so a
mismatch is impossible by construction rather than by care.

Both waiter deadlines are `finTranscriptTestDeadline` (shipped, 10 ms). Declare no new deadline.

Building one staged transcript is **not** what "do not duplicate `TestFinTranscriptFill`" forbids. What
must not be reproduced is that test's *assertions*: its three-field reading mapping, its decoy row and
its caller-side-outcomes-absent sweep. This test asserts gate values only.

### The four rows

Each row supplies `(PinMatchCount, PinWantCount)`; every other field is held at its staged value
(`StagedCommand: staged`, `RendezvousDone: true`, `PinScanErrored: false`). Writing `w` for
`finLivePinWantRows`:

| # | match | want | expected | The row's job |
|---|---|---|---|---|
| 1 | `w` | `w` | `finOutcomeReadyToClassify` | The rig-realistic input — the only row a live driver actually produces. Catches an assembly that drops the counts. |
| 2 | `w-1` | `w` | `finOutcomePinCountUnexpected` | `w-1` is `1` today — the exact value `finTranscriptStagedCaller` hardcodes — so this is the most direct kill for an assembly that inherited the fixture. Stays a disagreeing pair under any change to `w`. |
| 3 | `0` | `0` | `finOutcomePinCountUnexpected` | Pins the gate's `\|\| PinWantCount < 1` guard **surviving the composition**. It is **not** the row that catches a forgotten fill — do not write that in a comment; see the matrix. |
| 4 | `c` | `c` | `finOutcomeReadyToClassify` | The only row that catches an assembly forwarding the match count while fixing the want internally. A deliberate contract row over a want no live driver emits. |

**Row 4's constant is derived, not written:**

```go
// finLiveAssembleContractWant is a want NO LIVE DRIVER EMITS — the driver always
// passes finLivePinWantRows. Derived as +1 rather than written as a literal so it
// can never coincide with the driver's want: a later edit that "fixed" this row by
// pinning it to finLivePinWantRows would delete the only check that the want
// travels at all. Non-producible-by-design, stating its reason at itself, following
// finding_staging_gate_test.go:433-436.
const finLiveAssembleContractWant = finLivePinWantRows + 1
```

The derivation **is** the guard — it makes `!= finLivePinWantRows` structural and `>= 1` free. Do not
also add a runtime precondition asserting the same two facts; that would be duplicate machinery over a
tautology.

### The matrix this table is answerable to

Derived independently against the gate's real arms and confirmed against the ticket's:

| mis-assembly | row 1 `(w,w)` | row 2 `(w-1,w)` | row 3 `(0,0)` | row 4 `(c,c)` |
|---|---|---|---|---|
| inherits `finTranscriptStagedCaller`'s `1, 1`; ignores its inputs | green | **RED** | **RED** | green |
| ignores its inputs; both counts left at zero | **RED** | green | green | **RED** |
| copies the want into the match count | green | **RED** | green | green |
| forwards the match, fixes the want internally | green | green | green | **RED** |
| substitutes its own `match == want` test for the gate | green | green | **RED** | green |

Row 3 is the sole RED for the last mutant and row 4 for the fourth — neither is decorative. Row 1's
REDs coincide with row 4's against these five; its distinct job is that it is **the input the live
driver produces**, so it is the regression guard on the real path and the only row proving the
eight-field composition reaches the pass-through at all. Keep all four.

### Assertions, and what is deliberately absent

- Every row's expectation is read from `finLiveAssembleStaging`'s return value. **No assertion in this
  file goes to `finOutcomeStagingGate` directly** — a row re-asserting a (counts → outcome) pair
  against the gate is a second copy of `finOutcomeGateCases`, which already ships `2/2 → ready`,
  `1/2 → unexpected` and `0/0 → unexpected`.
- Assert `got.Value` only. **No `Detail` assertion.** Read `TestFinOutcomeResultCarriesNoCapturedBytes`
  (`:705-790`) before deciding otherwise — it is stronger than its name suggests, and it is what
  discharges this file's obligation. It sweeps **every** row of `finOutcomeGateCases()` (`:721`),
  plants a needle into **both** command operands (`:729-733`), asserts the planted row still reaches
  its original arm so the sweep cannot decay onto one arm (`:738`), pins the per-row Detail headroom so
  a leak cannot be truncated away into a false green (`:752`), scans the marshalled result for the
  needle (`:765`), **and** rejects any `command`/`args`/`comm`/`argv`-shaped key a future field might
  add (`:778`). Because the assembly returns the gate's result unchanged (AC1), every (record → result)
  pair this file can produce is one that sweep already covers. A second needle sweep here would prove
  nothing new.
- No `finOutcomeIsValue` membership assertion — `TestFinOutcomeValuesAgreeWithThePredicate` owns it.
- **Failure messages: two sinks, two rules — do not conflate them.**
  - A published `Detail` may never carry either command **in any form, including a length or a
    prefix** (`finOutcomeResult`'s doc, `:192-197`). This file formats no `Detail` at all, so the rule
    is satisfied structurally.
  - A *test* `t.Errorf` is not a published record. Its house form is **lengths only**
    (`finding_staging_fill_test.go:556-562`, followed throughout #1342). So: name the two counts and
    the two outcome values freely — the shipped rule permits both even in a Detail — and if a message
    must refer to the staged command, report `len(staged)`.
  - If you are ever unsure which sink you are in, print neither the command nor its length. The one
    resolution that is always wrong is printing the command itself.

### The one gap no value-only test can close

Stated rather than papered over. Two mis-assemblies survive all four rows:

1. **A swap of the two counts inside the literal.** The gate's decision is
   `(m != w || w < 1) ? unexpected : ready`. When `m == w` a swap is the identity; when `m != w` both
   orders take the same arm. **No input distinguishes a swap by outcome value** — only the Detail's two
   interpolated numbers differ, and asserting the gate's prose here would be asserting another file's
   Detail. Held by the name-for-name literal rule and by review.
2. **Hardcoding the three transcript fields at their staged values** (`BashIssued: true`, etc.). All
   four rows use a correctly-staged transcript, so a hardcode is green on all of them. Catching it
   needs a no-Bash-call or wrong-command row, which is exactly `TestFinTranscriptFill`'s rows 2 and 3
   — the assertions AC2 forbids reproducing. Held by the no-literal-on-any-RHS rule and by review; the
   fill's own route to those outcomes is proven at `finding_staging_fill_test.go:577-587`.

Both are review-discharged obligations, not oversights. **Say so in the file's doc header**, in these
terms, so a later reader does not "discover" the gap and add the duplicate rows.

---

## Concurrency model

None. No goroutine, no channel, no `errgroup`, no shutdown sequence — the assembly is a straight-line
composition and the test is sequential.

Two constraints follow from `WithWorktree` calling `t.Setenv("HOME", …)`:

- **No subtest may call `t.Parallel()`.** Go's runtime refuses `t.Setenv` + `t.Parallel()` in the same
  test tree, so this is enforced deterministically rather than by convention — the note exists so
  nobody adds the call and then removes `WithWorktree` to make it compile.
- `WithWorktree` is called in the parent, before any `t.Run`. Subtests inherit the process-global
  `HOME`, and the parent's `t.Setenv` is restored only after every subtest has finished.

`finTranscriptFill` polls to a deadline (`probePollInterval` = 200 ms). Offline the file is complete
before the call, so both waiters return on their first pass and no row sleeps.

---

## Error handling

The assembly has **no error return and no failure arm of its own**, matching every gate and reduction
in this family: an instrument reading is a datum, not a reason to abort a turn.

- Every wrong or missing reading has a named home among the gate's seven outcomes. Adding a second
  error channel would give one of those outcomes two producers.
- **The assembly adds no `t.Fatal` / `t.Error`.** It inherits exactly one abort path: `ReadJSONL`
  `t.Fatalf`s on a transcript it cannot open or parse (`fixtures.go:150-164`), reached through
  `finTranscriptScanBash`. That is #1304's shipped behaviour, out of scope to change here, and it is
  named so a live caller knows the assembly can abort a turn on an unreadable transcript. Note
  `probeWaitForBashToolUse` guards with `os.Stat` first (`:767`), so a *missing* file is not fatal —
  it times out to "no Bash call issued", which is the safe direction.
- Zero-value direction: every one of the eight fields zeroes into a failure arm or into the
  `PinWantCount < 1` guard. There is no zero-valued eight-field record that reaches the pass-through.

---

## Prohibitions (AC3)

### The no-exec rule is a symbol list, not a grep

An `exec.` grep reads clean here by construction and therefore proves nothing. **Forbidden in this
file, each because it execs, spawns, blocks or skips *inside a helper* no grep of this file would see:**

| Symbol | Why |
|---|---|
| `probeProcessSnapshot`, `pinScanArgv`, `tdnScan` | Each execs `ps` internally. There is no `ps` flag to get wrong here because there is no `ps`: no `-E`, no `-Eww`, no BSD `eww`. Those flags dump `CLAUDE_CODE_OAUTH_TOKEN` and `ANTHROPIC_API_KEY`. |
| `spawnProbePyry`, `holdProbeFIFO` | Spawn `pyry` / create a real FIFO. #1340's job, explicitly out of scope. |
| `WithWorktreeAuthenticated` | It `t.Skipf`s when neither `ANTHROPIC_API_KEY` nor `CLAUDE_CODE_OAUTH_TOKEN` is set (`fixtures.go:100-107`), **and a skip exits 0** — reaching for it would silently convert an offline test into one that never runs on a credential-free machine and still reports green. |
| `os.Getenv`, `os.Environ`, `os.Setenv` | This file makes **no direct** environment call. It is **not** true that no environment is read — see § `WithWorktree` is the containment boundary. |
| `finTranscriptStagedCaller` | Its `1, 1` must not reach the assembly. Contrast target, never a call. |
| `finOutcomeStagedBase`, `finOutcomeGateCases` | Gate-side fixtures; calling either makes this a copy of the gate test. |
| `finLivePinReduce`, `finLivePinWantRows` **in the assembly's body** | The driver's to call. `finLivePinWantRows` is permitted in the *test* as a row value — scoped by layer, not by file. |

### `WithWorktree` is the containment boundary — REQUIRED here, not forbidden

The blocker's header forbids `WithWorktree` and `t.TempDir()` because it declares constants and touches
no filesystem. **This file must write a transcript and read it back through the fill.** Do not inherit
that half of its list — it is the single most likely copy-paste error when writing this file's header
from the blocker's.

And `WithWorktree` is not a convenience here. **Both** ends of this file's I/O resolve `HOME`:

- write: `writeFixtureLines` → `os.UserHomeDir()` → `tuidriver.SessionJSONLPath(home, workdir, sessionID)`
- read: `finTranscriptFill` → `ReadJSONL` → `resolveAndOpenJSONL` → `os.UserHomeDir()` (`fixtures.go:397`)

`WithWorktree`'s `t.Setenv("HOME", t.TempDir())` is what makes both resolve **inside this test's own
temp dir**. Omit it and `writeFixtureLines` writes a synthetic transcript into the operator's *real*
`~/.claude/projects/…` tree at `testSessionID` — a write outside the sandbox, into live session
storage, from a test that would still report green. Call `WithWorktree` **before** the first
`writeFixtureLines`, in the parent, and never construct the transcript path by hand.

### Captured bytes

- The staged and issued commands cross as captured strings because the shipped record builder needs
  them as inputs and reduces them itself. This file **writes none of them to an artifact, logs none of
  them, and interpolates none of them into any `Detail`** — it formats no `Detail` at all, so
  `trailDetail`'s `reachMaxCommandBytes` cap never applies here.
- No matched-row (`reachProc.Command`) values cross this file at all: the pin reduction is #1338's and
  is not called here. Should a later edit bring one in, it crosses as the **capped** `reachProc.Command`
  the shipped matcher already produces (`reachCapCommand`, `background_reach_probe_test.go:945`). Do not
  re-read an uncapped argv to "repair" a truncation — the cap is the discipline, not a defect.
- **No JSON tags on `finLiveAssembleFacts`**, and none on anything else this ticket adds that carries
  either command — the rule `finTranscriptReading` (`:92-93`) and `finOutcomeStaging` (`:141-157`) both
  state at themselves. `IssuedCommand` is verbatim model output; a tag is the first step toward
  publishing it into a public issue.

### Build tag and gating

`//go:build e2e_realclaude`. Runs for real under `make e2e-realclaude`
(`$(GO) test -tags e2e_realclaude ./internal/e2e/realclaude/...`). **Not env-gated, and must not become
so:** `TestMain` (`fixtures_test.go:348-354`) branches only on `GO_TEST_HELPER_PROCESS` and otherwise
runs `m.Run()`, so a regression here is red on a credential-free machine. No `t.Skip` of any kind.
Nothing here needs a Claude login, which is why this ticket carries no `needs-real-claude` label.

`go vet` and `staticcheck` in `make check` run **without** `-tags e2e_realclaude`, so neither analyses
this file. `make e2e-realclaude` is the gate — run it.

---

## Verification

```bash
go test -race -tags e2e_realclaude -run '^TestFinLiveAssemble' -v ./internal/e2e/realclaude/
make e2e-realclaude
```

Mutation-check the four rows before calling it done. Use the shipped overlay recipe — mutate a copy in
the scratchpad and run `go test -overlay=<abs-path>.json`, so no mutation is ever written into the
worktree. Each of the five mis-assemblies in the matrix must produce exactly the REDs its row predicts;
a mutant that goes green everywhere means a row is wrong, not that the mutant is benign.

---

## Open questions

1. **Does #1340 need the `finOutcomeStaging` record as well as the decision?** This spec returns
   `finOutcomeResult` only, on the stated grounds. If #1340 finds it needs the record, that is a
   signature change argued in #1340's own spec — not a speculative second return value here.
2. **Row 2's `w-1`.** Today `w = 2`, so `w-1 = 1`, exactly `finTranscriptStagedCaller`'s hardcode. If
   `finLivePinWantRows` ever becomes `1`, row 2 becomes `(0, 1)` — still a disagreeing pair reaching
   the same arm, so the row stays correct, but it stops being the pointed contrast with the fixture.
   Not worth machinery today; noted so a future editor knows the coincidence was deliberate.

---

## Scope check

| Red line | Limit | This ticket |
|---|---|---|
| New files | ≤ 3 | **1** (one `_test.go`) |
| Total written LOC (prod + tests + helpers + doc) | ≤ ~600 | **~300–420** — anchored on the two same-shape analogues, both a single new file in this family under this build tag: #1342 = 489, #1338 = 514. This ships fewer symbols than either (1 type, 1 assembly, 1 case type, 1 test, 1 const vs. #1342's 9). |
| New exported types/interfaces | ≤ 5 | **0** (all file-local, lowercase, build-tagged) |
| Consumer call sites needing simultaneous update | ≤ 10 | **0** — no existing file is edited; no shipped signature changes. `codegraph_impact` fan-out is empty by construction. |
| Acceptance criteria | ≤ 5 | **3** |
| Distinct reject branches in a state machine | ≤ 10 | **0** — the assembly has no branches; the gate owns all seven arms and is shipped. |
| Production source files (non-test, non-md) | < 5 | **0** |

**Size: S.** Not overridden to XS: this family's doc density puts a bottom-up line count well under
the real figure, and 300+ lines with a full doc header is genuinely S-shaped (cf. #1313 = 211,
#1316 = 250, both S).

**File-overlap check** (branch-based, `git fetch origin --prune` first, 44 remote `feature/*` branches
scanned at `fde02ff`): no in-flight branch touches
`internal/e2e/realclaude/finding_live_assembly_test.go`. `origin/feature/363` touches
`internal/e2e/realclaude/fixtures.go`, which this design **reads** (`WithWorktree`, `ReadJSONL`) and
does not modify — no conflict region. `origin/feature/1260` touches an unrelated `realclaude` file. No
`blockedBy` set.

**Identifier prefix** re-verified at `fde02ff` on 2026-08-05: `finLiveAssemble` = **0** occurrences
across `*.go`, against a live control (`trail[A-Z]` = 1424 matching lines). The broader `finLive[A-Z]`
check earlier tickets carry is **96** and would falsely report this prefix taken — do not use it.

---

## Security review

Run because the ticket carries `security-sensitive`. Two MUST FIX findings were raised against the
first draft, the spec was revised inline, and the checklist re-walked from the top. Both fixes are
folded into the sections above; they are recorded here rather than silently absorbed.

**Verdict:** PASS

**Findings:**

- **[Trust boundaries] No findings — and the design narrows the boundary.** `IssuedCommand` is verbatim
  model output, the most untrusted string in the design; it crosses file → memory through shipped code
  (`ReadJSONL` → `parseContentBlocks` → `probeToolUseInput` → `reachToolUseCommand`) and is fenced by
  two named types rather than scattered: `finTranscriptReading` on the transcript side (3 fields) and
  the new `finLiveAssembleFacts` on the caller side (5 fields). The assembly is the only place they
  meet. Untrusted bytes go **in**; only `finOutcomeResult` — a closed-set value plus a capped, command-free
  Detail — comes **out**, because the assembly returns the gate's result and nothing else. The
  type-system signal for downstream callers is the no-json-tags asymmetry stated at
  `finOutcomeStaging` (`:141-157`, "INPUT ONLY — NEVER PUBLISHED") against `finOutcomeResult`'s tags;
  this spec extends it to `finLiveAssembleFacts`.

- **[Tokens, secrets, credentials] No findings; two traps named as prohibitions.** No token is
  generated, stored, compared or logged. (1) `probeProcessSnapshot` / `pinScanArgv` / `tdnScan` each
  exec `ps` inside a helper no grep of this file would see; `-E` / `-Eww` / BSD `eww` dump
  `CLAUDE_CODE_OAUTH_TOKEN` and `ANTHROPIC_API_KEY` into the read. All three symbols are forbidden by
  name, and there is no `ps` flag to get wrong because there is no `ps`. (2) `WithWorktreeAuthenticated`
  `t.Skipf`s when neither credential is set (`fixtures.go:100-107`) **and a skip exits 0** — a
  silent-green hazard rather than a leak, forbidden by name with that reason attached.

- **[File operations] MUST FIX — fixed.** The first draft asserted "no environment is read or written
  at all" and justified `WithWorktree` as needed because the test "must write a transcript". Both ends
  of this file's I/O in fact resolve `HOME`: `writeFixtureLines` → `os.UserHomeDir()`, and
  `ReadJSONL` → `resolveAndOpenJSONL` → `os.UserHomeDir()` (`fixtures.go:397`). A developer who
  believed the spec's claim and dropped `WithWorktree` would have `writeFixtureLines` write a synthetic
  transcript into the operator's **real** `~/.claude/projects/…` session storage at `testSessionID`,
  from a test still reporting green. Fixed: `WithWorktree` is now specified as the *containment
  boundary* on both paths, with the bad outcome named, and the prohibition-table row no longer makes
  the false claim. Otherwise: path traversal is structurally inapplicable —
  `finLiveStageFixtureFIFOPath` is synthetic and is concatenated into a *command string* that is never
  opened, stat'd, joined or executed, and the only real path is built by the shipped
  `tuidriver.SessionJSONLPath` from a const `sessionID` and a `t.TempDir()` workdir, with no
  caller-controlled component. Permissions are the shipped fixture's (`0o700` dir, `0o600` file) and
  this ticket does not restate or relax them. Atomic writes are not needed: the transcript is written
  once, in the parent, **before** any subtest reads it, so there is no write-during-read — one more
  reason the parent-level write beats a per-subtest one.

- **[Subprocess / external command execution] No findings — nothing execs, and the check is the symbol
  list, not a grep.** An `exec.` grep reads clean here by construction and therefore proves nothing;
  the spec ships a per-symbol forbidden list with a reason each. `spawnProbePyry` and `holdProbeFIFO`
  (spawn `pyry`, create a real FIFO) are forbidden as #1340's job. No `sh -c`: the design deliberately
  uses `finLiveStageCommand`'s bare form over `finOutcomeHoldCommand`'s `sh -c … ; exit 0`. The string
  `cat /tmp/…` exists here purely as **data** — written into a JSONL and compared as opaque bytes —
  and is never handed to `exec.Command` nor shell-interpreted. No child process, so no environment
  inheritance, no signal handling and no double-fork escape to reason about; `finLiveStageEnvDelta()`
  is #1342's and is deliberately **not** consumed here.

- **[Cryptographic primitives] Not applicable, with reason.** No RNG, hashing, key derivation, key
  material or nonce anywhere in the design. The one comparison in the data flow is byte equality of two
  commands, it lives inside the shipped gate rather than here, and neither operand is a secret —
  `crypto/subtle.ConstantTimeCompare` would be the *wrong* primitive, not a missing one.

- **[Network & I/O] No findings.** No socket, listener, HTTP server, TLS config or remote peer; the
  timeout/slow-loris/TLS-version items have no surface here. The one unbounded-input question — the
  transcript read — has an explicit shipped cap: `maxLineBytes = 16 << 20`
  (`internal/agentrun/jsonl/reader.go:30`), enforced at `:247`. The two poll deadlines are the shipped
  `finTranscriptTestDeadline` (10 ms), so no row can hang.

- **[Error messages, logs, telemetry] SHOULD FIX — fixed.** The draft told the developer to "report
  lengths" while the quoted `finOutcomeResult` rule forbids a command's length *in a Detail*. Two sinks,
  two rules, and a developer resolving the apparent contradiction the wrong way would print the command
  itself. Fixed: the two sinks are now separated explicitly, with "print neither" as the tie-breaker.
  This file writes no artifact, logs nothing and formats no `Detail`, so `trailDetail`'s
  `reachMaxCommandBytes` cap never applies. The published-Detail obligation is discharged by
  `TestFinOutcomeResultCarriesNoCapturedBytes` — **read and verified for this review, not cited by
  name**: it sweeps every `finOutcomeGateCases()` row, plants into *both* command operands, asserts the
  planted row still reaches its original arm, pins per-row headroom so a leak cannot be truncated into
  a false green, scans the marshalled result for the needle, and rejects `command`/`args`/`comm`/`argv`-
  shaped keys. Since the assembly returns the gate's result unchanged, that sweep covers this file's
  output.

- **[Concurrency] No findings.** No goroutine, channel, lock or shared mutable state, so lock ordering,
  leakage and shutdown-mid-Send do not arise. The one process-global is `WithWorktree`'s
  `t.Setenv("HOME", …)`; Go's runtime refuses `t.Setenv` in a test tree that has called `t.Parallel()`,
  which makes the no-parallel constraint deterministic rather than advisory, and top-level parallel
  tests never overlap a sequential one. A write interrupted mid-`os.WriteFile` leaves a truncated
  transcript, on which `ReadJSONL` `t.Fatalf`s — loud, and the safe direction.

- **[Threat model alignment] No applicable document; the family's own threats are addressed.**
  `docs/threat-model.md` does not exist in this repo (checked at `fde02ff`), and
  `docs/protocol-mobile.md` § Security model is relay-scoped — this is an offline, build-tagged test
  assembly with no network surface, so no relay threat applies. The four threats this file family
  states at itself are each addressed above: captured bytes reaching a published record (trust
  boundaries + errors/logs), `ps` flags dumping credential env vars (tokens), a credential-gated skip
  reporting green-but-unrun (tokens), and operator filesystem paths landing in a test file (file
  operations — the synthetic `finLiveStageFixtureFIFOPath` is shipped for exactly this).

**Residual, accepted and stated in the spec rather than hidden:** two mis-assemblies survive all four
rows — a swap of the two counts inside the literal (provably undetectable by outcome value: the gate's
decision is symmetric in `m`/`w` whenever `m == w`, and takes the same arm whenever `m != w`), and
hardcoding the three transcript fields at their staged values. Both are held by the composite-literal
rules (all eight keys, no literal on any RHS, name-for-name) and by review. Neither is a security
finding — both fail *toward* a wrong verdict on a probe, never toward publishing captured bytes.

**Reviewer:** architect (self-review per `architect/security-review.md`)
**Date:** 2026-08-05
