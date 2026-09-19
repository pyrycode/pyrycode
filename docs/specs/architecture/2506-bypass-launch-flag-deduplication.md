# 2506 — Emit the bypass launch flag only once

## Files read

- `internal/sessions/session.go` → `operatorBypass`, `claudeSettingsArgs`, `Session.spawnArgs` — defines bypass provenance, the unconditional settings suffix, and live argv recomposition.
- `internal/sessions/pool.go` → `New`, `Pool.buildSession`, `Pool.UpdateSettings` — contains both initial argv construction paths and the live settings path that calls `Session.spawnArgs`.
- `internal/sessions/runner_config_posture_test.go` → `TestRunnerConfigOperatorBypass`, `TestSpawnArgsOperatorBypassSurvivesRecompose` — pins provenance across construction and recomposition.
- `docs/knowledge/features/sessions-package-key-types-sessionsettings-claudesettingsargs.md` → `SessionSettings` + `claudeSettingsArgs` — records why the capability flag is unconditional and why provenance must still be read from the settings-free base.
- `docs/knowledge/features/development-verification.md` → “Prove that tests distinguish the change” — requires fixtures that exercise each branch rather than assertions satisfied by mere flag presence.

## Change

Add one unexported final-composition helper that combines a settings-free base with `claudeSettingsArgs`, keeps the first exact `bypassPermissionsArg` token in its original order, and omits later copies. Use it in `New`, `Pool.buildSession`, and `Session.spawnArgs`, so bootstrap, minted, revived, and recomposed argv all share the same rule. The helper returns a fresh slice and never mutates `spawnBase`; `operatorBypass` therefore continues to read the operator-provided base, while model, effort, requested permission mode, and unrelated arguments retain their order. `skipDangerousModePermissionPrompt` remains settings-file content and is outside this exact-token argv rule.

## Testing strategy

- Extend `TestRunnerConfigOperatorBypass` with repeated operator flags and require exactly one bypass token on every initially constructed runner while retaining the original provenance answer.
- Extend `TestSpawnArgsOperatorBypassSurvivesRecompose` across bases containing zero, one, and repeated bypass tokens; assert one final flag, an unchanged base slice, retained unrelated arguments, and the requested permission-mode pair.
- Run the focused tests red before production changes, then run the `internal/sessions` race suite. The existing `Pool.UpdateSettings` tests continue to cover both live install branches.

## Documentation handoff

None. The ticket has no documentation acceptance criterion; the later documentation stage may update the sessions package note that currently describes duplicate flags as a cosmetic edge case.

## Revisions

None.
