# Spec #610 — Backpressure + droppable-delta policy on the per-session push queue

**Ticket:** #610 (EPIC #596 Phase 2 structured streaming). **Size:** S (held; not downgraded — see § Sizing).
**Label:** `security-sensitive` — the security-review pass (per `architect/security-review.md`) is mandatory and appended at the end of this spec.
**Depends on:** #633 (structured-stream live wiring, CLOSED/merged) — the producer goroutine this layer protects is live on `main`.

## Context

Phase 2's structured emitter (#632) fans interactive envelopes to capability-granted phones by calling `(*relay.V2SessionManager).Push` **synchronously**: the public `Push` enqueues a `pushReq` onto the unbuffered `m.push` channel and **waits for the reply**, which Run sends only *after* `handlePush` has sealed the envelope under `s.send` and forwarded it via `m.send` → `Outbound`. Because `transport.Client.Send` blocks on an unbuffered `sendCh` until the prior `conn.Write` drains (≤ `WriteTimeout`), a congested-but-alive relay blocks `Outbound` → blocks Run inside `handlePush` → blocks the `Push` caller for the full seal+send. The `Push` caller is #633's producer goroutine draining the session JSONL; wedging it is exactly the ADR-025 open risk (line 220).

`assistant_delta` is loss-tolerant: a later `turn_end` (and, eventually, #611 reconnect replay) reconciles the phone's view, so dropping the oldest queued deltas to keep the queue moving is safe. Control events (`turn_state`, `tool_use`, `tool_result`, `turn_end`, `stall`, and the coarse `message`) carry state the phone cannot reconstruct from later traffic — never drop them.

This ticket makes the push surface **non-blocking under pressure** and adds the **event-class-aware drop policy**. It does not touch #609's coalescing, #611's reconnect replay, or the capability gate / fan-out in `emit`.

## Design

### The one real design wrinkle (and why it shapes everything)

The package's load-bearing invariant: **`s.send` / `s.recv` / `s.state` / `m.sessions` are touched only on the single `Run` goroutine, no mutex** — because flynn/noise's `CipherState` nonce counter is not concurrency-safe (struct doc, 158–167, 353–367). The seal (`s.send.Encrypt`) therefore **must** stay on Run. So the buffer cannot be drained-and-sealed by a second goroutine.

Two consequences pin the whole design:

1. **Drop before seal.** The Noise send nonce is strictly sequential; the phone's `recv` CipherState expects nonces `n, n+1, …` with no gaps. A dropped *sealed* frame is unrecoverable — the next frame MAC-fails and the conn dies at 4421. **Therefore the queue holds *unsealed* `protocol.Envelope`s and the drop policy runs pre-seal**; sealing happens only on the frames that will actually be sent, in order, on Run. (This is the same property today's `handlePush` has: "a push either seals fully under the old key or fully under the new key, never a torn read" — buffering unsealed envelopes composes with re-key for free.)

2. **Decouple enqueue from the slow `m.send`.** The producer must not wait for the seal+send. So the public `Push` enqueues into a per-session bounded buffer and **returns immediately**, never round-tripping through Run. Run drains the buffer (seal+send) on its own schedule, **one envelope per drain pass**, so a single slow `Outbound` interleaves with — never monopolises — Run's servicing of `ActiveConns`/inbound frames/wakes.

> **The seal stays single-writer on Run; the buffer + drop policy + a leaf mutex live in front of it; Run drains incrementally.** That is the reconciliation the ticket names. The buffer is the easy half; the leaf-mutex registry that lets an off-Run `Push` reach the right session's queue *without* reading Run-owned `m.sessions`, plus the one-per-pass drain that keeps Run responsive, is the real work.

### New types and manager fields — `internal/relay/v2session.go`

A per-session bounded FIFO of unsealed envelopes (unexported; **no exported type added**):

```go
type queuedEnv struct {
    env       protocol.Envelope
    droppable bool // env.Type == protocol.TypeAssistantDelta
}
type pushQueue struct {
    items   []queuedEnv // FIFO; bounded per the drop policy
    dropped uint64      // observability counter (no app content)
}
```

`V2SessionManager` gains (mirroring the existing channel-field doc style, 372–409):

