# Conversation → session binding

How each phone-created discussion gets its own dedicated, isolated claude session. Two halves tie `internal/conversations` (the `Conversation.CurrentSessionID` binding field) to `internal/sessions` (the `Pool` that mints and supervises sessions):

- **Create path (#677, workdir #685, scratch creation #696, deferred spawn #2085, recorded-cwd drift #2568)** — `create_conversation` eagerly mints + binds a session, recording it on `CurrentSessionID`, in the conversation's own validated, trust-marked `Cwd` (#685) so discussions targeting different projects are isolated on disk. #696 lets the phone's default `~/.pyrycode/scratch` resolve under `$HOME` and be created before spawn (expand leading `~`, `MkdirAll` after the `$HOME` check). Since #2085 the mint registers, persists and supervises the session but does **not** spawn claude — the child comes up on the conversation's first message, on exactly the lazy-respawn path an idle-evicted session already takes. #2568 records the resolved realpath the session actually spawns in, not the client's spelling of it, and rewrites rows recorded before that fix once at startup. See [§ Spawn deferred to the first message](#spawn-deferred-to-the-first-message-2085), [§ One-time startup normalisation](#one-time-startup-normalisation-of-rows-recorded-before-2568).
- **Routing path (#678)** — `send_message` resolves that bound session and delivers the inbound turn there instead of to the bootstrap.
- **Rotation maintenance (#739)** — when a bound session is re-keyed by a `/clear` rotation (old id → new id), the owning conversation's `CurrentSessionID` is re-pointed at the new id and the retired id is appended to `SessionHistory`, so the binding stays current beyond the session's first rotation. Eviction is binding-neutral. See [§ Maintaining the binding across rotation](#maintaining-the-binding-across-rotation-739).
- **Stamping the boundary's routing key (#741)** — the `session_transition` producer reads that maintained binding (session id → owning conversation) and stamps `conversation_id` onto every emitted envelope, so the phone (`pyrycode-mobile#336`) folds the session-boundary marker into the correct thread; an unresolvable binding drops the whole event fail-closed rather than emit a guessed key. See [§ Reading the binding to stamp `conversation_id`](#reading-the-binding-to-stamp-conversation_id-741).

The create + routing halves land in the `internal/relay/handlers` package; the rotation maintenance lands in `internal/sessions` + `internal/conversations`. Foundational + consumer slices of EPIC #672 ("per-conversation sessions"). See [ADR 025](../decisions/025-mobile-remote-head-interactive-session.md), [`docs/multi-session.md`](../../multi-session.md).

## What it does and why

Before #677 there was exactly one supervised claude — the bootstrap session — and `create_conversation` only wrote a registry row, leaving `CurrentSessionID` empty. Every discussion therefore shared the single bootstrap claude: a turn in one discussion could disturb another. #677 wires the create path onto the existing `sessions.Pool` so **each conversation mints and binds its own dedicated session**, recorded via the existing `Conversation.CurrentSessionID` field. #678 then makes `send_message` actually *route* to that bound session, so inbound turns are now isolated per discussion (until then, the binding existed but every turn still went to the bootstrap). See [§ Routing](#routing-send_message-consumes-the-binding).

## How it works

### Eager bind at create time

When the daemon handles a `create_conversation` frame, the handler mints a session **before** recording the registry row:

1. Decode payload, resolve `name` / `promoted` (server defaults for null fields). `cwd` — what gets **recorded** — is not settled yet; it depends on the mint's answer (step 3).
2. `id, err := conversations.NewID()` — server-minted conversation id (crypto/rand UUIDv4).
3. **Mint the session:** `creator.Create(ctx, string(id), spawnDir)` where `spawnDir` is the *raw* phone-requested `p.Cwd` (empty for a null `Cwd`). `Pool.Mint` mints a session UUID → registers + persists it in the sessions registry → supervises. Since [#2085](#spawn-deferred-to-the-first-message-2085) it does **not** activate — no claude spawns here. Returns `(sessionID, dir, err)`: `dir` is the validator's answer — empty for a default `Cwd`, the confined, trust-marked realpath for a set one (#685; see [§ `Cwd` is the validated, trust-marked spawn workdir](#cwd-is-the-validated-trust-marked-spawn-workdir-685)) — and is what step 4 records, not the raw `spawnDir` (#2568).
4. `cwd := defaultCwd; if p.Cwd != nil { cwd = dir }` — a null request still records the daemon's default; a set one records `creator.Create`'s resolved answer, never the client's spelling, so `default`, `~/x/default` and its absolute form all record the one folder they spawn in rather than three sidebar workspaces for it (#2568). `reg.Create(Conversation{ID, Name, Cwd: cwd, CurrentSessionID: sessionID, IsPromoted, LastUsedAt})` — the bound session id is populated on the row.
5. `reg.Save(registryPath)` — eager persist (the field round-trips through the registry's atomic Save/Load, so the binding survives a daemon restart).
6. Reply `conversation_created`. The wire reply is **unchanged** — it carries no session field; the binding is internal state surfaced only in the registry row.

The bind is **eager**; the spawn is not — that distinction is the entire point of #2085 (see below). AC#1 forces the bind: the registry row must carry a non-empty `CurrentSessionID` referring to a pool session *immediately after the create frame is handled*, which is what lets `request_session_settings` / `set_session_settings` resolve on a conversation nobody has messaged yet ([pyrycode-desktop#1054](https://github.com/pyrycode/pyrycode-desktop/issues/1054)). What #2085 moved is only the activate: `Pool.Mint` is the register-and-supervise half of what `Pool.Create`/`CreateIn` used to do in one call, and `CreateIn` is now `Mint` + `Activate` — so `internal/control`'s `sessions.new` verb, the one caller that should still bring a session straight up, is unaffected.

> "Exists in the Pool" means the session has a registry entry + a `p.sessions` map entry. That holds even after idle-eviction later moves the process to disk — **evicted is a lifecycle state, not removal**. A freshly minted, never-messaged session is byte-for-byte in that same state: the binding is durable, and readable/writable, whether or not a claude process is currently running.

### Spawn deferred to the first message (#2085)

Through #2085, `create_conversation` minted a session **and spawned claude for it** in the same call, so every per-session setting (model, effort, permission mode) had to be applied to a child already running: a change went in as an in-band slash command, and clearing one back to claude's own default couldn't be expressed in-band at all — the first turn of a new conversation always ran at whatever it was minted with, with a visible correction after the fact. #2085 moves the spawn off the create path entirely. `Pool.Mint(label, spawnDir string) (SessionID, error)` is `CreateIn`'s body minus the final `Activate` — ctx-free, mirroring `Pool.Revive`, because it does no blocking work. The child comes up on the conversation's **first message**, through the same lazy-respawn path an idle-evicted session already takes on its next `send_message` (see [§ Two load-bearing invariants](#two-load-bearing-invariants)): the msgqueue drain resolves the bound session and calls the cap-enforcing `Pool.Activate` itself. No new lifecycle path was added.

Consequences:

- **Settings compose into the launch instead of correcting it afterwards.** A model/effort/permission-mode set before the first message is simply what the child launches with, via the launch-flag path that already omits a flag for an empty (default) value. `Pool.UpdateSettings`'s evicted-session case — "the argv install alone applies on the next Activate, and the caller still sees success" — already covered this; #2085 just made that the ordinary case for a new conversation rather than a corner one reached only after eviction.
- **The rotation skip-set prime moved with the spawn.** `RegisterAllocatedUUID`'s entry has a 30s `allocatedTTL`, primed so claude's CREATE of `<uuid>.jsonl` isn't read by the rotation watcher as a `/clear`. Priming at mint time and spawning arbitrarily later — whenever the operator sends — would let the entry expire before the child ever opens the transcript, so the watcher would rotate a brand-new session on its very first turn. The prime now happens inside `Pool.Activate`, the pool's single spawn entry, immediately before the actual `Session.Activate` call: one site covers every deferred spawn (first message, idle reactivation, `GetOrCreateIn`). Re-priming an already-active session is harmless — `IsAllocated` consumes on first hit and `pruneAllocatedLocked` drops the rest at TTL.
- **Process-exhaustion residue got cheaper, not worse** — see [§ Process-exhaustion / spawn amplification](#deferred-to-follow-ups-epic-672) below.
- **The validated-then-spawn window widens from milliseconds to unbounded — accepted.** [§ Residual TOCTOU window](#cwd-is-the-validated-trust-marked-spawn-workdir-685) above already accepts a symlink-swap race between `resolveSpawnDir`'s `EvalSymlinks` and claude's `chdir`; #2085 widens *when* that race can be won, not *what* winning it takes. The frozen value is `trustMark`'s own realpath, held on the `*Session` with no disk re-read between check and spawn, so winning still requires write access to the operator's `$HOME` — the same requirement, for longer. This differs from #1487's revive, which re-validates at its own spawn site because it re-reads a **raw, persisted** `conv.Cwd` from a mutable file **across a daemon restart** — unvalidated bytes from a previous process lifetime, not an in-process frozen value.

### The `SessionCreator` seam (keeps `handlers/` import-clean)

The handler depends on a narrow consumer-declared interface, mirroring the sibling `TurnWriter`:

```go
// internal/relay/handlers/create_conversation.go
type SessionCreator interface {
    // spawnDir == "" → the daemon's shared trusted workdir (default, unchanged).
    // A non-empty spawnDir is the phone's raw requested Cwd, validated +
    // trust-marked by the impl before the mint (#685); an escape wraps
    // ErrSpawnDirRejected. Mints and binds only — the child comes up on the
    // conversation's first message, not here (#2085). dir is the impl's
    // resolved answer — "" for an empty spawnDir, the confined, trust-marked
    // realpath otherwise — and is what the handler records as the
    // conversation's Cwd for a set request (#2568): one resolver on the
    // create path, so the recorded path and the spawn dir cannot disagree.
    Create(ctx context.Context, label, spawnDir string) (sessionID, dir string, err error)
}
```

`*sessions.Pool.CreateIn` returns `(sessions.SessionID, error)`, not `(string, error)`, so it does **not** satisfy this directly. It is adapted at the `cmd/pyry` boundary — the only package that knows both `*sessions.Pool` and `handlers.SessionCreator` — by a thin wrapper mirroring the existing `poolResolver`, which **also owns the cmd-layer validation** of the phone-requested spawn workdir:

```go
// cmd/pyry/main.go
type sessionMinter struct{ p *sessions.Pool }
// ctx is unused since #2085: Pool.Mint is ctx-free — it spawns nothing, so
// there is nothing left here to cancel or time out.
func (m sessionMinter) Create(_ context.Context, label, spawnDir string) (string, string, error) {
    resolved, err := resolveSpawnDir(spawnDir)   // confine to $HOME + trust-mark (#685)
    if err != nil {
        return "", "", err
    }
    id, err := m.p.Mint(label, resolved)
    return string(id), resolved, err   // resolved is what #2568 records, not spawnDir
}
```

`sessionMinter{pool}` is threaded through `startRelay` → `startRelayV2` and into both `handlers.CreateConversation(...)` registration sites (the v1 dispatcher and the v2 manager handler map). Result: `internal/relay/handlers` stays free of any `internal/sessions` import — the cycle-free property is preserved, and the cmd-layer adapter is the sole validator of the spawn workdir (see [§ `Cwd` is the validated, trust-marked spawn workdir](#cwd-is-the-validated-trust-marked-spawn-workdir-685)).

**Any new caller for whom an empty spawn dir is not a legitimate request must guard the empty string itself.** `resolveSpawnDir("")` is fail-open by contract — `("", nil)`, success, no confinement, no trust-mark — because that is exactly what the phone's optional `Cwd` needs. A caller with no such optional-input meaning (`internal/control`'s `channel.new` verb, #2155, is the first) cannot rely on `resolveSpawnDir` to reject an empty string on its behalf; it has to check before calling in. See [control-plane.md § Channel: new verb](control-plane.md#channel-new-verb-channelnew-2155).

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

### One-time startup normalisation of rows recorded before #2568

Before #2568, `create_conversation` recorded the client's raw `Cwd` string, not the realpath it spawned in — so a row created as `default`, one as `~/pyry-workspace/default` and one as the absolute form were three sidebar workspaces for one folder, and `pyry channel new` / `change_workspace` (which already recorded the realpath) disagreed with create's own rows. `normaliseLegacyCwds` (`cmd/pyry/conversation_cwd_normalise.go`), called from `runSupervisor` right after `conversations.Load` and the logger are both available, rewrites the drift once per startup, before anything else reads the registry:

- Collects every distinct stored `Cwd` that is **non-empty and not `filepath.IsAbs`** — relative or `~`-prefixed. An absolute row is never touched, resolvable or not; an empty `Cwd` names no folder anyone requested.
- Resolves each through `resolveWorkspaceDir` — the **strict** confiner (tilde expansion + `confineWorkdirToHome`, no `MkdirAll`, no `trustMark`) already used by the `change_workspace` path, not the create-aware, folder-creating `confineWorkdirToHomeCreating`: startup must not create a folder or auto-trust one just because a stale row named it. A relative value resolves against the daemon's process cwd, the same base create resolved it against.
- Applies the successes in one call to `Registry.RekeyCwds(rekey map[string]string) int`, which under one `mu` critical section rewrites every row's `Cwd` and moves each rekeyed `workspace_labels` entry to its new key — **a label already stored under the resolved (usually absolute) key wins**; where two legacy keys collapse into the same unlabelled key, the lexicographically first (sorted) old key's label wins. `RekeyCwds` is the registry's first primitive that touches rows and label keys together in one lock; nothing before #2568 needed to.
- A `Cwd` that fails to resolve (the folder is gone, or it now escapes `$HOME`) is **left exactly as stored** — every later spawn from it re-validates through `resolveSpawnDir` anyway — and logged at `Warn` with the static event `conversations.legacy_cwd_unresolved` plus a `rows` count, **never the path and never the error** (the confine error names the path). If anything was rewritten, `reg.Save` persists it; a save failure logs `conversations.legacy_cwd_save_failed` and the daemon continues on the in-memory rewrite, which the next `Save` picks up. The daemon always starts — this function returns nothing to fail on.

### Mint-failure and timeout behaviour

The mint is still bounded by a 30s timeout (`createConversationMintTimeout`, matching control's session-create budget), but since #2085 that budget is **inert against the current implementation**: `sessionMinter.Create` discards its `ctx`, `resolveSpawnDir` takes no `context.Context`, and `Pool.Mint` is ctx-free by design — none of that stops a wedged filesystem syscall. The constant is kept only as the bound a future ctx-honouring `SessionCreator` would get; it protects nothing today. There is no spawn left to bound at create time at all — that wait moves to the first message, where it lands on the drain's own `inboundActivateTimeout`.

Any mint error (pool not running, in-pool save failure) fails the whole create: the handler logs at `Warn` (`create_conversation.session_mint_failed`, fields `conn_id` / `conversation_id` / wrapped `err`) and replies `protocol.CodeServerBinaryOffline` **retryable**, returning **before** `reg.Create` — so there is no half-bound orphan conversation row. The phone retries onto a fresh conversation + session. A spawn failure on the *first message* is no longer a create-time failure at all — it surfaces on the msgqueue drain's own retry/give-up path, identical to an idle-evicted conversation's reactivation failure today.

## Routing: `send_message` consumes the binding

Split out to [`conversation-session-binding-routing.md`](conversation-session-binding-routing.md): the `SessionRouter` seam (mirrors `SessionCreator`), the two load-bearing invariants (empty-binding guard fires before any `Lookup`; `Activate` funnels through `Pool.Activate`), the error-mapping table, and the enqueue-and-ack contract — including the #2159 lesson that a step wedged between `EnqueueDelivery` and `replyAck` races the drain, not just the clock.

## Maintaining the binding across rotation (#739)

The create path writes `CurrentSessionID` **once**, at conversation creation, then froze it. But a bound session's id is not stable for life: a `/clear` rotation re-keys it in place (old id → new id; `Pool.RotateID`, driven by the [rotation watcher](rotation-watcher.md)). Through #738 the registry was never told, so after the **first** rotation the conversation still pointed at the retired id — the binding was stale, the documented `SessionHistory` trail did not exist, and a reverse lookup (session id → owning conversation) silently missed. #739 implements the **write side** of that maintenance so the downstream consumer #741 (the read side, [§ below](#reading-the-binding-to-stamp-conversation_id-741)) resolves against a correct binding.

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

## Reading the binding to stamp `conversation_id` (#741)

\#739 maintains the binding so a reverse lookup resolves correctly; #741 **is** that reverse lookup, wired into the `session_transition` producer (`cmd/pyry/session_transition_v2.go`). Every emitted `session_transition` envelope now carries the `conversation_id` of the thread the transitioning session belongs to, so `pyrycode-mobile#336` can fold the session-boundary marker into the correct conversation instead of having no routing key. The wire field itself was added by [#740](../codebase/740.md) (it shipped emitting `conversation_id: ""`); #741 fills it.

### The duplicated read scan

`conversationForSession(convReg, sid) (string, bool)` (`cmd/pyry/relay.go`) is the read counterpart to #739's `RebindSession` write. It scans `convReg.List()` and matches a conversation iff `c.CurrentSessionID == sid` **or** `slices.Contains(c.SessionHistory, sid)`, returning `string(c.ID), true` on the first match (the single-owner invariant — a session id binds exactly one conversation for life — makes the first match the only match). An empty `sid` short-circuits to `("", false)`, mirroring `RebindSession`'s empty-`oldID` guard: an unbound conversation carries `CurrentSessionID == ""` and must never be swept in by a stray empty lookup. The registry exposes **no** by-session-id read method, so the scan is duplicated cmd-side rather than extracted into a shared primitive (PROJECT-MEMORY "Resist over-DRY on duplicated registry primitives") — this producer is its only consumer. It is race-safe against a concurrent `RebindSession`: `List()` copies the slice header under the registry mutex, and a concurrent append writes at/above `len` (or reallocates), never overwriting an index the captured `[0,len)` read touches.

### Resolve-and-stamp in `broadcast`, by `payload.NewSessionID`

The resolve happens in the emitter's `broadcast`, **once per transition** before the per-conn fan-out, against `payload.NewSessionID` — the *live* binding id for both reasons:

- **`clear`** — `NewSessionID == t.NewID`, which equals `CurrentSessionID` after #739's rebind-before-fan-out; the `SessionHistory` half of the scan covers the async-drain double-rotation race (a second rotation could advance `CurrentSessionID` past this transition's `NewID` before `Run` drains it, leaving `NewID` in history — still resolving to the same conversation).
- **`idle_evict`** — `t.NewID` is empty, so `toWirePayload` mirrors the evicted `PreviousID` onto both wire id fields; `NewSessionID` is that retained id, which still owns the binding (eviction is binding-neutral).

Resolving by the single `NewSessionID` field is branch-free and matches the wire semantic "`conversation_id` names the conversation owning `new_session_id`."

### Fail-closed: unresolvable → whole-event drop

When the binding is unresolvable (race: the session was torn down before `Run` drained the transition), `broadcast` **drops the whole event** — no envelope is fanned to any conn — rather than emit a boundary with an empty or guessed `conversation_id`. A mis-resolution would be a cross-conversation confidentiality leak, so the producer never guesses. This is the same whole-event-drop shape as the existing unknown-reason drop; `Run` survives to process the next transition, and the per-conn `Push` resilience for the resolved case is unchanged. The drop logs at Debug with `reason` only — `conversation_id` is sensitive alongside session ids and `workspace_cwd`, kept out of every log line.

### Purity preserved via an injected resolver closure

`conversationForSession` is **not** called from `toWirePayload` — that mapping seam stays pure and registry-free, the same discipline that keeps it cmd-side away from `internal/sessions`'s genuine import-cycle constraint. Instead the producer receives an injected `resolveConv func(string) (string, bool)` closure, constructed in `startRelayV2` (`relay.go`) where `convReg` is already in scope (it is an existing `startRelayV2` parameter — so, unlike the `boundHostFunc` mirror the ticket estimated, **no** `main.go`/`startRelay` threading was needed) and threaded one hop into the emitter. `session_transition_v2.go` therefore never imports `internal/conversations`. Spec verdict: **PASS**. See [codebase/741.md](../codebase/741.md).

## Edge cases & limitations

- **Inbound + the structured outbound stream now route per-conversation (#678 → #687 → #679).** When #678 landed, claude's *replies* still fanned out from the bootstrap session — and worse, the structured interactive turn stream read its conversation cursor from the bootstrap supervisor, which #678 leaves empty for routed turns, so the structured reply stream went **silent** after the first per-conversation route. [#687](../codebase/687.md) closed the first half — *attribution*: a `cmd/pyry` *active-conversation* signal (`activeConversation`, stamped by `sessionRouter.Route` on success) re-keys the structured stream's two cursor readers (live emitter + #647 reconnect-replay) to it, so the stream **emits again** and each envelope carries the routed conversation's `conversation_id`. [#679](../codebase/679.md) closes the second half — *content*: the producer now **follows the active conversation**, tailing the bound session's transcript **by bound session id** (`resolveBoundSessionJSONL`, mtime-independent) over the bound session's own supervisor, and re-subscribing when the active conversation (or its session) changes. So a *different* session writing more recently can no longer cross-stream its output into the active conversation's reply — the cross-conversation confidentiality property is now enforced, not merely coincidental in the single-operator case. [#686](../codebase/686.md) then re-points that by-id resolver at the conversation's **own per-`Cwd` JSONL directory** (`~/.claude/projects/<encoded-cwd>/`, derived from the bound session's captured spawn `WorkDir`), since #685 spawns per-conversation sessions in distinct directories — so the filename (#679) *and* the directory (#686) are both per-conversation; a default null-`Cwd` session keeps resolving from the shared dir unchanged. The **coarse** v1 bridge (`assistant_turn.go`, the non-interactive dispatch-leg surface) still reads the bootstrap cursor and is unchanged; the v2 coarse bridge (`assistant_turn_v2.go`) was deleted in [#699](../codebase/699.md). The real-claude e2e confirms the full phone→claude→phone round-trip is intact.
- **Accepted residue — unbound session on a non-empty-id error.** `Pool.Create` can return a non-empty id *with* an error (e.g. the mint persisted, then `Activate` timed out; the lifecycle goroutine may still bring the session up against the pool ctx after the handler's timeout fires). The handler treats *any* error as a clean mint failure and does not bind it, so such a session is left registered in the Pool with no conversation pointing at it. This is benign — the same shape as a session that ran and idled out, recoverable by the Pool's own lifecycle — and the race is unobserved, so per evidence-based fix selection no cleanup logic was added.
- **Process-exhaustion / spawn amplification (deferred, and reduced by #2085).** Through #2085, eager binding made `create_conversation` a process-spawning operation, so an authenticated phone spamming creates could exhaust host processes/memory. Since #2085 a create costs a registry row, a per-session settings file and one parked lifecycle goroutine — no process — because the spawn is deferred to the first message; the residue is strictly cheaper than before, not a new bound. A phone that instead spams *messages* still drives real spawns, and the existing in-architecture bound for that is `Pool.ActiveCap` (LRU-evicts a victim when the cap is hit) — but it **defaults to uncapped** (`-pyry-active-cap 0`). A dedicated per-operator create/message quota / rate-limit is new dispatch policy and is a named #672-family follow-up. Ops mitigation today: set `-pyry-active-cap` and/or `-pyry-idle-timeout`.
- **`ActiveCap` churn.** When `ActiveCap` *is* set, each `create_conversation` activation can LRU-evict another conversation's live claude. Acceptable: eviction preserves the on-disk JSONL and the session re-activates on the next `send_message`. This cross-discussion cap eviction (and the no-bleed guarantee that only the deliberate LRU victim transitions) is pinned by [#680](../codebase/680.md)'s binary-boundary e2e — the slice that closes Phase 2.0 by proving per-conversation sessions are full citizens of the idle-evict / cap machinery.
- **Restart scope — closed by #1487.** `sessions.New` still materialises only the bootstrap from `sessions.json`, so every minted per-conversation session is dropped on a warm start. Since [#1487](../codebase/1487.md) that is no longer fatal to the thread: `sessionRouter.resolve` intercepts the resulting `ErrSessionNotFound` and lazily re-materialises the session via `Pool.Revive`, sourcing the spawn directory from the **conversations** registry (`conv.Cwd`) — the only place it is persisted — and re-running `resolveSpawnDir` so a `Cwd` that has become an escape since mint time is rejected rather than trusted. The revive is **lazy** (first touch, not startup, so a restart does not spawn one claude per conversation) and **non-spawning** (`Pool.Revive` registers at `stateEvicted`; the child comes up on the `boundSession.Activate` the drain already performs). Three residues, all accepted:
  - **Phone-set `YOLO` does not survive.** A revived session carries zero `SessionSettings`, so a phone-granted `--dangerously-skip-permissions` is dropped — and the next persist drops it from disk too. Deliberate: a restart is a natural revocation point for a permission bypass, and re-granting is one `settings` verb away. `registryEntry`'s fail-closed rationale was written when only the bootstrap was materialised; #833's "persisted spawn settings must survive restart" argument was about *operator* intent on the bootstrap and does not transfer.
  - **A deliberately-removed session can be resurrected.** The `sessions.remove` control verb drops a session from the pool and `sessions.json` but does not clear the owning conversation's `CurrentSessionID`, so the next `send_message` re-registers the id. Distinguishing "removed" from "dropped by restart" needs persisted registry membership. Unobserved; documented rather than defended against.
  - **An *untouched* entry is still erased from disk.** A persist that happens before the conversation's first post-restart touch (e.g. a bootstrap idle-eviction) still rewrites `sessions.json` without the minted entries. Harmless, because the revive sources its id and cwd from the conversations registry rather than from `sessions.json`.
- **Workspace re-read on rotation — closed by #1475.** Through #1474, `sessionRouter.revive` above was the *only* production reader of `conv.Cwd`: a `change_workspace` reply promised the new folder would reach a spawn, but nothing did until the daemon happened to restart. #1475 makes a `new_session` rotation the second fresh spawn — `activeSessionStarter.StartNewSession` re-reads the bound conversation's recorded `Cwd` and re-confines it with the same `resolveSpawnDir` validator `revive` uses, installing the result on the runner (`(*streamsup.Runner).SetSpawnWorkDir`) between the pool-side rotation and `RestartFresh`. An empty or refused recording leaves the successor in the directory its runner already had — fail-closed, never an unconfined spawn — and the refusal is recorded with the conversation id and no path, the same posture this section's revive already takes. **Rotation and post-restart revive are now the two fresh-spawn paths that read `Cwd`; an idle-evict re-activation is a resume, not a fresh spawn, and still keeps the runner's existing directory.** See [the `new_session` seam's workspace re-read](v2-session-manager-state-machine-inbound-new-session-sessionstarter-seam.md#workspace-re-read-on-rotation-1475) for the dispatch-level ordering and its security posture, and [the runner-side directory pair](streamsup-package-satisfying-sessions-runner.md) for `SetSpawnWorkDir`/`ClaudeSessionsDir`.

## Deferred to follow-ups (EPIC #672)

- **Distinct per-conversation working directory — done (#685), default scratch created (#696), and the bridge follows it — done (#686).** `conversation.Cwd` is now validated (confine to `$HOME` + trust-mark realpath) and used as the bound session's spawn workdir; the phone's default `~/.pyrycode/scratch` resolves under `$HOME` and is created before spawn ([#696](../codebase/696.md)); see [§ `Cwd` is the validated, trust-marked spawn workdir](#cwd-is-the-validated-trust-marked-spawn-workdir-685). [#686](../codebase/686.md) closed the strand: the outbound reply stream's by-id resolver now reads from the conversation's own per-`Cwd` JSONL directory (default sessions unchanged). Still open from this strand: a dedicated `conversation.cwd_rejected` error code (reused `protocol.malformed` for now).
- **Per-operator create quota / rate-limit** (dispatch policy).
- **Per-conversation outbound routing — structured stream done.** All three pieces landed: the structured stream's attribution follows the active conversation ([#687](../codebase/687.md)), its reply **content** follows the bound session's transcript by id ([#679](../codebase/679.md)), and that transcript is resolved from the conversation's own per-`Cwd` directory ([#686](../codebase/686.md)). Still open: the surviving **coarse** v1 bridge (`assistant_turn.go`) still fans out from the bootstrap cursor — re-keying it is a further #672-family follow-up (the v2 coarse bridge was removed in [#699](../codebase/699.md), so only v1 remains). **Multi-operator isolation** (two phones each viewing a *different* conversation concurrently) is also deferred — the structured stream fans out to all interactive conns by capability with no connection→conversation binding for output; #679 covers only the *single* active reply stream following the operator's current conversation.

## Related

- [conversations-package.md](conversations-package.md) — the `Conversation.CurrentSessionID` binding field (and `SessionHistory`, written in production for the first time by #739's rotation rebind).
- [conversations-registry.md](conversations-registry.md) — atomic Save/Load that round-trips the binding (AC#3); the `RebindSession` write primitive (#739).
- [rotation-watcher.md](rotation-watcher.md) — live `/clear` detection → `Pool.RotateID`, the re-key that precedes the #739 rebind.
- [sessions-package.md](sessions-package.md) — `Pool.Create` mint primitive (§ *Pool.Create*) and `buildSession` (the `tpl.WorkDir` / `--session-id`-only spawn point).
- [v2-session-manager.md § Inbound interrupt](v2-session-manager.md#inbound-interrupt-707--interrupter-seam--esc-routing) — since [#1121](../codebase/1121.md), the inbound `interrupt` control frame resolves the active conversation's bound runner via the same `CurrentSessionID → Pool.Lookup` shape this doc's `SessionRouter` seam uses for `send_message`, instead of the bootstrap supervisor.
- [idle-eviction.md](idle-eviction.md) — "evicted is a state, not removal"; lazy respawn on next `send_message`, now per-conversation via `Pool.Activate`.
- [relay-package.md](relay-package.md) — the `create_conversation` / `send_message` handlers and the `SessionCreator` / `SessionRouter` seams alongside `TurnWriter`.
- [codebase/677.md](../codebase/677.md), [codebase/678.md](../codebase/678.md) — per-ticket implementation notes (create + routing halves).
- [codebase/739.md](../codebase/739.md) — per-ticket note for the rotation-maintenance half (`RebindSession` + the `notifyTransition` reason-branch).
- [codebase/741.md](../codebase/741.md) — per-ticket note for the read half (`conversationForSession` + the resolve/stamp/drop in the `session_transition` producer); [codebase/740.md](../codebase/740.md) added the wire field it fills.
- [protocol-package.md](protocol-package.md) — `SessionTransitionPayload.ConversationID`, the routing key #741 populates.
- [codebase/680.md](../codebase/680.md) — Phase 2.0 capstone: e2e proving per-conversation sessions idle-evict, reactivate, and obey the active cap without cross-bleed.
- [codebase/687.md](../codebase/687.md), [codebase/679.md](../codebase/679.md), [codebase/686.md](../codebase/686.md) — the outbound structured-stream migration (attribution + content + per-`Cwd` directory).
- [turnbridge-package.md](turnbridge-package.md) — the producer / follow-active subscriber (`NewTargetSubscriber`) #679 re-keys.
- [ADR 025](../decisions/025-mobile-remote-head-interactive-session.md) — mobile remote-head interactive session.
