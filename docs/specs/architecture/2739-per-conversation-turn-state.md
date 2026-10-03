# #2739: a conversation's events are dropped while another conversation is active

## Files read

- `cmd/pyry/stream_turn_drain.go` → `startStreamTurnDrainV2`: the active-session gate that drops every non-active session's event (`stream_turn.not_active`), after `busy.observe`.
- `cmd/pyry/interactive_turn_v2.go` → `interactiveTurnEmitterV2`, `Handle`, `startTurnIfNeeded`, `endTurn`, `flushDelta`, `closeForConversation`, `turnPhaseSnapshot`: one open turn, one delta buffer, one phase; the #1062 follow-active switch in `Handle`.
- `cmd/pyry/relay.go` → the stream branch of the relay leg that builds the emitter and the drain (`activeSession` closure over `boundSessionIDForActive`); `conversationForSession`, the resolver `turnBusyTracker` already uses.
- `cmd/pyry/stream_turn_drain_test.go` → `stubActiveSession`, `dropWatcher`, `TestStreamTurnDrainV2_ScopingDropsBackground`, `TestStreamTurnDrainV2_PostRotationForwardsAndStaleStillDrops`.
- `cmd/pyry/interactive_turn_v2_switch_test.go`: the #1062 follow-active switch tests, which pin the contract this ticket removes.
- `internal/e2e/relay_v2_stream_interrupt_test.go` → `TestRelayV2_StreamInterruptNamedConversationStopsThatOne` M4(b), which waits for the dropped record.
- `internal/e2e/relay_v2_stream_background_task_frames_test.go`, `internal/e2e/channel_post_test.go` → `waitForHistoryEntries`: the replay-rider and history-on-disk patterns the new e2e copies.
- `internal/e2e/internal/fakeclaude/main.go` → `loadStreamReplay`: REPLAY_FIRST / SECOND / RELEASE; the first child to see the release file deletes it.

No in-flight feature branch touches these files.

## Context

The daemon attributes every stream event to the single global active conversation. The drain drops any event whose session is not the active conversation's bound session, and the emitter — the only writer of history, ring, client frames and the turn-end wake — holds one turn, closed whenever the cursor moves. Two conversations used at once therefore lose each other's turn tails for good (observed 2026-10-03, 3141cc9b / d766ef15). The fix attributes each event to its own conversation by session id and keeps turn state per conversation.

## Design

**Drain.** `startStreamTurnDrainV2`'s `activeSession func() (string, bool)` parameter becomes `conversationFor func(sessionID string) (conversationID string, ok bool)`. After `busy.observe` and the echo hand-off, the drain resolves `env.sessionID`; on `!ok` it logs Debug `stream_turn.no_conversation` with `kind` and `session_id` only and drops the event. Otherwise it calls `emitter.HandleFor(ctx, convID, ev)`. Production passes `conversationForSession(w.convReg, sid)`, so a tail from a just-rotated session (in `SessionHistory`) reaches its own conversation instead of being dropped.

**Emitter state.** The single-turn fields (`inTurn`, `turnID`, `turnConvID`, `seq`, `currentState`, `childLanes`, `deltaBuf`, `deltaMsgID`, `deltaParent`, `deltaConvID`) move into a new unexported `convTurnState` struct. The emitter keeps `turns map[string]*convTurnState` and embeds `*convTurnState` as the *selected* conversation's state; `HandleFor` selects (creating on first sight) the state for its conversation before running the unchanged per-kind switch. Field promotion means every method body, and every existing test that reads `e.inTurn` / `e.turnID` / `e.seq` after driving one conversation, keeps working unchanged. The constructor selects an empty placeholder so a fresh emitter's fields read as zero.

**Entry points.** `HandleFor(ctx, convID, ev)` is the new contract. `Handle(ctx, ev)` stays as a thin wrapper that reads the cursor and calls `HandleFor` — kept for its 182 test call sites. The #1062 follow-active switch is deleted: a cursor move no longer ends the prior conversation's turn, because that conversation's events now keep arriving under its own id.

