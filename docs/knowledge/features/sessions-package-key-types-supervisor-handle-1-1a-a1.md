# Supervisor handle (1.1a-A1)

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
