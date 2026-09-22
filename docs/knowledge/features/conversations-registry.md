# `conversations.json` Registry

On-disk persistence for `internal/conversations.Registry`. Stores the binary's per-conversation state — id, name, cwd, current/historical session ids, promotion flag, last-used timestamp — at `~/.pyry/<name>/conversations.json`. Phase 3 storage primitive consumed by future promotion API (#218), auto-archive predicate (#219), and auto-archive sweep (#220).

Lives in the same `internal/conversations` package as the `Conversation` type (#216) — no subpackage. Stdlib only (`encoding/json`, `errors`, `fmt`, `io/fs`, `os`, `path/filepath`, `sort`, `sync`).

## Status

- **Phase 3 foundation (#217):** mutex-guarded `Registry` + atomic save + load. ID generator + validator (`NewID`, `ValidID`) co-located in the same package. Six exports on the registry: `Load`, `(*Registry).Save / Create / Get / List / Update`. One `ListFilter` struct.
- **Promotion primitive (#218):** `(*Registry).Promote(id, name)` flips a discussion to a named channel under the registry lock; four exported sentinel errors (`ErrConversationNotFound`, `ErrConversationAlreadyPromoted`, `ErrPromotionNameInUse`, `ErrPromotionNameEmpty`) cover the refusal cases.
- **Deletion primitive (#237):** `(*Registry).Delete(id) bool` removes a single entry by ID under the registry lock; consumed by the auto-archive sweep ([`features/conversations-auto-archive.md`](conversations-auto-archive.md)). #217 explicitly deferred deletion until a real consumer surfaced; #220's sweep is that consumer.
- **Rotation-rebind primitive (#739):** `(*Registry).RebindSession(oldID, newID string) bool` re-points the conversation bound to `oldID` at `newID` and appends `oldID` to `SessionHistory`, under the registry lock; consumed by the pool's `/clear` rotation path so the conversation↔session binding stays current beyond the first rotation ([`features/conversation-session-binding.md`](conversation-session-binding.md) § *Maintaining the binding across rotation*). The first production caller to **write** `SessionHistory`.
- **Durable manual-archive primitive (#880):** `(*Registry).SetArchived(id, archived bool) bool` flips the durable `Conversation.IsArchived` flag under the registry lock; `ListFilter.IsArchived *bool` narrows `List` to active-only/archived-only/both, ANDing with `IsPromoted` when both are set on one filter. Distinct from the auto-archive `Sweep` ([`features/conversations-auto-archive.md`](conversations-auto-archive.md)), which permanently deletes rather than flagging — but no longer independent of it: since #1488 `ShouldArchive` reads `IsArchived` and the sweep skips archived rows, so a manual archive is durable until the user unarchives. Called by the `archive_conversation`/`unarchive_conversation` wire verbs (#881), the sole production caller. See [codebase/880.md](../codebase/880.md), [codebase/881.md](../codebase/881.md).
- **Bounded system-prompt primitive (#2149):** `(*Registry).SetSystemPrompt(id ConversationID, prompt *string) error` validates and sets `Conversation.SystemPrompt` under the registry lock; `MaxSystemPromptBytes = 8192` (inclusive) and two sentinels (`ErrSystemPromptTooLong`, `ErrSystemPromptInvalidUTF8`) join `ErrConversationNotFound` as the refusal set. #2150 reads the stored value at spawn (`Pool.refreshSystemPrompt`, called from `Pool.Activate`), #2151 wires `SetSystemPrompt` to the `set_system_prompt` wire verb (`handlers.SetSystemPrompt`, [`relay-package.md`](relay-package.md)), and #2152 reads it back over the wire (`request_system_prompt` / `system_prompt`, [v2-session-manager doc](v2-session-manager-state-machine-inbound-request-system-prompt-systempromptfor-seam.md)) — the full cluster is landed. `Registry.Update` remains the unvalidated escape hatch for the field, exactly as for every other field.
- **Workspace-label storage primitive (#2206):** `(*Registry).WorkspaceLabel(cwd string) (string, bool)` / `(*Registry).SetWorkspaceLabel(cwd string, label *string)` persist an operator-chosen display name for a workspace, keyed by the exact `cwd` string (byte-exact, no normalization) rather than by conversation id — a workspace has no row of its own, so the label lives in its own top-level map instead of a per-conversation field. Storage only: no wire verb sets it yet (#2207) and no payload reads it onto the wire yet (#2208, #2210). See § *`WorkspaceLabel` / `SetWorkspaceLabel`* below.
- **Last context-usage reading primitive (#2460):** `(*Registry).SetLastContextUsage(id ConversationID, reading ContextUsageReading) bool` records the summary of the last context-window reading claude reported for a conversation — `Model`, `TotalTokens`, `MaxTokens`, `Percentage`, `AsOf` — under the registry lock, structurally touching only `Conversation.LastContextUsage`. Takes a value rather than a pointer, unlike `SetSystemPrompt`'s tri-state door: no producer ever clears a reading, so "set it back to nil" is unreachable rather than merely unused. Two production callers write through the same recorder — the post-turn interactive-turn emitter arm and the on-demand `request_context_usage` flight — so the stored value is whichever reading claude produced last; last write wins by design (see § *`SetLastContextUsage`* in [`conversations-registry-crud.md`](conversations-registry-crud.md)). No wire verb reads it back yet — deferred as a named follow-up, see *Out of scope* below.
- **Removal observer (#1502):** `(*Registry).SetOnDelete(fn func(id ConversationID))` installs a single removal callback, replacing any earlier one; `nil` clears it. `Delete` calls it once per hit, with that conversation's id, after releasing `r.mu` — never on a miss. Because `Delete` is the one funnel both removal paths use (the `delete_conversation` handler and the idle `Sweep`), this one slot sees every removal regardless of which path caused it. The daemon's only caller wires it to `(*eventring.Ring).Drop`, freeing the removed conversation's retained replay events (see [`features/eventring-package.md`](eventring-package.md) § *Ownership & wiring*). See § *`SetOnDelete`* in [`conversations-registry-crud.md`](conversations-registry-crud.md).

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
func (r *Registry) SetOnDelete(fn func(id ConversationID))
func (r *Registry) Promote(id ConversationID, name string) error
func (r *Registry) RebindSession(oldID, newID string) bool
func (r *Registry) SetArchived(id ConversationID, archived bool) bool
func (r *Registry) SetSystemPrompt(id ConversationID, prompt *string) error
func (r *Registry) WorkspaceLabel(cwd string) (string, bool)
func (r *Registry) SetWorkspaceLabel(cwd string, label *string)
func (r *Registry) SetLastContextUsage(id ConversationID, reading ContextUsageReading) bool

type ContextUsageReading struct {
    Model       string
    TotalTokens int
    MaxTokens   int
    Percentage  int
    AsOf        time.Time
}
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
operator-set one; see [`conversations-registry-crud.md`](conversations-registry-crud.md) § `SetSystemPrompt`.

`workspace_labels` (#2206) is a **top-level** sibling of `conversations`, not a per-row field —
a workspace has no row of its own, and N conversations can share one `cwd`. Omitted from the
example above because no label is set; when present it looks like:

```json
{
  "conversations": [ ... ],
  "workspace_labels": {
    "/Users/juhana/projects/taxes": "Tax Filing"
  }
}
```

See § *`WorkspaceLabel` / `SetWorkspaceLabel`* below.

`last_context_usage` (#2460) is likewise omitted here — this row has never had a reading recorded.
When present it looks like:

```json
{
  "last_context_usage": {
    "model": "claude-opus-5",
    "total_tokens": 41000,
    "max_tokens": 200000,
    "percentage": 21,
    "as_of": "2026-09-16T07:05:03Z"
  }
}
```

All five inner keys always emit — no `omitempty` inside `ContextUsageReading` — because a
fresh session genuinely reads zero tokens at zero percent, and `omitempty` there would encode that
fact as an absence. The outer field on `Conversation` carries the never-reported state instead:
`nil` (an absent `last_context_usage` key) means claude has never reported for this conversation,
not "reported a zero reading." `as_of` is normalized to UTC by the setter, so it always encodes
with a `Z` offset regardless of what the daemon clock's `time.Time` carried.

**The event's three inventories are deliberately absent.** `turnevent.ContextUsage` — the event
both producers read this summary from — also carries `Categories`, `MCPTools`, and `MemoryFiles`
breakdowns with their dropped counts. None of the three reaches this file: they carry workspace
memory-file paths and MCP server names that the frame's no-log rule keeps out of records, and a
reconnecting client needs the headline numbers rather than a breakdown taken some turns ago. The
projection that builds a `ContextUsageReading` lists exactly the five stored fields, so there is no
field to forget to strip.

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

**A map-valued top-level field cannot reuse the slice's shallow-copy reasoning (#2206).** `workspaceLabels map[string]string` is copied **element-wise** into the snapshot inside the same `r.mu` section, not shared by map-header copy. The slice argument above works only because appends land at indices at or past the snapshot's length — maps have no equivalent disjointness: a map-header copy aliases the same buckets, so a concurrent `delete` or an insert-triggered rehash during `json.Marshal`'s range is a fatal `concurrent map iteration and map write` runtime throw that **kills the daemon**, not a race the detector reports or a non-concurrent test would ever reach. The same hazard is why `WorkspaceLabel` answers per-key `(string, bool)` rather than returning the map itself — a caller ranging over a returned map outside the lock hits the identical throw. Any future map-valued registry field needs the same two disciplines: element-wise copy in `Save`, per-key reads only.

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

Split into [`conversations-registry-crud.md`](conversations-registry-crud.md) (2026-09-07, this
document was over the 50000-byte cap). Covers `Create`, `Get`, `List`, `Update`, `Delete`,
`SetOnDelete` (#1502), `RebindSession` (#739), `SetArchived` (#880), `SetSystemPrompt` (#2149),
`WorkspaceLabel` / `SetWorkspaceLabel` (#2206), `SetLastContextUsage` (#2460), and `Promote`.

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
- `TestRegistry_WorkspaceLabel_*` (#2206) — `_SetReadClear` (table: unset/set/overwrite/clear/
  clear-unset/explicit-empty-vs-cleared, plus byte-exact key rows distinguishing `/a`, `/a/`,
  `/A`, and a trailing-space variant as four distinct keys); `_RoundTrip` (two labels survive
  `Save` → `Load`, plus an insertion-order-permutation arm pinning `encoding/json`'s sorted-map-
  key output, plus a non-ASCII/newline arm); `_DoesNotPersist` (mirrors
  `_SetSystemPrompt_DoesNotPersist`); `_ConcurrentAccess` (goroutines mixing `SetWorkspaceLabel`,
  `WorkspaceLabel`, `Create`, `Save` — the one test that exercises `Save`'s encode against a live
  concurrent map writer, i.e. the fatal-throw path `-race` alone does not report). Plus
  `TestRegistry_Save_NoLabelsOmitsKey` (two arms: never-set, and set-then-cleared — the second is
  the one a `IsArchived`/`SystemPrompt`-shaped byte-stability test structurally cannot cover) and
  `TestRegistry_Load_AbsentLabelKeyDecodesEmpty` (mirrors `_AbsentPromptKeyDecodesNone`).
- `TestRegistry_SetLastContextUsage_*` (#2460) — hit stores exactly the five values and leaves
  every other field of the row and every other row untouched; miss returns `false` and modifies
  nothing; a non-UTC `AsOf` is stored as UTC and marshals with a `Z` offset; the stored pointer is
  not the caller's (mutating the caller's local after the call does not affect the stored row); the
  setter does not persist (mirrors `TestRegistry_SetSystemPrompt_DoesNotPersist`). Plus
  `TestRegistry_Save_NilLastContextUsageOmitsKey` (an all-nil registry's `Save` output contains no
  `last_context_usage` substring — the `IsArchived`/`SystemPrompt`-shaped byte-stability pair) and a
  `Save` → `Load` round trip using `time.Date` fixtures, never `time.Now`, so no monotonic component
  survives to break `DeepEqual`.

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
- **Workspace-label wire surface.** #2206 lands storage only. Setting a label over the wire (#2207, which also owns the not-found/non-blank/length refusals), surfacing it on `list_conversations` (#2208), and pushing it on live frames (#2210) are separate tickets against the same two accessors.
- **Answering `request_context_usage` from the stored `last_context_usage` field when no fresh reading can be taken.** #2460 lands storage only, written by both producers; no wire verb reads this field back yet — a reconnecting client still gets a fresh reading or nothing, never the stored one. Named as this ticket's own follow-up.

## Related

- [`features/conversations-registry-crud.md`](conversations-registry-crud.md) — the CRUD method reference (`Create`/`Get`/`List`/`Update`/`Delete`/`SetOnDelete`/`RebindSession`/`SetArchived`/`SetSystemPrompt`/`WorkspaceLabel`+`SetWorkspaceLabel`/`SetLastContextUsage`/`Promote`), split out of this document.
- [`features/eventring-package.md`](eventring-package.md) — the sole production consumer of `SetOnDelete`: `cmd/pyry` wires the observer to `(*eventring.Ring).Drop` so a removed conversation's retained replay events don't pin daemon memory (#1502).
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
- `docs/protocol-mobile.md` § *Application-envelope size cap* — the 65519-byte frame ceiling `MaxSystemPromptBytes` is sized against; read it alongside the HTML-escaping arithmetic in [`conversations-registry-crud.md`](conversations-registry-crud.md) § `SetSystemPrompt` before reusing the bound elsewhere.
- [`features/conversations-auto-archive.md`](conversations-auto-archive.md) — the `Sweep`/`ShouldArchive` hard-delete auto-archive; `SetArchived` (#880) is a distinct soft-archive mechanism, not a variant of it, but the hard-delete sweep now honours it — `ShouldArchive` exempts `IsArchived` rows (#1488).
- `docs/specs/architecture/217-conversations-registry-crud.md` — architect's spec for the CRUD foundation.
- `docs/specs/architecture/218-conversations-promotion-api.md` — architect's spec for `Promote`.
- `docs/specs/architecture/739-conversation-session-binding-rotation.md` — architect's spec for `RebindSession` + the rotation rebind.
- `docs/specs/architecture/880-durable-archived-state.md` — architect's spec for `SetArchived` + `ListFilter.IsArchived` + the read-surface projection.
- `docs/specs/architecture/2149-conversation-system-prompt.md` — architect's spec for `SystemPrompt` + `SetSystemPrompt`.
- `docs/specs/architecture/2206-workspace-label-registry-storage.md` — architect's spec for `WorkspaceLabel` + `SetWorkspaceLabel`.
- [`features/v2-session-manager-concurrency.md`](v2-session-manager-concurrency.md) § `request_context_usage` — the on-demand producer's resolver/flight machinery `SetLastContextUsage`'s second caller (`contextUsageResolver.fly`) writes into.
- `docs/specs/architecture/2460-conversation-last-context-usage.md` — architect's spec for `ContextUsageReading` + `SetLastContextUsage` + the two-producer `contextUsageRecorder`.
