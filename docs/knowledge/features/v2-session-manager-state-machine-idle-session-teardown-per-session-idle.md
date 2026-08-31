# Idle-session teardown (#774) — per-session idle timer + in-repo sweep

The relay↔binary leg is a **single multiplexed WebSocket**: every phone's frames arrive on one `Connection.Frames()` channel keyed by `conn_id`, and there is **no per-connection disconnect frame**. When a phone drops or backgrounds (the protocol says phones close-on-background, then push-to-wake — `docs/protocol-mobile.md:872`), the binary receives *nothing* for that `conn_id`. Before this slice the stale `V2Session` — two Noise `CipherState`s + an armed 1-hour `rekeyTimer` — lingered until the scheduled rekey fired (up to an hour later), went unanswered, and only then closed at 4426. Under normal connect/disconnect churn these idle encrypted sessions accumulated. This slice bounds their lifetime with an **in-repo idle sweep**: an open session that receives no inbound frame within `idleTimeout` is torn down through the existing [`closeWith`](#noise_msg-in-v2stateopen--application-dispatch-and-aead-failure-teardown) path. (This is the ticket body's "option b" — the daemon side of the gap the 2026-07-03 cross-repo review flagged; the cross-repo alternative, a relay-emitted per-connection disconnect signal, does not exist in this repo and is a deferred future ticket. See [§ Out of scope](#out-of-scope-deferred).)

**The design constraint (why this is more than a bare timer).** `V2Session` cipher-state access is single-Run-goroutine-owned with no mutex — *the loop is the lock*. So the idle mechanism reuses the **existing** `time.AfterFunc → wakeSignal → handleWake` machinery the #450 rekey timer established: the timer callback does the actual teardown decision **on the Run goroutine**, never inside the callback. No new package, no new exported type beyond the close code, no new goroutine topology, no signature changes → zero consumer fan-out.

**New package-level values (mirror the `rekeyInterval` idiom).**

- `idleTimeout` — lowercase, test-overridable `var` defaulting to **15 min**. Well short of the 1-hour `rekeyInterval` so a dropped/backgrounded phone's cipher states never linger up to an hour; long enough not to tear down a foregrounded-but-momentarily-quiet phone mid-read (which would force a disruptive re-handshake on the next tap). Not yet config-driven — the same deferred posture as `modalDenyTimeout` (still a package var, not config-exposed). Tests substitute a sub-second value via `prev := idleTimeout; idleTimeout = …; t.Cleanup(func() { idleTimeout = prev })`.
- `wakeIdleTimeout` — new unexported `wakeKind` const joining `wakeRekeyEmit` / `wakeRekeyReplyTimeout`; the per-session timer-event discriminator carried on `m.wake`.
- `StatusIdleTimeout websocket.StatusCode = 4408` — the idle-teardown WS close code, echoing HTTP 408 (Request Timeout), consistent with the existing 44xx←HTTP convention (4401←401, 4404←404, 4409←409, 4429←429). Added to the § Error codes table in `docs/protocol-mobile.md` (direction: binary, forwarded by relay).

**New per-session state on `V2Session`** (Run-goroutine-owned, no lock, same regime as `rekeyTimer`):

- `idleTimer *time.Timer` — armed at `V2StateOpen` alongside `rekeyTimer`, rescheduled by `handleWake` when activity is recent, stopped+nil'd by `closeWith`. Nil before open; nil after `closeWith`.
- `lastActivityAt time.Time` — stamped on **every** inbound frame in `handleFrame`. The idle deadline the timer chases. Run-owned; never crosses the wire, so the monotonic reading is retained (PROJECT-MEMORY's `time.Time` round-trip discipline does not apply).

**Mechanism: a single-arm timer that chases the last-activity deadline.** Chosen over "re-arm on every inbound frame" (keeps `handleFrame`'s hot path to a single field write — no per-frame `Stop`+re-alloc churn) and over "periodic sweep over the sessions map" (that needs a second duration + a separate channel; the AC asked for **one** test-overridable window in the `rekeyInterval` idiom). `armIdleTimer(ctx, s, d)` copies `armRekeyTimer`'s callback shape exactly — its `AfterFunc` callback does only a non-blocking `select { case m.wake <- wakeSignal{s, wakeIdleTimeout}: case <-ctx.Done(): }`, touching **only** `m.wake` (never `s.send`/`s.recv`/`s.state`/`m.sessions`), so it cannot race the cipher states.

```
1. Arm at open   — handleNoiseInit success tail, next to s.rekeyTimer = m.armRekeyTimer(ctx, s):
                     s.idleTimer = m.armIdleTimer(ctx, s, idleTimeout)
2. Stamp on frame — top of handleFrame, right after the V2StateClosed early-return
                     (a torn-down conn's late frame is dropped first): s.lastActivityAt = time.Now()
3. Fire → decide  — handleWake's wakeIdleTimeout arm, AFTER the existing `if state != Open { return }` guard:
                     idle := time.Since(s.lastActivityAt)
                     idle <  idleTimeout → reschedule for (idleTimeout - idle), return  ← a frame arrived after arm
                     idle >= idleTimeout → log v2.idle.teardown; closeWith(ctx, s, StatusIdleTimeout /*4408*/, nil)
4. Teardown       — closeWith (reused): state→Closed, stop+nil idleTimer alongside rekeyTimer/rekeyReplyTimer,
                     delete m.sessions[X] + m.queues[X], emit one close-only Outbound{ConnID:X, CloseCode:4408}
```

The reschedule re-points the timer at exactly `lastActivityAt + idleTimeout`, so an active session's idle timer fires ~once per `idleTimeout` and reschedules; an idle session's fires once and tears down. No per-frame timer churn.

**Stale-wake safety = two deterministic guards composed** (belt-and-suspenders where both layers are code, not a second stochastic timer):

- `w.s.state != V2StateOpen` (existing) — catches "`closeWith` ran between fire and wake" (e.g. a rekey failure tore the session down first).
- `idle < idleTimeout` (new) — catches "an inbound frame arrived after this timer was armed but before the wake was serviced"; `handleFrame` already re-stamped `lastActivityAt`, so the reschedule re-points the timer at the fresh deadline. **No active session is ever torn down by a stale wake.**

Both reads (`state`, `lastActivityAt`) and the act (`closeWith` / reschedule) happen in one synchronous `handleWake` call on the owning goroutine — no TOCTOU. On `Run` exit `runCtx` cancels, so a fired-but-undelivered idle callback releases via `ctx.Done` (no goroutine leak); an un-fired `AfterFunc` holds only a timer-heap entry.

**Reconnect after a sweep is clean.** `closeWith` deleted the session + queue, so a later frame on the same `conn_id` lazy-creates a fresh `V2StateAwaitingInit` session (existing `handleFrame` behaviour) and the returning phone re-handshakes from scratch — no stuck/half-torn-down state, no #647 replay reconciliation needed (a swept session is fully deleted, so there is no `last_event_id` continuity to preserve across a sweep). Sending 4408 to a `conn_id` whose phone already dropped is a harmless relay no-op.

**Security posture (spec-stage review verdict PASS).** The change is *net-positive* for secret hygiene: idle teardown **drops** the session's two Noise `CipherState`s (via `closeWith` → GC), bounding the in-memory lifetime of key material — the ticket's actual motivation. The timer is driven solely by the session's **own** inbound-frame cadence and is keyed by `*V2Session`: a phone can re-arm only its own timer (it sends frames only on the `conn_id` the relay assigned to its WS) and can neither force teardown of another phone's session nor prevent teardown of one. Keeping one's own session alive by using it is normal paired-device behaviour, not a bypass — the sweep targets *idle* sessions by design. The `v2.idle.teardown` log carries only `event` / `conn_id` / `close_code`; the fixed 4408 close code is not a per-session oracle; the close envelope carries `Frame == nil` (no sealed payload). See [codebase/774.md](../codebase/774.md).
