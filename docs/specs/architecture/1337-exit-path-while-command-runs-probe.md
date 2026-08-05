# #1337 — Probe: does `pyry agent-run` reach its normal exit path while a backgrounded command still runs? (ptyrunner default, live)

**Size:** S (confirmed; not split)
**File:** new `internal/e2e/realclaude/finding_exit_path_probe_test.go` under `//go:build e2e_realclaude`, plus one field on `finLiveRunHandle` in `internal/e2e/realclaude/finding_live_run_test.go`.
**Identifier prefix:** `finExit*`. Re-verified at `34b863e`: `finExit[A-Z]` is **0** across `*.go`; control `trail[A-Z]` is **1679** and `finLive[A-Z]` is **170**, so the recipe reports free only when free.

---

## Files to read first

Read these before writing anything. Every one of them is consumed by this rig; none is re-derived.

**The driver you call, and the one file you edit**

- `internal/e2e/realclaude/finding_live_run_test.go:119-173` — `finLiveRunHandle` as it stands before your edit, and at `:101-118` the five deliberate omissions (your edit adds a field that is not one of them; do not restate a field count anywhere, since yours changes it). Extract: what crosses whole (`Pin`, `Staging`), what is receive-only (`PyryExited`), and the invitation this ticket accepts — *"If #1337 needs one of these, that is #1337's argument to make."*
- `internal/e2e/realclaude/finding_live_run_test.go:247-446` — `finLiveRunStage`'s body. Extract: the cleanup registration order (`:267-300`), the `cmd.Wait` goroutine you edit (`:326-329`), the two `t.Fatalf` abort paths you inherit (`:331-341`), and the during-turn pin (`:388-423`).
- `internal/e2e/realclaude/finding_live_run_test.go:5-70` — the file header. Extract: the `ps -E`/`-Eww` prohibition and the sentence at `:63-64` counting *"the two `t.Fatalf` messages"* and *"the one `t.Logf`"* — your edit must not falsify it.

**The composition, in call order**

- `internal/e2e/realclaude/finding_run_gather_test.go:239-270` — `finGatherInputs`' six fields. Extract: `Pinned` is `[]int`; `ClaudeState`'s only admissible producer is `pinReadState(...).Verdict`; `Stderr` is `[]byte`.
- `internal/e2e/realclaude/finding_run_gather_test.go:348-358` — `finSighting`'s eight fields. Extract: it carries neither `.Line` nor the `*resultTrailer`, which is why it is safe to forward.
- `internal/e2e/realclaude/finding_run_gather_test.go:424-507` — `finGatherReadings`' three returns and its `pinScanArgv(in.Needles, nil)` call at `:478`. Extract: the scan is internal, only counts and per-pid liveness come back, and `Needles` is used with **no** exclusions.
- `internal/e2e/realclaude/trail_run_outcome_test.go:306-321` — the classifier's ordered decision list. Extract: Step 2 (attribution) outranks Steps 3-8, so a post-trailer match count of 0 costs nothing.
- `internal/e2e/realclaude/finding_trailer_evidence_test.go:197-214, :241-272` — `finTrailerBuild`. Extract: the first parameter is a bare `string` from **either** closed set (the classifier's eleven or the staging tier's seven), carried as returned.
- `internal/e2e/realclaude/finding_run_record_test.go:222-242` — `finRecordInputs`' eight fields. Extract: `Rows` is `[]reachProc`, `ClaudeCommand` crosses whole and is reduced internally, and there is no runner-agreement input.
- `internal/e2e/realclaude/finding_run_record_test.go:123-135` — `finRecordRun.ExitCode`'s doc. Extract: **0 is a real successful exit**, and a caller with no observed exit hands `pinExitStatusUnknown` — *"a documented caller obligation, not a validated one."* This is the second criterion's whole argument.
- `internal/e2e/realclaude/finding_artifact_write_test.go:131-163` — `finWriteArtifacts`. Extract: the two files it writes, and the note's content rule at `:141-150` (never a `%v` verb on a struct or a slice).

**The staging tier and the values you branch on**

- `internal/e2e/realclaude/finding_staging_gate_test.go:113-134` — the seven staging outcomes. Extract: `finOutcomeReadyToClassify` is the pass-through and is deliberately **not** the zero value.
- `internal/e2e/realclaude/finding_staging_gate_test.go:198-201` — `finOutcomeResult`'s two fields, both publishable.

**Precedents you copy the shape of**

