# #1338 — Reduce a during-turn pin scan to the FIFO-needle row count, pgid set, matched rows and claude argv

One new file: `internal/e2e/realclaude/finding_live_pin_test.go`, `//go:build e2e_realclaude`.
One pure function, one named constant, one reading type, one synthetic-bytes fixture, one offline trap.
**No live scan, no `ps` exec, no `pyry` spawn, no `*testing.T` in the reduction, no existing file edited.**

---

## Files to read first

Generated from `codegraph_context` over the ticket title + AC paraphrase, then pruned to what the design
actually turns on. This is the turn-1 data load — read these before writing a line.

| Path | What to extract |
|---|---|
| `internal/e2e/realclaude/process_pin_liveness_test.go:120-176` | `pinScan`'s four fields; **`pinPartition:167` assigns `MatchCount = len(out.Matches)`** — the count-from-`len` idiom this spec's `RowCount` copies so a count never has two producers; `pinMatchArgvExcluding:173` signature. |
| `internal/e2e/realclaude/process_pin_liveness_test.go:137-148` | The prose *"reachMatchArgvRows is this package's one full-argv matcher and #1235 must not grow a second"*. Growing a bespoke FIFO matcher here would falsify shipped prose by this ticket's own hand. |
| `internal/e2e/realclaude/background_reach_probe_test.go:884-929` | `reachMatchArgvRows` — matches on the **uncapped** `command` (`:913`), stores `reachCapCommand(command)` (`:924`), records the hit list in `Needles` (`:925`). The whole of AC4's truncation trap rests on this asymmetry. |
| `internal/e2e/realclaude/background_reach_probe_test.go:931-962` | `reachCommandColumn` (the cap applies to the command column, i.e. **after** the three integer columns), `reachCapCommand`, `reachMatchedNeedle` — the membership test AC1 mandates. |
| `internal/e2e/realclaude/background_reach_probe_test.go:111-125` | `reachMaxCommandBytes = 512`, `reachTruncationMarker`, and the cap's stated reason. The fixture derives its padding **from this constant**, never from a literal 512. |
| `internal/e2e/realclaude/background_reach_probe_test.go:159-169` | `reachProc`'s field set (`PID/PPID/PGID/Command/Needles`) — `Command` and `Needles` are populated for content-matched rows only. |
| `internal/e2e/realclaude/background_reach_probe_test.go:1159-1218` | `reachArgvFixture` + `TestReachMatchArgvRows` — the four-column table shape and the well-formed-row accounting this trap copies. |
| `internal/e2e/realclaude/teardown_liveness_probe_test.go:507-542` | `tdnPinHeld` — the shipped FIFO filter, **and the shape not to copy**: it dedupes pids and collapses to a single pgid, returning 0 on a non-singleton group set (`:538-541`). `:511-514` is #1230's live measurement (the constant's whole basis); `:516-520` is the group count of 1 (wrong fill #2). |
| `internal/e2e/realclaude/teardown_liveness_probe_test.go:557-575` | `tdnClaudeCommand` — reads `reachMatchedNeedle(m, tdnClaudeNeedle)` over `scan.Matches`, returns `""` unless exactly one row hits. |
| `internal/e2e/realclaude/teardown_liveness_probe_test.go:150-160` | `tdnClaudeNeedle = "--append-system-prompt-file"` and the argued disjointness from the FIFO needle. |
| `internal/e2e/realclaude/finding_staging_gate_test.go:158-186` | `finOutcomeStaging`'s eight fields, **no json tags and why** — the content rule this spec's reading type mirrors. `PinMatchCount`/`PinWantCount` at `:181-185`. |
| `internal/e2e/realclaude/finding_staging_gate_test.go:343-357` | The count arm. A mis-fill fires `stage-pin-count-unexpected` on a correctly staged run, with no other symptom. |
| `internal/e2e/realclaude/finding_staging_gate_test.go:389-409` | `finOutcomeStagedBase` at `PinMatchCount/PinWantCount: 2` — a gate **input**, not a reading. Read `:395-397` to see the citation this ticket must not repeat. |
| `internal/e2e/realclaude/finding_attribution_fanout_test.go:190-229` | Why `finAttributeFanOut` takes `[]int` and not `[]reachProc` (the credential channel), and that it dedupes + `sort.Ints` **internally** (`:220-229`). This is why the projection handed on is raw. |
| `internal/e2e/realclaude/trail_run_rig_test.go:506-568` | `TestTrailRigCarriesMoreThanOneMatchedRow`. Read `:563` and confirm for yourself: it fails on `MatchCount <= 1`, i.e. asserts **`> 1`**, never `== 2`. Do not cite it as corroboration of 2. |
| `internal/e2e/realclaude/finding_staging_fill_test.go:1-60` | The file-header idiom for this family: scope, the offline inventory, the `go test` run line, then the named sections. |
| `docs/knowledge/codebase/1268.md:145-157` | #1268's mutation-tested corroboration — dropping the wrapper cut the count 2→1 on a rig-staged `sh -c`. Corroboration only; the primary basis is #1230's live run. |
| `Makefile:55-56` | `e2e-realclaude` runs `go test -tags e2e_realclaude ./internal/e2e/realclaude/...` — no env gate, no credentials. This file's tests run there for real. |

