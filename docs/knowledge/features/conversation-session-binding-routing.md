# conversation-session-binding — routing (`send_message` consumes the binding)

Split out of [`conversation-session-binding.md`](conversation-session-binding.md) (parent doc — see there for how the binding is created and maintained). This child covers the read side: how `send_message` resolves a conversation to its bound session, and the enqueue-and-ack contract that governs what runs before the wire reply.

## Routing: `send_message` consumes the binding

\#678 is the consumer half. Where the create path *writes* `CurrentSessionID`, `send_message` *reads* it to select the session a turn is delivered to. Before #678 the handler held a single `TurnWriter` (the bootstrap session) and routed every turn there regardless of `ConversationID`; now it resolves the frame's conversation to its bound session. Since [#721](../codebase/721.md) the handler no longer *delivers* synchronously — it validates the binding, **enqueues**, and acks; the daemon's `msgqueue` drain runs Activate-before-write against *that* session asynchronously (see [§ Enqueue-and-ack (#721)](#enqueue-and-ack-721)).

### The `SessionRouter` seam (mirrors `SessionCreator`)

The handler depends on a second consumer-declared interface that *returns* the existing `TurnWriter`, so `handlers/` still imports no `internal/sessions`:

```go
// internal/relay/handlers/send_message.go
type SessionRouter interface {
    Route(conversationID string) (TurnWriter, error)
}
```

`Route` is **ctx-free** — a registry read + field check + pool lookup, non-blocking, no cancellation surface. Since #1487 a *missing* pool entry additionally takes the revive branch below, which adds filesystem syscalls (`EvalSymlinks`, possibly `MkdirAll`, the `trustMark` write, the registry persist) but still never waits on a child process, and runs at most once per session per daemon lifetime — after it succeeds `Lookup` hits and the steady-state path is byte-identical. Since #721 the handler **discards** the returned writer (it only validates the binding before enqueue); the blocking work (`Activate`, `WriteUserTurn`) happens on the `msgqueue` drain, which re-resolves the writer per attempt via `sessionRouter.resolve` — see [§ Enqueue-and-ack (#721)](#enqueue-and-ack-721).

The implementation lives at `cmd/pyry` (the only package importing both `conversations` and `sessions`), beside `sessionMinter`:

```go
// cmd/pyry/main.go
type sessionRouter struct {
    pool    *sessions.Pool
    convReg *conversations.Registry
    active  *activeConversation   // the #687 follow-active cursor Route stamps
}
// resolve is the single resolution authority — the side-effect-free core (#721).
func (r sessionRouter) resolve(conversationID string) (handlers.TurnWriter, error) {
    conv, ok := r.convReg.Get(conversations.ConversationID(conversationID))
    if !ok {
        return nil, conversations.ErrConversationNotFound      // → conversation.not_found
    }
    if conv.CurrentSessionID == "" {
        return nil, errNoBoundSession                          // → server.binary_offline (before any Lookup!)
    }
    id := sessions.SessionID(conv.CurrentSessionID)
    sess, err := r.pool.Lookup(id)
    if errors.Is(err, sessions.ErrSessionNotFound) {
        sess, err = r.revive(id, conversationID, conv.Cwd)     // #1487 — daemon-restart revive
    }
    if err != nil {
        return nil, err                                        // → server.binary_offline
    }
    return boundSession{pool: r.pool, sess: sess, id: id}, nil
}

// revive re-materialises a dropped session; it does NOT spawn claude (#1487).
func (r sessionRouter) revive(id sessions.SessionID, label, cwd string) (*sessions.Session, error) {
    spawnDir, err := resolveSpawnDir(cwd)   // the SAME validator the mint path uses
    if err != nil {
        return nil, err                     // ErrSpawnDirRejected → server.binary_offline, nothing registered
    }
    return r.pool.Revive(id, label, spawnDir)
}

// Route layers the active-conversation cursor stamp (#687) onto resolve.
func (r sessionRouter) Route(conversationID string) (handlers.TurnWriter, error) {
    w, err := r.resolve(conversationID)
    if err != nil {
        return nil, err
    }
    r.active.set(conversationID)   // stamp only on success — the phone-interaction moment
    return w, nil
}
```

Since #721, `Route` is a thin wrapper that adds the `r.active.set` cursor stamp; `resolve` is the stamp-free core. Both the handler (validation) and the `msgqueue` drain (`newInboundDeliver`) go through `resolve`, so neither can bypass the empty-binding guard, and **only `Route` moves the cursor** — the drain must not (it would re-stamp at *drain* time, corrupting the #679/#687 follow-active signal). Guarded by `TestSessionRouter_ResolveDoesNotStamp`.

### Two load-bearing invariants

