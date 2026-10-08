# Spec — #677: create_conversation mints & binds a dedicated claude session per conversation

**Size:** S. Three production files touched (`internal/relay/handlers/create_conversation.go`, `cmd/pyry/relay.go`, `cmd/pyry/main.go`), ~160 LOC including tests. Call-site cascade for the signature change is 2 production + 5 test = 7, under the 10-site red line. `security-sensitive` — security-review pass appended at the end.

## Context

Today `create_conversation` only writes a registry row (`create_conversation.go:84-90`) with `CurrentSessionID` left empty, so every discussion shares the single bootstrap claude. This slice — the foundational cut of #672 — wires the create path onto the existing `sessions.Pool` so each conversation gets its own dedicated, isolated session, recorded via the existing `Conversation.CurrentSessionID` field. The session spawns in the daemon's already-trust-marked shared workdir (`cmd/pyry/main.go:513` `trustMark`); the phone-influenced `Cwd` is deliberately **not** a spawn input here — distinct per-conversation working directories (and the trust/boundary validation they require) are the per-conversation-workdir follow-up.

## Design

### Decision: eager mint via `Pool.Create` (AC#1 forces it)

The ticket delegates eager-vs-lazy to the architect. **AC#1 settles it: "the registry row has a non-empty `CurrentSessionID` referring to a session that exists in the sessions `Pool`" *after the frame is handled*.** A first-message lazy bind would leave `CurrentSessionID` empty (no pool membership) until a later `send_message` — violating AC#1. So the session must be registered in the Pool at create time. `Pool.Create(ctx, label)` is exactly that primitive (mint UUID → register + persist → supervise → activate); reuse it as-is. No new "register-without-spawn" primitive — that would be a new exported method and scope creep.

Note the distinction AC#1 actually requires: a session **exists in the Pool** while it has a registry entry + `p.sessions` map entry. That holds even after the idle/cap machinery later evicts it to disk (evicted is a lifecycle *state*, not removal). So "exists in the Pool" ≠ "claude is currently running" — the binding is durable even when the process is idle-evicted.

### Threading the Pool's mint surface to the handler

A new consumer-declared interface, mirroring the sibling `TurnWriter` so `handlers/` stays free of `internal/sessions`/`internal/supervisor` imports:

```go
// in internal/relay/handlers/create_conversation.go
type SessionCreator interface {
    Create(ctx context.Context, label string) (string, error)
}
```

`*sessions.Pool.Create` returns `(sessions.SessionID, error)`, not `(string, error)`, so it does not satisfy this directly — adapt at the `cmd/pyry` boundary with a thin wrapper that mirrors the existing `poolResolver` (which exists for exactly this "Pool's signature doesn't match the consumer interface" reason):

```go
// in cmd/pyry/main.go, beside poolResolver
type sessionMinter struct{ p *sessions.Pool }
func (m sessionMinter) Create(ctx context.Context, label string) (string, error) {
    id, err := m.p.Create(ctx, label)
    return string(id), err
}
```

Threading:
- Add a `creator handlers.SessionCreator` parameter to `startRelay` and `startRelayV2` (the `pool` value is already passed into `startRelay` as the `transitions` arg, so a second narrow view of the same value is a one-arg addition per signature — these are two distinct capabilities, so two distinct narrow interfaces is correct per CODING-STYLE, not a conflation into `transitionObserverSink`).
- Pass `sessionMinter{pool}` at the `startRelay(...)` call in `cmd/pyry/main.go:581`.
- Thread `creator` into both `handlers.CreateConversation(...)` registrations — the v1 dispatcher (`relay.go:162`) and the v2 manager handler map (`relay.go:301`).

### Handler body change (`CreateConversation`)

New constructor signature (suggested ordering; developer may adjust):

```go
func CreateConversation(reg ConversationCreator, creator SessionCreator,
    registryPath, defaultCwd string, logger *slog.Logger) dispatch.Handler
```

Revised flow inside the returned handler — unchanged steps marked, new steps in **bold**:

