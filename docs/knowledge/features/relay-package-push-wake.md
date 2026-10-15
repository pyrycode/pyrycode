# `internal/relay` push-wake — `SendPushWake` and the daemon-side trigger

Split out of [`relay-package.md`](relay-package.md) (parent doc — see there for the connection/transport layers this surface sits behind). This child covers waking an absent phone end to end: the relay-addressed wire call and the daemon-side `pushWaker` that drives it.

## Push-wake surface (`SendPushWake`, #2564)

`(*Connection).SendPushWake(platform, token string) error` asks the relay to send a platform push to an absent device — the daemon-authored half of `docs/protocol-mobile.md` § `push_wake`. Unlike `CloseConn` and `Send`, the envelope is **relay-addressed, not conn-addressed**: it carries no `conn_id` and no `frame`. It can't reuse `protocol.RoutingEnvelope` for this — that type's `conn_id` and `frame` fields have no `omitempty`, so it always serializes both keys, which is off-spec for an envelope meant to carry neither. `SendPushWake` marshals its own unexported `pushWakeEnvelope{PushWake: pushWakeRequest{Platform, Token}}` via `marshalPushWake`, then forwards through the same `transport.Client.Send` path as `CloseConn`, returning the transport sentinels unchanged. A marshal failure returns a fixed sentinel rather than a wrapped encoder error, so no error from this method can carry the token; neither send path logs the token. The envelope is never sealed in a Noise session — the relay is its only reader.

### Correlated feedback and connection lifetime