---

## Context

A later ticket stages one live `pyry agent-run` turn: a `cat` on a FIFO held un-finishable, the
auto-background trigger fired, the process table pinned **during** the turn. That run has exactly one
turn to spend. Everything it can be made to get wrong offline, it should be made to get wrong offline.

`pinScan` is already a value — `pinMatchArgvExcluding(table []byte, needles []string, exclude map[int]string) pinScan`
(`process_pin_liveness_test.go:173`) turns a `ps` table's **bytes** into one, and the live wrapper
`pinScanArgv` (`:191`) does the same over a real exec. So the reduction from that scan to the four
things the staging record consumes is separable from the exec, and a synthetic table drives exactly
the matching the live path uses.

### The count is the whole ticket

`finOutcomeStagingGate`'s count arm (`finding_staging_gate_test.go:347`) returns
`finOutcomePinCountUnexpected` when `PinMatchCount != PinWantCount`. A mis-fill does not fail loudly in
development. It fires on every **correctly staged** run, reporting a staging failure while the rig
looks correct, and burns a live turn per attempt.

One scan carries two needles — the run's FIFO path and `tdnClaudeNeedle` — because
`reachMatchArgvRows` matches a row on **any** needle (`background_reach_probe_test.go:911-916`). That
is the precedent at `teardown_liveness_probe_test.go:377` and it is what makes both wrong fills
available and plausible:

| candidate | value on a healthy run | source |
|---|---|---|
| `scan.MatchCount` | **3** — wrapper + `cat` + claude | both needles in one scan |
| distinct pgids of the FIFO rows | **1** | `teardown_liveness_probe_test.go:516-520` |
| **rows carrying the FIFO needle** | **2** ✅ | `teardown_liveness_probe_test.go:511-514` |

Both wrong candidates are integers of the right type in the right frame. Neither is caught by anything
shipped. This spec's answer is not a comment: all three values are **asserted distinct** in the trap
(§ Testing strategy, T2), so a regression to either wrong fill is red.

### Where the 2 comes from

#1230's live run of this exact shape matched **two rows** on the FIFO needle — the `zsh -c` wrapper
claude runs Bash through, whose argv carries the whole command string, and the `cat` itself. Recorded
verbatim in shipped code at `teardown_liveness_probe_test.go:511-514`. #1268 corroborates it against a
rig-staged `sh -c` and mutation-tested it (`docs/knowledge/codebase/1268.md:151-157`): dropping the
wrapper cut the count 2→1.

**Do not re-derive it, and do not cite `trail_run_rig_test.go:563` for it.** That assertion is
`MatchCount <= 1` → fail, i.e. *more than one*. `finding_staging_gate_test.go:395-397` already cites it
as the basis for its own `PinMatchCount: 2`, and that citation is weaker than the number it justifies —
verified against the source at `8970095`. This ticket's constant is therefore **strictly stronger than
any shipped assertion**, and rests on #1230's live measurement alone. A live run reporting a different
count is a non-verdict outcome the gate already returns; it is never a silent first-match, and never a
reason to loosen the constant.

---

## Design

### The reading type

```go
type finLivePinReading struct {
    Rows          []reachProc
    RowCount      int
    PGIDs         []int
    ClaudeCommand string
}
```

