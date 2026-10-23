# Delivering `channel.post`: completed thread posts, recovery and wake

A successful `channel.post` durably accepts the entire message before returning.
`channelPoster` resolves the channel and mints its turn identity;
`channelDelivery.accept` atomically saves the whole text, conversation ID, turn ID
and acceptance timestamp in the instance's private `channel-delivery.json`.
Its sole daemon-owned consumer chunks the post, records every chunk and one
completion in the composition root's existing `conversationHistory`. Those
retained sources feed authoritative thread items and explicit catch-up; raw
post publication is retired for every app (#3737, integrated acceptance #3718).
Idle delivery starts immediately on acceptance without waiting for a client, relay configuration or
user turn.

`channelPostEmitterV2` retains only a completion hook that calls the existing
`pushWaker` after a newly recorded durable completion. Its `announce` method is
an inert constructor seam; `channelDelivery` has no announcement hook or raw
fan-out. Neither deltas nor completion enter the interactive ring. Nil/failed
history and absent clients cannot restore content publication. Existing live
controls and replay infrastructure remain separate; #3738/#3739 own their
retirement. See the [integrated evidence](thread-package-shadow-lifecycle-and-evidence-retirement.md#integrated-retirement-acceptance-3718).

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
exactly one `turn_end` with the same turn ID in the durable source log.
The fold consumes that recorded order; no raw live announcement is made.
A failed head keeps later posts in that conversation pending while other conversations can progress.

An active Claude turn holds delivery, not acceptance: `channel post` returns
success after persisting the whole post without waiting for that turn to finish.
No held delta or post completion enters the durable source log. Release
follows the preceding turn's recorded real completion, or an accepted actual
child exit/confirmed
runner stop after preceding queued deltas have been handled, buffered text
flushed and the published lifecycle closed. An early pool eviction request is
not that boundary; it holds posts and successor starts through confirmed stop,
even if completion publishes in the meantime. Stale exits cannot close a newer
turn. See [session teardown](streamsup-package-per-conversation-turn-busy-track-session-teardown-clear.md).

Posts accepted by the release boundary finish whole-post delivery before an
ordinary successor user turn or reset wrap-up can start. `beginDelivery` makes
the final pending check atomic with the busy mark and write reservation under
the same consumer mutex as acceptance, recording, turn boundaries and wake
triggering. `turnBusyTracker.lockPostBoundary` shares that gate. Child writes
run outside that mutex; reservations keep posts held until writes return,
including in-flight send-now writes after a completion or carry/grace close.
A pending history retry blocks successor starts even after a recorded prefix.
Checking only an inbound snapshot or tracker-idle state would let a later
acceptance or undrained completion split the reply or post.

The durable text representation is `assistant_delta`, with no second
`message`/role-assistant entry or post `turn_state`. Canonical thread items and
explicit catch-up carry the post to apps. A delta alone does not finish the post
or notify a device. The post's private `channelPostTurnEndPayload` supplies exactly
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
uses, retaining source payload bounds even after raw transport retirement.
`TestChannelPoster_MaximumPostFitsEnvelopeCap` measures a maximum-size,
`<`-filled post against the envelope cap and checks its rejoined bytes. If it
fails, lower `maxDeltaTextBytes` rather than relaxing the measurement.

`TestChannelPost_RecordingReplayLiveWakeOrder` retains maximum-size byte
reconstruction, durable completion-before-wake and absence of raw live/replay
delivery. Fake-daemon channel-post checks retain source/canonical and actual
wake witnesses; the raw history endpoint is retired (#3715).
Client content recovery uses [Thread query messages](../../protocol-mobile.md#thread-query-messages).

## Live announcements, bounded replay and durable recovery

The historical section anchor remains for inbound links; raw announcements and
post replay copies are removed. `channelDelivery.deliver` invokes `complete`
only after a new successful durable completion, and `channelPostEmitterV2.complete`
then calls `pushWaker.Trigger` with the post's conversation and `pushWakeTurnEnd`,
including when no client is connected. Failed completion never wakes. Existing
eligibility, connected-device suppression, coalescing and mute rules remain;
see [push wake](relay-package-push-wake.md).

Catch-up and cleanup-only retry/reload do not invoke the completion hook or
wake again. Apps recover content through explicit thread catch-up/pages; this
creates no automatic durable tail backfill or exactly-once network receipt
guarantee. Shared live-state ring/replay wiring remains downstream #3738/#3739.

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
keeps accepted work pending for automatic retry without a wake. Retained prefixes
remain fold inputs. Delta-only pending records need completion; retry reconciles
the same identity without duplicating durable chunks/completion.

The narrow `channelDeliveryHistory` interface uses `AppendWithMetadata` and
raw `Page`. Each new delta stores explicit `shown: true`; the normal host-post
completion stores `shown: false`, through the shared
[history producer classifier](history-package-producers-legacy-compatibility.md#legacy-eligibility-and-explicit-visibility-2965).
Both types remain source vocabulary for the fold, with no raw history endpoint,
replay or live publication. Explicit visibility retains its stored meaning;
authoritative shown-thread progress governs unread accounting.

After process restart, undelivered or partially recorded posts resume with their
original identities and chunk order, yielding exactly one history delta per
chunk and exactly one completion. Only deltas **and completion** make pending
work durably complete. A fully completed post left pending by interrupted cleanup
is removed without another append, replay insertion, announcement or wake, even
if the process died before the wake. In-memory delivered markers also
prevent repeated wake when cleanup persistence fails. Durability
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
confirmed stop finish source recording before posts can be recorded.

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
inspects exact durable completion keys and observes durable recording → wake,
while rejecting raw frames/replay and repeated wakes.
`TestChannelDelivery_RetryAndCarryIndependence` checks both directions of carry
independence and automatic retry without publication;
`TestChannelDelivery_ConcurrentFIFO` compares durable acceptance, history and
completed source order.

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
absence from source history before release, and the intact reply followed by its
real completion before the separate completed post and successor turn.
`TestChannelPost_E2E_DisconnectedWakeReplayAndHistory` routes the replay
conversation, observes an actual content-free outbound wake, then checks the
ordered completed post in retained source history, rejects raw reconnect content
and requires no repeated wake.

See the [original fan-out design](../../specs/architecture/2498-channel-post-live-delivery.md#revisions)
and [durable-delivery design](../../specs/architecture/2810-durable-channel-post.md)
for the acceptance/recovery revision and ownership security review; the
[turn-boundary design](../../specs/architecture/2811-channel-post-turn-boundary.md)
records the holding and confirmed-stop ordering contract.
The [completion design](../../specs/architecture/2809-channel-post-completion.md)
records the original minimal host-post payload and replay/wake ordering;
[retirement](thread-package-shadow-lifecycle-and-evidence-retirement.md#residual-content-retirement-3737)
supersedes its raw publication path while preserving durable recovery and wake.
