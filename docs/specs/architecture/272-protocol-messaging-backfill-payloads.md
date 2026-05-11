# 272 — `net`: v1 messaging + backfill payload structs

## Files to read first

- `docs/protocol-mobile.md:289-329` — § `send_message` and § `message`. Pins field names (`conversation_id`, `message_id`, `text`, `role`) and the role-value table (`user` / `assistant` / `system`). The two JSON examples in this range are the golden fixtures' authoritative source.
- `docs/protocol-mobile.md:439-454` — § `backfill_since`. The example shows `conversation_id: null` literally, which forces `*string` + `omitempty` to round-trip as JSON `null`-or-absent rather than the empty string. `since_ts` is RFC3339Nano (see § Message envelope for the format rule that applies to every timestamp on the wire); `max_messages` is an `int` advisory cap.
- `docs/protocol-mobile.md:456-478` — § `message_chunk` and § `backfill_done`. The chunk-payload key is `messages` (plural, slice), shape "same as `message.payload`, multiple"; the done-payload key is `delivered` (`int`).
- `docs/protocol-mobile.md:177-201` — § Message envelope. RFC3339Nano timestamp rule (`time.Time` on the wire, monotonic clock stripped on marshal); applies to `BackfillSincePayload.SinceTS`. Restated so the fixture format matches without re-checking.
- `internal/protocol/envelope.go:23-30` — `Envelope` struct. The payload structs in this ticket slot into `Envelope.Payload (json.RawMessage)` via a second-pass `json.Unmarshal`; the round-trip tests embed each payload inside an `Envelope` fixture to prove that path.
- `internal/protocol/envelope_test.go:11-27` — `canonical` and `readFixture` test helpers. This ticket's test file reuses both (same package, no re-import). DO NOT redefine.
- `internal/protocol/envelope_test.go:29-94` — `TestEnvelope_RoundTrip_*` shape. The new round-trip tests follow this exact pattern: read fixture → unmarshal → spot-assert fields → re-marshal → canonicalise → `bytes.Equal`. Same idiom, one test function per payload type.
- `internal/protocol/codes.go:44-58` — `TypeSendMessage`, `TypeMessage`, `TypeBackfillSince`, `TypeMessageChunk`, `TypeBackfillDone`. The five constants already exist; the test fixtures use them as the `type` field value via `Envelope.Type`. No new constants needed in this ticket.
- `docs/specs/architecture/255-protocol-envelope-routing-errors-compat.md` — sibling spec (#255). Establishes the `internal/protocol` package's "pure data, zero runtime dependencies" stance and the `time.Time`-on-the-wire / `Equal`-not-`==` convention; both apply here verbatim.
- `CODING-STYLE.md` — `gofmt`, stdlib-only, table-driven tests. Inherited unchanged; not restated below.

## Context

Phase 3 Track C — the messaging-and-backfill slice of the v1 payload catalog. The framing primitives (`Envelope`, `RoutingEnvelope`, error-code consts, type-name consts, `IsV1Compatible`) landed in #255 and now live in `internal/protocol/envelope.go` + `codes.go`. The 16-type wire catalog is being split into per-category slices (#256 parent → handshake/control #271, messaging/backfill #272, conversations TBD, push #275) so each slice stays ≤5 exported types and ≤~50 production LOC.

This ticket adds the five payload structs that slot into `Envelope.Payload (json.RawMessage)` once the future dispatcher (`#248`) reads `Envelope.Type` and selects which struct to second-pass-decode. The shapes are fixed verbatim by `docs/protocol-mobile.md` § Message types — the example payload in each subsection is this ticket's golden-file fixture, end of story.

Why these five are grouped:

- `MessageChunkPayload.Messages` reuses `MessagePayload` — the spec says "same shape as `message.payload`, multiple." Splitting `message` from `message_chunk` would either duplicate the row type (drift risk) or force a cross-slice dependency for a single field. Keeping them together is the smaller seam.
- `send_message` is the phone→binary user message; `message` is the binary→phone echo / assistant reply; `backfill_since` is the phone→binary catch-up request; `message_chunk` + `backfill_done` are the binary→phone responses. All five are on the same wire-direction axis as a logical conversation flow — they share a documentation neighbourhood in the spec and a maintenance neighbourhood here.

Why a new file rather than appending to `envelope.go`:

`envelope.go` is the framing-layer file (outer envelope, routing wrapper, sentinels, compat predicate). `codes.go` is the constants file. Adding per-type DTOs to either would conflate the framing/data axis; the existing pattern (sibling branches #271 use `handshake.go`, #275 uses `push.go`) is "one file per spec-section group." This ticket follows it with `messaging.go`. The file boundary is the same axis as the spec's `## Message types` subsection groups; navigation is mechanical.

## Design

### Package layout

One new production file, one new test file, five new fixture files. No new packages, no new exported identifiers beyond the five DTOs.

```
internal/protocol/messaging.go               (new, ~40 production LOC)
internal/protocol/messaging_test.go          (new, ~120 LOC)
internal/protocol/testdata/send_message.json     (new — envelope-wrapped)
internal/protocol/testdata/message.json          (new — envelope-wrapped)
internal/protocol/testdata/backfill_since.json   (new — envelope-wrapped)
internal/protocol/testdata/message_chunk.json    (new — envelope-wrapped, ≥2 messages)
internal/protocol/testdata/backfill_done.json    (new — envelope-wrapped)
```

Two new Go files (within the architect's "more than 3 new files" red line); five fixture files are data, not code, and count under the same red line only in the data-on-disk sense — they carry no Go symbols and do not enter the type-checker. The five fixtures match one-per-spec-subsection, which is the obvious slicing for golden-file tests.

### `internal/protocol/messaging.go`

```go
package protocol

import "time"

// SendMessagePayload is the body of an Envelope whose Type == TypeSendMessage
// (docs/protocol-mobile.md § send_message). Phone → binary direction.
// All three fields are required by the spec; no omitempty.
type SendMessagePayload struct {
    ConversationID string `json:"conversation_id"`
    MessageID      string `json:"message_id"`
    Text           string `json:"text"`
}

// MessagePayload is the body of an Envelope whose Type == TypeMessage
// (docs/protocol-mobile.md § message). Binary → phone direction; carries
// either a user-message echo (to other paired devices) or an assistant
// reply.
//
// Role is one of "user", "assistant", "system" per the spec's field table.
// The type stays string (not a named Role enum) — the binary already treats
// role-strings as string-typed elsewhere, and a typed Role would force a
// converter at every internal call site for no wire-format gain.
type MessagePayload struct {
    ConversationID string `json:"conversation_id"`
    MessageID      string `json:"message_id"`
    Role           string `json:"role"`
    Text           string `json:"text"`
}

// BackfillSincePayload is the body of an Envelope whose Type ==
// TypeBackfillSince (docs/protocol-mobile.md § backfill_since). Phone →
// binary direction.
//
// ConversationID is *string so the wire can carry JSON null (meaning "all
// conversations" per the spec example) distinctly from absent and from the
// empty string. SinceTS is RFC3339Nano per the envelope's timestamp rule;
// MaxMessages is the phone's advisory cap on returned-message count.
type BackfillSincePayload struct {
    SinceTS        time.Time `json:"since_ts"`
    ConversationID *string   `json:"conversation_id,omitempty"`
    MaxMessages    int       `json:"max_messages"`
}

// MessageChunkPayload is the body of an Envelope whose Type ==
// TypeMessageChunk (docs/protocol-mobile.md § message_chunk). Binary →
// phone direction; streamed during a backfill response. Messages reuses
// MessagePayload directly — the spec says "same shape as message.payload,
// multiple."
type MessageChunkPayload struct {
    Messages []MessagePayload `json:"messages"`
}

// BackfillDonePayload is the body of an Envelope whose Type ==
// TypeBackfillDone (docs/protocol-mobile.md § backfill_done). Binary →
// phone direction; sent after the last message_chunk to mark completion.
// Delivered is the total messages emitted across the preceding
// message_chunk envelopes for this backfill request.
type BackfillDonePayload struct {
    Delivered int `json:"delivered"`
}
```

### Field-by-field rationale

| Field | Type | `omitempty`? | Why |
|---|---|---|---|
| `SendMessagePayload.ConversationID` | `string` | no | Required (spec example). Empty string is a malformed frame, dispatcher's problem. |
| `SendMessagePayload.MessageID` | `string` | no | Required (spec example). Same logic. |
| `SendMessagePayload.Text` | `string` | no | Required. Empty string is technically valid wire-side; semantic validation (rate-limiting empty sends, etc.) is dispatcher. |
| `MessagePayload.ConversationID` | `string` | no | Required. |
| `MessagePayload.MessageID` | `string` | no | Required. |
| `MessagePayload.Role` | `string` | no | Required. Stays `string`, NOT a named `Role` type — see § "Why `Role` stays `string`" below. |
| `MessagePayload.Text` | `string` | no | Required. |
| `BackfillSincePayload.SinceTS` | `time.Time` | no | Required. `time.Time` (not `string`) because consumers compute on it (skew checks, ordering); pinned by precedent (`docs/PROJECT-MEMORY.md` § "time.Time on the wire when consumers compute on it"). |
| `BackfillSincePayload.ConversationID` | `*string` | **yes** | Spec example shows literal `null`. `*string` + `omitempty` lets the wire encode JSON `null` (when set to non-nil `""` … see below), absent, or set distinctly. |
| `BackfillSincePayload.MaxMessages` | `int` | no | Required. `int` (not `*int`) — spec presents it as always-supplied advisory cap. |
| `MessageChunkPayload.Messages` | `[]MessagePayload` | no | Required. A `nil` slice marshals as `null`; the spec example shows a non-empty array, so the fixture is non-empty too. |
| `BackfillDonePayload.Delivered` | `int` | no | Required. `0` is a valid wire value (zero deliveries is meaningful). |

### Why `MessageChunkPayload.Messages` reuses `MessagePayload`

The spec says it directly: "same shape as `message.payload`, multiple." Splitting `MessageChunkRowPayload` (or similar) off as a separate type would:

1. Force the same field list to be maintained twice → silent drift between the standalone `message` and the inside-`message_chunk` row, exactly the bug the spec text is written to forbid.
2. Cost one round-trip test per duplicate type for no observable wire-format gain.

The reuse is a hard requirement in the AC ("MessageChunkPayload.Messages reuses MessagePayload directly — no duplicate row type"), pinned in the AC because this is the obvious-but-wrong place a future contributor might add a duplicate. The test in § Testing strategy assert-loops over `MessageChunkPayload.Messages` and applies the same field expectations as the standalone `message` fixture.

### Why `BackfillSincePayload.ConversationID` is `*string` (not `string`)

The spec example has `"conversation_id": null` literally on the wire. Three possible Go shapes:

1. `string` — null on the wire decodes as the empty string. Empty string is also a wire-legitimate "send me everything for the conversation whose id is the empty string", which a future stricter dispatcher might reject as a malformed frame. The two cases (null = "all conversations" per the spec footer; "" = potential dispatcher-rejection) become indistinguishable. Wrong.
2. `*string` + `omitempty` — null on the wire decodes as a nil pointer. The dispatcher's "this means all conversations" branch is `payload.ConversationID == nil`, which is the spec's semantics one-to-one. **This is what we use.** The marshal round-trip preserves the absent/null distinction at the boundary: if the spec example sends `"conversation_id": null` explicitly, the round-trip MUST preserve that exact byte sequence — see § "Fixture format" below for how this is done.
3. `sql.NullString` or a custom wrapper — overkill; `*string` carries the same information with one pointer indirection and zero new types.

Trade-off cost: callers must check `if payload.ConversationID != nil { ... }` rather than `if payload.ConversationID != "" { ... }`. That's a one-line guard at the call site (zero call sites today; a single call site at the dispatcher when it lands). The benefit — distinguishing "all conversations" from "empty-string-typo conversation" — outweighs the extra deref.

### Why `Role` stays `string`

The AC pins this explicitly: `MessagePayload.Role` is `string`, not a named `Role` type. Two reasons:

1. **No call-site churn**: the binary already treats role strings as `string` elsewhere (e.g. in conversation message tracking; even though those internal types may evolve, a typed `Role` here would force a converter at every internal use site for zero observable wire-format gain).
2. **No closed-set guarantee at the framing layer**: the spec says "one of `user`, `assistant`, `system`" — a closed set today, but extending the set in v1.x is a backwards-compatible additive change (e.g. adding `tool` for tool-call messages later). A typed `Role` constants block + a `valid` predicate would have to be widened too, and would tempt a `Role.Validate()` method that re-introduces a wire-format-aware gate inside this package (the same anti-pattern #255 rejected with `IsV1Compatible`-not-`Envelope.Validate`).

If a future ticket needs role-set validation, it lives at the dispatcher, not here.

### Fixture format

Five fixtures under `internal/protocol/testdata/`, one per type. Each is a complete `Envelope` JSON object (not just the payload), so the round-trip test exercises both the outer envelope's `json.RawMessage` deferred-decode AND the per-type struct's marshal/unmarshal in one pass.

Fixture content (canonical compact JSON, one frame per file, no trailing newline beyond the file's natural EOL):

- **`send_message.json`** — directly from spec § send_message:
  ```json
  {"id":7,"type":"send_message","ts":"2026-05-08T10:33:14.012Z","payload":{"conversation_id":"c1","message_id":"m9","text":"what's the weather like in Helsinki tomorrow?"}}
  ```
- **`message.json`** — directly from spec § message:
  ```json
  {"id":240,"type":"message","ts":"2026-05-08T10:33:14.300Z","payload":{"conversation_id":"c1","message_id":"m12","role":"assistant","text":"Tomorrow in Helsinki: 4°C, light snow showers in the afternoon."}}
  ```
- **`backfill_since.json`** — directly from spec § backfill_since (carries `"conversation_id":null` literally on the wire, which is the most fragile fixture):
  ```json
  {"id":6,"type":"backfill_since","ts":"2026-05-08T08:30:00Z","payload":{"since_ts":"2026-05-08T08:14:02Z","conversation_id":null,"max_messages":500}}
  ```
  IMPORTANT for the developer: the payload's field order MUST be `since_ts, conversation_id, max_messages` for the round-trip to be byte-stable. Go's `encoding/json` marshals struct fields in declaration order — keep the struct declaration order matched to the fixture (the design above already does, but a future reorderer must update the fixture in lockstep).
- **`message_chunk.json`** — derived from spec § message_chunk; the spec's example uses `/* ... */` placeholders, so this fixture concretises with two real messages (≥2 required by AC for non-trivial slice coverage):
  ```json
  {"id":280,"type":"message_chunk","ts":"2026-05-08T10:34:00.500Z","in_reply_to":6,"payload":{"messages":[{"conversation_id":"c1","message_id":"m12","role":"assistant","text":"Tomorrow in Helsinki: 4°C, light snow showers in the afternoon."},{"conversation_id":"c1","message_id":"m13","role":"user","text":"thanks"}]}}
  ```
- **`backfill_done.json`** — directly from spec § backfill_done:
  ```json
  {"id":281,"type":"backfill_done","ts":"2026-05-08T10:34:30.100Z","in_reply_to":6,"payload":{"delivered":47}}
  ```

### Why `conversation_id: null` is `omitempty` and how the round-trip stays byte-identical

`*string` + `omitempty` together would, in the naive case, marshal a nil pointer as **absent** (skipped entirely) rather than as `null`. The fixture's literal `"conversation_id":null` would then round-trip to absent, which is NOT byte-identical.

This is the single subtle interaction in the ticket. Two reconciliations:

1. **Marshal-side trick (chosen)**: declare `BackfillSincePayload.ConversationID` as `*string` **without** `omitempty`. A nil pointer marshals as `null`; a non-nil pointer marshals as the string value. The fixture's `"conversation_id":null` decodes to nil and re-encodes to `null` — byte-identical.

   **Update the design above accordingly**: drop `omitempty` from `BackfillSincePayload.ConversationID`. The corrected struct field is:

   ```go
   ConversationID *string `json:"conversation_id"`
   ```

   The AC reads: "Optional / nullable fields use `*T` + `omitempty` where the spec marks them optional or shows `null` in the example." Read literally that prescribes `omitempty`; but the spec example **explicitly shows `null` on the wire**, which means the dispatcher and the relay expect the key present and the value `null`. `omitempty` would drop the key. The AC's intent is "the field is optional from the caller's POV" — `*string` (rather than `string`) is the part that satisfies that intent; `omitempty` is the part that would break the wire match. **Drop `omitempty` for this field only.** (The other `omitempty` candidate in this ticket — none — does not arise; all other fields are required.)

2. **Alternative considered**: keep `omitempty`, and amend the fixture to omit `conversation_id` entirely. **Rejected**: the spec example *literally writes `null`*, and the canonical fixture in this spec is "the JSON in the spec, verbatim." Diverging from the spec wire bytes in the fixture would defeat the fixture's role as a regression detector for spec-drift.

Document this exception inline in `messaging.go` as a one-line comment on the field — the future contributor who sees `*string` without `omitempty` should immediately know why:

```go
ConversationID *string `json:"conversation_id"` // *string + no omitempty: spec wire shows literal `null`; omitempty would drop the key.
```

This is the rare case where a comment is mandatory under the project's "default to no comments" rule — the WHY (preserve `null`-on-wire) is non-obvious and removing the comment would lead a future contributor to "fix" the missing `omitempty` and silently break the round-trip.

### Why one file (`messaging.go`) and not five

Considered. Rejected because:

- Five files for five DTOs is ceremony — total production LOC fits comfortably in ~40 lines; the architect "more than 3 new files" red line would be tripped on Go code alone with no offsetting cohesion gain.
- The five types are spec-grouped together (one continuous block of `docs/protocol-mobile.md`); grep'ing for `MessagePayload` should land all the messaging types in one open buffer, not five tab switches.
- Sibling-slice precedent: #271 puts all handshake/control payloads in `handshake.go` (one file, multiple types); #275 puts all push payloads in `push.go`. Following the convention.

### Why no constructors, no methods, no `Validate()`

Same logic as #255 § "Why no Envelope.Validate() method" and § "Why no constructor". The AC pins this explicitly: "No constructors, no methods — pure DTOs." Going further: this ticket is in scope for adding payload struct types; it is OUT of scope for predicates, helpers, marshalers, or methods. Resist.

## Concurrency model

This is pure-data, additive package work. No new goroutines. No new locks. No new I/O. No shutdown sequence.

The five new struct types are value types, safe for concurrent reads, unsafe for concurrent mutation — same convention as `Envelope` and `RoutingEnvelope`. The dispatcher always constructs each from a fresh `json.Unmarshal` and discards after handler dispatch; concurrent-mutation is structurally impossible at the use sites we expect.

## Error handling

No new failure modes introduced by this package. Marshal/unmarshal errors flow up to the caller (the future dispatcher) unchanged. Specifically:

- Malformed JSON in `Envelope.Payload`: `json.Unmarshal` into one of the new types returns a `*json.SyntaxError` or `*json.UnmarshalTypeError`. The dispatcher's responsibility is to convert this to `CodeProtocolMalformed` (a wire code already defined in `codes.go`).
- Missing required field (e.g. `text` absent from `send_message` payload): Go's `encoding/json` does NOT enforce required fields — the field decodes to its zero value. Required-field validation is the dispatcher's job (#248), NOT this package's. This is the same boundary `IsV1Compatible` draws: framing-layer types are "structurally well-formed JSON" but not "semantically validated." The fixture tests do NOT assert on required-field-missing cases; they assert on the spec example shape, end of story.
- `BackfillSincePayload.SinceTS` malformed: `time.Time.UnmarshalJSON` rejects malformed RFC3339 at unmarshal time. Same as `Envelope.TS` — not this package's problem to retry or wrap.
- `MessageChunkPayload.Messages` empty: decodes fine as a zero-length slice. Wire-legitimate (e.g. `{"messages":[]}`); the dispatcher decides whether to forward or drop. No validation here.

No new sentinels. No new exported errors. The two existing sentinels (`ErrUnknownType`, `ErrUnsupported`) remain the package's only error surface.

## Testing strategy

One test file: `internal/protocol/messaging_test.go`. Five test functions, one per payload type. Each test embeds the payload inside an `Envelope` (so the existing `canonical` and `readFixture` helpers apply unchanged) and exercises:

1. Read fixture from `testdata/<type>.json`.
2. `json.Unmarshal` into a fresh `Envelope`.
3. Assert `env.Type == Type<…>` (the matching `codes.go` constant).
4. `json.Unmarshal(env.Payload, &payload)` into the per-type struct.
5. Spot-assert each field of `payload` (use `time.Time.Equal` for `BackfillSincePayload.SinceTS`, never `==`).
6. `json.Marshal(env)` and compare `canonical(out)` against `canonical(raw)`. `bytes.Equal` MUST be true.

### Test function shapes

- **`TestSendMessagePayload_RoundTrip`** — fixture `send_message.json`. Asserts `payload.ConversationID == "c1"`, `payload.MessageID == "m9"`, `payload.Text` starts with `"what's the weather"`. Round-trip byte-identical.
- **`TestMessagePayload_RoundTrip`** — fixture `message.json`. Asserts `payload.Role == "assistant"`, `payload.ConversationID == "c1"`, `payload.MessageID == "m12"`, `payload.Text != ""`. Round-trip byte-identical.
- **`TestBackfillSincePayload_RoundTrip`** — fixture `backfill_since.json`. Critical assertions:
  - `payload.ConversationID == nil` (NOT `*payload.ConversationID == ""` — that would panic on a nil pointer, AND it would be the wrong check). The test must explicitly compare to `nil`.
  - `payload.SinceTS.Equal(time.Date(2026, 5, 8, 8, 14, 2, 0, time.UTC))`.
  - `payload.MaxMessages == 500`.
  - Round-trip byte-identical INCLUDING the literal `"conversation_id":null` in the re-marshalled output. The byte-equal check is the regression detector for the `*string`-without-`omitempty` design decision; if a future contributor adds `omitempty` back, this test fails on the byte comparison (the key disappears from the output) before the spec drift can land.
- **`TestMessageChunkPayload_RoundTrip`** — fixture `message_chunk.json` (≥2 messages, per AC). Asserts:
  - `len(payload.Messages) == 2`.
  - `payload.Messages[0].Role == "assistant"` AND `payload.Messages[1].Role == "user"` (covers that distinct role values round-trip).
  - `payload.Messages[1].MessageID == "m13"` (covers that distinct IDs round-trip — guards against a silent slice-aliasing or copy bug).
  - Round-trip byte-identical.
- **`TestBackfillDonePayload_RoundTrip`** — fixture `backfill_done.json`. Asserts `payload.Delivered == 47` AND `env.InReplyTo != nil && *env.InReplyTo == 6`. Round-trip byte-identical.

### What NOT to test

- `json.Marshal` / `json.Unmarshal` correctness for `string`, `int`, `*string`, `time.Time`, `[]struct{}`. Stdlib contract.
- Required-field-missing decode behaviour (decodes to zero value; not this layer's problem).
- Validation of `MessagePayload.Role` against the closed set `{user, assistant, system}`. NOT this package's job; the spec already says the field stays `string` here.
- Round-trip of `Envelope`-without-a-payload. Already covered by `TestEnvelope_RoundTrip_Minimal` in `envelope_test.go`.
- Cross-payload interactions (e.g. `send_message` followed by `message`). Dispatch-layer concern.
- Per-type constructors. None exist (AC: "No constructors, no methods").

### Helper reuse

`canonical` and `readFixture` live in `envelope_test.go:11-27`. Both are package-private (same `package protocol` test package); the new `messaging_test.go` reuses them by direct identifier reference. **DO NOT redefine.** A duplicated `canonical` would compile fine but the maintenance burden is real — the project has burned cycles on test-helper drift before. One copy.

## Out of scope (do not implement here)

- The other 11 v1 payload types (`hello`, `hello_ack`, `error`, `ack`, `list_conversations`, `conversations`, `create_conversation`, `conversation_created`, `promote_conversation`, `conversation_updated`, `register_push_token`). Sibling slices — #271 (handshake/control: hello, hello_ack, error, ack), #275 (push), and a conversations-slice ticket (TBD).
- A `Role` named type or any role-validation predicate. Stays `string` per AC.
- Token-by-token streaming (`message_chunk` is the wire shape only; the spec is explicit that v1 emits one finished message per `message` envelope, and `message_chunk` is reserved for backfill flow today + v1.1 streaming later).
- Backfill orchestration: chunk-size enforcement (the spec's "≤ 50 messages per envelope" default), ordering by `(conversation_id, ts)`, deciding when to send `backfill_done`. All dispatcher concerns, not framing.
- `Envelope.ID` monotonicity or `InReplyTo`-references-real-prior-ID validation. Per-connection state, not this layer's.
- A `MessageChunkPayload.Validate()` checking `len(Messages) >= 1` or similar. Wire allows empty; dispatcher decides.
- A `BackfillSincePayload.IsAllConversations() bool` predicate. One pointer-nil-check at the call site is fine.
- An `AllV1MessagingTypes []string` export. No consumer needs it.
- Adding any new `Type*` constant or wire-code constant. All five `Type*` constants this ticket consumes already exist in `codes.go`.
- Constructors / builders for any of the five DTOs.
- A `json.Marshaler` / `json.Unmarshaler` custom implementation on any of the five types. The default struct-tag-driven implementation suffices; a custom marshaler would be a YAGNI maintenance burden and would re-open the `*string` round-trip subtlety inside Go code rather than declaring it via tags.

## Open questions

None. Every AC item maps to an unambiguous code path:

- "New per-type payload structs in `internal/protocol/`" → `messaging.go` with five struct declarations above.
- "`json` tags matching the spec's snake_case field names" → tag list pinned line-by-line above.
- "`omitempty` on optional fields" → applies to zero fields in this ticket (see § "Why `conversation_id: null` is `omitempty` and how the round-trip stays byte-identical" — the AC's literal `omitempty` prescription is overridden by the spec's `null`-on-wire requirement for the one nullable field; no other field is optional).
- "Optional / nullable fields use `*T`" → `BackfillSincePayload.ConversationID` is `*string`. Sole instance.
- "`MessageChunkPayload.Messages` reuses `MessagePayload` directly" → struct definition above; one-line slice type, no helper struct.
- "Tests: per-type golden-file round-trip" → five test functions above.
- "Fixtures live under `internal/protocol/testdata/` as one JSON file per type" → file list above.
- "The `message_chunk` fixture must contain ≥2 messages" → fixture content above contains exactly 2 messages.
- "No new exported types beyond the five payload structs. No constructors, no methods" → design above adds exactly five exported identifiers, all struct types; nothing else.
