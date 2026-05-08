# `conversations.json` Registry

On-disk persistence for `internal/conversations.Registry`. Stores the binary's per-conversation state — id, name, cwd, current/historical session ids, promotion flag, last-used timestamp — at `~/.pyry/<name>/conversations.json`. Phase 3 storage primitive consumed by future promotion API (#218), auto-archive predicate (#219), and auto-archive sweep (#220).

Lives in the same `internal/conversations` package as the `Conversation` type (#216) — no subpackage. Stdlib only (`encoding/json`, `errors`, `fmt`, `io/fs`, `os`, `path/filepath`, `sort`, `sync`).

## Status

- **Phase 3 foundation (#217):** mutex-guarded `Registry` + atomic save + load. ID generator + validator (`NewID`, `ValidID`) co-located in the same package. Six exports on the registry: `Load`, `(*Registry).Save / Create / Get / List / Update`. One `ListFilter` struct.
- **Promotion primitive (#218):** `(*Registry).Promote(id, name)` flips a discussion to a named channel under the registry lock; four exported sentinel errors (`ErrConversationNotFound`, `ErrConversationAlreadyPromoted`, `ErrPromotionNameInUse`, `ErrPromotionNameEmpty`) cover the refusal cases.

## Surface

```go
// id.go
func NewID() (ConversationID, error)
func ValidID(s string) bool

// registry.go
type Registry struct { /* unexported */ }

type ListFilter struct {
    IsPromoted *bool
}

var (
    ErrConversationNotFound        = errors.New("conversations: conversation not found")
    ErrConversationAlreadyPromoted = errors.New("conversations: conversation already promoted")
    ErrPromotionNameInUse          = errors.New("conversations: promotion name already in use")
    ErrPromotionNameEmpty          = errors.New("conversations: promotion name is empty")
)

func Load(path string) (*Registry, error)
func (r *Registry) Save(path string) error
func (r *Registry) Create(c Conversation)
func (r *Registry) Get(id ConversationID) (Conversation, bool)
func (r *Registry) List(filter ...ListFilter) []Conversation
func (r *Registry) Update(id ConversationID, fn func(*Conversation)) bool
func (r *Registry) Promote(id ConversationID, name string) error
```

`Registry` holds the in-memory conversation slice plus a guarding mutex. Construct via `Load` (cold-start mints empty; warm-start reads from disk) or directly via `&Registry{}` (zero value is the empty registry — documented). Methods are safe for concurrent use.

## Path

```
~/.pyry/<sanitized-name>/conversations.json
```

The registry API is path-agnostic — `Load(path)` and `Save(path)` take any absolute path. Resolving `~/.pyry/<name>/conversations.json` is the consumer's job (mirrors `internal/sessions`'s `loadRegistry(path)` / `saveRegistryLocked(path, reg)` and `internal/devices`'s discipline). Permissions: directory `0o700`, file `0o600`.

## ID generator and validator

`NewID` returns a fresh UUIDv4-shaped `ConversationID` from `crypto/rand` (16 bytes, version-4 nibble, RFC 4122 variant, lowercase hex, dashes at canonical positions). Returns an error only when the system rng fails. Body is byte-for-byte the `internal/sessions/id.go:NewID` recipe with the typed-id alias swapped — duplicated rather than extracted; no shared helper.

`ValidID(s)` reports whether `s` is the canonical shape `NewID` produces: 36 chars, lowercase hex, dashes at positions 8/13/18/23, version-4 nibble (`'4'`) at position 14, RFC 4122 variant nibble (`'8'/'9'/'a'/'b'`) at position 19. Empty input returns false. **Lowercase only** — uppercase rejected (matches `sessions.ValidID`); the on-disk record is always the lowercase form `NewID` produces.

The `ConversationID` doc-comment in `conversation.go` (#216) deferred the generator and validity predicate to this ticket; that promise is now satisfied.

## Schema

```json
{
  "conversations": [
    {
      "id": "0a2c1f5d-...-...",
      "name": "tax-filing",
      "cwd": "/Users/juhana/projects/taxes",
      "current_session_id": "8e3...",
      "session_history": ["2c4...", "9a1..."],
      "is_promoted": true,
      "last_used_at": "2026-05-09T12:35:01.012Z"
    }
  ]
}
```

Envelope shape (`{"conversations": [...]}`), not a bare top-level array. Reserves room for future top-level fields (schema version, archive cursor) without breaking jq pipelines or stdlib decoder discipline. Same future-proofing rationale as the sessions and devices registries.

No `version` field today (out of scope per AC; defer until first migration). `Conversation` JSON tags + `omitempty` placement are pinned by [`features/conversations-package.md`](conversations-package.md) — `name` / `current_session_id` / `session_history` carry `omitempty`; `id` / `cwd` / `is_promoted` / `last_used_at` always appear, even at zero value.

## Atomic write

`Save` mirrors `internal/devices/registry.go:Save`:

```
os.MkdirAll(dir, 0o700)
os.CreateTemp(dir, ".conversations-*.json.tmp")
defer os.Remove(tmp)
os.Chmod(tmp, 0o600)
json.NewEncoder(f).Encode(...)   // SetIndent("", "  ")
f.Sync()
f.Close()
os.Rename(tmp, path)             // commit point
```

`os.Rename` on the same filesystem is atomic on Linux ext4 / macOS APFS. SIGKILL between `CreateTemp` and `Rename` leaves the pre-existing target untouched and an orphan `.conversations-*.json.tmp` (cleaned up best-effort by `defer os.Remove(tmp)`). SIGKILL after `Rename` leaves the new file in place. Partial JSON in the target file is unreachable.

The `0o600` chmod is applied unconditionally before the encode even though `os.CreateTemp`'s default already creates with mode `0o600` — same belt-and-suspenders pattern as the sessions and devices recipes, defends against a future umask-permissive env or stdlib behaviour change.

No parent-directory fsync (per `lessons.md` § "Atomic on-disk writes" — operator-recoverable JSON, ext4/APFS rename-entry update is durable enough).

**The atomic-write recipe is duplicated, not shared.** Per the issue tech note, no shared helper across `internal/sessions`, `internal/devices`, and `internal/conversations`. The three registries will diverge as Phase 3 grows (different sort keys, different envelopes, different uniqueness invariants); a shared helper at this stage would hide divergence.

## Save concurrency: lock, snapshot, release, write

Like `internal/devices`, `Save` snapshots under the lock and writes outside it:

```go
r.mu.Lock()
snapshot := make([]Conversation, len(r.conversations))
copy(snapshot, r.conversations)
r.mu.Unlock()
// sort + atomic write happen WITHOUT the lock held
```

Concurrent `Create` / `Get` / `List` / `Update` calls are not blocked behind the I/O syscall window. Two concurrent `Save` calls produce two complete temp files and two renames; the later rename wins (`os.Rename` is atomic per call, no torn write). Callers that need "Save once, everyone observes the new state" call `Save` from a single goroutine.

`Conversation`'s slice field (`SessionHistory []string`) is **not** deep-copied at the snapshot boundary. The shallow copy is safe today because nothing in the caller mutates `SessionHistory` after handing the value to `Create` or after `Update`'s callback returns. If a future mutation pattern starts mutating in place outside the registry lock, the snapshot will need a per-element `append([]string(nil), c.SessionHistory...)` deep copy.

## Sort discipline

Snapshot is sorted by `LastUsedAt` ascending, tiebroken by `ID` byte-exact, before encode:

```go
sort.SliceStable(snapshot, func(i, j int) bool {
    if !snapshot[i].LastUsedAt.Equal(snapshot[j].LastUsedAt) {
        return snapshot[i].LastUsedAt.Before(snapshot[j].LastUsedAt)
    }
    return snapshot[i].ID < snapshot[j].ID
})
```

Diverges from devices's `PairedAt`/`Name` ordering: `Conversation` is mutable (rename, promote, rotate sessions, bump LastUsedAt), so `LastUsedAt` is the natural "recently active" axis; `ID` is the determinism-only tiebreaker (`Cwd` is creation-time stable but less semantically meaningful). Two registries with the same logical content but different `Create` order produce byte-identical files.

`time.Time.Equal` (not `==`) for the primary comparator — JSON roundtrip strips monotonic-clock state and `==` would treat otherwise-equal timestamps as unequal (see `lessons.md` § "JSON roundtrip strips monotonic-clock state").

Sort runs on the Save-side snapshot, not on the live in-memory slice — `Create` insertion order in memory is preserved while disk output stays deterministic.

## Load semantics

| Disk state | `Load` returns |
|---|---|
| File missing (`fs.ErrNotExist`) | `(empty *Registry, nil)` — cold start. |
| File present, zero bytes | `(empty *Registry, nil)` — same as missing. |
| File present, valid JSON | `(*Registry{conversations: rf.Conversations}, nil)`. |
| File present, malformed JSON | `(nil, fmt.Errorf("registry: parse %s: %w", path, err))`. |
| File present, other I/O error | `(nil, fmt.Errorf("registry: read %s: %w", path, err))`. |

Empty-file → empty-registry asymmetry vs. `internal/config.Load` (which surfaces empty as a parse error) is deliberate: `conversations.json` is pyry-owned and zero bytes is a benign cold-start state; `config.json` is operator-owned and zero bytes is operator error.

`Load` of a malformed file returns an error AND a nil `*Registry`. The caller decides whether to halt startup (correct for production) or fall back to empty (incorrect — masks operator error). `Load` does not auto-fall-back.

The returned `*Registry` is independent of the on-disk file — subsequent `Save` calls re-encode from the in-memory slice; the file may be moved or deleted between `Load` and `Save` without affecting in-memory state.

## CRUD

### `Create(c Conversation)`

Lock, append, unlock. **Caller owns uniqueness** — `Create` does not validate that `c.ID` is unique, well-formed, or non-empty. Same convention as `devices.Add`: keeping the registry I/O-thin lets the consuming layer (the conversations API in #218) own validation policy, which may evolve. AC pins the literal signature with no return value; match it exactly.

### `Get(id ConversationID) (Conversation, bool)`

Linear scan under lock; returns the first entry whose `ID` matches. Returns `(Conversation{}, false)` on miss. Byte-exact `==` comparison — `ConversationID` is a string newtype, no normalization. Linear scan is correct at this scale: a Phase 3 user will have O(10²) conversations at the high end. Indexing is premature.

### `List(filter ...ListFilter) []Conversation`

Returns a copy of the in-memory list, optionally narrowed by filter:

- `r.List()` — return all conversations (snapshot copy).
- `r.List(ListFilter{IsPromoted: ptrTo(true)})` — only promoted (channels).
- `r.List(ListFilter{IsPromoted: ptrTo(false)})` — only unpromoted (discussions).
- `r.List(ListFilter{IsPromoted: nil})` — equivalent to `r.List()` (nil pointer means "no filter on this field").

Variadic for ergonomics, **not** AND-composition: when more than one `ListFilter` is supplied, only `filter[0]` is consulted (documented in the doc comment). The returned slice is a copy; callers may mutate it freely without affecting registry state. `IsPromoted *bool` distinguishes "filter out unpromoted" (`true`) from "filter out promoted" (`false`) from "no filter" (`nil`) — three states, which a bare `bool` cannot express.

### `Update(id ConversationID, fn func(*Conversation)) bool`

Locate the entry with matching `ID`, invoke `fn` with a pointer to the slice element under the registry lock, return `true`. On miss, return `false` and do not invoke `fn`.

Critical contract for callers:

- **`fn` runs with `r.mu` held.** `fn` MUST NOT call back into the registry (any `Registry` method would deadlock — `sync.Mutex` is non-reentrant).
- **`fn` MUST NOT retain the `*Conversation` pointer past return.** A future `Create` may reallocate the slice; the pointer becomes a dangling reference into the old backing array.
- **`fn` may read and mutate any field.** The registry does not validate post-mutation state — does not reject a flip that duplicates another entry's ID, does not reject `LastUsedAt` going backwards. Same "caller owns invariants" stance as `Create`.

Pointer-to-slice-element is the right shape because `Conversation` carries a `*string Name` and a `[]string SessionHistory`; pass-by-value would force `fn` to construct a full replacement struct, defeating the point. `devices` doesn't need an `Update` because device records are append-only after pairing; conversations mutate (rename, promote, rotate sessions, bump LastUsedAt), so this method is genuinely needed. See [ADR 022](../decisions/022-conversations-update-callback-under-lock.md) for the snapshot-mutate-swap alternative considered and rejected.

`Update` returns `bool`, not `(Conversation, bool)`. AC pins the no-return-value-for-the-post-state signature; if a future caller needs a post-mutation snapshot, add it then. Calling `Get(id)` after `Update` returns `true` works but races a concurrent `Update` on the same id — use the callback to read the post-state in place if that matters.

### `Promote(id ConversationID, name string) error`

In-memory primitive that turns a discussion into a named channel: flips `IsPromoted` to `true` and sets `Name` to a non-nil pointer to `name`. Validation, the uniqueness scan, and the two-field mutation all run under `r.mu`; on any refusal the registry is left untouched. Persistence is the caller's job — `Promote` does not call `Save`, matching the `Create` / `Update` convention.

| Refusal | Sentinel | Wire code (mapped by later ticket) |
|---|---|---|
| id absent | `ErrConversationNotFound` | `conversation.not_found` |
| target already `IsPromoted == true` | `ErrConversationAlreadyPromoted` | `conversation.already_promoted` |
| `name` collides with another *promoted* conversation | `ErrPromotionNameInUse` | TBD (likely `conversation.name_in_use`) |
| `name` empty or whitespace-only | `ErrPromotionNameEmpty` | TBD (likely `conversation.name_empty` / 400) |

Sentinels are exported and returned naked (`return ErrPromotionNameEmpty`); the primitive has no extra context to add — id and name are caller-supplied. Distinguish via `errors.Is`. The `not_found` sentinel lives in this package even though `internal/sessions` has `ErrSessionNotFound`: the two registries are deliberately decoupled (per ADR 022 and the registry tech notes), so a shared `ErrNotFound` would couple them.

Behavioural fine print:

- **Empty/whitespace check uses `strings.TrimSpace`** — covers ASCII space/tab/newline plus Unicode whitespace. The stored `Name` is the **untrimmed** input; trimming is a refusal predicate, not a normalizer. `Promote(id, "  general  ")` accepts the literal string with surrounding spaces.
- **Uniqueness scope is "another *promoted* conversation"**, byte-exact `==`, case-sensitive, no Unicode normalization. A historical unpromoted record with a stray non-nil `Name` (e.g. a future `pyry conv name` that names a discussion before promoting) does not block. The `name-conflict-with-unpromoted-OK` test row pins this.
- **No partial mutation on refusal.** Every refusal returns before touching `r.conversations[idx]`. The mutation is two field assignments at the bottom of the happy path; nothing earlier writes.
- **Pointer ownership.** The implementation copies `name` into a fresh local before taking its address, so the stored `*Name` never aliases a caller-mutable variable. Strings are immutable so this is defensive idiom rather than necessity.
- **No `LastUsedAt` bump.** `Promote` only flips `IsPromoted` and sets `Name`. If a consuming layer wants to bump `LastUsedAt` on promote, it calls `Update` after `Promote`. Two registry calls; no atomicity loss for this specific pair.

`Promote` is a new method, not a thin wrapper over `Update`: `Update`'s callback returns no error, so building `Promote` on top of it would force the caller to thread refusal through a captured `*error`, which is uglier than just writing the dedicated method. The duplication is two field assignments under the same lock — trivial.

## Tests

`internal/conversations/registry_test.go`, same-package, table-driven, `t.Parallel()` everywhere except permission-mutating tests, stdlib only.

Mirroring `devices`:

- `TestRegistry_LoadMissingFile` / `TestRegistry_LoadEmptyFile` / `TestRegistry_LoadMalformedJSON` (asserts wrapped `registry: parse` prefix).
- `TestRegistry_CreateSaveLoadRoundTrip` — two conversations with distinct `LastUsedAt`, asserts sort by `LastUsedAt` then `ID` and round-trip equality (`time.Time.Equal`, never `==`).
- `TestRegistry_Get` — table-driven hit / miss-empty / miss-non-matching / miss-empty-registry.
- `TestRegistry_SaveFilePermissions` — parent dir mode `0o700`, file mode `0o600`. Skipped on Windows.
- `TestRegistry_SaveStableOrdering` — sort-before-encode produces byte-identical output across `Create` permutations.
- `TestRegistry_SaveAtomicRenamePreservesOldFile` — chmod-the-dir-readonly proves the pre-existing file survives a failed save unchanged. Skipped on Windows.
- `TestRegistry_ConcurrentReadWrite` — race-detector probe across mixed `Create` / `List` / `Get`.

New (no devices counterpart):

- `TestRegistry_List_Filter` — table: nil filter, `IsPromoted=true`, `IsPromoted=false`; verifies the returned slice is a copy (mutating it does not affect a subsequent `List`); verifies the multi-filter case uses `filter[0]` only.
- `TestRegistry_Update_Hit` — Update bumps `LastUsedAt`, flips `IsPromoted`, sets `Name`; subsequent `Get` reflects the mutation.
- `TestRegistry_Update_Miss` — Update on absent id returns `false`, `fn` never invoked (test-controlled flag), registry untouched.
- `TestRegistry_Update_PointerStability` — within `fn`, mutating `*Conversation` propagates to subsequent reads (pins the contract; trivially true given `&r.conversations[i]`).
- `TestRegistry_Promote` (#218) — table-driven, one row per AC bullet: success, unknown id, already promoted, name conflict against another *promoted* record (refused), name conflict against an *unpromoted* record (accepted — pins uniqueness scope), empty name, whitespace-only name. Each refusal row also asserts the target is byte-equal to its pre-call state via `Get`.
- `TestRegistry_Promote_DoesNotPersist` (#218) — `Save` → `Promote` → `Load` from the same path; the loaded registry shows `IsPromoted == false`, pinning that `Promote` does not call `Save` implicitly.

`internal/conversations/id_test.go` mirrors `internal/sessions/id_test.go`:

- `TestNewID_Format` — regex `^[0-9a-f]{8}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{12}$`, plus `ValidID` returns true.
- `TestNewID_Unique` — 1000 IDs, no duplicates.
- `TestValidID` — table: empty, wrong length, wrong dashes, wrong version nibble, wrong variant nibble, valid v4, all-uppercase (rejected — predicate is lowercase-only).

## Out of scope (deferred)

- **`Remove` method.** Not in AC; conversations are not deleted in Phase 3 — they're archived via `IsPromoted` flips and history retention. If a future API ticket needs deletion, that ticket adds it with its own tests.
- **Schema versioning.** Per AC: defer until first migration. The envelope shape reserves the field; add it then, not now.
- **Daemon wiring.** This ticket lands the package; the supervisor / API layer that calls `Load` at startup and `Save` after mutations is a separate ticket.
- **CLI / wire-protocol bindings of `Promote`.** The in-memory primitive landed in #218; `pyry conv promote` and the mobile `promote_conversation` frame (sentinel-to-wire-code mapping for `conversation.not_found` / `conversation.already_promoted` and the `name_in_use` / `name_empty` codes) are the still-deferred consumers.
- **Cwd-move on promote.** Mobile's `promote_conversation` payload carries an optional `cwd`; physically moving the conversation's working directory to a workspace folder is a separate UX flow layered on top of the primitive, not in #218.
- **`pyry conv name <id> <name>` for renaming or clearing channel names.** Out of scope; `Promote` cannot create a promoted conversation with `*Name == ""` (rejected by `ErrPromotionNameEmpty`), and a future rename/clear primitive would have its own validation.
- **Auto-archive predicate + sweep.** #219, #220.
- **Migration from existing `Session` registry.** TBD ticket once Conversations is proven on disk; Phase 1/2 sessions stay untouched.
- **Shared atomic-write helper across `devices` and `conversations`.** Issue tech note explicitly forbids; revisit only if real divergence cost surfaces.

## Related

- [`features/conversations-package.md`](conversations-package.md) — `Conversation` + `ConversationID` (#216), the on-disk record shape this registry persists.
- [`features/devices-registry.md`](devices-registry.md) — the structural reference implementation (atomic write, envelope shape, snapshot-then-write Save).
- [`features/sessions-registry.md`](sessions-registry.md) — the older atomic-rename recipe both registries trace to.
- [ADR 020](../decisions/020-devices-registry-snapshot-then-write.md) — Save snapshots under lock, performs I/O outside (the pattern this registry inherits).
- [ADR 022](../decisions/022-conversations-update-callback-under-lock.md) — `Update` runs the caller's callback under the registry lock (over snapshot-mutate-swap).
- `internal/sessions/id.go` — the `NewID` / `ValidID` template `internal/conversations/id.go` clones.
- `docs/specs/architecture/217-conversations-registry-crud.md` — architect's spec for the CRUD foundation.
- `docs/specs/architecture/218-conversations-promotion-api.md` — architect's spec for `Promote`.
