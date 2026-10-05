# #2815: Claude account token from the macOS Keychain or the Linux Secret Service

## Files read

- `cmd/pyry/claude_account.go` → `newClaudeAccount`: source dispatch and startup refusals naming only the origin; the new `keychain:` case goes here.
- `cmd/pyry/claude_account.go` → `readOpReference`, `opContextFailure`, `cappedBuffer`, `withoutAccountToken`, `parseTokenBytes`: the exec handling the new reader shares.
- `cmd/pyry/claude_account.go` → `claudeAccountPayload`: the #2839 accessor-kind to wire-kind mapping. It passes the accessor kind through, so an accessor kind of `os_keychain` already maps to the wire kind.
- `internal/protocol/claude_account.go` → `ClaudeAccountKindOSKeychain`: wire kind; its comment says no daemon source exists yet.
- `cmd/pyry/claude_account_op_test.go`: fake executables on `PATH`, leak assertions; the pattern for the new tests.
- `cmd/pyry/claude_account_test.go` → `TestNewClaudeAccount_RefusesWithoutEchoingValue`: refusal-without-echo pattern.

No other feature branch touches these files.

## Context

The daemon reads its Claude token from a file or from 1Password. Operators on a Mac or a Linux desktop want the OS secret store without 1Password. This adds `keychain:<name>` as a third source with the same accessor, the same per-launch read and the same refusal rules.

## Design

- **Source form.** `keychain:<name>`, from flag, env or `claude-account.json`. `<name>` is the item's service name. Empty, containing a control byte, or starting with `-` is a startup error `claude account source from <origin>: malformed keychain item name`. On a platform other than darwin or linux the source is a startup error naming only the origin. The "unsupported source" text lists the new form.
- **Platform.** A package variable `claudeAccountGOOS` (default `runtime.GOOS`) chosen at construction, so tests on Linux exercise both platforms. `keychainCommand(goos, name) (tool string, args []string, ok bool)`:
  - darwin: `security find-generic-password -s <name> -w`
  - linux: `secret-tool lookup service <name>`
- **Accessor kind** `os_keychain`; `claudeAccountPayload` passes it through to `protocol.ClaudeAccountKindOSKeychain`. The protocol comment is updated to say the source exists.
- **Shared runner.** `readOpReference`'s body becomes `runTokenCommand(ctx, tool, args, reasons)`, where `reasons` is a small struct of the six fixed strings (unavailable, failed, empty, invalid, timed out, cancelled). `readOpReference` and the new `readKeychainItem` each call it with their own reason set. Same process-group kill, `WaitDelay`, capped stdout, stderr discarded, no stdin, env without `CLAUDE_CODE_OAUTH_TOKEN`, return on ctx end without waiting for the tool. `opContextFailure` becomes the runner's context branch using the reason set.
- **Keychain reasons:** "keychain tool unavailable", "keychain read failed", "keychain output empty", "keychain output invalid", "keychain read timed out", "keychain read cancelled".

## Concurrency model

Unchanged from the `op://` reader: one goroutine per read waits on the child; the read returns at ctx end, `cmd.Cancel` kills the process group and `WaitDelay` bounds the background Wait.

## Error handling

Every failure is an `accountReadError` with a fixed reason; no exec error, stdout, stderr, token or item name reaches logs, errors or `status()`. No fallback, no cached token.

## Testing strategy

New `cmd/pyry/claude_account_keychain_test.go`, fakes `security` and `secret-tool` on a prepended `PATH`, `claudeAccountGOOS` set per test with cleanup:

- per platform: success with the exact argument vector (fake refuses any other vector; the name has a space), non-zero exit with leaking output, empty output, invalid output, hang cut by deadline, hang cut by cancel, missing tool (`PATH` set to an empty dir).
- leak assertion: planted token and item name absent from log, error and status.
- malformed names (`keychain:`, control byte, leading `-`) from flag, env and file refused naming the origin and not echoing the value; unsupported GOOS refused.
- kind `os_keychain` and `claudeAccountPayload` wire kind.
- Existing `op://` tests guard the refactor.

## Open questions

- None.

## Documentation handoff

Pending for the documentation stage:

- `docs/guide.md`, "Claude account source": add `keychain:<name>` with flag, env and JSON examples. Show item creation on macOS (`security add-generic-password -s <name> -a "$USER" -w`) and Linux (`secret-tool store --label=... service <name>`, package `libsecret-tools`). Warn against reusing "Claude Code-credentials".
- `docs/deployment.md`, "Claude account source": macOS access prompt and "Always Allow"; LaunchAgent works, system LaunchDaemon cannot reach the login keychain; Linux needs an unlocked desktop keyring and a session D-Bus; headless Linux, pyrybox included, keeps the owner-only file. Replace the "Related, not yet built" mention of #2815.
