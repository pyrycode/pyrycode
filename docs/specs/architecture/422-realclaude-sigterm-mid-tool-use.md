# 422 — e2e/realclaude: mid-tool_use SIGTERM cleanup (subprocess + JSONL state)

## Files to read first

- `internal/e2e/realclaude/fixtures.go:32-78` — `WithWorktree`, `WithWorktreeAuthenticated`, `ReadJSONL`. The new test uses `WithWorktreeAuthenticated` (real-API). `ReadJSONL` parses through `jsonl.NewReader` which silently drops any trailing partial line — see the explicit `\n`-terminator check in §"JSONL terminal-shape assertions" below; do NOT rely on `ReadJSONL` to surface a half-written tail.
- `internal/e2e/realclaude/fixtures.go:123-188` — `RunPyryAgentRun` is **not usable here** (synchronous Run, no PID access). The unexported `ensurePyryBuilt` and `parseInitSessionID` ARE reused by the new file (same package).
- `internal/e2e/realclaude/resilience_test.go:271-350` — the precedent for "drop to `exec.CommandContext` directly when the synchronous helper does not fit": `resolveClaudeBin`, `runClaudeDirect`. This ticket follows the same precedent for a file-local `spawnPyryAgentRun` helper, rather than widening `fixtures.go` for a one-off shape.
- `internal/e2e/realclaude/tool_loop_test.go:147-178` — `contentBlock`, `parseContentBlocks`. Reused directly; the new test imports nothing new for content-block parsing. (Same package, same build tag, no struct extension required — `tool_use.Name`, `tool_use.ID`, and `tool_result.ToolUseID` are already there.)
- `internal/e2e/realclaude/prompt_fidelity_test.go:75-89` — `jsonlPathFor` for failure-message diagnostics.
- `internal/e2e/realclaude/per_agent_test.go:135-144` — `truncate([]byte) string` capped at 1 KiB. Reuse in failure messages; do NOT re-define.
- `internal/e2e/realclaude/long_session_test.go:35-62` — anti-chain steering wording in `longSessionSystemPrompt`; the new prompt borrows the "run it once, do NOT chain" shape but reduces to a single Bash invocation.
- `cmd/pyry/agent_run.go:200-247` — `runAgentRun` installs `signal.NotifyContext(ctx, syscall.SIGTERM, syscall.SIGINT)`, then delegates to `streamrunner.Run`. Ctx-cancel from SIGTERM is the failure path under test.
- `internal/agentrun/streamrunner/runner.go:37-175` — `killGrace = 5 * time.Second`, `cmd.Cancel = SIGTERM`, `cmd.WaitDelay = killGrace`. **This is exactly the 5-second budget the bounded-exit assertion pins**; pyry's production contract is: claude gets SIGTERM, then SIGKILL 5 s later. If a future change weakens either side of that, this test fires.
- `internal/agentrun/jsonl/reader.go:171-262` — `Reader.Next` semantics: malformed-JSON lines are logged-and-skipped, trailing partial bytes are retained internally and NEVER surfaced. Explicit byte-tail check is the only way to assert "no half-written line."

## Context

When pyry receives SIGTERM with a Bash subprocess in flight, three production invariants must hold:

1. **Subprocess cleanup.** Claude's child Bash process is reaped within the 5 s grace window — no orphan `sleep` left running after pyry exits.
2. **JSONL consistency.** The on-disk session JSONL at `~/.claude/projects/<encoded-cwd>/<session>.jsonl` ends at a complete envelope boundary — no half-written trailing line that a future `--continue` would choke on.
3. **Bounded exit window.** Pyry itself exits within 5 s of SIGTERM (the `killGrace` constant in `streamrunner/runner.go:40`).

