# #2825 — Claude account tokens from 1Password with a configurable CLI

## Files read

- `cmd/pyry/claude_account.go` → `newClaudeAccount`, `resolveClaudeAccountSource`, `readTokenFile`, `claudeAccount.read`/`prime`/`status`, `tokenFileError`: the accessor this ticket adds a second reader behind.
- `cmd/pyry/main.go` → `pyryFlagValues`, the `fs.String(claudeAccountFlagName, …)` registration and the `newClaudeAccount` call in `runSupervisor`: where the new flag and env var are wired.
- `internal/streamsup/runner.go` → `Runner.accountTokenEnv`, `AccountTokenFailure`, `AccountTokenProvider`: the per-attempt gate (10 s deadline, cancellation precedence, LF/CRLF trim, env replacement) reused unchanged.
- `cmd/pyry/claude_account_test.go` → `TestNewClaudeAccount_RefusesWithoutEchoingValue`, `configuredAccount`: refused-value test that currently lists `op://`.
- `cmd/pyry/claude_account_startup_test.go` → `TestClaudeAccount_TwoInstancesEachGetOwnToken`, `startAccountInstance`, `TestClaudeAccount_RefusedSourceStopsStartup` ("env scheme" case): integration pattern and the second test that refuses `op://` today.
- `docs/knowledge/features/claude-account-source.md` § "The selection file needed the same trust check as the secret it points to": `op_cli` in `claude-account.json` picks an executable that receives no secret but whose stdout becomes the token, so it goes through the same owner/mode check as `source` — reading both keys through one opener keeps that true. § "The ten-second bound covers reads that return, not reads that hang": for a subprocess the bound must be enforced by returning on `ctx.Done()`, not by waiting for the child. § "Testing": the leak scan over `HOME` must exempt only operator secrets, so the fake `op` and its token live in a scratch dir outside `HOME`.

No other `feature/*` branch touches these four files.

## Context

#2824 shipped the per-instance accessor with one source kind (absolute token file) and refuses `op://` at startup. Operators want 1Password as the master copy so rotation needs no edit of a daemon-side file. This adds `op://…` as a second source kind behind the same accessor; the streamsup gate, backoff and wiring are untouched.

## Design

All production changes are in `cmd/pyry/claude_account.go` plus flag wiring in `cmd/pyry/main.go`.

**Selection.**
- New constants: `claudeAccountOpCLIEnv = "PYRY_CLAUDE_ACCOUNT_OP_CLI"`, `claudeAccountOpCLIFlagName = "pyry-claude-account-op-cli"`, `claudeAccountOpCLIDefault = "op"`.
- `readClaudeAccountField(instanceDir, key) (value, origin string, err error)`: the current file-reading half of `resolveClaudeAccountSource` (O_NONBLOCK open, fstat owner/mode/regular check, 64 KiB cap, JSON object), returning the string under `key`. Absent file or absent key → `""`; non-string/null → error naming the key and the file. `resolveClaudeAccountSource` keeps its signature and calls it with `"source"`.
- `resolveClaudeAccountOpCLI(flagValue, envValue, instanceDir) (cli, origin string, err error)`: flag, then env, then the file's `op_cli`, else `op`, independent of where the source came from. Called only when the source is `op://`, so file sources never read or validate `op_cli` (unchanged behaviour).
- `validOpCLI(v) bool`: either a bare name matching `[A-Za-z0-9._+-]+` other than `.`/`..`, or `filepath.IsAbs(v)` with no control byte (`< 0x20`, `0x7f`). Spaces are allowed only in absolute paths. Anything else (e.g. `op read`, `./op`, `bin/op`, `op;x`) → startup error `claude account 1Password CLI from <origin>: …` with no value.
- `newClaudeAccount(flagValue, envValue, opCLIFlag, opCLIEnv, instanceDir, logger)`: a source with prefix `op://`, longer than the prefix and with no control byte, selects kind `"1password"` with `reader = readOpReference(ctx, cli, source)`. A malformed `op://` value is refused like any other unusable source, naming only the origin. Absolute paths keep the file reader; every other value stays refused.

