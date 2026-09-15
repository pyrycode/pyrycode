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
    LastSeenTS       *time.Time `json:"last_seen_ts,omitempty"` // decoded, no consumer (#2090)
    Token            string     `json:"token,omitempty"`        // #308; in-band device-pairing token under v2 (plaintext — MUST NOT be logged)
    Capabilities     []string   `json:"capabilities,omitempty"` // #607; phone's advertised feature set, e.g. [CapabilityInteractive]
    LastEventID      *uint64    `json:"last_event_id,omitempty"` // #647; durable event_id the phone last saw, for mid-turn reconnect replay (untrusted — consumer range/ring-bounds it)
}

type HelloAckPayload struct {
    ProtocolVersion string   `json:"protocol_version"`
    ServerID        string   `json:"server_id"`
    ConnID          string   `json:"conn_id"`
    Capabilities    []string `json:"capabilities,omitempty"` // #607; daemon's supported feature set (intersection with the phone's claim — enforced in #608)
    WorkspaceRoot   string   `json:"workspace_root,omitempty"` // daemon host's absolute ~/pyry-workspace base; authorised peers only
}

type ErrorPayload struct {
    Code           string `json:"code"`
    Message        string `json:"message"`
    Retryable      bool   `json:"retryable"`
    RetryAfterS    *int   `json:"retry_after_s,omitempty"`
    ConversationID string `json:"conversation_id,omitempty"` // #2443
}

