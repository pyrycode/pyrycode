# #880 — Durable archived state on conversations, surfaced in the list read

## Files to read first

- `internal/conversations/conversation.go:29-72` — the `Conversation` struct. Add the new
  `IsArchived` flag here; note the field-ordering comment (don't re-order existing fields) and
  contrast the `IsPromoted` tag (no `omitempty`) with what this field needs (`omitempty` — see Design).
- `internal/conversations/registry.go:139-189` — `ListFilter`, the `List` filter loop, and the
  `Update` helper. Extend `ListFilter` with `IsArchived *bool` and add the mirror filter check in `List`.
- `internal/conversations/registry.go:191-300` — `RebindSession`, `Delete`, `Promote`. These are the
  package's **semantic single-field mutators** returning hit/miss; model the new `SetArchived` on them
  (lock → scan → mutate one field → return bool; no `Save`).
- `internal/conversations/registry.go:63-116` — `Save`/`Load`. `Save` sorts by `LastUsedAt` then `ID`
  and re-encodes from the in-memory slice; this is what gives the flag its round-trip durability for free.
  No `created_at` field exists (per the ticket note) — the sort key is `last_used_at`, `id`.
- `internal/protocol/conversations_read.go:21-34` — `ConversationSummary`. Add `IsArchived bool`
  here; mirror the `IsPromoted` tag treatment (always serialized, **no** `omitempty`).
- `internal/relay/handlers/list_conversations.go:37-51` — the projection loop. Add one line mapping
  `conv.IsArchived` into the summary. The handler stays **unfiltered** (`reg.List()` with no args).
- `internal/conversations/registry_test.go:455-480` — the existing `List` filter table + the `ptrTo`
  helper. Extend this table with the `IsArchived` cases rather than writing a new test harness.
- `internal/relay/handlers/list_conversations_test.go:73-160` — `TestListConversations_*`; reuse this
  harness (fake lister + reply capture) for the archived-surfacing test.
- `internal/protocol/compat_test.go:12-15` — confirms only type-string constants are frozen for the
  conversations frames; there is **no** golden byte assertion on `ConversationSummary`, so adding a
  wire key is safe.

## Context

The conversation registry (`internal/conversations`) has no persisted archived state. Auto-archive
(`Sweep` / `ShouldArchive`, #219) permanently `Delete`s idle discussions — irreversible. To support
user-initiated archive/restore (#881), a conversation must instead carry a **durable archived flag**
that survives reload and can be flipped back to active, and that flag must be visible in the
`list_conversations` read so a paired client can render active vs. archived lists and live counts
without a second query.

This slice ships **only** the durable state and its read surface. The
`archive_conversation` / `unarchive_conversation` wire verbs that flip the flag are #881, which
depends on and consumes this slice's `SetArchived` primitive. Split from #821.

Auto-archive semantics are **out of scope**: do not change `Sweep`/`ShouldArchive` to set the flag
instead of deleting. Reconciling auto-archive with soft-archive is a separate concern.

Not `security-sensitive` — this slice adds durable state + a read projection; the untrusted-inbound
surface (the verbs that flip the flag) lives in #881, which carries the label.

## Design

Four additive touch points. Nothing is renamed, no signature changes, no consumer cascade.

### 1. `Conversation.IsArchived bool` — the durable flag (`conversation.go`)

Add a single field after `IsPromoted`, before `LastUsedAt`, grouping the two state flags:

```go
IsArchived bool `json:"is_archived,omitempty"`
```

**Tag choice — `omitempty`, deliberately unlike `IsPromoted`.** AC1 requires "an absent key decodes
as active, with no migration step." `omitempty` delivers exactly that in both directions:

- **Decode:** a pre-existing on-disk row with no `is_archived` key decodes to `false` (Go zero value) =
  active. No migration pass, no default-injection.
- **Encode / byte-stability:** an active conversation (`false`) serializes with the key **omitted**, so
  a registry of all-active rows is byte-identical to its pre-#880 form. Only genuinely-archived rows
  gain `"is_archived": true`. This preserves the "byte-stable reload discipline" the ticket calls out.

`IsPromoted` deliberately drops `omitempty` because that field's product contract is "the unpromoted
default must be explicit on disk." `IsArchived`'s contract is the opposite — "absent == active, no
migration" — so `omitempty` is not just acceptable, it is what satisfies AC1. Call this out in a field
comment so a future reader doesn't "fix" the inconsistency.

### 2. `ListFilter.IsArchived *bool` + `List` narrowing (`registry.go`)

Extend the filter struct with a second nil-pointer field, mirroring `IsPromoted`:

```go
type ListFilter struct {
	IsPromoted *bool
	IsArchived *bool
}
```

In `List`, add the mirror check next to the existing `IsPromoted` guard: a non-nil `IsArchived`
skips rows whose `IsArchived` differs. Two set fields **AND** naturally (an already-true property of
the single-`ListFilter` shape; unrelated to the "only `filter[0]` is consulted" variadic rule, which
is about multiple `ListFilter` args). Result:

- `IsArchived == nil` → both active and archived returned (today's behavior; **AC3** "unfiltered
  returns both").
- `IsArchived == ptrTo(false)` → active only.
- `IsArchived == ptrTo(true)` → archived only.

### 3. `Registry.SetArchived(id, archived) bool` — the mutator seam (`registry.go`)

New method, modeled on `Delete` / `RebindSession`:

```go
func (r *Registry) SetArchived(id ConversationID, archived bool) bool
```

- **Behavior:** under `r.mu`, scan for the row whose `ID == id`; on hit, set `IsArchived = archived`
  and return `true`; on miss, return `false` and mutate nothing (**AC4**: "miss for an unknown id,
  leaving the registry unmodified on miss").
- **Flips exactly one field.** This is the deterministic enforcement of the ticket's "must not change
  id, cwd, name, or session binding" constraint — the method structurally cannot touch another field,
  so #881's verb handler can't get it wrong. (Belt-and-suspenders: the invariant lives in code, not in
  a convention the downstream handler must remember.)
- **Idempotent / symmetric:** `SetArchived(id, true)` on an already-archived row returns `true` and
  leaves it archived; the single `archived bool` arg both sets and clears — one method, symmetric
  toggle, no separate `Archive`/`Unarchive` pair.
- **No `Save`.** Persistence is the caller's concern, matching `Create` / `Update` / `Promote` /
  `Delete` / `RebindSession`.

**Why a dedicated method rather than reusing `Update(id, fn)`.** `Update` already returns the same
hit/miss contract, and `SetArchived` could be expressed as `Update(id, func(c){ c.IsArchived = a })`.
But (a) it hands the caller free rein over every field, so the "flips exactly one field" guarantee
would live only in #881's closure (stochastic); (b) the package's established idiom for
named-semantic mutations is a dedicated method (`Promote`, `RebindSession`) — `Update` is the
escape hatch, not the default. A ~10-line `SetArchived` makes #881 a one-line call and keeps the
single-field invariant deterministic. This is the recommended shape.

### 4. `ConversationSummary.IsArchived` + handler projection (`conversations_read.go`, `list_conversations.go`)

Add to the wire summary, mirroring `IsPromoted` (always serialized, **no** `omitempty` — the client
must be able to read the flag for active rows too, to partition and count both sides):

```go
IsArchived bool `json:"is_archived"`
```

Place it after `IsPromoted` in the struct. In the handler projection loop, add
`IsArchived: conv.IsArchived`. The handler continues to call `reg.List()` **with no filter**, so the
reply carries both active and archived rows, each tagged (**AC5**: archived conversations appear in
the reply, tagged, so the client partitions active vs. archived and counts each). Do **not** add a new
read verb and do **not** filter in the handler — the `ListFilter.IsArchived` narrowing (item 2) is the
registry-level capability for other consumers/tests, distinct from this always-both reply.

### Data flow

```
#881 verb handler ──SetArchived(id,true)──> Registry (in-mem flip) ──Save──> conversations.json
                                                                              (is_archived:true only
                                                                               on archived rows)
        reload ──> Load ──> Registry (absent key → active; present true → archived)

client ──list_conversations──> ListConversations handler ──reg.List() [both]──> project each row
        <──conversations (each row tagged is_archived)──────────────────────────┘
```

## Concurrency model

No new goroutines. `SetArchived` scans and mutates under the existing `r.mu` (same lock discipline as
`Delete`/`RebindSession`) — find-then-mutate is atomic, no window a concurrent `Create`/`Delete` could
redirect. `List` copies out under the lock as today. `Save` snapshots under the lock, sorts and encodes
outside it — unchanged.

## Error handling

No new error values. `SetArchived` reports miss via `bool` (matching `Delete`/`RebindSession`), not a
sentinel — there is no partial-failure or validation branch to distinguish. The handler's existing
`json.Marshal`-error path is unchanged. `Load` of a row with an absent `is_archived` key is not an
error — it decodes to the active default (this is the "no migration" contract, not a failure mode).

## Testing strategy

Extend existing tables/harnesses rather than adding new files. Scenarios (developer writes bodies in
the project idiom):

**`internal/conversations` (registry_test.go):**
- **Round-trip durability (AC2):** Create a mix of archived and active conversations → `Save` → `Load`
  → each reloads with its `IsArchived` preserved. Compare `time.Time` fields via `.Equal`.
- **Absent-key decodes active (AC1):** `Load` a hand-written registry JSON whose row omits
  `is_archived` → that row's `IsArchived == false`. No migration invoked.
- **Byte-stable active encode (AC1):** an all-active registry `Save` emits bytes containing no
  `is_archived` key (omitempty); a `Save`→`Load`→`Save` is byte-identical.
- **`SetArchived` hit sets + clears (AC4):** `SetArchived(id, true)` → `true`, `Get` shows
  `IsArchived == true`; assert `ID`, `Cwd`, `Name`, `CurrentSessionID`, `SessionHistory`, `IsPromoted`
  are unchanged (the single-field guarantee). Then `SetArchived(id, false)` → `true`, flag cleared.
- **`SetArchived` miss (AC4):** `SetArchived(unknownID, true)` → `false`; registry length and a
  sentinel row are untouched.
- **`SetArchived` idempotent:** two `true` calls → both `true`, flag stays true.
- **List filter (AC3):** extend the existing `ListFilter` table with `IsArchived: nil` (both),
  `ptrTo(true)` (archived only), `ptrTo(false)` (active only), and one combined
  `{IsPromoted: ptrTo(x), IsArchived: ptrTo(y)}` case to pin the AND semantics.

**`internal/protocol` (conversations_read_test.go):**
- `ConversationsPayload` with one archived + one active row marshals with `"is_archived": true` and
  `"is_archived": false` both present, and round-trips.

**`internal/relay/handlers` (list_conversations_test.go):**
- A registry containing both active and archived conversations → the `conversations` reply includes
  both rows, each carrying the correct `is_archived` (the client-partition seam). Reuse the existing
  fake lister + reply-capture harness.

## Open questions

- **`protocol-mobile.md` `conversations` example** gains an `is_archived` key on each row. That doc is
  a protocol reference under `docs/`, not a developer deliverable — the documentation phase updates it
  from this spec + the merged diff. Do **not** add it as a developer AC.
- **Filter surface for the handler:** this slice keeps the `list_conversations` reply always-both.
  If a future client wants a server-narrowed archived-only read, it rides on `ListFilter.IsArchived`
  (already built here) via a query field on `ListConversationsPayload` — out of scope now, noted so
  #881 or a later ticket doesn't re-derive it.
