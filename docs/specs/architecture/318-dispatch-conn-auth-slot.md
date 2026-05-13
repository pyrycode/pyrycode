# #318 — `dispatch`: per-conn auth slot on `*dispatch.Conn`

## Files to read first

- `internal/dispatch/dispatch.go:62-110` — `Conn` struct, `ConnID`, `NextID`, `Send`, `Reply`. The auth slot lives here.
- `internal/dispatch/dispatch.go:155-181` — `FirstFrameGate` / `FirstFrameOutcome` contract. New `Device` field lands on `FirstFrameOutcome`.
- `internal/dispatch/dispatch.go:329-402` — `routeConn`, `runConn`, `runGate`. `setAuth` call slots into `runGate`'s accept-and-continue branch (next to the existing `NextID()` advance).
- `internal/dispatch/dispatch.go:201-214` — `connState` (`gateRun`, `closed`). Single-writer-per-conn invariant the auth slot relies on.
- `internal/dispatch/gate_test.go:53-110` — `TestFirstFrameGate_Accept` is the template for the new "accept populates slot" test.
- `internal/dispatch/gate_test.go:112-156` — `TestFirstFrameGate_Reject` is the template for the new "close-intent does not populate slot" test (it already exercises the close-intent path and asserts the second frame is dropped).
- `internal/relay/auth.go:44-47` — `AuthOutcome`. New `*devices.Device` field on accept.
- `internal/relay/auth.go:77-119` — `AuthenticateFirstFrame`. The `device, ok := reg.Validate(token)` result on line 90 is what flows into `AuthOutcome.Device` on the accept branch (lines 92-104). Reject / malformed paths leave it nil.
- `internal/relay/auth_test.go:66-170` — existing accept/reject/malformed coverage. Must continue to pass; one new assertion on `outcome.Device` on the accept branch.
- `cmd/pyry/relay.go:24-39` — `authGate` closure. One added field in the accept-branch literal: `Device: outcome.Device`.
- `internal/devices/device.go:24-43` — `Device` shape (the trusted snapshot handlers will read via `Conn.Auth()`).

## Context

`relay.AuthenticateFirstFrame` already validates the phone's token against `*devices.Registry` and obtains the matched `*devices.Device` on accept. Today that pointer is used to log `device_name` and is then discarded. `AuthOutcome` carries only `{Response, CloseConn}` back to the gate closure in `cmd/pyry/relay.go`, which in turn produces a `dispatch.FirstFrameOutcome` that carries `{Response, CloseConn, Code, Err}`. Verb handlers running on the per-conn goroutine cannot ask "which device authenticated this conn?" — they would have to re-call `reg.Validate(env.Token)` on the second frame, except `env.Token` is only prepended by the relay on the first frame, so re-validation isn't even possible.

This slice introduces the seam once: a per-conn auth slot on `*dispatch.Conn`, populated by the dispatcher from the gate's outcome on the accept path, and never on reject, malformed, or close-intent paths. No verb handler is registered here. Slice B (`register_push_token` rewrite, separate ticket) consumes the seam.

## Design

### Package boundary

The dispatcher gains an import edge to `internal/devices`. Two reasons this is the right call:

- `internal/devices` is a leaf package (its own imports are stdlib-only: `crypto/sha256`, `crypto/subtle`, `encoding/hex`, `time`, plus the sibling `Registry` type which also imports only stdlib + `os`). No cycle risk; the dispatcher's existing import set (`encoding/json`, `errors`, `log/slog`, `sync`, `sync/atomic`, `time`, `internal/protocol`) stays clean.
- The type-erased alternative (`any` on `FirstFrameOutcome.Device` and `Conn.Auth() any`) forces every verb handler to type-assert on read — a sharp edge that handler authors will get wrong at least once. Returning a concrete `*devices.Device` keeps the API self-documenting and lets the compiler enforce the contract.

### `*dispatch.Conn` — the slot

