# Spec #765 — Held/deferred inbound-response primitive in the ACP transport

**Ticket:** #765 — feat(acp): held/deferred inbound-response primitive in the ACP transport
**Size:** S (confirmed — not downgraded to XS; see § Sizing)
**Security-sensitive:** No (label absent; behavior-preserving transport plumbing — no host content parsed, no routing/gate decision on untrusted input). Security-review step skipped.
**Package:** `internal/acp` only. No cross-package coordination.

---

## Files to read first

- `internal/acp/acp.go:221-244` — `dispatchRequest`. The **defer seam**: this is where a handler's return currently becomes a written response. The whole change lives here plus a new sibling file.
- `internal/acp/acp.go:361-405` — `writeSuccess` / `writeError` / `writeErrorWithData` / `reply` / `writeMessage`. The **serialized write path** (`writeMu`). The deferred resolve MUST reuse `writeSuccess` / `writeErrorWithData`; do **not** add a second write path.
- `internal/acp/acp.go:53-90` — `Handler` type + `Transport` struct. The contract to **preserve**: `Handler` stays `func(ctx, params) (any, error)`. Note `writeMu` / `pendingMu` are documented **leaf locks** (never held across a handler call or a read) — the responder guard must keep that discipline. Do **not** overload the outbound `pending` map (acp.go:86) for inbound holds.
- `internal/acp/acp.go:171-219` — `handleLine`. Shows the request-vs-notification classifier (`dispatchRequest` reached only when `msg.Method != nil && msg.ID != nil`, so a deferred request always has a non-nil id) and the **scanner-buffer lifetime** note that drives the id-copy rule below.
- `internal/acp/jsonrpc.go:8-39` — error codes + `Error` + `NewError`. `ReplyError` takes `*Error`.
- `internal/acp/jsonrpc.go:41-97` — `rpcMessage` / `successResponse` / `errorResponse` / `rpcError`. Wire shapes and the id-echo contract (`idOrNull`).
- `internal/acp/acp_test.go:17-56` — `run` / `runErr` single-shot harness + `echoParams`. Reused for the defer-suppression test (Serve runs to EOF; a deferred-but-unresolved request emits no frame).
- `internal/acp/acp_test.go:412-523` — `syncBuffer`, `liveTransport`, `newLiveTransport`, `nextRequest`, `feedResp`, `goCall`. The **cross-goroutine harness** the resolve-later / read-loop-alive tests reuse (Serve on its own goroutine over `io.Pipe`s). `feedResp` writes a line onto the transport's reader — it works for inbound **request** lines too, not just responses. A small `nextResponse` helper (read one line off `reqR`, parse as a response frame) is the only harness addition needed.
- `cmd/pyry/acp.go:106-110, 136-137, 177, 221` — the three **existing** sync handlers (`session/new`, `session/load`, `session/cancel`) registered on the transport. Proof that the `Handler` signature must stay unchanged: any signature change fans out to these three plus ~15 test handlers. This spec keeps them byte-identical.
- `CODING-STYLE.md` §§ Concurrency, Testing — leaf-lock discipline, `atomic` for shared flags, table-driven, `t.Parallel()`, `go test -race`.

---

## Context

**Epic #600 — `pyry acp` as a thin adapter over the shared remote-head core.** An ACP turn is one `session/prompt` request that streams notifications and then returns a `stopReason`. The adapter must hold the in-flight `session/prompt` call open for the whole turn and resolve it later — from the outbound event-stream goroutine, not the read loop.