1. Decode payload; on error → `protocol.CodeProtocolMalformed`, not retryable (unchanged).
2. Resolve `promoted` / `cwd` / `name` (unchanged).
3. `id, err := conversations.NewID()`; on error → `protocol.CodeServerBinaryOffline`, retryable (unchanged).
4. **Mint the session, bounded by a timeout:** `mintCtx, cancel := context.WithTimeout(ctx, createConversationMintTimeout)` (30s, matching `sendMessageActivateTimeout` and control's `handleSessionsNew`; `time` is already imported); `sessionID, err := creator.Create(mintCtx, string(id))`; `cancel()`. The label is the server-minted conversation id — a stable session↔conversation breadcrumb in the session registry; the label never reaches the claude argv (`buildSession` only uses the SessionID for `--session-id`).
   - **On error → log at Warn (`create_conversation.session_mint_failed`, fields: `conn_id`, `conversation_id`, `err`) and reply `protocol.CodeServerBinaryOffline`, retryable; return without creating a row.** Failing before `reg.Create` means no half-bound orphan conversation; the phone retries and gets a fresh conversation + session. (The Pool may leave a benign unbound session entry on a non-empty-id error per its docstring — accepted residue, see Error handling.)
5. **`reg.Create(conversations.Conversation{ID: id, Name: name, Cwd: cwd, CurrentSessionID: string(sessionID), IsPromoted: promoted, LastUsedAt: now})`** — the one substantive line: populate `CurrentSessionID` before the existing eager `Save`.
6. `reg.Save(registryPath)` — best-effort eager persist (unchanged). Because the field is set in step 5, AC#3 is satisfied automatically (the registry round-trips all fields).
7. Marshal + reply `conversation_created` (unchanged). **The wire reply is unchanged** — `ConversationCreatedPayload` has no session field, and adding one is a wire break out of this slice's scope. The bound session id is internal binding state, surfaced only in the registry row.

### Update the SECURITY comment

The current comment (`create_conversation.go:46-52`) asserts "no code path in the current tree spawns a process … A future ticket that spawns a per-conversation claude session at `conversation.Cwd` MUST canonicalise + boundary-check the path." This slice makes the handler a spawn-consumer, so the comment must be rewritten to state precisely: *this handler now spawns a per-conversation session via the Pool, but into the daemon's already-trust-marked shared workdir (`buildSession`'s `tpl.WorkDir`), never `conversation.Cwd`; `Cwd` remains inert stored metadata; canonicalising + boundary-checking `Cwd`-as-spawn-input is still deferred to the per-conversation-workdir follow-up.* Keeping `Cwd` out of the spawn path is AC#4 and is enforced structurally — the handler passes only `(mintCtx, string(id))` to `creator.Create`.

## Concurrency model

No new goroutines or locks in the handler. `Pool.Create` is documented safe for concurrent use (serializes briefly through `Pool.mu` for register+persist, then runs supervise/Activate under the existing `capMu`). The session's lifecycle goroutine is owned by the Pool (scheduled on `Pool.Run`'s errgroup via `supervise`), exits on the pool's run-context — not the handler's. The handler's only resource is the `mintCtx` timeout context, released via `cancel()`/`defer`. Multiple conns creating concurrently is safe; the Pool owns serialization.

## Error handling

| Failure | Reply | Row created? |
|---|---|---|
| Malformed payload | `protocol.malformed` (not retryable) | no (unchanged) |
| `conversations.NewID()` rng failure | `server.binary_offline` (retryable) | no (unchanged) |
| `creator.Create` fails (any error: `ErrPoolNotRunning`, save failure, activate timeout, ctx deadline) | `server.binary_offline` (retryable) | **no** |
| Mint succeeds, `reg.Save` fails | `conversation_created` (success) | yes (in-memory; durability best-effort, unchanged) |

**Accepted residue:** `Pool.Create` can return a non-empty id *with* an error (e.g. the mint persisted then Activate timed out; per its docstring the lifecycle goroutine may still bring the session up against the pool ctx). We treat *any* error as a clean mint failure and do not bind it, so such a session is left registered in the Pool with no conversation pointing at it. This is benign — the same shape as a session that ran and idled out, recoverable by the Pool's own lifecycle — and the race is unobserved, so per evidence-based fix selection we do not add cleanup logic for it. Note it in `docs/knowledge/codebase/677.md` (written by documentation, not here).

