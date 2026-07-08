# Spec #842 — live-apply per-session settings by restarting the running session

**Ticket:** pyrycode/pyrycode#842 · **Size:** S · **Labels:** `security-sensitive`
**Depends on:** #845 (merged — persist path + trigger point), #833/#840 (`SessionSettings` / `claudeSettingsArgs` / `Pool.UpdateSettings`).

## Files to read first

- `internal/supervisor/supervisor.go:570-646` — `Run` loop + `buildClaudeArgs`. The loop reads `s.cfg.ClaudeArgs` once per iteration (line 594) and passes the main `ctx` straight to `runOnce`. **This is the seam that gets swapped** (read args from a live field; derive a per-iteration ctx).
- `internal/supervisor/supervisor.go:686-844` — `runOnce`: `exec.CommandContext(ctx, …)` is how the child is killed (ctx cancel → SIGKILL → `sess.Wait()` returns). The restart mechanism induces this exit on a *derived* ctx, not the supervisor's ctx.
- `internal/supervisor/supervisor.go:497-537` — `setSession`/`WaitForPTY`/`sessReadyCh` choreography (the pattern the restart does **not** need to duplicate — the restart is fire-and-forget, see § Error handling).
- `internal/sessions/pool.go:585-610` — `Pool.UpdateSettings` (the persist tail this ticket extends). Note it holds `p.mu` across merge+`saveLocked` and returns; the restart trigger is appended **after** `p.mu` is released.
- `internal/sessions/pool.go:394-461` — `Pool.New` bootstrap supervisor build: `ClaudeArgs = clone(Bootstrap.ClaudeArgs) + claudeSettingsArgs(settings)`, `ResumeLast = cfg.Bootstrap.ResumeLast`. Store the **base** (settings-free) args here.
- `internal/sessions/pool.go:1122-1170` — `buildSession` (minted): `args = clone(tpl.ClaudeArgs) + "--session-id" + id + claudeSettingsArgs(settings)`, `ResumeLast: false`. Store the base here too.
- `internal/sessions/session.go:61-109` — `SessionSettings`, `SettingsUpdate`, `claudeSettingsArgs` (the **single** argv-composition helper; the YOLO fail-safe lives here — reused verbatim).
- `internal/sessions/session.go:434-498` — `runActive` runs `s.sup.Run(subCtx)` in a goroutine and only reacts when `Run` *returns*. The in-place restart keeps `Run` running (it loops internally), so `runActive` and the `active↔evicted` state machine are untouched — the restart never drives `lcMu`.
- `internal/supervisor/supervisor_test.go:395-409` — shutdown-return contract: the test accepts `context.Canceled`/`DeadlineExceeded`/`nil`. Preserved by returning `ctx.Err()` on the shutdown path.
- `docs/knowledge/codebase/840.md` § "Why no race with a running supervisor" — the value-copy-at-construction argument that this ticket deliberately changes (args now re-read live under a mutex).
- `docs/lessons.md` § lock order (`Pool.mu → Session.lcMu`) — the restart trigger must run **outside** `Pool.mu` and must not touch `lcMu`.

## Context

