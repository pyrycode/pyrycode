# 255 — `net`: protocol envelope, routing, error codes, v1-compat predicate

## Files to read first

- `docs/protocol-mobile.md:177-201` — § Message envelope. The wire shape that `Envelope` mirrors field-for-field. `id`, `type`, `ts`, `payload` are required; `in_reply_to`, `payload_encrypted` are optional with `omitempty`. Locks the JSON tag names — snake_case, no abbreviation.
- `docs/protocol-mobile.md:100-122` — § Routing envelope. `RoutingEnvelope` is the relay-prepended `{conn_id, frame}` wrapper for binary↔relay direction. Phones never see it. `frame` is `json.RawMessage` so the relay can splice without parsing payloads.
- `docs/protocol-mobile.md:525-542` — § Error codes table. Twelve dotted-string codes; this ticket exports them as `Code*` constants. Pin-for-pin; no extras, no omissions.
- `docs/protocol-mobile.md:684-699` — § Reserved for v2. The `payload_encrypted: true` rule that drives the `IsV1Compatible` rejection. Restated here so the test fixtures match the spec wording.
- `docs/protocol-mobile.md:203-499` — § Message types. The 16 v1 type discriminators that populate `types.go` constants. This ticket consumes only the type-name strings; payload struct shapes are #256.
- `internal/conversations/registry.go:22-29` — sentinel-error declaration shape (`var ( ErrFoo = errors.New("conversations: foo") )`). The `IsV1Compatible` sentinels follow this idiom verbatim, swapping the package prefix.
- `docs/PROJECT-MEMORY.md:60` — "Refusal-to-wire-code mapping is the consumer's job." Pins why this package exports sentinels (`ErrUnknownType`, `ErrUnsupported`) instead of dotted-string-returning functions: the package surface stays free of wire-format coupling, and the dispatcher (a future ticket) does the `errors.Is → CodeProtocolUnknownType` translation at the WS edge.
- `docs/specs/architecture/243-conv-daemon-wiring.md` — most recent architect spec; structural template for "Files to read first / Context / Design / Concurrency / Error handling / Testing strategy / Out of scope" headings used here.
- `CODING-STYLE.md` — `gofmt` non-negotiable, stdlib-only, sentinel errors via `errors.New`, table-driven tests. This spec inherits all four constraints; not restated below.

## Context

Phase 3 Track C foundation. PR #188 (wire protocol v1 draft) merged 2026-05-08; spec is `docs/protocol-mobile.md`. This ticket lands the framing primitives — outer envelope, relay routing wrapper, error-code constants, type-name constants, and the v1-compatibility predicate — without any per-type payload structs and without any I/O.

