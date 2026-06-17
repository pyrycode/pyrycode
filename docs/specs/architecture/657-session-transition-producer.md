# Spec #657 — `session_transition` producer + capability-gated fan-out (cmd-side)

> Emit a `session_transition` v2 envelope to interactive phones when a session
> transitions (clear rotation or idle/cap eviction). Consumes #659's pool-side
> `TransitionObserver`, maps the session-local reason to the wire reason, builds
> a `protocol.SessionTransitionPayload`, and fans it out through the established
> #571 push surface — interactive conns only.

Ticket: #657 · Size: **S** · Blocked-by #659 (CLOSED, merged via PR #660) ·
Wire type from #656 · `security-sensitive`.

## Files to read first

- `internal/sessions/transition.go` (whole, 73 lines) — **the contract you consume.**
  `TransitionObserver func(SessionTransition)`, the `SessionTransition` fields,
  `ReasonClear`/`ReasonEviction`, and the **MUST-NOT-BLOCK** observer rule
  ("hand the signal off to a buffered channel and return"). `SetTransitionObserver`
  must be called **before** `Pool.Run`; the field is read-only after.
- `internal/sessions/session.go:285-296` — eviction-side `notifyTransition`:
  `PreviousID = s.id` (evicted), `NewID` left zero (`""`), `Reason = ReasonEviction`,
  `OccurredAt` stamped. `reason == ""` (spontaneous exit) signals nothing — so the
  only reasons reaching you are `ReasonClear` and `ReasonEviction`.
- `cmd/pyry/assistant_turn_v2.go` (whole, 217 lines) — **the structural template.**
  Your emitter is a near-verbatim mirror: buffered `in` channel, non-blocking
  `Enqueue` (drop-on-full), `Run(ctx)` drain loop, `broadcast` (marshal → fresh
  `ActiveConns` snapshot → capability filter → `Push` per conn), and
  `startAssistantTurnBridgeV2` wiring + cleanup. Copy the shape; change the input
  type, the payload, and **invert the capability filter** (interactive-only).
- `cmd/pyry/interactive_turn_v2.go:31-34, 302-362` — `interactiveBroadcaster`
  interface (`ActiveConns` + `Push`) — **reuse it, don't declare a third copy** —
  and `emit`'s capability gate (`if !c.Interactive { continue }`), the exact
  filter you want. Note `EventID`/ring is **out of scope** here (no replay AC).
- `internal/protocol/messaging.go:36-58` — `SessionTransitionPayload` (the struct
  you build). `WorkspaceCwd *string`, **no omitempty** → renders literal JSON
  `null` for non-`workspace_change`. `Reason` is a plain string over the closed
  set `{clear, idle_evict, workspace_change}` (no exported reason constants — use
  literals matching this doc).
- `internal/protocol/testdata/session_transition.json` — golden shape of the wire
  envelope (`reason:"idle_evict"`, `workspace_cwd:null`). Your test's expected
  payload mirrors this.
- `cmd/pyry/relay.go:268-359` — `startRelayV2`: where `mgr` is built (287), where
  `startInteractiveTurnStreamV2` is wired (340), and the `drain` cleanup ordering
  (346-358). You add a wiring call + cleanup here.
- `cmd/pyry/relay.go:88-152` — `startRelay`: the v1/v2 dispatcher you thread the
  new observer-sink param through (one call site → `startRelayV2` at 142).
- `cmd/pyry/main.go:460-493` — `pool` construction (460), the `startRelay(...)`
  call (489), and `pool.Run(ctx)` (514, further down). Confirms the install-before-Run
  ordering holds: `startRelay` runs at 489, `pool.Run` at 514.
