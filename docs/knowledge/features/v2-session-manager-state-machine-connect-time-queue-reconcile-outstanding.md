# Connect-time queue reconcile (#878) — retired

Outbound `queue_state` is retired for every app (#3727), on change and on
connect/reconnect, including peers without `thread`. `OutstandingQueues`,
`reconcileQueues`, the daemon's `outstandingQueues` adapter and exclusive
snapshot tests are removed. No connect-time thread publisher replaces them.

Apps negotiate `thread`, explicitly catch up each open conversation, and apply
live item updates by identity and revision. A queued active `user_message`
provides `content.queued_msg_id`; its `item.id` supplies the optional
`accepted_item_id` guard for dequeue/send-now. A delivered/dropped/lost item is
historical and cannot target a reused handle. See
[Queue (v2)](../../protocol-mobile.md#queue-v2) and
[query/reconnect recovery](../../protocol-mobile.md#reconnect--backfill-semantics).

The in-memory FIFO, `Snapshot`/`SnapshotAll`, optional `OnChange`, queue controls
and lifecycle/acceptance binding remain. Removing an outbound projection must
not remove these source and mutation seams; see
[queue API](msgqueue-package-api.md#introspection-removal-and-change-notification-719).
`TestQueueSnapshotsRetired` guards against restoring the wire vocabulary or
either producer, regardless of capability. The former reconcile's historical
design remains in [#878's record](../codebase/878.md).