- **No json tags, deliberately.** `Rows[i].Command` and `ClaudeCommand` are verbatim argv read off the
  ambient process table. This type mirrors `finOutcomeStaging`'s stated rule
  (`finding_staging_gate_test.go:142-150`): an intermediate that must not be embedded in, marshalled
  into, or quoted by any published record. Adding tags "for symmetry" is the first step toward
  publishing captured bytes into a public issue. State that at the type.
- **`Rows` and `RowCount` together mirror `pinScan.Matches`/`MatchCount`** — the same pairing, and
  `RowCount` is assigned exactly once, as `len(Rows)`, in the same place `pinPartition:167` assigns its
  own. One producer for the number; `RowCount` is never computed a second way.
- **`PGIDs` is a `[]int` and never `[]reachProc`.** Its consumer `finAttributeFanOut` takes `[]int`
  precisely because that signature closes the credential channel structurally
  (`finding_attribution_fanout_test.go:195-202`). The conversion happens here, in the reduction, so the
  record ticket downstream has an `[]int` to hand and never reaches into `Rows` for it.

### The constant

```go
const finLivePinWantRows = 2
```

Declared **up front**, with a doc comment that states, in this order:

1. **What it counts.** Rows carrying the run's FIFO needle. Not pids, not process groups.
2. **The reason.** #1230's live run matched two rows for one held command — the `zsh -c` wrapper whose
   argv carries the whole command string, and the forked `cat`
   (`teardown_liveness_probe_test.go:511-514`). #1268's mutation-tested 2→1 result
   (`docs/knowledge/codebase/1268.md:151-157`) is named as **corroboration**, with its subject
   (a rig-staged `sh -c`) stated so it is not mistaken for the primary basis.
   `trail_run_rig_test.go:563` is **not** cited — it asserts only `> 1`.
3. **Prohibition 1 — never fill from `scan.MatchCount`.** It is 3 on a healthy run: one scan carries
   both needles, so claude's own row is in it. Consequence: `finding_staging_gate_test.go:347` fires
   `stage-pin-count-unexpected` against a want of 2 on a correctly staged run, with no other symptom,
   costing a live turn.
4. **Prohibition 2 — never fill from the size of the process-group set.** It is 1 on a healthy run
   (`teardown_liveness_probe_test.go:516-520`): claude isolates the whole Bash command into one
   detached group. Same consequence, same cost.

Both prohibitions live **where the count is produced** — on this constant, not in a distant test file.

### The reduction

```go
func finLivePinReduce(scan pinScan, fifoPath string) finLivePinReading
```

Pure: no exec, no `*testing.T`, no clock, no goroutine, no I/O, no error return. One pass over
`scan.Matches`.

Behaviour, four values, one contract each:

| value | rule |
|---|---|
| `Rows` | every `m` in `scan.Matches` for which `reachMatchedNeedle(m, fifoPath)` is true, in scan order, **no dedup** |
| `RowCount` | `len(Rows)`, assigned once at the end |
| `PGIDs` | `m.PGID` for exactly the same rows, same order, **unsorted, duplicates intact** |
| `ClaudeCommand` | `tdnClaudeCommand(scan)` |

Four properties the developer must get right, each with a named failure:

- **Membership is `reachMatchedNeedle`, never a re-scan of `.Command`.** `reachMatchArgvRows` matches
  the **uncapped** line (`background_reach_probe_test.go:913`) and stores the capped one (`:924`), so a
  row whose FIFO path sits past byte 512 is genuinely matched while its retained `Command` no longer
  contains the path. `strings.Contains(m.Command, fifoPath)` looks equivalent and silently drops that
  row, yielding 1. T1's truncation pair is what makes this red rather than decorative.
- **No second full-argv matcher is grown.** This is a pure post-filter over what `reachMatchArgvRows`
  already produced — the same relationship `pinPartition` has to it, and for the same reason
  (`process_pin_liveness_test.go:137-148`). `probeHasCommand`
  (`background_trigger_probe_test.go:963`) is doubly wrong here: `probeAnnotateCommands` (`:930`)
  stores only `filepath.Base(argv[0])`, so the held command's `Command` is `cat` and it would match any
  unrelated `cat` on the machine — and it is scoped to descendants besides.
