# #2782 — log when a queued delivery waits for a busy turn

## Files read

- `cmd/pyry/stream_turn_busy.go` → `WaitIdle`: the generation-channel wait loop; membership check and channel capture share one lock acquisition (lost-wakeup rule in its doc). Callers outside the delivery seam: `session_reset.go` and `relay_context_usage.go`, both of which must stay silent.
- `cmd/pyry/stream_turn_busy.go` → `waitIdleForDelivery`: the delivery seam's bounded wait; nil-receiver and empty-id guards return nil at once.
- `cmd/pyry/stream_turn_busy.go` → `newTurnBusyTracker`, `t.logger`: the logger the tracker already holds (nil falls back to `slog.Default`); existing records use the `"relay: stream-turn …"` message prefix plus an `event` key.
- `cmd/pyry/stream_turn_busy.go` → the comment in `clearForSession` on why the unresolved-clear record carries `session_id` only. That record withholds a *resolved* conversation id for a session that has none. Here the conversation id is the caller's own key, and cmd/pyry already logs `conversation_id` widely (`channel.go`, `conversation_history.go`, `interactive_turn_v2.go`), so the ticket's field is in line with existing practice.
- `cmd/pyry/main.go` → `newInboundDeliver`: the sole caller of `waitIdleForDelivery`; unchanged.
- `cmd/pyry/stream_turn_busy_test.go` → the `WaitIdleForDelivery*` tests: must pass unchanged.
- `cmd/pyry/stdio_permission_test.go` → `lockedBuffer`: reusable race-safe log sink for a test that reads the log while the waiter goroutine runs.

## Change

`waitIdleForDelivery` emits one INFO record the first time its wait finds the conversation busy:

- message `relay: stream-turn delivery held; conversation busy`, `event` = `stream_turn.delivery_hold`, `conversation_id`. No payload, no session id, no other fields.

To make "finds it busy" exact rather than a separate pre-check (a `Busy` call before `WaitIdle` is a second lock acquisition, so a turn opening between the two would park the delivery silently — the very gap the ticket closes), `WaitIdle`'s loop moves into an unexported `waitIdle(ctx, conversationID, onPark func())`. `onPark`, when non-nil, runs once, after the lock is released, on the first iteration that finds the conversation busy, and before the `select`. `WaitIdle` calls it with nil, so its two other callers stay silent and its contract is unchanged. `waitIdleForDelivery` passes a closure that logs. Later iterations — wakeups from other conversations' transitions — never call it again. The nil-tracker and empty-id guards return before any of this, and an idle conversation returns on the first iteration without parking, so those paths log nothing.

Ordering, cancellation, timeout and retry are untouched: the same loop, the same bounded context, the same returned errors. The record goes out before the wait returns on every path, including timeout and cancel.

No new exported type; `newInboundDeliver` and `newTurnBusyTracker` keep their signatures.

## Testing strategy

New test in `stream_turn_busy_test.go`, with a JSON handler over a `lockedBuffer`:

- **busy, then other-conversation churn, then clear:** conversation A open; start `waitIdleForDelivery` in a goroutine; poll until the hold record appears (proving the waiter has captured its generation channel); open and close conversation B so the waiter wakes and re-parks; close A; the wait returns nil. Exactly one hold record, level INFO, `conversation_id` = A, and the opener's text appears nowhere in the log.
- **busy, timeout:** one record, `DeadlineExceeded` still returned.
- **idle, empty id:** no record. (Nil receiver has no logger; the existing nil-receiver test covers its return.)

Existing `WaitIdleForDelivery*`, `WaitIdle` and `newInboundDeliver` tests run unchanged.

## Documentation handoff

Pending for the documentation stage: in `docs/knowledge/features/streamsup-package-per-conversation-turn-busy-track-delivery-seam-consumer-mid-turn-hold.md`, in the intro under `# Delivery-seam consumer, mid-turn hold (#1199)`, name the INFO record (`event=stream_turn.delivery_hold`, `conversation_id`) and say it fires once for each delivery attempt that waits.

## Security review

**Verdict:** PASS

**Findings:**

- [Trust boundaries] No findings. Nothing new crosses a boundary: the record reads only `conversationID`, which `newInboundDeliver` received from msgqueue's own key, and the payload never reaches `waitIdleForDelivery` (its signature carries none), so it cannot be logged by construction.
- [Tokens, secrets, credentials] No findings. No token, session id or payload is in scope of the closure; the record's fields are fixed literals plus `conversation_id`.
- [File operations] No findings. No file I/O.
- [Subprocesses] No findings. None started.
- [Cryptography] No findings. None used.
- [Network and I/O] No findings. Log volume is bounded at one record per delivery attempt, and attempts are bounded by msgqueue's retry policy; other-conversation churn cannot multiply it because `onPark` fires once per `waitIdle` call.
- [Errors, logs, telemetry] SHOULD FIX (handled in the build): the test asserts the opener's text is absent from the log, so a later widening of the record to carry content fails. `conversation_id` is logged deliberately, per the ticket and existing cmd/pyry practice; `session_id` is not.
- [Concurrency] No findings. `onPark` runs after `t.mu` is released, so the logger (which may block on I/O) is never called under the tracker's lock; the membership check and channel capture still share one acquisition, preserving the lost-wakeup rule. No goroutine is added.
- [Threat model] No findings. Daemon-local log line; no relay or mobile surface changes.

**Reviewer:** builder (self-review per the security-review checklist)
**Date:** 2026-10-04
