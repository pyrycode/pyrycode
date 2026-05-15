# `internal/agentrun/streamrunner` — no-PTY stream-json subprocess primitive

Headless sibling of [`agentrun.Drive`](agentrun-package.md): spawns `claude` as a plain subprocess (no PTY), writes one stream-json user-turn envelope to its stdin, forwards stdout/stderr to caller-supplied writers, and maps the child's exit to the verb-level contract. Caller assembles the full claude argv (including `--input-format stream-json --output-format stream-json --dangerously-skip-permissions`); this primitive owns only the spawn, the stdin envelope, and the ctx-cancel teardown.

Introduced #390 as a leaf primitive. Caller wiring lands in #391; until then the package has no production consumer.

## Public API

```go
type Config struct {
    ClaudeBin   string       // required; resolved path to claude
    WorkDir     string       // required; child cwd
    Args        []string     // full claude argv (excluding argv[0])
    PromptBytes []byte       // user-turn prompt text; UTF-8; JSON-encoded into the envelope
    Stdout      io.Writer    // required; forwarded child stdout. MUST NOT block.
    Stderr      io.Writer    // required; forwarded child stderr. MUST NOT block.
    Env         []string     // optional; appended to os.Environ() in the child
    Logger      *slog.Logger // optional; defaults to slog.Default()
}

// Run spawns claude with cfg.Args, writes one stream-json user-turn envelope
// to its stdin and closes stdin, forwards stdout/stderr, and waits for the
// child to exit.
//
// Returns:
//   - nil on clean (exit 0) child termination.
//   - nil on ctx-cancel-driven teardown — operator shutdown is success.
//   - *exec.ExitError on non-zero child exit not triggered by ctx cancel.
//   - a wrapped error from pre-Start setup (stdin pipe, spawn).
func Run(ctx context.Context, cfg Config) error
```

No other exported identifiers. The envelope is constructed from unexported `userTurn` / `userTurnMessage` / `userTurnContentText` structs.

## Envelope shape

A single newline-terminated JSON line matching the 2026-05-14 probe:

```json
{"type":"user","message":{"role":"user","content":[{"type":"text","text":"<prompt>"}]}}
```

`PromptBytes` is JSON-encoded via `encoding/json` — **not** shell-escaped — so embedded double-quotes, backslashes, newlines, and control characters round-trip cleanly. After the marshalled bytes, `Run` writes `'\n'` and closes stdin. The write is synchronous in the calling goroutine (single ~150-byte payload); failures of `Write` or `Close` are logged at Warn and not returned. Reason: the child may have already exited and its exit code is the authoritative outcome. Same logged-and-continued pattern as `drive.go`'s PTY writes.

## ctx-cancel teardown

Delegated entirely to stdlib's `os/exec`:

```go
cmd.Cancel    = func() error { return cmd.Process.Signal(syscall.SIGTERM) }
cmd.WaitDelay = 5 * time.Second
```

Stdlib's ctx-watcher invokes `cmd.Cancel` (SIGTERM) → child exits or 5s elapses → stdlib SIGKILLs + closes pipes → `cmd.Wait()` returns → `Run` returns nil. No package-owned goroutine, timer, or state machine. The 5s grace is hardcoded per AC ("no timing knobs") and mirrors `budget.GracePeriod` for symmetry.

Return mapping at exit:

```go
waitErr := cmd.Wait()
if ctx.Err() != nil { return nil }   // operator teardown is success
return waitErr                       // nil on exit 0; *exec.ExitError otherwise
```

## Logging discipline

The package logs **only**:

- Warn on stdin write or close failure (error message only).

It does **not** log:

- `cfg.PromptBytes` or any substring.
- The marshalled envelope bytes.
- Anything from `cfg.Stdout` / `cfg.Stderr` (opaque `io.Writer` values — the package never sees event payload content in a parseable form).

Pinned in the package doc-comment.

## Dependency direction

Imports are limited to stdlib: `context`, `encoding/json`, `errors`, `fmt`, `io`, `log/slog`, `os`, `os/exec`, `syscall`, `time`. Crucially, the package does **not** import `internal/supervisor` (the PTY helper) nor any sibling `internal/agentrun/*` subpackage — it is a leaf primitive. Verifiable by:

