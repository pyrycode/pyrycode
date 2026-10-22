# Inbound dequeue_message (#723) — `QueueRemover` seam → `msgqueue.Remove`

`dequeue_message` is a v2 **control** envelope (phone → binary, payload
`{conversation_id, queued_msg_id, accepted_item_id?}`), intercepted in
`dispatchAppFrame`'s
discriminator switch **before** `dispatch.Route` (the `interrupt` / `modal_cancel` /
`request_snapshot` boundary) — there is **no** `dispatch.Route` handler. It lets a
phone with an interactive session **cancel a queued `send_message` before it drains**
to claude: the daemon removes the named, not-yet-drained message from the live
`msgqueue` backlog. Apps observe accepted-item thread updates after explicit
catch-up. Outbound `queue_state` is retired for every connection (#3727).
**`security-sensitive`**: an inbound handler mutating daemon queue state from an
untrusted phone frame (mirror of `send_message`), and the **second** inbound frame
whose authorization *is* the `interactive` capability (after #707; spec-stage
security review, verdict PASS). See [`codebase/723.md`](../codebase/723.md).

- **`QueueRemover` consumer seam.** The relay declares the one-method interface
  `QueueRemover interface{ Remove(conversationID string, queuedMsgID uint64) bool }`
  (beside `Interrupter` / `ModalResolver`) and reaches the daemon-owned queue through
  it, so `internal/relay` imports neither `internal/msgqueue` nor `cmd/pyry`. The
  concrete `*msgqueue.Queue` satisfies it via `Remove` (#719), with additive
  `RemoveItem` support for bound requests (#3340). The optional nil-safe
  `V2SessionConfig.QueueRemover` field is
  wired in `cmd/pyry/relay.go`'s `startRelayV2` by widening the `queue` param to the
  concrete `*msgqueue.Queue` (which already flowed in as a `handlers.Enqueuer`) and
  adding one line, `QueueRemover: queue`.
- **`handleDequeueMessage(s, env)`** — the only new logic. Runs on the manager's
  **single Run dispatch goroutine**, so the `s.interactive` read is lock-free under
  the package's single-owner invariant. The signature takes **`(s, env)` — no `ctx`**
  — the documented `handleInterrupt` deviation from the `(ctx, s, env)` siblings:
  `Remove` takes no context and the handler sends no `forwardEnvelope`;
  lifecycle/history observers own accepted-item convergence. Order is **load-bearing — capability gate first**:
  1. **`if !s.interactive` → return** (no `Remove`, no mutation, no panic). The
     inbound capability gate (AC-3 negative path). `s.interactive` is
     server-authoritative (#626) — never the phone's raw advertisement. Still a
     **one-line check, NOT an abstraction** — a second consumer of the bare
     `s.interactive` check (after `interrupt`) does not justify a shared inbound-gate
     helper (CODING-STYLE over-DRY).
  2. **`if m.cfg.QueueRemover == nil`** → debug-log `v2.dequeue.inert`, return
     (foreground / pre-wire; mirrors `handleInterrupt`'s nil-`Interrupter` guard).
  3. **Reject every payload decode error**, then validate `accepted_item_id` with
     `queueItemBinding`. `json.Unmarshal` can retain valid target fields before
     returning a type error, including through duplicate keys; treating failure
     as a zero-value target can therefore remove a real message. Malformed
     payloads and present-invalid bindings return before actions or logging.
     Raw payloads, supplied bindings and decode errors are never echoed or logged.
  4. **Choose the operation by binding presence.** Valid omission calls
     `QueueRemover.Remove`; a supplied JSON integer in `1..9007199254740991` calls
     the additive `RemoveItem(string, uint64, uint64) bool` capability. A remover
     without that capability never falls back to `Remove`. The untrusted
     `conversation_id` is passed verbatim and scopes the live queue lookup;
     `accepted_item_id`, from accepted thread `item.id`, guards that conversation
     and queue handle against the daemon's current-run acceptance. Missing,
     unfinished, failed, mismatched or retired bindings are silent no-ops, with
     no mutation, cancellation, observers or wire output. Lookup neither waits
     for acceptance nor creates history references. See
     [atomic queue binding](msgqueue-package-api.md#exported-surface).
  5. **Log only content-free discriminants** (`removed` → INFO `v2.dequeue.removed`,
     `!removed` → DEBUG `v2.dequeue.noop`; only `conn_id` / `conversation_id` /
     `queued_msg_id`, never `env.Payload`). **No reply, no broadcast.**

**`false` is success of a valid request, not an error (AC-2):** unknown-id,
already-delivered, and the committing head are treated identically — a
quiet no-op, never an error reply (`msgqueue.Remove` already encodes all four cases).
**Convergence follows lifecycle facts:** the handler emits nothing; successful
removal retains optional `OnChange` notification and the terminal observers
that feed accepted-item thread updates. No queue snapshot is published. **No `KnownConversation`/`Route` membership gate** —
`dequeue_message` only mutates the named conversation's own existing backlog and
returns nothing, so the deterministic `convID`-scoping is self-validating; adding a
stochastic membership check would defend an unobserved failure mode and risk a
false-negative drop of a legitimate dequeue (Belt-and-Suspenders → different fabric;
the deliberate, reviewed choice in [`codebase/723.md`](../codebase/723.md) § Security).

**Twin: `send_queued_now` (#2729)** writes the named message into the running
turn instead of dropping it, mirroring every posture above (interactive gate
first, nil-seam inert, decode-error rejection, optional binding, content-free
logs, no reply) through a `QueueSender` seam declared beside `QueueRemover` — but it runs on the
conn's `appFrameWorker` rather than inline on `Run`, because the write ends
in an undeadlined stdin pipe write. See
[Inbound `send_queued_now`](v2-session-manager-state-machine-inbound-send-queued-now-queuesender-sea.md).
