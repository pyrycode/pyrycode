# #2085 — Start the claude process on the first message, not when the conversation is created

## Files read

- `internal/relay/handlers/create_conversation.go` → `CreateConversation`, `SessionCreator`, `createConversationMintTimeout` — the create handler, the seam whose implementation defers, and the timeout whose stated rationale ("Pool activation blocks until claude's PTY is ready") stops being true.
- `internal/sessions/pool.go` → `Pool.CreateIn`, `Pool.Create`, `Pool.buildSession`, `Pool.supervise`, `Pool.Activate`, `Pool.RegisterAllocatedUUID`, `Pool.IsAllocated`, `allocatedTTL` — the whole create sequence, the spawn entry, and the rotation skip-set.
- `internal/sessions/get_or_create.go` → `Pool.materialise`, `Pool.GetOrCreateIn` — the existing register-without-spawn core; the shape `Mint` mirrors, and the second site that primes the skip-set.
- `internal/sessions/revive.go` → `Pool.Revive` — the non-spawning sibling whose contract prose (`"the child comes up on the next Pool.Activate"`) `Mint` copies, and whose ctx-free signature is the API signal that it cannot block.
- `internal/sessions/transition.go` → `Pool.RotateForNewSession` — states the register-before-spawn ordering invariant the skip-set protects.
- `cmd/pyry/main.go` → `sessionMinter.Create`, `sessionRouter.resolve`, `sessionRouter.revive`, `boundSession`, `newInboundDeliver` — the only production caller of `CreateIn`, and the drain that already performs the cap-enforcing `Activate` a deferred spawn rides on.
- `internal/control/server.go` → the `sessions.new` verb's `s.sessioner.Create(ctx, label)` — the out-of-scope caller that must keep spawning, and the reason `CreateIn`'s contract cannot simply lose its activate.
- `internal/sessions/selfheal_test.go`, `internal/sessions/transition_test.go` → the two standing assertions about who does and does not prime the skip-set; neither is disturbed by priming at `Pool.Activate`.
- `internal/e2e/per_conversation_eviction_test.go` → `TestE2E_PerConversation_CapEvictsCrossDiscussion`, `TestE2E_PerConversation_IdleEvictsAndReactivates`, `createConversationViaPhone`, `drainForReply` — the one test that breaks outright and the one that goes vacuous.
- `internal/e2e/relay_v2_stream_model_list_reconcile_test.go`, `..._slash_command_list_reconcile_test.go`, `..._background_task_roster_reconcile_test.go` → each drives a turn after its create, so each still passes; each carries the now-false "the mint is a spawning `Pool.Activate`" prose in its header and in a timeout diagnostic.
- `internal/e2e/relay_v2_stream_run_config_test.go` → creates a conversation, drives **no** turn, and reads/writes settings: the standing regression proof for AC#2, unchanged.
- `internal/e2e/realclaude/interactive_per_conversation_liveness_test.go` → all three cases already `sealSendMessage` after the create, and `perTurnReplyBudget` (120s) is already documented as covering a cold spawn — no edit needed.
- `docs/knowledge/features/conversation-session-binding.md` § *Eager bind at create time*, § *`Cwd` is the validated, trust-marked spawn workdir*, § *Residual TOCTOU window (accepted)* — the bind-vs-spawn distinction, the sole-validator posture, and the accepted-TOCTOU record this ticket widens.

## Context

`create_conversation` mints a conversation's dedicated session **and spawns claude for it**, so a conversation nobody has spoken in still costs a process, and — the reason this ticket exists — every per-session setting has to be applied to an already-running child. A model or effort chosen after creation goes in as an in-band slash command, and clearing one back to claude's own default cannot be expressed in-band at all. The first turn of a new conversation therefore always runs at whatever the session was minted with, with a visible correction afterwards.

Deferring the spawn to the first message makes anything set before that message simply what the child launches with: the launch-flag path already exists and already omits a flag for an empty value.