- **Nothing is deduped, anywhere.** Not `Rows`, not `PGIDs`. `finAttributeFanOut` dedupes and sorts
  internally (`finding_attribution_fanout_test.go:220-229`), and its own comment explains that the sort
  is what makes the record a pure function of the *set* rather than of `ps` output order. Pre-reducing
  here duplicates that work and destroys the raw evidence. `tdnPinHeld` (`teardown_liveness_probe_test.go:521`)
  is the shipped FIFO filter but the **wrong shape** to copy: it dedupes pids and collapses to a single
  pgid, returning 0 when the rows do not resolve to exactly one group (`:538-541`).
- **`tdnClaudeCommand` runs over `scan`, not over `Rows`.** Claude's row carries the claude needle and
  **not** the FIFO needle — that disjointness is argued at `teardown_liveness_probe_test.go:155-160`.
  So `tdnClaudeCommand(finLivePinReading.Rows-as-a-scan)` would return `""` on every healthy run: zero
  rows hit, `n != 1`, empty string. That is a silent wrong answer with a plausible-looking cause. T1's
  claude-argv assertion is what catches it.

### What this ticket does **not** ship

- No live scan, no `ps` exec, no `pyry` spawn, no FIFO, no turn, no `t.Skip`, no env gate.
- No caller. The reduction is driven end-to-end by this ticket's trap and by nothing else.
- No edit to any existing file. In particular `finOutcomeStagedBase`
  (`finding_staging_gate_test.go:406-407`) and `finTranscriptStagedCaller`
  (`finding_staging_fill_test.go:360-361`) are **not** migrated to the constant: they are gate
  *inputs* that only have to satisfy `PinMatchCount == PinWantCount >= 1`, not readings of a process
  table, and rewriting them is a fixture cascade across files this ticket otherwise never opens.
- No `docs/knowledge/codebase/1338.md`. The documentation phase writes it after the PR merges.

---

## Data flow

```
                     (later ticket)                  (this ticket)                  (later tickets)
  ps -axww bytes ──> pinScanArgv / ────> pinScan ──> finLivePinReduce ──┬─> RowCount ─────> finOutcomeStaging.PinMatchCount
                     pinMatchArgvExcluding                              │                   vs finLivePinWantRows
                       │                                                ├─> PGIDs []int ──> finAttributeFanOut(stderr, pgids, …)
                       │                                                ├─> Rows        ──> record ticket (capped Command only)
                       └── reachMatchArgvRows ── the ONE full-argv      └─> ClaudeCommand ─> provenance, never a gate
                           matcher; this file grows no second
```

`RowCount` and `PinWantCount` are separate fills at the record ticket's call site; this ticket ships
the reading and the constant, and the gate compares them.

---

## Concurrency model

**None, and that is the design.** `finLivePinReduce` spawns no goroutine, takes no context, reads no
clock and holds no lock. Its inputs are a value and a string; its output is a value. The whole file is
offline: no exec, no FIFO, no subject process, no `*testing.T`-driven waiting. The trap is a plain
`go test` run with `t.Parallel()` per `CODING-STYLE.md`.

The reduction is safe to call concurrently by construction — it mutates nothing it does not allocate.
Note it does **not** deep-copy `scan.Matches[i].Needles`; the returned `Rows` alias the scan's slices.
That is correct here (the scan is a value the caller owns and this file never mutates either side) and
should be stated in the doc comment rather than defended with a copy.

---

## Error handling

The reduction has **no error return and no failure arm**, which is a deliberate consequence of where
it sits:

- The `ps` exec's error is `pinScanArgv`'s, and it already has a named home in the record:
  `finOutcomeStaging.PinScanErrored` → `finOutcomePinScanErrored`, ranked **above** the count arm
  (`finding_staging_gate_test.go:332-341`) precisely so a count of 0 the error produced is never
  reported as a count that was measured. Adding a second error channel here would give that outcome
  two producers.
- **A zero scan reduces to a zero reading.** `finLivePinReduce(pinScan{}, path)` returns
  `Rows == nil`, `RowCount == 0`, `PGIDs == nil`, `ClaudeCommand == ""`. It does not panic and does not
  invent a value. `RowCount == 0` then fails the gate's count arm against a want of 2, which is the
  correct outcome — but only ever reached after `PinScanErrored` has had its turn.
