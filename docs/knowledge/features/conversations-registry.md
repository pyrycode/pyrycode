# `conversations.json` Registry

On-disk persistence for `internal/conversations.Registry`. Stores the binary's per-conversation state — id, name, cwd, current/historical session ids, promotion flag, last-used timestamp — at `~/.pyry/<name>/conversations.json`. Phase 3 storage primitive consumed by future promotion API (#218), auto-archive predicate (#219), and auto-archive sweep (#220).

Lives in the same `internal/conversations` package as the `Conversation` type (#216) — no subpackage. Stdlib only (`encoding/json`, `errors`, `fmt`, `io/fs`, `os`, `path/filepath`, `sort`, `sync`).

## Status

- **Phase 3 foundation (#217):** mutex-guarded `Registry` + atomic save + load. ID generator + validator (`NewID`, `ValidID`) co-located in the same package. Six exports on the registry: `Load`, `(*Registry).Save / Create / Get / List / Update`. One `ListFilter` struct.
- **Promotion primitive (#218):** `(*Registry).Promote(id, name)` flips a discussion to a named channel under the registry lock; four exported sentinel errors (`ErrConversationNotFound`, `ErrConversationAlreadyPromoted`, `ErrPromotionNameInUse`, `ErrPromotionNameEmpty`) cover the refusal cases.
- **Deletion primitive (#237):** `(*Registry).Delete(id) bool` removes a single entry by ID under the registry lock; consumed by the auto-archive sweep ([`features/conversations-auto-archive.md`](conversations-auto-archive.md)). #217 explicitly deferred deletion until a real consumer surfaced; #220's sweep is that consumer.
- **Rotation-rebind primitive (#739):** `(*Registry).RebindSession(oldID, newID string) bool` re-points the conversation bound to `oldID` at `newID` and appends `oldID` to `SessionHistory`, under the registry lock; consumed by the pool's `/clear` rotation path so the conversation↔session binding stays current beyond the first rotation ([`features/conversation-session-binding.md`](conversation-session-binding.md) § *Maintaining the binding across rotation*). The first production caller to **write** `SessionHistory`.
- **Durable manual-archive primitive (#880):** `(*Registry).SetArchived(id, archived bool) bool` flips the durable `Conversation.IsArchived` flag under the registry lock; `ListFilter.IsArchived *bool` narrows `List` to active-only/archived-only/both, ANDing with `IsPromoted` when both are set on one filter. Distinct from the auto-archive `Sweep` ([`features/conversations-auto-archive.md`](conversations-auto-archive.md)), which permanently deletes rather than flagging — but no longer independent of it: since #1488 `ShouldArchive` reads `IsArchived` and the sweep skips archived rows, so a manual archive is durable until the user unarchives. Called by the `archive_conversation`/`unarchive_conversation` wire verbs (#881), the sole production caller. See [codebase/880.md](../codebase/880.md), [codebase/881.md](../codebase/881.md).
- **Bounded system-prompt primitive (#2149):** `(*Registry).SetSystemPrompt(id ConversationID, prompt *string) error` validates and sets `Conversation.SystemPrompt` under the registry lock; `MaxSystemPromptBytes = 8192` (inclusive) and two sentinels (`ErrSystemPromptTooLong`, `ErrSystemPromptInvalidUTF8`) join `ErrConversationNotFound` as the refusal set. #2150 reads the stored value at spawn (`Pool.refreshSystemPrompt`, called from `Pool.Activate`) and #2151 wires `SetSystemPrompt` to the `set_system_prompt` wire verb (`handlers.SetSystemPrompt`, [`relay-package.md`](relay-package.md)); #2152 (reading it back over the wire) is the one slice still open.

## Surface

```go
// id.go
func NewID() (ConversationID, error)
func ValidID(s string) bool

// registry.go
type Registry struct { /* unexported */ }

type ListFilter struct {
    IsPromoted *bool
    IsArchived *bool
}

var (
    ErrConversationNotFound        = errors.New("conversations: conversation not found")
    ErrConversationAlreadyPromoted = errors.New("conversations: conversation already promoted")
    ErrPromotionNameInUse          = errors.New("conversations: promotion name already in use")
    ErrPromotionNameEmpty          = errors.New("conversations: promotion name is empty")
    ErrSystemPromptTooLong         = errors.New("conversations: system prompt exceeds the maximum byte length")
    ErrSystemPromptInvalidUTF8     = errors.New("conversations: system prompt is not valid UTF-8")
)

const MaxSystemPromptBytes = 8192 // inclusive

func Load(path string) (*Registry, error)
func (r *Registry) Save(path string) error
func (r *Registry) Create(c Conversation)
func (r *Registry) Get(id ConversationID) (Conversation, bool)
func (r *Registry) List(filter ...ListFilter) []Conversation
func (r *Registry) Update(id ConversationID, fn func(*Conversation)) bool
func (r *Registry) Delete(id ConversationID) bool
func (r *Registry) Promote(id ConversationID, name string) error
func (r *Registry) RebindSession(oldID, newID string) bool
func (r *Registry) SetArchived(id ConversationID, archived bool) bool
func (r *Registry) SetSystemPrompt(id ConversationID, prompt *string) error
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

`is_archived` (#880) is omitted from this example because it's `omitempty` and this row is active
— it only appears, as `"is_archived": true`, on a row the user has manually archived. See
*Durable archive flag (`IsArchived`, #880)* below.

`system_prompt` (#2149) is likewise omitted here — this row holds no prompt. It appears as
`"system_prompt": ""` on an explicitly-emptied row and `"system_prompt": "<text>"` on an
operator-set one; see § `SetSystemPrompt` above.

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

## Save concurrency: dedicated save mutex serializes the whole sequence (#868)

`Save` holds a second, dedicated `saveMu sync.Mutex` across the *entire* snapshot→sort→encode→fsync→rename sequence, kept deliberately separate from `mu` (which still guards `conversations` for reads/mutations):

```go
r.saveMu.Lock()
defer r.saveMu.Unlock()

r.mu.Lock()
snapshot := make([]Conversation, len(r.conversations))
copy(snapshot, r.conversations)
r.mu.Unlock()
// sort + atomic write happen under saveMu but WITHOUT mu held
```

**Lock order is one-directional: `saveMu → mu`, never the reverse.** `Save` takes `saveMu`, then briefly takes `mu` for the snapshot copy and releases it before any I/O; no mutator or reader ever touches `saveMu`. Concurrent `Create` / `Get` / `List` / `Update` calls are not blocked behind the I/O syscall window — only against each other and against the short snapshot-copy critical section, exactly as before.

Taking the snapshot **inside** `saveMu` is what gives the durability guarantee: for any two `Save` calls, snapshot order == `saveMu`-acquire order == rename order, so the last `Save` to acquire `saveMu` always renders on disk. Before #868, `Save` only held `mu` for the snapshot copy and then wrote unlocked — two overlapping saves could interleave so an **older** snapshot's rename landed last, silently clobbering a newer one (self-healing on the next `Save`, but durably lost if the daemon restarted first). Three production goroutines call `Save` concurrently: the v2 manager's `create_conversation`/`rename_conversation` handlers, the auto-archive sweep loop, and the `/clear` rebind observer — making the interleaving a real, not theoretical, hazard. See [codebase/868.md](../codebase/868.md).

This diverges from `internal/devices`, whose `Save` still only snapshots-under-lock-then-writes-unlocked ([ADR 020](../decisions/020-devices-registry-snapshot-then-write.md)) — devices has no known concurrent-`Save` caller today (`pyry pair` / `pyry pair revoke` are single-goroutine CLI paths), so its "later rename wins" posture, which guarantees no torn write but *not* no lost update, was left as-is rather than generalized into a shared helper (per the issue tech note and PROJECT-MEMORY's "duplicate until a fifth registry forces extraction" rule).

`Conversation`'s slice field (`SessionHistory []string`) is **not** deep-copied at the snapshot boundary. The shallow copy is safe even though `RebindSession` (#739) now **appends** to `SessionHistory` in production: the append runs **under `r.mu`** (serialized against the snapshot copy) and only ever writes at index ≥ the old length, while a Save snapshot's view is exactly `[0:old-len]` — so the encode and a concurrent append touch disjoint addresses, and the snapshot captures a consistent pre- or post-rebind history with no torn read. `TestPool_OnRotate_RebindRaceConcurrentSave` (`internal/sessions`) exercises concurrent `RebindSession` + `Save` under `-race`. The deep-copy caveat still applies only to a hypothetical *in-place element* mutation of `SessionHistory` outside the registry lock — which no caller does; such a pattern would need a per-element `append([]string(nil), c.SessionHistory...)` deep copy.

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
- `r.List(ListFilter{IsArchived: ptrTo(true)})` — only archived (#880). `ptrTo(false)` — only active. `nil` (or the field omitted) — both, today's/unfiltered behavior.
- `r.List(ListFilter{IsPromoted: ptrTo(true), IsArchived: ptrTo(true)})` — both non-nil fields on **one** `ListFilter` AND (#880): archived channels only.

Variadic for ergonomics, **not** AND-composition **across separate `ListFilter` args**: when more than one `ListFilter` is supplied as separate variadic args, only `filter[0]` is consulted (documented in the doc comment). This is orthogonal to the AND-of-non-nil-fields rule *within* one `ListFilter` struct — the two rules operate at different levels (across args vs. within one arg) and are not in tension, though a skim can misread them as contradictory (code review NIT on #880; left as-is, not worth a rework). The returned slice is a copy; callers may mutate it freely without affecting registry state. `IsPromoted *bool` / `IsArchived *bool` each distinguish "filter to true" / "filter to false" / "no filter" (`nil`) — three states, which a bare `bool` cannot express.

### `Update(id ConversationID, fn func(*Conversation)) bool`

Locate the entry with matching `ID`, invoke `fn` with a pointer to the slice element under the registry lock, return `true`. On miss, return `false` and do not invoke `fn`.

Critical contract for callers:

- **`fn` runs with `r.mu` held.** `fn` MUST NOT call back into the registry (any `Registry` method would deadlock — `sync.Mutex` is non-reentrant).
- **`fn` MUST NOT retain the `*Conversation` pointer past return.** A future `Create` may reallocate the slice; the pointer becomes a dangling reference into the old backing array.
- **`fn` may read and mutate any field.** The registry does not validate post-mutation state — does not reject a flip that duplicates another entry's ID, does not reject `LastUsedAt` going backwards. Same "caller owns invariants" stance as `Create`.

Pointer-to-slice-element is the right shape because `Conversation` carries a `*string Name` and a `[]string SessionHistory`; pass-by-value would force `fn` to construct a full replacement struct, defeating the point. `devices` doesn't need an `Update` because device records are append-only after pairing; conversations mutate (rename, promote, rotate sessions, bump LastUsedAt, move workspace — #823), so this method is genuinely needed. See [ADR 022](../decisions/022-conversations-update-callback-under-lock.md) for the snapshot-mutate-swap alternative considered and rejected.

`Update` returns `bool`, not `(Conversation, bool)`. AC pins the no-return-value-for-the-post-state signature; if a future caller needs a post-mutation snapshot, add it then. Calling `Get(id)` after `Update` returns `true` works but races a concurrent `Update` on the same id — use the callback to read the post-state in place if that matters.

### `Delete(id ConversationID) bool`

Locate the entry whose `ID` matches and remove it via the slice-element-removal idiom (`r.conversations = append(r.conversations[:i], r.conversations[i+1:]...)`). Returns `true` on hit, `false` on miss. Mutex-guarded, no I/O, no validation, no `Save` — disk persistence stays with the caller, matching the `Create` / `Update` / `Promote` convention.

Order-preserving: surrounding entries' relative order is unchanged. O(n) linear scan + O(n) shift, same complexity as `Get` / `Update`. Returns on first match — the registry does not enforce ID uniqueness on `Create`, but `Sweep` (its original consumer) iterates a `List()` snapshot exactly once per entry, so a duplicated ID is visited and deleted twice. The `delete_conversation` handler (#822, [codebase/822.md](../codebase/822.md)) is a second consumer, calling `Delete` once per phone-supplied id rather than iterating a snapshot — this is the terminal hard-delete primitive AC #822 reuses instead of introducing a soft-delete field; a miss maps to `conversation.not_found`.

`List` returns a copy, so a snapshot taken before `Delete` is unaffected by the deletion: a caller iterating the snapshot can call `Delete` mid-loop without disturbing the iteration. This contract is pinned by the `delete-snapshot-safety` test row.

The byte-exact `==` comparison on `ID` matches `Get`'s contract; no normalization. Does not race a concurrent `Create` of the same id — both serialize through `r.mu`.

### `RebindSession(oldID, newID string) bool` (#739)

Re-points the conversation **currently bound** to `oldID` at `newID`, recording `oldID` in the history trail. Locate the entry whose `CurrentSessionID == oldID`, set `CurrentSessionID = newID` and `SessionHistory = append(SessionHistory, oldID)`, return `true`. On miss, return `false` and mutate nothing. The scan and mutation are a **single critical section under `r.mu`** — no find-then-update window a concurrent `Create`/`Delete` could redirect (the same no-TOCTOU posture as `Update`/`Promote`).

This is the **write side** of the conversation↔session binding maintenance: the pool's `/clear` rotation path calls it after `RotateID` re-keys the session map, so the binding stays current beyond the first rotation. See [`features/conversation-session-binding.md`](conversation-session-binding.md) § *Maintaining the binding across rotation* for the full data flow and the eviction-neutrality argument. The matching reverse **read** lookup (session id → conversation id) is deferred to the downstream consumer #741, which adds its own scan rather than sharing one (PROJECT-MEMORY "Resist over-DRY on duplicated registry primitives").

Contract:

- **Match key is `CurrentSessionID`, not `ID`.** Unlike every other CRUD method, this scans by the *bound session id*. A session id binds exactly one conversation (set once at creation), so **first match wins and stops**, mirroring `Get`/`Update`; pathological duplicates rebind the first only — deterministic and documented.
- **Empty `oldID` returns `false` before scanning.** An unbound conversation carries `CurrentSessionID == ""` (the unset sentinel), so a stray empty-id call must never sweep the first unbound row into a rebind. This is a **data-integrity guard at the primitive boundary**, not the eviction defense — that lives at the call site, which only rebinds on a `/clear` rotation (where `newID` is non-empty and distinct). Precondition (caller-guaranteed on the rotation path): `oldID` and `newID` non-empty and distinct.
- **`SessionHistory` is oldest-first, append-in-place.** `append(SessionHistory, oldID)` — the retired id goes on the **end**, satisfying the field's documented "rotation appends in place" contract ([`features/conversations-package.md`](conversations-package.md)). #739 is the first production caller to write this field.
- **No implicit `Save`.** Disk persistence stays with the caller, matching the `Create`/`Update`/`Promote`/`Delete` convention. The rotation caller (`Pool.rebindConversation`) `Save`s only on a `true` return, treats a `Save` error as non-fatal (the in-memory rebind is already usable), and skips `Save` entirely on a miss so the file mtime stays stable.

### `SetArchived(id ConversationID, archived bool) bool` (#880)

Flips the durable manual-archive flag: locate the entry whose `ID` matches, set
`IsArchived = archived`, return `true`. On miss, return `false` and mutate nothing. Scan and
mutation are a single critical section under `r.mu` — same no-TOCTOU posture as `Update` /
`Promote` / `RebindSession`.

- **One `bool` arg both archives and restores.** `SetArchived(id, true)` on an already-archived
  row returns `true` and leaves it archived (idempotent); there is no separate `Archive`/
  `Unarchive` pair.
- **Flips exactly one field, structurally.** The method has no way to touch `Cwd`, `Name`,
  `CurrentSessionID`, `SessionHistory`, or `IsPromoted` — this is deterministic enforcement of the
  #880/#881 contract "toggling archived must not change id, cwd, name, or session binding," not a
  convention the downstream verb handler has to remember.
- **No implicit `Save`.** Matches `Create`/`Update`/`Promote`/`Delete`/`RebindSession`; persistence
  is the caller's job.
- **Modeled on `Delete`/`RebindSession`, not built on `Update`.** `Update(id, fn func(*Conversation))`
  could express the same flip, but that hands the closure free rein over every field — the
  single-field guarantee would then live only in the caller. A dedicated ~10-line method is this
  package's established idiom for named-semantic mutations (`Promote`, `RebindSession`); `Update`
  stays the escape hatch for ad hoc multi-field changes.

Had no production callers as of #880; #881's `archive_conversation`/`unarchive_conversation`
handler (`internal/relay/handlers.ArchiveConversation`) is the sole caller, flipping the flag
then re-reading via `Get` for the reply snapshot (deliberately not folded into a single `Update`
closure — see [codebase/881.md](../codebase/881.md)). See [codebase/880.md](../codebase/880.md).

### `SetSystemPrompt(id ConversationID, prompt *string) error` (#2149)

Validates then sets the durable per-conversation system prompt: locate the entry whose `ID`
matches, assign `SystemPrompt`, return `nil`. On any refusal the registry is left untouched.
Scan and mutation are one critical section under `r.mu` — same no-TOCTOU posture as
`Update`/`Promote`/`RebindSession`/`SetArchived`. No implicit `Save`.

- **One `*string` arg spans all three states, including "clear."** `nil` returns the row to
  "no prompt"; a non-nil pointer (including one to `""`) stores its pointee after
  validation. This is deliberate, not just convenient: the ticket's premise is that every
  wire verb goes through one validated door, which is only true if the *clear* path goes
  through it too. A `prompt string` signature would leave `Update` — documented as
  deliberately unvalidated — as the only way back to "no prompt," pushing a whole verb
  outside the validated boundary. `SetArchived`'s single `bool` that both sets and clears is
  the same idea one type up.
- **Validation runs before the lock, value-first then identity** — length, then UTF-8
  validity, then the not-found scan, mirroring `Promote`'s empty-name-before-not-found
  order. Length first is an O(1) gate: checking UTF-8 validity first would force a full scan
  of an arbitrarily large hostile input before rejecting it for size. A value that is both
  over-length and invalid UTF-8 therefore returns `ErrSystemPromptTooLong`, pinned by a test
  row so a future reorder of the two checks can't silently change the sentinel a caller maps
  to a wire code.
- **Invalid UTF-8 is refused, not sanitized, because `encoding/json` substitutes U+FFFD on
  marshal.** That substitution can *grow* a value past its accepted length after it was
  already admitted — a measured 12-byte invalid input persists as 16 bytes — which would
  break the round-trip guarantee (a stored value must survive `Save` → `Load` unchanged) for
  any value near the bound. Any future byte-bounded string field in this package that also
  promises round-trip fidelity needs the same UTF-8 gate for the same reason; the two
  guarantees are incompatible without it.
- **The 8192-byte bound is sized against the wire envelope, not against the raw string.**
  `docs/protocol-mobile.md` § *Application-envelope size cap* puts the frame ceiling at
  65519 bytes, but `encoding/json`'s HTML-escaping (`<`, `>`, `&`, U+2028, U+2029) costs up
  to six bytes per escaped rune — a hostile all-`<` prompt at the bound serializes to
  roughly 49 KB, not 8192. That still leaves headroom in a single-prompt payload, so the
  bound holds today, but the "room to spare" reasoning was checked against the unescaped
  byte count and does not automatically survive a future payload that carries more than one
  prompt-sized field, or a `list_conversations`-style reply that embeds several. Re-derive
  the escaped-worst-case arithmetic before reusing this bound in a list-shaped payload.
- **Pointer ownership.** The implementation copies the pointee into a fresh local before
  taking its address, so the stored pointer never aliases a caller-held variable — the same
  defensive idiom `Promote` uses for `Name`. `Get` and `List` keep copying records shallowly
  and sharing the stored pointer, exactly as they already do for `Name`.
- **No implicit `Save`, no logger, nothing logged.** Matches `Create`/`Update`/`Promote`/
  `Delete`/`RebindSession`/`SetArchived`. All three refusal sentinels are static and
  returned naked — none interpolates the value, its length, or the conversation id — and the
  package's one logging site (`sweep_loop.go` → `sweepOnce`) logs a count and a `Save`
  error, never a record field. A refused value therefore cannot reach a log line or a wire
  error message through this path.

Store-only as of #2149: no getter, no wire verb, no CLI binding. `Get`/`List` are the read
path the sibling slices (#2150, #2152) use.

**`ErrSystemPromptInvalidUTF8` is unreachable from the `set_system_prompt` wire verb (#2151).**
`encoding/json` substitutes U+FFFD for both an invalid byte and an unpaired surrogate escape
while decoding a Go string, so whatever `json.Unmarshal` hands the handler is always valid
UTF-8 regardless of what arrived on the wire — measured against three hostile encodings, all
three decoded clean. The sentinel is still correctly kept and mapped (dropping it would be the
fail-open shape a default-arm review flagged), and it stays live for the registry's other
callers (`Update`, a future CLI) — but a test that feeds hostile bytes through the wire payload
expecting this refusal will instead land on the success path. #2151 pins the unreachability
itself with its own test rather than discovering it as a failing assertion.

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

- `TestRegistry_List_Filter` — table: nil filter, `IsPromoted=true`, `IsPromoted=false`; verifies the returned slice is a copy (mutating it does not affect a subsequent `List`); verifies the multi-filter case uses `filter[0]` only. Extended by #880 with four conversations spanning every `(IsPromoted, IsArchived)` combination so each filter's expected result is an unambiguous id set: `IsArchived=true`/`false`/nil, plus a combined `{IsPromoted, IsArchived}` case pinning the within-one-filter AND semantics.
- `TestRegistry_Update_Hit` — Update bumps `LastUsedAt`, flips `IsPromoted`, sets `Name`; subsequent `Get` reflects the mutation.
- `TestRegistry_Update_Miss` — Update on absent id returns `false`, `fn` never invoked (test-controlled flag), registry untouched.
- `TestRegistry_Update_PointerStability` — within `fn`, mutating `*Conversation` propagates to subsequent reads (pins the contract; trivially true given `&r.conversations[i]`).
- `TestRegistry_Promote` (#218) — table-driven, one row per AC bullet: success, unknown id, already promoted, name conflict against another *promoted* record (refused), name conflict against an *unpromoted* record (accepted — pins uniqueness scope), empty name, whitespace-only name. Each refusal row also asserts the target is byte-equal to its pre-call state via `Get`.
- `TestRegistry_Promote_DoesNotPersist` (#218) — `Save` → `Promote` → `Load` from the same path; the loaded registry shows `IsPromoted == false`, pinning that `Promote` does not call `Save` implicitly.
- `TestRegistry_Delete` (#237) — table-driven: `hit` (seed 1, delete returns `true`, `Get` returns `ok=false`); `miss-empty-registry`; `miss-non-matching` (seed `A`, delete `B`, `A` untouched); `preserves-order` (seed `A`, `B`, `C`; delete `B`; `List` returns `[A, C]` in order — pins the order-preserving slice idiom against an accidental swap-with-last optimisation); `delete-snapshot-safety` (seed 2, take `snap := r.List()`, `Delete(snap[0].ID)`, assert `snap` unchanged in length and element identity — pins the documented "List returns a copy" contract from #217); `delete-twice-second-misses` (seed 1, delete returns `true`, second delete returns `false`).
- `TestRegistry_RebindSession_*` (#739) — six scenarios mirroring `TestRegistry_Update_*`: `_Hit` (rebind returns `true`, target's `CurrentSessionID == newID` and `SessionHistory` tail `== oldID`, unrelated rows byte-identical); `_AppendOrder` (a row already carrying `[s0]` becomes `[s0, oldID]` — oldest-first append, not prepend); `_Miss` (no row bound to `oldID` → `false`, snapshot equality before/after); `_EmptyOldIDGuard` (a registry containing an **unbound** row, `CurrentSessionID == ""`, is **not** matched by `RebindSession("", new)` — pins the data-integrity guard); `_DoesNotPersist` (mirror `TestRegistry_Promote_DoesNotPersist` — `RebindSession` alone writes no file); `_FirstMatchOnly` (two rows pathologically bound to the same `oldID` → only the first rebinds). The pool-side rotation/eviction tests live in `internal/sessions/transition_test.go` — see [codebase/739.md](../codebase/739.md).
- `TestRegistry_SetArchived_HitSetsAndClears` / `_Miss` / `_Idempotent` (#880) — hit asserts `IsArchived` flips **and** every other field (`Name`, `Cwd`, `CurrentSessionID`, `SessionHistory`, `IsPromoted`, `LastUsedAt`) is byte-identical to its pre-call value (the single-field guarantee), then clears via the same method; miss asserts `false` and a sentinel row untouched; idempotent asserts two `true` calls both succeed and the flag stays `true`.
- `TestRegistry_SetArchived_RoundTrip` (#880, AC2) — an archived and an active conversation both survive `Save` → `Load` with their flag intact.
- `TestRegistry_Load_AbsentArchivedKeyDecodesActive` (#880, AC1) — hand-written registry JSON with the `is_archived` key omitted from a row decodes that row's `IsArchived == false`, no migration invoked.
- `TestRegistry_Save_ActiveOmitsArchivedKey` (#880, AC1) — an all-active registry's `Save` output contains no `is_archived` substring; `Save → Load → Save` is byte-identical (the byte-stable reload discipline extended to the new field).
- `TestRegistry_SetSystemPrompt_*` (#2149) — `_HitStoresVerbatim` (byte-equal stored value, every other field untouched, stored pointer not the caller's pointer); `_Refusals` (table: over-length, invalid UTF-8, unknown id, and the two combined-refusal ordering rows pinning length-first; each row also asserts `err.Error()` carries no fragment of a marked input — AC5); `_BoundaryIsBytes` (exactly `MaxSystemPromptBytes` accepted, one more refused, 4096 vs 4097 two-byte runes — pins bytes-not-runes); `_ClearAndExplicitlyEmpty` (nil clears, `strPtr("")` stores non-nil-empty, nil on an unknown id still refuses); `_RoundTrip` (none / explicitly-empty / newlines-and-non-ASCII survive `Save` → `Load`); `_DoesNotPersist` (mirrors `_Promote_DoesNotPersist`); `_RefusedValueNeverReachesDisk` (a refused marked value appears in neither file's bytes — AC5). Plus `TestRegistry_Save_NoPromptOmitsKey` and `TestRegistry_Load_AbsentPromptKeyDecodesNone` (AC4, mirroring the `IsArchived` pair) and `TestConversation_SystemPromptThreeStates` in `conversation_test.go` (AC1, the type-level round trip).

`internal/conversations/id_test.go` mirrors `internal/sessions/id_test.go`:

- `TestNewID_Format` — regex `^[0-9a-f]{8}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{12}$`, plus `ValidID` returns true.
- `TestNewID_Unique` — 1000 IDs, no duplicates.
- `TestValidID` — table: empty, wrong length, wrong dashes, wrong version nibble, wrong variant nibble, valid v4, all-uppercase (rejected — predicate is lowercase-only).

## Out of scope (deferred)

- **Schema versioning.** Per AC: defer until first migration. The envelope shape reserves the field; add it then, not now.
- **Daemon wiring.** This ticket lands the package; the supervisor / API layer that calls `Load` at startup and `Save` after mutations is a separate ticket.
- **`pyry conv promote` CLI.** The in-memory primitive landed in #218; no CLI binding exists yet. The mobile `promote_conversation` wire frame landed in #949 (sentinel-to-wire-code mapping: `ErrPromotionNameEmpty`/`ErrPromotionNameInUse` → `protocol.malformed`, `ErrConversationNotFound` → `conversation.not_found`, `ErrConversationAlreadyPromoted` → `conversation.already_promoted`) — see [`handlers.PromoteConversation`](relay-package.md) / [codebase/949.md](../codebase/949.md).
- **Cwd-move on promote — resolved as "does not move" (#949).** Mobile's `promote_conversation` payload carries a *required* (not optional) `cwd`, but #949 deliberately does not consume it: physically moving the conversation's working directory is Option A, a separate, larger security-sensitive ticket layered on top of this primitive (a `change_workspace`-style confined path set), not a silent extension of promote. `Registry.Promote`'s signature is unchanged by #949 — the wire handler composes it with `Get`/`Save`, no primitive-level cwd parameter was added.
- **`pyry conv name <id> <name>` CLI for renaming or clearing channel names.** Still out of scope — no CLI binding. The mobile-facing `rename_conversation` wire verb landed in #820 and needed **no new primitive**: it reuses `Update` directly (the same setter this doc's Types section already covers), with its own empty-name guard in the handler layer rather than in `Registry`. Clearing a name (setting `Name` back to `nil`) is still unbuilt on either surface.
- **Auto-archive predicate + sweep.** #219, #220.
- **Migration from existing `Session` registry.** TBD ticket once Conversations is proven on disk; Phase 1/2 sessions stay untouched.
- **Shared atomic-write helper across `devices` and `conversations`.** Issue tech note explicitly forbids; revisit only if real divergence cost surfaces.
- **Reading `SystemPrompt` back over the wire.** #2149 landed storage, #2150 reads it at spawn, #2151 sets it over the wire (`set_system_prompt`) — #2152 is the one slice still open. `Registry.Update` remains the unvalidated escape hatch for the field, exactly as for every other field.

## Related

- [`features/conversations-package.md`](conversations-package.md) — `Conversation` + `ConversationID` (#216), the on-disk record shape this registry persists.
- [`features/devices-registry.md`](devices-registry.md) — the structural reference implementation (atomic write, envelope shape, snapshot-then-write Save).
- [`features/sessions-registry.md`](sessions-registry.md) — the older atomic-rename recipe both registries trace to.
- [ADR 020](../decisions/020-devices-registry-snapshot-then-write.md) — Save snapshots under lock, performs I/O outside (the pattern `devices` still uses; `conversations` diverged from it in #868 — see *Save concurrency* above).
- [ADR 022](../decisions/022-conversations-update-callback-under-lock.md) — `Update` runs the caller's callback under the registry lock (over snapshot-mutate-swap).
- `internal/sessions/id.go` — the `NewID` / `ValidID` template `internal/conversations/id.go` clones.
- [`features/conversation-session-binding.md`](conversation-session-binding.md) — the consumer of `RebindSession`: the `/clear` rotation rebind that keeps the binding current (§ *Maintaining the binding across rotation*).
- [codebase/739.md](../codebase/739.md) — per-ticket note for the `RebindSession` write primitive + the pool-side rotation/eviction wiring.
- [codebase/868.md](../codebase/868.md) — per-ticket note for the `saveMu` fix (dedicated save mutex closes the cross-`Save` lost-update race).
- [codebase/880.md](../codebase/880.md) — per-ticket note for `SetArchived` + `ListFilter.IsArchived` + the `list_conversations` read surfacing.
- `docs/protocol-mobile.md` § *Application-envelope size cap* — the 65519-byte frame ceiling `MaxSystemPromptBytes` is sized against; read it alongside the HTML-escaping arithmetic in § `SetSystemPrompt` above before reusing the bound elsewhere.
- [`features/conversations-auto-archive.md`](conversations-auto-archive.md) — the `Sweep`/`ShouldArchive` hard-delete auto-archive; `SetArchived` (#880) is a distinct soft-archive mechanism, not a variant of it, but the hard-delete sweep now honours it — `ShouldArchive` exempts `IsArchived` rows (#1488).
- `docs/specs/architecture/217-conversations-registry-crud.md` — architect's spec for the CRUD foundation.
- `docs/specs/architecture/218-conversations-promotion-api.md` — architect's spec for `Promote`.
- `docs/specs/architecture/739-conversation-session-binding-rotation.md` — architect's spec for `RebindSession` + the rotation rebind.
- `docs/specs/architecture/880-durable-archived-state.md` — architect's spec for `SetArchived` + `ListFilter.IsArchived` + the read-surface projection.
- `docs/specs/architecture/2149-conversation-system-prompt.md` — architect's spec for `SystemPrompt` + `SetSystemPrompt`.
