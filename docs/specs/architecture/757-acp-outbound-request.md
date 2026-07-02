# Spec #757 — `internal/acp`: outbound-request primitive (agent→client call, id-correlated)

**Ticket:** #757 (split from #746, blocked by #755, sub-issue of epic #600)
**Size:** S — 2 production files modified (`acp.go`, `jsonrpc.go`), 0 new files, **0 new exported types** (one new exported *method* `Transport.Call`; reuses the existing `*Error`), 0 consumer call sites (unused until T7/T8). ~80 production LOC + ~200 test LOC. 5 reject/drop branches (under the 10-branch line).
**Security-sensitive:** No (not labelled). This adds a transport mechanism — id correlation + a pending-call registry — that drives no claude, registers no real ACP method, and makes no trust/dispatch/gate decision. The outbound call is agent-initiated; the response comes from the same client the transport already serves.

## Files to read first

Read these before writing code. Each line says what to extract.

- `internal/acp/acp.go:61-106` — `Transport` struct + `New`. **What to extract:** where to add the three new registry fields (`nextID`, `pendingMu`, `pending`) and the one-line `pending` map init in `New`. Note the existing `writeMu`/`enc` seam already lives here.
- `internal/acp/acp.go:194-209` — `handleLine`'s classification switch, specifically the **response-frame case at :199-208** that today logs-and-drops. **What to extract:** the exact case you replace. `msg.ID != nil && (msg.Result != nil || msg.Error != nil)` is already the correct guard; swap the `Debug` drop for `t.routeResponse(&msg)`. The unknown-id path keeps log-and-drop, now *inside* `routeResponse`.
- `internal/acp/acp.go:286-294` — `writeMessage` + the `writeMu` doc comment. **What to extract:** the write seam `Call` reuses verbatim. `writeMessage(v any) error` already serialises every write under `writeMu`; the outbound request goes through it with **no change to the write path**. Note the comment already names #757 as the sharer.
- `internal/acp/jsonrpc.go:20-52` — `Error`, `rpcError`, `rpcMessage`. **What to extract:** (a) reuse `*Error` as the returned mapped error — do **not** add a new exported error type (Technical Notes); (b) reuse `rpcError{Code,Message,Data}` to decode an inbound error object; (c) add the new **unexported** outbound `request` wire type in this file next to the existing encode structs.
- `internal/acp/acp_test.go:17-52` — the `run`/`runErr` harness + `echoParams`. **What to extract:** this harness is **single-shot and synchronous** (`strings.NewReader` fed whole, `Serve` runs to EOF, then output is parsed). It does **not** fit the outbound tests, which need `Serve` live in a goroutine while the test writes a response *after* observing the request. The Testing strategy below specifies the new live-Serve harness; reuse the diagnostics-sink pattern (`slog.NewTextHandler` over a `bytes.Buffer`) and the JSON-frame assertions.
- `CODING-STYLE.md` §§ "Concurrency", "Testing" — mutex-for-shared-state, channel-for-signalling, `go test -race` is mandatory, table-driven stdlib-only tests, `t.Parallel()`.

## Context

Epic #600 makes `pyry acp` speak the Agent Client Protocol. ACP is **bidirectional**: besides answering host requests (built by #755), the agent issues its *own* requests to the client — `session/request_permission` (T8's permission proxy) and the held `session/prompt` return path (T7). Both need an outbound-request primitive on the transport.

Providing that primitive as part of the transport floor — before any handler ticket lands — means T7/T8 register handlers and issue outbound calls without reopening the transport's read loop. This ticket adds **only the outbound direction**: id generation, a pending-call registry, a blocking outbound call, and routing of inbound *response* lines to their waiters. It drives no claude, registers no real ACP method, and is unused by any consumer until T7/T8 — **the unit tests are the contract.**

#755 deliberately left the seam open: `writeMessage`/`writeMu` is pre-built and documented as "the seam #757's outbound-request writer shares," and `handleLine`'s response-frame case already tolerates a well-formed response line by dropping it, documented as "#757 seam." This ticket fills both.