- **An empty `ClaudeCommand` is ambiguity, not failure.** `tdnClaudeCommand` returns `""` when zero or
  several rows carry the claude needle (`teardown_liveness_probe_test.go:571-573`). The runner label is
  provenance. `ClaudeCommand` is **not** one of `finOutcomeStaging`'s eight fields and must not be
  gated on, here or downstream.
- **An empty `fifoPath` matches nothing.** `reachMatchArgvRows` never records an empty needle
  (`background_reach_probe_test.go:913` requires `needle != ""`), so no row's `Needles` can contain
  `""` and `reachMatchedNeedle(m, "")` is false for every row. A caller that forgot to pass the path
  gets `RowCount == 0` and the gate's count arm, not a match against everything. Worth one sentence in
  the doc comment; it needs no code.

---

## Testing strategy

One synthetic four-column `ps` table **built as bytes**, turned into a `pinScan` by the real matcher.
Building a `pinScan` by hand instead would skip `reachMatchArgvRows` — and the truncation property that
makes T1's membership assertion red exists only inside that function.

### The fixture

```go
const finLivePinFIFOPath = "/tmp/pyry-fin-live-pin/live-pin-hold"   // synthetic; no operator path
func finLivePinTable() []byte                                       // the four-row ps table
func finLivePinScan(t *testing.T) pinScan                           // pinMatchArgvExcluding(table, needles, nil)
```

The scan is `pinMatchArgvExcluding(finLivePinTable(), []string{finLivePinFIFOPath, tdnClaudeNeedle}, nil)`
— both needles, and an **empty exclusion set** (`nil` map; exclusions belong to the driver ticket).
That fixes the totals at `RowsScanned` 4 / `MatchCount` 3 / FIFO rows 2 / distinct groups 1.

Four rows, following `reachArgvFixture`'s shape (`background_reach_probe_test.go:1163-1173`) rather
than inventing a format — `pid ppid pgid command`, pids ascending:

| # | pid | ppid | pgid | row | what it is load-bearing for |
|---|---|---|---|---|---|
| 1 | 200 | 100 | **200** | claude, carrying `--append-system-prompt-file`, **no** FIFO path, under the cap | its group must be **distinct** from the wrapper's — sharing it would make "claude's group is absent from the projection" red against a *correct* implementation |
| 2 | 300 | 200 | **300** | the `zsh -c` wrapper, **argv longer than `reachMaxCommandBytes`**, FIFO path occurring **only past byte 512** | the truncation pair (T1) |
| 3 | 400 | 300 | **300** | the `cat`, same group as the wrapper, FIFO path within the cap | the second counted row |
| 4 | 500 | 1 | 500 | an unrelated daemon, neither needle | keeps `RowsScanned` (4) distinct from `MatchCount` (3); never enters `Matches`, constrains nothing else |

**Building row 2 — the recipe, so nobody hand-counts bytes.** The cap applies to the *command column*,
i.e. everything after the three integer columns (`reachCommandColumn:933`). Compose it as
`head + pad + tail` where `head = "/bin/zsh -c "`, `tail = " cat " + finLivePinFIFOPath`, and `pad` is a
filler token repeated until `len(head)+len(pad) >= reachMaxCommandBytes`. **Derive the repeat count
from `reachMaxCommandBytes`, never from a literal 512** — the fixture then tracks the constant if it
ever moves. The FIFO path's first occurrence is then at index ≥ 512, so `command[:512]` cannot contain
it.

The filler token must carry **neither** needle and must not introduce a **second** occurrence of the
FIFO path. `"--pad "` satisfies both. A filler carrying the claude needle would make
`tdnClaudeCommand` ambiguous (`n != 1` → `""`) and fail T1's last assertion against a *correct*
implementation; say so at the token's declaration.

### T1 — `TestFinLivePinReduce`: the four outputs

Scenarios (assertions, not code):

- **Both FIFO rows are counted.** `RowCount == finLivePinWantRows` (2), and `Rows`' pids are exactly
  `[300, 400]` in that order. This is the constant, exercised.
