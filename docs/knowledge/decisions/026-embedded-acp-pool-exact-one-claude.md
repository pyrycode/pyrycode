# ADR 026: Embedded ACP pool — suppress the eager bootstrap claude, gate the first `Create` on readiness

**Status:** Accepted (2026-07-03, ticket [#761](https://github.com/pyrycode/pyrycode/issues/761))
**Phase:** Epic [#600](https://github.com/pyrycode/pyrycode/issues/600) — `pyry acp` as a thin adapter over the shared remote-head core
**Refines:** [ADR 016](016-bootstrap-ignores-persisted-lifecycle-state.md) (bootstrap ignores persisted lifecycle state); sibling to [ADR 023](023-activate-waits-pty-readiness.md) (readiness-gate pattern)

## Context

`pyry acp` (from [#756](https://github.com/pyrycode/pyrycode/issues/756)) is a
JSON-RPC stdio transport that today drives no claude. [#761](https://github.com/pyrycode/pyrycode/issues/761)
delivers the first ACP method that does real work — `session/new` — plus the
composition root it needs: an embedded `internal/sessions` pool inside the
`pyry acp` subprocess, trimmed to the ACP subset (no relay, control socket,
conversations registry, or message queue).

ACP maps one ACP session onto exactly **one** running interactive `claude`
(**divergence 6**). `session/new` must allocate one pool session, start one
supervised interactive claude, and return a fresh id — **no more, no fewer**
claudes — and it must be the real interactive one billed under the interactive
subscription (**hard cost invariant**: never `claude -p`, never the metered
Agent SDK).

Two facts about the existing pool collide with that requirement (verified
against live code):

1. **The pool always eager-spawns a bootstrap claude.** `sessions.New` forces
   the bootstrap to `stateActive` — both the warm-start branch and the
   fresh-mint else-branch default to it, encoding the daemon-mode startup
   contract "claude is available" ([ADR 016](016-bootstrap-ignores-persisted-lifecycle-state.md)).
   `pool.Run` then unconditionally `supervise(bootstrap)`s it → `runActive` →
   an interactive claude. Minted sessions from `Pool.Create` start lazy
   (`stateEvicted`), but the bootstrap does not. So a naive standup that then
   `Pool.Create`s a session per `session/new` yields **two** interactive claudes
   for one ACP session — a divergence-6 violation ("no more, no fewer") *and* a
   hard-cost violation (a second interactive-billed claude running unaddressed).
   The collision is invisible at the LOC / reuse level; the ticket body framed
   the work as "reuse `Pool.Create`" without accounting for it.

2. **The first `Create` can race `pool.Run`'s startup, unrecoverably.**
   `Pool.Create` calls the unexported `supervise`, which returns
   `ErrPoolNotRunning` when `runGroup` is nil — i.e. before `Run` has wired the
   errgroup handle. On that path `Create` returns a **non-empty id** for a
   session that is minted (and would-be persisted) but **never supervised**.
   There is no public re-supervise, and `Activate` on such a session blocks
   forever (no lifecycle goroutine reads its `activateCh`). The session is
   permanently stuck. A microsecond window between backgrounding `pool.Run` and
   the first `session/new` is enough to strand the very first call.

A third fact shapes the standup but is not itself a decision point:
**foreground mode clobbers stdout.** `supervisor.runOnce` in foreground mode
(`Bridge == nil`) copies claude's PTY output to `os.Stdout` (supervisor.go:771-775),
which ACP owns for the JSON-RPC frame stream — so the ACP-hosted claude must run
in **service mode** (`Bridge` set), where output routes to the Bridge (discarded
when nothing attaches). This is settled by construction, not a fork.

## Decision

Add **two purely-additive `internal/sessions` primitives** and stand the ACP
pool up on them. Both preserve today's behaviour at their zero value and have a
single new consumer (the ACP composition root) — no call-site fan-out.

1. **`Config.BootstrapEvicted bool`** — when true, `New` constructs the
   bootstrap in `stateEvicted` (overriding the warm/cold-start `lcState` choice),
   so `pool.Run → supervise(bootstrap) → runEvicted` parks it and **spawns no
   claude**. The eager claude becomes solely `Pool.Create`'s job, so
   `session/new` produces exactly one.

2. **`Pool.Ready() <-chan struct{}`** — a channel (internal `readyCh`, created
   in `New`) closed **once** by `Run` under `sync.Once`, right after
   `runGroup`/`runCtx` are wired and `supervise(bootstrap)` returns nil — i.e.
   once `Create`'s `supervise` can no longer hit `ErrPoolNotRunning`. Before
   `Run` is ever called the channel is open (never ready).

The composition root (`cmd/pyry/acp.go`) backgrounds `pool.Run`, `select`s on
`Ready()` before serving, registers `session/new` (which calls
`pool.Create(ctx, "")` and returns `{"sessionId": "<uuid>"}`), and joins the
pool on EOF/signal. The pool is stood up with `BootstrapEvicted: true` and a
service-mode `Bootstrap.Bridge`.

## Rationale

### Why `BootstrapEvicted` over adopting `Pool.Default()`

The obvious alternative to suppressing the bootstrap is to **adopt** it: let
`session/new` return the pool's already-supervised bootstrap session
(`Pool.Default()`) instead of minting a new one, so there is only ever one
claude. Rejected because:

- **`Default()` returns a fixed id.** ACP's `session/new` contract mints a
  *fresh* id each call; adopting the bootstrap would return the same id every
  time. That directly contradicts the ticket's "fresh id each call" and pulls
  #762's *same-id no-double-spawn* concern into this ticket's scope.
- **It couples `session/new` to the bootstrap's identity.** A second
  `session/new` would need a real mint anyway (the bootstrap is already taken),
  so "adopt for the first, mint for the rest" is a special case that the
  additive-knob approach avoids entirely — every `session/new` is a plain
  `Pool.Create`.

`BootstrapEvicted` keeps `session/new` uniform (always `Pool.Create`, always a
fresh id) and confines the change to a three-line branch in `New` whose
zero-value is byte-identical to today.

### Why not restructure the pool to have no bootstrap at all

Removing the bootstrap concept for embedded hosts would ripple through
`Pool.Default()` / `Pool.Lookup("")` — the per-process invariant that a bootstrap
entry always exists. Keeping the bootstrap as a **dormant `stateEvicted`
placeholder** preserves that invariant (it still answers `Default()`/`Lookup("")`)
while spawning nothing. The pool shape is unchanged; only the bootstrap's
lifecycle state differs.

### Why the evicted bootstrap is safe here (and the #202 hang does not apply)

[ADR 016](016-bootstrap-ignores-persisted-lifecycle-state.md) records the #202
warm-start hang: a bootstrap that warm-starts `stateEvicted` from a persisted
registry, with no attach client to drive `Activate`, hangs. That risk does
**not** apply to the ACP pool because it uses `RegistryPath == ""` (nothing
persisted) and **never `Activate`s the bootstrap** — it stays a dormant
placeholder until `Run`'s ctx cancels. The documented constraint on
`BootstrapEvicted` is therefore: *do not pair it with a `RegistryPath` that
would persist "evicted" for the bootstrap and later wake it with no attach
client.* Embedded, non-persistent hosts satisfy this by construction.

### Why `Ready()` is a correctness gate, not a nicety

The failure it prevents is **unrecoverable, not merely racy** (see Context #2):
a first `session/new` that races `pool.Run` startup strands a minted-but-
unsupervised session with a non-empty id and no public recovery path. A retry or
poll would be a stochastic guard against a deterministic ordering hazard — the
wrong fabric. `Ready()` is a channel closed exactly once at the precise moment
`supervise` becomes safe, so the composition root's `select` on it is a
deterministic, race-free gate. This mirrors [ADR 023](023-activate-waits-pty-readiness.md)'s
`WaitForPTY`: strengthen the startup contract with a channel-backed readiness
signal rather than teach every caller to retry.

### Why service mode (`Bridge` set) is mandatory

Not a fork, but load-bearing: ACP owns `os.Stdout` for the JSON-RPC frame
stream, and foreground `supervisor.runOnce` copies claude's PTY output there.
Setting `Bootstrap.Bridge` puts every Created session in service mode, so
claude's `MirrorOutput` routes to a per-session Bridge (discarded with no
attacher/observer) and stdout stays clean. The Bridge is the supervisor's I/O
mediator, **not** one of the prohibited subsystems (relay/control/conversations/
queue).

## Consequences

- **`session/new` produces exactly one interactive claude** — the divergence-6
  and hard-cost invariants hold, asserted by counting **total** supervised
  claudes (a per-id check would pass vacuously) at both the pool layer and
  end-to-end.
- **The first `session/new` can never strand on `ErrPoolNotRunning`** — the
  composition root gates on `Ready()` before serving, so no frame is dispatched
  until `supervise` is safe.
- **The two primitives are reusable by any embedded pool host** with an inverted
  "zero claudes until asked" contract (future `pyry acp` siblings, other
  adapters). #762 (`session/load`) builds directly on this shape and owns the
  sibling same-id no-double-spawn concern.
- **`BootstrapEvicted` carries a documented soundness constraint** (never
  persist/Activate the bootstrap under it) — enforced by convention in the
  `Config` doc comment, not by code, because the only consumer (embedded ACP)
  satisfies it structurally. An evidence-based choice: no observed misuse, so no
  code-level guard.
- **The ACP pool stays non-`security-sensitive`** — the spawn reuses the daemon
  workdir and the shared `trustMark`; the handler ignores `cwd`/`mcpServers`, so
  no caller path reaches the spawn. Honouring a caller `cwd` re-opens the
  confinement surface and flips the label; deferred to #762+.

## Alternatives considered

- **Adopt `Pool.Default()` as the ACP session** — fixed id contradicts
  "fresh id each call" and pulls #762's same-id concern in. Rejected.
- **Restructure the pool to omit the bootstrap for embedded hosts** — ripples
  through the `Default()`/`Lookup("")` invariant across every initialiser.
  Rejected; a dormant evicted placeholder is minimal.
- **Retry/poll the first `Create` on `ErrPoolNotRunning`** — a stochastic guard
  against a deterministic, unrecoverable ordering hazard. Rejected in favour of
  the `Ready()` channel gate.
- **Run the ACP claude in foreground mode** — clobbers the JSON-RPC frame stream
  on stdout. Rejected; service-mode Bridge is mandatory.

## References

- Ticket: [#761](https://github.com/pyrycode/pyrycode/issues/761)
- Spec: [`docs/specs/architecture/761-acp-session-new-embedded-pool.md`](../../specs/architecture/761-acp-session-new-embedded-pool.md)
- Per-ticket record: [`codebase/761.md`](../codebase/761.md)
- Code: `internal/sessions/pool.go` (`Config.BootstrapEvicted`, `Pool.readyCh`/`readyOnce`, `Pool.Ready`, the `New` override, the `Run` close), `cmd/pyry/acp.go` (`runACP`, `serveACPWithPool`, `newSessionHandler`)
- Feature docs: [`features/sessions-package.md`](../features/sessions-package.md#configbootstrapevicted--poolready-761), [`features/acp-package.md`](../features/acp-package.md#sessionnew-and-the-embedded-pool-761)
- Related ADRs: [016](016-bootstrap-ignores-persisted-lifecycle-state.md) (bootstrap lifecycle state), [023](023-activate-waits-pty-readiness.md) (readiness-gate pattern)
- Epic: [#600](https://github.com/pyrycode/pyrycode/issues/600) `pyry acp`
</content>
