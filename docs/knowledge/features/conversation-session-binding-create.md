# conversation-session-binding — create path (mint, bind, agent, settings)

Split out of [`conversation-session-binding.md`](conversation-session-binding.md) (parent doc — see there for the wire-level create/reply contract, rotation maintenance, and the read-side `conversation_id` stamping). This child covers the create path in full: how `create_conversation` mints and binds a session, including agent selection (#2647), requested model/effort (#2665), the `SessionCreator` seam, the validated spawn workdir, legacy-row normalisation, and mint-failure handling.

## How it works

### Eager bind at create time

When the daemon handles a `create_conversation` frame, the handler mints a session **before** recording the registry row:

1. Decode payload, resolve `name` / `promoted` (server defaults for null fields). `cwd` — what gets **recorded** — is not settled yet; it depends on the mint's answer (step 3).
2. `id, err := conversations.NewID()` — server-minted conversation id (crypto/rand UUIDv4).
3. **Mint the session:** `creator.Create(ctx, string(id), spawnDir, agent, settings)` where `spawnDir` is the *raw* phone-requested `p.Cwd` (empty for a null `Cwd`), `agent` is `protocol.AgentClaude` or `protocol.AgentCodex`, already resolved and refused-if-invalid by the handler before this call (#2647; see [§ Agent selection](#agent-selection-2647)), and `settings` is the request's own `model`/`effort` pointers, already past their shape check (#2665; see [§ Requested model and effort](#requested-model-and-effort-2665)). `Pool.Mint` mints a session UUID → registers + persists it in the sessions registry → supervises; it is `MintAs(label, spawnDir, HarnessClaude)`, itself now `MintWith(label, spawnDir, harness, MintDefaults(harness))` (#2665) — and `creator.Create` calls `MintWith` directly, with `MintDefaults(agent)` overridden field-by-field by any present `settings`. Since [#2085](#spawn-deferred-to-the-first-message-2085) it does **not** activate — no claude spawns here (a Codex session's build is the one exception — see [§ Agent selection](#agent-selection-2647)). Returns `(sessionID, dir, err)`: `dir` is the validator's answer — empty for a default `Cwd`, the confined, trust-marked realpath for a set one (#685; see [§ `Cwd` is the validated, trust-marked spawn workdir](#cwd-is-the-validated-trust-marked-spawn-workdir-685)) — and is what step 4 records, not the raw `spawnDir` (#2568).
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

### Agent selection (#2647)

`create_conversation` accepts an optional `agent` (`"claude"` / `"codex"`; absent means claude). The handler resolves it — `resolveCreateAgent` in `internal/relay/handlers/create_conversation.go` — **before** `conversations.NewID` or the mint: `"codex"` from a conn that has not negotiated `multi_agent` ([capability](../../protocol-mobile.md#capability-negotiation-v2)), or any value outside `protocol.AgentClaude` / `protocol.AgentCodex`, is refused with `protocol.unsupported` and creates nothing — no session, no registry entry, no conversation row. Only the two validated constants ever reach `creator.Create`.

`Pool.Mint(label, spawnDir)` is now `MintAs(label, spawnDir, HarnessClaude)`; `MintAs(label, spawnDir, harness)` is the general form and is what `sessionMinter.Create` calls with the handler-resolved agent. It does not validate the harness — the injected `RunnerFactory` is the one place that decides which harnesses have a runner, the same contract [§ Runner + RunnerFactory](sessions-package-key-types-runner-interface-runnerfactory.md) already states for `sessions.json`'s persisted `harness` field. The harness `buildSessionAs` carries onto the built `Session` is what `saveLocked` persists on the registry entry, so a Codex conversation's session revives as Codex after a daemon restart, on the existing #2593 mechanism.

**A Codex mint starts at Codex's own defaults, not the operator's Claude settings — unless the request itself named one.** `Pool.MintDefaults(harness)` (#2665) is the one definition of what a bare mint gives: `mintSettings()` (the operator's configured model/effort, never the bypass, #1575) for `HarnessClaude`, the zero `SessionSettings` for anything else, so a Codex spawn argv carries no `--model`/`--effort` by default and Codex chooses its own. This is a deliberate asymmetry with `Revive`, which inherits the *session's own* persisted settings regardless of harness — the zero-settings arm applies only to a **fresh** non-Claude session, which has no prior settings to inherit. A `create_conversation` request naming `model`/`effort` overrides this default field-by-field before the mint — see [§ Requested model and effort](#requested-model-and-effort-2665).

**Unlike a Claude mint, a Codex mint can fail before `create_conversation` replies, not on the first message.** [§ Spawn deferred to the first message](#spawn-deferred-to-the-first-message-2085) above holds for the *child process* on every harness — `Pool.Activate` still runs at the conversation's first message, not here. But `buildSessionAs` calls the injected `RunnerFactory` synchronously to construct the `Runner` value itself, for every harness, and the two factories differ in what that construction touches: `newStreamRunnerFactory` (Claude) does no I/O — it only assembles config and decorator closures — while `newCodexRunnerFactory` (`cmd/pyry/codex_runner.go`) probes Codex before returning, starting the app-server against the daemon's Codex home to check its version and sign-in state, then stopping it. A host with no usable Codex (binary missing, too old, not signed in) therefore fails the *mint* — the whole `create_conversation` request — with the existing retryable `server.binary_offline`, exactly like any other mint failure ([§ Mint-failure and timeout behaviour](#mint-failure-and-timeout-behaviour)); it does not defer to the msgqueue drain's retry/give-up path the way a Claude spawn failure does. A reader relying on "the create path never touches the external binary" (true for Claude since #2085) would be wrong for Codex.

### Requested model and effort (#2665)

`create_conversation` accepts optional `model` and `effort` (`omitempty`, the #2647 `agent` pattern): nil keeps what `MintDefaults(agent)` gives; a present value becomes the mint's `Model`/`Effort`, field by field. The handler runs the **shape** check (`relay.ValidModel` / `relay.ValidEffort` — the same grammar [`set_session_settings`](../../protocol-mobile.md#set_session_settings) enforces) before anything is minted, and refuses with `protocol.malformed` and `create_conversation`'s own static message — not `set_session_settings`'s — on failure. The **membership** check runs inside `sessionMinter.Create`, not in a separate seam before `conversations.NewID` as the ticket's own technical note first suggested: `effort` must be checked against "the model the session will start on", which for Claude is the operator default, and validating that in one call and minting from `MintDefaults` in a second invites a race — an operator settings change between the two could validate against one model and mint on another. Composing `start := MintDefaults(agent)` overridden by any present field, then checking `start` before minting `start` itself, closes that window by construction (one snapshot, both uses). The membership check reuses exactly what `settingsUpdaterAdapter.UpdateSettings` runs (`agentModelVocabulary` read unbound — there is no session yet — then `validateModelVocabulary`, `validateEffortVocabulary`), so **a create cannot store a value a later `set_session_settings` would refuse**. A refused model answers `protocol.malformed`, `requested model is not offered` (`set_session_settings`'s own message, reused verbatim); an unavailable vocabulary answers the retryable `model_list.unavailable`. Every refusal returns before `resolveSpawnDir` (the trust-mark) and before the mint — no session, no registry entry, no conversation row, and the requested value is never echoed in a reply or a log line. See [`docs/protocol-mobile.md` § `create_conversation`](../../protocol-mobile.md#application-message-types) for the wire-level contract.

A refusal test that only asserts "no session was created" cannot by itself prove the check ran *before* `resolveSpawnDir` — a check that moved to run after it would still leave the pool empty on a refusal, for a different reason. The production test suite instead sends a refused request with `spawnDir` set to `/`, which `resolveSpawnDir` would itself reject: getting the settings sentinel back, not `ErrSpawnDirRejected`, is what shows the membership check actually ran first.

### The `SessionCreator` seam (keeps `handlers/` import-clean)

The handler depends on a narrow consumer-declared interface, mirroring the sibling `TurnWriter`:

```go
// internal/relay/handlers/create_conversation.go
type SessionCreator interface {
    // spawnDir == "" → the daemon's shared trusted workdir (default, unchanged).
    // A non-empty spawnDir is the phone's raw requested Cwd, validated +
    // trust-marked by the impl before the mint (#685); an escape wraps
    // ErrSpawnDirRejected. agent is protocol.AgentClaude or protocol.AgentCodex,
    // already validated by the handler (#2647) — an implementation never sees
    // another value. settings is the request's model/effort pointers, already
    // past their shape check (#2665); the impl checks a present one against
    // agent's own vocabulary before anything else, returning
    // relay.ErrModelNotOffered / relay.ErrEffortNotOffered /
    // relay.ErrModelVocabularyUnavailable with nothing created. Mints and
    // binds only — the child comes up on the conversation's first message,
    // not here (#2085), except that a Codex build probes Codex synchronously
    // before Create returns (see § Agent selection). dir is the impl's
    // resolved answer — "" for an empty spawnDir, the confined, trust-marked
    // realpath otherwise — and is what the handler records as the
    // conversation's Cwd for a set request (#2568): one resolver on the
    // create path, so the recorded path and the spawn dir cannot disagree.
    Create(ctx context.Context, label, spawnDir, agent string, settings CreateSettings) (sessionID, dir string, err error)
}

// CreateSettings is a create_conversation's requested model and effort
// (#2665): nil is absent, and a pointer to "" is the agent's own default, as
// on set_session_settings.
type CreateSettings struct{ Model, Effort *string }
```

`*sessions.Pool.CreateIn` returns `(sessions.SessionID, error)`, not `(string, error)`, so it does **not** satisfy this directly. It is adapted at the `cmd/pyry` boundary — the only package that knows both `*sessions.Pool` and `handlers.SessionCreator` — by a thin wrapper mirroring the existing `poolResolver`, which **also owns the cmd-layer validation** of the phone-requested spawn workdir:

```go
// cmd/pyry/main.go
type sessionMinter struct {
    p *sessions.Pool
    // saved is the daemon's persisted model vocabulary (#2665) — the same
    // third source settingsUpdaterAdapter checks against, so a create and a
    // later set_session_settings accept exactly the same values.
    saved savedModelVocabulary
}
// ctx is unused since #2085: Pool.Mint/MintAs is ctx-free — it spawns nothing
// on the claude path, so there is nothing left here to cancel or time out. A
// Codex build's synchronous probe (#2647) is likewise not bounded by ctx.
func (m sessionMinter) Create(_ context.Context, label, spawnDir, agent string, settings handlers.CreateSettings) (string, string, error) {
    base := m.p.MintDefaults(agent)                            // what a bare mint would give
    start := sessions.SessionSettings{Model: base.Model, Effort: base.Effort}
    if settings.Model != nil {
        start.Model = *settings.Model
    }
    if settings.Effort != nil {
        start.Effort = *settings.Effort
    }
    if (settings.Model != nil && *settings.Model != "") || (settings.Effort != nil && *settings.Effort != "") {
        list, have := agentModelVocabulary(m.p, m.saved, agent, "")
        // validateModelVocabulary / validateEffortVocabulary — the checks
        // settingsUpdaterAdapter.UpdateSettings runs — against start.Model,
        // returning before any side effect on refusal.
    }
    resolved, err := resolveSpawnDir(spawnDir)   // confine to $HOME + trust-mark (#685)
    if err != nil {
        return "", "", err
    }
    id, err := m.p.MintWith(label, resolved, agent, start)
    return string(id), resolved, err   // resolved is what #2568 records, not spawnDir
}
```

`sessionMinter{pool, modelVocabulary}` is threaded through `startRelay` → `startRelayV2` and into both `handlers.CreateConversation(...)` registration sites (the v1 dispatcher and the v2 manager handler map). Result: `internal/relay/handlers` stays free of any `internal/sessions` import — the cycle-free property is preserved, and the cmd-layer adapter is the sole validator of the spawn workdir (see [§ `Cwd` is the validated, trust-marked spawn workdir](#cwd-is-the-validated-trust-marked-spawn-workdir-685)).

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

Any mint error (pool not running, in-pool save failure) fails the whole create: the handler logs at `Warn` (`create_conversation.session_mint_failed`, fields `conn_id` / `conversation_id` / wrapped `err`) and replies `protocol.CodeServerBinaryOffline` **retryable**, returning **before** `reg.Create` — so there is no half-bound orphan conversation row. The phone retries onto a fresh conversation + session. A spawn failure on the *first message* is no longer a create-time failure at all — it surfaces on the msgqueue drain's own retry/give-up path, identical to an idle-evicted conversation's reactivation failure today — **except for a Codex mint** (#2647), where the runner factory's own probe of Codex can fail the mint itself; see [§ Agent selection](#agent-selection-2647).

