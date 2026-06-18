# Conversation → session binding

How each phone-created discussion gets its own dedicated, isolated claude session. This is the create-path wiring that ties `internal/conversations` (the `Conversation.CurrentSessionID` binding field) to `internal/sessions` (the `Pool` that mints and supervises sessions), landed by the `create_conversation` handler in `internal/relay/handlers`.

Foundational slice: #677 (EPIC #672, "per-conversation sessions"). See [ADR 025](../decisions/025-mobile-remote-head-interactive-session.md), [`docs/multi-session.md`](../../multi-session.md).

## What it does and why

Before #677 there was exactly one supervised claude — the bootstrap session — and `create_conversation` only wrote a registry row, leaving `CurrentSessionID` empty. Every discussion therefore shared the single bootstrap claude: a turn in one discussion could disturb another. #677 wires the create path onto the existing `sessions.Pool` so **each conversation mints and binds its own dedicated session**, recorded via the existing `Conversation.CurrentSessionID` field. Discussions are now isolated.

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

## Edge cases & limitations

- **Accepted residue — unbound session on a non-empty-id error.** `Pool.Create` can return a non-empty id *with* an error (e.g. the mint persisted, then `Activate` timed out; the lifecycle goroutine may still bring the session up against the pool ctx after the handler's timeout fires). The handler treats *any* error as a clean mint failure and does not bind it, so such a session is left registered in the Pool with no conversation pointing at it. This is benign — the same shape as a session that ran and idled out, recoverable by the Pool's own lifecycle — and the race is unobserved, so per evidence-based fix selection no cleanup logic was added.
- **Process-exhaustion / spawn amplification (deferred).** Eager binding makes `create_conversation` a process-spawning operation; an authenticated phone spamming creates can exhaust host processes/memory. The existing in-architecture bound is `Pool.ActiveCap` (LRU-evicts a victim when the cap is hit) — but it **defaults to uncapped** (`-pyry-active-cap 0`). A dedicated per-operator create quota / rate-limit is new dispatch policy and is a named #672-family follow-up. Ops mitigation today: set `-pyry-active-cap` and/or `-pyry-idle-timeout`.
- **`ActiveCap` churn.** When `ActiveCap` *is* set, each `create_conversation` activation can LRU-evict another conversation's live claude. Acceptable: eviction preserves the on-disk JSONL and the session re-activates on the next `send_message`.
- **Restart scope.** Only the `CurrentSessionID` *field* round-trips on registry reload. Reviving / re-binding the live claude process across a daemon restart is the Pool's existing session-lifecycle / startup-reconciliation concern, out of scope here.

## Deferred to follow-ups (EPIC #672)

- **Distinct per-conversation working directory** + the trust/boundary validation that makes `conversation.Cwd` a safe spawn input. Until then, all conversation sessions share the trusted bootstrap workdir.
- **Per-operator create quota / rate-limit** (dispatch policy).

## Related

- [conversations-package.md](conversations-package.md) — the `Conversation.CurrentSessionID` binding field (and `SessionHistory`).
- [conversations-registry.md](conversations-registry.md) — atomic Save/Load that round-trips the binding (AC#3).
- [sessions-package.md](sessions-package.md) — `Pool.Create` mint primitive (§ *Pool.Create*) and `buildSession` (the `tpl.WorkDir` / `--session-id`-only spawn point).
- [idle-eviction.md](idle-eviction.md) — "evicted is a state, not removal"; lazy respawn on next `send_message`.
- [relay-package.md](relay-package.md) — the `create_conversation` handler and the `SessionCreator` seam alongside `TurnWriter`.
- [codebase/677.md](../codebase/677.md) — per-ticket implementation notes.
- [ADR 025](../decisions/025-mobile-remote-head-interactive-session.md) — mobile remote-head interactive session.