```
type Conn struct {
    id       string
    nextID   atomic.Uint64
    outbound chan<- protocol.RoutingEnvelope
    auth     *devices.Device   // new; set once by the dispatcher on accept
}

// Auth returns the authenticated device snapshot for this conn, or nil
// if the first-frame gate has not yet accepted on this conn (the
// gate-disabled test path or a pre-accept frame).
//
// Safe to call from any handler running on the per-conn goroutine:
// the dispatcher writes the slot exactly once, before dispatching the
// second-and-later frames through the handler table. Reads happen-
// before any handler invocation by Go's memory model (same goroutine,
// sequential).
func (c *Conn) Auth() *devices.Device { return c.auth }

// setAuth is the dispatcher-only seam. Verb handlers cannot reach it
// (unexported, same-package use). Called at most once per conn, on
// the per-conn goroutine, from runGate's accept branch — single-
// writer invariant matches NextID's atomic.
func (c *Conn) setAuth(d *devices.Device) { c.auth = d }
```

No mutex, no atomic. Same justification as the existing pattern: the per-conn goroutine is the only writer; reads occur strictly after the write because both happen on the same goroutine in the order (gate runs → setAuth → response forwarded → for-loop iterates → handler runs). The race detector is satisfied (no cross-goroutine access).

### `relay.AuthOutcome` — the carrier

```
type AuthOutcome struct {
    Response  protocol.RoutingEnvelope
    CloseConn bool
    Device    *devices.Device   // new; non-nil only on accept
}
```

`AuthenticateFirstFrame`'s accept branch sets `Device: device` (from the existing `reg.Validate` return on line 90). Reject branch and the `ErrMalformedHelloFrame` early return both leave `Device` zero (nil) — by virtue of returning `AuthOutcome{...}` literals that don't set it.

`device.Name` continues to be logged on accept; the new field does not change log policy. `device` is never logged on reject or wrapped into the returned error (existing invariant — name-enumeration probes stay defended).

### `dispatch.FirstFrameOutcome` — the gate's verdict

```
type FirstFrameOutcome struct {
    Response  protocol.RoutingEnvelope
    CloseConn bool
    Code      uint16
    Err       error
    Device    *devices.Device   // new; non-nil only on accept
}
```

Contract documented inline: `Device` populated iff `Err == nil && !CloseConn`. The dispatcher MUST NOT call `setAuth` on the close-intent branch or the `Err` branch — those paths leave the slot nil even if a buggy gate populates `Device`.

### `cmd/pyry/relay.go` — the wiring

The existing `authGate` already constructs the `FirstFrameOutcome` literal on the accept branch (`out := dispatch.FirstFrameOutcome{Response: outcome.Response}`). One field added:

```
out := dispatch.FirstFrameOutcome{
    Response: outcome.Response,
    Device:   outcome.Device,   // nil-safe: relay leaves it nil on reject; gate path here is accept
}
if outcome.CloseConn { ... }    // close-intent branch: Device stays as set, but
return out                       // dispatcher ignores Device when CloseConn==true
```

Note: by the contract above, the dispatcher ignores `Device` on the close-intent and `Err` branches. The closure does not need to nil it out explicitly — defence in depth lives on the consumer (dispatcher), not the producer (gate). The current literal already places `Device: outcome.Device` only inside the accept-branch construction, so structurally the close path never carries a device anyway (relay.AuthOutcome.Device is nil on reject).

### Dispatcher integration — `runGate`

Existing `runGate` body (dispatch.go:377-402) accepts an outcome, handles `Err` and `CloseConn` early, then on the accept-and-continue branch advances `NextID()` past the gate's `hello_ack`. The auth slot writes here, before the NextID advance and before the response is published. Pseudocode (diff against existing):

```
outcome := d.cfg.FirstFrame(ctx, routing)
if outcome.Err != nil { ... return false }
resp := outcome.Response
if outcome.CloseConn {
    resp.CloseCode = outcome.Code
    // publish resp; return true. NEVER call setAuth here.
} else {
    st.conn.setAuth(outcome.Device)   // NEW — before publishing
    // publish resp
    _ = st.conn.NextID()              // existing: advance past hello_ack (id=1)
}
return outcome.CloseConn
```

