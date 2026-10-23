# Reconnect recovery — retired event replay and explicit thread queries

Authenticated `hello.last_event_id` is an ignored unknown key (#3738).
`HelloClientPayload` no longer declares or serializes it. Absent, zero, stale,
maximum-uint64, string, object, array and null values have identical admission
and delivery behavior when the rest of the hello is valid. Token/static-key
binding, client-version validation and capability negotiation still apply.

## Content recovery

No connection receives event-ring replay or raw `resync`, with or without
`thread`. `replayMissed`, its per-session tail/watermark, the replay pump and
its Run/reconnect/teardown hooks are removed. `forwardEnvelopeSource` no longer
compares event IDs. A cursor from another conversation or a previous daemon
cannot mute future delivery.

Clients with negotiated thread delivery explicitly catch up **each open
conversation** by its completed epoch/version. Hello and conversation listing
do not query items; the daemon's active conversation never substitutes for the
requested scope. An unusable checkpoint produces `thread_reset`: request
version zero for the newest bounded window, then fetch older pages with the
successful final marker's continuation. Certify a checkpoint only after all
batches assemble and the separate successful final marker arrives. Retained
source history remains the fold input; it is distinct from in-memory event IDs,
connection envelope IDs and client presentation/read marks.

See [thread query admission and completion](v2-session-manager-state-machine-capability-negotiation-on-the-handshake-thread-delivery-and-summary-projection.md)
and the [mobile reconnect contract](../../protocol-mobile.md#reconnect--backfill-semantics).

## Current-state reconciliation and delivery

Content recovery does not replace current prompt/live-state reconciliation.
Thread connections reconcile supplied readings and clears; supported legacy
connections still receive outstanding answerable prompts and current snapshots.
Turn phases retain their [publish-before-fan-out contract](v2-session-manager-state-machine-connect-time-turn-phase-reconcile-running.md).
No ring tail holds these frames behind historical content.

Removing replay gates does not remove `livePending`: a fresh reading parked
behind its clear must finish before another reading replaces that slot, and
ordinary pushes wait behind it. Hosted capacity admission still reserves space
before popping or consuming a reading. Transport-down holds consume no queued
head or send nonce; [reconnect wakes the ordinary drain](v2-session-manager-state-machine-immediate-flush-on-reconnect-reconnect.md).
[Inline replies](v2-session-manager-state-machine-inline-reply-seals-gated-behind-transportdow.md)
retain their pre-seal transport probe. Rekey, teardown, bounded queues and
per-connection ordering retain their existing ownership and admission rules.

`SetReplaySource` still stores the emitter's ring/cursor wiring, but neither is
consulted by admission, forwarding or reconciliation. Ring ownership, storage,
configuration and surviving deletion observers remain for #3739; this retirement
does not claim they are removed. #3719 owns final integration acceptance.

## Regression coverage

`TestV2Session_LastEventIDIgnored` installs a real retained ring tail and a
counted cursor callback, checks all obsolete cursor shapes with and without
`thread`, then verifies ordered updates for the same and another conversation.
A correlated barrier proves no replay/resync preceded ordinary replies, and
reopening the same connection identity uses fresh Noise ciphers without reading
the callback. Empty ring fixtures would miss the zero/stale replay regression;
maximum uint64 separately exercises the formerly gap-classified input.

`TestThreadRecoveryAfterReconnect` proves hello calls no content provider,
explicit unusable-checkpoint recovery returns `thread_reset`, and newest-window
and page requests return scoped items plus a successful final boundary. The
requested-scope, host-isolation, pending-reading, hosted-capacity and nonce
oracles remain independent checks; absence of replay alone proves none of them.