The transport as built (#755) dispatches every inbound request **inline on the single read-loop goroutine**: `Serve → handleLine → dispatchRequest` calls the handler synchronously and writes the response the instant the handler returns. `Handler` is `func(ctx, params) (result any, err error)` — there is no way to say "not yet." So "hold the call open" today has only two (wrong) implementations: **block** the handler (stalls the read loop for the whole turn — no second prompt or `session/cancel` can be read) or **return** (resolves immediately, not held). The outbound `pending` map is for `Call`s only and does not apply to inbound holds.

This ticket adds the missing capability: an inbound handler can **defer** its response, and any goroutine can **resolve** it later through the existing serialized write path. Pure transport plumbing — no session semantics, no host content interpreted. Consumed by #749 (the `session/prompt` handler, which defers and stashes the responder) and T7 #751 (which resolves it with the `stopReason` on `TurnEnd`); neither can be built without it.

---

## Design

### Decision: context-injected responder + `ErrDeferred` sentinel — keep `Handler` unchanged

The handler needs a handle (request id + a way to write through `writeMu`) to resolve later; only the transport can mint it. Two ways to hand it over:

1. **Change the `Handler` signature** to `func(ctx, params, *Responder) (any, error)`. Rejected: fans out to the 3 merged handlers in `cmd/pyry/acp.go` + ~15 inline test handlers (~18 sites, all forced to accept a param they ignore). That is exactly the "mechanical edits" cascade the sizing red lines flag, and it makes a 1-concept change touch two packages.
2. **Inject the responder via `context.Context`** (request-scoped values are the idiomatic use of `context`, per `net/http`) and signal defer with a sentinel error. Chosen: **fully additive** — `Handler`, `Register`, and every existing handler/test stay byte-identical. Only new code is added; only the one method that needs deferral opts in.

### Single answer-authority per id

`dispatchRequest` builds one `*Responder` per request and routes **its own** synchronous success/error path through that responder too (not through `writeSuccess`/`writeError` directly). The responder's once-guard therefore becomes the **single authority** that answers a given id — sync path, deferred path, and any accidental mix all funnel through one guarded resolve. "Exactly one frame per id" is then a structural invariant, not a property that depends on handler discipline. Existing wire output is unchanged (the responder's methods call the same `writeSuccess`/`writeErrorWithData`).

### New surface (all in `internal/acp`, new file `responder.go`)

```go
// Sentinels.
var ErrDeferred = errors.New("acp: response deferred")          // handler → dispatchRequest: don't write now
var ErrAlreadyResolved = errors.New("acp: response already resolved") // 2nd resolve → returned, never a 2nd frame

// Responder answers exactly one inbound request id, once, from any goroutine.
type Responder struct { /* t *Transport; id json.RawMessage (owned copy); done atomic.Bool */ }

func (r *Responder) Reply(result any) error       // success frame; guarded → ErrAlreadyResolved on 2nd call
func (r *Responder) ReplyError(e *Error) error    // error frame; guarded; nil e → generic internal error

// ResponderFrom returns the responder dispatchRequest injected, or nil if none
// (e.g. a notification handler, or a ctx not from dispatchRequest). Consumers nil-check.
func ResponderFrom(ctx context.Context) *Responder
```

- **`Reply(result any)`** — `done.CompareAndSwap(false, true)` gate; on win, `t.writeSuccess(r.id, result)` (which already falls back to an internal-error frame on a marshal bug — still one frame); on loss, return `ErrAlreadyResolved`, write nothing.
- **`ReplyError(e *Error)`** — same gate; on win, `t.writeErrorWithData(r.id, e.Code, e.Message, e.Data)`. `e == nil` is a consumer bug: resolve as `CodeInternalError`/"internal error" rather than deref-panic (preserves never-panic + exactly-one-frame).
- **`ResponderFrom`** — unexported zero-size context key type (`type responderKey struct{}`); `ctx.Value(responderKey{})` type-asserted to `*Responder`, nil on miss.

### `dispatchRequest` change (the only edit to `acp.go`)

Signature and classifier unchanged. After looking up the handler, build the responder, inject it, dispatch, then branch on the sentinel:

