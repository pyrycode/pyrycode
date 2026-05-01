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
- **Persistence as a Config field, empty disables** — `Config.RegistryPath` (or any "where to persist" string) defaulting to empty = no I/O lets unit tests construct a Pool with `t.TempDir()` or with persistence off entirely, with no test-only branches inside `New`. Production callers always pass a real path; the empty case is genuinely test-only.

## Open Questions

- **Backoff cooldown/bail-out** — if crashes happen N times in T seconds, should the supervisor give up? Currently retries forever, which is the right default for a service supervisor (a supervised child that never starts is the operator's problem to investigate, not for pyry to give up on).
- **Phase 0.5 — Real production test** — supervisor hasn't been tested with a real `claude` child on pyrybox running as a launchd/systemd service. The tmux setup is still running. This is the only Phase 0 item left after PRs #1-#10.

(Earlier "Session ID tracking" and "Control socket design" questions were resolved by the PR series that landed Phase 0.2–0.4: `--continue` for session continuity, line-delimited JSON over a Unix socket for control.)
