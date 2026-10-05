# #2824 — per-instance Claude account source from owner-only token files

## Files read

- `internal/streamsup/runner.go` → `AccountTokenProvider`, `AccountTokenFailure`, `Config.AccountTokenProvider`, `accountTokenEnv`: the provider contract #2823 shipped. The runner already applies the ten-second read deadline, trims one LF/CRLF, refuses empty or whitespace-bearing tokens and replaces every inherited `CLAUDE_CODE_OAUTH_TOKEN` entry. Only allowlisted categories reach diagnostics.
- `cmd/pyry/main.go` → `pyryFlagValues`/`splitArgs`: a new value flag must be registered or its value falls through to claude's argv.
- `cmd/pyry/main.go` → `runSupervisor`: composition root; flag set, `config.Load`, `checkDebugCapture` (the startup-refusal posture this ticket copies), `selectInteractiveRunner` call.
- `cmd/pyry/main.go` → `resolveInstanceDirPath`: `~/.pyry/<sanitized-name>/`, home of `claude-account.json`.
- `cmd/pyry/main.go` → `selectInteractiveRunner`, `harnessRunnerFactory`: the Claude factory is wrapped beside the Codex one; only the Claude arm gets the provider.
- `cmd/pyry/streamsup_runner.go` → `streamApprovalConfig`, `newStreamRunnerFactory`: the struct argument already threads daemon-singleton values into each Claude runner; adding one field leaves the 13 + 8 test call sites untouched.
- `cmd/pyry/codex_runner.go` → `codexHarness.approval`: Codex receives the same struct but reads only `registry`, `timeout`, `surface`, so the new field never reaches a Codex child.
- `cmd/pyry/channel_delivery_test.go` → in-process `runSupervisor` calls with `/bin/true` children: the seam for the two-instance startup test.

## Context

#2823 gave `streamsup` a per-attempt token provider; nothing in `cmd/pyry` sets it. This ticket adds per-instance source selection, the first source kind (absolute file path), an instance-scoped accessor, and wires its read into the Claude runner factory. #2825 (`op://`), #2816 (client visibility) and #2815 (OS stores) build on the source string and the accessor. No decision record needed: this follows the existing startup-refusal and instance-dir conventions.

## Design

New file `cmd/pyry/claude_account.go`.

**Selection.** `resolveClaudeAccountSource(flagValue, envValue, instanceDir string) (source, origin string, err error)`. First nonempty of flag, env, then the `source` string in `<instanceDir>/claude-account.json`. The file is read only when both earlier values are empty; an absent file means not configured. Unknown keys are ignored (decoded into `map[string]json.RawMessage`). An unreadable file, invalid JSON or a non-string `source` is an error. `origin` is `flag -pyry-claude-account-source`, `env PYRY_CLAUDE_ACCOUNT_SOURCE` or the JSON file's path.

**Construction.** `newClaudeAccount(flagValue, envValue, instanceDir string, logger) (*claudeAccount, error)`. Empty source → a not-configured accessor whose `provider()` is nil (launch behaviour unchanged). An absolute path → kind `file`, reader `readTokenFile(path)`. Anything else, relative paths and URI schemes included, is refused. Every error names the origin and never echoes the value.

**File reader.** `readTokenFile(ctx, path) (token string, failure streamsup.AccountTokenFailure, err error)`. Opens afresh each call with `O_RDONLY|O_NONBLOCK` (a FIFO opens without blocking), `fstat`s the opened descriptor, and refuses: not a regular file, owner UID ≠ `os.Geteuid()`, any `0o077` mode bit, empty content, content over 4096 bytes, or a token (after stripping one LF or CRLF) holding any byte outside printable non-space ASCII. Errors are `tokenFileError{reason}` carrying a short daemon-authored reason and never the path or bytes. Empty → `AccountTokenEmptyOutput`, oversize/invalid → `AccountTokenInvalidOutput`, everything else → `AccountTokenReadFailure`.

**Accessor.** `claudeAccount` holds `kind`, the reader, and under a mutex `state` (`not-configured` | `ready` | `failed`) and `reason`.
- `read(ctx)` calls the reader and records the outcome: failure → state failed + reason, logged at Warn with kind and reason; success after failure → ready, logged at Info. It returns exactly what the reader returned, so a failed read never falls back to an earlier token. The token is never stored.
- `provider() streamsup.AccountTokenProvider`: nil when not configured, else `a.read`.
- `prime(ctx)`: the one bounded startup read (ten seconds), whose failure is logged and not fatal.
- `status() claudeAccountStatus{Kind, State, Reason}`: never the token or path.

**Wiring.** `runSupervisor` registers `-pyry-claude-account-source` (also in `pyryFlagValues`), builds the accessor after `checkDebugCapture` (refusal → startup error), primes it, and passes `accountToken: account.provider()` in the `streamApprovalConfig` literal. `newStreamRunnerFactory` sets `scfg.AccountTokenProvider = approval.accountToken`. The daemon's own environment is never written.

## Concurrency model

No goroutines. `read` is called concurrently by every runner's spawn path; the mutex guards only `state`/`reason`, never held across the file read. Shutdown is the runners' own.

