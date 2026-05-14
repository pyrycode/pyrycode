# Spec: agent-run/jsonl tail watcher (#349)

Ties the path-encoding helper (`agentrun.EncodeProjectDir`, #347) and the JSONL line reader (`jsonl.Reader`, #348) together behind a single fsnotify-driven watcher. The watcher waits for `~/.claude/projects/<encoded-cwd>/<sid>.jsonl` to appear, opens it, and feeds appended bytes into the line reader until the deterministic end-of-turn signal fires (or the caller cancels).

## Files to read first

- `internal/agentrun/jsonl/reader.go:60-99` — `Config` + `Reader` constructor; note that `Reader` is pull-based and EOF is **non-sticky** (the same `*os.File` can return more bytes on a later read once new data is appended).
- `internal/agentrun/jsonl/reader.go:120-192` — `Next()` / `Offset()` contract: returns `io.EOF` only when both the current read produced 0 bytes AND the source signalled EOF; `Offset()` advances past every consumed line including silently-skipped non-assistant ones.
- `internal/agentrun/trust.go:41-51` — `EncodeProjectDir(workdir)` contract: chains `ResolveWorkdir` (resolves symlinks; macOS `/var` → `/private/var`) then maps `/` and `.` to `-`. Returns the dashed name WITHOUT the `~/.claude/projects/` prefix.
- `internal/sessions/rotation/watcher.go:1-220` — the pattern reference: fsnotify `NewWatcher` + `Add(dir)`, single Run goroutine with `select { ctx.Done / fsw.Events / fsw.Errors }`, CREATE-event matching, bounded-retry probe for the "CREATE fires before claude has the file ready" race. **Do not import this package**; copy the loop shape, not the rotation-specific match logic.
- `internal/sessions/rotation/watcher_test.go:60-91` — the `startWatcher` helper pattern (Run in a goroutine, `t.Cleanup` cancels + waits for goroutine exit with timeout). Reuse this shape.
- `internal/sessions/rotation/watcher_test.go:96-131` — assertion pattern: poll a thread-safe recorder with a deadline rather than `time.Sleep`.
- `internal/agentrun/jsonl/testdata/clean.jsonl` — real fixture used by `reader_test.go`; reuse for the integration test by reading it line-by-line and re-emitting into a tempdir-hosted fake JSONL.
- `docs/lessons.md` § "Don't trust ticket bodies on filesystem layout — observe" (line 52) — files live **directly** in `<encoded-cwd>/`, no `sessions/` subdir. Pinned for the integration test path construction.

## Context

The agent-run verb (#338, not yet wired) needs to observe the assistant's entire turn from outside the claude process. Claude writes its per-session JSONL to `~/.claude/projects/<encoded-cwd>/<sid>.jsonl`; this file does not exist when claude spawns and is appended-to line by line.

#347 shipped the path encoder; #348 shipped the line parser with a deterministic `EndOfTurn` signal. This ticket is the filesystem orchestration that sits between them.

## Design

### Package layout

New sibling sub-package of `internal/agentrun/jsonl/`:

```
internal/agentrun/jsonl/tail/
  watcher.go        // ~150 lines: Config, Watcher, New, Run, Offset
  watcher_test.go   // tests for the four AC scenarios
```

Package name: `tail`. Import path: `github.com/pyrycode/pyrycode/internal/agentrun/jsonl/tail`. Mirrors the existing `internal/sessions/rotation/` sub-package pattern: a small, single-purpose package that wraps an `fsnotify.Watcher` with project-specific lifecycle.

Rationale for "subpackage, not same package": `internal/agentrun/jsonl/` is a pure parser today (no fs, no fsnotify). Keeping it that way preserves the parser's testability (it consumes `io.Reader` fixtures with no filesystem) and makes the dependency direction obvious: `tail` depends on `jsonl`, never the other way around.

### Project-dir existence (resolves the architect-to-confirm question)

The encoded project dir `~/.claude/projects/<encoded-cwd>/` may not exist at spawn time on the very first run in a workdir. We resolve this by **MkdirAll-ing the encoded dir at `New` time before adding the fsnotify watch**, mirroring exactly what `rotation.New` does (`watcher.go:97-107`).

- Mode `0o700` matches the existing pattern.
- Idempotent: if claude created the dir already, `MkdirAll` is a no-op.
- If pyry creates it first, claude is observed to be content writing into a pre-existing dir (this is how claude already behaves when the parent `~/.claude/projects/` exists).
- No two-level watch needed. This stays inside the "Simplicity first" principle and avoids dynamic watch addition.

If empirically a future revision finds claude refuses to write into a pre-created dir, escalate to a two-level watch via a follow-up; until that's observed, defer (per the "Evidence-Based Fix Selection" principle).

### Public API

```go
package tail

type Config struct {
    Workdir     string          // pyry's agent-run workdir
    SessionID   string          // claude's session UUID (the <sid>.jsonl stem)
    HomeDir     string          // ~/; injectable for tests (defaults to os.UserHomeDir on empty)
    StartOffset int64           // resume hint; passed to jsonl.Config.StartOffset and used to Seek
    OnEvent     func(jsonl.Event) // invoked per assistant event from the Run goroutine
    OnEndOfTurn func()            // invoked at most once; after it returns, Run returns nil
    Logger      *slog.Logger
}

func New(cfg Config) (*Watcher, error)
func (w *Watcher) Run(ctx context.Context) error
func (w *Watcher) Offset() int64
```

Five exported names (`Config`, `Watcher`, `New`, `Run`, `Offset`). Two exported types.

### `New` contract

1. Validate: `Workdir`, `SessionID` non-empty; `OnEvent` non-nil; `OnEndOfTurn` non-nil. (`Logger` defaults to `slog.Default()`.)
2. `HomeDir`: if empty, call `os.UserHomeDir()`.
3. Compute `dir = filepath.Join(HomeDir, ".claude/projects", encoded)` where `encoded = agentrun.EncodeProjectDir(Workdir)`. `EncodeProjectDir` already does symlink resolution; the `dir` we hand to fsnotify is therefore the canonical form.
4. `os.MkdirAll(dir, 0o700)`.
5. `fsnotify.NewWatcher()`, then `Add(dir)`. On failure, close and return wrapped error.
6. Stash `expectedPath = filepath.Join(dir, SessionID+".jsonl")` on the Watcher.

Errors: `nil` only on success. Validation errors are `"tail: empty Workdir"`-style; system errors wrap as `"tail: mkdir %s: %w"`, `"tail: fsnotify: %w"`, etc.

### `Run` contract

`Run(ctx)` blocks. It returns:
- `nil` after `OnEndOfTurn` has been invoked and returned.
- `ctx.Err()` when `ctx` is cancelled before end-of-turn fires.
- A wrapped error on unrecoverable I/O failure (e.g. `jsonl.ErrLineTooLarge`, or `reader.Next()` returning a non-EOF error).

Lifecycle (single goroutine — same shape as `rotation.Watcher.Run`):

```
defer fsw.Close()

// (1) Initial stat covers the race where the file already exists.
//     Order matters: fsnotify.Add has already been called by New, so any
//     CREATE that races with the stat is queued in fsw.Events.
if f, err := os.Open(expectedPath); err == nil {
    open f, construct reader, seek to StartOffset if > 0, drain
    if end-of-turn fired → invoke OnEndOfTurn, return nil
}

// (2) Event loop.
for {
    select {
    case <-ctx.Done():
        return ctx.Err()
    case ev, ok := <-fsw.Events:
        if !ok { return nil }
        if ev.Name != expectedPath { continue }
        if !opened && ev.Op.Has(fsnotify.Create) {
            open with openWithRetry; construct reader; drain
            opened = true
        } else if opened && (ev.Op.Has(fsnotify.Write) || ev.Op.Has(fsnotify.Create)) {
            drain
        }
        if end-of-turn fired → invoke OnEndOfTurn, return nil
    case err, ok := <-fsw.Errors:
        if !ok { return nil }
        log Warn; continue
    }
}
```

Helper sketches (signatures + behavior, not bodies):

- `openWithRetry(ctx context.Context, path string) (*os.File, error)` — `os.Open` with bounded retry on `fs.ErrNotExist` using `probeRetryDelays = []time.Duration{0, 50*ms, 200*ms}`. Mirrors `rotation.probeWithRetry` for the "CREATE fires before claude has the file content ready" race. Returns `ctx.Err()` if cancelled mid-retry.
- `drain() (endOfTurn bool, err error)` — calls `reader.Next()` in a loop. On each `(Event, nil)` invokes `cfg.OnEvent(ev)`; if `ev.EndOfTurn`, returns `(true, nil)`. On `io.EOF`, returns `(false, nil)` — the file hasn't grown yet, wait for the next fsnotify event. Wraps any other error (notably `jsonl.ErrLineTooLarge`).

### Concurrency model

- One goroutine: `Run`. No background goroutines, no inner `errgroup`.
- `fsnotify.Watcher` is created in `New` and closed in `Run` via `defer`. **Caller must call `Run`** for cleanup to happen — `New` returning success and the caller never calling `Run` leaks the fsnotify watcher. This matches `rotation.Watcher`'s contract; same risk, same mitigation (`Run` is mandatory; the test helper enforces it via `t.Cleanup`).
- `OnEvent` and `OnEndOfTurn` are invoked from the `Run` goroutine. Callers that need to push events to another goroutine should send on a channel from the callback; the watcher itself stays single-threaded.
- `Offset()` is safe to call from the same goroutine that called `Run` (i.e., after `Run` returned). It is NOT safe to call concurrently with `Run`; document this on the method.

### Error handling

- `fsw.Errors`: log at Warn, continue. Same as rotation watcher — fsnotify errors are typically transient on Linux/macOS and ENOSPC-like conditions are not our problem to fix.
- `os.Open` retry exhausted (all three attempts returned `fs.ErrNotExist`): log at Warn and continue the event loop. A later WRITE may surface the file. (Don't bail — the CREATE may have been spurious.)
- `os.Open` returns a non-ENOENT error (e.g. permission denied): bail with wrapped error.
- `reader.Next()` returns `jsonl.ErrLineTooLarge`: bail with `errors.Is`-able wrap.
- `reader.Next()` returns any other non-EOF error: bail with wrapped error.

### `Offset()`

Returns `reader.Offset()` if the reader exists, else `cfg.StartOffset`. Used by the caller to persist a resume point after `Run` returns. Document: "Returns the byte position of the next unread line. Call after `Run` returns; not safe concurrently with `Run`."

## Testing strategy

Mirror `internal/sessions/rotation/watcher_test.go` style: a `startWatcher(t, cfg)` helper that runs `Run` in a goroutine and registers a `t.Cleanup` that cancels and waits with a 2-second deadline. A thread-safe recorder type collects `OnEvent` invocations.

Each scenario gets its own `t.Parallel()` test. Polling deadlines use `time.Now().Before(deadline)` with `time.Sleep(20*time.Millisecond)`, matching the rotation pattern.

### Scenario: realpath encoding (macOS only)

- Create `t.TempDir()` somewhere — on macOS this lands under `/var/folders/...`, which `EncodeProjectDir` resolves through `/private/var`.
- Build `cfg.HomeDir = <fake home>` and `cfg.Workdir = <tempdir>`.
- Construct the watcher.
- Assert the directory `cfg.HomeDir + "/.claude/projects/-private-var-folders-..."` was created (one of `MkdirAll`'s side effects).
- Skip with `runtime.GOOS != "darwin"` (Linux doesn't have the `/var → /private/var` symlink layer).

### Scenario: late-create

- Fake home in `t.TempDir()`.
- Workdir in another `t.TempDir()`.
- Start the watcher BEFORE the JSONL exists.
- After ~50ms, write `<sid>.jsonl` line-by-line (a 3-line fixture: one assistant entry with no end_turn, one user entry, one assistant entry with `stop_reason: end_turn` and non-empty text). Use `os.OpenFile` with `O_APPEND` between writes, calling `Sync` between each line.
- Assert: `OnEvent` invoked twice (for the two assistant entries); `OnEndOfTurn` invoked once; `Run` returned `nil`.

### Scenario: existing-file

- Pre-create `<sid>.jsonl` in the fake projects dir with a complete fixture (two assistant entries, second is end_turn with text).
- Start the watcher.
- Assert: the initial-stat path picks it up; `OnEvent` invoked twice; `OnEndOfTurn` invoked once; `Run` returned `nil` within ~500ms (no fsnotify event was needed).

### Scenario: integration via fixture

- Read `internal/agentrun/jsonl/testdata/clean.jsonl` from the test, line by line.
- Start the watcher pointed at a tempdir-hosted fake home.
- Replay each line into the watched JSONL with a small delay between writes (≤ 5ms — just enough to interleave fsnotify events with reads).
- Assert: count of `OnEvent` invocations equals 25 (the `clean.jsonl` assistant count, per `reader_test.go:43-45`); `OnEndOfTurn` fired exactly once on the last assistant entry.

### Scenario: context cancellation

- Start the watcher with the JSONL absent.
- Cancel the context.
- Assert: `Run` returns `context.Canceled` within 500ms; no goroutine leaks.

(Not strictly enumerated in the AC but follows the rotation watcher's coverage and proves AC #4 — "All goroutines tear down cleanly on `context.Context` cancellation; no leaked fsnotify watchers on shutdown".)

## Open questions

- **Logger interface for the watcher.** Does the per-event log call risk leaking JSONL content? No — like `jsonl.Reader`, the watcher logs only path + error + offset, never line bytes. Pinned in package doc-comment.
- **Resume across restarts.** `StartOffset` is wired through to `jsonl.Config.StartOffset` and used to `f.Seek` after open. The watcher itself doesn't persist anything; the caller is responsible for snapshotting `Offset()` if they want durable resume. This stays out of scope for #349.
