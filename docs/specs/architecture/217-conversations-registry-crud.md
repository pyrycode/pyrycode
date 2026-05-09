# #217 — `conv: conversations.json` registry CRUD

Phase 3 foundation. Adds on-disk persistence for the `Conversation` entity introduced in #216, mirroring the `internal/devices` registry pattern (atomic write + mutex-guarded in-memory state).

## Files to read first

- `internal/conversations/conversation.go` — entity + `ConversationID` type. Note the comment at line 18–19: "#217 owns the generator and the validity predicate." This spec satisfies that.
- `internal/devices/registry.go:1-155` — the reference implementation. Same envelope shape, same atomic-write recipe, same mutex discipline. Copy structure verbatim and rename.
- `internal/devices/registry_test.go:1-355` — the test file to mirror. Cover the same eight scenarios (load-missing, load-empty, load-malformed, round-trip, remove-present, remove-absent, find-by-X, file-permissions, stable-ordering, atomic-rename-preserves-old, concurrent-rw) adapted to the conversation-specific verbs.
- `internal/sessions/id.go:1-70` — the `NewID` / `ValidID` template. The conversation ID generator is a byte-for-byte clone with `SessionID` → `ConversationID`.
- `CODING-STYLE.md` — gofmt, `log/slog` (not used in this package — pure data layer), error wrapping (`fmt.Errorf("...: %w", err)`), `context.Context` is **not** required here (registry methods are sync, in-memory, never block on I/O after Load/Save returns).

## Context

`internal/conversations/conversation.go` defined the on-disk record shape but is I/O-free. This ticket adds:

1. ID generation (`NewID`, `ValidID`) — referenced by the conversation type's package doc but not yet implemented.
2. `Registry` type — load from `~/.pyry/conversations.json`, mutate in memory, save atomically.

The `Registry` is consumed in later Phase 3 tickets (the conversations API, the auto-archive predicate). This ticket adds no consumers — it's a leaf package addition.

The Tech Note in the issue body is binding: **resist over-DRY on the atomic-write helper**. Duplicate the `Save` body from `internal/devices/registry.go`. Do not extract a shared helper. The two registries will diverge as Phase 3 grows (different sort keys, different envelopes, different uniqueness invariants); shared helpers at this stage hide divergence.

## Design

### Package layout

Two new files in `internal/conversations/`:

```
internal/conversations/
  conversation.go       (existing — unchanged)
  conversation_test.go  (existing — unchanged)
  id.go                 (new — NewID, ValidID)
  id_test.go            (new — format + uniqueness tests)
  registry.go           (new — registryFile, Registry, Load, Save, Create, Get, List, Update)
  registry_test.go      (new — CRUD + atomic-write + concurrency tests)
```

### `id.go` — clone of `internal/sessions/id.go`

```go
package conversations

import (
    "crypto/rand"
    "fmt"
)

// NewID returns a fresh UUIDv4-shaped ConversationID, drawn from crypto/rand.
func NewID() (ConversationID, error) { /* identical body to sessions.NewID */ }

// ValidID reports whether s is a canonical UUIDv4 string. Same predicate as
// sessions.ValidID — version-4 nibble + RFC 4122 variant + lowercase hex.
func ValidID(s string) bool { /* identical body to sessions.ValidID */ }
```

The doc comment on `ConversationID` in `conversation.go` already promises this shape ("Format conventions … #217 owns the generator and the validity predicate"). No changes needed to `conversation.go`.

### `registry.go` — envelope

```go
type registryFile struct {
    Conversations []Conversation `json:"conversations"`
}
```

Bare envelope — no `version` field, matching `devices/registry.go`. Future schema migration is a separate ticket; reserving a top-level object (rather than a top-level array) is the only forward-compat affordance needed today.

### `registry.go` — `Registry` type

```go
type Registry struct {
    mu            sync.Mutex
    conversations []Conversation
}
```

Same shape as `devices.Registry`. Construct via `Load`; pass `&Registry{}` directly in tests (cold start with empty slice is the documented zero value).

### `Load`

```go
func Load(path string) (*Registry, error)
```

Identical semantics to `devices.Load`:

- Missing file → `&Registry{}, nil` (cold start).
- Zero-byte file → `&Registry{}, nil`.
- Malformed JSON → `nil, fmt.Errorf("registry: parse %s: %w", path, err)`.
- I/O error → `nil, fmt.Errorf("registry: read %s: %w", path, err)`.

### `Save`

```go
func (r *Registry) Save(path string) error
```

Identical recipe to `devices.Save`, with two adaptations:

