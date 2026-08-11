# `internal/dispatch` (#307; v1 gate/demux removed #1039)

Single-inbound-frame routing primitive between a relay/session-manager
frame source and the per-envelope-type processors
(`internal/relay/handlers/*`). Pure package: imports `internal/protocol`
and `internal/devices` only — no I/O, no transport.

The package originally (#307–#446) also owned a v1 demux goroutine
(`Dispatcher.Run`) and a first-frame auth gate (`FirstFrameGate`) that
fanned inbound frames out per `conn_id`. Both were orphaned when #913
removed the v1 relay leg — the v2 session manager
(`internal/relay/v2session.go`) does its own capability-aware routing and
calls into this package for exactly one thing: single-frame handler-table
dispatch via `Route`. #1039 deleted the dead gate/demux machinery
(`Config`, `FirstFrameGate`, `FirstFrameOutcome`, `Dispatcher`,
`connState`, `New`, `Register`, `Outbound`, `ActiveConns`, `Run`,
`routeConn`, `runConn`, `runGate`, `handleOne`, `setAuth`) outright, since
none of it had a production caller. See [codebase/1039.md](../codebase/1039.md)
for the removal; [codebase/307.md](../codebase/307.md),
[codebase/308.md](../codebase/308.md), [codebase/311.md](../codebase/311.md),
[codebase/318.md](../codebase/318.md) remain as historical record of the
removed design (gate/demux concurrency model, broadcast eligibility
tracking, per-conn auth-slot write seam) but no longer describe live code.

## What it is (current, post-#1039)

```go
package dispatch

type Handler func(ctx context.Context, c *Conn, env protocol.Envelope) error

type Conn struct { /* opaque */ }
func (c *Conn) ConnID() string
func (c *Conn) NextID() uint64
func (c *Conn) Auth() *devices.Device // set once at construction; nil if constructed without one
func (c *Conn) Send(ctx context.Context, env protocol.Envelope) error
func (c *Conn) Reply(ctx context.Context, req protocol.Envelope,
                    respType string, payload json.RawMessage) error

// Production Conn factory. The caller (the v2 session manager) owns its
// own per-conn goroutine and supplies the auth device it already
// resolved during the Noise handshake.
func NewConn(id string, outbound chan<- protocol.RoutingEnvelope,
              auth *devices.Device) *Conn

// Test-fixtures only — do not call from production code.
func NewTestConn(id string, outbound chan<- protocol.RoutingEnvelope,
                  auth *devices.Device) *Conn

// Route decodes frame, refuses frames whose type is not a known inbound
// app-frame type via protocol.IsKnownAppType, and dispatches to the
// handler registered for the envelope Type.
// Synchronous — runs the handler on the caller's goroutine; the caller
// drains conn's outbound channel afterwards.
func Route(ctx context.Context, logger *slog.Logger, conn *Conn,
            handlers map[string]Handler, frame json.RawMessage)
```

`Conn.auth` is now write-once at construction (`NewConn` / `NewTestConn`)
— there is no post-construction mutator. The v2 session manager passes
the handshake-matched `*devices.Device` directly
(`v2session.go:1051` → `dispatch.NewConn(s.connID, outbound, s.device)`);
the live production reader is `internal/relay/handlers/register_push_token.go`'s
`c.Auth()` call.

## Per-frame routing (`Route`)

| Inbound shape                             | Wire code              | `in_reply_to` |
|------------------------------------------ |----------------------- |---------------|
| `frame` not JSON-decodable                | `protocol.malformed`   | absent        |
| `PayloadEncrypted=true`                   | `protocol.unsupported` | `&req.ID`     |
| `Type` empty / not in `inboundAppTypeSet`  | `protocol.unknown_type`| `&req.ID`     |
| known type with no handler registered     | `protocol.unsupported` | `&req.ID`     |
| known type with handler                   | handler invoked        | —             |

Sentinel-to-`Code*` mapping happens inside `Route` (consumer's job per
`docs/PROJECT-MEMORY.md` § "Refusal-to-wire-code mapping is the
consumer's job"). `protocol.IsKnownAppType`'s encrypted-wins-over-unknown
check order is inherited verbatim — see the
[lessons in `codebase/307.md`](../codebase/307.md#islv1compatible-check-order-pins-the-stricter-rejection).

A non-nil error returned from the handler is logged at WARN; `Route`
synthesises no reply from it. `handlers` may be nil — every envelope then
falls through to the "no handler registered" reply path. Error envelopes
carry only the `Code*` string + a static `Message`; decode-error text,
stack info, and any byte derived from untrusted input never echo back on
the wire (`sendError`, unexported).

## Concurrency model

Goroutine-free. `Route` runs synchronously on the caller's goroutine; the
v2 session manager owns whatever goroutine calls it (one per conn_id, per
its own design — not this package's concern). This strictly shrank from
the pre-#1039 shape, which ran a demux goroutine (`Run`) fanning out to
one goroutine per `conn_id` (`routeConn` → `runConn`) with `WaitGroup` /
`d.mu` / channel-close shutdown choreography — all deleted.

## Test surface (`internal/dispatch/dispatch_test.go`)

Same-package, stdlib only, `go test -race`. Kept-branch coverage is
expressed as direct `Route` / `Conn` calls (construct `conn :=
NewConn(id, outbound, nil)` or `NewTestConn`, call `Route(ctx,
testLogger(), conn, handlers, frame)` synchronously, read the reply off
`outbound`):

- `TestRoute_StandaloneInvocation` / `TestRoute_NoHandler_UnsupportedReply`
  — the retained pair (predate #1039).
- `TestMalformedInnerFrame`, `TestUnknownType`, `TestEncryptedRefusal` —
  re-homed off the deleted `Dispatcher` harness by #1039; cover the
  malformed / `IsKnownAppType` unknown-type / `IsKnownAppType`
  unsupported-feature branches respectively.
- `TestIDCounter_MonotonicPerConn` — re-homed by #1039; two `NewConn`s,
  handler calls `NextID` four times, asserts per-conn independence.

`internal/dispatch/gate_test.go` (the v1 `FirstFrameGate` test suite) was
deleted in full by #1039, along with the `Dispatcher.Run` lifecycle tests
(`TestCtxCancel_Teardown`, `TestFramesClose_Teardown`,
`TestTwoConns_ArrivalOrderPreservedPerConn`, `ActiveConns` snapshot tests,
`Register`/`New` guard-panic tests) and their `runDispatcher` harness
helper.

## Dependencies

- `internal/protocol` (#255 + #271) — `Envelope`, `RoutingEnvelope`,
  `IsKnownAppType`, `Code*` constants, `ErrorPayload`, `TypeError`.
- `internal/devices` — `*devices.Device` carried on `Conn.auth`.

## Out of scope (deferred)

- **Per-conn close intent on handler error.** Handlers return `error` but
  `Route` only logs at WARN; no close-conn surface exists post-#1039 (the
  v1 auth gate was the only one, and it's gone). A consumer that needs
  this would add a typed return or sentinel — no consumer requires it yet.
- **Auth / first-frame gating** now lives entirely in the v2 Noise
  handshake (`internal/relay/v2session.go`), not in this package.

## Related

- Per-ticket records: [`codebase/307.md`](../codebase/307.md) (scaffold),
  [`codebase/308.md`](../codebase/308.md) (v1 auth gate, removed),
  [`codebase/311.md`](../codebase/311.md) (v1 broadcast eligibility,
  removed), [`codebase/318.md`](../codebase/318.md) (v1 auth slot,
  removed), [`codebase/446.md`](../codebase/446.md) (`Route`/`NewConn`
  introduced), [`codebase/913.md`](../codebase/913.md) (v1 relay branch
  removed, orphaning this package's gate/demux), [`codebase/1039.md`](../codebase/1039.md)
  (gate/demux machinery physically deleted).
- Upstream caller: [`features/relay-package.md`](relay-package.md) (v2
  session manager's `Route`/`NewConn` call sites).
- Protocol primitives: [`features/protocol-package.md`](protocol-package.md)
  (`IsKnownAppType`, `Code*` constants).
- Refusal-to-wire-code mapping convention: `docs/PROJECT-MEMORY.md` §
  "Refusal-to-wire-code mapping is the consumer's job"
