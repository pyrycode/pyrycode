# `internal/conversations` package

Pure type definition for the Phase 3 `Conversation` entity. Greenfield package alongside the existing `internal/sessions` registry; no I/O, no consumers, no constructors. Foundation for the `conv:` ticket series (#217 registry CRUD, #218 promotion API, #219 auto-archive predicate, #220 auto-archive sweep).

## What it is

A `Conversation` is a long-lived thread that owns a sequence of underlying claude sessions and carries presentation metadata (display name, promoted/unpromoted state, working directory). It is a richer entity than `Session`: the conversation lives across session rotations, where a `Session` is the underlying claude process + JSONL.

## Files

```
internal/conversations/
  conversation.go         Conversation struct + ConversationID typedef
  conversation_test.go    Round-trip JSON tests + omitempty assertions
```

One package, two files, ~75 LOC production + ~100 LOC test. Stdlib only (`time` for the timestamp; `encoding/json` is consumed by tests, not the production type).

## Types

### `ConversationID string`

Per-conversation identifier. **Distinct nominal type** from `sessions.SessionID` so a value of one cannot be silently passed where the other is expected — a `Conversation` carries both its own ID and a `CurrentSessionID`, and confusing the two is the most plausible bug in the upcoming registry/API code. The empty `ConversationID("")` is the unset sentinel. Format conventions (UUIDv4 vs. other) are not fixed here; #217 owns the generator and the validity predicate.

### `Conversation`

| Field | Type | JSON tag | Always present? |
|-------|------|----------|-----------------|
| `ID` | `ConversationID` | `id` | yes |
| `Name` | `*string` | `name,omitempty` | no — pointer distinguishes nil ("never named") from `""` |
| `Cwd` | `string` | `cwd` | yes — captured at creation, never updated |
| `CurrentSessionID` | `string` | `current_session_id,omitempty` | no — empty when no session is bound |
| `SessionHistory` | `[]string` | `session_history,omitempty` | no — empty/nil omitted |
| `IsPromoted` | `bool` | `is_promoted` | yes — `false` = discussion, `true` = channel |
| `LastUsedAt` | `time.Time` | `last_used_at` | yes — bumped on user activity |

`Name`, `CurrentSessionID`, `SessionHistory` carry `,omitempty` so unpromoted/unnamed conversations and conversations with no session bound serialize without those keys. `IsPromoted`, `Cwd`, `ID`, `LastUsedAt` do **not** use `omitempty` — they must always appear, even at zero value. The `IsPromoted: false` default ("discussion") must be explicit on disk.

## Decisions

### `ID` is `ConversationID`, not bare `string`

A conversation carries both its own ID and an embedded `CurrentSessionID`; both being plain `string` makes them swappable at any call site. Distinct nominal types make the swap a compile error. Cost is one extra line and the occasional `ConversationID(s)` conversion at the package boundary — accepted. Mirrors the `sessions.SessionID` pattern from `internal/sessions/id.go`.

### `SessionHistory` is chronological (oldest-first)

Rotation appends in place: `append(SessionHistory, prevID)`. Chronological ordering matches the natural append pattern and avoids O(n) shifts on every rotation; presentation layers that want newest-first reverse on read (one O(n) traversal per render, vs. O(n) shifts on every rotation). The most recently retired session sits at the tail (`SessionHistory[len-1]`).

### `Name` is `*string`, not `string`

Pointer distinguishes "absent" (nil — the user has never named this conversation) from "explicitly empty" (non-nil pointer to `""`). Unpromoted conversations typically leave this nil; promoted conversations (channels) usually carry a name. The future `pyry conv name <id> ""` UX would set a non-nil empty string; "rename to nothing" and "never named" are different states.

### `CurrentSessionID` and `SessionHistory[i]` are `string`, not `sessions.SessionID`

Keeps `internal/conversations` decoupled from `internal/sessions` while Phase 3 lives alongside Phase 1/2. If a future refactor unifies the two packages, those fields can be retyped without touching the JSON wire format.

## Concurrency

None. Pure value type — no goroutines, no channels, no mutexes. Safe to copy by value. Safe to pass across goroutines provided the usual "no concurrent mutation of `SessionHistory` slice header" Go rules are followed by the future owner (#217's registry).

## Tests

`conversation_test.go` is table-driven, stdlib only, same-package.

- **`TestConversation_JSONRoundTrip`** — two cases: promoted + named + non-empty history (≥2 entries to verify ordering), and unpromoted + unnamed + nil history. For each: `json.Marshal` → `json.Unmarshal` → `reflect.DeepEqual(in, out)`. Test inputs use `time.Date(...)` (no monotonic clock component) so `DeepEqual` on `time.Time` survives the round-trip, per `lessons.md` § "JSON roundtrip strips monotonic-clock state".
- **`TestConversation_OmitemptyAbsentForUnpromoted`** — marshals an unpromoted, unnamed, no-history conversation; asserts `"name"`, `"current_session_id"`, `"session_history"` are absent from the bytes (via `bytes.Contains`); spot-checks that `"id"`, `"cwd"`, `"is_promoted"`, `"last_used_at"` **are** present (locks in "must always appear" half of the AC, easy to break by accidentally adding `,omitempty` later).

`SessionHistory == nil` round-trips to nil under `omitempty`; the unpromoted test case sets the input to `nil` (not `[]string{}`) so `DeepEqual` holds.

## Out of scope

- Persistence — #217 lands `~/.pyry/<name>/conversations.json` registry CRUD with the same atomic-rename + `0600` recipe `internal/sessions/registry.go` and `internal/devices/registry.go` use.
- Validity predicate (`ValidConversationID`) — #217's concern; mirror `sessions.ValidID` if conversation IDs are also UUIDv4.
- Promotion API (`pyry conv promote`, `pyry conv name`) — #218.
- Auto-archive predicate + sweep — #219, #220.
- Migration from existing `Session` registry — TBD ticket once Conversations is proven on disk. Phase 1/2 sessions stay untouched.
- Schema versioning — defer until first migration.

## Related

- [`internal/sessions`](sessions-package.md) — the existing `Session` model. Lives alongside `internal/conversations`; not coupled.
- [`internal/sessions/id.go`](../../../internal/sessions/id.go) — `SessionID` typedef pattern this `ConversationID` mirrors.
- [`internal/sessions/registry.go`](../../../internal/sessions/registry.go) — JSON tag style (snake_case, `omitempty` placement) the `Conversation` struct follows.
- [`docs/specs/architecture/216-conversation-type.md`](../../specs/architecture/216-conversation-type.md) — architect's spec for this ticket.