- Build `resp` capturing an **owned copy** of the id (see id-copy rule) and a fresh `done` flag; `ctx = context.WithValue(ctx, responderKey{}, resp)`.
- `result, err := h(ctx, msg.Params)`.
- `errors.Is(err, ErrDeferred)` → **return**, write nothing (handler owns `resp`, resolves later).
- other `err != nil` → same mapping as today (`errors.As` `*Error` → `resp.ReplyError(rpcErr)`; else log the detail at Warn and `resp.ReplyError(NewError(CodeInternalError, "internal error"))`).
- else → `resp.Reply(result)`.

The method-not-found early return (`writeError` before a handler exists) stays as-is — no responder is built when there is no handler.

### id-copy rule (correctness pin)

The responder holds the id until a resolve that may happen many `Scan`s later, long after `dispatchRequest` returns. `scanner.Bytes()` is only valid until the next `Scan` (see the `handleLine` note). So the responder MUST capture an **owned copy** of the id bytes — `append(json.RawMessage(nil), msg.ID...)` — never alias `msg.ID`. The synchronous path is unaffected (it resolves before the next `Scan`), but the copy is unconditional so both paths share one construction. `msg.ID` is guaranteed non-nil here (classifier gate), so the copied id is always a real id and `idOrNull` echoes it verbatim.

### Data flow

```
read loop (Serve):  handleLine → dispatchRequest
                       build *Responder{id-copy, done=false} → ctx
                       h(ctx, params)
                         ├─ returns (result,nil)      → resp.Reply(result)        → one frame, inline
                         ├─ returns (_, *Error)        → resp.ReplyError(*Error)    → one frame, inline
                         ├─ returns (_, plain err)     → log; resp.ReplyError(internal) → one frame, inline
                         └─ returns (_, ErrDeferred)   → return; NO frame; read loop scans next line
                                  (handler stashed resp via ResponderFrom)

later, any goroutine (e.g. T7 outbound stream on TurnEnd):
                       resp.Reply(stopReason)  ── done CAS ──► writeSuccess(id) ─ writeMu ─► one frame
                       resp.Reply(...) again   ── CAS loses ─► ErrAlreadyResolved, no frame
```

---

## Concurrency model

- **No new goroutines.** The primitive adds cross-goroutine *state* (a responder resolved off the read loop), not new goroutines.
- **Write serialization:** every resolve goes through `writeSuccess` / `writeErrorWithData` → `reply` → `writeMessage` under the existing `writeMu`. A resolve racing the read loop's own dispatch write is serialized there — the invariant `writeMu` already guarantees. No new lock.
- **Resolve-once:** `Responder.done atomic.Bool` with `CompareAndSwap(false, true)`. Leaf-level, held across nothing. Two goroutines racing to resolve the same id: exactly one wins the CAS and writes; the loser gets `ErrAlreadyResolved`. No mutex needed.
- **Leaf-lock discipline preserved:** the `done` CAS is not held across the write; `writeMu` is taken *inside* `writeMessage` after the CAS returns. No lock ordering introduced.
- **Read loop liveness:** a deferring handler returns promptly (after stashing `resp`), so `dispatchRequest` returns and `Serve` proceeds to the next `Scan`. Holding a request open no longer blocks classification/dispatch of later frames — the core behavioral fix.

---

## Error handling / edge cases