## Testing strategy

Add a `stubSessionCreator` test double (mirror `stubTurnWriter` in `send_message_test.go`): records each `(ctx, label)` call; returns a configurable id and error; default returns a fresh distinct id per call so distinctness is observable. Update all 5 existing `CreateConversation(...)` call sites to pass the stub. Scenarios (bullets, not full bodies — developer writes them in the table-driven idiom):

- **AC#1 — binds an existing session.** Create one conversation; assert the stored row's `CurrentSessionID` is non-empty and equals the id the stub returned. (Rewrite `TestCreateConversation_CreatedID_ValidatesForSendMessage` — its "no claude session spawned at create time" premise inverts under eager binding; repoint it to assert the binding instead.)
- **AC#1 — label is the conversation id.** Assert the stub received `string(id)` as the label (the same id echoed in the `conversation_created` reply), and that it received exactly one `Create` call.
- **AC#2 — distinct per conversation.** Run the handler twice against a stub that returns incrementing ids; assert the two rows carry two different non-empty `CurrentSessionID`s. (The "each with its own `--session-id`/JSONL" half is `Pool.Create`/`buildSession`'s existing guarantee — referenced, not re-tested at the handler unit level.)
- **AC#3 — binding persists across reload.** Extend `TestCreateConversation_EagerPersist_SurvivesReload`: after `conversations.Load` from disk, assert the reloaded row's `CurrentSessionID` equals the value set at create time (round-trips through the registry's atomic Save/Load).
- **AC#4 — Cwd is never a spawn input.** Drive the handler with an explicit non-default `Cwd`; assert the stub's recorded label is the conversation id (a UUID), never the cwd, and that the stub's `Create` signature carries no cwd argument at all (structural: the interface has none).
- **Mint-failure path.** Stub returns an error; assert the reply is `server.binary_offline` retryable AND `reg.List()` is empty (no row created).
- **Existing malformed / all-null / explicit-fields tests** stay green with the stub wired (regression that the success path and reply shape are unchanged).
- **AC#5 — bootstrap unaffected.** Structural, not a new unit test: the bootstrap session is minted by `Pool.New` independently; `create_conversation` only calls `creator.Create` on a create frame. Covered by the existing `internal/sessions` bootstrap tests staying green. No e2e needed for this slice (the handler-level stub covers AC#1–#4; spawning a real claude per conversation through the `/bin/sleep` e2e harness adds turn cost without exercising the binding logic).

## Open questions

- **Process-exhaustion bound (see Security review §6/§9).** Eager binding makes `create_conversation` a process-spawning operation. Concurrent live processes are bounded only by `Pool.ActiveCap`, which defaults to uncapped (`-pyry-active-cap 0`). A dedicated per-operator create quota / rate-limit is new dispatch policy and is deferred to a follow-up; production should set `-pyry-active-cap` and/or `-pyry-idle-timeout`. This is a documentation + ops note for this slice, not new code.
- **`ActiveCap` churn interaction.** When `ActiveCap` *is* set, each `create_conversation` activation can LRU-evict another conversation's live claude (existing `Pool.Activate` behaviour). Acceptable for this slice (eviction preserves the on-disk JSONL and the session re-activates on next `send_message`); flag for the documentation phase.

---

## Security review

**Verdict:** PASS

**Findings:**