- **The truncation is asserted, not assumed** — the pair that makes membership red:
  - row 300 **is** in `Rows`, **and**
  - its retained `Command` does **not** contain `finLivePinFIFOPath`.

  An implementation that re-scans `.Command` counts 1 and fails the first half. Without the second
  half the property goes vacuous the day the fixture row drops under 512 bytes. Assert both, in the
  same test, with a failure message naming `reachMatchedNeedle` as the rule.
- **The projection is raw.** `PGIDs` has exactly **two** entries and they are **equal** (both 300).
  Two entries is what proves "duplicates intact" — a deduping implementation yields one.
  Do not sort-then-compare; compare positionally.
- **Claude is absent from both.** `200` appears **nowhere** in `PGIDs`, and pid `200` is not in `Rows`.
- **Claude's argv comes back.** `ClaudeCommand` equals row 1's command string exactly, and is
  non-empty. This is the assertion that catches `tdnClaudeCommand` being run over the FIFO-filtered
  rows instead of over the scan.
- **The zero scan is inert.** `finLivePinReduce(pinScan{}, finLivePinFIFOPath)` returns
  `RowCount == 0`, nil `Rows`, nil `PGIDs`, empty `ClaudeCommand`, and does not panic.

### T2 — `TestFinLivePinCountIsNeitherWrongCandidate`: the three-way distinctness pin

Over the same scan, assert all three candidate values and that they differ:

- `scan.MatchCount == 3` — the first wrong fill, pinned as a value rather than prohibited in prose.
- the **deduplicated** group-set size is `1` — the second wrong fill, likewise pinned. Compute the
  dedup locally in the test; the reduction itself must not.
- `finLivePinReduce(...).RowCount == finLivePinWantRows == 2`.
- `scan.RowsScanned == 4` — keeps the scanned total distinct from `MatchCount`.

All three candidates are then distinct **by assertion**. A regression that reintroduces either wrong
fill is red, and a later edit that collapses them — dropping the claude row would make `MatchCount` 2 —
fails this trap instead of silently disarming it. Each failure message should name which wrong fill it
is guarding and what it costs (`stage-pin-count-unexpected` on a correctly staged run, one live turn).

### Running it

```
go test -race -tags e2e_realclaude -run '^TestFinLivePin' -v ./internal/e2e/realclaude/
```

Not env-gated and must not be — nothing here needs a Claude login. These run for real under
`make e2e-realclaude` (`Makefile:55-56`), which is why the ticket carries no `needs-real-claude` label.

### Naming

`finLivePin*` throughout. Re-verified at `8970095` on 2026-08-05:
`rg 'finLive[A-Z]' --glob '*.go' | wc -l` → **0**, with the control `rg 'trail[A-Z]' --glob '*.go' | wc -l`
→ **1422** (non-zero, so the check is meaningful). Prefixes already taken by sibling instrument
tickets: `finGather*`, `finAttribute*`, `finStage*`, `finOutcome*`, `finRecord*`, `finTrailer*`,
`finWrite*`, `finTranscript*`. The two siblings that consume this reduction take `finLiveStage*` and
`finLiveRun*` — do not take those.

---

## Sizing and overlap (recorded, not deferred)

**Red lines, counted raw.** New files: 1. Production source files (`*.go` excluding `*_test.go`)
created or modified: **0**. New exported types/interfaces: **0** — everything is unexported and
file-local under the build tag. Consumer call sites needing simultaneous update: **0** — no existing
symbol changes signature and no fixture is migrated (reason stated above). Acceptance criteria: 5.
Distinct error/reject branches: **0** — the reduction has no error return and no failure arm.
Projected total written work, sized against the nearest merged analogues in this package rather than
bottom-up (doc density here is ≈ 1:1, which makes bottom-up counts run low): #1324 +480, #1313 +523,
#1326 +613, all `size:s`; `finding_staging_fill_test.go` is 602 lines, `finding_stage_held_group_test.go`
631. This deliverable is smaller than any of them — one function body of ~15 lines, one constant, one
4-field struct, one fixture builder, two tests — and lands ≈ 450–600 lines including its header and
doc comments. Under the ~600 red line. **Size confirmed `s`; not split.**

