# #2565 — spec: define the `push_wake` binary-to-relay envelope

Short plan: the ticket's whole deliverable is protocol-spec text, and the builder
role may not edit `docs/protocol-mobile.md`. The work is carried to the
documentation stage through the handoff below. No production code or test
changes.

## Files read

- `docs/protocol-mobile.md` § "Out of scope (v2)" — the "Push notification payload format" bullet that calls APNs/FCM payloads an out-of-band channel; the ticket asks for it to name the relay as sender.
- `docs/protocol-mobile.md` § "Routing envelope (binary↔relay leg)" — the `close_code` line and the unknown-field tolerance rule; `push_wake` is defined beside `close_code`.
- `docs/protocol-mobile.md` § "Wire shapes" — the `{conn_id, frame, token?, close_code?}` summary sentence, which should gain `push_wake?`.
- `internal/protocol/envelope.go` → `Envelope.CloseCode` — the `close_code,omitempty` precedent for a relay-interpreted field; the Go field for `push_wake` belongs to #2564, not here.
- Issue #2564 — the daemon consumer: sends one `push_wake` per absent FCM device, never Noise-sealed, token never logged.

## Change

Spec text only, in one file, owned by the documentation stage. The routing
envelope gains a relay-interpreted `push_wake` form sent binary→relay on the
`/v1/server` connection. Like `close_code`, the relay reads it itself and forwards
nothing to any phone. It carries no `conn_id` and no `frame`, only a `push_wake`
object with `platform` (`fcm`) and the device's opaque FCM `token`. Nothing else
moves: `Envelope` is unchanged by this ticket, and the unknown-field tolerance
rule already makes an older relay drop the envelope rather than fail the leg.

## Testing strategy

No new logic, so no new test. The documentation stage's `make docs-guard` covers
the edited file's heading hygiene; the envelope's round-trip test lands with the
Go field in #2564.

## Documentation handoff

**Status: pending for the documentation stage.** Both acceptance criteria of
#2565 are satisfied only by this text landing in `docs/protocol-mobile.md`.

1. **§ "Routing envelope (binary↔relay leg)"** — immediately after the line
   "The `close_code` field on the binary→relay direction is unchanged.", add a
   `push_wake` subsection stating:
   - Shape, binary→relay only, no `conn_id` and no `frame`:
     `{"push_wake": {"platform": "fcm", "token": "<opaque FCM registration token>"}}`
   - Fields: `platform` — string, `fcm` is the only value sent (an `apns` token
     is skipped until an iOS client exists); `token` — string, the opaque token
     the phone reported in `register_push_token`, passed through verbatim.
   - The relay is the envelope's **only reader**. It is addressed to the relay,
     never sealed in a Noise session, and never forwarded to a phone.
   - On receipt the relay sends one **data-only, high-priority** FCM message to
     `token` with **no payload fields**. The push carries no content: it only
     wakes the app, which reconnects over its own Noise session and learns
     what happened there.
   - What the relay learns is a token to wake, nothing about the session, turn
     or prompt that caused it.
   - An **older relay** that does not know the envelope logs and drops it; the
     binary↔relay leg stays open, so the daemon degrading to "no push" never
     breaks the connection.
   - Cross-reference: relay side pyrycode/pyrycode-relay#130, daemon side #2564.
2. **§ "Wire shapes"** — extend `{conn_id, frame, token?, close_code?}` to
   `{conn_id, frame, token?, close_code?, push_wake?}` and link the new
   subsection.
3. **§ "Out of scope (v2)"** — replace "Push notification payload format.
   Out-of-band channel; APNs/FCM payloads remain plaintext." with a line saying
   the relay sends the push on the binary's `push_wake` request, the push carries
   no payload, and it is outside the Noise channel; link the new subsection.
