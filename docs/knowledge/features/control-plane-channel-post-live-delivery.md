# Fanning `channel.post` out: completed host posts, replay and wake

A successful `channel.post` durably accepts the entire message before returning.
`channelPoster` resolves the channel and mints its turn identity;
`channelDelivery.accept` atomically saves the whole text, conversation ID, turn ID
and acceptance timestamp in the instance's private `channel-delivery.json`.
Its sole daemon-owned consumer chunks the post, records every chunk and one
completion in the composition root's existing `conversationHistory`, then
publishes the same payloads through `channelPostEmitterV2`. Idle delivery starts
immediately on acceptance without waiting for a client, relay configuration or
user turn.

`channelPostEmitterV2` retains `conversationUpdateEmitterV2`'s fan-out shape:
a leaf mutex over its envelope-ID counter, the #607 interactive-capability gate,
and a `Push` loop that logs and skips a torn-down connection. `startRelayV2` returns
its announce hook through `startRelay`; `startRelayV2` also installs the completion
hook and shares the interactive emitter's ring and existing `pushWaker`.
`runSupervisor` installs the delta hook before starting the consumer independently
of the relay leg. Both hooks are nil without a relay; durable completion still
records, and hook results are never consulted. A fan-out
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
has its own turn ID, sequences from zero, and text whose concatenation reproduces
the input byte-for-byte. Its ordered `assistant_delta` chunks are followed by
exactly one `turn_end` with the same turn ID, in live delivery and served history.
All chunks and completion reach history before the first live announcement;
publication follows their recorded order. A failed head keeps later
posts in that conversation pending while other conversations can progress.

An active Claude turn holds delivery, not acceptance: `channel post` returns
success after persisting the whole post without waiting for that turn to finish.
No held delta or post completion enters served history or live frames. Release
follows the preceding turn's published real completion, or an accepted actual
child exit/confirmed
runner stop after preceding queued deltas have been handled, buffered text
flushed and the published lifecycle closed. An early pool eviction request is
not that boundary; it holds posts and successor starts through confirmed stop,
even if completion publishes in the meantime. Stale exits cannot close a newer
turn. See [session teardown](streamsup-package-per-conversation-turn-busy-track-session-teardown-clear.md).

Posts accepted by the release boundary finish whole-post delivery before an
ordinary successor user turn or reset wrap-up can start. `beginDelivery` makes
the final pending check atomic with the busy mark and write reservation under
the same consumer mutex as acceptance, recording, replay, live fan-out and wake
triggering. `turnBusyTracker.lockPostBoundary` shares that gate. Child writes
run outside that mutex; reservations keep posts held until writes return,
including in-flight send-now writes after a completion or carry/grace close.
A pending history retry blocks successor starts even after a recorded prefix.
Checking only an inbound snapshot or tracker-idle state would let a later
acceptance or unpublished completion split the reply or post.

The text has one representation: `assistant_delta`, with no second
`message`/role-assistant entry or post `turn_state`. A lone delta renders live
or from a served page and starts a fresh bubble, but does not finish the post or
notify a device. The post's private `channelPostTurnEndPayload` supplies exactly
four keys: `conversation_id`, `turn_id`, `stop_reason: "end_turn"`, and
`producer: "channel_post"`. The producer identifies daemon-authored host-post
completion. Claude-only `outcome`, `is_error`, `terminal_reason`,
`error_category`, duration, cost, turn-count and token-usage fields are entirely
absent, including zero/null placeholders. Claude's `protocol.TurnEndPayload`
serialization and result reporting stay unchanged. Holding preserves the intact
Claude reply and its real completion before the separate completed post, which
finishes before a successor turn.

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

A heavily escaped maximum post can exceed a single served history page's byte
bound even though every envelope fits. Follow page cursors to reconstruct the
whole post; a short page is not the entire log. The multi-chunk fake-daemon
fixture fits one page, while `TestChannelPost_RecordingReplayLiveWakeOrder`
retains maximum-size byte reconstruction.

## Live announcements, bounded replay and durable recovery

After all durable writes succeed, `channelPostEmitterV2.announce` and `complete`
append each delta/completion to the same ring registered with
`V2SessionManager.SetReplaySource` before that event's live fan-out. Each live
envelope carries its assigned connection-independent `event_id`, identical for
all recipients of that event and retained on replay. Ring event IDs and durable
history entry IDs are different domains. After completion fan-out, `complete`
calls `pushWaker.Trigger` with the post's own conversation and `pushWakeTurnEnd`,
including when no client is connected. All post events are recorded in history
and the shared ring before this trigger; the interactive emitter's best-effort
history path cannot establish that guarantee. Existing eligibility,
connected-device suppression, coalescing and client mute/per-conversation
notification rules remain; see [push wake](relay-package-push-wake.md).

