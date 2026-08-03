# #1268 — Probe instrument: prove the trailer classifier flips inside its own rig against real processes (offline)

## Files to read first

Everything this rig needs already exists. Nothing below is edited — read to call.

- `internal/e2e/realclaude/trail_run_outcome_test.go:177-224` — `trailRunReadings`, field by field. This is the record the rig must fill; the doc comment on each field says which values its producer can emit and which zero value is safe.
- `internal/e2e/realclaude/trail_run_outcome_test.go:366-473` — the nine contract checks. C2, C3/C4, C6, C8/C9 all bite this rig. Read them before writing the gather helper, not after it fails.
- `internal/e2e/realclaude/trail_run_outcome_test.go:476-596` — the eight decision steps. Step 1 (gate) short-circuits before the argv steps; step 3 (`PyryExited`) voids everything below it. Both are why the rig must stage a usable trailer and `PyryExited = true`.
- `internal/e2e/realclaude/trail_run_outcome_test.go:641-647` — the one existing row built end to end by the real producers (`trailGate(trailScan(...))` and `trailAdmitAttribution(tdnClassifyReapLog(...), realGate.Reason)`). The gather helper is this composition, live.
- `internal/e2e/realclaude/result_trailer_observation_test.go:206-262` — `trailWaitForTrailer`: the stamping order, `lastMiss`, and the two `BoundFrom` arms. Lines 255-262 are the exact branch AC4 exists to pin.
- `internal/e2e/realclaude/result_trailer_observation_test.go:125-138` — `trailObservation`. Note it embeds `trailScanResult`; `obs.trailScanResult` is what you hand `trailGate`.
- `internal/e2e/realclaude/result_trailer_observation_test.go:282-291` — `trailFixtureTrailer`, `terminal_reason: "completed"`. The line to seed the rig's stdout buffer with.
- `internal/e2e/realclaude/trailer_admissibility_test.go:242-300` — `trailGate`'s arms, so you can predict `trailGateUsable` + `Reason: "completed"` rather than discover it.
- `internal/e2e/realclaude/trailer_admissibility_test.go:356-452` — `trailAdmitAttribution`: the four contract checks (including the empty-`certified` one) and the `tdnReapNoLine` → `trailAdmitVoidNoLine` arm this rig lands on.
- `internal/e2e/realclaude/teardown_liveness_test.go:144-197` — `tdnClassifyReapLog`. Two facts the rig depends on: the `heldPGID <= 1` guard returns `tdnReapInstrumentFailed`, and `LineCount == 0` returns `tdnReapNoLine`.
- `internal/e2e/realclaude/process_pin_liveness_test.go:191-198` — `pinScanArgv`: zero-`pinScan`-on-error, and the two counts the readings carry.
- `internal/e2e/realclaude/process_pin_liveness_test.go:204-219` — the four per-pid values, and why the zombie/reaped pair is never collapsed.
- `internal/e2e/realclaude/process_pin_liveness_test.go:275-300` and `:332-438` — `pinReadState` and `pinClassifyState`'s ten-branch order. Branch 8 (`pinIsZombie`, `state[0] == 'Z'`) and branch 5 (exit non-zero, empty stdout *and* empty stderr) are the two AC3 asserts.
- `internal/e2e/realclaude/process_pin_liveness_test.go:222-232` — `pinStateColumns` and its never-add-`command` prohibition. The rig does not touch this; read it so you don't try to.
- `internal/e2e/realclaude/background_reach_probe_test.go:884-928` — `reachMatchArgvRows`: matching is against the **whole** command line and the cap is applied *after* matching. This is why a FIFO path works as a needle and why a shell wrapper matches alongside its child.
- `internal/e2e/realclaude/background_trigger_probe_test.go:663-720` — `holdProbeFIFO`: the rendezvous channel, the persistent write end, and the cleanup that releases it. Read the cleanup carefully — its ordering constrains the rig (§ Concurrency model).
- `internal/e2e/realclaude/background_trigger_probe_test.go:722-748` — `probeSyncBuffer`, and `probePollInterval` at `:131`.
- `internal/e2e/realclaude/fifo_reader_liveness_test.go:125-180` — `fifoLiveRead` and its fd-direction argument; also `TestFIFOReaderLiveness_FlipsAcrossOneReaderLifetime:270-341`, which is the closest existing rig to staging A and the shape to mirror.
- `docs/specs/architecture/1251-teardown-liveness-probe.md` — how this family stages a FIFO subject and records a before/after pair.
- `docs/knowledge/codebase/1230.md`, `docs/knowledge/codebase/1235.md` — why a half-run instrument publishing an absence is treated as a measured defect.

