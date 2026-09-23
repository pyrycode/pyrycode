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

## Security review

**Verdict:** PASS

The change is comment-only, so no category can gain an exploitable code path. The risk is a comment that misstates the agent-run permission posture: a maintainer who trusts it believes the wrong thing about the tool gate. The review walks each category against that risk.

**Findings:**

- [Trust boundaries / posture] No findings. The rewritten posture twins, in the doc of `finLiveStageStreamEnvDelta` and in SITE F of `finLiveRunStage`, name `--permission-mode dontAsk` plus the per-spawn deny-default `--settings` file as the only enforcement. They do not call `--dangerously-skip-permissions` the posture or `--allowed-tools` the gate. Checked against the code: `buildStreamRunnerClaudeArgs` passes no `PermissionArgs` when yolo=true, `streamrunner.BuildClaudeArgs` then emits `--permission-mode dontAsk` followed by `--settings`, and `TestBuildStreamRunnerClaudeArgs_Shape` pins that argv. `runAgentRunStreamRunner` writes the settings file from `--allowed-tools` through `settingsWrite`. The rig's `--allowed-tools=Bash` in `spawnProbePyry` therefore bounds the tool surface through that file, which is what the comments say.
- [File operations / session id] No findings. SITE G keeps the decision to add no guard on the claude-minted session id, which `parseInitSessionID` accepts without shape validation and `jsonlPathFor` joins into a path under the rig's temp HOME. That remains acceptable. The producer is the real claude CLI, so no attacker supplies the string. The read target sits inside the rig's own temp HOME. The rig reads the file and never writes it. No such failure has been observed, and the rule against guarding unobserved failure modes applies. Moving from a pyry-minted to a claude-minted id did not change the trust level: both producers are local processes the rig launched.
- [Subprocess execution] No findings. No argv, environment or exec call changes. The delta funcs' return literals are unchanged, and `TestFinLiveStageEnvDeltaNamesTheRunner` and `TestFinLiveStageStreamEnvDeltaNamesTheRunner` pin them.
- [Tokens, secrets, credentials] No findings. The passages name no credential and change no handling. The LEG 1 statement that claude's fd 2 is pyry's stderr describes existing plumbing (`Stderr: os.Stderr` in `runAgentRunStreamRunner`) and adds no new output route.
- [Cryptographic primitives], [Network & I/O], [Concurrency]: not applicable. The change touches no code, so it adds no RNG use, socket, lock or goroutine.
- [Error messages, logs] No findings. No log or error string changes.
- [Threat model alignment] OUT OF SCOPE. The production doc on `buildStreamRunnerClaudeArgs` still claims that yolo=true emits `--dangerously-skip-permissions`. It is the one stale posture statement left, and open ticket #1515 already tracks it. This ticket does not edit production files.

**Reviewer:** builder (self-review per the security-review checklist)
**Date:** 2026-09-23

## Revisions

- 2026-09-23 (rework after verifier FAIL on PR #2547): added the `## Security review` section above, which the `security-sensitive` label requires and which the first build omitted. The design did not change. Also rewrapped one over-long comment line in SITE B of `finLiveRunStage` (verifier NIT).
