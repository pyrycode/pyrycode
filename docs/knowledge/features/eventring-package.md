# Event-ring retirement

The daemon no longer retains an event ring or assigns ring-derived event IDs.
Durable source history and HistoryEntryID remain separate; content recovery uses
explicit per-conversation thread catch-up/pages/reset.

Event replay admission and delivery were removed in #3738; storage, configuration
and API wiring were removed in #3739. This page keeps the former package's path
so existing links resolve. Historical ring designs describe a removed component.
[Integrated acceptance](v2-session-manager-state-machine-reconnect-replay-hello-last-event-id-rin.md#integrated-acceptance)
records #3719's verdict and retained dispositions.

## Where the durable id is assigned (the load-bearing point)

`appendConversationHistory` obtains `HistoryEntryID` from a successful source
append before supported live-state fan-out. `publishLiveState` carries that same
history ID, payload and logical timestamp to each interactive recipient while
advancing the emitter's envelope counter per recipient. It assigns no `EventID`.
Source recording remains independent of recipient count; nil or failed storage
omits `history_entry_id` and cannot restore retired content publication.
`TestLiveProducers_HistoryEntryID` checks actual delivered envelopes against
stored records, including multiple recipients, no recipients and storage failure.

`Envelope.ID` serves connection delivery/correlation; `HistoryEntryID` names a
durable entry in one conversation. Generic optional `Envelope.EventID` remains
in the schema and retained validation/size/projection contracts, without a
production ring source. Neither envelope ID is a durable read-mark target.
See [envelope metadata](protocol-package-types-envelope.md#replay-cursors-and-durable-read-marks)
and [history producers](history-package-producers-legacy-compatibility.md).

## Ownership & wiring (why the constructor signature is unchanged)

The emitter, `streamTurnSink` and `V2SessionManager` hold no ring or replay cursor.
`SetReplaySource`, ring arguments and ring-exclusive configuration are removed;
`newInteractiveTurnEmitterV2` keeps its existing signature. Live-state retention
and durable source recording remain independent owners.

`cleanupOnConversationDelete` composes suggestions/pending-inference cancellation,
stream/live removal and joined thread cleanup through the registry's single
`SetOnDelete` slot in both relay postures. There is no ring `Drop`. See
[registry removal](conversations-registry-crud.md#setondeletefn-funcid-conversationid-1502)
and [thread lifecycle](thread-package-shadow-lifecycle-and-evidence-coordination.md).

## Files

`internal/eventring/ring.go` and its obsolete tests are deleted. Current behavior
belongs to `publishLiveState`, `appendConversationHistory`,
`cleanupOnConversationDelete` and the relay's explicit thread query handlers.

## Related

- [Explicit reconnect recovery](v2-session-manager-state-machine-reconnect-replay-hello-last-event-id-rin.md): ignored obsolete hello cursors, thread catch-up/pages/reset and current-state reconciliation.
- [Conversation thread](thread-package.md): durable fold inputs and scoped recovery.
- [Model-list projection](v2-session-manager-state-machine-connect-time-model-list-reconcile-retain.md#projection-provenance): projection eligibility is independent of event IDs.
- [ADR 042](../decisions/042-daemon-built-thread.md): thread recovery replaces the transitional event replay path.