## Context

#1266 shipped the trailer observation and its lateness bound. #1270 shipped the two admissibility results. #1271 shipped `trailClassifyRun`, an eleven-valued pure classifier. All three are proven against fixtures and synthetic buffers, and that is the gap: **a pure classifier will happily classify inputs gathered from somewhere other than where the probe claims to gather them.** A rig wired to the wrong path emits the same positive as a rig wired to the right one.

What closes it is a *pre-subject negative* followed by *in-rig transitions*, all through the code path the live probes will use. This ticket builds exactly that rig, offline: a real FIFO, a real `cat`, a synthetic stdout buffer, and no pyry and no claude at all.

Two consequences of what actually shipped, load-bearing throughout:

- **`trailOutcomeRunningAtTrailer` is unreachable here.** It comes from step 2 alone, gated on `trailAdmitProof`, which requires a reap line. This rig stages none and must not grow one. A live `cat` therefore lands on `trailOutcomeMatchedUnattributed` — the correct answer for a match with a silent attribution.
- **The flip is driven by `MatchCount`, not by liveness.** Step 8 (`trailOutcomeNoRowMatched`) versus step 7 (`trailOutcomeMatchedUnattributed`). Per-pid verdicts are corroboration and never move the outcome; only `pinStateInstrumentFailed` is tested at all (step 6).

## Design

### One new file, one prefix

`internal/e2e/realclaude/trail_run_rig_test.go`, build tag `e2e_realclaude`, package `realclaude`. Purely additive — no existing file is edited and no existing call site changes.

