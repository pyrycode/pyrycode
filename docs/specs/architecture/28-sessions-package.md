# Architecture spec — #28 Phase 1.0a: introduce internal/sessions package (no consumers yet)

**Ticket:** [#28](https://github.com/pyrycode/pyrycode/issues/28)
**Parent:** [#27](https://github.com/pyrycode/pyrycode/issues/27) (locked design source: `docs/specs/architecture/27-session-addressable-runtime.md`)
**Sibling:** [#29](https://github.com/pyrycode/pyrycode/issues/29) (Child B — consumer rewiring; same feature branch)
**Status:** Draft for development
**Size:** **S** (~150 lines production + tests, no consumer churn)

## Context

Slice 1 of 2 for parent ticket #27. Introduce the `internal/sessions` package as a self-contained, well-typed unit with its own tests. No consumer rewiring lands here — `cmd/pyry/main.go` and `internal/control` continue to consume `*supervisor.Supervisor` directly. Child B (#29) flips both consumers in one mechanical follow-up commit on the same feature branch.

The parent spec is the design source of truth. This document defines only the parts of that spec that are realised in this slice (package shape, types, lifecycle delegation, tests) and does not redesign anything. Where the parent spec mentions consumer-side changes, this slice deliberately omits them.

The package ships with unit tests that exercise every type and lifecycle path. It is built into the binary, but unimported by production code in this slice — a routine "introduce the package ahead of integration" pattern, bounded to one PR-shaped unit.

## Design

### Package layout

New package: `internal/sessions`. Three files:

```
internal/sessions/
  id.go         SessionID type, NewID() (UUIDv4 via crypto/rand)
  session.go    Session struct: wraps one supervisor + optional bridge
  pool.go       Pool struct: registry, lifecycle, Config / SessionConfig
```

No edits to existing files in production code paths. No edits to `internal/supervisor` or `internal/control` in this slice. `cmd/pyry/main.go` is untouched.

### Dependency direction

```
internal/sessions  →  internal/supervisor
```

`internal/sessions` imports `internal/supervisor` (for `*supervisor.Supervisor`, `*supervisor.Bridge`, `supervisor.Config`, `supervisor.State`, `supervisor.ErrBridgeBusy`). It does **not** import `internal/control` — `internal/control` stays free of `sessions` references in this slice (Child B introduces them).

The reverse import (`supervisor` → `sessions`) is forbidden and enforced by the package layout. Verify by `go list -deps ./internal/supervisor/...` not naming `internal/sessions`.

### Key types

**`internal/sessions/id.go`**

```go
package sessions

// SessionID is a per-session identifier. Locked design uses UUIDs (crypto/rand,
// 36-char canonical 8-4-4-4-12 form). Phase 1.0 generates one at startup for
// the bootstrap entry; the wire protocol does not carry it yet (Phase 1.1).
type SessionID string

// NewID returns a fresh UUIDv4-shaped SessionID, drawn from crypto/rand.
// Returns an error only when the system rng fails — fatal at startup, same
// fail-fast semantics as today's supervisor.New errors.
func NewID() (SessionID, error)
```

Implementation: read 16 bytes from `crypto/rand.Reader`, set the version (`b[6] = b[6]&0x0f | 0x40`) and variant (`b[8] = b[8]&0x3f | 0x80`) bits, format with `fmt.Sprintf("%08x-%04x-%04x-%04x-%012x", ...)`. No external dependency on a UUID library — stdlib only, ~15 lines.

Empty `SessionID` (`""`) is the **unset sentinel**, never a valid generated ID. The parent spec relies on this in `Pool.Lookup("")` resolving to the default entry; the property is exercised by `TestPool_LookupEmpty_ResolvesToDefault`.

**`internal/sessions/session.go`**

```go
package sessions

import (
    "context"
    "errors"
    "io"
    "log/slog"

    "github.com/pyrycode/pyrycode/internal/supervisor"
)

// ErrAttachUnavailable is returned by Session.Attach when the session has no
// bridge (foreground mode). The Phase 1.0 control plane consumes the
// supervisor's bridge directly and never calls this; Child B (#29) wires the
// control plane to use Session.Attach, at which point this error gets mapped
// to the existing "daemon may be in foreground mode" wire string.
var ErrAttachUnavailable = errors.New("sessions: attach unavailable (no bridge)")

// Session is one supervised claude instance plus the bridge that mediates its
// I/O in service mode. Exactly one Session per Pool entry. Phase 1.1+ spawns
// many; today there is exactly one (the bootstrap entry).
type Session struct {
    id     SessionID
    sup    *supervisor.Supervisor
    bridge *supervisor.Bridge // nil in foreground mode
    log    *slog.Logger
}

// ID returns the session's stable identifier.
func (s *Session) ID() SessionID { return s.id }

// State returns a snapshot of the supervisor's runtime state. Pure delegation
// to (*supervisor.Supervisor).State — preserves the existing
// safe-from-any-goroutine contract.
func (s *Session) State() supervisor.State { return s.sup.State() }

// Attach binds a client to this session's bridge. Returns ErrAttachUnavailable
// when the session has no bridge (foreground mode). Otherwise delegates to
// (*supervisor.Bridge).Attach, propagating supervisor.ErrBridgeBusy verbatim
// when a second client races for the same session.
//
// The bridge does not close in/out — caller owns their lifecycle and closes
// them after `done` fires. Same contract as supervisor.Bridge.Attach.
func (s *Session) Attach(in io.Reader, out io.Writer) (done <-chan struct{}, err error) {
    if s.bridge == nil {
        return nil, ErrAttachUnavailable
    }
    return s.bridge.Attach(in, out)
}

// Run blocks until ctx is cancelled, supervising this session's claude child.
// Pure delegation to (*supervisor.Supervisor).Run today; Phase 1.1+ keeps
// this shape so Pool.Run can fan out one goroutine per session.
func (s *Session) Run(ctx context.Context) error { return s.sup.Run(ctx) }
```

The `log` field is stored but unused in the Phase 1.0 implementation (no per-session log lines emitted — see parent spec open question #3). Keep the field so Phase 1.1 can attach it without re-shaping the struct; reference it in the constructor so `staticcheck` does not flag an unused field. (`s.log = cfg.Logger` in the constructor is sufficient — staticcheck inspects writes, not reads.)

**`internal/sessions/pool.go`**

```go
package sessions

import (
    "context"
    "errors"
    "fmt"
    "log/slog"
    "sync"
    "time"

    "github.com/pyrycode/pyrycode/internal/supervisor"
)

// ErrSessionNotFound is returned by Pool.Lookup for a non-empty unknown id.
var ErrSessionNotFound = errors.New("sessions: session not found")

// Config is what cmd/pyry hands to sessions.New.
type Config struct {
    Bootstrap SessionConfig
    Logger    *slog.Logger
}

// SessionConfig is the per-session invocation shape. Phase 1.0 uses it only
// for the bootstrap entry; Phase 1.1's `pyry sessions new` populates it for
// each new session.
//
// Phase 1.0 honours ResumeLast (which maps to supervisor.Config.ResumeLast,
// i.e. --continue on restart). The locked-design `claude --session-id <uuid>`
// invocation lands in Phase 1.1+ and is deliberately NOT introduced here
// (parent spec open question #2: "wait").
type SessionConfig struct {
    ClaudeBin  string
    WorkDir    string
    ResumeLast bool
    ClaudeArgs []string
    Bridge     *supervisor.Bridge // nil = foreground

    BackoffInitial time.Duration
    BackoffMax     time.Duration
    BackoffReset   time.Duration
}

// Pool owns the set of sessions managed by one pyry process. Phase 1.0
// constructs exactly one entry — the bootstrap session — at New().
type Pool struct {
    mu        sync.RWMutex
    sessions  map[SessionID]*Session
    bootstrap SessionID
    log       *slog.Logger
}

// New constructs a Pool, generating an ID for the bootstrap entry and
// constructing the underlying *supervisor.Supervisor. Returns an error if
// the rng or supervisor.New fails — both are fatal-at-startup conditions.
func New(cfg Config) (*Pool, error)

// Lookup resolves a SessionID to a Session. An empty id resolves to the
// default (bootstrap) entry — this is the mechanism that lets the Phase 1.0
// control plane (after Child B) call Lookup(req.SessionID) with the
// currently-empty field, and Phase 1.1 populates the field with no handler
// diff. A non-empty unknown id returns ErrSessionNotFound.
func (p *Pool) Lookup(id SessionID) (*Session, error)

// Default returns the bootstrap session. Equivalent to Lookup("") today;
// kept as an explicit accessor because cmd/pyry will need the bootstrap
// entry at startup (Child B).
func (p *Pool) Default() *Session

// Run blocks until ctx is cancelled, supervising every session in the pool.
// Phase 1.0: one session, one direct call to bootstrap.Run(ctx) — no errgroup.
// Phase 1.1+ replaces the body with errgroup fan-out; the call shape is the
// extension point.
func (p *Pool) Run(ctx context.Context) error
```

#### `New(cfg Config)` — internal sequence

1. Default `cfg.Logger` to `slog.Default()` if nil.
2. `id, err := NewID()` — wrap the error: `fmt.Errorf("sessions: generate bootstrap id: %w", err)`.
3. Translate `cfg.Bootstrap` to `supervisor.Config{ClaudeBin, WorkDir, ResumeLast, ClaudeArgs, Bridge, Logger: cfg.Logger, BackoffInitial, BackoffMax, BackoffReset}`. The supervisor's own `New` applies its defaults (claude bin lookup, backoff timings); `sessions.New` does not duplicate them.
4. `sup, err := supervisor.New(supCfg)` — return wrapped error on failure.
5. Construct `&Session{id, sup, bridge: cfg.Bootstrap.Bridge, log: cfg.Logger}`.
6. Construct `&Pool{sessions: map[SessionID]*Session{id: sess}, bootstrap: id, log: cfg.Logger}` and return.

#### `Lookup` — RWMutex semantics

```go
func (p *Pool) Lookup(id SessionID) (*Session, error) {
    p.mu.RLock()
    defer p.mu.RUnlock()
    if id == "" {
        return p.sessions[p.bootstrap], nil
    }
    sess, ok := p.sessions[id]
    if !ok {
        return nil, ErrSessionNotFound
    }
    return sess, nil
}
```

`Default()` is a separate accessor — same body without the empty-string branch — so callers reading the bootstrap session at startup don't pay the lookup-by-string overhead and don't carry an `error` return that they know is impossible.

#### `Run` — direct delegation, no errgroup

```go
func (p *Pool) Run(ctx context.Context) error {
    p.mu.RLock()
    bootstrap := p.sessions[p.bootstrap]
    p.mu.RUnlock()
    return bootstrap.Run(ctx)
}
```

Reasoning (parent spec, "Concurrency model"): the errgroup fan-out is the structural extension point for Phase 1.1, but introducing it now would mean adding concurrency machinery we cannot exercise (only one session exists). The parent spec is explicit: "Add it with the first multi-session test in 1.1." This slice obeys.

The RLock around the bootstrap read is overkill today (no writers exist) but documents the contract: `p.sessions` is a shared map, all reads take the read lock. Phase 1.1's `Pool.Add` will take the write lock without changing `Run`.

### Concurrency model

Phase 1.0 introduces no new goroutines in the sessions layer. `Pool.Run` calls `Session.Run` calls `supervisor.Run` — the existing PTY-spawn / wait / backoff loop is the only work. The control-plane goroutine, the supervisor's own I/O bridge goroutines, and the SIGWINCH watcher are unchanged (they all live in their existing packages).

`sync.RWMutex` on `Pool.sessions`:
- `Lookup` and `Default` take the read lock.
- `Pool.Run` takes the read lock once, briefly, to grab the bootstrap pointer.
- No writers in 1.0. Phase 1.1's `Add(SessionConfig) (*Session, error)` will take the write lock.

The race detector (`go test -race ./...`) is the regression net for this. The parent spec calls out the cross-goroutine handoff between `Pool.Run` (main goroutine) and future control-server lookups; in this slice the handoff is moot (no consumer), but the lock structure is in place so Child B inherits it for free.

### Error handling

| Condition | Surface |
|---|---|
| `NewID()` rng failure | `sessions.New` returns `fmt.Errorf("sessions: generate bootstrap id: %w", err)`. Fatal at startup. |
| `supervisor.New` failure (claude binary not found, etc.) | Propagated wrapped: `fmt.Errorf("sessions: bootstrap supervisor: %w", err)`. |
| `Pool.Lookup` unknown non-empty id | `ErrSessionNotFound` (sentinel, matchable via `errors.Is`). Not reachable in Phase 1.0 (no consumer); covered by tests. |
| `Session.Attach` on session with nil bridge | `ErrAttachUnavailable` (sentinel). Not reachable in Phase 1.0; covered by tests. |
| `Session.Attach` while bridge busy | `supervisor.ErrBridgeBusy` propagated verbatim — no wrap, so `errors.Is(err, supervisor.ErrBridgeBusy)` continues to work in callers. |
| `Session.Run` / `Pool.Run` ctx cancel | `context.Canceled` propagated from `supervisor.Run`. |

Sentinel errors (`ErrSessionNotFound`, `ErrAttachUnavailable`) live in the `sessions` package per the project convention. `supervisor.ErrBridgeBusy` stays in the `supervisor` package — `Session.Attach` does not re-export or wrap it.

## Testing strategy

Three test files mirroring the production layout. Stdlib `testing` only; no testify. The 10 test rows below are the AC-mandated coverage.

### `internal/sessions/id_test.go`

| Test | Verifies | Notes |
|---|---|---|
| `TestNewID_Format` | Returned IDs match `^[0-9a-f]{8}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{12}$` (36 chars total). | Compile a `regexp.MustCompile` once at the top of the test. |
| `TestNewID_Unique` | 1000 IDs in a row yield no duplicates. | `map[SessionID]struct{}` collision check. Smoke test for crypto/rand wiring. |

### `internal/sessions/pool_test.go`

A single `helperPool(t *testing.T) *Pool` builder constructs a pool with a no-op-friendly bootstrap config. Use a real benign binary as `ClaudeBin` (`/bin/sleep` on Unix — both Linux and macOS have it, and `exec.LookPath` succeeds) — none of these tests call `Pool.Run` or `Session.Run`, so the binary is never spawned. This avoids the test-helper-process dance entirely for the pool tests.

| Test | Verifies |
|---|---|
| `TestPool_New_BootstrapInstalled` | After `New(cfg)`, `pool.Default()` returns non-nil; its `ID()` is a valid 36-char UUID-shaped string. |
| `TestPool_LookupEmpty_ResolvesToDefault` | `pool.Lookup("")` returns the same pointer as `pool.Default()`. The consumer-path AC. |
| `TestPool_LookupByID_ReturnsBootstrap` | `pool.Lookup(pool.Default().ID())` returns the bootstrap session. |
| `TestPool_LookupUnknown_ReturnsErrSessionNotFound` | `errors.Is(err, sessions.ErrSessionNotFound)` for a fabricated 36-char ID. |

### `internal/sessions/session_test.go`

| Test | Verifies | Approach |
|---|---|---|
| `TestSession_State_DelegatesToSupervisor` | `sess.State()` returns the supervisor's State snapshot (Phase, ChildPID, etc.). | Construct via `helperPool` and assert the initial state — `Phase == supervisor.PhaseStarting`, `ChildPID == 0`. No `Run` call. |
| `TestSession_Attach_NoBridge` | A session constructed without a bridge returns `ErrAttachUnavailable` from `Attach`. | Build a pool with `Bootstrap.Bridge = nil`. Call `Attach(strings.NewReader(""), io.Discard)`. `errors.Is(err, sessions.ErrAttachUnavailable)`. |
| `TestSession_Attach_DelegatesToBridge` | A session with a bridge: first `Attach` returns a non-nil done channel; second concurrent `Attach` returns `supervisor.ErrBridgeBusy`. | Build with `Bootstrap.Bridge = supervisor.NewBridge(logger)`. First `Attach` uses an `io.Pipe` so the input pump blocks and `done` stays open; second `Attach` returns the busy error. Close the writer to release the first attach in `t.Cleanup`. |
| `TestSession_Run_StopsOnContextCancel` | `sess.Run(ctx)` returns `context.Canceled` after `cancel()`. | Use `/bin/sleep` with a long argument (`["3600"]`) as the fake claude. The supervisor spawns it in a PTY; main test calls `cancel()`; `Run` returns `context.Canceled` (the supervisor's `Run` returns `ctx.Err()` directly, regardless of how the child exits). Assert `errors.Is(err, context.Canceled)` within a `time.After(2*time.Second)` deadline. |

#### Why not duplicate `TestHelperProcess`

The parent spec's open question #4 recommended duplicating the supervisor's `TestHelperProcess` re-exec helper into `internal/sessions/session_test.go` (~20 lines). Investigating the actual code surface revealed a blocker: `supervisor.Config.helperEnv` is **unexported**, and it's the only seam by which the supervisor passes test-only env to the spawned child without polluting the parent test process's `os.Environ()`. External packages cannot set it.

The three viable workarounds:

1. **Promote `helperEnv` to `ExtraEnv` (exported).** Cleanest re-use, but expands `supervisor.Config`'s public surface for tests-only reasons — the very thing open question #4 was trying to avoid.
2. **`t.Setenv("GO_TEST_HELPER_PROCESS", "1")` in the sessions test.** Pollutes the test process's env; the supervisor's `cmd.Env = append(os.Environ(), ...)` then propagates to the child correctly, but the env mutation conflicts with `t.Parallel()` and risks bleeding into sibling tests in the same binary.
3. **Use a real benign binary (`/bin/sleep`) as the fake claude.** No re-exec, no env injection, no helper duplication. The supervisor spawns the binary, ctx cancellation kills it, `supervisor.Run` returns `ctx.Err()` directly — which is exactly what the test asserts.

This spec adopts option 3: it requires zero test infrastructure beyond what `os/exec` provides, side-steps the unexported-field problem, keeps the supervisor's test surface unchanged, and the test asserts the only contract that matters (delegation of context cancellation). `/bin/sleep` exists on both target platforms (Linux and macOS); CI runs both.

The other three session tests (`State`, `Attach_NoBridge`, `Attach_DelegatesToBridge`) construct a `Session` via `Pool` but never call `Run`, so no fake child is needed at all.

### Race detector

`go test -race ./...` must pass. The pool tests exercise `Lookup`/`Default` from a single goroutine; the `Run` test crosses goroutines (test goroutine cancels ctx; supervisor goroutine returns from `Run`) and is the meaningful race-detector load.

### Static analysis

`go vet ./...` and `staticcheck ./...` clean. The unused `log` field on `Session` is satisfied by the constructor write described in the `session.go` notes above.

## Wire / behaviour invariants (this slice introduces zero behaviour change)

- `Request`/`Response` JSON shape unchanged — Child B does the rewiring and even Child B keeps the wire shape.
- No new log lines; no startup log changes; bootstrap session ID **not logged** (parent spec open question #3).
- No CLI surface changes.
- `cmd/pyry/main.go` is byte-identical post-merge of this slice.
- `internal/control/server.go`, `server_test.go`, and the rest of the `internal/control` tree are byte-identical post-merge of this slice.
- `internal/supervisor/*` is byte-identical post-merge of this slice.

The new package is built (it's in the module path) and imported by the test binary, but no production code path imports it. Verified by `go build ./...` succeeding and `grep -r 'internal/sessions' cmd/ internal/control/ internal/supervisor/` returning empty.

## Acceptance criteria mapping

Each AC bullet maps to an artefact in this spec:

- **New package shape, sentinel errors** → "Package layout" + "Key types" sections; `id.go`/`session.go`/`pool.go` defined.
- **`NewID` 36-char canonical UUID, crypto/rand** → `id.go` "Implementation" paragraph.
- **`New` installs one bootstrap entry, `Default` non-nil** → `New(cfg Config)` internal sequence step 6.
- **`Lookup("")` resolves to `Default()`** → `Lookup` body + `TestPool_LookupEmpty_ResolvesToDefault`.
- **`Lookup(Default().ID())` resolves correctly** → `Lookup` body + `TestPool_LookupByID_ReturnsBootstrap`.
- **`Lookup(unknown)` → `ErrSessionNotFound` (errors.Is)** → `Lookup` body + `TestPool_LookupUnknown_ReturnsErrSessionNotFound`.
- **`Session.ID()` / `State()` / `Attach` / `Attach` busy** → `session.go` types + four session_test rows.
- **`Run(ctx)` returns `context.Canceled`** → `Pool.Run` body + `Session.Run` body + `TestSession_Run_StopsOnContextCancel`.
- **No changes to main.go / control / supervisor** → "Wire / behaviour invariants" section + dependency-direction grep test described there.
- **Sessions does not import control; supervisor does not import sessions** → "Dependency direction" section.
- **All 10 unit-test rows present; `-race` clean; `vet` clean** → "Testing strategy" section.

## Hand-off to Child B (#29)

Child B does **not** need to read this document — it consumes the package via the public API defined in "Key types". The integration points it will modify:

1. `internal/control/server.go` — replace `StateProvider`+`AttachProvider` interfaces with `SessionResolver`; per-verb `Lookup("")` calls.
2. `cmd/pyry/main.go` — replace `supervisor.New` + `NewBridge` direct construction with `sessions.New(Config{...})` and call `pool.Run(ctx)`.
3. `internal/control/server_test.go` — `fakeState` becomes a fake resolver returning a `*sessions.Session`; constructor calls drop the `attach` parameter.

The package contract this spec defines is the seam Child B integrates against. Anything Child B needs (e.g. additional `Pool` accessors) is out of scope for this slice and goes back to the parent #27 spec for review.

## Out of scope (deferred to Child B and beyond)

- Consumer-side rewiring of `internal/control` and `cmd/pyry/main.go` → Child B (#29).
- `Request.SessionID` field, CLI subcommands, `pyry attach <id>` → Phase 1.1.
- `claude --session-id <uuid>` invocation → Phase 1.1+.
- `errgroup` fan-out inside `Pool.Run` → Phase 1.1, with the first multi-session test.
- Per-session log lines, including bootstrap ID logging → Phase 1.1+.
- `Pool.Add(SessionConfig) (*Session, error)` (write-locked map insert) → Phase 1.1.

## Open questions (for the developer to resolve during build)

1. **Should `helperPool` live as a test helper or be inlined per test?** Recommendation: define one `helperPool(t *testing.T, withBridge bool) *Pool` in `pool_test.go` and reuse it from `session_test.go` via the same package's test scope. The `withBridge` switch is the only axis. Inline-per-test would duplicate the `sessions.Config` construction four times for ~no gain.

2. **`/bin/sleep` portability.** Both Linux and macOS ship `/bin/sleep`; CI runs both. If a future CI environment lacks it, the fallback is `exec.LookPath("sleep")` — but the test should fail loudly (call `t.Skip` only if the lookup explicitly returns `exec.ErrNotFound`, never silently). Recommendation: use the absolute path `/bin/sleep`; if `exec.LookPath` fails, `t.Skipf("benign binary not available: %v", err)`. This keeps the test honest about what it's exercising.

3. **`Session.log` field and `staticcheck`.** The `log` field is written by the constructor and not read in 1.0. Modern `staticcheck` flags U1000 only on completely-unused symbols (read **and** write absent); a written-never-read struct field is allowed. If staticcheck does flag it, drop the field for now and re-add it in 1.1 — keep the spec's wording prescriptive rather than the field's presence. Recommendation: include the field; only remove if a CI run actually flags it.

4. **`Pool.Run` RLock micro-optimisation.** Strictly, `bootstrap.Run(ctx)` could be called without taking the lock (the bootstrap pointer is set once at `New` and never mutated until Phase 1.1 introduces `Add`). Recommendation: take the RLock anyway — cost is microseconds, benefit is that the read pattern is the same as `Lookup`/`Default` and the contract is uniform, so when Phase 1.1 adds writers there is one less subtle "but here we don't lock" rule.

## Size

**S.** The package is bounded: `id.go` ~20 lines, `session.go` ~50 lines, `pool.go` ~80 lines. Tests scale roughly 1:1 (~150 lines test code). No churn in existing files. The work is mechanical once the contract above is fixed; there is no consumer integration in this slice.
