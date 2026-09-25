# #2586 — Codex session settings per turn; Claude-only features unavailable

## Files read

- `cmd/pyry/codex_runner.go` → `codexRunner`, `WriteUserTurn`, `SetPermissionMode`, `SetSpawnPermissionMode`, `SetModel`, `setTurnSettings`, `codexTurnSettings`, `newCodexRunnerFactory`, `errCodexPostureFixed` — the runner whose posture is fixed today and whose per-turn overrides this ticket completes.
- `internal/codexsup/client.go` → `TurnInput`, `(*Client).StartTurn` — the `turn/start` encoder that gains the three posture fields.
- `internal/sessions/pool.go` → `Pool.UpdateSettings`, `inBandDeliverable`, `Pool.deliverSettingsInBand` — the live-apply path; the effort send is the one that must not reach Codex as a user turn.
- `internal/sessions/session.go` → `claudeSettingsArgs`, `canonicalPermissionMode`, `canonicalSettings` — the Claude composer this ticket adds a counterpart for; stored `YOLO` ⇔ `PermissionMode == bypassPermissions`.
- `internal/sessions/runnerstate.go` → `RunnerConfig.PermissionMode` — the construction-time posture the Codex factory currently drops.
- `internal/e2e/internal/fakecodex/main.go` → `turnStart` — the fake has no way to show the `turn/start` params it received.
- `cmd/pyry/main.go` → `resolveBoundMCPStatus`, `resolveBoundEffectiveEffort`; `cmd/pyry/mcp_actuate_v2.go` → `boundMCPChildActuator`; `cmd/pyry/relay_context_usage.go` → `contextUsageResolve`; `cmd/pyry/session_slash_command_list.go` → `resolveBoundSlashCommandList` — the five Claude-only seams, each a structural type assertion `codexRunner` fails.
- `cmd/pyry/session_mcp_status_test.go` → `newMCPStatusQueryTestPool` — the pool-with-custom-factory fixture shape the Claude-only test mirrors.
- `internal/sessions/pool_update_settings_inband_test.go` → `TestPool_UpdateSettings_InBand_ModelAndEffort`, `TestPool_DeliverSettingsInBand_EnableWritesTheEscalation` — the Claude `/effort` pin that must stay green unchanged.
- Codex v2 schema `TurnStartParams` (generated from the locally installed `codex-cli 0.155.0-alpha.9.2`; 0.156.1 could not be fetched — npm's release-age policy refuses it). Confirmed: `approvalPolicy` is `"on-request"`/`"never"`/`"untrusted"` or `{"granular":{sandbox_approval,rules,mcp_elicitations[,request_permissions,skill_approval]}}`; `approvalsReviewer` is `"user"`/`"auto_review"`; `sandboxPolicy` is `{"type":"readOnly"|"workspaceWrite"|"dangerFullAccess"|…}` with every other field defaulted. All three are "for this turn and subsequent turns".

## Context

ADR 038 fixed every Codex session at the read-only `config.toml` posture. The operator's posture choice is persisted but never applied, and a live effort change arrives at Codex as the user turn `/effort <level>`. Codex accepts model, effort, approval policy, sandbox and reviewer on each `turn/start`, so all five can be applied per turn with no respawn.

ADR 038 is superseded in part (per-turn overrides now apply the stored posture; the daemon-owned home, the default decline and the `config.toml` baseline before the first turn still stand). The documentation stage owns that ADR edit — see the handoff below.

## Design

### 1. `codexsup.TurnInput` carries the posture (`internal/codexsup/client.go`)

`TurnInput` gains three string fields, each empty = omitted from the wire:

- `ApprovalPolicy` — `ApprovalGranular` | `ApprovalOnRequest` (`"on-request"`) | `ApprovalNever` (`"never"`)
- `Sandbox` — `SandboxReadOnly` (`"readOnly"`) | `SandboxWorkspaceWrite` (`"workspaceWrite"`) | `SandboxDangerFullAccess` (`"dangerFullAccess"`)
- `ApprovalsReviewer` — `ReviewerUser` (`"user"`) | `ReviewerAutoReview` (`"auto_review"`)

Exported string constants name every value (no new types). `StartTurn` encodes them as `approvalPolicy`, `sandboxPolicy` (`{"type": <Sandbox>}`) and `approvalsReviewer`. `ApprovalGranular` is encoded as the object `{"granular":{"sandbox_approval":true,"rules":true,"mcp_elicitations":true}}`; any other approval value is sent as its string. With the three fields empty the params are byte-identical to today (`TestRequestParamShapes`' existing literal stays).

### 2. The composer (`cmd/pyry/codex_settings.go`, new)

```go
// codexTurnOverrides is claudeSettingsArgs' counterpart for a Codex session.
func codexTurnOverrides(s sessions.SessionSettings) codexsup.TurnInput
```

Returns `Model`, `Effort` passed through, and the full posture from this table (Text left empty):

| Stored posture | ApprovalPolicy | Sandbox | Reviewer |
|---|---|---|---|
| `YOLO` true, or `bypassPermissions` | never | dangerFullAccess | user |
| `default`, `acceptEdits` | granular | workspaceWrite | user |
| `plan` | granular | readOnly | user |
| `auto` | on-request | workspaceWrite | auto_review |
| `dontAsk` | never | workspaceWrite | user |
| anything else, `""` included | granular | readOnly | user |

The YOLO check comes first, mirroring `claudeSettingsArgs` where the bit is the authoritative half of the posture. The posture is ALWAYS non-empty, so every turn re-asserts it; that is what stops a sticky looser override from surviving a tightening.

### 3. The runner (`cmd/pyry/codex_runner.go`)

- `codexRunnerConfig` gains `PermissionMode`; the factory fills it from `RunnerConfig.PermissionMode` (today dropped).
- `codexRunner` gains `mode string` beside `model`/`effort`, under `mu`. A small `settings()` helper returns `sessions.SessionSettings{Model, Effort, PermissionMode: mode, YOLO: mode == sessions.PermissionModeBypass}` — the runner learns the posture only as a mode, and the pool's own derivation makes the bit and the mode agree.
- `WriteUserTurn` builds `codexTurnOverrides(settings)`, sets `Text`, and passes it to `StartTurn`. Gates unchanged.
- `SetSpawnPermissionMode(mode)` and `SetPermissionMode(mode) error` both store `mode` (the latter returns nil). `errCodexPostureFixed` is deleted.
- New `SetEffort(effort string) error` stores `effort`, returns nil — the in-band effort hook below.
- Doc comments on the factory, `Restart`, and the type are updated: `config.toml` is the baseline before the first turn, every turn asserts the stored posture.

### 4. The in-band effort path (`internal/sessions/pool.go`)

An unexported consumer-side interface in `pool.go`:

```go
type effortSetter interface{ SetEffort(effort string) error }
```

`deliverSettingsInBand`'s effort clause: if `sup` implements `effortSetter`, call `SetEffort(*update.Effort)` (error → `notDelivered("effort", err)`); otherwise the existing `send("effort", "/effort "+…)`. Claude's runner does not implement it, so Claude's delivery is the DEFAULT branch — a new runner that forgets the method gets the visible `/effort` turn, never a silent skip. Model and posture need nothing new: `SetModel` and `SetPermissionMode` already reach the runner and now both stick.

Every live change to a non-empty value therefore sends nothing to Codex and respawns nothing (`inBandDeliverable` is unchanged; `SetSpawnArgs` re-reads model/effort from the argv, and the next `WriteUserTurn` carries all five). A model or effort *cleared* to `""` still takes the `Restart` branch — out of this ticket's AC (see Open questions).

### 5. The fake Codex (`internal/e2e/internal/fakecodex/main.go`)

New env `FAKECODEX_TURN_LOG`: when set, `turnStart` appends each request's raw params as one line to that file before answering. Documented in the header's configuration list.

### Claude-only features

No code change: `codexRunner` implements none of `mcpStatusQuerier`, `mcpChildActuator`, `contextUsageQuerier`, `effectiveEffortQuerier` or the slash-command lister, so each seam falls through to its refusal. A test pins it.

## Concurrency model

No new goroutines. `mode`, `model`, `effort` are read as one snapshot under `r.mu` in `WriteUserTurn`, as today; `mu` stays a leaf. A setter racing a turn gives that turn either the old or the new posture, never a mix; any turn started after `UpdateSettings` returns carries the new one, because every setter runs synchronously inside it.

## Error handling

`SetEffort`, `SetPermissionMode` never fail (the pool gates the vocabulary through `permissionModeKnown`; an unknown mode reaching the composer maps to read-only). `StartTurn` errors are wrapped as today. No log line carries a model, effort or mode (#833).

## Testing strategy

- `cmd/pyry/codex_settings_test.go` — `TestCodexTurnOverrides`: one row per table line, including `acceptEdits`, YOLO with a contradictory mode, `""`, and an unknown mode; model/effort pass through.
- `internal/codexsup/client_test.go` — extend `TestRequestParamShapes` (or a sibling) with a posture turn: granular object, `sandboxPolicy {"type":"readOnly"}`, `approvalsReviewer`; plus `"never"` + `dangerFullAccess` string form. The existing exact-literal case pins "empty fields omitted".
- `internal/sessions/pool_update_settings_inband_test.go` — `TestPool_DeliverSettingsInBand_EffortSetterSkipsTheTurn`: a double embedding `runnerDouble` plus `SetEffort` receives the effort and zero user turns. Existing `/effort high` test stays unchanged (the Claude pin).
- `cmd/pyry/codex_runner_test.go` — adapt `TestCodexRunner_RestartResumesAndInstallsSettings`'s `SetPermissionMode` assertion (now nil). New `TestCodexRunner_LiveSettingsReachNextTurn`: a `sessions.Pool` whose factory builds a `codexRunner` on the fake with `FAKECODEX_TURN_LOG`; after a first turn, `UpdateSettings(model, effort, plan)` adds no `turn/start` and keeps the same bound client and `RestartCount`; the next turn's params carry the new model/effort and granular/readOnly/user; then a YOLO enable yields never/dangerFullAccess on the following turn. Also asserts the first turn carries the construction-time `default` posture (factory wiring of `PermissionMode`).
- `cmd/pyry/codex_claude_only_test.go` — pool whose factory returns an unstarted `codexRunner`; bind a conversation; the five resolvers each return their refusal (context usage: nil querier, true) within a deadline context.

## Open questions

- A model or effort cleared to `""` restarts the Codex runner and then omits the field; Codex's thread keeps the previous value. Not in the AC ("non-empty value"); left for the effort-vocabulary ticket (#2588) to decide.
- Schema confirmed against 0.155.0-alpha.9.2, not 0.156.1 (unfetchable here). The live gate (`needs-real-claude`) is the check for 0.156.1.

## Documentation handoff (pending — documentation stage)

- `docs/knowledge/features/codexsup-package.md` § Production wiring: replace "`SetPermissionMode` always errors" and "the posture lives only in `config.toml`" with the per-turn posture mapping (table above).
- ADR 038: per-turn overrides now apply the stored posture, superseding its fixed read-only posture; the daemon-owned home and the default decline still stand, and `config.toml` remains the baseline before the first turn.

## Security review

**Verdict:** PASS

**Findings:**

- [Trust boundaries] No findings — the posture reaching the runner is already gated upstream (internal/relay's `validPermissionMode` admits no `bypassPermissions` string; `Pool.UpdateSettings` gates through `permissionModeKnown`). `codexTurnOverrides` is the single mapping point and fails closed: any unrecognised or empty mode maps to granular + readOnly. Model and effort pass through as JSON string values exactly as #2620 already sent them; nothing reaches an argv or a path.
- [Trust boundaries] Accepted by product contract — the `default` row maps to workspaceWrite, which lets the model write inside the workspace without asking, looser than Claude's `default`. The table is the ticket's AC; recorded here so the verifier sees it was a decision, not an accident.
- [Concurrency] Accepted limitation, documented — a posture tightening (e.g. a YOLO revoke) lands on the NEXT `turn/start`; a turn already running keeps the sandbox it started with. The operator's `interrupt` verb ends it. Every turn re-asserts the full posture, so a looser sticky override never outlives the next turn. Carried into the documentation handoff so the codexsup overview states it.
- [Concurrency] No findings — `mode`/`model`/`effort` are one snapshot under the leaf `codexRunner.mu`; every setter runs synchronously inside `Pool.UpdateSettings`, so a turn started after it returns carries the new posture. No new goroutines or locks.
- [Concurrency] No findings — a respawn (`thread/resume`) or `RestartFresh` (`thread/start`) runs no turn before the next `WriteUserTurn`, which re-asserts the posture; the `config.toml` read-only baseline covers the window before it.
- [Fail-closed wiring] SHOULD FIX (Phase B) — the factory currently drops `RunnerConfig.PermissionMode`. Threading it is required; if missed, the session stays read-only (fail-closed, not an escalation). `TestCodexRunner_LiveSettingsReachNextTurn` asserts the first turn carries the construction-time `default` posture.
- [Tokens, secrets] No findings — no credential is read, stored or logged. The fake's `FAKECODEX_TURN_LOG` file holds turn text; it is test-only, env-gated, and written at 0600.
- [File operations] No findings — production file handling is unchanged (`prepareCodexHome` untouched). The fake's log path comes from the test's own env.
- [Subprocess] No findings — no new argv; the environment passed to `codex app-server` is unchanged. The real Codex ignores `FAKECODEX_TURN_LOG`.
- [Crypto] Not applicable — no randomness, keys or comparisons are introduced.
- [Network & I/O] No findings — the only new bytes are three bounded fields on an outbound request; nothing new is read.
- [Logs] No findings — `SetEffort`, `SetPermissionMode` and `SetSpawnPermissionMode` log nothing and return no error carrying a value; `WriteUserTurn`'s existing logs name no setting (#833).
- [Threat model] No findings — the escalation keeps exactly one wire spelling (the YOLO bit); `codexTurnOverrides` checks the bit before the mode, mirroring `claudeSettingsArgs`. Approval requests produced by the granular policy still take codexsup's default decline until #2587 (OUT OF SCOPE here, owned by #2587). The `auto` row routes approvals to Codex's `auto_review` reviewer by the table's design.

**Reviewer:** builder (self-review per the security-review checklist)
**Date:** 2026-09-25

## Revisions

- 2026-09-25 (Phase B): the runner helper the Design calls `settings()` is named `turnSettings()` (it returns the settings every turn asserts; `settings` read as a field). The codexsup posture test is the sibling `TestTurnPostureShapes`, not an extension of `TestRequestParamShapes`, whose exact literal stays as the "empty fields omitted" pin. No contract changed.
