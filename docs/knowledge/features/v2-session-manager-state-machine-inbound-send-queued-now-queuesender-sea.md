# Inbound `send_queued_now` (#2729) — `QueueSender` seam → `msgqueue.SendNow`

`send_queued_now` is `dequeue_message`'s twin in authority and shape — a v2
**control** envelope (phone → binary, payload `{conversation_id,
queued_msg_id, accepted_item_id?}`), ungated beyond paired authentication and the
`interactive` capability, no reply. Connections without `thread` observe the
`queue_state` change and live delivered `message`; thread connections observe
accepted-item updates, with unsolicited and connect-time `queue_state` withheld.
It writes the named queued message
into the conversation's **running** turn instead of dropping it. See
[`docs/protocol-mobile.md` § `send_queued_now`](../../protocol-mobile.md#send_queued_now)
for the wire contract and
[msgqueue-package-send-now.md](msgqueue-package-send-now.md) for the engine op
it calls. See [`codebase/723.md`](../codebase/723.md) for `dequeue_message`,
the handler this one mirrors.

- **`QueueSender` consumer seam**, declared beside `QueueRemover`:
  `QueueSender interface{ SendNow(conversationID string, queuedMsgID uint64)
  bool }`. `*msgqueue.Queue` satisfies both, so `internal/relay` still imports
  neither `internal/msgqueue` nor `cmd/pyry`. Wired in `cmd/pyry/relay.go`'s
  `startRelayV2` through `suggestionQueueSender`, which forwards `SendNowItem`
  as well as legacy `SendNow` and invalidates suggestions only on success.
  `QueueRemover` still receives the queue directly. A decorator must forward
  the bound capability: relay never falls back to unbound sending when it is absent.
- **`handleSendQueuedNow(s, plaintext)`** decodes the envelope itself (unlike
  `handleDequeueMessage`, which receives an already-decoded `env` — see §
  Runs off `Run` below for why), then decodes `protocol.SendQueuedNowPayload`.
  Every envelope/payload decode error returns before action or logging: failed
  decoding can leave valid target fields populated, so tolerating it could send
  a real message. `queueItemBinding` distinguishes valid omission (legacy
  `SendNow`) from a supplied JSON integer in `1..9007199254740991` (additive
  `SendNowItem(string, uint64, uint64) bool` capability). Present-invalid bindings
  and missing bound capability never fall back. Current-run acceptance matching
  follows [Drop's guard](v2-session-manager-state-machine-inbound-dequeue-message-queueremover-sea.md):
  no waiting or historical lookup, and no mutation, write, cancellation, observers
  or wire output on missing/pending/failed/mismatched/retired bindings. `true` →
  `Info "v2.send_now.delivered"`; `false` → `Debug "v2.send_now.noop"`. Both
  log only `conn_id`/`conversation_id`/`queued_msg_id` — the `dequeue_message`
  field set exactly, never supplied bindings, raw payloads, queued text or the
  client's message id. A nil `QueueSender` is `Debug "v2.send_now.inert"` and
  returns before decoding anything.
- **`false` is success of a valid request, not an error** — identical posture
  to `Remove`'s `false`: the turn is idle (the ordinary drain will deliver it
  in FIFO order), the id is unknown or already delivered, the head is
  `committing`, the session is Codex (whose write path starts a new turn
  rather than feeding the running one), or the write itself failed and
  `msgqueue` put the message back where it was. None of these is
  distinguished on the wire or in the log. A refusal produces no state change
  to observe, exactly as for `dequeue_message`.

## Runs off `Run`, on the conn's `appFrameWorker` — the one structural departure from `dequeue_message` (security review, 2026-10-03)

`dequeue_message`'s handler runs inline on the manager's single `Run`
dispatch goroutine because `msgqueue.Remove` only ever touches in-memory
state. `send_queued_now` cannot: a successful `SendNow` ends in
`streamsup.WriteTurn`, a plain stdin pipe write with **no deadline**. Running
that on `Run` would let one conversation's wedged child stall `send_now`
delivery — and, transitively, every other control frame this connection's
`Run` goroutine would otherwise dispatch — for as long as the pipe write
blocks.

The fix, from the ticket's security review: `dispatchAppFrame`'s discriminator
switch routes `TypeSendQueuedNow` to `appFrameSendQueuedNow`, a job enqueued
onto the connection's own bounded `appFrameWorker` queue (the same per-conn
worker `attachment_chunk` already uses for its own off-`Run` work) rather than
handled inline. A wedged child now stalls only that one connection's
app-frame jobs, never the manager's shared `Run` goroutine or another
connection's frames.

**The `interactive` capability gate stays on `Run`.** `dispatchAppFrame`
checks `s.interactive` — Run-owned, read lock-free under the single-owner
invariant — **before** the job is queued, so a non-interactive connection's
frame never reaches the worker at all. `handleSendQueuedNow` itself does not
read `s.interactive`: it would be reading Run-owned state from the wrong
goroutine, which is exactly the hazard moving the handler off `Run` would
otherwise reopen. This is the reason the handler decodes the raw `plaintext`
rather than receiving a pre-decoded `protocol.Envelope` the way
`handleDequeueMessage` does — the outer `Envelope` decode has to happen on the
worker goroutine too, downstream of the gate that already ran on `Run`.

Pinned by `TestV2Session_SendQueuedNow_SendsByCapability` and
`TestV2Session_SendQueuedNow_NilSenderInert`
(`internal/relay/v2session_send_now_test.go`).

## `sessionRouter.isClaude` — the second security-review fix, ordering the busy check before resolve

`newSendNowDeliver` (`cmd/pyry/send_now.go`) is the `msgqueue.SendNowFunc`
this seam ultimately calls through. The security review's second finding: an
early draft resolved the conversation's writer before checking whether its
turn was even running, and `resolve` can revive a dormant session
(`sessionRouter.revive`) — so an idle conversation's send-now attempt would
silently respawn a session only to then refuse the write. The fix reorders
the two checks: `busy.openForSendNow(convID)` runs first, and `resolve` is
reached only once the tracker confirms the turn is actually running. See
[streamsup-package-per-conversation-turn-busy-track-send-now-carry.md](streamsup-package-per-conversation-turn-busy-track-send-now-carry.md)
for `openForSendNow` and the busy-tracker carry it threads through a turn
boundary.

`isClaude` (`sessionRouter.isClaude`) refuses before either of those: a
conversation bound to a Codex session always returns `errSendNowNotClaude`,
because Codex's write path starts a new turn rather than feeding the running
one. `settingsUpdaterAdapter.Capabilities`' `mid_turn_input` reports this
split since #2730 — true for Claude, still false for Codex.

`newSendNowDeliver` never calls `Activate`: a running turn already has a live
child by definition, and an absent one must fail the write (leaving the
message queued for the ordinary drain) rather than spawn a session for a
send-now that arrived too late. Pinned by
`TestSendNowDeliver_WritesOnlyIntoARunningClaudeTurn`
(`cmd/pyry/send_now_test.go`).

**Since #2730, `newSendNowDeliver` also registers the write with a
`*sendNowPlacement` before it happens** (`place.write` for queue-backed
attempts since #2820; `place.expect` for legacy callers without metadata),
so the operator-message history entry and live `message` push for this
delivery wait for claude's own echo of it and land where claude actually
read it, rather than at this write. See [history-package.md §
Producers](history-package-producers.md#producers-2114-2115) for the commit-deferral
mechanism and [streamsup-package-turn-io-envelope-write-stdout-parser.md](streamsup-package-turn-io-envelope-write-stdout-parser.md)
for the echo the spawn now produces.
