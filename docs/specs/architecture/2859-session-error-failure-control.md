# #2859 — same-runner session-error failure and release

## Files read

- `internal/streamsup/runner.go` → `beginSpawn`, `spawnAndWait`, `Run`: one-acquisition spawn inputs, leaf locks, automatic backoff and stable session identity.
- `internal/streamsup/helper_test.go` → `helperChild`: subprocess-owned argv/environment witnesses and stdin-driven exits.
- `internal/e2e/realclaude/harness_daemon_test.go` → `spawnBootstrapDaemon`, wire and registry helpers: isolated daemon lifecycle and encrypted phone traffic.
- `internal/e2e/realclaude/fixtures.go` → `ensurePyryBuilt`, `buildEnvWithRealHome`: existing untagged/prebuilt selection must stay unchanged.
- `internal/e2e/realclaude/interactive_stream_multiturn_continuity_test.go` → `drainForCompletedTurnText`: completion includes an idle boundary after reply text.
- `internal/msgqueue/queue.go` → `drain`, `giveUp`: production two-minute give-up drops an undelivered head.
- `internal/protocol/messaging.go` → `QueueStatePayload`, `SessionErrorPayload`: conversation-scoped state witnesses.
- `docs/knowledge/features/streamsup-package.md` and its supervise-loop topic: a child-owned marker, rather than `onSpawn`, proves execution; crash callbacks are synchronous and lock-free.
- `docs/knowledge/features/e2e-realclaude.md` § Test infrastructure: tagged harness compilation and executed live counts matter.
- `docs/knowledge/features/msgqueue-package.md`, `development-verification.md`, `CODING-STYLE.md`: delivery proof, independent witnesses and subprocess tests.

## Context

Mobile #1731 needs an external failure/release bridge inside an isolated daemon without replacing its Runner. A local selection file provides that bridge without widening production factory interfaces. No decision record is needed. No other fetched feature branch touches the two existing files this design changes.

Sizing: one deliverable, four acceptance criteria, approximately 600–700 written lines including plan and tests; zero new exported types/interfaces, one existing consumer refactored locally, fewer than ten rejection branches. This fits the five builder limits and the S estimate.

## Design

`spawnAndWait` calls a build-selected, unexported `spawnClaudeBin() (string, error)` before constructing the command. Ordinary builds return `Config.ClaudeBin` directly and never inspect activation inputs. Under `e2e_realclaude`, an unset `PYRY_E2E_CLAUDE_BIN_FILE` also returns that exact path. When set, it names a driver-owned local file containing one absolute executable path (optional surrounding whitespace), bounded to 4096 bytes. Invalid or unreadable activated input fails that spawn rather than silently running real Claude.

The driver writes a private temporary file and atomically renames it over the selection file. A spawn reads one complete selection; the command keeps that immutable value. This independent executable selection needs no Runner mutation or extra mutex acquisition: `beginSpawn` retains its current one-acquisition snapshot and all existing argv/environment/workdir composition. Updating the file sends no signal, restart hint or rotation. It changes only a future spawn; normal backoff continues.

Extract the existing daemon spawn body into a private helper taking an explicit daemon binary. `spawnBootstrapDaemon` continues to select its existing untagged/prebuilt binary. The new session-error test explicitly builds `go build -tags e2e_realclaude -o <temporary-path>/pyry ./cmd/pyry`, ignoring `PYRY_E2E_BIN`, then reuses that spawn helper and the encrypted wire harness.

Two isolated arms start a controlled failing executable. Its initial exit is gated until the phone connects and queues a message, avoiding losing the one-shot crash notice before handshake. Further executions exit immediately. The first arm observes child-crashing plus retained backlog and selects real Claude; the second observes blocked plus empty backlog before selecting real Claude and sending a distinct fresh message. Neither restarts the daemon or Runner, changes the bound session, nor asks for a manual respawn.

## Concurrency model

No watcher or production goroutine is added. File replacement and one bounded read are the synchronization contract; all I/O runs outside Runner leaf locks. Tests join their Runner and daemon goroutines on cleanup. Existing daemon and phone receive lifecycles remain unchanged.

## Error handling

Activated file open/read, size or absolute-path validation errors return contextual spawn errors into the existing backoff path. Executable start/exit errors retain current semantics. Missing credentials skip live tests explicitly. Daemon exit, unexpected scope, blocked-before-release, dropped/retained backlog mismatches, and reply timeout fail the live proof with bounded deadlines.

## Testing strategy

Write failing tests before the implementation. Tagged Runner tests observe child-owned executable identity, unchanged argv/environment/workdir, updates while a child lives, unchanged backoff/session, and atomic replacement racing launches. An untagged subprocess test sets activation input and proves the configured child still runs. Tagged table cases reject invalid/unreadable input and preserve the unset path.

Live arms decrypt every frame in nonce order, collect queue and session-error state, and wait for a completed real-Claude turn. Delivery identifiers and reply content distinguish the fresh message from the dropped one. Check persisted conversation binding and daemon liveness through release. Use the production retry/backoff/give-up timing with bounded observation deadlines.

Run ordinary and tagged streamsup race tests, `go vet ./...`, `go build` for ordinary and tagged daemons, and tagged live-package compilation. Dispatcher owns the live run and full-module verifier gate; no live evidence is claimed locally.

## Open questions

None. A selection file alone supplies the external next-spawn control; a callable setter is unnecessary.

## Documentation handoff

Pending documentation stage: in `docs/knowledge/features/e2e-realclaude.md` § **Test infrastructure**, document the actual tagged build command, `PYRY_E2E_CLAUDE_BIN_FILE` activation, absolute-path selection and atomic replacement/release contract for an external isolated driver, next-spawn semantics and ordinary-build exclusion. State that release before give-up delivers retained backlog, while release after `session.blocked` needs a fresh message and never replays dropped backlog. Mobile #1731 consumes this contract.

## Security review

**Verdict:** PASS

**Findings:**

- [Trust boundaries / threat model] The tagged `spawnClaudeBin` is the sole local file-to-exec boundary. Activation is trusted driver process environment; remote/conversation input cannot select it. Ordinary builds exclude that parser and ignore its input entirely. Drivers must use an isolated tagged daemon, never a production deployment.
- [Tokens] No credentials are read, stored or logged by the control. Child environment remains the existing per-spawn account-token path; live authentication stays with the dispatcher.
- [File operations] SHOULD FIX: driver selection files use mode 0600 inside private temporary directories and atomic same-directory rename. A 4096-byte read cap and absolute-path validation reject malformed input. Driver-owned local symlinks/executable replacement are trusted just as configured `ClaudeBin` already is.
- [Subprocesses] `exec.CommandContext` receives one path, not shell text; preserve existing argv, environment, cwd and termination/reaping. Test fixtures may use fixed shell scripts without interpolating untrusted values.
- [Cryptography / network and I/O] No new socket, command, cryptography or nonce handling. Wire tests reuse Noise handshake/decryption and bounded receives; selection read is bounded local I/O.
- [Errors / logs] Selection failures carry context without logging file contents. Session-error messages remain existing fixed daemon text, with no executable or token data on the wire.
- [Concurrency] No new lock or watcher. Read selection outside all leaf locks; atomic rename provides complete snapshots. Tests prove live child and backoff are left alone and join children on shutdown.

**Reviewer:** builder (self-review per security-review checklist)
**Date:** 2026-10-06
