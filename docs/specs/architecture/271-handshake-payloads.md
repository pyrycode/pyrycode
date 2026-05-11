# Spec: v1 handshake/control payload structs (#271)

## Files to read first

- `internal/protocol/envelope.go:1-95` — `Envelope` and `RoutingEnvelope` field layout, package doc comment voice, `v1TypeSet` membership map. The payload structs in this ticket are decoded *from* `Envelope.Payload (json.RawMessage)` and re-encoded *back into* it; their tag style and ordering follow this file's conventions.
- `internal/protocol/codes.go:36-62` — `TypeHello` / `TypeHelloAck` / `TypeError` / `TypeAck` wire constants. Tests reference these by symbol, not by string literal.
- `internal/protocol/envelope_test.go:1-125` — round-trip test shape: `readFixture` + `canonical` helpers via `os.ReadFile("testdata/...")` + `json.Compact` byte-equivalence comparison. New tests reuse these helpers verbatim, no new infrastructure.
- `internal/protocol/testdata/envelope_full.json` and `internal/protocol/testdata/envelope_minimal.json` — canonical field order at the Envelope level (id, type, ts, payload, in_reply_to) and time-encoding shape (`2026-05-08T10:33:14.012Z`). New fixtures must use the same Envelope-level ordering, not the order the spec markdown happens to print.
- `docs/protocol-mobile.md:205-285` — § Message types — `hello` (both roles), `hello_ack`, `error`, `ack`. The example JSON in each subsection is the golden fixture content for this ticket; the `error` field table (lines 272-277) pins optionality of `retry_after_s`.
- `docs/PROJECT-MEMORY.md:54` — #256's Out-of-Scope memo; confirms this ticket is the handshake-and-control slice of the split payload-catalog plan.

## Context

Phase 3 Track C, the handshake-and-control slice of #256. The framing primitives (`Envelope`, `RoutingEnvelope`, error-code consts, type-name consts, `IsV1Compatible`) already exist in `internal/protocol/`. This ticket adds typed structs for the four handshake/control payloads — `hello`, `hello_ack`, `error`, `ack` — so dispatch and pairing-auth handlers compose against one schema source of truth instead of decoding `json.RawMessage` ad hoc.

Scope is pure data: five new exported structs, zero methods, zero constructors, zero behaviour. Sibling slices (messaging, conversations, backfill, push) are out of scope and follow in their own tickets.

## Design

### Package surface

Add a single new file `internal/protocol/handshake.go` containing five exported types. No changes to `envelope.go`, `codes.go`, or `envelope_test.go`. No new package, no new sub-package.

The package's existing convention (per `envelope.go`'s doc comment: "no I/O, no socket handling, no context plumbing … pure data") applies unchanged. New structs are tagged DTOs with no methods. The "one file per spec-table group" convention from the technical notes drives the single-file layout: all four spec subsections (`hello`, `hello_ack`, `error`, `ack`) live in the same § Message types table, so they co-locate in one file. `HelloServerPayload` and `HelloClientPayload` are siblings under the `hello` subsection.

### Types

```go
package protocol

import (
    "encoding/json"
    "time"
)

// HelloServerPayload is the body of a "hello" envelope sent by the binary
// after WS upgrade (docs/protocol-mobile.md § Message types).
type HelloServerPayload struct {
    Role             string   `json:"role"` // always "server"
    ServerID         string   `json:"server_id"`
    BinaryVersion    string   `json:"binary_version"`
    ProtocolVersions []string `json:"protocol_versions"`
}

// HelloClientPayload is the body of a "hello" envelope sent by the phone
// after WS upgrade. LastSeenTS is optional; when present it triggers a
// backfill (docs/protocol-mobile.md § Message types, § Backfill semantics).
type HelloClientPayload struct {
    Role             string     `json:"role"` // always "client"
    DeviceName       string     `json:"device_name"`
    ClientVersion    string     `json:"client_version"`
    ProtocolVersions []string   `json:"protocol_versions"`
    LastSeenTS       *time.Time `json:"last_seen_ts,omitempty"`
}

// HelloAckPayload is the body of a "hello_ack" envelope sent in response
// to "hello" (docs/protocol-mobile.md § Message types). ConnID echoes the
// relay-assigned id back to the phone for diagnostics only.
type HelloAckPayload struct {
    ProtocolVersion string `json:"protocol_version"`
    ServerID        string `json:"server_id"`
    ConnID          string `json:"conn_id"`
}

// ErrorPayload is the body of an "error" envelope (docs/protocol-mobile.md
// § Message types, § Error codes). RetryAfterS is optional and advisory;
// it is meaningful only when Retryable is true.
type ErrorPayload struct {
    Code        string `json:"code"`
    Message     string `json:"message"`
    Retryable   bool   `json:"retryable"`
    RetryAfterS *int   `json:"retry_after_s,omitempty"`
}

// AckPayload is the body of a generic "ack" envelope; empty by spec
// (docs/protocol-mobile.md § Message types).
type AckPayload struct{}
```

