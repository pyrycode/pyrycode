# Knowledge Base Index

Evergreen documentation for Pyrycode. Updated as things change, not appended to.

Search with QMD: `mcp__qmd__query(collection: "pyrycode-docs", query: "your query")`

## Architecture

| File | Topic |
|------|-------|
| [system-overview.md](architecture/system-overview.md) | Module structure, data flows, platform support |

## Decisions

| # | File | Decision |
|---|------|----------|
| 001 | [001-go-language.md](decisions/001-go-language.md) | Go as the implementation language |
| 002 | [002-pty-supervisor.md](decisions/002-pty-supervisor.md) | PTY-level wrapping over alternatives |
| 003 | [003-session-addressable-runtime.md](decisions/003-session-addressable-runtime.md) | `internal/sessions` Pool wraps the supervisor for additive Phase 1.1+ |
| 004 | [004-fsnotify-for-rotation-detection.md](decisions/004-fsnotify-for-rotation-detection.md) | `fsnotify` for live `/clear` detection (over polling or raw inotify+kqueue) |
| 005 | [005-idle-eviction-state-machine.md](decisions/005-idle-eviction-state-machine.md) | Per-session two-state machine + explicit `Activate` for idle eviction / lazy respawn |
| 006 | [006-concurrent-active-cap-lru.md](decisions/006-concurrent-active-cap-lru.md) | `Config.ActiveCap` + LRU victim selection at `Pool.Activate`; force-eviction `Session.Evict` primitive |
| 007 | [007-bridge-iteration-boundaries.md](decisions/007-bridge-iteration-boundaries.md) | `Bridge` input path moves from `io.Pipe` to `chan []byte` + per-iteration cancel (`BeginIteration` / `EndIteration`) so the input pump terminates per `runOnce` iteration instead of leaking and racing the next one for typed bytes during a restart |

## Features

| File | Topic |
|------|-------|
| [sessions-package.md](features/sessions-package.md) | `internal/sessions` — `SessionID`, `Session`, `Pool`; supervisor wrapper for multi-session readiness |
| [sessions-registry.md](features/sessions-registry.md) | `~/.pyry/<name>/sessions.json` — schema, atomic write, load semantics; sessions survive `pyry stop` |
| [jsonl-reconciliation.md](features/jsonl-reconciliation.md) | Startup scan of `~/.claude/projects/<encoded-cwd>/<uuid>.jsonl`; `Pool.RotateID` self-heals registry across `/clear` |
| [rotation-watcher.md](features/rotation-watcher.md) | Live `/clear` detection: fsnotify on the claude dir + per-PID FD probe (Linux `/proc/<pid>/fd`, macOS `lsof`) drives `Pool.RotateID` |
| [control-plane.md](features/control-plane.md) | `internal/control` — Unix-socket JSON server, `SessionResolver` seam, verb dispatch, attach handoff |
| [idle-eviction.md](features/idle-eviction.md) | Per-session active↔evicted state machine; idle timer + concurrent-active-cap (LRU) eviction triggers; `Activate` / `Evict` primitives |
| [e2e-harness.md](features/e2e-harness.md) | `internal/e2e` (build tag `e2e` or `e2e_install`) — `Harness` + `Start(t)` / `StartIn(t, home, flags...)` spawn pyry in temp / caller-supplied HOME with optional variadic flags (last-wins), `StartExpectingFailureIn(t, home)` for failed-start assertions, `Harness.Run(t, verb, args...)` auto-injects socket, `RunBare(t, args...)` drives daemon-free verbs, `Harness.Stop(t)` graceful mid-test teardown (idempotent with `t.Cleanup`); sibling `AttachHarness` + `StartAttach(t, sessionID)` (#125) wraps `pyry attach` in a `creack/pty` master/slave pair with `TestHelperProcess`-echo as supervised claude for interactive PTY round-trip tests; sibling `StartRotation(t, home, sessionsDir, initialUUID, trigger)` (#123) wires the fake-claude binary as the supervised child via `spawnWith(spawnOpts)` core + three `PYRY_FAKE_CLAUDE_*` env vars for rotation-watcher e2e; `AttachHarness.WaitDetach` + `AttachHarness.Run` (#127, sharing `runVerb` with `Harness.Run`) drive the documented `Ctrl-B d` detach + post-detach `pyry status` for the triple-invariant clean-detach proof; covers `stop`/`logs`/`version`/`status` (running + stopped), three restart-survival proofs (active, evicted-state, `lastActiveAt`), corrupt-registry fail-loud, missing-`~/.claude/projects/` clean-startup, idle-eviction + lazy-respawn (raw `VerbAttach` over the control socket), attach round-trip bytes, attach clean-detach, and attach-survives-claude-restart (the last surfaced + fixed the bridge input-pump leak — see [ADR 007](decisions/007-bridge-iteration-boundaries.md)) at the binary boundary; SIGTERM/SIGKILL teardown, leak verification via re-exec |
| [fakeclaude-binary.md](features/fakeclaude-binary.md) | `internal/e2e/internal/fakeclaude` — test-only `package main` that opens a `<uuid>.jsonl` and rotates it on a triggerable signal (rotation-watcher e2e primitive) |
| [install-e2e.md](features/install-e2e.md) | `internal/e2e/install_{linux,darwin}_test.go` (build tag `e2e_install`) — `pyry install-service` × `systemctl --user` (Linux) / `launchctl bootstrap gui/<uid>` (macOS) round-trip + bug-#19 PATH inheritance regression guard + cleanup-on-fatal re-exec verification on both platforms |
