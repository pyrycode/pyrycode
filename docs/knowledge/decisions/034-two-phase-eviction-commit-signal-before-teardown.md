# 034. Eviction commits `stateEvicted` (signal + flip) BEFORE child teardown, persists after

## Status

Accepted (#1186)

## Context

[ADR 013](013-evict-activate-persist-ordering.md) made `transitionTo` symmetric for both directions: flip `lcState` (+ allocate the opposite-direction channel) under `lcMu`, persist with `lcMu` released, then close the current-direction wake channel. For the evicted direction, `Session.Run`'s outer loop only called `transitionTo(stateEvicted)` *after* `runActive` returned — i.e. after `cancelSup`/`drainSup` had already torn the child down. The session stayed `stateActive` for the entire teardown window.

That window is real wall-clock time (the child has to be signalled and reaped). A `send_message` delivery whose `Activate` call landed inside it read `lcState == stateActive`, so `Activate`'s already-active fast path no-op'd against a supervisor that was already dying — the turn was acked (enqueued) but never delivered, with no error and no respawn. #1186's evidence was a live real-claude run of the stream-runner gate (#1177): `TestInteractiveStreamResumeAfterEviction` sends its resume turn the instant it observes the `session.idle_eviction` WARN, landing squarely in the teardown window. The settled-state fake tests (#396 PTY, #680 per-conversation) don't catch this because they wait for the registry to read `"evicted"` before delivering — by construction, after the window has already closed. The race is generic to `internal/sessions` (any `Activate` caller racing any eviction path — idle timer, cap-policy `Evict`, or spontaneous exit), not stream-runner-specific; the stream real-claude gate is simply the first caller whose timing exposed it.

## Decision

Split the evicted-direction transition into two ordered halves, replacing `transitionTo(stateEvicted)`:

- **`beginEvict(reason)`** — runs *before* `cancelSup`/`drainSup`. Fires `notifyTransition` (if `reason != ""`), then under `lcMu` flips `lcState = stateEvicted`, stamps `lastActiveAt`, and swaps in a fresh open `activeCh`. Does **not** persist and does **not** close `evictedCh`.
- **`endEvict()`** — runs *after* the child has actually stopped. Persists the registry (now consistent with `stateEvicted`), then closes `evictedCh` under `lcMu`.

All three `runActive` exit paths (idle timer, cap-policy `s.evictCh`, and the defensive spontaneous-exit branch) now call `beginEvict` immediately before `cancelSup()`/`drainSup()` and `endEvict()` immediately after. `Session.Run`'s outer loop no longer calls `transitionTo(stateEvicted)` at all — `runActive` fully commits the eviction itself before returning. `transitionTo` is retained, unchanged, for the `stateActive` (reactivation) direction; ADR 013's contract still holds there in full.

A racing `Activate` now observes a non-active session mid-teardown, signals `activateCh`, and blocks on the fresh `activeCh` — driving a real respawn once `endEvict` (and the subsequent re-activation) completes, instead of no-op'ing against a dying child.

### Rework: signal must fire before the flip, not adjacent to the old flip position

The first cut of this split kept `notifyTransition` at its old call site — Run's outer loop, after `runActive` returned — while moving only the state flip into `beginEvict`. That decoupled the two: a consumer reading `LifecycleState() == stateEvicted` was no longer guaranteed the eviction transition had already fired, because the signal now fired teardown-window-later than the flip it used to be adjacent to. QA's `TestPool_TransitionObserver_IdleEvictionFires` caught this in ~2 of 46 `-race` runs. Fixed by threading `reason` into `beginEvict` and firing `notifyTransition` there, immediately before the `lcMu` flip — restoring the pre-#1186 "adjacent, no scheduling point between them" ordering, and giving a `lcMu`-mediated happens-before edge (notify → `Unlock` → observer's `Lock` reads `evicted`) that orders the signal ahead of `stateEvicted` becoming observable.

## Rationale

**Why not persist inside `beginEvict` too?** `Session.Evict`'s documented contract (cap-policy eviction) is "blocks until the supervisor has stopped." Closing `evictedCh` before the child is reaped would release an `Evict` waiter while a process is still exiting — a transient active-cap overshoot the concurrent-cap machinery exists to prevent. Persist-then-close must stay *after* teardown; only the signal-and-flip half needed to move earlier. This is why the split is two named phases rather than one earlier `transitionTo` call.

**Why this doesn't touch the `stateActive` direction.** Reactivation isn't racing a teardown window — there's no "child being torn down" moment to protect a concurrent reader from. ADR 013's single-`transitionTo` shape is still correct and simplest there; splitting it would be unmotivated by any observed failure (Evidence-Based Fix Selection).

**Why a hermetic fake-tier test, not just the real-claude gate.** `internal/sessions/session_evict_race_test.go`'s `TestSession_IdleEviction_ActivateRacingTeardownRespawns` is the deterministic sibling to #396's `TestE2E_IdleEviction_RespawnsOnSendMessage`: a `raceRunner` Runner double holds the teardown window open on its first `Run` invocation, a test goroutine injects `Activate` into that held-open window, and the test asserts a second spawn follows once the window releases. It fails on `main` (no respawn — the #1186 silent-drop) and passes with the fix, clean under `-race`. This pins the contract without a live `claude`, so #1177 (real-claude, operator-gated) is confirmation of the live behavior, not the sole proof the fix works.

## Consequences

- **ADR 013's "symmetric both directions" claim is now accurate only for `stateActive`.** For `stateEvicted`, the flip happens inside `runActive` via `beginEvict`, before teardown — not in `Run`'s outer loop after `runActive` returns. Readers of ADR 013 should treat this ADR as the up-to-date word on the evicted direction; ADR 013 remains correct for reactivation.
- **The fix lives in the shared `internal/sessions` primitive, not `internal/streamsup`.** Every `Activate` caller — the stream runner's inbound drain, the PTY control-plane attach path, the per-conversation bound-session path — benefits, even though only the stream real-claude gate's timing (send immediately on the idle-eviction WARN) happened to expose the race. The settled-state fake tests (#396, #680) structurally cannot hit this window because they wait for `"evicted"` on disk before delivering.
- **Cap-policy `Evict`'s "blocks until stopped" contract is unchanged** — `endEvict` still persists-then-closes only after teardown, same as `transitionTo(stateEvicted)` did.
- **A registry-persist failure in `endEvict` is still non-fatal** — logged and self-healing on the next transition, same posture `transitionTo` had (tearing down every session over one disk hiccup during a routine eviction would be a wildly disproportionate blast radius).
- `internal/e2e`'s #396 (`TestE2E_IdleEviction_RespawnsOnSendMessage`) and #680 per-conversation e2e tests pass unchanged — they assert the settled outcome, which this ADR doesn't alter.

## Related

- [ADR 013](013-evict-activate-persist-ordering.md) — the persist-seam contract this ADR partially supersedes (evicted direction only; reactivation direction unchanged).
- [codebase/1186.md](../codebase/1186.md) — implementation summary, including the rework.
- [features/idle-eviction.md § Two-phase eviction commit](../features/idle-eviction.md) — evergreen mechanism description.
- Ticket: [#1186](https://github.com/pyrycode/pyrycode/issues/1186); unblocks [#1177](https://github.com/pyrycode/pyrycode/issues/1177) (the live real-claude oracle this fix was diagnosed from).
- Code: `internal/sessions/session.go` (`beginEvict`, `endEvict`, `runActive`, `Run`), `internal/sessions/session_evict_race_test.go`.