**File-overlap check** (`git fetch origin --prune` then `git diff --name-only origin/main...origin/feature/N`
across all 46 remote feature branches, run at `8970095`): no in-flight branch touches
`internal/e2e/realclaude/finding_live_pin_test.go`. The parent `origin/feature/1336` has an empty diff
against main. No `addBlockedBy` needed.

---

## Open questions

1. **The stale citation at `finding_staging_gate_test.go:395-397` is left alone, deliberately.** That
   comment justifies its own `PinMatchCount: 2` by naming `TestTrailRigCarriesMoreThanOneMatchedRow`,
   which asserts only `> 1` — verified against the source at `8970095`. Correcting it means editing a
   file this ticket otherwise never opens, and the number it defends is a gate *input* rather than a
   reading, so nothing is measured wrong today. **Flagged for PO as a follow-up ticket**, not fixed
   here. Whoever picks it up should point the comment at `teardown_liveness_probe_test.go:511-514` and,
   once this ticket lands, at `finLivePinWantRows`.
2. **Should the reduction return `Rows` at all?** AC1 mandates it and the record ticket needs it, so
   yes — but it is the one field carrying verbatim argv. If the record ticket turns out to need only
   pids and the capped commands, a later ticket may narrow the field. Not a defence to build now: no
   failure mode has been observed, and `[]reachProc` here is already the capped form.
3. **Does the live table ever put claude's row in the wrapper's process group?** #1230 measured
   count=1 for the FIFO rows' group on all three reps, and claude sitting in its own group is what
   `reachArgvFixture` encodes. If a live run ever shows otherwise, T1's "claude's group is absent from
   the projection" assertion is about the *fixture*, not about the live table, so it stays green — but
   the driver ticket's exclusion set is where that would need handling.

---

## Security review

**Verdict:** PASS

**Findings:**

- **[Trust boundaries] SHOULD FIX (documented in the design, developer must land the comment).** There
  is exactly one boundary and it is the reduction's own signature. `scan.Matches[i].Command` is
  verbatim argv read off the **ambient** process table — any local process's command line, not just
  this run's — and it crosses into `finLivePinReading.Rows` and `finLivePinReading.ClaudeCommand`. The
  boundary is explicit and single (one function, one type), not scattered. What makes it hold is that
  the type carries **no json tags**, mirroring `finOutcomeStaging`'s stated rule
  (`finding_staging_gate_test.go:142-150`): the reading cannot be marshalled into a published record by
  accident. Downstream callers know they hold untrusted bytes because the type says so at its
  declaration. The developer must write that rule at the type; a `finLivePinReading` with tags is the
  finding.
- **[Trust boundaries] No findings — the `[]int` channel is preserved, not reopened.** #1281's
  knowledge doc (`docs/knowledge/codebase/1281.md:38-45`) assigns exactly this obligation: *"a caller
  holding `pinScan.Matches` converts at its own call site, taking `.PGID` and nothing else"*, and flags
  it as a code-review item for each conversion site. This spec is such a site, and it converts here:
  `PGIDs []int` is built in the reduction, so the record ticket has an `[]int` to hand
  `finAttributeFanOut` and never needs to reach into `Rows`. A design that returned only `Rows` and
  left the `.PGID` projection to the consumer would pass every test in this file while pushing the
  channel one file downstream. Code review should check the field is `[]int` and not `[]reachProc`.
- **[Tokens, secrets, credentials] No findings — the capability is absent, not guarded.** The concrete
  threat in this family is `CLAUDE_CODE_OAUTH_TOKEN` / `ANTHROPIC_API_KEY` reaching an artifact
  destined for a public issue, and the only route is a `ps` environment column. This file **execs
  nothing at all**, so there is no flag to get wrong: no `ps -E`, no `-Eww`, no BSD `eww`, no
  `exec.Command`, no `os/exec` import. The shipped exec that will feed this reduction
  (`background_reach_probe_test.go:876`) already uses `-axww` — `-ww` widens the *command* column and
  touches no environment — and its comment at `:868-872` states the rule. Nothing in this spec adds a
  process read. **Grep is not the check here**: an `exec.` sweep reads clean on this file by
  construction, so the check is the symbol list — `pinScanArgv`, `probeProcessSnapshot`, `tdnScan`,
  `holdProbeFIFO`, `WithWorktreeAuthenticated` are all forbidden in this file because each execs or
  blocks internally; `pinMatchArgvExcluding`, `reachMatchArgvRows`, `reachMatchedNeedle`,
  `reachCapCommand` and `tdnClaudeCommand` are pure over bytes and stay allowed.
