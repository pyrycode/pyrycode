# Architecture spec — #27 Phase 1.0 session-addressable runtime layer

**Ticket:** [#27](https://github.com/pyrycode/pyrycode/issues/27)
**Status:** Draft for development
**Locked design source:** [`docs/multi-session.md`](../../multi-session.md), [`docs/plan.md`](../../plan.md)

## Context

Today's `pyry` is structurally one-claude: `cmd/pyry/main.go` constructs a single `*supervisor.Supervisor` and a single `*supervisor.Bridge` (when in service mode) and hands both to the control plane. Phase 1.1 (CLI multi-session) and 1.2 (persistence + idle eviction) need a session-addressable layer they can extend additively, not bolt onto the supervisor.

This ticket lifts that one supervisor into a session-keyed pool that holds exactly one bootstrap entry today. External behaviour is unchanged — wire protocol, foreground/service split, raw-mode handling, SIGWINCH, `--continue` on restart, byte-identical `pyry status`/`stop`/`logs`/`attach` output — but the consumer-side interfaces inside `internal/control` start speaking session IDs, with an absent ID resolving to the default entry.

The motivating constraint: **Phase 1.1 should be additive**. Adding a `SessionID` field to `Request`, wiring CLI subcommands, and supporting `pyry attach <id>` should not require touching `internal/supervisor` or rewiring the lifecycle. After this ticket, the seam where 1.1 plugs in is `Pool.Lookup` and `SessionConfig`.

## Design

### Package layout

New package: `internal/sessions`. Owns session identity, session lifecycle, and the bootstrap-entry pool.

```
internal/sessions/
  id.go         SessionID type, NewID() (UUID via crypto/rand)
  session.go    Session struct: wraps one supervisor + optional bridge
  pool.go       Pool struct: registry, lifecycle, Config / SessionConfig
```

`internal/supervisor` is unchanged in shape — `Supervisor`, `Config`, `Bridge`, `State` keep their current public surfaces. The supervisor stays the workhorse; sessions wraps it with identity and registry semantics.

`internal/control` consumes a single `SessionResolver` interface (defined in `internal/control`, satisfied by `*sessions.Pool`) instead of today's `StateProvider` + `AttachProvider` pair.

Dependency direction:

```
cmd/pyry  →  internal/sessions  →  internal/supervisor
         ↘                     ↗
            internal/control
```

`internal/control` gains a new import of `internal/sessions` (for the `SessionID` type referenced in the resolver interface). `internal/supervisor` keeps no upward imports.

### Naming choice

Package name `sessions` (plural) avoids stuttering on common method calls (`sessions.New`, `sessions.NewID`) and reads naturally next to the locked `sessions.json`/`pyry sessions` terminology. The Phase 1 design doc refers to this layer as "the Pool"; the type name `Pool` is preserved for that direct match.

`runtime` was rejected because it shadows the stdlib `runtime` package and forces import aliases.

### Key types

**`sessions.SessionID`**

```go
// SessionID is a per-session identifier. Locked design uses UUIDs (crypto/rand,
// 36-char canonical form). Phase 1.0 generates one at startup for the bootstrap
// entry; the wire protocol does not carry it yet (Phase 1.1).
type SessionID string

// NewID returns a fresh UUIDv4-shaped SessionID.
func NewID() (SessionID, error)
```

Empty `SessionID` ("") is the **unset sentinel**, not a valid ID. `Pool.Lookup("")` resolves to the default entry — the mechanism that lets Phase 1.0 wire handlers can call `Lookup(req.SessionID)` once Phase 1.1 adds the field, with no behaviour change for old clients.

**`sessions.Session`**

```go
// Session is one supervised claude instance plus the bridge that mediates its
// I/O in service mode. Exactly one Session per Pool entry. Phase 1.1+ spawns
// many; today there is exactly one (the bootstrap entry).
type Session struct {
    id     SessionID
    sup    *supervisor.Supervisor
    bridge *supervisor.Bridge // nil in foreground mode
    log    *slog.Logger
}

func (s *Session) ID() SessionID { return s.id }
func (s *Session) State() supervisor.State { return s.sup.State() }

// Attach binds a client to this session's bridge. Returns ErrAttachUnavailable
// when the session has no bridge (foreground mode), preserving today's
// "daemon may be in foreground mode" error message at the wire layer.
func (s *Session) Attach(in io.Reader, out io.Writer) (done <-chan struct{}, err error)

// Run blocks until ctx is cancelled, supervising this session's claude child.
// Today this is a thin wrapper over s.sup.Run(ctx); Phase 1.1+ keeps the same
// shape so per-session goroutines can be spawned by Pool.Run.
func (s *Session) Run(ctx context.Context) error { return s.sup.Run(ctx) }
```

**`sessions.Pool`**

```go
// Pool owns the set of sessions managed by one pyry process. Phase 1.0
// constructs exactly one entry — the bootstrap session — at New().
type Pool struct {
    mu        sync.RWMutex
    sessions  map[SessionID]*Session
    bootstrap SessionID
    log       *slog.Logger
}

// Config is what cmd/pyry hands to sessions.New.
type Config struct {
    Bootstrap SessionConfig
    Logger    *slog.Logger
}

// SessionConfig is the per-session invocation shape — what Phase 1.1
// `pyry sessions new` will populate, but in Phase 1.0 only the bootstrap
// entry uses it.
//
// Phase 1.0 honours ResumeLast (which maps to supervisor.Config.ResumeLast,
// i.e. --continue on restart). The locked-design `claude --session-id <uuid>`
// invocation lands in Phase 1.1+ and is deliberately NOT introduced here.
type SessionConfig struct {
    ClaudeBin                                 string
    WorkDir                                   string
    ResumeLast                                bool
    ClaudeArgs                                []string
    Bridge                                    *supervisor.Bridge // nil = foreground
    BackoffInitial, BackoffMax, BackoffReset  time.Duration
}

func New(cfg Config) (*Pool, error)

// Lookup resolves a SessionID to a Session. An empty id resolves to the
// default (bootstrap) entry — this is the mechanism that keeps the unchanged
// Phase 1.0 wire protocol working: handlers call Lookup(req.SessionID),
// Phase 1.1 adds the field, no handler diff.
func (p *Pool) Lookup(id SessionID) (*Session, error)

// Default returns the bootstrap session. Equivalent to Lookup("") today;
// kept as an explicit accessor because cmd/pyry needs the bootstrap entry
// at startup for logging.
func (p *Pool) Default() *Session

// Run blocks until ctx is cancelled, supervising every session in the pool.
// Phase 1.0: one session, one supervisor.Run goroutine. Phase 1.1+: errgroup
// fan-out — same call shape, internal change only.
func (p *Pool) Run(ctx context.Context) error

// ErrSessionNotFound is returned by Lookup for a non-empty unknown id.
var ErrSessionNotFound = errors.New("sessions: session not found")
```

### Control plane consumer-side rewiring

`internal/control/server.go` collapses today's two narrow interfaces:

```go
// REMOVED:
type StateProvider  interface { State() supervisor.State }
type AttachProvider interface { Attach(in io.Reader, out io.Writer) (...) }

// ADDED:
type SessionResolver interface {
    Lookup(id sessions.SessionID) (*sessions.Session, error)
}
```

`*sessions.Pool` satisfies `SessionResolver`. `*sessions.Session` already exposes `State()` and `Attach()`.

`Server.handle` keeps its verb dispatch shape. Each verb that needs session state resolves first:

```go
sess, err := s.sessions.Lookup(/* req.SessionID — empty in Phase 1.0 */ "")
if err != nil {
    _ = enc.Encode(Response{Error: err.Error()})
    return
}
// then sess.State(), sess.Attach(...), etc.
```

`Request` is **unchanged** in 1.0 (no `SessionID` field). The empty-string argument to `Lookup` is hard-coded at every call site for now; Phase 1.1 changes that single token to `req.SessionID`.

`NewServer` signature changes accordingly:

```go
// before
func NewServer(socketPath string, state StateProvider, logs LogProvider, attach AttachProvider, shutdown func(), log *slog.Logger) *Server

// after
func NewServer(socketPath string, sessions SessionResolver, logs LogProvider, shutdown func(), log *slog.Logger) *Server
```

The `attach` parameter goes away — attach availability is a property of the session (its bridge being non-nil). `Server.handleAttach` calls `sess.Attach(...)`; if the session is foreground, `Attach` returns `sessions.ErrAttachUnavailable` and the handler emits the same "daemon may be in foreground mode" wire message it does today (text-equivalent for byte-identical output).

The `nil sessions` panic at construction stays — same reasoning as today's `state == nil` check.

### `cmd/pyry/main.go` rewiring

The `runSupervisor` function changes from "build supervisor + bridge + control server" to "build pool + control server":

```go
// Foreground vs service detection stays in main.go (it's a main-only concern
// rooted in stdin's TTY-ness, not a per-session concern).
var bridge *supervisor.Bridge
if !term.IsTerminal(int(os.Stdin.Fd())) {
    bridge = supervisor.NewBridge(logger)
}

pool, err := sessions.New(sessions.Config{
    Logger: logger,
    Bootstrap: sessions.SessionConfig{
        ClaudeBin:  *claudeBin,
        WorkDir:    *workdir,
        ResumeLast: *resume,
        ClaudeArgs: claudeArgs,
        Bridge:     bridge,
    },
})

ctrl := control.NewServer(socketPath, pool, logRing, cancel, logger)
// ... ctrl.Listen, go ctrl.Serve(ctx), pool.Run(ctx), shutdown ...
```

The startup log line ("pyrycode starting", `name`, `claude`, `socket`) is preserved verbatim — the session ID is **not** logged in 1.0 to keep `pyry logs` byte-identical to Phase 0.

### Data flow (unchanged externally)

```
Foreground:
  Terminal ── stdin ─→ pyry main ─→ sessions.Pool.Run ─→ Session.Run
                                                      └─→ supervisor.Run
                                                            └─→ PTY ←→ claude
  Terminal ←── stdout ──────────────────────────────────────────── PTY

Service mode:
  pyry attach client ←→ unix socket ←→ control.Server
                                        └─→ pool.Lookup("") ─→ Session.Attach
                                                                 └─→ Bridge ←→ PTY ←→ claude
```

The arrows are the same as Phase 0; only the boxes between `pyry main` and the supervisor have new names.

### Concurrency model

Phase 1.0 keeps the existing two-goroutine top-level layout in `cmd/pyry/main.go`:

1. **Main goroutine** — calls `pool.Run(ctx)`, blocks until ctx is cancelled.
2. **Control goroutine** — `go ctrl.Serve(ctx)`, accepts client connections.

Inside `Pool.Run` today: one call to `bootstrap.Run(ctx)` → `supervisor.Run(ctx)` → existing PTY-spawn / wait / backoff loop. No new goroutines introduced by the sessions layer in Phase 1.0.

Inside `Pool.Run` in Phase 1.1+: errgroup fan-out, one goroutine per session, propagating the first non-nil error. This is the structural extension point but is **not implemented in 1.0** — adding it now would mean introducing concurrency we can't exercise yet (only one session exists). Add it with the first multi-session test in 1.1.

`Pool.Lookup` and `Pool.Default` are read-mostly; guard the map with `sync.RWMutex` so future per-session-add operations (1.1) take the write lock without contending with the hot read path.

Shutdown sequence is unchanged:

1. SIGINT/SIGTERM → context cancel.
2. `pool.Run` returns `context.Canceled` (propagated from `supervisor.Run`).
3. Bridge (if present) is left for GC — no explicit close needed; the supervisor's PTY closure already unblocks bridge readers via `io.Pipe` semantics on Close. (Same as today.)
4. `ctrl.Close()` removes the socket file; in-flight handlers drain via the existing `streamingWG`.

### Error handling

| Failure | Surface |
|---|---|
| `sessions.NewID` rng failure | `sessions.New` returns the wrapped error — fatal at startup, same fail-fast semantics as today's `supervisor.New`. |
| `Pool.Lookup` unknown id | `ErrSessionNotFound`, wrapped into the wire `Response.Error`. Not reachable in Phase 1.0 (handlers only call `Lookup("")`); becomes live in Phase 1.1. |
| `Session.Attach` on foreground session | `sessions.ErrAttachUnavailable`. The control-plane handler maps this to the same "daemon may be in foreground mode" wire string for byte-identical output. |
| `Session.Attach` while bridge busy | The underlying `supervisor.ErrBridgeBusy` propagates verbatim. Same wire surface as today. |
| Supervisor spawn / backoff failures | Unchanged — bubbled up through `Pool.Run` exactly as `supervisor.Run` bubbles them today. |

The new error sentinels (`ErrSessionNotFound`, `ErrAttachUnavailable`) live in `internal/sessions` and are matched with `errors.Is` in callers per the project convention.

### Wire protocol invariants (preserved)

- `Request` JSON shape unchanged — no `session_id` field.
- `Response` JSON shape unchanged — no per-session metadata.
- `Status`, `Logs`, `Stop`, `Attach` verbs identical.
- `pyry status` byte-identical: `Phase`, `Child PID`, `Restart count`, `Last uptime`, `Next backoff`, `Started at`, `Uptime` — all sourced from `pool.Default().State()`, which delegates to the same `supervisor.State` struct.
- `pyry logs` byte-identical: ring buffer is unchanged; no new log lines emitted by the sessions layer in 1.0.
- `pyry attach` byte-identical: same Bridge, same handshake, same raw stream.
- `--continue` on restart preserved: `SessionConfig.ResumeLast` is wired straight to `supervisor.Config.ResumeLast`.
- Foreground vs service bridge selection still keys off `term.IsTerminal(stdin)` in `cmd/pyry/main.go`.

## Testing strategy

Per AC: new tests cover the runtime layer. Existing supervisor and control tests remain green; the control tests need a one-line constructor signature update plus their fake state shape adjusted from `StateProvider` to `SessionResolver`.

### `internal/sessions/pool_test.go`

| Test | Verifies |
|---|---|
| `TestPool_New_BootstrapInstalled` | After `New(cfg)`, `pool.Default()` returns a non-nil session whose `ID()` is a valid UUID-shaped string. |
| `TestPool_LookupEmpty_ResolvesToDefault` | `pool.Lookup("")` returns the same `*Session` as `pool.Default()`. **This is the consumer-path AC.** |
| `TestPool_LookupByID_ReturnsBootstrap` | `pool.Lookup(pool.Default().ID())` returns the bootstrap session. |
| `TestPool_LookupUnknown_ReturnsErrSessionNotFound` | `errors.Is(err, sessions.ErrSessionNotFound)` for a fabricated ID. |

### `internal/sessions/session_test.go`

| Test | Verifies |
|---|---|
| `TestSession_State_DelegatesToSupervisor` | `sess.State()` returns the supervisor's `State` snapshot. (Use a stub or hit a real `supervisor.New` with a fake claude via the existing `TestHelperProcess` pattern.) |
| `TestSession_Attach_NoBridge` | A session constructed without a bridge returns `sessions.ErrAttachUnavailable` from `Attach`. |
| `TestSession_Attach_DelegatesToBridge` | A session with a bridge returns the bridge's done channel; second concurrent attach gets `supervisor.ErrBridgeBusy`. |
| `TestSession_Run_StopsOnContextCancel` | `sess.Run(ctx)` returns `context.Canceled` after `cancel()`. (Reuses the `TestHelperProcess` fake child the supervisor tests already have.) |

### `internal/sessions/id_test.go`

| Test | Verifies |
|---|---|
| `TestNewID_Format` | Returned IDs match the 36-char canonical UUID shape (`8-4-4-4-12` hex with dashes). |
| `TestNewID_Unique` | 1000 IDs in a row yield no duplicates (smoke test for crypto/rand wiring). |

### `internal/control/server_test.go` (modifications)

- `fakeState` becomes `fakeResolver`, which returns a `*sessions.Session` (or a small interface, depending on what the server takes — see "Open questions" below).
- `NewServer` calls drop the `attach` parameter.
- Existing `TestServer_Status`, `TestServer_Logs`, `TestServer_Stop`, `TestServer_AttachBusy`, etc. keep their assertions; only the wiring at construction changes.
- One new test: `TestServer_Status_ResolvesDefaultSession` — a request with no session field returns the default session's state. This is the AC's "consumer-side interfaces aware of session IDs internally, absent ID resolves to default."

### Manual smoke (per AC)

1. `go build -o pyry ./cmd/pyry && ./pyry` in a terminal — supervisor runs, raw mode + SIGWINCH work.
2. Service-mode launch matching pyrybox: `nohup ./pyry > /tmp/pyry.out 2> /tmp/pyry.err < /dev/null &`. Then in another shell: `pyry status` → byte-identical to Phase 0; `pyry attach` → interactive bridge works; `pyry logs` → expected lines; `pyry stop` → graceful shutdown.
3. Diff `pyry status` and `pyry logs` output against a Phase 0 binary built from `main` to confirm byte-equivalence.

### Race detector

`go test -race ./...` must pass. Particular attention: the `sync.RWMutex` on `Pool.sessions` and the cross-goroutine handoff between `Pool.Run` (main goroutine) and control-server lookups.

## Why M, not split

Considered slices:

- **A: introduce `internal/sessions` (Pool, Session, ID) populated alongside the existing wiring; main.go and control still go direct to `*supervisor.Supervisor`.** Lands the package as dead code — AC-violating until a follow-up rewires the consumers. No external behaviour change observable, no integration to test.
- **B: migrate `cmd/pyry/main.go` to construct `sessions.Pool` for the supervisor lifecycle; control plane still consumes `StateProvider` + `AttachProvider`.** Leaves the control plane consumer-side ID-unaware, which directly violates the third AC ("control plane's consumer-side interfaces … aware of session IDs internally"). Structurally the runtime layer would not actually own the consumer surface it is meant to own.
- **C: rewire control plane to consume `SessionResolver` while main.go keeps direct supervisor wiring.** Inverts ownership — the runtime layer would be a consumer-side fiction, not the structural owner of the supervisor it is meant to wrap.

The seam between A and B+C is artificial (dead code). The seam between A+B and C splits a single-file rewiring that has no internal interface boundary. The seam between A+C and B inverts ownership.

The work is fundamentally one structural unit: introduce the layer, route the lifecycle through it, and route the consumer-side through it — all three are required to satisfy the ACs and all three touch the same wiring point. Splitting either creates dead code on the first slice or fails the ACs on the first slice.

The risk surface is mitigated by: (a) the supervisor and bridge packages stay shape-stable, (b) control plane changes are mechanical (one-line resolver call where there used to be a direct field access), (c) byte-equivalence smoke tests catch any observable drift, (d) the existing supervisor and control tests stay green throughout (they're the regression net for the hot path).

Estimated production-code lines: ~80 in `pool.go`, ~50 in `session.go`, ~20 in `id.go`, ~20 net change in `internal/control/server.go`, ~15 net change in `cmd/pyry/main.go`. Total ≈ 185 lines. Tests scale roughly 1:1.

## Open questions (for the developer to resolve during build)

1. **`SessionResolver` returning `*sessions.Session` vs an interface.** Returning the concrete `*sessions.Session` keeps the resolver definition trivial and avoids a stuttering `control.Session` interface. Cost: `internal/control` imports `internal/sessions`. Benefit: zero indirection, no adapter types in tests. Recommendation: return `*sessions.Session`; revisit in Phase 1.1 if the control plane grows a need to mock per-session behaviour.
2. **`SessionConfig` field for the future `--session-id` flag.** The 1.1 work passes a UUID through to `claude --session-id <uuid>`. Should `SessionConfig` carry an explicit `UseSessionID bool` toggle today (off, asserting `--continue` semantics) or wait? Recommendation: **wait**. Adding the toggle now invents a config surface for behaviour that doesn't exist. Add it with the 1.1 PR that introduces the actual `--session-id` invocation.
3. **Logging the bootstrap session ID.** `pyry logs` is required to be byte-identical, so the structured log lines emitted at startup must not gain a `session_id` field in 1.0. The ID exists internally and can be added to startup logs in 1.1 when the user-visible multi-session shape arrives. Recommendation: do not log the bootstrap ID in 1.0.
4. **Test placement for the lifecycle-cancellation test.** `TestSession_Run_StopsOnContextCancel` needs the `TestHelperProcess` fake-child machinery currently in `internal/supervisor/supervisor_test.go`. Recommendation: duplicate the minimal helper into `internal/sessions/session_test.go` rather than exporting it from `internal/supervisor`. The duplication is ~20 lines and avoids growing the supervisor package's exported surface for tests-only reasons.

## Size

**M.** ~185 lines of production code across one new package and two modified files. See "Why M, not split" above for the seam analysis.
