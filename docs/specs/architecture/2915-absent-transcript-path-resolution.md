# #2915: Absent-transcript respawn path resolution

## Files read

- `internal/e2e/stream_absent_transcript_respawn_test.go` → `TestE2E_StreamNeverEstablishedSession_RespawnsWithCreateForm`: probe derivation and post-kill assertions.
- `internal/canonicalpath/path.go` → `Resolve`: canonical absolute path and resolution-error contract from merged #2911.
- `internal/agentrun/workdir.go` → `ResolveWorkdir`: matching legacy resolution semantics.
- `internal/sessions/reconcile.go` → `DefaultClaudeSessionsDir`: resolved workdir encoding under the isolated HOME.
- `cmd/pyry/streamsup_runner.go` → `streamClaudeSessionsDir`: daemon transcript probe derivation.
- `docs/knowledge/features/e2e-harness.md` → build-helper HOME ordering: build both binaries before overriding HOME and use uncached tagged execution.
- `docs/knowledge/features/canonicalpath-package.md` → consumer migration contract.
- `docs/knowledge/features/development-verification.md` → test execution: confirm the named test ran.

## Change

Replace the test's `agentrun` import with `canonicalpath` and call `canonicalpath.Resolve(home)` before `sessions.DefaultClaudeSessionsDir`. Preserve the error and nonempty-directory guards, absent-transcript fixture, binary-build ordering, and all post-kill assertions. No production behavior changes. One deliverable; approximately 30 written lines, zero new exported types, one consumer, two acceptance criteria, and no new reject branches. No feature branch overlaps the target file.

## Testing strategy

Use the existing named test as the behavior proof: after SIGKILL it requires exactly one restart, a live replacement PID, `--session-id`, and no `--resume`. Run its baseline and migrated versions with `go test -tags e2e -race -count=1 -v -run '^TestE2E_StreamNeverEstablishedSession_RespawnsWithCreateForm$' ./internal/e2e`. Run the touched e2e package with tags and race, `go vet ./...`, and build `./cmd/pyry` to a scratch path. The dispatcher runs `make check`, including the full-module suite, after the PR opens.
