# Conversation → session binding

How each phone-created discussion gets its own dedicated, isolated claude session. Two halves tie `internal/conversations` (the `Conversation.CurrentSessionID` binding field) to `internal/sessions` (the `Pool` that mints and supervises sessions):

- **Create path (#677, workdir #685, scratch creation #696)** — `create_conversation` eagerly mints + binds a session, recording it on `CurrentSessionID`, and (since #685) spawns it in the conversation's own validated, trust-marked `Cwd` so discussions targeting different projects are isolated on disk. #696 lets the phone's default `~/.pyrycode/scratch` resolve under `$HOME` and be created before spawn (expand leading `~`, `MkdirAll` after the `$HOME` check).
- **Routing path (#678)** — `send_message` resolves that bound session and delivers the inbound turn there instead of to the bootstrap.
- **Rotation maintenance (#739)** — when a bound session is re-keyed by a `/clear` rotation (old id → new id), the owning conversation's `CurrentSessionID` is re-pointed at the new id and the retired id is appended to `SessionHistory`, so the binding stays current beyond the session's first rotation. Eviction is binding-neutral. See [§ Maintaining the binding across rotation](#maintaining-the-binding-across-rotation-739).

The create + routing halves land in the `internal/relay/handlers` package; the rotation maintenance lands in `internal/sessions` + `internal/conversations`. Foundational + consumer slices of EPIC #672 ("per-conversation sessions"). See [ADR 025](../decisions/025-mobile-remote-head-interactive-session.md), [`docs/multi-session.md`](../../multi-session.md).

## What it does and why

Before #677 there was exactly one supervised claude — the bootstrap session — and `create_conversation` only wrote a registry row, leaving `CurrentSessionID` empty. Every discussion therefore shared the single bootstrap claude: a turn in one discussion could disturb another. #677 wires the create path onto the existing `sessions.Pool` so **each conversation mints and binds its own dedicated session**, recorded via the existing `Conversation.CurrentSessionID` field. #678 then makes `send_message` actually *route* to that bound session, so inbound turns are now isolated per discussion (until then, the binding existed but every turn still went to the bootstrap). See [§ Routing](#routing-send_message-consumes-the-binding).

## How it works

### Eager bind at create time

When the daemon handles a `create_conversation` frame, the handler mints a session **before** recording the registry row:

1. Decode payload, resolve `cwd` / `name` / `promoted` (server defaults for null fields).
2. `id, err := conversations.NewID()` — server-minted conversation id (crypto/rand UUIDv4).
3. **Mint the session:** `creator.Create(mintCtx, string(id), spawnDir)` where `mintCtx` is a 30s timeout context and `spawnDir` is the conversation's validated spawn workdir — empty for a default `Cwd`, the trust-marked realpath for a set `Cwd` (#685; see [§ `Cwd` is the validated, trust-marked spawn workdir](#cwd-is-the-validated-trust-marked-spawn-workdir-685)). `Pool.CreateIn` mints a session UUID → registers + persists it in the sessions registry → supervises → activates (spawns claude). Returns the new `SessionID`.
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
    // spawnDir == "" → the daemon's shared trusted workdir (default, unchanged).
    // A non-empty spawnDir is the phone's raw requested Cwd, validated +
    // trust-marked by the impl before spawning (#685); an escape wraps
    // ErrSpawnDirRejected.
    Create(ctx context.Context, label, spawnDir string) (string, error)
}
```

`*sessions.Pool.CreateIn` returns `(sessions.SessionID, error)`, not `(string, error)`, so it does **not** satisfy this directly. It is adapted at the `cmd/pyry` boundary — the only package that knows both `*sessions.Pool` and `handlers.SessionCreator` — by a thin wrapper mirroring the existing `poolResolver`, which **also owns the cmd-layer validation** of the phone-requested spawn workdir:

```go
// cmd/pyry/main.go
type sessionMinter struct{ p *sessions.Pool }
func (m sessionMinter) Create(ctx context.Context, label, spawnDir string) (string, error) {
    resolved, err := resolveSpawnDir(spawnDir)   // confine to $HOME + trust-mark (#685)
    if err != nil {
        return "", err
    }
    id, err := m.p.CreateIn(ctx, label, resolved)
    return string(id), err
}
```

`sessionMinter{pool}` is threaded through `startRelay` → `startRelayV2` and into both `handlers.CreateConversation(...)` registration sites (the v1 dispatcher and the v2 manager handler map). Result: `internal/relay/handlers` stays free of any `internal/sessions` import — the cycle-free property is preserved, and the cmd-layer adapter is the sole validator of the spawn workdir (see [§ `Cwd` is the validated, trust-marked spawn workdir](#cwd-is-the-validated-trust-marked-spawn-workdir-685)).

### `Cwd` is the validated, trust-marked spawn workdir (#685)

Through #677, the session spawned in the daemon's **shared** trusted workdir and the phone-influenced `conversation.Cwd` was inert stored metadata — *structurally* excluded from the spawn path. **#685 reverses that deferral:** the conversation's `Cwd` is now a validated spawn input, so a discussion's claude runs in its own recorded directory and discussions targeting different projects are isolated on disk.

Because `Cwd` is phone-influenced, it is an **untrusted spawn input** — validated with the *same* posture as the daemon's own bootstrap workdir before it is used:

1. The handler reads the **raw nullable** `p.Cwd` into `spawnDir` (`null → ""`, set → the raw requested path) — kept separate from the defaulted `cwd` that feeds the recorded row + reply. So "where to spawn" (`spawnDir`) and "what to record" (`cwd`) stay distinct: a default conversation records `defaultCwd` yet spawns in `tpl.WorkDir`, byte-identical to today (**AC#4**).
2. The cmd-layer adapter's `resolveSpawnDir` (`cmd/pyry/main.go`, sibling of `confineWorkdirToHome`) is the **sole validator**, mirroring the bootstrap's own `confine → trust → spawn-in-realpath` sequence:
   - `""` → `("", nil)`: `Pool.CreateIn` falls back to the shared `tpl.WorkDir`; `trustMark` is **not** called.
   - set → `expandTilde` (a leading `~`/`~/` → the daemon's `$HOME`; `#696`) → `confineWorkdirToHomeCreating` (canonicalise *both* the candidate and `$HOME` via `EvalSymlinks`, confine to `$HOME`, **create the dir if missing**; `#696`) → `trustMark(realpath)` → return **trustMark's** realpath. Order is load-bearing — `trustMark` has no `$HOME` bound, so confining first is what keeps an out-of-`$HOME` path from being auto-trusted. Returning trustMark's own value (not a re-derived path) makes claude's cwd and the trust-marked path byte-identical, so the spawned claude does not wedge on the first-run workspace-trust modal (**AC#3**).
3. A `Cwd` that escapes `$HOME` after symlink resolution (including via a symlink under `$HOME` pointing outside, or a symlinked *ancestor* of a not-yet-existing path; `#696`) is **rejected**: `resolveSpawnDir` wraps `handlers.ErrSpawnDirRejected`, and the handler maps it to a **non-retryable** `protocol.malformed` reply (static message, no path echoed). The handler returns **before** `reg.Create`, so an escape never leaves a half-bound conversation row (**AC#2**). A transient `trustMark` write failure is returned *plain* (no sentinel) → retryable `server.binary_offline`.

**Default-scratch creation (#696).** A phone cannot know the daemon's absolute home, so it sends the default `Cwd` as `~/.pyrycode/scratch` meaning "the daemon's home", and a first-time host has no such dir. #696 makes that resolve and be created before spawn — **narrowly reversing #685's then-correct "a non-existent requested path is rejected" posture, for the phone path and the default-scratch case only**: `expandTilde` (leading `~`/`~/` only — no `$VAR`, no `~user`) anchors the path at the real `$HOME`, and the create-aware `confineWorkdirToHomeCreating` canonicalises the **longest existing ancestor** (probed with `os.Lstat`, so a symlinked ancestor is resolved, not stepped over), runs the `$HOME` containment check on that resolved candidate **before** `os.MkdirAll(..., 0o700)`, then re-confines the created realpath. Creation is therefore `$HOME`-gated: a symlinked-ancestor escape (`~/link -> /tmp/evil`) is rejected and **never created**. When the path already exists this reduces exactly to `confineWorkdirToHome`, so #685's happy path is byte-identical (**AC#4**). `confineWorkdirToHome` itself and the daemon-startup caller (`runSupervisor`) are **unchanged** — the operator's `-pyry-workdir` is shell-expanded and must still pre-exist. See [codebase/696.md](../codebase/696.md).

`internal/relay/handlers` still does **no** path handling — it forwards the raw value through the typed `SessionCreator` seam and maps the sentinel; the canonicalise + confine + trust all live at the cmd layer. `Pool.CreateIn` ([#684](sessions-package.md#per-session-spawn-workdir-createin--getorcreatein-684)) uses the resolved realpath verbatim (it does not re-validate — by contract the caller hands it a pre-resolved realpath). See [codebase/685.md](../codebase/685.md).

> **Residual TOCTOU window (accepted).** Between the confine-time `EvalSymlinks` and claude's eventual `chdir`, a path that resolved inside `$HOME` could be swapped to an escaping symlink — the *same* window the daemon's own bootstrap workdir already accepts, requiring control of the operator's home to win. Neither #685 nor #696 widens it: #696's `MkdirAll` runs only after the pre-creation containment check passes, and the created realpath is re-confined before trust+spawn. Closing it fully (`openat2`/`RESOLVE_BENEATH`) is out of scope and unobserved.

### Mint-failure and timeout behaviour

The mint is bounded by a 30s timeout (`createConversationMintTimeout`, matching the drain's `inboundActivateTimeout` — #721's successor to the removed `sendMessageActivateTimeout` — and control's session-create budget). `Pool.Activate` blocks until claude's PTY is ready (~2–15s) or ctx-cancel; the bound turns a wedged spawn into a retryable error instead of pinning the per-conn goroutine.

Any mint error (pool not running, activate timeout, in-pool save failure, ctx deadline) fails the whole create: the handler logs at `Warn` (`create_conversation.session_mint_failed`, fields `conn_id` / `conversation_id` / wrapped `err`) and replies `protocol.CodeServerBinaryOffline` **retryable**, returning **before** `reg.Create` — so there is no half-bound orphan conversation row. The phone retries onto a fresh conversation + session.

## Routing: `send_message` consumes the binding

#678 is the consumer half. Where the create path *writes* `CurrentSessionID`, `send_message` *reads* it to select the session a turn is delivered to. Before #678 the handler held a single `TurnWriter` (the bootstrap session) and routed every turn there regardless of `ConversationID`; now it resolves the frame's conversation to its bound session. Since [#721](../codebase/721.md) the handler no longer *delivers* synchronously — it validates the binding, **enqueues**, and acks; the daemon's `msgqueue` drain runs Activate-before-write against *that* session asynchronously (see [§ Enqueue-and-ack (#721)](#enqueue-and-ack-721)).

### The `SessionRouter` seam (mirrors `SessionCreator`)

The handler depends on a second consumer-declared interface that *returns* the existing `TurnWriter`, so `handlers/` still imports no `internal/sessions`:

```go
// internal/relay/handlers/send_message.go
type SessionRouter interface {
    Route(conversationID string) (TurnWriter, error)
}
```

`Route` is **ctx-free** — a pure in-memory lookup (registry read + field check + pool lookup), non-blocking, no cancellation surface. Since #721 the handler **discards** the returned writer (it only validates the binding before enqueue); the blocking work (`Activate`, `WriteUserTurn`) happens on the `msgqueue` drain, which re-resolves the writer per attempt via `sessionRouter.resolve` — see [§ Enqueue-and-ack (#721)](#enqueue-and-ack-721).

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
    if err != nil {
        return nil, err                                        // ErrSessionNotFound → server.binary_offline
    }
    return boundSession{pool: r.pool, sess: sess, id: id}, nil
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
- **`boundSession.Activate` funnels through `Pool.Activate`, not `Session.Activate`.** `*sessions.Session` already satisfies `TurnWriter` directly; the `boundSession` wrapper exists *only* to redirect `Activate` through the cap-enforcing `Pool.Activate(ctx, id)`. The bootstrap was special (always active, never cap-evicted) so it could use `Session.Activate`; per-conversation sessions are full `ActiveCap` citizens — activating one may LRU-evict a peer, which only happens inside `Pool.Activate`. Bypassing it would break the invariant the idle-evict follow-up (#680) relies on. An idle-evicted bound session therefore re-activates on the next **drain delivery attempt** for that conversation (since #721; before #721, on the next `send_message`) — the [idle-eviction.md](idle-eviction.md) lazy-respawn contract, now per-conversation.

### Error mapping (no new wire code)

| Case | Detected in | Reply | Retryable |
|---|---|---|---|
| Unknown `ConversationID` | `Route`: `Registry.Get` miss | `conversation.not_found` | no |
| No bound session (`CurrentSessionID == ""`) | `Route`: empty-id guard | `server.binary_offline` | yes |
| Bound id not in pool (`ErrSessionNotFound`) | `Route`: `Pool.Lookup` | `server.binary_offline` | yes |

All three are checked synchronously **before enqueue** (#721): the handler's only wire replies are these rejects + the ack. `errNoBoundSession` is an unexported sentinel with no wire surface. A conversation that becomes unbound/deleted *after* the ack (a TOCTOU window) no longer maps to a wire reply — the drain's `resolve`/`WriteUserTurn` error is **absorbed and retried** (the ack already promised delivery). There is no conversation-delete verb today, so a permanent post-ack unbind is currently unreachable; the daemon-restart boundary bounds it.

### Enqueue-and-ack (#721)

[#721](../codebase/721.md) makes the [#704](../codebase/704.md) `internal/msgqueue` queue live and swaps `send_message` from synchronous delivery to **enqueue-and-ack**. The handler now: decodes → `Route` (validate binding + stamp cursor) → `Enqueue(convID, text)` → `replyAck`. The two pre-#721 timeout constants (`sendMessageActivateTimeout`, `sendMessageDeliverTimeout`) and the whole delivery-result switch are gone — no blocking call remains in the handler. The daemon's `msgqueue` drain delivers the backlog one message at a time through the reliable `WriteUserTurn` path, re-resolving the bound session per attempt via the stamp-free `sessionRouter.resolve`, `Activate`ing under `inboundActivateTimeout` (30s), then writing with the **raw lifecycle ctx** (no deliver cap — that block is the drain's turn-end pacing).

**The ack now means "accepted into the backlog," not "delivered."** This is asymmetric by design: at enqueue we have a live phone to tell "retry" (malformed / unknown / unbound all reject synchronously, preserving the "unbound → error, not bootstrap" guarantee above); once enqueued the ack promised delivery, so a transient resolve/activate/write failure is held and retried by the drain rather than surfaced. The wire-level `send_message` request/reply is unchanged ([ADR 025](../decisions/025-mobile-remote-head-interactive-session.md) line 123). See [codebase/721.md](../codebase/721.md) for the full ack-contract table and [features/msgqueue-package.md](msgqueue-package.md) for the drain engine.

## Maintaining the binding across rotation (#739)

The create path writes `CurrentSessionID` **once**, at conversation creation, then froze it. But a bound session's id is not stable for life: a `/clear` rotation re-keys it in place (old id → new id; `Pool.RotateID`, driven by the [rotation watcher](rotation-watcher.md)). Through #738 the registry was never told, so after the **first** rotation the conversation still pointed at the retired id — the binding was stale, the documented `SessionHistory` trail did not exist, and a reverse lookup (session id → owning conversation) silently missed. #739 implements the **write side** of that maintenance so the downstream consumer #741 (the read side) resolves against a correct binding.

### The rebind, driven from the transition chokepoint

`notifyTransition` (`internal/sessions/transition.go`) is the off-lock chokepoint **both** transition reasons pass through. #739 adds a reason-branch that rebinds **before** the observer fan-out:

```go
func (p *Pool) notifyTransition(t SessionTransition) {
    if t.Reason == ReasonClear {
        p.rebindConversation(t.PreviousID, t.NewID)   // /clear only
    }
    if p.transitionObserver != nil {
        p.transitionObserver(t)                        // #657/#659 emitter; #741 reads the binding later
    }
}
```

`Pool.rebindConversation` calls the new `conversations.Registry.RebindSession(oldID, newID)` write primitive (scan by `CurrentSessionID == oldID` under `r.mu`, set `CurrentSessionID = newID`, `append(SessionHistory, oldID)`, first-match-and-stop — see [conversations-registry.md](conversations-registry.md#rebindsessionoldid-newid-string-bool-739)) and, **only on a hit**, persists `conversations.json` via the registry's atomic `Save`. It is a no-op when no registry is wired (`p.convReg == nil`, the case for test pools) or when no conversation owns the rotated id (AC#4 — `Save` skipped, file mtime stable). A `Save` error is logged at `Warn` and swallowed: the in-memory rebind is already applied and usable, so durability is best-effort (matching `create_conversation`'s eager persist and `RotateID`'s non-fatal save). #739 is the first production caller to **write** `SessionHistory` — it was a documented-but-unwritten field until now.

Data flow on a `/clear` rotation:

```
rotation watcher → onRotate(old,new)
    → RotateID(old,new)                      [Pool.mu held: map re-key + sessions.json save]
    → notifyTransition({Clear, old, new})    [no pool locks held]
        → rebindConversation(old,new)        [ReasonClear branch]
            → convReg.RebindSession(old,new) [conversations.Registry.mu: scan + mutate]
            → convReg.Save(path)             [on hit only; atomic temp+fsync+rename]
        → transitionObserver({Clear,old,new})[#741 resolves session→conversation later, against the CURRENT binding]
```

**Why drive from `notifyTransition`, not a new observer.** The transition signal has a **single** observer slot (`SetTransitionObserver`, "set once"), already owned by the `session_transition_v2.go` producer (installed before `Pool.Run`) — a second observer is impossible. Placing the rebind at the common chokepoint, rebind *then* observer, makes the #741 ordering **structural**: the in-memory rebind (and its `Save`) complete synchronously before the observer hands the signal to its buffered channel, so #741 never resolves against a half-applied binding.

### Eviction is binding-neutral, by construction

`ReasonEviction` (idle timeout or cap-policy) fires with `PreviousID = s.id` and an **empty `NewID`**: the session keeps its id, stays in the pool map, and re-activates later under the *same* id (the [idle-eviction](idle-eviction.md) "evicted is a state, not removal" contract). So `CurrentSessionID` stays valid across eviction with **no write needed**. Because `runActive` returns only `ReasonEviction`/`""` (never `ReasonClear`), an eviction never enters the rebind branch — binding-neutrality (AC#2) holds by control flow, not by a runtime guard. This matters: a naive "set `CurrentSessionID = NewID`, append `PreviousID`" applied to the empty-`NewID` eviction signal would **clear** the binding (breaking the `send_message` respawn guard at `main.go:923`) and append a colliding duplicate of the current id. The reason-branch is the real defense against that corruption; a second `oldID == ""` guard inside `RebindSession` defends the *unrelated* stray-empty-call case (it would **not** catch a mis-routed eviction, which carries a non-empty `PreviousID`).

### Confidentiality (security-sensitive)

The binding maintained here is the attribution a downstream mobile-facing consumer (#741) uses to route a session-boundary event to a conversation, so a mis-write is a cross-conversation leak that **originates here**. The rebind is driven **entirely by server-internal state** — the daemon's own rotation watcher and pool-managed session UUIDs; no untrusted phone input flows in. Mis-attribution is prevented by deterministic byte-exact first-match (each session id binds exactly one conversation), and the persisted file is updated atomically (mutate exactly the matched row → whole-snapshot temp+fsync+rename, no torn write). The only log line is a `Save`-failure `Warn` carrying non-secret session-id routing fields. Spec verdict: **PASS**. See [codebase/739.md](../codebase/739.md).

## Edge cases & limitations

- **Inbound + the structured outbound stream now route per-conversation (#678 → #687 → #679).** When #678 landed, claude's *replies* still fanned out from the bootstrap session — and worse, the structured interactive turn stream read its conversation cursor from the bootstrap supervisor, which #678 leaves empty for routed turns, so the structured reply stream went **silent** after the first per-conversation route. [#687](../codebase/687.md) closed the first half — *attribution*: a `cmd/pyry` *active-conversation* signal (`activeConversation`, stamped by `sessionRouter.Route` on success) re-keys the structured stream's two cursor readers (live emitter + #647 reconnect-replay) to it, so the stream **emits again** and each envelope carries the routed conversation's `conversation_id`. [#679](../codebase/679.md) closes the second half — *content*: the producer now **follows the active conversation**, tailing the bound session's transcript **by bound session id** (`resolveBoundSessionJSONL`, mtime-independent) over the bound session's own supervisor, and re-subscribing when the active conversation (or its session) changes. So a *different* session writing more recently can no longer cross-stream its output into the active conversation's reply — the cross-conversation confidentiality property is now enforced, not merely coincidental in the single-operator case. [#686](../codebase/686.md) then re-points that by-id resolver at the conversation's **own per-`Cwd` JSONL directory** (`~/.claude/projects/<encoded-cwd>/`, derived from the bound session's captured spawn `WorkDir`), since #685 spawns per-conversation sessions in distinct directories — so the filename (#679) *and* the directory (#686) are both per-conversation; a default null-`Cwd` session keeps resolving from the shared dir unchanged. The **coarse** v1 bridge (`assistant_turn.go`, the non-interactive dispatch-leg surface) still reads the bootstrap cursor and is unchanged; the v2 coarse bridge (`assistant_turn_v2.go`) was deleted in [#699](../codebase/699.md). The real-claude e2e confirms the full phone→claude→phone round-trip is intact.
- **Accepted residue — unbound session on a non-empty-id error.** `Pool.Create` can return a non-empty id *with* an error (e.g. the mint persisted, then `Activate` timed out; the lifecycle goroutine may still bring the session up against the pool ctx after the handler's timeout fires). The handler treats *any* error as a clean mint failure and does not bind it, so such a session is left registered in the Pool with no conversation pointing at it. This is benign — the same shape as a session that ran and idled out, recoverable by the Pool's own lifecycle — and the race is unobserved, so per evidence-based fix selection no cleanup logic was added.
- **Process-exhaustion / spawn amplification (deferred).** Eager binding makes `create_conversation` a process-spawning operation; an authenticated phone spamming creates can exhaust host processes/memory. The existing in-architecture bound is `Pool.ActiveCap` (LRU-evicts a victim when the cap is hit) — but it **defaults to uncapped** (`-pyry-active-cap 0`). A dedicated per-operator create quota / rate-limit is new dispatch policy and is a named #672-family follow-up. Ops mitigation today: set `-pyry-active-cap` and/or `-pyry-idle-timeout`.
- **`ActiveCap` churn.** When `ActiveCap` *is* set, each `create_conversation` activation can LRU-evict another conversation's live claude. Acceptable: eviction preserves the on-disk JSONL and the session re-activates on the next `send_message`. This cross-discussion cap eviction (and the no-bleed guarantee that only the deliberate LRU victim transitions) is pinned by [#680](../codebase/680.md)'s binary-boundary e2e — the slice that closes Phase 2.0 by proving per-conversation sessions are full citizens of the idle-evict / cap machinery.
- **Restart scope.** Only the `CurrentSessionID` *field* round-trips on registry reload. Reviving / re-binding the live claude process across a daemon restart is the Pool's existing session-lifecycle / startup-reconciliation concern, out of scope here.

## Deferred to follow-ups (EPIC #672)

- **Distinct per-conversation working directory — done (#685), default scratch created (#696), and the bridge follows it — done (#686).** `conversation.Cwd` is now validated (confine to `$HOME` + trust-mark realpath) and used as the bound session's spawn workdir; the phone's default `~/.pyrycode/scratch` resolves under `$HOME` and is created before spawn ([#696](../codebase/696.md)); see [§ `Cwd` is the validated, trust-marked spawn workdir](#cwd-is-the-validated-trust-marked-spawn-workdir-685). [#686](../codebase/686.md) closed the strand: the outbound reply stream's by-id resolver now reads from the conversation's own per-`Cwd` JSONL directory (default sessions unchanged). Still open from this strand: a dedicated `conversation.cwd_rejected` error code (reused `protocol.malformed` for now).
- **Per-operator create quota / rate-limit** (dispatch policy).
- **Per-conversation outbound routing — structured stream done.** All three pieces landed: the structured stream's attribution follows the active conversation ([#687](../codebase/687.md)), its reply **content** follows the bound session's transcript by id ([#679](../codebase/679.md)), and that transcript is resolved from the conversation's own per-`Cwd` directory ([#686](../codebase/686.md)). Still open: the surviving **coarse** v1 bridge (`assistant_turn.go`) still fans out from the bootstrap cursor — re-keying it is a further #672-family follow-up (the v2 coarse bridge was removed in [#699](../codebase/699.md), so only v1 remains). **Multi-operator isolation** (two phones each viewing a *different* conversation concurrently) is also deferred — the structured stream fans out to all interactive conns by capability with no connection→conversation binding for output; #679 covers only the *single* active reply stream following the operator's current conversation.

## Related

- [conversations-package.md](conversations-package.md) — the `Conversation.CurrentSessionID` binding field (and `SessionHistory`, written in production for the first time by #739's rotation rebind).
- [conversations-registry.md](conversations-registry.md) — atomic Save/Load that round-trips the binding (AC#3); the `RebindSession` write primitive (#739).
- [rotation-watcher.md](rotation-watcher.md) — live `/clear` detection → `Pool.RotateID`, the re-key that precedes the #739 rebind.
- [sessions-package.md](sessions-package.md) — `Pool.Create` mint primitive (§ *Pool.Create*) and `buildSession` (the `tpl.WorkDir` / `--session-id`-only spawn point).
- [idle-eviction.md](idle-eviction.md) — "evicted is a state, not removal"; lazy respawn on next `send_message`, now per-conversation via `Pool.Activate`.
- [relay-package.md](relay-package.md) — the `create_conversation` / `send_message` handlers and the `SessionCreator` / `SessionRouter` seams alongside `TurnWriter`.
- [codebase/677.md](../codebase/677.md), [codebase/678.md](../codebase/678.md) — per-ticket implementation notes (create + routing halves).
- [codebase/739.md](../codebase/739.md) — per-ticket note for the rotation-maintenance half (`RebindSession` + the `notifyTransition` reason-branch).
- [codebase/680.md](../codebase/680.md) — Phase 2.0 capstone: e2e proving per-conversation sessions idle-evict, reactivate, and obey the active cap without cross-bleed.
- [codebase/687.md](../codebase/687.md), [codebase/679.md](../codebase/679.md), [codebase/686.md](../codebase/686.md) — the outbound structured-stream migration (attribution + content + per-`Cwd` directory).
- [turnbridge-package.md](turnbridge-package.md) — the producer / follow-active subscriber (`NewTargetSubscriber`) #679 re-keys.
- [ADR 025](../decisions/025-mobile-remote-head-interactive-session.md) — mobile remote-head interactive session.
