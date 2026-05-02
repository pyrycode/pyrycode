# `internal/sessions` Package

The session-addressable runtime layer that wraps `internal/supervisor` with identity (`SessionID`) and registry (`Pool`) semantics. One `Pool` holds the set of supervised claude instances managed by a single `pyry` process.

Today the pool holds exactly one entry — the **bootstrap session** — so external behaviour is unchanged from the pre-Phase-1 supervisor-only world. The package shape is the seam Phase 1.1+ extends additively (multi-session CLI, `pyry attach <id>`, idle eviction) without touching `internal/supervisor`.

## Status

- **Phase 1.0a (#28):** package introduced, fully tested, no production consumers.
- **Phase 1.0b (#29):** consumers wired. `cmd/pyry/main.go` constructs the `Pool`; `internal/control` resolves session state via a `SessionResolver` interface defined in the control package. Wire protocol unchanged; `pyry status`/`stop`/`logs`/`attach` are byte-identical to Phase 0.
- **Phase 1.2a (#34):** `Config.RegistryPath`, on-disk `sessions.json`, cold/warm-start in `Pool.New`. See [sessions-registry.md](sessions-registry.md).
- **Phase 1.2b-A (#38):** `Config.ClaudeSessionsDir`, startup reconciliation pass in `Pool.New`, `Pool.RotateID` seam. See [jsonl-reconciliation.md](jsonl-reconciliation.md).
- **Phase 1.2b-B (#39):** errgroup wrap in `Pool.Run` (bootstrap + rotation watcher); `RegisterAllocatedUUID` skip set on `Pool`. See [rotation-watcher.md](rotation-watcher.md).
- **Phase 1.2c-A (#40):** per-session `active ↔ evicted` lifecycle goroutine, `Config.IdleTimeout` / `SessionConfig.IdleTimeout`, `Session.Activate` / `Pool.Activate`, registry `lifecycle_state`. `Session.Run` rewritten as the lifecycle loop; `Session.Attach` gains attach bookkeeping. See [idle-eviction.md](idle-eviction.md).
- **Phase 1.2c-B (#41):** `Config.ActiveCap` + LRU eviction at `Pool.Activate`; `Session.Evict` public primitive; `Pool.capMu` outermost lock. See [idle-eviction.md](idle-eviction.md) and [ADR 006](../decisions/006-concurrent-active-cap-lru.md).
- **Phase 1.1a-A1 (#72):** `Pool.runGroup` / `Pool.runCtx` supervisor handle, unexported `Pool.supervise(sess)` helper, `ErrPoolNotRunning` sentinel. Bootstrap fan-out refactored onto the helper; the watcher fan-out is unchanged (not a `*Session`). The seam Phase 1.1a-A2's `Pool.Create` consumes.
- **Phase 1.1a-A2 (#73):** `Pool.Create(ctx, label) (SessionID, error)` — mint, persist, activate primitive. New unexported fields `Pool.sessionTpl` (per-session template snapshotted from `cfg.Bootstrap`) and `Pool.idleTimeoutDefault` (mirror of `Config.IdleTimeout`). `claude --session-id <uuid>` is now baked on the spawn path for non-bootstrap sessions. Bootstrap behaviour unchanged.
- **Phase 1.1b-A (#60):** `Pool.List() []SessionInfo` read primitive + new `SessionInfo` value type. Operator-visible snapshot (id, label, lifecycle state, last-active timestamp, bootstrap flag) for every session in the pool, sorted by `LastActiveAt` desc with `SessionID` asc tiebreak. Synthetic `"bootstrap"` label substitution for empty-on-disk bootstrap labels lives at this layer (consumers get the same UX guarantee). Read-only; no on-disk mutation. The internal seam Phase 1.1b-B's `pyry sessions list` CLI verb (46-B) consumes.
- **Phase 1.1c-A (#62):** `Pool.Rename(id, newLabel) error` write primitive — typed mutator for the `label` field that flows through `saveLocked`. Empty `newLabel` clears the on-disk value to `""` (synthetic `"bootstrap"` substitution from #60 then resumes for the bootstrap entry); non-empty labels persist verbatim and are reflected by `Pool.List`. Unknown id returns `ErrSessionNotFound` with on-disk and in-memory state byte-identical to before. The internal seam Phase 1.1c-B's `pyry sessions rename` CLI verb (47-B) consumes.
- **Phase 1.1d-A1 (#94):** `Pool.Remove(ctx, id) error` write primitive + `ErrCannotRemoveBootstrap` sentinel — terminates the named session's claude process and drops its registry entry. JSONL on disk is **not** touched (disposition lands in 64-A2 / #95). Bootstrap is rejected via the sentinel; unknown ids reuse `ErrSessionNotFound`. Delete-then-evict ordering: `Pool.mu` covers the in-memory delete + persist, then is released before `Session.Evict` so the lifecycle goroutine's `transitionTo → Pool.persist` can reacquire the mutex without deadlock. Save-failure rolls the in-memory delete back. The internal seam the future `pyry sessions rm` CLI verb (#65) consumes.
- **Phase 1.1d-A2 (#95):** `Pool.Remove` signature evolves to `(ctx, id, opts RemoveOptions) error` + `JSONLPolicy` enum (`JSONLLeave` / `JSONLArchive` / `JSONLPurge`). Zero-value `RemoveOptions{}` preserves 1.1d-A1 behaviour byte-for-byte. Archive moves the live JSONL into `<pyry-data-dir>/archived-sessions/<uuid>.jsonl` (data-dir = parent of `registryPath`; subdir auto-created; errors wrapping `fs.ErrExist` if destination exists; source-absent is a no-op). Purge deletes the JSONL (source-absent is a no-op). Disposition runs under `Pool.mu` after `saveLocked` so the registry+JSONL transition is observably atomic; on disposition failure the registry is already committed and `Session.Evict` still runs.
- **Phase 1.1e-A (#66):** `Pool.ResolveID(arg string) (SessionID, error)` prefix resolver + new `ErrAmbiguousSessionID` sentinel. Maps a user-supplied UUID-or-prefix string to a canonical `SessionID` under `Pool.mu` (RLock). Empty arg → bootstrap (same seam as `Pool.Lookup("")`); exact full-UUID match short-circuits via single map lookup; otherwise scan for unique prefix match. Zero matches → `ErrSessionNotFound` (reused); ≥2 → `ErrAmbiguousSessionID` wrapped via `fmt.Errorf("%w:\n%s", …)` so the message lists `<uuid> (<label>)` pairs on their own lines, sorted by `SessionID` ascending. The internal seam Phase 1.1e-B's `pyry attach <id>` wire + CLI consumes; 47-B / 48-B may opportunistically refactor onto it when next touched.
- **Phase 1.1+:** `Request.SessionID` on the wire (#71 — `sessions.new` verb consumes `Pool.Create`), per-session log lines, channel-driven auto-mint.

## Package Layout

```
internal/sessions/
  id.go         SessionID, NewID()
  session.go    Session: wraps one supervisor + optional bridge
  pool.go       Pool: registry, lifecycle, Config, SessionConfig, RotateID
  registry.go   On-disk sessions.json (loadRegistry, saveRegistryLocked)
  reconcile.go  Startup JSONL scan (encodeWorkdir, mostRecentJSONL, reconcileBootstrapOnNew)
```

## Key Types

### `SessionID`

```go
type SessionID string

func NewID() (SessionID, error)
```

A 36-char canonical UUIDv4 (`8-4-4-4-12` hex with dashes), drawn from `crypto/rand`. No external UUID library — stdlib only, ~15 lines. The version (`b[6] = b[6]&0x0f | 0x40`) and variant (`b[8] = b[8]&0x3f | 0x80`) bits are set explicitly.

The empty `SessionID` (`""`) is the **unset sentinel**, never a valid generated ID. `Pool.Lookup("")` resolves to the default entry — the mechanism that lets future handlers call `Lookup(req.SessionID)` against an empty wire field without a special case.

### `Session`

```go
type Session struct { /* id, sup, bridge, log, lifecycle fields */ }

func (s *Session) ID() SessionID
func (s *Session) State() supervisor.State
func (s *Session) LifecycleState() lifecycleState
func (s *Session) Attach(in io.Reader, out io.Writer) (done <-chan struct{}, err error)
func (s *Session) Activate(ctx context.Context) error
func (s *Session) Run(ctx context.Context) error
```

One supervised claude instance plus the bridge that mediates its I/O in service mode. As of 1.2c-A each Session owns a lifecycle goroutine driving an `active ↔ evicted` state machine (see [idle-eviction.md](idle-eviction.md)).

- `State()` returns the supervisor's snapshot — same safe-from-any-goroutine contract. In `evicted`, the supervisor reports `PhaseStopped` (faithful — it really isn't running).
- `LifecycleState()` returns the current lifecycle state under `lcMu`. Used by tests and (eventually) richer status payloads.
- `Attach` returns `ErrAttachUnavailable` when `bridge == nil` (foreground mode); otherwise delegates to `(*supervisor.Bridge).Attach`. `supervisor.ErrBridgeBusy` is propagated **verbatim** so callers' `errors.Is` checks keep working. Bumps `attached` under `lcMu`; the wrapper goroutine spawned here decrements on bridge `done`. **Contract:** callers must `Activate` first — `bridge.Attach` on an evicted session would block on the pipe forever.
- `Activate(ctx)` moves an evicted session to `active`, blocking until the supervisor has started (or `ctx` cancels). No-op when already active. Idempotent under concurrent calls.
- `Run(ctx)` blocks until ctx cancellation, driving the lifecycle loop (`runActive` ↔ `runEvicted`). The supervisor is started on an inner ctx during active periods and drained when the ctx cancels.

The `log` field is written by the constructor but not read in 1.0 (no per-session log lines yet — see [parent ADR](../decisions/003-session-addressable-runtime.md)). Kept on the struct so 1.1 can attach without reshaping.

### `Pool`

```go
type Pool struct { /* mu RWMutex, sessions map, bootstrap, log */ }

func New(cfg Config) (*Pool, error)
func (p *Pool) Lookup(id SessionID) (*Session, error)
func (p *Pool) Default() *Session
func (p *Pool) Run(ctx context.Context) error
func (p *Pool) RotateID(oldID, newID SessionID) error
func (p *Pool) Activate(ctx context.Context, id SessionID) error
func (p *Pool) Create(ctx context.Context, label string) (SessionID, error)
func (p *Pool) List() []SessionInfo
func (p *Pool) Rename(id SessionID, newLabel string) error
func (p *Pool) ResolveID(arg string) (SessionID, error)
```

`New` generates a `SessionID`, constructs the underlying `*supervisor.Supervisor` from `cfg.Bootstrap`, and installs the result as the single bootstrap entry. Both `NewID` failure and `supervisor.New` failure are wrapped (`sessions: generate bootstrap id: %w`, `sessions: bootstrap supervisor: %w`) and treated as fatal-at-startup.

`Lookup`:

- empty id → bootstrap entry, no error
- known id → that entry
- non-empty unknown id → `ErrSessionNotFound` (sentinel, matchable via `errors.Is`)

`Default()` is a separate accessor with the same body minus the empty-string branch — startup paths that need the bootstrap don't carry an `error` return they know is impossible.

`Run` wraps the bootstrap session and (when `ClaudeSessionsDir` is set) the rotation watcher under `errgroup.WithContext`. The 1.2b-B errgroup wrap is the extension point Phase 1.1's N-session fan-out reuses by adding one `g.Go(sess.Run)` per pool entry. As of 1.1a-A1 (#72), `Run` exposes the live group on `*Pool` (see *Supervisor handle* below) so post-`Run` code paths can join the same supervised set; the bootstrap itself is now scheduled through that seam.

`Activate(ctx, id)` (1.2c-A) is a thin wrapper that resolves `id` and calls `Session.Activate`. Symmetry with the rest of the surface; future routers get a single entry point. Returns `ErrSessionNotFound` for unknown ids.

`Create(ctx, label)` (1.1a-A2) is the user-facing seam for minting a new session. See *Pool.Create* below for the full sequence and failure modes.

`RotateID` (1.2b-A) atomically swaps the in-memory entry keyed by `oldID` with one keyed by `newID`, updates the bootstrap pointer if `oldID` was the bootstrap, bumps `last_active_at`, and persists. `p.mu` (write) is held across the entire operation. `RotateID(x, x)` is a no-op; unknown `oldID` returns `ErrSessionNotFound`. This is the load-bearing seam shared between startup reconciliation and the upcoming live-detection (`/clear` while claude is running) work — see [jsonl-reconciliation.md](jsonl-reconciliation.md).

### `Config` / `SessionConfig`

```go
type Config struct {
    Bootstrap         SessionConfig
    Logger            *slog.Logger
    RegistryPath      string        // sessions.json path; "" disables persistence (test-only)
    ClaudeSessionsDir string        // claude's <uuid>.jsonl dir; "" disables startup reconcile
    IdleTimeout       time.Duration // default per-session eviction window; 0 disables
}

type SessionConfig struct {
    ClaudeBin  string
    WorkDir    string
    ResumeLast bool
    ClaudeArgs []string
    Bridge     *supervisor.Bridge // nil = foreground

    BackoffInitial time.Duration
    BackoffMax     time.Duration
    BackoffReset   time.Duration

    IdleTimeout    time.Duration  // 0 inherits Config.IdleTimeout
}
```

`SessionConfig` mirrors the relevant fields of `supervisor.Config`; `New` translates one to the other. Defaults (claude bin lookup, backoff timings) are applied by `supervisor.New` — `sessions.New` does **not** duplicate them.

`ResumeLast` maps to `--continue` on restart, as today. The locked-design `claude --session-id <uuid>` invocation is deliberately **not** plumbed in 1.0 — Phase 1.1+ adds it.

### Supervisor handle (1.1a-A1)

Two unexported fields on `*Pool` hold the live errgroup while `Run` is in progress:

```go
runGroup *errgroup.Group   // set in Pool.Run, cleared on return
runCtx   context.Context   // the gctx returned by errgroup.WithContext
```

Both are guarded by `Pool.mu` and read together so a caller never sees a half-initialised handle. `Pool.Run` writes them under `Pool.mu` (write) right after `errgroup.WithContext`, and clears them in a `defer` so a panicking goroutine still resets the handle.

```go
func (p *Pool) supervise(sess *Session) error
```

`supervise` schedules `sess.Run(gctx)` on the live group. RLock-snapshots `runGroup` + `runCtx`, releases the lock, then calls `g.Go` off-lock. Returns `ErrPoolNotRunning` (`var ErrPoolNotRunning = errors.New("sessions: pool not running")`) when the handle is `nil` — i.e. before `Run` has wired it or after `Run` has cleared it. Matchable with `errors.Is`.

The helper is unexported. Phase 1.1a-A2's `Pool.Create(ctx, label)` is the consumer: build a `*Session`, then `p.supervise(sess)` to fan it onto the same supervised set as the bootstrap. The bootstrap fan-out inside `Pool.Run` is itself rewritten to call `supervise` (the helper is exercised in production from day one rather than living dormant).

The watcher fan-out (`g.Go(func() error { return w.Run(gctx) })`) does **not** go through `supervise` — the watcher is not a `*Session`.

**Lock discipline.** `supervise` takes only `Pool.mu` (RLock) briefly; it does not call into `Session.lcMu`. The documented orders (`Pool.mu → Session.lcMu`, `Pool.capMu → Pool.mu → Session.lcMu`) are unchanged. Concurrent `supervise` callers contend only with `Run`'s one-shot setup and one-shot teardown, never with each other.

**Race windows.** A `supervise` call racing teardown either acquires RLock first (sees the handle, schedules onto a group whose ctx is about to be cancelled — `Session.Run` handles `ctx.Done` cleanly) or after (sees `nil`, returns the sentinel). The "scheduled onto a soon-cancelled group" case is safe: `errgroup.Group.Go` is documented as concurrency-safe and the scheduled func observes the cancelled ctx immediately, exiting via the existing shutdown path.

### Pool.Create (1.1a-A2)

The user-facing primitive that ties together every existing seam — `NewID`, `saveLocked`, `RegisterAllocatedUUID`, `supervise`, `Activate` — to mint a fresh non-bootstrap session. One well-tested entry point so downstream callers (Phase 1.1a-B's `sessions.new` verb, future channel-driven auto-mint) don't re-derive the sequence.

```go
func (p *Pool) Create(ctx context.Context, label string) (SessionID, error)
```

**Two unexported fields support it,** captured at `New()` time and read-only after:

- `sessionTpl SessionConfig` — shallow copy of `cfg.Bootstrap`. `Create` clones `ClaudeArgs`, appends `--session-id <uuid>`, sets `ResumeLast = false`, and (if `tpl.Bridge != nil`) mints a fresh `*supervisor.Bridge`. The bridge presence is the service-mode signal — sharing one bridge across N sessions would multiplex their I/O into a single client view.
- `idleTimeoutDefault time.Duration` — mirrors `Config.IdleTimeout`. `Create` applies the same fallback `New()` applies to the bootstrap (per-session zero → pool default).

**Sequence (in order — each step depends on the previous):**

1. `NewID()` — fresh UUIDv4
2. Build per-session `SessionConfig`: clone `ClaudeArgs`, append `--session-id <uuid>`, `ResumeLast=false`, fresh `Bridge` in service mode
3. `supervisor.New(supCfg)` — wrap on `sessions: create supervisor: %w` failure (no state mutated yet)
4. Build `*Session` in `stateEvicted` (label verbatim — empty preserved as empty; `bootstrap=false`; `createdAt=lastActiveAt=now`)
5. **Persist phase under `p.mu` (write):** insert into `p.sessions[id]`, call `saveLocked()`. On save failure, `delete(p.sessions, id)` rollback under the same lock and return `("", err)`. Lock released.
6. `RegisterAllocatedUUID(id)` — primes the rotation watcher's skip-set. Must fire before claude opens the JSONL or the CREATE looks like a `/clear` rotation. The 30s TTL is well clear of the sub-second spawn path.
7. `supervise(sess)` — schedules `sess.Run(gctx)` on the live errgroup. On `ErrPoolNotRunning`, return `(id, err)` — entry is on disk, no lifecycle goroutine.
8. `Activate(ctx, id)` — cap-aware. The new session is in `stateEvicted` so it doesn't count toward `active` for the cap pre-flight; `pickLRUVictim` excludes the target. On Activate failure, return `(id, err)`.

**Why persist *before* activate.** A save failure with claude already running leaves an unsupervised orphan: claude has opened its JSONL, started a conversation, and pyry has no on-disk record. The next pyry start won't reconcile it; the JSONL becomes a ghost. A registry-only entry that didn't activate is benign — same shape as a session that ran, idled out, and is now reattachable. A subsequent attach goes through `Pool.Activate` (the same primitive used here) and brings it up. See [lessons.md § Lock-order pitfalls when a callee persists](../../lessons.md#lock-order-pitfalls-when-a-callee-persists) for the lock-order discipline.

**Why register-allocated *after* persist, *before* activate.** `RegisterAllocatedUUID` has a 30s TTL window. It must fire before claude opens the JSONL (so the watcher's skip-set has the UUID when the CREATE fsnotify event lands). Doing it after persist (rather than before) makes the order robust to a slow registry write — TTL countdown starts from a known-recent moment.

**Why supervise *before* Activate.** `Session.Activate` sends on `activateCh` (buffered 1) then waits on `activeCh` until the lifecycle goroutine flips to active. If `sess.Run` isn't running yet, the buffered signal is held and `Activate` blocks until ctx cancels. So `supervise` (which schedules `sess.Run`) must precede `Activate`. If `supervise` returns `ErrPoolNotRunning`, bail before calling `Activate` — no goroutine to wake.

**Cap pre-flight via `Pool.Activate` directly (choice (a)).** Two viable shapes were considered: (a) call `Pool.Activate(ctx, id)` and let its existing cap path evict, or (b) run a cap pre-flight before registering. Choice (a): the new session IS in the pool and IS in `stateEvicted` — same shape as any other evicted session being reactivated. No duplicated cap logic, no new code path through `pickLRUVictim`, no transient inconsistent view from `Snapshot`/`Lookup` (which (b) would create).

**Failure-mode discriminator: id-or-empty.** The returned `SessionID` is the caller's signal:

| Failure point | Caller sees | On-disk state | In-memory state |
|---|---|---|---|
| `NewID` (rng) | wrapped err, `""` | unchanged | unchanged |
| `supervisor.New` | wrapped err, `""` | unchanged | unchanged |
| `saveLocked` | err verbatim, `""` | unchanged | rolled back |
| `supervise` | `ErrPoolNotRunning`, valid id | entry persisted | entry in map; no lifecycle goroutine |
| `Pool.Activate` | err verbatim (often `ctx.Err`), valid id | entry persisted | entry in map; lifecycle goroutine running; lcState may race to active |

Empty id ⇒ "nothing persisted, nothing to clean up." Non-empty id ⇒ "entry on disk, decide what to do (retry Activate, accept the eventual lifecycle, leave it for next pyry start)." Use `errors.Is(err, ErrPoolNotRunning)` to distinguish the not-running case from an Activate failure.

**ctx cancellation race.** If the caller cancels `ctx` after `supervise` succeeded but before `Activate` returns, `sess.Activate` may have already sent the buffered signal on `activateCh` before its ctx check. The lifecycle goroutine respects the *pool's* run-context (the errgroup's `gctx`), not the caller's, so the session may still spin up to active even though `Create` returns `(id, ctx.Err)`. This is the inherent shape of the buffered-signal lifecycle — tests should not depend on "Activate error → claude not running" as a hard invariant.

**Lock order — unchanged.** `Create` introduces no new ordering edges:

| Step | Locks | Order |
|---|---|---|
| Register + persist | `Pool.mu` (write) → `Session.lcMu` (briefly inside `saveLocked`) | `Pool.mu → Session.lcMu` ✓ |
| RegisterAllocatedUUID | `Pool.mu` (write) | trivially ✓ |
| supervise | `Pool.mu` (RLock) | trivially ✓ |
| Activate (cap path) | `capMu → Pool.mu` (RLock in pickLRU) → `Session.lcMu` | `capMu → Pool.mu → Session.lcMu` ✓ |

Critically: `Create` does NOT hold `p.mu` across `supervise`, `Activate`, or `RegisterAllocatedUUID`. The lock is taken only for the register+persist couple, then released. Concurrent `Create` calls each mint their own UUID via `crypto/rand` and serialise on `Pool.mu` for the persist couple; cap-path serialisation continues through `capMu` if `activeCap > 0`.

**Bridge: fresh per session in service mode.** `tpl.Bridge != nil` ⇒ `supervisor.NewBridge(p.log)` for the new session; `tpl.Bridge == nil` (foreground mode) ⇒ `nil`. Foreground-mode `Create` is operationally odd (the new session's output goes to its JSONL but has no live client) — not gated against, since the control verb path will only call `Create` in service mode.

**No new public types or sentinels.** `Pool.Create` is the only new exported name. `ErrPoolNotRunning` (from #72) is the only sentinel `Create` propagates.

### Pool.List (1.1b-A)

The typed read primitive Phase 1.1b-B's `pyry sessions list` CLI verb (#46-B) calls instead of poking at `sessions.json` directly. One method, one new value type, no error path.

```go
type SessionInfo struct {
    ID             SessionID
    Label          string         // synthetic "bootstrap" substituted for the
                                  // bootstrap entry when its on-disk label is
                                  // empty; on-disk value is unchanged.
    LifecycleState lifecycleState
    LastActiveAt   time.Time
    Bootstrap      bool           // true for the bootstrap entry; lets
                                  // consumers disambiguate without re-checking IDs.
}

func (p *Pool) List() []SessionInfo
```

**Deep-copy by construction.** Every field is a value type (`SessionID`/`string`/`time.Time` are values; `lifecycleState` is a `uint8` enum). Mutating a `SessionInfo` cannot affect pool state or registry contents — no defensive cloning, no documentation contract that callers can violate.

**Sort: `LastActiveAt` desc, `SessionID` asc tiebreak.** Most-recent-first matches operator intuition (the session you just used is at the top). The id tiebreak makes ordering deterministic across calls — important for unit tests where time freezes; degenerate at runtime. Uses `sort.Slice` to match `registry.go`'s style.

**Bootstrap label substitution lives here, not in 46-B's renderer.** The wire payload is self-explanatory (every consumer gets `"bootstrap"` instead of the empty string for the unlabelled bootstrap entry) and the on-disk registry entry is **not** mutated. An operator-set bootstrap label passes through verbatim — `Bootstrap` (the bool) is the discriminator, not the label string.

**Why a new type, not extending `SnapshotEntry`.** `SnapshotEntry{ID, PID}` exists for the rotation watcher's closure-over-primitives boundary (`internal/sessions/rotation` cannot import `internal/sessions`). Adding `lifecycleState` to it would either bloat every rotation snapshot or push the enum into `rotation`'s import set. Two distinct types, one per consumer, is cleaner than a shared shape.

**Lock order — unchanged.** `Pool.mu` (RLock) → `Session.lcMu` (Lock). Identical to `Pool.saveLocked` and `Pool.pickLRUVictim`. The bootstrap flag, label, and id are immutable post-`New` / post-`RotateID` from any reader holding `Pool.mu`, so they're read off-`lcMu`; `lcState` and `lastActiveAt` MUST be read under `lcMu` (the lifecycle goroutine writes them under `lcMu` in `transitionTo` and `touchLastActive`). Each session's pair is read under one `lcMu` acquire — no torn reads.

**Read-only.** No `lastActiveAt` bump, no state transition, no `persist()` call. The AC's "registry fields unchanged on disk after the call" is a direct consequence of not calling `saveLocked`.

**Concurrent List:** standard RWMutex semantics — concurrent `List` callers don't contend with each other; concurrent `List + Create` (or `List + RotateID`) sees one ordering or the other, neither corrupts the result. Race-clean under `-race`.

### Pool.Rename (1.1c-A)

The typed write primitive Phase 1.1c-B's `pyry sessions rename` CLI verb (#47-B) calls instead of poking at `sessions.json` directly. One method, no new types, no new sentinel errors.

```go
func (p *Pool) Rename(id SessionID, newLabel string) error
```

**Sequence under `Pool.mu` (write):**

1. Resolve `id` in `p.sessions`. Unknown ⇒ `ErrSessionNotFound`; in-memory and on-disk state byte-identical to before (no `saveLocked` call).
2. If `sess.label == newLabel`, no-op return — skips `saveLocked` so the registry mtime stays stable for an idempotent rename. Same precedent as `RotateID(x, x)`.
3. Otherwise: snapshot `prev := sess.label`, set `sess.label = newLabel`, call `saveLocked()`. On save failure, restore `sess.label = prev` and return the error verbatim.

**Why `Pool.mu`, not `Session.lcMu`.** `Session.label` is read under `Pool.mu` by `List` (RLock) and `saveRegistryLocked` (Lock — caller holds write). The lifecycle goroutine in `Session.Run` does not read `label`. So `Pool.mu` is the correct guard; taking `lcMu` would add a lock-order edge for no benefit. The doc-comment on `Session.label` (`session.go:68-72`) was updated to reflect that `label` is mutable via `Pool.Rename` under `Pool.mu` (write).

**Why hold `Pool.mu` across the disk write.** Releasing it between the in-memory mutation and the disk write would let a concurrent `Lookup` observe the new label while the disk still has the old one — exactly the in-memory-vs-on-disk drift the existing locking discipline rules out. Matches `RotateID` and every `Pool.persist`-from-`Session.transitionTo` callback.

**Save-failure rollback is belt-and-suspenders.** `saveRegistryLocked`'s temp+rename discipline makes the rename the commit point — partial writes are unreachable, so disk state never disagrees with the rolled-back in-memory state. The rollback exists so a subsequent retry has consistent inputs and a `Lookup` after a failed `Rename` doesn't return a label that isn't on disk. Error returned verbatim (no `Rename:`-specific wrap) — `saveLocked`/`saveRegistryLocked` already produce well-prefixed `registry: …` errors and double-wrapping breaks `errors.Is` symmetry with other persist sites (`RotateID`, `Create`, `Session.transitionTo`).

**Bootstrap-label semantics.** `Rename` writes the verbatim string through to disk and does **not** branch on `sess.bootstrap`. Two cases fall out for free:

- `Rename(bootstrapID, "")` — clears on-disk label; `Pool.List` then re-applies the synthetic `"bootstrap"` substitution introduced in #60.
- `Rename(bootstrapID, "primary")` — persists `"primary"`; `Pool.List` reflects it verbatim (no synthetic substitution, since the on-disk value is non-empty). The disk record is still bootstrap-flagged (`Bootstrap: true`).

The substitution rule lives in `List`, not in `Rename` — keeping the writer simple and the substitution uniform across consumers.

**No validation.** Empty strings are explicitly permitted by the AC; nothing else is mentioned. Length caps, character-class restrictions, and uniqueness checks belong at the CLI layer (47-B) where operator-input policy lives. Same posture as `Pool.Create`'s unvalidated `label` parameter.

**Strict full-`SessionID` only.** UUID-prefix resolution is a CLI/UX concern; the pool primitive matches on the full id to keep the API surface and the test matrix small. Phase 1.1c-B can resolve a prefix via `Pool.List` and then call `Rename` with the full id.

### Pool.Remove (1.1d-A1)

The typed delete primitive the future `pyry sessions rm` CLI verb (#65) calls instead of touching processes or `sessions.json` directly. One method, one new exported sentinel (`ErrCannotRemoveBootstrap`), no other type additions.

```go
var ErrCannotRemoveBootstrap = errors.New("sessions: cannot remove bootstrap session")
func (p *Pool) Remove(ctx context.Context, id SessionID) error
```

**Sequence — delete-then-evict.**

1. Take `Pool.mu` (write).
2. Resolve `id` in `p.sessions`. Unknown ⇒ release `Pool.mu`, return `ErrSessionNotFound` (in-memory + on-disk state byte-identical, no `saveLocked` call).
3. If `sess.bootstrap` ⇒ release `Pool.mu`, return `ErrCannotRemoveBootstrap` (bytes-identical, same as above).
4. `delete(p.sessions, id)`, then `saveLocked()`. On save failure: restore `p.sessions[id] = sess`, release the lock, return the error verbatim. (Mirrors `Pool.Rename`'s rollback discipline.)
5. Release `Pool.mu`.
6. Call `sess.Evict(ctx)`. Returns only after the child has exited (or `ctx` cancels). The on-disk JSONL is **not** touched — disposition (archive / purge) is 64-A2 / #95.

**Why delete-then-evict (not evict-then-delete).** Holding `Pool.mu` across `Session.Evict` deadlocks: the lifecycle goroutine's `transitionTo` calls `Pool.persist`, which reacquires `Pool.mu` (write). The cap-policy path (#41) hits the same constraint and uses `capMu` as the outer mutex; here the simpler resolution is to release `Pool.mu` after the in-memory delete commits — concurrent `Lookup` / `Activate` / `Rename` / `List` callers see the session as gone from that moment on, so there's no half-removed state for any observer to witness, and no risk of a re-spawn race against a session that's about to die.

**Why `ctx` (not the AC's bare `id` shape).** `Session.Evict` already accepts a context; passing one through keeps the (potentially long-lived) termination interruptible and matches `Pool.Activate(ctx, id)` / `Pool.Create(ctx, label)`.

**Bootstrap rejection is structural, not policy.** The bootstrap is the per-process invariant `Pool.Lookup("")` resolves to. Removing it would leave the pool in a state no caller can satisfy without an explicit re-bootstrap pass. The sentinel surface lets the CLI distinguish "operator targeted bootstrap" from "id not found" without string-matching error text.

**Termination reuse.** `Pool.Remove` does not re-implement SIGTERM/SIGKILL/grace logic. `Session.Evict` already drives the supervisor's child via `exec.CommandContext` cancel ⇒ SIGKILL ⇒ `cmd.Wait` returns. The supervisor today does not have a SIGTERM grace window — earlier docs that referenced one describe an aspiration, not the current behaviour. SIGKILL is uncatchable, so no fallback path is needed.

**Already-evicted sessions are a fast path.** If `sess` is already in `stateEvicted` (prior idle eviction, prior cap-policy eviction, or warm-started in evicted), `Session.Evict` is an immediate no-op — no second persist runs, the registry write in step 4 is the only mutation.

**Lifecycle goroutine after Remove.** Once `Pool.Remove` returns, `sess.Run`'s loop transitions to `runEvicted` and parks on `<-s.activateCh` and `<-ctx.Done()`. The session is no longer reachable via `Pool.sessions`, so no caller can signal `activateCh`; the goroutine survives until `Pool.Run`'s `runCtx` cancels at pool shutdown (typically pyry exit). Bounded resource cost: one orphan goroutine + ~kilobyte `*Session` per `Remove` per pool lifetime. For pyrycode's expected workload (low-tens of sessions per pool, operator-driven removes) this is operationally invisible. Per the project's evidence-based fix selection, do not add a per-session terminate signal until observed.

**No `Session.lcMu` taken.** `Pool.Remove` does not read `lcState` / `lastActiveAt` / `attached`; the lifecycle goroutine continues to take `lcMu` inside `transitionTo` exactly as before. Lock-order graph (`Pool.capMu → Pool.mu → Session.lcMu`) is unchanged.

**Strict full-`SessionID` only.** Same posture as `Pool.Rename`. Empty-id is *not* the bootstrap shorthand it is in `Pool.Lookup` — empty falls through to the "not in map" branch and returns `ErrSessionNotFound`. Destructive operations require an explicit id.

### Pool.Remove JSONL disposition (1.1d-A2 / #95)

Phase 1.1d-A2 adds a `RemoveOptions` parameter so callers pick the on-disk disposition. The signature became `Pool.Remove(ctx, id, opts RemoveOptions) error`; `RemoveOptions{}` (zero value) is byte-identical to the 1.1d-A1 behaviour above (JSONL untouched).

```go
type JSONLPolicy uint8

const (
    JSONLLeave   JSONLPolicy = iota // do not touch the JSONL (default)
    JSONLArchive                    // mv to <pyry-data-dir>/archived-sessions/<uuid>.jsonl
    JSONLPurge                      // delete the JSONL
)

type RemoveOptions struct {
    JSONL JSONLPolicy
}

func (p *Pool) Remove(ctx context.Context, id SessionID, opts RemoveOptions) error
```

**Why an enum, not two booleans.** A `bool Archive`/`bool Purge` shape makes (true, true) a representable-but-illegal state. The enum makes the "exactly one disposition" property type-level, the zero value is well-defined (Leave), and the dispatch switch is exhaustive.

**Why a struct, not a positional `JSONLPolicy` parameter.** Future options (`Force`, `Reason`, …) stay additive at zero call-site churn. Same precedent as stdlib `os.RemoveAll` would have been if it took options.

**Pyry data-dir resolution.** No new config knob — the per-instance data-dir is the **parent of `Pool.registryPath`** (`~/.pyry/<sanitized-name>/sessions.json` ⇒ `~/.pyry/<sanitized-name>/`). `claudeSessionsDir` is claude's directory (the JSONL *source*), not the pyry-owned destination root. When `registryPath == ""` (test/disabled mode), `JSONLArchive` errors with `"sessions: archive requires a registry path"`; `JSONLPurge` and `JSONLLeave` are no-ops.

**Disposition runs under `Pool.mu` after `saveLocked`.** Single critical section, single observable transition: a concurrent `Pool.List` either sees the session present (registry + JSONL both untouched) or absent (registry + JSONL both at their final state). The held-lock window grows by one stat + one rename or unlink — single-syscall granularity, well inside the existing `saveLocked` envelope. POSIX inode semantics keep the operation safe even though claude may still hold the JSONL fd open (rename preserves the fd→inode binding; unlink lets pending writes drain into the soon-to-be-orphaned inode).

**Source-absent semantics.** Both `JSONLArchive` and `JSONLPurge` are success no-ops when the live JSONL is missing — symmetric "ensure the file is at its target state" intent.

**Destination-exists semantics.** Archive errors (wrapping `fs.ErrExist`) when `<archiveDir>/<uuid>.jsonl` already exists. Re-archiving the same UUID is almost always a bug; silent overwrite would lose transcript history. The `errors.Is(err, fs.ErrExist)` shape leaves room for a future CLI `--force`.

**`os.Rename`, not copy + unlink.** Source and destination both live under `$HOME` in normal deployments — same filesystem. EXDEV would surface as a clear error rather than silent corruption. No copy-then-delete fallback today; defer until observed.

**Failure ordering — registry committed before disposition.** On `saveLocked` failure: in-memory delete rolls back, JSONL untouched, child not terminated. On `disposeJSONLLocked` failure: registry already removed (in-memory + on-disk), `Session.Evict` is *still* called (the registry says the session is gone, the child must follow), the disposition error is returned. If disposition and Evict both fail, disposition wins (the new failure mode this signature introduces, and the more actionable one).

**Bootstrap and unknown-id rejection still run before disposition.** `Remove(bootstrapID, {JSONL: JSONLPurge})` does *not* touch the bootstrap's JSONL — structural invariants take precedence over destructive opts.

### Pool.ResolveID (1.1e-A)

The typed prefix resolver Phase 1.1e-B's `pyry attach <id>` wire + CLI surface (and any future verb taking a session selector) consumes instead of inlining the same `strings.HasPrefix` walk over `Pool.List`. The natural pairing with `Pool.Lookup`:

| Caller-supplied input | API |
|---|---|
| canonical `SessionID` (or `""`) | `Pool.Lookup(id)` |
| user input string (UUID, prefix, or `""`) | `Pool.ResolveID(arg)` |

```go
var ErrAmbiguousSessionID = errors.New("sessions: ambiguous session id")

func (p *Pool) ResolveID(arg string) (SessionID, error)
```

**Resolution order, all under `Pool.mu` (RLock):**

1. `arg == ""` ⇒ bootstrap id, no error. Same seam as `Pool.Lookup("")`.
2. `arg` is an exact key in the in-memory `p.sessions` map ⇒ that id, no error. Single map lookup; the short-circuit never falls through to the prefix scan, so a full-UUID match always wins over any coincidental prefix overlap with no extra scan cost.
3. Scan `p.sessions`, collect every `*Session` whose `SessionID` has `arg` as a prefix (`strings.HasPrefix`). Exactly one match ⇒ that id. Zero ⇒ `ErrSessionNotFound` (reused). ≥2 ⇒ `ambiguousError(matches)`.

**Sentinel + `fmt.Errorf("%w: …")` over a struct error type.** The AC required "the simpler shape that keeps `errors.Is` matching cheap" and "no new exported types beyond the new typed error." A struct error with `Matches []SessionRef` would expose a second exported type and force every consumer to either type-assert or duplicate the formatting. The chosen shape — `var ErrAmbiguousSessionID = errors.New(...)` plus `fmt.Errorf("%w:\n%s", ErrAmbiguousSessionID, lines)` — gives `errors.Is` one pointer compare via the wrapped chain, lets the CLI consumer `fmt.Fprintln(os.Stderr, err)` and get a human-readable list verbatim, and stays within the AC's exported-surface budget.

**Match list formatting.** Sorted by `SessionID` ascending (same tiebreak as `Pool.List`) so the error message is deterministic and tests can pin the exact substring. Each line is `<uuid> (<label>)`. The synthetic `"bootstrap"` substitution from `Pool.List` is mirrored one-for-one — when the bootstrap entry's on-disk label is empty, the formatter writes `<uuid> (bootstrap)` rather than `<uuid> ()`. Otherwise operators would see one name in `pyry sessions ls` and another in the disambiguation prompt.

**No `Session.lcMu` taken.** `ResolveID` reads only `id` (mutated by `RotateID` under `Pool.mu` write — invariant documented at that site) and `label` (mutated by `Pool.Rename` under `Pool.mu` write). Both sit under `Pool.mu`'s reader set; no new lock-order edges. `lcState` and `lastActiveAt` are not consulted — `ResolveID` does **not** filter by lifecycle state. An evicted session is still a registry entry, and `pyry sessions rename <prefix>` / `pyry sessions rm <prefix>` must still resolve it. Filtering, if any, is a verb-layer policy (e.g. a future `pyry attach` may bounce active-vs-idle differently).

**No minimum prefix length.** A one-character prefix is accepted as long as it is unique. Refusing short prefixes (1- or 2-char) for safety is a CLI-layer guard, not a pool invariant — the same posture as `Pool.Rename` declining to validate `newLabel` and `Pool.Create` declining to validate `label`.

**No whitespace trimming.** The pool primitive accepts whatever string the caller hands it. Trimming is the CLI's responsibility (`flag` already handles positional args; an explicit `strings.TrimSpace` at the CLI layer is one line).

**Returns `SessionID`, not `*Session` / `SessionInfo`.** Smallest possible surface, symmetric with the rest of the wire/CLI flow: 1.1e-B unmarshals an id from the request, calls `ResolveID`, then routes to `Lookup` / `Activate` / `Remove` with the resolved id. Returning `*Session` would tempt callers to short-circuit the second lookup — but the second lookup is the lock-clean way to guard against a session being removed between resolve and use, and saving the second hashmap probe is not worth the sharp edge.

**Concurrency.** `Pool.mu` (RLock) for the whole call. Concurrent `ResolveID + List` share the read lock and run truly concurrently. Concurrent `ResolveID + writer` (`Rename` / `Create` / `Remove` / `RotateID`) blocks briefly behind the writer and observes either pre- or post-write state — the same race-clean shape callers were already prepared for. Concurrent `ResolveID + ResolveID` share the read lock with no contention. The lock-order graph (`Pool.capMu → Pool.mu → Session.lcMu`) is unchanged.

**A session removed mid-resolve** either appears in the scan (caller then races on the second `Lookup` and sees `ErrSessionNotFound`) or doesn't — both outcomes are valid races the consumer was already prepared for. No error wrapping prefix is added (the wrapped sentinel already begins with `sessions: …`; double-wrapping would just produce `sessions: resolve: sessions: …`).

**Out of scope** (handed to 1.1e-B): wire protocol field carrying the resolved/unresolved id, control verb routing, CLI argument parsing, refactoring 47-B / 48-B's inlined prefix resolvers (opportunistic when those callers are next touched).

## Concurrency

`sync.RWMutex` on `Pool.sessions`:

- `Lookup` and `Default` take the read lock.
- `Run` takes the read lock once briefly to grab the bootstrap pointer and `claudeSessionsDir`.
- Writers: `RotateID` (1.2b-A), `RegisterAllocatedUUID` / `IsAllocated` mutations (1.2b-B), `persist` (1.2c-A; called from `Session.transitionTo`). Phase 1.1's `Pool.Add(SessionConfig)` plugs in the same way.

`sync.Mutex` on each `Session.lcMu` (1.2c-A): protects `lcState`, `attached`, `activeCh`, `lastActiveAt`. **Lock order: `Pool.mu → Session.lcMu`**. `Session.transitionTo` releases `lcMu` *before* calling `Pool.persist` so `saveLocked`'s per-session re-acquire can't deadlock. `RotateID` mutates `session.id` without `lcMu` — documented invariant: today's only callers run before any lifecycle goroutine begins observing the id.

Goroutines introduced in this layer (1.2c-A):

1. **Per-Session lifecycle goroutine** — body of `Session.Run`, owns the `active ↔ evicted` state machine and idle timer.
2. **Per-active-period supervisor goroutine** — wraps `s.sup.Run(subCtx)` and pipes the result to `runErr`.
3. **Per-attach detach-watcher** — decrements `attached` when the bridge's done channel fires.

The PTY spawn / wait / backoff loop, the I/O bridge goroutines, and the SIGWINCH watcher all remain in their existing packages.

## Errors

| Condition | Surface |
|---|---|
| `NewID` rng failure | `sessions.New` returns wrapped error. Fatal. |
| `supervisor.New` failure | Wrapped: `sessions: bootstrap supervisor: %w`. Fatal. |
| `Pool.Lookup` unknown id | `ErrSessionNotFound` (sentinel). |
| `Pool.Rename` unknown id | `ErrSessionNotFound` (sentinel). On-disk + in-memory state byte-identical to before. |
| `Pool.Rename` save failure | Wrapped error from `saveLocked` propagated verbatim; in-memory label rolled back to prior value. |
| `Pool.Remove` unknown id | `ErrSessionNotFound` (sentinel). On-disk + in-memory state + JSONL byte-identical to before. |
| `Pool.Remove` bootstrap target | `ErrCannotRemoveBootstrap` (sentinel). On-disk + in-memory state + JSONL byte-identical to before. |
| `Pool.Remove` save failure | Error from `saveLocked` propagated verbatim; in-memory delete rolled back; child not terminated. |
| `Pool.Remove` ctx cancellation during Evict | `ctx.Err()` from `Session.Evict`. Registry entry already deleted and persisted; child terminates asynchronously under the pool's `runCtx`. |
| `Pool.Remove` archive destination exists | Wrapped error matching `errors.Is(err, fs.ErrExist)`. Registry entry already removed (disposition runs after persist); live JSONL and existing archive both untouched; child still terminated by Evict. |
| `Pool.Remove` archive when registry persistence disabled | `"sessions: archive requires a registry path"`. Registry entry already removed; child still terminated by Evict. |
| `Pool.ResolveID` no match | `ErrSessionNotFound` (sentinel). |
| `Pool.ResolveID` ≥2 prefix matches | `ErrAmbiguousSessionID` (sentinel) wrapped with `fmt.Errorf("%w:\n%s", …)`; message lists each `<uuid> (<label>)` pair on its own line, sorted by `SessionID` asc; bootstrap-empty-label substituted with `"bootstrap"`. |
| `Pool.supervise` before/after `Run` | `ErrPoolNotRunning` (sentinel). |
| `Session.Attach` with nil bridge | `ErrAttachUnavailable` (sentinel). |
| `Session.Attach` while bridge busy | `supervisor.ErrBridgeBusy` propagated **verbatim** — no wrap. |
| `Session.Run` / `Pool.Run` ctx cancel | `context.Canceled` from the supervisor. |

Sentinels (`ErrSessionNotFound`, `ErrAttachUnavailable`, `ErrPoolNotRunning`, `ErrCannotRemoveBootstrap`, `ErrAmbiguousSessionID`) live in `internal/sessions`. `supervisor.ErrBridgeBusy` stays in `internal/supervisor`.

## Dependency Direction

```
internal/sessions  →  internal/supervisor
```

`internal/sessions` imports `internal/supervisor`. The reverse is forbidden — verifiable with `go list -deps ./internal/supervisor/...`. `internal/sessions` does **not** import `internal/control`; control will (after Phase 1.0b) import sessions for `SessionID` and the resolver interface, never the other way around.

## Testing

Three test files mirror the production layout. Stdlib `testing` only.

- **`id_test.go`** — format regex match, 1000-iteration uniqueness smoke test for `crypto/rand` wiring.
- **`pool_test.go`** — bootstrap installation, `Lookup("")` ↔ `Default()` identity, lookup by ID, unknown-ID sentinel match. Uses `/bin/sleep` as the "claude" binary; tests never call `Run`, so it's never spawned.
- **`pool_create_test.go`** (1.1a-A2) — `HappyPath` (UUID + entry shape after `Run` is live); `BootstrapUnchanged` (`Default()` returns the same `*Session` pointer pre/post `Create`); `LabelRoundTrip` (empty + non-empty labels round-trip via JSON unmarshal, not string match); `CapPassthrough_EvictsLRU` (`ActiveCap=1` evicts the bootstrap when `Create` activates); `SuperviseFails_EntryOnDisk` (no `Run` ⇒ `ErrPoolNotRunning`, valid id, entry on disk, ChildPID=0); `PersistFails_NoEntry_NoSpawn` (`registryPath` set to a non-directory path ⇒ empty id, no entry, only bootstrap in `Snapshot`).
- **`pool_list_test.go`** (1.1b-A) — `BootstrapOnly` (single entry, `Bootstrap=true`, `Label="bootstrap"`, on-disk `label` re-read from `sessions.json` is still empty); `OrderingByLastActive` (mutate three sessions' `lastActiveAt` under `lcMu` directly to `t0` / `t0+1m` / `t0+2m`, assert desc order; add a fourth equal-time entry, assert id-asc tiebreak is stable across two `List` calls); `BootstrapLabelPassthrough` (warm-start from a `sessions.json` whose bootstrap entry has `label: "main"` — synthetic substitution does NOT clobber); `RaceClean` (N goroutines × 100 `List` calls plus a mutator goroutine, `-race`-clean).
- **`pool_rename_test.go`** (1.1c-A) — `RoundTrip` (rename bootstrap to `"main"`; assert in-memory via `List`, on-disk by re-reading `sessions.json`); `EmptyClears` (rename to `"foo"` then to `""`; assert on-disk label is empty AND `List[0].Label == "bootstrap"` synthetic substitution resumes); `UnknownID` (zero-UUID returns `ErrSessionNotFound`, `bytes.Equal(before, after)` for the on-disk file, `List` snapshot deep-equal); `RaceWithList` (concurrent `Rename` + `List` goroutines under `-race`); `BootstrapPersistsAndShows` (rename bootstrap to `"primary"`; assert on-disk `Bootstrap=true` AND `Label="primary"`, `List[0].Label == "primary"` — no synthetic substitution).
- **`pool_resolve_id_test.go`** (1.1e-A) — `EmptyReturnsBootstrap`; `FullUUID`; `UniquePrefix` (1/4/8/16/35-char prefixes against a single-bootstrap pool); `FullUUIDBeatsPrefix` (synthetic two-session pool built via in-package `pool.sessions[id] = &Session{…}` writes; passes a full id whose prefix would also match a sibling and asserts the exact match wins); `AmbiguousPrefix` (synthetic two-session pool sharing a prefix; asserts `errors.Is(err, ErrAmbiguousSessionID)` plus the exact sorted match-list substring including the `"bootstrap"` substitution); `NoMatch` (zero-UUID + clearly-non-prefix `"zzzz"` both return `ErrSessionNotFound`); `RaceWithList` (concurrent `ResolveID` + `List` goroutines under `-race`).
- **`pool_remove_test.go`** (1.1d-A1) — `HappyPath` (Create + Remove a non-bootstrap session: assert child PID gone, `Lookup` returns `ErrSessionNotFound`, registry on disk has bootstrap only, stub JSONL byte-identical); `Bootstrap_Rejected` (Remove bootstrap returns `ErrCannotRemoveBootstrap`; registry bytes/mtime + `List` snapshot + JSONL byte-identical); `UnknownID` (zero-UUID returns `ErrSessionNotFound`; same byte-identity assertions); `RaceWithList` (concurrent Create+Remove writers and `List` readers under `-race`); `TerminatesUncooperativeChild` (`/bin/sh -c 'trap "" TERM INT HUP; exec sleep 86400'` as the fake claude — SIGKILL via `exec.CommandContext` cancel terminates it inside a 10s budget, no real-time `time.Sleep` in the assertion).
- **`session_test.go`** — `State` delegation, `Attach` with no bridge, `Attach` busy via `io.Pipe` (first attach blocks on input, second races and gets `supervisor.ErrBridgeBusy`), `Run` ctx-cancel via a real `/bin/sleep 3600` child.

### Why no `TestHelperProcess` re-exec helper

The parent spec considered duplicating `internal/supervisor`'s `TestHelperProcess` re-exec pattern into the sessions package (~20 lines) per the project's "duplicate, don't export test surface" convention. The blocker: `supervisor.Config.helperEnv` is unexported and is the only way to pass test-only env to the spawned child without polluting the parent test process's `os.Environ()`. External packages cannot set it.

The chosen workaround is to use a real benign binary (`/bin/sleep`) as the fake claude. No re-exec, no env injection, no helper duplication. The supervisor spawns it, ctx cancellation kills it, `supervisor.Run` returns `ctx.Err()` — which is the only contract the test asserts.

`/bin/sleep` exists on both Linux and macOS; CI runs both. If a future CI environment lacks it, `t.Skipf` on `exec.LookPath` failure rather than silently passing.

## Production Consumers (Phase 1.0b)

After #29, `cmd/pyry/main.go` constructs `*sessions.Pool` and `internal/control` consumes a `SessionResolver` (defined inside `internal/control` — see [control-plane.md](control-plane.md)). External behaviour is unchanged:

- `Request`/`Response` JSON shapes unchanged. No `session_id` field yet.
- No new log lines; the bootstrap session ID is **not** logged.
- Startup log line preserved verbatim (`pyrycode starting` with the same fields).
- `pyry status`/`stop`/`logs`/`attach` are byte-identical to Phase 0.
- Foreground vs service mode keys off `term.IsTerminal(os.Stdin.Fd())` in `cmd/pyry/main.go`, unchanged.
- Restart still uses `--continue`. `claude --session-id <uuid>` is **not** plumbed in 1.0.

`*sessions.Pool` does not satisfy `control.SessionResolver` directly: `Pool.Lookup` returns the concrete `*sessions.Session`, while the resolver interface returns `control.Session`. Go does not do covariant return types on interface satisfaction, so `cmd/pyry` defines a 5-line `poolResolver` adapter to bridge the two. See [lessons.md](../../lessons.md#interface-adapters-for-covariant-returns) and [control-plane.md](control-plane.md).

## References

- Spec: [`docs/specs/architecture/28-sessions-package.md`](../../specs/architecture/28-sessions-package.md)
- Parent design: [`docs/specs/architecture/27-session-addressable-runtime.md`](../../specs/architecture/27-session-addressable-runtime.md)
- ADR: [`003-session-addressable-runtime.md`](../decisions/003-session-addressable-runtime.md)
- Locked phase design: [`docs/multi-session.md`](../../multi-session.md), [`docs/plan.md`](../../plan.md)
