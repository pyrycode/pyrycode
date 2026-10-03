# #2712 — Re-assert a running turn's phase on connect

## Files read

- `cmd/pyry/interactive_turn_v2.go` → `interactiveTurnEmitterV2`: lifecycle fields (`inTurn`, `turnConvID`, `currentState`) are drain-goroutine-only; `transitionTo` (de-dup + emit), `endTurn`, `closeForConversation`, the follow-active switch at the top of `Handle`; `emit` appends to the ring BEFORE it calls `ActiveConns`. The `hist` / `waker` fields show the post-construction assignment idiom (86 constructor call sites).
- `cmd/pyry/relay.go` → `startRelayV2`: the `V2SessionConfig` literal is built before the emitter, which exists only under `w.streamSink != nil`; `RetainedBackgroundTaskRosters` is assigned straight through so a nil field stays nil.
- `internal/relay/v2session_seams.go` → `V2SessionConfig.RetainedBackgroundTaskRosters`: the seam doc the twins share (Run goroutine, bounded time, enumerate-all, nil ⇒ no reconcile).
- `internal/relay/v2session_rosterreconcile.go` → `reconcileBackgroundTaskRosters`: the twin to copy (capability gate, enqueue via `Push`, nil `EventID`, content-free logging).
- `internal/relay/v2session_handshake.go` → success tail of `handleNoiseInit`: the six reconciles, then `replayMissed`.
- `internal/relay/v2session.go` → `ActiveConns` is a round-trip through Run (`snapshot` channel), so an emitter's fan-out cannot be served while Run is inside a handshake; `forwardEnvelope` drops a live frame whose `EventID <= s.replayThrough`.
- `internal/relay/v2session_replay.go` → `replayMissed` sets `replayThrough` from the ring on Run.
- `internal/turnbridge/outbound.go` → `TurnState`, `BuildTurnState`; `internal/protocol/interactive.go` → `TurnStatePayload`.
- `internal/relay/v2session_rosterreconcile_test.go`: harness (`startManager`, `openModalConn`, `waitForEnvelopes`, `noiseMsgsForConn`, `decryptAppFrame`).

No other feature branch touches these four existing files.

## Context

A phone that reconnects mid-turn never learns the turn's phase: `turn_state` rides only the Mode A event stream, the replay restates nothing sent before the cursor, and the emitter de-duplicates so a long single-phase turn sends nothing new. This adds the seventh Mode B reconcile. No decision record needed; it follows the six twins.

## Design

### Producer (cmd/pyry)

New unexported type in `interactive_turn_v2.go`:

- `turnPhaseSnapshot` — `sync.Mutex` over one `(conversationID, state)` pair; the zero value means no running turn with a sent phase.
  - `publish(convID string, state turnbridge.TurnState)` — `idle` clears, `thinking` / `responding` sets. Nil receiver is a no-op.
  - `clear()` — nil-safe no-op on nil receiver.
  - `running() []protocol.TurnStatePayload` — nil when clear, otherwise one payload built with `turnbridge.BuildTurnState`.

One value, not a map, because the emitter holds at most one open turn; the seam returns a slice so the relay contract is "every conversation with a turn running" and survives a later multi-turn emitter.

Emitter: new field `phases *turnPhaseSnapshot`, assigned after construction (the `hist` idiom). Kept current at exactly two points, which every lifecycle path already goes through:

- `transitionTo` calls `e.phases.publish(convID, state)` after the de-dup check and BEFORE `emit`. Covers thinking/responding in `Handle`, and idle from `TurnEnd` and `closeForConversation`.
- `endTurn` calls `e.phases.clear()`. Covers the follow-active switch in `Handle` (which ends the prior turn without an idle) and is a second clear after idle transitions.

`startTurnIfNeeded` publishes nothing: an open turn whose phase was never sent stays absent (ticket: do not derive a phase that was never sent). The de-dup state `currentState` is untouched by the snapshot and by any read of it.

Wiring in `startRelayV2`: before the config literal, under `w.streamSink != nil`, mint `turnPhases := &turnPhaseSnapshot{}` and a `runningTurnPhases` func var set to `turnPhases.running`; assign `RunningTurnPhases: runningTurnPhases` (nil with no sink, so nil ⇒ no reconcile holds), and `emitter.phases = turnPhases` beside `emitter.waker`.

### Seam and reconcile (internal/relay)

