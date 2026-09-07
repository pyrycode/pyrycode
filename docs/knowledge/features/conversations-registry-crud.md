# `conversations.json` Registry — CRUD Methods

Split out of [`conversations-registry.md`](conversations-registry.md) (2026-09-07, over the
50000-byte cap) — see that document for the envelope, atomic-write recipe, save-concurrency
model, sort discipline, and load semantics all of these methods sit on top of.

## `Create(c Conversation)`

Lock, append, unlock. **Caller owns uniqueness** — `Create` does not validate that `c.ID` is unique, well-formed, or non-empty. Same convention as `devices.Add`: keeping the registry I/O-thin lets the consuming layer (the conversations API in #218) own validation policy, which may evolve. AC pins the literal signature with no return value; match it exactly.

## `Get(id ConversationID) (Conversation, bool)`

Linear scan under lock; returns the first entry whose `ID` matches. Returns `(Conversation{}, false)` on miss. Byte-exact `==` comparison — `ConversationID` is a string newtype, no normalization. Linear scan is correct at this scale: a Phase 3 user will have O(10²) conversations at the high end. Indexing is premature.

## `List(filter ...ListFilter) []Conversation`

Returns a copy of the in-memory list, optionally narrowed by filter:

- `r.List()` — return all conversations (snapshot copy).
- `r.List(ListFilter{IsPromoted: ptrTo(true)})` — only promoted (channels).
- `r.List(ListFilter{IsPromoted: ptrTo(false)})` — only unpromoted (discussions).
- `r.List(ListFilter{IsPromoted: nil})` — equivalent to `r.List()` (nil pointer means "no filter on this field").
- `r.List(ListFilter{IsArchived: ptrTo(true)})` — only archived (#880). `ptrTo(false)` — only active. `nil` (or the field omitted) — both, today's/unfiltered behavior.
- `r.List(ListFilter{IsPromoted: ptrTo(true), IsArchived: ptrTo(true)})` — both non-nil fields on **one** `ListFilter` AND (#880): archived channels only.

Variadic for ergonomics, **not** AND-composition **across separate `ListFilter` args**: when more than one `ListFilter` is supplied as separate variadic args, only `filter[0]` is consulted (documented in the doc comment). This is orthogonal to the AND-of-non-nil-fields rule *within* one `ListFilter` struct — the two rules operate at different levels (across args vs. within one arg) and are not in tension, though a skim can misread them as contradictory (code review NIT on #880; left as-is, not worth a rework). The returned slice is a copy; callers may mutate it freely without affecting registry state. `IsPromoted *bool` / `IsArchived *bool` each distinguish "filter to true" / "filter to false" / "no filter" (`nil`) — three states, which a bare `bool` cannot express.

## `Update(id ConversationID, fn func(*Conversation)) bool`

Locate the entry with matching `ID`, invoke `fn` with a pointer to the slice element under the registry lock, return `true`. On miss, return `false` and do not invoke `fn`.

Critical contract for callers:

- **`fn` runs with `r.mu` held.** `fn` MUST NOT call back into the registry (any `Registry` method would deadlock — `sync.Mutex` is non-reentrant).
- **`fn` MUST NOT retain the `*Conversation` pointer past return.** A future `Create` may reallocate the slice; the pointer becomes a dangling reference into the old backing array.
- **`fn` may read and mutate any field.** The registry does not validate post-mutation state — does not reject a flip that duplicates another entry's ID, does not reject `LastUsedAt` going backwards. Same "caller owns invariants" stance as `Create`.

Pointer-to-slice-element is the right shape because `Conversation` carries a `*string Name` and a `[]string SessionHistory`; pass-by-value would force `fn` to construct a full replacement struct, defeating the point. `devices` doesn't need an `Update` because device records are append-only after pairing; conversations mutate (rename, promote, rotate sessions, bump LastUsedAt, move workspace — #823), so this method is genuinely needed. See [ADR 022](../decisions/022-conversations-update-callback-under-lock.md) for the snapshot-mutate-swap alternative considered and rejected.

`Update` returns `bool`, not `(Conversation, bool)`. AC pins the no-return-value-for-the-post-state signature; if a future caller needs a post-mutation snapshot, add it then. Calling `Get(id)` after `Update` returns `true` works but races a concurrent `Update` on the same id — use the callback to read the post-state in place if that matters.

## `Delete(id ConversationID) bool`

Locate the entry whose `ID` matches and remove it via the slice-element-removal idiom (`r.conversations = append(r.conversations[:i], r.conversations[i+1:]...)`). Returns `true` on hit, `false` on miss. Mutex-guarded, no I/O, no validation, no `Save` — disk persistence stays with the caller, matching the `Create` / `Update` / `Promote` convention.

Order-preserving: surrounding entries' relative order is unchanged. O(n) linear scan + O(n) shift, same complexity as `Get` / `Update`. Returns on first match — the registry does not enforce ID uniqueness on `Create`, but `Sweep` (its original consumer) iterates a `List()` snapshot exactly once per entry, so a duplicated ID is visited and deleted twice. The `delete_conversation` handler (#822, [codebase/822.md](../codebase/822.md)) is a second consumer, calling `Delete` once per phone-supplied id rather than iterating a snapshot — this is the terminal hard-delete primitive AC #822 reuses instead of introducing a soft-delete field; a miss maps to `conversation.not_found`.

`List` returns a copy, so a snapshot taken before `Delete` is unaffected by the deletion: a caller iterating the snapshot can call `Delete` mid-loop without disturbing the iteration. This contract is pinned by the `delete-snapshot-safety` test row.

The byte-exact `==` comparison on `ID` matches `Get`'s contract; no normalization. Does not race a concurrent `Create` of the same id — both serialize through `r.mu`.

## `RebindSession(oldID, newID string) bool` (#739)

Re-points the conversation **currently bound** to `oldID` at `newID`, recording `oldID` in the history trail. Locate the entry whose `CurrentSessionID == oldID`, set `CurrentSessionID = newID` and `SessionHistory = append(SessionHistory, oldID)`, return `true`. On miss, return `false` and mutate nothing. The scan and mutation are a **single critical section under `r.mu`** — no find-then-update window a concurrent `Create`/`Delete` could redirect (the same no-TOCTOU posture as `Update`/`Promote`).

This is the **write side** of the conversation↔session binding maintenance: the pool's `/clear` rotation path calls it after `RotateID` re-keys the session map, so the binding stays current beyond the first rotation. See [`features/conversation-session-binding.md`](conversation-session-binding.md) § *Maintaining the binding across rotation* for the full data flow and the eviction-neutrality argument. The matching reverse **read** lookup (session id → conversation id) is deferred to the downstream consumer #741, which adds its own scan rather than sharing one (PROJECT-MEMORY "Resist over-DRY on duplicated registry primitives").

Contract:

- **Match key is `CurrentSessionID`, not `ID`.** Unlike every other CRUD method, this scans by the *bound session id*. A session id binds exactly one conversation (set once at creation), so **first match wins and stops**, mirroring `Get`/`Update`; pathological duplicates rebind the first only — deterministic and documented.
- **Empty `oldID` returns `false` before scanning.** An unbound conversation carries `CurrentSessionID == ""` (the unset sentinel), so a stray empty-id call must never sweep the first unbound row into a rebind. This is a **data-integrity guard at the primitive boundary**, not the eviction defense — that lives at the call site, which only rebinds on a `/clear` rotation (where `newID` is non-empty and distinct). Precondition (caller-guaranteed on the rotation path): `oldID` and `newID` non-empty and distinct.
- **`SessionHistory` is oldest-first, append-in-place.** `append(SessionHistory, oldID)` — the retired id goes on the **end**, satisfying the field's documented "rotation appends in place" contract ([`features/conversations-package.md`](conversations-package.md)). #739 is the first production caller to write this field.
- **No implicit `Save`.** Disk persistence stays with the caller, matching the `Create`/`Update`/`Promote`/`Delete` convention. The rotation caller (`Pool.rebindConversation`) `Save`s only on a `true` return, treats a `Save` error as non-fatal (the in-memory rebind is already usable), and skips `Save` entirely on a miss so the file mtime stays stable.

## `SetArchived(id ConversationID, archived bool) bool` (#880)

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

## `SetSystemPrompt(id ConversationID, prompt *string) error` (#2149)

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

## `WorkspaceLabel(cwd string) (string, bool)` / `SetWorkspaceLabel(cwd string, label *string)` (#2206)

Persist an operator-chosen display name for a workspace, keyed by the exact `cwd` string
stored on conversations (byte-exact, no normalization) — **not** by conversation id, since a
workspace has no row of its own and N conversations may share one `cwd`. Both take only `r.mu`;
neither calls `Save`, matching every other primitive in this package.

- **`WorkspaceLabel` is `Get`'s shape, one key per call — deliberately not a map getter.**
  Handing back the live map would let a caller range over it outside the lock while
  `SetWorkspaceLabel` mutates, which is the fatal-throw hazard described in
  [`conversations-registry.md`](conversations-registry.md) § *Save concurrency*, not a data
  race a non-concurrency-focused test would surface.
- **`SetWorkspaceLabel`'s `*string` both sets and clears through one door**, mirroring
  `SetSystemPrompt`. `nil` deletes the key so a read reports absent rather than a present empty
  string; a non-nil pointer's pointee is copied into a fresh local before storing, so the map
  never aliases a caller-held variable. The map is lazily allocated on first store — a registry
  that never sets a label never allocates one, which means **the first store on a registry
  loaded from a pre-ticket file (no `workspace_labels` key at all) assigns into a nil map**;
  the lazy-allocation branch exists specifically for that path and is only exercised there, not
  by the common case where a label is set before the first `Save`.
- **Unvalidated by design — the caller owns non-blank and length bounds.** Unlike its neighbour
  `SetSystemPrompt`, which validates length and UTF-8 behind a sentinel, this setter has no
  refusal at all: #2207's wire handler is the sole validator, and it also owns the not-found
  refusal against the conversation list (this layer stores whatever key it is given, checked
  against nothing). A future second caller must not assume this setter behaves like its
  validated sibling just because they sit in the same file.
- **The not-found refusal #2207 owns is this map's only ceiling, not just a UX nicety.** A
  `workspace_labels` key is creatable only at a path that byte-equals a stored conversation's
  `cwd`, so key count is bounded by the number of distinct `cwd`s the daemon actually hosts
  purely because the wire handler refuses every other path. Nothing at this layer enforces
  that — a future caller that skips the existence check (a CLI binding, say) would remove the
  map's only bound. The reasoning is recorded here, next to the primitive, so a future relaxer
  finds it before shipping one.
- **`rename_workspace`'s consumer interface (`handlers.WorkspaceLabeler`) deliberately has no
  `WorkspaceLabel` read method**, even though the handler needs to echo the stored label back
  in its reply. The reply is projected from the *request's* validated value instead — which is
  provably identical to what `SetWorkspaceLabel` just stored, by its verbatim-store contract —
  so the interface has no door through which the handler could reply with a label the requester
  did not itself supply. Omitting a read method from a narrow interface is a disclosure barrier
  here, not just minimalism; the same shape is worth reaching for anywhere a handler echoes back
  exactly what it was just told to store.
- **`omitempty` on a map key tests length, not nilness** — a nil map and an allocated-then-
  emptied map both serialize to nothing. That is what makes "never set a label" and "set then
  cleared every label" indistinguishable on disk, and what lets `Save` copy the map
  unconditionally without reintroducing the key. It also means a byte-stability test built only
  from a registry that never held the field cannot catch a regression here: `Save`
  unconditionally emitting `"workspace_labels": null` (or `{}`) would still round-trip as a
  fixed point against a never-set registry while no longer matching a pre-ticket file. Proving
  the omission requires a **set-then-cleared** arm compared byte-for-byte against a never-set
  arm — the shape `TestRegistry_Save_NoLabelsOmitsKey` uses, and the pattern to copy for any
  future `omitempty`-map field.
- **`WorkspaceLabel` is itself a `Registry` method, so the `Update` warning above applies to it by name, not just in the abstract.** #2210's two producers that build a `conversation_updated` payload inside `Update`'s callback (`change_workspace`, `rename_conversation`) each learned this the concrete way: both moved the label read to *after* `Update` returns and after its `hit` check (capturing the callback-scoped `cwd`/`resolved` value first), rather than calling `WorkspaceLabel` from inside `fn`. A security review flagged the naive placement as more than a correctness bug — a goroutine parked forever holding `r.mu` inside a deadlocked callback stalls every registry consumer, a client-triggerable denial of service reachable by one ordinary frame.
- **Key the lookup off the value about to be stored, not the value the client asked for — and a test built on an identity resolver cannot catch a violation of this.** `change_workspace` confines the client's requested `Cwd` to a resolved realpath before storing it and putting it on the reply; #2210's handler looks the label up under that resolved value, never the raw request. A test whose resolver happens to be the identity function passes either way, which is why `change_workspace_test.go`'s fixture (`acceptResolver`, mapping the request path to a genuinely different realpath) stores distinct decoy labels under both the request path and the resolved path — only a resolver that actually rewrites the path, paired with decoys on both keys, can tell "keyed off the stored `cwd`" apart from "keyed off what the client asked for."
- **Per-label size bound: 128 UTF-8 bytes, picked at #2207 against the list-shaped consumer, not
  the single field.** This layer still bounds nothing itself (validation is `rename_workspace`'s,
  per above) — the number is recorded here because it was chosen with #2208 in view.
  `MaxWorkspaceLabelBytes`'s posture is borrowed from `MaxDeviceNameBytes` (bytes not runes,
  refuse rather than truncate, offending bytes never echoed) but its *value* is re-derived: worst
  case JSON escaping is 6 bytes per source byte, so a 128-byte label costs at most 768 wire bytes
  (~790 with its key) inside the 65519-byte v2 envelope #2208's `list_conversations` reply must
  fit in — roughly 80 fully-escaped worst-case labelled rows, ~50 for a realistic ASCII label.
  Picking a larger bound here would have moved that arithmetic into a ticket that couldn't
  revisit it. Key count is still unbounded at this layer and still relies on the wire handler's
  not-found refusal (previous bullet) — nothing here garbage-collects an orphan, and an orphan
  stays invisible since no client shows a workspace with no conversations.

## `Promote(id ConversationID, name string) error`

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
