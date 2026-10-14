# Daemon-wide Claude settings (`-pyry-claude-settings`)

`cmd/pyry/claude_settings.go` applies one operator-chosen Claude settings file to every interactive Claude child an instance starts, whatever folder its conversation runs in. User-facing behaviour is in [`guide.md` § Daemon-wide Claude settings](../../guide.md#daemon-wide-claude-settings); this page covers the design and the facts it rests on.

## Shape

- `newClaudeSettings(flagValue, envValue, instanceDir, logger)` resolves the file: the `-pyry-claude-settings` flag, then `$PYRY_CLAUDE_SETTINGS`, then `<instanceDir>/claude-settings.json` when it exists. The order follows [Claude account source](claude-account-source.md). A flag or env value that is not one absolute path, or carries a control byte, is a startup error naming its origin. Nothing set returns a nil `*claudeSettings`, whose `composer()` is nil, so argv is untouched. It also removes `<instanceDir>/claude-settings-composed/`, which is safe because no child of this daemon exists yet.
- `streamsup.Config.SettingsComposer` is the seam. `spawnAndWait` calls `composeSettingsArgs` on every launch attempt, before the account token read: it finds the last `--settings` value in the attempt's argv (two-token or joined form), asks the composer for a replacement, and appends `--settings <value>` when there was none. An error refuses the launch through the same setup-failure path a failed token read takes: `started == false`, backoff, retry, `OnCrashLoop` once refusals repeat, and the log line `claude launch refused`.
- `(*claudeSettings).compose(current)` re-reads the operator file each time (`readSettingsObject`: regular file, owned by the effective UID, no `0o022` bit, at most 1 MiB, one JSON object in valid UTF-8). With `current == ""` it returns the operator path itself. Otherwise it reads the session's file (or inline JSON), merges them with `mergeSettings`, and writes the result atomically, mode 0600, to `claude-settings-composed/<sha256(current)[:8] hex>.json`. One file per session settings file, rewritten in place on every launch.
- `mergeSettings(operator, session)`: `hooks` merge per event, the operator's matcher groups first and the session's appended (`mergeHooks`; either side's `hooks` must be an object of arrays or the launch is refused). Any other key on both sides takes the session's value, because the session's keys are what keep a child off unanswerable startup modals, and `compose` logs a warning naming those keys. Today the session file holds only `enableAllProjectMcpServers` and `skipDangerousModePermissionPrompt`; the per-event hook merge keeps the operator's hooks alive if the daemon ever adds session hooks.
- `runSupervisor` wires it as `streamApprovalConfig.settings`, and `newStreamRunnerFactory` sets `scfg.SettingsComposer`. Codex children and the reply-suggestion fallback (`replyFallbackArgs`, which passes `--setting-sources ""` and its own inline `--settings` with hooks disabled) are deliberately excluded.

## Claude keeps only the last `--settings`

Measured 2026-10-14 against Claude Code 2.1.289: two `--settings` files each wiring a `SessionStart` and `UserPromptSubmit` hook, passed in one argv, ran only the second file's hooks. Every pool session already carries `--settings <session-settings/<id>.json>` (#943's MCP enablement switches), so adding the operator file as a second flag would have silently dropped one of the two. That is why the daemon merges into one file instead. If a later Claude release starts layering repeated `--settings`, the merge is still correct, only redundant.

## Identical hook commands run once

Measured the same day: a project `.claude/settings.json` and a `--settings` file defining the same `UserPromptSubmit` command ran it once, even with different `timeout` values; two different commands both ran. So wiring a hook in both a workspace's settings and the daemon file does not double it.

## Why per-launch, and why the trust check

The file is read on every launch for the account token's reason: an edit applies to the next Claude start without a restart, and a file that has gone missing never lets Claude start without the hooks the operator relies on. The ownership and write-bit check matches `claude-account.json`'s: a settings file's hooks run commands as the daemon user, so a file another local user could write would be code execution as the operator.

## Operational gotchas

- **umask 0002.** On hosts with per-user groups the default umask makes a new file 0664, and the `0o022` check refuses it as group writable. Hit while preparing pyrybox's file on 2026-10-14; `chmod 600` fixes it.
- **The default file is resolved once.** `~/.pyry/<name>/claude-settings.json` is chosen at startup only if it exists. Deleting it while the daemon runs does not turn the option off: every launch is refused as missing until the file is back or the daemon restarts.