The v2 `set_session_settings` verb (#845) persists a session's model / effort / YOLO via `Pool.UpdateSettings` and applies them **on the session's next spawn**. But the supervisor bakes its spawn argv at construction (`Config.ClaudeArgs`, read once per `Run` iteration) and never re-reads the live `SessionSettings`, so a *running* session keeps its old model/effort/YOLO until it restarts for some unrelated reason. YOLO has no live actuator and effort has no live slash command, so the one mechanism that applies all three uniformly to a running session is a **restart: kill the child, let the supervisor relaunch it with the new argv, resuming the conversation.**

This ticket adds the missing seam — swap the supervisor's spawn args and force the running child to relaunch — and drives it from `UpdateSettings`'s successful persist so one client message both persists and takes effect (AC #3).

**A note on the "next Activate" claim.** The ticket states evicted sessions apply persisted settings "on the next Activate (the existing #833 behaviour)." In today's code that is only true for the bootstrap-across-*daemon*-restart path (`Pool.New` re-reads the registry). A *reused* supervisor (evict → activate within one process) re-runs `s.sup.Run` with the **stale baked args** — a latent gap from #833. This spec's swap is **unconditional** (the supervisor's live args are updated whether or not a child is running), so it closes that gap for free: an evicted session's next `Activate` reads the swapped args. The *kill* is the only conditional part (there must be a live child to kill).

## Design

Two mechanisms, in-place, no supervisor rebuild:

1. **Supervisor: live-swappable spawn args + induced restart** (`internal/supervisor`).
2. **Sessions: recompose argv from the persisted settings and trigger the restart** (`internal/sessions`), appended to the existing `UpdateSettings` tail.

The design principle: **a settings restart is just an induced child-exit.** The supervisor's existing restart loop already knows how to relaunch-and-resume (that is its entire purpose); the only new thing is swapping the argv *before* the relaunch. Resume semantics (AC #2) are therefore inherited verbatim from the crash-recovery path — `buildClaudeArgs(liveArgs, firstRun=false, ResumeLast)` prepends `--continue` for the bootstrap (ResumeLast=true) and relies on the baked `--session-id <id>` for minted sessions, exactly as a spontaneous respawn does today.

### Supervisor changes (`internal/supervisor/supervisor.go`)

New unexported state on `Supervisor`, guarded by a new **leaf** mutex `restartMu` (never nested with `mu`/`sessMu`/`convMu`):

- `claudeArgs []string` — the live spawn args. Initialised in `New` from `cfg.ClaudeArgs`. `Run` reads it (not `s.cfg.ClaudeArgs`) at the top of each iteration.
- `iterCancel context.CancelFunc` — the current iteration's cancel; set at iteration start, cleared (`nil`) after `runOnce` returns.
- `restartCh chan struct{}` (buffered 1, allocated once in `New`) — a deliberate-restart hint: consumed post-`runOnce` to skip backoff, and watched inside the backoff wait to interrupt it.

New exported method — the seam the sessions layer calls:

```go
// Restart swaps the claude spawn args and, if a child is currently running,
// forces it to exit so the restart loop relaunches with the new args. When no
// child is running, only the args are swapped — they take effect on the next
// spawn (e.g. the session's next Activate). Non-blocking, fire-and-forget: the
// supervisor's forever-retry loop guarantees the relaunch. Safe from any
// goroutine.
func (s *Supervisor) Restart(args []string)
```

Behaviour (≤15 lines): under `restartMu` set `claudeArgs = slices.Clone(args)` and capture `cancel := s.iterCancel`; release. If `cancel == nil` return (no live child; swap alone). Otherwise non-blocking-send on `restartCh` (`select { case s.restartCh <- struct{}{}: default: }`) **then** call `cancel()`. The send-before-cancel order guarantees `Run`'s post-`runOnce` drain observes the hint.

`Run` loop restructure (behaviour-identical when `Restart` is never called):

- Read args live: `args := buildClaudeArgs(s.liveArgs(), firstRun, s.cfg.ResumeLast)` where `liveArgs()` clones `claudeArgs` under `restartMu`.
- Per-iteration ctx: `iterCtx, cancel := context.WithCancel(ctx)`; `s.setIterCancel(cancel)`; `err := s.runOnce(iterCtx, args, onSpawn)`; `cancel()`; `s.setIterCancel(nil)`.
- **Shutdown detection moves from the error to the ctx:** replace the `case errors.Is(err, context.Canceled): return err` arm with `if ctx.Err() != nil { return ctx.Err() }` immediately after `runOnce`. This is what lets an `iterCtx`-only cancel (a restart kill) fall through to the loop instead of being mistaken for shutdown. The shutdown-return value stays `context.Canceled` (test at `supervisor_test.go:397` accepts it).
- After logging the exit and setting `firstRun = false`: drain `restartCh` non-blocking → `deliberate`. If `deliberate`, `continue` (skip backoff — a settings restart is not a crash). Otherwise enter the backoff wait, adding `case <-s.restartCh:` to its `select` so a restart arriving *during* backoff (child already crashed) breaks the wait and relaunches immediately.

`runOnce` needs **no change** beyond receiving `iterCtx` in place of `ctx` (it already threads its parameter into `exec.CommandContext` and every `sess.*` ctx). Killing `iterCtx` sends SIGKILL and unblocks `sess.Wait()` exactly as the existing shutdown path does.

**Invariant to preserve:** when `Restart` is never called, `liveArgs()` returns the `cfg.ClaudeArgs` clone and the per-iteration ctx is a pure pass-through of the supervisor ctx — argv and lifecycle are byte-for-byte the pre-#842 behaviour. Assert this (§ Testing).

### Sessions changes (`internal/sessions`)

**Store the settings-free base args per session** so the restart can recompose full argv from the persisted settings without re-deriving the two assembly recipes:

- `Session` gains `spawnBase []string` — full spawn args **minus** the `claudeSettingsArgs` suffix.
- `buildSession` (minted): `base := append(slices.Clone(tpl.ClaudeArgs), "--session-id", string(id))`; `full := append(slices.Clone(base), claudeSettingsArgs(settings)...)`; store `spawnBase: base`, pass `full` as `ClaudeArgs`.
- `Pool.New` (bootstrap): `base := slices.Clone(cfg.Bootstrap.ClaudeArgs)`; `full := append(slices.Clone(base), claudeSettingsArgs(settings)...)`; store `spawnBase: base` on the bootstrap `Session`, pass `full` as `ClaudeArgs`.
- One helper, the **single** recompose site (mirrors the fail-safe single-source rule):

```go
// spawnArgs composes the full claude argv for the given settings: the
// settings-free base plus claudeSettingsArgs(settings). The only argv
// recompose path outside session construction; both route through
// claudeSettingsArgs, so the YOLO fail-safe is enforced in exactly one place.
func (s *Session) spawnArgs(settings SessionSettings) []string
```

Construction argv is byte-identical to today (base + `claudeSettingsArgs`; zero settings → `nil` append).

**Extend `Pool.UpdateSettings`'s tail** (`pool.go:585`). Convert the `defer p.mu.Unlock()` to explicit unlocks so the restart trigger runs outside `Pool.mu` (lock-order rule). After the successful `saveLocked` and while still under `p.mu`, compute `newArgs := sess.spawnArgs(merged)` (reads immutable `spawnBase` + local `merged`) and capture `sup := sess.sup`; unlock `p.mu`; then `sup.Restart(newArgs)`; return nil. Unchanged branches: not-found → `ErrSessionNotFound`; no-op (`merged == sess.settings`) → return nil **without** calling `Restart` (nothing changed → nothing to relaunch); `saveLocked` error → roll back `sess.settings`, return the error, **no** `Restart` (persist failed → don't disturb the running child).

`Restart` is called for **both** running and evicted sessions — the swap is unconditional; the supervisor decides internally whether a kill is needed.

### Data flow

```
phone → relay → handleSetSessionSettings (#845, unchanged)
      → settingsUpdaterAdapter.UpdateSettings (cmd/pyry, unchanged)
      → Pool.UpdateSettings:
            [p.mu] merge → saveLocked (persist) → newArgs = spawnArgs(merged) [/p.mu]
            sup.Restart(newArgs)            ── non-blocking, outside p.mu
                 └─ swap claudeArgs; if live child: hint restartCh + cancel iterCtx
Supervisor.Run loop (already running under runActive, not re-entered):
     runOnce(iterCtx) unblocks (SIGKILL) → ctx.Err()==nil → drain restartCh (deliberate)
     → skip backoff → buildClaudeArgs(liveArgs, firstRun=false, ResumeLast)
     → respawn claude with new argv, resuming the conversation
```

## Concurrency model

- **No new goroutine.** The restart runs the supervisor's existing `Run` goroutine (owned by `runActive`). `Restart` is a synchronous swap+signal on the caller's goroutine (the relay dispatch goroutine, via `UpdateSettings`).
- **New lock `restartMu`** guards `claudeArgs` + `iterCancel`, both tiny critical sections. Leaf-only: `Restart` **releases** `restartMu` before calling `cancel()` (cancel doesn't re-enter `restartMu`), and `Run`'s `setIterCancel`/`liveArgs` take only `restartMu`. Never nested with `mu`, `sessMu`, `convMu`.
- **`restartCh` (buffered 1)** carries exactly the "a deliberate restart happened" hint. One `Restart` → at most one token; consumed in exactly one place (post-`runOnce` drain **or** the backoff-wait `select`, never both, because a during-run restart makes `runOnce` return before the wait is reached, and a during-backoff restart never queued a token the drain could have taken). Coalescing (two rapid restarts) is correct: the second `Restart` overwrites `claudeArgs` with the newest value and its dropped send is immaterial — the single pending token still forces one relaunch with the latest args.
- **Lock order.** `UpdateSettings` performs the persist under `Pool.mu`, releases it, then calls `Restart`. The restart never acquires `Pool.mu` or any `Session.lcMu` — it drives only supervisor-internal state. The `Pool.mu → Session.lcMu` order is untouched, and no blocking work happens under `Pool.mu` (`Restart` is non-blocking).
- **Session state machine untouched.** Because the restart keeps `Supervisor.Run` running (it loops internally rather than returning), `runActive`'s `<-runErr` never fires; the session stays `stateActive` across the restart. No `lcMu`, no `transitionTo`, no registry write beyond the `saveLocked` the persist already did.
- **Args-swap visibility.** `claudeArgs` is swapped as a whole slice under `restartMu` and read under `restartMu` by `Run`; no torn read, and the value-copy-at-construction argument from #840 is deliberately replaced by this mutex-guarded live read.

## Error handling

**AC #4 contract choice — "recover to a defined state" (the ticket's option b), via a deterministic kill + the supervisor's forever-retry loop.** The restart is fire-and-forget and does **not** block `UpdateSettings` waiting for the relaunch, for two reasons: (1) the #845 handler runs synchronously on the v2 manager's single dispatch goroutine — blocking it for a kill+respawn would stall the whole mobile connection's frame processing; (2) a synchronous wait would need the caller's ctx threaded through the `SettingsUpdater` interface (a cross-package signature change), which the non-blocking design avoids entirely.

The forbidden AC #4 state — *"settings on disk, stale child still running, success reply sent"* — cannot occur:

- **Stale child is terminated deterministically.** `cancel()` on `iterCtx` sends SIGKILL (os/exec guarantee) and unblocks `sess.Wait()`. There is no path where the persist succeeds and the old (stale-settings) child keeps running: the kill is unconditional whenever a live child exists.
- **Never left dead.** `Supervisor.Run` retries spawns forever with backoff; a transient relaunch failure (e.g. `claude` momentarily unavailable) is retried, not abandoned. The session is never wedged in a childless state by a settings change — the same reliability guarantee the daemon already relies on for crash recovery.
- **The success reply is honest:** persisted **and** the stale-settings child has been terminated **and** the supervisor will bring the session back with the new argv. This is the same "the change is applied; the process reflecting the old state is gone" contract every restart provides.

Other failure modes:

- **Persist fails** → `saveLocked` error returned, `sess.settings` rolled back, **no `Restart`** → running child keeps its (still-correct) old settings. No half-apply.
- **Unknown session** → `ErrSessionNotFound`, no `Restart`. (Adapter maps to `relay.ErrSessionUnknown`, unchanged.)
- **No-op change** (`merged == sess.settings`) → return nil, **no `Restart`** → no gratuitous child kill / conversation-resume churn.
- **Restart races an eviction/shutdown.** If `runActive`'s ctx is cancelled (idle eviction / daemon stop) concurrently with the kill, `Run` returns and no respawn happens — the session evicts with settings persisted; they apply on next `Activate` (the evicted-session carve-out). Defined state, not dead.
- **Kill mid-turn.** SIGKILL can drop an uncommitted assistant turn (identical to a crash); committed history is on claude's JSONL and resumes. Inherent to "kill + resume"; in scope by the ticket's own definition.

## Testing strategy

`internal/supervisor` (table-driven, `-race`, `TestHelperProcess` fake child):

- **Byte-identical baseline (no `Restart`):** `Run` a fake child through a normal spawn→exit→backoff cycle; assert argv and phase transitions are unchanged from pre-#842 (guards the `liveArgs`/`iterCtx` refactor). Reuse the `spawn_test.go` / recorder idiom.
- **Restart swaps argv on a running child:** spawn a long-lived fake child; call `Restart(newArgs)`; assert the next spawn's argv reflects `newArgs`, the child PID changed, and backoff was **skipped** (relaunch is prompt, not after the backoff delay).
- **Restart with no live child only swaps:** call `Restart` before `Run` (or after the child exited); assert no panic and that the *next* `Run`/spawn uses the swapped args.
- **Restart during backoff interrupts the wait:** force the child to exit into backoff, then `Restart`; assert the relaunch happens promptly (the `restartCh` arm of the backoff select) with the new args.
- **Shutdown still returns `context.Canceled`:** cancel the supervisor ctx while a child runs; assert `Run` returns a context error and does **not** treat it as a restart (regression guard for the moved shutdown check).
- **Coalescing:** two rapid `Restart` calls → the final spawn uses the last args; exactly one relaunch.

`internal/sessions` (table-driven, `-race`, recorder harness from `pool_settings_test.go`):

- **`UpdateSettings` on a running session triggers a relaunch with the new argv** (`--model`/`--effort`/`--dangerously-skip-permissions`), and the resumed spawn carries the resume flag appropriate to the session (bootstrap: `--continue`; minted: `--session-id <id>`) — **AC #1 + #2**.
- **Single message → persisted *and* live** (AC #3): one `UpdateSettings` call both writes the registry (assert on disk) and relaunches (assert argv).
- **No-op update does not relaunch** (assert the recorder shows no second spawn; registry mtime stable).
- **Persist failure does not relaunch** and rolls back settings (inject a `saveLocked` failure).
- **Evicted session: swap only, no kill** — `UpdateSettings` on an evicted session persists and swaps; the next `Activate` spawns with the new argv (closes the latent gap); no spurious spawn while evicted.
- **YOLO true→false terminates the bypass child** (§ Security): with a running YOLO child, `UpdateSettings{YOLO: ptr(false)}` relaunches **without** `--dangerously-skip-permissions`.
- **YOLO absent/false never yields the bypass flag** across the restart (the fail-safe, asserted on recomposed argv).

## Security review

*(Mandatory: ticket is `security-sensitive`. Pass performed on this spec before commit. Verdict below.)*

**Trust boundaries.** The untrusted-write surface (a paired phone's `set_session_settings` payload) is #845's boundary and is already reviewed there: capability-gated, `model`/`effort` shape-validated, YOLO `*bool` presence-contracted, all before `UpdateSettings`. **This ticket adds no new inbound surface.** It consumes the already-persisted, already-validated `SessionSettings` and turns it into a fresh child. The new security-relevant surfaces are exactly two: (a) recomposing the spawn argv on restart, and (b) the *timing* of terminating a bypass-permissions child when YOLO is revoked.

**Assets & threats walked:**

1. **YOLO fail-safe-OFF must survive the restart (primary asset).** The recomposed argv is produced **only** by `Session.spawnArgs → claudeSettingsArgs(merged)` — the same deterministic helper #833/#840/#845 already enforce. `claudeSettingsArgs` emits `--dangerously-skip-permissions` **iff** `YOLO == true` and emits no "disable" flag; a `false`/absent-then-`false` YOLO yields argv without the flag. The single-source invariant is the whole argument: **the bypass flag has exactly one origin (`claudeSettingsArgs`), and `spawnBase` never contains it** (`spawnBase` = operator-controlled `tpl.ClaudeArgs`/`Bootstrap.ClaudeArgs` + `--session-id`, none YOLO-derived). So no persisted-false state can recompose into a bypass child. Deterministic code, no stochastic component — belt-and-suspenders is different fabric (pointer-nil presence in #840 + single-source argv here).
2. **YOLO revocation must terminate the bypass process (security-*positive*).** Without a live restart, flipping YOLO `true→false` would persist `false` while a `--dangerously-skip-permissions` child kept running indefinitely — a real regression. The unconditional kill on any real settings change (a `true→false` flip is a real change, not the no-op short-circuit) **terminates that bypass child promptly** and relaunches without the flag. This ticket is a net reduction in the YOLO exposure window. The residual window — between persisting `false` and SIGKILL landing — is bounded by signal delivery + `sess.Wait()` and is irreducible (a running process cannot be un-bypassed in place); the alternative (apply-on-next-spawn) leaves it open indefinitely.
3. **argv injection.** No new value crosses the trust boundary here — `model`/`effort` were validated by #845 before persistence, and the restart re-emits them as **separate argv tokens** under `exec.CommandContext` (no shell). `spawnBase` is operator-controlled. No injection surface is introduced.
4. **Torn / racing argv swap.** `claudeArgs` is swapped as a whole slice under `restartMu` and read under `restartMu`; a spawn can never observe a half-updated arg list (e.g. `--model` without its value, or a stale bypass flag mixed with new args).
5. **Privilege / blast radius.** The restart runs in the same process and user context; it spawns the same `claude` binary via the same code path with no elevation. It drives only supervisor-internal state (no `Pool.mu`/`lcMu`), so it cannot corrupt the registry or the session state machine.
6. **Denial-of-service via forced churn.** A flood of `set_session_settings` could induce repeated kills. This is bounded by the same upstream authz as #845 (interactive-capability gate, authenticated peer) and by the no-op short-circuit (identical settings don't relaunch); a distinct-value flood is no worse than the client repeatedly toggling its own session, which it is entitled to do. No new amplification.

**Decision criteria.** The one security-critical property — a persisted-false/absent YOLO can never yield a bypass child, and a revoked YOLO promptly loses its bypass child — is enforced by deterministic code with a single argv source reused verbatim, and the change is net-positive on the revocation path. No untrusted value is introduced or re-validated here. No finding requires a spec revision. **Verdict: PASS.**

## Open questions

- **Minted-session resume on induced restart.** The bootstrap (the live mobile consumer today) resumes via `--continue` (ResumeLast=true) — AC #2 is directly covered. A *minted* session's induced restart relaunches with `--session-id <id>` and no `--continue` (ResumeLast=false), inheriting the existing crash-recovery respawn path. The developer should confirm `claude --session-id <existing-id>` resumes rather than errors/forks on the second spawn; if it does not, minted-session live-restart would need `--continue` semantics. Not a blocker for the primary path — flag it in the PR if the minted assertion is hard to exercise.
- **`RestartCount` semantics.** A deliberate settings-restart skips backoff and does not increment `State().RestartCount` (which remains a crash-restart counter). If operators want settings-restarts surfaced in `State`, add a distinct counter in a follow-up — out of scope here.
