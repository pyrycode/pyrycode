# System Overview

Pyrycode is a process supervisor that keeps a Claude Code session alive across crashes and reboots. Phase 0 is a single-session supervisor; later phases add multi-session routing, Channels integration, and remote access.

## Module Structure

```
pyrycode/
├── cmd/pyry/                  Binary entry point
│   └── main.go                CLI parsing, signal setup, supervisor init
├── internal/supervisor/       Core process supervision
│   ├── supervisor.go          Supervisor type: PTY spawn, I/O bridge, restart loop
│   ├── backoff.go             Backoff timer: exponential delay with stability reset
│   └── winsize.go             SIGWINCH → PTY size sync
├── internal/sessions/         Session-addressable runtime (Phase 1.0+)
│   ├── id.go                  SessionID + UUIDv4 NewID() via crypto/rand
│   ├── session.go             Session: wraps one supervisor + optional bridge; lifecycle goroutine (active↔evicted state machine, idle timer); Activate / Run / Attach with attach bookkeeping
│   ├── pool.go                Pool: in-memory registry, Config (RegistryPath + ClaudeSessionsDir + IdleTimeout + ActiveCap), load-or-mint bootstrap on New, RotateID seam, saveLocked + persist, errgroup Run with supervise() fan-out seam, allocated-UUID skip set, Snapshot, Activate (cap-aware), capMu
│   ├── registry.go            On-disk schema (registryFile, registryEntry); loadRegistry, saveRegistryLocked (atomic temp+rename), pickBootstrap, sortEntriesByCreatedAt
│   ├── reconcile.go           Startup JSONL scan: encodeWorkdir, mostRecentJSONL, reconcileBootstrapOnNew, DefaultClaudeSessionsDir
│   └── rotation/              Live /clear watcher (Phase 1.2b-B)
│       ├── watcher.go         fsnotify lifecycle, event loop, probe orchestration with bounded retry
│       ├── probe.go           Probe interface, parseProcFD, parseLsofOutput, noopProbe fallback
│       ├── probe_linux.go     //go:build linux  — /proc/<pid>/fd walk
│       └── probe_darwin.go    //go:build darwin — lsof -nP -p <pid> -F fn shell-out
├── internal/control/          Control-plane server (Unix socket, JSON)
│   ├── server.go              Server, SessionResolver / Session interfaces, verb dispatch
│   ├── attach.go              Attach handoff to supervisor bridge
│   └── logs.go                Ring-buffer log streaming
├── internal/e2e/              End-to-end test harness (//go:build e2e || e2e_install)
│   ├── harness.go             Harness, Start(t), pyry build helper, readiness poll, teardown
│   ├── harness_test.go        Smoke + failure-injection (re-exec + processAlive)
│   └── install_linux_test.go  //go:build linux && e2e_install — systemd round-trip, PATH inheritance, cleanup-on-fatal (#80)
├── systemd/pyry.service       Linux systemd user unit
└── launchd/dev.pyrycode.pyry.plist   macOS launchd plist
```

Dependency direction: `cmd/pyry → internal/sessions → internal/supervisor`, with `internal/control` importing `internal/sessions` for the `SessionID` type referenced by its `SessionResolver` interface. `internal/sessions/rotation` is downstream of `internal/sessions` (no back-edge — the contract is closures over primitive types so the rotation package never imports its host). `internal/supervisor` has no upward imports — verifiable with `go list -deps ./internal/supervisor/...`.

## Data Flow

### Interactive Session

```
User terminal
    │
    ├── stdin ──────> pyry ──> PTY master fd ──> claude (child process)
    │
    └── stdout <───── pyry <── PTY master fd <── claude (child process)
```

The supervisor puts the controlling terminal into raw mode so keystrokes pass through unmodified. SIGWINCH signals are forwarded to the PTY so terminal resizes propagate to the child.

### Restart Cycle

```
supervisor.Run()
    │
    ├── runOnce() ──> spawn claude in PTY, bridge I/O
    │                 wait for child exit
    │
    ├── child exited? ──> apply backoff delay
    │                     if uptime > resetAfter: reset backoff to initial
    │                     respawn with --continue (after first run, when ResumeLast is true)
    │
    └── ctx cancelled? ──> graceful shutdown
```

