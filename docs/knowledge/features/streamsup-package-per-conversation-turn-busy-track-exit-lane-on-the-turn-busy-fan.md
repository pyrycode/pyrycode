# Exit lane on the turn-busy fan-in (#1209)

`streamsup.Config.OnChildExit` fires after `spawnAndWait` has joined stdout
through `cmd.Wait`. `Parser.emit` calls its sink synchronously, so the callback
follows every parsed event offered by that child. It is a producer boundary,
not a publication barrier: the single drain may still have a queued tail or
buffered delta to publish. Exits normally ride `streamTurnSink.ch` behind that
tail. Delivering a close on an independent select lane could let it overtake
openers and leave busy state reopened after the close was spent.

## Retained boundaries when the queue is full

The closing reserve narrows loss but cannot guarantee capacity: closing events
can fill all 256 slots too. Losing both a child exit and confirmed runner stop
would strand the eviction hold, accepted posts and successor turns. Since
\#2811, `streamTurnSink.offer` retains a full-queue exit in `stopped`;
`runnerStopped` always retains the confirmed boundary supplied by
[`sessions.Config.OnRunnerStopped`](sessions-package-key-types-transition-observer.md#confirmed-runner-stop-configonrunnerstopped).
Neither callback waits for queue capacity.

A short leaf `offerMu` serializes successful enqueues and stop retention.
`queued` advances only for a successfully enqueued envelope; each retained stop
records the last such position in `confirmedStreamStop.after`. The sole drain
tracks processed positions, applies ready stops after those predecessors have
been published, and checks them before handling later queued output. Queue
emptiness cannot prove that barrier, and receiving a wake token cannot replace
it. `stoppedWake` only coalesces wakeups; the map owns the notifications.

Retention keeps the newest exit stamp per daemon-owned session identity.
Repeated stops move the boundary toward the later producer join; an older child
exit arriving at the lock later cannot overwrite it. State growth follows
session identities, not child-authored event volume. No tracker calls, history
I/O or broadcaster I/O run under the sink lock. Full-queue child exits retain
the existing content-free `stream_turn.exit_sink_full` Warn with `session_id`
only. This retention applies to exits/stops, **not** `TurnEnd`, which remains
subject to closing-class drops after the reserve is exhausted. See
[class-aware capacity](streamsup-package-per-conversation-turn-busy-track-class-aware-fan-in-reserve.md).

## Exit epochs protect marks outside the fan-in

FIFO positions cover marks placed by the drain, but `openForDelivery` places a
busy mark directly from the inbound/reset delivery goroutine. An old exit
already queued or retained could be consumed after that newer mark.
`exitForTag` stamps exits before offering them; `runnerStopped` stamps confirmed
stops too. `clearForExit` rejects a boundary whose epoch is at or before the
mark's captured exit-lane position. A retained stale stop therefore cannot
clear a successor any more than a queued stale exit can. See
[delivery-seam epoch protection](streamsup-package-per-conversation-turn-busy-track-delivery-seam-consumer-mid-turn-hold.md).

A post-bound tracker additionally rejects exits at or before a pending eviction
hold's captured epoch, even if completion cleared busy membership. An accepted
boundary flushes and closes that conversation's emitter lifecycle before
releasing post activity; compare-and-delete retires only the hold it accepted.
See [confirmed teardown](streamsup-package-per-conversation-turn-busy-track-session-teardown-clear.md).

## Routing and single-writer discipline

`streamTurnEnvelope.exit` explicitly discriminates a close from an event;
`ev` is unset on exits. A nil event sentinel would be silently classified as
`unknown` by `eventKind` and would make a non-event a value accepted by `Handle`.
The drain handles exits before event observation or emission, using the same
session-to-conversation resolver as `clearForSession`. Unresolvable boundaries
change nothing. Background conversations receive their own closes regardless
of the active cursor.

`newStreamRunnerFactory` binds `OnChildExit` through `exitForTag` and the parser
through `sinkForTag`, both reading the same live `streamSessionTag`. Rotation
cannot leave their tags on different sessions. `runSupervisor` separately binds
confirmed `Runner.Run` return to `runnerStopped`. All exit handling and emitter
calls remain inline on the one drain goroutine; no asynchronous clear can
reorder closure with later openers.

## Tests that exercise the ordering

Bind a drain fixture's tracker to **its own** sink's exit epoch source. Earlier
exit tests without that binding stayed green with or without the stale-exit
guard, so they did not prove it was armed. For a negative ordering assertion,
barrier on a later envelope rather than the absence of an effect.

`TestChannelDelivery_ConfirmedStopSurvivesSaturation` exhausts closing capacity,
checks callback return and eventual post release, and rejects stale stops behind
newer activity. `TestChannelDelivery_RetainedStopBeforeLaterProducerOutput`
proves a retained stop follows queued predecessors but precedes later output;
merely adding another select lane would fail that ordering.
