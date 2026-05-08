# #218 — `conversations.Registry.Promote`

## Files to read first

- `internal/conversations/registry.go:108-179` — `Create`, `Get`, `List`, `Update` shapes; the in-memory mutate-under-lock pattern this method joins. `Update`'s linear scan + `r.mu` discipline is the template.
- `internal/conversations/conversation.go:29-72` — `Conversation` field shapes; specifically `Name *string` (nil = "never named", non-nil pointer to `""` = "explicitly empty") and `IsPromoted bool`.
- `internal/conversations/registry_test.go:75-193` — `TestRegistry_CreateSaveLoadRoundTrip` and `TestRegistry_Get` patterns; the existing `ptrTo[T any]` helper at line 14 and the `mustParseTime` helper at line 16 are reused as-is.
- `docs/knowledge/decisions/022-conversations-update-callback-under-lock.md` — locks in the "every mutation goes through `r.mu`" invariant. Name-uniqueness scanning must happen under that same lock, otherwise two concurrent `Promote` calls with the same name both see "free" and both succeed.
- `docs/knowledge/features/conversations-registry.md` — registry surface doc; this ticket appends a `Promote` subsection and four exported sentinels under "CRUD". Naming/style for new exports follows what's there.
- `internal/sessions/pool.go:32-49` and `internal/update/checksum.go:13-25` — repo-wide sentinel idiom: `var ErrFoo = errors.New("pkg: short lower-case sentence")`. Match this exactly.
- `docs/protocol-mobile.md` § "Errors and acks" — wire codes `conversation.not_found` and `conversation.already_promoted`. The primitive does not emit those strings; the wire-protocol layer (later ticket) does the mapping. The primitive's job is to surface a `errors.Is`-distinguishable sentinel per refusal case.

## Context

Phase 3 ships promotion as the user-visible action that turns a throwaway discussion (ephemeral, auto-archive-eligible) into a named channel (long-lived, exempt from auto-archive). This ticket lands the in-memory primitive on `*conversations.Registry`. CLI (`pyry conv promote`) and wire-protocol (`promote_conversation` frame) bindings come in later tickets and call this primitive.

Boundary: the primitive flips `IsPromoted` and sets `Name`. It does not move `Cwd`, does not call `Save`, does not log. The caller owns persistence (consistent with `Create` and `Update`), and the caller owns the cwd-move UX flow (which the mobile payload hints at via an optional `cwd` field, but is layered on top of this primitive in a separate ticket).

## Design

### Surface

One new method and four new exported sentinels on `internal/conversations`:

```go
// registry.go
var (
    ErrConversationNotFound      = errors.New("conversations: conversation not found")
    ErrConversationAlreadyPromoted = errors.New("conversations: conversation already promoted")
    ErrPromotionNameInUse        = errors.New("conversations: promotion name already in use")
    ErrPromotionNameEmpty        = errors.New("conversations: promotion name is empty")
)

// Promote flips the conversation with id to promoted state and sets its
// display name to a non-nil pointer to name. Returns one of the exported
// sentinels on refusal:
//
//   - ErrConversationNotFound       — id is not present in the registry.
//   - ErrConversationAlreadyPromoted — target already has IsPromoted == true.
//   - ErrPromotionNameInUse         — another *promoted* conversation already
//                                      uses name (case-sensitive byte-exact
//                                      comparison; unpromoted conversations
//                                      do not participate in the uniqueness
//                                      check).
//   - ErrPromotionNameEmpty         — name is empty or contains only Unicode
//                                      whitespace.
//
// Validation, uniqueness scan, and mutation all happen under r.mu so a
// concurrent second Promote with the same name cannot slip through. On any
// refusal the registry is left untouched and no field of any record is
// modified. Persistence is the caller's responsibility — Promote does not
// call Save, matching the Create / Update convention.
func (r *Registry) Promote(id ConversationID, name string) error
```

`Promote` is a new method, not a thin wrapper over `Update`. `Update`'s callback shape returns no error — adopting it would force the caller of `Promote` to thread refusal through a side-channel (a captured `*error`), which is uglier than just writing the dedicated method. The mutation logic itself is short enough that the duplication cost is trivial.

### Body sketch

```go
func (r *Registry) Promote(id ConversationID, name string) error {
    if strings.TrimSpace(name) == "" {
        return ErrPromotionNameEmpty
    }
    r.mu.Lock()
    defer r.mu.Unlock()

    idx := -1
    for i := range r.conversations {
        if r.conversations[i].ID == id {
            idx = i
            break
        }
    }
    if idx == -1 {
        return ErrConversationNotFound
    }
    if r.conversations[idx].IsPromoted {
        return ErrConversationAlreadyPromoted
    }
    for i := range r.conversations {
        if i == idx {
            continue
        }
        c := &r.conversations[i]
        if !c.IsPromoted {
            continue
        }
        if c.Name != nil && *c.Name == name {
            return ErrPromotionNameInUse
        }
    }
    n := name
    r.conversations[idx].IsPromoted = true
    r.conversations[idx].Name = &n
    return nil
}
```

