# #1282 — Fill the gather's parameters from a real held command in its own process group

**Size:** S. One new file, zero production source files, zero consumer call sites.
**Package:** `internal/e2e/realclaude`, build tag `e2e_realclaude`.
**New file:** `internal/e2e/realclaude/finding_stage_held_group_test.go`.
**Identifier prefix:** `finStage*` (census at `980b141`: `finStage[A-Z]` = 0; controls `fin[A-Z]` = 587, `trail[A-Z]` = 1240, both non-zero).

---

## Files to read first

Turn-1 data load. Each entry says what to extract; do not re-derive any of it.

| Path | What to extract |
|---|---|
| `internal/e2e/realclaude/finding_run_gather_test.go:230-279` | `finGatherReadings`' body — the four parameters, which fields it stages, and where `record.Selected` is copied into `Admit`. **Call it; do not rebuild it and do not change its signature.** |
| `internal/e2e/realclaude/finding_run_gather_test.go:200-215` | The caller obligation this ticket discharges: `pinned` is `[]int`, and the caller holding `pinScan.Matches` converts at its own call site taking `.PGID` and nothing else. Also the conditional `PyryExited` note — see § *What stays staged*. |
| `internal/e2e/realclaude/trail_run_rig_test.go:506-556` | **The staging to copy.** `exec.LookPath` in the parent, both operands as positional parameters, `exit 0` to defeat the exec optimisation, `cmd.Env = []string{}`, `SysProcAttr{Setpgid: true}`, and the `defer` (not `t.Cleanup`) that kills `-pgid`. |
| `internal/e2e/realclaude/trail_run_rig_test.go:277-284` | The flip test's subject — the staging **not** to copy: no `SysProcAttr`, so the pinned group would be `syscall.Getpgrp()` and AC3's own-group arm would collapse into AC2's finding arm. |
| `internal/e2e/realclaude/trail_run_rig_test.go:119` | `trailRigHeldPGID()` — `syscall.Getpgrp()`, the exact hardcoding AC3's second arm traps. |
| `internal/e2e/realclaude/trail_run_rig_test.go:49-57` | The failure-message redaction rule this file's header restates, and the reason. |
| `internal/e2e/realclaude/trail_run_rig_test.go:572-586` | The per-matched-pid liveness loop and `pinIsVerdict` call to mirror — see § *Liveness is live for the first time*. |
| `internal/e2e/realclaude/process_pin_liveness_test.go:191-198` | `pinScanArgv(needles, exclude) (pinScan, error)` — zero `pinScan` on error. |
| `internal/e2e/realclaude/process_pin_liveness_test.go:128-136` | `pinScan`: `Matches []reachProc`, `RowsScanned`, `MatchCount`. `Matches` is the value that must never leave the helper. |
| `internal/e2e/realclaude/background_reach_probe_test.go:161-168` | `reachProc` — `PID`, `PPID`, `PGID`, and `Command`, which is verbatim argv. `.PGID` is the join key; `.Command` never leaves the scan. |
| `internal/e2e/realclaude/process_pin_liveness_test.go:206-232` | `pinStateColumns = "pid=,ppid=,stat="` and its never-add-`command` prohibition. This ticket adds no `ps` read of its own. |
| `internal/e2e/realclaude/process_pin_liveness_test.go:1142` | `pinIsVerdict` — the membership predicate over the four per-pid states. Call it; do not re-switch. |
| `internal/e2e/realclaude/finding_attribution_fanout_test.go:186-260` | `finAttributeFanOut` step 1 (dedupe + sort) and step 2 (`pgid <= 1` → unreportable). Step 1 is why two rows in one group reduce to one entry. |
| `internal/e2e/realclaude/finding_attribution_fanout_test.go:88-125` | `finAttributeRecord` — `Conditions`, `Unreportable`, `Entries`, `Selected`. `len(Entries)` is the distinct-group count. |
| `internal/e2e/realclaude/trailer_admissibility_test.go:533-537` | `trailReapLine(count int, pgids string) string` — the synthetic reap line in `reap.go:65`'s slog shape. |
| `internal/e2e/realclaude/trailer_admissibility_test.go:440-478` | `trailAdmitAttribution`'s verdict switch: `tdnReapNoLine` → `trailAdmitVoidNoLine`, `tdnReapHeldPGIDAbsent` → `trailAdmitVoidGroupUnnamed`, `tdnReapHeldPGIDKilled` + `LineCount == 1` → `trailAdmitProof`. |
| `internal/e2e/realclaude/teardown_liveness_test.go:144-158` | `tdnClassifyReapLog`'s `heldPGID <= 1` guard → `tdnReapInstrumentFailed`. Why `syscall.Getpgrp()` (always > 1) reaches the *absent* arm and not the instrument arm. |
| `internal/e2e/realclaude/trail_run_outcome_test.go:505-598` | Classifier Steps 2–8 verbatim. **Step 2 (proof) outranks Step 6 (liveness instrument) and Step 7 (`MatchCount > 0`).** This ordering decides every arm below. |
| `internal/e2e/realclaude/background_trigger_probe_test.go:663-700` | `holdProbeFIFO(t, path)` — mkfifo, holds the write end, returns the rendezvous channel, releases in a `t.Cleanup`. |
| `internal/e2e/realclaude/fifo_reader_liveness_test.go:86,100,106,133` | `fifoLiveReaderPresent`, `fifoLiveReaderCommand` (`cat`), `fifoLiveRendezvousWait`, `fifoLiveRead(path)`. |
| `internal/e2e/realclaude/result_trailer_observation_test.go:282` | `trailFixtureTrailer` — the ordinary trailer certifying `"completed"`. |