Reconnect replay reads retained events without invoking the completion hook or
waking again. `replayMissed` uses the daemon's active conversation and
`hello.last_event_id`, not a client-selected conversation. Retention remains
bounded by `MaxEventsPerConversation` (1024); `assistant_delta` remains droppable
in `pushQueue.enqueue` and preferentially evicted by `convRing.evictOldest`.
An expired cursor receives the existing `resync`. The ring lives only in the
daemon and does not survive restart; history-backed restart recovery and bounded
replay provide no exactly-once network receipt guarantee or conversation-selection
change. Automatic durable tail catch-up remains
[#2744](https://github.com/pyrycode/pyrycode/issues/2744).

The hold diagnostic deadline is **five minutes from durable acceptance**
(`channelPostHoldDeadline`). Crossing it while held emits
`channel_post.hold_deadline` once per post per process, containing only fixed
event text, conversation ID and turn ID. The post stays pending. Expiry never
delivers, interrupts, marks idle, abandons or ends a still-live Claude turn.
It is a diagnostic threshold, not a delivery timeout.

`history.Store.AppendWithMetadata` is per-event, so a recorded prefix can be
visible while a later write retries. `channelDelivery.deliver` scans **every**
newest-first `Page`, matching the conversation, turn ID, sequence and text, then appends only
missing chunks in ascending sequence, then the matching four-field completion.
Looking only at the newest page would duplicate a prefix hidden by unrelated
newer entries. A failure at any write, including completion after every delta,
keeps accepted work pending for automatic retry and publishes nothing to replay,
live clients or wake. Delta-only pending records need completion and publication;
retry reconciles the same identity without duplicating durable chunks/completion.

The narrow `channelDeliveryHistory` interface uses `AppendWithMetadata` and
raw `Page`. Each new delta stores explicit `shown: true`; the normal host-post
completion stores `shown: false`, through the shared
[history producer classifier](history-package-producers-legacy-compatibility.md#legacy-eligibility-and-explicit-visibility-2965).
Both types remain eligible for legacy history, replay and live delivery;
visibility metadata changes unread accounting without changing their payloads.

After process restart, undelivered or partially recorded posts resume with their
original identities and chunk order, yielding exactly one history delta per
chunk and exactly one completion. Only deltas **and completion** make pending
work durably complete. A fully completed post left pending by interrupted cleanup
is removed without another append, replay insertion, announcement or wake, even
if the process died before its first live push. In-memory delivered markers also
prevent repeated publication/wake when cleanup persistence fails. Durability
matches history's process-restart contract; it does not promise survival of a
machine crash.

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

The ten daily posts measured in `7dc049bc` between 2026-09-24 and 2026-10-03
retain recoverable text and their missed alerts. They are not migrated, completed
retroactively or re-notified by this change.

## Pending delivery and channel carry

Private client delivery and [`channelCarry`](control-plane-channel-post-carry.md#carrying-a-posted-channel-message-into-claudes-next-turn-2499)
are independent. Newly accepted posts record carry once, after acceptance.
Carrying or clearing text for Claude cannot consume pending client delivery,
and client-delivery cleanup cannot consume carry. Retry and reload never call
`channelCarry.record`: doing so could re-add text already cleared for Claude.
Carry keeps its existing best-effort persistence and growth bounds, including
the possible crash window between acceptance and carry recording.

Refusals and logs omit posted text, raw storage errors and content-derived chunk
counts or sequences. Acceptance, delivery-retry and cleanup logs use fixed
events and identity fields.

`TestChannelDelivery_ReloadInterruptions` places recorded events behind newer
history pages and checks undelivered, partial, delta-only and fully completed
recovery. `TestChannelDelivery_CompletionRequired` proves automatic retry after
completion recording fails; `TestChannelPost_RecordingReplayLiveWakeOrder`
inspects the exact raw completion keys across history/replay/live and observes
durable recording → per-event shared replay recording → live fan-out → wake.
`TestChannelDelivery_RetryAndCarryIndependence` checks both directions of carry
independence and automatic retry with and without an announcer;
`TestChannelDelivery_ConcurrentFIFO` compares durable acceptance, history and
announcement order.

**History doubles must intercept the method delivery actually calls.**
`testPostHistory`, `testBlockedPostHistory` and `testPosterHistory` implement
`AppendWithMetadata`. Overriding only `Append` on a double with an embedded
`Store` lets the promoted metadata method bypass injected failures or shutdown
barriers. Forward the supplied metadata when delegating to the real store so
those tests also exercise the production visibility contract.

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
real completion before the separate completed post and successor turn.
`TestChannelPost_E2E_DisconnectedWakeReplayAndHistory` routes the replay
conversation, observes an actual content-free outbound wake, then checks ordered
reconnect replay and the same completed post in served history, with no replay wake.

See the [original fan-out design](../../specs/architecture/2498-channel-post-live-delivery.md#revisions)
and [durable-delivery design](../../specs/architecture/2810-durable-channel-post.md)
for the acceptance/recovery revision and ownership security review; the
[turn-boundary design](../../specs/architecture/2811-channel-post-turn-boundary.md)
records the holding and confirmed-stop ordering contract.
The [completion design](../../specs/architecture/2809-channel-post-completion.md)
records the minimal host-post payload and replay/wake ordering.
