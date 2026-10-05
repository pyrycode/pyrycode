# Session-teardown clear (#1202)

Pool lifecycle changes and published turn closure are separate boundaries.
`Session.beginEvict` announces `ReasonEviction` before cancelling and joining
the runner. Even an empty fan-in at that instant can receive the old producer's
late tail. Releasing a durable post there would record it before the preceding
Claude reply; a delayed conversation-only close could instead close a successor
turn. The production tracker bound to `channelDelivery` therefore keeps busy
membership and publication intact at early transitions.

## Eviction holds through confirmed producer stop

`startSessionTransitionStreamV2` composes the pool's single-valued
`TransitionObserver`: the existing transition emitter enqueues first, then
`transitionClearsTurn` resolves the session-bearing reason. Unknown reasons
change no busy state. For a post-bound tracker, `ReasonEviction` calls
`holdForTeardown`, which resolves the session daemon-side and records the current
exit-lane epoch in `channelDelivery.teardown`. This leaf `sync.Map` lets the pool
observer return without waiting for the consumer mutex's history/publication
I/O. Pending eviction gates posts, ordinary/reset reservations and send-now.
A real `TurnEnd` can publish normally but cannot retire the eviction hold.

Actual child exits follow their parsed output. The optional
[`sessions.Config.OnRunnerStopped`](sessions-package-key-types-transition-observer.md#confirmed-runner-stop-configonrunnerstopped)
also reports each completed `Runner.Run` before eviction completion/reactivation.
`runSupervisor` wires that confirmed stop to `streamTurnSink.runnerStopped`.
The extra notification matters when the last child exit was already offered
before the eviction request: there may be no later child exit to release it.

The single stream drain consumes the accepted boundary after preceding queued
events, calls `interactiveTurnEmitterV2.closeForConversation` to flush buffered
text and close that conversation's published lifecycle, then releases activity.
Exit stamps at or before the eviction epoch are rejected even if completion
has already cleared busy membership. `clearForExit` also preserves the newer
reservation's epoch guard. After publication, compare-and-delete removes only
the eviction hold that exit accepted; a new transition arriving during
publication cannot be cleared by the older boundary. No conversation-only
teardown close can retire a newer turn on this bound path.

Confirmed stops survive a full fan-in through retained state ordered by
successful enqueue positions, not queue emptiness or wake receipt. See
[exit transport](streamsup-package-per-conversation-turn-busy-track-exit-lane-on-the-turn-busy-fan.md).
Holding and closure work without a relay URL because `startRelay` still starts
the history-backed emitter and stream drain with `historyOnlyBroadcaster`.

## Clear transitions and the unbound tracker

An in-band `/clear` can rotate a session ID while retaining the live child.
The bound path's `clearForSession` is a no-op and `ReasonClear` installs no
eviction hold: publication waits for real completion or actual exit, rather
than requiring a runner stop that may never follow that reset.

On the legacy unbound path, `clearForSession` still clears synchronously and
requests a lifecycle close through the drain. It is nil-receiver-safe and
session-keyed; its injected resolver runs outside the tracker mutex. An
unresolved session logs `stream_turn.clear_unresolved` with `session_id` only
and changes nothing. Resolved clears share `setBusy`'s delete/broadcast protocol
and clear all retained tool calls with an empty `toolCallDelta`.
The drain remains the sole writer of emitter lifecycle state.

The session key comes from the pool lifecycle rather than the wire.
`transitionClearsTurn` uses the live ID supplied by `toWirePayload`; daemon-side
`conversationForSession` also checks session history, so a rotated tail still
belongs to its own conversation. A moving `streamSessionTag` keeps parser and
child-exit tags aligned; exit epochs protect reservations placed outside the
fan-in. See [session rotation](streamsup-package-session-rotation-notification-onsessionrotate.md)
and [delivery reservations](streamsup-package-per-conversation-turn-busy-track-delivery-seam-consumer-mid-turn-hold.md).

## Ordering tests

`TestChannelDelivery_TeardownCompletionGatesSuccessorUntilExit` holds late output
after the early transition, publishes completion, then tests stale exits and a
competing successor before confirmed stop. `TestChannelDelivery_ExitTeardownAndStaleExit`
checks buffered-text flush and newer-turn protection.
`TestChannelDelivery_ConfirmedStopSurvivesSaturation` fills the sink with closing
events and proves callbacks return, preceding tails publish, posts eventually
release and stale stops cannot clear successor activity.
