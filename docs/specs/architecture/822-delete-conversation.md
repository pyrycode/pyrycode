# Spec: `delete_conversation` wire message (#822)

## Context

A paired client (desktop Channel Info **Delete**, later mobile) needs a wire verb
to **permanently** remove a conversation so it disappears from every paired
client. Mobile currently stubs the action because the daemon has no message for
it; desktop is building its Delete button against this verb. Landing it in the
shared daemon gives both clients one implementation.

**Destructive semantics — hard (permanent) delete.** The reversible path already
exists as archive/unarchive (#881, on #880's durable `is_archived` state). A
`delete` distinct from `archive` must be the permanent path, else the two verbs
are redundant. Hard delete maps onto the terminal removal primitive the daemon
already uses — `conversations.Registry.Delete(id) bool`, invoked by Sweep — so
**no new registry field, schema, or soft-delete state** is introduced. Archive =
recoverable; delete = gone. Independent of #881.

This verb is a **clone of `rename_conversation`** (#820): decode → mutate the
registry → eager best-effort `Save` → typed reply, correlated via `in_reply_to`,
with static user-facing error strings. It is a v1 `dispatch.Route` request/reply
verb — a `v1TypeSet` member, **not** a v2-only control frame. ("v2 wire message"
in the title refers to the encrypted v2 transport, not the v1/v2 type partition.)

## Files to read first

- `internal/relay/handlers/rename_conversation.go` (all, 129 lines) — **THE
  template.** Clone its structure verbatim: decode → mutate → eager `Save` →
  typed reply; the `msg*` static-string constants; the `SECURITY:` doc comment
  discipline. Note the two deliberate divergences called out under **Error
  handling** below (malformed-branch logging).
- `internal/relay/handlers/register_push_token.go:120-138` — `replyError(ctx, c,
  env, code, message, retryable)` and `replyAck`: the shared package helpers the
  reject branches call. `replyError` marshals a static `protocol.ErrorPayload`.
- `internal/dispatch/dispatch.go:149-163` — `Conn.Reply(ctx, req, respType,
  payload)` sets `InReplyTo = req.ID` and `TS`. This is what satisfies AC #2's
  `in_reply_to` correlation — the handler does not build the envelope itself.
- `internal/conversations/registry.go:240-251` — `Delete(id ConversationID)
  bool`: exact byte-match removal under the registry lock; returns hit/miss. The
  terminal primitive AC #1 reuses. **A miss (`false`) becomes
  `conversation.not_found`.**
- `internal/conversations/registry.go:72-115` — `Save(path string) error`:
  atomic temp-file→fsync→rename. The eager best-effort persist for AC #1's
  restart-survival. Failure is logged, not fatal (mirrors rename/create).
- `internal/protocol/conversations_write.go:46-71` — `RenameConversationPayload`
  (request; `conversation_id` json tag) and `ConversationUpdatedPayload` (reply;
  `id` json tag). Mirror these for the two new payload structs.
- `internal/protocol/codes.go:50-62` — the conversations `Type*` const block and
  the `TypeRenameConversation` doc-comment shape to copy. `CodeConversationNotFound`
  (line 22) and `CodeProtocolMalformed` (line 10) are the two wire codes reused.
- `internal/protocol/envelope.go:118-136` — `v1TypeSet` map. Add two entries.
- `internal/protocol/compat_test.go` — three type lists (lines 9, 99, 174) and
  the hardcoded `want := 17` count (line 109). Add both new types to all three
  lists; bump `17 → 19`. The partition union check (line 222) stays balanced
  because both types go into `v1TypeSet` **and** the `all` list.
- `internal/relay/handlers/rename_conversation_test.go:1-70` — the handler test
  harness to clone: `dispatch.NewTestConn`, `conversations.Load` + `reg.Create`
  seeding on a `t.TempDir()` path, the `recv` helper reading one outbound frame.
- `cmd/pyry/relay.go:175-176` and `362-363` — the two register sites: the v1
  `d.Register(...)` block **and** the v2 `Handlers` map. Wire the handler in
  both, mirroring create/rename.

## Design

### New protocol vocabulary (`internal/protocol`)

Two `Type*` constants in `codes.go`, in the conversations block after
`TypeRenameConversation`, each with a doc comment mirroring the
`TypeRenameConversation` comment (state it is a v1 `dispatch.Route` write verb, a
`v1TypeSet` member, not a v2 control frame):

- `TypeDeleteConversation = "delete_conversation"` — phone → binary request.
- `TypeConversationDeleted = "conversation_deleted"` — binary → phone reply.

Two payload structs in `conversations_write.go`, mirroring the existing family's
json-tag conventions:

- `DeleteConversationPayload{ ConversationID string `json:"conversation_id"` }`
  — the request body. One required field, the target id (mirrors
  `PromoteConversationPayload` / `RenameConversationPayload`'s value-typed
  `ConversationID`).
- `ConversationDeletedPayload{ ID string `json:"id"` }` — the ack body. Uses the
  `id` json tag matching `ConversationCreatedPayload` / `ConversationUpdatedPayload`.

**Why a dedicated `conversation_deleted` reply, not `conversation_updated`:** the
record no longer exists post-deletion, so an "updated record" payload
(name/cwd/last_used_at) has no source. The minimal ack carries only the id — the
one fact AC #2 requires ("identifies which conversation was deleted").

Add both constants to `v1TypeSet` (`envelope.go`).

### Handler (`internal/relay/handlers/delete_conversation.go` — new)

Consumer-defined minimal interface (mirrors `ConversationRenamer`):

```go
type ConversationDeleter interface {
    Delete(id conversations.ConversationID) bool
    Save(path string) error
}
```

`*conversations.Registry` satisfies it structurally — no adapter.

Constructor signature (mirrors `RenameConversation`):

```go
func DeleteConversation(reg ConversationDeleter, registryPath string, logger *slog.Logger) dispatch.Handler
```

Behavior (contract — the developer writes the body by cloning
`rename_conversation.go`):

1. Decode `env.Payload` into `DeleteConversationPayload`. On error → malformed
   reject (see Error handling).
2. `hit := reg.Delete(conversations.ConversationID(p.ConversationID))`. On miss
   (`!hit`) → not-found reject.
3. Eager best-effort `reg.Save(registryPath)`; on error, log and continue
   (durability is best-effort, the in-memory removal already happened — mirrors
   rename/create).
4. Marshal `ConversationDeletedPayload{ID: p.ConversationID}` and
   `return c.Reply(ctx, env, protocol.TypeConversationDeleted, payloadJSON)`.

There is no empty-field guard equivalent to rename's empty-name check: an empty
or malformed `conversation_id` simply misses `Delete` and returns
`conversation.not_found`, which is the correct AC #4 behavior. Do **not** add a
pre-validation branch that would echo a distinct error for a blank id.

### Wiring (`cmd/pyry/relay.go`)

Two additive lines, mirroring rename, using the same
`resolveConversationsRegistryPath(instanceName)` and `convReg`:

- v1 block (~line 177):
  `d.Register(protocol.TypeDeleteConversation, handlers.DeleteConversation(convReg, resolveConversationsRegistryPath(instanceName), logger))`
- v2 `Handlers` map (~line 364):
  `protocol.TypeDeleteConversation: handlers.DeleteConversation(convReg, resolveConversationsRegistryPath(instanceName), logger),`

### AC #3 is satisfied for free

`list_conversations` reads the live registry (`internal/relay/handlers/list_conversations.go`).
A hard-deleted row is gone from `r.conversations`, so it cannot appear in list
results. **No consumer-side change** — do not touch the list handler.

## Concurrency model

None new. `Registry.Delete` and `Registry.Save` each take the registry mutex
internally; the conversations registry is a single-writer (the daemon), so there
is **no reload-before-save** — the in-memory registry is authoritative, exactly
as create/rename treat it. The handler runs on the per-conn dispatch goroutine
and does no goroutine spawning, no channels, no shared state beyond the registry.

## Error handling

Two reject branches. **Both reply with a fixed static string — no attacker
payload bytes (the supplied id, a decode-error fragment) ever reach the wire.**
Define static message constants mirroring rename's `msg*` constants.

| Branch | Trigger | Wire reply | Log fields |
|---|---|---|---|
| malformed | `json.Unmarshal` fails | `CodeProtocolMalformed` + static `"malformed delete_conversation payload"`, non-retryable | event + `conn_id` **only** |
| not_found | `Delete` returns `false` | `CodeConversationNotFound` + static `"conversation not found"`, non-retryable | event + `conn_id` + `conversation_id` |
| success | `Delete` returns `true` | `TypeConversationDeleted` + `{id}` | event + `conn_id` + `conversation_id` |

**Two deliberate divergences from the rename template — keep them, do not
"fix" the handler back to match rename:**

1. **The malformed branch logs `conn_id` only — NOT the decode `err`.** Rename
   logs `"err", err` here; delete must not. Go's `json.Unmarshal` errors
   (`*json.SyntaxError`) can embed offending input bytes, and `env.Payload` is
   attacker-controlled — logging the error would leak payload bytes into the
   daemon log, which AC #5 forbids ("no ... decode-error string ... logged").
2. **The malformed branch logs no `conversation_id`.** On a decode failure the
   struct is at most partially populated, so `p.ConversationID` may hold raw
   attacker bytes with no guarantee of well-formedness. Log only `conn_id`.

The not_found and success branches log `conversation_id` as a **structured slog
field** (handler-escaped, non-secret opaque id), consistent with the reviewed
rename/create precedent. AC #5's log-constraint governs the malformed branch;
AC #4 (not_found) is silent on logging, so the precedent applies. The id is never
placed on the wire on either failure path.

**Idempotent-on-miss (AC #4):** because `Delete` mutates nothing on a miss and
the reply is the same static `conversation.not_found`, re-issuing
`delete_conversation` for an already-deleted id returns identical output with no
state change — no special handling required.

## Security review (`security-sensitive`)

See the [Security review pass](#security-review-pass) section below (this ticket
carries the `security-sensitive` label; the pass is mandatory and appears after
Open questions).

## Testing strategy

Three test surfaces, all cloning existing patterns.

### Protocol round-trip (`internal/protocol/conversations_write_test.go`)

- `DeleteConversationPayload` round-trips through an envelope: `Type ==
  TypeDeleteConversation`, `conversation_id` survives marshal→unmarshal.
- `ConversationDeletedPayload` round-trips: `Type == TypeConversationDeleted`,
  `id` survives.

Model on `TestRenameConversationPayload_RoundTrip` /
`TestConversationUpdatedPayload_RoundTrip`.

### Compat drift (`internal/protocol/compat_test.go`)

Add `TypeDeleteConversation, TypeConversationDeleted` to all three type lists
(the `allTypes`/`all` slices at lines 9, 99, 174) and bump `want := 17` → `19`.
The existing `TestTypeConstants_V1V2Partition` union check then re-passes because
both types are v1 members. No new test function needed.

### Handler (`internal/relay/handlers/delete_conversation_test.go` — new)

Clone `rename_conversation_test.go`'s harness (`NewTestConn`, `Load`+`Create`
seed on a temp path, `recv`). Scenarios (bullets, not code):

- **Success + persistence + reply shape.** Seed one conversation; send a
  `delete_conversation` naming its id. Assert: the reply is
  `conversation_deleted`, `in_reply_to` == request id, payload `id` == the
  deleted id; the registry no longer contains the row; **and a fresh
  `conversations.Load` of the same temp path returns a registry without the row**
  (proves the eager `Save` persisted the removal — AC #1 restart-survival).
- **List no longer shows it (AC #3).** After a successful delete, a
  `list_conversations` handler call against the same registry omits the deleted
  id. (Or assert via the registry's list accessor directly if lighter.)
- **not_found (AC #4).** Send a delete for an id not in the registry; assert the
  reply is an `error` frame with code `conversation.not_found`, the registry is
  unchanged, and the wire payload contains no echo of the supplied id.
- **Idempotent-on-miss (AC #4).** Delete a seeded id (success), then re-send the
  same id; assert the second reply is `conversation.not_found` and no state
  change.
- **Malformed (AC #5).** Send an envelope whose payload is not decodable into
  `DeleteConversationPayload` (e.g. a JSON array, or `conversation_id` as a
  number); assert the reply is `protocol.malformed` and the static message
  contains **no** bytes from the payload. If the harness can capture logs (slog
  test handler), assert the malformed log record carries no `err`/`conversation_id`
  field carrying payload bytes.

Run `go test -race ./...`, `go vet ./...`, `staticcheck ./...`.

## Scope note (sizing)

**Size: S — do not split.** The §1 primary red lines pass with margin: 2 new
files (`delete_conversation.go` + its test), ~365 total LOC (handler ~130
cloned + handler test ~150 + vocab ~30 + protocol tests ~40 + compat/relay edits
~15), 3 new exported types (`DeleteConversationPayload`,
`ConversationDeletedPayload`, `ConversationDeleter`), **zero** consumer
call-site cascade (purely additive; no existing symbol changes signature), 2
reject branches, and 5 ACs that are facets of one handler.

The §4 production-file self-check lands at exactly 5 `.go` files (codes.go,
conversations_write.go, envelope.go, delete_conversation.go, relay.go). That
count is a proxy for turn cost that mis-fires here: three of the five are
single-package protocol-vocab one-liners (the codebase spreads vocab across
`codes.go` consts / `conversations_write.go` payloads / `envelope.go`
`v1TypeSet`), an organization artifact, not real scope. The **direct
security-sensitive twin `rename_conversation` (#820)** shipped this identical
5-file shape as one S. Splitting into an unwired-vocab child + handler child
would cost more pipeline turns than a proven-S clone and contradict the twin.
Sized S.

## Open questions

- **Reply type name.** `conversation_deleted` is chosen (mirrors the
  `conversation_created`/`conversation_updated` family). If desktop's client
  already keys on a different name, reconcile at integration — the handler is a
  one-line change to the reply `Type` constant. No blocker.

---

## Security review pass

**Verdict:** PASS

The one MUST-FIX surfaced by the pass (malformed-branch logging leaking payload
bytes) is addressed inline in the **Error handling** section before this verdict
— the spec mandates the two logging divergences from the rename template. No
open MUST-FIX remains.

**Findings:**

- **[Trust boundaries]** No open MUST-FIX. Single boundary: `DeleteConversation`,
  one `json.Unmarshal` of the phone-supplied `DeleteConversationPayload`. Its one
  untrusted field `conversation_id` is used *only* as an exact-match registry key
  inside `Delete` (byte comparison; no path/argv/query construction — a miss is
  `conversation.not_found`, and a phone cannot mint or collide an id, only name
  one that already exists). Downstream holds parsed types. Within a server-id,
  paired devices are one trust domain (ADR 025 § Security model); any paired
  phone may delete any conversation, consistent with `list`/`create`/`promote`/
  `rename`. The tenant boundary is the server-id, enforced structurally at the
  Noise IK handshake (unpaired → 4401, never reaches `dispatchAppFrame`).
  **Delete-specific note:** unlike rename, deletion is *permanent and
  irrecoverable*. This is an accepted, intended property — archive/unarchive
  (#881) is the reversible path; a paired device is already trusted with
  destructive actions (it drives the live session). No elevated per-device gate
  is warranted beyond pairing.
- **[Over-deletion / wrong-target]** No finding. `Delete` removes the *first* row
  whose `ID == id` and returns; conversation ids are unique, high-entropy
  (`crypto/rand`, minted by `create`). An exact-match therefore removes exactly
  one specific, caller-named row — no wildcard, prefix, or injected match, no
  over-deletion, no collateral row.
- **[Tokens/secrets]** N/A — mints, stores, and compares no token or secret;
  mints no id (no `crypto/rand` surface).
- **[File operations]** No finding. No user input reaches a filesystem path:
  `registryPath` is server-derived (`resolveConversationsRegistryPath`) and
  `conversation_id` is a registry key, never a path component. Persistence reuses
  `Registry.Save` (atomic temp-file `0600` + `0700` dir + fsync + rename); no new
  stat-then-open, so no TOCTOU and no symlink surface.
- **[Subprocess]** N/A — executes no subprocess; passes no value to
  `exec.Command`. Strict attack-surface *reduction* versus `create_conversation`.
- **[Cryptographic primitives]** N/A — no crypto in the handler; AEAD framing is
  the unchanged transport layer's concern.
- **[Network, I/O & DoS]** No MUST-FIX. The handler reads no socket; inbound
  frame size is capped by the v2 transport decoder upstream, transitively
  bounding `conversation_id` length. No explicit application id-length cap —
  acceptable because delete *shrinks* state rather than accumulating (anti-
  amplifying), fans out to no one, and a miss is a bounded linear scan over a
  small per-user registry. Not a DoS amplifier. No HTTP/WS/TLS surface added.
- **[Error messages, logs, telemetry]** MUST-FIX addressed inline; the reason
  this ticket carries the label. (1) Every reject branch replies with a fixed
  static string — no payload bytes (the supplied id or a decode-error fragment)
  reach the wire (AC #5 wire half). (2) The malformed branch logs **`conn_id`
  only** — not the decode `err` (Go `*json.SyntaxError` can embed offending input
  bytes) and not `conversation_id` (a decode failure leaves the struct partially
  populated, so `p.ConversationID` may hold raw attacker bytes). This is the
  deliberate divergence from the rename template, mandated by AC #5's "no ...
  decode-error string ... logged." (3) not_found and success log
  `conversation_id` as a **structured slog field** (handler-escaped, non-secret
  opaque id) per the reviewed rename/create precedent; on success the id is a
  proven-real registry id (`Delete` returned hit), and on failure the id never
  touches the wire. Log injection via a structured field is mitigated by slog's
  handler escaping. No telemetry.
- **[Information disclosure / enumeration]** No finding. The not_found-vs-success
  distinction is an existence oracle, but a paired phone can already enumerate all
  conversations via `list_conversations`, so delete grants no new capability; ids
  are high-entropy, defeating blind enumeration.
- **[Concurrency]** No finding. The handler acquires no lock itself; `Delete` and
  `Save` each take `r.mu` internally. Delete-then-Save is two separate locked ops,
  but the conversations registry is single-writer-authoritative and `Save`
  snapshots under the lock, so any interleaving with a concurrent handler persists
  a consistent state reflecting every committed mutation — no lost update, no
  corruption (same posture as rename/create). Replay is harmless: a replayed
  delete for a live id removes it once; a second replay is `conversation.not_found`
  — idempotent in effect, so no nonce/idempotency key is needed (contrast the
  modal verbs). **Shutdown mid-op:** a kill after the in-memory `Delete` but
  before `Save` reloads the row from disk on restart (the conversation
  "reappears") — the accepted best-effort durability window shared by
  rename/create/sweep; AC #1's restart-survival is asserted only on the
  Save-succeeded success path.
- **[Threat model alignment]** Untrusted-phone-input, cross-tenant, and
  over-deletion threats are in scope and addressed above. **Live fan-out of the
  deletion to *other* connected clients is explicitly OUT OF SCOPE** (per the
  ticket, matching rename): the ack goes only to the requester; other clients see
  the conversation vanish on their next `list_conversations`. A live
  `conversation_deleted` broadcast is a separate future concern.

**Reviewer:** architect (self-review per `architect/security-review.md`)
**Date:** 2026-07-09
