# Transition observer (#659)

The injectable, in-process signal a `cmd/pyry`-side consumer (#657) wires to map
session boundaries onto the v2 `session_transition` wire event — **without**
`internal/sessions` importing `internal/protocol` / `internal/relay` (the import
cycle that forces the producer to the `cmd/pyry` boundary). All the machinery
lives in `transition.go`.

```go
type TransitionReason string

const (
    ReasonClear    TransitionReason = "clear"    // /clear rotation: id changed in place
    ReasonEviction TransitionReason = "eviction" // idle OR cap; no successor id
)

type SessionTransition struct {
    PreviousID SessionID
    NewID      SessionID // empty for eviction (no successor)
    Reason     TransitionReason
    OccurredAt time.Time // stamped by internal/sessions at fire
}

type TransitionObserver func(SessionTransition)

func (p *Pool) SetTransitionObserver(obs TransitionObserver)
```

**Package-local reason vocabulary, not the wire's.** `TransitionReason` is a
`string` type owned by this package; #657 maps it onto the wire
`{clear, idle_evict, workspace_change}`. Mirrors the standing "refusal-to-wire-code
mapping is the consumer's job, not the primitive's" convention — the cycle-free
boundary stays at `internal/sessions`.

**Func type, not interface.** Matches the package's closure-injection precedent
(`rotation.Config.OnRotate`, `supervisor.Config.ValidateConversation`).

**Post-construction setter, set-once-before-`Run`.** The pool is built via
`sessions.New` at `cmd/pyry/main.go:460`; the consumer/emitter (#657) comes up
later (`startRelay`), so the observer cannot be a `Config` field. `SetTransitionObserver`
writes `Pool.transitionObserver`; the field is then **read-only**, read lock-free
by the lifecycle + watcher goroutines (both spawned by `Run`) via `Run`'s
goroutine-start happens-before edge — the same "read-only after New" convention as
`convReg` / `activeCap`. A set-after-`Run` call is a programming error the race
detector flags. `nil` (the zero value) disables signalling.

**Two fire sites, both off-lock and post-persist** (the `#41`/`#155`/`#169`
lock-order + pre-persist-exposure lessons):

- **Clear** — `Pool.onRotate(old, new)` (the `Pool.Run` `OnRotate` closure routes
  through it instead of calling `RotateID` directly): on `RotateID` success, fire
  `ReasonClear` with old/new ids; the `RotateID` error is returned verbatim and a
  failed/no-op rotation fires nothing. Fires after `Pool.mu` is released.
  Startup reconciliation (`reconcile.go`) calls `RotateID` **directly**, so no
  spurious clear fires at boot before an observer is wired. Production clear paths
  are the live fsnotify watcher, `RotateForNewSession` since #1125 (the `new_session`
  control verb's daemon-driven direct rotation), and `AdoptAnnouncedID` since #2135
  (the parser-side follower adopting claude's own reset announcement) — all three
  fire the same `ReasonClear` off-`Pool.mu`, no new `TransitionReason`. See
  [`sessions-package-key-types-adoptannouncedid.md`](sessions-package-key-types-adoptannouncedid.md).
- **Eviction** — `Session.runActive` now returns `(TransitionReason, error)`;
  `Session.Run` fires `ReasonEviction` (empty `NewID`) **after** `transitionTo(stateEvicted)`
  returns (post-persist, no `lcMu` held), behind a `reason != "" && s.pool != nil`
  guard. The idle (`<-timerCh`, `attached==0`) and cap (`<-s.evictCh`) paths both
  return `ReasonEviction`; the defensive spontaneous-exit (`<-runErr`) and
  shutdown (`<-ctx.Done()`) paths return `""` / `ctx.Err()` and fire nothing — the
  wire has no "crashed"/shutdown reason.

`Pool.notifyTransition` (unexported) is the nil-guarded leaf callback both sites
call; it takes no lock and the observer runs with no `Pool.mu`/`Session.lcMu`/`capMu`
held. **Idle and cap collapse to one `ReasonEviction`** (evidence-based — #656's
wire has no separate cap reason); the `string` type leaves room for a future
`ReasonCapEviction` with zero signature churn.

**Synchronous, no new goroutine.** Fires run on the goroutine that already owns
the transition (lifecycle for eviction, rotation-watcher for clear) — a per-fire
goroutine would add goroutines to paths that deliberately have none and could
reorder signals. The non-blocking burden is therefore the observer's: the
`TransitionObserver` contract documents "MUST NOT block — hand off to a buffered
channel"; #657 owns the non-blocking impl. See [codebase/659.md](../codebase/659.md).

**This pattern does not transfer to every pool setter — check whose goroutines
read the field, not which precedent the setter resembles.** #2148's
`Pool.SetClientIdentityResolver` first copied this contract verbatim (plain
field, set-once-before-`Run`) on the reasoning that its readers are also
`Activate`'s callers. They are not: the resolver is read by goroutines the
*relay's* v2 manager spawns (a per-conn `appFrameWorker` on every handshake),
and `startRelayV2` starts that manager's `Run` before it installs anything —
unlike `SetTransitionObserver`'s readers, which are exclusively `Pool.Run`'s
own descendants and therefore provably created after `startRelay` returns. A
conn completing its handshake in that window would read the field concurrently
with the install's write. `Pool.clientIdentity` is an `atomic.Pointer` instead.
The general form: this setter shape is race-free only when every reader
goroutine is a descendant of the *same* `Run` the install precedes — verify
that per field, since two setters that look identical can differ in exactly
this way.
