# `Config.BootstrapEvicted` + `Pool.Ready()` (#761)

`Config.BootstrapEvicted` suppresses eager bootstrap spawning for embedded pool
hosts; `Pool.Ready` gates callers that create or mint sessions during startup.
Both originated in the embedded `pyry acp` host (epic #600, removed in #1348).
The daemon now gates default-workspace seeding and control serving on readiness.
Design rationale: [ADR 026](../decisions/026-embedded-acp-pool-exact-one-claude.md).

**`Config.BootstrapEvicted bool`** — when true, `New` forces the bootstrap to
`stateEvicted` *after* the warm/cold-start `lcState` choice, so `Pool.Run →
supervise(bootstrap) → runEvicted` parks it and it **spawns no claude**. The
default (both warm-start and fresh-mint) forces `stateActive` — the daemon-mode
startup contract "claude is available" ([ADR 016](../decisions/016-bootstrap-ignores-persisted-lifecycle-state.md))
— which eager-spawns a bootstrap claude the moment `Pool.Run` starts. With
`BootstrapEvicted`, a single `Pool.Create` is the only interactive claude
(ACP's divergence 6 / hard-cost invariant).

```go
if cfg.BootstrapEvicted {
    lcState = stateEvicted        // → activeCh open, evictedCh closed (existing branch)
}
```

**Soundness constraint (documented on the field).** Sound only when the
bootstrap is **never Activated and never persisted evicted**: the pool keeps it
as a dormant `Default()`/`Lookup("")` placeholder until `Run`'s ctx cancels. Do
**not** pair it with a `RegistryPath` that would persist "evicted" for the
bootstrap and later warm-start it with no attach client to drive `Activate` —
that is the [#202 hang the warm-start branch guards against](../decisions/016-bootstrap-ignores-persisted-lifecycle-state.md).
Embedded, non-persistent hosts (`RegistryPath == ""`) satisfy this by
construction.

**`Pool.Ready() <-chan struct{}`** — a channel (internal `readyCh`, created in
`New`) closed **once** by `Run` under `sync.Once` (`readyOnce`), right after
`runGroup`/`runCtx` are wired and `supervise(bootstrap)` returns nil — i.e. once
`Pool.Create` can schedule supervision while `Run` is active. This signals a
wired supervisor handle and scheduled bootstrap lifecycle, without waiting for
the bootstrap child to reach a running state. Before `Run` is ever called the
channel is open (never ready). It stays closed after shutdown, so it is a startup
barrier, not a live-pool health check: `Run` clears the supervisor handle on
return. Read lock-free (set once in `New`, never reassigned; safe to `select` on
repeatedly and from multiple goroutines).

```go
func (p *Pool) Ready() <-chan struct{}   // closed once Create's supervise is safe
```

Why this is a **correctness gate, not a nicety**: `Create → supervise` returns
`ErrPoolNotRunning` when `runGroup` is nil, and on that path `Create` returns a
**non-empty id** for a session already minted in memory and persisted when
`RegistryPath` is configured, but **never supervised** — there is no public
re-supervise, and `Activate` on it blocks until its caller cancels. The session
is stuck for that pool lifetime. A
microsecond window between backgrounding `pool.Run` and the first `Create` is
enough to strand the first call. Hosts must gate startup `Create` and `Mint`
callers on `Ready()` with a cancellable wait, without delaying `Run` itself.
Same channel-backed readiness shape as
[ADR 023](../decisions/023-activate-waits-pty-readiness.md)'s `Supervisor.WaitForPTY`.

In `runSupervisor`, `seedWhenReady` gates default-workspace creation (#2569),
and `serveControlWhenReady` gates control requests (#2866). The latter preserves
`ctrl.Listen`'s early ownership claim and waits on the daemon context, then
serves under detached `controlCtx`; otherwise startup cancellation can hang or
shutdown can release ownership before delivery writers stop. See
[control lifecycle](control-plane.md#lifecycle) and the
[socket regression and wiring tests](control-plane-testing.md#startup-readiness-and-ownership).