```bash
go list -deps ./internal/agentrun/streamrunner/... | grep pyrycode/internal/supervisor
```

Expected output: empty.

## Why a separate primitive

- `agentrun.Drive` (PTY-driven, interactive bridge mode) and `streamrunner.Run` (subprocess, headless stream-json mode) are different mechanisms for different claude invocation shapes. The 2026-05-14 probe confirmed that `claude --input-format stream-json --output-format stream-json --dangerously-skip-permissions` runs cleanly without a PTY, without a trust dialog, and without a JSONL tail watcher — stdin in, event stream out, exit code at the end. None of `Drive`'s PTY plumbing, defensive trust-dialog dismissal, or background-drain goroutine is needed.
- Keeping the two side-by-side (rather than overloading `Drive` with a no-PTY mode flag) preserves a clean test surface and a clean failure mode — readers of either function don't have to reason about both paths.
- The existing `internal/agentrun/streamjson` package is upstream of this one's eventual caller: it parses JSONL `Event`s and re-emits stream-json. `streamrunner` owns the subprocess + stdio pipe; `streamjson` owns the event-shape transformation. The two never compose inside this package.

## Testing

Same-package `_test.go` with a `TestStreamRunnerHelperProcess` fake claude (re-exec via `os.Args[0]` + `-test.run`). Modes keyed by `GO_STREAMRUNNER_HELPER_MODE`:

- `clean` — read stdin to EOF, write three deterministic stream-json lines (`system init` / `assistant text` / `result success`), exit 0.
- `exit1` — drain stdin briefly, exit 1.
- `sleep` — install SIGTERM handler that prints `"got SIGTERM"` to stderr and exits 0 within ~50ms; otherwise sleep 30s.
- `echo_stdin` — copy stdin to `GO_STREAMRUNNER_HELPER_STDIN_FILE` (mode 0o600), exit 0.

Four test cases against the four observable behaviours: clean exit (stdout substring check), non-zero exit (`errors.As(&exitErr)` + `ExitCode() == 1`), ctx-cancel mid-run (`Run` returns nil, elapsed < 6s, `"got SIGTERM"` on stderr — sanity-checks SIGTERM not SIGKILL was the trigger), and stdin envelope round-trip with a deliberately tricky prompt (embedded `"`, `\n`, `\\`, `\x01`) → assert the helper's captured file unmarshalls into the expected `userTurn` shape with `Content[0].Text` byte-for-byte equal to the input.

## Consumers

- `pyry agent-run` (deferred to #391) — caller assembles the full claude argv including `--input-format stream-json --output-format stream-json --verbose --dangerously-skip-permissions --allowed-tools … --model … --effort … --max-turns … --append-system-prompt-file …`, passes the prompt bytes through `Config.PromptBytes`, and threads the per-verb stdout/stderr writers. The current `Drive`-based path stays as the interactive bridge mode.

## Out of scope

- Parsing stream-json events on the way through — that's [`streamjson`](streamjson-package.md)'s concern, and this primitive's writers are opaque.
- Multi-turn drives — the AC pins single user-turn-then-close-stdin. Defer multi-turn until a consumer needs it.
- Operator-tunable timing knobs (trust-dialog delay, prompt delay, grace window) — none apply; stream-json mode has no trust dialog and no TUI write timing, and the SIGTERM grace is fixed.

## Related

- [agentrun-package.md](agentrun-package.md) — the PTY-driven sibling (`Drive`) this primitive parallels; shares the "ctx-cancel is success" return contract and the "log-and-continue on stdin write failure" pattern.
- [streamjson-package.md](streamjson-package.md) — the event-stream emitter that #391's caller will compose with this primitive's stdout writer.
- [pyry-agent-run-command.md](pyry-agent-run-command.md) — the verb that consumes both halves; will switch from `Drive` to `streamrunner.Run` once #391 lands.
- Spec [`docs/specs/architecture/390-streamrunner-primitive.md`](../../specs/architecture/390-streamrunner-primitive.md) — the build-time architect spec.
