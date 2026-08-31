# `Envelope`

The outer wire shape every application frame conforms to (`docs/protocol-mobile.md` § Message envelope, lines 177–201). Field order matches the spec table verbatim.

```go
type Envelope struct {
    ID        uint64          `json:"id"`
    Type      string          `json:"type"`
    TS        time.Time       `json:"ts"`
    Payload   json.RawMessage `json:"payload"`
    InReplyTo *uint64         `json:"in_reply_to,omitempty"`

    // EventID — durable per-conversation event id (eventring); #649.
    EventID *uint64 `json:"event_id,omitempty"`

    PayloadEncrypted bool `json:"payload_encrypted,omitempty"`
}
```

- `TS` is `time.Time` (not `string`) — the dispatcher needs typed time for the binary's 7-day-back / 5-min-forward clock-skew cap (spec § Clock-skew handling) without re-parsing on every read. Marshals as RFC 3339 nano; round-trip caveat: `time.Time` carries a monotonic-clock reading stripped by JSON marshal, so tests compare via `time.Time.Equal`, never `==` or `reflect.DeepEqual` (per `docs/PROJECT-MEMORY.md:1071`).
- `Payload` is `json.RawMessage` to enable deferred decode: the dispatcher reads `Type` from the outer envelope, then unmarshals `Payload` into the per-type struct that `Type` selects. Also lets a malformed payload of a known type fail-loud at `protocol.malformed` with the offending envelope's `id` intact, instead of failing the outer parse.
- `InReplyTo`, `EventID`, and `PayloadEncrypted` are `omitempty`. `payload_encrypted: false` MUST be omitted on the wire (the `envelope_full.json` fixture pins this).
- **`EventID *uint64` (#649)** is the durable, per-conversation event id from the `internal/eventring` ring (`eventring.Ring.Append`'s return) — distinct from `ID`, the per-conn envelope counter that resets each reconnect. It is stamped **only** by the interactive structured-stream emitter (`cmd/pyry/interactive_turn_v2.go`'s `emit`), so a reconnecting phone can advertise the latest one it saw as `last_event_id` (`HelloClientPayload.LastEventID`, #647); the inbound consumer that accepts and replays from it is sibling #647 (`security-sensitive`; daemon code not yet merged — see [codebase/647.md](../codebase/647.md)). **Pointer + `omitempty`, mirroring `InReplyTo` exactly:** every other `Envelope{...}` construction site (v1 messaging, dispatch, non-interactive) leaves it nil → omitted → byte-identical wire ("absent, not null/0"). Ring ids are always ≥ 1, so a non-nil pointer never encodes `0`. `TestEnvelope_EventIDOmitempty` pins the omit/round-trip shape; the unchanged `envelope_full.json` / `envelope_minimal.json` fixtures are the byte-stability regression guard. See [codebase/649.md](../codebase/649.md) and [eventring-package.md](eventring-package.md).
