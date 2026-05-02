# Spec: Pool supervisor fan-out seam (#72)

## Context

`Pool.Run` (`internal/sessions/pool.go:314`) builds an `errgroup` locally,
fans out the bootstrap `Session` and the rotation watcher, and returns when
`g.Wait()` returns. The group is not stored on `*Pool`. Anything created
*after* `Run` has started — Phase 1.1's `Pool.Create(ctx, label)` (sibling
ticket) and any later code path that adds a session — has no way to join
that supervised set.

This ticket exposes the join seam — only the seam. No `Pool.Create`, no
UUID minting, no registry write, no `Activate` orchestration. The bootstrap
fan-out is refactored onto the new helper so the helper is exercised in
production from day one (rather than living dormant until Phase 1.1a-A2
lands).

## Design

### New surface

Two unexported fields on `*Pool`, one exported sentinel, one unexported
helper.

```go
// Pool fields (additions, declared adjacent to existing mu/sessions/...).
type Pool struct {
    // ... existing fields ...

    // runGroup and runCtx are set when Pool.Run begins and cleared
    // before Run returns. supervise(sess) reads them under p.mu (RLock)
    // and calls runGroup.Go off-lock so future Pool.Create can hand a
    // freshly-built Session into the same errgroup that supervises the
    // bootstrap. nil when Run is not active. Read together so a caller
    // never sees a half-initialised handle.
    runGroup *errgroup.Group
    runCtx   context.Context
}
```

```go
// ErrPoolNotRunning is returned by supervise when called before Pool.Run
// has wired the supervisor handle, or after Run has cleared it.
var ErrPoolNotRunning = errors.New("sessions: pool not running")
```

```go
// supervise schedules sess.Run on Pool.Run's live errgroup. Returns
// ErrPoolNotRunning when the handle is not set. Callers must invoke
// supervise only while Pool.Run is active; the sentinel turns the
// race-prone "supervise after shutdown" case into a clean error rather
// than a silent leak.
//
// Lock discipline: takes p.mu (RLock) briefly to snapshot runGroup +
// runCtx, then releases the lock before calling g.Go. Does not touch
// Session.lcMu. Preserves Pool.mu → Session.lcMu and
// Pool.capMu → Pool.mu → Session.lcMu lock orders.
func (p *Pool) supervise(sess *Session) error {
    p.mu.RLock()
    g, gctx := p.runGroup, p.runCtx
    p.mu.RUnlock()
    if g == nil {
        return ErrPoolNotRunning
    }
    g.Go(func() error { return sess.Run(gctx) })
    return nil
}
```

### Pool.Run wiring

`Pool.Run` is rewritten to:

1. Construct the errgroup with `errgroup.WithContext(ctx)`.
2. Take `p.mu` (write), set `p.runGroup = g` and `p.runCtx = gctx`,
   release.
3. Defer the symmetric clear: `p.mu` (write), zero both fields, release.
   The defer fires even if a goroutine on the group panics — by the time
   `Run` unwinds, the handle is reliably gone.
4. Resolve the bootstrap session once (existing snapshot of `bootstrap`
   and `dir` under `p.mu.RLock`).
5. Call `p.supervise(bootstrap)`. The sentinel cannot fire here — we
   just set the handle. Treat `nil` as the only expected return; a
   non-nil error is a programmer error and should propagate from `Run`.
6. The watcher fan-out (`g.Go(func() error { return w.Run(gctx) })`)
   stays as-is. The watcher is not a `*Session` and does not need to go
   through the helper.
7. `return g.Wait()` unchanged.

Sketch:

```go
func (p *Pool) Run(ctx context.Context) error {
    p.mu.RLock()
    bootstrap := p.sessions[p.bootstrap]
    dir := p.claudeSessionsDir
    p.mu.RUnlock()

    g, gctx := errgroup.WithContext(ctx)

    p.mu.Lock()
    p.runGroup, p.runCtx = g, gctx
    p.mu.Unlock()
    defer func() {
        p.mu.Lock()
        p.runGroup, p.runCtx = nil, nil
        p.mu.Unlock()
    }()

    if err := p.supervise(bootstrap); err != nil {
        return fmt.Errorf("sessions: supervise bootstrap: %w", err)
    }

    if dir != "" {
        // unchanged: rotation.New + g.Go(w.Run)
    }

    return g.Wait()
}
```

The `fmt.Errorf` on `supervise` is defensive — the only way it fires is
if a future refactor moves the handle-set after this call. It's cheap;
keep it.

### Why fields on Pool, not a separate guard struct

The natural alternative is a small `runHandle` struct (group + ctx) held
behind its own mutex. Two reasons not to:

- The handle's read site (`supervise`) already needs `p.mu` to be
  available — it's the lock that protects every other "what's in this
  pool right now" read. Splitting into a second mutex doubles the lock
  count without buying anything.
- `Pool.mu` is already an `RWMutex`. Reads of the handle (via
  `supervise`) take `RLock`; writes (Run setup/teardown) take `Lock`.
  Concurrent supervises don't contend with each other, only with Run's
  one-shot setup and one-shot teardown.

### Why RLock, not Lock, in supervise

The fields are written exactly twice per `Pool.Run` invocation: once at
setup, once in the deferred teardown. They are read once per `supervise`
call. RLock matches the access pattern — N concurrent supervise readers,
zero or one writer at a time, the writer always serialised against any
in-flight reader. This is the textbook RWMutex case.

`g.Go` itself is safe to call concurrently from multiple goroutines —
`errgroup.Group` is documented as concurrency-safe.

## Concurrency model

Three actors touch `runGroup` / `runCtx`:

1. **`Pool.Run`'s setup** — single goroutine, single write under
   `p.mu.Lock`. Happens once.
