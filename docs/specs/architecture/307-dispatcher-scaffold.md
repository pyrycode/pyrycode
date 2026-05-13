# Spec — `internal/dispatch` scaffold + daemon wiring (#307)

## Files to read first

- `internal/relay/connection.go:161-186` — `Frames() <-chan protocol.RoutingEnvelope` / `Wait` / `Close` contract. Frames channel closes when the relay lifecycle terminates (covers ctx cancel and transport-fatal paths). Frames are delivered in arrival order.
- `internal/relay/connection.go:120-146` / `190-219` — how the relay's transport is constructed and how `client.Send` is reached today (handshake path); the new `Connection.Send` wraps the same `transport.Client`.
- `internal/protocol/envelope.go` — `Envelope` (id/type/ts/payload/in_reply_to) and `RoutingEnvelope{ConnID, Frame}`; `Frame` is `json.RawMessage` so the relay never decodes payloads.
- `internal/protocol/codes.go:9-31` — `CodeProtocolUnsupported = "protocol.unsupported"` and the rest of the `Code*` set the dispatcher maps `errors.Is` results to at the call site.
- `internal/protocol/envelope.go:47-75` — `IsV1Compatible` + `ErrUnknownType` / `ErrUnsupported` sentinels (already maps `payload_encrypted` to `unsupported`; the dispatcher composes this).
- `internal/transport/wssclient.go:262-300` — `Client.Send([]byte) error`; concurrency-safe, returns `ErrDisconnected` post-drop. The new `Connection.Send` is a JSON-marshal wrapper around it.
- `cmd/pyry/relay.go:88-119` — current `for range conn.Frames()` discard goroutine, plus the `Wait`/shutdown classification. Dispatcher wiring replaces the discard with two goroutines (run dispatcher; forward dispatcher Outbound to the relay).
- `docs/protocol-mobile.md:100-125` — Routing envelope semantics: `conn_id` is relay-assigned and opaque; the binary uses it verbatim to address replies; phones never see it.
- `docs/PROJECT-MEMORY.md:20` — Refusal-to-wire-code mapping convention: primitives return Go sentinels; the dispatcher does the `errors.Is → CodeProtocol*` mapping at the call site.

## Context

