# Spec: `archive_conversation` + `unarchive_conversation` wire verbs (#881)

## Context

A paired client (desktop Archive screen / Channel Info archive action, later
mobile) needs wire verbs to **archive** a conversation and later **restore** it,
and to see the change reflected without a full re-list. Mobile stubs archive
today because the daemon has no message for it; desktop is building the restore
row against these verbs. Landing them in the shared daemon lets both clients
inherit one implementation (reference-client discipline).

The durable archived state (`Conversation.IsArchived`, `Registry.SetArchived`,
`ListFilter.IsArchived`, `ConversationSummary.IsArchived`) and its list-read
surface **already landed in #880** (merged, on `main`). This slice adds the two
wire verbs that flip that state and reply with the updated record. `SetArchived`
was built by #880 specifically as this ticket's mutator — it has **no production
caller yet; #881 is that caller**.

`archive_conversation` and `unarchive_conversation` are a **symmetric toggle** of
the same durable flag — same request shape (a conversation id), same handler
structure, same not-found / reply / idempotency behavior — differing only in the
`bool` handed to `SetArchived`. They therefore ship as **one ticket** with **one
shared request payload type** and **one parameterized handler factory**
registered twice. (Splitting archive from unarchive would touch identical files
with nothing to toggle alone — the real seam is durable-state ↔ wire-verbs, which
is #880 ↔ #881; that seam is already consumed.)

Each verb is a **clone of `rename_conversation`** (#820): decode → mutate the
registry → eager best-effort `Save` → reply `conversation_updated`, correlated
via `in_reply_to`, with static user-facing error strings. It is a v1
`dispatch.Route` request/reply verb — a `v1TypeSet` member, **not** a v2-only
control frame. ("wire message" in the title refers to the encrypted v2
transport, not the v1/v2 type partition.)

### Reconciling AC1/AC2 "broadcasts" with AC5 "same path as rename"

AC1/AC2 say the verb "broadcasts a `conversation_updated`." AC5 says both types
are "dispatched through the same phone → binary write-verb path as the existing
conversation write verbs (e.g. `rename_conversation`)." These are consistent, and
**AC5 governs the mechanism**: `rename_conversation` (and create/delete) reply
**only to the requester** via `dispatch.Conn.Reply` (correlated `in_reply_to`).
There is **no** conversation-event fan-out primitive on this path — `dispatch.Conn`
exposes `Send` and `Reply`, no `Broadcast` (the `connBroadcaster` in
`cmd/pyry/assistant_turn.go` is the unrelated interactive assistant-stream
emitter, not a registry-event fan-out). The `ConversationUpdatedPayload`
doc-comment calls the type "broadcast," but its two existing producers (rename,
and now archive) deliver it as a **reply to the requester**.

**Decision:** reply-to-requester via `c.Reply`, exactly like rename. This
satisfies the Technical Notes' goal — "a client updates live counts without
re-listing": the *requesting* client archives, gets `conversation_updated` back
carrying the new `is_archived`, and updates its own row without a re-list. **Live
fan-out to *other* connected clients is OUT OF SCOPE**, matching #820 (rename) and
#822 (delete, whose reviewed spec scoped fan-out out explicitly). Do **not** build
a multi-client fan-out; other clients see the change on their next
`list_conversations`.

## Files to read first

- `internal/relay/handlers/rename_conversation.go` (all, 129 lines) — **THE
  template.** Clone its structure: decode → mutate → eager `Save` → typed
  `conversation_updated` reply; the `msg*` static-string constants; the
  `SECURITY:` doc-comment discipline; the "capture the reply snapshot then
  `Reply`" shape. #881 diverges in two places (below): it snapshots via `Get`
  (not an `Update` closure), and its malformed-branch logging follows delete's
  hardened form.
- `internal/relay/handlers/delete_conversation.go` (all, ~105 lines) — **the
  security-sensitive twin.** Copy its **malformed-branch logging divergence**
  (log `conn_id` ONLY — no `err`, no `conversation_id`) and its `SECURITY:`
  doc-comment. #881 carries the same `security-sensitive` label for the same
  reason.
- `internal/conversations/registry.go:271-281` — `SetArchived(id, archived bool)
  bool`: the #880 single-field mutator this handler calls. Flips exactly
  `IsArchived`, atomic under `r.mu`, hit/miss, **no `Save`**. A miss (`false`)
  becomes `conversation.not_found`.
- `internal/conversations/registry.go:126-137` — `Get(id) (Conversation, bool)`:
  returns a **value copy** under `r.mu`. The reply snapshot source (see
  **Snapshot for the reply** below).
- `internal/conversations/registry.go:72-115` — `Save(path) error`: atomic
  temp-file → fsync → rename. The eager best-effort persist for AC #1/#2
  restart-survival. Failure is logged, not fatal.
- `internal/relay/handlers/list_conversations.go:37-45` — projects `conv.Name` /
  `conv.IsArchived` **directly** into the reply payload. Precedent that reusing
  the snapshot's `*string` `Name` and `bool` `IsArchived` in the reply is safe.
- `internal/protocol/conversations_write.go:46-92` — `RenameConversationPayload`
  (request shape to mirror), `DeleteConversationPayload` (the id-only value-typed
  shape to mirror), and `ConversationUpdatedPayload` (the reply — **extend it**;
  see Design).
- `internal/protocol/conversations_read.go:27-39` — `ConversationSummary`, whose
  `IsArchived bool json:"is_archived"` (**no `omitempty`**, placed right after
  `IsPromoted`) is the exact wire-field precedent to mirror on
  `ConversationUpdatedPayload`, including the "always serialized so the client can
  partition active vs. archived" rationale.
- `internal/protocol/codes.go:22,10,50-75` — `CodeConversationNotFound` (22),
  `CodeProtocolMalformed` (10), the conversations `Type*` block, and the
  `TypeRenameConversation` / `TypeDeleteConversation` doc-comment shape to copy.
- `internal/protocol/envelope.go:118-138` — `v1TypeSet` map. Add two entries.
- `internal/protocol/compat_test.go` — three type lists (lines 9-19, 100-110,
  176-212) and the hardcoded `want := 19` count (line 111). Add both new types to
  all three lists; bump `19 → 21`. The partition union check (line 225) stays
  balanced because both types go into `v1TypeSet` **and** the `all` list.
- `internal/protocol/conversations_write_test.go:125-155,215-257` — the
  `*_RoundTrip` fixture pattern (request + `conversation_updated`) to clone/extend.
- `internal/protocol/testdata/conversation_updated.json` — the fixture the
  round-trip test byte-compares; **must** gain `"is_archived"` (see Design).
- `internal/relay/handlers/rename_conversation_test.go:1-130` — the handler test
  harness to clone: `dispatch.NewTestConn`, `conversations.Load` + `reg.Create`
  seeding on a `t.TempDir()` path, the `recv` helper, `assertRename…EnvelopeShape`.
- `internal/relay/handlers/register_push_token.go:128` — `replyError(ctx, c, env,
  code, message, retryable)`, the shared package helper the reject branches call.
- `internal/dispatch/dispatch.go:153` — `Conn.Reply(ctx, req, respType, payload)`
  sets `InReplyTo = req.ID` + `TS`. This satisfies the `in_reply_to` correlation;
  the handler does not build the envelope itself.
- `internal/conversations/registry_test.go:1031-1090` — `SetArchived` tests; reuse
  their archived-seed pattern (`reg.Create(Conversation{…, IsArchived: true})`)
  when seeding the unarchive / idempotent-archive cases.
- `cmd/pyry/relay.go:174-179,362-366` — the two register sites (v1 `d.Register`
  block **and** v2 `Handlers` map). Wire **both** verbs in **both** sites.

## Design

### New protocol vocabulary (`internal/protocol`)

**Two `Type*` constants** in `codes.go`, in the conversations block after
`TypeConversationDeleted`, each with a doc comment mirroring
`TypeRenameConversation` (state: phone → binary v1 `dispatch.Route` write verb,
`v1TypeSet` member, not a v2 control frame; replies with the reused
`conversation_updated`):

- `TypeArchiveConversation = "archive_conversation"` — sets `IsArchived = true`.
- `TypeUnarchiveConversation = "unarchive_conversation"` — sets `IsArchived = false`.

**One shared request payload** in `conversations_write.go` (the ticket authorizes
a single shared type — the two verbs are id-only and identical):

```go
// ArchiveConversationPayload is the body of BOTH archive_conversation and
// unarchive_conversation frames — a symmetric toggle of one durable flag, so
// one id-only payload serves both. Value-typed ConversationID mirrors
// Delete/PromoteConversationPayload.
type ArchiveConversationPayload struct {
    ConversationID string `json:"conversation_id"`
}
```

Do **not** reuse `DeleteConversationPayload` (semantic coupling / false
dependency) and do **not** mint a second `Unarchive…` type.

**Extend `ConversationUpdatedPayload`** (same file) with one field, mirroring
`ConversationSummary.IsArchived` verbatim — **no `omitempty`**, placed right after
`IsPromoted`:

```go
type ConversationUpdatedPayload struct {
    ID         string    `json:"id"`
    IsPromoted bool      `json:"is_promoted"`
    IsArchived bool      `json:"is_archived"` // NEW — always serialized (mirror ConversationSummary)
    Name       *string   `json:"name"`
    Cwd        string    `json:"cwd"`
    LastUsedAt time.Time `json:"last_used_at"`
}
```

`is_archived` is **not** `omitempty` so the client always reads the flag —
including when it is `false` (a restored/active conversation), which AC #2
requires the reply to reflect explicitly. An absent key could not distinguish
"restored to active" from "old daemon." Field placement matters for the
byte-deterministic fixture round-trip (see `docs/knowledge/codebase/274.md`):
after `IsPromoted`, grouping the two state bools, matching `ConversationSummary`.

**Add both constants to `v1TypeSet`** (`envelope.go`).

### Handler (`internal/relay/handlers/archive_conversation.go` — new)

Consumer-defined minimal interface (mirrors `ConversationRenamer` /
`ConversationDeleter`); `*conversations.Registry` satisfies it structurally, no
adapter:

```go
type ConversationArchiver interface {
    SetArchived(id conversations.ConversationID, archived bool) bool
    Get(id conversations.ConversationID) (conversations.Conversation, bool)
    Save(path string) error
}
```

**One parameterized factory** — the symmetric toggle collapses to a single
function selected by the `archived bool`:

```go
func ArchiveConversation(reg ConversationArchiver, registryPath string, logger *slog.Logger, archived bool) dispatch.Handler
```

Behavior (contract — developer writes the body by cloning
`rename_conversation.go`, taking the malformed-log form from
`delete_conversation.go`):

1. Decode `env.Payload` into `ArchiveConversationPayload`. On error → **malformed
   reject** (see Error handling).
2. `hit := reg.SetArchived(conversations.ConversationID(p.ConversationID), archived)`.
   On miss (`!hit`) → **not-found reject**.
3. `cv, ok := reg.Get(conversations.ConversationID(p.ConversationID))` — snapshot
   the post-flip record for the reply. On `!ok` → **not-found reject** (the row was
   deleted by a concurrent `delete_conversation` between the flip and the read;
   see **Snapshot for the reply**).
4. Eager best-effort `reg.Save(registryPath)`; on error, log at `Error` and
   continue — durability is best-effort, the in-memory flip already happened
   (mirrors rename/create/delete).
5. Build the reply from the snapshot and reply:
   `ConversationUpdatedPayload{ID: string(cv.ID), IsPromoted: cv.IsPromoted,
   IsArchived: cv.IsArchived, Name: cv.Name, Cwd: cv.Cwd, LastUsedAt: cv.LastUsedAt}`
   → `json.Marshal` → `return c.Reply(ctx, env, protocol.TypeConversationUpdated, payloadJSON)`.

**Verb label.** The handler derives its verb string from `env.Type` — a trusted,
dispatch-validated constant (the dispatcher matched it against the registered key
to route here, so it is exactly `"archive_conversation"` or
`"unarchive_conversation"`, never attacker bytes). Use it for the slog `event`
prefix and the malformed static message (`"malformed " + env.Type + " payload"`).
The not-found message is the shared verb-independent `"conversation not found"`.
(Two per-verb `msg*` constants selected on `archived` are an acceptable
alternative; both satisfy "static string, no payload bytes.")

**No pre-validation guard.** Unlike rename's empty-name check, there is no field to
validate beyond the id: an empty/garbage `conversation_id` simply misses
`SetArchived` and returns `conversation.not_found` (correct AC #3 behavior). Do
**not** add a distinct blank-id error branch.

**Idempotency is free.** `SetArchived(id, true)` on an already-archived row
returns `hit=true` and leaves it archived; the handler still `Save`s and still
replies `conversation_updated` with `is_archived: true` (AC #1's "still
broadcasts the unchanged state"). Same for `SetArchived(id, false)` on an active
row (AC #2). No "did the value change" branch — `SetArchived` returns only
hit/miss, and always-Save-on-hit matches the rename/delete precedent.

### Snapshot for the reply — `SetArchived` + `Get`, not an `Update` closure

`rename_conversation` snapshots inside an `Update(id, fn)` closure under one lock.
#881 deliberately **does not**: #880 shipped `SetArchived` as the dedicated
single-field mutator precisely so #881 flips the flag through **deterministic
code that structurally cannot touch another field** — stronger than a closure.
So the handler flips via `SetArchived` (honoring #880's design; not orphaning the
primitive), then reads the record back via `Get` for the reply.

`SetArchived` and `Get` are two separate locked ops. The only interleaving that
matters is a concurrent `delete_conversation` removing the row **between** them,
which surfaces as `Get` returning `ok=false`. Handle it as a **not-found reject**
(step 3): the flip committed but the record is gone, so `conversation_updated` has
no source and `not_found` is truthful for the row's current state. This window is
narrow, requires a concurrent delete of the *same* id, and mutates nothing further
— consistent with the registry's single-writer-authoritative posture (see
Concurrency). It is not a defense for an unobserved failure mode; it is correct
handling of `Get`'s `(_, bool)` contract, which the code must check regardless.

`cv.Name` (a `*string`) and `cv.IsArchived` from the value-copy snapshot are used
directly in the reply — the read-only-projection pattern `list_conversations`
already uses (`Registry.Update` replaces `Name` pointers rather than mutating the
pointee, so the snapshot's pointer is stable to read).

### `rename_conversation.go` — populate the new field (required)

Extending the shared `ConversationUpdatedPayload` obliges **every** producer to
set `IsArchived` correctly, else rename of an archived conversation would reply
`is_archived: false` (wrong, corrupting a client's archived/active partition).
Rename's only production producer is the `Update` closure at
`rename_conversation.go:89-95`; add one field to the struct literal:
`IsArchived: cv.IsArchived`. One line. The existing rename handler test still
passes (its seeded row is not archived, so the value stays `false`); the archive
handler tests cover the `true` case end-to-end.

### Wiring (`cmd/pyry/relay.go`)

Four additive lines total — both verbs in **both** register sites, reusing
`convReg` and `resolveConversationsRegistryPath(instanceName)`, with the factory's
`archived` argument selecting the verb:

- v1 `d.Register` block (~line 177):
  - `d.Register(protocol.TypeArchiveConversation, handlers.ArchiveConversation(convReg, resolveConversationsRegistryPath(instanceName), logger, true))`
  - `d.Register(protocol.TypeUnarchiveConversation, handlers.ArchiveConversation(convReg, resolveConversationsRegistryPath(instanceName), logger, false))`
- v2 `Handlers` map (~line 365): the same two entries in map-literal form.

### AC #5 "relay passes them through unchanged" — free

The relay forwards `RoutingEnvelope` frames it does not intercept without parsing
payloads (it holds zero per-user state). `archive_conversation` /
`unarchive_conversation` are ordinary v1 `dispatch.Route` request/reply verbs, not
v2 control frames the v2 session manager intercepts — so the relay passes them
through structurally, exactly as it does `rename_conversation`. **No relay code
change.** The compat partition test enforces they are `v1TypeSet` members (not
`v2OnlyTypes`), which is what "same path as rename" means concretely.

## Concurrency model

None new. `SetArchived`, `Get`, and `Save` each take the registry mutex
internally; the conversations registry is single-writer-authoritative (the
daemon), so there is **no reload-before-save** — the in-memory registry is
authoritative, exactly as create/rename/delete treat it. The handler runs on the
per-conn dispatch goroutine and spawns no goroutine, holds no lock itself, and
shares no state beyond the registry.

`SetArchived` → `Get` → `Save` is three separately-locked ops. `Save` snapshots
under the lock, so any interleaving with a concurrent handler persists a
consistent state reflecting every committed mutation (no lost update, no
corruption). The one interleaving with an observable effect — a concurrent
`delete_conversation` between `SetArchived` and `Get` — is handled by the step-3
`not_found` reject. Replay is harmless: a replayed archive/unarchive re-applies
the same idempotent flip; no nonce or idempotency key is needed (contrast the
modal verbs).

**Shutdown mid-op:** a kill after the in-memory flip but before `Save` loses the
flip on restart (the accepted best-effort durability window shared by
rename/create/delete/sweep). AC #1/#2 restart-survival is asserted only on the
Save-succeeded path.

## Error handling

Two reject branches per verb (plus the defensive concurrent-delete case folding
into not_found). **Every reject replies with a fixed static string — no attacker
payload bytes (the supplied id, a decode-error fragment) ever reach the wire.**

| Branch | Trigger | Wire reply | Log fields |
|---|---|---|---|
| malformed | `json.Unmarshal` fails | `CodeProtocolMalformed` + static `"malformed <type> payload"`, non-retryable | event + `conn_id` **only** |
| not_found | `SetArchived` miss, **or** `Get` miss (concurrent delete) | `CodeConversationNotFound` + static `"conversation not found"`, non-retryable | event + `conn_id` + `conversation_id` |
| success | `SetArchived` hit + `Get` hit | `TypeConversationUpdated` + updated record | event + `conn_id` + `conversation_id` |

**Deliberate divergence from the rename template — keep it, do not "fix" the
handler back to match rename** (mirrors `delete_conversation.go`, mandated by the
`security-sensitive` label): the **malformed branch logs `conn_id` only** — NOT the
decode `err` (Go `*json.SyntaxError` can embed offending input bytes) and NOT
`conversation_id` (a decode failure leaves the struct partially populated, so
`p.ConversationID` may hold raw attacker bytes). The not_found and success
branches log `conversation_id` as a **structured slog field** (handler-escaped,
non-secret opaque id): on success it is a proven-real id (`SetArchived` returned
hit), and on either failure path the id never touches the wire.

## Testing strategy

Four surfaces, all cloning existing patterns.

### Compat drift (`internal/protocol/compat_test.go`)

Add `TypeArchiveConversation, TypeUnarchiveConversation` to all three type lists
(lines 9-19, 100-110, 176-212) and bump `want := 19` → `21`. The
`TestTypeConstants_V1V2Partition` union check re-passes because both types are v1
members. No new test function.

### Protocol round-trip (`internal/protocol/conversations_write_test.go` + testdata)

- **`ArchiveConversationPayload` round-trips** through an envelope: add fixture(s)
  and a test modeled on `TestRenameConversationPayload_RoundTrip`. One shared
  payload type serves both verbs; two envelope fixtures
  (`archive_conversation.json` / `unarchive_conversation.json`, differing only in
  `type`) exercise both wire types.
- **Extend `TestConversationUpdatedPayload_RoundTrip`**: the fixture
  `testdata/conversation_updated.json` must gain `"is_archived":false` (in the
  is_promoted-adjacent position) or the byte-equal `canonical` compare fails; add
  a `p.IsArchived` assertion.

### Handler (`internal/relay/handlers/archive_conversation_test.go` — new)

Clone `rename_conversation_test.go`'s harness (`NewTestConn`, `Load` + `Create`
seed on a temp path, `recv`, `assert…EnvelopeShape`). Because both verbs share the
factory, exercise both `archived=true` and `archived=false` registrations.
Scenarios (bullets, not code):

- **Archive success + persistence + reply shape (AC #1).** Seed one active
  conversation. Invoke the `archived=true` handler naming its id. Assert: reply is
  `conversation_updated`, `in_reply_to` == request id, `id`==1 on a fresh conn,
  payload `is_archived == true`, and `id`/`is_promoted`/`name`/`cwd`/`last_used_at`
  are unchanged from the seed; the registry row's `IsArchived` is true; **and a
  fresh `conversations.Load` of the temp path returns the row with `IsArchived`
  true** (eager `Save` persisted — restart-survival).
- **Unarchive success (AC #2).** Seed a row with `IsArchived: true`. Invoke the
  `archived=false` handler; assert reply `is_archived == false`, row cleared,
  reload shows active.
- **Archive idempotent (AC #1).** Seed archived; invoke `archived=true`; assert no
  error, reply `is_archived == true`, still persisted/replied.
- **Unarchive idempotent (AC #2).** Seed active; invoke `archived=false`; assert no
  error, reply `is_archived == false`.
- **not_found — both verbs (AC #3).** Invoke each handler for an id not in the
  registry; assert reply is an `error` frame with `conversation.not_found`, the
  registry is unchanged, and the wire payload contains no echo of the supplied id.
- **Malformed — both verbs (AC #4).** Send a payload undecodable into
  `ArchiveConversationPayload` (e.g. a JSON array, or `conversation_id` as a
  number); assert reply is `protocol.malformed` with a static message containing
  no payload bytes. If the harness captures logs (slog test handler), assert the
  malformed record carries no `err`/`conversation_id` field bearing payload bytes.

Run `go test -race ./...`, `go vet ./...`, `staticcheck ./...`.

## Scope note (sizing) — Size: S, do not split

**§1 primary red lines pass with margin:** 2 new `.go` files
(`archive_conversation.go` + its test), ~500 total LOC projected (handler ~110
cloned; handler test ~250; vocab ~35; protocol round-trip tests + 2 fixtures ~45;
1-line rename populate; compat/relay edits ~20), **2** new exported types
(`ArchiveConversationPayload`, `ConversationArchiver` — `ConversationUpdatedPayload`
is extended, not new; the two `Type*` are consts), **zero** consumer call-site
cascade (purely additive — no existing signature changes; the rename touch is a
one-field literal addition, not a signature edit), 2 reject branches per verb, and
5 ACs that are facets of one shared handler.

**§4 production-file self-check — transparent count.** The spec prescribes new or
modified content for **6** production `.go` files: `codes.go`,
`conversations_write.go`, `envelope.go` (three single-package protocol-vocab
one-liners), `archive_conversation.go` (new handler), `rename_conversation.go`
(one-line field populate), `cmd/pyry/relay.go` (four wiring lines). This is the
documented **write-verb false-positive**, not a real oversize, and there is **no
valid split**, for three independent reasons:

1. **All daemon primitives pre-exist.** `SetArchived` + `Get` + `Save` +
   `CodeConversationNotFound` + `ConversationUpdatedPayload` + the list-read
   surface are already on `main` (#880 / earlier). The net-new surface is the
   handler + protocol vocab only — exactly the shape #820 (`rename`) and #822
   (`delete`) shipped as **one S each** (both merged). This is *not* the
   multi-concern vertical slice the §4 gate targets (a new signal threaded through
   new model → mapper → adapter → consumer layers); here the vertical is thin
   because there is nothing new below the handler.
2. **The only structural seam is already consumed.** Parent #821 was split on the
   durable-state ↔ wire-verbs boundary into #880 (state) and this ticket (verbs).
   There is no second seam to cut.
3. **The residual "seam" (archive vs unarchive) is PO-forbidden.** They are a
   symmetric toggle sharing one payload and one handler factory; a per-verb split
   would touch identical files with nothing to independently toggle (artificial
   halving). A vocab-only vs handler split would ship an unobservable, unhandled
   verb (a `v1TypeSet` member routing nowhere) — the unwired-primitive
   anti-pattern; and it contradicts the #820/#822 precedent of shipping vocab +
   handler together.

The 6th file beyond #820's 5 is the one-line `rename_conversation.go` populate,
forced by extending the shared reply payload — a trivial edit, not a new concern.
PO has ruled `size:s` (label present; the #821 split decision and #822's
reasoning both name #881 as the one-S verb child). Per architect guidance, this is
documented transparently rather than round-tripped to a PO who already ruled.

## Open questions

- **Reply field placement / `protocol-mobile.md`.** `is_archived` is placed after
  `is_promoted` to mirror `ConversationSummary`; the fixture must match in
  lockstep. `rename_conversation`/`delete_conversation` are **not** listed in the
  `docs/protocol-mobile.md` message-types table (§ Application message types), so
  per that precedent this slice adds **no** `protocol-mobile.md` change — the
  developer's worktree stays code + tests + this spec. If integration reveals a
  desktop/mobile client keying on a different field position, reconcile at
  integration; the wire is additive and clients ignore unknown ordering.
- **Shared vs per-verb payload name.** `ArchiveConversationPayload` is shared by
  both verbs (the ticket authorizes this). If a future reviewer prefers an
  explicit `UnarchiveConversationPayload` alias, it is a mechanical addition; the
  handler is unaffected (it decodes an id-only body).

---

## Security review pass

**Verdict:** PASS

The one MUST-FIX the pass surfaces (malformed-branch logging leaking payload
bytes) is addressed inline in **Error handling** before this verdict — the spec
mandates the delete-style logging divergence. No open MUST-FIX remains.

**Findings:**

- **[Trust boundaries]** No open MUST-FIX. One boundary: `ArchiveConversation`,
  one `json.Unmarshal` of the phone-supplied `ArchiveConversationPayload`. Its
  single untrusted field `conversation_id` is used *only* as an exact-match
  registry key inside `SetArchived` and `Get` (byte comparison; no path/argv/query
  construction — a miss is `conversation.not_found`, and a phone cannot mint or
  collide an id, only name one that already exists). Downstream holds parsed types.
  Within a server-id, paired devices are one trust domain (ADR 025 § Security
  model); any paired phone may archive/restore any conversation, consistent with
  `list`/`create`/`promote`/`rename`. The tenant boundary is the server-id,
  enforced structurally at the Noise IK handshake (unpaired → 4401, never reaches
  `dispatchAppFrame`). **Archive-specific note:** unlike delete, archive is
  **reversible** (unarchive restores) — strictly lower stakes than the reviewed
  delete twin; a paired device toggling a durable metadata flag is a benign edit,
  no elevated per-device gate beyond pairing.
- **[Over-mutation / wrong-target]** No finding — **stronger than rename.**
  `SetArchived` flips *exactly* `IsArchived` on the first row whose `ID == id` and
  returns; it structurally cannot touch `id`/`cwd`/`name`/promoted/session state
  (#880's deterministic single-field guarantee, not a closure convention).
  Conversation ids are unique, high-entropy (`crypto/rand`, minted by `create`),
  so the exact-match mutates exactly one caller-named row — no wildcard/prefix
  match, no collateral row.
- **[Tokens/secrets]** N/A — mints, stores, compares no token or secret; mints no
  id (no `crypto/rand` surface).
- **[File operations]** No finding. No user input reaches a filesystem path:
  `registryPath` is server-derived (`resolveConversationsRegistryPath`);
  `conversation_id` is a registry key, never a path component. Persistence reuses
  `Registry.Save` (atomic temp-file `0600` + `0700` dir + fsync + rename); no new
  stat-then-open, no TOCTOU, no symlink surface.
- **[Subprocess]** N/A — executes no subprocess; passes no value to
  `exec.Command`.
- **[Cryptographic primitives]** N/A — no crypto in the handler; AEAD framing is
  the unchanged transport layer's concern.
- **[Network, I/O & DoS]** No MUST-FIX. The handler reads no socket; inbound frame
  size is capped by the v2 transport decoder upstream, transitively bounding
  `conversation_id` length. No explicit application id-length cap — acceptable: a
  toggle neither accumulates state nor amplifies (it flips one existing bool), fans
  out to no one, and a miss is a bounded linear scan over a small per-user
  registry. Not a DoS amplifier. No HTTP/WS/TLS surface added.
- **[Error messages, logs, telemetry]** MUST-FIX addressed inline; the reason for
  the label. (1) Every reject replies with a fixed static string — no payload
  bytes (the supplied id, a decode-error fragment) reach the wire (AC #4 wire
  half). (2) The malformed branch logs **`conn_id` only** — not the decode `err`
  (`*json.SyntaxError` can embed offending input bytes) and not `conversation_id`
  (partial-populate on decode failure → may hold raw attacker bytes). This is the
  deliberate divergence from the rename template, copied from
  `delete_conversation.go`. (3) not_found and success log `conversation_id` as a
  structured slog field (handler-escaped, non-secret opaque id) per the reviewed
  rename/create/delete precedent; on success it is a proven-real id, on failure it
  never touches the wire. The verb label in the malformed message is `env.Type`, a
  trusted dispatch-validated constant (never attacker bytes). No telemetry.
- **[Information disclosure / enumeration]** No finding. The not_found-vs-success
  distinction is an existence oracle, but a paired phone already enumerates all
  conversations via `list_conversations`, so archive grants no new capability; ids
  are high-entropy, defeating blind enumeration. The new `is_archived` reply field
  is a boolean state flag the requester already learns from `list_conversations` —
  no new information disclosed.
- **[Concurrency]** No finding. The handler holds no lock itself; `SetArchived` /
  `Get` / `Save` each take `r.mu` internally. The `SetArchived`→`Get` window is
  handled: a concurrent `delete` of the same id surfaces as a `Get` miss →
  `not_found` (a consistent, committed-state reply; no lost update, no leak).
  `Save` snapshots under the lock, so any interleaving persists a consistent
  state. Replay is harmless — a replayed toggle re-applies the same idempotent
  flip; no nonce/idempotency key needed (contrast the modal verbs). Shutdown
  before `Save` reloads the pre-flip state on restart (accepted best-effort
  durability window shared by rename/create/delete/sweep).
- **[Threat model alignment]** Untrusted-phone-input, cross-tenant, and
  over-mutation threats are in scope and addressed above. **Live fan-out of the
  archive/restore to *other* connected clients is explicitly OUT OF SCOPE** (per
  the ticket, matching rename/delete): the `conversation_updated` reply goes only
  to the requester; other clients observe the change on their next
  `list_conversations`. A live multi-client broadcast is a separate future
  concern.

**Reviewer:** architect (self-review per `architect/security-review.md`)
**Date:** 2026-07-09
