# Spec — #332: agent-run: spawn interactive claude + PTY-drive single user-turn

Status: draft (architect)
Size: S
Sub-issue of #329. Builds on closed #339 (settings-file emit) and #342 (workspace-trust mark).

## Context

`pyry agent-run` today (post-#337/#339/#342) parses its full flag surface, marks the workdir trusted in `~/.claude.json`, writes the deny-default per-spawn settings JSON, and prints `settings-file: <path>` on stdout. It does NOT yet spawn `claude`.

This ticket adds the spawn + PTY-drive sequence that the Phase A spike (`/tmp/agent-run-spike/pty_drive.py`) demonstrated. The product behaviour: from `runAgentRun`, after the settings file is emitted, spawn interactive `claude` in a PTY, drive a single user-turn (dismiss the trust dialog defensively, then type the prompt), background-drain PTY output, and tear down cleanly on `SIGTERM` to pyry. No JSONL parsing, no end-of-turn detection — that lands in #333.

## Files to read first

- `cmd/pyry/agent_run.go:1-195` — current `runAgentRun`; this ticket adds the spawn block after the `settings-file:` print, plus a new `buildClaudeArgs` helper.
- `cmd/pyry/agent_run_test.go:1-30` and `newValidArgsFixture` — the existing fixture that drives `runAgentRun`; the new tests reuse it.
- `internal/supervisor/supervisor.go:301-431` (`runOnce`) — the canonical PTY-spawn shape (`exec.CommandContext` + `pty.Start`); the new `SpawnPTY` helper extracts that shape verbatim. Lines 396-449 (`openTTYInput`, `stdinFallback`) are NOT relevant — agent-run never bridges to a controlling terminal.
- `internal/supervisor/supervisor.go:54-104` — `Config` for the existing supervisor; the new `SpawnConfig` mirrors the subset relevant to one-shot spawn (`ClaudeBin`, `WorkDir`, `Logger`, `helperEnv`-style env tail).
- `internal/agentrun/settings.go:46-82` — `WriteSettings` return path is the value passed to `--settings`. Confirms the path policy.
- `internal/agentrun/trust.go` — `MarkWorkdirTrusted` is the upstream guard; the spec's "trust dialog defensive Enter" is a belt-and-suspenders write, not a substitute.
- `/tmp/agent-run-spike/pty_drive.py:25-67` — exact drive timings and teardown shape the Go port mirrors. Note the spike uses `--permission-mode acceptEdits` for the experiment; this ticket switches to `default` (see § Security).
- `docs/lessons.md:13-27` ("PTY Testing") and `docs/lessons.md:225-260` ("PTY master fds on darwin do not support SetReadDeadline") — constrain the unit-test strategy (no PTY deadlines on macOS; bridge tests use `TestHelperProcess`, not a real PTY).
- `docs/knowledge/codebase/339.md` § "Stdout marker contract" — `settings-file:` is the sole stdout contract today; this ticket does NOT add a second stdout line on success.

## Design

### Seam: a thin `SpawnPTY` primitive in `internal/supervisor`

The ticket requires that we reuse the existing supervisor for the PTY spawn ("do NOT introduce a parallel spawning primitive"). `supervisor.runOnce` is unexported and tightly bound to the restart loop + bridge wiring. We extract the spawn-and-handle bit as a small exported helper that BOTH `runOnce` and the new agent-run driver consume.

Add to `internal/supervisor/spawn.go` (new file):

```go
// SpawnConfig is the minimum surface SpawnPTY needs. Mirrors the
// relevant subset of Config; intentionally separate so callers that
// don't want a full Supervisor (one-shot agent-run) don't pull in
// backoff/bridge/state fields.
type SpawnConfig struct {
    Bin     string        // executable path; required
    Args    []string      // argv (without argv[0])
    WorkDir string        // optional; empty means inherit
    Env     []string      // appended to os.Environ()
    Logger  *slog.Logger  // optional; defaults to slog.Default()
}

// SpawnPTY launches Bin in a PTY using exec.CommandContext + pty.Start.
// On ctx cancel: SIGTERM is forwarded; WaitDelay enforces a SIGKILL grace.
// Caller owns lifecycle — must Wait the *exec.Cmd and Close the *os.File.
//
// Defaults:
//   - cmd.Cancel sends SIGTERM (not the stdlib default Kill).
//   - cmd.WaitDelay = 5 * time.Second.
// Callers that need different timings overwrite the fields on the
// returned *exec.Cmd before calling Wait.
func SpawnPTY(ctx context.Context, cfg SpawnConfig) (*exec.Cmd, *os.File, error)
```

Behaviour contract (no body in the spec — see § Testing for the invariants the test pins):

- `cmd := exec.CommandContext(ctx, cfg.Bin, cfg.Args...)`; sets `cmd.Dir = cfg.WorkDir` when non-empty; sets `cmd.Env = append(os.Environ(), cfg.Env...)`.
- `cmd.Cancel = func() error { return cmd.Process.Signal(syscall.SIGTERM) }`.
- `cmd.WaitDelay = 5 * time.Second`.
- `ptmx, err := pty.Start(cmd)`; wraps as `supervisor: pty start: %w` on error.
- Returns `(cmd, ptmx, nil)` on success.

`runOnce` is **not** refactored to consume `SpawnPTY` in this ticket. That refactor is a follow-up (file a tracking comment on #329); doing it here would balloon the diff and pull `runOnce`'s I/O-bridge teardown into a primitive shape that neither caller wants. The duplication is one-line (`pty.Start(cmd)` appears in both), justified.

### Driver: `internal/agentrun/drive.go`

New file. Exports a single function:

```go
// Drive spawns claude with the agent-run argv, drives a single user-turn
// via the PTY, background-drains the PTY output, and waits for the child
// to exit (typically via ctx cancel → SIGTERM → SIGKILL grace).
//
// Returns the child's exit error (from exec.Cmd.Wait). nil on a clean
// exit; *exec.ExitError on non-zero exit; context.Canceled wrapped when
// ctx cancellation triggered the teardown.
func Drive(ctx context.Context, cfg DriveConfig) error
```

```go
type DriveConfig struct {
    ClaudeBin    string  // required; "claude" by default at the caller
    WorkDir      string  // required; passed as cmd.Dir
    Args         []string // full claude argv (built at the call site)
    Logger       *slog.Logger

    // Timings — exposed for tunability AND for unit tests that override
    // them to ~ms. Zero values use the spike-validated defaults.
    TrustDialogDelay time.Duration // default 2500 * time.Millisecond
    PromptDelay      time.Duration // default 3500 * time.Millisecond

    // PromptBytes is the user-turn text to type after PromptDelay. The
    // driver appends a single "\r" after these bytes; callers MUST NOT
    // include a trailing CR. Read by the caller from --prompt-file
    // before calling Drive.
    PromptBytes []byte
}
```

Drive sequence (no body in the spec; pinned by tests):

1. Call `supervisor.SpawnPTY(ctx, ...)` with `Bin=ClaudeBin`, `Args=cfg.Args`, `WorkDir=cfg.WorkDir`, `Logger=cfg.Logger`. Failures: return wrapped as `agentrun: drive: spawn: %w`.
2. `defer ptmx.Close()`.
3. Spawn the background-drain goroutine — `io.Copy(io.Discard, ptmx)` until error (typically `ptmx.Close` from the deferred line above). This is the load-bearing piece: without it, claude blocks once its output buffer fills (~64 KB on the kernel-side line discipline).
4. Sleep `cfg.TrustDialogDelay`. Wake on `ctx.Done()` (use `select` with `time.After`). Write `[]byte{'\r'}` to `ptmx`.
5. Sleep `cfg.PromptDelay`. Same `select` shape. Write `cfg.PromptBytes` followed by `[]byte{'\r'}` to `ptmx` (two separate `Write` calls is fine; the spike does one combined write — we match the spike for byte-identical fidelity).
6. Call `cmd.Wait()` and return its result. Teardown comes from `ctx` cancellation through `cmd.Cancel` (SIGTERM) + `cmd.WaitDelay` (SIGKILL after 5s). The drain goroutine exits when the deferred `ptmx.Close` unblocks its `io.Copy`.

Notes on the sleep / context-cancel interleaving:

- During the two sleeps, `ctx` cancellation aborts the sleep AND skips the pending PTY write (writes after cancel are pointless — `cmd.Cancel` has already SIGTERMed the child).
- After step 5 we're in `cmd.Wait()`, which itself respects `cmd.Cancel`. Nothing else to wire.

No mutex, no second goroutine beyond the drain. Single-shot, single-caller; concurrency surface is intentionally minimal.

### Wiring: `cmd/pyry/agent_run.go`

Add at the bottom of `runAgentRun`, after the `fmt.Printf("settings-file: ...")` line:

1. Read `parsed.promptFile` contents into a `[]byte` (`os.ReadFile`). Wrap errors as `agent-run: read prompt-file: %w`.
2. Build the claude argv via a new `buildClaudeArgs(parsed agentRunArgs, settingsPath string) []string` helper:
   - `--settings <settingsPath>` (from the just-emitted file)
   - `--permission-mode default` (load-bearing — see § Security)
   - `--model <parsed.model>`
   - `--append-system-prompt-file <parsed.systemPromptFile>`
   - `--effort <parsed.effort>` (always emitted — `parsed.effort` is a required flag, validated non-empty by `parseAgentRunArgs`; the ticket's "[--effort <e>]" bracket reflects the underlying claude CLI's optionality, not pyry's)
   - **Drop `--allowedTools`** (the settings-file allow-list is the authority; the spike confirmed `--allowedTools` is a no-op in interactive mode under the settings layer)
   - **Drop `--max-turns`** for now — claude's interactive mode does not honour `--max-turns`; the dispatcher uses it for budget bookkeeping outside the spawn. (Confirm with a comment + see "Open questions".)
   - **Drop `--output-format stream-json`** for now — interactive claude streams its TUI; `stream-json` is `-p`-mode only. The flag remains parsed at the pyry CLI surface (the dispatcher requires it) but is not propagated to the spawn. Document in code.
3. Construct a `context.Background()`-rooted context that cancels on `SIGTERM`/`SIGINT` to pyry. Use `signal.NotifyContext(context.Background(), syscall.SIGTERM, syscall.SIGINT)`. Defer the `stop` function.
4. Call `agentrun.Drive(ctx, agentrun.DriveConfig{...})`. Map the return:
   - `nil` → return `nil` (clean exit — the dispatcher treats this as success at this layer; turn-success vs turn-failure is a JSONL parse problem, deferred to #333).
   - `context.Canceled` / `signal.NotifyContext`-induced exit → return `nil` (operator-driven SIGTERM is a clean shutdown).
   - Other error → wrap as `agent-run: drive: %w`.
5. No additional stdout output. `settings-file:` remains the sole stdout marker; drive output (PTY stream) is discarded.

The flag-parse path is untouched. The two existing `agent-run` tests still pass against the parse layer; the new spawn block is exercised by new tests that use a fake claude binary.

## Files modified / created

Production:
- `internal/supervisor/spawn.go` (new, ~40 LOC)
- `internal/agentrun/drive.go` (new, ~80 LOC)
- `cmd/pyry/agent_run.go` (modified, ~50 net LOC: `buildClaudeArgs` helper + `runAgentRun` tail)

Tests:
- `internal/supervisor/spawn_test.go` (new, ~80 LOC) — SIGTERM-then-SIGKILL grace using `TestHelperProcess`-style fake.
- `internal/agentrun/drive_test.go` (new, ~150 LOC) — drive sequence + drain + teardown against a fake binary.
- `cmd/pyry/agent_run_test.go` (modified, ~80 net LOC) — `buildClaudeArgs` argv shape test + one wiring test that drives `runAgentRun` against a `fakeclaude` binary built from `TestHelperProcess`.
- `internal/agentrun/drive_e2e_test.go` (new, ~60 LOC, build-tagged or `testing.Short()`-gated) — drives the real `claude` binary in a tmpdir, asserts JSONL file appears within a budget, then `SIGTERM`s and asserts process exits within `WaitDelay`.

Production count: 3 (`internal/supervisor/spawn.go`, `internal/agentrun/drive.go`, `cmd/pyry/agent_run.go`). Under the 5-file architect red line.

## Concurrency model

Goroutines spawned by `Drive`:
- **drain goroutine** — `io.Copy(io.Discard, ptmx)`. Exits when `ptmx.Close` fails its Read. No other writer or reader of `ptmx` exists concurrently except `Drive` itself (the two scripted `ptmx.Write` calls during the sleeps). PTY master fds tolerate concurrent reader + writer (different sides of the line discipline).

Goroutines spawned by `signal.NotifyContext` (stdlib):
- one internal signal-watcher; cancels the ctx on SIGTERM/SIGINT.

Shutdown sequence on `ctx` cancel:
1. ctx is `Done()`; the `select` in the sleep-and-write blocks (steps 4/5) returns immediately.
2. If we're past step 5, `cmd.Wait()` is the only blocker. `cmd.Cancel` fires SIGTERM at the child via `exec.CommandContext`'s internal goroutine.
3. The child exits (or `cmd.WaitDelay` elapses after 5s and the runtime SIGKILLs).
4. `cmd.Wait()` returns; `defer ptmx.Close()` runs; the drain goroutine's `io.Copy` returns; `Drive` returns.

No goroutine outlives `Drive`. No timer leaks (`time.After` channels are GC'd when no longer referenced; we don't keep `Timer` handles).

## Error handling

| Failure | Behaviour |
|---|---|
| `os.ReadFile(promptFile)` fails in wiring | Returned wrapped as `agent-run: read prompt-file: %w`. No spawn attempted; settings file is left on disk (overwrite-safe on next invocation). |
| `SpawnPTY` fails (e.g. `exec.LookPath` finds nothing — already validated at `parseAgentRunArgs` but defensive) | Returned wrapped as `agentrun: drive: spawn: %w`. |
| `ptmx.Write` during drive returns an error | Log via `cfg.Logger.Warn` with `err`, but do NOT bail — proceed to `cmd.Wait()`. A failed write usually means the child has already exited; `cmd.Wait()` will return that exit error in another beat and the operator sees the real cause. |
| `ctx` cancelled before step 4's write | Skip both writes; go straight to `cmd.Wait()`. `cmd.Cancel` fires SIGTERM; child exits. |
| Child exits non-zero before we reach `cmd.Wait()` | `cmd.Wait()` returns `*exec.ExitError`; we wrap and return. The drain goroutine drains any post-exit buffered output before unblocking. |
| `cmd.WaitDelay` (5s) elapses without child exit after SIGTERM | The runtime SIGKILLs. `cmd.Wait` returns. Total worst-case teardown latency: ~5s. |

## Security

This ticket is `security-sensitive`. Three observations the implementer must honour:

1. **`--permission-mode default` is load-bearing.** The settings file emitted by #339 has `defaultMode: "deny"`. The spike used `acceptEdits` for ergonomic experimentation, but `acceptEdits` overrides the file's `defaultMode`, silently defeating the deny-default whitelist. The ticket body calls this out; the `buildClaudeArgs` helper MUST emit `--permission-mode default` (literal string). Pin this with a unit test on `buildClaudeArgs` that asserts the exact flag pair appears.
2. **`--allowedTools` MUST NOT be emitted.** Same load-bearing reason: in interactive mode under a settings layer, `--allowedTools` is additive and silently broadens the allow-list. The settings file is the authority. Pin this with a unit test on `buildClaudeArgs` that asserts `--allowedTools` does NOT appear in the produced argv (even though `parsed.allowedTools` is non-empty).
3. **PTY input is operator-controlled, not user-controlled.** The bytes typed into the PTY come from `--prompt-file`, which the dispatcher (operator) wrote. They flow into `claude`'s TUI; control sequences in the file could in principle manipulate claude's TUI parser. We do NOT sanitise. Trust boundary: pyry trusts its own operator-supplied prompt file the same way it trusts the operator-supplied `--workdir`. Document in code comment.

Non-issues:
- **No new file writes outside `workdir` and the existing PTY fd.** `ptmx` is the only fd `Drive` writes to; no logs, no temp files.
- **No shell.** `exec.CommandContext` takes argv directly; no shell expansion of `parsed.promptFile`, `parsed.workdir`, etc.
- **Settings-file path** flows from `agentrun.WriteSettings` (validated workdir) into `--settings` as a single argv element; no concat into a shell string.

See § Security review (below) for the adversarial pass.

## Testing strategy

### Unit (under `-race`, table-driven where useful)

**`internal/supervisor/spawn_test.go`**

- `TestSpawnPTY_BasicExitsCleanly` — fake child via `TestHelperProcess` mode `exit0`; assert `cmd.Wait` returns `nil` and `ptmx` is non-nil.
- `TestSpawnPTY_SIGTERMOnCancel` — fake child sleeps 30s; cancel ctx; assert child exits in well under `WaitDelay` (≤500ms is generous). Pins `cmd.Cancel` is SIGTERM not Kill (mode that traps SIGTERM and exits 0 distinguishes the two).
- `TestSpawnPTY_SIGKILLAfterGrace` — fake child traps SIGTERM and ignores it; cancel ctx; assert child exits via SIGKILL within `WaitDelay + 1s`. (Override `cmd.WaitDelay` post-return to ~200ms in the test, so we don't burn 5s per run.)

**`internal/agentrun/drive_test.go`** — uses a fake claude (via `TestHelperProcess`) that:
- Reads its stdin (PTY slave) line-by-line.
- Appends each line to a file at `GO_TEST_HELPER_STDIN_FILE`.
- Optionally sleeps before reading to simulate the trust-dialog timing.

Scenarios (bullet, not function bodies):

- **Happy path drive sequence.** TrustDialogDelay = 50ms, PromptDelay = 50ms, PromptBytes = `[]byte("ping")`. Drive returns `nil` after the fake exits on EOF. Inspect the stdin-capture file: contents are `"\r" + "ping" + "\r"` exactly (or `"\rping\r"` if the spike's single-write shape is chosen).
- **Background drain prevents block.** Fake child writes 1 MB of output then exits. With drain enabled, `Drive` returns within a small budget (≤1s). Removing the drain goroutine in the test would hang (don't assert that; comment-document).
- **ctx cancel during trust-dialog sleep.** Cancel before TrustDialogDelay elapses; assert no bytes were written to the PTY (stdin-capture file empty), Drive returns wrapped-ctx-error or nil (depending on chosen mapping; specify in the spec — recommend: returns `nil` because cancellation by operator is success at the verb).
- **ctx cancel between trust and prompt writes.** Cancel after the `\r` write but before the prompt write; stdin-capture file ends after the first `\r`, no prompt bytes appear.
- **Child exits non-zero.** Fake mode `exit1`; Drive returns `*exec.ExitError`, the error is wrapped at the `cmd/pyry/agent_run.go` layer (asserted in the wiring test).
- **PTY write error tolerated.** Fake child exits immediately after a brief sleep; the prompt-write race-loses to the exit. The write returns `EIO` or similar; Drive logs `Warn` and proceeds to `cmd.Wait()`, returning the child's exit error. No second-level error wrapping that hides the primary cause.

**`cmd/pyry/agent_run_test.go` additions**

- `TestBuildClaudeArgs_Shape` — table-driven over a few `agentRunArgs` permutations; pins the exact argv slice. Includes the security-load-bearing assertions: `--permission-mode default` present, `--allowedTools` absent.
- `TestRunAgentRun_DrivesFakeClaude` — uses a `fakeclaude` test binary (built via `TestHelperProcess` or a `go build` of a small main). Drives `runAgentRun` end-to-end; asserts (a) settings file exists on disk, (b) the fake observed its stdin (or env-marker), (c) the function returns `nil` after the fake exits cleanly. Note: this test must `t.Setenv("PATH", ...)` so `parseAgentRunArgs`'s validation finds the fake (no — `parseAgentRunArgs` only validates flag presence, not bin lookup; the lookup happens inside `Drive` via `exec.CommandContext`). The test threads the fake binary path through a hook on `runAgentRun` — see "Open questions" for which hook.

### E2E (gated by `!testing.Short()` and `claude` on PATH)

**`internal/agentrun/drive_e2e_test.go`**

- Build a tmpdir workdir. Build the full argv as `runAgentRun` would. Write a tiny prompt file (`"hello"`). Call `Drive` with a 30s deadline ctx. Concurrently watch `~/.claude/projects/<encoded-workdir>/` for a `.jsonl` file appearing. Within 5s (generous), the file should exist. Then cancel ctx; assert `Drive` returns within `WaitDelay + 1s` and the claude process is no longer present (`os.FindProcess` + `Signal(syscall.Signal(0))` returns `os: process already finished`).

Note: the encoded-cwd rule (`/` AND `.` → `-`) used by claude is already documented in `docs/lessons.md`'s "Claude session storage on disk" section; the e2e test consumes that via `agentrun.ResolveWorkdir` (existing, from #341).

## Open questions

1. **`fakeclaude` injection mechanism for `TestRunAgentRun_DrivesFakeClaude`.** `runAgentRun` hard-codes `"claude"` as the binary lookup. Options:
   - (a) Add a `claudeBin string` field to `agentRunArgs` populated from `os.Getenv("PYRY_CLAUDE_BIN")` at the top of `runAgentRun` when set (test-only env knob; documented as such). **Recommended** — zero production surface change, test-friendly.
   - (b) Extract `runAgentRun` to accept a `Driver` interface. Heavier; defer.
2. **`Drive` return mapping on operator SIGTERM.** Spec recommends mapping `ctx.Err() == context.Canceled` (from `signal.NotifyContext`) to `nil` at the `runAgentRun` boundary — operator-driven termination is success at the verb level. The implementer should pin this in the wiring test; if the dispatcher prefers a non-zero exit code on operator cancel, file a follow-up.
3. **`stream-json` propagation.** Today the spawn drops `--output-format stream-json` because interactive claude streams its TUI. The dispatcher requires the flag at the pyry CLI to declare intent. If a future ticket adds JSONL parsing in pyry (vs. parsing claude's own JSONL on disk per #333), this question reopens. Out of scope.
4. **Configurable `WaitDelay`.** Hard-coded 5s in `SpawnPTY`. Tests override post-return. If production tuning is ever needed, expose via `SpawnConfig.WaitDelay`. Don't pre-add.

## Split proposal

Not applicable — this spec ships as one slice. 3 production files, ~190 LOC, zero edit fan-out.

---

# Security review (per § "Security review" in agents/architect/CLAUDE.md)

Performed inline per the `security-sensitive` label requirement. Stance: adversarial; assume a malicious operator who controls every input the dispatcher passes to `pyry agent-run`.

## Trust boundaries

- **Operator (dispatcher) → `pyry agent-run` flag surface.** All flag values originate here: `--prompt-file`, `--system-prompt-file`, `--allowed-tools`, `--workdir`, `--model`, `--effort`, etc. `parseAgentRunArgs` validates file existence, mode, and tag-shape; it does NOT sanitise contents.
- **`pyry agent-run` → spawned `claude`.** Argv is constructed by `buildClaudeArgs`; no shell. PTY input is the literal bytes from `--prompt-file` plus two `\r`s.
- **`pyry agent-run` → filesystem.** Writes one file (`.pyry-agent-run-settings.json` inside `--workdir`, via the #339 helper) and one well-known config file (`~/.claude.json`, via the #341/#342 helper). This ticket adds no new write surface.

## Categories walked

| Category | Finding | File:line / decision |
|---|---|---|
| Shell / command injection | None. `exec.CommandContext` with argv slice; no `sh -c`. | `internal/agentrun/drive.go` (new) → calls `supervisor.SpawnPTY` which uses `exec.CommandContext(ctx, cfg.Bin, cfg.Args...)`. |
| Path traversal | `--workdir` is validated by `requireDir` (must exist as dir). `--prompt-file` / `--system-prompt-file` validated by `requireRegularFile`. Adversary could point them at e.g. `/etc/passwd` (regular file). `--system-prompt-file` content flows into claude as system prompt — that's a claude-side concern, not pyry's. `--prompt-file` content flows into PTY — same. Pyry does not exfiltrate or re-emit either file's content beyond the spawn. | `cmd/pyry/agent_run.go:97-106` (existing); spawn does not aggravate this. |
| Privilege boundary | Process runs as the invoking user. No setuid, no capability change. SIGTERM/SIGKILL target only the child PID (`cmd.Process.Signal`, not pgid). | `internal/supervisor/spawn.go` (new). |
| Permission bypass | The load-bearing security control of agent-run is the deny-default settings file consumed via `--settings` + `--permission-mode default`. The spec requires both: `buildClaudeArgs` MUST emit `--permission-mode default` AND MUST NOT emit `--allowedTools`. Both pinned by unit tests. If either drifts, the whitelist is silently defeated. **This is the single highest-value security invariant in the ticket.** | `cmd/pyry/agent_run.go` (modified) + `cmd/pyry/agent_run_test.go` (test asserts argv shape). |
| Resource exhaustion / DoS | Background drain bounds PTY-buffer growth. `WaitDelay = 5s` bounds shutdown latency. Sleep durations are constants. No unbounded loops, no per-message allocations. | `internal/agentrun/drive.go` (new). |
| Race / TOCTOU | `--prompt-file` is read into memory at the `runAgentRun` layer *after* settings emit but *before* spawn. The file could be swapped between `requireRegularFile` (parse-time) and the read; we read whichever bytes are present at read-time. Acceptable — the operator is the writer, threat is self-inflicted. | `cmd/pyry/agent_run.go` (modified). |
| Secret / credential exposure | No credentials are read or written by this ticket. The settings file is mode 0600 (#339). `~/.claude.json` is touched only via the #341 helper. PTY output goes to `io.Discard`, not to disk or logs. | n/a. |
| Logging / PII | `cfg.Logger.Warn` on write errors logs the error string only, not the prompt bytes. `slog.Info` from `SpawnPTY` (if added) should log the argv WITHOUT the prompt bytes (argv doesn't include them; they flow through PTY). | `internal/agentrun/drive.go` (new). |
| Signal handling | `signal.NotifyContext` for SIGTERM/SIGINT; `stop()` deferred. Standard idiom. No signal masking. Child receives SIGTERM via `cmd.Cancel`; if it ignores, SIGKILL after `WaitDelay`. | `cmd/pyry/agent_run.go` (modified). |
| Network surface | None. This ticket adds no network code. | n/a. |
| Dependencies | One new stdlib package usage (`os/signal`, already used elsewhere in pyry). No new third-party imports — `creack/pty` already in use by supervisor. | n/a. |

## Adversarial walkthrough

1. **Operator sets `--allowed-tools "Bash"` hoping the spawn will emit `--allowedTools` and let the child run any Bash command.** Defence: `buildClaudeArgs` does NOT emit `--allowedTools`. The settings file's allow-list (which CAN contain `Bash`) is the only allow-list claude sees. If `Bash` is in the operator's allow-list, claude can run Bash — that's the operator's choice, not a bypass.
2. **Operator sets `--workdir /tmp` and `--prompt-file /etc/shadow`.** `requireRegularFile` accepts `/etc/shadow` if readable (it's not, for non-root users). If readable (root), its bytes flow into claude's PTY. Claude's TUI is the consumer; pyry does not exfiltrate. Operator already has root; threat model is self-inflicted.
3. **Operator sets `--prompt-file` to a file containing ANSI escapes that resize the claude TUI's perceived window.** Possible. Claude is the consumer of those bytes; the worst-case is a confused TUI render, not a permission bypass (settings file is independently consulted by claude's permission layer, not its renderer).
4. **Operator's prompt file is replaced between `requireRegularFile` and `os.ReadFile`.** TOCTOU. Operator-controlled both before and after; not a privilege escalation.
5. **Operator sets `--model evil-model`.** Passed verbatim to claude as `--model evil-model`. Claude validates; if it accepts an unknown model, that's a claude-side concern. No pyry-side amplification.
6. **Child claude refuses SIGTERM and forks.** `cmd.WaitDelay = 5s` SIGKILLs the parent process, NOT the process group. Forked children survive. **Documented limitation**, not a ticket-level fix. If we want pgid-kill semantics, that's `cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}` + `syscall.Kill(-pid, SIGKILL)`. Out of scope for #332; file a follow-up if the dispatcher observes orphan claude processes in practice. (Spike teardown does the same single-PID kill; not a regression.)
7. **Concurrent `pyry agent-run` invocations against the same workdir.** Settings file write races (last-writer-wins, #339 noted). Trust file write is locked (#341). PTY spawn itself is independent per-process. Adversary impact: bounded — the second invocation's settings file may transiently appear corrupted between rename steps (atomic-rename mitigates) but cannot leak privileges across invocations. Dispatcher serialises per-workdir.

## Verdict: PASS

The two load-bearing invariants (`--permission-mode default` present, `--allowedTools` absent) are pinned by unit tests in `cmd/pyry/agent_run_test.go`. The signal-handling, write-discipline, and shutdown-latency contracts are pinned by `internal/agentrun/drive_test.go` and `internal/supervisor/spawn_test.go`. No unsanitised input crosses a privilege boundary; no new network or secret surface; no shell. Pgid-kill is a noted limitation, not a blocker (consistent with the spike + existing supervisor behaviour).