- `pushMu sync.Mutex` — **leaf lock.** Guards `queues` (the map) and every `pushQueue`'s contents. Held only around lookup + enqueue/pop; **never** held across `Encrypt`, `m.send`, or any channel op, and never nested with any other lock.
- `queues map[string]*pushQueue` — connID → queue. **Key set is Run-managed** (created at session open, deleted at close); `Push` only mutates a queue's *contents*.
- `drainCh chan struct{}` (capacity 1) — "some queue has work." Non-blocking sends from `Push` and from the drain re-signal coalesce into at most one pending wake.

Remove the `push chan pushReq` field and the `pushReq` type. `NewV2SessionManager` initialises `queues` (`make(map[...])`) and `drainCh` (`make(chan struct{}, 1)`).

New constant (single named value, tunable, documented; not config — no AC asks):

```go
// pushQueueCap bounds the per-session push buffer. Starting value pending the
// ADR-025 load test (line 220). Post-#609 deltas arrive per-message/~250 ms,
// so 256 gives ample headroom to ride out one WriteTimeout window without
// dropping, while bounding worst-case per-session memory.
const pushQueueCap = 256
```

### Drop policy — `pushQueue.enqueue` (called under `pushMu`)

`droppable := env.Type == protocol.TypeAssistantDelta`. The method returns whether a drop occurred (so `Push` can log after releasing the lock). Behaviour:

| state at enqueue | incoming | action |
|---|---|---|
| `len < cap` | any | append. |
| `len == cap`, a delta is queued | delta | **evict the oldest queued delta**, append the new delta (AC#2: most-recent text retained). |
| `len == cap`, a delta is queued | control | **evict the oldest queued delta**, append the control event (AC#3: control admitted by evicting a droppable, never by dropping control). |
| `len == cap`, no delta queued (all control) | delta | **drop the incoming delta** (loss-tolerant; cannot evict a control event). |
| `len == cap`, no delta queued (all control) | control | **admit past nominal cap** (documented soft overflow — see decision below). |

Invariants: the method only ever *removes* entries and appends at the tail, so the relative order of every surviving envelope is preserved (AC#4). Each drop increments `dropped`.

**Design decision — soft overflow for control in the all-control-saturated case.** The trilemma *bounded ∧ never-drop-control ∧ never-block-producer* is unsatisfiable when the queue fills entirely with control events; one guarantee must yield. We yield "strictly bounded": admit the control event past nominal `cap` rather than drop it or block the producer. Rationale: in every reachable state under the realistic invariant that the high-volume stream is deltas, `len ≤ cap` holds; the all-control case requires a *connected-but-very-slow* relay sustained across hundreds of control events with zero interleaved text (during a true stall the relay drops to `ErrNotConnected` and Run drains instantly, so the buffer never fills). Control backlog is self-limiting by the turn protocol (O(1) control events per turn), and — critically — **the phone cannot drive control-event volume** (push is server→phone only; see Security review § Network & I/O). The alternative (drop the incoming control) loses state the phone cannot reconstruct, violating the ticket's hard guarantee. The soft overflow is a bounded-in-practice pressure-relief valve, logged at debug.

The drop policy is a pure function of (queue contents, incoming class) and is the primary unit-test surface — see § Testing.

### Public `Push` — rewritten (off-Run, non-blocking)

Contract (signature unchanged: `Push(ctx context.Context, connID string, env protocol.Envelope) error`):

1. If `ctx.Err() != nil`, return it (preserves the emitter's `ctx.Err()` teardown branch; the only error path that still depends on `ctx`).
2. `pushMu.Lock()`; `q, ok := m.queues[connID]`. If `!ok` → unlock, return `ErrConnNotFound`. Else `dropped := q.enqueue(env, …)`; unlock.
3. Non-blocking signal: `select { case m.drainCh <- struct{}{}: default: }`.
4. If `dropped`, debug-log (`conn_id`, running `dropped` count, `env.Type` class — **no payload**); return `nil`.

`Push` **never** touches `s.send`, `m.sessions`, or `Outbound`, and never blocks on the relay. It is safe to call from any goroutine.

**Error-contract change (documented).** `Push` now collapses "session not open" into `ErrConnNotFound`: a queue exists iff the session reached `V2StateOpen`, so a not-open or unknown conn has no queue. `ErrSessionNotOpen` is no longer returned by the public `Push` (it remains a package sentinel used by `Rekey` and by the Run-side forward). The **security gate is preserved** — the Run-side `forwardEnvelope` still checks `s.state == V2StateOpen` before sealing, so a buffered push to a conn that closed/de-authed before drain is dropped there, never delivered to an un-authenticated peer. Both callers only debug-log the error, so the change is invisible to them.

### Queue lifecycle (Run-owned)

- **Create:** in `handleNoiseInit`, at the success tail where `s.state = V2StateOpen` (855), add the queue: `pushMu.Lock(); m.queues[s.connID] = &pushQueue{}; pushMu.Unlock()`.
- **Delete:** in `closeWith`, alongside `delete(m.sessions, s.connID)` (1436): `pushMu.Lock(); delete(m.queues, s.connID); pushMu.Unlock()`. Any buffered-but-undrained envelopes are dropped (the conn is terminal). The close envelope itself is still sent synchronously via `m.send` (terminal, bypasses the buffer).

Because both maps are mutated only on Run, `queue-exists ⟺ session is V2StateOpen` holds for any Run-side observer. Re-key keeps the session `V2StateOpen` throughout, so no non-open-with-queue window exists.

### Run-side drain

Run's select replaces the push arm:

```
case <-m.drainCh:
    m.drainOnce(runCtx)
```

`drainOnce` contract — **pops at most one buffered envelope per pass**:
1. Under `pushMu`: find one non-empty queue (range `m.queues` — Go's per-range randomisation gives rough fairness across the realistically-tiny conn count), pop its head (FIFO), note `connID`. Record whether any queue is still non-empty. Unlock.
2. If nothing was popped, return.
3. Forward on Run via the existing seal path: `forwardEnvelope(runCtx, connID, env)` (today's `handlePush` body — look up `m.sessions[connID]`, require `V2StateOpen`, `s.send.Encrypt`, wrap, `m.send`). A returned error (session vanished / not open / seal failure) is debug-logged and the envelope dropped — no app content in the log.
4. If any queue still has items, non-blocking re-signal `drainCh`.

One `Outbound` per pass is the "slow `m.send` must not re-block the producer" guard: Run returns to its `select` between sends, so `ActiveConns`, inbound frames, and wakes are serviced with at most one in-flight `Outbound` (≤ one `WriteTimeout`) of delay — instead of a whole-buffer drain. The cap-1 `drainCh` + re-signal is a self-perpetuating pump with no lost wakeups: a `Push` that enqueues while Run is mid-pass lands its signal into the (now-drained) channel and triggers the next pass.

### Data flow

```
#633 producer goroutine (drains JSONL; calls emit → ActiveConns, then Push per conn)
     │  Push(connID, env):  pushMu → enqueue(drop policy) → unlock → signal drainCh → return  (NEVER blocks on relay)
     ▼
m.queues[connID]  (per-session bounded FIFO of UNSEALED envelopes)
     ▲  drainOnce (Run goroutine, one env per pass):
     │     pushMu → pop head → unlock → forwardEnvelope (seal under s.send + m.send) → re-signal if more
Run select  ── interleaves drainCh with Frames / wake / manualRekey / snapshot ──▶ Outbound → relay
```

## Concurrency model

- **Seal stays single-writer.** Every `s.send.Encrypt` (handshake replies, app replies, rekey emit, snapshot replies, **and the drain**) runs on Run. `Push` never seals. The package's no-mutex-on-`s.send` invariant is preserved verbatim.
- **`pushMu` is a leaf lock.** Held only around map lookup + `enqueue`/pop (both O(cap), cap small). Released before any `Encrypt`/`m.send`/channel send. Never nested with `mu`/`state`/`convMu` or any other lock. Lock-order documentation: `pushMu` orders below nothing (it is taken alone).
- **Map ownership.** `m.queues` *keys* are created/deleted only on Run (open/close); `Push` mutates only queue *contents* under `pushMu`; `drainOnce` pops under `pushMu`. No reader sees a half-updated map. `m.sessions` remains Run-only and is never read by `Push`.
- **`drainCh` cap-1, non-blocking sends, single receiver (Run).** No lost wakeups (argued above); no goroutine blocks on it.
- **No new goroutine.** The drain is a Run select arm. Teardown: on `runCtx.Done()` Run returns; undrained buffers are GC'd with the manager. In-flight `Push` callers never block on Run, so none can wedge on shutdown.
- **`-race` clean by construction:** the only cross-goroutine shared state (`queues`, each `pushQueue`, `dropped`) is fully `pushMu`-guarded; `drainCh` is a channel.

## Error handling

- **No live conn / slow relay:** unchanged transport posture — `forwardEnvelope` → `m.send` → `Outbound` returns `ErrNotConnected`/`ErrDisconnected`, logged at debug and dropped (the v1 reconnect contract). The buffer means the producer never observes this.
- **Drop under pressure:** not an error — `enqueue` returns a `dropped` bool; `Push` returns `nil` and debug-logs the running count + type class.
- **`ErrConnNotFound`** (wraps `control.ErrConnNotFound`, preserving the wire-mapping invariant): no queue for `connID`. **`ctx.Err()`**: cancelled-at-entry only. No new sentinel, no new exported type.
- **Seal/marshal failure in `forwardEnvelope`:** realistically unreachable under correct flynn/noise; the envelope is dropped with a debug log carrying **no** `env`/plaintext/ciphertext/key bytes (existing posture).

## Testing strategy

Stdlib `testing`, `-race`, table/scenario-driven. The drop policy is a pure unit surface — cover the ACs there (fast, deterministic, no cipher harness); use one integration test for the end-to-end non-blocking guarantee.

**Unit tests on `pushQueue.enqueue`** (table-driven; build `protocol.Envelope`s with just a `Type`, no seal). Describe inputs + expected `items` order + `dropped`; the developer writes them in the file idiom:
- **Under cap** — mixed deltas + control below `cap` → all retained, FIFO order, `dropped == 0`.
- **Drop-oldest delta (AC#2)** — fill to `cap` with deltas, enqueue one more delta → the oldest delta is gone, the newest retained, order preserved, `dropped == 1`.
- **Control evicts a delta (AC#3)** — `cap` with at least one delta, enqueue a control event → a delta evicted, control appended at tail, `dropped == 1`, no control lost.
- **Control never dropped when deltas present** — interleaved full queue, push N control events → exactly N deltas evicted (oldest-first), every control event present, in order.
- **Order preserved across drops (AC#4)** — scripted interleave that forces several delta evictions → the surviving subsequence equals the enqueue subsequence with the evicted deltas removed.
- **All-control soft overflow (edge)** — fill `cap` with control, enqueue another control → admitted (`len == cap+1`), `dropped == 0`; enqueue a delta into an all-control-full queue → dropped (`dropped == 1`), queue unchanged.

**Integration test — non-blocking under a stalled `Outbound` (AC#1).** Add a stalling outbound double (a recorder whose `outbound` blocks on a release channel, then records). Drive a session to open (`driveToOpen`), stall the outbound, then from the test goroutine fire many `Push` calls and assert **each returns within a tight deadline** while the outbound is blocked (the producer is not wedged); assert the drop counter engaged once past `cap`; release the outbound and `waitForEnvelopes` + `decryptAppFrame` the survivors **in order** under the phone's `recv` state (proves drop-before-seal left no nonce gap and order is preserved end-to-end).

**Existing Push tests — disposition:**
- `…InterleavedWithReply_DecryptsUnderRace`, `…ConcurrentWithReplies_NoNonceCorruption` — **expected to pass unchanged.** They fire `Push` (now buffered) concurrently with inbound requests, then `waitForEnvelopes` (which polls) for all frames and decrypt in capture order, order-agnostic between reply/push classes. All seals still happen on Run, so nonce integrity holds; the 8 message-pushes are well under `cap` so none drop. Re-run under `-race` to confirm.
- `…UnknownConn_ErrConnNotFound`, `…ClosedSession_ReturnsErrConnNotFound` — unchanged (still `ErrConnNotFound`; verify `closeWith` deletes the queue so the closed-session case still finds no queue).
- `…NotOpen_ReturnsErrSessionNotOpen` — **update.** A white-box-injected non-open session has no queue → `Push` now returns `ErrConnNotFound`. Rename to `…NotOpen_ReturnsErrConnNotFound`, expect `ErrConnNotFound`, and (optionally) add a drain-path assertion that the security gate still holds: a buffered push to a session that is then closed is never forwarded. Add a comment that the `V2StateOpen` gate moved to `forwardEnvelope`.
- `…CtxCancelled_ReturnsCtxErr` — **minor update.** Still asserts `context.Canceled` (because `Push` checks `ctx.Err()` first), but the comment "with no Run goroutine draining `m.push`…" is obsolete — replace it with "Push checks ctx before the enqueue, so a cancelled ctx short-circuits without consulting the queues."

`make check` (vet + `-race` + staticcheck) green. No e2e: the producer→manager seam is unit/integration-covered here; the structured-receive capstone is #642 (blocked on the harness gap, out of scope).

## Sizing

**Held at S; not downgraded to XS.** Production: **1 file** (`internal/relay/v2session.go`), ~100–130 LOC net (the `pushQueue`/`enqueue` drop policy ~35, `drainOnce`/pop ~25, `Push` rewrite ~15, struct/const/`New` edits ~15, queue create/delete ~8, `handlePush`→`forwardEnvelope` mechanical rename across 3 in-file call sites). §4 production-file count = **1** — far under the ≥5 gate. **Zero new exported types** (`queuedEnv`/`pushQueue` unexported; `pushQueueCap` a const). **Zero forced consumer edits** — `Push`'s signature is unchanged, so the two callers (#589 bridge, #632 emitter) compile and behave unchanged (they debug-log the error). The rename is 3 same-file sites, not a cross-package cascade — **not refactor-shaped**, well under the 10-call-site line. Reject branches: the drop policy is 5 cases in one method (counted above), well under the ≥10 state-machine line. Total written work (production ~120 + tests ~350–420 + this spec) projects comfortably under the 600-LOC ceiling: most AC coverage is table-driven unit tests on a pure helper, and the two expensive race tests are expected to survive unchanged. The non-trivial cost that keeps this S rather than trivial is the single-`Run`-goroutine reconciliation (drop-before-seal + leaf-mutex registry + one-per-pass drain), not LOC or fan-out — exactly as the ticket framed it.

## Open questions

- **Capacity calibration.** `pushQueueCap = 256` is a starting value; ADR-025 line 220 calls for a real load test to calibrate it. Count-based bounding (envelopes, not bytes) is the natural reading of "fixed capacity"; if large coalesced deltas prove a memory concern under load, byte-bounding is a contained refinement — deferred to that load test, not this slice.
- **Soft overflow vs. strict cap.** Documented above as a deliberate decision (yield "strictly bounded" to preserve never-drop-control + never-block-producer in the unreachable-in-practice all-control case). A future reviewer who prefers a hard cap would have to choose which other guarantee to drop; do not silently change it.
- **`ActiveConns` is still a Run round-trip.** The producer's per-`emit` `ActiveConns(ctx)` serialises through Run and can wait up to one in-flight `Outbound` (≤ one `WriteTimeout`). Fully decoupling *reads* from a slow `Outbound` would need a lock-free session snapshot or async `m.send` everywhere — larger blast radius, **out of scope** (this ticket's AC is the *enqueue* path). The one-per-pass drain bounds that wait to a single send rather than a full-buffer drain.
- **Dropped-counter surfacing.** The per-session `dropped` counter is logged at debug on drop (the ADR-025 "make the drop policy observable" note). Exposing it as a metric/snapshot field is deferred — no AC asks, and it would widen the surface.
- **Drop-on-close (deliberate).** Buffered envelopes for a closing conn are discarded (the conn is terminal; #611 reconnect replay, not this ticket, reconciles a returning phone). Do not add a drain-on-close flush.

## Security review

**Verdict:** PASS

This pass was run adversarially against the spec above (per `architect/security-review.md`), assuming the spec has holes. The push surface is **server → phone, outbound**: the producer (the daemon's #633 JSONL drainer and #632/#589 emitters) is the trusted source, and the phone supplies **nothing** into the queue — its only lever is reading slowly. That framing drives most findings.

**Findings:**

- **[Trust boundaries] No MUST FIX.** The only authn-relevant boundary is "deliver only to an authenticated (`V2StateOpen`) conn." It is preserved and explicit at the Run-side `forwardEnvelope` `s.state == V2StateOpen` check (today's `handlePush`, v2session.go:1595). The error-contract change (public `Push` collapses not-open → `ErrConnNotFound`) does **not** weaken it: a not-yet-open conn has no queue, so `Push` enqueues nothing; a conn that de-auths/closes between enqueue and drain is dropped at `forwardEnvelope`. The `connID` reaching `Push` in production comes from `ActiveConns` (open-only), never from the phone. Adversarial check (handshake-complete-but-token-unvalidated session): no queue exists → `Push` refuses → double-gated by `forwardEnvelope`. No leak.

- **[Cryptographic primitives] No MUST FIX — load-bearing invariant pinned.** The single most important property is **drop-before-seal**: the Noise send nonce is strictly sequential, so dropping an *unsealed* envelope is safe but dropping a *sealed* frame would gap the phone's `recv` nonce → MAC failure (indistinguishable from tampering) → 4421 close. The design holds unsealed envelopes and runs the drop policy pre-seal (Design consequence #1); the integration test decrypts survivors **in order** under the phone's `recv` state, which pins "no nonce gap." Re-key composition is safe: a buffered envelope seals under whatever `s.send` is current at drain; a successful re-key is same-peer only (peer-static continuity, `bytes.Equal`, v2session.go:926) and resets nonces on both sides, so no key/nonce reuse and no cross-peer delivery. No hand-rolled crypto; the only randomness (Go map-range order for drain fairness) is non-security.

- **[Network & I/O] No MUST FIX — this ticket *closes* a DoS risk.** The bounded per-session queue + non-blocking `Push` are the guard for ADR-025's open risk (line 220: a slow relay must not wedge the daemon). DoS reasoning: (a) the queue is bounded, capping delta memory; (b) the soft-overflow is **not phone-drivable** — control events are daemon-originated; the phone cannot inject them; (c) queues are **per-session** (`map[connID]`), so one phone's backpressure cannot touch another conn or the daemon; (d) a hostile phone reading slowly can at worst drop *its own* deltas (degraded self-view) and, in the unreachable-in-practice all-control-saturated case, grow *its own* bounded control backlog — never wedge or OOM the daemon. `Outbound` blocking is bounded by the transport `WriteTimeout` then fast-fails to `ErrNotConnected`; a truly-dead conn is reaped by the existing ping/pong heartbeat. No new network surface.

- **[Error messages, logs, telemetry] No MUST FIX (one developer constraint).** The two new debug logs (drop in `Push`, drop in `drainOnce`) MUST carry only `conn_id`, the `dropped` count, and `env.Type` (a closed protocol type-name constant, e.g. `"assistant_delta"` — not content). **Never log `env.Payload`, plaintext, ciphertext, or key bytes** — consistent with the package's existing no-app-content discipline (e.g. the AEAD-fail log at v2session.go:1044 deliberately omits the error text). `forwardEnvelope`'s error log inherits the existing wrapped-error-without-bytes posture. Spec mandates this; code-review must confirm `env.Type` is the only envelope-derived field logged.

- **[Concurrency] No MUST FIX.** `pushMu` is a leaf lock, taken alone, never nested, and released before any `Encrypt`/`m.send`/channel send — no deadlock path. All cross-goroutine shared state (`queues`, each `pushQueue`, `dropped`) is fully `pushMu`-guarded; `m.sessions`/`s.send` stay Run-only. Lost-wakeup analysis for the cap-1 `drainCh` + re-signal pump: every enqueue is followed by a signal attempt; whether the signal lands or hits `default` (channel already pending), a future drain pass acquires `pushMu` *after* the enqueue's release and therefore observes the queued envelope — no envelope can be stranded. Shutdown: no new goroutine; in-flight `Push` never blocks on Run, so none wedge on `runCtx` cancel; orphaned buffers GC. `-race`-clean by construction.

- **[Threat model alignment] No MUST FIX.** Addresses ADR-025 § Backpressure/replay (line 128: `assistant_delta` drop-oldest, control never drops) and closes the line-220 open risk. The phone's recovery of dropped deltas (mid-turn reconnect replay) is **OUT OF SCOPE → #611** (named in the ticket and § Open questions).

- **[Tokens/secrets] N/A** — no token/secret/credential handling added; the seal reuses the existing `s.send` CipherState unchanged; no key material is logged.
- **[File operations] N/A** — the buffer is purely in-memory, GC'd with the manager; nothing is persisted to disk; no path handling.
- **[Subprocess execution] N/A** — no `exec`, no shell, no external command.

**Reviewer:** architect (self-review per `architect/security-review.md`)
**Date:** 2026-06-08
