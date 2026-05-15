# 396 — send_message triggers lazy respawn after idle eviction

## Context

`--pyry-idle-timeout` evicts an idle bootstrap session by cancelling its
supervisor (claude is SIGKILLed); the session moves to `stateEvicted`.
Lazy respawn-on-attach is implemented via `Session.Activate`, and
`handleAttach` in `internal/control/server.go` calls it before binding
the bridge. The CLI `pyry attach` path therefore respawns correctly.

The **relay-routed inbound path** does not. The flow is:

```
phone/plugin WS → relay → dispatch.Run → handlers.SendMessage(sess)
                                              ↓
                                  sess.WriteUserTurn(convID, text)
                                              ↓
                          supervisor.WriteUserTurn(id, payload)
                                              ↓
                              ptmx == nil ?  → return nil (silent drop)
```

After idle eviction the bootstrap session's supervisor has `ptmx == nil`.
`Supervisor.WriteUserTurn` matches `Bridge.Write`'s discard-on-unattached
semantics and returns `nil` (`internal/supervisor/supervisor.go:167-169`).
The handler emits `send_message.ack` to the phone and the user-visible
result is: ack delivered, no claude, no respawn, no log line tying the
silence to eviction. Discord/Telegram channel-routed traffic is the
observed casualty (pyrybox, 2026-05-15: 7.5h silent outage between SIGKILL
and manual `systemctl restart`).

This is supervision-incomplete: the supervisor is alive, the supervisory
promise is broken, and no operator signal is produced.

### Scope choice

Path A. The cost is bounded (3 production files, ~50 prod-line diff) and
it closes the actual outage rather than papering it. Path B would still
leave Discord/Telegram inbound dropped — the bot would not respond until
a human attached over the CLI. Path A makes `send_message` an attach
event in its own right; `pyry attach` and Discord/Telegram inbound use
the same `Activate → drive` shape after this lands.

