# Inbound dequeue_message (#723) — `QueueRemover` seam → `msgqueue.Remove`

`dequeue_message` is a v2 **control** envelope (phone → binary, payload
`{conversation_id, queued_msg_id}`), intercepted in `dispatchAppFrame`'s
discriminator switch **before** `dispatch.Route` (the `interrupt` / `modal_cancel` /
`request_snapshot` boundary) — there is **no** `dispatch.Route` handler. It lets a
phone with an interactive session **cancel a queued `send_message` before it drains**
to claude: the daemon removes the named, not-yet-drained message from the live
`msgqueue` backlog, and the existing change seam refreshes the phone's `queue_state`.
**`security-sensitive`**: an inbound handler mutating daemon queue state from an
untrusted phone frame (mirror of `send_message`), and the **second** inbound frame
whose authorization *is* the `interactive` capability (after #707; spec-stage
security review, verdict PASS). See [`codebase/723.md`](../codebase/723.md).

- **`QueueRemover` consumer seam.** The relay declares the one-method interface
  `QueueRemover interface{ Remove(conversationID string, queuedMsgID uint64) bool }`
  (beside `Interrupter` / `ModalResolver`) and reaches the daemon-owned queue through
  it, so `internal/relay` imports neither `internal/msgqueue` nor `cmd/pyry`. The
  concrete `*msgqueue.Queue` satisfies it via the existing `Remove` (#719) — **zero
  new msgqueue code**. The optional nil-safe `V2SessionConfig.QueueRemover` field is
  wired in `cmd/pyry/relay.go`'s `startRelayV2` by widening the `queue` param to the
  concrete `*msgqueue.Queue` (which already flowed in as a `handlers.Enqueuer`) and
  adding one line, `QueueRemover: queue`.
- **`handleDequeueMessage(s, env)`** — the only new logic. Runs on the manager's
  **single Run dispatch goroutine**, so the `s.interactive` read is lock-free under
  the package's single-owner invariant. The signature takes **`(s, env)` — no `ctx`**
  — the documented `handleInterrupt` deviation from the `(ctx, s, env)` siblings:
  `Remove` takes no context, there is no `forwardEnvelope`, and `queue_state`
  convergence is decoupled onto the #722 emitter goroutine, so the handler owns no
  cancellable work. Order is **load-bearing — capability gate first**:
  1. **`if !s.interactive` → return** (no `Remove`, no mutation, no panic). The
     inbound capability gate (AC-3 negative path). `s.interactive` is
     server-authoritative (#626) — never the phone's raw advertisement. Still a
     **one-line check, NOT an abstraction** — a second consumer of the bare
     `s.interactive` check (after `interrupt`) does not justify a shared inbound-gate
     helper (CODING-STYLE over-DRY).
  2. **`if m.cfg.QueueRemover == nil`** → debug-log `v2.dequeue.inert`, return
     (foreground / pre-wire; mirrors `handleInterrupt`'s nil-`Interrupter` guard).
  3. **Decode tolerantly** (`_ = json.Unmarshal(env.Payload, &p)`): a decode failure
     leaves zero-value fields, which `Remove("", 0)` no-ops on. The decode error and
     the payload bytes are **never** echoed back to the phone or into a log (the
     never-echo discipline of `handleRequestSnapshot` / `handleModalCancel`).
  4. **`removed := m.cfg.QueueRemover.Remove(p.ConversationID, p.QueuedMsgID)`** — the
     one-line engine call. The untrusted `conversation_id` is passed **verbatim**;
     `Remove`'s `convID`-scoping is the security boundary (`Remove(A,…)` provably
     never touches conversation B's backlog).
  5. **Log only content-free discriminants** (`removed` → INFO `v2.dequeue.removed`,
     `!removed` → DEBUG `v2.dequeue.noop`; only `conn_id` / `conversation_id` /
     `queued_msg_id`, never `env.Payload`). **No reply, no broadcast.**

**`false` is success of a valid request, not an error (AC-2):** unknown-id,
already-delivered, and the in-flight (draining) head are treated identically — a
quiet no-op, never an error reply (`msgqueue.Remove` already encodes all four cases).
**AC-4 convergence is automatic, never re-emitted:** the handler emits nothing;
`Remove`'s `notify` fires the `OnChange` seam on a successful removal, and the #722
`queueStateEmitterV2` (the **sole** `queue_state` emitter) pushes the updated snapshot
to interactive phones. **No `KnownConversation`/`Route` membership gate** —
`dequeue_message` only mutates the named conversation's own existing backlog and
returns nothing, so the deterministic `convID`-scoping is self-validating; adding a
stochastic membership check would defend an unobserved failure mode and risk a
false-negative drop of a legitimate dequeue (Belt-and-Suspenders → different fabric;
the deliberate, reviewed choice in [`codebase/723.md`](../codebase/723.md) § Security).