2. **`Pool.Run`'s teardown** — single goroutine, single write under
   `p.mu.Lock` (the deferred clear). Happens once.
3. **`supervise` callers** — N goroutines, each takes `p.mu.RLock`,
   reads both fields, releases, calls `g.Go` off-lock.

Race-free by construction:

- A supervise call that arrives before setup observes `runGroup == nil`
  and returns `ErrPoolNotRunning`.
- A supervise call that arrives after teardown observes `runGroup == nil`
  and returns `ErrPoolNotRunning`.
- A supervise call interleaved with `Run`'s body sees both fields set
  consistently — they are written together under one Lock and read
  together under one RLock.
- A supervise call racing with teardown either acquires RLock first
  (sees the handle, schedules onto a group whose ctx is about to be
  cancelled — sess.Run handles ctx.Done) or after (sees nil, returns
  the sentinel).

The "racing with teardown, scheduled onto a soon-cancelled group"
window is the messy case. It's safe: `errgroup.Group.Go` accepts
goroutines after Wait has been called by the same goroutine that owns
Wait — but Wait is single-threaded inside `Run`. The relevant guarantee
is from `errgroup`'s docs: scheduling onto a group whose context is
cancelled means the scheduled func runs and observes a cancelled ctx
immediately. `Session.Run` handles that — it's the existing shutdown
path.

We can sharpen this further (snapshot under lock, check `gctx.Err()`
before scheduling) but the underlying behaviour is already safe and
the extra check would race anyway. Keep the helper as drafted.

### Lock order

Unchanged from current state. The helper takes only `p.mu` (RLock);
the existing documented orders are:

- `Pool.mu → Session.lcMu`
- `Pool.capMu → Pool.mu → Session.lcMu`

`supervise` does not call into `Session.lcMu` (it does not call
`Session.Activate` or `Session.LifecycleState` — only schedules
`Session.Run`, which manages `lcMu` internally on its own goroutine).
No new lock-order edges introduced.

## Error handling

- `supervise(sess)` returns `ErrPoolNotRunning` when the handle is not
  set. Callers use `errors.Is(err, sessions.ErrPoolNotRunning)` to test.
- `supervise` does not propagate errors from `sess.Run` — those are
  handled by `errgroup.Wait` returning the first non-nil error from
  `Run`, identical to the bootstrap path today.
- The bootstrap-supervise call inside `Run` wraps the (impossible)
  sentinel as `fmt.Errorf("sessions: supervise bootstrap: %w", err)`.
  This is belt-and-suspenders — the precondition is established two
  lines above — but cheap insurance against a future reordering bug.
- Defer-on-return clears the handle even when a supervised goroutine
  panics, because errgroup propagates panics through `Wait` and the
  deferred cleanup runs as `Run` unwinds.

## Testing strategy

All tests live in `internal/sessions/pool_test.go`. Use the existing
`helperPoolWithSleepArgs` constructor where a real bootstrap is needed;
for sentinel-only tests, a bare `New(Config{...})` with a discarded
logger is enough.

| Test | What it asserts |
|---|---|
| `TestPool_Supervise_BeforeRun_ReturnsErrPoolNotRunning` | Construct a Pool, call `supervise(default)` before `Run`, assert `errors.Is(err, ErrPoolNotRunning)`. |
| `TestPool_Supervise_AfterRunReturns_ReturnsErrPoolNotRunning` | Start `Run` in a goroutine, wait until ChildPID > 0, cancel ctx, wait for `Run` to return, then call `supervise(default)`, assert sentinel. |
| `TestPool_Supervise_ConcurrentCalls_RaceClean` | Start `Run`, wait for ChildPID > 0, fire N (e.g. 32) goroutines each calling `supervise(default)`. All return nil; `go test -race` is the assertion. |

The existing `TestPool_Run_NoWatcherWhenDirEmpty` and
`TestPool_Run_StartsWatcher` continue to pass unchanged — they exercise
the bootstrap fan-out, which now flows through `supervise`. If they fail
after the refactor, the helper has changed semantics from the inline
`g.Go` call.

The concurrent-supervise test schedules N copies of the bootstrap
session onto the group. That's fine for a race-cleanliness assertion —
the bootstrap is already running once, scheduling its `Run` again would
re-enter its lifecycle goroutine, but the test cancels ctx before
asserting (so the extra goroutines see `ctx.Done` and return). A
cleaner alternative is to call supervise on a *different* dummy
`*Session` constructed for the test. Either is acceptable; the dummy-
session shape is closer to the real future call site (`Pool.Create`
hands in a freshly-built session) and avoids the "supervise the
bootstrap twice" oddity. Pick the dummy-session shape.

For the dummy-session shape, build a minimal `*Session` with
`/bin/sleep 60` so its `Run` actually does something testable (rather
than nil-deref'ing). The existing `helperPoolWithSleepArgs` shows the
shape for the bootstrap; adapt.

### Verification

- `go test -race ./internal/sessions/...`
- `go vet ./...`
- `staticcheck ./...`

## Open questions

None. The ticket body and this spec resolve every interface choice. The
only judgment call deferred to the developer is the dummy-session vs.
schedule-bootstrap-twice shape for the concurrency test — the spec
recommends dummy-session. Either is correct.

## Out of scope (reminder)

Per the ticket body:

- `Pool.Create(ctx, label)` public API
- UUID minting, registry persistence, `Pool.Activate` orchestration,
  `RegisterAllocatedUUID` plumbing, `--session-id <uuid>` baking
- Cap-aware behaviour for new sessions, persist-then-activate ordering
- Control verb / CLI router (#71)

If the implementation drifts toward any of these, stop and re-scope.