Ordering rationale: setAuth before publish (and before NextID advance) is irrelevant for correctness — the next reader is the next iteration of `runConn`'s for-loop, which only runs after `runGate` returns. Placing setAuth before publish keeps the "all dispatcher-side state mutations occur before we hand control back to the wire" reading order, which mirrors how the gateRun flag is handled.

### Failure modes

- **Gate returns Device==nil on accept.** Possible if a hostile-by-bug gate closure forgets the field. Dispatcher does not defend against this — `c.Auth()` returns nil and verb handlers must nil-check. Slice B and every later verb-handler ticket carry a "nil-check c.Auth()" rule, enforced by the handler reviewer and by the verb-handler test suite. This slice does NOT introduce a dispatcher-side panic for "accept with nil device" — that would be defensive code for a failure mode that hasn't happened. (Per pipeline principle: don't ship a defense for an unobserved failure.)
- **Gate returns Device on reject or Err path.** Dispatcher ignores it — the close-intent branch in `runGate` does not call `setAuth`, and the `Err` branch returns before any slot mutation. The test case in AC #5 (iii) pins this invariant.

## Concurrency model

No new goroutines. No new locks. The auth slot relies on the existing single-writer-per-conn invariant that already justifies `gateRun bool` and `*Conn`'s field set without mutex:

- Writer: `runGate` on the per-conn goroutine, exactly once per conn, before the first handler-table dispatch.
- Readers: `Conn.Auth()` called from handlers — which run on the same per-conn goroutine, sequentially after `runGate` returns.
- Cross-goroutine read: if a handler spawns a goroutine that reads `c.Auth()`, the write happens-before the goroutine spawn (Go memory model: goroutine start synchronizes with all prior writes on the spawning goroutine). Race detector is satisfied.

## Error handling

No new error types. No new error paths. The dispatcher's existing `Err`-path fallthrough and `CloseConn`-path close-with-code handling are untouched. The slot is purely additive on the accept path.

## Testing strategy

All in `internal/dispatch/gate_test.go` (same package — direct access to unexported `setAuth` / `auth` for the zero-value test). Run under `go test -race ./...`.

- **`TestFirstFrameGate_AcceptPopulatesAuth`** — accept-path device propagation.
  - Build a sentinel `&devices.Device{Name: "test-phone", ...}` in the test.
  - Build a fake gate that returns `FirstFrameOutcome{Response: helloAckResponse(...), Device: <sentinel>}`.
  - Register a handler for a sentinel envelope type (e.g., the existing `send_message` test type — register it specifically for this test) that records `c.Auth()` into a `chan *devices.Device` for the test to drain.
  - Feed hello + sentinel-typed frame.
  - Assert: handler observed `c.Auth() != nil` AND `c.Auth() == sentinel` (pointer equality — handlers see the same device the gate handed in, not a copy).

- **`TestConnAuth_NilBeforeGate`** — zero-value test.
  - Construct `c := &Conn{id: "c-1"}` directly (in-package access).
  - Assert `c.Auth() == nil`.
  - One-liner; pins the documented zero-state.

- **`TestFirstFrameGate_CloseConnDoesNotPopulateAuth`** — close-intent invariant.
  - Build a sentinel device.
  - Fake gate returns `FirstFrameOutcome{Response: authErrorResponse(...), CloseConn: true, Code: 4401, Device: <sentinel>}` — a deliberately misbehaving gate that supplies a device on the close path.
  - Register a handler that, if ever called, records the observed `c.Auth()` into a channel.
  - Feed hello (gate-driven close) + a second frame on the same conn.
  - Drain outbound; assert (a) the auth-error response is published, (b) the second frame is dropped (existing close-intent behaviour), (c) the handler channel is empty — no handler observation of the sentinel device, confirming the goroutine exited before any handler ran.

- **`relay.AuthOutcome` test coverage** — `internal/relay/auth_test.go` already has `TestAuthenticateFirstFrame_ValidToken`, `_UnknownToken`, `_RevokedTokenSameUX`, `_EmptyToken`, `_MalformedHelloFrame`. Add one assertion to `_ValidToken`: `outcome.Device != nil && outcome.Device.Name == <expected>`. Add the symmetric assertion to `_UnknownToken`, `_RevokedTokenSameUX`, `_EmptyToken`, `_MalformedHelloFrame`: `outcome.Device == nil`. These ride along on existing setup; no new test functions required.