- `internal/relay/v2session.go:1831-1860` (`Push` — non-blocking enqueue+wake) and
  `:1986-2025` (`ActiveConns` — **blocks** on a Run-goroutine round-trip). Read
  these to understand *why* the observer cannot call `ActiveConns` directly (it
  blocks → violates #659's contract) and why the buffered hand-off is mandatory.
- `cmd/pyry/interactive_turn_v2_test.go:33-127` and `cmd/pyry/assistant_turn_test.go:22-45`
  — reusable test doubles: `fakeInteractiveBcast` (snapshots + recorded pushes),
  `recordedPush`, `pushTypes`, `pushesFor`, `discardLogger`. Reuse; don't re-author.

## Context

The `session_transition` wire type (constant `protocol.TypeSessionTransition` +
`protocol.SessionTransitionPayload` + docs SSOT) shipped in **#656**. The
session-side signal — surfacing `/clear` rotations and idle/cap evictions out of
`internal/sessions` via an injectable `TransitionObserver` — shipped in **#659**
(merged). This ticket is the **cmd-side producer**: it installs the observer,
maps the session-local reason onto the wire reason, and fans the
`TypeSessionTransition` envelope to capability-gated interactive phones the same
way structured turn events flow today (`interactive_turn_v2.go`).

`internal/sessions` deliberately does **not** import `internal/protocol` (import
cycle): #659 emits its own `TransitionReason` vocabulary, and the mapping to the
wire `reason` string happens here, cmd-side.

`workspace_change` is **out of scope** — there is no server-side workspace-change
source (`WorkDir` is set once at spawn). #656 keeps it a valid wire reason so the
mobile decoder stays exhaustive; this producer never emits it. `WorkspaceCwd` is
always JSON `null`.

Unblocks mobile `pyrycode/pyrycode-mobile#336`. See ADR 025.

## Design

### New file: `cmd/pyry/session_transition_v2.go`

One unexported emitter + one wiring function. Structurally a mirror of
`assistantTurnEmitterV2` / `startAssistantTurnBridgeV2`.

**`sessionTransitionEmitterV2`** — fields: `bcast interactiveBroadcaster` (reuse
the existing interface — `*relay.V2SessionManager` satisfies it), `logger
*slog.Logger`, `in chan sessions.SessionTransition` (buffered), `nextID uint64`
(per-conn envelope-ID counter; own counter — see Concurrency).

Methods (signatures + 1-line behavior; **no full bodies** — copy `assistant_turn_v2.go`):

- `Enqueue(t sessions.SessionTransition)` — the observer callback. Non-blocking
  `select { case e.in <- t: default: WARN-drop }`. Matches `sessions.TransitionObserver`'s
  `func(SessionTransition)` signature so it installs directly. This is the
  #659-mandated "hand off to a buffered channel and return."
- `Run(ctx)` — drain loop: `select { <-ctx.Done() → return; t, ok := <-e.in → broadcast(ctx, t) }`.
  Mirror `assistantTurnEmitterV2.Run`.
- `broadcast(ctx, t)` — map → marshal → fresh `ActiveConns` snapshot → **interactive-only**
  filter → `Push` per conn. No cursor read, no id mint (contrast `assistant_turn_v2`).
  Per-conn `Push` error is DEBUG-logged and the loop continues (AC#2 robustness);
  `ctx.Err() != nil` mid-fan-out returns early.

**Reason → wire mapping** (a pure helper `toWirePayload(t) (protocol.SessionTransitionPayload, bool)`,
the unit-testable seam):

| #659 `TransitionReason` | wire `reason` | `previous_session_id` | `new_session_id` | `workspace_cwd` |
|---|---|---|---|---|
| `ReasonClear` (`"clear"`) | `"clear"` | `t.PreviousID` (old) | `t.NewID` (new) | `null` |
| `ReasonEviction` (`"eviction"`) | `"idle_evict"` | `t.PreviousID` (evicted) | `t.PreviousID` (evicted) | `null` |
| anything else | — | **drop**, DEBUG log, `ok=false` | | |

- `OccurredAt = t.OccurredAt` (already `UTC`, stamped by #659 — do not re-stamp).
- Eviction has no successor id (`t.NewID == ""`); per mobile #336, map the evicted
  id onto **both** wire fields (do not emit an empty `new_session_id`).
- The unknown-reason `default` is defensive: only `ReasonClear`/`ReasonEviction`
  reach the observer today, but a future #659 reason must not silently emit a
  malformed envelope — drop + log instead.
- `WorkspaceCwd` is always `nil` (literal JSON `null`). Never set here.

**Envelope construction** — mirror `assistant_turn_v2.go:154-160`: per interactive
conn, `e.nextID++`, then `protocol.Envelope{ID: e.nextID, Type: protocol.TypeSessionTransition,
TS: time.Now().UTC(), Payload: payloadJSON}`. Leave `EventID` **nil** (no replay
ring — see Open Questions).

**`startSessionTransitionStreamV2(ctx, sink transitionObserverSink, bcast interactiveBroadcaster, logger) func()`**
— mirror `startAssistantTurnBridgeV2`:
1. build the emitter,
2. `sink.SetTransitionObserver(emitter.Enqueue)` — installs the observer (must run
   before `Pool.Run`; the call site guarantees this),
3. start `Run(ctx)` on a goroutine,
4. return a cleanup that waits for `Run` to exit on ctx-cancel.

   **Cleanup does NOT close `in` and does NOT clear the observer.** `SetTransitionObserver`
   is read-only after `Pool.Run` (can't be cleared), and a late `Enqueue` racing
   teardown is panic-safe precisely because `in` is never closed — a non-blocking
   send to an open-but-full channel just drops. Same rationale as
   `startAssistantTurnBridgeV2`'s "we rely on ctx cancellation to drain Run."

**`transitionObserverSink`** — narrow consumer-declared interface (one method):
```go
type transitionObserverSink interface {
	SetTransitionObserver(sessions.TransitionObserver)
}
```
`*sessions.Pool` satisfies it. Declared in this file. (This file imports
`internal/sessions`; `relay.go` does not need to — it only passes the value through.)

### Edit: `cmd/pyry/relay.go`

- `startRelayV2(...)` — add a `transitions transitionObserverSink` parameter. After
  the `startInteractiveTurnStreamV2` block (~line 344), wire unconditionally
  (whenever the v2 manager exists — see "Why no bridge-gate" below):
  `streamTransitionsCleanup := startSessionTransitionStreamV2(ctx, transitions, mgr, logger)`.
  Add it to the `drain` cleanup sequence (before `<-mgrDone`, alongside the other
  producer cleanups).
- `startRelay(...)` — add the same `transitions transitionObserverSink` parameter
  and pass it to `startRelayV2` (line 142). The v1 branch ignores it
  (`session_transition` is a v2-only wire event).

### Edit: `cmd/pyry/main.go`

- Pass `pool` as the new argument to the `startRelay(...)` call at line 489.
  `*sessions.Pool` satisfies `transitionObserverSink` structurally; no new import
  (main.go already imports `internal/sessions`).

### Why no bridge-gate / why unconditional in `startRelayV2`

The coarse bridge and structured turn stream gate on `bridge != nil` because both
tap the **PTY output observer**, which exists only in service mode. The transition
producer has no PTY dependency — it consumes pool transitions, which fire in any
mode. The natural gate is simply "the v2 manager exists" (i.e. inside
`startRelayV2`). The capability filter (`ActiveConns` → `Interactive`) is the real
delivery gate: with no interactive phone connected, the fan-out reaches nobody.
Do **not** cargo-cult the `bridge != nil` gate here. (Clear transitions require the
rotation watcher, which `claudeSessionsDir == ""` disables independently; eviction
transitions fire regardless. Either way, no-interactive-phone ⇒ no delivery.)

## Concurrency model

- **Producers of the signal** (unchanged, #659): the pool's per-session lifecycle
  goroutine (eviction) and the rotation-watcher goroutine (clear) call
  `notifyTransition` → `emitter.Enqueue`, **synchronously, no lock held**. `Enqueue`
  is a non-blocking buffered send (drop-on-full) → honors #659's MUST-NOT-BLOCK
  contract. Buffer size: small (e.g. 16) — transitions are rare; the buffer
  absorbs a burst, drop-on-full bounds memory and never wedges the pool.
- **The emitter `Run` goroutine** (new, started by `startSessionTransitionStreamV2`):
  drains `in`; per transition calls `bcast.ActiveConns(ctx)` (blocks on the
  manager's Run-goroutine round-trip — *fine, this is the emitter's own goroutine,
  allowed to block*) then `bcast.Push` per conn (non-blocking enqueue+wake).
- **The manager `Run` goroutine** (unchanged): services `ActiveConns` snapshots and
  drains pushes to the wire under the V2StateOpen gate.

**Why a buffered channel is required (and how it reconciles with the ticket's "do
not add a new channel or fan-out path").** That note means: do not invent a
*manager-side* fan-out primitive parallel to `Push`/`ActiveConns`. We don't — the
fan-out flows through the single existing push surface. The buffered hand-off
channel is the *consumer-side* mechanism #659's observer contract **explicitly
mandates** ("hand the signal off to a buffered channel and return"), and is
necessary because `ActiveConns` blocks. It is the same shape `assistant_turn_v2.go`
already uses for the PTY-drain → fan-out hand-off; this is reuse of an established
pattern, not a new path.

**Install-before-Run ordering.** `SetTransitionObserver` runs inside
`startSessionTransitionStreamV2` ← `startRelayV2` ← `startRelay` (main.go:489),
strictly before `pool.Run(ctx)` (main.go:514). The first `Enqueue` can only fire
after `pool.Run` starts the lifecycle/watcher goroutines, by which time `mgr.Run`
(started at relay.go:315) is also running. No race; the detector stays green.

**Shutdown.** ctx cancel → `pool.Run` returns (lifecycle + watcher goroutines stop;
no more `Enqueue`) → deferred `relayCleanup` → `drain` waits for the emitter `Run`
to exit on `ctx.Done()`, then `<-mgrDone`. In-flight buffered transitions are
abandoned at shutdown (phones are disconnecting too) — same as `assistant_turn_v2`.

## Error handling

- **Unknown reason** → drop + DEBUG (`event=session_transition.unknown_reason`,
  `reason=<value>`); never emit a malformed envelope.
- **Payload marshal failure** → drop + DEBUG (defensive; the payload is a closed
  struct of strings/time and cannot fail in practice). Never echo the payload.
- **Per-conn `Push` error** → DEBUG (`event=session_transition.push_err`,
  `conn_id`, `err`); continue the loop (a dropped conn must not abort the others).
  `ctx.Err() != nil` → return (teardown).
- **Full input channel** → WARN-drop in `Enqueue` (`event=session_transition.queue_full`).
- **Empty `new_session_id` on clear** (`t.NewID == ""`) is not expected for clear
  (rotation always has a successor); emit as-is — do not special-case. Only
  eviction maps both fields to the evicted id.

**Logging discipline (carry forward the v2 emitter posture).** Give the emitter a
`// SECURITY:` doc comment mirroring `assistant_turn_v2.go` / `interactive_turn_v2.go`:
the only fields logged at any level are content-free discriminants — `event`,
`reason`, `conn_id`, and `Push`'s transport-sentinel `err`. The marshaled payload
is **never** logged (no `payloadJSON`, no `err.Error()` on the marshal path). The
payload carries no application content (session ids + reason + timestamp only), so
there is nothing sensitive to leak — but the discipline is kept verbatim so a
future field addition can't quietly start leaking through a log line. Session ids
are non-secret routing identifiers (`session_id` is already a standard log field
across `internal/sessions`); logging them is fine.

## Testing strategy

New file `cmd/pyry/session_transition_v2_test.go`. **Scope to the ACs** — do not
mirror the 451-line `assistant_turn_v2_test.go` wholesale; reuse its fakes.
Stdlib `testing`, table-driven where natural. Use `time.Time.Equal` for
`OccurredAt` comparisons (monotonic-clock strip on JSON round-trip — project
convention).

- **`toWirePayload` mapper (pure, table-driven)** — the cheapest, highest-value
  unit:
  - `ReasonClear` with prev=`A`, new=`B` → `{previous:A, new:B, reason:"clear", workspace_cwd:nil}`, `ok=true`.
  - `ReasonEviction` with prev=`A`, new=`""` → `{previous:A, new:A, reason:"idle_evict", workspace_cwd:nil}`, `ok=true`.
  - unknown reason `"frobnicate"` → `ok=false`.
  - `OccurredAt` propagated verbatim; `WorkspaceCwd` always nil.
- **End-to-end fan-out (AC#1, #2, #4)** — drive `broadcast` (or `Enqueue` + a
  short `Run`) with a `fakeInteractiveBcast` snapshot of `[{ConnID:"i", Interactive:true}, {ConnID:"n", Interactive:false}]`:
  - clear transition → exactly one recorded push, to `"i"`, `Type == TypeSessionTransition`,
    decoded payload `reason=="clear"`, `previous_session_id`/`new_session_id` correct,
    `workspace_cwd` JSON `null`. **Zero pushes to `"n"`** (AC#3).
  - eviction transition → push to `"i"` with `reason=="idle_evict"`, both id fields
    == evicted id, `workspace_cwd` null. Zero pushes to `"n"`.
- **Capability gate in isolation (AC#3)** — snapshot of only non-interactive conns
  → zero pushes.
- **Push-error continuation (AC#2)** — `pushErr` set for the first of two
  interactive conns → the second still receives its push.
- **Non-blocking `Enqueue`** — fill `in` to capacity, assert the next `Enqueue`
  returns without blocking and is dropped (a buffered channel of size N, N+2 sends
  without a draining `Run`, assert no goroutine blocks / drop is logged). This
  guards the #659 MUST-NOT-BLOCK contract.

Run `go test -race ./cmd/pyry/...`, `go vet ./...`, `gofmt`.

## Open questions

- **Reconnect replay of `session_transition`.** This producer does not append to
  the #647 replay ring (`EventID` left nil) — no AC asks for it, and the ring is
  the turn-stream's. A phone that reconnects mid-stream and replays via
  `last_event_id` therefore will **not** receive a missed session boundary. For
  #336's rendering completeness this is a gap, but it is squarely a #647-replay
  concern, not this ticket's. **Recommend a follow-up ticket** if/when reconnect
  replay must include session boundaries (decision: whether transitions join the
  per-conversation ring, and under what durable id). Do not pull it in here.
- **Buffer size for `in`.** Proposed 16. Transitions are rare (a `/clear` or an
  idle eviction, human-paced). If a future high-churn scenario emerges, revisit;
  drop-on-full keeps the contract absolute regardless.

## Security review

**Verdict:** PASS

**Findings:**

- **[Trust boundaries]** No findings. This path is **outbound-only** (internal
  session state → wire); no untrusted phone input enters it. The boundary is
  explicit and doubled: the `Interactive` capability filter in `broadcast`
  (`if !c.Interactive { continue }`, AC#3) decides *who* receives the event, and
  `V2SessionManager.forwardEnvelope` re-checks `s.state == V2StateOpen` at seal
  time (`internal/relay/v2session.go:1945-1947`), dropping any envelope to a conn
  that de-authed or tore down between the `ActiveConns` snapshot and the drain —
  closing the snapshot→drain TOCTOU on the security-relevant property. The
  unknown-reason `default: drop` (mapper `ok=false`) prevents a future #659 reason
  from emitting an unvalidated string onto the wire.
- **[Tokens, secrets, credentials]** N/A. The path handles no tokens, secrets, or
  credentials. Session ids are `crypto/rand`-minted UUID routing identifiers, not
  auth material; `StaticPriv`/Noise keys are untouched; nothing is minted here
  (ids arrive from the #659 transition).
- **[File operations]** N/A. No path construction, no file I/O.
- **[Subprocess / external command]** N/A. None.
- **[Cryptographic primitives]** N/A in this slice. The AEAD seal is inherited
  unchanged from `forwardEnvelope` (existing, audited); no `crypto/rand` use here.
- **[Network & I/O]** No findings. Outbound, small closed-struct payload, no
  unbounded read. Resource exhaustion is bounded by the size-16 drop-on-full
  `in` channel **and** the manager's per-conn drop-under-pressure push queue.
  Transitions are server-originated (`/clear` rotation, idle/cap eviction) — **not
  phone-triggerable** — so there is no remote amplification / DoS vector to add.
- **[Error messages, logs, telemetry]** No findings (one SHOULD, addressed inline
  in § Error handling). Logs carry only content-free discriminants (`event`,
  `reason`, `conn_id`, `err` sentinel); the marshaled payload is never logged. The
  payload contains no application content (session ids + reason + timestamp), so
  there is nothing sensitive even on the defensive paths.
- **[Concurrency]** No findings. No new locks. The observer (`Enqueue`) is a
  lock-free non-blocking channel send, invoked off-lock per #659's contract — it
  never calls `Push`, so it touches no manager lock and adds no lock-ordering
  edge. `nextID` is single-goroutine (no atomic needed). `SetTransitionObserver`
  runs strictly before `Pool.Run` (install-before-Run, race-free per #659). The
  one new goroutine (`Run`) exits on `ctx.Done()` and cleanup waits for it (no
  leak); `in` is never closed, so a teardown-racing `Enqueue` is panic-safe. The
  non-blocking observer is itself a liveness guarantee: a wedged fan-out can never
  stall the pool's lifecycle/watcher goroutines.
- **[Threat model alignment]** No findings. Per `docs/protocol-mobile.md`
  § Security model: **threat #3 (relay-operator MITM)** — the payload is sealed
  under the session's `send` CipherState in `forwardEnvelope`; the relay sees only
  opaque ciphertext, and this producer never puts a `cwd` on the wire
  (`workspace_cwd` always `null`). **Threat #6 (replay)** — each emission is a
  fresh live envelope under Noise's AEAD nonce discipline; `EventID` is nil so it
  is not part of the #647 replay-dedup path. **Threat #7 (DoS)** — unchanged;
  server-originated + drop-on-full, no new surface. The ADR 025 interactive-event-
  family invariants (interactive-only, no-raw-bytes, capability-gated) are all
  satisfied. `workspace_change` is named out of scope (no server-side source).

**Reviewer:** architect (self-review per `architect/security-review.md`)
**Date:** 2026-06-17