The blockers (#2124 model-list source, #2125 on-demand `request_model_list`) are merged at `1cc01a05`, so the model and effort menus stay populated across the pre-send window this change creates.

No ADR is warranted: this narrows an existing path rather than introducing a new decision axis, and `conversation-session-binding.md` is the doc that already carries the eager-bind contract this amends. The documentation phase owns folding it in.

## Design

### The shape

`Pool.CreateIn` today is: `NewID` → `buildSession` (lands in `stateEvicted`) → register + persist under `p.mu` → prime the rotation skip-set → `supervise` → `Activate`. Only the last step spawns. The change is to make everything up to `supervise` reachable without the `Activate`, and to move the skip-set prime to where the spawn actually happens.

**`Pool.Mint(label, spawnDir string) (SessionID, error)`** — a straight extraction of the `CreateIn` body minus the final `Activate`. Ctx-free, exactly as `Pool.Revive` is ctx-free: the absent `context.Context` is the API signal that it does no blocking work and cannot spawn. Contract:

- Returns the freshly minted `SessionID` of a session registered, persisted and supervised in `stateEvicted`, with its lifecycle goroutine parked on the activate signal — byte-for-byte the shape an idle-evicted session has, and the shape `Pool.Revive` produces.
- The child comes up on the caller's first `Pool.Activate`, through the existing lazy-respawn path. No new lifecycle path.
- Error shapes are inherited unchanged from today's `CreateIn`: `("", err)` when the failure is at or before the persist (nothing on disk, nothing in memory, the built session's settings file removed), `(id, err)` when `supervise` fails (the entry is on disk).
- Settings are `Pool.mintSettings()` — the operator's configured model and effort, read before `p.mu` is taken, exactly as today. Not the zero value: this is a mint, not a revive.

**`Pool.CreateIn`** becomes `Mint` + `Activate`, preserving its current observable behaviour byte-for-byte (the `RegisterAllocatedUUID` it performed is now performed by `Activate`, see below). `Pool.Create` is unchanged and keeps delegating to `CreateIn`, so `internal/control`'s `sessions.new` verb keeps bringing its session up — explicitly out of scope.

**`sessionMinter.Create`** (`cmd/pyry/main.go`) — the handler-facing `handlers.SessionCreator` implementation, and the *only* production caller of `CreateIn` — switches to `Mint`. `resolveSpawnDir` still runs first and is still the sole validator. The seam's `ctx` parameter becomes unused and is written `_ context.Context`, with a comment naming why (nothing here blocks any more).

**Nothing else moves.** The handler still mints, binds and eagerly persists; the row still carries a non-empty `CurrentSessionID` when `conversation_created` is replied. `set_session_settings` and `request_session_settings` are unchanged — the read half (`resolveBoundRunSettings` → `Pool.SettingsFor`) is a registry read that never touches a child, and `Pool.UpdateSettings` already documents the evicted case ("the argv install alone applies on the next Activate, and the caller still sees success").

### Where the skip-set prime goes

`RegisterAllocatedUUID` exists so that claude's CREATE of `<uuid>.jsonl` is not read by the rotation watcher as a `/clear`. Its entry has a 30s TTL (`allocatedTTL`), so priming at mint and spawning minutes later leaves the entry gone by the time the child opens the transcript — the watcher would rotate a brand-new session on its very first turn.

The prime moves to **`Pool.Activate`**, immediately after its `Lookup` resolves the target and before either `sess.Activate` call. That is the pool's single cap-enforcing spawn entry, so one site covers every deferred spawn: `CreateIn`'s own immediate activate, the msgqueue drain's `boundSession.Activate`, `GetOrCreateIn`'s register-path activate, and a reactivation after idle eviction.

Three properties make this safe rather than merely convenient:

- **It cannot mask a real rotation.** A `/clear` produces a *different* uuid; priming `X` only ever skips a `CREATE` of `X.jsonl`, which is exactly the event we want skipped when `X` is the session being spawned.
- **Re-priming is harmless.** `Pool.IsAllocated` consumes on first hit and `pruneAllocatedLocked` drops the rest at TTL, so an entry primed for an already-live session or a no-op activate simply expires.
- **The `RotateForNewSession` ordering invariant is preserved.** The prime completes before the activate signal is delivered, so registration precedes the spawn it protects.

