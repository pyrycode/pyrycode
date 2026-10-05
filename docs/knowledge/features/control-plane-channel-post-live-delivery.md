# Fanning `channel.post` out: `assistant_delta` live delivery (#2498)

A successful `channel.post` durably accepts the entire message before returning.
`channelPoster` resolves the channel and mints its turn identity;
`channelDelivery.accept` atomically saves the whole text, conversation ID, turn ID
and acceptance timestamp in the instance's private `channel-delivery.json`.
Its sole daemon-owned consumer chunks the post, records every chunk in the
composition root's existing `conversationHistory`, then announces the same
payloads through `channelPostEmitterV2`. Idle delivery starts immediately on
acceptance without waiting for a client, relay configuration or user turn.

`channelPostEmitterV2` retains `conversationUpdateEmitterV2`'s fan-out shape:
a leaf mutex over its envelope-ID counter, the #607 interactive-capability gate,
and a `Push` loop that logs and skips a torn-down connection. `startRelayV2` returns
its announce hook through `startRelay`; `runSupervisor` installs it on
`channelDelivery` before starting the consumer independently of the relay leg.
The hook is nil without a relay, and its result is never consulted. A fan-out
failure cannot turn durable acceptance into a refusal.

`assistant_delta` is otherwise produced only by a supervised claude turn (`interactiveTurnEmitterV2`); `channelPostEmitterV2` is its second producer and the first that is not one. The two must stay distinguishable at the daemon boundary even though the wire frame is identical, which is why `docs/protocol-mobile.md`'s v2 type table records both.

## A fresh `turn_id` per post

`newChannelPostTurnID` mints one per post from `conversations.NewID`, using
`crypto/rand`. Acceptance preserves that ID together with the conversation and
whole text through retry and process restart. A failed acceptance returns the
static `msgChannelPostRecordFailed`, publishes no pending work, appends or
announces no chunks, and records no carry; previously accepted posts stay intact.
A later history failure leaves the accepted post pending for automatic retry
without another post or restart.

Concurrent posts to one conversation deliver FIFO by durable acceptance, with
each post's chunks contiguous relative to other posts and Claude output. Each
has its own turn ID, sequences from zero, and text whose concatenation reproduces the input
byte-for-byte. All chunks reach history before the first live announcement;
announcements follow their recorded post/chunk order. A failed head keeps later
posts in that conversation pending while other conversations can progress.

An active Claude turn holds delivery, not acceptance: `channel post` returns
success after persisting the whole post without waiting for that turn to finish.
No held chunk enters served history or live frames. Release follows the preceding
turn's published real completion, or an accepted actual child exit/confirmed
runner stop after preceding queued deltas have been handled, buffered text
flushed and the published lifecycle closed. An early pool eviction request is
not that boundary; it holds posts and successor starts through confirmed stop,
even if completion publishes in the meantime. Stale exits cannot close a newer
turn. See [session teardown](streamsup-package-per-conversation-turn-busy-track-session-teardown-clear.md).

Posts accepted by the release boundary finish whole-post delivery before an
ordinary successor user turn or reset wrap-up can start. `beginDelivery` makes
the final pending check atomic with the busy mark and write reservation under
the same consumer mutex as acceptance, recording and announcement. Child writes
run outside that mutex; reservations keep posts held until writes return,
including in-flight send-now writes after a completion or carry/grace close.
A pending history retry blocks successor starts even after a recorded prefix.
Checking only an inbound snapshot or tracker-idle state would let a later
acceptance or unpublished completion split the reply or post.

