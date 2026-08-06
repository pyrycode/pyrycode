# #1349 — Probe rig: stage a live `agent-run` turn on the headless `PYRY_USE_STREAMJSON=1` path (offline)

**Size:** S (confirmed — 3 files, all `*_test.go`; 0 production source files; 1 call site; 0 new exported types; 0 reject branches)

## Files to read first

Generated from `codegraph_context` on the ticket title + AC paraphrase, then pruned and extended by hand with the production-side files codegraph does not surface for a test-only change.

| File / range | What to extract |
|---|---|
| `internal/e2e/realclaude/finding_live_run_test.go:1-71` | The file header. Its first sentence names "the ptyrunner default" and is one of the prose sites AC4 invalidates. |
| `internal/e2e/realclaude/finding_live_run_test.go:199-271` | The driver's doc comment — every paragraph AC4 makes you classify. Especially `:206-213` (NO PARAMETERS BEYOND t), `:248-254` (turn headroom), `:256-264` (no budget-fired run), `:266-270` (the skip-guard conclusion). |
| `internal/e2e/realclaude/finding_live_run_test.go:271-292` | The body's opening: the composite literal whose `EnvDelta:` field is the one line AC3 changes. |
| `internal/e2e/realclaude/finding_live_run_test.go:344-366` | The `cmd.Wait` goroutine and its PTY argument at `:349-352` — the load-bearing finding. |
| `internal/e2e/realclaude/finding_live_run_test.go:462-467` | The `scan.MatchCount` is-3-on-a-healthy-run claim. A fifth prose site, not named by the ticket. |
| `internal/e2e/realclaude/finding_live_staging_test.go:163-191` | `finLiveStageEnvDelta` and its doc — the shape the new sibling copies, and the text that must stay unchanged. |
| `internal/e2e/realclaude/finding_live_staging_test.go:404-489` | `TestFinLiveStageEnvDeltaNamesTheRunner` and `finLiveStageDeltaHas`. The new trap is a sibling of this, NOT a copy — read `:415-427` closely, it is the comment AC2 forbids copying. |
| `internal/e2e/realclaude/finding_exit_path_probe_test.go:209-220` | The one call site. `h := finLiveRunStage(t)` at `:216`. |
| `internal/e2e/realclaude/background_reach_probe_test.go:1091-1113` | `reachRunnerPathFromEnv` — ambient-first read, exact-`"1"` truthiness, and the full-string return the label truncator consumes. |
| `internal/e2e/realclaude/background_trigger_probe_test.go:621-646` | `spawnProbePyry` — `cmd.Env = append(os.Environ(), extraEnv...)` and the two non-`*os.File` output buffers. No `WaitDelay` is set. |
| `internal/e2e/realclaude/finding_run_record_test.go:260-272`, `:430-440` | `finRecordRunnerLabel` (the `" ("` truncator the trap compares through) and `finRecordEnvDelta()` (the `=0` sibling). |
| `internal/agentrun/streamrunner/runner.go:170-205`, `:229-251` | `cmd.Stdout = parser` (`:176`), `cmd.Stderr = cfg.Stderr` (`:177`), `cmd.WaitDelay = killGrace` (`:204`), and `waitErr` flowing out at `:231`/`:250`. `killGrace = 5 * time.Second` at `:44`. |
| `internal/agentrun/ptyrunner/runner.go:294-320`, `:611-625` | `cmd.Stderr = cfg.Stderr` set BEFORE the PTY spawn (`:296`), and `buildArgs` — which omits `--max-turns` and `--allowed-tools`. |
| `cmd/pyry/agent_run.go:258-293`, `:295-333`, `:363-377` | The `PYRY_USE_STREAMJSON == "1"` dispatch, the error→exit mapping at `:271-277`, `Stderr: os.Stderr` on BOTH paths (`:291`, `:328`), and `buildStreamRunnerClaudeArgs` — which DOES emit `--append-system-prompt-file` (`:372`). |
| `$(go env GOMODCACHE)/github.com/creack/pty@v1.1.24/run.go:38-50` | `StartWithAttrs` fills stdin/stdout/stderr with the tty **only when nil**. This is why the PTY does not detach claude's stderr from pyry's. |

Not in codegraph and worth one look each: `docs/knowledge/codebase/1342.md` (why the delta names the variable rather than leaving it unset) and `docs/knowledge/codebase/1337.md` (the 2026-08-06 live run this spec treats as evidence).

## Context

`finLiveRunStage` stages one live `pyry agent-run` turn and hands back a handle. It has one caller, the #1337 exit-path probe, which ran green live against claude 2.1.220 on 2026-08-06. The environment it stages with is hardcoded: the driver reads `finLiveStageEnvDelta()` at `finding_live_run_test.go:290`, and that delta names `PYRY_USE_STREAMJSON=0`.

