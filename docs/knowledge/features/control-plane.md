# Control Plane

`internal/control` exposes the on-disk control surface of `pyry`: a Unix domain socket (`~/.pyry/<name>.sock`, mode `0600`) speaking line-delimited JSON. Each connection is one request, one response — except `VerbAttach`, which hands the connection off to the supervisor's I/O bridge for the lifetime of the attachment.

Verbs today: `status`, `stop`, `logs`, `attach`. The wire shape (`Request`/`Response` JSON) is held stable across phases — `AttachPayload.SessionID` (Phase 1.1e-C) was added additively with `omitempty` so empty-SessionID payloads marshal byte-identically to v0.5.x output, keeping v0.5.x clients round-tripping against a v0.7.x server during the rollover window.

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
    Activate(ctx context.Context) error  // 1.2c-A
}

// SessionResolver maps a SessionID to a Session and resolves loose-input
// selectors (full UUID / unique prefix / empty) to a canonical SessionID.
type SessionResolver interface {
    Lookup(id sessions.SessionID) (Session, error)
    // ResolveID maps a loose-input selector to a concrete SessionID.
    // Errors flow verbatim — handleAttach wraps them as "attach: <err>".
    ResolveID(arg string) (sessions.SessionID, error)
}
```

`*sessions.Session` satisfies `Session` structurally. `*sessions.Pool` does **not** satisfy `SessionResolver` directly because `Pool.Lookup` returns the concrete `*sessions.Session` rather than the `control.Session` interface — Go does not do covariant return types on interface satisfaction. A small `poolResolver` adapter in `cmd/pyry/main.go` bridges the two; both `Lookup` and `ResolveID` are 1-line passthroughs.

The empty-id-resolves-to-default convention is shared across both methods: `Lookup("")` and `ResolveID("")` both resolve to the bootstrap session (the latter via `Pool.ResolveID`'s empty-arg fast path). `handleAttach` (1.1e-C) takes loose input from `AttachPayload.SessionID` and routes it through `ResolveID` first, then `Lookup` — see [Attach: ResolveID-then-Lookup](#attach-resolveid-then-lookup-11e-c) below. Other verbs that don't yet take a selector (`status`, `logs`, `stop`) continue to call `Lookup("")` directly.

### Why a single resolver instead of `StateProvider` + `AttachProvider`

Phase 0 wired the supervisor into control via two narrow interfaces (`StateProvider` for `VerbStatus`, `AttachProvider` for `VerbAttach`). Two providers worked when there was exactly one supervisor; once a session has identity, every verb needs the same lookup step before it does its work. Collapsing to one resolver removes the dual-provider plumbing and gives each handler the same shape:

```go
sess, err := s.sessions.Lookup("")
if err != nil { /* encode error */; return }
// use sess.State() or sess.Attach(...)
```

`VerbLogs` and `VerbStop` are intentionally process-global today (logs come from the ring buffer; stop calls the supervisor-context cancel). They do **not** call the resolver. Phase 1.1 may revisit `VerbStop` if per-session stop becomes a verb.

## Attach: ResolveID-then-Lookup (1.1e-C)

`handleAttach` resolves the client's session selector through `Pool.ResolveID` before any bridge work, then re-fetches the session with `Pool.Lookup`. Two sequential calls, two sequential `Pool.mu` RLocks:

```go
id, err := s.sessions.ResolveID(sessionID) // sessionID = req.Attach.SessionID, or "" if Attach is nil
if err != nil {
    _ = enc.Encode(Response{Error: fmt.Sprintf("attach: %v", err)})
    return false
}
sess, err := s.sessions.Lookup(id)
if err != nil {
    _ = enc.Encode(Response{Error: fmt.Sprintf("attach: %v", err)})
    return false
}
// ... unchanged: clear deadline, Activate, Attach, hand off conn.
```

Why two calls instead of one `ResolveSession` API:

- **`Pool.ResolveID` returns `SessionID`, not `*Session` (decision from #66).** Returning `*Session` would tempt callers to skip the second lookup, but the second `Lookup` is the lock-clean way to guard against a session being removed between resolve and use. Window is microseconds (one RLock release + one RLock acquire); race outcome is `ErrSessionNotFound` from `Lookup`, which encodes to the same `"attach: sessions: session not found"` wire string the resolver itself produces. Operator-visible diagnostic is identical either way.
- **Each call takes its own `Pool.mu` RLock — no new locking required.** Concurrent attaches against different sessions remain fully parallel; the dispatcher spawns one goroutine per accept, and `Pool` is the only shared state.

A `nil` `req.Attach` (no payload at all) is treated identically to an empty `SessionID` — both pass `""` into `ResolveID`, which returns the bootstrap id. Phase 0 / v0.5.x clients that omit the payload entirely keep working.

Resolution errors encode as `"attach: <err>"` verbatim through `fmt.Sprintf("%v", err)`:

| Failure | Wire `Response.Error` |
|---|---|
| `ErrSessionNotFound` (no match, or resolve-then-lookup race) | `"attach: sessions: session not found"` |
| `ErrAmbiguousSessionID` (≥2 matches) | `"attach: sessions: ambiguous session id:\n<uuid> (<label>)\n<uuid> (<label>)"` |

The bridge state is **untouched** on the error path — `ResolveID` and `Lookup` both return before `conn.SetDeadline(time.Time{})`, before `Activate`, before `Attach`. Tests assert this via the fake session's attach-call counter, not just by string-matching the response. The `errors.Is(err, sessions.ErrAmbiguousSessionID)` discriminator continues to work server-side; only the message reaches the wire.

The 1.1e-C slice was wire + server only. The CLI surface (`pyry attach <id>` positional) is wired in 1.1e-D — see [§ Attach: CLI Surface](#attach-cli-surface-11e-d) below.

## Attach: CLI Surface (1.1e-D)

The Phase 1.1e end-to-end multi-session attach surface lands in `cmd/pyry/main.go` and `internal/control/attach_client.go`. Three changes, all dumb passthrough — the CLI does **not** parse, validate, or interpret the session selector. It is a string passed straight from `os.Args` to `AttachPayload.SessionID`. All resolution happens server-side in `Pool.ResolveID`.

### `parseClientFlags` returns positionals

`parseClientFlags` (the shared helper for `status` / `stop` / `logs` / `attach`) now surfaces `fs.Args()`:

```go
func parseClientFlags(name string, args []string) (socketPath string, rest []string, err error)
```

`runStatus` / `runLogs` / `runStop` bind `rest` to `_` — same silent-ignore-of-stray-positionals behaviour as before. Only `runAttach` consumes it. Out-of-scope per AC; opportunistic "reject extra args" for sibling verbs is deferred until they take their own positionals.

### `runAttach` — optional positional after flags

```go
func runAttach(args []string) error {
    socketPath, rest, err := parseClientFlags("pyry attach", args)
    if err != nil { return err }
    sessionID, err := attachSelectorFromArgs(rest)
    if err != nil {
        fmt.Fprintln(os.Stderr, "pyry attach: too many arguments")
        fmt.Fprintln(os.Stderr, "usage: pyry attach [flags] [<id>]")
        os.Exit(2)
    }
    // ...unchanged: cols/rows from term.GetSize, control.Attach, ...
}
```

`attachSelectorFromArgs(rest []string) (string, error)` is the unit-testable seam. Empty rest → `""` (server resolves to bootstrap). One arg → that arg, verbatim. More than one → `errTooManyAttachArgs`.

Two design points:

- **`os.Exit(2)` for "too many arguments", not a returned error.** The convention in `main.go` is that `runFoo` returning an error becomes "exit 1 with the message printed by `main`" — semantically a runtime failure. Usage errors (the user typed the command wrong) are exit 2 by POSIX convention, visually distinct, and consistent with the existing `os.Exit(2)` at the top of `main` for argument-shape errors. The error/exit split is checkable: error return ⇒ runtime, `os.Exit(2)` ⇒ user typed wrong.
- **No client-side trimming or validation of `<id>`.** Whitespace-only, malformed UUID, mixed case — all pass through verbatim. `pyry attach " "` reaches the server's `Pool.ResolveID` which responds `ErrSessionNotFound`. The CLI's job is transport, not lint. UUID parsing and prefix logic in `cmd/pyry` or `internal/control/attach_client.go` is a regression — reviewers should `grep -rn 'HasPrefix\|uuid.Parse'` and reject any match.

### `control.Attach` — extended signature

```go
func Attach(ctx context.Context, socketPath string, cols, rows int, sessionID string) error
```

Extended in place rather than adding a sibling — there is exactly one external caller (`cmd/pyry/main.go runAttach`) and the function already builds the `AttachPayload`. The new arg flows into `AttachPayload.SessionID`; empty marshals to `{"cols":80,"rows":24}` under the `omitempty` tag (1.1e-C's load-bearing decision), preserving v0.5.x byte-identical wire output. Pinned by the `TestAttach_WireBackCompat_EmptySessionID` regression test from #101.

### Help text

```
  pyry attach [flags] [<id>]                     attach local terminal to daemon
                                                  (Ctrl-B d to detach; <id>
                                                  selects a session — full
                                                  UUID or unique prefix; omit
                                                  for the bootstrap session)