- `internal/e2e/realclaude/background_reach_probe_test.go:266-316` — the probe entry point: `reachEnableEnv` skip, `os.MkdirTemp` (deliberately not `t.TempDir()`), `t.Logf` of the dir, delegate to a body function. Copy this shape; **do not** copy the `PYRY_USE_STREAMJSON` gate at `:296-306` (withdrawn — see § Design).
- `internal/e2e/realclaude/background_reach_probe_test.go:1091-1113` — `reachRunnerPathFromEnv`. Extract: it reads ambient `os.Getenv` first and only then lets the delta override, which is why the delta must be passed.
- `internal/e2e/attach_stdio.go:231-237` — the repo's own `ProcessState.ExitCode()` read with a `-1` fallback. This is the shipped shape for the handle's new field; `auto_attach.go:357-363` is the identical sibling.
- `internal/e2e/realclaude/process_pin_liveness_test.go:234-236` — `pinExitStatusUnknown = -1`, and `:275-295` for `pinReadState`.
- `internal/e2e/realclaude/trail_run_outcome_test.go:611-631` — `trailRunWellFormed` / `trailRunProofReadings`, the fixtures the one offline test drives.

**Production code the evidence is about (read, do not touch)**

- `internal/agentrun/ptyrunner/runner.go:479-485` — the teardown-order comment pinning `emitter.Close() → cancel() → [reap] → sess.Close()`, with the reap defer at `:398`. This is the ordering the whole proof rests on.
- `internal/agentrun/ptyrunner/runner.go:492-503` and `:596-606` — the budget `Terminate` hook (reaps *inside* the hook, pre-trailer) and `Run`'s `return nil` on a cancelled run context.
- `cmd/pyry/agent_run.go:266-278` — the exit mapping. Extract: only a non-nil, non-`context.Canceled` error becomes a non-zero exit, so completion and budget-termination share an exit code.
- `internal/agentrun/reap.go:56-57, :65, :76` — the skip-if-already-exited and the log line naming the groups actually killed. This log is the primary evidence.

---

## Context

`pyry agent-run`'s ptyrunner path writes its result trailer at `emitter.Close()`, which the runner's own defer chain pins **before** `sess.Close()`'s SIGTERM and before the descendant reap. So at trailer time claude is alive, unsignalled, and any command claude auto-backgrounded is still a descendant of pyry. Nobody has measured whether pyry therefore declares a turn finished while the work that turn described is still in flight.

Three things make this harder than "run it and look", and all three shape the design:

1. **The exit code cannot separate the outcomes.** Completion and budget-termination both exit 0 (`agent_run.go:271-277`). The discriminator is `terminal_reason` in the trailer.
2. **The instant of interest is not directly observable.** A rig sees the trailer only when it polls stdout, up to one `probePollInterval` (200 ms) after the write; the reap starts effectively immediately after that write. A liveness read taken at *observation* time systematically finds the command already reaped. Reporting that as "the command had exited" is a false negative on the question being asked.
3. **The witness that does work is pyry's own reap log.** `ReapDescendantGroups` logs the groups it actually killed and skips those already gone, so a group named there was alive when the reaper ran — which on this path is strictly after the trailer was written. That ordering argument is internal to the run's own logs and does not depend on any point-in-time read.

