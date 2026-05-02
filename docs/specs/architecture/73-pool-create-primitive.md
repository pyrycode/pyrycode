# Spec: `Pool.Create` primitive — mint, persist, activate (#73)

## Context

Phase 1.1a-A delivers a single new public seam on `*Pool`: a method that
takes a label, mints a fresh session, and brings it up under the existing
spawn path. The internal supervision plumbing (`Pool.supervise`,
`runGroup`/`runCtx`, `ErrPoolNotRunning`) is already in place from the
sibling ticket #72 — this ticket consumes it.

Today's pool can:

- Mint a UUID (`NewID`, `internal/sessions/id.go`)
- Persist the registry under `Pool.mu` (`Pool.saveLocked`,
  `internal/sessions/pool.go:476`)
- Run a session through the cap-aware spawn path (`Pool.Activate`,
  `internal/sessions/pool.go:530`)
- Skip a freshly-minted UUID's CREATE on the rotation watcher
  (`Pool.RegisterAllocatedUUID`, `internal/sessions/pool.go:430`)
- Schedule a `*Session`'s `Run` onto `Pool.Run`'s errgroup
  (`Pool.supervise`, `internal/sessions/pool.go:386`)

What's missing is the entry point that ties all five together. Every
downstream caller — Phase 1.1a-B's `sessions.new` control verb (#71),
Phase 2.0's channel-driven auto-mint, future operator tooling — should
hit one well-tested seam, not re-derive the sequence.