The prime inside `Pool.materialise` stays where it is: it is documented as living inside the register critical section so a concurrent watcher snapshot sees register + skip-set atomically, and `Revive`'s caller still relies on it. It becomes belt-and-suspenders relative to the `Activate` prime rather than dead — different fabric is not required here because both are the same deterministic map write, and removing it would be an out-of-scope change to the attach and revive paths.

### Prose corrections (no behaviour)

- `createConversationMintTimeout`'s doc comment argues its 30s budget from "Pool activation blocks until claude's PTY is ready". The bound is kept — it still turns a wedged registry save or a wedged `resolveSpawnDir` into a retryable reply rather than pinning the conn's app-frame worker — but the rationale is rewritten to what actually blocks now.
- `CreateConversation`'s docstring and its `SECURITY:` block, plus `SessionCreator`'s contract comment, say the mint spawns. Corrected in place to "mints and binds; the child comes up on the conversation's first message".
- The three reconcile e2e headers and their timeout diagnostics assert "an over-the-wire `create_conversation` is a spawning `Pool.Activate`". Each already drives a turn, so each keeps passing; the sentences are corrected to point at the turn as the thing that brings the child up.

## Concurrency model

No new goroutines, no new locks, no lock-order change.

- `Mint` inherits `CreateIn`'s discipline verbatim: `buildSession` off-lock, then `p.mu` held across `p.sessions[id] = sess` + `saveLocked` (with in-memory rollback and settings-file removal on save failure), released before `RegisterAllocatedUUID` and `supervise`. `supervise` takes `p.mu.RLock` briefly to snapshot the run group. `Pool.mu → Session.lcMu` and `Pool.capMu → Pool.mu → Session.lcMu` are untouched.
- The prime added to `Pool.Activate` takes `p.mu` (write) via the exported `RegisterAllocatedUUID`, **before** `p.capMu` is taken. Taking `p.mu` and releasing it before `capMu` acquires nothing and inverts nothing: the documented order is `capMu → mu`, and a completed-then-released `mu` acquisition ahead of `capMu` cannot participate in a cycle. `Pool.Lookup` (which already runs first in `Activate`) takes `p.mu.RLock` in the same position, so the shape is already precedented on this exact path.
- The lifecycle goroutine scheduled by `supervise` exits on the pool's run context, unchanged. A minted-but-never-activated session's goroutine parks on the activate signal exactly as an idle-evicted one's does — it is not a leak, it is the existing evicted state.

## Error handling

| Failure | Where | Result |
|---|---|---|
| `NewID` / `buildSession` fails | `Mint` | `("", err)`; nothing registered; settings file removed by `buildSession`'s own defer |
| `saveLocked` fails | `Mint` | `("", err)`; in-memory registration rolled back, settings file removed — unchanged from `CreateIn` |
| `supervise` fails (`ErrPoolNotRunning`) | `Mint` | `(id, ErrPoolNotRunning)` — entry on disk, unchanged from `CreateIn` |
| `resolveSpawnDir` rejects the `Cwd` | `sessionMinter.Create` | wraps `handlers.ErrSpawnDirRejected` → non-retryable `protocol.malformed`, no row created — unchanged |
| Any other mint failure | `CreateConversation` | retryable `server.binary_offline`, handler returns before `reg.Create` — unchanged |
| Spawn fails on the first message | `newInboundDeliver` → `boundSession.Activate` | the msgqueue drain's existing retry/give-up path, identical to an idle-evicted conversation's reactivation today |

The one moved failure is worth naming: a spawn that would previously have failed the `create_conversation` reply now fails the first *delivery* instead, where the drain retries it and `OnGiveUp` surfaces a typed `session_error`. That is strictly the treatment an idle-evicted conversation already receives, and the ticket records the operator-visible consequence (waiting for startup after sending, rather than during creation) as accepted on 2026-09-04.

## Testing strategy

**Unit — `internal/sessions`:**

