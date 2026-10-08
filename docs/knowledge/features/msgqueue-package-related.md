# Message queue integrations and related documentation

Part of [`internal/msgqueue`](msgqueue-package.md).

## Related

- [codebase/704.md](../codebase/704.md) — engine ticket record (patterns + lessons).
- [codebase/719.md](../codebase/719.md) — introspection / remove / change-notify
  ticket record (the in-flight-head no-op rule, fire-after-unlock).
- [features/eventring-package.md](eventring-package.md) — the **outbound** sibling
  this mirrors (per-conversation in-memory store, daemon-resident, same restart
  boundary).
- `internal/supervisor` `WriteUserTurn` — the #594 reliable-delivery path whose
  shape `DeliverFunc` mirrors and whose `WaitReady` block *is* the drain's pacing.
- [features/turnbridge-package.md](turnbridge-package.md) — the "shipped unwired,
  injected-function-seam, `Config` + `New` + `Run`" template this engine follows.
- [ADR 025](../decisions/025-mobile-remote-head-interactive-session.md) — § wire
  protocol (`send_message` queued-by-daemon, the `{queued_msg_id, message_id, text, ts}`
  record — `message_id` added by #2092).
- [ADR 042](../decisions/042-daemon-built-thread.md) — memory-only queue facts
  and durable acceptance linkage; see the [identity/lifecycle API](msgqueue-package-api.md#exported-surface)
  and [confirmation arbitration](msgqueue-package-lifecycle.md#concurrency-model).
- [codebase/721.md](../codebase/721.md) — the **live wiring** ticket record: the
  `cmd/pyry` constructor, `newInboundDeliver` delivery seam, the `Route`/`resolve`
  split (the drain re-resolves without stamping the #687 cursor), and the
  enqueue-and-ack contract change in `send_message`.
- **Consumers:** **#722 landed the `queue_state` producer** ([codebase/722.md](../codebase/722.md))
  — `cmd/pyry`'s `queueStateEmitterV2` hooks `OnChange`, re-reads via `Snapshot`, and
  fans a per-conversation `queue_state` to interactive phones (the first live consumer
  of `OnChange` + the read side of `Snapshot`). **#723 landed the `dequeue_message`
  handler** ([codebase/723.md](../codebase/723.md)) — `internal/relay`'s
  `handleDequeueMessage` (via the consumer-declared `QueueRemover` seam) calls `Remove`
  on an interactive phone's inbound control frame, and its successful-removal `notify`
  drives the #722 producer to refresh `queue_state` (the **first live consumer of
  `Remove`**; the in-flight-head/unknown-id `false` no-op is what makes a hostile or
  stale id a safe no-op). Both #722/#723 split from the original #705 reporting/removal
  slice. **[#869](../codebase/869.md) landed the deferred inbound bound**: a
  per-conversation `MaxQueuedPerConversation` cap (default 100) at `Enqueue`,
  reject-never-drop, mapped by `send_message` to a retryable `server.binary_busy`
  reply. **[#878](../codebase/878.md) landed the connect-time reconcile enumeration
  seam**: `SnapshotAll` (every non-empty conversation's backlog in one lock hold),
  consumed by `cmd/pyry`'s `outstandingQueues` adapter and, through it,
  `internal/relay`'s `OutstandingQueues` seam — see
  [`v2-session-manager.md` § Connect-time queue reconcile](v2-session-manager.md#connect-time-queue-reconcile-878--outstandingqueues-seam--reconcilequeues).
  **[#1000](../codebase/1000.md) landed the persistent-failure give-up bound**: a
  per-head elapsed deadline (`GiveUpAfter`, default 2m) that abandons a
  persistently-failing head instead of retrying it forever, plus an `OnGiveUp`
  seam mirroring `OnChange` — split from #991 as the not-security-sensitive
  engine-side half; shipped unwired (nil) until the wire+producer sibling (#1001,
  itself re-split at its vocab seam into wire vocabulary #1007 — shipped,
  unwired — and producer+wiring #1008, security-sensitive, blocked-by-#1007)
  landed. The legacy give-up producer is live; optional lifecycle identity
  adoption and durable recording remain with #2971/#2972.
- **[#1199](../codebase/1199.md) closed a `DeliverFunc`-contract gap on the stream-json runner,
  engine-side unchanged.** The package's contract — `DeliverFunc` "MUST block while claude is busy …
  that blocking IS the drain's turn-end pacing" (`DeliverFunc`) — held for the PTY delivery seam
  (`supervisor.WriteUserTurn` gates on `waitReadyAutoContinue`) but not for stream-json:
  `streamsup.Runner.WriteUserTurn` returns as soon as the envelope is in the child's stdin pipe, so the
  drain emptied as fast as it could write and the queued-backlog UI / drop-before-drain control never
  appeared on that runner. `newInboundDeliver` now waits on `cmd/pyry`'s per-conversation
  `turnBusyTracker` (`waitIdleForDelivery`, bounded by `streamTurnHoldTimeout`) and marks the
  conversation busy (`openForDelivery`) both between `Activate` and the write — placed *before*
  `WriteTurn`'s `turncommit` claim, which is what keeps the head `draining && !committing`, i.e.
  droppable, for the whole wait. `Remove`, `commitGate`, and the give-up bound above are consumed
  exactly as they stand; nothing in this package changed. On PTY the tracker is nil and both calls are
  no-ops — `newInboundDeliver`'s body is semantically unchanged there.
- **#1911 gave `Config.Pending` its first live producer since #1348 removed
  `supervisor.ErrTrustModalPending`'s.** A head held by #1199's stream-path hold is exempted from
  `GiveUpAfter` for as long as a person is being asked to approve something on that conversation
  (`streamApprovalBridge.ApprovalParked`, #1919) — see [Bounded give-up](msgqueue-package-give-up.md)
  for the gating pattern. Engine-side unchanged apart from the doc comments on `PendingFunc` and `Config.Pending`,
  which no longer name the deleted trust-modal producer as the wiring.
- **#2115 added `OnDelivered`, the engine's fourth seam and the first
  consumed outside `cmd/pyry`'s wire producers.** `cmd/pyry`'s `newOperatorMessageHistory` hooks it
  to write the operator's own typed message into [`internal/history`](history-package.md)'s durable
  conversation log — the first consumer for which `newInboundDeliver` (the delivery seam itself)
  could not have worked from its payload argument, which contains composed `delivery` rather than
  `text`. `DeliveryMessage` now supplies the safe projection there for stream placement. See
  [Delivered notification](msgqueue-package-lifecycle.md#delivered-notification-2115) and
  [history-package.md § Producers](history-package.md#producers-2114-2115).
  **#2699 added a second `OnDelivered` consumer on the same call, `operatorMessageEmitterV2`, needing
  no new engine guarantee.** The msgqueue tests already pinned `OnDelivered`'s once-per-delivery,
  never-on-give-up, never-on-removed-head semantics (`TestQueue_OnDelivered_FiresOnceAcrossRetries`,
  `…_SilentOnGiveUp`, `…_SilentWhenHeadRemovedBeforeCommit`) before this ticket existed; a producer
  that only ever runs from that seam inherits them for free. The `cmd/pyry`-level test this ticket
  added therefore covers just the shape specific to the new producer — retry-then-deliver pushed
  exactly once, carrying the #2038 host-path-free payload — not the once/never cases again.
- **#2729 added `SendNow`, the engine's fifth op and the second that mutates the backlog out of order
  (after `Remove`).** It writes a queued message into the conversation's running turn rather than its
  backlog slot, reusing `Remove`'s take-the-waiting-head-out-of-the-FIFO argument instead of a new one,
  and added `QueuedMessage.SentNow` so `channelCarry.clearDelivered` ([control-plane.md § Carrying a
  posted channel message into claude's next
  turn](control-plane-channel-post-carry.md#carrying-a-posted-channel-message-into-claudes-next-turn-2499)) can skip a
  delivery it did not compose. See [Send-now delivery](msgqueue-package-send-now.md) and
  [v2-session-manager-state-machine-inbound-send-queued-now-queuesender-sea.md](v2-session-manager-state-machine-inbound-send-queued-now-queuesender-sea.md)
  for the relay-side handler.