```

Terse, matches the surrounding block.

### Error propagation (no new code)

All four error classes already produce the correct behaviour with the changes above:

| Class | Path |
|---|---|
| Daemon not running | `dial` fails → `control.Attach` returns error → `runAttach` wraps `attach: %w` → exit 1 |
| Unknown id / ambiguous prefix | `handleAttach` (1.1e-C) encodes `Response.Error="attach: …"` → client returns `errors.New(resp.Error)` → wrapped `attach: %w` → exit 1 |
| `ErrBridgeBusy` | `Session.Attach` returns it → server encodes as `Response.Error` → same client path |
| Extra positionals | usage to stderr, `os.Exit(2)` |

The bridge-never-opened invariant is enforced server-side (1.1e-C). Nothing in the CLI can violate it: client sends one Request, reads one Response; if `resp.Error` is set, the client returns before raw mode and `io.Copy` is never started.

The doubled `attach: attach: …` wrapping (server prefixes once, client wraps again) is a known minor wart in the Phase 0 surface that the AC explicitly preserves — "messages match the existing surface, no rewording." Don't fix here.

### Tests

Two test files. Stdlib `testing` only.

- **`cmd/pyry/args_test.go` — `TestParseClientFlags_ReturnsRest`**: pins the seam — empty args, recognised flags only, single positional after flags, two positionals (passed through as a 2-len slice; "too many" is `runAttach`'s decision, not the parser's).
- **`internal/control/attach_test.go`** — extended with `TestAttach_PassesSessionID_OnWire` cases (no-arg → `""`, full-UUID, unique-prefix all reach the server with the expected `AttachPayload.SessionID`).

Resolver and bridge error paths are **not** re-tested through the CLI shell. `internal/control/attach_resolve_test.go` (1.1e-C) covers them exhaustively against the wire — the wire is the contract. Re-testing through the CLI wrapper would duplicate ground for no incremental confidence.

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

## Attach: Activate-before-bind (1.2c-A)

`handleAttach` calls `Session.Activate(ctx)` before `Session.Attach(conn, conn)` so an evicted session is woken before the bridge is bound:

```go
activateCtx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
defer cancel()
if err := sess.Activate(activateCtx); err != nil {
    _ = enc.Encode(Response{Error: fmt.Sprintf("attach: activate: %v", err)})
    return false
}
done, err := sess.Attach(conn, conn)
```

The 30s window caps the documented 2-15s respawn latency with safety margin. A busted respawn surfaces as a clean `attach: activate: <err>` rather than a hung attach. `bridge.Attach` on an evicted session would block on the pipe forever (no claude to drain it) — the Activate-first contract is load-bearing.

`handleStatus` does **not** activate. Status on an evicted session reports the supervisor's `PhaseStopped` (faithful — the supervisor really isn't running) and avoids spurious wakeups from a poll. See [idle-eviction.md](idle-eviction.md).

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

`server_test.go`, `attach_test.go`, `attach_resolve_test.go`, `logs_test.go` exercise the full surface with `fakeResolver` + `fakeSession` test doubles satisfying `SessionResolver` + `Session`. `recordingResolver` records both `Lookup` and `ResolveID` arguments — pinning the resolve-then-lookup ordering visible at review time.

`attach_resolve_test.go` covers the 1.1e-C surface: byte-identical wire output for empty-`SessionID` payloads against a v0.5.x baseline, full-UUID resolution, unique-prefix resolution, ambiguous-prefix error before bridge open, and unknown-id error before bridge open. The "before bridge open" assertion uses the fake session's attach-call counter, not just response-string matching.

Tests that need a real bridge (`TestServer_StopWhileAttached`, `TestServer_BridgeAttach`, `TestServer_ConcurrentAttachRace`) wrap a real `*supervisor.Bridge` in a `fakeSession` whose `attachFn` delegates to `bridge.Attach`.

## References

- [`sessions-package.md`](sessions-package.md) — the package providing `*Session`, `*Pool`, `SessionID`.
- [ADR 003](../decisions/003-session-addressable-runtime.md) — why the resolver seam exists.
- Spec: [`docs/specs/architecture/29-wire-sessions-pool-consumers.md`](../../specs/architecture/29-wire-sessions-pool-consumers.md).