## Design

### What changes

Three additive pieces, all inside `internal/acp`:

1. **`jsonrpc.go`** — one new **unexported** wire type `request` (the outbound agent→client frame) and one new **unexported** delivery type `callResult`.
2. **`acp.go`** — three new fields on `Transport`; one new exported method `Call`; one new unexported method `routeResponse`; a one-line change to `handleLine`'s response-frame case.
3. **`acp_test.go`** — a live-Serve test harness + the AC-4 result/error tests + the AC-2/AC-3 race/cancellation tests.

### New Transport state (the real cost — first cross-goroutine transport state)

Add to the `Transport` struct:

```go
nextID    atomic.Uint64                 // outbound id counter; Add(1) ⇒ 1,2,3,… never 0
pendingMu sync.Mutex                    // guards pending (leaf lock)
pending   map[uint64]chan callResult    // id ⇒ waiter; buffered cap 1 each
```

`New` initialises `pending: make(map[uint64]chan callResult)`. `nextID` and `pendingMu` are zero-value ready.

`#755`'s transport is single-goroutine and lock-free by design (handler map is write-once-before-Serve). The `pending` registry breaks that: it is **written by the caller's goroutine** (in `Call`) and **read/deleted by the read-loop goroutine** (in `routeResponse`). `pendingMu` is the synchronisation that makes that safe — this is why AC-2 demands `-race`. `nextID` is atomic so id generation needs no lock.

### `callResult` (unexported, `jsonrpc.go`)

```go
type callResult struct {
    result json.RawMessage // set on a success response (nil ⇒ JSON null result)
    err    *Error          // set on an error response; reuses the existing *Error
}
```

Exactly one of the two is meaningful per response; the channel carries one `callResult` then is dropped.

### `request` wire type (unexported, `jsonrpc.go`)

```go
type request struct {
    Jsonrpc string          `json:"jsonrpc"` // always "2.0"
    ID      uint64          `json:"id"`
    Method  string          `json:"method"`
    Params  json.RawMessage `json:"params,omitempty"` // omitted when the caller passes nil
}
```

Id is a JSON number from `nextID`. Per the Technical Notes: inbound requests are answered by echoing the host's id verbatim, so the transport generates no inbound ids and an outbound number cannot collide with one.

### `Call` — the blocking outbound primitive (exported method)

```go
// Call issues a JSON-RPC 2.0 request to the client with a freshly-generated
// id and blocks until the matching response is read, returning the result
// (as raw JSON) or the mapped *Error. ctx cancellation returns ctx.Err() and
// reclaims the pending slot. MUST be called from a goroutine other than the
// one running Serve (inbound handlers dispatch inline on the read loop).
func (t *Transport) Call(ctx context.Context, method string, params any) (json.RawMessage, error)
```

Ordering is load-bearing (state it in the doc comment):

1. **Marshal `params` first** (before touching shared state): `nil` → omit the field; otherwise `json.Marshal`. A marshal error returns immediately — no id burned into a dangling slot. (`json.RawMessage` params round-trip verbatim since it implements `Marshaler`.)
2. `id := t.nextID.Add(1)`.
3. **Register the slot before writing:** `ch := make(chan callResult, 1)`; under `pendingMu`, `t.pending[id] = ch`. Registering *before* the write closes the race where a fast response arrives before the waiter exists.
4. `t.writeMessage(request{...})` — reuses the `writeMu` seam unchanged. On write error: reclaim the slot (under `pendingMu`, `delete`), return the wrapped error.
5. `select { case <-ctx.Done(): reclaim, return ctx.Err(); case r := <-ch: return r.result / r.err }`.

The buffered channel (cap 1) is the correctness core behind AC-3: the read loop's send **never blocks**, even if the waiter has already been reclaimed. See Concurrency model.

### `routeResponse` — deliver an inbound response to its waiter (unexported)