The help-text contract from the issue body ("respawn latency 2-15s on
next attach") is already gone from `cmd/pyry/main.go:1329` after #395
landed; no doc change is part of this ticket.

### Race we have to close

`Session.Activate` today returns when the lifecycle goroutine has
transitioned `stateEvicted → stateActive` and persisted — i.e., when
`activeCh` is closed by `transitionTo` in `internal/sessions/session.go:380-416`.
That close happens **before** the next iteration of `Session.Run`
schedules `runActive`, which then starts `supervisor.Run` on a
goroutine. `supervisor.Run → runOnce → pty.Start → setPTY(ptmx)` is the
sequence that binds `ptmx`. There is a real ~hundreds-of-ms window
where Activate has returned but `ptmx` is still nil. A `WriteUserTurn`
issued in that window is silently dropped — the *same* failure mode the
bug describes, just shifted by milliseconds.

The control-plane attach path masks this race because `Bridge.Attach`
queues bytes via `b.in` until `Bridge.SetPTY` is called. The relay path
writes to `ptmx` directly via `Supervisor.WriteUserTurn`; there is no
queue. We close the race by strengthening Activate's promise:

> After `Session.Activate(ctx)` returns nil, the session's supervisor
> has a bound PTY and `WriteUserTurn`/`Resize` will reach the live
> claude.

This is the only sane Activate contract; the attach path benefits too
(it removes a known foot-gun on the WriteUserTurn-first ordering that
the streaming-cursor handler will eventually want).

## Files to read first

- `internal/sessions/pool.go:297-425` — `New` and the `#202`
  bootstrap-state invariant. Why bootstrap warm-starts in `stateActive`
  regardless of persisted lifecycle. New code must not break this.
- `internal/sessions/session.go:198-247` — `Activate`/`Evict` shapes,
  `activeCh`/`evictedCh` semantics. We extend Activate; the existing
  channel discipline is unchanged.
- `internal/sessions/session.go:263-416` — `Run`, `runActive`,
  `runEvicted`, `transitionTo`. The lifecycle goroutine; we add one
  log line in the timer-fire branch (~340).
- `internal/supervisor/supervisor.go:90-194` — `Supervisor` struct,
  `WriteUserTurn`, `setPTY`. We add a `ptmxReadyCh` field, init it in
  `New`, broadcast it in `setPTY`, expose `WaitForPTY(ctx)`.
- `internal/supervisor/supervisor.go:196-220` — `New`. Add the chan
  init.
- `internal/supervisor/supervisor.go:304-420` — `runOnce`. Verifies
  `setPTY(ptmx)` is the readiness moment in both bridge and no-bridge
  branches. No code change here.
- `internal/relay/handlers/send_message.go` — extend `TurnWriter` to
  add `Activate(ctx) error`; call Activate before WriteUserTurn.
- `cmd/pyry/relay.go:87-148` — `startRelay` wires the session into
  `handlers.SendMessage(sess, …)`. `*sessions.Session` continues to
  satisfy the extended interface adapter-free (it already has
  `Activate(ctx context.Context) error`).
- `internal/relay/handlers/send_message_test.go:27-37, 110-220` —
  `stubTurnWriter` and the four existing handler-level tests. Stub
  gains `Activate`; one new test exercises the Activate→WriteUserTurn
  ordering.
- `internal/e2e/idle_test.go:45-100` — `TestE2E_IdleEviction_LazyRespawn`
  via VerbAttach. Reference shape for #398's send_message variant; this
  spec does NOT add a new e2e test (that's #398's job).
- `internal/control/server.go:33-48, 656-740` — the existing Session
  interface and `handleAttach` flow, both reusing the strengthened
  Activate contract. No code change here; just verifying nothing breaks.

## Design

### Three small changes

1. **`Supervisor.WaitForPTY(ctx)` plus chan plumbing.**
   New field on `Supervisor`:
   ```go
   ptmxReadyCh chan struct{} // closed when ptmx != nil; freshened by setPTY(nil)
   ```
   - `New` initializes `ptmxReadyCh = make(chan struct{})` (open).
   - `setPTY(f)` under `ptmxMu`: if `f != nil` and `ptmxReadyCh` is open,
     `close(ptmxReadyCh)`; if `f == nil` and `ptmxReadyCh` is closed,
     allocate a fresh open chan. Idempotent if called twice with the
     same shape.
   - New method `WaitForPTY(ctx context.Context) error`: snapshots
     `ptmxReadyCh` under `ptmxMu`, then selects on it and `ctx.Done()`.
     Returns `ctx.Err()` on cancel, `nil` on close.

   Locking invariant unchanged: `ptmxMu` stays leaf-only, never held
   while acquiring `convMu` or `mu`. The chan reference capture under
   `ptmxMu` followed by an unlocked select is the standard pattern
   already used in `Session.Activate` for `activeCh`.

2. **`Session.Activate` awaits PTY readiness.**
   At the end of the existing function — after the `select { case <-ch
   : … case <-ctx.Done(): … }` block returns nil — call
   `s.sup.WaitForPTY(ctx)` and return its error verbatim. Two-line diff
   in body, plus a contract update in the doc comment:

   > After Activate returns nil, the underlying supervisor has a bound
   > PTY and WriteUserTurn/Resize will reach the live claude. ctx
   > cancellation while waiting for PTY readiness returns ctx.Err().

   No change to `activeCh`/`evictedCh` choreography or the `#202`
   bootstrap-state invariant: Activate's *trigger* is still the state
   flip, the *return* now waits one extra step.

3. **Eviction log line at SIGKILL.**
   In `runActive` at the timer-fire branch
   (`internal/sessions/session.go:333-343`), before `cancelSup()`,
   emit a structured WARN-level log:
   ```
   "session: idle eviction firing"
     event="session.idle_eviction"
     session_id=<id>
     idle_timeout=<duration>
     bootstrap=<bool>
   ```
   This is the SIGKILL-cause record AC#3 requires. The existing
   supervisor-side `"claude exited" err="signal: killed"` line at
   `internal/supervisor/supervisor.go:265` stays — it's the truthful
   PTY-level event — but is now preceded by a session-level line that
   names the cause.

4. **`handlers.SendMessage` plumbs Activate before WriteUserTurn.**
   - Extend `TurnWriter` (one new method):
     ```go
     type TurnWriter interface {
         Activate(ctx context.Context) error
         WriteUserTurn(conversationID string, payload []byte) error
     }
     ```
   - In the handler body, before the existing `w.WriteUserTurn(...)`
     call, run:
     ```go
     activateCtx, cancel := context.WithTimeout(ctx, sendMessageActivateTimeout)
     defer cancel()
     if err := w.Activate(activateCtx); err != nil { … }
     ```
   - `sendMessageActivateTimeout` is a package-private const at the top
     of `send_message.go`, value `30 * time.Second` (matches
     `internal/control/server.go:676` so behavior is uniform across
     attach events).
   - On Activate error: log a structured WARN (`event=
     "send_message.activate_failed"`, fields: `conn_id`,
     `conversation_id`, `err`), then return the error via
     `replyError(ctx, c, env, protocol.CodeServerError, …, false)` if
     it's the timeout/ctx case; ctx.Canceled propagates as the existing
     handler conventions do (return the error so dispatch's Run unwinds
     for that conn).

   The handler's existing `WriteUserTurn` error switch is unchanged.

### Data flow after the change

```
phone/plugin send_message → handlers.SendMessage
                              ↓
                       w.Activate(activateCtx)         ← new
                              ↓ (blocks 0…30s)
                       supervisor has bound PTY        ← new guarantee
                              ↓
                       w.WriteUserTurn(convID, text)
                              ↓
                       ptmx.Write(payload) succeeds
                              ↓
                       handler emits send_message.ack
```

On an already-active session, Activate is a no-op:
- `Session.Activate` finds `lcState == stateActive`, no signal sent,
  receives on already-closed `activeCh` immediately;
- `Supervisor.WaitForPTY` finds `ptmxReadyCh` already closed,
  receives immediately.
Combined cost on the steady-state hot path: two non-blocking channel
receives. Negligible.

### Concurrency model

- `Supervisor.ptmxReadyCh` is protected by the existing `ptmxMu`. The
  chan reference is captured under lock then awaited unlocked — same
  pattern as `Session.activeCh`. No new lock; no change to lock order.
- Multiple concurrent send_message handlers each call Activate. The
  primitive collapses concurrent Activates onto the single
  `activateCh <- struct{}{}` signal; all wait on the shared `activeCh`.
  After PTY-ready, each runs its own `WriteUserTurn` under `ptmxMu`.
  Order across concurrent inbound messages is preserved by the
  dispatcher's per-conn serialization; cross-conn order is undefined
  (it always was).
- The lifecycle goroutine continues to be the only writer of
  `lcState`/`activeCh`/`evictedCh`. `setPTY` continues to be called
  only from `runOnce` (supervisor's goroutine). No new writer paths.

### Error handling

| Failure                                              | Behavior                                                                    |
|------------------------------------------------------|-----------------------------------------------------------------------------|
| Activate ctx expires (30s elapsed, no PTY)           | WARN `send_message.activate_failed`, wire `protocol.CodeServerError`, no ack |
| Activate returns ctx.Canceled (conn closing)         | Propagate up, dispatcher's per-conn unwind handles it (unchanged contract)  |
| Activate succeeds, WriteUserTurn validation refuses  | Existing branch — `protocol.CodeConversationNotFound` reply, no change      |
| Activate succeeds, ptmx writeable, write fails       | Existing wrapped error path (`supervisor: write user turn:`), no change     |

The Activate failure modes are new wire surface but reuse the existing
`replyError` shape and an existing error code (`CodeServerError`). No
new protocol code or payload is introduced.

### Why not change `WriteUserTurn` to wait internally

Tempting, but it would either (a) require `WriteUserTurn` to accept a
context, breaking the established `TurnWriter` shape, or (b) embed an
unbounded internal wait. Path A's wait belongs at the Activate seam
because Activate is already the named operation for "ensure session is
ready" — strengthening its contract is a single conceptual move.

### Why not restructure transitionTo to defer activeCh close

Considered. Would entangle the `#202` bootstrap-state invariant
documented in `pool.go:323-333` (closing activeCh out of band changes
when `New` returns to callers warm-starting in `stateActive`). The
WaitForPTY layering keeps Activate's mid-flight semantics unchanged and
adds one extra wait at the tail. Smaller blast radius.

## Testing strategy

Unit additions (the developer writes the table/test bodies in the
project's testing idiom; this spec only enumerates scenarios):

1. **`Supervisor.WaitForPTY` contract** (`internal/supervisor/supervisor_test.go`)
   - Already-set ptmx returns immediately.
   - Not-yet-set ptmx blocks; `setPTY(ptmx)` unblocks waiter.
   - `setPTY(nil)` after a prior set freshens the chan; next `WaitForPTY`
     blocks again until next non-nil `setPTY`.
   - ctx cancel while waiting returns `ctx.Err()`.

2. **`Session.Activate` PTY-readiness guarantee** (`internal/sessions/session_test.go`)
   - Drive a session through `Evict → Activate` using `/bin/sleep` as
     the claude binary (same harness as
     `TestSession_IdleEvictionDeferredWhileAttached`). Assert that by
     the time Activate returns, the supervisor's `ptmxReadyCh` is
     closed (introspected via a new test-only helper, or asserted
     indirectly: a `Supervisor.WaitForPTY(ctxNoWait)` returns nil
     immediately after Activate).
   - ctx cancel during the PTY-wait portion returns `ctx.Err()` and
     does NOT roll back the state flip.

3. **`handlers.SendMessage` Activate ordering** (`internal/relay/handlers/send_message_test.go`)
   - `stubTurnWriter` gains an `Activate` method that records call
     order against `WriteUserTurn`. Scenarios:
     - Happy path: Activate called before WriteUserTurn; ack emitted.
     - Activate returns ctx.DeadlineExceeded: WriteUserTurn NOT called;
       `send_message.activate_failed` log emitted; wire reply uses
       `CodeServerError`.
     - Activate returns conversations.ErrConversationNotFound (won't
       happen in production today, but pins the "Activate error
       short-circuits WriteUserTurn" branch for future).

4. **Idle-eviction log line** (`internal/sessions/session_test.go`)
   - Reuse the existing `TestSession_IdleEvictionDeferredWhileAttached`
     harness with a slog handler that records records. Detach, wait
     for eviction, assert one record exists with
     `event=session.idle_eviction` and the expected fields.

E2E is intentionally NOT added here. #398 is the dedicated e2e ticket
and is blocked-by this one; once this lands it can drive an end-to-end
exercise of the relay-routed path via `fakerelay`.

### What counts as an attach event (AC#4)

For #398's purposes:

> A `send_message` envelope received over an authenticated relay-routed
> WS conn is an attach event for idle-respawn semantics. It triggers
> `Session.Activate(activateCtx)` with the same 30s budget the CLI
> attach path uses, and the supervisor is fully ready (PTY bound)
> before `Supervisor.WriteUserTurn` is invoked.

## Open questions

- **Should `pyry status` surface "evicted" lifecycle state?** Out of
  scope here; AC#1 is satisfied by Path A's automatic respawn. If a
  future ticket wants an explicit operator view, it can extend the
  existing status payload — Path A doesn't lock anything out.
- **Should we add a metric for activate-on-send_message wait time?**
  Defer — no metrics infrastructure exists yet; the WARN log on
  Activate timeout is enough for the current observability bar.

## Scope self-check

Production source files prescribed:
1. `internal/supervisor/supervisor.go` — modified (~25 lines)
2. `internal/sessions/session.go` — modified (~10 lines: Activate
   contract, idle-eviction log)
3. `internal/relay/handlers/send_message.go` — modified (~15 lines)

Total: 3 production files, ~50 production-line diff. Edit fan-out:
`TurnWriter` has one production consumer (`cmd/pyry/relay.go:139`)
which continues to compile without change since `*sessions.Session`
already has `Activate(ctx context.Context) error`. Within S budget.
