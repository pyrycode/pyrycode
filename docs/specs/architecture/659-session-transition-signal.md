# Spec #659 — surface session-transition signals (clear + eviction) from the pool

**Ticket:** #659 — feat(sessions): surface session-transition signals (clear + eviction) from the pool
**Size:** S (3 production files, 3 new exported types, ~280 LOC total incl. tests; `runActive` has 1 caller — no edit fan-out)
**Split from:** #657 (the `cmd/pyry` fan-out half, blocked on this slice).
**Not labelled `security-sensitive`** — behaviour-preserving in-process plumbing; adds no inbound handler, no dispatch policy, no nonce/crypto/net surface. Skip the security-review pass.

## Context

The v2 wire has a `session_transition` event (#656, merged: `protocol.SessionTransitionPayload`, reason ∈ `{clear, idle_evict, workspace_change}`). The producer that emits it lives at the `cmd/pyry` boundary (#657) because `internal/sessions` must not import `internal/relay`/`internal/protocol` (import cycle). Today the two server-side transition sources are internal to `internal/sessions` with **no caller-visible signal**:

- **Clear** — the rotation watcher calls `OnRotate(oldID, newID)` (wired at `pool.go:810`), which only calls `p.RotateID`. No external observer.
- **Eviction (idle / cap)** — `runActive` (`session.go:311`) returns `nil` on both the idle (`<-timerCh`) and cap (`<-s.evictCh`) paths; `Run` then transitions to `stateEvicted`. The reason is not threaded out.

This slice adds an **injectable, in-process transition observer** to the pool so #657 can map it to the wire event. It does **not** build the wire event or fan it out.

## Files to read first

- `internal/sessions/session.go:270-373` — `Run` loop + `runActive`. **The eviction surfacing site.** `runActive` returns `error`; this slice changes it to `(TransitionReason, error)`. Note the **four** return paths: `<-ctx.Done()` (returns `ctx.Err()`, no signal), `<-runErr` (spontaneous exit, `nil`, **no signal** — wire has no "crashed" reason), `<-timerCh` w/ `attached==0` (idle → eviction signal), `<-s.evictCh` (cap → eviction signal).
- `internal/sessions/session.go:387-438` — `transitionTo`. **Do not modify.** Fire the observer *after* it returns — the post-persist, waiters-woken, no-`lcMu`-held point. Its 3-phase choreography (flip+alloc under `lcMu` → release → persist → re-acquire → close wake chan) is load-bearing (#155/#169).
- `internal/sessions/pool.go:778-831` — `Pool.Run`; the `OnRotate` closure at `:810` is the clear surfacing site. Route it through a new `p.onRotate` so the fire logic is testable without the watcher.
- `internal/sessions/pool.go:152-215` — `Pool` struct + the "read-only after New, no lock needed" field convention (`convReg`, `activeCap`, `sessionTpl`). The observer field follows this pattern.
- `internal/sessions/pool.go:427-464` — `RotateID` contract (returns `ErrSessionNotFound`/save error; releases `Pool.mu` before returning).
- `cmd/pyry/main.go:460-493` — pool built at `:460` via `sessions.New`; `startRelay` (the consumer's home) runs at `:489`, **after** New. This construction order is why the observer is a **post-construction setter**, not a `Config` field (the emitter doesn't exist at New time).
- `internal/protocol/messaging.go:52-58` — `SessionTransitionPayload` (`previous_session_id`, `new_session_id`, `reason`, `occurred_at`, `workspace_cwd`). The mapping target for #657 — **not imported here**.
- `docs/lessons.md:79-82` ("Lock order with callback into the host") and `:102-107` ("In-memory state flips before persist completes") — why the observer fires off-lock and post-persist.
- `internal/sessions/pool_cap_test.go` (`helperPoolCap`, `addCapTestSession`) and `internal/sessions/session_test.go` — reuse for the eviction tests (Bridge-mode supervisors, `/bin/sleep` fake claude, "settle into stateActive before racing" discipline).

## Design

### New types (`internal/sessions/transition.go`)

A new file holds all the observer machinery (one concern per file, matching `id.go`/`reconcile.go`/`get_or_create.go`).

```go
// TransitionReason is an internal/sessions-local vocabulary — NOT protocol's
// wire reason (import cycle). The cmd/pyry consumer (#657) maps it to the
// wire {clear, idle_evict, workspace_change}.
type TransitionReason string

const (
	ReasonClear    TransitionReason = "clear"
	ReasonEviction TransitionReason = "eviction" // idle OR cap; #657 maps both → wire "idle_evict"
)

// SessionTransition is one observed lifecycle transition. NewID is empty for
// eviction (no successor). OccurredAt is captured by internal/sessions at fire.
type SessionTransition struct {
	PreviousID SessionID
	NewID      SessionID
	Reason     TransitionReason
	OccurredAt time.Time
}

// TransitionObserver is notified of clear/eviction transitions. Invoked
// SYNCHRONOUSLY from the firing goroutine with NO session/pool lock held; the
// implementation MUST NOT block (hand off to a buffered channel). nil disables.
type TransitionObserver func(SessionTransition)
```

**Decisions:**

- **Func type, not interface.** Matches the existing closure-injection pattern in this package (`rotation.Config.OnRotate`, `supervisor.Config.ValidateConversation`). The producer owns the type because it stores+invokes it.
- **One eviction reason, not idle-vs-cap.** AC3 permits collapsing; #656's wire has no separate cap reason, so distinguishing them here is a defense for an unobserved need (evidence-based fix selection). The `TransitionReason` string type leaves room to split later with zero signature churn.
- **The defensive spontaneous-exit path (`<-runErr`) fires nothing.** The wire has no "crashed" reason; `runActive` returns reason `""` there and `Run` skips the fire (see below). The `<-ctx.Done()` path (daemon shutdown) returns `ctx.Err()` and never reaches a transition.

### Wiring: post-construction setter (`Pool`)

- **`Pool.SetTransitionObserver(obs TransitionObserver)`** — sets the field. **Contract: call before `Pool.Run`.** The field is then read-only; concurrent reads from the lifecycle + watcher goroutines (both spawned by `Run`) are race-free via `Run`'s goroutine-creation happens-before edge. **No lock** — mirrors the `convReg`/`activeCap` "read-only after New" convention (`pool.go:162-183`). Document the before-`Run` contract loudly; a set-after-`Run` call is a programming error the race detector will flag.
- **New `Pool` field:** `transitionObserver TransitionObserver` (zero value nil = disabled).
- **`Pool.notifyTransition(t SessionTransition)`** (unexported) — `if p.transitionObserver != nil { p.transitionObserver(t) }`. Called by both fire sites. No lock taken, no lock held during the callback (leaf, off-lock — satisfies the "callback into the host" lesson).

### Clear fire site (`Pool`)

- **`Pool.onRotate(oldID, newID SessionID) error`** (unexported) — `RotateID(oldID, newID)`; on success, `notifyTransition(SessionTransition{PreviousID: oldID, NewID: newID, Reason: ReasonClear, OccurredAt: time.Now().UTC()})`; return the `RotateID` error verbatim. Firing only on success means a failed/no-op rotation emits nothing (the watcher already logs+continues on `OnRotate` error).
- **`Pool.Run` `OnRotate` closure** (`pool.go:810`) becomes `OnRotate: func(o, n string) error { return p.onRotate(SessionID(o), SessionID(n)) }`. `RotateID(x,x)` is a no-op returning nil — note it still fires a (degenerate prev==new) signal; acceptable, but the watcher never calls `OnRotate` with equal ids (`handleCreate` returns early when `ref.ID == stem`), so this is unreachable in practice. Leave `onRotate` simple; do not special-case it.

### Eviction fire site (`Session.runActive` + `Session.Run`)

- **`runActive` signature:** `func (s *Session) runActive(ctx context.Context) (TransitionReason, error)`. Return value per path:
  - `<-ctx.Done()` → `("", ctx.Err())`
  - `<-runErr` (spontaneous) → `("", nil)`
  - `<-timerCh` w/ `attached==0` (idle) → `(ReasonEviction, nil)`
  - `<-s.evictCh` (cap) → `(ReasonEviction, nil)`
  - The `attached>0` re-arm `continue` stays inside the loop (no return).
- **`Session.Run` `stateActive` case:**

  ```
  reason, err := s.runActive(ctx)
  if err != nil { return err }
  if err := s.transitionTo(stateEvicted); err != nil { return fmt.Errorf("persist evicted: %w", err) }
  if reason != "" && s.pool != nil {
      s.pool.notifyTransition(SessionTransition{PreviousID: s.id, Reason: reason, OccurredAt: time.Now().UTC()})
  }
  ```

  Fires **after** `transitionTo` (post-persist, no `lcMu`). `reason != ""` skips the spontaneous-exit path. `s.pool != nil` mirrors `transitionTo`'s guard (test-constructed sessions may lack a pool). `NewID` left zero (empty) — eviction has no successor. The `stateEvicted→stateActive` direction fires nothing (re-activation is not a signalled transition; the wire has no "activated" reason).
- `s.id` read off-lock here is consistent with the existing `runActive` `s.log.Warn(... string(s.id))` and is not concurrently rotated during its own eviction.

### Why synchronous (no goroutine in `internal/sessions`)

Spawning a goroutine per fire would add goroutines to paths that deliberately have none and would reorder signals (a later eviction could overtake an earlier clear). Synchronous invocation preserves ordering and keeps the package goroutine-free on this path. The non-blocking burden is the observer's (#657 hands off to its emitter's buffered outbound). Documented as the `TransitionObserver` contract.

## Concurrency model

- **No new goroutines.** Fires are synchronous on the goroutine that already owns the transition: the **lifecycle goroutine** (`Session.Run`) for eviction, the **rotation watcher goroutine** (`Pool.Run`'s errgroup) for clear.
- **Lock discipline:** `notifyTransition` takes no lock and the observer runs with no `Pool.mu`/`Session.lcMu`/`capMu` held — clear fires after `RotateID` returns (`Pool.mu` released); eviction fires after `transitionTo` returns (`lcMu` released, persist done). No new lock-order edges; the `Pool.mu → Session.lcMu` and `capMu → Pool.mu → Session.lcMu` orders are untouched.
- **Observer field visibility:** set-once-before-`Run`, read-only thereafter; visibility via `Run`'s goroutine-start happens-before. Concurrent reads (lifecycle + watcher firing at once) are safe.

## Error handling

- `onRotate` returns `RotateID`'s error verbatim (watcher logs+continues); no observer fire on the error path.
- A persist failure in `transitionTo` returns before the eviction fire (existing fatal-to-`Run` behaviour preserved); no fire on that path.
- The observer is `func(SessionTransition)` (no error return) — it is a notification, not a gate; a misbehaving observer cannot fail a transition. A panicking observer would crash the lifecycle/watcher goroutine — that is the observer's contract to uphold (do not recover in `internal/sessions`; a swallowed panic would hide a #657 bug).

## Testing strategy (`internal/sessions/transition_test.go`, scenarios — not full bodies)

Use a test observer that appends each `SessionTransition` to a slice under its own mutex (fires come from different goroutines). Reuse `helperPoolCap`/`addCapTestSession`/`/bin/sleep` patterns; settle sessions into `stateActive` before driving evictions (per `lessons.md:94`).

- **Clear:** wire an observer, call `p.onRotate(old, new)` directly; assert one signal with `PreviousID==old`, `NewID==new`, `Reason==ReasonClear`, non-zero `OccurredAt`. Assert `RotateID` actually rotated (registry/in-memory id moved) — i.e. the existing `RotateID` behaviour is unchanged.
- **`onRotate` on unknown id:** returns `ErrSessionNotFound`, fires **no** signal.
- **Idle eviction:** pool with a short `IdleTimeout`, no attaches; run the lifecycle; assert one signal `Reason==ReasonEviction`, `PreviousID==<session id>`, `NewID==""`. (Mirror an existing idle-eviction test's setup.)
- **Cap eviction:** `ActiveCap=1`, activate a second session to force LRU eviction of the first; assert the evicted peer produced one `ReasonEviction` signal.
- **Spontaneous-exit path fires nothing:** drive the `<-runErr` return (supervisor exits on its own under a still-live outer ctx) and assert **no** signal — guards the `reason != ""` gate.
- **Nil observer is a no-op:** with no `SetTransitionObserver` call, drive a rotation + idle + cap eviction; assert no panic and that registry persistence + rotation + eviction behave byte-identically to today (assert final registry state / lifecycle state, not just "no panic").
- **`-race` clean:** run idle+cap+clear concurrently enough to exercise simultaneous fires from the lifecycle and watcher goroutines; the observer's own mutex must keep the recorder race-free.

## Open questions

- **Distinct cap-vs-idle reason later?** If a future consumer needs to render cap eviction differently, add `ReasonCapEviction` and return it from the `<-s.evictCh` path — a one-line change, no signature churn. Deferred (no observed need; wire collapses both).
- **Blocking-observer defense?** If a blocking #657 observer is ever observed to stall the lifecycle/watcher goroutine, revisit (a bounded send into a pool-owned channel, or a documented hard requirement). Not built now — the contract is documented and #657 owns the non-blocking impl (evidence-based; do not pre-build the goroutine defense for an unobserved failure).
