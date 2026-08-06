# 1340 — Probe rig: the live staging driver for one `pyry agent-run` turn and its handle

**Ticket:** [#1340](https://github.com/pyrycode/pyrycode/issues/1340) · **Size:** S · **Blockers:** #1338, #1342, #1343 (all merged at `3e6df75`)

**Deliverable:** one new file, `internal/e2e/realclaude/finding_live_run_test.go`, under `//go:build e2e_realclaude`. It declares one handle type and one driver function. **It ships no test.** Its only exercise is compilation and `go test`'s vet subset under `make e2e-realclaude`; the live consumer is #1337.

---

## Files to read first

Read these before writing a line. Each entry says what to extract; none of them needs to be re-derived.

**The three blockers — the parts the driver consumes and must not rebuild**

- `internal/e2e/realclaude/finding_live_pin_test.go:97-214` — `finLivePinWantRows = 2` (`:140`) and `finLivePinReduce(scan pinScan, fifoPath string) finLivePinReading` (`:202`). Read the two PROHIBITION paragraphs (`:125-139`) — they name the two wrong fills for `PinWantCount` and the cost of each.
- `internal/e2e/realclaude/finding_live_pin_test.go:63-99` — `finLivePinReading`'s four fields and its "INPUT ONLY — NEVER PUBLISHED / no json tags" posture. The handle inherits that posture; see § Design.
- `internal/e2e/realclaude/finding_live_staging_test.go:86-206` — `finLiveStageFIFOName` (`:100`), `finLiveStageSystemPrompt` (`:114`), `finLiveStageCommand` (`:135`), `finLiveStagePrompt` (`:158`), `finLiveStageEnvDelta()` (`:189`). Note `:204-205`: the live FIFO path is "#1340 joins `finLiveStageFIFOName` onto its own `t.TempDir()`" — that is this spec's § Design step 3, already written down by the blocker.
- `internal/e2e/realclaude/finding_live_assembly_test.go:196-290` — `finLiveAssembleFacts`' five fields and `finLiveAssembleStaging`'s signature + doc. Read `:259-264` (the "if #1340 needs more, that is #1340's argument to make" paragraph) — this spec's § What the handle deliberately does not carry answers it.
- `internal/e2e/realclaude/finding_staging_fill_test.go:234-264` — `finTranscriptFill`. **Extract one fact:** its Bash selection is content-first over the staged command (`finTranscriptSelectBash`, `:255`), which is why the driver's own waiters are a *timing* wait and never a *selection*.
- `internal/e2e/realclaude/finding_staging_gate_test.go:290-366` — the gate's arm ORDER: identity → trigger → rendezvous → pin-scan-errored → count → pass-through. The driver's straight-line body (§ Design) is only correct because of this ranking; read it before deciding to add an early return.

**The two nearest precedents — copy their shape, do not re-invent it**

- `internal/e2e/realclaude/background_reach_probe_test.go:320-453` — `runReachProbe`, the closest analogue end to end. **`:355-374` is the cleanup block to copy verbatim** (registration order, the `PyryPID <= 0` guard with its `// LOAD-BEARING` comment, the grace `select`). `:392-399` is the `cmd.Wait` goroutine and the zombie/`Signal(0)` reason.
- `internal/e2e/realclaude/background_trigger_probe_test.go:438-453` — the same guard **without** the comment. Named here so you can see the one you are NOT copying from; the reach copy's comment cites this line.
- `internal/e2e/realclaude/teardown_liveness_probe_test.go:372-387` — the one-scan-two-needles pin with its exclusion map and per-entry reasons. Copy the reason strings.

**The shipped helpers the driver calls (signatures + contracts only)**

- `internal/e2e/realclaude/background_trigger_probe_test.go:116-148` — the timing constants (`probeClaudeChildDeadline` 25s, `probeSessionIDDeadline` 20s, `probeRendezvousDeadline` 60s, `probeToolUseDeadline` 30s, `probeToolResultDeadline` 45s, `probePyryExitGrace` 20s) and `probeMaxTurns = "6"`.
- `internal/e2e/realclaude/background_trigger_probe_test.go:606-745` — `probeClaudeVersion`, `spawnProbePyry`, `holdProbeFIFO`, `probeSyncBuffer`. Note `holdProbeFIFO`'s `:652-655`: the write end never leaves the helper and releases only in the `t.Cleanup` it registers itself.
- `internal/e2e/realclaude/background_trigger_probe_test.go:759-816` — `probeWaitForBashToolUse` (returns the FIRST Bash tool_use regardless of command — the shipped #1223 gap) and `probeWaitForToolResult` (**matches on `ToolUseID` equality, so an empty id would poll to expiry for nothing** — this is why § Design step 12 guards).
- `internal/e2e/realclaude/background_trigger_probe_test.go:973-988` — `probeWaitForDirectChild(root, timeout) int`, 0 on timeout. Its internal snapshot is the narrow `ps -axo pid=,ppid=,pgid=` descendant walk (`:870`) and the driver discards it — only the int is returned.
- `internal/e2e/realclaude/process_pin_liveness_test.go:120-197` — `pinScan`, `pinPartition` (records an exclusion only when it fired, `:146-148`), `pinScanArgv(needles, exclude) (pinScan, error)`; the zero `pinScan` on error at `:194`.
- `internal/e2e/realclaude/background_reach_probe_test.go:162-168, 869-960` — `reachProc`, `reachScanArgv`'s exact `ps -axww -o pid=,ppid=,pgid=,command=` (`:876`, **no `-E`**), `reachMatchArgvRows`' any-needle rule (`:911-916`), `reachCapCommand` (`:945`), `reachMatchedNeedle` (`:955`).
- `internal/e2e/realclaude/teardown_liveness_probe_test.go:150-161` — `tdnClaudeNeedle = "--append-system-prompt-file"` and its disjointness-from-the-FIFO-needle argument. `:560-574` — `tdnClaudeCommand` returns `""` on `n != 1`.
- `internal/e2e/realclaude/fixtures.go:96-107` — `WithWorktreeAuthenticated`: skips (not fails) when neither credential variable is set, and calls `t.Setenv` (so **no `t.Parallel()` anywhere on this path**). `:325` — `ensurePyryBuilt`.
- `internal/e2e/realclaude/resilience_test.go:282-296` — `resolveClaudeBin`.
- `internal/e2e/realclaude/finding_run_gather_test.go:239-270` — `finGatherInputs`' six fields. This is the downstream consumer the handle is sized against; read `:250-251` (`Pinned` is `[]int` and never `[]reachProc`) and `:256-269` (`ClaudeState`'s admissible producer).

**Production facts the design rests on (read, do not change)**

- `internal/agentrun/ptyrunner/runner.go:229-243` (package doc) and `:479-485` (the in-function defer-LIFO comment) — `emitter.Close()` writes the trailer BEFORE `sess.Close()`'s SIGTERM. Claude is alive and unsignalled at trailer time. The two comments differ in detail (the in-function one additionally names the second `cancel()` and `[reap]`); both agree on the trailer-before-SIGTERM ordering, which is the only part this driver depends on.
- `internal/agentrun/ptyrunner/runner.go:398` — the descendant-reap defer, ordered before `sess.Close`. `internal/agentrun/reap.go:75-76` — it costs one `ps` exec. Together: the window between the trailer's write and the reap is roughly one exec, which is why the pin is taken **during** the turn.
- `internal/agentrun/ptyrunner/runner.go:492-503` — the budget `Terminate` hook reaps INSIDE the hook, before the trailer. `:600-601` / `:606` — `Run` returns `nil` on a cancelled run context exactly as on normal completion. `cmd/pyry/agent_run.go:271-277` — only a non-nil, non-`context.Canceled` error becomes a non-zero exit. Together: **the exit code cannot separate the outcomes, so this driver stages no budget-fired run.**

---

## Context

A live probe of `pyry agent-run` splits cleanly in two. The **staging** half — did the model issue the command we asked for, did the auto-background trigger fire, did the FIFO rendezvous complete, did the during-turn process pin match the expected rows — is deterministic, and is where every observed failure of this rig family has occurred. The **reading** half — what pyry's trailer says, and whether the held command was alive when it was written — needs a live claude and cannot be established any other way.

This ticket is the driver for the staging half, and only that half. It spawns pyry, drives the turn to the point where a process pin is meaningful, takes the pin, hands the pin and the rig's own facts to the shipped assembly, and returns a handle carrying the run's live facts and the staging tier's verdict. #1337 takes that handle and spends the remaining wall clock of the same live turn on the reading.

Everything a fixture can reach already shipped, in three blockers merged at `3e6df75`:

| From | Identifier | What it gives the driver |
|---|---|---|
| #1338 | `finLivePinReduce` | the whole pin reduction: `Rows`, `RowCount`, `PGIDs`, `ClaudeCommand` |
| #1338 | `finLivePinWantRows = 2` | the expected row count, forwarded as `PinWantCount` |
| #1342 | `finLiveStageFIFOName`, `finLiveStageCommand`, `finLiveStagePrompt`, `finLiveStageEnvDelta`, `finLiveStageSystemPrompt` | the run's FIFO name, staged command, prompt, env delta, system prompt |
| #1343 | `finLiveAssembleFacts` | the five caller-side fields the driver fills |
| #1343 | `finLiveAssembleStaging` | fills all eight fields and returns the gate's decision **as returned** |

**None of these is re-derived here.** The driver's whole content is the part that genuinely needs a live process: spawn, hold, wait, pin, assemble, hand back.

---

## Design

### The handle

One file-local type, `finLiveRunHandle`, **returned as a pointer**. The pointer is not a style preference: the pyry-exit kill cleanup must be registered *before* `holdProbeFIFO` (§ Cleanups), which is before pyry's pid exists, so the cleanup closure has to read a pid written later. That is exactly the shape the reach precedent uses with `rec.PyryPID` (`background_reach_probe_test.go:364`), and it is the reason the handle is allocated as the driver's first statement rather than composed at the end.

Contract sketch — field set and types only; the developer writes the per-field doc comments:

```go
type finLiveRunHandle struct {
	PyryPID       int                  // the spawned `pyry agent-run` process
	ClaudePID     int                  // pyry's direct child, from probeWaitForDirectChild
	PyryExited    <-chan struct{}      // closed by the cmd.Wait goroutine; RECEIVE-ONLY
	Stdout        *probeSyncBuffer     // live buffer, NOT a []byte snapshot
	Stderr        *probeSyncBuffer     // live buffer, NOT a []byte snapshot
	FIFOPath      string               // workdir/finLiveStageFIFOName
	EnvDelta      []string             // exactly what was handed to spawnProbePyry
	ClaudeVersion string               // probeClaudeVersion's best-effort read
	Pin           finLivePinReading    // #1338's reduction, WHOLE and unaltered
	Staging       finOutcomeResult     // finLiveAssembleStaging's return, AS RETURNED
}
```

Ten fields covering AC1's eleven items — `Pin` supplies three of them (matched rows, pgid set, claude argv) plus the row count.

**`NO JSON TAGS`, and the rule is inherited rather than invented.** `Pin.Rows[i].Command` and `Pin.ClaudeCommand` are verbatim argv read off the *ambient* process table — any local process's command line, not just this run's. `finLivePinReading` (`finding_live_pin_test.go:66-74`) and `finOutcomeStaging` (`finding_staging_gate_test.go:142`) both carry the same prohibition for the same reason. Adding tags "for symmetry" is the first step toward a published record quoting captured bytes into a public issue.

**`Pin` crosses whole, as `finLivePinReading`.** Not flattened into three fields. Flattening would re-declare four values that already have one declaration, drop `RowCount`, and invite a consumer to recompute it as `len(Rows)` — a second producer for a number `pinPartition` and `finLivePinReduce` each assign exactly once. AC1's "neither is re-derived here" is discharged structurally by handing the reduction's own type across.

**`Stdout` and `Stderr` are `*probeSyncBuffer` and never `[]byte`.** This is load-bearing, not convenience: pyry's reap log lands on stderr at teardown, and the trailer lands on stdout at `emitter.Close()` — **both after this driver returns.** A `[]byte` on the handle would be a snapshot taken strictly too early, and #1337's `finGatherInputs.Stderr []byte` would be filled from a buffer that had not yet seen the bytes it exists to read. The consumer calls `.Bytes()` (which returns a copy, `background_trigger_probe_test.go:736-742`) at its own reading point.

**`PyryExited` is `<-chan struct{}`.** Receive-only, so a consumer cannot close a channel the driver's goroutine owns. Its zero (a nil channel) blocks forever rather than reading as "exited", which is the safe direction.

### What the handle deliberately does not carry

#1343's doc left one question open and this is the answer (`finding_live_assembly_test.go:259-264`): **#1340 does not need more.** `finLiveAssembleStaging`'s return is not widened, and the eight-field record is not rebuilt here.

- **No `finOutcomeStaging` record.** Its own doc says "INPUT ONLY — NEVER PUBLISHED". The verdict crosses; the record does not.
- **No workdir and no session id.** Both are consumed *inside* the driver, by the assembly. `finGatherInputs`' six fields need neither, and the ticket states nothing further is owed downstream.
- **No staged command field.** It is not lost, it is *derived*: `finLiveStageCommand(h.FIFOPath)` is a pure function of a field the handle already carries, from the one source the driver itself used. A second copy on the handle would be a second declaration of a captured string with no consumer.
- **No tool_use id, no raw tool_result bytes, no raw `pinScan`.** Model-controlled bytes and the pre-reduction scan; the driver has no downstream use for any of them, and `pinScan` carries every ambient match rather than only this run's.
- **No artifact and no artifact dir.** #1337 owns the record and the artifact. This driver writes nothing to disk except the two prompt files pyry itself reads (§ step 6).

If #1337 needs one of these, that is #1337's argument to make — the same posture #1343 took toward this ticket.

### The driver

```go
func finLiveRunStage(t *testing.T) *finLiveRunHandle
```

No parameters beyond `t`: the driver establishes its own credentialed worktree, and there is no configuration a caller could vary that would still be this run. It is `t.Helper()`-marked and **may not be called from a parallel test** — `WithWorktreeAuthenticated` calls `t.Setenv`, and Go's runtime refuses that pairing.

The body is **straight-line**: no early return, no disposition of its own, no failure arm. Steps, in order — the order is the contract:

1. `workdir := WithWorktreeAuthenticated(t)` — **first**, so a machine without credentials *skips* before anything is created, and so `HOME` is repointed before any path is derived from it.
2. `claudeBin := resolveClaudeBin(t)`; allocate the handle with `ClaudeVersion: probeClaudeVersion(claudeBin)` and `EnvDelta: finLiveStageEnvDelta()`.
3. `fifoPath := filepath.Join(workdir, finLiveStageFIFOName)` → `h.FIFOPath`. This is the join #1342 named at `finding_live_staging_test.go:204-205`.
4. Declare `var stdout, stderr probeSyncBuffer` and set `h.Stdout` / `h.Stderr`, and `pyryExited := make(chan struct{})` → `h.PyryExited`. All three exist before any cleanup is registered so the handle's literal is complete except for the four progressively-filled fields (`PyryPID`, `ClaudePID`, `Pin`, `Staging`).
5. **Register the pyry-exit kill cleanup** — see § Cleanups. It must precede step 6.
6. `holdProbeFIFO(t, fifoPath)` → the rendezvous channel. **Strictly after step 5.**
7. Write `prompt.txt` (`finLiveStagePrompt(fifoPath)`) and `system.txt` (`finLiveStageSystemPrompt`) into `workdir` at mode `0o600`, following `background_reach_probe_test.go:378-385` exactly.
8. `bin := ensurePyryBuilt(t)`; `cmd := spawnProbePyry(t, bin, workdir, promptPath, systemPath, h.EnvDelta, &stdout, &stderr)`; `h.PyryPID = cmd.Process.Pid`.
9. Start the `cmd.Wait` goroutine that closes `pyryExited` — see § Concurrency.
10. `h.ClaudePID = probeWaitForDirectChild(h.PyryPID, probeClaudeChildDeadline)`; **`t.Fatalf` if 0** (§ Error handling).
11. `sessionID := probeWaitForSessionID(&stdout, probeSessionIDDeadline)`; **`t.Fatalf` if `""`** (§ Error handling).
12. `select` on the rendezvous against `probeRendezvousDeadline` → a local `rendezvousDone bool`. **Not fatal, no early return.**
13. `toolUseID, _ := probeWaitForBashToolUse(t, workdir, sessionID, probeToolUseDeadline)`. The raw envelope is discarded — see § The waits are timing, not selection.
14. If `toolUseID != ""`, `probeWaitForToolResult(t, workdir, sessionID, toolUseID, probeToolResultDeadline)` and **discard its return.** The guard exists because that waiter matches on `ToolUseID` equality (`background_trigger_probe_test.go:806`): an empty id would poll a full 45 s for a match it cannot make. The return is discarded because every question it could answer is one the assembly answers content-first from the same transcript.
15. **Take the pin** — one scan, both needles, two exclusions (§ The pin).
16. `h.Pin = finLivePinReduce(scan, fifoPath)`.
17. `h.Staging = finLiveAssembleStaging(t, workdir, sessionID, finLiveAssembleFacts{...}, probeToolUseDeadline, probeToolResultDeadline)`; `return h`.

**Why straight-line.** Every staging failure the driver could branch on already has a named home in the gate, and the gate is *rank-ordered* so the driver need not decide which one to report (`finding_staging_gate_test.go:290-366`: identity outranks trigger outranks rendezvous outranks pin-scan-errored outranks count). An early return on a rendezvous miss would save at most one deadline of wall clock on a run that has already lost a live turn, at the cost of a path that can only ever be exercised live. The two `t.Fatalf`s in steps 10–11 are the exception and § Error handling states why they are not gate-shaped.

### The waits are timing, not selection

`finTranscriptFill` selects its Bash call **content-first** against the staged command (`finding_staging_fill_test.go:255`, via `finTranscriptSelectBash`). So the driver's steps 13–14 decide nothing; they exist solely to place the pin at an instant when the pin is meaningful.

**The driver takes those waits itself, and skipping them is the failure this AC exists to prevent.** `finLiveAssembleStaging` also waits internally — but it takes the pin counts as *inputs*, so its wait happens strictly after the pin. A driver reasoning "the assembly does the waiting" pins before the `cat` exists, matches 0 or 1 rows, and fires the gate's count arm reporting `stage-pin-count-unexpected` on a correctly staged run, with no other symptom and one live claude turn spent finding out.

The assembly's second read of the transcript is **expected and is not a defect**: the file is on disk by then, so both of its waiters return on their first poll. That is the case its deadline parameters exist for (`finding_live_assembly_test.go:240-244`), and the driver passes those same live deadlines through.

**Named limitation, accepted.** `probeWaitForBashToolUse` returns the FIRST Bash tool_use regardless of `input.command` — a deliberately-shipped #1223 gap. If the model issues some other Bash call first, the driver's timing keys off that envelope and the pin may land early. Both precedents guard against this with a content-first `strings.Contains` check because both *decide a disposition* from it; **this driver decides nothing, so it adds no guard.** The consequence of the untimed case is bounded and safe: the assembly still selects content-first, so the run reports either `stage-command-not-staged` (the staged call never came) or `stage-pin-count-unexpected` (it came after the pin) — both non-verdict outcomes, never a false `ready-to-classify`. Growing a second content-first waiter is the shared-rig edit both precedents explicitly declined to make, and it is not made here.

### The pin

Taken at step 15 — **after the `tool_result` has fired and while the FIFO write end is still held**, never after the trailer is observed.

**Why during and not after.** A rig sees the trailer only when it polls pyry's stdout, up to one `probePollInterval` (200 ms) after `emitter.Close()` writes it. The descendant reap at `runner.go:398` starts effectively immediately after that write and finishes in the time of one `ps` exec (`reap.go:75-76`). A scan taken after the observed trailer matches nothing on a healthy run. Pinning while the `tool_result` has fired and the FIFO is still held is guaranteed to match, because `cat` is blocked in `read()` until EOF and the write end is held by `holdProbeFIFO`, whose only release is in the `t.Cleanup` it registers itself — which by construction runs after the caller's body.

**One scan, both needles.**

- Needles: `[]string{fifoPath, tdnClaudeNeedle}` — the shipped precedent at `teardown_liveness_probe_test.go:377`. It saves a second `ps`: `reachMatchArgvRows` matches a row carrying **any** needle (`background_reach_probe_test.go:911-916`), so one scan yields both populations, and `finLivePinReduce` separates them (`reachMatchedNeedle` for the FIFO rows, `tdnClaudeCommand` over the whole scan for claude's).
- Exclusions: `map[int]string{os.Getpid(): "the rig's own test binary", h.PyryPID: "the `pyry agent-run` process the rig spawned"}` — reasons copied from `teardown_liveness_probe_test.go:378-381`. **Neither should ever fire.** `pinPartition` records an exclusion only when it actually matched (`process_pin_liveness_test.go:146-148`), so an entry appearing in a record is itself the signal that a needle leaked into a process it should not have reached.
- Call: `pinScanArgv(needles, exclude)`. Its error sets `PinScanErrored`; the scan value is used regardless (it is the zero `pinScan` on error, `process_pin_liveness_test.go:194`, which reduces to a zero reading).

**The scan is handed to `finLivePinReduce` unchanged.** The driver does not filter, count or dedupe it. And it forwards `finLivePinWantRows` as the want — **never `scan.MatchCount`** (3 on a healthy run: claude's own row rides along on the second needle) and **never `len(h.Pin.PGIDs)`** (1 on a healthy run: claude isolates the whole Bash command into one detached group). Both wrong fills are argued at `finding_live_pin_test.go:125-139`, and both have the same consequence — the gate's count arm fires against a want of 2 on a correctly staged run.

**The facts handed to the assembly** (`finLiveAssembleFacts`, all five keys present, no literal on any right-hand side except the shipped constant):

| Field | Value | Source |
|---|---|---|
| `StagedCommand` | `finLiveStageCommand(fifoPath)` | #1342 — the same call the prompt was built from |
| `RendezvousDone` | step 12's local | this driver |
| `PinScanErrored` | `err != nil` from `pinScanArgv` | this driver |
| `PinMatchCount` | `h.Pin.RowCount` | #1338's reduction |
| `PinWantCount` | `finLivePinWantRows` | #1338's constant |

`h.Staging` is `finLiveAssembleStaging`'s return **as returned** — not re-derived, not renamed, not cross-checked into a new verdict, not inspected to choose a different code path. The assembly already guarantees that property in its own body; the driver's obligation is to add nothing to it.

### Turn headroom, and no budget-fired run

`spawnProbePyry` already passes `--max-turns=probeMaxTurns` (`"6"`), which leaves room for the turn to **complete** — claude receives the `tool_result` and replies — not merely to reach the Bash call. **No new spawn helper is added:** `spawnProbePyry` registers no `t.Cleanup` of its own, starts pyry, and returns the `*exec.Cmd`, which is exactly this driver's requirement.

Staging a budget-fired run is prohibited for two independent reasons, both production facts:

1. **The exit code cannot separate the outcomes.** The budget's `Terminate` hook cancels the run context (`runner.go:502`); `Run` returns `nil` on a cancelled run context (`:600-601`) exactly as on normal completion (`:606`); and `runAgentRun` maps only a non-nil, non-`context.Canceled` error to a non-zero exit (`cmd/pyry/agent_run.go:271-277`). The discriminator is the trailer, not the exit status.
2. **The budget path cannot answer the downstream question at all.** Its hook reaps *inside* the hook, before the trailer is written (`runner.go:492-503`, the reap at `:499`).

`PYRY_USE_STREAMJSON=0` is in #1342's env delta by name (`finding_live_staging_test.go:174-179`), and `spawnProbePyry` appends the delta to `os.Environ()`, so the delta wins over an ambient setting. **No `PYRY_USE_STREAMJSON` skip guard is needed here** — the reach probe's guard (`background_reach_probe_test.go:296`) exists because its delta does not name the variable.

---

## Concurrency model

Two goroutines are in play and neither is this file's invention.

**The `cmd.Wait` goroutine (step 9).** Runs `_ = cmd.Wait()` then `close(pyryExited)`. Copied from `background_reach_probe_test.go:396-399`, and **its reason must be carried in the comment** (`:392-395`): pyry is a direct child of the test process, so between exit and `Wait` it is a zombie — and a zombie answers `Signal(0)` with nil. A liveness probe would therefore report an already-returned pyry as still running, falsifying the during-turn claim. The channel is the signal; a signal-0 probe is not.

Lifecycle: the goroutine exits when `cmd.Wait` returns, which happens when pyry exits and its stdout/stderr copiers finish. Both cleanups guarantee that terminates — the FIFO release lets the turn finish, and the SIGKILL after `probePyryExitGrace` is the net under it. Claude runs on a PTY, so the held `cat` does not inherit pyry's stdout/stderr pipe write ends and cannot hold `Wait` open. No leak.

**`holdProbeFIFO`'s hold goroutine.** Owned entirely by the shipped helper; the write end never leaves it. Its shutdown is the helper's own `t.Cleanup`.

**The driver's own body never reads `pyryExited` and never releases the hold early.** The channel crosses the handle unread so the consumer can take that wait itself, with the hold still held — which is the whole reason #1337 can observe the trailer at all. This does **not** strip the grace `select` out of the kill cleanup; see § Cleanups.

**Shared-state discipline.** `h` is written only by the driver's body. The kill cleanup reads `h.PyryPID`, but `t.Cleanup` functions run after the test body returns, so there is no concurrent access and nothing to lock. `stdout`/`stderr` are written by os/exec's copier goroutines while the body and #1337 read them — which is precisely why `probeSyncBuffer` exists and why a plain `bytes.Buffer` would be a `-race` failure.

### Cleanups — the registration order is the contract

Registered in this order, so LIFO runs them in reverse:

1. **The pyry-exit kill cleanup** (step 5)
2. `holdProbeFIFO`'s own cleanup (step 6, registered by the helper)

LIFO therefore **releases the FIFO first**, giving pyry a real chance to finish the turn and exit on its own, and makes the kill defence-in-depth rather than the thing that produced the exit.

**This is called out rather than left implicit because inverting it has no symptom.** Registering the kill *after* the hold, so LIFO kills before releasing, yields a clean-looking run: green, handle populated, and the downstream exit reading silently measuring the rig instead of pyry. Both nearest precedents already do this and both say so in a comment (`background_reach_probe_test.go:355-374`, `background_trigger_probe_test.go:438-453`). **Copy that order and copy the comment.**

The cleanup body, in order:

- **The `h.PyryPID <= 0` guard, verbatim, with the reach copy's comment** (`background_reach_probe_test.go:359-366`). The trigger copy (`:443-445`) is the same guard with no comment and is the wrong one to copy from — the reach copy's comment is itself a citation back to it (`// LOAD-BEARING (background_trigger_probe_test.go:443)`). Without the guard, any failure before `cmd.Start` reaches `syscall.Kill(-0, SIGKILL)`, and `kill(0, sig)` is defined as "send to every process in the **caller's own** process group": the test binary would SIGKILL itself and its siblings. One line, whole-run blast radius.
- **The grace `select`** — `select { case <-pyryExited: case <-time.After(probePyryExitGrace) }` (`:367-373`). **Kept.** This is what gives pyry its real chance to exit on its own; the AC's "the driver's body never reads `pyryExited`" is about the body, not this cleanup.
- On the grace-expiry branch, `t.Logf` naming `probePyryExitGrace` — the honest stand-in for the precedents' `rec.note`, since this file has no record. It names a duration and nothing from the process table.
- `_ = syscall.Kill(-h.PyryPID, syscall.SIGKILL)`. ESRCH is benign; `spawnProbePyry` set `Setpgid`, so the negative pid reaps pyry's whole group.

---

## Error handling

**Two `t.Fatalf`s, and they are the only ones.** Both are rig faults with **no named home in the gate**, and reporting them through the gate would file a false story:

| Step | Condition | Why fatal rather than a fact |
|---|---|---|
| 10 | `probeWaitForDirectChild` returns 0 | pyry never spawned claude. The gate has no arm for "the supervisor never came up", and every reading downstream would be about a process that does not exist. |
| 11 | `probeWaitForSessionID` returns `""` | Without a session id the assembly reads a transcript path that cannot exist, comes back `BashIssued: false`, and the gate reports `stage-no-bash-call` — "the model issued no Bash call" for a run in which the model was never asked. A false attribution, which is exactly the class this family guards against. |

Both messages follow the precedents (`background_reach_probe_test.go:403`, `:409`): the deadline that expired plus `truncate(stderr.Bytes())`. That is pyry's own structured log, it goes to a test failure message and never to an artifact, and **nothing from the process table and no argv is interpolated into either.**

**Everything else is a fact, never a failure.** No Bash call, a different command, no trigger, no rendezvous, a failed `ps`, a surprising row count — each has a named, rank-ordered arm in `finOutcomeStagingGate`, and the driver's job is to fill the field and hand it over. The driver adds no `t.Error`, no error return, and no disposition of its own. This is the same posture every gate and reduction in this family carries: an instrument reading is a datum, not a reason to abort a turn.

**One inherited abort path, named so a live caller knows about it.** `finLiveAssembleStaging` inherits `ReadJSONL`'s `t.Fatalf` on a transcript it cannot open or parse (`fixtures.go:152`, `:163`) — #1304's shipped behaviour. A *missing* file is not fatal: `probeWaitForBashToolUse` guards with `os.Stat` first (`background_trigger_probe_test.go:767`), so it times out to "no Bash call issued", the safe direction.

**Captured-bytes discipline.** The driver opens no channel the shipped records keep shut:

- **No `ps -E` / `-Eww` anywhere.** Those dump `CLAUDE_CODE_OAUTH_TOKEN` and `ANTHROPIC_API_KEY`. This driver needs no environment read at all. Its only content scan is `pinScanArgv` → `reachScanArgv`'s `ps -axww -o pid=,ppid=,pgid=,command=` (`background_reach_probe_test.go:876`) — `-ww` widens output without touching the environment.
- Matched rows and claude argv cross the handle as the **capped `reachProc.Command`** the shipped matcher already produces (`reachCapCommand`, `:945`). **Do not re-read an uncapped argv to "repair" a truncation** — the cap is the discipline, not a defect, and `finLivePinReduce`'s membership test reads the recorded needle list precisely so a truncated row still counts (`finding_live_pin_test.go:151-158`).
- This file **writes none of them to an artifact, logs none of them, and interpolates none of them into any `Detail`.** It produces no `Detail` at all — the only one on the handle is the gate's, and the gate quotes neither operand of its identity arm by construction (`finding_staging_gate_test.go:304`).
- **An empty `Pin.ClaudeCommand` is ambiguity, not a staging failure.** `tdnClaudeCommand` returns `""` when zero *or several* rows carry the claude needle (`teardown_liveness_probe_test.go:571-573`). It is provenance; it is not one of `finOutcomeStaging`'s eight fields, and it must not be gated on here or downstream.
- The content pin is a full-table `ps -axww` **by construction** — that is how the wrapper and the `cat` are found by argv content — and **only its matched rows are recorded.** The driver records no tree snapshot at all: `probeWaitForDirectChild`'s internal walk is the narrow descendant walk rooted at pyry's pid (`background_trigger_probe_test.go:870`) and the driver keeps only the int pid it returns.

---

## Testing strategy

**Compile and vet only, and that is the whole of it.** The driver is not offline-drivable — that is the entire reason its three blockers exist, and the reachable behaviour is already covered by their traps.

- The file is compiled and vetted under `make e2e-realclaude` (`$(GO) test -tags e2e_realclaude ./internal/e2e/realclaude/...`) with **no Claude login**, because it ships no test that runs the driver. `go test` runs its default vet subset; none of those analysers flags an uncalled package-level function. Note that `go vet` and `staticcheck` in `make check` run **without** `-tags e2e_realclaude`, so neither analyses this file.
- `finLiveRunStage` will have **no caller anywhere in the tree** until #1337 lands. That is correct and expected. **Do not invent a caller to silence a lint that does not run, and do not add an offline test that spawns pyry or a real `claude`.** `needs-real-claude` sits on #1337, not here, matching every sibling instrument ticket (#1302, #1304, #1313, #1316, #1320, #1324, #1326, #1338, #1342, #1343).
- **Do not re-add the FIFO-name distinctness rule.** It shipped in #1342 as `TestFinLiveStageFIFONameIsDisjointFromEveryShippedName` (`finding_live_staging_test.go:363`), which pins `finLiveStageFIFOName` substring-disjoint in both directions against every other shipped name. This ticket *consumes* the name; it does not declare it, so it owes no trap for it.

**Identifier prefix.** `finLiveRun*`. Verified at `3e6df75` with the **narrow** form and its known-taken control:

```
rg -o 'finLiveRun' --glob '*.go' .        # 0
rg -o 'trail[A-Z]' --glob '*.go' .        # 1679 — the control that proves the recipe reports free only when free
```

**Do not use the broad `finLive[A-Z]` form** — it returns **140** at `3e6df75` (the three blockers shipped `finLivePin*`, `finLiveStage*`, `finLiveAssemble*`) and would report `finLiveRun*` as taken when it is free. Re-run both before starting. Prefixes taken by sibling instrument tickets: `finGather*`, `finAttribute*`, `finStage*`, `finOutcome*`, `finRecord*`, `finTrailer*`, `finWrite*`, `finTranscript*`, `finLivePin*`, `finLiveStage*`, `finLiveAssemble*`.

---

## Out of scope

- The pin reduction and its expected count (#1338); the staged command literal, prompt, FIFO name and env delta (#1342); the eight-field assembly (#1343). **Consume, never re-derive.**
- The trailer reading, the run classification, the record and the artifact (#1337). This driver reaches the staging verdict and stops.
- Headless `PYRY_USE_STREAMJSON=1` (#1237); turn accounting — `num_turns`, `--max-turns` (#1234); the interactive turn-state surface (#1227); teardown reaping and what survives pyry's exit (#1231).
- `process_pin_liveness_test.go:603-613`'s known guard-quality gap from #1235 (recorded at `docs/knowledge/codebase/1235.md:88-94`) stays out.
- **No knowledge-base note is a deliverable here.** `docs/knowledge/codebase/1340.md` is the documentation phase's, written from this spec plus the merged diff.

---

## Open questions

1. **Does #1337 want the session id on the handle?** This spec says no, following #1343's posture (the handle carries what its consumer needs and nothing more). If #1337's record wants to publish `session_id`, that is #1337's argument to make and one field to add — the driver already holds the value.
2. **The un-timed decoy case has never been observed.** #1223's system prompt asks for exactly one Bash call and fired 7/7. If a live run ever comes back `stage-pin-count-unexpected` with a correctly staged command visible in the transcript, the decoy path is the first hypothesis, and the fix is a content-first waiter in the shared rig — a change both precedents declined to make and which should be its own ticket, not an in-place edit here.

---

## Security review

**Verdict:** PASS

**Findings:**

- **[1. Trust boundaries]** Three boundaries, each explicit and each already owned by a shipped, single-function reduction rather than by this file. (a) *Process table → memory*: `pinScanArgv` → `reachMatchArgvRows` is the package's one full-argv matcher, and this driver adds no second one; the rows reach the handle only through `finLivePinReduce` (`finding_live_pin_test.go:202`). (b) *Model-controlled transcript → verdict*: crossed entirely inside `finLiveAssembleStaging`, which selects content-first and never lets a model-supplied string become a code path; the driver discards both raw envelopes (steps 13–14), so no model bytes reach the handle at all. (c) *Subprocess stdout → parent state*: the only value parsed out of pyry's stdout is the session id, via the shipped `probeWaitForSessionID`, and it is used solely as a path component under the test's own `t.TempDir()`-rooted `HOME`. Downstream callers are signalled by type: `finLivePinReading` and `finLiveRunHandle` both carry **no json tags**, which is this family's shipped marker for "input only — never published".
- **[2. Tokens, secrets, credentials]** No finding, and the *absence* is structural rather than careful. The threat is real and named: `CLAUDE_CODE_OAUTH_TOKEN` / `ANTHROPIC_API_KEY` live in the environment of every process this rig touches, and this family's artifacts get pasted into public issues. The driver takes **no environment read of any process** — no `ps -E`, no `-Eww`, no `-e` with an environment column, no `probeReadLeverEnv` call. Its one content scan is `ps -axww -o pid=,ppid=,pgid=,command=` (`background_reach_probe_test.go:876`), whose column set cannot contain an environment. `WithWorktreeAuthenticated` re-pins the operator's credential into the subprocess environment (`fixtures.go:96-107`) — that is pre-existing, is what makes a live turn possible at all, and this driver neither reads nor copies it. No token is generated, stored, rotated or compared here, so entropy, storage-mode and constant-time-comparison questions are inapplicable by construction.
- **[3. File operations]** Four paths, all rooted in the test's own `t.TempDir()` and none caller-controlled. `fifoPath` is `filepath.Join(workdir, finLiveStageFIFOName)` where the name is a shipped compile-time constant — no user input, no traversal surface. `prompt.txt` / `system.txt` are written at **`0o600`** (§ Design step 7), matching the precedent. The transcript path comes from `jsonlPathFor(workdir, sessionID)`; `sessionID` is pyry-minted and, on the ptyrunner path, is the uuid pyry itself generated. **TOCTOU:** `probeWaitForBashToolUse` does `os.Stat` then `ReadJSONL` (`background_trigger_probe_test.go:767-768`) — a genuine check-then-use, but on a path inside a `t.TempDir()` whose `HOME` was repointed before the directory existed, written by this test's own subprocess, so there is no second writer to win the gap. No symlink is followed on any path the driver constructs. No atomic-write requirement: nothing here persists state that a later run reads.
- **[4. Subprocess / external command execution]** Four execs, all shipped. **No `sh -c` anywhere in the driver** — `spawnProbePyry` uses `exec.Command` with a fixed argv (`background_trigger_probe_test.go:623-633`), and the one shell that does run (`zsh -c`, the wrapper claude runs its Bash tool through) is claude's, not this rig's. The one value the driver contributes to a command line is `fifoPath`, and it reaches claude **through a prompt file**, not through argv — it is never concatenated into a shell string by this file. `finLiveStageCommand` is a bare `cat <path>`, chosen over `finOutcomeHoldCommand`'s `sh -c … ; exit 0` precisely because the model is asked to reproduce it verbatim (`finding_live_staging_test.go:131-134`). **Environment:** inherited from `os.Environ()` plus a two-entry delta, both entries shipped constants (`BASH_DEFAULT_TIMEOUT_MS=5000`, `PYRY_USE_STREAMJSON=0`); nothing is scrubbed and nothing needs to be, since the driver publishes no environment read. **Signals / double-fork escape:** `spawnProbePyry` sets `Setpgid`, so the cleanup's `Kill(-pid, SIGKILL)` reaps the whole group rather than a single pid — which is exactly the double-fork case. The `PyryPID <= 0` guard is the one finding this category *would* have produced, and § Cleanups mandates it verbatim with its comment: without it a pre-`cmd.Start` failure sends SIGKILL to the caller's own process group, killing the test binary and its siblings.
- **[5. Cryptographic primitives]** Not applicable, and the reason is that no value here is security-relevant randomness: no token, nonce, key or id is generated by this file. The one uuid in play is minted by pyry (`ptyrunner/runner.go:618`) and only ever compared for path construction. No `math/rand`, no hand-rolled primitive, no secret comparison.
- **[6. Network & I/O]** No sockets, no listeners, no HTTP surface — inapplicable. The analogous concerns are answered anyway: every read is size-capped or deadline-bounded. `reachCapCommand` caps each retained argv at `reachMaxCommandBytes`; every wait in steps 10–14 carries an explicit shipped deadline (25 s / 20 s / 60 s / 30 s / 45 s); the `ps` execs run under `context.WithTimeout`; the kill cleanup is bounded by `probePyryExitGrace`. There is no unbounded read and no path that can block forever. The unbounded surface that *does* exist is `probeSyncBuffer`, which grows with pyry's stdout — bounded in practice by `--max-turns=6` and by the test's own timeout, and shipped that way in both precedents.
- **[7. Error messages, logs, telemetry]** The sharpest category for this file, and the design closes it in three places. The two `t.Fatalf`s carry a deadline and `truncate(stderr.Bytes())` — pyry's own structured log, to a test failure message, never to an artifact. The grace-expiry `t.Logf` names a duration and nothing else. The driver produces **no `Detail` of its own**; the only one on the handle is the gate's, which quotes neither operand of its identity arm by construction (`finding_staging_gate_test.go:304`) and whose `PinScanErrored` arm deliberately drops `ps` stderr as "a captured string on the same footing as argv" (`:337`). Captured argv reaches the handle only capped, and the handle carries no json tags so it cannot be marshalled into a published record by accident. **No telemetry, no metrics, no artifact** — this file writes nothing to disk but the two prompt files pyry reads.
- **[8. Concurrency]** No locks are taken by this file, so lock-ordering is inapplicable; the one piece of shared mutable state (`stdout` / `stderr`) is guarded inside the shipped `probeSyncBuffer`, which exists for exactly this reason and returns a copy from `Bytes()`. `h` is written only by the driver body and read by a `t.Cleanup`, which runs after the body returns — no concurrent access, verifiable under `-race`. **Goroutine lifecycle:** one goroutine is spawned (`cmd.Wait` → `close(pyryExited)`), and § Concurrency states its exit condition and why it cannot be held open by the `cat` (claude is on a PTY, so no descendant inherits pyry's stdout pipe). `holdProbeFIFO`'s goroutine has a shipped shutdown path for the no-reader case. **Shutdown safety:** interruption mid-run leaves the FIFO release and the group SIGKILL as the two cleanups, in LIFO order; nothing partial persists on disk, because nothing persistent is written.
- **[9. Threat model alignment]** No relay surface and no `docs/threat-model.md` in this repo, so the applicable model is this file family's own, stated in the shipped headers: **artifacts from these rigs are pasted into public issues, so the adversary is an accidental credential or argv disclosure by the rig itself.** The design addresses it on every channel the family has named — no environment read (rule 1), capped argv only, no json tags on any carrier, matched rows only rather than a full table, and no artifact written from this file at all. Two threats are explicitly out of scope and named with their owners: the un-timed decoy Bash call (open question 2 — a shared-rig change, its own ticket) and `process_pin_liveness_test.go:603-613`'s known guard-quality gap (#1235, recorded at `docs/knowledge/codebase/1235.md:88-94`).

**Reviewer:** architect (self-review per `architect/security-review.md`)
**Date:** 2026-08-05
