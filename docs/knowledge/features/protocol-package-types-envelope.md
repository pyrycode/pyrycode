# `Envelope`

The outer wire shape every application frame conforms to
([`docs/protocol-mobile.md` § Wire shapes](../../protocol-mobile.md#wire-shapes),
“Application envelope”).

```go
type Envelope struct {
    ID        uint64          `json:"id"`
    Type      string          `json:"type"`
    TS        time.Time       `json:"ts"`
    Payload   json.RawMessage `json:"payload"`
    InReplyTo *uint64         `json:"in_reply_to,omitempty"`

    // EventID — in-memory replay cursor, unique daemon-wide.
    EventID *uint64 `json:"event_id,omitempty"`

    // HistoryEntryID — durable per-conversation history entry id.
    HistoryEntryID *uint64 `json:"history_entry_id,omitempty"`

    // Live-state metadata, supplied only for thread-capable delivery.
    SessionID           json.RawMessage `json:"session_id,omitempty"`
    SessionStateCleared bool            `json:"session_state_cleared,omitempty"`

    PayloadEncrypted bool `json:"payload_encrypted,omitempty"`
}
```

- `TS` is `time.Time` (not `string`) — the dispatcher needs typed time for the binary's 7-day-back / 5-min-forward clock-skew cap (spec § Clock-skew handling) without re-parsing on every read. Marshals as RFC 3339 nano; round-trip caveat: `time.Time` carries a monotonic-clock reading stripped by JSON marshal, so tests compare via `time.Time.Equal`, never `==` or `reflect.DeepEqual` (per `docs/PROJECT-MEMORY.md:1071`).
- `Payload` is `json.RawMessage` to enable deferred decode: the dispatcher reads `Type` from the outer envelope, then unmarshals `Payload` into the per-type struct that `Type` selects. Also lets a malformed payload of a known type fail-loud at `protocol.malformed` with the offending envelope's `id` intact, instead of failing the outer parse.
- `InReplyTo`, `EventID`, `HistoryEntryID`, and `PayloadEncrypted` are `omitempty`. `payload_encrypted: false` MUST be omitted on the wire (the `envelope_full.json` fixture pins this).

## Replay cursors and durable read marks

`Envelope.ID` is the connection counter used for request/reply correlation; it
resets on reconnect. `Envelope.EventID` comes from `eventring.Ring.Append` and
is unique daemon-wide, ascending but potentially sparse within a conversation
(#2022). It survives phone reconnects and child respawns, but not daemon
restarts. The interactive structured-stream emitter stamps it so a returning
client can advertise `HelloClientPayload.LastEventID` for replay. See
[eventring](eventring-package.md) for retention and replay boundaries.

`Envelope.HistoryEntryID` identifies `HistoryEntry.ID`, the durable
per-conversation entry used by `MarkConversationReadPayload.UpTo`. Real entries
start at 1 and survive daemon restarts. **These namespaces cannot be joined by
numeric equality:** a ring id may belong to another conversation and resets
with the daemon, while a history id belongs to one persisted log. Using either
`ID` or `EventID` as a read-mark target can therefore mark the wrong position.
See [history payloads](protocol-package-types-history-payloads.md) and
the [read-mark contract](../../protocol-mobile.md#marking-a-conversation-read).

The three direct live producers attach the successful `Store.Append` result:
`interactiveTurnEmitterV2.emit`, `sessionTransitionEmitterV2.broadcast`, and
`newOperatorMessageHistory`'s commit handed to `operatorMessageEmitterV2.broadcast`
(#2861). The result is shared across recipients alongside the stored payload and
timestamp. Session transitions have no ring id; an operator push with no ring
still carries its history id. See [history producers](history-package-producers.md#producers-2114-2115)
for append placement and the operator handoff.

History-backed interactive-turn and operator-message ring events retain the
original successful append's id before publication (#2909), even with no live
recipient. `drainReplayOnce` restores a non-nil `Envelope.HistoryEntryID` only
from a nonzero retained `Event.HistoryEntryID`; authenticated, sealed replay
preserves that id, payload and timestamp and appends no new history. Channel-post
ring events still have no history id, and session transitions stay outside ring
replay. See [replay](v2-session-manager-state-machine-reconnect-replay-hello-last-event-id-rin.md).

Older daemons, absent or failed history storage, and non-history-backed frames
(live or replayed) omit the history key. Absent or failed storage still permits live
delivery and existing ring recording. Absence requires history/list fallback to
obtain a durable target: a history entry's `ID`, or
`ConversationSummary.LatestEntryID` when marking through the latest entry the
operator has read. The metadata conveys no authorization.

Both optional ids use `*uint64` with `omitempty`: nil omits the key entirely,
preserving legacy wire bytes. A non-nil pointer serializes its value, so the
struct declaration alone does not enforce the real-entry minimum of 1.
`TestEnvelope_HistoryEntryIDRoundTrip` checks raw key omission and preserves
distinct connection, ring and history ids through encode/decode/re-encode,
including the maximum uint64. `TestEnvelope_EventIDOmitempty` pins the replay
field's optional shape; `TestEnvelope_RoundTrip_Full` and
`TestEnvelope_RoundTrip_Minimal` pin nil history pointers and byte-identical
compacted legacy fixtures.

Check optional-key omission on the actual wire bytes. Decoding a missing key and
JSON `null` into `*uint64` produces nil in both cases; re-marshalling then erases
the distinction. `TestV2Session_Reconnect_HistoryEntryID` inspects the decrypted
authenticated replay JSON and compares durable id 8 with ring id 2 and a fresh
history read, so a missing key, `null`, zero or a substituted ring id cannot
satisfy the same assertions. See [protocol boundary tests](development-verification.md#protocol-boundaries).

## Live-state session metadata

`Envelope.SessionID` must preserve three states: omitted means metadata was not
supplied, JSON `null` positively means no producing session, and a nonempty JSON
string identifies the producer. A `*string` with `omitempty` would collapse
omitted and null into nil during decoding, then erase the explicit no-session
fact on re-encoding. `json.RawMessage` preserves both: nil omits the key, while
the bytes `null` emit a present null. Raw JSON does not validate the shape;
producers and consumers must enforce null or a nonempty string. This metadata
conveys no authorization and never replaces an existing payload session field.

`SessionStateCleared: true` with payload `{}` explicitly clears the normalized
family's session-scoped readings before fresh state. Ordinary updates omit the
flag. Delivery must
restrict these fields to thread-negotiated live state and enforce the empty
clear payload. Relay projection strips both keys from non-thread connections,
even if a supplied frame includes them, and omits explicit clear frames entirely. Legacy
producers retain their existing wire bytes. Relay-owned supplied-state delivery
and reconciliation are implemented (#3082); actual daemon provenance/retention
and provider installation/production activation remain pending #3076/#3077. See
[relay delivery](v2-session-manager-state-machine-capability-negotiation-on-the-handshake.md#thread-delivery-and-summary-projection),
[the wire contract](../../protocol-mobile.md#message-envelope) and
[ADR 042](../decisions/042-daemon-built-thread.md#decision).

Validate the exact generated clear as well as the fresh reading against
`MaxThreadEnvelopeBytes`, with JSON escaping and session metadata included.
Even a fresh `{}` reading exactly at the cap can produce an oversized clear:
`session_state_cleared: true` adds bytes. `validateLiveState` measures both
envelopes using the same `liveStateClear` family normalization as delivery.
Rejecting only at seal time can strand the drain pump if an undeliverable fresh
reading is retained behind its failed clear. The relay retains pending fresh
state only after the clear succeeds, so invalid queued/snapshot input cannot
block healthy readings. `TestLiveStateGeneratedClearBound` proves rejection,
subsequent delivery and pump exhaustion; `TestLiveStateClearFailure` proves no
pending state or watermark survives a failed clear. `TestLiveStateBounds` uses
`turnbridge.MapEvent` near-cap slash-command payloads and worst JSON escaping,
so producer budget changes cannot hide behind a hand-built small fixture.
Ordinary live state does not use the thread-item continuation codec.

`TestEnvelopeSessionMetadataRoundTrip` decodes omitted/null/string metadata,
re-marshals decoded ordinary or empty-clear payloads into their envelopes, and
inspects emitted keys. Distinct envelope and payload session IDs prove that
adding metadata does not overwrite the payload's own field. Checking only a
decoded pointer, or reusing untouched raw payload bytes, would miss these
regressions; see [protocol boundaries](development-verification.md#protocol-boundaries).

## Native HTTP bridge semantics

The [hosted-app contract](../../hosted-apps.md#client-resource-bridge) is defined
ahead of hosting implementation. Android `WebResourceRequest` exposes no request
body, and `WebResourceResponse` rejects 3xx statuses. Resource interception alone
would silently lose POST bodies or fail redirect responses. Electron and Android
therefore need a narrow body-capable Fetch adapter alongside resource loaders:
Fetch returns API status 200–599 without following redirects or exposing
`location`; resource loaders accept only 200–299/400–599. Reject raw non-GET
resource traffic before dispatch so bypassing the adapter cannot discard a body.
See the [Android request interface](https://developer.android.com/reference/android/webkit/WebResourceRequest)
and [response restrictions](https://developer.android.com/reference/android/webkit/WebResourceResponse).

Connection-local `Envelope.ID` cannot identify app work across reconnects.
Pending tables must include connection/view generation and the bound app/release;
daemon completions also belong to a process generation. Retire partial assembly
on disconnect, host switch or release replacement, discard late replies, and
refresh discovery before fresh reads. An abort cannot establish whether a write
committed, so even a retryable bridge error cannot authorize automatic mutation
replay. Hosted frames omit conversation/session/replay metadata and rely on Noise
for encryption, not `PayloadEncrypted`. The
[shared fixtures](../../specs/architecture/3120-hosted-app-contract.md#10-shared-json-examples)
belong to the design; executable daemon/native conformance remains downstream.

## Testing payload absence

An empty, non-nil `json.RawMessage` fails envelope marshaling before a relay
handler sees it. Use nil for a test request with no payload body, as
`TestV2Session_StopBackgroundTask_Contract` does (#2791). Because `Envelope.Payload`
has no `omitempty`, nil marshals as `"payload":null`; testing a literally omitted
key requires raw envelope bytes. Valid JSON with the wrong typed shape exercises
the handler's decode boundary; invalid raw JSON instead fails envelope marshaling.
