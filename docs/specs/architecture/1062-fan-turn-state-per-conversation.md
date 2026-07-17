# Spec #1062 — fan `turn_state` on per-conversation interactive sessions

**Ticket:** [#1062](https://github.com/pyrycode/pyrycode/issues/1062) — split from #1050 (turn-phase half; the `modal_shown` half is #1063).
**Size:** S. One production file (`cmd/pyry/interactive_turn_v2.go`), ~8 production lines. A recovered hermetic oracle and a real-claude liveness rung dominate the diff but add no production surface.
**Security-sensitive:** No. `turn_state` carries only coarse daemon state (thinking/responding/idle) with no application content, fanned wire-direct; the cross-conversation scoping property is the *existing* `conversation_id` stamp on `TurnStatePayload`, asserted here, not newly designed. No audited routing surface. (Confirmed against the label criteria — [[security-sensitive-label-tracks-design-not-lineage]]: outbound daemon-STATE is wire-direct, not policy.)

---

## Files to read first

- `cmd/pyry/interactive_turn_v2.go:76-111` — `interactiveTurnEmitterV2` struct + its lifecycle fields (`inTurn`, `turnID`, `seq`, `currentState`). **The fix adds one field here.**
- `cmd/pyry/interactive_turn_v2.go:139-220` — `Handle`: reads the conversation cursor once, type-switches into lifecycle actions. **The switch-detect guard goes at the top, after the empty-cursor check.**
- `cmd/pyry/interactive_turn_v2.go:222-243` — `startTurnIfNeeded`: opens a turn, mints `turnID`, resets `seq`/`currentState` only when `!inTurn`. **Set the new owning-conversation field here.**
- `cmd/pyry/interactive_turn_v2.go:245-256` — `transitionTo`: the de-dup (`if e.currentState == state { return }`) that swallows the new conversation's opening `responding`. This is the observed defect site; the fix repairs its precondition, not this function.
- `cmd/pyry/interactive_turn_v2.go:258-290` — `endTurn` + `flushDelta`: the two primitives the guard composes (flush the abandoned turn's buffered text, then mark it closed). `flushDelta` emits against `deltaConvID` (captured when buffering began), not the live cursor — this is why flush-on-abandon attributes correctly.
- `cmd/pyry/interactive_turn_stream_v2.go:436-510` — `resolveTarget`: the follow-active resolver. `active.watch()` returns `(convID, switchCh)`; a cursor change fires `switchCh`, the subscriber tears down and re-subscribes. **Confirms the switch is already threaded — no new wiring needed; the emitter detects it via the cursor it already reads.**
- `cmd/pyry/interactive_turn_v2_test.go` — hermetic emitter test file. Home of `stubCursor` (with `.set`), `fakeInteractiveBcast`, `recordedPush`, `assistantDeltas`, `pushTypes`, `discardLogger`. **The recovered oracle lands here.**
- `origin/feature/1050:cmd/pyry/zz_repro_1050_test.go` — the RED repro to recover (`git show origin/feature/1050:cmd/pyry/zz_repro_1050_test.go`). Genuinely RED on `main`. Rename off the `zz_repro`/`1050` scratch naming; see Testing strategy for the adaptation.
- `internal/e2e/realclaude/interactive_per_conversation_liveness_test.go` — the harness the real-claude rung reuses verbatim: `startPerConversationHarness`, `createConversationViaPhone`, `sealSendMessage`, `drainForReply`.
- `internal/e2e/realclaude/interactive_bootstrap_liveness_test.go:182-239` — `drainForAssistantReply`: the exact drain shape `drainForTurnState` mirrors (decrypt every noise_msg in receive order, skip non-matching envelopes, return on the target).
- `internal/protocol/interactive.go:16-25` — `TurnStatePayload{ConversationID, State}`; wire `State` ∈ `"thinking" | "responding" | "idle"`. AC3's scoping key.
- `internal/turnbridge/outbound.go:36-41,111` — `TurnState` constants + `BuildTurnState`; the `responding`/`thinking` values the drain matches.

---

## Context

For a **per-conversation** interactive session (a conversation created over the wire — the daemon mints a dedicated per-conversation claude session), the daemon streams `assistant_delta` and `turn_end` but never fans `turn_state` (thinking/responding). The remote renders the full reply but never learns the turn is running: no interrupt affordance, no observable queue window, and a false "the turn seems to have stalled…" warning after a complete reply.

**Confirmed root cause** (ticket Technical Notes; verified against the code). A single long-lived `interactiveTurnEmitterV2` owns the turn lifecycle for *every* conversation on the interactive leg. Its content envelopes (`assistant_delta`, `turn_end`, `stall`) reach the wire **direct** via `emit`/`emitMapped` (no de-dup). Its `turn_state` reaches the wire **de-duped** through `transitionTo`, which drops the emit when `currentState` already equals the target. `startTurnIfNeeded` re-mints `turnID`/`seq` and clears `currentState` **only when `!inTurn`**.

The follow-active switch (`resolveTarget` + `active.watch()`) fires when `send_message` routes a turn to a *different* conversation and re-stamps the cursor. The subscriber tears down the prior conversation's tail **mid-turn** and re-subscribes onto the new one — so the prior turn's `TurnEnd` is never delivered. `inTurn` stays `true` and `currentState` stays `StateResponding`. When the new conversation's first `TextChunk` arrives:

1. `startTurnIfNeeded` sees `inTurn == true` → returns early, **no reset**. `turnID`/`seq`/`currentState` still belong to the abandoned prior turn.
2. `transitionTo(newConv, StateResponding)` sees `currentState == StateResponding` → **de-dups away**. No `turn_state` fires.
3. The delta still flushes via `emitMapped` (no de-dup) — carrying the abandoned turn's `turnID`/`seq`.

Fingerprint: **deltas + turn_end fan, but no thinking/responding turn_state** — exactly the reported symptom. The clean-turn-end path is unaffected: a delivered `TurnEnd` sets `inTurn=false` and `currentState=idle`, so the next conversation opens fresh (proven by the recovered control test).

---

## Design

**Principle:** a turn belongs to a conversation. The emitter's turn lifecycle (`inTurn`/`turnID`/`seq`/`currentState`) is implicitly keyed to the conversation that opened it, but nothing records *which* conversation, so a follow-active switch orphans that state onto the next conversation. The fix makes the ownership explicit and abandons an orphaned turn when the cursor moves.

This is an emitter-local invariant repair. It touches **only `cmd/pyry/interactive_turn_v2.go`**. No change to `interactive_turn_stream_v2.go`, no new callback across the `turnbridge` boundary, no signature change → **zero consumer cascade** (the emitter's only construction site, `startInteractiveTurnStreamV2`, is untouched).

### 1. Record the owning conversation

Add one unguarded lifecycle field to `interactiveTurnEmitterV2` (same single-`Handle`-goroutine contract as the existing lifecycle fields):

```
turnConvID string // conversation that owns the currently-open turn; set at turn open, compared on each Handle
```

Set it in `startTurnIfNeeded` alongside the existing resets, at the point a turn opens (`e.inTurn = true`):

```
e.turnConvID = convID
```

### 2. Abandon an orphaned turn when the cursor moves

At the **top of `Handle`**, after the empty-cursor drop and before the type-switch:

```
if e.inTurn && convID != e.turnConvID {
    // Follow-active switch (#1062): the prior conversation's subscription was
    // torn down mid-turn, so its TurnEnd never arrived and inTurn/currentState
    // are stale. Flush its buffered delta against its OWN conversation (deltaConvID
    // is captured, not the live cursor), then mark the turn closed so
    // startTurnIfNeeded re-mints a fresh turn — a new turnID, seq 0, and an
    // opening turn_state — for the new conversation.
    e.flushDelta(ctx)
    e.endTurn()
}
```

**Contract:** after this guard, when `Handle` reaches a content event, `startTurnIfNeeded(convID)` sees `!inTurn`, re-mints `turnID`, resets `seq=0`, clears `currentState=""`, and records `turnConvID=convID`. The subsequent `transitionTo(convID, StateResponding)` then emits (no longer de-duped). The new conversation's delta carries the fresh `turnID` with `seq 0`.

**Why `flushDelta` before `endTurn`:** `flushDelta` emits against `deltaConvID` (captured when the prior turn buffered its text) using the prior turn's `turnID`/`seq` — which are still in place at guard time. So the abandoned conversation's partial text keeps correct attribution and wire position, consistent with the "flush before any state change" discipline already in every `Handle` case. It is a no-op when nothing is buffered.

**Why the top of `Handle` and not inside `startTurnIfNeeded`:** the flush needs `ctx` (to emit) and must run *before* the re-mint (otherwise leftover buffered text would flush against the fresh turn's `turnID`, a cross-turn misattribution). `startTurnIfNeeded` has no `ctx` and cannot emit. The `Handle`-top guard also covers the degenerate cases where the first post-switch event is a `TurnEnd` (drops correctly: `inTurn` now false) or a `Stall` (emits; see Open questions) uniformly.

### Data flow (unchanged except the guard)

```
active.watch() cursor flips A→B   (send_message routes B; sessionRouter.Route re-stamps)
        │
        ▼  subscriber tears down A's tail mid-turn, re-subscribes onto B (resolveTarget)
        │
B's first TextChunk ──► Handle: convID=B, e.turnConvID=A, e.inTurn=true
        │                        └─ guard fires: flushDelta(→A), endTurn()   ◄── THE FIX
        │
        ├─ startTurnIfNeeded(B): mint turnID_B, seq=0, currentState="", turnConvID=B
        ├─ transitionTo(B, responding): currentState "" → responding  ► turn_state fans  ◄── was de-duped
        └─ buffer + flush delta: conversation_id=B, turn_id=turnID_B, seq=0
```

### Cross-conversation scoping (AC3 — assertion, not new design)

Every `turn_state` is built by `BuildTurnState(convID, state)` and stamped with `conversation_id` on `TurnStatePayload`. B's opening `responding` is stamped `B`; the abandoned flush is stamped `A` (via `deltaConvID`). The client renders each against its own conversation. The fix adds no wire field and does not weaken this property; the hermetic oracle asserts it (a `turn_state` for the abandoned conversation never carries the new conversation's id and vice-versa).

---

## Concurrency model

Unchanged. `Handle`, `startTurnIfNeeded`, `endTurn`, and `flushDelta` all run only on the producer's single `Run` goroutine (`OnEvent` invoked serially; `OnFlush` routed back onto the same goroutine). The new `turnConvID` field is read/written only inside `Handle`/`startTurnIfNeeded`, same unguarded-but-race-free contract as `inTurn`/`currentState`. No new goroutine, no lock, no channel.

---

## Error handling

No new failure modes. The guard composes two existing no-op-safe primitives:
- `flushDelta` is a no-op on an empty buffer; on a non-empty buffer it emits through the existing `emit` path (Push errors already logged at debug, teardown already handled via `ctx.Err()`).
- `endTurn` only sets `inTurn=false`.

The turn-id mint failure path in `startTurnIfNeeded` (crypto/rand, defensive) is unchanged: on failure the turn stays closed and the next event retries — after the guard, that simply means the new conversation retries its first transition, no worse than today.

---

## Testing strategy

### Deterministic hermetic oracle (AC4) — RED on `main`, GREEN after fix

Recover `origin/feature/1050:cmd/pyry/zz_repro_1050_test.go` into the emitter test file (`interactive_turn_v2_test.go`, where all its helpers live) and **rename off the `zz_repro`/`1050` scratch naming** (e.g. `interactive_turn_v2_switch_test.go` or fold into the existing file with `TestInteractiveTurnV2_Switch*` names). Adapt the three cases:

- **`…SwitchWithOpenPriorTurn_EmitsResponding`** (the AC4 oracle). Drive conv A (open a turn, flush a delta, **no `TurnEnd`**) → `stubCursor.set(convB)` → drive conv B's first content. Assert B's delta reached the wire (precondition — the symptom) **and** a `turn_state` carries conv B. **RED on `main`** (the de-dup swallows B's `responding`), **GREEN after the fix**. This is the recovered `TestRepro1050_SwitchWithOpenPriorTurnDropsResponding` — keep its assertion; it already encodes the desired post-fix behavior.
- **`…SwitchResetsTurnIdentity`** (recovered `…GreenWhenLifecycleResetOnSwitch`, **strengthened**). **Delete the manual `e.inTurn = false; e.currentState = ""` lines** — the emitter now does this itself. Assert B gets a `responding` turn_state, B's delta carries a **fresh** `turn_id` (≠ A's), and `seq == 0`.
- **`…CleanPriorTurnEndUnaffected`** (recovered control). Prior turn ends cleanly (`TurnEnd` delivered) before the cursor moves; assert B still gets its `responding` with no extra reset. Isolates the defect to the open-prior-turn carryover and guards against the guard misfiring on the normal path.

Scenarios (developer writes assertions in the project idiom; helpers already exist):
- Single-conversation turn (no switch) still emits exactly one opening `responding` and de-dups intra-turn — regression guard that the guard never fires within one conversation.
- Abandoned conversation's buffered text (if any) flushes stamped with **its** conversation_id before B's opening state (AC3 scoping + wire ordering).

### Real-claude liveness rung (AC5) — liveness only, not the deterministic oracle

New file `internal/e2e/realclaude/interactive_turn_state_liveness_test.go` (`//go:build e2e_realclaude`). Placement alone wires it into `make e2e-realclaude` / `make preship` via the package glob — **no Makefile change** (mirrors #854/#997/#1031).

- Reuse `startPerConversationHarness`, `createConversationViaPhone`, `sealSendMessage` verbatim.
- Add one drain helper `drainForTurnState(t, phone, cs, convID, timeout)` — a near-copy of `drainForAssistantReply` (decrypt every noise_msg in receive order to keep the receive nonce in sync; skip non-matching envelopes) that returns once it observes a `TypeTurnState` envelope whose `TurnStatePayload.ConversationID == convID` and `State` ∈ {`"thinking"`, `"responding"`}. A timeout with no such state is the liveness RED this rung captures on `main`.
- **Case:** create a per-conversation conversation over the wire, `send_message`, and assert `drainForTurnState` returns a `thinking`/`responding` for that conversation within `perTurnReplyBudget` (120s — a cold real-claude PTY). Optionally follow with `drainForAssistantReply` on the same conversation to assert the reply path did not regress (AC2), reusing the same in-order drain discipline.
- **Liveness, not deterministic** (per #997's timing-race finding): the follow-active switch → open-prior-turn precondition only fires when the producer is mid-backoff at the instant the switch lands, which this fast co-located harness rarely reproduces. The deterministic RED lives at the hermetic emitter level above. This rung's value is standing real-claude coverage that `turn_state` genuinely fans on the per-conversation path. Skips cleanly when claude/creds are absent (inherited from `startPerConversationHarness`).

### Full suite

`go test -race ./...` plus `go vet` / `staticcheck` (the CI gate). Known flakes per [[known-test-flakes]] (realclaude SIGTERM, wssclient -race) are re-run, not treated as regressions.

---

## Open questions

- **Abandoned conversation's residual state.** The fix does not emit a `turn_state idle` for the abandoned conversation (its client self-clears on the next turn activity, per the `Stall` self-clear precedent, and the observed defect is only the *new* conversation's missing opening state). Evidence-based: a stale `responding` on the abandoned conversation's client has not been reported. Left as a non-goal; revisit only if observed.
- **`Stall` as the first post-switch event.** `Stall` does not call `startTurnIfNeeded`, so between the guard's `endTurn()` and the next `startTurnIfNeeded` the emitter's `turnID` is still the abandoned turn's. A `Stall` arriving as the very first event after a switch would emit with the stale `turnID` (but the correct live `conversation_id`). `Stall` is explicitly not turn-scoped (`turn_id` on a stall is cosmetic) and a stall onset requires prior mid-turn tracker activity, so "first post-switch event is a Stall" is degenerate. Not designed for; noted so the developer does not add `turnID` reset to the guard (that reset is `startTurnIfNeeded`'s single responsibility).
- **One-event misattribution at the exact switch instant.** `Handle` reads the cursor fresh at its top (the established #589 pattern), so an event tailed just before the cursor flip but Handled just after is attributed to the new conversation. Pre-existing property of the emitter, not introduced or fixed here; out of scope for #1062.
