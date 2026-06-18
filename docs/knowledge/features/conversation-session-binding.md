# Conversation → session binding

How each phone-created discussion gets its own dedicated, isolated claude session. Two halves tie `internal/conversations` (the `Conversation.CurrentSessionID` binding field) to `internal/sessions` (the `Pool` that mints and supervises sessions):

- **Create path (#677)** — `create_conversation` eagerly mints + binds a session, recording it on `CurrentSessionID`.
- **Routing path (#678)** — `send_message` resolves that bound session and delivers the inbound turn there instead of to the bootstrap.

Both land in the `internal/relay/handlers` package. Foundational + consumer slices of EPIC #672 ("per-conversation sessions"). See [ADR 025](../decisions/025-mobile-remote-head-interactive-session.md), [`docs/multi-session.md`](../../multi-session.md).

## What it does and why

Before #677 there was exactly one supervised claude — the bootstrap session — and `create_conversation` only wrote a registry row, leaving `CurrentSessionID` empty. Every discussion therefore shared the single bootstrap claude: a turn in one discussion could disturb another. #677 wires the create path onto the existing `sessions.Pool` so **each conversation mints and binds its own dedicated session**, recorded via the existing `Conversation.CurrentSessionID` field. #678 then makes `send_message` actually *route* to that bound session, so inbound turns are now isolated per discussion (until then, the binding existed but every turn still went to the bootstrap). See [§ Routing](#routing-send_message-consumes-the-binding).

## How it works

### Eager bind at create time

When the daemon handles a `create_conversation` frame, the handler mints a session **before** recording the registry row:

1. Decode payload, resolve `cwd` / `name` / `promoted` (server defaults for null fields).
2. `id, err := conversations.NewID()` — server-minted conversation id (crypto/rand UUIDv4).
3. **Mint the session:** `creator.Create(mintCtx, string(id))` where `mintCtx` is a 30s timeout context. `Pool.Create` mints a session UUID → registers + persists it in the sessions registry → supervises → activates (spawns claude). Returns the new `SessionID`.
4. `reg.Create(Conversation{ID, Name, Cwd, CurrentSessionID: sessionID, IsPromoted, LastUsedAt})` — the bound session id is populated on the row.
5. `reg.Save(registryPath)` — eager persist (the field round-trips through the registry's atomic Save/Load, so the binding survives a daemon restart).
6. Reply `conversation_created`. The wire reply is **unchanged** — it carries no session field; the binding is internal state surfaced only in the registry row.

The bind is **eager**, not the "first-message lazy bind" sketched in [`docs/multi-session.md`](../../multi-session.md). AC#1 forces it: the registry row must carry a non-empty `CurrentSessionID` referring to a pool session *immediately after the create frame is handled*. A lazy bind would leave the field empty until a later `send_message`. `Pool.Create` is reused as-is; no new "register-without-spawn" primitive was added.

> "Exists in the Pool" means the session has a registry entry + a `p.sessions` map entry. That holds even after idle-eviction later moves the process to disk — **evicted is a lifecycle state, not removal**. So the binding is durable even when the claude process is not currently running.

### The `SessionCreator` seam (keeps `handlers/` import-clean)

The handler depends on a narrow consumer-declared interface, mirroring the sibling `TurnWriter`:

```go
// internal/relay/handlers/create_conversation.go
type SessionCreator interface {
    Create(ctx context.Context, label string) (string, error)
}
```

`*sessions.Pool.Create` returns `(sessions.SessionID, error)`, not `(string, error)`, so it does **not** satisfy this directly. It is adapted at the `cmd/pyry` boundary — the only package that knows both `*sessions.Pool` and `handlers.SessionCreator` — by a thin wrapper mirroring the existing `poolResolver`:

```go
// cmd/pyry/main.go
type sessionMinter struct{ p *sessions.Pool }
func (m sessionMinter) Create(ctx context.Context, label string) (string, error) {
    id, err := m.p.Create(ctx, label)
    return string(id), err
}
```

`sessionMinter{pool}` is threaded through `startRelay` → `startRelayV2` and into both `handlers.CreateConversation(...)` registration sites (the v1 dispatcher and the v2 manager handler map). Result: `internal/relay/handlers` stays free of any `internal/sessions` import — the cycle-free property is preserved.

### `Cwd` is structurally excluded from the spawn path (AC#4)

The session spawns in the daemon's **already-trust-marked shared workdir** — `Pool.buildSession` uses `tpl.WorkDir` (= `cfg.Bootstrap.WorkDir`), and claude's argv gets only `--session-id <server-minted-uuid>`. The phone-influenced `conversation.Cwd` is **never** a spawn input: the handler passes only `(mintCtx, string(id))` to `creator.Create`, and the `SessionCreator` interface carries no cwd argument at all. `Cwd` remains inert stored metadata, echoed back only to the operator's own paired devices.

This is enforced *structurally*, not by validation — there is no path for `Cwd` to reach `exec.Command`. Giving each conversation its own *distinct* working directory (and the canonicalisation + boundary validation that requires) is the deferred per-conversation-workdir follow-up.

### Mint-failure and timeout behaviour

The mint is bounded by a 30s timeout (`createConversationMintTimeout`, matching `sendMessageActivateTimeout` and control's session-create budget). `Pool.Activate` blocks until claude's PTY is ready (~2–15s) or ctx-cancel; the bound turns a wedged spawn into a retryable error instead of pinning the per-conn goroutine.

Any mint error (pool not running, activate timeout, in-pool save failure, ctx deadline) fails the whole create: the handler logs at `Warn` (`create_conversation.session_mint_failed`, fields `conn_id` / `conversation_id` / wrapped `err`) and replies `protocol.CodeServerBinaryOffline` **retryable**, returning **before** `reg.Create` — so there is no half-bound orphan conversation row. The phone retries onto a fresh conversation + session.

## Routing: `send_message` consumes the binding

#678 is the consumer half. Where the create path *writes* `CurrentSessionID`, `send_message` *reads* it to select the session a turn is delivered to. Before #678 the handler held a single `TurnWriter` (the bootstrap session) and routed every turn there regardless of `ConversationID`; now it resolves the frame's conversation to its bound session and runs the existing Activate-before-write against *that* session.

### The `SessionRouter` seam (mirrors `SessionCreator`)

The handler depends on a second consumer-declared interface that *returns* the existing `TurnWriter`, so `handlers/` still imports no `internal/sessions`:

```go
// internal/relay/handlers/send_message.go
type SessionRouter interface {
    Route(conversationID string) (TurnWriter, error)
}
```

`Route` is **ctx-free** — a pure in-memory lookup (registry read + field check + pool lookup), non-blocking, no cancellation surface. The blocking work (`Activate`, `WriteUserTurn`) still happens in the returned writer under the handler's unchanged two 30s budgets.

The implementation lives at `cmd/pyry` (the only package importing both `conversations` and `sessions`), beside `sessionMinter`:

```go
// cmd/pyry/main.go
type sessionRouter struct {
    pool    *sessions.Pool
    convReg *conversations.Registry
}
func (r sessionRouter) Route(conversationID string) (handlers.TurnWriter, error) {
    conv, ok := r.convReg.Get(conversations.ConversationID(conversationID))
    if !ok {
        return nil, conversations.ErrConversationNotFound      // → conversation.not_found
    }
    if conv.CurrentSessionID == "" {
        return nil, errNoBoundSession                          // → server.binary_offline (before any Lookup!)
    }
    id := sessions.SessionID(conv.CurrentSessionID)
    sess, err := r.pool.Lookup(id)
    if err != nil {
        return nil, err                                        // ErrSessionNotFound → server.binary_offline
    }
    return boundSession{pool: r.pool, sess: sess, id: id}, nil
}
```

### Two load-bearing invariants

- **The empty-`CurrentSessionID` guard fires *before* any `Lookup`.** `Pool.Lookup("")` returns the **bootstrap** session. Without the up-front `== ""` rejection, an unbound conversation would silently route the phone's turn into the shared bootstrap claude — the confused-deputy / isolation break AC#4 forbids. Rejecting first maps the case to a retryable `server.binary_offline` instead. (The phone supplies only the `ConversationID` lookup key and the `Text`; the routing *target* is the server-stored `CurrentSessionID`, never phone-writable — so the phone can only address a conversation whose server-minted id it already holds, and can never point it at an arbitrary session.)
- **`boundSession.Activate` funnels through `Pool.Activate`, not `Session.Activate`.** `*sessions.Session` already satisfies `TurnWriter` directly; the `boundSession` wrapper exists *only* to redirect `Activate` through the cap-enforcing `Pool.Activate(ctx, id)`. The bootstrap was special (always active, never cap-evicted) so it could use `Session.Activate`; per-conversation sessions are full `ActiveCap` citizens — activating one may LRU-evict a peer, which only happens inside `Pool.Activate`. Bypassing it would break the invariant the idle-evict follow-up (#680) relies on. An idle-evicted bound session therefore re-activates on the next `send_message` (the [idle-eviction.md](idle-eviction.md) lazy-respawn contract, now per-conversation).

### Error mapping (no new wire code)

| Case | Detected in | Reply | Retryable |
|---|---|---|---|
| Unknown `ConversationID` | `Route`: `Registry.Get` miss | `conversation.not_found` | no |
| No bound session (`CurrentSessionID == ""`) | `Route`: empty-id guard | `server.binary_offline` | yes |
| Bound id not in pool (`ErrSessionNotFound`) | `Route`: `Pool.Lookup` | `server.binary_offline` | yes |
| Conversation deleted mid-flight (TOCTOU) | `WriteUserTurn` → bound session's `ValidateConversation` | `conversation.not_found` | no |

The unknown-conversation reject now fires at *routing* time rather than delivery time — net behaviour to the phone is identical, but it no longer spends an Activate budget on a doomed turn. The deliver-switch `ErrConversationNotFound` arm stays to defend the TOCTOU where the conversation is deleted between `Route` and delivery. `errNoBoundSession` is an unexported sentinel with no wire surface. The two-phase Activate→WriteUserTurn block is otherwise **byte-identical** to the pre-#678 handler (AC#5: only the session-selection step is new).

## Edge cases & limitations

- **Inbound routes per-conversation; the outbound half is migrating in stages (#678 → #687 → #679).** When #678 landed, claude's *replies* still fanned out from the bootstrap session — and worse, the structured interactive turn stream read its conversation cursor from the bootstrap supervisor, which #678 leaves empty for routed turns, so the structured reply stream went **silent** after the first per-conversation route. [#687](../codebase/687.md) closes the first half: it introduces a `cmd/pyry` *active-conversation* signal (`activeConversation`, stamped by `sessionRouter.Route` on success) and re-keys the structured stream's two cursor readers (live emitter + #647 reconnect-replay) to it, so the stream **emits again** and each envelope carries the routed conversation's `conversation_id` (attribution fixed). Still deferred: **which transcript the producer tails** (recency-resolved `resolveLatestSessionJSONL`) is #679 (`blocked-by` #687) — in the single-operator case the active bound session is also the most-recent writer, so attribution and content already agree. The **coarse** v1/v2 bridges (`assistant_turn.go` / `assistant_turn_v2.go`, the non-interactive surface) still read the bootstrap cursor and are unchanged. The real-claude e2e confirms the full phone→claude→phone round-trip is intact.
- **Accepted residue — unbound session on a non-empty-id error.** `Pool.Create` can return a non-empty id *with* an error (e.g. the mint persisted, then `Activate` timed out; the lifecycle goroutine may still bring the session up against the pool ctx after the handler's timeout fires). The handler treats *any* error as a clean mint failure and does not bind it, so such a session is left registered in the Pool with no conversation pointing at it. This is benign — the same shape as a session that ran and idled out, recoverable by the Pool's own lifecycle — and the race is unobserved, so per evidence-based fix selection no cleanup logic was added.
- **Process-exhaustion / spawn amplification (deferred).** Eager binding makes `create_conversation` a process-spawning operation; an authenticated phone spamming creates can exhaust host processes/memory. The existing in-architecture bound is `Pool.ActiveCap` (LRU-evicts a victim when the cap is hit) — but it **defaults to uncapped** (`-pyry-active-cap 0`). A dedicated per-operator create quota / rate-limit is new dispatch policy and is a named #672-family follow-up. Ops mitigation today: set `-pyry-active-cap` and/or `-pyry-idle-timeout`.
- **`ActiveCap` churn.** When `ActiveCap` *is* set, each `create_conversation` activation can LRU-evict another conversation's live claude. Acceptable: eviction preserves the on-disk JSONL and the session re-activates on the next `send_message`.
- **Restart scope.** Only the `CurrentSessionID` *field* round-trips on registry reload. Reviving / re-binding the live claude process across a daemon restart is the Pool's existing session-lifecycle / startup-reconciliation concern, out of scope here.

## Deferred to follow-ups (EPIC #672)

- **Distinct per-conversation working directory** + the trust/boundary validation that makes `conversation.Cwd` a safe spawn input. Until then, all conversation sessions share the trusted bootstrap workdir.
- **Per-operator create quota / rate-limit** (dispatch policy).
- **Per-conversation outbound routing.** The structured stream's attribution now follows the active conversation ([#687](../codebase/687.md)); making the reply **content** follow the bound session's transcript is #679 (`blocked-by` #687). The coarse v1/v2 bridges still fan out from the bootstrap cursor — re-keying them is a further #672-family follow-up.

## Related

- [conversations-package.md](conversations-package.md) — the `Conversation.CurrentSessionID` binding field (and `SessionHistory`).
- [conversations-registry.md](conversations-registry.md) — atomic Save/Load that round-trips the binding (AC#3).
- [sessions-package.md](sessions-package.md) — `Pool.Create` mint primitive (§ *Pool.Create*) and `buildSession` (the `tpl.WorkDir` / `--session-id`-only spawn point).
- [idle-eviction.md](idle-eviction.md) — "evicted is a state, not removal"; lazy respawn on next `send_message`, now per-conversation via `Pool.Activate`.
- [relay-package.md](relay-package.md) — the `create_conversation` / `send_message` handlers and the `SessionCreator` / `SessionRouter` seams alongside `TurnWriter`.
- [codebase/677.md](../codebase/677.md), [codebase/678.md](../codebase/678.md) — per-ticket implementation notes (create + routing halves).
- [ADR 025](../decisions/025-mobile-remote-head-interactive-session.md) — mobile remote-head interactive session.
