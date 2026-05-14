# `internal/agentrun/jsonl/tail` — fsnotify tail watcher for claude session JSONL

The filesystem-orchestration layer between `agentrun.EncodeProjectDir` ([agentrun-package.md](agentrun-package.md), #347) and the pure JSONL line reader ([jsonl-reader.md](jsonl-reader.md), #348). Waits for `~/.claude/projects/<encoded-cwd>/<sid>.jsonl` to appear via fsnotify, opens it (with bounded retry to absorb the "CREATE fires before claude has the file ready" race), and drives a `jsonl.Reader` until the deterministic end-of-turn signal fires or the caller cancels.

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
  home = cfg.HomeDir or os.UserHomeDir()
  dir  = home + "/.claude/projects/" + agentrun.EncodeProjectDir(Workdir)
  os.MkdirAll(dir, 0o700)                          // first-run-in-this-workdir case
  fsw = fsnotify.NewWatcher(); fsw.Add(dir)
  expectedPath = dir + "/" + SessionID + ".jsonl"

Run(ctx):
  defer fsw.Close() + close opened file
  (1) os.Stat(expectedPath)
        on ok  → openAndDrain (covers pre-existing file)
  (2) for { select ctx.Done / fsw.Events / fsw.Errors }
        CREATE for expectedPath + file not yet opened → openAndDrain
        WRITE/CREATE for expectedPath + file opened    → drain
        any other event                                → ignore
        fsw.Errors                                     → log Warn, continue
        ctx.Done                                       → return ctx.Err()
```

`openAndDrain` opens the file with bounded retry, seeks to `StartOffset > 0`, constructs the `jsonl.Reader` against the live `*os.File`, and drains. `drain` calls `reader.Next()` in a loop, invoking `OnEvent` on each event; on `EndOfTurn=true` it invokes `OnEndOfTurn` and returns `(done=true, nil)`. On `io.EOF` it returns `(done=false, nil)` — the file hasn't grown yet, wait for the next fsnotify event. Any non-EOF reader error bails the Run.

## Project-directory existence — resolved

The encoded project dir `~/.claude/projects/<encoded-cwd>/` may not exist at spawn time on the very first run in a workdir. The watcher resolves this by `MkdirAll(dir, 0o700)` at `New` time **before** `fsnotify.Add(dir)`, mirroring `rotation.New` (`internal/sessions/rotation/watcher.go:97-107`). Idempotent: if claude created the dir already, MkdirAll is a no-op; if pyry creates it first, claude is observed to be content writing into a pre-existing dir. No two-level watch needed.

If a future revision ever observes claude refusing to write into a pre-created dir, escalate to a two-level watch via follow-up. Defer until empirically observed.

## CREATE-before-ready race

fsnotify on Linux + macOS will fire `CREATE` for the file as soon as `open(O_CREAT)` returns in claude — which can be a few ms before claude writes the first byte. `openWithRetry` walks `probeRetryDelays = []time.Duration{0, 50ms, 200ms}` (250ms total worst-case), mirroring `rotation.probeWithRetry`. On exhaustion (file genuinely went away — spurious CREATE) the watcher logs at Warn and continues the event loop; a later WRITE may surface the file. Permission-denied and other non-ENOENT errors bail immediately.

## Initial-stat covers the pre-existing-file race

`fsnotify.Add(dir)` is called in `New`; `Run`'s first action is `os.Stat(expectedPath)`. Order matters: any CREATE that races with the stat is queued in `fsw.Events` and replayed by the event loop, so the watcher never double-opens. If the stat succeeds, the file is opened immediately (no fsnotify event needed); if the stat returns `fs.ErrNotExist`, the event loop takes over.

## Concurrency model

One goroutine: `Run`. No background goroutines, no inner `errgroup`. `OnEvent` and `OnEndOfTurn` are invoked synchronously from the Run goroutine; callers that need to push events to another goroutine should send on a channel from the callback. `Offset()` is safe to call only after `Run` returns — it reads the underlying reader's offset without a lock and is NOT safe to call concurrently with `Run`. Documented on the method.

## Resume contract

`Config.StartOffset` flows through two places: `f.Seek(StartOffset, SeekStart)` on open, and `jsonl.Config.StartOffset` on reader construction. Together these make `Offset()` report **absolute file positions** so the caller can persist the value as a durable resume point. The watcher itself does not persist anything — durability is the caller's responsibility. Out of scope for #349.

## Error classes

| Class | Surface |
|---|---|
| `OnEndOfTurn` fired | `Run` returns `nil` |
| Context cancelled | `Run` returns `ctx.Err()` |
| `jsonl.Reader` returns `ErrLineTooLarge` or non-EOF read error | `Run` returns the wrapped error |
| `os.Open` retry exhausted with `fs.ErrNotExist` | Logged Warn, event loop continues |
| `os.Open` non-ENOENT (e.g. permission denied) | `Run` returns the wrapped error |
| `fsw.Errors` channel emits | Logged Warn, event loop continues |
| `fsw.Events` channel closes | `Run` returns `nil` (watcher externally closed) |

## Trust boundary

Same `MUST NOT log file contents at any layer` stance as the parser and the agentrun primitives ([jsonl-reader.md](jsonl-reader.md), [agentrun-package.md](agentrun-package.md)). The watcher logs only paths, offsets, and error messages — never line bytes. Pinned in the package doc-comment.

## Tests

`internal/agentrun/jsonl/tail/watcher_test.go` — six test functions, all `t.Parallel()`. The `startWatcher(t, cfg)` helper runs `Run` in a goroutine and registers a `t.Cleanup` that cancels and asserts Run exits within 2s. An `eventRecorder` (mutex-wrapped) collects `OnEvent` invocations and end-turn fires; `waitForEndOfTurn` polls the recorder against a deadline rather than `time.Sleep`-then-check.

| Test | Pins |
|---|---|
| `TestNew_ValidationErrors` | Empty `Workdir` / `SessionID` and nil `OnEvent` / `OnEndOfTurn` each return an error with the expected substring |
| `TestNew_RealpathEncoding` (darwin-only) | A `t.TempDir()` under `/var/folders/...` watches `~/.claude/projects/-private-var-folders-...`, NOT `-var-folders-...` |
| `TestWatcher_LateCreate` | Watcher started before JSONL exists; CREATE fires → file opened with retry → WRITE events drive parsing → end-of-turn fires once |
| `TestWatcher_ExistingFile` | JSONL pre-exists at Run; initial-stat picks it up without any fsnotify event; `Run` returns nil after end-of-turn |
| `TestWatcher_FixtureIntegration` | Replays `internal/agentrun/jsonl/testdata/clean.jsonl` line-by-line into a tempdir-hosted fake home; asserts 25 events + 1 end-of-turn fire, last event `EndOfTurn=true` |
| `TestWatcher_ContextCancellation` | Cancellation while waiting for the JSONL to appear → `Run` returns `context.Canceled` within 500ms; goroutine teardown clean |

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

- [jsonl-reader.md](jsonl-reader.md) — the parser this watcher drives.
- [agentrun-package.md](agentrun-package.md) — `EncodeProjectDir` (#347) supplies the directory name.
- [rotation-watcher.md](rotation-watcher.md) — the pattern reference (fsnotify lifecycle + bounded retry-probe shape).
- [ADR 004](../decisions/004-fsnotify-for-rotation-detection.md) — the fsnotify-over-polling rationale that applies equally here.
