# Spec: v1 conversations-read payload structs (#273)

## Context

Phase 3 Track C — the conversations-read slice of the v1 payload catalog. The framing primitives (`Envelope`, `RoutingEnvelope`, error/type-name consts, `IsV1Compatible`) already live in `internal/protocol/`. This ticket adds the per-type Go structs for `list_conversations` and `conversations` so the future dispatch handler decodes typed values out of `Envelope.Payload (json.RawMessage)` instead of re-deriving the shape ad hoc.

Shapes are fixed verbatim by `docs/protocol-mobile.md` § `list_conversations` and § `conversations`. The example payloads in those subsections are this ticket's golden fixtures, end of story. Pure DTOs: no I/O, no methods, no constructors.

## Files to read first

- `docs/protocol-mobile.md:331-369` — `list_conversations` (empty payload) and `conversations` (array of summaries with `name: null` on one row and a string on another) subsections. Both example envelopes are copied verbatim into testdata. Read first; everything else is plumbing.
- `internal/protocol/envelope.go:11-30` — package doc + `Envelope` struct. The new file lives in the same package and mirrors this file's tone (struct-only, no methods, doc comments that point at the spec).
- `internal/protocol/codes.go:48-50` — `TypeListConversations` and `TypeConversations` constants already declared. Use these in the tests (`env.Type == TypeListConversations` / `TypeConversations`), do **not** hardcode the strings.
- `internal/protocol/push.go:1-19` — closest sibling DTO (just merged via #275). Same package; same shape constraints (struct + json tags only). Mirror its doc-comment style: lead with what the frame is, cite the spec section, note any non-obvious wire-type choice.
- `internal/protocol/push_test.go:9-41` — golden round-trip test mirror. The new test file in the same package reuses `canonical` and `readFixture` (defined in `envelope_test.go:11-27`) without imports.
- `internal/protocol/testdata/register_push_token.json` and `testdata/envelope_full.json` — formatting reference: single line, compact, no extraneous whitespace; `os.ReadFile` preserves the trailing newline byte if present, which `json.Compact` strips, so byte-equality is on the *compacted* form (already handled by the `canonical` helper).

## Design

### Production code

New file `internal/protocol/conversations_read.go`. Three structs, no methods, no constructors. All in one file because the spec's `list_conversations` / `conversations` are a single request/response pair and the row type (`ConversationSummary`) is only referenced from `ConversationsPayload`.

```go
package protocol

import "time"

// ListConversationsPayload is the body of a list_conversations frame
// (docs/protocol-mobile.md § list_conversations). Phone → binary. The
// payload is empty by spec; the type exists so the dispatcher can decode
// into a concrete value rather than a json.RawMessage.
type ListConversationsPayload struct{}

// ConversationsPayload is the body of a conversations frame
// (docs/protocol-mobile.md § conversations). Binary → phone, sent in
// reply to a list_conversations request. Order of the Conversations
// slice is preserved from the wire — the binary is the source of truth
// for ordering (e.g. most-recently-used first); this type does not
// reorder.
type ConversationsPayload struct {
	Conversations []ConversationSummary `json:"conversations"`
}

// ConversationSummary is one row of a ConversationsPayload
// (docs/protocol-mobile.md § conversations). Name is a pointer because
// the spec admits null on the wire (an unnamed scratch conversation);
// see the design note in 273-conversations-read-payloads.md for why it
// is *not* declared with json:",omitempty".
type ConversationSummary struct {
	ID            string    `json:"id"`
	Name          *string   `json:"name"`
	IsPromoted    bool      `json:"is_promoted"`
	Cwd           string    `json:"cwd"`
	LastMessageTS time.Time `json:"last_message_ts"`
	LastUsedAt    time.Time `json:"last_used_at"`
}
```

### `omitempty` on `Name`: deliberate deviation from AC

The ticket body asks for `*string` + `omitempty` on `Name`. Those two together are *incompatible* with the byte-equivalent round-trip the same AC requires, given the spec fixture:

- The spec example for `conversations` includes a row with `"name": null` explicitly on the wire.
- Unmarshalling `"name": null` into `*string` produces a nil pointer.
- Re-marshalling a nil pointer with `,omitempty` *omits* the `"name"` key entirely.
- Result: `{"name": null, ...}` → decode → encode → `{...}` (no `name` key). Bytes differ; round-trip test fails.

Two reconciliations exist:

1. Drop `,omitempty` on `Name`. Nil pointer marshals as `null`, preserving the spec's explicit `null` on the wire. Byte round-trip succeeds.
2. Keep `,omitempty` and rewrite the fixture so the unnamed row omits `"name"` instead of carrying `null`. Diverges from the spec example.

This spec picks (1). Rationale: `docs/protocol-mobile.md` is the source of truth for wire format and shows `"name": null` literally; changing the fixture would silently drift the Go DTOs away from the documented wire shape. The mobile client (TypeScript) reading these frames would also need to handle `null` regardless, so the Go type matching `null` is the conservative choice. The `,omitempty` was almost certainly an authoring slip in the ticket body, not a load-bearing requirement.

This is the only AC deviation in this spec. All other fields follow the ticket body exactly.

### `time.Time` round-trip

The spec example uses `"2026-05-08T10:31:02Z"` — RFC 3339, no fractional seconds. Go's `time.Time.MarshalJSON` uses `RFC3339Nano`, which omits a fractional component when none is present, so a parsed `2026-05-08T10:31:02Z` re-marshals byte-identically. No custom marshaller needed. The AC's mention of "RFC 3339 nano" is the *upper bound* of precision the wire carries, not a requirement to always emit fractional seconds.

### Fixtures

Two new files under `internal/protocol/testdata/`, both single-line compact JSON matching the formatting of `envelope_full.json`.

`testdata/list_conversations.json`:

```json
{"id":3,"type":"list_conversations","ts":"2026-05-08T10:33:14.012Z","payload":{}}
```

`testdata/conversations.json` (verbatim from spec § conversations, compacted; preserves both rows — one with `name` set, one with `name: null`):

```json
{"id":250,"type":"conversations","ts":"2026-05-08T10:33:14.012Z","in_reply_to":3,"payload":{"conversations":[{"id":"c1...","name":"kitchen-claw refactor","is_promoted":true,"cwd":"/Users/juhana/Workspace/Projects/KitchenClaw","last_message_ts":"2026-05-08T10:31:02Z","last_used_at":"2026-05-08T10:31:02Z"},{"id":"c2...","name":null,"is_promoted":false,"cwd":"/Users/juhana/pyry-workspace/scratch","last_message_ts":"2026-05-08T09:14:11Z","last_used_at":"2026-05-08T09:14:11Z"}]}}
```

Notes:

- `id`, `type`, `in_reply_to`, `ts` values are copied from the spec example. The `ts` on the spec example is shown as `"..."`; I substitute the same `2026-05-08T10:33:14.012Z` used by the existing fixtures, for corpus consistency. There is no semantic constraint on envelope `ts` here.
- `in_reply_to: 3` is present on the `conversations` fixture (matches the spec example: the response references the request that ID-3 issued).
- Field order inside each conversation object matches the spec example: `id, name, is_promoted, cwd, last_message_ts, last_used_at`. Go's `encoding/json` emits struct fields in declaration order, so the struct field order in `ConversationSummary` (above) is chosen to match — this is what makes byte-equality survive re-marshalling.

### Test

New file `internal/protocol/conversations_read_test.go`, same package as `envelope_test.go`/`push_test.go`. Reuses `canonical` and `readFixture` directly.

Two test functions, each mirroring `TestRegisterPushTokenPayload_RoundTrip`:

```go
func TestListConversationsPayload_RoundTrip(t *testing.T) {
	raw := readFixture(t, "list_conversations.json")

	var env Envelope
	if err := json.Unmarshal(raw, &env); err != nil {
		t.Fatalf("unmarshal envelope: %v", err)
	}
	if env.Type != TypeListConversations {
		t.Errorf("Type: got %q, want %q", env.Type, TypeListConversations)
	}

	var p ListConversationsPayload
	if err := json.Unmarshal(env.Payload, &p); err != nil {
		t.Fatalf("unmarshal payload: %v", err)
	}

	out, err := json.Marshal(env)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	if !bytes.Equal(canonical(t, out), canonical(t, raw)) {
		t.Errorf("round-trip bytes differ:\n got: %s\nwant: %s", out, raw)
	}
}

func TestConversationsPayload_RoundTrip(t *testing.T) {
	raw := readFixture(t, "conversations.json")

	var env Envelope
	if err := json.Unmarshal(raw, &env); err != nil {
		t.Fatalf("unmarshal envelope: %v", err)
	}
	if env.Type != TypeConversations {
		t.Errorf("Type: got %q, want %q", env.Type, TypeConversations)
	}

	var p ConversationsPayload
	if err := json.Unmarshal(env.Payload, &p); err != nil {
		t.Fatalf("unmarshal payload: %v", err)
	}
	if len(p.Conversations) != 2 {
		t.Fatalf("Conversations: got len %d, want 2", len(p.Conversations))
	}

	// Row 0: name set.
	c0 := p.Conversations[0]
	if c0.ID != "c1..." || c0.Cwd != "/Users/juhana/Workspace/Projects/KitchenClaw" || !c0.IsPromoted {
		t.Errorf("row 0 scalar fields: %+v", c0)
	}
	if c0.Name == nil || *c0.Name != "kitchen-claw refactor" {
		t.Errorf("row 0 Name: got %v, want pointer to %q", c0.Name, "kitchen-claw refactor")
	}

	// Row 1: name null on wire → nil pointer.
	c1 := p.Conversations[1]
	if c1.ID != "c2..." || c1.Cwd != "/Users/juhana/pyry-workspace/scratch" || c1.IsPromoted {
		t.Errorf("row 1 scalar fields: %+v", c1)
	}
	if c1.Name != nil {
		t.Errorf("row 1 Name: got pointer to %q, want nil (wire was null)", *c1.Name)
	}

	out, err := json.Marshal(env)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	if !bytes.Equal(canonical(t, out), canonical(t, raw)) {
		t.Errorf("round-trip bytes differ:\n got: %s\nwant: %s", out, raw)
	}
}
```

The byte-equality check on the `conversations` fixture is what proves *both* `*string` branches survive the round-trip in their wire-correct forms (name=null stays `null`, name=set stays a string). That is the central invariant of this ticket; everything else is sugar.

Why decode-from-`Envelope.Payload` rather than decode-from-raw-payload-bytes: the AC requires validating that the payload struct slots into `Envelope.Payload (json.RawMessage)` once the dispatcher reads `Envelope.Type`. Testing the full envelope round-trip exercises that exact path. A standalone payload-only round-trip would not.

Why no separate "unknown field rejection" or "missing field" tests: stdlib `encoding/json` is permissive by default, and the ticket explicitly scopes those concerns out (dispatcher's job). Golden round-trips are the contract.

## Concurrency model

None. Pure data types. No goroutines, no `context.Context`, no channels.

## Error handling

None at this layer. `json.Unmarshal` returns its own errors to callers; these types add no error semantics. Validation (e.g. `IsPromoted == true` requiring `Name != nil`) belongs to the dispatcher, not the wire types.

## Testing strategy

Two golden-file round-trips (defined above). Together they verify:

1. Both fixtures decode cleanly into `Envelope`.
2. `Envelope.Payload` decodes cleanly into `ListConversationsPayload` / `ConversationsPayload`.
3. `ConversationSummary` recovers all scalar fields including both `Name == nil` and `Name != nil` branches.
4. Re-marshalling the envelopes produces bytes that compact-equal the originals (the critical invariant for wire types).

Run locally with:

```bash
go test -race ./internal/protocol/
```

CI's existing `go vet` / `staticcheck` / `go test -race` matrix already covers this package; no workflow changes needed.

## Open questions

None. The only architect-discretion calls were (a) file layout — one file vs. three — resolved to one file (`conversations_read.go`) because the request/response pair is a single conceptual unit and total LOC stays well under any split threshold; and (b) the `omitempty` / `null` tension on `ConversationSummary.Name`, resolved by dropping `omitempty` so the spec's explicit-`null` wire shape round-trips byte-equivalently (full reasoning under § Design above).