#1353 wants the same rig pointed at the headless stream path. Today that needs either an edit to the shared delta — which would silently repoint the existing caller — or a second copy of the driver. This ticket makes the delta an input and ships the second delta plus its trap. **No turn is staged and no live claude is involved.**

The expensive part is not the parameter. It is that the driver's doc comment argues its correctness in five places from facts that are true of ptyrunner specifically, and one of those arguments is the only recorded reason `pyryExited` ever closes.

## Design

### 1. The second delta (`finding_live_staging_test.go`)

A new package-level func returning `[]string{"BASH_DEFAULT_TIMEOUT_MS=5000", "PYRY_USE_STREAMJSON=1"}` — this file's own two literals, allocated fresh per call (a func, not a var, for the reason `finLiveStageEnvDelta`'s doc gives at `:186-188`).

It must not be built by mutating, wrapping, copying-and-appending-to, or `slices.Clone`-ing `finLiveStageEnvDelta()`. Two independent literal sets is the point: AC1's property is that an edit to either cannot silently change the other, and any derivation destroys exactly that.

`finLiveStageEnvDelta` is **not renamed, not re-homed, not touched**. Technical Notes forbid it and its own trap reads it at two sites.

**Naming.** `finLiveStageStreamEnvDelta`. The prefix `finLiveStageStream` is verified free; measured at `250e998` with the ticket's own recipe:

```
git grep -o -h "<prefix>[A-Za-z0-9_]*" 250e998 -- '*.go' | wc -l
```

| Prefix | Occurrences | Distinct identifiers | Role |
|---|---|---|---|
| `finLive` | 178 | 27 | reproduces the ticket's re-derived count exactly |
| `finRecord` | 196 | 22 | present-prefix control |
| `finZzzNotAPrefix` | 0 | 0 | absent-prefix control |
| `finLiveStageStream` | **0** | **0** | the prefix this ticket claims |

The Technical Note's operative constraint is *free and verified*, and `finLiveStageStream` is both. It sits inside the `finLiveStage*` declaration family deliberately: the same note forbids renaming `finLiveStageEnvDelta`, and the two deltas have to be adjacent in one namespace for a reader to compare them at all — which is what AC1's independence property is read against. Do not invent a disconnected prefix for one function.

