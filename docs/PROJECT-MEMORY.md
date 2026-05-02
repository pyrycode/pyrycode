# Project Memory — Pyrycode

Repo-level session memory. Read this at the start of every session.

## What's Built

### Codebase (Phase 0)
- **Supervisor core** — PTY spawn via `creack/pty`, raw-mode stdin/stdout bridging in foreground mode, Bridge-mediated I/O in service mode, exponential backoff restart with stability reset, `--continue` injection on restart for session persistence
- **SIGWINCH forwarding** — terminal resizes propagate from controlling terminal to child PTY (foreground mode only; attach mode locks the size at attach time)
- **Control plane** — Unix domain socket (`~/.pyry/<name>.sock`, 0600), line-delimited JSON protocol, verbs: `status`, `stop`, `logs`, `attach`
- **CLI transparency** — unknown args forward verbatim to claude; pyry's own flags use `-pyry-*` prefix; `-pyry-name` plus `PYRY_NAME` env var for named multi-instance
- **Graceful shutdown** — SIGINT/SIGTERM cancel the supervisor context, child is killed via `exec.CommandContext`, socket removed on exit
- **Service configs** — systemd user unit (`systemd/pyry.service`), macOS launchd plist (`launchd/dev.pyrycode.pyry.plist`)
- **~1700 source + ~1100 test Go lines** as of late Apr 2026, 10+ PRs merged