The seam exists because the locked design says: claude runs with
`--session-id <uuid>` baked in (so claude doesn't pick the UUID itself),
and the rotation watcher's skip-set must be primed before claude opens
the JSONL (so the CREATE doesn't trigger a spurious `RotateID`). Both of
those interlocks are non-obvious and re-deriving them per caller is
exactly the kind of thing that breaks silently in Phase 2.

Design sources (read before reviewing this spec):

- `docs/multi-session.md` — Phase 1.1 row, locked direction
- `docs/knowledge/features/sessions-package.md` — current `Pool` shape
- `docs/knowledge/features/sessions-registry.md` — `sessions.json` schema
- `docs/knowledge/features/idle-eviction.md` — `Session.Activate` lifecycle
- `docs/specs/architecture/72-pool-supervise-seam.md` — sibling, hard
  prerequisite
- `docs/specs/architecture/41-concurrent-active-cap.md` — cap-aware
  spawn path; `pickLRUVictim` semantics
- `docs/lessons.md` § "Lock-order pitfalls when a callee persists"

## Design

### New surface

One new exported method, one new unexported field on `*Pool`.

```go
// Create mints a fresh session, persists it, and brings it up under
// the cap-aware spawn path. Returns the new SessionID and an error.
//
// The returned id is valid (and the registry entry is on disk) even
// when err is non-nil, *unless* err comes from the persist step — see
// the failure-mode table below. Callers should treat the id as
// authoritative whenever it is non-empty.
//
// Requires Pool.Run to be active. Returns ErrPoolNotRunning when the
// supervise seam is not wired; in that case the session has been
// persisted to the registry but no lifecycle goroutine is running.
//
// Concurrency: safe for concurrent use. Each call serialises through
// Pool.mu briefly (registration + persist), then runs the
// supervise/activate sequence off-lock under the cap-aware path's
// existing capMu serialisation.
func (p *Pool) Create(ctx context.Context, label string) (SessionID, error)
```

```go
// Pool fields (one addition, declared adjacent to existing config-
// derived fields).
type Pool struct {
    // ... existing fields ...

    // sessionTpl is the per-session template captured from
    // cfg.Bootstrap at New(). Pool.Create copies this, overrides
    // ResumeLast/ClaudeArgs, and (in service mode) mints a fresh
    // Bridge so each new session gets its own I/O channel. Read-only
    // after New — no lock needed.
    sessionTpl SessionConfig
}
```

`sessionTpl` is a shallow copy of `cfg.Bootstrap`. The `Bridge` field is
*kept* — its presence is the service-mode signal (non-nil = service mode,
nil = foreground). `Pool.Create` uses it to decide whether to mint a
fresh `*supervisor.Bridge` for the new session.

No new exported types or sentinels. `ErrPoolNotRunning` (from #72) is
the only sentinel `Create` propagates.

### Sequence

```
Create(ctx, label)
  ├─ id = NewID()                      // fresh UUIDv4
  ├─ build SessionConfig from sessionTpl:
  │    ResumeLast = false
  │    ClaudeArgs = clone(tpl.ClaudeArgs) ++ ["--session-id", id]
  │    Bridge     = nil OR supervisor.NewBridge(p.log)
  ├─ build supervisor.Config from per-session SessionConfig
  ├─ sup, err = supervisor.New(supCfg)
  │    err → return "", err            // no state mutated
  ├─ build *Session in stateEvicted:
  │    activeCh   = make(chan struct{})    // open
  │    evictedCh  = closedChan()           // closed
  │    activateCh = make(chan struct{}, 1) // buffered 1
  │    evictCh    = make(chan struct{}, 1) // buffered 1
  │    pool       = p
  │    bootstrap  = false
  │    label      = label (verbatim, empty preserved)
  │    createdAt  = time.Now().UTC()
  │    lastActiveAt = same instant
  │    idleTimeout = tpl.IdleTimeout (or cfg.IdleTimeout fallback —
  │                                   resolved at registration time
  │                                   the same way Bootstrap is)
  ├─ p.mu.Lock()
  │    p.sessions[id] = sess
  │    err = p.saveLocked()
  │    err → delete(p.sessions, id); p.mu.Unlock(); return "", err
  ├─ p.mu.Unlock()
  ├─ p.RegisterAllocatedUUID(id)       // primes the rotation skip-set
  ├─ err = p.supervise(sess)           // sess.Run scheduled on errgroup
  │    err → return id, err            // entry on disk; no lifecycle goroutine
  ├─ err = p.Activate(ctx, id)         // cap-aware; signals lifecycle goroutine
  │    err → return id, err            // entry on disk; lifecycle goroutine
  │                                       running; lcState may transition to
  │                                       active anyway (see "ctx cancellation
  │                                       race" below)
  └─ return id, nil
```

### Why persist before activate

A save failure with claude already running leaves an unsupervised
orphan: claude has opened its JSONL, started a conversation, and pyry
has no on-disk record that this session exists. The next pyry start
won't reconcile it; the JSONL becomes a ghost.

A registry-only entry that didn't activate is recoverable. The
operator (or the next call site) sees a UUID in `sessions.json` with
`lifecycle_state: evicted` and zero JSONL on disk. A subsequent attach
goes through `Pool.Activate` (the same primitive used here) and brings
it up. The persisted-but-not-running window is a benign state — exactly
the same shape as a session that ran, idled out, and is now
reattachable.

This rule is documented in the source via a one-line comment on the
persist call. Inline rationale, not a separate doc.

### Why register-allocated *after* persist, *before* activate

`RegisterAllocatedUUID` has a 30s TTL window. It must fire before
claude opens the JSONL (so the watcher's skip-set has the UUID when
the CREATE fsnotify event lands). The spawn — `Activate` →
`sess.Activate` → lifecycle goroutine flips to `runActive` →
supervisor spawns claude — is sub-second in production, well inside
the TTL.

The order persist → register-allocated → activate is robust to a
slow registry write: if `saveLocked` is unexpectedly slow (e.g.
fsync stall), `RegisterAllocatedUUID` happens *after* the slow step,
so the TTL countdown starts from a known-recent moment.

The TTL is a `var`, not a `const` (`internal/sessions/pool.go:19`),
so test injection can shorten it if needed. We don't need to here —
nothing in `Create` blocks long enough to matter.

### Cap pre-flight choice — choice (a), call `Pool.Activate` directly

Two viable shapes per the ticket body:

- **(a)** Call `Pool.Activate(ctx, id)`. Its existing cap path runs
  `pickLRUVictim(target=id)`, which excludes the target from the
  victim set. Since the new session is in `stateEvicted` at the
  point of the cap pre-flight, it doesn't increment `active` either
  way — the count reflects only currently-active peers. Cap=1 with
  one active peer: `active=1 >= cap=1`, peer evicted, then
  `sess.Activate` proceeds and the new session enters `stateActive`.
  Cap=2 with one active peer: `active=1 < cap=2`, no eviction,
  `sess.Activate` proceeds. Both correct.

- **(b)** Run the cap pre-flight before registering. Call
  `pickLRUVictim` against an early snapshot, evict, then register
  and activate.

Pick **(a)**. `Pool.Activate`'s contract isn't bent — the new
session IS in the pool and IS in `stateEvicted`, the same shape as
any other evicted session being reactivated. There is no duplicated
cap logic and no new code path through `pickLRUVictim`. The only
asymmetry from a "normal" reactivation is that the entry was
created milliseconds ago — but `pickLRUVictim` ranks by
`lastActiveAt`, which is set on register-time and (because the new
session is evicted) doesn't enter the active-set scan. The asymmetry
is invisible.

Choice (b) would require a new pre-flight code path *and* would
violate the "register before exposing the id" invariant — a victim
eviction that runs while the new session isn't in `p.sessions` means
`Snapshot` and `Lookup` see a transient inconsistent view. Not worth
it.

### Why supervise *before* Activate

`Session.Activate` (`internal/sessions/session.go:158`) sends on
`activateCh` (buffered 1) and then waits on `activeCh` until the
lifecycle goroutine flips state to active. If `sess.Run` isn't
running yet, the buffered signal is held and `Activate` blocks
forever (until ctx cancels).

So: `supervise` (which schedules `sess.Run`) must run before
`Activate`. The order in the sequence above is correct. If
`supervise` fails (sentinel `ErrPoolNotRunning`), bail before
calling `Activate` — no goroutine to wake.

### Bridge: fresh per session in service mode

When `sessionTpl.Bridge != nil` (service mode), the new session gets
a fresh `supervisor.NewBridge(p.log)`. Each session's PTY needs its
own bridge — sharing one bridge across multiple sessions would
multiplex their I/O into a single client view (not what anyone wants
when Phase 1.1a-B's `pyry attach <id>` lands).

When `sessionTpl.Bridge == nil` (foreground mode), the new session
gets `nil`. Foreground pyry has a single-session shape today —
adding a second session in foreground means the second session's
output goes to its JSONL but has no live client. That's fine for
this ticket (control verbs come in #71); `Attach` against the new
session's nil bridge returns `ErrAttachUnavailable`, which the
control plane already maps cleanly.

Foreground-mode Create is technically allowed but operationally odd.
We don't gate against it — the control verb path will only call
Create in service mode, and tests exercise both shapes.

### `--session-id <uuid>` baking

Append `--session-id` and the minted UUID to the cloned ClaudeArgs
slice. `slices.Clone` (Go 1.21+) is the right primitive — appending
to the template's slice directly would mutate `sessionTpl.ClaudeArgs`
across calls.

`ResumeLast = false` is set explicitly. The bootstrap's `--continue`
behaviour (driven by `ResumeLast` from the `-pyry-resume` flag) is
captured in `sessionTpl.ResumeLast` but is overridden here. New
sessions never resume — they ARE fresh.

The bootstrap's own resume behaviour is untouched: the bootstrap
entry was constructed in `New()` with `cfg.Bootstrap` (including
its `ResumeLast`); `Create` doesn't touch the bootstrap entry at
all.

## Concurrency model

Three sites in `Create` touch shared state:

1. **Persist phase** — `p.mu.Lock` held across `p.sessions[id] = sess`
   and `p.saveLocked()`. On failure, the rollback (`delete(p.sessions, id)`)
   runs under the same Lock. Released before any further work.

2. **RegisterAllocatedUUID** — takes `p.mu.Lock` independently. Trivial.

3. **supervise + Pool.Activate** — both run off-lock from `Create`'s
   perspective. `supervise` takes `p.mu.RLock` briefly (per #72).
   `Pool.Activate` takes `p.capMu` (cap path) and then `p.mu.RLock`
   (in `pickLRUVictim`) and `Session.lcMu` (in the per-session
   touch/Activate path).

### Lock order — unchanged

The existing documented orders are:

- `Pool.mu → Session.lcMu`
- `Pool.capMu → Pool.mu → Session.lcMu`

`Create` introduces no new orders:

| Step | Locks taken | Order |
|---|---|---|
| Register + persist | `Pool.mu` (write), then `Session.lcMu` (briefly inside `saveLocked` to read each session's state) | `Pool.mu → Session.lcMu` ✓ |
| RegisterAllocatedUUID | `Pool.mu` (write) | trivially ✓ |
| supervise | `Pool.mu` (RLock) | trivially ✓ |
| Activate (cap path) | `capMu`, then `Pool.mu` (RLock in pickLRU), then `Session.lcMu` (in pickLRU iteration and in `sess.Activate`) | `capMu → Pool.mu → Session.lcMu` ✓ |
| Activate (uncapped path) | `Session.lcMu` (in `sess.Activate`) | trivially ✓ |

Critically: `Create` does NOT hold `p.mu` across `supervise`,
`Activate`, or `RegisterAllocatedUUID`. The lock is taken only for
the register+persist couple, then released. This avoids re-entrant
acquisition (the same lesson #41 learned the hard way — see
`docs/lessons.md` § "Lock-order pitfalls when a callee persists").

### Concurrent `Create` calls

Two goroutines calling `Create` at the same time:

- Each mints its own UUID via `NewID` (crypto/rand; collision
  probability indistinguishable from 0 over the lifetime of any pyry
  process).
- Each contends on `p.mu.Lock` for the register+persist couple. They
  serialise. Each saveLocked write reflects both registrations
  in-order (whichever wins the lock first writes a registry with one
  new entry; the second writes a registry with two new entries).
- Both then independently RegisterAllocatedUUID, supervise, Activate.
  Cap path serialises through `capMu` if `activeCap > 0`.

No starvation, no inversion. The serial bottleneck is `Pool.mu`,
which is already the hot lock for this package — adding `Create` to
the set of writers is the same shape as `RotateID`.

### ctx cancellation race

If the caller cancels `ctx` after `supervise` succeeded but before
`Activate` returns: `sess.Activate` reads its cancelled ctx, returns
`ctx.Err`. But `sess.Activate` already sent the buffered signal on
`activateCh` *before* the ctx check. The lifecycle goroutine
(running `sess.Run` on the errgroup, in `runEvicted`) reads
`activateCh` and transitions to `stateActive` regardless of the
caller's ctx.

So `Create` may return `(id, ctx.Err)` while the session is, in fact,
spinning up to active. The AC's "operator sees a UUID that isn't
running" is the *typical* outcome — but a race is possible. The
lifecycle goroutine respects the *pool's* run-context (the errgroup's
`gctx`), not the caller's. Sessions whose Activate returned an error
to the caller still come up correctly under pyry's supervision.

This is not a bug — it's the inherent shape of the buffered-signal
lifecycle. Document it once in the source comment on `Create` and
move on. Tests should not depend on "Activate error → claude not
running" as a hard invariant; they should assert the registry shape
and the returned id, and let the lifecycle goroutine do whatever
it does.

## Error handling

| Failure point | Caller sees | On-disk state | In-memory state |
|---|---|---|---|
| `NewID` (rng) | wrapped err, empty id | unchanged | unchanged |
| `supervisor.New` | wrapped err, empty id | unchanged | unchanged |
| `saveLocked` | err verbatim, empty id | unchanged | rolled back (`delete(p.sessions, id)`) |
| `supervise` | `ErrPoolNotRunning`, valid id | entry persisted | entry in map; no lifecycle goroutine |
| `Pool.Activate` | err verbatim (often `ctx.Err`), valid id | entry persisted | entry in map; lifecycle goroutine running; lcState may race to active |

Wrapping conventions:

- `NewID` and `supervisor.New` errors: wrap with
  `fmt.Errorf("sessions: create %s: %w", op, err)`. Match the existing
  package's wrap style (see `New()`'s "sessions: bootstrap supervisor").
- `saveLocked` error: return verbatim. Caller distinguishes via the
  empty id (`""` means "no entry persisted").
- `supervise` and `Pool.Activate` errors: return verbatim. Caller
  uses `errors.Is(err, ErrPoolNotRunning)` to distinguish the
  "not-running" case from an Activate failure.

The id-or-empty rule is the discriminator: an empty `SessionID` means
"nothing persisted, nothing to clean up." A non-empty `SessionID`
means "entry is on disk, decide what to do (retry Activate, accept
the eventual lifecycle, leave it for next pyry start)."

## Testing strategy

All tests live in `internal/sessions/pool_test.go`. Use existing
helpers (`helperPool`, `helperPoolWithSleepArgs`) where applicable;
add minimal new helpers only if the existing ones don't compose.

### Required tests

| Test | What it asserts |
|---|---|
| `TestPool_Create_HappyPath` | `Create(ctx, "")` returns a valid UUID; `Lookup(id)` resolves to a `*Session`; the registry on disk contains both the bootstrap and the new entry; new entry has `bootstrap: false`, label empty, `lifecycle_state` absent (active default) or "active"; ChildPID > 0 after Activate completes (or after a short wait for the lifecycle goroutine to spawn). Requires `Pool.Run` running on a goroutine. |
| `TestPool_Create_LabelRoundTrip` | Two sub-tests: empty label → registry entry has `"label": ""`; non-empty label "alpha" → registry entry has `"label": "alpha"`. Read the JSON file directly and assert via `json.Unmarshal` into a struct, not via a string match. |
| `TestPool_Create_PersistFails_NoEntry_NoSpawn` | Inject persist failure by setting `pool.registryPath` to a path under a non-directory (e.g. `/dev/null/cant`). Call `Create`. Assert: returned id is empty, error non-nil, `Lookup` of any post-call snapshot does not contain the new id, no claude process spawned (assert `len(pool.Snapshot()) == 1` — bootstrap only). |
| `TestPool_Create_SuperviseFails_EntryOnDisk` | Construct a Pool, do NOT call `Run`. Call `Create`. Assert: returned id is non-empty, `errors.Is(err, ErrPoolNotRunning)` is true, the registry on disk contains the new entry, `Lookup(id)` resolves (in-memory entry exists), ChildPID for the new session is 0 (no claude). |
| `TestPool_Create_BootstrapUnchanged` | Capture `Default().ID()` before `Create`. Call `Create`. Assert: `Default()` returns the same `*Session` pointer (`==` comparison) and the same id. `Lookup("")` returns the same session. The bootstrap entry in the on-disk registry still has `bootstrap: true`. |
| `TestPool_Create_CapPassthrough_EvictsLRU` | Build pool with `ActiveCap: 1`, run on a goroutine, wait for bootstrap PID > 0. Call `Create(ctx, "")`. Assert: bootstrap transitions to `stateEvicted` (LRU victim — it's the only peer), new session transitions to `stateActive`, new session's ChildPID > 0, bootstrap's ChildPID is 0 after the transition settles. Use `eventually(t, ...)` polling for the lifecycle observations. |

### Notes on test helpers

- `helperPoolWithSleepArgs` is the right starting point for tests that
  call `Run`. The bootstrap spawns `/bin/sleep 3600`. `Create` will
  also need to spawn — the new session inherits `ClaudeBin: "/bin/sleep"`
  and `ClaudeArgs: ["3600"]` from `sessionTpl`, plus `--session-id <uuid>`
  appended. `/bin/sleep` ignores extra args, so this works.

- For tests that need bridge-mode behaviour (none in the required list,
  but `helperPool(t, true)` is available), the new session will get a
  fresh `supervisor.NewBridge(p.log)` per the design.

- For the persist-failure test, `pool.registryPath` is unexported; the
  test is in the same package (`internal/sessions`) so direct field
  access works. No new export needed.

- For the supervise-failure test, just don't call `Run` — `runGroup`
  stays nil, `supervise` returns the sentinel.

- For the cap test, the developer may need a small `eventually` helper
  if one isn't already in the test file (a polling loop with timeout).
  Existing pool tests use ad-hoc polling; either reuse or add a
  three-line helper.

### Verification

```
go test -race ./internal/sessions/...
go vet ./...
staticcheck ./...
```

CI runs all three. Local run before commit.

## Open questions

None. The ticket body and this spec resolve every interface choice.

The only judgment call deferred to the developer: whether to factor
out a `buildSessionStruct(...)` helper (the per-session struct
construction is ~15 lines and is similar to the bootstrap path in
`New()` lines 208-228, but the fields differ enough that DRYing them
isn't a clear win). Recommendation: leave them inline. Two ~15-line
constructions with different lifecycle-state defaults is clearer than
a helper with a `lcState lifecycleState` parameter.

## Out of scope (reminder)

Per the ticket body:

- The `Pool.supervise` seam itself (#72, hard prerequisite)
- Control protocol verb `sessions.new` (#71)
- `pyry sessions <verb>` CLI router (#71)
- `--name` flag parsing (#71)
- Phase 1.1b/c/d/e session verbs
- Discord channel-driven auto-mint (Phase 2.0)
- Any change to `Config.ActiveCap` defaults or `-pyry-active-cap` flag
  (deferred to Phase 2.0 per #41)

If the implementation drifts toward any of these, stop and re-scope.
