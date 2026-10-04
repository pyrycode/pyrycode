# Synchronize prompt shutdown tests with pool readiness (#2765)

## Files read

- `internal/sessions/pool_system_prompt_test.go` → `TestPool_Run_RemovesSystemPromptFileAtShutdown`, `TestPool_Run_RemovesSessionPromptsAtShutdown`: existing file-presence and post-shutdown removal assertions.
- `internal/sessions/pool_mcp_settings_test.go` → `TestPool_Run_CleansUpBootstrapSettingsFile`: bootstrap settings lifetime assertion.
- `internal/sessions/pool_settings_test.go` → `recordingRunnerFactory`, `waitArgvRaw`: argv is captured during construction, before the pool runs.
- `internal/sessions/pool.go` → `Run`, `Ready`: readiness closes after supervisor setup; shutdown defers remove files before Run returns.
- `internal/sessions/pool_create_test.go` → `runPoolInBackground`: existing bounded readiness and cleanup pattern; these tests also need explicit shutdown before checking removal.
- `docs/knowledge/features/sessions-package-testing.md` § Pool readiness in runner fixtures: argv capture cannot establish readiness; cleanup must precede fatal assertions.
- `docs/knowledge/features/sessions-package-key-types-config-bootstrapevicted-pool-ready.md` → `Pool.Ready`: public startup gate.
- `CODING-STYLE.md` and `docs/knowledge/features/development-verification.md`: goroutine cleanup, race checks and verification boundaries.

## Change

In all three shutdown tests, wait on `Pool.Ready()` with a five-second timeout before minting or cancelling. Register cleanup immediately after launching Run to cancel and join with the existing fifteen-second shutdown budget, including on fatal assertions. Replace the single-consumer result channel with a closed completion channel so explicit shutdown and cleanup can both observe completion. Preserve the existing file-presence checks and removal checks after Run returns; argv remains only a way to obtain the bootstrap prompt path. No production code or readiness semantics change.

This is one deliverable: reliable shutdown assertions. Estimated written work is approximately 100 lines including this plan, with zero new exported types, zero production call sites, two acceptance criteria and no new state-machine rejection branches; all sizing limits hold. No other remote feature branch currently edits either target test file.

## Testing strategy

Run the existing session-prompt shutdown test under the race detector for 1000 repetitions before and after the change to expose the startup race and verify its removal. Run all three shutdown tests together, then `go test -race ./internal/sessions`, `go vet ./...` and `go build ./cmd/pyry` (placing the binary outside the worktree). The verifier owns the full-module gate. No documentation or live-test handoff is required.