Notes the developer must respect:

- **Field order matches the spec example order, not the markdown table order.** This is what the JSON encoder emits, which is what the round-trip byte-equivalence check verifies. Reordering fields breaks tests.
- **Required fields are non-pointer.** `Role`, `ServerID`, `BinaryVersion`, `ProtocolVersions`, `DeviceName`, `ClientVersion`, `ProtocolVersion`, `ConnID`, `Code`, `Message`, `Retryable` — all non-pointer, no `omitempty`. They are always present on the wire per spec.
- **Optional fields are `*T` + `omitempty`.** `LastSeenTS` and `RetryAfterS` only. `*time.Time` round-trips through `encoding/json` using `time.RFC3339Nano`; the existing `envelope_full.json` fixture proves this preserves both the `Z`-suffix and millisecond precision byte-equivalently.
- **No validation, no constructors, no methods.** `Role` is documented as `"server"` / `"client"` in a comment; runtime enforcement is the dispatcher's concern (out of scope).
- **`AckPayload` is `struct{}`, not `map[string]any` or `json.RawMessage`.** `json.Marshal(AckPayload{})` emits `{}`, which matches the spec's `"payload": {}`.
- **Imports are exactly `encoding/json` (transitively, via the `json` tag mechanism — no actual import needed on `handshake.go`) and `time`.** No new package dependencies. In practice `handshake.go` will only need `import "time"` since the `json:"..."` tags are string literals.

### Why two `Hello*Payload` structs and not one

The spec has the binary's hello and the phone's hello as two structurally distinct frames sharing only the envelope type name (`"hello"`) and the dispatch site. `role` is the discriminator, but field sets diverge (`server_id` + `binary_version` vs. `device_name` + `client_version` + optional `last_seen_ts`). Modelling as a union (single struct with all fields, mostly-optional) would lose type-level encoding of which fields belong with which role and force every consumer to validate role-field consistency by hand. Two structs lets the dispatcher decode based on `role` and pass a strongly-typed payload to each handler. This is the AC's intent (AC#3) and matches how the spec author wrote the document.

The dispatcher's "which struct to decode into" decision is out of scope for this ticket — it lives with the dispatch/pairing-auth tickets (#248–#250). This package only provides the two struct types.

## Data flow

```
WS frame bytes
  ─json.Unmarshal─▶ Envelope { Payload: json.RawMessage }
                       │
                       ▼ (dispatcher reads Envelope.Type, possibly peeks payload's "role")
                  json.Unmarshal(env.Payload, &HelloServerPayload{})   ← this ticket
                  json.Unmarshal(env.Payload, &HelloClientPayload{})   ← this ticket
                  json.Unmarshal(env.Payload, &HelloAckPayload{})      ← this ticket
                  json.Unmarshal(env.Payload, &ErrorPayload{})         ← this ticket
                  json.Unmarshal(env.Payload, &AckPayload{})           ← this ticket
```

Encoding direction is symmetric: a handler builds a `*Payload` struct, calls `json.Marshal` on it, sets the result as `Envelope.Payload`, then marshals the `Envelope`.

