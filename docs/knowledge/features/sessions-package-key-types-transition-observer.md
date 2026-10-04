# Transition observer (#659)

The injectable, in-process signal a `cmd/pyry`-side consumer (#657) wires to map
session boundaries onto the v2 `session_transition` wire event — **without**
`internal/sessions` importing `internal/protocol` / `internal/relay` (the import
cycle that forces the producer to the `cmd/pyry` boundary). All the machinery
lives in `transition.go`.

**Update-only, not an ID-discovery feed.** Creation (`sessions.New`,
`Pool.Mint` / `CreateIn`, `GetOrCreateIn`) emits no transition, and the push does
not cover every session ID change. `Pool.RotateBootstrapForSelfHeal` silently
rekeys the bootstrap session, but is an uncalled primitive with no production
caller today. Its silence is pinned by `TestRotateBootstrapForSelfHeal`'s
“does not fire a client transition” subtest. Listening alone therefore leaves a
fresh conversation's client without a session ID and cannot keep IDs current
in every case.

Clients discover or re-read IDs through
[`request_session_settings`](../../protocol-mobile.md#request_session_settings):
supply the named conversation's `conversation_id`, then read
`session_settings.session_id`. An empty ID means no addressable session; session
settings remain read-only. `handleRequestSessionSettings` uses `RunConfigFor`,
whose production source, `resolveBoundRunSettings`, resolves that conversation's
live or persisted dormant binding rather than a shared bootstrap fallback.
See the [wire contract](../../protocol-mobile.md#session_transition).

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
(`RunnerConfig.AdoptAnnouncedReset`).

**Post-construction setter, set-once-before-`Run`.** The pool is built via
`sessions.New` in `cmd/pyry.main`; the consumer/emitter (#657) comes up
later (`startRelay`), so the observer cannot be a `Config` field. `SetTransitionObserver`
writes `Pool.transitionObserver`; the field is then **read-only**, read lock-free
by the per-session lifecycle goroutines and the runners they start via `Run`'s
goroutine-start happens-before edge — the same "read-only after New" convention as
`convReg` / `activeCap`. A set-after-`Run` call is a programming error the race
detector flags. `nil` (the zero value) disables signalling.

**Current notification paths, all off-lock** (the `#41`/`#155`/`#169`
lock-order lessons):

- **Clear** — `Pool.RotateForNewSession` drives a daemon-minted rotation;
  `Pool.AdoptAnnouncedID` follows the child's reset announcement. Both rekey and
  attempt persistence before firing `ReasonClear` off-`Pool.mu`.
  `AdoptAnnouncedID` suppresses equal-ID announcements; refused rotations fire
  nothing. `notifyTransition` rebinds the owning conversation before fan-out so
  the consumer can resolve the new ID. The fsnotify watcher and `onRotate` were
  retired by #2137; neither is a current notification source. See
  [`sessions-package-key-types-adoptannouncedid.md`](sessions-package-key-types-adoptannouncedid.md).
- **Agent switch** — `Pool.PublishSwitchTransition` directly fires `ReasonClear`
  after the caller has persisted the conversation's new binding. It bypasses
  `notifyTransition` to avoid a second rebind and best-effort save.
- **Eviction** — `Session.beginEvict` fires `ReasonEviction` (empty `NewID`)
  **before** the lifecycle state flip and child teardown, with no `lcMu` held,
  behind a `reason != "" && s.pool != nil` guard. `Session.endEvict` persists
  after the child stops. The idle path (`<-timerCh`, firing only once
  `Config.TurnBusy` — nil or reporting no open turn — no longer defers it, #1486) and the cap path
  (`<-s.evictCh`) both return `ReasonEviction`; the defensive spontaneous-exit
  (`<-runErr`) and shutdown (`<-ctx.Done()`) paths return `""` / `ctx.Err()` and
  fire nothing — the wire has no "crashed"/shutdown reason.
  `notifyTransition` suppresses eviction for a session already removed from the
  pool, preventing teardown from adding a second boundary beside an agent switch.

`Pool.notifyTransition` (unexported) is the shared fan-out for rotation and
eviction. Its eviction membership check takes and releases `Pool.mu` before
the nil-guarded observer callback; the observer runs with no `Pool.mu`/`Session.lcMu`/`capMu`
held. **Idle and cap collapse to one `ReasonEviction`** (evidence-based — #656's
wire has no separate cap reason); the `string` type leaves room for a future
`ReasonCapEviction` with zero signature churn.

**Synchronous, no new goroutine.** Fires run on the goroutine that already owns
the transition (lifecycle for eviction; the runner's parse goroutine or the
control-plane caller for clear) — a per-fire
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