`internal/relay.Connection.Frames()` (#248) yields `protocol.RoutingEnvelope` values keyed by `conn_id` — one logical phone connection per id. Today, `cmd/pyry/relay.go` drains and discards. This ticket installs the dispatcher seam that downstream verb slices (#303 / #304 / #305) plug into, but registers no real handlers yet — every inbound frame falls through to `protocol.unsupported`. Auth gating and the 4401 close path land in #308.

Two design rules from existing conventions drive the shape:

1. **Wire-code mapping at the call site** (`docs/PROJECT-MEMORY.md:20`): handlers and the empty-table fall-through return Go sentinels; the dispatcher maps to `Code*` strings when it constructs the error envelope.
2. **Carrier-agnostic primitives** (the #249 pattern): the dispatcher takes a generic `<-chan protocol.RoutingEnvelope` and an outbound channel — it does NOT import `internal/relay`. Wiring lives in `cmd/pyry`.

## Design

### Package placement

New leaf package `internal/dispatch`. Imports `internal/protocol` only. Pure — no I/O, no `internal/relay` import, no `cmd/pyry` import. Two files:

- `internal/dispatch/dispatch.go` — `Dispatcher`, `Config`, `Handler`, `Conn`, `New`, `Register`, `Run`, `Outbound`.
- `internal/dispatch/dispatch_test.go` — same-package tests, stdlib only.

### Types and contract

- `type Handler func(ctx context.Context, c *Conn, env protocol.Envelope) error`
  - Signature is the registration target. Returning a non-nil error is logged at WARN and otherwise ignored in v1 (no close-conn semantics until #308 lands `CloseConn`). Document this in the type's doc-comment.
- `type Conn struct { ... }` — per-`conn_id` state, exposed to handlers.
  - `func (c *Conn) ConnID() string`
  - `func (c *Conn) NextID() uint64` — monotonic, starts at 1, allocated under `sync/atomic.Uint64` (concurrent-safe even though the per-conn goroutine is the only writer today; cheap insurance for future fan-out inside a handler).
  - `func (c *Conn) Send(ctx context.Context, env protocol.Envelope) error` — marshals `env` to `json.RawMessage`, wraps in `RoutingEnvelope{ConnID: c.id, Frame: …}`, pushes to dispatcher's Outbound channel; selects on `ctx.Done` for shutdown. Caller is responsible for setting `env.ID = c.NextID()` and `env.TS = time.Now().UTC()`. (Helper `Conn.Reply` covers the request/response path — see below.)
  - `func (c *Conn) Reply(ctx context.Context, req protocol.Envelope, respType string, payload json.RawMessage) error` — convenience: builds an envelope with `ID: c.NextID()`, `InReplyTo: &req.ID`, `Type: respType`, `TS: time.Now().UTC()`, `Payload: payload`, then `Send`s it. This is the AC-load-bearing helper for "`in_reply_to` matches the request id".
- `type Config struct { Frames <-chan protocol.RoutingEnvelope; OutboundBuffer int; Logger *slog.Logger }`
  - `OutboundBuffer` defaults to 32 when zero. Bounded backpressure: a slow Outbound consumer pauses the per-conn goroutines, which is the desired flow control.
- `type Dispatcher struct { ... }`
  - `func New(cfg Config) *Dispatcher` — validates `cfg.Frames != nil` and `cfg.Logger != nil`; panics on nil (programmer error; same posture as `transport.New` for missing required fields).
  - `func (d *Dispatcher) Register(envType string, h Handler)` — must be called before `Run`. Inserts into `map[string]Handler`. Panics on duplicate registration (programmer error; downstream slices each register one route).
  - `func (d *Dispatcher) Run(ctx context.Context) error` — blocks until the input `Frames` channel closes or `ctx` is done; returns `nil` for the normal exit and `ctx.Err()` for cancellation.
  - `func (d *Dispatcher) Outbound() <-chan protocol.RoutingEnvelope` — drains binary→relay frames; the caller (`cmd/pyry/relay.go`) forwards each to `Connection.Send`.

### Concurrency model

```
Run(ctx)
  │
  ├─ for env := range cfg.Frames:
  │     state, ok := conns[env.ConnID]
  │     if !ok { state = newConnState(env.ConnID); start per-conn goroutine }
  │     send env to state.input (buffered, blocking on backpressure)
  │
  ├─ ctx.Done    → close every state.input, wait WaitGroup, drain Outbound? no.
  └─ Frames closed → same teardown
```

- One demux goroutine: `Run` itself.
- N per-conn goroutines: one per active `conn_id`. Each owns a buffered `chan protocol.RoutingEnvelope` (size 8 — enough to absorb a small burst while a handler runs). Reads serially in arrival order; processes each frame; loops until input closes.
- Per-conn lookup table `map[string]*connState` lives on `*Dispatcher`; protected by `sync.Mutex`. The demux goroutine is the only writer; reads happen only from `Run` so the mutex protects against future concurrent introspection (e.g. a future `Snapshot()` for metrics) without forcing a redesign.
- Outbound channel: a single buffered `chan protocol.RoutingEnvelope` shared across all conns. Per-conn goroutines push; the cmd/pyry forwarder pulls. Bounded backpressure is the design.
- WaitGroup on per-conn goroutines: `Run` waits for all to exit before returning, then closes Outbound. The dispatcher owning Outbound close is what lets the cmd/pyry forwarder use `for env := range d.Outbound()` cleanly.

### Per-frame routing inside the per-conn goroutine

For each `env protocol.RoutingEnvelope`:
1. Decode `env.Frame` into `protocol.Envelope`. On JSON error: log WARN with `conn_id` + decode error; build an `error` envelope with `code: protocol.malformed` and `in_reply_to` omitted (no request id available); `Conn.Send`; continue.
2. `protocol.IsV1Compatible(inner)`:
   - `ErrUnsupported` (encrypted): build `error` envelope with `code: protocol.unsupported`, `in_reply_to: &inner.ID`; send; continue.
   - `ErrUnknownType`: same shape but `code: protocol.unknown_type`. (Note: empty-table fall-through below targets KNOWN types whose handler isn't registered → `protocol.unsupported`. The v1-compat check distinguishes "type not in v1 type set" from "type in v1 set but no handler registered yet" — two distinct refusals per spec.)
   - nil: lookup `handlers[inner.Type]`; on miss, build `error` envelope with `code: protocol.unsupported`, `in_reply_to: &inner.ID`; send; continue. On hit, invoke `h(ctx, conn, inner)`; non-nil result logged at WARN.

The mapping from sentinels to `Code*` strings is exactly the convention in `docs/PROJECT-MEMORY.md:20`. Capture the three branches as helpers (`errorEnvelopeFor(req, code)` + `sendError`) so the wire shape is built in one place.

### Shutdown sequence

- `ctx.Done` (daemon shutdown): demux loop selects on `ctx.Done` and the input channel; on ctx done it closes every `state.input`, waits for per-conn goroutines (WaitGroup), closes Outbound, returns `ctx.Err()`.
- Input `Frames` channel close (relay lifecycle ended): same teardown, returns `nil`.
- No per-conn-only close path in v1 — see Open Questions.

### Wiring (`cmd/pyry/relay.go`)

Replace the current single goroutine that `for range conn.Frames()` discards with:

1. Construct dispatcher: `d := dispatch.New(dispatch.Config{Frames: conn.Frames(), Logger: logger})`.
2. Goroutine A — dispatcher: `runErr := d.Run(ctx)`. Logged at debug on return.
3. Goroutine B — outbound forwarder: `for env := range d.Outbound() { _ = relayConn.Send(env) }`. Send errors logged at debug (transport-internal recycling already handles drops).
4. `conn.Wait()` classification path is unchanged.
5. The `cleanup` closure waits on both goroutines after `conn.Close()`. The dispatcher's `Outbound()` closes when `Run` returns, which guarantees the forwarder exits.

Handler registration is empty in this slice. Downstream verb slices each add one `d.Register(protocol.TypeXxx, xxxHandler)` line before `d.Run` is called.

### `internal/relay` addition — `Connection.Send`

New method, ~10 lines:

```go
// Send marshals env to JSON and forwards it to the relay over the
// current transport connection. Returns transport.ErrDisconnected if
// the conn is currently dropped (caller can choose to retry or drop).
func (c *Connection) Send(env protocol.RoutingEnvelope) error
```

Implementation: `json.Marshal(env)`, then `c.client.Send(raw)`. Document the disconnected-frame semantics in the doc-comment (transport-level reconnect is already in place; the relay handshake re-runs on the next `Connected()` signal, so a missed frame on a dropped conn is consistent with v1 protocol expectations).

## Error handling

| Failure | Surface |
|---|---|
| Inbound frame: `RoutingEnvelope.Frame` JSON-undecodable | log WARN; send `error{code: protocol.malformed}` with empty `in_reply_to` |
| Inbound frame: `payload_encrypted=true` | send `error{code: protocol.unsupported, in_reply_to: req.id}` |
| Inbound frame: empty / unknown `type` | send `error{code: protocol.unknown_type, in_reply_to: req.id}` |
| Inbound frame: known v1 type with no handler registered | send `error{code: protocol.unsupported, in_reply_to: req.id}` |
| Handler returns non-nil error | log WARN; continue. v2/auth ticket (#308) introduces close-conn intent. |
| `Conn.Send` blocked: Outbound full | per-conn goroutine blocks until the forwarder drains (intended backpressure) |
| `Conn.Send` ctx cancel during shutdown | returns wrapped `ctx.Err`; per-conn goroutine exits |

## Testing strategy

Same-package tests, stdlib only. Each test constructs a `Dispatcher`, feeds frames on an input channel the test owns, drains Outbound, asserts envelopes.

- **Empty-table unsupported.** Send a `RoutingEnvelope` with inner type `TypeSendMessage` and a known `id`. Drain Outbound. Expect one `RoutingEnvelope`; decode inner; assert `Type == TypeError`, decode payload, assert `code == CodeProtocolUnsupported`, `*InReplyTo == req.id`, `ConnID` echoed.
- **Unknown type.** Inner type `"bogus"`. Expect `code == CodeProtocolUnknownType`.
- **Encrypted refusal.** `payload_encrypted=true`. Expect `code == CodeProtocolUnsupported`.
- **Malformed inner frame.** `Frame: []byte("not json")`. Expect `code == CodeProtocolMalformed`, `InReplyTo == nil`.
- **id counter monotonic.** Register a stub handler that calls `c.NextID()` four times and records results. Send one frame. Assert returned ids are `[1, 2, 3, 4]`. Repeat with a second `conn_id`: assert that conn's counter also starts at 1 (per-conn ownership).
- **`Reply` builds correct `in_reply_to`.** Stub handler calls `c.Reply(ctx, req, TypeMessage, somePayload)`. Drain Outbound. Decode; assert `InReplyTo != nil && *InReplyTo == req.id`, `Type == TypeMessage`, `ID == 1`.
- **Ctx cancel teardown.** Start `Run` in a goroutine with a long-lived input. Cancel ctx. Assert `Run` returns within a short deadline, Outbound closes, and a per-conn goroutine (started via a first frame) does not leak. Verify via a per-test `WaitGroup` populated by the stub handler's exit; assert `wg.Wait` returns under a 1s deadline.
- **Frames-close teardown.** Same shape but close the input channel instead of cancelling ctx. Assert `Run` returns `nil`, Outbound closes.
- **Two conns interleaved.** Send frames for `conn-a` and `conn-b`; assert per-conn frames are dispatched in arrival order within each conn (no cross-conn ordering guarantee needed). The two-conn case also pins per-conn `id` independence.
- **Race coverage.** All tests pass under `go test -race`. The two-conn interleave + ctx-cancel test exercise the demux goroutine + per-conn goroutines + WaitGroup teardown concurrently.

No tests of the cmd/pyry wiring — that path is covered indirectly by the existing relay tests once the dispatcher is wired (manual smoke on first integration is enough; full e2e is out of scope for the scaffold).

## Open questions

- **Per-conn close signal.** The wire protocol does NOT currently define a `connection_closed` envelope from the relay. The AC reads "per-conn goroutine exits cleanly when the phone disconnects (the frame stream signals close for that `conn_id`)" — the only signal in v1 is the whole-stream close. Per-conn goroutines therefore live until daemon shutdown / relay lifecycle end. A follow-up (likely paired with #308's auth-close path) should either:
  (a) extend the wire protocol with a per-conn close signal the relay emits, or
  (b) introduce an idle/inactivity timeout per conn.

  Recommendation: defer to #308's design — when auth fails the dispatcher must close the conn, which is the first real "close one conn but not the others" requirement; the same mechanism extends naturally to phone-disconnect once the wire spec adds the signal.

- **Handler-return-error semantics.** Logged-and-continued in v1. #308's `AuthOutcome{CloseConn: bool}` introduces the first real close-conn intent; the cleanest extension is a typed return — e.g. `Handler` returns a `*Action` struct or a sentinel `ErrCloseConn` the dispatcher recognises. Leave as-is for the scaffold.

- **Outbound buffer size.** Defaulted to 32. Revisit when verb slices land if the bounded backpressure pauses dispatch under load (unlikely at v1 volumes; phone-side throughput is low).