None of these is currently pinned by a test. The sibling resilience tests (#382) cover other cleanup-shaped contracts (premature stdin close, malformed stream-json, large prompt), but the SIGTERM-mid-tool_use cell is unobserved. The 2026-05-16 fork-bomb incident (`ca8b688`) was a different shape but the same family: a cleanup-path regression that nothing pinned. Adding this test now is a regression guard for the next member of that family.

Cost budget per `make e2e-realclaude` run: ~$0.005 (`--effort=low`, `--model=claude-haiku-4-5`, `--max-turns=2`, one Bash invocation that never completes its tool_result envelope). Below `budget_test.go`'s cache-hit test by an order of magnitude.

## Design

### File

One new file: `internal/e2e/realclaude/sigterm_mid_tool_use_test.go`. Build tag `//go:build e2e_realclaude`. Package `realclaude`. No edits to `fixtures.go`, `resilience_test.go`, `tool_loop_test.go`, or any other existing file.

Single test function: `TestRealClaude_SigtermMidToolUse`. Does NOT call `t.Parallel()` (matches existing realclaude convention).

### Test setup

1. `workdir := WithWorktreeAuthenticated(t)` — opt-in fixture; skips cleanly if `ANTHROPIC_API_KEY` is unset.
2. Write `prompt.txt` (file-scope constant `sigtermPrompt`) and `system.txt` (`sigtermSystemPrompt`) into `workdir` at mode `0o600`. Failure → `t.Fatalf`.
3. `bin := ensurePyryBuilt(t)` — reuse the same build-once-per-process helper used by `RunPyryAgentRun`.

### Prompt design

Two file-scope constants. The prompts steer haiku toward a **single** Bash invocation running `sleep 30`, so a `tool_use` is guaranteed in flight when the test sends SIGTERM ~3 s after spawn.

- `sigtermSystemPrompt`: one paragraph; "regression-guard test agent" framing, "use the Bash tool exactly once, run the command verbatim, do NOT chain with `&&` or `;`, do NOT comment, do NOT do anything else." Mirrors `longSessionSystemPrompt`'s anti-chain wording.
- `sigtermPrompt`: "Use the Bash tool to run `sleep 30`. Do nothing else."

The 30-second sleep is far longer than the test's combined 3 s pre-SIGTERM window + 5 s post-SIGTERM grace, so the Bash subprocess is guaranteed to be alive when the signal lands and is the relevant in-flight `tool_use`.

`--max-turns=2`: turn 1 is the assistant `tool_use`; turn 2 would happen after the `tool_result` (which never gets written because SIGTERM kills claude first). `2` is the minimum that lets claude reach the `tool_use` event; `1` would not — claude would not have a budget to call a tool.

### Spawn helper (file-local, single caller)

File-local helper `spawnPyryAgentRun(t, bin, workdir, promptPath, systemPath) *exec.Cmd` because `RunPyryAgentRun` runs to completion synchronously and provides no PID access during the run. Per the ticket's "Technical Notes" guidance and `resilience_test.go`'s precedent: **do NOT widen `fixtures.go` until a second test needs the same shape.**

Helper contract:

- Constructs the same argv as `RunPyryAgentRun` (see `fixtures.go:158-168`): `agent-run --prompt-file=… --system-prompt-file=… --allowed-tools=Bash --max-turns=2 --effort=low --model=claude-haiku-4-5 --workdir=… --output-format=stream-json`.
- `cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}` — places pyry in its own process group whose pgid equals pyry's pid. The orphan check (see below) walks this pgid.
- `cmd.Stdout` and `cmd.Stderr` bound to `*bytes.Buffer` values owned by the test. The test never reads these buffers concurrently with the child running (it does so only after `cmd.Wait` returns), so plain `bytes.Buffer` is safe here.
- `cmd.Env = os.Environ()` (inherits `HOME` and `ANTHROPIC_API_KEY` set by `WithWorktreeAuthenticated`). No extra env required.
- Calls `cmd.Start()`; `t.Fatalf` on failure (start failure is not a contract under test).
- Returns the `*exec.Cmd`. Buffers are passed by pointer separately, or — to keep the helper ergonomic — the helper accepts the buffer pointers as parameters and returns only `*exec.Cmd`. Either shape is fine; pick the one with the cleaner call site (probably: helper takes `*bytes.Buffer` parameters for stdout/stderr and returns `*exec.Cmd`).

The helper is ~20 lines. It exists solely to keep the test body readable; it is NOT a candidate for promotion to `fixtures.go` until a second test needs the same shape.

### Timing sequence

1. `cmd.Start()` returns; capture `pgid := cmd.Process.Pid` (valid because `Setpgid: true` made pyry its own process-group leader).
2. `time.Sleep(3 * time.Second)` — gives claude time to start, write the `tool_use` envelope to the on-disk JSONL, and fork the Bash subprocess.
3. `cmd.Process.Signal(syscall.SIGTERM)` — sends SIGTERM to pyry. `t.Fatalf` if Signal returns an error (extremely unlikely; the process must still be alive at this point because we haven't waited yet).
4. Bounded wait: run `cmd.Wait` in a goroutine, select against a 5-second `time.After`. If the timer fires first, fall through to a `cmd.Process.Kill()` salvage and `t.Fatalf("pyry did not exit within 5s of SIGTERM …")` with stderr included.
   - The 5 s budget is the production contract (`streamrunner.killGrace`). Pyry's own teardown is structurally bounded by it: SIGTERM → ctx cancel → `streamrunner.Run`'s `cmd.Wait` returns within `killGrace`. A budget violation IS the regression.

### Orphan-subprocess check

After `cmd.Wait` returns, assert that no processes remain in the pgid.

**Mechanism:** shell out to `pgrep -g <pgid>` and parse the output. `pgrep -g` is present on both macOS (since 10.8) and Linux (procps-ng).

- Exit code 1 with empty output → no orphans (the success case).
- Exit code 0 with one-or-more PID lines → orphans (the failure case; the test fails with the PID list, the pgid, and `truncate(stderr.Bytes())` for diagnostics).
- Any other exit code (e.g. pgrep not installed) → `t.Fatalf` with the error and a note that `pgrep -g` is required.

**Why process groups, not `pgrep -P`:** `pgrep -P <pyry-pid>` returns only direct children. After pyry exits, claude is reparented to init/launchd, so `-P` would falsely report "no orphans" regardless of whether claude or its Bash descendant are still alive. Process-group membership is preserved across reparenting, so `pgrep -g <pgid>` catches the actual orphan. The AC explicitly invites the architect to pick the shape "most reliable on macOS + Linux"; `pgrep -g` is that shape.

**Filter:** explicitly drop pyry's own pid from the result list (defense against a transient kernel state where pyry is reaped but its zombie pid briefly remains visible). Practically, after `cmd.Wait` the pid is fully reaped, but the filter is cheap and removes a class of false positives.

**Optional cleanup:** if the orphan check fails, the test sends `syscall.Kill(-pgid, syscall.SIGKILL)` before `t.Fatalf` so the orphaned `sleep 30` does not linger past the test run. Log-and-ignore on Kill error. This is salvage-on-failure, not part of the contract under test.

### JSONL terminal-shape assertions

Branch B (clean stream truncation at a complete envelope boundary) is the **architect-picked shape** for this test, on the following reasoning:

1. Claude writes the on-disk JSONL line-by-line as events occur. The `tool_use` envelope is written **before** the Bash subprocess starts. The matching `user` / `tool_result` envelope is written **after** the Bash subprocess completes (or fails).
2. SIGTERM lands while the Bash subprocess is still sleeping. Claude is blocked in `wait` on its Bash child; the `tool_result` envelope has not been written. Claude's own SIGTERM handling (whatever it is) does not flush a structured non-success trailer to the on-disk JSONL — that file is the session-state file claude uses for `--continue`, not a streamjson result trailer. The empirical expectation is **no trailer line at all in the on-disk JSONL**; instead the file ends after the last completed envelope (the assistant `tool_use`).
3. Branch A (structured trailer with non-success subtype) would require claude to have an explicit "on signal, write a result-trailer line to the on-disk JSONL" hook. There is no evidence of such a hook in claude's documented behaviour; the stream-json result trailer is a **stdout** feature, not an on-disk-JSONL feature.

The developer SHOULD verify this empirically with one probe run before locking the assertions in (see §"Open questions"). If branch A turns out to hold, the developer flips assertion (4) below to "find a result envelope with `subtype != success`" — same surface, opposite sign. The spec contract — that the terminal shape is **pinned**, in one direction or the other — is unchanged.

**Branch B assertions, in evaluation order:**

1. `sessionID := parseInitSessionID(stdout.Bytes())`; `sessionID != ""`. Failure includes `truncate(stdout)`. If pyry exited before claude emitted the `system/init` envelope, the test cannot proceed — note in the failure message that the 3 s pre-SIGTERM window may be too tight on this runner.
2. `jsonlPath := jsonlPathFor(workdir, sessionID)`. Read the raw file bytes: `jsonlBytes, err := os.ReadFile(jsonlPath)`. `t.Fatalf` on read error.
3. **No half-written line.** `len(jsonlBytes) > 0` AND `jsonlBytes[len(jsonlBytes)-1] == '\n'`. On failure, compute `lastNL := bytes.LastIndexByte(jsonlBytes, '\n')` and `trailing := len(jsonlBytes) - lastNL - 1`; failure message names `jsonlPath`, `trailing` (the count of trailing partial bytes), and the index of the last newline. This is the **only** way to surface a half-written tail — `ReadJSONL` silently retains it (see `jsonl/reader.go:188-262`).
4. **`tool_use` present.** Walk events from `ReadJSONL`. Find the first `assistant`-kinded event whose content blocks include a `tool_use` with `Name == "Bash"` and `ID != ""`. Record `bashToolUseID`. Failure ("no Bash tool_use observed") indicates SIGTERM landed before claude invoked Bash; failure message points the operator to tune the pre-SIGTERM sleep (raise from 3 s to 5 s).
5. **`tool_result` absent.** Walk all subsequent `user`-kinded events. Assert that NONE contain a `tool_result` content block with `tool_use_id == bashToolUseID`. Failure ("matching tool_result on disk — Bash completed before SIGTERM landed") indicates the test setup is broken: either `sleep 30` finished too fast (impossible) or the pre-SIGTERM sleep was too long. Failure message names `bashToolUseID`, `jsonlPath`, and recommends reducing the pre-SIGTERM sleep or extending the Bash command.

**Helper reuse for envelope parsing:** use `parseContentBlocks` from `tool_loop_test.go:168` directly. The existing `contentBlock` struct (with `Name`, `ID`, `Type`, `ToolUseID`) covers exactly what assertion (4) and (5) need; no struct extension required.

### Helper inventory (additions)

- `spawnPyryAgentRun(t, bin, workdir, promptPath, systemPath, stdoutBuf, stderrBuf) *exec.Cmd` — ~20 lines.
- `processesInProcessGroup(t, pgid int) []int` — ~15 lines. Parses `pgrep -g <pgid>` output, filters out `pgid` itself, returns the remaining PIDs.

Total new helper code: ~35 lines. Both file-local. Neither is a candidate for `fixtures.go` until a second test needs the same shape.

### Reused helpers (no changes)

- `WithWorktreeAuthenticated` — `fixtures.go:45`.
- `ReadJSONL` — `fixtures.go:59`.
- `ensurePyryBuilt` — `fixtures.go:236` (unexported; same-package access).
- `parseInitSessionID` — `fixtures.go:280` (same-package access).
- `parseContentBlocks` — `tool_loop_test.go:168`.
- `jsonlPathFor` — `prompt_fidelity_test.go:79`.
- `truncate` — `per_agent_test.go:138`.

### Concurrency model

Two goroutines, both owned by the test body:

1. A background goroutine that runs `cmd.Wait()` and sends its error on a buffered (cap 1) `chan error`. Lives only for the duration of the bounded wait.
2. The test main goroutine, which `select`s the wait channel against `time.After(5 * time.Second)`.

No locks, no shared mutable state. The `bytes.Buffer` values backing stdout/stderr are written by the `exec.Cmd` machinery during the run and read by the test body only AFTER `cmd.Wait` returns — happens-before is provided by the `Wait` completion.

### Error handling

Every assertion failure uses `t.Fatalf` with a diagnostic that names (a) `jsonlPath` or the stdout/stderr snippet (truncated to 1 KiB via `truncate`), (b) the observed value, (c) the expected value or threshold, (d) a remediation hint when the failure mode is "test setup is wrong" rather than "production is broken" (e.g., the pre-SIGTERM sleep is too tight or too long).

Structural failures (workdir setup, prompt-file write, `cmd.Start`, `pgrep` missing) call `t.Fatalf` directly — they are NOT contracts under test.

## Testing strategy

The test itself is the regression sensor. To verify the test works:

1. Run `make e2e-realclaude` locally with `ANTHROPIC_API_KEY` set and the `claude` binary on PATH. The test must pass cleanly across two consecutive runs (no flakes).
2. **Sanity probe before locking in:** print (`t.Logf`) the last 5 events from `ReadJSONL` and the last 200 bytes of `jsonlBytes` on the first dev run, so the developer can visually confirm the terminal shape matches branch B. Strip the `t.Logf` lines before committing if the shape matches; if it does NOT match (i.e., branch A holds — a structured trailer line IS present), flip assertion (5) per §"JSONL terminal-shape assertions" and leave a single comment recording the observation.
3. If the test reports "no Bash tool_use observed in JSONL" reproducibly, raise the pre-SIGTERM sleep from 3 s to 5 s — do NOT lower `--max-turns` or shorten the prompt; those don't help and may break the test in other ways.

No unit-test counterpart. The point is end-to-end coverage of the SIGTERM signalling path, which no unit test can provide.

## Open questions

- **Branch A vs branch B.** The architect picks branch B (clean stream truncation at envelope boundary) based on the empirical expectation that claude does NOT write a structured trailer to its on-disk JSONL on signal. The developer should verify with the sanity probe described in §"Testing strategy" before committing. If branch A holds, flip assertion (5) and commit. **Do NOT** weaken the assertion into "either branch passes" — the spec contract is that the shape is **pinned**.
- **pgrep cross-platform stability.** `pgrep -g <pgid>` is present on both macOS and Linux today. If a future CI runner ships without `pgrep`, the test will fail loudly (clear "pgrep not installed" message), at which point the team picks between (a) replacing the helper with `syscall.Kill(-pgid, 0)` + `errors.Is(err, syscall.ESRCH)` (no diagnostic enumeration, but no shell dependency) or (b) installing pgrep in the CI image. Not in scope for #422.
- **3 s pre-SIGTERM sleep.** Chosen as a balance between "long enough for claude to write the `tool_use` envelope to disk and fork Bash" and "short enough not to dominate the test wall-clock." Empirically reasonable for haiku-low; if flake observed, raise to 5 s and document in-comment. Do NOT go below 2 s — the `tool_use` envelope write is the timing-critical event.

## Self-check (architect, pre-commit)

- Production-source files modified or created (excluding `*_test.go`, `*.md`, the spec itself): **0**. Well under the ≥ 5 split threshold.
- Test-source files: **1 new** (`internal/e2e/realclaude/sigterm_mid_tool_use_test.go`).
- New exported types: 0.
- Edit fan-out: 0 call sites touched (purely additive new test file).
- Red lines: none tripped. Single new test file, ~150 LOC including the two file-local helpers and the prompt constants, no new exported types, no consumer cascade.
- File-overlap check (2026-05-16, branch-based via `git branch -r`): `origin/feature/363` touches `internal/e2e/realclaude/fixtures.go`. This spec deliberately does NOT modify `fixtures.go`; the new test is a standalone file. **No overlap.**
- Security-sensitive label: not present on #422. No adversarial pass required.
