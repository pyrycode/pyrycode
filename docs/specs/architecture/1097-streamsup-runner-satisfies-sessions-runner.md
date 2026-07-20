# Spec #1097 — streamsup: `*streamsup.Runner` satisfies `sessions.Runner`

**Ticket:** [#1097](https://github.com/pyrycode/pyrycode/issues/1097) · **Size:** S · **Security-sensitive:** no

Make a streamsup-backed runner drivable through the existing `sessions.Runner`
seam so the session pool can run a stream-json conversation through the exact
lifecycle it uses for the PTY supervisor, and so the config toggle (#1081) can
compile a `stream-json` `RunnerFactory` arm at all.

This is a purely **additive** slice: the `sessions.Runner` interface and every
call site that dispatches through it already exist (they were written for
`*supervisor.Supervisor` in #1077). Nothing on the `sessions` side changes. We
add the four missing methods to `*streamsup.Runner`, a native lifecycle-state
type, a new live-restart seam in the Run loop, and a thin covariant adapter in
`cmd/pyry` that hosts the compile assertion.

---

## Files to read first

Turn-1 reading list. Read these before writing code; they are the exact seams
this slice extends and mirrors.

- `internal/sessions/runner.go:25-46` — the **`Runner` interface** (the five
  methods to satisfy) + the existing `var _ Runner = (*supervisor.Supervisor)(nil)`
  assertion (the pattern AC1 mirrors) + `RunnerFactory` (the seam #1081 wires).
- `internal/sessions/session.go:219-248` — `Session.State`/`WriteUserTurn`/
  `Supervisor` — the dispatch sites that call `s.sup.*` through the interface.
  Note `Supervisor()` stays concrete (returns nil for a non-supervisor runner);
  stream wiring reaches the runner through `Runner`, not `Supervisor()`.
- `internal/sessions/session.go:335-355` — `Session.Activate` ends with
  `return s.sup.WaitForPTY(ctx)`. This is why `WaitForPTY` is on the seam and
  what AC5's "returns cleanly" must satisfy.
- `internal/sessions/pool.go:684-722` — `Pool.UpdateSettings` →
  `sup.Restart(newArgs)`. Confirms `Restart` is dispatched **through the
  interface** and shows `newArgs = sess.spawnArgs(merged)` (the base argv the
  streamsup Run loop must wrap). This is the `set_session_settings` path that
  works for free once `Restart` exists.
- `internal/streamsup/runner.go:60-341` — the file being extended: `Config`,
  `Runner` struct + `mu`/`stdin`, `New`, `Stdin`, the **`Run` loop**,
  `spawnAndWait` (incl. the `cmd.Cancel` reap+SIGTERM and the `started`/`firstRun`
  gate), and `buildArgs` (fixed prefix + base + `--session-id`/`--resume`).
- `internal/streamsup/envelope.go:70-116` — **`WriteTurn`** contract:
  `ErrNoLiveChild` on `w == nil` (before the gate), the `turncommit.From(ctx)`
  gate claim → `turncommit.ErrDropped`, wrapped write errors. `WriteUserTurn`
  wraps this verbatim — no new envelope construction.
- `internal/supervisor/supervisor.go:71-89` — `Phase` consts + `State` struct.
  streamsup's native `State`/`Phase` mirror these string-for-string so the
  adapter maps 1:1.
- `internal/supervisor/supervisor.go:656-812` — **`Restart` / `liveArgs` /
  `setIterCancel` / `Run`-loop `iterCtx` wiring / `drainRestart`** — the
  live-restart pattern to port. AC3 says mirror this.
- `internal/supervisor/supervisor.go:256-260, 273-277, 716-800` — `State()` /
  `updateState` / the Run-loop `updateState` calls (Starting/Running/Backoff/
  Stopped). The streamsup state instrumentation mirrors these exactly.
- `internal/control/server.go:1010` — `buildStatus(supervisor.State)` reads
  `Phase, ChildPID, StartedAt, RestartCount, LastUptime, NextBackoff`. This is
  why the native state must be faithful across all six fields, not just Phase.
- `cmd/pyry/main.go:1008-1063` — `poolResolver` / `sessionMinter` /
  `settingsUpdaterAdapter`: the **covariant-adapter precedent**. The streamsup
  adapter is the same wrap-and-forward shape.
- `docs/lessons.md` § "Interface adapters for covariant returns" — the rationale
  (Go has no covariant return on interface satisfaction).
- `internal/streamsup/helper_test.go` — the fake-child harness (`TestMain`
  dispatch on `GO_STREAMSUP_HELPER`, modes `echo_lines`/`block_sigterm`/`crash`/
  `stream_json`, the `GO_STREAMSUP_HELPER_ARGV_FILE` argv-recording seam). The
  live-restart test grows this (see Testing).
- `internal/streamsup/runner_test.go:239-395` — `helperRunCfg`, the `onSpawn`
  counter, and `TestRunner_RestartsOnCrash` / `TestRunner_ResumeIDStableAcrossRestart`
  (the ARGV_FILE + `--resume` assertions the live-restart test reuses).

---

## Context

`internal/streamsup` (built across #1076 → #1087 → #1088 → #1089 → #1093 →
#1094) supervises a long-lived headless claude child but exposes only
`Run(ctx)` and `Stdin()`. The `sessions.Runner` seam additionally requires
`State()`, `WriteUserTurn()`, `WaitForPTY()`, and `Restart()`.

Two parent-ticket scope items fall out for free once these exist, because the
per-conversation data paths already dispatch through `sessions.Runner`:

- **send_message** → `Session.WriteUserTurn` → `s.sup.WriteUserTurn`.
- **set_session_settings** → `Pool.UpdateSettings` → `sup.Restart(newArgs)`.

No new wiring is needed for either — they work the moment the interface is
satisfied.

---

## Design

### Component map

| Symbol | Location | New/changed |
|---|---|---|
| `streamsup.Phase` + consts | `internal/streamsup/state.go` (new) | new type |
| `streamsup.State` | `internal/streamsup/state.go` (new) | new type |
| `Runner.State() State` | `internal/streamsup/state.go` (new) | new method |
| `Runner.updateState(fn)` | `internal/streamsup/state.go` (new) | new helper |
| `Runner.WriteUserTurn(ctx, convID, payload) error` | `internal/streamsup/runner.go` | new method |
| `Runner.WaitForPTY(ctx) error` | `internal/streamsup/runner.go` | new method |
| `Runner.Restart(args []string)` | `internal/streamsup/runner.go` | new method |
| `Runner.liveArgs()` / `setIterCancel` / `drainRestart` | `internal/streamsup/runner.go` | new helpers |
| Run-loop restart + state instrumentation | `internal/streamsup/runner.go` (`Run`, `spawnAndWait`, `New`) | changed |
| `streamRunner` adapter + `var _ sessions.Runner = …` | `cmd/pyry/streamsup_runner.go` (new) | new type + assertion |

Three production source files (`internal/streamsup/runner.go` modified,
`internal/streamsup/state.go` new, `cmd/pyry/streamsup_runner.go` new).

### Why an adapter (the covariant snag)

`sessions.Runner.State()` returns `supervisor.State`, but streamsup's package
doc forbids importing `internal/supervisor` (`runner.go:22-30`). Go has no
covariant return on interface satisfaction, so `*streamsup.Runner` cannot
declare `State() supervisor.State`. Resolve at the composition root, **not** by
relaxing the interface or adding the import:

- `*streamsup.Runner` gets **streamsup-native** signatures: `State() streamsup.State`,
  plus `WriteUserTurn`/`WaitForPTY`/`Restart`/`Run` whose signatures reference no
  supervisor type and therefore already match the interface exactly.
- A thin `streamRunner` adapter in `cmd/pyry` (which imports both packages) wraps
  `*streamsup.Runner`, maps `State() streamsup.State → supervisor.State`, and
  forwards the other four unchanged. The adapter — not the concrete runner — is
  what satisfies `sessions.Runner`.

This mirrors `poolResolver` (`cmd/pyry/main.go:1008`) exactly.

### `streamsup.State` / `Phase` (new file `state.go`)

Mirror `supervisor.State`/`Phase` field-for-field and string-for-string so the
adapter maps trivially. Contract sketch (types + doc, not bodies):

```go
type Phase string
const (
    PhaseStarting Phase = "starting"
    PhaseRunning  Phase = "running"
    PhaseBackoff  Phase = "backoff"
    PhaseStopped  Phase = "stopped"
)

// State is the stream-json analogue of supervisor.State. The cmd/pyry adapter
// maps it to supervisor.State (streamsup must not import internal/supervisor).
type State struct {
    Phase        Phase
    ChildPID     int
    StartedAt    time.Time
    RestartCount int
    LastUptime   time.Duration
    NextBackoff  time.Duration
}
```

Add to `Runner`: a leaf mutex `stateMu sync.Mutex` and `state State`;
`updateState(fn func(*State))` and `State() State` mirror
`supervisor.go:256-260, 273-277` verbatim. Keep `stateMu` **separate** from the
existing `mu` (which guards `stdin`) — different concern, different access
pattern (control-plane reader vs. Run-goroutine writer).

### Run-loop state instrumentation (mirror supervisor)

Instrument the existing `Run` loop / `spawnAndWait` to keep `state` faithful,
mirroring `supervisor.Run` (`supervisor.go:716-800`):

- Top of `Run`: set `Phase=PhaseStarting`, `StartedAt=time.Now()` (once).
- `defer` at Run top: set `Phase=PhaseStopped`, `ChildPID=0`, `NextBackoff=0`.
- On spawn (in `spawnAndWait`, right after `setStdin`, beside the existing
  `cfg.onSpawn` seam): set `Phase=PhaseRunning`, `ChildPID=cmd.Process.Pid`,
  `NextBackoff=0`.
- Before the backoff wait (crash path only, **not** a deliberate restart): set
  `Phase=PhaseBackoff`, `ChildPID=0`, `RestartCount++`, `LastUptime=uptime`,
  `NextBackoff=delay`.

`buildStatus` consumes all six fields, so all six must be populated.

### Live-restart seam (mirror `supervisor.Restart`)

streamsup's Run loop currently has **no** live-restart signal (see the loop
comment at `runner.go:205-207`). Port the supervisor pattern:

New `Runner` fields (guarded by a new leaf `restartMu sync.Mutex`):

- `args []string` — the live spawn base argv (initialised from `cfg.Args` in
  `New`; swapped by `Restart`). Run reads it via `liveArgs()` instead of
  `r.cfg.Args`.
- `iterCancel context.CancelFunc` — the current spawn's cancel; published each
  iteration, cleared between spawns.
- `restartCh chan struct{}` — buffered(1) deliberate-restart hint (allocated in
  `New`).

Method contracts (signatures + behaviour; port bodies from `supervisor.go`):

```go
// Restart swaps the live base argv and, if a child is running, forces it to
// exit so the loop relaunches with the new args (resuming via --resume).
// Non-blocking, fire-and-forget; drives only restartMu + restartCh + iterCancel
// (no sessions/Pool lock). Mirrors supervisor.Restart (supervisor.go:672-687).
func (r *Runner) Restart(args []string)

func (r *Runner) liveArgs() []string           // clone of r.args under restartMu
func (r *Runner) setIterCancel(c context.CancelFunc)
func (r *Runner) drainRestart() bool            // non-blocking consume of restartCh
```

Run-loop changes (mirror `supervisor.go:731-799`):

1. Read argv via `buildArgs(r.liveArgs(), firstRun, r.cfg.SessionID)`.
2. Derive `iterCtx, cancel := context.WithCancel(ctx)`, `setIterCancel(cancel)`,
   pass **`iterCtx`** (not the parent `ctx`) to `spawnAndWait`, then `cancel()`
   and `setIterCancel(nil)` after it returns. `cmd.Cancel`'s reap+SIGTERM now
   fires on either a parent-ctx cancel (shutdown) **or** an iterCtx cancel
   (restart) — both want the child cleanly torn down.
3. Keep the existing `if ctx.Err() != nil { return ctx.Err() }` after
   `spawnAndWait` — it uses the **parent** `ctx`, so a Restart (iterCtx-only
   cancel) leaves `ctx.Err()` nil and falls through to relaunch, while a real
   shutdown returns. This is the load-bearing shutdown-vs-restart discriminator,
   already present.
4. After the `started`/`firstRun` update, add `if r.drainRestart() { continue }`
   to skip backoff on a deliberate restart (a restart is not a crash).
5. Add `case <-r.restartCh:` to the backoff `select` so a restart arriving while
   the child is already down (mid-backoff) breaks the wait and relaunches.

Because `firstRun` is already `false` after the first successful spawn, a
restart respawns with `--resume <sessionID>` — the conversation resumes, matching
AC3. No change to `buildArgs`.

### `WriteUserTurn` / `WaitForPTY` (methods on `*streamsup.Runner`, in `runner.go`)

```go
// WriteUserTurn writes the user envelope to the live child's stdin and claims
// the turncommit gate, wrapping the reviewed WriteTurn free function (#1088).
// A nil Stdin() (no live child) yields ErrNoLiveChild without writing; a gate
// deny yields turncommit.ErrDropped with zero bytes written. conversationID is
// accepted for interface conformance and future outbound-cursor wiring (T4/T7);
// this slice does not yet track a cursor.
func (r *Runner) WriteUserTurn(ctx context.Context, conversationID string, payload []byte) error {
    return WriteTurn(ctx, r.Stdin(), payload)
}

// WaitForPTY returns cleanly: the stream path has no PTY to await. The
// no-live-child window is handled per-turn by WriteTurn's retryable
// ErrNoLiveChild, so there is no readiness gate to block on here (AC5).
func (r *Runner) WaitForPTY(ctx context.Context) error { return nil }
```

`WriteUserTurn` deliberately reuses `WriteTurn`'s exact contract — the no-live-child
refusal (AC2) and gate semantics are already reviewed under #1088/#1093, so no
new envelope construction or parse boundary is introduced.

### `streamRunner` adapter (new file `cmd/pyry/streamsup_runner.go`)

Wrap-and-forward, mirroring `poolResolver`:

```go
type streamRunner struct{ r *streamsup.Runner }

func (a streamRunner) State() supervisor.State                 // maps a.r.State()
func (a streamRunner) WriteUserTurn(ctx, convID, payload) error // → a.r.WriteUserTurn
func (a streamRunner) WaitForPTY(ctx) error                    // → a.r.WaitForPTY
func (a streamRunner) Run(ctx) error                           // → a.r.Run
func (a streamRunner) Restart(args []string)                   // → a.r.Restart

var _ sessions.Runner = streamRunner{} // AC1 compile assertion
```

Plus a small `mapStreamState(streamsup.State) supervisor.State` — a field-by-field
copy; `Phase` maps as `supervisor.Phase(string(s.Phase))` since the values are
identical. The compile assertion is the AC1 deliverable; the factory arm that
*constructs* a `streamRunner` from a `supervisor.Config` is #1081's scope, not
this ticket's.

---

## Concurrency model

Three independent **leaf** mutexes on `Runner`, never nested:

- `mu` (existing) — guards `stdin`, swapped at spawn/teardown by the Run
  goroutine, read by `Stdin()` from the #1088 writer goroutine.
- `stateMu` (new) — guards `state`; written by the Run goroutine
  (`updateState`), read by the control plane (`State()`).
- `restartMu` (new) — guards `args` and `iterCancel`; written by the Run
  goroutine (per-iteration) and by any goroutine calling `Restart`.

`restartCh` is a buffered(1) channel; `Restart`'s send is non-blocking (coalesces
rapid restarts to one relaunch with the newest args, exactly as
`supervisor.Restart`).

**Shutdown vs. restart.** A parent-`ctx` cancel is shutdown: `Run` returns
`ctx.Err()`. An `iterCtx`-only cancel (from `Restart`) tears down just the child;
`Run` falls through and relaunches with the swapped args. The discriminator is
the existing `if ctx.Err() != nil` check on the **parent** ctx after
`spawnAndWait` — unchanged.

**Lock-order safety for `Restart`.** Like `supervisor.Restart`, streamsup's
`Restart` touches only `restartMu` + `restartCh` + `iterCancel` — never a
sessions/Pool lock — so `Pool.UpdateSettings` calls it after releasing `p.mu`
with no lock-order concern (`pool.go:712-720`).

---

## Error handling

- `WriteUserTurn` returns `WriteTurn`'s errors verbatim: `ErrNoLiveChild`
  (no live child, before the gate — retryable), `turncommit.ErrDropped` (gate
  deny, zero bytes), or a wrapped `streamsup: write turn: %w` on a pipe write
  failure. No new error types.
- `WaitForPTY` never errors on the stream path (returns nil).
- `Restart` is fire-and-forget (no return value); the forever-retry Run loop
  guarantees the relaunch, so a transient respawn failure is absorbed by backoff
  exactly as a crash.
- `State()` is a lock-guarded snapshot; never errors.

---

## Testing strategy

Bulleted scenarios — the developer writes the code in the streamsup table-driven
idiom, reusing `helperRunCfg` + the `onSpawn` counter + `GO_STREAMSUP_HELPER_ARGV_FILE`.

**AC1 — compile assertion.** `var _ sessions.Runner = streamRunner{}` in
`cmd/pyry/streamsup_runner.go`. The build failing on a missing/mistyped method is
the proof; no separate test needed. Add a tiny unit test for `mapStreamState`
(each `Phase` value maps to its supervisor twin; the six numeric/time fields copy
through).

**AC2 — `WriteUserTurn`.** Reuse the `stream_json` helper mode / envelope test
scaffolding:
- Live child → `WriteUserTurn(ctx, "c1", payload)` returns nil and the child
  receives exactly one envelope line carrying the prompt (assert via the echoed
  marker).
- No live child (before first spawn / `Stdin()` nil) → returns `ErrNoLiveChild`,
  zero bytes written.
- Gate on ctx denies (`turncommit` gate returns false) → returns
  `turncommit.ErrDropped`, zero bytes written (assert sink `len == 0`, mirroring
  the #1093 gate test).

**AC3 — live `Restart`.** Grow the harness with a mode that records each spawn's
argv to `GO_STREAMSUP_HELPER_ARGV_FILE` **and blocks** (records like `crash`, but
stays alive until SIGTERM instead of exiting) — a live restart must prove the
first child was killed by `Restart`, not by self-exit. Then:
- Start `Run` on a cancellable ctx; wait for spawn 1 (onSpawn counter).
- Call `Restart(newArgs)` with a distinguishable arg.
- Assert spawn 2 occurs, its argv contains `newArgs` **and** `--resume <sessionID>`
  (not `--session-id`), and `Run`'s ctx is **not** cancelled (Run is still
  looping — cancel it explicitly at test end and assert it returns `ctx.Err()`).
- Assert `RestartCount` did **not** increment for the deliberate restart (it is
  not a crash — `drainRestart` skipped the backoff path).

**AC4 — `State()`.** Drive `Run` and assert the `Phase` progression
Starting → Running (with non-zero `ChildPID` and `StartedAt`) → … → Stopped on
ctx cancel. A crash-then-backoff cycle (reuse the `crash` mode) asserts
`Phase=Backoff` with `RestartCount` incremented and `NextBackoff > 0`.

**AC5 — `WaitForPTY`.** Returns nil promptly with no live child and with a live
child (there is no readiness to await).

Run `go test -race ./internal/streamsup/... ./cmd/pyry/...` and
`staticcheck ./...` before finishing (see Open questions on U1000).

---

## Open questions

- **`conversationID` is unused this slice.** `WriteUserTurn` accepts it for
  interface conformance; the outbound cursor it will eventually stamp (the
  `CurrentConversation` analogue) is downstream stream-wiring (T4/T7), reached
  through a different seam. Documenting it as intentionally-deferred here; do not
  invent cursor tracking in this ticket (no observed consumer yet).
- **staticcheck U1000 on the adapter.** `streamRunner`'s methods and
  `mapStreamState` are reached only via the `var _ sessions.Runner = streamRunner{}`
  assertion until #1081 wires the factory arm. staticcheck treats methods required
  to satisfy an interface the type is converted to as used, so this should be
  clean — but verify with `staticcheck ./...`. If U1000 fires unexpectedly, the
  correct fix is #1081's factory wiring, not silencing; flag it rather than adding
  a `//nolint`-style suppression.
- **`WaitForPTY` and ctx.** Spec'd as `return nil` (no readiness gate on the
  stream path). Honouring `ctx.Err()` would be a defensible nicety but is not
  required by AC5 and adds nothing observable; keep it a bare `return nil` unless
  a consumer surfaces a need.