Everything needed to consume that evidence is shipped and offline-proven. `finLiveRunStage` (#1340) stages the turn and hands back a handle; `finGatherReadings` → `trailClassifyRun` → `finTrailerBuild` → `finRecordBuild` → `finWriteArtifacts` turn it into a publishable record. This ticket is the live entry point that wires them and publishes the result. It re-derives nothing.

---

## Design

### The shape of the file

One new test file with, in order: the file header, two constants, one pure helper, the entry-point test, the probe body, and one offline table test.

```go
const finExitEnableEnv = "PYRY_PROBE_EXIT_PATH"   // free at 34b863e; five PYRY_PROBE_* names exist, none is this
const finExitPyryExitDeadline = 120 * time.Second
```

`finExitPyryExitDeadline` is **this ticket's own constant and is not `probePyryExitGrace`.** Its doc must say why, because the two are easy to conflate: `probePyryExitGrace` (20 s, `background_trigger_probe_test.go:134`) measures the driver's defence-in-depth cleanup waiting *after the FIFO release* before SIGKILLing (`finding_live_run_test.go:277-300`). This one measures a turn **completing** with the FIFO **still held** — claude receiving the `tool_result`, producing a final assistant message, `emitter.Close()` writing the trailer, teardown, exit. That is a model round-trip plus teardown, not a mechanical unblock. 120 s is deliberately generous: a too-short deadline produces a false `trailOutcomeVoidPyryDidNotExit` on a healthy run, and on a `needs-real-claude` ticket that costs an operator round-trip, whereas a too-long one costs only wall-clock on a run that has already failed.

The skip message documents `-timeout 15m`, which accommodates this deadline alongside the driver's own (25 + 20 + 30 + 45 + 60 s) and the gather's 10 s trailer wait, plus the worktree setup and the `pyry` build.

**One skip gate, not two.** Follow `background_reach_probe_test.go:273-283`'s `reachEnableEnv` shape so `make e2e-realclaude` skips by default and the message names the exact invocation. **Do NOT add the reach probe's `PYRY_USE_STREAMJSON=1` gate** (`:296-306`): neither of its two reasons transfers. Its delta does not name the variable, whereas `finLiveStageEnvDelta()` names `PYRY_USE_STREAMJSON=0` explicitly and `spawnProbePyry` appends the delta to `os.Environ()`, which `os/exec` resolves in favour of the later value; and its content-first root pinning keys on `--session-id`, whereas this rig resolves claude through `probeWaitForDirectChild`'s descendant walk. Copying the gate would skip a run that would have been correct. The third criterion's observed-path reading is the real guard and is strictly stronger.

The entry point mirrors the precedent exactly: skip gate → `os.MkdirTemp("", "pyry-1337-probe-*")` (**not** `t.TempDir()`, which is removed when the test ends and the operator needs these files to compose the issue comment) → `t.Logf` naming the dir → delegate to the body function on the same `*testing.T`. **No `t.Parallel`** — `finLiveRunStage` reaches `WithWorktreeAuthenticated`, which calls `t.Setenv`, and Go's runtime refuses that pairing. That helper also *skips* when neither credential variable is set, which is a separate skip from the opt-in gate.

### The one edit to `finding_live_run_test.go`

The handle gains pyry's exit status. The driver's wait goroutine currently discards it (`_ = cmd.Wait()`, `:326-329`) and `cmd` is function-local, so no consumer can recover it — `cmd.Wait` has already reaped pyry and a second wait returns `ECHILD`.

Contract:

- New field `ExitStatus int` on `finLiveRunHandle`, documented as **valid only after a receive from `PyryExited`**.
- Initialised to `pinExitStatusUnknown` in the driver's first composite literal (`:258-265`). This is one line and it removes the zero-value hazard the record's own doc names: an unwritten `int` reads as 0, which `finRecordRun.ExitCode` documents as *a real successful exit*. Initialising to `-1` makes the unwritten value point the safe way, in the same zero-polarity doctrine `trailRunReadings.PyryExited` and `finOutcomeReadyToClassify` each argue for themselves. It makes the *value* safe; it does not make an unsynchronised *access* safe, and the field's doc says so.
- The goroutine writes it **before** `close(pyryExited)`. That close is the only happens-before edge a consumer has; a write after it is a race that reads correct on every run nobody is examining. Shape, following `internal/e2e/attach_stdio.go:231-237`: `cmd.Wait()`, then guard `cmd.ProcessState != nil` and assign `cmd.ProcessState.ExitCode()`, then close. The `Wait` error itself stays discarded — it carries no information the exit status does not.

**Prose that goes stale by this edit, and must be updated in the same commit.** The type's summary sentence at `:82-84` enumerates the handle's contents and becomes incomplete; extend it to name the exit status. `PyryExited`'s field doc at `:126-133` must state that the status is written before the close and that the receive is the edge that makes reading it safe. The five-omission block at `:101-118` stays true as written — the exit status was never one of the five — and its closing invitation is what this ticket is accepting; say so in one clause rather than rewriting the block. The header's count of *"the two `t.Fatalf` messages"* and *"the one `t.Logf`"* (`:63-64`) stays true: this edit adds neither.

### The probe body, in order

The order is load-bearing at three points and each is called out below.

**1. Stage.** `h := finLiveRunStage(t)`. Everything after this runs with the FIFO write end still held: `holdProbeFIFO` releases only in the `t.Cleanup` it registers itself, which by construction runs after this body returns.

**2. Wait for pyry's own exit — in the body, never in a cleanup.** This is the first criterion's whole point. If the wait were taken after the hold's release, the rig's own release would have produced the exit and the reading would measure the rig rather than pyry.

```go
pyryExited := false
exitCode := pinExitStatusUnknown
select {
case <-h.PyryExited:
    pyryExited = true
    exitCode = h.ExitStatus // safe HERE and only here: the receive is the happens-before edge
case <-time.After(finExitPyryExitDeadline):
}
```

**The read sits inside the arm, and that is not a style choice.** Any shape that evaluates `h.ExitStatus` outside the receive — passing it to a helper alongside a bool, hoisting it above the `select` — reads the field concurrently with the driver's goroutine and is a data race `go test -race` will flag. There is therefore deliberately **no** `finExitObservedCode(exited bool, status int)` helper: the only race-free shape is the one that needs no helper, and the unexited path simply leaves `pinExitStatusUnknown` in place. That value satisfies the second criterion's requirement that a non-exiting run must not publish `exit_code: 0`.

`-1` is not uniquely "did not exit" — `ProcessState.ExitCode()` also returns `-1` for a signalled process, which is what the driver's defence-in-depth SIGKILL would produce. Both mean "not a clean self-exit", which is the distinction the record needs; **which of the two it was** is stated through the log channel in step 7, as the second criterion requires.

**3. Read the claude child's liveness.** `pinReadState(h.ClaudePID).Verdict`, and nothing else — never a raw `ps` column, because this value crosses the gather unvalidated, is republished as `claude_state`, and is quoted into a published `Detail`. `h.ClaudePID` is guaranteed positive (the driver fatals on 0).

It is taken **here**, after the exit wait, and its expected value on a healthy run is `no-such-process`: pyry has exited, so `sess.Close()` has already SIGTERMed claude. That is the corroboration's **known blindness**, and it is exactly why it is corroboration and not the finding — the reap runs between the trailer and claude's SIGTERM, so in that window the command is dead-by-reap while claude still reads alive, and a witness keyed on claude's liveness would certify the read as timely over precisely the case this probe exists to catch. Record it; never let it override an attribution.

**4. Gather.** One call, on the pass-through of everything above:

```go
readings, attribution, sighting := finGatherReadings(finGatherInputs{
    Stdout:      h.Stdout,                 // the LIVE buffer; the gather polls it
    Needles:     []string{h.FIFOPath},     // the FIFO path ALONE
    Stderr:      h.Stderr.Bytes(),         // read AFTER the exit, so the reap log is in it
    Pinned:      h.Pin.PGIDs,              // already []int; nothing is converted here
    PyryExited:  pyryExited,
    ClaudeState: claudeState,
})
```

`Needles` is the FIFO path **alone**. Do not copy the driver's two-needle list (`finding_live_run_test.go:411-416`): that scan carries `tdnClaudeNeedle` because `finLivePinReduce` separates the populations afterwards, and the gather has no such reduction — adding the claude needle would put claude's own row into the classifier's match-count arms and into the published liveness list. The FIFO path alone is safe without exclusions: neither the test binary's argv nor pyry's carries it.

`Stderr` must be read after the exit wait. pyry's reap log lands on stderr at teardown, which is why the handle carries live buffers rather than a snapshot.

**5. Decide the published outcome — staging first, classifier only on the pass-through.**

```go
// finExitClassify returns the outcome string finTrailerBuild publishes, the
// classifier's full outcome, and whether the classifier was consulted at all.
// The classifier is reached on finOutcomeReadyToClassify and on nothing else.
func finExitClassify(staging finOutcomeResult, readings trailRunReadings) (string, trailRunOutcome, bool)
```

On a non-pass-through staging value it returns `(staging.Value, trailRunOutcome{}, false)`; on `finOutcomeReadyToClassify` it returns `(c.Value, c, true)` for `c := trailClassifyRun(readings)`.

This is the fourth criterion made structural. On an unstaged run the argv scan still runs over a healthy process table, parses rows and matches nothing — which would reach the classifier's final fall-through arm, one of its three *answers*, published about a run in which no command ever existed. `finTrailerBuild`'s first parameter is a bare `string` fed from **either** closed set precisely so the staging value can be carried straight through, so no adapter is needed.

It is a named pure function rather than an inline `if` for one reason: it is the only decision this ticket introduces that is drivable offline, and it is the one the fourth criterion is entirely about. See § Testing strategy.

**6. Build and write.**

```go
rec := finRecordBuild(finRecordInputs{
    ExitCode:      exitCode,
    Rows:          h.Pin.Rows,          // the DURING-turn pin, never a second scan
    Liveness:      readings.Liveness,   // the gather's post-trailer per-pid reads
    Attribution:   attribution,         // the gather's second return, whole
    Trailer:       finTrailerBuild(outcome, sighting),
    RunnerFromEnv: reachRunnerPathFromEnv(h.EnvDelta),
    ClaudeCommand: h.Pin.ClaudeCommand, // whole; the builder reduces it
    ClaudeVersion: h.ClaudeVersion,
})
finWriteArtifacts(t, artifactDir, rec)
```

Four obligations here, each of which has a wrong answer that would pass compilation:

- `Rows` is `h.Pin.Rows`, the during-turn pin. The gather never returns `scan.Matches`; a second post-trailer scan would be the re-derivation the fourth criterion forbids and on a healthy run would publish an empty `matched_rows`. `Pin.Rows` is already FIFO-needle-only — the driver's scan matched three rows because claude's own row rides the second needle, and `finLivePinReduce` filtered it out.
- `Trailer` is built from the gather's **third return**. Never call `trailWaitForTrailer` a second time: by then the trailer is already in the buffer, the second call matches on its first poll and reports `trailBoundFromStart`, a discriminator whose own doc says it bounds nothing. That is a mis-report, not a cost. The raw trailer line is not published at all — it holds verbatim model output, and `finSighting` carries neither `.Line` nor the `*resultTrailer`, so the prohibition holds by the shape of the input.
- `RunnerFromEnv` comes from `reachRunnerPathFromEnv(h.EnvDelta)` with the **real** delta. That function reads ambient `os.Getenv` first and only then lets the delta override, so an empty or partial delta would make it a reading of the operator's shell rather than of this run.
- `ClaudeCommand` is `h.Pin.ClaudeCommand` **whole and unexamined**. An **empty value is admissible and is not a staging failure**: `tdnClaudeCommand` returns `""` when zero *or* several rows carry the claude needle, so emptiness is ambiguity about which row was claude's, never a claim that the run took the other path. It reaches `tdnRunnerFromArgv` inside the builder and lands on the shipped `indeterminate` verdict — the third answer, never a disagreement. Do not gate on it, default it, or repair it with a second argv read. The rig computes **neither** the argv label nor the agreement; `finRecordBuild` does both internally.

**7. Publish what the artifact writer does not.** The record carries only the trailer sub-record's outcome *string*; the classifier's `Detail`, `ClaudeState`, `LivenessSummary`, gate value and match counts reach no field of `finRecordRun`, and the staging result reaches no artifact at all. So the fifth criterion's channel is `t.Logf`, which is provably safe for both values — `TestTrailRunOutcomeCarriesNoCapturedBytes` (`trail_run_outcome_test.go:1141`) and `TestFinOutcomeResultCarriesNoCapturedBytes` (`finding_staging_gate_test.go:705`) — and keeps the two proven artifact files exactly as the shipped writer produces them rather than growing a second file-writing surface no sweep covers.

Log, each on its own line: the staging result; the classifier's full outcome, or an explicit "not consulted" line naming the staging value that stopped it; whether pyry exited within `finExitPyryExitDeadline` and that the FIFO hold was still held for the whole of that wait; and the one-sentence finding required by the second criterion.

Render **field by field or through `json.Marshal`** — both types carry json tags. **Never a `%v` verb applied to a struct or a slice**; that is the content rule the writer's own note line states, and it is the concrete mechanism by which an argv or a sub-record `Detail` reaches a public issue.

The finding sentence keys on the classifier's value: `trailOutcomeRunningAtTrailer` means pyry declared the turn finished while the command was still running. It must also state that **the exit code alone does not separate a completed run from a budget-terminated one**, naming `terminal_reason` as the field used instead. When the classifier was not consulted, the sentence says no claim is made and names the staging outcome. Name `terminal_reason` and the counts; do not quote `stop_reason` (model-influenced and carried uncapped by design — the record publishes it as a field, which is the exposure decision already made) and do not quote any sub-record's `Detail`.

### What the evidence will look like on a healthy run, and why that is not an inconsistency

Three readings will look wrong to someone reading the artifact cold, and the record must say so through the log channel rather than leave them to read as defects:

- **`matched_rows` holds `finLivePinWantRows` (2) rows while the classifier's `match_count` is 0.** The rows are the during-turn pin; the count is the gather's post-trailer scan, taken after the reap. That gap *is* the systematic-lateness story. It can only be told through the log channel, because `finRecordRun` has no match-count field at all and the artifact alone cannot show the two numbers side by side.
- **`liveness` is absent from the artifact.** It is `omitempty` and the post-trailer scan matched nothing, so there are no per-pid reads. Expected, for the same reason.
- **`lateness_bounded` is `false` and `lateness_bound_from` is the from-start discriminator.** This rig waits for pyry's exit before gathering, so the trailer is already in the buffer and `trailWaitForTrailer` matches on its first poll. The bound is uninformative **by construction on this rig**, and that costs nothing: the second criterion's primary evidence is the reap-log attribution, and the classifier consults it at Step 2, which outranks Steps 4, 5, 7 and 8 in its own ordered list.

**Do not stretch the hold past the reap to force a post-trailer match.** That would destroy the ordering the whole proof rests on.

### Deliberately not done

- **No budget-fired run is staged.** Its `Terminate` hook reaps *inside* the hook, before the trailer is written, so a budget-fired run produces a "command was already gone" reading that says nothing about the exit path, and its reap-log entry, being pre-trailer, proves nothing either. `spawnProbePyry` passes `--max-turns=probeMaxTurns` (`"6"`), so headroom is generous but not infinite; a run that hits it anyway is handled as a non-verdict outcome, not as a finding.
- **No environment read of any process.** `ps -E` / `-Eww` dump `CLAUDE_CODE_OAUTH_TOKEN` and `ANTHROPIC_API_KEY`; this rig needs no environment read at all. `-ww` widens output without touching the environment and is what the shipped scanner already uses. The rig itself runs no `ps` beyond `pinReadState`'s pid-pinned lookup, whose column set is `pid=,ppid=,stat=` by construction.
- **No second argv scan, no second trailer wait, no re-derived agreement verdict, no re-derived match count.**

---

## Concurrency model

No goroutine is created by this ticket. The one that matters is the driver's `cmd.Wait` goroutine, whose contract this ticket tightens:

- **The edge.** `close(pyryExited)` is the sole happens-before edge between the goroutine's write of `ExitStatus` and this rig's read of it. The write goes strictly before the close; the read goes strictly inside the receive arm.
- **Zombie safety.** pyry is a direct child of the test binary, so between exit and `Wait` it is a zombie, and a zombie answers `Signal(0)` with `nil`. A liveness probe would report "still running" for an already-returned pyry. The channel is the only correct witness, which is why the first criterion's wait is a channel receive and not a `pinReadState` on `h.PyryPID`.
- **Termination.** The goroutine exits when `cmd.Wait` returns, which happens when pyry exits and its stdout/stderr copiers finish. The FIFO release and the SIGKILL cleanup both guarantee that. Claude runs on a PTY, so the held command does not inherit pyry's pipe write ends and cannot hold `Wait` open.
- **Cleanup order (inherited, not re-established).** The driver registers the SIGKILL cleanup *before* `holdProbeFIFO`, so LIFO releases the FIFO first and pyry gets a real chance to exit on its own. Inverting it has no symptom — a green-looking run in which the rig, not pyry, produced the exit. This rig registers no cleanup that could release the hold, and adds no cleanup before the driver's.
- **Buffers.** `probeSyncBuffer.Bytes()` returns a copy under a mutex, so reading `h.Stderr` here while the copier is still writing is safe, and handing `h.Stdout` to the gather is non-destructive.

---

## Error handling

**Assert almost nothing.** `t.Fatalf` fires only on structural failure, and this rig adds exactly one of its own: `os.MkdirTemp` failing, following the precedent. Every other failure mode is *recorded*, because a probe that turns an unexpected reading into a red test loses the reading.

Three abort paths are **inherited** from the driver and are not re-guarded here: pyry never spawning a claude child, no `system/init` session id, and `ReadJSONL`'s fatal on a transcript it cannot open or parse, reached through the assembly.

Everything else has a named home in a shipped tier and is consumed as returned, never re-derived, renamed, or cross-checked into a new verdict:

| Failure | Where it lands |
|---|---|
| Model issued no Bash call, wrong command, no trigger, no rendezvous, `ps` errored, surprising row count | one of the staging tier's six failure values; the classifier is never consulted |
| pyry did not exit within the deadline | `readings.PyryExited` false → `trailOutcomeVoidPyryDidNotExit` at Step 3, unless Step 2 already found proof |
| Post-trailer scan errored, parsed no rows, or matched nothing | Steps 4, 5, 8 — all below the attribution |
| A liveness read failed as an instrument | Step 6 |
| claude's argv was ambiguous | `indeterminate`, the third agreement answer |
| `json.Marshal` of a logged value failed | logged as a marshal failure; the run continues |
| The artifact writer failed | its own `t.Errorf`; the remaining write still happens |

---

## Testing strategy

**This file ships one offline test, and the entry point is not it.** The entry point needs a live claude and a credentialed worktree; on `make e2e-realclaude` it skips, and a skip is the normal outcome. Its exercise there is compilation and `go test`'s vet subset. Note that `go vet` and `staticcheck` in `make check` run **without** `-tags e2e_realclaude`, so neither analyses this file — `make e2e-realclaude` is the gate.

The one thing worth trapping is `finExitClassify`, because it is the only decision this ticket introduces that is pure and offline-drivable, and because the fourth criterion is entirely about it.

`TestFinExitClassifyConsultsTheClassifierOnlyOnThePassThrough` — table-driven over `finOutcomeValues()`, stdlib `testing` only:

- **One row per failure value** (six of them, ranged from the shipped closed set rather than hand-listed): the returned outcome is that staging value byte for byte, `consulted` is false, and the returned `trailRunOutcome` is the **zero value** — assert `Value == ""`, which `trailIsRunOutcome` rejects, so a reader can look it up and find it is not a member.
- **One row for `finOutcomeReadyToClassify` over `trailRunProofReadings()`**: `consulted` is true and the outcome is `trailOutcomeRunningAtTrailer`.
- **One row for `finOutcomeReadyToClassify` over `trailRunWellFormed()`**: `consulted` is true and the outcome is whatever the shipped classifier returns for that fixture — assert against the value `trailClassifyRun` itself produces, so the row pins the pass-through rather than re-asserting the classifier's own table.

Drive both fixtures through the shipped constructors, never a hand-built `trailRunReadings`: a hand-built one is exactly the fixture the classifier's contract block C1-C9 exists to reject.

Each plausible mis-implementation has at least one row that goes red, and each row is some mis-implementation's sole red:

| Mis-implementation | Red on |
|---|---|
| Classifier consulted unconditionally | all six failure rows (outcome becomes a `trail*` value; `consulted` true) |
| Classifier never consulted | both pass-through rows |
| Staging value returned even on the pass-through | both pass-through rows (outcome mismatch) |
| `consulted` inverted | every row |
| Classifier called on failure rows and only its outcome discarded | the six failure rows, via the zero-`trailRunOutcome` assertion |
| Pass-through gated on the wrong constant | the pass-through rows, and one failure row |

No trap is added for the two constants. `finExitEnableEnv`'s value is asserted by nothing that could drift — it appears in the gate and in the skip message, and a mismatch skips loudly with the name printed. `finExitPyryExitDeadline`'s relationship to `probePyryExitGrace` is an argument, not an invariant; a test asserting they differ would pin a tautology. Neither failure mode has been observed, and a defence for an unobserved failure mode is a cost with no evidence behind it.

**Manual verification, by the operator, on a machine with a Claude login:**

```
PYRY_PROBE_EXIT_PATH=1 go test -tags e2e_realclaude -timeout 15m -v \
  -run 'TestRealClaude_ExitPathWhileCommandRuns' ./internal/e2e/realclaude/
```

The run prints the artifact directory; `run.json` and `run.md` plus the four log lines are what the issue comment is composed from.

**This ticket parks for that run.** The dispatch environment has no Claude login, so the entry point skips and a skip exits 0 — which reads as a false pass. That false green shipped an unverified permission-path change once already (#1168 / PR #1169).

---

## Open questions

1. **Is 120 s enough for the turn to complete?** Sized for a model round-trip plus teardown; unmeasured on this exact prompt shape. A run that voids on `trailOutcomeVoidPyryDidNotExit` with the trailer visibly present in the logged evidence means the deadline was short, not that pyry hung — raise the constant and re-run rather than treating it as a finding.
2. **Does claude ever isolate the backgrounded command into a group pyry's reaper does not walk?** #1230 measured one detached group on all three reps. If the attribution comes back `trailAdmitVoidGroupUnnamed` on a run whose staging passed, that is a reading about the reaper's reach and belongs to #1231, not to this probe.
3. **The follow-up is decided by the reading, not in advance.** If the exit path mis-reports, file a ticket naming what it mis-reports. If it does not, post the explicit note saying so, so the next person does not re-ask.

---

## Security review

**Verdict:** PASS

**Findings:**

- **[Trust boundaries]** No new boundary. Every untrusted input crosses a *shipped* reduction before this rig sees it: model-controlled bytes are reduced by `finLiveAssembleStaging` inside the driver, the trailer is reduced to `finSighting`'s eight scalars inside the gather, and ambient argv is reduced to three integers inside `finRecordBuild`. This rig holds `h.Pin.Rows` (verbatim argv) and `h.Pin.ClaudeCommand` and forwards both to `finRecordBuild` **unexamined and unformatted**; the spec forbids reading, logging, or interpolating either. The one boundary this rig itself decides is § Design step 5's staging gate, which is a comparison against a package constant.
- **[Tokens, secrets, credentials]** MUST-NOT-LEAK channel identified and closed three ways. An operator's `CLAUDE_CODE_OAUTH_TOKEN` / `ANTHROPIC_API_KEY` reaches a public issue only through a process **environment** read or a verbatim argv. (a) The rig runs no `ps` of its own; `pinReadState`'s column set is `pid=,ppid=,stat=` by construction and `ps -E` / `-Eww` are prohibited by name. (b) `finGatherInputs.Pinned` is `[]int` and `h.Pin.PGIDs` is already that type, so no `[]reachProc → .PGID` conversion site exists in this rig — the channel's usual reopening point is structurally absent. (c) `ClaudeVersion` is capped inside `finRecordBuild`, which matters because the version probe's error path interpolates the resolved binary path (an operator home directory). No token is generated, stored, rotated, or revoked by this ticket.
- **[File operations]** Two writes, both through the shipped `finWriteArtifacts` at `0o600`. The artifact directory is `os.MkdirTemp` with a rig-authored pattern — no caller-controlled component, so path traversal and symlink following are structurally inapplicable rather than merely unaddressed. **SHOULD FIX / accepted:** the directory deliberately outlives the test (the operator needs the files), so it persists under `TMPDIR` at the default `0700` `MkdirTemp` mode. That is the shipped precedent's behaviour and the reason it declines `t.TempDir()`; the files hold no secret by the sweep above. Non-atomic writes are fine — a partial artifact is a lost reading, not a corrupted state, and nothing reads these files back.
- **[Subprocess execution]** No `exec.Command` in this rig and no `sh -c`. The two subprocess families it reaches are shipped: `spawnProbePyry` (inside the driver) and `pinReadState`'s `ps -p <pid>`, whose sole caller-supplied operand is an `int` the instrument rejects when non-positive — precisely because `strconv.Itoa(-1)` would reach `ps` as a flag rather than an operand. `h.ClaudePID` is guaranteed positive by the driver's fatal. Environment: the rig inherits `os.Environ()` plus `finLiveStageEnvDelta()`'s two entries and scrubs nothing, which is correct — claude needs the operator's credentials to run at all, and the exposure question is what gets *recorded*, answered above.
- **[Cryptographic primitives]** Not applicable — no randomness, no hashing, no comparison against a secret. The one identity comparison (`h.Staging.Value == finOutcomeReadyToClassify`) is between two package constants, where timing carries no signal.
- **[Network & I/O]** Not applicable — no socket, no listener, no HTTP server, no unbounded read. The only reads are `probeSyncBuffer.Bytes()` (an in-memory buffer the rig's own child fills) and the gather's bounded poll. Every string retained in the record is capped at 512 bytes by `reachCapCommand` inside the shipped builders.
- **[Error messages, logs, telemetry]** The highest-risk surface in this ticket, because the fifth criterion *requires* a new log channel. Closed by restricting it to two values with passing byte sweeps (`TestTrailRunOutcomeCarriesNoCapturedBytes`, `TestFinOutcomeResultCarriesNoCapturedBytes`), by mandating field-by-field or `json.Marshal` rendering, and by banning `%v` on a struct or slice — which is the concrete mechanism by which a `%v` on `h.Pin.Rows` would print every matched row's full argv from a line that reads as ordinary debug formatting. The finding sentence is restricted to counts, closed-set values and `terminal_reason`; `stop_reason` and every sub-record `Detail` are excluded. The `t.Fatalf` this rig adds names only a directory-creation error. No telemetry, no metrics.
- **[Concurrency]** One finding, addressed in the design rather than left to the developer. Reading `h.ExitStatus` outside the `<-h.PyryExited` receive is a data race with the driver's `cmd.Wait` goroutine — and it is the *natural* shape (hoist the value, pass it to a helper), which is why § Design step 2 mandates the read inside the arm and explicitly rejects the helper. `pinExitStatusUnknown` initialisation makes the unwritten value point the safe way as defence in depth, and the field's doc states the ordering obligation. No locks are taken, so lock ordering is not applicable; the shutdown path is the driver's inherited LIFO cleanup chain, and no goroutine is leaked because the only one exits when `cmd.Wait` returns.
- **[Threat model alignment]** Not a relay or network ticket. The governing threat here is the family's own standing one — *artifacts go into a public issue* — which is what every finding above is scoped against. Out of scope and named: the headless `PYRY_USE_STREAMJSON=1` path (#1237), teardown reaping and what survives pyry's exit (#1231), turn accounting (#1234), the interactive turn-state surface (#1227), and `process_pin_liveness_test.go:603-613`'s known guard-quality gap (#1235, recorded in `docs/knowledge/codebase/1235.md`).

**Reviewer:** architect (self-review per `architect/security-review.md`)
**Date:** 2026-08-05