Notes that matter for implementation:

- **Empty-name check runs before the lock.** It needs no registry state, and short-circuiting outside the lock keeps the lock-held path uniform.
- **`strings.TrimSpace` for whitespace.** The AC says "empty or whitespace-only"; `TrimSpace` covers ASCII space/tab/newline plus Unicode whitespace, matching Go conventions. The stored `Name` is the **untrimmed** input — this is a refusal predicate, not a normalizer. If the user passes `"  general  "`, the primitive rejects no, accepts the literal string with surrounding spaces; trimming as a side effect would be a separate decision documented in a later UX ticket.
- **Scan visits every record, including the target's own index.** The `i == idx` guard skips the target so we don't compare its (currently nil) name to itself. After the scan, mutating the target's name is safe because the target was the only entry whose `IsPromoted` we are flipping.
- **Name-uniqueness scope is "another *promoted* conversation".** A historical unpromoted record with a stray non-nil `Name` (e.g. a future `pyry conv name` flow that names a discussion before promoting) does not block. This matches the AC literally and is the test case that pins the behaviour.
- **Comparison is byte-exact `==` on `string`.** Per the AC: case-sensitive, no Unicode normalization, no fold. `Name` is a `*string` pointer; the comparison dereferences only after a nil-guard.
- **Pointer ownership.** The line `n := name; ... .Name = &n` allocates a fresh string-header on the heap so the stored pointer does not alias a stack variable across the function return and is independent of any future caller mutation of their local `name` value (strings are immutable, so the header copy is the only concern; this pattern is used as a defensive idiom matching `ptrTo` in the test file).
- **No partial mutation on refusal.** Every refusal returns before touching `r.conversations[idx]`. The mutation is two field assignments at the bottom of the happy path; nothing earlier writes.

### Concurrency model

`Promote` is the second mutation entry point on the registry (alongside `Update`). The lock discipline is identical to `Update`:

- Single `r.mu.Lock()` covers validation, scan, and mutation.
- Lock-held window is `O(N)` over the in-memory slice for the uniqueness scan. At Phase 3 scale (O(10²) conversations), this is ~µs and not a contention concern.
- `fn` callback hazards from ADR 022 do not apply — there is no caller-supplied callback. The whole body is in-package and provably never re-enters the registry.
- `Save` continues to snapshot under the same lock and write outside it (registry.go:62-65). A `Promote` that lands during the snapshot copy either runs entirely before the copy (full mutation visible on disk) or entirely after (mutation observable only after a follow-up `Save`). There is no torn write.

A reader (`Get`, `List`) running concurrently with `Promote` either sees the pre-state or the post-state — never a half-mutated record — because the two field writes happen with `r.mu` held and readers acquire the same lock.

### Error model

Four sentinels, exported, declared together at the top of `registry.go` after the `registryFile` block. Caller maps to wire codes via `errors.Is`:

| Refusal | Sentinel | Wire code |
|---|---|---|
| id absent | `ErrConversationNotFound` | `conversation.not_found` |
| already `IsPromoted` | `ErrConversationAlreadyPromoted` | `conversation.already_promoted` |
| name collides with another promoted record | `ErrPromotionNameInUse` | (later ticket — likely `conversation.name_in_use`) |
| name empty/whitespace | `ErrPromotionNameEmpty` | (later ticket — likely `conversation.name_empty` or 400 invalid argument) |

Sentinels are returned naked (`return ErrPromotionNameEmpty`), not wrapped (`fmt.Errorf("...: %w", err)`). The primitive has no extra context to add — id and name are caller-supplied and the caller already has them. This matches `internal/sessions/pool.go`'s `ErrSessionNotFound` style: bare return, caller wraps if it wants to add context for logging.

`ErrConversationNotFound` lives in this package even though `internal/sessions` has `ErrSessionNotFound` — the two registries are deliberately decoupled (per the issue tech note and ADR 022). Sharing a `ErrNotFound` would couple them and violate the "registries diverge" stance.

## Testing strategy

Append to `internal/conversations/registry_test.go`. All tests are table-driven where shape allows, `t.Parallel()`, stdlib only, same-package. Reuse the file's existing `ptrTo[T any]` helper for `*string` construction and `mustParseTime` for timestamps.

One required new test function:

### `TestRegistry_Promote`

Single table, one sub-test per row, `t.Parallel()` per sub-test. Each row builds a fresh `*Registry` via a `setup` closure, calls `Promote`, and asserts:

1. The returned `error` matches the expected sentinel via `errors.Is` (or is `nil` on the success path).
2. `Get(id)` after the call returns the expected post-state — `IsPromoted` flipped or unchanged, `Name` set or unchanged.
3. On refusal rows, every record in the registry equals its pre-call value (loop with a recorded snapshot or check the specific known fields). This is the "left untouched" invariant.

Required rows (mapping 1:1 to AC bullets):

| Name | Setup | Input | Expected error | Post-state of target |
|---|---|---|---|---|
| `success` | one unpromoted, no name | id=that, name=`"general"` | nil | `IsPromoted=true`, `*Name=="general"` |
| `unknown-id` | one unpromoted | id=different, name=`"general"` | `ErrConversationNotFound` | unchanged |
| `already-promoted` | one promoted (`Name=ptrTo("old")`) | id=that, name=`"new"` | `ErrConversationAlreadyPromoted` | unchanged (still `*Name=="old"`) |
| `name-conflict-with-promoted` | one promoted `Name=ptrTo("dup")`, one unpromoted | id=unpromoted, name=`"dup"` | `ErrPromotionNameInUse` | unchanged |
| `name-conflict-with-unpromoted-OK` | one unpromoted with `Name=ptrTo("dup")`, one unpromoted with no name | id=second, name=`"dup"` | nil | `IsPromoted=true`, `*Name=="dup"` |
| `empty-name` | one unpromoted | id=that, name=`""` | `ErrPromotionNameEmpty` | unchanged |
| `whitespace-name` | one unpromoted | id=that, name=`"   \t\n"` | `ErrPromotionNameEmpty` | unchanged |

The `name-conflict-with-unpromoted-OK` row is the key correctness pin: it proves the uniqueness scope is "another *promoted* conversation", not "anyone with a non-nil name". Without this row, an over-broad scan that ignored `IsPromoted` would still pass the rest of the table.

### `TestRegistry_Promote_DoesNotPersist`

Standalone test (not table-driven). Build a registry, `Save` to a temp path, `Promote` successfully, then `Load` from the same path and assert the loaded registry shows `IsPromoted=false` for the target. Pins "no implicit `Save`" the same way the AC pins "persistence stays with the caller". One test, ~15 lines.

### `TestRegistry_Promote_LockHeldDuringScan` — defer

Verifying "the uniqueness scan and the mutation share the lock" via a race-detector probe (two goroutines hammering `Promote` with the same name) is achievable but the standard race detector primarily catches data-race-on-bytes, not "two writers both observed free." A correctness probe would need a goroutine that calls `Promote` mid-scan, which the primitive's structure makes impossible by construction — there is no callback hook.

ADR 022 establishes the discipline; this test would mostly assert "we trust ADR 022." Skip. If a future contributor refactors `Promote` to release the lock between scan and mutation, code review catches it.

## Knowledge-doc updates

Append to `docs/knowledge/features/conversations-registry.md`:

1. Add `Promote` to the surface listing under "Surface".
2. Add a `### Promote(id, name)` subsection under "CRUD", documenting the four refusals, the in-package error sentinels, the under-lock uniqueness scope (promoted-only), and the "no Save called" caller convention.
3. Add a sentence to "Out of scope (deferred)" replacing the existing "**Promotion API.** #218." line: now points to this spec and notes the cwd-move and CLI/wire bindings as still-deferred consumers.

No new ADR. The decision (callback-under-lock for mutations) is already covered by ADR 022; `Promote` is an instance of that pattern, not a new one. Adding an ADR per primitive would dilute the registry's decision record.

`docs/knowledge/features/conversations-package.md` is **not** updated — it documents the type, not the registry surface.

After doc edits, run `qmd update && qmd embed`.

## Open questions

1. **Should `Promote` accept a `Conversation`-shaped `apply` callback (e.g. setting `LastUsedAt = time.Now()` at the same time)?** Deferred. The AC scopes the primitive to `IsPromoted` + `Name`. Bumping `LastUsedAt` on promote is a UX choice the consuming layer (CLI / wire-protocol) makes by calling `Update` after `Promote`. Two registry calls, one `Save` — no atomicity loss for this specific pair because both run under the same lock if needed (consumer can wrap in its own coordinator). Revisit if the consumer turns out to need a single atomic call.

2. **`ErrPromotionNameEmpty` vs. a generic `ErrInvalidPromotionName`.** Picked the specific name because it's the only validation the primitive performs and a generic name leaves room for ambiguity ("invalid how?"). If a future ticket adds length caps or character restrictions, the new validation gets its own sentinel; the existing one stays scoped to empty/whitespace.

3. **`Name` storage when a future caller wants `*Name == ""` for a *promoted* conversation.** Disallowed transitively by `ErrPromotionNameEmpty` — `Promote` cannot create a promoted conversation with `*Name == ""`. A future `pyry conv name <id> ""` UX that intentionally clears a channel name is out of scope and would need its own primitive (and would also need to relax this rule or validate elsewhere). Not this ticket's concern.
