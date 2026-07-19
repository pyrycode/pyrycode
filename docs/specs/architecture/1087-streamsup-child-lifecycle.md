# Spec #1087 — `internal/streamsup`: persistent stream-json child lifecycle

First slice of the new `internal/streamsup` package: the **process lifecycle only**.
Spawn a long-lived headless claude in `--input-format stream-json --output-format
stream-json` mode, hold its stdin open across many turns, restart on crash with the
supervisor's exponential-backoff ladder, resume with `--resume <id>` (stable id, no
fork), and shut down cleanly (SIGTERM→SIGKILL grace + reap of claude's detached Bash
process groups). Turn I/O (envelope write + stdout→turnevent parsing) is #1088; the
turncommit/idle/stall gates are #1089. Both build on this foundation.

**This package contains no transcript tailing.** That is the point of the package — it
structurally kills the #528/#996/#989 bind-latency family. The only filesystem reasoning
allowed is resolving the workdir realpath for spawn hygiene (below); no fsnotify, no
JSONL-path resolution, no `<uuid>.jsonl` anything.

## Files to read first

- `internal/agentrun/streamrunner/runner.go:134-252` — **the spawn/teardown template.**
  `exec.CommandContext` + `cmd.Cancel` (reap → SIGTERM) + `cmd.WaitDelay` (SIGKILL
  fallback) + `StdinPipe`. Lift this shape wholesale. **Critical inversion:** line 223
  `stdin.Close()` — streamrunner closes stdin after one write; streamsup must **not**
  (the child is multi-turn; #1088 writes onto the held-open handle).
- `internal/agentrun/streamrunner/runner.go:1-24` — package doc: the "must not import
  `internal/supervisor` nor sibling agentrun subpackages" rule + the `go list -deps`
  verification snippet. Mirror both in `streamsup`'s package doc (satisfies AC1).
- `internal/agentrun/streamrunner/reap.go:1-16` — the `reapDescendantGroupsFn` seam var
  (points at `agentrun.ReapDescendantGroups`, swappable in tests). Copy verbatim.
- `internal/agentrun/reap.go:18-67` — `ReapDescendantGroups` contract (what the seam
  calls; do **not** re-implement — import `internal/agentrun` and call it).
- `internal/agentrun/workdir.go:33-43` — `ResolveWorkdir`: resolve `WorkDir` before spawn
  (macOS `/tmp`→`/private/tmp`, the #989 symlink hazard). Wraps `fs.ErrNotExist`.
- `internal/agentrun/exitclass.go:23` — `ExitErrIsBenign`: classify benign
  broken-pipe/already-exited errors when closing the old stdin on restart (avoids a
  spurious WARN; see streamrunner runner.go:224).
- `internal/supervisor/backoff.go:1-46` — `backoffTimer` (exponential + stability reset).
  **Copy verbatim** into `internal/streamsup/backoff.go` (cannot import `supervisor`).
- `internal/supervisor/backoff_test.go:8-152` — the backoff ladder table test. Lift it —
  it is the AC3 assertion.
- `internal/supervisor/supervisor.go:706-829` — `Run` loop + `buildClaudeArgs`: the
  restart / backoff / resume / ctx-cancel-shutdown structure to mirror. Adapt
  `--continue`/`--session-id` → the `--session-id`-then-`--resume` shape below, and the
  PTY/`tuidriver.Spawn` host → a plain `exec.Cmd` (streamrunner's shape).
- `internal/agentrun/streamrunner/runner_test.go:36-55,127-206` — `helperRunCfg` +
  `reapRecorder`/`swapReapSeam`/`assertReapedLivePid`. Lift the harness and the reap-seam
  test pattern.
- `internal/agentrun/streamrunner/helper_test.go:28-154` — `TestStreamRunnerHelperProcess`
  fake-child pattern (env-keyed modes). Adapt modes for this slice (block-on-stdin /
  echo-stdin / exit-to-force-restart / SIGTERM-handler).
- QMD `pyrycode-docs` → `second-brain/.../streamrunner-interactive-spike-findings.md`
  (T1 spike, #1075) — the live-verified protocol constraints. Key facts baked into this
  spec: no `-p`; per-turn `system` init; **plain `--resume <id>` reuses the on-disk id and
  does not fork** (§3); resolve workdir realpath (§3); idle session persists (§6).

## Context

The PTY interactive path (`internal/supervisor` → tui-driver) binds a conversation to a
transcript by tailing `<uuid>.jsonl`, which introduces the bind-latency race the
#528/#996/#989 family fought. The stream-json path removes the screen and the transcript
tail entirely: one headless claude speaks JSON events on stdout and accepts JSON turns on
stdin. To host it we need a supervisor-shaped runner — long-lived, crash-restarting,
resume-preserving — but spawning a **plain subprocess** (streamrunner's shape) rather than
a PTY session, and **holding stdin open** across turns rather than closing it after one.

This slice stands up exactly that lifecycle skeleton and nothing else. It is the
ptyrunner-skeleton analogue (#471) for the stream-json path. It ships unwired: no pool,
relay, or `cmd/pyry` consumer. #1088 adds the stdin envelope writer + stdout parser on top
of the seams here; #1089 adds the commit/idle/stall gates.

## Design

### Package layout

```
internal/streamsup/
  runner.go        Config, Runner, New, Run, spawn/held-stdin, Stdin accessor, buildArgs
  backoff.go       backoffTimer (copied verbatim from internal/supervisor/backoff.go)
  reap.go          reapDescendantGroupsFn seam (→ agentrun.ReapDescendantGroups)
  runner_test.go   fake-child harness + argv/held-stdin/restart/resume/teardown tests
  backoff_test.go  backoff ladder table test (lifted)
```

**Import boundary (AC1).** Imports `internal/agentrun` (root package — for
`ReapDescendantGroups`, `ResolveWorkdir`, `ExitErrIsBenign`) and stdlib only. Must **not**
import `internal/supervisor` or any sibling agentrun subpackage
(`ptyrunner`/`streamrunner`/…). `internal/agentrun` (root) is the parent, not a sibling —
importing it is the intended reuse (streamrunner does the same). Package doc carries the
verification command, mirroring streamrunner:

```
go list -deps ./internal/streamsup/... | grep pyrycode/internal/supervisor   # expect: empty
```

### `Config`

Constructor-injection struct, `supervisor.Config`/`streamrunner.Config` idiom. Fields:

- `ClaudeBin string` — resolved path to claude. Required.
- `WorkDir string` — child cwd. Required. **Resolved via `agentrun.ResolveWorkdir` in
  `New`** (or at first spawn); the resolved path is what `cmd.Dir` is set to and is the
  only path canonicalisation this package does.
- `SessionID string` — caller-minted claude session UUID. Required. First spawn passes
  `--session-id <SessionID>` (establishes the on-disk transcript under a known id);
  respawns pass `--resume <SessionID>` (reattach, append, no fork). The pool owns minting
  and shape-validation (`sessions.ValidID`); `streamsup` checks non-empty only.
- `Args []string` — caller-supplied pass-through argv (e.g. `--model <m>`, later
  `--permission-prompt-tool …`). `streamsup` owns only the fixed stream-json prefix;
  everything else is the caller's, mirroring `supervisor.buildClaudeArgs(claudeArgs, …)`.
- `Stdout io.Writer` — child stdout sink. **Optional; nil → child stdout goes to
  `/dev/null`** (a plain `exec.Cmd` with `cmd.Stdout == nil` discards, so an unconsumed
  stdout never blocks the child). This is the seam #1088's turnevent parser plugs into.
- `Stderr io.Writer` — child stderr sink. Optional; nil → discarded.
- `Logger *slog.Logger` — nil → `slog.Default()`.
- `BackoffInitial`, `BackoffMax`, `BackoffReset time.Duration` — zero → the supervisor
  defaults (500ms / 30s / 60s), applied in `New`.
- `Env []string` — appended to `os.Environ()` in the child. Optional; production leaves
  nil, tests thread `TestHelperProcess` wiring (streamrunner idiom).
- `onSpawn func(pid int)` — **unexported test seam**, called once per spawn after
  `cmd.Start` and after the stdin handle is stored. Nil in production. Lets a test observe
  "child N is up, `Stdin()` is live" without polling. (Same unexported-injection idiom as
  `supervisor.helperEnv`/`deliverFn`.)

### `Runner`

```go
type Runner struct { /* cfg, log, resolved workdir, mu-guarded stdin, backoffTimer state */ }

func New(cfg Config) (*Runner, error)          // validate + apply defaults + resolve workdir
func (r *Runner) Run(ctx context.Context) error // the supervise loop (blocks until ctx cancel)
func (r *Runner) Stdin() io.Writer              // the held-open stdin handle, or nil between spawns
```

- `New` validates `ClaudeBin`/`WorkDir`/`SessionID` non-empty, `exec.LookPath`s the binary
  (surface not-found like `supervisor.New`), resolves `WorkDir` via `ResolveWorkdir`
  (a non-existent workdir → wrapped `fs.ErrNotExist`, fail fast), and clones `Args`.
- `Stdin() io.Writer` returns the live child's stdin (the write end of `cmd.StdinPipe()`)
  under a leaf mutex, or **nil when no child is live** (between spawns / mid-restart /
  before first spawn). Returns `io.Writer` (not `io.WriteCloser`) so #1088 cannot close a
  handle the runner owns. This is the held-open-stdin seam; #1088's writer marshals the
  turn envelope and writes here, treating nil as a "no live child" refusal (the
  `ErrNoLiveSession` analogue).

### `buildArgs` — pure, unit-tested (AC2 + AC4)

```go
func buildArgs(base []string, firstRun bool, sessionID string) []string
```

Assembles one spawn's argv:

1. Fixed stream-json prefix: `--input-format stream-json --output-format stream-json --verbose`.
   **Never `-p`/`--print`** — the non-`-p` choice is billing-tied and spike-verified
   (multi-turn/interrupt/resume/approval all work without it).
2. Then `base` (the caller's `Args`, e.g. `--model <m>`).
3. Then the id flag: `firstRun` → `--session-id <sessionID>`; else → `--resume <sessionID>`.

Pure function, no `Runner` state, never mutates `base` (append to a fresh slice — see
`supervisor.buildClaudeArgs`). First-spawn `--session-id` + respawn `--resume` with the
**same** `sessionID` is why the on-disk id is stable across a kill-and-restart (spike §3:
plain `--resume` reuses the id, does not fork; `--fork-session` is the unused opt-in). It
also mirrors the daemon bootstrap's deterministic-`--session-id` precedent (#839).

### Spawn + held-open stdin (per iteration)

Mirror streamrunner runner.go:134-213, minus the envelope write/close and the watchdog:

- `cmd := exec.CommandContext(ctx, cfg.ClaudeBin, args)`, `cmd.Dir = resolvedWorkDir`,
  `cmd.Stdout = cfg.Stdout`, `cmd.Stderr = cfg.Stderr`, `cmd.Env = append(os.Environ(), cfg.Env…)`.
  (`ctx` is the parent `Run` ctx — a fresh `exec.Cmd` per spawn; no derived per-iteration
  ctx is needed, this slice has no live-restart seam like `supervisor.Restart`.)
- `cmd.Cancel = func() error { reapDescendantGroupsFn(cmd.Process.Pid, logger); return cmd.Process.Signal(syscall.SIGTERM) }`
  and `cmd.WaitDelay = killGrace` (`const killGrace = 5 * time.Second`, streamrunner's value).
  Reap-then-SIGTERM fires only on ctx cancel (operator teardown) — the #565/#924 shape;
  a spontaneous crash does not invoke it, matching the proven streamrunner/ptyrunner
  behaviour (no evidence claude orphans groups on its own exit — do not add speculative
  crash-path reaping).
- `stdin, err := cmd.StdinPipe()` **before** `cmd.Start()`. Store the write end under the
  runner's stdin mutex. **Do not close it after spawn.** Call `onSpawn(pid)` if set.
- `waitErr := cmd.Wait()`. On return the child has exited (crash) or been torn down
  (cancel). Close the old stdin best-effort (`ExitErrIsBenign` swallows the expected
  broken-pipe / already-closed) and set the stored handle to nil, so `Stdin()` reports no
  live child until the next spawn re-opens a fresh pipe.

### `Run` loop

Mirror `supervisor.Run` (supervisor.go:716-800), swapping the PTY host for the plain
spawn above:

- `bo := newBackoffTimer(…)`; `firstRun := true`.
- Loop: if `ctx.Err() != nil` return it. `args := buildArgs(clonedArgs, firstRun, cfg.SessionID)`.
  `start := time.Now()`; spawn + `cmd.Wait()`; `uptime := time.Since(start)`.
- **Shutdown is a parent-ctx cancel, not the child-exit error** (supervisor.go:762): after
  wait, `if ctx.Err() != nil { return ctx.Err() }` — a clean graceful-shutdown return.
- Else it was a crash: `firstRun = false`; `delay := bo.next(uptime)`; then
  `select { case <-time.After(delay): case <-ctx.Done(): return ctx.Err() }`, and loop to
  respawn. (No `restartCh`/`drainRestart` — the live-settings-restart seam is a supervisor
  concern this slice deliberately omits.)

### Concurrency model

- **One goroutine: the caller's `Run`.** No internal fan-out goroutines. `cmd.Wait` blocks
  the loop; os/exec runs its own stdout/stderr forwarders and its ctx-watcher (which
  invokes `cmd.Cancel`) internally.
- **`cmd.Cancel` runs on the os/exec ctx-watcher goroutine**, so the reap seam is called
  off-loop — the stdin mutex and the reap-recorder test double are the only shared state
  it touches; both are mutex-guarded (see `streamrunner`'s `reapRecorder`).
- **`Stdin()` is called from #1088's writer goroutine** concurrently with `Run` swapping
  the handle at spawn/teardown → the stdin field is guarded by a leaf mutex. Capture the
  pointer under the lock and release before use (the `supervisor.WriteUserTurn`
  sessMu-capture-then-release discipline), so a slow write never blocks teardown.
- **Shutdown sequence:** parent ctx cancel → os/exec fires `cmd.Cancel` (reap descendant
  Bash groups, then SIGTERM claude) → `WaitDelay` SIGKILLs after `killGrace` if claude has
  not exited → `cmd.Wait` returns → `Run` sees `ctx.Err() != nil` → returns `ctx.Err()`.

### Error handling

| Failure | Handling |
|---|---|
| `ClaudeBin`/`WorkDir`/`SessionID` empty | `New` returns a wrapped error (fail fast). |
| claude binary not on PATH | `New` returns `exec.LookPath` error (supervisor idiom). |
| `WorkDir` does not exist | `ResolveWorkdir` wraps `fs.ErrNotExist`; `New` propagates. |
| `StdinPipe`/`cmd.Start` failure | Wrapped `fmt.Errorf("streamsup: …: %w", err)`; the loop treats it as a crashed iteration → backoff + retry (a transient spawn failure must not kill the daemon). |
| Child crash (non-zero / signal) | Backoff, respawn with `--resume`. Uptime feeds the stability reset. |
| Old-stdin close on restart | Best-effort; `ExitErrIsBenign` demotes broken-pipe/closed to Debug, real errors to Warn (streamrunner runner.go:223-229). |
| Reap enumeration/kill failure | Swallowed inside `ReapDescendantGroups` (Warn, pids only); claude still gets SIGTERM. |

No custom error types are needed in this slice. #1088 introduces the "no live child"
sentinel for the write path when it consumes `Stdin()`.

## Testing strategy

Table-driven, stdlib `testing`, `go test -race`. Reuse streamrunner's fake-child idiom
(`TestHelperProcess` re-exec keyed by env). Scenarios (bulleted — developer writes them in
the project idiom):

- **`buildArgs` shape (AC2 + AC4), pure/table.** first-spawn: contains the three fixed
  stream-json flags + `--verbose`, contains `--session-id <id>`, does **not** contain
  `--resume`, does **not** contain `-p`/`--print`; respawn (`firstRun=false`): contains
  `--resume <id>`, not `--session-id`; the id is byte-identical across first-spawn and
  respawn for a fixed `SessionID` (the no-fork/stable-id assertion); `Args` pass-through
  preserved in order; `base` not mutated.
- **Held-open stdin (AC2), integration.** Fake child mode: write a "ready" line to stdout,
  then read stdin and echo each line back to stdout; on stdin EOF write "GOT_EOF". Wire
  `Config.Stdout` to a buffer and `onSpawn` to a ready signal. After spawn: assert
  `Stdin() != nil`; write a line via `Stdin()`; assert the echo appears on stdout within a
  window; assert "GOT_EOF" is **absent** (stdin was not closed after spawn). Cancel ctx to
  end.
- **Backoff ladder (AC3), pure/table.** Lift `supervisor.backoff_test.go` verbatim against
  the copied `backoffTimer`: short uptime → delay grows and caps at max; uptime >
  `resetAfter` → resets to initial; exact-threshold does not reset.
- **Restart on crash (AC3 wiring), integration.** Fake child exits quickly (short sleep →
  exit 1) without draining stdin; run with a tiny `BackoffInitial`. Count spawns via
  `onSpawn` (or a per-spawn marker file). Assert ≥2 spawns occur (the loop respawned),
  then cancel ctx and assert `Run` returns `ctx.Err()`.
- **Resume id stable across restart (AC4), integration.** Fake child records its own
  `os.Args` to a per-spawn file, then exits. Assert spawn 1's captured argv carries
  `--session-id <id>` and spawn 2's carries `--resume <id>` with the **same** id (proves
  the on-disk id is reused, no fork). (Complements the pure `buildArgs` test with the real
  wiring.)
- **Teardown SIGTERM + grace (AC5), integration.** Fake child installs a SIGTERM handler
  (print "got SIGTERM" to stderr, exit 0) and otherwise blocks reading stdin. Cancel the
  parent ctx ~100ms in; assert `Run` returns `ctx.Err()` (or nil per the chosen shutdown
  contract) within < `killGrace` (SIGTERM handled, not fallen through to SIGKILL) and that
  stderr shows "got SIGTERM". Clone streamrunner's `TestRun_CtxCancelMidRun`.
- **Teardown reaps descendant groups (AC5), integration, non-parallel.** Lift
  `reapRecorder`/`swapReapSeam`/`assertReapedLivePid`: swap `reapDescendantGroupsFn`,
  cancel ctx, assert the seam fired with a plausible pid (> 1). (The real reap tree is
  `agentrun.TestReapDescendantGroups`' job, already covered.)
- **PTY path untouched (AC5), structural.** `internal/supervisor` has zero diff — the
  developer simply does not touch it; the reviewer confirms via the PR diff.
- **Import isolation (AC1), structural.** Package doc carries the `go list -deps | grep`
  snippet; CI vet/build proves compilation in isolation. No transcript-tailing symbols
  (no fsnotify import, no `.jsonl` reference) — a reviewer grep.

## Open questions

- **`Stdout`/`Stderr` optionality.** Spec makes them optional (nil → `/dev/null`) so this
  slice needn't fabricate a parser. #1088 will make `Stdout` the parser sink. If #1088
  prefers streamsup to own the parser wiring instead of the caller supplying it, that is a
  #1088 decision — this slice only needs the sink forwarded. No change here either way.
- **`Stdin()` handle type.** Returns `io.Writer` to deny #1088 the ability to close a
  runner-owned handle. If #1088 finds it needs an explicit "flush/half-close turn" signal
  to claude, that verb is added in #1088 on top of the held handle — not here.
- **Reap on spontaneous crash.** Deliberately not done (reap fires only on ctx-cancel
  teardown, the proven streamrunner/ptyrunner shape). Revisit only if a live run is
  observed orphaning Bash groups on a claude self-exit — no evidence today
  (Evidence-Based Fix Selection).
