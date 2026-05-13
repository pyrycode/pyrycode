# `internal/dispatch` (#307)

Per-phone-conn demultiplexer + handler-table seam sitting between
`internal/relay.Connection.Frames()` and the per-envelope-type
processors (`internal/relay/handlers/*`, downstream verb slices). Pure
package: imports `internal/protocol` only — no I/O, no transport, no
`internal/relay`. Carrier-agnostic so a future loopback / unit-test
transport plugs the same channels in without touching the dispatcher.

## What it is

```go
package dispatch

type Handler func(ctx context.Context, c *Conn, env protocol.Envelope) error

type Conn struct { /* opaque */ }
func (c *Conn) ConnID() string
func (c *Conn) NextID() uint64
func (c *Conn) Send(ctx context.Context, env protocol.Envelope) error
func (c *Conn) Reply(ctx context.Context, req protocol.Envelope,
                    respType string, payload json.RawMessage) error

type Config struct {
    Frames         <-chan protocol.RoutingEnvelope // required
    OutboundBuffer int                              // default 32 when 0
    Logger         *slog.Logger                     // required
}

type Dispatcher struct { /* opaque */ }
func New(cfg Config) *Dispatcher
func (d *Dispatcher) Register(envType string, h Handler) // pre-Run only
func (d *Dispatcher) Run(ctx context.Context) error
func (d *Dispatcher) Outbound() <-chan protocol.RoutingEnvelope
```

## Concurrency model

```
Run(ctx)                       (demux goroutine)
  ├─ select { ctx.Done | Frames closed | env <- Frames }
  │     getOrCreateConn(env.ConnID)            (one critical section
  │       └─ alloc connState + go runConn       under d.mu — lookup,
  │     state.input <- env  (size-8 buffer)     insert, and goroutine
  │                                             start are atomic)
  ├─ teardown: close every state.input
  └─ wg.Wait(); close(outbound); return
```

- **One demux goroutine** — `Run` itself.
- **N per-conn goroutines** — one per active `conn_id`. Each reads its
  buffered `chan protocol.RoutingEnvelope` (size 8) and serially calls
  `handleOne`. Arrival order preserved within a conn; no cross-conn
  ordering guarantee.
- **Shared bounded outbound channel** — per-conn goroutines push;
  the cmd/pyry forwarder pulls. Slow consumer pauses producers — the
  intended flow control.
- **WaitGroup teardown** — `Run` waits for all per-conn goroutines to
  exit before closing `Outbound`, so the caller can `for env := range
  d.Outbound()` and know the drain is complete.

## Per-frame routing (`handleOne`)

| Inbound shape                             | Wire code              | `in_reply_to` |
|------------------------------------------ |----------------------- |---------------|
| `RoutingEnvelope.Frame` not JSON-decodable| `protocol.malformed`   | absent        |
| `PayloadEncrypted=true`                   | `protocol.unsupported` | `&req.ID`     |
| `Type` empty / not in v1 set              | `protocol.unknown_type`| `&req.ID`     |
| v1 `Type` with no handler registered      | `protocol.unsupported` | `&req.ID`     |
| v1 `Type` with handler                    | handler invoked        | —             |

