# #2668 — Codex command and file-change rows carry a tool name and a subject

## Files read

- `internal/codexsup/translate.go` → `commandItem`, `fileChangeItem`, `maxToolTitle`, `cut` — the two ToolStart builders this changes.
- `internal/codexsup/translate_test.go` → `TestTranslateHandBuilt` (`command_failed`, `file_change` cases), `TestTranslateToolItemEdges` — existing assertions on the titles and inputs.
- `internal/turnbridge/outbound.go` → `MapEvent` ToolStart arm, `inputSummary`, `inputFields` — `Title` becomes `tool_use.name`, `RawInput` becomes both `input_summary` (compact JSON) and `input` (top-level fields). `Locations` is not carried.

## Change

`commandItem` titles its ToolStart `shell` instead of the command, keeping the `{"command","cwd"}` RawInput, so a client shows the command once, as the subject. With the title a constant, `maxToolTitle` and its `cut` call have nothing to bound and are removed, with the title-cap assertion in `TestTranslateToolItemEdges`. The command is still unbounded in RawInput, as it already was; turnbridge's `inputFields` and `inputSummary` cap it on the wire.

`fileChangeItem` keeps its `apply_patch` title and its Locations, and adds a RawInput of `{"paths":"<every changed path joined by \", \">"}`, so `input.paths` and `input_summary` name the files. No other consumer reads either title: `cmd/pyry/codex_runner.go` hands the events straight to turnbridge.

No in-flight feature branch touches `internal/codexsup/translate*`.

## Testing strategy

- Update `TestTranslateHandBuilt`'s `command_failed` title to `shell` and give each `file_change` ToolStart its `paths` RawInput (patch-1 carries two paths, so the ", " join is covered).
- Add one wire-level test in `internal/codexsup` that replays those two hand-built fixtures through `turnbridge.MapEvent` and asserts the `tool_use` payload: name `shell` with `command` and `cwd` in `Input`; name `apply_patch` with `paths` in `Input` and the paths inside `InputSummary`. `turnbridge` depends only on `protocol` and `turnevent`, so the test import makes no cycle.

## Documentation handoff

None named by the ticket. Pending for the documentation stage: `docs/knowledge/features/codexsup-package.md`, the `commandExecution` and `fileChange` paragraph, describes the ToolStart as `RawInput` `{command, cwd}` for a command and Locations plus the `apply_patch` title for a file change. It should add the `shell` title for a command and the `{paths}` RawInput for a file change.