- **[Trust boundaries]** No MUST FIX. The phone→process boundary is the `create_conversation` payload decode (`json.Unmarshal`, `create_conversation.go:55-56`), unchanged. The *new* data crossing into the spawn path is: (a) the spawn workdir = `buildSession`'s `tpl.WorkDir` = the daemon's `trustMark`-ed shared workdir (`cmd/pyry/main.go:509-513`) — server-trusted, not phone-influenced; (b) the claude argv's `--session-id` = a server-minted UUID (`sessions.NewID`, crypto/rand). The one phone-influenced field that *could* be a spawn input, `conversation.Cwd`, is structurally excluded — the handler passes only `(mintCtx, string(id))` to `creator.Create`, and `buildSession` never reads the label/Cwd into argv. The boundary is explicit (one `creator.Create` call with two non-phone-controlled arguments) and documented in the rewritten SECURITY comment. This is the exact property AC#4 asserts.
- **[Subprocess / external command execution]** No MUST FIX. The only value reaching `exec.Command` that this slice influences is `--session-id <uuid>`, and that UUID is server-minted by `Pool.Create`→`NewID` (crypto/rand, canonical-shape `ValidID`), never phone-supplied. The label (= the server-minted conversation id) is recorded in the session registry as metadata and does **not** reach argv (`buildSession:949` uses the SessionID only). No `sh -c`. Env inheritance and signal handling are the Pool/supervisor's existing behaviour, unchanged by this slice.
- **[Tokens, secrets, credentials]** Not applicable — no tokens, keys, or credentials are created, stored, or compared by this change. The session id is a non-secret routing UUID (already a standard log field across `internal/sessions`); logging it on the success path leaks nothing.
- **[File operations]** No findings. No new path is constructed from phone input. The registry `Save` reuses the existing atomic temp-file→fsync→rename recipe at mode 0600 (`registry.go:72-116`), unchanged. `Cwd` remains inert stored metadata; it is not joined into any filesystem path on the spawn path in this slice (that lands with the per-conversation-workdir follow-up).
- **[Network & I/O]** SHOULD FIX (addressed in design) + OUT OF SCOPE (follow-up). The inbound frame is already size-capped by the transport (1 MiB WS read ceiling, `internal/transport`). Two sub-concerns from eager spawning: (1) **Per-conn goroutine hang** — `Pool.Create`→`Activate` blocks until claude's PTY is ready (documented 2–15s) or ctx-cancel; an unbounded wait would pin the per-conn goroutine on a wedged spawn. *Addressed in design:* the mint is wrapped in a 30s `context.WithTimeout`, converting a wedged spawn into a retryable `server.binary_offline` (mirrors `send_message.go`). (2) **Process-spawn amplification** — each frame now spawns a real claude process; an authenticated phone spamming `create_conversation` can exhaust host processes/memory. Concurrent live processes are bounded by `Pool.ActiveCap`, but it defaults to uncapped. *Classification:* the in-architecture bound (`ActiveCap`) already exists and is an ops/config setting; a dedicated per-operator create quota/rate-limit is new dispatch policy and is **OUT OF SCOPE → named follow-up** (per-conversation-workdir epic #672 family). The 30s timeout bounds the single-call hang but not the aggregate spawn rate; this is stated honestly in Open questions. Not a MUST FIX because (a) the actor is an authenticated paired device, (b) an operator mitigation exists today, (c) the policy layer is legitimately a separate ticket.
- **[Cryptographic primitives]** Not applicable — no crypto introduced. Session-id entropy is `Pool`'s existing `crypto/rand` UUIDv4.
- **[Error messages, logs, telemetry]** No findings. The new log line (`create_conversation.session_mint_failed`) carries only `conn_id`, `conversation_id` (non-secret routing ids, already logged on sibling paths), and the Pool's wrapped `err` (carries package context, no payload/secret). Payload text is never logged (consistent with the existing handler discipline). The success log may add the non-secret `session_id`.
- **[Concurrency]** No findings. The handler adds no locks and no handler-owned goroutines; `Pool.Create` is documented concurrency-safe and the session lifecycle goroutine is Pool-owned (exits on the pool run-context). The `mintCtx` timeout is released via `cancel()`/`defer` — no leak. The non-empty-id-with-error residue (a session that may activate against the pool ctx after our timeout) is documented in Error handling and is benign (idle-evictable, no dangling FD — same property the supervisor's teardown-safe PTY path established).
- **[Threat model alignment]** `docs/protocol-mobile.md` § Security model treats paired devices as authenticated but not fully trusted. The relevant threat this slice touches is *an authenticated phone triggering server-side resource consumption* — surfaced above under Network & I/O, mitigated by `ActiveCap` + the per-call timeout, with the dedicated quota named as a follow-up. The phone cannot influence *what* is spawned (server-minted UUID) or *where* (server-trusted workdir) — only *that* a spawn happens, and only via an authenticated, rate-mitigable channel.

**Reviewer:** architect (self-review per `architect/security-review.md`)
**Date:** 2026-06-18
