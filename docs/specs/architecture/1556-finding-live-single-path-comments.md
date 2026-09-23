# #1556 — rewrite the five finding_live_* passages that argue from a stream-vs-pty comparison

Short plan: comment-only change in two `e2e_realclaude`-tagged test files, no new type, state or failure mode.

## Files read

- `internal/e2e/realclaude/finding_live_staging_test.go` → doc of `finLiveStageStreamEnvDelta` (the `PYRY_USE_STREAMJSON=1` bullet and `# Choosing this delta chooses a permission posture`). Edited.
- `internal/e2e/realclaude/finding_live_run_test.go` → doc of `finLiveRunStage` (SITE F, SITE G, the transcript-reads bullet under "What the delta does NOT change") and SITE B / LEG 1 in its body. Edited.
- `cmd/pyry/agent_run.go` → `runAgentRun` (no longer reads `PYRY_USE_STREAMJSON`), `runAgentRunStreamRunner` (writes the deny-default settings file via `settingsWrite`, sets `WorkDir: parsed.workdir`, `Stderr: os.Stderr`), `buildStreamRunnerClaudeArgs` (yolo=true passes no `PermissionArgs`). Source of truth, not edited.
- `internal/agentrun/streamrunner/args.go` → `BuildClaudeArgs`: with no `PermissionArgs` it emits `--permission-mode dontAsk`, then `--settings`, and never `--session-id`. Its doc records `--settings` as the boundary and `--allowed-tools` as not the enforcement.
- `internal/agentrun/streamrunner/runner.go` → `Run`: `cmd.Dir = cfg.WorkDir`, `cmd.Stderr = cfg.Stderr`.
- `cmd/pyry/agent_run_test.go` → `TestBuildStreamRunnerClaudeArgs_Shape` pins `--permission-mode dontAsk`; the "went with the terminal path in #1348" wording is the historical-marking model.

## Change

Rewrite five comment passages so each states its conclusion for the one remaining agent-run path, with any pty term kept only as marked history ("the terminal path deleted in #1348"):

1. **`PYRY_USE_STREAMJSON=1` bullet** — only the production-dispatch clause changes: production no longer reads the variable; the reason for naming it explicitly (`reachRunnerPathFromEnv` reads the ambient first) stays verbatim.
2. **Posture twins** (staging `# Choosing this delta…` and SITE F) — identical statement in both: neither delta chooses a production posture; agent-run has one posture, and the per-spawn deny-default `--settings` file written by `runAgentRunStreamRunner` from `--allowed-tools` is the only enforcement. The rig's `--allowed-tools=Bash` (`spawnProbePyry`) bounds the surface through that file, not through the flag. No text calls `--allowed-tools` the gate.
3. **SITE G** — claude mints the session id because the stream argv carries no `--session-id`; the rig reads it unvalidated; the no-guard decision stays.
4. **ONE ASYMMETRY bullet** — claude gets the raw `parsed.workdir` (`streamrunner` `cmd.Dir`), which is what the rig derives; no asymmetry, no a-fortiori step. The 2026-08-06 green run is labelled a terminal-path run.
5. **LEG 1** — claude's fd 1 is a pyry-created pipe; fd 2 is pyry's stderr because `runAgentRunStreamRunner` sets `Stderr: os.Stderr`, so stderr is the one fd claude shares with the rig. The 2026-08-06 evidence is labelled terminal-path; its fd-2 conclusion transfers because stderr was pyry's on that path too.

Nothing else moves: return literals, the `=0` bullet, and the out-of-scope passages the ticket lists (SITE C/D, TWO DELTAS, `probeWaitForDirectChild`, cleanup topology, LEG 2, "THE 3 IS RE-DERIVED") are untouched.

**Deviation from the ticket's AC wording, decided on the code.** AC 2 names the one posture as `--dangerously-skip-permissions`. The code says otherwise: `buildStreamRunnerClaudeArgs` passes no `PermissionArgs` when yolo=true, so `BuildClaudeArgs` emits `--permission-mode dontAsk`, and `TestBuildStreamRunnerClaudeArgs_Shape` pins that. Writing the skip flag would make the comment false, which defeats the ticket's goal that each conclusion be checkable against the code. The passages therefore name `--permission-mode dontAsk` plus the `--settings` file as the posture. `buildStreamRunnerClaudeArgs`'s own doc still claims yolo=true emits the skip flag; that stale production comment is already tracked by #1515 and is out of scope here.

## Testing strategy

No new logic, no new test. Proof is the ticket's mechanical checks: the `runAgentRunPty` git-grep (two out-of-scope `teardown_*` hits remain), the comment-only diff filter, `go vet -tags e2e_realclaude ./internal/e2e/realclaude/`, `go build -tags e2e_realclaude ./...`, and `go test -tags e2e_realclaude -run 'TestFinLiveStage.*EnvDeltaNamesTheRunner' ./internal/e2e/realclaude/` showing 2 `=== RUN` lines.

## Documentation handoff

None — the ticket carries no documentation requirement.
