# Spec #777 — Pace the v2 mid-turn-reconnect replay (one event per Run pass)

Ticket: [#777](https://github.com/pyrycode/pyrycode/issues/777) · Size: **S** · Label: **security-sensitive**

## Files to read first

- `internal/relay/v2session.go:1946-2002` — `replayMissed`: the unbounded inline loop this ticket paces. Note the ring/cursor read under `pushMu`, the gap→`emitResync` branch, the `s.replayThrough = min(afterID, newest)` clamp (#663), and the per-event `s.replayThrough = ev.ID` trailing-watermark advance. **The whole loop moves out; the ring read + gap + clamp stay.**
- `internal/relay/v2session.go:2362-2418` — `drainOnce`: the existing one-per-Run-pass yielding precedent to mirror exactly (pop one, forward, re-signal if more). This is the shape `drainReplayOnce` copies.
- `internal/relay/v2session.go:2438-2473` — `forwardEnvelope`: the shared seal-and-forward path. Read its dedup guard (`env.EventID != nil && *env.EventID <= s.replayThrough`) — the trailing watermark must keep this guard from self-dropping replay frames.
- `internal/relay/v2session.go:2301-2360` — `Push` + `drainCh` non-blocking-wake idiom (cap-1 + default + re-signal). `replayCh` copies this exactly.
- `internal/relay/v2session.go:712-736` — `Run` select; you add one arm. `internal/relay/v2session.go:1184-1212` — the `handleNoiseInit` success tail that calls `replayMissed` (the point where the conn becomes enumerable, then replay is triggered — the ordering argument hinges on this order).
- `internal/relay/v2session.go:263-375` — `V2Session` fields + the single-owner-goroutine doc; `:588-665` — `V2SessionManager` fields (add `replayCh` beside `drainCh`); `:2166-2213` — `closeWith` (add `s.replayQueue = nil` beside the timer nils).
- `internal/eventring/ring.go:44-58` — `eventring.Event` (the `replayQueue` element type); `:156-207` — `After` (returns a *copied* `[]Event` + `gap`) and `NewestID`. `:37-42` — `MaxEventsPerConversation = 1024`.
- `internal/relay/v2session_replay_test.go:59-136` — `waitConnOpen` + `reconnectScenario`: **the load-bearing harness invariant this change breaks** (see Testing strategy). `internal/relay/v2session_test.go:169` — `waitForEnvelopes(t, rec, n)`, the already-existing poll helper the harness must switch to. `:40-96` — `v2Recorder` (ordered `snapshot()`), `startManager`.
- `docs/specs/architecture/647-*.md` (if present) and `internal/relay/v2session.go:364-374` — the `replayThrough` dedup contract you must preserve.

## Context

The v2 session manager is single-goroutine — "the loop is the lock" (`V2SessionManager` doc, `internal/relay/v2session.go:573-587`). Every per-conn op is serialised through `Run`, which makes the Noise `CipherState` single-writer invariant structural: `s.send.Encrypt` and the re-key `s.send, s.recv = …` swap all happen on the one dispatch goroutine, so the send-nonce sequence can never be raced.

That model is correct **while every per-conn op is bounded-small**, and today all but one are: re-key/handshake ≈100µs (accepted "defer until observed" per spec #453), snapshot render ≤4KB (`tui-driver` `DefaultBufferCap`, dropped from this ticket as a non-issue). The one **unbounded** op is `replayMissed`: a phone reconnecting mid-turn with a stale `last_event_id` triggers `ring.After(...)` returning up to `MaxEventsPerConversation = 1024` events, and the current loop seals + forwards **every one inline in a single Run pass**. Until that finishes, `Run` services no other connection, inbound frame, wake, or snapshot request — a latent fairness cliff (filed from the Cross-Repo Code Review 2026-07-03).

The fix: pace `replayMissed` to forward **one event per Run pass**, mirroring the `drainOnce` push-drain precedent that was built for exactly this reason. This changes only *when* replay frames are forwarded (interleaved with the select), never *where* (still `s.send.Encrypt` on the Run goroutine) — so the seal single-writer invariant is untouched.

## Design

### Why a dedicated replay queue, not the existing push queue

The ticket floats two mechanisms: (a) feed replay events through the existing per-conn push queue so `drainOnce` interleaves them, or (b) a per-conn replay cursor the Run loop advances one event per pass. **Choose (b).** Two hard facts rule out (a):

1. **Drop policy gaps replay.** The push queue is bounded at `pushQueueCap = 256` with a drop-oldest-delta policy (`pushQueue.enqueue`, `:185-210`). A replay batch can be up to `MaxEventsPerConversation = 1024` events — 4× the cap. Routing it through the push queue would silently evict the oldest queued replay deltas, producing exactly the "partial, gap-ful replay" AC #2 forbids. A dedicated queue bounded only by what `ring.After` already materialised has no drop, no gap.
2. **The trailing watermark can't share the guarded path with a pre-set watermark.** `forwardEnvelope`'s dedup guard drops any envelope with `*EventID <= s.replayThrough`. Replay works only because `replayThrough` *trails one event behind* the frame being forwarded (`:1976-1980`). If replay frames flowed through the same path with `replayThrough` pre-set to `newest`, the guard would drop every one. Per-event advancement needs a dedicated drain that owns the advance step — which is (b).

### New state (both unexported; zero new exported surface)

| Symbol | Type | Owner | Doc-comment must state |
|---|---|---|---|
| `V2Session.replayQueue` | `[]eventring.Event` | Run goroutine (no lock/atomic — same regime as `state`, `replayThrough`) | Not-yet-forwarded replay tail; nil/empty ⇒ no replay in flight; bounded by ring retention (≤ `MaxEventsPerConversation`), never the push cap. |
| `V2SessionManager.replayCh` | `chan struct{}` (cap 1) | wakeup coordination only | Mirrors `drainCh`: "some session has a pending replay tail." Cap-1 + non-blocking send + re-signal = self-perpetuating pump, no lost wakeup. Carries no data — the queue lives on the session. |

`NewV2SessionManager` initialises `replayCh: make(chan struct{}, 1)` beside `drainCh`.

### Control flow

**`Run` select** gains one arm (place it beside the `drainCh` arm, `:730-731`):
```
case <-m.replayCh:
    m.drainReplayOnce(runCtx)
```

**`replayMissed`** — the inline `for _, ev := range events { … }` loop (`:1981-2001`) is replaced by *store-and-signal*. Everything up to and including `s.replayThrough = min(afterID, newest)` (the ring read, the `gap`→`emitResync` return, the #663 clamp) is unchanged. Then:
- if `len(events) == 0`: return (caught up — nothing to pace).
- else: `s.replayQueue = events`; non-blocking send on `m.replayCh` (cap-1 + `default`). Return immediately.

`replayMissed` still runs on Run inside `handleNoiseInit`, so it completes (a bounded ring read + a slice assignment) before Run returns to its select — this is what keeps replay events strictly *ahead of* any live event for the conn (see Concurrency model).

**`drainReplayOnce(ctx)`** — new method, structurally a copy of `drainOnce` over `m.sessions` instead of `m.queues` (no `pushMu` — `replayQueue` is Run-owned):
- Scan `m.sessions` for the first `s` with `len(s.replayQueue) > 0` (Go map-random order = rough cross-conn fairness, same as `drainOnce`). None → return.
- Pop head: `ev := s.replayQueue[0]`; zero the slot for GC; `s.replayQueue = s.replayQueue[1:]`.
- Reconstruct the replay envelope exactly as the current loop does (`:1982-1989`): `ID/Type/TS/Payload` from `ev`, `EventID: &id` with `id := ev.ID` a per-iteration local (never `&ev.ID`).
- `forwardEnvelope(ctx, s.connID, replay)`:
  - **error** → debug-log (same `v2.replay.frame_dropped` line, no app bytes) **and abandon the rest**: `s.replayQueue = nil`, return. (Session vanished / seal failed — mirror the current loop's early `return`.)
  - **success** → `s.replayThrough = ev.ID` (the trailing-watermark advance — identical to the inline loop).
- Re-signal:
  - if `s.replayQueue` is now empty → non-blocking send on `m.drainCh` (release the conn's live events the gate held back — see below).
  - if any session still has `len(replayQueue) > 0` → non-blocking send on `m.replayCh` (keep the pump running; covers concurrent multi-conn replays with one cap-1 channel, same as `drainOnce`'s `more`).

**`drainOnce` per-conn gate** — when selecting a queue to pop, **skip any `connID` whose session has a non-empty `replayQueue`**. `drainOnce` runs on Run, so reading `m.sessions[connID].replayQueue` is a lock-free Run-owned read (it already reaches `m.sessions` transitively via `forwardEnvelope`). The buffered live events stay in FIFO order in their push queue; when the conn's replay finishes, `drainReplayOnce`'s `drainCh` re-signal drains them. Recommended shape: read the set of replay-pending conn-ids from `m.sessions` **before** taking `pushMu`, then skip them inside the existing `pushMu`-guarded scan — keeps `pushMu` guarding only `m.queues`, consistent with its "taken alone" doc.

**`closeWith`** — add `s.replayQueue = nil` beside the timer nils (`:2183-2194`). Defensive/symmetric: the session is already `delete`d from `m.sessions` so `drainReplayOnce`'s scan can't find it, but nil-ing matches the timer-cleanup pattern and drops the slice for GC promptly.

### Data flow (mid-turn reconnect of conn A while a turn streams live)

```
handleNoiseInit(A) success tail  [Run pass N]
  ├─ A → V2StateOpen, queue created, A now enumerable
  └─ replayMissed(A, afterID): ring.After → events[3..1024]
        gap?  → emitResync, return         (unchanged path, no queue)
        else  → replayThrough = min(afterID,newest)
                A.replayQueue = events; signal replayCh; return
  Run returns to select ───────────────────────────────────────────►

[pass N+1..]  select fairly interleaves:
  replayCh → drainReplayOnce: forward ONE (e3), replayThrough=3, re-signal replayCh
  Frames   → conn B's noise_init serviced   ◄── B not blocked (AC #1)
  replayCh → forward e4 … (one per pass) …
  drainCh  → A's live events?  GATED (A.replayQueue non-empty) → skip, stay buffered
  …
  replayCh → forward e1024 (last); A.replayQueue empty; replayThrough=newest
             → signal drainCh
  drainCh  → A's buffered live events now drain, in order, all id > newest  (AC #2)
```

## Concurrency model

- **Ownership.** `replayQueue` and `replayThrough` are Run-owned — written/read only on the dispatch goroutine (`replayMissed`, `drainReplayOnce`, the `forwardEnvelope` guard, `closeWith`). No lock, no atomic; same regime the file's doc already asserts for `state`/`interactive`/`replayThrough`. `replayCh` carries `struct{}` (wakeup only) — it references no session, spawns no goroutine.
- **Seal single-writer (AC #3) — preserved, not weakened.** Every replay frame is still sealed by `s.send.Encrypt` inside `forwardEnvelope` on the Run goroutine. Pacing changes the *interleaving* of forwards with the select, never the goroutine. Two forwards for the same conn never run concurrently (both on Run), so the Noise send-nonce sequence is monotonic exactly as today. A re-key swap that lands between two replay forwards composes correctly: `forwardEnvelope` reads `s.send` at execution time, so each frame seals fully under whichever key is current (the property `:2430-2433` already documents).
- **Per-conn ordering (AC #2).** For a given conn, all replay frames forward via `drainReplayOnce` *before* any live frame via `drainOnce`, because: (1) `replayMissed` populates `replayQueue` on Run pass N, *before* Run returns to the select — and the conn only becomes visible to the off-Run live emitter through `ActiveConns`, which funnels onto Run and is serviced no earlier than pass N+1. So no live event can be in A's push queue before its `replayQueue` is populated. (2) The `drainOnce` gate keeps A's push queue un-drained until `replayQueue` empties. Result: replay ids (all ≤ `newest`, ascending) precede live ids (all > `newest`) on the wire. The `replayThrough` guard remains the belt for any straggler live dupe with id ≤ `newest`.
- **Cross-conn fairness (AC #1).** `drainReplayOnce` forwards exactly one frame then returns to the select; a fair (uniform-random) select services other conns' `Frames`/`wake`/`snapshot`/`drainCh` between forwards. B is delayed by at most one seal/forward — the same guarantee `drainOnce` already makes for the push stream.
- **No lost wakeup.** `replayCh` uses the `drainCh` idiom verbatim: cap-1 channel, non-blocking send, drain re-signals while work remains. A signal that lands while Run is mid-pass triggers the next pass; concurrent multi-conn replays are covered by the single channel because `drainReplayOnce` re-signals if *any* session still has a tail.

## Error handling

| Failure | Behaviour |
|---|---|
| `forwardEnvelope` returns error mid-replay (session vanished / seal failure) | Debug-log the existing `v2.replay.frame_dropped` line (no payload/ciphertext/key bytes), set `s.replayQueue = nil` (abandon remaining tail), return. Mirrors the current inline loop's early `return`. |
| `ctx`/Run teardown during a replay | `drainReplayOnce` runs under Run's `runCtx`; on Run exit the loop stops and the sessions map (with any `replayQueue`) is dropped. `replayCh` holds no goroutine, so nothing leaks — same as `drainCh`. |
| `closeWith` fires mid-replay (idle sweep, rekey-reply timeout, AEAD fail) | Session deleted from `m.sessions`; `drainReplayOnce`'s scan can no longer find it; `s.replayQueue = nil` drops the tail. Any queued-but-undrained live events are discarded with the push queue, exactly as today. |
| Gap (aged-out cursor) | Unchanged: `emitResync` emits one marker and returns *before* any queue is populated — the resync path never touches `replayQueue`, so AC #2's "exactly one resync marker, never a partial gap-ful replay" is structurally intact. |

No new error type, no new close code, no new log event key.

## Testing strategy

All new/changed tests live in `internal/relay/v2session_replay_test.go` (same package, table-driven where natural, stdlib only). `go test -race ./internal/relay/...` must stay green.

**Harness rework (load-bearing — do this first).** `waitConnOpen` + `reconnectScenario` (`:59-136`) assume "conn enumerable ⇒ replay already fully forwarded," which paced replay makes false (the tail drains over passes *after* the conn appears). Switch `reconnectScenario` to poll with the existing `waitForEnvelopes(t, rec, wantEnvs)` (`v2session_test.go:169`) instead of `waitConnOpen` + immediate `snapshot()`, and update the now-false `waitConnOpen` doc comment. The existing replay tests assert *final forwarded content* (ids/order/payloads), which is unchanged — only the wait mechanism moves. They should stay green after the swap:
- `TestV2Session_Reconnect_ReplaysMissedTail`, `_CaughtUp_NoReplay`, `_AbsentLastEventID_NoReplay`, `_ReplayDisabled_NoReplay`, `_OutOfRangeLastEventID_LiveStreamDelivered`, and the watermark-guard / resync tests.

**New scenarios (bullets — developer writes them in the project idiom):**
- **AC #1 fairness — a large replay does not block another conn.** Conn A reconnects with a large in-ring tail (e.g. `afterID=0`, ring holds ~200 events). While A's replay is draining, feed conn B's `noise_init` on `Frames`. Assert B's `noise_resp` is forwarded *before* A's final replay frame (in the recorder's ordered snapshot, B's frame index < A's last replay index). With ~200 A-events the "B serviced only after all of A" outcome has probability ≈ 2⁻²⁰⁰ under a fair select — deterministic in practice; note the statistical argument in a comment. (Proves Run returns to the select between replay forwards.)
- **AC #2 ordering under interleave.** A reconnects (replay `e_{k+1}..e_newest`); mid-replay, `Push` a live event `e_{newest+1}` to A. Assert A receives every replay event in ascending id order *before* the live event, with no duplicate and no gap. (Proves the `drainOnce` gate + `drainCh` re-signal.)
- **AC #2 dedup preserved.** Keep / extend the watermark-guard assertion: a live envelope with `EventID <= replayThrough` is dropped (no frame, no error) even with paced replay.
- **AC #2 resync unchanged.** Aged-out `last_event_id` still yields exactly one `resync` marker and zero replay frames (gap path never populates the queue).
- **AC #3 race.** A `-race` test driving a replay concurrently with live `Push`es to the same conn *and* traffic on a second conn, asserting no data race and no send-nonce violation (the run stays green; the interleaved forwards all seal on Run).
- **Pacing unit assertion (optional but cheap).** After `replayMissed` populates an N-event `replayQueue`, one `replayCh`-driven pass forwards exactly one frame and leaves N−1 queued — the direct statement of "one per Run pass." Drive via `Frames`/recorder rather than reaching into Run internals.

## Open questions

- **Cross-conn fairness under a reconnect storm.** Map-random selection across `m.sessions` is fair in expectation (matches `drainOnce`). If a many-conn simultaneous-large-replay pattern is ever *observed* to starve a conn, a round-robin cursor could replace map-random — but that is the same latent concern `drainOnce` already carries, so defer until observed (Evidence-Based Fix Selection). Not in scope.
- **No separate `replayQueue` cap.** The tail is already bounded by ring retention (≤ `MaxEventsPerConversation = 1024`); adding a second cap would only reintroduce the gap risk that motivated *not* using the push queue. Left uncapped by design; note it in the field doc.

## Security review (self-review — label `security-sensitive`)

This ticket reschedules AEAD-sealed frame dispatch (`s.send.Encrypt`) on the internet-exposed mobile surface and touches the Noise send-nonce single-writer invariant. Adversarial pass over the change:

- **Trust boundaries / untrusted input.** `afterID` (phone `last_event_id`) is the only attacker-controlled value, and this change does **not** move or weaken its handling: validation stays entirely in `replayMissed` *before* the queue is populated — `ring.After` range/ring-bounds it, the #663 `min(afterID, newest)` clamp caps the watermark to server-known reality, and the conversation is `cursor()`-resolved (never phone-named). A hostile-large `afterID` still classifies as caught-up (empty) or gap (one `resync`). No phone bytes enter `replayQueue` — it holds only server-produced `eventring.Event`s the authenticated conn is already entitled to stream. **No new untrusted-input path.** The whole replay path is gated behind `Devices.Validate` (token-OK tail of `handleNoiseInit`) — an unpaired peer never reaches it.
- **Nonce / seal single-writer (the catastrophic risk).** Every `s.send.Encrypt` still runs on the single Run goroutine via `forwardEnvelope`. Pacing changes *when* forwards happen (interleaved with the select), never *where* — two seals for one conn are never concurrent, so send-nonce monotonicity is preserved exactly as in the current inline loop. A re-key swap landing between two paced forwards composes because `forwardEnvelope` reads `s.send` at execution time (whole-old-key or whole-new-key, never torn; old state never re-read post-swap). Each event is popped once (`replayQueue[1:]`) and forwarded once; on error the tail is abandoned (`replayQueue = nil`), never retried — so **no double-seal / nonce-reuse** path exists. **Invariant preserved, not weakened (AC #3).**
- **Wire-order recv nonce.** Frames seal sequentially on Run in send order = wire order; the phone's recv nonce advances in that same order. Replay-then-live ordering (the gate) keeps the byte stream gap-free, so no MAC-failure/4421 is introduced by the reschedule.
- **Secret hygiene.** The only touched log line is the existing `v2.replay.frame_dropped` debug (conn_id + transport sentinel `err`); `replayQueue` / `replayCh` are never logged. No payload, ciphertext, or key bytes enter any new field.
- **Resource exhaustion / DoS — net-positive.** `replayQueue` is bounded by ring retention (≤ `MaxEventsPerConversation = 1024`) per conn; a phone cannot grow it past what the ring already holds. Held-back live events remain bounded by `pushQueueCap = 256` with the existing drop policy. Critically, the *purpose* of this change is that a large replay can no longer monopolize Run — so a paired-device reconnect storm is now **less** able to starve other conns than the pre-ticket inline loop. A slow replay delays only that conn's own live delivery, never another's.
- **Fail-closed.** Replay disabled (nil ring/cursor) → early return, no queue. Forward error → abandon tail (stop, don't loop/retry). Session closed/de-authed mid-replay → `forwardEnvelope`'s `V2StateOpen` gate applies to *every* paced forward, so a buffered replay frame for a conn that closed before its pass is dropped there, never sealed for an un-authenticated peer.

**Verdict: PASS.** No FAIL findings. The change is a Run-goroutine-local scheduling refactor that preserves every cryptographic invariant, adds no attacker-reachable input path, leaks no secrets, and improves DoS resistance. Residual risk is limited to the documented Open questions (cross-conn fairness under an observed storm), which is deferred per Evidence-Based Fix Selection.