Test code is written by the developer in the project's table-driven idiom — these bullets are scenarios, not code.

## Open questions

None. The import edge decision is resolved (accept it; see Design § Package boundary). The slot-write timing is resolved (in `runGate`'s accept branch, before NextID advance). The setAuth seam is unexported.

## Security review

**Verdict:** PASS

**Findings:**

- **[Trust boundaries]** No findings. The trust boundary is `reg.Validate(token) → (*devices.Device, true)` at `internal/relay/auth.go:90`. Past that point the `*Device` pointer is a trusted snapshot of an authenticated row. This slice carries that pointer forward through two struct fields and stores it once; it does not introduce any new untrusted-input parsing. Downstream verb handlers receive the device through `Conn.Auth()` which returns `*devices.Device` (concrete type, not `any`) — the type system signals "trusted device snapshot" without further commentary.
- **[Tokens, secrets, credentials]** No findings. The plain token (`env.Token`) is consumed exactly once by `AuthenticateFirstFrame`, never copied into the dispatcher, never logged, never written into the slot. What the slot holds is `*devices.Device`, whose token-bearing field is `TokenHash` (SHA-256 hex, not the plain credential). A handler reading `c.Auth().TokenHash` sees a hash; a handler logging `c.Auth().Name` matches existing `auth.accept` log policy. No new token-bearing log site.
- **[File operations]** N/A — this slice introduces no filesystem I/O.
- **[Subprocess / external command execution]** N/A — no subprocess execution.
- **[Cryptographic primitives]** N/A — no new crypto. Existing `reg.Validate`'s constant-time compare (per `devices` package's design) is unchanged.
- **[Network & I/O]** No findings. No new wire fields, no new envelope types, no new sockets opened. The wire protocol is byte-for-byte unchanged; the slice mutates only in-process state on `*dispatch.Conn` and the two intermediate carrier structs.
- **[Error messages, logs, telemetry]** No findings. No new log sites. The existing `auth.accept` (logs `device_name`) and `auth.reject` (no device fields) lines are unchanged. The dispatcher logs `conn_id` only at the slot-write point — and that point is silent (no log statement added; `setAuth` is a one-liner field write).
- **[Concurrency]** No findings. Single-writer-per-conn: `setAuth` is called exactly once on the per-conn goroutine before any handler runs on that goroutine; subsequent reads of `Auth()` happen on the same goroutine. Cross-goroutine reads (handler-spawned worker goroutines) are happens-before-safe under Go's memory model (goroutine start synchronizes with prior writes). The race detector is satisfied (verified by `go test -race ./...` in the test plan). No new locks; the existing `d.mu` and `connState` invariants are untouched. **One adversarial scenario considered:** a misbehaving `FirstFrameGate` closure that supplies `Device` on the close-intent or `Err` branch. The dispatcher's `runGate` calls `setAuth` only inside the accept-and-continue branch (`!outcome.CloseConn && outcome.Err == nil` is the implicit guard, because the `Err` and `CloseConn` branches both `return` before reaching the accept-path code). Test case (iii) pins this — a fake gate deliberately returns `CloseConn: true` with a sentinel device and the test asserts no handler observes it.
- **[Threat model alignment]** No findings. `docs/protocol-mobile.md` § Security model threats covered here: (a) phone token forgery — unchanged, still gated by `reg.Validate`; (b) name enumeration via reject logs — unchanged, the new `Device` field is never set on the reject path; (c) handler-side privilege escalation via tampered auth state — addressed by making `setAuth` unexported so verb handler closures cannot mutate auth from inside a handler. Out of scope: token revocation propagation to live conns (a paired device whose token is revoked mid-session still holds its `*Device` pointer on `Conn.Auth()` until the WS closes); this is the documented v1 semantic and is owned by the future revocation-propagation ticket (#TBD; not yet filed).

**Reviewer:** architect (self-review per `architect/security-review.md`)
**Date:** 2026-05-13