## Concurrency model

None. Pure data types, no mutable state, no goroutines, no channels, no context plumbing. Safe to share across goroutines as values; safe to copy.

## Error handling

None. These are DTOs. Marshal/unmarshal errors propagate via the stdlib `encoding/json` error contract; no package-level wrapping.

## Testing strategy

Add `internal/protocol/handshake_test.go` and five fixture files under `internal/protocol/testdata/`. Reuse the `canonical(t, b)` and `readFixture(t, name)` helpers already defined in `envelope_test.go` — they live in the same `package protocol` test binary, so no copy-paste, no new helpers.

### Fixtures

One JSON file per payload type, each containing a *complete* Envelope with the payload inlined. Field order at the Envelope level matches `envelope.go`'s struct order: `id, type, ts, payload, in_reply_to` (omit `in_reply_to` when nil; omit `payload_encrypted` when false — both `omitempty`).

`internal/protocol/testdata/hello_server.json`:

```json
{"id":1,"type":"hello","ts":"2026-05-08T10:33:14.012Z","payload":{"role":"server","server_id":"8f7e","binary_version":"0.10.0","protocol_versions":["v1"]}}
```

`internal/protocol/testdata/hello_client.json`:

```json
{"id":1,"type":"hello","ts":"2026-05-08T10:33:14.012Z","payload":{"role":"client","device_name":"Juhana's Pixel 8","client_version":"pyrycode-mobile 0.1.0","protocol_versions":["v1"],"last_seen_ts":"2026-05-08T08:14:02Z"}}
```

`internal/protocol/testdata/hello_ack.json`:

```json
{"id":1,"type":"hello_ack","ts":"2026-05-08T10:33:14.012Z","payload":{"protocol_version":"v1","server_id":"8f7e","conn_id":"c-7f3a"},"in_reply_to":1}
```

`internal/protocol/testdata/error.json`:

```json
{"id":99,"type":"error","ts":"2026-05-08T10:33:14.012Z","payload":{"code":"auth.invalid_token","message":"device token not recognised; re-pair via pyry pair on the binary","retryable":false},"in_reply_to":42}
```

`internal/protocol/testdata/ack.json`:

```json
{"id":100,"type":"ack","ts":"2026-05-08T10:33:14.012Z","payload":{},"in_reply_to":42}
```

Notes:

- Identifier strings (`server_id`, `conn_id`, …) are shortened from the spec's `"8f7e..."` ellipsis-suffix form to concrete bytes (`"8f7e"`, `"c-7f3a"`). The spec's `"..."` is illustrative, not literal; the fixtures need parseable JSON.
- `ts` is normalised to the existing `envelope_full.json` value so all fixtures share one canonical timestamp — easier to scan, no semantic difference.
- The `hello_client` fixture's `last_seen_ts` ("2026-05-08T08:14:02Z", no fractional seconds) verifies that `*time.Time` + `time.RFC3339Nano` re-emits the same Z-suffixed string without fractional padding. This is the Go stdlib's documented behaviour but worth pinning.
- `in_reply_to` is placed *after* `payload` in fixture JSON, matching `envelope.go`'s struct field order (`Payload` declared before `InReplyTo`), not the order the spec markdown happens to print. The existing `envelope_full.json` already follows this rule.

### Test functions

Five table-free, per-type test functions in `handshake_test.go`, each modelled on `TestEnvelope_RoundTrip_Full`:

```go
func TestHelloServerPayload_RoundTrip(t *testing.T) {
    raw := readFixture(t, "hello_server.json")

    var env Envelope
    if err := json.Unmarshal(raw, &env); err != nil { t.Fatalf(...) }
    if env.Type != TypeHello { t.Errorf(...) }

    var payload HelloServerPayload
    if err := json.Unmarshal(env.Payload, &payload); err != nil { t.Fatalf(...) }
    if payload.Role != "server" { t.Errorf(...) }
    if payload.ServerID != "8f7e" { t.Errorf(...) }
    // … one or two more field assertions to anchor the decode

    // Re-encode payload, splice back into envelope, re-encode envelope.
    payloadBytes, err := json.Marshal(payload)
    if err != nil { t.Fatalf(...) }
    env.Payload = payloadBytes
    out, err := json.Marshal(env)
    if err != nil { t.Fatalf(...) }

    if !bytes.Equal(canonical(t, out), canonical(t, raw)) {
        t.Errorf("round-trip bytes differ:\n got: %s\nwant: %s", out, raw)
    }
}
```

