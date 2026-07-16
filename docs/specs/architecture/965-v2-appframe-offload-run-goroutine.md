# #965 — Offload app-handler execution off the V2SessionManager Run goroutine

**Size:** S · **Security-sensitive:** yes (Noise send-CipherState single-ownership on an internet-exposed surface) · **Extends:** #909

## Files to read first

Read these before writing code. Code refs are current as of this spec (main @ `becde57`).

- `internal/relay/v2session.go:436-473` — **`Run` select loop.** The eight existing arms and the single-goroutine-actor contract. You add one arm here (`m.appReply`).
- `internal/relay/v2session.go:623-723` — **`dispatchAppFrame`.** Two halves: the control-envelope discriminator (`:640-680`, stays on Run) and the spawn-Route-then-blocking-drain loop (`:682-722`, *this is what blocks Run for the handler's whole duration* — the body moves off-Run). This is the surgical center of the ticket.
- `internal/relay/v2session.go:725-751` — **`forwardAppReply`.** The seal-and-forward body (`s.send.Encrypt` → wrap → `m.send`), doc-commented "MUST run only on the Run goroutine" (#909). You add a `V2StateOpen` gate; the call site moves from the inline drain to the new `m.appReply` arm — still on Run.
- `internal/relay/v2session.go:938-1035` — **`drainOnce` + `Push`.** The *exact* pattern to mirror for the new reply arm: per-conn buffered queue mutated off-Run under a leaf lock, a cap-1 `drainCh` non-blocking wake, seal on Run one-item-per-pass. Read the field docs on `drainCh`/`replayCh` (`:341-357`) — the new `m.appReply` field's doc should match this house style.
- `internal/relay/v2session.go:154-278` — **`V2Session` struct + Run-owned-field doc convention** (see `lastActivityAt:243`, `replayQueue:268`). Add `appFrames` + `done` fields following this style.
- `internal/relay/v2session.go:301-388` — **`V2SessionManager` struct.** Add the `appReply` channel field.
- `internal/relay/v2session.go:799-850` — **`closeWith`.** Where per-session teardown lives (timers stopped, push queue deleted, session removed from map). Add the worker-stop (`close(s.done)`) here, symmetric with the `m.queues` delete.
- `internal/relay/v2session_handshake.go:293-357` — **`handleNoiseInit` success tail.** Where `s.device`, `V2StateOpen`, the push queue, and the idle timer are set up. Create `s.appFrames`/`s.done` and spawn the worker here — *after* `s.device` is set and state is `V2StateOpen` (establishes the happens-before the worker relies on).
- `internal/relay/v2session_handshake.go:419-438` — **`handleNoiseMsg`, `V2StateOpen` case.** `s.recv.Decrypt` runs on Run here (stays on Run — recv-cipher is single-owner too), then calls `dispatchAppFrame`. **This call is unchanged** — only `dispatchAppFrame`'s body changes.
- `internal/relay/v2session_seams.go:244-260` — **`V2SessionConfig.Handlers` doc.** Its SECURITY note ("handlers run on the manager's single dispatch goroutine … drained before `dispatchAppFrame` returns") is already stale post-#909 and this ticket falsifies it further. Rewrite it to describe the worker model.
- `internal/relay/handlers/create_conversation.go:53-59` and `:162-164` — **stale "per-conn goroutine" comments** (the retired v1 dispatcher's model). Required comment fix per the ticket.
- `internal/relay/v2session_appframe_test.go` — **#909 test harness to reuse:** `prolificHandler`, `appFrameCount`, `driveToOpen`, `v2Recorder`, `sealAppFrame`, `waitForOutboundCount`, `decryptAppFrame`, the `openSession` struct (`frames`/`initSend`/`initRecv`). Your AC tests extend this file.
- `internal/relay/v2session_modal_test.go` — modal deny-on-timeout harness for AC-1(b).
- `docs/knowledge/codebase/909.md` — the partial fix, the **interleave-drain pattern**, the `forwardAppReply` single-owner invariant, and the "Out of scope: head-of-line blocking" bullet that *is* this ticket. Read-only.
- `docs/specs/architecture/909-v2-appframe-concurrent-drain.md` — mirror its security-review structure.
- `docs/knowledge/features/v2-session-manager.md` § Concurrency / § Out of scope — the evergreen narrative to stay consistent with. **Read-only — documentation phase owns it.**

## Context

`V2SessionManager.Run` (`v2session.go:436`) is a single-goroutine actor: one `select` over inbound frames, per-session timers (`wake`, `modalTimeout`), manual rekey, the push drain, reconnect, replay, and snapshot. Every Noise cipher operation — `s.recv.Decrypt` on inbound, `s.send.Encrypt` on every outbound seal — happens on this goroutine, because flynn/noise `CipherState`s are single-owner (a concurrent `Encrypt` reuses a send-nonce, corrupting the encrypted wire and forcing a 4421 close of a live session).

#909 moved `dispatch.Route` (→ handler → `c.Send`, which is a marshal + channel push, **no AEAD**) onto a short-lived per-frame goroutine, keeping the seal on Run. But `dispatchAppFrame` still **blocks Run until Route returns** — its drain loop (`:705-722`) is a private two-arm select with nothing else to do while the handler runs. So a slow handler (the concrete case: `create_conversation` → `pool.CreateIn` → `Activate` waits up to 30s for PTY readiness, `handlers/create_conversation.go:59`) parks Run for that whole duration. While parked, Run services none of its other arms: other connections' frames pile up in `Frames`, the modal deny-on-timeout timer can't fire, scheduled/manual rekey stalls, the idle sweep stalls. This is the head-of-line-blocking the retired v1 Dispatcher's doc comment warned about (`internal/dispatch/dispatch.go`), and #909 explicitly deferred it ("#909 fixed the reply-*count* deadlock, not this reply-*latency* stall").

**This ticket removes the blocking.** Handler execution moves onto a per-connection worker goroutine; the reply seal folds back into Run's main `select` via a new arm, so Run keeps servicing every other arm while a handler runs.

**Not a new goroutine-count regression, a bounded one:** goroutines = (open sessions, one worker each) + (in-flight `Route` calls, one short-lived each). Both bounded by open-conn count.

**File-overlap check:** the branch scan flagged `origin/feature/449` touching `v2session.go`. Verified **false positive** — #449 is CLOSED, has no open PR, is 1188 commits behind main, and its work (the re-key responder path) already shipped on main; its diff is against the pre-#964-split file layout. Not a live merge target. No block.

## Design

Keep the actor. Add per-connection sub-actors for the slow work; results flow back to the actor for sealing. This is the same "Run owns cipher, off-Run does non-AEAD work" split #909 established — extended from one frame to a per-conn FIFO stream.

### Data flow

```
Run (unchanged up to the seal decision):
  Frames → handleNoiseMsg → s.recv.Decrypt (ON RUN) → dispatchAppFrame:
    ├─ control type (rekey/modal/interrupt/new_session/dequeue/snapshot/
    │  debug_bundle/settings)?  → handle inline ON RUN         [unchanged, fast]
    └─ application frame        → NON-BLOCKING enqueue plaintext → s.appFrames
                                   then RETURN to the select     [Run is free]

per-conn worker  (appFrameWorker, off-Run, exactly one per open session):
  for each plaintext from s.appFrames, in FIFO arrival order:
      routeAppFrame: spawn Route goroutine (#909's shape, preserved);
                     drain its outbound channel → forward each reply to m.appReply
                     (Route → handler → c.Send = marshal + channel push, NO AEAD)

Run select arm (NEW):
  m.appReply → forwardAppReply(s, reply) → s.send.Encrypt (SEAL, ON RUN) → m.send
```

The control-envelope discriminator (`dispatchAppFrame:640-680`) **stays on Run**: those handlers touch `s.send`, session state, and timers, and are fast (keystrokes, a bounded screen render, queue ops). Only the `dispatch.Route` tail — where `send_message`/`create_conversation` block — moves off-Run. A control frame arriving while an app handler runs in the worker is serviced immediately on Run; that is the *intended* improvement (AC-1's "modal deny-on-timeout still fires; interrupt/rekey unblocked"), not an ordering violation.

### New/changed types and contracts (no bodies — see the test that pins each invariant)

- **`V2Session.appFrames chan []byte`** — per-conn FIFO queue of decrypted app-frame plaintexts. Buffered (`appFrameQueueDepth`, a new const; propose **16**, a generous margin over realistic request/response pipelining depth of 1–2, sized to bound inbound memory). **Never closed** (avoids close-vs-send races); the worker terminates via `s.done`/ctx, and the channel is GC'd with the session. Written only on Run (`dispatchAppFrame`); read only by the worker.
- **`V2Session.done chan struct{}`** — closed by `closeWith` to stop the worker. Follows the timer-cleanup pattern. Created alongside `appFrames` at open.
- **`appReplyMsg struct { s *V2Session; reply protocol.RoutingEnvelope }`** — unexported carrier on the new manager channel.
- **`V2SessionManager.appReply chan appReplyMsg`** — workers post sealed-pending replies here; Run's new arm seals them. Buffered small (propose `handlerOutboundBuf` = 8); unbuffered is also correct (pure backpressure onto the worker). Doc it in the `drainCh`/`replayCh` house style.
- **`appFrameWorker(ctx context.Context, s *V2Session)`** — the per-conn goroutine. Loop selecting on `{ <-s.done: return; <-ctx.Done(): return; pt := <-s.appFrames: <recheck s.done, abandon if closed> routeAppFrame(ctx, s, pt) }`. One frame fully processed (Route returned + all replies forwarded) before the next is dequeued → per-conn serialization.
- **`routeAppFrame(ctx, s, plaintext)`** — the body lifted verbatim from `dispatchAppFrame:682-722` (spawn `Route` on a sub-goroutine, drain `outbound` concurrently), with the one change that each reply is forwarded to `m.appReply` (via a small `forwardToRun` helper: blocking send with a `ctx.Done` escape) instead of calling `forwardAppReply` directly. Preserves #909's prolific-handler deadlock guard (the concurrent Route-goroutine + drain) unchanged.
- **`dispatchAppFrame`** — keeps the discriminator switch (`:640-680`) on Run; its tail becomes a **non-blocking** enqueue onto `s.appFrames` with an overflow branch (see Error handling). Update its doc comment (it currently describes the on-Run blocking drain).
- **`forwardAppReply`** — add a leading `if s.state != V2StateOpen { <debug-log>; return }` gate (mirrors `forwardEnvelope:1062`), so a reply produced by a worker whose session `closeWith` already tore down is dropped rather than sealed under a dead session. Reading `s.state` here is safe: `forwardAppReply` runs on Run.
- **`closeWith`** — add `close(s.done)` (guarded `if s.done != nil`), symmetric with the existing `m.queues` delete; the top-of-function `V2StateClosed` guard makes it close-once.
- **`handleNoiseInit` open tail** — after `s.device` is set and `s.state = V2StateOpen`, create `s.appFrames`/`s.done` and `go m.appFrameWorker(ctx, s)`. `ctx` here is the Run-derived `runCtx`.

## Concurrency model

**Goroutine inventory (per open session):** one long-lived worker (`appFrameWorker`) + at most one short-lived `Route` goroutine at a time (spawned per app frame inside `routeAppFrame`, dies when the handler returns). Plus the existing per-session timer callbacks.

**Ownership table:**

| State | Owner (writer) | Readers |
|---|---|---|
| `s.send` / `s.recv` (CipherStates) | **Run only** — decrypt in `handleNoiseMsg`, seal in `forwardAppReply`/`forwardEnvelope`/`sealError`/rekey | Run only |
| `s.state`, timers, `s.replayQueue`, `m.sessions`, `m.queues` keys | Run only | Run (`forwardAppReply` state gate is a Run read) |
| `s.device`, `s.connID` | set-once on Run before `V2StateOpen` | worker (immutable after open; the `go` at open is the happens-before edge) |
| `s.appFrames` (contents) | Run (`dispatchAppFrame` send) | worker (receive) — safe concurrent send/recv, never closed |
| `s.done` | closed by Run (`closeWith`) | worker |
| `m.appReply` (contents) | workers (send) | Run (receive + seal) |

**Single-owner cipher — the load-bearing invariant (AC-3):** the worker runs only `Route → handler → c.Send` (marshal + channel push, no AEAD). It never touches `s.send` or `s.recv`. Every seal stays on Run: control replies inline, app replies via the `m.appReply` arm, push events via `drainOnce`, replay via `drainReplayOnce`, snapshot via `forwardEnvelope` — all Run arms, serialized by the select, so the send-nonce advances monotonically and never races. Inbound `s.recv.Decrypt` also stays on Run (in `handleNoiseMsg`, before the enqueue), so the recv-nonce advances in strict frame-arrival order. `-race` is the proof (an off-Run seal or a concurrent CipherState touch fires the detector).

**Per-connection ordering (AC-2):** the worker is a single goroutine processing `s.appFrames` FIFO, and doesn't dequeue frame N+1 until frame N's `Route` returned and all its replies were forwarded → no two same-conn handlers run concurrently, and all of frame N's replies enter `m.appReply` before any of frame N+1's. `m.appReply` is a channel (FIFO across senders), so a single worker's sequential sends are received by Run in order → sealed in order. Cross-conn interleaving on the shared channel is fine (only per-conn order is required). Decryption order is preserved independently (all on Run, FIFO from `Frames`, which is per-conn-ordered by the transport).

**No deadlock:** Run's enqueue to `s.appFrames` is non-blocking (overflow → close, never a block). Run drains `m.appReply` as a select arm. The worker's send to `m.appReply` blocks at most until Run reaches that arm — and every Run arm completes quickly (the slowest is a single `m.send`, bounded by one WriteTimeout). No cycle: Run→`s.appFrames` never blocks; worker→`m.appReply` ↔ Run-drains-`m.appReply`.

**Shutdown / worker termination:** workers take `runCtx`; on Run exit `cancelRun()` fires and each worker returns via its `ctx.Done` arm (after its in-flight `Route` observes ctx and returns — handlers are ctx-aware, e.g. `create_conversation`'s 30s `Activate` respects ctx). Per-session `closeWith` closes `s.done`, terminating that session's worker without waiting for Run exit (no per-session goroutine leak under conn churn). A worker may briefly outlive Run's return while its final ctx-cancelled handler unwinds — the same posture as the existing timer-callback goroutines (which honor `runCtx` and "leave no goroutine behind" eventually). See Open questions on an optional `sync.WaitGroup` join.

## Error handling

- **Inbound queue overflow (backpressure).** The relay↔binary leg is one multiplexed WebSocket with no per-conn flow control, so per-conn backpressure must be synthetic. `dispatchAppFrame` enqueues non-blocking (`select { case s.appFrames <- pt: default: … }`); on the `default` (buffer full = `appFrameQueueDepth` app frames in flight for one conn, well beyond request/response norms) it calls `closeWith(ctx, s, StatusProtocolMismatch, nil)` with a distinct WARN reason (e.g. `app_frame_queue_overflow`). This bounds inbound memory deterministically, never blocks Run, and is self-inflicted only: frames are AEAD-decrypted under `s.recv` before reaching the queue, so only the authenticated phone can fill its own queue — no cross-conn or injection vector. Reuse 4421 rather than mint a new wire code (avoids a `docs/protocol-mobile.md` § Error-codes change; a pacing violation is protocol-adjacent). Blocking the enqueue is **rejected** (reintroduces cross-conn HOL — the exact regression); dropping the frame is **rejected** (silently strands a request/response). Overflow is a not-yet-observed path (evidence-based); the bound is the responsible floor for adding an inbound queue on an internet-exposed component, not a speculative feature.
- **Reply for a torn-down session.** A worker mid-handler when `closeWith` runs finishes, then forwards replies to `m.appReply`; `forwardAppReply`'s new `V2StateOpen` gate drops them (debug log, no seal). No nonce burned on a dead session.
- **Abandon buffered frames on close.** After `closeWith` closes `s.done`, the worker's dequeue re-checks `s.done` and abandons queued-but-unstarted frames (avoids spawning children/handlers for a conn being torn down). A handler already in-flight at close completes (unavoidable, ctx-bounded) — no worse than today, where the same handler runs to completion before the close-triggering frame is even read.
- **Seal/marshal failure** — unchanged from #909 (`forwardAppReply` WARN-drops, never emits an unsealed frame).
- **Transport down** — unchanged; app replies go through `forwardAppReply`→`m.send`, which debug-drops on a disconnected leg (the reconnect handles recovery).

## Testing strategy

Extend `v2session_appframe_test.go`; reuse the #909 harness. Test handlers that block **must** honor `ctx` (`select { case <-release: case <-ctx.Done(): }`) so `sess.stop` unblocks them at cleanup. Scenarios (bullets, not bodies — write them in the package idiom):

- **AC-1(a) — slow handler doesn't stall a different conn.** Two open sessions (A, B) on one manager. A registered handler blocks on a test-controlled channel. Conn A sends a frame of that type (its worker blocks). Conn B sends `list_conversations`. Assert B's reply is sealed + emitted on `Outbound` promptly (short deadline, no dependence on releasing A). Then release A and assert its reply arrives. *Harness note:* `driveToOpen` hardcodes `v2TestConnID`; driving a second conn to open likely needs a small `driveToOpenConn(connID)` variant (distinct conn_id + its own initiator keypair). Flag this as the one harness extension.
- **AC-1(b) — modal deny-on-timeout fires while a handler is blocked.** Using the `v2session_modal_test.go` harness, arm a modal with a short deny-timeout; block an app handler on conn A; assert `ModalResolver.ResolveTimeout` fires and the safe-deny emits on schedule (not deferred behind the blocked handler).
- **AC-1 (rekey witness).** With an app handler blocked, assert a scheduled or manual rekey still emits (`RekeyInterval` seam / `Rekey` call) — the "same non-blocking property covers rekey" clause.
- **AC-2 — per-conn serialization + reply order.** On one conn, a handler that records entry/exit (or gates on a barrier) proves handler N+1 does not enter until N exits (no concurrency). Send two app frames; assert the two sealed replies decrypt (via `initRecv`, which decrypts strictly in send-counter order — a reorder surfaces as an AEAD auth failure, per codebase/909.md) with `in_reply_to` in arrival order.
- **AC-2 regression — #909 prolific handler still green.** `TestV2Session_OpenState_ProlificHandler_NoDeadlock` must still pass unchanged (the concurrent Route-goroutine + drain is preserved inside `routeAppFrame`).
- **Overflow policy.** Fill one conn's `appFrames` past `appFrameQueueDepth` (a permanently-blocked handler + enough pipelined frames) and assert the conn is closed with 4421; assert other conns are unaffected.
- **AC-3 — race clean.** `go test -race ./...` and `make check` green; the AC-1/AC-2 tests double as the race fixture (an off-Run seal breaks the ordered-decrypt assertion under `-race`).

## Open questions

- **`appFrameQueueDepth` value.** Proposed 16. Realistic pipelining depth is 1–2; 16 bounds memory while never tripping legitimate bursts. Tunable if real usage shows larger legit bursts — a package var (lowercase) mirrors the `idleTimeout` test-seam idiom if the overflow test needs a sub-value.
- **`m.appReply` buffer.** Proposed 8 (`handlerOutboundBuf`). Unbuffered is equally correct (backpressure onto the worker). Developer picks; document the choice.
- **`sync.WaitGroup` join on Run exit.** Not required by the ACs; the existing timer-callback goroutines already terminate via `runCtx` without a join, and workers match that. If the "no goroutines outlive Run" doc claim is to be literal, a WaitGroup incremented at worker spawn and waited before `Run` returns is a clean add — deferred as a nicety.
- **Control-handler latency stays on Run (out of scope).** `handleDebugBundleRequest` assembles an in-memory archive (log ring + newest recording) on Run. Bounded, and not the ticket's named case (`create_conversation` via `dispatch.Route`). If a future profile shows bundle assembly stalling Run, the same worker mechanism can offload it — not now (evidence-based; unobserved).

## Security review

Adversarial self-review per the `security-sensitive` label. Mindset: assume a hostile authenticated phone and a hostile/observing relay; hunt for a way to reuse a nonce, seal off-Run, exhaust memory, or leak a secret.

### Trust boundaries
- **Inbound frame → plaintext.** Enforced by `s.recv.Decrypt` on Run (`handleNoiseMsg:420`), *before* anything reaches the new queue. Only a phone holding the session's recv key produces a valid plaintext. So the `s.appFrames` queue carries only authenticated bytes; the overflow path is self-inflicted per conn, never a cross-conn or unauthenticated injection vector.
- **Worker ↔ Run.** The worker crosses back only via `m.appReply` (routing envelope: conn_id + already-marshaled inner frame). It never holds `s.send`/`s.recv`/keys/state.

### Categories walked
- **Nonce reuse / send-CipherState (the label's core concern).** Every `s.send.Encrypt` remains on Run — app replies land on the `m.appReply` arm and seal there; control/push/replay/snapshot/rekey seals are already Run arms. Run is single-threaded, so seals serialize and the send-nonce advances monotonically. The worker provably never calls `Encrypt`. `s.recv.Decrypt` also stays on Run (before the enqueue), so recv-nonce order is preserved. **Verdict: single-ownership intact.** `-race` + the ordered-decrypt AC tests are the enforcement.
- **Ordering/replay across the seam.** Per-conn FIFO holds (single worker, FIFO channel, one frame fully drained before the next). A reordered seal would break the AEAD counter and fail the phone's decrypt — a loud failure, not silent corruption.
- **Memory exhaustion (DoS).** The new inbound queue is bounded (`appFrameQueueDepth`); overflow closes the conn. Without the bound this would be an unbounded-growth vector on an internet-exposed component — the bound is why it isn't. Off-Run `Route` goroutines are one-at-a-time per conn (worker serialization), so goroutine count stays bounded by open-conn count.
- **Use-after-close / dead-session seal.** `forwardAppReply`'s new `V2StateOpen` gate drops replies for a torn-down session; buffered frames are abandoned on `close(s.done)`. No seal, no nonce burn, no wire emission for a closed conn.
- **Secret-in-log discipline.** No new logged field carries plaintext, ciphertext, key bytes, or `peerStatic`. The overflow WARN and the drop debug logs carry only `conn_id` + a static reason/count — matching the package's existing outbound-drop posture.
- **Channel-close safety.** `s.appFrames` is never closed (GC'd with the session). `s.done` is closed exactly once (the `closeWith` `V2StateClosed` guard). Send-to-`s.appFrames` and `close(s.done)` both run on Run, serialized — no concurrent send/close.

### Verdict: **PASS**

No off-Run seal, no nonce-reuse path, no unbounded inbound queue, no dead-session emission, no secret leak. The offload preserves #909's single-owner-cipher property and adds a deterministic inbound bound. Proceed to implementation.