### Codebase (Phase 1.0, tickets #28 + #29)
- **`internal/sessions` package** — `SessionID` (UUIDv4 via `crypto/rand`, stdlib only), `Session` (wraps one `*supervisor.Supervisor` + optional `*supervisor.Bridge`), `Pool` (single-bootstrap registry with `RWMutex`-protected map). Sentinel errors `ErrSessionNotFound`, `ErrAttachUnavailable`. `Pool.Lookup("")` resolves to the bootstrap entry — the seam Phase 1.1's `Request.SessionID` plugs into.
- **Production consumers wired (#29)** — `cmd/pyry/main.go` constructs `*sessions.Pool` (with the supervisor.Config template inside `SessionConfig`); `internal/control` consumes a single `SessionResolver` interface (replaces Phase 0's `StateProvider` + `AttachProvider` pair). A 5-line `poolResolver` adapter in `cmd/pyry` bridges `Pool` → `SessionResolver` (covariant-return workaround). Wire protocol unchanged; `pyry status`/`stop`/`logs`/`attach` byte-identical to Phase 0. Foreground-mode attach error string preserved verbatim via `errors.Is(err, sessions.ErrAttachUnavailable)` mapping in `handleAttach`.
- See [knowledge/features/sessions-package.md](knowledge/features/sessions-package.md), [knowledge/features/control-plane.md](knowledge/features/control-plane.md), and [ADR 003](knowledge/decisions/003-session-addressable-runtime.md).

### Codebase (Phase 1.2c-A, ticket #40)
- **Idle eviction + lazy respawn** — per-`*Session` lifecycle goroutine drives a two-state machine (`active` ↔ `evicted`). When `attached == 0` and the idle timer fires, the supervisor's inner ctx is cancelled and claude exits cleanly; the JSONL on disk is preserved. `Session.Activate(ctx)` respawns the supervisor pointing at the same JSONL on disk so the conversation continues. `Session.Run` rewritten from one-line supervisor delegate into a `runActive` / `runEvicted` loop; `Pool.Run` shape **unchanged** (multi-session fan-out is Phase 1.1's job).
- **Activity = attach state** — while `attached > 0` the timer re-arms on fire (poll-with-grace; eviction may overshoot the configured timeout by up to one window). `last_active_at` is bumped on every state transition for the upcoming LRU follow-up's victim selection.
- **`-pyry-idle-timeout` flag** — production default `15m`, `0` disables eviction entirely (test default + operator escape hatch). Wired through `Config.IdleTimeout` → `SessionConfig.IdleTimeout` (per-session override falls back to pool default).
- **Activate-before-Attach contract** — `bridge.Attach` on an evicted session would block on the pipe forever. `internal/control.Session` interface gained `Activate(ctx)`; `handleAttach` calls it with a 30s timeout cap before binding the bridge. `handleStatus` does **not** activate (faithful `PhaseStopped` for evicted; no spurious wakeups from a poll). Wire response on respawn failure: `attach: activate: <err>`.
- **Registry `lifecycle_state` field** — `"active"` / `"evicted"`, `omitempty` so the dominant active case stays off disk (preserves #34's idempotent-reload byte-stability). Old pyry binaries ignore the field; new pyry reading old files defaults to `"active"`. Bootstrap warm-starts in whatever state the registry says — including starting in `evicted` if pyry was stopped while evicted.
- **Lock order: `Pool.mu → Session.lcMu`** — `Session.lcMu` protects `lcState` / `attached` / `activeCh` / `lastActiveAt` (when read for the registry snapshot). `Session.transitionTo` releases `lcMu` before calling `Pool.persist` so `saveLocked`'s per-session re-acquire doesn't deadlock. `RotateID` mutates `session.id` without `lcMu` — documented invariant (today's only callers run before any lifecycle goroutine observes the id).
- **Concurrency primitives** — `activeCh` (closed-on-active broadcast) + `activateCh` (buffered 1 signal). `Activate` snapshots `activeCh` under `lcMu` *before* waiting so a concurrent evict-replace doesn't drop the wakeup. Concurrent `Activate`s collapse to one signal via the buffered send.
- See [knowledge/features/idle-eviction.md](knowledge/features/idle-eviction.md), [ADR 005](knowledge/decisions/005-idle-eviction-state-machine.md).

### Codebase (Phase 1.2b-B, ticket #39)
- **Live `/clear` rotation watcher** — `internal/sessions/rotation` package: fsnotify on `cfg.ClaudeSessionsDir` + per-PID FD probe (Linux walks `/proc/<pid>/fd`, macOS shells out to `lsof -nP -p <pid> -F fn`). On a CREATE for `<new>.jsonl` whose UUID a tracked PID has open, calls `Pool.RotateID(old, new)` — same seam #38 established. Detection latency ~250ms worst-case (probeWithRetry walks 0/50/200ms attempts to handle CREATE-before-open races).
- **`Pool.Run` errgroup wrap** — `golang.org/x/sync/errgroup` ties the bootstrap supervisor and the watcher; cancellation propagates both ways. Phase 1.1's N-session fan-out reuses the same wrapper — the extension point is one `g.Go` per pool entry.
- **Pyry-allocated UUID skip set on `Pool`** — `RegisterAllocatedUUID` / `IsAllocated`. Consume-on-first-hit, 30s TTL pruned opportunistically, all under `p.mu` (write). No live caller in 1.2b-B (pyry still uses `--continue`); scaffolding lands now so Phase 1.1's `claude --session-id` is a one-liner.
- **`rotation.Config` closures over primitives** — the rotation package never imports `internal/sessions`. The `SessionID ↔ string` conversion happens once, inside `Pool.Run`'s closure wiring. `Pool.Snapshot` returns a small `SnapshotEntry` struct; `snapshotForRotation` translates to `rotation.SessionRef`.
- **Failure posture** — rotation detection is never load-bearing. fsnotify init failure, missing `lsof`, missing `/proc`, save failure during `OnRotate` — all logged warn/debug; pyry continues. `MkdirAll(dir, 0700)` for a not-yet-existing claude dir.
- **First new external runtime dep since `creack/pty`** — `github.com/fsnotify/fsnotify` v1.10.0, justified in [ADR 004](knowledge/decisions/004-fsnotify-for-rotation-detection.md). `golang.org/x/sync` lands as a sibling for errgroup.
- See [knowledge/features/rotation-watcher.md](knowledge/features/rotation-watcher.md), [ADR 004](knowledge/decisions/004-fsnotify-for-rotation-detection.md).

### Codebase (Phase 1.2b-A, ticket #38)
- **Startup JSONL reconciliation** — `Pool.New` scans `~/.claude/projects/<encoded-cwd>/<uuid>.jsonl`, picks the most-recently-modified one, and rotates the registry's bootstrap entry to that UUID if it disagrees. Self-heals after `/clear` (claude rotates session UUIDs on `/clear`; without reconciliation, post-`pyry stop` the registry would still point at the pre-`/clear` UUID).
- **`Pool.RotateID(oldID, newID)`** — atomic swap of in-memory map key + bootstrap pointer + `saveLocked`, all under `p.mu` (write). `RotateID(x, x)` is a no-op; unknown old id returns `ErrSessionNotFound`. Bumps `last_active_at`; preserves `created_at`. The load-bearing seam reused by Phase 1.2b-B (live-detection via fsnotify).
- **`encodeWorkdir`** — claude maps both `/` AND `.` to `-` in its `~/.claude/projects/` path component (verified empirically 2026-05-02). `/foo/.bar` → `-foo--bar` (note the doubled dash). Unexported; encoding lives inside `internal/sessions` and `cmd/pyry` only sees the resolved path via `DefaultClaudeSessionsDir`.
- **JSONL location** — files live directly in `<encoded-cwd>/<uuid>.jsonl`. There is **no** `sessions/` subdirectory (the ticket body claimed otherwise; the spec followed reality).
- **Failure posture** — missing/unreadable claude dir is logged warn and ignored (startup proceeds). Save failure during rotation is fatal-at-startup (matches 1.2a cold-start). The pre-rotation JSONL is never deleted or modified — only the registry pointer moves.
- **Expected one-time rotation in 1.2a** — on the second `pyry` restart, claude's UUID ≠ pyry's mint, so reconcile fires once and logs an Info line. Becomes rare after Phase 1.1+ wires `claude --session-id <uuid>`.
- See [knowledge/features/jsonl-reconciliation.md](knowledge/features/jsonl-reconciliation.md).

### Codebase (Phase 1.2a, ticket #34)
- **Session registry on disk** — `~/.pyry/<sanitized-name>/sessions.json` (file 0600, dir 0700), sibling to the per-name socket. Schema: `version` (forward-marker), `sessions[]` with `id` / `label` / `created_at` / `last_active_at` / `bootstrap`. Default `encoding/json` decoder tolerates unknown fields for forward compat.
- **`Pool.New` load-or-mint** — Cold start (missing or empty file) mints a fresh UUID and writes the registry before returning. Warm start reads the bootstrap-marked entry, reuses its UUID + metadata, and does **not** rewrite the file (warm reload is not a state change). Malformed JSON is fatal at startup — operator must fix or remove.
- **Atomic write seam** — `saveRegistryLocked` does `os.CreateTemp` → `Chmod 0600` → encode → fsync → close → `os.Rename`. Rename is the commit point; partial JSON is unreachable in the target. `defer os.Remove(tmp)` cleans up orphaned temps best-effort. `Pool.saveLocked` is the package-internal hook Phase 1.1's `Pool.Add` / `Rename` / `Remove` will call before returning success — caller holds `Pool.mu` (write) across the disk I/O.
- **Bootstrap marker on disk** — `bootstrap: true` is persisted explicitly so `Pool.Lookup("")` doesn't depend on file ordering. Phase 1.1's `pyry sessions rm <bootstrap-uuid>` thus has a clean question to answer (refuse, or promote another entry) instead of relying on array position. `omitempty` keeps non-bootstrap entries clean on disk.
- **Stable disk ordering** — `sortEntriesByCreatedAt` (then by `ID`) before write makes the file's byte content a deterministic function of the in-memory set, defeating Go's randomized map iteration and giving the AC's idempotent-reload guarantee. Degenerate for 1.2a's single entry; the property is paid for upfront ahead of 1.1.
- See [knowledge/features/sessions-registry.md](knowledge/features/sessions-registry.md).

### Documentation
- README, plan.md (phase roadmap), CLAUDE.md, CODING-STYLE.md
- Knowledge base: system-overview, 2 ADRs (Go language, PTY supervisor)
- This file, lessons.md

### Infrastructure
- GitHub Actions CI: go vet, staticcheck, go test -race
- QMD search: pyrycode-docs, pyrycode-root collections
- .claude/settings.json with safety rules

## Patterns Established

- **Config struct pattern** — all configuration in a single `Config` struct, defaults applied in `New()`
- **Context-based cancellation** — `context.Context` flows through `Run()` → `runOnce()`, checked at every wait point
- **Structured logging** — `log/slog` with injected logger, not a global
- **Exponential backoff with stability reset** — backoff doubles on restart, resets to initial if child stayed up longer than `BackoffReset`
- **Deferred cleanup** — `defer` for terminal restore, PTY close, signal stop
- **Empty ID resolves to default** — `Pool.Lookup("")` returns the bootstrap session, so future `req.SessionID` fields can be added with no handler-side branching (old clients send empty, get the bootstrap; new clients send a real ID, get the right entry)
- **Introduce-then-rewire slicing** — split #27 into #28 (new package + tests, no consumers) and #29 (mechanical consumer rewiring) to keep each PR focused
- **Consumer-side interface definition** — `internal/control` defines the interfaces it consumes (`SessionResolver`, `Session`) rather than importing them from the producer package. Keeps `internal/sessions` free of control-plane concerns and lets tests fake the surface without exporting test seams from the producer.
- **Wire-string preservation via `errors.Is` mapping** — when refactoring an error path that crosses package boundaries, map the new sentinel back to the old wire string explicitly (`if errors.Is(err, sessions.ErrAttachUnavailable) { … Phase 0 string … }`) rather than letting `fmt.Sprintf("%v", err)` change client output. Required when an AC says "byte-identical."
- **Atomic on-disk writes via temp + rename** — for operator-recoverable JSON state files, `os.CreateTemp` in the same dir → encode → `f.Sync()` → `f.Close()` → `os.Rename(tmp, path)` makes the rename the commit point and partial files unreachable. `defer os.Remove(tmp)` cleans up orphans best-effort (no-op after successful rename). Don't bother with directory fsync unless real-world corruption is observed — pyry's registry is recoverable, not a database.
- **Stable disk ordering for idempotent reload** — sort serialized collections by a stable key (e.g. `created_at` then `id`) before writing. Defeats Go's randomized map iteration so save→load→save round-trips are byte-stable, and makes load-twice-equals-load-once into a real property rather than a probabilistic one.
- **Persistence as a Config field, empty disables** — `Config.RegistryPath` (or any "where to persist" string) defaulting to empty = no I/O lets unit tests construct a Pool with `t.TempDir()` or with persistence off entirely, with no test-only branches inside `New`. Production callers always pass a real path; the empty case is genuinely test-only. Same shape reused for `Config.ClaudeSessionsDir` in 1.2b-A.
- **One mutation seam, multiple drivers** — `Pool.RotateID` is the single place that swaps a session's in-memory id and persists. Startup reconciliation drives it from a directory scan; the live-detection watcher drives it from an fsnotify event. Defining the seam once with the lock-and-save contract baked in meant the second driver landed as a goroutine that calls a function, not a redesign.
- **Closures over primitives across package boundaries** — when a downstream package (`internal/sessions/rotation`) needs to call back into its host (`internal/sessions`), express the contract as a struct of closures over primitive types (`string`, `int`) rather than an interface that references the host's domain types (`SessionID`). The downstream package then has zero knowledge of the host, and the type-safety conversion (`SessionID ↔ string`) happens exactly once, in the wiring site. Avoids the import cycle the obvious "watcher takes a `*Pool`" design would create.
- **Test seam via package var, not interface injection** — `var newProbe = rotation.DefaultProbe` in `internal/sessions/pool.go`. Tests overwrite to inject a fake; production callers see no extra plumbing. Same shape as `allocatedTTL` and `probeRetryDelays` (test-tunable knobs that aren't config). Cheaper than threading a factory through `Config` for a single test surface.
- **Errgroup as the fan-out seam** — `Pool.Run`'s `errgroup.WithContext` ties the bootstrap supervisor and the rotation watcher; Phase 1.1's N-session fan-out adds one `g.Go(func() error { return sess.Run(gctx) })` per session and reuses the same cancellation+error semantics. One wrapper, two phases of payoff.
- **Two-state machine with explicit-verb wake-up** — when a runtime entity needs an `idle ↔ live` lifecycle, prefer (a) a per-entity goroutine that owns both the state machine and the underlying resource lifecycle, (b) an explicit `Activate(ctx)` call rather than implicit wake on resolve/lookup, and (c) a closed-on-state broadcast channel + buffered(1) signal channel for the wake-up handshake. Implicit wake-on-lookup tangles read-side concerns (status reads paying respawn latency); explicit Activate keeps every caller's contract sharp. Used for `Session.Activate` (1.2c-A); will be reused by future per-session lifecycles (router, bind/unbind).
- **Lock order `Pool.mu → Session.lcMu` with release-before-callback** — when a callee needs to call back into the host's lock-protected persistence (`Pool.persist` from `Session.transitionTo`), release the inner lock *before* the callback so the host's re-acquire of the inner lock inside its serialization path doesn't deadlock. Document the order at every site that takes both. Same shape would apply to any "child mutates state then asks parent to persist" coupling.

## Open Questions

- **Backoff cooldown/bail-out** — if crashes happen N times in T seconds, should the supervisor give up? Currently retries forever, which is the right default for a service supervisor (a supervised child that never starts is the operator's problem to investigate, not for pyry to give up on).
- **Phase 0.5 — Real production test** — supervisor hasn't been tested with a real `claude` child on pyrybox running as a launchd/systemd service. The tmux setup is still running. This is the only Phase 0 item left after PRs #1-#10.

(Earlier "Session ID tracking" and "Control socket design" questions were resolved by the PR series that landed Phase 0.2–0.4: `--continue` for session continuity, line-delimited JSON over a Unix socket for control.)
