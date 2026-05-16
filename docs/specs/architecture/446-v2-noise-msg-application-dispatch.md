# 446 — `internal/relay`: v2 `noise_msg` application dispatch + tampered-frame teardown

## Files to read first

Each entry says **what to extract**, so the developer's turn-1 data load is complete.

- `docs/specs/architecture/445-v2-inner-frame-handshake.md` — predecessor spec. The transition table is the contract for the cells we leave alone; only the `noise_msg`-in-`open` cell changes here. Reuse the same close codes, log policy, atomicity rule.
- `docs/knowledge/features/v2-session-manager.md:101-107` — current transition table. The `open` row's `noise_msg, decrypt succeeds → drop (open-state dispatch deferred)` cell is what this slice fills; the `noise_msg, decrypt fails → drop` cell becomes `close(4421), → closed`.
- `internal/relay/v2session.go:445-510` — `handleNoiseMsg`. Two existing branches stay unchanged (`V2StateAwaitingInit`, `V2StateHandshakeComplete`); only `V2StateOpen` is expanded. Note line 503-508 is the current "drop" stub.
- `internal/relay/v2session.go:557-595` — `closeWith` and `send` helpers. `closeWith` already deletes the session from `m.sessions` (line 569), so AC #3 (local cleanup after AEAD-failure close) is satisfied **structurally** by reusing `closeWith`; the developer must verify and pin this with a test, not add a parallel cleanup path.
- `internal/relay/v2session.go:63-69` — `V2Session` struct. Add one field: `device *devices.Device` (matched device snapshot, set in `handleNoiseInit`'s token-accept branch, read by handler dispatch in this slice).
- `internal/relay/v2session.go:410-443` — `handleNoiseInit` token-accept branch. One edit: capture `device` into `s.device = &device` right before `s.state = V2StateOpen` (line 442).
- `internal/relay/v2session.go:86-111` — `V2SessionConfig`. Add one optional field: `Handlers map[string]dispatch.Handler`. `nil` is acceptable (means "no app handlers registered"; every open-state app envelope falls through to a sealed `protocol.unsupported` reply). Constructor validation needs no new check.
- `internal/dispatch/dispatch.go:59` — `Handler` signature: `func(ctx, *Conn, protocol.Envelope) error`. Unchanged in this slice. Handlers reply via `c.Reply` / `c.Send` which push onto `c.outbound`.
- `internal/dispatch/dispatch.go:108-110` — existing `NewTestConn`. Doc says "test fixtures only — do not call from production code". Sibling `NewConn` (this slice adds it) shares the implementation; the test-only restriction stays on `NewTestConn`.
- `internal/dispatch/dispatch.go:523-554` — `handleOne`. The body becomes the new `Route` function. The 1-line `runConn` caller updates to `Route(ctx, d.cfg.Logger, c, d.handlers, routing.Frame)`.
- `internal/dispatch/dispatch.go:556-579` — `sendError`. Extract to a package-private free function `sendError(ctx, logger, conn, inReplyTo, code, message)` so the new `Route` (also in `package dispatch`) calls it. Mechanical; preserves behaviour.
- `internal/protocol/envelope.go:76-91` — `ErrUnknownType` / `ErrUnsupported` sentinels + `IsV1Compatible`. Route reuses these unchanged — v1 and v2 share the same compat-check; the spec deliberately does not introduce v2-specific error semantics here.
- `internal/protocol/codes.go:9-31` — error code constants. `CodeProtocolMalformed` / `CodeProtocolUnsupported` / `CodeProtocolUnknownType` are the only codes Route emits in this slice. No new codes.
- `internal/relay/handlers/send_message.go` — example handler shape. Synchronous reply via `c.Reply(ctx, env, type, payload)` or `replyError(...)`; the handler returns after the reply. No long-lived goroutines spawned from inside the handler. This is the synchronous-handler assumption the open-state dispatch design rests on.
- `internal/relay/handlers/list_conversations.go` — second example handler shape. Same synchronous-reply pattern.
- `internal/relay/v2session_test.go:108-205` — test harness helpers (`buildHelloEarlyData`, `wrapInnerFrame`, `decodeRespFrame`, `decodeNoiseMsg`, `v2Recorder`, `startManager`). Reuse for new tests; do not duplicate.
- `internal/relay/v2session_test.go:212-288` — `TestV2Session_HappyPath`. Pattern for "drive handshake to open", reused as the setup for new open-state tests.
- `internal/e2e/relay_v2_handshake_test.go:36-113` — `v2Harness` + `startV2Harness`. E2E pattern this slice extends with a stub-handler registration and a phone-side encrypted-envelope round-trip.
- `internal/e2e/relay_v2_handshake_test.go:117-188` — `dialPhone`, `sendNoiseInit`, `readInnerFrame`, `buildHelloEarly` helpers. Reuse.
- `internal/devices/auth.go:32-46` — `Registry.Validate(plain) → (Device, bool)`. The `Device` value returned on hit is what we capture into `s.device`. Take its address into a fresh local so `s.device` is a stable pointer (don't point into the returned value's storage if it goes out of scope — but in Go a local taken-address always escapes safely).
- `docs/protocol-mobile.md:436-440` — atomicity ordering MUST: sealed frame before close on the wire. Already enforced by `closeWith`'s single-envelope emit; this slice's new branches reuse it.
- `docs/protocol-mobile.md:447-460` — close-code table; 4421 is the only code this slice emits new.
- `docs/lessons.md` § "AEAD decryption errors" (search) — if present, captures any prior gotchas around CipherState nonce monotonicity on decrypt failure. flynn/noise increments the receive-side counter only on success; a failed `Decrypt` leaves the counter unchanged, so close-then-cleanup IS the only safe response (you cannot "skip and retry" a tampered frame).

## Context

#445 landed the per-conn v2 state machine through `handshakeComplete` → `open` with CipherStates live and the matched device. The `noise_msg` row in the `open` cell is the only behaviour deferred — frames there are currently dropped silently (`v2session.go:503-508`).

This slice fills that cell with three load-bearing behaviours:

1. **Application dispatch.** AEAD-decrypt the inbound `noise_msg` payload, JSON-unmarshal as a v1-shaped `protocol.Envelope`, look up the handler in `cfg.Handlers`, invoke it on a per-frame `*dispatch.Conn`, capture the handler's replies, AEAD-encrypt each reply, wrap as `noise_msg`, emit via `Outbound`.
2. **AEAD-failure teardown.** A `noise_msg` whose `Decrypt` returns non-nil is treated as tampered/replayed/truncated: emit a close-only routing envelope at 4421, drop the session entry, transition to `closed`. The handler chain is NOT reached.
3. **Local cleanup on the AEAD-failure path.** Reuse `closeWith` (already deletes from `m.sessions`) so the next `noise_init` for the same conn_id lazy-creates a fresh session in `awaitingInit`. No new cleanup code path.

The handler chain in `internal/relay/handlers/` does not change. Each handler is a `dispatch.Handler` that reads `env`, optionally consumes per-conn state (`c.Auth()`, `c.NextID()`), and replies via `c.Reply` / `c.Send`. The v1 dispatcher (`internal/dispatch.Dispatcher`) is the canonical handler-chain runner; this slice extracts a small reusable handler-table dispatch function (`dispatch.Route`) so v2 doesn't duplicate the malformed/unsupported/unknown-type error-envelope logic.

Per the ticket § Out of scope: phone-initiated WS close cleanup, re-key handling, pre-flight release-flag gate, production wiring of `V2SessionManager` into `cmd/pyry/relay.go`, and changes to `internal/relay/handlers/` payload shapes all stay deferred.

## Design

### Surface — additive

#### `internal/dispatch/dispatch.go` (modified)

```go
// NewConn constructs a *Conn for callers that own their own per-conn
// goroutine and route envelopes outside the Dispatcher.Run loop (e.g.
// the v2 session manager, which decrypts a noise_msg before dispatching
// the inner envelope through the handler table). The caller owns
// outbound and is responsible for draining it.
//
// Distinct from NewTestConn only in policy: NewTestConn carries the
// "test fixtures only" restriction; NewConn is the production-allowed
// equivalent. Implementations are identical.
func NewConn(id string, outbound chan<- protocol.RoutingEnvelope, auth *devices.Device) *Conn

// Route dispatches a single inbound envelope on conn through handlers,
// using the same malformed / IsV1Compatible / unknown-type error-envelope
// paths as Dispatcher.Run. Suitable for callers that own their own
// per-conn goroutine and only need single-frame handler-table dispatch.
//
// Error replies (malformed envelope JSON, unsupported v1 features,
// unknown envelope type, no registered handler) are emitted via
// conn.Send → conn.outbound. A non-nil error returned from the handler
// itself is logged at WARN; no automatic reply is synthesised (matches
// Dispatcher.Run's posture). handlers may be nil — every envelope falls
// through to the "no handler registered" reply path.
//
// Route does NOT block on conn.outbound capacity behaviour: the caller
// is responsible for sizing the channel so handler+Route replies fit
// without head-of-line-blocking the dispatch loop.
func Route(ctx context.Context, logger *slog.Logger, conn *Conn, handlers map[string]Handler, frame json.RawMessage)
```

**Refactor inside `package dispatch`** (file-private mechanical change, ≤10 LOC moved):

- Extract `Dispatcher.sendError` to a package-private free function `sendError(ctx, logger, conn, inReplyTo, code, message)`. Body unchanged.
- Replace `Dispatcher.handleOne` body with a single call: `Route(ctx, d.cfg.Logger, c, d.handlers, routing.Frame)`. Method stays for the `runConn` caller — same signature, same callers.
- Existing dispatch tests pin behaviour; the refactor must keep them green without modification.

#### `internal/relay/v2session.go` (modified)

```go
type V2SessionConfig struct {
    // ... existing fields unchanged ...

    // Handlers is the application-layer envelope-type → handler table.
    // Optional: nil or empty map means no app handlers are registered;
    // open-state app envelopes fall through to a sealed
    // protocol.unsupported reply. Mirror v1's
    // internal/dispatch.Dispatcher.Register registration shape —
    // production wires Handlers via the daemon, same handlers as v1.
    //
    // SECURITY: handlers run on the manager's single dispatch goroutine
    // (same goroutine that mutates s.send / s.recv). Handlers MUST be
    // synchronous and MUST NOT spawn long-lived background goroutines
    // that retain a reference to the *dispatch.Conn passed in — the
    // conn's outbound channel is per-frame and is drained before this
    // function returns; sends from a forked goroutine after that drain
    // are silently lost.
    Handlers map[string]dispatch.Handler
}
```

```go
type V2Session struct {
    // ... existing fields unchanged ...

    // device is the matched device snapshot from the handshake's
    // token-accept branch. Surfaced into the per-frame *dispatch.Conn as
    // auth so handlers can call c.Auth(). Set exactly once in
    // handleNoiseInit's token-OK path before state advances to
    // V2StateOpen.
    device *devices.Device
}
```

Two minimal edits to `handleNoiseInit`:

1. In the token-accept branch (currently `v2session.go:436-442`), insert `s.device = &device` between the log line and `s.state = V2StateOpen`. The `device` local already holds the matched `devices.Device` value returned from `cfg.Devices.Validate`.
2. No other changes to `handleNoiseInit`.

### `noise_msg` in `V2StateOpen` — the new dispatch branch

The single new code path. Replaces the current `_ = inner; return` stub (`v2session.go:503-508`). Behaviour described as a sequence of steps; the developer writes the imperative code.

1. **AEAD-decrypt.** `plaintext, err := s.recv.Decrypt(inner.Data)`. On err: log `v2.aead.fail` with `conn_id` and `close_code=4421`; call `m.closeWith(ctx, s, StatusProtocolMismatch, nil)`; return. **Do NOT include the AEAD error text in the log** (the underlying flynn/noise error may carry the failed counter index, which is not sensitive but is also not operator-actionable — keep the log field set minimal). The handler chain is NOT reached on this branch (AC #2).
2. **Construct per-frame Conn.** Allocate `outbound := make(chan protocol.RoutingEnvelope, handlerOutboundBuf)` where `handlerOutboundBuf = 8`. Construct `conn := dispatch.NewConn(s.connID, outbound, s.device)`.
3. **Dispatch.** `dispatch.Route(ctx, m.cfg.Logger, conn, m.cfg.Handlers, plaintext)`. Route is synchronous: it returns after the handler returns (or after the error-reply path completes for malformed/unsupported envelopes). All reply RoutingEnvelopes the handler/Route emitted are now in `outbound`.
4. **Drain and re-seal.** Iterate `outbound` non-blockingly (the channel is intentionally NOT closed — see § Concurrency for why). For each captured `replyRouting`:
   - AEAD-encrypt: `ciphertext, err := s.send.Encrypt(replyRouting.Frame)`. On err: log a warning with `conn_id` only; **do not** emit the unencrypted frame and do not advance the close path. (Realistically unreachable under correct flynn/noise.)
   - Wrap: `frame, err := marshalInnerFrameV2(protocol.TypeNoiseMsg, ciphertext)`.
   - Emit: `m.send(protocol.RoutingEnvelope{ConnID: s.connID, Frame: frame})`. **The reply's `CloseCode` is ignored** in this slice — handlers do not return close codes through `c.Send`/`c.Reply`; the close-code field on the routing envelope is reserved for the v2 manager's own close-intent emissions.

The new function is `(*V2SessionManager).dispatchAppFrame(ctx, s, plaintext)` (≤40 LOC). `handleNoiseMsg`'s `V2StateOpen` case calls it and returns.

#### Sketch (contract, not implementation)

```go
// dispatchAppFrame runs Route on a per-frame *dispatch.Conn, drains any
// reply envelopes the handler emitted, AEAD-seals each one under s.send,
// wraps as noise_msg, and forwards via m.send. Synchronous; returns
// only after the handler has returned and all replies have been drained.
//
// Assumes: handlers do not spawn long-lived goroutines that retain
// conn; the per-frame outbound buffer is large enough to absorb a
// handler's synchronous replies without blocking c.Send.
func (m *V2SessionManager) dispatchAppFrame(ctx context.Context, s *V2Session, plaintext []byte)
```

### Concurrency

**No new goroutines.** The manager's single dispatch loop continues to own all `*V2Session` mutation. Handler-chain dispatch happens synchronously inside the loop, on the same goroutine that mutates `s.send` / `s.recv`. The ticket explicitly pins this: *"Concurrency unchanged from the previous slice — same single dispatch loop per conn-id."*

**Per-frame Conn outbound channel sizing.** Buffer 8. All three production handlers (`send_message`, `list_conversations`, `register_push_token`) emit exactly one reply per invocation. Route emits at most one error reply (malformed/unsupported/unknown_type/no-handler). Buffer 8 is a generous safety margin and is documented as the synchronous-handler assumption above.

**Why the channel is not closed.** Closing `outbound` after the handler returns would race with any goroutine the handler accidentally forked that holds the channel — closing on the sending side is a panic. By NOT closing and draining non-blockingly (`for { select { case env := <-outbound: ...; default: return } }`), the manager survives a misbehaving handler. A handler that forks a sender goroutine emits silently into a leaked channel after `dispatchAppFrame` returns — the leak is bounded by the channel's capacity, and the GC reclaims it once the goroutine exits.

**Head-of-line blocking.** A slow synchronous handler stalls the manager's dispatch loop for ALL conn_ids, not just the current one. The current handlers (`send_message`'s 30s `Activate` timeout being the longest) make this worst-case 30s. Per-conn fan-out is **deliberately deferred** — the ticket explicitly says so. Tracked in Open Questions as the priority follow-up before production cutover.

**CipherState access.** `s.send.Encrypt` and `s.recv.Decrypt` execute on the dispatch loop; the same goroutine that previously mutated them in `handleNoiseInit` mutates them here. flynn/noise's monotonic 64-bit nonce counter is consumed in-order. No lock needed.

### State-machine transition table — updated

The existing table from #445 is unchanged except for the two cells in the `open` column for `noise_msg`. The full row is restated for clarity:

| Inbound on conn_id | `awaitingInit` | `handshakeComplete` | `open` (changes) | `closed` |
|---|---|---|---|---|
| `noise_init` | run handshake | close(4421), → closed | close(4421), → closed | drop |
| `noise_resp` | close(4421), → closed | close(4421), → closed | close(4421), → closed | drop |
| `noise_msg`, decrypts cleanly | close(4421), → closed | sealed `auth.invalid_token` + close(4401), → closed | **dispatch via handlers; sealed reply emitted; state stays `open`** | drop |
| `noise_msg`, decrypt fails | close(4421), → closed | close(4421), → closed | **close(4421), → closed (AEAD-failure teardown)** | drop |
| Unknown `type` / bad `v` / malformed | close(4421), → closed | close(4421), → closed | close(4421), → closed | drop |

The `closed` column row for `noise_msg, decrypt fails` is "drop" because the session map entry has already been deleted by `closeWith` — the next frame for this conn_id lazy-creates a fresh `awaitingInit` session per the existing `handleFrame` logic (`v2session.go:187-191`). This IS the local-cleanup-on-close behaviour AC #3 demands; it is structurally provided by reusing `closeWith` and verified by test.

### Error handling

| Failure mode | Close code | AEAD-sealed error envelope? | Notes |
|---|---|---|---|
| Tampered `noise_msg` in `open` (AEAD decrypt fail) | 4421 | no | close-only; handler chain unreached (AC #2) |
| Replayed `noise_msg` (nonce duplicate) | 4421 | no | same path as above — flynn/noise rejects at AEAD step |
| Truncated `noise_msg` (data < 16 bytes AEAD tag) | 4421 | no | same path |
| Malformed envelope JSON inside open-state `noise_msg` | (no close) | yes | Route emits sealed `protocol.malformed` error envelope; state stays `open` |
| Unknown envelope `Type` for which no handler is registered | (no close) | yes | Route emits sealed `protocol.unsupported` error envelope; state stays `open` |
| `IsV1Compatible` rejects (`PayloadEncrypted=true`, unknown type) | (no close) | yes | Route emits sealed `protocol.unsupported` or `protocol.unknown_type` |
| Handler returns non-nil error | (no close) | none synthesised | logged at WARN per Route's existing posture; state stays `open` |
| `s.send.Encrypt` fails on a handler reply | (no close) | none emitted | logged at WARN; reply lost. State stays `open`. Realistically unreachable. |
| `marshalInnerFrameV2` fails | (no close) | none emitted | same posture as Encrypt failure |

**Key invariant:** AEAD-failure on inbound `noise_msg` is the ONLY new close-code path in this slice. All other failures emit sealed application-layer error envelopes (because we have CipherStates and the channel is healthy) and leave the session in `open` for the next frame.

**Atomicity reuse.** The AEAD-failure teardown path uses `closeWith(ctx, s, StatusProtocolMismatch, nil)` — close-only, no sealed error frame. This is intentional: a tampered/replayed/truncated frame may have arrived from an attacker on the wire path (relay MITM, or a phone that's lost session state); emitting a sealed reply under `s.send` could leak information about the binary's nonce state. Close-only is the minimal signal. Mirrors the `noise_msg`-decrypt-fail-in-`handshakeComplete` row from #445 which is also close-only at 4421.

### Log policy (security-load-bearing)

Extends the v1 / #445 posture. The implementation MUST adhere; code-review checks each rule against the diff.

- **MUST NOT log at any level**: the AEAD plaintext (`plaintext` from `s.recv.Decrypt`), the AEAD ciphertext (`inner.Data`), handler reply envelope bytes (pre-encrypt), the encrypted reply bytes (post-`s.send.Encrypt`), base64 forms of any of the above, `s.send` / `s.recv` internal state. Same MUST applies to `slog` fields and to error wrapping.
- **MUST log on AEAD failure (open state)**: event class `v2.aead.fail`, `conn_id`, `close_code=4421`. NO error text from `s.recv.Decrypt` (the underlying flynn/noise error may contain counter indices that are not load-bearing for operators). NO envelope shape information (since we cannot inspect a frame that didn't decrypt).
- **MUST log on handler-chain dispatch**: the existing v1 handler logs (`send_message.ack`, etc.) are emitted by the handlers themselves and inherit their per-handler log policy unchanged. The v2 manager itself does NOT add a per-envelope log line on the open-state happy path — high-frequency message traffic should not spam the log channel. A `debug`-level "dispatched" log is acceptable but not required.
- **MUST log on Route-synthesised error replies**: Route already logs at WARN for malformed-envelope decode failure (mirrors `dispatch.handleOne`). The log fields stay unchanged — `conn_id`, decode-error class, NO frame bytes.

### Testing strategy

Tests split between same-package unit (state-machine + dispatch glue, fast, no WS) and e2e (real WS via fakerelay).

#### `internal/relay/v2session_test.go` — same-package unit (new tests)

Reuse `v2Recorder`, `startManager`, `buildHelloEarlyData`, `wrapInnerFrame`, `decodeNoiseMsg`, `v2PairedRegistry`, `genV2Keypair`. Each test below is described by its scenario; the developer writes the test in stdlib `testing` idiom matching the existing file.

- **`TestV2Session_OpenState_EncryptedRoundTrip`** — drive the happy-path handshake to open (reuse the `TestV2Session_HappyPath` setup), then register a stub handler in `cfg.Handlers` keyed by an arbitrary envelope type (e.g. `protocol.TypeListConversations`) that replies via `c.Reply(ctx, env, replyType, payload)` with a known payload. Initiator-side: AEAD-seal a `protocol.Envelope{Type: TypeListConversations, ...}` under `initSend`, wrap as `noise_msg`, feed to manager. Assert: exactly ONE outbound envelope with `CloseCode == 0`, frame is `noise_msg`, decrypts under `initRecv` to a `protocol.Envelope` with the expected `Type` / `Payload` and `InReplyTo` pointing back at the request envelope's `ID`. Verifies AC #1 end-to-end on the dispatch glue.
- **`TestV2Session_OpenState_TamperedNoiseMsg_4421`** — drive happy path to open. Build a real `noise_msg` from a marshalled envelope under `initSend.Encrypt`, then flip one byte of the ciphertext before wrapping. Feed. Assert: exactly ONE outbound envelope with `CloseCode == uint16(StatusProtocolMismatch)`, `Frame == nil`. After the manager has processed, assert `mgr.sessions[v2TestConnID] == nil` (deletion via `closeWith`). Verifies AC #2 and AC #3 jointly.
- **`TestV2Session_OpenState_FreshNoiseInitAfterAEADClose`** — companion to the prior test. After the AEAD-failure close+cleanup, send a SECOND `noise_init` (from a fresh `noise.Initiator` keyed against the same `respPub`) on the same `conn_id`. Assert: handshake completes, a new `noise_resp` envelope is emitted (count goes up by 1), `mgr.sessions[v2TestConnID]` is non-nil and in `V2StateOpen`. Verifies AC #3 explicitly: the post-cleanup session entry is fresh `awaitingInit`-then-`open`, with no carry-over from the previous `s.send` / `s.recv`. (Cross-check: the new `s.send` is a different pointer / contains a different nonce-counter state. Visible by sealing a frame under the OLD `initSend` and feeding it post-handshake — Decrypt should fail because the new session's `s.recv` is a different CipherState.)
- **`TestV2Session_OpenState_TamperedFrame_HandlerNotReached`** — drive happy path to open. Register a stub handler that flips a `*atomic.Bool` when called. Feed a tampered `noise_msg`. Assert: `CloseCode == 4421`, AND the atomic flag is still false. Structurally proves "handler chain not reached" on AEAD failure.
- **`TestV2Session_OpenState_UnknownEnvelopeType_SealedUnsupportedReply`** — drive happy path to open with NO handlers registered (Handlers nil). Feed a well-formed encrypted `Envelope{Type: TypeListConversations}`. Expect: ONE outbound `noise_msg` envelope, `CloseCode == 0`, decrypts to a `protocol.Envelope{Type: TypeError}` with `ErrorPayload.Code == CodeProtocolUnsupported`. State remains `open` (`mgr.sessions[v2TestConnID].State() == V2StateOpen`). Verifies Route's error-envelope path is exercised correctly through the encrypt+wrap pipeline.
- **`TestV2Session_OpenState_MalformedInnerEnvelope_SealedMalformedReply`** — drive happy path to open. AEAD-seal raw garbage bytes (e.g. `[]byte("not json")`) under `initSend`, wrap as `noise_msg`. Expect: ONE outbound `noise_msg` envelope, decrypts to `Envelope{Type: TypeError, Payload: {Code: CodeProtocolMalformed}}`. State remains `open`.

`TestNewV2SessionManager_ConfigValidation` from #445 stays unchanged; `Handlers` is optional so no new validation cases.

#### `internal/dispatch/dispatch_test.go` — verify the refactor (existing tests must stay green)

The refactor of `handleOne` → `Route` and `sendError` → free function must NOT change observable behaviour. Existing dispatch tests are the regression guard. Developer responsibility: run `go test ./internal/dispatch/...` after the refactor; if any test fails, the refactor is wrong.

Add one new test:

- **`TestRoute_StandaloneInvocation`** — construct a `*Conn` via `NewConn` with a buffered channel, register `handlers["foo"] = func(...) { return c.Reply(...) }`, call `Route(ctx, logger, conn, handlers, frameJSON)`, assert the outbound channel received the expected reply. Pins the externally-exposed Route contract.

(Optionally: small tests for the malformed-frame / unsupported-feature / unknown-type / no-handler error-envelope paths via Route directly. Cheap, ≤40 LOC total.)

#### `internal/e2e/relay_v2_handshake_test.go` (`//go:build e2e`) — extend

Three new subtests added to the existing `TestRelayV2_Handshake` matrix (or a sibling `TestRelayV2_AppDispatch`, developer's call — same harness). The `v2Harness` constructor grows one parameter: `handlers map[string]dispatch.Handler` (default `nil` for existing happy/bad-token/IK-reject tests). Wire `handlers` into `relay.V2SessionConfig.Handlers`.

- **`testV2EncryptedEchoRoundTrip`** — paired device, handshake to open. Register an echo handler keyed by `protocol.TypeListConversations` that replies with a known `protocol.ConversationsPayload`. Phone-side: complete handshake, capture `initSend` / `initRecv`. AEAD-seal a `list_conversations` envelope under `initSend`, wrap as `noise_msg`, send via phone. Read one inner frame back from the phone; assert it is `noise_msg`, decrypt with `initRecv`, decode as `Envelope{Type: TypeConversations}` whose `InReplyTo` matches the request. Verifies the end-to-end encrypted echo through the existing handler chain (AC #1, e2e).
- **`testV2TamperedNoiseMsg_4421`** — paired device, handshake to open. Phone sends a tampered `noise_msg` (well-formed inner frame envelope, ciphertext with flipped byte). Phone's next read errors; assert `fakephone.LastCloseStatus() == 4421`. No handler reached (verified by NOT registering any handler — if a handler is registered, the test can additionally assert it was not invoked via a side-effect flag; but absence-of-side-effect is harder to pin in e2e, so the unit test owns that assertion).
- **`testV2NoiseInitAfterTamperedClose`** — paired device, handshake to open. Send tampered `noise_msg`, observe 4421 close. The phone connection is gone; reconnect a SECOND `fakephone.Client` with the same conn (different WS, fakerelay assigns a fresh conn_id by default — adjust the harness to force the same conn_id, OR accept that "same conn_id" is an artificial unit-test concept and let this case live only in the unit suite). If the developer determines forcing same-conn-id is fakerelay-invasive, drop this e2e and rely solely on `TestV2Session_OpenState_FreshNoiseInitAfterAEADClose`'s unit-test coverage. Document the decision in the test comment.

The e2e tests for AC #3's "fresh awaitingInit after close" are best validated at the unit-test layer because the conn_id reuse is awkward over real WS. The unit test pins it deterministically.

### Wire-format and protocol changes

**None.** All wire shapes are unchanged from #445. This slice only adds binary-side dispatch behaviour to a row of the existing transition table.

### `cmd/pyry/relay.go` wiring

**Not modified in this slice.** Production daemon continues to wire the v1 dispatcher; the v2 manager remains test-only. Production cutover lives in a follow-up (likely after this slice + the release-flag gate from #436). Open Question 1 below tracks the cutover decision.

The `V2SessionConfig.Handlers` field is wired in test code only; the production handler registration (currently in `cmd/pyry/relay.go` against `dispatch.Dispatcher.Register`) is not duplicated for v2 here.

## Open questions

1. **Production cutover.** With `noise_msg` dispatch landed, v2 is functionally complete enough for an operator-facing deploy. Cutover involves: re-wiring `cmd/pyry/relay.go` to construct `V2SessionManager` instead of `Dispatcher`, AND a strategy for registering handlers against `V2SessionConfig.Handlers` instead of `Dispatcher.Register`. The latter is straightforward (same handler functions, different registration site). The pre-flight release-flag gate (#436) gates the production flip. Out of scope for this slice; explicitly tracked as the next slice.
2. **Per-conn fan-out.** A long-running handler (e.g. `send_message`'s 30s `Activate` timeout on a freshly-evicted session) stalls the manager's dispatch loop for ALL conn_ids until the handler returns. v1's dispatcher absorbs this with per-conn goroutines; v2 does not in this slice. The follow-up: spawn one goroutine per `conn_id` (matching `dispatch.Dispatcher.runConn`) once `s.send` / `s.recv` are exposed under a per-conn mutex. Tracked in v2-session-manager.md Open Q3 — concrete answer: introduce a small per-session mutex guarding `s.send` and `s.recv`; the dispatch loop dispatches to per-conn goroutines that take the mutex before each CipherState op.
3. **Phone-WS-close cleanup.** Unchanged from #445: no relay→binary "phone disconnected" signal exists in the v2 wire today. State entries for phone-disconnected conns linger until the binary↔relay leg recycles. Already in #445's Open Q1.

## Scope self-check

Production source files modified or created (excluding tests, `*.md`, the spec):

1. `internal/dispatch/dispatch.go` — modified (new exported `Route` and `NewConn`; internal `handleOne` → 1-line wrapper; `sendError` extracted to package-private free function).
2. `internal/relay/v2session.go` — modified (new `Handlers` field on `V2SessionConfig`; new `device` field on `V2Session`; one-line edit in `handleNoiseInit` token-accept branch; new `dispatchAppFrame` function; expanded `V2StateOpen` cases in `handleNoiseMsg` for both AEAD-success-dispatch and AEAD-failure-close).

Count: **2 production source files**. Well under the 5-file size:s ceiling.

New exported symbols:

- `dispatch.NewConn` (function)
- `dispatch.Route` (function)
- `V2SessionConfig.Handlers` (field — not a new type)

**0 new exported types.** 2 new exported functions + 1 field. Well under the 5-type ceiling.

Production LOC estimate: 75–110. Within S (≤150).

Edit fan-out: `dispatch.sendError` (unexported, 6 callers within `dispatch.go`) and `dispatch.handleOne` (unexported, 1 caller within `dispatch.go`). Verified via codegraph + grep: zero external consumers. The refactor is one-file mechanical.

Acceptance criteria: **4.** Within boundary.

Size: **S confirmed.**

## Security review

**Verdict:** PASS

**Findings:**

- **[Trust boundaries]** No findings — single explicit boundary at `s.recv.Decrypt` (one call site, in `dispatchAppFrame`'s entry path). All plaintext bytes downstream of this call are AEAD-authenticated; from there they flow into `dispatch.Route`, which reuses v1's identical malformed-/unsupported-/unknown-type-envelope handling. The boundary is narrower than v1 (v1's boundary is at WS-frame ingress; v2's is one layer deeper, AT the AEAD-decrypt). Empty plaintext (`Decrypt` returns `[]byte{}` cleanly) and zero-Type envelopes (`{}`) both fall through Route's existing error-envelope paths — no new attacker-controllable code branch.
- **[Tokens, secrets, credentials]** OUT OF SCOPE — `c.Auth()` returns the device snapshot captured at handshake time (`s.device`). Subsequent device revocation does NOT tear down the active conn; the connection's handler-chain context continues to hold the now-stale `*devices.Device` pointer until the WS leg recycles. **Same posture as v1** (where `dispatch.Conn.auth` has the same lifetime relative to the first-frame gate). Revocation propagation for active conns is a separate ticket — name it explicitly to a follow-up if/when revocation lands. This slice introduces no new revocation surface.
- **[File operations]** N/A — this slice performs no file I/O. The dispatched handlers may (e.g. `send_message`'s supervisor write), but those paths are unchanged from v1 and have their own security review.
- **[Subprocess / external execution]** N/A — no subprocess interaction in this slice.
- **[Cryptographic primitives]** No findings — AEAD primitives (`s.send.Encrypt`, `s.recv.Decrypt`) inherited from `internal/noise` (#433, separately reviewed); empty-AD invariant is structurally enforced at the type system; nonce monotonicity is flynn/noise's 64-bit monotonic counter, single-writer-per-session structurally guaranteed by the dispatch loop. No new primitive introduced. No path exists for a handler to bypass AEAD on the egress side: every captured reply on the per-frame `outbound` channel is encrypted before forwarding via `m.send`; the manager's other `m.send` call sites (handshake `noise_resp`, sealed error envelopes from #445) are unchanged.
- **[Network & I/O]** No findings, with one documented assumption — inbound `Data` size cap (65535 bytes decoded) is inherited from `decodeInnerFrameV2` (#445); outer `RoutingEnvelope` cap inherited from `internal/transport`'s 1 MiB read limit. Per-frame `outbound` channel sized at **8** (capacity); each of the three production handlers (`send_message`, `list_conversations`, `register_push_token`) emits exactly one reply, so the cap is a 7-envelope safety margin. A buggy handler that emits more than 8 replies blocks on `c.Send` until either (a) the channel drains (it won't — drain runs after `Route` returns), or (b) the dispatch ctx is cancelled (process shutdown). Worst case is a stalled-but-survivable dispatch loop until shutdown; not exploitable by a phone (handlers are operator-installed binary code, not attacker-controlled). Head-of-line-blocking across conn_ids on a slow handler is the SAME ALREADY-EXISTING surface from #445 (a handshake-time `Activate` would have the same effect) — tracked as Open Question 2 (per-conn fan-out follow-up).
- **[Error messages, logs, telemetry]** No findings — AEAD-failure events log event class (`v2.aead.fail`), `conn_id`, and `close_code=4421` ONLY. The underlying flynn/noise error text is intentionally NOT logged (it may include counter indices, which are not operator-actionable AND could theoretically aid an attacker doing replay reconnaissance against the binary's nonce state, however marginally). Route's existing error replies use static strings (`"malformed envelope"`, `"unsupported envelope feature"`, `"unknown envelope type"`, `"no handler registered for envelope type"`) — zero attacker-controlled echo. Plaintext envelope bytes and ciphertext bytes MUST NOT appear in any logged field; pinned in § Log policy.
- **[Concurrency]** No findings — single dispatch goroutine continues to own all `*V2Session` mutation. No new mutex, no new channel-pair between goroutines (the per-frame `outbound` channel is owned entirely by the dispatch loop's call frame). CipherState ops (`Encrypt`, `Decrypt`) all execute on the dispatch goroutine. A handler that forks a goroutine retaining `*dispatch.Conn` is a documented anti-pattern: such a goroutine writes to the per-frame `outbound` channel after `dispatchAppFrame` has returned; writes block on the buffer (capacity 8) and eventually wait on ctx.Done; both the goroutine and the channel are GC'd once ctx is cancelled. Bounded blast radius (1 leaked channel per misbehaving handler invocation). The CipherState bytes themselves are NOT accessible from the forked goroutine — `s.send` / `s.recv` are not exposed through `*dispatch.Conn`.
- **[Threat model alignment]** No findings.
  - **Threat #3** (relay MITM): AEAD channel from #445 unchanged; every inbound and outbound application frame in `open` flows through CipherState. A relay-injected `noise_msg` with tampered ciphertext fails AEAD authentication → 4421 close per AC #2.
  - **Threat #5** (compromised phone): a paired device can send arbitrary envelopes; the dispatched handler holds the device snapshot via `c.Auth()` and applies its own per-handler authorization (unchanged from v1). No new authorization surface.
  - **Threat #6** (replay): flynn/noise's monotonic counter rejects duplicate-counter frames at `Decrypt` → AEAD-failure branch → 4421 close. AC #2 covers this.
  - **Threat #7** (tampered frame): AEAD authentication rejects → AEAD-failure branch. AC #2.
  - **Threat #8** (out-of-order frames): flynn/noise requires in-order counter receipt; an out-of-order frame fails Decrypt → 4421 close. Acceptable.
  - **AC #4 invariant** ("handler chain not reached on tampered frame") is **structurally enforced**: the AEAD-decrypt error branch in `handleNoiseMsg`'s `V2StateOpen` case returns BEFORE `dispatchAppFrame` is called — there is no code path from a failed Decrypt to a handler invocation. Pinned by `TestV2Session_OpenState_TamperedFrame_HandlerNotReached`.

**Reviewer:** architect (self-review per `agents/architect/security-review.md`)
**Date:** 2026-05-16