Every new identifier takes the `trailRig` prefix. It is in the `trail` family (it composes that family's producers) and it is the rig rather than the instrument, and the compound says both. The sibling prefixes in this package (`tdn`, `pin`, `reach`, `fifo`, `probe`) are untouched.

Run line for the header, matching the sibling's convention:

```
go test -race -tags e2e_realclaude -run '^TestTrailRig' -v ./internal/e2e/realclaude/
```

**Header budget: ~40 lines, not ~90.** The 60-90-line doctrine headers on `trail_run_outcome_test.go` and `trailer_admissibility_test.go` earn their length because each ships a new closed value space that a future edit could quietly widen. This file ships no constants, no outcome set and no classifier — it is a rig over shipped producers. Its header needs to say four things and stop: what the rig proves that fixtures cannot; that `trailOutcomeRunningAtTrailer` is deliberately unreachable; the two staged values and why; and the reap-line prohibition. Anything beyond that duplicates a sibling's header, and a duplicated doctrine is one that can drift.

### The one seam: `trailRigGather`

The entire rig funnels through one function, so "no field is hand-assigned" is checkable in one place rather than argued across four call sites.

```go
// trailRigGather runs every producer this rig can run and returns the readings
// they emit, plus the trailer observation they were derived from. The
// observation is a RIG-LOCAL intermediate — it is never assigned into
// trailRunReadings (which has no field that could hold it) and never published.
func trailRigGather(stdout *probeSyncBuffer, needles []string) (trailRunReadings, trailObservation)
```

Two parameters, because two things genuinely vary across the four call sites. Everything else is a constant of the rig and is documented inside the function rather than repeated at each call.

Order of operations inside, and why each step is the shape it is:

1. `obs := trailWaitForTrailer(stdout, trailRigTrailerWait)` — blocks until the trailer is visible or the deadline fires. `readings.BoundFrom = obs.BoundFrom`.
2. `readings.Gate = trailGate(obs.trailScanResult)` — the embedded scan result, reused rather than re-scanned. This is also the composition a live probe performs, so the rig exercises it rather than approximating it.
3. `scan, err := pinScanArgv(needles, nil)` → `ArgvScanErrored = err != nil`, `MatchCount = scan.MatchCount`, `RowsScanned = scan.RowsScanned`. No exclusions: this rig excludes nothing, and `nil` says so more clearly than an empty map.
4. One `pinReadState(m.PID)` appended to `readings.Liveness` for each `m` in `scan.Matches`. This is what makes AC5's per-matched-pid read true by construction rather than by a separate loop at one call site.
5. `reap := tdnClassifyReapLog(nil, trailRigHeldPGID())` — the reap-line-free buffer is a literal `nil` **inside** this function, not a parameter. There is no pyry in this rig, so there is no stderr to pass; making it un-passable is what makes "no reap line was synthesised" a property of the file rather than a promise about its call sites.
6. `if readings.Gate.Reason != "" { readings.Admit = trailAdmitAttribution(reap, readings.Gate.Reason) }` — **the guard is mandatory.** C4 rejects a non-empty `Admit` under a gate that certifies nothing, and C3 rejects a zero `Admit` under one that does. Calling the predicate unconditionally fails C4 on every non-usable gate; skipping it unconditionally fails C3 on every usable one.
7. `readings.PyryExited = true` and `readings.ClaudeState = ""`, with the reason in a comment beside each. These are the only two staged values in the file.

Nothing else is assigned. No `tdnReapOutcome`, `trailGateResult` or `trailAdmitResult` appears as a struct literal anywhere in the file.

### The rig's constants

- `trailRigTrailerWait = 10 * time.Second` — the wait's timeout. Exceeds the induced lateness by ~6×, so AC4's deadline arm cannot fire.
- `trailRigInducedLateness = 8 * probePollInterval` (1.6 s) — AC4's induced gap. The AC floor is 5 intervals; 8 buys the assertion margin argued in § AC4 below.
- `trailRigRendezvousWait = 10 * time.Second` — matches `fifoLiveRendezvousWait`.
- `trailRigDeathWait = 10 * time.Second` — deadline on the poll to the zombie state.
- `trailRigHeldPGID()` returns `syscall.Getpgrp()`, the test process's own group.

**Why one pgid for every reading.** This rig runs no reaper, so no group is "held" in the sense `tdnClassifyReapLog` means. The value only has to be one that function can classify: its `heldPGID <= 1` guard returns `tdnReapInstrumentFailed`, which would route the attribution to `trailAdmitVoidInstrument` instead of the honest `trailAdmitVoidNoLine`. Using the *same* group for the pre-subject and during-subject readings is the point: a negative reading must differ from the positive one in exactly the dimension under test, and here that dimension is the argv match count and nothing else.

### The two staged values

`PyryExited = true` is not cosmetic. Step 3 voids the whole record when it is false, so a rig that left it at its zero value would land every reading on `trailOutcomeVoidPyryDidNotExit` and AC2's flip would be structurally invisible — the two readings would agree, and the test would look like it had disproven the thing it was built to prove. The staged `true` is honest: there is no pyry in this rig to fail to exit.

`ClaudeState = ""` because no claude runs. C7 admits `""` explicitly ("not read"), and the classifier tests `ClaudeState` on no arm at all.

### Why the stdout buffer must carry a usable trailer

Step 1 answers before any argv step. A rig whose gate is `trailGateNoTrailer` short-circuits to `trailOutcomeVoidNoTrailer` on both readings and the flip never happens. So staging A seeds its `probeSyncBuffer` with `trailFixtureTrailer + "\n"` before the first gather, landing on `trailGateUsable` with `Reason: "completed"`. A budget-fired reason would divert step 1 to `trailOutcomeVoidBudgetFired` instead; `trailFixtureTrailer` is the right fixture precisely because it is ordinary.

### Two stagings, not one

**Staging A** (AC1, AC2, AC3) launches the subject directly: `exec.Command("cat", fifoPath)`. The subject is a direct child of the test process, which is what makes AC3's pair deterministically producible — `Kill()` leaves a zombie the test has not reaped, and `Wait()` reaps it.

**Staging B** (AC5) launches through a shell so two rows carry the needle. The subject is then a *grandchild*: `cmd.Wait()` on the shell does not reap the `cat`, so AC3's second reading would not be deterministic here. Splitting the stagings keeps each AC's claim exact instead of making one rig serve two incompatible parent relationships.

### Staging B's shell form

Two hazards, both worth naming because each costs turns to rediscover:

- **The shell must actually fork.** `sh`, `dash`, `bash` and `zsh` all exec-optimise `sh -c '<single simple command>'`, replacing the shell with the command and leaving *one* process — and one matching row, failing AC5's `MatchCount > 1`. Appending a builtin defeats it: with `cat …; exit 0`, `cat` is no longer the last command and must be forked.
- **The path is not interpolated into the command string.** Use `exec.Command("sh", "-c", `cat "$1"; exit 0`, "sh", fifoPath)`. The path arrives as a positional parameter, so no shell metacharacter in it is ever interpreted. `ps` still prints it — it is `argv[3]` of the shell — so the needle matches the shell's row as required. See § Security review, category 4.

### The rig's failure messages

Rig assertions name counts, pids, verdicts, outcome values and `BoundFrom`. They never print `pinScan.Matches` (verbatim argv from the ambient process table) and never print an observation's `Line` or `Trailer`. This is the same content rule `trailRunOutcome.Detail` carries, applied to the rig's own `t.Fatalf` strings — the place a future edit most easily breaks it.

## Test plan

Three test functions. Scenarios, not code — the developer writes them in the package's idiom.

### `TestTrailRigFlipsAcrossOneSubjectLifetime` — AC1, AC2, AC3

- Create `t.TempDir()`; the FIFO path inside it is the **needle**. It is unique per run and no process's command line carries it before the subject exists — including the test binary's own, whose argv is `-test.run=…` and nothing more.
- Seed a `probeSyncBuffer` with `trailFixtureTrailer + "\n"`.
- `rendezvous := holdProbeFIFO(t, fifoPath)`.
- **Pre-subject gather.** Assert, in this order:
  - `readings.ArgvScanErrored` is false, and fail loudly if not — an errored scan lands on `trailOutcomeVoidArgvScanErrored`, and treating that void as the negative would be the rig proving nothing while looking green.
  - `readings.RowsScanned > 0`. This is what separates the reading from `trailOutcomeVoidNoRowsParsed`.
  - `readings.MatchCount == 0`.
  - `readings.Gate.Value == trailGateUsable` and `readings.Gate.Reason == "completed"`; `readings.Admit.Value == trailAdmitVoidNoLine`. These two prove the gate and attribution legs genuinely ran through their producers, rather than the flip being an accident of a short-circuiting void.
  - `trailClassifyRun(readings).Value == trailOutcomeNoRowMatched`.
- Launch `exec.Command("cat", fifoPath)` with an empty `Env` (§ Security review, category 4). Record `subjectPID := cmd.Process.Pid`.
- Wait on `rendezvous` under `trailRigRendezvousWait`; `t.Fatalf` on expiry naming the deadline.
- `fifoLiveRead(fifoPath).Verdict == fifoLiveReaderPresent`. The rendezvous proves a reader *arrived*; this proves one is *still* holding the read end at scan time. Safe here only because `holdProbeFIFO`'s persistent write end means this call's transient second write end is not the last one closing.
- **During-subject gather**, same needle, same buffer. Assert:
  - `readings.MatchCount > 0` and `readings.RowsScanned > 0`.
  - `len(readings.Liveness) == readings.MatchCount`.
  - `trailClassifyRun(readings).Value == trailOutcomeMatchedUnattributed`.
  - The two outcome values differ. State this as its own assertion, not as an inference from the two above — it is the rig's actual claim.
- **AC3, reading one.** `cmd.Process.Kill()`, then poll `pinReadState(subjectPID)` until it settles, under `trailRigDeathWait`:
  - `pinStateExitedNotReaped` → done, this is the recorded reading.
  - `pinStateRunning` → retry. `Kill()` is asynchronous; the pre-signal state is expected.
  - `pinStateInstrumentFailed` → `t.Fatalf` immediately. Retrying past an instrument failure is exactly the spin-until-green shape that turns a broken instrument into a pass.
  - `pinStateNoSuchProcess` → `t.Fatalf` immediately. The test is the parent and has not waited, so this is unproducible; reaching it means the rig has lost the parent relationship its whole AC3 claim rests on.
  - Deadline expiry → `t.Fatalf` naming the last verdict seen.
- **AC3, reading two.** `cmd.Wait()`, then `pinReadState(subjectPID)` **once**. Assert `pinStateNoSuchProcess`. No poll: `Wait` is synchronous with reaping, so a retry loop here would only be able to hide a defect.
- Assert the two dead verdicts differ from each other and neither is `pinStateRunning` or `pinStateInstrumentFailed`.
- Do **not** assert the argv match set at either dead reading. `ps` renders a zombie's `command=` without the original argv (`<defunct>`, `[cat] <defunct>`), and which form a platform prints is not this ticket's claim.

### `TestTrailRigBoundIsMeasuredFromTheLastMiss` — AC4

No FIFO and no subject. A `probeSyncBuffer` and the gather helper.

- Empty buffer. Needle: a string that matches nothing (e.g. the test's `t.TempDir()` path, with no process staged under it).
- Stamp `spawnedAt`, then run `trailRigGather` in a goroutine sending to a **buffered** channel (cap 1), so the goroutine cannot leak on a send if the test has already failed and returned.
- `time.Sleep(trailRigInducedLateness)`; stamp `appendedAt`; append `trailFixtureTrailer + "\n"` to the buffer.
- Receive the result; `induced := appendedAt.Sub(spawnedAt)`.
- Assert `obs.State == trailSeen` and `obs.BoundFrom == trailBoundFromMiss`. This is the deterministic half — the start-derived reading the assertion exists to catch reports `trailBoundFromStart` and fails here outright.
- Assert `obs.Staleness < induced - 2*probePollInterval`.

  **Why the margin, and why the bare `< induced` is not enough.** A start-derived staleness is `now - start`, where `now` is the successful poll's stamp (at or after `appendedAt`, by up to one poll interval) and `start` is stamped inside the wait (at or after `spawnedAt`, by microseconds). So the buggy value sits within roughly one poll interval of `induced` on *either* side, and `Staleness < induced` would pass under the bug whenever goroutine scheduling exceeded the post-append poll granularity. A correct miss-derived staleness is at most one poll interval plus a scan. With `induced` at 8 intervals, a threshold at 6 leaves a correct reading ~5 intervals of headroom and the buggy one ~2 intervals the wrong side of the line. The assertion strictly implies AC4's "strictly less than the lateness the rig induced".
- Free corroboration, one line: `trailIsRunOutcome(trailClassifyRun(readings).Value)` is true and the value is not `trailOutcomeOutOfContract`. This proves the composed gather emits a contract-legal record on the miss path too, not only on the pre-seeded one.

### `TestTrailRigCarriesMoreThanOneMatchedRow` — AC5

- Same FIFO staging as A: `t.TempDir()`, `holdProbeFIFO`, buffer seeded with `trailFixtureTrailer + "\n"`.
- Launch `exec.Command("sh", "-c", `cat "$1"; exit 0`, "sh", fifoPath)` with `SysProcAttr{Setpgid: true}` and an empty `Env`.
- `defer` — not `t.Cleanup` — a `syscall.Kill(-pgid, syscall.SIGKILL)` followed by `cmd.Wait()`. See § Concurrency model for why the ordering is load-bearing.
- Wait on `rendezvous`: it fires when the `cat` opens the read end, which is also what guarantees the `cat` has execed and carries the needle in its argv.
- Gather. Assert:
  - `readings.MatchCount > 1`.
  - `len(readings.Liveness) == readings.MatchCount` — one `pinReadState` per matched pid. The classifier deliberately has no length-alignment check (`trail_run_outcome_test.go:300-303`), so the rig is where this belongs.
  - Every entry in `readings.Liveness` carries a `pinIsVerdict` value and none is `pinStateInstrumentFailed`.
  - `trailClassifyRun(readings).Value == trailOutcomeMatchedUnattributed` — a match count above one does not resolve to "the" pid and does not change the outcome.

### AC5's PASS-not-SKIP verification

Not code. Run and read:

```
make e2e-realclaude   # first: the target as it ships
go test -tags e2e_realclaude -run '^TestTrailRig' -v ./internal/e2e/realclaude/
```

Grep the verbose output for **both** `--- PASS` and `--- SKIP` against each new test name. The whole package exits 0 in a few seconds when its credential-gated specs all skip, so exit 0 alone does not distinguish "ran and passed" from "skipped". Three `--- PASS` lines and zero `--- SKIP` lines for `TestTrailRig*` is the evidence; record it in the PR body.

Also run `go test -race -tags e2e_realclaude -run '^TestTrailRig' ./internal/e2e/realclaude/` — the rig spawns goroutines around a shared buffer and `-race` is the point of `probeSyncBuffer` existing.

## Concurrency model

Three goroutines are in play, and each has a named exit.

1. **`holdProbeFIFO`'s hold goroutine.** Parks in `open(path, O_WRONLY)` until a reader arrives, then waits on `release`. Its `t.Cleanup` closes `release` and, if no reader ever came, opens the read end non-blockingly to unpark it. Owned entirely by the existing helper; the rig adds nothing.

2. **AC4's gather goroutine.** Exits when `trailWaitForTrailer` returns — on the appended trailer, or on `trailRigTrailerWait`. The result channel is buffered at 1 so the send cannot block a goroutine whose test has already returned.

3. **The subject process's own lifetime.** Staging A ends it inside the test body (AC3 requires exactly that). Staging B ends it with a deferred group kill.

**The cleanup-ordering trap, stated once because it deadlocks rather than fails.** `t.Cleanup` runs LIFO after the test body, and `holdProbeFIFO` registers its cleanup at call time. A `t.Cleanup(func(){ cmd.Wait() })` registered *after* `holdProbeFIFO` therefore runs *before* the FIFO release — and blocks forever, because the `cat` is still blocked on a FIFO whose write end is still held, waiting for the EOF that only that later cleanup can deliver. Both stagings avoid it the same way: the subject is killed and waited from the test body or from a `defer` inside it, both of which run before any `t.Cleanup`.

**Why staging B needs the process group.** `cmd.Process.Kill()` on the shell does not reach the `cat`. Without `Setpgid` plus `Kill(-pgid, SIGKILL)`, the `cat` is orphaned for the remainder of the test — it does eventually die when `holdProbeFIFO`'s cleanup closes the write end, but until then it is a live process carrying the needle in its argv, and this package's tests run sequentially in one binary. Killing the group closes that window deterministically.

## Error handling

The rig's whole subject matter is instruments that fail without aborting, so the discipline is about which failures are *data* and which are *test failures*.

- **A datum, recorded and classified:** the argv scan's error, a per-pid instrument failure, an absent trailer, an unclassifiable attribution. Every one of these has a named outcome, and the classifier's job is to give it one. The gather helper never inspects them and never fails a test.
- **A test failure, loud and immediate:** `ArgvScanErrored` true at a reading the rig claims is a genuine negative; `pinStateInstrumentFailed` at either AC3 reading; `pinStateNoSuchProcess` before `Wait`; the rendezvous or death deadline expiring; `trailOutcomeOutOfContract` from any gather. Each of these means the rig did not measure what it claims to measure, and reporting it as the negative is the exact defect this family exists to refuse.

The line between the two: a reading the *classifier* is entitled to make is a datum; a reading that would make the rig's own claim unfounded is a failure. `trailOutcomeVoidArgvScanErrored` is a legitimate outcome of the classifier and an illegitimate result for this rig, and both statements are true at once.

## Security review

**Verdict:** PASS

**Findings:**

- **[Trust boundaries]** No MUST FIX. One boundary, and it is narrow: `ps -axww -o pid=,ppid=,pgid=,command=` reads the *ambient* process table, including other users' command lines. Three shipped properties keep it from reaching a record — `reachScanArgv` never lets the raw table leave its frame (`background_reach_probe_test.go:873-881`); the needle is a `t.TempDir()` path unique to the run, so only the rig's own processes match; and `trailRunReadings` carries counts rather than `pinScan.Matches`. The rig adds no fourth path. The one place a developer could reopen it is a `t.Fatalf` that prints `scan.Matches`, which § "The rig's failure messages" forbids explicitly.

- **[Subprocess / external command execution]** Two findings, both addressed in the design rather than deferred.

  *`sh -c` is used deliberately, and AC5 requires it* — the shell wrapper is the mechanism by which two rows carry the needle. Shell interpretation of the path is removed rather than mitigated: `exec.Command("sh", "-c", `cat "$1"; exit 0`, "sh", fifoPath)` passes the path as a positional parameter, so no metacharacter in it is ever interpreted, while `ps` still prints it as `argv[3]` and the needle still matches. The command string is a compile-time constant with no interpolation site at all. The alternative — `fmt.Sprintf("cat %s; exit 0", fifoPath)` — is what a developer types first, and it is a shell-injection shape even though today's only input is `t.TempDir()`.

  *SHOULD FIX — the subjects inherit the operator's environment.* Under `make e2e-realclaude` the test process may carry `CLAUDE_CODE_OAUTH_TOKEN` / `ANTHROPIC_API_KEY`, and `exec.Command` passes the parent environment through by default. Nothing in this design reads any process's environment — both `ps` reads use explicit column lists with no `-E`, no `-e` environment column and no BSD `eww`, and `pinStateColumns` carries an enforcing test against exactly that. So this is defence in depth, not a live exposure. It is also one line: set `cmd.Env = []string{}` on both subjects. `cat` and `sh` need no environment, and `exec.LookPath` resolves the binary from the *parent's* `PATH` before `Env` applies, so the empty environment costs nothing. It makes "this rig's subjects cannot leak the operator's token through any future column addition" true by construction rather than by a sibling file's prohibition. Prescribed in the test plan.

- **[File operations]** No MUST FIX. The FIFO is created by `holdProbeFIFO` at mode `0o600` inside `t.TempDir()` (mode `0700`, removed by the framework). `fifoLiveRead` does `Lstat` then `OpenFile`, a check-then-use pair — but on a path the test itself created inside its own private temp dir, with no other writer, so the swap-during-the-gap window has no actor in it. The mode gate is a positive allowlist on `os.ModeNamedPipe` rather than a blacklist, which is the property that keeps `/dev/null` from reading as a live FIFO. Inherited, not introduced; nothing in this spec widens it.

- **[Error messages, logs, telemetry]** No MUST FIX, and one rule made explicit because it is the one a future edit breaks. The rig writes no artifact and no log; its only operator-visible strings are `t.Fatalf` messages. Those may name counts, pids, verdicts, outcome values and `BoundFrom`, and may not name `pinScan.Matches`, `trailObservation.Line` or `trailScanResult.Trailer`. Today `Line` would only ever hold `trailFixtureTrailer`, a synthetic constant — the rule is stated anyway, because the file's value is that a live probe can be built on it, and at that point `Line` holds verbatim model output marked operator-review-before-paste.

- **[Concurrency]** No MUST FIX. Three goroutines, each with a named exit (§ Concurrency model). The one hazard with teeth is a deadlock rather than a leak — the `t.Cleanup` LIFO ordering against `holdProbeFIFO`'s release — and it is called out with the shape that triggers it. AC4's result channel is buffered at 1 specifically so a failed test cannot strand its gather goroutine on a send. `probeSyncBuffer` is mutex-guarded and its `Bytes()` returns a copy, so the concurrent poll is race-clean by construction; `-race` is in the verification steps rather than assumed.

- **[Tokens, secrets, credentials]** Not applicable, as a design decision rather than an omission: this rig mints, stores, compares and transmits nothing. Its only contact with credentials is the inherited-environment finding above, which is why that one is written up under subprocess execution rather than dismissed here.

- **[Cryptographic primitives]** Not applicable. No randomness is generated. Uniqueness comes from `t.TempDir()`, whose collision resistance is a test-isolation property and not a security one — nothing branches on the path being unguessable.

- **[Network & I/O]** Not applicable. No socket, no listener, no HTTP server, no network read of any kind. The only unbounded read is `bufio.Scanner` inside `trailScan`, deliberately left at its 64 KiB default because raising it only moves the threshold — and the resulting abort is a *named outcome* (`trailGateScanAborted` → `trailOutcomeVoidTrailerScanAborted`) rather than a silent truncation. `ps` output is bounded by `reachPSTimeout` and by `reachCapCommand`.

- **[Threat model alignment]** No relay surface and no daemon surface. The threat this file's family exists against is not an external actor at all — it is an instrument that publishes an absence it never measured. This rig's failure-vs-datum split (§ Error handling) is the alignment: every reading that would make the rig's own claim unfounded is a loud test failure, and only readings the classifier is entitled to make are treated as data.

**Reviewer:** architect (self-review per `architect/security-review.md`)
**Date:** 2026-08-04

## Open questions

- **Does `exec.Command("sh", ...)` resolve to a shell that forks for a non-final command on every CI platform this runs on?** The `cat "$1"; exit 0` form defeats the exec-optimisation in `sh`, `dash`, `bash` and `zsh`, which covers macOS and the Linux runners. If AC5's `MatchCount > 1` fails on a platform anyway, the fallback is a second needle-carrying process staged directly (two `cat`s on two FIFOs) rather than a shell trick — AC5's claim is "more than one row matched and none of them was resolved to *the* pid", and the shell wrapper is one way to produce that, not the claim itself.
- **Should the pre-subject gather assert `trailBoundFromStart`?** It will report exactly that — the buffer is pre-seeded, so the first poll matches and `lastMiss` is zero. Asserting it costs one line and pins that the rig's first reading is honestly unbounded. It is left out of the test plan because no AC asks for it and AC4 covers the discriminator properly; add it if the developer finds it clarifies the file.
- **Sizing note for the record.** No red line tripped: one new file, zero consumer call sites, zero new exported types, zero new outcome constants or reject branches, five ACs. Projected total written work is ~450-550 lines, which is at the S boundary rather than comfortably inside it. It is not splittable at a profit — the fixed cost (header, `trailRigGather`, the FIFO staging) is ~180 lines shared by every AC, and the marginal ACs are 55-120 lines each, so any split duplicates more than it removes. The header budget in § Design is the lever that keeps this inside S; the sibling files' 700-1200-line convention is a classifier-shipping cost this file does not pay.