- `V2SessionConfig.RunningTurnPhases func() []protocol.TurnStatePayload` — doc in the twins' shape: Run goroutine, bounded time (one mutex, no I/O), enumerate-all keyed by `conversation_id`, pure read, nil ⇒ no reconcile, carries only a server-minted id and a fixed phase vocabulary.
- New file `v2session_turnphasereconcile.go`: `reconcileTurnPhases(ctx, s)` — `!s.interactive || seam == nil` ⇒ return; for each payload marshal and `Push` a `TypeTurnState` envelope with fixed `ID: 1` and nil `EventID`; the twins' content-free log records on the two error arms, nothing on success.
- Called from the handshake tail AFTER the `replayMissed` block, not beside the other six (see Concurrency).

## Concurrency model

Writer: the drain goroutine (via `transitionTo` / `endTurn`). Reader: Run (via the seam). One mutex inside `turnPhaseSnapshot`, never held across `emit`, `Push` or any channel operation, so it cannot participate in a deadlock with the `ActiveConns` round-trip.

Why "a turn that ends while the connection is opening" cannot leave the conn on a running phase (AC2):

1. The emitter clears the snapshot (U) before `emit`, and `emit` appends to the ring (A) before asking Run for `ActiveConns` (S). So U < A < S on the drain goroutine.
2. The handshake runs entirely on Run: queue created, `replayMissed` reads the ring and sets `replayThrough` (W), then the reconcile reads the snapshot (R) and pushes. S cannot be served while Run is in the handshake.
3. If R observes a running phase, then U is after R, so A is after W: the idle's event id exceeds `replayThrough` (ring ids are ring-wide monotonic) and is not deduped, and S is served after the handshake, so the conn is in the fan-out and the idle lands on `m.queues` behind the reconciled frame.
4. If U precedes R, R sends nothing; the client starts idle under reset-on-reconnect.

Placing R before `replayMissed` breaks step 3: the idle can be appended between R and W, ride the replay queue (which drains first), and the live copy is then dropped by the `replayThrough` dedup — leaving the reconciled `thinking` last. That is the reason for the placement.

A frame sent in a transition race (R reads `responding` after U set it but before its emit) is followed by the same `responding` live — idempotent, as AC3 requires.

## Error handling

Marshal of a two-string struct cannot fail; defensive skip with a content-free Warn. `Push` error: Debug, stop on ctx teardown. Nil seam or nil snapshot are no-ops.

## Testing strategy

cmd/pyry (`interactive_turn_v2_phase_test.go`), driving `Handle` with scripted events and a `stubCursor`:
- Snapshot empty before any event and after `startTurnIfNeeded`-only paths (e.g. a `ToolProgress` that opens a turn without a transition).
- `ThoughtChunk` ⇒ `thinking`; then `TextChunk` ⇒ `responding`; `TurnEnd` ⇒ empty.
- `closeForConversation` ⇒ empty; follow-active switch to another conversation ⇒ prior conversation absent.
- Ordering invariant: a fake broadcaster whose `ActiveConns` records `running()` at each fan-out — when `idle` is emitted the snapshot is already empty; when `thinking` is emitted it already shows `thinking`.
- Reading the snapshot mid-turn does not change emission: the next transition emits exactly once, and a repeated same-phase event emits nothing.
- Nil `phases` field: emitter behaves as before (existing tests cover this implicitly; one explicit run).

internal/relay (`v2session_turnphasereconcile_test.go`), on the roster harness:
- Delivery: interactive conn receives one `turn_state` per seam payload (`thinking`, `responding`), payload equal, `EventID` nil.
- Non-interactive conn receives none; nil seam and empty seam send none.
- Unicast: opening B does not re-send to an already-open A.

## Open questions

- Whether any test in `internal/relay` asserts the exact envelope count after the handshake with a seam it doesn't set — none expected, since the seam is nil by default.

## Documentation handoff

Pending for the documentation stage — `docs/protocol-mobile.md` § Reconnect / Backfill semantics, under **Mode B**:
- "Reconcile on connect" bullet: list the current phase of a running turn among the control state re-asserted on connect — a `turn_state` is unicast for every conversation with a turn running, carrying its current phase, and nothing for an idle conversation.
- Match-and-replace bullet: add `conversation_id` for a turn phase; a re-sent `turn_state` for a known `conversation_id` replaces that conversation's phase in place.
- Closing count: six frames → seven, naming #2712.
- § `turn_state`: one sentence that the frame is also re-asserted on connect for a running turn, without an `event_id`.
