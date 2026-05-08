# #216 — Conversation type + JSON schema

Foundation ticket for the Phase 3 `conv:` series. Defines the on-disk shape for conversations as a pure Go type with `encoding/json` tags. No persistence, no consumers, no I/O.

## Files to read first

- `internal/sessions/registry.go:14-29` — `registryFile` / `registryEntry` shape: tag style (snake_case), `omitempty` placement, field-ordering convention. The new `Conversation` struct mirrors this style.
- `internal/sessions/id.go:1-32` — `SessionID` typedef pattern (`type SessionID string`) and its package-level doc comment treatment. The conversation `ID` field follows the same pattern as a `ConversationID` typedef.
- `internal/sessions/id.go:43-69` — `ValidID` shape, for context only. **Do not** port a `ValidConversationID` here — that belongs in #217 if/when it's needed; this ticket is type-only.
- `CODING-STYLE.md` — gofmt, doc comment style, table-driven test conventions.

## Context

Phase 3 introduces `Conversation` as a richer entity than `Session`: a conversation owns a current claude session plus a history of prior sessions, and carries a promotion bit (discussion vs. channel) along with display metadata. This ticket lands the type alone — registry CRUD (#217), promotion API (#218), and auto-archive (#219, #220) build on it.

The package is greenfield (`internal/conversations`); the existing `internal/sessions` package is untouched. A migration ticket (TBD) will reconcile the two later.

## Design

### Package layout

```
internal/conversations/
  conversation.go         New: Conversation struct + ConversationID typedef
  conversation_test.go    New: round-trip JSON tests
```

One package, one production file, one test file. Pure type definition — no exported functions, no constructors, no I/O.

### Types

```go
// Package conversations defines the Phase 3 Conversation entity: a long-lived
// thread that owns a sequence of underlying claude sessions and carries
// presentation metadata (name, promoted/unpromoted state).
//
// This package is intentionally I/O-free. Persistence (sessions.json-style
// registry on disk) lands in #217.
package conversations

import "time"

// ConversationID is a per-conversation identifier. Distinct from
// sessions.SessionID so that a value of one type cannot be silently passed
// where the other is expected — a Conversation carries both its own ID and a
// CurrentSessionID, and confusing them is the most plausible bug in the
// upcoming registry/API code.
//
// The empty ConversationID ("") is the unset sentinel. Format conventions
// (UUIDv4 vs. other) are not fixed here; #217 owns the generator and the
// validity predicate.
type ConversationID string

// Conversation is the on-disk and in-memory shape of a Phase 3 conversation.
// Field tags are snake_case to match the existing sessions registry style
// (internal/sessions/registry.go).
//
// Field ordering is preserved exactly as the AC requires; do not re-order to
// optimize struct padding — the JSON encoding is the source of truth and
// reviewer diff stability matters more than a few padding bytes per record.
type Conversation struct {
    // ID is the conversation's stable identifier. Always present.
    ID ConversationID `json:"id"`

    // Name is the user-visible display name. A pointer so that "absent"
    // (nil — the user has never named this conversation) is distinguishable
    // from "explicitly empty" (non-nil pointer to ""). Unpromoted
    // conversations typically leave this nil; promoted conversations
    // (channels) usually carry a name.
    Name *string `json:"name,omitempty"`

    // Cwd is the absolute working directory captured at conversation
    // creation time. Always present; never updated after creation.
    Cwd string `json:"cwd"`

    // CurrentSessionID is the underlying claude session this conversation
    // currently points at. Empty string when no session is bound (e.g., a
    // freshly created conversation that has not yet been started, or one
    // whose session has been archived). Empty values are omitted from the
    // JSON output.
    CurrentSessionID string `json:"current_session_id,omitempty"`

    // SessionHistory is the ordered list of prior session IDs that this
    // conversation has pointed at, in **chronological (oldest-first)** order.
    // The most recently retired session sits at the tail
    // (SessionHistory[len-1]); rotation appends in place
    // (append(SessionHistory, prevID)). Chronological ordering is chosen
    // because it matches the natural append pattern and avoids O(n) shifts
    // on every rotation; presentation layers that want newest-first can
    // reverse on read. An empty/nil slice is omitted from JSON output.
    SessionHistory []string `json:"session_history,omitempty"`

    // IsPromoted distinguishes the two conversation modes:
    //   false — discussion (ephemeral, eligible for auto-archive)
    //   true  — channel    (long-lived, named, exempt from auto-archive)
    // Always serialized; the field is meaningful in both states and the
    // unpromoted default ("discussion") must be explicit on disk.
    IsPromoted bool `json:"is_promoted"`

    // LastUsedAt is bumped whenever the conversation has user activity.
    // Used by "recently active" sorts and by the auto-archive predicate
    // (#219). Always present.
    LastUsedAt time.Time `json:"last_used_at"`
}
```

### Decisions called out in the AC

The ticket left two choices to the architect:

1. **`ID` is a typedef (`ConversationID`), not a bare `string`.** A conversation carries both its own ID and an embedded `CurrentSessionID`; both being plain `string` makes them swappable at any call site. Distinct nominal types make the swap a compile error. Cost is one extra line and the occasional `ConversationID(s)` conversion at the package boundary — accepted.

2. **`SessionHistory` is oldest-first (chronological).** Rationale embedded in the field comment above: matches the `append` pattern naturally; reversal at presentation time is O(n) once, vs. O(n) on every rotation if newest-first is stored.

### Data flow

None in this ticket. The struct is data; consumers (#217 registry, #218 promotion, #219+ auto-archive) wire it up later.

## Concurrency model

None. Pure value type — no goroutines, no channels, no mutexes. Safe to copy by value; safe to pass across goroutines provided the usual "no concurrent mutation of `SessionHistory` slice header" Go rules are followed by the future owner (#217's registry).

## Error handling

None. `encoding/json` handles its own errors at the call site; this file defines no functions.

## Testing strategy

`internal/conversations/conversation_test.go` is table-driven, stdlib only.

### Round-trip cases (must cover at minimum)

1. **Promoted, named, non-empty history.** Name is a non-nil pointer to "general"; `IsPromoted` true; `CurrentSessionID` non-empty; `SessionHistory` non-empty (≥2 entries to verify ordering is preserved).
2. **Unpromoted, unnamed, no history.** Name nil; `IsPromoted` false; `CurrentSessionID` ""; `SessionHistory` nil.

For each row:
- Marshal to JSON via `json.Marshal`.
- Unmarshal back into a fresh `Conversation`.
- Assert `reflect.DeepEqual(in, out)`.

`time.Time` round-trip caveat: marshaling and unmarshaling drops the monotonic clock reading. Construct test inputs with `time.Date(...)` (no monotonic component) so `DeepEqual` succeeds — the existing `internal/sessions` tests use this idiom; mirror it.

`SessionHistory == nil` vs. `[]string{}` round-trips as nil under `omitempty` (the key is absent on the wire). Set the input to `nil` (not `[]string{}`) for the unpromoted case so `DeepEqual` holds.

### Omitempty assertion (case 2 only)

After marshaling the unpromoted case, assert that the JSON bytes do **not** contain `"name"`, `"current_session_id"`, or `"session_history"`. Use `bytes.Contains` against a literal byte slice for each key. Keep the assertion straightforward — no JSON re-parsing needed since these field names are unique within the struct.

### Always-present assertion

Spot-check on the same unpromoted case that `"id"`, `"cwd"`, `"is_promoted"`, and `"last_used_at"` **are** present in the marshaled bytes. This locks in the "must always appear" half of the AC, which is otherwise easy to break by accidentally adding `,omitempty` later.

## Open questions

None blocking. Two notes for downstream tickets:

- **`ConversationID` validity predicate.** This ticket does not define `ValidID`. #217 (registry CRUD) will need one and should mirror `sessions.ValidID` if conversation IDs are also UUIDv4. Decision deferred to #217's architect.
- **Coupling to `sessions.SessionID`.** `CurrentSessionID` and `SessionHistory[i]` are typed as `string`, not `sessions.SessionID`, to keep `internal/conversations` decoupled from `internal/sessions` while Phase 3 lives alongside Phase 1/2. If a future refactor unifies the two packages, those fields can be retyped without touching the JSON wire format.
