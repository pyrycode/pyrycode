# Control Plane

`internal/control` exposes the on-disk control surface of `pyry`: a Unix domain socket (`~/.pyry/<name>.sock`, mode `0600`) speaking line-delimited JSON. Each connection is one request, one response — except `VerbAttach`, which hands the connection off to the supervisor's I/O bridge for the lifetime of the attachment.

Verbs today: `status`, `stop`, `logs`, `attach`. The wire shape (`Request`/`Response` JSON) is held stable across phases — Phase 1.1 will add `Request.SessionID` additively without changing existing fields.

## Server Construction

```go
func NewServer(
    socketPath string,
    sessions   SessionResolver,
    logs       LogProvider,
    shutdown   func(),
    log        *slog.Logger,
) *Server
```

`sessions` is the only required dependency that nil-panics at construction. Programmer error surfaces immediately, not on the first request from a future shell.

`logs` and `shutdown` are optional. When nil, the corresponding verb returns an error response — used in tests that care about isolated verbs.

## Resolver Seam

The control plane consumes session state through one interface pair, both defined in `internal/control` (the consumer side):

```go
// Session is the per-session view the control plane needs.
type Session interface {
    State() supervisor.State
    Attach(in io.Reader, out io.Writer) (done <-chan struct{}, err error)
}

// SessionResolver maps a SessionID to a Session. Empty id resolves to the
// default (bootstrap) entry.
type SessionResolver interface {
    Lookup(id sessions.SessionID) (Session, error)
}
```

`*sessions.Session` satisfies `Session` structurally. `*sessions.Pool` does **not** satisfy `SessionResolver` directly because `Pool.Lookup` returns the concrete `*sessions.Session` rather than the `control.Session` interface — Go does not do covariant return types on interface satisfaction. A 5-line `poolResolver` adapter in `cmd/pyry/main.go` bridges the two.

The empty-id-resolves-to-default convention is the seam Phase 1.1 swaps. Today every verb that needs session state calls `s.sessions.Lookup("")`. When `Request.SessionID` lands, those calls become `s.sessions.Lookup(req.SessionID)` — no handler-side branching, no special "if id == \"\"" cases scattered across verbs.

### Why a single resolver instead of `StateProvider` + `AttachProvider`

Phase 0 wired the supervisor into control via two narrow interfaces (`StateProvider` for `VerbStatus`, `AttachProvider` for `VerbAttach`). Two providers worked when there was exactly one supervisor; once a session has identity, every verb needs the same lookup step before it does its work. Collapsing to one resolver removes the dual-provider plumbing and gives each handler the same shape:

```go
sess, err := s.sessions.Lookup("")
if err != nil { /* encode error */; return }
// use sess.State() or sess.Attach(...)
```

`VerbLogs` and `VerbStop` are intentionally process-global today (logs come from the ring buffer; stop calls the supervisor-context cancel). They do **not** call the resolver. Phase 1.1 may revisit `VerbStop` if per-session stop becomes a verb.

## Attach: Foreground-mode Wire String

A foreground-mode pyry has no `*supervisor.Bridge` — its supervised child is bound directly to the local terminal. Calling `pyry attach` against such a daemon must return Phase 0's exact error string:

```
attach: no attach provider configured (daemon may be in foreground mode)
```

Under the hood this is now `sessions.ErrAttachUnavailable` flowing out of `Session.Attach`. `handleAttach` maps it explicitly:

```go
if errors.Is(err, sessions.ErrAttachUnavailable) {
    _ = enc.Encode(Response{Error: "attach: no attach provider configured (daemon may be in foreground mode)"})
    return false
}
```

A bare `fmt.Sprintf("attach: %v", err)` would surface `attach: sessions: attach unavailable (no bridge)` — observable client drift. The mapping is **load-bearing** for byte-identical output.

`supervisor.ErrBridgeBusy` (second client tries to attach while another is connected) flows through the unchanged `fmt.Sprintf` path, preserving Phase 0's wire surface for that case.

## Process-Global vs Per-Session

| Concern | Scope today | Source |
|---|---|---|
| `status` payload | per-session (one supervisor) | `sess.State()` |
| `attach` stream | per-session (one bridge) | `sess.Attach(...)` |
| `logs` ring buffer | process-global | `LogProvider`, written by all loggers |
| `stop` shutdown | process-global | `shutdown` cancel func |

Phase 1.1's `pyry sessions list` and `pyry attach <id>` extend the per-session column. Logs and stop stay process-global until a concrete need pushes them otherwise.

## Lifecycle

Two top-level goroutines, unchanged from Phase 0:

1. **Main goroutine** — calls `pool.Run(ctx)`, blocks until ctx cancellation.
2. **Control goroutine** — `go ctrl.Serve(ctx)`, accepts client connections, dispatches verbs.

Shutdown is unchanged: `SIGINT`/`SIGTERM` → `signal.NotifyContext` cancels the context → `pool.Run` returns `context.Canceled` → `ctrl.Close()` removes the socket file → in-flight handlers drain via `streamingWG`.

## Testing

`server_test.go`, `attach_test.go`, `logs_test.go` exercise the full surface with `fakeResolver` + `fakeSession` test doubles satisfying `SessionResolver` + `Session`. `recordingResolver` (used in `TestServer_Status_ResolvesDefaultSession`) wraps a `fakeResolver` to record `Lookup` arguments — pinning the empty-id-resolves-to-default seam so Phase 1.1's swap to `req.SessionID` is visible at review time.

Tests that need a real bridge (`TestServer_StopWhileAttached`, `TestServer_BridgeAttach`, `TestServer_ConcurrentAttachRace`) wrap a real `*supervisor.Bridge` in a `fakeSession` whose `attachFn` delegates to `bridge.Attach`.

## References

- [`sessions-package.md`](sessions-package.md) — the package providing `*Session`, `*Pool`, `SessionID`.
- [ADR 003](../decisions/003-session-addressable-runtime.md) — why the resolver seam exists.
- Spec: [`docs/specs/architecture/29-wire-sessions-pool-consumers.md`](../../specs/architecture/29-wire-sessions-pool-consumers.md).
