# Spec — #274: net: v1 conversations-write payload structs

## Files to read first

- `docs/protocol-mobile.md` §§ `create_conversation`, `conversation_created`, `promote_conversation`, `conversation_updated` (lines 371–437) — the four spec examples. These ARE the golden-file fixtures verbatim (with `"ts": "..."` substituted for a concrete timestamp).
- `internal/protocol/envelope.go:23-30` — `Envelope` shape; `Payload` is `json.RawMessage`, decoded in two passes by the dispatcher.
- `internal/protocol/codes.go:47-53` — existing type-name consts (`TypeCreateConversation`, `TypeConversationCreated`, `TypePromoteConversation`, `TypeConversationUpdated`). Use them in tests; do not redefine.
- `internal/protocol/conversations_read.go` — sibling slice, same package, same documentation style. In particular: `ConversationSummary` (lines 27–34) shows the `*string` + no-`omitempty` pattern for spec-optional fields whose example wire shows `null`. Mirror the rationale comment.
- `internal/protocol/conversations_read_test.go` — sibling test layout. Reuse `canonical()` and `readFixture()` from `envelope_test.go`; do not re-declare them.
- `internal/protocol/envelope_test.go:11-27` — `canonical()` / `readFixture()` helpers (package-level, shared across all `_test.go` files).
- `internal/protocol/push.go` + `push_test.go` — the most recent sibling (#275). Matches the per-type comment + round-trip test pattern this ticket reproduces.
- `internal/protocol/testdata/conversations.json` — example of a multi-row fixture containing both `"name": "..."` and `"name": null`, demonstrating the round-trip invariant this spec must preserve.

## Context

Phase 3 Track C, the conversations-write payload slice. Framing primitives (`Envelope`, `RoutingEnvelope`, type-name and error-code consts, `IsV1Compatible`) are merged. The sibling slices `register_push_token` (#275) and conversations-read (#273) established the per-type convention this ticket follows:

- One Go file per spec-table section (or section group) under `internal/protocol/`.
- One golden-file fixture per type at `internal/protocol/testdata/<type>.json`, byte-equal to the spec example wrapped in an `Envelope`.
- One round-trip test per type: unmarshal into `Envelope`, second-pass unmarshal `Envelope.Payload` into the per-type DTO, re-marshal, compare `canonical()` bytes against `readFixture()` bytes.

This ticket adds DTOs for the four conversations-write types. Pure data, no I/O, no validation, no constructors.

## Design

### File layout

One new production file, one new test file, four new fixtures:

```
internal/protocol/conversations_write.go              (~80 LOC incl. comments)
internal/protocol/conversations_write_test.go         (~120 LOC)
internal/protocol/testdata/create_conversation.json
internal/protocol/testdata/conversation_created.json
internal/protocol/testdata/promote_conversation.json
internal/protocol/testdata/conversation_updated.json
```

Single Go file: per the ticket's "architect's discretion" note, and consistent with `conversations_read.go` which groups `ListConversationsPayload` + `ConversationsPayload` + `ConversationSummary` together.

### Struct definitions

Field order in each Go struct **MUST** match the wire-field order of that type's spec example. `encoding/json` emits fields in struct-declaration order, and `canonical()` (json.Compact) does not reorder keys — the test compares compacted bytes, so any mismatch in field order breaks round-trip. The order below is dictated by the spec examples in `docs/protocol-mobile.md` lines 371–437.

```go
// CreateConversationPayload is the body of a create_conversation frame
// (docs/protocol-mobile.md § create_conversation). Phone → binary. All
// three fields are spec-optional — the binary fills server-side defaults
// when null is on the wire (or when a field is absent under future-relaxed
// encoders; v1 fixtures always send the key with an explicit value).
//
// Fields are pointers without omitempty so a nil value round-trips as
// JSON null (matching the spec example's "name": null / "cwd": null) and
// a pointer-to-zero round-trips as the zero scalar (matching the spec
// example's "is_promoted": false). omitempty on a nil pointer would drop
// the key entirely, breaking byte-equivalent round-trip.
type CreateConversationPayload struct {
    IsPromoted *bool   `json:"is_promoted"`
    Name       *string `json:"name"`
    Cwd        *string `json:"cwd"`
}

// ConversationCreatedPayload is the body of a conversation_created frame
// (docs/protocol-mobile.md § conversation_created). Binary → phone, sent
// in reply to a create_conversation. ID, IsPromoted, Cwd, LastUsedAt are
// spec-required and non-nilable. Name is a pointer because the spec
// example shows "name": null (an unnamed scratch conversation); see the
// rationale on CreateConversationPayload for why omitempty is omitted.
type ConversationCreatedPayload struct {
    ID         string    `json:"id"`
    IsPromoted bool      `json:"is_promoted"`
    Cwd        string    `json:"cwd"`
    Name       *string   `json:"name"`
    LastUsedAt time.Time `json:"last_used_at"`
}

// PromoteConversationPayload is the body of a promote_conversation frame
// (docs/protocol-mobile.md § promote_conversation). Phone → binary. All
// three fields are spec-required: a promoted conversation must carry a
// name and an effective cwd, and the conversation_id must resolve to an
// existing row.
type PromoteConversationPayload struct {
    ConversationID string `json:"conversation_id"`
    Name           string `json:"name"`
    Cwd            string `json:"cwd"`
}

// ConversationUpdatedPayload is the body of a conversation_updated frame
// (docs/protocol-mobile.md § conversation_updated). Binary → phone,
// broadcast to all phones on this server-id. ID, IsPromoted, Cwd,
// LastUsedAt are required. Name is spec-optional (a previously unnamed
// conversation can be updated without acquiring a name) and is a pointer
// for the same round-trip reason given above.
type ConversationUpdatedPayload struct {
    ID         string    `json:"id"`
    IsPromoted bool      `json:"is_promoted"`
    Name       *string   `json:"name"`
    Cwd        string    `json:"cwd"`
    LastUsedAt time.Time `json:"last_used_at"`
}
```

The single import is `"time"`.

No methods, no constructors, no validation helpers — pure DTOs. (AC: "No new exported types beyond the four payload structs.")

### Wire-shape invariant

Per AC: "fields the spec example shows as `null` must round-trip as JSON `null` (not as absent keys)". This is enforced by the *combination* of (a) pointer types for spec-optional fields and (b) absence of `omitempty`. Both halves are load-bearing — drop `omitempty` is not the default, and pointer-with-omitempty is exactly the common mistake. The comment block on `CreateConversationPayload` documents the contract once; the others reference it.

### Fixture wire shapes

Each fixture is the spec example, byte-for-byte, with a concrete `ts` (and `in_reply_to` for `conversation_created`). The envelope key order is fixed by `Envelope`'s field declaration order (`id`, `type`, `ts`, `payload`, `in_reply_to`, `payload_encrypted`); the payload's internal key order follows each struct's declaration order. `omitempty` on `InReplyTo` and `PayloadEncrypted` drops them when zero — matches sibling fixtures.

A reasonable `ts` is `"2026-05-08T10:33:14.012Z"` (the value used by `envelope_full.json` and `register_push_token.json`); for `last_used_at` use the spec example's value (`"2026-05-08T10:34:01Z"` or `"2026-05-08T10:34:30Z"`) — note these have **no fractional seconds**, which round-trips through `time.Time` cleanly. Do not pad with `.000` — Go's `time.Time.MarshalJSON` emits `2026-05-08T10:34:01Z` regardless, so a fixture written with `.000Z` would fail the byte-equivalent check.

Recommended fixture contents:

`testdata/create_conversation.json`:
```json
{"id":4,"type":"create_conversation","ts":"2026-05-08T10:33:14.012Z","payload":{"is_promoted":false,"name":null,"cwd":null}}
```

`testdata/conversation_created.json`:
```json
{"id":260,"type":"conversation_created","ts":"2026-05-08T10:33:14.012Z","payload":{"id":"c3...","is_promoted":false,"cwd":"/Users/juhana/pyry-workspace/scratch","name":null,"last_used_at":"2026-05-08T10:34:01Z"},"in_reply_to":4}
```

Note the envelope-level field order here: `id, type, ts, payload, in_reply_to`. This matches the `Envelope` struct's declaration order, which is what Go's marshaller will emit on re-encode. The spec example has `in_reply_to` between `ts` and `payload`, but the existing `envelope_full.json` and the `Envelope` struct put it after `payload`. The round-trip target is the **Go-marshaller output**, not the spec example's literal key order at envelope level — sibling fixtures already established this convention; mirror it. Inside `payload`, however, the per-type struct's declaration order matches the spec example exactly, so payload-internal ordering does match the spec.

`testdata/promote_conversation.json`:
```json
{"id":5,"type":"promote_conversation","ts":"2026-05-08T10:33:14.012Z","payload":{"conversation_id":"c2...","name":"weekly-planning","cwd":"/Users/juhana/pyry-workspace/weekly-planning"}}
```

`testdata/conversation_updated.json`:
```json
{"id":270,"type":"conversation_updated","ts":"2026-05-08T10:33:14.012Z","payload":{"id":"c2...","is_promoted":true,"name":"weekly-planning","cwd":"/Users/juhana/pyry-workspace/weekly-planning","last_used_at":"2026-05-08T10:34:30Z"}}
```

Each fixture is a single line terminated by one `\n` (matches sibling fixtures; `os.ReadFile` returns including the trailing newline; `json.Compact` strips whitespace including the newline, so canonicalized output equals canonicalized input). If a final newline is present in either side but absent in the other, the comparison still works because `canonical()` strips it on both.

## Testing strategy

One `_test.go` file, four test functions, all in package `protocol`. Each follows the existing `TestRegisterPushTokenPayload_RoundTrip` template:

1. `raw := readFixture(t, "<type>.json")`
2. Unmarshal `raw` into an `Envelope`; assert `env.Type == Type<Name>` const.
3. Second-pass unmarshal `env.Payload` into the per-type DTO; assert a couple of representative field values (matches `conversations_read_test.go` granularity — verify required scalars, verify pointer-vs-nil for spec-null fields).
4. Re-marshal the `Envelope`; assert `bytes.Equal(canonical(t, out), canonical(t, raw))`.

Per-type field-value assertions to include (these prove the optionality contract, not just the byte equality):

- **`create_conversation`**: assert `p.IsPromoted != nil && *p.IsPromoted == false`; `p.Name == nil`; `p.Cwd == nil`. (Proves wire `false` → pointer-to-false, not nil; wire `null` → nil.)
- **`conversation_created`**: assert `p.ID == "c3..."`, `p.IsPromoted == false`, `p.Cwd == "/Users/juhana/pyry-workspace/scratch"`, `p.Name == nil` (wire `null`), `p.LastUsedAt` equals parsed `2026-05-08T10:34:01Z`. Also assert `env.InReplyTo != nil && *env.InReplyTo == 4`.
- **`promote_conversation`**: assert all three string fields equal their wire values.
- **`conversation_updated`**: assert `p.ID == "c2..."`, `p.IsPromoted == true`, `p.Name != nil && *p.Name == "weekly-planning"`, `p.Cwd == "/Users/juhana/pyry-workspace/weekly-planning"`, `p.LastUsedAt` equals parsed `2026-05-08T10:34:30Z`.

No table-driven test — each fixture is distinct enough that a flat function-per-type reads cleaner and matches the established sibling style (`conversations_read_test.go`, `push_test.go`).

### What is *not* tested

- `IsV1Compatible` for these types — already covered by `compat_test.go` via the `v1TypeSet` map in `envelope.go`; these four `Type*` consts are already in that set.
- Marshalling from a Go struct constructed in code (round-trip starts from the fixture, which is the spec source of truth). Constructing in Go and asserting against a string literal would just re-test `encoding/json`.
- Error paths (malformed JSON, missing required fields). The package is pure data with no validation; rejection is a dispatcher concern, deferred to a future ticket.

## Error handling

None at this layer. `json.Unmarshal` errors propagate to the dispatcher as `protocol.malformed` (mapping handled by the dispatcher ticket, not here). Required-field absence is silently zero-valued by `encoding/json` — this is the standard Go contract and matches the sibling slices' choice.

## Concurrency model

N/A — pure DTOs, no goroutines, no shared state.

## Open questions

None. The shapes are dictated verbatim by `docs/protocol-mobile.md` lines 371–437; the optionality contract is verbatim from the AC; the test pattern is verbatim from `push_test.go` and `conversations_read_test.go`.

## Out of scope (per ticket)

- The dispatcher / second-pass decode wiring.
- `internal/conversations` registry (the in-memory store these payloads will eventually flow into).
- `ConversationSummary` reuse — explicitly excluded by the ticket; the read slice's row type is not used here.
- Sibling message types (`backfill_*`, messaging payloads, handshake — those are or will be separate tickets).
