# #1251 — Teardown-liveness probe: does a backgrounded Bash command survive pyry's teardown, and by whose hand does it die?

**Ticket:** [#1251](https://github.com/pyrycode/pyrycode/issues/1251) (split from #1231)
**Size:** S — confirmed, not overridden
**Labels:** `security-sensitive`, `needs-real-claude`
**Blocker:** #1250, merged in PR #1252 (`aa523fa`). Everything this ticket calls is on `main`.

---

## 0. Size check, and what this spec deliberately does not build

### 0.1 The red lines, counted

| Red line | Count | Verdict |
|---|---|---|
| > 3 new files | **1** (`internal/e2e/realclaude/teardown_liveness_probe_test.go`) plus one bounded edit to `teardown_liveness_test.go` | pass |
| > 5 new exported types | **0** — the package is `internal`, build-tagged `e2e_realclaude`, and every symbol here is lowercase | pass |
| > 10 consumer call sites updated simultaneously | **6** lines, all inside `teardown_liveness_test.go` (`:328`, `:346`, `:363`, `:868`, `:885-886`, `:934-940`, `:948-963`). Verified by `grep -rn '\.Liveness\|\.ArgvScan\b\|rec\.FIFO\b\|tdnRecord\|writeTdnArtifacts' internal/e2e/realclaude/` — there is no second consumer. #1236's rig is **not** on `main` (no `trail*` symbols exist in the package), so the widening cascades nowhere. | pass |
| > 5 acceptance criteria | **5** exactly | pass |
| ≥ 10 distinct reject branches in a state machine | **7**, all routed through **two** helpers (`tdnFinish` for record-and-skip, `tdnDecideAfter` for the verdict) per the ticket's own Technical Note | pass |
| ≥ 5 production source files (`*.go` excluding `*_test.go`) | **0** — this ticket adds no production code | pass |
| > ~600 LOC total written work | **projected ~750–850**, over the generic line. See § 0.2. | pass, on package-specific cost data |

### 0.2 The LOC line, honestly

The generic ~600-LOC ceiling is calibrated on tickets whose cost is scattered edits across a codebase. This package's cost shape is different and there is direct, four-ticket evidence for it:

| Ticket | File | LOC shipped | Size label | Rework / max_turns |
|---|---|---|---|---|
| #1230 | `background_reach_probe_test.go` | 1461 | `size:s` | none |
| #1235 | `process_pin_liveness_test.go` | 1198 | `size:s` | none |
| #1239 | `fifo_reader_liveness_test.go` | 663 | `size:s` | none |
| #1250 | `teardown_liveness_test.go` | 988 | `size:s` | none |

None carries a `rework-count` or `error:developer:*` label. #1250's own knowledge doc records the shape explicitly: *"File landed at 988 lines against the spec's ~550–600 estimate — assessed as a spec-estimate miss, not scope creep… the comment-to-code ratio (0.45) matches the package's siblings (0.39 / 0.48 / 0.48)."* The LOC in this package is dominated by design commentary written in one or two `Write` calls, not by per-site Edit cycles — and #1251 is the **most** reuse-heavy of the family: ten of its eleven instruments already exist and are called, not written.

The projection is stated so it can be checked against, not so it can be argued around. If the developer's own estimate at implementation time exceeds ~1000 LOC, that is the signal to stop and route back, not to keep writing.

### 0.3 Scope cut from this design (do not add these back)

These were considered and are deliberately **not** in scope. Cutting them is what holds the size, so re-adding one is scope creep, not thoroughness.

- **No new offline self-check function for the widened `tdnRecord`.** The existing `TestTdnRecordWriter` (`:843`) is *extended in place* — its per-member redaction loop gains the new snapshot members, and `tdnFixtureRecord` populates them. No new `Test…` function for the record.
- **No second per-pid liveness read, no second argv matcher, no second FIFO-hold helper, no second descendant walk, no second artifact writer, no new prompt.**
- **No `docs/knowledge/codebase/1251.md`.** The knowledge doc is the documentation phase's deliverable, written from this spec plus the merged diff. The developer's worktree mutates code, tests, and this spec file only. AC5's "note" is discharged inside the rig — see § 3.8.
- **No follow-up-ticket-filing automation.** AC5's "files a follow-up ticket" on a leak is an operator action off the back of the artifact; the rig's job is to go red and print the pgid, the teardown path and the runner. Say so in the failure message.
- **No `-ww`-widened integer snapshot, no `reach.ps.txt` sibling file.** `writeTdnArtifacts` emits exactly one file and that is itself the redaction assertion (§ 6).

### 0.4 File-overlap check

`git fetch origin --prune` then a diff of every `origin/feature/<N>` against `origin/main`. Three branches touch `internal/e2e/realclaude/`:

- `origin/feature/1174` — `interactive_stream_new_session_test.go` (new file)
- `origin/feature/1240` — `interactive_background_idle_probe_test.go` (new file, `bgIdle*` prefix)
- `origin/feature/363` — `fixtures.go`

None touches `teardown_liveness_test.go`, and this ticket does not touch `fixtures.go`. **No overlap; no blocker added.** The `bgIdle*` prefix on `feature/1240` does not collide with `tdn*`; still, run one redeclare check before naming anything new (§ 3.1).

---

## 1. Files to read first

Read these before writing a line. This is the turn-1 data load; the design below assumes all of it.

| Path | What to extract |
|---|---|
| `internal/e2e/realclaude/teardown_liveness_test.go:76-374` | `tdnReapMessage`, the four reap verdicts, `tdnClassifyReapLog`, `tdnRecord` (`:328`), `writeTdnArtifacts` (`:363`). This is the file you edit. |
| `internal/e2e/realclaude/teardown_liveness_test.go:529-540` | The SHOULD-FIX row. Note that `tdnFixtureDefaultTwo` (`:392`) already carries `4242`, so the row's name is false — § 3.9. |
| `internal/e2e/realclaude/teardown_liveness_test.go:843-965` | `TestTdnRecordWriter`'s three subtests and `tdnFixtureRecord`; both need the widened shape. |
| `internal/agentrun/reap.go:35-67` | `:52`'s three exclusions, `:56-62` skipping `ESRCH` **before** the append (why a pgid in the line proves `kill(2)` succeeded), `:64`'s `len(reaped) > 0` emit guard (why `no-reap-line` is ambiguous), `:65` the line itself. |
| `internal/e2e/realclaude/background_reach_probe_test.go:320-453` | `runReachProbe` — the staging template. **Read `:355-356` as the anti-pattern**: it registers the pyry-exit cleanup *before* `holdProbeFIFO` so LIFO releases the FIFO first. Copying that inverts this ticket's measurement (§ 3.3). |
| `internal/e2e/realclaude/background_reach_probe_test.go:459-560` | `reachMeasure`'s ordering discipline and its establish-the-handle-before-reading-the-table rule. |
| `internal/e2e/realclaude/background_reach_probe_test.go:795-855` | `reachFinish` (the record-and-skip / summary-log shape to mirror) and `writeReachArtifacts` (which writes a **second** file — do **not** follow that half). |
| `internal/e2e/realclaude/background_reach_probe_test.go:859-882, 945-960, 1051-1090` | `reachScanArgv`'s `-ww` / no-`-E` discipline, `reachCapCommand`, `reachMatchedNeedle`, `reachBackgroundHandle`, `reachToolUseCommand`, `reachRunnerPathFromEnv`, `reachRunnerPathFromArgv` (**do not reuse the last one** — § 3.7). |
| `internal/e2e/realclaude/background_trigger_probe_test.go:106-180` | `probe*Deadline` constants, `probeMaxTurns`/`probeModel`/`probeEffort`, `probeSystemPrompt`, `probePrompt`. |
| `internal/e2e/realclaude/background_trigger_probe_test.go:616-816` | `spawnProbePyry` (`:621`), `holdProbeFIFO` (`:663`, and its `t.Cleanup` release contract at `:689-713`), `probeSyncBuffer` (`:722`), `probeWaitForSessionID` (`:746`), `probeWaitForBashToolUse` (`:762`, and its first-match defect), `probeWaitForToolResult` (`:793`), `probeToolUseInput` (`:827`). |
| `internal/e2e/realclaude/process_pin_liveness_test.go:120-197` | `pinScan` (`Matches` is a **slice**), `pinExclusion`, `pinMatchArgvExcluding`, `pinScanArgv`. |
| `internal/e2e/realclaude/process_pin_liveness_test.go:199-438` | The four verdicts, `pinStateColumns` and its `NEVER add command/args/comm` doc comment, `pinReadState`, `pinClassifyState`'s ten-branch order, `pinIsZombie`'s first-rune rule. |
| `internal/e2e/realclaude/fifo_reader_liveness_test.go:80-212` | `fifoLiveOutcome`, `fifoLiveRead`'s positive-allowlist mode gate, and why its transient second write end is safe only while `holdProbeFIFO`'s is held. |
| `cmd/pyry/agent_run.go:255-300, 364-378` | `signal.NotifyContext(SIGTERM, SIGINT)` at `:258` (SIGTERM to pyry's **pid** is what runs the real teardown), the `PYRY_USE_STREAMJSON` dispatch at `:266`, `runAgentRunPty` at `:299` passing **no** `Logger`, and `buildStreamRunnerClaudeArgs` at `:364`. |
| `internal/agentrun/ptyrunner/runner.go:616-625` | `buildArgs` — the ptyrunner argv. Compare against `buildStreamRunnerClaudeArgs` above; both emit `--append-system-prompt-file`, which is the fact § 3.7 turns on. |
| `docs/knowledge/codebase/1250.md` § Lessons learned | The one open SHOULD FIX (repaired here), the four NITs (not this ticket's), and the wrong-channel-stderr and cited-handler patterns. |

---

## 2. Context

`agentrun.ReapDescendantGroups` is pyry's defence against claude leaking Bash subprocesses. claude now moves a Bash command to the background when its timeout expires. #1230 established the predictive half (the backgrounded process is inside the reaper's BFS reach and its pgid survives all three exclusions); an operator measured the confirmatory half by hand on 2026-07-30, 3/3.

**The answer exists. This ticket is the rig that keeps it true.** The reaper is a four-ticket defence, the backgrounding behaviour is days old and claude-version-dependent, and the whole reading rests on a `cmd.Cancel` ordering that any teardown refactor could silently invert — with nothing going red.

**The live risk is a manufactured pass, not a missed leak.** The expected reading is published in a public comment and the pipeline cannot run the live half (no claude login; a skip exits 0). So the record must be a rig artifact, and the instrument must be shown to flip inside the very run whose verdict it reports.

Three realities produce "the process was dead after pyry exited", and only one means the reaper's reach is intact:

1. the reaper SIGKILLed its group,
2. the command finished on its own,
3. it died alongside claude rather than by the reaper's hand.

Both discriminators are structural, not prose. **(2) is ruled out by the still-held FIFO** — `cat` cannot reach EOF while the rig holds the write end. **(3) is ruled out by a pgid appearing in the reap line**, because `reap.go:56-62` skips `ESRCH` *before* the append, so membership in that list proves `kill(2)` succeeded.

**Runner-independence is an argument from the shared `reapDescendantGroupsFn` seam** (`ptyrunner/runner.go:314,398,499`; `streamrunner/runner.go:201`; `streamsup/runner.go:567` — all verified pointing at `agentrun.ReapDescendantGroups`), **not a measurement.** Label it as such wherever it is written up.

---

## 3. Design

### 3.1 Two files, one prefix

| File | Change |
|---|---|
| `internal/e2e/realclaude/teardown_liveness_test.go` | Widen `tdnRecord`, add `tdnSnapshot` + the disposition enum + `decide`, update `tdnFixtureRecord` and `TestTdnRecordWriter`, repair the SHOULD-FIX row. |
| `internal/e2e/realclaude/teardown_liveness_probe_test.go` | **New.** The live rig. |

Share the `tdn` prefix (#1250 shipped ~25 `tdn*` symbols). **Before naming anything, run one redeclare check** — `grep -rn 'func tdn\|type tdn\|const tdn\|tdn[A-Z][A-Za-z]* *=' internal/e2e/realclaude/`. `feature/1240` uses `bgIdle*` and does not collide.

**Test-function naming is load-bearing.** #1250's file header advertises the offline suite as `go test -tags e2e_realclaude -run '^TestTdn' -v ./internal/e2e/realclaude/`, and its knowledge doc records **"22 subtests, zero SKIP"** as a property of that command. A live, credential-gated test named `TestTdn…` would join that suite and add a SKIP to it, silently falsifying the claim. So:

- Live test: **`TestRealClaude_TeardownLiveness`** (mirrors `TestRealClaude_BackgroundReachability`).
- Offline self-checks in the new file: **`TestTdnRunnerFromArgv`**, **`TestTdnDecideAfter`** — pure, no credentials, no skip, and they legitimately belong to the `^TestTdn` suite.

### 3.2 What the record must hold — `tdnSnapshot` and the widened `tdnRecord`

`tdnRecord` as merged holds a single `Liveness`, a single `FIFO` and a single `ArgvScan`, and carries no provenance. AC2 needs both snapshots, AC3 needs the content match re-run after teardown, AC5 needs three provenance values. `Notes` is not the answer — AC2's flip recorded as a sentence is the prose this ticket exists to stop accepting.

Contract (fields and tags; the developer writes the doc comments in the file's idiom):

```go
// tdnSnapshot is one point-in-time reading of the pinned command: the content
// match, one per-pid liveness read for every pid pinned at the before-snapshot,
// and the FIFO's own reader state.
type tdnSnapshot struct {
	At       string            `json:"at"`            // tdnAtBefore | tdnAtAfter
	ArgvScan pinScan           `json:"argv_scan"`
	ScanErr  string            `json:"argv_scan_error,omitempty"`
	Liveness []pinStateOutcome `json:"liveness"`      // index-aligned with tdnRecord.HeldPIDs
	FIFO     fifoLiveOutcome   `json:"fifo"`
}
```

`tdnRecord` changes:

- **replace** `HeldPID int` with `HeldPIDs []int` (§ 3.4), keep `HeldPGID int`;
- **replace** `ArgvScan` / `Liveness` / `FIFO` with `Before *tdnSnapshot` and `After *tdnSnapshot` — replace, do not keep alongside: a member that no longer says *which* snapshot it holds is ambiguity in a published artifact;
- **add** `ClaudeVersion`, `TeardownPath`, `RunnerFromEnv`, `RunnerFromArgv` (four fields for AC5's three provenance values; the runner is split because the env read is documentation and the argv read is the evidence — § 3.7);
- **add** `Disposition` / `DispositionDetail` plus a `decide(verdict, format string, args ...any)` setter, mirroring `reachRecord.decide` (`background_reach_probe_test.go:261`);
- **keep** `Ticket`, `Notes`, `note`, `Reap`.

`Ticket` becomes `"1251"` in the rig's record; `tdnTicket` (`= "1250"`) stays as the offline instrument's own provenance and is **not** repurposed.

**The `#1250` composition contract still holds and must be restated in the doc comment:** the record *composes*, it does not *conclude*. `Disposition` is a field the rig **sets**; nothing inside the record derives it, and no pid/content join happens here. The join stays at the rig level (§ 3.5), which is the contract #1250 wrote down for both live consumers.

**Index alignment is an invariant, not a convention:** `len(Before.Liveness) == len(After.Liveness) == len(HeldPIDs)`, and entry *i* of each is about `HeldPIDs[i]`. Every `pinStateOutcome` carries its own `PID`, so the artifact is self-checking; assert the alignment in the rig rather than trusting it.

### 3.3 Staging, and the cleanup-ordering trap

Everything up to and including the after-snapshot happens **in the test body**. `holdProbeFIFO` releases only in the `t.Cleanup` it registers itself, which by construction runs after the body — so doing the SIGTERM, the wait and the after-snapshot in the body holds the FIFO across teardown for free. `holdProbeFIFO` needs no edit.

Body order (each step's failure routes to `tdnFinish` — § 3.6):

1. `WithWorktreeAuthenticated(t)` (skips without credentials before anything is created), `resolveClaudeBin(t)`, `probeClaudeVersion`.
2. Build `rec` with `Disposition = tdnDispositionSkipped` and a detail saying no claim is made. Seed the standing notes (§ 3.8).
3. **Register `t.Cleanup(func(){ writeTdnArtifacts(t, artifactDir, rec) })` FIRST** so LIFO runs it last — a structural `t.Fatalf` still leaves evidence on disk.
4. `rendezvous := holdProbeFIFO(t, fifoPath)`.
5. **Register the defence-in-depth group kill AFTER `holdProbeFIFO`.** This is the deliberate inversion of `runReachProbe:355-356`. LIFO then runs kill-then-release, which is harmless because the body has already taken every reading. The `if rec.PyryPID <= 0 { return }` guard is **load-bearing**: `syscall.Kill(-0, SIGKILL)` signals the caller's own group and would SIGKILL the test binary and its siblings.
6. Write `prompt.txt` / `system.txt`, `ensurePyryBuilt`, `spawnProbePyry` with `tdnEnvDelta = []string{"BASH_DEFAULT_TIMEOUT_MS=5000"}` (#1223's settled lever; `spawnProbePyry` already sets `Setpgid`). Record `rec.PyryPID`.
7. `go func(){ _ = cmd.Wait(); close(pyryExited) }()`. **Never `Signal(0)`** — pyry is a direct child and becomes a zombie between exit and `Wait`, and a zombie answers `Signal(0)` with nil.
8. `probeWaitForSessionID` → `probeWaitForBashToolUse` → guard the tool_use's `input.command` carries the run's FIFO path (`probeToolUseInput` + `reachToolUseCommand` + `strings.Contains`; keep only the boolean, do **not** edit the shared rig) → `probeWaitForToolResult` → `reachBackgroundHandle` must report a handle.
9. **Before-snapshot** (§ 3.4, § 3.5).
10. **Teardown:** `cmd.Process.Signal(syscall.SIGTERM)` — to the **pid**, never `Kill(-pid, …)` and never SIGKILL. Either would measure a leak pyry's real teardown never produces. `agent-run` installs `signal.NotifyContext(SIGTERM, SIGINT)` at `cmd/pyry/agent_run.go:258`, so this cancels the run context and runs the real teardown, reap included.
11. Wait on `pyryExited` with `probePyryExitGrace` (20 s, reused — no new constant). Timeout → record-and-skip: the teardown never completed, so no reading about it is available.
12. **After-snapshot.**
13. `rec.Reap = tdnClassifyReapLog(stderr.Bytes(), rec.HeldPGID)`.
14. `tdnDecideAfter(rec)` (§ 3.6), then `tdnFinish`.

Unlike `runReachProbe`, a missing session id is **record-and-skip, not `t.Fatalf`** — a `t.Fatalf` there discards the artifact-bearing path for a condition that is a staging miss, not a structural fault. Keep `t.Fatalf` only for: `mkfifo` failure (inside `holdProbeFIFO`), the prompt-file writes, `ensurePyryBuilt`, and `spawnProbePyry`'s own start failure.

### 3.4 Pinning identity: every matched row, exactly one pgid

`pinScanArgv` returns `Matches` as a **slice** and deliberately refuses to resolve "the" pid. #1230's live run matched **two** rows on the FIFO needle — the `zsh -c` wrapper claude runs Bash through, whose argv carries the whole command string, and the `cat` itself. A first-match resolve is the defect this family already flags.

Needles: `[]string{fifoPath, "--append-system-prompt-file"}`. Exclusions: `map[int]string{os.Getpid(): "the probe's own test binary", rec.PyryPID: "the pyry agent-run process the probe spawned"}`.

The two needles are **disjoint by construction**, verified against the argv builders:

- Neither runner puts the prompt on claude's argv — `ptyrunner.buildArgs` (`runner.go:616-625`) and `buildStreamRunnerClaudeArgs` (`agent_run.go:364-378`) both deliver it via `PromptBytes` (stdin / TUI). So the FIFO path reaches only the `zsh -c` wrapper and `cat`.
- pyry's own argv carries `--system-prompt-file=…`, which does **not** contain the string `--append-system-prompt-file`. Only claude's argv does.

So: rows carrying `fifoPath` (via `reachMatchedNeedle`) are the **held rows**; rows carrying `--append-system-prompt-file` are **claude**, used only for the runner label. Neither exclusion should ever fire — `pinPartition` records an exclusion only when it actually matched, so a firing entry in the artifact is itself the signal that a needle leaked into a process it should not have.

`tdnPinHeld(scan, fifoPath) (pids []int, pgid int, ok bool)`:

- zero held rows → not ok (AC2's "finds no row" void);
- collect the distinct pgids across held rows. **Exactly one** → that is `HeldPGID`. **More than one** → not ok: the reap classification has no single subject and the run cannot interpret its own staging. (Expected shape is one — claude isolates the whole Bash command into one detached group, and the hand run measured `count=1` on all three reps.)

### 3.5 The two snapshots, and the join rule that keeps a zombie from reading as pid reuse

`tdnSnapshotAt(at string, fifoPath string, pids []int) tdnSnapshot` takes one reading, in this order: `pinScanArgv` (record `Matches` and any error into `ScanErr` — **the scan's error and the liveness verdicts are separate values with no cross-assignment**, per #1235's gate-placement obligation), then `pinReadState(pid)` for each pid in `pids`, then `fifoLiveRead(fifoPath)`.

- **Before:** the scan comes first and *produces* `pids` via `tdnPinHeld`; then the per-pid reads and the FIFO read.
- **After:** the per-pid reads are over the pids pinned **before** — AC3's "the after-read is a pure liveness read on that pinned pid". The after scan is the **content re-match**, not a re-pin.

**AC2's flip gate.** Every before-liveness verdict must be `pinStateRunning`. Anything else — `dead`, a zombie, an instrument failure, or no row at all — **voids the run**: it is recorded as a staging or instrument fault and *never* as a clean result. This is what makes the known answer earnable: the expected reading is `dead`, so an instrument hard-wired to `dead` passes a live run while proving nothing.

`fifoLiveRead` is recorded at both points as **corroboration**, not as a gate. Deliberate: the leak verdict is decided by the liveness read and the content match only, so the FIFO state cannot invert it in either direction. A before-FIFO that is not `reader-present` while the before-liveness says `running` is a contradiction, and the rig records it with `rec.note(...)` rather than adding a branch. Under-gating here costs nothing; over-gating costs a reject branch.

**The join rule — the sharpest decision in this spec.** AC3 requires a recycled pid to be caught by re-running the content match. But a **zombie also fails the content match**: the kernel replaces a defunct process's argv (`<defunct>` on Linux, `(cat)` on macOS), so its row no longer carries the FIFO needle. A naive "after-liveness says alive-ish and the content match failed ⇒ pid reuse" rule would misfile every zombie. The rule is therefore scoped to the one verdict that can produce a finding:

> The after content re-match is dispositive **only when the after-liveness verdict is `pinStateRunning`**. For `pinStateExitedNotReaped` and `pinStateNoSuchProcess` the needle's absence is expected and carries no information — the needle was consumed at the before-snapshot and never has to survive into the after-read.

That is also why AC3's `<defunct>` command column is not a problem to solve: `stat` comes from the per-pid read (`pinStateOutcome.StateColumn`), `command` comes from the argv scan's matched rows (`pinScan.Matches[].Command`), and only the before-snapshot's scan is required to carry one.

### 3.6 Disposition — a positive allowlist with exactly one red arm

Four values, in the shared file next to the reap verdicts, with a `tdnIsDisposition` validator mirroring `tdnIsReapVerdict`:

| Value | When | Test outcome |
|---|---|---|
| `tdnDispositionReaperKilled` (`"dead-by-reaper"`) | preconditions held; **every** after-liveness verdict is `pinStateNoSuchProcess` or `pinStateExitedNotReaped`; `Reap.Verdict == tdnReapHeldPGIDKilled` | **pass**, full strength — reading (1). (2) is ruled out by the held FIFO, (3) by `reap.go:56-62` skipping `ESRCH` before the append. |
| `tdnDispositionDeadUnattributed` (`"dead-not-attributed-to-the-reaper"`) | as above, but `Reap.Verdict` is `tdnReapHeldPGIDAbsent` or `tdnReapNoLine` | **pass**, weaker reading — the command is dead and could not have finished on its own, but the reaper's hand is not established. `no-reap-line` is recorded as **ambiguous by construction** (`reap.go:64` guards the emit on `len(reaped) > 0`), never as "the reaper never ran". |
| `tdnDispositionLeaked` (`"leaked"`) | any after-liveness verdict is `pinStateRunning` **and** that pid's after content re-match still carries the run's FIFO path | **fail** — the only red arm. The `t.Errorf` names the leaked pgid, the teardown path and the runner, and states that a follow-up ticket is owed. |
| `tdnDispositionSkipped` (`"skipped"`) | everything else | **`t.Skip`** |

`tdnDispositionSkipped` covers, each with its own `DispositionDetail` sentence but through **one** helper: rendezvous timeout, no session id, no Bash `tool_use`, a `tool_use` that does not run the run's FIFO, no background handle, a before-snapshot that is not uniformly `running` (including zero matched rows and a multi-pgid match), a `ScanErr` at either snapshot, pyry not exiting within the deadline, `pinStateInstrumentFailed` at either snapshot, `Reap.Verdict == tdnReapInstrumentFailed`, and **`pinStateRunning` whose content re-match no longer carries the FIFO path** (pid reuse — AC3's mitigation, not a leak).

Two rules the allowlist encodes and the developer must not soften:

- **A broken instrument never opens a leak ticket.** Either classifier reporting `instrument-failed` records and skips. Without `tdnReapInstrumentFailed` as its own arm, an unparseable `pgids=` collapses into `reap-line-without-held-pgid`, which a consumer reads as "the reaper ran and did not kill our group" — a leak finding manufactured out of the instrument's own breakage.
- **A trigger miss is not a pyry regression.** Preconditions unmet ⇒ record and skip.

`tdnDecideAfter(rec *tdnRecord)` is **pure over `rec`**: no exec, no clock, no `*testing.T`. That is what makes it drivable by `TestTdnDecideAfter` offline (§ 5).

### 3.7 The runner label — and why `reachRunnerPathFromArgv` must **not** be reused

`reachRunnerPathFromArgv` (`background_reach_probe_test.go:1118-1124`) keys on `--append-system-prompt-file` and its comment calls that "the ptyrunner-shape marker". **That is wrong for a ticket that may run on either runner.** `buildStreamRunnerClaudeArgs` (`cmd/pyry/agent_run.go:372`) emits the identical flag, so the helper answers `"ptyrunner"` on the streamrunner path too. It is harmless in #1230, which skips outright when `PYRY_USE_STREAMJSON=1`; this ticket deliberately does **not** copy that gate (its rationale — content-first pinning on `--session-id` — does not transfer, and inheriting it would exclude the very path the operator measured). Reusing the helper here would mislabel the record on the stream path with nothing going red. #1236's Technical Notes propagated the same claim; do not inherit it either.

`tdnRunnerFromArgv(claudeCommand string) string` — pure over one string, four arms:

- contains `--session-id` and not `--input-format` → `"ptyrunner (claude argv carries --session-id)"` — only `ptyrunner.buildArgs` (`runner.go:618`) emits it;
- contains `--input-format` and not `--session-id` → `"streamrunner (claude argv carries --input-format)"` — only `buildStreamRunnerClaudeArgs` (`agent_run.go:366`) emits it;
- both, or neither → `"indeterminate (…)"`, naming what was and was not present.

Fed from the before-snapshot's claude row. Zero or several claude rows → `"indeterminate"`, not a skip: the runner label is provenance, not a precondition.

`reachRunnerPathFromEnv(tdnEnvDelta)` is reused verbatim for `RunnerFromEnv` and recorded as **documentation, not corroboration** — a standing `rec.note`, exactly as #1230's record carries. Its own comment records that `PYRY_USE_STREAMJSON` was already exported in the operator's shell on 2026-07-25 and silently invalidated a #1223 gate; that is the concrete reason the argv read is the evidential one.

`TeardownPath` is a constant string in the rig: `"operator SIGTERM to the pyry agent-run pid (signal.NotifyContext, cmd/pyry/agent_run.go:258) — not the budget-hit and not the watchdog teardown"`.

### 3.8 AC5's note — where it lives

Discharged inside the rig, seeded onto `rec` before any measurement so it survives even a skipped run. Three `rec.note(...)` calls plus the new file's package header:

1. the two structural discriminators — the still-held FIFO rules out reading (2); a pgid in the reap line proves `kill(2)` succeeded (`reap.go:56-62` skips `ESRCH` before the append) and so rules out reading (3);
2. the three things **not** measured — the terminal/PTY path *for teardown*, `internal/streamsup`'s daemon lifecycle, and that **runner-independence is an argument from the shared `reapDescendantGroupsFn` seam, not a measurement**;
3. that `RunnerFromEnv` is documentation and `RunnerFromArgv` is the evidence.

The knowledge doc (`docs/knowledge/codebase/1251.md`) is documentation's, not the developer's (§ 0.3).

### 3.9 The inherited SHOULD FIX — a one-token repair

`teardown_liveness_test.go:529-540`, the row named *"two reap lines, the held pgid only in the second"*, does not discriminate the mutation its name claims to guard. Its `stderr` is `tdnFixtureDefaultTwo + "\n" + tdnFixtureTextOne` with `held: 4242` — and `tdnFixtureDefaultTwo` (`:392`) renders `pgids="[89355 4242]"`, so `4242` is in the **first** line. A first-line-only union still returns `held-pgid-in-reap-line` and the row passes; only the `LineCount` / `Count` bookkeeping catches the truncation, not the verdict a consumer reads.

**Repair: swap the concatenation order** to `tdnFixtureTextOne + "\n" + tdnFixtureDefaultTwo`. Then line 1 contributes `{89355}` only and `4242` appears solely in line 2, so a first-line-only union yields `tdnReapHeldPGIDAbsent` and the row fails on the **verdict**. Every existing expectation is unchanged — `wantPGIDs` is still `[]int{89355, 4242}` (89355 from line 1, 4242 new in line 2; the duplicate 89355 is deduped by `seen`), `wantLines` is still 2, `wantCount` is still `1 + 2 = 3`. Update the row's comment to state the mutation it now catches. AC4's multi-group requirement rests on this arm, which is why it is repaired here rather than deferred a third time.

**Do not fold in #1235's deferred SHOULD FIX or its pid-reuse TOCTOU mitigation** — those are #1236's. (This ticket implements a pid-reuse *join* for its own reading, § 3.5; that is not the same obligation.)

---

## 4. Concurrency model

Three goroutines beyond the test's own, all pre-existing patterns:

| Goroutine | Started by | Exits when |
|---|---|---|
| `os/exec` stream copiers into `probeSyncBuffer` | `spawnProbePyry` | pyry's stdout/stderr close; joined by `cmd.Wait()`. `probeSyncBuffer` is mutex-guarded because the body reads it mid-run (`-race` clean). |
| the FIFO write-end holder | `holdProbeFIFO` | its own `t.Cleanup` closes `release`; bounded by `probeFIFOReleaseDeadline` (10 s), with the parked-in-`open()` case unblocked by a non-blocking read-end open. |
| the pyry reaper (`_ = cmd.Wait(); close(pyryExited)`) | the body, immediately after `spawnProbePyry` | `cmd.Wait()` returns. `close` is the single-writer signal; the body only ever receives. |

Shutdown sequence, deliberately ordered so the FIFO outlives the measurement:

```
body:  SIGTERM(pyry.pid) -> <-pyryExited -> after-snapshot -> classify -> decide -> writeTdnArtifacts -> t.Errorf/t.Skip
LIFO cleanups: kill pyry group (no-op)  ->  release FIFO write end (cat gets EOF and exits)
               ->  writeTdnArtifacts (registered first, runs last)
```

`writeTdnArtifacts` is called **twice** — explicitly in `tdnFinish` before any `t.Errorf`/`t.Skip` (AC5: artifacts precede failure), and again from the cleanup registered at step 3 (the net for a structural `t.Fatalf`). Same path, idempotent overwrite; the second write carries the completer record. `t.Skip` inside a test body runs `t.Cleanup`s normally, so the net holds on the skip path too.

No leaked orphan on a red run: `holdProbeFIFO`'s cleanup closes the last write end, `cat` sees EOF and exits on its own. The rig never SIGKILLs the held group itself — doing so would destroy the evidence of the very leak it just found.

---

## 5. Error handling and testing strategy

**Failure taxonomy.** Structural `t.Fatalf` only where no record could exist or be trusted: `mkfifo`, the two prompt-file writes, `ensurePyryBuilt`, `spawnProbePyry`'s start. Everything else is recorded — *a probe that turns an unexpected reading into a red test loses the reading.* Exactly one condition is red (`tdnDispositionLeaked`); everything ambiguous skips.

**Live verification** (operator, off-pipeline — the ticket carries `needs-real-claude` and a skip exits 0, so this must not close on a green that was a skip):

```
PYRY_PROBE_TEARDOWN_LIVENESS=1 go test -tags e2e_realclaude -timeout 10m -v \
  -run 'TestRealClaude_TeardownLiveness' ./internal/e2e/realclaude/
```

Expected: `dead-by-reaper`, before-liveness `running` / after-liveness `no-such-process` for every pinned pid, before-FIFO `reader-present`, reap `held-pgid-in-reap-line` with `count=1`. Run on **both** runners (add `PYRY_USE_STREAMJSON=1` for the second) and paste both artifacts; the record's `RunnerFromArgv` is what names which ran. The `t.Logf` summary must distinguish a real run from a skip at a glance — a `go test` exit code cannot.

**Offline coverage — two table-driven tests, no new instrument.** Both pure, both credential-free, both under `^TestTdn`:

- `TestTdnRunnerFromArgv`: a real ptyrunner-shaped argv → ptyrunner; a real streamrunner-shaped argv → streamrunner; an argv carrying **only** `--append-system-prompt-file` → indeterminate (this row is the regression guard against re-adopting `reachRunnerPathFromArgv`); an argv carrying both markers → indeterminate; empty → indeterminate.
- `TestTdnDecideAfter`: drive `tdnDecideAfter` over synthesised `tdnRecord`s — all-dead + `held-pgid-in-reap-line` → `dead-by-reaper`; all-dead + `no-reap-line` → `dead-not-attributed-to-the-reaper` with the ambiguity named in the detail; all-dead + `reap-line-without-held-pgid` → the same weaker arm; one pid `running` with the FIFO needle still in its after row → `leaked`; one pid `running` with the needle **gone** → `skipped` (pid reuse, never `leaked`); one pid `exited-but-not-yet-reaped` with the needle gone → **not** pid reuse, still a dead arm (this row is the § 3.5 join rule's guard); `pinStateInstrumentFailed` on either snapshot → `skipped`; `tdnReapInstrumentFailed` → `skipped`; a before-snapshot verdict that is not `running` → `skipped`.

**Extend, do not add, in `TestTdnRecordWriter`.** `tdnFixtureRecord` populates `Before` and `After` (composed from the real classifiers, not literals) and a `tdnIsDisposition`-valid `Disposition`; the redaction subtest's per-member loop points at `rec.Before.Liveness` / `rec.Before.FIFO` / `rec.After.Liveness` / `rec.After.FIFO` / `rec.Reap`, and the positive half reads `rec.Before.ArgvScan` for its `"command"` key. The round-trip subtest compares `round.Before.Liveness[0].Verdict` against `rec`'s. The exactly-one-file / `0600` subtest is unchanged.

**Gates.** `gofmt -l` on the two changed files (the package is dirty on `main` — filter to your own diff), `go vet -tags e2e_realclaude ./...`, `go test -tags e2e_realclaude -race -run '^TestTdn' -v ./internal/e2e/realclaude/`, and confirm that run reports **zero SKIP** (§ 3.1).

---

## 6. Redaction — inherited, not re-derived

- **Never** `ps -E`, `-e` with an environment column, or BSD `eww`. All print every process's environment, which on an operator machine means `CLAUDE_CODE_OAUTH_TOKEN` / `ANTHROPIC_API_KEY`, into an artifact destined for a public issue. The rig performs **no** environment read and must not gain the capability.
- Both `ps` call sites are inherited unchanged: `reachScanArgv`'s `ps -axww -o pid=,ppid=,pgid=,command=` (via `pinScanArgv`) and `pinStateArgs`' `ps -p <pid> -o pid=,ppid=,stat=`. **Do not widen `pinStateColumns`** — its doc comment forbids `command`/`args`/`comm` and `TestPinStateColumns_ReadsNoEnvironment` (`:950`) enforces it. `command` reaches the record only from the argv scan's already-matched rows.
- **No full-table command-column `ps` in the comment.** `writeTdnArtifacts` emits exactly one file by design, and "the writer writes exactly one file" is #1250's redaction assertion, checked by reading the directory. Do **not** follow `writeReachArtifacts`, which emits a second file holding a verbatim `ps` snapshot. The raw table never leaves `reachScanArgv`'s frame.
- Every recorded command string is capped through `reachCapCommand` before it lands in a `Detail` or a matched row.
- The artifact directory is `os.MkdirTemp("", "pyry-1251-probe-*")` — deliberately not `t.TempDir()`, which is removed when the test ends and the operator needs the file afterwards. Mode `0600`, inherited from `writeTdnArtifacts`.

---

## 7. Open questions

1. **Which runner does the operator run first?** Both are live-viable: #1230's rig passed on ptyrunner in 15.03 s (2026-07-30) and the hand run went 3/3 on streamrunner. The hand run's single ptyrunner attempt wedged before claude ran anything (PTY quiet 31 s, watchdog fired, zero turns) — one observation against a path that has since worked, not a reason to rule it out. The design is runner-agnostic and the record names which ran; resolve by running both.
2. **Does `probeWaitForSessionID` return promptly on the streamrunner path?** `parseInitSessionID` reads the `system/init` envelope off pyry's stdout, which both runners emit, and `jsonlPathFor` is runner-agnostic — so it should. If it does not, the run records-and-skips with `"no session id"` rather than losing the artifact, and that skip is itself the answer.
3. **`--max-turns=6`** (`spawnProbePyry`, `probeMaxTurns`) gives generous but finite headroom. A budget-fired run reaps *inside* the `Terminate` hook (`ptyrunner/runner.go:492-503`), before this rig's SIGTERM — so its reading would be about a different teardown path. Not gated (it needs a trailer read this ticket has no other use for); if the artifact shows a dead command with `no-reap-line`, check the turn count before reading anything into it. Left as an open question rather than a branch, on purpose.

---

## Security review

**Verdict:** PASS

**Findings:**

- **[Trust boundaries]** Three untrusted → trusted crossings, each already funnelled through exactly one pure classifier that the design forbids widening. (a) pyry's captured **stderr** → `tdnClassifyReapLog` (`teardown_liveness_test.go:144`), pure over bytes, membership over parsed integers, attribute keys bounded by `tdnAttrIndex`. (b) **`ps` stdout/stderr** → `pinClassifyState` (`process_pin_liveness_test.go:332`), whose ten-branch order is the contract that keeps every ambiguous input on `instrument-failed` and off `no-such-process`. (c) **claude's session JSONL and `tool_result`** → `probeToolUseInput` / `reachToolUseCommand` / `reachBackgroundHandle`, from which only derived booleans and short ids reach the record. Model-controlled bytes are never recorded verbatim. § 3.5's rule that the scan's error and the liveness verdicts are separate values with no cross-assignment keeps the boundaries from bleeding into each other.
- **[Tokens, secrets, credentials]** The dominant risk in this file family, and it is a **disclosure** risk, not a storage one: the artifact is pasted into a public GitHub issue and the operator's environment holds `CLAUDE_CODE_OAUTH_TOKEN` / `ANTHROPIC_API_KEY`. The spec generates and stores no secret. § 6 forbids `ps -E`, `-e` with an environment column, and BSD `eww`; forbids widening `pinStateColumns` (enforced by `TestPinStateColumns_ReadsNoEnvironment`); forbids a second artifact file; and forbids any full-table command-column `ps` in the comment. `TestTdnRecordWriter`'s marshalled-record scan for `environ` / `ps -E` / `-eww` / `eww` survives the widening because the per-member loop is extended to the new snapshot members (§ 5). No credential appears in a log, a `Detail`, or an error message.
- **[File operations]** Files created: `prompt.txt` and `system.txt` at `0600` inside the authenticated worktree; the FIFO at `0600` via `holdProbeFIFO`'s `syscall.Mkfifo`; `teardown.json` at `0600` via `writeTdnArtifacts`. Every path is rig-constructed from `WithWorktreeAuthenticated`'s directory and `os.MkdirTemp` — **no user-controlled component reaches a path**, so path traversal has no input to travel on. `fifoLiveRead` closes the symlink/TOCTOU question with a **positive allowlist**: `os.Lstat` (not `Stat`) plus `mode.Type() != os.ModeNamedPipe` rejects a symlinked or swapped path before the open, and a blacklist that merely excluded regular files would admit `/dev/null`, whose bare open succeeds with no reader anywhere. No atomic-rename need: `teardown.json` is a whole-file write of a marshalled buffer, and a truncated artifact is loud (invalid JSON) rather than silently partial. **SHOULD FIX (already in the design, restated for code review):** the artifact directory is `os.MkdirTemp`, which is `0700` — do not relocate it to a shared, world-readable path for convenience.
- **[Subprocess / external command execution]** Two `ps` invocations, both with fixed argument lists built by named functions (`pinStateArgs`, and `reachScanArgv`'s literal), never by string concatenation, and **never through a shell** — no `sh -c` anywhere in the design. The only integer reaching an argument position is a pid, and `pinReadState` rejects `pid <= 0` **before** the exec precisely because `strconv.Itoa(-1)` renders as `-1`, which `ps` would consume as a flag. `spawnProbePyry` passes pyry's own flags as separate argv elements. **Environment:** inherited plus a one-variable delta (`BASH_DEFAULT_TIMEOUT_MS=5000`), which is the whole plumbing story #1223 settled — no scrubbing is attempted and none is claimed. **Signals:** SIGTERM to pyry's **pid**, never its group and never SIGKILL (§ 3.3 step 10), with the defence-in-depth group kill guarded by `rec.PyryPID <= 0` because `syscall.Kill(-0, SIGKILL)` signals the *caller's* own group — one line, whole-run blast radius. Double-fork escape is not a hazard to defend against here; it is the **subject of the measurement**, and the rig deliberately does not kill the held group itself (§ 4) so a real leak survives into the evidence.
- **[Cryptographic primitives]** Not applicable, and the design decision that makes it so is that the run's unique identifier is the **FIFO path** under an `os.MkdirTemp`-derived worktree — uniqueness from the OS's own directory allocation, not from an RNG this spec would have to justify. No `math/rand`, no `crypto/rand`, no comparison against a secret, so no constant-time-compare requirement.
- **[Network & I/O]** No sockets, no listeners, no HTTP, no TLS. The unbounded-input surfaces are pyry's captured stdout/stderr and claude's session JSONL, both of which are bounded in practice by `--max-turns=6` and by every deadline in § 3.3 being finite (`probeRendezvousDeadline` 60 s, `probeToolUseDeadline` 30 s, `probeToolResultDeadline` 45 s, `probePyryExitGrace` 20 s, `reachPSTimeout` 5 s, `probeFIFOReleaseDeadline` 10 s) plus `go test -timeout 10m`. **SHOULD FIX:** `probeSyncBuffer` has no size cap, so a pathologically chatty pyry could grow it without bound before a deadline fires. Recoverable downstream and not exploitable by a hostile actor in a test-only rig on the operator's own machine; noted so code review can weigh it rather than discover it.
- **[Error messages, logs, telemetry]** Every string that can reach the published artifact passes through `reachCapCommand` — not cosmetic, since a reap line can carry an unbounded pgid list and a matched row an unbounded argv. Recorded fields are pids, pgids, `stat` columns, errnos and short verdict strings. The model's verbatim Bash params are **not** recorded (only the derived FIFO-match boolean), and the `tool_result`'s text is not pasted unexamined. No telemetry, no metrics, no user-identifiable aggregation.
- **[Concurrency]** No locks are taken by this design beyond `probeSyncBuffer`'s single internal mutex, so lock ordering cannot be inconsistent. Every goroutine has a named exit condition and a bounded deadline (§ 4). The one check-then-use window is the two `ps` calls at each snapshot; what bounds it is stated rather than assumed — the held command cannot exit while the FIFO write end is held, and only pids matched by a run-unique needle are ever used, with the after-read's pid-reuse case caught by the § 3.5 join rule rather than by hope. Shutdown mid-write leaves at worst a truncated `teardown.json`, which fails `json.Unmarshal` loudly.
- **[Threat model alignment]** `docs/protocol-mobile.md` § Security model governs relay tickets; this ticket opens no network surface and is out of its scope. The CLI-relevant threat this design *does* sit inside is the one this file family has repeatedly named: **an artifact destined for a public issue leaking the operator's environment**, addressed under [Tokens] and § 6. Explicitly **out of scope** and named for their owners: `internal/streamsup`'s daemon lifecycle and the terminal/PTY path *for teardown* remain unmeasured (recorded in the rig's own notes, § 3.8); #1235's deferred SHOULD FIX and its pid-reuse TOCTOU mitigation belong to **#1236**, not here.

**Reviewer:** architect (self-review per `architect/security-review.md`)
**Date:** 2026-07-31
