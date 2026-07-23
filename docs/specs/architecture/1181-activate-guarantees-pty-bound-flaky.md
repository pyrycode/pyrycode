# Spec #1181 — Deflake `TestSession_Activate_GuaranteesPTYBound`

**Ticket:** [#1181](https://github.com/pyrycode/pyrycode/issues/1181) · **Size:** S (in practice XS — one test file, ~20 lines, zero production) · **Security-sensitive:** no

## Files to read first

- `internal/sessions/session_test.go:333-374` — `TestSession_Activate_GuaranteesPTYBound`, the flaky test being rewritten. Extract: the current fixture arms `helperPoolIdle(t, 80*time.Millisecond)`, waits for `stateEvicted` (relying on idle eviction), Activates, then re-checks with `WaitForPTY(10ms)`.
- `internal/sessions/session_persist_test.go:101-142` — `TestSession_ActivateBlocksUntilPersisted`, the **proven deterministic idiom to mirror**: `helperPoolIdle(t, 0)` → `pollUntil(stateActive)` → `sess.Evict(ctx)` → assert `stateEvicted` → `sess.Activate(ctx)`. Copy this shape.
- `internal/sessions/session.go:345-365` — `Activate`. Extract: its final statement is `return s.sup.WaitForPTY(ctx)`, so `Activate` returning nil already means the PTY was bound *at the instant it returned*. The contract is not in doubt.
- `internal/sessions/session.go:377-394` — `Evict`. Extract: force-evict via the buffered `evictCh`; **independent of the idle timer** — works with `idleTimeout==0`.
- `internal/sessions/session.go:516-580` — `runActive`. Extract: the idle `timer` is armed at the *start* of `runActive` (before the PTY binds) and re-armed on every Activate; the `case <-s.evictCh` arm returns `ReasonEviction` regardless of `idleTimeout`; a zero `idleTimeout` sets `timerCh = nil` (never selects).
- `internal/supervisor/supervisor.go:617-657` — `setSession` + `WaitForPTY`. Extract: `setSession(non-nil)` closes `sessReadyCh`; `setSession(nil)` **freshens** it (allocates a new unclosed channel). `WaitForPTY` reads the current `sessReadyCh` under `sessMu` and waits on it. This freshen-on-clear is the mechanism the flake exploits.
- `internal/sessions/session_test.go:116-165` — `helperPoolIdle` (accepts `idle=0`, documented "eviction disabled") + `pollUntil`. The fixture the rewrite reuses unchanged.
- `internal/supervisor/supervisor_test.go:1097` — `TestSupervisor_WaitForPTY_FreshensAfterClear`. Extract: canonical proof that a session clear replaces `sessReadyCh` with an unclosed channel — i.e. `deadline exceeded` on a fresh channel is exactly what a post-Activate eviction produces.

## Context

`TestSession_Activate_GuaranteesPTYBound` fails intermittently under full-suite `make check` with `-race` (once in 20+ runs; never isolated or package-scoped). Signature: `WaitForPTY immediately after Activate = context deadline exceeded, want nil`. Surfaced while QA-gating PR #1180 but **confirmed pre-existing and unrelated** — that PR adds only an `e2e_realclaude`-gated test file plus a spec doc, nothing in `internal/sessions`.

### Root cause — test-timing fragility, not a production race

The fixture arms an **80ms idle timeout with no client attached** (`helperPoolIdle(t, 80*time.Millisecond)`; child is `/bin/sh -c 'exec sleep 3600'`). The failure is a legitimate *later* eviction racing the test's re-check, not a bind failure:

1. The session idle-evicts once (the test waits for `stateEvicted`), then `Activate(activateCtx=5s)` re-activates it. `Activate`'s own final statement is `WaitForPTY(activateCtx)`, so **the PTY-bound contract holds at the instant `Activate` returns.**
2. But re-activation re-enters `runActive`, which arms a **fresh 80ms idle timer** at the top of the function (before the PTY even binds) and, with no client attached, will fire and evict again. Only part of that 80ms is consumed by spawn + bind before `Activate` returns; the remainder is a live eviction window.
3. The test's *second* check, `WaitForPTY(checkCtx=10ms)`, re-reads `s.sessReadyCh` fresh. Under full-suite `-race` scheduling pressure the test goroutine can be descheduled long enough for the idle timer to fire in that gap. Eviction tears down the supervisor, `setSession(nil)` **freshens `sessReadyCh` to a new unclosed channel**, and the 10ms check then waits on that unclosed channel → `context.DeadlineExceeded`.

This matches the observed signature precisely: `deadline exceeded` (not `context.Canceled`), on a freshened/unclosed channel. It is **not** an `Activate`/PTY-bind ordering gap — the bind held at return; the assertion simply observes a *later* moment at which the session has been correctly idle-evicted by its own timer.

**Verdict: test-only. No production code changes** (AC5 — no genuine production race was found).

## Design

Rewrite `TestSession_Activate_GuaranteesPTYBound` to the already-proven `idleTimeout=0` + `Evict`/`Activate` idiom used by `TestSession_ActivateBlocksUntilPersisted` in the same package. This removes the racing idle timer entirely and drives the required evicted→active cycle deterministically.

Why this is deterministic rather than "widen a deadline and hope" (AC2): with `idleTimeout=0` there is **no timer that can ever fire**, so nothing can call `setSession(nil)` to freshen `sessReadyCh` after `Activate`. The session stays active indefinitely; `sessReadyCh` is closed by `setSession(non-nil)` during re-activation and **stays closed permanently**. The re-check reads an already-closed channel — an immediate, contention-independent read. The reason it can no longer fail is structural (no eviction source exists), not a bigger timeout.

Rewritten flow (mirror of `session_persist_test.go:101-142`, tail-extended with the readiness re-check):

1. `pool := helperPoolIdle(t, 0)` — idle eviction disabled. `sess := pool.Default()`; run `sess.Run(ctx)` on a goroutine (`t.Cleanup(cancel)` as today).
2. `pollUntil(2s, stateActive)` — the bootstrap warm-starts active; confirm the lifecycle goroutine has entered `runActive`.
3. `sess.Evict(evictCtx)` — deterministically drive to `stateEvicted` (the `evictCh` arm is idle-timer-independent). `Evict` blocks until the evict persist completes, so no poll is needed; a follow-up `LifecycleState()==stateEvicted` assert is a cheap belt.
4. `sess.Activate(activateCtx=5s)` — re-activate from evicted. Unchanged assertion: non-nil → `t.Fatalf`.
5. `sess.sup.WaitForPTY(checkCtx=10ms)` — the retained non-vacuous assertion. Non-nil → `t.Errorf`.

No signature changes, no new helpers, no new fixture. `Evict`, `Activate`, `WaitForPTY`, and `helperPoolIdle(t, 0)` are all existing, already-exercised capabilities.

### Non-vacuity is preserved (AC3)

The assertion itself is untouched: `WaitForPTY(checkCtx) != nil → fail`, still observing `sessReadyCh` closed. If `Activate` ever returned nil while the PTY were genuinely unbound (e.g. a regression that removed `Activate`'s internal `WaitForPTY`), `setSession(non-nil)` would not yet have run, `sessReadyCh` would be a fresh open channel, and the check would still time out and fail. Only the *fixture's eviction race* is removed — not *what* is checked.

**Keep the check window short (10ms).** It must stay short to preserve the "bound *at return*, not eventually" semantics: a wide window would wait for a late bind and thereby *mask* a "returns-before-bound" regression. Determinism comes from `idleTimeout=0`, never from the window size, so there is no need — and it would be wrong — to widen it. The only residual failure mode at 10ms is pathological goroutine starvation reading an *already-closed* channel, which is shared by every timed test and is not the observed failure.

Update the check's code comment to state the new guarantee: the channel is permanently closed because idle eviction is disabled, so the read is deterministic (replace the stale "80ms idle timer still running" mental model).

## Concurrency model

Unchanged from the current test and its sibling. One lifecycle goroutine (`sess.Run`) drives the `active↔evicted` machine; the test goroutine drives transitions via the existing blocking primitives (`Evict` waits on `evictedCh`, `Activate` waits on `activeCh` then `WaitForPTY`), each of which already serialises against `transitionTo`'s post-persist channel closes under `lcMu`. Removing the `idleTimeout>0` timer removes the only asynchronous transition source, leaving a fully test-driven, deterministic sequence. `t.Parallel()` is retained.

## Error handling

Test-level only. `Evict`/`Activate` failures → `t.Fatalf` (as the sibling test does); the `WaitForPTY` re-check → `t.Errorf` (unchanged). No production error paths touched. `helperPoolIdle` already `t.Skipf`s when `/bin/sh` is absent.

## Testing strategy

- **Verification of the fix:** run `TestSession_Activate_GuaranteesPTYBound` under full-suite `make check` ≥ 20 times; expect zero failures (AC4). Package-scoped (`go test -race ./internal/sessions/…`) and isolated (`-run TestSession_Activate_GuaranteesPTYBound -count=100 -race`) runs must stay green. Because the flake required full-suite `-race` contention to reproduce at all, the full-suite loop is the load-bearing gate; the isolated `-count` loop is a cheap complementary check.
- **Non-vacuity spot-check (developer's own confidence, not a committed test):** temporarily stub out `Activate`'s trailing `WaitForPTY` locally and confirm the rewritten test *fails* with `deadline exceeded` — proving the assertion still catches a genuinely-unbound return. Revert before committing.
- **No new tests.** This is a deflake of an existing assertion; adding tests would be scope creep.
- `go vet ./...` and `staticcheck ./...` clean.

## Open questions

None blocking. One note for the developer: `Evict` is called on a session that may not have finished binding its PTY when the pre-evict state is reached — that is fine (Evict only requires `stateActive`, and the subsequent `Activate` spawns and binds a fresh supervisor, which is the transition the assertion actually exercises). If you prefer to mirror "evict a *bound* PTY," an initial `WaitForPTY` before `Evict` is harmless but unnecessary; do not add it unless it reads more clearly to you.
