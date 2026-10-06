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

The optional history field is declared by #2860; **emission awaits
[#2861](https://github.com/pyrycode/pyrycode/issues/2861)**. Absence requires
history/list fallback to obtain a durable target: a history entry's `ID`, or
`ConversationSummary.LatestEntryID` when marking through the latest entry the
operator has read. Declaring metadata alone establishes no producer provenance
or authorization.

Both optional ids use `*uint64` with `omitempty`: nil omits the key entirely,
preserving legacy wire bytes. A non-nil pointer serializes its value, so the
struct declaration alone does not enforce the real-entry minimum of 1.
`TestEnvelope_HistoryEntryIDRoundTrip` checks raw key omission and preserves
distinct connection, ring and history ids through encode/decode/re-encode,
including the maximum uint64. `TestEnvelope_EventIDOmitempty` pins the replay
field's optional shape; `TestEnvelope_RoundTrip_Full` and
`TestEnvelope_RoundTrip_Minimal` pin nil history pointers and byte-identical
compacted legacy fixtures.

## Testing payload absence

An empty, non-nil `json.RawMessage` fails envelope marshaling before a relay
handler sees it. Use nil for a test request with no payload body, as
`TestV2Session_StopBackgroundTask_Contract` does (#2791). Because `Envelope.Payload`
has no `omitempty`, nil marshals as `"payload":null`; testing a literally omitted
key requires raw envelope bytes. Valid JSON with the wrong typed shape exercises
the handler's decode boundary; invalid raw JSON instead fails envelope marshaling.