### Backoff Strategy

- Initial delay: 500ms
- Doubles on each restart: 500ms → 1s → 2s → 4s → ... → 30s (max)
- Resets to initial if the child stayed up longer than 60s (stability indicator)
- Context cancellation (SIGINT/SIGTERM) breaks out of the backoff wait

## Key Types

### `supervisor.Config`

All supervisor configuration in a single struct. Passed to `supervisor.New()`.

Fields: ClaudeBin (path to claude), WorkDir (child's cwd), ResumeLast (use --continue after first run), ClaudeArgs (pass-through args), Bridge (optional service-mode I/O mediator), Logger (*slog.Logger), backoff params (Initial, Max, Reset durations).

### `supervisor.Bridge`

Service-mode I/O mediator. A single `Bridge` instance persists across child restarts; the supervisor brackets each `runOnce` iteration with `Bridge.BeginIteration()` / `Bridge.EndIteration()` so the input pump goroutine terminates cleanly per iteration instead of leaking and racing the next one for queued attach-client bytes. Input path: `chan []byte` + per-iteration cancel (`Bridge.Read` selects between an incoming chunk and EOF on iteration end — Go's `select` non-determinism preserves any in-flight chunk for the next iteration). Output path: forward to attached writer or discard; `Write` never returns an error so the PTY-drain goroutine cannot wedge mid-disconnect. Resize seam: `SetPTY(*os.File)` registers the per-iteration PTY master under a leaf-only `ptyMu`; `Resize(rows, cols uint16)` calls `pty.Setsize` (silently no-ops between iterations). `runOnce` clears the registration with `SetPTY(nil)` **before** `EndIteration` so a racing `Resize` sees nil rather than a closed fd. See [ADR 007](../decisions/007-bridge-iteration-boundaries.md), [ADR 008](../decisions/008-bridge-resize-seam.md).

### `supervisor.Supervisor`

Owns the child process lifecycle. Two methods:
- `New(cfg Config) (*Supervisor, error)` — validates config, applies defaults
- `Run(ctx context.Context) error` — the main loop: spawn, wait, backoff, repeat

### `supervisor.backoffTimer`

Extracted backoff logic. Computes the next delay based on how long the previous child ran:
- `next(uptime time.Duration) time.Duration` — returns the delay and advances internal state
- `reset()` — returns to initial delay

## Platform Support

- **Linux:** Primary target. systemd user unit for daemon management.
- **macOS:** Supported. launchd plist provided. Cross-compile verified for darwin/amd64 and darwin/arm64.
- **Windows:** Out of scope. Would need ConPTY instead of Unix PTY, different signal handling, and a service wrapper.

## Dependencies

| Module | Purpose | Why not stdlib |
|--------|---------|----------------|
| `creack/pty` | PTY allocation and size management | No stdlib PTY support |
| `fsnotify/fsnotify` | Live `/clear` rotation detection on the claude sessions dir (Phase 1.2b-B) | Cross-platform inotify+kqueue without owning two stacks. See [ADR 004](../decisions/004-fsnotify-for-rotation-detection.md). |
| `golang.org/x/term` | Terminal raw mode, state save/restore | Extended terminal ops not in stdlib |
| `golang.org/x/sync` | `errgroup` for `Pool.Run`'s bootstrap+watcher fan-out (Phase 1.1+ extends to N sessions) | Semi-official extension; clearer than ad-hoc 2-goroutine coordination |
| `golang.org/x/sys` | System calls (indirect, via x/term and fsnotify) | — |

### Session Registry (Phase 1.2a)

```
~/.pyry/<sanitized-name>/sessions.json    (file 0600, dir 0700)
~/.pyry/<sanitized-name>.sock             (sibling — single-writer per name)
```

`Pool.New` reads the registry on startup. Missing or empty file → cold start (mint UUID, write file). Valid file → warm start (reuse persisted UUID, no rewrite). Malformed JSON → fatal at startup.

`saveLocked` writes via `os.CreateTemp` → fsync → `os.Rename` in the same directory. Rename is the commit point; partial JSON is unreachable in the target file. Called under `Pool.mu` (write) by mutating ops; in 1.2a only `Pool.New`'s cold-start path invokes it.

Forward-compat: `version` is a future hook; unknown top-level and per-session fields are silently ignored on read.

### Startup JSONL Reconciliation (Phase 1.2b-A)

```
~/.claude/projects/<encoded-cwd>/<uuid>.jsonl    (claude's own files)
```

`Pool.New` scans the per-workdir claude session dir, finds the most-recently-modified `<uuid>.jsonl`, and rotates the registry's bootstrap entry to that UUID if it disagrees. Self-heals across `/clear` (claude rotates UUIDs on `/clear`; without reconciliation, post-`pyry stop` the registry would still point at the pre-`/clear` UUID).

`encodeWorkdir` maps cwd → claude's path component by replacing both `/` and `.` with `-`. The pre-rotation JSONL is never modified — only the registry pointer moves. Missing/unreadable claude dir is logged and ignored (startup proceeds with the existing bootstrap). The mutation goes through `Pool.RotateID`, the load-bearing seam reused by Phase 1.2b-B's live-detection watcher.

### Live `/clear` Rotation Watcher (Phase 1.2b-B)

```
Pool.Run (errgroup)
    ├── bootstrap.Run(gctx)
    └── rotation.Watcher.Run(gctx)
              │
              ▼
        fsnotify CREATE on ~/.claude/projects/<encoded-cwd>/<new>.jsonl
              │
              ▼
        IsAllocated(<new>)? → consume + skip (Phase 1.1's --session-id mints)
        Snapshot()         → [{id: <old>, pid}, ...]
        probeWithRetry(pid) → /proc/<pid>/fd walk (Linux) or `lsof -F fn` (Darwin)
              │
              ▼
        match → OnRotate(<old>, <new>) → Pool.RotateID
```

`internal/sessions/rotation` is its own package, dependency-direction-respecting (no import of `internal/sessions`). The contract is `rotation.Config` closures over primitive types, wired in `Pool.Run`. Watcher disabled (and pyry startup proceeds) when `ClaudeSessionsDir` is empty, `fsnotify` init fails, or — on darwin — `lsof` is missing from PATH (`noopProbe` fallback). See [features/rotation-watcher.md](../features/rotation-watcher.md) and [ADR 004](../decisions/004-fsnotify-for-rotation-detection.md).

### Idle Eviction + Lazy Respawn (Phase 1.2c-A)

```
Session.Run (per-session lifecycle goroutine)
    │
    ├── runActive   → supervisor up, idle timer armed
    │     │
    │     ├── attached>0 on fire → re-arm (poll-with-grace)
    │     ├── attached==0 on fire → cancel inner ctx → drain runErr → evict
    │     └── outer ctx done    → cancel inner ctx → drain runErr → return
    │
    └── runEvicted  → no supervisor; wait on activateCh or ctx
              │
              ▼
    transitionTo(state) → Pool.persist → registry write
```

Each `*Session` owns a per-session lifecycle goroutine that drives an `active ↔ evicted` two-state machine. Activity = "at least one client attached" (`attached > 0`). On the idle timeout with no attaches, the supervisor's inner ctx is cancelled and claude exits cleanly — the JSONL on disk is preserved untouched. `Session.Activate(ctx)` (called by `handleAttach` before `Attach`) wakes the session and respawns the supervisor pointing at the same JSONL.

Registry gains `lifecycle_state` (`omitempty`, defaults to `"active"`). Bootstrap warm-starts in whatever state the registry says. Lock order: `Pool.mu → Session.lcMu`. CLI: `-pyry-idle-timeout` (default `15m`, `0` disables). See [features/idle-eviction.md](../features/idle-eviction.md) and [ADR 005](../decisions/005-idle-eviction-state-machine.md).

### E2E Harness (Phase test-infra, ticket #68)

```
internal/e2e (//go:build e2e)
    │
    ├── ensurePyryBuilt(t) ──> sync.Once go build  (or $PYRY_E2E_BIN)
    │
    └── Start(t) *Harness
          ├── HOME=t.TempDir(), -pyry-socket=<tmp>/pyry.sock,
          │   -pyry-claude=/bin/sleep -- infinity, -pyry-idle-timeout=0
          ├── cmd.Start
          ├── go { cmd.Wait; close(doneCh) }
          ├── waitForReady: os.Stat + net.Dial loop, 5s deadline,
          │                 short-circuit on doneCh
          └── t.Cleanup(SIGTERM → 3s → SIGKILL → 1s → os.Remove(socket))
```

Build-tag-isolated package; default `go test ./...` does not compile it. Invoke
with `go test -tags=e2e ./internal/e2e/...`. The supervised "claude" is
`/bin/sleep infinity` (exists on Linux + macOS, survives until SIGTERM); idle
eviction disabled so the smoke path isn't racing the timer. Failure-injection
verification re-execs the test binary (`-test.run=^TestInnerFatalChild$` +
`PYRY_E2E_INNER_FATAL_OUT` env var) so an inner `t.Fatal` runs in a fresh
process; the parent reads the state file and asserts the pid is gone (POSIX
`Signal(0)` probe) and the socket is `fs.ErrNotExist`. See
[features/e2e-harness.md](../features/e2e-harness.md).

CLI-driver wrappers (`Harness.Status()`, `Stop()`, generic `Run(args...)`),
`Option`s, the first feature-flavoured e2e, and CI wiring are deferred to the
#51 follow-up.

### Install-Service E2E (Phase test-infra, ticket #80)

```
internal/e2e/install_linux_test.go (//go:build linux && e2e_install)
    │
    ├── TestE2EInstall_RoundTrip_Linux
    │     install.Install → daemon-reload → start → waitForActive
    │     → pyry status -pyry-name=<name> → stop → waitForInactive
    │     → t.Cleanup(stop/disable/remove/daemon-reload)
    │
    ├── TestE2EInstall_PathInheritance_Linux        (no systemd needed)
    │     install.Install with EnvPath = $PATH, HomeDir = t.TempDir()
    │     → assert every entry of $PATH appears in Environment="PATH=..."
    │       with $HOME/ → %h/ substitution (bug-#19 regression guard)
    │
    └── TestE2EInstall_CleanupOnFatal_Linux         (re-exec)
          exec.Command(os.Args[0], -test.run=^TestInstallFatalChild$)
          ↓ child installs + starts + t.Fatal
          → parent: stat(unitPath) is ErrNotExist
                    is-active <name> != "active"
```

Build tag `e2e_install` is **separate** from `e2e` so default e2e CI doesn't
require a running systemd `--user` session. `harness.go`'s tag was widened to
`e2e || e2e_install` so the install tests reuse `ensurePyryBuilt` /
`childEnv`. Tests skip cleanly when `systemctl --user is-system-running`
reports `offline` / `unknown` / missing (CI runners, containers without D-Bus).
`install.Install` is called directly rather than via the CLI binary to avoid a
test-only override on `Options.Binary`. See
[features/install-e2e.md](../features/install-e2e.md).

## Future Architecture (not yet implemented)

- **Phase 1.1a-A1 (#72) — landed:** `Pool.supervise(sess)` seam + `runGroup`/`runCtx` handle on `*Pool`. Bootstrap fan-out in `Pool.Run` flows through the helper; the watcher fan-out stays inline (not a `*Session`). `ErrPoolNotRunning` sentinel for before/after-`Run` calls.
- **Phase 1.1+:** `Pool.Create(ctx, label)` (sibling A2 — consumer of the supervise seam, landed), `AttachPayload.SessionID` on the wire (1.1e-C, landed — server routes via `Pool.ResolveID`; CLI surface still pending), `pyry sessions new` calling `Pool.RegisterAllocatedUUID` before `claude --session-id <uuid>`, `pyry attach <id>` CLI positional (1.1e-D), per-session log lines. Live-resize loop landed end-to-end across #136 (`Bridge.Resize` seam) + #137 (`VerbResize` wire + `handleResize` server applier) + #133 (`startWinsizeWatcher` client-side SIGWINCH emitter in `pyry attach`).
- **Phase 2:** Channels — inbound event routing from Discord/Telegram
- **Phase 3:** Cross-cutting services — knowledge capture, memsearch, cron runner in-process
- **Phase 4:** Remote access — relay server, E2E encryption (Noise Protocol), QR pairing
- **Phase 5:** Voice — WebRTC via pion/webrtc, STT/TTS pipeline