**Coalescing timer.** One shared timer, armed when a buffer goes empty→non-empty and no other conversation has buffered text (so the window still runs from the oldest unflushed chunk). Per-conversation flush (`flushDelta`, flushing the selected conversation, called from Handle's arms) stops the timer only when no conversation has buffered text left. The drain's `flushC` case calls a new `flushAll`, which flushes every conversation with buffered text and stops the timer.

**closeForConversation.** Selects that conversation's state if it exists and closes only its turn.

**turnPhaseSnapshot.** Holds `map[convID]state`; `publish(convID, state)` sets or (idle) deletes that entry, `clear(convID)` deletes it, `running()` returns one payload per entry, sorted by conversation id for determinism. `endTurn` clears only the selected conversation.

**Cursor.** `activeConversation` keeps choosing reconnect replay's default (`SetReplaySource`) and the `Handle` wrapper. `boundSessionIDForActive` loses its drain caller; it stays if other callers remain, otherwise it is removed.

## Concurrency model

Unchanged. The drain goroutine is still the only caller of `HandleFor`, `flushDelta`, `flushAll` and `closeForConversation`, so the per-conversation map and the embedded selection are single-goroutine state. `turnPhaseSnapshot` stays the one cross-goroutine value, guarded by its leaf mutex (never held across a push). No goroutine is added.

## Error handling

An event with no resolvable conversation is dropped with a content-free Debug record. A turn-id mint failure leaves only that conversation's turn closed. `turns` entries live for the daemon's life; a conversation's entry is removed when its turn ends with no buffered text, so the map holds at most the conversations with a turn open or text buffered.

## Testing strategy

- New unit test (`interactive_turn_v2_switch_test.go`, replacing the follow-active switch tests): interleave turns on A and B through `HandleFor` — each keeps its own turn id and seq numbering, buffered text is attributed to its own conversation, A's turn end leaves B's turn open, and `turnPhaseSnapshot.running` reports both running turns.
- Timer: buffered text on two conversations, one `flushAll` emits both deltas.
- Drain tests: `stubActiveSession` becomes a session→conversation resolver stub with the same `set`/`get` names; `ScopingDropsBackground` becomes "background conversation forwarded under its own id"; the stale half of `PostRotationForwardsAndStaleStillDrops` now forwards the stale tail to its own conversation; the "no active" tests now assert the `stream_turn.no_conversation` drop. `dropWatcher` watches the new event name.
- e2e (`internal/e2e/relay_v2_stream_background_conversation_test.go`): A bound to bootstrap, B minted; REPLAY_FIRST = tool_use, REPLAY_SECOND = tool_result + text + result, RELEASE re-created until both children consumed it. Send to A, await A's tool_use, send to B, await B's tool_use (cursor now on B), release; assert A's tool_result, assistant text and turn_end reach the wire stamped A, and A's on-disk history holds the tool_result and turn_end.
- `relay_v2_stream_interrupt_test.go` M4(b) asserts B's turn_end frame on the wire; diagnostics in `relay_v2_stream_send_test.go` and `per_conversation_eviction_test.go` reworded.

## Open questions

- Whether `boundSessionIDForActive` has callers besides the drain; delete it only if none remain.
- Whether any existing emitter test relies on the follow-active switch outside the switch test file; the package run settles it.

## Documentation handoff

Pending for the documentation stage: the interactive turn emitter / stream drain package overview (`docs/knowledge/features/` topic owning `stream_turn_drain` and the interactive v2 emitter) should replace the "drain gates on the active conversation's bound session" and #1062 follow-active-switch descriptions with per-conversation attribution by session id and per-conversation turn state.

## Security review

**Verdict:** PASS

**Findings:**

- [Trust boundaries] No new boundary. The session id on a fan-in envelope is daemon-authored (`streamSessionTag`, set by the runner factory), never claude's or a client's. Attribution to a conversation goes through `conversationForSession` over the daemon's own registry, the resolver `turnBusyTracker` already trusts. Removing the active gate does not widen who sees frames: delivery was and stays capability-gated in `emit` (interactive conns only), and the paired operator's phones already receive every conversation's frames when it is active; frames carry their conversation id so clients filter by view.
- [Tokens] No findings: no token, key or credential is touched.
- [File operations] No new file path. History appends go through the existing `appendConversationHistory` with the resolved conversation id, which comes from the registry, never from event content.
- [Subprocesses] No findings: no child is spawned or signalled by this change.
- [Cryptography] No findings. Turn ids still come from `conversations.NewID` (crypto/rand); nothing new is random.
- [Network and I/O] SHOULD FIX: per-conversation state is a new allocation keyed by conversation. It must be bounded: an entry is created only for a conversation the registry resolves, and removed when its turn ends with nothing buffered. The flush loop still splits at `maxDeltaTextBytes`, per conversation.
- [Errors, logs] SHOULD FIX: the new `stream_turn.no_conversation` drop carries `event`, `kind` (via `eventKind`, variant name only) and `session_id` — never event content. The test asserts the field set.
- [Concurrency] No new goroutine or lock. The per-conversation map and the embedded selection are touched only on the drain goroutine; `turnPhaseSnapshot`'s map is behind its existing leaf mutex and `running` copies under the lock before building payloads.
- [Threat model] `docs/protocol-mobile.md` § Security model: all paired devices belong to the one operator; no per-device conversation authorization exists to bypass. Per-client view filtering is out of scope (the ticket places it at the outbound edge, not before the history write), no ticket needed today.

**Reviewer:** builder (self-review per the security-review checklist)
**Date:** 2026-10-03

## Revisions

### 2026-10-03: startup frames now reach the bound conversation's history (verifier rework 1)

The verifier's gate found four e2e regressions the Testing strategy missed: `TestRelayV2_MCPActuationGatedAuditedAndAnsweredFresh`, `TestRelayV2_MCPStatusRequestQueriesLiveChildRequesterOnly`, `TestRelayV2_StreamMCPStatusReachesConnectedPhone` and `TestRelayV2_ConversationHistory`. All four relied on the bootstrap child's startup frames being dropped because no client had routed a message yet, so no active cursor existed.

**Decision: startup frames with no turn behind them are recorded, not gated.** The production design is unchanged. A frame such as `mcp_status` describes the child, and the child belongs to its bound conversation, so it is that conversation's history. On main the same frames already reached history whenever a child started for the active conversation; only the no-cursor case dropped them, which was an artefact of the active gate this ticket removes. Gating them in the emitter would reintroduce a cursor-shaped rule for one class of event.

**Test changes.**
- The three MCP-status tests synced on the `stream_turn.not_active kind=mcp_status` drop record. They now sync on the bootstrap status's entry in the bound conversation's on-disk history, through a new e2e helper `waitForHistoryType`, which proves the emitter handled that frame before any phone connected. Their later expectations were unaffected: a phone that connects without a resume cursor receives no ring replay.
- `TestRelayV2_ConversationHistory` walked a seeded log on the conversation bound to the bootstrap session, so the bootstrap child's startup entries raced the walk. It now seeds the walked conversation bound to a session no child runs, and binds a separate conversation to the bootstrap session, keeping the walk's exact-count claim deterministic.