`(*Connection).SendPushWakeFeedback(ctx context.Context, platform, token,
requestContext string) (<-chan string, error)` implements the opt-in
[invalid-token result contract](../../protocol-mobile.md#push_wake_result)
(#3435). It sends `push_wake` with `platform`, `token` and a fresh,
token-independent `request_id`, with no `conn_id` or `frame`. IDs use 1–64
ASCII bytes from `A-Z a-z 0-9 _ -` and are never reused during the
`Connection` lifetime, including reconnects. Legacy `SendPushWake` still
omits `request_id` and acknowledges queue admission; the opt-in method waits
for successful socket write completion. Old peers can remain silent.

The immutable `requestContext` string stays local, never serialized or logged
by this API. It can hold an opaque caller-owned handle to the paired identity
and exact registration snapshot. The buffered, request-local channel emits
that original string at most once for valid `invalid_token` feedback, then
closes. Callers must check the receive boolean: closure without a value and
silence never signal invalidity. Submission/write failure returns an error
and no channel, retiring any binding without notification.

At most 128 bindings are pending per live transport generation. Each expires
60 seconds after initial submission; write completion and replies never renew
the deadline, and a result exactly at expiry is already too late. At capacity,
the API sends the legacy shape on the captured generation and returns an
already-closed feedback channel on successful write. Overflow preserves all
accepted bindings and deadlines. Timers and cancellation callbacks release
idle bindings without requiring another wake or reply.

Bindings are installed before submission so an immediate reply can be retained,
but notification also requires successful write completion and a still-live,
unexpired binding. Request cancellation retires its binding even after the
method returns. Disconnect, replacement, `Close` and connection-lifecycle
cancellation retire the generation's bindings; reconnect starts empty and
requests never migrate to replacement sockets. Submission uses
`transport.Client.SendGeneration`; `ReceiveGeneration` preserves the receive
pump's socket identity. Shared `Connection.Frames` and reconnect notifications
cannot establish that ownership after replacement, even when an ID matches.
See [transport generation ownership](transport-package.md#socket-generation-ownership).
Ordinary frames keep their order and the existing single observer of
`Connection.Reconnected` remains unchanged.

`forwardFrames` consumes any present outer `push_wake_result` before phone
routing, including malformed results. `consumeWakeResult` validates the whole
required-field decode, ID syntax and exact `invalid_token` code before lookup;
unknown fields are tolerated. Malformed/unknown results, duplicates, expired
and stale-generation replies cannot notify or consume a live binding. Required
fields use pointers because decoding a duplicate key ending in JSON `null`
into a string can preserve an earlier valid value without returning an error.
Whole-decode error rejection also prevents partially decoded live IDs from
being acted on. `TestWakeFeedbackResults` exercises these cases against a live
binding before sending a valid reply. Malformed routing diagnostics use fixed
text, so decoder errors cannot reflect supplied bytes or inner-frame content.

Daemon registry consumption and conditional removal remain pending in
[Core #3436](https://github.com/pyrycode/pyrycode/issues/3436); this API mutates
neither registrations nor pairing. Caller identity must come from the retained
local context, never result-supplied fields. Separate `Registry.FindByTokenHash`
and `Registry.UpdatePushRegistration` calls leave a gap in which a replacement
token can arrive and then be erased. The pending consumer must compare
and clear atomically against in-process updates inside the existing
[lock/reload/mutate/save region](relay-package-handlers.md#behavioural-contract),
which also preserves concurrent pairing/revocation writes.
Coordinated [operator activation](../../protocol-mobile.md#push-wake-publication-and-activation)
also remains pending; hermetic injected replies do not prove live provider behavior.

The companion [relay #213](https://github.com/pyrycode/pyrycode-relay/issues/213)
cannot satisfy the feedback deadline by reusing `WSConn.Send` unchanged: that
method waits for its serialized writer before starting a ten-second write
timeout. The [five-second feedback budget](../../protocol-mobile.md#feedback-bounds-and-overflow)
includes queue and writer wait as well as I/O, with earlier cancellation on
expiry, disconnect or shutdown. Moving delivery off the read loop alone does
not bound its lifetime. Likewise, evicting a coalescing-cache entry must not
release accounting for an original result still queued for delivery; otherwise
queue pressure can bypass the feedback caps. These are companion requirements,
not behavior implemented by protocol publication.

### Daemon-side trigger (`cmd/pyry`'s `pushWaker`, #2564)

The daemon wakes an absent phone from three fan-out points:

- `interactiveTurnEmitterV2.Handle`'s `turnevent.TurnEnd` arm, right after `endTurn()` — connected phones already got the `turn_end` from that fan-out; the wake reaches the ones that don't have an open session.
- `channelPostEmitterV2.complete`, after host-post `turn_end` fan-out, with the post's own conversation and `pushWakeTurnEnd`, including when no client is connected. All ordered deltas and completion have succeeded in durable history and entered the shared replay ring before this trigger, inside the serialized safe delivery boundary. Fully completed pending work is cleanup-only; reconnect replay reads retained events without calling `complete`, so neither repeats a wake. See [channel-post delivery](control-plane-channel-post-live-delivery.md).
- `streamApprovalBridge.Surface`'s live `modal_shown` broadcast, right after it fires. Reconnect replay (`reconcileModals`) pushes outstanding modals directly and never passes through `Surface`, so replaying a modal on reconnect never triggers a wake.

All three call `Trigger(conversationID, trigger)` on a nil-safe `pushWaker` — a non-blocking, cap-1 channel send — so fan-out never waits on a device-registry read, an open-session snapshot, or a relay write. The actual pass runs on the waker's own goroutine, started in `startRelayV2` beside `conn`/`registry`/`mgr` and exiting on the daemon ctx. Host posts reuse its eligibility, connected-device suppression and coalescing; client mute/per-conversation notification rules still apply.

A device is woken when its `Platform` is `fcm` with a non-empty `PushToken`, no open v2 session belongs to it, and it was not already woken inside a 30 s window — a failed send does not start that window, so the next trigger retries it. "Belongs to it" is decided on `ActiveConn.DeviceTokenHash` (the `TokenHash` of the device the handshake authenticated, filled in `handleActiveConns` from `s.device`), **never** the hello-claimed `ActiveConn.DeviceName` — a phone could otherwise silence another device's wake by claiming that device's name in `hello`. See [`v2-session-manager-surface.md`](v2-session-manager-surface.md) for the `ActiveConn` field list.

Eligible devices are sent newest `PairedAt` first (\#2602): `wakeAbsent` sorts the `devices.Registry.List()` copy with `slices.SortStableFunc` (comparing `PairedAt` descending) right before the send loop. The relay caps each server-id to a burst of 6 wakes and silently drops the rest as rate-limited; dead tokens from earlier pairings are currently never pruned (the correlated API is available, but daemon adoption in Core #3436, relay #213 and coordinated activation remain pending), so sending in `List()`'s on-disk order (oldest first, per `Registry.save`'s persisted sort) let stale registrations spend the whole burst before the live phone's wake was ever sent, dropping it. The sort is stable, so devices with equal (e.g. zero) `PairedAt` keep registry order, and it sorts the `List()` copy rather than relying on or reversing the registry's own ordering, so it doesn't couple to `devices.Registry`'s internal ordering guarantee.

**Since #2601, a phone drops out of `ActiveConns` the instant its WebSocket ends, not just at the 15-minute idle sweep.** The relay's own close notice (a `{conn_id, close_code}` routing envelope with no `frame`, pyrycode-relay#152) now tears the v2 session down at once — see [`v2-session-manager.md` § Inbound peer-close notice](v2-session-manager-state-machine-inbound-peer-close-handlepeerclose-teardown.md). Before this, a phone that had just backgrounded stayed listed as an open session until the sweep fired, so `wakeAbsent` skipped it and a turn ending or a modal appearing while it was briefly away sent no wake at all — the failure this section's #2564 motivation describes. The idle sweep remains the fallback for a relay that never sends the notice.

**Lesson: a nil `ActiveConns` snapshot after ctx cancellation is not "nobody is connected."** `V2SessionManager.ActiveConns` returns `nil` once its `ctx` is cancelled or `Run` has exited. Every prior consumer (broadcast, per-conn push) reads that as "reach nobody," which is harmless — an empty send target sends nothing. The push-wake trigger reads the *absence* of a device from that same snapshot as the positive signal "no open session, so wake it," which inverts the harmless case into "wake every device" at exactly the moment (shutdown) it must not. `wakeAbsent` re-checks `ctx.Err()` immediately after taking the snapshot and returns without sending if it is non-nil. Any future `ActiveConns` consumer that treats *absence from the snapshot* as a trigger for action, rather than as a filter that only narrows an existing intent, needs the same check.

**Since #2705, every pass logs its decision at Info, the daemon's default level.** The two pre-existing lines, `push_wake.sent` and `push_wake.send_err`, were Debug-only until then, so neither reached the journal unless `-pyry-verbose` was set — the silence that left two 2026-10-02 notification failures undiagnosable from the daemon side. Per fcm device with a non-empty token, `wakeAbsent` now logs exactly one of three events: `push_wake.sent`, `push_wake.send_err` (the send error's text is never logged — the relay's error path can echo the token), or `push_wake.skipped` with `reason=device_connected` (an open v2 session already belongs to the device) or `reason=recently_woken` (inside `pushWakeWindow`). Each line carries `device_name` (the registry's `devices.Device.Name`, never the hello-claimed `ActiveConn.DeviceName`), `conversation_id`, and `trigger` — `turn_end` from `interactiveTurnEmitterV2.Handle`'s `TurnEnd` arm or `channelPostEmitterV2.complete`, or `modal_shown` from `streamApprovalBridge.Surface`'s live broadcast (reconnect replay via `reconcileModals` never passes through `Surface`, so it never produces a `modal_shown` trigger). A burst of triggers while a pass runs still collapses into one more pass; the collapsed pass names only its most recent trigger, and a trigger folded in by a race between `Trigger` and the pass about to read it leaves no line naming it at all — read the log as one line per device per pass, not one line per `Trigger` call.

To read the journal: a turn end with the phone away and no `push_wake.sent` for that device means the daemon never asked the relay to wake it — check the matching `push_wake.skipped` reason, or whether the device is `fcm` with a token at all. A `push_wake.sent` with no background `relay: v2 handshake accept` from that device within roughly `pushWakeWindow` points at the relay or FCM, not the daemon; see pyrycode-relay#150 for the relay-side half of that diagnosis.

**Lesson: a test logger that captures below the production level can't catch a level regression.** `newTestPushWaker` captured at Debug, so the original Debug-level `push_wake.sent`/`send_err` lines passed every assertion in `push_wake_test.go` while being invisible in the real daemon's default-Info journal — the exact gap #2705 closes. It now captures at Info, so the same tests also prove the level, not only the content. The redaction test (`TestPushWaker_SendFailure_TokenNeverLogged`) pins the companion trap: a token, hash or error string reused across fixtures can match a forbidden value by accident even when redaction is broken, so it uses a distinctive token/hash/error string the right content would never otherwise produce.