The served text remains `assistant_delta` alone, with no `turn_state` or
`turn_end`. A lone delta renders live or from a history page, and its fresh turn
ID starts a fresh bubble. `TurnEndPayload` would claim Claude-authored results
that a host post cannot supply. Completion, reconnect replay and wake belong to
[#2809](https://github.com/pyrycode/pyrycode/issues/2809). Holding preserves the
original Claude reply and its real completion; it supplies no post lifecycle.

**The id's failure contract follows what it addresses, not what the old code called it.** `newChannelPostMessageID` fell back to `""` on an rng failure, correctly — a dedupe key nobody reads degrades harmlessly. `newChannelPostTurnID` refuses the post instead: a `turn_id` is the address a client groups chunks by, and every post minting `""` would coalesce into a single bubble with every other one, breaking the "one post is one rendering" property outright rather than degrading a field nobody reads.

## Chunking and envelope bounds

`MaxChannelPostBytes` (64 KiB) and the v2 application envelope's cap
(`maxV2AppEnvelope`, 65519 B) are unrelated bounds. A producer that marshals one
straight into the other passes every short-fixture test and fails on a maximum
message: JSON escaping can cost six bytes per input byte. `channelDelivery.deliver`
owns `splitDeltaText(text, maxDeltaTextBytes)`, the same splitter `flushDelta`
uses, and constructs each payload once for both history and announcement.
`TestChannelPoster_MaximumPostFitsEnvelopeCap` measures a maximum-size,
`<`-filled post against the envelope cap and checks its rejoined bytes. If it
fails, lower `maxDeltaTextBytes` rather than relaxing the measurement.

## Live-only announcements and durable recovery

Live announcements have no connect-time replay or exactly-once network receipt
guarantee. Private pending delivery makes acceptance durable; the conversation
log makes missed frames readable through the existing history delta-rendering
path. `assistant_delta` remains droppable in `pushQueue.enqueue` and
`convRing.evictOldest`. `EventID` stays unset because this emitter owns no
`eventring`. History recovery does not imply automatic tail catch-up, which
belongs to [#2744](https://github.com/pyrycode/pyrycode/issues/2744).

The hold diagnostic deadline is **five minutes from durable acceptance**
(`channelPostHoldDeadline`). Crossing it while held emits
`channel_post.hold_deadline` once per post per process, containing only fixed
event text, conversation ID and turn ID. The post stays pending. Expiry never
delivers, interrupts, marks idle, abandons or ends a still-live Claude turn.
It is a diagnostic threshold, not a delivery timeout.

`history.Store.Append` is per-chunk, so a recorded prefix can be visible while a
later write retries. `channelDelivery.deliver` scans **every** newest-first
`Page`, matching the conversation, turn ID, sequence and text, then appends only
missing chunks in ascending sequence. Looking only at the newest page would
duplicate a prefix hidden by unrelated newer entries.

After process restart, undelivered or partially recorded posts resume with their
original identities and chunk order, yielding exactly one history delta per
chunk. A fully recorded post left pending by interrupted cleanup is removed
without another append or announcement, even if the process died before its
first live push. In-memory delivered markers also prevent repeated announcements
when cleanup persistence fails. Durability matches history's process-restart
contract; it does not promise survival of a machine crash.

`runSupervisor` claims the control socket before loading pending state and
establishes `channelDelivery.beforeInbound` before inbound delivery starts.
Recovery starts without live activity marks, at a safe startup boundary.
Recovered posts finish history delivery before a new user turn is delivered in
the same conversation, ahead of carry composition and user-turn writing;
`beginDelivery` rechecks pending work under the reservation gate so concurrent
acceptance cannot overtake that check.
Unaffected conversations can progress. Socket ownership lasts through post
writer shutdown; see [Control plane lifecycle](control-plane.md#lifecycle).

Holding, release and startup recovery work without any attached client or relay
URL. In that configuration `startRelay` still runs the stream drain with a
history-backed interactive emitter and `historyOnlyBroadcaster`; completion and
confirmed stop close publication into history before posts can be recorded.

## Pending delivery and channel carry

Private client delivery and [`channelCarry`](control-plane.md#carrying-a-posted-channel-message-into-claudes-next-turn-2499)
are independent. Newly accepted posts record carry once, after acceptance.
Carrying or clearing text for Claude cannot consume pending client delivery,
and client-delivery cleanup cannot consume carry. Retry and reload never call
`channelCarry.record`: doing so could re-add text already cleared for Claude.
Carry keeps its existing best-effort persistence and growth bounds, including
the possible crash window between acceptance and carry recording.

Refusals and logs omit posted text, raw storage errors and content-derived chunk
counts or sequences. Acceptance, delivery-retry and cleanup logs use fixed
events and identity fields.

`TestChannelDelivery_ReloadInterruptions` places recorded chunks behind newer
history pages and checks undelivered, partial and fully recorded recovery.
`TestChannelDelivery_RetryAndCarryIndependence` checks both directions of carry
independence and automatic retry with and without an announcer;
`TestChannelDelivery_ConcurrentFIFO` compares durable acceptance, history and
announcement order.

`TestChannelDelivery_PublishedCompletionBeforePostAndSuccessor` holds completion
publication to prove tracker-idle alone cannot release a post.
`TestChannelDelivery_RetryAndReloadBlockCompetingStart` proves partial-write and
on-disk recovery precedence over a competing inbound turn.
`TestChannelDelivery_HoldDeadlineAndIndependentIdle` advances the diagnostic clock
while a live turn remains busy and another conversation delivers.
`TestChannelDelivery_InFlightSendNowHoldsAcrossClose` covers the write reservation
beyond a carried close; `TestChannelDelivery_NoRelayPublicationAndTeardown`
checks relay-disabled production wiring. The release-file-gated
`TestChannelPost_E2E_HeldUntilRealCompletion` checks successful held acceptance,
absence from live/history before release, and the intact reply followed by its
real completion before the separate live post.

See the [original fan-out design](../../specs/architecture/2498-channel-post-live-delivery.md#revisions)
and [durable-delivery design](../../specs/architecture/2810-durable-channel-post.md)
for the acceptance/recovery revision and ownership security review; the
[turn-boundary design](../../specs/architecture/2811-channel-post-turn-boundary.md)
records the holding and confirmed-stop ordering contract.
