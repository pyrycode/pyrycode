# 003 — Session-Addressable Runtime via `internal/sessions` Pool

**Status:** Accepted (Phase 1.0a, ticket #28; consumer rewiring in #29)
**Date:** 2026-05-01

## Context

`pyry` shipped Phase 0 as a structurally one-claude supervisor: `cmd/pyry/main.go` constructed a single `*supervisor.Supervisor` and (in service mode) a single `*supervisor.Bridge`, and handed both to `internal/control` via a `StateProvider` + `AttachProvider` interface pair.

Phase 1.1 (CLI multi-session: `pyry sessions new`, `pyry attach <id>`) and 1.2 (persistence + idle eviction) need a session-addressable layer they can extend additively. Bolting an ID and registry onto `internal/supervisor` would either:

1. Pollute the supervisor with concerns it shouldn't own (identity, registry, lookup), or
2. Force every Phase 1.1 change to ripple through the supervisor's public surface.

The design constraint was **additive Phase 1.1**: adding `Request.SessionID`, wiring CLI subcommands, and supporting `pyry attach <id>` should not require touching `internal/supervisor` or rewiring the lifecycle.

## Decision

Introduce a new package `internal/sessions` that wraps `internal/supervisor` with identity (`SessionID`) and registry (`Pool`) semantics. The supervisor remains the workhorse and keeps its current public surface; sessions adds the layer above it.

Key shape:

- `Pool` owns the set of sessions (today: one bootstrap entry).
- `Session` wraps one `*supervisor.Supervisor` plus an optional `*supervisor.Bridge`.
- `Pool.Lookup(id SessionID)` resolves to a `*Session`. **Empty `SessionID` resolves to the default (bootstrap) entry.**
- `internal/control` consumes a single `SessionResolver` interface (Phase 1.0b/#29) instead of the `StateProvider` + `AttachProvider` pair.
- Dependency direction: `cmd/pyry → internal/sessions → internal/supervisor`. `internal/control` will (after 1.0b) import `internal/sessions`. The supervisor never imports upward.

The slice was split into two children of #27:

- **#28 (Phase 1.0a):** introduce the package + tests, no consumer rewiring. Self-contained, mergeable as a coherent unit.
- **#29 (Phase 1.0b):** mechanical follow-up that flips `internal/control` and `cmd/pyry/main.go` to consume the pool.

## Rationale

**Why a new package, not a method on `supervisor.Supervisor`.** Identity and registry are different concerns from PTY spawn / restart / I/O bridging. Keeping `internal/supervisor` focused on one child's lifecycle preserves the tested core. Phase 1.1's `Pool.Add(SessionConfig)` and idle eviction belong with the registry, not on the supervisor.

**Why "empty ID resolves to default."** It lets the future `req.SessionID` field be added to `Request` with no handler-side branching. Old clients send empty, handlers call `Lookup("")`, get the bootstrap entry — same behaviour as today. New clients send a real ID, handlers call the same `Lookup(...)`, get the right entry. No "if id == \"\" use default else look it up" scattered across verbs.

**Why split #27 into #28 + #29.** Introducing the package and its tests is a self-contained, low-risk change. Rewiring `internal/control`'s interfaces and `cmd/pyry/main.go`'s construction is a mechanical follow-up that's easier to review when the new types already exist and have their own tests. The slice keeps each PR focused and reviewable.

**Why `Pool.Run` calls `bootstrap.Run(ctx)` directly (no errgroup).** Errgroup is the structural extension point for Phase 1.1's multi-session fan-out, but introducing it now means adding concurrency machinery we cannot exercise (only one session exists, so no fan-out behaviour to test). The locked design says "add it with the first multi-session test in 1.1" — this slice obeys.

**Why `sync.RWMutex` despite no writers in 1.0.** The lock structure documents the contract: `p.sessions` is shared, all reads take the read lock. Phase 1.1's `Pool.Add` takes the write lock without changing `Run`. Cheap discipline now beats discovering "but here we don't lock" exceptions later.

**Why duplicate `TestHelperProcess` was rejected (and replaced with `/bin/sleep`).** The supervisor's `Config.helperEnv` is unexported, blocking external packages from re-using the re-exec pattern cleanly. The three options were: (1) export `helperEnv` (expands public surface for tests-only), (2) `t.Setenv` in the sessions tests (pollutes env, breaks `t.Parallel`), or (3) use `/bin/sleep` as a real benign binary. Option 3 won: zero new test infrastructure, the supervisor's surface stays unchanged, and the only contract the test needs to assert is ctx-cancel delegation, which `/bin/sleep` exercises faithfully.

## Alternatives Considered

- **Add `ID()`, `Attach()`, registry methods to `Supervisor`.** Rejected: conflates lifecycle with identity, blocks Phase 1.2 idle eviction without further refactoring.
- **Package name `runtime`.** Rejected: shadows stdlib `runtime`, forces import aliases.
- **Single-PR rewrite of #27.** Rejected: larger diff, harder to review, conflates package introduction (low-risk) with consumer rewiring (touches the wire).
- **Phase 1.1 invocation (`claude --session-id <uuid>`) in this slice.** Rejected: out of scope for 1.0; the locked-design open question said "wait." Lands when CLI multi-session lands.

## Consequences

**Positive**

- Phase 1.1 plugs in at `Pool.Lookup` and `SessionConfig`, with no churn in `internal/supervisor`.
- Sentinel errors (`ErrSessionNotFound`, `ErrAttachUnavailable`) give callers the matchers they need.
- `internal/control`'s interface surface shrinks from two providers to one resolver (after 1.0b).
- `Session.Run`/`Pool.Run` keep the same shape for 1.1's errgroup conversion.

**Negative / costs**

- An extra layer of indirection between `cmd/pyry` and `supervisor` (one more package boundary, one more constructor).
- `Session.log` is dead in 1.0 — written, never read. Justified by the spec to avoid reshaping the struct in 1.1.
- The package ships unused-by-production for one merge cycle (between #28 and #29). Acceptable: the test binary imports it, and #29 is queued in the same feature branch.

**Forward-looking**

- `Pool.Add(SessionConfig) (*Session, error)` (write-locked map insert) — Phase 1.1.
- `errgroup` fan-out in `Pool.Run` — Phase 1.1, with the first multi-session test.
- Per-session log lines, including bootstrap ID logging — Phase 1.1+.
- `Request.SessionID` on the wire — Phase 1.1.
- `claude --session-id <uuid>` invocation — Phase 1.1+.

## References

- Parent ticket: [#27](https://github.com/pyrycode/pyrycode/issues/27)
- This slice: [#28](https://github.com/pyrycode/pyrycode/issues/28)
- Sibling: [#29](https://github.com/pyrycode/pyrycode/issues/29)
- Specs: [27-session-addressable-runtime.md](../../specs/architecture/27-session-addressable-runtime.md), [28-sessions-package.md](../../specs/architecture/28-sessions-package.md)
- Feature doc: [sessions-package.md](../features/sessions-package.md)
- Locked phase design: [`docs/multi-session.md`](../../multi-session.md), [`docs/plan.md`](../../plan.md)
