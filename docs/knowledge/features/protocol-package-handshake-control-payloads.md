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
    ClientVersion    string     `json:"client_version"` // free text on the wire; #2576 defines, and #2578's ParseClientVersion (client_version.go) parses, the <app>/<MAJOR>.<MINOR>.<PATCH> format — not enforced by this struct itself
    ClientFeatures   string     `json:"client_features,omitempty"` // optional untrusted plain-text self-report in v2; no consumer yet (#2898)
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
    WorkspaceRoot   string   `json:"workspace_root,omitempty"` // daemon host's absolute workspace base; admitted peers only
}

type ErrorPayload struct {
    Code             string `json:"code"`
    Message          string `json:"message"`
    Retryable        bool   `json:"retryable"`
    RetryAfterS      *int   `json:"retry_after_s,omitempty"`
    ConversationID   string `json:"conversation_id,omitempty"`   // #2443
    MinClientVersion string `json:"min_client_version,omitempty"` // #2576
}

type AckPayload struct{}
```

Conventions:

- **Two `Hello*Payload` structs, not a union.** The binary's hello and the phone's hello share only the envelope type name (`"hello"`) and dispatch site; field sets diverge. `role` is the discriminator. Modelling as a single struct with mostly-optional fields would lose type-level encoding of which fields belong with which role and force every consumer to validate role-field consistency by hand.
- **Optional fields use `omitempty`; required fields are non-pointer.** A pointer preserves a meaningful zero value (`LastSeenTS`, `LastEventID`, `RetryAfterS`); a string or slice uses its empty value when empty and absent mean the same thing (`ClientFeatures`, `Token`, `Capabilities`, `WorkspaceRoot`). `time.Time` zero-value as sentinel for `LastSeenTS` was rejected — `time.Time{}` marshals as `"0001-01-01T00:00:00Z"`, which would pollute the wire.
- **`AckPayload` is `struct{}`.** `json.Marshal(AckPayload{})` emits `{}` byte-for-byte, matching the spec's `"payload": {}`.
- **Field declaration order matches the spec example order.** The JSON encoder emits fields in struct-declaration order; that's what the round-trip byte-equivalence check verifies. Reordering breaks tests.
- **No constructors, no methods, no validation.** Runtime enforcement of `Role` discriminators (a phone sending `role: "server"`, etc.) is the dispatcher's concern (#248–#250). The `Role` constant is documented in struct comments only.
- **`ClientFeatures string` is additive within v2 + `omitempty`.** It is an
  optional, untrusted plain-text self-report, unrelated to negotiated
  `Capabilities`. Absent and empty decode to an empty string and marshal with
  the key omitted, keeping the legacy hello fixtures byte-identical. Nonempty
  decoded strings round-trip verbatim, including whitespace and control
  characters, with no trimming, content validation or new handshake rejection.
  Accepting this DTO field does not admit it into a prompt: relay retention and
  prompt admission and attributed rendering remain pending
  [#2898](https://github.com/pyrycode/pyrycode/issues/2898). See the
  [wire contract](../../protocol-mobile.md#hello-v2-specific-note).
- **`Capabilities []string` is additive + `omitempty` (#607).** Both phone-facing hello payloads gained it: the phone advertises its understood features in `hello`, the daemon echoes its supported set in `hello_ack`. `omitempty` is the byte-identical lever — a nil/empty slice drops the key (absent, not `null`), so the unedited `hello_client.json` / `hello_ack.json` fixtures round-trip byte-identically (same precedent as `RoutingEnvelope.Token`). Six values are defined: `CapabilityInteractive = "interactive"`, `CapabilityQuestion = "question"` (since [#2020](../decisions/037-capability-strings-not-version-numbers.md)), `CapabilityModelList = "model_list"` (since #2172, detecting the pair of #2124's daemon-wide model-list fallback and #2125's `request_model_list` verb — one string for both, since neither is separately useful without the other), `CapabilityContextUsage = "context_usage"` (since #2431, detecting the `request_context_usage` verb), `CapabilityMultiAgent = "multi_agent"` (since #2643), and `CapabilityStopBackgroundTask = "stop_background_task"` (since #2797) (all six wire-vocabulary constants live in `handshake.go` next to the field). `CapabilityContextUsage` follows `CapabilityQuestion`'s posture rather than `CapabilityModelList`'s: it is pure detection with no fallback half to pair with, since the verb gates on `CapabilityInteractive` alone — a client advertising `context_usage` without `interactive` negotiates as non-interactive and the verb is unreachable, exactly the case `CapabilityQuestion`'s own entry records. **`CapabilityMultiAgent` breaks that pattern: it is the first capability that is not pure detection.** Its neighbours only tell a client which daemon build it is talking to; this one changes what the daemon actually sends — a negotiating client reads `ConversationSummary.Agent` on every row of a `conversations` reply, and a non-negotiating client is never sent a Codex-run conversation at all (see [conversations-read payloads](protocol-package-types-conversations-read-payloads.md)). It still grants no *interactive* access — every interactive gate reads `CapabilityInteractive` alone, unaffected by this string — so "detection only, grants no access" is true of the interactive surface specifically, not of the wire shape as a whole. This is **advertisement only** — the daemon intersecting the phone's claimed set with its own (echoing only what *it* supports, never blindly mirroring) and any capability-gated fan-out or reply-shaping are the consumer's trust decision (#608, #2643), not this layer's; `CapabilityQuestion` in particular gates nothing itself; it is pure client-side build detection (ADR 037). `TestHelloClientPayload_CapabilitiesRoundTrip` / `TestHelloAckPayload_CapabilitiesRoundTrip` pin both the round-trip and the omit shape; the pre-existing fixture round-trips stay unchanged as the byte-stability regression guard. `TestCapability_Constants_MatchSpec` (#2020, mirroring `TestErrorCode_Constants_MatchSpec`) pins each `Capability*` constant's exact string against the spec — added because negotiation tests using constants symbolically on both the advertise and the expect side cannot catch a fat-fingered wire value; #2797 also uses the literal `"stop_background_task"` directly in both negotiation tables; see [drift detectors](protocol-package-drift-detectors.md).

  `stop_background_task` is detection only: clients draw a per-task stop button
  only when `hello_ack` echoes the advertised string through intersection
  negotiation. It grants no interactive access and adds no verb gate; an
  interactive client can stop a task without advertising it. Keep detection
  separate from authorization so a supported feature remains usable by existing
  interactive clients. See the [stop wire contract](../../protocol-mobile.md#stop-background-task-v2)
  and [relay negotiation](v2-session-manager-state-machine-capability-negotiation-on-the-handshake.md).

- **`WorkspaceRoot string` is additive + `omitempty`.** It reports the daemon
  host's absolute workspace base so a client can resolve relative workspace
  previews; advertisement grants no filesystem access and does not assert that
  the directory exists. The manager's generic `V2SessionConfig.WorkspaceBase`
  contract distinguishes unsupplied from explicitly empty: nil uses
  `WorkspaceRoot()`'s lexical `$HOME/pyry-workspace` default (empty without an
  available absolute HOME); a supplied absolute value is advertised verbatim,
  independently of HOME; a supplied empty or relative value omits the key
  without falling back. The caller owns resolution and must keep the pointed-to
  string immutable while the manager runs. Handshake selection performs no
  directory inspection, creation or symlink resolution.

  The production daemon supplies one startup-resolved value (#2761): the
  canonical service process cwd when absolute, resolvable and within canonical
  HOME, including HOME itself; otherwise the lexical `$HOME/pyry-workspace`
  fallback, which need not exist. Without an available absolute HOME it supplies
  an explicit empty value regardless of cwd. `--pyry-workdir` controls Claude's
  spawn folder independently. Startup resolution creates and trust-marks nothing
  and shares its immutable result with the [one-time seed](conversations-registry.md).

  An empty value omits the key, preserving the legacy fixture. This DTO cannot
  enforce the trust boundary itself:
  [`handleNoiseInit`](v2-session-manager-state-machine-noise-init-happy-and-failure-path.md)
  populates it only after device-token validation, client-version admission and
  atomic static-key binding succeed. Every rejected peer gets an ack without
  the key even with a supplied absolute base, because a rejected peer can decrypt
  the `hello_ack` used to establish the encrypted rejection channel. The value
  travels only inside the encrypted Noise response, never in unencrypted
  routing fields or daemon logs. `TestHelloAckPayload_WorkspaceRootRoundTrip`
  pins the wire omit/round-trip shape; relay handshake tests own authorization,
  encryption-only transport, no-creation and no-log proofs. See the
  [wire contract](../../protocol-mobile.md#hello_ack-v2-specific-note).
- **`LastSeenTS *time.Time` is decoded and has no consumer.** It reads as a peer
  of `LastEventID` below — same `*T` + `omitempty` shape, same struct — but
  nothing in `internal/relay` or elsewhere reads the decoded value: a `hello`
  carrying it behaves byte-for-byte like one omitting it. It is kept as
  accepted wire vocabulary rather than removed (`docs/protocol-mobile.md` §
  Reconnect / Backfill semantics once documented it as driving a v1 bulk-history
  backfill; it never did — see that section's Changelog, #2090). A reconnecting
  phone that wants the tail it missed sends `LastEventID` instead. Whether real
  history should exist, and what would source it, is #2091's decision.
- **`ErrorPayload.ConversationID` is additive + `omitempty` (#2443).**
  `new_session`'s bare form rotates the daemon's cursor conversation, so its
  workspace-refusal reply supplies the daemon-resolved subject that
  `in_reply_to` alone cannot identify. Other producers must retain that
  daemon-authored provenance rather than echo a client lookup key.

  **`stop_background_task.refused` is the narrow correlation exception (#2791):**
  it carries the requested nonempty conversation id even when a missing task id
  is refused before conversation resolution. Gating this refusal on registry
  membership would turn it into a conversation-membership probe. The id
  therefore asserts neither membership nor task existence and must never be
  logged. `in_reply_to` still identifies the request; no task id or child
  diagnostic is reflected. Other error replies omit the field, preserving their
  earlier wire shape. See the
  [stop contract](../../protocol-mobile.md#stop-background-task-v2).
- **`ErrorPayload.MinClientVersion` is additive + `omitempty` (#2576), and carries `CodeClientUpdateRequired`'s minimum version alone — never any other error's.** `omitempty` keeps every existing error reply byte-identical. Same never-echo discipline as `ConversationID` above: it is the daemon's configured minimum for the requesting app, not a reflection of the client's own `client_version`. It is omitted on the same reject when the `hello`'s `client_version` could not be parsed at all, because the daemon then has no app name to look a minimum up by — see [protocol-package-constants-codes-go-error-codes-21.md](protocol-package-constants-codes-go-error-codes-21.md) and `docs/protocol-mobile.md` § Compatibility. The sender is `internal/relay`'s `handleNoiseInit`/`checkClientVersion` (#2578) — see [`v2-session-manager-state-machine-noise-init-happy-and-failure-path.md` § The client-version reject arm](v2-session-manager-state-machine-noise-init-happy-and-failure-path.md#the-client-version-reject-arm-2578).
- **`LastEventID *uint64` is additive + `omitempty` (#647).** The phone's inbound reconnect-replay cursor: the durable `event_id` (the `Envelope.EventID` #649 surfaces outbound) it last saw, advertised on mid-turn reconnect so the daemon replays the missed tail from the `internal/eventring` ring or emits a `resync` marker. **Pointer + `omitempty` is load-bearing** — ring ids are always ≥ 1, so a non-nil pointer never encodes `0` and a nil pointer is omitted; a phone advertising none keeps the v1 hello byte-identical (key absent, not `null`). Same shape as `LastSeenTS`. This wire-type layer does **no enforcement** — `LastEventID` is **untrusted remote input**, and the consumer (`internal/relay`, #647, `security-sensitive`) range/shape-validates it and bounds replay by the ring. `TestHelloClientPayload_LastEventIDRoundTrip` pins the omit/round-trip shape. **Implementation caveat:** the #647 daemon consumer carries an unresolved code-review MUST FIX and is not yet merged — see [codebase/647.md](../codebase/647.md). The wire field itself is stable.

Five fixture files under `testdata/` (one per type, each a complete `Envelope` with the payload inlined) drive five per-type `*_RoundTrip` tests in `handshake_test.go`. The tests reuse `readFixture` and `canonical` helpers from `envelope_test.go`. The byte-equivalence check (`canonical(out) == canonical(raw)`) is the load-bearing assertion; per-type field asserts exist to localise failure messages. The `hello_client.json` fixture's `last_seen_ts: "2026-05-08T08:14:02Z"` (no fractional seconds) pins the `time.RFC3339Nano` no-fractional round-trip behaviour.

`TestHelloClientPayload_ClientFeaturesRoundTrip` compares the complete decoded
DTO, checks the marshalled key's omission or verbatim value, and decodes it again;
`Capabilities` is asserted independently of the description. Use generic NUL and
BEL values to prove control-character preservation in a DTO test. A terminal
colour escape literal can pass Go tests while failing `substrate-guard`, because
terminal substrate vocabulary is restricted to the TUI driver. See
[protocol verification](development-verification.md#protocol-boundaries).

Sibling payload slices not yet landed: messaging (`send_message` / `message`), conversations (`list_conversations` / `conversations` / `create_conversation` / `conversation_created` / `promote_conversation` / `conversation_updated`), push (`register_push_token`).