type AckPayload struct{}
```

Conventions:

- **Two `Hello*Payload` structs, not a union.** The binary's hello and the phone's hello share only the envelope type name (`"hello"`) and dispatch site; field sets diverge. `role` is the discriminator. Modelling as a single struct with mostly-optional fields would lose type-level encoding of which fields belong with which role and force every consumer to validate role-field consistency by hand.
- **Optional fields use `omitempty`; required fields are non-pointer.** A pointer preserves a meaningful zero value (`LastSeenTS`, `LastEventID`, `RetryAfterS`); a string or slice uses its empty value when empty and absent mean the same thing (`Token`, `Capabilities`, `WorkspaceRoot`). `time.Time` zero-value as sentinel for `LastSeenTS` was rejected — `time.Time{}` marshals as `"0001-01-01T00:00:00Z"`, which would pollute the wire.
- **`AckPayload` is `struct{}`.** `json.Marshal(AckPayload{})` emits `{}` byte-for-byte, matching the spec's `"payload": {}`.
- **Field declaration order matches the spec example order.** The JSON encoder emits fields in struct-declaration order; that's what the round-trip byte-equivalence check verifies. Reordering breaks tests.
- **No constructors, no methods, no validation.** Runtime enforcement of `Role` discriminators (a phone sending `role: "server"`, etc.) is the dispatcher's concern (#248–#250). The `Role` constant is documented in struct comments only.
- **`Capabilities []string` is additive + `omitempty` (#607).** Both phone-facing hello payloads gained it: the phone advertises its understood features in `hello`, the daemon echoes its supported set in `hello_ack`. `omitempty` is the byte-identical lever — a nil/empty slice drops the key (absent, not `null`), so the unedited `hello_client.json` / `hello_ack.json` fixtures round-trip byte-identically (same precedent as `RoutingEnvelope.Token`). Four values are defined: `CapabilityInteractive = "interactive"`, `CapabilityQuestion = "question"` (since [#2020](../decisions/037-capability-strings-not-version-numbers.md)), `CapabilityModelList = "model_list"` (since #2172, detecting the pair of #2124's daemon-wide model-list fallback and #2125's `request_model_list` verb — one string for both, since neither is separately useful without the other), and `CapabilityContextUsage = "context_usage"` (since #2431, detecting the `request_context_usage` verb) (all four wire-vocabulary constants live in `handshake.go` next to the field). `CapabilityContextUsage` follows `CapabilityQuestion`'s posture rather than `CapabilityModelList`'s: it is pure detection with no fallback half to pair with, since the verb gates on `CapabilityInteractive` alone — a client advertising `context_usage` without `interactive` negotiates as non-interactive and the verb is unreachable, exactly the case `CapabilityQuestion`'s own entry records. This is **advertisement only** — the daemon intersecting the phone's claimed set with its own (echoing only what *it* supports, never blindly mirroring) and any capability-gated fan-out are the consumer's trust decision (#608), not this layer's; `CapabilityQuestion` in particular gates nothing itself; it is pure client-side build detection (ADR 037). `TestHelloClientPayload_CapabilitiesRoundTrip` / `TestHelloAckPayload_CapabilitiesRoundTrip` pin both the round-trip and the omit shape; the pre-existing fixture round-trips stay unchanged as the byte-stability regression guard. `TestCapability_Constants_MatchSpec` (#2020, mirroring `TestErrorCode_Constants_MatchSpec`) pins each `Capability*` constant's exact string against the spec — added because every capability-negotiation test in `internal/relay` passes the constant symbolically on both the advertise and the expect side, so a fat-fingered wire value would otherwise be self-consistent and green everywhere; see [drift detectors](protocol-package-drift-detectors.md).
- **`WorkspaceRoot string` is additive + `omitempty`.** It reports the daemon host's absolute `~/pyry-workspace/` base so a client can resolve relative workspace previews; it does not assert that the directory exists and does not create or inspect it. An empty value omits the key, preserving the legacy fixture and covering both home-resolution failure and rejected device-token authorization. This DTO cannot enforce the trust boundary itself: [`handleNoiseInit`](v2-session-manager-state-machine-noise-init-happy-and-failure-path.md) must populate it only after `devices.Registry.Validate` accepts the peer, because a rejected peer can decrypt the `hello_ack` used to establish the encrypted rejection channel. `TestHelloAckPayload_WorkspaceRootRoundTrip` pins the wire omit/round-trip shape; the relay handshake tests own authorization, encryption-only transport, no-creation and no-log proofs.
- **`LastSeenTS *time.Time` is decoded and has no consumer.** It reads as a peer
  of `LastEventID` below — same `*T` + `omitempty` shape, same struct — but
  nothing in `internal/relay` or elsewhere reads the decoded value: a `hello`
  carrying it behaves byte-for-byte like one omitting it. It is kept as
  accepted wire vocabulary rather than removed (`docs/protocol-mobile.md` §
  Reconnect / Backfill semantics once documented it as driving a v1 bulk-history
  backfill; it never did — see that section's Changelog, #2090). A reconnecting
  phone that wants the tail it missed sends `LastEventID` instead. Whether real
  history should exist, and what would source it, is #2091's decision.
- **`ErrorPayload.ConversationID` is additive + `omitempty` (#2443), and it names the conversation an error is ABOUT rather than the request it answers.** Every existing error reply already lets the client infer its subject from `in_reply_to` (the client sent the request, so it knows what it named) and omits the field, keeping their wire shape byte-identical to the pre-#2443 one. It exists for the one reply that can't: `new_session`'s bare (unnamed) form rotates the daemon's own cursor conversation, so only the daemon — which resolved that cursor — knows which conversation a refusal reply is about. **SECURITY, stated in the field's own doc comment: the value is DAEMON-AUTHORED and MUST NOT be an echo of a client-supplied id** — the same discipline `V2SessionConfig.RunConfigFor` / `ModelListFor` already state for their own reported ids. A future producer that reaches for this field to save itself a lookup, rather than to name a subject `in_reply_to` genuinely cannot, would be misusing it.
- **`LastEventID *uint64` is additive + `omitempty` (#647).** The phone's inbound reconnect-replay cursor: the durable `event_id` (the `Envelope.EventID` #649 surfaces outbound) it last saw, advertised on mid-turn reconnect so the daemon replays the missed tail from the `internal/eventring` ring or emits a `resync` marker. **Pointer + `omitempty` is load-bearing** — ring ids are always ≥ 1, so a non-nil pointer never encodes `0` and a nil pointer is omitted; a phone advertising none keeps the v1 hello byte-identical (key absent, not `null`). Same shape as `LastSeenTS`. This wire-type layer does **no enforcement** — `LastEventID` is **untrusted remote input**, and the consumer (`internal/relay`, #647, `security-sensitive`) range/shape-validates it and bounds replay by the ring. `TestHelloClientPayload_LastEventIDRoundTrip` pins the omit/round-trip shape. **Implementation caveat:** the #647 daemon consumer carries an unresolved code-review MUST FIX and is not yet merged — see [codebase/647.md](../codebase/647.md). The wire field itself is stable.

Five fixture files under `testdata/` (one per type, each a complete `Envelope` with the payload inlined) drive five per-type `*_RoundTrip` tests in `handshake_test.go`. The tests reuse `readFixture` and `canonical` helpers from `envelope_test.go`. The byte-equivalence check (`canonical(out) == canonical(raw)`) is the load-bearing assertion; per-type field asserts exist to localise failure messages. The `hello_client.json` fixture's `last_seen_ts: "2026-05-08T08:14:02Z"` (no fractional seconds) pins the `time.RFC3339Nano` no-fractional round-trip behaviour.

Sibling payload slices not yet landed: messaging (`send_message` / `message`), conversations (`list_conversations` / `conversations` / `create_conversation` / `conversation_created` / `promote_conversation` / `conversation_updated`), push (`register_push_token`).
