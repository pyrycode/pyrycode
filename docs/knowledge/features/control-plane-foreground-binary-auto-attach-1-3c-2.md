# Foreground binary auto-attach (1.3c-2)

When `pyry` is invoked as a foreground binary (no `attach` / `status` / `stop` / `logs` / `sessions` / `install-service` / `version` / `help` subcommand) AND the claude-side args contain `--session-id <uuid>`, pyry now checks whether a daemon already hosts that UUID and — if so — dispatches to `control.AttachStdio` instead of spawning a fresh supervised claude. The motivating consumer is Claudian's "claude binary path" UI setting: pointing it at `~/.local/bin/pyry` Just Works against a running daemon, without a wrapper script and without socket-name management.

### Decision flow

The gate is two private helpers in `cmd/pyry/main.go`, called from `runSupervisor` after `socketPath` is resolved and before any supervisor-mode side effect (logger, ring buffer, Bridge, Pool init):

```
runSupervisor(args)
  ├─ splitArgs(args) → pyryArgs, claudeArgs
  ├─ parse pyry flags
  ├─ socketPath := resolveSocketPath(*socketFlag, *name)
  │
  ├─ tryAutoAttach(socketPath, claudeArgs):
  │   ├─ if PYRY_NO_AUTO_ATTACH=="1" → (false, nil)
  │   ├─ id := extractSessionID(claudeArgs); if "" → (false, nil)
  │   ├─ if os.Stat(socketPath) errors → (false, nil)   ← <50ms ENOENT fast path
  │   ├─ ctx, cancel := context.WithTimeout(Background, 1s); defer cancel
  │   ├─ has, err := control.SessionsHasID(ctx, socketPath, id)
  │   │   if err != nil || !has → (false, nil)
  │   └─ control.AttachStdio(Background, socketPath, id, os.Stdin, os.Stdout, false)
  │       → (true, fmt.Errorf("attach: %w", err))   // err may be nil
  │
  └─ if !handled: existing supervisor path (logger, Bridge, Pool, Run)
```

The auto-attach branch reuses the **same daemon-name resolution chain** (`-pyry-name` flag → `PYRY_NAME` env → `DefaultName`) as every existing pyry client subcommand — `resolveSocketPath` is called once, and both branches consume its result.

### Conservative posture: every probe failure falls through

Auto-attach is the *exception*, not the default. There is exactly one path that takes the attach branch (the daemon's registry definitively has the UUID); every other outcome lands in supervised-spawn mode unchanged:

| Failure mode | Outcome |
|---|---|
| `PYRY_NO_AUTO_ATTACH=1` | fall through |
| no `--session-id` in `claudeArgs` | fall through (no syscall) |
| `os.Stat(socket)` ENOENT | fall through (<50ms, AC#3) |
| `os.Stat(socket)` other error (EACCES, EPERM, …) | fall through |
| dial timeout / refused / unresponsive daemon | fall through (1s probe ctx) |
| `SessionsHasID` returns `(_, err)` (transport, malformed UUID) | fall through |
| `SessionsHasID` returns `(false, nil)` (UUID absent) | fall through |
| `SessionsHasID` returns `(true, nil)` | dispatch to `AttachStdio` |

The "everything that isn't a true success falls through" rule is the load-bearing simplicity. Existing pyry users (no daemon, or no `--session-id` in their args) see no behavioural change.

### `extractSessionID` — claude flag shapes

Scans `claudeArgs` (already split out by `splitArgs` — `--session-id` is unambiguously a claude flag, never a pyry flag) for the four shapes claude itself accepts:

- `--session-id <value>`
- `--session-id=<value>`
- `-session-id <value>`
- `-session-id=<value>`

First match wins. `--session-id` as the last arg with no value returns `""` (treated as "absent"). The returned string is opaque — no UUID validation client-side; `SessionsHasID` rejects malformed input server-side and the helper coerces that to fall-through.

### `PYRY_NO_AUTO_ATTACH` escape hatch

Strict equality with `"1"` (matches `GODEBUG`/`GOTRACEBACK` convention; `PYRY_NO_AUTO_ATTACH=true` still probes). Used by tests and as a debug knob — operators bypassing auto-attach to pin a regression to the supervised path. The check is the first line of `tryAutoAttach`, before any syscall.

### Why dispatch inside `runSupervisor`, not `run()`

The top-level verb-switch dispatches on `os.Args[1]` against a closed set (`version`, `status`, `stop`, `logs`, `attach`, `sessions`, `install-service`, `help`, `-v`, `--version`, `-h`, `--help`); none can ever be `--session-id`. Splicing inside `runSupervisor` reuses the existing `splitArgs` + `resolveSocketPath` parse for free; splicing in `run()` would force a duplicate flag parse.

### What the auto-attach branch is byte-identical to

`pyry attach --stdio <uuid>` — same `control.AttachStdio` call, same `os.Stdin`/`os.Stdout` wiring, same `context.Background()`, same `createIfMissing=false` (we already proved existence via has-id; passing `true` would be redundant on the take branch). The daemon cannot tell whether the connecting client is `pyry attach --stdio` or `pyry --session-id <uuid>` falling into the auto-attach branch — wire-identical.

No human-affordance stderr lines (`--stdio` mode already suppresses them); the dispatched path is silent, matching what an SDK consumer expects from a transparent byte conduit.

### Tests

`cmd/pyry/auto_attach_test.go` covers `extractSessionID`'s shape rules and every fall-through arm of `tryAutoAttach`: no-session-id, env opt-out (strict `"1"`), socket absent (<50ms wall-clock budget), daemon unresponsive (listen but never accept; returns within 1.5s via the 1s probe ctx), has-id returns false, has-id errors on malformed input. The has-id branches use a tiny in-test Unix-socket accept loop that decodes one Request and writes back a canned Response — no full server harness.

The attach-commit branch (has-id true → `AttachStdio` runs) is intentionally **not** unit-tested; e2e coverage of the dispatch lives in #163 (happy path — `TestE2E_ForegroundAutoAttach_AttachesWhenDaemonHasSession` in `internal/e2e/auto_attach_happy_test.go`, see [e2e-harness.md § Foreground Auto-Attach Harness Pattern](e2e-harness.md#foreground-auto-attach-harness-pattern-auto_attachgo-auto_attach_happy_testgo-163)) and #164 (fallback scenarios). `internal/control/attach_stdio_client_test.go` (#154) covers `AttachStdio`'s own contract.

\#163's test was the **first end-to-end proof** of `control.AttachStdio` against a real daemon — #161/#162's stdio-attach tests were `t.Skip`'d pending #167, then rotated to #257 once #167 landed (the underlying harness bug was a separate `--session-id`-vs-test-binary collision). Auto-attach reaches `AttachStdio` directly from `runSupervisor` (no `parseClientFlags`, no verb dispatch, no `Pool.Create` argv-append against the test binary), so neither bug reaches this code path.

See `docs/specs/architecture/158-foreground-auto-attach.md` for the full ticket-time design; this section is the canonical evergreen reference.