- **Double-resolve** → second `Reply`/`ReplyError` loses the CAS → returns `ErrAlreadyResolved`, writes no frame. (AC: exactly one frame per id.)
- **Resolve-after-teardown** (resolve after `Serve` returned / reader EOF'd): **no dedicated teardown state.** The `Transport` and `writeMu` outlive `Serve`; `writeMessage` → `enc.Encode` on a closed/dead writer returns an error that `reply` logs at Warn and drops. Combined with the once-guard, a post-teardown resolve is at most one *attempted* frame that fails silently — never a panic, never a second frame. Per Evidence-Based Fix Selection, do **not** add a speculative `closed` flag for an unobserved failure; the existing write path already tolerates a dead writer.
- **`ReplyError(nil)`** → map to `CodeInternalError`/"internal error" (consumer bug; never deref-panic).
- **Handler returns `ErrDeferred` but never stashes `resp`** → the request hangs (no frame ever). This is a consumer contract (#749's concern), not the primitive's to police. The sentinel means "I have taken ownership and will resolve."
- **Diagnostics isolation preserved:** the primitive logs nothing containing frame bodies; the only new log is the existing Warn on a plain handler error (id/detail to stderr, never the wire) and the existing write-failure Warn.

---

## Testing strategy

Add to `internal/acp/acp_test.go`. Table-driven where natural; `t.Parallel()`; the whole package already runs under `-race` in CI (`make check`). Scenarios (write bodies in the project idiom — do not paste these as code):

1. **Defer suppresses the synchronous frame** (single-shot `run`). Register a handler that pulls `ResponderFrom(ctx)`, stashes it, returns `ErrDeferred`. Assert the writer output has **no** frame for that request (Serve runs to EOF; the deferred request is never resolved). Also assert `ResponderFrom` returned non-nil.
2. **Resolve later from another goroutine writes exactly one correct frame** (`liveTransport`). Handler stashes `resp` into a test-owned channel and returns `ErrDeferred`; feed the request via `feedResp`. From the test goroutine (≠ Serve's read loop), call `resp.Reply(...)`; read one frame via a new `nextResponse` helper; assert it echoes the request id and carries the expected `result` (and the error path via `resp.ReplyError` echoing id + code).
3. **Read loop stays alive while a request is held** (`liveTransport`). Register `hold` (defers, stashes resp) and `quick` (replies synchronously). Feed request #1 (`hold`), then request #2 (`quick`); assert #2's response is written and correct **before** the test resolves #1 — proving classification/dispatch continued while #1 was deferred. Then resolve #1 and assert its frame.
4. **Exactly one frame per id on double-resolve.** After a first resolve writes one frame, a second `resp.Reply`/`resp.ReplyError` returns `ErrAlreadyResolved` and writes no additional frame. Assert exactly one frame on the wire and the returned sentinel.
5. **Resolve-after-teardown is safe** (optional but cheap). Capture a `resp`, let Serve reach EOF (harness cleanup EOFs the reader), then resolve; assert no panic and no second frame (a best-effort write may be logged-and-dropped).
6. **`-race` coverage** is provided by scenarios 2–4 (concurrent resolve vs. read-loop dispatch/writes) run under the package's existing `-race` gate; scenario 3 is the explicit concurrent-write case.

Existing tests (sync request/response, error mapping, error-data passthrough, notification-no-response) must pass **unchanged** — the routing of the sync path through the responder produces identical wire output.

---

## Sizing

- **Production:** `acp.go` `dispatchRequest` edit (~10 lines) + new `responder.go` (~55 lines incl. doc comments). 2 production files, both `internal/acp`. New exported types: **`Responder`** (1) — well under 5.
- **Tests:** ~110 lines across 5 scenarios + a ~10-line `nextResponse` helper.
- **Total:** ~185 lines. Under S (≤400) and the split line (≤600).
- **Edit fan-out:** none. `Handler`/`Register` unchanged; the 3 merged handlers + all test handlers untouched. `codegraph`/grep confirm `dispatchRequest` is called only from `handleLine` (internal). Purely additive → size by lines. **No split.**
- **File-overlap:** no in-flight `origin/feature/*` branch touches `internal/acp/*` (checked). No block needed.
- **Not XS:** the mechanism is small, but 5 test scenarios including two cross-goroutine `-race` cases (harness orchestration + timing) carry real assertion-debugging cost. S is the honest call; PO's `size:s` confirmed.

---

## Open questions

- **`ReplyError(nil)` policy** — spec maps nil → generic internal error to preserve never-panic. If the developer prefers a documented panic for a clear programmer bug, either is acceptable; the wire-safety property (one frame, no panic on the resolve path) is the binding constraint.
- **`responder.go` vs folding into `acp.go`** — recommended as a new file for focus; folding the ~55 lines into `acp.go` is acceptable and keeps the file count at 1. Either satisfies the gate.
