# Handshake / control payloads (#271)

Five DTOs that slot into `Envelope.Payload (json.RawMessage)` once the dispatcher reads `Envelope.Type`. Pure data — no methods, no constructors, no validation. Spec source: `docs/protocol-mobile.md` § Message types — `hello`, `hello_ack`, `error`, `ack`.

```go
type HelloServerPayload struct {
    Role             string   `json:"role"` // always "server"
    ServerID         string   `json:"server_id"`
    BinaryVersion    string   `json:"binary_version"`
    ProtocolVersions []string `json:"protocol_versions"`
}

type HelloClientPayload struct {
    Role             string     `json:"role"` // always "client"
    DeviceName       string     `json:"device_name"`
    ClientVersion    string     `json:"client_version"`
    ProtocolVersions []string   `json:"protocol_versions"`
    LastSeenTS       *time.Time `json:"last_seen_ts,omitempty"`
    Token            string     `json:"token,omitempty"`        // #308; in-band device-pairing token under v2 (plaintext — MUST NOT be logged)
    Capabilities     []string   `json:"capabilities,omitempty"` // #607; phone's advertised feature set, e.g. [CapabilityInteractive]
    LastEventID      *uint64    `json:"last_event_id,omitempty"` // #647; durable event_id the phone last saw, for mid-turn reconnect replay (untrusted — consumer range/ring-bounds it)
}

type HelloAckPayload struct {
    ProtocolVersion string   `json:"protocol_version"`
    ServerID        string   `json:"server_id"`
    ConnID          string   `json:"conn_id"`
    Capabilities    []string `json:"capabilities,omitempty"` // #607; daemon's supported feature set (intersection with the phone's claim — enforced in #608)
}

type ErrorPayload struct {
    Code        string `json:"code"`
    Message     string `json:"message"`
    Retryable   bool   `json:"retryable"`
    RetryAfterS *int   `json:"retry_after_s,omitempty"`
}

type AckPayload struct{}
```

Conventions:

- **Two `Hello*Payload` structs, not a union.** The binary's hello and the phone's hello share only the envelope type name (`"hello"`) and dispatch site; field sets diverge. `role` is the discriminator. Modelling as a single struct with mostly-optional fields would lose type-level encoding of which fields belong with which role and force every consumer to validate role-field consistency by hand.
- **Optional fields are `*T` + `omitempty`; required fields are non-pointer.** Only `LastSeenTS` and `RetryAfterS` carry `omitempty`. `time.Time` zero-value as sentinel for `LastSeenTS` was rejected — `time.Time{}` marshals as `"0001-01-01T00:00:00Z"`, which would pollute the wire.
- **`AckPayload` is `struct{}`.** `json.Marshal(AckPayload{})` emits `{}` byte-for-byte, matching the spec's `"payload": {}`.
- **Field declaration order matches the spec example order.** The JSON encoder emits fields in struct-declaration order; that's what the round-trip byte-equivalence check verifies. Reordering breaks tests.
- **No constructors, no methods, no validation.** Runtime enforcement of `Role` discriminators (a phone sending `role: "server"`, etc.) is the dispatcher's concern (#248–#250). The `Role` constant is documented in struct comments only.
- **`Capabilities []string` is additive + `omitempty` (#607).** Both phone-facing hello payloads gained it: the phone advertises its understood features in `hello`, the daemon echoes its supported set in `hello_ack`. `omitempty` is the byte-identical lever — a nil/empty slice drops the key (absent, not `null`), so the unedited `hello_client.json` / `hello_ack.json` fixtures round-trip byte-identically (same precedent as `RoutingEnvelope.Token`). Two values are defined: `CapabilityInteractive = "interactive"` and, since [#2020](../decisions/037-capability-strings-not-version-numbers.md), `CapabilityQuestion = "question"` (both wire-vocabulary constants live in `handshake.go` next to the field). This is **advertisement only** — the daemon intersecting the phone's claimed set with its own (echoing only what *it* supports, never blindly mirroring) and any capability-gated fan-out are the consumer's trust decision (#608), not this layer's; `CapabilityQuestion` in particular gates nothing itself; it is pure client-side build detection (ADR 037). `TestHelloClientPayload_CapabilitiesRoundTrip` / `TestHelloAckPayload_CapabilitiesRoundTrip` pin both the round-trip and the omit shape; the pre-existing fixture round-trips stay unchanged as the byte-stability regression guard. `TestCapability_Constants_MatchSpec` (#2020, mirroring `TestErrorCode_Constants_MatchSpec`) pins each `Capability*` constant's exact string against the spec — added because every capability-negotiation test in `internal/relay` passes the constant symbolically on both the advertise and the expect side, so a fat-fingered wire value would otherwise be self-consistent and green everywhere; see [drift detectors](protocol-package-drift-detectors.md).
- **`LastEventID *uint64` is additive + `omitempty` (#647).** The phone's inbound reconnect-replay cursor: the durable `event_id` (the `Envelope.EventID` #649 surfaces outbound) it last saw, advertised on mid-turn reconnect so the daemon replays the missed tail from the `internal/eventring` ring or emits a `resync` marker. **Pointer + `omitempty` is load-bearing** — ring ids are always ≥ 1, so a non-nil pointer never encodes `0` and a nil pointer is omitted; a phone advertising none keeps the v1 hello byte-identical (key absent, not `null`). Same shape as `LastSeenTS`. This wire-type layer does **no enforcement** — `LastEventID` is **untrusted remote input**, and the consumer (`internal/relay`, #647, `security-sensitive`) range/shape-validates it and bounds replay by the ring. `TestHelloClientPayload_LastEventIDRoundTrip` pins the omit/round-trip shape. **Implementation caveat:** the #647 daemon consumer carries an unresolved code-review MUST FIX and is not yet merged — see [codebase/647.md](../codebase/647.md). The wire field itself is stable.

Five fixture files under `testdata/` (one per type, each a complete `Envelope` with the payload inlined) drive five per-type `*_RoundTrip` tests in `handshake_test.go`. The tests reuse `readFixture` and `canonical` helpers from `envelope_test.go`. The byte-equivalence check (`canonical(out) == canonical(raw)`) is the load-bearing assertion; per-type field asserts exist to localise failure messages. The `hello_client.json` fixture's `last_seen_ts: "2026-05-08T08:14:02Z"` (no fractional seconds) pins the `time.RFC3339Nano` no-fractional round-trip behaviour.

Sibling payload slices not yet landed: messaging (`send_message` / `message`), conversations (`list_conversations` / `conversations` / `create_conversation` / `conversation_created` / `promote_conversation` / `conversation_updated`), push (`register_push_token`).
