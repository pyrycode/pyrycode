# `internal/sessions` Package

The session-addressable runtime layer that wraps `internal/supervisor` with identity (`SessionID`) and registry (`Pool`) semantics. One `Pool` holds the set of supervised claude instances managed by a single `pyry` process.

Today the pool holds exactly one entry — the **bootstrap session** — so external behaviour is unchanged from the pre-Phase-1 supervisor-only world. The package shape is the seam Phase 1.1+ extends additively (multi-session CLI, `pyry attach <id>`, idle eviction) without touching `internal/supervisor`.

## Status

- **Phase 1.0a (#28):** package introduced, fully tested, no production consumers.
- **Phase 1.0b (#29):** consumers wired. `cmd/pyry/main.go` constructs the `Pool`; `internal/control` resolves session state via a `SessionResolver` interface defined in the control package. Wire protocol unchanged; `pyry status`/`stop`/`logs`/`attach` are byte-identical to Phase 0.
- **Phase 1.2a (#34):** `Config.RegistryPath`, on-disk `sessions.json`, cold/warm-start in `Pool.New`. See [sessions-registry.md](sessions-registry.md).
- **Phase 1.2b-A (#38):** `Config.ClaudeSessionsDir`, startup reconciliation pass in `Pool.New`, `Pool.RotateID` seam. See [jsonl-reconciliation.md](jsonl-reconciliation.md).
- **Phase 1.2b-B (#39):** errgroup wrap in `Pool.Run` (bootstrap + rotation watcher); `RegisterAllocatedUUID` skip set on `Pool`. See [rotation-watcher.md](rotation-watcher.md).
- **Phase 1.2c-A (#40):** per-session `active ↔ evicted` lifecycle goroutine, `Config.IdleTimeout` / `SessionConfig.IdleTimeout`, `Session.Activate` / `Pool.Activate`, registry `lifecycle_state`. `Session.Run` rewritten as the lifecycle loop; `Session.Attach` gains attach bookkeeping. See [idle-eviction.md](idle-eviction.md).
- **Phase 1.2c-B (#41):** `Config.ActiveCap` + LRU eviction at `Pool.Activate`; `Session.Evict` public primitive; `Pool.capMu` outermost lock. See [idle-eviction.md](idle-eviction.md) and [ADR 006](../decisions/006-concurrent-active-cap-lru.md).
- **Phase 1.1a-A1 (#72):** `Pool.runGroup` / `Pool.runCtx` supervisor handle, unexported `Pool.supervise(sess)` helper, `ErrPoolNotRunning` sentinel. Bootstrap fan-out refactored onto the helper; the watcher fan-out is unchanged (not a `*Session`). The seam Phase 1.1a-A2's `Pool.Create` consumes.
- **Phase 1.1+:** `Pool.Create(ctx, label)` (sibling #71-line follow-up), `Request.SessionID` on the wire, `claude --session-id <uuid>` invocation, per-session log lines.

## Package Layout

```
internal/sessions/
  id.go         SessionID, NewID()
  session.go    Session: wraps one supervisor + optional bridge
  pool.go       Pool: registry, lifecycle, Config, SessionConfig, RotateID
  registry.go   On-disk sessions.json (loadRegistry, saveRegistryLocked)
  reconcile.go  Startup JSONL scan (encodeWorkdir, mostRecentJSONL, reconcileBootstrapOnNew)
```

## Key Types

### `SessionID`

```go
type SessionID string

func NewID() (SessionID, error)
```

A 36-char canonical UUIDv4 (`8-4-4-4-12` hex with dashes), drawn from `crypto/rand`. No external UUID library — stdlib only, ~15 lines. The version (`b[6] = b[6]&0x0f | 0x40`) and variant (`b[8] = b[8]&0x3f | 0x80`) bits are set explicitly.

The empty `SessionID` (`""`) is the **unset sentinel**, never a valid generated ID. `Pool.Lookup("")` resolves to the default entry — the mechanism that lets future handlers call `Lookup(req.SessionID)` against an empty wire field without a special case.

### `Session`

```go
type Session struct { /* id, sup, bridge, log, lifecycle fields */ }

func (s *Session) ID() SessionID
func (s *Session) State() supervisor.State
func (s *Session) LifecycleState() lifecycleState
func (s *Session) Attach(in io.Reader, out io.Writer) (done <-chan struct{}, err error)
func (s *Session) Activate(ctx context.Context) error
func (s *Session) Run(ctx context.Context) error
```

One supervised claude instance plus the bridge that mediates its I/O in service mode. As of 1.2c-A each Session owns a lifecycle goroutine driving an `active ↔ evicted` state machine (see [idle-eviction.md](idle-eviction.md)).

- `State()` returns the supervisor's snapshot — same safe-from-any-goroutine contract. In `evicted`, the supervisor reports `PhaseStopped` (faithful — it really isn't running).
- `LifecycleState()` returns the current lifecycle state under `lcMu`. Used by tests and (eventually) richer status payloads.
- `Attach` returns `ErrAttachUnavailable` when `bridge == nil` (foreground mode); otherwise delegates to `(*supervisor.Bridge).Attach`. `supervisor.ErrBridgeBusy` is propagated **verbatim** so callers' `errors.Is` checks keep working. Bumps `attached` under `lcMu`; the wrapper goroutine spawned here decrements on bridge `done`. **Contract:** callers must `Activate` first — `bridge.Attach` on an evicted session would block on the pipe forever.
- `Activate(ctx)` moves an evicted session to `active`, blocking until the supervisor has started (or `ctx` cancels). No-op when already active. Idempotent under concurrent calls.
- `Run(ctx)` blocks until ctx cancellation, driving the lifecycle loop (`runActive` ↔ `runEvicted`). The supervisor is started on an inner ctx during active periods and drained when the ctx cancels.

The `log` field is written by the constructor but not read in 1.0 (no per-session log lines yet — see [parent ADR](../decisions/003-session-addressable-runtime.md)). Kept on the struct so 1.1 can attach without reshaping.

### `Pool`

```go
type Pool struct { /* mu RWMutex, sessions map, bootstrap, log */ }

func New(cfg Config) (*Pool, error)
func (p *Pool) Lookup(id SessionID) (*Session, error)
func (p *Pool) Default() *Session
func (p *Pool) Run(ctx context.Context) error
func (p *Pool) RotateID(oldID, newID SessionID) error
func (p *Pool) Activate(ctx context.Context, id SessionID) error
```

`New` generates a `SessionID`, constructs the underlying `*supervisor.Supervisor` from `cfg.Bootstrap`, and installs the result as the single bootstrap entry. Both `NewID` failure and `supervisor.New` failure are wrapped (`sessions: generate bootstrap id: %w`, `sessions: bootstrap supervisor: %w`) and treated as fatal-at-startup.

`Lookup`:

- empty id → bootstrap entry, no error
- known id → that entry
- non-empty unknown id → `ErrSessionNotFound` (sentinel, matchable via `errors.Is`)

`Default()` is a separate accessor with the same body minus the empty-string branch — startup paths that need the bootstrap don't carry an `error` return they know is impossible.

`Run` wraps the bootstrap session and (when `ClaudeSessionsDir` is set) the rotation watcher under `errgroup.WithContext`. The 1.2b-B errgroup wrap is the extension point Phase 1.1's N-session fan-out reuses by adding one `g.Go(sess.Run)` per pool entry. As of 1.1a-A1 (#72), `Run` exposes the live group on `*Pool` (see *Supervisor handle* below) so post-`Run` code paths can join the same supervised set; the bootstrap itself is now scheduled through that seam.

`Activate(ctx, id)` (1.2c-A) is a thin wrapper that resolves `id` and calls `Session.Activate`. Symmetry with the rest of the surface; future routers get a single entry point. Returns `ErrSessionNotFound` for unknown ids.

`RotateID` (1.2b-A) atomically swaps the in-memory entry keyed by `oldID` with one keyed by `newID`, updates the bootstrap pointer if `oldID` was the bootstrap, bumps `last_active_at`, and persists. `p.mu` (write) is held across the entire operation. `RotateID(x, x)` is a no-op; unknown `oldID` returns `ErrSessionNotFound`. This is the load-bearing seam shared between startup reconciliation and the upcoming live-detection (`/clear` while claude is running) work — see [jsonl-reconciliation.md](jsonl-reconciliation.md).

### `Config` / `SessionConfig`

```go
type Config struct {
    Bootstrap         SessionConfig
    Logger            *slog.Logger
    RegistryPath      string        // sessions.json path; "" disables persistence (test-only)
    ClaudeSessionsDir string        // claude's <uuid>.jsonl dir; "" disables startup reconcile
    IdleTimeout       time.Duration // default per-session eviction window; 0 disables
}

type SessionConfig struct {
    ClaudeBin  string
    WorkDir    string
    ResumeLast bool
    ClaudeArgs []string
    Bridge     *supervisor.Bridge // nil = foreground

    BackoffInitial time.Duration
    BackoffMax     time.Duration
    BackoffReset   time.Duration

    IdleTimeout    time.Duration  // 0 inherits Config.IdleTimeout
}
```

`SessionConfig` mirrors the relevant fields of `supervisor.Config`; `New` translates one to the other. Defaults (claude bin lookup, backoff timings) are applied by `supervisor.New` — `sessions.New` does **not** duplicate them.

`ResumeLast` maps to `--continue` on restart, as today. The locked-design `claude --session-id <uuid>` invocation is deliberately **not** plumbed in 1.0 — Phase 1.1+ adds it.

### Supervisor handle (1.1a-A1)

Two unexported fields on `*Pool` hold the live errgroup while `Run` is in progress:

```go
runGroup *errgroup.Group   // set in Pool.Run, cleared on return
runCtx   context.Context   // the gctx returned by errgroup.WithContext
```

Both are guarded by `Pool.mu` and read together so a caller never sees a half-initialised handle. `Pool.Run` writes them under `Pool.mu` (write) right after `errgroup.WithContext`, and clears them in a `defer` so a panicking goroutine still resets the handle.

```go
func (p *Pool) supervise(sess *Session) error
```

`supervise` schedules `sess.Run(gctx)` on the live group. RLock-snapshots `runGroup` + `runCtx`, releases the lock, then calls `g.Go` off-lock. Returns `ErrPoolNotRunning` (`var ErrPoolNotRunning = errors.New("sessions: pool not running")`) when the handle is `nil` — i.e. before `Run` has wired it or after `Run` has cleared it. Matchable with `errors.Is`.

The helper is unexported. Phase 1.1a-A2's `Pool.Create(ctx, label)` is the consumer: build a `*Session`, then `p.supervise(sess)` to fan it onto the same supervised set as the bootstrap. The bootstrap fan-out inside `Pool.Run` is itself rewritten to call `supervise` (the helper is exercised in production from day one rather than living dormant).

The watcher fan-out (`g.Go(func() error { return w.Run(gctx) })`) does **not** go through `supervise` — the watcher is not a `*Session`.

**Lock discipline.** `supervise` takes only `Pool.mu` (RLock) briefly; it does not call into `Session.lcMu`. The documented orders (`Pool.mu → Session.lcMu`, `Pool.capMu → Pool.mu → Session.lcMu`) are unchanged. Concurrent `supervise` callers contend only with `Run`'s one-shot setup and one-shot teardown, never with each other.

**Race windows.** A `supervise` call racing teardown either acquires RLock first (sees the handle, schedules onto a group whose ctx is about to be cancelled — `Session.Run` handles `ctx.Done` cleanly) or after (sees `nil`, returns the sentinel). The "scheduled onto a soon-cancelled group" case is safe: `errgroup.Group.Go` is documented as concurrency-safe and the scheduled func observes the cancelled ctx immediately, exiting via the existing shutdown path.

## Concurrency

`sync.RWMutex` on `Pool.sessions`:

- `Lookup` and `Default` take the read lock.
- `Run` takes the read lock once briefly to grab the bootstrap pointer and `claudeSessionsDir`.
- Writers: `RotateID` (1.2b-A), `RegisterAllocatedUUID` / `IsAllocated` mutations (1.2b-B), `persist` (1.2c-A; called from `Session.transitionTo`). Phase 1.1's `Pool.Add(SessionConfig)` plugs in the same way.

`sync.Mutex` on each `Session.lcMu` (1.2c-A): protects `lcState`, `attached`, `activeCh`, `lastActiveAt`. **Lock order: `Pool.mu → Session.lcMu`**. `Session.transitionTo` releases `lcMu` *before* calling `Pool.persist` so `saveLocked`'s per-session re-acquire can't deadlock. `RotateID` mutates `session.id` without `lcMu` — documented invariant: today's only callers run before any lifecycle goroutine begins observing the id.

Goroutines introduced in this layer (1.2c-A):

1. **Per-Session lifecycle goroutine** — body of `Session.Run`, owns the `active ↔ evicted` state machine and idle timer.
2. **Per-active-period supervisor goroutine** — wraps `s.sup.Run(subCtx)` and pipes the result to `runErr`.
3. **Per-attach detach-watcher** — decrements `attached` when the bridge's done channel fires.

The PTY spawn / wait / backoff loop, the I/O bridge goroutines, and the SIGWINCH watcher all remain in their existing packages.

## Errors

| Condition | Surface |
|---|---|
| `NewID` rng failure | `sessions.New` returns wrapped error. Fatal. |
| `supervisor.New` failure | Wrapped: `sessions: bootstrap supervisor: %w`. Fatal. |
| `Pool.Lookup` unknown id | `ErrSessionNotFound` (sentinel). |
| `Pool.supervise` before/after `Run` | `ErrPoolNotRunning` (sentinel). |
| `Session.Attach` with nil bridge | `ErrAttachUnavailable` (sentinel). |
| `Session.Attach` while bridge busy | `supervisor.ErrBridgeBusy` propagated **verbatim** — no wrap. |
| `Session.Run` / `Pool.Run` ctx cancel | `context.Canceled` from the supervisor. |

Sentinels (`ErrSessionNotFound`, `ErrAttachUnavailable`, `ErrPoolNotRunning`) live in `internal/sessions`. `supervisor.ErrBridgeBusy` stays in `internal/supervisor`.

## Dependency Direction

```
internal/sessions  →  internal/supervisor
```

`internal/sessions` imports `internal/supervisor`. The reverse is forbidden — verifiable with `go list -deps ./internal/supervisor/...`. `internal/sessions` does **not** import `internal/control`; control will (after Phase 1.0b) import sessions for `SessionID` and the resolver interface, never the other way around.

## Testing

Three test files mirror the production layout. Stdlib `testing` only.

- **`id_test.go`** — format regex match, 1000-iteration uniqueness smoke test for `crypto/rand` wiring.
- **`pool_test.go`** — bootstrap installation, `Lookup("")` ↔ `Default()` identity, lookup by ID, unknown-ID sentinel match. Uses `/bin/sleep` as the "claude" binary; tests never call `Run`, so it's never spawned.
- **`session_test.go`** — `State` delegation, `Attach` with no bridge, `Attach` busy via `io.Pipe` (first attach blocks on input, second races and gets `supervisor.ErrBridgeBusy`), `Run` ctx-cancel via a real `/bin/sleep 3600` child.

### Why no `TestHelperProcess` re-exec helper

The parent spec considered duplicating `internal/supervisor`'s `TestHelperProcess` re-exec pattern into the sessions package (~20 lines) per the project's "duplicate, don't export test surface" convention. The blocker: `supervisor.Config.helperEnv` is unexported and is the only way to pass test-only env to the spawned child without polluting the parent test process's `os.Environ()`. External packages cannot set it.

The chosen workaround is to use a real benign binary (`/bin/sleep`) as the fake claude. No re-exec, no env injection, no helper duplication. The supervisor spawns it, ctx cancellation kills it, `supervisor.Run` returns `ctx.Err()` — which is the only contract the test asserts.

`/bin/sleep` exists on both Linux and macOS; CI runs both. If a future CI environment lacks it, `t.Skipf` on `exec.LookPath` failure rather than silently passing.

## Production Consumers (Phase 1.0b)

After #29, `cmd/pyry/main.go` constructs `*sessions.Pool` and `internal/control` consumes a `SessionResolver` (defined inside `internal/control` — see [control-plane.md](control-plane.md)). External behaviour is unchanged:

- `Request`/`Response` JSON shapes unchanged. No `session_id` field yet.
- No new log lines; the bootstrap session ID is **not** logged.
- Startup log line preserved verbatim (`pyrycode starting` with the same fields).
- `pyry status`/`stop`/`logs`/`attach` are byte-identical to Phase 0.
- Foreground vs service mode keys off `term.IsTerminal(os.Stdin.Fd())` in `cmd/pyry/main.go`, unchanged.
- Restart still uses `--continue`. `claude --session-id <uuid>` is **not** plumbed in 1.0.

`*sessions.Pool` does not satisfy `control.SessionResolver` directly: `Pool.Lookup` returns the concrete `*sessions.Session`, while the resolver interface returns `control.Session`. Go does not do covariant return types on interface satisfaction, so `cmd/pyry` defines a 5-line `poolResolver` adapter to bridge the two. See [lessons.md](../../lessons.md#interface-adapters-for-covariant-returns) and [control-plane.md](control-plane.md).

## References

- Spec: [`docs/specs/architecture/28-sessions-package.md`](../../specs/architecture/28-sessions-package.md)
- Parent design: [`docs/specs/architecture/27-session-addressable-runtime.md`](../../specs/architecture/27-session-addressable-runtime.md)
- ADR: [`003-session-addressable-runtime.md`](../decisions/003-session-addressable-runtime.md)
- Locked phase design: [`docs/multi-session.md`](../../multi-session.md), [`docs/plan.md`](../../plan.md)
