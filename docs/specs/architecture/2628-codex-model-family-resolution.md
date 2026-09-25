# #2628 — a Codex session follows its model family on every turn

## Files read

- `cmd/pyry/codex_settings.go` → `codexTurnOverrides` — builds every turn's overrides; model passes through today.
- `cmd/pyry/codex_runner.go` → `codexHarness` (carries `vocab`), `newCodexRunnerFactory` (wires `Models: h.vocab.RetainCodex`), `codexRunnerConfig`, `codexRunner.WriteUserTurn` (hands `in.Model` to the translator's `SetModel` and sends the turn), `codexRunner.turnSettings`.
- `cmd/pyry/model_vocabulary_store.go` → `modelVocabularyStore.CodexModels` (deep copy, nil-receiver-safe, takes the store's leaf mutex), `RetainCodex` (empty list is a no-op).
- `internal/codexsup/translate.go` → `Translator.SetModel`, `holdUsage`, `turnCompleted` — the turn's `ModelWindows` are keyed by the model `SetModel` set, and only when a `thread/tokenUsage/updated` with a context window arrived for the turn.
- `internal/e2e/internal/fakecodex/main.go` → `turn.run`, marker constants — the fake sends no token usage, so today no fake turn reports a model.
- `cmd/pyry/codex_runner_test.go` → `TestCodexRunner_LiveSettingsReachNextTurn`, `readTurnLog`, `codexHarnessT` — the pool + factory + turn-log pattern the new test mirrors.
- `cmd/pyry/codex_model_list_test.go` → `fakeCodexFamilies` — the fake's families (`luna` → `gpt-6-luna`).

## Context

Slice S3d of Codex support. A session stores a family (`luna`); the daemon resolves it to the family's newest version from the Codex entries #2627 holds, on every `turn/start`, so a new release is picked up with no respawn. Claude and the dispatcher are out of scope.

## Design

**Resolution (pure, `codex_settings.go`).** `resolveCodexModel(model string, families []turnevent.ModelOption) string`: returns the `ResolvedModel` of the first entry whose `Value` equals `model` exactly and whose `ResolvedModel` is non-empty; otherwise returns `model` unchanged. An empty model is returned at once, so it stays empty. So a family with no held entries, an unlisted version, and anything else pass through; a model is never replaced by one that is not its family's resolution.

**Wiring (`codex_runner.go`).** `codexRunnerConfig` gains `Families func() []turnevent.ModelOption` — the Codex entries held now; nil holds none. `newCodexRunnerFactory` sets it to `h.vocab.CodexModels` (a method value on a nil store is safe, as `RetainCodex` already is). `WriteUserTurn`, after releasing `r.mu` and before the translator update, sets `in.Model = resolveCodexModel(in.Model, families)` where `families` is `r.cfg.Families()` when set. The resolved model is what both `SetModel` and `StartTurn` get, so the reported model equals the sent model. `r.model` (the stored setting) is never written, so the session keeps the family.

Resolving per call to `WriteUserTurn` (not at construction) is what lets the second turn after a `RetainCodex` carry the newer version.

**Fake Codex (`internal/e2e/internal/fakecodex/main.go`, test infrastructure).** A new marker `[fakecodex:usage]`: before completing the turn, the fake sends one `thread/tokenUsage/updated` for the turn with small non-zero `total`/`last` counts and a `modelContextWindow`. The translator then keys the turn's `TurnEnd.ModelWindows` by the model `SetModel` set, which is how a test observes the model the turn reports. Opt-in by marker so no existing turn's `TurnEnd` changes. Documented in the fake's header comment beside the other markers.

## Concurrency model

No new goroutines. `CodexModels` takes the store's leaf mutex; it is called with neither `r.mu` nor `r.trMu` held, so it adds no lock-order edge.

## Error handling

None new: resolution cannot fail; an absent or empty list is pass-through.

## Testing strategy

- `codex_settings_test.go`: table test for `resolveCodexModel` — family resolves; unlisted version (`gpt-5.6-sol`) unchanged; family with nil and with empty list unchanged; empty model unchanged; an entry with an empty `ResolvedModel` does not replace the model.
- `codex_runner_test.go`: through `sessions.New` and `newCodexRunnerFactory` with a real `modelVocabularyStore`, reading `FAKECODEX_TURN_LOG`, with `[fakecodex:usage]` turns so `TurnEnd.ModelWindows[0].ModelID` is observable (the sink records the turn ends):
  1. stored model `luna`, store holds the fake's families → turn/start `model` is `gpt-6-luna`, turn reports `gpt-6-luna`;
  2. `RetainCodex` moves `luna` to `gpt-6.1-luna` → the next turn carries and reports `gpt-6.1-luna`, same client, no restart; the pool's stored model is still `luna`;
  3. stored model set to `gpt-5.6-sol` → sent and reported unchanged.
  The factory's own spawn refreshes the store via `readModels`; the test waits for that read to land before seeding its own entries so the refresh cannot overwrite them mid-test.

RED first: case 1 fails today because the model is sent as `luna`.

## Open questions

- Whether the spawn-time `RetainCodex` can race the test's `RetainCodex` — resolved in the test by waiting for the spawn's read (store non-nil) before retaining.

## Documentation handoff

Pending for the documentation stage: the Codex model-family behaviour (a stored family resolves to the newest held version on every `turn/start`; unlisted models and an empty store pass through) belongs in the Codex section of the owning package overview under `docs/knowledge/features/`. The fake Codex's new `[fakecodex:usage]` marker is documented in its own header comment.
