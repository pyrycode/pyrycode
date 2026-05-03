# Control Plane

`internal/control` exposes the on-disk control surface of `pyry`: a Unix domain socket (`~/.pyry/<name>.sock`, mode `0600`) speaking line-delimited JSON. Each connection is one request, one response — except `VerbAttach`, which hands the connection off to the supervisor's I/O bridge for the lifetime of the attachment.

Verbs today: `status`, `stop`, `logs`, `attach`, `resize`, `sessions.new`, `sessions.rm`, `sessions.rename`. The wire shape (`Request`/`Response` JSON) is held stable across phases — `AttachPayload.SessionID` (Phase 1.1e-C) was added additively with `omitempty` so empty-SessionID payloads marshal byte-identically to v0.5.x output, keeping v0.5.x clients round-tripping against a v0.7.x server during the rollover window. `VerbResize` (#137) was added in the same additive manner — a brand-new verb on a fresh connection, no impact on the other verbs' wire output. `VerbSessionsNew` (#75) extends the pattern: a new `Request.Sessions *SessionsPayload` field with `omitempty` keeps existing-verb wire output byte-identical (pinned by `TestProtocol_SessionsRoundTripBackCompat`). `VerbSessionsRm` (#98) extends `SessionsPayload` with `ID`/`JSONLPolicy` (both `omitempty`) and adds a `Response.ErrorCode` field (also `omitempty`) for typed-sentinel propagation — same back-compat guard, byte-identical existing-verb output. `VerbSessionsRename` (#90) extends `SessionsPayload` with one further `omitempty` field (`NewLabel`) and reuses the `Response.ErrorCode` envelope verbatim — no new wire constants.

## Server Construction

```go
func NewServer(
    socketPath string,
    sessions   SessionResolver,
    logs       LogProvider,
    shutdown   func(),
    log        *slog.Logger,
    sessioner  Sessioner,
) *Server
```

`sessions` is the only required dependency that nil-panics at construction. Programmer error surfaces immediately, not on the first request from a future shell.

`logs`, `shutdown`, and `sessioner` are optional. When nil, the corresponding verb returns an error response — used in tests that care about isolated verbs. `sessioner` is wired in production to `*sessions.Pool` in `cmd/pyry/main.go` (#116); `*sessions.Pool` satisfies `Sessioner` directly because `Pool.Create` returns `sessions.SessionID`, matching the interface signature with no adapter (contrast with `poolResolver` for the read-side `Lookup`). Pre-#116 the call site passed `nil` and `VerbSessionsNew` returned `"sessions.new: no sessioner configured"`. See `docs/specs/architecture/75-control-sessions-new.md` for the seam design.

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

## Attach: Handshake Geometry (#136)

Between `Activate` and `Attach`, `handleAttach` applies the client's terminal
size to the supervised PTY through a typed seam:

```go
if payload != nil && payload.Cols > 0 && payload.Rows > 0 {
    rows := clampUint16(payload.Rows)
    cols := clampUint16(payload.Cols)
    if err := sess.Resize(rows, cols); err != nil &&
        !errors.Is(err, sessions.ErrAttachUnavailable) {
        s.log.Warn("control: attach geometry resize failed", ...)
    }
}
```

Three boundary rules:

- **Zero is the "don't touch" sentinel.** Either `Cols` or `Rows` being zero
  (or `payload` being nil) issues no resize. Matches the `omitempty` tags on
  `AttachPayload`.
- **`int → uint16` clamps silently.** `clampUint16` returns `math.MaxUint16`
  for out-of-range positives. A real terminal will never report dimensions
  that large; a client that does is buggy or hostile. No log on clamp.
- **Argument order swap at the boundary.** The wire is cols-then-rows
  (`AttachPayload`); `Session.Resize` / `Bridge.Resize` are rows-then-cols
  (matching `pty.Winsize`). `handleAttach` is the only site that deals with
  both orders.

The `Session` interface gains `Resize(rows, cols uint16) error`:

```go
type Session interface {
    State() supervisor.State
    Attach(in io.Reader, out io.Writer) (done <-chan struct{}, err error)
    Activate(ctx context.Context) error
    Resize(rows, cols uint16) error // #136
}
```

`*sessions.Session.Resize` is a one-line delegator to `Bridge.Resize` (or
returns `ErrAttachUnavailable` in foreground mode — swallowed by
`handleAttach` since foreground mode has its own SIGWINCH watcher in
`winsize.go`). No lifecycle locking; does not touch `lcMu`, does not bump
`lastActiveAt`, does not interact with the active↔evicted state machine.

`Bridge.Resize` is the supervisor-side seam — see [ADR 008](../decisions/008-bridge-resize-seam.md)
for why it lives on `*Bridge` and not on `*Supervisor`. The bridge holds a
leaf-only `ptyMu` mutex over the per-iteration `*os.File`; `runOnce` calls
`SetPTY(ptmx)` after `pty.Start` and `SetPTY(nil)` **before** `EndIteration`
so an in-flight `Resize` that races iteration teardown sees nil rather than
a closed fd.

**Resize errors never fail the attach.** A `pty.Setsize` error (e.g. `EBADF`
on a closed fd in the narrow race window) is logged at Warn and the attach
proceeds. Geometry is best-effort; a wrong window size is recoverable on the
user's next keystroke.

The handshake-geometry block is the first consumer of the seam; the
live-resize wire message + server applier are #137 (see [§ Resize: Live
Wire Message and Applier](#resize-live-wire-message-and-applier-137) below).
The client-side SIGWINCH handler that emits the live message is #133.

## Resize: Live Wire Message and Applier (#137)

`VerbResize` carries a live window-size update for an already-attached
session on a **separate, one-shot control connection** — independent of
the (long-lived) attach conn. Same lifecycle as `VerbStatus` / `VerbStop` /
`VerbLogs`: client dials, sends one JSON request, reads one JSON ack,
closes. See [ADR 009](../decisions/009-resize-wire-shape.md) for why
side-channel beat framed-escape-in-byte-stream.

### Wire shape

```go
const VerbResize Verb = "resize"

type Request struct {
    Verb   Verb           `json:"verb"`
    Attach *AttachPayload `json:"attach,omitempty"`
    Resize *ResizePayload `json:"resize,omitempty"` // populated for VerbResize
}

type ResizePayload struct {
    SessionID string `json:"sessionID,omitempty"`
    Cols      int    `json:"cols,omitempty"`
    Rows      int    `json:"rows,omitempty"`
}
```

`ResizePayload` mirrors `AttachPayload`'s field shape: empty `SessionID`
selects bootstrap; full UUID or unique prefix selects a specific session;
`Cols`/`Rows` are wire ints, narrowed + swapped at the seam boundary.
Either dimension being zero is the "unknown / don't touch" sentinel — same
rule as the handshake path.

### Server applier

`handleResize` mirrors the resolve-then-lookup-then-seam shape of
`handleAttach`'s handshake-geometry block (cf. [§ Attach: Handshake
Geometry](#attach-handshake-geometry-136) above), minus the
connection-handoff machinery:

```go
func (s *Server) handleResize(enc *json.Encoder, payload *ResizePayload) {
    if payload == nil {
        _ = enc.Encode(Response{Error: "resize: missing payload"})
        return
    }
    id, err := s.sessions.ResolveID(payload.SessionID)
    if err != nil { /* encode "resize: <err>"; return */ }
    sess, err := s.sessions.Lookup(id)
    if err != nil { /* encode "resize: <err>"; return */ }
    if payload.Cols > 0 && payload.Rows > 0 {
        rows := clampUint16(payload.Rows)
        cols := clampUint16(payload.Cols)
        if err := sess.Resize(rows, cols); err != nil &&
            !errors.Is(err, sessions.ErrAttachUnavailable) {
            s.log.Warn("control: resize failed", ...)
        }
    }
    _ = enc.Encode(Response{OK: true})
}
```

### Error posture: OK ack even when the seam errors

The same posture as the handshake-geometry block. Geometry is best-effort.
The client gets `OK: true` whenever the request itself was processable;
seam failures (transient `EBADF` on a closed fd, `ErrAttachUnavailable`
from a foreground session) are logged at Warn and swallowed. Two reasons:

- The client has nothing useful to do with a "resize failed" response.
  The next SIGWINCH will re-emit; the user's terminal already shows the
  new size locally.
- Symmetry with handshake. Inventing a new error contract here would mean
  client code has to handle resize failures differently from handshake
  failures, with no operational benefit.

The only error responses are pre-seam routing failures, which signal a
malformed or routing-broken request:

| Failure | Wire `Response.Error` |
|---|---|
| `payload == nil` | `"resize: missing payload"` |
| `ResolveID` returns an error | `"resize: <err>"` (e.g. `"resize: sessions: ambiguous session id: ..."`) |
| `Lookup` returns an error | `"resize: <err>"` (e.g. `"resize: sessions: session not found"`) |

Decode failures on the request body land in `handle`'s existing
decode-error branch and never reach `handleResize`.

### Why "session must currently be attached" is NOT enforced

A resize can arrive between `pyry attach` and the actual bridge handshake
completing, or after a transient detach during a SIGWINCH burst. Requiring
`sess.Attached()` would introduce a race window with no upside —
`Bridge.Resize` already silently no-ops when `ptmx` is nil. Letting the
resize through unconditionally is simpler and matches `pty.Setsize`'s own
semantics (the kernel doesn't care whether anyone is currently reading the
master).

### Decoding errors cannot tear down the attach session

This property (an explicit AC#2 of #137) is **structural**, not coded.
The resize conn and the attach conn are independent `net.Conn`s; a
malformed JSON body, a bogus session id, or a seam error all live entirely
on the resize conn and never propagate to the attach conn. No
error-handling code is needed to honour the contract — the topology
honours it. `handleResize`'s godoc records this for the next reader.

### Concurrency

No new mutexes, channels, or goroutines. Three surfaces interact, all
already in place:

- Per-connection handler goroutines from `Server.Serve`'s accept loop. A
  resize request gets its own conn → its own handler. Handlers complete
  in ~1ms (one resolver lookup + one `pty.Setsize`).
- Cross-conn ordering: an attach conn (handler #1, blocked in the bridge
  input pump) and one or more resize conns (handlers #2..#N) run
  concurrently and independently.
- `Bridge.ptyMu` serialisation: two concurrent resize handlers contend
  briefly on the leaf-only mutex; last-write-wins is the only meaningful
  semantic for window-size, and the kernel's `ioctl(TIOCSWINSZ)` honours
  exactly that.

Race scenarios audited in [#137's spec](../../specs/architecture/137-resize-wire-message.md);
representative cases include resize-during-child-restart (silent nil from
`Bridge.Resize`), simultaneous resizes (both ioctls succeed; second wins),
resize for an evicted session (silent nil; next handshake reapplies), and
resize for a foreground session (`ErrAttachUnavailable` swallowed —
foreground has its own SIGWINCH watcher in `winsize.go`).

### Client helper

```go
func SendResize(ctx context.Context, socketPath, sessionID string, cols, rows int) error
```

Sibling of `Status` / `Stop` / `Logs`, reusing the same `request()` helper
(one-shot dial → encode → decode → close). A successful return means the
server received and dispatched the request — the seam's own success is
best-effort and not visible to the client. Callers (e.g. a SIGWINCH
handler) should not retry on transient failure; the next SIGWINCH will
re-emit.

`SendResize` is defined now and unused until #133's SIGWINCH handler in
`pyry attach` lands. Defining it now keeps that ticket trivially small.

### Caveat status

| File:lines | Status after #137 + #133 |
|---|---|
| `internal/control/protocol.go` | After #137: rewritten to point at `VerbResize`/`ResizePayload`. After #133: parenthetical updated to identify `startWinsizeWatcher` as the producer (no longer "deferred to #133"). |
| `internal/control/attach_client.go:25-27` | After #133: rewritten — drops the Phase-0 "detach and reattach to update" sentence; replaced with one identifying the SIGWINCH watcher as the live-resize producer and `Bridge.Resize` as the seam that applies it. |

### Tests

`internal/control/resize_test.go` (~290 LOC, single new file) covers the
end-to-end server path against the existing `fakeSession` + `fakeResolver`
test doubles (which already record `resizeCalls` from #136 — no test infra
changes). Coverage: applies-to-seam, zero-dim no-op (table-driven over
`{0,0}, {0,40}, {120,0}` plus `nil` payload), unknown-session error,
seam-error-returns-OK, foreground-session-silent (`ErrAttachUnavailable`
swallowed), oversize-dim clamp at the boundary, plus two
`SendResize` round-trip tests (handshake server stand-up vs. error wire
string) using the same hand-rolled `net.Listen` shape as
`TestAttach_ClientSendsSessionID`. The structural "decoding errors do not
tear down the attach session" property is intentionally **not** unit-tested
through a dedicated integration test; it falls out of conn independence
and is documented in `handleResize`'s godoc rather than asserted.

## Resize: Live SIGWINCH Watcher (#133)

The client-side producer for `VerbResize`. Lives next to `Attach` in
`attach_client.go` (no new file, no exported type) and runs only for the
lifetime of an attach. Closes the live-resize loop end-to-end:

```
terminal resize → SIGWINCH on the client → startWinsizeWatcher
  → SendResize on a fresh control conn → handleResize → Session.Resize
  → Bridge.Resize → pty.Setsize → child redraws
```

### Wiring inside `Attach`

`Attach` installs the watcher *after* the handshake ack succeeds and tears
it down via `defer` before `Attach` returns:

```go
if !resp.OK {
    return errors.New("control: attach ack missing")
}

stopWinsize := startWinsizeWatcher(ctx, readTerminalSize, func(ctx context.Context, cols, rows int) error {
    return SendResize(ctx, socketPath, sessionID, cols, rows)
})
defer stopWinsize()
```

Two ordering rules:

- **Install after the handshake.** A resize emitted before the server has
  bound a session would race; deferring installation to after the ack
  removes that window structurally.
- **`defer stopWinsize()` runs before `defer conn.Close()`.** Both defers
  exist; LIFO order tears down the SIGWINCH watcher first, then the attach
  conn. This avoids a SIGWINCH firing into a half-closed conn — and since
  resize uses its own conn anyway, the order is also operationally
  irrelevant; documenting it just pins intent.

### `startWinsizeWatcher` — small helper, dependency-injected I/O

```go
type terminalSizeReader func() (cols, rows int, ok bool)

func startWinsizeWatcher(
    ctx context.Context,
    read terminalSizeReader,
    send func(ctx context.Context, cols, rows int) error,
) (stop func())
```

Pure orchestration: signal in, callback out. The `read` and `send`
parameters keep `os` and the network out of the watcher's body; production
wires them to `pty.GetsizeFull(os.Stdin)` (via `readTerminalSize`) and
`SendResize`. Tests inject stubs.

The watcher does **not** prime an initial size at startup — initial
geometry flows through the handshake `AttachPayload` (AC#2 of #133). The
window between handshake and the first SIGWINCH is microseconds; priming
would push a duplicate resize in the common case where geometry didn't
change.

### Synchronous teardown is the load-bearing guarantee

`stop` does three things in order:

1. `signal.Stop(sigCh)` — unsubscribe from SIGWINCH delivery.
2. `close(done)` — wake the watcher goroutine's `select`.
3. `<-gone` — block until the goroutine has actually exited.

Step 3 makes teardown *synchronous*, not *eventual*. No goroutine or
signal subscription outlives the `defer stopWinsize()` call site. This is
what makes "no signal handler or goroutine leaks across attach/detach
cycles" (AC#3) true structurally rather than as a guarantee that needs a
retry budget.

Importantly, if `SendResize` is in flight when `stop` fires, the goroutine
hasn't reached its `select` yet — `<-gone` waits out the in-flight call.
Detach cannot race against a half-completed resize.

### Production reader uses `os.Stdin` directly

```go
func readTerminalSize() (cols, rows int, ok bool) {
    if !term.IsTerminal(int(os.Stdin.Fd())) {
        return 0, 0, false
    }
    size, err := pty.GetsizeFull(os.Stdin)
    if err != nil {
        return 0, 0, false
    }
    return int(size.Cols), int(size.Rows), true
}
```

`os.Stdin` is used directly rather than wrapping `os.Stdin.Fd()` in a
fresh `*os.File` — the same convention `internal/supervisor/winsize.go`
follows. Wrapping a raw fd in a fresh `*os.File` makes the wrapper
finalizable; if it's GC'd while the underlying fd is in use, the
finalizer-driven `close(2)` plus a subsequent `open(2)` of any fd can race
into a reused fd, with `pty.Setsize` then targeting the wrong file.
Using `os.Stdin` directly inherits the runtime-managed lifecycle of the
process's actual stdin.

### Best-effort posture, no logging

`SendResize` errors are silently dropped: transient daemon hiccup, ctx
cancelled mid-flight, server-side `Error` string from a stale id —
all fall through the `_ = send(...)` line. The next SIGWINCH retries.
This matches `SendResize`'s own godoc and the server's posture
(`handleResize` returns OK even when the seam errors).

The watcher logs nothing. The package is library-level; `Attach`'s caller
(`cmd/pyry/main.go`) sets the logging conventions, and SendResize errors
are uninteresting in practice (either "daemon went away" — already
surfaced by the attach conn dropping — or "transient socket hiccup" — the
next SIGWINCH corrects it).

### Burst coalescing

A user dragging the window corner produces a flurry of SIGWINCH. The
buffered-cap-1 `sigCh` collapses any signals that arrive while the
goroutine is mid-`SendResize` to *one* queued event. After the in-flight
`SendResize` returns, the goroutine reads the queued one and emits a
fresh resize with the *current* size (re-read at signal time, not stale).
The server serialises under `Bridge.ptyMu`; last-write-wins on the
kernel `ioctl(TIOCSWINSZ)`. Same coalescing shape as the daemon-side
`supervisor/winsize.go`.

### Concurrency and the attach conn

The watcher does **not** touch the attach `conn`. Each SIGWINCH is its
own short-lived dial via `SendResize`. So three goroutines exist inside
`Attach` after handshake — output copier, input pump (the calling
goroutine itself, in `copyWithEscape`), and the watcher — and they share
no mutable state. A malformed resize, daemon-side seam error, or
transient dial failure lives entirely on the resize conn and never
disturbs the byte stream on the attach conn.

### Tests

`internal/control/attach_winsize_test.go` (build tag `//go:build unix`,
since `syscall.SIGWINCH` is unix-only and the project excludes Windows):

- `TestStartWinsizeWatcher_SIGWINCHEmitsResize` — hand-rolled `net.Listen`
  server (mirrors `TestSendResize_RoundTrip`), stub `read` returns
  `(120, 40, true)`, `syscall.Kill(syscall.Getpid(), syscall.SIGWINCH)`
  delivers the signal, server records the decoded `Request{Verb:
  VerbResize, Resize: &ResizePayload{Cols:120, Rows:40, SessionID:"abc"}}`.
- `TestStartWinsizeWatcher_StopIsSynchronousAndLeakFree` — 50 iterations
  of `startWinsizeWatcher` + `Kill SIGWINCH` + `stop` + `cancel`; samples
  `runtime.NumGoroutine()` before and after with a small slack.
  Per-iteration leakage would dwarf any test-noise budget.

Both tests run **without** `t.Parallel()` because `syscall.Kill(SIGWINCH)`
is process-wide; any concurrent test subscribing to SIGWINCH would race.
Audit confirms no other test in `internal/control` does so today; if a
future test adds a peer subscriber, it must coordinate.

The wire-shape SIGWINCH→`SendResize` transformation is the primary AC of
#133; structural integration with `pty.GetsizeFull` and `os.Stdin` is
covered by the existing `supervisor/winsize.go` patterns and is not
re-exercised here. End-to-end coverage (real PTY, real attach, real
resize) lives in #126.

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

## Sessions: removal seam (1.1d-B1)

The second `sessions.*` verb is `sessions.rm`. The control server consumes session-removal commands through the `Remover` interface in `internal/control`, embedded into `Sessioner` so `NewServer`'s signature stays stable as the namespace grows:

```go
type Remover interface {
    Remove(ctx context.Context, id sessions.SessionID, opts sessions.RemoveOptions) error
}

type Sessioner interface {
    Create(ctx context.Context, label string) (sessions.SessionID, error)
    Remover
}
```

`*sessions.Pool` satisfies `Remover` directly via `Pool.Remove` (#94/#95). Aggregating per-verb sub-interfaces into `Sessioner` (rather than threading a new constructor parameter per seam) keeps the 27-call-site `NewServer` signature unchanged — a deliberate response to the #75 cascade. Phase 1.1b/c/e (`list`, `rename`, `attach` orchestration) will continue this pattern.

### Wire shape

`SessionsPayload` carries the union of all `sessions.*` verb arguments, with `omitempty` per field:

```go
type SessionsPayload struct {
    Label       string      `json:"label,omitempty"`       // sessions.new
    ID          string      `json:"id,omitempty"`          // sessions.rm
    JSONLPolicy JSONLPolicy `json:"jsonlPolicy,omitempty"` // sessions.rm
}

type JSONLPolicy string  // "leave" | "archive" | "purge" (empty = leave)
```

`sessions.rm` uses the `OK`/`Error` envelope — no typed result.

### Typed errors via Response.ErrorCode

`Pool.Remove` returns two sentinels the CLI sibling needs to match on. `Response.ErrorCode` carries a stable wire token decoupled from the message string:

```
Response.ErrorCode == "session_not_found"        → sessions.ErrSessionNotFound
Response.ErrorCode == "cannot_remove_bootstrap"  → sessions.ErrCannotRemoveBootstrap
```

The server detects the sentinel with `errors.Is` (so future server-side wrapping won't break the wire token), encodes both `Error` and `ErrorCode`, and the client's `SessionsRm` returns the bare sentinel directly so callers can `errors.Is` against it. Untyped errors flow through `Response.Error` verbatim with no `ErrorCode`.

The string-token rather than message-string approach means renaming a sentinel's message is no longer a wire-protocol break — only the `ErrorCode` enum is.

### JSONL policy translation

The wire enum (`JSONLPolicy` string) and the internal enum (`sessions.JSONLPolicy` uint8) are deliberately distinct. `protocol.go` stays import-free; the translation (`toSessionsPolicy` in `server.go`) lives next to the handler. Strings are jq-debuggable on the wire and durable across protocol versions if the underlying enum order ever changes. Empty wire value maps to `JSONLLeave` (matches the internal zero value); unknown values surface as `"sessions.rm: unknown jsonl policy %q"` in `Response.Error` rather than silent fallback.

See `docs/specs/architecture/98-control-sessions-rm.md` for the full design.

## Sessions: rename seam (1.1c-B1)

The third `sessions.*` verb is `sessions.rename`. The control server consumes session-rename commands through the `Renamer` interface in `internal/control`, embedded into `Sessioner` alongside `Remover`:

```go
type Renamer interface {
    Rename(id sessions.SessionID, newLabel string) error
}

type Sessioner interface {
    Create(ctx context.Context, label string) (sessions.SessionID, error)
    Remover
    Renamer
}
```

`*sessions.Pool` satisfies `Renamer` directly via `Pool.Rename` (#62). `Renamer` does not take a context — `Pool.Rename`'s signature is `(id, newLabel) error` and the operation is bounded by a single `Pool.mu` critical section + `saveLocked`, so the seam mirrors that shape adapter-free. Adding `Renamer` to `Sessioner` keeps `NewServer`'s signature stable; the rationale documented under "Sessions: removal seam" applies identically.

### Wire shape

`SessionsPayload` gains one omitempty field used by `sessions.rename`:

```go
type SessionsPayload struct {
    Label       string      `json:"label,omitempty"`       // sessions.new
    ID          string      `json:"id,omitempty"`          // sessions.rm, sessions.rename
    JSONLPolicy JSONLPolicy `json:"jsonlPolicy,omitempty"` // sessions.rm
    NewLabel    string      `json:"newLabel,omitempty"`    // sessions.rename
}
```

`sessions.rename` uses the `OK`/`Error` envelope — no typed result. Empty `NewLabel` on the wire is forwarded to `Pool.Rename` as the empty string, which clears the on-disk label per #62's contract.

### Typed errors via Response.ErrorCode

One sentinel propagates from `Pool.Rename`:

```
Response.ErrorCode == "session_not_found"  → sessions.ErrSessionNotFound
```

The client maps this to the corresponding sentinel error so callers can match with `errors.Is`. Untyped errors (e.g. registry persist failures) flow through `Response.Error` verbatim with no `ErrorCode`.

The `ErrorCode` envelope and the `ErrCodeSessionNotFound` token are reused verbatim from #98 — no new wire constants. This is the intended dividend of #98's wire-error infrastructure landing first: subsequent verbs reuse the envelope at zero incremental wire cost.

See `docs/specs/architecture/90-control-sessions-rename.md` for the full design.

## Sessions: list seam (1.1b-B1)

The fourth `sessions.*` verb is `sessions.list` — the first read-side member of the namespace. The control server consumes session-snapshot reads through the `Lister` interface in `internal/control`, embedded into `Sessioner` alongside `Remover` and `Renamer`:

```go
type Lister interface {
    List() []sessions.SessionInfo
}

type Sessioner interface {
    Create(ctx context.Context, label string) (sessions.SessionID, error)
    Remover
    Renamer
    Lister
}
```

`*sessions.Pool` satisfies `Lister` directly via `Pool.List` (#60). `Lister` does not take a context — `Pool.List`'s signature is `() []SessionInfo` and the operation is bounded by `Pool.mu` (RLock) + each `Session.lcMu` briefly, so the seam mirrors that shape adapter-free. `NewServer`'s signature is unchanged; the rationale documented under "Sessions: removal seam" applies identically.

### Wire shape

The verb carries no request payload — `Request.Sessions` stays nil for `sessions.list`, and `SessionsPayload` gains no new field. Response carries a new omitempty payload:

```go
type Response struct {
    Status       *StatusPayload       `json:"status,omitempty"`
    Logs         *LogsPayload         `json:"logs,omitempty"`
    SessionsNew  *SessionsNewResult   `json:"sessionsNew,omitempty"`
    SessionsList *SessionsListPayload `json:"sessionsList,omitempty"` // 1.1b-B1
    OK           bool                 `json:"ok,omitempty"`
    Error        string               `json:"error,omitempty"`
    ErrorCode    ErrorCode            `json:"errorCode,omitempty"`
}

type SessionsListPayload struct {
    Sessions []SessionInfo `json:"sessions"`
}

type SessionInfo struct {
    ID         string    `json:"id"`
    Label      string    `json:"label"`
    State      string    `json:"state"`       // "active" | "evicted"
    LastActive time.Time `json:"last_active"` // RFC3339Nano on the wire
    Bootstrap  bool      `json:"bootstrap,omitempty"`
}
```

`SessionInfo` is defined in `protocol.go` rather than reusing `sessions.SessionInfo` directly so external Go callers / future hand-written wire clients don't transitively import `internal/sessions` for the package-private `lifecycleState` enum.

### Encoding choices

- **`State` is a self-documenting string**, not an int. The two values `"active"` and `"evicted"` are exactly what `lifecycleState.String()` produces — the same encoding the on-disk registry uses, so the wire and the registry agree on token spelling without a parallel translation table. Same convention as `JSONLPolicy` (see `lessons.md` § "Wire enums: prefer self-documenting strings").
- **`LastActive` is `time.Time`**, not a pre-formatted string. `encoding/json` marshals `time.Time` to RFC3339Nano (jq-debuggable, byte-stable), and the typed value lets 61-B's renderer compute relative durations ("3m ago") without re-parsing. Note: JSON roundtrip strips the monotonic-clock component — comparisons after a roundtrip must use `time.Equal`, not `==` or `reflect.DeepEqual` (see `lessons.md` § "JSON roundtrip strips monotonic-clock state").
- **`Bootstrap` carries omitempty**, eliding the discriminator on every non-bootstrap entry.

### Translation site

The internal `sessions.SessionInfo` (with the package-private `lifecycleState` enum) is converted to the wire-level `control.SessionInfo` (with the public `string` state) inside `handleSessionsList`, the one handler — not at the renderer. The CLI consumer in 61-B receives wire-level types and does not import `internal/sessions`.

### No typed sentinels

`Pool.List` does not return errors. The seam's only failure path is the nil-sessioner branch ("sessions.list: no sessioner configured"); `ErrorCode` stays empty for this verb. `SessionsList` (the client wrapper) treats a nil `Response.SessionsList` on a non-error response as a daemon contract violation and returns `"control: empty sessions.list response"`. An explicit zero-length slice (`{"sessionsList":{"sessions":[]}}`) decodes to a non-nil payload with `len(Sessions) == 0` and surfaces as a well-formed empty result.

### Sort order

The seam returns whatever order `Pool.List` returned (LastActiveAt descending, SessionID ascending tiebreak). Final user-facing ordering is the responsibility of the CLI renderer (61-B); this layer does not re-sort.

See `docs/specs/architecture/87-control-sessions-list.md` for the full design.

### CLI renderer (1.1b-B2)

The operator-facing `pyry sessions list [--json]` verb consumes `control.SessionsList` and renders the snapshot in either of two formats. The renderer choices made here are the template the rest of Phase 1.1 follows for tabular output.

- **Columns.** `UUID`, `LABEL`, `STATE`, `LAST-ACTIVE`. UUIDs render in their full 36-character canonical form — no truncation. The "operators copy/paste UUIDs" property is load-bearing.
- **Padding.** Two spaces between columns via `text/tabwriter` (stdlib, no deps). Standard Go-CLI convention (`go list -m`, `go env`). Padding=1 cramps the table; padding=3+ wastes terminal width.
- **Time format.** `LAST-ACTIVE` renders as RFC3339 in the table. RFC3339Nano in `--json` (passed through verbatim from the wire's `time.Time`). Locale-aware "3m ago" formatting deferred — absolute timestamps are unambiguous across timezones and across log-paste-into-issue boundaries.
- **JSON envelope.** `{"sessions":[...]}`, intentionally not a bare array — leaves room for future top-level fields (e.g. `generated_at`, `schema_version`) without a breaking change. Per-element shape is `control.SessionInfo` JSON-encoded verbatim. Single trailing newline (`json.Encoder.Encode` default; what jq pipelines expect).
- **Sort policy.** The renderer re-sorts by `LastActive` desc with `ID` asc tiebreak (`sort.SliceStable` + `time.Time.Equal`). `Pool.List` already returns this order, but the renderer enforces it as a defence against future wire changes that would otherwise reshuffle every operator's table.
- **Bootstrap label.** The wire substitutes the bootstrap entry's empty on-disk label with `"bootstrap"` (per `Pool.List`'s contract); this layer renders verbatim. Empty `Label` for a non-bootstrap entry renders as the empty cell.
- **Timeout.** 5s, matching `runStatus`/`runLogs`. Diverges from rm/rename's 30s because list does not wait on `Pool.mu` against active sessions.

See `docs/specs/architecture/88-cli-sessions-list.md` for the full design.

## Sessions: CLI Router (1.1a-B2)

`pyry sessions <verb>` is the operator-facing surface for the `sessions.*` namespace. The router lives in `cmd/pyry/main.go` (`runSessions`) and dispatches `new` (#76) and `rm` (#99) today; 1.1b/c plug in as one switch case + one `runSessions<Verb>` helper each. The structural invariant — "one line per future verb" — is what the architect's choice of dispatch shape buys.

### Top-level dispatch

`run()`'s top-level switch gains a single case:

```go
case "sessions":
    return runSessions(os.Args[2:])
```

Adding 1.1b/c/d/e never touches this site again — the sub-router owns the rest.

### Sub-router

```go
const sessionsVerbList = "new, rm, rename, list" // appended by future verbs in lockstep with the switch

func runSessions(args []string) error {
    socketPath, rest, err := parseClientFlags("pyry sessions", args)
    if err != nil { return err }
    if len(rest) == 0 {
        return errSessionsUsage("missing subcommand")
    }
    sub, subArgs := rest[0], rest[1:]
    switch sub {
    case "new":
        return runSessionsNew(socketPath, subArgs)
    case "rm":
        return runSessionsRm(socketPath, subArgs)
    case "rename":
        return runSessionsRename(socketPath, subArgs)
    default:
        return errSessionsUsage(fmt.Sprintf("unknown verb %q", sub))
    }
}
```

Two design points lock the shape for the follow-on tickets:

- **The sub-router takes the *parsed* `socketPath`, not raw args.** `-pyry-socket` / `-pyry-name` are parsed exactly once by the existing `parseClientFlags` helper, before sub-verb dispatch. The convention "global pyry flags before the sub-verb" is enforced structurally — a `-pyry-name` placed *after* `new` reaches `runSessionsNew`'s own `flag.NewFlagSet` and produces "flag provided but not defined", not silent shadowing. Mirrors the top-level `splitArgs` convention ("pyry flags must come before claude args"). Pinned by `TestRunSessions_GlobalFlagAfterSubcommand_FailsCleanly` in `cmd/pyry/sessions_test.go`.
- **Constant `sessionsVerbList` over a derived list (map keys / reflection on the switch).** With one verb today and four 1-line additions in 1.1b/c/d/e, the duplication is one token per verb in two places (switch case + constant). A `map[string]func` would derive the list from `range m` but force a sort and pay an iteration cost that only amortizes at 3+ verbs. Dead-simple beats indirection here.

Unknown-verb (`pyry sessions list` before #61 lands) and missing-verb (`pyry sessions`) both surface through `errSessionsUsage` as `sessions: <detail>\nverbs: <list>`, exit 1. The router never falls through to the "forward unknown args to claude" path — the top-level switch returns from `runSessions` before `runSupervisor` is reached, so the verb namespace is closed structurally.

### `runSessionsNew` handler

```go
func runSessionsNew(socketPath string, args []string) error {
    label, err := parseSessionsNewArgs(args)
    if err != nil { return fmt.Errorf("sessions new: %w", err) }

    ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
    defer cancel()

    id, err := control.SessionsNew(ctx, socketPath, label)
    if err != nil { return fmt.Errorf("sessions new: %w", err) }
    fmt.Println(id)
    return nil
}
```

`parseSessionsNewArgs(args) (label string, err error)` is the unit-testable seam — flag-parse + arity check, no network. Mirrors `attachSelectorFromArgs`'s split (1.1e-D); keeps `cmd/pyry/sessions_test.go` table-driven over flag forms (`--name`, `-name`, `--name=`, `--name=glued`, extra positional, unknown flag) without dialling the socket.

Three boundary rules pinned by AC:

- **Stdout is exactly `<uuid>\n`** — `fmt.Println(id)` writes the canonical 36-character UUIDv4 with a single trailing newline, no surrounding text. Pinned by `TestSessionsNew_E2E_Labelled` / `_Unlabelled` against `^[0-9a-f]{8}-[0-9a-f]{4}-4[0-9a-f]{3}-[89ab][0-9a-f]{3}-[0-9a-f]{12}\n$`.
- **Empty `--name` / no `--name` are equivalent** — both produce `label=""`, which `Pool.Create` stores verbatim as a no-label registry entry. The synthetic `"bootstrap"` substitution in `Pool.List` does *not* apply because `Bootstrap=false`; non-bootstrap empty-label sessions stay empty-labelled.
- **30s timeout mirrors the server-side ceiling.** `handleSessionsNew` uses `context.WithTimeout(..., 30s)` for `Pool.Create`; the client matches so neither side hangs the other. Lower would race the claude-spawn path (2-15s typical); higher gains nothing operationally — a stuck `Pool.Create` at 30s is a bug to surface, not paper over.

### `runSessionsRm` handler (1.1d-B2)

```go
func runSessionsRm(socketPath string, args []string) error {
    id, policy, err := parseSessionsRmArgs(args)
    if err != nil {
        if errors.Is(err, errSessionsRmUsage) {
            fmt.Fprintln(os.Stderr, "pyry sessions rm:", err)
        }
        os.Exit(2)
    }

    ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
    defer cancel()

    canonical, err := resolveSessionIDViaList(ctx, socketPath, id)
    if err != nil {
        switch {
        case errors.Is(err, errAmbiguousPrefix):
            fmt.Fprintln(os.Stderr, err.Error()); os.Exit(1)
        case errors.Is(err, sessions.ErrSessionNotFound):
            fmt.Fprintf(os.Stderr, "no session with id %q\n", id); os.Exit(1)
        }
        return fmt.Errorf("sessions rm: %w", err)
    }

    if err := control.SessionsRm(ctx, socketPath, canonical, policy); err != nil {
        switch {
        case errors.Is(err, sessions.ErrCannotRemoveBootstrap):
            fmt.Fprintln(os.Stderr, "cannot remove bootstrap session"); os.Exit(1)
        case errors.Is(err, sessions.ErrSessionNotFound):
            fmt.Fprintf(os.Stderr, "no session with id %q\n", id); os.Exit(1)
        }
        return fmt.Errorf("sessions rm: %w", err)
    }
    return nil
}
```

Three design points anchor this handler:

- **Client-side prefix resolution via `control.SessionsList`.** `resolveSessionIDViaList` enumerates the wire-level snapshot, prefers an exact match, then falls back to a `strings.HasPrefix` scan. Zero hits → `sessions.ErrSessionNotFound`; one → canonical UUID; many → `errAmbiguousPrefix` carrying the AC-prescribed multi-line `<uuid> <label>` list (sorted by ID asc, bootstrap with empty label rendered as `bootstrap`). Mirrors `Pool.ResolveID`'s order so client-side and server-side resolution agree byte-for-byte. Cost: two RTTs per `rm` instead of one. See [ADR 011](../decisions/011-cli-prefix-resolution.md).
- **Three AC-prescribed messages bypass `main`'s outer error printer.** Ambiguous prefix, unknown UUID, and bootstrap rejection emit plain text via `fmt.Fprintln(os.Stderr, ...)` + `os.Exit(1)`, **without** the `pyry:` prefix `main` prepends to returned errors. Other errors (e.g. dial failure on a stopped daemon) flow through `fmt.Errorf("sessions rm: %w", err)` → `pyry: sessions rm: …`. The `runAttach` precedent uses the same shape for its exit-2 path. `os.Exit` skips the deferred `cancel()`; the only resource involved is a process-local timer reaped on exit.
- **One `errSessionsRmUsage` sentinel covers every parse-time failure.** `parseSessionsRmArgs` wraps mutually-exclusive flags, wrong arity, and `flag.Parse` errors all in the same sentinel. `runSessionsRm` matches with `errors.Is` once, exits 2, and prints `pyry sessions rm: <wrapped-msg>`. Every error path out of the parser is, definitionally, a usage error — discrimination would buy nothing.

A single TOCTOU race exists: `SessionsList` returns the canonical UUID, then another caller removes the session before our `SessionsRm` lands. The wire returns `ErrSessionNotFound` from the second step; the CLI surfaces the **operator's typed `<id>`** (possibly a prefix), not the canonical UUID — preserves debugging context. No retry; let the operator re-list.

`parseSessionsRmArgs(args) (id string, policy control.JSONLPolicy, err error)` is the unit-testable seam — flag-parse + arity + mutual-exclusion guard, no network. Empty policy on the wire normalises to `JSONLPolicyLeave` server-side.

### `runSessionsRename` handler (1.1c-B2a)

```go
func runSessionsRename(socketPath string, args []string) error {
    id, newLabel, err := parseSessionsRenameArgs(args)
    if err != nil {
        if errors.Is(err, errSessionsRenameUsage) {
            fmt.Fprintln(os.Stderr, "pyry sessions rename:", err)
        }
        os.Exit(2)
    }

    ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
    defer cancel()

    if err := control.SessionsRename(ctx, socketPath, id, newLabel); err != nil {
        if errors.Is(err, sessions.ErrSessionNotFound) {
            fmt.Fprintf(os.Stderr, "no session with id %q\n", id)
            os.Exit(1)
        }
        return fmt.Errorf("sessions rename: %w", err)
    }
    return nil
}
```

The simplest `runSessions<Verb>` so far — strictly fewer branches than `runSessionsRm`:

- **No prefix resolution.** `<id>` is forwarded verbatim to `control.SessionsRename`. AC#4 explicitly forbids prefix support in this slice; non-UUID input falls through to the server's `ErrSessionNotFound` mapping. The follow-up ergonomic slice lifts `resolveSessionIDViaList` into a shared helper at that point (the third caller of prefix resolution — currently only `rm`).
- **One typed-sentinel branch.** `Pool.Rename`'s only typed sentinel is `ErrSessionNotFound` (#62 has no bootstrap-rejection — renaming the bootstrap is allowed). #90's wire surface only propagates `ErrCodeSessionNotFound` for this verb. The matched case prints `no session with id "<id>"` to stderr (no `pyry:` prefix) and `os.Exit(1)`; everything else flows through `fmt.Errorf("sessions rename: %w", err)` to main's top-level `pyry: ` printer.
- **Empty `<new-label>` is a valid value, not an arity hole.** `parseSessionsRenameArgs` checks `fs.NArg() == 2`, not "two non-empty values" — so `pyry sessions rename <uuid> ""` parses as `(uuid, "")` and forwards through. `Pool.Rename` treats `""` as "clear the on-disk label" per #62. AC#1 explicitly requires this; no separate `--clear` flag.
- **Exactly 2 positionals (not "≥2").** Three or more is rejected as a usage error so multi-word labels must be quoted: `pyry sessions rename <uuid> "hello world"`. Same shape `git config` and `kubectl label` use; prevents silent token loss like `pyry sessions rename <uuid> hello world` discarding `world`.
- **No second positional split into a `--name`-style flag.** `<old> <new>` mirrors `git mv` / `kubectl rename`. Reusing `--name` (the create-time label flag on `pyry sessions new`) on rename would conflate two distinct semantics across verbs.
- **`<id>` is echoed back in the unknown-id message** (the operator's input string, not a normalised form). Today `<id>` must be the full canonical UUID, so input and canonical are identical — but using the raw input keeps the message format identical when prefix resolution arrives in the follow-up slice.

`parseSessionsRenameArgs(args) (id, newLabel string, err error)` is the unit-testable seam — flag-parse + arity, no network. The FlagSet exists for symmetry with `new` and `rm` (no flags today).

### Sub-verb flag parsing — own FlagSet per verb

Each sub-verb's flag parser is its own `flag.NewFlagSet("pyry sessions <verb>", flag.ContinueOnError)`. Mirrors `runInstallService`'s precedent. Phase 1.1c's `--new-name` and 1.1d's `--archive`/`--purge` will reuse the same shape — no namespace-specific options accumulate on the top-level FlagSet, no name collision possible across sub-verbs.

### Error propagation

| Scenario | Operator-visible message | Source |
|---|---|---|
| `pyry sessions` (no verb) | `pyry: sessions: missing subcommand\nverbs: new, rm, rename, list` (exit 1) | `errSessionsUsage` |
| `pyry sessions bogus` (unknown verb) | `pyry: sessions: unknown verb "bogus"\nverbs: new, rm, rename, list` (exit 1) | `errSessionsUsage` |
| `pyry sessions new` against stopped daemon | `pyry: sessions new: dial …: connect: no such file or directory` (exit 1) | `request()` → `dial()` wrap |
| `pyry sessions new --name foo bar` | `pyry: sessions new: unexpected positional "bar"` (exit 1) | `parseSessionsNewArgs` arity check |
| Server-side `Pool.Create` failure | `pyry: sessions new: sessions: create supervisor: <claude err>` (exit 1) | `Response.Error` propagated by `SessionsNew` |
| Server-side activation failure (id valid, lifecycle goroutine respawns later) | `pyry: sessions new: <activate err>` (exit 1). **Registry entry remains** — operator can `pyry attach <uuid>` once 1.1e ships. | per AC#4 |
| `pyry sessions rename <uuid>` (only one positional) | `pyry sessions rename: usage: expected <id> <new-label>, got 1 positional args` (exit 2) | `parseSessionsRenameArgs` arity check |
| `pyry sessions rename <unknown-uuid> alpha` | `no session with id "<unknown-uuid>"` (exit 1, no `pyry:` prefix) | `errors.Is(err, sessions.ErrSessionNotFound)` |
| `pyry sessions rename` against stopped daemon | `pyry: sessions rename: dial …: connect: no such file or directory` (exit 1) | `request()` → `dial()` wrap |

Activation failure is **not** distinguishable from a generic error at the wire boundary (the wire has no separate "id valid despite error" channel). The registry entry persists because `Pool.Create` saves before the activation step, so AC#4 is satisfied server-side.

### Help text

```
  pyry sessions <verb> [flags]                   manage sessions on a running
                                                  daemon (verbs: new, rm, rename, list)
```

Phase 1.1b/c/d/e each append one verb to the parenthesised list, in lockstep with `sessionsVerbList`.

### Tests

- **`cmd/pyry/sessions_test.go`** (unit, ~100 LOC) — pins router shape and flag-parse / arity rules in isolation. `TestRunSessions_NoSubcommand`, `TestRunSessions_UnknownVerb`, `TestRunSessions_GlobalFlagAfterSubcommand_FailsCleanly`, and table-driven `TestParseSessionsNewArgs` over `--name` / `-name` / `--name=` / extra positional / unknown flag.
- **`internal/e2e/sessions_new_test.go`** (e2e, build tag `e2e`, ~165 LOC) — daemon-up against the `writeSleepClaude` stand-in (#116): `TestSessionsNew_E2E_Labelled` / `_Unlabelled` (stdout regex + registry post-condition: ID present, `Label`, `Bootstrap=false`); `TestSessionsNew_E2E_UnknownVerb` (registry session count unchanged before/after — AC#3); `TestSessionsNew_E2E_NoDaemon` (`RunBare` against bogus socket; non-zero exit, non-empty stderr, no `panic`/`goroutine`/`runtime/` substrings — AC#2).
- **`cmd/pyry/sessions_test.go`** (extended for #99) — `TestParseSessionsRmArgs` table over no-args, only-flags, id-only, `--archive`, `--purge`, both flags (mutually exclusive), trailing positional, flag-after-positional (Go `flag` halts at first non-flag), unknown flag. `TestRunSessions_RmDispatch` pins the router wiring against a bogus socket (asserts the failure is a dial error, not the "unknown verb" router diagnostic).
- **`internal/e2e/sessions_rm_test.go`** (e2e, build tag `e2e`, ~290 LOC) — nine tests covering: happy path on full UUID and unique prefix, `--archive` and `--purge` plumbing, ambiguous-prefix rendering (mints sessions until pigeonhole-collision on first hex char — bound 17 over 16 hex digits), unknown UUID, bootstrap rejection, mutually-exclusive flags (exit 2), no-daemon dial failure (clean error, no panic).
- **`cmd/pyry/sessions_test.go`** (extended for #92) — `TestParseSessionsRenameArgs` table over no-args, only-`<id>`, `<id> <label>`, `<id> ""` (empty-label clear is valid), trailing positional (rejected — multi-word labels must be quoted), label with embedded space (single token — passes), unknown flag. `TestRunSessions_RenameDispatch` pins the router wiring against a bogus socket (asserts the error wraps `sessions rename:`, not the `unknown verb` router diagnostic).
- **`internal/e2e/sessions_rename_test.go`** (e2e, build tag `e2e`, ~163 LOC) — five tests: happy-path rename (label flips `before` → `after` in the registry); empty-label clear (`pyry sessions rename <uuid> ""` clears the on-disk label); unknown UUID (typed-error mapping → `no session with id "<uuid>"`, exit 1, registry untouched); no-daemon dial failure (`RunBare` against bogus socket; non-zero exit, no `panic`/`goroutine`/`runtime/` substrings); wrong-arity (one positional, exit **2** with `expected <id> <new-label>` on stderr, registry untouched).

`Pool.Create` failure surfacing (typed sentinels, nil-Sessioner branch, etc.) is **not** re-tested through the CLI shell — `internal/control/sessions_new_test.go` (#75) covers it exhaustively against the wire. The CLI is `fmt.Errorf("sessions new: %w", err)` over a wire client we trust.

See [ADR 010](../decisions/010-sessions-cli-sub-router.md) for why the sub-router takes a parsed `socketPath` rather than raw args, and why `sessionsVerbList` is a constant rather than derived.

## Process-Global vs Per-Session

| Concern | Scope today | Source |
|---|---|---|
| `status` payload | per-session (one supervisor) | `sess.State()` |
| `attach` stream | per-session (one bridge) | `sess.Attach(...)` |
| `resize` (live) | per-session (one bridge) | `sess.Resize(...)` (#137) |
| `logs` ring buffer | process-global | `LogProvider`, written by all loggers |
| `stop` shutdown | process-global | `shutdown` cancel func |

Phase 1.1's `pyry sessions new` (#76) and the upcoming `pyry sessions list` / `pyry attach <id>` extend the per-session column. Logs and stop stay process-global until a concrete need pushes them otherwise.

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