- **[Error messages, logs, telemetry] SHOULD FIX — one concrete leak shape the trap can create.**
  The reduction itself writes nothing: no artifact, no `slog` call, no `trailDetail`, no `Detail`
  interpolation. But T1's failure messages are the one place captured-shaped bytes could be printed,
  and the failing assertion for the truncation case is *"row 300's retained Command does not contain
  the FIFO path"* — the tempting message quotes the command. Here that is a **synthetic fixture
  string**, so it is harmless today; the rule to land in the file header is that failure messages name
  **pids, counts and needle names**, never a `Command` value, so the habit survives contact with a
  live caller. Code review should check the `t.Fatalf`/`t.Errorf` argument lists.
- **[File operations] No findings — no filesystem access exists.** No `os.Open`, no `os.Stat`, no
  `t.TempDir()`, no FIFO, no path is opened. `finLivePinFIFOPath` is a **synthetic constant string**
  used only as a needle for `strings.Contains` inside the shipped matcher. It is never opened,
  canonicalised, joined, or passed to anything that touches a path — so path traversal, TOCTOU,
  symlink following and file modes are all structurally inapplicable rather than merely unaddressed.
  The constant must not be built from `t.TempDir()` or `os.Getenv`: either would put an operator
  filesystem path into a test file for nothing (the precedent reason at
  `finding_staging_gate_test.go:370-375`).
- **[Subprocess / external command execution] No findings — nothing is executed.** No `exec.Command`,
  no `sh -c`, no environment inheritance, no signal handling, no fork. The fixture's row 2 *contains
  the text* `/bin/zsh -c …` — it is a byte string in a synthetic table, compared with
  `strings.Contains` and never lexed, split, path-resolved or run. `finding_staging_gate_test.go:152-157`
  states this same rule for its two command operands; the reduction inherits it: the commands are
  **opaque bytes**, and the reduction asks nothing of them at all.
- **[Cryptographic primitives] N/A by design — no randomness, no comparison against a secret.** The
  reduction is deterministic over its inputs; `math/rand` and `crypto/rand` are both absent, and the
  only comparisons are needle-equality inside `reachMatchedNeedle` (string equality against a
  fixture-owned needle, not a secret) — so constant-time comparison has no subject.
- **[Network & I/O] N/A by design — no socket, no reader, no deadline to set.** The only input is a
  `pinScan` value the caller already holds. Size limits are nonetheless inherited and **must not be
  undone**: every retained command is already capped at `reachMaxCommandBytes` by
  `reachCapCommand` (`background_reach_probe_test.go:945`). The spec forbids re-reading an uncapped
  argv to "repair" a truncation — the cap is the discipline, not a defect, and AC4's over-cap fixture
  row exists to prove the reduction survives one.
- **[Concurrency] No findings — nothing shared, nothing mutated.** No goroutine is spawned, so there is
  no lifecycle to leak; no lock is taken, so there is no ordering to document; no shared state is
  read-then-written, so there is no TOCTOU. The one aliasing fact is named in § Concurrency model:
  `Rows` aliases `scan.Matches[i].Needles` rather than deep-copying. This is safe because neither this
  file nor its callers mutate a `reachProc` after `reachMatchArgvRows` builds it, and the spec requires
  the aliasing be stated in the doc comment rather than defended with a copy — a copy would be a
  defence for a failure mode nobody has observed.
- **[Threat model alignment] OUT OF SCOPE, named with its owner.** The live `ps` read, its exclusion
  set, the staged command literal and the env delta are the driver and record tickets' surfaces; the
  trailer reading, run classification and the published artifact are #1337's. This ticket's whole
  contribution to the threat model is negative — it adds no capability: no exec, no file, no socket, no
  environment read, no publication site. `process_pin_liveness_test.go:603-613`'s known guard-quality
  gap from #1235 (`docs/knowledge/codebase/1235.md:87-94`) stays out and is not touched.

**Reviewer:** architect (self-review per `architect/security-review.md`)
**Date:** 2026-08-05
