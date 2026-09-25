# #2666 — a Codex session with no model entry offers only Codex effort levels

## Files read

- `cmd/pyry/main.go` → `fallbackEffortLevels`, `effortLevelsFor`, `validateEffortVocabulary`, `settingsUpdaterAdapter.UpdateSettings`, `settingsUpdaterAdapter.Capabilities` — the fallback and its two readers; both readers already hold the session's harness from `HarnessFor`.
- `cmd/pyry/session_model_list.go` → `agentModelVocabulary` — for Codex, `have` is exactly "at least one family held", and `list.Models` is the held families.
- `cmd/pyry/codex_settings.go` → `codexTurnOverrides` — sends the stored effort on `turn/start` unfiltered, which is why an over-wide fallback fails the next turn.
- `cmd/pyry/settings_agent_vocabulary_test.go`, `cmd/pyry/session_capabilities_test.go` — the tables whose Codex fallback rows change.
- `docs/knowledge/decisions/039-capability-belongs-to-agent-and-model-together.md` § Rationale — "the fallback set for an unmatched model is deliberately still shared"; this ticket supersedes that for Codex.

## Change

`effortLevelsFor` and `validateEffortVocabulary` gain a leading `harness string` parameter. The matched-entry path is unchanged. When no usable entry matches, a Claude session (and any other harness) still gets `fallbackEffortLevels`, the five; a Codex session gets a new `codexCommonEffortLevels(list.Models)`: the levels of the first held family, in its order, kept only when every other held family's `EffortLevels` also names them, and nil when no family is held (`!have`). Intersecting is conservative under truncation: a cut level list can only shrink the result, never widen it. `UpdateSettings` and `Capabilities` pass the harness they already resolved, so the reported list and the accepted set stay one function. No other caller exists (`validateEffortVocabulary` has one production caller plus its test).

Overlaps: no in-flight branch touches these files.

## Testing strategy

- `TestValidateEffortVocabulary` gains a harness column. Claude rows keep their expectations (the fallback five). The Codex fallback rows change: unlisted/empty Codex model accepts `medium` (common to luna and sol), refuses `xhigh` and `ultra`; Codex with no families held refuses every non-empty level and still clears on empty.
- `TestSettingsUpdaterAdapter_EffortFollowsModel` Codex subtests and `TestSettingsUpdaterAdapter_Capabilities` Codex fallback rows move from the fallback five to `[low medium]`, and to no levels when no family is held. `TestSettingsUpdaterAdapter_CapabilitiesAreAccepted` keeps proving listed ⊆ accepted.

## Documentation handoff (pending, documentation stage)

- `docs/knowledge/decisions/039-capability-belongs-to-agent-and-model-together.md`, § Decision and § Rationale: the shared fixed fallback now applies to Claude only; a Codex model with no entry falls back to the levels every held Codex family advertises (first family's order), none when no family is held (#2666).