Splitting the wire-protocol foundation along this seam (#255 = framing, #256 = payload catalog) keeps each slice ≤5 exported types and ≤100 production lines, and gives downstream consumers (#247 WSS dial+handshake, #248-#250 auth/dispatch wiring) a stable schema for envelope shape and error reporting before the per-type catalog lands. `Envelope.Payload` is `json.RawMessage` precisely so #256's structs slot in via a second-pass `json.Unmarshal` against a buffer the dispatcher already holds.

Why a separate package (`internal/protocol`):

- One source of truth for wire-format types, consumable by `internal/relay-client` (binary→relay WS connection), `internal/dispatch` (envelope-to-handler routing), `cmd/pyry-relay` (the relay binary, future repo), and the mobile clients (via golden JSON fixtures, not Go code). Keeping it out of `internal/sessions` or `internal/conversations` prevents wire concerns from polluting business-logic packages and keeps the import graph one-directional (consumers depend on `internal/protocol`; never the reverse).
- `internal/protocol` has zero runtime dependencies (no `context`, no `net`, no I/O, no `slog`). Pure types + constants + one predicate. The package's API surface is a `git diff` away from being a documentation artifact — adding behaviour to it would be the wrong instinct, and the file layout below makes that obvious.

## Design

### Package layout

One new package, two files. The ticket body lists four file names as "illustrative — architect may consolidate if total LOC stays small." Two files keep the new-files count comfortably below the architect red line and group symbols by what-they-are (types vs string constants):

```
internal/protocol/envelope.go         (new, ~50 production LOC)
internal/protocol/codes.go            (new, ~50 production LOC)
internal/protocol/envelope_test.go    (new, ~80 LOC)
internal/protocol/compat_test.go      (new, ~50 LOC)
internal/protocol/testdata/           (new — 2 golden JSON files)
```

`envelope.go` carries the two structs, the two sentinels, and the `IsV1Compatible` predicate (i.e. the package's behaviour surface). `codes.go` carries the 12 error-code constants and the 16 type-name constants (pure data; no behaviour). The split is along a clean axis — `envelope.go` is the file a maintainer reads to understand the package; `codes.go` is the file they grep when they need to look up a wire string. Splitting `codes.go` further into `errors.go` + `types.go` would cross the architect's "more than 3 new files" red line for no readability gain (the two constant blocks are ~12 + ~16 lines, both grouped by spec table order).

`testdata/` holds two golden JSON files used by the round-trip tests; see [Testing strategy](#testing-strategy).

### `internal/protocol/envelope.go`

```go
// Package protocol declares the wire-format types for pyrycode's mobile
// WebSocket protocol v1. The package is pure data: no I/O, no socket
// handling, no context plumbing. Consumers (internal/relay-client,
// internal/dispatch, future cmd/pyry-relay) marshal/unmarshal these types
// against the wire and dispatch on Envelope.Type.
//
// The single source of truth for field names, optionality, and wire
// semantics is docs/protocol-mobile.md. When that document changes,
// this package changes; the test fixtures under testdata/ pin the
// round-trip shape.
package protocol

import (
    "encoding/json"
    "errors"
    "time"
)

// Envelope is the outer wire shape every application frame conforms to
// (docs/protocol-mobile.md § Message envelope). The Payload field carries
// the per-type body as a deferred-decode json.RawMessage; per-type structs
// live in this package's siblings (introduced in #256).
//
// Field order follows the spec table verbatim. Marshal/unmarshal preserves
// snake_case field names and applies omitempty to in_reply_to and
// payload_encrypted per the spec's "optional" markers.
type Envelope struct {
    ID               uint64          `json:"id"`
    Type             string          `json:"type"`
    TS               time.Time       `json:"ts"`
    Payload          json.RawMessage `json:"payload"`
    InReplyTo        *uint64         `json:"in_reply_to,omitempty"`
    PayloadEncrypted bool            `json:"payload_encrypted,omitempty"`
}

// RoutingEnvelope wraps an Envelope with the relay-prepended conn_id used
// on the binary↔relay leg only (docs/protocol-mobile.md § Routing
// envelope). Phones never see it; the relay strips it before forwarding
// frames to phones and prepends it before forwarding frames to the binary.
//
// Frame is json.RawMessage so the relay can splice without parsing
// payloads — a structural property of the design (the relay holds zero
// per-user state).
type RoutingEnvelope struct {
    ConnID string          `json:"conn_id"`
    Frame  json.RawMessage `json:"frame"`
}

// Sentinel errors returned by IsV1Compatible. Callers (the future WS
// dispatch layer, #248) distinguish refusal cases via errors.Is and map
// each sentinel to its dotted-string wire code at the call site:
//
//   ErrUnknownType  -> protocol.unknown_type   (CodeProtocolUnknownType)
//   ErrUnsupported  -> protocol.unsupported    (CodeProtocolUnsupported)
//
// The mapping itself lives at the call site, not here, mirroring the
// internal/conversations sentinel-to-wire-code convention pinned by
// docs/PROJECT-MEMORY.md § "Refusal-to-wire-code mapping is the consumer's
// job, NOT the primitive's." This keeps internal/protocol free of wire
// dispatch knowledge while still letting the dispatcher pattern-match.
var (
    ErrUnknownType = errors.New("protocol: unknown envelope type")
    ErrUnsupported = errors.New("protocol: unsupported envelope feature")
)

// IsV1Compatible reports whether env is acceptable under wire-protocol v1.
// Returns nil when env.Type is in the v1 type set and env.PayloadEncrypted
// is false. Returns ErrUnsupported when env.PayloadEncrypted is true
// (reserved for v2; docs/protocol-mobile.md § Reserved for v2). Returns
// ErrUnknownType when env.Type is empty or not in the v1 set.
//
// Check order: PayloadEncrypted first, Type second. The order is
// observable through errors.Is at the call site; pinning it here keeps
// the wire-code emitted by the dispatcher predictable when a frame
// happens to fail both checks at once (a malformed-on-two-axes frame
// reports as protocol.unsupported, not protocol.unknown_type — the
// stricter rejection wins).
//
// IsV1Compatible does not validate Payload contents, ID monotonicity, or
// TS skew. Those are dispatcher concerns (#248) and are out of scope for
// the framing layer.
func IsV1Compatible(env Envelope) error {
    if env.PayloadEncrypted {
        return ErrUnsupported
    }
    if !v1TypeSet[env.Type] {
        return ErrUnknownType
    }
    return nil
}

// v1TypeSet is a package-private membership lookup constructed from the
// exported Type* constants in codes.go. Defined as an init-time map so the
// hot path in IsV1Compatible is one map read.
var v1TypeSet = map[string]bool{
    TypeHello:               true,
    TypeHelloAck:            true,
    TypeError:               true,
    TypeAck:                 true,
    TypeSendMessage:         true,
    TypeMessage:             true,
    TypeListConversations:   true,
    TypeConversations:       true,
    TypeCreateConversation:  true,
    TypeConversationCreated: true,
    TypePromoteConversation: true,
    TypeConversationUpdated: true,
    TypeBackfillSince:       true,
    TypeMessageChunk:        true,
    TypeBackfillDone:        true,
    TypeRegisterPushToken:   true,
}
```

### `internal/protocol/codes.go`

```go
package protocol

// Error-code constants — wire values for Envelope.Type == TypeError
// payloads' "code" field. Source: docs/protocol-mobile.md § Error codes.
// The naming convention is Code<Category><Reason>, matching the
// dotted-string structure category.reason.
//
// Categories are protocol/auth/server/conversation/message/relay; the
// constants are grouped below in the same order as the spec table.
const (
    // Protocol errors.
    CodeProtocolUnknownType = "protocol.unknown_type"
    CodeProtocolMalformed   = "protocol.malformed"
    CodeProtocolUnsupported = "protocol.unsupported"

    // Auth errors.
    CodeAuthInvalidToken = "auth.invalid_token"
    CodeAuthTokenRevoked = "auth.token_revoked"

    // Server errors.
    CodeServerBinaryOffline = "server.binary_offline"
    CodeServerBinaryBusy    = "server.binary_busy"

    // Conversation errors.
    CodeConversationNotFound        = "conversation.not_found"
    CodeConversationAlreadyPromoted = "conversation.already_promoted"

    // Message errors.
    CodeMessageTooLong = "message.too_long"

    // Relay errors.
    CodeRelayNoServer         = "relay.no_server"
    CodeRelayServerIDConflict = "relay.server_id_conflict"
)

// Envelope-type constants — wire values for Envelope.Type. Source:
// docs/protocol-mobile.md § Message types. The set is closed in v1; new
// types require a v2 envelope (per the protocol's versioning policy).
//
// Constants are grouped below by category to match the spec's section
// order: handshake/control, messaging, conversations, backfill, push.
const (
    // Handshake and control.
    TypeHello    = "hello"
    TypeHelloAck = "hello_ack"
    TypeError    = "error"
    TypeAck      = "ack"

    // Messaging.
    TypeSendMessage = "send_message"
    TypeMessage     = "message"

    // Conversations.
    TypeListConversations   = "list_conversations"
    TypeConversations       = "conversations"
    TypeCreateConversation  = "create_conversation"
    TypeConversationCreated = "conversation_created"
    TypePromoteConversation = "promote_conversation"
    TypeConversationUpdated = "conversation_updated"

    // Backfill.
    TypeBackfillSince = "backfill_since"
    TypeMessageChunk  = "message_chunk"
    TypeBackfillDone  = "backfill_done"

    // Push.
    TypeRegisterPushToken = "register_push_token"
)
```

### Why the type-set is a map literal, not a generated `[]string`

Two alternatives were considered:

1. **`[]string` slice + linear scan inside `IsV1Compatible`.** Sixteen entries; the linear scan is fine performance-wise (a Type assertion happens once per frame, not per byte). Rejected because it duplicates the constant names — both the slice and the constants list every type, and a future addition has to touch both. The map literal duplicates them once; the duplication is visible at the same indentation level as the constants block, which makes drift obvious in code review.
2. **`go:generate`-driven membership check.** Overkill for a 16-entry closed set in a stable wire-protocol. Adds a build-step dependency, a generation file, and a pre-commit obligation — none justified by the maintenance load of one closed list.

A map keeps the lookup O(1), the duplication minimal, and the code review trivial. If the v1 type set ever grows past ~50 entries (no plausible path under the protocol's "additive changes stay v1, breaking changes go v2" rule), this is a recheck point — not before.

### Why `Envelope.TS` is `time.Time`, not `string`

`docs/protocol-mobile.md` § Message envelope says: `RFC 3339 nano-precision UTC timestamp from the sender`. `encoding/json` marshals `time.Time` to RFC3339Nano (jq-debuggable, byte-stable on the happy path) and decodes back into a typed value. The dispatcher (#248) needs `time.Time` to apply the binary's 7-day backward / 5-min forward cap (§ Clock-skew handling) without re-parsing — going `string` would force every consumer to call `time.Parse` on every read.

This follows the project memory pattern at `docs/PROJECT-MEMORY.md:1071` ("`time.Time` on the wire when consumers compute on it; pre-formatted string when display-only"). `TS` is consumed for math (skew checks, ordering); typed wins.

Round-trip caveat: Go's `time.Time` carries a monotonic clock reading that is stripped by JSON marshal. Tests must compare via `time.Time.Equal`, never `==` or `reflect.DeepEqual`. This is restated in [Testing strategy](#testing-strategy).

### Why `Envelope.Payload` is `json.RawMessage`

The dispatcher decodes `Envelope` once to read the type discriminator, then decodes `Payload` again into the per-type struct that `Type` selects. `json.RawMessage` makes the deferred decode mechanical (no re-encode/re-decode round-trip) and avoids forcing this package to know about the 16 payload struct types defined in #256. Bonus: a malformed payload in a known type doesn't fail the outer parse — the dispatcher can return `protocol.malformed` with the offending envelope's `id` intact, which `Envelope.Payload (json.RawMessage)` enables and `Envelope.Payload (interface{})` would not.

The relay's `RoutingEnvelope.Frame` is also `json.RawMessage` for the same reason — the relay must not parse payloads; the splice is byte-for-byte.

### Why `IsV1Compatible` returns sentinels (not dotted strings)

`docs/PROJECT-MEMORY.md:60` pins: "Refusal-to-wire-code mapping is the consumer's job." `internal/conversations` exports `ErrConversationNotFound`/`ErrConversationAlreadyPromoted`/etc.; the `internal/control` consumer maps each sentinel to a wire code at the dispatch boundary. This package follows the same convention.

Concretely, the dispatch layer (#248) will look like:

```go
if err := protocol.IsV1Compatible(env); err != nil {
    code := protocol.CodeProtocolMalformed
    switch {
    case errors.Is(err, protocol.ErrUnsupported):
        code = protocol.CodeProtocolUnsupported
    case errors.Is(err, protocol.ErrUnknownType):
        code = protocol.CodeProtocolUnknownType
    }
    return sendError(env.ID, code, err.Error(), false)
}
```

Returning `string` from `IsV1Compatible` would couple this package to wire codes; returning `error` lets `internal/protocol` stay a pure data layer and lets the dispatcher do the wire-format choice. The cost is one switch at the call site — well-pinned by precedent.

### Out-of-scope behaviour for `IsV1Compatible`

`IsV1Compatible` is deliberately narrow:

- Does NOT validate `Envelope.ID` is non-zero or monotonic — the dispatcher tracks per-connection envelope-id state; this predicate is per-frame and stateless.
- Does NOT validate `Envelope.TS` is within skew bounds — that's the binary-side clock-skew cap (§ Clock-skew handling), enforced in #248.
- Does NOT validate `Envelope.Payload` shape — that's #256's per-type struct unmarshal.
- Does NOT validate `InReplyTo` references a real prior `id` — connection-state concern, not framing.
- Does NOT distinguish "type not in v1 set" from "type known but disallowed for this connection role" (e.g. a phone sending `hello_ack`). Role-restriction is a dispatch concern; #248 owns it.

These exclusions are restated under [Out of scope](#out-of-scope) so a future regression can't widen the predicate by accident.

### Why no `Envelope.Validate()` method

Considered. Rejected because `IsV1Compatible(env)` reads cleanly at the call site and the function form composes with `errors.Is` without method-receiver gymnastics. A `Validate` method would either duplicate `IsV1Compatible` or replace it; neither helps. Keep one entry point.

### Why no constructor (`NewEnvelope(...)`)

`Envelope` has no invariants enforced at construction time — every field is independently settable, optional fields are zero-valuable, and there is no derived state. A constructor would be six positional arguments masquerading as a struct literal. Go's struct-literal-with-named-fields is the canonical shape; we use it.

If the dispatcher (#248) needs ergonomic helpers (`MakeError(id, code, msg)`, `MakeAck(id, inReplyTo)`), they live in #248's package — those helpers depend on per-type payload structs that don't exist yet, so they belong with #256's catalog or with the dispatcher, not here.

## Concurrency model

This package is pure data. No goroutines. No locks. No I/O. No shutdown sequence.

`Envelope` and `RoutingEnvelope` are value types, safe for concurrent reads. They are not safe for concurrent mutation, but a wire-format type that is mutated after marshal/unmarshal is a programmer error in any case — the dispatcher always constructs them from a fresh decode and discards them after dispatch.

`v1TypeSet` is a package-level `map[string]bool` initialised at package init time and read-only thereafter. Concurrent reads of an unmutated Go map are race-free (per the Go memory model and the explicit `sync` package documentation). No `sync.Once`, no `sync.RWMutex` — both would be ceremony for an immutable lookup table.

`IsV1Compatible` is a pure function: same input, same output, no side effects, no allocations on the happy path (it returns one of three pre-existing values: `nil`, the sentinel `ErrUnsupported`, or the sentinel `ErrUnknownType`).

## Error handling

Failure modes the package introduces:

1. **`json.Unmarshal` of a malformed envelope.** Caller's problem. The standard library returns a typed error (`*json.SyntaxError`, `*json.UnmarshalTypeError`); the dispatcher wraps these as `protocol.malformed` at its call site. This package doesn't intercept — the unmarshal error propagates up.
2. **`Envelope.PayloadEncrypted == true`.** `IsV1Compatible` returns `ErrUnsupported`. Dispatcher maps to `CodeProtocolUnsupported`.
3. **`Envelope.Type` empty or unknown.** `IsV1Compatible` returns `ErrUnknownType`. Dispatcher maps to `CodeProtocolUnknownType`.
4. **`Envelope.TS` zero or malformed RFC3339.** Standard library handles malformed at unmarshal time (`time.Time.UnmarshalJSON` returns an error); a zero `TS` after a successful unmarshal means the field was literally `null` or absent — dispatcher's clock-skew check will catch it as out-of-bounds. Not a framing concern.
5. **`Envelope.Payload` empty / null / not an object.** Per spec § Message envelope, `payload` is required but may be `{}`. `json.RawMessage` accepts any valid JSON value (object, array, null, scalar). Per-type payload validation is #256; this layer doesn't gate on `payload` shape.

Failures the package **cannot** cause:

- A panic. There are no slice/map indexed accesses with caller-controlled indices, no type assertions, no goroutines.
- A leak. No `os.File`, no `net.Conn`, no goroutine, no allocation that outlives the call.
- A race. No mutable shared state.

## Testing strategy

Three test files; ~150 LOC total. All stdlib `testing`. Two golden JSON fixtures under `internal/protocol/testdata/`.

### `internal/protocol/envelope_test.go`

#### `TestEnvelope_RoundTrip_Golden`

Goal: pin the exact wire-format bytes for `Envelope` against a hand-authored fixture matching the spec § Message envelope example, including snake_case names, RFC3339Nano `ts`, present-and-omitted optional fields.

Shape:

1. Two fixtures under `testdata/`:
    - `envelope_full.json` — every field populated (`id`, `type`, `ts`, `payload`, `in_reply_to`, `payload_encrypted=false`). Even though `payload_encrypted: false` is `omitempty`-eligible, the wire MUST omit it when false; this fixture file is written to omit it. Tests that the absent field unmarshals to the zero value (`false`) and re-marshals to absent.
    - `envelope_minimal.json` — only required fields (`id`, `type`, `ts`, `payload`). `in_reply_to` and `payload_encrypted` absent. Tests `omitempty` on both optional fields.
2. Read fixture; `json.Unmarshal` into an `Envelope`; assert field-by-field equality. Use `time.Time.Equal` for `TS` (monotonic-clock-strip on round-trip; never `==` or `reflect.DeepEqual` per `docs/PROJECT-MEMORY.md:1071`).
3. `json.Marshal` the decoded `Envelope`; compare bytes against the fixture (canonicalised: trim trailing newline, run both through `json.Compact`). Round-trip must be byte-identical.

Two fixtures cover both branches (full + minimal) of `omitempty` behaviour; the table grows linearly if more cases land.

#### `TestRoutingEnvelope_RoundTrip_Golden`

Goal: pin `RoutingEnvelope` shape: `conn_id` + `frame` (a verbatim envelope JSON object).

One fixture: `routing_envelope.json`, shaped as `{"conn_id": "c-7f3a", "frame": <full envelope>}` (uses the same body as `envelope_full.json`). Round-trip:

1. Unmarshal into `RoutingEnvelope`.
2. Assert `ConnID == "c-7f3a"`.
3. Unmarshal `Frame` again into an `Envelope`; assert it round-trips identically to fixture #1's expectations (proves `json.RawMessage` doesn't lose precision on the splice).
4. Re-marshal `RoutingEnvelope`; compare canonicalised bytes against the fixture.

The splice property — `Frame` is byte-preserving across an unmarshal/marshal cycle — is exactly what the relay relies on (it does NOT parse payloads). Pinning it as a test makes a future "let's change `Frame` to a typed `*Envelope`" regression visible at PR time.

### `internal/protocol/compat_test.go`

#### `TestIsV1Compatible` — table-driven truth-table

Goal: pin every (input → sentinel) mapping for `IsV1Compatible`.

Table rows:

| Name | `env.Type` | `env.PayloadEncrypted` | Expected error |
|---|---|---|---|
| every-known-type-accepted | each of the 16 `Type*` constants | false | nil |
| empty-type-rejected | `""` | false | `ErrUnknownType` |
| unknown-type-rejected | `"frobnicate"` | false | `ErrUnknownType` |
| typo-near-known-type-rejected | `"helo"` | false | `ErrUnknownType` |
| encrypted-rejected-known-type | `TypeHello` | true | `ErrUnsupported` |
| encrypted-rejected-unknown-type | `"frobnicate"` | true | `ErrUnsupported` |

Last row pins the order-of-checks decision (encryption-rejection wins over type-rejection). Use `errors.Is` for comparison — never `==`. The 16-known-types case generates one row per constant via a `[]string` literal of the constants; if a future contributor adds a 17th type to `codes.go` but forgets to add it to `v1TypeSet`, this test does not catch it on its own (the test's loop is over the constants, not the map). The next test below covers that gap.

#### `TestV1TypeSet_CoversAllExportedTypeConstants`

Goal: pin the invariant that the `v1TypeSet` map and the `Type*` constants stay in lockstep. Catches the "added a new `Type*` const, forgot to add it to the map" regression.

Implementation: a `[]string` literal listing every `Type*` constant by name, mapped over to assert `v1TypeSet[t] == true` for each. The `[]string` literal is the maintenance burden — one entry per constant, sibling to the `v1TypeSet` literal in `envelope.go`. A drift between them surfaces as a test failure.

This is the "mechanical drift detector" approach. The alternative (reflection over the package's exported identifiers via `go/types`) is heavier and not worth it for a closed 16-entry set. The constants list in this test is the third copy of the type list (alongside the constants block in `codes.go` and the `v1TypeSet` literal in `envelope.go`); the explicit triple-copy is by design — every drift between them fails CI loudly.

#### `TestErrorCode_Constants_MatchSpec`

Goal: a dumb exact-string assertion for each `Code*` constant, paired with the spec's dotted string. Catches the "fat-fingered `protocol.unkown_type`" regression at the lowest possible cost.

Implementation: a `map[string]string` literal listing each constant's identifier-as-string mapped to its expected wire value. Twelve entries; one row per error code in the spec table.

This test exists because the cost of getting a wire string wrong (the relay accepts a frame that v1 implementations should reject; or worse, a frame is silently ignored) outweighs the cost of typing twelve assertions.

### What NOT to test

- `json.Marshal` / `json.Unmarshal` correctness for `time.Time`, `*uint64`, `bool`, `string`, `uint64`, `json.RawMessage`. Those are stdlib contracts.
- Concurrent access to `IsV1Compatible` — it's a pure function on immutable state; no race-detector test would surface a meaningful regression.
- `IsV1Compatible` against a `Envelope` with malformed `Payload`. The predicate doesn't gate on `Payload` shape (out of scope, restated in [Error handling](#error-handling)).
- `IsV1Compatible` against `Envelope.TS` skew. Same — out of scope.
- Per-type payload struct round-trips. Owned by #256.
- Cross-version negotiation (`hello.protocol_versions`). The wire field lives inside the per-type payload (`HelloServerPayload`/`HelloClientPayload`); this ticket only carries the type-discriminator string. #256 owns the payload struct; #248 owns the negotiation handshake.

## Out of scope (do not implement here)

- The 16 per-type payload structs (`HelloServerPayload`, `SendMessagePayload`, etc.). #256.
- Token-by-token streaming flow (`message_chunk` orchestration, partial-message buffer). #258 or successor.
- WS close codes (`1000`/`1011`/`4401`/`4404`/`4409`). Those are transport-layer concerns owned by #247 (WSS dial+handshake) and the relay's router, not the JSON envelope.
- Auth/dispatch wiring (`hello_ack`-on-connect, `auth.invalid_token`-on-bad-token, role-based type restriction). #248–#250.
- `Envelope.ID` monotonicity tracking and dedup. Per-connection state; lives with the connection holder.
- `Envelope.TS` clock-skew enforcement (7-day backward / 5-min forward caps). #248.
- A `Validate()` method on `Envelope` or a constructor for `Envelope`. Documented above.
- `errors.New("protocol.unknown envelope type")` (string-prefixed-with-dotted-code). The sentinels are clean Go errors; the dotted strings live in `Code*` constants. Mixing them would re-couple the package to wire format — avoid.
- A `[]string` slice of all `Type*` values exported as `AllV1Types`. No consumer needs it; YAGNI.
- A `go:generate`-driven membership check. Overkill for 16 entries.

## Open questions

None. Every AC corresponds to an unambiguous code path:

- `Envelope` struct → `envelope.go`, six fields with json tags pinned line-by-line by spec § Message envelope.
- `RoutingEnvelope` struct → `envelope.go`, two fields pinned by spec § Routing envelope.
- 12 error-code constants → `codes.go`, one per row of spec § Error codes.
- 16 type-name constants → `codes.go`, one per § Message types subsection.
- `IsV1Compatible` → `envelope.go`, returns nil/`ErrUnsupported`/`ErrUnknownType`; check order pinned in the doc-comment.
- Two sentinels → `envelope.go`, idiomatic `var ( Err… = errors.New(…) )` block.
- Tests → `envelope_test.go` + `compat_test.go` + 3 fixture files; coverage matrix above.

## Security review

This ticket carries the `security-sensitive` label. The pass below follows `agents/architect/security-review.md`'s checklist and applies the framing-layer scope (no I/O, no socket handling, no runtime state).

**Verdict:** PASS

**Findings:**

- **[Trust boundaries]** No findings — this package is downstream of `json.Unmarshal`. The trust boundary is the unmarshal call at the dispatcher (#248); after that, types in this package are "structurally well-formed JSON" but **not** "semantically validated." The spec is explicit about this in § "Out-of-scope behaviour for `IsV1Compatible`" and § "Error handling" point 5. The dispatcher (#248) is responsible for calling `IsV1Compatible` AND for per-type payload unmarshal AND for clock-skew checks AND for ID-monotonicity tracking — naming each as a downstream obligation prevents this package from being mistaken for a complete trust gate.
- **[Tokens, secrets, credentials]** N/A — this package handles no tokens. Device-token validation lives in `internal/devices` (existing) and the dispatcher (#248). The spec explicitly does NOT log `Envelope.Payload` or include it in error strings (the sentinel error messages are static — `"protocol: unknown envelope type"`, `"protocol: unsupported envelope feature"` — which keeps a malformed-payload-with-a-token from leaking via an error returned upward).
- **[File operations]** N/A — this package performs no I/O. Test fixtures under `testdata/` are read by the test process only; `go test` standard sandbox rules apply.
- **[Subprocess / external command execution]** N/A — no `os/exec` use.
- **[Cryptographic primitives]** N/A — no crypto operations. The `payload_encrypted` field is reserved for v2 and is rejected, not interpreted; `IsV1Compatible` returns `ErrUnsupported` before any v2-shaped data could be processed. Spec § Reserved for v2 (lines 684-699) is the source of truth; this implementation matches it.
- **[Network & I/O]** No findings at the package layer — no sockets, no listeners. **One downstream obligation, restated for the dispatcher (#248):** `json.Unmarshal` on a `[]byte` longer than the WS-frame max-size cap is a DoS vector. The cap MUST be applied at the WS read boundary BEFORE this package's types are constructed. Spec § Message types pins the per-message cap implicitly (`message.too_long` at 1 MiB per single message); the dispatcher needs to enforce a per-frame cap consistent with that, not let unbounded frames reach `json.Unmarshal`. Out of scope for this ticket, but the predicate is a sentinel of where the cap MUST already have been applied — name-checking it here so #248's spec inherits the obligation explicitly.
- **[Error messages, logs, telemetry]** No findings — the two sentinel errors carry static strings only (no field values, no payload contents). `IsV1Compatible` does not log. Downstream callers MUST NOT log `Envelope.Payload` — that is restated as a #248 obligation.
- **[Concurrency]** No findings — pure-data package; no goroutines, no locks, no shared-mutable state. `v1TypeSet` is a package-level map populated at init and read-only thereafter; concurrent reads of an unmutated Go map are race-free per the Go memory model.
- **[Threat model alignment]** Walked against `docs/protocol-mobile.md` § Security model:
    - Threat #1 (prompt injection): out of scope — payload contents are not interpreted here; the threat is structural to the architecture and lives at the LLM dispatch layer.
    - Threat #2 (server-id race): out of scope — server-id handling is the relay's and the binary's WS handshake concern (#247, #248), not the JSON envelope's.
    - Threat #3 (relay operator MITM): out of scope — TLS termination is the relay's; E2E encryption is reserved for v2 (rejected here via `ErrUnsupported`).
    - Threat #4 (token leak via phone): out of scope — token storage and validation live elsewhere.
    - Threat #5 (implementation bugs): the predicate's check-order is pinned in code AND in tests; the type-set drift test catches "added a `Type*` const, forgot the map" regression; the wire-string test catches "fat-fingered the dotted code" regression. Three explicit drift detectors for a closed-set foundation layer.
    - Threat #6 (replay attacks): out of scope — replay defence is `Envelope.ID` monotonicity tracking (per-connection state), owned by #248.
    - Threat #7 (DoS): partially addressed — `IsV1Compatible` is O(1) on the input size and allocation-free on the rejection path. The DoS vector that is NOT mitigated here is unbounded `[]byte` arriving at `json.Unmarshal`; that is the WS read boundary's job, named explicitly above as a #248 obligation.

**Reviewer:** architect (self-review per `agents/architect/security-review.md`)
**Date:** 2026-05-09
