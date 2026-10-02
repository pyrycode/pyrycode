# #2704 — record which device and app version sent each message, and when it was tapped

## Files read

- `internal/relay/handlers/send_message.go` → `SendMessage`, `Enqueuer`, the `send_message.enqueued` log line: the enqueue call site and the line that gains `device_name` / `client_version`.
- `internal/msgqueue/queue.go` → `EnqueueAttached`, `queued`, `QueuedMessage`, `notifyDelivered`: the record and the delivered projection #2596 widened for `attachment_ids`; this ticket widens them the same way.
- `cmd/pyry/operator_message_history.go` → `newOperatorMessageHistory`: builds the stored `role: "user"` `MessagePayload`; since #2699 the live `message` push reuses these bytes, so it needs no change of its own.
- `internal/protocol/messaging.go` → `SendMessagePayload` (+ its `UnmarshalJSON`), `MessagePayload`: the inbound and stored wire shapes.
- `internal/relay/v2session_handshake.go` → the token-OK accept path in `handleNoiseInit` (`recordClientVersion`, then `s.device = &device`): where the conn's device snapshot is taken before this hello's version is applied.
- `internal/relay/v2session.go` → `routeAppFrame`: hands `s.device` to `dispatch.NewConn`, so the handler reads it as `c.Auth()`. Not changed.
- `internal/dispatch/dispatch.go` → `Conn.Auth`: the snapshot the handler reads. Not changed.
- `internal/sessions/systemprompt.go` → `AdmitClientVersion`: the one admission filter for a client version (≤ bound, valid UTF-8, no control chars, no `"`).
- `internal/devices/device.go` → `Device.ClientVersion`, `Device.Name`.
- `internal/relay/v2session_test.go` → `TestV2Session_OpenState_HandlerAuthDevice`: the pattern for a relay test that captures `c.Auth()` from inside a handler.
- Analogue: #2596 (`9fb817c2`), one field along the same send_message → queue → history path.

No other feature branch touches these files.

## Context

The only way to tell which app sent a message today is joining the `send_message enqueued` line's `conn_id` against the handshake-accept line's `device_name`, which fails once the handshake line ages out. The history carries no sender at all, and nothing records when the user tapped Send (the history stamps a queued message at delivery). This adds three optional keys to the stored operator turn, and two to the enqueue log line.

## Design

**Current version on the conn snapshot.** In `handleNoiseInit`'s accept path, after `recordClientVersion(s.connID, device, hello.ClientVersion)` and before `s.device = &device`, set `device.ClientVersion = sessions.AdmitClientVersion(hello.ClientVersion)`. `recordClientVersion` persists exactly that admitted value (it stores `""` for a refused or absent one), so the snapshot now matches the record just written instead of the previous release's value. `recordClientVersion` still compares against the pre-update snapshot, so its write-only-on-change fast path is untouched. Re-key never touches `s.device`, so the value holds for the session's life. No other production code reads `Auth().ClientVersion`. The handler then reads `c.Auth().Name` and `c.Auth().ClientVersion`, nil-checking `c.Auth()`.

**Wire in.** `SendMessagePayload.ClientSentAt json.RawMessage` (`client_sent_at,omitempty`). Raw rather than `string` so a wrongly typed value (a number, an object) cannot fail the whole decode and refuse the message: the AC says an unparseable value is dropped and the message accepted.

**Parse once, in the handler.** `parseClientSentAt(raw json.RawMessage) time.Time` returns the zero time unless raw decodes as a JSON string that `time.Parse(time.RFC3339, …)` accepts and whose UTC year is in 1..9999 (so the re-formatted value is itself valid RFC 3339). Returns UTC. The raw bytes go no further than this function.

**Queue.** New `Queue.EnqueueSent(convID, messageID, text, delivery string, attachmentIDs []string, deviceName, clientVersion string, clientSentAt time.Time) uint64`; `EnqueueAttached` becomes a wrapper passing zero values (as `EnqueueDelivery` wraps it). `queued` and `QueuedMessage` gain `deviceName`/`DeviceName`, `clientVersion`/`ClientVersion`, `clientSentAt`/`ClientSentAt`, set only on the delivered projection (`notifyDelivered`), like `AttachmentIDs`. Positional rather than a struct because the handler's `Enqueuer` interface is consumer-side and `handlers/` deliberately does not import `msgqueue`. `handlers.Enqueuer` becomes `EnqueueSent`.