Per-type variants assert the discriminating field(s):

- `TestHelloServerPayload_RoundTrip` — assert `Role == "server"`, `ServerID != ""`.
- `TestHelloClientPayload_RoundTrip` — assert `Role == "client"`, `LastSeenTS != nil` and equal to the expected time, `DeviceName != ""`.
- `TestHelloAckPayload_RoundTrip` — assert `ProtocolVersion == "v1"`, `ConnID != ""`, `env.InReplyTo != nil`.
- `TestErrorPayload_RoundTrip` — assert `Code == CodeAuthInvalidToken`, `Retryable == false`, `RetryAfterS == nil` (verifies optional-pointer absence + `omitempty` symmetry).
- `TestAckPayload_RoundTrip` — assert decode succeeds, re-encoded payload is `{}`, byte-equivalence holds.

The byte-equivalence check is the load-bearing assertion. Field-value asserts exist to keep failure messages localized — when the round-trip diff fires, it's faster to see "ServerID was wrong" than to eyeball two JSON blobs.

### What's NOT tested

- The `retry_after_s` *present* path on `ErrorPayload`. AC stipulates one fixture per type, and the spec's example omits this field. Not adding a second `error_with_retry.json` fixture — the omitempty-with-nil path covers half the contract; the present-path is structurally identical to `HelloClient.LastSeenTS` which IS exercised. If a future ticket needs to pin the present-path, add the fixture then.
- Decode errors (malformed payload, wrong type, etc.). Not the AC's concern; the dispatcher owns that boundary.
- Cross-type swaps (e.g. decoding a `hello_ack` payload into a `HelloServerPayload` struct). Not the AC's concern.

## Open questions

- None blocking. Architect notes:
  - `time.RFC3339Nano` re-emits `"2026-05-08T08:14:02Z"` byte-equivalently for the no-fractional case. Confirmed via Go stdlib source and the existing `envelope_full.json` fixture's millisecond round-trip — if either assumption breaks in practice during dev, normalise the fixture's `last_seen_ts` to include `.000Z` to match what `time.Time` emits. The dev should run the test first; if it passes, the assumption holds; if it fails on `last_seen_ts`, adjust the fixture.
  - `HelloClientPayload.LastSeenTS` could plausibly be `time.Time` (zero-value as sentinel) instead of `*time.Time`. Rejected: the spec marks it explicitly optional and the AC fixes the pointer shape. Empty `time.Time{}` would marshal as `"0001-01-01T00:00:00Z"`, polluting the wire. Pointer + omitempty is the only correct encoding.

## Acceptance checklist (mirrors the ticket)

- [ ] `internal/protocol/handshake.go` defines exactly five exported types: `HelloServerPayload`, `HelloClientPayload`, `HelloAckPayload`, `ErrorPayload`, `AckPayload`. No methods, no constructors, no helpers.
- [ ] JSON tags match the spec's snake_case field names; `omitempty` only on `LastSeenTS` and `RetryAfterS`.
- [ ] Required fields non-pointer; optional fields `*T`.
- [ ] Two separate `Hello*Payload` structs; no union, no interface.
- [ ] Five fixture files under `internal/protocol/testdata/`, each a full Envelope with the payload inlined.
- [ ] Five per-type `*_RoundTrip` tests in `internal/protocol/handshake_test.go`, using `readFixture` + `canonical` helpers from `envelope_test.go`.
- [ ] `go test -race ./internal/protocol/...` passes.
- [ ] `go vet ./...` + `staticcheck ./...` clean.
- [ ] No new dependencies. Stdlib `encoding/json` (transitive through tag mechanism) + `time` only.
