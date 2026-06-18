# Spec — #678: send_message routes the turn to the conversation's bound session

**Size:** S. Three production files touched (`internal/relay/handlers/send_message.go`, `cmd/pyry/relay.go`, `cmd/pyry/main.go`), ~180 LOC including tests. `SendMessage` call-site cascade for the signature change is 2 production + 7 test = 9, under the 10-site red line — and there is **no transitive cascade** (the handler's return type `dispatch.Handler` is unchanged, so swapping its constructor's parameter type does not ripple beyond the direct call sites). The `startRelay`/`startRelayV2`/`main.go` parameter threading is the same shape #677 did when it added `creator` — a swap of one narrow-interface parameter (`sess handlers.TurnWriter` → `router handlers.SessionRouter`). This is the acknowledged direct sibling of #677: same files, same consumer-declared-interface + cmd/pyry-adapter seam, type/consumer seam already established. `security-sensitive` — security-review pass appended at the end.

## Files to read first

- `internal/relay/handlers/send_message.go:47-60` — `TurnWriter` interface (`Activate` + `WriteUserTurn`). This stays as-is; it becomes the **return type** of the new `SessionRouter`. The consumer-declared-interface idiom (plain types only, no `internal/sessions` import) is the pattern to extend.
- `internal/relay/handlers/send_message.go:74-151` — the whole handler. `:84-107` is the Activate phase (30s `activateCtx`), `:113-149` is the deliver phase + the result switch. **This two-phase block must stay byte-identical, operating on the resolved write surface.** The only edit here is the constructor signature + a resolution prelude before `:89`.
- `internal/relay/handlers/send_message.go:124` — the existing `errors.Is(err, conversations.ErrConversationNotFound)` arm in the deliver switch. It stays (defends the TOCTOU case where the conversation is deleted between routing and delivery — the bound session's own `ValidateConversation` still fires). `handlers/` already imports `internal/conversations` (line 10); it must **not** gain an `internal/sessions` import.
- `internal/relay/handlers/create_conversation.go:51-59` — the `SessionCreator` interface: the exact consumer-declared-interface idiom to mirror for `SessionRouter`. `:86` shows the constructor threading a narrow collaborator alongside `reg`/`registryPath`/`logger`.
- `cmd/pyry/main.go:620-644` — `poolResolver` (covariant-return adapter, `Pool.Lookup` → `control.Session`) and `sessionMinter` (the #677 adapter, `Pool.Create` → `handlers.SessionCreator`). The new `sessionRouter` + `boundSession` adapters sit here, beside these, for the same reason: `cmd/pyry` is the only package importing both `internal/conversations` and `internal/sessions`.
- `cmd/pyry/main.go:580-585` — the `startRelay(...)` call. `bootstrap := pool.Default()` and `bootstrap.Supervisor()`/`bootstrap.Bridge()` stay (the assistant-turn bridge needs them); only the standalone `bootstrap` `TurnWriter` argument is replaced by `sessionRouter{pool, convReg}`.
- `cmd/pyry/relay.go:88-102` — `startRelay` signature (the `sess handlers.TurnWriter` parameter at `:96`). `:143-165` — v1 dispatcher registration (`handlers.SendMessage(sess, logger)` at `:165`, the `startRelayV2(...)` call at `:145`). `:272-306` — `startRelayV2` signature (`sess` at `:281`) + v2 manager registration (`handlers.SendMessage(sess, logger)` at `:305`).
- `internal/sessions/pool.go:685-696` — `Pool.Lookup(id) (*Session, error)`. **Critical:** `Lookup("")` returns the **bootstrap** session (line 688-690). This is the silent-route-to-bootstrap hazard AC#4 forbids — the empty-`CurrentSessionID` case must be rejected *before* any `Lookup`.
- `internal/sessions/pool.go:1145-1170` — `Pool.Activate(ctx, id) error`: the single cap-enforcing spawn-path entry. Activation MUST funnel through this, never `Session.Activate` directly, or it bypasses `ActiveCap` (the idle-evict follow-up #680 relies on it).
- `internal/sessions/pool.go:947-1002` — `buildSession`: confirms each per-conversation session gets its **own** `ValidateConversation` closure (`:966-973`) over `convReg.Get`, so the bound session's `WriteUserTurn` re-validates the conversation id (the TOCTOU defence above). `*Session` already satisfies `TurnWriter` (`Activate` + `WriteUserTurn`).
- `internal/conversations/registry.go:128-137` — `Registry.Get(id) (Conversation, bool)`: miss → `false` (maps to `conversation.not_found`).
- `internal/conversations/conversation.go:44-49` — `Conversation.CurrentSessionID string`: the binding field #677 populates at create time; empty when no session is bound.
- `internal/relay/handlers/send_message_test.go:27-53` — `stubTurnWriter` (records `Activate`/`WriteUserTurn` args + `callOrder`). The 7 existing tests pass it directly to `SendMessage`; they must now route through a `stubSessionRouter` that returns it. `:121-130` shows the table-test setup shape.
- `docs/knowledge/features/conversation-session-binding.md` — #677's eager-bind contract; "exists in the Pool" = registry entry + `p.sessions` entry, durable across idle-eviction.
- `docs/knowledge/features/idle-eviction.md` § "Activate-before-Attach contract" / "`Pool` surface" — why activation must go through `Pool.Activate` (cap), and the established 30s `send_message` Activate-before-write contract.

## Context

`send_message` today routes **every** turn to the single bootstrap session, regardless of the frame's `ConversationID` (`relay.go:165`, `:305` both wire `handlers.SendMessage(sess, …)` where `sess` is `pool.Default()`). The `ConversationID` reaches `Supervisor.WriteUserTurn`, but only to validate the conversation and stamp the outbound cursor — never to *select* a session. #677 made every conversation mint and bind its own dedicated session at create time (recorded in `Conversation.CurrentSessionID`). This slice — the consumer half of that work — makes `send_message` resolve the bound session and route the turn **there** instead of to the bootstrap, so each discussion's turns land in its own claude and never bleed across conversations.

The resolution inputs all already exist: `Registry.Get(id)` → the row (carrying `CurrentSessionID`); `Pool.Activate(ctx, sessionID)` → cap-enforcing activation; `Pool.Lookup(sessionID)` → the `*Session` to write on. No new wire code, no new primitives. The only genuinely new reject case is the **empty-`CurrentSessionID`** (no-bound-session) conversation, which lands on the established retryable `server.binary_offline` reply.

## Design

### Decision: a `SessionRouter` that returns the per-conversation `TurnWriter` (preserves the two-phase block)

AC#5 is the load-bearing constraint: *"only the session-selection step is new"* — the Activate-before-write ordering, the two 30s budgets, the ack envelope, and the activate/delivery error mapping must behave exactly as today. The cleanest way to honour that is to **resolve the write surface, then run the existing two-phase block against it unchanged.**

A new consumer-declared interface in `handlers/`, mirroring the sibling `TurnWriter`/`SessionCreator` so `handlers/` stays free of any `internal/sessions` import:

```go
// in internal/relay/handlers/send_message.go
//
// SessionRouter resolves a send_message frame's conversation id to the write
// surface for that conversation's bound claude session. Resolution is a pure
// in-memory lookup (no I/O, non-blocking) — the blocking activation happens in
// the returned TurnWriter's Activate. Failure mapping:
//   - unknown conversation                       → conversations.ErrConversationNotFound
//   - conversation has no bound session, or the
//     bound session id is not in the pool         → any other non-nil error
//     (the handler maps it to retryable server.binary_offline)
type SessionRouter interface {
    Route(conversationID string) (TurnWriter, error)
}
```

`Route` is deliberately **ctx-free**: it does `Registry.Get` (mutex read) + a field check + `Pool.Lookup` (RLock) — no blocking, no cancellation surface to honour. The 30s budgets stay where they are (the `activateCtx`/`deliverCtx` in the handler, applied to the returned writer's `Activate`/`WriteUserTurn`).

### Handler change (`SendMessage`) — one prelude, everything else unchanged

New constructor signature:

```go
func SendMessage(router SessionRouter, logger *slog.Logger) dispatch.Handler
```

Revised flow inside the returned handler (unchanged steps marked, **new** in bold):

1. Decode payload; on error → `protocol.CodeProtocolMalformed`, not retryable (**unchanged**).
2. **Resolve the bound session:** `w, err := router.Route(p.ConversationID)`. On error, a two-arm switch:
   - `errors.Is(err, conversations.ErrConversationNotFound)` → log `send_message.unknown_conversation` (conn_id, conversation_id) → `replyError(CodeConversationNotFound, msgConversationNotFound, false)` (not retryable). Reuses the existing constant + event name (the not_found arm previously fed by `WriteUserTurn`).
   - default (no bound session / bound id absent from pool) → log a **new** `send_message.no_bound_session` event (conn_id, conversation_id, err) → `replyError(CodeServerBinaryOffline, msgServerBinaryOffline, true)` (retryable). Reuses the existing constant.
3. **Two-phase Activate + WriteUserTurn against `w` — byte-identical to today** (`send_message.go:84-149`): `activateCtx` (30s) → `w.Activate`; the activate-failure arms (ctx.Canceled propagation vs `server.binary_offline`); `deliverCtx` (30s) → `w.WriteUserTurn(deliverCtx, p.ConversationID, []byte(p.Text))`; the result switch (`nil`→ack; `ErrConversationNotFound`→not_found; ctx.Canceled→propagate; default→binary_offline).

`p.ConversationID` is still passed to `WriteUserTurn` (it stamps the supervisor's outbound cursor that the assistant-turn bridge reads — #311/#312). The deliver switch's `ErrConversationNotFound` arm stays: the bound session's own `ValidateConversation` closure (`buildSession:966-973`) re-checks the id, catching the rare TOCTOU where the conversation is deleted between `Route` and delivery.

The only new constant is one log-event string. `msgConversationNotFound` / `msgServerBinaryOffline` / both `Code*` values are reused as-is (Technical Notes: "No new wire code is needed").

### The `sessionRouter` + `boundSession` adapters (at `cmd/pyry`, beside `sessionMinter`)

`cmd/pyry` is the only package importing both `internal/conversations` and `internal/sessions`, so the resolution that bridges them lives there — exactly where `poolResolver` and `sessionMinter` already live. Two unexported types (mirror `sessionMinter`'s shape; developer writes the bodies):

- `sessionRouter{pool *sessions.Pool; convReg *conversations.Registry}` satisfies `handlers.SessionRouter`. Its `Route(conversationID string)` performs the resolution **in this exact order**, and the order is load-bearing:
  1. `r.convReg.Get(conversations.ConversationID(conversationID))` — miss → return `conversations.ErrConversationNotFound` (→ `conversation.not_found`).
  2. **`conv.CurrentSessionID == "" → return errNoBoundSession`** *before any `Lookup`* (see invariant below) — an unexported sentinel (→ `server.binary_offline`).
  3. `r.pool.Lookup(sessions.SessionID(conv.CurrentSessionID))` — `ErrSessionNotFound` flows straight through (→ `server.binary_offline`).
  4. wrap the resolved `*Session` in `boundSession{pool, sess, id}` and return it.
- `boundSession{pool *sessions.Pool; sess *sessions.Session; id sessions.SessionID}` satisfies `handlers.TurnWriter`: `Activate(ctx)` returns `b.pool.Activate(ctx, b.id)`; `WriteUserTurn(ctx, convID, payload)` returns `b.sess.WriteUserTurn(ctx, convID, payload)`. `*sessions.Session` already satisfies `TurnWriter` directly — this wrapper exists **only** to redirect `Activate` through the cap-enforcing `Pool.Activate` (see second invariant). One unexported sentinel: `errNoBoundSession` (no wire surface).

**Why the empty-`CurrentSessionID` guard is load-bearing (AC#4).** `Pool.Lookup("")` returns the *bootstrap* session (`pool.go:688-690`), and `Pool.Activate(ctx, "")` would then activate and write to the bootstrap. That is precisely the "silently routed to the bootstrap session" failure AC#4 forbids. The `conv.CurrentSessionID == ""` check rejects before any `Lookup`/`Activate`, returning a non-`ErrConversationNotFound` error so the handler replies retryable `server.binary_offline`. This guard is asserted by a dedicated test.

**Why `boundSession.Activate` routes through `Pool.Activate`, not `Session.Activate`.** The bootstrap path used `Session.Activate` (the bootstrap is special — always active, never cap-evicted). Per-conversation sessions are full cap citizens: activating one may need to LRU-evict a peer (`Pool.Activate`'s cap path, `pool.go:1154-1169`). Calling `Session.Activate` directly would bypass `ActiveCap` and break the invariant #680 (idle-evict follow-up) depends on. `errNoBoundSession` is unexported (no wire surface); `Pool.Lookup`'s `ErrSessionNotFound` flows through the same default arm — both are "bound-but-unavailable" → `server.binary_offline`.

### Threading

- Replace the `sess handlers.TurnWriter` parameter with `router handlers.SessionRouter` in both `startRelay` (`relay.go:96`) and `startRelayV2` (`relay.go:281`). `sess` is consumed **only** by the two `handlers.SendMessage(sess, …)` sites — confirmed by grep — so this is a clean swap, not an addition. (The assistant-turn bridge takes `sup`/`bridge`, not `sess`.)
- Change both registrations to `handlers.SendMessage(router, logger)` (`relay.go:165`, `:305`), and thread `router` through the `startRelayV2(…)` call (`:145`).
- At `main.go:581`, pass `sessionRouter{pool: pool, convReg: convReg}` in place of the `bootstrap` `TurnWriter` argument. `bootstrap.Supervisor()` / `bootstrap.Bridge()` arguments are unchanged; only the standalone `bootstrap` writer arg goes away. `convReg` is already in scope at that call site (passed as the `convReg` argument today).

## Concurrency model

No new goroutines, no new locks. `Route` reads `Registry.Get` (its own mutex) and `Pool.Lookup` (`Pool.mu` RLock) — both already concurrency-safe, both non-blocking. `boundSession.Activate` delegates to `Pool.Activate`, whose cap serialisation (`Pool.capMu` → `Pool.mu` → `Session.lcMu`) is unchanged and documented in idle-eviction.md. The `*Session` pointer captured in `boundSession` is stable: `RotateID` mutates the id field, not the map pointer, and `Pool.Remove` is not on the send_message path; even a racing removal is benign because `Pool.Activate(ctx, id)` re-Lookups internally and returns `ErrSessionNotFound` (→ binary_offline) rather than touching a freed pointer. The handler's two `context.WithTimeout` budgets are released via `cancel()` exactly as today.

## Error handling

| Case | Where detected | Reply | Retryable |
|---|---|---|---|
| Malformed payload | handler decode (unchanged) | `protocol.malformed` | no |
| Unknown `ConversationID` | `Route`: `Registry.Get` miss | `conversation.not_found` | no |
| Conversation has no bound session (`CurrentSessionID == ""`) | `Route`: empty-id guard | `server.binary_offline` | **yes** |
| Bound id not in pool (`ErrSessionNotFound`) | `Route`: `Pool.Lookup` | `server.binary_offline` | yes |
| Activate fails / times out | handler phase 1 (unchanged) | `server.binary_offline` | yes |
| Conversation deleted mid-flight (TOCTOU) | `WriteUserTurn` → `ValidateConversation` → `ErrConversationNotFound` | `conversation.not_found` | no |
| No live session / wedged / uncommitted | `WriteUserTurn` default (unchanged) | `server.binary_offline` | yes |
| conn ctx cancelled | propagated (unchanged) | — (per-conn unwind) | — |

The unknown-conversation reject now fires at **routing** time (before Activate) rather than at delivery time. Net behaviour to the phone is identical — a `conversation.not_found` for an unknown id either way — but it no longer spends an Activate budget on a doomed turn.

## Testing strategy

Add a `stubSessionRouter` test double (mirror the `stubTurnWriter` idiom). It records the `conversationID` it was asked to route and returns a preconfigured `(TurnWriter, error)`. A tiny helper keeps the existing-test rewiring uniform: `func routeTo(w TurnWriter) SessionRouter { return &stubSessionRouter{tw: w} }`.

- **Rewire the 7 existing tests** (`send_message_test.go:131,174,215,241,282,322,346`) to construct the handler as `SendMessage(routeTo(stub), …)`. These assert the **unchanged** two-phase behaviour (success ack, activate-timeout → binary_offline, activate-ctx-canceled propagation, deliver `ErrConversationNotFound` TOCTOU → not_found, no-live-session → binary_offline, deliver-ctx-canceled propagation, payload/`callOrder` capture) and must stay green — they are the AC#5 regression guard that the contract is preserved.
- **AC#1 — routes to the bound session.** Router returns a distinct `stubTurnWriter` for `convID`; assert the handler ran `Activate` then `WriteUserTurn` on *that* writer (via `callOrder`) and replied ack. Assert `WriteUserTurn` received the frame's `ConversationID` (cursor-stamp contract) and the verbatim text.
- **AC#2 — two conversations, no cross-delivery.** A `stubSessionRouter` mapping `convID → distinct stubTurnWriter`. Drive the handler for conversation A then B; assert A's writer received A's turn and B's writer received B's turn, and neither received the other's (zero `WriteUserTurn` calls on the wrong writer).
- **AC#3 — idle-evicted bound session re-activates.** Unit-level: `boundSession.Activate` funnels through `Pool.Activate`. Assert at the handler level via a router whose returned writer's `Activate` is observed to run before `WriteUserTurn` (the existing Activate-before-write `callOrder` assertion already covers ordering; this AC's "through the cap path" is `Pool.Activate`'s existing guarantee, exercised by the `internal/sessions` cap tests, referenced not re-tested here). Note in the spec that the cmd/pyry `boundSession`/`sessionRouter` adapters are exercised structurally (they compile against the real `*sessions.Pool`); a focused adapter test is optional, see Open questions.
- **AC#4 — unknown conversation → not_found; no bound session → binary_offline; never bootstrap.**
  - Router returns `conversations.ErrConversationNotFound`; assert reply `conversation.not_found`, not retryable, and the writer's `WriteUserTurn` was **never** called.
  - Router returns `errNoBoundSession` (or any non-`ErrConversationNotFound` error); assert reply `server.binary_offline`, retryable, and no `WriteUserTurn`.
  - The "never silently routed to bootstrap" half is the `sessionRouter` empty-`CurrentSessionID` guard. Cover it where `convReg`/`pool` are real (a `cmd/pyry` adapter test, or an `internal/sessions`-backed check): a conversation row with `CurrentSessionID == ""` makes `Route` return an error and never returns the bootstrap session. **This is the one assertion that must touch the real `Pool.Lookup("")`-returns-bootstrap behaviour** — a pure handler stub cannot prove it. See Open questions for placement.
- **Malformed payload** stays green with the router wired (regression: decode rejection is upstream of routing).

All table-driven, stdlib `testing`, `t.Parallel()` where independent. Scenarios are bullets; the developer writes them in the project idiom.

## Open questions

- **Where to assert the empty-`CurrentSessionID`-never-routes-to-bootstrap guard.** The hazard lives in the `cmd/pyry` `sessionRouter` adapter (it calls the real `Pool.Lookup`, whose `""` arm returns bootstrap). Options: (a) a small `cmd/pyry` test constructing a real `*sessions.Pool` + `*conversations.Registry` with one bound and one empty-`CurrentSessionID` row, asserting `Route` errors on the empty one and returns the right `*Session` on the bound one; (b) fold it into an existing `cmd/pyry` relay/adapter test if one exists. The developer picks based on existing `cmd/pyry` test scaffolding; (a) is recommended because it directly pins AC#4's structural guarantee. The handler-level stub tests cover the wire mapping; this covers the resolution itself.
- **`Route` ctx-free vs ctx-carrying.** Chosen ctx-free (pure in-memory, non-blocking). If a future router grows blocking resolution (e.g. a disk read), add ctx then — not preemptively (CODING-STYLE: don't thread ctx where there's nothing to cancel).

---

## Security review

**Verdict:** PASS

**Threat framing.** This slice changes *which session* a phone's turn reaches. The adversarial question is therefore: **can an authenticated-but-not-fully-trusted paired device influence routing beyond the conversations it legitimately owns, or redirect a turn to a session it should not reach (notably the shared bootstrap)?** The phone supplies two values — `ConversationID` (a lookup key) and `Text` (the turn). It does **not** supply the routing target: `CurrentSessionID` is read from the server-stored registry row (minted server-side by #677, with no wire field for the phone to set it). So the phone selects a conversation by its server-minted id; the server alone decides which session that maps to.

**Findings:**

- **[Trust boundaries]** No MUST FIX — the one real hazard is closed by design and tested. The phone→server boundary is the `send_message` payload decode (unchanged). The *new* phone-influenced value crossing into a control decision is `ConversationID`, used as a byte-exact key into `Registry.Get`. Two properties bound it: (a) `ConversationID`s are server-minted crypto/rand UUIDv4 (`conversations.NewID`, #677) — unguessable, so a phone can only reach a conversation whose id it already legitimately holds; (b) the routing *target* (`CurrentSessionID`) is server-stored, never phone-writable, so the phone cannot point a conversation at an arbitrary session. **The key finding is the empty-`CurrentSessionID` guard:** `Pool.Lookup("")` returns the *bootstrap* session, so without an explicit empty-id rejection, a conversation with no bound session would silently route the phone's turn into the shared bootstrap claude (a confused-deputy / isolation break — exactly what AC#4 forbids). The design rejects `CurrentSessionID == ""` in `sessionRouter.Route` *before* any `Lookup`/`Activate`, mapping it to a retryable `server.binary_offline`, and a dedicated test pins it against the real `Pool.Lookup` behaviour. Net effect of the slice is *more* isolation than today (per-conversation sessions vs one shared bootstrap), not less. No cross-tenant surface is introduced: the conversations registry is per-operator and all paired devices already authenticate as that one operator — this change does not widen who can address what.
- **[Subprocess / external command execution]** No MUST FIX. Routing can trigger `boundSession.Activate` → `Pool.Activate` → respawn of an idle-evicted session (`claude --session-id <uuid>`). The `<uuid>` is the **server-minted** `SessionID` from the trusted row, never phone-supplied; the workdir is `tpl.WorkDir` (the daemon's trust-marked shared workdir, identical to #677). No phone-controlled value reaches `exec.Command` — the phone controls only *that* an already-bound session reactivates and the turn text reaching claude's stdin (verbatim, unchanged from today's `send_message`). No new argv influence, no `sh -c`.
- **[Tokens, secrets, credentials]** Not applicable. No tokens/keys/credentials are created, stored, or compared. `SessionID`/`ConversationID` are non-secret routing UUIDs (already standard log fields across `internal/sessions` and the relay handlers).
- **[File operations]** No findings. No new path is constructed from phone input. `Route` is two in-memory map lookups (`Registry.Get`, `Pool.Lookup`). Re-activation reuses the existing JSONL at `~/.claude/projects/<encoded-cwd>/<server-minted-uuid>.jsonl` — both path components are server-controlled (trusted shared workdir + server-minted uuid). Distinct per-conversation cwd (which would make `Cwd` a path input) remains deferred to the per-conversation-workdir follow-up; this slice adds no `Cwd`-derived path.
- **[Network & I/O]** No MUST FIX; one item carried forward (not introduced here). The inbound frame is already transport-size-capped (1 MiB WS read ceiling). `Route` is non-blocking, so it adds no new per-conn hang surface; the blocking steps (`Activate`, `WriteUserTurn`) remain bounded by the handler's existing two 30s `context.WithTimeout` budgets (unchanged — AC#5). **Process-spawn amplification:** routing can re-activate evicted sessions, and under a set `ActiveCap` each re-activation can LRU-evict a peer (existing `Pool.Activate` behaviour). This is bounded by the number of already-bound conversations and is a **strict subset** of #677's create-time spawn amplification — routing creates no new sessions, it only wakes existing bound ones. The in-architecture bound (`ActiveCap`) and the named per-operator create-quota follow-up (#672 family) already cover it; nothing new to fix in this consumer slice.
- **[Cryptographic primitives]** Not applicable — no crypto introduced.
- **[Error messages, logs, telemetry]** No findings. The new `send_message.no_bound_session` WARN line carries only `conn_id`, `conversation_id` (non-secret routing ids, already logged on the sibling `unknown_conversation`/`delivery_failed` arms), and the wrapped error (`errNoBoundSession` / `ErrSessionNotFound` — package context only, no payload, no secret). Payload `Text` is never logged at any level (the existing handler discipline is preserved unchanged). The reused `unknown_conversation` arm is unchanged.
- **[Concurrency]** No findings. No new locks or handler-owned goroutines. `Route` uses the existing thread-safe `Registry.Get` (its own mutex) and `Pool.Lookup` (`Pool.mu` RLock). The `*Session` pointer captured in `boundSession` is stable (RotateID mutates the id field, not the map pointer; `Pool.Remove` is off this path), and the worst-case mid-flight removal degrades to `Pool.Activate`'s internal re-Lookup returning `ErrSessionNotFound` (→ retryable reply), never a dangling-pointer dereference. The two `context.WithTimeout` budgets are released via `cancel()` exactly as today.
- **[Threat model alignment]** `docs/protocol-mobile.md` § Security model treats paired devices as authenticated but not fully trusted. The relevant threat — *a phone choosing which session its turn reaches* — is constrained to (a) conversations whose server-minted id the phone holds, and (b) the single server-bound session per conversation, which the phone can neither supply nor redirect. The empty-binding guard removes the one path that would have leaked a turn into the shared bootstrap. Residual resource consumption (re-activation churn) is bounded by `ActiveCap` and ⊆ the already-tracked #677 amplification, with the dedicated per-operator quota named as a follow-up.

**Reviewer:** architect (self-review per `architect/security-review.md`)
**Date:** 2026-06-18