1. **Sort key.** Sort the snapshot by `LastUsedAt` ascending, then by `ID` ascending as a tiebreaker. (`devices` uses `PairedAt` then `Name`; the analogous fields on `Conversation` are `LastUsedAt` and `ID`. `Cwd` is creation-time stable but less semantically meaningful as a sort axis.)
2. **Temp file pattern.** `os.CreateTemp(dir, ".conversations-*.json.tmp")`.

All other steps identical: `MkdirAll(dir, 0o700)`, `Chmod(tmp, 0o600)`, `json.NewEncoder` with two-space indent, `f.Sync()`, `f.Close()`, `os.Rename(tmp, path)`. The `defer os.Remove(tmp)` cleanup pattern is preserved.

### `Create`

```go
func (r *Registry) Create(c Conversation)
```

Mirrors `devices.Add`: lock, append, unlock. **Caller owns uniqueness** — `Create` does not validate that `c.ID` is unique, well-formed, or non-empty. Same convention as `devices.Add`, same rationale: keeping the registry I/O-thin lets the layer above (the conversations API in a later ticket) own validation policy, which may evolve.

The AC's literal signature is `Create(Conversation)` with no return — match it exactly. No error return.

### `Get`

```go
func (r *Registry) Get(id ConversationID) (Conversation, bool)
```

Linear scan under lock; returns the first entry whose `ID` matches. Returns `(Conversation{}, false)` on miss. Byte-exact comparison — `ConversationID` is a string alias, no normalization.

Linear scan is correct at this scale: a Phase 3 user will have O(10²) conversations at the high end. Indexing is premature.

### `List`

```go
type ListFilter struct {
    IsPromoted *bool
}

func (r *Registry) List(filter ...ListFilter) []Conversation
```

Variadic to satisfy "optional" without overloading. Semantics:

- `r.List()` — return all conversations (snapshot copy).
- `r.List(ListFilter{IsPromoted: ptrTo(true)})` — return only promoted (channels).
- `r.List(ListFilter{IsPromoted: ptrTo(false)})` — return only unpromoted (discussions).
- `r.List(ListFilter{IsPromoted: nil})` — equivalent to `r.List()` (nil pointer means "no filter on this field").
- `len(filter) > 1` — use `filter[0]` only; the variadic shape is for ergonomics, not for AND-composition. Document this in the doc comment.

The returned slice is a **copy**; callers may mutate it freely without affecting registry state. This matches `devices.List`.

Helper for tests:

```go
func ptrTo[T any](v T) *T { return &v }
```

Place in the test file, not in production code (only tests need to construct `*bool` literals at call sites).

### `Update`

```go
func (r *Registry) Update(id ConversationID, fn func(*Conversation)) bool
```

Locate the entry with matching `ID`, invoke `fn` with a pointer to the slice element under the registry lock, return `true`. On miss, return `false` and do not invoke `fn`.

Critical contract details for the doc comment:

- `fn` runs with `r.mu` held. `fn` MUST NOT call back into the registry (would deadlock — `sync.Mutex` is non-reentrant). Document this.
- `fn` MUST NOT retain the `*Conversation` pointer past return (slice may be reallocated by future `Create` calls). Document this.
- `fn` may read and mutate any field. The registry does not validate post-mutation state (e.g., does not reject a transition that flips `ID` to a duplicate value). Same "caller owns invariants" stance as `Create`.

Pointer-to-slice-element is the right shape because `Conversation` carries a `*string Name` and a `[]string SessionHistory`; pass-by-value semantics would force `fn` to construct a full replacement struct, defeating the point. `devices` doesn't need an `Update` because device records are append-only after pairing; conversations mutate (rename, promote, rotate sessions, bump LastUsedAt), so this method is genuinely needed.

## Concurrency model