**Reading.** `readOpReference(ctx, cli, ref) (string, AccountTokenFailure, error)`:
- `exec.CommandContext(ctx, cli, "read", ref)`: no shell; the reference is one argv entry and cannot be an option since it starts with `op://`. `Stdin` and `Stderr` nil (→ `/dev/null`): stderr is never captured. `Stdout` is a capped writer keeping at most `maxAccountTokenBytes+1` bytes and discarding the rest. Environment inherited (the CLI needs `HOME`, `PATH`, `OP_*`, WSL interop variables).
- `SysProcAttr{Setpgid: true}` and `Cancel` kills the whole process group, so a forked helper dies with the CLI. `WaitDelay` (1 s) bounds `Wait` if something outside the group still holds the stdout pipe.
- `Start`, then `Wait` in a goroutine that sends on a buffered channel; the caller selects on that channel and `ctx.Done()`. On `ctx.Done()` it returns at once (timeout or cancellation), so a CLI still running never holds the launch past the bound; the goroutine exits when the killed child is reaped or `WaitDelay` fires.
- Start error → "1Password CLI unavailable" (`AccountTokenReadFailure`); non-zero exit or any wait error → "1Password read failed"; deadline → "1Password read timed out" (`AccountTokenTimeout`); cancel → "1Password read cancelled" (`AccountTokenCancellation`); output over cap or failing the token shape → "1Password output invalid"; empty after one LF/CRLF trim → "1Password output empty".
- The LF/CRLF trim and printable-ASCII check move from `readTokenFile` into a shared `parseTokenBytes(data) (string, AccountTokenFailure)` both readers call; file reasons stay as they are.
- `tokenFileError` is renamed `accountReadError` since both readers return it; its text is always one of the fixed reasons above. No OS/exec error, stdout, stderr, reference or CLI path is ever wrapped.

`claudeAccount.read`, `prime`, `status` and `provider` are unchanged: every launch and the startup read run the CLI afresh, there is no cache, and a later success moves `failed` to `ready`.

**Wiring.** `main.go` registers `-pyry-claude-account-op-cli` (string, help text names env and file key), adds it to `pyryFlagValues`, and passes it and `os.Getenv(claudeAccountOpCLIEnv)` to `newClaudeAccount`. The source flag's help text mentions `op://`.

## Concurrency model

One short-lived goroutine per 1Password read, waiting on the child. It exits when `Wait` returns: normally on child exit, at the latest `WaitDelay` after the context-triggered group kill. The result channel is buffered so the goroutine never blocks after the caller has returned. Concurrent reads from several runners each get their own child; the accessor's last-finisher state caveat is #2816's.

## Error handling

Every failure refuses that launch; streamsup's existing backoff retries the spawn. The daemon never answers an unlock prompt: the CLI has no stdin and no terminal, the bound ends the attempt, and the next spawn retries after the operator unlocks 1Password. Startup errors (bad `op_cli`, malformed reference, bad JSON types) stop the daemon before the control socket opens, naming only the origin.

## Testing strategy

Fake `op` executables are `#!/bin/sh` scripts written to `t.TempDir()`.

- `resolveClaudeAccountOpCLI` table: flag > env > file `op_cli` > default; flag source still honours file `op_cli`; invalid `op_cli` values refused naming origin only; non-string `op_cli` refused.
- `readOpReference` / accessor scenarios: success with LF; reference containing a space arrives as exactly one argument after `read` (fake checks `$#` and `$1`/`$2`); non-zero exit; missing bare name and missing absolute path; hang cut off by deadline and by cancel, with the fake forking a `sleep` that holds stdout, returning well inside 3 s; empty output; invalid output (two lines, space); rotation between reads via a token file the fake cats; `failed` → `ready` after a later success; `op.exe` bare name found on a `PATH` directory whose path contains a space, and the same file selected by absolute path.
- Every failing fake prints the planted token and reference to stdout and stderr; tests assert neither appears in the captured log, returned error, or `status()`.
- Updated refusal tests: `op://` dropped from accepted-as-refused lists, replaced by `file:///…` and malformed `op://` values (control byte, bare prefix).
- Integration: two daemons under one `HOME`, instance A with a file source by flag, instance B with `{"source":"op://…"}` in its `claude-account.json` and a fake `op` prepended to `PATH` (fake and its token outside `HOME`); each fake-Claude child receives exactly its own token, and the log ring / `HOME` leak scan finds neither.

## Open questions

- Whether `WaitDelay` should be shorter than 1 s: it only bounds the background goroutine, not the launch, so 1 s is enough; revisit only if tests show leftover goroutines.

## Documentation handoff

Pending for the documentation stage:

- `docs/guide.md`, "Claude account source", "Pyry-specific flags" and "Environment knobs": add `op://vault/item/field` examples for the flag, the env var and the instance JSON; the `op_cli` setting with its flag (`-pyry-claude-account-op-cli`) and env (`PYRY_CLAUDE_ACCOUNT_OP_CLI`) forms and their precedence; and an `op.exe` example for WSL, such as `/mnt/c/Program Files/1Password CLI/op.exe`. State that the operator must already have WSL interop, Windows 1Password CLI and desktop-app integration set up. Link [1Password read](https://developer.1password.com/docs/cli/reference/commands/read/) and [desktop-app integration](https://developer.1password.com/docs/cli/app-integration/).
- `docs/deployment.md`, "Claude account source": background services cannot answer unlock or authorization prompts; reads time out after ten seconds without falling back to a stored Claude login; a later spawn retries after unlocking. Explain the startup read, the per-spawn read and rotation. Keep the headless-Linux owner-only-file recommendation and the separate pyrybox migration follow-up.
- `docs/knowledge/features/claude-account-source.md`: add the `op://` kind to "Shape" and the subprocess bound note.

## Security review

**Verdict:** PASS

**Findings:**

- [Trust boundaries] No findings. Two boundaries, each in one place: the CLI's stdout becomes a token only through `parseTokenBytes` (and streamsup's `accountTokenEnv` re-checks it); the executable choice from `claude-account.json` crosses only through `readClaudeAccountField`, which applies the same owner-only, not-group/other-writable check `source` already has, so another local user cannot redirect `op_cli` any more than `source`. Flag and env values are operator-controlled, at the same trust as `-pyry-claude`.
- [Tokens] SHOULD FIX: the CLI child would inherit `CLAUDE_CODE_OAUTH_TOKEN` from the daemon's environment, handing an unrelated credential to a process that does not need it. Build the CLI's environment from `os.Environ()` with that variable removed.
- [Tokens] No findings on exposure: stdout is held only in the capped buffer and returned, stderr goes to `/dev/null`, and no `exec`/`os` error (which carries the CLI path or argv) is wrapped; every error is an `accountReadError` with fixed text. No caching: each read runs the CLI afresh, and rotation or revocation in 1Password applies at the next spawn.
- [File operations] OUT OF SCOPE: an absolute `op_cli` path is executed without an owner/mode check on the executable itself, as `-pyry-claude` is today. A group-writable executable is the operator's choice; hardening executable selection across the daemon is not this ticket's.
- [File operations] No other findings: no path is joined from input; `claude-account.json` keeps its O_NONBLOCK descriptor-checked open.
- [Subprocesses] No findings. No shell: `exec.CommandContext(cli, "read", ref)` with the reference as one argv entry that starts with `op://` and so cannot parse as an option; control bytes in the reference are refused at startup. `validOpCLI` admits only a bare `[A-Za-z0-9._+-]+` name or an absolute path without control bytes, so a shell command or relative path is refused; Go's `exec.LookPath` refuses a `PATH` hit in the current directory. The child gets no stdin, runs in its own process group killed as a group on cancel, and `WaitDelay` bounds a descendant that escapes the group and holds the pipe. The caller returns on `ctx.Done()` regardless.
- [Subprocesses] No findings on `PATH` lookup: a bare name resolves through the daemon's own `PATH`, the same trust the daemon already places in it for `claude` and `codex`.
- [Cryptography] No findings: the design generates, compares and stores no secret material; the token passes through unchanged.
- [Network and I/O] No findings: stdout capped at `maxAccountTokenBytes+1` with the excess discarded, stderr not read, no stdin.
- [Errors, logs, telemetry] No findings. Logged fields stay `kind` and a fixed `reason`; startup errors name only the origin. Tests plant token and reference text on the fake's stdout and stderr and assert neither reaches logs, errors or `status()`.
- [Concurrency] No findings. One goroutine per read, exiting when `Wait` returns (at most `WaitDelay` after the group kill), sending on a buffered channel. If the daemon itself is SIGKILLed mid-read the CLI is orphaned and exits on its own; no daemon state is written by the read.
- [Threat model] OUT OF SCOPE: the accessor's last-finisher `status()` race is #2816's; OS-keychain sources are #2815.

**Reviewer:** builder (self-review per the security-review checklist)
**Date:** 2026-10-05
