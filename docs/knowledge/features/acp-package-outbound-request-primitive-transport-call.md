# Outbound-request primitive (`Transport.Call`) (#757)

ACP is bidirectional: besides answering host requests, the agent issues its own
request to the client. `Call` is a textbook JSON-RPC **pending-call registry** —
the transport's first cross-goroutine state.

### The registry

Three fields on `Transport`, all touched only under the discipline below:

```go
nextID    atomic.Uint64              // Add(1) ⇒ 1,2,3,… never 0. Lock-free id gen.
pendingMu sync.Mutex                 // guards pending. Leaf lock.
pending   map[uint64]chan callResult // id ⇒ waiter's cap-1 channel
```

`pending` is **written by the caller's goroutine** (in `Call`) and
**read/deleted by the read-loop goroutine** (in `routeResponse`) — the
synchronisation `pendingMu` provides is the real cost of #757, and is why AC-2
demands `-race`. `nextID` is atomic so id generation needs no lock. **No lock
nesting:** `pendingMu` and `writeMu` are never held simultaneously.

**Id namespace.** Outbound ids come from `nextID`. Inbound requests are answered
by echoing the host's id verbatim, so the transport generates no inbound ids and
an outbound id cannot collide with one.

### `Call(ctx, method, params) (json.RawMessage, error)`

Ordering is load-bearing (documented at the call site):

1. **Marshal `params` first**, before touching shared state — a marshal failure
   (`fmt.Errorf("acp: marshal params: %w", …)`) returns immediately, so no id is
   burned into a dangling slot. `nil` params → the field is omitted from the
   wire; a `json.RawMessage` round-trips verbatim.
2. `id := nextID.Add(1)`.
3. **Register the waiter before writing** — `ch := make(chan callResult, 1)`;
   under `pendingMu`, `pending[id] = ch`. Registering before the write closes the
   race where a fast response arrives before the waiter exists.
4. `writeMessage(request{...})` — reuses the `writeMu` seam **unchanged**. On a
   write error: reclaim the slot, return the wrapped error.
5. `select` on `ctx.Done()` (→ reclaim slot, return `ctx.Err()`) and the waiter
   channel (→ return `r.result` or `r.err`).

**`Call` MUST be issued from a goroutine other than the one running `Serve`.**
Inbound handlers dispatch inline on the read loop, so a `Call` that blocked that
loop would deadlock — only the read loop reads the response `Call` awaits. This
is documented in `Call`'s doc comment; enforcing it (the handler re-entrancy
question) is a T8 consumer concern, explicitly out of scope.

### `routeResponse` — delivery on the read loop (must never block)

Replaces #755's response-frame log-and-drop. On the read-loop goroutine:

1. Decode the id; non-numeric / out-of-range → **unknown id: log Debug + drop**.
2. Under `pendingMu`: look up `ch`, `delete` if present.
3. `!ok` → **unknown or already-reclaimed id: log Debug + drop** — the *normal*
   fate of a cancelled call's late response (Debug, not Warn, to avoid log spam).
4. Build `callResult`: an error frame → `&Error{Code,Message,Data}` (a malformed
   error object synthesises `&Error{CodeInternalError,"malformed error response"}`
   — deterministic, never blocks); a success frame → `result = msg.Result` (may
   be JSON null).
5. `ch <- r` — the **cap-1 buffer guarantees this send returns immediately**.

### Why the read loop never stalls (AC-3)

- **Cap-1 waiter channel.** Exactly one response exists per id, so the buffer
  always has room; `routeResponse`'s send returns immediately whether the waiter
  is still blocked, has been reclaimed (cancellation), or the id was unknown
  (which never reaches the send). The read loop is never stalled by an absent or
  slow waiter — this is the "no goroutine or pending-slot leak" guarantee.
- **Idempotent reclaim resolves every cancel-vs-deliver ordering.** Both the
  cancellation-`delete` and the delivery-`lookup+delete` run under `pendingMu`.
  If the read loop delivers first, the caller's cancel-path `delete` is a no-op
  and the buffered value is GC'd unread; if the caller reclaims first, the read
  loop's lookup misses → unknown-id drop. No double-delivery, no leak, either
  order.
- **No defensive copy needed across goroutines.** `json.RawMessage.UnmarshalJSON`
  copies its input into a fresh backing array, so `msg.Result` is already
  independent of the reused `bufio.Scanner` buffer and is safe to hand to the
  caller goroutine (`-race` confirms).

### Shutdown

`Serve`-exit does **not** drain pending waiters. If `Serve` returns while a
`Call` is blocked and its `ctx` never fires, the call blocks until the ctx does —
consumers (T7/T8) always pass a session-lifetime context, so there is no observed
need to fail-fast pending calls on `Serve` return. If a later ticket needs it,
the read loop can range over `pending` and deliver a sentinel error on exit —
additive, no wire change.

Full per-ticket detail: [`codebase/757.md`](../codebase/757.md).