- Registry methods are safe for concurrent use; all guarded by `r.mu`.
- `Save` snapshots under lock then encodes outside the lock. Same pattern as `devices.Save`. This means a `Save` in flight does not block concurrent `Create` / `Update` / `Get` / `List` calls.
- `Update` runs `fn` **with the lock held**. This is intentional: the alternative (snapshot-mutate-swap) would require a CAS loop or risk lost writes, and conversations are not high-contention. Document the no-callback constraint.
- No goroutines are spawned by this package. Lifetime is the caller's.
- No `context.Context` is threaded — registry operations are CPU-bound and fast (in-memory mutation; `Save`'s I/O completes in microseconds for the expected record count). If a future caller needs cancellation around `Save`, the right shape is a wrapper at that layer, not contaminating this package's surface.

## Error handling

- Wrap all errors with `fmt.Errorf("registry: <op>: %w", err)`. Match the prefix style in `devices/registry.go`. Sentinel errors are not introduced — there are no error-class distinctions a caller would branch on.
- `Save` failure leaves the pre-existing target file untouched (rename is atomic; temp file is unlinked via `defer`). The atomic-rename-preserves-old test in `devices/registry_test.go` (`TestRegistry_SaveAtomicRenamePreservesOldFile`) is the binding correctness check; mirror it.
- `Load` of a malformed file returns an error AND a nil `*Registry`. Caller decides whether to halt startup (correct for production) or fall back to empty (incorrect — masks operator error). Do not auto-fall-back inside `Load`.

## Testing strategy

Mirror `internal/devices/registry_test.go` one-for-one, adapted:

| Devices test | Conversations counterpart |
|---|---|
| `TestRegistry_LoadMissingFile` | same |
| `TestRegistry_LoadEmptyFile` | same |
| `TestRegistry_LoadMalformedJSON` | same — assert `"registry: parse"` substring |
| `TestRegistry_AddSaveLoadRoundTrip` | `TestRegistry_CreateSaveLoadRoundTrip` — two conversations, distinct `LastUsedAt`, verify sort by `LastUsedAt` then `ID` |
| `TestRegistry_RemovePresent` / `RemoveAbsent` | **omit** — no `Remove` in AC. (If the API ticket later needs deletion, that ticket adds it with its own tests.) |
| `TestRegistry_FindByTokenHash` | replaced by `TestRegistry_Get` — table-driven hit/miss-empty/miss-non-matching/miss-empty-reg |
| `TestRegistry_SaveFilePermissions` | same — verify `0o700` on parent dir, `0o600` on file |
| `TestRegistry_SaveStableOrdering` | same — Create in two different orders, assert byte-identical Save output |
| `TestRegistry_SaveAtomicRenamePreservesOldFile` | same — chmod parent dir read-only mid-test, assert original file is untouched |
| `TestRegistry_ConcurrentReadWrite` | adapted — concurrent `Create` + `List` + `Get` |

New tests (no devices counterpart):

- `TestRegistry_List_Filter` — table-driven: nil filter, `IsPromoted=true`, `IsPromoted=false`, mixed registry. Verify the returned slice is a copy (mutating it must not affect a subsequent `List`).
- `TestRegistry_Update_Hit` — Update bumps `LastUsedAt`, flips `IsPromoted`, sets `Name`; verify subsequent `Get` reflects the mutation.
- `TestRegistry_Update_Miss` — Update on absent ID returns `false`, `fn` never invoked (use a test-controlled flag), registry untouched.
- `TestRegistry_Update_PointerStability` — within `fn`, mutating `*Conversation` propagates to subsequent reads. (Trivially true given we pass `&r.conversations[i]`, but the test pins the contract.)

For `id_test.go`, mirror `internal/sessions/id_test.go`:

- `TestNewID_Format` — regex `^[0-9a-f]{8}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{12}$`, plus `ValidID` returns true.
- `TestNewID_Unique` — 1000 IDs, no duplicates.
- `TestValidID` — table-driven: empty, wrong length, wrong dashes, wrong version nibble, wrong variant nibble, valid v4, all-uppercase (reject — sessions's predicate is lowercase-only).

All tests `t.Parallel()` except those that mutate process-level state (filesystem permissions tests already follow this convention in `devices`).

## Open questions

1. **Sort key tiebreaker.** Spec uses `LastUsedAt` then `ID`. If reviewers prefer `ID` alone (simpler, time-independent), it's a one-line change. `LastUsedAt` first matches the "recently active" presentation order; `ID` first is determinism-only. Defer to implementer; this is not load-bearing.
2. **`Update` returning the post-mutation `Conversation`.** Current shape returns `bool`. An alternative is `(Conversation, bool)` returning a snapshot of the post-mutation entry. Argument for: callers reading-after-update would otherwise need a `Get` round-trip under a separate lock acquisition, which can race. Argument against: AC literal signature is `Update(id, fn func(*Conversation))` with no return type specified. Match the AC; if a Phase 3 caller needs the post-state, add it then.

Neither blocks implementation. Pick the simpler reading on both and proceed.

## Out of scope

- No `Remove` method — not in AC; conversations are not deleted in Phase 3 (they're archived via `IsPromoted` flips and history retention).
- No schema versioning — `registryFile` envelope shape is a placeholder for future evolution, but no `Version int` field is added today.
- No shared atomic-write helper across `devices` and `conversations` packages — the issue tech note explicitly forbids this.
- No daemon wiring — this ticket lands the package; the supervisor / API layer that calls `Load` at startup and `Save` after mutations is a separate ticket.