---

## Context

`finGatherReadings` (#1281) takes pyry's reap-log stderr and the pinned process-group set as parameters and proves both the finding and a genuine negative come out of its composition. Every row that proves it is driven from a synthetic stdout, a synthetic stderr and **hand-passed pgid integers**.

Hand-passed integers prove the composition; they do not prove the parameters can be filled. A live probe fills `pinned` from a real `pinScanArgv` over a real process table, taking each matched row's `.PGID` as the join key into pyry's reap log. This ticket closes that gap: a real held command, in a process group of its own, whose group comes off a real scan and lands on the finding through the gather.

Still offline — a real FIFO, a real `sh`, a real `cat`, a synthetic stdout buffer and a synthetic reap line. No live claude, no credentials, no turn, no `t.Skip`.

### The increment over #1281, stated exactly

`MatchCount` is the field that differs, and it moves the *negative* arm one step earlier:

| | #1281's rows | This ticket's rows |
|---|---|---|
| Subject staged | none | a real `sh` + `cat` held on a FIFO |
| `MatchCount` | 0 on every row | ≥ 2 on every row |
| `pinned` source | typed integers | a real `pinScanArgv` match set's `.PGID`s |
| Finding arm lands on | Step 2 → `trailOutcomeRunningAtTrailer` | Step 2 → `trailOutcomeRunningAtTrailer` (unchanged: Step 2 outranks Step 7) |
| Negative arm lands on | Step 8 → `trailOutcomeNoRowMatched` | **Step 7 → `trailOutcomeMatchedUnattributed`** |

The negative arm's move is the increment, not an inconsistency with the blocker. #1281's negative needle is a `t.TempDir()` path nothing is ever staged at, so nothing matches and it falls to Step 8. Here a real command carries the needle.

---

## Design

### One new file, three helpers, two tests

```
finding_stage_held_group_test.go
├─ constants          finStageFIFOName, finStageRendezvousWait
├─ finStageSubject    the staging's result: ints and needle paths, never a reachProc
├─ finStageHeldGroup  stage → rendezvous → scan → AC1's guard → call the body
├─ finStageReapLine   one anchored reap line naming one group
├─ finStageAssertLiveness   the per-matched-pid check Step 6 makes load-bearing
├─ TestFinStageRealHeldGroupFillsTheGather        AC1 (via helper), AC2, AC4
└─ TestFinStageRigHardcodingsCannotReachTheFinding  AC3
```

### `finStageSubject` — the staging's result, and AC5's structural half

```go
// finStageSubject is one staged held command's readings, carried as INTEGERS and
// needle paths and never as pinScan.Matches: reachProc.Command is verbatim argv
// off the operator's own process table.
type finStageSubject struct {
	Needles []string // this run's argv needle set — one t.TempDir()-derived FIFO path
	Pinned  []int    // each matched row's .PGID, in scan order, duplicates intact
	Group   int      // the one distinct group those rows share
	Rows    int      // pinScan.RowsScanned at staging time
}
```

`len(Pinned)` **is** the staging scan's `MatchCount` by construction — one entry per matched row — so AC4 needs no separate count field. `Pinned` keeps its duplicates: handing the fan-out the raw per-row slice is what makes step 1's dedupe the thing under test rather than a reduction this file performed first.

This struct is AC5's enforcement, in the same shape as `finAttributeFanOut`'s `[]int` signature: the type carries nothing from which a command string is reachable, so "no captured bytes leave the staging" is true by construction rather than by a discipline at each call site.

### `finStageHeldGroup` — the staging, and why it takes a body

```go
// finStageHeldGroup stages a command held un-finishable on a real FIFO in a
// process group of its own, pins that group off a real argv scan, asserts the
// group is distinct from the test process's own, and calls body with the result.
func finStageHeldGroup(t *testing.T, body func(finStageSubject))
```

**Why a callback and not a return value.** The `SIGKILL(-pgid)` must fire after the arms have run and before the test function returns, and AC1 forbids `t.Cleanup` for it (`trail_run_rig_test.go:538-542`). A helper that *returned* would fire its own `defer` at return, killing the subject before any arm ran. Passing the body in puts the `defer` in one place, ahead of every arm, and makes "no arm runs before the guard" structural instead of a rule each test remembers.

**Ordering against the FIFO release, stated.** `holdProbeFIFO` registers its release as a `t.Cleanup`, which runs after the test function returns. The kill `defer` is inside `finStageHeldGroup`, so it runs when the body returns — strictly *inside* the test body and therefore strictly *before* that release. The group dies by signal, not by EOF, and the release then closes a write end nothing is waiting on. Killing the **group** is what reaches the `cat`: it is a grandchild, and `cmd.Process.Kill()` on the shell does not reach it.

Sequence inside the helper:

1. `fifoPath := filepath.Join(t.TempDir(), finStageFIFOName)`; needles are `[]string{fifoPath}`. Unique per run, and no process's command line carries it before the subject execs — including the test binary's own.
2. `rendezvous := holdProbeFIFO(t, fifoPath)`.
3. `catPath, err := exec.LookPath(fifoLiveReaderCommand)` — resolved in the **parent**, `t.Fatalf` on error.
4. `cmd := exec.Command("sh", "-c", `"$1" "$2"; exit 0`, "sh", catPath, fifoPath)`. Both operands are positional parameters, so the command string is a compile-time constant with no interpolation site: `fmt.Sprintf("cat %s; exit 0", path)` is a shell-injection shape even when today's only input is `t.TempDir()`. `exit 0` after the command is what forces the fork across sh, dash, bash and zsh.
5. `cmd.Env = []string{}` — the subject inherits nothing. Under `make e2e-realclaude` this process may carry `CLAUDE_CODE_OAUTH_TOKEN` / `ANTHROPIC_API_KEY`, and `exec.Command` passes the parent environment through by default. `cat` needs no environment and the binary is already resolved from the parent's `PATH`.
6. `cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}` — **the load-bearing line**, then `cmd.Start()`.
7. `pgid := cmd.Process.Pid`, then the teardown `defer`, **three statements in this order**:

   ```go
   defer func() {
       _ = syscall.Kill(-pgid, syscall.SIGKILL) // reaches the grandchild `cat`
       _ = cmd.Process.Kill()                   // guarantees Wait() can return
       _ = cmd.Wait()
   }()
   ```

   `cmd.Process.Pid` is the right operand for the group kill *here*, and unfit as the subject of the guard in step 10.

   **The second kill is not redundant — see § *The teardown must not hang on the path it is meant to fail*.** The group kill alone is what reaches the `cat`; the direct kill alone is what keeps `cmd.Wait()` from blocking forever when the group kill finds no group.
8. Wait on `rendezvous` with a `time.After(finStageRendezvousWait)` arm; on timeout `t.Fatalf` naming the wait. The rendezvous fires when the `cat` opens the read end, which is also what guarantees it has execed and carries the needle in its argv.
9. `fifoLiveRead(fifoPath)` must read `fifoLiveReaderPresent` — the rendezvous proves a reader *arrived*, this proves one is *still holding* the read end at scan time.
10. `scan, err := pinScanArgv(needles, nil)`. Then, in order, each with its own `t.Fatalf`:
    - `err == nil` — an errored scan yields no match set to pin from.
    - `scan.RowsScanned > 0` — rows were read, separating this from a nothing-was-measured.
    - `scan.MatchCount > 1` — **AC4's first half**, and the premise for the wrapper/child pair.
    - Collect `pinned = append(pinned, m.PGID)` over `scan.Matches`; reduce to `distinct` (dedupe, no sort needed for a size check).
    - `len(distinct) == 1` — the rows share one group. Failure message names the **distinct pgid slice**, never the rows. This check has a second job: it is also the fail-closed gate against a *third-party* row carrying the needle. Any local process can put arbitrary bytes in its own argv, so the scan reads an ambient, untrusted table. An unrelated row matching the needle would land in a different group, `len(distinct)` would be 2, and the staging fails rather than pinning a group it did not stage.
    - **AC1's guard:** `distinct[0] != syscall.Getpgrp()`.
11. `body(finStageSubject{Needles: needles, Pinned: pinned, Group: distinct[0], Rows: scan.RowsScanned})`.

### AC1's guard: the subject of the comparison is the whole point

The guard exists so no arm below can be vacuous, and it is only worth writing if it can go red.

- `cmd.Process.Pid != syscall.Getpgrp()` **cannot** go red. A freshly forked child's pid is never the test process's group id, so that comparison holds whether or not `Setpgid` took effect. Dropping `SysProcAttr` — the exact regression the guard is for — leaves it silently green.
- `distinct[0] != syscall.Getpgrp()` **can**. Without `Setpgid` the scanned `PGID` *is* `syscall.Getpgrp()`, and it is the same value AC2 hands the gather. The guard goes red on the line that matters.

Without a group of its own, a synthetic stderr naming the pinned group would classify as `tdnReapHeldPGIDKilled` → `trailAdmitProof` for **both** the real arm and AC3's own-group arm — so AC3 would go red asserting the opposite of its claim, and #1268's hardcoding would look like it reaches the finding. The live shape agrees: `internal/agentrun/reap.go:52` skips `pgid <= 1 || pgid == self || pgid == rootPid`, so a command sharing pyry's own group is one the reaper can **never** report.

### The teardown must not hang on the path it is meant to fail

AC1's guard is designed to go red exactly when `SysProcAttr` is dropped — which is the mutation the § *Testing strategy* below tells the developer to run. **A guard that hangs instead of failing corrupts the verification it exists for**, so the teardown has to survive its own failure path.

The hazard, traced:

1. `Setpgid` is dropped, so the shell stays in the test process's group.
2. The guard `t.Fatalf`s. `t.Fatalf` calls `runtime.Goexit()`, which **runs deferred functions** — so the teardown fires.
3. `syscall.Kill(-pgid, SIGKILL)` targets group `pgid`, where `pgid` is the shell's *pid*. No such group exists — the shell is in the test's group. `ESRCH`, discarded by the `_ =`.
4. `cmd.Wait()` blocks on a live `sh`, which is waiting on a live `cat`, which is blocked reading a FIFO whose write end `holdProbeFIFO` still holds.
5. That write end is released by a `t.Cleanup`, which cannot run until the test goroutine finishes its `Goexit`. **Deadlock until the `go test` timeout.**

A developer running the mandated mutation would see a ten-minute hang and reasonably conclude the mutation *hung* rather than *went red*.

The direct `cmd.Process.Kill()` in step 7 closes it: the shell dies by signal regardless of what group it is in, so `Wait()` returns and the test fails in the normal way. The orphaned `cat` then receives its EOF from `holdProbeFIFO`'s cleanup and exits — one process outliving the test body by microseconds on a path that is already failing.

This is the same hazard `trail_run_rig_test.go:538-542` names as its reason for preferring `defer` over `t.Cleanup`, stated precisely: the danger is not the ordering of two cleanups, it is that **`cmd.Wait()` after a failed group-kill has no EOF source**. The ordering discipline and the second kill address the two halves of it.

The same reasoning covers the rendezvous-timeout path (step 8): with `Setpgid` intact the group kill suffices, and with it dropped the direct kill is what lets the timeout report itself.

### Two rows, one group — AC4

`sh -c '"$1" "$2"; exit 0'` forks rather than exec-replacing itself, so both the shell and the `cat` carry the needle in their argv and both sit in the one `Setpgid` group. `TestTrailRigCarriesMoreThanOneMatchedRow` already pins `MatchCount > 1` for this staging. `finAttributeFanOut`'s step 1 dedupes and sorts before anything else, so two rows carrying one group reduce to one entry.

The claim — *the count of groups is not the count of rows, proven against a real process table* — is two assertions on one arm:

- `len(subject.Pinned) > 1` (and `readings.MatchCount > 1` as corroboration from the gather's own scan)
- `len(record.Entries) == 1` and `record.Entries[0].PGID == subject.Group`

The live shape is the same pair: claude runs Bash through a shell wrapper, and #1268 mutation-tested that count (dropping the wrapper cut it 2→1).

### Liveness is live for the first time — and Step 6 sits above Step 7

**This is the design point most easily missed.** `readings.Liveness` is filled by `finGatherReadings` with one `pinReadState` per matched pid. In every prior `fin*` file the match set is empty, so `Liveness` is empty and Step 6 is unreachable. Here it is non-empty for the first time.

Step 6 (`pinStateInstrumentFailed` → `trailOutcomeVoidLivenessInstrument`) is consulted **before** Step 7. So:

- The **finding arm is immune** — Step 2 returns on `trailAdmitProof` before Step 6 is reached.
- AC2's **negative arm** and **both AC3 trap arms** are not. A single `pinStateInstrumentFailed` diverts them from `trailOutcomeMatchedUnattributed` to a `run-void-*` — precisely what AC2 forbids ("an answer, never a `run-void-*`").

`finStageAssertLiveness(t, readings)` therefore runs on every arm, mirroring `trail_run_rig_test.go:572-586`:

- `len(readings.Liveness) == readings.MatchCount` — one read per matched pid.
- `pinIsVerdict(read.Verdict)` for each — call the shipped predicate, never re-switch.
- no read is `pinStateInstrumentFailed` — every matched pid is a live process this test staged, so an instrument failure means the read did not run rather than that the process was gone. The message must say which of the two it is, because the alternative reading turns a broken instrument into a `run-void-*` that looks like a measured answer.

### The arms

Every arm calls `finGatherReadings(&stdout, subject.Needles, <stderr>, <pinned>)` and classifies its **returned readings** with `trailClassifyRun`. No arm hand-builds a `trailRunReadings`. Each arm seeds a **fresh** `probeSyncBuffer` with `trailFixtureTrailer` — an ordinary trailer certifying `"completed"`, which keeps Step 1 out of `trailOutcomeVoidBudgetFired`.

`finStageReapLine(pgid int) []byte` returns `[]byte(trailReapLine(1, fmt.Sprintf("[%d]", pgid)) + "\n")` — count 1 is what `trailAdmitAttribution` requires for `trailAdmitProof` (`LineCount == 1`, else `trailAdmitVoidNotOneReapLine`).

**`TestFinStageRealHeldGroupFillsTheGather`** — AC2 and AC4, over one staging:

| Arm | `stderr` | `pinned` | `Admit` | Outcome |
|---|---|---|---|---|
| the reap log names the real staged group | `finStageReapLine(subject.Group)` | `subject.Pinned` | `trailAdmitProof` | `trailOutcomeRunningAtTrailer` |
| the reap log names a different group | `finStageReapLine(subject.Group + 1)` | `subject.Pinned` | `trailAdmitVoidGroupUnnamed` | `trailOutcomeMatchedUnattributed` |

The two arms differ in **one dimension** — the group the reap line names. Everything else is byte for byte identical.

*Why `subject.Group + 1` and not a typed constant.* A literal like `4242` could collide with the real staged group on a busy machine, silently turning the negative arm into a second finding arm. `Group + 1` is unequal to `Group` by construction and is `>= 3`, so `finAttributeFanOut`'s step 2 does not mark it unreportable and `tdnClassifyReapLog`'s `heldPGID <= 1` guard does not fire. The value never leaves the synthetic stderr — nothing looks it up.

Per-arm assertions beyond the table: the gate reads `trailGateUsable` certifying `"completed"`; `finStageAssertLiveness`; `readings.MatchCount > 1`; and, on the finding arm only, AC4's `len(record.Entries) == 1` / `record.Entries[0].PGID == subject.Group` / `len(subject.Pinned) > 1`.

**`TestFinStageRigHardcodingsCannotReachTheFinding`** — AC3, over its own staging, three arms:

| Arm | `stderr` | `pinned` | Varies vs. control | `Admit` |
|---|---|---|---|---|
| **control** (AC2's finding arm) | `finStageReapLine(subject.Group)` | `subject.Pinned` | — | `trailAdmitProof` → `trailOutcomeRunningAtTrailer` |
| #1268's `nil` stderr | `nil` | `subject.Pinned` | stderr only | `trailAdmitVoidNoLine` |
| #1268's own-group key | the control's bytes, unchanged | `[]int{syscall.Getpgrp()}` | pinned pgid only | `trailAdmitVoidGroupUnnamed` |

The control runs **first and is asserted**, in this test's own staging. Without it a staging that could not reach the finding at all would make both traps pass while proving nothing.

**The two trap arms are never each other's control.** They differ in two dimensions, and a run varying both would assert its own construction rather than the claim.

**The assertion is at the `Admit` layer, never the outcome layer.** The word "void" names two things one layer apart. Both hardcodings produce an `Admit` **void**; neither produces a **void outcome**. With a certifying gate, `PyryExited` true, a clean scan and `MatchCount > 0`, both trap arms fall past Steps 3–6 to Step 7 and land on `trailOutcomeMatchedUnattributed` — an *answer* in the three-answer set. Both arms additionally assert `outcome.Value != trailOutcomeRunningAtTrailer`, which is the claim; asserting a `run-void-*` here would require breaking a *different* input and would vary two dimensions at once.

*Why the own-group arm reaches `trailAdmitVoidGroupUnnamed` and not `trailAdmitVoidInstrument`:* `syscall.Getpgrp()` is always > 1, so `tdnClassifyReapLog`'s `heldPGID <= 1` guard does not fire and `finAttributeFanOut`'s step 2 does not mark it unreportable. It reaches `tdnReapHeldPGIDAbsent` honestly. Confirm this — the instrument value would be a different claim.

**The trap is a test rather than a comment** because the failure it guards has no symptom in the answer: a gather wired to the un-passable stderr still composes, still classifies, and still returns a plausible outcome — a clean negative on every run, forever.

---

## Concurrency model

No goroutines are written by this ticket. Three pieces of concurrency are inherited and must be respected:

- **`holdProbeFIFO`'s goroutine** parks in `open(path, O_WRONLY)` until a reader arrives, closes the rendezvous channel, then waits on its release. The write end never leaves the helper. Its `t.Cleanup` is registered when the helper is called, so it runs after the test function returns.
- **The rendezvous select** is the only place this file blocks on the subject. It has a `time.After(finStageRendezvousWait)` arm; the timeout path `t.Fatalf`s rather than proceeding, because a reading taken without the rendezvous is not known to be about a live subject.
- **`finGatherReadings`' trailer poll** (`trailWaitForTrailer`, `finGatherTrailerWait` = 10 s) is entered with the buffer already seeded, so the first poll hits and no arm waits it out.

**Shutdown, in order:** body returns → `finStageHeldGroup`'s `defer` sends `SIGKILL` to `-pgid` (reaching both the shell and the `cat`), then to the shell directly → `cmd.Wait()` reaps the shell → helper returns → test function returns → `holdProbeFIFO`'s `t.Cleanup` closes the write end. Nothing waits on an EOF the kill has already made unnecessary.

**Shutdown on a failing path.** `t.Fatalf` runs deferred functions, so the teardown fires on every staging fault and on the AC1 guard alike. The direct `cmd.Process.Kill()` is what keeps `cmd.Wait()` from blocking when the group kill found no group — see § *The teardown must not hang on the path it is meant to fail*. Without it, the guard's own red path deadlocks until the `go test` timeout.

**The hold is what makes the scan-then-gather gap safe.** The pinned set is read once at staging time and each arm's gather re-scans the table. Between them the subject cannot exit: it is blocked reading a FIFO whose write end `holdProbeFIFO` holds for the whole test. That is the hold's purpose, and it is why no arm needs to re-take a reading or poll for one.

---

## Error handling

Every failure is a `t.Fatalf` — this file publishes no record and returns no error. Three classes, distinguished because collapsing them is how a broken instrument reads as a pass:

1. **Staging faults** (mkfifo, `LookPath`, `Start`, rendezvous timeout, FIFO reader absent). The reading was never taken. Fatal in the helper, before `body` runs.
2. **Instrument faults** (`pinScanArgv` errored, `RowsScanned == 0`, any `pinStateInstrumentFailed`). Something ran and could not answer. Fatal with a message naming which instrument and why the alternative reading would be a manufactured answer. **Never retried** — a poll that spins past an instrument failure turns the instrument's own breakage into a pass.
3. **Claim failures** (a wrong `Admit`, a wrong outcome, `len(Entries) != 1`, the AC1 guard). Fatal with a message naming the values on both sides and what the divergence means.

No arm has a retry loop. The subject is held on the FIFO for the whole test, so no reading needs to be re-taken.

---

## Redaction (AC5)

The file header states the rule and its reason, in `trail_run_rig_test.go:49-57`'s shape. **The rule stops being precautionary here:** this is the first `fin*` file whose scan matches live rows, so `pinScan.Matches` is non-empty for the first time in the family and `reachProc.Command` on those rows is verbatim argv off the operator's own process table.

- **May name:** counts, pids, pgids, verdicts, outcome values, gate and admit values, `BoundFrom`, `finAttribute*` condition names, the distinct-pgid slice.
- **May never name:** `pinScan.Matches` (or any `reachProc`), a `trailObservation`'s `Line`, a `trailScanResult`'s trailer, `pinExclusion.Command`.
- **May never print a whole `trailRunReadings` or a whole `finAttributeRecord`** — `%+v` or `%v` on either value. **Name scalar fields.** This diverges from the neighbouring file and the divergence is deliberate; see below.

#### Why the neighbour's whole-struct licence does not transfer

`finding_run_gather_test.go:100-108` permits printing a whole `readings` or `record`, "and only because `TestFinGatherReturnsNoCapturedBytes` proves it." **That proof does not cover this file's values.**

Every row in the blocker's file runs at `MatchCount == 0` — its own header says so — so `readings.Liveness` is empty on every value that test marshals. This ticket is the first in the family to fill it. `pinStateOutcome` (`process_pin_liveness_test.go:245-253`) carries three string fields the blocker's proof therefore never examined:

| Field | Contents |
|---|---|
| `Detail` | `tdnDetail`-formatted, quoting `StateColumn` |
| `StateColumn` | the classifier's own input, read off `ps` |
| `ToolStderr` | `ps`'s stderr from the per-pid read |

In practice these are safe — `pinStateArgs` is `-p <pid> -o pid=,ppid=,stat=`, so no command column and no environment column can reach them. **That is an argument from the shipped column list, not from the blocker's test**, and the distinction is the finding: a developer who copies the neighbour's licence would be relying on a proof that ran with the field empty. `finGatherExemptKeys`' `tool_stderr` entry anticipates this exact moment ("this file's rows match nothing, so `Liveness` is empty today and the first row that filled it would go red on a shipped field") — but that exemption lives in the blocker's test, which this ticket does not re-run with a filled `Liveness`.

Naming scalar fields costs nothing here: every assertion in this file is about a `.Value`, a count, or a pgid.

Enforced structurally rather than by discipline: `finStageSubject` carries `[]int` and needle paths, so `scan.Matches` never escapes `finStageHeldGroup`, and inside it the only fields read are `.PGID` and `.PID`. **The one-distinct-group failure message is the likeliest slip** — it must print the reduced `distinct []int`, never the rows it came from.

Two inherited disciplines this ticket adds no read of its own to: `pinScanArgv` → `reachScanArgv` uses `ps -axww -o pid=,ppid=,pgid=,command=` (`-ww` widens without touching the environment; never `-E`/`-Eww`), and `pinStateColumns` is `pid=,ppid=,stat=` with an enforcing test. **Do not add a command column to the per-pid read.**

`finGatherReadings`' own returns are already proven clean by `TestFinGatherReturnsNoCapturedBytes`; this ticket adds no new publishable record, so no new no-captured-bytes test is required.

---

## What stays staged, and what must not be touched

`finGatherReadings` hardcodes `PyryExited = true` and `ClaudeState = ""`. Its doc comment names #1282 as the owner of promoting them, but the obligation it states is **conditional**: *"BEFORE it feeds this gather a live pyry."* This ticket feeds it no pyry at all, so neither value can be exercised here — no offline test can distinguish either setting. Promoting them is **#1279's**, where a live pyry first makes `trailOutcomeVoidPyryDidNotExit` reachable.

**Do not** change `finGatherReadings`' signature. **Do not** edit `trail_run_rig_test.go` — #1268's `nil` literal is structural to that file and correct there. **Do not** rebuild `finAttributeFanOut`, re-derive the eleven-outcome set, or re-parse pyry's stderr for the reap line.

---

## Testing strategy

Two test functions, two stagings, five arms. Run with:

```
go test -race -tags e2e_realclaude -run '^TestFinStage' -v ./internal/e2e/realclaude/
```

Also required before the PR, because `make check` never compiles `e2e_realclaude`-tagged files:

```
go vet -tags e2e_realclaude ./internal/e2e/realclaude/...
go test -race -tags e2e_realclaude ./internal/e2e/realclaude/...
```

The full-package run is expected to be a PASS/SKIP split, not all-PASS — the live specs skip without credentials. Every test this ticket adds must **PASS**, never SKIP.

### Mutations that must turn a test red

Verify each by hand before opening the PR; each maps to an AC whose value would otherwise be assertable-but-unasserted.

- **Drop `SysProcAttr = &syscall.SysProcAttr{Setpgid: true}`** → AC1's guard goes red on `distinct[0] != syscall.Getpgrp()`. If it stays green, the guard is comparing the wrong operand. **It must go red in seconds, not hang** — if this mutation hangs, the teardown's direct `cmd.Process.Kill()` is missing or ordered after `cmd.Wait()`; see § *The teardown must not hang on the path it is meant to fail*. A hang here is a spec violation, not a slow test.
- **Change the guard's subject to `cmd.Process.Pid`**, keeping `Setpgid` dropped → green. This is the vacuous form; confirming it is green is what proves the scanned-`PGID` form is the load-bearing one.
- **Replace `subject.Pinned` with a typed integer** on AC2's finding arm → the reap line no longer names the pinned group, `Admit` falls to `trailAdmitVoidGroupUnnamed`, the finding arm goes red.
- **Drop the `exit 0`** from the shell command → the shell exec-replaces itself, one row matches, AC4's `MatchCount > 1` goes red.
- **Make the AC3 own-group arm's stderr differ from the control's** → the arm now varies two dimensions; there is no automatic red for this, so it is a review check rather than a mutation. Read the two arms side by side and confirm one expression is shared.
- **Force a `pinStateInstrumentFailed` into `Liveness`** (e.g. by pinning a pid of `-1`) → AC2's negative arm and both trap arms divert to `trailOutcomeVoidLivenessInstrument` and go red. Confirms Step 6 is genuinely above Step 7 for those arms and that `finStageAssertLiveness` is not decorative.

### Verify the mutation actually changed something

Run each mutation and confirm the specific *line* that reddens is the one the mutation targets, not an unrelated co-firing assertion. A mutation that reddens a neighbouring equality check has proved nothing about the assertion under test.

---

## Open questions

1. **`finGatherExemptKeys`' `tool_stderr` entry stays unexercised, and the blocker's no-captured-bytes proof still runs with an empty `Liveness`.** #1281 pre-placed that exemption for "the first row that filled `Liveness`". This ticket is that first row — but `TestFinGatherReturnsNoCapturedBytes` lives in #1281's file and stages no subject, so its `Liveness` is still empty, the exemption never fires, and `pinStateOutcome`'s three string fields are never walked. The Redaction § handles it defensively here (name scalars, never whole structs); **closing it properly means a staged-subject row in the blocker's own no-captured-bytes test, which is out of scope — that file may not be edited. File as a follow-up; do not widen this ticket.**
2. **Does `sh` on the CI runner fork as expected?** `TestTrailRigCarriesMoreThanOneMatchedRow` already ships the same staging and passes, so this is settled for the platforms in use. If AC4's `MatchCount > 1` ever flakes, the shell is the first place to look — not the scan.
3. **Whether AC2's negative arm should also assert `record.Selected == readings.Admit`.** The gather copies it under a non-`finAttributeNoGroups` record, and #1281's `finGatherAssertContract` already pins that equality for its own rows. Adding it here is cheap corroboration; leaving it out keeps the arm's claim about the outcome alone. Developer's call.

---

## Security review

**Verdict:** PASS (first pass FAILED with two MUST FIX; both fixed inline and re-walked)

**Findings:**

- **[Trust boundaries]** No findings. One boundary: `ps`'s ambient process table → parsed `reachProc` rows, crossed inside `finStageHeldGroup` and nowhere else. Any local process can put arbitrary bytes in its own argv, so the table is untrusted. `finStageSubject` carries `[]int` and needle paths, so `pinScan.Matches` structurally cannot escape the helper; only `.PGID` and `.PID` are read off it. The `len(distinct) == 1` check doubles as the fail-closed gate against a third-party row carrying the needle — it fails the staging rather than pinning a group it did not stage.

- **[Tokens, secrets, credentials]** **MUST FIX — fixed.** The spec licensed nothing explicitly about whole-struct printing, and the neighbouring file (`finding_run_gather_test.go:100-108`) *does* license it, on the strength of `TestFinGatherReturnsNoCapturedBytes`. That proof runs with `readings.Liveness` **empty** on every row — the blocker's own header states every row runs at `MatchCount == 0`. This ticket is the first in the family to fill `Liveness`, introducing three `pinStateOutcome` string fields (`Detail`, `StateColumn`, `ToolStderr`) the proof never examined. A developer copying the neighbour would rely on a proof that ran with the field empty. Fixed: § *Redaction* now forbids `%+v` on a whole `readings`/`record` and states why the licence does not transfer. (The fields are in fact safe — `pinStateArgs` is `-p <pid> -o pid=,ppid=,stat=` — but that is an argument from the shipped column list, not from the cited test.) No token is generated, stored, hashed or compared anywhere in this ticket.

- **[File operations]** No findings. `t.TempDir()` (Go-managed, `0700`, auto-removed); `syscall.Mkfifo(path, 0o600)` inside `holdProbeFIFO`, owner-only. The path is a compile-time-constant basename joined to a temp dir — no user input, no traversal vector. `Mkfifo` fails `EEXIST` rather than adopting an existing node, so the pre-create swap fails closed; the `0700` parent is not traversable by another user, which is also what makes the absence of `O_NOFOLLOW` on the FIFO open acceptable. All inherited from `holdProbeFIFO`, unchanged here. This ticket writes no files, so no atomic-write discipline applies.

- **[Subprocess / external command execution]** **MUST FIX — fixed.** `sh -c` is used, and justified rather than incidental: AC4's two-matched-rows claim requires the shell to fork. The injection surface is closed by shape, not by escaping — the script is a Go backtick literal with no interpolation site, and both operands arrive as positional parameters double-quoted as `"$1"` / `"$2"`, which `sh` does not re-scan for metacharacters. The `fmt.Sprintf("cat %s; exit 0", path)` form is named and rejected. Environment is explicitly scrubbed (`cmd.Env = []string{}`) against `CLAUDE_CODE_OAUTH_TOKEN` / `ANTHROPIC_API_KEY` under `make e2e-realclaude`; both binaries are resolved from the parent's `PATH` before the scrub, so the empty environment costs nothing. No double-fork escape: `cat` does not `setpgid`, so the group kill reaches it. **The finding was in teardown:** `syscall.Kill(-pgid, SIGKILL)` alone deadlocks on precisely the path AC1's guard exists to produce — with `Setpgid` dropped, `pgid` names no group, `ESRCH` is discarded, and `cmd.Wait()` blocks on a live `sh` whose `cat` awaits an EOF that only `holdProbeFIFO`'s `t.Cleanup` can deliver, which cannot run until the test goroutine finishes its `Goexit`. The mandated `Setpgid` mutation would hang for the `go test` timeout and read as "hung", not "red". Fixed: the teardown adds `cmd.Process.Kill()` between the group kill and `Wait()`, with § *The teardown must not hang on the path it is meant to fail* and a hang-is-a-spec-violation note on the mutation itself. `exec.LookPath` over the parent's `PATH` is a non-boundary — an actor controlling that environment already has code execution as the test user.

- **[Cryptographic primitives]** Not applicable, by design. No RNG, no hashing, no key material, no comparison against a secret. `t.TempDir()`'s uniqueness is a match-pattern property, not a security property: the needle is handed to a Go-side matcher and is never a filesystem operand or a capability.

- **[Network & I/O]** No findings. No sockets, listeners, HTTP or TLS. Every blocking wait is bounded: `finStageRendezvousWait` 10 s, `finGatherTrailerWait` 10 s (inherited), `reachPSTimeout` 5 s on the `ps` exec via `exec.CommandContext` (inherited). Retained strings are capped at `reachMaxCommandBytes` (512 B) by the shipped producers; this ticket retains none of its own.

- **[Error messages, logs, telemetry]** Covered by the two findings above; the operative rules are § *Redaction*'s may-name / may-never-name lists plus the whole-struct prohibition. Singled out as the likeliest slip: the `len(distinct) == 1` failure message, which must print the reduced `[]int` and never the rows it came from. No logging and no telemetry — this is a test file that publishes no record.

- **[Concurrency]** No findings. This ticket spawns zero goroutines and takes no locks; `holdProbeFIFO`'s goroutine and `probeSyncBuffer`'s mutex are inherited and already proven. Shutdown is stated for both the normal and the failing path (`t.Fatalf` runs defers, so teardown fires on every fault). The one check-then-use gap — the pinned set read at staging time versus each arm's re-scan — cannot go stale: the subject is blocked on a FIFO whose write end is held for the whole test, which is the hold's purpose.

- **[Threat model alignment]** No `docs/threat-model.md` exists in this repo (verified). The governing model is the `fin*`/`trail*` family's own, stated across `finding_attribution_fanout_test.go:195-202` and `process_pin_liveness_test.go:206-232`: *an artifact destined for a public GitHub issue must not carry the operator's credentials, read out of the ambient process table.* This ticket raises that exposure from theoretical to actual — it is the first `fin*` file whose scan matches live rows, so `pinScan.Matches` is non-empty and `reachProc.Command` holds verbatim argv off the operator's own machine. Addressed by § *Redaction* and by `finStageSubject`'s integer-only shape. Deferred and named: closing `finGatherExemptKeys`' `tool_stderr` exemption against a filled `Liveness` requires editing the blocker's file and is out of scope — Open question 1.

**Reviewer:** architect (self-review per `architect/security-review.md`)
**Date:** 2026-08-04
