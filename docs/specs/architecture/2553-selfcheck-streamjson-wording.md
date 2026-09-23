# #2553 — self-check FAIL message and agent-run comments describe the stream-json spawn

Short plan: comment and operator-facing string edits only. No new type, state or failure mode; the spawn, its argv and the detector are unchanged.

## Files read

- `cmd/pyry/agent_run_selfcheck.go` → `writeSelfCheckFailMessage`, `runAgentRunSelfCheck` — FAIL text and doc comments still describe the PTY / ptyrunner path.
- `cmd/pyry/agent_run_selfcheck_test.go` → `TestRunAgentRunSelfCheck_FAIL` — requires the substring `"PTY"`; changes in lockstep.
- `cmd/pyry/agent_run.go` → `buildStreamRunnerClaudeArgs`, `runAgentRunStreamRunner` — docs claim yolo=true emits `--dangerously-skip-permissions`; the code passes no `PermissionArgs` on that branch, so `BuildClaudeArgs` emits `--permission-mode dontAsk`.
- `internal/agentrun/selfcheck/selfcheck.go` → `SelfCheckDenyDefault`, `Config.Env`, `selfCheckMaxTurns` — docs name `ptyrunner.Run`, `ptyrunner.Config.Env`, an interactive-TUI claude, and `sessions.NewID` (no longer called).
- `internal/agentrun/streamrunner/args.go` → `BuildClaudeArgs` — the argv the FAIL message should point at; always emits `--max-turns <MaxTurns>` with no omit-when-zero case and no validation.
- `internal/agentrun/streamrunner/runner.go` → `Config.Env` — appended to `os.Environ()` in the child.

## Change

- FAIL message: "What was tested" becomes a headless stream-json claude spawn (`--input-format/--output-format stream-json`) with the per-spawn deny-default settings file via `--settings <path> --permission-mode dontAsk`. "What to check" names `internal/agentrun/streamrunner/args.go`'s `BuildClaudeArgs` as the argv to compare. The `References:` lines stay verbatim.
- `runAgentRunSelfCheck` / `writeSelfCheckFailMessage` docs: describe the stream-json spawn via `streamrunner.Run` with argv from `streamrunner.BuildClaudeArgs` (the same function the dispatcher spawn uses, since #1348).
- `selfcheck.go`: `SelfCheckDenyDefault` composes trust + settings + `streamrunner.BuildClaudeArgs` + `streamrunner.Run` against a headless stream-json claude; `Config.Env` reaches the child via `streamrunner.Config.Env`; `selfCheckMaxTurns` replaces the ptyrunner clause with what is true: `BuildClaudeArgs` always emits `--max-turns` with this value (no omit-when-zero), so dropping the budget for the self-check is not available.
- `agent_run.go`: `buildStreamRunnerClaudeArgs` doc says yolo=true passes no permission args, so `BuildClaudeArgs` emits `--permission-mode dontAsk`; yolo=false emits the permission-prompt-tool pair. The flag-notes bullet on the skip flag and the "Without it there is NO tool boundary" comment keep the #1387 history in the past tense (the skip flag used to be on the argv and defeated `--allowed-tools`; `--permission-mode dontAsk` replaced it). The `agentRunUsageDescription` help text is untouched.

## Testing strategy

`TestRunAgentRunSelfCheck_FAIL` swaps its `"PTY"` required substring for `"stream-json"` and `"BuildClaudeArgs"`, and gains a forbidden-substring check that `PTY`, `interactive-TUI` and `ptyrunner/runner.go` do not appear in the FAIL output before the `References:` line. Gate: `go test -race ./cmd/pyry/ ./internal/agentrun/selfcheck/`, `go vet ./...`, `go build ./cmd/pyry`, plus the AC's `rg` over the three files.

## Documentation handoff

None — the ticket carries no documentation-only criteria.
