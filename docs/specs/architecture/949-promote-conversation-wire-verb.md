# Spec #949 — Wire `promote_conversation` handler (save-as-channel)

**Ticket:** [#949](https://github.com/pyrycode/pyrycode/issues/949) — `promote_conversation` has no registered handler: save-as-channel fails on the wire
**Size:** S (confirmed) · **Label:** `security-sensitive` (review pass appended, § Security review)

## Context

Both remote clients (mobile pyrycode-mobile#348, desktop) send `promote_conversation`
to convert a scratch conversation into a named channel. The daemon defines the entire
surface — the `promote_conversation` message type, `PromoteConversationPayload`, the
reused `ConversationUpdatedPayload` reply, and `Registry.Promote(id, name)` — but never
registers a dispatch handler for it. Since #913 removed the legacy v1 `d.Register` block
and the `PYRY_MOBILE_V2` switch, `V2SessionConfig.Handlers` in `cmd/pyry/relay.go` is the
**sole** dispatch table, and `promote` is the only conversation-write verb missing from it
(`rename`/`delete`/`archive`/`unarchive`/`change_workspace` are all present).

`promote_conversation` is a `v1TypeSet` member, so the frame is a recognised type — but
with no handler it falls through `internal/dispatch`'s no-handler arm to a
`protocol.unsupported` reply, and save-as-channel fails on the wire for both clients. No
test catches it: client e2e runs against a fake daemon that answers anything, and the
daemon suites never send this verb. This is the client↔daemon contract-gap class the
2026-07-08 real-stack-coverage policy targets — hence the mandated fake-claude
`relay_v2_*` e2e (AC #4).

**This is pure wiring plus a test.** All primitives pre-exist. The one design decision the
ticket demands — what to do with the payload's required `Cwd`, which `Registry.Promote`
does not consume — is resolved in § The Cwd decision below, and drives the
`security-sensitive` verdict.

## Files to read first

The developer's turn-1 data load. Read these before writing anything.

- `cmd/pyry/relay.go:404-499` — the `V2SessionConfig.Handlers` map literal. **The one
  production edit lands here**: a `protocol.TypePromoteConversation:` entry alongside the
  neighbouring conversation-write verbs (`TypeRenameConversation` line 407 is the closest
  shape twin). Note each entry's constructor-argument pattern.
- `internal/relay/handlers/rename_conversation.go` (whole file, ~130 lines) — **the
  primary clone target.** Same reply type (`conversation_updated`), same
  `reg`/`registryPath`/`logger` constructor shape, same empty-name guard, same "log the id
  not the user-content name" discipline. The promote handler is this file with
  `Registry.Promote` substituted for the inline `Update` closure and one extra read-back.
- `internal/relay/handlers/change_workspace.go` (whole file) — the `security-sensitive`
  sibling. **Read it for what promote must NOT do**: change_workspace consumes an untrusted
  `Cwd` and must confine it; promote deliberately does not (§ The Cwd decision). Its
  malformed-branch logging discipline (conn_id only, `change_workspace.go:121`) is the one
  promote adopts over rename's (rename logs the decode err).
- `internal/conversations/registry.go:296-346` — `Registry.Promote(id, name) error`
  contract and its four error sentinels (`ErrPromotionNameEmpty`,
  `ErrConversationNotFound`, `ErrConversationAlreadyPromoted`, `ErrPromotionNameInUse`).
  **Every one must be mapped** (§ Error handling). Note: Promote locks internally and
  returns only `error` — it does NOT return the updated record.
- `internal/conversations/registry.go:142` — `Registry.Get(id) (Conversation, bool)`. The
  read-back the handler uses to build the reply after a successful Promote (Promote does
  not return the row). Returns the `Conversation` **by value** (race-safe snapshot).
- `internal/protocol/conversations_write.go:35-44` — `PromoteConversationPayload`
  (`conversation_id`, `name`, `cwd`, all value strings, all spec-required). **Do not modify
  this struct** — the `Cwd` field stays on the wire (client symmetry); the daemon simply
  does not consume it.
- `internal/protocol/conversations_write.go:112-131` — `ConversationUpdatedPayload`, the
  reused reply (same one rename/archive/change_workspace return).
- `internal/protocol/codes.go` — `CodeConversationNotFound`,
  `CodeConversationAlreadyPromoted`, `CodeProtocolMalformed` (the three wire codes this
  handler emits); `TypePromoteConversation` / `TypeConversationUpdated`.
- `internal/relay/handlers/register_push_token.go:128` — `replyError(ctx, c, env, code,
  message, retryable)` helper (shared across handlers; the reject-branch primitive).
- `internal/e2e/relay_v2_daemon_test.go` (whole file) — **the e2e clone target.**
  `testV2DaemonListConversationsRoundTrip` (line 89) is the exact scaffold: pair a device,
  seed `conversations.json`, `StartInWithEnv(... "PYRY_MOBILE_V2=1")`, drive the Noise
  handshake via `driveHandshakeToOpenDaemon`, seal a request with `initSend.Encrypt`, read
  the reply with `readInnerFrame` + `initRecv.Decrypt`. `decryptInnerEnvelope` (line 378)
  and the foreign-id `conversation.not_found` assertion in
  `testV2DaemonRequestSnapshotRoundTrip` (line 292) are the error-branch pattern.
- `docs/specs/architecture/823-change-workspace-wire-verb.md` § Security review — the
  format and depth the appended review section mirrors (promote's is simpler: no
  file-operation surface).

## The Cwd decision (the architect's mandated call)

`PromoteConversationPayload` carries a **required `Cwd`** that `Registry.Promote(id, name)`
does not consume. The ticket demands this be decided explicitly, not dropped silently. Two
options, both defensible:

- **(A) Promotion also sets the workspace.** Then `Cwd` is an untrusted, network-supplied
  filesystem path that MUST be `$HOME`-confined per the `change_workspace` precedent before
  storage. This is the surface the `security-sensitive` label anticipates.
- **(B) The channel inherits the scratch conversation's existing `Cwd`; the payload `Cwd`
  is not consumed as a workdir.**

**Decision: (B).** Rationale:

1. **Semantics.** Promotion ("Save as channel") is a naming/persistence operation, not a
   relocation. The working directory where the work is happening does not change when a
   scratch conversation becomes a channel. The workspace stays put; only `IsPromoted` and
   `Name` change — exactly what `Registry.Promote` already does.
2. **There is always a valid Cwd to inherit.** Every conversation carries a `Cwd` set at
   `create_conversation` time — either the phone's `$HOME`-confined requested cwd or
   `defaultCwd` (`create_conversation.go:126-133`, `conversation.go:40-46`). A scratch
   conversation is never cwd-less, so inheritance is always well-defined.
3. **It avoids introducing a new untrusted-path-as-workdir surface.** Option A would
   require (a) injecting a `WorkspaceResolver` and confining the untrusted path, AND (b)
   either extending `Registry.Promote`'s signature to set cwd (a registry API change,
   rippling to its tests) or a two-step promote-then-update-cwd (two locks, two saves, a
   TOCTOU window). Option B needs none of it — `Registry.Promote` is used verbatim, as
   AC #1 mandates.
4. **The reply is still truthful.** The reply's `Cwd` reflects the existing stored
   (already-confined) value, read back via `Registry.Get`, so the client sees the channel's
   real workspace.

**Consequence for the security label:** by choosing (B), no untrusted client path reaches
storage or a spawn workdir, so this verb introduces **no new untrusted-path surface** — it
is, by design, in the same trust class as `rename` (stores a display string) rather than
`change_workspace` (stores a path). The `security-sensitive` label is honoured by the
mandatory review pass (§ Security review), whose central finding is precisely that the
label-earning surface was *deliberately not built*. The payload `Cwd` field remains on the
wire for client symmetry / forward-compat but is **neither validated nor stored** — a
future editor must not "wire it up" without re-opening this decision and the security
review.

## Design

### Package structure

One new file, one edited line. No new package, no new exported type beyond the handler
constructor.

```
internal/relay/handlers/promote_conversation.go   (new)  — PromoteConversation handler
internal/relay/handlers/promote_conversation_test.go (new) — table-driven unit test
internal/e2e/relay_v2_promote_test.go              (new)  — fake-claude round-trip (tag e2e)
cmd/pyry/relay.go                                  (edit) — +1 entry in Handlers map
```

### Handler contract

Clone `RenameConversation`. Constructor signature (same shape as rename — no resolver, no
extra injection, because Option B consumes no path):

```
func PromoteConversation(reg ConversationPromoter, registryPath string, logger *slog.Logger) dispatch.Handler
```

Consumed registry surface — a new minimal consumer-side interface in this file (the
established "define the interface where it's consumed" pattern; `*conversations.Registry`
satisfies it structurally, no adapter):

```
type ConversationPromoter interface {
    Promote(id conversations.ConversationID, name string) error
    Get(id conversations.ConversationID) (conversations.Conversation, bool)
    Save(path string) error
}
```

Handler body behaviour (each numbered step is a few lines; do NOT expand into a
control-flow essay — follow rename's shape):

1. `json.Unmarshal` the `PromoteConversationPayload`. On failure → `replyError(...
   CodeProtocolMalformed, msgPromoteConversationMalformed, false)`. **Log conn_id only**
   (adopt change_workspace's malformed discipline, not rename's — this is a
   `security-sensitive` verb; a decode failure may leave `p.ConversationID` holding raw
   attacker bytes, so neither the err nor the id is logged here).
2. Call `reg.Promote(conversations.ConversationID(p.ConversationID), p.Name)`. Map its
   error per the table in § Error handling. (No pre-Promote empty-name guard needed —
   `Registry.Promote` itself returns `ErrPromotionNameEmpty` for a blank name, and the
   mapping routes it to `malformed`. Do not duplicate the guard; let the primitive own it.)
3. On `Promote == nil`: `reg.Save(registryPath)` — eager best-effort persist so the
   promotion survives a daemon restart; a Save error is logged (`Error`, names the registry
   path — safe) and is **non-fatal**, exactly as rename/change_workspace treat their Save.
4. Read the promoted row back: `got, ok := reg.Get(conversations.ConversationID(p.ConversationID))`.
   On the (racy) `!ok` — a concurrent delete between Promote and Get — reply
   `conversation.not_found` (truthful current state; see § Concurrency). On `ok`, project
   `got` into `ConversationUpdatedPayload{ID, IsPromoted, IsArchived, Name, Cwd, LastUsedAt}`
   and `c.Reply(ctx, env, protocol.TypeConversationUpdated, payloadJSON)`.

`Registry.Get` returns the `Conversation` by value and `got.Name` points at the immutable
heap string `Promote` stored, so the reply snapshot is race-consistent with no extra
copying (same property rename's under-lock snapshot gives).

### Registration (the one production edit)

Add to the `Handlers` map in `startRelayV2` (`cmd/pyry/relay.go`), alongside
`TypeRenameConversation`:

```
protocol.TypePromoteConversation: handlers.PromoteConversation(w.convReg, resolveConversationsRegistryPath(w.instanceName), logger),
```

Same three arguments rename receives. `w.convReg` (`*conversations.Registry`) already
satisfies `ConversationPromoter`.

### Data flow

```
phone → noise_msg(promote_conversation) → V2SessionManager.dispatchAppFrame
      → dispatch.Route → Handlers[promote_conversation]
      → PromoteConversation:
            Registry.Promote(id, name)   [sets IsPromoted=true, Name]
            Registry.Save(path)          [best-effort durability]
            Registry.Get(id)             [read-back for reply]
      → c.Reply(conversation_updated)    [correlated via in_reply_to]
      → noise_msg(conversation_updated) → phone
```

## Concurrency model

No new goroutines. The handler runs on the existing per-conn goroutine. `*conversations.Registry`
is the single writer; `Promote`, `Get`, and `Save` each take `r.mu` (or `saveMu→mu` for
Save) independently.

- **Promote→Get is not atomic** (two lock acquisitions). Between them a *concurrent* conn
  could mutate the same row. All outcomes are benign: a concurrent rename/archive/
  change_workspace → the read-back reflects that newer state (truthful); a concurrent
  delete → `Get` misses → reply `conversation.not_found` (truthful current state — the row
  is gone). Deletion racing promotion of the *same* conversation is pathological client
  behaviour; the degenerate reply is documented and acceptable. This is strictly less TOCTOU
  exposure than change_workspace's file-path window (which promote does not have at all).
- **Replay is harmless.** A replayed `promote_conversation` on an already-promoted row
  returns `ErrConversationAlreadyPromoted` → `conversation.already_promoted` — no nonce or
  idempotency key needed (contrast the modal verbs), same posture as rename/change_workspace.
- **Shutdown mid-op.** A kill after the in-memory Promote but before Save reverts the row
  to unpromoted on restart — the accepted best-effort durability window shared by the whole
  conversation-write family. AC-level restart survival is asserted only on the Save-succeeded
  path.

## Error handling

Every `Registry.Promote` sentinel is mapped; no error falls through to `unsupported`.

| Condition | Reply type | Code | Static message | Retryable |
|---|---|---|---|---|
| `Promote` returns `nil` | `conversation_updated` | — | (record) | — |
| JSON decode fails (pre-Promote) | `error` | `protocol.malformed` | `malformed promote_conversation payload` | false |
| `ErrPromotionNameEmpty` | `error` | `protocol.malformed` | `channel name must not be empty` | false |
| `ErrConversationNotFound` | `error` | `conversation.not_found` | `conversation not found` | false |
| `ErrConversationAlreadyPromoted` | `error` | `conversation.already_promoted` | `conversation already promoted` | false |
| `ErrPromotionNameInUse` | `error` | `protocol.malformed` | `channel name already in use` | false |
| `Get` miss after successful Promote (racy delete) | `error` | `conversation.not_found` | `conversation not found` | false |

Notes:
- `ErrPromotionNameEmpty` and `ErrPromotionNameInUse` are client-fixable input errors →
  `protocol.malformed`, mirroring rename's empty-name → malformed. **No new wire code is
  added** — the AC names only `conversation.not_found` / `conversation.already_promoted` for
  the two error cases it cares about, and those get their dedicated codes; the two
  name-validation failures fold into `malformed` (the existing bucket for
  non-retryable-with-same-input client input).
- Map with `errors.Is` per PROJECT-MEMORY's refusal-to-wire-code convention (the
  `internal/conversations` sentinel travels verbatim; the handler does the dotted-string
  mapping at the call site).
- All reject messages are **fixed static string constants** — no supplied bytes (name, id,
  decode-error fragment) reach the wire (mirrors the family; the AC-5-equivalent wire
  hygiene). The channel `Name` is user content and is **never logged** (rename precedent);
  only the decode-success `conversation_id` is logged as a structured slog field on the
  non-malformed branches.

## Testing strategy

### Unit test — `promote_conversation_test.go` (same-package, table-driven)

Clone `rename_conversation_test.go`'s harness (a real `*conversations.Registry` seeded with
rows, a `dispatch.Conn` test double capturing the reply). Scenarios (describe inputs +
expected reply; write the assertions in the project idiom, do not pre-write bodies here):

- **Happy path:** seed one scratch row (`IsPromoted:false`, a known `Cwd`). Promote with a
  fresh name → reply `conversation_updated` with `IsPromoted:true`, `Name` = requested,
  `Cwd` = **the seeded cwd** (proves inheritance / Option B), correlated `in_reply_to`.
  Registry row now `IsPromoted:true`.
- **Cwd ignored:** promote sending a payload `Cwd` **different from** the seeded row's cwd →
  reply and stored `Cwd` are the **seeded** value, not the payload value (locks Option B; a
  regression to Option A fails here).
- **Unknown id** → `error` / `conversation.not_found`; no row mutated.
- **Already promoted** (seed `IsPromoted:true`) → `error` / `conversation.already_promoted`.
- **Empty / whitespace name** → `error` / `protocol.malformed`; row unchanged.
- **Name in use** (seed a second *promoted* row with name "X"; promote a scratch row also to
  "X") → `error` / `protocol.malformed`; scratch row still unpromoted.
- **Malformed payload** (non-JSON bytes) → `error` / `protocol.malformed`.
- **Durability:** happy path calls `Save`; reload the registry from the path and assert the
  promoted state persisted.
- **Reject hygiene:** every error reply's `Message` is the static constant; assert no reply
  or log carries the requested name.

### E2E — `relay_v2_promote_test.go` (`//go:build e2e`, in `internal/e2e/`)

Clone `testV2DaemonListConversationsRoundTrip`. One test proving the round-trip over the
encrypted wire (AC #4):

- Pair a device; seed `conversations.json` with one scratch row
  (`"is_promoted":false`, a `cwd`, a fixed UUID); `StartInWithEnv(... "PYRY_MOBILE_V2=1")`;
  drive the Noise handshake to open via `driveHandshakeToOpenDaemon`.
- Seal and send `promote_conversation{conversation_id, name:"my-channel", cwd:<seeded cwd>}`.
- Assert the decrypted reply is `conversation_updated`, `in_reply_to` = request id,
  `IsPromoted:true`, `Name:"my-channel"`, `Cwd` = seeded cwd.
- Assert **registry state**: read `conversations.json` back off disk (the daemon eager-Saves)
  and confirm `is_promoted:true` + the name — OR re-round-trip `list_conversations` and read
  the promoted summary (the daemon-truth read the harness already supports). Prefer the
  on-disk read: it directly proves persistence and matches the seed-and-inspect pattern the
  daemon suite already uses.
- **RED on main / GREEN after:** on `main` (no handler), the daemon replies `error` /
  `protocol.unsupported`, so the `want conversation_updated` assertion fails — RED. After the
  registration + handler, GREEN. The developer verifies RED per the e2e-tagged-run
  convention (run the test against `main` with the fix reverted, or reason from the missing
  handler → unsupported arm). Run with `go test -tags e2e ./internal/e2e/ -run RelayV2.*Promote`
  and confirm non-vacuous.

### Gate

`make check` green (`go vet`, `staticcheck`, `go test -race`, and the e2e leg wired into the
gate per #919/#952).

## Open questions

- **None blocking.** The single design fork (Cwd) is resolved above. If a future ticket
  wants "Save as channel *into a different folder*", that is Option A and a **separate**
  security-sensitive ticket (promote + a change_workspace-style confined path set), not a
  silent extension of this handler — the decision above is the seam that keeps them separate.

---

## Security review

**Verdict:** PASS

This ticket carries the `security-sensitive` label, so this adversarial self-review over the
spec is mandatory (`architect/security-review.md`). The label anticipates Option A — a verb
that stores an untrusted, network-supplied filesystem path as a future spawn workdir (the
`change_workspace` surface, #823). **The central finding of this review is that the design
(§ The Cwd decision) chose Option B and therefore does not build that surface at all.** The
label is honoured by walking the categories and pinning where each untrusted field is
contained, not by waving the review off as "too small" — the label is the gate, not my
judgement of the size.

**Findings (walked category by category):**

- **[Trust boundaries]** No MUST-FIX. Single boundary: `PromoteConversation`, one
  `json.Unmarshal` of the phone-supplied `PromoteConversationPayload`. Its three untrusted
  fields are each contained at a named point:
  - `conversation_id` — used **only** as an exact-match registry key inside `Registry.Promote`
    / `Registry.Get` (byte comparison, no path/argv/query construction). A miss is
    `conversation.not_found`; a phone can only name an id that already exists, never mint or
    collide one.
  - `name` — passed to `Registry.Promote`, stored as an opaque display string, echoed only
    to the requester. `Registry.Promote` itself enforces non-empty and cross-row
    name-uniqueness among promoted rows; both refusals map to `protocol.malformed`.
  - `cwd` — **the label-anticipated field — is not consumed.** It reaches no filesystem
    operation, no `Registry` write, no spawn argument. It is read off the wire into the
    payload struct and discarded. The stored `Cwd` is the row's pre-existing value, set and
    `$HOME`-confined at `create_conversation` time (`create_conversation.go` →
    `resolveSpawnDir`). Within a server-id, paired devices are one trust domain (ADR 025 §
    Security model); the tenant boundary is the server-id, enforced structurally at the
    Noise IK handshake (unpaired → 4401, never reaches `dispatchAppFrame`).

- **[File operations]** N/A — **and this is the crux of the review.** Unlike
  `change_workspace`, this verb performs no path handling: no `filepath.Abs`, no
  `EvalSymlinks`, no confine, no `MkdirAll`, no `trustMark`. The untrusted `cwd` is not
  stored, so there is no path-traversal, symlink-escape, empty-path-footgun, tilde, or
  store→next-spawn TOCTOU surface to close — because none is opened. The only persistence is
  `conversations.Registry.Save` (unchanged atomic temp-`0600` + dir-`0700` + fsync + rename),
  writing two in-memory-set fields (`IsPromoted`, `Name`) plus the untouched existing `Cwd`.
  **Regression watch:** if a future edit consumes `p.Cwd` (stores it, or forwards it to a
  spawn), this N/A becomes an *active* untrusted-path surface and this review is void — that
  is the explicit reason § The Cwd decision forbids wiring the field without re-opening the
  review. The "Cwd ignored" unit test is the deterministic guard against that regression.

- **[Tokens/secrets]** N/A — the verb mints, stores, and compares no token or secret; mints
  no id (no `crypto/rand` surface — the conversation id is phone-named and only ever
  compared, never generated here).

- **[Subprocess / external command execution]** N/A — the handler spawns no process and
  passes no value to `exec.Command`. Deliberately (and unlike `create_conversation`) it does
  **not** mint or respawn a claude session: promotion is a metadata flip. No path this verb
  touches reaches a subprocess argument.

- **[Cryptographic primitives]** N/A — no crypto in the handler; AEAD framing is the
  unchanged v2 transport's concern (the frame is already decrypted by the time
  `dispatchAppFrame` routes it).

- **[Network, I/O & DoS]** No MUST-FIX. The handler reads no socket; inbound frame size is
  capped by the v2 transport decoder upstream, transitively bounding all three string
  fields. `Registry.Promote`'s name-uniqueness scan and `Registry.Get`'s lookup are bounded
  linear scans over a small per-user registry (anti-amplifying — promote replaces two fields
  on one row, accumulates nothing, and fans out to no one: the reply goes only to the
  requester). No HTTP/WS/TLS surface added.

- **[Error messages, logs, telemetry]** No MUST-FIX. (1) All reject branches reply with
  fixed static string constants — no supplied bytes (name, id, decode fragment) reach the
  wire. (2) Logging discipline matches the security-sensitive twins, not rename: the
  malformed branch logs **`conn_id` only** — not the decode `err` (Go's `json.Unmarshal`
  errors can embed offending input bytes) and not `conversation_id` (a decode failure may
  leave it holding raw attacker bytes). The decode-success branches (not_found,
  already_promoted, name-in-use, success) log `conversation_id` as a proven-decoded,
  slog-escaped, non-secret structured field. The channel **`name` is user content and is
  never logged** on any branch (rename precedent). The Save-failure branch additionally logs
  the save `err`, which names the *registry* path (a filesystem error), not attacker bytes.
  No telemetry.

- **[Concurrency]** No finding beyond § Concurrency model. No new goroutines. The
  Promote→Get non-atomicity yields only truthful-current-state replies (a racy concurrent
  delete → `conversation.not_found`); no find-then-mutate corruption, since each registry op
  is internally locked and `Get` returns a value snapshot. Replay is idempotent-safe (a
  re-promote of an already-promoted row is refused `already_promoted`), so no nonce is
  required.

- **[Threat model alignment]** Untrusted-phone-input and cross-tenant threats are in scope
  and addressed above. Two concerns are explicitly OUT OF SCOPE, each named with who picks it
  up: (1) **promoting into a client-chosen new folder** — that is Option A, a separate
  security-sensitive ticket combining promote with a `change_workspace`-style confined path
  set; the design deliberately keeps it separate. (2) **live fan-out to other connected
  clients** — the reply goes only to the requester, matching the rest of the
  conversation-write family; other clients inherit the promoted state on their next
  `list_conversations`.

**Reviewer:** architect (self-review per `architect/security-review.md`)
**Date:** 2026-07-14