## Error handling

- Configuration errors (bad source, bad JSON file) → `runSupervisor` returns before the control socket opens.
- Read errors → accessor failed, Warn log, runner refuses that launch with the category error (`streamsup` backoff retries; each retry reads afresh). Control socket and relay are unaffected. The next successful read clears the failure.

## Testing strategy

`cmd/pyry/claude_account_test.go`:
- Precedence table: flag beats env beats file; file read only when flag and env empty (a malformed file is not an error when flag is set); absent file / empty `source` → not configured; unknown keys ignored; relative path, `op://`, non-string `source`, invalid JSON, unreadable file → error naming origin without the value.
- File reader table: 0400 and 0600 accepted, LF/CRLF trimmed; missing, 0640, 0604, directory, FIFO (returns promptly), empty, newline-only, oversized, embedded space/NUL, two lines → refused with the right category.
- Recovery (chmod 0644 → failed, chmod 0600 → ready) and rotation (rename a new file in place → next read returns the new token).
- Leak check: a planted token never appears in logs, errors or `status()`.
- Startup: two in-process `runSupervisor` instances under one HOME, different names, workdirs, token files and claude helper scripts; instance A selects by flag, B by `claude-account.json`; a conflicting `CLAUDE_CODE_OAUTH_TOKEN` is inherited. A channel post drives each to spawn its helper, which records the token it received. Each sees only its own. The planted tokens appear in no file under `~/.pyry` and not in the `pyry logs` ring. A refused source makes `runSupervisor` return an error without the value.

## Open questions

- Does a channel post spawn the Claude child in-process with a helper script? Settle during the build; fall back to the `internal/e2e` harness if not.

## Documentation handoff

Pending for the documentation stage:
- `docs/guide.md`: "Pyry-specific flags" and "Environment knobs" entries, plus a "Claude account source" subsection under "Multiple instances" — flag/env/file precedence, `claude-account.json` example, raw-token format, ownership and modes, startup read and per-spawn refresh, failure and recovery (invalid configuration stops startup), shared Claude configuration preserved, a successful read does not prove the token valid; link https://code.claude.com/docs/en/authentication.
- `docs/deployment.md`: "Claude account source" section — provisioning from 1Password, restart needed for source changes but not token rotation, pyrybox on systemd 255 without TPM (encrypted user-service credentials need 256), migration of `claude-env` is follow-up, #2816 and #2815.

## Security review

**Verdict:** PASS

**Findings:**

- [Trust boundaries] Two boundaries, each one function. `resolveClaudeAccountSource` turns flag, env and JSON-file input into a source string plus an origin label; only an absolute path survives it. `readTokenFile` turns file bytes into a token; only printable non-space ASCII under 4096 bytes survives. Downstream, `streamsup.accountTokenEnv` re-validates before the token reaches a child environment.
- [Trust boundaries] SHOULD FIX: `claude-account.json` chooses which owner-only file is read and handed to a Claude child, so a group- or other-writable selection file, or one owned by another UID, would let another local user point the daemon at an unrelated single-line secret of the operator's. `resolveClaudeAccountSource` refuses that file when `fstat` on the opened descriptor shows another owner or any `0o022` bit.
- [Tokens] No findings on generation (none is generated). Storage is the operator's owner-only file; the accessor never stores the token, only state and a reason. Rotation is a fresh open on every read; a failed read returns no token, so there is no stale fallback. Revocation is the operator's, at Anthropic.
- [Tokens / Errors] SHOULD FIX: `os.Open` and `json.Unmarshal` errors carry the path or fragments of content. `readTokenFile` returns `tokenFileError` built from a fixed reason, never a wrapped OS error, and the selection errors use fixed text naming only the origin. The tests plant a token and assert it appears in no error, log line, `status()` value or file under `~/.pyry`.
- [File operations] No check-then-use gap: ownership, mode and type are read by `fstat` on the descriptor whose bytes are consumed. Opening with `O_NONBLOCK` keeps a FIFO from blocking; a directory, FIFO or device is refused before any read. Symlinks are followed on purpose (operators may link a provisioned file); the checks apply to the target actually opened, so a link cannot launder a foreign or loose file. The daemon writes no files in this design.
- [Subprocesses] No new subprocess. Only the Claude factory sets `AccountTokenProvider`; `codexHarness` reads only `registry`, `timeout` and `surface` from the shared struct, so Codex children and the daemon's own environment are unchanged. `streamsup` replaces every inherited `CLAUDE_CODE_OAUTH_TOKEN` entry.
- [Cryptography] Not applicable: no keys, nonces or secret comparisons.
- [Network and I/O] Reads are capped at 4096 bytes plus one for the token and one regular file for the JSON; `streamsup` bounds each read at ten seconds and `prime` does the same at startup.
- [Concurrency] One mutex guarding `state` and `reason`, never held across I/O; no goroutines started.
- [Threat model] OUT OF SCOPE: showing account state to clients is #2816; OS secret stores #2815; the `op://` reader #2825; token bytes lingering in garbage-collected memory are not addressed.

**Reviewer:** builder (self-review per the security-review checklist)
**Date:** 2026-10-05