- **The empty-`CurrentSessionID` guard fires *before* any `Lookup`.** `Pool.Lookup("")` returns the **bootstrap** session. Without the up-front `== ""` rejection, an unbound conversation would silently route the phone's turn into the shared bootstrap claude — the confused-deputy / isolation break AC#4 forbids. Rejecting first maps the case to a retryable `server.binary_offline` instead. (The phone supplies only the `ConversationID` lookup key and the `Text`; the routing *target* is the server-stored `CurrentSessionID`, never phone-writable — so the phone can only address a conversation whose server-minted id it already holds, and can never point it at an arbitrary session.)
- **`boundSession.Activate` funnels through `Pool.Activate`, not `Session.Activate`.** `*sessions.Session` already satisfies `TurnWriter` directly; the `boundSession` wrapper exists *only* to redirect `Activate` through the cap-enforcing `Pool.Activate(ctx, id)`. The bootstrap was special (always active, never cap-evicted) so it could use `Session.Activate`; per-conversation sessions are full `ActiveCap` citizens — activating one may LRU-evict a peer, which only happens inside `Pool.Activate`. Bypassing it would break the invariant the idle-evict follow-up (#680) relies on. An idle-evicted bound session therefore re-activates on the next **drain delivery attempt** for that conversation (since #721; before #721, on the next `send_message`) — the [idle-eviction.md](idle-eviction.md) lazy-respawn contract, now per-conversation. Since #2085 a freshly minted, never-messaged session takes the identical path on its *first* drain delivery attempt — there is no separate "first spawn" mechanism, only this one, reached one message earlier in a conversation's life than before.

### Error mapping (no new wire code)

| Case | Detected in | Reply | Retryable |
|---|---|---|---|
| Unknown `ConversationID` | `Route`: `Registry.Get` miss | `conversation.not_found` | no |
| No bound session (`CurrentSessionID == ""`) | `Route`: empty-id guard | `server.binary_offline` | yes |
| Bound id not in pool, revive succeeds (#1487) | `Route`: `Pool.Lookup` miss → `sessionRouter.revive` | *(no reject — the turn is accepted)* | — |
| Bound id not in pool, recorded `Cwd` escapes `$HOME` | `Route`: `resolveSpawnDir` (`ErrSpawnDirRejected`) | `server.binary_offline` | yes |
| Bound id not in pool, id not canonical UUIDv4 | `Route`: `Pool.Revive`'s `ValidID` (`ErrInvalidSessionID`) | `server.binary_offline` | yes |
| Bound id not in pool, pool not running | `Route`: `Pool.Revive` (`ErrPoolNotRunning`) | `server.binary_offline` | yes |

All three are checked synchronously **before enqueue** (#721): the handler's only wire replies are these rejects + the ack. `errNoBoundSession` is an unexported sentinel with no wire surface. A conversation that becomes unbound/deleted *after* the ack (a TOCTOU window) no longer maps to a wire reply — the drain's `resolve`/`WriteUserTurn` error is **absorbed and retried** (the ack already promised delivery). There is no conversation-delete verb today, so a permanent post-ack unbind is currently unreachable; the daemon-restart boundary bounds it.

### Enqueue-and-ack (#721)

[#721](../codebase/721.md) makes the [#704](../codebase/704.md) `internal/msgqueue` queue live and swaps `send_message` from synchronous delivery to **enqueue-and-ack**. The handler now: decodes → `Route` (validate binding + stamp cursor) → `Enqueue(convID, text)` → `replyAck`. The two pre-#721 timeout constants (`sendMessageActivateTimeout`, `sendMessageDeliverTimeout`) and the whole delivery-result switch are gone — no blocking call remains in the handler. The daemon's `msgqueue` drain delivers the backlog one message at a time through the reliable `WriteUserTurn` path, re-resolving the bound session per attempt via the stamp-free `sessionRouter.resolve`, `Activate`ing under `inboundActivateTimeout` (30s), then writing with the **raw lifecycle ctx** (no deliver cap — that block is the drain's turn-end pacing).

**The ack now means "accepted into the backlog," not "delivered."** This is asymmetric by design: at enqueue we have a live phone to tell "retry" (malformed / unknown / unbound all reject synchronously, preserving the "unbound → error, not bootstrap" guarantee above); once enqueued the ack promised delivery, so a transient resolve/activate/write failure is held and retried by the drain rather than surfaced. The wire-level `send_message` request/reply is unchanged ([ADR 025](../decisions/025-mobile-remote-head-interactive-session.md) line 123). See [codebase/721.md](../codebase/721.md) for the full ack-contract table and [features/msgqueue-package.md](msgqueue-package.md) for the drain engine.

**A step wedged between the enqueue and the ack races the drain, not just the clock (#2159).** `EnqueueDelivery` hands the turn to the `msgqueue` drain, which runs on its own goroutine and can Activate the session and have the child raise frames of its own (a permission modal, an assistant delta) while the handler is still doing something else before `replyAck`. #2159 put its auto-naming step (a registry `Update`, an eager `Save`, a `conversation_updated` fan-out — about 4 ms) in exactly that gap, and two stream-json e2e specs went red: the phone received `modal_shown` *ahead of* the `ack` it was still awaiting, because the drain-delivered turn provoked the modal inside that window. Both failures looked like a hung daemon (a 20s wait, no further log lines) rather than a reordering — a SIGQUIT dump showed everything correctly parked, with nothing blocked on the registry. The daemon was simply racing its own drain. The rule this sets for any future post-accept step in this handler: acceptance is established by the enqueue, so `replyAck` is the deadline — anything with its own latency (disk I/O, a broadcast, a second registry mutation) runs *after* the ack, not before it, on this handler and on any other that hands work to an asynchronous drain before replying. See [`specs/architecture/2159-first-message-names-the-chat.md`](../../specs/architecture/2159-first-message-names-the-chat.md)'s `## Revisions` entry for the full diagnosis and `TestSendMessage_AcksBeforeAutoNaming` for the regression pin.
