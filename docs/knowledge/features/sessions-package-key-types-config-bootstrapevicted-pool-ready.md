# `Config.BootstrapEvicted` + `Pool.Ready()` (#761)

Two purely-additive primitives for **embedded pool hosts** that map each caller
session onto its own `Pool.Create`'d claude and must run no eager, unaddressed
bootstrap claude. The sole consumer is `pyry acp`'s composition root (epic #600);
both preserve today's behaviour at their zero value with no call-site fan-out.
Design rationale: [ADR 026](../decisions/026-embedded-acp-pool-exact-one-claude.md).

**`Config.BootstrapEvicted bool`** — when true, `New` forces the bootstrap to
`stateEvicted` *after* the warm/cold-start `lcState` choice, so `Pool.Run →
supervise(bootstrap) → runEvicted` parks it and it **spawns no claude**. The
default (both warm-start and fresh-mint) forces `stateActive` — the daemon-mode
startup contract "claude is available" ([ADR 016](016-bootstrap-ignores-persisted-lifecycle-state.md))
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
that is the [#202 hang the warm-start branch guards against](016-bootstrap-ignores-persisted-lifecycle-state.md).
Embedded, non-persistent hosts (`RegistryPath == ""`) satisfy this by
construction.

**`Pool.Ready() <-chan struct{}`** — a channel (internal `readyCh`, created in
`New`) closed **once** by `Run` under `sync.Once` (`readyOnce`), right after
`runGroup`/`runCtx` are wired and `supervise(bootstrap)` returns nil — i.e. once
`Pool.Create`'s `supervise` can no longer return `ErrPoolNotRunning`. Before
`Run` is ever called the channel is open (never ready). Read lock-free (set once
in `New`, never reassigned; safe to `select` on repeatedly and from multiple
goroutines).

```go
func (p *Pool) Ready() <-chan struct{}   // closed once Create's supervise is safe
```

Why this is a **correctness gate, not a nicety**: `Create → supervise` returns
`ErrPoolNotRunning` when `runGroup` is nil, and on that path `Create` returns a
**non-empty id** for a session that is minted (would-be persisted) but **never
supervised** — there is no public re-supervise, and `Activate` on it blocks
forever. The session is permanently stuck; the failure is unrecoverable. A
microsecond window between backgrounding `pool.Run` and the first `Create` is
enough to strand the first call. So an embedded host must background `Run`, then
`select` on `Ready()` before issuing any `Create`. Same channel-backed
readiness shape as [ADR 023](023-activate-waits-pty-readiness.md)'s
`Supervisor.WaitForPTY`.