- `TestPool_Mint_RegistersWithoutSpawning` — `Mint` returns a valid id; the session is in the pool, on disk, in `stateEvicted`, and no child PID appears within a poll window. RED before the extraction exists.
- `TestPool_Mint_ThenActivateSpawns` — a subsequent `Pool.Activate` brings the same id up (`ChildPID > 0`), proving the lazy path is the ordinary one.
- `TestPool_Activate_PrimesAllocatedUUID` — `Mint`, force `allocatedTTL` expiry (the `pool_test.go` pattern that swaps the package var), then `Activate`, then assert `IsAllocated(id)` is true. This is AC#4's regression: it fails against a prime that stayed at mint time.
- `TestPool_CreateIn_StillActivates` — the out-of-scope control path: `CreateIn` (and therefore `Create`) still spawns.

**Unit — `internal/relay/handlers`:** the existing `create_conversation_test.go` suite drives a `stubSessionCreator` and asserts the row, the reply and the error mapping. It is behaviour-unchanged and is the proof that binding stays eager.

**e2e — `internal/e2e` (fake-claude tier), the larger half:**

- A new helper drives one message on a conversation and drains the wire until that turn closes, then asserts the bound session reached `active`. It is the "something that actually activates the session" AC#5 demands, and it exists so the eviction tests' later assertions are not satisfied by a session that was never active.
- `TestE2E_PerConversation_IdleEvictsAndReactivates` — each of the two conversations is activated through that helper before the `evicted` wait. Draining the priming turn to its terminal `turn_state{idle}` is load-bearing: the AC#2 drain hard-fails on a delta for `convA` that lacks the wake marker, so the priming turn's own deltas must be off the wire before the reactivation phase begins. Every existing assertion is kept as-is.
- `TestE2E_PerConversation_CapEvictsCrossDiscussion` — each create is followed by the same activation, so the cap is driven by three real activations rather than three creates. The LRU victim sequence is unchanged (bootstrap, then `boundA`) because each activation touches `lastActiveAt` in creation order.
- `relay_v2_stream_run_config_test.go` is left untouched and is the standing proof of AC#2: it creates a conversation, drives no turn, and reads and writes its settings.

**Not run here:** the full-module race suite and `make e2e-realclaude` are the verifier's gate. The three realclaude per-conversation liveness cases already send a message after their create, so they exercise the new first-message spawn without edits.

## Open questions

1. Does the `Pool.Activate` prime need to be skipped when the target is already active? Resolved in the design above: no — `IsAllocated` consumes on hit and expired entries are pruned on every write, so a redundant entry costs one map slot for at most `allocatedTTL`.
2. Should `createConversationMintTimeout` be removed now that the mint cannot block on a spawn? Resolved: keep it. It still bounds the registry save and `resolveSpawnDir`'s filesystem work, removing it would change the handler's error surface, and the ticket asks only for the *rationale* to be corrected.
3. Does the deferral require re-validating the conversation's `Cwd` at the spawn site, as #1487's revive path does? Answered in § Security review below.

## Security review

**Verdict:** PASS

**Findings:**