**Stored entry.** `MessagePayload` gains `DeviceName`, `ClientVersion`, `ClientSentAt string`, each `omitempty`, after `attachment_ids`. `newOperatorMessageHistory` sets the first two verbatim and `ClientSentAt` to `msg.ClientSentAt.Format(time.RFC3339Nano)` when non-zero. An entry with none of them marshals to today's bytes.

**Log line.** `send_message.enqueued` appends `device_name` and `client_version` only when non-empty. `client_sent_at` is never logged.

## Concurrency model

No new goroutines or locks. The snapshot field is written on Run before `V2StateOpen` and read-only after, which is the existing publication for `s.device`. The queue fields are copied under `q.mu` with the rest of the record.

## Error handling

No new error branch. A missing device record, empty name, inadmissible version or unparseable tap time each omit their key; the message is accepted and delivered as today.

## Testing strategy

- `handlers`: a table test over `client_sent_at` values — absent, valid `Z`, valid with offset (stored as UTC), fractional seconds, non-string, garbage string, out-of-range year after UTC conversion — asserting what reaches `EnqueueSent` and that the message is acked. A test that the enqueue log line carries `device_name` and `client_version` from `c.Auth()`, omits both keys on a conn with no device, and never contains the tap-time value.
- `msgqueue`: `EnqueueSent` projects the three values onto `OnDelivered`'s `QueuedMessage`.
- `cmd/pyry`: the stored entry carries the three keys; an entry with no values has the same bytes as before (no key present).
- `relay`: a handler captures `c.Auth().ClientVersion` on a conn whose device record holds an older `ClientVersion` and whose hello reports a new one; it sees the new one (the AC's stale-snapshot case). Also an inadmissible hello version reads `""`.

## Open questions

- Fractional-second precision: kept via `RFC3339Nano`; settle if a reviewer prefers whole seconds.

## Documentation handoff

Pending for the documentation stage, `docs/protocol-mobile.md`:

- § Application message types, `send_message` row: add the optional `client_sent_at` (RFC 3339 tap time from the client's clock). An unparseable value is dropped and the message is not refused.
- § Conversation history (v2) › A history entry: next to the `attachment_ids` paragraph, state that a stored `role: "user"` entry's payload carries `device_name` (the paired device record's name), `client_version` (the app version at send time) and `client_sent_at` (when the client says Send was tapped, client clock, not used for ordering, re-formatted by the daemon as UTC RFC 3339). Each is omitted when absent, and entries stored before this change have none of them.
- § Application message types, `message` row: the pushed operator message carries the same three keys.

## Security review

**Verdict:** PASS

**Findings:**

- [Trust boundaries] `client_sent_at` is untrusted, persisted and served to every paired device. The boundary is one function, `parseClientSentAt` in `handlers`: it yields a `time.Time`, so the queue, the record and the history producer hold a typed value and the raw bytes cannot reach storage structurally. The daemon re-formats it. The year range check keeps an offset-shifted extreme (`0000-01-01T00:00:00+01:00`) from producing a non-RFC-3339 string. Not used for ordering anywhere.
- [Trust boundaries] `client_version` is client-authored. It is taken from the snapshot only after `sessions.AdmitClientVersion`, the existing single filter (bounded length, valid UTF-8, no control characters, no `"`), so what is stored and logged is the same admitted value `pyry pair list` already shows. `device_name` comes from the pairing record (`Auth().Name`), never the hello's self-reported name.
- [Tokens] No findings. No token or key material is touched; the hello's `Token` is not retained (the version is copied by value as before).
- [File operations] No findings. No new file I/O; history persistence is the existing store.
- [Subprocesses] No findings. The tap time and sender keys never reach claude's input; delivery still carries only the composed prompt.
- [Cryptography] No findings. None used.
- [Network and I/O] No findings. The raw field is bounded by the existing application-envelope cap; `time.Parse` work is linear in it.
- [Errors, logs, telemetry] SHOULD FIX (build): the enqueue line logs `device_name` and `client_version` only; `client_sent_at` must never be logged, and a test asserts the tap-time bytes are absent from the line. The admitted version cannot carry newlines or quotes, so it cannot forge a log line; the record name is already logged on the handshake-accept line.
- [Errors, logs, telemetry] OUT OF SCOPE: a device name or version exposes one paired device's identity to the others through history. Accepted in the ticket (agreed 2026-10-02); OS / model detail stays out.
- [Concurrency] No findings. See Concurrency model.
- [Threat model] § Security model threat 1 (prompt injection) does not apply: none of the three values is composed into claude's input.

**Reviewer:** builder (self-review per the security-review checklist)
**Date:** 2026-10-03