Replaces the `handleLine` response-frame drop (`acp.go:199-208`). The guard stays `msg.ID != nil && (msg.Result != nil || msg.Error != nil)`; the body becomes `t.routeResponse(&msg)`.

`routeResponse(msg *rpcMessage)`:

1. Decode the id: `var id uint64; json.Unmarshal(msg.ID, &id)`. On error (non-numeric / not one we issue) → **unknown id: log Debug + drop** (never a waiter, never a panic).
2. Under `pendingMu`: `ch, ok := t.pending[id]`; if `ok`, `delete(t.pending, id)`. Unlock.
3. `!ok` → **unknown/reclaimed id: log Debug + drop** (this is the *normal* fate of a cancelled call's late response — Debug, not Warn, to avoid log spam; matches the existing drop precedent).
4. Build `callResult`: if `msg.Error != nil`, decode it into `rpcError` and map to `&Error{Code,Message,Data}` (on a malformed error object, synthesize `&Error{CodeInternalError, "malformed error response"}` — deterministic, never blocks); else `result = msg.Result` (may be nil ⇒ JSON null).
5. `ch <- callResult{...}` — the cap-1 buffer guarantees this send returns immediately.

### AC → design map

| AC | Satisfied by |
|---|---|
| AC-1 outbound request + block for matching-id result/error | `Call` steps 1-5; `routeResponse` step 4 |
| AC-2 race-safe distinct ids; unknown id logged + dropped, no panic | `nextID.Add`, `pendingMu`, `routeResponse` steps 1+3 (`-race` test) |
| AC-3 ctx cancel returns ctx.Err(), reclaims slot, late response dropped | `Call` step 5 `<-ctx.Done()` reclaim + cap-1 buffer + delete-under-mutex |
| AC-4 result test + mapped-error test | `routeResponse` step 4 (Testing strategy scenarios) |
| AC-5 no claude spawned; `make check` green | additive to `internal/acp`; no substrate literals |

## Concurrency model

- **Two goroutines now touch `Transport` state** (was one). The read-loop goroutine (`Serve`) runs `routeResponse`; a **separate** caller goroutine runs `Call`. Both mutate `pending` only under `pendingMu`. `nextID` is atomic.
- **`writeMu` becomes genuinely load-bearing** (was redundant under #755's single writer): a concurrent `Call` and an inbound `dispatchRequest` reply can now race on `w`. Both already funnel through `writeMessage` under `writeMu` — **no change needed**; this is exactly the seam #755 pre-built.
- **The read loop must never stall on a waiter.** Delivery in `routeResponse` step 5 sends on a **buffered cap-1** channel: exactly one response exists per id, so the buffer always has room and the send returns immediately — whether the waiter is still blocked, has been reclaimed (cancellation), or the id was unknown (which never reaches the send). This is why AC-3 says "no goroutine or pending-slot leak."
- **Reclaim races resolve under `pendingMu`.** Cancellation-delete and delivery-lookup+delete are both under the mutex. Whichever wins: if the read loop delivers first, the caller's cancel-path `delete` is a no-op and the buffered value is GC'd unread (dropped); if the caller reclaims first, the read loop's lookup misses → unknown-id drop. No double-delivery, no leak, either order.
- **No cross-goroutine byte aliasing.** `msg.Result`/`msg.Error` are `json.RawMessage`; `json.RawMessage.UnmarshalJSON` **copies** its input into a fresh backing array, so these fields are already independent of the `bufio.Scanner` buffer and are safe to hand to the caller goroutine — no extra copy required. (`-race` confirms.)

### Shutdown

`Call`'s liberation mechanism is its own `ctx`. If `Serve` returns (EOF/error) while a `Call` is blocked and its `ctx` is never cancelled, the call blocks until `ctx` fires — consumers (T7/T8) always pass a session-lifetime context. `Serve`-exit does **not** drain pending waiters; that is deferred (Open questions) — no observed need, and the caller-context covers it.

## Error handling

- **Unknown / undecodable id** → log Debug, drop; never delivered, never panics (AC-2).
- **Mapped error response** → decode `rpcError`, return `*Error` (reuses the existing type — AC-1, AC-4). Malformed error object → synthesized `*Error{CodeInternalError,…}` (never blocks the read loop).
- **Marshal-params failure** → `fmt.Errorf("acp: marshal params: %w", err)` from `Call` before any slot is registered.
- **Write failure** → reclaim the slot, return the wrapped write error from `Call`.
- **ctx cancellation** → `ctx.Err()` from `Call`, slot reclaimed (AC-3).
- **Diagnostics isolation** (unchanged discipline): drops are logged to `t.log` (stderr) only — never onto `w`, never the frame body.

## Testing strategy

Same-package `acp_test.go`, stdlib `testing` only, `go test -race`. The existing `run`/`runErr` harness is single-shot and cannot drive an outbound call — add a **live-Serve harness**:

- Reader = the read end of an `io.Pipe` (the test writes response lines into the write end *after* observing the request).
- Writer = a synchronising sink the test reads request frames from (an `io.Pipe` write end drained by a `bufio.Scanner` on its read end, or an equivalent). The test parses the outbound request off `w` to learn the generated `id`, then crafts a response carrying that id.
- `Serve` runs in its own goroutine; the test issues `Call` from another goroutine (or the test goroutine while a helper drives Serve). Close the reader to end `Serve` in teardown.

Scenarios (bullet-pointed; developer writes them in the project idiom):

- **AC-4 result path.** Start Serve; `Call(ctx, "session/x", params)` in a goroutine; read the request off `w`, assert it is a well-formed JSON-RPC request with a numeric `id` and the given method/params; write `{"jsonrpc":"2.0","id":<that id>,"result":{…}}` into the reader; assert `Call` returns the matching result and nil error.
- **AC-4 error path.** As above but feed `{"jsonrpc":"2.0","id":<that id>,"error":{"code":-32602,"message":"bad"}}`; assert `Call` returns a nil result and an `*Error` whose `Code`/`Message` (and `Data` when present) match (`errors.As` to `*Error`).
- **AC-2 race safety.** Launch N concurrent `Call`s; collect their request ids off `w`; assert all ids are **distinct**; feed each a response whose result encodes its own id; assert every call resolves to *its own* response. Must pass under `-race`.
- **AC-2 unknown id.** With no outstanding call (or an id never issued), feed `{"jsonrpc":"2.0","id":999999,"result":{}}`; assert nothing is delivered, `Serve` keeps running (a subsequent real call still resolves), no panic, and the diagnostics sink records a drop.
- **AC-3 cancellation.** Issue `Call` with a cancellable ctx; cancel before feeding any response; assert `Call` returns `ctx.Err()`. Then feed a *late* response with that id; assert it is dropped (no panic; a fresh call with a new id still works) — proves the slot was reclaimed and no waiter lingers.
- **Malformed error object** (optional, defensive): feed a response whose `error` is not a valid error object; assert `Call` returns a non-nil `*Error` and does not hang.

`make check` (vet, race, staticcheck, substrate-guard) must be green (AC-5). Substrate-guard stays trivially green — no claude-TUI literals added.

## Open questions

- **Serve-exit does not drain pending waiters.** Deferred: consumers always supply a governing `ctx`; no observed need for the transport to fail-fast pending calls on `Serve` return. If a later ticket needs it, the read loop can range over `pending` and deliver a sentinel error on exit — additive, no wire change.
- **Handler re-entrancy (T8).** Whether an inbound handler may itself issue a blocking `Call` (it runs inline on the read loop, which would then be blocked on a response only that same loop can read → deadlock) is explicitly a **consumer-ticket** concern. This ticket documents the "call from a different goroutine" constraint in `Call`'s doc comment and its tests honour it; it does not enforce it.
- **Id wraparound.** `uint64` from `nextID` will not wrap in any realistic session; no reuse guard is warranted (evidence-based — no observed failure).
