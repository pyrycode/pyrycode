# Spec — Ticket #390: `internal/agentrun/streamrunner` stream-json subprocess primitive

Status: ready for developer
Size: S (1 production file, ~100 LOC, 0 consumer call sites — #391 is the first consumer)
Scope: new package `internal/agentrun/streamrunner`. No edits to existing code.

## Files to read first

- `internal/agentrun/drive.go:22-107` — `DriveConfig` / `Drive` pattern this primitive mirrors with one decisive deviation (no PTY, `exec.Cmd` directly). Read for: the `Bin / WorkDir / Args / Env / Logger / PromptBytes` config shape to mirror; the `waitAndMap` ctx-cancel-is-success contract; the "log Warn on write failure, keep going" stdin-write style.
- `internal/agentrun/drive_test.go:15-106` — `TestDriveHelperProcess` + `helperDriveCfg`. The new test file should clone this exact shape under `streamrunner/` with new env-var names (`GO_STREAMRUNNER_HELPER` / `GO_STREAMRUNNER_HELPER_MODE` / …) and modes scoped to the four observable behaviours below.
- `internal/agentrun/budget/budget.go:1-60, 158-188` — pinned constants: `GracePeriod` default = 5s, "SIGTERM grace → SIGKILL" wording the new package will reuse for symmetry. Read the doc comment and the `killAfterGrace` body; this primitive's stdlib-equivalent (`cmd.WaitDelay`) does the same thing for the ctx-cancel path.
- `internal/supervisor/supervisor.go` (only the `SpawnPTY` signature + `cmd.Cancel` / `cmd.WaitDelay` if present) — DO NOT IMPORT. Confirm the file lives under `internal/supervisor` so the dependency-direction lint (`go list -deps`) can assert this new package excludes it.
- `cmd/pyry/agent_run.go:199-260` — caller surface that will (in #391) build the claude argv and pass it through this primitive. Out of scope for #390 but useful to understand why this primitive owns nothing about flag shape.
- `docs/lessons.md` (only § "Background-drain prevents PTY block") — not directly applicable (no PTY) but the reasoning carries over: any consumer-supplied writer that blocks will deadlock `cmd.Wait()`. Document this in `Stdout/Stderr` doc-comments.

## Context

`pyry agent-run` will (in #391) drive `claude --input-format stream-json --output-format stream-json` headlessly: one user-turn JSON envelope on stdin → claude emits a full event stream on stdout → claude exits with a status. No PTY, no trust dialog, no JSONL watcher. The 2026-05-14 probe at `echo '{"type":"user",…}' | claude … --dangerously-skip-permissions` validated this end-to-end.

This ticket adds the subprocess primitive in isolation. #391 wires it into the verb command. The split keeps the spawn-and-stdio mechanism testable in unit-test space (a fake claude binary via `TestHelperProcess`) and separated from #391's argv-building and #335-era settings/trust orchestration.

The existing `internal/agentrun/drive.go` is the PTY-driven primitive used today and is **not** being replaced or modified — it stays as the bridge to `claude` in interactive bridge mode. The new package is its no-PTY sibling for the headless verb path.

## Design

### Package layout

```
internal/agentrun/streamrunner/
    runner.go          Config + Run + envelope marshalling (production)
    runner_test.go     Run behaviour tests via TestHelperProcess (test)
    helper_test.go     TestHelperProcess fake claude (test; lives in same package)
```

One package, one exported entry point. No sub-packages. No `New() *Runner` constructor — pure function call shape.

### Public surface (signatures only)

```go
package streamrunner

// Config configures Run. All `required` fields validated at entry; zero
// values for optional fields fall through to defaults documented below.
type Config struct {
    ClaudeBin   string       // required; resolved path to claude
    WorkDir     string       // required; child cwd
    Args        []string     // full claude argv (without argv[0]); caller assembles flag shape
    PromptBytes []byte       // user-turn prompt text; UTF-8; may contain quotes/newlines/control chars (JSON-encoded by Run)
    Stdout      io.Writer    // required; forwarded child stdout. MUST NOT block — a blocking writer deadlocks cmd.Wait().
    Stderr      io.Writer    // required; forwarded child stderr. Same non-blocking requirement.
    Env         []string     // optional; appended to os.Environ() in the child. Tests use this for TestHelperProcess wiring.
    Logger      *slog.Logger // optional; defaults to slog.Default()
}

// Run spawns claude with the configured argv, writes one stream-json
// user-turn envelope to its stdin and closes stdin, forwards stdout/stderr
// to the configured writers, and waits for the child to exit.
//
// Returns:
//   - nil on clean (exit 0) child termination
//   - nil on ctx-cancel-driven teardown (operator shutdown is success)
//   - *exec.ExitError on non-zero child exit that was NOT triggered by ctx cancellation
//   - wrapped error from spawn or stdin pipe setup (rare; only pre-Start failures)
//
// On ctx cancel: child receives SIGTERM, then SIGKILL after a 5-second
// grace (stdlib exec.Cmd.WaitDelay does this automatically once Cancel
// is set to SIGTERM).
func Run(ctx context.Context, cfg Config) error
```

No other exported identifiers. The envelope struct types are unexported.

### Run flow

1. **Validate config.** `ClaudeBin == ""` → `errors.New("streamrunner: ClaudeBin required")`. Same for `WorkDir`, `Stdout`, `Stderr`. `PromptBytes` may be nil/empty (operator passes whatever they want; empty is a valid edge but a no-op turn). Logger nil → `slog.Default()`.

2. **Marshal envelope.** Use a private struct:

   ```go
   type userTurn struct {
       Type    string         `json:"type"`     // "user"
       Message userTurnMessage `json:"message"`
   }
   type userTurnMessage struct {
       Role    string                  `json:"role"`     // "user"
       Content []userTurnContentText   `json:"content"`
   }
   type userTurnContentText struct {
       Type string `json:"type"` // "text"
       Text string `json:"text"` // string(cfg.PromptBytes)
   }
   ```

   `json.Marshal` handles escaping. Append `'\n'` to the marshalled bytes so the line is newline-terminated (matches the probe's `echo '...' |` newline). DO NOT log the marshalled bytes.

3. **Build `*exec.Cmd`** via `exec.CommandContext(ctx, cfg.ClaudeBin, cfg.Args...)`. Then:

   ```
   cmd.Dir       = cfg.WorkDir
   cmd.Stdout    = cfg.Stdout
   cmd.Stderr    = cfg.Stderr
   cmd.Env       = append(os.Environ(), cfg.Env...)  // only if Env != nil — match drive.go style
   cmd.Cancel    = func() error { return cmd.Process.Signal(syscall.SIGTERM) }
   cmd.WaitDelay = 5 * time.Second
   ```

   `cmd.Cancel` overrides stdlib's default (SIGKILL) so claude receives a graceful SIGTERM first. `cmd.WaitDelay` is stdlib's "after Cancel runs, wait this long for the process to exit, then SIGKILL and close pipes." This is the stdlib equivalent of `budget.killAfterGrace` and replaces hand-rolled SIGTERM/grace/SIGKILL goroutines entirely. (See `os/exec` doc: WaitDelay activates only when ctx is cancelled or stdin/stdout/stderr I/O is still pending after the process exits.)

   *Concurrency note:* `cmd.Cancel` runs on the stdlib's internal context-watcher goroutine; `cmd.Process` is set before `Cancel` can fire (stdlib invariant — Cancel is installed before Start observes the ctx).

4. **Open stdin pipe** via `stdin, err := cmd.StdinPipe()`. Error here is a setup failure → return `fmt.Errorf("streamrunner: stdin pipe: %w", err)`. No Start has happened yet; nothing to clean up.

5. **Start child** via `cmd.Start()`. Error → return `fmt.Errorf("streamrunner: start: %w", err)`. No goroutines or pipes leaked (StdinPipe's writer is closed by the failed Start path; stdlib handles this).

6. **Write envelope + close stdin.** In the calling goroutine (no extra concurrency needed — this is a single ~150-byte synchronous write):

   ```
   _, writeErr := stdin.Write(envelope)
   closeErr := stdin.Close()
   if writeErr != nil { logger.Warn("streamrunner: stdin write failed", "err", writeErr) }
   if closeErr != nil { logger.Warn("streamrunner: stdin close failed", "err", closeErr) }
   ```

   Failures are logged-and-continued, not returned. Reason: child may have already exited (especially in tests with `fast_exit` modes) — the child's exit code is the authoritative outcome, not whether stdin made it through. Identical pattern to `drive.go:93-104`.

7. **Wait for child.** `err := cmd.Wait()`. Return value mapping (collapse into one helper, mirror `drive.waitAndMap`):

   ```
   if ctx.Err() != nil { return nil }   // operator-driven teardown is success
   return err                           // nil on exit 0; *exec.ExitError otherwise
   ```

   Wait's WaitDelay path (forced SIGKILL after ctx cancel) returns the child's signal exit, which `ctx.Err() != nil` correctly masks to nil.

### Why `exec.Cmd.WaitDelay` instead of a goroutine

Stdlib (Go 1.20+) added `Cmd.Cancel` and `Cmd.WaitDelay` precisely for the SIGTERM-grace-SIGKILL pattern. Using them:
- removes a goroutine + timer + state machine the package would otherwise own;
- is observable to readers — the contract is in the `os/exec` docs;
- matches the "no timing knobs" AC by hardcoding 5s (operator-tunable knobs are a Phase-2 problem at most).

The `budget` package owns its own timer because it terminates mid-run on a separate signal (turn count) and the SIGTERM source isn't ctx cancellation. The two paths are orthogonal — `budget` calls `cmd.Process.Signal(SIGTERM)` directly from its `Terminate` hook; `streamrunner`'s ctx-cancel path doesn't compete with it.

### Concurrency model

- One goroutine: the caller's. The stdlib internally manages stdout/stderr forwarding goroutines (one each); these are stdlib's problem, not the package's.
- No package-owned channels.
- No package-owned timers — `WaitDelay` is the timer.
- Shutdown sequence on ctx cancel: stdlib's ctx-watcher invokes `cmd.Cancel` (SIGTERM) → child exits or 5s elapses → stdlib SIGKILLs + closes pipes → `cmd.Wait()` returns → `Run` returns nil.

### Error handling

- Spawn / stdin-pipe failures: wrapped with `"streamrunner: <stage>:"` prefix and returned.
- Stdin write/close failures: logged at Warn, NOT returned. Child's exit status is the authoritative signal.
- Non-zero child exit: returned verbatim as `*exec.ExitError` so callers can `errors.As` and read `ExitCode()`.
- Clean exit: nil.
- Ctx cancel: nil (regardless of child's signal-exit code).

### Logging discipline

The package logs:
- Warn on stdin write/close failure (error message only, no envelope bytes).
- (Optional) Info at completion with `duration_ms` and `exit_code` int — debate-worthy; recommend leaving it OUT of v1 to keep the primitive silent. The verb command in #391 will own structured completion logs.

The package MUST NOT log:
- `cfg.PromptBytes` or any substring of it.
- The marshalled envelope bytes.
- Anything from `cfg.Stdout` (it's not the package's role to inspect what passes through).

This is pinned in the package doc-comment and asserted by absence — no test specifically guards this since the package never receives the stream-json content in a parseable form (writers are opaque `io.Writer`s).

### Dependency direction (lint-able)

Imports allowed: `context`, `encoding/json`, `errors`, `fmt`, `io`, `log/slog`, `os`, `os/exec`, `syscall`, `time`.

Imports forbidden: anything under `github.com/pyrycode/pyrycode/internal/supervisor`, `internal/agentrun/jsonl`, `internal/agentrun/streamjson`, `internal/agentrun/budget`, `internal/agentrun/trust`. The AC's "must not import supervisor's PTY" is the load-bearing one; the others are aspirational (this is a leaf primitive — adding any of them would imply the wrong responsibility).

Verifiable by:

```
go list -deps ./internal/agentrun/streamrunner/... | grep pyrycode/internal/supervisor
```

Expected: empty output. Add this as a comment in the package doc; a `TestStreamrunner_NoSupervisorImport` build-tag-free unit test using `go/packages` or `go list` is overkill — the developer's CI passes `go vet ./...` which catches accidental imports compilation-wise, and a code-review check is sufficient.

## Testing strategy

Same-package `_test.go` files. Stdlib `testing` only (project convention; no testify). The `TestHelperProcess` re-exec pattern is required because we need a real child process to exercise stdin/stdout/SIGTERM.

### `helper_test.go` — fake claude

Environment variables (clone of `drive_test.go`'s style):

- `GO_STREAMRUNNER_HELPER=1` — gates the helper test from real Go test runs.
- `GO_STREAMRUNNER_HELPER_MODE` — one of `clean | exit1 | sleep | echo_stdin`.
- `GO_STREAMRUNNER_HELPER_STDIN_FILE` — only for `echo_stdin`; absolute path the helper writes the received envelope to before exiting.

Modes (one short paragraph each — developer writes the Go bodies):

- **`clean`** — read stdin until EOF; write a fixed three-line stream-json sequence to stdout (`system init` → `assistant text` → `result`); exit 0.
- **`exit1`** — read stdin briefly; write nothing useful; exit 1.
- **`sleep`** — read stdin until EOF; install SIGTERM handler that prints "got SIGTERM" to stderr and exits 0 within ~50ms; otherwise sleep 30s. Used by ctx-cancel test.
- **`echo_stdin`** — read stdin to EOF; write the bytes verbatim to `GO_STREAMRUNNER_HELPER_STDIN_FILE` (mode 0o600); exit 0.

### `runner_test.go` — four scenarios

For each: a `helperRunCfg(t, mode, extraEnv...) Config` constructor mirroring `drive_test.go`'s `helperDriveCfg`. Sets `ClaudeBin = os.Args[0]`, `Args = ["-test.run=TestStreamRunnerHelperProcess", "--"]`, supplies `t.TempDir()` as `WorkDir`, captures `Stdout`/`Stderr` to `bytes.Buffer`s.

- **`TestRun_CleanExit`** — mode `clean`, ctx with 5s timeout. Assert: `Run` returns nil, `stdoutBuf` contains the three lines (string-contains check on each `"type":"<x>"` substring is enough — round-trip parsing is not this package's job), `stderrBuf` empty.

- **`TestRun_NonZeroExit`** — mode `exit1`, ctx with 5s timeout. Assert: `Run` returns non-nil; `errors.As(err, &exitErr)` succeeds; `exitErr.ExitCode() == 1`.

- **`TestRun_CtxCancelMidRun`** — mode `sleep`, parent cancels ctx after 100ms. Assert: `Run` returns nil; total elapsed < 6s (well under the 5s WaitDelay; sleep helper exits on SIGTERM in ~50ms); `stderrBuf` contains `"got SIGTERM"` substring (sanity check that SIGTERM was the trigger, not SIGKILL).

- **`TestRun_StdinEnvelopeRoundTrip`** — mode `echo_stdin`, `PromptBytes` set to a deliberately tricky string with embedded double-quote, newline, backslash, and U+0001 control char (e.g. `"hello \"world\"\n\x01end"` as raw bytes). After `Run` returns nil, read `GO_STREAMRUNNER_HELPER_STDIN_FILE`; `json.Unmarshal` it into a local `userTurn` struct mirror; assert `Type == "user"`, `Message.Role == "user"`, `len(Message.Content) == 1`, `Message.Content[0].Type == "text"`, `Message.Content[0].Text == string(promptBytes)`. This is the load-bearing assertion that JSON encoding (not shell escaping) is the path.

No e2e tests in this ticket — that's #391's bailiwick once the primitive is wired to the verb. CI flake budget: keep all four under 6s wall-clock.

### Race and shutdown

`go test -race ./internal/agentrun/streamrunner/...` must pass clean. The package has no shared state, so race issues would have to come from cmd.Cancel's goroutine racing the main goroutine on `cmd.Process` — stdlib guarantees `Process` is set before Cancel runs; we don't touch `cmd.Process` from the calling goroutine, so there's no race surface inside the package.

## Open questions

- **Should `Run` return the exit code as an int even on clean exit?** No — the AC says "returns the child's exit error: nil on clean exit, `*exec.ExitError` on non-zero." Sticking to that. If #391 wants the exit code, it `errors.As`s out the `*exec.ExitError`.
- **Should we log a "child started" line with the resolved binary + workdir?** No — adds noise that the verb command at #391 will already log at a more useful granularity.
- **Should the package support multiple user turns?** No — AC#3 is explicit: "a single … JSON line followed by stdin close." Multi-turn is a different primitive; defer until needed.

## Acceptance trace

| AC | Where satisfied |
|---|---|
| AC#1 (Run signature, exit-error semantics) | `runner.go` `Run` function; `waitAndMap`-equivalent tail. |
| AC#2 (Config fields, no PTY, no timing knobs) | `Config` struct above; `WaitDelay = 5s` hardcoded. |
| AC#3 (single-line JSON envelope, stdin close, JSON-encoded prompt) | Step 2 of Run flow; `TestRun_StdinEnvelopeRoundTrip`. |
| AC#4 (SIGTERM grace then SIGKILL on ctx cancel; Run returns nil) | `cmd.Cancel` + `cmd.WaitDelay`; `TestRun_CtxCancelMidRun`. |
| AC#5 (no prompt-byte or event-payload logging) | "Logging discipline" section; absence-asserted; package owns no event-parsing code. |
| Implicit (no supervisor.SpawnPTY import) | `go list -deps` check noted; only `os/exec` + `syscall` used. |
