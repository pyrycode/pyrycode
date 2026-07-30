# #1235 — Probe instrument: content-first pid pinning and a four-valued liveness read

**Size:** S (confirmed; contingent on the #1230 reuse, which is verified available — see § Blocker status)
**Deliverable:** one new test file. Zero production files. Zero modified files.

---

## Files to read first

Read these before writing anything. Every one is load-bearing; the reuse mandate (AC1) and
the no-touch mandate (AC5) both live in this list.

| Path | What to extract |
|---|---|
| `internal/e2e/realclaude/background_reach_probe_test.go:873-882` | `reachScanArgv` — the live `ps -axww -o pid=,ppid=,pgid=,command=` exec. **Call it; do not re-implement.** Note it discards partial output on error. |
| `internal/e2e/realclaude/background_reach_probe_test.go:884-929` | `reachMatchArgvRows` — the all-rows full-argv matcher. This is *the* matcher. AC1 is discharged by calling it. Note it returns `(matches, total)` and caps `Command` **after** matching. |
| `internal/e2e/realclaude/background_reach_probe_test.go:159-168` | `reachProc` row type — the row shape you reuse (`PID/PPID/PGID/Command/Needles`). It carries a **slice**, so it is not `==`-comparable (see § Testing, trap 2). |
| `internal/e2e/realclaude/background_reach_probe_test.go:945-962` | `reachCapCommand` / `reachMatchedNeedle` — the cap and the needle-membership read. Use `reachMatchedNeedle`, never re-scan `Command` (the cap may have truncated it). |
| `internal/e2e/realclaude/background_reach_probe_test.go:1175-1295` | `TestReachMatchArgvRows` — the table shape to mirror, and the behaviour your exclusion wrapper must leave **bit-identical** for its existing caller. Do not edit this test. |
| `internal/e2e/realclaude/background_reach_probe_test.go:57-92` | The redaction rules (file header). Rule 1 — **never `-E`, `-e` with an env column, or BSD `eww`** — binds your new `ps -p` call too. |
| `internal/e2e/realclaude/fifo_reader_liveness_test.go:80-254` | **The closest prior art.** `fifoLiveOutcome` + `fifoLiveRead` + `fifoLiveClassifyOpenErr`: a multi-valued instrument read with a dedicated `instrument-failed` value, a *pure* classifier split from the syscall, and a stable-name map. Mirror this structure exactly. |
| `internal/e2e/realclaude/fifo_reader_liveness_test.go:39-59` | Two lessons you must not re-learn: prove the flip on **one subject across one lifetime**, and `Kill()` is not the synchronisation point — `Wait()` is. |
| `internal/e2e/realclaude/fifo_reader_liveness_test.go:270-600` | The offline-test idiom in this package: real child process, no credentials, no `t.Skip`. |
| `internal/e2e/realclaude/background_trigger_probe_test.go:863-925` | `probeProcessSnapshot` / `probeDescendantsFromPS`. **Read to confirm you must not touch them.** The `len(fields) != 3` guard at `:896-898` is why widening the snapshot is forbidden (AC5). |
| `internal/e2e/realclaude/background_trigger_probe_test.go:927-970` | `probeAnnotateCommands` / `probeHasCommand` — the base-name defect this ticket exists to replace. Read so you don't reproduce it; do not edit. |
| `internal/e2e/realclaude/fixtures.go:96-107` | `WithWorktreeAuthenticated` — `t.Skipf`s without credentials. **Your tests must never reach this.** AC4 turns on it. |
| `internal/e2e/realclaude/fixtures_test.go:348-354` | `TestMain` — does **not** gate credentials. Offline tests genuinely run; the skip risk is per-test, not suite-level. |
| `docs/knowledge/codebase/1230.md` § Lessons learned | The error-gate placement lesson (round-1 MUST FIX) and the `reachProc`-grew-a-slice lesson. Both apply directly. |

---

## Context

Two live probes (#1236, then #1237) need one reading: *at the instant pyry wrote its
trailer, was the backgrounded Bash command still running?* Neither existing rig can take
it. This ticket builds the instrument only — no live turn, no verdict about pyry, nothing
downstream cites it as evidence. It is depended on as **code**.

Three defects to close, all measured rather than assumed:

1. **Identification is positional and by leaf name.** `probeAnnotateCommands` stores
   `filepath.Base(fields[1])`, so a held `cat <fifo>` records as `cat` and matches any
   unrelated `cat`. Already solved by #1230's `reachMatchArgvRows` — reuse it.
2. **The only process view is a subtree walk rooted at pyry.** A re-parented survivor
   reads as *absent*, which is indistinguishable from the probes' negative verdict.
   The re-check must be a lookup for one already-pinned pid with no descendant requirement.
3. **A state read that is not four-valued collapses two different findings.** `ps` lists
   zombies as rows, and `ps` exiting 1 does not distinguish "pid gone" from "instrument
   broken".

### Blocker status — verified, not assumed

#1230 has **merged**: `HEAD == origin/main == 834a176` ("Merge pull request #1232 from
pyrycode/feature/1230"). `reachScanArgv`, `reachMatchArgvRows`, `reachCommandColumn`,
`reachCapCommand`, `reachMatchedNeedle` and `reachProc` are all present on `main`. The
size-S contingency in the ticket body is satisfied; no `needs-rework:po` return is needed.

### Measurements taken during design (darwin 25.5, 2026-07-29/30)

The ticket's `ps` table reproduced exactly, plus two findings the ticket did not state.

| invocation | exit | stdout | stderr |
|---|---|---|---|
| `ps -p <dead> -o pid=,stat=` | 1 | *empty* | *empty* |
| `ps -p <live> -o nosuchcol=` | 1 | **`%cpu %mem acflag acflg args …`** (keyword list) | `ps: nosuchcol: keyword not found` |
| `ps -Z9q` (illegal option) | 1 | *empty* | `ps: illegal option -- Z` + usage |
| `ps -p 1 -o pid=,ppid=,stat=` | 0 | `    1     0 Ss` | *empty* |

**Finding A — a real zombie reads `ZN`, not `Z`.** Measured against a live zombie
(pid 15302): `ps -p 15302 -o pid=,ppid=,stat=` → `15302 15287 ZN`, and
`-o command=` → `<defunct>`. An equality check `state == "Z"` **misses a real zombie on
this machine.** The classification must test the **first rune**. Linux emits `Z` / `Z+`;
macOS appends flag characters (`N` = nice-adjusted). First-rune is the only form that
covers both.

**Finding B — the zombie arm lives on the *success* path.** That lookup exits **0** with a
well-formed row. Without a state column a zombie is therefore indistinguishable from a
running process — it reads as `running`. This is precisely why AC3 requires the state
column in the recorded output and not only in the classifier's logic.

Together these mean the discriminator between "the pid is gone" and "the instrument is
broken" is **stderr**, never the exit status — and the keyword-list row is the trap: code
that parses whatever stdout it got alongside an error reads `%cpu %mem acflag …` as
process rows.

---

## Design

### New file, new prefix

`internal/e2e/realclaude/process_pin_liveness_test.go`, build tag `//go:build e2e_realclaude`,
package `realclaude`.

**Identifier prefix: `pin`.** Verified free — the package uses `probe*` (on `main`),
`reach*` (#1230), `fifoLive*` (#1239); in-flight branches use `bg*` (#1240) and `stream*`
(#1174). Same package means a colliding prefix is a *redeclaration*, not merely a merge
conflict, so this was checked against remote branches rather than the worktree alone.

Nothing in this file is exported. Nothing outside this file is modified.

### Part A — exclusion-aware argv scan (AC1, AC2)

The matcher is `reachMatchArgvRows`. This part adds **only** a post-filter. It contains no
scanning, no parsing of `ps` output, and no second exec.

```go
// pinExclusion is one row the caller asked to be kept out of the matches,
// recorded with the reason so the record shows what was withheld and why.
type pinExclusion struct { PID int; Reason string; Command string; Needles []string }

// pinScan is the whole outcome. Matches is a SLICE and MatchCount an int:
// nothing here ever resolves to "the" pid.
type pinScan struct { Matches []reachProc; Exclusions []pinExclusion; RowsScanned, MatchCount int }
```

Three functions:

- `pinPartition(matches []reachProc, total int, exclude map[int]string) pinScan` — pure
  partition of already-matched rows by the caller's exclusion set. The shared core.
- `pinMatchArgvExcluding(table []byte, needles []string, exclude map[int]string) pinScan` —
  `pinPartition(reachMatchArgvRows(table, needles))`. The byte-pure surface the table tests drive.
- `pinScanArgv(needles []string, exclude map[int]string) (pinScan, error)` — the live
  wrapper: `reachScanArgv` for the exec, then `pinPartition`. Returns the zero `pinScan`
  on error, mirroring `reachScanArgv`'s discard-on-error contract.

Behaviour contract:

- Every matched row is retained; **the first is never preferred**. `MatchCount > 1` is a
  visible property of the record, not a condition resolved inside the instrument (AC1).
- Each retained row already carries its command-column text and matched needles — free from
  `reachProc`. Read membership via `reachMatchedNeedle`, never by re-scanning `Command`.
- An excluded pid is recorded in `Exclusions` with its reason **and** its command, so the
  record shows a withheld row was genuinely the instrument's own and not a lost finding.
- A pid in `exclude` that matched nothing produces no entry — an exclusion is recorded only
  when it actually fired.
- `RowsScanned` is `reachMatchArgvRows`' total, carried through unaltered.

**AC2's "existing caller unaffected" is structural, not a promise.** `reachMatchArgvRows`
is not edited; exclusion is a strictly downstream filter in a new function. A test pins it
(§ Testing).

### Part B — four-valued per-pid state read (AC3)

Four values, mutually exclusive, never collapsed:

```go
pinStateRunning          = "running"
pinStateExitedNotReaped  = "exited-but-not-yet-reaped"
pinStateNoSuchProcess    = "no-such-process"
pinStateInstrumentFailed = "instrument-failed"
```

```go
// pinStateOutcome is one read. StateColumn carries the gate's own input so a
// reader of the evidence sees what was classified, not merely the verdict (AC3).
type pinStateOutcome struct {
    Verdict, Detail string
    PID, PPID       int
    StateColumn     string
    ExitStatus      int
    ToolStderr      string
}
```

- `pinReadState(pid int) pinStateOutcome` — execs
  `ps -p <pid> -o pid=,ppid=,stat=` under a `context.WithTimeout` (reuse `reachPSTimeout`,
  5 s). Uses `.Output()`, which populates `(*exec.ExitError).Stderr`. **No `-E`, no `-e`
  with an environment column, no BSD `eww`** — see § Security review. Takes no `*testing.T`
  and never fails a test: an instrument failure observed mid-turn is a datum, not an abort.

  The column spec lives in a named package-level constant (`pinStateColumns =
  "pid=,ppid=,stat="`) so the tripwire test in § Testing can assert on it. A doc comment
  names the forbidden flags, mirroring `reachScanArgv:868-872`.
- `pinClassifyState(pid int, stdout []byte, err error) pinStateOutcome` — **pure**, and the
  entire testable surface. Splitting it from the exec is what lets the keyword-not-found and
  illegal-option arms be covered without provoking a real broken `ps`.
- `pinStateRow(stdout []byte) (pid, ppid int, state string, ok bool)` — parses one row using
  the package's `strings.Fields` / exactly-3-fields / `strconv.Atoi` discipline.
- `pinIsZombie(state string) bool` — `len(state) > 0 && state[0] == 'Z'`. **First rune, not
  equality** (Finding A). Carries a doc comment naming the measured `ZN` and the Linux `Z+`.

**Branch order is the contract**, not an implementation detail — it is what keeps the
stdout-alongside-error trap unreachable:

| # | Condition | Verdict |
|---|---|---|
| 0 | `pid <= 0` — checked **before** the exec | `instrument-failed` — no `ps` is run |
| 1 | `err != nil`, stderr (trimmed) non-empty | `instrument-failed` — names exit status + stderr |
| 2 | `err != nil`, not an `*exec.ExitError` (timeout, binary missing) | `instrument-failed` |
| 3 | `err != nil`, stderr empty, **stdout non-empty** | `instrument-failed` — stdout is *never* parsed |
| 4 | `err != nil`, stderr empty, stdout empty | `no-such-process` — the measured signature |
| 5 | `err == nil`, no usable row | `instrument-failed` — an absence is not a reading |
| 6 | `err == nil`, row parsed, `pinIsZombie(state)` | `exited-but-not-yet-reaped` |
| 7 | `err == nil`, row parsed, otherwise | `running` |

Rows 1 and 3 both precede row 4: **stderr and stdout are each checked before the
no-such-process arm is reachable.** Row 4 is the only input in the whole instrument that
yields `no-such-process`, mirroring `fifoLiveRead`'s ENXIO discipline.

Row 0 is a guard, not a reading: a non-positive pid is the instrument being called wrongly.
It matters because `strconv.Itoa(-1)` produces `-1`, which `ps` would consume as a flag
rather than as an operand — the one shape in this design where an integer could reach an
exec argument position and be read as something other than an operand. Rejecting before the
exec removes the question.

Every `instrument-failed` outcome carries a non-empty `Detail` plus `ExitStatus` and/or
`ToolStderr` — AC3's "names the failure". **`ToolStderr` and `Detail` are capped via the
existing `reachCapCommand`** before being recorded: Go's `.Output()` caps captured stderr at
32 KB, and the measured illegal-option case emits multi-line usage text, which is far too
much to carry into an artifact destined for a public issue.

**No descendant requirement.** `ps -p <pid>` is a direct lookup; there is no root, no walk,
no parent. A target re-parented to pid 1 reads exactly like any other (fixture in § Testing).
`PPID` is recorded so that property is visible in the evidence rather than merely argued.

### AC5 — what is deliberately not touched

`probeProcessSnapshot` (`:867`) and `probeDescendantsFromPS` (`:891`) are unchanged. The
per-pid read uses its **own narrow `ps -p`** with its own column set. The three-integer
snapshot is never widened: `probeDescendantsFromPS` skips any line where
`len(fields) != 3`, so adding a state column would make *every* line fail that guard and
the walk return empty — reading as "the command is absent", the exact false negative this
instrument exists to prevent. Three tests plus a helper comment pin that format.

Likewise `reachMatchArgvRows` and `TestReachMatchArgvRows` are untouched, and no second
full-argv matcher, descendant walk, or process-enumeration mechanism is introduced.

---

## Concurrency model

Minimal by construction. `pinReadState` execs one short-lived `ps` under a 5 s
`context.WithTimeout` and blocks on it; a timeout surfaces as branch 2
(`instrument-failed`, non-`ExitError`) rather than a hang. No goroutines, no channels, no
shared state — the instrument is a pure function plus one bounded exec.

The one live-process test (§ Testing) owns a child process across its own lifetime:
`Start` → read → `Kill` → read → `Wait` → read. `Wait` is the synchronisation point for
the reap, per `fifo_reader_liveness_test.go:52-59`; reading between `Kill` and `Wait` is
what makes the zombie arm observable at all, so the ordering is load-bearing.

---

## Error handling

| Failure | Handling |
|---|---|
| `ps` exits non-zero with stderr | `instrument-failed`, exit status + stderr recorded |
| `ps` exits non-zero, silent | `no-such-process` — the only path to this verdict |
| `ps` exits 0 with unparseable output | `instrument-failed` — never an absence |
| `ps` exec times out / binary missing | `instrument-failed` (branch 2) |
| Argv scan exec fails | `pinScanArgv` returns the error and the zero `pinScan`; the caller gates on it |
| Malformed / short / empty bytes | Skipped by the reused matcher's existing guards; `pinScan` returns empty slices with a real `RowsScanned`. Never a panic |

**Gate placement.** Per #1230's round-1 MUST FIX, the caller's error gate belongs *after*
the match outcome is decided, so a failed lookup neither relabels a genuine match nor
suppresses a genuine "scan fired, no row matched". This ticket ships no consumer, so the
obligation is discharged by keeping the two reads **independent**: `pinScanArgv`'s error
and `pinStateOutcome`'s verdict are separate values with no cross-assignment. #1236 inherits
the gate-placement duty and this spec says so explicitly rather than leaving it implied.

---

## Testing strategy

All offline. No live claude, no credentials, no daemon, no `t.Skip`. The tests must never
reach `WithWorktreeAuthenticated` (`fixtures.go:96`), which `t.Skipf`s without credentials.

**AC4's evidence command** — run with `ANTHROPIC_API_KEY` and `CLAUDE_CODE_OAUTH_TOKEN`
unset, and paste the output:

```
env -u ANTHROPIC_API_KEY -u CLAUDE_CODE_OAUTH_TOKEN \
  go test -tags e2e_realclaude -run '^TestPin' -v ./internal/e2e/realclaude/
```

Required: `--- PASS` for **every** new test and `--- SKIP` for none. A suite-level exit 0 is
**not** evidence — every test in this package skips to exit 0 without credentials.
`TestMain` (`fixtures_test.go:348`) does not gate credentials, so offline tests genuinely
run; the skip risk is per-test.

### `TestPinMatchArgvExcluding` — table over a local fixture

Define a `pinArgvFixture` const in this file (do **not** reuse `reachArgvFixture` — this
table needs an excluded row whose argv carries the needle, which that fixture has no reason
to grow). Include: two rows matching the needle, one instrument-owned row that *also*
matches, one non-matching row, and malformed lines (one field, two fields, non-integer pid).

Scenarios:

- Empty exclusion set → matches identical to `reachMatchArgvRows`' own output over the same
  bytes. **This is AC2's "existing caller unaffected" assertion** — assert against the
  reused matcher directly rather than a hardcoded list.
- An excluded pid whose argv **does** contain the needle → appears in `Exclusions` with its
  reason and command, and **not** in `Matches`. (AC2's named fixture.)
- Two rows match → both present, `MatchCount == 2`, order preserved, nothing resolved to
  the first. (AC1.)
- Each match carries the command-column text it matched in, and `reachMatchedNeedle` reports
  the needle. (AC1.)
- An exclusion entry for a pid that matched nothing → no exclusion recorded.
- Malformed, short, and empty input → empty matches, real `RowsScanned`, no panic. (AC4.)

### `TestPinClassifyState` — table over synthetic `(stdout, err)`

Construct `*exec.ExitError` values with a populated `Stderr` field directly; do not shell
out to a deliberately broken `ps`. Verify per case: verdict, `StateColumn`, and that
`instrument-failed` carries a non-empty `Detail` plus exit status or stderr.

| stdout | err | expect |
|---|---|---|
| *empty* | exit 1, stderr *empty* | `no-such-process` |
| `%cpu %mem acflag acflg args …` | exit 1, stderr `ps: nosuchcol: keyword not found` | `instrument-failed`; **`StateColumn` empty** — stdout not parsed |
| *empty* | exit 1, stderr `ps: illegal option -- Z` + usage | `instrument-failed` |
| `4242 1 S` | exit 1, stderr *empty* | `instrument-failed` (defensive; stdout never parsed alongside an error) |
| `4242 1 S` | `context.DeadlineExceeded` (not an `ExitError`) | `instrument-failed` |
| `15302 15287 ZN` | nil | `exited-but-not-yet-reaped`, `StateColumn == "ZN"` — the **measured macOS** shape |
| `15302 15287 Z+` | nil | `exited-but-not-yet-reaped` — the Linux shape |
| `4242 1 S` | nil | `running`, `PPID == 1` — **AC3's re-parented fixture**; no descendant requirement |
| `1 0 Ss` | nil | `running` |
| *empty* | nil | `instrument-failed` — an exit-0 absence is not a reading |
| `garbage` | nil | `instrument-failed` |
| `4242 1` (short) | nil | `instrument-failed` |
| *empty* | nil, called via `pinReadState(0)` | `instrument-failed` — the row-0 guard, no exec |

Plus two whole-table assertions (AC3's "no fixture maps to more than one"): every verdict is
one of the four constants, and no case yields a verdict outside its row.

### `TestPinStateColumns_ReadsNoEnvironment` — deterministic tripwire

The forbidden-flag rule is a doc comment, which is advisory. Pair it with a deterministic
check in different fabric: assert that `pinStateColumns` contains none of `command`,
`environ`, `args`, and that the exec argument list this file builds contains no `-E`, no
`-e`, and no BSD-syntax `eww`. This is cheap and it fires the moment someone widens the
column list to "improve the record" — the realistic regression, since the record is the
thing under pressure. It asserts on a constant, which is the point: the constant is what a
future edit changes.

### `TestPinReadState_FlipsAcrossOneProcessLifetime` — one subject, one lifetime

Synthetic bytes prove the classifier; they do **not** prove the exec wiring — a wrong column
order or a bad flag would pass every table case and fail in production. Per #1239's lesson,
prove the flip on **one** subject across **one** lifetime, not on separately constructed
subjects:

1. `Start` a `sleep` child → `pinReadState(child.Pid)` is `running`.
2. `Kill` the child, do **not** `Wait` → the read is `exited-but-not-yet-reaped`, and the
   recorded `StateColumn` begins with `Z`. This is the arm that would silently read
   `running` under an equality check against `"Z"` (Finding A), and the arm that a
   state-column-less lookup cannot see at all (Finding B).
3. `Wait` the child (reaping it) → the read is `no-such-process`.

`Wait` is the synchronisation point; step 2 must be observed before it. If step 2 proves
flaky under load, poll for a `Z`-prefixed state with a bounded deadline rather than
weakening the assertion — the zombie window is owned by this test, since the test process
is the child's parent and nothing else reaps it.

Register a `t.Cleanup` doing a best-effort `Kill` + `Wait` immediately after `Start`. Any
`t.Fatalf` between steps 1 and 3 otherwise leaves a live `sleep` child owned by a dead test
process — a real leak on a developer's machine, and the reason the flip test spawns `sleep`
rather than something long-lived.

### Traps

1. **`ps` output is width- and locale-sensitive.** Never assert exact spacing; parse with
   `strings.Fields`.
2. **`reachProc` carries a slice, so it is not `==`-comparable** and `%+v` comparison would
   silently start comparing the 512-byte-capped `Command`. Compare field-by-field
   (`docs/knowledge/codebase/1230.md` § Lessons learned).
3. **Do not add a `t.Skip` for "no ps".** `ps` is POSIX-mandatory on both supported
   platforms; a skip here would recreate the false-green AC4 exists to forbid.

---

## Security

The ticket carries `security-sensitive`. Full pass in § Security review below; the two
design-level commitments:

- **Argv only, never envp.** This file must never invoke `ps -E`, `ps -e` with an
  environment column, or BSD-syntax `ps eww`. `WithWorktreeAuthenticated` requires
  `CLAUDE_CODE_OAUTH_TOKEN` or `ANTHROPIC_API_KEY` in the outer environment, and pyry passes
  its environment to claude verbatim; one flag is the difference between this instrument and
  dumping a live credential into a public issue. The new `ps -p <pid> -o pid=,ppid=,stat=`
  reads three columns, none of them an environment. The safe design is to **not have the
  capability**.
- **No second full table.** The per-pid re-check is a lookup for one known pid and is not a
  reason to persist a second full process table into any record.

---

## Open questions

1. **Does `stat=` behave identically on Linux?** Verified on darwin 25.5 (`ZN`, `Ss`).
   `stat` is a valid column on procps and emits `Z` / `Z+`; the first-rune rule covers both.
   Unverified on a Linux runner — but CI does not run the `e2e_realclaude` tag, so this
   binds only an operator running the suite on Linux. The first-rune rule is the mitigation;
   no fallback is specified.
2. **Should `pinScan` record the needles that matched *nothing*?** Deferred. #1236 will know
   whether a zero-match needle is worth distinguishing from a zero-match scan; adding it
   now is a defence for a failure mode not yet observed.
3. **Exclusion reasons are caller-supplied strings, not an enum.** Deliberate: the
   instrument does not know why a caller excludes a pid. If #1236 and #1237 converge on the
   same three reasons, promoting them to constants is that ticket's call.

---

## Security review

**Verdict:** PASS

Run per the `security-sensitive` label, following `architect/security-review.md`. Default
verdict was FAIL until every category below was walked.

**Assets at risk.** The operator's `CLAUDE_CODE_OAUTH_TOKEN` / `ANTHROPIC_API_KEY`; the full
process table of the operator's machine (every process's argv, including unrelated work);
temp paths and the run's session UUID. The consuming probes paste their artifacts into a
**public issue**, so the blast radius of any disclosure here is public.

**Findings:**

- **[1. Trust boundaries]** No findings. `ps` stdout is untrusted input — any local process
  controls its own argv, so an unrelated process can place chosen bytes into the table this
  code reads. The boundary is explicit and singular per read: `reachMatchArgvRows`
  (`background_reach_probe_test.go:895`) for the argv side, parsing only pid/ppid/pgid as
  integers and treating the remainder as opaque capped text; `pinClassifyState` for the
  state side. Downstream holds parsed types (`reachProc`, `pinStateOutcome`) only — the raw
  table never becomes a record field. The `stat` column is kernel-generated from a small
  fixed alphabet and is not attacker-controlled.

- **[2. Tokens, secrets, credentials]** SHOULD FIX — *addressed in the design.* This code
  generates, stores and compares no tokens; the asset is the operator's credential in the
  environment, which the instrument must never read. Closed by **absence of capability**:
  the column spec is `pid=,ppid=,stat=` with no `-E`, no `-e` with an environment column,
  and no BSD `eww`. The realistic regression is a future developer widening the column list
  to "improve the record", and a doc comment alone is advisory. Paired with a deterministic
  tripwire in different fabric — `TestPinStateColumns_ReadsNoEnvironment` asserts the
  constant contains no environment-bearing flag. Note the asymmetry that makes this the
  highest-severity category here: one flag is the difference between this instrument and
  pasting a live credential into a public issue.

- **[3. File operations]** Not applicable, by design decision rather than omission: this
  instrument opens, creates, reads and writes **no files**. No path is derived from any
  input, no temp file is made, no mode is chosen. Artifact writing belongs to the consuming
  probes (#1236/#1237), which own their own file modes.

- **[4. Subprocess execution]** SHOULD FIX — *addressed in the design.* This is the category
  with real surface. `sh -c` is never used. The only non-constant exec argument in the new
  code is `strconv.Itoa(pid)`. A non-positive pid renders as `-1`, which `ps` would consume
  as a **flag rather than an operand** — the single shape in this design where an integer
  reaches an argument position and could be read as something else. Closed by classifier
  row 0: `pid <= 0` is rejected before any exec. Needles are matched in Go, in-process, and
  are never passed to `ps`, `grep`, or a shell. The child inherits the parent environment,
  which is harmless because `ps` does not print its *own* environment — the risk is `ps`
  printing *other* processes' environments, which is category 2's flag rule. `ps` is
  short-lived and bounded by a 5 s `CommandContext`, which kills it on timeout; no
  double-fork escape is possible from a process that does not fork.

- **[5. Cryptographic primitives]** Not applicable, with reason: no randomness, no hashing,
  no key material, and no comparison of an attacker-controlled value against a secret. There
  is nothing for `crypto/subtle` to protect. Fixtures are hand-written constants, so even
  the "`math/rand` acceptable for test fixtures" carve-out is unused.

- **[6. Network & I/O]** No findings. No network, no sockets, no listeners. On input-size
  limits: `.Output()` reads stdout unbounded in principle, but the per-pid lookup returns
  one row, and Go's `exec` caps captured stderr at 32 KB. No **new** unbounded read is
  introduced — the full-table read is #1230's existing, already-reviewed `reachScanArgv`.
  The ticket's "keep the reads proportionate" rule is honoured: the per-pid re-check is a
  lookup for one known pid and does not persist a second full table. Timeout discipline is
  explicit at 5 s per exec.

- **[7. Error messages, logs, telemetry]** SHOULD FIX — *addressed in the design.*
  `pinStateOutcome.ToolStderr` carries `ps`'s stderr into a record destined for a public
  issue, and the measured illegal-option case emits multi-line usage text (up to the 32 KB
  stdlib cap). Capped via the existing `reachCapCommand` rather than a new capping helper.
  Its *content* is not attacker-controlled — the stderr text derives from the instrument's
  own constant arguments — so this is a volume finding, not a disclosure one. Residual,
  stated rather than hidden: `pinExclusion.Command` publishes the instrument's own process
  argv, which discloses a temp path — the same class #1230 already publishes.
  MUST-NOT-record: any environment, any full process table. MUST-record: verdict, pid,
  state column, exit status.

- **[8. Concurrency]** SHOULD FIX — *addressed in the design.* No goroutines, no locks, no
  shared mutable state; the instrument is a pure function plus one bounded exec, so lock
  ordering and TOCTOU-on-shared-state do not arise. Process lifecycle does: the flip test
  `Start`s a child, and any `t.Fatalf` between steps 1 and 3 would leave a live `sleep`
  owned by a dead test process. Closed with a `t.Cleanup` best-effort `Kill` + `Wait`
  registered immediately after `Start`.

- **[9. Threat model alignment]** No findings. There is no `docs/threat-model.md`; the
  governing model for this family is the redaction ruleset in
  `background_reach_probe_test.go:57-92`. Rules 1 (argv never envp), 2 (raw table confined
  to one stack frame), 5 (cap every retained command) and 6 (only the integer snapshot
  persisted verbatim) all bind this ticket and are honoured above. This ticket **strengthens
  rule 3**: where #1230 relied on needles that *happen* not to collide with the instrument's
  own processes, AC2's exclusion set withholds instrument-owned pids explicitly and records
  the reason.

**Additional note — TOCTOU on pid reuse (SHOULD FIX, deferred to the consumer).** The pid is
pinned by content, then re-read later; the kernel could reuse it for an unrelated process in
between, which would read `running`. No single-pid lookup can detect this, and it is
precisely why AC3 demands four values rather than a boolean. The mitigation belongs to
#1236, which must join on the content match as well as the liveness read. Named here so the
constraint is inherited explicitly rather than rediscovered.

**Fail-open vs fail-safe.** The classifier fails **safe** in the direction that matters:
every ambiguous input lands on `instrument-failed`, never on `no-such-process` — the verdict
a consumer would read as a finding. Branch 3 (stdout non-empty alongside an error) is the
explicit fail-safe for the measured keyword-list trap. The one fail-open direction is a `ps`
that exits 0 with a plausible but wrong row; nothing in a single lookup can detect that,
which is why the flip test measures the wiring against a real process rather than trusting
synthetic bytes alone.

**No MUST FIX.** The four SHOULD FIX findings were cheap enough to fold into the design
rather than defer to code review; each is marked *addressed in the design* above and is
reflected in § Design and § Testing strategy.

**Reviewer:** architect (self-review per `architect/security-review.md`)
**Date:** 2026-07-30
