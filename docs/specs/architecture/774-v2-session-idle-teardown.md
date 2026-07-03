# Spec #774 — `internal/relay`: tear down an idle encrypted v2 session (in-repo idle sweep)

**Ticket:** [#774](https://github.com/pyrycode/pyrycode/issues/774) · **Size:** S · **Labels:** `security-sensitive`

## Files to read first

Everything is in one production file and its test. Read these ranges before touching code:

- `internal/relay/v2session.go:44-108` — the package-var timer idiom (`rekeyInterval`, `rekeyReplyTimeout`, `modalDenyTimeout`) + the `wakeKind` enum + `wakeSignal` struct. The new `idleTimeout` var and `wakeIdleTimeout` kind mirror these exactly.
- `internal/relay/v2session.go:27-42` — the WS close-code const block (`StatusProtocolMismatch` 4421, `StatusHandshakeFailure` 4426). The new `StatusIdleTimeout` 4408 const goes here.
- `internal/relay/v2session.go:238-326` — `V2Session` struct + the per-session `rekeyTimer` / `rekeyReplyTimer` field doc-comments. The two new fields (`idleTimer`, `lastActivityAt`) mirror the timer field's ownership doc.
- `internal/relay/v2session.go:663-742` — `Run` select loop, `handleWake` (the state-guard + kind switch), `armRekeyTimer` / `armRekeyReplyTimer`. `armIdleTimer` copies `armRekeyTimer`'s callback shape; the new `wakeIdleTimeout` case joins `handleWake`'s switch.
- `internal/relay/v2session.go:770-824` — `handleFrame` lazy-create + `V2StateClosed` early-return. The one-line `lastActivityAt` stamp lands right after the early-return.
- `internal/relay/v2session.go:1077-1099` — `handleNoiseInit` success tail where `s.state = V2StateOpen`, the push queue is created, and `s.rekeyTimer = m.armRekeyTimer(ctx, s)`. The idle timer is armed here alongside the rekey timer.
- `internal/relay/v2session.go:2067-2110` — `closeWith`: the full teardown (state→Closed, stop+nil `rekeyTimer`/`rekeyReplyTimer`, delete session, delete push queue, send `CloseCode` envelope). The idle-timer stop+nil joins the existing timer-stop region.
- `internal/relay/v2session_test.go:95-114` — `startManager(t, cfg)` harness (runs `Run` on a goroutine, returns a `stop` closure).
- `internal/relay/v2session_test.go:718-865` — `driveToOpen` / `driveToOpenCaps`: the canonical "phone handshakes to `V2StateOpen`" helper returning an `openSession`; reuse it verbatim.
- `internal/relay/v2session_test.go:1882-1920` — `TestV2Session_RekeyInitiator_Emit_ReArmViaResponder`: the exact idiom for overriding a package-var duration (save → set sub-second → `t.Cleanup` restore) and asserting a timer-driven emit under a background `Run`. **Not `t.Parallel`** — these tests mutate package vars. The idle tests copy this shape.
- `internal/relay/v2session_test.go:167-179` — `waitForEnvelopes(t, rec, n)` and the `v2Recorder.snapshot()` accessor for asserting emitted envelopes (including the close envelope).
- `docs/protocol-mobile.md:708-732` — § Error codes table + the HTTP-status-echo convention (4401←401, 4404←404, 4409←409, 4429←429). Add the `4408` row here.
- `docs/protocol-mobile.md:399` and `:872` — WS ping/pong is below the app layer (no per-phone liveness signal for the binary) and "Phone idle: close-on-background, push-to-wake … each reconnect performs a fresh Noise handshake." This is *why* the sweep exists and why a swept-then-returning phone re-handshakes cleanly (AC-3).

## Context

A phone's encrypted v2 session (`V2Session`) holds two Noise `CipherState`s and an armed 1-hour `rekeyTimer`. The relay↔binary leg is a **single multiplexed WebSocket**: every phone's frames arrive on one `Connection.Frames()` channel keyed by `conn_id`, and there is **no per-connection disconnect frame**. When one phone drops (or backgrounds — the protocol says phones close-on-background), the binary receives *nothing* for that `conn_id`. The stale session lingers until its scheduled rekey fires (up to an hour later), goes unanswered, and only then times out at 4426. Under normal connect/disconnect churn these stale encrypted sessions accumulate.

This ticket implements the **in-repo idle sweep** (the ticket body's option b): when an open v2 session receives no inbound frame within a bounded idle window, the manager tears it down through the existing `closeWith` path. Option (a) — a relay-emitted per-connection disconnect signal — is cross-repo relay work that does not exist in this repo and is out of scope; a future exact-teardown-on-disconnect ticket can supersede or complement this sweep.

## Design

One new per-session timer, wired through the **existing** `time.AfterFunc → wakeSignal → handleWake` machinery, reusing the **existing** `closeWith` teardown. No new package, no new exported type, no new goroutine topology, no signature changes → zero consumer fan-out.

### New package-level values (mirror the existing timer idiom)

| Symbol | Kind | Value | Notes |
|---|---|---|---|
| `idleTimeout` | package `var` (lowercase, test-overridable) | `15 * time.Minute` | The idle window. Well short of the 1-hour `rekeyInterval` so a dropped/backgrounded phone's cipher states never linger up to an hour. Long enough not to tear down a foregrounded-but-momentarily-quiet phone mid-read (which would force a disruptive re-handshake on the next tap). Test-overridable to sub-second via save/restore (`rekeyInterval` idiom). Not yet config-driven — a deferred concern, same posture as `modalDenyTimeout`'s #708 note. |
| `wakeIdleTimeout` | unexported `wakeKind` const | joins `wakeRekeyEmit`, `wakeRekeyReplyTimeout` | The per-session timer-event discriminator carried on `m.wake`. |
| `StatusIdleTimeout` | exported `websocket.StatusCode` const | `4408` | Idle-teardown WS close code. Echoes HTTP 408 (Request Timeout), consistent with the existing 44xx←HTTP convention. Added to the § Error codes table in `docs/protocol-mobile.md` (direction: binary, forwarded by relay). |

### New per-session state on `V2Session` (Run-goroutine-owned, no lock — "the loop is the lock")

- `idleTimer *time.Timer` — armed at `V2StateOpen`, re-scheduled by `handleWake`'s idle arm when activity is recent, stopped+nil'd by `closeWith`. Same lifecycle and ownership doc as `rekeyTimer`. Nil before open; nil after `closeWith`.
- `lastActivityAt time.Time` — stamped on every inbound frame in `handleFrame`. The idle deadline the timer chases. Run-owned; monotonic reading retained (never crosses the wire, so PROJECT-MEMORY's `time.Time` round-trip discipline does not apply).

### New helper (copies `armRekeyTimer`'s callback shape)

```
func (m *V2SessionManager) armIdleTimer(ctx context.Context, s *V2Session, d time.Duration) *time.Timer
```
`time.AfterFunc(d, …)` whose callback does a non-blocking `select { case m.wake <- wakeSignal{s: s, kind: wakeIdleTimeout}: case <-ctx.Done(): }`. Identical to `armRekeyTimer` except the kind and the caller-supplied duration `d` (the initial arm passes `idleTimeout`; the reschedule arm passes the remaining window). The callback touches **only** `m.wake` — never `s.send`/`s.recv`/`s.state`/`m.sessions` — so it cannot race the cipher states.

### Mechanism: single-arm timer that chases the last-activity deadline

Chosen over "re-arm the timer on every inbound frame" because it keeps `handleFrame`'s hot path to a **single field write** (no per-frame `Stop`+re-alloc churn), and over "periodic sweep over the sessions map" because that needs a second duration (sweep interval) and a separate channel — the AC asks for **one** test-overridable window in the `rekeyInterval` idiom, which a per-session timer keyed on `*V2Session` (reusing `m.wake`/`wakeSignal` unchanged) delivers with the smallest diff.

1. **Arm at open** — in `handleNoiseInit`'s success tail, next to `s.rekeyTimer = m.armRekeyTimer(ctx, s)`:
   `s.idleTimer = m.armIdleTimer(ctx, s, idleTimeout)`.
2. **Stamp on every frame** — at the top of `handleFrame`, immediately after the `V2StateClosed` early-return (so a torn-down conn's late frame is still dropped first): `s.lastActivityAt = time.Now()`. Every inbound frame — handshake or app — counts as activity. No timer op here.
3. **Fire → decide on the Run goroutine** — the `wakeIdleTimeout` case in `handleWake`, after the existing `if w.s.state != V2StateOpen { return }` guard:
   - `idle := time.Since(s.lastActivityAt)`
   - `if idle < idleTimeout` → a frame arrived after this timer was armed; **reschedule** for the remaining window (`s.idleTimer = m.armIdleTimer(ctx, s, idleTimeout-idle)`) and return. The timer now fires at exactly `lastActivityAt + idleTimeout`.
   - else → genuinely idle: log `v2.idle.teardown` (fields: `event`, `conn_id`, `close_code`) and `m.closeWith(ctx, s, StatusIdleTimeout, nil)`.
4. **Teardown reuses `closeWith`** — which already sets `V2StateClosed`, stops+nils `rekeyTimer`/`rekeyReplyTimer`, deletes the session + push queue, and sends the `CloseCode` envelope. **Add** an idle-timer stop+nil in its existing timer-stop region, mirroring the two existing `Stop()` blocks:
   ```
   if s.idleTimer != nil { s.idleTimer.Stop(); s.idleTimer = nil }
   ```

### Data flow

```
inbound frame on conn_id X ─► handleFrame ─► s.lastActivityAt = now   (Run goroutine)
                                             (timer keeps chasing)

idleTimer fires (fresh goroutine) ─► select{ m.wake<-{s,wakeIdleTimeout} | ctx.Done }
                                             │
Run select ◄─────────────────────────────────┘
   handleWake:
     state != Open?            → drop (already closed)
     idle < idleTimeout?       → reschedule for (idleTimeout - idle), return
     else                      → closeWith(ctx, s, 4408, nil)
                                   ├─ state = Closed
                                   ├─ stop+nil idleTimer, rekeyTimer, rekeyReplyTimer
                                   ├─ delete m.sessions[X], m.queues[X]
                                   └─ Outbound{ConnID:X, CloseCode:4408}
```

A later frame on `conn_id X` after teardown lazy-creates a fresh `V2StateAwaitingInit` session (existing `handleFrame` behaviour) → the returning phone re-handshakes cleanly (AC-3).

## Concurrency model

- **Single-writer invariant preserved.** `idleTimer` and `lastActivityAt` are mutated only on the Run dispatch goroutine (in `handleFrame`, `handleNoiseInit`, `handleWake`, `closeWith`), exactly like `rekeyTimer`/`state`. No mutex, no atomic — consistent with the package.
- **Timer callback is inert w.r.t. session state.** `armIdleTimer`'s `AfterFunc` callback only does the non-blocking `m.wake` send with a `ctx.Done` escape. It never reads or writes cipher states or maps. The actual teardown runs on the Run goroutine.
- **Stale-wake safety = two deterministic guards, composed** (belt-and-suspenders where the belt is deterministic code, not a second stochastic layer):
  - `w.s.state != V2StateOpen` (existing) — catches "`closeWith` ran between fire and wake" (e.g. a rekey failure tore the session down first).
  - `idle < idleTimeout` (new) — catches "an inbound frame arrived after this timer was armed but before the wake was serviced." In that case a frame's `handleFrame` already stamped `lastActivityAt`, and the reschedule re-points the timer at the fresh deadline; no active session is ever torn down by a stale wake.
  - No TOCTOU: the read (`state`, `lastActivityAt`) and the act (`closeWith` / reschedule) happen in one synchronous `handleWake` call on the owning goroutine.
- **Shutdown / no goroutine leak.** On `Run` exit, `runCtx` cancels; a fired-but-undelivered idle callback releases via `ctx.Done` (same as `armRekeyTimer`). `closeWith` stops the timer on every close path. An un-fired `AfterFunc` holds only a timer-heap entry, not a parked goroutine.
- **Reschedule cost is self-limiting.** For an active session the idle timer fires ~once per `idleTimeout` and reschedules; for an idle session it fires once and tears down. No per-frame timer churn.

## Error handling

| Failure mode | Behaviour |
|---|---|
| Timer fires during Run shutdown | Callback releases via `ctx.Done`; no wake delivered, no leak. |
| `closeWith` on an already-closed session | Guarded by the existing `if s.state == V2StateClosed { return }`; idempotent. |
| Idle sweep on a conn whose phone already dropped | `closeWith` sends the 4408 `CloseCode` envelope via `Outbound`; the relay applies it to a dead conn = harmless no-op (per ticket). |
| Idle sweep on a still-connected-but-quiet phone | Relay closes that phone's WS with 4408; phone reconnects and re-handshakes (protocol: close-on-background / push-to-wake). `closeWith` deleted the session + queue, so the next frame lazy-creates a fresh session — no stuck/half-torn-down state. |
| `Outbound` error while sending the close | Logged at debug and dropped by the existing `send` helper (relay reconnect handles recovery; the session is already torn down locally). |

## Testing strategy

Same-package tests in `internal/relay/v2session_test.go`. Override `idleTimeout` to sub-second via save/restore + `t.Cleanup`; **not `t.Parallel`** (mutates a package var), matching the rekey-initiator tests. Reuse `startManager`, `driveToOpen`, `waitForEnvelopes`, `v2Recorder.snapshot()`. Scenarios (developer writes the bodies in the project idiom):

- **Idle teardown fires + full teardown (AC-1).** Drive one conn to open; send no further frames; wait past `idleTimeout`. Assert: a `RoutingEnvelope` with `CloseCode == 4408` and `Frame == nil` is emitted; the session is removed (`ActiveConnIDs`/snapshot returns baseline, or a follow-up frame lazy-creates a fresh `V2StateAwaitingInit`); the push queue is gone (`m.queues` has no entry for the conn_id). Also assert the teardown pre-empts the 1-hour rekey (with `idleTimeout` ≪ `rekeyInterval`, the 4408 close arrives, not a `rekey_request`).
- **Activity re-arms — no premature teardown (reschedule arm).** Drive to open; send periodic app frames (round-tripping `noise_msg`s via the open session) at intervals shorter than `idleTimeout`, spanning a total wall-time greater than `idleTimeout`; assert **no** 4408 close during the active span. Then stop sending and assert the 4408 close appears after `idleTimeout` past the last frame. Exercises the `idle < idleTimeout` reschedule branch.
- **Churn returns to baseline (AC-3).** Repeat connect → handshake → go idle → sweep on N distinct conn_ids; after each idle window assert the live-session count (`len(m.sessions)` via the snapshot seam / `ActiveConnIDs`) returns to baseline — stale sessions do not accumulate.
- **Swept-then-reconnect re-handshakes cleanly (AC-3).** Drive conn_id `X` to open; let it sweep (observe 4408); then send a fresh `noise_init` on the **same** conn_id `X`; assert a clean handshake completes (fresh `noise_resp`, `V2StateOpen`) with no stuck state.

Run `go test -race ./internal/relay/...`.

## Open questions

- **`idleTimeout` default (15 min) and config-drivability.** Chosen to balance "reclaim dropped/backgrounded sessions promptly" against "don't tear down a foregrounded-but-idle phone mid-read." Because phones close-on-background (protocol §, line 872), the dominant lingering case is a phone that already closed its WS — for which even a shorter window is safe. Left as a test-overridable package var; wiring it to `internal/config` is a future concern (track alongside `modalDenyTimeout`/#708), not this ticket.
- **Event-driven exact teardown.** If the relay later emits a per-connection disconnect signal (the cross-repo option a), a follow-up can tear down on the exact event and treat this sweep as the coarse backstop. Out of scope here.
- Not a concern: #647 reconnect-replay reconciliation — a swept session is fully deleted, so a returning phone re-handshakes from scratch (no `last_event_id` continuity needed across a sweep).

## Security review

**Verdict:** PASS

**Findings:**

- **[Trust boundaries]** No MUST FIX. The idle timer is driven solely by the session's **own** inbound-frame cadence and is keyed by `*V2Session`. A phone can re-arm only its own timer (it can send frames only on the `conn_id` the relay assigned to its WS); it can neither force teardown of **another** phone's session nor prevent teardown of one. The teardown decision reads only Run-owned state (`s.state`, `s.lastActivityAt`) on the Run goroutine; no untrusted data influences the target session or the close code. Keeping one's own session alive by using it is normal paired-device behaviour, not a bypass — the sweep targets *idle* sessions by design.
- **[Tokens, secrets, credentials]** Not applicable — this path mints, stores, and compares no token. Net-positive for secret hygiene: idle teardown **drops** the session's two Noise `CipherState`s (via the existing `closeWith` → GC), bounding the in-memory lifetime of key material — the ticket's actual motivation. The 4408 close envelope carries `Frame == nil` (no sealed payload, no secret).
- **[File operations]** Not applicable — no path, no file I/O.
- **[Subprocess / external command execution]** Not applicable — no `exec`, no subprocess interaction.
- **[Cryptographic primitives]** No new crypto. The teardown reclaims existing CipherStates through the unchanged `closeWith` path; no key derivation, comparison, or handling is added. The change **shortens** the window during which idle cipher states and an armed 1-hour rekey timer persist — a defensive improvement, not a new surface.
- **[Network & I/O]** No MUST FIX. No new socket, listener, or read loop. One additional close envelope per swept session via the existing `Outbound`. The timer is per-session and self-limiting (fires at most ~once per `idleTimeout` per active session; a swept session emits exactly one 4408). No unbounded read; the inbound frame is still bounded by `maxNoisePayloadBytes` at `decodeInnerFrameV2` before it reaches `handleFrame`. A 4408 to an already-dead conn is a relay no-op.
- **[Error messages, logs, telemetry]** No MUST FIX. The `v2.idle.teardown` log line carries only `event`, `conn_id`, and `close_code` — no token, no key bytes, no payload (there is none). Consistent with the package's no-secrets-in-logs discipline (`v2session.go:279-283`). The fixed 4408 close code is not a per-session oracle — it reveals only "idle-swept," which is not sensitive.
- **[Concurrency]** No MUST FIX. One new per-session timer + two new Run-owned fields, no lock or atomic (the package's single-owner invariant). The `AfterFunc` callback touches **only** `m.wake` (non-blocking send + `ctx.Done` escape) and never the cipher states or maps, so it cannot race `s.send`/`s.recv`. Stale wakes are neutralised by two **deterministic** guards on the Run goroutine (`state != Open`; `idle < idleTimeout`) — the belt-and-suspenders fabric is code, not a second stochastic timer. No TOCTOU (read-and-act in one synchronous handler); no goroutine leak (`ctx.Done` release + `closeWith` stop).
- **[Threat model alignment]** Addressed and **improved**. The security-relevant goal — bound the lifetime of idle Noise cipher states and armed rekey timers under connect/disconnect churn — is directly served: before, key material + a 1-hour rekey timer could linger up to an hour after a phone silently drops; after, they are reclaimed within `idleTimeout` (15 min default). The mechanism adds no remote-drivable input (the timer is server-internal; a phone's only influence is re-arming its own session with frames it is already entitled to send). A paired device that hot-loops swept→re-handshake→swept is a compromised-device concern outside this ticket, already bounded by the per-server phone cap (4429) and token revocation. OUT OF SCOPE: an event-driven exact-disconnect signal (cross-repo relay work; a future ticket) — the sweep is the in-repo bound.

**Reviewer:** architect (self-review; `security-review.md` procedure not shipped into the worktree — pass conducted per the established category walk mirrored from spec #707)
**Date:** 2026-07-03
