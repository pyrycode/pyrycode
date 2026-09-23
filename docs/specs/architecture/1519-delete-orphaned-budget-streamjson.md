# #1519 — delete `agentrun/budget`, `agentrun/streamjson`, `IsNewLogicalTurn`

Short plan: a deletion, one assertion change, and comment repairs. No new type,
state or failure mode.

## Files read

- `internal/agentrun/budget/`, `internal/agentrun/streamjson/`, `internal/agentrun/turncount.go` (`IsNewLogicalTurn`) — deleted whole. Only importer outside their own trees: `trailer_admissibility_test.go` (`TestTrailBudgetTerminalReasonIsTheShippedWireValue`). `IsNewLogicalTurn`'s only callers are `budget` and `streamjson`.
- `internal/agentrun/streamrunner/watchdog.go` → `idleStallResult` doc — names `streamjson.Emitter` as the shape it mirrors.
- `internal/attachments/registry.go` → `newRegistryWithClock` doc — cites "streamjson's emitter constructor" as a precedent.
- `internal/streamsup/result_stop_shape_test.go` → `stopShapeCaptureName` doc — "no internal/agentrun/streamjson emitter".
- `internal/e2e/realclaude/trailer_admissibility_test.go` → `trailBudgetTerminalReason` (doc), `TestTrailBudgetTerminalReasonIsTheShippedWireValue`, the `streamjson` import.
- `internal/e2e/realclaude/budget_test.go` → file header, `maxTurnsPrompt` doc, `TestRealClaude_MaxTurnsHonored` — the live producer's test.
- `internal/e2e/realclaude/result_trailer_observation_test.go` → `trailFixtureTrailer` doc.
- `internal/e2e/realclaude/finding_run_gather_test.go` → `finGatherNeedleTrailer` doc.
- `internal/e2e/realclaude/tool_loop_test.go` → `resultTrailer` doc.
- `internal/e2e/realclaude/trailer_key_names_test.go` → file header § What the fixed decode cannot answer.
- `internal/e2e/realclaude/trailer_terminal_reason_test.go` → file header ptyrunner bullet, `trailReasonBlankOwesOne` doc.
- `internal/e2e/realclaude/finding_stream_exit_path_probe_test.go` → file header § What this path's evidence cannot say.

## Change

1. **Delete** the three trees (`budget/`, `streamjson/` incl. `testdata/`, `turncount.go` + `turncount_test.go`).
2. **`TestRealClaude_MaxTurnsHonored`** compares `trailer.TerminalReason` against `trailBudgetTerminalReason` (same package, same build tag) and its failure message names the constant, so a value change in the constant reddens a live budget-fired run. `maxTurnsPrompt`'s doc names the constant rather than the bare literal.
3. **Delete `TestTrailBudgetTerminalReasonIsTheShippedWireValue`** and the `streamjson` import (plus `bytes` if it becomes unused). Rewrite `trailBudgetTerminalReason`'s doc: the value is claude's own `terminal_reason` on a `--max-turns` stop (#1388) — `streamrunner.Run` forwards claude's trailer bytes unchanged and its only synthesised trailer (`idleStallResult`) says `idle_stall` — and `TestRealClaude_MaxTurnsHonored` pins it against a live run. The emptiness check in the neighbouring test's comment that points at "the constant's own comment for the rename risk" still reads correctly and stays.
4. **Comment repairs** (each repaired, not deleted):
   - `idleStallResult`: its field names, tags and order were chosen to match the trailer the since-deleted (#1348) `streamjson` emitter wrote; phrase without the `streamjson[./]` spelling. It is now the only pyry-synthesised trailer on the shipping path.
   - `newRegistryWithClock`: drop the dead precedent; `newStallTracker` stays as the precedent.
   - `stopShapeCaptureName`: "no pyry agent-run emitter anywhere on that path" (the point is the path is claude-direct).
   - `trailFixtureTrailer`, `finGatherNeedleTrailer`: wire order named as `idleStallResult`'s field order (`terminal_reason` last, so a capped line loses it first).
   - `resultTrailer`: mirrors the subset of claude's `result` line / `idleStallResult` under assertion.
   - `budget_test.go` header: the unit-side pins were deleted with the package in #1348/#1519; this live test is the pin.
   - `trailer_key_names_test.go`, `finding_stream_exit_path_probe_test.go`: fix the false "pyry invention" claim — claude writes `terminal_reason` on an error stop such as `--max-turns` (#1388) and omits it on a healthy run; pyry synthesises it only on the idle-stall trailer. The healthy-run conclusions each paragraph draws still hold.
   - `trailer_terminal_reason_test.go`: the ptyrunner bullet and `trailReasonBlankOwesOne` describe code deleted in #1348 (ptyrunner and its emitter's `Close` chokepoint) — say so, in past tense. Retiring the ptyrunner-path categories is out of scope.

Nothing else moves: `go list -deps ./cmd/...` names neither package, and no production code calls `IsNewLogicalTurn` outside them.

## Testing strategy

- Deletion: `go build ./...`, `go vet ./...`, `go vet -tags e2e_realclaude ./internal/e2e/realclaude/...` (the package `make check` cannot see); `go test -race` on `internal/agentrun/...`, `internal/attachments`, `internal/streamsup`; the offline `^TestTrail` subset of realclaude under the tag.
- AC2 is proven only by a live budget-fired run — the dispatcher's `needs-real-claude` gate runs `TestRealClaude_MaxTurnsHonored` after verification.
- The sweep regex from the ticket returns nothing under `internal/` or `cmd/`; `make cite-guard` clean on the repaired comments.

## Documentation handoff (pending — documentation stage)

- `docs/knowledge/features/agentrun-package.md`: stop documenting `IsNewLogicalTurn` as package API; drop the `budget` and `streamjson` rows and their importer bullets.
- `docs/knowledge/features/streamjson-package.md` and `budget-package.md`: keep both files; add a retired notice at the head of each naming #1519 and #1348.
- `docs/knowledge/INDEX.md`: adjust the summary lines for the two retired docs.