Its doc comment states, in this order: the two entries and what each is for; that the `=1` entry is explicit for the *same* reason the `=0` one is (`reachRunnerPathFromEnv` reads the ambient first at `background_reach_probe_test.go:1103`, so an empty delta reads the operator's shell); that the entries are this file's literals and not a composition; and the permission-posture consequence (§ 4, site F) with a pointer to the driver's doc rather than a second copy of the argument.

### 2. The trap (`finding_live_staging_test.go`)

One new test, sibling to `TestFinLiveStageEnvDeltaNamesTheRunner`, never a copy of it. Not `t.Parallel()` — it calls `t.Setenv`.

**The hostile ambient is `PYRY_USE_STREAMJSON=0`.** That choice does the real work, and the reasoning belongs in the test's comment:

- `reachRunnerPathFromEnv` flips to true only on an exact `PYRY_USE_STREAMJSON=1` delta entry (`:1105-1107`), and the last matching entry wins. So a `streamrunner` reading **under an ambient of `0`** proves the delta carries that exact entry with no later entry contradicting it. An empty, absent-key, or `=0`-carrying delta all read `ptyrunner` here. The claim therefore pins "names the variable explicitly, with value 1" by itself — it does not need a separate containment assertion to do it.
- The control (nil delta must read `ptyrunner`) is what the test can still get from the ambient, and its comment must state both halves honestly:
  - **excludes:** an *effective* ambient of `1` — under which the nil delta reads `streamrunner` and the control goes red before the claim can be satisfied by the environment rather than by the delta.
  - **cannot establish:** non-vacuity by construction. Only the exact string `"1"` is truthy, so an ambient of `0` is indistinguishable from unset and this control passes identically if `t.Setenv` never ran. The shipped trap's argument at `finding_live_staging_test.go:415-427` turns on its ambient being the *non-default* value; that argument does not transfer and **must not be copied across**. Copying it ships a false claim.

Assertions, as scenarios (the developer writes them in the file's idiom — `t.Fatalf` for the control, `t.Errorf` for the rest, following the shipped trap):

- **Control.** `finRecordRunnerLabel(reachRunnerPathFromEnv(nil))` is `ptyrunner` under the ambient. Fatal — everything below is uninterpretable without it.
- **The claim.** `finRecordRunnerLabel(reachRunnerPathFromEnv(finLiveStageStreamEnvDelta()))` is `streamrunner`. Compared through `finRecordRunnerLabel` and never against the raw return: `reachRunnerPathFromEnv` returns `"streamrunner (headless stream-json)"` (`:1110`), so a raw comparison is red against a correct implementation. Do not grow a second truncator.
- **The trigger survives the flip.** Every entry of `reachEnvDelta` is carried, asserted through `finLiveStageDeltaHas` against the shipped identifier — same shape and same reason as `:454-459`.
- **The two deltas disagree, exactly.** Every entry of `finRecordEnvDelta()` (i.e. `PYRY_USE_STREAMJSON=0`) is **absent** from the new delta. `finLiveStageDeltaHas` is an exact `KEY=VALUE` match (`:479-489`), which is what makes this bite — a prefix test would accept either value. This is the assertion that makes an accidental copy of the shipped delta red.
- **Exactly 2 entries**, with the shipped trap's recorded failure mode restated for this delta: a third entry that suppressed the auto-background trigger makes every run report `finOutcomeTriggerDidNotFire`.

**Copy-paste between the two traps goes red, structurally**, because both the ambient *and* the expected label flip: the shipped trap sets `1` and wants control=`streamrunner`/claim=`ptyrunner`; this one sets `0` and wants control=`ptyrunner`/claim=`streamrunner`. Any half-copy contradicts itself on the very first assertion. State that property in the new test's doc so the next editor knows the redness is designed rather than incidental.

The shipped trap is not edited. Both run in the same package, sequentially, and `t.Setenv` restores on cleanup.

### 3. The parameter (`finding_live_run_test.go`, `finding_exit_path_probe_test.go`)

Signature becomes `finLiveRunStage(t *testing.T, envDelta []string) *finLiveRunHandle`. The `EnvDelta:` field of the composite literal at `:290` takes `envDelta`; the value continues to reach `spawnProbePyry` through `h.EnvDelta` at `:341`, unchanged. No defaulting, no nil-check, no validation, no package-level fallback — a nil delta must read the ambient, which is precisely the failure `reachRunnerPathFromEnv`'s doc exists to make visible, and a silent default would hide it.

The one call site becomes `finLiveRunStage(t, finLiveStageEnvDelta())`. Nothing else changes there — and that includes its prose. `finding_exit_path_probe_test.go:81-89` argues at length that the reach probe's `PYRY_USE_STREAMJSON=1` gate is not inherited because `finLiveStageEnvDelta` names the variable explicitly, and `:188`'s skip message names both entries. Both stay TRUE precisely because this caller keeps passing the `=0` delta. **Verify them, do not edit them** — an "update" here would be the churn the Technical Notes warn about, and `:86-87` is the citation site A reuses.

**No failure message on the driver side may print `envDelta`.** The driver is newly holding a caller-supplied environment slice, and a caller can put anything in it; the two shipped `t.Fatalf`s print a deadline and pyry's stderr, the one `t.Logf` names a duration, and that stays exactly true. Adding `envDelta=%v` for debuggability would put a caller-supplied environment into test output on a rig whose process environment carries `CLAUDE_CODE_OAUTH_TOKEN` / `ANTHROPIC_API_KEY`.

**AC3's byte-identical claim is review-held**, and the review is a concrete field-for-field check, not a nod. The driver cannot run without a Claude login, so no offline suite can execute it; the discharge is that `finLiveStageEnvDelta()`'s contents are already pinned by the shipped trap at `finding_live_staging_test.go:441-476` — two entries (`:472`), the `reachEnvDelta` containment (`:454-459`), the `finRecordEnvDelta()` containment (`:460-465`) — and this ticket does not touch that function or that trap. Do that comparison yourself, name those three line ranges, and cite the review in the commit message. Do not add a skipping live test to "cover" it.

### 4. The doc-comment re-derivation (`finding_live_run_test.go`)

AC4's test is **whether a site's recorded justification cites the runner path**, not whether code branches on it. Nothing in the driver branches, so a branch-shaped reading answers "none" — and the answer is at least six. Classify every wait, rendezvous, gate and default individually. The table below is the classification this spec did; the developer verifies it against the code rather than transcribing it, and records the outcome on the driver.

#### Delta-independent (conclusion and reason both survive)

| Site | Why it survives |
|---|---|
| `WithWorktreeAuthenticated` / `resolveClaudeBin` skips (`:274-277`) | Credentials and binary resolution; no runner involved. |
| `holdProbeFIFO` rendezvous + `probeRendezvousDeadline` (`:329`, `:380-387`) | The FIFO is created by the rig and opened by claude's Bash child. Claude's behaviour, identical on both paths. |
| `probeWaitForDirectChild` (`:368`) | Claude is a direct child of pyry on both paths (`ptyrunner/runner.go:294` + `tuidriver.Spawn`; `streamrunner/runner.go:174`). The reach probe's skip guard (`background_reach_probe_test.go:296-302`) is NOT precedent here: it skips because *its* pin is content-first on `--session-id`, which the stream path's argv lacks. This driver resolves claude's pid by tree walk and pins on the FIFO needle, so that reason does not reach it. Say so — it is the guard a reader will expect to find inherited. |
| The pin: needles, exclusions, `finLivePinWantRows` (`:448-467`) | The matched rows are the `zsh -c` wrapper and the `cat`, which claude isolates into one detached group on both paths. The count of 2 is a claude-behaviour measurement, not a runner one. |
| `scan.MatchCount == 3` (`:464-465`) | **Re-derived, not inherited.** It holds because `tdnClaudeNeedle` is `--append-system-prompt-file` (`teardown_liveness_probe_test.go:160`) and BOTH argv builders emit it — `ptyrunner/runner.go:621` and `cmd/pyry/agent_run.go:372`. Do not rest this on `reachRunnerPathFromArgv`'s framing of that flag as "the ptyrunner-shape marker" (`background_reach_probe_test.go:1117`); `teardown_liveness_probe_test.go:894` and `finRecordFixtureNeitherArgv`'s doc both record correctly that it names no runner. Cite the two builders. |
| `Pin.ClaudeCommand` (`:185-189`) | Follows from the row above: claude's row carries the needle on both paths, so the field is populated on both, and its empty case stays the ambiguity the field's doc already describes. |
| The `finLiveAssembleStaging` transcript reads (`:474-480`) and `probeWaitForBashToolUse` (`:420`) — the *directory* half | The rig computes the path as `jsonlPathFor(workdir, sessionID)` → `tuidriver.SessionJSONLPath(home, workdir, sid)`, from the workdir *the rig* owns, on both paths. One asymmetry to record: pyry hands claude `realpath` (trust-marked, `agent_run.go:300`/`:318`) on ptyrunner and the raw `parsed.workdir` (`streamrunner` `cmd.Dir`) on the stream path. The rig's own derivation uses the raw workdir, so if the two ever diverge the stream path is the *closer* match — the conclusion holds a fortiori. The ptyrunner side is the one carrying empirical proof (2026-08-06 green). The *filename* half is delta-dependent — site G. |

#### Delta-dependent (the recorded justification cites the runner path)

**Site A — `:266-270`, the skip guard.** Asserts `PYRY_USE_STREAMJSON=0` is in the delta *by name* and concludes no skip guard is needed. Now false as written: the driver no longer knows which delta it got. Re-derive for both callers — the conclusion still holds, and for a *stronger* reason: whichever delta a caller passes names the variable explicitly and `os/exec` dedups in favour of the later value (`spawnProbePyry`'s `cmd.Env = append(os.Environ(), extraEnv...)` at `background_trigger_probe_test.go:637`), so the ambient loses either way. The reach probe's guard (`background_reach_probe_test.go:296`) exists because *its* delta does not name the variable; that contrast is worth keeping. State it as a **requirement on the caller's delta** — it must name `PYRY_USE_STREAMJSON` explicitly — not as a fact about one value. That requirement is the driver's half of the new trust boundary AC3 opens (§ Security review, finding 1).

Two halves, pinned in two different places, and the comment must not blur them. The rig's own env-side precedence (ambient read first, delta entries override, last entry wins) is `reachRunnerPathFromEnv`'s and is pinned offline by AC2's trap. **`os/exec`'s last-wins dedup on `cmd.Env` is stdlib behaviour that nothing in this package exercises** — no test here spawns a process, and AC2's trap runs against the rig's model rather than against `exec.Cmd`. It is true (`exec.Cmd` dedups `Env` keeping the last occurrence) and the shipped call-site prose already relies on it at `finding_exit_path_probe_test.go:86-87`, so cite that rather than re-deriving — but say which half is trap-pinned and which is inherited. Presenting the second as pinned here is the false-provenance failure this file's discipline exists to prevent.

**Site B — `:349-352`, the `cmd.Wait` PTY argument. This is the finding, and the two legs must be separated.**

*Leg 1 — the rig's `cmd.Wait` (what the sentence is actually about).* The rig's Wait returns when pyry has exited **and** both copy goroutines see EOF on the rig-created stdout/stderr pipes — i.e. when every duplicate of those two write ends is closed process-wide. `spawnProbePyry` sets two `probeSyncBuffer`s and no `WaitDelay`, so a held write end blocks it forever.

Who can hold one? Claude's fd 1 is never the rig's stdout write end on either path (PTY slave on ptyrunner; a *pyry-created* pipe on the stream path, `streamrunner/runner.go:176`). But claude's fd 2 **is** pyry's fd 2 — the rig's stderr write end — **on both paths**, because `cmd.Stderr = os.Stderr` at `agent_run.go:291` (stream) and `:328` (pty), and creack/pty fills stdin/stdout/stderr with the tty *only when nil* (`run.go:38-50`), so the PTY never displaces a Stderr ptyrunner already set (`ptyrunner/runner.go:296`).

So the shipped sentence is wrong in its reason even for its own path: the PTY covers the fd claude never has, and the fd claude *does* share is shared identically on both paths. What actually holds is that claude's Bash-tool child gets claude-created pipes rather than inheriting claude's fds — claude behaviour, path-independent — and the 2026-08-06 green ptyrunner run is direct evidence of it: the FIFO was still held when `pyryExited` closed, so the surviving `cat` did not hold the rig's stderr write end. **That evidence transfers to the stream path unchanged, because the fd in question is the same fd.** Record the conclusion, the corrected reason, and that the evidence is one live run rather than a proof.

*Leg 2 — pyry's own `cmd.Wait` on claude. This hazard is new on the stream path and ptyrunner does not have it.* On ptyrunner, claude's stdin/stdout are the tty and stderr is `os.Stderr` — all `*os.File`, so `os/exec` creates no pipes and Wait waits on process exit alone. On the stream path `cmd.Stdout = parser` is a non-`*os.File` writer (`streamrunner/runner.go:176`), so `os/exec` creates a pipe and Wait blocks until its copy goroutine reaches EOF — which requires every dup of *claude's* stdout write end to be closed, including any held by the detached Bash group #565 measured surviving claude's exit.

It is bounded, not unbounded: `cmd.WaitDelay = killGrace` (`:204`, `killGrace = 5s` at `:44`) is exactly the stdlib's documented "child process that exits but leaves its I/O pipes unclosed" case. If it fires, `Wait` returns `ErrWaitDelay`, `Run` returns it as `waitErr` (`:231`, `:250`), and `runAgentRun` maps a non-nil non-`context.Canceled` error to a non-zero exit (`agent_run.go:271-277`).

So the manufactured negative, if it happens, is **not** the one the ticket predicted. `pyryExited` still closes and it closes well inside both deadlines (5 s against `probePyryExitGrace` = 20 s and `finExitPyryExitDeadline` = 120 s). What #1353 would misread is `ExitStatus`: a correct, complete stream-path run would publish a non-zero exit code produced by the rig's still-held FIFO rather than by pyry. Same class of false attribution, different field. Record it on the driver, named, with the 5 s / 20 s / 120 s arithmetic, so #1353's reader meets it before its exit reading rather than after.

Whether leg 2 actually fires is not decidable offline and must not be asserted either way. The inference available is the one from leg 1 — if claude gives its Bash children fresh pipes for stderr (which the 2026-08-06 run shows), it almost certainly does for stdout too — and it is an inference. #1353 is where it gets measured; say that.

**Site C — `:248-254`, turn headroom.** `--max-turns=probeMaxTurns` ("6") reaches claude by different routes: it is in claude's argv on the stream path (`buildStreamRunnerClaudeArgs`, `agent_run.go:373`) and **absent** from it on ptyrunner (`buildArgs`, `ptyrunner/runner.go:617-624`), where the pyry-side budget `Counter` enforces it instead. Both bound the run at 6 turns, so the conclusion (headroom to complete the turn) holds. The mechanism sentence has to name both.

**Site D — `:256-264`, no budget-fired run.** Every citation in this paragraph is a ptyrunner line (`ptyrunner/runner.go:502`, `:600-601`, `:606`, `:492-503`, `:499`), and reason (2) — the budget hook reaping *inside* the hook, before the trailer is written — describes a hook the stream path does not have at all. On the stream path the bound is claude's own `--max-turns`; claude emits its `result` and exits, and that event passes through the parser to pyry's stdout. The conclusion (none is staged) is unchanged and correct on both; the argument is not transferable and must be re-derived per path or narrowed explicitly to the caller it describes.

**Site E — `:206-213`, "NO PARAMETERS BEYOND t … there is no configuration a caller could vary that would still be this run."** Falsified by AC3 outright. Rewrite: the driver takes exactly one varying input, the staging delta, and everything else remains fixed — including the `t.Setenv`/no-`t.Parallel()` constraint, which is unaffected. Also fix the file header's opening sentence (`:5-7`) and the driver's own first line (`:201-204`), both of which say "on the ptyrunner default".

**Site F — the permission posture, new prose.** Not currently in the comment and it belongs there, because a caller choosing a delta is choosing it. The stream path's sole production caller passes `yolo=true` (`agent_run.go:288`), which emits `--dangerously-skip-permissions` (`permissionArgs`, `mcp_config.go:35-38`); the ptyrunner path instead trust-marks the workdir and writes a per-spawn deny-default settings file (`agent_run.go:300-317`). On the tool surface, **relay the repo's recorded position rather than asserting your own**: `agent_run.go:354-359` records `--allowed-tools` as the authoritative tool gate under YOLO, with the blast radius bounded by it rather than by the trust dialog — and this rig passes `--allowed-tools=Bash` (`spawnProbePyry`, `background_trigger_probe_test.go:628`). So the flip changes the *mechanism* of the gate; the repo's position is that it does not change its *width*. Do not overclaim equivalence, do not imply the stream path is ungated, and note that this ticket exercises neither posture — the first live spawn under the new delta is #1353's. One sentence of the same content goes on the new delta's own doc, because that is where the choice is made and a reader should not have to navigate to the driver to find it.

Every one of the six is **fixed in place**, not appended as a note. A paragraph that keeps its ptyrunner-only reasoning with a "(but see below)" is the failure mode this AC exists to prevent.

**Site G — the session id's producer, and it becomes a path component.** Not named by the ticket; surfaced by the security pass and recorded here as part of the classification rather than only as a finding.

`probeWaitForSessionID` (`background_trigger_probe_test.go:746`) polls pyry's stdout and `parseInitSessionID` (`fixtures.go:377-393`) returns the `session_id` of the first `system`/`init` line, with **no shape validation** — any non-empty string is accepted. That string then becomes a filename component: `jsonlPathFor` → `SessionJSONLPath` → `filepath.Join(home, ".claude", "projects", EncodeCwd(cwd), sessionID+".jsonl")` (tui-driver `jsonl.go:35-37`).

Who authored it differs with the delta. On ptyrunner, pyry mints the UUID itself (`newSessionID()`, `agent_run.go:311`), passes it as `--session-id` (`ptyrunner/runner.go:618`), and the emitter re-emits it — a pyry-controlled value. On the stream path there is no `--session-id` in claude's argv (`buildStreamRunnerClaudeArgs`, `agent_run.go:363-377`), so claude mints its own and the rig reads claude's bytes passed verbatim through the parser. The value crosses from claude to a path the rig stats and reads.

Conclusion for this ticket: unchanged behaviour and no code change. Nothing is staged here, the producer is the real claude CLI rather than a hostile actor, and the read target is inside the rig's own temp `HOME`. But the *justification* for treating the id as safe cites the runner path, so it is a delta-dependent site by AC4's own test, and it must be recorded on the driver in one sentence: on the stream path the transcript filename is claude-authored and unvalidated, and #1353 — which actually stages the turn — is where that first matters. Do **not** add validation, a sanitiser, or a guard: no such failure has been observed, and a defence for an unobserved failure mode is the wrong trade at this tier.

## Concurrency model

Unchanged, and that is a deliverable. Do not touch:

- The **cleanup order** at `:293-327`: the pyry-kill cleanup is registered before `holdProbeFIFO` so LIFO releases the FIFO first. `finding_live_run_test.go:298-303` records that inverting it has no symptom. Threading a parameter must not move it.
- The `PyryExited` / `ExitStatus` happens-before edge (`:354-366`, `:127-157`). `ExitStatus` is written strictly before `close(pyryExited)`; the one consumer reads it inside the receive arm (`finding_exit_path_probe_test.go:227-239`). No new field, no new goroutine, no new synchronisation.
- The `h.PyryPID <= 0` guard at `:310` before `syscall.Kill(-h.PyryPID, …)`.

One process-group asymmetry worth a sentence in the classification but **not** a code change: streamrunner sets no `Setpgid`, so claude inherits pyry's group and the defence-in-depth `Kill(-PyryPID)` reaches it; on ptyrunner the PTY spawn puts claude in its own session, so it does not. The kill is defence-in-depth on both and the difference is benign (if anything tidier on the stream path).

## Error handling

No new failure arms anywhere. The driver's straight-line-body rule (`:215-225`) and its exactly-two-`t.Fatalf` rule (`:227-246`) both stand; the parameter adds neither. `finOutcomeStagingGate` keeps owning every staging failure, rank-ordered.

## Testing strategy

- The new trap runs offline, needs no credentials, execs nothing, touches no filesystem. It inherits the file's forbidden-symbol list (`finding_live_staging_test.go:36-55`) verbatim: no `spawnProbePyry`, `holdProbeFIFO`, `pinScanArgv`, `tdnScan`, `probeProcessSnapshot`, `WithWorktree*`, `t.TempDir()`, and no `os.Getenv`/`os.Environ`/`os.Setenv` — the single `t.Setenv` the hostile ambient needs is the one permitted write. An `exec.` grep proves nothing here; the symbol list is the check. The `os.Environ` ban is load-bearing rather than tidiness: this is the one test in the family whose *subject* is the ambient environment, so it is the one where a developer is most tempted to log it — and this rig's process environment carries `CLAUDE_CODE_OAUTH_TOKEN` / `ANTHROPIC_API_KEY`. Failure messages print this file's own literals (the delta, the runner label), which is what the shipped trap does at `:473`.
- The new trap must not be `t.Parallel()` — it calls `t.Setenv` and Go's runtime refuses that pairing, so this is deterministic rather than advisory. Say so in the doc, as the shipped trap does at `:408-411`. (The package's 27 `t.Parallel()` tests are not a hazard: top-level parallel tests resume only after every serial top-level test has finished, so none can observe the mutated ambient.)
- AC5 gate:
  ```
  go test -race -tags e2e_realclaude ./internal/e2e/realclaude/
  ```
  Expect: the new trap and the shipped one both green; `TestRealClaude_ExitPathWhileCommandRuns` **SKIPS** rather than failing. Note it skips at the *first* of two gates — the opt-in `finExitEnableEnv != "1"` check at `finding_exit_path_probe_test.go:181`, which returns before the driver is reached at all; the credential skip inside `WithWorktreeAuthenticated` is the second. So on a machine with a Claude login the outcome is still a skip, and the only way the parameter change can break AC5 is a compile error. That makes the check cheap — and it also means AC5 does not exercise the new code path, which is the honest framing for the commit message.
- Focused run while iterating: `go test -race -tags e2e_realclaude -run '^TestFinLiveStage' -v ./internal/e2e/realclaude/`.
- `make check` does not analyse these files (`go vet` and `staticcheck` run without the tag). The tagged `go test` is the gate.

## Open questions

1. **Does leg 2 fire?** Whether claude's detached Bash group holds claude's stdout write end past claude's exit is not decidable offline. Resolution is #1353's first live run: if `ExitStatus` comes back non-zero on an otherwise-complete turn, leg 2 fired and the fix is a rig-side one (the rig would need to distinguish `ErrWaitDelay`-produced exits), not a pyry-side one. This spec's obligation is that #1353 meets the possibility in the driver's comment rather than in a debugging session.
2. **`Pin.ClaudeCommand` content on the stream path** is populated (both builders emit the needle) but its *bytes* differ — no `--session-id`, plus `--dangerously-skip-permissions`. Nothing consumes it as more than provenance today. If #1353 wants to read a runner off it, `reachRunnerPathFromArgv` is the wrong instrument (it keys on a flag both paths emit) and that is its own ticket.

## Security review

**Verdict:** PASS (second pass — the first pass raised one MUST FIX, addressed inline as site G, then the checklist was re-walked from the top)

**Findings:**

- **[Trust boundaries] MUST FIX — addressed inline.** AC3 opens a boundary that did not exist: `finLiveRunStage` previously read a package-level function and now takes a caller-supplied `[]string` that reaches `cmd.Env` of a spawned `pyry agent-run` verbatim (`spawnProbePyry`, `background_trigger_probe_test.go:637`). Separately, the *session id* — which becomes a filesystem path component via `jsonlPathFor` → `SessionJSONLPath` (`filepath.Join(home, ".claude", "projects", EncodeCwd(cwd), sessionID+".jsonl")`) — changes producer with the delta: pyry-minted and passed as `--session-id` on ptyrunner (`agent_run.go:311`, `ptyrunner/runner.go:618`), claude-minted and read off stdout unvalidated on the stream path (`parseInitSessionID`, `fixtures.go:377-393`, accepts any non-empty string). Neither is exploitable as designed — the callers are test files in one build-tagged package, the producer is the real claude CLI, the read target is inside the rig's own temp `HOME`, and this ticket stages no turn — but the second was absent from the classification AC4 requires to be exhaustive. Fixed by adding **site G** and by requiring site A to state the delta contract on the caller rather than as a fact about one value. **No validation, sanitiser or guard is specified**: no such failure has been observed, and #1353 is the first ticket that actually spawns under it.
- **[Tokens, secrets, credentials] SHOULD FIX — addressed inline.** The rig's process environment carries `CLAUDE_CODE_OAUTH_TOKEN` / `ANTHROPIC_API_KEY` (re-pinned by `WithWorktreeAuthenticated`), and the new trap is the one test in the family whose *subject* is the ambient environment — the likeliest place for a developer to log `os.Environ()` while debugging. The spec now names the file's existing `os.Getenv`/`os.Environ`/`os.Setenv` ban as a credential guard rather than leaving it as inherited tidiness, and separately forbids printing the caller-supplied `envDelta` from the driver's failure messages. No tokens are generated, stored, rotated or revoked by this design.
- **[File operations] No findings.** The ticket writes no files. The trap does no filesystem access at all (a stated property of `finding_live_staging_test.go`) and uses the synthetic `finLiveStageFixtureFIFOPath` rather than `t.TempDir()`. The driver's `prompt.txt` / `system.txt` writes at `0o600` (`:332`, `:336`) are unchanged and untouched. The `os.Stat`-then-read in `probeWaitForBashToolUse` (`:767-768`) is a pre-existing check-then-use inside the rig's own temp `HOME`, not introduced or widened here. The one path-composition concern is the session id, recorded above.
- **[Subprocess / external command execution] SHOULD FIX — addressed inline.** The delta reaches a real `exec.Command` environment, and the design's claim that "the delta wins over the ambient" rests on two different mechanisms pinned in two different places: `reachRunnerPathFromEnv`'s precedence (pinned offline by AC2's trap) and `os/exec`'s last-wins `Env` dedup (stdlib behaviour that **nothing in this package exercises** — no test here spawns a process). Site A now requires the driver's comment to distinguish them and to cite `finding_exit_path_probe_test.go:86-87` rather than re-derive. Otherwise: no `sh -c` in anything this ticket declares (the staged literal is a bare `cat <fifo>` and the shipped trap already forbids `;`/`&&`/`|`/`sh -c` at `:328-333`); the environment is inherited rather than scrubbed, unchanged and deliberate (claude needs the credentials); the `h.PyryPID <= 0` guard before `syscall.Kill(-h.PyryPID, …)` is untouched. One topology change is recorded and accepted: streamrunner sets no `Setpgid`, so claude inherits pyry's group and the defence-in-depth group kill reaches it, where on ptyrunner the PTY session leaves claude outside it. Blast radius stays inside processes the rig created, and the detached Bash group survives on both paths already (#565) — no new orphan class.
- **[Cryptographic primitives] Not applicable, with reason.** No randomness, no key material, no hashing, no comparison against a secret. Every equality in the new code is over env entries and a fixed runner-label vocabulary (`ptyrunner` / `streamrunner`), none of which is attacker-controlled or secret, so `crypto/subtle` has nothing to protect here.
- **[Network & I/O] No findings.** No sockets, listeners, TLS or HTTP anywhere in scope; the trap performs no I/O. The category's applicable lens is unbounded wait, and site B answers it: the one newly-possible stall (pyry's `cmd.Wait` blocked on a copy goroutine whose pipe a surviving grandchild holds) is bounded by `cmd.WaitDelay = killGrace` = 5 s (`streamrunner/runner.go:44`, `:204`), inside both `probePyryExitGrace` (20 s) and `finExitPyryExitDeadline` (120 s). The consequence is a distorted `ExitStatus`, not a hang — recorded on the driver so #1353 meets it before its exit reading.
- **[Error messages, logs, telemetry] SHOULD FIX — addressed inline.** Two prohibitions now stated explicitly: the trap prints only this file's own literals (delta entries, runner label), following `:473`; the driver prints no `envDelta` in any message, keeping its two `t.Fatalf`s (deadline + pyry's stderr) and one `t.Logf` (a duration) exactly as shipped. No telemetry, no metrics, no artifact written by this ticket.
- **[Concurrency] No findings.** No new goroutines, so no new lifecycle to leak. The `ExitStatus`-write-before-`close(pyryExited)` happens-before edge and the register-kill-before-`holdProbeFIFO` LIFO ordering are both specified as untouched deliverables, precisely because inverting either has no symptom. The trap's `t.Setenv` cannot pair with `t.Parallel()` (Go's runtime panics), and the package's 27 parallel tests resume only after every serial top-level test completes, so none can observe the mutated ambient. Handle aliasing was considered and needs no defence: both deltas are funcs returning a fresh slice (the shape `finLiveStageEnvDelta`'s doc argues for at `:186-188`), `cmd.Env` is snapshotted at `Start`, and no caller-mutation failure has been observed.
- **[Threat model alignment] No findings.** No relay, protocol, device or network surface is touched, so `docs/protocol-mobile.md` § Security model has no applicable threat. The one threat the ticket itself names — flipping the delta flips the staged claude's permission posture from trust-mark + deny-default settings to `--dangerously-skip-permissions` — is site F, documented on both the driver (where a caller reads) and the new delta (where the choice is made), with the tool-gate claim attributed to `agent_run.go:354-359` rather than asserted. Out of scope and named: this ticket stages no turn, so neither posture is exercised; #1353 carries `needs-real-claude` and owns the first live spawn under the new delta.

**Reviewer:** architect (self-review per `architect/security-review.md`)
**Date:** 2026-08-06
