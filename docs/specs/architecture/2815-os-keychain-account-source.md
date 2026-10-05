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

Satisfied in the documentation stage; wording checked against `newClaudeAccount`, `keychainCommand`, `runTokenCommand`, `claudeAccountPayload` and the account-source tests:

- `docs/guide.md`, "Claude account source": add `keychain:<name>` with flag, env and JSON examples. Show item creation on macOS (`security add-generic-password -s <name> -a "$USER" -w`) and Linux (`secret-tool store --label=... service <name>`, package `libsecret-tools`). Warn against reusing "Claude Code-credentials".
- `docs/deployment.md`, "Claude account source": macOS access prompt and "Always Allow"; LaunchAgent works, system LaunchDaemon cannot reach the login keychain; Linux needs an unlocked desktop keyring and a session D-Bus; headless Linux, pyrybox included, keeps the owner-only file. Replace the "Related, not yet built" mention of #2815.

The verifier's additional handoff is also satisfied: `docs/knowledge/features/claude-account-source.md` covers the third source, shared runner, fixed keychain reasons and testing lessons; `docs/knowledge/features/protocol-package-types-claude-account-payloads.md` and `docs/protocol-mobile.md` identify the live `os_keychain` producer. The existing catalog entry is updated; no document was added or removed. Actual OS unlocking and service access remain operator checks, as the verifier noted.

## Security review

**Verdict:** PASS

**Findings:**

- [Trust boundaries] No findings. Two boundaries, each in one place. The source string enters through `newClaudeAccount`, from the flag, the environment or `claude-account.json`, and `readClaudeAccountField` applies the owner-only, not group- or other-writable check to the JSON field. The flag and the environment are operator-controlled, at the same trust as `-pyry-claude`. The tool's stdout becomes a token only through `parseTokenBytes`, which admits printable ASCII without spaces and nothing else, and streamsup's `accountTokenEnv` re-checks it.
- [Trust boundaries] No findings on item matching. `secret-tool lookup service <name>` returns any item whose `service` attribute matches, and `security find-generic-password -s <name>` returns the first item with that service, whatever its account. Another process of the same user could plant a matching item. But that process can already read the user's keyring and the owner-only token file, so planting an item crosses no boundary. A wrong value at worst fails Claude's own authentication on that launch.
- [Tokens] No findings on exposure. The item name lives only in the argv captured by the reader closure in `newClaudeAccount`, and it is never logged, wrapped into an error or copied into `claudeAccountStatus`. The `Label` in `claudeAccountPayload` is the separate operator field validated by `validAccountLabel`, never derived from the source. Startup refusals name only the origin. `runTokenCommand` keeps stdout in the capped buffer and sends stderr to `/dev/null`, and it never wraps an `exec` error, which would carry the tool path or argv. Every failure is an `accountReadError` with a fixed string from `keychainReasons`. The tests plant token and item-name text on both the fake's streams and assert neither appears in a log, an error or `status()`.
- [Tokens] No findings on lifecycle. There is no cache: every launch attempt runs the tool again, so rotating or deleting the item in the keychain applies at the next spawn, and a locked keyring or a denied prompt refuses that launch with no fallback to the inherited login.
- [Tokens] OUT OF SCOPE as a code check: Claude Code's own item, "Claude Code-credentials", holds the interactive login as compact JSON, and that JSON may pass `parseTokenBytes`. Naming that item would put the interactive login's credentials into the child's environment. The ticket makes this a docs rule, not a code check, so the warning is carried in the Documentation handoff for `docs/guide.md`. It stays inside the same user's trust boundary, since the child is `claude` run as the same user.
- [File operations] No findings. The source opens no file. Tests prepend a temporary directory to `PATH`, and the production code joins no path from input.
- [Subprocesses] No findings on injection. There is no shell: `exec.CommandContext(ctx, tool, args...)` gets the name as one argv element. `keychainCommand` fixes the tool and every other argument per platform, and an unknown GOOS is refused at startup. On darwin the name is the value of `-s`, so getopt would not read it as an option anyway. On linux it is positional to `secret-tool`'s GOption parser, which would read `-x` or `--help` as an option. So the leading-`-` refusal in `newClaudeAccount` is the injection guard there, and it is applied on both platforms. Control bytes, DEL included, are refused by `hasControlByte`.
- [Subprocesses] No findings on `PATH` lookup. Unlike `op_cli`, `security` and `secret-tool` cannot be configured to an absolute path, so they resolve through the daemon's `PATH`. `claude` also resolves through that `PATH` by default, through `-pyry-claude`, and receives the token. Anyone who can shadow `security` can already shadow the process the token goes to, so pinning the keychain tool alone would add nothing. Go's `exec.LookPath` refuses a match in the current directory.
- [Subprocesses] No findings on environment and lifetime. The child's environment is `withoutAccountToken(os.Environ())`. It has no `CLAUDE_CODE_OAUTH_TOKEN`, and it keeps `DBUS_SESSION_BUS_ADDRESS`, which `secret-tool` needs. There is no stdin. The tool runs in its own process group, which `cmd.Cancel` kills as a group, and `WaitDelay` bounds a descendant that holds the pipe. A keyring daemon that D-Bus activates belongs to the session bus, not to our group, so it is not killed. A macOS access prompt or a Linux unlock prompt blocks the read only until the ten-second per-attempt deadline, then the launch is refused with "keychain read timed out". The supervisor's restart backoff bounds how often a prompt can reappear.
- [Cryptography] No findings. The design generates, compares and stores no secret material, and the token passes through unchanged.
- [Network and I/O] No findings. Stdout is capped at `maxAccountTokenBytes+1` by `cappedBuffer`, and an oversized output is refused as invalid. Stderr is never read and there is no stdin.
- [Errors, logs, telemetry] No findings. The logged fields stay the accessor kind and a fixed reason. `os_keychain` maps to `protocol.ClaudeAccountKindOSKeychain` through `claudeAccountPayload` with no other field added.
- [Concurrency] No findings. Each read has one goroutine, which exits when `Wait` returns, at most `WaitDelay` after the group kill, and it sends on a buffered channel. `claudeAccountGOOS` is read once, while `newClaudeAccount` builds a keychain source, and only tests write it, serially, alongside `t.Setenv`. If the daemon is SIGKILLed mid-read the tool is orphaned and exits on its own, and the read writes no daemon state.
- [Threat model] OUT OF SCOPE: the accessor's last-finisher `status()` race belongs to #2816, as in #2825's review. The operating-system facts, such as a LaunchDaemon not reaching the login keychain and headless Linux keeping the keyring locked, are deployment guidance carried in the Documentation handoff and are not enforced in code.

**Reviewer:** builder (self-review per the security-review checklist)
**Date:** 2026-10-05

## Revisions

- 2026-10-05 (rework, verifier finding "plan has no `## Security review` section"): added the self-review above. It raises no MUST FIX or SHOULD FIX, so the design and the code are unchanged. Two terms in the Design section were named at build time and are recorded here: the reason set is the `commandReasons` type, and `opContextFailure` became `commandContextFailure`.
