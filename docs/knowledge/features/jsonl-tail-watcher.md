# `internal/agentrun/jsonl/tail` — fsnotify tail watcher for claude session JSONL

The filesystem-orchestration layer between `tuidriver.SessionJSONLPath` ([tui-driver](https://github.com/pyrycode/tui-driver), #509) and the pure JSONL line reader ([jsonl-reader.md](jsonl-reader.md), #348). Waits for `~/.claude/projects/<encoded-cwd>/<sid>.jsonl` to appear via `tuidriver.WaitForSessionJSONL` (50ms poll), opens it, and drives a `jsonl.Reader` over fsnotify WRITE events until the deterministic end-of-turn signal fires or the caller cancels. Follow-up [#512](https://github.com/pyrycode/pyrycode/issues/512) will replace the per-line read loop with `tuidriver.TailJSONL` and delete this package.

## Why a sibling subpackage

`internal/agentrun/jsonl/` is a pure parser today — no fs, no fsnotify, no goroutines. Keeping it that way preserves the parser's testability against `io.Reader` fixtures and makes the dependency direction unambiguous: `tail` imports `jsonl`, never the other way around. Mirrors the existing `internal/sessions/rotation/` shape: a small, single-purpose subpackage that wraps an `fsnotify.Watcher` with project-specific lifecycle.

## Public API

```go
package tail

type Config struct {
    Workdir     string             // pyry's agent-run workdir
    SessionID   string             // claude's session UUID (the <sid>.jsonl stem)
    HomeDir     string             // ~/; injectable for tests (defaults to os.UserHomeDir on empty)
    StartOffset int64              // resume hint; passed to jsonl.Config + used to Seek the file on open
    OnEvent     func(jsonl.Event)  // required; invoked per assistant event from the Run goroutine
    OnEndOfTurn func()             // required; invoked at most once; after it returns, Run returns nil
    Logger      *slog.Logger       // optional; defaults to slog.Default()
}

func New(cfg Config) (*Watcher, error)
func (w *Watcher) Run(ctx context.Context) error
func (w *Watcher) Offset() int64
```

Two exported types, three exported funcs/methods. Caller MUST call `Run` after `New` returns — `fsnotify.Watcher.Close()` happens via `defer` inside `Run`. Same contract shape as `rotation.Watcher`; same risk; same mitigation (test helper calls `Run` in a goroutine + `t.Cleanup` cancels).

## Lifecycle

```
New(cfg):
  validate Workdir / SessionID / OnEvent / OnEndOfTurn non-zero
  home         = cfg.HomeDir or os.UserHomeDir()
  expectedPath = tuidriver.SessionJSONLPath(home, cfg.Workdir, SessionID)
                                                                 //   ↳ EncodeCwd
                                                                 //     ↳ canonicalisePath (darwin F_GETPATH / linux EvalSymlinks)
  dir          = filepath.Dir(expectedPath)
  os.MkdirAll(dir, 0o700)                                        // first-run-in-this-workdir case
  fsw = fsnotify.NewWatcher(); fsw.Add(dir)

Run(ctx):
  defer fsw.Close() + close opened file
  (1) tuidriver.WaitForSessionJSONL(ctx, expectedPath)            // 50ms poll, returns on appearance / ctx
  (2) openAndDrain() — open, seek to StartOffset, construct jsonl.Reader, drain initial bytes
  (3) for { select ctx.Done / fsw.Events / fsw.Errors }
        WRITE/CREATE for expectedPath → drain
        any other event               → ignore
        fsw.Errors                    → log Warn, continue
        ctx.Done                      → return ctx.Err()
```

`openAndDrain` opens the file (no bounded retry — `WaitForSessionJSONL` already guaranteed appearance), seeks to `StartOffset > 0`, constructs the `jsonl.Reader` against the live `*os.File`, and drains. `drain` calls `reader.Next()` in a loop, invoking `OnEvent` on each event; on `EndOfTurn=true` it invokes `OnEndOfTurn` and returns `(done=true, nil)`. On `io.EOF` it returns `(done=false, nil)` — the file hasn't grown yet, wait for the next fsnotify event. Any non-EOF reader error bails the Run.

Canonicalisation is delegated to `tuidriver.EncodeCwd` (called inside `tuidriver.SessionJSONLPath`): darwin uses `F_GETPATH` via the open fd, linux uses `filepath.EvalSymlinks`. The pre-[#508](../codebase/508.md) shape called `agentrun.ResolveWorkdir(cfg.Workdir)` upstream as a fail-closed guard against missing-workdir typos; [#508](../codebase/508.md) deleted that explicit call after auditing that the upstream `ptyrunner` caller already pre-checks the workdir existence (`cmd.Start` fails on a non-existent `Dir: cfg.WorkDir` before `tail.New` is reached), so the eager check was defending against a structurally impossible state. The single canonicalisation happens once inside `EncodeCwd`; on resolution failure (which cannot reach this code path in production), `EncodeCwd` encodes the input as-passed and `MkdirAll` would create a wrong-named directory — surfaced downstream as `WaitForSessionJSONL` blocking until ctx-cancel rather than an early `fs.ErrNotExist`. See [codebase/508.md](../codebase/508.md) § Lessons learned for the audit that justified removing the dual layer.

## Project-directory existence — resolved

The encoded project dir `~/.claude/projects/<encoded-cwd>/` may not exist at spawn time on the very first run in a workdir. The watcher resolves this by `MkdirAll(dir, 0o700)` at `New` time **before** `fsnotify.Add(dir)`, mirroring `rotation.New` (`internal/sessions/rotation/watcher.go:97-107`). Idempotent: if claude created the dir already, MkdirAll is a no-op; if pyry creates it first, claude is observed to be content writing into a pre-existing dir. No two-level watch needed.

If a future revision ever observes claude refusing to write into a pre-created dir, escalate to a two-level watch via follow-up. Defer until empirically observed.

## Appearance race — handled by `tuidriver.WaitForSessionJSONL`

`Run`'s first action is `tuidriver.WaitForSessionJSONL(ctx, expectedPath)`: an initial `os.Stat` short-circuits if the file already exists, otherwise a 50ms `DefaultPollInterval` ticker loops until appearance, ctx cancellation, deadline, or a non-NotExist stat error short-circuits. The fsnotify subscription is armed in `New` before `Run` is called, so any WRITE events that arrive during the wait are buffered in `fsw.Events` (4096-deep default channel) and consumed by the event loop after the initial drain. No double-open is possible: only the up-front wait calls `openAndDrain`, and the event loop only calls `drain` on an already-open reader.

The pre-#509 design used `os.Stat` + `fsnotify.CREATE` event + `openWithRetry` walking `probeRetryDelays = {0, 50ms, 200ms}` to absorb the "CREATE fires before claude has the file ready" race; #509 collapsed all three into the single `tuidriver.WaitForSessionJSONL` call. The polling cadence is smoother (50ms throughout) than the previous stepped 0/50/200ms schedule, faster on average, and never slower than 50ms.

## Concurrency model

One goroutine: `Run`. No background goroutines, no inner `errgroup`. `OnEvent` and `OnEndOfTurn` are invoked synchronously from the Run goroutine; callers that need to push events to another goroutine should send on a channel from the callback. `Offset()` is safe to call only after `Run` returns — it reads the underlying reader's offset without a lock and is NOT safe to call concurrently with `Run`. Documented on the method.

## Resume contract

`Config.StartOffset` flows through two places: `f.Seek(StartOffset, SeekStart)` on open, and `jsonl.Config.StartOffset` on reader construction. Together these make `Offset()` report **absolute file positions** so the caller can persist the value as a durable resume point. The watcher itself does not persist anything — durability is the caller's responsibility. Out of scope for #349.

## Error classes

| Class | Surface |
|---|---|
| `OnEndOfTurn` fired | `Run` returns `nil` |
| Context cancelled / deadline exceeded during wait | `Run` returns `tuidriver.WaitForSessionJSONL`'s wrap — `fmt.Errorf("session jsonl %s did not appear: %w", path, context.Cause(ctx))`; `errors.Is(err, context.Canceled)` / `errors.Is(err, context.DeadlineExceeded)` match through the `%w` wrap |
| Context cancelled during the event loop | `Run` returns `ctx.Err()` |
| `tuidriver.WaitForSessionJSONL` non-NotExist stat error (e.g. ENOTDIR) | `Run` returns the wrapped error (no polling) |
| `jsonl.Reader` returns `ErrLineTooLarge` or non-EOF read error | `Run` returns the wrapped error |
| `os.Open` after wait returns nil (TOCTOU on file vanishing) | `Run` returns `tail: open %s: %w` |
| `fsw.Errors` channel emits | Logged Warn, event loop continues |
| `fsw.Events` channel closes | `Run` returns `nil` (watcher externally closed) |

## Trust boundary

Same `MUST NOT log file contents at any layer` stance as the parser and the agentrun primitives ([jsonl-reader.md](jsonl-reader.md), [agentrun-package.md](agentrun-package.md)). The watcher logs only paths, offsets, and error messages — never line bytes. Pinned in the package doc-comment.

## Tests

`internal/agentrun/jsonl/tail/watcher_test.go` — eight test functions, all `t.Parallel()`. The `startWatcher(t, cfg)` helper runs `Run` in a goroutine and registers a `t.Cleanup` that cancels and asserts Run exits within 2s. An `eventRecorder` (mutex-wrapped) collects `OnEvent` invocations and end-turn fires; `waitForEndOfTurn` polls the recorder against a deadline rather than `time.Sleep`-then-check. The `expectedEncodedDir(t, home, workdir, sessionID)` helper mirrors the production-code path computation exactly (`filepath.Dir(tuidriver.SessionJSONLPath(home, workdir, sessionID))` since [#508](../codebase/508.md)) so test and watcher agree on the directory by construction.

| Test | Pins |
|---|---|
| `TestNew_ValidationErrors` | Empty `Workdir` / `SessionID` and nil `OnEvent` / `OnEndOfTurn` each return an error with the expected substring |
| `TestNew_RealpathEncoding` (darwin-only) | A `t.TempDir()` under `/var/folders/...` watches `~/.claude/projects/-private-var-folders-...`, NOT `-var-folders-...` |
| `TestWatcher_LateCreate` | Watcher started before JSONL exists; `WaitForSessionJSONL` ticks until the file appears → WRITE events drive parsing → end-of-turn fires once |
| `TestWatcher_ExistingFile` | JSONL pre-exists at Run; `WaitForSessionJSONL`'s initial stat short-circuits, file opens immediately; `Run` returns nil after end-of-turn |
| `TestWatcher_FixtureIntegration` | Replays `internal/agentrun/jsonl/testdata/clean.jsonl` line-by-line into a tempdir-hosted fake home; asserts 25 events + 1 end-of-turn fire, last event `EndOfTurn=true` |
| `TestWatcher_ContextCancellation` | Cancellation while waiting for the JSONL to appear → `Run` returns `context.Canceled` (through `WaitForSessionJSONL`'s `%w` wrap) within 500ms; goroutine teardown clean |
| `TestWatcher_WaitTimeout` (#509) | `context.WithTimeout(150ms)` while JSONL never appears → `Run` returns `context.DeadlineExceeded` (through the `%w` wrap) within 500ms of the deadline |
| `TestWatcher_SpecialCharWorkdir` (#509) | Workdir literal `"a_b c.d"` → `filepath.Base(w.dir)` ends with `-a-b-c-d`; encoded dir exists on disk. Spot-check that the encoding seam reaches `tuidriver.EncodeCwd`'s per-byte rule (not the pre-[#501](../codebase/501.md) narrow replacer) |

`writeLineByLine` uses `O_CREATE|O_WRONLY|O_APPEND` between writes with `Sync` after each line; the fixture-integration test does the same via `bufio.Scanner` over the real fixture.

## Consumers

- The `pyry agent-run` verb ([pyry-agent-run-command.md](pyry-agent-run-command.md)) will own the resume-offset snapshot, max-turn enforcement, and the dispatcher hand-off (out of scope for #349; pickup by sibling work that wires the verb).
- No other consumers in this slice. Greenfield package — no migrations, no shims.

## Out of scope

- Resume-offset persistence — caller snapshots `Offset()` after `Run` returns; durable store TBD.
- Max-turn enforcement loop — consumes `OnEvent` and `OnEndOfTurn` from the verb layer.
- Two-level watch on `~/.claude/projects/` — deferred unless claude refuses to write into a pre-created dir (not observed).
- Multi-session / multi-watcher orchestration — one `Watcher` per session.

## Related

- [jsonl-reader.md](jsonl-reader.md) — the parser this watcher drives. To be replaced by `tuidriver.TailJSONL` in [#512](https://github.com/pyrycode/pyrycode/issues/512).
- [agentrun-package.md](agentrun-package.md) — owns `ResolveWorkdir` (no longer called from this watcher post-[#508](../codebase/508.md); sole remaining caller is `agentrun/trust`). The historical `EncodeProjectDir` wrapper (#347 → delegated in [#501](../codebase/501.md) → deleted in [#508](../codebase/508.md)) is gone; the encoder lives directly in `tuidriver.EncodeCwd`.
- [tui-driver](https://github.com/pyrycode/tui-driver) `pkg/tuidriver/jsonl.go` — library home of `SessionJSONLPath` + `WaitForSessionJSONL` + `DefaultPollInterval = 50ms` (tui-driver #58, commit `a2edf4f`, 2026-05-22).
- [codebase/509.md](../codebase/509.md) — migration ticket: path resolution + file-appear wait delegated to tui-driver.
- [codebase/501.md](../codebase/501.md) — encoder delegation prerequisite (per-byte rule now lives in `tuidriver.EncodeCwd`).
- [rotation-watcher.md](rotation-watcher.md) — sibling fsnotify watcher; still uses its own `probeRetryDelays` (different code, different package).
- [ADR 004](../decisions/004-fsnotify-for-rotation-detection.md) — the fsnotify-over-polling rationale that applies equally here.