- **[Trust boundaries] SHOULD FIX — restate the pool's non-validation contract on `Mint`.** The untrusted input is the phone's `Cwd`; the boundary is `resolveSpawnDir` at the cmd layer, and this ticket does not move it — it still runs inside `sessionMinter.Create`, still before `reg.Create`, still returning `trustMark`'s own realpath. What changes is that `Mint` becomes a *second* pool entry point taking a `spawnDir`, and a future caller reading only its signature could hand it a raw phone path. `CreateIn`, `GetOrCreateIn` and `Revive` each carry the "used verbatim and NOT validated, canonicalised, or trust-checked by the pool — callers supply a pre-resolved path" sentence for exactly this reason. `Mint` must carry it too. Implement in Phase B; the verifier checks it landed.
- **[Trust boundaries] No further findings — the phone gains no new capability.** The deferral lets the phone choose *when* the spawn happens, but it could already choose when to send a message. The resolved realpath is frozen onto the session at `buildSession` and is never re-read from phone-influenced state, so the phone cannot influence the path after validation.
- **[File operations] ACCEPTED RISK, recorded — the validated-then-spawn window widens from milliseconds to unbounded.** `resolveSpawnDir` canonicalises with `EvalSymlinks`, confines to `$HOME`, `MkdirAll`s only behind that check, trust-marks, and returns the realpath; that realpath is what the child chdirs to. Winning the widened window requires replacing an *ancestor* of an already-resolved realpath under the operator's `$HOME` with a symlink pointing outside it — i.e. write access to the operator's home, which is exactly what the confinement protects and what claude itself already has. **The decision is to accept**, and it is written down twice: here, and as a comment at `sessionMinter.Create`, the site that now defers. The reasoning that separates this from #1487, which *does* re-validate at its own spawn site: #1487 re-reads a **raw, persisted** `conv.Cwd` from a mutable file across a **daemon restart**, so its stored value is unvalidated bytes from a previous process lifetime; here the value is an in-process, already-resolved realpath held on the `*Session`, and no disk read stands between the check and the spawn. This is a widening of the residual window `conversation-session-binding.md` already records as accepted, not a new class of exposure.
- **[File operations] OUT OF SCOPE — re-validating at the spawn site.** The pool contractually does not validate paths and must not import the cmd-layer validator; `boundSession.Activate` holds no conversation row and the session's `WorkDir` is fixed at build, so re-validation would need a new pool primitive plus a `trustMark` write (into `~/.claude.json`) on every message delivery. Disproportionate for a window whose winner already owns `$HOME`. If it is ever wanted, it belongs with #1475 (the open, human-gated ticket for reading a stored conversation `Cwd` at spawn), which this ticket is explicitly barred from touching.
- **[File operations] No findings on modes or atomicity — no new file is created.** `writeMCPSettings` (per-session `--settings`) and `saveRegistryLocked` (atomic temp+fsync+rename) both still run at mint time, unchanged in mode and in ordering. `Mint` inherits `CreateIn`'s settings-file removal on a save-failure rollback verbatim.
- **[Subprocess execution] No findings — the set of writers to claude's argv is unchanged.** Model, effort and permission mode reach the argv only through `Pool.UpdateSettings` (which rejects an unrecognised mode) and `Pool.mintSettings` (operator config, never the bypass). The deferral moves *when* the suffix is composed, not *who* may write it, so no unvalidated value gains a path to `exec`. Launch-time bypass posture is governed by streamsup's spawn-time posture gate rather than by flag presence (#2065), so a setting applied before the first spawn lands under the same gate a mid-session change does.
- **[Tokens, secrets, credentials] No findings — none are minted, stored, or moved.** Session ids remain `sessions.NewID` (`crypto/rand`); the change adds no token lifecycle.
- **[Cryptographic primitives] No findings — no primitive is selected, used, or configured by this change.**
- **[Network & I/O] Spawn amplification is REDUCED, and the deferred follow-up's premise changes.** `conversation-session-binding.md` records "an authenticated phone spamming creates can exhaust host processes/memory" as a deferred #672-family threat whose only in-architecture bound is the default-uncapped `Pool.ActiveCap`. After this change a create costs a registry row, a per-session settings file and one parked lifecycle goroutine — the same residue a create already left today, minus the process. Strictly cheaper than the status quo in every dimension; no new exhaustion vector is introduced.
- **[Error messages, logs, telemetry] SHOULD FIX — `Mint` must contain no log call.** Its `spawnDir` parameter is a phone-influenced workspace path, which `Pool.Revive`'s contract already marks as never-loggable (the #741 precedent covering session ids and conversation ids). `CreateIn` logs nothing today and the extraction must keep it that way. The handler's own log fields (`conn_id`, `conversation_id`, `session_id`) are unchanged and carry no path.
- **[Concurrency] No findings — no lock-order inversion, and the added acquisition is precedented on this exact path.** The documented order is `Pool.capMu → Pool.mu → Session.lcMu`. The prime added to `Pool.Activate` acquires `p.mu` through `RegisterAllocatedUUID`, which releases it under its own `defer` before `Activate` reaches `p.capMu` — no goroutine ever holds `mu` while acquiring `capMu`, so no cycle exists. `Pool.Lookup` already takes and releases `p.mu.RLock` at that same position. Honest cost, not a defect: the uncapped delivery path now takes one write-lock (plus `pruneAllocatedLocked`'s scan, bounded by activations within `allocatedTTL`) where it previously took only a read-lock.
- **[Concurrency] No findings on goroutine lifecycle.** `Mint` keeps the `supervise` call, so every minted session still has exactly one lifecycle goroutine bound to the pool's run context. A minted-but-never-messaged session parks on the activate signal — byte-for-byte the state an idle-evicted session already occupies, and it exits on the same run-context cancellation. Nothing is spawned that lacks a shutdown path.
- **[Threat model alignment] No findings against `docs/protocol-mobile.md` § Security model.** The hostile-paired-phone capabilities that touch this path — choosing a `Cwd`, choosing settings, choosing when to send — are each validated by an unchanged gate, and the confused-deputy guard that matters most (`sessionRouter.resolve` rejecting an empty `CurrentSessionID` **before** any `Lookup`, so an unbound conversation can never be routed into the shared bootstrap claude) is untouched: this ticket keeps the bind eager precisely so that guard never has to fire on a fresh conversation.

**Reviewer:** builder (self-review per the security-review checklist)
**Date:** 2026-09-06

## Revisions

### 2026-09-06 — verifier findings on PR #2130 (comment accuracy)

No production logic changed; three claims this ticket made false were corrected, and one of them corrects a resolution recorded above.

- **Open question 2's resolution rationale was wrong, and is superseded.** It kept `createConversationMintTimeout` on the grounds that it "still bounds the registry save and `resolveSpawnDir`'s filesystem work". It bounds neither: `sessionMinter.Create` discards the ctx, `resolveSpawnDir` takes no `context.Context`, `Pool.Mint` is ctx-free by design, and a deadline does not interrupt a blocking filesystem syscall. The decision to keep the constant stands — the interface advertises a ctx and a future ctx-honouring `SessionCreator` would get a bound — but its doc comment, the `mintCtx` construction comment, the retryable-error enumeration (which listed a ctx deadline that can no longer occur) and `sessionMinter.Create`'s own comment now say plainly that it protects nothing against this implementation.
- **`Pool.Create`'s `Sequence:` line still described the skip-set prime between the persist and the supervise.** That is the one ordering this change moved, it is the ordering AC#4 turns on, and `CreateIn` redirects readers to it as the canonical description. Corrected to name `Pool.Activate` as the prime site, with the TTL reason.
- **The reading list stopped at `interactive_per_conversation_liveness_test.go`, so the realclaude tier was swept for behaviour but not for prose.** Correct on behaviour — every case there and in `interactive_conversation_lifecycle_test.go` sends a message after its create, so the live gate stays green with no edit. Wrong on prose: the transcribed `createConversationViaPhone`, `TestInteractivePerConversationLiveness_ActiveSwitch` and `TestInteractiveConversationLifecycle` each asserted the conversation was live on return from the create.
- **One of those was a coverage claim, not just a wording slip, and it is recorded rather than repaired.** `TestInteractiveConversationLifecycle`'s liveness turn was annotated as showing "the live session survived" the archive round-trip. With the child now coming up on that same turn, it shows the row is still routable after the round-trip but no longer witnesses a *running* child surviving it. Not bought back: archive/unarchive is a registry-metadata flip that never reaches the pool (`TestRelayV2_Archive` drives it against a seeded row with no session at all), so there is no path by which it could tear a child down, and restoring the witness costs a second live claude turn in `make preship` plus a turn-drain helper this package does not have. The loss and the reasoning are written at the step itself.
- **`mintEvicted` (`pool_update_settings_restart_test.go`) is `Pool.Mint`'s sequence by hand** — it cannot delegate, because `Mint` takes `mintSettings()` while those tests plant a specific model to update away from. Its stale `RegisterAllocatedUUID` call is dropped so the helper matches the real one, and a comment names the divergence risk.
