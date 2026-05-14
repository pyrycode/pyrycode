# Ticket #311 — net: assistant-turn → `message` envelope bridge

Bridges the supervised claude child's stdout (PTY output) into the relay
dispatcher's outbound surface. When claude emits an assistant turn, the
binary builds a `message` envelope stamped with the supervisor's
`CurrentConversation()` cursor and broadcasts it to every currently-active
phone conn.

This is the outbound counterpart to #322's inbound `send_message` path. Together
they close the bidirectional loop: phone → `send_message` → PTY (#322), then
PTY → `message` → phone (#311).

> **Revision 2 (2026-05-14).** Spec rework after code-review on PR #326
> identified a defect in the original `gateRun`-based race fix. The
> single-flag fix did NOT close the literal-id=1 vs broadcast race
> because `gateRun = true` was set BEFORE `runGate`'s `NextID()` advance.
> This revision splits the flag into `gateStarted` (local, prevents
> double-running the gate) and `gateCompleted` (cross-goroutine, set
> AFTER `NextID()` advance) — what `ActiveConns` filters on. See
> §"Component 2" and §"Security review" [Concurrency] for the full
> audit trail. Implementation delta vs the merged WIP: rename the
> `connState` field, add the second flag, split the assignment site
> inside `runConn`, and add a gate-enabled regression test.

## Files to read first

- `internal/supervisor/bridge.go:49-179` — `Bridge` type; existing
  `Write` (PTY → attached writer) is the existing seam; new observer
  hook is a 3-line tee inside it.
- `internal/supervisor/supervisor.go:140-194` — `WriteUserTurn` /
  `CurrentConversation` / `setPTY`; #312 mutex discipline (`convMu` /
  `ptmxMu` both leaf-only).
- `internal/supervisor/supervisor.go:321-364` — service-mode
  `io.Copy(s.cfg.Bridge, ptmx)` is the load-bearing PTY-drain
  goroutine; do NOT block it.
- `internal/dispatch/dispatch.go:63-152` — `Conn` API (`ConnID`,
  `NextID`, `Send`, `Reply`); per-conn ID monotonic via
  `atomic.Uint64`. New `ActiveConns()` reads `d.conns` under `d.mu`.
- `internal/dispatch/dispatch.go:249-264` — `connState` /
  `gateRun` / `closed` flags; the demux's `routeConn` already drops
  frames for closed conns. Broadcast can safely race conn-close. This
  ticket renames `gateRun` → `gateStarted` and adds a NEW
  `gateCompleted` flag — see §"Component 2" for the rationale.
- `internal/dispatch/dispatch.go:431-457` — `runConn` loop body;
  this is the file where the `gateStarted` / `gateCompleted` split
  lands. The accept-path and gate-disabled-tail flag writes are the
  load-bearing edits.
- `internal/dispatch/dispatch.go:459-494` — `runGate`; note the
  `_ = st.conn.NextID()` advance at the tail of the accept path
  (line ~491). `gateCompleted` MUST be set AFTER this returns —
  i.e. in the caller (`runConn`), not inside `runGate`.
- `internal/dispatch/dispatch.go:118-135` — `Conn.Send` is the
  canonical outbound seam; the broadcast loop calls it per-conn.
- `cmd/pyry/relay.go:86-189` — `startRelay` builds the dispatcher,
  registers handlers, owns the forwarder goroutine. New wiring lands
  here.
- `internal/relay/handlers/send_message.go:46-76` — sibling
  handler shape (inbound user-turn); the outbound bridge mirrors
  this in reverse.
- `internal/protocol/messaging.go:13-24` —
  `MessagePayload{ConversationID, MessageID, Role, Text}`; this
  ticket emits `Role: "assistant"`.
- `internal/protocol/codes.go:45` — `TypeMessage = "message"`.
- `internal/conversations/id.go` — `NewID()` / `ValidID()`; reused
  for `MessageID` generation (UUIDv4, `crypto/rand`).
- `internal/e2e/internal/fakeclaude/main.go` — extend with a
  scripted-stdout trigger mechanism (parallel to the existing
  `PYRY_FAKE_CLAUDE_TRIGGER` for rotation).
- `internal/e2e/relay_send_message_test.go` — sibling e2e shape;
  the assistant-turn e2e mirrors it through `Receive`.
- `internal/e2e/harness.go` — `StartRotationWithRelay` (#323) is
  reused as-is; no new harness helper needed.
- `docs/knowledge/codebase/312.md` — `WriteUserTurn` / cursor
  invariants; `CurrentConversation()` survives child restarts.
- `docs/knowledge/codebase/322.md` — inbound `send_message` shape;
  patterns reinforced (handler-owned interface, per-conn ID).
- `docs/knowledge/codebase/323.md` — fakeclaude observability
  pattern (`PYRY_FAKE_CLAUDE_STDIN_LOG`); use the same
  additive-by-env-var posture.

## Context

The dispatcher (#307) wires inbound phone frames to handlers; #322 wired the
inbound `send_message` verb through to `Supervisor.WriteUserTurn`. The
outbound direction is currently a one-way street: claude's PTY output is
forwarded to the attach client (or dropped when nobody is attached) via
`Bridge.Write`, but never reaches the relay-side `Conn.Send` surface that
would let a paired phone observe the assistant's reply.

This ticket adds the missing tap: a non-blocking observer on `Bridge.Write`,
plumbed through `cmd/pyry/relay.go` into a small emitter that reads the
supervisor's conversation cursor, builds a `message` envelope, and fans it
out to every attached phone conn.

Two architectural questions the issue body asks the architect to settle:

1. **Coupling shape** — channel, callback, or registration handle? **Decision:
   a callback registered on `*supervisor.Bridge`.** The bridge already mediates
   PTY output (its `Write` is called on every chunk); a one-method extension
   keeps the production change tight. The closure that captures supervisor +
   dispatcher state lives in `cmd/pyry/relay.go`, not in any leaf package —
   no new import edges between `internal/supervisor` and `internal/dispatch`.

2. **Routing topology** — owner-conn vs. all-subscribed? **Decision: broadcast
   to every currently-active conn for v1.** Pyrycode supports a single
   paired-phone topology in v1 (single token, single conn at a time in
   practice). A `conversation_id → conn_id` map is overdesign for a
   subscriber set of size ≤ 1. Future subscription semantics (#???) will
   replace the broadcast call without touching the supervisor or bridge
   sides.

## Design

### Component 1: `internal/supervisor/bridge.go` — output-observer hook

Add one unexported field, one exported setter, and a 3-line tee inside
`Write`:

```go
// Bridge struct gains:
outputObserver func([]byte)  // guarded by mu, alongside b.output
```

```go
// SetOutputObserver registers (or clears, when fn is nil) a callback
// invoked from Write with each PTY-output chunk before the chunk is
// forwarded to the attached writer.
//
// The observer runs on the supervisor's PTY-drain goroutine. It MUST
// NOT block, MUST NOT panic, and MUST NOT retain p past return — the
// supervisor's io.Copy reuses the buffer for the next read. Production
// observers must enqueue to a buffered channel and drop on overflow.
func (b *Bridge) SetOutputObserver(fn func([]byte))
```

Inside `Write` (before the existing `out == nil` check), under the
existing `b.mu`:

```go
b.mu.Lock()
out := b.output
obs := b.outputObserver
b.mu.Unlock()
if obs != nil {
    obs(p)
}
// ... existing forwarding code
```

Invariants preserved:

- **Write still never returns an error.** The observer is called for
  side-effect; its panic/block is contractually forbidden, not defended
  against here. (Defence-in-depth lives in the closure, not the bridge.)
- **No new lock order.** The observer field rides `b.mu`, the same lock
  already held to read `b.output`. No additional acquisition.
- **No retention of `p`.** Documented; the production observer copies on
  the way into the channel.

### Component 2: `internal/dispatch/dispatch.go` — `ActiveConns()` snapshot

Add one method to `Dispatcher`:

```go
// ActiveConns returns a snapshot of currently-active conns eligible for
// server-initiated outbound (broadcast). The returned slice excludes:
//   - conns marked closed (gate-reject path; routeConn drops further
//     frames for them)
//   - conns whose first-frame gate has not yet RETURNED on the
//     accept-and-continue path — i.e. connState.gateCompleted == false.
//     "Completed" means the gate's `_ = c.NextID()` advance has executed,
//     so the per-conn id counter has moved past the hello_ack's literal
//     id=1 and a broadcast call to c.NextID() will return id >= 2.
//
// The gate-completed filter is load-bearing: relay.AuthenticateFirstFrame
// emits hello_ack with literal ID=1 (auth.go:148), not via c.NextID().
// runGate calls `_ = c.NextID()` AFTER publishing hello_ack onto d.outbound
// so the next binary-originated frame on this conn (handler reply, etc.)
// gets id=2 (dispatch.go:486-492). A broadcast that races the gate would
// call c.NextID() first, claim id=1 for its message envelope, and collide
// with hello_ack on the wire (two envelopes both stamped id=1). Filtering
// on gateCompleted — set ONLY after runGate's NextID() advance — closes
// that race deterministically.
//
// The slice is fresh; callers may retain it. The returned *Conn pointers
// remain safe to call Send on — a conn that closes between snapshot and
// Send is handled by the demux's existing closed-conn drop in routeConn.
//
// Concurrency: holds d.mu briefly to copy the conns map under the same
// lock that guards both closed-flag and gateCompleted mutation; no new
// lock order.
func (d *Dispatcher) ActiveConns() []*Conn
```

Behaviour: iterate `d.conns` under `d.mu`, copy `*Conn` pointers where
`st.gateCompleted && !st.closed` into a fresh slice, return.

**Why two flags, not one.** `connState.gateRun` (as implemented today)
serves a local single-goroutine purpose: "have we already attempted the
gate on this conn, so we should NOT run it again on the next inbound
frame." It is set at the TOP of the gate path, BEFORE `runGate` runs —
because the whole point is to short-circuit subsequent loop iterations.
That `set-before-runGate` placement makes `gateRun` unfit as the
broadcast-eligibility predicate: a broadcast call that reads
`gateRun == true` between the assignment and `runGate`'s closing
`_ = c.NextID()` will still race the literal-id=1 hello_ack.

Split the responsibilities:

- **`gateStarted bool`** — renamed from `gateRun`. Local single-writer/
  reader on the per-conn goroutine. Lock-free, exactly as today. Purpose:
  prevent double-running the gate inside `runConn`'s loop. No
  cross-goroutine read.
- **`gateCompleted bool`** — NEW. Set ONLY after the gate has emitted
  hello_ack AND advanced the per-conn id counter via `_ = c.NextID()`.
  Written by the per-conn goroutine, read by `ActiveConns` callers on
  the emitter goroutine. Both writer and reader operate under `d.mu`
  (same lock that guards `closed`, no new lock order).

Concretely, `connState` becomes:

```go
type connState struct {
    conn          *Conn
    input         chan protocol.RoutingEnvelope
    gateStarted   bool  // local-only; prevents re-entering the gate path
    gateCompleted bool  // under d.mu; gates broadcast eligibility
    closed        bool  // under d.mu; gate-reject / close-intent
}
```

And `runConn`'s loop becomes (sketch — full body in `runConn`):

```go
for routing := range st.input {
    if !st.gateStarted {
        st.gateStarted = true  // local-only write
        if d.cfg.FirstFrame != nil {
            if d.runGate(ctx, st, routing) {
                d.mu.Lock()
                st.closed = true
                d.mu.Unlock()
                return
            }
            // Gate accepted. Mark broadcast-eligible AFTER runGate's
            // _ = c.NextID() advance (which runs inside runGate before
            // it returns on the accept path).
            d.mu.Lock()
            st.gateCompleted = true
            d.mu.Unlock()
            continue
        }
        // No gate configured — there is no hello_ack on the wire to
        // collide with, so the conn is immediately broadcast-eligible.
        // Still publish via d.mu for the cross-goroutine read.
        d.mu.Lock()
        st.gateCompleted = true
        d.mu.Unlock()
    }
    d.handleOne(ctx, st.conn, routing)
}
```

The accept-path `gateCompleted = true` assignment is sequenced AFTER
`runGate` returns, and `runGate` only returns on the accept path AFTER
it has both `select`-published hello_ack onto `d.outbound` AND executed
`_ = st.conn.NextID()`. So when `ActiveConns` observes
`gateCompleted == true`, the per-conn id counter is guaranteed to be ≥ 1
already, and the next `c.NextID()` call (from the broadcast site) returns
≥ 2 — the literal-id=1 collision is now structurally impossible.

The gate-disabled tail sets `gateCompleted = true` immediately because
there is no `hello_ack` at all in that configuration — id space starts
at id=1 for the first handler reply, and a broadcast claiming id=1 is
not a collision because the gate isn't competing for that id. (This
matches the existing test posture in `dispatch_test.go`, which uses
gate-disabled config.)

This is the minimal external surface; the dispatcher does NOT learn about
`MessagePayload` or `TypeMessage`. Fan-out lives in the wiring closure.

### Component 3: `cmd/pyry/assistant_turn.go` (new file) — the emitter

Lives in `package main` alongside `relay.go`. One unexported type plus
two functions:

- **`assistantTurnEmitter`** — owns one buffered `chan []byte` (size:
  16; bursty PTY chunks but bounded) and one goroutine that drains it.
  - `Enqueue(chunk []byte) { copy + non-blocking send; drop+WARN on full }`
    — the bridge observer call site. Copies `chunk` before queueing (the
    caller's slice is reused).
  - `Run(ctx)` — single goroutine. For each dequeued chunk: read
    `sup.CurrentConversation()`; if empty, drop. Otherwise build a
    `MessagePayload{ConversationID: cursor, MessageID: <fresh UUIDv4>,
    Role: "assistant", Text: string(chunk)}`, marshal, then for each
    `c := range disp.ActiveConns()` build `Envelope{ID: c.NextID(),
    Type: TypeMessage, TS: time.Now().UTC(), Payload: payloadJSON}`
    and call `c.Send(ctx, env)`. Per-conn Send errors logged at
    DEBUG (transport disconnects are normal — same posture as the
    existing forwarder).
  - Returns when ctx done OR when input channel closes.

- **`startAssistantTurnBridge(ctx, sup, bridge, disp, logger) (cleanup func())`**
  — wires it up. Construct the emitter, register
  `bridge.SetOutputObserver(emitter.Enqueue)`, spawn `go emitter.Run(ctx)`.
  Cleanup: `bridge.SetOutputObserver(nil)`, close the input channel, wait
  for Run to return. Idempotent (mirror `startRelay`'s cleanup pattern).

### `cmd/pyry/relay.go` — wire it in

`startRelay`'s signature grows to accept the bridge handle:

```go
func startRelay(
    ctx context.Context,
    logger *slog.Logger,
    instanceName, relayURL, version string,
    allowInsecure bool,
    shutdown context.CancelFunc,
    convReg *conversations.Registry,
    sess handlers.TurnWriter,
    sup *supervisor.Supervisor,         // NEW
    bridge *supervisor.Bridge,          // NEW (nil = foreground; bridge disabled)
) (cleanup func(), err error)
```

Inside `startRelay`, after `d := dispatch.New(...)` and the three
`d.Register(...)` lines, before the three goroutines spawn:

```go
var bridgeCleanup func()
if bridge != nil {
    bridgeCleanup = startAssistantTurnBridge(ctx, sup, bridge, d, logger)
}
```

The combined cleanup chains `bridgeCleanup` before the existing
dispatcher/forwarder/wait drains.

Alternative considered (and rejected): expose
`(*sessions.Session).SetOutputObserver(fn)` and read both bridge+supervisor
through the existing `handlers.TurnWriter`. **Rejected** because the
bootstrap session already wires both into the supervisor.Config the daemon
constructs; passing them directly to `startRelay` is one fewer indirection
and avoids growing the `TurnWriter` interface (which exists to satisfy a
narrow inbound contract).

`main.go`'s call to `startRelay` adds the two arguments — both are already
constructed earlier in the same scope (the bootstrap session's bridge and
supervisor are accessible via the existing pool wiring).

### Data flow

```
claude PTY output
    │
    ▼
ptmx Read  ──┐ (supervisor.go: io.Copy(bridge, ptmx))
             │
             ▼
       Bridge.Write
       ├── observer(chunk) ──> emitter.Enqueue ──> chan []byte
       └── attached out.Write (unchanged)               │
                                                       ▼
                                             emitter.Run goroutine
                                                       │
                                                       ▼
                                             sup.CurrentConversation()
                                              empty? drop : continue
                                                       │
                                                       ▼
                                             build MessagePayload + marshal
                                                       │
                                                       ▼
                                             for c := range disp.ActiveConns()
                                                       │
                                                       ▼
                                             c.Send(ctx, Envelope{
                                                 ID: c.NextID(),
                                                 Type: TypeMessage,
                                                 TS: now,
                                                 Payload: ...,
                                             })
                                                       │
                                                       ▼
                                             dispatch.outbound chan
                                                       │
                                                       ▼
                                             cmd/pyry forwarder
                                                       │
                                                       ▼
                                             conn.Send(routingEnv)
                                                       │
                                                       ▼
                                             transport.Client.Send
                                                       │
                                                       ▼
                                             relay  →  phone
```

## Concurrency model

- **Producer:** the supervisor's PTY-drain goroutine (one per `runOnce`
  iteration). Calls `Bridge.Write`, which calls `observer(p)` =
  `emitter.Enqueue(p)`. `Enqueue` is non-blocking: copy + `select { send :
  default }`. Drop-on-full logs WARN once per drop with `chunk_len` (no
  payload bytes).
- **Consumer:** one long-lived goroutine (`emitter.Run`), started by
  `startAssistantTurnBridge` and reaped on cleanup. Reads from the buffered
  chan, blocks on `c.Send(ctx, env)` per conn — `Send`'s outbound channel
  is the natural backpressure, which is fine on the consumer side because
  it is NOT the PTY-drain.
- **Conn snapshot semantics:** `ActiveConns()` returns a stale snapshot.
  A conn that closes between snapshot and `Send` either has its
  `Send(ctx, env)` complete (frame queued, harmlessly dropped at the
  transport layer per #307's reconnect semantics) or blocks until ctx
  done — bounded by the daemon's shutdown ctx, not indefinite.
- **Cursor read:** `sup.CurrentConversation()` is mutex-guarded under
  `convMu` (leaf-only, #312). The emitter goroutine takes it once per
  chunk, briefly. No new lock-order risk.
- **Lifecycle:** the emitter goroutine is owned by the `startAssistantTurnBridge`
  cleanup. On daemon shutdown, ctx cancels → `c.Send` returns ctx.Err
  → loop body checks ctx → returns. Cleanup chains: clear observer →
  close input chan → wait. Mirror `startRelay`'s pattern. No new lifecycle
  primitive.

**Lock order summary:** no new locks. The bridge's existing `mu` guards
the observer field. The dispatcher's existing `mu` guards the conns map.
The supervisor's existing `convMu` guards the cursor. None nest with each
other; each is held briefly and released before the next is taken.

## Error handling

- **`CurrentConversation() == ""`** — no inbound user-turn has been
  written yet. Drop the chunk silently; log at DEBUG with `chunk_len`.
  Phone will observe nothing (correct — there is no conversation to
  attach the assistant text to). Recovery: implicit, the first inbound
  `send_message` stamps the cursor and subsequent chunks land.
- **`Enqueue` channel full** — supplier outran consumer. Drop the chunk;
  WARN once with `chunk_len`. PTY drain keeps moving (the bridge's
  guarantee). Acceptable v1 behaviour: lossy mirror is better than a
  wedged child; future tickets can introduce coalescing/buffering if
  loss is observed in practice.
- **`json.Marshal(MessagePayload)` fails** — only `string` and `string`
  fields; cannot fail in practice. Defensive WARN + drop. The WARN must
  log only `chunk_len` and `conversation_id` — **NEVER** the marshal
  error's `.Error()` string, since it would echo bytes from the chunk
  if the input contained invalid UTF-8 (json packages quote bad input
  back into error text).
- **`Conn.Send(ctx, env)` returns ctx.Err** — daemon is shutting down.
  Exit the broadcast loop. Subsequent dequeues see ctx done and return.
- **`Conn.Send` returns other err** — today `Send` only returns ctx err
  (its sends-onto-channel path doesn't carry transport errors). Defensive
  DEBUG log; continue the per-conn loop.
- **Bridge cleared mid-broadcast (conn closed):** demux's `routeConn`
  marks `closed = true` under `d.mu`; the closed-conn drop fires on next
  inbound frame. Outbound to a closed conn lands on its still-live
  `outbound` chan (shared) and is forwarded to the transport, which
  drops it on disconnect per #307's semantics. No loss of safety.

## Testing strategy

Three layers, all under `go test -race`.

### Unit: `internal/supervisor/bridge_test.go`

Two new tests:

- **`TestBridge_OutputObserver_InvokedOnWrite`** — register a recorder
  observer (closure that captures incoming chunks into a slice under a
  mutex); call `Write([]byte("hello"))` twice; assert the recorder saw
  both chunks. Asserts the basic tee semantics.
- **`TestBridge_OutputObserver_NilSkipped`** — default state; `Write`
  proceeds normally (no panic, returns `len(p), nil`); verify by writing
  to a `bytes.Buffer` set via `Attach`.

No test for "observer must not block" or "must not panic" — those are
documented contracts, not enforced. The existing `Bridge_Write_NeverReturnsError`
posture stands.

### Unit: `internal/dispatch/dispatch_test.go`

Two new tests:

- **`TestDispatcher_ActiveConns_Snapshot`** (gate-disabled) — feed two
  `RoutingEnvelope`s with distinct `ConnID`s; wait for the demux to
  allocate both `connState`s (the test pattern from the existing
  `two-conns-arrival-order` test); assert `d.ActiveConns()` returns 2
  conns whose `ConnID()` values match the seeded IDs. This pins the
  gate-disabled tail of `runConn` setting `gateCompleted = true`
  immediately.

- **`TestDispatcher_ActiveConns_ExcludesPreGateConn`** (gate-enabled,
  REGRESSION) — configure a `FirstFrame` gate that blocks on a test
  channel (the gate goroutine awaits a signal before returning an
  accept outcome). Feed an inbound `RoutingEnvelope`; await the demux
  alloc as above; while the gate is still blocked, assert
  `d.ActiveConns()` returns ZERO conns (because `gateCompleted` is
  still false even though the per-conn goroutine has already set
  `gateStarted` and entered `runGate`). Release the gate; await
  hello_ack on `d.Outbound()`; assert `d.ActiveConns()` now returns
  ONE conn. This is the regression test for the original spec defect
  identified by code-review (#326): a single-flag implementation
  (set-before-runGate) would return the conn in the first assertion
  and a broadcast could race the literal-id=1 hello_ack. Required.

  Implementation note: the existing `gate_test.go` already has a
  blocking-gate test seam — the new test reuses that pattern; no new
  test helper infrastructure required.

Optionally: a closed-conn variant exercising the gate-reject path
(closed flag set), asserting `ActiveConns()` excludes the closed conn.
Skip unless the gate-reject scaffolding is already test-imported in
`dispatch_test.go`; the `gate_test.go` reject test already pins the
closed-flag mechanism, so the redundancy is low-value.

### Unit: `cmd/pyry/assistant_turn_test.go` (new file)

Stdlib-only test of the emitter wiring:

- **`TestAssistantTurnEmitter_DropsWhenCursorEmpty`** — stub supervisor
  whose `CurrentConversation()` returns `""`; enqueue a chunk; assert
  no `*Conn.Send` was observed (drain the dispatcher's outbound chan
  with a 50 ms deadline).
- **`TestAssistantTurnEmitter_FansOutToAllConns`** — stub two
  `*Conn`s wired to a shared outbound chan; cursor set to a known UUID;
  enqueue one chunk; drain the chan and assert two
  `RoutingEnvelope`s with distinct `ConnID`s, both decoding as
  `Envelope{Type: TypeMessage, Payload: MessagePayload{ConversationID:
  <known>, Role: "assistant", Text: <chunk>}}`.

Use `dispatch.NewTestConn` (the test seam from #319) for the stubbed
`*Conn`s. Bypass `ActiveConns()` by using a small internal seam (or
extract the fan-out into a free function `broadcastTurn(conns []*Conn,
payload ...) error` and unit-test that directly — avoids dispatcher
construction in the unit test).

### E2E: `internal/e2e/relay_assistant_turn_test.go` (new file, `//go:build e2e`)

Mirrors `TestRelay_SendMessage_AckAndPTYDelivery`:

1. `RunBareIn(t, home, "pair", ...)` → `decodePairPayload`.
2. Seed `<home>/.pyry/test/conversations.json` with a known
   `conversation_id`.
3. `StartRotationWithRelay(...)` — reused from #323; no new harness
   helper.
4. Phone hello → hello_ack.
5. Phone send_message → ack (this stamps `Supervisor.CurrentConversation()`
   via the existing #322 path; the assistant-turn bridge depends on this).
6. **Trigger fakeclaude to emit a scripted assistant chunk** — see
   fakeclaude extension below.
7. `phone.Receive(3 * time.Second)` — assert the envelope:
   - `Type == TypeMessage`
   - `InReplyTo == nil` (server-initiated, no request)
   - `ID >= 3` (hello_ack=1, send_message ack=2, so message ≥ 3 — exact
     value depends on observed ordering; assert monotonic-after-ack)
   - `Payload` decodes as `MessagePayload{ConversationID: knownConvID,
     Role: "assistant", Text: knownAssistantText, MessageID: <non-empty,
     ValidID>}`

Three behaviours pinned by step 7: conversation_id round-trip
(send_message in → message out with the same id), per-conn ID stamping
(monotonic above the ack), role labelling (`"assistant"`).

### fakeclaude extension

Add a new env var, `PYRY_FAKE_CLAUDE_ASSISTANT_TRIGGER`, that names a
path watched in parallel with `PYRY_FAKE_CLAUDE_TRIGGER`. When the file
appears:

- Read its content as the assistant text (bounded to a small max — the
  e2e writes < 256 bytes).
- Write it to `os.Stdout`.
- `os.Stdout.Sync()` (best-effort; PTY stdout doesn't always honour
  sync, but pyry's bridge reads in a tight loop so the write reaches
  the bridge promptly regardless).
- `os.Remove` the trigger.

Same posture as the existing rotation trigger:
default-off-when-env-unset, idempotent removal, no behavioural drift
when the e2e doesn't opt in.

The e2e writes the trigger file via `os.WriteFile` after the
send_message ack, then awaits the `message` envelope on the phone side.

## Open questions

1. **Per-conn ID for broadcast — atomic or pre-staged?** Each
   `c.NextID()` is an `atomic.Add`; calling it once per conn per
   message is fine. No question really — flagging just to confirm the
   AC's "outbound `id` is dispatcher-stamped (via `Conn.NextID`)"
   reads as "use the existing `c.NextID()`, no batching." Implementer:
   follow the per-conn shape exactly; do not invent a shared id
   counter.

2. **Drop-on-full backpressure WARN frequency.** A misbehaving claude
   that spams PTY output (or a misbehaving real-claude TUI emitting
   one chunk per ANSI escape) could log WARN every chunk. v1
   acceptable; future ticket can rate-limit. Keep the WARN; do NOT
   suppress.

3. **`Text: string(chunk)`** — the chunk is raw PTY bytes including
   possible ANSI escape codes when paired with real claude. For
   fakeclaude (v1), the bytes are clean. The phone receives raw
   bytes either way; rendering is the phone's problem. Documenting
   here so the future "real-claude assistant-turn parser" ticket
   inherits the contract: the bridge does NOT strip / decode /
   normalise bytes. That's a downstream concern.

4. **Foreground mode.** When `Bridge == nil` (foreground mode), no
   observer hook is available. `startRelay` already early-returns
   when `relayURL == ""`; this ticket inherits that gate via the
   `bridge != nil` check in `startAssistantTurnBridge` wiring.
   Foreground + relay-enabled is a config that doesn't exist in
   production today (the daemon constructs a Bridge whenever it
   runs as a service). If someone hand-wires it, the relay still
   works on the inbound path (`send_message` → PTY) but the outbound
   bridge is silent. Flagged in the doc-comment on `startRelay`.

5. **Order: cursor read vs. chunk dequeue.** The emitter reads
   `sup.CurrentConversation()` AFTER dequeue. If a future change
   wants "the cursor at the time of the chunk's arrival, not at
   the time of the broadcast," the chunk would need to carry the
   cursor through the channel. v1 punt: cursor is read at
   broadcast time. Acceptable because cursor mutation only
   happens on `send_message`, which is request/response — the phone
   knows its own latest send and won't misattribute a message
   envelope's `conversation_id` (the broadcast carries the id
   explicitly anyway).

## Security review

**Verdict:** PASS

**Findings:**

- [Trust boundaries] **SHOULD FIX (documented in design).** PTY output
  → `MessagePayload.Text` → phone is the boundary where bytes leave the
  supervised-process-trust zone and become "phone payload." Bytes flow
  verbatim; no decode, strip, or normalise. Documented in §"Open
  questions" #3 and inherited into the doc-comment on
  `startAssistantTurnBridge`. Phone-side rendering owns escape-aware
  display.
- [Tokens / Secrets] No findings — this slice handles no tokens. The
  `MessageID` UUIDv4 is generated via `conversations.NewID()` which
  uses `crypto/rand`.
- [File operations] No findings — this slice has no production file ops.
  The fakeclaude assistant-trigger file path is operator-supplied via
  env (test harness only) and inherits the existing rotation-trigger
  posture.
- [Subprocess execution] No findings — no new subprocess spawn; the
  ticket consumes the existing supervised claude's stdout via the
  existing PTY-drain goroutine.
- [Cryptographic primitives] No findings — `crypto/rand` via
  `conversations.NewID()` for `MessageID`. No new primitives.
- [Network & I/O] No findings of MUST-FIX class.
  - Per-chunk size: bounded by the supervisor's `io.Copy` buffer
    (32 KiB by default), then by the transport's 1 MiB WS write cap.
    No new explicit cap added; the inherited bounds are sufficient
    for v1.
  - Rate: the emitter's buffered channel (size 16) + drop-on-full
    means flooding manifests as lossy mirror, not unbounded growth
    or PTY wedge. Documented in §"Error handling" and §"Concurrency
    model".
- [Error messages / logs] **MUST-FIX-LEVEL discipline pinned in design.**
  PTY chunk bytes are NEVER logged at any level — not in WARN, not in
  DEBUG, not via wrapped errors. Logs carry only `chunk_len`,
  `conversation_id`, and `message_id`. Error envelopes built by this
  slice carry only static message strings (no chunk bytes echoed).
  This is the same posture as `send_message` (#322) and is pinned in
  the doc-comment on `startAssistantTurnBridge`.
- [Concurrency] **MUST FIX applied during the second architect pass
  (rework after code-review on PR #326).** Three pass-states summarised
  for the audit trail:

  **Pass 1.** Initial design exposed `Dispatcher.ActiveConns()` as a
  simple non-closed-conn snapshot — that would race the auth gate,
  since `relay.AuthenticateFirstFrame` emits hello_ack with literal
  `ID=1` (not via `c.NextID()`) and a broadcast claiming
  `c.NextID() == 1` before the gate's `_ = c.NextID()` advance would
  collide on the wire (two envelopes both stamped id=1, both pushed
  to the shared outbound channel).

  **Pass 2 (defective fix).** Proposed: filter `ActiveConns` on
  `gateRun && !closed` and move the `gateRun = true` assignment under
  `d.mu`. Developer implemented faithfully; code-review (#326) found
  the fix does NOT close the race — `gateRun = true` was set at the
  TOP of `runConn`'s gate path, BEFORE `runGate` runs, so the window
  between "gate started" and "hello_ack published with id=1, NextID
  advanced" remained open. A broadcast that read `gateRun == true` in
  that window would still race to claim id=1 first.

  **Pass 3 (current — actually closes the race).** Split the flag
  into two:

  - `gateStarted` (renamed from `gateRun`) — local single-writer/
    reader on the per-conn goroutine; lock-free; prevents
    double-running the gate. Set at the TOP of the gate path as
    before. Not read by `ActiveConns`.
  - `gateCompleted` — NEW; written under `d.mu` ONLY AFTER
    `runGate` returns on the accept path (i.e. after `_ =
    st.conn.NextID()` has executed) OR immediately on the
    gate-disabled tail (no hello_ack to collide with). Read by
    `ActiveConns` under `d.mu`.

  `ActiveConns` now filters on `gateCompleted && !closed`. The
  happens-before chain is:

  `runGate select-publish hello_ack onto d.outbound`
  → `runGate executes _ = c.NextID()` (counter ≥ 1)
  → `runGate returns`
  → `runConn locks d.mu and sets gateCompleted = true, unlocks`
  → `ActiveConns under d.mu observes gateCompleted == true`
  → `broadcast calls c.NextID()` returning ≥ 2

  The literal-id=1 collision is now structurally impossible: by the
  time `ActiveConns` returns a conn, its NextID counter has already
  advanced past 1. Spec §"Component 2" documents both flags and the
  required `runConn` body sketch; §"Testing strategy" adds
  `TestDispatcher_ActiveConns_ExcludesPreGateConn` as the regression
  test that fails under the Pass 2 implementation and passes under
  Pass 3.
- [Threat model alignment] No new threats introduced relative to
  `docs/protocol-mobile.md` § Security model. Relay-trust posture
  (v1 sees plaintext envelopes) is pre-existing; this slice neither
  worsens nor improves it. Phone-side render-injection of assistant
  text is a phone responsibility, documented as out-of-scope §
  "Open questions" #3. A future ticket may add an assistant-turn
  parser for real claude TUI (vs. fakeclaude's clean per-write
  chunks); that work introduces new trust-boundary considerations
  not in scope here.

**Reviewer:** architect (self-review per `agents/architect/security-review.md`)
**Date:** 2026-05-14

## What's NOT in this slice

- **Owner-conn routing.** Broadcast for v1. Subscription / fan-out by
  conversation_id is a future ticket.
- **Real-claude assistant-turn parser.** The bridge tees raw PTY
  chunks; what counts as a "turn" against the real claude TUI is a
  separate problem.
- **Backpressure tuning.** Fixed buffer-16, drop-on-full WARN. No
  coalescing, no per-conversation queueing.
- **Outbound rate-limiting.** A hostile child that spams PTY can
  flood the phone. Mitigation: trust the supervised process (we
  spawned it). Future tickets may add bounds.
- **`message` echo to other paired devices.** The protocol spec
  allows `Role: "user"` echoes for multi-device awareness; this
  ticket only emits `Role: "assistant"` from the PTY tap. User-turn
  echoes are a separate concern handled by a future hook on the
  `send_message` handler itself.
