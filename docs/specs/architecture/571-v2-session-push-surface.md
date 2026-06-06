# Spec — `internal/relay`: concurrency-safe v2 session push surface for unsolicited `noise_msg` (#571)

## Files to read first

- `internal/relay/v2session.go:98-105` — `manualRekeyReq` struct: the per-request reply-channel funnel shape the new `pushReq` mirrors byte-for-byte.
- `internal/relay/v2session.go:300-340` — `V2SessionManager` struct + the single-owner-goroutine concurrency contract (`sessions` mutated only by `Run`, no mutex). The `wake` / `manualRekey` field doc-comments are the template for the new `push` field's comment.
- `internal/relay/v2session.go:346-372` — `NewV2SessionManager`: where the `push` channel is allocated (`make(chan pushReq)`, unbuffered — same as `manualRekey`).
- `internal/relay/v2session.go:383-401` — `Run`'s `select` loop: add one `case req := <-m.push` arm next to the existing `manualRekey` arm.
- `internal/relay/v2session.go:1220-1288` — `Rekey` (public funnel method) + `handleManualRekey` (the on-dispatch-goroutine lookup + state-check handler). **`Push`/`handlePush` are the structural twins of these two functions** — same funnel-then-handle split, same `ErrConnNotFound` / `ErrSessionNotOpen` error contract.
- `internal/relay/v2session.go:1044-1119` — `emitRekeyRequest`: the exact `json.Marshal(envelope)` → `s.send.Encrypt` → `marshalInnerFrameV2(TypeNoiseMsg, …)` → `m.send` sequence `handlePush` reuses (minus the rekey timer/awaiting bookkeeping).
- `internal/relay/v2session.go:957-1001` — `dispatchAppFrame`: the in-flight reply path the push must interleave safely with. Confirms every `s.send.Encrypt` already runs on the `Run` goroutine.
- `internal/relay/v2session.go:1153-1165` — `marshalInnerFrameV2`: the noise_msg wrapper to reuse verbatim (no new wire shape).
- `internal/relay/v2session.go:62-76` — `ErrConnNotFound` (wraps `control.ErrConnNotFound`) + `ErrSessionNotOpen` sentinels, reused unchanged by `Push`.
- `internal/relay/v2session_test.go:94-117` (`startManager`), `:39-64` (`v2Recorder`), `:157-172` (`waitForEnvelopes`), `:690-774` (`openSession`, `driveToOpen`, `sealAppFrame`, `decryptAppFrame`) — the white-box harness the concurrency tests build on. `driveToOpen` returns the phone-side `initRecv` CipherState used to decrypt pushed frames; `v2Recorder.snapshot()` captures outbound frames in send order.
- `internal/relay/v2session_test.go:781-849` — `TestV2Session_OpenState_EncryptedRoundTrip`: the closest existing test shape — drives to open, registers a reply handler, sends a sealed request, decrypts the reply. The AC#1 concurrency test extends this pattern with a concurrent `Push`.
- `cmd/pyry/assistant_turn.go:97-159` — v1 `assistantTurnEmitter.broadcast`: how the consumer (#572) builds a `message` envelope (`protocol.TypeMessage` + `protocol.MessagePayload`) and fans out. Confirms the input the v2 push surface must accept is a fully-formed `protocol.Envelope`.
- `cmd/pyry/relay.go:255-297` — `startRelayV2`: where #572 will obtain the `*V2SessionManager` handle to call `Push`. No change in this ticket; read it to confirm the manager handle is reachable for the consumer.
- `docs/protocol-mobile.md` § Wire shapes (`noise_msg`, InnerFrameV2) and § Re-key — wire source of truth; confirms no new inner-frame or envelope type is introduced.

## Context

**What problem this solves.** On the v2 (Noise) mobile path the daemon's `V2SessionManager` can only reply to a phone synchronously — one reply per inbound frame, drained inside `dispatchAppFrame`. There is no way to send an *unsolicited* frame to an open session. The code calls this out directly at `v2session.go:208-210` ("a broadcast layer added in a later slice") and on `State()`.

**Why now.** This is the missing primitive behind every server-initiated delivery to a phone — most importantly the assistant's reply to `send_message`, which today returns only an `ack`. The consumer is the sibling ticket **#572** (the v2 assistant-turn bridge), which is `blocked-by` this one. #572 mirrors the v1 `startAssistantTurnBridge` (`cmd/pyry/assistant_turn.go`): it taps the assistant/PTY output stream and fans `message` envelopes to connected phones. The v1 path does this through `dispatch.Conn.Send` (a concurrency-safe channel write); the v2 path has no equivalent because `s.send` (the per-session Noise send CipherState) is a single-owner, no-mutex resource.

**The core hazard.** The whole package is built on a single-owner-goroutine invariant: the manager's `Run` goroutine is the only writer of `s.send` / `s.recv`, so there is no mutex (`v2session.go:138-147`, `:300-314`). `flynn/noise`'s `CipherState` carries a mutable 64-bit nonce counter and is **not safe for concurrent use**. An out-of-band push from a producer goroutine (the #572 bridge) that touched `s.send` directly would interleave with an in-flight `dispatchAppFrame` reply and reuse a nonce — an AEAD catastrophe. The push therefore needs a deliberate concurrency mechanism. The in-tree precedent is exact: `manualRekey chan manualRekeyReq` + `(*V2SessionManager).Rekey` already funnel a cross-goroutine request onto the `Run` goroutine so the lookup + `s.send` emit runs under the single-owner invariant (`v2session.go:331-340`, `:1220-1288`).

## Design

Purely additive. One production file (`internal/relay/v2session.go`). No new exported types; the error contract reuses the existing `ErrConnNotFound` / `ErrSessionNotOpen` sentinels. The new public method `Push` is the structural twin of `Rekey`; the new private handler `handlePush` is the twin of `handleManualRekey` + `emitRekeyRequest`'s seal sequence.

### Approach: single-writer funnel (NOT a mutex)

The ticket leaves the mechanism (mutex vs single-writer funnel) to the architect. **Decision: single-writer funnel**, mirroring `manualRekey`. Rationale:

- A mutex on `s.send` would contradict the package's documented no-mutex contract and would have to be threaded through *every* `s.send.Encrypt` site (`dispatchAppFrame`, `emitRekeyRequest`, `sealError`, `handleRekeyInit`'s swap) — a cross-cutting refactor of the in-flight reply path, violating "touch only what's necessary" and AC#5 ("no change to the inbound dispatch path's existing semantics").
- The funnel touches nothing in the existing dispatch path. It adds one channel, one `select` arm, and two new functions. All existing `s.send` writers stay exactly as they are, on the same single goroutine.
- The funnel composes correctly with the re-key CipherState swap for free: because `handleRekeyInit`'s `s.send, s.recv = newSend, newRecv` swap and `handlePush`'s `s.send.Encrypt` both run on the `Run` goroutine and are serialised by the `select`, a push can never seal under a half-swapped or stale CipherState (see § Concurrency model).

### New types and fields

```go
// pushReq is enqueued by (*V2SessionManager).Push and dequeued by Run on
// the push channel arm. reply is per-request (cap=1) so Run's reply send
// is non-blocking even if the caller's ctx fires between enqueue and reply.
// Mirrors manualRekeyReq.
type pushReq struct {
    connID string
    env    protocol.Envelope
    reply  chan error
}
```

Add one field to `V2SessionManager` (doc-comment mirrors `manualRekey`'s):

```go
// push funnels (*V2SessionManager).Push calls onto Run's dispatch
// goroutine so the lookup + seal-under-s.send + forward sequence runs
// under the single-owner-goroutine invariant. Unbuffered: backpressure is
// correct — if Run is busy, Push waits; the caller's ctx is the escape arm.
// Not closed by the manager on Run exit; in-flight callers unblock via
// ctx.Done.
push chan pushReq
```

Allocate it in `NewV2SessionManager` alongside `manualRekey`: `push: make(chan pushReq)`.

### `Run` select arm

Add next to the existing `manualRekey` arm (`v2session.go:397-398`):

```go
case req := <-m.push:
    req.reply <- m.handlePush(runCtx, req.connID, req.env)
```

### Public method — `Push`

```go
// Push seals env under the addressed session's send CipherState, wraps it
// as a noise_msg transport frame, and forwards it to the phone. Safe to
// call from any goroutine other than the dispatch goroutine: the request
// is funneled onto Run via m.push so s.send is never touched concurrently
// with an in-flight dispatchAppFrame reply or a re-key swap.
//
// connID names a specific connected phone. The caller owns env entirely
// (Type, ID, TS, Payload); Push performs no envelope validation — it is a
// transport primitive.
//
// Returns ErrConnNotFound (wraps control.ErrConnNotFound) when no session
// with connID exists or it has been torn down; ErrSessionNotOpen when the
// session exists but is not in V2StateOpen (still handshaking, or
// handshake-complete-but-token-unvalidated); ctx.Err() on caller
// cancellation; or a wrapped marshal/seal error (realistically
// unreachable under correct flynn/noise). Returns nil once the sealed
// frame is forwarded to Outbound — a transport-level drop (relay
// disconnected) is logged at debug inside m.send and NOT surfaced,
// matching v1 reconnect semantics and the rest of the package.
func (m *V2SessionManager) Push(ctx context.Context, connID string, env protocol.Envelope) error
```

Body mirrors `Rekey` (`v2session.go:1238-1251`): build `pushReq{connID, env, reply: make(chan error, 1)}`; `select` send onto `m.push` or `ctx.Done()`; `select` receive on `req.reply` or `ctx.Done()`.

### Private handler — `handlePush` (runs on the `Run` dispatch goroutine)

```go
// handlePush runs on Run's dispatch goroutine. It looks up the session,
// requires V2StateOpen, then seals env under s.send and forwards a
// noise_msg — reusing emitRekeyRequest's marshal→Encrypt→wrap→send
// sequence (minus the rekey bookkeeping).
func (m *V2SessionManager) handlePush(ctx context.Context, connID string, env protocol.Envelope) error
```

Behaviour (each step is one short block; no full body in this spec):

1. `s, ok := m.sessions[connID]`; `!ok` → return `ErrConnNotFound`. (A torn-down session was already `delete`d from the map by `closeWith`, so "closed" collapses into this same branch.)
2. `s.state != V2StateOpen` → return `ErrSessionNotOpen`. **Security gate:** a `V2StateHandshakeComplete` session holds CipherStates but has not passed the token check; refusing to push to it ensures no server output reaches an un-authenticated peer.
3. `json.Marshal(env)` → wrap-and-return on error (defensive; a `message` envelope is a closed struct of strings — see #572).
4. `s.send.Encrypt(envJSON)` → wrap-and-return on error. **Reads `s.send` at execution time on the dispatch goroutine**, so it always uses the current CipherState (composes with re-key swaps).
5. `marshalInnerFrameV2(protocol.TypeNoiseMsg, ciphertext)` → wrap-and-return on error.
6. `m.send(protocol.RoutingEnvelope{ConnID: s.connID, Frame: frame})`; return `nil`.

No log lines on the lookup-miss / not-open paths — the sentinel is returned and the caller (#572) decides log level. The seal/marshal error paths return wrapped errors (caller logs at debug); they do **not** log inside `handlePush` and must never echo `env`, plaintext, ciphertext, or key bytes (§ Security review).

**Inline, not extracted.** The seal→wrap→send sequence is already inlined at three sites (`dispatchAppFrame`, `emitRekeyRequest`, `sealError`). Adding a fourth inline instance is consistent with the package's existing choice; extracting a 2-line `Encrypt`+`marshalInnerFrameV2` helper for one new caller would be over-DRY (cf. PROJECT-MEMORY "Resist over-DRY on duplicated registry primitives"). Do not refactor the existing three sites.

### Input contract: `protocol.Envelope`, caller-owned

`Push` takes a fully-formed `protocol.Envelope`, not pre-marshaled bytes and not `(type, payload)`. Rationale: the ticket's verb is "seals an application envelope" — the envelope is the input. The consumer (#572) builds a `message` envelope (`Type: protocol.TypeMessage`, `Payload:` a marshaled `protocol.MessagePayload`) and owns `ID`/`TS`. The push surface stays a pure transport primitive with no envelope-construction policy baked in. See § Open questions on outbound envelope-ID policy.

### What is NOT in this ticket

- **No enumeration / broadcast.** `Push` is addressable-by-`conn_id` only, per the AC. #572 needs to fan out to *all* connected phones; the snapshot of connected `conn_id`s (the v2 analog of v1's `dispatch.Dispatcher.ActiveConns()`) is **#572's** concern — it is itself a cross-goroutine read of `m.sessions` that needs its own funnel/snapshot design, and bundling it here would over-scope #571. Flagged in § Open questions.
- **No production wiring.** Like `Rekey` before #459/#549 wired it, `Push` is reachable only from `internal/relay` tests until #572 wires the bridge. Asserted by a `var _` compile-time check is unnecessary (no interface to satisfy); the method is simply unexercised in production this slice.

## Concurrency model

- **Goroutines owned by this change: zero.** `Push` runs on the caller's goroutine up to the channel send; `handlePush` runs on the existing `Run` goroutine. No `go` statement is added.
- **The serialisation guarantee (AC#1, AC#2).** Every `s.send.Encrypt` in the package runs on the `Run` goroutine: the in-flight `dispatchAppFrame` reply, `emitRekeyRequest`, `sealError`, and now `handlePush`. `Run`'s `select` processes exactly one arm at a time. While `Run` is inside `handleFrame`→`dispatchAppFrame` sealing a reply, it is not in `handlePush`; the `pushReq` waits on the unbuffered `m.push` send until `Run` returns to its `select`. Conversely a push in progress blocks inbound-frame handling. The two can never touch `s.send` simultaneously — the nonce counter advances strictly in `Run`'s call order. This is the same structural argument that makes `Rekey` safe, now extended to application pushes.
- **Composition with re-key (AC#2 "never corrupt the send CipherState").** `handleRekeyInit` swaps `s.send, s.recv = newSend, newRecv` in a single tuple assignment on the `Run` goroutine (`v2session.go:851`). Because `handlePush` reads `s.send` at execution time on that same goroutine, a push and a swap are serialised: a push either seals fully under the old key (swap not yet applied) or fully under the new key (swap already applied) — never a torn read, never a stale captured pointer. No additional locking needed.
- **Backpressure.** `m.push` is unbuffered. A flood of concurrent `Push` callers serialises on `Run`; there is no unbounded queue. Each caller blocks until `Run` services it (or its ctx fires). This is correct flow control, identical to `manualRekey`.
- **Shutdown safety.** `m.push` is not closed on `Run` exit (mirrors `manualRekey`). An in-flight `Push` blocked on the channel send or the reply receive unblocks via its `ctx.Done()` arm. `runCtx` cancels on `Run` exit. No goroutine leak; no send-on-closed-channel panic.
- **Reply channel.** `pushReq.reply` is cap=1, so `Run`'s `req.reply <- m.handlePush(...)` never blocks even if the caller already abandoned the call via `ctx.Done()` between enqueue and reply.

## Error handling

| Condition | Result | Notes |
|---|---|---|
| `connID` not in `m.sessions` (unknown or torn-down) | `ErrConnNotFound` | Reused sentinel; wraps `control.ErrConnNotFound`. No mutation of any session. |
| Session exists, `state != V2StateOpen` | `ErrSessionNotOpen` | Reused sentinel. Security gate against pushing to un-authenticated peers. |
| Caller ctx cancelled before enqueue or before reply | `ctx.Err()` | Mirrors `Rekey`. |
| `json.Marshal(env)` fails | wrapped error | Defensive; realistically unreachable for a well-typed envelope. |
| `s.send.Encrypt` / `marshalInnerFrameV2` fails | wrapped error | Defensive; "realistically unreachable under correct flynn/noise" (same posture as `emitRekeyRequest`). Frame dropped, conn NOT closed. |
| `Outbound` returns transport error (relay disconnected) | `nil` returned; debug log in `m.send` | Frame lost per v1 reconnect semantics; not surfaced to caller. |

No new close codes, no conn teardown on push failure. A failed push never transitions the session out of `V2StateOpen` — it is a best-effort delivery, exactly like `emitRekeyRequest`'s drop-and-stay-open posture.

## Testing strategy

White-box tests in `internal/relay/v2session_test.go` (same package), stdlib `testing` only, all under `go test -race`. Build on the existing harness (`driveToOpen`, `v2Recorder`, `waitForEnvelopes`, `sealAppFrame`, `decryptAppFrame`). **No e2e oracle extension in this ticket** — see § Open questions (no production caller of `Push` exists until #572, so the real `send_message → message` round-trip is #572's e2e deliverable, extending `internal/e2e/relay_v2_daemon_test.go` there).

Scenarios (bullet form; developer writes the table/test bodies in the project idiom):

- **AC#1 — push interleaved with in-flight reply, both decrypt under `-race`.** Drive a paired session to open with a registered reply handler (à la `TestV2Session_OpenState_EncryptedRoundTrip`). From a separate goroutine call `mgr.Push(ctx, connID, <message envelope>)` while simultaneously feeding an inbound sealed request on `frames` (which triggers a `dispatchAppFrame` reply). Wait for both outbound envelopes via `waitForEnvelopes(rec, …)`. Decrypt `rec.snapshot()` in capture order under the phone's `initRecv`; assert **every** frame decrypts with no AEAD error and that the set contains both a `conversations` reply (InReplyTo = request ID) and a `message` push. Order between the two is nondeterministic (Run's `select`) — assert presence, not order; the in-order clean decrypt is the nonce-integrity proof.
- **AC#2 — concurrent pushes + in-flight replies never corrupt send CipherState.** Spawn N goroutines (e.g. 8) each calling `Push`, plus M inbound sealed requests, on the same open session. Collect N+M outbound frames; decrypt all in capture order under `initRecv`; assert all succeed. `-race` clean. (This is the stress version of AC#1; one test may cover both with N>1.)
- **AC#3 — addressability + defined errors.** Table-driven:
  - Push to a `connID` never seen → `errors.Is(err, ErrConnNotFound)`.
  - Push to a session driven only partway (awaiting-init / handshake-complete, never reaching open) → `errors.Is(err, ErrSessionNotOpen)`.
  - Push to a session that was opened then closed (e.g. after a tampered-frame 4421 teardown deletes it) → `errors.Is(err, ErrConnNotFound)`.
  - Assert no panic and that an unrelated open session on a *different* `conn_id` is unaffected (its subsequent solicited round-trip still decrypts) — pins "no mutation of another session's state."
- **AC#4 — reuses noise_msg + existing envelope, same decode path.** Implicit in AC#1/AC#2: the phone decodes the pushed frame through `decryptAppFrame` (the same helper that decodes solicited replies) and gets a valid `protocol.Envelope`. Add an explicit assertion that the pushed frame's inner type round-trips (e.g. a `message` envelope decodes to `Type == protocol.TypeMessage` with its payload intact). No new inner-frame or envelope type appears.
- **AC#5 — no regressions.** The entire existing `internal/relay` v2 suite must pass unmodified. The change is additive (new channel, arm, methods); no existing test is touched. Run `go test -race ./internal/relay/...` and `go test -race ./...` to confirm.
- **Ctx-cancellation.** A `Push` whose ctx is already cancelled returns `ctx.Err()` without blocking (cheap unit test against a manager whose `Run` is parked).

## Open questions

- **Outbound envelope-ID policy for unsolicited frames (resolve in #572).** `Push` leaves `env.ID` to the caller. v1's bridge assigns a per-conn monotonic `c.NextID()`; v2 has no per-session outbound counter (manager-internal frames use fixed IDs 1/2). #572 must decide whether a per-session monotonic ID is needed for `message` envelopes or whether `MessagePayload.MessageID` (a UUID, already present) suffices for phone-side ordering/dedup. If a manager-owned counter is wanted, it is a small follow-up addition to `V2Session`; it is **not** needed for the push primitive itself. Recommend #572 start with a simple caller-side counter and only escalate if the phone protocol requires monotonic envelope IDs.
- **Connected-session enumeration for fan-out (owned by #572).** #572 needs the set of currently-open `conn_id`s to broadcast. That is a cross-goroutine read of `m.sessions` requiring its own snapshot funnel (e.g. an `ActiveConnIDs() []string` method routed through the same `select`, or a connect/disconnect event surface). Deliberately out of scope here to keep #571 the minimal addressable-push primitive; #572's own architect run designs it.
- **CipherState zeroisation.** Unchanged from the rest of the package: no explicit `Wipe()` of old key bytes; the single-owner-goroutine invariant is the practical zeroisation property (`v2session.go:138-147`). `Push` introduces no new key-handling surface beyond reading the live `s.send`.

## Security review

**Verdict:** PASS

**Findings:**

- **[Trust boundaries]** No findings. `Push`'s caller is in-process trusted code (the #572 assistant-turn bridge fanning server-originated assistant output); the input `env` is server-built, not network-parsed — there is no untrusted→trusted crossing introduced here. The pushed bytes cross *out* to the phone, AEAD-sealed under `s.send`. The one boundary `handlePush` enforces is the `V2StateOpen` gate (step 2): server output is delivered only to a token-validated, fully-open session, never to a `V2StateHandshakeComplete` peer that holds CipherStates but failed/skipped the token check.
- **[Tokens, secrets, credentials]** No findings. `Push` mints no tokens and reads no token material. The session's `device` snapshot and `peerStatic` are untouched. The Noise send key (`s.send`) is read at execution time and never copied, logged, or returned.
- **[Cryptographic primitives]** No findings — and this is the category the ticket exists for. The **nonce-reuse hazard** is closed structurally: every `s.send.Encrypt` (in-flight reply, rekey emit, error seal, and now push) executes on the single `Run` goroutine, serialised by `Run`'s `select`; the unbuffered `m.push` funnel forces a cross-goroutine push to wait its turn rather than racing the counter. No key reuse across purposes (the existing `s.send`/`s.recv` split is unchanged). Composition with the re-key swap is safe: `handlePush` reads `s.send` after any swap that `Run` already applied, never a torn or stale pointer (§ Concurrency model). No hand-rolled crypto; reuses `internal/noise` verbatim.
- **[Concurrency]** No findings. No new locks (consistent with the documented no-mutex contract), so no lock-ordering surface. No check-then-mutate on shared state outside the `Run` goroutine — the lookup, state check, and seal all run on `Run`. Goroutine lifecycle: zero new goroutines; in-flight `Push` callers unblock via `ctx.Done()` on shutdown; `m.push` is intentionally not closed (mirrors `manualRekey`), so no send-on-closed-channel panic. Backpressure is bounded (unbuffered channel), so a hostile/buggy caller flooding `Push` cannot grow an unbounded queue — it only serialises and slows that caller.
- **[Error messages, logs, telemetry]** No findings, with an explicit MUST for the developer captured in the spec: `handlePush` emits **no** log line carrying `env`, plaintext, ciphertext, or key bytes. Lookup-miss / not-open return bare sentinels (no log). Seal/marshal errors return wrapped errors whose text is the operation name only (e.g. `"seal push envelope: %w"`), never the payload — matching the package's `dispatchAppFrame` / `emitRekeyRequest` "do not log the AEAD ciphertext" discipline. The transport-drop debug log in `m.send` logs `conn_id` + `close_code` + transport `err` only, unchanged.
- **[Network & I/O]** No findings. No new socket reads; the outbound frame reuses `marshalInnerFrameV2` (the existing `maxNoisePayloadBytes` size discipline applies on the *inbound* decode path and is unaffected). `Push` forwards through the existing `m.send`→`Outbound` path with its established disconnected-drop semantics.
- **[File operations]** N/A — `Push` performs no filesystem operations.
- **[Subprocess / external command execution]** N/A — no subprocess interaction.
- **[Threat model alignment]** Addresses `docs/protocol-mobile.md` § Re-key's nonce-ordering requirement for server-initiated frames (the funnel guarantees ordered nonce advancement across solicited replies, rekey emits, and unsolicited pushes). The `V2StateOpen` gate aligns with the spec's "token-validated before app traffic" requirement by refusing pushes to un-authenticated sessions. Enumeration/broadcast threats (e.g. a bridge fanning to a wrong/stale `conn_id`) are out of scope and named as #572's concern; `Push`'s `ErrConnNotFound` on an unknown/closed `conn_id` is the defensive floor that prevents cross-session delivery.

**Reviewer:** architect (self-review per `architect/security-review.md`)
**Date:** 2026-06-07
