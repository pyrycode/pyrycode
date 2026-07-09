# Spec — #820 `rename_conversation` wire message + daemon handler

**Ticket:** [#820](https://github.com/pyrycode/pyrycode/issues/820) — feat(wire): rename_conversation v2 wire message
**Size:** S (security-sensitive)
**Author:** architect

## Context

Both paired clients (desktop, mobile) need to rename a conversation and neither
has it: mobile stubs rename locally because the daemon exposes no wire verb, and
desktop is building a Rename dialog against this message now. Following the
reference-client discipline (fix universal gaps in the shared daemon/wire, not in
one client), this ticket adds the missing verb.

The daemon already has every primitive a rename needs — this ticket wires the
last verb, cloning the `create_conversation` handler shape:

- **Reply type** `conversation_updated` / `protocol.ConversationUpdatedPayload`
  already exists — reuse verbatim, do not mint a new reply type.
- **Registry setter** `conversations.Registry.Update(id, fn) bool` already
  mutates any field under the lock and returns hit/miss — reuse for the mutation.
- **`list_conversations`** reads live registry state, so a renamed title surfaces
  there for free (AC #3 is satisfied by the existing reader — no new code).
- **Error codes** `conversation.not_found` and `protocol.malformed` already exist.

The relay/transport is content-blind and carries the frame unchanged — **no
transport work**. The deliverable is a protocol type + payload struct + a daemon
handler + its two dispatch registrations.

## Files to read first

- `internal/relay/handlers/create_conversation.go:115-232` — **the handler shape
  to clone**: decode → validate → mutate registry via a minimal consumed
  interface → eager `Save` → typed reply via `c.Reply`. Note lines 16-42 (the
  static per-branch message constants — never echo attacker bytes) and 64-67 (the
  `ConversationCreator` minimal-interface pattern). Your handler is *simpler* —
  no session minting, no cwd, no `SessionCreator`.
- `internal/relay/handlers/create_conversation_test.go:1-95` — **the test harness
  to reuse**: `dispatch.NewTestConn`, the `recv()` helper, the temp-dir registry
  (`conversations.Load` on a `t.TempDir()` path), and the envelope builder. Clone
  these helpers with `rename`-prefixed names.
- `internal/relay/handlers/register_push_token.go:120-138` — `replyError` and
  `replyAck` are **package-level helpers** in `handlers`; reuse `replyError` for
  the malformed / empty-title / not-found branches. Do not redefine them.
- `internal/conversations/registry.go:169-189` — `Registry.Update` contract: `fn`
  runs under `r.mu`; returns `false` (no `fn` call, no mutation) on miss; `fn`
  MUST NOT call back into the registry (non-reentrant mutex) or retain the
  `*Conversation` pointer past return.
- `internal/conversations/registry.go:247-299` — `Registry.Promote` is the
  **empty-name precedent**: `strings.TrimSpace(name) == ""` → refuse; on accept it
  stores the **raw** (untrimmed) name. Mirror this in the handler (see Design).
- `internal/protocol/conversations_write.go:35-58` — `PromoteConversationPayload`
  (payload precedent) and `ConversationUpdatedPayload` (the reply struct to reuse
  as-is; note `Name` is `*string`).
- `internal/protocol/codes.go:39-65` — the closed `Type*` const block; the
  "Conversations" cluster (50-56) is where `TypeRenameConversation` lands.
  `CodeConversationNotFound` (22) and `CodeProtocolMalformed` (10) already exist.
- `internal/protocol/envelope.go:116-135` — `v1TypeSet`, the closed enumeration of
  dispatch.Route application types. `TypeRenameConversation` is added here (see
  the v1/v2 classification decision in Design).
- `internal/protocol/compat_test.go:8-222` — three constant enumerations
  (`TestIsV1Compatible` L10-17, `TestV1TypeSet_CoversAllExportedTypeConstants`
  L98-108, `TestTypeConstants_V1V2Partition` L172-206) plus the **hardcoded count
  `16`** at L107-108 that becomes `17`. Adding the const forces all three edits or
  the partition test fails.
- `internal/protocol/conversations_write_test.go:91-136` — the round-trip test
  precedent (`TestPromoteConversationPayload_RoundTrip`); add an equivalent
  `TestRenameConversationPayload_RoundTrip`.
- `cmd/pyry/relay.go:173-176` (v1 legacy `d.Register` block) and `:322-327` (v2
  `Handlers` map passed to `NewV2SessionManager`) — **the two wiring sites**.
  `TypeCreateConversation` appears in both; add `TypeRenameConversation` to both,
  mirroring it exactly.
- `internal/relay/v2session.go:1569-1613` — `dispatchAppFrame`: it intercepts only
  v2 *control* types (the `switch`), then falls through to
  `dispatch.Route(Handlers, plaintext)`. Confirms `rename_conversation` reaches
  the handler purely via the `Handlers` map — nothing else in the v2 path needs
  touching.

## Design

### Package structure

No new packages. Edits land in three existing areas:

| File | Change | Kind |
|------|--------|------|
| `internal/protocol/codes.go` | `TypeRenameConversation = "rename_conversation"` const (in the Conversations cluster) | modify |
| `internal/protocol/conversations_write.go` | `RenameConversationPayload` struct | modify |
| `internal/protocol/envelope.go` | `TypeRenameConversation: true` in `v1TypeSet` | modify |
| `internal/relay/handlers/rename_conversation.go` | the handler + its minimal consumed interface + static message constants | **new** |
| `cmd/pyry/relay.go` | two `Register` lines (v1 block + v2 `Handlers` map) | modify |

Test files (`compat_test.go`, `conversations_write_test.go`,
`rename_conversation_test.go`) are additionally edited/created — see Testing.

### Wire vocabulary

Request payload (new, in `conversations_write.go`). Both fields spec-required; a
rename must name a target and a new title:

```go
type RenameConversationPayload struct {
    ConversationID string `json:"conversation_id"`
    Name           string `json:"name"`
}
```

- **Do not reuse `PromoteConversationPayload`** — it also carries a required
  `Cwd`, which a rename neither has nor means. A dedicated two-field struct is
  correct.
- Reply reuses `protocol.ConversationUpdatedPayload` + `protocol.TypeConversationUpdated`
  verbatim. `Name` is `*string`; the handler sets it to a non-nil pointer to the
  new title.

### v1/v2 classification decision (why `envelope.go` is touched)

`rename_conversation` is a **dispatch.Route request/reply verb**, dispatched
through the `Handlers` map exactly like `create_conversation`, `promote_conversation`,
and `list_conversations` — *not* a v2 control frame intercepted before
`dispatch.Route` (contrast `new_session`, `set_session_settings`, `interrupt`,
which live in `v2OnlyTypes`). Every dispatch.Route inbound verb in the codebase
is a member of `v1TypeSet`; `rename_conversation` joins them.

`compat_test.go`'s `TestTypeConstants_V1V2Partition` requires every exported
`Type*` constant to be in `v1TypeSet` XOR `v2OnlyTypes`. Classifying it as
`v2OnlyTypes` would be semantically wrong (that set is documented as
"control-intercepted-before-dispatch.Route OR outbound-only push"; rename is
neither) and would leave the v1 `d.Register` site dead. So: **`v1TypeSet`**. This
mirrors `create_conversation` precisely. (The ticket title's "v2 wire message"
refers to the encrypted v2 *transport* it rides — not the v1/v2 type partition,
which is about dispatch mechanism.)

### Handler

Signature (in the new `rename_conversation.go`):

```go
func RenameConversation(reg ConversationRenamer, registryPath string, logger *slog.Logger) dispatch.Handler
```

Minimal consumed interface (mirrors `ConversationCreator`; `*conversations.Registry`
satisfies it structurally, no adapter):

```go
type ConversationRenamer interface {
    Update(id conversations.ConversationID, fn func(*conversations.Conversation)) bool
    Save(path string) error
}
```

Behavior — the request flow is: decode → empty-title guard → `Update` (which
carries the not-found bool) → eager `Save` → reply. Contract, not code:

1. **Decode** `env.Payload` into `RenameConversationPayload`. On error: log the
   wrapped err (never the payload bytes), `replyError(CodeProtocolMalformed,
   msgRenameConversationMalformed, retryable=false)`. (AC #5's malformed sibling.)
2. **Empty-title guard** — `strings.TrimSpace(p.Name) == ""` → log, `replyError(
   CodeProtocolMalformed, msgRenameConversationEmptyName, retryable=false)`. This
   runs **before** `Update`, so the stored name is untouched (AC #5). Mirrors the
   `Registry.Promote` empty-name refusal. The stored value is the **raw**
   `p.Name` (untrimmed), matching Promote — trim is used only for the check.
3. **Mutate + capture** via a single `Update` call. The `fn` closure sets
   `c.Name = &title` and, still under the lock, snapshots the post-mutation record
   into a `ConversationUpdatedPayload` (capturing `ID`, `IsPromoted`, `Cwd`,
   `LastUsedAt`, and the new `Name`). Capturing inside `fn` avoids a
   find-then-read TOCTOU — no second locked read. Do not retain the
   `*conversations.Conversation`; copy scalar fields + point `Name` at a local
   `title` string (not into the slice).
4. **Not-found** — `Update` returns `false` (it never invoked `fn`; registry
   unmodified): `replyError(CodeConversationNotFound, msgRenameConversationNotFound,
   retryable=false)`. (AC #4.)
5. **Eager Save** — `reg.Save(registryPath)`; on error log at `Error` and
   continue (best-effort durability, non-fatal — identical to
   `create_conversation`'s persist step: the row is live in-memory and usable).
   (AC #2 durability.)
6. **Reply** — marshal the captured `ConversationUpdatedPayload`, `c.Reply(ctx,
   env, protocol.TypeConversationUpdated, payloadJSON)`. `Reply` stamps `id`, `ts`,
   and `in_reply_to` (AC #2 correlation).

**`LastUsedAt` is NOT bumped.** Rename is a metadata edit, not a "use"; mirroring
`Registry.Promote` (which touches only `IsPromoted` + `Name`) keeps
`list_conversations` ordering stable. The reply echoes the existing `LastUsedAt`.

Static message constants (package-level, like `create_conversation`'s
`msgCreate*`) — fixed strings, never interpolating payload bytes:

- `msgRenameConversationMalformed = "malformed rename_conversation payload"`
- `msgRenameConversationEmptyName = "conversation name must not be empty"`
- `msgRenameConversationNotFound = "conversation not found"`

### Wiring (both sites, `cmd/pyry/relay.go`)

Add, adjacent to the `TypeCreateConversation` registrations:

- v1 legacy block (~L176): `d.Register(protocol.TypeRenameConversation, handlers.RenameConversation(convReg, resolveConversationsRegistryPath(instanceName), logger))`
- v2 `Handlers` map (~L326): `protocol.TypeRenameConversation: handlers.RenameConversation(convReg, resolveConversationsRegistryPath(instanceName), logger),`

`resolveConversationsRegistryPath(instanceName)` is the same registry path
`create_conversation` passes — reuse it.

## Data flow

```
phone → (v2 AEAD frame) → relay (content-blind) → daemon
  → V2SessionManager.dispatchAppFrame: not a control type → dispatch.Route
  → Handlers["rename_conversation"] = RenameConversation
      → decode → trim-guard → Registry.Update(id, setName+snapshot) → Registry.Save
      → c.Reply(conversation_updated, in_reply_to=req.id) → (AEAD) → phone
Other clients: unchanged; they see the new title on their next list_conversations.
```

## Concurrency model

No new goroutines. The handler runs synchronously on the manager's single
dispatch goroutine (`dispatchAppFrame`'s caller). The only shared state is the
registry, mutated under `r.mu` inside `Update`; the name-set and the reply-snapshot
both happen inside that single locked `fn`, so there is no find-then-mutate window
and no second read to race. Daemon is the single registry writer — no
reload-before-save (per project convention: one writer, blind save).

## Error handling

| Condition | Code | Message (static) | Retryable | Registry |
|-----------|------|------------------|-----------|----------|
| Payload won't decode | `protocol.malformed` | `msgRenameConversationMalformed` | false | untouched |
| Empty/whitespace title | `protocol.malformed` | `msgRenameConversationEmptyName` | false | untouched |
| `conversation_id` miss | `conversation.not_found` | `msgRenameConversationNotFound` | false | untouched |
| `Save` fails | — (no error reply) | logged at `Error`; reply still sent | — | mutated in-memory |

All error branches log the wrapped/underlying detail server-side and reply with a
fixed string — no payload bytes (title, id, decode error) on the wire.

**Log hygiene (all branches, success and reject).** Structured log fields are
`event`, `conn_id`, and `conversation_id` (a non-secret registry key — logging it
mirrors `create_conversation`). The **title / `Name` is user content and MUST NOT
be logged** on any branch. The decode-error branch may log the wrapped `err` (it
is a JSON-shape error, not the raw bytes); it must not log `env.Payload`.

## Testing strategy

Two test files. Clone the `create_conversation_test.go` harness helpers
(`NewTestConn`, `recv()`, temp-dir registry, envelope builder) with `rename`
names.

**`internal/relay/handlers/rename_conversation_test.go`** — table-driven, one
`recv()` assertion per case:

- **Success** — seed a registry row (via `reg.Create` or a loaded fixture), send a
  valid rename; assert the reply is `conversation_updated`, `in_reply_to` matches
  the request id, `Name` is the new title, and other fields (`ID`, `Cwd`,
  `IsPromoted`, `LastUsedAt`) are unchanged. Assert the registry row's `Name` is
  updated and `LastUsedAt` is **unchanged** (compare via `time.Time.Equal`).
- **Persistence** — after a successful rename, `conversations.Load(path)` (a fresh
  read of the eager-Saved file) returns the row with the new title (AC #2 survives
  restart).
- **Surfacing** — after rename, `ListConversations` (or `reg.List()`) returns the
  row with its new title (AC #3). Can be asserted in the success case.
- **Not found** — unknown `conversation_id`; assert `error` reply with
  `conversation.not_found`, and the seeded row(s) are byte-unchanged.
- **Empty title** — `""` and `"   "` (whitespace-only); assert `error` reply with
  `protocol.malformed`, and the seeded row's `Name` is unchanged.
- **Malformed payload** — non-JSON / wrong-shape bytes; assert `error` reply with
  `protocol.malformed`.
- (Optional) **No-echo discipline** — a title/id crafted to look like an injected
  string does not appear in the error reply message on the reject paths.

**`internal/protocol/conversations_write_test.go`** — add
`TestRenameConversationPayload_RoundTrip` mirroring the Promote round-trip:
marshal into an envelope, assert `Type == TypeRenameConversation`, unmarshal,
assert both fields survive.

**`internal/protocol/compat_test.go`** — add `TypeRenameConversation` to the three
enumerations and bump the hardcoded `16 → 17` (L107); the partition test then
proves it is classified exactly once (in `v1TypeSet`).

Run `go test -race ./...` and `go vet ./...`.

## Open questions

- **Title length cap.** Neither `create_conversation` nor `promote` caps `Name`
  length today; no oversized-title failure has been observed. Deferred — do not
  add a cap on spec authority (evidence-based fix selection). If a cap is ever
  wanted it belongs on the registry primitive so every write verb inherits it, not
  in this one handler. Noted, not gated.

## Sizing note (transparency)

Honest production-source-file count: **5** — `codes.go`, `conversations_write.go`,
`envelope.go` (three one-line/one-struct protocol-vocabulary edits in a single
package), the new `rename_conversation.go` handler (~70 LOC), and the two-line
`relay.go` wiring. This sits exactly at the §4 self-check threshold, but is shipped
as one S rather than split, because:

1. **`create_conversation` is the identical-shape precedent** — const + payload +
   `v1TypeSet` + handler + two-site wiring — and shipped as one ticket.
2. **Real turn cost is ~half budget** (~20-30 turns, ~400 total LOC): three of the
   five files are trivial protocol-vocabulary one-liners; the handler is a
   simpler-than-`create_conversation` clone (no session mint, no cwd). This is the
   inverse of the undercount the gate guards against.
3. **PO ruled S with a ticket-specific decision** (reply/setter/not-found/
   list-surfacing all pre-exist → thin vertical, don't split); splitting would
   produce a ~15-LOC vocabulary child — over-splitting the PO already rejected —
   and delay the desktop Rename dialog behind a two-ticket chain.

The §1 primary red lines all pass (2 new files, ~400 LOC, 1 new exported type +
1 const, 3 reject branches, 5 ACs, no 10+ consumer cascade).

## Security review

**Verdict:** PASS

**Findings:**

- **[Trust boundaries]** No MUST-FIX. Single explicit boundary: the handler
  `RenameConversation`, one `json.Unmarshal` of the phone-supplied
  `RenameConversationPayload`. Both untrusted fields are contained: `conversation_id`
  is used *only* as an exact-match registry key inside `Update` (byte comparison,
  no path/argv/query construction — a miss is `conversation.not_found`, and a phone
  cannot mint or collide an id, only name one that already exists); `title` is
  stored as an opaque display string and echoed only to the requester. Downstream
  holds parsed types. Within a server-id, paired devices are one trust domain (ADR
  025 § Security model) — any paired phone may rename any conversation, consistent
  with `list_conversations` / `create` / `promote`; the tenant boundary is the
  server-id, enforced structurally at the Noise IK handshake (unpaired → 4401,
  never reaches `dispatchAppFrame`).
- **[Tokens/secrets]** N/A — the verb mints, stores, and compares no token or
  secret. Unlike `create_conversation`, it mints no id (no `crypto/rand` surface).
- **[File operations]** No findings. No user input reaches a filesystem path:
  `registryPath` is server-derived (`resolveConversationsRegistryPath`), and
  `conversation_id` is a registry key, never a path component. Persistence reuses
  `conversations.Registry.Save` (atomic temp-file `0600` + `0700` dir + fsync +
  rename); no new stat-then-open, so no TOCTOU. No symlink surface.
- **[Subprocess]** N/A — the handler executes no subprocess and passes no value to
  `exec.Command`. This is a strict attack-surface *reduction* versus
  `create_conversation` (which mints a claude session with a phone-influenced cwd);
  rename touches only registry state.
- **[Cryptographic primitives]** N/A — no crypto in the handler; AEAD framing is
  the unchanged transport layer's concern.
- **[Network & I/O]** No MUST-FIX. The handler performs no socket read; inbound
  frame size is already capped by the v2 transport's frame decoder upstream, which
  bounds the title length transitively. Title length has no *explicit*
  application cap — SHOULD/deferred (see Open questions): an oversized title is
  bounded by the frame cap, `Update` *replaces* rather than appends (no
  accumulation), and it fans out to no one, so it is not a DoS amplifier. No
  HTTP/WS/TLS surface added.
- **[Error messages, logs, telemetry]** No MUST-FIX (SHOULD-FIX addressed inline).
  All three reject branches reply with fixed static strings — no payload bytes
  (title, id, or decode error) on the wire, per the `create_conversation`
  static-message discipline. The initial spec did not forbid logging the title
  server-side; the Error-handling § now mandates log fields `event` / `conn_id` /
  `conversation_id` only, with **title MUST-NOT-log**. No telemetry.
- **[Concurrency]** No findings. Single lock (`r.mu` via `Update`); the id-match,
  the `Name` set, and the reply snapshot all happen inside one locked `fn`, so
  there is no find-then-mutate or find-then-read window and no second lock
  acquisition (no ordering concern). No goroutines spawned. Shutdown mid-op: `Save`
  is atomic (rename is the commit); a kill after the in-memory set but before
  `Save` reverts the row to its prior name on restart — the same best-effort
  durability window as `create_conversation` and the sweep loop, matching AC #2's
  eager-Save intent. A replayed `rename_conversation` re-sets the identical title
  (`Update` is idempotent) — harmless, so no nonce/idempotency key is needed
  (contrast the modal verbs).
- **[Threat model alignment]** Untrusted-phone-input and cross-tenant threats are
  in scope and addressed above. **Live fan-out of the rename to *other* connected
  clients is explicitly OUT OF SCOPE** (per the ticket): the reply goes only to the
  requester; other clients inherit the new title on their next `list_conversations`.
  A live broadcast is a separate concern for a future ticket.

**Reviewer:** architect (self-review per `architect/security-review.md`)
**Date:** 2026-07-08
