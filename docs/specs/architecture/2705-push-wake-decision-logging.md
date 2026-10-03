# #2705 — log every push-wake decision, sent or skipped, without the token

## Files read

- `cmd/pyry/push_wake.go` → `pushWaker`, `Trigger`, `run`, `wakeAbsent`: the waker this ticket changes; the `SECURITY` paragraph on `pushWaker` forbids device names today.
- `cmd/pyry/push_wake_test.go` → `newTestPushWaker` (captures at Debug), `TestPushWaker_SendFailure_TokenNeverLogged`, the two call-site tests `TestInteractiveTurnEmitterV2_TurnEndTriggersWake` and `TestStreamApprovalBridge_SurfaceTriggersWake`.
- `cmd/pyry/interactive_turn_v2.go` → `interactiveTurnEmitterV2.Handle`, `turnevent.TurnEnd` arm: has `convID` in scope, calls `e.waker.Trigger()`.
- `cmd/pyry/modal_resolve_v2.go` → `streamApprovalBridge.Surface`: has `conversationID` in scope, calls `b.waker.Trigger()` after the live `modal_shown` broadcast.
- `cmd/pyry/relay.go` → `startRelayV2`: constructs the waker and starts `run`; unchanged.
- `internal/relay/v2session_handshake.go` → the `relay: v2 handshake accept` line: precedent for logging the registry name at Info under the key `device_name`.
- `cmd/pyry/pair.go` → the device name is set by the operator's `pyry pair` invocation, not by the phone.
- `docs/knowledge/features/relay-package.md` § "Daemon-side trigger": the eligibility rule on `DeviceTokenHash`, never the hello-claimed `DeviceName`; the cancelled-snapshot lesson (unchanged here).

No other feature branch touches these four files.

## Change

`Trigger()` becomes `Trigger(conversationID string, trigger pushWakeTrigger)`, where `pushWakeTrigger` is an unexported string type with two constants, `pushWakeTurnEnd = "turn_end"` and `pushWakeModalShown = "modal_shown"`. The two call sites pass their conversation id and their constant.

The waker keeps its cap-1 `trig` channel, so coalescing is unchanged. Beside it, a `pending pushWakeCause` (conversation id + trigger) guarded by a small mutex: `Trigger` overwrites `pending` and then does the non-blocking send; `run`, on receiving a signal, reads `pending` under the lock and passes it to `wakeAbsent(ctx, cause)`. A burst therefore collapses into one pass naming the most recent trigger. The mutex is held only for the copy, never across the pass, so `Trigger` still never waits on a snapshot, registry read or relay write.

`wakeAbsent`'s loop is unchanged in selection and order. Per device on `fcm` with a non-empty token it now logs exactly one Info line, `event` plus `device_name` (`d.Name`, the registry entry), `conversation_id` and `trigger`:

- open session by `DeviceTokenHash` → `push_wake.skipped`, `reason=device_connected`;
- inside `pushWakeWindow` → `push_wake.skipped`, `reason=recently_woken`;
- send returns nil → `push_wake.sent`;
- send fails → `push_wake.send_err` (no error text).

A device not on `fcm` or without a token keeps its silent `continue`. The `SECURITY` paragraph is updated: the registry name is allowed; token, token hash, error text and the hello-claimed `ActiveConn.DeviceName` stay out.

## Testing strategy

`newTestPushWaker` captures at Info, so every assertion also proves the Info level.

- **Decision lines (AC-1):** one pass over six devices — woken, failing send, connected, recently woken (seeded via a prior pass), apns, fcm without token. Parse the JSON log lines and assert exactly one line per eligible device with the right `event`/`reason`, and no line for the apns and tokenless devices.
- **Trigger through the real call sites (AC-2):** extend the two call-site tests: after `Handle(TurnEnd)` / `Surface`, take the pending signal and run the pass with one absent fcm device; the line carries `trigger=turn_end` / `modal_shown` and the call site's conversation id. A coalescing case: `Trigger(a, turn_end)` then `Trigger(b, modal_shown)` leaves one pending pass whose lines name `b` and `modal_shown`.
- **Redaction (AC-3):** rework `TestPushWaker_SendFailure_TokenNeverLogged`: the absent device "Pixel" fails with an error embedding its token; an open conn claims `DeviceName: "Pixel"` under the other device's hash. The log carries neither token, neither hash, nor the error text; "Pixel" appears as `push_wake.send_err`, the connected device is skipped under its own registry name. Existing coalescing and failed-send-opens-no-window assertions stay.

## Documentation handoff

Pending for the documentation stage: `docs/knowledge/features/relay-package.md` § Daemon-side trigger (`cmd/pyry`'s `pushWaker`, #2564) — add a short paragraph listing the three Info events (`push_wake.sent`, `push_wake.skipped` with `reason` `device_connected` / `recently_woken`, `push_wake.send_err`), their fields (`device_name` from the registry, `conversation_id`, `trigger` = `turn_end` / `modal_shown`), and how to read them: a turn end with the phone away and no `push_wake.sent` for it means the daemon never asked the relay; a `push_wake.sent` with no background `v2 handshake accept` after it points at the relay or FCM.

## Security review

**Verdict:** PASS

**Findings:**

- [Trust boundaries] No findings. The logged name is `devices.Device.Name` from `pushWakeDevices.List()`, set by the operator in `pyry pair`; the phone-controlled `ActiveConn.DeviceName` is never read by `wakeAbsent`, and eligibility stays keyed on `ActiveConn.DeviceTokenHash`. `conversationID` is daemon-minted. The trigger is one of two constants.
- [Tokens] No findings, by design: `d.PushToken` still reaches `send` only; `d.TokenHash` is a map key only and never a log field; the send error is dropped, not logged, because the relay error path may echo the token (pinned by the AC-3 test with a token-bearing error).
- [Errors, logs, telemetry] SHOULD FIX: moving from Debug to Info puts the device name in the default journal on every pass. Accepted precedent (`v2.handshake.accept` already logs it at Info), but the line volume is bounded by triggers × fcm devices, and coalescing is unchanged; the build must not add any other field (e.g. `ConnID` of the suppressing conn, which would let the journal correlate a phone-claimed identity). The verifier checks the field set is exactly `event`, `reason` (skips only), `device_name`, `conversation_id`, `trigger`.
- [Concurrency] No findings. One new mutex guarding `pending`, taken alone (no other lock held) in `Trigger` and in `run`; never held across `wakeAbsent`. `run` still exits on ctx cancel; no new goroutine.
- [File operations / Subprocesses / Cryptography / Network] Not touched: no file, process, crypto or socket code changes.
- [Threat model] The relevant threat (a paired phone silencing another device's wake by claiming its name) stays closed by keying on `DeviceTokenHash`; this ticket only adds logging. Device-name log-injection is not a concern: slog's handlers quote values, and the name is operator-supplied.

**Reviewer:** builder (self-review per the security-review checklist)
**Date:** 2026-10-03