Sentinel-to-`Code*` mapping happens inside the dispatcher (consumer's
job per `docs/PROJECT-MEMORY.md` § "Refusal-to-wire-code mapping is the
consumer's job"). `protocol.IsV1Compatible`'s
encrypted-wins-over-unknown check order is inherited verbatim — see the
[lessons in `codebase/307.md`](../codebase/307.md#islv1compatible-check-order-pins-the-stricter-rejection).

Error envelopes carry only the `Code*` string + a static `Message`.
Decode-error text, stack info, and any byte derived from untrusted
input never echo back on the wire.

## Register-before-Run is enforced, not advisory

The `handlers map[string]Handler` is read by per-conn goroutines without
a lock. To keep that defensible, `Run` flips an `atomic.Bool` before
reading from `Frames`; `Register` checks it and panics on late
registration — same posture as duplicate-type registration. Downstream
verb slices `Register` at startup, then call `Run`; the shape mirrors
`http.ServeMux.Handle` before `http.Server.Serve`.

## Shutdown

- **`ctx.Done`** (daemon shutdown): demux loop selects on `ctx.Done`
  and the input channel; on cancel it closes every `state.input`,
  waits the WaitGroup, closes `Outbound`, returns `ctx.Err()`.
- **`Frames` closed** (relay lifecycle ended): same teardown, returns
  `nil`.
- **No per-conn-only close path in v1.** The wire protocol does not
  emit a `connection_closed` envelope per `conn_id`; per-conn
  goroutines live until daemon shutdown or whole-stream lifecycle end.
  The auth-close path (#308) is the first real "close one conn but not
  the others" requirement; the same mechanism extends naturally to
  phone-disconnect once the wire spec adds the signal.

## Daemon wiring (`cmd/pyry/relay.go`)

```
relay.Connection.Frames() ──> dispatch.Run(ctx) ──> Outbound() ──> Connection.Send
                                                                     │
                                                                     ▼
                                                                  relay (WSS)
```

Three goroutines plus a deterministic cleanup chain replace the prior
drain-and-discard loop:

1. **Dispatcher goroutine** — `d := dispatch.New(...)`; `d.Run(ctx)`.
2. **Outbound forwarder** — `for env := range d.Outbound() { conn.Send(env) }`.
   Send errors logged at DEBUG (transport handles reconnect; dropped
   frames during a disconnected window are expected per v1 protocol
   semantics — see `connection.go:170-184`).
3. **Wait classifier** — `conn.Wait()` (unchanged from #301).

Cleanup waits in lifecycle order: `conn.Close()` → `Connection.run`
closes `c.frames` → `dispatcher.Run` returns → `dispatcher` closes
`Outbound` → forwarder exits → `Wait` returns. Each goroutine signals
the next via channel close; no auxiliary `done` channels needed.

The handler table stays empty in #307. Downstream verb slices add one
`d.Register(protocol.TypeXxx, ...)` line apiece before `Run` spawns —
#303 `list_conversations`, #304 `send_message`, #305
`register_push_token`. Until those land, every inbound phone frame
falls through to `protocol.unsupported`; that is the intentional
posture pending auth-gate #308.

## Security / operational notes

Cross-references from the architect's security review of #307:

- **Inbound frame size** is capped by `internal/transport`'s WS read
  path (`conn.SetReadLimit(1 MiB)`, see #247). The dispatcher does not
  re-enforce; verb slices likewise rely on the transport cap.
- **Head-of-line blocking**: the single demux goroutine blocks on
  `state.input <- env` if a per-conn handler stalls. In v1 (empty
  table) no handler runs, so this is not exploitable yet. Re-evaluate
  when the first long-running handler (likely an LLM-touching route)
  is registered.
- **Per-conn goroutine fan-out** is unbounded by the dispatcher;
  the relay enforces its per-binary connection cap per
  `docs/protocol-mobile.md`. The dispatcher inherits that cap.
- **Logging discipline**: WARN/DEBUG diagnostics carry `conn_id`,
  envelope `type`, envelope `id`, and the decode-error class — never
  the raw frame payload. Verb slices crossing this code path (message
  bodies, push tokens) must keep the same posture.

## Test surface (`internal/dispatch/dispatch_test.go`)

Same-package, stdlib only, passes under `go test -race`:

- `TestEmptyTable_UnsupportedType` — known v1 type (`TypeSendMessage`),
  no handler ⇒ `protocol.unsupported`, `in_reply_to=&req.ID`, `ConnID`
  echoed.
- `TestUnknownType` — `Type="bogus"` ⇒ `protocol.unknown_type`.
- `TestEncryptedRefusal` — `PayloadEncrypted=true` ⇒
  `protocol.unsupported` (encrypted-wins-over-unknown check order).
- `TestMalformedInnerFrame` — `Frame:[]byte("not json")` ⇒
  `protocol.malformed`, `InReplyTo == nil`.
- `TestIDCounter_MonotonicPerConn` — stub handler reads `NextID()` four
  times for two distinct `conn_id`s; each starts at 1 (per-conn
  ownership).
- `TestReply_InReplyToMatchesRequest` — `c.Reply` builds envelope with
  `InReplyTo == &req.ID`, `Type == respType`, `ID == 1`.
- `TestCtxCancel_Teardown` / `TestFramesClose_Teardown` — both
  shutdown paths return within a short deadline, `Outbound` closes, no
  leaked per-conn goroutines.
- `TestTwoConns_ArrivalOrderPreservedPerConn` — interleaved frames
  honour per-conn order; pins per-conn `id` independence.
- `TestRegister_DuplicatePanics` / `TestRegister_AfterRunPanics` /
  `TestNew_NilFrames|LoggerPanics` — programmer-error posture.

No e2e tests in this slice — the wiring is a strict extension of
#301's drain-and-discard (additive `Connection.Send`, empty outbound
until a verb slice registers a handler). Manual smoke at first
verb-slice integration is enough.

## Dependencies

- `internal/protocol` (#255 + #271) — `Envelope`, `RoutingEnvelope`,
  `IsV1Compatible`, `Code*` constants, `ErrorPayload`, `TypeError`.

## Out of scope (deferred)

- **Auth gating the first frame** (#308). Until then, every inbound
  phone frame after `hello` hits the empty handler table and gets
  `protocol.unsupported`.
- **Per-conn close intent on handler error.** Handlers return `error`
  in v1 but the dispatcher only logs at WARN. #308's
  `AuthOutcome.CloseConn` introduces the first real close-conn intent;
  extension is either a typed return (`*Action` struct) or a sentinel
  (`ErrCloseConn`).
- **Per-conn close signal from relay.** The wire protocol does not
  emit `connection_closed` per `conn_id`; per-conn goroutines live
  until whole-stream lifecycle end. Follow-up paired with #308.
- **Verb handlers.** `list_conversations` (#303), `send_message`
  (#304), `register_push_token` (#305) each register one route. This
  slice ships the seam, not the routes.

## Related

- Per-ticket record: [`codebase/307.md`](../codebase/307.md)
- Spec: [`specs/architecture/307-dispatcher-scaffold.md`](../../specs/architecture/307-dispatcher-scaffold.md)
- Upstream producer: [`features/relay-package.md`](relay-package.md)
  (`Connection.Frames()`, `Connection.Send`)
- Protocol primitives: [`features/protocol-package.md`](protocol-package.md)
  (`IsV1Compatible`, `Code*` constants)
- Refusal-to-wire-code mapping convention:
  `docs/PROJECT-MEMORY.md` § "Refusal-to-wire-code mapping is the
  consumer's job"
