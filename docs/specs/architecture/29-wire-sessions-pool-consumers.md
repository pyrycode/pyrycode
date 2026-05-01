# Architecture spec — #29 Phase 1.0b: wire cmd/pyry and internal/control to consume sessions.Pool

**Ticket:** [#29](https://github.com/pyrycode/pyrycode/issues/29)
**Parent:** [#27](https://github.com/pyrycode/pyrycode/issues/27) (locked design source: `docs/specs/architecture/27-session-addressable-runtime.md`)
**Sibling:** [#28](https://github.com/pyrycode/pyrycode/issues/28) (Child A — package introduction; merged)
**Status:** Draft for development
**Size:** **S** (~35 net production lines, mechanical test updates)

## Context

Slice 2 of 2 for parent #27. Child A (#28) shipped `internal/sessions` with no production consumers; this slice flips the consumers. After this lands, every Phase 1.0 acceptance criterion in the parent ticket goes green: pyry runs through `sessions.Pool` instead of a raw `*supervisor.Supervisor`; `internal/control` resolves session IDs internally; the wire protocol is unchanged; `pyry status`/`stop`/`logs`/`attach` are byte-identical to Phase 0.

The parent spec is the design source of truth for *why* this layering exists. This spec defines *what changes file-by-file in this slice* and resolves the parent's deferred open question on `SessionResolver` shape — which has a non-obvious testing implication that needs an architectural call before code lands.

## Design

### Dependency direction (post-merge)

```
cmd/pyry  →  internal/sessions  →  internal/supervisor
         ↘                     ↗
            internal/control
```

`internal/control` gains an import of `internal/sessions` (for `sessions.SessionID` referenced in the resolver interface). `internal/supervisor` keeps no upward imports — verified by `go list -deps ./internal/supervisor/...` not naming `internal/sessions` or `internal/control`.

### `internal/control/server.go` — the resolver seam

Today's two narrow interfaces collapse to a single resolver pair:

```go
// REMOVED:
type StateProvider  interface { State() supervisor.State }
type AttachProvider interface { Attach(in io.Reader, out io.Writer) (...) }

// ADDED:
type Session interface {
    State() supervisor.State
    Attach(in io.Reader, out io.Writer) (done <-chan struct{}, err error)
}

type SessionResolver interface {
    Lookup(id sessions.SessionID) (Session, error)
}
```

`*sessions.Session` satisfies `control.Session` structurally (it already has both methods with matching signatures — verify by reading `internal/sessions/session.go`). `*sessions.Pool` does **not** satisfy `SessionResolver` directly because Go does not do covariant return types on interface satisfaction (Pool's `Lookup` returns `*sessions.Session`, not `control.Session`). A trivial adapter in `cmd/pyry/main.go` bridges the two — see "cmd/pyry/main.go rewiring" below.

`Server` struct: drop `state StateProvider`, drop `attach AttachProvider`, add `sessions SessionResolver`. `closedCh`, `streamingWG`, `listener`, etc. are unchanged.

`NewServer` signature:

```go
// before
func NewServer(socketPath string, state StateProvider, logs LogProvider, attach AttachProvider, shutdown func(), log *slog.Logger) *Server

// after
func NewServer(socketPath string, sessions SessionResolver, logs LogProvider, shutdown func(), log *slog.Logger) *Server
```

The `nil sessions` panic-on-construction guard replaces today's `nil state` panic — same reasoning (programmer error, surface immediately):

```go
if sessions == nil {
    panic("control.NewServer: sessions is required, got nil")
}
```

`Server.handle` keeps its verb-dispatch shape. Verbs that need session state resolve through the resolver:

```go
switch req.Verb {
case VerbStatus:
    sess, err := s.sessions.Lookup("")  // Phase 1.1 will swap "" → req.SessionID
    if err != nil {
        _ = enc.Encode(Response{Error: err.Error()})
        return
    }
    _ = enc.Encode(Response{Status: buildStatus(sess.State())})
case VerbLogs:
    s.handleLogs(enc)        // unchanged — logs are process-global, not per-session
case VerbStop:
    s.handleStop(enc)        // unchanged — stop is process-global
case VerbAttach:
    if s.handleAttach(conn, enc) { closeConn = false }
default:
    _ = enc.Encode(Response{Error: fmt.Sprintf("unknown verb: %q", req.Verb)})
}
```

`handleAttach` resolves first, then calls `sess.Attach`:

```go
func (s *Server) handleAttach(conn net.Conn, enc *json.Encoder) (handedOff bool) {
    sess, err := s.sessions.Lookup("")  // Phase 1.1 will swap "" → req.SessionID
    if err != nil {
        _ = enc.Encode(Response{Error: fmt.Sprintf("attach: %v", err)})
        return false
    }
    _ = conn.SetDeadline(time.Time{})
    done, err := sess.Attach(conn, conn)
    if err != nil {
        // sessions.ErrAttachUnavailable from a foreground-mode session maps
        // to today's wire string for byte-identical output — the existing
        // fmt.Sprintf("attach: %v", err) shape produces "attach: sessions:
        // attach unavailable (no bridge)", which differs from Phase 0's
        // "attach: no attach provider configured (daemon may be in foreground
        // mode)". Match the Phase 0 string explicitly.
        if errors.Is(err, sessions.ErrAttachUnavailable) {
            _ = enc.Encode(Response{Error: "attach: no attach provider configured (daemon may be in foreground mode)"})
            return false
        }
        _ = enc.Encode(Response{Error: fmt.Sprintf("attach: %v", err)})
        return false
    }
    s.log.Info("control: client attached")
    _ = enc.Encode(Response{OK: true})
    s.streamingWG.Add(1)
    go func() {
        defer s.streamingWG.Done()
        <-done
        _ = conn.Close()
        s.log.Info("control: client detached")
    }()
    return true
}
```

The `errors.Is` mapping is **load-bearing** for AC "byte-identical attach output." Without it, a foreground-mode pyry's response to `pyry attach` would change from "no attach provider configured (daemon may be in foreground mode)" to "sessions: attach unavailable (no bridge)" — observable drift.

`handleLogs` and `handleStop` are unchanged. Logs come from the ring buffer (process-global); stop calls the configured `shutdown` callback (process-global). Neither needs session resolution today; Phase 1.1 may revisit `pyry stop` if per-session stop becomes a verb.

`buildStatus` is unchanged — it consumes a `supervisor.State` value, which `sess.State()` returns identically to today's `state.State()`.

### `cmd/pyry/main.go` — pool wiring

`runSupervisor` changes from "build supervisor + bridge + pass both to control" to "build pool + pass adapter to control."

```go
// Foreground vs service mode detection stays here (it's a main-only concern
// rooted in stdin's TTY-ness, not a per-session concern).
var bridge *supervisor.Bridge
if !term.IsTerminal(int(os.Stdin.Fd())) {
    bridge = supervisor.NewBridge(logger)
}

ctx, cancel := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
defer cancel()

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
if err != nil {
    return fmt.Errorf("pool init: %w", err)
}

ctrl := control.NewServer(socketPath, poolResolver{pool}, logRing, cancel, logger)
if err := ctrl.Listen(); err != nil {
    return fmt.Errorf("control listen: %w", err)
}
defer func() { _ = ctrl.Close() }()

ctrlDone := make(chan error, 1)
go func() { ctrlDone <- ctrl.Serve(ctx) }()

logger.Info("pyrycode starting",
    "version", Version,
    "name", *name,
    "claude", cfg.ClaudeBin,
    "socket", socketPath,
)
runErr := pool.Run(ctx)

_ = ctrl.Close()
<-ctrlDone

if runErr != nil && !errors.Is(runErr, context.Canceled) {
    return fmt.Errorf("supervisor: %w", runErr)
}
logger.Info("pyrycode stopped")
return nil
```

The startup log line is **byte-identical** — same fields, same order, no new `session_id`. The error-return shape (`"supervisor: %w"`) is preserved verbatim so `pyry` exit messages don't drift.

The pool adapter sits next to `runSupervisor`:

```go
// poolResolver adapts *sessions.Pool to control.SessionResolver. The shapes
// differ only in the return type: Pool.Lookup returns *sessions.Session,
// SessionResolver.Lookup returns control.Session (an interface satisfied by
// *sessions.Session). Go's lack of covariant return types on interface
// satisfaction is the only reason this adapter exists.
type poolResolver struct{ p *sessions.Pool }

func (r poolResolver) Lookup(id sessions.SessionID) (control.Session, error) {
    return r.p.Lookup(id)
}
```

Five lines plus a doc comment. Lives in `cmd/pyry/main.go` because it's the only place that knows both the concrete pool and the control-layer interface.

The `attachProvider` variable and the `bridge != nil` guard around it are deleted — attach availability is now a property of the session (its bridge being non-nil), surfaced via `sessions.ErrAttachUnavailable`.

### Lifecycle & concurrency (unchanged externally)

Two top-level goroutines, same as today:

1. **Main goroutine** — calls `pool.Run(ctx)`, blocks until ctx is cancelled. Inside Pool.Run today: one direct call to `bootstrap.Run(ctx)` → `supervisor.Run(ctx)`. No new goroutines.
2. **Control goroutine** — `go ctrl.Serve(ctx)`, accepts client connections.

Shutdown sequence is unchanged:

1. SIGINT/SIGTERM → context cancel via `signal.NotifyContext`.
2. `pool.Run` returns `context.Canceled` (propagated from `supervisor.Run`).
3. `ctrl.Close()` removes the socket file; in-flight handlers drain via `streamingWG`.

`Pool.Lookup` taking the read lock is the cross-goroutine handoff between the supervisor goroutine and control-server lookups. The race detector is the regression net.

### Wire protocol invariants (preserved)

- `Request`/`Response` JSON shapes unchanged — no `session_id` field.
- `pyry status` byte-identical: every field sourced from `pool.Default().State()` via the resolver, which delegates unchanged to `supervisor.State`.
- `pyry logs` byte-identical: ring buffer is unchanged; no new log lines emitted by the sessions layer.
- `pyry attach` byte-identical: same Bridge, same handshake, same raw stream, same "daemon may be in foreground mode" error string (preserved by the `ErrAttachUnavailable` mapping above).
- `--continue` on restart preserved: `SessionConfig.ResumeLast` is wired straight to `supervisor.Config.ResumeLast` inside `sessions.New`.
- Foreground vs service bridge selection still keys off `term.IsTerminal(stdin)` in `cmd/pyry/main.go`.
- Restart still uses `--continue`. `claude --session-id <uuid>` is **not** introduced here.
- Startup log line preserved verbatim.

## Resolved open question — `SessionResolver` shape

The parent spec deferred a recommendation: "Returning the concrete `*sessions.Session` keeps the resolver definition trivial … revisit in Phase 1.1 if the control plane grows a need to mock per-session behaviour." That need exists **now**, in this slice.

`internal/control/server_test.go` today has `fakeState{st: supervisor.State{Phase: PhaseRunning, ChildPID: 12345, ...}}` driving `TestServer_Status` and `TestServer_StatusInBackoff` with specific state values. After the rewire, those tests must drive `Server` through the resolver. The options:

| Option | Cost | Verdict |
|---|---|---|
| **A.** SessionResolver returns `*sessions.Session` (concrete). Tests construct real Sessions via `sessions.New`. | `*sessions.Session.State()` delegates to `*supervisor.Supervisor.State()`, which has no exported state setter — tests can only assert `PhaseStarting, ChildPID 0, RestartCount 0`, weakening `TestServer_Status` and `TestServer_StatusInBackoff` to triviality. | ✗ |
| **B.** SessionResolver returns `*sessions.Session`. Add a test-only state injector to `internal/sessions` (e.g. an exported `SessionForTest(state, bridge) *Session` plus a `testState *supervisor.State` shadow field on Session). | Modifies the freshly-merged sessions package for tests-only reasons; expands its public surface; introduces an ugly carve-out in Session.State(). | ✗ |
| **C.** SessionResolver returns a small `Session` interface defined in `internal/control`. `*sessions.Session` satisfies it structurally; tests fake it with a few lines. cmd/pyry adapter (~5 lines) bridges Pool → resolver. | One extra adapter type in `main.go`; resolver definition gains one line for the `Session` interface. | ✓ |

**Decision: Option C.** It keeps the sessions package untouched, keeps the test surface clean, follows the existing pattern (the removed `StateProvider` was likewise a small interface defined in `internal/control` where it is consumed), and preserves Phase 0's existing test fakes' shape — `fakeState`+`fakeAttachProvider` collapse into a single `fakeSession`+`fakeResolver` pair.

The literal AC text says "`SessionResolver` interface satisfied by `*sessions.Pool`." Option C deviates: `*sessions.Pool` is satisfied by an inline `poolResolver` adapter, not directly. This is a deliberate architectural call — the spec is the design source. Update the AC during PR review to reflect the adapter; the underlying intent (single resolver-shaped seam at the control plane, no `attach` parameter on `NewServer`, empty-id resolves to default) is preserved exactly.

The parent spec's Phase 1.1 forward path is unaffected: when `Request.SessionID` lands, `s.sessions.Lookup("")` becomes `s.sessions.Lookup(req.SessionID)`. No additional indirection introduced by Option C affects that swap.

## Error handling

| Failure | Surface |
|---|---|
| `pool.Run` propagation of `context.Canceled` | Same as Phase 0 — main returns nil, "pyrycode stopped" log line. |
| `pool.Run` propagation of supervisor spawn / backoff failure | Same as Phase 0 — wrapped with `"supervisor: %w"` and returned from `runSupervisor`. |
| `s.sessions.Lookup("")` returning `ErrSessionNotFound` | Not reachable in Phase 1.0 (the bootstrap entry always exists); covered defensively by the encoder error path. Becomes live in Phase 1.1. |
| `sess.Attach` returning `sessions.ErrAttachUnavailable` | Mapped explicitly to Phase 0's "no attach provider configured (daemon may be in foreground mode)" wire string. **Load-bearing** for byte-identical attach output. |
| `sess.Attach` returning `supervisor.ErrBridgeBusy` | Propagated via the existing `fmt.Sprintf("attach: %v", err)` path — same wire surface as today (`Bridge.Attach` returns it directly). |
| `sessions.New` rng or supervisor failures | Returned from `runSupervisor` as `"pool init: %w"`. Fatal at startup. |

The `ErrAttachUnavailable` mapping is the one place this slice has a behavior-preserving translation layer. Document it at the call site so Phase 1.1's wire-protocol changes don't accidentally drop the mapping.

## Testing strategy

### `internal/control/server_test.go` (modifications)

Mechanical updates plus one new test. The `fakeState` + `fakeAttachProvider` pair collapses into a `fakeResolver` + `fakeSession` pair satisfying the new control-package interfaces.

```go
// fakeSession satisfies control.Session for tests.
type fakeSession struct {
    mu       sync.Mutex
    state    supervisor.State
    attachFn func(in io.Reader, out io.Writer) (<-chan struct{}, error)
}

func (f *fakeSession) State() supervisor.State {
    f.mu.Lock()
    defer f.mu.Unlock()
    return f.state
}

func (f *fakeSession) Attach(in io.Reader, out io.Writer) (<-chan struct{}, error) {
    if f.attachFn == nil {
        return nil, errors.New("fakeSession: no attach configured")
    }
    return f.attachFn(in, out)
}

// fakeResolver returns its single fakeSession for any id (including "").
// Tests that exercise unknown-id paths set notFound to true.
type fakeResolver struct {
    sess     *fakeSession
    notFound bool
}

func (r *fakeResolver) Lookup(id sessions.SessionID) (Session, error) {
    if r.notFound {
        return nil, sessions.ErrSessionNotFound
    }
    return r.sess, nil
}
```

| Existing test | Update |
|---|---|
| `TestServer_Status`, `TestServer_StatusInBackoff` | Build `&fakeResolver{sess: &fakeSession{state: ...}}`; pass to `NewServer` (drops the `attach` arg, swaps `state` for resolver). Assertions unchanged. |
| `TestServer_UnknownVerb`, `TestServer_StaleSocketIsReplaced`, `TestServer_CloseRemovesSocket`, `TestServer_ListenRefusesActiveInstance`, `TestServer_ListenFailsWhenParentDirIsAFile`, `TestServer_ConcurrentClose`, `TestClient_DialFailsCleanly` | Mechanical: `&fakeState{}` → `&fakeResolver{sess: &fakeSession{}}`; drop `attach` arg from `NewServer` calls. |
| `TestNewServer_PanicsOnNilState` → renamed `TestNewServer_PanicsOnNilSessions` | Pass `nil` resolver, assert panic message mentions "sessions". |
| `TestServer_Stop`, `TestServer_StopWithoutHandler`, `TestServer_HandshakeTimeout`, `TestClient_ServerHangup` | Mechanical NewServer signature update. |
| `TestServer_AttachHandshakeAndStream`, `TestServer_AttachIgnoresGeometryToday` | `fakeAttachProvider` becomes the `attachFn` on a `fakeSession`. Mechanical re-wiring; assertions unchanged. |
| `TestServer_AttachWithoutProvider` → renamed `TestServer_AttachOnForegroundSession` | Resolver returns a `fakeSession` whose `attachFn` returns `sessions.ErrAttachUnavailable`. Assert response error text matches Phase 0's "no attach provider configured (daemon may be in foreground mode)" verbatim — this is the byte-identical AC's lock-in. |
| `TestServer_StopWhileAttached`, `TestServer_BridgeAttach`, `TestServer_ConcurrentAttachRace` | These use a real `*supervisor.Bridge` as the attach provider. After rewire: wrap the bridge in a `fakeSession` whose `attachFn` delegates to `bridge.Attach`. Assertions unchanged. |

**New test:**

```go
// TestServer_Status_ResolvesDefaultSession verifies the resolver path: a
// VerbStatus request with no session field flows through Lookup("") to the
// default session. Phase 1.1 will populate req.SessionID; this test pins
// the empty-id-resolves-to-default behaviour as the seam.
func TestServer_Status_ResolvesDefaultSession(t *testing.T) {
    t.Parallel()

    sess := &fakeSession{state: supervisor.State{
        Phase:        supervisor.PhaseRunning,
        ChildPID:     999,
        StartedAt:    time.Now(),
        RestartCount: 7,
    }}
    var lookupCalls []sessions.SessionID
    resolver := &recordingResolver{
        delegate: &fakeResolver{sess: sess},
        record:   func(id sessions.SessionID) { lookupCalls = append(lookupCalls, id) },
    }

    sock, stop := startServer(t, resolver)
    defer stop()

    resp, err := Status(context.Background(), sock)
    if err != nil { t.Fatalf("Status: %v", err) }
    if resp.ChildPID != 999 || resp.RestartCount != 7 {
        t.Errorf("status payload did not come from the resolved session: %+v", resp)
    }
    if len(lookupCalls) != 1 || lookupCalls[0] != "" {
        t.Errorf("expected exactly one Lookup call with empty id, got %v", lookupCalls)
    }
}
```

`recordingResolver` is a tiny one-off helper in this test file (a struct that wraps a delegate and appends to a slice on each `Lookup`). The seam — that the empty id is what the handler passes — is the assertion that Phase 1.1 will need to relax (to `req.SessionID`), so locking it down here makes the future change visible.

### Existing tests stay green

- `internal/sessions/*_test.go` — untouched. The package's contract is fixed; this slice only consumes it.
- `internal/supervisor/*_test.go` — untouched. The supervisor's public surface is unchanged.
- `cmd/pyry/args_test.go` — untouched. Argument splitting is independent of the supervisor wiring.

### Race detector

`go test -race ./...` must pass. The cross-goroutine handoffs exercised:

- `Pool.Lookup` (control-server goroutine) ↔ `Pool.Run` (main goroutine): RWMutex, already exercised by `internal/sessions` tests but now under load from real control traffic.
- `fakeSession.State` under `TestServer_Status` concurrent dial: existing mutex pattern preserved.

### Manual smoke (per AC)

Build a Phase 0 reference binary from `main` for byte-equivalence comparisons:

```bash
git worktree add /tmp/pyry-phase0 main
go -C /tmp/pyry-phase0 build -o /tmp/pyry-phase0/pyry ./cmd/pyry
go build -o ./pyry ./cmd/pyry
```

Then for each command, capture both outputs and `diff`:

```bash
# Foreground:
./pyry        # in terminal A
/tmp/pyry-phase0/pyry  # in terminal B (separate -pyry-name)

# Service mode (matching pyrybox deployment):
nohup ./pyry > /tmp/pyry.out 2> /tmp/pyry.err < /dev/null &
nohup /tmp/pyry-phase0/pyry -pyry-name pyry-phase0 > /tmp/pyry-phase0.out 2> /tmp/pyry-phase0.err < /dev/null &

# Compare:
diff <(./pyry status) <(/tmp/pyry-phase0/pyry -pyry-name pyry-phase0 status)
diff <(./pyry logs) <(/tmp/pyry-phase0/pyry -pyry-name pyry-phase0 logs)
# (timestamps will differ — strip them with sed before diffing)
```

Document the exact commands and their diffs in the PR description per the AC. Foreground mode also needs interactive verification: raw-mode keystroke pass-through and SIGWINCH propagation should feel identical to Phase 0 (resize the terminal mid-session, confirm claude redraws).

## Acceptance criteria mapping

Each AC bullet maps to an artefact in this spec:

- **`SessionResolver` interface satisfied by `*sessions.Pool`** → "Resolved open question" section. Spec deviates: satisfied via `poolResolver` adapter in cmd/pyry. AC text to be updated during PR review.
- **`StateProvider` and `AttachProvider` removed** → `internal/control/server.go` diff sketch above.
- **`NewServer` drops `attach` parameter; nil-sessions panic preserved** → `NewServer` signature change above.
- **Every verb resolves via `s.sessions.Lookup("")`** → `Server.handle` and `handleAttach` diff sketches above. Verbs that don't need session state (`Logs`, `Stop`) skip the resolver — process-global by design.
- **`cmd/pyry/main.go` constructs `sessions.Pool`; foreground/service detection unchanged** → "cmd/pyry/main.go rewiring" section.
- **`Request`/`Response` JSON unchanged; no `session_id`** → "Wire protocol invariants" section.
- **Byte-identical `pyry status`/`stop`/`logs`/`attach`** → "Wire protocol invariants" + `ErrAttachUnavailable` mapping in `handleAttach`.
- **Foreground mode bit-for-bit preserved** → `term.IsTerminal(os.Stdin.Fd())` keyed bridge selection unchanged; no supervisor-side changes.
- **Restart uses `--continue`, not `--session-id`** → `SessionConfig.ResumeLast` wiring; `--session-id` deferred per parent spec OQ #2.
- **Startup log preserved verbatim** → "cmd/pyry/main.go rewiring" log line preserved; no `session_id` field added.
- **`server_test.go` mechanical updates + new `TestServer_Status_ResolvesDefaultSession`** → "Testing strategy" section.
- **All existing tests stay green; `-race` clean; `vet` clean** → "Testing strategy" / "Race detector" sections.
- **Manual smoke per parent spec** → "Manual smoke" section with concrete commands.

## Out of scope (deferred)

- `Request.SessionID` field, `pyry sessions` subcommand, `pyry attach <id>`, `--session-id <uuid>`, `sessions.json` persistence, idle eviction, second session of any kind → Phase 1.1+.
- Errgroup fan-out inside `Pool.Run` → Phase 1.1's first multi-session test (parent spec, "Concurrency model").
- Per-session log lines, including bootstrap ID logging → Phase 1.1+ (parent spec OQ #3).

## Open questions (for the developer to resolve during build)

1. **Adapter location.** `poolResolver` is a 5-line adapter type. Recommendation: keep it inline in `cmd/pyry/main.go` next to `runSupervisor`. Promoting it to its own file or to `internal/control` would create a one-type file or push the adapter into the package the interface is *consumed* in (wrong direction). Inline keeps the boundary obvious: cmd/pyry knows both the pool and the control package, so the adapter belongs there.

2. **`recordingResolver` vs counting in `fakeResolver`.** `TestServer_Status_ResolvesDefaultSession` needs to assert `Lookup` was called with the empty id. Recommendation: wrap an existing `fakeResolver` with a `recordingResolver` that records the calls, rather than embedding a counter in `fakeResolver` itself. Most tests don't care about the calls; only the new test does. Keeping `fakeResolver` simple keeps the other 12 test rewrites trivial.

3. **`TestServer_AttachOnForegroundSession` rename vs keep `TestServer_AttachWithoutProvider`.** The behaviour under test changes shape: there's no longer a "no attach provider" wiring — there's a session whose bridge is nil. Recommendation: rename. The Phase 0 test name is misleading post-rewire; the new name documents what's actually being tested. The wire string assertion (the AC's byte-identical lock-in) carries over verbatim.

4. **Handle the stale `cfg.ClaudeBin` reference in the startup log.** Today's log line uses `cfg.ClaudeBin` (the supervisor.Config pre-defaults application). After the rewire, `cfg` no longer exists at `runSupervisor` scope — `claudeBin` is the flag pointer. Recommendation: log `*claudeBin` (the user-facing value, pre-defaults) for byte-identical output, since today's `cfg.ClaudeBin` is the same value pre-`supervisor.New`-defaults. Verify with the manual smoke diff.

## Size

**S.** ~35 net production lines:
- `internal/control/server.go`: −15 (StateProvider, AttachProvider, two struct fields, attach parameter, nil-state panic) + 25 (Session interface, SessionResolver interface, sessions field, NewServer signature, two handler resolver calls, ErrAttachUnavailable mapping) = +10.
- `cmd/pyry/main.go`: −8 (supervisor.New + bridge variable wiring + attachProvider guard) + 18 (sessions.New + poolResolver adapter + pool.Run) = +10. (Approximation; the supervisor import is dropped if no other reference remains, otherwise kept.)
- Net: ~+20 production. Test diff is mechanical and roughly 1:1 with the existing test file, plus the new ~30-line test.

The AC mapping is the bound on production change; tests scale with the existing surface (~470 lines of `server_test.go` + `attach_test.go`, all of which need mechanical NewServer-signature updates).
