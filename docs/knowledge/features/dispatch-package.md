# `internal/dispatch` (#307, extended #308, #318, #319, #311)

Per-phone-conn demultiplexer + handler-table seam sitting between
`internal/relay.Connection.Frames()` and the per-envelope-type
processors (`internal/relay/handlers/*`, downstream verb slices). Pure
package: imports `internal/protocol` only — no I/O, no transport, no
`internal/relay`. Carrier-agnostic so a future loopback / unit-test
transport plugs the same channels in without touching the dispatcher.

#308 plugs the auth gate in: `Config.FirstFrame` (optional) is invoked
once per new `conn_id` before normal handler-table dispatch and can
either accept-and-forward, reject-and-close (one envelope carrying
`Frame=<error>` + `CloseCode=4401`), or fall through to
`protocol.malformed`. Wired in `cmd/pyry/relay.go` to
`relay.AuthenticateFirstFrame`.

## What it is

```go
package dispatch

type Handler func(ctx context.Context, c *Conn, env protocol.Envelope) error

type Conn struct { /* opaque */ }
func (c *Conn) ConnID() string
func (c *Conn) NextID() uint64
func (c *Conn) Auth() *devices.Device // nil before gate accept; set once on accept (#318)
func (c *Conn) Send(ctx context.Context, env protocol.Envelope) error
func (c *Conn) Reply(ctx context.Context, req protocol.Envelope,
                    respType string, payload json.RawMessage) error

type Config struct {
    Frames         <-chan protocol.RoutingEnvelope // required
    OutboundBuffer int                              // default 32 when 0
    Logger         *slog.Logger                     // required
    FirstFrame     FirstFrameGate                   // optional (#308); nil disables gating
}

type FirstFrameGate func(ctx context.Context, env protocol.RoutingEnvelope) FirstFrameOutcome

type FirstFrameOutcome struct {
    Response  protocol.RoutingEnvelope // verbatim envelope to forward (gate owns ID/InReplyTo/TS)
    CloseConn bool                     // true → set Response.CloseCode=Code, stop per-conn goroutine
    Code      uint16                   // WS close code (4401 for auth.invalid_token)
    Err       error                    // gate-level failure → dispatcher emits protocol.malformed, no Response
    Device    *devices.Device          // #318; populated iff Err == nil && !CloseConn (accept-only)
}

type Dispatcher struct { /* opaque */ }
func New(cfg Config) *Dispatcher
func (d *Dispatcher) Register(envType string, h Handler) // pre-Run only
func (d *Dispatcher) Run(ctx context.Context) error
func (d *Dispatcher) Outbound() <-chan protocol.RoutingEnvelope
func (d *Dispatcher) ActiveConns() []*Conn  // #311; broadcast-eligibility snapshot

// Test-fixtures only — do not call from production code (#319).
func NewTestConn(id string, outbound chan<- protocol.RoutingEnvelope,
                  auth *devices.Device) *Conn
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

## First-frame gate (#308)

When `Config.FirstFrame` is non-nil, the dispatcher invokes it on the
**first** inbound frame for every new `conn_id`, on the per-conn
goroutine (so a slow gate stalls only one conn, never the demux). Three
outcomes:

| Outcome                       | Dispatcher action                                                                                                         |
|------------------------------ |---------------------------------------------------------------------------------------------------------------------------|
| `CloseConn=false` (accept)    | Publish `Response` onto `Outbound()`; advance `Conn.NextID()` one tick (gate's `hello_ack` is `id=1`, next handler reply gets `id=2`); subsequent frames flow through the handler table. |
| `CloseConn=true` (reject)     | Set `Response.CloseCode = Code` and publish; mark `connState.closed = true` under `d.mu`; per-conn goroutine returns. Further frames for this `conn_id` are dropped silently by the demux. |
| `Err != nil` (gate-malformed) | Emit `protocol.malformed` (no `in_reply_to`); per-conn goroutine continues with the gate consumed (subsequent frames take the normal handler-table path).                                  |

The closed-conn drop is structural: `routeConn` is the single critical
section that gates per-conn state, and its lookup-then-check happens
under the same `d.mu` that protects the per-conn goroutine's
exit-and-mark. A frame arriving for a just-closed conn returns
`(nil, true)` and the demux logs at Debug + continues — never blocks
on a channel send into a dead goroutine.

`CloseCode` is a **binary→relay** wire field. If a relay-side attacker
injects `CloseCode=4401` onto a phone→binary routing envelope, the
dispatcher ignores it (the gate runs on the inner frame as usual);
pinned by `TestFirstFrameGate_IgnoresInboundCloseCode`.

### Per-conn auth slot (#318)

The accept-and-continue branch of `runGate` calls an unexported
`(*Conn).setAuth(outcome.Device)` to populate a per-conn auth slot
**before** any handler-table dispatch runs on that conn. Verb handlers
read the matched device snapshot via `c.Auth()` rather than re-validating
the token (which is impossible anyway — `RoutingEnvelope.Token` is only
populated on the first frame per `conn_id`).

| State                           | `c.Auth()` returns                                |
|---------------------------------|---------------------------------------------------|
| Pre-gate (gate not yet run)     | `nil` — handlers MUST nil-check before deref      |
| Gate accept                     | `outcome.Device` (pointer-equal to gate's value)  |
| Gate reject (`CloseConn=true`)  | conn closes; no handler runs; slot never written  |
| Gate `Err`                      | gate consumed; subsequent frames hit handler table with `c.Auth() == nil` |

Concurrency posture is the existing single-writer-per-conn invariant:
`setAuth` is the sole writer, runs on the per-conn goroutine exactly
once before any handler dispatches, and `Auth()` is read by handlers on
the same goroutine. Cross-goroutine reads from handler-spawned workers
are happens-before-safe via goroutine-start synchronisation. No mutex,
no atomic — same justification as the `gateStarted` flag (see
§"Broadcast and the `gateStarted` / `gateCompleted` split (#311)" for the
rename history).

`setAuth` is **unexported** so verb handler closures cannot forge or
mutate auth state. The dispatcher does not panic on "accept with nil
device" — defensive code for an unobserved failure mode. Handler
authors must nil-check `c.Auth()` and the reviewer enforces it.

The `Device` carrier on `FirstFrameOutcome` is filled unconditionally
by the gate closure in `cmd/pyry/relay.go`, but the dispatcher only
calls `setAuth` inside the accept-and-continue branch — the `Err`
branch returns before reaching the call site, and the close-intent
branch publishes the reject envelope and exits without touching the
slot. Even a buggy gate that fills `Device` on a close-intent or `Err`
outcome leaves `Auth()` nil. Pinned by
`TestFirstFrameGate_CloseConnDoesNotPopulateAuth` and
`TestConnAuth_NilBeforeGate`.

See [codebase/318.md](../codebase/318.md) for the full design.

### Security: token in `RoutingEnvelope.Token`

`Token` is plaintext credential material. The dispatcher and any
`FirstFrameGate` implementation MUST NOT log it at any level. The only
consumer is `relay.AuthenticateFirstFrame`; the doc-comments on
`Config.Logger`, `FirstFrameGate`, and `RoutingEnvelope.Token` reiterate
this. See [codebase/308.md](../codebase/308.md) for the gate closure's
posture in `cmd/pyry/relay.go`.

## Broadcast and the `gateStarted` / `gateCompleted` split (#311)

`ActiveConns()` returns a snapshot of conns eligible for server-initiated
outbound (broadcast), used by `cmd/pyry`'s assistant-turn bridge to fan out
`message` envelopes from supervisor PTY output. Filtered to
`gateCompleted && !closed`:

```go
func (d *Dispatcher) ActiveConns() []*Conn  // holds d.mu briefly; fresh slice
```

The eligibility flag is **not** the same flag that prevents the per-conn
goroutine from re-running its gate. `connState` carries both:

| Flag             | Writer / reader            | Lock     | Set when                                                                              |
|------------------|----------------------------|----------|----------------------------------------------------------------------------------------|
| `gateStarted`    | per-conn goroutine only    | none     | TOP of gate path, before `runGate`. Local short-circuit; never read cross-goroutine. |
| `gateCompleted`  | per-conn → `ActiveConns`   | `d.mu`   | AFTER `runGate` returns on the accept path (i.e. after its `_ = c.NextID()` advance); OR immediately on the gate-disabled tail. |
| `closed`         | per-conn → demux           | `d.mu`   | Gate-reject path; unchanged from #308.                                               |

**Why the split is load-bearing.** `relay.AuthenticateFirstFrame` emits
`hello_ack` with **literal** `ID=1`, not via `c.NextID()`. `runGate`
publishes that envelope onto `d.outbound` and **then** runs
`_ = c.NextID()` so the next binary-originated frame on the conn gets
`id=2`. A broadcast call to `c.NextID()` that ran in the window between
gate-entry and that advance would claim `id=1` for its `message`
envelope and collide with `hello_ack` on the wire (two envelopes both
stamped id=1, both pushed to the shared outbound channel).

Setting `gateCompleted` **after** `runGate` returns — and reading it
under `d.mu` in `ActiveConns` — makes the collision structurally
impossible: when `ActiveConns` surfaces a conn, its `NextID` counter has
already advanced past 1, so any `c.NextID()` from a broadcast returns
≥ 2. Pinned by `TestDispatcher_ActiveConns_ExcludesPreGateConn`
(gate blocked on a test channel → 0 conns visible; gate released → 1
conn visible).

The gate-disabled tail sets `gateCompleted = true` immediately because
there is no `hello_ack` competing for `id=1` in that configuration —
the original dispatcher unit tests use gate-disabled config and the
gate-disabled snapshot path is pinned by
`TestDispatcher_ActiveConns_Snapshot`.

A conn that closes between `ActiveConns` snapshot and a subsequent
`c.Send` either has its frame queued onto the shared outbound chan
(and dropped at the transport layer per #307 reconnect semantics) or
blocks until ctx-cancel. No new lock order; both flags live under
the existing `d.mu`.

History: the original fix (PR #326) used a single `gateRun bool`
flipped at the **top** of the gate path. Code review caught that this
didn't close the race — `gateRun = true` ran **before** `runGate`,
leaving the same window open. The split landed in spec rev 2 + commit
`6f35312`. See [codebase/311.md § gateStarted / gateCompleted](../codebase/311.md)
for the full audit trail.

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
- **Per-conn close intent on auth reject** (#308). When the gate
  returns `CloseConn=true`, the per-conn goroutine publishes its
  outbound envelope with `CloseCode` set and exits; `connState.closed`
  flips under `d.mu` so the demux silently drops any straggler frame
  for the same `conn_id`. The phone-initiated close path (relay sends
  a per-conn close signal) is still deferred — the wire spec does not
  emit `connection_closed` per `conn_id` yet.

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
#303 `list_conversations` (landed), #319 `register_push_token` (landed,
sibling rewrite of the #250 pure handler against the new
`dispatch.Handler` signature), #304 `send_message` (pending). v1 types
without a registered handler still fall through to
`protocol.unsupported`.

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

Plus `internal/dispatch/gate_test.go` (#308):

- `TestFirstFrameGate_Accept` — gate runs once; second frame on the
  same conn bypasses the gate and hits the handler table.
- `TestFirstFrameGate_Reject` — one envelope with `Frame=<error>` AND
  `CloseCode==4401`; further frames for the same `conn_id` dropped.
- `TestFirstFrameGate_RejectDoesNotAffectOtherConns` — per-conn
  isolation.
- `TestFirstFrameGate_Err` — gate-malformed → `protocol.malformed`;
  gate is consumed, conn stays open.
- `TestFirstFrameGate_NilDisablesGate` — pre-#308 behaviour byte-stable.
- `TestFirstFrameGate_IgnoresInboundCloseCode` — pins the
  "CloseCode is binary→relay-only" wire invariant.
- `TestFirstFrameGate_ConcurrentConns` — ten conns in parallel
  under `-race`.

No e2e tests in this slice — the wiring is a strict extension of
#301's drain-and-discard (additive `Connection.Send`, empty outbound
until a verb slice registers a handler). Manual smoke at first
verb-slice integration is enough.

## Dependencies

- `internal/protocol` (#255 + #271) — `Envelope`, `RoutingEnvelope`,
  `IsV1Compatible`, `Code*` constants, `ErrorPayload`, `TypeError`.

### Test-only `Conn` constructor (#319)

`NewTestConn(id, outbound, auth) *Conn` is an exported, non-`_test.go`
constructor reserved for verb-handler test fixtures in sibling packages
(`internal/relay/handlers/*`). Go does not propagate `_test.go` symbols
across packages, so the seam has to be a regular export. Three field
assignments; `nextID` starts at zero (first `NextID()` returns 1).
Tests that want to simulate post-`hello_ack` state — where the gate
consumed id=1 and the first handler-originated reply lands at id=2 —
call `c.NextID()` once before invoking the handler.

The constructor does NOT mutate auth on a dispatcher-owned `*Conn`; it
only builds a fresh `*Conn` whose outbound channel the caller supplies.
The dispatcher remains the sole production `*Conn` factory (via
`routeConn`). Doc-comment opens with **"Test fixtures only — do not
call from production code."** Code-review checks no `cmd/` package
calls it.

## Out of scope (deferred)

- **Per-conn close intent on handler error.** Handlers return `error`
  in v1 but the dispatcher only logs at WARN. The auth gate is the
  only close-conn surface today; handlers that want to terminate a
  conn would need a typed return (`*Action` struct) or a sentinel
  (`ErrCloseConn`). No consumer requires this yet.
- **Per-conn close signal from relay.** The wire protocol does not
  emit `connection_closed` per `conn_id`; per-conn goroutines live
  until whole-stream lifecycle end (or auth-reject). Pairs with a
  future protocol revision.
- **Verb handlers.** `list_conversations` (#303), `send_message`
  (#304), `register_push_token` (#305) each register one route.
  Post-#308 the dispatcher is fully wired: a successful auth gate is
  followed by handler-table dispatch (currently empty → every frame
  falls through to `protocol.unsupported`).

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
