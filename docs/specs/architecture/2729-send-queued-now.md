# #2729 — `send_queued_now` delivers a queued message into the running claude turn

## Files read

- `internal/msgqueue/queue.go` → `Remove`, `commitGate`, `drain`, `notifyDelivered`, `QueuedMessage`: the claim-versus-drop race under `q.mu` that a send-now take must lose the same way; `deliverCancel` is how a taken waiting head's attempt is aborted.
- `internal/relay/v2session_modal.go` → `handleDequeueMessage`: the handler to mirror (interactive gate, nil-seam inert, tolerant decode, content-free logs, no reply).
- `internal/relay/v2session_seams.go` → `QueueRemover`, `V2SessionConfig.QueueRemover`: where the new consumer-side interface and config field sit.
- `internal/relay/v2session.go` → `dispatchAppFrame` switch: the intercept beside `TypeDequeueMessage`.
- `internal/protocol/codes.go`, `messaging.go` → `TypeDequeueMessage`, `DequeueMessagePayload`: the type constant and payload to mirror; `compat_test.go` tables list every v2 inbound type.
- `cmd/pyry/main.go` → `msgqueue.New` wiring, `newInboundDeliver`, `sessionRouter.resolve`, `boundSession`, `settingsUpdaterAdapter.Capabilities` (`MidTurnInput` stays false).
- `cmd/pyry/channel_carry.go` → `carryPending`, `clearDelivered`: the per-conversation composed count a send-now `OnDelivered` must not consume.
- `cmd/pyry/stream_turn_busy.go` → `observe`, `applyBusyLocked`, `openForDelivery`, `Busy`: the tracker the AC4 guard extends.
- `docs/knowledge/features/e2e-realclaude-mid-turn-user-capture-test-go.md`: a mid-turn write folds during a tool call, otherwise opens a second turn with its own `result`. Production does not pass `--replay-user-messages`, so no echo reaches the tracker to tell the two apart.
- `internal/sessions/pool.go` → `HarnessFor`: the Codex check.

## Context

Every queued message waits for idle. The phone wants a **Send now** that writes a queued message into the running claude turn. #2728 measured what claude does with such a write. `mid_turn_input` stays false; #2730 flips it.

## Design

### msgqueue

- `type SendNowFunc func(ctx context.Context, convID string, payload []byte) error` and `Config.SendNow` (nil ⇒ `SendNow` is inert, returns false).
- `QueuedMessage.SentNow bool`: set only on the delivered projection of a send-now delivery, so `OnDelivered` consumers can tell the two kinds apart. Not projected by `Snapshot`.
- `func (q *Queue) SendNow(convID string, id uint64) bool`:
  1. Under `q.mu`: find the item; unknown id, or the head while `committing`, returns false. Otherwise **take it out** of `items` (so a concurrent `Remove`/drain cannot claim it), remember its id, and if it was the head capture `deliverCancel`.
  2. Off-lock: cancel the head's in-flight attempt (as `Remove` does), then call `SendNow(ctx, convID, delivery)` with the queue's lifecycle ctx.
  3. Error ⇒ re-insert the item at its id-ordered position (ids are monotonic, so that is its original position), except that it never goes in front of a head the drain is `committing`; re-spawn a drain if needed; return false. No notify (the backlog is back as it was).
  4. Nil ⇒ `notify` (the `queue_state` without it), then `notifyDelivered` with `SentNow: true`; return true.

Removing during the write is what makes the head race safe: `commitGate` and `advanceLocked` already treat a head that is no longer at the front as dropped.

### relay

- `protocol.TypeSendQueuedNow = "send_queued_now"` and `SendQueuedNowPayload` (same fields as `DequeueMessagePayload`).
- `QueueSender interface { SendNow(conversationID string, queuedMsgID uint64) bool }`, `V2SessionConfig.QueueSender`.
- `handleSendQueuedNow(s, env)`: identical shape to `handleDequeueMessage` (non-interactive inert, nil seam inert, tolerant decode, Info on success / Debug on no-op, fields `conn_id`, `conversation_id`, `queued_msg_id` only, no reply).

### cmd/pyry

- `newSendNowDeliver(resolve, isClaude, busy) msgqueue.SendNowFunc`: resolve the conversation's bound writer; refuse a non-Claude session (`errSendNowNotClaude`); `busy.openForSendNow(convID)` returns false when the turn is idle ⇒ `errSendNowIdle`, nothing written; else `WriteUserTurn`; on write error run the undo. No `Activate`: a running turn has a live child, and an absent one must fail, not spawn.
- `sessionRouter.isClaude(convID)`: `HarnessFor` on the bound session equals `sessions.HarnessClaude`.
- Wiring: `SendNow: newSendNowDeliver(router.resolve, router.isClaude, turnBusy)` — deliberately NOT through `carryPending` or `markApprovalHolds`; `QueueSender: w.queue` in `relay.go`.
- `channelCarry.clearDelivered` returns early on `msg.SentNow`, so a send-now delivery never consumes the head's composed count (AC3).

### turnBusyTracker (AC4)

- `carry map[string]bool`, `grace map[string]*time.Timer`, `graceAfter time.Duration` (default `sendNowGrace = 10s`).
- `openForSendNow(convID) (ok bool, undo func())`: under `t.mu`, false if the conversation is not busy; otherwise set `carry[convID]`.
- `observe`'s close for a conversation with `carry` set does not close: it clears `carry` and arms a grace timer that closes the mark when it fires. Any opener for that conversation stops the timer (the write's own turn is now open and its `TurnEnd` closes it normally). Fold case: the one `TurnEnd` arms the grace, which expires and closes. Second-turn case: turn 1's `TurnEnd` arms the grace, turn 2's opener cancels it, turn 2's `TurnEnd` closes.
- Every other close (`applyBusyLocked` close branch: teardown, exit, undo, grace) drops `carry` and stops any timer.
- The undo: clears `carry` if still set; if a grace is armed, stops it and closes now.

## Concurrency model

`SendNow` runs on the relay manager's Run goroutine, like `Remove`; the stdin write is a pipe write, not a commit wait. The grace timer callback takes `t.mu` and goes through `applyBusyLocked` (the one close-and-replace protocol). A stale timer firing after a stop is guarded by comparing the stored timer pointer under the lock.

## Error handling

Any `SendNowFunc` error leaves the message at its original position; the drain delivers it at idle. Log lines carry no text, no client message id.

## Testing strategy

- msgqueue: waiting head taken (attempt cancelled, `OnDelivered` once with `SentNow`, others still drain in order); non-head taken; committing head refused; unknown id; seam error re-inserts in place and drains later; nil seam inert.
- relay: interactive call reaches the seam with the decoded ids; non-interactive and nil seam inert.
- cmd/pyry: `newSendNowDeliver` idle ⇒ no write; Codex ⇒ no write; busy ⇒ write; write error ⇒ undo. Tracker: close with carry stays busy until grace; opener during grace cancels it and the next close closes; undo after an armed grace closes. `clearDelivered` skips `SentNow`.

## Open questions

- Grace length: 10s trades a fold-case delay of the next queued message against a slow second-turn opener. Bounded either way.

## Documentation handoff

Pending for the documentation stage: `docs/protocol-mobile.md` § Queue (v2) — a `#### send_queued_now` subsection after `dequeue_message` and a message-type table row beside it, saying: inbound control, any paired client on an interactive connection, same fields as `dequeue_message`, no reply; acknowledged by the `queue_state` change and the operator `message` push; a no-op when the turn is idle, the id is unknown, or the session is Codex; delivered when the stdin write succeeds, not retried if the child dies before claude reads it.
