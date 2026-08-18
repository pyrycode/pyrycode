# #1487 — Conversations bound to minted sessions survive a daemon restart

**Size:** S (confirmed — PO's `size:s` stands; not downgraded)
**Direction chosen:** #2 from the ticket's Technical Notes — lazy revive in `sessionRouter.resolve`.
**No registry schema change.** `registryEntry` gains no field; `Session` retains no new state. The red line in the ticket body does not trip.

---

## Files to read first

| File | Symbol | What to extract |
|---|---|---|
| `cmd/pyry/main.go` | `sessionRouter.resolve` | The single production edit point. Note the load-bearing ordering: the empty-`CurrentSessionID` guard fires **before** any `Lookup`, because `Lookup("")` returns the bootstrap. |
| `cmd/pyry/main.go` | `boundSession` | The writer `resolve` returns. It is returned **unchanged** on the revive branch — there is exactly one write surface after this ticket. |
| `cmd/pyry/main.go` | `resolveSpawnDir` | Reused verbatim for AC #4. Already does expandTilde → `confineWorkdirToHomeCreating` → `trustMark`, and already wraps `handlers.ErrSpawnDirRejected` on escape. Do not reimplement any of it. |
| `cmd/pyry/main.go` | `resolveDefaultCwd` | Why a *default* conversation's recorded `Cwd` converges on the pool's `tpl.WorkDir` — see § "The default-`Cwd` convergence". |
| `cmd/pyry/main.go` | `sessionMinter.Create` | The mint-time precedent this revive mirrors: `resolveSpawnDir` then a pool call, with the cmd layer as sole validator. |
| `internal/sessions/get_or_create.go` | `GetOrCreateIn` | The sequence to factor: validate id → `buildSession` → lock → take-path → register → `saveLocked` (rollback on error) → prime skip-set → grab `runGroup` (rollback + `ErrPoolNotRunning` if nil) → `g.Go` → unlock → `Activate`. Only the trailing `Activate` is excluded from the new primitive. |
| `internal/sessions/pool.go` | `buildSession` | The in-memory shape a revived entry gets: `lcState: stateEvicted`, open `activeCh`, closed `evictedCh` — byte-identical to an idle-evicted session. Also where `spawnDir` becomes `RunnerConfig.WorkDir`. |
| `internal/sessions/pool.go` | `saveLocked` | Serialises exactly `p.sessions`. This is why AC #2 is satisfied by the *registration*, not by a separate write. |
| `internal/sessions/pool.go` | `Lookup` | The `ErrSessionNotFound` the revive branch intercepts. |
| `internal/sessions/pool.go` | `Ready` | Exported readiness signal — the `cmd/pyry` test fixture needs it (the in-package `runPoolInBackground` polls `pool.mu` directly and is not reachable from `cmd/pyry`). |
| `internal/sessions/registry.go` | `pickBootstrap` | The sole reader of `reg.Sessions`. Confirms non-bootstrap entries are parsed then discarded. **Unchanged by this ticket** — do not touch. |
| `internal/sessions/id.go` | `ValidID` | Canonical UUIDv4 shape. A non-canonical `CurrentSessionID` is rejected here, which changes an existing test's expectation (see § Testing). |
| `internal/conversations/conversation.go` | `Conversation` | The `Cwd` and `CurrentSessionID` fields. Read `Cwd`'s doc: it is deliberately decoupled from a running session's captured spawn `WorkDir`, and "the new folder takes effect on the conversation's next fresh session spawn". |
| `internal/relay/handlers/send_message.go` | `SendMessage` | The `Route`-error → wire-code mapping. **Unchanged**: any non-`ErrConversationNotFound` error is a retryable `server.binary_offline`. |
| `cmd/pyry/session_router_test.go` | `TestSessionRouter_Route`, `newRouterTestPool`, `stubRunner` | The fixture to extend. Its `conv-dangling` subtest asserts `ErrSessionNotFound` flows through — that assertion changes. |
| `cmd/pyry/conversation_spawndir_test.go` | `installRecordingTrustMark` | The `trustMark` seam override, plus the constraint that tests using it call `t.Setenv("HOME", …)` and therefore **cannot be `t.Parallel()`**. |
| `internal/sessions/pool_create_test.go` | `helperPoolCreate`, `runPoolInBackground` | The running-pool fixture the `Revive` unit tests need. |
| `internal/sessions/pool_spawndir_test.go` | `TestPool_GetOrCreateIn_SpawnsInGivenDir` | The pattern for asserting `spawnDir` reached `RunnerConfig.WorkDir`. Mirror it. |
| `internal/sessions/pool_test.go` | `TestPool_BootstrapEvictedOnDisk_StartsClaudeOnWarmStart` | The #202 pin. AC #5 is satisfied by this staying green **unmodified**. |
| `docs/knowledge/features/conversation-session-binding.md` | § "Edge cases & limitations" → "Restart scope" | The deferral this ticket pays. Also § "The `SessionRouter` seam" for the `resolve`/`Route` split contract. |

---

## Context

`Pool.New` materialises exactly one `*Session` from `sessions.json`: the bootstrap. Every other persisted entry is parsed by `loadRegistry`, ignored by `pickBootstrap`, and then erased by the first `saveLocked`. A conversation's `CurrentSessionID` round-trips correctly through the conversations registry, so after a restart a *healthy* binding points at a session the pool does not have. `sessionRouter.resolve` gets `ErrSessionNotFound` from `Lookup` and the `send_message` handler rejects with a retryable `server.binary_offline` — before any enqueue, so the message is never even queued. The thread is permanently dead.

The code already claims the fixed behaviour (`Pool.New`'s bootstrap-only `lifecycle_state` comment: *"Non-bootstrap sessions keep their persisted state; lazy respawn on attach is correct there"*). Nothing implements it.

**Why not rehydrate at `New`.** A revive driven by `sessions.json` has no working directory: `registryEntry` has no cwd field and `Session` never retains `spawnDir`. It would fall back to the daemon template `WorkDir` and revive the conversation into the *wrong project* — worse than today's loud rejection. Persisting the cwd is a registry schema change, which the ticket red-lines. The conversations registry already persists exactly the value needed (`Conversation.Cwd`, a `$HOME`-confined realpath), so the revive is driven from there.

---

## Design

Two changes, two production files.

### 1. `internal/sessions` — a non-spawning materialisation primitive

`GetOrCreateIn` already does everything a revive needs *except* that its last act is `p.Activate(ctx, id)`, which blocks until claude's PTY is ready (2–15s). `resolve` must stay non-blocking: since #721 the `send_message` handler has no blocking call, and `Route` sits on that path. So the revive cannot call `GetOrCreateIn`.

Factor `GetOrCreateIn`'s body — everything from `ValidID` through `g.Go` + unlock — into an unexported helper, and add one exported method on top of it:

```go
// Revive materialises a persisted-but-dropped session back into the pool
// WITHOUT spawning claude. The returned Session is in the evicted state, so it
// is indistinguishable from an idle-evicted one and respawns on the next
// Pool.Activate — the existing lazy-respawn contract, not a new path.
func (p *Pool) Revive(id SessionID, label, spawnDir string) (*Session, error)
```

The helper returns `(*Session, took bool, err error)` so `GetOrCreateIn` keeps its exact current semantics: **the take path must still return without Activating.** `GetOrCreateIn` becomes `helper → if took, return → Activate`; `Revive` becomes `helper → return the session`.

Inherited unchanged from that shared core, all load-bearing:

- `ValidID` rejects a non-canonical id before any state is touched.
- Register + `saveLocked` under `p.mu`, with the in-memory rollback on save failure.
- `registerAllocatedUUIDLocked` inside the same critical section (skip-set priming).
- `runGroup == nil` → rollback + `ErrPoolNotRunning`.
- `g.Go(sess.Run)` scheduled while still holding `p.mu`.
- Lock discipline: `p.mu` (write) held across register → save → schedule. No new lock, no new ordering.

`Revive` takes **no `context.Context`**. That absence is the API signal that it does not spawn.

### 2. `cmd/pyry` — the revive branch in `resolve`

```go
sess, err := r.pool.Lookup(id)
if errors.Is(err, sessions.ErrSessionNotFound) {
    sess, err = r.revive(id, conversationID, conv.Cwd)
}
if err != nil {
    return nil, err
}
return boundSession{pool: r.pool, sess: sess, id: id}, nil
```

with a small unexported method:

```go
// revive re-materialises a conversation's dropped session. resolveSpawnDir is
// the SAME validator the mint path uses (#685/#696) — re-run here because a
// path valid at mint time can become an escape before the restart (AC #4).
func (r sessionRouter) revive(id sessions.SessionID, label, cwd string) (*sessions.Session, error)
```

Behaviour: `resolveSpawnDir(cwd)` → on error return it verbatim → `r.pool.Revive(id, label, spawnDir)` → return its session.

The label is the conversation id, matching what `create_conversation` originally minted with (`creator.Create(mintCtx, string(id), spawnDir)`).

Nothing else in `resolve` moves. The empty-`CurrentSessionID` guard still fires first, so an unbound conversation can never enter the revive branch and can never be handed the bootstrap. `Route` still stamps the cursor only on overall success; `resolve` still never stamps.

### Data flow

```
post-restart send_message(convID)
  → handlers.SendMessage → sessionRouter.Route
      → resolve(convID)
          convReg.Get               → conv (CurrentSessionID, Cwd)
          empty-binding guard       → errNoBoundSession (unchanged)
          pool.Lookup(id)
            hit  → boundSession                                  [unchanged path]
            miss → resolveSpawnDir(conv.Cwd)                      [confine + trust-mark]
                     reject → ErrSpawnDirRejected  → retryable reject, nothing registered
                     ok     → pool.Revive(id, label, spawnDir)
                                 register + saveLocked + g.Go(sess.Run)   [no spawn]
                              → boundSession
      → active.set(convID)  → Enqueue → ack
  → msgqueue drain → resolve (hit now) → boundSession.Activate → Pool.Activate → claude spawns in spawnDir
                                       → WriteUserTurn
```

### Why the revived session is not a new lifecycle path

`buildSession` returns a Session at `stateEvicted` with an open `activeCh` and a closed `evictedCh`. That is byte-for-byte the shape an idle-evicted session has. The runner it builds carries `--session-id <the original uuid>` in its argv, and reactivation re-runs that same runner — which is exactly what already happens on every idle-evict → reactivate cycle today. So respawning claude against an existing JSONL under a reused `--session-id` is not new behaviour this ticket introduces; it is the already-exercised lazy-respawn path. The conversation's history continuity rides on that.

### The default-`Cwd` convergence (AC #3, load-bearing)

`create_conversation` deliberately separates "what to record" from "where to spawn": a conversation created with a null `cwd` **records** `defaultCwd` on its row but **spawns** with `spawnDir == ""`, i.e. in the pool's `tpl.WorkDir`. Reviving from `conv.Cwd` therefore looks like it could relocate a default conversation into a different directory — which would split its claude JSONL across two `~/.claude/projects/<encoded-cwd>/` directories and lose the history.

It does not, because the two paths converge:

- `defaultCwd = resolveDefaultCwd(workdir) = filepath.Abs(workdir)`
- `tpl.WorkDir = trustMark(confineWorkdirToHome(workdir)) = trustMark(EvalSymlinks(Abs(workdir)))`
- `resolveSpawnDir(defaultCwd) = trustMark(confineWorkdirToHomeCreating(Abs(workdir)))`, and `confineWorkdirToHomeCreating` reduces exactly to `confineWorkdirToHome` when the path already exists.

Both sides are `trustMark(EvalSymlinks(Abs(workdir)))`. Same bytes. This must be pinned by a test — it is the one place where an innocuous-looking change to `resolveDefaultCwd` or `confineWorkdirToHome` would silently relocate every default conversation.

### What is deliberately *not* changed

- **`Pool.New` / `pickBootstrap` / `registryEntry`.** No schema field, no startup rehydration. AC #5 (#202 non-regression) therefore holds structurally, not by a new guard.
- **`send_message`'s error mapping.** An escaping `Cwd` surfaces as a retryable `server.binary_offline` — byte-identical to the rejection that conversation gets *today*. Making it a non-retryable `protocol.malformed` (as `create_conversation` does for a rejected mint) would mean editing `internal/relay/handlers/send_message.go`, a third production file and a wire-behaviour change, for an outcome AC #4 does not ask for. Noted in Open questions.
- **Startup reconciliation.** Reviving every bound conversation at startup would spawn one claude per conversation. The revive is lazy, driven by the first touch.

---

## Concurrency model

No new goroutine is owned by this design. `Revive` schedules exactly one — `sess.Run` on `p.runGroup`, via the same `g.Go` call `GetOrCreateIn` already makes, exiting when the pool's run-context is cancelled. Leakage surface is unchanged.

- **Locks.** `Revive` acquires only `p.mu` (write), held across register → `saveLocked` → `g.Go`, released before returning. Identical to `GetOrCreateIn`. Lock order `Pool.mu → Session.lcMu` is untouched.
- **Concurrent revives of the same id.** Two `resolve` calls (two conns, or a conn and the drain) can race into the revive branch. The take path inside the shared core resolves it under `p.mu`: the loser gets the winner's `*Session`. Both callers end up with the same session; neither spawns. `resolveSpawnDir`'s `MkdirAll` is idempotent, so the duplicated validation is harmless.
- **Revive vs. `Pool.Run` shutdown.** If `runGroup` is nil (pool not yet running, or the field was never wired), the core rolls the registration back and returns `ErrPoolNotRunning`, which `resolve` surfaces as a retryable reject — today's behaviour for that conversation.
- **`resolve` is still non-blocking in the spawn sense.** The revive branch adds filesystem syscalls (`EvalSymlinks`, possibly `MkdirAll`, `trustMark`'s `~/.claude.json` write, `saveLocked`'s temp+fsync+rename) but no wait on a child process. It runs at most once per session per daemon lifetime: after it succeeds, `Lookup` hits and the steady-state path is byte-identical to today.

---

## Error handling

| Condition | Detected by | Result |
|---|---|---|
| Conversation unknown | `convReg.Get` miss (before revive) | `ErrConversationNotFound` → `conversation.not_found`, non-retryable. Unchanged. |
| `CurrentSessionID == ""` | empty-binding guard (before `Lookup`, so before revive) | `errNoBoundSession` → retryable `server.binary_offline`. Unchanged. |
| Bound id present in pool | `Lookup` hit | `boundSession`. Unchanged. |
| `Cwd` escapes `$HOME` (incl. symlinked ancestor) | `resolveSpawnDir` → `confineWorkdirToHomeCreating` | wraps `handlers.ErrSpawnDirRejected` → retryable `server.binary_offline`. **Nothing registered, no runner constructed, no directory created.** |
| `trustMark` write failure | `resolveSpawnDir` | plain error → retryable `server.binary_offline`. |
| `CurrentSessionID` not a canonical UUIDv4 | `ValidID` inside `Revive` | `ErrInvalidSessionID` → retryable `server.binary_offline`. Nothing registered. |
| Pool not running | `runGroup == nil` in the shared core | rollback + `ErrPoolNotRunning` → retryable `server.binary_offline`. |
| `saveLocked` fails during revive | shared core | in-memory registration rolled back, error returned → retryable reject. |

Every failure mode lands on the same wire outcome the conversation gets today. The revive can only *improve* a rejection into an acceptance; it can never turn a working conversation into a broken one.

Post-ack failures (the binding changes between enqueue and drain) are absorbed and retried by the drain, unchanged by this ticket.

---

## Testing strategy

`make check` covers all of it. Not `needs-real-claude`: no permission modal, live turn stream, or interrupt is involved.

### `internal/sessions` — new `pool_revive_test.go`

Use `helperPoolCreate(t, regPath, 0)` + `runPoolInBackground(t, pool)`.

- **Fresh id on a running pool** — `Revive` returns a `*Session`; `Lookup(id)` now hits and returns the same pointer; `State().ChildPID == 0` (nothing spawned); the on-disk `sessions.json` now contains the entry with `lifecycle_state: "evicted"`.
- **The revived session activates normally** — a subsequent `Pool.Activate(ctx, id)` brings up the child. This is the AC #1 half that proves "reaches a claude child".
- **Take path** — `Revive` on an id already in the pool returns the *existing* `*Session` (pointer identity against `Lookup`), does not replace it, and does not spawn.
- **`spawnDir` threading** — a non-empty `spawnDir` reaches `RunnerConfig.WorkDir`; an empty one falls back to `tpl.WorkDir`. Mirror `TestPool_GetOrCreateIn_SpawnsInGivenDir`'s capture pattern.
- **Pool not running** — build via `sessions.New` without `Run`: `ErrPoolNotRunning`, `Lookup` still misses, and `sessions.json` is unchanged (assert on file bytes, not just absence from the map — this is the rollback path).
- **Invalid id shape** — `ErrInvalidSessionID`, nothing registered.
- **`GetOrCreateIn` regression** — every existing test in `pool_get_or_create_test.go` and `pool_spawndir_test.go` stays green **unmodified**. In particular the take path must still return *without* Activating; if the refactor makes the take path Activate, those tests are the detector.

### `cmd/pyry` — extend `session_router_test.go`

`Revive` needs `runGroup`, so add a running-pool variant of `newRouterTestPool`: start `pool.Run(ctx)` on a goroutine, wait on `<-pool.Ready()`, cancel via `t.Cleanup`. Tests that call `resolveSpawnDir` set `$HOME` with `t.Setenv` and install `installRecordingTrustMark`, so they **must not** be `t.Parallel()`.

- **Warm-start round trip (AC #1 + #2 + #3)** — pool A with a `RegistryPath` under `t.TempDir()`; `CreateIn` a session in a `$HOME`-confined dir; build a `conversations.Registry` row carrying that `CurrentSessionID` + `Cwd`; stop pool A; `sessions.New` pool B from the *same* registry path. Assert first that `poolB.Lookup(id)` is `ErrSessionNotFound` — that is the bug, and without this rung the test can pass vacuously. Then `resolve(convID)` returns a `boundSession` with that id; `poolB.Lookup(id)` now hits; the on-disk `sessions.json` contains the entry (AC #2); the captured `RunnerConfig.WorkDir` is the confined realpath, not `tpl.WorkDir` (AC #3).
- **AC #2 ordering matters** — assert the persist *after* the resolve. See § Open questions: a persist that happens *before* the first touch still erases the entry, and that is in-scope-accepted behaviour.
- **Default-`Cwd` convergence (AC #3)** — a conversation whose `Cwd` is `resolveDefaultCwd(workdir)` revives with a spawnDir byte-identical to `confineWorkdirToHome(workdir)`. Assert on `installRecordingTrustMark`'s captured `gotWorkdir`.
- **AC #4, escaping symlink** — `Cwd` pointing at a `$HOME` symlink resolving outside `$HOME`: `resolve` returns an error satisfying `errors.Is(err, handlers.ErrSpawnDirRejected)`, `pool.Lookup(id)` still misses, and the `RunnerFactory` was never called. Reuse the fixture shape from `conversation_spawndir_test.go`'s escaping-symlink test.
- **AC #4, symlinked ancestor of a missing dir** — a not-yet-existing `Cwd` under a `$HOME` symlink pointing outside: rejected, and the directory is **not** created (assert the target path does not exist afterwards). #696 must not regress.
- **Cursor invariants stay** — extend `TestSessionRouter_ResolveDoesNotStamp` with a revive case: a successful revive via `resolve` must leave `active` empty, and the same binding via `Route` must stamp it.
- **`conv-dangling` subtest must be rewritten, not deleted.** Its binding is the literal `"session-not-in-pool"`, which `ValidID` rejects, so `resolve` now returns `ErrInvalidSessionID` rather than `ErrSessionNotFound`. Keep the case, retarget the assertion, and keep the "writer is nil on reject" rung — a malformed binding must still never yield a writer.
- **Unbound-conversation guard stays green unmodified** — `errNoBoundSession` fires before `Lookup`, therefore before any revive. If the revive branch were ever moved above the guard, this test is the detector.

### AC #5 (#202)

`TestPool_BootstrapEvictedOnDisk_StartsClaudeOnWarmStart` stays green **unmodified**. `Pool.New` is not touched, so this is a structural pin. Do not add a new test for it.

---

## Open questions

Resolve during implementation or record as accepted residue; none blocks the build.

1. **Phone-set YOLO does not survive the restart.** The shared core passes `SessionSettings{}`, so a revived session spawns with permissions *enforced*, discarding the `yolo: true` that `settingsUpdaterAdapter` may have persisted for a phone-minted session. This is the fail-closed answer and it is free — it requires no code. It is also the ticket's named open question, and the security review below adjudicates it as the correct outcome. **Consequence to accept:** the next `saveLocked` rewrites that entry with the setting dropped, so the phone's choice is lost from disk too, not merely inert. Do not "fix" this by threading persisted settings into the revive — that would need the discarded `registryEntry`, i.e. direction 1.
2. **A deliberately-removed session can be resurrected.** The `sessions.remove` control verb (`internal/control/server.go`'s `sessioner.Remove` call) drops a session from both the pool and `sessions.json`, but does not clear the owning conversation's `CurrentSessionID`. After this ticket the next `send_message` to that conversation re-registers the id. Distinguishing "removed" from "dropped by restart" requires consulting persisted registry membership, which means direction 1. Unobserved; accepted and documented rather than defended against.
3. **An untouched entry is still erased from disk.** AC #2's second clause ("a restart followed by any state-changing operation no longer erases it") holds only for a conversation that has been *touched* since the restart. A bootstrap idle-eviction that persists before any `send_message` still rewrites `sessions.json` without the minted entries. This is harmless under direction 2 — the revive sources its id and cwd from the *conversations* registry, not from `sessions.json`, so the erasure cannot break it — but it is a narrower guarantee than the AC's broadest reading, and closing it fully requires direction 1. Flagged for PO; not a reason to hold the build.
4. **`CreatedAt` resets.** `buildSession` stamps `time.Now()`, so the revived entry loses its original creation timestamp. Cosmetic — it only affects `sortEntriesByCreatedAt`'s disk ordering.
5. **Cold start with a surviving conversations registry.** If `sessions.json` is deleted but `conversations.json` is not, the bootstrap gets a fresh id and every conversation's binding dangles. Each then revives its old id as a *non-bootstrap* session in its own `Cwd`. That is the desired outcome, but worth a moment's confirmation that no code assumes "a non-bootstrap id in the pool was minted by this process".
6. **Escaping-`Cwd` retryability.** A permanently-escaping `Cwd` produces a *retryable* reject, so the phone retries forever against a condition that will never clear. This matches today's behaviour exactly for that conversation, and making it non-retryable means editing `send_message.go`. Left as-is deliberately; a dedicated `conversation.cwd_rejected` code is already a named #672-family follow-up in `conversation-session-binding.md`.

---

## Security review

**Verdict:** PASS

**Findings:**

- **[Trust boundaries]** No MUST FIX. The revive is a **new claude-spawn site consuming a phone-originated value** (`Conversation.Cwd`, written by `create_conversation` and mutable by the `change_workspace` verb). The boundary is explicit and singular: `resolveSpawnDir` in `cmd/pyry`, the *same* function the mint path uses, called before any pool state is touched. `Pool.Revive` documents — as `CreateIn`/`GetOrCreateIn` already do — that `spawnDir` is used verbatim and is **not** validated pool-side; the caller hands it a pre-resolved realpath. Downstream code holds only a trusted realpath. The design does not add a second place where a cwd is interpreted.
- **[File operations — path traversal]** No MUST FIX, and this is the category the ticket was labelled for. #696 adjudicated that a `Cwd` escaping `$HOME` after symlink resolution — including via a symlinked *ancestor* of a not-yet-existing path — must be rejected. That check ran at mint time only, and a path valid when minted can become an escape before the restart: an attacker with write access under `$HOME` replaces a component with a symlink out, then waits for a daemon restart. The revive is precisely where that stale validation would have been trusted. **The design re-runs the full validator at revive time rather than trusting the recorded value**, so the escape is caught with the same posture and the same code as at mint. AC #4's two test rungs (escaping symlink; symlinked ancestor of a missing dir, which must be rejected *and not created*) are the pins.
- **[File operations — TOCTOU]** No MUST FIX. The residual window between `EvalSymlinks` and claude's `chdir` is the *same* window `conversation-session-binding.md` already documents as accepted for both the daemon's own bootstrap workdir and the #685 mint path. This design does not widen it: `MkdirAll` runs only after the pre-creation containment check, and the created realpath is re-confined before trust+spawn. Closing it (`openat2`/`RESOLVE_BENEATH`) remains out of scope and unobserved.
- **[File operations — atomic writes / permissions]** No findings. `Revive` persists through the existing `saveLocked` → `saveRegistryLocked` path: temp file in the same dir at `0600`, fsync, rename. No new file is written by this design.
- **[Subprocess execution]** No MUST FIX, with one deliberate decision recorded. The revived child's argv is composed by the unmodified `buildSession` from `tpl.ClaudeArgs` + `--session-id <id>` + `--settings <path>` + `claudeSettingsArgs(settings)`. Two phone-influenced values reach it: the session id, which `ValidID` constrains to canonical UUIDv4 before anything is registered, and the settings suffix. **The revive passes `SessionSettings{}`, so `--dangerously-skip-permissions` is never appended.** This is the correct adjudication of the ticket's named open question: `registryEntry.YOLO`'s existing fail-closed rationale ("neither absence nor corruption can ever enable bypass") was written when only the bootstrap was materialised, and #833's "persisted spawn settings must survive restart" argument was about *operator* intent on the bootstrap — it does not transfer to a phone-set flag on a minted session. A restart is a natural revocation point for a phone-granted permission bypass, and re-granting is one `settings` verb away. Requiring the phone to re-assert bypass after a restart is strictly safer than silently reviving it, and it costs no code. Recorded as an intentional behaviour change in Open question 1 so it is not "fixed" later by accident.
- **[Error messages, logs, telemetry]** No MUST FIX. `resolveSpawnDir` already wraps the confine detail via `%v` for logs and the handler replies with the static `msgServerBinaryOffline` — **no path is echoed to the phone** on a rejected revive. `Pool.Revive` must not log the `spawnDir`: a conversation's workspace path is sensitive alongside session ids and `conversation_id` (the #741 precedent, where the whole event is dropped rather than emit a guessed routing key). SHOULD FIX for the developer: if a log line is added on the revive path, carry `session_id` only — not `spawnDir`, not `conversation_id`.
- **[Concurrency]** No findings. No new lock, no new lock order, no new goroutine owner. The register-then-persist critical section is inherited verbatim from `GetOrCreateIn`, including the rollback on save failure. The same-id race resolves through the existing take path under `p.mu`. Shutdown mid-revive leaves either the pre- or post-update `sessions.json` (atomic rename); a partially-registered in-memory entry cannot outlive the process.
- **[Threat model alignment]** No findings. The relevant threat from `docs/protocol-mobile.md` § Security model — an authenticated phone influencing where the daemon executes code — is addressed by the boundary above. **Confused-deputy check:** the phone supplies only the `ConversationID` lookup key; the revived session's id comes from the server-stored `CurrentSessionID` and is never phone-writable, so a phone cannot point a revive at an arbitrary session id, and the pre-`Lookup` empty-binding guard still prevents an unbound conversation from resolving to the bootstrap. **Resource exhaustion:** the revive spawns at most one claude per bound conversation, lazily, and every activation still funnels through the cap-enforcing `Pool.Activate` via `boundSession` — so revived sessions are full `ActiveCap` citizens exactly like minted ones. It does not widen the already-named "per-operator create quota" gap: no new conversation can be created through this path.
- **[Tokens, secrets, credentials]** Not applicable — the design generates, stores, and compares no credential. The only identifier it handles is a session UUID already minted by `crypto/rand` via `NewID`, re-read from server-owned state rather than generated here.
- **[Cryptographic primitives]** Not applicable — no randomness, hashing, key material, or comparison against a secret is introduced.
- **[Network & I/O]** Not applicable — no socket, listener, or wire-format change. The `send_message` request/reply shape and every error code it can emit are unchanged; the revive only converts a reject into an accept on the success path.

**Reviewer:** architect (self-review per `architect/security-review.md`)
**Date:** 2026-08-18
